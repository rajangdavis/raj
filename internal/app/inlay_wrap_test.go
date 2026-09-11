package app

import (
	"strings"
	"testing"

	"raj/internal/lsp"
)

// The fallback keeps a hint whenever its whole line, hints included, still
// fits one visual row, so a wrap toggle no longer drops an installed hint.
func TestWrapKeepsFittingHints(t *testing.T) {
	h := newHarness(t, "abc\ndef\n")
	if !h.Pane().Wrap {
		t.Fatal("setup: wrapping is on by default")
	}
	h.Draw() // the pane learns its text width from the frame
	h.installInlay(lsp.InlayHint{Pos: lsp.Position{Line: 1, Character: 0}, Text: "x"})
	if h.Pane().File.HintsAt(1) == nil {
		t.Fatal("setup: hints were not installed")
	}

	// Toggling wrap off and back on keeps the hint both times.
	h.press("ctrl+alt+w")
	if h.Pane().Wrap {
		t.Fatal("ctrl+alt+w did not turn wrapping off")
	}
	if h.Pane().File.HintsAt(1) == nil {
		t.Error("unwrapping dropped a hint that fits one row")
	}
	h.press("ctrl+alt+w")
	if !h.Pane().Wrap {
		t.Fatal("ctrl+alt+w did not turn wrapping back on")
	}
	if h.Pane().File.HintsAt(1) == nil {
		t.Error("re-wrapping dropped a hint that fits one row")
	}
}

// A hint on one line is dropped when that line, hints included, is wider than
// the pane, while a hint on another line that fits is kept — and the dropped
// line keeps the un-hinted column maths.
func TestTooWideHintIsNotInstalled(t *testing.T) {
	h := newHarness(t, "abc\ndef\n")
	h.Draw() // the fit test needs a laid-out width
	h.installInlay(
		lsp.InlayHint{Pos: lsp.Position{Line: 0, Character: 1}, Text: "x"},
		lsp.InlayHint{Pos: lsp.Position{Line: 1, Character: 0}, Text: strings.Repeat("W", 300)},
	)
	if got := h.Pane().File.HintsAt(0); got == nil {
		t.Error("a hint that fits was dropped with a too-wide one")
	}
	if got := h.Pane().File.HintsAt(1); got != nil {
		t.Errorf("a hint wider than the pane was installed: %v", got)
	}
	if line, col := h.Pane().File.LineCol(5); line != 1 || col != 1 {
		t.Errorf("line 1 offset 5 = %d:%d, want the un-hinted 1:1", line, col)
	}
	if got := h.Pane().File.OffsetAt(1, 1); got != 5 {
		t.Errorf("offset at 1:1 = %d, want 5", got)
	}
}

// A width change refilters the installed hints from the store, so a hint that
// was too wide for the editor beside the sidebar appears once the sidebar
// closes and the editor takes the whole terminal.
func TestSidebarWidthChangeRefiltersHints(t *testing.T) {
	h := newHarness(t, "abc\ndef\n")
	h.Draw()
	// Wider than the editor-with-sidebar text width, narrower than the full
	// terminal, so the width change is what decides it.
	h.installInlay(lsp.InlayHint{
		Pos:  lsp.Position{Line: 0, Character: 1},
		Text: strings.Repeat("W", 90),
	})
	if got := h.Pane().File.HintsAt(0); got != nil {
		t.Fatalf("setup: hint installed at the narrow width: %v", got)
	}
	// The explorer chord toggles: the first press focuses it, the second
	// closes it and gives the editor the whole terminal.
	h.press("shift+super+e", "shift+super+e")
	if h.SidebarMode() != SidebarNone {
		t.Fatalf("sidebar is %v, want it closed", h.SidebarMode())
	}
	if got := h.Pane().File.HintsAt(0); got == nil {
		t.Error("the wider editor did not restore the hint from the store")
	}
}
