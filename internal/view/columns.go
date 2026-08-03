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

// NewColumns returns a mapper; tab <= 0 uses TabWidth.
func NewColumns(tab int) Columns {
	if tab <= 0 {
		tab = TabWidth
	}
	return Columns{Tab: tab}
}

// Width is the display width of a line.
func (c Columns) Width(line string) int {
	return c.ColOf(line, len(line))
}

// ColOf converts a byte offset within a line to a display column.
func (c Columns) ColOf(line string, off int) int {
	col := 0
	for i, r := range line {
		if i >= off {
			break
		}
		col += c.advance(r, col)
	}
	return col
}

// OffsetOf converts a display column back to a byte offset within a line,
// clamping to the line's end. A column landing inside a tab or a wide rune
// resolves to that character's start, so the cursor never sits mid-glyph.
func (c Columns) OffsetOf(line string, col int) int {
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
