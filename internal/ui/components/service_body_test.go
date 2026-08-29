package components

import (
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/widget"

	"git.skobk.in/skobkin/aimonitor/internal/ui"
)

func serviceTestSnap(now time.Time) ui.ServiceVM {
	write := now.Add(-3 * time.Minute)

	return ui.ServiceVM{
		HasData: true,
		Service: []ui.MetricRowVM{
			{Label: "Status", Value: "Healthy", Level: ui.MetricGood},
			{Label: "Version", Value: "0.9.2"},
		},
		Memory: []ui.AgeRowVM{
			{Label: "Banks", Value: "27"},
			{Label: "Last write", Value: "3m ago", At: &write},
		},
		Activity: []ui.MetricRowVM{{Label: "Recall", Value: "18/m"}},
		Runtime:  []ui.MetricRowVM{{Label: "DB waiting", Value: "0"}},
		Queue:    nil,
	}
}

// collectTexts gathers every rendered text (labels and canvas text) under
// an object.
func collectTexts(t *testing.T, obj fyne.CanvasObject) []string {
	t.Helper()
	var out []string
	walkObjects(t, obj, func(o fyne.CanvasObject) {
		switch txt := o.(type) {
		case *widget.Label:
			out = append(out, txt.Text)
		case *canvas.Text:
			out = append(out, txt.Text)
		}
	})

	return out
}

func hasSection(t *testing.T, obj fyne.CanvasObject, title string) bool {
	t.Helper()
	found := false
	walkObjects(t, obj, func(o fyne.CanvasObject) {
		if l, ok := o.(*widget.Label); ok && l.Text == title {
			if l.TextStyle.Bold {
				found = true
			}
		}
	})

	return found
}

// The detail body renders the sections the VM carries and omits the rest;
// the unavailable memory rows never tick.
func TestServiceDetailBodySections(t *testing.T) {
	now := time.Unix(1770000000, 0)
	body, ticks := NewServiceDetailBody(serviceTestSnap(now), now)

	for _, want := range []string{"Service", "Memory", "Activity", "Runtime"} {
		if !hasSection(t, body, want) {
			t.Errorf("section %q missing", want)
		}
	}
	if hasSection(t, body, "Queue") {
		t.Error("an absent backlog must not render a Queue section")
	}

	texts := collectTexts(t, body)
	for _, want := range []string{"Healthy", "0.9.2", "Banks", "27", "Last write", "3m ago", "Recall", "18/m", "DB waiting", "0"} {
		if !contains(texts, want) {
			t.Errorf("text %q missing from the body", want)
		}
	}

	// Only the rows with an absolute instant return a refresher.
	if len(ticks) != 1 {
		t.Fatalf("ticks = %d, want 1 (only Last write carries an instant)", len(ticks))
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}

	return false
}

// The age refresher repaints the same value object in place as the clock
// advances; the row must not be rebuilt and counts must not change.
func TestServiceAgeRowsTickInPlace(t *testing.T) {
	now := time.Unix(1770000000, 0)
	body, ticks := NewServiceDetailBody(serviceTestSnap(now), now)
	if len(ticks) != 1 {
		t.Fatalf("ticks = %d, want 1", len(ticks))
	}

	var ageText *canvas.Text
	walkObjects(t, body, func(o fyne.CanvasObject) {
		if txt, ok := o.(*canvas.Text); ok && txt.Text == "3m ago" {
			ageText = txt
		}
	})
	if ageText == nil {
		t.Fatal("initial age text not found")
	}

	ticks[0](now.Add(time.Minute))
	if ageText.Text != "4m ago" {
		t.Errorf("age text = %q, want 4m ago after the tick", ageText.Text)
	}

	// A tick that does not change the text must not force a repaint.
	ticks[0](now.Add(time.Minute))
	if ageText.Text != "4m ago" {
		t.Errorf("age text = %q, want it stable", ageText.Text)
	}
}

// Without data the bodies degrade to the placeholder, never a half-empty
// grid.
func TestServiceBodiesWithoutData(t *testing.T) {
	body, ticks := NewServiceDetailBody(ui.ServiceVM{}, time.Unix(1770000000, 0))
	if !containsLabelText(t, body, "No data yet") {
		t.Error("detail body without data must show the placeholder")
	}
	if ticks != nil {
		t.Error("a placeholder body must return no tick refreshers")
	}
	if got := NewServiceOverviewBody(ui.ServiceOverviewVM{}); !containsLabelText(t, got, "No data yet") {
		t.Error("overview body without data must show the placeholder")
	}
}

// The overview body shows the bold summary and lays the pairs out two per
// row.
func TestServiceOverviewBody(t *testing.T) {
	vm := ui.ServiceOverviewVM{
		HasData: true,
		Summary: "Healthy · v0.9.2",
		Rows: []ui.MetricRowVM{
			{Label: "Banks", Value: "27"},
			{Label: "Facts", Value: "18.4k"},
			{Label: "Recall", Value: "18/m"},
			{Label: "LLM calls", Value: "—"},
			{Label: "DB active", Value: "2 / 10"},
		},
	}
	body := NewServiceOverviewBody(vm)

	texts := collectTexts(t, body)
	for _, want := range []string{"Healthy · v0.9.2", "Banks", "27", "Facts", "18.4k", "Recall", "18/m", "LLM calls", "—", "DB active", "2 / 10"} {
		if !contains(texts, want) {
			t.Errorf("text %q missing from the overview body", want)
		}
	}

	// An odd pair count must not panic or drop the last row.
	if !contains(texts, "2 / 10") {
		t.Error("trailing unpaired row was dropped")
	}
}
