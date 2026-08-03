package editor

import (
	"strconv"

	"raj/internal/piecetable"
	"raj/internal/syntax"
	"raj/internal/ui"
)

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

	for row := 0; row < h; row++ {
		line := p.Viewport.Top + row
		if line >= p.File.Lines() {
			break
		}
		p.drawGutter(s, x, y+row, gut, line, curLine, th)
		p.drawLine(s, x+gut, y+row, textW, line, sel, heads, th)
	}
	if focused {
		p.placeCaret(s, x+gut, y, textW, h)
	}
}

// placeCaret puts the terminal caret at the primary cursor, or hides it when
// that cursor has scrolled out of view.
func (p *Pane) placeCaret(s *ui.Screen, x, y, w, h int) {
	line, col := p.File.LineCol(p.Cursors.Primary().Head)
	row := line - p.Viewport.Top
	sx := x + col - p.Viewport.Left
	if row < 0 || row >= h || sx < x || sx >= x+w {
		return
	}
	s.SetCursor(sx, y+row)
}

func (p *Pane) drawGutter(s *ui.Screen, x, y, w, line, curLine int, th Theme) {
	style := th.Gutter
	if line == curLine {
		style = th.GutterCur
	}
	num := strconv.Itoa(line + 1)
	s.Fill(x, y, w, 1, style)
	s.SetString(x+w-1-len(num), y, num, style, len(num))
}

// drawLine renders one document line, expanding tabs and applying selection
// and authorship colours per display column.
func (p *Pane) drawLine(s *ui.Screen, x, y, w, line int, sel [][2]int, heads map[int]bool, th Theme) {
	start := p.File.LineStart(line)
	text := p.File.Line(line)
	expanded, offs := p.File.Cols.Expand(text)
	runes := []rune(expanded)

	tints := p.authorTints(start, len(text), th)
	tokens := p.File.Syntax.Line(line)

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
		if st, ok := syntax.StyleAt(tokens, offs[i]); ok {
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
			style = style.Plus(ui.Reverse)
		}
		s.Set(screenX, y, r, style)
		col++
	}

	// A secondary cursor at end of line has no character to invert, so draw it
	// on the blank cell past the text.
	endCol := p.File.Cols.Width(text)
	if heads[start+len(text)] && endCol >= p.Viewport.Left {
		if sx := x + endCol - p.Viewport.Left; sx < x+w {
			s.Set(sx, y, ' ', th.Text.Plus(ui.Reverse))
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
