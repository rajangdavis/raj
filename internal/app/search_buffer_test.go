package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// results renders the search pane's matches as "base:line" so a failure prints
// the whole list.
func results(h *harness) []string {
	out := make([]string, 0, len(h.Search.Result.Matches))
	for _, m := range h.Search.Result.Matches {
		out = append(out, filepath.Base(m.Path)+":"+itoa(m.Line))
	}
	return out
}

func query(h *harness, text string) {
	h.press("shift+super+f")
	h.typeText(text)
	h.press("enter")
}

// End to end: type into a buffer, search for it without saving, find it.
func TestSearchFindsUnsavedText(t *testing.T) {
	h := newWorkspace(t, 120, 30)
	h.OpenFile(filepath.Join(h.root, "main.go"))
	h.drain()
	h.typeText("marmoset")
	if !h.Pane().File.Dirty() {
		t.Fatal("setup: the buffer should be dirty")
	}

	query(h, "marmoset")
	if got := results(h); len(got) != 1 {
		t.Fatalf("results = %v, want one match in the unsaved buffer", got)
	}
}

// And the other direction: text deleted but not yet saved must stop matching,
// or the pane sends you to a line that no longer says that.
func TestSearchDoesNotReportDeletedText(t *testing.T) {
	h := newWorkspace(t, 120, 30)
	query(h, "unrelated")
	if got := results(h); len(got) != 1 {
		t.Fatalf("setup: disk results = %v, want one match to delete", got)
	}

	h.OpenFile(filepath.Join(h.root, "pkg/other.go"))
	h.drain()
	h.press("super+a") // select all
	h.press("backspace")
	h.typeText("package pkg\n")
	if strings.Contains(h.Pane().File.Text(), "unrelated") {
		t.Fatal("setup: the buffer still holds the text")
	}

	query(h, "unrelated")
	if got := results(h); len(got) != 0 {
		t.Errorf("reported stale matches from disk: %v", got)
	}
}

// A saved buffer is the same bytes as its file, so saving must not change what
// a search reports — the snapshot dropping out from under it is the seam where
// a double count or a miss would show up.
func TestSavingDoesNotChangeResults(t *testing.T) {
	h := newWorkspace(t, 120, 30)
	h.OpenFile(filepath.Join(h.root, "main.go"))
	h.drain()
	h.typeText("marmoset")

	query(h, "marmoset")
	before := results(h)
	h.press("super+s")
	h.press("shift+super+f")
	h.drain()
	query(h, "marmoset")

	after := results(h)
	if len(before) != len(after) {
		t.Errorf("before save %v, after save %v", before, after)
	}
	if len(after) != 1 {
		t.Errorf("results after saving = %v, want one", after)
	}
}

// A file created on disk while raj is running and never opened is still found,
// which is the case a buffer overlay could plausibly have broken by replacing
// the walk rather than layering on it.
func TestSearchStillReadsTheDisk(t *testing.T) {
	h := newWorkspace(t, 120, 30)
	if err := os.WriteFile(filepath.Join(h.root, "fresh.go"), []byte("var marmoset = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	query(h, "marmoset")
	if got := results(h); len(got) != 1 {
		t.Errorf("results = %v, want the file on disk", got)
	}
}
