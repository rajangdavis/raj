package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"raj/internal/piecetable"
	"raj/internal/store"
)

// A refused save must leave the tab open, the refusal in the status, the
// proposed set rolled back and the file on disk untouched. The human save
// gesture is super+s, which opens the save review for a pending set, so enter
// answers it. A following close must still ask, not take the refusal as
// permission to drop the buffer silently.
func TestRefusedSaveKeepsTheTabAndCloseStillPrompts(t *testing.T) {
	t.Parallel()
	const original = "package p\n\nfunc f() {\n\tx()\n\ty()\n}\n"
	root := t.TempDir()
	seedSettings(t, root, store.ScopeWorkspace, map[string]string{"save_check": "parse"})
	path := filepath.Join(root, "test.go")
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	h := newHarnessAt(t, root)
	h.OpenFile(path)
	id := propose(t, h, piecetable.Hunk{Start: len(original), End: len(original), Text: ")"})
	if got := h.Tabs.Count(); got != 1 {
		t.Fatalf("setup: tabs = %d, want 1", got)
	}

	h.press("super+s") // the save gesture opens the review for the pending set
	if !h.Prompt.Open {
		t.Fatal("super+s with a pending set did not open the save review")
	}
	h.press("enter") // answer the review: accept all and save

	if got := h.Tabs.Count(); got != 1 {
		t.Fatalf("tabs after a refused save = %d, want the tab kept", got)
	}
	if got := h.Pane().File.Path; got != path {
		t.Errorf("pane path = %q, want %q", got, path)
	}
	if got := h.Status(); !strings.Contains(got, "save refused") {
		t.Errorf("status = %q, want the composition refusal", got)
	}
	if got := h.Pane().File.Session().GroupState(id); got != piecetable.Proposed {
		t.Errorf("state after a refused save = %v, want the set rolled back to Proposed", got)
	}
	if got := len(h.Pane().File.Session().Pending()); got != 1 {
		t.Errorf("pending after a refused save = %d, want 1", got)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != original {
		t.Errorf("file = %q err %v, want the original bytes untouched", got, err)
	}

	h.press("super+w")
	if got := h.Tabs.Count(); got != 1 {
		t.Fatalf("tabs after the close gesture = %d, want the tab kept", got)
	}
	if !h.Prompt.Open {
		t.Fatal("closing after a refused save did not ask")
	}
}

// The dangerous shape is a refusal where the view already matches disk. Here a
// proposed fix (the closer that balances an unmatched brace) is saved, so its
// text reaches disk and the view equals the file; the set is then rejected,
// which is a state-only decision, so the view still equals disk while the
// accepted composition a save would write (the set's text excluded) is
// unbalanced. The save refuses, but ViewDirty alone reads the buffer as clean,
// so closeTabAt's no-prompt branch would drop the tab without asking. The
// refusal must still protect the pane.
func TestRefusedSaveWithTheViewMatchingDiskStillPromptsOnClose(t *testing.T) {
	t.Parallel()
	const original = "package p\n\nfunc f() {\n" // unbalanced on disk, before the proposed fix
	root := t.TempDir()
	seedSettings(t, root, store.ScopeWorkspace, map[string]string{"save_check": "parse"})
	path := filepath.Join(root, "test.go")
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	h := newHarnessAt(t, root)
	h.OpenFile(path)
	p := h.Pane()
	id := propose(t, h, piecetable.Hunk{Start: len(original), End: len(original), Text: "}"})

	h.press("super+s")
	if !h.Prompt.Open {
		t.Fatal("super+s with a pending set did not open the save review")
	}
	h.press("enter") // accept the fix and write it: "package p\n\nfunc f() {\n}" reaches disk
	if p.File.ViewDirty() {
		t.Fatalf("setup: the view still differs from disk after the save")
	}

	if !p.File.RejectGroup(id) {
		t.Fatal("reject failed")
	}
	if p.File.ViewDirty() {
		t.Fatalf("setup: rejecting an already-saved set left the view differing from disk")
	}

	h.press("super+s") // the accepted composition is unbalanced now: refused
	if got := h.Status(); !strings.Contains(got, "save refused") {
		t.Fatalf("status = %q, want the composition refusal", got)
	}
	if got := h.Tabs.Count(); got != 1 {
		t.Fatalf("tabs after the refused save = %d, want the tab kept", got)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != original+"}" {
		t.Errorf("file = %q err %v, want the saved bytes untouched", got, err)
	}

	h.press("super+w")
	if got := h.Tabs.Count(); got != 1 {
		t.Fatalf("tabs after the close gesture = %d, want the tab kept", got)
	}
	if !h.Prompt.Open {
		t.Fatal("a refused save left the close silent; the tab was closed without asking")
	}
}
