package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"

	updates "github.com/skobkin/go4updates"
	"github.com/skobkin/go4updates/releasefmt"
	fyneupdate "github.com/skobkin/go4updates/ui/fyne/update"

	"git.skobk.in/skobkin/aimonitor/internal/buildinfo"
	appupdates "git.skobk.in/skobkin/aimonitor/internal/updates"
)

// updateChecker is the seam between the app and the update manager; the
// production implementation is *appupdates.Manager and tests substitute a
// stub.
type updateChecker interface {
	Running() bool
	Start()
	Stop()
	CheckNow(ctx context.Context) (updates.Result, error)
}

// newUpdateChecker builds the production update manager; kept as a variable
// so tests can stub construction without a Forgejo server.
var newUpdateChecker = func(logger *slog.Logger, onAvailable func(updates.Result)) (updateChecker, error) {
	return appupdates.New(appupdates.Options{
		CurrentVersion:    buildinfo.Version,
		Logger:            logger,
		OnUpdateAvailable: onAvailable,
	})
}

// setupUpdateChecker creates the update manager. A nil checker is valid:
// every entry point degrades to a diagnostic dialog or a log line.
func (a *App) setupUpdateChecker() {
	checker, err := newUpdateChecker(a.log, a.onAutoUpdateAvailable)
	if err != nil {
		a.log.Warn("initialize update checker", "error", err)

		return
	}
	a.updateChecker = checker
}

// startUpdateChecks enables periodic checks per the stored preference;
// development builds and disabled configs skip them. Manual checks remain
// available either way.
func (a *App) startUpdateChecks() {
	if a.updateChecker == nil {
		return
	}
	automatic := a.repo.CheckUpdatesAutomatically()
	if !updateChecksEnabled(automatic, buildinfo.Version) {
		// Info, not debug: at the default level this is the only evidence
		// that update checking exists, and it explains the silence on dev
		// builds and user-disabled configs alike.
		a.log.Info("automatic update checks disabled",
			"version", buildinfo.Version,
			"configured", automatic)

		return
	}
	a.updateChecker.Start()
}

// updateChecksEnabled reports whether automatic checks should run for this
// preference and build: never on development builds, otherwise per
// preference.
func updateChecksEnabled(automatic bool, version string) bool {
	if version == buildinfo.DevVersion {
		return false
	}

	return automatic
}

// CheckUpdatesAutomatically reports the stored "Check for updates
// automatically" preference; the Settings window reads it through Deps.
func (a *App) CheckUpdatesAutomatically() bool { return a.repo.CheckUpdatesAutomatically() }

// SetCheckUpdatesAutomatically persists the preference and stops or (re)starts
// periodic checks on an actual change. Called on the UI thread from the
// Settings check callback; development builds never start periodic checks.
func (a *App) SetCheckUpdatesAutomatically(on bool) {
	a.repo.SetCheckUpdatesAutomatically(on)
	if a.updateChecker == nil {
		return
	}
	if buildinfo.Version == buildinfo.DevVersion {
		return
	}
	if on == a.updateChecker.Running() {
		return
	}
	if on {
		// Start performs an immediate first check; the manager suppresses a
		// repeated prompt for an already-shown version.
		a.updateChecker.Start()

		return
	}
	a.updateChecker.Stop()
}

// onAutoUpdateAvailable bridges from a Manager goroutine to the UI thread.
// It only enqueues and never blocks the event drain.
func (a *App) onAutoUpdateAvailable(result updates.Result) {
	fyne.Do(func() {
		a.presentAutoUpdate(result)
	})
}

// presentAutoUpdate shows the new-release dialog; the manager already
// deduplicated, so this runs at most once per version. The monitor is shown
// first: a dialog attached to a hidden window is invisible on most
// platforms.
func (a *App) presentAutoUpdate(result updates.Result) {
	a.monitor.Show()
	a.showUpdateDialog(result)
}

// checkForUpdates runs one manual update check and presents the outcome.
// Runs on the UI thread; the network round-trip happens in a goroutine.
func (a *App) checkForUpdates() {
	if a.updateChecker == nil {
		dialog.ShowError(errors.New("update checking is unavailable"), a.monitor.Window())

		return
	}
	if !a.checkingUpdates.CompareAndSwap(false, true) {
		return
	}
	a.setUpdateItemDisabled(true)
	go func() {
		result, err := a.updateChecker.CheckNow(context.Background())
		fyne.Do(func() {
			a.setUpdateItemDisabled(false)
			a.presentManualCheckResult(result, err)
			// Cleared last: a busy-flag observation implies the previous
			// check, including its presentation, has fully completed.
			a.checkingUpdates.Store(false)
		})
	}()
}

// presentManualCheckResult shows the manual-check outcome: unlike the
// automatic path, every result is surfaced, including failures, because the
// user explicitly asked.
func (a *App) presentManualCheckResult(result updates.Result, err error) {
	a.monitor.Show()
	if err != nil {
		a.log.Warn("manual update check failed", "error", err)
		dialog.ShowError(fmt.Errorf("check for updates failed:\n%w", err), a.monitor.Window())

		return
	}
	a.showUpdateDialog(result)
}

// showUpdateDialog renders the go4updates release dialog for the fetched
// result. Must run on the UI thread.
func (a *App) showUpdateDialog(result updates.Result) {
	fyneupdate.ShowDialog(a.monitor.Window(), result, fyneupdate.Options{
		Format: releasefmt.Options{
			// The linker requires an absolute repository URL; the feed's
			// value is derived from the configured server.
			Linker:         releasefmt.ForgejoLinker(result.Feed.RepositoryURL),
			ShortCommitSHA: true,
		},
		// Markdown options stay zero: remote release-note images are not
		// fetched.
	})
}

// setUpdateItemDisabled toggles the tray item while a manual check is in
// flight. The native systray item disables in place. The item pointer is
// swapped atomically because the menu — and with it the item — is rebuilt on
// every configuration save, possibly while a check is in flight.
func (a *App) setUpdateItemDisabled(disabled bool) {
	item := a.updateItem.Load()
	if item == nil {
		return
	}
	if disabled {
		item.Disable()

		return
	}
	item.Enable()
}
