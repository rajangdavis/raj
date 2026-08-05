package editor

import (
	"strconv"

	"raj/internal/piecetable"
	"raj/internal/syntax"
	"raj/internal/ui"
	"raj/internal/view"
)

// How a secondary cursor is drawn, arrived at by elimination. A cell holds one
// rune, so anything drawn AS the caret is drawn INSTEAD of the character: a bar
// glyph (U+258F) reads perfectly as an insertion point but rendered "Paste" as
// "Pa|te". Reverse video keeps the glyph but reads as a selection, which is the
// one thing a caret is not. Underline keeps the glyph and is unambiguous, but a
// one-pixel line is the least visible mark a cell can carry, and finding four
// carets in a screen of text is the whole job.
//
// So: a block of its own colour with the character still in it, dark text on a
// light cell. Visible at a glance, and not the selection grey or either find
// colour. The two theme fields are the only thing to change to try something
// else; the tests assert the character survives rather than the exact styling.

// Theme is the palette the pane draws with. Anything left as ui.Default
// inherits the host terminal's own colour, which is how raj picks up a Ghostty
// theme without being told about it.
type Theme struct {
	Text       ui.Style
	Gutter     ui.Style
	GutterCur  ui.Style
	Selection  ui.Style
	AgentTint  ui.Color
	CursorLine ui.Style
	FindMatch  ui.Style
	FindActive ui.Style

	// Caret is the block a secondary cursor draws, CaretText the character
	// left sitting in it.
	Caret     ui.Color
	CaretText ui.Color
}

// DefaultTheme names as little as possible: the terminal's foreground and
// background carry the document, and only the gutter, selection, and agent
// tint are given explicit colours.
func DefaultTheme() Theme {
	return Theme{
		Text:       ui.DefaultStyle,
		Gutter:     ui.DefaultStyle.With(ui.Ansi(8)),
		GutterCur:  ui.DefaultStyle.With(ui.Ansi(7)),
		Selection:  ui.DefaultStyle.On(ui.Ansi(238)),
		AgentTint:  ui.Ansi(22),
		FindMatch:  ui.DefaultStyle.On(ui.Ansi(58)),
		FindActive: ui.DefaultStyle.On(ui.Ansi(136)),
		Caret:      ui.Ansi(12),
		CaretText:  ui.Ansi(0),
	}
}

// GutterWidth is the space reserved for line numbers, sized to the document.
func (p *Pane) GutterWidth() int {
	return len(strconv.Itoa(p.File.Lines())) + 2
}

// Render draws the pane into a rectangle of the screen.
//
// It draws only the visible lines, and asks the buffer only for the visible
// byte range, so cost is proportional to the window rather than the document —
// a 50 MB file renders in the same time as a 50-line one.
func (p *Pane) Render(s *ui.Screen, x, y, w, h int, th Theme) {
	p.RenderFocused(s, x, y, w, h, th, true)
}

// RenderFocused draws the pane, showing the cursor only when it has focus. A
// pane that still shows a block cursor after focus has moved to the sidebar
// looks like it is the one receiving keystrokes.
func (p *Pane) RenderFocused(s *ui.Screen, x, y, w, h int, th Theme, focused bool) {
	p.focused = focused
	p.Resize(w-p.GutterWidth(), h)
	gut := p.GutterWidth()
	textW := w - gut
	if textW < 1 {
		return
	}

	sel := p.selectionRanges()
	// Only secondary cursors are drawn as cells. The primary one becomes the
	// terminal's own bar caret, which sits between characters rather than on
	// one, so it is unambiguous where an insertion will land.
	heads := map[int]bool{}
	if focused {
		heads = p.cursorOffsets()
		delete(heads, p.Cursors.Primary().Head)
	}
	curLine := p.File.LineOf(p.Cursors.Primary().Head)

	// Rows, not lines. When wrapping is off every line is one row and this
	// degenerates to the old loop; when it is on, the first line starts above
	// the pane by TopRow so a tall paragraph can be entered partway.
	row := -p.Viewport.TopRow
	for line := p.Viewport.Top; line < p.File.Lines() && row < h; line++ {
		breaks, text := p.lineBreaks(p.wrapBuf, line)
		p.wrapBuf = breaks
		for k := 0; k <= len(breaks) && row < h; k++ {
			lo, hi := view.RowBounds(breaks, len(text), k)
			if row >= 0 {
				// The gutter carries the line number on the first row only;
				// continuation rows get a blank one, so the numbers still count
				// lines rather than rows.
				p.drawGutter(s, x, y+row, gut, line, curLine, th, k == 0)
				p.drawLine(s, x+gut, y+row, textW, line, lo, hi, sel, heads, th)
			}
			row++
		}
	}
	if focused {
		p.placeCaret(s, x+gut, y, textW, h)
	}
}

// placeCaret puts the terminal caret at the primary cursor, or hides it when
// that cursor has scrolled out of view.
func (p *Pane) placeCaret(s *ui.Screen, x, y, w, h int) {
	if p.Wrap {
		p.placeCaretWrapped(s, x, y, w, h)
		return
	}
	line, col := p.File.LineCol(p.Cursors.Primary().Head)
	row := line - p.Viewport.Top
	sx := x + col - p.Viewport.Left
	if row < 0 || row >= h || sx < x || sx >= x+w {
		return
	}
	s.SetCursor(sx, y+row)
}

// placeCaretWrapped counts the rows the lines above the cursor occupy.
//
// Subtracting line numbers, the way the unwrapped path does, puts the caret on
// the cursor line's FIRST row and ignores everything that wrapped above it. The
// caret then sits several rows off from the text it belongs to.
func (p *Pane) placeCaretWrapped(s *ui.Screen, x, y, w, h int) {
	line, within, col := p.cursorRowCol(p.Cursors.Primary().Head)
	if line < p.Viewport.Top {
		return
	}
	row := -p.Viewport.TopRow
	for ln := p.Viewport.Top; ln < line; ln++ {
		row += p.RowsInLine(ln)
		if row >= h {
			return
		}
	}
	row += within
	if row < 0 || row >= h || col >= w {
		return
	}
	s.SetCursor(x+col, y+row)
}

func (p *Pane) drawGutter(s *ui.Screen, x, y, w, line, curLine int, th Theme, number bool) {
	style := th.Gutter
	if line == curLine {
		style = th.GutterCur
	}
	s.Fill(x, y, w, 1, style)
	if !number {
		return // a continuation row: the line already has its number above
	}
	num := strconv.Itoa(line + 1)
	s.SetString(x+w-1-len(num), y, num, style, len(num))
}

// drawLine renders one document line, expanding tabs and applying selection
// and authorship colours per display column.
func (p *Pane) drawLine(s *ui.Screen, x, y, w, line, lo, hi int, sel [][2]int, heads map[int]bool, th Theme) {
	// lo..hi is the byte range of this visual row within the line; without
	// wrapping it is the whole line. Columns are measured from the row's own
	// start, which is also how the wrap engine measures them — the two have to
	// agree or the caret lands a column off after every break.
	start := p.File.LineStart(line) + lo
	full := p.File.Line(line)
	text := full[lo:hi]
	expanded, offs := p.File.Cols.Expand(text)
	runes := []rune(expanded)

	tints := p.authorTints(start, len(text), th)
	tokens := p.File.Syntax.Line(line)
	// Syntax offsets are line-relative, so a continuation row has to shift by
	// where it starts or the colours slide left by the preceding rows' bytes.
	shift := lo

	col := 0
	for i, r := range runes {
		if col < p.Viewport.Left {
			col++
			continue
		}
		screenX := x + col - p.Viewport.Left
		if screenX >= x+w {
			break
		}
		off := start + offs[i]
		style := th.Text
		// Syntax first, then the author tint as a background, then selection,
		// then the cursor. Each layer only overrides what it needs to: the
		// tint keeps the token's foreground, so agent-written code is still
		// syntax-coloured.
		if st, ok := syntax.StyleAt(tokens, shift+offs[i]); ok {
			style = st
		}
		if bg, ok := tints[offs[i]]; ok {
			style = style.On(bg)
		}
		if m, cur := p.Find.Highlight(off); m {
			style = th.FindMatch
			if cur {
				style = th.FindActive
			}
		} else if inAny(sel, off) {
			style = th.Selection
		}
		if heads[off] {
			// The character stays; the cell behind it becomes the caret.
			style = style.On(th.Caret).With(th.CaretText).Plus(ui.Bold)
		}
		s.Set(screenX, y, r, style)
		col++
	}

	// A secondary cursor at end of line has no character to sit on, so the
	// caret is the blank cell past the text.
	endCol := p.File.Cols.Width(text)
	if hi == len(full) && heads[start+len(text)] && endCol >= p.Viewport.Left {
		if sx := x + endCol - p.Viewport.Left; sx < x+w {
			s.Set(sx, y, ' ', th.Text.On(th.Caret).With(th.CaretText))
		}
	}
}

// authorTints maps byte offsets within a line to a background colour, for text
// written by an agent. Original and user text is left untinted so the terminal
// background shows through.
func (p *Pane) authorTints(start, length int, th Theme) map[int]ui.Color {
	out := map[int]ui.Color{}
	if length == 0 {
		return out
	}
	for _, sp := range p.File.Spans(start, length) {
		if !piecetable.Author(sp.Author).IsAgent() {
			continue
		}
		for i := 0; i < sp.Len; i++ {
			out[sp.Off-start+i] = th.AgentTint
		}
	}
	return out
}

func (p *Pane) selectionRanges() [][2]int {
	var out [][2]int
	for _, c := range p.Cursors.All() {
		if c.HasSelection() {
			lo, hi := c.Range()
			out = append(out, [2]int{lo, hi})
		}
	}
	return out
}

func (p *Pane) cursorOffsets() map[int]bool {
	out := map[int]bool{}
	for _, c := range p.Cursors.All() {
		out[c.Head] = true
	}
	return out
}

func inAny(ranges [][2]int, off int) bool {
	for _, r := range ranges {
		if off >= r[0] && off < r[1] {
			return true
		}
	}
	return false
}
