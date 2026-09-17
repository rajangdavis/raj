package widget

import (
	"strings"
	"testing"

	"raj/internal/keys"
	"raj/internal/ui"
)

// menuItems builds a list with the shape a context menu draws: a plain row, a
// row with a right-aligned hint, a disabled row, and another plain row. The
// disabled row is in the middle on purpose, so a mapping or navigation that
// drops it shifts everything after it and the tests notice.
func menuItems() []MenuItem {
	return []MenuItem{
		{Label: "Open", Key: "open", Enabled: true},
		{Label: "Reveal in Finder", Key: "reveal", Detail: "cmd+R", Enabled: true},
		{Label: "Rename", Key: "rename", Enabled: false},
		{Label: "Delete", Key: "delete", Enabled: true},
	}
}

// menuOn opens the standard menu and draws it at the given anchor. The tests
// that measure geometry share it so they cannot disagree about the fixture.
func menuOn(t *testing.T, cols, rows, anchorCol, anchorRow int) (*Menu, *ui.Screen) {
	t.Helper()
	m := &Menu{}
	m.Show("File", menuItems())
	if !m.Open() {
		t.Fatal("setup: menu did not open")
	}
	s := ui.NewScreen(cols, rows)
	m.Render(s, anchorCol, anchorRow, cols, rows, DefaultTheme())
	return m, s
}

// Down and up must step over a disabled row without landing on it. Without the
// skip in move, down from the second item parks the highlight on the dim
// "Rename" row, where enter does nothing and the menu looks stuck.
func TestMenuNavigationSkipsDisabledRows(t *testing.T) {
	m, _ := menuOn(t, 40, 8, 0, 0)
	if m.sel != 0 {
		t.Fatalf("selection starts on %d, want the first enabled row", m.sel)
	}
	m.Handle(keys.LineDown)
	if m.sel != 1 {
		t.Errorf("down = %d, want 1", m.sel)
	}
	m.Handle(keys.LineDown)
	if m.sel != 3 {
		t.Errorf("down over a disabled row = %d, want 3", m.sel)
	}
	m.Handle(keys.LineUp)
	if m.sel != 1 {
		t.Errorf("up over a disabled row = %d, want 1", m.sel)
	}
	m.Handle(keys.LineUp)
	if m.sel != 0 {
		t.Errorf("up = %d, want 0", m.sel)
	}
}

// Navigation clamps, it does not wrap. This is widget.List.Move's rule and
// complete.Popup asserts it with a reason; wrapping a short menu flings the
// highlight from the bottom to the top, which reads as a glitch.
func TestMenuNavigationClampsAtTheEnds(t *testing.T) {
	m, _ := menuOn(t, 40, 8, 0, 0)
	for i := 0; i < 8; i++ {
		m.Handle(keys.LineDown)
	}
	if m.sel != 3 {
		t.Errorf("down past the end = %d, want to stay on 3", m.sel)
	}
	for i := 0; i < 8; i++ {
		m.Handle(keys.LineUp)
	}
	if m.sel != 0 {
		t.Errorf("up past the start = %d, want to stay on 0", m.sel)
	}
}

// Home and end go to the first and last *enabled* rows; a menu with a disabled
// tail must not park the highlight where enter cannot act.
func TestMenuHomeAndEndSkipDisabled(t *testing.T) {
	m, _ := menuOn(t, 40, 8, 0, 0)
	m.Handle(keys.LineEnd)
	if m.sel != 3 {
		t.Errorf("end = %d, want 3", m.sel)
	}
	m.Handle(keys.LineStart)
	if m.sel != 0 {
		t.Errorf("home = %d, want 0", m.sel)
	}
	// The doc/nav aliases mean the same thing and must agree.
	m.Handle(keys.DocEnd)
	if m.sel != 3 {
		t.Errorf("doc end = %d, want 3", m.sel)
	}
	m.Handle(keys.DocStart)
	if m.sel != 0 {
		t.Errorf("doc start = %d, want 0", m.sel)
	}
}

// Enter returns the highlighted item's key and closes. Without the return the
// caller cannot tell which action the row meant, and without the close the menu
// stays on top of the thing it just acted on.
func TestMenuEnterChoosesTheHighlightedKey(t *testing.T) {
	m, _ := menuOn(t, 40, 8, 0, 0)
	m.Handle(keys.LineDown)
	key, chosen := m.Handle(keys.Confirm)
	if !chosen || key != "reveal" {
		t.Fatalf("enter = (%q, %v), want (reveal, true)", key, chosen)
	}
	if m.Open() {
		t.Error("choosing left the menu open")
	}
}

// Escape closes without choosing. It has to be a way out that returns no key,
// or a caller that dispatches the result would run an action the user was
// trying to cancel.
func TestMenuEscapeClosesWithoutChoosing(t *testing.T) {
	m, _ := menuOn(t, 40, 8, 0, 0)
	key, chosen := m.Handle(keys.Cancel)
	if chosen || key != "" {
		t.Errorf("escape returned (%q, %v), want no choice", key, chosen)
	}
	if m.Open() {
		t.Error("escape did not close the menu")
	}
}

// Anything the menu does not claim returns false and leaves it open, so the
// caller can tell an ignored key from a cancellation and does not lose the
// menu to a stray keystroke.
func TestMenuIgnoresUnrelatedKeys(t *testing.T) {
	m, _ := menuOn(t, 40, 8, 0, 0)
	for _, a := range []keys.Action{keys.CharLeft, keys.WordRight, keys.PageDown, keys.Save, keys.Undo, keys.None} {
		if key, chosen := m.Handle(a); chosen || key != "" {
			t.Errorf("%v returned (%q, %v), want ignored", a, key, chosen)
		}
		if !m.Open() {
			t.Fatalf("%v closed the menu", a)
		}
	}
}

// A closed menu consumes nothing, so it cannot interfere when it is not shown.
func TestClosedMenuConsumesNothing(t *testing.T) {
	m := &Menu{}
	for _, a := range []keys.Action{keys.LineDown, keys.Confirm, keys.Cancel, keys.LineStart} {
		if key, chosen := m.Handle(a); chosen || key != "" {
			t.Errorf("a closed menu returned (%q, %v) for %v", key, chosen, a)
		}
	}
}

// Enter on a disabled row does nothing: the menu stays open because a dim row
// is not a way out. A menu of only disabled rows highlights nothing, and enter
// must not choose index 0 by default.
func TestMenuEnterOnDisabledDoesNothing(t *testing.T) {
	m := &Menu{}
	m.Show("Only", []MenuItem{
		{Label: "No", Key: "no", Enabled: false},
		{Label: "Also no", Key: "also", Enabled: false},
	})
	if !m.Open() {
		t.Fatal("a menu of disabled rows should still open")
	}
	if m.sel != -1 {
		t.Errorf("nothing is enabled but sel = %d", m.sel)
	}
	m.Handle(keys.LineDown)
	m.Handle(keys.LineUp)
	if m.sel != -1 {
		t.Errorf("navigation moved onto a disabled row: sel = %d", m.sel)
	}
	key, chosen := m.Handle(keys.Confirm)
	if chosen || key != "" {
		t.Errorf("enter chose (%q, %v) on a disabled row", key, chosen)
	}
	if !m.Open() {
		t.Error("enter on a disabled row closed the menu")
	}
}

// Show with an item list must highlight the first enabled row, not row zero:
// an explorer menu whose first entry is unavailable opens with nothing
// highlighted if this is wrong.
func TestMenuShowSkipsADisabledFirstRow(t *testing.T) {
	m := &Menu{}
	m.Show("File", []MenuItem{
		{Label: "Unavailable", Key: "x", Enabled: false},
		{Label: "Open", Key: "open", Enabled: true},
	})
	if m.sel != 1 {
		t.Errorf("sel = %d, want the first enabled row (1)", m.sel)
	}
}

// An empty menu is a programming error: nothing is drawn rather than an empty
// box. Without the guard, Show opens a two-cell rectangle that answers no key
// and can only be escaped, which is worse than not opening.
func TestMenuWithNoItemsRendersNothing(t *testing.T) {
	for _, items := range [][]MenuItem{nil, {}} {
		m := &Menu{}
		m.Show("File", items)
		if m.Open() {
			t.Fatal("an empty menu opened")
		}
		s := ui.NewScreen(20, 8)
		m.Render(s, 2, 2, 20, 8, DefaultTheme())
		for y := 0; y < 8; y++ {
			if got := s.Row(y); got != "" {
				t.Errorf("row %d = %q; an empty menu drew something", y, got)
			}
		}
	}
}

// Size must report exactly the rectangle Render drew. If it reported the
// unclamped content size instead, a caller drawing anything beside the menu
// would reserve the wrong cells.
//
// Fixture: "Reveal in Finder" plus its "cmd+R" hint is the widest row, so the
// interior is 24 and the box is 26x6.
func TestMenuSizeMatchesRender(t *testing.T) {
	m, s := menuOn(t, 40, 8, 38, 5)
	if w, h := m.Size(); w != 26 || h != 6 {
		t.Fatalf("Size = %dx%d, want 26x6", w, h)
	}
	ox, oy := m.Origin()
	if got := s.At(ox, oy).Rune; got != '╭' {
		t.Errorf("top-left cell = %q, want the box corner", got)
	}
	if got := s.At(ox+25, oy+5).Rune; got != '╯' {
		t.Errorf("bottom-right cell = %q, want the box corner", got)
	}
	if got := s.At(ox-1, oy).Rune; got != ' ' {
		t.Errorf("the cell left of the box = %q, want blank", got)
	}
}

// A menu anchored at the bottom-right corner must clamp onto the screen rather
// than draw off it. Without place, the box starts at the pointer and half of it
// is outside the terminal exactly where a right-click near the edge happens.
func TestMenuClampsInsideTheScreen(t *testing.T) {
	m, _ := menuOn(t, 40, 8, 38, 5)
	ox, oy := m.Origin()
	w, h := m.Size()
	if ox < 0 || oy < 0 || ox+w > 40 || oy+h > 8 {
		t.Fatalf("box at (%d,%d)+%dx%d leaves the 40x8 screen", ox, oy, w, h)
	}
	// Pinned to the bottom edge: the anchor at row 5 has no room for the flip.
	if oy+h != 8 {
		t.Errorf("bottom = %d, want the screen edge 8", oy+h)
	}
	// Pinned to the right edge.
	if ox+w != 40 {
		t.Errorf("right = %d, want the screen edge 40", ox+w)
	}
}

// A menu with room above but not below flips above the pointer; clamping it to
// the bottom instead would cover the row the pointer is on.
func TestMenuFlipsAboveWhenThereIsRoom(t *testing.T) {
	m, s := menuOn(t, 40, 14, 38, 10)
	ox, oy := m.Origin()
	_, h := m.Size()
	if oy+h > 14 {
		t.Fatalf("box %d..%d is off a 14-row screen", oy, oy+h)
	}
	if oy != 10-h {
		t.Errorf("top = %d, want %d (flipped above the anchor)", oy, 10-h)
	}
	if ox+m.w > 40 {
		t.Errorf("right = %d is off screen", ox+m.w)
	}
	if s.At(ox, oy).Rune != '╭' {
		t.Errorf("the box was not drawn at its reported origin")
	}
}

// RowAt must map every cell of a row to that row's item and the frame and title
// to no item, so a click one row off cannot dispatch the wrong action. It is
// positional through a disabled row: the row after the dim one still maps to
// its own key.
func TestMenuRowAtMapsExactly(t *testing.T) {
	m, _ := menuOn(t, 40, 8, 38, 5)
	ox, oy := m.Origin()
	w, h := m.Size()

	for _, c := range []struct {
		row  int
		want string
	}{
		{0, ""}, // top border, which carries the title
		{1, "open"},
		{2, "reveal"},
		{3, ""}, // disabled: occupies the row, chooses nothing
		{4, "delete"},
		{5, ""}, // bottom border
	} {
		// Every cell of the row maps the same way, not just the first column.
		for x := ox; x < ox+w; x++ {
			key, ok := m.RowAt(x, oy+c.row, ox, oy)
			if !ok {
				t.Fatalf("cell (%d,%d) on row %d reported outside the menu", x, oy+c.row, c.row)
			}
			if key != c.want {
				t.Fatalf("cell (%d,%d) hit %q, want %q", x, oy+c.row, key, c.want)
			}
		}
	}

	// A cell outside the box on any side is not the menu's.
	for _, p := range [][2]int{
		{ox - 1, oy + 1}, {ox + w, oy + 1},
		{ox + 1, oy - 1}, {ox + 1, oy + h},
	} {
		if key, ok := m.RowAt(p[0], p[1], ox, oy); ok || key != "" {
			t.Errorf("cell %v outside the box returned (%q, %v)", p, key, ok)
		}
	}
}

// The detail hint is right-aligned on its row, so a chord reads as separate
// from the label rather than running into it. Without the right alignment the
// hint would follow the label, which is what the struct field exists to avoid.
func TestMenuDrawsTheDetailRightAligned(t *testing.T) {
	m, s := menuOn(t, 40, 8, 38, 5)
	ox, oy := m.Origin()
	w, _ := m.Size()
	row := s.Row(oy + 2) // "Reveal in Finder" with its cmd+R
	if !strings.Contains(row, "Reveal in Finder") || !strings.Contains(row, "cmd+R") {
		t.Fatalf("row = %q, want the label and the hint", row)
	}
	// The hint ends one cell inside the right border, so the last character is
	// at origin+w-3 (origin+w-2 is the right pad, origin+w-1 the border).
	end := ox + w - 3
	if got := s.At(end, oy+2).Rune; got != 'R' {
		t.Errorf("rightmost hint cell = %q, want R", string(got))
	}
}

// The title is drawn on the top border, where the frame rows are: it must be
// visible and must not spill past the corner.
func TestMenuDrawsTheTitleOnTheBorder(t *testing.T) {
	m, s := menuOn(t, 40, 8, 38, 5)
	ox, oy := m.Origin()
	top := s.Row(oy)
	if !strings.Contains(top, "File") {
		t.Errorf("top border = %q, want the title", top)
	}
	if got := s.At(ox, oy).Rune; got != '╭' {
		t.Errorf("top-left corner = %q", got)
	}
	if got := s.At(ox+m.w-1, oy).Rune; got != '╮' {
		t.Errorf("top-right corner = %q", got)
	}
}

// Degenerate inputs must not panic: a zero screen, a closed menu, and a Hide
// twice.
func TestMenuDegenerateInputs(t *testing.T) {
	m := &Menu{}
	m.Render(ui.NewScreen(0, 0), 0, 0, 0, 0, DefaultTheme())
	m.Hide()
	m.Hide()
	m.Show("x", nil)
	if _, ok := m.RowAt(0, 0, 0, 0); ok {
		t.Error("a closed menu hit-tested a cell")
	}
	if w, h := m.Size(); w < 0 || h < 0 {
		t.Errorf("Size = %dx%d", w, h)
	}
}
