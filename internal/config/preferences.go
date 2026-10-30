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
	// keyUpdatesSkipAutomaticChecks backs the "Check for updates
	// automatically" preference as an INVERTED bool, for the same reason:
	// the default must be "checks on", so false/absent means automatic
	// checks run and true means they are skipped.
	keyUpdatesSkipAutomaticChecks = "updates.skip-automatic-checks"
	// keyNotificationsDisabled backs the master "Desktop notifications"
	// preference as an INVERTED bool: the default must be "on", so
	// false/absent means notifications may fire and true means they are
	// off. The two per-event keys below narrow it further, same pattern.
	keyNotificationsDisabled = "notifications.disabled"
	// keyNotificationsQuotaLowDisabled backs the "Quota window low" event
	// toggle, inverted for the same default-on reason.
	keyNotificationsQuotaLowDisabled = "notifications.quota-low.disabled"
	// keyNotificationsQuotaExhaustedDisabled backs the "Quota window
	// exhausted" event toggle, inverted for the same default-on reason.
	keyNotificationsQuotaExhaustedDisabled = "notifications.quota-exhausted.disabled"
	// keyStartupShowMonitor backs the "Show monitor window at startup"
	// preference. Plain storage: absent and false both mean the tray-first
	// default (hidden), true means the monitor window opens at launch.
	keyStartupShowMonitor = "startup.show-monitor"
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
	// PaceWindow is present only for pace-capable kinds; an empty value and
	// its absence are both the default (S7). Additive field: the source
	// document schema stays version 1, and older builds ignore it.
	PaceWindow string `json:"pace_window,omitempty"`

	// Exactly one provider block is set, matching Kind.
	AMDGPUWeb  *amdGPUWebRecord  `json:"amdgputop_web,omitempty"`
	LlamaSwap  *llamaSwapRecord  `json:"llama_swap,omitempty"`
	Hindsight  *hindsightRecord  `json:"hindsight,omitempty"`
	MiniMax    *miniMaxRecord    `json:"minimax,omitempty"`
	ZAI        *zaiRecord        `json:"zai,omitempty"`
	OpenRouter *openRouterRecord `json:"openrouter,omitempty"`
	Codex      *codexRecord      `json:"codex,omitempty"`
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

// codexRecord persists the Codex launcher configuration. Executable and Mode
// stay empty when they carry the default, and environment values are stored
// verbatim as ordinary non-secret configuration — they are never logged and
// never a substitute for a secret store.
type codexRecord struct {
	Executable string      `json:"executable,omitempty"`
	Mode       string      `json:"mode,omitempty"`
	Env        []envRecord `json:"env,omitempty"`
}

type envRecord struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

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

	return parseSourceDocument(raw)
}

// parseSourceDocument decodes and validates one stored document. This is
// the shared body of LoadInstances and the exported import path: schema
// check, per-record decode, whole-list validation.
func parseSourceDocument(raw string) ([]SourceInstance, error) {
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

// ParseSourceDocument validates and decodes a source document that came
// from outside the repository (the Settings import path). Same contract as
// the document handling of LoadInstances: a malformed, unsupported, or
// invalid document is an error naming the reason; nothing is persisted
// here.
func ParseSourceDocument(raw string) ([]SourceInstance, error) {
	return parseSourceDocument(raw)
}

// SourceDocument returns the raw stored source document — the exact JSON
// LoadInstances consumes — empty when none is stored. It carries no
// credential material by construction (secrets live only in the keychain,
// and this schema has no field that could hold one), so exporting it can
// never leak a secret.
func (r *Repository) SourceDocument() string {
	return r.prefs.String(keySourceDocument)
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

// CheckUpdatesAutomatically reports the "Check for updates automatically"
// preference. It defaults to true: absent and false both mean periodic
// checks run. Only automatic checks are gated — the manual tray check stays
// available regardless, and development builds never check automatically.
func (r *Repository) CheckUpdatesAutomatically() bool {
	return !r.prefs.Bool(keyUpdatesSkipAutomaticChecks)
}

// SetCheckUpdatesAutomatically persists the "Check for updates
// automatically" preference.
func (r *Repository) SetCheckUpdatesAutomatically(on bool) {
	r.prefs.SetBool(keyUpdatesSkipAutomaticChecks, !on)
}

// NotificationsEnabled reports the master "Desktop notifications"
// preference. It defaults to true: absent and false both mean desktop
// notifications may fire; the per-event toggles below narrow it further.
func (r *Repository) NotificationsEnabled() bool {
	return !r.prefs.Bool(keyNotificationsDisabled)
}

// SetNotificationsEnabled persists the master "Desktop notifications"
// preference.
func (r *Repository) SetNotificationsEnabled(on bool) {
	r.prefs.SetBool(keyNotificationsDisabled, !on)
}

// NotificationsQuotaLowEnabled reports the "Quota window low" event
// toggle. It defaults to true.
func (r *Repository) NotificationsQuotaLowEnabled() bool {
	return !r.prefs.Bool(keyNotificationsQuotaLowDisabled)
}

// SetNotificationsQuotaLowEnabled persists the "Quota window low" event
// toggle.
func (r *Repository) SetNotificationsQuotaLowEnabled(on bool) {
	r.prefs.SetBool(keyNotificationsQuotaLowDisabled, !on)
}

// NotificationsQuotaExhaustedEnabled reports the "Quota window exhausted"
// event toggle. It defaults to true.
func (r *Repository) NotificationsQuotaExhaustedEnabled() bool {
	return !r.prefs.Bool(keyNotificationsQuotaExhaustedDisabled)
}

// SetNotificationsQuotaExhaustedEnabled persists the "Quota window
// exhausted" event toggle.
func (r *Repository) SetNotificationsQuotaExhaustedEnabled(on bool) {
	r.prefs.SetBool(keyNotificationsQuotaExhaustedDisabled, !on)
}

// ShowMonitorOnStartup reports the "Show monitor window at startup"
// preference. It defaults to false: absent and false both mean the app
// starts to the tray only.
func (r *Repository) ShowMonitorOnStartup() bool {
	return r.prefs.Bool(keyStartupShowMonitor)
}

// SetShowMonitorOnStartup persists the "Show monitor window at startup"
// preference.
func (r *Repository) SetShowMonitorOnStartup(on bool) {
	r.prefs.SetBool(keyStartupShowMonitor, on)
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
	if PaceCapable(kind) {
		// Absent and zero both mean the default; an off-list stored value
		// snaps to the nearest preset like intervals do. Loads always carry
		// an explicit, on-list window for capable kinds.
		var window time.Duration
		if rec.PaceWindow != "" {
			var err error
			if window, err = time.ParseDuration(rec.PaceWindow); err != nil {
				return SourceInstance{}, fmt.Errorf("pace window %q: %w", rec.PaceWindow, err)
			}
		}
		inst.PaceWindow = normalizePaceWindow(window)
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
	case domain.KindCodex:
		if rec.Codex != nil {
			envs := make([]EnvOverride, 0, len(rec.Codex.Env))
			for _, env := range rec.Codex.Env {
				envs = append(envs, EnvOverride(env))
			}
			inst.Codex = &CodexSettings{Executable: rec.Codex.Executable, Mode: CodexMode(rec.Codex.Mode), Env: envs}
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
	if PaceCapable(inst.Kind) && inst.PaceWindow != 0 { // absence stays the default
		rec.PaceWindow = inst.PaceWindow.String()
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
	case domain.KindCodex:
		if inst.Codex != nil {
			r := &codexRecord{Executable: inst.Codex.Executable, Mode: string(inst.Codex.Mode)}
			for _, env := range inst.Codex.Env {
				r.Env = append(r.Env, envRecord(env))
			}
			rec.Codex = r
		}
	}

	return rec
}
