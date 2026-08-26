package credentials

import (
	"testing"

	"git.skobk.in/skobkin/aimonitor/internal/domain"
)

func TestAccountForInstanceFormat(t *testing.T) {
	id, err := AccountForInstance("0123456789abcdef0123456789abcdef", SlotAPIKey)
	if err != nil {
		t.Fatalf("account: %v", err)
	}
	if want := AccountID("source/0123456789abcdef0123456789abcdef/api-key"); id != want {
		t.Errorf("account = %q, want %q", id, want)
	}
}

func TestAccountForInstanceSeparatesSameKindInstances(t *testing.T) {
	a, err := AccountForInstance("aaaa", SlotAPIKey)
	if err != nil {
		t.Fatalf("account a: %v", err)
	}
	b, err := AccountForInstance("bbbb", SlotAPIKey)
	if err != nil {
		t.Fatalf("account b: %v", err)
	}
	if a == b {
		t.Fatalf("two instances must not share an account: %q", a)
	}
}

// Credentials are keyed by instance ID only, so a rename (a name change on
// the same ID) or a reorder (no ID change at all) can never move them.
func TestAccountForInstanceIndependentOfNameAndOrder(t *testing.T) {
	first, err := AccountForInstance("stable-id", SlotAPIKey)
	if err != nil {
		t.Fatalf("account: %v", err)
	}
	again, err := AccountForInstance("stable-id", SlotAPIKey)
	if err != nil {
		t.Fatalf("account again: %v", err)
	}
	if first != again {
		t.Errorf("account is not deterministic: %q vs %q", first, again)
	}
}

func TestAccountForInstanceSlotSpellingStable(t *testing.T) {
	if SlotAPIKey != "api-key" {
		t.Errorf("SlotAPIKey = %q, changing it orphans stored credentials", SlotAPIKey)
	}
}

func TestAccountForInstanceRejectsEmpty(t *testing.T) {
	if _, err := AccountForInstance("", SlotAPIKey); err == nil {
		t.Error("empty instance ID must be rejected")
	}
	if _, err := AccountForInstance(domain.InstanceID("abc"), ""); err == nil {
		t.Error("empty slot must be rejected")
	}
}
