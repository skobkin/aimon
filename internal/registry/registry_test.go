package registry

import (
	"context"
	"testing"
	"time"

	"git.skobk.in/skobkin/aimonitor/internal/config"
	"git.skobk.in/skobkin/aimonitor/internal/credentials"
	"git.skobk.in/skobkin/aimonitor/internal/domain"
)

func TestAllRegistersEveryKindOnce(t *testing.T) {
	all := All()
	if len(all) != 4 {
		t.Fatalf("registered %d kinds, want 4", len(all))
	}
	seen := map[domain.SourceKind]bool{}
	for _, d := range all {
		if seen[d.Kind] {
			t.Errorf("kind %q registered twice", d.Kind)
		}
		seen[d.Kind] = true
	}
	for _, kind := range []domain.SourceKind{domain.KindAMDGPUWeb, domain.KindLlamaSwap, domain.KindMiniMax, domain.KindZAI} {
		if !seen[kind] {
			t.Errorf("kind %q missing from the registry", kind)
		}
		if _, ok := Get(kind); !ok {
			t.Errorf("Get(%q) not found", kind)
		}
	}
	if _, ok := Get("openrouter"); ok {
		t.Error("unknown kind must not resolve")
	}
}

func TestDefinitionMetadata(t *testing.T) {
	cases := []struct {
		kind     domain.SourceKind
		category domain.Category
		name     string
		slots    int
	}{
		{domain.KindAMDGPUWeb, domain.CategoryLocal, "amdgputop-web", 0},
		{domain.KindLlamaSwap, domain.CategoryLocal, "llama-swap", 1},
		{domain.KindMiniMax, domain.CategoryRemote, "MiniMax", 1},
		{domain.KindZAI, domain.CategoryRemote, "Z.AI / GLM", 1},
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
		if len(def.CredentialSlots) != c.slots {
			t.Errorf("%s credential slots = %v, want %d", c.kind, def.CredentialSlots, c.slots)
		}
		for _, slot := range def.CredentialSlots {
			if slot != credentials.SlotAPIKey {
				t.Errorf("%s has unexpected slot %q", c.kind, slot)
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
	if err := def.Test(context.Background(), broken, fakeDeps()); err == nil {
		t.Error("testing without provider settings must fail")
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
