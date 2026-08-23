package credentials

import (
	"context"
	"errors"
	"runtime"
	"strings"

	"github.com/zalando/go-keyring"
)

// ServiceName namespaces all llm-monitor entries inside the keychain.
const ServiceName = "llm-monitor"

// statusProbeAccount is a reserved account name used only to probe the
// backend; no real credential is ever stored under it.
const statusProbeAccount = "status-probe"

// keyringOps is the injectable seam over the go-keyring package so tests
// never touch a real keychain.
type keyringOps struct {
	get    func(service, user string) (string, error)
	set    func(service, user, password string) error
	delete func(service, user string) error
}

// KeyringStore is the production Store over the OS keychain.
type KeyringStore struct {
	ops keyringOps
}

// NewKeyringStore returns a Store backed by the system keychain.
func NewKeyringStore() *KeyringStore {
	return &KeyringStore{ops: keyringOps{
		get:    keyring.Get,
		set:    keyring.Set,
		delete: keyring.Delete,
	}}
}

// newKeyringStore builds a store over injected operations (tests only).
func newKeyringStore(ops keyringOps) *KeyringStore {
	return &KeyringStore{ops: ops}
}

// Status probes the backend with a read of a reserved account. A "not
// found" answer means the keychain answered us — it is available.
func (s *KeyringStore) Status(ctx context.Context) Status {
	backend := backendName()
	_, err := s.ops.get(ServiceName, statusProbeAccount)
	if err == nil || errors.Is(err, keyring.ErrNotFound) {
		return Status{Backend: backend, State: StateAvailable}
	}
	return Status{
		Backend: backend,
		State:   classifyState(err),
		Message: err.Error(),
	}
}

// Get implements Store.
func (s *KeyringStore) Get(ctx context.Context, id AccountID) (string, error) {
	v, err := s.ops.get(ServiceName, string(id))
	if err != nil {
		return "", mapKeyringError(err)
	}
	return v, nil
}

// Set implements Store.
func (s *KeyringStore) Set(ctx context.Context, id AccountID, secret string) error {
	if err := s.ops.set(ServiceName, string(id), secret); err != nil {
		return mapKeyringError(err)
	}
	return nil
}

// Delete implements Store. Deleting an unset account is a success.
func (s *KeyringStore) Delete(ctx context.Context, id AccountID) error {
	if err := s.ops.delete(ServiceName, string(id)); err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return nil
		}
		return mapKeyringError(err)
	}
	return nil
}

// mapKeyringError translates go-keyring errors onto the Store contract:
// not-found passes through as ErrNotFound, everything else becomes a
// *StateError classified by cause.
func mapKeyringError(err error) error {
	if errors.Is(err, keyring.ErrNotFound) {
		return ErrNotFound
	}
	return &StateError{State: classifyState(err), Message: err.Error(), Err: err}
}

// classifyState maps a keychain error onto a backend state, following the
// qBtRemoteGo approach: platform support failures are explicit, Secret
// Service lock/prompt problems read as locked, and anything else is a
// generic availability failure.
func classifyState(err error) State {
	if errors.Is(err, keyring.ErrUnsupportedPlatform) {
		return StateUnsupported
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "locked") || strings.Contains(msg, "unlock") || strings.Contains(msg, "prompt") {
		return StateLocked
	}
	return StateUnavailable
}

// backendName returns a user-facing name for the platform keychain.
func backendName() string {
	switch runtime.GOOS {
	case "linux":
		return "Secret Service"
	case "darwin":
		return "Keychain"
	case "windows":
		return "Windows Credential Manager"
	}
	return "system keychain"
}
