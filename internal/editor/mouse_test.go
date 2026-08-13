package editor

import (
	"strings"
	"testing"
)

// caretCell is where the caret would be drawn for an offset, which is what
// OffsetAt must invert. Computed the same way placeCaret does.
func caretCell(p *Pane, off int) (x, y int) {
	line, col := p.File.LineCol(off)
	return col - p.Viewport.Left, line - p.Viewport.Top
}

// The contract: a click lands where the caret would be drawn for that offset.
// Anything else is a click that appears to miss, and the two halves have to
// stay inverses — so the round trip is asserted rather than either half.
func TestClickInvertsCaretPlacement(t *testing.T) {
	p := newTestPane("package main\n\nfunc main() {\n\tprintln(\"hi\")\n}\n")
	p.Resize(40, 10)

	for off := 0; off <= p.File.Len(); off++ {
		x, y := caretCell(p, off)
		if y < 0 || y >= 10 {
			continue
		}
		got := p.OffsetAt(x, y)
		if got != off {
			line, col := p.File.LineCol(off)
			t.Errorf("offset %d (line %d col %d) drew at (%d,%d) which reads back as %d",
				off, line, col, x, y, got)
		}
	}
}

// Tabs are elastic, so a click inside one has to resolve to the tab rather than
// to a column that does not exist. This is where naive column arithmetic goes
// wrong.
func TestClickOnTabs(t *testing.T) {
	p := newTestPane("\t\tindented\n") // tab width 2 in the fixture
	p.Resize(40, 10)

	// Columns 0-1 are the first tab, 2-3 the second, 4 onwards the text.
	cases := map[int]int{0: 0, 1: 0, 2: 1, 3: 1, 4: 2, 5: 3}
	for col, want := range cases {
		if got := p.OffsetAt(col, 0); got != want {
			t.Errorf("column %d resolved to offset %d, want %d", col, got, want)
		}
	}
}

// A click past the end of a line lands at the line's end rather than wrapping
// to the next: dragging past the right edge is how a whole line is selected,
// and wrapping would quietly take the following line's start too.
func TestClickPastLineEnd(t *testing.T) {
	p := newTestPane("short\nlonger line here\n")
	p.Resize(40, 10)

	end := len("short")
	for _, x := range []int{5, 10, 39, 200} {
		if got := p.OffsetAt(x, 0); got != end {
			t.Errorf("x=%d gave %d, want the line end %d", x, got, end)
		}
	}
}

// A click below the last line lands at the end of the document.
func TestClickBelowTheLastLine(t *testing.T) {
	p := newTestPane("one\ntwo\n")
	p.Resize(40, 10)
	for _, y := range []int{5, 9, 100} {
		got := p.OffsetAt(0, y)
		if got != p.File.LineStart(p.File.Lines()-1) && got != p.File.Len() {
			t.Errorf("y=%d gave %d, want the last line", y, got)
		}
	}
}

// Negative coordinates clamp rather than indexing backwards.
func TestClickOutsideClamps(t *testing.T) {
	p := newTestPane("one\ntwo\n")
	p.Resize(40, 10)
	if got := p.OffsetAt(-5, -5); got != 0 {
		t.Errorf("got %d, want the start of the document", got)
	}
}

// A scrolled viewport offsets both axes.
func TestClickWhenScrolled(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 100; i++ {
		b.WriteString("line ")
		b.WriteByte(byte('0' + i%10))
		b.WriteByte('\n')
	}
	p := newTestPane(b.String())
	p.Resize(40, 10)
	p.Viewport.Top = 40

	off := p.OffsetAt(0, 0)
	if line, _ := p.File.LineCol(off); line != 40 {
		t.Errorf("the top row is line %d, want 40", line)
	}
	off = p.OffsetAt(0, 3)
	if line, _ := p.File.LineCol(off); line != 43 {
		t.Errorf("row 3 is line %d, want 43", line)
	}
}

// A plain click collapses to one cursor: a pointer is a statement about one
// place, and leaving several behind means the next keystroke edits somewhere
// the click did not name.
func TestClickCollapsesToOneCursor(t *testing.T) {
	p := newTestPane("a\nb\nc\n")
	p.Resize(40, 10)
	p.DocStart(false)
	p.AddCursorVertical(+1)
	p.AddCursorVertical(+1)
	if len(p.Cursors.All()) < 2 {
		t.Fatal("setup: expected several cursors")
	}

	p.ClickAt(0, 1, false)
	if n := len(p.Cursors.All()); n != 1 {
		t.Errorf("%d cursors after a click, want 1", n)
	}
	if got := p.Cursors.Primary().Head; got != 2 {
		t.Errorf("cursor at %d, want the start of line 2", got)
	}
}

// A modifier click adds a cursor rather than moving the existing one.
func TestAddCursorAt(t *testing.T) {
	p := newTestPane("a\nb\nc\n")
	p.Resize(40, 10)
	p.DocStart(false)
	p.AddCursorAt(0, 2)
	if n := len(p.Cursors.All()); n != 2 {
		t.Fatalf("%d cursors, want 2", n)
	}
}

// A drag keeps the anchor where it began, which is what makes a selection grow
// in both directions.
func TestDragKeepsTheAnchor(t *testing.T) {
	p := newTestPane("hello world\nsecond line\n")
	p.Resize(40, 10)

	p.ClickAt(6, 0, false) // at "world"
	anchor := p.Cursors.Primary().Head
	p.DragTo(11, 0)
	c := p.Cursors.Primary()
	if c.Anchor != anchor {
		t.Errorf("anchor moved from %d to %d", anchor, c.Anchor)
	}
	if !c.HasSelection() {
		t.Error("dragging produced no selection")
	}
	// Dragging back past the start inverts the selection rather than clearing
	// it: the anchor is where the drag began, not the lower bound.
	p.DragTo(0, 0)
	c = p.Cursors.Primary()
	if c.Head >= c.Anchor {
		t.Error("dragging left did not invert the selection")
	}
}

// Double click selects a word, including when the pointer is just past its
// last letter — which is where it lands when you aim at the end of a word.
func TestSelectWordAt(t *testing.T) {
	p := newTestPane("alpha beta_two gamma\n")
	p.Resize(40, 10)

	cases := map[int]string{
		0: "alpha", 3: "alpha", 5: "alpha", // 5 is the space after it
		6: "beta_two", 10: "beta_two",
		15: "gamma",
	}
	for x, want := range cases {
		p.SelectWordAt(x, 0)
		c := p.Cursors.Primary()
		lo, hi := c.Range()
		if got := p.File.Text()[lo:hi]; got != want {
			t.Errorf("x=%d selected %q, want %q", x, got, want)
		}
	}
}

// A double click in whitespace selects nothing rather than the nearest word in
// some arbitrary direction.
func TestSelectWordInWhitespace(t *testing.T) {
	p := newTestPane("a    b\n")
	p.Resize(40, 10)
	p.SelectWordAt(3, 0)
	if p.Cursors.Primary().HasSelection() {
		t.Error("a click in whitespace selected something")
	}
}

// Triple click selects the line, including its newline so that deleting the
// selection removes the line rather than leaving a blank one.
func TestSelectLineAt(t *testing.T) {
	p := newTestPane("first\nsecond\nthird\n")
	p.Resize(40, 10)
	p.SelectLineAt(2, 1)
	lo, hi := p.Cursors.Primary().Range()
	if got := p.File.Text()[lo:hi]; got != "second\n" {
		t.Errorf("selected %q, want the whole line", got)
	}
}

// Wrapped panes count visual rows rather than lines, the same way the caret
// does — subtracting line numbers would put a click several rows off.
func TestClickWhenWrapped(t *testing.T) {
	p := newTestPane("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\nshort\n")
	p.Wrap = true
	p.Resize(14, 10) // narrow enough that the first line wraps

	if p.RowsInLine(0) < 2 {
		t.Fatal("setup: the first line should wrap")
	}
	// The row after the wrapped line's rows is the second document line.
	row := p.RowsInLine(0)
	off := p.OffsetAt(0, row)
	if line, _ := p.File.LineCol(off); line != 1 {
		t.Errorf("row %d is line %d, want 1", row, line)
	}
	// And a click on the wrapped line's second row is still line 0, past its
	// first row's worth of characters.
	off = p.OffsetAt(0, 1)
	line, col := p.File.LineCol(off)
	if line != 0 || col == 0 {
		t.Errorf("second row resolved to %d:%d, want line 0 past column 0", line, col)
	}
}

// Nothing here may panic on an empty buffer or a degenerate viewport, which is
// what a pane looks like before its first resize.
func TestMouseDegenerateInputs(t *testing.T) {
	for _, text := range []string{"", "\n", "no trailing newline"} {
		p := newTestPane(text)
		p.OffsetAt(0, 0)
		p.OffsetAt(100, 100)
		p.OffsetAt(-1, -1)
		p.ClickAt(0, 0, false)
		p.SelectWordAt(0, 0)
		p.SelectLineAt(0, 0)
		p.DragTo(5, 5)

		w := newTestPane(text)
		w.Wrap = true
		w.OffsetAt(0, 0)
		w.OffsetAt(50, 50)
	}
}
