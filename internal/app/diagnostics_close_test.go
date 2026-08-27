package app

import (
	"path/filepath"
	"testing"

	"raj/internal/lsp"
)

// Diagnostics outlive the request that produced them -- they are published, not
// asked for -- so the thing that has to be got right is when they stop being
// true. Closing a file is the clearest case: nothing will re-check it, so
// nothing it said should survive.

// Closing a file forgets its problems. The store is keyed by path, so without
// this they are held for the life of the session — counted in nothing visible,
// and resurrected stale the moment the file is reopened.
func TestClosingATabClearsItsDiagnostics(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	path := filepath.Join(h.Explorer.Tree.Root, "main.go")
	h.OpenFile(path)
	h.drain()

	h.diags.set(path, []lsp.Diagnostic{
		{Message: "undefined: needle", Severity: sevError},
	})
	if len(h.diags.forPath(path)) != 1 {
		t.Fatal("setup: the diagnostic was not stored")
	}

	h.press("super+w")
	if h.Tabs.Count() != 0 {
		t.Fatalf("setup: the tab did not close")
	}
	if got := h.diags.forPath(path); len(got) != 0 {
		t.Errorf("%d diagnostics survived the close", len(got))
	}
}

// Reopening a closed file must not show the problems it had when it was closed.
// They describe a version of the file that nothing has re-checked.
func TestReopeningDoesNotResurrectDiagnostics(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	path := filepath.Join(h.Explorer.Tree.Root, "main.go")
	h.OpenFile(path)
	h.diags.set(path, []lsp.Diagnostic{{Message: "stale", Severity: sevError}})
	h.press("super+w")
	h.OpenFile(path)
	h.drain()

	if sum := h.diags.summary(path); sum != "" {
		t.Errorf("status summary = %q, want nothing for a freshly reopened file", sum)
	}
}

// An unnamed buffer has no path to key anything on, and closing one must not
// panic or clear somebody else's diagnostics.
func TestClosingAnUnnamedBufferIsHarmless(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	path := filepath.Join(h.Explorer.Tree.Root, "main.go")
	h.OpenFile(path)
	h.diags.set(path, []lsp.Diagnostic{{Message: "real", Severity: sevError}})

	h.press("super+n") // a scratch buffer
	h.press("super+w")

	if len(h.diags.forPath(path)) != 1 {
		t.Error("closing a scratch buffer cleared another file's diagnostics")
	}
}
