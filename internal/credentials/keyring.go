package credentials

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"

	"github.com/zalando/go-keyring"
)

// ServiceName namespaces all aimonitor entries inside the keychain.
const ServiceName = "aimonitor"

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

// await runs the blocking keychain operation on its own goroutine and
// unblocks the caller when ctx is done, abandoning the operation.
//
// The go-keyring API has no context parameter, and its Secret Service
// binding can block indefinitely (a dbus daemon that never answers, an
// unlock prompt nobody sees). Every caller therefore passes a bounded
// context and reads "abandoned" as an unavailable backend instead of
// hanging the UI or startup. The abandoned goroutine keeps running until
// the call returns; bounding caller latency is the goal, not reaping it.
func await(ctx context.Context, run func()) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		run()
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// abandonedErr classifies a context cancellation as an unavailable
// backend, the same bucket as a dbus daemon that went away.
func abandonedErr(err error) error {
	return &StateError{
		State:   StateUnavailable,
		Message: fmt.Sprintf("keychain did not answer before the operation was abandoned (%v)", err),
		Err:     err,
	}
}

// Status probes the backend with a read of a reserved account. A "not
// found" answer means the keychain answered us — it is available.
func (s *KeyringStore) Status(ctx context.Context) Status {
	backend := backendName()
	var err error
	if waitErr := await(ctx, func() { _, err = s.ops.get(ServiceName, statusProbeAccount) }); waitErr != nil {
		return Status{
			Backend: backend,
			State:   StateUnavailable,
			Message: fmt.Sprintf("probe abandoned: %v", waitErr),
		}
	}
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
	var v string
	var err error
	if waitErr := await(ctx, func() { v, err = s.ops.get(ServiceName, string(id)) }); waitErr != nil {
		return "", abandonedErr(waitErr)
	}
	if err != nil {
		return "", mapKeyringError(err)
	}

	return v, nil
}

// Set implements Store.
func (s *KeyringStore) Set(ctx context.Context, id AccountID, secret string) error {
	var err error
	if waitErr := await(ctx, func() { err = s.ops.set(ServiceName, string(id), secret) }); waitErr != nil {
		return abandonedErr(waitErr)
	}
	if err != nil {
		return mapKeyringError(err)
	}

	return nil
}

// Delete implements Store. Deleting an unset account is a success.
func (s *KeyringStore) Delete(ctx context.Context, id AccountID) error {
	var err error
	if waitErr := await(ctx, func() { err = s.ops.delete(ServiceName, string(id)) }); waitErr != nil {
		return abandonedErr(waitErr)
	}
	if err != nil {
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
