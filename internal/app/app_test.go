package app

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"git.skobk.in/skobkin/aimonitor/internal/config"
	"git.skobk.in/skobkin/aimonitor/internal/credentials"
	"git.skobk.in/skobkin/aimonitor/internal/domain"
	"git.skobk.in/skobkin/aimonitor/internal/source"
)

// TestMetadataMirrorsFyneAppToml guards the duplication between
// FyneApp.toml (packaged apps) and appMetadata (every other run): the
// values must stay identical or the fyne.Do migration flag and version
// drift between build modes.
func TestMetadataMirrorsFyneAppToml(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "FyneApp.toml"))
	if err != nil {
		t.Fatalf("read FyneApp.toml: %v", err)
	}
	toml := string(raw)
	m := appMetadata()

	for _, field := range []struct{ key, value string }{
		{"ID", m.ID},
		{"Name", m.Name},
		{"Version", m.Version},
	} {
		if !strings.Contains(toml, fmt.Sprintf("%s = %q", field.key, field.value)) {
			t.Errorf("FyneApp.toml does not set %s = %q", field.key, field.value)
		}
	}
	if !strings.Contains(toml, fmt.Sprintf("Build = %d", m.Build)) {
		t.Errorf("FyneApp.toml does not set Build = %d", m.Build)
	}
	if !m.Migrations["fyneDo"] {
		t.Error("appMetadata must opt into the fyne.Do threading model")
	}
	if !strings.Contains(toml, "fyneDo = true") {
		t.Error("FyneApp.toml does not opt into the fyne.Do threading model")
	}
	if m.ID != appID {
		t.Errorf("metadata ID = %q, want the app ID %q", m.ID, appID)
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
		log:      logger,
		client:   &http.Client{},
		creds:    &memStore{},
		mgr:      source.NewManager(logger),
		adapters: map[domain.InstanceID]adapterEntry{},
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
		log:      logger,
		client:   &http.Client{},
		creds:    &memStore{},
		mgr:      source.NewManager(logger),
		adapters: map[domain.InstanceID]adapterEntry{},
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
