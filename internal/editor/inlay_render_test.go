package editor

import (
	"testing"

	"raj/internal/ui"
)

// withHints installs a hint set on one line of a pane, replacing any earlier
// set. The fixtures are built the way the app builds them: line-relative
// offsets, padding flags, display widths.
func withHints(p *Pane, line int, hs ...Hint) {
	var s HintSet
	for _, h := range hs {
		s.Add(line, h)
	}
	p.File.SetHints(&s)
}

// screenOf renders the pane at full focus and returns the screen, so a test can
// read the cell a hint (or the caret) landed on.
func screenOf(p *Pane, cols, rows int) *ui.Screen {
	s := ui.NewScreen(cols, rows)
	p.RenderFocused(s, 0, 0, cols, rows, DefaultTheme(), true)
	return s
}

// A hint's width is its padding plus the display width of its text, never the
// byte length: "日本" is four columns, not six.
func TestHintWidthCountsPaddingAndDisplayColumns(t *testing.T) {
	cases := []struct {
		h    Hint
		want int
	}{
		{Hint{Text: "abc"}, 3},
		{Hint{Text: "abc", Left: true}, 4},
		{Hint{Text: "abc", Left: true, Right: true}, 5},
		{Hint{Text: "日本"}, 4},
		{Hint{Text: "", Left: true, Right: true}, 2},
	}
	for _, tc := range cases {
		if got := tc.h.Width(); got != tc.want {
			t.Errorf("Width(%+v) = %d, want %d", tc.h, got, tc.want)
		}
	}

	cols := HintCols([]Hint{{Off: 2, Text: "x", Right: true}})
	if len(cols) != 1 || cols[0].Off != 2 || cols[0].Width != 2 {
		t.Errorf("HintCols = %+v, want one hint at off 2 width 2", cols)
	}
	if HintCols(nil) != nil {
		t.Error("HintCols(nil) should be nil, the no-hints fast path")
	}
}

// File.LineCol and File.OffsetAt stay inverses with hints on a line, and a
// column that lands inside a hint resolves to its anchor rather than a byte.
func TestLineColOffsetAtRoundTripWithHints(t *testing.T) {
	p := newTestPane("abcdef\n")
	// One hint of width 4 at byte 3: it occupies columns 3..6, so byte 4 is
	// pushed from column 4 to column 8.
	withHints(p, 0, Hint{Off: 3, Text: "XY", Left: true, Right: true})

	for off := 0; off <= len("abcdef"); off++ {
		line, col := p.File.LineCol(off)
		if line != 0 {
			t.Fatalf("offset %d landed on line %d", off, line)
		}
		if got := p.File.OffsetAt(line, col); got != off {
			t.Errorf("offset %d -> col %d -> offset %d", off, col, got)
		}
	}
	// The hint is not counted at its own anchor: the caret sits before it.
	if _, col := p.File.LineCol(3); col != 3 {
		t.Errorf("column at the hint anchor = %d, want 3 (before the hint)", col)
	}
	if _, col := p.File.LineCol(4); col != 8 {
		t.Errorf("column past the hint = %d, want 8", col)
	}
	if got := p.File.OffsetAt(0, 5); got != 3 {
		t.Errorf("column inside the hint resolved to %d, want the anchor 3", got)
	}

	// With no hints the conversions are exactly the old ones.
	p.File.ClearHints()
	if _, col := p.File.LineCol(4); col != 4 {
		t.Errorf("unhinted column at offset 4 = %d, want 4", col)
	}
	if got := p.File.OffsetAt(0, 4); got != 4 {
		t.Errorf("unhinted offset at column 4 = %d, want 4", got)
	}
}

// The terminal caret follows the hint-aware column map: before a hint at its
// anchor, and past its whole run after it. It can never be placed inside one.
func TestCaretLandsAfterHint(t *testing.T) {
	p := newTestPane("abcdef\n")
	withHints(p, 0, Hint{Off: 0, Text: "xy", Left: true, Right: true}) // width 4
	gut := p.GutterWidth()

	// The anchor's caret sits before the hint.
	p.Cursors.Set(0, 0)
	s := screenOf(p, 40, 4)
	if !s.CursorShown {
		t.Fatal("caret hidden for a visible cursor")
	}
	if s.CursorX != gut || s.CursorY != 0 {
		t.Errorf("caret at %d,%d; want %d,0 (before the hint)", s.CursorX, s.CursorY, gut)
	}

	// Past the hint, the caret is shifted by its width rather than landing on
	// the character the hint displaced.
	p.Cursors.Set(4, 4)
	if _, col := p.File.LineCol(4); col != 8 {
		t.Errorf("column at offset 4 = %d, want 8 after the width-4 hint", col)
	}
	s = screenOf(p, 40, 4)
	if s.CursorX != gut+8 || s.CursorY != 0 {
		t.Errorf("caret at %d,%d; want %d,0 (after the hint)", s.CursorX, s.CursorY, gut+8)
	}
}

// A click inside a hint clamps to the hint's anchor; clicks on either side
// resolve to the bytes the hint-aware column map puts there.
func TestClickInsideHintClampsToAnchor(t *testing.T) {
	p := newTestPane("abcdef\n")
	// Width 4 at byte 2: the hint occupies columns 2..5 and 'c' starts at 6.
	withHints(p, 0, Hint{Off: 2, Text: "XY", Left: true, Right: true})

	for _, x := range []int{2, 3, 4, 5} {
		if got := p.OffsetAt(x, 0); got != 2 {
			t.Errorf("click at column %d = offset %d, want the anchor 2", x, got)
		}
	}
	if got := p.OffsetAt(1, 0); got != 1 {
		t.Errorf("click before the hint = offset %d, want 1", got)
	}
	if got := p.OffsetAt(6, 0); got != 2 {
		t.Errorf("click on the character after the hint = offset %d, want 2", got)
	}
	if got := p.OffsetAt(7, 0); got != 3 {
		t.Errorf("click one past it = offset %d, want 3", got)
	}
}

// An end-of-line hint is drawn past the last character, and a hint's cells are
// styled as hints whatever byte a selection covers at its anchor.
func TestHintAtEndOfLineAndSelectionStyling(t *testing.T) {
	th := DefaultTheme()

	// EOL: anchored at len, drawn after the text.
	p := newTestPane("abc\n")
	withHints(p, 0, Hint{Off: 3, Text: "->"})
	gut := p.GutterWidth()
	s := screenOf(p, 40, 4)
	for i, want := range []rune("->") {
		c := s.At(gut+3+i, 0)
		if c.Rune != want {
			t.Errorf("EOL hint cell %d = %q, want %q", i, c.Rune, want)
		}
		if c.Style != th.InlayHint {
			t.Errorf("EOL hint cell %d style = %+v, want the inlay style", i, c.Style)
		}
	}
	if c := s.At(gut+2, 0); c.Style == th.InlayHint {
		t.Error("the last document cell was painted with the hint style")
	}

	// Selection: the anchor's byte is selected, but the hint cell is not
	// painted as selection. The byte after the hint keeps its selection.
	p = newTestPane("abc\n")
	withHints(p, 0, Hint{Off: 1, Text: "X"})
	p.Cursors.Set(2, 0) // select bytes 0..2, covering the anchor byte 1
	s = screenOf(p, 40, 4)
	gut = p.GutterWidth()
	hintCell := s.At(gut+1, 0)
	if hintCell.Rune != 'X' || hintCell.Style != th.InlayHint {
		t.Errorf("hint cell = %q/%+v, want X with the inlay style", hintCell.Rune, hintCell.Style)
	}
	selCell := s.At(gut+2, 0)
	if selCell.Rune != 'b' || selCell.Style.Bg != th.Selection.Bg {
		t.Errorf("selected byte = %q/%+v, want b with the selection background", selCell.Rune, selCell.Style)
	}
}

// A wrapped pane draws a hint on a line that fits one row: the fallback keeps
// hints whenever the whole line, hints included, still fits, wrapped or not.
func TestWrappedPaneDrawsFittingHint(t *testing.T) {
	p := newTestPane("abc\n")
	withHints(p, 0, Hint{Off: 1, Text: "X"})
	p.Wrap = true

	s := screenOf(p, 40, 4)
	gut := p.GutterWidth()
	if c := s.At(gut+1, 0); c.Rune != 'X' || c.Style != DefaultTheme().InlayHint {
		t.Errorf("wrapped draw cell = %q/%+v, want X with the inlay style", c.Rune, c.Style)
	}
	// The column map counts the hint whether wrapping is on or off.
	if _, col := p.File.LineCol(2); col != 3 {
		t.Errorf("wrapped column at offset 2 = %d, want 3 after the width-1 hint", col)
	}
}

// A wrapped pane with no hints keeps the unhinted column conversions byte for
// byte: the hint path is additive, not a rewrite of the wrapped one.
func TestWrappedPaneWithoutHintsIsUnchanged(t *testing.T) {
	p := newTestPane("abc\n")
	p.Wrap = true
	if _, col := p.File.LineCol(2); col != 2 {
		t.Errorf("wrapped column at offset 2 = %d, want the unhinted 2", col)
	}
	if got := p.File.OffsetAt(0, 2); got != 2 {
		t.Errorf("wrapped offset at column 2 = %d, want the unhinted 2", got)
	}
}

// On a hinted single-row line with wrapping on the caret and a click agree:
// the caret never lands inside a hint, and a click inside one clamps to the
// hint anchor.
func TestWrappedHintCaretAndClickRoundTrip(t *testing.T) {
	p := newTestPane("abcdef\n")
	// Width 4 at byte 2: the hint occupies columns 2..5 and byte 2 is drawn at
	// column 6.
	withHints(p, 0, Hint{Off: 2, Text: "XY", Left: true, Right: true})
	p.Wrap = true
	gut := p.GutterWidth()

	// The caret at the anchor sits before the hint; one byte later it is past
	// the whole run.
	p.Cursors.Set(2, 2)
	p.FollowCursor()
	s := screenOf(p, 40, 4)
	if s.CursorX != gut+2 || s.CursorY != 0 {
		t.Errorf("caret at %d,%d, want %d,0 before the hint", s.CursorX, s.CursorY, gut+2)
	}
	p.Cursors.Set(3, 3)
	p.FollowCursor()
	s = screenOf(p, 40, 4)
	if s.CursorX != gut+7 || s.CursorY != 0 {
		t.Errorf("caret at %d,%d, want %d,0 after the hint", s.CursorX, s.CursorY, gut+7)
	}

	// A click inside the hint clamps to its anchor; clicks either side resolve
	// to the bytes the hint-aware map puts there.
	for _, x := range []int{2, 3, 4, 5} {
		if got := p.OffsetAt(x, 0); got != 2 {
			t.Errorf("click at column %d = offset %d, want the anchor 2", x, got)
		}
	}
	if got := p.OffsetAt(1, 0); got != 1 {
		t.Errorf("click before the hint = offset %d, want 1", got)
	}
	if got := p.OffsetAt(6, 0); got != 2 {
		t.Errorf("click on the character after the hint = offset %d, want 2", got)
	}
	if got := p.OffsetAt(7, 0); got != 3 {
		t.Errorf("click one past it = offset %d, want 3", got)
	}
}

// The find highlight keys on a byte like selection does, so a hint anchored
// inside a match still paints as a hint and does not take the find colour.
func TestFindOverAnchorDoesNotStyleHint(t *testing.T) {
	p := newTestPane("abc\n")
	withHints(p, 0, Hint{Off: 1, Text: "X"})
	p.Find.Open = true
	p.Find.term = "b"
	p.Find.matches = []int{1}
	p.Find.at = 0

	s := screenOf(p, 40, 4)
	gut := p.GutterWidth()
	if h := s.At(gut+1, 0); h.Rune != 'X' || h.Style != DefaultTheme().InlayHint {
		t.Errorf("hint cell = %q/%+v, want X with the inlay style", h.Rune, h.Style)
	}
	if m := s.At(gut+2, 0); m.Rune != 'b' || m.Style == DefaultTheme().InlayHint {
		t.Errorf("matched byte = %q/%+v, want b painted by find", m.Rune, m.Style)
	}
}

// A hint whose run does not fit across the row is suppressed rather than
// clipped: none of its cells are painted, and a line whose only hint does not
// fit simply shows none of it.
func TestHintNotFittingIsSuppressed(t *testing.T) {
	p := newTestPane("abc\n")
	withHints(p, 0, Hint{Off: 0, Text: "WIDE", Left: true, Right: true}) // width 6
	th := DefaultTheme()

	// A row too narrow for the hint: the hint is not painted, and its
	// columns still count, so the text after it is pushed off the row too.
	s := screenOf(p, 8, 3)
	gut := p.GutterWidth()
	for x := gut; x < 8; x++ {
		if c := s.At(x, 0); c.Style == th.InlayHint {
			t.Fatalf("a hint that does not fit was painted at x=%d: %q", x, c.Rune)
		}
	}

	// A row wide enough: the hint is painted, starting with its left pad.
	s = screenOf(p, 20, 3)
	if c := s.At(gut, 0); c.Rune != ' ' || c.Style != th.InlayHint {
		t.Errorf("fitted hint pad = %q/%+v, want a hint-styled space", c.Rune, c.Style)
	}
}
