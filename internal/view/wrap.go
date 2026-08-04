package view

import (
	"unicode/utf8"

	"raj/internal/ui"
)

// Wrapping turns one document line into one or more visual rows.
//
// The policy question is only which positions count as a break opportunity; the
// scan is identical in all three cases. Character wrap allows every position,
// word wrap only whitespace, hybrid whitespace plus the separators that occur
// in code.
//
// Hybrid is the default and the only one worth shipping. Plain word wrap
// produces byte-identical output to character wrap on minified output and long
// paths, because those lines contain no whitespace at all — so it degrades to
// breaking mid-token on exactly the code where breaking well matters most.
// Character wrap does that everywhere. Hybrid breaks mid-token only when a
// single token is wider than the pane, which is the case nothing can help.
type BreakPolicy func(rune) bool

// BreakAnywhere is character wrapping.
func BreakAnywhere(rune) bool { return true }

// BreakOnSpace is classic word wrapping.
func BreakOnSpace(r rune) bool { return r == ' ' || r == '\t' }

// BreakHybrid is word wrapping extended with the punctuation that separates
// code, so a path or a call chain breaks at a boundary a reader expects.
func BreakHybrid(r rune) bool {
	switch r {
	case ' ', '\t', '/', '.', ',', ';', ':', '-', '_', ')', ']', '}', '>', '=', '|', '&', '+':
		return true
	}
	return false
}

// WrapRows is the visual row count of a line, without materialising the breaks.
// Always at least one, so an empty line still occupies a row.
//
// Most lines a viewport touches are counted and never drawn — scrolling past
// them, or locating the cursor — so this path must not allocate.
func (c Columns) WrapRows(s string, w int, opp BreakPolicy) int {
	return c.wrap(s, w, opp, nil)
}

// AppendWrap appends the byte offsets at which each new visual row begins, and
// returns the slice. The first row starts at 0 and is not listed, so the row
// count is len(result)+1.
//
// Takes a destination so the render path can reuse one buffer across lines and
// frames. Appending fresh cost 1016 bytes and 7 allocations on a 63-row line,
// and a frame draws fifty lines.
func (c Columns) AppendWrap(dst []int, s string, w int, opp BreakPolicy) []int {
	c.wrap(s, w, opp, &dst)
	return dst
}

// wrap is the single implementation; out may be nil to count only. One walk
// rather than a counter and a break finder, because two implementations of the
// same rule drift and the viewport needs them to agree exactly — otherwise the
// caret lands on a row the renderer never drew.
func (c Columns) wrap(s string, w int, opp BreakPolicy, out *[]int) int {
	if w < 1 || s == "" {
		return 1
	}
	rows, col := 1, 0
	// oppAt is the byte offset just past the last break opportunity still
	// available on the current row; -1 once the row has none left to use.
	oppAt, oppCol := -1, 0

	for i, r := range s {
		// A loop, not a branch. Retreating to a break opportunity does not
		// guarantee the rune now fits: a tab's width is elastic, so moving it
		// to a new column can make it WIDER. " 0\t" at width 2 with tab 4
		// retreats to after the space, recomputes the tab as three columns and
		// produces a row four columns wide. Found by fuzzing, not by reading.
		//
		// col == 0 terminates it: a rune too wide for the pane gets a row of
		// its own rather than looping forever.
		rw := c.runeCols(r, col)
		for col+rw > w && col > 0 {
			var at int
			if oppAt > 0 && oppAt <= i {
				at = oppAt
				col = c.colOfFrom(s[oppAt:i], oppCol)
			} else {
				// No usable opportunity on this row: break where we stand, or
				// an overlong token never advances. Minified output, base64
				// and deep paths all reach this.
				at, col = i, 0
			}
			if out != nil {
				*out = append(*out, at)
			}
			rows++
			oppAt, oppCol = -1, 0
			rw = c.runeCols(r, col)
		}
		col += rw
		if opp(r) {
			oppAt, oppCol = i+utf8.RuneLen(r), 0
		}
	}
	return rows
}

// runeCols is the width of a rune starting at column col. Tabs are elastic and
// advance to the next stop, which is why width cannot be decided per rune in
// isolation.
func (c Columns) runeCols(r rune, col int) int {
	if r == '\t' {
		tab := c.Tab
		if tab < 1 {
			tab = 1
		}
		return tab - col%tab
	}
	return ui.RuneWidth(r)
}

func (c Columns) colOfFrom(s string, start int) int {
	col := start
	for _, r := range s {
		col += c.runeCols(r, col)
	}
	return col
}

// RowOfBreaks reports which visual row an offset falls on, and the display
// column within that row, given a layout already computed by AppendWrap.
//
// Takes the breaks rather than computing them: the caller that needs this — the
// renderer placing the caret, the viewport deciding whether the cursor is on
// screen — has just laid the line out to draw it. An earlier version laid it
// out again per call and cost 5 us a time, which is per-frame and per-keystroke
// work to recover something already in hand.
func (c Columns) RowOfBreaks(breaks []int, s string, off int) (row, col int) {
	if off > len(s) {
		off = len(s)
	}
	start := 0
	for _, b := range breaks {
		if b > off {
			break
		}
		row++
		start = b
	}
	return row, c.colOfFrom(s[start:off], 0)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// OffsetAtRow is the inverse of RowOfBreaks: the byte offset in s at the given
// visual row and display column, clamped to that row's extent.
//
// Vertical motion needs this. A cursor is a byte offset, but moving down a
// wrapped line means moving one visual row and landing at the same display
// column, which is a position the offset alone cannot express.
func (c Columns) OffsetAtRow(breaks []int, s string, row, col int) int {
	lo, hi := RowBounds(breaks, len(s), row)
	return lo + c.OffsetOf(s[lo:hi], col)
}

// RowBounds is the byte range of one visual row, clamped to the line.
func RowBounds(breaks []int, length, row int) (lo, hi int) {
	if row < 0 {
		row = 0
	}
	if row > len(breaks) {
		row = len(breaks)
	}
	if row > 0 {
		lo = breaks[row-1]
	}
	hi = length
	if row < len(breaks) {
		hi = breaks[row]
	}
	return lo, hi
}
