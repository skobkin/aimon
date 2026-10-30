// Package settings implements the Settings window: a CRUD list of the
// configured source instances grouped Local/Remote, with an Add/Edit
// editor dialog, plus a General tab for the application-wide settings
// (privacy masking, keychain status, update checks). All network and
// keychain work runs in background goroutines; the UI thread only mutates
// widgets, and every background result crosses back through fyne.Do.
// Configuration mutations all pass through one guarded path (persist
// first, apply only on success), and at most one is in flight at a time.
package settings

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"io"
	"net/url"
	"path/filepath"
	"sync/atomic"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"git.skobk.in/skobkin/aimonitor/internal/buildinfo"
	"git.skobk.in/skobkin/aimonitor/internal/config"
	"git.skobk.in/skobkin/aimonitor/internal/credentials"
	"git.skobk.in/skobkin/aimonitor/internal/domain"
	"git.skobk.in/skobkin/aimonitor/internal/registry"
	"git.skobk.in/skobkin/aimonitor/internal/resources"
)

// testTimeout bounds one connection test, credential probe, or credential
// cleanup.
const testTimeout = 10 * time.Second

// codexTestTimeout is the longer Settings-side budget for one Codex
// connection test: a probe spans a process spawn, the JSON-RPC initialize,
// a live read, and the reap, so the shared 10s budget cannot cover it. The
// adapter's internal stage timeouts only shorten this; nothing may extend a
// context beyond the deadline chosen here (docs/S6_UI_UX.md §Test
// Connection).
const codexTestTimeout = 35 * time.Second

// Deps is everything Settings needs from the app.
type Deps struct {
	// LoadInstances returns the authoritative configured instance list.
	LoadInstances func() []config.SourceInstance
	// SaveInstances persists a candidate list and reconfigures the
	// runtime. It may block (manager Apply); it runs on a background
	// goroutine. On error nothing was persisted and the previous list
	// remains authoritative.
	SaveInstances func([]config.SourceInstance) error
	// CleanupCredentials best-effort deletes every credential slot of one
	// instance after it was deleted from the configuration.
	CleanupCredentials func(ctx context.Context, inst config.SourceInstance) error
	// Creds probes keychain status and stored-credential state. It is also
	// used to store a typed secret on editor Save.
	Creds credentials.Store
	// TestConnection runs the instance kind's connection test with the
	// editor's unsaved values, one entry per credential slot ("" = use the
	// stored credential for that slot); nothing is ever persisted by a
	// test. Multi-credential kinds report each capability in the report.
	TestConnection func(ctx context.Context, inst config.SourceInstance, typedSecrets map[credentials.Slot]string) registry.TestReport
	// HideSensitiveInfo reports the global privacy preference ("account
	// level monetary values are concealed").
	HideSensitiveInfo func() bool
	// SetHideSensitiveInfo persists the preference and re-renders visible
	// sensitive values. It runs on the UI thread.
	SetHideSensitiveInfo func(bool)
	// CheckUpdatesAutomatically reports the "Check for updates
	// automatically" preference. Development builds report the stored value,
	// but the app will not start automatic checks on them.
	CheckUpdatesAutomatically func() bool
	// SetCheckUpdatesAutomatically persists the preference and starts or
	// stops periodic checks accordingly. It runs on the UI thread.
	SetCheckUpdatesAutomatically func(bool)
	// NotificationsEnabled / SetNotificationsEnabled report and persist the
	// master "Desktop notifications" preference.
	NotificationsEnabled    func() bool
	SetNotificationsEnabled func(bool)
	// NotificationsQuotaLowEnabled / SetNotificationsQuotaLowEnabled
	// report and persist the "Quota window low" event toggle.
	NotificationsQuotaLowEnabled    func() bool
	SetNotificationsQuotaLowEnabled func(bool)
	// NotificationsQuotaExhaustedEnabled / SetNotificationsQuotaExhaustedEnabled
	// report and persist the "Quota window exhausted" event toggle.
	NotificationsQuotaExhaustedEnabled    func() bool
	SetNotificationsQuotaExhaustedEnabled func(bool)
	// ShowMonitorOnStartup / SetShowMonitorOnStartup report and persist the
	// "Show monitor window at startup" preference.
	ShowMonitorOnStartup    func() bool
	SetShowMonitorOnStartup func(bool)
	// AutostartState reports (present, supported): whether the start-on-login
	// entry currently exists and whether autostart is meaningful for this
	// binary (the control is disabled when it is not).
	AutostartState func() (present, supported bool)
	// SetAutostart writes or removes the start-on-login entry. It performs
	// a filesystem write and reports its error.
	SetAutostart func(bool) error
	// DiscoverGPUs lists the amdgputop-web GPUs at a base URL (the
	// amdgputop-web connection test doubles as discovery).
	DiscoverGPUs func(ctx context.Context, baseURL string) ([]domain.GPUInfo, error)
	// ConfigPaths reports the two on-disk locations the About tab links
	// to: the settings directory (the Fyne storage root, holding
	// preferences.json) and the current log file. Nil in tests that do
	// not care; the About tab then shows Unavailable.
	ConfigPaths func() (settingsDir, logFile string)
	// SourceDocument returns the raw stored source document — the exact
	// JSON the import path consumes and the export path writes — empty
	// when none is stored. Secret-free by construction.
	SourceDocument func() string

	// Done closes when the app begins quitting; background goroutines
	// bail out instead of touching the UI during teardown.
	Done <-chan struct{}
}

// Settings is the settings window. It is created once and reused; Show()
// rebuilds the list from the authoritative configuration.
type Settings struct {
	deps Deps
	win  fyne.Window

	// ctx parents every background probe, test, and credential operation;
	// it is cancelled when the app begins quitting (deps.Done) so
	// in-flight work aborts instead of running out its timeout against a
	// dying app. Config saves deliberately do not derive from it: a save
	// the user requested must persist even when it races Quit.
	ctx    context.Context
	cancel context.CancelFunc

	listBox         *fyne.Container
	addBtn          *widget.Button
	status          *widget.Label
	keychain        *widget.Label
	privacy         *widget.Check
	updates         *widget.Check
	notifs          *widget.Check
	notifsLow       *widget.Check
	notifsExhausted *widget.Check
	startupShow     *widget.Check
	autostart       *widget.Check
	startupHint     *widget.Label
	exportBtn       *widget.Button
	importBtn       *widget.Button
	tabs            *container.AppTabs
	aboutItem       *container.TabItem

	// mutBusy gates configuration mutations: at most one SaveInstances is
	// in flight. While one runs, every mutating control is disabled, so a
	// quick Enable followed by Move Down can never complete out of order.
	mutBusy atomic.Bool
	// mutControls are re-collected on every list rebuild (UI thread only).
	mutControls []fyne.CanvasObject
}

// NewSettings builds the window (hidden until Show).
func NewSettings(a fyne.App, deps Deps) *Settings {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Settings{deps: deps, ctx: ctx, cancel: cancel}
	// Cancel the root as soon as the app begins quitting.
	go func() {
		select {
		case <-deps.Done:
			cancel()
		case <-ctx.Done():
		}
	}()

	// Both status lines carry messages of unbounded length (a keychain
	// backend's own error text, a failed save's reason), so they wrap
	// instead of stretching the window to the message's single-line width.
	s.keychain = widget.NewLabel("")
	s.keychain.Wrapping = fyne.TextWrapWord
	s.status = widget.NewLabel("")
	s.status.Wrapping = fyne.TextWrapWord
	s.addBtn = widget.NewButtonWithIcon("Add source", theme.ContentAddIcon(), s.chooseKind)
	s.listBox = container.NewVBox()

	// The global privacy toggle lives in the General tab: it is an
	// application-wide setting, not per-source configuration. Toggling
	// persists immediately and re-renders visible sensitive values.
	s.privacy = widget.NewCheck("Hide sensitive info", func(on bool) {
		if s.deps.SetHideSensitiveInfo != nil {
			s.deps.SetHideSensitiveInfo(on)
		}
	})

	// The automatic update-check toggle is the same kind of application-wide
	// setting: toggling persists immediately and starts or stops periodic
	// checks. Manual checks from the tray are unaffected.
	s.updates = widget.NewCheck("Check for updates automatically", func(on bool) {
		if s.deps.SetCheckUpdatesAutomatically != nil {
			s.deps.SetCheckUpdatesAutomatically(on)
		}
	})

	// The notification toggles follow the same immediate-apply contract.
	// The master switch silences every event; the two event checks each
	// silence exactly one kind. They apply to the notifier's next event —
	// the notifier reads the preferences at event time.
	s.notifs = widget.NewCheck("Desktop notifications", func(on bool) {
		if s.deps.SetNotificationsEnabled != nil {
			s.deps.SetNotificationsEnabled(on)
		}
	})
	s.notifsLow = widget.NewCheck("Quota window low", func(on bool) {
		if s.deps.SetNotificationsQuotaLowEnabled != nil {
			s.deps.SetNotificationsQuotaLowEnabled(on)
		}
	})
	s.notifsExhausted = widget.NewCheck("Quota window exhausted", func(on bool) {
		if s.deps.SetNotificationsQuotaExhaustedEnabled != nil {
			s.deps.SetNotificationsQuotaExhaustedEnabled(on)
		}
	})

	// The startup toggles follow the same immediate-apply contract. Start
	// on login is a filesystem write, so its failure is surfaced in the
	// hint line under the toggle instead of being dropped.
	s.startupShow = widget.NewCheck("Show monitor window at startup", func(on bool) {
		if s.deps.SetShowMonitorOnStartup != nil {
			s.deps.SetShowMonitorOnStartup(on)
		}
	})
	s.autostart = widget.NewCheck("Start on login", func(on bool) {
		if s.deps.SetAutostart == nil {
			return
		}
		if err := s.deps.SetAutostart(on); err != nil {
			s.startupHint.SetText("Autostart: " + err.Error())
			s.startupHint.Show()
		}
	})
	s.startupHint = widget.NewLabel("")
	s.startupHint.TextStyle = fyne.TextStyle{Italic: true}
	s.startupHint.Wrapping = fyne.TextWrapWord
	s.startupHint.Hide()

	// The Configuration buttons move the secret-free source document in
	// and out of the app. Export is read-only; import ends in the guarded
	// mutation path, so it is disabled while another save is in flight.
	s.exportBtn = widget.NewButton("Export…", s.exportConfiguration)
	s.importBtn = widget.NewButton("Import…", s.importConfiguration)

	// The status line sits in a Border's centre, not an HBox: an HBox hands
	// out minimum widths, so a wrapping label would be squeezed to a sliver
	// the moment a long message arrived. Here it takes the width left over
	// from the button and wraps.
	bottom := container.NewBorder(nil, nil, s.addBtn, nil, s.status)

	// Two standard AppTabs — the monitor's grammar: everything about the
	// source instances stays in Sources, every application-wide setting
	// moves to General, and About carries the fixed build identity and
	// on-disk paths. The last-used tab persists across Show; like the
	// monitor, only the initial selection is forced here.
	sources := container.NewBorder(
		nil,
		container.NewVBox(widget.NewSeparator(), bottom),
		nil, nil,
		container.NewVScroll(s.listBox),
	)
	general := container.NewVBox(
		widget.NewLabelWithStyle("Privacy", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		s.privacy,
		s.keychain,
		widget.NewLabelWithStyle("Updates", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		s.updates,
		widget.NewLabelWithStyle("Notifications", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		s.notifs,
		s.notifsLow,
		s.notifsExhausted,
		widget.NewLabelWithStyle("Startup", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		s.startupShow,
		s.autostart,
		s.startupHint,
		widget.NewLabelWithStyle("Configuration", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		container.NewHBox(s.exportBtn, s.importBtn),
	)
	s.aboutItem = s.aboutTab()
	s.tabs = container.NewAppTabs(
		container.NewTabItem("Sources", sources),
		container.NewTabItem("General", general),
		s.aboutItem,
	)
	s.tabs.SelectIndex(0)

	win := a.NewWindow("Settings — AI Monitor")
	win.SetIcon(resources.Icon())
	win.SetContent(s.tabs)
	win.Resize(fyne.NewSize(560, 620))
	// Closing hides; the list is rebuilt from the stored configuration on
	// the next Show.
	win.SetCloseIntercept(func() { s.win.Hide() })
	s.win = win

	return s
}

// Window returns the underlying window.
func (s *Settings) Window() fyne.Window { return s.win }

// Show rebuilds the list from the authoritative configuration, reloads the
// application-wide preferences, and reveals the window.
func (s *Settings) Show() {
	if s.deps.HideSensitiveInfo != nil {
		s.privacy.SetChecked(s.deps.HideSensitiveInfo())
	}
	if s.deps.CheckUpdatesAutomatically != nil {
		s.updates.SetChecked(s.deps.CheckUpdatesAutomatically())
	}
	if s.deps.NotificationsEnabled != nil {
		s.notifs.SetChecked(s.deps.NotificationsEnabled())
	}
	if s.deps.NotificationsQuotaLowEnabled != nil {
		s.notifsLow.SetChecked(s.deps.NotificationsQuotaLowEnabled())
	}
	if s.deps.NotificationsQuotaExhaustedEnabled != nil {
		s.notifsExhausted.SetChecked(s.deps.NotificationsQuotaExhaustedEnabled())
	}
	if s.deps.ShowMonitorOnStartup != nil {
		s.startupShow.SetChecked(s.deps.ShowMonitorOnStartup())
	}
	if s.deps.AutostartState != nil {
		present, supported := s.deps.AutostartState()
		s.autostart.SetChecked(present)
		if supported {
			s.autostart.Enable()
			s.startupHint.Hide()
		} else {
			s.autostart.Disable()
			s.startupHint.SetText("Autostart is unavailable for development binaries.")
			s.startupHint.Show()
		}
	}
	s.refresh()
	s.probeKeychain()
	s.win.Show()
	s.win.RequestFocus()
}

// ShowAboutTab reveals the window with the About tab selected: the tray
// menu's "About" entry. The tab is selected before Show so the window
// never flashes the last-used tab.
func (s *Settings) ShowAboutTab() {
	s.tabs.Select(s.aboutItem)
	s.Show()
}

// aboutTab builds the About tab: the application identity (icon, name,
// version, commit, build date) and the two clickable on-disk paths —
// click opens the file manager there, through the same OpenURL path the
// tray Web UIs use. Everything on it is fixed for the process lifetime
// (stamped at link time or fixed by the storage root), so it is built
// once and never refreshed.
func (s *Settings) aboutTab() *container.TabItem {
	settingsDir, logFile := "", ""
	if s.deps.ConfigPaths != nil {
		settingsDir, logFile = s.deps.ConfigPaths()
	}
	rows := []fyne.CanvasObject{
		container.NewHBox(
			widget.NewIcon(resources.Icon()),
			widget.NewLabelWithStyle(buildinfo.AppName, fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		),
		widget.NewLabel("Version: " + buildinfo.Version),
	}
	// Commit and build date exist only in release builds; a dev build
	// shows what it is instead of empty lines.
	if buildinfo.Commit != "" {
		rows = append(rows, widget.NewLabel("Commit: "+buildinfo.Commit))
	}
	if buildinfo.BuildDate != "" {
		rows = append(rows, widget.NewLabel("Built: "+buildinfo.BuildDate))
	}
	rows = append(rows,
		widget.NewSeparator(),
		container.NewHBox(widget.NewLabel("Settings:"), pathLink(settingsDir)),
		container.NewHBox(widget.NewLabel("Logs:"), pathLink(logFile)),
	)

	return container.NewTabItem("About", container.NewVBox(rows...))
}

// pathLink renders one About-tab path: a standard hyperlink whose file
// URL opens the file manager there, or Unavailable when the app could not
// determine it. The URL is built from the path components (Scheme/Path),
// so escaping stays correct for exotic directory names.
func pathLink(path string) fyne.CanvasObject {
	if path == "" {
		return widget.NewLabel("Unavailable")
	}

	return widget.NewHyperlink(path, &url.URL{Scheme: "file", Path: filepath.ToSlash(path)})
}

// refresh rebuilds the instance list grouped by category, in configured
// order, and re-collects the mutating controls.
func (s *Settings) refresh() {
	instances := s.deps.LoadInstances()
	s.mutControls = s.mutControls[:0]

	var objs []fyne.CanvasObject
	for _, cat := range []struct {
		cat      domain.Category
		label    string
		emptyMsg string
	}{
		{domain.CategoryLocal, "Local", "No local sources."},
		{domain.CategoryRemote, "Remote", "No remote sources."},
	} {
		objs = append(objs, sectionLabel(cat.label))
		rows := make([]fyne.CanvasObject, 0, len(instances))
		for _, inst := range instances {
			if inst.Kind.Category() != cat.cat {
				continue
			}
			rows = append(rows, s.row(inst, instances))
		}
		if len(rows) == 0 {
			objs = append(objs, widget.NewLabelWithStyle(cat.emptyMsg,
				fyne.TextAlignLeading, fyne.TextStyle{Italic: true}))

			continue
		}
		objs = append(objs, rows...)
	}
	replaceObjects(s.listBox, objs...)
	s.setMutating(s.mutBusy.Load())
}

// row renders one instance from the same list snapshot refresh() iterates:
// enable toggle, name with kind label, and the per-row actions. Enable,
// Up/Down, and Delete persist immediately through the guarded mutation
// path; Edit opens the editor.
func (s *Settings) row(inst config.SourceInstance, instances []config.SourceInstance) fyne.CanvasObject {
	def, _ := registry.Get(inst.Kind)
	kindName := string(inst.Kind)
	if def.Kind != "" {
		kindName = def.DefaultName
	}

	check := widget.NewCheck("", nil)
	check.SetChecked(inst.Enabled)
	up := widget.NewButtonWithIcon("", theme.MoveUpIcon(), func() { s.move(inst.ID, MoveUp) })
	down := widget.NewButtonWithIcon("", theme.MoveDownIcon(), func() { s.move(inst.ID, MoveDown) })
	edit := widget.NewButton("Edit", func() { s.openEditor(inst.ID) })
	dup := widget.NewButtonWithIcon("", theme.ContentCopyIcon(), func() { s.duplicate(inst.ID) })
	del := widget.NewButtonWithIcon("", theme.DeleteIcon(), func() { s.confirmDelete(inst) })

	if !CanMoveUp(instances, inst.ID) {
		up.Disable()
	}
	if !CanMoveDown(instances, inst.ID) {
		down.Disable()
	}
	check.OnChanged = func(on bool) { s.setEnabled(inst.ID, on) }

	s.mutControls = append(s.mutControls, check, up, down, edit, dup, del)

	// The name is the user's own free text and can be long: wrap it so a
	// long name wraps inside the row instead of widening the window. The
	// kind label is fixed and stays unwrapped.
	title := container.NewVBox(
		wrappedLabel(inst.Name, fyne.TextStyle{Bold: true}),
		widget.NewLabelWithStyle(kindName, fyne.TextAlignLeading, fyne.TextStyle{Italic: true}),
	)
	actions := container.NewHBox(up, down, edit, dup, del)

	return container.NewBorder(nil, nil, check, actions, title)
}

// chooseKind is stage one of Add: pick the compiled kind, then the editor
// opens with a fresh draft instance. The kind's one-line description shows
// under the chooser as soon as one is selected, so picking a kind is an
// informed choice instead of a name guess.
func (s *Settings) chooseKind() {
	all := registry.All()
	options := make([]string, 0, len(all))
	for _, def := range all {
		options = append(options, def.DefaultName)
	}

	kindSelect := widget.NewSelect(options, nil)
	description := wrappedLabel("", fyne.TextStyle{Italic: true})
	kindSelect.OnChanged = func(name string) {
		for _, def := range all {
			if def.DefaultName == name {
				description.SetText(def.Description)

				return
			}
		}
	}
	form := widget.NewForm(
		widget.NewFormItem("Source type", kindSelect),
	)
	// The description sits below the form, not in it: a form row shares
	// the label column, so the text would wrap inside the narrow cell to
	// the right of "Source type" instead of using the dialog's full width.
	// The transparent spacer floors the dialog width — the form's own
	// minimum is sized to the select's placeholder and would leave the
	// select and the description cramped.
	width := canvas.NewRectangle(color.Transparent)
	width.SetMinSize(fyne.NewSize(360, 0))
	dlg := dialog.NewCustomConfirm("Add source", "Continue", "Cancel",
		container.NewVBox(form, description, width), func(ok bool) {
			if !ok || kindSelect.Selected == "" {
				return
			}
			for _, def := range all {
				if def.DefaultName == kindSelect.Selected {
					s.editDraft(def.Kind)

					return
				}
			}
		}, s.win)
	dlg.Show()
}

// openEditor edits an existing instance (identity by ID; the kind is
// immutable and shown read-only).
func (s *Settings) openEditor(id domain.InstanceID) {
	instances := s.deps.LoadInstances()
	i := indexByID(instances, id)
	if i < 0 {
		return // vanished between render and tap; refresh will catch up
	}
	s.openEditorFor(instances[i], false)
}

// editDraft opens the editor for a brand-new instance of the kind.
func (s *Settings) editDraft(kind domain.SourceKind) {
	draft, ok := newDraft(kind)
	if !ok {
		return
	}
	s.openEditorFor(draft, true)
}

// duplicate opens the editor for a fresh copy of an instance (the row
// action): new ID, copied provider settings/interval/pace window, name
// suffixed " (copy)". Credentials are not copied — keychain accounts are
// keyed per instance ID, so the copy is credential-free by construction,
// and nothing persists until the editor saves.
func (s *Settings) duplicate(id domain.InstanceID) {
	instances := s.deps.LoadInstances()
	i := indexByID(instances, id)
	if i < 0 {
		return
	}
	draft, ok := duplicateInstance(instances[i])
	if !ok {
		return
	}
	s.openEditorFor(draft, true)
}

// setEnabled persists an immediate enable/disable toggle.
func (s *Settings) setEnabled(id domain.InstanceID, on bool) {
	s.mutate(func() ([]config.SourceInstance, error) {
		return SetEnabled(s.deps.LoadInstances(), id, on), nil
	}, nil)
}

// move persists an immediate Up/Down reorder.
func (s *Settings) move(id domain.InstanceID, op func([]config.SourceInstance, domain.InstanceID) []config.SourceInstance) {
	s.mutate(func() ([]config.SourceInstance, error) {
		return op(s.deps.LoadInstances(), id), nil
	}, nil)
}

// confirmDelete asks once, then deletes the instance and, only after the
// configuration persist succeeded, best-effort removes its credentials.
// A failed credential cleanup never resurrects the instance; it surfaces
// as a warning.
func (s *Settings) confirmDelete(inst config.SourceInstance) {
	msg := fmt.Sprintf("Delete %q?", inst.Name)
	if def, ok := registry.Get(inst.Kind); ok && len(def.CredentialSlots) > 0 {
		msg += "\nIts stored credential is removed too."
	}
	dialog.NewConfirm("Delete source", msg, func(ok bool) {
		if !ok {
			return
		}
		s.mutate(
			func() ([]config.SourceInstance, error) { return RemoveInstance(s.deps.LoadInstances(), inst.ID), nil },
			func() { s.cleanupCredentials(inst) },
		)
	}, s.win).Show()
}

// cleanupCredentials runs on the mutation goroutine after a successful
// delete persist. Failures are sanitized to a warning; the deletion
// itself stands.
func (s *Settings) cleanupCredentials(inst config.SourceInstance) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	if err := s.deps.CleanupCredentials(ctx, inst); err != nil {
		if s.closing() {
			return
		}
		fyne.Do(func() {
			s.status.SetText("Deleted, but removing the stored credential failed: " + err.Error())
		})
	}
}

// exportConfiguration writes the stored source document to a user-chosen
// file. The document is secret-free by construction — credentials live
// only in the keychain and the schema has no field that could hold one —
// so the exported file can never leak a secret. An empty configuration
// exports the minimal valid document, so a file always imports back.
func (s *Settings) exportConfiguration() {
	if s.deps.SourceDocument == nil {
		return
	}
	dlg := dialog.NewFileSave(func(w fyne.URIWriteCloser, err error) {
		if err != nil {
			s.status.SetText("Export failed: " + err.Error())

			return
		}
		if w == nil {
			return // the chooser was canceled
		}
		defer func() { _ = w.Close() }()
		if _, werr := w.Write([]byte(exportDocument(s.deps.SourceDocument()))); werr != nil {
			s.status.SetText("Export failed: " + werr.Error())

			return
		}
		s.status.SetText("Configuration exported.")
	}, s.win)
	dlg.SetFileName("aimon-sources.json")
	dlg.Show()
}

// exportDocument renders the exported file body: the raw stored document,
// or the minimal valid document when nothing is stored.
func exportDocument(raw string) string {
	if raw == "" {
		return "{\n  \"schema_version\": 1\n}"
	}

	return raw
}

// importConfiguration reads a user-chosen file, validates it as a source
// document, and — only after an explicit confirmation — replaces the
// stored configuration through the guarded mutation path.
func (s *Settings) importConfiguration() {
	dialog.NewFileOpen(func(r fyne.URIReadCloser, err error) {
		if err != nil {
			s.status.SetText("Import failed: " + err.Error())

			return
		}
		if r == nil {
			return // the chooser was canceled
		}
		defer func() { _ = r.Close() }()
		raw, rerr := io.ReadAll(r)
		if rerr != nil {
			s.status.SetText("Import failed: " + rerr.Error())

			return
		}
		s.importDocument(string(raw),
			func(list []config.SourceInstance, commit func()) {
				dialog.NewConfirm("Import source configuration",
					"Replace current source configuration? Credentials are not included and are not affected.",
					func(ok bool) {
						if ok {
							commit()
						}
					}, s.win).Show()
			},
			func(msg string) { dialog.NewError(errors.New(msg), s.win).Show() })
	}, s.win).Show()
}

// importDocument is the dialog-independent body of the import path:
// validate first — a failure reports and stops before anything is written,
// and the stored document stays untouched. On success ask runs with the
// parsed candidate and the commit closure (capturing it); committing goes
// through the guarded mutation path. A colliding ID replaces that
// instance (its keychain accounts keep matching); credentials of absent
// instances are never touched.
func (s *Settings) importDocument(raw string, ask func(list []config.SourceInstance, commit func()), report func(string)) {
	list, err := config.ParseSourceDocument(raw)
	if err != nil {
		report(err.Error())

		return
	}
	ask(list, func() {
		s.mutate(func() ([]config.SourceInstance, error) { return list, nil }, nil)
	})
}

// mutate is the single guarded configuration mutation path. The candidate
// derivation runs on the UI thread (it reads the authoritative list); the
// persist runs on a background goroutine. On success the list is rebuilt
// from the (new) authoritative state; on failure the previous state is
// rebuilt unchanged and the error is surfaced. onSuccess, when set, runs
// on the mutation goroutine after a successful persist.
func (s *Settings) mutate(candidate func() ([]config.SourceInstance, error), onSuccess func()) {
	if !s.mutBusy.CompareAndSwap(false, true) {
		s.status.SetText("Another change is still saving…")

		return
	}
	list, err := candidate()
	if err != nil {
		s.mutBusy.Store(false)
		s.status.SetText(err.Error())

		return
	}
	s.setMutating(true)
	s.status.SetText("Saving…")

	go func() {
		saveErr := s.deps.SaveInstances(list)
		if saveErr == nil && onSuccess != nil {
			onSuccess()
		}
		if s.closing() {
			return
		}
		fyne.Do(func() {
			s.mutBusy.Store(false)
			s.setMutating(false)
			if saveErr != nil {
				s.status.SetText("Save failed: " + saveErr.Error())
			} else {
				s.status.SetText("")
			}
			s.refresh()
		})
	}()
}

// setMutating disables (or re-enables) every mutating control while a
// configuration mutation is in flight. UI thread only.
func (s *Settings) setMutating(busy bool) {
	for _, c := range s.mutControls {
		if busy {
			disableObject(c)
		} else {
			enableObject(c)
		}
	}
	if busy {
		s.addBtn.Disable()
		s.importBtn.Disable()
	} else {
		s.addBtn.Enable()
		s.importBtn.Enable()
	}
}

// disableObject/enableObject drive any row control; buttons and checks
// both satisfy fyne.Disableable. Re-enabling is unconditional: the row is
// rebuilt right after, restoring edge-dependent states (first Up, last
// Down).
func disableObject(c fyne.CanvasObject) {
	if d, ok := c.(fyne.Disableable); ok {
		d.Disable()
	}
}

func enableObject(c fyne.CanvasObject) {
	if d, ok := c.(fyne.Disableable); ok {
		d.Enable()
	}
}

// probeKeychain refreshes the backend status line (nothing is persisted as
// a hint; the secret value is never displayed).
func (s *Settings) probeKeychain() {
	s.keychain.SetText("Checking keychain…")
	go func() {
		ctx, cancel := context.WithTimeout(s.ctx, testTimeout)
		defer cancel()

		status := s.deps.Creds.Status(ctx)
		if s.closing() {
			return
		}
		fyne.Do(func() { s.applyKeychainState(status) })
	}()
}

func (s *Settings) applyKeychainState(status credentials.Status) {
	switch status.State {
	case credentials.StateAvailable:
		s.keychain.SetText(fmt.Sprintf("Keychain: %s", status.Backend))
	case credentials.StateUnsupported:
		s.keychain.SetText("Keychain: not supported on this platform — credentials cannot be stored")
	default:
		msg := status.Message
		if msg == "" {
			msg = string(status.State)
		}
		s.keychain.SetText(fmt.Sprintf("Keychain: %s (%s) — credentials unavailable", status.Backend, msg))
	}
}

func (s *Settings) closing() bool {
	select {
	case <-s.deps.Done:
		return true
	default:
		return false
	}
}

func sectionLabel(text string) fyne.CanvasObject {
	return widget.NewLabelWithStyle(text, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
}

// wrappedLabel builds a label that wraps at whatever width its container
// gives it instead of demanding its single-line width. Only labels that
// already receive the full width (a Border centre, a VBox child) may wrap.
func wrappedLabel(text string, style fyne.TextStyle) *widget.Label {
	l := widget.NewLabelWithStyle(text, fyne.TextAlignLeading, style)
	l.Wrapping = fyne.TextWrapWord

	return l
}

// testFailure renders a test failure using the error's user message when
// available.
func testFailure(err error) string {
	return "Failed: " + err.Error()
}

func replaceObjects(box *fyne.Container, objs ...fyne.CanvasObject) {
	box.Objects = objs
	box.Refresh()
}
