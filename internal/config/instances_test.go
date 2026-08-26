package config

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"

	"git.skobk.in/skobkin/aimonitor/internal/domain"
)

// validAMDGPU is a minimal valid amdgputop-web instance for list tests.
func validAMDGPU(id domain.InstanceID, name, url string) SourceInstance {
	return SourceInstance{
		ID:           id,
		Kind:         domain.KindAMDGPUWeb,
		Name:         name,
		Enabled:      true,
		PollInterval: DefaultLocalInterval,
		AMDGPUWeb:    &AMDGPUWebSettings{BaseURL: url},
	}
}

func TestGenerateInstanceID(t *testing.T) {
	seen := make(map[domain.InstanceID]bool)
	for i := 0; i < 100; i++ {
		id, err := GenerateInstanceID()
		if err != nil {
			t.Fatalf("generate: %v", err)
		}
		if len(id) != 32 {
			t.Fatalf("ID %q: want 32 hex chars, got %d", id, len(id))
		}
		if seen[id] {
			t.Fatalf("ID %q generated twice", id)
		}
		seen[id] = true
		for _, r := range string(id) {
			if !strings.ContainsRune("0123456789abcdef", r) {
				t.Fatalf("ID %q: non-hex character %q", id, r)
			}
		}
	}
}

func TestSettingsAccessor(t *testing.T) {
	cases := []struct {
		name string
		inst SourceInstance
		want ProviderSettings
		ok   bool
	}{
		{
			name: "matching settings",
			inst: SourceInstance{Kind: domain.KindZAI, ZAI: &ZAISettings{BaseURL: "https://api.z.ai"}},
			want: ZAISettings{BaseURL: "https://api.z.ai"},
			ok:   true,
		},
		{
			name: "missing settings",
			inst: SourceInstance{Kind: domain.KindZAI},
		},
		{
			name: "settings of another kind present",
			inst: SourceInstance{Kind: domain.KindZAI, MiniMax: &MiniMaxSettings{}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := c.inst.Settings()
			if ok != c.ok {
				t.Fatalf("ok = %v, want %v", ok, c.ok)
			}
			if !c.ok {
				return
			}
			if !SameSettings(got, c.want) {
				t.Errorf("settings = %#v, want %#v", got, c.want)
			}
		})
	}
}

func TestInstanceValidate(t *testing.T) {
	valid := []SourceInstance{
		validAMDGPU("id-1", "AMD GPU localhost", "http://127.0.0.1:8080"),
		{ID: "id-2", Kind: domain.KindLlamaSwap, Name: "swap", Enabled: false, PollInterval: 10 * time.Second, LlamaSwap: &LlamaSwapSettings{BaseURL: "http://127.0.0.1:8081"}},
		{ID: "id-3", Kind: domain.KindMiniMax, Name: "MiniMax Personal", Enabled: true, PollInterval: DefaultRemoteInterval, MiniMax: &MiniMaxSettings{}},
		{ID: "id-4", Kind: domain.KindZAI, Name: "Z.AI / GLM", Enabled: true, PollInterval: 5 * time.Minute, ZAI: &ZAISettings{BaseURL: "https://api.z.ai"}},
	}
	for i, inst := range valid {
		if errs := inst.Validate(); len(errs) > 0 {
			t.Errorf("valid case %d (%s): unexpected errors %v", i, inst.Name, errs)
		}
	}

	invalid := []struct {
		name   string
		modify func(*SourceInstance)
		field  string
	}{
		{"empty name", func(i *SourceInstance) { i.Name = "" }, "Name"},
		{"whitespace name", func(i *SourceInstance) { i.Name = "   " }, "Name"},
		{"unknown kind", func(i *SourceInstance) { i.Kind = "openrouter" }, "Kind"},
		{"no settings", func(i *SourceInstance) { i.Kind = domain.KindMiniMax; i.AMDGPUWeb = nil }, "Settings"},
		{"two settings blocks", func(i *SourceInstance) { i.MiniMax = &MiniMaxSettings{} }, "Settings"},
		{"mismatched settings", func(i *SourceInstance) { i.Kind = domain.KindMiniMax }, "Settings"},
		{"bad url", func(i *SourceInstance) { i.AMDGPUWeb.BaseURL = "ftp://nope" }, "URL"},
		{"empty url", func(i *SourceInstance) { i.AMDGPUWeb.BaseURL = "" }, "URL"},
		{"non-preset interval", func(i *SourceInstance) { i.PollInterval = 7 * time.Second }, "Poll interval"},
		{"remote interval on local kind", func(i *SourceInstance) { i.PollInterval = 5 * time.Minute }, "Poll interval"},
	}
	for _, c := range invalid {
		t.Run(c.name, func(t *testing.T) {
			inst := validAMDGPU("id-x", "x", "http://127.0.0.1:8080")
			c.modify(&inst)
			errs := inst.Validate()
			found := false
			for _, e := range errs {
				if e.Field == c.field {
					found = true
				}
			}
			if !found {
				t.Errorf("errors %v: want a %q error", errs, c.field)
			}
		})
	}
}

func TestValidateInstancesDuplicateIDsAndNames(t *testing.T) {
	a := validAMDGPU("same-id", "AMD GPU localhost", "http://127.0.0.1:8080")
	b := validAMDGPU("same-id", "AMD GPU server", "http://10.0.0.5:8080")

	errs := ValidateInstances([]SourceInstance{a, b})
	if len(errs) != 1 || errs[0].Field != "ID" {
		t.Fatalf("errors = %v, want exactly one duplicate-ID error", errs)
	}

	// Duplicate names are explicitly allowed.
	b.ID = "other-id"
	if errs := ValidateInstances([]SourceInstance{a, b}); len(errs) != 0 {
		t.Fatalf("duplicate names must be valid, got %v", errs)
	}
}

func TestSameSettings(t *testing.T) {
	if !SameSettings(AMDGPUWebSettings{BaseURL: "http://a", SelectedGPUID: "g1"}, AMDGPUWebSettings{BaseURL: "http://a", SelectedGPUID: "g1"}) {
		t.Error("equal settings must compare equal")
	}
	if SameSettings(AMDGPUWebSettings{BaseURL: "http://a"}, AMDGPUWebSettings{BaseURL: "http://b"}) {
		t.Error("different URLs must compare unequal")
	}
	if SameSettings(AMDGPUWebSettings{BaseURL: "http://a"}, LlamaSwapSettings{BaseURL: "http://a"}) {
		t.Error("different kinds must compare unequal")
	}
	if !SameSettings(MiniMaxSettings{}, MiniMaxSettings{}) {
		t.Error("empty minimax settings must compare equal")
	}
	if SameSettings(nil, MiniMaxSettings{}) || !SameSettings(nil, nil) {
		t.Error("nil handling is wrong")
	}
}

func TestLoadInstancesAbsentIsZeroSources(t *testing.T) {
	repo := NewRepository(newMemPreferences())

	list, err := repo.LoadInstances()
	if err != nil {
		t.Fatalf("absent document must load as zero sources, got %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("absent document = %d sources, want 0", len(list))
	}
	if repo.HasDocument() {
		t.Error("HasDocument must be false before anything is saved")
	}
}

func TestSaveLoadInstancesRoundTrip(t *testing.T) {
	prefs := newMemPreferences()
	repo := NewRepository(prefs)

	stored := []SourceInstance{
		validAMDGPU("gpu-localhost", "AMD GPU localhost", "http://127.0.0.1:8080"),
		validAMDGPU("gpu-server", "AMD GPU server", "http://10.0.0.5:8080"),
		{ID: "mm-1", Kind: domain.KindMiniMax, Name: "MiniMax Personal", Enabled: true, PollInterval: 2 * time.Minute, MiniMax: &MiniMaxSettings{}},
		{ID: "zai-1", Kind: domain.KindZAI, Name: "Z.AI / GLM", Enabled: false, PollInterval: 5 * time.Minute, ZAI: &ZAISettings{BaseURL: "https://api.z.ai"}},
	}
	if err := repo.SaveInstances(stored); err != nil {
		t.Fatalf("save: %v", err)
	}
	if !repo.HasDocument() {
		t.Error("HasDocument must be true after SaveInstances")
	}

	loaded, err := repo.LoadInstances()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded) != len(stored) {
		t.Fatalf("loaded %d sources, want %d", len(loaded), len(stored))
	}
	for i := range stored {
		if loaded[i].ID != stored[i].ID || loaded[i].Kind != stored[i].Kind ||
			loaded[i].Name != stored[i].Name || loaded[i].Enabled != stored[i].Enabled ||
			loaded[i].PollInterval != stored[i].PollInterval {
			t.Errorf("source[%d] common fields mismatch: loaded %+v, want %+v", i, loaded[i], stored[i])
		}
		want, _ := stored[i].Settings()
		got, _ := loaded[i].Settings()
		if !SameSettings(got, want) {
			t.Errorf("source[%d] settings mismatch: got %#v, want %#v", i, got, want)
		}
	}

	// The stored order is authoritative and must survive the round-trip.
	var order []string
	for _, inst := range loaded {
		order = append(order, string(inst.ID))
	}
	want := "gpu-localhost,gpu-server,mm-1,zai-1"
	if got := strings.Join(order, ","); got != want {
		t.Errorf("order = %q, want %q", got, want)
	}
}

func TestSaveInstancesRejectsInvalid(t *testing.T) {
	prefs := newMemPreferences()
	repo := NewRepository(prefs)

	bad := []SourceInstance{validAMDGPU("id", "", "http://127.0.0.1:8080")} // empty name
	if err := repo.SaveInstances(bad); err == nil {
		t.Fatal("saving an invalid list must fail")
	}
	if repo.HasDocument() {
		t.Error("a rejected save must not write the document")
	}
	if raw := prefs.String(keySourceDocument); raw != "" {
		t.Errorf("a rejected save wrote %q", raw)
	}
}

func TestLoadInstancesMalformedDocument(t *testing.T) {
	prefs := newMemPreferences()
	prefs.SetString(keySourceDocument, "{not json")

	if _, err := NewRepository(prefs).LoadInstances(); err == nil {
		t.Fatal("malformed JSON must be an error, never silently empty")
	}
	// The malformed document stays untouched for the user to inspect.
	if !NewRepository(prefs).HasDocument() {
		t.Error("malformed document must remain stored, not cleared")
	}
}

func TestLoadInstancesUnsupportedSchemaVersion(t *testing.T) {
	prefs := newMemPreferences()
	prefs.SetString(keySourceDocument, `{"schema_version":99,"sources":[]}`)

	_, err := NewRepository(prefs).LoadInstances()
	if err == nil || !strings.Contains(err.Error(), "schema version") {
		t.Fatalf("unsupported schema version must error mentioning the version, got %v", err)
	}
}

func TestLoadInstancesUnknownKindAndBadFields(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"unknown kind", `{"schema_version":1,"sources":[{"id":"a","kind":"openrouter","name":"x","enabled":true,"poll_interval":"5s","minimax":{}}]}`, "unknown source kind"},
		{"bad duration", `{"schema_version":1,"sources":[{"id":"a","kind":"minimax","name":"x","enabled":true,"poll_interval":"soon","minimax":{}}]}`, "poll interval"},
		{"empty name", `{"schema_version":1,"sources":[{"id":"a","kind":"minimax","name":"","enabled":true,"poll_interval":"30s","minimax":{}}]}`, "Name"},
		{"duplicate ids", `{"schema_version":1,"sources":[{"id":"a","kind":"minimax","name":"x","enabled":true,"poll_interval":"30s","minimax":{}},{"id":"a","kind":"zai","name":"y","enabled":true,"poll_interval":"30s","zai":{"base_url":"https://api.z.ai"}}]}`, "duplicate instance ID"},
		{"missing settings block", `{"schema_version":1,"sources":[{"id":"a","kind":"zai","name":"x","enabled":true,"poll_interval":"30s"}]}`, "Settings"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			prefs := newMemPreferences()
			prefs.SetString(keySourceDocument, c.raw)
			_, err := NewRepository(prefs).LoadInstances()
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error = %v, want it to mention %q", err, c.want)
			}
		})
	}
}

// The document DTO must never grow a credential field. This marshals a
// fully-populated document and checks every key on the wire.
func TestSourceDocumentHasNoSecretFields(t *testing.T) {
	doc := sourceDocument{
		SchemaVersion: sourceSchemaVersion,
		Sources: []sourceRecord{
			{
				ID: "a", Kind: "amdgputop-web", Name: "n", Enabled: true, PollInterval: "5s",
				AMDGPUWeb: &amdGPUWebRecord{BaseURL: "http://127.0.0.1:8080", SelectedGPUID: "gpu"},
				LlamaSwap: &llamaSwapRecord{BaseURL: "http://127.0.0.1:8081"},
				MiniMax:   &miniMaxRecord{},
				ZAI:       &zaiRecord{BaseURL: "https://api.z.ai"},
			},
		},
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var keys []string
	var walk func(v any)
	walk = func(v any) {
		switch tv := v.(type) {
		case map[string]any:
			for k, child := range tv {
				keys = append(keys, k)
				walk(child)
			}
		case []any:
			for _, child := range tv {
				walk(child)
			}
		}
	}
	var parsed any
	if err := json.Unmarshal(b, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	walk(parsed)
	sort.Strings(keys)

	for _, k := range keys {
		for _, fragment := range []string{"key", "token", "secret", "password", "credential"} {
			if strings.Contains(k, fragment) {
				t.Errorf("document key %q looks credential-related", k)
			}
		}
	}
}
