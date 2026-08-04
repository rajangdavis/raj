package editor

import "raj/internal/view"

// Wrapping makes one document line occupy several visual rows, which breaks the
// assumption every viewport calculation used to rest on: that row N of the pane
// shows line Top+N.
//
// The mapping is rebuilt per frame rather than cached globally. Recomputing the
// visible window costs about 14 microseconds — under a tenth of a percent of a
// frame — and it is O(pane height) rather than O(document), so it does not grow
// with file size. A global line-to-row table would be faster to query but costs
// 17 milliseconds to rebuild on every width change, which is more than a whole
// frame, repeated dozens of times during a drag.
//
// Viewport.Top therefore stays a document line and gains Viewport.TopRow, the
// visual row within that line. A resize clamps TopRow against the top line's
// new row count and nothing else has to be invalidated.

// WrapPolicy is the break rule. Hybrid breaks at whitespace like word wrap and
// also at the punctuation that separates code, so a path or a call chain breaks
// where a reader expects. Plain word wrap produces byte-identical output to
// character wrap on minified output and long paths, since those lines hold no
// whitespace at all — it degrades to splitting mid-token on exactly the code
// where breaking well matters most.
var WrapPolicy = view.BreakHybrid

// textWidth is the columns available for text, excluding the gutter.
func (p *Pane) textWidth() int {
	w := p.Viewport.Cols
	if w < 1 {
		return 1
	}
	return w
}

// lineBreaks lays out one line into the pane width, returning nil when wrapping
// is off so callers take the unwrapped path without a branch at each use.
//
// buf lets the renderer reuse one slice across every line of a frame; a 63-row
// line cost 1016 bytes and 7 allocations through a fresh append.
func (p *Pane) lineBreaks(buf []int, line int) ([]int, string) {
	text := p.File.Line(line)
	if !p.Wrap {
		return nil, text
	}
	return p.File.Cols.AppendWrap(buf[:0], text, p.textWidth(), WrapPolicy), text
}

// RowsInLine is the visual row count of a line: always one when wrapping is off.
func (p *Pane) RowsInLine(line int) int {
	if !p.Wrap {
		return 1
	}
	return p.File.Cols.WrapRows(p.File.Line(line), p.textWidth(), WrapPolicy)
}

// cursorRowCol locates an offset as a visual row within its line, plus the
// display column within that row.
func (p *Pane) cursorRowCol(off int) (line, row, col int) {
	line = p.File.LineOf(off)
	text := p.File.Line(line)
	within := off - p.File.LineStart(line)
	if !p.Wrap {
		return line, 0, p.File.Cols.ColOf(text, within)
	}
	var buf [64]int
	breaks := p.File.Cols.AppendWrap(buf[:0], text, p.textWidth(), WrapPolicy)
	row, col = p.File.Cols.RowOfBreaks(breaks, text, within)
	return line, row, col
}

// stepRow moves one visual row up or down, crossing into the next or previous
// line when the current one runs out. Returns the same position at the ends of
// the document, so callers can step without bounds-checking every hop.
func (p *Pane) stepRow(line, row, dir int) (int, int) {
	if dir > 0 {
		if row+1 < p.RowsInLine(line) {
			return line, row + 1
		}
		if line+1 < p.File.Lines() {
			return line + 1, 0
		}
		return line, row
	}
	if row > 0 {
		return line, row - 1
	}
	if line > 0 {
		return line - 1, p.RowsInLine(line-1) - 1
	}
	return line, row
}

// beforeRow orders two visual positions.
func beforeRow(l1, r1, l2, r2 int) bool {
	return l1 < l2 || (l1 == l2 && r1 < r2)
}

// followCursorWrapped scrolls so the cursor's visual row is on screen.
//
// The comparison is row against row. Doing it in lines — asking whether the
// cursor's LINE is within Top..Top+Rows — reports a cursor as visible when it
// has been pushed off the bottom by the wrapped rows above it, and the caret
// then draws nowhere while you type into it.
func (p *Pane) followCursorWrapped() {
	line, row, col := p.cursorRowCol(p.Cursors.Primary().Head)
	v := &p.Viewport

	off := v.ScrollOff
	if v.Rows > 0 && off*2 >= v.Rows {
		off = (v.Rows - 1) / 2
	}

	// Above the top: put the cursor at the scrolloff margin.
	if beforeRow(line, row, v.Top, v.TopRow) {
		tl, tr := line, row
		for i := 0; i < off; i++ {
			tl, tr = p.stepRow(tl, tr, -1)
		}
		v.Top, v.TopRow = tl, tr
		p.clampWrapTop()
		v.Left = 0
		return
	}

	// Walk a pane's worth forward. Bounded by Rows, not by the document.
	tl, tr := v.Top, v.TopRow
	limit := v.Rows - off
	if limit < 1 {
		limit = 1
	}
	for i := 0; i < limit; i++ {
		if tl == line && tr == row {
			v.Left = 0
			return
		}
		nl, nr := p.stepRow(tl, tr, +1)
		if nl == tl && nr == tr {
			v.Left = 0
			return // end of document; nothing below to scroll to
		}
		tl, tr = nl, nr
	}

	// Below the fold: place the cursor on the last usable row.
	tl, tr = line, row
	for i := 0; i < v.Rows-1-off; i++ {
		nl, nr := p.stepRow(tl, tr, -1)
		if nl == tl && nr == tr {
			break
		}
		tl, tr = nl, nr
	}
	v.Top, v.TopRow = tl, tr
	p.clampWrapTop()
	v.Left = 0
	_ = col
}

// clampWrapTop keeps TopRow inside the top line. A resize changes how many rows
// that line occupies, and a stale TopRow would scroll past its own line.
func (p *Pane) clampWrapTop() {
	v := &p.Viewport
	if v.Top < 0 {
		v.Top, v.TopRow = 0, 0
	}
	if max := p.File.Lines() - 1; v.Top > max {
		v.Top = max
		if v.Top < 0 {
			v.Top = 0
		}
		v.TopRow = 0
	}
	if n := p.RowsInLine(v.Top); v.TopRow >= n {
		v.TopRow = n - 1
	}
	if v.TopRow < 0 {
		v.TopRow = 0
	}
}

// scrollRows moves the viewport by visual rows without moving the cursor.
func (p *Pane) scrollRows(delta int) {
	dir := 1
	if delta < 0 {
		dir, delta = -1, -delta
	}
	l, r := p.Viewport.Top, p.Viewport.TopRow
	for i := 0; i < delta; i++ {
		nl, nr := p.stepRow(l, r, dir)
		if nl == l && nr == r {
			break
		}
		l, r = nl, nr
	}
	p.Viewport.Top, p.Viewport.TopRow = l, r
	p.clampWrapTop()
}

// moveVerticalWrapped moves each cursor one visual row, keeping its goal column.
//
// Moving by whole lines while the screen shows rows is what makes down jump a
// whole paragraph in wrapped prose instead of moving to the next line of it.
func (p *Pane) moveVerticalWrapped(delta int, extend bool) {
	p.Cursors.Apply(func(c Cursor) Cursor {
		line, row, col := p.cursorRowCol(c.Head)
		if c.Goal > col {
			col = c.Goal
		}
		dir := 1
		n := delta
		if delta < 0 {
			dir, n = -1, -delta
		}
		for i := 0; i < n; i++ {
			line, row = p.stepRow(line, row, dir)
		}
		text := p.File.Line(line)
		var buf [64]int
		breaks := p.File.Cols.AppendWrap(buf[:0], text, p.textWidth(), WrapPolicy)
		within := p.File.Cols.OffsetAtRow(breaks, text, row, col)
		c.Head = p.File.LineStart(line) + within
		if !extend {
			c.Anchor = c.Head
		}
		c.Goal = col
		return c
	})
}
