// Package view turns a byte-oriented buffer into something with lines,
// columns, and a visible window. It holds no terminal state and does no
// rendering, so all of it tests without a screen.
package view

import (
	"sort"
	"strings"
)

// Index maps between byte offsets and line numbers.
//
// It stores the byte offset of every line start and maintains that slice
// incrementally rather than rescanning the document. An edit shifts the tail of
// the slice, which is a memmove of a few hundred kilobytes on a 40k-line file —
// tens of microseconds, comfortably inside a frame — where a rescan would be a
// walk of the whole piece tree on every keystroke.
//
// If that ever stops being fast enough, the upgrade is a Fenwick tree over line
// lengths, which makes the shift O(log n). The benchmark harness already has
// one. It is not worth the complexity until profiling says so.
type Index struct {
	starts []int // starts[0] is always 0; ascending, one entry per line
}

// NewIndex builds an index over the whole document text.
func NewIndex(text string) *Index {
	ix := &Index{starts: []int{0}}
	for i := 0; i < len(text); i++ {
		if text[i] == '\n' {
			ix.starts = append(ix.starts, i+1)
		}
	}
	return ix
}

// Lines is the number of lines. A document always has at least one, and text
// ending in a newline has a final empty line — matching what an editor shows.
func (ix *Index) Lines() int { return len(ix.starts) }

// LineStart is the byte offset where a line begins. Out-of-range lines clamp.
func (ix *Index) LineStart(line int) int {
	if line < 0 {
		return 0
	}
	if line >= len(ix.starts) {
		return ix.starts[len(ix.starts)-1]
	}
	return ix.starts[line]
}

// LineOf reports which line an offset falls on.
func (ix *Index) LineOf(off int) int {
	if off <= 0 {
		return 0
	}
	// The first start strictly greater than off belongs to the next line.
	i := sort.SearchInts(ix.starts, off+1)
	return i - 1
}

// LineEnd is the offset just past a line's last byte, excluding its newline.
// total is the document length, needed for the final line.
func (ix *Index) LineEnd(line, total int) int {
	if line+1 < len(ix.starts) {
		return ix.starts[line+1] - 1 // drop the newline
	}
	return total
}

// LineLen is a line's length in bytes, excluding its newline.
func (ix *Index) LineLen(line, total int) int {
	return ix.LineEnd(line, total) - ix.LineStart(line)
}

// Insert updates the index for text inserted at pos.
//
// Starts strictly after pos shift right; a start exactly at pos does not,
// because text inserted at a line's first byte belongs to that line rather than
// pushing the line marker along.
func (ix *Index) Insert(pos int, text string) {
	if text == "" {
		return
	}
	n := len(text)
	first := sort.SearchInts(ix.starts, pos+1)
	for i := first; i < len(ix.starts); i++ {
		ix.starts[i] += n
	}
	if !strings.Contains(text, "\n") {
		return
	}
	var added []int
	for i := 0; i < n; i++ {
		if text[i] == '\n' {
			added = append(added, pos+i+1)
		}
	}
	ix.splice(first, added)
}

// InsertLen is Insert for a caller that has already found the newlines and does
// not want to materialise the text to do it. starts holds the document offset of
// each line begun by the insertion — that is, one past each newline — in
// ascending order.
//
// The distinction matters on a large paste: reading the inserted span back as a
// string to count its newlines allocates a full copy of it, and the copy is
// discarded immediately. The buffer can be scanned in place instead.
func (ix *Index) InsertLen(pos, n int, starts []int) {
	if n == 0 {
		return
	}
	first := sort.SearchInts(ix.starts, pos+1)
	for i := first; i < len(ix.starts); i++ {
		ix.starts[i] += n
	}
	ix.splice(first, starts)
}

// splice inserts already-shifted line starts at position first, keeping the
// slice sorted.
func (ix *Index) splice(first int, added []int) {
	if len(added) == 0 {
		return
	}
	ix.starts = append(ix.starts, added...)
	copy(ix.starts[first+len(added):], ix.starts[first:len(ix.starts)-len(added)])
	copy(ix.starts[first:], added)
}

// Delete updates the index for length bytes removed at pos.
//
// It needs no knowledge of the deleted text: the lines that disappeared are
// exactly the starts falling in (pos, pos+length], which the index already
// knows. Avoiding that lookup matters — reading the deleted text would mean
// copying it out of the buffer purely to count newlines.
func (ix *Index) Delete(pos, length int) {
	if length <= 0 {
		return
	}
	end := pos + length
	from := sort.SearchInts(ix.starts, pos+1)
	to := sort.SearchInts(ix.starts, end+1)
	if to > from {
		ix.starts = append(ix.starts[:from], ix.starts[to:]...)
	}
	for i := from; i < len(ix.starts); i++ {
		ix.starts[i] -= length
	}
}

// Rebuild discards the incremental state and reindexes from scratch. Used after
// a wholesale replacement — reload from disk, session restore — where replaying
// individual edits would be slower and more fragile than a single scan.
func (ix *Index) Rebuild(text string) { *ix = *NewIndex(text) }
