package app

import (
	"fmt"
	"time"

	"raj/internal/editor"
	"raj/internal/timing"
	"raj/internal/ui"
)

// Draw renders a frame and hands it to the host.
func (a *App) Draw() {
	// The frame timer covers the whole paint, Present included: the beat the
	// save-review question is about is wall-clock to the screen, not to the
	// last Set. It also brackets the pending-marks accumulator, which the
	// editor adds to as PendingMarks walks the journal.
	var drawStart time.Time
	if timing.On {
		drawStart = time.Now()
		timing.ResetPending()
		defer a.traceDraw(drawStart)
	}
	// Hints are dropped here rather than at each mutation site: a keystroke
	// reaches Draw after the edit, so comparing the document version against
	// the installed answer catches typed keys, paste, undo, redo and a control
	// write in one place, before a stale overlay can be painted.
	a.invalidateHints()
	// Lenses are dropped by the same rule and for the same reason: their
	// anchors move with the text, so the frame that would draw them checks the
	// version first.
	a.invalidateLenses()
	// The semantic overlay is keyed to the document version the same way: an
	// edit moves the bytes it was measured on, so the frame that would paint
	// it checks the version first and re-installs a still-current answer when
	// the active tab has changed underneath it.
	a.invalidateSemantic()
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
	// poor basis for a diff even though it is technically accurate. When only
	// the panes moved, raj is the only writer and the frame covers every cell,
	// so Repaint rather than Invalidate — erasing first would flash the whole
	// screen on every sidebar toggle. A size change is not raj's own: the
	// terminal changed what is on screen (a resize leaves the cells outside the
	// old geometry undefined, and a resume may find a shell has been drawing),
	// so the first frame and every later size change clear before the write.
	if l != a.lastLayout {
		if cols != a.lastCols || rows != a.lastRows {
			a.host.Invalidate()
		} else {
			a.host.Repaint()
		}
		a.lastLayout = l
	}
	a.lastCols, a.lastRows = cols, rows
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
	// The context menu floats above the picker and below a dialog, matching
	// the order the pointer resolves a press in: a dialog first, then the
	// menu, then the picker.
	if a.Menu.Open() {
		a.drawMenu(cols, rows)
	}
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

// traceDraw is the frame timer's tail. It reports the frame and the pending
// journal walk inside it, then, when a save finished since the last frame, the
// distance from that save to the end of the first frame that paints the buffer
// clean — the lag the save-review question is about. Installed only when the
// timing gate is open; see package timing.
func (a *App) traceDraw(start time.Time) {
	pending, calls := timing.TakePending()
	timing.Log("draw", time.Since(start), "pending", pending, "calls", calls)
	if a.saveDoneAt.IsZero() {
		return
	}
	dirty := true
	if p := a.Tabs.Active(); p != nil {
		dirty = p.File.Dirty()
	}
	if !dirty {
		timing.Log("save-clean", time.Since(a.saveDoneAt))
	}
	a.saveDoneAt = time.Time{}
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
	restore := a.screen.Clip(l.SidebarX, l.SidebarTop, w, l.SidebarRows)
	switch a.sidebar {
	case SidebarExplorer:
		a.Explorer.Render(a.screen, l.SidebarX, l.SidebarTop, w, l.SidebarRows, a.wth, focused)
	case SidebarSearch:
		a.Search.Render(a.screen, l.SidebarX, l.SidebarTop, w, l.SidebarRows, a.wth, focused)
	case SidebarProblems:
		a.Problems.Render(a.screen, l.SidebarX, l.SidebarTop, w, l.SidebarRows, a.wth, focused)
	}
	restore()
	if l.ShowEditor {
		// The divider spans the full editor height, not just the content: the
		// pane boundary reads as continuous even where the sidebar is padded.
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
	if n := p.Find.Rows(); n > 0 {
		top, rows = top+n, rows-n
		if rows < 1 {
			rows = 1
		}
	}
	first := p.Viewport.Top

	// One mark per display row, the most severe. A line with a warning and an
	// error is an error line.
	//
	// The mark is keyed in session coordinates and placed in display ones: a
	// fold above the line shifts it down, and a line inside a fold has no row
	// to draw on and is dropped rather than clamped onto the fold row.
	worst := map[int]int{}
	for _, it := range items {
		row := p.DispOfDocLine(it.Range.Start.Line)
		if row < first || row >= first+rows {
			continue
		}
		if sev, seen := worst[row]; !seen || severityRank(it.Severity) < severityRank(sev) {
			worst[row] = it.Severity
		}
	}
	for row, sev := range worst {
		st := a.theme.Gutter
		switch severityRank(sev) {
		case 0:
			st = st.With(ui.Ansi(1)) // red
		case 1:
			st = st.With(ui.Ansi(3)) // yellow
		}
		a.screen.SetString(l.EditorX, top+(row-first), severityMark(sev), st, 1)
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
	if n := p.Find.Rows(); n > 0 {
		top, rows = top+n, rows-n
		if rows < 1 {
			rows = 1
		}
	}
	first := p.Viewport.Top
	// Two passes so a removal wins a shared line: red is the mark that cannot
	// be seen anywhere else, since a deletion leaves no text to tint.
	//
	// The hunk stays in session coordinates and each touched line is placed on
	// the display: a fold above the mark shifts it down, and a line the fold
	// hides draws nothing rather than a mark on the fold row.
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
				row := p.DispOfDocLine(line)
				if row < first || row >= first+rows {
					continue
				}
				color := a.theme.ProposedAdd
				if removed {
					color = a.theme.ProposedDel
				}
				a.screen.SetString(l.EditorX, top+(row-first),
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
	// The completion popup anchors on the display row and column of the word
	// (showCompletion calls DispPos), so its top is the viewport top unchanged.
	//
	// The hover panel still pins its anchor in session coordinates — hover()
	// captures it from File.LineCol — and places itself at (anchorLine -
	// topLine) rows below the editor origin. A fold between the viewport top
	// and the anchor shifts the anchor display row without moving its session
	// line by the same amount, so sessionTopFor hands back the top in that
	// same session coordinate; with no fold it is Viewport.Top exactly. When
	// hover() is moved onto DispPos like showCompletion, this becomes a plain
	// p.Viewport.Top and the helper goes.
	hoverLine, hoverCol := a.Hover.Anchor()
	// The hover panel first, so a completion popup that overlaps it is drawn
	// on top. Completion is what you are doing; hover is what you were
	// reading, and the one being typed into should not be buried.
	a.Hover.Render(a.screen, x, top, w, rows, sessionTopFor(p, hoverLine, hoverCol), a.wth)
	if a.Complete.Open {
		a.Complete.Render(a.screen, x, top, w, rows, p.Viewport.Top, a.wth)
	}
}

// sessionTopFor maps the pane viewport top into the session coordinate a
// session-anchored floating overlay expects, so (anchorLine - sessionTopFor)
// is the anchor display row minus the viewport display top.
//
// The anchor byte is resolved through DispPos — which handles a mid-line fold,
// not just a whole-line one — and the offset between the anchor session line
// and its display row is added back to the viewport top. It is an exact
// inverse only while the anchor is a session coordinate; see the caller for
// why hover is the only such overlay left.
func sessionTopFor(p *editor.Pane, anchorLine, anchorCol int) int {
	if anchorLine < 0 {
		return p.Viewport.Top
	}
	off := p.File.LineStart(anchorLine) + anchorCol
	anchorRow, _ := p.DispPos(off)
	return anchorLine - anchorRow + p.Viewport.Top
}

// textArea is the editor's text region, excluding the gutter and the find bar.
// Both floating overlays are placed against it, so they cannot disagree about
// where column zero of the document is.
func (a *App) textArea(l Layout, p *editor.Pane) (x, y, w, h int) {
	top, rows := l.TopY, l.Rows
	if n := p.Find.Rows(); n > 0 {
		top, rows = top+n, rows-n
		if rows < 1 {
			rows = 1
		}
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
	// Bring the display projection up to date with the mode before anything
	// measures the pane: fitHints and RenderFocused both call GutterWidth and
	// Resize, which read DisplayLines, and the render itself reads the map.
	// UpdateDisplay memoises, so the common path is a key comparison rather
	// than a journal walk every frame.
	p.UpdateDisplay(a.displayPolicy())
	// The pane hints were filtered for the width they had when they were
	// installed. A resize, a sidebar or a split changes that width, and this is
	// the first point in the frame where the new width is known, so the set is
	// refreshed here — before RenderFocused resizes the pane itself.
	a.fitHints(p, l.EditorW-p.GutterWidth())
	a.fitLenses(p, l.EditorW-p.GutterWidth())
	restore := a.screen.Clip(l.EditorX, l.TopY, l.EditorW, l.Rows)
	defer restore()
	a.screen.Fill(l.EditorX, l.TopY, l.EditorW, l.Rows, ui.DefaultStyle)

	top, rows := l.TopY, l.Rows
	if n := p.Find.Rows(); n > 0 {
		p.Find.Render(a.screen, l.EditorX, top, l.EditorW, a.wth)
		top, rows = top+n, rows-n
		if rows < 1 {
			rows = 1
		}
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
		if f.ViewDirty() {
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
	if note := a.pendingRemovalNote(); note != "" {
		left += "  " + note
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
