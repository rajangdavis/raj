package app

import (
	"time"

	"raj/internal/editor"
	"raj/internal/keys"
	"raj/internal/ui"
)

// Pointer handling in the editor.
//
// Only the editor: the tabs, the explorer and the search results are all
// clickable in principle and none of them are handled here, because each needs
// its own hit-testing against what it drew and doing them together would mean
// four half-tested mappings instead of one tested one.

// clickInterval is how close two presses must be to count as a double click.
// The platform default is around 500ms and the value is not worth a setting;
// what matters is that it is a real threshold rather than "the previous event",
// since a press two minutes later in the same cell is not a double click.
const clickInterval = 400 * time.Millisecond

// clickTracker counts rapid presses in one place.
type clickTracker struct {
	count int
	col   int
	row   int
	at    time.Time
}

// press records a press and returns how many it is in the current sequence:
// 1 for a single click, 2 for a double, 3 for a triple, then back to 1.
//
// Position matters as well as time. Two presses far apart on screen are two
// clicks however quickly they arrive, and treating them as a double click would
// select a word the pointer never touched.
func (c *clickTracker) press(col, row int, now time.Time) int {
	near := col == c.col && row == c.row
	soon := !c.at.IsZero() && now.Sub(c.at) <= clickInterval
	if near && soon {
		c.count++
		if c.count > 3 {
			c.count = 1 // a fourth click starts over rather than doing nothing
		}
	} else {
		c.count = 1
	}
	c.col, c.row, c.at = col, row, now
	return c.count
}

// pointer routes a non-wheel mouse event.
func (a *App) pointer(ev ui.Mouse) {
	// A release ends a drag wherever it happens, including outside the editor:
	// a button released over the sidebar is still released, and leaving the
	// drag flag set would make the next pointer movement extend a selection
	// nobody is holding.
	if !ev.Press {
		a.drag = false
		return
	}
	if ev.Button != keys.MouseLeft {
		return // middle and right do nothing yet
	}

	p := a.Tabs.Active()
	if p == nil {
		return
	}
	cols, rows := a.screen.Size()
	l := computeLayout(cols, rows, a.sidebar, a.focus)
	x, y, ok := a.editorCell(l, p, ev.Col, ev.Row)

	if ev.Motion {
		// A drag continues even when the pointer leaves the text area, because
		// letting go of the selection because you moved a cell too far is
		// worse than extending it to the nearest edge. The coordinates are
		// clamped rather than the event dropped.
		if a.drag {
			p.DragTo(x, y)
		}
		return
	}
	if !ok {
		return // a press outside the editor is not the editor's
	}

	a.focus = FocusEditor
	a.Complete.Hide()
	switch a.click.press(ev.Col, ev.Row, time.Now()) {
	case 2:
		p.SelectWordAt(x, y)
	case 3:
		p.SelectLineAt(x, y)
	default:
		switch {
		case ev.Mods&keys.ModShift != 0:
			// Shift-click extends from where the cursor already is, which is
			// how a selection is made without holding the button down.
			p.ClickAt(x, y, true)
		case ev.Mods&keys.ModSuper != 0 || ev.Mods&keys.ModAlt != 0:
			p.AddCursorAt(x, y)
		default:
			p.ClickAt(x, y, false)
		}
		a.drag = true
	}
	a.status = ""
}

// editorCell converts a screen cell to one relative to the pane's text area,
// and reports whether it was inside it.
//
// The gutter and the find bar are subtracted here rather than in the pane,
// because only the caller knows what it drew above and beside the text. The
// coordinates are returned clamped even when ok is false, so a drag that
// wanders out of the pane still has somewhere sensible to extend to.
func (a *App) editorCell(l Layout, p *editor.Pane, col, row int) (x, y int, ok bool) {
	top, rows := l.TopY, l.Rows
	if p.Find.Open {
		top, rows = top+1, rows-1
	}
	g := p.GutterWidth()
	textX := l.EditorX + g
	textW := l.EditorW - g

	x, y = col-textX, row-top
	inside := l.ShowEditor &&
		col >= textX && col < textX+textW &&
		row >= top && row < top+rows
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	if y >= rows && rows > 0 {
		y = rows - 1
	}
	return x, y, inside
}
