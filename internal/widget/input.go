// Package widget holds the small reusable pieces the panes share: text inputs,
// list selection, and borders. Nothing here knows about files or buffers.
package widget

import (
	"strings"

	"raj/internal/keys"
	"raj/internal/ui"
)

// Input is a single-line text field.
//
// It keeps its own cursor as a byte offset and does its own clipping, so a long
// query scrolls horizontally inside the field rather than overflowing the pane.
type Input struct {
	Label  string
	Text   string
	Cursor int
	// Anchor is the other end of the selection. It equals Cursor when nothing
	// is selected, which makes "has a selection" a comparison rather than a
	// third piece of state that can disagree with the first two.
	Anchor  int
	Focused bool
	scroll  int
}

// Selection returns the selected byte range, lo <= hi. They are equal when
// nothing is selected.
func (in *Input) Selection() (lo, hi int) {
	if in.Anchor < in.Cursor {
		return in.Anchor, in.Cursor
	}
	return in.Cursor, in.Anchor
}

// HasSelection reports whether any text is selected.
func (in *Input) HasSelection() bool { return in.Anchor != in.Cursor }

// SetText replaces the contents and puts the caret at the end, clearing any
// selection. Callers that assign to Text directly would leave Anchor pointing
// into text that no longer exists.
func (in *Input) SetText(text string) {
	in.Text = text
	in.Cursor, in.Anchor = len(text), len(text)
}

// moveTo places the caret, dragging the anchor with it unless extending.
func (in *Input) moveTo(pos int, extend bool) {
	in.Cursor = pos
	if !extend {
		in.Anchor = pos
	}
}

// deleteSelection removes the selected range and reports whether it did. It is
// the first thing every destructive edit tries, so backspace, delete and typing
// all replace a selection rather than acting beside it.
func (in *Input) deleteSelection() bool {
	if !in.HasSelection() {
		return false
	}
	lo, hi := in.Selection()
	in.Text = in.Text[:lo] + in.Text[hi:]
	in.Cursor, in.Anchor = lo, lo
	return true
}

// Handle applies an action or literal text, reporting whether it was consumed.
// Unconsumed keys fall through to the pane, which is how tab escapes a field.
func (in *Input) Handle(a keys.Action, text string) bool {
	switch a {
	// A plain move collapses any selection; the shift variants extend it. The
	// two differ only in that flag, so they cannot drift apart.
	case keys.CharLeft:
		in.moveTo(in.collapseOr(prevBoundary, true), false)
	case keys.CharRight:
		in.moveTo(in.collapseOr(nextBoundary, false), false)
	case keys.SelCharLeft:
		in.moveTo(prevBoundary(in.Text, in.Cursor), true)
	case keys.SelCharRight:
		in.moveTo(nextBoundary(in.Text, in.Cursor), true)
	case keys.WordLeft:
		in.moveTo(prevWord(in.Text, in.Cursor), false)
	case keys.WordRight:
		in.moveTo(nextWord(in.Text, in.Cursor), false)
	case keys.SelWordLeft:
		in.moveTo(prevWord(in.Text, in.Cursor), true)
	case keys.SelWordRight:
		in.moveTo(nextWord(in.Text, in.Cursor), true)
	case keys.LineStart, keys.DocStart:
		in.moveTo(0, false)
	case keys.LineEnd, keys.DocEnd:
		in.moveTo(len(in.Text), false)
	case keys.SelLineStart, keys.SelDocStart:
		in.moveTo(0, true)
	case keys.SelLineEnd, keys.SelDocEnd:
		in.moveTo(len(in.Text), true)
	case keys.Backspace:
		if in.deleteSelection() {
			break
		}
		if in.Cursor > 0 {
			p := prevBoundary(in.Text, in.Cursor)
			in.Text = in.Text[:p] + in.Text[in.Cursor:]
			in.Cursor, in.Anchor = p, p
		}
	case keys.Delete:
		if in.deleteSelection() {
			break
		}
		if in.Cursor < len(in.Text) {
			n := nextBoundary(in.Text, in.Cursor)
			in.Text = in.Text[:in.Cursor] + in.Text[n:]
		}
	case keys.SelectAll:
		// Previously this emptied the field. With no selection to represent,
		// "select everything" had nowhere to put its result, so it did the one
		// thing that looked similar and was destructive instead.
		in.Anchor, in.Cursor = 0, len(in.Text)
	case keys.None:
		if text == "" || text == "\n" {
			return false
		}
		in.deleteSelection()
		in.Text = in.Text[:in.Cursor] + text + in.Text[in.Cursor:]
		in.Cursor += len(text)
		in.Anchor = in.Cursor
	default:
		return false
	}
	return true
}

// collapseOr implements the macOS rule that an unshifted arrow next to a
// selection jumps to that edge rather than moving one place from the caret.
func (in *Input) collapseOr(step func(string, int) int, left bool) int {
	if in.HasSelection() {
		lo, hi := in.Selection()
		if left {
			return lo
		}
		return hi
	}
	return step(in.Text, in.Cursor)
}

// Render draws the field with a border. The boundary is deliberately explicit:
// a search box needs to look like somewhere you type, not like another line of
// the pane.
func (in *Input) Render(s *ui.Screen, x, y, w int, th Theme) {
	if w < 3 {
		return
	}
	border := th.Border
	if in.Focused {
		border = th.BorderFocus
	}
	Box(s, x, y, w, 3, border)
	if in.Label != "" && w > len(in.Label)+4 {
		s.SetString(x+2, y, " "+in.Label+" ", border, w-4)
	}

	inner := w - 2
	in.clip(inner)
	// No placeholder: the label is already on the border, and repeating it
	// inside makes an empty field look like it has content.
	s.SetString(x+1, y+1, in.Text[in.scroll:], th.Text, inner)
	in.renderSelection(s, x+1, y+1, inner, th)

	if in.Focused {
		col := runeCols(in.Text[in.scroll:in.Cursor])
		if col < inner {
			c := s.At(x+1+col, y+1)
			r := c.Rune
			if r == 0 || c.Width == 0 {
				r = ' '
			}
			s.Set(x+1+col, y+1, r, th.Text.Plus(ui.Reverse))
		}
	}
}

// renderSelection repaints the selected span in reverse video. It runs after
// the text is drawn and only changes styles, so it cannot disagree with the
// glyphs already on screen — including wide runes, whose continuation cells are
// left alone.
func (in *Input) renderSelection(s *ui.Screen, x, y, w int, th Theme) {
	lo, hi := in.Selection()
	if lo == hi || hi <= in.scroll {
		return
	}
	if lo < in.scroll {
		lo = in.scroll
	}
	col := runeCols(in.Text[in.scroll:lo])
	for i := lo; i < hi; {
		n := nextBoundary(in.Text, i)
		cw := runeCols(in.Text[i:n])
		if col >= w {
			return
		}
		c := s.At(x+col, y)
		r := c.Rune
		if r == 0 || c.Width == 0 {
			r = ' '
		}
		s.Set(x+col, y, r, th.Text.Plus(ui.Reverse))
		col += cw
		i = n
	}
}

// clip scrolls the field so the cursor stays visible.
func (in *Input) clip(width int) {
	if in.Cursor < in.scroll {
		in.scroll = in.Cursor
	}
	for runeCols(in.Text[in.scroll:in.Cursor]) >= width && in.scroll < in.Cursor {
		in.scroll = nextBoundary(in.Text, in.scroll)
	}
	if in.scroll > len(in.Text) {
		in.scroll = len(in.Text)
	}
}

func runeCols(s string) (n int) {
	for _, r := range s {
		n += ui.RuneWidth(r)
	}
	return
}

// isWordByte is the class that word motion stops at the edge of. Deliberately
// crude — letters, digits and underscore — because these fields hold queries,
// globs and paths, and treating "*.go" or "internal/keys" as one word is what
// makes alt+left useless for editing them.
func isWordByte(b byte) bool {
	return b == '_' || b >= '0' && b <= '9' ||
		b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= 0x80
}

// prevWord skips any run of non-word bytes, then the word before it. This is
// what alt+left should have done all along: it was aliased to prevBoundary,
// which walks one UTF-8 rune, so word motion and character motion were the same
// keystroke wearing two names.
func prevWord(s string, i int) int {
	for i > 0 && !isWordByte(s[i-1]) {
		i--
	}
	for i > 0 && isWordByte(s[i-1]) {
		i--
	}
	return i
}

func nextWord(s string, i int) int {
	for i < len(s) && !isWordByte(s[i]) {
		i++
	}
	for i < len(s) && isWordByte(s[i]) {
		i++
	}
	return i
}

func prevBoundary(s string, i int) int {
	for i > 0 {
		i--
		if s[i]&0xC0 != 0x80 {
			return i
		}
	}
	return 0
}

func nextBoundary(s string, i int) int {
	for i < len(s) {
		i++
		if i >= len(s) || s[i]&0xC0 != 0x80 {
			return i
		}
	}
	return len(s)
}

// Fields is a group of inputs plus any other focusable controls, cycled by tab.
type Fields struct {
	Inputs []*Input
	At     int
}

// Focus marks the active input and clears the rest.
func (f *Fields) Focus() {
	for i, in := range f.Inputs {
		in.Focused = i == f.At
	}
}

// Trim removes surrounding whitespace from every field's text.
func (f *Fields) Trim() {
	for _, in := range f.Inputs {
		// Clamp, don't just assign: trimming shortens the text, and a caret or
		// anchor left past the new end indexes out of range on the next edit.
		in.SetText(strings.TrimSpace(in.Text))
	}
}
