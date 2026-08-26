package components

import (
	"testing"

	"fyne.io/fyne/v2"
)

func TestDonutTextBlockCentersBothLinesInHole(t *testing.T) {
	// 80 DIP donut with CutoutRatio 0.5: the hole is the inner half,
	// spanning y from 20 to 60 in the center column.
	size := fyne.NewSize(donutSize, donutSize)
	pct := fyne.NewSize(30, 19)
	lbl := fyne.NewSize(16, 15)

	pctPos, lblPos := donutTextBlock(size, pct, lbl)

	// The 19+2+15 = 36-high block is centered: top = (80-36)/2 = 22.
	if pctPos.Y != 22 {
		t.Errorf("pct y = %v, want 22", pctPos.Y)
	}
	if lblPos.Y != 43 { // 22 + 19 + gap 2
		t.Errorf("lbl y = %v, want 43", lblPos.Y)
	}
	// Both lines horizontally centered.
	if pctPos.X != 25 { // (80-30)/2
		t.Errorf("pct x = %v, want 25", pctPos.X)
	}
	if lblPos.X != 32 { // (80-16)/2
		t.Errorf("lbl x = %v, want 32", lblPos.X)
	}
	// Neither line reaches the ring.
	if pctPos.Y < 20 {
		t.Errorf("pct y = %v, enters the ring (hole starts at 20)", pctPos.Y)
	}
	if lblPos.Y+15 > 60 {
		t.Errorf("lbl bottom = %v, enters the ring (hole ends at 60)", lblPos.Y+15)
	}
}

func TestDonutTextBlockStaysCenteredWhenTight(t *testing.T) {
	// An oversized block (26+2+18 = 46 > 40 DIP hole) overflows the hole
	// symmetrically rather than snapping onto the ring.
	size := fyne.NewSize(donutSize, donutSize)
	pct := fyne.NewSize(40, 26)
	lbl := fyne.NewSize(20, 18)

	pctPos, lblPos := donutTextBlock(size, pct, lbl)

	want := float32(donutSize-46) / 2
	if pctPos.Y != want {
		t.Errorf("pct y = %v, want %v (block centered, not halves)", pctPos.Y, want)
	}
	if lblPos.Y != want+26+2 {
		t.Errorf("lbl y = %v, want %v", lblPos.Y, want+26+2)
	}
}
