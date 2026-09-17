package app

import (
	"strings"
	"testing"

	"raj/internal/keys"
	"raj/internal/picker"
)

// cmd+shift+p was a bound chord with nothing behind it, so it was taken from
// the terminal for nothing. It opens the palette over the keymap, which is what
// makes every other chord discoverable by name.
func TestCommandPaletteOpensAndLists(t *testing.T) {
	h := newHarness(t, "package main\n")
	h.press("shift+super+p")

	if !h.Picker.Open || h.Focused() != FocusPicker {
		t.Fatal("cmd+shift+p did not open the command palette")
	}
	if h.Picker.Mode() != picker.Commands {
		t.Errorf("palette mode = %v, want Commands", h.Picker.Mode())
	}
	// Every bound action but the palette itself: running "command palette" from
	// inside the palette would only reopen it.
	if got, want := h.Picker.Results(), len(keys.Commands())-1; got != want {
		t.Errorf("palette listed %d actions, want %d", got, want)
	}
}

// The list filters like the file and symbol overlays do, and the row it settles
// on is the action the query names rather than an arbitrary subsequence match.
func TestCommandPaletteFilters(t *testing.T) {
	h := newHarness(t, "package main\n")
	h.press("shift+super+p")
	all := h.Picker.Results()

	h.typeText("sidebar")
	got := h.Picker.Results()
	if got == 0 {
		t.Fatal("no action matched \"sidebar\"")
	}
	if got >= all {
		t.Errorf("query listed %d of %d actions; it did not narrow the list", got, all)
	}
	if top := h.Picker.Top(); !strings.Contains(top, "toggle sidebar") {
		t.Errorf("top match = %q, want the sidebar toggle", top)
	}
}

// Enter runs the chosen action through the same dispatch a chord takes, so a
// palette entry does exactly what pressing its chord does. Toggling the sidebar
// is the harmless, observable stand-in for that dispatch.
func TestCommandPaletteRunsTheChosenAction(t *testing.T) {
	h := newHarness(t, "package main\n")
	// The harness opens a document into the explorer New starts in, so close
	// it first: the toggle below is only observable against a known state,
	// the same reason openSidebar never assumes where it started.
	if h.SidebarMode() != SidebarNone {
		h.press("super+b")
	}
	if h.SidebarMode() != SidebarNone {
		t.Fatal("setup: the sidebar is not closed")
	}
	h.press("shift+super+p")
	h.typeText("toggle sidebar")
	h.press("enter")

	if got := h.SidebarMode(); got != SidebarExplorer {
		t.Errorf("sidebar = %v, want explorer after running toggle sidebar", got)
	}
	if h.Picker.Open {
		t.Error("the palette stayed open after running a command")
	}
}

// The dispatch is the key path, refusals included: in review mode an edit
// action chosen from the palette is refused exactly as its chord is, and the
// document is untouched.
func TestCommandPaletteHonoursReviewMode(t *testing.T) {
	h := newHarness(t, "one\ntwo\n")
	h.press("super+r") // toggle_review
	if h.mode != ModeReview {
		t.Fatal("setup: review mode did not turn on")
	}
	before := h.text()

	h.press("shift+super+p")
	if !h.Picker.Open {
		t.Fatal("the palette did not open")
	}
	h.typeText("delete line")
	h.press("enter")

	if got := h.text(); got != before {
		t.Errorf("delete line ran from the palette in review mode: %q", got)
	}
	if got := h.Status(); !strings.Contains(got, "read-only in review mode") {
		t.Errorf("status = %q, want the review-mode refusal", got)
	}
}
