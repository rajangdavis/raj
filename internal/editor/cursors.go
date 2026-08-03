package editor

import "sort"

// Cursor is one caret with a selection. Head is where the caret sits and where
// typing happens; Anchor is the other end of the selection, equal to Head when
// nothing is selected.
//
// Goal is the display column the cursor wants when moving vertically. Without
// it, arrowing down through a short line and back up lands you at the short
// line's end instead of where you started — the single most noticeable cursor
// bug an editor can have.
type Cursor struct {
	Head, Anchor int
	Goal         int
}

// HasSelection reports whether anything is selected.
func (c Cursor) HasSelection() bool { return c.Head != c.Anchor }

// Range returns the selection in ascending order.
func (c Cursor) Range() (int, int) {
	if c.Head < c.Anchor {
		return c.Head, c.Anchor
	}
	return c.Anchor, c.Head
}

// Cursors is the multi-cursor set, kept sorted by Head with the primary cursor
// tracked by identity rather than index so merging cannot silently move it.
type Cursors struct {
	list    []Cursor
	primary int
}

// NewCursors starts with a single cursor at offset 0.
func NewCursors() *Cursors { return &Cursors{list: []Cursor{{}}} }

func (cs *Cursors) All() []Cursor { return cs.list }
func (cs *Cursors) Count() int    { return len(cs.list) }

// Primary is the cursor the viewport follows and the status line reports.
func (cs *Cursors) Primary() Cursor {
	if cs.primary >= len(cs.list) {
		cs.primary = len(cs.list) - 1
	}
	if cs.primary < 0 {
		return Cursor{}
	}
	return cs.list[cs.primary]
}

// Set collapses to a single cursor, the usual result of clicking or of any
// action that is not explicitly multi-cursor.
func (cs *Cursors) Set(head, anchor int) {
	cs.list = []Cursor{{Head: head, Anchor: anchor}}
	cs.primary = 0
}

// Add introduces another cursor, merging it if it coincides with one already
// present.
func (cs *Cursors) Add(head, anchor int) {
	cs.list = append(cs.list, Cursor{Head: head, Anchor: anchor})
	cs.Normalize()
}

// Replace swaps in a whole cursor set, for actions that recompute every cursor
// rather than moving them individually.
func (cs *Cursors) Replace(list []Cursor) {
	if len(list) == 0 {
		return
	}
	cs.list = list
	cs.primary = 0
	cs.Normalize()
}

// Clear collapses to just the primary cursor, dropping its selection. This is
// escape's job, and it must always be reachable — a stuck multi-cursor state
// with no way out is the fastest way to make an editor feel broken.
func (cs *Cursors) Clear() {
	p := cs.Primary()
	p.Anchor = p.Head
	cs.list = []Cursor{p}
	cs.primary = 0
}

// CollapseSelections drops selections but keeps every cursor.
func (cs *Cursors) CollapseSelections() {
	for i := range cs.list {
		cs.list[i].Anchor = cs.list[i].Head
	}
}

// Normalize sorts the set and merges cursors that have collided. Multi-cursor
// editing constantly makes cursors converge — deleting the text between two of
// them, for instance — and leaving duplicates means every subsequent keystroke
// is applied twice at the same place.
func (cs *Cursors) Normalize() {
	if len(cs.list) < 2 {
		return
	}
	primary := cs.Primary()
	sort.SliceStable(cs.list, func(i, j int) bool {
		a, b := cs.list[i], cs.list[j]
		if a.Head != b.Head {
			return a.Head < b.Head
		}
		return a.Anchor < b.Anchor
	})

	out := cs.list[:1]
	for _, c := range cs.list[1:] {
		last := &out[len(out)-1]
		if overlaps(*last, c) {
			last.Head, last.Anchor = merge(*last, c)
			continue
		}
		out = append(out, c)
	}
	cs.list = out

	cs.primary = 0
	for i, c := range cs.list {
		if lo, hi := c.Range(); primary.Head >= lo && primary.Head <= hi {
			cs.primary = i
			break
		}
	}
}

// Apply mutates every cursor through fn, then renormalises. Movement actions
// use this so they never have to think about ordering or collisions.
func (cs *Cursors) Apply(fn func(Cursor) Cursor) {
	for i := range cs.list {
		cs.list[i] = fn(cs.list[i])
	}
	cs.Normalize()
}

// Shift moves every cursor to account for an edit, so cursors other than the
// one that made the edit stay on the text they were pointing at.
func (cs *Cursors) Shift(pos, removed, added int) {
	for i := range cs.list {
		cs.list[i].Head = shiftOffset(cs.list[i].Head, pos, removed, added)
		cs.list[i].Anchor = shiftOffset(cs.list[i].Anchor, pos, removed, added)
	}
	cs.Normalize()
}

func shiftOffset(off, pos, removed, added int) int {
	switch {
	case off <= pos:
		return off
	case off >= pos+removed:
		return off - removed + added
	default:
		return pos // inside the deleted span: collapse to its start
	}
}

func overlaps(a, b Cursor) bool {
	alo, ahi := a.Range()
	blo, bhi := b.Range()
	return blo <= ahi && alo <= bhi
}

// merge combines two overlapping cursors, preserving the direction of the one
// that reaches furthest so shift-selecting through an existing selection keeps
// growing in the direction you are dragging.
func merge(a, b Cursor) (head, anchor int) {
	alo, ahi := a.Range()
	blo, bhi := b.Range()
	lo, hi := min(alo, blo), max(ahi, bhi)
	if a.Head >= a.Anchor {
		return hi, lo
	}
	return lo, hi
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
