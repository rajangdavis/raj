package view

// Viewport is the window onto the document: which line is at the top of the
// pane and how far right it is scrolled, in display columns.
//
// It owns nothing but those two numbers and the rules for adjusting them. The
// rules are where editors feel good or bad — scrolljoff keeps context under the
// cursor, and the "already visible means don't move" check is what stops the
// view jittering while you arrow around.
type Viewport struct {
	Top  int // first visible line
	Left int // first visible display column

	// TopRow is the visual row within Top that the pane starts at, so a
	// wrapped line taller than the pane can be entered partway. Always 0 when
	// wrapping is off, which is what keeps the unwrapped path unchanged.
	TopRow int
	Cols   int // visible width in columns
	Rows   int // visible height in lines

	// ScrollOff is how many lines of context to keep above and below the
	// cursor. Zero lets the cursor sit on the very first and last rows.
	ScrollOff int
}

// Resize sets the visible area, clamping scroll so a shrink cannot leave the
// viewport pointing past the end of the document.
func (v *Viewport) Resize(cols, rows, lines int) {
	v.Cols, v.Rows = cols, rows
	v.clampTop(lines)
}

// Bottom is the line just past the last visible one.
func (v *Viewport) Bottom() int { return v.Top + v.Rows }

// Visible reports whether a line is currently on screen.
func (v *Viewport) Visible(line int) bool { return line >= v.Top && line < v.Bottom() }

// ScrollTo moves the viewport so (line, col) is visible, doing nothing when it
// already is. The minimum-movement rule matters: scrolling to centre on every
// cursor move makes a file feel like it is sliding around under you.
func (v *Viewport) ScrollTo(line, col, lines int) {
	off := v.ScrollOff
	if v.Rows > 0 && off*2 >= v.Rows {
		off = (v.Rows - 1) / 2 // a tall scrolloff in a short pane would deadlock
	}

	if line-off < v.Top {
		v.Top = line - off
	} else if line+off >= v.Bottom() {
		v.Top = line + off - v.Rows + 1
	}
	v.clampTop(lines)

	switch {
	case col < v.Left:
		v.Left = col
	case v.Cols > 0 && col >= v.Left+v.Cols:
		v.Left = col - v.Cols + 1
	}
	if v.Left < 0 {
		v.Left = 0
	}
}

// Center places a line in the middle of the pane, for jumps where continuity
// with the previous position is meaningless — go-to-line, search results.
func (v *Viewport) Center(line, lines int) {
	v.Top = line - v.Rows/2
	v.clampTop(lines)
}

// ScrollBy moves the viewport without moving the cursor, for wheel and
// page-key scrolling.
func (v *Viewport) ScrollBy(delta, lines int) {
	v.Top += delta
	v.clampTop(lines)
}

// clampTop keeps Top within range. The last line is allowed to scroll to the
// top of the pane so the end of a file is reachable without dead space rules
// getting in the way.
func (v *Viewport) clampTop(lines int) {
	max := lines - 1
	if max < 0 {
		max = 0
	}
	if v.Top > max {
		v.Top = max
	}
	if v.Top < 0 {
		v.Top = 0
	}
}
