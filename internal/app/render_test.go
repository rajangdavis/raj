package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"raj/internal/keys"
	"raj/internal/lsp"
	"raj/internal/piecetable"
	"raj/internal/tabs"
	"raj/internal/ui"
)

// These tests pin the D2b rule for the app's gutter and floating consumers: a
// mark stays in session coordinates and is placed on the display map, so a fold
// above it shifts it down by the hidden rows and a mark inside a fold is not
// drawn at all. The fold is built the way the editor builds one — reject a
// multi-line insertion in Edit mode — so the tests exercise the real projection
// rather than a hand-built one.

// foldedFixture is the one scenario every test below derives from: four lines,
// a rejected insertion of two lines at the top, and a later session line that
// must be placed one display row higher than its session line.
const foldedFixture = "aaa\nbbb\nccc\nddd\n"

// foldedMarkHarness builds the fixture, rejects a two-line "HIDDEN1\nHIDDEN2\n"
// insertion after the first line, and returns the pane and the session line of
// the last document line.
//
// The insertion is proposed and rejected so that Edit mode's
// AcceptedAndProposed policy folds it; a rejected insertion is excluded from
// the composition, so its two session lines collapse to one fold row and every
// line after it is pulled one display row up. A one-line fold would not shift
// anything — one fold row stands where one session line was — which is why the
// fixture hides two.
func foldedMarkHarness(t *testing.T) (*harness, int) {
	t.Helper()
	h := newHarness(t, foldedFixture)
	at := strings.Index(foldedFixture, "bbb")
	id := propose(t, h, piecetable.Hunk{Start: at, End: at, Text: "HIDDEN1\nHIDDEN2\n"})
	if !h.Pane().File.RejectGroup(id) {
		t.Fatal("reject failed")
	}
	h.Draw()

	p := h.Pane()
	// The inserted lines are session 1 and 2: both have no display row.
	if got := p.DispOfDocLine(1); got != -1 {
		t.Fatalf("DispOfDocLine(1) = %d, want -1: the fixture did not fold a line", got)
	}
	if got := p.DispOfDocLine(2); got != -1 {
		t.Fatalf("DispOfDocLine(2) = %d, want -1: the fixture did not fold two lines", got)
	}
	// The last line is session 5 after the insertion ("aaa", the two hidden,
	// then bbb/ccc/ddd) and sits on display row 4: the shift the tests follow.
	if got := p.DispOfDocLine(5); got != 4 {
		t.Fatalf("DispOfDocLine(5) = %d, want 4: the fixture did not shift the tail", got)
	}
	return h, 5
}

// editorLayout is the layout the frame was drawn at, so a test can address the
// gutter cell a mark should occupy.
func editorLayout(h *harness) Layout {
	return computeLayout(120, 12, h.App.sidebar, h.App.focus)
}

// gutterCell is the screen cell a display row draws its gutter mark in.
func gutterCell(h *harness, l Layout, row int) ui.Cell {
	y := l.TopY + (row - h.Pane().Viewport.Top)
	return h.host.Last().At(l.EditorX, y)
}

// A diagnostic published on a line below a fold is drawn on that line's display
// row, not its session line: session line 5 lands on display row 4, so the
// naive placement would paint one row low.
func TestDiagnosticMarkFollowsFoldShift(t *testing.T) {
	h, hiddenTail := foldedMarkHarness(t)
	path := h.docPath(h.Pane())
	h.diags.set(path, []lsp.Diagnostic{{Range: lsp.Range{Start: lsp.Position{Line: hiddenTail}}, Severity: sevError}})
	h.Draw()

	l := editorLayout(h)
	if cell := gutterCell(h, l, 4); cell.Rune != 'E' {
		t.Errorf("display row 4 rune = %q, want E: the mark did not follow the fold", cell.Rune)
	}
	// The session line's own row (5) must not hold the mark: placing by session
	// number would put it there.
	if cell := gutterCell(h, l, 5); cell.Rune == 'E' {
		t.Error("display row 5 holds an E: the mark was placed by session line, not display row")
	}
}

// A diagnostic on a line the fold hides is skipped, not clamped onto the fold
// row: the hidden line has no display row of its own.
func TestDiagnosticMarkInsideFoldIsSkipped(t *testing.T) {
	h, _ := foldedMarkHarness(t)
	path := h.docPath(h.Pane())
	h.diags.set(path, []lsp.Diagnostic{{Range: lsp.Range{Start: lsp.Position{Line: 1}}, Severity: sevError}})
	h.Draw()

	l := editorLayout(h)
	// Find the fold row and assert the gutter there is clean. Also assert no
	// E anywhere in the gutter, since a skip must not be a clamp.
	foldRow := -1
	for row := 0; row < h.Pane().DisplayLines(); row++ {
		if _, _, ok := h.Pane().Fold(row); ok {
			foldRow = row
		}
		if cell := gutterCell(h, l, row); cell.Rune == 'E' {
			t.Errorf("display row %d holds an E, want no mark for a hidden line", row)
		}
	}
	if foldRow < 0 {
		t.Fatal("fixture has no fold row")
	}
}

// A pending proposal below a fold draws its green gutter mark on the display
// row, matching PendingMark.DispLine; the review gutter follows the same shift
// as the diagnostics gutter.
func TestProposalMarkFollowsFoldShift(t *testing.T) {
	h, _ := foldedMarkHarness(t)
	off := strings.Index(h.text(), "ddd")
	propose(t, h, piecetable.Hunk{Start: off, End: off + len("ddd"), Text: "DDD"})
	h.Draw()

	marks := h.Pane().PendingMarks()
	if len(marks) != 1 {
		t.Fatalf("pending marks = %d, want 1", len(marks))
	}
	row := marks[0].DispLine(h.Pane())
	if row != 4 {
		t.Fatalf("mark display row = %d, want 4", row)
	}

	l := editorLayout(h)
	if cell := gutterCell(h, l, row); cell.Style.Fg != h.App.theme.ProposedAdd {
		t.Errorf("display row %d fg = %v, want the added colour %v", row, cell.Style.Fg, h.App.theme.ProposedAdd)
	}
	// The naive session row must be clean: a mark placed there is the bug.
	if cell := gutterCell(h, l, marks[0].Line); cell.Style.Fg == h.App.theme.ProposedAdd {
		t.Error("the session row holds the added-colour mark: placement ignored the fold")
	}
}

// With no decisions the map is the identity, so a diagnostic and a proposal
// land on their session lines exactly as they did before the projection.
func TestMarksIdentityWithoutFolds(t *testing.T) {
	h := newHarness(t, foldedFixture)
	off := strings.Index(h.text(), "ddd")
	propose(t, h, piecetable.Hunk{Start: off, End: off + len("ddd"), Text: "DDD"})
	h.diags.set(h.docPath(h.Pane()), []lsp.Diagnostic{
		{Range: lsp.Range{Start: lsp.Position{Line: 2}}, Severity: sevError},
	})
	h.Draw()

	p := h.Pane()
	if p.DisplayLines() != p.File.Lines() {
		t.Fatalf("DisplayLines = %d, want File.Lines = %d with no decisions", p.DisplayLines(), p.File.Lines())
	}
	if got := p.DispOfDocLine(2); got != 2 {
		t.Fatalf("DispOfDocLine(2) = %d, want the identity 2", got)
	}
	l := editorLayout(h)
	if cell := gutterCell(h, l, 2); cell.Rune != 'E' {
		t.Errorf("display row 2 rune = %q, want E on the session line", cell.Rune)
	}
	marks := p.PendingMarks()
	if len(marks) != 1 || marks[0].DispLine(p) != marks[0].Line {
		t.Errorf("marks = %+v, want one on its session line with no fold", marks)
	}
}

// sessionTopFor hands a session-anchored overlay the viewport top in the
// anchor's coordinate, so the overlay's (anchorLine - top) is the display
// delta. With no fold it is the plain viewport top, which is what keeps a
// clean buffer's popup exactly where it was.
func TestSessionTopForIdentityWithoutFolds(t *testing.T) {
	h := newHarness(t, foldedFixture)
	p := h.Pane()
	p.Viewport.Top = 2
	// No projection: the anchor rows and columns are the session ones.
	if got := sessionTopFor(p, 3, 0); got != p.Viewport.Top {
		t.Errorf("sessionTopFor = %d, want the viewport top %d with no fold", got, p.Viewport.Top)
	}
}

// Below a fold the overlay placement rows are display rows: sessionTopFor
// cancels the anchor's session-to-display offset, so (anchorLine - top) is the
// anchor display row minus the viewport top. Session line 5 sits on display row
// 4, and with the viewport top on display row 2 the top must come back as
// session line 3 so the difference is 1.
func TestSessionTopForAdjustsBelowFold(t *testing.T) {
	h, tail := foldedMarkHarness(t)
	p := h.Pane()
	p.Viewport.Top = 2
	got := sessionTopFor(p, tail, 0)
	// anchorRow(tail) = 4, so got = 5 - 4 + 2 = 3.
	if got != 3 {
		t.Fatalf("sessionTopFor = %d, want 3", got)
	}
	anchorRow, _ := p.DispPos(p.File.LineStart(tail) + 0)
	if delta := tail - got; delta != anchorRow-p.Viewport.Top {
		t.Errorf("anchor - top = %d, want the display delta %d", delta, anchorRow-p.Viewport.Top)
	}
}

// The phone drawer collapsed handle is a phone-only bottom strip: it is present
// in Edit and in Review, at least two rows tall and full width. The ordinary
// profile draws no drawer at all.
func TestPhoneDrawerHandleOnlyInPhone(t *testing.T) {
	p := newPhoneHarnessSize(t, reviewFixture, 120, 30)
	p.status = ""
	p.Draw()
	l := p.layout(120, 30)
	if l.BarY != 30-phoneDrawerRows {
		t.Fatalf("phone BarY = %d, want the handle top at %d", l.BarY, 30-phoneDrawerRows)
	}
	if want := 30 - tabs.PhoneStripRows - phoneDrawerRows; l.Rows != want {
		t.Errorf("editor Rows = %d, want %d", l.Rows, want)
	}
	h := p.drawerHandle
	if h.label != "[actions]" || h.y != l.BarY || h.w != 120 || h.h < 2 {
		t.Fatalf("handle = %+v, want [actions] at row %d, full width, >=2 rows", h, l.BarY)
	}
	if got := p.host.Last().Row(h.y + h.h - 1); !strings.Contains(got, "[actions]") {
		t.Errorf("handle row = %q, want the label", got)
	}
	if len(p.drawerPanel) != 0 {
		t.Errorf("panel = %+v, want collapsed", p.drawerPanel)
	}

	// Review keeps the same collapsed handle.
	p.press("super+r")
	if p.mode != ModeReview {
		t.Fatalf("mode = %v after super+r, want Review", p.mode)
	}
	p.Draw()
	if got := p.host.Last().Row(p.drawerHandle.y + p.drawerHandle.h - 1); !strings.Contains(got, "[actions]") {
		t.Errorf("review handle row = %q, want the label", got)
	}

	n := newHarness(t, reviewFixture) // the ordinary profile
	n.status = ""
	n.Draw()
	if n.drawerHandle != (drawerItem{}) || len(n.drawerPanel) != 0 {
		t.Errorf("non-phone drawer state = %+v/%+v, want nothing", n.drawerHandle, n.drawerPanel)
	}
	if strings.Contains(n.host.Text(), "[actions]") {
		t.Error("the drawer was drawn without --phone")
	}
}

// The handle is a two-row, full-width target: a tap on either row toggles it,
// and a tap above the open panel collapses it.
func TestPhoneDrawerTapOpensAndOutsideCloses(t *testing.T) {
	h := newPhoneHarnessSize(t, "one\ntwo\n", 120, 30)
	h.status = ""
	h.Draw()
	if h.drawerOpen {
		t.Fatal("fixture drawer is already open")
	}
	top := h.drawerHandle.y
	bottom := h.drawerHandle.y + h.drawerHandle.h - 1
	h.Handle(ui.Mouse{Mouse: keys.Mouse{Button: keys.MouseLeft, Press: true, Col: 2, Row: top}})
	if !h.drawerOpen {
		t.Fatal("a tap on the top handle row did not open the drawer")
	}
	h.Draw()
	if len(h.drawerPanel) == 0 {
		t.Fatal("the open drawer drew no buttons")
	}
	h.Handle(ui.Mouse{Mouse: keys.Mouse{Button: keys.MouseLeft, Press: true, Col: 2, Row: bottom}})
	if h.drawerOpen {
		t.Error("a tap on the bottom handle row did not collapse the drawer")
	}
	h.Handle(ui.Mouse{Mouse: keys.Mouse{Button: keys.MouseLeft, Press: true, Col: 2, Row: top}})
	h.Draw()
	if !h.drawerOpen {
		t.Fatal("the drawer did not reopen")
	}
	if h.drawerPanelTop <= 0 {
		t.Fatalf("panelTop = %d, want room above the panel", h.drawerPanelTop)
	}
	h.Handle(ui.Mouse{Mouse: keys.Mouse{Button: keys.MouseLeft, Press: true, Col: 2, Row: h.drawerPanelTop - 1}})
	if h.drawerOpen {
		t.Error("a tap outside the panel did not collapse the drawer")
	}
}

// A button cell is at least two rows tall and its recorded span covers the
// whole cell, so a tap on any row or column dispatches; a row outside the cell
// does not. The real tap dispatches the same path as the chord.
func TestPhoneDrawerButtonCellTapsEveryRowAndColumn(t *testing.T) {
	h := newPhoneHarnessSize(t, reviewFixture, 120, 30)
	propose(t, h, piecetable.Hunk{Start: reviewAt, End: reviewAt + len(reviewOld), Text: reviewNew})
	h.press("super+r") // the review controls live in Review mode
	h.Pane().Cursors.Set(reviewAt+1, reviewAt+1)
	h.status = ""
	h.Draw()
	h.Handle(ui.Mouse{Mouse: keys.Mouse{Button: keys.MouseLeft, Press: true, Col: 2, Row: h.drawerHandle.y}})
	if !h.drawerOpen {
		t.Fatal("setup: the drawer did not open")
	}
	h.Draw()
	var cell drawerItem
	found := false
	for _, b := range h.drawerPanel {
		if b.label == "accept" {
			cell, found = b, true
		}
	}
	if !found {
		t.Fatalf("the accept button was not drawn: %+v", h.drawerPanel)
	}
	if cell.h != drawerCellRows {
		t.Fatalf("accept cell height = %d, want %d bordered rows", cell.h, drawerCellRows)
	}
	if cell.w < 8 {
		t.Fatalf("accept cell width = %d, want a phone-sized cell", cell.w)
	}
	for dy := 0; dy < cell.h; dy++ {
		for _, col := range []int{cell.x, cell.x + cell.w/2, cell.x + cell.w - 1} {
			if act, ok := h.drawerAt(col, cell.y+dy); !ok || act != keys.AcceptProposed {
				t.Errorf("cell (%d,%d) resolved to (%q,%v), want accept", col, cell.y+dy, act, ok)
			}
		}
	}
	if act, ok := h.drawerAt(cell.x+1, cell.y-1); ok && act == keys.AcceptProposed {
		t.Error("a row outside the accept cell still resolved to accept")
	}
	if act, ok := h.drawerAt(cell.x+1, cell.y+cell.h); ok && act == keys.AcceptProposed {
		t.Error("the row below the accept cell still resolved to accept")
	}
	h.Handle(ui.Mouse{Mouse: keys.Mouse{Button: keys.MouseLeft, Press: true, Col: cell.x + cell.w/2, Row: cell.y + cell.h - 1}})
	if !strings.Contains(h.Status(), "accepted") {
		t.Errorf("status after the tap = %q, want the accept reported", h.Status())
	}
	if marks := h.Pane().PendingMarks(); len(marks) != 0 {
		t.Errorf("marks after the tap = %+v, want none", marks)
	}
	if !h.drawerOpen {
		t.Error("a button tap closed the drawer; a review pass must be a run of taps")
	}
}

// The close button is the existing close, so it still refuses unsaved work: the
// buffer stays open and the question is asked.
func TestPhoneDrawerCloseRefusesDirty(t *testing.T) {
	h := newPhoneHarnessSize(t, "one\n", 120, 30)
	h.typeText("x") // dirty the buffer
	if !h.Pane().File.ViewDirty() {
		t.Fatal("fixture buffer is not dirty")
	}
	h.status = ""
	h.Draw()
	h.Handle(ui.Mouse{Mouse: keys.Mouse{Button: keys.MouseLeft, Press: true, Col: 2, Row: h.drawerHandle.y}})
	h.Draw()
	var cell drawerItem
	found := false
	for _, b := range h.drawerPanel {
		if b.label == "close" {
			cell, found = b, true
		}
	}
	if !found {
		t.Fatalf("the close button was not drawn: %+v", h.drawerPanel)
	}
	h.Handle(ui.Mouse{Mouse: keys.Mouse{Button: keys.MouseLeft, Press: true, Col: cell.x + cell.w/2, Row: cell.y + cell.h - 1}})
	if h.Tabs.Count() != 1 {
		t.Errorf("tabs after close on a dirty buffer = %d, want the buffer kept", h.Tabs.Count())
	}
	if !h.Prompt.Open {
		t.Error("closing a dirty buffer did not ask before discarding it")
	}
}

// esc toggles the drawer in the phone profile only; the ordinary profile keeps
// esc as Cancel and never grows a drawer.
func TestPhoneDrawerKeyToggles(t *testing.T) {
	h := newPhoneHarness(t, "one\ntwo\n")
	h.Draw()
	if h.drawerOpen {
		t.Fatal("fixture drawer is already open")
	}
	h.press("esc")
	if !h.drawerOpen {
		t.Fatal("esc did not open the phone drawer")
	}
	h.press("esc")
	if h.drawerOpen {
		t.Error("a second esc did not close the phone drawer")
	}

	n := newHarness(t, "one\ntwo\n")
	n.press("esc")
	if n.drawerOpen {
		t.Error("esc opened a drawer in the ordinary profile")
	}
}

// The drawer tables carry the review controls and the general controls, and the
// combined table holds both. Without the split Edit mode would still show the
// review buttons.
func TestPhoneDrawerButtonsDispatchTheExistingActions(t *testing.T) {
	review := map[keys.Action]bool{}
	for _, b := range drawerReviewButtons {
		review[b.action] = true
	}
	general := map[keys.Action]bool{}
	for _, b := range drawerGeneralButtons {
		general[b.action] = true
	}
	for _, want := range []keys.Action{
		keys.PrevProposed, keys.NextProposed, keys.AcceptProposed,
		keys.RejectProposed, keys.ClearRejected, keys.ReviewProposed,
	} {
		if !review[want] {
			t.Errorf("review table is missing %q", want)
		}
	}
	for _, want := range []keys.Action{keys.Save, keys.FilePicker, keys.CloseTab, keys.Quit} {
		if !general[want] {
			t.Errorf("general table is missing %q", want)
		}
	}
	all := map[keys.Action]bool{}
	for _, b := range drawerAllButtons {
		all[b.action] = true
	}
	for want := range review {
		if !all[want] {
			t.Errorf("combined table is missing the review action %q", want)
		}
	}
	for want := range general {
		if !all[want] {
			t.Errorf("combined table is missing the general action %q", want)
		}
	}
}

// The status and the handle share the two-row handle strip: the status takes
// the first row, the label the last, and expiring the status leaves the handle.
func TestPhoneStatusSharesTheHandleRow(t *testing.T) {
	h := newPhoneHarness(t, "one\ntwo\n")
	h.press("super+r") // Review
	if h.mode != ModeReview {
		t.Fatalf("fixture mode = %v after super+r, want Review", h.mode)
	}
	h.typeText("X") // refused: the read-only note
	note := h.Status()
	if note == "" {
		t.Fatal("setup: no status to show")
	}
	h.Draw()
	l := h.layout(120, 12)
	if l.BarY != 12-phoneDrawerRows {
		t.Fatalf("BarY = %d, want %d", l.BarY, 12-phoneDrawerRows)
	}
	statusRow := h.host.Last().Row(l.BarY)
	if !strings.Contains(statusRow, note) {
		t.Errorf("handle row 0 = %q, want the status %q", statusRow, note)
	}
	if !strings.Contains(h.host.Last().Row(l.BarY+1), "[actions]") {
		t.Errorf("handle row 1 = %q, want the label", h.host.Last().Row(l.BarY+1))
	}
	if got := h.host.Last().Row(l.BarY - 1); strings.Contains(got, note) {
		t.Errorf("row %d repeats the status; it must share the strip", l.BarY-1)
	}
	h.statusAt = time.Now().Add(-phoneStatusTTL - time.Second)
	h.expirePhoneStatus()
	h.Draw()
	if got := h.host.Last().Row(l.BarY + 1); !strings.Contains(got, "[actions]") {
		t.Errorf("handle row after expiry = %q, want the label back", got)
	}
}

// The phone enters Review with ctrl+r, the ctrl alias of super+r. This drives
// the whole path — key event, alias resolution, dispatch, toggleReview — so a
// dropped alias or a broken dispatch fails here, not only in the keymap.
func TestPhoneCtrlRAliasEntersReview(t *testing.T) {
	h := newPhoneHarness(t, "one\ntwo\n")
	if h.mode != ModeEdit {
		t.Fatalf("fixture mode = %v at startup, want Edit", h.mode)
	}
	h.press("ctrl+r")
	if h.mode != ModeReview {
		t.Fatalf("mode = %v after ctrl+r with aliases on, want Review", h.mode)
	}
	h.press("ctrl+r")
	if h.mode != ModeEdit {
		t.Fatalf("mode = %v after a second ctrl+r, want Edit", h.mode)
	}
}

// drawerButton finds a recorded panel button by label.
func drawerButton(t *testing.T, h *harness, label string) drawerItem {
	t.Helper()
	for _, b := range h.drawerPanel {
		if b.label == label {
			return b
		}
	}
	t.Fatalf("drawer has no %q button: %+v", label, h.drawerPanel)
	return drawerItem{}
}

// tapDrawerCell taps the centre row of a recorded button cell.
func tapDrawerCell(h *harness, b drawerItem) {
	h.Handle(ui.Mouse{Mouse: keys.Mouse{Button: keys.MouseLeft, Press: true,
		Col: b.x + b.w/2, Row: b.y + b.h/2}})
}

// openPhoneDrawer opens the collapsed handle and redraws so the panel exists.
func openPhoneDrawer(h *harness) {
	h.Handle(ui.Mouse{Mouse: keys.Mouse{Button: keys.MouseLeft, Press: true,
		Col: 2, Row: h.drawerHandle.y}})
	h.Draw()
}

// A drawer button dispatches and leaves the panel up, so a review pass is a run
// of taps. Without the keep-open change the first tap closes it and the next
// must reopen it by hand.
func TestPhoneDrawerStaysOpenAcrossButtonTaps(t *testing.T) {
	h := newPhoneHarnessSize(t, reviewFixture, 120, 30)
	propose(t, h, piecetable.Hunk{Start: reviewAt, End: reviewAt + len(reviewOld), Text: reviewNew})
	h.press("super+r") // prev and next are review controls
	h.status = ""
	h.Draw()
	openPhoneDrawer(h)
	if !h.drawerOpen {
		t.Fatal("setup: the drawer did not open")
	}
	tapDrawerCell(h, drawerButton(t, h, "prev"))
	if !h.drawerOpen {
		t.Error("prev closed the drawer")
	}
	h.Draw()
	tapDrawerCell(h, drawerButton(t, h, "next"))
	if !h.drawerOpen {
		t.Error("next closed the drawer after a second tap")
	}
}

// Each bordered cell is three rows, every row and column inside the recorded
// block is the button, and the runes and centred label are what the frame
// shows. Without the shared cell geometry the border rows would fall outside
// the hit rectangle the pointer uses.
func TestPhoneDrawerButtonBorders(t *testing.T) {
	h := newPhoneHarnessSize(t, "one\ntwo\n", 120, 30)
	h.status = ""
	h.Draw()
	openPhoneDrawer(h)
	// Inspect a genuinely unselected cell: opening the drawer leaves the
	// selection on the first general button, so save carries the marker and
	// open does not.
	cell := drawerButton(t, h, "open")
	if cell.h != drawerCellRows {
		t.Fatalf("open cell height = %d, want %d", cell.h, drawerCellRows)
	}
	for dy := 0; dy < cell.h; dy++ {
		for _, col := range []int{cell.x, cell.x + cell.w/2, cell.x + cell.w - 1} {
			if act, ok := h.drawerAt(col, cell.y+dy); !ok || act != keys.FilePicker {
				t.Errorf("cell (%d,%d) resolved to (%q,%v), want open", col, cell.y+dy, act, ok)
			}
		}
	}
	if act, ok := h.drawerAt(cell.x+1, cell.y-1); ok && act == keys.FilePicker {
		t.Error("the row above the open cell resolved to open")
	}
	if act, ok := h.drawerAt(cell.x+1, cell.y+cell.h); ok && act == keys.FilePicker {
		t.Error("the row below the open cell resolved to open")
	}

	// Measure the row and the interior by rune, not byte: the selected save on
	// the same row is what a whole-row Contains or a byte index would misread.
	frame := h.host.Last()
	top := []rune(frame.Row(cell.y))
	mid := []rune(frame.Row(cell.y + 1))
	bot := []rune(frame.Row(cell.y + 2))
	if top[cell.x] != '┌' || top[cell.x+cell.w-1] != '┐' {
		t.Errorf("top border = %q, want corner runes", string(top[cell.x:cell.x+cell.w]))
	}
	if bot[cell.x] != '└' || bot[cell.x+cell.w-1] != '┘' {
		t.Errorf("bottom border = %q, want corner runes", string(bot[cell.x:cell.x+cell.w]))
	}
	if mid[cell.x] != '│' || mid[cell.x+cell.w-1] != '│' {
		t.Errorf("label row edges = %q, want vertical runes", string(mid[cell.x:cell.x+cell.w]))
	}
	inner := mid[cell.x+1 : cell.x+cell.w-1]
	idx := -1
	for i, r := range inner {
		if r != ' ' {
			idx = i
			break
		}
	}
	pad := len(inner) - len([]rune(cell.label))
	if idx != pad/2 {
		t.Errorf("label %q not centred: index %d, want %d", string(inner), idx, pad/2)
	}
	if strings.Contains(string(top), cell.label) || strings.Contains(string(bot), cell.label) {
		t.Error("the label leaked onto a border row")
	}
}

// The review controls are drawn only in Review; the general controls are drawn
// in both modes. Without the split both modes draw the same ten buttons, and a
// phone in Edit mode offers decisions it cannot make.
func TestPhoneDrawerReviewControlsOnlyInReview(t *testing.T) {
	h := newPhoneHarnessSize(t, reviewFixture, 120, 30)
	propose(t, h, piecetable.Hunk{Start: reviewAt, End: reviewAt + len(reviewOld), Text: reviewNew})
	h.status = ""
	h.Draw()
	openPhoneDrawer(h)

	labels := func() map[string]bool {
		out := map[string]bool{}
		for _, b := range h.drawerPanel {
			out[b.label] = true
		}
		return out
	}
	edit := labels()
	for _, want := range []string{"save", "open", "close", "exit"} {
		if !edit[want] {
			t.Errorf("Edit panel is missing the general control %q: %+v", want, edit)
		}
	}
	for _, no := range []string{"prev", "next", "accept", "reject", "clear", "list"} {
		if edit[no] {
			t.Errorf("Edit panel drew the review control %q", no)
		}
	}
	if got := len(h.drawerPanel); got != len(drawerGeneralButtons) {
		t.Errorf("Edit panel has %d buttons, want %d", got, len(drawerGeneralButtons))
	}

	h.press("super+r")
	h.Draw()
	review := labels()
	for _, want := range []string{"prev", "next", "accept", "reject", "clear", "list", "save", "open", "close", "exit"} {
		if !review[want] {
			t.Errorf("Review panel is missing %q: %+v", want, review)
		}
	}
	if got := len(h.drawerPanel); got != len(drawerAllButtons) {
		t.Errorf("Review panel has %d buttons, want %d", got, len(drawerAllButtons))
	}
}

// A review action has no span in Edit mode: it is not in the table, so no cell
// resolves to it and the coordinate its Review cell occupies is outside the
// shorter Edit panel. Without the split the accept cell is drawn in Edit and a
// tap would decide a change.
func TestPhoneDrawerReviewCellAbsentInEdit(t *testing.T) {
	r := newPhoneHarnessSize(t, reviewFixture, 120, 30)
	propose(t, r, piecetable.Hunk{Start: reviewAt, End: reviewAt + len(reviewOld), Text: reviewNew})
	r.press("super+r")
	r.Draw()
	openPhoneDrawer(r)
	accept := drawerButton(t, r, "accept")

	h := newPhoneHarnessSize(t, "one\ntwo\n", 120, 30)
	h.status = ""
	h.Draw()
	openPhoneDrawer(h)
	for _, b := range h.drawerPanel {
		switch b.action {
		case keys.PrevProposed, keys.NextProposed, keys.AcceptProposed,
			keys.RejectProposed, keys.ClearRejected, keys.ReviewProposed:
			t.Errorf("Edit panel drew the review action %q at %d,%d", b.label, b.x, b.y)
		}
	}
	if act, ok := h.drawerAt(accept.x, accept.y); ok && act == keys.AcceptProposed {
		t.Error("a tap at the Review accept cell resolved to accept in Edit mode")
	}
}

// Up opens the drawer and selects the first drawn button, and the selected cell
// carries the highlight. Without the directional routing Up would scroll the
// editor and there would be no selection to highlight.
func TestPhoneDrawerUpOpensAndSelectsFirst(t *testing.T) {
	h := newPhoneHarnessSize(t, "one\ntwo\n", 120, 30)
	h.status = ""
	h.Draw()
	if h.drawerOpen {
		t.Fatal("fixture drawer is already open")
	}
	h.press("up")
	if !h.drawerOpen {
		t.Fatal("up did not open the drawer")
	}
	if h.drawerSel != 0 {
		t.Fatalf("selection = %d, want the first button", h.drawerSel)
	}
	h.Draw()
	if len(h.drawerPanel) == 0 {
		t.Fatal("the open drawer drew no buttons")
	}
	sel := h.drawerPanel[0]
	if want := drawerGeneralButtons[0].label; sel.label != want {
		t.Errorf("first Edit button = %q, want %q", sel.label, want)
	}
	frame := h.host.Last()
	if st := frame.At(sel.x, sel.y).Style; st.Fg != ui.Ansi(15) || st.Bg != ui.Ansi(4) {
		t.Errorf("selected border style = %+v, want the white-on-blue accent block", st)
	}
	innerOf := func(it drawerItem) string {
		row := []rune(frame.Row(it.y + 1))
		return string(row[it.x+1 : it.x+it.w-1])
	}
	if !strings.Contains(innerOf(sel), "▸") {
		t.Errorf("selected label %q, want the marker", innerOf(sel))
	}
	other := h.drawerPanel[1]
	if st := frame.At(other.x, other.y).Style; st.Bg == ui.Ansi(4) {
		t.Errorf("unselected border style = %+v, want the plain panel style", st)
	}
	// Inspect the unselected cell own interior: save and open share the row,
	// and a whole-row check would see the selected save marker.
	if strings.Contains(innerOf(other), "▸") {
		t.Errorf("unselected label %q, want no marker", innerOf(other))
	}
}

// Down closes an open drawer and is the editor line-down when closed. Without
// the routing Down would do nothing while open.
func TestPhoneDrawerDownClosesAndIsNormalWhenClosed(t *testing.T) {
	h := newPhoneHarnessSize(t, "one\ntwo\n", 120, 30)
	h.status = ""
	h.Draw()
	h.press("up")
	if !h.drawerOpen {
		t.Fatal("setup: up did not open the drawer")
	}
	h.press("down")
	if h.drawerOpen {
		t.Fatal("down did not close the drawer")
	}
	if line, _ := h.Pane().File.LineCol(h.Pane().Cursors.Primary().Head); line != 0 {
		t.Fatalf("cursor line = %d after up/down, want 0", line)
	}
	h.press("down")
	if line, _ := h.Pane().File.LineCol(h.Pane().Cursors.Primary().Head); line != 1 {
		t.Errorf("closed down left the cursor on line %d, want 1", line)
	}
}

// Left and Right move within the drawn, mode-filtered cells and clamp at both
// ends; they never select a button the current mode omitted. Entering Review
// grows the set and leaving it clamps the selection back in.
func TestPhoneDrawerLeftRightClamp(t *testing.T) {
	h := newPhoneHarnessSize(t, reviewFixture, 120, 30)
	propose(t, h, piecetable.Hunk{Start: reviewAt, End: reviewAt + len(reviewOld), Text: reviewNew})
	h.status = ""
	h.Draw()
	h.press("up")
	if h.drawerSel != 0 {
		t.Fatalf("selection = %d, want 0", h.drawerSel)
	}
	h.press("left")
	if h.drawerSel != 0 {
		t.Errorf("left at the first moved to %d, want a clamp", h.drawerSel)
	}
	for i := 0; i < 10; i++ {
		h.press("right")
	}
	if h.drawerSel != len(h.drawerPanel)-1 {
		t.Fatalf("right clamped at %d, want %d", h.drawerSel, len(h.drawerPanel)-1)
	}
	for _, b := range h.drawerPanel {
		switch b.action {
		case keys.PrevProposed, keys.NextProposed, keys.AcceptProposed,
			keys.RejectProposed, keys.ClearRejected, keys.ReviewProposed:
			t.Errorf("Edit selection set includes the review action %q", b.label)
		}
	}

	h.press("super+r") // entering Review grows the button set
	if h.mode != ModeReview {
		t.Fatalf("mode = %v after super+r, want Review", h.mode)
	}
	if h.drawerSel < 0 || h.drawerSel >= len(h.drawerPanel) {
		t.Fatalf("selection %d outside the Review panel of %d", h.drawerSel, len(h.drawerPanel))
	}
	for i := 0; i < 10; i++ {
		h.press("right")
	}
	if h.drawerSel != len(h.drawerPanel)-1 {
		t.Fatalf("Review right clamped at %d, want %d", h.drawerSel, len(h.drawerPanel)-1)
	}
	if got := h.drawerPanel[h.drawerSel].label; got != "exit" {
		t.Errorf("Review last = %q, want exit", got)
	}
	h.press("super+r") // leaving Review clamps back into the general set
	if h.mode != ModeEdit {
		t.Fatalf("mode = %v after the second super+r, want Edit", h.mode)
	}
	if h.drawerSel < 0 || h.drawerSel >= len(h.drawerPanel) {
		t.Fatalf("selection %d outside the Edit panel of %d", h.drawerSel, len(h.drawerPanel))
	}
}

// Tab activates the selected button through the same dispatch a tap uses.
// Without the routing Tab would indent the editor instead.
func TestPhoneDrawerTabActivatesSelection(t *testing.T) {
	h := newPhoneHarnessSize(t, "one\ntwo\n", 120, 30)
	h.typeText("x") // dirty, so the save has an observable effect
	h.status = ""
	h.Draw()
	h.press("up") // opens the drawer
	// Select save by action: the general set starts with files/search/save, so
	// the first button is no longer save.
	h.drawerSel = drawerSelIndex(t, h, keys.Save)
	h.press("tab")
	if h.Pane().File.Dirty() {
		t.Error("tab on the save selection did not save the buffer")
	}
	onDisk, err := os.ReadFile(h.Pane().File.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(onDisk), "x") {
		t.Errorf("disk = %q, want the saved text", onDisk)
	}
}

// The ordinary profile routes neither Up nor Tab through a drawer.
func TestOrdinaryProfileUpAndTabUnchanged(t *testing.T) {
	h := newHarness(t, "one\ntwo\n")
	// Build the asserted state explicitly. The frame first: a pane that has
	// never been laid out has Viewport.Cols == 0, and wrapped vertical movement
	// then falls back to a one-column text width, so Down steps a row inside
	// line 0 instead of onto line 1. Every other down test gets a frame from a
	// preceding chord; this one has to draw for itself.
	h.focusEditor()
	h.Draw()
	h.Pane().Cursors.Set(0, 0)
	if line, _ := h.Pane().File.LineCol(h.Pane().Cursors.Primary().Head); line != 0 {
		t.Fatalf("setup: cursor starts on line %d, want 0", line)
	}
	h.press("down")
	if line, _ := h.Pane().File.LineCol(h.Pane().Cursors.Primary().Head); line != 1 {
		t.Fatalf("setup: down left the cursor on line %d, want 1", line)
	}
	h.press("up")
	if h.drawerOpen {
		t.Error("the ordinary profile opened a drawer on up")
	}
	if line, _ := h.Pane().File.LineCol(h.Pane().Cursors.Primary().Head); line != 0 {
		t.Errorf("up left the cursor on line %d, want 0", line)
	}
	// Left/Right stay cursor movement in the ordinary profile.
	h.Pane().Cursors.Set(0, 0)
	h.press("right")
	if _, col := h.Pane().File.LineCol(h.Pane().Cursors.Primary().Head); col != 1 {
		t.Errorf("ordinary right left the cursor at column %d, want 1", col)
	}
	h.press("left")
	if _, col := h.Pane().File.LineCol(h.Pane().Cursors.Primary().Head); col != 0 {
		t.Errorf("ordinary left left the cursor at column %d, want 0", col)
	}
	h.Pane().Cursors.Set(0, 0)
	h.press("tab")
	if got := h.text(); got != "\tone\ntwo\n" {
		t.Errorf("tab text = %q, want the indent", got)
	}
}

// With the drawer closed, Left/Right walk the tabs in the phone profile,
// wrapping exactly as the tab-cycle chord does. With it open they move the
// selection and leave the tab alone.
func TestPhoneDrawerClosedLeftRightCycleTabs(t *testing.T) {
	h := newPhoneHarnessSize(t, "one\ntwo\n", 120, 30)
	dir := filepath.Dir(h.Pane().File.Path)
	second := filepath.Join(dir, "second.go")
	if err := os.WriteFile(second, []byte("package second\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.OpenFile(second)
	h.Draw()
	first := h.Tabs.All()[0].File.Path
	active := func() string { return h.Tabs.Active().File.Path }
	if active() != second {
		t.Fatalf("setup: active = %s, want the newly opened %s", active(), second)
	}

	h.press("left") // wraps from the second back to the first
	if active() != first {
		t.Errorf("left active = %s, want %s", active(), first)
	}
	h.press("left") // and wraps the other way at the first
	if active() != second {
		t.Errorf("left at the first active = %s, want the wrap to %s", active(), second)
	}
	h.press("right")
	if active() != first {
		t.Errorf("right active = %s, want %s", active(), first)
	}
	if h.drawerOpen {
		t.Error("left/right opened the drawer")
	}

	h.press("up")
	if !h.drawerOpen {
		t.Fatal("up did not open the drawer")
	}
	before := active()
	h.press("right")
	if active() != before {
		t.Errorf("right with the drawer open changed the tab to %s", active())
	}
	if h.drawerSel != 1 {
		t.Errorf("selection = %d, want 1", h.drawerSel)
	}
}

// The collapsed handle shows the open-tab count bottom-left beside the
// [actions] label, with a live status on the row above. Opening and closing a
// tab updates it.
func TestPhoneHandleShowsTabCount(t *testing.T) {
	h := newPhoneHarnessSize(t, "one\ntwo\n", 20, 30)
	h.Draw()
	handleRow := func() string {
		return h.host.Last().Row(h.drawerHandle.y + h.drawerHandle.h - 1)
	}
	if got := handleRow(); !strings.Contains(got, "1 tab") || !strings.Contains(got, "[actions]") {
		t.Fatalf("handle = %q, want the count and the label", got)
	}
	// A local phone has no connection, so it draws no dot; the count keeps the
	// left edge.
	if got := handleRow(); strings.Contains(got, "●") {
		t.Errorf("the local phone handle drew a connection dot: %q", got)
	}
	if r := []rune(handleRow()); len(r) == 0 || r[0] != '1' {
		t.Errorf("handle = %q, want the count at the left edge", handleRow())
	}
	dir := filepath.Dir(h.Pane().File.Path)
	second := filepath.Join(dir, "second.go")
	if err := os.WriteFile(second, []byte("package second\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.OpenFile(second)
	h.Draw()
	if got := handleRow(); !strings.Contains(got, "2 tabs") {
		t.Errorf("handle after open = %q, want 2 tabs", got)
	}
	h.closeTabAt(h.Tabs.Index())
	h.Draw()
	if got := handleRow(); !strings.Contains(got, "1 tab") {
		t.Errorf("handle after close = %q, want 1 tab", got)
	}
	// A live status shares the strip on the other row.
	h.status = "hello"
	h.Draw()
	if top := h.host.Last().Row(h.drawerHandle.y); !strings.Contains(top, "hello") {
		t.Errorf("status row = %q, want the status", top)
	}
	if bot := handleRow(); !strings.Contains(bot, "1 tab") || !strings.Contains(bot, "[actions]") {
		t.Errorf("handle row = %q, want the count and label beside the status", bot)
	}
}

// Activating open hands focus to the file picker, and the drawer must close
// first: its modal key handling would otherwise swallow every key the picker
// needs. Without the close rule the typed text never reaches the picker.
func TestPhoneDrawerOpenClosesAndPickerReceivesKeys(t *testing.T) {
	h := newPhoneHarnessSize(t, "one\ntwo\n", 120, 30)
	h.status = ""
	h.Draw()
	openPhoneDrawer(h)
	// Select open by action, not by position: files and search now sit first.
	h.drawerSel = drawerSelIndex(t, h, keys.FilePicker)
	h.press("tab")
	if h.drawerOpen {
		t.Error("activating open left the drawer up")
	}
	if !h.Picker.Open {
		t.Fatal("open did not raise the picker")
	}
	h.typeText("two")
	if got := h.Picker.Query(); got != "two" {
		t.Errorf("picker query = %q, want the typed text", got)
	}
}

// The review controls need Review mode and something pending. A decision that
// empties the set removes them again, and a local tap on accept keeps the
// drawer open because the action stays in the editor.
func TestPhoneDrawerReviewControlsNeedPendingChanges(t *testing.T) {
	h := newPhoneHarnessSize(t, reviewFixture, 120, 30)
	h.status = ""
	h.press("super+r") // Review, but nothing pending yet
	if h.mode != ModeReview {
		t.Fatalf("mode = %v, want Review", h.mode)
	}
	h.Draw()
	openPhoneDrawer(h)
	generalOnly := func() bool {
		for _, b := range h.drawerPanel {
			switch b.action {
			case keys.PrevProposed, keys.NextProposed, keys.AcceptProposed,
				keys.RejectProposed, keys.ClearRejected, keys.ReviewProposed:
				return false
			}
		}
		return true
	}
	if !generalOnly() || len(h.drawerPanel) != len(drawerGeneralButtons) {
		t.Fatalf("Review with no pending set drew %+v, want the general buttons only", h.drawerPanel)
	}

	propose(t, h, piecetable.Hunk{Start: reviewAt, End: reviewAt + len(reviewOld), Text: reviewNew})
	h.Draw()
	if generalOnly() || len(h.drawerPanel) != len(drawerAllButtons) {
		t.Fatalf("a pending set did not add the review controls: %+v", h.drawerPanel)
	}

	// Accept the set from the drawer; it stays open through the decision.
	h.Pane().Cursors.Set(reviewAt+1, reviewAt+1)
	tapDrawerCell(h, drawerButton(t, h, "accept"))
	if !h.drawerOpen {
		t.Error("accept closed the drawer")
	}
	h.Draw()
	if !generalOnly() {
		t.Fatalf("after the decision the review controls stayed: %+v", h.drawerPanel)
	}
}

// drawerSelIndex finds a drawn drawer button index by the action it runs.
func drawerSelIndex(t *testing.T, h *harness, action keys.Action) int {
	t.Helper()
	for i, b := range h.drawerPanel {
		if b.action == action {
			return i
		}
	}
	t.Fatalf("drawer has no %v button: %+v", action, h.drawerPanel)
	return -1
}

// A drawer decision that empties the pending sets moves the selection to save,
// and a save moves it to close. Without the follow-up the selection stays on
// the review button and, once the panel shrinks, clamps to the wrong cell.
func TestPhoneDrawerDecisionSelectsSaveThenClose(t *testing.T) {
	h := newPhoneHarnessSize(t, reviewFixture, 120, 30)
	propose(t, h, piecetable.Hunk{Start: reviewAt, End: reviewAt + len(reviewOld), Text: reviewNew})
	h.press("super+r")
	h.Pane().Cursors.Set(reviewAt+1, reviewAt+1)
	openPhoneDrawer(h)
	h.drawerSel = drawerSelIndex(t, h, keys.AcceptProposed)

	h.press("tab")
	if h.activePending() != 0 {
		t.Fatalf("pending = %d, want the last set decided", h.activePending())
	}
	if got := h.drawerPanel[h.drawerSel].action; got != keys.Save {
		t.Errorf("selection = %v, want save after the last decision", got)
	}

	h.press("tab")
	if got := h.drawerPanel[h.drawerSel].action; got != keys.CloseTab {
		t.Errorf("selection = %v, want close after save", got)
	}
}

// With two sets the first decision leaves the selection on the review button,
// because one set is still pending; the second decision, which empties the set,
// moves it to save.
func TestPhoneDrawerFirstDecisionKeepsReviewSelection(t *testing.T) {
	h := newPhoneHarnessSize(t, "aaa\nbbb\nccc\n", 120, 30)
	twoSets(t, h)
	h.press("super+r")
	openPhoneDrawer(h)
	accept := drawerSelIndex(t, h, keys.AcceptProposed)
	h.drawerSel = accept

	h.press("tab") // the caret auto-advanced with the accept
	if h.activePending() != 1 {
		t.Fatalf("pending = %d, want 1 left", h.activePending())
	}
	if h.drawerSel != accept {
		t.Errorf("selection = %d, want it kept on accept %d", h.drawerSel, accept)
	}
	if got := h.drawerPanel[h.drawerSel].action; got != keys.AcceptProposed {
		t.Errorf("selection = %v, want accept while a set is pending", got)
	}

	h.press("tab") // the caret is on the second set from the auto-advance
	if h.activePending() != 0 {
		t.Fatalf("pending = %d, want both decided", h.activePending())
	}
	if got := h.drawerPanel[h.drawerSel].action; got != keys.Save {
		t.Errorf("selection = %v, want save after the last decision", got)
	}
}

// Rejecting the last set from the drawer moves the selection to save too, even
// though the reject itself leaves the caret in place.
func TestPhoneDrawerRejectOfLastSelectsSave(t *testing.T) {
	h := newPhoneHarnessSize(t, reviewFixture, 120, 30)
	propose(t, h, piecetable.Hunk{Start: reviewAt, End: reviewAt + len(reviewOld), Text: reviewNew})
	h.press("super+r")
	h.Pane().Cursors.Set(reviewAt+1, reviewAt+1)
	openPhoneDrawer(h)
	h.drawerSel = drawerSelIndex(t, h, keys.RejectProposed)

	h.press("tab")
	if h.activePending() != 0 {
		t.Fatalf("pending = %d, want the last set rejected", h.activePending())
	}
	if got := h.drawerPanel[h.drawerSel].action; got != keys.Save {
		t.Errorf("selection = %v, want save after the last reject", got)
	}
}

// drawerOrder finds the indices of the navigation and save buttons in the
// drawn panel.
func drawerOrder(t *testing.T, h *harness) (files, search, save int) {
	t.Helper()
	files, search, save = -1, -1, -1
	for i, b := range h.drawerPanel {
		switch b.action {
		case keys.FocusExplorer:
			files = i
		case keys.FocusSearch:
			search = i
		case keys.Save:
			save = i
		}
	}
	return
}

// files and search are the navigation buttons: present in both modes, ordered
// before save, and activating one hands focus to the sidebar, which closes the
// drawer through the focus-change rule. Without them there is no tap route to
// the explorer or search on a phone.
func TestPhoneDrawerFilesAndSearchBeforeSave(t *testing.T) {
	h := newPhoneHarnessSize(t, "one\ntwo\n", 120, 30)
	h.status = ""
	h.Draw()
	openPhoneDrawer(h)
	files, search, save := drawerOrder(t, h)
	if files < 0 || search < 0 || save < 0 || !(files < save && search < save) {
		t.Fatalf("Edit order files=%d search=%d save=%d, want both before save", files, search, save)
	}
	tapDrawerCell(h, drawerButton(t, h, "files"))
	if h.drawerOpen {
		t.Error("files left the drawer open")
	}
	if h.Focused() != FocusSidebar || h.SidebarMode() != SidebarExplorer {
		t.Errorf("files focus=%v sidebar=%v, want the explorer", h.Focused(), h.SidebarMode())
	}

	// Review mode with a set pending still carries both, still before save.
	r := newPhoneHarnessSize(t, reviewFixture, 120, 30)
	propose(t, r, piecetable.Hunk{Start: reviewAt, End: reviewAt + len(reviewOld), Text: reviewNew})
	r.press("super+r")
	openPhoneDrawer(r)
	files, search, save = drawerOrder(t, r)
	if files < 0 || search < 0 || save < 0 || !(files < save && search < save) {
		t.Fatalf("Review order files=%d search=%d save=%d, want both before save", files, search, save)
	}
	tapDrawerCell(r, drawerButton(t, r, "search"))
	if r.drawerOpen {
		t.Error("search left the drawer open")
	}
	if r.Focused() != FocusSidebar || r.SidebarMode() != SidebarSearch {
		t.Errorf("search focus=%v sidebar=%v, want the search pane", r.Focused(), r.SidebarMode())
	}
}

// With the explorer focused the phone arrow repurposing must not apply: Up
// moves the tree selection and must not open the drawer, and Left/Right reach
// the sidebar rather than cycling tabs. Without the focus gate Up would open
// the drawer and Left/Right would switch tabs.
func TestPhoneArrowKeysReachTheFocusedSidebar(t *testing.T) {
	h := newPhoneHarnessSize(t, "one\ntwo\n", 120, 30)
	dir := filepath.Dir(h.Pane().File.Path)
	second := filepath.Join(dir, "second.go")
	if err := os.WriteFile(second, []byte("package second\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.OpenFile(second)
	h.Explorer.Tree.Refresh()
	h.Draw()
	h.handleKeyAction(keys.FocusExplorer)
	if h.Focused() != FocusSidebar {
		t.Fatalf("focus = %v, want the sidebar", h.Focused())
	}
	explorerSelect(t, h, "test.go") // the last entry, so Up has somewhere to go
	before, _ := h.Explorer.SelectedPath()
	h.press("up")
	if after, _ := h.Explorer.SelectedPath(); after == before {
		t.Errorf("up did not move the tree selection off %s", before)
	}
	if h.drawerOpen {
		t.Error("up in the explorer opened the drawer")
	}
	if h.Focused() != FocusSidebar {
		t.Fatalf("up in the explorer moved focus to %v", h.Focused())
	}

	// Left/Right reach the explorer as directory open/close, not as tab
	// cycling. Without the focus gate drawerKey consumes them and switches
	// tabs, so the directory never opens and the active tab changes.
	pkg := filepath.Join(dir, "pkg")
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.Explorer.Tree.Refresh()
	explorerSelect(t, h, "pkg")
	active := h.Tabs.Active().File.Path
	if h.Explorer.Tree.Expanded(pkg) {
		t.Fatal("setup: pkg is already open")
	}
	h.press("right")
	if !h.Explorer.Tree.Expanded(pkg) {
		t.Error("right did not open the directory")
	}
	h.press("left")
	if h.Explorer.Tree.Expanded(pkg) {
		t.Error("left did not close the directory")
	}
	if got := h.Tabs.Active().File.Path; got != active {
		t.Errorf("left/right changed the active tab to %s, want %s", got, active)
	}
	if h.drawerOpen {
		t.Error("left/right in the explorer opened the drawer")
	}

	// The handle tap is the one way to open the drawer from a sidebar.
	openPhoneDrawer(h)
	if !h.drawerOpen {
		t.Error("a handle tap did not open the drawer over the sidebar")
	}
}

// When a review is under way the drawer opens on next, the review-navigation
// button, found by action rather than position. Without the context default it
// would open on the first cell (prev in Review, files in Edit).
func TestPhoneDrawerOpensOnNextInReview(t *testing.T) {
	h := newPhoneHarnessSize(t, reviewFixture, 120, 30)
	propose(t, h, piecetable.Hunk{Start: reviewAt, End: reviewAt + len(reviewOld), Text: reviewNew})
	h.press("super+r")
	h.press("up")
	if got := h.drawerPanel[h.drawerSel].action; got != keys.NextProposed {
		t.Errorf("open in Review selected %v, want next", got)
	}
}

// Edit, and Review with nothing pending, keep the first general button as the
// open default.
func TestPhoneDrawerOpensOnFilesWithoutReview(t *testing.T) {
	edit := newPhoneHarnessSize(t, "one\ntwo\n", 120, 30)
	edit.press("up")
	if got := edit.drawerPanel[edit.drawerSel].action; got != drawerGeneralButtons[0].action {
		t.Errorf("Edit open selected %v, want %v", got, drawerGeneralButtons[0].action)
	}

	review := newPhoneHarnessSize(t, reviewFixture, 120, 30)
	review.press("super+r") // Review, nothing pending
	review.press("up")
	if got := review.drawerPanel[review.drawerSel].action; got != drawerGeneralButtons[0].action {
		t.Errorf("empty Review open selected %v, want %v", got, drawerGeneralButtons[0].action)
	}
}

// Both open gestures get the same context default: a handle tap and esc in
// Review with a pending set both land on next.
func TestPhoneDrawerTapAndEscOpenOnNextInReview(t *testing.T) {
	tap := newPhoneHarnessSize(t, reviewFixture, 120, 30)
	propose(t, tap, piecetable.Hunk{Start: reviewAt, End: reviewAt + len(reviewOld), Text: reviewNew})
	tap.press("super+r")
	tap.Draw()
	openPhoneDrawer(tap)
	if got := tap.drawerPanel[tap.drawerSel].action; got != keys.NextProposed {
		t.Errorf("handle tap selected %v, want next", got)
	}

	esc := newPhoneHarnessSize(t, reviewFixture, 120, 30)
	propose(t, esc, piecetable.Hunk{Start: reviewAt, End: reviewAt + len(reviewOld), Text: reviewNew})
	esc.press("super+r")
	esc.press("esc") // the phone esc toggles the drawer open
	if !esc.drawerOpen {
		t.Fatal("esc did not open the drawer")
	}
	if got := esc.drawerPanel[esc.drawerSel].action; got != keys.NextProposed {
		t.Errorf("esc open selected %v, want next", got)
	}
}

// A tab switch is a new drawer context: the next frame re-selects the open
// default instead of carrying the previous tab's index. The phone flow that
// reported it decided the last set on one tab and moved to save and close with
// the drawer still open; the next tab then opened on clear.
func TestPhoneDrawerSelectionResetsOnTabSwitch(t *testing.T) {
	h := newPhoneHarnessSize(t, reviewFixture, 120, 30)
	propose(t, h, piecetable.Hunk{Start: reviewAt, End: reviewAt + len(reviewOld), Text: reviewNew})
	first := h.Pane()

	// A second tab with its own pending set, prepared before the drawer opens.
	dir := filepath.Dir(first.File.Path)
	second := filepath.Join(dir, "second.go")
	if err := os.WriteFile(second, []byte("package second\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.OpenFile(second)
	secondPane := h.Pane()
	propose(t, h, piecetable.Hunk{Start: 0, End: 0, Text: "// y\n"})

	// Back to the first tab and into Review with the drawer open on next.
	if !h.Tabs.Focus(first) {
		t.Fatal("setup: could not focus the first tab")
	}
	h.press("super+r")
	openPhoneDrawer(h)

	if !h.phone {
		t.Fatal("the harness is not the phone profile")
	}
	if !h.drawerOpen {
		t.Fatal("setup: the drawer did not open")
	}
	if h.activePending() == 0 {
		t.Fatal("setup: the first tab has no pending set")
	}
	offDefault := drawerSelIndex(t, h, keys.ClearRejected)
	if got := h.drawerPanel[h.drawerSel].action; got != keys.NextProposed {
		t.Fatalf("setup: selection = %v, want the review default next", got)
	}

	// Move off the default, then switch to the other pending tab. Both panels
	// carry every button, so without the per-pane reset the stale clear index
	// survives the switch and no clamp moves it.
	h.drawerSel = offDefault
	if !h.Tabs.Focus(secondPane) {
		t.Fatal("could not focus the second tab")
	}
	h.Draw()

	if got := h.drawerPanel[h.drawerSel].action; got != keys.NextProposed {
		t.Errorf("after a tab switch selection = %v, want the default next", got)
	}
	if !h.drawerOpen {
		t.Error("the tab switch closed the drawer")
	}
}
