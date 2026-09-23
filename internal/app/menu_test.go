package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"raj/internal/keys"
	"raj/internal/prompt"
	"raj/internal/ui"
)

// The context-menu tests hold the app's half of the feature: what a press
// resolves to, what the menu is about, and where a chosen item goes. The
// widget's own geometry is covered in internal/widget; here the menu is
// addressed by the label drawn in the frame, the same way pointer_panes_test
// addresses a pane.

// rightClick sends a right-button press at a screen cell and draws.
func rightClick(h *harness, col, row int) {
	h.Handle(ui.Mouse{Mouse: keys.Mouse{
		Button: keys.MouseRight, Press: true, Col: col, Row: row,
	}})
	h.drain()
}

// explorerCell finds text in the last frame below the tab bar, so a filename
// that is also a tab label resolves to its tree row rather than to the tab.
func explorerCell(t *testing.T, h *harness, text string) (col, row int) {
	t.Helper()
	s := h.host.Last()
	if s == nil {
		t.Fatal("nothing has been drawn")
	}
	cols, rows := s.Size()
	l := computeLayout(cols, rows, h.sidebar, h.focus)
	for y := l.SidebarTop; y < l.SidebarTop+l.SidebarRows; y++ {
		line := s.Row(y)
		i := strings.Index(line, text)
		if i < 0 {
			continue
		}
		col = 0
		for _, r := range line[:i] {
			col += ui.RuneWidth(r)
		}
		if col < l.SidebarX || col >= l.SidebarX+l.SidebarW {
			col = 0
			continue // the match was in the editor, not the tree
		}
		return col, y
	}
	t.Fatalf("%q is not in the sidebar:\n%s", text, h.host.Text())
	return 0, 0
}

// menuClick finds a menu row by its label and left-clicks it.
//
// It resolves the label inside the drawn menu box rather than searching the
// whole frame. The New File item's detail is the directory the menu was opened
// in, and t.TempDir() names that directory after the running test, so a
// frame-wide search for "Rename" or "Delete" finds the test's own name in that
// detail on the New File row first and clicks the wrong item. The label text is
// drawn one cell inside the border and one past the pad, so the row is the one
// whose label column begins with the label.
func menuClick(t *testing.T, h *harness, label string) {
	t.Helper()
	s := h.host.Last()
	if s == nil {
		t.Fatal("nothing has been drawn")
	}
	ox, oy := h.Menu.Origin()
	_, mh := h.Menu.Size()
	want := []rune(label)
	for y := oy + 1; y < oy+mh-1; y++ {
		match := true
		for i, r := range want {
			if s.At(ox+2+i, y).Rune != r {
				match = false
				break
			}
		}
		if match {
			click(h, ox+2, y, 0)
			return
		}
	}
	t.Fatalf("menu row %q is not in the drawn box:\n%s", label, h.host.Text())
}

// A right-click on an explorer file row opens the file item set. Without the
// MouseRight branch in pointer, every right press returned at the "not left"
// guard and no menu ever appeared.
func TestRightClickExplorerFileOpensMenu(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	h.openSidebar("shift+super+e", SidebarExplorer)
	h.drain()

	col, row := explorerCell(t, h, "README.md")
	rightClick(h, col, row)

	if !h.Menu.Open() {
		t.Fatal("a right-click on an explorer file opened no menu")
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

// Choosing Rename from the file menu opens the shared name prompt rather than
// renaming anything on its own. Without the chooseMenu -> renamePath dispatch
// the row would be inert.
func TestMenuRenameOpensNamePrompt(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	h.openSidebar("shift+super+e", SidebarExplorer)
	h.drain()

	col, row := explorerCell(t, h, "README.md")
	rightClick(h, col, row)
	menuClick(t, h, "Rename")

	if !h.Prompt.Open {
		t.Fatal("choosing rename did not open the name prompt")
	}
	if got := h.Prompt.Title(); got != "Rename" {
		t.Errorf("prompt title = %q, want Rename", got)
	}
}

// Choosing Delete records a proposal through ProposeDeletion and raises the
// gate; it must not unlink anything before approval. Without the
// proposeDeleteFile dispatch the file would be untouched, but so would the
// proposal; this pins both halves: recorded, and still on disk.
func TestMenuDeleteProposesBeforeApproval(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	path := filepath.Join(h.primaryRoot(), "README.md")
	h.OpenFile(path)
	h.drain()
	h.openSidebar("shift+super+e", SidebarExplorer)
	h.drain()

	col, row := explorerCell(t, h, "README.md")
	rightClick(h, col, row)
	menuClick(t, h, "Delete")

	if !h.Prompt.Open {
		t.Fatal("choosing delete did not open the deletion prompt")
	}
	if _, ok := h.pendingDeletions[path]; !ok {
		t.Errorf("no pending deletion recorded for %s; pending = %v", path, h.pendingDeletions)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the file was deleted before approval: %v", err)
	}

	// Dismissing the gate keeps the proposal and the file: the prompt is the
	// only place the bytes can actually go.
	h.press("esc")
	if _, err := os.Stat(path); err != nil {
		t.Errorf("dismissing the prompt deleted the file: %v", err)
	}
	if _, ok := h.pendingDeletions[path]; !ok {
		t.Error("dismissing the prompt dropped the proposal")
	}
}

// A left-click on a menu row runs it. Copy Path reaches the host clipboard the
// same way the copy chord does, which is the observable end of the dispatch.
func TestMenuLeftClickRunsRow(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	h.openSidebar("shift+super+e", SidebarExplorer)
	h.drain()

	want := filepath.Join(h.primaryRoot(), "README.md")
	col, row := explorerCell(t, h, "README.md")
	rightClick(h, col, row)
	menuClick(t, h, "Copy Path")

	if got := h.host.Clipboard(); got != want {
		t.Errorf("clipboard = %q, want %q", got, want)
	}
	if h.Menu.Open() {
		t.Error("the menu stayed open after a row was chosen")
	}
}

// Esc closes the menu. It is the same Cancel action the widget consumes, so a
// right-click menu is dismissible exactly like every other overlay.
func TestMenuEscapeCloses(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	h.openSidebar("shift+super+e", SidebarExplorer)
	h.drain()

	col, row := explorerCell(t, h, "README.md")
	rightClick(h, col, row)
	if !h.Menu.Open() {
		t.Fatal("setup: no menu to dismiss")
	}
	h.press("esc")
	if h.Menu.Open() {
		t.Error("esc did not close the menu")
	}
}

// A click outside an open menu closes it and is swallowed: it must not focus
// or move what was underneath. Without the first-refusal branch in pointer the
// click would fall through to the editor and move the caret.
func TestMenuOutsideClickIsSwallowed(t *testing.T) {
	h := newWorkspace(t, 160, 24)
	h.OpenFile(filepath.Join(h.primaryRoot(), "main.go"))
	h.drain()
	h.openSidebar("shift+super+e", SidebarExplorer)
	h.drain()

	col, row := explorerCell(t, h, "main.go")
	rightClick(h, col, row)
	if !h.Menu.Open() {
		t.Fatal("setup: no menu")
	}

	before := h.Pane().Cursors.Primary().Head
	ox, oy := editorOrigin(h)
	click(h, ox+5, oy+2, 0) // well right of the box on the sidebar

	if h.Menu.Open() {
		t.Error("an outside click left the menu open")
	}
	if h.Focused() != FocusSidebar {
		t.Errorf("focus = %v after an outside click, want the sidebar; the click must be swallowed", h.Focused())
	}
	if got := h.Pane().Cursors.Primary().Head; got != before {
		t.Errorf("cursor moved to %d after an outside click; it must be swallowed", got)
	}
}

// A right-click on the tab bar opens the tab item set for the tab under the
// pointer, not for the active one.
func TestRightClickTabOpensTabMenu(t *testing.T) {
	h := newWorkspace(t, 160, 24)
	dir := h.Explorer.Tree.Root
	h.OpenFile(filepath.Join(dir, "main.go"))
	h.OpenFile(filepath.Join(dir, "README.md"))
	h.drain()

	col, row := findCell(t, h, "README.md")
	rightClick(h, col, row)

	if !h.Menu.Open() {
		t.Fatal("a right-click on a tab opened no menu")
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

// keys.OpenMenu opens the same menu for whatever has focus: the selected
// explorer entry when the sidebar has focus, and otherwise the active tab.
// This is the handleGlobal wiring the brief calls for; without the case the
// action fell through to the focused pane and did nothing.
func TestOpenMenuKeyOpensForFocus(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	h.openSidebar("shift+super+e", SidebarExplorer)
	h.drain()
	for i, e := range h.Explorer.Tree.Entries() {
		if strings.HasSuffix(e.Path, "README.md") {
			h.Explorer.List().Sel = i
		}
	}
	h.dispatch(keys.OpenMenu, "")
	h.drain()
	if !h.Menu.Open() {
		t.Fatal("keys.OpenMenu opened no menu for the focused explorer entry")
	}
	if h.menuTarget.kind != menuFile || !strings.HasSuffix(h.menuTarget.path, "README.md") {
		t.Fatalf("target = %+v, want the focused explorer entry", h.menuTarget)
	}

	h2 := newWorkspace(t, 120, 24)
	h2.OpenFile(filepath.Join(h2.primaryRoot(), "main.go"))
	h2.drain()
	h2.dispatch(keys.OpenMenu, "")
	h2.drain()
	if !h2.Menu.Open() {
		t.Fatal("keys.OpenMenu opened no menu for the active tab")
	}
	if h2.menuTarget.kind != menuTab || !strings.HasSuffix(h2.menuTarget.path, "main.go") {
		t.Fatalf("target = %+v, want the active tab", h2.menuTarget)
	}
}

// A press that resolves to no target opens nothing: the explorer heading, an
// empty tab bar, and the editor all have no item set. Without explorerRowAt's
// bounds check the heading would resolve to row zero and open the first file's
// menu instead.
func TestMenuNeedsATarget(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	h.openSidebar("shift+super+e", SidebarExplorer)
	h.drain()
	l := computeLayout(120, 24, h.sidebar, h.focus)
	rightClick(h, l.SidebarX+2, l.SidebarTop) // the heading row
	if h.Menu.Open() {
		t.Error("a right-click on the explorer heading opened a menu")
	}

	empty := newWorkspace(t, 120, 24)
	rightClick(empty, 60, 0) // the tab bar with no tabs
	if empty.Menu.Open() {
		t.Error("a right-click on an empty tab bar opened a menu")
	}

	editor := newHarness(t, "hello\n")
	ox, oy := editorOrigin(editor)
	rightClick(editor, ox+2, oy)
	if editor.Menu.Open() {
		t.Error("a right-click in the editor opened a menu")
	}
}

// Choosing New File from the explorer menu opens the ordinary save-as prompt,
// seeded with the folder the menu was opened in, and nothing reaches disk until
// the save completes. The item used to ask for a name and write the file with a
// bare os.WriteFile before opening it; this pins the replacement: the prompt is
// AskPath's (tab completes) and the buffer's save is what makes the file.
func TestMenuNewFileOpensASeededSaveAsPrompt(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	h.openSidebar("shift+super+e", SidebarExplorer)
	h.drain()

	col, row := explorerCell(t, h, "README.md")
	rightClick(h, col, row)
	menuClick(t, h, "New File")

	if !h.Prompt.Open {
		t.Fatal("New File opened no prompt")
	}
	if got := h.Prompt.Title(); got != "Save as" {
		t.Errorf("prompt title = %q, want Save as", got)
	}
	want := h.primaryRoot() + string(filepath.Separator)
	if got := h.Prompt.Text(); got != want {
		t.Errorf("seed = %q, want the menu's folder %q", got, want)
	}
	// The scratch buffer has no path yet: the buffer's save is what will make
	// the file, not the menu item.
	if got := h.Pane().File.Path; got != "" {
		t.Errorf("scratch buffer path = %q, want unnamed before the save", got)
	}
	// The prompt is save-as's own field, so tab completes here too.
	h.typeText("REA")
	h.press("tab")
	if got := h.Prompt.Text(); !strings.HasSuffix(got, "README.md") {
		t.Errorf("field = %q, want tab to complete README.md", got)
	}
	// Nothing reaches disk until the save runs.
	fresh := filepath.Join(h.primaryRoot(), "notes.md")
	setPromptText(h, fresh)
	if _, err := os.Stat(fresh); !os.IsNotExist(err) {
		t.Errorf("a file existed before the save completed (err=%v)", err)
	}
}

// Completing the seeded prompt creates the file through the buffer's ordinary
// save, and the buffer that made it becomes that file rather than a leftover
// untitled tab.
func TestMenuNewFileCompletesThroughSaveAs(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	h.openSidebar("shift+super+e", SidebarExplorer)
	h.drain()

	col, row := explorerCell(t, h, "README.md")
	rightClick(h, col, row)
	menuClick(t, h, "New File")

	path := filepath.Join(h.primaryRoot(), "notes.md")
	setPromptText(h, path)
	h.press("enter")

	if h.Prompt.Open {
		t.Fatalf("dialog still open; title = %q", h.Prompt.Title())
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the save did not create the file: %v", err)
	}
	if got := h.Pane().File.Path; got != path {
		t.Errorf("buffer path = %q, want %q; the buffer should be the new file", got, path)
	}
}

// The menu's New File inherits the missing-parent offer because it runs the
// same save-as path: naming a file in a folder that is not there asks before
// creating the tree, which the old create-immediately item never did.
func TestMenuNewFileOffersToCreateAMissingParent(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	h.openSidebar("shift+super+e", SidebarExplorer)
	h.drain()

	col, row := explorerCell(t, h, "README.md")
	rightClick(h, col, row)
	menuClick(t, h, "New File")

	path := filepath.Join(h.primaryRoot(), "new", "deep", "notes.md")
	setPromptText(h, path)
	h.press("enter")
	if !h.Prompt.Open {
		t.Fatalf("no create-directory dialog; status = %q", h.Status())
	}
	answer(h, prompt.Create)

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the file was not created through the missing-parent offer: %v", err)
	}
}

// Cancelling the menu's save-as prompt leaves neither a file nor the scratch
// buffer the item opened. The old item created the file before opening it, so
// this is the buffer the new path has to clean up after itself.
func TestMenuNewFileCancelLeavesNoFileOrTab(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	h.openSidebar("shift+super+e", SidebarExplorer)
	h.drain()
	before := h.Tabs.Count()

	col, row := explorerCell(t, h, "README.md")
	rightClick(h, col, row)
	menuClick(t, h, "New File")
	if !h.Prompt.Open {
		t.Fatal("setup: New File opened no prompt")
	}
	// Name a file, then back out: the answer that would have created it is
	// never given.
	cancelled := filepath.Join(h.primaryRoot(), "cancelled.md")
	setPromptText(h, cancelled)
	h.press("esc")

	if h.Prompt.Open {
		t.Fatal("escape did not dismiss the prompt")
	}
	if got := h.Tabs.Count(); got != before {
		t.Errorf("tab count = %d after cancelling, want %d; the scratch buffer was left behind", got, before)
	}
	if _, err := os.Stat(cancelled); !os.IsNotExist(err) {
		t.Errorf("a file was created despite cancelling (err=%v)", err)
	}
}
