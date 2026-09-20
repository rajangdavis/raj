package app

import (
	"os"
	"path/filepath"
	"strings"
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

// A ctrl+left-click is the context-menu gesture where the terminal cannot
// deliver a right button. On an explorer row it opens that entry's menu,
// exactly as a right-click does. Without the ctrl branch in pointer the press
// is an ordinary left click: it falls through to clickSidebar, which opens the
// file, and no menu ever appears.
func TestCtrlClickExplorerRowOpensMenu(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	h.openSidebar("shift+super+e", SidebarExplorer)
	h.drain()

	col, row := explorerCell(t, h, "README.md")
	click(h, col, row, keys.ModCtrl)

	if !h.Menu.Open() {
		t.Fatal("a ctrl+left-click on an explorer file opened no menu")
	}
	if h.menuTarget.kind != menuFile || !strings.HasSuffix(h.menuTarget.path, "README.md") {
		t.Fatalf("target = %+v, want the README.md file", h.menuTarget)
	}
	frame := h.host.Text()
	for _, label := range []string{"Open", "New File", "New Folder", "Rename", "Delete", "Copy Path"} {
		if !strings.Contains(frame, label) {
			t.Errorf("file menu is missing %q:\n%s", label, frame)
		}
	}
}

// The ctrl substitute reaches the tab bar too and opens the menu for the tab
// under the pointer, not for the active one. Without the ctrl branch the press
// is a plain left click, which selects the tab and opens no menu.
func TestCtrlClickTabOpensTabMenu(t *testing.T) {
	h := newWorkspace(t, 160, 24)
	dir := h.Explorer.Tree.Root
	h.OpenFile(filepath.Join(dir, "main.go"))
	h.OpenFile(filepath.Join(dir, "README.md"))
	h.drain()

	col, row := findCell(t, h, "README.md")
	click(h, col, row, keys.ModCtrl)

	if !h.Menu.Open() {
		t.Fatal("a ctrl+left-click on a tab opened no menu")
	}
	if h.menuTarget.kind != menuTab || !strings.HasSuffix(h.menuTarget.path, "README.md") {
		t.Fatalf("target = %+v, want the README.md tab", h.menuTarget)
	}
	frame := h.host.Text()
	for _, label := range []string{"Close", "Save", "Rename File", "Delete File", "Reveal"} {
		if !strings.Contains(frame, label) {
			t.Errorf("tab menu is missing %q:\n%s", label, frame)
		}
	}
}

// A ctrl+left-click that is not on a menu target is still a plain left click:
// in the editor it moves the caret and opens nothing. This is why the gesture
// is scoped by menuTargetAt. Without that scope — with the ctrl check wired
// straight to rightClick — the press would resolve no target, rightClick would
// return without opening anything, and the caret would never move.
func TestCtrlClickInEditorIsAPlainClick(t *testing.T) {
	h := newHarness(t, "first line\nsecond line\nthird line\n")
	ox, oy := editorOrigin(h)

	click(h, ox+3, oy+1, keys.ModCtrl)

	if h.Menu.Open() {
		t.Error("a ctrl+left-click in the editor opened a menu")
	}
	line, col := h.Pane().File.LineCol(h.Pane().Cursors.Primary().Head)
	if line != 1 || col != 3 {
		t.Errorf("cursor at %d:%d, want 1:3; ctrl+left must stay a plain click", line, col)
	}
}

// A plain left-click is untouched: it selects the tab under the pointer and
// opens no menu. The ctrl condition is what keeps the gesture from being wired
// to every left press, which would open a menu instead of selecting.
func TestPlainLeftClickStillSelectsNoMenu(t *testing.T) {
	h := newWorkspace(t, 160, 24)
	dir := h.Explorer.Tree.Root
	h.OpenFile(filepath.Join(dir, "main.go"))
	h.OpenFile(filepath.Join(dir, "README.md"))
	h.drain()
	if h.Tabs.Index() != 1 {
		t.Fatalf("setup: active tab = %d, want README.md at 1", h.Tabs.Index())
	}

	col, row := findCell(t, h, "main.go")
	click(h, col, row, 0)

	if h.Menu.Open() {
		t.Error("a plain left-click on a tab opened a menu")
	}
	if got := h.Tabs.Index(); got != 0 {
		t.Errorf("active tab = %d after clicking main.go, want 0", got)
	}
}

// Inside an open menu a ctrl+left-click chooses the row, exactly as a plain
// left-click does: the first-refusal block in pointer takes any left press
// before the gesture is classified. This passes before the change as well; it
// is the ordering guard — a ctrl check placed above that block would dismiss
// the menu and resolve the press again instead of choosing.
func TestCtrlClickChoosesOpenMenuRow(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	h.openSidebar("shift+super+e", SidebarExplorer)
	h.drain()

	col, row := explorerCell(t, h, "README.md")
	rightClick(h, col, row)
	if !h.Menu.Open() {
		t.Fatal("setup: no menu")
	}
	// The first row is Open, which opens the target file; a press that is not
	// chosen leaves the menu open and changes nothing.
	ox, oy := h.Menu.Origin()
	click(h, ox+2, oy+1, keys.ModCtrl)

	if h.Menu.Open() {
		t.Error("a ctrl+left-click on a menu row did not choose it")
	}
	if p := h.Pane(); p == nil || !strings.HasSuffix(p.File.Path, "README.md") {
		t.Errorf("the Open row did not run; pane = %v", p)
	}
}

// In the phone profile an explorer entry is two rows tall and the whole block
// is the target: a tap on the second row, at any column across the sidebar,
// selects that entry. Without the tall rows the second row is the next entry
// and the tap opens the wrong file.
func TestPhoneExplorerTallBlockIsTheTapTarget(t *testing.T) {
	h := newPhoneHarnessSize(t, "x\n", 120, 30)
	dir := filepath.Dir(h.Tabs.Active().File.Path)
	first := filepath.Join(dir, "aaa.go")
	if err := os.WriteFile(first, []byte("package aaa\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.Explorer.Tree.Refresh()
	h.drain()
	if !h.Explorer.Tall {
		t.Fatal("the phone profile did not make the explorer rows tall")
	}
	l := h.layout(120, 30)
	second := h.Tabs.Active().File.Path
	// Entries sort aaa.go then test.go. The second row of a block is the
	// header, the blocks above it, and one row into its own block.
	cases := []struct {
		row  int
		want string
	}{
		{l.SidebarTop + 3, first},
		{l.SidebarTop + 5, second},
	}
	cols := []int{l.SidebarX + 1, l.SidebarX + l.SidebarW/2, l.SidebarX + l.SidebarW - 1}
	for _, tc := range cases {
		for _, col := range cols {
			h.Explorer.List().Sel = -1
			click(h, col, tc.row, 0)
			if got := h.Tabs.Active().File.Path; got != tc.want {
				t.Errorf("a tap at column %d, row %d selected %s, want %s", col, tc.row, got, tc.want)
			}
		}
	}
}

// The context-menu hit test shares the explorer RowAt: both rows of a tall
// block resolve to that entry, at every column inside the sidebar, and the
// first row of the next block is the neighbour. Without the shared mapping the
// app would resolve the lower row to the next entry.
func TestPhoneExplorerMenuRowUsesTallBlocks(t *testing.T) {
	h := newPhoneHarnessSize(t, "x\n", 120, 30)
	dir := filepath.Dir(h.Tabs.Active().File.Path)
	if err := os.WriteFile(filepath.Join(dir, "aaa.go"), []byte("package aaa\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.Explorer.Tree.Refresh()
	h.drain()
	l := h.layout(120, 30)
	cols := []int{l.SidebarX + 1, l.SidebarX + l.SidebarW/2, l.SidebarX + l.SidebarW - 1}
	for _, row := range []int{l.SidebarTop + 2, l.SidebarTop + 3} {
		for _, col := range cols {
			idx, e, ok := h.explorerRowAt(l, col, row)
			if !ok || idx != 0 || e.Name != "aaa.go" {
				t.Errorf("explorerRowAt(col %d, row %d) = (%d, %q, %v), want entry 0 aaa.go",
					col, row, idx, e.Name, ok)
			}
		}
	}
	if _, e, ok := h.explorerRowAt(l, l.SidebarX+1, l.SidebarTop+4); !ok || e.Name != "test.go" {
		t.Errorf("the next block's first row = %q/%v, want test.go", e.Name, ok)
	}
}
