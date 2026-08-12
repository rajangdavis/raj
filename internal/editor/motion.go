package editor

import (
	"unicode"
	"unicode/utf8"
)

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
// ScrollPage moves the viewport a page without touching a single cursor.
//
// Scrolling used to move the cursor, which reads fine with one caret and badly
// with several: paging to look at something else collapses a multi-cursor set
// you spent keystrokes building, and there is no way to get it back except
// cursor-undo. The view and the carets are now independent — scroll away, and
// the next thing that moves a cursor pulls the view back to it, because every
// cursor action still ends in FollowCursor.
func (p *Pane) ScrollPage(dir int) {
	rows := p.Viewport.Rows
	if rows < 2 {
		rows = 2
	}
	p.Viewport.ScrollBy((rows-1)*dir, p.File.Lines())
}

// ScrollRows moves the view by rows without moving the cursor, for the wheel.
//
// The cursor deliberately stays put. Scrolling with the wheel is looking, not
// navigating: dragging the cursor along would change what the next keystroke
// edits, and the cursor would arrive somewhere the user never chose.
func (p *Pane) ScrollRows(rows int) {
	p.Viewport.ScrollBy(rows, p.File.Lines())
}

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

// SelectLine expands every cursor to whole lines, trailing newline included,
// and pressing it again takes the next line — Sublime Text's cmd+l.
//
// One formula covers both: the selection runs from the start of the line the
// low end sits on to the start of the line *after* the one the high end sits
// on. With no selection that is the current line; after a first press the high
// end sits at the start of the following line, so LineOf lands there and the
// span grows by exactly one line. The head goes to the end so that shift+up or
// a second press continues in the direction the eye expects.
func (p *Pane) SelectLine() {
	p.Cursors.Apply(func(c Cursor) Cursor {
		lo, hi := c.Range()
		start := p.File.LineStart(p.File.LineOf(lo))
		end := p.File.Len()
		if next := p.File.LineOf(hi) + 1; next < p.File.Lines() {
			end = p.File.LineStart(next)
		}
		_, col := p.File.LineCol(end)
		return Cursor{Head: end, Anchor: start, Goal: col}
	})
}

// SplitIntoLines replaces every selection with one cursor per line it spans,
// clipped to the selection at both ends. This is Sublime's cmd+shift+l, and it
// is what makes cmd+l worth stacking: select the block, then split it.
//
// The end of a cmd+l selection sits at the start of the following line, and
// that line is not part of it — splitting there would leave a stray cursor on a
// line the user never selected. A selection ending exactly on a boundary
// therefore drops its last line, the same boundary rule cmd+l uses to decide
// what to extend next.
//
// Cursors with no selection are carried through untouched rather than dropped:
// splitting nothing should not cost you the caret it came from.
func (p *Pane) SplitIntoLines() {
	var out []Cursor
	for _, c := range p.Cursors.All() {
		if !c.HasSelection() {
			out = append(out, c)
			continue
		}
		lo, hi := c.Range()
		first, last := p.File.LineOf(lo), p.File.LineOf(hi)
		if last > first && hi == p.File.LineStart(last) {
			last--
		}
		for line := first; line <= last; line++ {
			start, end := p.File.LineStart(line), p.File.LineEnd(line)
			if start < lo {
				start = lo
			}
			if end > hi {
				end = hi
			}
			_, col := p.File.LineCol(end)
			out = append(out, Cursor{Head: end, Anchor: start, Goal: col})
		}
	}
	p.Cursors.Replace(out)
}

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
	// Runes, not bytes. Reading one byte and calling it a character makes every
	// continuation byte of a multi-byte rune look like a non-word character, so
	// the scan stops inside the rune and leaves the cursor at an offset the rest
	// of the editor cannot address. Found by the spec properties, which walk
	// documents containing text that is not ASCII.
	next := func(i int) (rune, int) { // the rune at i, and the offset after it
		if i < 0 || i >= n {
			return 0, i
		}
		r, size := utf8.DecodeRuneInString(p.File.Slice(i, min(4, n-i)))
		return r, i + size
	}
	prev := func(i int) (rune, int) { // the rune before i, and where it starts
		if i <= 0 {
			return 0, 0
		}
		start := p.prevBoundary(i)
		r, _ := utf8.DecodeRuneInString(p.File.Slice(start, i-start))
		return r, start
	}

	look := off
	if dir > 0 {
		for look < n { // skip whatever is not a word
			r, after := next(look)
			if isWord(r) {
				break
			}
			look = after
		}
		for look < n { // then cross the word
			r, after := next(look)
			if !isWord(r) {
				break
			}
			look = after
		}
		return clamp(look, 0, n)
	}
	for look > 0 {
		r, start := prev(look)
		if isWord(r) {
			break
		}
		look = start
	}
	for look > 0 {
		r, start := prev(look)
		if !isWord(r) {
			break
		}
		look = start
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
