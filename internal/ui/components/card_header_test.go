package components

import (
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"git.skobk.in/skobkin/aimonitor/internal/domain"
	"git.skobk.in/skobkin/aimonitor/internal/ui"
)

func countIcons(t *testing.T, obj fyne.CanvasObject) int {
	t.Helper()
	n := 0
	walkObjects(t, obj, func(o fyne.CanvasObject) {
		if _, ok := o.(*widget.Icon); ok {
			n++
		}
	})

	return n
}

// A remote kind gets its provider logo at the header's leading edge; a
// local kind renders the plain title/status header as before.
func TestSourceCardHeaderLogo(t *testing.T) {
	test.NewTempApp(t)

	cases := []struct {
		kind  domain.SourceKind
		icons int
	}{
		{domain.KindClaude, 1},
		{domain.KindCodex, 1},
		{domain.KindZAI, 1},
		{domain.KindLlamaSwap, 0},
		{domain.KindAMDGPUWeb, 0},
		{domain.KindHindsight, 0},
	}
	for _, tc := range cases {
		card, _ := NewSourceCard(ui.CardVM{Source: tc.kind, Title: "Source", Status: "5s ago"}, widget.NewLabel("body"))
		if got := countIcons(t, card); got != tc.icons {
			t.Errorf("%s: header icons = %d, want %d", tc.kind, got, tc.icons)
		}
	}
}

// The logo is a fixed square: it neither adds header height beyond the
// title's nor widens the card.
func TestSourceCardHeaderLogoDoesNotGrowTheCard(t *testing.T) {
	test.NewTempApp(t)

	plain, _ := NewSourceCard(ui.CardVM{Source: domain.KindLlamaSwap, Title: "Source", Status: "5s ago"}, widget.NewLabel("body"))
	logo, _ := NewSourceCard(ui.CardVM{Source: domain.KindZAI, Title: "Source", Status: "5s ago"}, widget.NewLabel("body"))

	if p, l := plain.MinSize(), logo.MinSize(); l.Height != p.Height {
		t.Errorf("logo header height = %v, plain = %v", l.Height, p.Height)
	}
}
