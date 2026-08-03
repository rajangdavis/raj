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
	Label   string
	Text    string
	Cursor  int
	Focused bool
	scroll  int
}

// Handle applies an action or literal text, reporting whether it was consumed.
// Unconsumed keys fall through to the pane, which is how tab escapes a field.
func (in *Input) Handle(a keys.Action, text string) bool {
	switch a {
	case keys.CharLeft, keys.WordLeft:
		in.Cursor = prevBoundary(in.Text, in.Cursor)
	case keys.CharRight, keys.WordRight:
		in.Cursor = nextBoundary(in.Text, in.Cursor)
	case keys.LineStart, keys.DocStart:
		in.Cursor = 0
	case keys.LineEnd, keys.DocEnd:
		in.Cursor = len(in.Text)
	case keys.Backspace:
		if in.Cursor > 0 {
			p := prevBoundary(in.Text, in.Cursor)
			in.Text = in.Text[:p] + in.Text[in.Cursor:]
			in.Cursor = p
		}
	case keys.Delete:
		if in.Cursor < len(in.Text) {
			n := nextBoundary(in.Text, in.Cursor)
			in.Text = in.Text[:in.Cursor] + in.Text[n:]
		}
	case keys.SelectAll:
		in.Text, in.Cursor = "", 0
	case keys.None:
		if text == "" || text == "\n" {
			return false
		}
		in.Text = in.Text[:in.Cursor] + text + in.Text[in.Cursor:]
		in.Cursor += len(text)
	default:
		return false
	}
	return true
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
		in.Text = strings.TrimSpace(in.Text)
	}
}
