package editor

import (
	"raj/internal/keys"
	"strings"
	"testing"
)

func TestMoveLines(t *testing.T) {
	p := newTestPane("one\ntwo\nthree")
	p.Cursors.Set(5, 5) // on "two"
	p.MoveLines(-1)
	if got := p.File.Text(); got != "two\none\nthree" {
		t.Fatalf("after move up = %q", got)
	}
	if line := p.File.LineOf(p.Cursors.Primary().Head); line != 0 {
		t.Errorf("cursor on line %d, want it to follow the line to 0", line)
	}
	p.MoveLines(+1)
	if got := p.File.Text(); got != "one\ntwo\nthree" {
		t.Errorf("after move back = %q", got)
	}
}

// Moving past either end must do nothing rather than corrupting the buffer.
func TestMoveLinesAtBoundaries(t *testing.T) {
	p := newTestPane("a\nb")
	p.Cursors.Set(0, 0)
	p.MoveLines(-1)
	if got := p.File.Text(); got != "a\nb" {
		t.Errorf("moving the first line up changed the buffer: %q", got)
	}
	p.Cursors.Set(2, 2)
	p.MoveLines(+1)
	if got := p.File.Text(); got != "a\nb" {
		t.Errorf("moving the last line down changed the buffer: %q", got)
	}
}

func TestCopyLines(t *testing.T) {
	p := newTestPane("x\ny")
	p.Cursors.Set(0, 0)
	p.CopyLines(+1)
	if got := p.File.Text(); got != "x\nx\ny" {
		t.Errorf("copy down = %q", got)
	}
}

func TestAddNextOccurrence(t *testing.T) {
	p := newTestPane("foo bar foo baz foo")
	p.Cursors.Set(0, 0)
	p.AddNextOccurrence() // selects the word under the cursor
	if n := p.Cursors.Count(); n != 1 {
		t.Fatalf("first press should select the word, got %d cursors", n)
	}
	p.AddNextOccurrence()
	if n := p.Cursors.Count(); n != 2 {
		t.Fatalf("cursors = %d, want 2", n)
	}
	p.AddNextOccurrence()
	if n := p.Cursors.Count(); n != 3 {
		t.Fatalf("cursors = %d, want 3", n)
	}
	p.InsertText("X")
	if got := p.File.Text(); got != "X bar X baz X" {
		t.Errorf("text = %q", got)
	}
}

func TestSelectAllOccurrences(t *testing.T) {
	p := newTestPane("a a a b")
	p.Cursors.Set(0, 0)
	p.SelectAllOccurrences()
	if n := p.Cursors.Count(); n != 3 {
		t.Errorf("cursors = %d, want 3", n)
	}
}

func TestToggleComment(t *testing.T) {
	p := newTestPane("func a() {}\nfunc b() {}")
	p.Cursors.Set(20, 0) // spans both lines
	p.ToggleComment()
	if got := p.File.Text(); got != "// func a() {}\n// func b() {}" {
		t.Fatalf("after comment = %q", got)
	}
	p.Cursors.Set(25, 0)
	p.ToggleComment()
	if got := p.File.Text(); got != "func a() {}\nfunc b() {}" {
		t.Errorf("after uncomment = %q", got)
	}
}

// A block is uncommented only when every non-blank line is commented; a mixed
// block gets commented instead.
func TestToggleCommentMixedBlock(t *testing.T) {
	p := newTestPane("// done\ntodo")
	p.Cursors.Set(12, 0)
	p.ToggleComment()
	if got := p.File.Text(); got != "// // done\n// todo" {
		t.Errorf("mixed block = %q, want everything commented", got)
	}
}

// Comments are inserted at the block's shallowest indent so a nested block
// stays aligned.
func TestToggleCommentKeepsIndent(t *testing.T) {
	p := newTestPane("  a\n    b")
	p.Cursors.Set(9, 0)
	p.ToggleComment()
	if got := p.File.Text(); got != "  // a\n  //   b" {
		t.Errorf("indented block = %q", got)
	}
}

func TestCommentTokenByExtension(t *testing.T) {
	for path, want := range map[string]string{
		"a.go": "//", "b.py": "#", "c.lua": "--", "d.unknown": "", "Makefile": "#",
	} {
		if got := commentToken(path); got != want {
			t.Errorf("%s -> %q, want %q", path, got, want)
		}
	}
}

func TestCopyAndCut(t *testing.T) {
	p := newTestPane("hello\nworld")
	p.Cursors.Set(0, 0)
	if got := p.Copy().Text; got != "hello\n" {
		t.Errorf("copy with no selection = %q, want the whole line", got)
	}
	p.Cursors.Set(5, 0)
	if got := p.Cut().Text; got != "hello" {
		t.Errorf("cut selection = %q", got)
	}
	if got := p.File.Text(); got != "\nworld" {
		t.Errorf("after cut = %q", got)
	}
}

func TestFindStepsAndWraps(t *testing.T) {
	p := newTestPane("aa bb aa cc aa")
	p.Find.Show(p)
	p.Find.Handle(p, keys.None, "a")
	p.Find.Handle(p, keys.None, "a")
	if n := len(p.Find.Matches()); n != 3 {
		t.Fatalf("matches = %d, want 3", n)
	}
	first := p.Cursors.Primary().Head
	p.Find.Handle(p, keys.Confirm, "")
	if p.Cursors.Primary().Head == first {
		t.Error("enter did not advance to the next match")
	}
	p.Find.Handle(p, keys.Confirm, "")
	p.Find.Handle(p, keys.Confirm, "")
	if got := p.Cursors.Primary().Head; got != first {
		t.Errorf("head = %d after wrapping, want %d", got, first)
	}
}

// Smart case: a lowercase query is case-insensitive, a query with a capital is
// not.
func TestFindSmartCase(t *testing.T) {
	p := newTestPane("Foo foo FOO")
	p.Find.Show(p)
	for _, r := range "foo" {
		p.Find.Handle(p, keys.None, string(r))
	}
	if n := len(p.Find.Matches()); n != 3 {
		t.Errorf("lowercase query matched %d, want 3", n)
	}
	p.Find.Hide()
	p.Cursors.Set(0, 0)
	p.Find.Show(p)
	for _, r := range "Foo" {
		p.Find.Handle(p, keys.None, string(r))
	}
	if n := len(p.Find.Matches()); n != 1 {
		t.Errorf("capitalised query matched %d, want 1", n)
	}
}

// Opening find with a selection seeds the query from it.
func TestFindSeedsFromSelection(t *testing.T) {
	p := newTestPane("alpha beta alpha")
	p.Cursors.Set(5, 0)
	p.Find.Show(p)
	if got := p.Find.Query(); got != "alpha" {
		t.Errorf("query = %q, want the selection", got)
	}
	if n := len(p.Find.Matches()); n != 2 {
		t.Errorf("matches = %d, want 2", n)
	}
}

func TestFindHighlightMarksCurrent(t *testing.T) {
	p := newTestPane(strings.Repeat("x", 3) + " x")
	p.Find.Show(p)
	p.Find.Handle(p, keys.None, "x")
	if m, _ := p.Find.Highlight(0); !m {
		t.Error("first match not highlighted")
	}
	if m, _ := p.Find.Highlight(3); m {
		t.Error("a space was highlighted")
	}
}

// Moving a line must carry its cursor with it. Adjusting byte offsets after the
// swap puts the cursor on the line that moved the other way.
func TestMoveLinesCarriesCursor(t *testing.T) {
	p := newTestPane("aaa\nbbb\nccc")
	p.Cursors.Set(5, 5) // inside "bbb"
	p.MoveLines(+1)
	if got := p.File.Text(); got != "aaa\nccc\nbbb" {
		t.Fatalf("text = %q", got)
	}
	line, col := p.File.LineCol(p.Cursors.Primary().Head)
	if line != 2 || col != 1 {
		t.Errorf("cursor at %d:%d, want 2:1 following bbb", line, col)
	}
	p.MoveLines(-1)
	if got := p.File.Text(); got != "aaa\nbbb\nccc" {
		t.Errorf("moving back gave %q", got)
	}
	if line, _ := p.File.LineCol(p.Cursors.Primary().Head); line != 1 {
		t.Errorf("cursor on line %d, want 1", line)
	}
}

// A multi-line selection moves as a block.
func TestMoveLinesBlock(t *testing.T) {
	p := newTestPane("1\n2\n3\n4")
	p.Cursors.Set(3, 0) // lines 0 and 1
	p.MoveLines(+1)
	if got := p.File.Text(); got != "3\n1\n2\n4" {
		t.Errorf("text = %q, want the block moved down", got)
	}
}
