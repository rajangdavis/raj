package app

import "testing"

// The sidebar panes are a segment of the focus ring with an exit at each end.
// Before this, shift+tab stopped dead at the first component, so the only way
// back to the editor from there was tabbing all the way forward or a chord.

// The explorer opens on the tree rather than its filter field, so the first
// shift+tab walks back within the pane and only the second leaves it.
func TestShiftTabLeavesTheExplorerBackwards(t *testing.T) {
	h := newHarness(t, "body")
	h.press("shift+super+e")
	if h.Focused() != FocusSidebar {
		t.Fatalf("focus = %v, want the sidebar", h.Focused())
	}

	h.press("shift+tab")
	if h.Focused() != FocusSidebar {
		t.Fatal("shift+tab left the pane from the tree; it should reach the filter first")
	}

	h.press("shift+tab")
	if h.Focused() != FocusEditor {
		t.Errorf("focus = %v, want the editor", h.Focused())
	}
}

func TestShiftTabLeavesTheSearchPaneBackwards(t *testing.T) {
	h := newHarness(t, "body")
	h.press("shift+super+f")
	if h.Focused() != FocusSidebar {
		t.Fatalf("focus = %v, want the sidebar", h.Focused())
	}
	h.press("shift+tab")

	if h.Focused() != FocusEditor {
		t.Errorf("focus = %v, want the editor", h.Focused())
	}
}

// Only from the first component. Elsewhere shift+tab still walks the ring, or
// the pane would be impossible to move backwards through at all.
func TestShiftTabWalksBackWithinThePane(t *testing.T) {
	// Tall enough for the full layout: the compact one has no glob fields to
	// walk back through.
	h := newHarnessSize(t, "body", 120, 24)
	h.press("shift+super+f")
	h.press("tab", "tab") // query -> include -> exclude
	h.press("shift+tab")

	if h.Focused() != FocusSidebar {
		t.Fatalf("shift+tab left the pane from the middle of the ring")
	}
	if in := h.Search.ActiveInput(); in == nil {
		t.Fatal("focus is not on a field")
	}
	h.typeText("glob")
	if got := h.Search.ActiveInput().Text; got != "glob" {
		t.Errorf("typed into %q; expected to be back on the include field", got)
	}
}

// Leaving is one key; coming back is a chord. Tab indents in the document, so a
// one-key route in would make editing interruptible — that is the constraint
// the old dead end was protecting, and it must survive.
func TestTabInTheEditorDoesNotReturnToTheSidebar(t *testing.T) {
	h := newHarness(t, "body")
	h.press("shift+super+e")
	h.press("shift+tab", "shift+tab")
	if h.Focused() != FocusEditor {
		t.Fatalf("setup: focus = %v, want the editor", h.Focused())
	}

	h.press("tab")
	if h.Focused() != FocusEditor {
		t.Errorf("tab left the editor; focus = %v", h.Focused())
	}
	if got := h.text(); got == "body" {
		t.Error("tab did not indent the document")
	}

	h.press("shift+tab")
	if h.Focused() != FocusEditor {
		t.Errorf("shift+tab left the editor; focus = %v", h.Focused())
	}
}

// Forward still exits too — the change must not have traded one dead end for
// another.
func TestTabStillLeavesTheExplorerForwards(t *testing.T) {
	h := newHarness(t, "body")
	h.press("shift+super+e")
	for i := 0; i < 8 && h.Focused() == FocusSidebar; i++ {
		h.press("tab")
	}
	if h.Focused() != FocusEditor {
		t.Errorf("tabbing forward never reached the editor; focus = %v", h.Focused())
	}
}
