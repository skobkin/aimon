// Package app wires everything together: the Fyne application, the tray,
// the source manager, the config repository, and the two windows. The UI
// thread only ever calls non-blocking manager methods; every Apply runs
// either at startup (before the event loop) or on the Settings save
// goroutine.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"slices"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"fyne.io/fyne/v2"
	fyneapp "fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/systray"
	"github.com/go-gl/glfw/v3.4/glfw"

	"git.skobk.in/skobkin/aimonitor/internal/buildinfo"
	"git.skobk.in/skobkin/aimonitor/internal/config"
	"git.skobk.in/skobkin/aimonitor/internal/credentials"
	"git.skobk.in/skobkin/aimonitor/internal/domain"
	"git.skobk.in/skobkin/aimonitor/internal/pace"
	"git.skobk.in/skobkin/aimonitor/internal/registry"
	"git.skobk.in/skobkin/aimonitor/internal/resources"
	"git.skobk.in/skobkin/aimonitor/internal/source"
	"git.skobk.in/skobkin/aimonitor/internal/source/amdgpuweb"
	"git.skobk.in/skobkin/aimonitor/internal/ui/monitor"
	"git.skobk.in/skobkin/aimonitor/internal/ui/settings"
)

// appID is the single application identity (preferences path, tray,
// packaging all key off it).
const appID = "com.skobkin.aimonitor"

// keyringProbeTimeout bounds the startup keychain availability probe: the
// keychain gates credential features only, so a wedged backend delays the
// log line, never the event loop.
const keyringProbeTimeout = 5 * time.Second

// appMetadata is the runtime application identity. Nothing injects it from
// outside the binary — the version and name come from buildinfo, stamped
// at link time for tagged releases (see New).
func appMetadata() fyne.AppMetadata {
	return fyne.AppMetadata{
		ID:         appID,
		Name:       buildinfo.AppName,
		Version:    buildinfo.Version,
		Migrations: map[string]bool{"fyneDo": true},
	}
}

// App is the running application.
type App struct {
	fyneApp  fyne.App
	deskApp  desktop.App
	log      *slog.Logger
	client   *http.Client
	creds    credentials.Store
	repo     *config.Repository
	mgr      *source.Manager
	monitor  *monitor.Monitor
	settings *settings.Settings
	pace     *pace.Tracker

	// updateChecker performs release update checks; nil when construction
	// failed, in which case every entry point degrades to a log line or a
	// diagnostic dialog.
	updateChecker updateChecker
	// checkingUpdates guards one manual update check at a time.
	checkingUpdates atomic.Bool
	// updateItem is the tray's "Check for updates…" item, swapped whenever
	// the menu is rebuilt; nil on off-desktop runs.
	updateItem atomic.Pointer[systray.MenuItem]

	// loadErr records a failed document load from New; Run surfaces it.
	// It is written once before any reader exists.
	loadErr error

	done    chan struct{}
	running atomic.Bool
	unsub   func()
	trayEnd func()

	// uiUpdate collapses bursts of state-change notifications into at most
	// one pending monitor re-render: N sources finishing a poll within the
	// same dispatch window must cost one rebuild, not N.
	uiUpdate uiCoalescer

	mu        sync.RWMutex // guards instances and the adapter cache
	instances []config.SourceInstance
	adapters  map[domain.InstanceID]adapterEntry
	// paceWindows mirrors the pace estimation windows the tracker was last
	// told about, so a pace-window-only change can be detected without
	// querying the tracker.
	paceWindows map[domain.InstanceID]time.Duration

	// applyMu serializes applyInstances against itself. It must stay
	// separate from mu: Manager.Apply waits for source-loop teardown whose
	// completion callback takes mu (see applyInstances).
	applyMu sync.Mutex
}

// uiCoalescer lets one UI update wait while others arrive. schedule
// dispatches update exactly once per generation: calls while an update is
// pending are absorbed by it, and the flag clears when the dispatched
// update runs, so a notification arriving during that update schedules a
// fresh one. dispatch must run update on the UI thread (fyne.Do).
type uiCoalescer struct {
	pending atomic.Bool
}

func (c *uiCoalescer) schedule(dispatch func(func()), update func()) {
	if c.pending.Swap(true) {
		return
	}
	dispatch(func() {
		c.pending.Store(false)
		update()
	})
}

// adapterEntry caches one instance's built adapter together with the
// provider settings it was built from. Settings equality drives rebuilds:
// rename, reorder and enable/disable reuse the adapter (and its poller),
// while a material settings change builds a new one.
type adapterEntry struct {
	settings config.ProviderSettings
	src      source.Source
}

// New creates the application (windows built, sources not yet started).
// A failed configuration load is recorded, not fatal: Run reports it and
// continues with zero sources, leaving the stored document untouched.
func New() (*App, error) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	fa := fyneapp.NewWithID(appID)
	// Nothing injects the metadata from outside, so it is set here: without
	// it the fyne.Do migration flag and the version would be missing and
	// every start would log the not-migrated warning.
	fyneapp.SetMetadata(appMetadata())
	fa.SetIcon(resources.Icon())

	repo := config.NewRepository(fa.Preferences())
	instances, loadErr := repo.LoadInstances()

	creds := credentials.NewKeyringStore()
	client := &http.Client{}
	mgr := source.NewManager(logger)

	a := &App{
		fyneApp:     fa,
		log:         logger,
		client:      client,
		creds:       creds,
		repo:        repo,
		mgr:         mgr,
		instances:   instances,
		adapters:    map[domain.InstanceID]adapterEntry{},
		pace:        pace.NewTracker(),
		paceWindows: map[domain.InstanceID]time.Duration{},
		loadErr:     loadErr,
		done:        make(chan struct{}),
	}
	// From here on the app and every consumer (adapters, Settings) share
	// the guarded store: a credential change must reset the owning
	// instance's pace history (see paceGuardStore).
	a.creds = newPaceGuardStore(creds, a.paceCredentialChanged)
	a.running.Store(true)

	a.monitor = monitor.NewMonitor(fa, a, a.done)
	a.settings = settings.NewSettings(fa, settings.Deps{
		LoadInstances:        a.Instances,
		SaveInstances:        a.SaveInstances,
		CleanupCredentials:   a.cleanupCredentials,
		Creds:                a.creds,
		TestConnection:       a.testConnection,
		HideSensitiveInfo:    a.HideSensitiveInfo,
		SetHideSensitiveInfo: a.SetHideSensitiveInfo,

		CheckUpdatesAutomatically:    a.CheckUpdatesAutomatically,
		SetCheckUpdatesAutomatically: a.SetCheckUpdatesAutomatically,
		DiscoverGPUs: func(ctx context.Context, baseURL string) ([]domain.GPUInfo, error) {
			return amdgpuweb.New(baseURL, "", client, logger).Discover(ctx)
		},
		Done: a.done,
	})

	// Wayland has no per-window icon: the compositor draws the taskbar
	// entry from the xdg-toplevel app-id, resolved to a .desktop entry and
	// its Icon=. Fyne 2.8's glfw driver never sets an app-id (no
	// WindowHintString call anywhere in the driver), so GLFW passes an
	// empty appId to xdg_toplevel_set_app_id — which it then skips — and
	// Plasma cannot match the window to any desktop entry, leaving a blank
	// taskbar entry. Window.SetIcon is also a Wayland no-op, so there is no
	// fallback there. GLFW only forwards this hint to the created toplevel,
	// and it asserts that glfw.Init() has already run: the hint must be set
	// after Fyne's lazy init (the first window construction above triggers
	// it) and before the first Show() actually creates the GLFW window.
	// Fyne never calls DefaultWindowHints nor resets string hints, so both
	// windows built below inherit it. The hint is silently ignored on X11
	// and the other platforms.
	glfw.WindowHintString(glfw.WaylandAppID, appID)

	a.setupUpdateChecker()
	a.setupTray()
	a.unsub = a.mgr.Subscribe(func(id domain.InstanceID) {
		if !a.running.Load() {
			return
		}
		a.recordPace(id)
		a.uiUpdate.schedule(fyne.Do, a.monitor.OnStatesChanged)
	})
	a.monitor.StartTicker()

	return a, nil
}

// Run starts the configured sources and the event loop, then joins the
// source goroutines after the loop returns.
func (a *App) Run() {
	a.log.Info("starting", "app", appID, "version", a.fyneApp.Metadata().Version, "build_date", buildinfo.BuildDate)

	// Fyne's own SIGINT/SIGTERM handling quits only the event loop; with
	// no window ever focused it also skips unregistering the tray icon,
	// and Run would then block forever in Wait with the pollers still
	// running — an invisible process holding a dead tray entry. Route
	// termination signals through the same graceful path as tray Quit.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		a.quit()
	}()

	// Keychain state changes are Info-worthy (they gate credential
	// features); the probe never touches a real account. It is bounded:
	// go-keyring's Secret Service binding can block indefinitely on a
	// wedged dbus daemon, and startup must not hang on it.
	probeCtx, cancelProbe := context.WithTimeout(context.Background(), keyringProbeTimeout)
	st := a.creds.Status(probeCtx)
	cancelProbe()
	if st.State != credentials.StateAvailable {
		a.log.Warn("keychain not available", "state", string(st.State), "backend", st.Backend, "message", st.Message)
	}

	// A failed load is surfaced, never papered over: the stored document
	// stays untouched and the app runs with zero sources.
	if a.loadErr != nil {
		a.log.Error("loading source configuration failed; continuing with no sources", "error", a.loadErr)
		a.fyneApp.SendNotification(fyne.NewNotification("AI Monitor",
			"Failed to load source configuration; see the log. The stored configuration was left untouched."))
	}

	// Apply before the event loop exists: no contention with the UI.
	a.applyInstances(a.Instances())
	// First run (no document ever saved): open Settings once the loop is
	// up, so the app is not just a tray icon with nothing configured.
	if !a.repo.HasDocument() {
		a.fyneApp.Lifecycle().SetOnStarted(a.settings.Show)
	}
	a.startUpdateChecks()
	a.fyneApp.Run()
	a.mgr.Wait()
	a.log.Info("stopped")
}

// setupTray installs the tray icon and menu. Without a desktop driver
// (off-desktop run) the monitor window shows at startup instead.
//
// The native systray is managed directly via the systray package rather
// than through Fyne's SetSystemTrayMenu / SetSystemTrayWindow.  Fyne's
// internal runSystray creates a real "SystrayMonitor" GLFW window (used
// as a quit-detection sentinel); on Wayland the window is created with
// default visibility and Plasma adds it to the taskbar as a spurious
// empty entry.  Building the menu ourselves avoids that window entirely
// while keeping the same behaviour (left-click toggles the monitor,
// right-click shows the context menu).
func (a *App) setupTray() {
	desk, ok := a.fyneApp.(desktop.App)
	if !ok {
		a.monitor.Show()

		return
	}
	a.deskApp = desk

	// Left-click on the tray icon toggles the splash monitor window
	// (show if hidden, hide if shown).
	//
	// This must be registered before the tray is published. systray
	// snapshots ItemIsMenu from the tapped callbacks while exporting its
	// DBus properties, concurrently with the onReady callback below, and
	// never updates the snapshot. Registering here instead of in onReady
	// makes ItemIsMenu deterministically false; losing that race used to
	// register the item as menu-only, which makes Plasma open the context
	// menu on left-click instead of calling Activate.
	systray.SetOnTapped(func() {
		fyne.Do(a.monitor.ToggleVisible)
	})

	start, end := systray.RunWithExternalLoop(func() {
		// SetSystemTrayIcon handles SVG→PNG conversion and calls
		// systray.SetIcon internally.
		desk.SetSystemTrayIcon(resources.Icon())
		systray.SetTooltip("AI Monitor")

		// Some XDG systray implementations crash without a title.
		title := a.fyneApp.Metadata().Name
		if title == "" {
			title = a.fyneApp.UniqueID()
		}
		systray.SetTitle(title)

		a.rebuildTrayMenu()
	}, func() {
		// tear-down is handled by the end function (nativeEnd + Quit).
	})
	start()
	a.trayEnd = end
}

// rebuildTrayMenu tears down and rebuilds the systray context menu.
// Safe to call from any goroutine (systray is internally synchronized).
func (a *App) rebuildTrayMenu() {
	systray.ResetMenu()

	mShow := systray.AddMenuItem("Show Monitor", "")
	mRefresh := systray.AddMenuItem("Refresh", "")

	// Open submenu lists every configured instance's web UI — including
	// disabled ones: navigation stays useful independently of polling.
	var mOpen *systray.MenuItem
	if links := registry.WebLinks(a.Instances()); len(links) > 0 {
		mOpen = systray.AddMenuItem("Web UIs", "")
		for _, l := range links {
			link := l
			sub := mOpen.AddSubMenuItem(link.Name, "")
			go func() {
				for range sub.ClickedCh {
					if u, err := url.Parse(link.URL); err == nil && u.Scheme != "" {
						if err := a.fyneApp.OpenURL(u); err != nil {
							a.log.Warn("opening web UI failed", "link", link.Name, "error", err)
						}
					}
				}
			}()
		}
	}

	mSettings := systray.AddMenuItem("Settings", "")
	// A manual check may be in flight when the menu is rebuilt (every config
	// save rebuilds it). The item is stored before the busy flag is read: a
	// completing check re-enables whichever item the pointer holds at its
	// enable moment, so reading the flag first could publish a disabled item
	// whose enabler had already left for the previous one and strand it
	// disabled while idle. A click slipping through the store-to-disable
	// window is rejected by the checkForUpdates CAS guard.
	mUpdates := systray.AddMenuItem("Check for updates…", "")
	a.updateItem.Store(mUpdates)
	if a.checkingUpdates.Load() {
		mUpdates.Disable()
	}
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Quit", "")

	go func() {
		for range mShow.ClickedCh {
			fyne.Do(a.monitor.Show)
		}
	}()
	go func() {
		for range mRefresh.ClickedCh {
			a.mgr.RefreshAll()
		}
	}()
	go func() {
		for range mSettings.ClickedCh {
			fyne.Do(a.settings.Show)
		}
	}()
	go func() {
		for range mUpdates.ClickedCh {
			fyne.Do(a.checkForUpdates)
		}
	}()
	go func() {
		for range mQuit.ClickedCh {
			a.quit()
		}
	}()
}

// Instances implements monitor.MonitorController: the authoritative
// configured instance list, in persisted order. The copy is deliberate —
// callers must not mutate the app's list.
func (a *App) Instances() []config.SourceInstance {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make([]config.SourceInstance, len(a.instances))
	copy(out, a.instances)

	return out
}

// States implements monitor.MonitorController.
func (a *App) States() map[domain.InstanceID]domain.Runtime { return a.mgr.States() }

// PaceStates implements monitor.MonitorController: the derived usage-pace
// estimates of the pace-capable sources (S7).
func (a *App) PaceStates() map[domain.InstanceID]pace.InstancePaces { return a.pace.States() }

// HideSensitiveInfo implements monitor.MonitorController: the global
// privacy preference, read straight from the repository.
func (a *App) HideSensitiveInfo() bool { return a.repo.HideSensitiveInfo() }

// recordPace feeds one completed refresh into the pace tracker. It runs on
// the manager's loop goroutine before the UI update is scheduled; the
// tracker is mutex-guarded and touches no Fyne. Failure notifications
// carry the retained last-good snapshot: re-recording it would fabricate
// a flat interval (a false idle), so they are skipped and the next
// successful poll continues the series. Sample times always come from the
// snapshot's CollectedAt, never from receive time.
func (a *App) recordPace(id domain.InstanceID) {
	inst, ok := a.instanceByID(id)
	if !ok {
		return
	}
	rt, ok := a.mgr.States()[id]
	if !ok {
		return
	}
	a.recordPaceSnapshot(id, inst, rt)
}

// recordPaceSnapshot applies one runtime to the tracker: the kind-aware
// half of recordPace, separate so tests can drive it without a live
// manager refresh.
func (a *App) recordPaceSnapshot(id domain.InstanceID, inst config.SourceInstance, rt domain.Runtime) {
	if !config.PaceCapable(inst.Kind) {
		return
	}
	if rt.LastError != nil || rt.Snapshot == nil {
		return
	}
	switch snap := rt.Snapshot.(type) {
	case domain.QuotaSnapshot:
		a.pace.ObserveQuota(id, snap.CollectedAt, snap.Windows)
	case domain.CodexSnapshot:
		a.pace.ObserveQuota(id, snap.CollectedAt, snap.Windows)
	case domain.OpenRouterSnapshot:
		if snap.Key != nil {
			a.pace.ObserveSpend(id, snap.CollectedAt, snap.Key.UsageTotal)
		}
	}
}

// instanceByID resolves one configured instance under the instances lock.
func (a *App) instanceByID(id domain.InstanceID) (config.SourceInstance, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, inst := range a.instances {
		if inst.ID == id {
			return inst, true
		}
	}

	return config.SourceInstance{}, false
}

// SetHideSensitiveInfo persists the privacy preference and re-renders the
// monitor so visible sensitive values follow it immediately. It is called
// only from the Settings window's check callback on the UI thread, so the
// re-render needs no fyne.Do hop.
func (a *App) SetHideSensitiveInfo(hide bool) {
	a.repo.SetHideSensitiveInfo(hide)
	a.monitor.OnStatesChanged()
}

// RefreshAll implements monitor.MonitorController.
func (a *App) RefreshAll() { a.mgr.RefreshAll() }

// OpenSettings implements monitor.MonitorController.
func (a *App) OpenSettings() { a.settings.Show() }

// SaveInstances is the single mutation path for the configured sources:
// the candidate list is persisted first, and only a successful persist
// replaces the authoritative in-memory list and reconfigures the manager
// and the tray. On failure nothing changes — the caller keeps the previous
// runtime and configuration. It runs on a background goroutine, never on
// the UI thread (Apply may wait out a request timeout). A save racing Quit
// still persists, but skips reconfiguring an already-cancelled manager and
// poking the tray through a dying event loop.
func (a *App) SaveInstances(list []config.SourceInstance) error {
	if err := a.repo.SaveInstances(list); err != nil {
		return err
	}
	if !a.running.Load() {
		return nil
	}
	a.applyInstances(list)
	// The tray's Web UIs submenu follows the configured instance set.
	if a.deskApp != nil {
		a.rebuildTrayMenu()
	}

	return nil
}

// appVersion reports the running application version for source adapters
// that identify themselves to their provider (the Codex CLI JSON-RPC
// clientInfo). An unavailable version degrades to an empty string rather
// than blocking adapter construction.
func (a *App) appVersion() string {
	if a.fyneApp == nil {
		return ""
	}

	return a.fyneApp.Metadata().Version
}

// applyInstances reconfigures the manager for the given list, keyed by
// instance ID. An adapter is rebuilt only when its provider settings
// changed (settings equality); rename, reorder and enable/disable reuse
// the cached adapter, so their manager registration is unchanged and the
// poller is never restarted. Instances that cannot be built are logged and
// skipped — the persisted list stays authoritative for the UI.
//
// The instances lock is never held across Manager.Apply: Apply waits for
// the cancelled source loops to exit, and a loop's completion callback
// (recordPace → instanceByID) takes that same lock — holding it across
// Apply would deadlock a settings save against the very poll it
// reconfigures, and freeze every other lock reader (the UI rebuilds) for
// the teardown's duration. Applies are serialized against each other with
// a separate mutex; concurrent readers only ever see the state before or
// after a whole apply.
func (a *App) applyInstances(list []config.SourceInstance) {
	a.applyMu.Lock()
	defer a.applyMu.Unlock()

	a.mu.Lock()
	regs, stalePace := a.applyInstancesLocked(list)
	a.mu.Unlock()

	a.mgr.Apply(regs)

	if rePace := a.settlePace(list, stalePace); rePace && a.running.Load() {
		a.uiUpdate.schedule(fyne.Do, a.monitor.OnStatesChanged)
	}
}

// settlePace is the post-Apply half of applyInstances: it drops the stale
// pace histories and syncs the estimation windows, reporting whether a
// user-visible pace change needs a re-render. It must run only after
// Manager.Apply has returned: until Apply has waited out a replaced loop,
// that loop can still deliver its final completion callback, whose
// recordPace reads the old runtime — a history cleared earlier would let
// that stale reading repopulate the fresh series, and its first sample
// would be estimated across the adapter boundary. Apply waits for every
// stopped loop's done, which closes only after the loop's last callback,
// so anything recorded between the rebuild and this point is wiped here;
// afterwards only the new adapters feed the tracker.
func (a *App) settlePace(list []config.SourceInstance, stalePace []domain.InstanceID) bool {
	rePace := false
	a.mu.Lock()
	defer a.mu.Unlock()

	for _, id := range stalePace {
		if a.dropPaceHistoryLocked(id) {
			rePace = true
		}
	}

	// Pace estimation windows apply without touching adapters or history:
	// estimates are computed at read time, so a changed window re-renders
	// and takes effect immediately. Interval-only, rename and
	// enable/disable edits land here with an unchanged window and do not
	// even re-render.
	for _, inst := range list {
		if !config.PaceCapable(inst.Kind) {
			continue
		}
		if prev, known := a.paceWindows[inst.ID]; known && prev == inst.PaceWindow {
			continue
		}
		a.pace.SetWindow(inst.ID, inst.PaceWindow)
		a.paceWindows[inst.ID] = inst.PaceWindow
		rePace = true
	}
	// The window mirror tracks the configured instances, not the
	// registrations: a build failure leaves an instance configured but
	// unregistered (Apply detaches its loop without a replacement), and
	// the window dropPaceHistoryLocked just restored for it must survive
	// here — pruning by registration would forget it, and the next save
	// would then re-announce a window nothing visible lost.
	for id := range a.paceWindows {
		kept := false
		for _, inst := range list {
			if inst.ID == id {
				kept = true

				break
			}
		}
		if !kept {
			delete(a.paceWindows, id)
		}
	}

	return rePace
}

// dropPaceHistoryLocked clears one instance's pace history and re-applies
// its configured estimation window (Reset drops the tracker's stored
// window together with the samples). It reports whether the instance is
// configured and pace-capable, i.e. whether the drop is user-visible.
// Callers hold mu.
func (a *App) dropPaceHistoryLocked(id domain.InstanceID) bool {
	a.pace.Reset(id)
	delete(a.paceWindows, id)
	for _, inst := range a.instances {
		if inst.ID != id {
			continue
		}
		if !config.PaceCapable(inst.Kind) {
			return false
		}
		a.pace.SetWindow(id, inst.PaceWindow)
		a.paceWindows[id] = inst.PaceWindow

		return true
	}

	return false
}

// applyInstancesLocked is the lock-guarded half of applyInstances: it
// rebuilds adapters and decides which pace histories are stale. It returns
// the registrations for Manager.Apply and the instance IDs whose pace
// history must drop — the drops themselves run in settlePace, only after
// Apply has waited out the replaced loops (see settlePace).
func (a *App) applyInstancesLocked(list []config.SourceInstance) (map[domain.InstanceID]source.Registration, []domain.InstanceID) {
	regs := make(map[domain.InstanceID]source.Registration, len(list))
	live := make(map[domain.InstanceID]adapterEntry, len(list))
	stalePace := []domain.InstanceID(nil)
	deps := registry.Deps{Creds: a.creds, Client: a.client, Log: a.log, AppVersion: a.appVersion()}
	for _, inst := range list {
		def, ok := registry.Get(inst.Kind)
		if !ok {
			a.log.Error("unknown source kind; instance skipped", "kind", string(inst.Kind), "instance", string(inst.ID))

			continue
		}
		settings, ok := inst.Settings()
		if !ok {
			a.log.Error("instance settings missing or mismatched; instance skipped", "kind", string(inst.Kind), "instance", string(inst.ID))

			continue
		}
		entry, cached := a.adapters[inst.ID]
		if !cached || !config.SameSettings(entry.settings, settings) {
			// A new adapter means a new upstream identity: the pace history
			// of the old one must never bleed into the new series (base-URL
			// change, credential change, …). The drop itself is deferred to
			// settlePace, after Manager.Apply has waited out the old loop.
			stalePace = append(stalePace, inst.ID)
			src, err := def.Build(inst, deps)
			if err != nil {
				a.log.Error("building source instance failed; instance skipped", "instance", string(inst.ID), "error", err)

				continue
			}
			entry = adapterEntry{settings: settings, src: src}
		}
		live[inst.ID] = entry
		regs[inst.ID] = source.Registration{
			Kind:     inst.Kind,
			Source:   entry.src,
			Enabled:  inst.Enabled,
			Interval: inst.PollInterval,
		}
	}

	// Instances that disappeared (or failed to build) lose their pace
	// history with their adapter. A stayed-but-failed rebuild is already
	// claimed above — its Build runs after the claim — so skip it here:
	// a second entry would drop and re-announce the same series twice.
	for id := range a.adapters {
		if _, ok := live[id]; !ok && !slices.Contains(stalePace, id) {
			stalePace = append(stalePace, id)
		}
	}
	a.adapters = live
	a.instances = list

	return regs, stalePace
}

// quit is the single shutdown path (tray Quit). It is idempotent: the
// CAS gate makes re-entry a no-op. Ordering matters: unsubscribe manager
// notifications, stop timers, cancel source work, then quit the loop;
// Run joins the source goroutines after the loop returns.
func (a *App) quit() {
	if !a.running.CompareAndSwap(true, false) {
		return
	}
	a.log.Info("quitting")
	a.unsub()
	close(a.done)
	a.mgr.Cancel()
	// Stopped before the event loop quits so the auto-update handler's final
	// fyne.Do dispatch is still serviced; Stop is bounded — cancelling the
	// context aborts any in-flight check request immediately.
	if a.updateChecker != nil {
		a.updateChecker.Stop()
	}
	if a.trayEnd != nil {
		a.trayEnd()
	}
	a.fyneApp.Quit()
}

// testerStore returns a credential store that answers Get for one account
// with the typed override (when non-empty) and delegates everything else
// to the real keychain, so Settings tests reuse the real adapters.
func (a *App) testerStore(overrides map[credentials.AccountID]string) credentials.Store {
	return &overrideStore{inner: a.creds, overrides: overrides}
}

// testConnection runs one instance's kind test with the editor's unsaved
// values. Typed-but-unsaved secrets ride along through the override store,
// keyed per slot so a multi-credential kind tests each capability exactly
// as Save would; nothing persists.
func (a *App) testConnection(ctx context.Context, inst config.SourceInstance, typed map[credentials.Slot]string) registry.TestReport {
	def, ok := registry.Get(inst.Kind)
	if !ok {
		return registry.TestReport{
			Lines: []string{fmt.Sprintf("Failed: unknown source kind %q", inst.Kind)},
		}
	}
	overrides := make(map[credentials.AccountID]string, len(typed))
	for _, slot := range def.CredentialSlots {
		secret := typed[slot]
		if secret == "" {
			continue
		}
		account, err := credentials.AccountForInstance(inst.ID, slot)
		if err != nil {
			return registry.TestReport{Lines: []string{"Failed: " + err.Error()}}
		}
		overrides[account] = secret
	}
	deps := registry.Deps{Creds: a.testerStore(overrides), Client: a.client, Log: a.log, AppVersion: a.appVersion()}

	return def.Test(ctx, inst, deps)
}

// cleanupCredentials best-effort deletes every credential slot of a
// deleted instance. Missing entries already satisfy the request; anything
// else is reported to the caller (as a warning — the deletion stands).
func (a *App) cleanupCredentials(ctx context.Context, inst config.SourceInstance) error {
	def, ok := registry.Get(inst.Kind)
	if !ok || len(def.CredentialSlots) == 0 {
		return nil
	}
	var errs []error
	for _, slot := range def.CredentialSlots {
		account, err := credentials.AccountForInstance(inst.ID, slot)
		if err != nil {
			errs = append(errs, err)

			continue
		}
		if err := a.creds.Delete(ctx, account); err != nil && !errors.Is(err, credentials.ErrNotFound) {
			errs = append(errs, fmt.Errorf("%s: %w", slot, err))
		}
	}

	return errors.Join(errs...)
}

// paceGuardStore wraps the credential store so a changed secret can reset
// the owning instance's pace history. Provider settings — and therefore
// adapter identity — never include secrets: swapping a key in Settings
// reuses the running adapter, and the adapter picks the new value up from
// the keychain on its next refresh, so nothing else tells the pace tracker
// that its series now measures a different upstream counter (S7: spend
// pace is per-key usage; quota kinds are likewise per account). The old
// value is compared so re-saving an unchanged secret keeps the history;
// an unreadable old value (locked keychain) counts as changed — dropping
// history is the safe direction. The compared values live only for that
// comparison: they are never stored, logged, or persisted.
type paceGuardStore struct {
	inner   credentials.Store
	changed func(id credentials.AccountID, oldSecret, newSecret string)
}

func newPaceGuardStore(store credentials.Store, changed func(id credentials.AccountID, oldSecret, newSecret string)) *paceGuardStore {
	return &paceGuardStore{inner: store, changed: changed}
}

func (s *paceGuardStore) Status(ctx context.Context) credentials.Status { return s.inner.Status(ctx) }

func (s *paceGuardStore) Get(ctx context.Context, id credentials.AccountID) (string, error) {
	return s.inner.Get(ctx, id)
}

func (s *paceGuardStore) Set(ctx context.Context, id credentials.AccountID, secret string) error {
	old, _ := s.inner.Get(ctx, id) // best effort: an unreadable old value counts as changed
	if err := s.inner.Set(ctx, id, secret); err != nil {
		return err
	}
	s.changed(id, old, secret)

	return nil
}

func (s *paceGuardStore) Delete(ctx context.Context, id credentials.AccountID) error {
	old, _ := s.inner.Get(ctx, id)
	if err := s.inner.Delete(ctx, id); err != nil {
		return err
	}
	s.changed(id, old, "")

	return nil
}

// paceCredentialChanged reacts to one credential account changing value:
// the owning instance's pace series measured the old credential's upstream
// counter, so its history drops even though the adapter — whose identity
// is the unchanged provider settings — keeps running. Only the api-key
// slot reacts: every pace feed is measured with that credential (quota
// snapshots, Codex usage, the OpenRouter spend total), while OpenRouter's
// management key feeds the credit balance the pace never reads. It runs on
// whatever goroutine wrote the secret (the Settings save path), never on
// the UI thread.
func (a *App) paceCredentialChanged(account credentials.AccountID, oldSecret, newSecret string) {
	if oldSecret == newSecret {
		return // re-saving the same secret changes nothing upstream
	}
	inst, slot, ok := a.instanceForAccount(account)
	if !ok || slot != credentials.SlotAPIKey {
		return // not a pace-measured credential of a known instance
	}
	a.mu.Lock()
	a.dropPaceHistoryLocked(inst.ID)
	a.mu.Unlock()
	if a.running.Load() {
		a.uiUpdate.schedule(fyne.Do, a.monitor.OnStatesChanged)
	}
}

// instanceForAccount resolves which configured instance owns a credential
// account, and through which slot, via the registry's per-kind slot lists.
func (a *App) instanceForAccount(account credentials.AccountID) (config.SourceInstance, credentials.Slot, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, inst := range a.instances {
		def, ok := registry.Get(inst.Kind)
		if !ok {
			continue
		}
		for _, slot := range def.CredentialSlots {
			if acc, err := credentials.AccountForInstance(inst.ID, slot); err == nil && acc == account {
				return inst, slot, true
			}
		}
	}

	return config.SourceInstance{}, "", false
}

// overrideStore layers typed-in credentials over a backing store.
type overrideStore struct {
	inner     credentials.Store
	overrides map[credentials.AccountID]string
}

func (o *overrideStore) Status(ctx context.Context) credentials.Status { return o.inner.Status(ctx) }

func (o *overrideStore) Get(ctx context.Context, id credentials.AccountID) (string, error) {
	if secret, ok := o.overrides[id]; ok && secret != "" {
		return secret, nil
	}

	return o.inner.Get(ctx, id)
}

func (o *overrideStore) Set(ctx context.Context, id credentials.AccountID, secret string) error {
	return o.inner.Set(ctx, id, secret)
}

func (o *overrideStore) Delete(ctx context.Context, id credentials.AccountID) error {
	return o.inner.Delete(ctx, id)
}
