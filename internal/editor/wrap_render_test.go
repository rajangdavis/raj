package editor

import (
	"strings"
	"testing"

	"raj/internal/ui"
)

func wrapPane(t *testing.T, body string, cols, rows int) *Pane {
	t.Helper()
	p := NewPane(NewFile("t.md", body, 2))
	p.Wrap = true
	p.Resize(cols, rows)
	return p
}

// render draws into a screen and returns the text of each row, so assertions
// are about what a reader sees rather than about internal offsets.
func render(p *Pane, cols, rows int) []string {
	s := ui.NewScreen(cols, rows)
	p.RenderFocused(s, 0, 0, cols, rows, Theme{}, true)
	out := make([]string, rows)
	for y := 0; y < rows; y++ {
		var sb strings.Builder
		for x := 0; x < cols; x++ {
			c := s.At(x, y)
			if c.Rune == 0 {
				sb.WriteByte(' ')
				continue
			}
			sb.WriteRune(c.Rune)
		}
		out[y] = strings.TrimRight(sb.String(), " ")
	}
	return out
}

// A line longer than the pane occupies several rows instead of being clipped.
func TestWrappedLineOccupiesSeveralRows(t *testing.T) {
	p := wrapPane(t, "alpha beta gamma delta epsilon zeta\nshort\n", 20, 8)
	rows := render(p, 20, 8)
	if rows[0] == "" {
		t.Fatal("nothing drawn")
	}
	// "short" must appear below the wrapped rows, not on row 1.
	at := -1
	for i, r := range rows {
		if strings.Contains(r, "short") {
			at = i
		}
	}
	if at <= 1 {
		t.Errorf("second line drawn at row %d; the long line above did not wrap\n%q", at, rows)
	}
}

// The line number appears once per line, not once per visual row.
func TestGutterNumbersLinesNotRows(t *testing.T) {
	p := wrapPane(t, "alpha beta gamma delta epsilon zeta\nshort\n", 20, 8)
	rows := render(p, 20, 8)
	ones := 0
	for _, r := range rows {
		if strings.HasPrefix(strings.TrimSpace(r), "1 ") || strings.TrimSpace(r) == "1" {
			ones++
		}
	}
	if ones > 1 {
		t.Errorf("line 1 numbered %d times; continuation rows should be blank\n%q", ones, rows)
	}
}

// The caret must land on the row holding the cursor, counting the rows that
// wrapped above it. Subtracting line numbers puts it on the line's first row.
func TestCaretFollowsWrappedRows(t *testing.T) {
	body := "alpha beta gamma delta epsilon zeta eta theta\nsecond\n"
	p := wrapPane(t, body, 20, 10)
	// Put the cursor on the second document line.
	p.Cursors.Set(len(body)-8, len(body)-8)
	p.FollowCursor()

	s := ui.NewScreen(20, 10)
	p.RenderFocused(s, 0, 0, 20, 10, Theme{}, true)
	cy := s.CursorY

	line, within, _ := p.cursorRowCol(p.Cursors.Primary().Head)
	want := -p.Viewport.TopRow + within
	for ln := p.Viewport.Top; ln < line; ln++ {
		want += p.RowsInLine(ln)
	}
	if cy != want {
		t.Errorf("caret at row %d, want %d", cy, want)
	}
}

// Down inside a wrapped line moves one visual row, not a whole paragraph.
func TestDownMovesOneVisualRow(t *testing.T) {
	p := wrapPane(t, "alpha beta gamma delta epsilon zeta eta theta\nnext\n", 20, 10)
	p.Cursors.Set(0, 0)
	_, row0, _ := p.cursorRowCol(p.Cursors.Primary().Head)
	p.MoveVertical(1, false)
	line1, row1, _ := p.cursorRowCol(p.Cursors.Primary().Head)
	if line1 != 0 || row1 != row0+1 {
		t.Errorf("down landed on line %d row %d; want line 0 row %d", line1, row1, row0+1)
	}
}

// Down at the last row of a line crosses into the next line.
func TestDownCrossesIntoNextLine(t *testing.T) {
	p := wrapPane(t, "alpha beta gamma delta epsilon\nnext line here\n", 20, 10)
	p.Cursors.Set(0, 0)
	for i := 0; i < 10; i++ {
		p.MoveVertical(1, false)
		if p.File.LineOf(p.Cursors.Primary().Head) == 1 {
			return
		}
	}
	t.Error("never reached the second line stepping down")
}

// A resize clamps TopRow rather than leaving it pointing past its own line.
func TestResizeClampsTopRow(t *testing.T) {
	p := wrapPane(t, strings.Repeat("word ", 40)+"\nsecond\n", 20, 6)
	p.Viewport.TopRow = 8
	p.Resize(200, 6) // now the line fits on one row
	if n := p.RowsInLine(p.Viewport.Top); p.Viewport.TopRow >= n {
		t.Errorf("TopRow %d past the %d rows of line %d", p.Viewport.TopRow, n, p.Viewport.Top)
	}
}

// Scrolling into the middle of a paragraph taller than the pane must be
// possible, which is what TopRow exists for.
func TestScrollIntoTallParagraph(t *testing.T) {
	p := wrapPane(t, strings.Repeat("word ", 200)+"\n", 20, 5)
	if got := p.RowsInLine(0); got <= 5 {
		t.Fatalf("setup: paragraph is %d rows, needs to exceed the pane", got)
	}
	p.scrollRows(3)
	if p.Viewport.Top != 0 || p.Viewport.TopRow != 3 {
		t.Errorf("Top=%d TopRow=%d, want 0/3", p.Viewport.Top, p.Viewport.TopRow)
	}
}

// Typing at the end of a long line keeps the cursor on screen. Comparing lines
// against a row-sized pane reports it visible when wrapped rows have pushed it
// off the bottom, and the caret then draws nowhere.
func TestCursorStaysVisibleOnLongLine(t *testing.T) {
	p := wrapPane(t, strings.Repeat("word ", 60)+"\n", 20, 6)
	end := p.File.Len() - 1
	p.Cursors.Set(end, end)
	p.FollowCursor()

	line, within, _ := p.cursorRowCol(p.Cursors.Primary().Head)
	row := -p.Viewport.TopRow + within
	for ln := p.Viewport.Top; ln < line; ln++ {
		row += p.RowsInLine(ln)
	}
	if row < 0 || row >= 6 {
		t.Errorf("cursor at row %d of a 6-row pane", row)
	}
}

// With wrapping off, everything behaves exactly as before: one line per row and
// no TopRow. This is what keeps the feature opt-in rather than a rewrite.
func TestWrapOffIsUnchanged(t *testing.T) {
	body := strings.Repeat("word ", 60) + "\nsecond\n"
	p := NewPane(NewFile("t.go", body, 2))
	p.Resize(20, 6)
	p.Cursors.Set(0, 0)
	p.FollowCursor()
	if p.Viewport.TopRow != 0 {
		t.Errorf("TopRow = %d with wrapping off", p.Viewport.TopRow)
	}
	if got := p.RowsInLine(0); got != 1 {
		t.Errorf("rowsIn = %d with wrapping off, want 1", got)
	}
}
