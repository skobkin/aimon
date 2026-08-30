package components

import (
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"git.skobk.in/skobkin/aimonitor/internal/ui"
)

// incompatibleCLIFooter is the real-world failure footer behind the report
// of a monitor window that grew to fit one line: a whole sentence of
// authored guidance, not a short status word.
const incompatibleCLIFooter = "Last refresh failed: This Codex CLI does not support the app-server rate-limit interface. Update Codex CLI or choose a compatible executable."

// singleLineWidth measures the width a label demands when it does not wrap.
func singleLineWidth(t *testing.T, text string, style fyne.TextStyle) float32 {
	t.Helper()

	return widget.NewLabelWithStyle(text, fyne.TextAlignLeading, style).MinSize().Width
}

// labelByText returns the first label in the tree carrying exactly text.
func labelByText(t *testing.T, obj fyne.CanvasObject, text string) *widget.Label {
	t.Helper()
	var found *widget.Label
	walkObjects(t, obj, func(o fyne.CanvasObject) {
		if l, ok := o.(*widget.Label); ok && l.Text == text && found == nil {
			found = l
		}
	})

	return found
}

// assertWraps checks that a free-text label wraps and that its container
// therefore stays well below the text's single-line width.
func assertWraps(t *testing.T, l *widget.Label, text string, parent fyne.CanvasObject) {
	t.Helper()
	if l == nil {
		t.Fatalf("label %q not found", text)
	}
	if l.Wrapping != fyne.TextWrapWord {
		t.Errorf("label %q wrapping = %v, want word wrap", text, l.Wrapping)
	}
	if want := singleLineWidth(t, text, l.TextStyle); parent.MinSize().Width >= want {
		t.Errorf("container min width %.0f reaches %q's single-line width %.0f; the text must wrap instead",
			parent.MinSize().Width, text, want)
	}
}

// A card footer carries a refresh failure of unbounded length. It must wrap:
// the tab bodies sit in a vertical scroll container whose minimum width is
// its content's, so one unwrapped sentence widens the whole monitor window.
func TestSourceCardFooterWrapsInsteadOfWideningTheCard(t *testing.T) {
	test.NewTempApp(t)

	card, _ := NewSourceCard(ui.CardVM{
		Title: "Codex", Status: "1m ago · stale", Footer: incompatibleCLIFooter,
	}, container.NewVBox())

	assertWraps(t, labelByText(t, card, incompatibleCLIFooter), incompatibleCLIFooter, card)
}

// The header keeps the card grammar "Source name …… 5s ago" on one row. Its
// labels must not wrap: an HBox hands out minimum widths, so a wrapping
// header label would collapse into a sliver.
func TestSourceCardHeaderStaysOnOneRow(t *testing.T) {
	test.NewTempApp(t)

	card, _ := NewSourceCard(ui.CardVM{Title: "Codex", Status: "46s ago"}, container.NewVBox())

	for _, text := range []string{"Codex", "46s ago"} {
		l := labelByText(t, card, text)
		if l == nil {
			t.Fatalf("header label %q not found", text)
		}
		if l.Wrapping != fyne.TextWrapOff {
			t.Errorf("header label %q wraps; the header row would collapse it", text)
		}
		if w := singleLineWidth(t, text, l.TextStyle); l.MinSize().Width < w {
			t.Errorf("header label %q min width %.0f is below its text width %.0f", text, l.MinSize().Width, w)
		}
	}
}

// Other free-text lines that render at their container's full width — the
// service status summary, the running-model names, the quota raw-count line
// — wrap for the same reason a footer does.
func TestFreeTextLinesWrapInsteadOfWideningTheirCard(t *testing.T) {
	test.NewTempApp(t)

	status := "Degraded: the database pool is exhausted and consolidations are backing up"
	service := NewServiceOverviewBody(ui.ServiceOverviewVM{
		HasData: true,
		Summary: status,
		Rows:    []ui.MetricRowVM{{Label: "Banks", Value: "27"}},
	})
	assertWraps(t, labelByText(t, service, status), status, service)

	model := "hf.co/Qwen/Qwen2.5-72B-Instruct-GGUF:q4_k_m_xl_a_b_c"
	runtime := NewRuntimeOverviewBody(ui.RuntimeOverviewVM{
		HasData: true,
		Summary: "1 model running",
		Names:   []string{model},
	})
	assertWraps(t, labelByText(t, runtime, model), model, runtime)

	secondary := "12,345,678 / 100,000,000 tokens of the monthly plan allowance"
	row, _ := NewQuotaRow(ui.QuotaWindowVM{
		Name: "Monthly", UsedText: "12% used", Used: 0.12, Remaining: 0.88, Secondary: secondary,
	})
	assertWraps(t, labelByText(t, row, secondary), secondary, row)
}
