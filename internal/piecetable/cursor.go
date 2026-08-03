package piecetable

import "sort"

// Cursors (a.k.a. anchors or marks) are the editor-facing reason the position
// index exists. A cursor is a document byte offset that must (a) resolve to a
// concrete (buffer, offset) location, and (b) survive edits — text inserted
// before a cursor pushes it right; text deleted around it pulls it left.
//
// This layer connects the structures we built:
//   - RESOLVE a cursor -> (buffer, offset) is exactly PieceBTree.At(offset):
//     one O(log n) descent through the order-statistics index.
//   - SHIFT cursors on edit is an order-statistics problem on the cursors
//     themselves: find the first cursor at/after the edit point (binary search
//     on sorted offsets) and adjust the tail.
//   - MULTI-CURSOR editing is the multi-edit batch workload: N cursors each
//     insert/delete at once, which is precisely where the flat buffer's single
//     sequential batch pass beats N independent tree descents. ApplyEditsBatch
//     shifts all cursors and reports the sorted edit list to hand to
//     FlatPieces.ApplyBatchInserts.
//
// Gravity: an insert exactly AT a cursor pushes the cursor right (left-gravity
// anchor); a delete covering a cursor collapses it to the deletion start.

type CursorSet struct {
	offs []int // sorted ascending document byte offsets
}

func NewCursorSet(offsets ...int) *CursorSet {
	c := &CursorSet{offs: append([]int(nil), offsets...)}
	sort.Ints(c.offs)
	return c
}

func (c *CursorSet) Offsets() []int { return c.offs }

// Add inserts a cursor at offset, keeping the set sorted.
func (c *CursorSet) Add(off int) {
	i := sort.SearchInts(c.offs, off)
	c.offs = append(c.offs, 0)
	copy(c.offs[i+1:], c.offs[i:])
	c.offs[i] = off
}

// Resolve maps a cursor's document offset to a concrete (buffer, offset)
// location via the position index — one O(log n) descent.
func (c *CursorSet) Resolve(pt *PieceBTree, cursor int) (buf, bufOffset int) {
	return pt.At(cursor)
}

// ShiftInsert accounts for `length` bytes inserted at document position `pos`:
// every cursor at or after pos moves right by length. O(log C + affected).
func (c *CursorSet) ShiftInsert(pos, length int) {
	i := sort.SearchInts(c.offs, pos)
	for ; i < len(c.offs); i++ {
		c.offs[i] += length
	}
}

// ShiftDelete accounts for `length` bytes deleted starting at `pos`: cursors
// inside the deleted span collapse to pos; cursors past it move left by length.
func (c *CursorSet) ShiftDelete(pos, length int) {
	end := pos + length
	for i := sort.SearchInts(c.offs, pos); i < len(c.offs); i++ {
		if c.offs[i] < end {
			c.offs[i] = pos
		} else {
			c.offs[i] -= length
		}
	}
}

// Edit is one queued edit at a document offset (insert len>0 / delete len<0).
type Edit struct {
	At     int // document byte offset
	Length int // bytes inserted (>0) or deleted (<0)
}

// ApplyInsertsBatch shifts every cursor for a whole batch of simultaneous
// insertions in a single merge pass (O(C + E) after sorting). Edit positions
// are in ORIGINAL document coordinates and applied together: a cursor at offset
// off moves right by the total length of all insertions at positions <= off
// (left gravity). This is the multi-cursor analogue of the flat buffer's batch
// memmove — many simultaneous inserts resolved in one sweep instead of
// re-walking the cursor list per edit.
func (c *CursorSet) ApplyInsertsBatch(edits []Edit) {
	if len(edits) == 0 {
		return
	}
	es := append([]Edit(nil), edits...)
	sort.SliceStable(es, func(a, b int) bool { return es[a].At < es[b].At })
	ei, cum := 0, 0
	for i, off := range c.offs {
		for ei < len(es) && es[ei].At <= off {
			cum += es[ei].Length
			ei++
		}
		c.offs[i] = off + cum
	}
}
