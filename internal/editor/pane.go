package editor

import (
	"strings"

	"raj/internal/piecetable"
	"raj/internal/view"
)

// Pane is one editing surface: a file, its cursors, and the window onto it.
//
// Edits are applied cursor-by-cursor from the highest offset down, so earlier
// cursors' offsets stay valid while later ones are being edited. Doing it the
// other way requires shifting every remaining cursor after each edit, which is
// where multi-cursor implementations usually go wrong.
type Pane struct {
	File     *File
	Cursors  *Cursors
	Viewport view.Viewport
	Author   piecetable.Author
	Find     Find

	focused bool
}

// NewPane wraps a file for editing.
func NewPane(f *File) *Pane {
	return &Pane{
		File:     f,
		Cursors:  NewCursors(),
		Viewport: view.Viewport{ScrollOff: 2},
		Author:   piecetable.User,
	}
}

// Resize sets the visible area in cells.
func (p *Pane) Resize(cols, rows int) {
	p.Viewport.Resize(cols, rows, p.File.Lines())
}

// FollowCursor scrolls so the primary cursor is visible.
func (p *Pane) FollowCursor() {
	line, col := p.File.LineCol(p.Cursors.Primary().Head)
	p.Viewport.ScrollTo(line, col, p.File.Lines())
}

// InsertText types at every cursor, replacing selections.
func (p *Pane) InsertText(text string) {
	p.editEachCursor(func(c Cursor) (pos, remove int, insert string) {
		lo, hi := c.Range()
		return lo, hi - lo, text
	})
}

// Paste inserts a block of text as a single edit.
//
// A paste has no per-cursor semantics: it is one chunk of text arriving at one
// place. Routing it through the per-cursor machinery makes a large paste into
// as many ops as there are cursors, each appending its own copy to the author
// store, and turns one undo step into several. This appends once and commits
// once, which is also why a 100-line paste costs three pieces rather than
// hundreds.
func (p *Pane) Paste(text string) {
	if text == "" {
		return
	}
	p.File.Begin()
	defer p.File.End()

	c := p.Cursors.Primary()
	lo, hi := c.Range()
	p.Cursors.Set(lo, lo)
	if hi > lo {
		p.File.Delete(p.Author, lo, hi-lo)
	}
	p.File.Insert(p.Author, lo, text)
	at := lo + len(text)
	p.Cursors.Set(at, at)
	p.FollowCursor()
}

// PasteDistributed gives each cursor one line of the clipboard, which is the
// other half of a multi-cursor copy: select three names, copy, make three
// cursors elsewhere, paste, and each name lands at its own cursor.
//
// Cursors are edited highest-offset-first so earlier ones stay valid, and the
// whole thing is one undo step. Total bytes stored is the clipboard once —
// each cursor takes a different slice — unlike inserting the whole clipboard at
// every cursor, which stores it N times.
func (p *Pane) PasteDistributed(lines []string) {
	cursors := p.Cursors.All()
	if len(lines) != len(cursors) {
		p.Paste(strings.Join(lines, "\n"))
		return
	}
	p.File.Begin()
	defer p.File.End()

	for i := len(cursors) - 1; i >= 0; i-- {
		lo, hi := cursors[i].Range()
		if hi > lo {
			p.File.Delete(p.Author, lo, hi-lo)
			p.Cursors.Shift(lo, hi-lo, 0)
		}
		p.File.Insert(p.Author, lo, lines[i])
		p.Cursors.Shift(lo, 0, len(lines[i]))
		p.bumpCursorsAt(lo, len(lines[i]))
	}
	p.Cursors.CollapseSelections()
	p.Cursors.Normalize()
	p.FollowCursor()
}

// DeleteBackward is backspace: remove the selection, or the character before
// the cursor.
func (p *Pane) DeleteBackward() {
	p.editEachCursor(func(c Cursor) (int, int, string) {
		if c.HasSelection() {
			lo, hi := c.Range()
			return lo, hi - lo, ""
		}
		if c.Head == 0 {
			return c.Head, 0, ""
		}
		prev := p.prevBoundary(c.Head)
		return prev, c.Head - prev, ""
	})
}

// DeleteForward is the delete key.
func (p *Pane) DeleteForward() {
	p.editEachCursor(func(c Cursor) (int, int, string) {
		if c.HasSelection() {
			lo, hi := c.Range()
			return lo, hi - lo, ""
		}
		if c.Head >= p.File.Len() {
			return c.Head, 0, ""
		}
		return c.Head, p.nextBoundary(c.Head) - c.Head, ""
	})
}

// DeleteLine removes the whole line each cursor sits on.
func (p *Pane) DeleteLine() {
	p.editEachCursor(func(c Cursor) (int, int, string) {
		line := p.File.LineOf(c.Head)
		start := p.File.LineStart(line)
		end := p.File.LineEnd(line)
		if end < p.File.Len() {
			end++ // take the newline too
		} else if start > 0 {
			start-- // last line: take the preceding newline instead
		}
		return start, end - start, ""
	})
}

// OpenLineBelow inserts a newline after the current line and moves there,
// regardless of where in the line the cursor sits.
func (p *Pane) OpenLineBelow() {
	p.editEachCursor(func(c Cursor) (int, int, string) {
		return p.File.LineEnd(p.File.LineOf(c.Head)), 0, "\n"
	})
	p.MoveTo(func(c Cursor, f *File) int { return c.Head }, false)
}

// OpenLineAbove inserts a newline before the current line.
func (p *Pane) OpenLineAbove() {
	p.editEachCursor(func(c Cursor) (int, int, string) {
		return p.File.LineStart(p.File.LineOf(c.Head)), 0, "\n"
	})
}

// Indent adds one indent unit to every line touched by a cursor or selection.
func (p *Pane) Indent() { p.reindent(true) }

// Outdent removes one indent unit from every touched line.
func (p *Pane) Outdent() { p.reindent(false) }

func (p *Pane) reindent(add bool) {
	unit := strings.Repeat(" ", p.File.Cols.Tab)
	lines := p.touchedLines()
	// Bottom-up: editing a later line cannot invalidate an earlier line's start.
	for i := len(lines) - 1; i >= 0; i-- {
		start := p.File.LineStart(lines[i])
		text := p.File.Line(lines[i])
		if add {
			p.applyEdit(start, 0, unit)
			continue
		}
		if n := leadingSpaces(text, p.File.Cols.Tab); n > 0 {
			p.applyEdit(start, n, "")
		}
	}
	p.Cursors.Normalize()
}

// touchedLines is every line any cursor or selection covers, ascending.
func (p *Pane) touchedLines() []int {
	seen := map[int]bool{}
	var out []int
	for _, c := range p.Cursors.All() {
		lo, hi := c.Range()
		for line := p.File.LineOf(lo); line <= p.File.LineOf(hi); line++ {
			if !seen[line] {
				seen[line] = true
				out = append(out, line)
			}
		}
	}
	sortInts(out)
	return out
}

// leadingSpaces is how much indentation to strip: up to one tab width of
// spaces, or a single literal tab.
func leadingSpaces(line string, tab int) int {
	if strings.HasPrefix(line, "\t") {
		return 1
	}
	n := 0
	for n < len(line) && n < tab && line[n] == ' ' {
		n++
	}
	return n
}

// editEachCursor applies one edit per cursor, highest offset first so that
// earlier cursors' positions remain valid throughout.
func (p *Pane) editEachCursor(fn func(Cursor) (pos, remove int, insert string)) {
	cursors := p.Cursors.All()
	for i := len(cursors) - 1; i >= 0; i-- {
		pos, remove, insert := fn(cursors[i])
		if remove == 0 && insert == "" {
			continue
		}
		p.applyEdit(pos, remove, insert)
	}
	p.Cursors.CollapseSelections()
	p.Cursors.Normalize()
}

// applyEdit performs one replacement and shifts every cursor to match.
func (p *Pane) applyEdit(pos, remove int, insert string) {
	if remove > 0 {
		p.File.Delete(p.Author, pos, remove)
		p.Cursors.Shift(pos, remove, 0)
	}
	if insert != "" {
		p.File.Insert(p.Author, pos, insert)
		p.Cursors.Shift(pos, 0, len(insert))
		p.bumpCursorsAt(pos, len(insert))
	}
}

// bumpCursorsAt moves cursors sitting exactly at an insertion point to the end
// of the inserted text. Shift deliberately leaves them put — that is right for
// a cursor elsewhere in the document watching text appear before it, and wrong
// for the cursor that did the typing.
func (p *Pane) bumpCursorsAt(pos, n int) {
	for i, c := range p.Cursors.All() {
		if c.Head == pos {
			p.Cursors.list[i].Head = pos + n
		}
		if c.Anchor == pos {
			p.Cursors.list[i].Anchor = pos + n
		}
	}
}

func (p *Pane) prevBoundary(off int) int {
	for off > 0 {
		off--
		if p.File.Slice(off, 1)[0]&0xC0 != 0x80 {
			return off
		}
	}
	return 0
}

func (p *Pane) nextBoundary(off int) int {
	n := p.File.Len()
	for off < n {
		off++
		if off >= n || p.File.Slice(off, 1)[0]&0xC0 != 0x80 {
			return off
		}
	}
	return n
}

func sortInts(a []int) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j] < a[j-1]; j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
}
