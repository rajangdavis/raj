package app

import (
	"time"

	"raj/internal/editor"
	"raj/internal/keys"
	"raj/internal/ui"
)

// Pointer handling.
//
// Every press is resolved against the geometry the last frame was drawn with,
// and each pane hit-tests against what it drew rather than against a second
// copy of the arithmetic. That is the whole design: a click can only land on
// the wrong row if the pane also drew it there.
//
// The order below is the order things are drawn, reversed. An overlay is on
// top, so it takes the press wherever it falls and swallows the ones outside
// it — a modal dialog that lets a click through to the tab it is asking about
// is worse than one that ignores it.

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
	cols, rows := a.screen.Size()
	l := computeLayout(cols, rows, a.sidebar, a.focus)

	if ev.Motion {
		// A drag only ever extends a selection in the document. Dragging
		// through a list would have to mean either scrolling or a range
		// selection, and neither pane has a range to select.
		if a.drag && ev.Button == keys.MouseLeft {
			if p := a.Tabs.Active(); p != nil {
				x, y, _ := a.editorCell(l, p, ev.Col, ev.Row)
				p.DragTo(x, y)
			}
		}
		return
	}

	// Middle-click closes a tab. There is no × drawn on a tab to aim at, and
	// adding one would spend a column of every label on a target that is
	// missed as often as it is hit at sidebar widths; middle-click is what
	// every browser and most editors already use for the same thing.
	if ev.Button == keys.MouseMiddle {
		if i, ok := a.Tabs.HitTest(0, cols, ev.Col); ok && ev.Row == l.TabY {
			a.closeTabAt(i)
		}
		return
	}
	if ev.Button != keys.MouseLeft {
		return // right-click does nothing yet
	}

	switch {
	case a.Prompt.Open:
		// A dialog takes every press while it is open, including the ones
		// outside it. Clicking off a modal question cannot dismiss it — the
		// question has to be answered, and the answer decides what happens to
		// the thing being asked about.
		a.Prompt.ClickAt(cols, rows, ev.Col, ev.Row)
		a.settlePrompt()
		return
	case a.Picker.Open:
		path, inside := a.Picker.ClickAt(cols, rows, ev.Col, ev.Row)
		if path != "" {
			a.openFromPicker(path)
		}
		if inside {
			return
		}
		// A press outside an open picker dismisses it, the way clicking off a
		// menu does, and then belongs to whatever it landed on.
		a.Picker.Hide()
		a.focus = FocusEditor
	}

	if ev.Row == l.TabY {
		if i, ok := a.Tabs.HitTest(0, cols, ev.Col); ok {
			a.Tabs.Goto(i + 1)
			a.focusEditor()
			a.refreshSyntax()
		}
		return
	}
	if l.ShowSidebar && ev.Col >= l.SidebarX && ev.Col < l.SidebarX+l.SidebarW &&
		ev.Row >= l.TopY && ev.Row < l.TopY+l.Rows {
		a.clickSidebar(l, ev)
		return
	}
	if l.ShowEditor {
		a.clickEditor(l, ev)
	}
}

// clickSidebar routes a press into whichever sidebar is open. The divider
// column belongs to the sidebar's width but is drawn over, so the panes are hit
// tested against the width they were rendered with rather than the one they
// were allotted.
func (a *App) clickSidebar(l Layout, ev ui.Mouse) {
	a.focus = FocusSidebar
	a.status = ""
	w := l.SidebarW
	if l.ShowEditor {
		w--
	}
	dx, dy := ev.Col-l.SidebarX, ev.Row-l.TopY
	switch a.sidebar {
	case SidebarExplorer:
		if path, _ := a.Explorer.ClickAt(dy, l.Rows); path != "" {
			a.OpenFile(path)
		}
	case SidebarSearch:
		if path, line, _ := a.Search.ClickAt(dx, dy, w, l.Rows); path != "" {
			a.OpenFile(path)
			a.jumpTo(line)
		}
	}
}

// clickEditor is the original editor mapping: the find bar takes the row it
// drew, and the text area takes the rest.
func (a *App) clickEditor(l Layout, ev ui.Mouse) {
	p := a.Tabs.Active()
	if p == nil {
		return
	}
	if p.Find.Open && ev.Row == l.TopY {
		a.focus = FocusEditor
		p.Find.ClickAt(ev.Col - l.EditorX)
		return
	}
	x, y, ok := a.editorCell(l, p, ev.Col, ev.Row)
	if !ok {
		return // a press outside the text area is not the editor's
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
