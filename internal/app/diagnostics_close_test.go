package app

import (
	"path/filepath"
	"strings"
	"testing"

	"raj/internal/lsp"
	"raj/internal/problems"
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
	if !h.diags.published(path) {
		t.Fatal("setup: the publish was not recorded")
	}

	h.press("super+w")
	if h.Tabs.Count() != 0 {
		t.Fatalf("setup: the tab did not close")
	}
	if got := h.diags.forPath(path); len(got) != 0 {
		t.Errorf("%d diagnostics survived the close", len(got))
	}
	// The published flag goes with the list: a reopened file has been heard
	// from by nothing, and must report unpublished rather than clean.
	if h.diags.published(path) {
		t.Error("the closed file still reads as published")
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

// ---------- the problems pane ----------

// The pane is a view over the store, so opening it must show what the store
// already holds rather than only what arrives afterwards.
func TestProblemsPaneShowsExistingDiagnostics(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	path := filepath.Join(h.Explorer.Tree.Root, "main.go")
	h.diags.set(path, []lsp.Diagnostic{{Message: "undefined: needle", Severity: sevError}})

	h.press("shift+super+m")
	if h.sidebar != SidebarProblems {
		t.Fatalf("the pane did not open; sidebar = %v", h.sidebar)
	}
	if h.Problems.Count() != 1 {
		t.Errorf("%d problems in the pane, want 1", h.Problems.Count())
	}
	if !strings.Contains(h.host.Text(), "undefined: needle") {
		t.Errorf("the message is not on screen:\n%s", h.host.Text())
	}
}

// Enter on a problem opens its file at its line, which is the whole point of
// the pane: the gutter answers "what is wrong here", this answers "where".
func TestProblemsPaneJumps(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	path := filepath.Join(h.Explorer.Tree.Root, "main.go")
	var d lsp.Diagnostic
	d.Range.Start.Line = 2
	d.Severity = sevError
	d.Message = "boom"
	h.diags.set(path, []lsp.Diagnostic{d})

	h.press("shift+super+m")
	h.press("down")  // onto the problem, past the heading
	h.press("enter") // open it

	if h.Pane() == nil {
		t.Fatal("nothing opened")
	}
	if got := h.Pane().File.Name(); got != "main.go" {
		t.Errorf("opened %s", got)
	}
	line, _ := h.Pane().File.LineCol(h.Pane().Cursors.Primary().Head)
	if line != 2 {
		t.Errorf("landed on line %d, want 2", line)
	}
}

// Closing a file removes its problems from the pane as well as from the store.
// A list naming a file no tab is showing, with problems nothing will re-check,
// is worse than an empty list.
func TestClosingAFileRemovesItFromTheProblemsPane(t *testing.T) {
	h := newWorkspace(t, 120, 24)
	path := filepath.Join(h.Explorer.Tree.Root, "main.go")
	h.OpenFile(path)
	h.diags.set(path, []lsp.Diagnostic{{Message: "boom", Severity: sevError}})
	h.press("shift+super+m")
	if h.Problems.Count() != 1 {
		t.Fatal("setup: the problem is not in the pane")
	}

	h.focus = FocusEditor
	h.press("super+w")
	if h.Problems.Count() != 0 {
		t.Errorf("%d problems left after the tab closed", h.Problems.Count())
	}
}

// The pane's severity ranking and the app's must agree. Two places deciding
// this differently is how a file shows a red mark in the gutter and nothing in
// the list — so the agreement is asserted rather than assumed, since the two
// switch statements are deliberately duplicated.
func TestSeverityRankingsAgree(t *testing.T) {
	for sev := 0; sev <= 5; sev++ {
		var d lsp.Diagnostic
		d.Severity = sev
		d.Message = "x"
		p := problems.New()
		p.Set([]problems.File{{Path: "/w/a.go", Items: []lsp.Diagnostic{d}}})
		hdr := p.Rows()[0]

		store := newDiagnostics()
		store.set("/w/a.go", []lsp.Diagnostic{d})
		errs, warns := store.counts("/w/a.go")

		if hdr.Errors != errs || hdr.Warnings != warns {
			t.Errorf("severity %d: pane says %dE %dW, store says %dE %dW",
				sev, hdr.Errors, hdr.Warnings, errs, warns)
		}
	}
}
