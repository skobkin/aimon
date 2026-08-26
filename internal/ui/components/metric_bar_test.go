package components

import (
	"testing"

	"fyne.io/fyne/v2"
)

func TestPillGeometryCentersPillAndText(t *testing.T) {
	bar := fyne.NewSize(200, 20)
	text := fyne.NewSize(80, 12)

	pillPos, pillSize, textPos := pillGeometry(bar, text)

	// Padded pill: 80+2*6 wide, 12+2*2 high, centered with equal insets.
	if pillSize.Width != 92 || pillSize.Height != 16 {
		t.Errorf("pill size = %v, want 92x16", pillSize)
	}
	if pillPos.X != 54 || pillPos.Y != 2 {
		t.Errorf("pill pos = %v, want (54,2)", pillPos)
	}
	// Text centered in the same bar bounds, both axes.
	if textPos.X != 60 || textPos.Y != 4 {
		t.Errorf("text pos = %v, want (60,4)", textPos)
	}
}

func TestPillGeometryKeepsInsetForTallText(t *testing.T) {
	// 18+2*2 = 22 would overrun the 20-high bar; the clamp keeps a
	// 1-DIP inset top and bottom instead of a flush lower edge.
	bar := fyne.NewSize(200, 20)
	text := fyne.NewSize(80, 18)

	pillPos, pillSize, _ := pillGeometry(bar, text)

	if pillSize.Height != 18 {
		t.Errorf("pill height = %v, want 18 (20 minus two 1-DIP insets)", pillSize.Height)
	}
	if pillPos.Y != 1 {
		t.Errorf("pill y = %v, want 1", pillPos.Y)
	}
}

func TestPillGeometryCapsPillWidthToBar(t *testing.T) {
	bar := fyne.NewSize(100, 20)
	text := fyne.NewSize(120, 12)

	pillPos, pillSize, textPos := pillGeometry(bar, text)

	if pillSize.Width != 100 {
		t.Errorf("pill width = %v, want capped at bar width 100", pillSize.Width)
	}
	if pillPos.X != 0 {
		t.Errorf("pill x = %v, want 0", pillPos.X)
	}
	// The text stays centered in the bar and may overflow the capped pill.
	if textPos.X != -10 {
		t.Errorf("text x = %v, want -10", textPos.X)
	}
}
