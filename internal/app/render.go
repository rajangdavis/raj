package app

import (
	"fmt"

	"raj/internal/editor"
	"raj/internal/ui"
)

// Draw renders a frame and hands it to the host.
func (a *App) Draw() {
	// Hints are dropped here rather than at each mutation site: a keystroke
	// reaches Draw after the edit, so comparing the document version against
	// the installed answer catches typed keys, paste, undo, redo and a control
	// write in one place, before a stale overlay can be painted.
	a.invalidateHints()
	cols, rows := a.syncSize()
	a.screen.Clear()
	if rows < 2 || cols < 4 {
		// The error is deliberately ignored here and only here: this is the
		// too-small-to-draw path, and there is nothing useful to fall back to.
		_ = a.host.Present(a.screen)
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
		a.drawDiagnosticMarks(l)
		a.drawProposalMarks(l)
	}
	a.drawStatus(cols, rows-1)

	// Completion sits over the editor but under the picker and any dialog: it
	// is anchored to the caret, so it belongs with the text rather than with
	// the overlays that take focus.
	if l.ShowEditor && !a.Picker.Open && !a.Prompt.Open {
		a.drawCompletion(l)
	}

	a.Debug.Render(a.screen, a, 0, l.TopY, cols, l.Rows, a.wth)
	// The picker floats above everything, so it is drawn last.
	a.Picker.Render(a.screen, cols, rows, a.wth)
	// A dialog is modal, so it floats above even the picker.
	a.Prompt.Render(a.screen, cols, rows, a.wth)
	if err := a.host.Present(a.screen); err != nil {
		// A failed or short write leaves the terminal holding part of a frame.
		// The host has already marked itself dirty, so the next Draw repaints
		// in full; surfacing it in the status line makes a repeating failure
		// visible instead of looking like random corruption.
		a.status = "display write failed: " + err.Error()
	}
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
	case SidebarProblems:
		a.Problems.Render(a.screen, l.SidebarX, l.TopY, w, l.Rows, a.wth, focused)
	}
	restore()
	if l.ShowEditor {
		for y := l.TopY; y < l.TopY+l.Rows; y++ {
			a.screen.Set(l.SidebarX+l.SidebarW-1, y, '│', a.wth.Border)
		}
	}
}

// drawCompletion places the popup in the editor's coordinates, past the gutter
// and offset by the find bar when it is open — the same origin the text itself
// is drawn from, or the list would sit a column or a row away from the word it
// is completing.
// drawDiagnosticMarks writes a severity letter into the gutter beside each
// problem line.
//
// Drawn over the line numbers rather than beside them, because widening the
// gutter for a column that is empty most of the time costs every file a column
// forever. A number under a mark is still recoverable — the cursor position is
// in the status line — and the mark only covers its first digit.
func (a *App) drawDiagnosticMarks(l Layout) {
	p := a.Tabs.Active()
	if p == nil {
		return
	}
	items := a.diags.forPath(a.docPath(p))
	if len(items) == 0 {
		return
	}
	top, rows := l.TopY, l.Rows
	if p.Find.Open {
		top, rows = top+1, rows-1
	}
	first := p.Viewport.Top

	// One mark per line, the most severe. A line with a warning and an error
	// is an error line.
	worst := map[int]int{}
	for _, it := range items {
		ln := it.Range.Start.Line
		if ln < first || ln >= first+rows {
			continue
		}
		if sev, seen := worst[ln]; !seen || severityRank(it.Severity) < severityRank(sev) {
			worst[ln] = it.Severity
		}
	}
	for ln, sev := range worst {
		row := top + (ln - first)
		st := a.theme.Gutter
		switch severityRank(sev) {
		case 0:
			st = st.With(ui.Ansi(1)) // red
		case 1:
			st = st.With(ui.Ansi(3)) // yellow
		}
		a.screen.SetString(l.EditorX, row, severityMark(sev), st, 1)
	}
}

// drawProposalMarks writes the pending-review mark into the gutter: one cell
// on each line a proposed change touches — the writer initial in green where
// text was added, red where text was removed. Colour carries the state of the
// change; the letter carries who, from the participant table. It shares the
// diagnostics marks trade: the number under a mark is recoverable from the
// status line, and a mark drawn over its first digit is still readable.
func (a *App) drawProposalMarks(l Layout) {
	p := a.Tabs.Active()
	if p == nil {
		return
	}
	marks := p.PendingMarks()
	if len(marks) == 0 {
		return
	}
	top, rows := l.TopY, l.Rows
	if p.Find.Open {
		top, rows = top+1, rows-1
	}
	first := p.Viewport.Top
	// Two passes so a removal wins a shared line: red is the mark that cannot
	// be seen anywhere else, since a deletion leaves no text to tint.
	for _, removed := range []bool{false, true} {
		for _, m := range marks {
			if m.Removed != removed {
				continue
			}
			last := m.Line
			if m.End > m.Start {
				last = p.File.LineOf(m.End - 1)
			}
			for line := m.Line; line <= last; line++ {
				if line < first || line >= first+rows {
					continue
				}
				color := a.theme.ProposedAdd
				if removed {
					color = a.theme.ProposedDel
				}
				a.screen.SetString(l.EditorX, top+(line-first),
					a.participantInitial(m.Author), a.theme.Gutter.With(color), 1)
			}
		}
	}
}

func (a *App) drawCompletion(l Layout) {
	p := a.Tabs.Active()
	if p == nil {
		return
	}
	x, top, w, rows := a.textArea(l, p)
	// The hover panel first, so a completion popup that overlaps it is drawn
	// on top. Completion is what you are doing; hover is what you were
	// reading, and the one being typed into should not be buried.
	a.Hover.Render(a.screen, x, top, w, rows, p.Viewport.Top, a.wth)
	if a.Complete.Open {
		a.Complete.Render(a.screen, x, top, w, rows, p.Viewport.Top, a.wth)
	}
}

// textArea is the editor's text region, excluding the gutter and the find bar.
// Both floating overlays are placed against it, so they cannot disagree about
// where column zero of the document is.
func (a *App) textArea(l Layout, p *editor.Pane) (x, y, w, h int) {
	top, rows := l.TopY, l.Rows
	if p.Find.Open {
		top, rows = top+1, rows-1
	}
	g := p.GutterWidth()
	return l.EditorX + g, top, l.EditorW - g, rows
}

func (a *App) drawEditor(l Layout) {
	p := a.Tabs.Active()
	if p == nil {
		a.drawEmpty(l)
		return
	}
	// The pane hints were filtered for the width they had when they were
	// installed. A resize, a sidebar or a split changes that width, and this is
	// the first point in the frame where the new width is known, so the set is
	// refreshed here — before RenderFocused resizes the pane itself.
	a.fitHints(p, l.EditorW-p.GutterWidth())
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

	review := a.mode == ModeReview
	left, right := " "+a.focusName(), ""
	if review {
		// The badge and keybar are the status line in Review mode: the mode is
		// modal, and the shortcuts and progress are what matter while it is on.
		left = " " + a.reviewBar()
	} else if p := a.Tabs.Active(); p != nil {
		f := p.File
		dirty := ""
		if f.Dirty() {
			dirty = " •"
		}
		left = fmt.Sprintf(" %s%s", f.Name(), dirty)
	}
	if p := a.Tabs.Active(); p != nil {
		f := p.File
		line, col := f.LineCol(p.Cursors.Primary().Head)
		right = fmt.Sprintf("%d:%d  %d pieces ", line+1, col+1, f.Pieces())
		if n := p.Cursors.Count(); n > 1 {
			right = fmt.Sprintf("%d cursors  ", n) + right
		}
	}
	if !review {
		// Always name the focused pane: on a narrow window only one pane is
		// drawn, so the status line is the only thing that says where keys are
		// going.
		left += "  [" + a.focusName() + "]"
		if p := a.Tabs.Active(); p != nil {
			if sum := a.diags.summary(a.docPath(p)); sum != "" {
				left += "  " + sum
			}
		}
	}
	// A transient message outranks the diagnostic on the cursor's line: the
	// status is something raj just did and the diagnostic is always there, so
	// showing both would let a stale message be the thing that is buried.
	switch {
	case a.status != "":
		left += "  " + a.status
	default:
		if !review {
			if d := a.diagnosticAtCursor(); d != "" {
				left += "  " + d
			}
		}
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
	case FocusPrompt:
		return a.Prompt.Title()
	}
	return "raj"
}
