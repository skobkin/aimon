package settings

import (
	"context"
	"math"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"git.skobk.in/skobkin/aimonitor/internal/config"
	"git.skobk.in/skobkin/aimonitor/internal/domain"
)

// stubDeps builds a Settings whose background operations are never run by
// layout tests.
func stubDeps() Deps {
	return Deps{
		LoadInstances:      func() []config.SourceInstance { return nil },
		SaveInstances:      func([]config.SourceInstance) error { return nil },
		CleanupCredentials: func(context.Context, config.SourceInstance) error { return nil },
		TestConnection:     func(context.Context, config.SourceInstance, string) error { return nil },
		DiscoverGPUs:       func(context.Context, string) ([]domain.GPUInfo, error) { return nil, nil },
		Done:               make(chan struct{}),
	}
}

// findButtons collects every button in the object tree by its text.
func findButtons(t *testing.T, obj fyne.CanvasObject, into map[string]*widget.Button) {
	t.Helper()
	switch o := obj.(type) {
	case *widget.Button:
		into[o.Text] = o
	case *fyne.Container:
		for _, child := range o.Objects {
			findButtons(t, child, into)
		}
	case fyne.Widget:
		for _, child := range test.TempWidgetRenderer(t, o).Objects() {
			findButtons(t, child, into)
		}
	}
}

// The editor's Save/Cancel row must stretch across the dialog as equal
// halves. Wrapping it in an HBox of an HScroll once collapsed the whole
// row to the scrollbar's minimum size, clipping both buttons.
func TestEditorButtonsShareDialogBottomRow(t *testing.T) {
	s := NewSettings(test.NewTempApp(t), stubDeps())
	s.win.Show()

	s.editDraft(domain.KindAMDGPUWeb)

	buttons := map[string]*widget.Button{}
	for _, overlay := range s.win.Canvas().Overlays().List() {
		findButtons(t, overlay, buttons)
	}
	cancel, save := buttons["Cancel"], buttons["Save"]
	if cancel == nil || save == nil {
		t.Fatalf("editor buttons missing from the dialog overlay; found %v", keys(buttons))
	}
	if cancel.Size().IsZero() || save.Size().IsZero() {
		t.Fatalf("buttons were never laid out: cancel=%v save=%v", cancel.Size(), save.Size())
	}
	if math.Abs(float64(cancel.Position().Y-save.Position().Y)) > 0.5 {
		t.Errorf("Save and Cancel must sit on one row: cancel at %v, save at %v",
			cancel.Position(), save.Position())
	}
	if w := save.Size().Width; w < 100 || math.Abs(float64(w-cancel.Size().Width)) > 1 {
		t.Errorf("Save and Cancel must share the row as equal halves; cancel=%v save=%v",
			cancel.Size(), save.Size())
	}
}

func keys(m map[string]*widget.Button) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
