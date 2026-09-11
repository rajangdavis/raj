package view

import "raj/internal/ui"

// TabWidth is the default indent size. Configurable per buffer; two spaces is
// raj's default.
const TabWidth = 2

// Columns maps between byte offsets within a line and display columns.
//
// Three different notions of "position" have to stay distinct or the cursor
// drifts: the byte offset (what the buffer indexes), the rune index (what
// nobody actually wants), and the display column (where the terminal draws).
// Tabs and wide characters make the last two disagree with the first, so every
// conversion goes through here rather than being open-coded per pane.
type Columns struct {
	Tab int // columns a tab advances to the next multiple of
}

// HintCol is a view-only inlay hint: display columns that are not part of the
// line text. Off is the line-relative byte offset the hint is anchored at, and
// Width is the display width it occupies, already including any padding. Hints
// in a slice arrive sorted by Off ascending.
type HintCol struct {
	Off   int
	Width int
}

// NewColumns returns a mapper; tab <= 0 uses TabWidth.
func NewColumns(tab int) Columns {
	if tab <= 0 {
		tab = TabWidth
	}
	return Columns{Tab: tab}
}

// Width is the display width of a line. With no hints it is the plain width;
// WidthHints is the same conversion with hint cells counted.
func (c Columns) Width(line string) int {
	return c.WidthHints(line, nil)
}

// WidthHints is the display width of a line once every hint in hints is drawn:
// the base width plus the widths of all hints. Unlike ColOfHints, a hint
// anchored at end of line is included, because there is no later offset it
// could be strictly before.
func (c Columns) WidthHints(line string, hints []HintCol) int {
	w := c.colOf(line, len(line))
	for _, h := range hints {
		w += h.Width
	}
	return w
}

// ColOf converts a byte offset within a line to a display column. With no hints
// it is the un-hinted conversion; ColOfHints carries the hint arithmetic.
func (c Columns) ColOf(line string, off int) int {
	return c.ColOfHints(line, off, nil)
}

// ColOfHints converts a byte offset within a line to the display column at
// which that boundary is drawn once hints are interleaved.
//
// A hint anchored at offset Off is drawn starting at the boundary Off, and the
// caret for that boundary sits BEFORE the hint. So the result is the base
// column of off plus the widths of hints anchored STRICTLY BEFORE off: a hint
// at exactly off starts at this column and is not counted; a hint before it
// pushes the column right.
func (c Columns) ColOfHints(line string, off int, hints []HintCol) int {
	col := c.colOf(line, off)
	for _, h := range hints {
		if h.Off >= off {
			break
		}
		col += h.Width
	}
	return col
}

// OffsetOf converts a display column back to a byte offset within a line,
// clamping to the line's end. A column landing inside a tab or a wide rune
// resolves to that character's start, so the cursor never sits mid-glyph. With
// no hints it is the un-hinted conversion; OffsetOfHints carries the hint
// arithmetic.
func (c Columns) OffsetOf(line string, col int) int {
	return c.OffsetOfHints(line, col, nil)
}

// OffsetOfHints converts a display column back to a byte offset within a line
// once hints are interleaved.
//
// Hints are walked in order accumulating their widths. A column inside a hint's
// span [start, start+Width) resolves to that hint's anchor Off, never to a
// byte, because a hint has none. A column at or before a hint's start stops the
// walk, and the widths accumulated so far are subtracted before the remainder
// is resolved against the line with offsetOf. A zero-width hint has an empty
// span, so it adds nothing and a column at its anchor still resolves to its Off.
func (c Columns) OffsetOfHints(line string, col int, hints []HintCol) int {
	shift := 0
	for _, h := range hints {
		start := c.colOf(line, h.Off) + shift
		if col < start {
			break
		}
		if col < start+h.Width {
			return h.Off
		}
		shift += h.Width
	}
	return c.offsetOf(line, col-shift)
}

// colOf is the un-hinted offset-to-column conversion; ColOf and ColOfHints
// both build on it so there is one loop.
func (c Columns) colOf(line string, off int) int {
	col := 0
	for i, r := range line {
		if i >= off {
			break
		}
		col += c.advance(r, col)
	}
	return col
}

// offsetOf is the un-hinted column-to-offset conversion; OffsetOf and
// OffsetOfHints both build on it.
func (c Columns) offsetOf(line string, col int) int {
	cur := 0
	for i, r := range line {
		if cur >= col {
			return i
		}
		next := cur + c.advance(r, cur)
		if next > col {
			return i // col falls inside this character
		}
		cur = next
	}
	return len(line)
}

// Expand renders a line for display, replacing tabs with the spaces they
// advance to. Returns the expanded text and, for each display column, the byte
// offset of the character occupying it — which is what the renderer needs to
// paint author tints against the original byte ranges.
func (c Columns) Expand(line string) (string, []int) {
	out := make([]rune, 0, len(line)+8)
	offs := make([]int, 0, len(line)+8)
	col := 0
	for i, r := range line {
		w := c.advance(r, col)
		if r == '\t' {
			for k := 0; k < w; k++ {
				out = append(out, ' ')
				offs = append(offs, i)
			}
		} else {
			out = append(out, r)
			offs = append(offs, i)
			for k := 1; k < w; k++ {
				offs = append(offs, i) // continuation column of a wide rune
			}
		}
		col += w
	}
	return string(out), offs
}

// advance is how many columns a rune occupies starting at column col. Tabs are
// elastic: they advance to the next tab stop rather than a fixed width.
func (c Columns) advance(r rune, col int) int {
	if r == '\t' {
		return c.Tab - col%c.Tab
	}
	if w := ui.RuneWidth(r); w > 0 {
		return w
	}
	return 0
}
