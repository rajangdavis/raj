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
			if s.At(x, y).Style.Attr&ui.Reverse != 0 {
				n++
			}
		}
	}
	if n == 0 {
		t.Error("no secondary cursor drawn")
	}
}
