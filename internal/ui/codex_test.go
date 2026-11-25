package ui

import (
	"testing"
	"time"

	"git.skobk.in/skobkin/aimonitor/internal/config"
	"git.skobk.in/skobkin/aimonitor/internal/domain"
	"git.skobk.in/skobkin/aimonitor/internal/pace"
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

func codexInstance() config.SourceInstance {
	i := inst("codx1", "ChatGPT Codex", domain.KindCodex, true)
	i.Codex = &config.CodexSettings{}

	return i
}

func TestBuildCodexVMWindows(t *testing.T) {
	vm := BuildCodexVM(codexSnap(), pace.InstancePaces{}, now, false)
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
	vm := BuildCodexVM(codexSnap(), pace.InstancePaces{}, now, true)
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

func TestBuildCodexVMResetCreditExpiry(t *testing.T) {
	cases := []struct {
		name string
		at   time.Time
		want string
	}{
		{"same year", now.Add(36 * time.Hour), "3 · next expires " + now.Add(36*time.Hour).Format("2 Jan")},
		{"other year", now.AddDate(1, 0, 0), "3 · next expires " + now.AddDate(1, 0, 0).Format("2 Jan 2006")},
	}
	for _, tc := range cases {
		snap := codexSnap()
		at := tc.at
		snap.ResetCreditsNextExpiry = &at
		vm := BuildCodexVM(snap, pace.InstancePaces{}, now, true)
		if got := vm.Account[1].Value; got != tc.want {
			t.Errorf("%s: credits row = %q, want %q", tc.name, got, tc.want)
		}
	}

	// An expiry without a count adds no row: the count is the anchor.
	snap := codexSnap()
	snap.ResetCreditsAvailable = nil
	at := now.Add(time.Hour)
	snap.ResetCreditsNextExpiry = &at
	if vm := BuildCodexVM(snap, pace.InstancePaces{}, now, true); len(vm.Account) != 1 {
		t.Errorf("rows = %+v, want only Plan", vm.Account)
	}
}

func TestBuildCodexVMAbsentOptionalFieldsOmitted(t *testing.T) {
	snap := codexSnap()
	snap.PlanType = ""
	snap.ResetCreditsAvailable = nil
	snap.RateLimitReachedType = ""
	vm := BuildCodexVM(snap, pace.InstancePaces{}, now, true)
	if len(vm.Account) != 0 {
		t.Fatalf("absent metadata produced rows: %+v", vm.Account)
	}

	snap.RateLimitReachedType = "primary"
	vm = BuildCodexVM(snap, pace.InstancePaces{}, now, true)
	if len(vm.Account) != 1 {
		t.Fatalf("reached rows = %+v, want one", vm.Account)
	}
	if vm.Account[0].Label != "Rate limit" || vm.Account[0].Level != MetricWarning {
		t.Errorf("reached row = %+v, want a warning-level status", vm.Account[0])
	}

	// Unknown plan names pass through unchanged and never as a fake one.
	snap.PlanType = "team"
	vm = BuildCodexVM(snap, pace.InstancePaces{}, now, true)
	if vm.Account[0].Value != "team" {
		t.Errorf("plan row = %+v, want the raw name", vm.Account[0])
	}
}

func TestBuildCodexOverview(t *testing.T) {
	vm := BuildCodexOverview(codexSnap(), pace.InstancePaces{}, now)
	if !vm.HasData || len(vm.Windows) != 2 {
		t.Fatalf("overview = %+v, want two donuts", vm)
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
	vm = BuildCodexOverview(single, pace.InstancePaces{}, now)
	if len(vm.Windows) != 1 {
		t.Fatalf("single-window overview = %+v", vm)
	}
}

func TestBuildCodexCardsDispatch(t *testing.T) {
	rt := domain.Runtime{Enabled: true, LastSuccessAt: now, Snapshot: codexSnap()}
	card := BuildCard(codexInstance(), rt, pace.InstancePaces{}, now, true)
	if card.Kind != KindQuota {
		t.Fatalf("card kind = %q, want %q", card.Kind, KindQuota)
	}
	if len(card.Quota.Windows) != 2 || len(card.Quota.Account) == 0 {
		t.Fatalf("detail card = %+v", card.Quota)
	}

	overview := BuildOverviewCard(codexInstance(), rt, pace.InstancePaces{}, now)
	if overview.Kind != OverviewQuota {
		t.Fatalf("overview kind = %q, want %q", overview.Kind, OverviewQuota)
	}
	if overview.Title != "ChatGPT Codex (Plus)" {
		t.Errorf("overview title = %q, want the plan joined to the source name", overview.Title)
	}
	if len(overview.Quota.Windows) != 2 {
		t.Fatalf("overview card = %+v", overview.Quota)
	}
}

// The plan name lives in the overview card title, never as a line of its
// own: it is appended once a snapshot lands, and an absent or unknown plan
// leaves the title untouched.
func TestCodexOverviewTitleCarriesPlan(t *testing.T) {
	build := func(snap domain.CodexSnapshot) OverviewCardVM {
		return BuildOverviewCard(codexInstance(),
			domain.Runtime{Enabled: true, LastSuccessAt: now, Snapshot: snap}, pace.InstancePaces{}, now)
	}

	if got := build(codexSnap()).Title; got != "ChatGPT Codex (Plus)" {
		t.Errorf("title = %q, want \"ChatGPT Codex (Plus)\"", got)
	}

	noPlan := codexSnap()
	noPlan.PlanType = ""
	if got := build(noPlan).Title; got != "ChatGPT Codex" {
		t.Errorf("title with no plan = %q, want the bare source name", got)
	}

	unknown := codexSnap()
	unknown.PlanType = "team"
	if got := build(unknown).Title; got != "ChatGPT Codex (team)" {
		t.Errorf("title with unknown plan = %q, want the raw plan name appended", got)
	}

	// Before the first snapshot there is no plan to show.
	if got := BuildOverviewCard(codexInstance(),
		domain.Runtime{Enabled: true, LastSuccessAt: now}, pace.InstancePaces{}, now).Title; got != "ChatGPT Codex" {
		t.Errorf("title before first snapshot = %q, want the bare source name", got)
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
// the codex extension: no Account rows.
func TestQuotaKindsStayUntouched(t *testing.T) {
	snap := domain.QuotaSnapshot{
		Kind: domain.KindMiniMax,
		Windows: []domain.QuotaWindow{
			{ID: "interval", Name: "5-hour", UsedFraction: 0.31, RemainingFraction: 0.69},
		},
	}
	vm := BuildQuotaVM(snap, pace.InstancePaces{}, now, true)
	if len(vm.Account) != 0 {
		t.Fatalf("minimax account rows = %+v", vm.Account)
	}
}

func TestQuotaSharesRows(t *testing.T) {
	snap := domain.QuotaSnapshot{
		Kind: domain.KindClaude,
		Plan: "Max",
		Shares: []domain.QuotaShare{
			{Name: "Claude Code", PercentOfLimit: 34.4},
			{Name: "Chats", PercentOfLimit: 0.3},
			{Name: "Idle", PercentOfLimit: 0},
		},
	}
	vm := BuildQuotaVM(snap, pace.InstancePaces{}, time.Now(), true)
	want := []MetricRowVM{
		{Label: "Plan", Value: "Max"},
		{Label: "Claude Code", Value: "34% of weekly limit"},
		{Label: "Chats", Value: "<1% of weekly limit"},
		{Label: "Idle", Value: "0% of weekly limit"},
	}
	if len(vm.Account) != len(want) {
		t.Fatalf("account rows = %+v, want %+v", vm.Account, want)
	}
	for i, w := range want {
		if vm.Account[i] != w {
			t.Errorf("row %d = %+v, want %+v", i, vm.Account[i], w)
		}
	}

	// The Overview never shows the split.
	if ov := BuildQuotaVM(snap, pace.InstancePaces{}, time.Now(), false); len(ov.Account) != 0 {
		t.Errorf("non-detail account rows = %+v, want none", ov.Account)
	}
}

func TestExtraUsageRow(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	cases := []struct {
		name string
		x    domain.QuotaExtraUsage
		want string
	}{
		{"off", domain.QuotaExtraUsage{}, "Off"},
		{"limit+util", domain.QuotaExtraUsage{Enabled: true, Used: f(3.2), Limit: f(50), Utilization: f(6.4), Currency: "USD"}, "$3.20 / $50.00 (6%)"},
		{"derived pct", domain.QuotaExtraUsage{Enabled: true, Used: f(5), Limit: f(50)}, "$5.00 / $50.00 (10%)"},
		{"no limit", domain.QuotaExtraUsage{Enabled: true, Used: f(2.5), Currency: "EUR"}, "2.50 EUR"},
		{"eur limit", domain.QuotaExtraUsage{Enabled: true, Used: f(2.5), Limit: f(20), Currency: "EUR"}, "2.50 / 20.00 EUR (13%)"},
		{"on only", domain.QuotaExtraUsage{Enabled: true}, "On"},
	}
	for _, c := range cases {
		row := extraUsageRow(c.x)
		if row.Text != c.want || !row.Sensitive || row.Label != "Extra usage" {
			t.Errorf("%s: %+v, want text %q", c.name, row, c.want)
		}
	}
}
