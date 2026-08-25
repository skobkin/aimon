package monitor

import (
	"testing"

	"fyne.io/fyne/v2/test"

	"git.skobk.in/skobkin/aimonitor/internal/config"
	"git.skobk.in/skobkin/aimonitor/internal/domain"
)

// stubCtrl is the minimal controller for building the real window.
type stubCtrl struct{}

func (stubCtrl) States() map[domain.SourceID]domain.Runtime { return nil }
func (stubCtrl) Config() config.Config                      { return config.Default() }
func (stubCtrl) RefreshAll()                                {}
func (stubCtrl) OpenSettings()                              {}

// The actions overlay must keep its buttons at their natural height: a
// Stack stretches its children to the full window size and an HBox then
// grows its children to the row height, so a bare action row stretched
// the buttons window-tall. The row is VBox-wrapped in NewMonitor; this
// guards that structure against regression.
func TestActionsOverlayKeepsButtonHeight(t *testing.T) {
	app := test.NewApp()
	done := make(chan struct{})
	defer close(done)
	m := NewMonitor(app, stubCtrl{}, done)
	m.Window().Resize(monitorSize)

	got := m.refreshBtn.Size().Height
	if got <= 0 {
		t.Fatal("refresh button has zero size; the test driver ran no layout")
	}
	if want := m.refreshBtn.MinSize().Height; got > want+0.5 {
		t.Errorf("refresh button height = %v, want its natural height %v", got, want)
	}
}
