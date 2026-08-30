package app

import (
	"testing"

	"raj/internal/lsp"
)

// ---------- isIncomplete ----------

// A complete list is everything that could go at that point, so a longer prefix
// can only select a subset of it. Filtering locally gives the same answer as
// asking again, and the flag is what makes that safe to assume.
func TestCompleteListIsFilteredRatherThanRefetched(t *testing.T) {
	h := newHarness(t, "handoff()\n\n")
	h.press("ctrl+g")
	h.typeText("2")
	h.press("enter")
	h.typeText("hand")

	parkAnswer(h, "hand", []lsp.CompletionItem{
		{Label: "handleRequest", Insert: "handleRequest"},
		{Label: "handoverFile", Insert: "handoverFile"},
	})
	if c, _ := h.Complete.Selected(); c.Word != "handleRequest" {
		t.Fatalf("setup: selected %q", c.Word)
	}
	gen := h.completeGen

	// Typing on narrows the prefix. The cached list covers it, so no new
	// request is made — which the generation counter records, since every
	// request bumps it.
	h.typeText("l")
	if h.completeGen != gen {
		t.Errorf("a request was sent for a prefix the cached list already covered")
	}
	c, ok := h.Complete.Selected()
	if !ok || c.Word != "handleRequest" {
		t.Errorf("selected %q, want the surviving item from the cached list", c.Word)
	}
	if n := h.Complete.Count(); n != 1 {
		t.Errorf("%d candidates, want only the one still matching", n)
	}
}

// An incomplete list is the server saying it truncated the answer. Caching it
// would freeze the first answer's arbitrary cut, so a large package would show
// a handful of results that never improve however much more is typed.
func TestIncompleteListIsRefetched(t *testing.T) {
	h := newHarness(t, "handoff()\n\n")
	h.press("ctrl+g")
	h.typeText("2")
	h.press("enter")
	h.typeText("hand")

	line, col := h.Complete.Anchor()
	h.parkCompletion(lspAnswer{
		gen: h.completeGen, kind: answerCompletion, prefix: "hand",
		items:      []lsp.CompletionItem{{Label: "handleRequest", Insert: "handleRequest"}},
		incomplete: true, line: line, col: col,
	})
	h.applyAnswer()
	if c, _ := h.Complete.Selected(); c.Word != "handleRequest" {
		t.Fatalf("setup: selected %q", c.Word)
	}

	if h.cached.items != nil {
		t.Error("an incomplete list was cached")
	}
}

// A cached list belongs to one word at one place. The same prefix typed
// somewhere else is a different question, and answering it from the cache would
// suggest completions computed against another scope entirely.
func TestCachedListDoesNotFollowTheCursor(t *testing.T) {
	h := newHarness(t, "handoff()\n\n\n")
	h.press("ctrl+g")
	h.typeText("2")
	h.press("enter")
	h.typeText("hand")
	parkAnswer(h, "hand", []lsp.CompletionItem{
		{Label: "handleRequest", Insert: "handleRequest"},
	})
	if !h.cached.covers("hand", h.cached.line, h.cached.col) {
		t.Fatal("setup: nothing was cached")
	}

	// A different line, same text.
	if h.cached.covers("hand", h.cached.line+1, h.cached.col) {
		t.Error("the cached list answered for a different line")
	}
	// The same line, a word starting one column over.
	if h.cached.covers("hand", h.cached.line, h.cached.col+1) {
		t.Error("the cached list answered for a different word on the line")
	}
	// A prefix that is not an extension: backspacing past what was asked for
	// means the list may be missing items that were filtered out of it.
	if h.cached.covers("han", h.cached.line, h.cached.col) {
		t.Error("the cached list answered for a shorter prefix")
	}
}

// Moving the cursor away drops the cached list along with the popup. Keeping it
// would let the next word be answered from the previous word's list.
func TestCachedListIsDroppedWhenThePopupCloses(t *testing.T) {
	h := newHarness(t, "handoff()\n\n")
	h.press("ctrl+g")
	h.typeText("2")
	h.press("enter")
	h.typeText("hand")
	parkAnswer(h, "hand", []lsp.CompletionItem{
		{Label: "handleRequest", Insert: "handleRequest"},
	})
	if h.cached.items == nil {
		t.Fatal("setup: nothing was cached")
	}

	// A move the popup does not claim: it navigates with up and down, so those
	// would move the selection rather than leave the word.
	h.press("super+left") // line start
	if h.Complete.Open {
		t.Fatal("the popup survived a cursor move")
	}
	if h.cached.items != nil {
		t.Error("the cached list outlived the popup it belonged to")
	}
}

// ---------- the trigger key ----------

// Completion appears on its own after MinPrefix characters. ctrl+space asks for
// it deliberately, which is the only way to get it with a shorter prefix.
func TestTriggerKeySummonsWithAShortPrefix(t *testing.T) {
	h := newHarness(t, "handoff()\n\n")
	h.press("ctrl+g")
	h.typeText("2")
	h.press("enter")
	h.typeText("h") // one character: below MinPrefix
	if h.Complete.Open {
		t.Fatal("setup: the popup opened on its own with one character")
	}

	h.press("ctrl+space")
	if !h.Complete.Open {
		t.Fatalf("ctrl+space did not summon the popup; status = %q", h.Status())
	}
	if c, _ := h.Complete.Selected(); c.Word != "handoff" {
		t.Errorf("selected %q, want the buffer word", c.Word)
	}
}

// Pressing it again re-asks rather than redisplaying what is already up. "Ask
// again" is the only thing a second press could reasonably mean.
func TestTriggerKeyReasksWhenAlreadyOpen(t *testing.T) {
	h := newHarness(t, "handoff()\n\n")
	h.press("ctrl+g")
	h.typeText("2")
	h.press("enter")
	h.typeText("hand")
	parkAnswer(h, "hand", []lsp.CompletionItem{
		{Label: "handleRequest", Insert: "handleRequest"},
	})
	if h.cached.items == nil {
		t.Fatal("setup: nothing was cached")
	}

	h.press("ctrl+space")
	if h.cached.items != nil {
		t.Error("a second press answered from the cache instead of re-asking")
	}
}

// The chord must not type a space. It is decoded as ctrl+space and would
// otherwise fall through to the buffer as literal text.
func TestTriggerKeyDoesNotType(t *testing.T) {
	h := newHarness(t, "word\n")
	before := h.text()
	h.press("ctrl+space")
	if got := h.text(); got != before {
		t.Errorf("buffer = %q, want %q; the chord typed something", got, before)
	}
}

// Somewhere with no word and no candidates says so rather than doing nothing
// visible. An explicit ask deserves an explicit answer.
func TestTriggerKeyWithNothingToOfferSaysSo(t *testing.T) {
	h := newHarness(t, "")
	h.press("ctrl+space")
	if h.Complete.Open {
		t.Fatal("a popup opened over an empty buffer")
	}
	if h.Status() == "" {
		t.Error("nothing happened and nothing said why")
	}
}

// The threshold moved out of the ranker and into the caller, so it needs a test
// where it now lives: the popup must still not appear on its own after one
// character.
func TestPopupStillWaitsForTwoCharacters(t *testing.T) {
	h := newHarness(t, "handoff()\n\n")
	h.press("ctrl+g")
	h.typeText("2")
	h.press("enter")

	h.typeText("h")
	if h.Complete.Open {
		t.Error("the popup appeared on its own after one character")
	}
	h.typeText("a")
	if !h.Complete.Open {
		t.Error("the popup did not appear after two")
	}
}

// ---------- the hover panel ----------

// A panel describes the thing the cursor was on, so the moment the cursor moves
// it is describing something that is no longer there. A stale box floating over
// the code is worse than the status line it replaced, because it is bigger and
// looks more authoritative.
func TestHoverPanelClosesOnAnyAction(t *testing.T) {
	for _, chord := range []string{"down", "right", "super+left", "backspace"} {
		h := newHarness(t, "package main\n\nfunc F() {}\n")
		h.lspGen = 1
		h.park(lspAnswer{gen: 1, kind: answerHover, text: "func F()"})
		h.applyAnswer()
		if !h.Hover.Open {
			t.Fatal("setup: no panel opened")
		}

		h.press(chord)
		if h.Hover.Open {
			t.Errorf("%s left the panel open", chord)
		}
	}
}

// Typing closes it too: the text under the cursor has changed, so the answer
// describes a version of the line that no longer exists.
func TestHoverPanelClosesOnTyping(t *testing.T) {
	h := newHarness(t, "package main\n")
	h.lspGen = 1
	h.park(lspAnswer{gen: 1, kind: answerHover, text: "func F()"})
	h.applyAnswer()
	if !h.Hover.Open {
		t.Fatal("setup: no panel opened")
	}

	h.typeText("x")
	if h.Hover.Open {
		t.Error("typing left the panel open")
	}
}

// Escape closes the panel before anything else sees it, so the first escape
// dismisses the box rather than a selection underneath it.
func TestEscapeClosesTheHoverPanelFirst(t *testing.T) {
	h := newHarness(t, "package main\n")
	h.press("shift+right") // make a selection to compete with
	h.lspGen = 1
	h.park(lspAnswer{gen: 1, kind: answerHover, text: "func F()"})
	h.applyAnswer()
	if !h.Hover.Open {
		t.Fatal("setup: no panel opened")
	}

	h.press("esc")
	if h.Hover.Open {
		t.Error("escape did not close the panel")
	}
	if !h.Pane().Cursors.Primary().HasSelection() {
		t.Error("escape also cleared the selection; the panel should have claimed it")
	}
}
