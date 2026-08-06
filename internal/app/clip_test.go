package app

import (
	"strings"
	"testing"
)

// cut and copy are global actions, claimed before any pane sees the chord. The
// bug these pin is that they therefore always acted on the document: cmd+c in
// the search box copied from the editor, and cmd+x edited the file being
// searched.

func TestCopyInTheSearchBoxTakesTheQuery(t *testing.T) {
	h := newHarness(t, "document contents here")
	h.press("shift+super+f")
	h.typeText("needle")
	h.press("super+a") // select the field, not the document
	h.press("super+c")

	if got := h.host.Clipboard(); got != "needle" {
		t.Errorf("clipboard = %q, want needle", got)
	}
}

func TestCutInTheSearchBoxLeavesTheDocumentAlone(t *testing.T) {
	h := newHarness(t, "document contents here")
	before := h.text()
	h.press("shift+super+f")
	h.typeText("needle")
	h.press("super+a")
	h.press("super+x")

	if got := h.text(); got != before {
		t.Errorf("cutting in the search box edited the document: %q", got)
	}
	if got := h.host.Clipboard(); got != "needle" {
		t.Errorf("clipboard = %q, want needle", got)
	}
}

// A field has no "current line" to fall back on, so cut with nothing selected
// must do nothing rather than empty the box.
func TestCutWithNoSelectionInAFieldIsInert(t *testing.T) {
	h := newHarness(t, "body")
	h.press("shift+super+f")
	h.typeText("keepme")
	h.press("super+x")

	if got := h.text(); got != "body" {
		t.Errorf("document changed: %q", got)
	}
	if got := h.Search.ActiveInput().Text; got != "keepme" {
		t.Errorf("the query was emptied: %q", got)
	}
}

// The find bar lives inside the editor pane, so focus alone does not
// distinguish it — it is the case most likely to be missed.
func TestCopyInTheFindBarTakesTheQuery(t *testing.T) {
	h := newHarness(t, "alpha beta gamma")
	h.press("super+f")
	h.typeText("beta")
	h.press("super+a")
	h.press("super+c")

	if got := h.host.Clipboard(); got != "beta" {
		t.Errorf("clipboard = %q, want beta", got)
	}
}

// The save-as dialog is modal and sits above whatever had focus.
func TestCopyInADialogTakesTheField(t *testing.T) {
	h := newHarness(t, "")
	h.press("super+n")
	h.typeText("scratch")
	h.press("super+s")
	h.press("super+a")
	h.press("super+c")

	if got := h.host.Clipboard(); !strings.HasSuffix(got, "/") || got == "" {
		t.Errorf("clipboard = %q, want the seeded path from the dialog", got)
	}
	if got := h.Pane().File.Text(); got != "scratch" {
		t.Errorf("the buffer was touched: %q", got)
	}
}

// With focus in the document, cut and copy must still act on the document.
func TestCopyInTheEditorStillTakesTheDocument(t *testing.T) {
	h := newHarness(t, "alpha beta")
	h.press("super+a")
	h.press("super+c")

	if got := h.host.Clipboard(); got != "alpha beta" {
		t.Errorf("clipboard = %q, want the document", got)
	}
}

// The explorer has no text field, so it must fall through to the document
// rather than silently doing nothing.
func TestCopyInTheExplorerFallsThroughToTheDocument(t *testing.T) {
	h := newHarness(t, "alpha beta")
	h.press("super+a") // select all in the editor first
	h.press("shift+super+e")
	h.press("super+c")

	if got := h.host.Clipboard(); got != "alpha beta" {
		t.Errorf("clipboard = %q, want the document", got)
	}
}
