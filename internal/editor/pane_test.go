package editor

import (
	"raj/internal/ui"
	"strings"
	"testing"

	"raj/internal/piecetable"
)

func newTestPane(content string) *Pane {
	p := NewPane(NewFile("t.go", content, 2))
	p.Resize(40, 10)
	return p
}

// Agent-written text must render with a tint; user text must not.
func TestRenderTintsAgentText(t *testing.T) {
	p := newTestPane("user text here")
	p.File.ApplyDiff(piecetable.Agent, p.File.Session().Version(),
		[]piecetable.Hunk{{Start: 5, End: 9, Text: "AGNT"}})

	s := ui.NewScreen(40, 10)
	th := DefaultTheme()
	p.Render(s, 0, 0, 40, 10, th)

	gut := p.GutterWidth()
	if bg := s.At(gut+5, 0).Style.Bg; bg != th.AgentTint {
		t.Errorf("agent text bg = %v, want tint %v", bg, th.AgentTint)
	}
	if bg := s.At(gut+0, 0).Style.Bg; bg == th.AgentTint {
		t.Error("user text should not be tinted")
	}
}

// Deleting across a multi-cursor set must not apply an edit twice at one spot.
func TestMultiCursorDeleteDoesNotDoubleApply(t *testing.T) {
	p := newTestPane("ab\nab\nab")
	p.AddCursorVertical(1)
	p.AddCursorVertical(1)
	p.CharRight(false)
	p.DeleteBackward()
	if got := p.File.Text(); got != "b\nb\nb" {
		t.Errorf("text = %q, want b\\nb\\nb", got)
	}
}

// Cursors that collide must merge, or every later keystroke fires twice.
func TestCollidingCursorsMerge(t *testing.T) {
	p := newTestPane("xy")
	p.Cursors.Set(0, 0)
	p.Cursors.Add(1, 1)
	p.CharRight(false)
	p.CharRight(false)
	if n := p.Cursors.Count(); n != 1 {
		t.Errorf("cursors = %d, want 1 after collision", n)
	}
}

// Indenting a selection spanning lines indents every line it touches, once.
func TestIndentSelection(t *testing.T) {
	p := newTestPane("a\nb\nc")
	p.Cursors.Set(3, 0) // through the start of line 3
	p.Indent()
	if got := p.File.Text(); got != "  a\n  b\nc" {
		t.Errorf("text = %q", got)
	}
}

// Rendering a wide-character line must not overflow the pane.
func TestRenderWideRunesStayInBounds(t *testing.T) {
	p := newTestPane(strings.Repeat("日", 60))
	s := ui.NewScreen(40, 4)
	p.Render(s, 0, 0, 40, 4, DefaultTheme())
	if got := len([]rune(s.Row(0))); got > 40 {
		t.Errorf("row rendered %d runes into a 40-column screen", got)
	}
}

// The line index must stay correct after an agent diff, which mutates the
// buffer without going through Insert or Delete.
func TestIndexTracksAgentDiff(t *testing.T) {
	p := newTestPane("one\ntwo\nthree")
	p.File.ApplyDiff(piecetable.Agent, p.File.Session().Version(),
		[]piecetable.Hunk{{Start: 4, End: 7, Text: "a\nb\nc"}})
	want := "one\na\nb\nc\nthree"
	if got := p.File.Text(); got != want {
		t.Fatalf("text = %q", got)
	}
	if got := p.File.Lines(); got != 5 {
		t.Errorf("lines = %d, want 5", got)
	}
	if got := p.File.Line(2); got != "b" {
		t.Errorf("line 2 = %q, want b", got)
	}
}

// The editor shows the terminal caret only when it has focus. A pane still
// showing a caret after focus moved to the sidebar looks like the one receiving
// keystrokes.
func TestCaretShownOnlyWhenFocused(t *testing.T) {
	p := newTestPane("hello world")
	shown := func(focused bool) bool {
		s := ui.NewScreen(40, 4)
		p.RenderFocused(s, 0, 0, 40, 4, DefaultTheme(), focused)
		return s.CursorShown
	}
	if !shown(true) {
		t.Error("focused pane should show the caret")
	}
	if shown(false) {
		t.Error("unfocused pane should hide the caret")
	}
}

// The caret sits at the cursor's line and column, offset by the gutter.
func TestCaretPosition(t *testing.T) {
	p := newTestPane("abc\ndefgh")
	p.Cursors.Set(7, 7) // line 1, column 3
	s := ui.NewScreen(40, 4)
	p.RenderFocused(s, 0, 0, 40, 4, DefaultTheme(), true)
	if !s.CursorShown {
		t.Fatal("caret hidden")
	}
	if s.CursorY != 1 || s.CursorX != p.GutterWidth()+3 {
		t.Errorf("caret at %d,%d, want %d,1", s.CursorX, s.CursorY, p.GutterWidth()+3)
	}
}

// A cursor scrolled out of view hides the caret rather than pinning it to an
// edge, which would point at a line the user is not on.
func TestCaretHiddenWhenScrolledAway(t *testing.T) {
	p := newTestPane("a\nb\nc\nd\ne\nf\ng\nh")
	p.Cursors.Set(0, 0)
	p.Viewport.Top = 5
	s := ui.NewScreen(40, 3)
	p.RenderFocused(s, 0, 0, 40, 3, DefaultTheme(), true)
	if s.CursorShown {
		t.Error("caret shown for a cursor above the viewport")
	}
}

// Secondary cursors still draw as cells: a terminal has only one caret.
func TestSecondaryCursorsDrawAsCells(t *testing.T) {
	p := newTestPane("one\ntwo\nthree")
	p.AddCursorVertical(1)
	s := ui.NewScreen(40, 4)
	p.RenderFocused(s, 0, 0, 40, 4, DefaultTheme(), true)
	n := 0
	for y := 0; y < 4; y++ {
		for x := 0; x < 40; x++ {
			if s.At(x, y).Style.Bg == DefaultTheme().Caret {
				n++
			}
		}
	}
	if n == 0 {
		t.Error("no secondary cursor drawn")
	}
}

// cmd+l takes the whole line including its newline, and repeating it takes the
// next line — the property that makes it worth having over shift+down.
func TestSelectLineExpandsByOneLineEachPress(t *testing.T) {
	p := newTestPane("aaa\nbbb\nccc\n")
	p.Cursors.Set(5, 5) // middle of "bbb"

	want := [][2]int{{4, 8}, {4, 12}, {4, 12}} // third press has nowhere to go
	for i, w := range want {
		p.SelectLine()
		c := p.Cursors.Primary()
		if lo, hi := c.Range(); lo != w[0] || hi != w[1] {
			t.Fatalf("press %d: selection = (%d,%d), want (%d,%d)", i+1, lo, hi, w[0], w[1])
		}
		if c.Head != w[1] {
			t.Errorf("press %d: head = %d, want the end at %d", i+1, c.Head, w[1])
		}
	}
}

// A cursor already at column zero must still select its own line, not the one
// before it: the "already on a boundary" case is a press, not a repeat.
func TestSelectLineAtColumnZero(t *testing.T) {
	p := newTestPane("aaa\nbbb\nccc\n")
	p.Cursors.Set(4, 4)
	p.SelectLine()
	if lo, hi := p.Cursors.Primary().Range(); lo != 4 || hi != 8 {
		t.Errorf("selection = (%d,%d), want (4,8)", lo, hi)
	}
}

// The last line of a file with no trailing newline ends at Len, and pressing
// again must not run off the end.
func TestSelectLineLastLineWithoutNewline(t *testing.T) {
	p := newTestPane("aaa\nbbb")
	p.Cursors.Set(6, 6)
	p.SelectLine()
	p.SelectLine()
	if lo, hi := p.Cursors.Primary().Range(); lo != 4 || hi != p.File.Len() {
		t.Errorf("selection = (%d,%d), want (4,%d)", lo, hi, p.File.Len())
	}
}

// Every cursor expands, and cursors that grow into each other merge rather than
// leaving two overlapping selections of the same text.
func TestSelectLineMultiCursor(t *testing.T) {
	p := newTestPane("aaa\nbbb\nccc\nddd\n")
	p.Cursors.Set(1, 1)
	p.Cursors.Add(9, 9) // "ccc"
	p.SelectLine()
	if got := p.Cursors.Count(); got != 2 {
		t.Fatalf("cursor count = %d, want 2", got)
	}
	for i, want := range [][2]int{{0, 4}, {8, 12}} {
		if lo, hi := p.Cursors.All()[i].Range(); lo != want[0] || hi != want[1] {
			t.Errorf("cursor %d = (%d,%d), want (%d,%d)", i, lo, hi, want[0], want[1])
		}
	}

	// Adjacent lines: the two selections meet and must collapse to one.
	p = newTestPane("aaa\nbbb\nccc\n")
	p.Cursors.Set(1, 1)
	p.Cursors.Add(5, 5)
	p.SelectLine()
	if got := p.Cursors.Count(); got != 1 {
		t.Errorf("adjacent lines: cursor count = %d, want 1 after merge", got)
	}
	if lo, hi := p.Cursors.Primary().Range(); lo != 0 || hi != 8 {
		t.Errorf("merged selection = (%d,%d), want (0,8)", lo, hi)
	}
}

// cmd+l then cmd+shift+l is the Sublime idiom: two lines selected, two cursors,
// and no third cursor on the line the selection merely ended at.
func TestSplitIntoLinesAfterSelectLine(t *testing.T) {
	p := newTestPane("aaa\nbbb\nccc\n")
	p.Cursors.Set(1, 1)
	p.SelectLine()
	p.SelectLine() // [0,8): lines 0 and 1
	p.SplitIntoLines()

	if got := p.Cursors.Count(); got != 2 {
		t.Fatalf("cursor count = %d, want 2", got)
	}
	for i, want := range [][2]int{{0, 3}, {4, 7}} {
		if lo, hi := p.Cursors.All()[i].Range(); lo != want[0] || hi != want[1] {
			t.Errorf("cursor %d = (%d,%d), want (%d,%d)", i, lo, hi, want[0], want[1])
		}
	}
}

// A partial selection splits into partial selections, clipped at both ends.
func TestSplitIntoLinesClipsToSelection(t *testing.T) {
	p := newTestPane("aaa\nbbb\nccc")
	p.Cursors.Set(6, 1) // head mid line 1, anchor mid line 0
	p.SplitIntoLines()

	if got := p.Cursors.Count(); got != 2 {
		t.Fatalf("cursor count = %d, want 2", got)
	}
	for i, want := range [][2]int{{1, 3}, {4, 6}} {
		if lo, hi := p.Cursors.All()[i].Range(); lo != want[0] || hi != want[1] {
			t.Errorf("cursor %d = (%d,%d), want (%d,%d)", i, lo, hi, want[0], want[1])
		}
	}
}

// Splitting with nothing selected must not lose the cursor.
func TestSplitIntoLinesKeepsSelectionlessCursors(t *testing.T) {
	p := newTestPane("aaa\nbbb\n")
	p.Cursors.Set(5, 5)
	p.SplitIntoLines()
	if got := p.Cursors.Count(); got != 1 {
		t.Fatalf("cursor count = %d, want 1", got)
	}
	if c := p.Cursors.Primary(); c.Head != 5 || c.HasSelection() {
		t.Errorf("cursor = %+v, want a bare caret at 5", c)
	}
}

// Selecting the whole document and splitting gives one cursor per line, and no
// extra cursor past the final newline.
func TestSplitIntoLinesWholeDocument(t *testing.T) {
	p := newTestPane("aaa\nbbb\nccc\n")
	p.SelectAll()
	p.SplitIntoLines()
	if got := p.Cursors.Count(); got != 3 {
		t.Fatalf("cursor count = %d, want 3", got)
	}
}

// A secondary cursor draws a block behind the character it sits on, without
// replacing it. The bar glyph tried first covered the character — "Paste" drawn
// as "Pa|te" — and a cursor that eats a letter is worse than one shaped like a
// block. The property asserted here is that pairing: the character survives and
// the cell is unmistakably a caret.
func TestSecondaryCursorsDrawAsBars(t *testing.T) {
	p := newTestPane("aaa\nbbb\nccc\n")
	p.AddCursorVertical(+1)
	if p.Cursors.Count() != 2 {
		t.Fatalf("cursor count = %d, want 2", p.Cursors.Count())
	}

	s := ui.NewScreen(40, 10)
	p.RenderFocused(s, 0, 0, 40, 10, DefaultTheme(), true)
	gut := p.GutterWidth()

	th := DefaultTheme()
	if cell := s.At(gut, 1); cell.Rune != 'b' {
		t.Errorf("secondary cursor covered its character: cell = %q, want 'b'", cell.Rune)
	} else if cell.Style.Bg != th.Caret {
		t.Error("secondary cursor is not drawn as a caret block")
	}
	if cell := s.At(gut, 0); cell.Rune != 'a' || cell.Style.Bg == th.Caret {
		t.Errorf("primary cursor cell = %q; it should be the terminal caret", cell.Rune)
	}
}

// A secondary cursor at the end of a line has nothing to cover, and must still
// be drawn on the blank cell past the text.
func TestSecondaryCursorAtEndOfLine(t *testing.T) {
	p := newTestPane("aaa\nbbb\n")
	p.Cursors.Set(3, 3) // primary: end of line 0, drawn by the terminal
	p.Cursors.Add(7, 7) // secondary: end of line 1

	s := ui.NewScreen(40, 10)
	p.RenderFocused(s, 0, 0, 40, 10, DefaultTheme(), true)
	gut := p.GutterWidth()
	if cell := s.At(gut+3, 1); cell.Style.Bg != DefaultTheme().Caret {
		t.Errorf("end-of-line cursor is not drawn: cell = %q", cell.Rune)
	}
}
