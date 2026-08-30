package ui

import (
	"testing"
	"time"

	"git.skobk.in/skobkin/aimonitor/internal/config"
	"git.skobk.in/skobkin/aimonitor/internal/domain"
)

// codexSnap builds a Codex snapshot for the presentation tests: a 5-hour
// primary window with a reset time, a 1-hour secondary without one, plus
// the optional metadata.
func codexSnap() domain.CodexSnapshot {
	reset := now.Add(2 * time.Hour)
	credits := int64(3)

	return domain.CodexSnapshot{
		CollectedAt: now,
		PlanType:    "plus",
		Windows: []domain.QuotaWindow{
			{ID: "codex-primary", Name: "5-hour", UsedFraction: 0.30, RemainingFraction: 0.70, ResetAt: &reset},
			{ID: "codex-secondary", Name: "1-hour", UsedFraction: 0.004, RemainingFraction: 0.996},
		},
		RateLimitReachedType:  "",
		ResetCreditsAvailable: &credits,
	}
}

func codexInstance(id string) config.SourceInstance {
	i := inst(id, "ChatGPT Codex", domain.KindCodex, true)
	i.Codex = &config.CodexSettings{}

	return i
}

func TestBuildCodexVMWindows(t *testing.T) {
	vm := BuildCodexVM(codexSnap(), now, false)
	if len(vm.Windows) != 2 {
		t.Fatalf("windows = %d, want 2", len(vm.Windows))
	}
	first := vm.Windows[0]
	if first.Name != "5-hour" || first.UsedText != "30% used" || first.Used != 0.30 || first.Remaining != 0.70 {
		t.Errorf("first row = %+v", first)
	}
	if first.ResetAt == nil {
		t.Error("primary reset instant lost")
	}
	if vm.Windows[1].ResetAt != nil || vm.Windows[1].ResetText != "" {
		t.Errorf("absent reset must stay absent: %+v", vm.Windows[1])
	}
	if len(vm.Account) != 0 {
		t.Errorf("overview must not carry account rows: %+v", vm.Account)
	}
}

func TestBuildCodexVMAccountRows(t *testing.T) {
	vm := BuildCodexVM(codexSnap(), now, true)
	if len(vm.Account) != 2 {
		t.Fatalf("account rows = %+v, want Plan and Reset credits", vm.Account)
	}
	if vm.Account[0].Label != "Plan" || vm.Account[0].Value != "Plus" {
		t.Errorf("plan row = %+v", vm.Account[0])
	}
	if vm.Account[1].Label != "Reset credits" || vm.Account[1].Value != "3" {
		t.Errorf("credits row = %+v", vm.Account[1])
	}
}

func TestBuildCodexVMAbsentOptionalFieldsOmitted(t *testing.T) {
	snap := codexSnap()
	snap.PlanType = ""
	snap.ResetCreditsAvailable = nil
	snap.RateLimitReachedType = ""
	vm := BuildCodexVM(snap, now, true)
	if len(vm.Account) != 0 {
		t.Fatalf("absent metadata produced rows: %+v", vm.Account)
	}

	snap.RateLimitReachedType = "primary"
	vm = BuildCodexVM(snap, now, true)
	if len(vm.Account) != 1 {
		t.Fatalf("reached rows = %+v, want one", vm.Account)
	}
	if vm.Account[0].Label != "Rate limit" || vm.Account[0].Level != MetricWarning {
		t.Errorf("reached row = %+v, want a warning-level status", vm.Account[0])
	}

	// Unknown plan names pass through unchanged and never as a fake one.
	snap.PlanType = "team"
	vm = BuildCodexVM(snap, now, true)
	if vm.Account[0].Value != "team" {
		t.Errorf("plan row = %+v, want the raw name", vm.Account[0])
	}
}

func TestBuildCodexOverview(t *testing.T) {
	vm := BuildCodexOverview(codexSnap(), now)
	if !vm.HasData || len(vm.Windows) != 2 {
		t.Fatalf("overview = %+v, want two donuts", vm)
	}
	if vm.Subtitle != "Plus" {
		t.Errorf("subtitle = %q, want \"Plus\"", vm.Subtitle)
	}
	if vm.Windows[0].Label != "5h" || vm.Windows[0].PctText != "30%" {
		t.Errorf("first donut = %+v", vm.Windows[0])
	}
	// Tiny real usage must never display as zero.
	if vm.Windows[1].PctText != "<1%" {
		t.Errorf("second donut = %+v, want \"<1%%\"", vm.Windows[1])
	}
	if vm.Windows[1].ResetAt != nil {
		t.Error("absent reset must stay absent on the donut")
	}

	// One usable window renders alone, without a fake second donut.
	single := codexSnap()
	single.Windows = single.Windows[:1]
	single.PlanType = ""
	vm = BuildCodexOverview(single, now)
	if len(vm.Windows) != 1 || vm.Subtitle != "" {
		t.Fatalf("single-window overview = %+v", vm)
	}
}

func TestBuildCodexCardsDispatch(t *testing.T) {
	rt := domain.Runtime{Enabled: true, LastSuccessAt: now, Snapshot: codexSnap()}
	card := BuildCard(codexInstance("codx1"), rt, now, true)
	if card.Kind != KindQuota {
		t.Fatalf("card kind = %q, want %q", card.Kind, KindQuota)
	}
	if len(card.Quota.Windows) != 2 || len(card.Quota.Account) == 0 {
		t.Fatalf("detail card = %+v", card.Quota)
	}

	overview := BuildOverviewCard(codexInstance("codx1"), rt, now)
	if overview.Kind != OverviewQuota {
		t.Fatalf("overview kind = %q, want %q", overview.Kind, OverviewQuota)
	}
	if overview.Quota.Subtitle != "Plus" || len(overview.Quota.Windows) != 2 {
		t.Fatalf("overview card = %+v", overview.Quota)
	}
}

func TestCodexShortLabelMapping(t *testing.T) {
	cases := []struct {
		id, name, want string
	}{
		{"codex-primary", "5-hour", "5h"},
		{"codex-secondary", "1-hour", "1h"},
		{"codex-secondary", "Weekly", "week"},
		{"codex-primary", "Primary", "Primary"},
	}
	for _, c := range cases {
		w := domain.QuotaWindow{ID: c.id, Name: c.name}
		if got := shortLabel(w); got != c.want {
			t.Errorf("shortLabel(%s/%s) = %q, want %q", c.id, c.name, got, c.want)
		}
	}

	// Headline preference: the codex primary window wins, its sibling comes
	// second, an unrelated window never displaces them.
	windows := []domain.QuotaWindow{
		{ID: "codex-secondary", Name: "1-hour"},
		{ID: "mcp-monthly", Name: "Monthly"},
		{ID: "codex-primary", Name: "5-hour"},
	}
	sel := selectHeadlineWindows(windows)
	if len(sel) != 2 || sel[0].ID != "codex-primary" || sel[1].ID != "codex-secondary" {
		t.Fatalf("headline selection = %+v", sel)
	}
}

// The MiniMax and Z.AI quota rendering must be byte-for-byte unaffected by
// the codex extension: no Account rows, no subtitle.
func TestQuotaKindsStayUntouched(t *testing.T) {
	snap := domain.QuotaSnapshot{
		Kind: domain.KindMiniMax,
		Windows: []domain.QuotaWindow{
			{ID: "interval", Name: "5-hour", UsedFraction: 0.31, RemainingFraction: 0.69},
		},
	}
	vm := BuildQuotaVM(snap, now, true)
	if len(vm.Account) != 0 {
		t.Fatalf("minimax account rows = %+v", vm.Account)
	}
	ov := BuildQuotaOverview(snap, now)
	if ov.Subtitle != "" {
		t.Fatalf("minimax subtitle = %q", ov.Subtitle)
	}
}
