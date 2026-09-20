package app

import (
	"fmt"
	"strings"
	"time"

	"raj/internal/editor"
	"raj/internal/keys"
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

	l := a.layout(cols, rows)
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
	if a.phone {
		// The phone profile keeps one bottom row for the action drawer, in Edit
		// and Review alike; the expanded panel is drawn over the content. A
		// live status shares the collapsed handle row.
		a.drawPhoneDrawer(cols, l)
	} else {
		a.drawStatus(cols, rows-1)
	}

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
	case SidebarSettings:
		a.settingsPane.Render(a, a.screen, l.SidebarX, l.SidebarTop, w, l.SidebarRows, focused)
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
		if a.sidebar == SidebarSettings {
			return "settings"
		}
		return "explorer"
	case FocusPicker:
		return "go to file"
	case FocusPrompt:
		return a.Prompt.Title()
	}
	return "raj"
}

// drawerItem is one touch target in the phone action drawer: a labelled button
// drawn at row y over the columns [x, x+w). The handle carries action None; the
// pointer toggles rather than dispatches it.
type drawerItem struct {
	label  string
	action keys.Action
	x, y   int
	w, h   int
}

// drawerButton is one entry in the action drawer table. Every label names an
// action the keyboard already binds, so a tap runs the same dispatch a chord
// does.
type drawerButtonDef struct {
	label  string
	action keys.Action
}

// drawerReviewButtons are the review controls. They are drawn only while the
// app is in Review mode, because in Edit mode there is no change set on screen
// to decide and a hidden cell must have no hit span.
var drawerReviewButtons = []drawerButtonDef{
	{"prev", keys.PrevProposed},
	{"next", keys.NextProposed},
	{"accept", keys.AcceptProposed},
	{"reject", keys.RejectProposed},
	{"clear", keys.ClearRejected},
	{"list", keys.ReviewProposed},
}

// drawerGeneralButtons are always present: the editor actions a phone needs
// whatever the mode. The navigation buttons come first, because reaching the
// explorer or the search pane is how you get somewhere; then the buffer
// actions.
var drawerGeneralButtons = []drawerButtonDef{
	{"files", keys.FocusExplorer},
	{"search", keys.FocusSearch},
	{"save", keys.Save},
	{"open", keys.FilePicker},
	{"close", keys.CloseTab},
	{"exit", keys.Quit},
}

// drawerAllButtons is Review order: the review controls first, so Review opens
// onto them, then the general controls.
var drawerAllButtons = append(append([]drawerButtonDef{}, drawerReviewButtons...), drawerGeneralButtons...)

// drawer cell geometry: each button cell is three screen rows tall — a top
// border row, the label row, a bottom border row — two per row with a blank
// column between them. The whole bordered block is the target, so a tap on a
// border row is a tap on the button.
const (
	drawerCellRows = 3
	drawerGap      = 1
)

// drawerLayout places the collapsed handle and, when open, the panel and its
// buttons. It is the one function the renderer and the pointer share, so a tap
// cannot land on a button other than the one drawn under it. Buttons sit two to
// a row, so every target is a phone-sized cell.
//
// reviewControls selects the table: the general controls alone, or the review
// controls followed by the general ones. The caller decides it from the mode
// and whether the active pane has anything awaiting a decision, so the renderer
// and the pointer cannot disagree about which cells exist.
func drawerLayout(cols, rows int, open, reviewControls bool) (handle drawerItem, panelTop, panelRows int, buttons []drawerItem) {
	handle = drawerItem{label: "[actions]", y: rows - phoneDrawerRows, w: cols, h: phoneDrawerRows}
	if !open {
		return handle, 0, 0, nil
	}
	table := drawerGeneralButtons
	if reviewControls {
		table = drawerAllButtons
	}
	const perRow = 2
	rowsOfButtons := (len(table) + perRow - 1) / perRow
	panelRows = rowsOfButtons * drawerCellRows
	panelTop = handle.y - panelRows
	if panelTop < 0 {
		panelTop, panelRows = 0, handle.y
	}
	totalGap := (perRow - 1) * drawerGap
	bw := (cols - totalGap) / perRow
	for i, b := range table {
		row, col := i/perRow, i%perRow
		x := col * (bw + drawerGap)
		w := bw
		if col == perRow-1 {
			w = cols - x
		}
		y := panelTop + row*drawerCellRows
		if y+drawerCellRows > handle.y {
			continue // the strip is too short for this cell
		}
		buttons = append(buttons, drawerItem{label: b.label, action: b.action, x: x, y: y, w: w, h: drawerCellRows})
	}
	return handle, panelTop, panelRows, buttons
}

// drawerCell paints one bordered button cell: a top border row, a label row
// with the label centred, and a bottom border row. The border spans the same
// columns the hit rectangle covers, so a tap anywhere on the block — border
// rows included — is a tap on the button.
func drawerCell(s *ui.Screen, b drawerItem, style ui.Style) {
	if b.w < 2 || b.h < 3 {
		s.SetString(b.x, b.y, b.label, style, b.w)
		return
	}
	inner := b.w - 2
	// Measure the label in runes, not bytes: the selected cell carries a
	// multi-byte marker, and byte arithmetic would centre the block against the
	// wrong edge and leave the right border short of x+w-1. The marker is part
	// of the measured label, so the marked text itself is what centres.
	label := []rune(b.label)
	if len(label) > inner {
		label = label[:inner]
	}
	pad := inner - len(label)
	mid := "│" + strings.Repeat(" ", pad/2) + string(label) + strings.Repeat(" ", pad-pad/2) + "│"
	s.SetString(b.x, b.y, "┌"+strings.Repeat("─", inner)+"┐", style, b.w)
	s.SetString(b.x, b.y+1, mid, style, b.w)
	s.SetString(b.x, b.y+2, "└"+strings.Repeat("─", inner)+"┘", style, b.w)
}

// drawerKey routes a keystroke to the phone action drawer while it is open, or
// opens it on Up. It returns true when the drawer consumed the key, so the
// editor never also sees Left/Right/Tab. It works on the resolved keys action,
// not raw bytes, so it shares the keymap mapping and only the phone profile
// reaches it.
//
// Clamp, not wrap: Left at the first button and Right at the last stay put, and
// the linear index follows the two-per-row grid even when the last row holds a
// single button.
//
// While the drawer is open every key is swallowed except Esc (it still toggles
// the drawer closed), Quit (so a stray panel can never wedge the session) and
// the review toggle (switching mode cannot edit, and it is how the review
// controls become reachable); a panel over the editor must not let a stray key
// edit underneath it.
func (a *App) drawerKey(action keys.Action) bool {
	if !a.phone {
		return false
	}
	if !a.drawerOpen {
		// The closed-drawer keys are a repurposing of the editor pane only.
		// With the explorer, search, problems, picker or a prompt focused they
		// are that surface keys, and Up must not open the drawer there: the
		// handle tap is how a sidebar opens it.
		if a.focus != FocusEditor {
			return false
		}
		switch action {
		case keys.LineUp:
			// Up opens the drawer on the context default.
			a.drawerOpen = true
			a.drawerSel = 0
			a.drawerWant = a.drawerOpenWant()
			return true
		case keys.CharLeft:
			// Left/Right walk the tabs while the drawer is closed, mirroring
			// the tab-cycle chord rather than a second implementation of it.
			a.dispatch(keys.PrevTab, "")
			return true
		case keys.CharRight:
			a.dispatch(keys.NextTab, "")
			return true
		}
		return false
	}
	switch action {
	case keys.ToggleDrawer, keys.Quit, keys.ToggleReview:
		// Esc closes through the global handler, Quit is never swallowed, and
		// the review toggle changes which buttons exist.
		return false
	case keys.LineUp:
		// Movement is Left/Right while open.
		return true
	case keys.LineDown:
		a.drawerOpen = false
		return true
	case keys.CharLeft:
		a.drawerMove(-1)
		return true
	case keys.CharRight:
		a.drawerMove(1)
		return true
	case keys.Indent, keys.CycleFocus:
		// Tab, in the editor scope and beside it: activate the selection.
		if it, ok := a.selectedDrawer(); ok {
			a.drawerDispatch(it.action)
		}
		return true
	}
	return true
}

// drawerOpenWant is the button the drawer opens onto: next when a review is
// under way (Review mode with something pending), else the first general
// button, which is files. The target is named by action and resolved against
// the drawn panel, so it survives the mode/pending button set and a short
// screen. It is the open default only; the post-decision jumps are separate.
func (a *App) drawerOpenWant() keys.Action {
	if a.mode == ModeReview && a.activePending() > 0 {
		return keys.NextProposed
	}
	return drawerGeneralButtons[0].action // files
}

// drawerDispatch runs a drawer button and closes the panel when the action
// handed the keyboard to an overlay. It is the one place the rule lives, so a
// button that opens a picker or a dialog inherits it: the drawer is modal for
// the keys it draws and would otherwise swallow every key the new surface
// needs. An action that stays in the editor keeps the panel up.
func (a *App) drawerDispatch(action keys.Action) {
	before := a.focus
	beforePending := a.activePending()
	a.dispatch(action, "")
	if a.focus != before || a.Picker.Open || a.Prompt.Open {
		a.drawerOpen = false
	}
	if !a.drawerOpen {
		// The action handed the keyboard to an overlay, so the panel is gone
		// and there is no selection to move; drop any pending jump.
		a.drawerWant = keys.None
		return
	}
	// The selection follows the review workflow. A decision that emptied the
	// pending sets hands the next step to save; a save hands it to close. A
	// refused decision, or one that left sets pending, leaves the selection
	// alone. The button is named by action and resolved when the next frame
	// draws the filtered set, so it survives the panel shrinking.
	switch action {
	case keys.Save:
		a.drawerWant = keys.CloseTab
	case keys.AcceptProposed, keys.RejectProposed, keys.ClearRejected:
		if beforePending > 0 && a.activePending() == 0 {
			a.drawerWant = keys.Save
		}
	}
}

// activePending is how many change sets the active pane still has awaiting a
// decision.
func (a *App) activePending() int {
	p := a.Tabs.Active()
	if p == nil {
		return 0
	}
	return len(p.File.Session().Pending())
}

// drawerActionIndex finds a drawn cell by the action it runs, so selecting a
// button by name survives a different mode/pending set and a short screen that
// drops cells.
func drawerActionIndex(buttons []drawerItem, action keys.Action) (int, bool) {
	for i, b := range buttons {
		if b.action == action {
			return i, true
		}
	}
	return 0, false
}

// hasPendingChanges reports whether the active pane holds at least one change
// set awaiting a decision. It is the same test save uses for its refusal, and
// the cheapest accessor that answers it: Pending filters the diff walk, while
// the mark and group projections allocate and sort. The drawer recomputes it
// every frame because sets appear and vanish as agents write and the user
// decides.
func (a *App) hasPendingChanges() bool {
	p := a.Tabs.Active()
	return p != nil && len(p.File.Session().Pending()) > 0
}

// drawerMove moves the selection by delta within the drawn cells, clamping at
// both ends.
func (a *App) drawerMove(delta int) {
	n := len(a.drawerPanel)
	if n == 0 {
		a.drawerSel = 0
		return
	}
	a.drawerSel += delta
	if a.drawerSel < 0 {
		a.drawerSel = 0
	}
	if a.drawerSel >= n {
		a.drawerSel = n - 1
	}
}

// selectedDrawer is the button the selection names, when the drawer is open and
// the index is inside the drawn panel.
func (a *App) selectedDrawer() (drawerItem, bool) {
	if !a.drawerOpen || a.drawerSel < 0 || a.drawerSel >= len(a.drawerPanel) {
		return drawerItem{}, false
	}
	return a.drawerPanel[a.drawerSel], true
}

// drawPhoneDrawer paints the phone profile bottom row and, when open, the
// action panel over the content. The handle shares its one row with a live
// status: the status on the left, the handle on the right.
func (a *App) drawPhoneDrawer(cols int, l Layout) {
	a.drawerHandle = drawerItem{}
	a.drawerPanel = a.drawerPanel[:0]
	a.drawerPanelTop, a.drawerPanelRows = 0, 0
	if l.BarY < 0 {
		return
	}
	reviewControls := a.mode == ModeReview && a.hasPendingChanges()
	handle, panelTop, panelRows, buttons := drawerLayout(cols, l.BarY+phoneDrawerRows, a.drawerOpen, reviewControls)
	a.drawerHandle, a.drawerPanel = handle, buttons
	a.drawerPanelTop, a.drawerPanelRows = panelTop, panelRows

	style := ui.DefaultStyle.Plus(ui.Reverse)
	if a.drawerOpen {
		// The selection indexes the drawn cells, so a mode change or a short
		// screen that drops a cell can never leave it naming a button that is
		// not on screen.
		if len(buttons) == 0 {
			a.drawerSel = 0
		} else if a.drawerSel >= len(buttons) {
			a.drawerSel = len(buttons) - 1
		} else if a.drawerSel < 0 {
			a.drawerSel = 0
		}
		// A pending jump is resolved here, against the cells actually drawn:
		// the target may have shifted index or gone (short screen), in which
		// case the clamped selection stands.
		if a.drawerWant != keys.None {
			if i, ok := drawerActionIndex(buttons, a.drawerWant); ok {
				a.drawerSel = i
			}
			a.drawerWant = keys.None
		}
		// The selected cell is the accent block — white on the BorderFocus
		// blue — not a weight change, which reads faint on a phone. The marker
		// keeps it unambiguous in a monochrome terminal.
		selStyle := ui.DefaultStyle.With(ui.Ansi(15)).On(ui.Ansi(4))
		for y := panelTop; y < panelTop+panelRows; y++ {
			a.screen.Fill(0, y, cols, 1, style)
		}
		for i, b := range buttons {
			st := style
			if i == a.drawerSel {
				st = selStyle
				b.label = "▸ " + b.label
			}
			for dy := 0; dy < b.h; dy++ {
				a.screen.Fill(b.x, b.y+dy, b.w, 1, st)
			}
			drawerCell(a.screen, b, st)
		}
	}

	// The handle is a full-width, phoneDrawerRows-tall target. A live status
	// shares the strip: it takes the first row and the label the last.
	for dy := 0; dy < handle.h; dy++ {
		a.screen.Fill(0, handle.y+dy, cols, 1, style)
	}
	if a.status != "" {
		if a.status != a.statusShown {
			a.statusShown = a.status
			a.statusAt = time.Now()
		}
		a.screen.SetString(0, handle.y, " "+a.status, style, cols)
	}
	// Bottom row, left to right: the client connection dot (attached clients
	// only), the tab count, and the [actions] label right-aligned. Precedence
	// when the strip is narrow: the dot is pinned at column 0, the label keeps
	// the right and never starts before column 1, and the count is truncated
	// into whatever lies between them. The status is on the row above, so
	// nothing here shares a cell with it.
	bottom := handle.y + handle.h - 1
	hx := cols - len(handle.label) - 1
	if hx < 1 {
		hx = 1
	}
	countX := 0
	if a.attach {
		countX = 2 // the dot owns column 0 and the gap after it
	}
	if hx > countX {
		a.screen.SetString(countX, bottom, a.tabCountLabel(), style, hx-countX)
	}
	a.screen.SetString(hx, bottom, handle.label, style, cols-hx)
	if a.attach {
		dot := ui.DefaultStyle.With(ui.Ansi(2))
		if a.clientIsDown() {
			dot = ui.DefaultStyle.With(ui.Ansi(1))
		}
		a.screen.Set(0, bottom, '●', dot)
	}
}

// tabCountLabel is the collapsed handle left-hand count: the tabs the user has
// open. A preview slot is transient and left out, and a headless buffer is not
// a tab at all. The count alone reads better than active/total because the tab
// strip already marks the active tab, and it is shorter on a narrow phone.
func (a *App) tabCountLabel() string {
	n := a.Tabs.Count()
	if a.Tabs.Preview() != nil {
		n--
	}
	if n == 1 {
		return "1 tab"
	}
	return fmt.Sprintf("%d tabs", n)
}
