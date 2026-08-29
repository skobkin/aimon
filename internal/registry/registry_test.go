package registry

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"git.skobk.in/skobkin/aimonitor/internal/config"
	"git.skobk.in/skobkin/aimonitor/internal/credentials"
	"git.skobk.in/skobkin/aimonitor/internal/domain"
)

func TestAllRegistersEveryKindOnce(t *testing.T) {
	all := All()
	if len(all) != 6 {
		t.Fatalf("registered %d kinds, want 6", len(all))
	}
	seen := map[domain.SourceKind]bool{}
	for _, d := range all {
		if seen[d.Kind] {
			t.Errorf("kind %q registered twice", d.Kind)
		}
		seen[d.Kind] = true
	}
	for _, kind := range []domain.SourceKind{domain.KindAMDGPUWeb, domain.KindLlamaSwap, domain.KindHindsight, domain.KindMiniMax, domain.KindZAI, domain.KindOpenRouter} {
		if !seen[kind] {
			t.Errorf("kind %q missing from the registry", kind)
		}
		if _, ok := Get(kind); !ok {
			t.Errorf("Get(%q) not found", kind)
		}
	}
	if _, ok := Get("not-a-kind"); ok {
		t.Error("unknown kind must not resolve")
	}
}

func TestDefinitionMetadata(t *testing.T) {
	cases := []struct {
		kind     domain.SourceKind
		category domain.Category
		name     string
		slots    []credentials.Slot
	}{
		{domain.KindAMDGPUWeb, domain.CategoryLocal, "amdgputop-web", nil},
		{domain.KindLlamaSwap, domain.CategoryLocal, "llama-swap", []credentials.Slot{credentials.SlotAPIKey}},
		{domain.KindHindsight, domain.CategoryLocal, "Hindsight", []credentials.Slot{credentials.SlotAPIKey}},
		{domain.KindMiniMax, domain.CategoryRemote, "MiniMax", []credentials.Slot{credentials.SlotAPIKey}},
		{domain.KindZAI, domain.CategoryRemote, "Z.AI / GLM", []credentials.Slot{credentials.SlotAPIKey}},
		{domain.KindOpenRouter, domain.CategoryRemote, "OpenRouter", []credentials.Slot{credentials.SlotAPIKey, credentials.SlotManagementKey}},
	}
	for _, c := range cases {
		def, ok := Get(c.kind)
		if !ok {
			t.Fatalf("%s: not registered", c.kind)
		}
		if def.Category != c.category {
			t.Errorf("%s category = %s, want %s", c.kind, def.Category, c.category)
		}
		if def.Category != c.kind.Category() {
			t.Errorf("%s category disagrees with domain.SourceKind.Category()", c.kind)
		}
		if def.DefaultName != c.name {
			t.Errorf("%s default name = %q, want %q", c.kind, def.DefaultName, c.name)
		}
		if len(def.CredentialSlots) != len(c.slots) {
			t.Fatalf("%s credential slots = %v, want %v", c.kind, def.CredentialSlots, c.slots)
		}
		for i, slot := range def.CredentialSlots {
			if slot != c.slots[i] {
				t.Errorf("%s slot[%d] = %q, want %q (spelling is stable storage)", c.kind, i, slot, c.slots[i])
			}
		}
		// Interval presets come from the kind's (per-kind) preset list.
		want := config.KindIntervals(c.kind)
		if len(def.Intervals) != len(want) {
			t.Errorf("%s intervals = %v, want %v", c.kind, def.Intervals, want)
		} else {
			for i := range want {
				if def.Intervals[i] != want[i] {
					t.Errorf("%s interval[%d] = %v, want %v", c.kind, i, def.Intervals[i], want[i])
				}
			}
		}
		if def.DefaultInterval == 0 {
			t.Errorf("%s has no default interval", c.kind)
		}
		if !slices.Contains(def.Intervals, def.DefaultInterval) {
			t.Errorf("%s default interval %v is not one of its presets %v", c.kind, def.DefaultInterval, def.Intervals)
		}
	}

	// Hindsight is Local but polls a memory-service aggregate, so it carries
	// its own shifted-up ladder instead of the local 5s one.
	hs, _ := Get(domain.KindHindsight)
	if hs.DefaultInterval != config.DefaultHindsightInterval {
		t.Errorf("hindsight default interval = %v, want %v", hs.DefaultInterval, config.DefaultHindsightInterval)
	}
	if local, _ := Get(domain.KindAMDGPUWeb); local.DefaultInterval != config.DefaultLocalInterval ||
		local.Intervals[0] == hs.Intervals[0] {
		t.Errorf("the local kinds must keep their own %v ladder, hindsight starts at %v",
			config.DefaultLocalInterval, hs.Intervals[0])
	}

	def, _ := Get(domain.KindOpenRouter)
	api := def.SlotHint(credentials.SlotAPIKey)
	mgmt := def.SlotHint(credentials.SlotManagementKey)
	if api == "" || mgmt == "" || api == mgmt {
		t.Errorf("slot hints must be distinct non-empty texts, got %q / %q", api, mgmt)
	}
	noHints, _ := Get(domain.KindMiniMax)
	if noHints.SlotHint != nil && noHints.SlotHint(credentials.SlotAPIKey) != "" {
		t.Error("single-slot kinds need no hint unless they define one")
	}
}

func TestDefaultSettings(t *testing.T) {
	def, _ := Get(domain.KindAMDGPUWeb)
	s, ok := def.DefaultSettings().(config.AMDGPUWebSettings)
	if !ok || s.BaseURL != config.DefaultAMDGPUWebURL {
		t.Errorf("amdgputop-web default settings = %#v", def.DefaultSettings())
	}
	def, _ = Get(domain.KindZAI)
	zs, ok := def.DefaultSettings().(config.ZAISettings)
	if !ok || zs.BaseURL != config.DefaultZAIURL {
		t.Errorf("zai default settings = %#v", def.DefaultSettings())
	}
	def, _ = Get(domain.KindHindsight)
	hs, ok := def.DefaultSettings().(config.HindsightSettings)
	if !ok || hs.BaseURL != config.DefaultHindsightURL || hs.WebUIURL != "" {
		t.Errorf("hindsight default settings = %#v", def.DefaultSettings())
	}
}

func inst(id, name string, kind domain.SourceKind, settings config.ProviderSettings) config.SourceInstance {
	// Hindsight polls a memory aggregate, so it starts one preset higher.
	interval := config.DefaultLocalInterval
	if kind == domain.KindHindsight {
		interval = config.DefaultHindsightInterval
	}
	i := config.SourceInstance{ID: domain.InstanceID(id), Kind: kind, Name: name, Enabled: true, PollInterval: interval}
	switch s := settings.(type) {
	case config.AMDGPUWebSettings:
		i.AMDGPUWeb = &s
	case config.LlamaSwapSettings:
		i.LlamaSwap = &s
	case config.HindsightSettings:
		i.Hindsight = &s
	case config.MiniMaxSettings:
		i.MiniMax = &s
	case config.ZAISettings:
		i.ZAI = &s
	case config.OpenRouterSettings:
		i.OpenRouter = &s
	}
	return i
}

// fakeDeps is the minimal dependency set for Build; the fake store
// stands in for the keychain and the adapters never talk to it here.
func fakeDeps() Deps {
	return Deps{Client: nil, Log: nil, Creds: fakeStore{}}
}

func TestBuildTwoSameKindInstancesAreIndependent(t *testing.T) {
	a := inst("aaaa", "AMD GPU localhost", domain.KindAMDGPUWeb, config.AMDGPUWebSettings{BaseURL: "http://127.0.0.1:8080"})
	b := inst("bbbb", "AMD GPU server", domain.KindAMDGPUWeb, config.AMDGPUWebSettings{BaseURL: "http://10.0.0.5:8080"})
	def, _ := Get(domain.KindAMDGPUWeb)

	srcA, err := def.Build(a, fakeDeps())
	if err != nil {
		t.Fatalf("build a: %v", err)
	}
	srcB, err := def.Build(b, fakeDeps())
	if err != nil {
		t.Fatalf("build b: %v", err)
	}
	if srcA == srcB {
		t.Fatal("two instances must get distinct adapters (pointer identity drives reconfiguration)")
	}
	if srcA.Kind() != domain.KindAMDGPUWeb || srcB.Kind() != domain.KindAMDGPUWeb {
		t.Error("adapter kind metadata is wrong")
	}

	// Same for a credentialed kind: separate instances, separate accounts.
	mmA := inst("mm-1", "MiniMax Personal", domain.KindMiniMax, config.MiniMaxSettings{})
	mmB := inst("mm-2", "MiniMax Secondary", domain.KindMiniMax, config.MiniMaxSettings{})
	def, _ = Get(domain.KindMiniMax)
	srcMmA, err := def.Build(mmA, fakeDeps())
	if err != nil {
		t.Fatalf("build mm a: %v", err)
	}
	srcMmB, err := def.Build(mmB, fakeDeps())
	if err != nil {
		t.Fatalf("build mm b: %v", err)
	}
	if srcMmA == srcMmB {
		t.Fatal("two MiniMax instances must get distinct adapters")
	}
}

func TestBuildRejectsMissingSettings(t *testing.T) {
	broken := config.SourceInstance{ID: "x", Kind: domain.KindZAI, Name: "x", Enabled: true, PollInterval: 5 * time.Second}
	def, _ := Get(domain.KindZAI)
	if _, err := def.Build(broken, fakeDeps()); err == nil {
		t.Error("building without provider settings must fail")
	}
	if report := def.Test(context.Background(), broken, fakeDeps()); report.OK {
		t.Error("testing without provider settings must not succeed")
	}
}

func TestWebURL(t *testing.T) {
	cases := []struct {
		kind   domain.SourceKind
		url    string
		want   string
		wantOK bool
	}{
		{domain.KindAMDGPUWeb, "http://10.0.0.5:8080", "http://10.0.0.5:8080", true},
		{domain.KindLlamaSwap, "http://127.0.0.1:8081", "http://127.0.0.1:8081/ui", true},
		{domain.KindMiniMax, "", MiniMaxConsoleURL, true},
		{domain.KindZAI, "https://api.z.ai", ZAIUsageURL, true},
		{domain.KindOpenRouter, "", OpenRouterActivityURL, true},
		// Hindsight exposes its web UI only when a Web UI URL is configured;
		// the API base URL is never reused for it (no port derivation).
		{domain.KindHindsight, "http://127.0.0.1:9999", "http://127.0.0.1:9999", true},
		{domain.KindHindsight, "", "", false},
	}
	for _, c := range cases {
		def, _ := Get(c.kind)
		instance := inst("id", "n", c.kind, def.DefaultSettings())
		switch c.kind {
		case domain.KindAMDGPUWeb:
			instance.AMDGPUWeb = &config.AMDGPUWebSettings{BaseURL: c.url}
		case domain.KindLlamaSwap:
			instance.LlamaSwap = &config.LlamaSwapSettings{BaseURL: c.url}
		case domain.KindZAI:
			instance.ZAI = &config.ZAISettings{BaseURL: c.url}
		case domain.KindHindsight:
			instance.Hindsight = &config.HindsightSettings{BaseURL: "http://127.0.0.1:8888", WebUIURL: c.url}
		}
		got, ok := def.WebURL(instance)
		if ok != c.wantOK || got != c.want {
			t.Errorf("%s WebURL = (%q, %v), want (%q, %v)", c.kind, got, ok, c.want, c.wantOK)
		}
	}
}

func TestWebLinksUsesNamesOrderAndDisabled(t *testing.T) {
	instances := []config.SourceInstance{
		inst("1", "AMD GPU server", domain.KindAMDGPUWeb, config.AMDGPUWebSettings{BaseURL: "http://10.0.0.5:8080"}),
		{ID: "2", Kind: domain.KindMiniMax, Name: "MiniMax Secondary", Enabled: false, PollInterval: time.Minute, MiniMax: &config.MiniMaxSettings{}},
		inst("3", "MiniMax Personal", domain.KindMiniMax, config.MiniMaxSettings{}),
		inst("4", "llama local", domain.KindLlamaSwap, config.LlamaSwapSettings{BaseURL: "http://127.0.0.1:8081"}),
		// No Web UI URL configured: no tray entry.
		inst("5", "Hindsight local", domain.KindHindsight, config.HindsightSettings{BaseURL: "http://127.0.0.1:8888"}),
	}

	links := WebLinks(instances)
	if len(links) != 4 {
		t.Fatalf("links = %v, want four (Hindsight without a Web UI URL omitted)", links)
	}
	wantNames := []string{"AMD GPU server", "MiniMax Secondary", "MiniMax Personal", "llama local"}
	for i, l := range links {
		if l.Name != wantNames[i] {
			t.Errorf("link[%d] name = %q, want %q", i, l.Name, wantNames[i])
		}
		if l.URL == "" {
			t.Errorf("link[%d] has no URL", i)
		}
	}

	// A Web UI URL yields its own tray entry, independent of polling.
	instances = append(instances, inst("6", "Hindsight cloud", domain.KindHindsight,
		config.HindsightSettings{BaseURL: "http://127.0.0.1:8890", WebUIURL: "http://hs-ui.example.invalid"}))
	links = WebLinks(instances)
	if len(links) != 5 {
		t.Fatalf("links = %d, want 5", len(links))
	}
	if last := links[4]; last.Name != "Hindsight cloud" || last.URL != "http://hs-ui.example.invalid" {
		t.Errorf("last link = %+v, want the Hindsight web UI URL", last)
	}

	// An invalid instance (missing settings) yields no link, not a panic.
	instances = append(instances, config.SourceInstance{ID: "7", Kind: domain.KindZAI, Name: "broken", Enabled: true, PollInterval: time.Minute})
	if links := WebLinks(instances); len(links) != 5 {
		t.Errorf("links = %d, want 5 (broken instance omitted)", len(links))
	}
}

// hindsightTestServer serves the four Hindsight endpoints and records the
// bank-list request URIs, so the connection test proves its one-row probe.
type hindsightTestServer struct {
	mu         sync.Mutex
	banksTotal int64
	bankURIs   []string
}

func (s *hindsightTestServer) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			w.Write([]byte(`{"status":"healthy","database":"connected"}`))
		case "/health/live":
			w.Write([]byte(`{"status":"ok","version":"0.9.2","uptime_seconds":60}`))
		case "/metrics":
			w.Write([]byte("hindsight_db_pool_size 10\n"))
		case "/v1/default/banks":
			s.mu.Lock()
			s.bankURIs = append(s.bankURIs, r.URL.RequestURI())
			s.mu.Unlock()
			s.mu.Lock()
			total := s.banksTotal
			s.mu.Unlock()
			fmt.Fprintf(w, `{"banks":[{"bank_id":"x","fact_count":1}],"total":%d,"limit":1,"offset":0}`, total)
		default:
			http.NotFound(w, r)
		}
	})
}

func (s *hindsightTestServer) uris() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.bankURIs...)
}

func TestHindsightTestConnection(t *testing.T) {
	srv := &hindsightTestServer{banksTotal: 27}
	server := httptest.NewServer(srv.handler())
	defer server.Close()

	instance := inst("hs-1", "Hindsight", domain.KindHindsight,
		config.HindsightSettings{BaseURL: server.URL, WebUIURL: server.URL + "/ui"})
	def, _ := Get(domain.KindHindsight)

	report := def.Test(context.Background(), instance, Deps{Creds: fakeStore{}, Client: server.Client()})
	want := []string{"Health: OK", "Liveness: OK", "Metrics: OK", "Banks: OK (27 visible)"}
	if len(report.Lines) != len(want) {
		t.Fatalf("report = %v, want %v", report.Lines, want)
	}
	for i := range want {
		if report.Lines[i] != want[i] {
			t.Errorf("line[%d] = %q, want %q", i, report.Lines[i], want[i])
		}
	}
	if !report.OK {
		t.Error("all-OK report must count as success")
	}

	// The bank probe asks for exactly one row.
	uris := srv.uris()
	if len(uris) != 1 || uris[0] != "/v1/default/banks?limit=1&offset=0" {
		t.Errorf("bank probe requests = %v, want a single limit=1 request", uris)
	}

	// A rejected surface reports a sanitized per-line reason, not raw
	// payloads, and the test is not a success.
	denied := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer denied.Close()

	instance.Hindsight = &config.HindsightSettings{BaseURL: denied.URL}
	report = def.Test(context.Background(), instance, Deps{Creds: fakeStore{}, Client: denied.Client()})
	if report.OK {
		t.Error("all-probes-failed report must not be a success")
	}
	for i, line := range report.Lines {
		if strings.Contains(line, "401") || strings.Contains(line, "Unauthorized") {
			t.Errorf("line[%d] leaks raw status details: %q", i, line)
		}
	}
	if report.Lines[0] != "Health: Authentication failed" {
		t.Errorf("health line = %q, want the sanitized auth message", report.Lines[0])
	}

	// A proxy stranding exactly /health/live (an exact-match Path(/health)
	// rule, say) hands that endpoint to the web UI, which answers with
	// HTML: the report must blame Liveness by name and leave Health OK.
	split := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			w.Write([]byte(`{"status":"healthy","database":"connected"}`))
		case "/health/live":
			w.Write([]byte("<!doctype html><html><body>control plane</body></html>"))
		case "/metrics":
			w.Write([]byte("hindsight_db_pool_size 10\n"))
		case "/v1/default/banks":
			fmt.Fprintf(w, `{"banks":[{"bank_id":"x","fact_count":1}],"total":3,"limit":1,"offset":0}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer split.Close()

	instance.Hindsight = &config.HindsightSettings{BaseURL: split.URL}
	report = def.Test(context.Background(), instance, Deps{Creds: fakeStore{}, Client: split.Client()})
	wantSplit := []string{
		"Health: OK",
		"Liveness: Invalid provider response",
		"Metrics: OK",
		"Banks: OK (3 visible)",
	}
	if len(report.Lines) != len(wantSplit) {
		t.Fatalf("report = %v, want %v", report.Lines, wantSplit)
	}
	for i := range wantSplit {
		if report.Lines[i] != wantSplit[i] {
			t.Errorf("line[%d] = %q, want %q", i, report.Lines[i], wantSplit[i])
		}
	}
	if report.OK {
		t.Error("a broken liveness surface must not count as a success")
	}

	// Missing settings is an infrastructure failure, not a probe report.
	broken := config.SourceInstance{ID: "hs-x", Kind: domain.KindHindsight, Name: "broken", Enabled: true}
	if report := def.Test(context.Background(), broken, Deps{Creds: fakeStore{}}); report.OK || len(report.Lines) != 1 {
		t.Errorf("missing-settings report = %+v, want a single failure line", report)
	}

	// Build works without a credential store (unauthenticated instance).
	src, err := def.Build(instance, Deps{Creds: nil, Client: server.Client()})
	if err != nil {
		t.Errorf("build without a credential store: %v", err)
	} else if src.Kind() != domain.KindHindsight {
		t.Errorf("built adapter kind = %s", src.Kind())
	}
}

// fakeStore is an in-memory credentials.Store for Build paths that only
// need a non-nil store.
type fakeStore struct{}

func (fakeStore) Status(context.Context) credentials.Status {
	return credentials.Status{State: credentials.StateAvailable}
}
func (fakeStore) Get(context.Context, credentials.AccountID) (string, error) {
	return "", credentials.ErrNotFound
}
func (fakeStore) Set(context.Context, credentials.AccountID, string) error { return nil }
func (fakeStore) Delete(context.Context, credentials.AccountID) error      { return nil }

// blockingTransport counts every attempted HTTP request and refuses it,
// keeping a capability-gating test fully local while proving zero traffic.
type blockingTransport struct {
	mu       sync.Mutex
	attempts int
}

func (t *blockingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	t.mu.Lock()
	t.attempts++
	t.mu.Unlock()
	return nil, errors.New("unexpected HTTP request")
}

func (t *blockingTransport) count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.attempts
}

// An OpenRouter instance with neither credential configured must produce a
// per-slot report where everything is skipped, must not count as success,
// and above all must not touch the network.
func TestOpenRouterTestWithoutCredentialsSkipsEverythingLocally(t *testing.T) {
	inst := inst("or-1", "OpenRouter", domain.KindOpenRouter, config.OpenRouterSettings{})
	def, ok := Get(domain.KindOpenRouter)
	if !ok {
		t.Fatal("openrouter is not registered")
	}

	tr := &blockingTransport{}
	report := def.Test(context.Background(), inst, Deps{
		Creds:  fakeStore{},
		Client: &http.Client{Transport: tr},
	})

	if len(report.Lines) != 2 {
		t.Fatalf("report = %v, want one line per capability", report.Lines)
	}
	wantLabels := []string{"API key", "Management key"}
	for i, line := range report.Lines {
		if !strings.HasPrefix(line, wantLabels[i]+": ") ||
			!strings.Contains(line, "not configured (skipped)") {
			t.Errorf("line[%d] = %q, want %q: not configured (skipped)", i, line, wantLabels[i])
		}
	}
	if report.OK {
		t.Error("with no capability exercised the test must not be reported as success")
	}
	if n := tr.count(); n != 0 {
		t.Errorf("%d HTTP request(s) were attempted without any credential", n)
	}
}
