package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"raj/internal/prompt"
)

// These drive the same path a keystroke takes in the terminal — chord, decode,
// keymap, dispatch, render — so they fail if any layer of the new bindings is
// missing, not only the last one.

func TestNewFileOpensAnUnnamedTab(t *testing.T) {
	h := newHarness(t, "original")
	before := h.Tabs.Count()

	h.press("super+n")

	if got := h.Tabs.Count(); got != before+1 {
		t.Fatalf("tab count = %d, want %d", got, before+1)
	}
	p := h.Pane()
	if p.File.Path != "" {
		t.Errorf("new buffer has path %q, want empty", p.File.Path)
	}
	if p.File.Text() != "" {
		t.Errorf("new buffer = %q, want empty", p.File.Text())
	}
	if p.File.Name() != "untitled" {
		t.Errorf("name = %q, want untitled", p.File.Name())
	}
	if h.Focused() != FocusEditor {
		t.Errorf("focus = %v, want the editor", h.Focused())
	}
}

// Two presses mean two buffers. Tabs dedupes by path and an unnamed buffer has
// none, so this is the case where that dedupe could wrongly collapse them.
func TestNewFileTwiceOpensTwoBuffers(t *testing.T) {
	h := newHarness(t, "")
	before := h.Tabs.Count()
	h.press("super+n")
	h.typeText("first")
	h.press("super+n")
	h.typeText("second")

	if got := h.Tabs.Count(); got != before+2 {
		t.Fatalf("tab count = %d, want %d", got, before+2)
	}
	if got := h.Pane().File.Text(); got != "second" {
		t.Errorf("active buffer = %q, want second", got)
	}
}

func TestSaveAsWritesToTheChosenPath(t *testing.T) {
	h := newHarness(t, "")
	h.press("super+n")
	h.typeText("scratch contents")
	h.press("super+s")

	if !h.Prompt.Open {
		t.Fatal("saving an unnamed buffer did not ask for a path")
	}
	if want := h.root + string(filepath.Separator); h.Prompt.Text() != want {
		t.Errorf("field seeded with %q, want %q", h.Prompt.Text(), want)
	}

	h.typeText("notes.md")
	h.press("enter")

	if h.Prompt.Open {
		t.Fatal("dialog still open after confirming")
	}
	path := filepath.Join(h.root, "notes.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("file not written: %v", err)
	}
	if string(data) != "scratch contents" {
		t.Errorf("on disk = %q", data)
	}
	if h.Pane().File.Path != path {
		t.Errorf("buffer path = %q, want %q", h.Pane().File.Path, path)
	}
	if h.Pane().File.Dirty() {
		t.Error("buffer still dirty after a successful save")
	}
	if h.Focused() != FocusEditor {
		t.Errorf("focus = %v, want the editor back", h.Focused())
	}
}

// A cancelled save-as must leave the disk alone AND leave the buffer unnamed,
// so the next cmd+s asks again rather than writing somewhere half-chosen.
func TestSaveAsCancelledWritesNothing(t *testing.T) {
	h := newHarness(t, "")
	h.press("super+n")
	h.typeText("unsaved")
	h.press("super+s")
	h.typeText("gone.md")
	h.press("esc")

	if h.Prompt.Open {
		t.Fatal("escape did not dismiss the dialog")
	}
	if _, err := os.Stat(filepath.Join(h.root, "gone.md")); !os.IsNotExist(err) {
		t.Errorf("file was written despite cancelling (err=%v)", err)
	}
	if h.Pane().File.Path != "" {
		t.Errorf("buffer was renamed to %q despite cancelling", h.Pane().File.Path)
	}
}

func TestSaveAsRelativePathResolvesAgainstTheRoot(t *testing.T) {
	h := newHarness(t, "")
	if err := os.Mkdir(filepath.Join(h.root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	h.press("super+n")
	h.typeText("nested")
	h.press("super+s")
	// Clear the seeded absolute path and answer with a bare relative one.
	h.press("super+a")
	h.typeText("sub/deep.txt")
	h.press("enter")

	if _, err := os.ReadFile(filepath.Join(h.root, "sub", "deep.txt")); err != nil {
		t.Fatalf("relative path did not resolve against the root: %v", err)
	}
}

func TestSaveAsOverExistingFileAsksFirst(t *testing.T) {
	for _, tc := range []struct {
		name    string
		keys    []string
		wantOld bool
	}{
		{"overwrite", []string{"enter"}, false},
		{"cancel", []string{"right", "enter"}, true},
		{"escape", []string{"esc"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, "")
			victim := filepath.Join(h.root, "victim.txt")
			if err := os.WriteFile(victim, []byte("old"), 0o644); err != nil {
				t.Fatal(err)
			}
			h.press("super+n")
			h.typeText("new")
			h.press("super+s")
			h.typeText("victim.txt")
			h.press("enter")

			if !h.Prompt.Open {
				t.Fatal("saving over an existing file did not ask")
			}
			if got := h.Prompt.Selected(); got != prompt.Overwrite {
				t.Errorf("default answer = %q, want %q", got, prompt.Overwrite)
			}
			h.press(tc.keys...)

			data, err := os.ReadFile(victim)
			if err != nil {
				t.Fatal(err)
			}
			if got := string(data) == "old"; got != tc.wantOld {
				t.Errorf("file = %q, wanted the original kept = %v", data, tc.wantOld)
			}
		})
	}
}

// A named file saves straight through: the dialog is for buffers that have
// nowhere to go, and making cmd+s always ask would be intolerable.
func TestSaveOnANamedFileDoesNotAsk(t *testing.T) {
	h := newHarness(t, "start")
	h.typeText("x")
	h.press("super+s")

	if h.Prompt.Open {
		t.Fatal("cmd+s on a named file opened a dialog")
	}
	data, err := os.ReadFile(h.Pane().File.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "xstart" {
		t.Errorf("on disk = %q, want xstart", data)
	}
}

func TestCloseCleanTabDoesNotAsk(t *testing.T) {
	h := newHarness(t, "untouched")
	h.press("super+w")

	if h.Prompt.Open {
		t.Fatal("closing a clean tab asked a question")
	}
	if got := h.Tabs.Count(); got != 0 {
		t.Errorf("tab count = %d, want 0", got)
	}
}

// The core guarantee: a tab with unsaved changes is never closed without an
// answer that asked for it.
func TestCloseDirtyTabAsksBeforeDiscarding(t *testing.T) {
	for _, tc := range []struct {
		name      string
		answer    []string
		wantOpen  int
		wantOnDsk string
	}{
		{"save", []string{"enter"}, 0, "editedbase"},
		{"discard", []string{"right", "enter"}, 0, "base"},
		{"cancel", []string{"right", "right", "enter"}, 1, "base"},
		{"escape", []string{"esc"}, 1, "base"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, "base")
			path := h.Pane().File.Path
			h.typeText("edited")
			if !h.Pane().File.Dirty() {
				t.Fatal("setup: buffer is not dirty")
			}

			h.press("super+w")
			if !h.Prompt.Open {
				t.Fatal("closing a dirty tab did not ask")
			}
			if got := h.Prompt.Selected(); got != prompt.Save {
				t.Errorf("default answer = %q, want %q", got, prompt.Save)
			}
			h.press(tc.answer...)

			if got := h.Tabs.Count(); got != tc.wantOpen {
				t.Errorf("tab count = %d, want %d", got, tc.wantOpen)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != tc.wantOnDsk {
				t.Errorf("on disk = %q, want %q", data, tc.wantOnDsk)
			}
		})
	}
}

// Closing a dirty unnamed buffer is the chain: confirm, then save-as, then
// close — and only if the second question is answered too.
func TestCloseDirtyUnnamedBufferChainsIntoSaveAs(t *testing.T) {
	h := newHarness(t, "")
	before := h.Tabs.Count()
	h.press("super+n")
	h.typeText("keep me")
	h.press("super+w")

	if !h.Prompt.Open {
		t.Fatal("no confirmation for the dirty scratch buffer")
	}
	h.press("enter") // Save

	if !h.Prompt.Open {
		t.Fatal("answering Save did not go on to ask for a path")
	}
	if got := h.Prompt.Title(); got != "Save as" {
		t.Fatalf("second dialog is %q, want Save as", got)
	}
	h.typeText("kept.txt")
	h.press("enter")

	if h.Prompt.Open {
		t.Fatal("dialog still open")
	}
	data, err := os.ReadFile(filepath.Join(h.root, "kept.txt"))
	if err != nil {
		t.Fatalf("file not written: %v", err)
	}
	if string(data) != "keep me" {
		t.Errorf("on disk = %q", data)
	}
	if got := h.Tabs.Count(); got != before {
		t.Errorf("tab count = %d, want %d — the tab should have closed", got, before)
	}
}

// Cancelling the path question must abandon the close as well. Losing the work
// here would be the worst outcome available: the user asked to keep it.
func TestCancellingSaveAsAbandonsTheClose(t *testing.T) {
	h := newHarness(t, "")
	h.press("super+n")
	h.typeText("precious")
	after := h.Tabs.Count()

	h.press("super+w")
	h.press("enter") // Save
	h.press("esc")   // ...but do not choose a path

	if got := h.Tabs.Count(); got != after {
		t.Fatalf("tab count = %d, want %d — the tab must survive", got, after)
	}
	if got := h.Pane().File.Text(); got != "precious" {
		t.Errorf("buffer = %q, want precious", got)
	}
}

// A dialog is modal. While it is open the global chords underneath must not
// fire — least of all the one that would close the tab being asked about.
func TestDialogSwallowsGlobalChords(t *testing.T) {
	h := newHarness(t, "base")
	h.typeText("edit")
	h.press("super+w")
	if !h.Prompt.Open {
		t.Fatal("setup: no dialog")
	}

	h.press("super+w", "super+p", "super+b")

	if !h.Prompt.Open {
		t.Fatal("a global chord dismissed the dialog")
	}
	if got := h.Tabs.Count(); got != 1 {
		t.Errorf("tab count = %d, want 1 — cmd+w fired underneath the dialog", got)
	}
	if h.Picker.Open {
		t.Error("cmd+p opened the picker underneath the dialog")
	}
}

func TestDialogIsRendered(t *testing.T) {
	h := newHarness(t, "base")
	h.typeText("edit")
	h.press("super+w")

	screen := h.host.Text()
	for _, want := range []string{"Unsaved changes", prompt.Save, prompt.Discard, prompt.Cancel} {
		if !strings.Contains(screen, want) {
			t.Errorf("%q not on screen:\n%s", want, screen)
		}
	}
}

func TestSaveAsSetsTheLanguageForHighlighting(t *testing.T) {
	h := newHarness(t, "")
	h.press("super+n")
	h.typeText("package main")
	if h.Pane().File.Syntax.Enabled() {
		t.Fatal("an unnamed buffer should have no language")
	}

	h.press("super+s")
	h.typeText("prog.go")
	h.press("enter")

	if !h.Pane().File.Syntax.Enabled() {
		t.Error("saving as .go did not enable highlighting")
	}
}
