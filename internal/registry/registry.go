// Package registry is the compiled table of source kinds: the one place
// that knows each kind's category, defaults, credential slots, web UI, and
// how to build and test its adapter. This is a static registry, not a
// plugin system — adding a kind means adding a Definition here and its
// adapter package, then recompiling.
package registry

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"git.skobk.in/skobkin/aimonitor/internal/config"
	"git.skobk.in/skobkin/aimonitor/internal/credentials"
	"git.skobk.in/skobkin/aimonitor/internal/domain"
	"git.skobk.in/skobkin/aimonitor/internal/source"
	"git.skobk.in/skobkin/aimonitor/internal/source/amdgpuweb"
	"git.skobk.in/skobkin/aimonitor/internal/source/llamaswap"
	"git.skobk.in/skobkin/aimonitor/internal/source/minimax"
	"git.skobk.in/skobkin/aimonitor/internal/source/zai"
)

// Console pages of the remote providers (their own usage dashboards, not
// the API endpoints the adapters poll).
const (
	MiniMaxConsoleURL = "https://platform.minimax.io/console/usage"
	ZAIUsageURL       = "https://z.ai/manage-apikey/coding-plan/personal/usage"
)

// Deps are the shared construction dependencies handed to Build and Test.
// Creds may be wrapped (for example an override store carrying a typed but
// unsaved secret for a Settings connection test).
type Deps struct {
	Creds  credentials.Store
	Client *http.Client
	Log    *slog.Logger
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
	// editor values. It never persists anything.
	Test func(ctx context.Context, inst config.SourceInstance, deps Deps) error
	// WebURL derives the provider web UI URL from instance settings; ok is
	// false for kinds without a web UI.
	WebURL func(inst config.SourceInstance) (string, bool)
}

// apiKeySlots is the single-slot credential set shared by the credentialed
// kinds.
var apiKeySlots = []credentials.Slot{credentials.SlotAPIKey}

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
		Test: func(ctx context.Context, inst config.SourceInstance, deps Deps) error {
			s := inst.AMDGPUWeb
			if s == nil {
				return missingSettings(inst)
			}
			_, err := amdgpuweb.New(s.BaseURL, s.SelectedGPUID, deps.Client, deps.Log).Discover(ctx)
			return err
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
		Test: func(ctx context.Context, inst config.SourceInstance, deps Deps) error {
			s := inst.LlamaSwap
			if s == nil {
				return missingSettings(inst)
			}
			account, err := credentials.AccountForInstance(inst.ID, credentials.SlotAPIKey)
			if err != nil {
				return err
			}
			return llamaswap.New(s.BaseURL, account, deps.Creds, deps.Client, deps.Log).Test(ctx)
		},
		WebURL: func(inst config.SourceInstance) (string, bool) {
			if inst.LlamaSwap == nil {
				return "", false
			}
			return inst.LlamaSwap.BaseURL + "/ui", true
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
		Test: func(ctx context.Context, inst config.SourceInstance, deps Deps) error {
			if inst.MiniMax == nil {
				return missingSettings(inst)
			}
			account, err := credentials.AccountForInstance(inst.ID, credentials.SlotAPIKey)
			if err != nil {
				return err
			}
			if deps.Creds == nil {
				return fmt.Errorf("test %s instance %s: a credential store is required", inst.Kind, inst.ID)
			}
			_, err = minimax.New(account, deps.Creds, deps.Client, deps.Log).Refresh(ctx)
			return err
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
		Test: func(ctx context.Context, inst config.SourceInstance, deps Deps) error {
			s := inst.ZAI
			if s == nil {
				return missingSettings(inst)
			}
			account, err := credentials.AccountForInstance(inst.ID, credentials.SlotAPIKey)
			if err != nil {
				return err
			}
			if deps.Creds == nil {
				return fmt.Errorf("test %s instance %s: a credential store is required", inst.Kind, inst.ID)
			}
			_, err = zai.New(s.BaseURL, account, deps.Creds, deps.Client, deps.Log).Refresh(ctx)
			return err
		},
		WebURL: func(inst config.SourceInstance) (string, bool) {
			if inst.ZAI == nil {
				return "", false
			}
			return ZAIUsageURL, true
		},
	},
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
