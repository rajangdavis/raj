package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"raj/internal/editor"
	"raj/internal/lsp"
	"raj/internal/piecetable"
)

// renameRangeOf locates sub in text and returns the LSP range covering it, so
// the edits a test feeds the app are the ones a server would send rather than
// hand-counted columns.
func renameRangeOf(t *testing.T, text, sub string) lsp.Range {
	t.Helper()
	i := strings.Index(text, sub)
	if i < 0 {
		t.Fatalf("fixture has no %q to rename", sub)
	}
	doc := lsp.NewDocument(text)
	return lsp.Range{Start: doc.Position(i), End: doc.Position(i + len(sub))}
}

// renamePaneAt finds the open or announced pane for a path.
func renamePaneAt(t *testing.T, h *harness, path string) *editor.Pane {
	t.Helper()
	for _, p := range h.Tabs.All() {
		if sameFile(path, p.File.Path) {
			return p
		}
	}
	t.Fatalf("no pane for %s", path)
	return nil
}

// A server that never advertised renameProvider is refused in the same voice
// as the other capability gates, before a request the server would answer
// method-not-found and before a name is collected.
func TestRenameCapabilityGate(t *testing.T) {
	const want = "language server does not support rename"
	if got := capabilityGap(nil, "rename"); got != want {
		t.Errorf("absent provider: gap = %q", got)
	}
	if got := capabilityGap(json.RawMessage(`false`), "rename"); got != want {
		t.Errorf("false provider: gap = %q", got)
	}
	if got := capabilityGap(json.RawMessage(`{"prepareProvider":true}`), "rename"); got != "" {
		t.Errorf("advertised provider: gap = %q, want none", got)
	}
}

// The chord resolves, the feature runs, and with no server for the file type
// it says so without changing anything. A .txt has no configured server, so
// this exercises the no-server path without spawning one on a machine that
// happens to have gopls installed.
func TestRenameWithoutAServerIsHarmless(t *testing.T) {
	h := newHarness(t, "package main\n")
	path := filepath.Join(h.root, "notes.txt")
	if err := os.WriteFile(path, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.OpenFile(path)
	before := h.Pane().File.Text()
	h.press("ctrl+super+r")
	if h.Pane().File.Text() != before {
		t.Error("a rename with no server changed the buffer")
	}
	if h.Prompt.Open {
		t.Error("a rename with no server opened the name prompt")
	}
	if !strings.Contains(h.Status(), "no language server") {
		t.Errorf("status = %q, want the no-server message", h.Status())
	}
}

// A refused prepare is spoken before a name is asked for. The regression this
// guards is the old shape: collect a name, send the rename, and report the
// failure after the user has typed one.
func TestRenamePrepareRefusalIsSpoken(t *testing.T) {
	h := newHarness(t, "package main\n")
	h.lspGen = 1
	h.park(lspAnswer{gen: 1, kind: answerPrepareRename})
	h.applyAnswer()
	if h.Prompt.Open {
		t.Fatal("a refused prepare opened the name prompt")
	}
	if !strings.Contains(h.Status(), "nothing to rename") {
		t.Errorf("status = %q, want the refusal", h.Status())
	}
	// A server's own refusal message is more specific than the client's and is
	// passed through rather than replaced.
	h.lspGen = 2
	h.park(lspAnswer{gen: 2, kind: answerPrepareRename, text: "cannot rename this element"})
	h.applyAnswer()
	if h.Status() != "cannot rename this element" {
		t.Errorf("status = %q, want the server message", h.Status())
	}
}

// The new name comes from the shared prompt, seeded with the server's
// suggested name and selected so typing replaces it. There is no second text
// widget, and the seed is the current name rather than a blank field.
func TestRenameCollectsANameThroughThePrompt(t *testing.T) {
	h := newHarness(t, "package main\n\nfunc Old() {}\n")
	h.lspGen = 1
	h.park(lspAnswer{gen: 1, kind: answerPrepareRename, target: &lsp.RenameTarget{
		Range:       lsp.Range{Start: lsp.Position{Line: 2, Character: 5}, End: lsp.Position{Line: 2, Character: 8}},
		Placeholder: "Old",
	}})
	h.applyAnswer()
	if !h.Prompt.Open {
		t.Fatalf("no rename prompt; status = %q", h.Status())
	}
	if h.Prompt.Title() != "Rename" {
		t.Errorf("title = %q, want Rename", h.Prompt.Title())
	}
	if h.Prompt.Text() != "Old" {
		t.Errorf("seed = %q, want the server placeholder", h.Prompt.Text())
	}
	h.typeText("Renamed")
	if h.Prompt.Text() != "Renamed" {
		t.Errorf("after typing = %q, want the typed name", h.Prompt.Text())
	}
	before := h.Pane().File.Text()
	h.press("esc")
	if h.Prompt.Open {
		t.Error("escape left the prompt open")
	}
	if h.Pane().File.Text() != before {
		t.Error("cancelling the prompt changed the buffer")
	}
	if !strings.Contains(h.Status(), "cancelled") {
		t.Errorf("status = %q, want a cancellation", h.Status())
	}
}

// A WorkspaceEdit touches several documents by URI; every document's edits must
// land in that document. A flattened application would put one file's ranges
// into another file and corrupt both, so this pins the grouping and the
// replacement of each file's identifier.
func TestRenameAppliesWorkspaceEditAcrossOpenBuffers(t *testing.T) {
	h := newWorkspace(t, 120, 30)
	main := filepath.Join(h.root, "main.go")
	helper := filepath.Join(h.root, "pkg", "helper.go")
	h.OpenFile(main)
	h.OpenFile(helper)
	mainText, _ := os.ReadFile(main)
	helperText, _ := os.ReadFile(helper)

	h.lspGen = 1
	h.park(lspAnswer{gen: 1, kind: answerRename, text: "pin", edit: &lsp.WorkspaceEdit{
		Docs: []lsp.DocumentEdits{
			{Path: main, Edits: []lsp.TextEdit{{Range: renameRangeOf(t, string(mainText), "needle"), NewText: "pin"}}},
			{Path: helper, Edits: []lsp.TextEdit{{Range: renameRangeOf(t, string(helperText), "needle"), NewText: "pin"}}},
		},
	}})
	h.applyAnswer()

	mp := renamePaneAt(t, h, main)
	hp := renamePaneAt(t, h, helper)
	if !strings.Contains(mp.File.Text(), "pin()") || strings.Contains(mp.File.Text(), "needle") {
		t.Errorf("main = %q, want needle renamed to pin", mp.File.Text())
	}
	if !strings.Contains(hp.File.Text(), "func pin()") || strings.Contains(hp.File.Text(), "needle") {
		t.Errorf("helper = %q, want needle renamed to pin", hp.File.Text())
	}
	if !strings.Contains(h.Status(), "save") {
		t.Errorf("status = %q, want the save reminder", h.Status())
	}
}

// An unopened file is a target too. It is loaded, edited and announced as a
// tab; the edit is all-or-nothing, so the file the user never opened still got
// the rename rather than being skipped silently.
func TestRenameAppliesToAnUnopenedFileAsAWhole(t *testing.T) {
	h := newWorkspace(t, 120, 30)
	main := filepath.Join(h.root, "main.go")
	helper := filepath.Join(h.root, "pkg", "helper.go")
	h.OpenFile(main) // helper stays unopened
	mainText, _ := os.ReadFile(main)
	helperText, _ := os.ReadFile(helper)

	h.lspGen = 1
	h.park(lspAnswer{gen: 1, kind: answerRename, text: "pin", edit: &lsp.WorkspaceEdit{
		Docs: []lsp.DocumentEdits{
			{Path: main, Edits: []lsp.TextEdit{{Range: renameRangeOf(t, string(mainText), "needle"), NewText: "pin"}}},
			{Path: helper, Edits: []lsp.TextEdit{{Range: renameRangeOf(t, string(helperText), "needle"), NewText: "pin"}}},
		},
	}})
	h.applyAnswer()

	mp := renamePaneAt(t, h, main)
	if !strings.Contains(mp.File.Text(), "pin()") || strings.Contains(mp.File.Text(), "needle") {
		t.Errorf("main = %q", mp.File.Text())
	}
	hp := renamePaneAt(t, h, helper)
	if !strings.Contains(hp.File.Text(), "func pin()") {
		t.Errorf("unopened helper = %q, want the rename applied", hp.File.Text())
	}
	if !hp.File.ViewDirty() {
		t.Error("the edited unopened file was not left dirty and unsaved")
	}
	if h.Tabs.Count() != 2 {
		t.Errorf("tabs = %d, want the touched file revealed as a tab", h.Tabs.Count())
	}
	if h.Tabs.Active() != mp {
		t.Error("revealing the touched file pulled the view off the one being read")
	}
}

// A target that cannot be loaded refuses the whole rename, naming the file.
// This is the all-or-nothing rule: the document that was already open must be
// untouched, because a half-renamed symbol is worse than an unrenamed one.
func TestRenameRefusesWholeWhenATargetCannotBeLoaded(t *testing.T) {
	h := newWorkspace(t, 120, 30)
	main := filepath.Join(h.root, "main.go")
	h.OpenFile(main)
	mainText, _ := os.ReadFile(main)
	before := h.Pane().File.Text()

	missing := filepath.Join(h.root, "pkg", "missing.go")
	h.lspGen = 1
	h.park(lspAnswer{gen: 1, kind: answerRename, text: "pin", edit: &lsp.WorkspaceEdit{
		Docs: []lsp.DocumentEdits{
			{Path: main, Edits: []lsp.TextEdit{{Range: renameRangeOf(t, string(mainText), "needle"), NewText: "pin"}}},
			{Path: missing, Edits: []lsp.TextEdit{{Range: lsp.Range{Start: lsp.Position{Line: 0, Character: 0}, End: lsp.Position{Line: 0, Character: 1}}, NewText: "pin"}}},
		},
	}})
	h.applyAnswer()

	if h.Pane().File.Text() != before {
		t.Errorf("the open file changed despite an unloadable target: %q", h.Pane().File.Text())
	}
	if !strings.Contains(h.Status(), "missing.go") {
		t.Errorf("status = %q, want it to name the unloadable file", h.Status())
	}
	if h.Tabs.Count() != 1 {
		t.Errorf("tabs = %d, want no tab for a refused rename", h.Tabs.Count())
	}
}

// File operations are not text edits. A rename answer that asks to create,
// rename or delete a file cannot be applied through the text path, and applying
// the text while dropping the operation would leave the workspace
// half-changed, so the whole answer is refused.
func TestRenameRefusesFileOperations(t *testing.T) {
	h := newHarness(t, "package main\n")
	before := h.Pane().File.Text()
	h.lspGen = 1
	h.park(lspAnswer{gen: 1, kind: answerRename, text: "New", edit: &lsp.WorkspaceEdit{
		ResourceOps: []string{"rename"},
	}})
	h.applyAnswer()
	if h.Pane().File.Text() != before {
		t.Error("a file-operation answer changed a buffer")
	}
	if !strings.Contains(h.Status(), "file operations") {
		t.Errorf("status = %q, want the refusal", h.Status())
	}
}

// An answer computed against text the buffer has since left is refused whole.
// Without the version guard the edits land at stale offsets, which is the
// silent partial-rename failure the rule exists to prevent.
func TestRenameRefusesAnAnswerForAChangedBuffer(t *testing.T) {
	h := newHarness(t, "package main\n\nfunc Old() {}\n")
	path := h.Pane().File.Path
	text := h.Pane().File.Text()
	before := text

	h.lspGen = 1
	h.park(lspAnswer{
		gen: 1, kind: answerRename, text: "New",
		versions: map[string]int{path: int(h.Pane().File.Session().Version()) + 1},
		edit: &lsp.WorkspaceEdit{Docs: []lsp.DocumentEdits{
			{Path: path, Edits: []lsp.TextEdit{{Range: renameRangeOf(t, text, "Old"), NewText: "New"}}},
		}},
	})
	h.applyAnswer()

	if h.Pane().File.Text() != before {
		t.Errorf("a stale rename was applied: %q", h.Pane().File.Text())
	}
	if !strings.Contains(h.Status(), "changed while") {
		t.Errorf("status = %q, want the staleness refusal", h.Status())
	}
}

// A rename answer with no edits is "nothing to do", not a failure and not a
// prompt.
func TestRenameWithNoEditsIsHarmless(t *testing.T) {
	h := newHarness(t, "package main\n")
	before := h.Pane().File.Text()
	h.lspGen = 1
	h.park(lspAnswer{gen: 1, kind: answerRename})
	h.applyAnswer()
	if h.Pane().File.Text() != before {
		t.Error("a null workspace edit changed the buffer")
	}
	if !strings.Contains(h.Status(), "no changes") {
		t.Errorf("status = %q, want the no-op message", h.Status())
	}
}

// The word helper used when the server advertises no prepare half covers the
// whole identifier, not just the half before the caret. PrefixAt stops at the
// caret by design, so reusing it here would rename only the prefix.
func TestRenameWordCoversTheWholeIdentifier(t *testing.T) {
	text := "call + handleRequest(x)"
	lo, hi := renameWord(text, 10)
	if got := text[lo:hi]; got != "handleRequest" {
		t.Errorf("word = %q, want handleRequest", got)
	}
	if lo, hi := renameWord(text, 6); lo != hi {
		t.Errorf("a caret on punctuation selected %q", text[lo:hi])
	}
}

// A rename leaves the caret where the user was looking, not at whichever edit
// happened to be applied last. The caret is mapped through the edits in the
// document's original coordinates, so it must be read before the batch lands:
// applyServerEdits moves the cursor as it replaces each span.
func TestRenameKeepsTheCaretNearWhereItWas(t *testing.T) {
	h := newHarness(t, "hello world\n")
	text := h.text()
	path := h.Pane().File.Path
	h.Pane().Cursors.Set(8, 8) // inside "world"

	h.lspGen = 1
	h.park(lspAnswer{gen: 1, kind: answerRename, text: "pin", edit: &lsp.WorkspaceEdit{
		Docs: []lsp.DocumentEdits{
			{Path: path, Edits: []lsp.TextEdit{
				{Range: renameRangeOf(t, text, "hello"), NewText: "hi"},
				{Range: renameRangeOf(t, text, "world"), NewText: "socket"},
			}},
		},
	}})
	h.applyAnswer()

	if got := h.text(); got != "hi socket\n" {
		t.Fatalf("buffer = %q, want both edits applied", got)
	}
	// "world" became "socket"; the caret was inside the replaced span, so it
	// lands at the end of the replacement, before the newline. Reading it after
	// the batch instead leaves it at 2, the end of "hi".
	if got := h.Pane().Cursors.Primary().Head; got != 9 {
		t.Errorf("caret = %d, want 9 (mapped through the edits)", got)
	}
}

// A rename whose span overlaps a pending change set changes no text at all and
// names the set. A rename is one edit even though it spans a document's spans,
// and which of the two texts is right is the user's decision: applying the
// spans that do not overlap would leave the symbol half renamed with no note of
// which half landed. Without the batch lease check the non-overlapping span
// lands and the buffer changes.
func TestRenameRefusesOverAProposedSpan(t *testing.T) {
	h := newHarness(t, "hello world\n")
	id := propose(t, h, piecetable.Hunk{Start: 0, End: 5, Text: "HELLO"})
	text := h.text()
	if text != "HELLO world\n" {
		t.Fatalf("setup text = %q, want the proposal applied", text)
	}
	before := text

	path := h.Pane().File.Path
	h.lspGen = 1
	h.park(lspAnswer{gen: 1, kind: answerRename, text: "pin", edit: &lsp.WorkspaceEdit{
		Docs: []lsp.DocumentEdits{
			{Path: path, Edits: []lsp.TextEdit{
				{Range: renameRangeOf(t, text, "HELLO"), NewText: "hi"},
				{Range: renameRangeOf(t, text, "world"), NewText: "socket"},
			}},
		},
	}})
	h.applyAnswer()

	if got := h.text(); got != before {
		t.Errorf("a rename overlapping a proposed span changed the buffer: %q", got)
	}
	if !strings.Contains(h.Status(), "change set") ||
		!strings.Contains(h.Status(), fmt.Sprintf("change set %d", id)) {
		t.Errorf("status = %q, want the lease refusal naming set %d", h.Status(), id)
	}
}

// A rename with no leases still applies to every span and is one undo step:
// the whole batch is one user action, so a single undo reverses it. Without the
// shared applier each span would be its own undo step.
func TestRenameWithoutLeasesIsOneUndo(t *testing.T) {
	h := newHarness(t, "hello world\n")
	before := h.text()
	text := before

	path := h.Pane().File.Path
	h.lspGen = 1
	h.park(lspAnswer{gen: 1, kind: answerRename, text: "pin", edit: &lsp.WorkspaceEdit{
		Docs: []lsp.DocumentEdits{
			{Path: path, Edits: []lsp.TextEdit{
				{Range: renameRangeOf(t, text, "hello"), NewText: "hi"},
				{Range: renameRangeOf(t, text, "world"), NewText: "socket"},
			}},
		},
	}})
	h.applyAnswer()

	if got := h.text(); got != "hi socket\n" {
		t.Fatalf("buffer = %q, want both edits applied", got)
	}
	h.press("super+z")
	if got := h.text(); got != before {
		t.Errorf("one undo left %q, want %q", got, before)
	}
}
