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
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"fyne.io/fyne/v2"
	fyneapp "fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/systray"

	"git.skobk.in/skobkin/aimonitor/internal/config"
	"git.skobk.in/skobkin/aimonitor/internal/credentials"
	"git.skobk.in/skobkin/aimonitor/internal/domain"
	"git.skobk.in/skobkin/aimonitor/internal/registry"
	"git.skobk.in/skobkin/aimonitor/internal/resources"
	"git.skobk.in/skobkin/aimonitor/internal/source"
	"git.skobk.in/skobkin/aimonitor/internal/source/amdgpuweb"
	"git.skobk.in/skobkin/aimonitor/internal/ui/monitor"
	"git.skobk.in/skobkin/aimonitor/internal/ui/settings"
)

// appID is the single application identity, identical to FyneApp.toml
// (preferences path, tray, packaging all key off it).
const appID = "com.skobkin.aimonitor"

// keyringProbeTimeout bounds the startup keychain availability probe: the
// keychain gates credential features only, so a wedged backend delays the
// log line, never the event loop.
const keyringProbeTimeout = 5 * time.Second

// appMetadata mirrors FyneApp.toml for runs without packaged metadata
// (see New). Keep the values in sync with the toml; a test guards the
// duplication.
func appMetadata() fyne.AppMetadata {
	return fyne.AppMetadata{
		ID:         appID,
		Name:       "AI Monitor",
		Version:    "0.4.0",
		Build:      4,
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
	// Packaged builds get their metadata injected by the fyne tool; every
	// other run (go run, an installed binary) finds no FyneApp.toml next
	// to the executable, so the fyne.Do migration flag and the version
	// would be missing and every start would log the not-migrated warning.
	fyneapp.SetMetadata(appMetadata())
	fa.SetIcon(resources.Icon())

	repo := config.NewRepository(fa.Preferences())
	instances, loadErr := repo.LoadInstances()

	creds := credentials.NewKeyringStore()
	client := &http.Client{}
	mgr := source.NewManager(logger)

	a := &App{
		fyneApp:   fa,
		log:       logger,
		client:    client,
		creds:     creds,
		repo:      repo,
		mgr:       mgr,
		instances: instances,
		adapters:  map[domain.InstanceID]adapterEntry{},
		loadErr:   loadErr,
		done:      make(chan struct{}),
	}
	a.running.Store(true)

	a.monitor = monitor.NewMonitor(fa, a, a.done)
	a.settings = settings.NewSettings(fa, settings.Deps{
		LoadInstances:        a.Instances,
		SaveInstances:        a.SaveInstances,
		CleanupCredentials:   a.cleanupCredentials,
		Creds:                creds,
		TestConnection:       a.testConnection,
		HideSensitiveInfo:    a.HideSensitiveInfo,
		SetHideSensitiveInfo: a.SetHideSensitiveInfo,
		DiscoverGPUs: func(ctx context.Context, baseURL string) ([]domain.GPUInfo, error) {
			return amdgpuweb.New(baseURL, "", client, logger).Discover(ctx)
		},
		Done: a.done,
	})

	a.setupTray()
	a.unsub = a.mgr.Subscribe(func(domain.InstanceID) {
		if !a.running.Load() {
			return
		}
		a.uiUpdate.schedule(fyne.Do, a.monitor.OnStatesChanged)
	})
	a.monitor.StartTicker()
	return a, nil
}

// Run starts the configured sources and the event loop, then joins the
// source goroutines after the loop returns.
func (a *App) Run() {
	a.log.Info("starting", "app", appID, "version", a.fyneApp.Metadata().Version)

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

		// Left-click on the tray icon toggles the splash monitor window
		// (show if hidden, hide if shown).
		systray.SetOnTapped(func() {
			fyne.Do(a.monitor.ToggleVisible)
		})

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
						a.fyneApp.OpenURL(u)
					}
				}
			}()
		}
	}

	mSettings := systray.AddMenuItem("Settings", "")
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

// HideSensitiveInfo implements monitor.MonitorController: the global
// privacy preference, read straight from the repository.
func (a *App) HideSensitiveInfo() bool { return a.repo.HideSensitiveInfo() }

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

// applyInstances reconfigures the manager for the given list, keyed by
// instance ID. An adapter is rebuilt only when its provider settings
// changed (settings equality); rename, reorder and enable/disable reuse
// the cached adapter, so their manager registration is unchanged and the
// poller is never restarted. Instances that cannot be built are logged and
// skipped — the persisted list stays authoritative for the UI.
func (a *App) applyInstances(list []config.SourceInstance) {
	a.mu.Lock()
	defer a.mu.Unlock()

	regs := make(map[domain.InstanceID]source.Registration, len(list))
	live := make(map[domain.InstanceID]adapterEntry, len(list))
	deps := registry.Deps{Creds: a.creds, Client: a.client, Log: a.log}
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

	a.adapters = live
	a.mgr.Apply(regs)
	a.instances = list
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
	deps := registry.Deps{Creds: a.testerStore(overrides), Client: a.client, Log: a.log}
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
