package app

import (
	"testing"

	"raj/internal/lsp"
)

// snippetItem is a completion candidate carrying a snippet template, as a
// server that honoured snippetSupport would send it. Insert stays the label so
// the popup shows the same word it always did; Snippet is the template.
func snippetItem(label, template string) lsp.CompletionItem {
	return lsp.CompletionItem{
		Label:            label,
		Insert:           label,
		InsertTextFormat: 2,
		Snippet:          template,
	}
}

// Accepting a snippet inserts the template's literal text and leaves the caret
// on the first placeholder, selected. Without the engine the template's label
// would be inserted instead and no stop would exist to select.
func TestSnippetAcceptExpandsAndStartsSession(t *testing.T) {
	h := acceptServerEdit(t, "hand", snippetItem("hand", "handler(${1:req})"))
	h.press("tab")

	if got := cursorLineText(h); got != "handler(req)" {
		t.Fatalf("line = %q, want the expanded snippet", got)
	}
	if !h.snippet.active {
		t.Fatal("accepting a snippet did not start a session")
	}
	start, end := h.Pane().Cursors.Primary().Range()
	if got := h.text()[start:end]; got != "req" {
		t.Errorf("selection = %q, want the first placeholder", got)
	}
}

// Tab moves to the next stop and selects its placeholder, the one key the
// session claims from the editor.
func TestSnippetTabAdvancesStops(t *testing.T) {
	h := acceptServerEdit(t, "hand", snippetItem("hand", "${1:a}${2:b}"))
	h.press("tab")

	start, end := h.Pane().Cursors.Primary().Range()
	if got := h.text()[start:end]; got != "a" {
		t.Fatalf("first stop selection = %q, want a", got)
	}
	h.press("tab")
	start, end = h.Pane().Cursors.Primary().Range()
	if got := h.text()[start:end]; got != "b" {
		t.Errorf("second stop selection = %q, want b", got)
	}
	if !h.snippet.active {
		t.Error("the session ended before the last stop")
	}
}

// Typing over a selected placeholder replaces it, because the selection is a
// real buffer selection. The edit also ends the session, so the stops do not
// survive text that moved them.
func TestSnippetTypingReplacesSelection(t *testing.T) {
	h := acceptServerEdit(t, "hand", snippetItem("hand", "${1:a}${2:b}"))
	h.press("tab")
	h.typeText("x")

	if got := cursorLineText(h); got != "xb" {
		t.Errorf("line = %q, want the typed character to replace the selection", got)
	}
	if h.snippet.active {
		t.Error("the session survived an edit")
	}
}

// Escape ends the session and falls through to the editor's cancel, so tab
// means indent again — the session claims tab only while it is running.
func TestSnippetEscapeEndsAndTabIndents(t *testing.T) {
	h := acceptServerEdit(t, "hand", snippetItem("hand", "${1:a}${2:b}"))
	h.press("tab")
	if !h.snippet.active {
		t.Fatal("setup: no session")
	}
	h.press("esc")
	if h.snippet.active {
		t.Fatal("escape did not end the session")
	}
	before := h.text()
	h.press("tab")
	if h.text() == before {
		t.Error("tab did not indent after the session ended")
	}
}

// A snippet carried by a textEdit replaces the server's range with the expanded
// text, not the raw template.
func TestSnippetTextEditExpandsTheRange(t *testing.T) {
	h := acceptServerEdit(t, "hand", lsp.CompletionItem{
		Label:            "handler",
		InsertTextFormat: 2,
		Snippet:          "handler(${1:req})",
		Edit: &lsp.TextEdit{
			Range:   lsp.Range{Start: lsp.Position{Line: 1, Character: 0}, End: lsp.Position{Line: 1, Character: 4}},
			NewText: "handler(${1:req})",
		},
	})
	h.press("tab")

	if got := cursorLineText(h); got != "handler(req)" {
		t.Fatalf("line = %q, want the expanded snippet", got)
	}
	start, end := h.Pane().Cursors.Primary().Range()
	if got := h.text()[start:end]; got != "req" {
		t.Errorf("selection = %q, want the placeholder", got)
	}
}

// An additionalTextEdit before the completion's range — an import line — moves
// the inserted text right; the stops must move with it or the selection lands
// on the wrong bytes.
func TestSnippetStopsFollowAnAdditionalEdit(t *testing.T) {
	h := acceptServerEdit(t, "hand", lsp.CompletionItem{
		Label:            "handler",
		InsertTextFormat: 2,
		Snippet:          "handler(${1:req})",
		Edit: &lsp.TextEdit{
			Range:   lsp.Range{Start: lsp.Position{Line: 1, Character: 0}, End: lsp.Position{Line: 1, Character: 4}},
			NewText: "handler(${1:req})",
		},
		Additional: []lsp.TextEdit{{
			Range:   lsp.Range{Start: lsp.Position{Line: 0, Character: 0}, End: lsp.Position{Line: 0, Character: 0}},
			NewText: "// x\n",
		}},
	})
	h.press("tab")

	start, end := h.Pane().Cursors.Primary().Range()
	if got := h.text()[start:end]; got != "req" {
		t.Errorf("selection = %q, want the placeholder after the shifted import", got)
	}
}

// A plain completion is untouched: same insert, same one undo, and no session
// to arbitrate tab against indent.
func TestPlainCompletionDoesNotStartASession(t *testing.T) {
	h := acceptServerEdit(t, "hand", lsp.CompletionItem{Label: "handleRequest", Insert: "handleRequest"})
	h.press("tab")

	if h.snippet.active {
		t.Error("a plain completion started a snippet session")
	}
	if got := cursorLineText(h); got != "handleRequest" {
		t.Errorf("line = %q, want the plain word", got)
	}
}

// Shift+tab steps back through the stops and stops at the first one rather than
// wrapping, so a mistaken key cannot leave the snippet.
func TestSnippetShiftTabStepsBack(t *testing.T) {
	h := acceptServerEdit(t, "hand", snippetItem("hand", "${1:a}${2:b}"))
	h.press("tab")
	h.press("tab")
	h.press("shift+tab")

	start, end := h.Pane().Cursors.Primary().Range()
	if got := h.text()[start:end]; got != "a" {
		t.Errorf("selection after shift+tab = %q, want the first stop", got)
	}
}

// Expanding a snippet is one edit, so it reverses in one undo — the same cost
// as accepting a plain word. Without the single change group, the delete of the
// prefix and the insert of the expansion would be two undo steps.
func TestSnippetAcceptIsOneUndo(t *testing.T) {
	h := acceptServerEdit(t, "hand", snippetItem("hand", "handler(${1:req})"))
	h.press("tab")
	if !h.snippet.active {
		t.Fatal("setup: no session")
	}
	h.press("super+z")
	if got := cursorLineText(h); got != "hand" {
		t.Errorf("line after one undo = %q, want the typed prefix back", got)
	}
}
