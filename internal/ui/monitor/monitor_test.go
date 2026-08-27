package monitor

import (
	"slices"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"git.skobk.in/skobkin/aimonitor/internal/config"
	"git.skobk.in/skobkin/aimonitor/internal/domain"
)

// stubCtrl is the minimal controller for building the real window. The
// privacy preference defaults to hiding, like a fresh install.
type stubCtrl struct{}

func (stubCtrl) Instances() []config.SourceInstance           { return nil }
func (stubCtrl) States() map[domain.InstanceID]domain.Runtime { return nil }
func (stubCtrl) RefreshAll()                                  {}
func (stubCtrl) OpenSettings()                                {}
func (stubCtrl) HideSensitiveInfo() bool                      { return true }

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

// statefulCtrl serves one OpenRouter instance with a landed snapshot so
// the tab dispatchers exercise their spend bodies.
type statefulCtrl struct {
	stubCtrl
	hidden bool
}

func (c *statefulCtrl) Instances() []config.SourceInstance {
	return []config.SourceInstance{{
		ID: "or-1", Kind: domain.KindOpenRouter, Name: "OpenRouter",
		Enabled: true, PollInterval: time.Minute, OpenRouter: &config.OpenRouterSettings{},
	}}
}

func (c *statefulCtrl) States() map[domain.InstanceID]domain.Runtime {
	return map[domain.InstanceID]domain.Runtime{
		"or-1": {
			Enabled:       true,
			LastSuccessAt: time.Unix(1770000000, 0),
			Snapshot: domain.OpenRouterSnapshot{
				CollectedAt: time.Unix(1770000000, 0),
				Key: &domain.OpenRouterKeyUsage{
					UsageDaily: 0.42, UsageWeekly: 3.81, UsageMonthly: 12.47, UsageTotal: 41.08,
				},
				Account: &domain.OpenRouterAccountCredits{TotalCredits: 105.89, TotalUsage: 32.47, Balance: 73.42},
			},
		},
	}
}

func (c *statefulCtrl) HideSensitiveInfo() bool { return c.hidden }

// The Remote tab renders an OpenRouter card through NewSpendDetailBody:
// usage text visible even while privacy hides account values.
func TestRemoteTabRendersSpendBody(t *testing.T) {
	app := test.NewApp()
	done := make(chan struct{})
	defer close(done)

	ctrl := &statefulCtrl{hidden: true}
	m := NewMonitor(app, ctrl, done)
	m.rebuild()

	texts := allTexts(t, m.remoteBox)
	if !slices.Contains(texts, "$41.08") { // Total is a detail-only row
		t.Errorf("remote tab lacks the Total row; texts = %v", texts)
	}
	if slices.Contains(texts, "$73.42") {
		t.Error("masked Balance must not render its real value while hidden")
	}
	balance := findCanvasText(t, m.remoteBox, balanceMask)
	if balance == nil {
		t.Fatalf("no masked Balance found; texts = %v", texts)
	}
	ctrl.hidden = false
	m.rebuild()
	if got := findCanvasText(t, m.remoteBox, "$73.42"); got == nil {
		t.Error("privacy off must reveal Balance with its real value")
	}
	if got := findCanvasText(t, m.remoteBox, balanceMask); got != nil {
		t.Errorf("the mask must disappear when privacy is off: %+v", got)
	}
}

const balanceMask = "$•••••"

// The Overview tab renders the compact spend body: no Total line.
func TestOverviewTabRendersCompactSpendBody(t *testing.T) {
	app := test.NewApp()
	done := make(chan struct{})
	defer close(done)

	ctrl := &statefulCtrl{hidden: true}
	m := NewMonitor(app, ctrl, done)
	m.rebuild()

	texts := allTexts(t, m.overviewBox)
	if slices.Contains(texts, "$41.08") {
		t.Error("an overview card must not show the Total row")
	}
	if !slices.Contains(texts, balanceMask) && !slices.Contains(texts, "$73.42") {
		t.Errorf("overview card lacks a Balance row; texts = %v", texts)
	}
}

func allTexts(t *testing.T, obj fyne.CanvasObject) []string {
	t.Helper()
	var out []string
	switch o := obj.(type) {
	case fyne.Widget:
		for _, child := range test.TempWidgetRenderer(t, o).Objects() {
			out = append(out, allTexts(t, child)...)
		}
	case *fyne.Container:
		for _, child := range o.Objects {
			out = append(out, allTexts(t, child)...)
		}
	case *widget.Label:
		out = append(out, o.Text)
	case *canvas.Text:
		out = append(out, o.Text)
	}
	return out
}

func findCanvasText(t *testing.T, obj fyne.CanvasObject, s string) *canvas.Text {
	t.Helper()
	var found *canvas.Text
	switch o := obj.(type) {
	case fyne.Widget:
		for _, child := range test.TempWidgetRenderer(t, o).Objects() {
			if txt := findCanvasText(t, child, s); txt != nil {
				found = txt
			}
		}
	case *fyne.Container:
		for _, child := range o.Objects {
			if txt := findCanvasText(t, child, s); txt != nil {
				found = txt
			}
		}
	case *canvas.Text:
		if o.Text == s {
			found = o
		}
	}
	return found
}
