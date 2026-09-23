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
	"raj/internal/tabs"
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

// TestMain points XDG_STATE_HOME at a temp dir for the whole package, so the
// tests that open a workspace store write their state there instead of into
// the user's real state home. The editor's state lives outside .raj now, so a
// test root no longer contains it, and this is what keeps the suite hermetic.
func TestMain(m *testing.M) {
	state, err := os.MkdirTemp("", "raj-test-state-")
	if err != nil {
		panic(err)
	}
	if err := os.Setenv("XDG_STATE_HOME", state); err != nil {
		panic(err)
	}
	code := m.Run()
	_ = os.RemoveAll(state)
	os.Exit(code)
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
	// Release the workspace store on the way out, so a test binary with many
	// apps does not leak one SQLite handle each.
	t.Cleanup(a.CloseState)
	a.Search.Debounce = time.Nanosecond // tests wait on Settle, not on the clock
	a.OpenFile(path)
	return &harness{App: a, host: host}
}

// TestPrimaryRootIsTheConstructorRoot pins that routing the single root through
// workspace.Roots preserves its value: the constructor stores exactly the root
// it was given and primaryRoot hands it back, so every reader that moved to
// primaryRoot sees what the old root field held.
func TestPrimaryRootIsTheConstructorRoot(t *testing.T) {
	root := t.TempDir()
	host := ui.NewFakeHost(80, 24)
	defer host.Close()
	a := New(host, root, 2)
	defer a.CloseState()
	if got := a.primaryRoot(); got != root {
		t.Errorf("primaryRoot() = %q, want the constructor root %q", got, root)
	}
}

// newOptionsHarness is newHarness launched with explicit Options, so a test
// exercises the real profile resolution rather than poking the App fields.
func newOptionsHarness(t *testing.T, content string, o Options) *harness {
	t.Helper()
	return newOptionsHarnessSize(t, content, 120, 12, o)
}

// newOptionsHarnessSize is newOptionsHarness at an explicit terminal size, for
// the phone drawer whose expanded panel needs more than a dozen rows.
func newOptionsHarnessSize(t *testing.T, content string, cols, rows int, o Options) *harness {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.go")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	host := ui.NewFakeHost(cols, rows)
	t.Cleanup(func() { host.Close() })
	o.TabWidth = 2
	a := NewWithOptions(host, dir, o)
	t.Cleanup(a.CloseState)
	a.Search.Debounce = time.Nanosecond
	a.OpenFile(path)
	return &harness{App: a, host: host}
}

// newPhoneHarness is the bare --phone profile: the layout plus the ctrl
// aliases the implication turns on.
func newPhoneHarness(t *testing.T, content string) *harness {
	t.Helper()
	return newOptionsHarness(t, content, Options{Phone: true})
}

// newPhoneHarnessSize is a phone harness at an explicit size, so the expanded
// drawer panel has room above its collapsed handle.
func newPhoneHarnessSize(t *testing.T, content string, cols, rows int) *harness {
	t.Helper()
	return newOptionsHarnessSize(t, content, cols, rows, Options{Phone: true})
}

// The phone flag is a profile switch, not a stored setting: it reaches the app
// and the tab strip, and it reserves a two-row strip. Options has no *Set
// partner for it, so it never enters main's flag.Visit override logic and a
// stored setting cannot turn it on.
func TestPhoneProfilePlumbing(t *testing.T) {
	h := newHarness(t, "x\n")
	if h.App.phone || h.App.Tabs.Phone() || h.App.ctrlAliases {
		t.Fatal("the default profile must not be phone and must not alias")
	}
	plain := layoutFor(120, 24, h.sidebar, h.focus, false, false)
	if plain.TabRows != 1 || plain.BarY != -1 {
		t.Errorf("ordinary layout = TabRows %d BarY %d, want 1/-1", plain.TabRows, plain.BarY)
	}
	if plain != computeLayout(120, 24, h.sidebar, h.focus) {
		t.Error("layoutFor(false) differs from computeLayout; the ordinary profile must be unchanged")
	}

	p := newPhoneHarness(t, "x\n")
	if !p.phone || !p.Tabs.Phone() {
		t.Fatal("Options.Phone did not reach the app and its tab strip")
	}
	if !p.ctrlAliases {
		t.Fatal("the default phone profile did not turn on the ctrl aliases")
	}
	if got := p.Tabs.StripRows(); got != tabs.PhoneStripRows {
		t.Errorf("phone StripRows = %d, want %d", got, tabs.PhoneStripRows)
	}
	// The phone profile always keeps one bottom row for the action drawer,
	// whether or not there is a review target.
	l := p.layout(120, 24)
	if l.TabRows != tabs.PhoneStripRows || l.BarY != 24-phoneDrawerRows {
		t.Errorf("phone layout = TabRows %d BarY %d, want %d/%d", l.TabRows, l.BarY, tabs.PhoneStripRows, 24-phoneDrawerRows)
	}
	if want := 24 - tabs.PhoneStripRows - phoneDrawerRows; l.Rows != want {
		t.Errorf("phone editor Rows = %d, want %d (24 - %d tab rows - %d handle rows)", l.Rows, want, tabs.PhoneStripRows, phoneDrawerRows)
	}
	// The drawer key fallback is bound only under --phone, so the ordinary
	// keymap keeps esc as Cancel.
	if got := p.keymap.Lookup(keys.Editor, "esc"); got != keys.ToggleDrawer {
		t.Errorf("phone esc = %q, want ToggleDrawer", got)
	}
	if got := h.keymap.Lookup(keys.Editor, "esc"); got != keys.Cancel {
		t.Errorf("ordinary esc = %q, want Cancel", got)
	}
}

// TestPhoneAliasProfileMatrix pins the implication rule: --phone implies the
// ctrl aliases unless --ctrl-aliases was passed explicitly. It drives the same
// ProfileFlags main uses and then builds the app, so a rule that only lived in
// the pure function would still fail here.
func TestPhoneAliasProfileMatrix(t *testing.T) {
	cases := []struct {
		name               string
		phone, ctrl, cSet  bool
		wantPhone, wantAlt bool
	}{
		{"neither", false, false, false, false, false},
		{"phone implies aliases", true, false, false, true, true},
		{"phone keeps aliases off", true, false, true, true, false},
		{"aliases without the layout", false, true, true, false, true},
		{"explicit aliases on", true, true, true, true, true},
		{"explicit aliases off alone", false, false, true, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			phoneOn, aliasesOn := ProfileFlags(tc.phone, tc.ctrl, tc.cSet)
			if phoneOn != tc.wantPhone || aliasesOn != tc.wantAlt {
				t.Fatalf("ProfileFlags = phone %v aliases %v, want %v/%v",
					phoneOn, aliasesOn, tc.wantPhone, tc.wantAlt)
			}
			h := newOptionsHarness(t, "x\n", Options{
				Phone:          tc.phone,
				CtrlAliases:    tc.ctrl,
				CtrlAliasesSet: tc.cSet,
			})
			if h.phone != tc.wantPhone || h.ctrlAliases != tc.wantAlt {
				t.Errorf("app phone=%v ctrlAliases=%v, want %v/%v",
					h.phone, h.ctrlAliases, tc.wantPhone, tc.wantAlt)
			}
			if got := h.Tabs.StripRows() == tabs.PhoneStripRows; got != tc.wantPhone {
				t.Errorf("phone layout = %v, want %v", got, tc.wantPhone)
			}
			// ctrl+s is a free alias of super+s (Save): it exists exactly when
			// the aliases are on.
			if got := h.keymap.Lookup(keys.Editor, "ctrl+s") == keys.Save; got != tc.wantAlt {
				t.Errorf("ctrl+s resolves to Save = %v, want %v", got, tc.wantAlt)
			}
			// ctrl+r is the alias the phone uses to enter Review; without it the
			// review controls are unreachable from a super-less terminal.
			if got := h.keymap.Lookup(keys.Editor, "ctrl+r") == keys.ToggleReview; got != tc.wantAlt {
				t.Errorf("ctrl+r resolves to ToggleReview = %v, want %v", got, tc.wantAlt)
			}
			// The startup report is empty off and names the known collisions on:
			// without the report plumbing a phone would silently lose them.
			report := h.CtrlAliasReport()
			if tc.wantAlt && report == "" {
				t.Error("aliases on but the collision report is empty; the ctrl+c/z/g overlaps should be reported")
			}
			if !tc.wantAlt && report != "" {
				t.Errorf("aliases off but the report is %q", report)
			}
		})
	}
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

// A mixed-ending file is normalised to the dominant ending when it is saved, so
// the user is told at open, beside the indent warning
// TestOpenSurfacesIndentAndEncodingWarnings pins. The normal-ending case is the
// guard that the check stays silent when it has nothing to say. Modelled on
// TestMixedEndingsWarn and TestSaveWritesFile.
func TestOpenSurfacesMixedEndingWarning(t *testing.T) {
	h := newHarness(t, "a\r\nb\nc\r\n")
	if got := h.Status(); !strings.Contains(got, "mixed line endings") {
		t.Errorf("mixed-ending status = %q, want the encoding warning", got)
	}
	quiet := newHarness(t, "a\nb\nc\n")
	if got := quiet.Status(); got != "" {
		t.Errorf("normal-ending status = %q, want quiet", got)
	}
}

// A property of the file just opened must survive the open: a broken Makefile
// carries both an indent warning and, when its endings are mixed, an encoding
// warning, and both must appear in the one status sentence. The editor-layer
// TestIndentWarning pins only the string; this pins the app wiring that was
// dead before the wave. Modelled on TestOpenSurfacesMixedEndingWarning.
func TestOpenSurfacesIndentAndEncodingWarnings(t *testing.T) {
	h := newHarness(t, "content")
	path := filepath.Join(h.primaryRoot(), "Makefile")
	if err := os.WriteFile(path, []byte(".PHONY: b\r\nbuild:\r\n  go build\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.OpenFile(path)
	got := h.Status()
	if !strings.Contains(got, "tabs") {
		t.Errorf("status = %q, want the indent warning", got)
	}
	if !strings.Contains(got, "mixed line endings") {
		t.Errorf("status = %q, want the encoding warning", got)
	}
}

// A buffer an agent inspected but never opened is revealed as a tab by the
// first request that touches it, and owes the same warning a freshly opened one
// does. announce gave it presentation state but not fileWarning, so a
// mixed-ending file an agent revealed stayed silent. Modelled on
// TestOpenSurfacesMixedEndingWarning, the open path this one must match.
func TestAnnounceSurfacesFileWarning(t *testing.T) {
	h := newHarness(t, "base\n")
	path := filepath.Join(h.primaryRoot(), "mixed.txt")
	if err := os.WriteFile(path, []byte("a\r\nb\nc\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := h.loadHeadless(path)
	if err != nil {
		t.Fatalf("loadHeadless: %v", err)
	}
	if p.File.EncodingWarning() == "" {
		t.Fatal("fixture: the file has no encoding warning to surface")
	}
	h.announce(p)
	if got := h.Status(); !strings.Contains(got, "mixed line endings") {
		t.Errorf("status = %q, want the encoding warning on reveal", got)
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
	// The same routing handleKey uses, so a test that walks the action set
	// exercises the dispatch rather than a copy of it that can drift.
	h.App.dispatch(a, "")
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
	added := filepath.Join(h.primaryRoot(), "added.go")
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

// The completion prefix comes from the row on screen, not the session line: a
// rejected mid-line insertion is hidden behind a fold, so it must not appear in
// the word the popup completes. Reading the session bytes would fold the hidden
// "REJ" into the prefix and find nothing for the visible "bar".
func TestCompletionPrefixIsTheRowTextBelowAMidLineFold(t *testing.T) {
	// "barbaz" is what the on-screen "bar" completes to; the rejected "REJ"
	// sits between "foo" and "bar" in the session and is folded away.
	h := newHarness(t, "foobar\nbarbaz\n")
	p := h.Pane()
	id := propose(t, h, piecetable.Hunk{Start: 3, End: 3, Text: "REJ"})
	if !p.File.RejectGroup(id) {
		t.Fatal("reject failed")
	}
	head := len("fooREJbar") // end of "bar", before its newline
	p.Cursors.Set(head, head)
	h.drain()

	folded := false
	for i := 0; i < p.DisplayLines(); i++ {
		if _, _, ok := p.Fold(i); ok {
			folded = true
			break
		}
	}
	if !folded {
		t.Fatal("fixture built no fold for the rejected insertion")
	}
	row, _ := p.DispPos(head)
	if row == p.File.LineOf(head) {
		t.Fatalf("fixture does not exercise the split: row %d == session line %d", row, p.File.LineOf(head))
	}
	if got := p.RowText(row); got != "bar" {
		t.Fatalf("RowText(%d) = %q, want the on-screen bar", row, got)
	}
	if !h.showCompletion(p, 0) {
		t.Fatal("no completion was offered: the session prefix leaked past the fold")
	}
	if got := h.Complete.Prefix(); got != "bar" {
		t.Errorf("completion prefix = %q, want bar, the on-screen text", got)
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

// One jump path: jumpToSessionLine is the single map-aware jump the chord
// (App.jumpTo), the sidebar, the review walk (cycleProposed) and EnterReview
// all funnel through. A fold-hidden target is skipped, and a visible one
// centres on its display row while the caret stays on the session line.
func TestOneJumpPathSkipsHiddenAndCentresVisible(t *testing.T) {
	h := newHarness(t, strings.Repeat("xxxxxxxx\n", 20))
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
	jumpToSessionLine(p, 1) // 1-based line 1, hidden by the fold
	if got := p.Cursors.Primary().Head; got != before {
		t.Errorf("caret moved to %d for a hidden line, want it left at %d", got, before)
	}
	if p.Viewport.Top != top {
		t.Errorf("viewport scrolled to %d for a hidden line, want %d", p.Viewport.Top, top)
	}

	// Session line 15 is visible at display row 14, two hidden lines folded
	// into one row above it: the caret is file-true and the centring is the row.
	if got := p.DispOfDocLine(15); got != 14 {
		t.Fatalf("DispOfDocLine(15) = %d, want 14 below the fold", got)
	}
	jumpToSessionLine(p, 16)
	if got := p.File.LineOf(p.Cursors.Primary().Head); got != 15 {
		t.Fatalf("caret on session line %d after the jump, want 15", got)
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
	if got := h.Complete.Prefix(); got != "foo" {
		t.Errorf("completion prefix = %q with no decisions, want foo", got)
	}
	if line, _ := h.Complete.Anchor(); line != p.File.LineOf(head) {
		t.Errorf("completion anchored on row %d, want session line %d with no decisions",
			line, p.File.LineOf(head))
	}
}

// Every action the palette can offer must have a handler, chordless ones
// included. TestEveryBoundActionIsHandled walks the bound set; this walks
// keys.Unbound, so adding a palette-only command without a dispatch case fails
// here rather than as a silent no-op. The signal is the same "unhandled:"
// status the editor sets when an action reaches a pane with nobody to take it.
//
// Without the dispatch case the action falls through handleGlobal and the
// editor reports it unhandled; before the action was in Unbound this test had
// nothing to walk, which is why listing and handling are checked together.
func TestEveryUnboundActionIsHandled(t *testing.T) {
	skip := map[keys.Action]bool{
		keys.Quit:    true, // ends the session
		keys.Suspend: true, // stops the process
	}
	for _, action := range keys.Unbound {
		if skip[action] {
			continue
		}
		h := newHarness(t, "package main\nfunc F() {}\n")
		h.handleKeyAction(action)
		if strings.HasPrefix(h.Status(), "unhandled:") {
			t.Errorf("%s is runnable from the palette but nothing handles it; "+
				"either add a dispatch case or drop it from keys.Unbound", action)
		}
	}
}

// A palette-only command is listed by name alone: with an empty chord there is
// nothing after the label separator, so the row must not carry one.
//
// Without the label guard the row is "copy relative path  ", with the trailing
// separator of the empty chord column, and this fails.
func TestPaletteRendersChordlessCommandByName(t *testing.T) {
	h := newHarness(t, "package main\n")
	h.press("shift+super+p")
	h.typeText("copy relative path")
	if got, want := h.Picker.Top(), "copy relative path"; got != want {
		t.Errorf("palette row = %q, want %q", got, want)
	}
}

// copy_relative_path is palette-only and copies the workspace-relative spelling
// of the active buffer, which is what a shell rooted at the workspace wants.
//
// Without the dispatch case the clipboard stays empty and the status is
// "unhandled:", so both assertions fail.
func TestCopyRelativePathCopiesRelativeSpelling(t *testing.T) {
	h := newHarness(t, "package main\n")
	h.handleKeyAction(keys.CopyRelPath)
	if got, want := h.host.Clipboard(), "test.go"; got != want {
		t.Errorf("clipboard = %q, want %q", got, want)
	}
	if got := h.Status(); !strings.Contains(got, "test.go") {
		t.Errorf("status = %q, want it to say what was copied", got)
	}
}

// An unnamed buffer has no path, and an empty string on the clipboard would
// look like a copy that worked. It is refused in words instead.
//
// Without the dispatch case the status is "unhandled:" rather than the refusal,
// so the status assertion fails; the clipboard assertion holds either way and is
// here to pin that nothing is written.
func TestCopyRelativePathRefusesUnnamedBuffer(t *testing.T) {
	h := newHarness(t, "package main\n")
	h.Pane().File.Path = ""
	h.handleKeyAction(keys.CopyRelPath)
	if got := h.host.Clipboard(); got != "" {
		t.Errorf("clipboard = %q, want it untouched for an unnamed buffer", got)
	}
	if got := h.Status(); !strings.Contains(got, "no path") {
		t.Errorf("status = %q, want a refusal naming the missing path", got)
	}
}

// A path outside the workspace root — save-as permits one — has no relative
// spelling, so the absolute path is copied and the status line says so rather
// than inventing a "../" path that depends on the process work directory.
//
// Without the dispatch case the clipboard stays empty, so the clipboard
// assertion fails.
func TestCopyRelativePathFallsBackOutsideTheRoot(t *testing.T) {
	h := newHarness(t, "package main\n")
	outside := filepath.Join(t.TempDir(), "elsewhere.go")
	h.Pane().File.Path = outside
	h.handleKeyAction(keys.CopyRelPath)
	if got := h.host.Clipboard(); got != outside {
		t.Errorf("clipboard = %q, want the absolute path %q", got, outside)
	}
	if got := h.Status(); !strings.Contains(got, "outside the workspace") {
		t.Errorf("status = %q, want it to say the path is outside the workspace", got)
	}
}

// A daemon app built on the headless host still serves the control socket: the
// request parks for the event thread, the host-parked wake drives that thread,
// and the reply arrives with no terminal in the loop. Without the headless host
// a daemon had no host to construct; without the host wake the request would
// time out pumping only ticks.
func TestHeadlessHostServesControl(t *testing.T) {
	h := ui.NewHeadlessHost()
	t.Cleanup(func() { h.Close() })
	dir := t.TempDir()
	path := filepath.Join(dir, "a.go")
	if err := os.WriteFile(path, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := NewWithOptions(h, dir, Options{TabWidth: 2})
	t.Cleanup(a.CloseState)
	a.OpenFile(path)
	sock := controlSock(t, "headless.sock")
	if err := a.StartControl(sock); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.StopControl)
	c, err := control.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })

	type result struct {
		res control.Response
		err error
	}
	got := make(chan result, 1)
	go func() {
		res, err := c.Do(control.Request{Op: "buffers"})
		got <- result{res, err}
	}()
	deadline := time.After(control.ReplyTimeout)
	for {
		select {
		case r := <-got:
			if r.err != nil {
				t.Fatalf("buffers: %v", r.err)
			}
			if !r.res.OK || len(r.res.Buffers) != 1 {
				t.Fatalf("buffers = %+v, want the one open buffer", r.res)
			}
			return
		case <-deadline:
			t.Fatal("no reply: the headless event loop did not drain control")
		default:
			// Pump the headless event loop the way Run would.
			select {
			case e := <-h.Events():
				a.Handle(e)
			default:
			}
			time.Sleep(time.Millisecond)
		}
	}
}

// The daemon graceful path is closing the headless host: Run returns on the
// closed event stream, and its deferred save writes the session. Without the
// headless host there is nothing to close; without Run deferred save the
// session would be lost on shutdown.
func TestHeadlessShutdownSavesSession(t *testing.T) {
	h := ui.NewHeadlessHost()
	dir := t.TempDir()
	path := filepath.Join(dir, "a.go")
	if err := os.WriteFile(path, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := NewWithOptions(h, dir, Options{TabWidth: 2})
	t.Cleanup(a.CloseState)
	a.OpenFile(path)

	done := make(chan error, 1)
	go func() { done <- a.Run() }()
	// Close at once: the graceful path, before any idle tick can start a
	// language server. The ticker first tick is 150 ms away.
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not return after the host closed")
	}
	if st := storedSession(t, a); len(st.Tabs) != 1 || st.Tabs[0].Path != path {
		t.Errorf("session = %+v, want the open file saved on the graceful path", st.Tabs)
	}
}
