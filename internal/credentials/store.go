// Package credentials stores source credentials in the system keychain
// via github.com/zalando/go-keyring. There is no plaintext fallback in S1
// legacy decision D12, docs/DECISION_LOG.md): when the keychain is
// unavailable, credential features
// degrade, they do not bypass.
package credentials

import (
	"context"
	"errors"
	"fmt"

	"git.skobk.in/skobkin/aimonitor/internal/domain"
)

// State describes the usability of the keychain backend.
type State string

// Keychain backend states.
const (
	// StateAvailable: reads and writes are expected to work.
	StateAvailable State = "available"
	// StateLocked: the keychain exists but requires an unlock/prompts.
	StateLocked State = "locked"
	// StateUnavailable: the backend cannot be reached right now.
	StateUnavailable State = "unavailable"
	// StateUnsupported: the platform has no keychain implementation.
	StateUnsupported State = "unsupported"
)

// Status is a point-in-time report about the keychain backend, surfaced in
// Settings so users can see why credential rows are disabled.
type Status struct {
	Backend string
	State   State
	Message string
}

// AccountID names one stored credential. Stable identifiers; changing one
// orphans the stored secret. Accounts are built by AccountForInstance;
// v0.2 installations may still hold old kind-keyed entries
// ("source/llama-swap" and friends) that are intentionally never migrated
// or deleted.
type AccountID string

// Slot identifies one credential within a source instance. S3 needs only
// the API key slot; the concept exists so a future kind with more than one
// secret needs no new key scheme.
type Slot string

// SlotAPIKey is the ordinary API key/token slot used by the credentialed
// kinds (llama-swap optional, MiniMax, Z.AI / GLM, and the inference key of
// OpenRouter).
const SlotAPIKey Slot = "api-key"

// SlotManagementKey is OpenRouter's second, independent credential slot:
// a Management key granting account-level credit reads. It is never used as
// an API-key substitute or vice versa.
const SlotManagementKey Slot = "management-key"

// AccountForInstance builds the keychain account for one instance's
// credential slot: "source/<instance-id>/<slot>". The account is scoped by
// the stable opaque instance ID, so a rename, reorder, or enable change
// never moves credentials, and two instances of the same kind never share
// one. An empty instance ID or slot is a programming error and is rejected.
func AccountForInstance(id domain.InstanceID, slot Slot) (AccountID, error) {
	if id == "" {
		return "", fmt.Errorf("credential account: empty instance ID")
	}
	if slot == "" {
		return "", fmt.Errorf("credential account: empty slot")
	}

	return AccountID(fmt.Sprintf("source/%s/%s", id, slot)), nil
}

// ErrNotFound reports that no credential is stored for an account. It is
// distinct from a broken keychain: callers treat it as "not configured".
var ErrNotFound = errors.New("credential not found")

// Store reads and writes credentials. Implementations must be safe for
// concurrent use and must honor ctx: the keychain calls themselves may be
// synchronous, but a cancelled ctx must unblock the caller (a wedged
// Secret Service must never hang the UI or startup).
type Store interface {
	// Status probes the backend without touching any real account.
	Status(ctx context.Context) Status
	// Get returns the stored secret, or ("", ErrNotFound) when unset.
	// A backend problem is reported as *StateError.
	Get(ctx context.Context, id AccountID) (string, error)
	// Set stores (overwrites) the secret for an account.
	Set(ctx context.Context, id AccountID, secret string) error
	// Delete removes the credential; deleting an unset account succeeds.
	Delete(ctx context.Context, id AccountID) error
}

// StateError reports that the keychain backend itself is in a state that
// prevented the operation. State says which one; Message is safe for UI
// display; Err is the underlying cause (may be nil).
type StateError struct {
	State   State
	Message string
	Err     error
}

func (e *StateError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("keychain %s: %s: %v", e.State, e.Message, e.Err)
	}

	return fmt.Sprintf("keychain %s: %s", e.State, e.Message)
}

func (e *StateError) Unwrap() error { return e.Err }
