package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	if got := h.text(); got != "  line" {
		t.Fatalf("after indent = %q, want two spaces", got)
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
