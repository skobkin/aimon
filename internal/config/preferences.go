package config

import (
	"encoding/json"
	"fmt"
	"time"

	"git.skobk.in/skobkin/aimonitor/internal/domain"
)

// Preferences is the storage backend the repository needs. It is satisfied
// structurally by fyne.Preferences, which keeps this package Fyne-free and
// trivially fake-able in tests.
type Preferences interface {
	Bool(key string) bool
	String(key string) string
	SetBool(key string, value bool)
	SetString(key string, value string)
	RemoveValue(key string)
}

// Preference keys. These are stable across releases; renaming one silently
// discards user settings. Defined here and nowhere else.
const (
	keyAMDGPUEnabled = "source.amdgputop-web.enabled"
	keyAMDGPUURL     = "source.amdgputop-web.url"
	keyAMDGPUGPU     = "source.amdgputop-web.gpu"
	keyAMDGPUEvery   = "source.amdgputop-web.interval"

	keySwapEnabled = "source.llama-swap.enabled"
	keySwapURL     = "source.llama-swap.url"
	keySwapEvery   = "source.llama-swap.interval"

	keyMiniMaxEnabled = "source.minimax.enabled"
	keyMiniMaxEvery   = "source.minimax.interval"

	keyZAIEnabled = "source.zai.enabled"
	keyZAIURL     = "source.zai.url"
	keyZAIEvery   = "source.zai.interval"

	// keyConfigured marks that the user has saved settings at least
	// once; until then the app treats every launch as a first run.
	keyConfigured = "app.configured"

	// keySourceDocument holds the S3 dynamic source list as one JSON
	// document. The old fixed per-kind keys above are no longer read once
	// this key is in use; they are left in place in existing installations
	// and never rewritten (there is intentionally no v0.2 -> v0.3
	// migration).
	keySourceDocument = "sources.document"
)

// sourceSchemaVersion is the dynamic source document format this build
// reads and writes. An unknown version on load is an error, never a silent
// rewrite.
const sourceSchemaVersion = 1

// Repository loads and saves the non-secret configuration through the
// Preferences interface. No credential material ever passes through here.
type Repository struct {
	prefs Preferences
}

// NewRepository wraps a Preferences backend.
func NewRepository(prefs Preferences) *Repository {
	return &Repository{prefs: prefs}
}

// Load reads the stored configuration and sanitizes it. Missing values
// fall back to defaults; corrupt values are replaced, never fatal.
func (r *Repository) Load() Config {
	def := Default()

	cfg := Config{
		AMDGPUWeb: AMDGPUWeb{
			Enabled:       r.prefs.Bool(keyAMDGPUEnabled),
			BaseURL:       r.prefs.String(keyAMDGPUURL),
			SelectedGPUID: r.prefs.String(keyAMDGPUGPU),
			PollInterval:  loadInterval(r.prefs, keyAMDGPUEvery, def.AMDGPUWeb.PollInterval),
		},
		LlamaSwap: LlamaSwap{
			Enabled:      r.prefs.Bool(keySwapEnabled),
			BaseURL:      r.prefs.String(keySwapURL),
			PollInterval: loadInterval(r.prefs, keySwapEvery, def.LlamaSwap.PollInterval),
		},
		MiniMax: MiniMax{
			Enabled:      r.prefs.Bool(keyMiniMaxEnabled),
			PollInterval: loadInterval(r.prefs, keyMiniMaxEvery, def.MiniMax.PollInterval),
		},
		ZAI: ZAI{
			Enabled:      r.prefs.Bool(keyZAIEnabled),
			BaseURL:      r.prefs.String(keyZAIURL),
			PollInterval: loadInterval(r.prefs, keyZAIEvery, def.ZAI.PollInterval),
		},
	}
	return cfg.Sanitize()
}

// Save writes the full configuration. Intervals are stored as
// Duration.String().
func (r *Repository) Save(cfg Config) {
	r.prefs.SetBool(keyAMDGPUEnabled, cfg.AMDGPUWeb.Enabled)
	r.prefs.SetString(keyAMDGPUURL, cfg.AMDGPUWeb.BaseURL)
	r.prefs.SetString(keyAMDGPUGPU, cfg.AMDGPUWeb.SelectedGPUID)
	r.prefs.SetString(keyAMDGPUEvery, cfg.AMDGPUWeb.PollInterval.String())

	r.prefs.SetBool(keySwapEnabled, cfg.LlamaSwap.Enabled)
	r.prefs.SetString(keySwapURL, cfg.LlamaSwap.BaseURL)
	r.prefs.SetString(keySwapEvery, cfg.LlamaSwap.PollInterval.String())

	r.prefs.SetBool(keyMiniMaxEnabled, cfg.MiniMax.Enabled)
	r.prefs.SetString(keyMiniMaxEvery, cfg.MiniMax.PollInterval.String())

	r.prefs.SetBool(keyZAIEnabled, cfg.ZAI.Enabled)
	r.prefs.SetString(keyZAIURL, cfg.ZAI.BaseURL)
	r.prefs.SetString(keyZAIEvery, cfg.ZAI.PollInterval.String())

	r.prefs.SetBool(keyConfigured, true)
}

// Configured reports whether the user has ever saved settings. Until the
// first Save every launch is a first run (Settings opens at startup).
func (r *Repository) Configured() bool {
	return r.prefs.Bool(keyConfigured)
}

func loadInterval(prefs Preferences, key string, def time.Duration) time.Duration {
	raw := prefs.String(key)
	if raw == "" {
		return def
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return def
	}
	return d
}

// sourceDocument is the persistence DTO of the dynamic source list. Field
// spelling is stable; durations are stored as Duration.String(). No
// credential material has a field here — secrets live only in the keychain.
type sourceDocument struct {
	SchemaVersion int            `json:"schema_version"`
	Sources       []sourceRecord `json:"sources"`
}

type sourceRecord struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"`
	Name         string `json:"name"`
	Enabled      bool   `json:"enabled"`
	PollInterval string `json:"poll_interval"`

	// Exactly one provider block is set, matching Kind.
	AMDGPUWeb *amdGPUWebRecord `json:"amdgputop_web,omitempty"`
	LlamaSwap *llamaSwapRecord `json:"llama_swap,omitempty"`
	MiniMax   *miniMaxRecord   `json:"minimax,omitempty"`
	ZAI       *zaiRecord       `json:"zai,omitempty"`
}

type amdGPUWebRecord struct {
	BaseURL       string `json:"base_url"`
	SelectedGPUID string `json:"gpu_id,omitempty"`
}

type llamaSwapRecord struct {
	BaseURL string `json:"base_url"`
}

type miniMaxRecord struct{}

type zaiRecord struct {
	BaseURL string `json:"base_url"`
}

// LoadInstances reads the dynamic source document. A missing document is
// the valid zero-source state (nil, nil) — the expected first S3 startup,
// since v0.2 fixed-source configuration is intentionally not migrated. A
// present but malformed, unsupported, or invalid document is an error; the
// caller must surface it and must not overwrite the document.
func (r *Repository) LoadInstances() ([]SourceInstance, error) {
	raw := r.prefs.String(keySourceDocument)
	if raw == "" {
		return nil, nil
	}

	var doc sourceDocument
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return nil, fmt.Errorf("parse source document: %w", err)
	}
	if doc.SchemaVersion != sourceSchemaVersion {
		return nil, fmt.Errorf("unsupported source document schema version %d (this build reads version %d)", doc.SchemaVersion, sourceSchemaVersion)
	}

	list := make([]SourceInstance, 0, len(doc.Sources))
	for i, rec := range doc.Sources {
		inst, err := recordToInstance(rec)
		if err != nil {
			return nil, fmt.Errorf("source document entry %d: %w", i+1, err)
		}
		list = append(list, inst)
	}
	if errs := ValidateInstances(list); len(errs) > 0 {
		return nil, fmt.Errorf("invalid source document: %w", joinValidationErrors(errs))
	}
	return list, nil
}

// SaveInstances validates and writes the dynamic source document. An
// invalid list is never persisted.
func (r *Repository) SaveInstances(list []SourceInstance) error {
	if errs := ValidateInstances(list); len(errs) > 0 {
		return fmt.Errorf("refusing to save invalid source list: %w", joinValidationErrors(errs))
	}

	doc := sourceDocument{SchemaVersion: sourceSchemaVersion}
	for _, inst := range list {
		doc.Sources = append(doc.Sources, instanceToRecord(inst))
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("serialize source document: %w", err)
	}
	r.prefs.SetString(keySourceDocument, string(b))
	return nil
}

// HasDocument reports whether a dynamic source document is stored at all.
// This is the first-run marker for the dynamic model (it does not validate
// the document; load errors are surfaced separately).
func (r *Repository) HasDocument() bool {
	return r.prefs.String(keySourceDocument) != ""
}

func recordToInstance(rec sourceRecord) (SourceInstance, error) {
	kind := domain.SourceKind(rec.Kind)
	interval, err := time.ParseDuration(rec.PollInterval)
	if err != nil {
		return SourceInstance{}, fmt.Errorf("poll interval %q: %w", rec.PollInterval, err)
	}
	inst := SourceInstance{
		ID:           domain.InstanceID(rec.ID),
		Kind:         kind,
		Name:         rec.Name,
		Enabled:      rec.Enabled,
		PollInterval: interval,
	}
	switch kind {
	case domain.KindAMDGPUWeb:
		if rec.AMDGPUWeb != nil {
			inst.AMDGPUWeb = &AMDGPUWebSettings{BaseURL: rec.AMDGPUWeb.BaseURL, SelectedGPUID: rec.AMDGPUWeb.SelectedGPUID}
		}
	case domain.KindLlamaSwap:
		if rec.LlamaSwap != nil {
			inst.LlamaSwap = &LlamaSwapSettings{BaseURL: rec.LlamaSwap.BaseURL}
		}
	case domain.KindMiniMax:
		if rec.MiniMax != nil {
			inst.MiniMax = &MiniMaxSettings{}
		}
	case domain.KindZAI:
		if rec.ZAI != nil {
			inst.ZAI = &ZAISettings{BaseURL: rec.ZAI.BaseURL}
		}
	default:
		return SourceInstance{}, fmt.Errorf("unknown source kind %q", rec.Kind)
	}
	return inst, nil
}

func instanceToRecord(inst SourceInstance) sourceRecord {
	rec := sourceRecord{
		ID:           string(inst.ID),
		Kind:         string(inst.Kind),
		Name:         inst.Name,
		Enabled:      inst.Enabled,
		PollInterval: inst.PollInterval.String(),
	}
	switch inst.Kind {
	case domain.KindAMDGPUWeb:
		if inst.AMDGPUWeb != nil {
			rec.AMDGPUWeb = &amdGPUWebRecord{BaseURL: inst.AMDGPUWeb.BaseURL, SelectedGPUID: inst.AMDGPUWeb.SelectedGPUID}
		}
	case domain.KindLlamaSwap:
		if inst.LlamaSwap != nil {
			rec.LlamaSwap = &llamaSwapRecord{BaseURL: inst.LlamaSwap.BaseURL}
		}
	case domain.KindMiniMax:
		if inst.MiniMax != nil {
			rec.MiniMax = &miniMaxRecord{}
		}
	case domain.KindZAI:
		if inst.ZAI != nil {
			rec.ZAI = &zaiRecord{BaseURL: inst.ZAI.BaseURL}
		}
	}
	return rec
}
