// Source instances: the S3 dynamic configuration model. A configured source
// is one instance of a compiled source kind; multiple instances of the same
// kind are valid. Credentials are deliberately absent — they live only in
// the system keychain under instance-scoped accounts.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"git.skobk.in/skobkin/aimonitor/internal/domain"
)

// ProviderSettings is the provider-specific non-secret configuration of one
// source instance. Exactly one concrete settings value is present per
// instance, matching its Kind; the concrete structs are comparable, which
// the app uses to detect material configuration changes without restarting
// a poller.
type ProviderSettings interface {
	providerSettings()
}

// SourceInstance is one configured source: a compiled adapter kind with
// user-chosen non-secret settings. Names are presentation only — duplicates
// are allowed and the name never forms identity. The kind is immutable
// after creation; to change it, delete the instance and create a new one.
type SourceInstance struct {
	// ID is the stable opaque instance identity (domain.InstanceID),
	// generated at creation and never reused as anything else.
	ID domain.InstanceID
	// Kind selects the compiled adapter.
	Kind domain.SourceKind
	// Name is the user-visible label shown everywhere.
	Name         string
	Enabled      bool
	PollInterval time.Duration

	// Exactly one provider settings pointer is non-nil, matching Kind.
	AMDGPUWeb *AMDGPUWebSettings
	LlamaSwap *LlamaSwapSettings
	MiniMax   *MiniMaxSettings
	ZAI       *ZAISettings
}

// AMDGPUWebSettings are the amdgputop-web settings.
type AMDGPUWebSettings struct {
	BaseURL       string
	SelectedGPUID string
}

// LlamaSwapSettings are the llama-swap settings. An API key, when used, is
// stored in the keychain under the instance's account — never here.
type LlamaSwapSettings struct {
	BaseURL string
}

// MiniMaxSettings are the MiniMax settings; the endpoint is fixed, so there
// is nothing to configure beyond the common fields.
type MiniMaxSettings struct{}

// ZAISettings are the Z.AI / GLM settings. The base URL is configurable to
// allow the bigmodel.cn mirror.
type ZAISettings struct {
	BaseURL string
}

func (AMDGPUWebSettings) providerSettings() {}
func (LlamaSwapSettings) providerSettings() {}
func (MiniMaxSettings) providerSettings()   {}
func (ZAISettings) providerSettings()       {}

// Settings returns the provider settings value matching Kind. ok is false
// when the pointer matching Kind is nil (a mismatch the pointer fields may
// still hold is reported by Validate, not here).
func (i SourceInstance) Settings() (ProviderSettings, bool) {
	switch i.Kind {
	case domain.KindAMDGPUWeb:
		if i.AMDGPUWeb != nil {
			return *i.AMDGPUWeb, true
		}
	case domain.KindLlamaSwap:
		if i.LlamaSwap != nil {
			return *i.LlamaSwap, true
		}
	case domain.KindMiniMax:
		if i.MiniMax != nil {
			return *i.MiniMax, true
		}
	case domain.KindZAI:
		if i.ZAI != nil {
			return *i.ZAI, true
		}
	}
	return nil, false
}

// Label names the instance in validation messages: the user-visible name
// when set, otherwise the kind.
func (i SourceInstance) Label() string {
	if name := strings.TrimSpace(i.Name); name != "" {
		return name
	}
	return string(i.Kind)
}

// ValidateInstances checks a whole configured source list: every instance
// must be individually valid (see SourceInstance.Validate) and instance IDs
// must be unique. Duplicate names are explicitly allowed.
func ValidateInstances(list []SourceInstance) []ValidationError {
	var errs []ValidationError
	seen := make(map[domain.InstanceID]int, len(list))
	for idx := range list {
		inst := list[idx]
		errs = append(errs, inst.Validate()...)
		if prev, dup := seen[inst.ID]; dup {
			errs = append(errs, ValidationError{
				Source:  inst.Label(),
				Field:   "ID",
				Message: fmt.Sprintf("duplicate instance ID (also used by source %d)", prev+1),
			})
		} else {
			seen[inst.ID] = idx
		}
	}
	return errs
}

// Validate checks one instance: known kind, non-empty name, exactly one
// provider settings pointer matching the kind, a usable URL where the kind
// has one, and a poll interval from the kind's presets.
func (i SourceInstance) Validate() []ValidationError {
	var errs []ValidationError
	label := i.Label()

	if strings.TrimSpace(i.Name) == "" {
		errs = append(errs, ValidationError{Source: label, Field: "Name", Message: "must not be empty"})
	}

	set := 0
	if i.AMDGPUWeb != nil {
		set++
	}
	if i.LlamaSwap != nil {
		set++
	}
	if i.MiniMax != nil {
		set++
	}
	if i.ZAI != nil {
		set++
	}

	switch i.Kind {
	case domain.KindAMDGPUWeb, domain.KindLlamaSwap, domain.KindMiniMax, domain.KindZAI:
	default:
		errs = append(errs, ValidationError{Source: label, Field: "Kind", Message: fmt.Sprintf("unknown source kind %q", string(i.Kind))})
	}
	if set != 1 {
		errs = append(errs, ValidationError{Source: label, Field: "Settings", Message: "exactly one provider settings block is required"})
		return errs
	}

	// set == 1 from here on; check the block matches the kind and the
	// kind-specific fields are valid.
	switch i.Kind {
	case domain.KindAMDGPUWeb:
		if i.AMDGPUWeb == nil {
			errs = append(errs, mismatchSettings(label))
		} else {
			errs = append(errs, validateURL(label, i.AMDGPUWeb.BaseURL)...)
		}
	case domain.KindLlamaSwap:
		if i.LlamaSwap == nil {
			errs = append(errs, mismatchSettings(label))
		} else {
			errs = append(errs, validateURL(label, i.LlamaSwap.BaseURL)...)
		}
	case domain.KindMiniMax:
		if i.MiniMax == nil {
			errs = append(errs, mismatchSettings(label))
		}
	case domain.KindZAI:
		if i.ZAI == nil {
			errs = append(errs, mismatchSettings(label))
		} else {
			errs = append(errs, validateURL(label, i.ZAI.BaseURL)...)
		}
	}

	errs = append(errs, validateInterval(label, i.PollInterval, KindIntervals(i.Kind))...)
	return errs
}

func mismatchSettings(label string) ValidationError {
	return ValidationError{Source: label, Field: "Settings", Message: "provider settings do not match the source kind"}
}

// KindIntervals returns the offered poll interval presets for a kind's
// category (DECISIONS D8: local sources poll faster than remote plans).
func KindIntervals(kind domain.SourceKind) []time.Duration {
	if kind.Category() == domain.CategoryLocal {
		return LocalIntervals
	}
	return RemoteIntervals
}

// SameSettings reports whether two provider settings values are equal (same
// concrete type and same field values). It drives adapter rebuild detection:
// rename, reorder, and enable changes never change settings, so the poller
// keeps running; a URL/GPU change does and only that instance is rebuilt.
func SameSettings(a, b ProviderSettings) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	switch av := a.(type) {
	case AMDGPUWebSettings:
		bv, ok := b.(AMDGPUWebSettings)
		return ok && av == bv
	case LlamaSwapSettings:
		bv, ok := b.(LlamaSwapSettings)
		return ok && av == bv
	case MiniMaxSettings:
		_, ok := b.(MiniMaxSettings)
		return ok
	case ZAISettings:
		bv, ok := b.(ZAISettings)
		return ok && av == bv
	}
	return false
}

// joinValidationErrors converts validation errors into one joined error
// for callers that return a plain error.
func joinValidationErrors(errs []ValidationError) error {
	list := make([]error, len(errs))
	for i, e := range errs {
		list[i] = e
	}
	return errors.Join(list...)
}

// GenerateInstanceID returns a new random opaque instance ID: 128 bits from
// crypto/rand, hex-encoded. IDs are independent of kind and name; the
// stdlib generator avoids adding a UUID dependency for this alone.
func GenerateInstanceID() (domain.InstanceID, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate instance ID: %w", err)
	}
	return domain.InstanceID(hex.EncodeToString(b[:])), nil
}
