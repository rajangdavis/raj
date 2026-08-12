package app

import (
	"path/filepath"
	"strings"
	"testing"

	"raj/internal/keys"
	"raj/internal/ui"
)

// wheel sends notches at a column, which is how routing is decided.
func wheel(h *harness, button keys.MouseButton, col, n int) {
	for i := 0; i < n; i++ {
		h.Handle(ui.Mouse{Mouse: keys.Mouse{
			Button: button, IsWheel: true, Press: true, Col: col, Row: 5,
		}})
	}
	h.drain()
}

func editorTop(h *harness) int { return h.Pane().Viewport.Top }

// The whole point: in the alternate screen a wheel notch used to go to the
// terminal's own scrollback, which the alt screen is not part of, so nothing
// happened at all.
func TestWheelScrollsTheEditor(t *testing.T) {
	h := newHarness(t, doc(200))
	if editorTop(h) != 0 {
		t.Fatal("setup: should start at the top")
	}
	wheel(h, keys.WheelDown, 80, 1)
	if got := editorTop(h); got != wheelRows {
		t.Errorf("one notch moved the view to %d, want %d", got, wheelRows)
	}
	wheel(h, keys.WheelUp, 80, 1)
	if got := editorTop(h); got != 0 {
		t.Errorf("scrolling back left the view at %d, want 0", got)
	}
}

// The cursor stays where it was. Scrolling is looking, not navigating: moving
// the cursor would change what the next keystroke edits and put it somewhere
// nobody chose.
func TestWheelDoesNotMoveTheCursor(t *testing.T) {
	h := newHarness(t, doc(200))
	before := h.Pane().Cursors.Primary().Head
	wheel(h, keys.WheelDown, 80, 5)
	if got := h.Pane().Cursors.Primary().Head; got != before {
		t.Errorf("cursor moved from %d to %d", before, got)
	}
	if editorTop(h) == 0 {
		t.Error("the view did not move either")
	}
}

// Scrolling stops at both ends rather than running off.
func TestWheelClampsAtTheEnds(t *testing.T) {
	h := newHarness(t, doc(30))
	wheel(h, keys.WheelUp, 80, 10)
	if got := editorTop(h); got != 0 {
		t.Errorf("scrolled above the top to %d", got)
	}
	// The last line is allowed to reach the top of the pane, by the same rule
	// that governs every other kind of scrolling — see Viewport.clampTop.
	wheel(h, keys.WheelDown, 80, 200)
	// doc(30) ends with a newline, so the buffer has 31 lines and the last is
	// empty. clampTop lets that line reach the top of the pane, which is the
	// same rule every other kind of scrolling follows.
	if got, want := editorTop(h), h.Pane().File.Lines()-1; got != want {
		t.Errorf("scrolled to %d, want %d (the last line at the top)", got, want)
	}
}

// The wheel scrolls what is under the pointer, not what has focus. That is
// what a pointer is for — reaching something without going there first.
func TestWheelRoutesByPosition(t *testing.T) {
	// A short pane, so the fixture's handful of files is more than fits and
	// there is something to scroll. Sizing the window to the data beats
	// padding the fixture with files no other test wants.
	h := newWorkspace(t, 160, 6)
	h.openSidebar("shift+super+e", SidebarExplorer)
	h.OpenFile(filepath.Join(h.root, "main.go"))
	h.drain()
	if h.Focused() != FocusEditor {
		t.Fatal("setup: expected editor focus")
	}
	if n, rows := len(h.Explorer.Tree.Entries()), h.Explorer.List().Rows; n <= rows {
		t.Fatalf("setup: %d entries in %d rows, nothing to scroll", n, rows)
	}

	treeTop := h.Explorer.List().Top
	wheel(h, keys.WheelDown, 2, 1) // over the sidebar
	if h.Explorer.List().Top == treeTop {
		t.Error("a notch over the sidebar did not scroll the tree")
	}
	if editorTop(h) != 0 {
		t.Error("a notch over the sidebar scrolled the focused editor instead")
	}

	// And the converse: over the editor, the editor moves and the tree does
	// not. Together these are what "routes by position" means.
	treeTop = h.Explorer.List().Top
	wheel(h, keys.WheelDown, 120, 1)
	if editorTop(h) == 0 {
		t.Error("a notch over the editor did not scroll it")
	}
	if h.Explorer.List().Top != treeTop {
		t.Error("a notch over the editor scrolled the tree")
	}
}

// An overlay takes the wheel wherever the pointer is: it is drawn over
// everything, so scrolling the pane behind it would scroll something invisible.
func TestWheelOverAnOpenPickerScrollsThePicker(t *testing.T) {
	h := newWorkspace(t, 160, 30)
	h.OpenFile(filepath.Join(h.root, "main.go"))
	h.drain()
	h.press("super+p")
	if !h.Picker.Open {
		t.Fatal("setup: picker should be open")
	}
	before := editorTop(h)
	wheel(h, keys.WheelDown, 80, 2)
	if editorTop(h) != before {
		t.Error("the wheel reached the editor behind the picker")
	}
}

// A horizontal wheel does nothing rather than scrolling vertically, which is
// what happens if the button is not checked.
func TestHorizontalWheelIsIgnored(t *testing.T) {
	h := newHarness(t, doc(200))
	wheel(h, keys.WheelLeft, 80, 3)
	wheel(h, keys.WheelRight, 80, 3)
	if got := editorTop(h); got != 0 {
		t.Errorf("a horizontal wheel scrolled vertically to %d", got)
	}
}

// A non-wheel mouse event is carried but does nothing yet, and must not be
// mistaken for a keystroke that types into the buffer.
func TestClicksDoNothingYet(t *testing.T) {
	h := newHarness(t, "hello")
	h.Handle(ui.Mouse{Mouse: keys.Mouse{Button: keys.MouseLeft, Press: true, Col: 2, Row: 2}})
	h.drain()
	if got := h.Pane().File.Text(); got != "hello" {
		t.Errorf("a click changed the buffer to %q", got)
	}
	if strings.Contains(h.Status(), "error") {
		t.Errorf("a click reported %q", h.Status())
	}
}
