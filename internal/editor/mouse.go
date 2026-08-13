package editor

// Mouse positioning: turning a screen cell into a document offset.
//
// This is the exact inverse of placeCaret, and the two have to stay inverses —
// a click that lands somewhere other than where the caret would be drawn for
// that offset is a click that appears to miss. Where the arithmetic is subtle
// it is subtle in both directions, which is why the tests assert the round trip
// rather than either half.
//
// Three coordinate systems meet here, and the conversion order matters: a
// screen cell becomes a display column, a display column becomes a byte offset
// within a line, and only then is it a document offset. Skipping the middle
// step is what makes clicks land wrong on lines containing tabs or CJK.

// OffsetAt converts a cell within the pane's text area to a document offset.
//
// x and y are relative to the text area — the caller subtracts the gutter and
// any bar above the pane, since only the caller knows what it drew there.
//
// A click past the end of a line lands at the line's end rather than wrapping
// to the next, because dragging past the right edge is how a whole line gets
// selected and wrapping would quietly select the following line's start. A
// click below the last line lands at the end of the document, for the same
// reason.
func (p *Pane) OffsetAt(x, y int) int {
	if p.Wrap {
		return p.offsetAtWrapped(x, y)
	}
	if y < 0 {
		y = 0
	}
	line := p.Viewport.Top + y
	if last := p.File.Lines() - 1; line > last {
		line = last
	}
	if line < 0 {
		return 0
	}
	col := x + p.Viewport.Left
	if col < 0 {
		col = 0
	}
	text := p.File.Line(line)
	return p.File.LineStart(line) + p.File.Cols.OffsetOf(text, col)
}

// offsetAtWrapped walks visual rows the way placeCaretWrapped does, counting
// the rows each line occupies rather than assuming one row per line.
func (p *Pane) offsetAtWrapped(x, y int) int {
	if y < 0 {
		y = 0
	}
	if x < 0 {
		x = 0
	}
	// Start above the viewport's first line by however many of its rows are
	// scrolled off, which is what TopRow records.
	row := -p.Viewport.TopRow
	lines := p.File.Lines()
	for line := p.Viewport.Top; line < lines; line++ {
		n := p.RowsInLine(line)
		if y < row+n {
			within := y - row
			if within < 0 {
				within = 0
			}
			return p.offsetInRow(line, within, x)
		}
		row += n
	}
	return p.File.Len()
}

// offsetInRow resolves a column within one visual row of a wrapped line.
func (p *Pane) offsetInRow(line, within, x int) int {
	breaks, text := p.lineBreaks(nil, line)
	start := 0
	if within > 0 && within-1 < len(breaks) {
		start = breaks[within-1]
	}
	end := len(text)
	if within < len(breaks) {
		end = breaks[within]
	}
	if start > len(text) {
		start = len(text)
	}
	if end > len(text) {
		end = len(text)
	}
	segment := text[start:end]
	// The column is measured from the start of the row, but tab stops are
	// measured from the start of the line — so the segment is expanded in its
	// own right only when it begins at a tab stop. Rows begin at a break
	// chosen by the wrapper, which does not align to tab stops, so the offset
	// is resolved within the segment and the residual accepted: a click inside
	// a tab that straddles a wrap point resolves to that tab, which is the
	// same answer the caret would give.
	return p.File.LineStart(line) + start + p.File.Cols.OffsetOf(segment, x)
}

// ClickAt places the cursor where the pointer is.
//
// A plain click collapses to a single cursor: a pointer is a statement about
// one place, and leaving several cursors behind would mean the next keystroke
// edits somewhere the click did not name.
func (p *Pane) ClickAt(x, y int, extend bool) {
	off := p.OffsetAt(x, y)
	if extend {
		p.Cursors.Set(off, p.Cursors.Primary().Anchor)
	} else {
		p.Cursors.Set(off, off)
	}
	p.FollowCursor()
}

// AddCursorAt adds a cursor at the pointer rather than moving the existing one,
// which is what a modifier-click means in every editor that has one.
func (p *Pane) AddCursorAt(x, y int) {
	off := p.OffsetAt(x, y)
	list := append(p.Cursors.All(), Cursor{Head: off, Anchor: off})
	p.Cursors.Replace(list)
	p.FollowCursor()
}

// DragTo extends the selection to the pointer, keeping the anchor where the
// drag began.
func (p *Pane) DragTo(x, y int) {
	off := p.OffsetAt(x, y)
	p.Cursors.Set(off, p.Cursors.Primary().Anchor)
	p.FollowCursor()
}

// SelectWordAt selects the word under the pointer, for a double click.
func (p *Pane) SelectWordAt(x, y int) {
	off := p.OffsetAt(x, y)
	lo, hi := p.wordAround(off)
	p.Cursors.Set(hi, lo)
	p.FollowCursor()
}

// SelectLineAt selects the line under the pointer, for a triple click.
func (p *Pane) SelectLineAt(x, y int) {
	off := p.OffsetAt(x, y)
	line := p.File.LineOf(off)
	start := p.File.LineStart(line)
	end := p.File.Len()
	if line+1 < p.File.Lines() {
		end = p.File.LineStart(line + 1)
	}
	p.Cursors.Set(end, start)
	p.FollowCursor()
}

// wordAround is the word boundaries around an offset, or the offset twice when
// it is not in a word.
func (p *Pane) wordAround(off int) (lo, hi int) {
	line := p.File.LineOf(off)
	start := p.File.LineStart(line)
	text := p.File.Line(line)
	i := off - start
	if i < 0 || i > len(text) {
		return off, off
	}
	isWord := func(b byte) bool {
		return b == '_' || b >= '0' && b <= '9' ||
			b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= 0x80
	}
	// A click just past a word selects it, which is what happens when the
	// pointer lands on the space after the last letter.
	if i == len(text) || !isWord(text[i]) {
		if i > 0 && isWord(text[i-1]) {
			i--
		} else {
			return off, off
		}
	}
	a, b := i, i
	for a > 0 && isWord(text[a-1]) {
		a--
	}
	for b < len(text) && isWord(text[b]) {
		b++
	}
	return start + a, start + b
}
