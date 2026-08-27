package components

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/widget"
)

// SensitiveValue is a text label that conceals an account-level monetary
// value behind a fixed mask while the global privacy preference hides it,
// revealing the real value only while the pointer hovers it (S4 spec §10).
// It is presentation-only: masking never mutates or forgets the stored
// value, and when the preference is off the real text always shows.
//
// This is one of the narrowly approved custom widgets, alongside QuotaBar
// and MetricBar; it exists because no standard widget can mask and reveal
// on hover.
type SensitiveValue struct {
	widget.BaseWidget

	text     string      // the real value; never modified by masking
	hidden   func() bool // the global privacy predicate
	revealed bool        // pointer is currently over the widget
}

// maskText is the fixed concealment string. It is intentionally constant,
// and the widget reserves room for whichever of real/masked text is wider,
// so conceal↔reveal never reflows the row (which would move the row away
// under a stationary pointer and cause hover flicker).
const maskText = "$•••••"

// NewSensitiveValue builds one value label. hidden is the app-wide privacy
// predicate ("values are concealed"); nil means never hide.
func NewSensitiveValue(text string, hidden func() bool) *SensitiveValue {
	if hidden == nil {
		hidden = func() bool { return false }
	}
	v := &SensitiveValue{text: text, hidden: hidden}
	v.ExtendBaseWidget(v)
	return v
}

// SetText replaces the real value.
func (v *SensitiveValue) SetText(text string) {
	v.text = text
	v.Refresh()
}

// Text returns the real value (never the mask).
func (v *SensitiveValue) Text() string { return v.text }

// Masked reports whether the value currently displays as the mask.
func (v *SensitiveValue) Masked() bool {
	return v.hidden() && !v.revealed
}

// DisplayText returns the string currently rendered.
func (v *SensitiveValue) DisplayText() string {
	if v.Masked() {
		return maskText
	}
	return v.text
}

// MouseIn implements desktop.Hoverable: hovering reveals the real value.
func (v *SensitiveValue) MouseIn(*desktop.MouseEvent) {
	v.revealed = true
	v.Refresh()
}

// MouseOut implements desktop.Hoverable: leaving re-masks immediately.
func (v *SensitiveValue) MouseOut() {
	v.revealed = false
	v.Refresh()
}

// MouseMoved implements desktop.Hoverable; reveal state does not change
// while the pointer stays inside.
func (v *SensitiveValue) MouseMoved(*desktop.MouseEvent) {}

// CreateRenderer implements fyne.Widget. One text object renders
// whichever string applies; the stable geometry comes from measuring both
// strings in MinSize, not from holding both in the tree.
func (v *SensitiveValue) CreateRenderer() fyne.WidgetRenderer {
	t := neutralText(v.DisplayText())
	return &sensitiveValueRenderer{w: v, text: t}
}

type sensitiveValueRenderer struct {
	w    *SensitiveValue
	text *canvas.Text
}

func (r *sensitiveValueRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.text}
}

func (r *sensitiveValueRenderer) Destroy() {}

// MinSize covers both the real and the masked string so conceal/reveal
// keeps identical geometry — never narrower than either text.
func (r *sensitiveValueRenderer) MinSize() fyne.Size {
	real := fyne.MeasureText(r.w.text, r.text.TextSize, r.text.TextStyle)
	mask := fyne.MeasureText(maskText, r.text.TextSize, r.text.TextStyle)
	return real.Max(mask)
}

func (r *sensitiveValueRenderer) Layout(size fyne.Size) {
	r.text.Move(fyne.NewPos(0, 0))
	shown := r.text.MinSize()
	if shown.Width > size.Width {
		shown.Width = size.Width
	}
	if shown.Height > size.Height {
		shown.Height = size.Height
	}
	r.text.Resize(shown)
}

func (r *sensitiveValueRenderer) Refresh() {
	r.text.Text = r.w.DisplayText()
	r.text.Refresh()
	canvas.Refresh(r.w)
}
