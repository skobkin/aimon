// Package registry is the compiled table of source kinds: the one place
// that knows each kind's category, defaults, credential slots, web UI, and
// how to build and test its adapter. This is a static registry, not a
// plugin system — adding a kind means adding a Definition here and its
// adapter package, then recompiling.
package registry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"git.skobk.in/skobkin/aimonitor/internal/config"
	"git.skobk.in/skobkin/aimonitor/internal/credentials"
	"git.skobk.in/skobkin/aimonitor/internal/domain"
	"git.skobk.in/skobkin/aimonitor/internal/source"
	"git.skobk.in/skobkin/aimonitor/internal/source/amdgpuweb"
	"git.skobk.in/skobkin/aimonitor/internal/source/codex"
	"git.skobk.in/skobkin/aimonitor/internal/source/hindsight"
	"git.skobk.in/skobkin/aimonitor/internal/source/llamaswap"
	"git.skobk.in/skobkin/aimonitor/internal/source/minimax"
	"git.skobk.in/skobkin/aimonitor/internal/source/openrouter"
	"git.skobk.in/skobkin/aimonitor/internal/source/zai"
)

// Console pages of the remote providers (their own usage dashboards, not
// the API endpoints the adapters poll).
const (
	MiniMaxConsoleURL     = "https://platform.minimax.io/console/usage"
	ZAIUsageURL           = "https://z.ai/manage-apikey/coding-plan/personal/usage"
	OpenRouterActivityURL = "https://openrouter.ai/activity"
)

// Deps are the shared construction dependencies handed to Build and Test.
// Creds may be wrapped (for example an override store carrying a typed but
// unsaved secret for a Settings connection test).
type Deps struct {
	Creds  credentials.Store
	Client *http.Client
	Log    *slog.Logger
	// AppVersion is the running application version, reported by source
	// adapters that identify themselves to their provider (currently the
	// Codex CLI JSON-RPC clientInfo).
	AppVersion string
}

// Definition is the compiled metadata and construction recipe for one
// source kind.
type Definition struct {
	// Kind identifies the compiled adapter.
	Kind domain.SourceKind
	// Category groups instances into the Local/Remote UI sections.
	Category domain.Category
	// DefaultName prefills a new instance's name in the Add editor.
	DefaultName string
	// DefaultInterval and Intervals are the new-instance poll interval and
	// the offered presets for the kind's category.
	DefaultInterval time.Duration
	Intervals       []time.Duration
	// CredentialSlots lists the kind's known keychain slots for scoping
	// and (on delete) best-effort cleanup. Empty for credentialess kinds;
	// llama-swap's single slot is optional.
	CredentialSlots []credentials.Slot
	// SlotHint optionally returns concise explanatory text for one of the
	// kind's slots, shown under its editor password entry. Nil means no
	// hint is shown.
	SlotHint func(slot credentials.Slot) string
	// WebLabel names the provider's own web UI on card buttons.
	WebLabel string

	// DefaultSettings returns fresh default provider settings for a new
	// instance (default URLs).
	DefaultSettings func() config.ProviderSettings
	// Build constructs the adapter for one configured instance. The
	// instance must already be valid; a missing settings block is a
	// programming error reported as an error, not silently tolerated.
	Build func(inst config.SourceInstance, deps Deps) (source.Source, error)
	// Test performs a Settings connection test with the instance's unsaved
	// editor values. It never persists anything. Kinds with several
	// credentials report each capability separately in the report; with no
	// capability configured the report lists every slot as skipped and is
	// not successful (nothing was verified).
	Test func(ctx context.Context, inst config.SourceInstance, deps Deps) TestReport
	// WebURL derives the provider web UI URL from instance settings; ok is
	// false for kinds without a web UI.
	WebURL func(inst config.SourceInstance) (string, bool)
}

// apiKeySlots is the single-slot credential set shared by the credentialed
// kinds.
var apiKeySlots = []credentials.Slot{credentials.SlotAPIKey}

// openRouterSlots lists both OpenRouter credential slots in reporting
// order: the ordinary inference/API key first, then the optional account
// Management key. The two are never interchangeable.
var openRouterSlots = []credentials.Slot{credentials.SlotAPIKey, credentials.SlotManagementKey}

// definitions lists every compiled kind. Local kinds first, remote second;
// within a category the list order is only a fallback for type choosers —
// user-visible order always comes from the configured instance list.
var definitions = []Definition{
	{
		Kind:            domain.KindAMDGPUWeb,
		Category:        domain.CategoryLocal,
		DefaultName:     "amdgputop-web",
		DefaultInterval: config.DefaultLocalInterval,
		Intervals:       config.LocalIntervals,
		WebLabel:        "AMDGPUTOP web UI",
		DefaultSettings: func() config.ProviderSettings {
			return config.AMDGPUWebSettings{BaseURL: config.DefaultAMDGPUWebURL}
		},
		Build: func(inst config.SourceInstance, deps Deps) (source.Source, error) {
			s := inst.AMDGPUWeb
			if s == nil {
				return nil, missingSettings(inst)
			}

			return amdgpuweb.New(s.BaseURL, s.SelectedGPUID, deps.Client, deps.Log), nil
		},
		Test: func(ctx context.Context, inst config.SourceInstance, deps Deps) TestReport {
			s := inst.AMDGPUWeb
			if s == nil {
				return failedTestReport(missingSettings(inst))
			}
			_, err := amdgpuweb.New(s.BaseURL, s.SelectedGPUID, deps.Client, deps.Log).Discover(ctx)

			return singleTestReport(err)
		},
		WebURL: func(inst config.SourceInstance) (string, bool) {
			if inst.AMDGPUWeb == nil {
				return "", false
			}

			return inst.AMDGPUWeb.BaseURL, true
		},
	},
	{
		Kind:            domain.KindLlamaSwap,
		Category:        domain.CategoryLocal,
		DefaultName:     "llama-swap",
		DefaultInterval: config.DefaultLocalInterval,
		Intervals:       config.LocalIntervals,
		CredentialSlots: apiKeySlots, // optional: no stored key means unauthenticated
		WebLabel:        "llama-swap UI",
		DefaultSettings: func() config.ProviderSettings {
			return config.LlamaSwapSettings{BaseURL: config.DefaultLlamaSwapURL}
		},
		Build: func(inst config.SourceInstance, deps Deps) (source.Source, error) {
			s := inst.LlamaSwap
			if s == nil {
				return nil, missingSettings(inst)
			}
			account, err := credentials.AccountForInstance(inst.ID, credentials.SlotAPIKey)
			if err != nil {
				return nil, err
			}
			// deps.Creds may be nil: a known-unauthenticated instance.
			return llamaswap.New(s.BaseURL, account, deps.Creds, deps.Client, deps.Log), nil
		},
		Test: func(ctx context.Context, inst config.SourceInstance, deps Deps) TestReport {
			s := inst.LlamaSwap
			if s == nil {
				return failedTestReport(missingSettings(inst))
			}
			account, err := credentials.AccountForInstance(inst.ID, credentials.SlotAPIKey)
			if err != nil {
				return failedTestReport(err)
			}

			return singleTestReport(llamaswap.New(s.BaseURL, account, deps.Creds, deps.Client, deps.Log).Test(ctx))
		},
		WebURL: func(inst config.SourceInstance) (string, bool) {
			if inst.LlamaSwap == nil {
				return "", false
			}

			return inst.LlamaSwap.BaseURL + "/ui", true
		},
	},
	{
		Kind:            domain.KindHindsight,
		Category:        domain.CategoryLocal,
		DefaultName:     "Hindsight",
		DefaultInterval: config.DefaultHindsightInterval,
		Intervals:       config.HindsightIntervals,
		CredentialSlots: apiKeySlots, // optional: no stored key means unauthenticated
		SlotHint: func(slot credentials.Slot) string {
			return "Optional Bearer token for protected/cloud Hindsight APIs"
		},
		WebLabel: "Hindsight",
		DefaultSettings: func() config.ProviderSettings {
			return config.HindsightSettings{BaseURL: config.DefaultHindsightURL}
		},
		Build: func(inst config.SourceInstance, deps Deps) (source.Source, error) {
			s := inst.Hindsight
			if s == nil {
				return nil, missingSettings(inst)
			}
			account, err := credentials.AccountForInstance(inst.ID, credentials.SlotAPIKey)
			if err != nil {
				return nil, err
			}
			// deps.Creds may be nil: a known-unauthenticated instance.
			return hindsight.New(s.BaseURL, account, deps.Creds, deps.Client, deps.Log), nil
		},
		Test: hindsightTestReport,
		WebURL: func(inst config.SourceInstance) (string, bool) {
			if inst.Hindsight == nil || inst.Hindsight.WebUIURL == "" {
				return "", false
			}

			return inst.Hindsight.WebUIURL, true
		},
	},
	{
		Kind:            domain.KindMiniMax,
		Category:        domain.CategoryRemote,
		DefaultName:     "MiniMax",
		DefaultInterval: config.DefaultRemoteInterval,
		Intervals:       config.RemoteIntervals,
		CredentialSlots: apiKeySlots,
		WebLabel:        "MiniMax usage",
		DefaultSettings: func() config.ProviderSettings {
			return config.MiniMaxSettings{}
		},
		Build: func(inst config.SourceInstance, deps Deps) (source.Source, error) {
			if inst.MiniMax == nil {
				return nil, missingSettings(inst)
			}
			account, err := credentials.AccountForInstance(inst.ID, credentials.SlotAPIKey)
			if err != nil {
				return nil, err
			}
			if deps.Creds == nil {
				return nil, fmt.Errorf("build %s instance %s: a credential store is required", inst.Kind, inst.ID)
			}

			return minimax.New(account, deps.Creds, deps.Client, deps.Log), nil
		},
		Test: func(ctx context.Context, inst config.SourceInstance, deps Deps) TestReport {
			if inst.MiniMax == nil {
				return failedTestReport(missingSettings(inst))
			}
			account, err := credentials.AccountForInstance(inst.ID, credentials.SlotAPIKey)
			if err != nil {
				return failedTestReport(err)
			}
			if deps.Creds == nil {
				return failedTestReport(fmt.Errorf("test %s instance %s: a credential store is required", inst.Kind, inst.ID))
			}
			_, err = minimax.New(account, deps.Creds, deps.Client, deps.Log).Refresh(ctx)

			return singleTestReport(err)
		},
		WebURL: func(inst config.SourceInstance) (string, bool) {
			if inst.MiniMax == nil {
				return "", false
			}

			return MiniMaxConsoleURL, true
		},
	},
	{
		Kind:            domain.KindZAI,
		Category:        domain.CategoryRemote,
		DefaultName:     "Z.AI / GLM",
		DefaultInterval: config.DefaultRemoteInterval,
		Intervals:       config.RemoteIntervals,
		CredentialSlots: apiKeySlots,
		WebLabel:        "Z.ai usage",
		DefaultSettings: func() config.ProviderSettings {
			return config.ZAISettings{BaseURL: config.DefaultZAIURL}
		},
		Build: func(inst config.SourceInstance, deps Deps) (source.Source, error) {
			s := inst.ZAI
			if s == nil {
				return nil, missingSettings(inst)
			}
			account, err := credentials.AccountForInstance(inst.ID, credentials.SlotAPIKey)
			if err != nil {
				return nil, err
			}
			if deps.Creds == nil {
				return nil, fmt.Errorf("build %s instance %s: a credential store is required", inst.Kind, inst.ID)
			}

			return zai.New(s.BaseURL, account, deps.Creds, deps.Client, deps.Log), nil
		},
		Test: func(ctx context.Context, inst config.SourceInstance, deps Deps) TestReport {
			s := inst.ZAI
			if s == nil {
				return failedTestReport(missingSettings(inst))
			}
			account, err := credentials.AccountForInstance(inst.ID, credentials.SlotAPIKey)
			if err != nil {
				return failedTestReport(err)
			}
			if deps.Creds == nil {
				return failedTestReport(fmt.Errorf("test %s instance %s: a credential store is required", inst.Kind, inst.ID))
			}
			_, err = zai.New(s.BaseURL, account, deps.Creds, deps.Client, deps.Log).Refresh(ctx)

			return singleTestReport(err)
		},
		WebURL: func(inst config.SourceInstance) (string, bool) {
			if inst.ZAI == nil {
				return "", false
			}

			return ZAIUsageURL, true
		},
	},
	{
		Kind:            domain.KindOpenRouter,
		Category:        domain.CategoryRemote,
		DefaultName:     "OpenRouter",
		DefaultInterval: config.DefaultRemoteInterval,
		Intervals:       config.RemoteIntervals,
		CredentialSlots: openRouterSlots,
		SlotHint: func(slot credentials.Slot) string {
			switch slot {
			case credentials.SlotManagementKey:
				return "account credit balance"
			default:
				return "per-key usage and spending limit"
			}
		},
		WebLabel: "OpenRouter activity",
		DefaultSettings: func() config.ProviderSettings {
			return config.OpenRouterSettings{}
		},
		Build: func(inst config.SourceInstance, deps Deps) (source.Source, error) {
			s := inst.OpenRouter
			if s == nil {
				return nil, missingSettings(inst)
			}
			apiAccount, mgmtAccount, err := openRouterAccounts(inst)
			if err != nil {
				return nil, err
			}
			if deps.Creds == nil {
				return nil, fmt.Errorf("build %s instance %s: a credential store is required", inst.Kind, inst.ID)
			}
			// An instance without stored credentials builds fine: its
			// refresh then fails locally with zero requests (S4 rules §2),
			// keeping credential removal recoverable.
			return openrouter.New(apiAccount, mgmtAccount, deps.Creds, deps.Client, deps.Log), nil
		},
		Test: func(ctx context.Context, inst config.SourceInstance, deps Deps) TestReport {
			s := inst.OpenRouter
			if s == nil {
				return failedTestReport(missingSettings(inst))
			}
			apiAccount, mgmtAccount, err := openRouterAccounts(inst)
			if err != nil {
				return failedTestReport(err)
			}
			if deps.Creds == nil {
				return failedTestReport(fmt.Errorf("test %s instance %s: a credential store is required", inst.Kind, inst.ID))
			}
			a := openrouter.New(apiAccount, mgmtAccount, deps.Creds, deps.Client, deps.Log)

			return capabilityTestReport(ctx, []namedCapability{
				{
					label: "API key",
					run: func(ctx context.Context) (present bool, err error) {
						key, err := a.FetchKeyUsage(ctx)

						return key != nil, err
					},
				},
				{
					label: "Management key",
					run: func(ctx context.Context) (present bool, err error) {
						account, err := a.FetchAccountCredits(ctx)

						return account != nil, err
					},
				},
			})
		},
		WebURL: func(inst config.SourceInstance) (string, bool) {
			if inst.OpenRouter == nil {
				return "", false
			}

			return OpenRouterActivityURL, true
		},
	},
	{
		// Remote despite the local transport: the quota being monitored is
		// the ChatGPT subscription's, reached only through the user's own
		// Codex CLI (docs/S6_CODEX.md).
		Kind:            domain.KindCodex,
		Category:        domain.CategoryRemote,
		DefaultName:     "ChatGPT Codex",
		DefaultInterval: config.DefaultCodexInterval,
		Intervals:       config.CodexIntervals,
		// No credential slots: the CLI owns the ChatGPT login, and AI
		// Monitor never sees a token.
		DefaultSettings: func() config.ProviderSettings {
			return config.CodexSettings{}
		},
		Build: func(inst config.SourceInstance, deps Deps) (source.Source, error) {
			s := inst.Codex
			if s == nil {
				return nil, missingSettings(inst)
			}

			return codex.New(*s, deps.Log, deps.AppVersion), nil
		},
		Test:   codexTestReport,
		WebURL: noWebURL,
	},
}

// openRouterAccounts resolves the two instance-scoped keychain accounts of
// an OpenRouter instance in slot order.
func openRouterAccounts(inst config.SourceInstance) (apiAccount, mgmtAccount credentials.AccountID, err error) {
	apiAccount, err = credentials.AccountForInstance(inst.ID, credentials.SlotAPIKey)
	if err != nil {
		return "", "", err
	}
	mgmtAccount, err = credentials.AccountForInstance(inst.ID, credentials.SlotManagementKey)
	if err != nil {
		return "", "", err
	}

	return apiAccount, mgmtAccount, nil
}

// All returns every compiled kind's definition: local kinds first, then
// remote.
func All() []Definition {
	out := make([]Definition, len(definitions))
	copy(out, definitions)

	return out
}

// Get returns the definition of one kind.
func Get(kind domain.SourceKind) (Definition, bool) {
	for _, d := range definitions {
		if d.Kind == kind {
			return d, true
		}
	}

	return Definition{}, false
}

// Link is one instance's web UI navigation entry (tray submenu).
type Link struct {
	Name string
	URL  string
}

// WebLinks lists the web UI entries of every configured instance, in
// configured order, labeled with instance names. Disabled instances are
// included: navigation remains useful independently of polling. Instances
// whose kind exposes no web UI are omitted.
func WebLinks(instances []config.SourceInstance) []Link {
	links := make([]Link, 0, len(instances))
	for _, inst := range instances {
		def, ok := Get(inst.Kind)
		if !ok {
			continue
		}
		if url, has := def.WebURL(inst); has {
			links = append(links, Link{Name: inst.Name, URL: url})
		}
	}

	return links
}

func missingSettings(inst config.SourceInstance) error {
	return fmt.Errorf("build %s instance %s: provider settings missing", inst.Kind, inst.ID)
}

// TestReport is the outcome of a Settings connection test. Multi-credential
// kinds report one line per capability so a partial configuration shows
// exactly what was verified and what was skipped.
type TestReport struct {
	// Lines are user-presentable results in capability order.
	Lines []string
	// OK is true only when at least one capability was actually exercised
	// and every exercised capability succeeded. An all-skipped report is
	// NOT a success: with no credential configured nothing was verified.
	OK bool
}

// namedCapability is one independently checkable credential capability.
type namedCapability struct {
	label string
	// run exercises the capability. present reports whether a credential
	// was configured for it at all; err carries any request failure.
	run func(ctx context.Context) (present bool, err error)
}

// capabilityTestReport runs every configured capability against its own
// endpoint and renders one line per capability:
//
//	<label>: OK / <label>: not configured (skipped) / <label>: <reason>
//
// Failure reasons are sanitized source messages — never raw provider
// payloads or tokens. Nothing persists; the caller owns that boundary.
func capabilityTestReport(ctx context.Context, checks []namedCapability) TestReport {
	report := TestReport{Lines: make([]string, 0, len(checks))}
	tested, failed := 0, 0
	for _, c := range checks {
		present, err := c.run(ctx)
		switch {
		case err != nil:
			failed++
			report.Lines = append(report.Lines, c.label+": "+sanitizedReason(err))
		case !present:
			report.Lines = append(report.Lines, c.label+": not configured (skipped)")
		default:
			tested++
			report.Lines = append(report.Lines, c.label+": OK")
		}
	}
	report.OK = tested > 0 && failed == 0

	return report
}

// hindsightTestReport renders the Hindsight connection test as four
// lines: Health, Liveness (/health/live), Metrics, and Banks with the
// visible bank count. The liveness probe reports on its own line so a
// proxy rule stranding exactly that endpoint is visible by name instead
// of masquerading as a /health failure (DECISION_LOG, 2026-08-29). All
// four surfaces are probed even when one fails, so a partial
// misconfiguration shows every broken surface at once.
func hindsightTestReport(ctx context.Context, inst config.SourceInstance, deps Deps) TestReport {
	s := inst.Hindsight
	if s == nil {
		return failedTestReport(missingSettings(inst))
	}
	account, err := credentials.AccountForInstance(inst.ID, credentials.SlotAPIKey)
	if err != nil {
		return failedTestReport(err)
	}
	out := hindsight.New(s.BaseURL, account, deps.Creds, deps.Client, deps.Log).Test(ctx)
	if out.Err != nil {
		return failedTestReport(out.Err)
	}
	health := "Health: OK"
	if out.HealthErr != nil {
		health = "Health: " + sanitizedReason(out.HealthErr)
	}
	liveness := "Liveness: OK"
	if out.LiveErr != nil {
		liveness = "Liveness: " + sanitizedReason(out.LiveErr)
	}
	metrics := "Metrics: OK"
	if out.MetricsErr != nil {
		metrics = "Metrics: " + sanitizedReason(out.MetricsErr)
	}
	banks := fmt.Sprintf("Banks: OK (%d visible)", out.BanksTotal)
	if out.BanksErr != nil {
		banks = "Banks: " + sanitizedReason(out.BanksErr)
	}

	return TestReport{
		Lines: []string{health, liveness, metrics, banks},
		OK:    out.OK(),
	}
}

// noWebURL is the WebURL of kinds without a provider web UI; the Codex
// subscription has no quota dashboard AI Monitor could point at
// (docs/S6_UI_UX.md).
func noWebURL(config.SourceInstance) (string, bool) {
	return "", false
}

// codexTestReport renders the Codex connection test as one line per stage,
// so the deepest stage that broke is the one named (docs/S6_UI_UX.md):
//
//	Codex CLI: OK
//	ChatGPT: OK · Plus
//	Rate limits: OK · 2 windows
//
// The probe itself is the adapter's throwaway one-shot; it never persists a
// process and never shows account email, ID or token.
func codexTestReport(ctx context.Context, inst config.SourceInstance, deps Deps) TestReport {
	s := inst.Codex
	if s == nil {
		return failedTestReport(missingSettings(inst))
	}
	out, err := codex.New(*s, deps.Log, deps.AppVersion).Test(ctx)
	if err != nil {
		if out.CLIIncompatible {
			return TestReport{Lines: []string{
				"Codex CLI: incompatible",
				"This version does not support account/rateLimits/read.",
			}}
		}
		switch out.CLIState {
		case codex.DiscoveryNotFoundInPath:
			return TestReport{Lines: []string{
				"Codex CLI: not found in PATH",
				"Install Codex CLI or choose its executable/wrapper.",
			}}
		case codex.DiscoveryExplicitMissing, codex.DiscoveryNotExecutable:
			return TestReport{Lines: []string{"Codex CLI: not found or not executable"}}
		}

		// The launcher resolved; the exchange with ChatGPT is what broke.
		return TestReport{Lines: []string{"Codex CLI: OK", "ChatGPT: " + sanitizedReason(err)}}
	}
	chatgpt := "ChatGPT: OK"
	if plan := CodexPlanLabel(out.PlanType); plan != "" {
		chatgpt += " · " + plan
	}

	return TestReport{
		Lines: []string{"Codex CLI: OK", chatgpt, fmt.Sprintf("Rate limits: OK · %d windows", out.Windows)},
		OK:    true,
	}
}

// CodexPlanLabel titles the provider's plan name where the known values are
// concerned and passes anything else through trimmed-but-unchanged, so an
// unknown future plan never renders as garbage or as a fake known one. The
// UI reuses it so the overview title suffix and the detail Account row spell
// the plan identically.
func CodexPlanLabel(plan string) string {
	switch strings.ToLower(strings.TrimSpace(plan)) {
	case "plus":
		return "Plus"
	case "pro":
		return "Pro"
	case "go":
		return "Go"
	case "free":
		return "Free"
	}

	return strings.TrimSpace(plan)
}

// singleTestReport wraps a kind whose whole test is one operation,
// preserving the historical single-line editor wording.
func singleTestReport(err error) TestReport {
	if err != nil {
		return TestReport{Lines: []string{"Failed: " + err.Error()}}
	}

	return TestReport{Lines: []string{"OK"}, OK: true}
}

// failedTestReport wraps an infrastructure failure (bad settings, missing
// store) for both report shapes.
func failedTestReport(err error) TestReport {
	return TestReport{Lines: []string{"Failed: " + err.Error()}}
}

// sanitizedReason prefers the categorized user-facing message of a source
// error over its raw "category: detail" text.
func sanitizedReason(err error) string {
	var serr *source.Error
	if errors.As(err, &serr) {
		return serr.UserMessage()
	}

	return err.Error()
}
