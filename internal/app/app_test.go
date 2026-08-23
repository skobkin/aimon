package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"git.skobk.in/skobkin/aimonitor/internal/credentials"
)

// TestMetadataMirrorsFyneAppToml guards the duplication between
// FyneApp.toml (packaged apps) and appMetadata (every other run): the
// values must stay identical or the fyne.Do migration flag and version
// drift between build modes.
func TestMetadataMirrorsFyneAppToml(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "FyneApp.toml"))
	if err != nil {
		t.Fatalf("read FyneApp.toml: %v", err)
	}
	toml := string(raw)
	m := appMetadata()

	for _, field := range []struct{ key, value string }{
		{"ID", m.ID},
		{"Name", m.Name},
		{"Version", m.Version},
	} {
		if !strings.Contains(toml, fmt.Sprintf("%s = %q", field.key, field.value)) {
			t.Errorf("FyneApp.toml does not set %s = %q", field.key, field.value)
		}
	}
	if !strings.Contains(toml, fmt.Sprintf("Build = %d", m.Build)) {
		t.Errorf("FyneApp.toml does not set Build = %d", m.Build)
	}
	if !m.Migrations["fyneDo"] {
		t.Error("appMetadata must opt into the fyne.Do threading model")
	}
	if !strings.Contains(toml, "fyneDo = true") {
		t.Error("FyneApp.toml does not opt into the fyne.Do threading model")
	}
	if m.ID != appID {
		t.Errorf("metadata ID = %q, want the app ID %q", m.ID, appID)
	}
}

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
