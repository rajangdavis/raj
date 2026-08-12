package app

import (
	"strings"
	"testing"

	"raj/internal/ui"
)

// End to end through the real key path: the conveniences are on by default, so
// they have to work from a keystroke rather than only from a direct call.
func TestTypingBracketsInTheEditor(t *testing.T) {
	h := newHarness(t, "")
	h.typeText("func f(")
	if got, want := h.Pane().File.Text(), "func f()"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestNewlineIndentsThroughTheKeyPath(t *testing.T) {
	h := newHarness(t, "        deep")
	h.press("ctrl+g")
	h.typeText("1")
	h.press("enter")
	h.Pane().LineEnd(false)
	h.press("enter")
	h.typeText("x")

	if got, want := h.Pane().File.Text(), "        deep\n        x"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// A paste arriving as a ui.Paste event is data and must not be paired, which
// is the distinction that keeps the feature safe.
func TestPastedBracketsAreUntouched(t *testing.T) {
	h := newHarness(t, "")
	h.Handle(ui.Paste{Text: "if (x) { return [1] }"})
	h.drain()
	if got, want := h.Pane().File.Text(), "if (x) { return [1] }"; got != want {
		t.Errorf("paste was altered: got %q, want %q", got, want)
	}
}

// Typing a whole line of code comes out as written, with no doubled closers
// and nothing swallowed.
func TestTypingALineOfCode(t *testing.T) {
	h := newHarness(t, "")
	h.typeText(`x := map[string]int{"a": 1}`)
	if got, want := h.Pane().File.Text(), `x := map[string]int{"a": 1}`; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if strings.Count(h.Pane().File.Text(), "}") != 1 {
		t.Errorf("doubled a closer: %q", h.Pane().File.Text())
	}
}
