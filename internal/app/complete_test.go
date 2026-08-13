package app

import (
	"path/filepath"
	"strings"
	"testing"
)

// cursorLineText is the text of the line the cursor is on.
func cursorLineText(h *harness) string {
	p := h.Pane()
	return p.File.Line(p.File.LineOf(p.Cursors.Primary().Head))
}

const completeSrc = `package main

func handleRequest() {}
func handshake() {}

func main() {

}
`

// End to end through the key path: typing enough of a word offers the words
// already in the buffer.
func TestCompletionOffersBufferWords(t *testing.T) {
	h := newHarness(t, completeSrc)
	h.press("ctrl+g")
	h.typeText("7")
	h.press("enter")
	h.typeText("han")

	if !h.Complete.Open {
		t.Fatal("no completion popup after typing a prefix")
	}
	if h.Complete.Count() < 2 {
		t.Errorf("offered %d candidates, want both handleRequest and handshake",
			h.Complete.Count())
	}
}

// One character is not enough to discriminate, so nothing is offered.
func TestCompletionWaitsForEnoughPrefix(t *testing.T) {
	h := newHarness(t, completeSrc)
	h.press("ctrl+g")
	h.typeText("7")
	h.press("enter")
	h.typeText("h")
	if h.Complete.Open {
		t.Error("offered candidates after one character")
	}
}

// Tab accepts, replacing the typed prefix with the whole word.
func TestCompletionAcceptsWithTab(t *testing.T) {
	h := newHarness(t, completeSrc)
	h.press("ctrl+g")
	h.typeText("7")
	h.press("enter")
	h.typeText("handl")
	if !h.Complete.Open {
		t.Fatal("no popup to accept from")
	}
	h.press("tab")

	if h.Complete.Open {
		t.Error("the popup stayed open after accepting")
	}
	if got := cursorLineText(h); got != "handleRequest" {
		t.Errorf("line = %q, want handleRequest", got)
	}
}

// Accepting is one edit, so it is one undo step.
func TestCompletionIsOneUndo(t *testing.T) {
	h := newHarness(t, completeSrc)
	h.press("ctrl+g")
	h.typeText("7")
	h.press("enter")
	h.typeText("handl")
	h.press("tab")
	h.press("super+z")

	if line := cursorLineText(h); strings.Contains(line, "handleRequest") {
		t.Errorf("one undo left %q; accepting should be a single step", line)
	}
}

// Escape dismisses without changing the buffer, which is what makes the popup
// safe to leave on screen.
func TestCompletionEscapeLeavesTheBuffer(t *testing.T) {
	h := newHarness(t, completeSrc)
	h.press("ctrl+g")
	h.typeText("7")
	h.press("enter")
	h.typeText("handl")
	before := h.Pane().File.Text()
	h.press("esc")

	if h.Complete.Open {
		t.Error("escape did not close the popup")
	}
	if got := h.Pane().File.Text(); got != before {
		t.Error("escape changed the buffer")
	}
}

// The popup must not swallow ordinary editing. Every key it does not claim has
// to reach the editor, or typing becomes unpredictable while it is showing.
func TestCompletionDoesNotSwallowTyping(t *testing.T) {
	h := newHarness(t, completeSrc)
	h.press("ctrl+g")
	h.typeText("7")
	h.press("enter")
	h.typeText("han")
	if !h.Complete.Open {
		t.Fatal("setup: expected a popup")
	}
	h.typeText("d")
	if got := cursorLineText(h); got != "hand" {
		t.Errorf("line = %q, want hand — the popup ate a keystroke", got)
	}
	h.press("backspace")
	if got := cursorLineText(h); got != "han" {
		t.Errorf("line = %q after backspace, want han", got)
	}
}

// Moving the cursor closes it: a cursor that jumped is no longer finishing the
// word it was on.
func TestCompletionClosesOnCursorMove(t *testing.T) {
	h := newHarness(t, completeSrc)
	h.press("ctrl+g")
	h.typeText("7")
	h.press("enter")
	h.typeText("han")
	if !h.Complete.Open {
		t.Fatal("setup: expected a popup")
	}
	h.press("left")
	if h.Complete.Open {
		t.Error("the popup survived a cursor move")
	}
}

// Several cursors close it. A completion is one word at one place, and applying
// it at cursors mid-word in different identifiers would replace text nobody
// looked at.
func TestCompletionClosesWithMultipleCursors(t *testing.T) {
	h := newHarness(t, "han\nhan\nhan\n")
	h.press("ctrl+g")
	h.typeText("1")
	h.press("enter")
	h.Pane().LineEnd(false)
	h.press("super+alt+down")
	h.drain()
	if len(h.Pane().Cursors.All()) < 2 {
		t.Skip("no multi-cursor chord in this build")
	}
	h.typeText("d")
	if h.Complete.Open {
		t.Error("the popup opened with several cursors")
	}
}

// The cache must not change what is suggested — it is a cost fix, and a stale
// entry would show up as a suggestion for text that is no longer in the buffer.
func TestCompletionStaysCorrectAcrossEdits(t *testing.T) {
	h := newHarness(t, completeSrc)
	h.press("ctrl+g")
	h.typeText("8")
	h.press("enter")

	// A word that is not in the buffer yet offers nothing.
	h.typeText("handbr")
	if h.Complete.Open {
		t.Fatal("suggested something for a word nothing matches")
	}
	// Finish it, so the buffer now contains it.
	h.typeText("ake")
	h.press("enter")

	// Typing the same prefix again must now find it. It cannot, if the buffer
	// is being served from a cache entry made before the edit.
	h.typeText("handbr")
	if !h.Complete.Open {
		t.Fatal("a word typed a moment ago is not suggested")
	}
	found := false
	for i := 0; i < h.Complete.Count(); i++ {
		if c, ok := h.Complete.Selected(); ok && c.Word == "handbrake" {
			found = true
			break
		}
		h.press("down")
	}
	if !found {
		t.Error("handbrake was not among the candidates")
	}
}

// Closing a tab drops its words, or a long session suggests from every file
// ever opened.
func TestCompletionForgetsClosedBuffers(t *testing.T) {
	h := newWorkspace(t, 120, 30)
	h.OpenFile(filepath.Join(h.root, "pkg/helper.go"))
	h.drain()
	h.OpenFile(filepath.Join(h.root, "main.go"))
	h.drain()

	h.Pane().DocEnd(false)
	h.typeText("\nnee")
	if !h.Complete.Open {
		t.Fatal("setup: expected candidates from the other buffer")
	}
	h.press("esc")

	// Close the other tab and retype: needle lived in helper.go.
	for _, p := range h.Tabs.All() {
		if strings.HasSuffix(p.File.Path, "helper.go") {
			h.Tabs.Focus(p)
			h.Tabs.Close()
			break
		}
	}
	h.drain()
	h.typeText("d")
	for i := 0; i < h.Complete.Count(); i++ {
		if c, ok := h.Complete.Selected(); ok && c.Detail == "helper.go" {
			t.Error("suggested a word from a closed buffer")
		}
		h.press("down")
	}
}
