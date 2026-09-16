package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"raj/internal/control"
	"raj/internal/keys"
	"raj/internal/piecetable"
	"raj/internal/prompt"
	"raj/internal/ui"
	"raj/internal/widget"
)

// harness drives the real application against a headless host. Every test below
// exercises the same path a keystroke takes in the terminal: chord synthesis,
// decode, keymap resolution, pane mutation, render.
type harness struct {
	*App
	host *ui.FakeHost
}

func newHarness(t *testing.T, content string) *harness {
	t.Helper()
	return newHarnessSize(t, content, 120, 12)
}

// newHarnessSize builds an app over a temp directory containing one file.
func newHarnessSize(t *testing.T, content string, cols, rows int) *harness {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.go")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	host := ui.NewFakeHost(cols, rows)
	t.Cleanup(func() { host.Close() })
	a := New(host, dir, 2)
	a.Search.Debounce = time.Nanosecond // tests wait on Settle, not on the clock
	a.OpenFile(path)
	return &harness{App: a, host: host}
}

// drain feeds every queued event through the app, then draws.
func (h *harness) drain() {
	for {
		select {
		case e := <-h.host.Events():
			h.Handle(e)
		default:
			// Search runs off the event thread now, so a test that types a
			// query and asserts on the results has to wait for it.
			h.Search.Settle(2 * time.Second)
			h.Draw()
			return
		}
	}
}

func (h *harness) press(chords ...string) {
	for _, c := range chords {
		h.host.Press(c)
	}
	h.drain()
}

func (h *harness) typeText(s string) {
	h.host.Type(s)
	h.drain()
}

func (h *harness) text() string { return h.Pane().File.Text() }

func TestTypingInsertsText(t *testing.T) {
	h := newHarness(t, "")
	h.typeText("hello")
	if got := h.text(); got != "hello" {
		t.Errorf("buffer = %q, want hello", got)
	}
	if !strings.Contains(h.host.Text(), "hello") {
		t.Errorf("not rendered:\n%s", h.host.Text())
	}
}

func TestCursorMovementAndEditing(t *testing.T) {
	h := newHarness(t, "abc")
	h.press("super+right") // line end
	h.typeText("d")
	if got := h.text(); got != "abcd" {
		t.Fatalf("after append = %q", got)
	}
	h.press("backspace", "backspace")
	if got := h.text(); got != "ab" {
		t.Errorf("after backspace = %q, want ab", got)
	}
}

// The whole chain has to hold for a chord: Ghostty's byte sequence, the
// decoder, the keymap, and the pane.
func TestSelectAllAndReplace(t *testing.T) {
	h := newHarness(t, "throw this away")
	h.press("super+a")
	h.typeText("new")
	if got := h.text(); got != "new" {
		t.Errorf("buffer = %q, want new", got)
	}
}

func TestUndoRedoThroughKeybindings(t *testing.T) {
	h := newHarness(t, "base")
	h.typeText("XY")
	if got := h.text(); got != "XYbase" {
		t.Fatalf("setup = %q", got)
	}
	h.press("super+z")
	h.press("super+z")
	if got := h.text(); got != "base" {
		t.Errorf("after undo = %q, want base", got)
	}
	h.press("shift+super+z")
	if got := h.text(); got != "Xbase" {
		t.Errorf("after redo = %q, want Xbase", got)
	}
}

func TestSaveWritesFile(t *testing.T) {
	h := newHarness(t, "content")
	if !h.Pane().File.Dirty() {
		// a freshly opened file is clean
		h.typeText("x")
	}
	h.press("super+s")
	data, err := os.ReadFile(h.Pane().File.Path)
	if err != nil {
		t.Fatalf("reading saved file: %v", err)
	}
	if string(data) != h.text() {
		t.Errorf("on disk %q, in buffer %q", data, h.text())
	}
	if h.Pane().File.Dirty() {
		t.Error("file still dirty after save")
	}
	if !strings.Contains(h.Status(), "saved") {
		t.Errorf("status = %q", h.Status())
	}
}

// Vertical movement must remember the column it wanted, or arrowing down
// through a short line and back up lands somewhere else.
func TestGoalColumnSurvivesShortLines(t *testing.T) {
	h := newHarness(t, "aaaaaaaaaa\nbb\ncccccccccc")
	h.press("super+right") // end of line 1, column 10
	h.press("down", "down")
	line, col := h.Pane().File.LineCol(h.Pane().Cursors.Primary().Head)
	if line != 2 || col != 10 {
		t.Errorf("landed at %d:%d, want 2:10", line, col)
	}
}

func TestMultiCursorTyping(t *testing.T) {
	h := newHarness(t, "one\ntwo\nthree")
	h.press("alt+super+down") // add a cursor on line 2
	if n := h.Pane().Cursors.Count(); n != 2 {
		t.Fatalf("cursors = %d, want 2", n)
	}
	h.typeText("X")
	if got := h.text(); got != "Xone\nXtwo\nthree" {
		t.Errorf("buffer = %q", got)
	}
}

func TestEscapeCollapsesCursors(t *testing.T) {
	h := newHarness(t, "a\nb\nc")
	h.press("alt+super+down", "alt+super+down")
	if h.Pane().Cursors.Count() < 2 {
		t.Fatal("expected multiple cursors")
	}
	h.press("esc")
	if n := h.Pane().Cursors.Count(); n != 1 {
		t.Errorf("cursors = %d after escape, want 1", n)
	}
}

func TestIndentAndOutdent(t *testing.T) {
	h := newHarness(t, "line")
	h.press("tab")
	// The harness file is test.go, and Go indents with tabs.
	if got := h.text(); got != "\tline" {
		t.Fatalf("after indent = %q, want a tab", got)
	}
	h.press("shift+tab")
	if got := h.text(); got != "line" {
		t.Errorf("after outdent = %q", got)
	}
}

func TestPasteIsOneEdit(t *testing.T) {
	h := newHarness(t, "")
	before := h.Pane().File.Pieces()
	h.Handle(ui.Paste{Text: strings.Repeat("pasted line\n", 100)})
	h.Draw()
	if got := h.Pane().File.Pieces(); got-before > 3 {
		t.Errorf("paste created %d pieces; it should be a single edit", got-before)
	}
	if !strings.HasPrefix(h.text(), "pasted line") {
		t.Errorf("buffer = %q", h.text()[:20])
	}
}

// The status line reports the file, dirty state, and cursor position.
func TestStatusLine(t *testing.T) {
	h := newHarness(t, "abc")
	h.typeText("x")
	frame := h.host.Text()
	last := frame[strings.LastIndex(frame, "\n")+1:]
	if !strings.Contains(last, "test.go") || !strings.Contains(last, "•") {
		t.Errorf("status line = %q, want filename and dirty marker", last)
	}
	if !strings.Contains(last, "1:2") {
		t.Errorf("status line = %q, want cursor position 1:2", last)
	}
}

// Rendering must not depend on document size: only visible lines are drawn.
func TestRendersOnlyVisibleLines(t *testing.T) {
	h := newHarnessSize(t, strings.Repeat("a line of text\n", 10000), 120, 12)
	h.drain()
	frame := h.host.Text()
	if got := strings.Count(frame, "\n") + 1; got > 12 {
		t.Errorf("rendered %d rows for a 12-row screen", got)
	}
	if !strings.Contains(frame, "a line of text") {
		t.Errorf("nothing rendered:\n%s", frame)
	}
}

// Scrolling follows the cursor to the end of a long document.
func TestScrollFollowsCursor(t *testing.T) {
	h := newHarness(t, strings.Repeat("x\n", 500))
	h.press("super+down") // document end
	if top := h.Pane().Viewport.Top; top < 400 {
		t.Errorf("viewport top = %d, want near the end", top)
	}
}

// widgetTheme exposes the shared theme to tests.
func widgetTheme() widget.Theme { return widget.DefaultTheme() }

// Escape must always be able to leave a multi-cursor state: a stuck set with no
// way out is the fastest way to make an editor feel broken. Asserted through
// the real key path, since the failure was never in Cursors.Clear.
func TestEscapeCollapsesMultiCursor(t *testing.T) {
	h := newHarness(t, "aaa\nbbb\nccc\n")
	h.press("alt+super+down", "alt+super+down")
	if got := h.Pane().Cursors.Count(); got != 3 {
		t.Fatalf("cursor count = %d, want 3 before escape", got)
	}
	h.press("esc")
	if got := h.Pane().Cursors.Count(); got != 1 {
		t.Errorf("cursor count = %d, want 1 after escape", got)
	}
	if h.Pane().Cursors.Primary().HasSelection() {
		t.Error("escape left a selection behind")
	}
}

// handleKeyAction drives an action straight into the key path, bypassing chord
// resolution. Tests that walk the whole action set need this: going through a
// chord would test the keymap as well, and a chord that resolves differently on
// one platform would make the test platform-dependent for no reason.
func (h *harness) handleKeyAction(a keys.Action) {
	// The same order handleKey uses: globals get first refusal, then the
	// focused pane. Routing straight to handleEditor would report every global
	// action as unhandled, which is how the first version of this reported
	// cmd+s as unimplemented.
	if !h.App.handleGlobal(a) {
		switch h.App.focus {
		case FocusPicker:
			h.App.openFromPicker(h.App.Picker.Handle(a, ""))
		case FocusSidebar:
			h.App.handleSidebar(a, "")
		default:
			h.App.handleEditor(a, "")
		}
	}
	h.drain()
}

// A headless buffer is cached, so the next lookup has to notice when the file
// moved on disk rather than serving the snapshot it was first loaded from.
func TestHeadlessReloadsWhenDiskChanges(t *testing.T) {
	h := controlHarness(t, "root\n")
	c := h.dial(t)
	dir := filepath.Dir(h.Tabs.Active().File.Path)
	path := filepath.Join(dir, "changing.go")
	if err := os.WriteFile(path, []byte("first\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	first := c.do(h, control.Request{Op: "text", Path: path})
	if !first.OK || first.Text() != "first\n" {
		t.Fatalf("first read = %+v", first)
	}
	p, ok := h.findHeadless(path)
	if !ok {
		t.Fatal("read did not load the path headlessly")
	}
	sess := p.File.Session()

	// A longer rewrite changes the size, so the stamp catches it even where
	// the filesystem's mtime resolution is coarse.
	if err := os.WriteFile(path, []byte("second, longer\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	again := c.do(h, control.Request{Op: "text", Path: path})
	if !again.OK || again.Text() != "second, longer\n" {
		t.Fatalf("second read = %+v, want the new text", again)
	}
	p2, ok := h.findHeadless(path)
	if !ok {
		t.Fatal("path left the headless registry")
	}
	if p2 != p {
		t.Errorf("lookup returned a different pane")
	}
	if p2.File.Session() == sess {
		t.Errorf("the document was not reloaded")
	}
}

// An unchanged file is not reloaded: the lookup reuses the cached pane and
// document after one stat.
func TestHeadlessUnchangedFileIsReused(t *testing.T) {
	h := controlHarness(t, "root\n")
	c := h.dial(t)
	dir := filepath.Dir(h.Tabs.Active().File.Path)
	path := filepath.Join(dir, "stable.go")
	if err := os.WriteFile(path, []byte("stable\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if r := c.do(h, control.Request{Op: "text", Path: path}); !r.OK {
		t.Fatalf("read = %+v", r)
	}
	p, ok := h.findHeadless(path)
	if !ok {
		t.Fatal("read did not load the path headlessly")
	}
	sess := p.File.Session()

	if r := c.do(h, control.Request{Op: "text", Path: path}); !r.OK || r.Text() != "stable\n" {
		t.Fatalf("second read = %+v", r)
	}
	p2, ok := h.findHeadless(path)
	if !ok || p2 != p {
		t.Fatal("lookup did not reuse the cached pane")
	}
	if p2.File.Session() != sess {
		t.Errorf("an unchanged file was reloaded")
	}
}

// A failed stat or read keeps the cached copy rather than dropping the buffer.
func TestHeadlessStatErrorKeepsCache(t *testing.T) {
	h := controlHarness(t, "root\n")
	c := h.dial(t)
	dir := filepath.Dir(h.Tabs.Active().File.Path)
	path := filepath.Join(dir, "gone.go")
	if err := os.WriteFile(path, []byte("keep me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if r := c.do(h, control.Request{Op: "text", Path: path}); !r.OK {
		t.Fatalf("read = %+v", r)
	}
	p, ok := h.findHeadless(path)
	if !ok {
		t.Fatal("read did not load the path headlessly")
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	// The failed read must not evict the buffer: the text already read is the
	// only copy of a file that is now gone.
	got := c.do(h, control.Request{Op: "text", Path: path})
	if !got.OK || got.Text() != "keep me\n" {
		t.Fatalf("read after removal = %+v, want the cached text", got)
	}
	p2, ok := h.findHeadless(path)
	if !ok || p2 != p {
		t.Fatal("the unreadable buffer was dropped from the registry")
	}
}

// A file created outside raj appears in the explorer once the idle scan runs,
// with no raj action to trigger a refresh.
func TestSyncFileTreePicksUpANewFile(t *testing.T) {
	h := newHarness(t, "package main\n")
	added := filepath.Join(h.root, "added.go")
	if err := os.WriteFile(added, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.syncFileTree(time.Now().Add(2 * time.Second))
	for _, e := range h.Explorer.Tree.Entries() {
		if e.Path == added {
			return
		}
	}
	t.Errorf("a file created outside raj is not in the tree")
}

// Save-as lists what is already in the directory being typed into, so a name is
// visible before enough of it has been typed to complete.
func TestSaveAsListsDirectoryEntries(t *testing.T) {
	h := newWorkspace(t, 120, 30)
	saveAsPrompt(t, h)

	screen := h.host.Text()
	for _, name := range []string{"README.md", "main.go", "pkg/"} {
		if !strings.Contains(screen, name) {
			t.Errorf("save-as does not list %q:\n%s", name, screen)
		}
	}
}

// An arrow steps into the listing and enter takes the highlighted entry, so a
// listed file resolves to its full path rather than the typed prefix.
func TestSaveAsChoosingAListedFileUsesItsPath(t *testing.T) {
	h := newWorkspace(t, 120, 30)
	saveAsPrompt(t, h)
	h.typeText("README")
	h.press("down", "enter")

	if !h.Prompt.Open || h.Prompt.Title() != "File exists" {
		t.Fatalf("choosing a listed file did not resolve to it; status = %q", h.Status())
	}
	answer(h, prompt.Cancel)
	if h.Status() != "save cancelled" {
		t.Errorf("status = %q after cancelling", h.Status())
	}
}

// A fold above the caret hides session lines, so the completion popup must
// anchor on the display row rather than the session line, or the list sits a
// row away from the word it completes.
func TestCompletionAnchorsBelowAFold(t *testing.T) {
	// The candidate "foobar" is what "foo" completes to: the word being typed
	// is not offered as its own completion, so a buffer of "foo" alone would
	// show an empty list and the anchor could not be observed.
	h := newHarness(t, "foobar alpha\nbravo\ncharlie\ndelta foo\n")
	p := h.Pane()
	// A rejected two-line insertion stays in the session but the edit
	// composition hides it, so it becomes one fold row over two session lines.
	id := propose(t, h, piecetable.Hunk{Start: 0, End: 0, Text: "HIDDEN-1\nHIDDEN-2\n"})
	if !p.File.RejectGroup(id) {
		t.Fatal("reject failed")
	}
	head := len(p.File.Text()) - 1 // end of the last line, after "foo"
	p.Cursors.Set(head, head)
	h.drain()

	if p.DisplayLines() >= p.File.Lines() {
		t.Fatalf("no fold: DisplayLines = %d, session lines = %d", p.DisplayLines(), p.File.Lines())
	}
	wantRow, _ := p.DispPos(head)
	if wantRow == p.File.LineOf(head) {
		t.Fatalf("fixture does not exercise the shift: row %d == session line %d", wantRow, p.File.LineOf(head))
	}
	if !h.showCompletion(p, 0) {
		t.Fatal("no completion was offered at the word")
	}
	if line, _ := h.Complete.Anchor(); line != wantRow {
		t.Errorf("completion anchored on row %d, want display row %d (session line %d)",
			line, wantRow, p.File.LineOf(head))
	}
}

// A fold above the target shifts a jump too: the viewport must centre on the
// display row, not the session line the target came in as.
func TestJumpCentresOnTheDisplayRowBelowAFold(t *testing.T) {
	h := newHarness(t, strings.Repeat("xxxxxxxx\n", 20))
	p := h.Pane()
	id := propose(t, h, piecetable.Hunk{Start: 0, End: 0, Text: "HIDDEN-1\nHIDDEN-2\n"})
	if !p.File.RejectGroup(id) {
		t.Fatal("reject failed")
	}
	p.Cursors.Set(0, 0)
	h.drain()

	// Session line 15 is visible at display row 14: two hidden lines become one
	// fold row above it.
	if got := p.DispOfDocLine(15); got != 14 {
		t.Fatalf("DispOfDocLine(15) = %d, want 14 below the fold", got)
	}
	h.jumpTo(16) // 1-based line 16 is session line 15
	if got := p.File.LineOf(p.Cursors.Primary().Head); got != 15 {
		t.Fatalf("caret on session line %d after jumpTo(16), want 15", got)
	}
	wantTop := 14 - p.Viewport.Rows/2
	if wantTop < 0 {
		wantTop = 0
	}
	if max := p.DisplayLines() - 1; wantTop > max {
		wantTop = max
	}
	if p.Viewport.Top != wantTop {
		t.Errorf("viewport top = %d after the jump, want %d (centred on display row 14)",
			p.Viewport.Top, wantTop)
	}
}

// A line a fold hides has no row to land on: the jump is skipped rather than
// clamped onto the fold marker.
func TestJumpSkipsALineInsideAFold(t *testing.T) {
	h := newHarness(t, "alpha\nbravo\ncharlie\n")
	p := h.Pane()
	id := propose(t, h, piecetable.Hunk{Start: 0, End: 0, Text: "HIDDEN-1\nHIDDEN-2\n"})
	if !p.File.RejectGroup(id) {
		t.Fatal("reject failed")
	}
	p.Cursors.Set(p.File.Len(), p.File.Len())
	h.drain()

	if row := p.DispOfDocLine(0); row != -1 {
		t.Fatalf("session line 0 shows at row %d, want -1 inside the fold", row)
	}
	before := p.Cursors.Primary().Head
	top := p.Viewport.Top
	h.jumpTo(1) // 1-based line 1, the hidden "HIDDEN-1"
	if got := p.Cursors.Primary().Head; got != before {
		t.Errorf("caret moved to %d for a hidden line, want it left at %d", got, before)
	}
	if p.Viewport.Top != top {
		t.Errorf("viewport scrolled to %d for a hidden line, want %d", p.Viewport.Top, top)
	}
}

// With no decisions the projection is the identity, so a jump and a completion
// anchor land exactly where they did before the display map existed.
func TestJumpAndCompletionAreIdentityWithoutDecisions(t *testing.T) {
	// "foobar" gives the typed "foo" a completion; the word itself is not
	// offered as its own candidate.
	h := newHarness(t, "foobar alpha\nbravo\ncharlie\ndelta foo\n")
	p := h.Pane()
	if p.DisplayLines() != p.File.Lines() {
		t.Fatalf("no-decision projection is not the identity: %d rows vs %d lines", p.DisplayLines(), p.File.Lines())
	}
	for ln := 0; ln < p.File.Lines(); ln++ {
		if got := p.DispOfDocLine(ln); got != ln {
			t.Fatalf("DispOfDocLine(%d) = %d with no decisions, want the identity", ln, got)
		}
	}
	h.jumpTo(3) // 1-based line 3 is session line 2, "charlie"
	if got := p.File.LineOf(p.Cursors.Primary().Head); got != 2 {
		t.Errorf("caret on session line %d after jumpTo(3), want 2", got)
	}
	head := len(p.File.Text()) - 1
	p.Cursors.Set(head, head)
	h.drain()
	if !h.showCompletion(p, 0) {
		t.Fatal("no completion was offered")
	}
	if line, _ := h.Complete.Anchor(); line != p.File.LineOf(head) {
		t.Errorf("completion anchored on row %d, want session line %d with no decisions",
			line, p.File.LineOf(head))
	}
}
