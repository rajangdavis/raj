package app

import (
	"testing"
	"time"

	"raj/internal/keys"
	"raj/internal/ui"
)

// click sends a press at a screen cell.
func click(h *harness, col, row int, mods int) {
	h.Handle(ui.Mouse{Mouse: keys.Mouse{
		Button: keys.MouseLeft, Press: true, Col: col, Row: row, Mods: mods,
	}})
	h.drain()
}

func release(h *harness) {
	h.Handle(ui.Mouse{Mouse: keys.Mouse{Button: keys.MouseLeft, Col: 0, Row: 0}})
	h.drain()
}

func dragTo(h *harness, col, row int) {
	h.Handle(ui.Mouse{Mouse: keys.Mouse{
		Button: keys.MouseLeft, Press: true, Motion: true, Col: col, Row: row,
	}})
	h.drain()
}

// editorOrigin is where the text area starts, which is what a test has to aim
// at: the gutter and the tab bar are not the editor.
//
// It draws first, because a pane learns its width from being rendered and a
// wrapped pane that has never been drawn wraps at the wrong column — so a click
// resolved against it lands somewhere the user would never have seen. The real
// app draws after every event, so this is the state a pointer always meets.
func editorOrigin(h *harness) (x, y int) {
	h.Draw()
	cols, rows := h.screen.Size()
	l := computeLayout(cols, rows, h.sidebar, h.focus)
	return l.EditorX + h.Pane().GutterWidth(), l.TopY
}

// A click puts the cursor where it landed.
func TestClickMovesTheCursor(t *testing.T) {
	h := newHarness(t, "first line\nsecond line\nthird line\n")
	ox, oy := editorOrigin(h)
	click(h, ox+3, oy+1, 0)

	line, col := h.Pane().File.LineCol(h.Pane().Cursors.Primary().Head)
	if line != 1 || col != 3 {
		t.Errorf("cursor at %d:%d, want 1:3", line, col)
	}
}

// A press outside the text area is not the editor's. Clicking the gutter must
// not move the cursor to column zero of that line by accident.
func TestClickOutsideTheTextAreaIsIgnored(t *testing.T) {
	h := newHarness(t, "first line\nsecond line\n")
	ox, oy := editorOrigin(h)
	before := h.Pane().Cursors.Primary().Head

	click(h, ox-1, oy+1, 0) // the gutter
	if got := h.Pane().Cursors.Primary().Head; got != before {
		t.Errorf("a click in the gutter moved the cursor to %d", got)
	}
}

// A drag selects, and keeps its anchor where the press was.
func TestDragSelects(t *testing.T) {
	h := newHarness(t, "hello world\n")
	ox, oy := editorOrigin(h)

	click(h, ox, oy, 0)
	dragTo(h, ox+5, oy)
	c := h.Pane().Cursors.Primary()
	if !c.HasSelection() {
		t.Fatal("dragging produced no selection")
	}
	lo, hi := c.Range()
	if got := h.Pane().File.Text()[lo:hi]; got != "hello" {
		t.Errorf("selected %q, want hello", got)
	}
}

// Motion with no button held down must not extend anything: the release ended
// the drag, and a pointer wandering across the screen afterwards is not a
// selection.
func TestMotionAfterReleaseDoesNothing(t *testing.T) {
	h := newHarness(t, "hello world\n")
	ox, oy := editorOrigin(h)

	click(h, ox, oy, 0)
	dragTo(h, ox+5, oy)
	release(h)
	before := h.Pane().Cursors.Primary()

	dragTo(h, ox+11, oy)
	after := h.Pane().Cursors.Primary()
	if after.Head != before.Head || after.Anchor != before.Anchor {
		t.Error("motion after release changed the selection")
	}
}

// A release outside the editor still ends the drag. Leaving the flag set would
// make the next pointer movement extend a selection nobody is holding.
func TestReleaseOutsideTheEditorEndsTheDrag(t *testing.T) {
	h := newWorkspace(t, 160, 30)
	h.OpenFile(h.root + "/main.go")
	h.drain()
	ox, oy := editorOrigin(h)

	click(h, ox, oy, 0)
	h.Handle(ui.Mouse{Mouse: keys.Mouse{Button: keys.MouseLeft, Col: 0, Row: 0}})
	h.drain()
	if h.drag {
		t.Error("a release outside the editor left the drag running")
	}
}

// Two rapid presses in one place select a word; three select the line. Two far
// apart are two clicks however quickly they arrive.
func TestDoubleAndTripleClick(t *testing.T) {
	h := newHarness(t, "alpha beta gamma\nsecond line\n")
	ox, oy := editorOrigin(h)

	click(h, ox+7, oy, 0)
	click(h, ox+7, oy, 0)
	lo, hi := h.Pane().Cursors.Primary().Range()
	if got := h.Pane().File.Text()[lo:hi]; got != "beta" {
		t.Errorf("double click selected %q, want beta", got)
	}

	click(h, ox+7, oy, 0)
	lo, hi = h.Pane().Cursors.Primary().Range()
	if got := h.Pane().File.Text()[lo:hi]; got != "alpha beta gamma\n" {
		t.Errorf("triple click selected %q, want the line", got)
	}
}

func TestClicksFarApartAreNotADoubleClick(t *testing.T) {
	h := newHarness(t, "alpha beta gamma\n")
	ox, oy := editorOrigin(h)

	click(h, ox+1, oy, 0)
	click(h, ox+12, oy, 0)
	if h.Pane().Cursors.Primary().HasSelection() {
		t.Error("two clicks in different places selected a word")
	}
}

// A slow second press is a new click. The tracker is a real threshold, not
// "whatever happened last".
func TestSlowSecondClickIsNotADouble(t *testing.T) {
	var c clickTracker
	now := time.Now()
	if got := c.press(5, 5, now); got != 1 {
		t.Errorf("first press counted %d", got)
	}
	if got := c.press(5, 5, now.Add(clickInterval*2)); got != 1 {
		t.Errorf("a slow second press counted %d, want 1", got)
	}
}

// A fourth click starts over rather than doing nothing, so hammering the
// button cycles rather than sticking on "line selected".
func TestFourthClickStartsOver(t *testing.T) {
	var c clickTracker
	now := time.Now()
	want := []int{1, 2, 3, 1, 2}
	for i, w := range want {
		if got := c.press(1, 1, now.Add(time.Duration(i)*10*time.Millisecond)); got != w {
			t.Errorf("press %d counted %d, want %d", i+1, got, w)
		}
	}
}

// Shift-click extends from where the cursor is, which is how a selection is
// made without holding the button.
func TestShiftClickExtends(t *testing.T) {
	h := newHarness(t, "hello world\n")
	ox, oy := editorOrigin(h)

	click(h, ox, oy, 0)
	click(h, ox+5, oy, keys.ModShift)
	c := h.Pane().Cursors.Primary()
	if !c.HasSelection() {
		t.Fatal("shift-click did not extend")
	}
	lo, hi := c.Range()
	if got := h.Pane().File.Text()[lo:hi]; got != "hello" {
		t.Errorf("selected %q, want hello", got)
	}
}

// A modifier click adds a cursor rather than moving the existing one.
func TestModifierClickAddsACursor(t *testing.T) {
	h := newHarness(t, "one\ntwo\nthree\n")
	ox, oy := editorOrigin(h)

	click(h, ox, oy, 0)
	click(h, ox, oy+2, keys.ModSuper)
	if n := len(h.Pane().Cursors.All()); n != 2 {
		t.Errorf("%d cursors, want 2", n)
	}
}

// Clicking the editor focuses it, so the next keystroke goes where the click
// went rather than to whatever had focus before.
func TestClickFocusesTheEditor(t *testing.T) {
	h := newWorkspace(t, 160, 30)
	h.OpenFile(h.root + "/main.go")
	h.drain()
	h.openSidebar("shift+super+e", SidebarExplorer)
	if h.Focused() != FocusSidebar {
		t.Fatal("setup: expected sidebar focus")
	}

	ox, oy := editorOrigin(h)
	click(h, ox, oy, 0)
	if h.Focused() != FocusEditor {
		t.Error("clicking the editor did not focus it")
	}
}

// Right and middle buttons do nothing rather than something surprising.
func TestOtherButtonsDoNothing(t *testing.T) {
	h := newHarness(t, "hello\n")
	before := h.Pane().Cursors.Primary().Head
	for _, b := range []keys.MouseButton{keys.MouseRight, keys.MouseMiddle} {
		h.Handle(ui.Mouse{Mouse: keys.Mouse{Button: b, Press: true, Col: 20, Row: 5}})
		h.drain()
	}
	if got := h.Pane().Cursors.Primary().Head; got != before {
		t.Errorf("cursor moved to %d", got)
	}
}

// Clicking with no file open must not panic.
func TestClickWithNoPane(t *testing.T) {
	h := newWorkspace(t, 120, 30)
	for _, p := range h.Tabs.All() {
		h.Tabs.Focus(p)
		h.Tabs.Close()
	}
	h.drain()
	click(h, 20, 5, 0)
	dragTo(h, 25, 6)
	release(h)
}
