package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"raj/internal/prompt"
)

// saveAsPrompt opens an unnamed buffer and gets to the save-as field.
func saveAsPrompt(t *testing.T, h *harness) {
	t.Helper()
	h.press("super+n")
	h.typeText("hello\n")
	h.press("super+s")
	if !h.Prompt.Open {
		t.Fatal("save-as did not open")
	}
}

// setPromptText replaces the field contents, standing in for typing a path.
// The field is seeded with the workspace root, so a test wanting an absolute
// path of its own has to clear it first.
func setPromptText(h *harness, text string) {
	for range h.Prompt.Text() {
		h.press("backspace")
	}
	h.typeText(text)
}

// answer moves a confirm dialog to an option and takes it. Dialogs are answered
// with arrows and enter, so this is what a user does rather than a shortcut
// around the key path.
func answer(h *harness, want string) {
	for i := 0; i < 6 && h.Prompt.Selected() != want; i++ {
		h.press("right")
	}
	if h.Prompt.Selected() != want {
		panic("option not found: " + want)
	}
	h.press("enter")
}

// ---------- creating a missing directory ----------

// Saving into a directory that is not there used to fail with the raw
// os.WriteFile error — "no such file or directory" against a path just typed in
// full, which reads as the save being rejected rather than as a missing folder.
func TestSaveAsOffersToCreateAMissingDirectory(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	saveAsPrompt(t, h)
	setPromptText(h, filepath.Join(h.Explorer.Tree.Root, "new", "deep", "notes.md"))
	h.press("enter")

	if !h.Prompt.Open {
		t.Fatalf("no dialog; status = %q", h.Status())
	}
	if !strings.Contains(h.host.Text(), "does not exist") {
		t.Fatalf("the dialog does not explain itself:\n%s", h.host.Text())
	}
}

// Answering Create makes the directory and completes the save.
func TestCreatingTheDirectorySaves(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	root := h.Explorer.Tree.Root
	saveAsPrompt(t, h)
	setPromptText(h, filepath.Join(root, "new", "deep", "notes.md"))
	h.press("enter")
	answer(h, prompt.Create)

	path := filepath.Join(root, "new", "deep", "notes.md")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the file was not written: %v", err)
	}
	if !strings.Contains(string(body), "hello") {
		t.Errorf("contents = %q", body)
	}
}

// Declining makes nothing. Creating directories is the one step of save-as that
// puts something on disk the user did not name, so a typo must not leave a
// stray tree behind.
func TestDecliningLeavesNoDirectory(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	root := h.Explorer.Tree.Root
	saveAsPrompt(t, h)
	setPromptText(h, filepath.Join(root, "typo", "notes.md"))
	h.press("enter")
	answer(h, prompt.Cancel)

	if _, err := os.Stat(filepath.Join(root, "typo")); err == nil {
		t.Error("a directory was created after the question was declined")
	}
}

// An existing directory is not asked about: the question is only worth asking
// when the answer changes something.
func TestExistingDirectoryIsNotQueried(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	root := h.Explorer.Tree.Root
	saveAsPrompt(t, h)
	setPromptText(h, filepath.Join(root, "notes.md"))
	h.press("enter")

	if h.Prompt.Open {
		t.Errorf("a dialog opened for a directory that exists: %q", h.Prompt.Title())
	}
	if _, err := os.Stat(filepath.Join(root, "notes.md")); err != nil {
		t.Errorf("the file was not written: %v", err)
	}
}

// A failed save must not leave the buffer claiming a path it is not at, or the
// next plain save writes there without asking — turning one visible failure
// into a silent one.
func TestFailedSaveRestoresThePath(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	root := h.Explorer.Tree.Root
	// Saving onto a directory fails for anyone, including root — which
	// permissions do not, so a chmod-based version of this test passes
	// vacuously in a container.
	target := filepath.Join(root, "pkg")

	saveAsPrompt(t, h)
	before := h.Pane().File.Path
	setPromptText(h, target)
	h.press("enter")
	if h.Prompt.Open {
		answer(h, prompt.Overwrite) // it exists, so it asks first
	}

	if h.Pane() == nil {
		t.Fatal("the pane vanished")
	}
	if got := h.Pane().File.Path; got != before {
		t.Errorf("path = %q after a failed save, want it left as %q", got, before)
	}
}

// ---------- tab completion ----------

// Tab in a path field completes, which is what made save-as feel like a text
// box rather than a file dialog.
func TestTabCompletesAUniqueName(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	root := h.Explorer.Tree.Root
	saveAsPrompt(t, h)
	setPromptText(h, filepath.Join(root, "READ"))
	h.press("tab")

	if got := h.Prompt.Text(); !strings.HasSuffix(got, "README.md") {
		t.Errorf("field = %q, want it completed to README.md", got)
	}
}

// Ambiguity completes as far as the names agree and then stops, so repeated
// tabs converge rather than cycling through candidates.
func TestTabCompletesToTheCommonPrefix(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	root := h.Explorer.Tree.Root
	for _, n := range []string{"report-a.md", "report-b.md"} {
		if err := os.WriteFile(filepath.Join(root, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	saveAsPrompt(t, h)
	setPromptText(h, filepath.Join(root, "rep"))
	h.press("tab")

	got := h.Prompt.Text()
	if !strings.HasSuffix(got, "report-") {
		t.Errorf("field = %q, want it stopped at the shared prefix %q", got, "report-")
	}
	h.press("tab")
	if second := h.Prompt.Text(); second != got {
		t.Errorf("a second tab changed %q to %q; it should have converged", got, second)
	}
}

// A completed directory gets a separator, so tab walks down a tree one press
// per level rather than needing a slash typed between each.
func TestTabAppendsASeparatorForDirectories(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	root := h.Explorer.Tree.Root
	saveAsPrompt(t, h)
	setPromptText(h, filepath.Join(root, "pk"))
	h.press("tab")

	if got := h.Prompt.Text(); !strings.HasSuffix(got, "pkg"+string(filepath.Separator)) {
		t.Errorf("field = %q, want a trailing separator after the directory", got)
	}
}

// Nothing to complete does nothing, rather than clearing the field or beeping.
func TestTabWithNoMatchesLeavesTheFieldAlone(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	root := h.Explorer.Tree.Root
	saveAsPrompt(t, h)
	want := filepath.Join(root, "zzzz")
	setPromptText(h, want)
	h.press("tab")

	if got := h.Prompt.Text(); got != want {
		t.Errorf("field = %q, want it untouched at %q", got, want)
	}
}

// Hidden files are completed only when asked for, the same rule the tree and
// the search walk use.
func TestTabSkipsHiddenFilesUnlessAsked(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	root := h.Explorer.Tree.Root
	if err := os.WriteFile(filepath.Join(root, ".secret"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := h.completePath(filepath.Join(root, "")); strings.Contains(got, ".secret") {
		t.Errorf("a bare prefix completed to a hidden file: %q", got)
	}
	if got := h.completePath(filepath.Join(root, ".sec")); !strings.HasSuffix(got, ".secret") {
		t.Errorf("an explicit dot prefix did not complete: %q", got)
	}
}

// Tab must not complete in a question whose answer is not a path.
func TestTabDoesNotCompleteInAPlainQuestion(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	h.ask("Rename", "REA", func(string, bool) {})
	h.drain()
	h.press("tab")

	if got := h.Prompt.Text(); got != "REA" {
		t.Errorf("field = %q; a plain question completed a path", got)
	}
}
