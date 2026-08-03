package app

import (
	"fmt"

	"raj/internal/ui"
)

// Draw renders a frame and hands it to the host.
func (a *App) Draw() {
	cols, rows := a.syncSize()
	a.screen.Clear()
	if rows < 2 || cols < 4 {
		a.host.Present(a.screen)
		return
	}

	l := computeLayout(cols, rows, a.sidebar, a.focus)
	// A layout change moves every pane boundary, so the previous frame is a
	// poor basis for a diff even though it is technically accurate. Repainting
	// once is cheaper than a subtle residue bug at a pane edge.
	if l != a.lastLayout {
		a.host.Invalidate()
		a.lastLayout = l
	}
	a.Tabs.Render(a.screen, 0, l.TabY, cols, a.wth)

	if l.ShowSidebar {
		a.drawSidebar(l)
	}
	if l.ShowEditor {
		a.drawEditor(l)
	}
	a.drawStatus(cols, rows-1)

	a.Debug.Render(a.screen, a, 0, l.TopY, cols, l.Rows, a.wth)
	// The picker floats above everything, so it is drawn last.
	a.Picker.Render(a.screen, cols, rows, a.wth)
	a.host.Present(a.screen)
}

// syncSize adopts the host's size, since a resize can arrive between frames.
func (a *App) syncSize() (int, int) {
	cols, rows := a.screen.Size()
	if hc, hr := a.host.Size(); hc != cols || hr != rows {
		a.screen.Resize(hc, hr)
		return hc, hr
	}
	return cols, rows
}

func (a *App) drawSidebar(l Layout) {
	focused := a.focus == FocusSidebar
	// The divider owns the last column, so content is drawn one narrower.
	// Without this the rule paints over the search fields' right border.
	w := l.SidebarW
	if l.ShowEditor {
		w--
	}
	restore := a.screen.Clip(l.SidebarX, l.TopY, w, l.Rows)
	switch a.sidebar {
	case SidebarExplorer:
		a.Explorer.Render(a.screen, l.SidebarX, l.TopY, w, l.Rows, a.wth, focused)
	case SidebarSearch:
		a.Search.Render(a.screen, l.SidebarX, l.TopY, w, l.Rows, a.wth, focused)
	}
	restore()
	if l.ShowEditor {
		for y := l.TopY; y < l.TopY+l.Rows; y++ {
			a.screen.Set(l.SidebarX+l.SidebarW-1, y, '│', a.wth.Border)
		}
	}
}

func (a *App) drawEditor(l Layout) {
	p := a.Tabs.Active()
	if p == nil {
		a.drawEmpty(l)
		return
	}
	restore := a.screen.Clip(l.EditorX, l.TopY, l.EditorW, l.Rows)
	defer restore()
	a.screen.Fill(l.EditorX, l.TopY, l.EditorW, l.Rows, ui.DefaultStyle)

	top, rows := l.TopY, l.Rows
	if p.Find.Open {
		p.Find.Render(a.screen, l.EditorX, top, l.EditorW, a.wth)
		top, rows = top+1, rows-1
	}
	p.RenderFocused(a.screen, l.EditorX, top, l.EditorW, rows, a.theme,
		a.focus == FocusEditor)
}

// drawEmpty is what shows when the last tab is closed. Closing the last tab
// leaves raj running, so this state needs to say what to do next.
func (a *App) drawEmpty(l Layout) {
	hints := []string{
		"no file open",
		"",
		"cmd+p       find a file",
		"cmd+shift+e file explorer",
		"cmd+shift+f search",
	}
	top := l.TopY + l.Rows/2 - len(hints)/2
	for i, line := range hints {
		x := l.EditorX + (l.EditorW-len(line))/2
		a.screen.SetString(x, top+i, line, a.wth.Dim, l.EditorW)
	}
}

// drawStatus is the bottom line: file, dirty marker, cursor position, and any
// transient message. Piece count is shown because it is the number that says
// when a session has grown large enough to want compacting.
func (a *App) drawStatus(cols, y int) {
	style := ui.DefaultStyle.Plus(ui.Reverse)
	a.screen.Fill(0, y, cols, 1, style)

	left, right := " "+a.focusName(), ""
	if p := a.Tabs.Active(); p != nil {
		f := p.File
		dirty := ""
		if f.Dirty() {
			dirty = " •"
		}
		left = fmt.Sprintf(" %s%s", f.Name(), dirty)
		line, col := f.LineCol(p.Cursors.Primary().Head)
		right = fmt.Sprintf("%d:%d  %d pieces ", line+1, col+1, f.Pieces())
		if n := p.Cursors.Count(); n > 1 {
			right = fmt.Sprintf("%d cursors  ", n) + right
		}
	}
	// Always name the focused pane: on a narrow window only one pane is drawn,
	// so the status line is the only thing that says where keys are going.
	left += "  [" + a.focusName() + "]"
	if a.status != "" {
		left += "  " + a.status
	}

	a.screen.SetString(0, y, left, style, cols)
	if w := len(right); w > 0 && w < cols {
		a.screen.SetString(cols-w, y, right, style, w)
	}
}

func (a *App) focusName() string {
	switch a.focus {
	case FocusSidebar:
		if a.sidebar == SidebarSearch {
			return "search"
		}
		return "explorer"
	case FocusPicker:
		return "go to file"
	}
	return "raj"
}
