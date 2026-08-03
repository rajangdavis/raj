package ui

import (
	"strings"
	"unicode/utf8"
)

// Cell is one character position.
type Cell struct {
	Rune  rune
	Style Style
	// Width is the display columns the rune occupies: 1 normally, 2 for wide
	// East Asian and emoji, 0 for the continuation cell that follows a wide
	// rune. Continuation cells are never drawn; they hold the position so
	// column arithmetic stays honest.
	Width int8
}

var blank = Cell{Rune: ' ', Style: DefaultStyle, Width: 1}

// Screen is a grid of cells raj renders into. It is a plain value store with no
// terminal knowledge, which is what lets the headless host assert on frames.
type Screen struct {
	cols, rows int
	cells      []Cell
	clip       rect

	// Cursor is where the terminal's own caret goes. Reverse video marks a
	// cell, which reads as a selected character rather than a position — you
	// cannot tell whether the caret sits before or after it. A real bar cursor
	// sits between cells, which is what an insertion point actually is.
	CursorX, CursorY int
	CursorShown      bool
}

// rect bounds where writes may land.
type rect struct{ x, y, w, h int }

func (r rect) contains(x, y int) bool {
	return x >= r.x && y >= r.y && x < r.x+r.w && y < r.y+r.h
}

// NewScreen returns a blank screen.
func NewScreen(cols, rows int) *Screen {
	s := &Screen{}
	s.Resize(cols, rows)
	return s
}

func (s *Screen) Size() (int, int) { return s.cols, s.rows }

// Resize reallocates and blanks the grid. Content is not preserved: every
// resize is followed by a full repaint anyway, since layout changes.
func (s *Screen) Resize(cols, rows int) {
	if cols < 0 {
		cols = 0
	}
	if rows < 0 {
		rows = 0
	}
	s.cols, s.rows = cols, rows
	s.cells = make([]Cell, cols*rows)
	s.clip = rect{0, 0, cols, rows}
	s.Clear()
}

// Clear blanks every cell to the terminal's default colours.
func (s *Screen) Clear() {
	for i := range s.cells {
		s.cells[i] = blank
	}
	s.CursorShown = false
}

// SetCursor places the terminal caret. Out-of-bounds positions hide it, which
// is what should happen when the cursor scrolls out of view.
func (s *Screen) SetCursor(x, y int) {
	if x < 0 || y < 0 || x >= s.cols || y >= s.rows {
		s.CursorShown = false
		return
	}
	s.CursorX, s.CursorY, s.CursorShown = x, y, true
}

// Fill blanks a rectangle with a style, for pane backgrounds and gutters.
func (s *Screen) Fill(x, y, w, h int, st Style) {
	for row := y; row < y+h; row++ {
		for col := x; col < x+w; col++ {
			s.Set(col, row, ' ', st)
		}
	}
}

// Clip restricts writes to a rectangle and returns a function restoring the
// previous one.
//
// This is a structural guarantee rather than a convention: a pane given a clip
// physically cannot paint over its neighbour, so a selection highlight or a
// wide rune at the edge of the editor can never bleed across the divider into
// the sidebar. Panes still compute their own bounds; this catches the cases
// where they get it wrong.
func (s *Screen) Clip(x, y, w, h int) (restore func()) {
	prev := s.clip
	s.clip = rect{x, y, w, h}
	return func() { s.clip = prev }
}

// Set writes one rune. Writes outside the clip are dropped rather than
// panicking: a rendering bug should not take the editor down mid-keystroke.
func (s *Screen) Set(x, y int, r rune, st Style) {
	if x < 0 || y < 0 || x >= s.cols || y >= s.rows || !s.clip.contains(x, y) {
		return
	}
	r = sanitize(r)
	w := int8(RuneWidth(r))
	if w < 1 {
		w = 1 // combining runes occupy their cell as a placeholder
	}
	if w == 2 && !s.clip.contains(x+1, y) {
		// A wide rune whose second column falls outside the clip would paint
		// over the neighbour. Draw a placeholder instead of half a glyph.
		s.cells[y*s.cols+x] = Cell{Rune: '…', Style: st, Width: 1}
		return
	}
	s.cells[y*s.cols+x] = Cell{Rune: r, Style: st, Width: w}
	if w == 2 && x+1 < s.cols {
		s.cells[y*s.cols+x+1] = Cell{Rune: 0, Style: st, Width: 0}
	}
}

// SetString writes a string starting at x, clipped to maxWidth display columns.
// It returns the columns consumed, so callers can lay out fields left to right
// without recomputing widths.
func (s *Screen) SetString(x, y int, text string, st Style, maxWidth int) int {
	used := 0
	for _, r := range text {
		// Measure what will actually be drawn: Set replaces control bytes with
		// a placeholder, and measuring the original would let a line of them
		// overflow the clip.
		w := RuneWidth(sanitize(r))
		if w < 1 {
			w = 1
		}
		if used+w > maxWidth {
			break
		}
		s.Set(x+used, y, r, st)
		used += w
	}
	return used
}

// At reads a cell. Out-of-bounds reads return a blank.
func (s *Screen) At(x, y int) Cell {
	if x < 0 || y < 0 || x >= s.cols || y >= s.rows {
		return blank
	}
	return s.cells[y*s.cols+x]
}

// Row returns a row's text with styling stripped, for tests and debugging.
func (s *Screen) Row(y int) string {
	var sb strings.Builder
	for x := 0; x < s.cols; x++ {
		if c := s.At(x, y); c.Width > 0 {
			sb.WriteRune(c.Rune)
		}
	}
	return strings.TrimRight(sb.String(), " ")
}

// Diff renders the changes from prev to s as terminal output. A nil prev, or
// one of a different size, forces a full repaint.
//
// Diffing is what keeps a keystroke from repainting the whole window: a typed
// character touches one cell, so the emitted bytes are a cursor move and one
// rune rather than several kilobytes.
func (s *Screen) Diff(prev *Screen) string {
	full := prev == nil || prev.cols != s.cols || prev.rows != s.rows
	var sb strings.Builder
	cur := Style{Fg: Color(-2)} // impossible, so the first cell always emits SGR
	lastX, lastY := -2, -2

	for y := 0; y < s.rows; y++ {
		for x := 0; x < s.cols; x++ {
			// Never write the bottom-right cell. On most terminals that
			// triggers a scroll, which shifts every row up and leaves the diff
			// describing a screen that no longer exists.
			if y == s.rows-1 && x == s.cols-1 {
				continue
			}
			c := s.cells[y*s.cols+x]
			if c.Width == 0 {
				continue // continuation of a wide rune, already emitted
			}
			if !full && prev.At(x, y) == c {
				continue
			}
			if y != lastY || x != lastX {
				sb.WriteString(cursorTo(x, y))
			}
			if c.Style != cur {
				sb.WriteString(c.Style.sgr())
				cur = c.Style
			}
			r := c.Rune
			if r == 0 {
				r = ' '
			}
			sb.WriteRune(r)
			lastX, lastY = x+int(c.Width), y
		}
	}
	if sb.Len() > 0 {
		sb.WriteString("\x1b[0m")
	}
	return sb.String()
}

// Clone returns a deep copy, used to retain a frame for the next diff.
func (s *Screen) Clone() *Screen {
	c := &Screen{cols: s.cols, rows: s.rows, clip: s.clip,
		CursorX: s.CursorX, CursorY: s.CursorY, CursorShown: s.CursorShown,
		cells: make([]Cell, len(s.cells))}
	copy(c.cells, s.cells)
	return c
}

// sanitize replaces anything that must never reach the terminal verbatim.
//
// A raw control byte is not merely unreadable: ESC begins a sequence the
// terminal executes, so a single stray 0x1b from a binary file or a mangled
// buffer hijacks the screen and can leave it in a state raj cannot repair.
// Everything unprintable becomes a visible placeholder instead, which also
// makes the damage legible rather than mysterious.
func sanitize(r rune) rune {
	switch {
	case r == 0:
		return ' '
	case r < 0x20 || r == 0x7f:
		return '·' // C0 controls, including ESC
	case r >= 0x80 && r <= 0x9f:
		return '·' // C1 controls
	case r == utf8.RuneError:
		return '?'
	case !utf8.ValidRune(r):
		return '?'
	}
	return r
}

func cursorTo(x, y int) string {
	return "\x1b[" + itoa(y+1) + ";" + itoa(x+1) + "H"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [8]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
