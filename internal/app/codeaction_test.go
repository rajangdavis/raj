package app

import (
	"encoding/json"
	"strings"
	"testing"

	"raj/internal/lsp"
)

// A code-action answer lists in the picker, and choosing a row applies the
// edit the action carries. This is the whole human path after the wire: without
// the picker mode and the choose route the answer would decode, list nothing,
// and no edit would ever land.
func TestChooseCodeActionAppliesItsEdit(t *testing.T) {
	h := newHarness(t, "package main\n")
	path := h.Pane().File.Path
	h.lspGen = 1
	h.park(lspAnswer{gen: 1, kind: answerCodeAction, actions: []lsp.CodeAction{{
		Title: "Add the fix",
		Edit: &lsp.WorkspaceEdit{Docs: []lsp.DocumentEdits{{
			Path: path,
			Edits: []lsp.TextEdit{{
				Range:   lsp.Range{Start: lsp.Position{Line: 0, Character: 0}, End: lsp.Position{Line: 0, Character: 0}},
				NewText: "// fixed\n",
			}},
		}}},
	}}})
	h.applyAnswer()
	if !h.Picker.Open || h.Focused() != FocusPicker {
		t.Fatal("the code-action list did not open")
	}
	h.press("enter")
	if got := h.text(); got != "// fixed\npackage main\n" {
		t.Errorf("buffer = %q, want the edit applied", got)
	}
	if h.Picker.Open {
		t.Error("choosing should close the picker")
	}
}

// A command action is sent to the server, not applied as text. With no live
// server in the harness the choice must still do something and say so rather
// than silently return; the wire form of the command is pinned in the lsp
// package's ExecuteCommand test.
func TestChooseCommandCodeActionWithoutAServerIsHarmless(t *testing.T) {
	h := newHarness(t, "package main\n")
	h.lspGen = 1
	h.park(lspAnswer{gen: 1, kind: answerCodeAction, actions: []lsp.CodeAction{{
		Title:   "Run it",
		Command: &lsp.Command{Title: "Run it", Command: "raj.test"},
	}}})
	h.applyAnswer()
	h.press("enter")
	if got := h.text(); got != "package main\n" {
		t.Errorf("buffer = %q; a command action must not edit text", got)
	}
	if h.Status() == "" {
		t.Error("choosing a command action said nothing")
	}
}

// A resolve-only action carries data and no edit or command. Choosing it now
// sends codeAction/resolve rather than refusing; with no live server the
// attempt still must not change the buffer and must say why, rather than
// reporting a success that applied nothing.
func TestChooseResolveOnlyCodeActionWithoutAServerIsHarmless(t *testing.T) {
	h := newHarness(t, "package main\n")
	h.Pane().File.Path = "/w/notes.txt" // no configured server: deterministic
	h.codeActionPath = h.docPath(h.Pane())
	h.lspGen = 1
	h.park(lspAnswer{gen: 1, kind: answerCodeAction, actions: []lsp.CodeAction{{
		Title: "Extract function",
		Kind:  "refactor.extract",
		Data:  json.RawMessage(`{"token":"abc"}`),
	}}})
	h.applyAnswer()
	h.press("enter")
	if got := h.text(); got != "package main\n" {
		t.Errorf("buffer = %q; a resolve-only action applied text with no server", got)
	}
	if h.Status() == "" {
		t.Error("choosing a resolve-only action with no server said nothing")
	}
}

// A codeAction/resolve answer applies the edit it returned through the same
// path a direct edit action uses. Without the resolve route the parked answer
// would be treated as a fresh list and no edit would land.
func TestResolvedCodeActionAppliesItsEdit(t *testing.T) {
	h := newHarness(t, "package main\n")
	path := h.Pane().File.Path
	h.lspGen = 1
	h.park(lspAnswer{
		gen:      1,
		kind:     answerCodeAction,
		feature:  codeActionResolveFeature,
		versions: map[string]int{path: int(h.Pane().File.Session().Version())},
		actions: []lsp.CodeAction{{
			Title: "Add the fix",
			Edit: &lsp.WorkspaceEdit{Docs: []lsp.DocumentEdits{{
				Path: path,
				Edits: []lsp.TextEdit{{
					Range:   lsp.Range{Start: lsp.Position{Line: 0, Character: 0}, End: lsp.Position{Line: 0, Character: 0}},
					NewText: "// fixed\n",
				}},
			}}},
		}},
	})
	h.applyAnswer()
	if got := h.text(); got != "// fixed\npackage main\n" {
		t.Errorf("buffer = %q, want the resolved edit applied", got)
	}
	if h.Picker.Open {
		t.Error("a resolve answer opened the picker again")
	}
}

// A resolve that answers with neither an edit nor a command is refused by name,
// not reported as success. This is the silence the refusal exists to prevent.
func TestResolvedCodeActionWithNothingUsableRefuses(t *testing.T) {
	h := newHarness(t, "package main\n")
	h.lspGen = 1
	h.park(lspAnswer{
		gen:     1,
		kind:    answerCodeAction,
		feature: codeActionResolveFeature,
		actions: []lsp.CodeAction{{Title: "Extract function", Data: json.RawMessage(`{"token":"abc"}`)}},
	})
	h.applyAnswer()
	if got := h.text(); got != "package main\n" {
		t.Errorf("buffer = %q; a resolve that completed nothing must apply nothing", got)
	}
	if !strings.Contains(h.Status(), "resolved to nothing") {
		t.Errorf("status = %q, want the resolve refusal", h.Status())
	}
}

// The resolve capability is read from codeActionProvider's resolveProvider
// option, with the shared rule: absent, false and a bare true all mean the
// server asked for no resolves, and an options object with resolveProvider true
// is support. The gate is separate from the provider itself so a server that
// offers actions without completing them is refused before the request.
func TestCodeActionResolveCapabilityGate(t *testing.T) {
	const want = "needs a codeAction/resolve step the server does not support; nothing was applied"
	if got := codeActionResolveGap(lsp.ServerCapabilities{}); got != want {
		t.Errorf("absent provider: %q", got)
	}
	if got := codeActionResolveGap(lsp.ServerCapabilities{CodeActionProvider: json.RawMessage(`true`)}); got != want {
		t.Errorf("bare true provider: %q", got)
	}
	if got := codeActionResolveGap(lsp.ServerCapabilities{CodeActionProvider: json.RawMessage(`{"resolveProvider":true}`)}); got != "" {
		t.Errorf("advertised resolve provider: %q, want none", got)
	}
}

// An edit that asks for a file operation cannot be applied through the text
// path, so the whole action is refused: applying its text edits and ignoring
// the create would half-change the workspace.
func TestChooseCodeActionRefusesResourceOps(t *testing.T) {
	h := newHarness(t, "package main\n")
	path := h.Pane().File.Path
	h.lspGen = 1
	h.park(lspAnswer{gen: 1, kind: answerCodeAction, actions: []lsp.CodeAction{{
		Title: "Split the file",
		Edit: &lsp.WorkspaceEdit{
			Docs: []lsp.DocumentEdits{{
				Path: path,
				Edits: []lsp.TextEdit{{
					Range:   lsp.Range{Start: lsp.Position{Line: 0, Character: 0}, End: lsp.Position{Line: 0, Character: 0}},
					NewText: "// moved\n",
				}},
			}},
			ResourceOps: []string{"create"},
		},
	}}})
	h.applyAnswer()
	h.press("enter")
	if got := h.text(); got != "package main\n" {
		t.Errorf("buffer = %q; a resource-op action must apply nothing", got)
	}
	if !strings.Contains(h.Status(), "file operations") {
		t.Errorf("status = %q, want a resource-op refusal", h.Status())
	}
}

// An edit computed against a version the buffer has left is refused whole
// rather than applied at stale offsets. The answer's pinned versions are the
// only signal the picker path has between the request and the choice.
func TestChooseCodeActionRefusesWhenBufferMoved(t *testing.T) {
	h := newHarness(t, "package main\n")
	path := h.Pane().File.Path
	h.lspGen = 1
	h.park(lspAnswer{
		gen:      1,
		kind:     answerCodeAction,
		versions: map[string]int{path: int(h.Pane().File.Session().Version()) + 1},
		actions: []lsp.CodeAction{{
			Title: "Add the fix",
			Edit: &lsp.WorkspaceEdit{Docs: []lsp.DocumentEdits{{
				Path: path,
				Edits: []lsp.TextEdit{{
					Range:   lsp.Range{Start: lsp.Position{Line: 0, Character: 0}, End: lsp.Position{Line: 0, Character: 0}},
					NewText: "// fixed\n",
				}},
			}}},
		}},
	})
	h.applyAnswer()
	h.press("enter")
	if got := h.text(); got != "package main\n" {
		t.Errorf("buffer = %q; a stale edit must apply nothing", got)
	}
	if !strings.Contains(h.Status(), "stale") {
		t.Errorf("status = %q, want a staleness refusal", h.Status())
	}
}

// An empty answer is the normal "nothing applies here", and it must say so
// rather than open an empty picker.
func TestEmptyCodeActionAnswerSaysSo(t *testing.T) {
	h := newHarness(t, "package main\n")
	h.lspGen = 1
	h.park(lspAnswer{gen: 1, kind: answerCodeAction})
	h.applyAnswer()
	if h.Picker.Open {
		t.Error("an empty answer opened the picker")
	}
	if h.Status() == "" {
		t.Error("an empty answer said nothing")
	}
}

// A resolve-only row is marked, so the extra round trip on choosing it is
// visible before the user commits to it.
func TestCodeActionLabelMarksResolve(t *testing.T) {
	label := codeActionLabel(lsp.CodeAction{
		Title: "Extract function",
		Kind:  "refactor.extract",
		Data:  json.RawMessage(`{"token":"abc"}`),
	})
	for _, want := range []string{"Extract function", "refactor.extract", "resolve"} {
		if !strings.Contains(label, want) {
			t.Errorf("label = %q, missing %q", label, want)
		}
	}
}

// The gate reads CodeActionProvider, in the shared "does not support" voice;
// the command path is gated separately on ExecuteCommandProvider, because a
// server can offer actions without accepting commands.
func TestCodeActionCapabilityGates(t *testing.T) {
	const wantActions = "language server does not support code actions"
	if got := codeActionGap(lsp.ServerCapabilities{}); got != wantActions {
		t.Errorf("absent codeActionProvider: %q", got)
	}
	if got := codeActionGap(lsp.ServerCapabilities{CodeActionProvider: json.RawMessage(`false`)}); got != wantActions {
		t.Errorf("false codeActionProvider: %q", got)
	}
	if got := codeActionGap(lsp.ServerCapabilities{CodeActionProvider: json.RawMessage(`{"codeActionKinds":["quickfix"]}`)}); got != "" {
		t.Errorf("advertised codeActionProvider: %q, want none", got)
	}
	const wantExec = "language server does not support execute command"
	if got := executeCommandGap(lsp.ServerCapabilities{}); got != wantExec {
		t.Errorf("absent executeCommandProvider: %q", got)
	}
	if got := executeCommandGap(lsp.ServerCapabilities{ExecuteCommandProvider: json.RawMessage(`{"commands":["raj.test"]}`)}); got != "" {
		t.Errorf("advertised executeCommandProvider: %q, want none", got)
	}
}

// The handshake advertises CodeAction literal support, so a server may send
// CodeAction objects with an edit rather than only the legacy Command results.
// Without the advertisement a server that respects the capability set returns
// only Commands, and the edit results this feature is built around never come.
func TestClientCapabilitiesAdvertiseCodeActionLiterals(t *testing.T) {
	caps := clientCapabilities()
	td, _ := caps["textDocument"].(map[string]any)
	if td == nil {
		t.Fatal("no textDocument capabilities")
	}
	ca, ok := td["codeAction"].(map[string]any)
	if !ok {
		t.Fatalf("codeAction = %T, want an options object", td["codeAction"])
	}
	if _, ok := ca["codeActionLiteralSupport"]; !ok {
		t.Error("codeActionLiteralSupport is not advertised")
	}
}
