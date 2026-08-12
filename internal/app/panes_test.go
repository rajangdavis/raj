package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"raj/internal/ui"
)

// workspace builds a small tree to explore and search.
func workspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"main.go":           "package main\n\nfunc main() {\n\tneedle()\n}\n",
		"README.md":         "# project\n\nsome needle text\n",
		"pkg/helper.go":     "package pkg\n\nfunc needle() {}\n",
		"pkg/other.go":      "package pkg\n\nfunc unrelated() {}\n",
		"node_modules/x.go": "package x\n\nfunc needle() {}\n",
	}
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func newWorkspace(t *testing.T, cols, rows int) *harness {
	t.Helper()
	host := ui.NewFakeHost(cols, rows)
	t.Cleanup(func() { host.Close() })
	a := New(host, workspace(t), 2)
	a.Search.Debounce = time.Nanosecond // tests wait on Settle, not on the clock
	return &harness{App: a, host: host}
}

// openSidebar puts focus on a sidebar without assuming where it started. The
// chord toggles, so pressing it blindly may close a sidebar that already had
// focus — which is right for a user and wrong for a test setup step.
func (h *harness) openSidebar(chord string, want Sidebar) {
	if h.SidebarMode() == want && h.Focused() == FocusSidebar {
		h.drain()
		return
	}
	h.press(chord)
}

func TestExplorerOpensFile(t *testing.T) {
	h := newWorkspace(t, 120, 20)
	h.openSidebar("shift+super+e", SidebarExplorer)
	if h.Focused() != FocusSidebar {
		t.Fatal("the explorer should have focus")
	}
	// The tree lists directories first, so pkg/ is selected; skip to a file.
	h.press("down", "down") // pkg/, then main.go or README.md
	h.press("enter")
	if h.Pane() == nil {
		t.Fatal("nothing opened")
	}
	if h.Focused() != FocusEditor {
		t.Error("opening a file should move focus to the editor")
	}
}

func TestExplorerExpandsDirectory(t *testing.T) {
	h := newWorkspace(t, 120, 20)
	h.openSidebar("shift+super+e", SidebarExplorer)
	before := len(h.Explorer.Tree.Entries())
	h.press("enter") // pkg/ is first
	if got := len(h.Explorer.Tree.Entries()); got <= before {
		t.Errorf("entries %d -> %d; expanding should reveal children", before, got)
	}
}

// Tab walks the sidebar's components and, past the last, leaves for the editor.
// Shift+tab must not bring it back — that is the one-way rule.
func TestSidebarTabEscapesOneWay(t *testing.T) {
	h := newWorkspace(t, 120, 20)
	h.openSidebar("shift+super+e", SidebarExplorer)

	// Focus lands on the tree, which is the last stop now that the toggle is
	// drawn under the heading, so shift+tab is what reaches the toggle.
	h.press("shift+tab") // tree -> changed-only toggle
	if h.Focused() != FocusSidebar {
		t.Fatal("shift+tab left the sidebar")
	}
	h.press("tab") // toggle -> tree
	if h.Focused() != FocusSidebar {
		t.Fatal("tab left the sidebar too early")
	}
	h.press("tab") // past the last component -> editor
	if h.Focused() != FocusEditor {
		t.Fatal("tab did not hand focus to the editor")
	}

	h.press("shift+tab")
	if h.Focused() != FocusEditor {
		t.Error("shift+tab returned to the sidebar; it must not")
	}
	h.press("shift+super+e")
	if h.Focused() != FocusSidebar {
		t.Error("the chord should bring focus back")
	}
}

// Once focus is in the editor, tab is indentation rather than navigation.
func TestTabIndentsInEditor(t *testing.T) {
	h := newHarness(t, "line")
	h.press("tab")
	if got := h.text(); got != "  line" {
		t.Errorf("text = %q, want an indent", got)
	}
	if h.Focused() != FocusEditor {
		t.Error("tab moved focus out of the editor")
	}
}

func TestSearchFindsAndOpens(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	h.openSidebar("shift+super+f", SidebarSearch)
	h.typeText("needle")
	if n := len(h.Search.Result.Matches); n == 0 {
		t.Fatal("no matches for a string present in three files")
	}
	for _, m := range h.Search.Result.Matches {
		if strings.Contains(m.Path, "node_modules") {
			t.Error("node_modules should be skipped")
		}
	}
	h.press("enter")
	if h.Pane() == nil {
		t.Fatal("enter on a result opened nothing")
	}
	if h.Focused() != FocusEditor {
		t.Error("opening a result should focus the editor")
	}
}

// The include field must narrow results by glob.
func TestSearchIncludeGlob(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	h.openSidebar("shift+super+f", SidebarSearch)
	h.typeText("needle")
	all := len(h.Search.Result.Matches)
	h.press("tab") // query -> include
	h.typeText("*.md")
	got := len(h.Search.Result.Matches)
	if got == 0 || got >= all {
		t.Errorf("include glob gave %d of %d matches; expected a strict subset", got, all)
	}
	for _, m := range h.Search.Result.Matches {
		if !strings.HasSuffix(m.Path, ".md") {
			t.Errorf("non-matching file %s survived the include glob", m.Path)
		}
	}
}

// A bad regex must report rather than crash, and only when regex mode is on.
func TestSearchBadRegex(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	h.openSidebar("shift+super+f", SidebarSearch)
	h.typeText("a(b")
	if h.Search.Result.Err != nil {
		t.Error("literal mode should not treat a(b as a pattern")
	}
	h.press("tab", "tab", "tab") // to the regex toggle
	h.typeText(" ")
	if h.Search.Result.Err == nil {
		t.Error("regex mode should report an invalid pattern")
	}
}

func TestPickerOpensFile(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	h.press("super+p")
	if h.Focused() != FocusPicker {
		t.Fatal("cmd+p did not focus the picker")
	}
	h.typeText("helper")
	h.press("enter")
	if h.Pane() == nil || !strings.HasSuffix(h.Pane().File.Path, "helper.go") {
		t.Fatalf("picker opened %v", h.Pane())
	}
	if h.Picker.Open {
		t.Error("picker should close after choosing")
	}
}

func TestPickerEscapeCancels(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	h.press("super+p")
	h.press("esc")
	if h.Picker.Open || h.Focused() == FocusPicker {
		t.Error("escape should dismiss the picker")
	}
}

func TestTabsOpenCloseReopen(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	dir := h.Explorer.Tree.Root
	h.OpenFile(filepath.Join(dir, "main.go"))
	h.OpenFile(filepath.Join(dir, "README.md"))
	if got := h.Tabs.Count(); got != 2 {
		t.Fatalf("tabs = %d, want 2", got)
	}
	h.press("super+w")
	if got := h.Tabs.Count(); got != 1 {
		t.Fatalf("after close tabs = %d, want 1", got)
	}
	h.press("shift+super+t")
	if got := h.Tabs.Count(); got != 2 {
		t.Errorf("after reopen tabs = %d, want 2", got)
	}
}

// Closing the last tab leaves raj running with an empty editor.
func TestCloseLastTabKeepsRunning(t *testing.T) {
	h := newHarness(t, "content")
	h.press("super+w")
	if h.quit {
		t.Fatal("closing the last tab quit raj")
	}
	if h.Pane() != nil {
		t.Error("expected no active pane")
	}
	if !strings.Contains(h.host.Text(), "no file open") {
		t.Errorf("empty state not drawn:\n%s", h.host.Text())
	}
}

func TestOpeningSameFileReusesTab(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	p := filepath.Join(h.Explorer.Tree.Root, "main.go")
	h.OpenFile(p)
	h.OpenFile(p)
	if got := h.Tabs.Count(); got != 1 {
		t.Errorf("tabs = %d, want 1", got)
	}
}

func TestTabSwitching(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	dir := h.Explorer.Tree.Root
	h.OpenFile(filepath.Join(dir, "main.go"))
	h.OpenFile(filepath.Join(dir, "README.md"))
	h.press("alt+super+left")
	if !strings.HasSuffix(h.Pane().File.Path, "main.go") {
		t.Errorf("prev tab landed on %s", h.Pane().File.Path)
	}
	h.press("alt+super+right")
	if !strings.HasSuffix(h.Pane().File.Path, "README.md") {
		t.Errorf("next tab landed on %s", h.Pane().File.Path)
	}
}

// Below the narrow breakpoint only one pane draws, and which one follows focus.
func TestNarrowLayoutShowsOnePane(t *testing.T) {
	wide := computeLayout(120, 24, SidebarExplorer, FocusSidebar)
	if !wide.ShowSidebar || !wide.ShowEditor {
		t.Error("a wide window should show both panes")
	}
	narrow := computeLayout(70, 24, SidebarExplorer, FocusSidebar)
	if !narrow.ShowSidebar || narrow.ShowEditor {
		t.Error("a narrow window with sidebar focus should show only the sidebar")
	}
	narrowEd := computeLayout(70, 24, SidebarExplorer, FocusEditor)
	if narrowEd.ShowSidebar || !narrowEd.ShowEditor {
		t.Error("a narrow window with editor focus should show only the editor")
	}
	if narrowEd.EditorW != 70 {
		t.Errorf("editor width = %d, want the full 70 columns", narrowEd.EditorW)
	}
}

// cmd+b closes the sidebar and returns focus to the editor.
func TestToggleSidebar(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	h.openSidebar("shift+super+e", SidebarExplorer)
	h.press("super+b")
	if h.SidebarMode() != SidebarNone {
		t.Error("cmd+b did not close the sidebar")
	}
	if h.Focused() != FocusEditor {
		t.Error("closing the sidebar should focus the editor")
	}
	h.press("super+b")
	if h.SidebarMode() == SidebarNone {
		t.Error("cmd+b did not reopen the sidebar")
	}
}

// Pressing a sidebar's own chord while it has focus closes it.
func TestSidebarChordToggles(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	h.openSidebar("shift+super+e", SidebarExplorer)
	h.press("shift+super+e")
	if h.SidebarMode() != SidebarNone {
		t.Error("repeating the chord should close the sidebar")
	}
}

// Switching between sidebars keeps focus rather than closing.
func TestSwitchBetweenSidebars(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	h.openSidebar("shift+super+e", SidebarExplorer)
	h.press("shift+super+f")
	if h.SidebarMode() != SidebarSearch || h.Focused() != FocusSidebar {
		t.Errorf("sidebar = %v focus = %v, want search with focus", h.SidebarMode(), h.Focused())
	}
}

// Results are grouped: a header per file, then one row per hit.
func TestSearchGroupsByFile(t *testing.T) {
	h := newWorkspace(t, 120, 30)
	h.openSidebar("shift+super+f", SidebarSearch)
	h.typeText("needle")

	rows := h.Search.Rows()
	if len(rows) == 0 {
		t.Fatal("no rows")
	}
	if !rows[0].IsHdr {
		t.Error("the first row should be a file header")
	}
	// Every header must be followed by exactly the number of match rows it
	// claims, all for its own file. That is the structural property; counting
	// headers against matches only distinguishes them when some file has more
	// than one hit.
	matches := 0
	for i := 0; i < len(rows); i++ {
		if !rows[i].IsHdr {
			continue
		}
		if rows[i].Count < 1 {
			t.Fatalf("header for %s claims %d matches", rows[i].Path, rows[i].Count)
		}
		for j := 1; j <= rows[i].Count; j++ {
			if i+j >= len(rows) {
				t.Fatalf("header for %s claims %d matches but the list ends", rows[i].Path, rows[i].Count)
			}
			r := rows[i+j]
			if r.IsHdr || r.Match.Path != rows[i].Path {
				t.Fatalf("row %d under %s is %+v", i+j, rows[i].Path, r)
			}
			matches++
		}
		i += rows[i].Count
	}
	if matches != len(h.Search.Result.Matches) {
		t.Errorf("%d match rows for %d matches", matches, len(h.Search.Result.Matches))
	}
}

// A file with several hits gets one header, not one per hit.
func TestSearchGroupsRepeatedHits(t *testing.T) {
	h := newWorkspace(t, 120, 30)
	h.openSidebar("shift+super+f", SidebarSearch)
	h.typeText("func")

	headers := map[string]int{}
	for _, r := range h.Search.Rows() {
		if r.IsHdr {
			headers[r.Path]++
		}
	}
	for path, n := range headers {
		if n != 1 {
			t.Errorf("%s got %d headers, want 1", path, n)
		}
	}
	if len(headers) == 0 {
		t.Fatal("no results for func")
	}
}

// Enter on a header folds the group; enter on a match opens the file.
func TestSearchHeaderTogglesAndMatchOpens(t *testing.T) {
	h := newWorkspace(t, 120, 30)
	h.openSidebar("shift+super+f", SidebarSearch)
	h.typeText("needle")
	h.press("tab", "tab", "tab", "tab", "tab", "tab") // into the results list

	before := len(h.Search.Rows())
	h.press("enter") // fold the first file
	if got := len(h.Search.Rows()); got >= before {
		t.Errorf("rows %d -> %d; folding should hide matches", before, got)
	}
	if h.Pane() != nil {
		t.Error("folding a header must not open a file")
	}
	if r := h.Search.Rows()[h.Search.List().Sel]; !r.IsHdr {
		t.Error("selection should stay on the header it folded")
	}

	h.press("enter") // unfold
	if got := len(h.Search.Rows()); got != before {
		t.Errorf("rows %d after unfolding, want %d", got, before)
	}
	h.press("down") // onto the first match
	h.press("enter")
	if h.Pane() == nil {
		t.Fatal("enter on a match opened nothing")
	}
}

// Left and right collapse and expand without moving off the header.
func TestSearchArrowsFold(t *testing.T) {
	h := newWorkspace(t, 120, 30)
	h.openSidebar("shift+super+f", SidebarSearch)
	h.typeText("needle")
	h.press("tab", "tab", "tab", "tab", "tab", "tab")

	before := len(h.Search.Rows())
	h.press("left")
	if got := len(h.Search.Rows()); got >= before {
		t.Errorf("left did not fold: rows %d -> %d", before, got)
	}
	h.press("right")
	if got := len(h.Search.Rows()); got != before {
		t.Errorf("right did not unfold: rows = %d, want %d", got, before)
	}
}

// A resize must force a full repaint: the terminal's contents outside the old
// geometry are undefined, so the previous frame is not a safe diff basis.
func TestResizeInvalidates(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	h.drain()
	before := h.host.Invalidations()
	h.Handle(ui.Resize{Cols: 100, Rows: 30})
	h.Draw()
	if h.host.Invalidations() <= before {
		t.Error("resize did not invalidate the frame")
	}
}

// A layout change moves every pane boundary, so it invalidates too.
func TestLayoutChangeInvalidates(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	h.openSidebar("shift+super+e", SidebarExplorer)
	before := h.host.Invalidations()
	h.press("super+b") // closing the sidebar gives the editor the full width
	if h.host.Invalidations() <= before {
		t.Error("closing the sidebar did not invalidate the frame")
	}
}

// Opening a binary must be declined with a message, not rendered. Its bytes as
// text are unreadable, unsaveable without corruption, and used to include
// escape sequences the terminal executed.
func TestOpenBinaryIsDeclined(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	bin := filepath.Join(h.Explorer.Tree.Root, "a.out")
	if err := os.WriteFile(bin, []byte("\x7fELF\x02\x00\x1b[2J\x00garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.OpenFile(bin)
	h.Draw()

	if h.Pane() != nil {
		t.Fatal("a binary file was opened")
	}
	if !strings.Contains(h.Status(), "binary") {
		t.Errorf("status = %q, want a binary-file message", h.Status())
	}
	if strings.ContainsAny(h.host.Text(), "\x1b\x00") {
		t.Error("binary content reached the frame")
	}
}

// Each search option is its own tab stop and flips with space or enter. They
// were previously a single unmarked row that only responded to typing r, c or
// w, which is undiscoverable.
func TestSearchTogglesAreFocusable(t *testing.T) {
	h := newWorkspace(t, 120, 30)
	h.openSidebar("shift+super+f", SidebarSearch)
	h.typeText("needle")

	h.press("tab", "tab", "tab") // query -> include -> exclude -> regex
	h.typeText(" ")
	if !h.Search.Options().Regex {
		t.Error("space on the regex stop did not enable it")
	}
	h.press("tab")
	h.press("enter")
	if !h.Search.Options().Case {
		t.Error("enter on the case stop did not enable it")
	}
	h.press("tab")
	h.typeText(" ")
	if !h.Search.Options().Word {
		t.Error("space on the whole-word stop did not enable it")
	}
	h.typeText(" ")
	if h.Search.Options().Word {
		t.Error("space did not turn it back off")
	}
}

// A selected row in an unfocused pane must look different from both a selected
// row in a focused pane and an ordinary row, or a sidebar you have tabbed away
// from still looks like it is taking keystrokes.
func TestFocusStatesAreDistinct(t *testing.T) {
	th := widgetTheme()
	sel := th.Focus(true, true)
	inactive := th.Focus(true, false)
	plain := th.Focus(false, false)
	if sel == inactive || sel == plain || inactive == plain {
		t.Errorf("focus states collide: selected=%+v inactive=%+v plain=%+v", sel, inactive, plain)
	}
	if th.Heading(true) == th.Heading(false) {
		t.Error("focused and unfocused headings look the same")
	}
}

// cmd+f opens the in-buffer bar, steps through matches, and escape closes it.
func TestFindInFileThroughKeybindings(t *testing.T) {
	h := newHarness(t, "alpha beta alpha gamma alpha")
	h.press("super+f")
	h.typeText("alpha")
	p := h.Pane()
	if !p.Find.Open {
		t.Fatal("cmd+f did not open the find bar")
	}
	if n := len(p.Find.Matches()); n != 3 {
		t.Fatalf("matches = %d, want 3", n)
	}
	first := p.Cursors.Primary().Head
	h.press("super+f") // again steps to the next match
	if p.Cursors.Primary().Head == first {
		t.Error("repeating cmd+f did not advance")
	}
	if !strings.Contains(h.host.Text(), "find:") {
		t.Errorf("find bar not rendered:\n%s", h.host.Text())
	}
	h.press("esc")
	if p.Find.Open {
		t.Error("escape did not close the find bar")
	}
}

// Typing while find is open goes to the query, not the buffer.
func TestFindCapturesTyping(t *testing.T) {
	h := newHarness(t, "abc")
	h.press("super+f")
	h.typeText("b")
	if got := h.text(); got != "abc" {
		t.Errorf("buffer changed while find was open: %q", got)
	}
	if got := h.Pane().Find.Query(); got != "b" {
		t.Errorf("query = %q, want b", got)
	}
}

// cmd+c writes to the system clipboard via the host.
func TestCopyWritesClipboard(t *testing.T) {
	h := newHarness(t, "copy me\nsecond")
	h.press("super+a")
	h.press("super+c")
	if got := h.host.Clipboard(); got != "copy me\nsecond" {
		t.Errorf("clipboard = %q", got)
	}
}

func TestCutRemovesAndCopies(t *testing.T) {
	h := newHarness(t, "cut this")
	h.press("super+a")
	h.press("super+x")
	if got := h.text(); got != "" {
		t.Errorf("buffer = %q, want empty", got)
	}
	if got := h.host.Clipboard(); got != "cut this" {
		t.Errorf("clipboard = %q", got)
	}
}

// The whole chain for the new editing chords.
func TestNewEditingChords(t *testing.T) {
	cases := []struct {
		name, content, chord, want string
	}{
		{"move line down", "a\nb", "alt+down", "b\na"},
		{"copy line down", "a", "shift+alt+down", "a\na"},
		{"toggle comment", "x := 1", "super+/", "// x := 1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, c.content)
			h.press(c.chord)
			if got := h.text(); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// cmd+d selects the word, then adds a cursor per press.
func TestAddNextOccurrenceChord(t *testing.T) {
	h := newHarness(t, "go go go")
	h.press("super+d")
	if n := h.Pane().Cursors.Count(); n != 1 {
		t.Fatalf("first press gave %d cursors, want 1 selection", n)
	}
	h.press("super+d")
	h.press("super+d")
	if n := h.Pane().Cursors.Count(); n != 3 {
		t.Errorf("cursors = %d, want 3", n)
	}
}

// Highlighting must never run during a frame: chroma costs tens of
// milliseconds and rendering happens on every keystroke.
func TestRenderDoesNotBlockOnSyntax(t *testing.T) {
	h := newHarness(t, strings.Repeat("func f() { x := 1 }\n", 400))
	start := time.Now()
	for i := 0; i < 20; i++ {
		h.typeText("x")
	}
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Errorf("20 keystrokes took %v; highlighting is on the render path", d)
	}
}

// One user action is one undo step. Typing with several cursors used to take
// one cmd+z per cursor, and the intermediate states were ones no user created.
func TestUndoIsOneStepPerAction(t *testing.T) {
	h := newHarness(t, "go go go")
	h.press("super+d", "super+d", "super+d") // select word, then two more
	if n := h.Pane().Cursors.Count(); n != 3 {
		t.Fatalf("cursors = %d, want 3", n)
	}
	h.typeText("X")
	if got := h.text(); got != "X X X" {
		t.Fatalf("after typing = %q", got)
	}
	h.press("super+z")
	if got := h.text(); got != "go go go" {
		t.Errorf("one undo gave %q; it should reverse the whole action", got)
	}
	h.press("shift+super+z")
	if got := h.text(); got != "X X X" {
		t.Errorf("one redo gave %q", got)
	}
}

// A multi-line action is also a single step.
func TestUndoGroupsMultiLineActions(t *testing.T) {
	h := newHarness(t, "a\nb\nc")
	h.press("super+a")
	h.press("tab") // indents three lines
	if got := h.text(); got != "  a\n  b\n  c" {
		t.Fatalf("after indent = %q", got)
	}
	h.press("super+z")
	if got := h.text(); got != "a\nb\nc" {
		t.Errorf("one undo gave %q, want the whole indent reversed", got)
	}
}

// A reopened tab must be highlighted like any other, not left plain because it
// bypassed the normal open path.
func TestReopenedTabIsHighlighted(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	h.OpenFile(filepath.Join(h.Explorer.Tree.Root, "main.go"))
	h.press("super+w")
	h.press("shift+super+t")
	p := h.Pane()
	if p == nil {
		t.Fatal("reopen produced no pane")
	}
	if !p.File.Syntax.Enabled() {
		t.Fatal("reopened go file is not highlighted")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		h.Handle(ui.Tick{})
		if p.File.Syntax.Ready() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Error("a reopened file never tokenised")
}

// A freshly opened file tokenises without needing an edit first.
func TestOpenedFileTokenisesWithoutEditing(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	h.OpenFile(filepath.Join(h.Explorer.Tree.Root, "main.go"))
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		h.Handle(ui.Tick{})
		if h.Pane().File.Syntax.Ready() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Error("an untouched file never tokenised")
}

// Editing must retokenise promptly, not only when the idle tick comes round.
func TestSyntaxRefreshesAfterEdit(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	h.OpenFile(filepath.Join(h.Explorer.Tree.Root, "main.go"))
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !h.Pane().File.Syntax.Ready() {
		h.Handle(ui.Tick{})
		time.Sleep(2 * time.Millisecond)
	}
	h.typeText("// x\n") // a keystroke, with no tick afterwards
	for time.Now().Before(deadline) {
		if h.Pane().File.Syntax.Ready() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Error("editing did not trigger a retokenise")
}

// Undo must move the cursor to the change. A cursor left at a stale offset
// reports a column past the end of its line, and the view scrolls sideways —
// which is what made undo look like it was mangling the whole file.
func TestUndoMovesCursorToTheChange(t *testing.T) {
	h := newHarness(t, "line one\nline two\nline three")
	h.press("super+down") // to the end of the document
	h.typeText("X")
	h.press("super+up") // back to the top, far from the edit
	h.press("super+z")

	off := h.Pane().Cursors.Primary().Head
	if line := h.Pane().File.LineOf(off); line != 2 {
		t.Errorf("cursor on line %d after undo, want the line that changed", line)
	}
	if off > h.Pane().File.Len() {
		t.Errorf("cursor at %d is past the buffer end %d", off, h.Pane().File.Len())
	}
	if left := h.Pane().Viewport.Left; left != 0 {
		t.Errorf("view scrolled sideways to %d after undo", left)
	}
}

// Switching tabs gives the editor focus, so reaching a tab from a sidebar is one
// step. Both directions are checked: cmd+1-9 went back to the terminal, so the
// cycling chords are now the only way in and carry the focus behaviour alone.
func TestTabSwitchFocusesEditor(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	dir := h.Explorer.Tree.Root
	h.OpenFile(filepath.Join(dir, "main.go"))
	h.OpenFile(filepath.Join(dir, "README.md"))
	h.openSidebar("shift+super+e", SidebarExplorer)
	if h.Focused() != FocusSidebar {
		t.Fatal("setup: expected sidebar focus")
	}
	h.press("alt+super+left")
	if h.Focused() != FocusEditor {
		t.Error("prev-tab did not focus the editor")
	}
	h.openSidebar("shift+super+e", SidebarExplorer)
	h.press("alt+super+right")
	if h.Focused() != FocusEditor {
		t.Error("next-tab did not focus the editor")
	}
}

// Tab steps through find matches rather than indenting.
func TestFindTabCyclesMatches(t *testing.T) {
	h := newHarness(t, "aa bb aa cc aa")
	h.press("super+f")
	h.typeText("aa")
	first := h.Pane().Cursors.Primary().Head
	h.press("tab")
	second := h.Pane().Cursors.Primary().Head
	if second == first {
		t.Fatal("tab did not advance to the next match")
	}
	if got := h.text(); got != "aa bb aa cc aa" {
		t.Errorf("tab indented the buffer: %q", got)
	}
	h.press("shift+tab")
	if h.Pane().Cursors.Primary().Head != first {
		t.Error("shift+tab did not go back")
	}
}

// Cutting a whole line leaves the cursor at the start of the line that took its
// place, not at a column past the end of it.
func TestCutLineCursorPosition(t *testing.T) {
	h := newHarness(t, "aaaaaaaaaa\nbb\ncc")
	h.press("super+right") // end of line 0
	h.press("super+x")
	if got := h.text(); got != "bb\ncc" {
		t.Fatalf("text = %q", got)
	}
	off := h.Pane().Cursors.Primary().Head
	line, col := h.Pane().File.LineCol(off)
	if line != 0 || col != 0 {
		t.Errorf("cursor at %d:%d, want 0:0", line, col)
	}
}

// cmd+up and cmd+down move between the search query and its results, which is
// the only move you make often: the glob fields are set once and left alone.
func TestSearchJumpsBetweenQueryAndResults(t *testing.T) {
	h := newWorkspace(t, 120, 30)
	h.openSidebar("shift+super+f", SidebarSearch)
	h.typeText("needle")
	if len(h.Search.Rows()) == 0 {
		t.Fatal("setup: no results")
	}

	h.press("super+down") // straight to the results
	h.press("enter")      // folds the first header
	if h.Search.Rows()[0].Open {
		t.Error("cmd+down did not reach the results list")
	}

	h.press("super+up") // back to the query field
	h.typeText("XX")
	if got := h.Search.Rows(); len(got) != 0 {
		t.Errorf("typing after cmd+up did not reach the query: %d rows remain", len(got))
	}
}

// Undoing a multi-cursor edit restores every cursor, not one at whichever site
// happened to be committed last — which was the bottom of the file, since edits
// apply highest-offset-first.
func TestUndoRestoresMultiCursor(t *testing.T) {
	h := newHarness(t, "go\ngo\ngo")
	h.press("super+d", "super+d", "super+d")
	if n := h.Pane().Cursors.Count(); n != 3 {
		t.Fatalf("cursors = %d, want 3", n)
	}
	h.typeText("X")
	h.press("super+z")

	if got := h.text(); got != "go\ngo\ngo" {
		t.Fatalf("text = %q", got)
	}
	if n := h.Pane().Cursors.Count(); n != 3 {
		t.Errorf("cursors = %d after undo, want 3 restored", n)
	}
	if line := h.Pane().File.LineOf(h.Pane().Cursors.Primary().Head); line != 0 {
		t.Errorf("primary cursor on line %d, want the first edit site", line)
	}
}

// A paste is one edit: one undo step, and a handful of pieces however many
// cursors are active.
func TestPasteIsASingleEdit(t *testing.T) {
	h := newHarness(t, "a\nb\nc")
	h.press("super+d", "super+d") // two cursors
	before := h.Pane().File.Pieces()
	h.Handle(ui.Paste{Text: strings.Repeat("pasted\n", 50)})
	h.Draw()

	if got := h.Pane().File.Pieces() - before; got > 3 {
		t.Errorf("paste created %d pieces, want at most 3", got)
	}
	if !strings.Contains(h.text(), "pasted") {
		t.Fatal("paste did not land")
	}
	h.press("super+z")
	if got := h.text(); got != "a\nb\nc" {
		t.Errorf("one undo gave %q; a paste should be one step", got)
	}
}

// Paging scrolls the view and leaves every cursor where it was. Moving the
// cursor with the view reads fine with one caret and badly with several: paging
// to look at something else would collapse a multi-cursor set.
func TestPageScrollsWithoutMovingCursors(t *testing.T) {
	h := newHarnessSize(t, strings.Repeat("x\n", 200), 80, 12)
	h.drain()
	h.press("pgdown")

	if line := h.Pane().File.LineOf(h.Pane().Cursors.Primary().Head); line != 0 {
		t.Errorf("cursor moved to line %d; paging must not move it", line)
	}
	if top := h.Pane().Viewport.Top; top == 0 {
		t.Error("viewport did not scroll")
	}
	h.press("pgup")
	if top := h.Pane().Viewport.Top; top != 0 {
		t.Errorf("viewport top = %d after paging back, want 0", top)
	}
}

// The view stays where it was scrolled to until something moves a cursor, and
// then it snaps back — otherwise an edit would land off screen.
func TestCursorMotionPullsTheViewBack(t *testing.T) {
	h := newHarnessSize(t, strings.Repeat("x\n", 200), 80, 12)
	h.drain()
	h.press("pgdown", "pgdown")
	if h.Pane().Viewport.Top == 0 {
		t.Fatal("viewport did not scroll")
	}
	h.press("down")
	line := h.Pane().File.LineOf(h.Pane().Cursors.Primary().Head)
	if !h.Pane().Viewport.Visible(line) {
		t.Errorf("cursor on line %d is off screen at top %d", line, h.Pane().Viewport.Top)
	}
}

// A multi-cursor set must survive paging: this is the case the old behaviour
// got wrong, since moving the cursor collapses the set to one.
func TestPagePreservesMultiCursor(t *testing.T) {
	h := newHarnessSize(t, strings.Repeat("x\n", 200), 80, 12)
	h.drain()
	h.press("alt+super+down", "alt+super+down")
	before := h.Pane().Cursors.Count()
	if before != 3 {
		t.Fatalf("cursor count = %d, want 3", before)
	}
	h.press("pgdown")
	if got := h.Pane().Cursors.Count(); got != before {
		t.Errorf("cursor count = %d after paging, want %d", got, before)
	}
}

// Shift+page still moves the cursor: it is a selection, and a selection needs
// an end to move.
func TestSelPageStillMovesTheCursor(t *testing.T) {
	h := newHarnessSize(t, strings.Repeat("x\n", 200), 80, 12)
	h.drain()
	h.press("shift+pgdown")
	if !h.Pane().Cursors.Primary().HasSelection() {
		t.Error("shift+pgdown selected nothing")
	}
}

// With nothing open, raj starts in the explorer: an editor with no file is not
// a useful place for the keys to be.
func TestStartsInExplorerWhenNothingOpen(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	h.drain()
	if h.Focused() != FocusSidebar || h.SidebarMode() != SidebarExplorer {
		t.Errorf("focus = %v sidebar = %v, want the explorer", h.Focused(), h.SidebarMode())
	}
}

// Naming a file on the command line takes focus to it instead.
func TestOpeningAFileFocusesTheEditor(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	h.OpenFile(filepath.Join(h.Explorer.Tree.Root, "main.go"))
	if h.Focused() != FocusEditor {
		t.Errorf("focus = %v, want the editor", h.Focused())
	}
}

// The debug pane records what arrived and what it resolved to, which is the
// only way to tell a chord Ghostty swallowed from one that did nothing.
func TestDebugPaneRecordsKeys(t *testing.T) {
	h := newHarness(t, "hello")
	h.press("shift+ctrl+d")
	if !h.Debug.Open {
		t.Fatal("ctrl+shift+d did not open the debug pane")
	}
	h.typeText("a")
	h.press("super+s")

	lines := h.Debug.Lines()
	if len(lines) < 2 {
		t.Fatalf("recorded %d lines, want at least 2", len(lines))
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "super+s") || !strings.Contains(joined, "save") {
		t.Errorf("save chord not recorded:\n%s", joined)
	}
	if !strings.Contains(joined, `text "a"`) {
		t.Errorf("typed text not recorded:\n%s", joined)
	}
	h.Handle(ui.Tick{})
	h.Draw()
	if !strings.Contains(h.host.Text(), "pieces") {
		t.Errorf("statistics not rendered:\n%s", h.host.Text())
	}
	h.press("shift+ctrl+d")
	if h.Debug.Open {
		t.Error("the chord did not close the pane")
	}
}

// A paste must reach whatever has focus. Gating it on the editor meant pasting
// into the search box or the picker vanished silently: the payload arrives as
// one event, so the text fields never saw it and had nothing to fall back to.
func TestPasteIntoSearchField(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	h.press("shift+super+f")
	if h.Focused() != FocusSidebar || h.SidebarMode() != SidebarSearch {
		t.Fatal("setup: expected search focus")
	}
	h.Handle(ui.Paste{Text: "needle"})
	h.drain()
	if got := h.Search.Options().Text; got != "needle" {
		t.Errorf("query = %q, want %q", got, "needle")
	}
}

// A multi-line paste into a single-line field takes the first line: the field
// has nowhere to put the rest, and literal newlines render as placeholders.
func TestPasteIntoFieldTakesFirstLine(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	h.press("shift+super+f")
	h.Handle(ui.Paste{Text: "first\nsecond\nthird"})
	h.drain()
	if got := h.Search.Options().Text; got != "first" {
		t.Errorf("query = %q, want %q", got, "first")
	}
}

// A path pasted from a shell carries a trailing newline, which must not be
// searched for literally.
func TestPasteTrimsTrailingNewline(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	h.press("shift+super+f")
	h.Handle(ui.Paste{Text: "main.go\n"})
	h.drain()
	if got := h.Search.Options().Text; got != "main.go" {
		t.Errorf("query = %q, want %q", got, "main.go")
	}
}

// Picking a file must open the file, not a blank buffer named after it. The
// index is relative to the workspace, so what the picker hands back has to be
// resolved against the workspace rather than against the shell's cwd — which
// is the same thing only when raj was started from the directory it is editing.
func TestPickerOpensTheRealFile(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	h.press("super+p")
	h.typeText("helper")
	h.press("enter")

	if h.Pane() == nil {
		t.Fatal("nothing opened")
	}
	if got := h.Pane().File.Path; !filepath.IsAbs(got) {
		t.Errorf("opened %q; a relative path resolves against the cwd", got)
	}
	if got := h.Pane().File.Text(); !strings.Contains(got, "func needle()") {
		t.Errorf("opened an empty or wrong buffer: %q", got)
	}
	if h.Pane().File.Dirty() {
		t.Error("a freshly picked file is dirty, so it was created rather than read")
	}
}

// The picker had no paste coverage at all, which is how the field could hold a
// pasted path while the list showed nothing without a test noticing.
func TestPasteIntoPicker(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	h.press("super+p")
	if h.Focused() != FocusPicker {
		t.Fatal("setup: expected picker focus")
	}
	h.Handle(ui.Paste{Text: "main.go"})
	h.drain()
	if got := h.Picker.Query(); got != "main.go" {
		t.Errorf("query = %q, want main.go", got)
	}
	if h.Picker.Results() == 0 {
		t.Error("the pasted name matched nothing")
	}
	if !h.Picker.Open || h.Focused() != FocusPicker {
		t.Error("a paste closed the picker")
	}
}

// A path pasted from a shell or a compiler carries a prefix and a position the
// index does not. The picker narrows it rather than searching for it literally.
func TestPasteIntoPickerNarrowsAPath(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	h.press("super+p")
	h.Handle(ui.Paste{Text: filepath.Join(h.root, "main.go") + ":12:4\n"})
	h.drain()
	if h.Picker.Results() == 0 {
		t.Fatalf("pasted path matched nothing; query = %q", h.Picker.Query())
	}
	if got := h.Picker.Top(); got != "main.go" {
		t.Errorf("top result = %q, want main.go", got)
	}
}

// Pasting a compiler line into the picker and pressing enter opens the file
// where the compiler was pointing. The position is parsed out to make the path
// match; throwing it away afterwards would land at the top of the file.
func TestPasteIntoPickerOpensAtThePosition(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	h.press("super+p")
	h.Handle(ui.Paste{Text: filepath.Join(h.root, "pkg/helper.go") + ":3:6\n"})
	h.drain()
	h.press("enter")

	if h.Pane() == nil {
		t.Fatal("nothing opened")
	}
	if got := filepath.Base(h.Pane().File.Path); got != "helper.go" {
		t.Fatalf("opened %q, want helper.go", got)
	}
	line, col := h.Pane().File.LineCol(h.Pane().Cursors.Primary().Head)
	if line+1 != 3 || col+1 != 6 {
		t.Errorf("cursor at %d:%d, want 3:6", line+1, col+1)
	}
}

// A path with no position opens at the top, unchanged.
func TestPickerWithoutAPositionOpensAtTheTop(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	h.press("super+p")
	h.Handle(ui.Paste{Text: "pkg/helper.go"})
	h.drain()
	h.press("enter")

	if line, _ := h.Pane().File.LineCol(h.Pane().Cursors.Primary().Head); line != 0 {
		t.Errorf("cursor on line %d, want the top", line+1)
	}
}

// The explorer has no text field, and its only keys.None handler treats a space
// as the changed-only toggle. A pasted space must not flip that filter.
func TestPasteIntoExplorerIsIgnored(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	h.press("shift+super+e")
	before := h.Explorer.Tree.ChangedOnly
	h.Handle(ui.Paste{Text: " "})
	h.drain()
	if h.Explorer.Tree.ChangedOnly != before {
		t.Error("a paste toggled the changed-only filter")
	}
}

// Wrapping is on unless a caller turns it off. A line running off the right
// edge is worse than one continuing below, so horizontal scrolling is the
// fallback rather than the default — and there is no flag to remember.
func TestWrapIsOnByDefault(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	if !h.WrapDefault {
		t.Fatal("WrapDefault is off")
	}
	dir := h.Explorer.Tree.Root
	h.OpenFile(filepath.Join(dir, "main.go"))
	p := h.Tabs.Active()
	if p == nil {
		t.Fatal("no pane opened")
	}
	if !p.Wrap {
		t.Error("an opened pane did not inherit the wrap default")
	}
}

// A wrapped pane never scrolls horizontally: the line continues below instead,
// so there is nothing off to the right to scroll to.
func TestWrappedPaneDoesNotScrollHorizontally(t *testing.T) {
	h := newWorkspace(t, 60, 12)
	dir := h.Explorer.Tree.Root
	path := filepath.Join(dir, "wide.md")
	if err := os.WriteFile(path, []byte(strings.Repeat("word ", 200)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.OpenFile(path)
	p := h.Tabs.Active()
	p.Cursors.Set(p.File.Len()-1, p.File.Len()-1)
	p.FollowCursor()
	h.drain()
	if p.Viewport.Left != 0 {
		t.Errorf("Left = %d on a wrapped pane", p.Viewport.Left)
	}
	if got := p.RowsInLine(0); got < 2 {
		t.Errorf("line occupies %d rows at %d cols; it should wrap", got, p.Viewport.Cols)
	}
}

// The changed-only toggle sits under the heading and is reachable with cmd+up,
// with cmd+down going back to the tree — the same jump the search pane uses.
func TestExplorerFilterIsReachableWithCmdUpDown(t *testing.T) {
	h := newWorkspace(t, 120, 20)
	h.openSidebar("shift+super+e", SidebarExplorer)

	h.press("super+up", "enter") // jump to the toggle and flip it
	if !h.Explorer.Tree.ChangedOnly {
		t.Fatal("cmd+up then enter did not reach the changed-only toggle")
	}
	h.press("super+down", "enter") // back to the tree: enter expands pkg/
	if !h.Explorer.Tree.ChangedOnly {
		t.Error("cmd+down left focus on the toggle; enter flipped it back")
	}
}

// Deep paths are truncated in the tree, so the selected one is spelled out on
// the pane's last row.
func TestExplorerShowsSelectedPath(t *testing.T) {
	h := newWorkspace(t, 120, 20)
	h.openSidebar("shift+super+e", SidebarExplorer)
	h.press("enter")        // expand pkg/
	h.press("down", "down") // into it
	sel := h.Explorer.Tree.Rel(h.Explorer.Selected())
	if sel == "" {
		t.Fatal("nothing selected")
	}
	if !strings.Contains(h.host.Text(), sel) {
		t.Errorf("selected path %q is not on screen:\n%s", sel, h.host.Text())
	}
}

// cmd+g steps to the next match with the find bar closed: find something, keep
// editing, then keep stepping without reopening anything.
func TestFindNextWithTheBarClosed(t *testing.T) {
	h := newHarness(t, "needle\nfiller\nneedle\nfiller\nneedle\n")
	h.press("super+f")
	h.typeText("needle")
	h.press("esc") // close the bar, keep the query

	// Incremental search left the cursor on the last match, so stepping forward
	// wraps. Where it lands matters less than the two invariants: it moves, and
	// stepping back undoes it exactly.
	start := h.Pane().Cursors.Primary().Head
	h.press("super+g")
	next := h.Pane().Cursors.Primary().Head
	if next == start {
		t.Errorf("cmd+g did not move from %d", start)
	}
	h.press("shift+super+g")
	if back := h.Pane().Cursors.Primary().Head; back != start {
		t.Errorf("cmd+shift+g landed at %d, want back at %d", back, start)
	}
}
