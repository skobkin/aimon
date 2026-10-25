package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"sync"
	"testing"
	"time"

	"git.skobk.in/skobkin/aimonitor/internal/buildinfo"
	"git.skobk.in/skobkin/aimonitor/internal/config"
	"git.skobk.in/skobkin/aimonitor/internal/credentials"
	"git.skobk.in/skobkin/aimonitor/internal/domain"
	"git.skobk.in/skobkin/aimonitor/internal/pace"
	"git.skobk.in/skobkin/aimonitor/internal/source"
)

// TestAppMetadataCarriesBuildInfo guards the identity flow: the metadata
// name and version must come from buildinfo (stamped at link time for
// tagged releases), never be hardcoded here.
func TestAppMetadataCarriesBuildInfo(t *testing.T) {
	m := appMetadata()

	if m.ID != appID {
		t.Errorf("metadata ID = %q, want the app ID %q", m.ID, appID)
	}
	if m.Name != buildinfo.AppName {
		t.Errorf("metadata Name = %q, want buildinfo.AppName %q", m.Name, buildinfo.AppName)
	}
	if m.Version != buildinfo.Version {
		t.Errorf("metadata Version = %q, want buildinfo.Version %q", m.Version, buildinfo.Version)
	}
	if !m.Migrations["fyneDo"] {
		t.Error("appMetadata must opt into the fyne.Do threading model")
	}
}

// memStore is a fake credential store; it never touches a keychain.
type memStore struct {
	mu     sync.Mutex
	secret string
}

func (s *memStore) Status(ctx context.Context) credentials.Status {
	return credentials.Status{State: credentials.StateAvailable}
}

func (s *memStore) Get(ctx context.Context, id credentials.AccountID) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.secret == "" {
		return "", credentials.ErrNotFound
	}

	return s.secret, nil
}

func (s *memStore) Set(ctx context.Context, id credentials.AccountID, secret string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.secret = secret

	return nil
}

func (s *memStore) Delete(ctx context.Context, id credentials.AccountID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.secret = ""

	return nil
}

func TestOverrideStorePrefersTypedValue(t *testing.T) {
	inner := &memStore{secret: "stored-value"}
	account := credentials.AccountID("source/test-a/api-key")
	o := &overrideStore{inner: inner, overrides: map[credentials.AccountID]string{account: "typed-value"}}

	got, err := o.Get(context.Background(), account)
	if err != nil || got != "typed-value" {
		t.Errorf("Get with override = %q, %v; want typed-value", got, err)
	}
}

func TestOverrideStoreDelegatesWithoutOverride(t *testing.T) {
	inner := &memStore{secret: "stored-value"}
	o := &overrideStore{inner: inner, overrides: map[credentials.AccountID]string{}}

	got, err := o.Get(context.Background(), credentials.AccountID("source/test-a/api-key"))
	if err != nil || got != "stored-value" {
		t.Errorf("Get without override = %q, %v; want stored-value", got, err)
	}
}

func TestOverrideStoreDelegatesOtherAccounts(t *testing.T) {
	inner := &memStore{secret: "stored-value"}
	o := &overrideStore{
		inner:     inner,
		overrides: map[credentials.AccountID]string{credentials.AccountID("source/test-a/api-key"): "typed-value"},
	}

	got, err := o.Get(context.Background(), credentials.AccountID("source/test-b/api-key"))
	if err != nil || got != "stored-value" {
		t.Errorf("Get for other account = %q, %v; want stored-value", got, err)
	}
}

// A multi-slot kind layers each typed secret over its own slot's stored
// credential; every other slot keeps using the keychain untouched.
func TestOverrideStoreLayersTypedSecretsPerSlot(t *testing.T) {
	inner := &memStore{secret: "stored-api"}
	api := credentials.AccountID("source/or-a/api-key")
	mgmt := credentials.AccountID("source/or-a/management-key")
	o := &overrideStore{inner: inner, overrides: map[credentials.AccountID]string{mgmt: "typed-management"}}

	if got, _ := o.Get(context.Background(), mgmt); got != "typed-management" {
		t.Errorf("management slot = %q, want the typed secret", got)
	}
	if got, _ := o.Get(context.Background(), api); got != "stored-api" {
		t.Errorf("api slot = %q, want the stored credential", got)
	}
}

// gpuInst builds a minimal valid amdgputop-web instance.
func gpuInst(id, name, baseURL string, enabled bool) config.SourceInstance {
	return config.SourceInstance{
		ID:           domain.InstanceID(id),
		Kind:         domain.KindAMDGPUWeb,
		Name:         name,
		Enabled:      enabled,
		PollInterval: time.Hour, // no scheduled churn during the test
		AMDGPUWeb:    &config.AMDGPUWebSettings{BaseURL: baseURL},
	}
}

// A failed SaveInstances (validation or persistence) must leave the
// authoritative list and the runtime completely untouched — no partial
// application of an unsaved configuration.
func TestSaveInstancesFailureLeavesStateUntouched(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	a := &App{
		log:         logger,
		client:      &http.Client{},
		creds:       &memStore{},
		mgr:         source.NewManager(logger),
		adapters:    map[domain.InstanceID]adapterEntry{},
		pace:        pace.NewTracker(),
		paceWindows: map[domain.InstanceID]time.Duration{},
	}
	defer a.mgr.Stop()

	good := []config.SourceInstance{
		gpuInst("gpu-a", "GPU A", "http://127.0.0.1:49101", true),
	}
	a.applyInstances(good)

	// Invalid candidate: duplicate instance IDs fail validation inside
	// SaveInstances before anything is persisted or applied.
	bad := []config.SourceInstance{
		gpuInst("gpu-a", "GPU A", "http://127.0.0.1:49101", true),
		gpuInst("gpu-a", "Duplicate", "http://127.0.0.1:49102", true),
	}
	if err := a.SaveInstances(bad); err == nil {
		t.Fatal("the invalid candidate must be rejected")
	}

	if got := a.Instances(); len(got) != 1 || got[0].Name != "GPU A" {
		t.Errorf("authoritative list changed: %+v", got)
	}
	if states := a.mgr.States(); len(states) != 1 {
		t.Errorf("runtime changed after a failed save: %d entries", len(states))
	}
	if _, ok := a.mgr.States()["gpu-a"]; !ok {
		t.Error("the running instance must be untouched")
	}
}

// Rename, reorder and enable/disable must reuse the built adapter (so the
// manager never restarts the poller); a material settings change must
// rebuild it (so the manager clears the old endpoint's snapshot).
func TestApplyInstancesAdapterReuseAndRebuild(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	a := &App{
		log:         logger,
		client:      &http.Client{},
		creds:       &memStore{},
		mgr:         source.NewManager(logger),
		adapters:    map[domain.InstanceID]adapterEntry{},
		pace:        pace.NewTracker(),
		paceWindows: map[domain.InstanceID]time.Duration{},
	}
	defer a.mgr.Stop()

	a.applyInstances([]config.SourceInstance{
		gpuInst("gpu-a", "Old name", "http://127.0.0.1:49001", true),
		gpuInst("gpu-b", "GPU B", "http://127.0.0.1:49002", true),
	})
	first := a.adapters["gpu-a"].src

	a.applyInstances([]config.SourceInstance{
		gpuInst("gpu-b", "GPU B renamed", "http://127.0.0.1:49002", false), // reordered first
		gpuInst("gpu-a", "New name", "http://127.0.0.1:49001", true),       // renamed
	})
	if a.adapters["gpu-a"].src != first {
		t.Error("rename/reorder/enable changes must reuse the adapter (no poller restart)")
	}

	// Material settings change: a new adapter pointer, which the manager
	// treats as a reset-worthy reconfiguration.
	a.applyInstances([]config.SourceInstance{
		gpuInst("gpu-a", "New name", "http://127.0.0.1:49003", true),
	})
	if a.adapters["gpu-a"].src == first {
		t.Error("a settings change must rebuild the adapter")
	}

	// The authoritative list follows the last applied one.
	if got := a.Instances(); len(got) != 1 || got[0].ID != "gpu-a" {
		t.Errorf("Instances() = %+v, want only gpu-a", got)
	}
}

// dispatchQueue stands in for fyne.Do in tests: calls enqueue, and the
// event loop drains them later — the pending flag stays set in between.
type dispatchQueue struct{ queued []func() }

func (d *dispatchQueue) dispatch(f func()) { d.queued = append(d.queued, f) }

func (d *dispatchQueue) drain() {
	// Update enqueued during a pass run in the next pass, like an event
	// loop draining a queue that callbacks keep refilling.
	for len(d.queued) > 0 {
		batch := d.queued
		d.queued = nil
		for _, f := range batch {
			f()
		}
	}
}

// N notifications landing inside one dispatch window must collapse into a
// single UI update: every poller finishing at once costs one rebuild, not
// one per source.
func TestUICoalescerCollapsesBursts(t *testing.T) {
	var c uiCoalescer
	q := &dispatchQueue{}
	runs := 0
	update := func() { runs++ }

	for i := 0; i < 10; i++ {
		c.schedule(q.dispatch, update)
	}
	if got := len(q.queued); got != 1 {
		t.Fatalf("burst of 10 notifications queued %d updates, want 1", got)
	}
	q.drain()
	if runs != 1 {
		t.Errorf("drain ran the update %d times, want 1", runs)
	}

	// Once the dispatched update ran, the next notification schedules a
	// fresh one — nothing is dropped across generations.
	c.schedule(q.dispatch, update)
	if got := len(q.queued); got != 1 {
		t.Errorf("notification after dispatch queued %d updates, want 1", got)
	}
	q.drain()
	if runs != 2 {
		t.Errorf("second drain ran the update %d times total, want 2", runs)
	}
}

// A notification arriving during the dispatched update itself (the flag is
// already cleared when it runs) must schedule a follow-up, not be absorbed.
func TestUICoalescerRearmsDuringDispatch(t *testing.T) {
	var c uiCoalescer
	q := &dispatchQueue{}
	runs := 0
	var update func()
	update = func() {
		runs++
		if runs == 1 {
			// Re-entrant notification while the UI update is in flight.
			c.schedule(q.dispatch, update)
		}
	}

	c.schedule(q.dispatch, update)
	if got := len(q.queued); got != 1 {
		t.Fatalf("re-entrant notification left %d queued updates, want 1 (absorbed scheduling would leave 0)", got)
	}
	q.drain()
	if runs != 2 {
		t.Errorf("re-entrant notification produced %d updates, want 2", runs)
	}
}

// --- S7 pace wiring ---

// miniInst builds a minimal MiniMax instance: a pace-capable kind with a
// fixed endpoint, so applyInstances builds a real adapter without I/O.
func miniInst(id string, paceWindow time.Duration) config.SourceInstance {
	return config.SourceInstance{
		ID:           domain.InstanceID(id),
		Kind:         domain.KindMiniMax,
		Name:         "MiniMax " + id,
		Enabled:      true,
		PollInterval: time.Hour, // no scheduled churn during the test
		PaceWindow:   paceWindow,
		MiniMax:      &config.MiniMaxSettings{},
	}
}

func quotaSnapAt(at time.Time, used float64) domain.QuotaSnapshot {
	return domain.QuotaSnapshot{
		Kind:        domain.KindMiniMax,
		CollectedAt: at,
		Windows: []domain.QuotaWindow{
			{ID: "interval", Name: "5-hour", UsedFraction: used, RemainingFraction: 1 - used},
		},
	}
}

func paceTestApp() *App {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	return &App{
		log:         logger,
		client:      &http.Client{},
		creds:       &memStore{},
		mgr:         source.NewManager(logger),
		adapters:    map[domain.InstanceID]adapterEntry{},
		pace:        pace.NewTracker(),
		paceWindows: map[domain.InstanceID]time.Duration{},
	}
}

// recordPace must skip everything that would poison the series: non-pace
// kinds, failed refreshes (the retained last-good snapshot would
// fabricate a flat interval), missing snapshots, and OpenRouter keys
// without usage data.
func TestRecordPaceSkips(t *testing.T) {
	a := paceTestApp()
	at := time.Unix(1770000000, 0)

	gpu := gpuInst("gpu-a", "GPU", "http://127.0.0.1:49001", true)
	a.recordPaceSnapshot("gpu-a", gpu, domain.Runtime{Snapshot: domain.GPUSnapshot{CollectedAt: at}})
	a.recordPaceSnapshot("gpu-a", gpu, domain.Runtime{LastError: errors.New("boom")})

	mm := miniInst("mm", 0)
	a.recordPaceSnapshot("mm", mm, domain.Runtime{Snapshot: quotaSnapAt(at, 0.1), LastError: errors.New("boom")})
	a.recordPaceSnapshot("mm", mm, domain.Runtime{})

	or := config.SourceInstance{
		ID: domain.InstanceID("or"), Kind: domain.KindOpenRouter, Name: "OpenRouter",
		Enabled: true, PollInterval: time.Hour, OpenRouter: &config.OpenRouterSettings{},
	}
	a.recordPaceSnapshot("or", or, domain.Runtime{Snapshot: domain.OpenRouterSnapshot{CollectedAt: at}})

	if got := a.pace.States(); len(got) != 0 {
		t.Errorf("tracker recorded %v, want nothing", got)
	}
}

// Quota, codex and spend snapshots all land in the tracker through their
// respective entry points.
func TestRecordPaceFeedsTracker(t *testing.T) {
	a := paceTestApp()
	at := time.Unix(1770000000, 0)
	mm := miniInst("mm", 0)

	a.recordPaceSnapshot("mm", mm, domain.Runtime{Snapshot: quotaSnapAt(at, 0.1)})
	a.recordPaceSnapshot("mm", mm, domain.Runtime{Snapshot: quotaSnapAt(at.Add(5*time.Minute), 0.2)})
	ip, ok := a.pace.States()["mm"]
	if !ok {
		t.Fatal("quota series missing")
	}
	w, ok := ip.Windows["interval"]
	if !ok || w.State != pace.StateActive {
		t.Fatalf("interval estimate = %+v (ok %v), want Active", w, ok)
	}
	// 0.1 fraction over 5 minutes ⇒ 1.2/h.
	if math.Abs(w.SpeedFrac-1.2) > 1e-9 {
		t.Errorf("speed = %v, want 1.2", w.SpeedFrac)
	}

	cd := miniInst("cd", 0)
	cd.Kind = domain.KindCodex
	cd.MiniMax = nil
	cd.Codex = &config.CodexSettings{}
	a.recordPaceSnapshot("cd", cd, domain.Runtime{Snapshot: domain.CodexSnapshot{
		CollectedAt: at,
		Windows:     []domain.QuotaWindow{{ID: "codex-primary", Name: "5-hour", UsedFraction: 0.3, RemainingFraction: 0.7}},
	}})
	ip, ok = a.pace.States()["cd"]
	if !ok {
		t.Fatal("codex series missing")
	}
	if _, ok := ip.Windows["codex-primary"]; !ok {
		t.Errorf("codex windows = %v", ip.Windows)
	}

	or := config.SourceInstance{
		ID: domain.InstanceID("or"), Kind: domain.KindOpenRouter, Name: "OpenRouter",
		Enabled: true, PollInterval: time.Hour, OpenRouter: &config.OpenRouterSettings{},
	}
	a.recordPaceSnapshot("or", or, domain.Runtime{Snapshot: domain.OpenRouterSnapshot{
		CollectedAt: at, Key: &domain.OpenRouterKeyUsage{UsageTotal: 5},
	}})
	a.recordPaceSnapshot("or", or, domain.Runtime{Snapshot: domain.OpenRouterSnapshot{
		CollectedAt: at.Add(10 * time.Minute), Key: &domain.OpenRouterKeyUsage{UsageTotal: 7},
	}})
	ip, ok = a.pace.States()["or"]
	if !ok || ip.Spend == nil {
		t.Fatalf("spend estimate missing: %+v", ip)
	}
	// $2 over 10 minutes ⇒ $12/h.
	if math.Abs(ip.Spend.SpeedCount-12) > 1e-9 {
		t.Errorf("spend speed = %v, want 12", ip.Spend.SpeedCount)
	}
}

// Pace history survives exactly the edits that keep the adapter: an
// interval-only change and a rename keep the series, a provider-settings
// change and a removal drop it.
func TestApplyInstancesPaceHistoryMatrix(t *testing.T) {
	at := time.Unix(1770000000, 0)
	seed := func(a *App) {
		a.pace.ObserveQuota("mm", at, quotaSnapAt(at, 0.1).Windows)
		a.pace.ObserveQuota("mm", at.Add(30*time.Minute), quotaSnapAt(at.Add(30*time.Minute), 0.2).Windows)
	}
	activeExists := func(a *App, what string) {
		t.Helper()
		ip, ok := a.pace.States()["mm"]
		if !ok {
			t.Fatalf("%s: series missing", what)
		}
		w, ok := ip.Windows["interval"]
		if !ok || w.State != pace.StateActive {
			t.Fatalf("%s: estimate = %+v, want Active", what, w)
		}
	}

	t.Run("interval-only keeps history", func(t *testing.T) {
		a := paceTestApp()
		defer a.mgr.Stop()
		first := miniInst("mm", time.Hour)
		a.applyInstances([]config.SourceInstance{first})
		seed(a)
		second := first
		second.PollInterval = 5 * time.Minute
		a.applyInstances([]config.SourceInstance{second})
		activeExists(a, "interval-only")
	})

	t.Run("provider settings change resets", func(t *testing.T) {
		a := paceTestApp()
		defer a.mgr.Stop()
		zaiAt := func(baseURL string) config.SourceInstance {
			inst := miniInst("mm", time.Hour)
			inst.Kind = domain.KindZAI
			inst.MiniMax = nil
			inst.ZAI = &config.ZAISettings{BaseURL: baseURL}

			return inst
		}
		a.applyInstances([]config.SourceInstance{zaiAt("https://api.z.ai")})
		seed(a)
		// A base-URL change is a provider-settings change: new adapter,
		// new upstream identity, history dropped.
		a.applyInstances([]config.SourceInstance{zaiAt("https://bigmodel.cn")})
		if _, ok := a.pace.States()["mm"]; ok {
			t.Error("history survived an adapter rebuild")
		}
	})

	t.Run("removal resets", func(t *testing.T) {
		a := paceTestApp()
		defer a.mgr.Stop()
		mm, other := miniInst("mm", time.Hour), miniInst("other", time.Hour)
		a.applyInstances([]config.SourceInstance{mm, other})
		seed(a)
		a.applyInstances([]config.SourceInstance{other})
		if _, ok := a.pace.States()["mm"]; ok {
			t.Error("history survived instance removal")
		}
	})
}

// A pace-window-only change must recompute immediately from retained
// history — no re-collecting, no adapter rebuild.
func TestApplyInstancesPaceWindowRecompute(t *testing.T) {
	a := paceTestApp()
	defer a.mgr.Stop()
	at := time.Unix(1770000000, 0)

	wide := miniInst("mm", time.Hour)
	a.applyInstances([]config.SourceInstance{wide})
	a.pace.ObserveQuota("mm", at, quotaSnapAt(at, 0.1).Windows)
	a.pace.ObserveQuota("mm", at.Add(30*time.Minute), quotaSnapAt(at.Add(30*time.Minute), 0.2).Windows)
	activeExists := func(want pace.State, what string) {
		t.Helper()
		w := a.pace.States()["mm"].Windows["interval"]
		if w.State != want {
			t.Fatalf("%s: state = %v, want %v", what, w.State, want)
		}
	}
	activeExists(pace.StateActive, "60m window")

	narrow := wide
	narrow.PaceWindow = 10 * time.Minute
	a.applyInstances([]config.SourceInstance{narrow})
	activeExists(pace.StateCollecting, "10m window: 30m-old endpoint left the window")
}

// An adapter rebuild resets the tracker — history included — but the pace
// window is configuration, not history: the fresh tracker must come back
// with the configured window instead of silently reverting to the default
// while Settings keeps showing the chosen preset.
func TestApplyInstancesRebuildKeepsPaceWindow(t *testing.T) {
	a := paceTestApp()
	defer a.mgr.Stop()
	at := time.Unix(1770000000, 0)

	zaiAt := func(baseURL string) config.SourceInstance {
		inst := miniInst("mm", time.Hour)
		inst.Kind = domain.KindZAI
		inst.MiniMax = nil
		inst.ZAI = &config.ZAISettings{BaseURL: baseURL}

		return inst
	}
	a.applyInstances([]config.SourceInstance{zaiAt("https://api.z.ai")})
	a.pace.ObserveQuota("mm", at, quotaSnapAt(at, 0.1).Windows)
	a.pace.ObserveQuota("mm", at.Add(30*time.Minute), quotaSnapAt(at.Add(30*time.Minute), 0.2).Windows)

	// A base-URL change rebuilds the adapter and drops the history.
	a.applyInstances([]config.SourceInstance{zaiAt("https://bigmodel.cn")})
	if _, ok := a.pace.States()["mm"]; ok {
		t.Fatal("history survived an adapter rebuild")
	}

	// The fresh series must be measured with the configured 60m window: a
	// 30m span fits it (Active), but not the 20m default (Collecting) —
	// the reverted-window bug read exactly as the latter.
	a.pace.ObserveQuota("mm", at, quotaSnapAt(at, 0.1).Windows)
	a.pace.ObserveQuota("mm", at.Add(30*time.Minute), quotaSnapAt(at.Add(30*time.Minute), 0.2).Windows)
	w := a.pace.States()["mm"].Windows["interval"]
	if w.State != pace.StateActive {
		t.Errorf("after rebuild: state = %v, want Active (60m window retained, not the 20m default)", w.State)
	}
}

// The locked half of an apply must not touch the tracker: until Manager.Apply
// has waited out a replaced loop, that loop's final completion callback can
// still record its old snapshot, and a history cleared earlier would let it
// repopulate the fresh series. The drop therefore runs in settlePace, after
// Apply returns — which also wipes anything recorded in between.
func TestApplyInstancesDefersPaceResetUntilAfterApply(t *testing.T) {
	a := paceTestApp()
	defer a.mgr.Stop()
	at := time.Unix(1770000000, 0)

	zaiAt := func(baseURL string) config.SourceInstance {
		inst := miniInst("mm", time.Hour)
		inst.Kind = domain.KindZAI
		inst.MiniMax = nil
		inst.ZAI = &config.ZAISettings{BaseURL: baseURL}

		return inst
	}
	a.applyInstances([]config.SourceInstance{zaiAt("https://api.z.ai")})
	a.pace.ObserveQuota("mm", at, quotaSnapAt(at, 0.1).Windows)
	a.pace.ObserveQuota("mm", at.Add(30*time.Minute), quotaSnapAt(at.Add(30*time.Minute), 0.2).Windows)

	// The base-URL change rebuilds the adapter: the locked half queues the
	// drop but leaves every sample in place — Apply has not run yet, so an
	// old loop may still be recording.
	regs, stale := a.applyInstancesLocked([]config.SourceInstance{zaiAt("https://bigmodel.cn")})
	if len(stale) != 1 || stale[0] != domain.InstanceID("mm") {
		t.Fatalf("stale pace ids = %v, want [mm]", stale)
	}
	if w, ok := a.pace.States()["mm"].Windows["interval"]; !ok || w.State != pace.StateActive {
		t.Fatalf("locked phase dropped the history before Apply: %+v (ok %v)", w, ok)
	}

	// Only the post-Apply half wipes the history — and restores the window,
	// which Reset drops together with the samples.
	a.mgr.Apply(regs)
	if rePace := a.settlePace([]config.SourceInstance{zaiAt("https://bigmodel.cn")}, stale); !rePace {
		t.Error("settlePace reported no user-visible change for a rebuilt pace instance")
	}
	if _, ok := a.pace.States()["mm"]; ok {
		t.Fatal("history survived the post-Apply reset")
	}

	// The rebuilt series is measured with the configured 60m window, not
	// the 20m default (30m span fits the former, not the latter).
	a.pace.ObserveQuota("mm", at, quotaSnapAt(at, 0.1).Windows)
	a.pace.ObserveQuota("mm", at.Add(30*time.Minute), quotaSnapAt(at.Add(30*time.Minute), 0.2).Windows)
	if w := a.pace.States()["mm"].Windows["interval"]; w.State != pace.StateActive {
		t.Errorf("after rebuild: state = %v, want Active (60m window retained)", w.State)
	}
}

// A build failure leaves an instance configured but unregistered — Apply
// detaches its loop without a replacement — so the window mirror must be
// pruned against the configured list, not the registrations: the window
// dropPaceHistoryLocked restores for such an instance would otherwise be
// forgotten, and the next save would re-announce it as a user-visible
// pace change nothing visible justifies.
func TestSettlePaceKeepsWindowMirrorForUnregisteredInstance(t *testing.T) {
	a := paceTestApp()
	defer a.mgr.Stop()

	inst := miniInst("mm", time.Hour)
	inst.Kind = domain.KindZAI
	inst.MiniMax = nil
	inst.ZAI = &config.ZAISettings{BaseURL: "https://api.z.ai"}
	list := []config.SourceInstance{inst}

	// No registrations at all: the adapter build failed for the still-
	// configured instance, while its history still had to drop.
	if rePace := a.settlePace(list, []domain.InstanceID{"mm"}); !rePace {
		t.Fatal("settlePace reported no change for a dropped history")
	}
	if got, ok := a.paceWindows["mm"]; !ok || got != inst.PaceWindow {
		t.Fatalf("window mirror after settle = %v (ok %v), want the configured window", got, ok)
	}

	// The follow-up save must not re-announce the window it never lost.
	if rePace := a.settlePace(list, nil); rePace {
		t.Error("second settle reported a user-visible pace change")
	}
}

// Only the api-key credential feeds a pace series: the OpenRouter spend
// total is the API key's own usage counter, while the management key
// measures the account credit balance the pace never reads. Rotating the
// management key must therefore keep the history (the guard used to drop
// on any of a kind's slots); rotating the API key still drops it.
func TestPaceGuardIgnoresManagementKeySlot(t *testing.T) {
	at := time.Unix(1770000000, 0)
	a := paceTestApp()
	defer a.mgr.Stop()
	a.creds = newPaceGuardStore(&memStore{secret: "or-key-v1"}, a.paceCredentialChanged)
	or := config.SourceInstance{
		ID: domain.InstanceID("or"), Kind: domain.KindOpenRouter, Name: "OpenRouter",
		Enabled: true, PollInterval: time.Hour, OpenRouter: &config.OpenRouterSettings{},
	}
	a.applyInstances([]config.SourceInstance{or})
	a.pace.ObserveSpend("or", at, 5)
	a.pace.ObserveSpend("or", at.Add(10*time.Minute), 7)

	if err := a.creds.Set(context.Background(), "source/or/management-key", "or-mgmt-v2"); err != nil {
		t.Fatal(err)
	}
	if ip, ok := a.pace.States()["or"]; !ok || ip.Spend == nil {
		t.Errorf("management-key rotation wiped the spend series: %+v (ok %v)", ip, ok)
	}

	if err := a.creds.Set(context.Background(), "source/or/api-key", "or-api-v2"); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.pace.States()["or"]; ok {
		t.Error("api-key rotation kept the spend series")
	}
}

// A credential change swaps the upstream counter a pace series measures
// even though the adapter — whose identity is the unchanged provider
// settings — keeps running: the guarded store drops the owning instance's
// history, keeps it when the same secret is re-saved, and ignores accounts
// no configured instance owns.
func TestCredentialChangeResetsPaceHistory(t *testing.T) {
	at := time.Unix(1770000000, 0)
	account := credentials.AccountID("source/mm/api-key")

	newGuardedApp := func(secret string) *App {
		a := paceTestApp()
		a.creds = newPaceGuardStore(&memStore{secret: secret}, a.paceCredentialChanged)
		a.applyInstances([]config.SourceInstance{miniInst("mm", time.Hour)})
		a.pace.ObserveQuota("mm", at, quotaSnapAt(at, 0.1).Windows)
		a.pace.ObserveQuota("mm", at.Add(30*time.Minute), quotaSnapAt(at.Add(30*time.Minute), 0.2).Windows)

		return a
	}
	active := func(a *App, what string) {
		t.Helper()
		w, ok := a.pace.States()["mm"].Windows["interval"]
		if !ok || w.State != pace.StateActive {
			t.Fatalf("%s: estimate = %+v (ok %v), want Active", what, w, ok)
		}
	}

	t.Run("changed secret resets", func(t *testing.T) {
		a := newGuardedApp("key-v1")
		defer a.mgr.Stop()
		if err := a.creds.Set(context.Background(), account, "key-v2"); err != nil {
			t.Fatal(err)
		}
		if _, ok := a.pace.States()["mm"]; ok {
			t.Fatal("history survived a credential change")
		}
		// The fresh series comes back with the configured window (Reset
		// drops it with the samples): a 30m span fits 60m, not the default.
		a.pace.ObserveQuota("mm", at, quotaSnapAt(at, 0.1).Windows)
		a.pace.ObserveQuota("mm", at.Add(30*time.Minute), quotaSnapAt(at.Add(30*time.Minute), 0.2).Windows)
		active(a, "after credential change")
	})

	t.Run("same secret keeps history", func(t *testing.T) {
		a := newGuardedApp("key-v1")
		defer a.mgr.Stop()
		if err := a.creds.Set(context.Background(), account, "key-v1"); err != nil {
			t.Fatal(err)
		}
		active(a, "re-saved same secret")
	})

	t.Run("foreign account ignored", func(t *testing.T) {
		a := newGuardedApp("key-v1")
		defer a.mgr.Stop()
		if err := a.creds.Set(context.Background(), "source/other/api-key", "key-v2"); err != nil {
			t.Fatal(err)
		}
		active(a, "unrelated account changed")
	})

	t.Run("removed secret resets", func(t *testing.T) {
		a := newGuardedApp("key-v1")
		defer a.mgr.Stop()
		if err := a.creds.Delete(context.Background(), account); err != nil {
			t.Fatal(err)
		}
		if _, ok := a.pace.States()["mm"]; ok {
			t.Fatal("history survived a credential removal")
		}
	})
}
