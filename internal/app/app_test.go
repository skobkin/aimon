package app

import (
	"context"
	"sync"
	"testing"

	"github.com/skobkin/llm-monitor/internal/credentials"
)

// memStore is a fake credential store; it never touches a keychain.
type memStore struct {
	mu     sync.Mutex
	secret string
}

func (s *memStore) Status(ctx context.Context) credentials.Status {
	return credentials.Status{State: credentials.StateAvailable}
}

func (s *memStore) Get(ctx context.Context, id credentials.AccountID) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.secret == "" {
		return "", credentials.ErrNotFound
	}
	return s.secret, nil
}

func (s *memStore) Set(ctx context.Context, id credentials.AccountID, secret string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.secret = secret
	return nil
}

func (s *memStore) Delete(ctx context.Context, id credentials.AccountID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.secret = ""
	return nil
}

func TestOverrideStorePrefersTypedValue(t *testing.T) {
	inner := &memStore{secret: "stored-value"}
	o := &overrideStore{inner: inner, account: credentials.AccountZAI, override: "typed-value"}

	got, err := o.Get(context.Background(), credentials.AccountZAI)
	if err != nil || got != "typed-value" {
		t.Errorf("Get with override = %q, %v; want typed-value", got, err)
	}
}

func TestOverrideStoreDelegatesWithoutOverride(t *testing.T) {
	inner := &memStore{secret: "stored-value"}
	o := &overrideStore{inner: inner, account: credentials.AccountZAI, override: ""}

	got, err := o.Get(context.Background(), credentials.AccountZAI)
	if err != nil || got != "stored-value" {
		t.Errorf("Get without override = %q, %v; want stored-value", got, err)
	}
}

func TestOverrideStoreDelegatesOtherAccounts(t *testing.T) {
	inner := &memStore{secret: "stored-value"}
	o := &overrideStore{inner: inner, account: credentials.AccountZAI, override: "typed-value"}

	got, err := o.Get(context.Background(), credentials.AccountMiniMax)
	if err != nil || got != "stored-value" {
		t.Errorf("Get for other account = %q, %v; want stored-value", got, err)
	}
}
