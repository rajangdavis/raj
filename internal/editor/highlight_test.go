package editor

import (
	"testing"

	"raj/internal/ui"
)

// A nil set is the no-highlights fast path: reading it must not panic, must
// report nothing, and must not be live, because the renderer reads it without a
// nil check of its own.
func TestHighlightSetNilIsSafe(t *testing.T) {
	var s *HighlightSet
	if got := s.At(0); got != nil {
		t.Errorf("nil set At(0) = %+v, want nil", got)
	}
	if got := NewHighlightSet(nil, 0, 0); got != nil {
		t.Errorf("NewHighlightSet(nil) = %+v, want nil", got)
	}
	if s.Live(0, 0) {
		t.Error("a nil set reported live")
	}
}

// The pin is the whole safety property of a caret-dependent overlay: a set is
// live only for the text version and caret it was measured at. Without it a
// highlight would paint on moved bytes, which is an emphasis on the wrong word.
func TestHighlightSetLivePin(t *testing.T) {
	s := NewHighlightSet(map[int][]HighlightRun{0: {{Start: 0, End: 3}}}, 7, 42)
	if !s.Live(7, 42) {
		t.Error("the set is not live for its own version and caret")
	}
	if s.Live(8, 42) {
		t.Error("the set is live for a moved version")
	}
	if s.Live(7, 43) {
		t.Error("the set is live for a moved caret")
	}
}

// The emphasis layers over the token: the highlighted byte carries the
// highlight background and an unhighlighted byte does not. Without the
// composition the decoded answer would install and paint nothing.
func TestHighlightOverlayPaintsEmphasis(t *testing.T) {
	p := newTestPane("hello world\n")
	th := DefaultTheme()
	p.File.SetHighlight(NewHighlightSet(map[int][]HighlightRun{
		0: {{Start: 6, End: 11}},
	}, 0, 0))

	s := ui.NewScreen(40, 10)
	p.RenderFocused(s, 0, 0, 40, 10, th, true)

	gut := p.GutterWidth()
	if c := s.At(gut+6, 0); c.Rune != 'w' || c.Style.Bg != th.HighlightRead {
		t.Errorf("byte 6 = %q %+v, want 'w' on the read highlight %v", c.Rune, c.Style, th.HighlightRead)
	}
	if c := s.At(gut+0, 0); c.Style.Bg == th.HighlightRead {
		t.Error("an unhighlighted byte carries the read emphasis")
	}
	if c := s.At(gut+11, 0); c.Style.Bg == th.HighlightRead {
		t.Error("the emphasis painted past its end")
	}
}

// A write gets the write emphasis and a read gets the read one, so the
// distinction the server sent is visible rather than flattened into one mark.
func TestHighlightOverlayDistinguishesWrite(t *testing.T) {
	p := newTestPane("aa bb\n")
	th := DefaultTheme()
	p.File.SetHighlight(NewHighlightSet(map[int][]HighlightRun{
		0: {{Start: 0, End: 2}, {Start: 3, End: 5, Write: true}},
	}, 0, 0))

	s := ui.NewScreen(40, 10)
	p.RenderFocused(s, 0, 0, 40, 10, th, true)

	gut := p.GutterWidth()
	if bg := s.At(gut+0, 0).Style.Bg; bg != th.HighlightRead {
		t.Errorf("read byte bg = %v, want %v", bg, th.HighlightRead)
	}
	if bg := s.At(gut+3, 0).Style.Bg; bg != th.HighlightWrite {
		t.Errorf("write byte bg = %v, want %v", bg, th.HighlightWrite)
	}
}

// A set whose caret or version has moved must paint nothing, because a
// highlight for another caret is an emphasis on the wrong word. This is the
// drop on mismatch the render owns, and it is what makes a caret move
// invalidate the overlay without a clear between the move and the next frame.
func TestHighlightOverlayDropsWhenPinnedStateMoves(t *testing.T) {
	p := newTestPane("hello world\n")
	th := DefaultTheme()
	p.File.SetHighlight(NewHighlightSet(map[int][]HighlightRun{
		0: {{Start: 6, End: 11}},
	}, 0, 99)) // caret 99: not where the highlight was measured

	s := ui.NewScreen(40, 10)
	p.RenderFocused(s, 0, 0, 40, 10, th, true)
	if c := s.At(p.GutterWidth()+6, 0); c.Style.Bg == th.HighlightRead {
		t.Error("a highlight pinned to another caret painted")
	}
}
