package editor

import "unicode"

// MoveTo moves every cursor through fn. extend keeps the anchor, turning the
// motion into a selection; otherwise the selection collapses.
//
// Every motion routes through here so that the shift-variant of a key is never
// a separate implementation — SelWordLeft is WordLeft with extend set, and they
// cannot drift apart.
func (p *Pane) MoveTo(fn func(Cursor, *File) int, extend bool) {
	p.Cursors.Apply(func(c Cursor) Cursor {
		c.Head = clamp(fn(c, p.File), 0, p.File.Len())
		if !extend {
			c.Anchor = c.Head
		}
		_, col := p.File.LineCol(c.Head)
		c.Goal = col
		return c
	})
}

// MoveVertical moves by whole lines, preserving each cursor's goal column.
func (p *Pane) MoveVertical(delta int, extend bool) {
	if p.Wrap {
		// One visual row, not one line: moving by lines while the screen shows
		// rows makes "down" jump a whole paragraph in wrapped prose.
		p.moveVerticalWrapped(delta, extend)
		return
	}
	p.Cursors.Apply(func(c Cursor) Cursor {
		line, col := p.File.LineCol(c.Head)
		if c.Goal > col {
			col = c.Goal // remember the column we wanted, not the one we got
		}
		target := clamp(line+delta, 0, p.File.Lines()-1)
		c.Head = p.File.OffsetAt(target, col)
		if !extend {
			c.Anchor = c.Head
		}
		c.Goal = col
		return c
	})
}

// CharLeft and friends are the motions the keymap dispatches to. Each is a
// one-line wrapper so the action table stays declarative.
func (p *Pane) CharLeft(extend bool) {
	p.MoveTo(func(c Cursor, f *File) int {
		if !extend && c.HasSelection() {
			lo, _ := c.Range()
			return lo
		}
		return p.prevBoundary(c.Head)
	}, extend)
}

func (p *Pane) CharRight(extend bool) {
	p.MoveTo(func(c Cursor, f *File) int {
		if !extend && c.HasSelection() {
			_, hi := c.Range()
			return hi
		}
		return p.nextBoundary(c.Head)
	}, extend)
}

func (p *Pane) LineStart(extend bool) {
	// First press goes to the first non-blank character, second to column zero.
	// Reaching indented code is the common case; reaching column zero is not.
	p.MoveTo(func(c Cursor, f *File) int {
		line := f.LineOf(c.Head)
		start := f.LineStart(line)
		text := f.Line(line)
		indent := start + len(text) - len(trimLeadingSpace(text))
		if c.Head == indent {
			return start
		}
		return indent
	}, extend)
}

func (p *Pane) LineEnd(extend bool) {
	p.MoveTo(func(c Cursor, f *File) int { return f.LineEnd(f.LineOf(c.Head)) }, extend)
}

func (p *Pane) DocStart(extend bool) {
	p.MoveTo(func(Cursor, *File) int { return 0 }, extend)
}

func (p *Pane) DocEnd(extend bool) {
	p.MoveTo(func(c Cursor, f *File) int { return f.Len() }, extend)
}

func (p *Pane) WordLeft(extend bool) {
	p.MoveTo(func(c Cursor, f *File) int { return p.scanWord(c.Head, -1) }, extend)
}

func (p *Pane) WordRight(extend bool) {
	p.MoveTo(func(c Cursor, f *File) int { return p.scanWord(c.Head, +1) }, extend)
}

// MovePage moves by a screenful, scrolling the view the same distance so the
// cursor keeps its position on screen. Moving the cursor a page without moving
// the view is disorienting: the text jumps but the caret appears not to.
func (p *Pane) MovePage(dir int, extend bool) {
	rows := p.Viewport.Rows
	if rows < 2 {
		rows = 2
	}
	step := (rows - 1) * dir
	p.Viewport.ScrollBy(step, p.File.Lines())
	p.MoveVertical(step, extend)
}

// SelectAll selects the whole document with a single cursor.
func (p *Pane) SelectAll() { p.Cursors.Set(p.File.Len(), 0) }

// AddCursorVertical adds a cursor one line above or below each existing one,
// which is the cmd+alt+up/down gesture.
func (p *Pane) AddCursorVertical(delta int) {
	for _, c := range p.Cursors.All() {
		line, col := p.File.LineCol(c.Head)
		target := line + delta
		if target < 0 || target >= p.File.Lines() {
			continue
		}
		off := p.File.OffsetAt(target, col)
		p.Cursors.Add(off, off)
	}
}

// scanWord walks to the next word boundary, skipping separators first and then
// the word itself — so repeated presses land between words rather than
// advancing one character at a time through punctuation.
func (p *Pane) scanWord(off, dir int) int {
	n := p.File.Len()
	at := func(i int) rune {
		if i < 0 || i >= n {
			return 0
		}
		return rune(p.File.Slice(i, 1)[0])
	}
	look := off
	if dir < 0 {
		look = off - 1
	}
	for look >= 0 && look < n && !isWord(at(look)) {
		look += dir
	}
	for look >= 0 && look < n && isWord(at(look)) {
		look += dir
	}
	if dir < 0 {
		return clamp(look+1, 0, n)
	}
	return clamp(look, 0, n)
}

func isWord(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

func trimLeadingSpace(s string) string {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	return s[i:]
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
