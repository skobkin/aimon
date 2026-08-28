package credentials

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
)

// fakeKeyring records calls and replays scripted results. Tests never
// touch a real keychain.
type fakeKeyring struct {
	stored map[string]string // "service/user" -> secret

	getErr    error
	setErr    error
	deleteErr error
}

func newFakeKeyring() *fakeKeyring {
	return &fakeKeyring{stored: map[string]string{}}
}

// testAccount is one instance-scoped account, as production code builds
// them. Tests never touch a real keychain.
func testAccount() AccountID {
	acc, err := AccountForInstance("test-instance-1", SlotAPIKey)
	if err != nil {
		panic(err)
	}
	return acc
}

func (f *fakeKeyring) get(service, user string) (string, error) {
	if f.getErr != nil {
		return "", f.getErr
	}
	v, ok := f.stored[service+"/"+user]
	if !ok {
		return "", keyring.ErrNotFound
	}
	return v, nil
}

func (f *fakeKeyring) set(service, user, password string) error {
	if f.setErr != nil {
		return f.setErr
	}
	f.stored[service+"/"+user] = password
	return nil
}

func (f *fakeKeyring) delete(service, user string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	delete(f.stored, service+"/"+user)
	return nil
}

func TestGetNotFoundPassesThrough(t *testing.T) {
	s := newKeyringStore(keyringOps{get: newFakeKeyring().get, set: newFakeKeyring().set, delete: newFakeKeyring().delete})

	_, err := s.Get(context.Background(), testAccount())
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestGetSetRoundTrip(t *testing.T) {
	fake := newFakeKeyring()
	s := newKeyringStore(keyringOps{get: fake.get, set: fake.set, delete: fake.delete})
	ctx := context.Background()

	if err := s.Set(ctx, testAccount(), "test-token"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := s.Get(ctx, testAccount())
	if err != nil || got != "test-token" {
		t.Fatalf("Get = (%q, %v), want (test-token, nil)", got, err)
	}
	if v := fake.stored[ServiceName+"/source/test-instance-1/api-key"]; v != "test-token" {
		t.Errorf("stored under %q with wrong value %q", ServiceName+"/source/test-instance-1/api-key", v)
	}
}

func TestDeleteIdempotent(t *testing.T) {
	fake := newFakeKeyring()
	s := newKeyringStore(keyringOps{get: fake.get, set: fake.set, delete: fake.delete})
	ctx := context.Background()

	if err := s.Delete(ctx, testAccount()); err != nil {
		t.Fatalf("deleting an unset account must succeed, got %v", err)
	}
	if err := s.Set(ctx, testAccount(), "k"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, testAccount()); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get(ctx, testAccount()); !errors.Is(err, ErrNotFound) {
		t.Errorf("after delete, Get should be ErrNotFound, got %v", err)
	}
}

func TestStateClassification(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want State
	}{
		{"unsupported platform", keyring.ErrUnsupportedPlatform, StateUnsupported},
		{"collection locked", errors.New("The secret service collection is locked"), StateLocked},
		{"unlock failed", errors.New("failed to unlock keyring"), StateLocked},
		{"prompt dismissed", errors.New("prompt dismissed by user"), StateLocked},
		{"dbus down", errors.New("dbus connection failed"), StateUnavailable},
		{"zombie margin", errors.New("secret service died unexpectedly"), StateUnavailable},
	}
	for _, tc := range cases {
		fake := newFakeKeyring()
		fake.getErr = tc.err
		s := newKeyringStore(keyringOps{get: fake.get, set: fake.set, delete: fake.delete})

		_, err := s.Get(context.Background(), testAccount())
		var se *StateError
		if !errors.As(err, &se) {
			t.Errorf("%s: expected *StateError, got %T %v", tc.name, err, err)
			continue
		}
		if se.State != tc.want {
			t.Errorf("%s: state = %q, want %q", tc.name, se.State, tc.want)
		}
		if !errors.Is(se, tc.err) {
			t.Errorf("%s: StateError must unwrap to the cause", tc.name)
		}
		if !strings.Contains(se.Error(), string(tc.want)) {
			t.Errorf("%s: Error() should mention the state: %q", tc.name, se.Error())
		}
	}
}

func TestStatusProbe(t *testing.T) {
	ctx := context.Background()

	// Healthy backend, probe account simply absent -> available.
	fake := newFakeKeyring()
	s := newKeyringStore(keyringOps{get: fake.get, set: fake.set, delete: fake.delete})
	st := s.Status(ctx)
	if st.State != StateAvailable || st.Backend == "" {
		t.Fatalf("probe of healthy backend = %+v", st)
	}

	// A stored probe value still means available.
	fake2 := newFakeKeyring()
	fake2.stored[ServiceName+"/status-probe"] = "x"
	s2 := newKeyringStore(keyringOps{get: fake2.get, set: fake2.set, delete: fake2.delete})
	if st := s2.Status(ctx); st.State != StateAvailable {
		t.Errorf("probe with existing value = %+v, want available", st)
	}

	// Backend errors surface with classification and message.
	fake3 := newFakeKeyring()
	fake3.getErr = errors.New("secret service is locked")
	s3 := newKeyringStore(keyringOps{get: fake3.get, set: fake3.set, delete: fake3.delete})
	st3 := s3.Status(ctx)
	if st3.State != StateLocked || st3.Message == "" {
		t.Errorf("locked probe = %+v", st3)
	}
}

// blockingGet models a wedged Secret Service: the call never returns.
func blockingGet(service, user string) (string, error) {
	<-make(chan struct{}) // blocks forever
	return "", nil
}

// A wedged backend must not hang the caller: once ctx expires, Status and
// Get report an unavailable backend instead of blocking forever. The
// abandoned goroutine itself keeps waiting — that is go-keyring's
// ctx-less API, bounding caller latency is all a caller can do.
func TestKeyringOpsHonorContextDeadline(t *testing.T) {
	s := newKeyringStore(keyringOps{get: blockingGet, set: func(string, string, string) error { return nil }, delete: func(string, string) error { return nil }})

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	st := s.Status(ctx)
	if st.State != StateUnavailable || !strings.Contains(st.Message, "abandoned") {
		t.Errorf("wedged probe = %+v, want unavailable with an abandonment message", st)
	}
	if _, err := s.Get(ctx, testAccount()); !errors.As(err, new(*StateError)) {
		t.Errorf("wedged Get error = %v, want *StateError", err)
	}
	if err := s.Set(ctx, testAccount(), "x"); !errors.As(err, new(*StateError)) {
		t.Errorf("wedged Set error = %v, want *StateError", err)
	}
	if err := s.Delete(ctx, testAccount()); !errors.As(err, new(*StateError)) {
		t.Errorf("wedged Delete error = %v, want *StateError", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("calls blocked for %v; an expired ctx must unblock the caller", elapsed)
	}
}

func TestSetUsesServiceAndAccount(t *testing.T) {
	fake := newFakeKeyring()
	s := newKeyringStore(keyringOps{get: fake.get, set: fake.set, delete: fake.delete})

	_ = s.Set(context.Background(), testAccount(), "secret-value")

	if len(fake.stored) != 1 {
		t.Fatalf("expected exactly one stored entry, got %d", len(fake.stored))
	}
	if _, ok := fake.stored["aimonitor/source/test-instance-1/api-key"]; !ok {
		t.Errorf("entry must be stored under service/account, have %v", fake.stored)
	}
}
