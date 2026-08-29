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

// countingCtrl counts controller reads, which rebuild() makes exactly once
// per rebuild; it turns "how often did the tree rebuild" into a plain
// integer without instrumenting the monitor itself.
type countingCtrl struct {
	stubCtrl
	statesReads int
}

func (c *countingCtrl) States() map[domain.InstanceID]domain.Runtime {
	c.statesReads++

	return nil
}

// A hidden window must not rebuild on state changes: Fyne caches widget
// renderers at layout time even when nothing paints, and a hidden window
// never paints, so the render expiry that would free them never runs and
// every hidden rebuild leaks its whole widget tree (see OnStatesChanged).
// Show() rebuilds, so freshness is unaffected.
func TestHiddenMonitorSkipsRebuilds(t *testing.T) {
	app := test.NewApp()
	done := make(chan struct{})
	defer close(done)

	ctrl := &countingCtrl{}
	m := NewMonitor(app, ctrl, done)

	m.OnStatesChanged() // hidden: must be a no-op
	if ctrl.statesReads != 0 {
		t.Fatalf("hidden rebuild ran %d times, want 0", ctrl.statesReads)
	}

	m.Show() // revealing must render the current states
	if ctrl.statesReads != 1 {
		t.Fatalf("Show() rebuilt %d times, want 1", ctrl.statesReads)
	}

	m.OnStatesChanged() // visible: rebuilds for fresh data
	if ctrl.statesReads != 2 {
		t.Fatalf("visible rebuild ran %d times, want 1 more (got %d)", ctrl.statesReads-1, ctrl.statesReads)
	}

	m.hide()
	m.OnStatesChanged() // hidden again: skipped
	if ctrl.statesReads != 2 {
		t.Fatalf("rebuild after hide ran, total reads = %d, want %d", ctrl.statesReads, 2)
	}
}

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

// quotaCtrl serves one MiniMax instance whose snapshot has landed and
// whose quota window resets in the future: both the freshness status and a
// reset countdown are live time-dependent texts on the tree. Times are
// taken at States() so the test can tick relative to them.
type quotaCtrl struct {
	stubCtrl
	now time.Time
}

func (c *quotaCtrl) Instances() []config.SourceInstance {
	return []config.SourceInstance{{
		ID: "mm-1", Kind: domain.KindMiniMax, Name: "MiniMax",
		Enabled: true, PollInterval: time.Minute, MiniMax: &config.MiniMaxSettings{},
	}}
}

func (c *quotaCtrl) States() map[domain.InstanceID]domain.Runtime {
	c.now = time.Now()
	reset := c.now.Add(2 * time.Hour)

	return map[domain.InstanceID]domain.Runtime{
		"mm-1": {
			Enabled:       true,
			LastSuccessAt: c.now,
			Snapshot: domain.QuotaSnapshot{
				Kind: domain.KindMiniMax,
				Windows: []domain.QuotaWindow{
					{ID: "interval", Name: "5-hour", UsedFraction: 0.17, RemainingFraction: 0.83, ResetAt: &reset},
				},
			},
		},
	}
}

// flatten collects every canvas object of the tabs, in order.
func flatten(t *testing.T, objs []fyne.CanvasObject) []fyne.CanvasObject {
	t.Helper()
	var out []fyne.CanvasObject
	var walk func(o fyne.CanvasObject)
	walk = func(o fyne.CanvasObject) {
		out = append(out, o)
		switch c := o.(type) {
		case fyne.Widget:
			for _, child := range test.TempWidgetRenderer(t, c).Objects() {
				walk(child)
			}
		case *fyne.Container:
			for _, child := range c.Objects {
				walk(child)
			}
		}
	}
	for _, o := range objs {
		walk(o)
	}

	return out
}

// The per-second ticker must age the time-dependent labels (freshness,
// reset countdowns) in place: updateTimeTexts changes those texts while
// the canvas object tree stays identical. Rebuilding the tree every second
// was exactly the cache churn the ticker exists to avoid.
func TestUpdateTimeTextsAgesLabelsInPlace(t *testing.T) {
	app := test.NewApp()
	done := make(chan struct{})
	defer close(done)

	ctrl := &quotaCtrl{}
	m := NewMonitor(app, ctrl, done)
	m.Show()

	beforeObjs := append(flatten(t, m.overviewBox.Objects), flatten(t, m.remoteBox.Objects)...)
	beforeTexts := allTexts(t, m.overviewBox)
	beforeTexts = append(beforeTexts, allTexts(t, m.remoteBox)...)

	m.updateTimeTexts(ctrl.now.Add(90 * time.Second))

	afterObjs := append(flatten(t, m.overviewBox.Objects), flatten(t, m.remoteBox.Objects)...)
	afterTexts := allTexts(t, m.overviewBox)
	afterTexts = append(afterTexts, allTexts(t, m.remoteBox)...)

	if len(beforeObjs) != len(afterObjs) {
		t.Fatalf("tree size changed: %d -> %d objects", len(beforeObjs), len(afterObjs))
	}
	for i := range beforeObjs {
		if beforeObjs[i] != afterObjs[i] {
			t.Fatalf("object %d was replaced by the tick; labels must age in place", i)
		}
	}

	// The freshness label aged from "0s ago" to "1m ago".
	if !slices.Contains(beforeTexts, "0s ago") {
		t.Errorf("initial texts lack the fresh status; texts = %v", beforeTexts)
	}
	if !slices.Contains(afterTexts, "1m ago") {
		t.Errorf("tick did not age the freshness label; texts = %v", afterTexts)
	}
	// The quota row countdown counted down: 2h minus the build/tick skew
	// renders as 1h59m, then 1h58m ninety seconds later.
	if !slices.Contains(beforeTexts, "5-hour · 1h59m") {
		t.Errorf("initial texts lack the quota countdown; texts = %v", beforeTexts)
	}
	if !slices.Contains(afterTexts, "5-hour · 1h58m") {
		t.Errorf("tick did not age the countdown; texts = %v", afterTexts)
	}
}

// A rebuild replaces the tick refresher set: after new states land, the
// ticker must drive the labels of the new tree. If the closures of the
// discarded tree survived, the status would keep aging from the old
// snapshot ("1h ago") instead of the new one ("1m ago").
func TestRebuildReplacesTickRefreshers(t *testing.T) {
	app := test.NewApp()
	done := make(chan struct{})
	defer close(done)

	ctrl := &quotaCtrl{}
	m := NewMonitor(app, ctrl, done)
	m.Show()

	ctrl.now = time.Now().Add(time.Hour) // a new snapshot lands an hour later
	m.OnStatesChanged()                  // visible: rebuilds from fresh states
	m.updateTimeTexts(ctrl.now.Add(90 * time.Second))

	texts := allTexts(t, m.remoteBox)
	if !slices.Contains(texts, "1m ago") {
		t.Errorf("ticker did not adopt the rebuilt tree; texts = %v", texts)
	}
	if slices.Contains(texts, "1h ago") {
		t.Errorf("stale refresher from the discarded tree still drives a label; texts = %v", texts)
	}
}

// hindsightCtrl serves one Hindsight instance with a landed aggregate
// snapshot. webUI controls the optional Web UI URL, so the tests cover
// both the "Open Hindsight" button and its absence.
type hindsightCtrl struct {
	stubCtrl
	now   time.Time
	webUI string
}

func (c *hindsightCtrl) Instances() []config.SourceInstance {
	return []config.SourceInstance{{
		ID: "hs-1", Kind: domain.KindHindsight, Name: "Hindsight",
		Enabled: true, PollInterval: 5 * time.Second,
		Hindsight: &config.HindsightSettings{
			BaseURL:  "http://127.0.0.1:8888",
			WebUIURL: c.webUI,
		},
	}}
}

func (c *hindsightCtrl) States() map[domain.InstanceID]domain.Runtime {
	c.now = time.Now()
	lastWrite := c.now.Add(-3 * time.Minute)

	return map[domain.InstanceID]domain.Runtime{
		"hs-1": {
			Enabled:       true,
			LastSuccessAt: c.now,
			Snapshot: domain.HindsightSnapshot{
				CollectedAt: c.now,
				Service: domain.HindsightService{
					Status:   "healthy",
					Database: "connected",
					Version:  "0.9.2",
					Uptime:   2*24*time.Hour + 7*time.Hour,
				},
				Memory: &domain.HindsightMemory{Banks: 27, Facts: 18400, LastWriteAt: &lastWrite},
				Activity: domain.HindsightActivity{
					RecallPerMin:   f64ptr(18),
					LLMCallsPerMin: f64ptr(4),
				},
				ActivitySeen: domain.HindsightActivitySeen{Recall: true, LLMCalls: true},
				Runtime:      domain.HindsightRuntime{DBPoolSize: f64ptr(10), DBPoolIdle: f64ptr(8), DBPoolMax: f64ptr(10)},
				Queue:        &domain.HindsightQueue{Pending: f64ptr(3), Failed: f64ptr(0)},
			},
		},
	}
}

func f64ptr(v float64) *float64 { return &v }

// The Local tab renders the service detail body with its sections, and the
// "Open Hindsight" button appears only when a Web UI URL is configured.
func TestLocalTabRendersServiceBody(t *testing.T) {
	app := test.NewApp()
	done := make(chan struct{})
	defer close(done)

	ctrl := &hindsightCtrl{webUI: "http://127.0.0.1:9999"}
	m := NewMonitor(app, ctrl, done)
	m.rebuild()

	texts := allTexts(t, m.localBox)
	for _, want := range []string{"Service", "Status", "Healthy", "Memory", "Banks", "27", "Activity", "Recall", "18/m", "Queue", "Pending", "3", "Runtime", "DB pool active", "2 / 10"} {
		if !slices.Contains(texts, want) {
			t.Errorf("local tab lacks %q; texts = %v", want, texts)
		}
	}
	if !slices.Contains(buttonTexts(t, m.localBox), "Open Hindsight") {
		t.Error("a Web UI URL must produce the Open Hindsight button")
	}

	// Without a Web UI URL the body renders but the button must not.
	ctrl.webUI = ""
	m.rebuild()
	texts = allTexts(t, m.localBox)
	if !slices.Contains(texts, "Banks") {
		t.Errorf("service body vanished without a Web UI URL; texts = %v", texts)
	}
	if slices.Contains(buttonTexts(t, m.localBox), "Open Hindsight") {
		t.Error("Open Hindsight must be absent without a Web UI URL")
	}
}

// The Overview tab renders the compact service body: summary plus headline
// pairs, none of the detail-only rows.
func TestOverviewTabRendersServiceBody(t *testing.T) {
	app := test.NewApp()
	done := make(chan struct{})
	defer close(done)

	m := NewMonitor(app, &hindsightCtrl{}, done)
	m.rebuild()

	texts := allTexts(t, m.overviewBox)
	for _, want := range []string{"Healthy · v0.9.2", "Banks", "27", "Recall", "18/m", "LLM calls", "4/m", "Pending", "3"} {
		if !slices.Contains(texts, want) {
			t.Errorf("overview card lacks %q; texts = %v", want, texts)
		}
	}
	for _, detailOnly := range []string{"Status", "Uptime", "Retain", "Consolidation backlog"} {
		if slices.Contains(texts, detailOnly) {
			t.Errorf("overview card shows detail-only row %q", detailOnly)
		}
	}
}

// The memory ages must tick in place through the monitor's collected
// refreshers, exactly like freshness labels and quota countdowns.
func TestServiceAgeRowsAgeInPlace(t *testing.T) {
	app := test.NewApp()
	done := make(chan struct{})
	defer close(done)

	ctrl := &hindsightCtrl{}
	m := NewMonitor(app, ctrl, done)
	m.rebuild()

	beforeObjs := flatten(t, m.localBox.Objects)
	if !slices.Contains(allTexts(t, m.localBox), "3m ago") {
		t.Fatal("initial tree lacks the Last write age")
	}

	m.updateTimeTexts(ctrl.now.Add(90 * time.Second))

	afterObjs := flatten(t, m.localBox.Objects)
	if len(beforeObjs) != len(afterObjs) {
		t.Fatalf("tree size changed: %d -> %d objects", len(beforeObjs), len(afterObjs))
	}
	for i := range beforeObjs {
		if beforeObjs[i] != afterObjs[i] {
			t.Fatalf("object %d was replaced by the tick; ages must update in place", i)
		}
	}
	if !slices.Contains(allTexts(t, m.localBox), "4m ago") {
		t.Error("tick did not age the Last write row")
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

// buttonTexts collects the visible text of every button in the tree; a
// button's caption lives on the widget, not in a Label child. The Button
// case must precede the Widget case: *widget.Button is a fyne.Widget.
func buttonTexts(t *testing.T, obj fyne.CanvasObject) []string {
	t.Helper()
	var out []string
	switch o := obj.(type) {
	case *widget.Button:
		out = append(out, o.Text)
	case fyne.Widget:
		for _, child := range test.TempWidgetRenderer(t, o).Objects() {
			out = append(out, buttonTexts(t, child)...)
		}
	case *fyne.Container:
		for _, child := range o.Objects {
			out = append(out, buttonTexts(t, child)...)
		}
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
