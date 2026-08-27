// Package settings implements the Settings window: a CRUD list of the
// configured source instances grouped Local/Remote, with an Add/Edit
// editor dialog. All network and keychain work runs in background
// goroutines; the UI thread only mutates widgets, and every background
// result crosses back through fyne.Do. Configuration mutations all pass
// through one guarded path (persist first, apply only on success), and at
// most one is in flight at a time.
package settings

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"git.skobk.in/skobkin/aimonitor/internal/config"
	"git.skobk.in/skobkin/aimonitor/internal/credentials"
	"git.skobk.in/skobkin/aimonitor/internal/domain"
	"git.skobk.in/skobkin/aimonitor/internal/registry"
	"git.skobk.in/skobkin/aimonitor/internal/resources"
)

// testTimeout bounds one connection test, credential probe, or credential
// cleanup.
const testTimeout = 10 * time.Second

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
	// DiscoverGPUs lists the amdgputop-web GPUs at a base URL (the
	// amdgputop-web connection test doubles as discovery).
	DiscoverGPUs func(ctx context.Context, baseURL string) ([]domain.GPUInfo, error)

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

	listBox  *fyne.Container
	addBtn   *widget.Button
	status   *widget.Label
	keychain *widget.Label

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

	s.keychain = widget.NewLabel("")
	s.status = widget.NewLabel("")
	s.status.Wrapping = fyne.TextWrapWord
	s.addBtn = widget.NewButtonWithIcon("Add source", theme.ContentAddIcon(), s.chooseKind)
	s.listBox = container.NewVBox()

	bottom := container.NewHBox(s.addBtn, s.status)

	content := container.NewBorder(
		container.NewVBox(s.keychain, widget.NewSeparator()),
		container.NewVBox(widget.NewSeparator(), bottom),
		nil, nil,
		container.NewVScroll(s.listBox),
	)

	win := a.NewWindow("Settings — AI Monitor")
	win.SetIcon(resources.Icon())
	win.SetContent(content)
	win.Resize(fyne.NewSize(560, 620))
	// Closing hides; the list is rebuilt from the stored configuration on
	// the next Show.
	win.SetCloseIntercept(func() { s.win.Hide() })
	s.win = win
	return s
}

// Window returns the underlying window.
func (s *Settings) Window() fyne.Window { return s.win }

// Show rebuilds the list from the authoritative configuration and reveals
// the window.
func (s *Settings) Show() {
	s.refresh()
	s.probeKeychain()
	s.win.Show()
	s.win.RequestFocus()
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
	del := widget.NewButtonWithIcon("", theme.DeleteIcon(), func() { s.confirmDelete(inst) })

	if !CanMoveUp(instances, inst.ID) {
		up.Disable()
	}
	if !CanMoveDown(instances, inst.ID) {
		down.Disable()
	}
	check.OnChanged = func(on bool) { s.setEnabled(inst.ID, on) }

	s.mutControls = append(s.mutControls, check, up, down, edit, del)

	title := container.NewVBox(
		widget.NewLabelWithStyle(inst.Name, fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		widget.NewLabelWithStyle(kindName, fyne.TextAlignLeading, fyne.TextStyle{Italic: true}),
	)
	actions := container.NewHBox(up, down, edit, del)
	return container.NewBorder(nil, nil, check, actions, title)
}

// chooseKind is stage one of Add: pick the compiled kind, then the editor
// opens with a fresh draft instance.
func (s *Settings) chooseKind() {
	all := registry.All()
	options := make([]string, 0, len(all))
	for _, def := range all {
		options = append(options, def.DefaultName)
	}

	kindSelect := widget.NewSelect(options, nil)
	form := widget.NewForm(widget.NewFormItem("Source type", kindSelect))
	dlg := dialog.NewCustomConfirm("Add source", "Continue", "Cancel", form, func(ok bool) {
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
	} else {
		s.addBtn.Enable()
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

// testFailure renders a test failure using the error's user message when
// available.
func testFailure(err error) string {
	return "Failed: " + err.Error()
}

func replaceObjects(box *fyne.Container, objs ...fyne.CanvasObject) {
	box.Objects = objs
	box.Refresh()
}
