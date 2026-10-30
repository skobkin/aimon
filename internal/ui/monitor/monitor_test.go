package monitor

import (
	"slices"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"git.skobk.in/skobkin/aimonitor/internal/config"
	"git.skobk.in/skobkin/aimonitor/internal/domain"
	"git.skobk.in/skobkin/aimonitor/internal/pace"
	"git.skobk.in/skobkin/aimonitor/internal/source"
	"git.skobk.in/skobkin/aimonitor/internal/ui"
	"git.skobk.in/skobkin/aimonitor/internal/ui/components"
)

// stubCtrl is the minimal controller for building the real window. The
// privacy preference defaults to hiding, like a fresh install.
type stubCtrl struct{}

func (stubCtrl) Instances() []config.SourceInstance           { return nil }
func (stubCtrl) States() map[domain.InstanceID]domain.Runtime { return nil }
func (stubCtrl) PaceStates() map[domain.InstanceID]pace.InstancePaces {
	return nil
}
func (stubCtrl) RefreshAll()                  {}
func (stubCtrl) RefreshOne(domain.InstanceID) {}
func (stubCtrl) OpenSettings()                {}
func (stubCtrl) HideSensitiveInfo() bool      { return true }

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
	hidden    bool
	refreshed []domain.InstanceID
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

func (c *statefulCtrl) RefreshOne(id domain.InstanceID) { c.refreshed = append(c.refreshed, id) }

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

// sensitiveValuesIn walks the container and returns its SensitiveValue
// widgets (renderer descent via flatten, as everywhere in this file).
func sensitiveValuesIn(t *testing.T, box *fyne.Container) []*components.SensitiveValue {
	t.Helper()
	var out []*components.SensitiveValue
	for _, o := range flatten(t, box.Objects) {
		if v, ok := o.(*components.SensitiveValue); ok {
			out = append(out, v)
		}
	}

	return out
}

func findSensitiveByText(t *testing.T, box *fyne.Container, text string) *components.SensitiveValue {
	t.Helper()
	for _, v := range sensitiveValuesIn(t, box) {
		if v.Text() == text {
			return v
		}
	}

	return nil
}

// A rebuild replaces the hovered SensitiveValue with a fresh widget, and
// Fyne recomputes hover only when the pointer moves — so without carried
// state the value would snap back to its mask until the next mouse move.
// The reveal key must restore the reveal on the new widget and the
// hover-out of the replaced widget must re-mask the live one.
func TestSensitiveRevealSurvivesRebuild(t *testing.T) {
	app := test.NewApp()
	done := make(chan struct{})
	defer close(done)

	ctrl := &statefulCtrl{hidden: true}
	m := NewMonitor(app, ctrl, done)
	m.rebuild()

	old := findSensitiveByText(t, m.remoteBox, "$73.42")
	if old == nil {
		t.Fatal("no Balance value found in the remote tab")
	}
	if old.DisplayText() != balanceMask {
		t.Fatalf("Balance starts unmasked: %q", old.DisplayText())
	}

	// Hovering reveals and records the row key.
	old.MouseIn(&desktop.MouseEvent{})
	if old.DisplayText() != "$73.42" {
		t.Fatalf("hover did not reveal Balance: %q", old.DisplayText())
	}

	// The rebuild swaps in a new widget; the key must keep it revealed.
	m.rebuild()
	fresh := findSensitiveByText(t, m.remoteBox, "$73.42")
	if fresh == nil {
		t.Fatal("rebuild lost the Balance value")
	}
	if fresh == old {
		t.Fatal("rebuild did not replace the widget; test proves nothing")
	}
	if fresh.DisplayText() != "$73.42" {
		t.Errorf("the replacement widget re-masked a hovered value: %q", fresh.DisplayText())
	}
	if got := findCanvasText(t, m.remoteBox, "$73.42"); got == nil {
		t.Error("the rendered text is not the revealed value after rebuild")
	}

	// The replaced widget still receives the hover-out (Fyne tracks hover
	// by object identity): it clears the key and re-masks the live widget.
	old.MouseOut()
	if fresh.DisplayText() != balanceMask {
		t.Errorf("hover-out of the replaced widget left the live value revealed: %q", fresh.DisplayText())
	}
	if got := findCanvasText(t, m.remoteBox, balanceMask); got == nil {
		t.Error("the rendered text did not return to the mask")
	}

	// Hovering a different row must not unmask Balance.
	fresh2 := findSensitiveByText(t, m.remoteBox, "$105.89")
	if fresh2 == nil {
		t.Fatal("no Purchased credits value found")
	}
	fresh2.MouseIn(&desktop.MouseEvent{})
	if got := findSensitiveByText(t, m.remoteBox, "$73.42"); got.DisplayText() != balanceMask {
		t.Errorf("hovering another row unmasked Balance: %q", got.DisplayText())
	}
}

// incompatibleCLIMessage is the authored Codex setup guidance carried by a
// config-category failure — the footer behind the report of a monitor
// window that grew to fit one long line.
const incompatibleCLIMessage = "This Codex CLI does not support the app-server rate-limit interface. Update Codex CLI or choose a compatible executable."

// failingCtrl serves one Codex instance whose only refresh failed with that
// long config detail, which the view model passes through verbatim.
type failingCtrl struct {
	stubCtrl
}

func (c *failingCtrl) Instances() []config.SourceInstance {
	return []config.SourceInstance{{
		ID: "cx-1", Kind: domain.KindCodex, Name: "Codex",
		Enabled: true, PollInterval: time.Minute, Codex: &config.CodexSettings{},
	}}
}

func (c *failingCtrl) States() map[domain.InstanceID]domain.Runtime {
	return map[domain.InstanceID]domain.Runtime{
		"cx-1": {
			Enabled: true,
			LastError: &source.Error{
				Category: source.CatConfig,
				Detail:   incompatibleCLIMessage,
			},
		},
	}
}

// A failed refresh renders its message as the card footer. That footer must
// wrap: the tab bodies live in a vertical scroll container whose minimum
// width is its content's, so one unwrapped sentence pushes the whole
// monitor window far past its normal size.
func TestFailureFooterWrapsInsteadOfWideningTheTab(t *testing.T) {
	app := test.NewApp()
	done := make(chan struct{})
	defer close(done)

	m := NewMonitor(app, &failingCtrl{}, done)
	m.rebuild()

	footer := "Last refresh failed: " + incompatibleCLIMessage
	texts := allTexts(t, m.remoteBox)
	if !slices.Contains(texts, footer) {
		t.Fatalf("remote tab lacks the failure footer; texts = %v", texts)
	}

	var label *widget.Label
	for _, o := range flatten(t, m.remoteBox.Objects) {
		if l, ok := o.(*widget.Label); ok && l.Text == footer {
			label = l

			break
		}
	}
	if label == nil {
		t.Fatal("the footer is not rendered as a label")
	}
	if label.Wrapping != fyne.TextWrapWord {
		t.Errorf("footer wrapping = %v, want word wrap", label.Wrapping)
	}

	// The tab must keep fitting the window it is given: a wide unwrapped
	// footer would demand roughly twice the monitor's width.
	if w := m.remoteBox.MinSize().Width; w > monitorSize.Width {
		t.Errorf("remote tab min width %.0f exceeds the %.0f window; the footer must wrap instead",
			w, monitorSize.Width)
	}
}

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

// stalePairCtrl serves two local llama-swap instances side by side in the
// overview grid, the right one stale: its "… · stale" status label is the
// widest header element in the report of a clipped monitor window.
type stalePairCtrl struct {
	stubCtrl
}

func (c *stalePairCtrl) Instances() []config.SourceInstance {
	return []config.SourceInstance{
		{ID: "gpu", Kind: domain.KindLlamaSwap, Name: "GPU server", Enabled: true, PollInterval: time.Minute, LlamaSwap: &config.LlamaSwapSettings{}},
		{ID: "inf", Kind: domain.KindLlamaSwap, Name: "inference server.lan", Enabled: true, PollInterval: time.Minute, LlamaSwap: &config.LlamaSwapSettings{}},
	}
}

func (c *stalePairCtrl) States() map[domain.InstanceID]domain.Runtime {
	snap := domain.LlamaSwapSnapshot{
		CollectedAt: time.Unix(1770000000, 0),
		Healthy:     true,
		RunningModels: []domain.RunningModel{
			{ID: "bge", Name: "BGE-M3 Embedding 8k (CPU)"},
			{ID: "gemma", Name: "Gemma 4 E4B LS 32k"},
		},
	}

	return map[domain.InstanceID]domain.Runtime{
		"gpu": {Enabled: true, LastSuccessAt: time.Unix(1770000000, 0), Snapshot: snap},
		// A failed refresh keeps the last-good snapshot on display (D6) and
		// derives the stale status, while a healthy poll shows a short age.
		"inf": {
			Enabled:       true,
			LastSuccessAt: time.Unix(1770000000, 0),
			LastError:     &source.Error{Category: source.CatHTTP, Detail: "connection refused"},
			Snapshot:      snap,
		},
	}
}

// A stale card status ("5h32m · stale") must not widen the monitor: the
// overview lays its cards out in two fixed columns, so one wide header
// once pushed the whole tab — and with it the floating action buttons —
// past the fixed window width, clipping everything on the right. The
// header now wraps its title instead of demanding a wider row.
func TestStaleCardDoesNotWidenTheOverview(t *testing.T) {
	app := test.NewApp()
	done := make(chan struct{})
	defer close(done)

	m := NewMonitor(app, &stalePairCtrl{}, done)
	m.rebuild()

	stale := false
	for _, s := range allTexts(t, m.overviewBox) {
		if strings.HasSuffix(s, "· stale") {
			stale = true
		}
	}
	if !stale {
		t.Fatalf("no stale card status on the overview; texts = %v", allTexts(t, m.overviewBox))
	}

	if w := m.overviewBox.MinSize().Width; w > monitorSize.Width {
		t.Errorf("overview min width %.0f exceeds the %.0f window; the stale header must not widen it", w, monitorSize.Width)
	}
	if w := m.win.Content().MinSize().Width; w > monitorSize.Width {
		t.Errorf("monitor content min width %.0f exceeds the %.0f window", w, monitorSize.Width)
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

// Tapping an Overview card selects the card's detail tab and scrolls the
// card to the top of the tab: with two local sources, the second tile
// must land its matching detail card at the viewport top.
func TestOverviewCardTapDrillsDownToLocalDetail(t *testing.T) {
	app := test.NewApp()
	done := make(chan struct{})
	defer close(done)

	m := NewMonitor(app, &stalePairCtrl{}, done)
	m.win.Resize(fyne.NewSize(540, 220)) // small viewport: the scroll must actually move
	m.Show()
	if m.tabs.SelectedIndex() != 0 {
		t.Fatalf("overview tab not selected initially: %d", m.tabs.SelectedIndex())
	}

	tile := findTapCardFor(t, m.overviewBox, "inference server.lan")
	if tile == nil {
		t.Fatal("the stale local card is not tappable")
	}
	test.Tap(tile)

	if got := m.tabs.SelectedIndex(); got != 1 {
		t.Fatalf("tap selected tab %d, want Local (1)", got)
	}

	// The scrolled content box sits at -offset, placing the target card at
	// the viewport top: box y = -(the preceding cards' heights + padding).
	// Child positions stay box-relative, so the box's own position is the
	// scroll signal.
	m.win.Resize(fyne.NewSize(540, 220)) // force the layout pass
	expected := m.localBox.Objects[0].MinSize().Height + theme.Padding()
	if pos := m.localBox.Position().Y; pos < -expected-2 || pos > -expected+2 {
		t.Errorf("content box y = %.1f, want ~-%.1f (target card at viewport top)", pos, expected)
	}
}

// The same drill-down for a remote card, which is the first card of its
// tab: offset 0 — the tap still must select the Remote tab.
func TestOverviewCardTapDrillsDownToRemoteDetail(t *testing.T) {
	app := test.NewApp()
	done := make(chan struct{})
	defer close(done)

	m := NewMonitor(app, &quotaCtrl{}, done)
	m.win.Resize(fyne.NewSize(540, 220))
	m.Show()

	tile := findTapCardFor(t, m.overviewBox, "MiniMax")
	if tile == nil {
		t.Fatal("the MiniMax overview card is not tappable")
	}
	test.Tap(tile)

	if got := m.tabs.SelectedIndex(); got != 2 {
		t.Fatalf("tap selected tab %d, want Remote (2)", got)
	}
	m.win.Resize(fyne.NewSize(540, 220))
	if pos := m.remoteBox.Position().Y; pos < -2 || pos > 2 {
		t.Errorf("first remote card y = %.1f, want ~0 (offset 0)", pos)
	}
}

func findTapCardFor(t *testing.T, box *fyne.Container, title string) *tapCard {
	t.Helper()
	for _, o := range flatten(t, box.Objects) {
		if tile, ok := o.(*tapCard); ok && slices.Contains(allTexts(t, tile.card), title) {
			return tile
		}
	}

	return nil
}

// The wrapper must not change the card's geometry: MinSize is the card's,
// so the overview grid and the top-alignment invariant stay untouched.
func TestTapCardKeepsCardGeometry(t *testing.T) {
	test.NewTempApp(t)

	card := widget.NewCard("Title", "Subtitle", widget.NewLabel("body"))
	tile := newTapCard(card, nil)
	if got, want := tile.MinSize(), card.MinSize(); got != want {
		t.Errorf("tile min size = %v, want the card's %v", got, want)
	}
}

// The hover highlight appears on MouseIn and disappears on MouseOut, and
// the highlight rectangle itself is never measured into MinSize.
func TestTapCardHoverHighlight(t *testing.T) {
	test.NewTempApp(t)

	card := widget.NewCard("Title", "", widget.NewLabel("body"))
	tile := newTapCard(card, nil)
	if !tile.highlight.Hidden {
		t.Fatal("the highlight must start hidden")
	}

	tile.MouseIn(&desktop.MouseEvent{})
	if tile.highlight.Hidden {
		t.Error("MouseIn must show the highlight")
	}
	if got, want := tile.MinSize(), card.MinSize(); got != want {
		t.Errorf("hovering changed the tile min size: %v vs %v", got, want)
	}

	tile.MouseOut()
	if !tile.highlight.Hidden {
		t.Error("MouseOut must hide the highlight")
	}
}

// An interactive child of the wrapped card must win the tap over the tile:
// Fyne's hit-test matches the wrapper first and the descendant last, so the
// button receives the click and the drill-down does not fire. Tapping the
// card's non-interactive area still drills down.
func TestTapCardChildButtonTakesPrecedence(t *testing.T) {
	app := test.NewTempApp(t)
	firedButton, firedTile := false, false

	btn := widget.NewButton("go", func() { firedButton = true })
	tile := newTapCard(widget.NewCard("Title", "Sub", btn), func() { firedTile = true })
	win := app.NewWindow("tap")
	win.SetContent(tile)
	win.Resize(fyne.NewSize(300, 200))
	win.Show()

	// Dead-center on the button: the button wins.
	btnPos := fyne.CurrentApp().Driver().AbsolutePositionForObject(btn)
	center := btnPos.Add(fyne.NewPos(btn.MinSize().Width/2, btn.MinSize().Height/2))
	test.TapCanvas(win.Canvas(), center)
	if !firedButton {
		t.Error("the inner button did not receive the tap")
	}
	if firedTile {
		t.Error("the tile must not fire when an inner button is tapped")
	}

	// The card's title corner: no interactive child there, the tile fires.
	cardPos := fyne.CurrentApp().Driver().AbsolutePositionForObject(tile.card)
	test.TapCanvas(win.Canvas(), cardPos.Add(fyne.NewPos(4, 4)))
	if !firedTile {
		t.Error("tapping the non-interactive card area must drill down")
	}
}

// iconButtons collects the icon-only (textless) buttons of the tree; the
// per-source refresh action is the only one inside a detail tab.
func iconButtons(t *testing.T, box *fyne.Container) []*widget.Button {
	t.Helper()
	var out []*widget.Button
	for _, o := range flatten(t, box.Objects) {
		if b, ok := o.(*widget.Button); ok && b.Text == "" {
			out = append(out, b)
		}
	}

	return out
}

// Every detail card header carries an icon-only per-source refresh button:
// tapping it refreshes just that instance and briefly disables the button
// as feedback, mirroring the global refresh button.
func TestDetailCardRefreshButtonRefreshesOneInstance(t *testing.T) {
	app := test.NewApp()
	done := make(chan struct{})
	defer close(done)

	ctrl := &statefulCtrl{hidden: true}
	m := NewMonitor(app, ctrl, done)
	m.Show()
	m.tabs.SelectIndex(2) // the Remote tab, where the OpenRouter card lives
	m.win.Resize(fyne.NewSize(540, 660))

	btns := iconButtons(t, m.remoteBox)
	if len(btns) != 1 {
		t.Fatalf("remote tab has %d icon-only buttons, want one per card", len(btns))
	}
	if btns[0].Disabled() {
		t.Fatal("the refresh button must start enabled")
	}
	test.Tap(btns[0])
	if got := ctrl.refreshed; len(got) != 1 || got[0] != "or-1" {
		t.Fatalf("RefreshOne calls = %v, want [or-1]", got)
	}
	if !btns[0].Disabled() {
		t.Error("the button must disable briefly as refresh feedback")
	}
}

// The Overview cards carry no refresh action: the per-source refresh lives
// in the detail headers only.
func TestOverviewHasNoPerSourceRefreshButtons(t *testing.T) {
	app := test.NewApp()
	done := make(chan struct{})
	defer close(done)

	m := NewMonitor(app, &stalePairCtrl{}, done)
	m.Show()

	if btns := iconButtons(t, m.overviewBox); len(btns) != 0 {
		t.Errorf("overview has %d icon-only buttons, want none", len(btns))
	}
}

// The header action must stay compact: the card grows by no more than the
// icon plus padding — no new row, no text chrome — and the freshness label
// stays rightmost in the header.
func TestRefreshButtonHeaderStaysCompact(t *testing.T) {
	test.NewTempApp(t)

	vm := ui.CardVM{Instance: "x", Title: "OpenRouter", Status: "1s ago"}
	body := widget.NewLabel("body")
	withAction, _ := components.NewSourceCardWithHeaderAction(vm, body,
		widget.NewButtonWithIcon("", theme.ViewRefreshIcon(), nil))
	plain, _ := components.NewSourceCard(vm, body)

	growth := withAction.MinSize().Width - plain.MinSize().Width
	if growth <= 0 {
		t.Error("the header action added no width; it is missing")
	}
	if max := theme.IconInlineSize() + 6*theme.Padding(); growth > max {
		t.Errorf("header grew %.1f wider than the icon budget %.1f", growth, max)
	}
}

// The queued drill-down offset applies clamped once the scroller knows its
// viewport, and waits (instead of being zeroed) while the scroller is
// unsized — a tab switched inside a tap handler is laid out at frame time.
func TestDetailScrollPendingAppliesWhenSized(t *testing.T) {
	test.NewTempApp(t)

	box := container.NewVBox(widget.NewLabel("a"), widget.NewLabel("b"), widget.NewLabel("c"))
	d := newDetailScroll(box)
	d.Resize(fyne.NewSize(100, 40))

	d.scrollToY(1000) // far past the tail: must clamp, not overscroll
	if d.hasPending {
		t.Fatal("pending must be consumed once the viewport size is known")
	}
	if want := box.MinSize().Height - 40; d.scroll.Offset.Y < want-1 {
		t.Errorf("offset = %v, want clamped to ~%v", d.scroll.Offset.Y, want)
	}

	unsized := newDetailScroll(container.NewVBox(widget.NewLabel("x")))
	unsized.scrollToY(10)
	if !unsized.hasPending {
		t.Fatal("an unsized scroller must retain the pending offset for the retry")
	}
}

// allTexts collects the visible text of every label in the tree. The Label
// and Text cases must precede the Widget case: *widget.Label and
// *canvas.Text are fyne.CanvasObjects, and a Label is a fyne.Widget too.
func allTexts(t *testing.T, obj fyne.CanvasObject) []string {
	t.Helper()
	var out []string
	switch o := obj.(type) {
	case *fyne.Container:
		for _, child := range o.Objects {
			out = append(out, allTexts(t, child)...)
		}
	case *widget.Label:
		out = append(out, o.Text)
	case *canvas.Text:
		out = append(out, o.Text)
	case fyne.Widget:
		for _, child := range test.TempWidgetRenderer(t, o).Objects() {
			out = append(out, allTexts(t, child)...)
		}
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
