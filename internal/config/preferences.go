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
	// keySourceDocument holds the dynamic source list as one JSON
	// document. The v0.2 fixed per-kind keys ("source.*", "app.configured")
	// are no longer read or written; they are left in place in existing
	// installations and never rewritten (there is intentionally no
	// v0.2 -> v0.3 migration — a missing document means zero sources).
	keySourceDocument = "sources.document"
	// keyPrivacyShowSensitive backs the global "Hide sensitive info" UI
	// preference as an INVERTED bool: the plain Preferences.Bool cannot
	// distinguish "absent" from "false", and the default must be hide, so
	// false/absent means sensitive values are concealed and true means
	// they are shown.
	keyPrivacyShowSensitive = "privacy.show-sensitive"
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
	AMDGPUWeb  *amdGPUWebRecord  `json:"amdgputop_web,omitempty"`
	LlamaSwap  *llamaSwapRecord  `json:"llama_swap,omitempty"`
	Hindsight  *hindsightRecord  `json:"hindsight,omitempty"`
	MiniMax    *miniMaxRecord    `json:"minimax,omitempty"`
	ZAI        *zaiRecord        `json:"zai,omitempty"`
	OpenRouter *openRouterRecord `json:"openrouter,omitempty"`
}

type amdGPUWebRecord struct {
	BaseURL       string `json:"base_url"`
	SelectedGPUID string `json:"gpu_id,omitempty"`
}

type llamaSwapRecord struct {
	BaseURL string `json:"base_url"`
}

type hindsightRecord struct {
	BaseURL  string `json:"base_url"`
	WebUIURL string `json:"web_ui_url,omitempty"`
}

type miniMaxRecord struct{}

type zaiRecord struct {
	BaseURL string `json:"base_url"`
}

type openRouterRecord struct{}

// LoadInstances reads the dynamic source document. A missing document is
// the valid zero-source state (nil, nil) — the expected first S3 startup,
// since v0.2 fixed-source configuration is intentionally not migrated. A
// present but malformed, unsupported, or invalid document is an error; the
// caller must surface it and must not overwrite the document. A stored poll
// interval the current preset list no longer offers is normalized in memory
// to the nearest offered preset; the document itself stays untouched.
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

// HideSensitiveInfo reports the global "Hide sensitive info" preference
// (S4 spec §9). It defaults to true: absent and false both mean account
// monetary values stay concealed until revealed on hover. Only this UI
// presentation flag is affected — never API requests, credentials, or
// logging.
func (r *Repository) HideSensitiveInfo() bool {
	return !r.prefs.Bool(keyPrivacyShowSensitive)
}

// SetHideSensitiveInfo persists the "Hide sensitive info" preference.
func (r *Repository) SetHideSensitiveInfo(hide bool) {
	r.prefs.SetBool(keyPrivacyShowSensitive, !hide)
}

func recordToInstance(rec sourceRecord) (SourceInstance, error) {
	kind := domain.SourceKind(rec.Kind)
	interval, err := time.ParseDuration(rec.PollInterval)
	if err != nil {
		return SourceInstance{}, fmt.Errorf("poll interval %q: %w", rec.PollInterval, err)
	}
	// Snap an interval the current preset list no longer offers (an instance
	// saved with the old 5s Hindsight default) instead of failing the load of
	// the whole document. In-memory only: the stored document is never
	// rewritten by a load, and SaveInstances still refuses off-list values.
	interval = normalizeInterval(kind, interval)
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
	case domain.KindHindsight:
		if rec.Hindsight != nil {
			inst.Hindsight = &HindsightSettings{BaseURL: rec.Hindsight.BaseURL, WebUIURL: rec.Hindsight.WebUIURL}
		}
	case domain.KindMiniMax:
		if rec.MiniMax != nil {
			inst.MiniMax = &MiniMaxSettings{}
		}
	case domain.KindZAI:
		if rec.ZAI != nil {
			inst.ZAI = &ZAISettings{BaseURL: rec.ZAI.BaseURL}
		}
	case domain.KindOpenRouter:
		if rec.OpenRouter != nil {
			inst.OpenRouter = &OpenRouterSettings{}
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
	case domain.KindHindsight:
		if inst.Hindsight != nil {
			rec.Hindsight = &hindsightRecord{BaseURL: inst.Hindsight.BaseURL, WebUIURL: inst.Hindsight.WebUIURL}
		}
	case domain.KindMiniMax:
		if inst.MiniMax != nil {
			rec.MiniMax = &miniMaxRecord{}
		}
	case domain.KindZAI:
		if inst.ZAI != nil {
			rec.ZAI = &zaiRecord{BaseURL: inst.ZAI.BaseURL}
		}
	case domain.KindOpenRouter:
		if inst.OpenRouter != nil {
			rec.OpenRouter = &openRouterRecord{}
		}
	}

	return rec
}
