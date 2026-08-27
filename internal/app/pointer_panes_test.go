package app

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"raj/internal/keys"
	"raj/internal/ui"
)

// The spec these tests hold the pointer to: clicking a cell does whatever the
// thing drawn in that cell says it will do.
//
// So they never compute where a row ought to be. They draw a frame, search the
// frame for the text they want to hit, and click the cell it was actually
// found in. A hit-test that drifts from its renderer fails here even when both
// halves are internally consistent — which is the only failure mode that
// matters, and the one that duplicated arithmetic in a test cannot catch.

// findCell returns the screen cell where text starts on the last presented
// frame.
//
// It converts a byte index into a display column rather than using the index
// directly. The panes draw box borders and disclosure triangles, all of which
// are multi-byte, so on any row that has one the two differ — and a test that
// confused them would aim two columns left of what it meant and then report the
// code as broken.
func findCell(t *testing.T, h *harness, text string) (col, row int) {
	t.Helper()
	s := h.host.Last()
	if s == nil {
		t.Fatal("nothing has been drawn")
	}
	_, rows := s.Size()
	for y := 0; y < rows; y++ {
		line := s.Row(y)
		i := strings.Index(line, text)
		if i < 0 {
			continue
		}
		for _, r := range line[:i] {
			col += ui.RuneWidth(r)
		}
		return col, y
	}
	t.Fatalf("%q is not on screen:\n%s", text, h.host.Text())
	return 0, 0
}

func middleClick(h *harness, col, row int) {
	h.Handle(ui.Mouse{Mouse: keys.Mouse{
		Button: keys.MouseMiddle, Press: true, Col: col, Row: row,
	}})
	h.drain()
}

// ---------- tabs ----------

// Clicking a tab switches to it. The bar is drawn from labels of varying width,
// so this is the case that would break first if the spans were recomputed.
func TestClickTabSwitches(t *testing.T) {
	h := newWorkspace(t, 120, 20)
	root := h.Explorer.Tree.Root
	h.OpenFile(filepath.Join(root, "main.go"))
	h.OpenFile(filepath.Join(root, "README.md"))
	h.drain()
	if got := h.Pane().File.Name(); got != "README.md" {
		t.Fatalf("setup: active tab is %s", got)
	}

	col, row := findCell(t, h, "main.go")
	click(h, col, row, 0)
	if got := h.Pane().File.Name(); got != "main.go" {
		t.Errorf("clicking the main.go tab left %s active", got)
	}
}

// Every tab must be reachable by clicking the label the user can see. Aiming at
// each column of each label in turn is the cheap exhaustive version of the test
// above, and it is what catches an off-by-one at a tab boundary.
func TestClickEveryColumnOfEveryTab(t *testing.T) {
	h := newWorkspace(t, 120, 20)
	root := h.Explorer.Tree.Root
	for _, name := range []string{"main.go", "README.md", "pkg/helper.go", "pkg/other.go"} {
		h.OpenFile(filepath.Join(root, name))
	}
	h.drain()

	cols, _ := h.host.Last().Size()
	l := computeLayout(cols, 20, h.sidebar, h.focus)
	for want, p := range h.Tabs.All() {
		name := p.File.Name()
		start, row := findCell(t, h, name)
		if row != l.TabY {
			t.Fatalf("%s was found on row %d, not the tab bar", name, row)
		}
		for col := start; col < start+len(name); col++ {
			click(h, col, row, 0)
			if got := h.Tabs.Index(); got != want {
				t.Fatalf("clicking column %d of %s selected tab %d, want %d",
					col, name, got, want)
			}
		}
	}
}

// Middle-click closes the tab under the pointer, which need not be the active
// one — that is the whole point of aiming at it.
func TestMiddleClickClosesTheTabUnderThePointer(t *testing.T) {
	h := newWorkspace(t, 120, 20)
	root := h.Explorer.Tree.Root
	h.OpenFile(filepath.Join(root, "main.go"))
	h.OpenFile(filepath.Join(root, "README.md"))
	h.drain()

	col, row := findCell(t, h, "main.go")
	middleClick(h, col, row)

	if h.Tabs.Count() != 1 {
		t.Fatalf("%d tabs left, want 1", h.Tabs.Count())
	}
	if got := h.Pane().File.Name(); got != "README.md" {
		t.Errorf("the wrong tab was closed; %s is left", got)
	}
}

// A tab holding unsaved work asks before closing, however it was asked to. The
// pointer must not be a way round the guard the chord respects.
func TestMiddleClickOnDirtyTabAsksFirst(t *testing.T) {
	h := newWorkspace(t, 120, 20)
	h.OpenFile(filepath.Join(h.Explorer.Tree.Root, "main.go"))
	h.typeText("x")
	if !h.Pane().File.Dirty() {
		t.Fatal("setup: the buffer is not dirty")
	}

	col, row := findCell(t, h, "main.go")
	middleClick(h, col, row)

	if h.Tabs.Count() != 1 {
		t.Error("a dirty tab was closed without asking")
	}
	if !h.Prompt.Open {
		t.Error("no dialog asked about the unsaved changes")
	}
}

// ---------- explorer ----------

// Clicking a file in the tree opens it, on the first click.
func TestClickExplorerFileOpens(t *testing.T) {
	h := newWorkspace(t, 120, 20)
	h.openSidebar("shift+super+e", SidebarExplorer)

	col, row := findCell(t, h, "main.go")
	click(h, col, row, 0)

	if h.Pane() == nil {
		t.Fatal("nothing opened")
	}
	if got := h.Pane().File.Name(); got != "main.go" {
		t.Errorf("opened %s", got)
	}
}

// Clicking a directory folds it rather than opening anything, which is what
// enter does on one and what the disclosure triangle says will happen.
func TestClickExplorerDirectoryExpands(t *testing.T) {
	h := newWorkspace(t, 120, 20)
	h.openSidebar("shift+super+e", SidebarExplorer)
	before := len(h.Explorer.Tree.Entries())

	col, row := findCell(t, h, "pkg")
	click(h, col, row, 0)

	if got := len(h.Explorer.Tree.Entries()); got <= before {
		t.Errorf("entries %d -> %d; the click should have expanded pkg/", before, got)
	}
	if h.Pane() != nil {
		t.Error("clicking a directory opened a file")
	}
}

// Clicking a binary refuses out loud. A status line under a tree that still
// shows the name it just declined reads as nothing having happened.
func TestClickBinaryInExplorerRefusesLoudly(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	bin := filepath.Join(h.Explorer.Tree.Root, "a.out")
	if err := os.WriteFile(bin, []byte("\x7fELF\x02\x00garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.Explorer.Tree.Refresh()
	h.openSidebar("shift+super+e", SidebarExplorer)

	col, row := findCell(t, h, "a.out")
	click(h, col, row, 0)

	if h.Pane() != nil {
		t.Fatal("a binary was opened")
	}
	if !h.Prompt.Open {
		t.Fatal("the refusal was silent")
	}
	if !strings.Contains(h.host.Text(), "not a text file") {
		t.Errorf("the reason is not on screen:\n%s", h.host.Text())
	}
}

// The changed-only toggle is drawn as a checkbox, so clicking it flips it.
func TestClickExplorerFilterToggle(t *testing.T) {
	h := newWorkspace(t, 120, 20)
	h.openSidebar("shift+super+e", SidebarExplorer)
	before := h.Explorer.Tree.ChangedOnly

	col, row := findCell(t, h, "changed only")
	click(h, col, row, 0)

	if h.Explorer.Tree.ChangedOnly == before {
		t.Error("clicking the checkbox did not flip it")
	}
}

// ---------- search ----------

// Clicking a match opens its file at its line.
func TestClickSearchMatchOpensAtLine(t *testing.T) {
	h := newWorkspace(t, 120, 30)
	h.openSidebar("shift+super+f", SidebarSearch)
	h.typeText("needle")
	if h.Search.Result.Files == 0 {
		t.Fatal("setup: the search found nothing")
	}

	// Matches are drawn as "<line>: <text>", so the line number is on screen
	// and the assertion can be made against what was rendered rather than
	// against a row index the renderer never saw.
	want := 0
	for _, r := range h.Search.Rows() {
		if !r.IsHdr {
			want = r.Match.Line
			break
		}
	}
	if want == 0 {
		t.Fatal("setup: no match rows were drawn")
	}
	col, row := findCell(t, h, strconv.Itoa(want)+": ")
	click(h, col, row, 0)

	if h.Pane() == nil {
		t.Fatal("clicking a match opened nothing")
	}
	line, _ := h.Pane().File.LineCol(h.Pane().Cursors.Primary().Head)
	if line+1 != want {
		t.Errorf("landed on line %d, want %d", line+1, want)
	}
}

// Clicking a file header folds it, which is what enter does on one.
func TestClickSearchHeaderFolds(t *testing.T) {
	h := newWorkspace(t, 120, 30)
	h.openSidebar("shift+super+f", SidebarSearch)
	h.typeText("needle")
	before := len(h.Search.Rows())

	var col, row int
	found := false
	for _, r := range h.Search.Rows() {
		if r.IsHdr && r.Open {
			col, row = findCell(t, h, filepath.Base(r.Path))
			found = true
			break
		}
	}
	if !found {
		t.Skip("no open group to fold")
	}
	click(h, col, row, 0)

	if got := len(h.Search.Rows()); got >= before {
		t.Errorf("rows %d -> %d; clicking a header should have folded it", before, got)
	}
	if h.Pane() != nil {
		t.Error("clicking a header opened a file")
	}
}

// A search option is drawn as a checkbox, so clicking it flips that option and
// not one of its neighbours — they sit three columns apart.
func TestClickSearchToggles(t *testing.T) {
	h := newWorkspace(t, 120, 30)
	h.openSidebar("shift+super+f", SidebarSearch)
	h.typeText("needle")

	col, row := findCell(t, h, "[ ]Aa")
	click(h, col+3, row, 0) // the "Aa" label itself

	if !h.Search.Options().Case {
		t.Error("clicking the case toggle did not turn it on")
	}
	if h.Search.Options().Regex || h.Search.Options().Word {
		t.Error("clicking the case toggle flipped a neighbour")
	}
}

// Clicking into the query field places the caret where it was clicked, so
// typing lands there rather than at the end.
func TestClickSearchFieldPlacesCaret(t *testing.T) {
	h := newWorkspace(t, 120, 30)
	h.openSidebar("shift+super+f", SidebarSearch)
	h.typeText("needle")

	col, row := findCell(t, h, "needle")
	click(h, col+3, row, 0) // between "nee" and "dle"
	h.typeText("X")

	if got := h.Search.ActiveInput().Text; got != "neeXdle" {
		t.Errorf("query = %q, want neeXdle", got)
	}
}

// ---------- overlays ----------

// A dialog is modal, so a press on the tab bar behind it must not reach it.
func TestPromptSwallowsClicksBehindIt(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	root := h.Explorer.Tree.Root
	h.OpenFile(filepath.Join(root, "main.go"))
	h.OpenFile(filepath.Join(root, "README.md"))
	h.typeText("x") // make it dirty so closing asks
	h.press("super+w")
	if !h.Prompt.Open {
		t.Fatal("setup: no dialog is open")
	}

	before := h.Tabs.Index()
	click(h, 2, 0, 0) // the first tab, behind the dialog
	if h.Tabs.Index() != before {
		t.Error("a click reached the tab bar through a modal dialog")
	}
	if !h.Prompt.Open {
		t.Error("the dialog closed itself")
	}
}

// Clicking a dialog's button answers with it rather than merely selecting it.
func TestClickPromptButtonAnswers(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	h.OpenFile(filepath.Join(h.Explorer.Tree.Root, "main.go"))
	h.typeText("x")
	h.press("super+w")
	if !h.Prompt.Open {
		t.Fatal("setup: no dialog is open")
	}

	col, row := findCell(t, h, "Don't Save")
	click(h, col, row, 0)

	if h.Prompt.Open {
		t.Error("the dialog is still open")
	}
	if h.Tabs.Count() != 0 {
		t.Errorf("%d tabs left; Don't Save should have closed it", h.Tabs.Count())
	}
}

// Clicking a result in the file picker opens it.
func TestClickPickerResultOpens(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	h.press("super+p")
	if !h.Picker.Open {
		t.Fatal("setup: the picker did not open")
	}
	h.typeText("helper")
	if h.Picker.Results() == 0 {
		t.Fatal("setup: the picker found nothing")
	}

	col, row := findCell(t, h, "helper.go")
	click(h, col, row, 0)

	if h.Pane() == nil {
		t.Fatal("clicking a result opened nothing")
	}
	if got := h.Pane().File.Name(); got != "helper.go" {
		t.Errorf("opened %s", got)
	}
}
