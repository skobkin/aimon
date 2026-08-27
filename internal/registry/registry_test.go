package registry

import (
	"context"
	"errors"
	"net/http"
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
	if len(all) != 5 {
		t.Fatalf("registered %d kinds, want 5", len(all))
	}
	seen := map[domain.SourceKind]bool{}
	for _, d := range all {
		if seen[d.Kind] {
			t.Errorf("kind %q registered twice", d.Kind)
		}
		seen[d.Kind] = true
	}
	for _, kind := range []domain.SourceKind{domain.KindAMDGPUWeb, domain.KindLlamaSwap, domain.KindMiniMax, domain.KindZAI, domain.KindOpenRouter} {
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
		// Interval presets come from the kind's category.
		if len(def.Intervals) != len(config.KindIntervals(c.kind)) {
			t.Errorf("%s intervals do not match config.KindIntervals", c.kind)
		}
		if def.DefaultInterval == 0 {
			t.Errorf("%s has no default interval", c.kind)
		}
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
}

func inst(id, name string, kind domain.SourceKind, settings config.ProviderSettings) config.SourceInstance {
	i := config.SourceInstance{ID: domain.InstanceID(id), Kind: kind, Name: name, Enabled: true, PollInterval: 5 * time.Second}
	switch s := settings.(type) {
	case config.AMDGPUWebSettings:
		i.AMDGPUWeb = &s
	case config.LlamaSwapSettings:
		i.LlamaSwap = &s
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
		kind domain.SourceKind
		url  string
		want string
	}{
		{domain.KindAMDGPUWeb, "http://10.0.0.5:8080", "http://10.0.0.5:8080"},
		{domain.KindLlamaSwap, "http://127.0.0.1:8081", "http://127.0.0.1:8081/ui"},
		{domain.KindMiniMax, "", MiniMaxConsoleURL},
		{domain.KindZAI, "https://api.z.ai", ZAIUsageURL},
		{domain.KindOpenRouter, "", OpenRouterActivityURL},
	}
	for _, c := range cases {
		def, _ := Get(c.kind)
		inst := inst("id", "n", c.kind, def.DefaultSettings())
		switch c.kind {
		case domain.KindAMDGPUWeb:
			inst.AMDGPUWeb = &config.AMDGPUWebSettings{BaseURL: c.url}
		case domain.KindLlamaSwap:
			inst.LlamaSwap = &config.LlamaSwapSettings{BaseURL: c.url}
		case domain.KindZAI:
			inst.ZAI = &config.ZAISettings{BaseURL: c.url}
		}
		got, ok := def.WebURL(inst)
		if !ok || got != c.want {
			t.Errorf("%s WebURL = (%q, %v), want (%q, true)", c.kind, got, ok, c.want)
		}
	}
}

func TestWebLinksUsesNamesOrderAndDisabled(t *testing.T) {
	instances := []config.SourceInstance{
		inst("1", "AMD GPU server", domain.KindAMDGPUWeb, config.AMDGPUWebSettings{BaseURL: "http://10.0.0.5:8080"}),
		{ID: "2", Kind: domain.KindMiniMax, Name: "MiniMax Secondary", Enabled: false, PollInterval: time.Minute, MiniMax: &config.MiniMaxSettings{}},
		inst("3", "MiniMax Personal", domain.KindMiniMax, config.MiniMaxSettings{}),
		inst("4", "llama local", domain.KindLlamaSwap, config.LlamaSwapSettings{BaseURL: "http://127.0.0.1:8081"}),
	}

	links := WebLinks(instances)
	if len(links) != 4 {
		t.Fatalf("links = %v, want all four", links)
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

	// An invalid instance (missing settings) yields no link, not a panic.
	instances = append(instances, config.SourceInstance{ID: "5", Kind: domain.KindZAI, Name: "broken", Enabled: true, PollInterval: time.Minute})
	if links := WebLinks(instances); len(links) != 4 {
		t.Errorf("links = %d, want 4 (broken instance omitted)", len(links))
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
