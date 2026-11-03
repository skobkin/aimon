// Rich-text helpers for the pace-adjacent labels: a plain widget.Label
// cannot color part of its text, so the quota row titles, the Overview
// countdown lines and the pace captions are widget.RichText built from
// explicit TextSegments. The helpers keep the established label
// discipline — compare the rendered text and repaint only on change
// (S7_IMPLEMENTATION_SPEC §8).
package components

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"git.skobk.in/skobkin/aimonitor/internal/ui"
)

// textSeg builds an inline text segment with the given style.
func textSeg(text string, style widget.RichTextStyle) *widget.TextSegment {
	return &widget.TextSegment{Text: text, Style: style}
}

// plainSeg builds an inline segment in the theme foreground color.
func plainSeg(text string, align fyne.TextAlign, ts fyne.TextStyle) *widget.TextSegment {
	return textSeg(text, widget.RichTextStyle{Alignment: align, TextStyle: ts, Inline: true})
}

// errorSeg builds an inline segment in the theme error color — the
// burn-out estimate and the over-100% projection text. Color is never
// the only signal: the "≈" marker and the wording stay plain text.
func errorSeg(text string, align fyne.TextAlign, ts fyne.TextStyle) *widget.TextSegment {
	return textSeg(text, widget.RichTextStyle{Alignment: align, TextStyle: ts, ColorName: theme.ColorNameError, Inline: true})
}

// projectionSeg renders the projection caption segment ("≈137% at
// reset") in the error color when the raw projection exceeds 100% — the
// text counterpart of the ProjectionBar's overflow segment.
func projectionSeg(pace *ui.PaceVM, align fyne.TextAlign) *widget.TextSegment {
	if pace.Projected > 1.0 {
		return errorSeg(pace.ProjectedText, align, fyne.TextStyle{})
	}

	return plainSeg(pace.ProjectedText, align, fyne.TextStyle{})
}

// richTextPlain concatenates a rich-text label's rendered text — what
// the compare gates read.
func richTextPlain(rt *widget.RichText) string {
	return segmentsPlain(rt.Segments)
}

// segmentsPlain concatenates the rendered text of candidate segments.
func segmentsPlain(segs []widget.RichTextSegment) string {
	s := ""
	for _, seg := range segs {
		s += seg.Textual()
	}

	return s
}

// setRichText writes segments into a rich-text label. A same-shaped
// update mutates the existing text segments in place — RichText caches
// its canvas objects per segment pointer, so keeping the pointers keeps
// the object tree identical (the discipline the per-second tick lives
// by). A structural change replaces the segments. Either way the refresh
// is skipped when the rendered text is unchanged, so a tick never
// repaints a line that reads the same.
func setRichText(rt *widget.RichText, segs []widget.RichTextSegment) {
	if richTextPlain(rt) == segmentsPlain(segs) {
		return
	}
	if richTextSameShape(rt.Segments, segs) {
		for i, seg := range segs {
			rt.Segments[i].(*widget.TextSegment).Text = seg.(*widget.TextSegment).Text
		}
	} else {
		rt.Segments = segs
	}
	rt.Refresh()
}

// richTextSameShape reports whether new text segments can update old ones
// in place: same count, all text segments, same styles (RichTextStyle is
// comparable — alignment, color name and text style decide identity).
func richTextSameShape(old, new []widget.RichTextSegment) bool {
	if len(old) != len(new) {
		return false
	}
	for i := range new {
		o, okOld := old[i].(*widget.TextSegment)
		n, okNew := new[i].(*widget.TextSegment)
		if !okOld || !okNew || o.Style != n.Style {
			return false
		}
	}

	return true
}
