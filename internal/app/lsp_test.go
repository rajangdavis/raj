package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"raj/internal/control"
	"raj/internal/editor"
	"raj/internal/lsp"
	"raj/internal/piecetable"
	"raj/internal/tabs"
)

// The rule the whole integration follows: no language feature may make the
// editor worse when it is unavailable. A file with no server says so once and
// changes nothing else.
func TestHoverWithoutAServerIsHarmless(t *testing.T) {
	h := newHarness(t, "some plain text\n")
	before := h.Pane().File.Text()
	h.press("super+i")

	if got := h.Pane().File.Text(); got != before {
		t.Error("asking for a hover changed the buffer")
	}
	// The harness fixture is a .go file, so which message appears depends on
	// whether gopls is installed where the tests run. Either is correct; what
	// matters is that something explains it and nothing else changed.
	if h.Status() == "" {
		t.Error("nothing happened and nothing said why")
	}
}

func TestDefinitionWithoutAServerIsHarmless(t *testing.T) {
	h := newHarness(t, "some plain text\n")
	head := h.Pane().Cursors.Primary().Head
	h.press("alt+super+d")

	if got := h.Pane().Cursors.Primary().Head; got != head {
		t.Error("the cursor moved with no server to say where to")
	}
	if len(h.Tabs.All()) != 1 {
		t.Error("a tab was opened with no definition to open")
	}
}

// An empty diagnostics list must not read as a clean file when there is no
// server to have said so. A fresh harness has no live server, so the answer
// carries a non-ok status whatever gopls is installed.
func TestLSPDiagnosticsReportsNoServer(t *testing.T) {
	h := newHarness(t, "package main\n")
	hs := host{a: h.App}
	caller, err := hs.LSP(h.Pane().File.Path, 0, 0, "diagnostics")
	if err != nil {
		t.Fatalf("LSP diagnostics: %v", err)
	}
	data, err := caller.Run(context.Background())
	if err != nil {
		t.Fatalf("diagnostics run: %v", err)
	}
	var out control.LSPResult
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("diagnostics JSON %q: %v", data, err)
	}
	if out.Status == "" || out.Status == control.LSPStatusOK {
		t.Errorf("status = %q; an empty list with no live server must not read as ok", out.Status)
	}
}

// Every reason there is no server maps to a status the CLI can refuse on, and
// only a live one is "ok".
func TestLSPStatusNamesTheState(t *testing.T) {
	cases := map[serverState]string{
		serverReady:      control.LSPStatusOK,
		serverStarting:   control.LSPStatusStarting,
		serverNotStarted: control.LSPStatusNotStarted,
		serverMissing:    control.LSPStatusMissing,
		serverNone:       control.LSPStatusNoServer,
		serverGaveUp:     control.LSPStatusGaveUp,
	}
	for st, want := range cases {
		if got := lspStatus(st); got != want {
			t.Errorf("lspStatus(%v) = %q, want %q", st, got, want)
		}
	}
}

// An answer for a position the cursor has left is dropped. It is worse than no
// answer, because it is shown as though it described where the cursor is now.
func TestStaleAnswersAreDropped(t *testing.T) {
	h := newHarness(t, "package main\n")
	h.lspGen = 5
	h.park(lspAnswer{gen: 4, kind: answerHover, text: "stale"})
	h.applyAnswer()

	if strings.Contains(h.Hover.Text(), "stale") {
		t.Errorf("panel = %q; a superseded answer was shown", h.Hover.Text())
	}

	h.park(lspAnswer{gen: 5, kind: answerHover, text: "current"})
	h.applyAnswer()
	if !strings.Contains(h.Hover.Text(), "current") {
		t.Errorf("panel = %q, want the current answer", h.Hover.Text())
	}
}

// A hover with nothing in it opens no panel: an empty box is worse than a
// word, and most positions in most files have nothing to say about them. The
// status line is where that word goes.
func TestEmptyHoverOpensNoPanel(t *testing.T) {
	h := newHarness(t, "package main\n")
	h.lspGen = 1
	h.park(lspAnswer{gen: 1, kind: answerHover, text: ""})
	h.applyAnswer()
	if h.Hover.Open {
		t.Error("an empty answer opened a panel")
	}
	if h.Status() == "" {
		t.Error("nothing happened and nothing said why")
	}
}

// A multi-line hover keeps its lines. Folding them onto one row was the status
// line's limitation and the reason the panel exists: a signature without its
// line breaks is a signature that has lost its shape.
func TestMultiLineHoverKeepsItsLines(t *testing.T) {
	h := newHarness(t, "package main\n")
	h.lspGen = 1
	h.park(lspAnswer{gen: 1, kind: answerHover, text: "func F(x int) error\n\nDoes a thing."})
	h.applyAnswer()

	if !h.Hover.Open {
		t.Fatal("no panel opened")
	}
	got := h.Hover.Text()
	if !strings.Contains(got, "\n") {
		t.Error("the answer was folded onto one line")
	}
	for _, want := range []string{"func F(x int) error", "Does a thing."} {
		if !strings.Contains(got, want) {
			t.Errorf("panel = %q, missing %q", got, want)
		}
	}
}

// A server that never advertised signature help is told apart from one with
// nothing to say: the gate speaks before the request, in the same voice as the
// no-server messages. A present-and-false provider means no.
func TestSignatureHelpCapabilityGate(t *testing.T) {
	const want = "language server does not support signature help"
	if got := capabilityGap(nil, "signature help"); got != want {
		t.Errorf("absent provider: gap = %q", got)
	}
	if got := capabilityGap(json.RawMessage(`false`), "signature help"); got != want {
		t.Errorf("false provider: gap = %q", got)
	}
	if got := capabilityGap(json.RawMessage(`{"triggerCharacters":["("]}`), "signature help"); got != "" {
		t.Errorf("advertised provider: gap = %q, want none", got)
	}
}

// The host LSP gate accepts the signature mode. Without it wired into the mode
// switch, host.LSP refuses it as "unknown lsp mode" before a server is ever
// asked — the failure is in raj, not in the absence of a server.
func TestSignatureHelpModeIsAcceptedByHostLSP(t *testing.T) {
	h := newHarness(t, "package main\n")
	hs := host{a: h.App}
	_, err := hs.LSP(h.Pane().File.Path, 1, 1, "signature")
	// Whether a server is installed or not, the one wrong answer is a refusal
	// of the mode itself: "no server" and a live caller are both correct here.
	if err != nil && strings.Contains(err.Error(), "unknown lsp mode") {
		t.Errorf("the signature mode was refused as unknown: %v", err)
	}
}

// A signature answer opens the hover panel with the active signature and its
// documentation, and does not move the caret: a signature help is something you
// read, not a jump. It reuses the panel rather than a second widget.
func TestSignatureHelpOpensThePanelWithoutMovingTheCaret(t *testing.T) {
	h := newHarness(t, "package main\n\nfunc F(a int, b string) error { return nil }\n\nfunc G() { _ = F(1, \"x\") }\n")
	head := h.Pane().Cursors.Primary().Head
	h.lspGen = 1
	h.park(lspAnswer{
		gen:  1,
		kind: answerSignature,
		help: &lsp.SignatureHelp{
			Signatures: []lsp.Signature{{
				Label:         "func F(a int, b string) error",
				Documentation: "F does a thing.",
				Parameters: []lsp.Parameter{
					{Label: "a int", Start: 7, End: 12, HasOffsets: true},
					{Label: "b string", Start: 14, End: 22, HasOffsets: true},
				},
			}},
			ActiveSignature: 0,
			ActiveParameter: 1,
		},
		line: 1,
		col:  3,
	})
	h.applyAnswer()

	if !h.Hover.Open {
		t.Fatal("no signature panel opened")
	}
	got := h.Hover.Text()
	for _, want := range []string{"func F(a int, b string) error", "F does a thing."} {
		if !strings.Contains(got, want) {
			t.Errorf("panel = %q, missing %q", got, want)
		}
	}
	if h.Pane().Cursors.Primary().Head != head {
		t.Error("the caret moved; a signature help must not jump")
	}
}

// Nothing at the position is a word on the status line, not an empty overlay.
func TestSignatureHelpNothingToShow(t *testing.T) {
	h := newHarness(t, "package main\n")
	h.lspGen = 1
	h.park(lspAnswer{gen: 1, kind: answerSignature})
	h.applyAnswer()
	if h.Hover.Open {
		t.Error("an empty answer opened a panel")
	}
	if !strings.Contains(h.Status(), "no signature help") {
		t.Errorf("status = %q", h.Status())
	}
}

// A server sends parameter offsets in UTF-16 code units into the signature
// label, the panel marks the active parameter with them, and the plain panel
// text reads as the signature without the mark characters leaking through.
func TestSignatureHelpMarksTheActiveParameter(t *testing.T) {
	h := newHarness(t, "package main\n")
	h.lspGen = 1
	h.park(lspAnswer{
		gen:  1,
		kind: answerSignature,
		help: &lsp.SignatureHelp{
			Signatures: []lsp.Signature{{
				Label:      "func F(name string, n int)",
				Parameters: []lsp.Parameter{{Label: "name string", Start: 7, End: 18, HasOffsets: true}, {Label: "n int", Start: 20, End: 25, HasOffsets: true}},
			}},
			ActiveSignature: 0,
			ActiveParameter: 0,
		},
		line: 1,
		col:  3,
	})
	h.applyAnswer()
	if got := h.Hover.Text(); got != "func F(name string, n int)" {
		t.Errorf("panel = %q, want the whole signature", got)
	}
}

// A definition result moves the cursor to the named position, converting from
// the server's UTF-16 coordinates to the buffer's byte offsets.
func TestDefinitionJumps(t *testing.T) {
	h := newHarness(t, "line one\nline two\nline three\n")
	h.lspGen = 1
	h.park(lspAnswer{
		gen:  1,
		kind: answerDefinition,
		locs: []lsp.Location{{
			Path:  h.Pane().File.Path,
			Range: lsp.Range{Start: lsp.Position{Line: 2, Character: 5}},
		}},
	})
	h.applyAnswer()

	line, col := h.Pane().File.LineCol(h.Pane().Cursors.Primary().Head)
	if line != 2 || col != 5 {
		t.Errorf("cursor at %d:%d, want 2:5", line, col)
	}
}

// Nothing found says so rather than jumping somewhere arbitrary.
func TestDefinitionNotFound(t *testing.T) {
	h := newHarness(t, "package main\n")
	head := h.Pane().Cursors.Primary().Head
	h.lspGen = 1
	h.park(lspAnswer{gen: 1, kind: answerDefinition})
	h.applyAnswer()

	if h.Pane().Cursors.Primary().Head != head {
		t.Error("the cursor moved with nothing found")
	}
	if !strings.Contains(h.Status(), "no definition") {
		t.Errorf("status = %q", h.Status())
	}
}

// A language with no configured server is not an error, and neither is a file
// type with no language at all.
func TestServerSelection(t *testing.T) {
	s := newServers([]string{"/w"})
	for _, path := range []string{"/w/notes.txt", "/w/Makefile", "/w/a.unknown", ""} {
		ls, st := s.for_(path, nil)
		if ls != nil || st != serverNone {
			t.Errorf("%q got a server (state %d)", path, st)
		}
	}
	if _, st := s.for_("/w/a.java", nil); st != serverNone {
		t.Error("java has a language id but no configured server")
	}
}

// A markdown file must resolve to the remark server: LanguageID already names
// the language, and the command table has to agree, or the editor reports a
// markdown buffer as a file type with no server.
func TestMarkdownFileResolvesToRemark(t *testing.T) {
	// An empty PATH keeps the test hermetic: the binary is missing rather than
	// started for real, and for_ still resolves the language. serverNone would
	// mean no command was configured at all.
	t.Setenv("PATH", "")
	s := newServers([]string{"/w"})
	_, st := s.for_("/w/notes.md", nil)
	if st == serverNone {
		t.Fatal("a markdown file was reported as having no server configured")
	}
	if msg := st.message("/w/notes.md"); strings.Contains(msg, "file type") {
		t.Errorf("message = %q; markdown is a supported file type", msg)
	}
	if msg := st.message("/w/notes.md"); !strings.Contains(msg, "remark-language-server") {
		t.Errorf("message = %q, want it to name the remark server", msg)
	}
	// for_ consults exactly this entry after LanguageID, so the key and the
	// --stdio argument are asserted where the lookup is decided.
	got := command[lsp.LanguageID("/w/notes.md")]
	want := []string{"remark-language-server", "--stdio"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("command[markdown] = %q, want %q", got, want)
	}
}

// A server that never advertised references is told apart from one with nothing
// to say: the gate speaks before the request, and its message names the feature
// in the same voice as the no-server messages. A present-and-false provider is
// the reason the capability fields are raw — it means no, and must not fail the
// handshake.
func TestReferencesCapabilityGate(t *testing.T) {
	if got := capabilityGap(nil, "references"); got != "language server does not support references" {
		t.Errorf("absent provider: gap = %q", got)
	}
	if got := capabilityGap(json.RawMessage(`false`), "references"); got != "language server does not support references" {
		t.Errorf("false provider: gap = %q", got)
	}
	if got := capabilityGap(json.RawMessage(`{"workDoneProgress":true}`), "references"); got != "" {
		t.Errorf("advertised provider: gap = %q, want none", got)
	}
}

// The locations a references answer carries go to the picker, and Enter opens
// the chosen use at its line and column. It reuses the picker path symbols
// already jump through, so there is no second listing UI to keep in step.
func TestReferencesOpenThePicker(t *testing.T) {
	h := newHarness(t, "line one\nline two\nline three\n")
	h.lspGen = 1
	h.park(lspAnswer{
		gen:  1,
		kind: answerReferences,
		locs: []lsp.Location{
			{Path: h.Pane().File.Path, Range: lsp.Range{Start: lsp.Position{Line: 1, Character: 2}}},
			{Path: h.Pane().File.Path, Range: lsp.Range{Start: lsp.Position{Line: 2, Character: 4}}},
		},
	})
	h.applyAnswer()

	if !h.Picker.Open || h.Focused() != FocusPicker {
		t.Fatal("references did not open the picker")
	}
	if got := h.Picker.Results(); got != 2 {
		t.Fatalf("picker results = %d, want 2", got)
	}
	h.press("enter")
	if h.Picker.Open {
		t.Error("choosing should close the picker")
	}
	line, col := cursorLine(h), cursorCol(h)
	if line != 2 || col != 3 {
		t.Errorf("cursor at %d:%d, want 2:3", line, col)
	}
}

// No references is a word on the status line, not an empty overlay.
func TestReferencesNotFound(t *testing.T) {
	h := newHarness(t, "package main\n")
	h.lspGen = 1
	h.park(lspAnswer{gen: 1, kind: answerReferences})
	h.applyAnswer()
	if h.Picker.Open {
		t.Error("an empty answer opened the picker")
	}
	if !strings.Contains(h.Status(), "no references") {
		t.Errorf("status = %q", h.Status())
	}
}

// The provider set the campaign grows into is announced, or a server will not// Each sibling jump reads its own capability: a server that advertised one and
// not another must be refused for the one it lacks, in the same voice as the
// no-server messages, and reading the wrong field would ask for a method the
// server will not answer.
func TestSiblingJumpCapabilityGates(t *testing.T) {
	caps := lsp.ServerCapabilities{
		DeclarationProvider:    json.RawMessage(`true`),
		TypeDefinitionProvider: json.RawMessage(`false`),
		ImplementationProvider: json.RawMessage(`{"workDoneProgress":true}`),
	}
	cases := []struct {
		name string
		req  jumpRequest
		want string
	}{
		{"declaration", declarationJump(), ""},
		{"type definition", typeDefinitionJump(), "language server does not support type definition"},
		{"implementation", implementationJump(), ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := capabilityGap(c.req.provider(caps), c.req.feature); got != c.want {
				t.Errorf("gap = %q, want %q", got, c.want)
			}
		})
	}
	// An absent provider is a gap for each: a server that never mentioned the
	// feature must not be asked, and the message names the feature.
	for _, req := range []jumpRequest{declarationJump(), typeDefinitionJump(), implementationJump()} {
		want := "language server does not support " + req.feature
		if got := capabilityGap(req.provider(lsp.ServerCapabilities{}), req.feature); got != want {
			t.Errorf("%s: absent provider gap = %q, want %q", req.feature, got, want)
		}
	}
}

// The host LSP gate accepts the three sibling modes. Without them wired into the
// mode switch, host.LSP refuses each as "unknown lsp mode" before a server is
// asked — the failure is in raj, not in the absence of a server.
func TestSiblingJumpModesAreAcceptedByHostLSP(t *testing.T) {
	h := newHarness(t, "package main\n")
	hs := host{a: h.App}
	for _, mode := range []string{"declaration", "type-definition", "implementation"} {
		if _, err := hs.LSP(h.Pane().File.Path, 1, 1, mode); err != nil && strings.Contains(err.Error(), "unknown lsp mode") {
			t.Errorf("%s was refused as unknown: %v", mode, err)
		}
	}
}

// A declaration answer jumps like a definition, and the empty answer names the
// feature rather than definition.
func TestDeclarationJumps(t *testing.T) {
	h := newHarness(t, "line one\nline two\nline three\n")
	h.lspGen = 1
	h.park(lspAnswer{
		gen: 1, kind: answerJump, feature: "declaration", mode: jumpToFirst,
		locs: []lsp.Location{{
			Path:  h.Pane().File.Path,
			Range: lsp.Range{Start: lsp.Position{Line: 2, Character: 5}},
		}},
	})
	h.applyAnswer()

	line, col := h.Pane().File.LineCol(h.Pane().Cursors.Primary().Head)
	if line != 2 || col != 5 {
		t.Errorf("cursor at %d:%d, want 2:5", line, col)
	}
}

// No declaration is a word on the status line naming the lookup, not a silent
// no-op and not "no definition found".
func TestDeclarationNotFoundNamesTheFeature(t *testing.T) {
	h := newHarness(t, "package main\n")
	head := h.Pane().Cursors.Primary().Head
	h.lspGen = 1
	h.park(lspAnswer{gen: 1, kind: answerJump, feature: "declaration", mode: jumpToFirst})
	h.applyAnswer()
	if h.Pane().Cursors.Primary().Head != head {
		t.Error("the cursor moved with nothing found")
	}
	if !strings.Contains(h.Status(), "no declaration found") {
		t.Errorf("status = %q", h.Status())
	}
}

// Several implementations is the normal case, so the answer lists them and
// Enter opens the chosen one. A single implementation jumps instead: an overlay
// for one row is in the way.
func TestImplementationPicksWhenSeveral(t *testing.T) {
	h := newHarness(t, "line one\nline two\nline three\n")
	h.lspGen = 1
	h.park(lspAnswer{
		gen: 1, kind: answerJump, feature: "implementation",
		mode: pickWhenMany, title: "Implementations",
		locs: []lsp.Location{
			{Path: h.Pane().File.Path, Range: lsp.Range{Start: lsp.Position{Line: 1, Character: 2}}},
			{Path: h.Pane().File.Path, Range: lsp.Range{Start: lsp.Position{Line: 2, Character: 4}}},
		},
	})
	h.applyAnswer()

	if !h.Picker.Open || h.Focused() != FocusPicker {
		t.Fatal("several implementations did not open the picker")
	}
	h.press("enter")
	if h.Picker.Open {
		t.Error("choosing should close the picker")
	}
	line, col := cursorLine(h), cursorCol(h)
	if line != 2 || col != 3 {
		t.Errorf("cursor at %d:%d, want 2:3", line, col)
	}
}

func TestSingleImplementationJumps(t *testing.T) {
	h := newHarness(t, "line one\nline two\nline three\n")
	h.lspGen = 1
	h.park(lspAnswer{
		gen: 1, kind: answerJump, feature: "implementation",
		mode: pickWhenMany, title: "Implementations",
		locs: []lsp.Location{{
			Path:  h.Pane().File.Path,
			Range: lsp.Range{Start: lsp.Position{Line: 2, Character: 5}},
		}},
	})
	h.applyAnswer()
	if h.Picker.Open {
		t.Error("a single implementation opened the picker")
	}
	line, col := h.Pane().File.LineCol(h.Pane().Cursors.Primary().Head)
	if line != 2 || col != 5 {
		t.Errorf("cursor at %d:%d, want 2:5", line, col)
	}
}

// The provider set the campaign grows into is announced, or a server will not
// send the response shapes the later waves consume.
func TestClientCapabilitiesAnnounceTheProviderSet(t *testing.T) {
	caps := clientCapabilities()
	td, _ := caps["textDocument"].(map[string]any)
	if td == nil {
		t.Fatal("no textDocument capabilities")
	}
	for _, name := range []string{
		"hover", "definition", "declaration", "typeDefinition", "implementation",
		"references", "documentHighlight", "completion", "signatureHelp",
		"formatting", "rangeFormatting", "rename", "codeAction", "codeLens",
		"documentSymbol", "documentLink", "semanticTokens",
		"inlayHint", "foldingRange", "synchronization",
	} {
		if _, ok := td[name]; !ok {
			t.Errorf("textDocument.%s is not advertised", name)
		}
	}
	// foldingRange is advertised now that a feature requests it; without the
	// advertisement a server never answers, and the fold toggle would have no
	// ranges to toggle.
	folding, ok := td["foldingRange"]
	if !ok {
		t.Error("textDocument.foldingRange is not advertised")
	}
	if _, isMap := folding.(map[string]any); !isMap {
		t.Errorf("foldingRange = %T, want an options object", folding)
	}

	ws, _ := caps["workspace"].(map[string]any)
	if ws == nil {
		t.Fatal("no workspace capabilities")
	}
	for _, name := range []string{
		"symbol", "configuration", "workspaceEdit", "executeCommand",
		"didChangeWatchedFiles", "workspaceFolders",
	} {
		if _, ok := ws[name]; !ok {
			t.Errorf("workspace.%s is not advertised", name)
		}
	}
	// raj has one root and never sends workspace/didChangeWorkspaceFolders, so
	// it must not claim folder support. true here asks servers for folder
	// events and a workspace/workspaceFolders request nothing answers.
	if got := ws["workspaceFolders"]; got != false {
		t.Errorf("workspace.workspaceFolders = %v, want false for a single-root client", got)
	}
}

// The inlayHint capability is presence-only: a server sends hints only when
// the client advertised it, and it must not advertise resolveSupport, which
// would defer a hint's text edits to a second request raj does not make.
func TestClientCapabilitiesAdvertiseInlayHints(t *testing.T) {
	caps := clientCapabilities()
	td, _ := caps["textDocument"].(map[string]any)
	if td == nil {
		t.Fatal("no textDocument capabilities")
	}
	inlay, ok := td["inlayHint"].(map[string]any)
	if !ok {
		t.Fatalf("inlayHint = %T, want an options object", td["inlayHint"])
	}
	if len(inlay) != 0 {
		t.Errorf("inlayHint = %v, want an empty object — presence is the advertisement", inlay)
	}
}

// Stopping is safe with nothing started, and safe twice.
func TestStopAllIsSafe(t *testing.T) {
	s := newServers([]string{"/w"})
	s.stopAll()
	s.stopAll()
}

// Each reason for having no server needs a different reaction from the user —
// install something, wait, look at why it keeps dying, or accept that this
// language has none. One message for all four told nobody anything, and on a
// Go file it said the file type was unsupported while the server was starting.
func TestServerStateMessagesAreDistinct(t *testing.T) {
	seen := map[string]bool{}
	for _, st := range []serverState{serverStarting, serverMissing, serverGaveUp, serverNone} {
		msg := st.message("/w/a.go")
		if msg == "" {
			t.Errorf("state %d has no message", st)
		}
		if seen[msg] {
			t.Errorf("state %d repeats the message %q", st, msg)
		}
		seen[msg] = true
	}
	if got := serverReady.message("/w/a.go"); got != "" {
		t.Errorf("a ready server says %q, want nothing", got)
	}
}

// A missing binary says which one, so the fix is obvious rather than a guess.
func TestMissingBinaryIsNamed(t *testing.T) {
	if got := serverMissing.message("/w/a.go"); !strings.Contains(got, "gopls") {
		t.Errorf("message = %q, want it to name gopls", got)
	}
	if got := serverMissing.message("/w/a.rs"); !strings.Contains(got, "rust-analyzer") {
		t.Errorf("message = %q, want it to name rust-analyzer", got)
	}
}

// A Go file must never be told its type is unsupported. That was the reported
// symptom: the message said "no language server for this file" on a .go file,
// which is the one thing that was not true.
func TestGoFileIsNeverCalledUnsupported(t *testing.T) {
	s := newServers([]string{"/w"})
	_, st := s.for_("/w/main.go", nil)
	if st == serverNone {
		t.Fatal("a Go file was reported as having no server configured")
	}
	if msg := st.message("/w/main.go"); strings.Contains(msg, "file type") {
		t.Errorf("message = %q; Go is a supported file type", msg)
	}
}

// Every path handed to LSP is absolute. A relative one produces
// file://internal/editor/actions.go — a URI whose host is "internal" and which
// names nothing the server can open.
func TestDocumentPathsAreAbsolute(t *testing.T) {
	h := newWorkspace(t, 120, 30)
	h.OpenFile(filepath.Join(h.primaryRoot(), "main.go"))
	h.drain()

	if got := h.docPath(h.Pane()); !filepath.IsAbs(got) {
		t.Errorf("docPath = %q, want an absolute path", got)
	}
	if got := lsp.URI(h.docPath(h.Pane())); !strings.HasPrefix(got, "file:///") {
		t.Errorf("URI = %q, want three slashes — two means the next segment is a host", got)
	}
}

// A pane with no path on disk has nothing to sync, and must not be made into a
// URI relative to the workspace.
func TestUnnamedBufferHasNoDocumentPath(t *testing.T) {
	h := newHarness(t, "text\n")
	h.Pane().File.Path = ""
	if got := h.docPath(h.Pane()); got != "" {
		t.Errorf("docPath = %q, want empty for an unnamed buffer", got)
	}
	if got := h.docPath(nil); got != "" {
		t.Errorf("docPath(nil) = %q", got)
	}
}

// parkAnswer delivers a completion answer as a server would, anchored to the
// word the popup is currently showing.
//
// The anchor is part of an answer now: a list describes one word at one place,
// and "hand" on line 2 is not the same question as "hand" on line 40. Tests go
// through here so a fake answer carries what a real one does.
func parkAnswer(h *harness, prefix string, items []lsp.CompletionItem) {
	line, col := h.Complete.Anchor()
	h.parkCompletion(lspAnswer{
		gen: h.completeGen, kind: answerCompletion, prefix: prefix,
		items: items, line: line, col: col,
	})
	h.applyAnswer()
}

// Buffer words show instantly and the server's answer replaces them. A
// completion list that appears a beat after you stop typing feels broken even
// when it is better, so the fast answer goes up first.
func TestLSPCompletionReplacesBufferWords(t *testing.T) {
	h := newHarness(t, "handoff()\n\n")
	h.press("ctrl+g")
	h.typeText("2")
	h.press("enter")
	h.typeText("hand")
	if !h.Complete.Open {
		t.Fatal("no buffer-word popup")
	}
	if c, _ := h.Complete.Selected(); c.Word != "handoff" {
		t.Fatalf("buffer words showed %q", c.Word)
	}

	h.completeGen++
	parkAnswer(h, "hand", []lsp.CompletionItem{
		{Label: "handleRequest", Insert: "handleRequest", Detail: "func()"},
	})

	c, ok := h.Complete.Selected()
	if !ok || c.Word != "handleRequest" {
		t.Errorf("selected %q, want the server's answer", c.Word)
	}
	if c.Detail != "func()" {
		t.Errorf("detail = %q, want the type from the server", c.Detail)
	}
}

// An answer for a prefix the typing has moved past is dropped: showing it would
// suggest completions for a word that is no longer being typed.
func TestStaleCompletionIsDropped(t *testing.T) {
	h := newHarness(t, "handoff()\n\n")
	h.press("ctrl+g")
	h.typeText("2")
	h.press("enter")
	h.typeText("hand")

	h.parkCompletion(lspAnswer{
		gen: h.completeGen, kind: answerCompletion, prefix: "zzz",
		items: []lsp.CompletionItem{{Label: "wrong", Insert: "wrong"}},
	})
	h.applyAnswer()

	if c, _ := h.Complete.Selected(); c.Word == "wrong" {
		t.Error("a completion for a different prefix was shown")
	}
}

// A server answer that filters down to nothing leaves the buffer words up:
// something usually right beats an empty list.
func TestEmptyServerAnswerKeepsBufferWords(t *testing.T) {
	h := newHarness(t, "handoff()\n\n")
	h.press("ctrl+g")
	h.typeText("2")
	h.press("enter")
	h.typeText("hand")
	before, _ := h.Complete.Selected()

	parkAnswer(h, "hand", []lsp.CompletionItem{{Label: "nomatch", Insert: "nomatch"}})

	if c, _ := h.Complete.Selected(); c.Word != before.Word {
		t.Errorf("selected %q, want the buffer words left alone", c.Word)
	}
}

// The server's ordering is kept rather than re-ranked. It encodes scope and
// type compatibility, which is the reason to ask a server at all.
func TestServerOrderingSurvivesToThePopup(t *testing.T) {
	// The buffer needs a word that is a real completion of the prefix, or no
	// popup opens for the server's answer to replace.
	h := newHarness(t, "handoff()\n\n")
	h.press("ctrl+g")
	h.typeText("2")
	h.press("enter")
	h.typeText("hand")

	parkAnswer(h, "hand", []lsp.CompletionItem{
		{Label: "handZebra", Insert: "handZebra"},
		{Label: "handApple", Insert: "handApple"},
	})

	// Neither has a sort key, so the label orders — but the point is that the
	// popup shows what the lsp package ordered rather than re-sorting by
	// length or locality the way buffer words are ranked.
	if c, _ := h.Complete.Selected(); c.Word != "handApple" {
		t.Errorf("first candidate %q, want the lsp ordering", c.Word)
	}
}

// A completion answer arriving with the popup closed must not reopen it.
func TestCompletionDoesNotReopenAClosedPopup(t *testing.T) {
	h := newHarness(t, "handoff()\n\n")
	h.parkCompletion(lspAnswer{
		gen: h.completeGen, kind: answerCompletion, prefix: "",
		items: []lsp.CompletionItem{{Label: "surprise", Insert: "surprise"}},
	})
	h.applyAnswer()
	if h.Complete.Open {
		t.Error("a late answer reopened the popup")
	}
}

// The journal window becomes the batch a sync sends: one edit per op, in
// application order, with the inserted text read back out of the stores.
// Offsets here come from the document, never a hand count — the UTF-16
// conversion downstream stands on them being right.
func TestEditsSince(t *testing.T) {
	sess := piecetable.NewSession(piecetable.NewDoc("one\ntwo\n", 8))
	v0 := sess.Version()

	two := strings.Index("one\ntwo\n", "two")
	sess.ApplyDiff(piecetable.User, sess.Version(), []piecetable.Hunk{
		{Start: two, End: two + len("two"), Text: "TWO"},
	})
	v1 := sess.Version()

	edits := editsSince(sess, v0)
	if len(edits) != 1 {
		t.Fatalf("edits = %v, want the one op", edits)
	}
	want := lsp.Edit{Start: two, End: two + len("two"), Text: "TWO"}
	if edits[0] != want {
		t.Errorf("edit = %+v, want %+v", edits[0], want)
	}

	// The window pins to the version: asking from the present yields nothing.
	if got := editsSince(sess, v1); got != nil {
		t.Errorf("editsSince(present) = %v, want nil — nothing since", got)
	}

	// Undo is an op too, and it restores what the edit removed: the reversal
	// deletes "TWO" and re-inserts the "two" still held in the store.
	if !sess.Undo(piecetable.User) {
		t.Fatal("undo refused its only op")
	}
	edits = editsSince(sess, v1)
	if len(edits) != 1 {
		t.Fatalf("edits after undo = %v, want the reversal", edits)
	}
	want = lsp.Edit{Start: two, End: two + len("TWO"), Text: "two"}
	if edits[0] != want {
		t.Errorf("undo edit = %+v, want %+v", edits[0], want)
	}
}

// A window of several ops stays in order, and an insertion's text reads back
// byte for byte — the multibyte case, since the ranges convert to UTF-16
// against these exact offsets.
func TestEditsSincePreservesOrderAndText(t *testing.T) {
	const doc = "a λ日 😀\n"
	sess := piecetable.NewSession(piecetable.NewDoc(doc, 8))
	v0 := sess.Version()

	at := strings.Index(doc, "λ")
	sess.Insert(piecetable.User, at, "→")
	sess.Delete(piecetable.User, 0, 1)

	edits := editsSince(sess, v0)
	if len(edits) != 2 {
		t.Fatalf("edits = %v, want insert then delete", edits)
	}
	if want := (lsp.Edit{Start: at, End: at, Text: "→"}); edits[0] != want {
		t.Errorf("insert = %+v, want %+v", edits[0], want)
	}
	if want := (lsp.Edit{Start: 0, End: 1, Text: ""}); edits[1] != want {
		t.Errorf("delete = %+v, want %+v", edits[1], want)
	}
}

// A multi-hunk agent diff is the batch incremental sync was built for. Each
// hunk lands as one op, and every hunk after the first was rebased through the
// hunks before it, so its offsets mean something only in the frame those hunks
// produced — which is also the frame the server applies a ranged change to.
// The proof is a replay: applying the batch in order to the pre-diff text must
// reproduce the buffer byte for byte.
func TestEditsSinceMultiHunkDiff(t *testing.T) {
	const doc = "one two λ😀\nthree\nfour\n"
	sess := piecetable.NewSession(piecetable.NewDoc(doc, 8))
	v0 := sess.Version()

	at := func(text, sub string) int {
		i := strings.Index(text, sub)
		if i < 0 {
			t.Fatalf("%q not in %q — the test's own fixture is wrong", sub, text)
		}
		return i
	}
	_, conflicts, _ := sess.ApplyDiff(piecetable.User, v0, []piecetable.Hunk{
		// Disjoint hunks, each written against v0: a shrink, a replacement
		// past it that the shrink moves, and a deletion past both.
		{Start: at(doc, "two"), End: at(doc, "two") + len("two"), Text: "2"},
		{Start: at(doc, "λ😀"), End: at(doc, "λ😀") + len("λ😀"), Text: "X"},
		{Start: at(doc, "four"), End: at(doc, "four") + len("four")},
	})
	if len(conflicts) != 0 {
		t.Fatalf("conflicts = %v, want none — the hunks are disjoint", conflicts)
	}

	edits := editsSince(sess, v0)
	if len(edits) != 3 {
		t.Fatalf("edits = %v, want one per hunk", edits)
	}

	// The wants are derived by replaying the hunks on the test's own copy, so
	// a wrongly rebased offset shows as a wrong value, not a restatement of
	// the code under test.
	mid1 := doc[:at(doc, "two")] + "2" + doc[at(doc, "two")+len("two"):]
	mid2 := mid1[:at(mid1, "λ😀")] + "X" + mid1[at(mid1, "λ😀")+len("λ😀"):]
	wants := []lsp.Edit{
		{Start: at(doc, "two"), End: at(doc, "two") + len("two"), Text: "2"},
		{Start: at(mid1, "λ😀"), End: at(mid1, "λ😀") + len("λ😀"), Text: "X"},
		{Start: at(mid2, "four"), End: at(mid2, "four") + len("four")},
	}
	for i, want := range wants {
		if edits[i] != want {
			t.Errorf("edit %d = %+v, want %+v", i, edits[i], want)
		}
	}

	// The property the UTF-16 conversion downstairs stands on: replayed in
	// order, each edit against the frame its predecessors produced, the batch
	// arrives at the buffer exactly.
	replay := doc
	for _, e := range edits {
		if e.Start < 0 || e.End < e.Start || e.End > len(replay) {
			t.Fatalf("edit %+v does not fit the %d-byte frame it claims", e, len(replay))
		}
		replay = replay[:e.Start] + e.Text + replay[e.End:]
	}
	if got := sess.Buffer().Slice(0, sess.Buffer().Len()); replay != got {
		t.Errorf("replayed batch %q, buffer %q", replay, got)
	}
}

// acceptServerEdit drives the same path a keystroke takes: open a blank line, type
// a prefix that opens the popup, park a server answer carrying the item, and
// return the harness with the popup showing it. "handleRequest" is already in the
// buffer, so a buffer word opens the popup and the server answer replaces it.
func acceptServerEdit(t *testing.T, prefix string, item lsp.CompletionItem) *harness {
	t.Helper()
	h := newHarness(t, "handleRequest()\n\n")
	h.press("ctrl+g")
	h.typeText("2")
	h.press("enter")
	h.typeText(prefix)
	if !h.Complete.Open {
		t.Fatal("setup: no popup")
	}
	parkAnswer(h, prefix, []lsp.CompletionItem{item})
	return h
}

// A textEdit names the span to overwrite, so accepting replaces it rather than
// typing the word. The suffix past the range is left alone, which is the
// difference from the plain path that appends the remainder.
func TestAcceptServerTextEditReplacesRange(t *testing.T) {
	h := acceptServerEdit(t, "hand", lsp.CompletionItem{
		Label:  "handleRequest",
		Insert: "handleRequest",
		Edit: &lsp.TextEdit{
			Range:   lsp.Range{Start: lsp.Position{Line: 1, Character: 0}, End: lsp.Position{Line: 1, Character: 4}},
			NewText: "handleEdit",
		},
	})
	h.press("tab")
	if got := cursorLineText(h); got != "handleEdit" {
		t.Errorf("line = %q, want handleEdit — the server range was not replaced", got)
	}
}

// The import line travels as an additionalTextEdit. Without it the completion
// leaves the file uncompilable, which is the whole reason to honour the field.
func TestAcceptServerAdditionalTextEditsLand(t *testing.T) {
	h := acceptServerEdit(t, "hand", lsp.CompletionItem{
		Label: "handleRequest",
		Edit: &lsp.TextEdit{
			Range:   lsp.Range{Start: lsp.Position{Line: 1, Character: 0}, End: lsp.Position{Line: 1, Character: 4}},
			NewText: "handleRequest",
		},
		Additional: []lsp.TextEdit{{
			Range:   lsp.Range{Start: lsp.Position{Line: 0, Character: 0}, End: lsp.Position{Line: 0, Character: 0}},
			NewText: "import \"fmt\"\n",
		}},
	})
	h.press("tab")
	if got := h.text(); !strings.HasPrefix(got, "import \"fmt\"\n") {
		t.Errorf("text = %q, want the import line at the top", got)
	}
	if got := cursorLineText(h); got != "handleRequest" {
		t.Errorf("line = %q, want the completed word under the cursor", got)
	}
}

// The word and its import are one user action, so one undo reverses both.
func TestAcceptServerEditsAreOneUndo(t *testing.T) {
	h := acceptServerEdit(t, "hand", lsp.CompletionItem{
		Label: "handleRequest",
		Edit: &lsp.TextEdit{
			Range:   lsp.Range{Start: lsp.Position{Line: 1, Character: 0}, End: lsp.Position{Line: 1, Character: 4}},
			NewText: "handleRequest",
		},
		Additional: []lsp.TextEdit{{
			Range:   lsp.Range{Start: lsp.Position{Line: 0, Character: 0}, End: lsp.Position{Line: 0, Character: 0}},
			NewText: "import \"fmt\"\n",
		}},
	})
	before := h.text()
	h.press("tab")
	if h.text() == before {
		t.Fatal("accepting changed nothing")
	}
	h.press("super+z")
	if got := h.text(); got != before {
		t.Errorf("one undo left %q, want %q", got, before)
	}
}

// A candidate with no textEdit keeps the old path exactly: the remainder of the
// word is typed after the prefix.
func TestAcceptWithoutTextEditTypesTheRemainder(t *testing.T) {
	h := acceptServerEdit(t, "hand", lsp.CompletionItem{
		Label: "handleRequest", Insert: "handleRequest",
	})
	h.press("tab")
	if got := cursorLineText(h); got != "handleRequest" {
		t.Errorf("line = %q, want handleRequest", got)
	}
}

// The server answered when the prefix was shorter, so its range ends before the
// cursor. The characters typed since must be overwritten, not left dangling
// after the insertion.
func TestAcceptServerEditCoversTypedExtension(t *testing.T) {
	h := acceptServerEdit(t, "han", lsp.CompletionItem{
		Label: "handleRequest",
		Edit: &lsp.TextEdit{
			Range:   lsp.Range{Start: lsp.Position{Line: 1, Character: 0}, End: lsp.Position{Line: 1, Character: 3}},
			NewText: "handleEdit",
		},
	})
	h.typeText("d")
	if got := cursorLineText(h); got != "hand" {
		t.Fatalf("setup: line = %q, want hand", got)
	}
	h.press("tab")
	if got := cursorLineText(h); got != "handleEdit" {
		t.Errorf("line = %q, want handleEdit — the typed extension should be replaced", got)
	}
}

// The idle scan registers a dirty document the server has not opened, and
// pushes one the server already has when the buffer has moved past the version
// it saw. The decision is pure, so the cases can be stated without a language
// server — there is no fake server in this package to drive a live sync, so the
// registration rule is pinned here.
func TestNeedsSync(t *testing.T) {
	cases := []struct {
		name           string
		open, dirty    bool
		synced, buffer int
		want           bool
	}{
		{"not open and dirty", false, true, 0, 5, true},
		{"not open and clean", false, false, 0, 5, false},
		{"open and current", true, true, 5, 5, false},
		{"open and behind", true, false, 4, 5, true},
		{"open and ahead", true, false, 6, 5, true},
	}
	for _, tc := range cases {
		if got := needsSync(tc.open, tc.dirty, tc.synced, tc.buffer); got != tc.want {
			t.Errorf("%s: needsSync(%v, %v, %d, %d) = %v, want %v",
				tc.name, tc.open, tc.dirty, tc.synced, tc.buffer, got, tc.want)
		}
	}
}

// The idle scan must never start a language server, and must not search PATH
// either: the lookup is byID-only, so a fresh editor with no server costs one
// map probe per pane. A registered server that is not live yet is skipped
// without a panic.
func TestSyncDirtyDocsNeverStartsAServer(t *testing.T) {
	h := newHarness(t, "package main\n")
	h.syncDirtyDocs()
	if n := len(h.servers.byID); n != 0 {
		t.Fatalf("the idle scan registered %d server(s) with none running", n)
	}
	// The states a start leaves behind while the handshake is in flight or
	// after a crash: an entry with no sync, and one whose sync has no live
	// connection. Neither may be treated as live.
	key := serverKey{root: h.servers.rootFor(h.Pane().File.Path), lang: lsp.LanguageID(h.Pane().File.Path)}
	h.servers.byID[key] = &langServer{srv: &lsp.Server{}}
	h.syncDirtyDocs()
	h.servers.byID[key] = &langServer{srv: &lsp.Server{}, sync: lsp.NewSync(nil, lsp.SyncFull)}
	h.syncDirtyDocs()
}

// lspSaved on an editor with no live server must do nothing: a save is no
// reason to start a language server, and a pane with no path has nothing on
// disk anyway. There is no fake LSP server in this package to observe the
// notification — the wire itself is covered by the lsp package's TestChanged.
func TestLSPSavedWithoutAServerIsHarmless(t *testing.T) {
	h := newHarness(t, "package main\n")
	h.lspSaved(h.Pane())
	// A pane with no path, one with no File, and no pane at all are the same
	// no-op: each has nothing on disk for a server to hear about.
	h.lspSaved(&editor.Pane{File: &editor.File{}})
	h.lspSaved(&editor.Pane{})
	h.lspSaved(nil)
	if n := len(h.servers.byID); n != 0 {
		t.Errorf("lspSaved registered %d server(s) with none running", n)
	}
}

// stubGopls puts a fake gopls on PATH so a start is observable in the server
// table without a real language server. for_ records the entry synchronously,
// before the handshake a stub cannot complete, so an assertion on the table
// does not race the goroutine start.
func stubGopls(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "gopls"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The stub goes first so it shadows a real gopls; the rest of PATH stays so
	// the explorer tree's git lookups are unaffected by the save.
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// An ordinary save of an already-named file must not start a language server.
// lspSaved stays live because a save is no reason to spawn something nothing
// asked for; the gate is the rename, so a same-path save warms nothing even
// with a server binary available. This pins the rule the rename gate relies
// on, so a future change that warms on every save is caught here.
func TestOrdinarySaveDoesNotWarmAServer(t *testing.T) {
	stubGopls(t)
	h := newHarness(t, "package main\n")
	p := h.Pane()
	h.write(p, p.File.Path, false, nil)
	// The save landed, so the table is empty because nothing warmed and not
	// because the write failed before the gate was reached.
	if got := h.Status(); !strings.Contains(got, "saved") {
		t.Fatalf("setup: the save did not land: status = %q", got)
	}
	if n := len(h.servers.byID); n != 0 {
		t.Errorf("an ordinary save registered %d server(s), want none", n)
	}
}

// The save that names a buffer is the first moment its language can be chosen,
// so it must start the server then: without warmSaved, diagnostics and hints
// wait for a hover or a reopen. The start is read out of the server table,
// where for_ records it before the handshake finishes; the same-path save in
// the test above is the negative half of the rename gate.
func TestSaveAsWarmsTheLanguageServer(t *testing.T) {
	stubGopls(t)
	h := newHarness(t, "package main\n")
	p := h.Pane()
	renamed := filepath.Join(filepath.Dir(p.File.Path), "renamed.go")
	h.write(p, renamed, false, nil)
	if n := len(h.servers.byID); n != 1 {
		t.Fatalf("the save-as registered %d server(s), want the go server started", n)
	}
	if _, ok := h.servers.byID[serverKey{root: h.servers.rootFor(renamed), lang: "go"}]; !ok {
		t.Errorf("servers = %v, want the go server for the renamed .go file", h.servers.byID)
	}
}

// openLanguages is the distinct language ids of the open panes, in the order
// they are first seen. WarmServers turns each into one server per distinct
// root, so a nil pane, an unnamed buffer and a file type with no language must
// not appear in the list, and two files of one language must appear once.
func TestOpenLanguages(t *testing.T) {
	pane := func(path string) *editor.Pane {
		return &editor.Pane{File: &editor.File{Path: path}}
	}
	cases := []struct {
		name  string
		panes []*editor.Pane
		want  []string
	}{
		{"none", nil, nil},
		{"nil pane and nil file", []*editor.Pane{nil, {}}, nil},
		{"no language", []*editor.Pane{pane("Makefile"), pane("notes.txt"), pane("")}, nil},
		{"distinct in first-seen order", []*editor.Pane{pane("a.go"), pane("b.rs"), pane("c.py")}, []string{"go", "rust", "python"}},
		{"duplicates collapse to the first", []*editor.Pane{pane("a.go"), pane("b.go"), pane("c.rs"), pane("d.go")}, []string{"go", "rust"}},
		{"empty ids skipped between", []*editor.Pane{pane("a.go"), pane("README"), pane("b.rs")}, []string{"go", "rust"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := openLanguages(tc.panes)
			if len(got) != len(tc.want) {
				t.Fatalf("openLanguages = %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("openLanguages = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// WarmServers with nothing open must touch no server registry. It is not run
// against a harness that has real files: for_ would spawn a real language server
// if one is installed, so the spawn path is deliberately left uncovered here —
// the pure helper above is where the language selection is pinned, and this
// covers the empty case that must be a no-op.
func TestWarmServersWithNoPanesStartsNothing(t *testing.T) {
	a := &App{Tabs: tabs.New(2), servers: newServers([]string{"/w"})}
	a.WarmServers()
	if n := len(a.servers.byID); n != 0 {
		t.Fatalf("WarmServers registered %d server(s) with no panes", n)
	}
}

// serverInitOptions turns gopls's inlay hints on — gopls is the one server that
// ships them all disabled — and configures nothing for a language with no such
// need, so the initialize params omit the field rather than send a null.
func TestServerInitOptionsTurnOnGoplsHints(t *testing.T) {
	opts, ok := serverInitOptions("go").(map[string]any)
	if !ok {
		t.Fatalf("serverInitOptions(go) = %#v, want a map", serverInitOptions("go"))
	}
	hints, ok := opts["hints"].(map[string]any)
	if !ok {
		t.Fatalf("go options = %#v, want a hints map", opts)
	}
	for _, name := range []string{
		"assignVariableTypes", "rangeVariableTypes",
		"compositeLiteralFields", "constantValues",
	} {
		if hints[name] != true {
			t.Errorf("hints[%q] = %v, want true", name, hints[name])
		}
	}
	for _, id := range []string{"rust", "python", ""} {
		if got := serverInitOptions(id); got != nil {
			t.Errorf("serverInitOptions(%q) = %#v, want nil", id, got)
		}
	}
}

// The server asks for settings and gets one null per item, in order. With no
// settings surface the honest value is "unset"; the array length is the
// contract that lets the server match answers to its requests. Inventing
// settings would be worse than saying nothing is configured.
func TestConfigurationIsAnsweredWithNulls(t *testing.T) {
	ls := &langServer{}
	res, rerr := ls.handleServerRequest(&lsp.Message{
		Method: "workspace/configuration",
		Params: json.RawMessage(`{"items":[{"section":"gopls"},{"section":"editor"}]}`),
	})
	if rerr != nil {
		t.Fatalf("workspace/configuration refused: %v", rerr)
	}
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); got != "[null,null]" {
		t.Errorf("result = %s, want [null,null]", got)
	}
}

// A showMessageRequest is dismissed rather than left hanging. The handler
// returns no action item, which marshal turns into the protocol's null.
func TestShowMessageRequestIsDismissed(t *testing.T) {
	ls := &langServer{}
	res, rerr := ls.handleServerRequest(&lsp.Message{
		Method: "window/showMessageRequest",
		Params: json.RawMessage(`{"type":1,"message":"restart?","actions":[{"title":"yes"}]}`),
	})
	if rerr != nil {
		t.Fatalf("showMessageRequest refused: %v", rerr)
	}
	if res != nil {
		t.Errorf("result = %#v, want the nil dismissal", res)
	}
}

// A server-initiated edit is refused explicitly, so the server knows why it did
// not land rather than waiting on silence. applied:false is the protocol's own
// refusal shape, and the reason is what makes it actionable.
func TestApplyEditIsRefusedExplicitly(t *testing.T) {
	ls := &langServer{}
	res, rerr := ls.handleServerRequest(&lsp.Message{
		Method: "workspace/applyEdit",
		Params: json.RawMessage(`{"edit":{"changes":{}}}`),
	})
	if rerr != nil {
		t.Fatalf("applyEdit answered with an error: %v", rerr)
	}
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Applied       bool   `json:"applied"`
		FailureReason string `json:"failureReason"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("applyEdit result %s: %v", b, err)
	}
	if out.Applied {
		t.Error("a server-initiated edit was reported as applied")
	}
	if out.FailureReason == "" {
		t.Error("refusal carried no reason")
	}
}

// Registration changes what the workspace-symbol gate allows. The server that
// registers workspace/symbol after the handshake must be asked; one that never
// registered it still reads as unsupported, and an unregistration closes the
// gate again.
func TestDynamicWorkspaceSymbolRegistrationOpensTheGate(t *testing.T) {
	ls := &langServer{}
	if got := ls.capabilityGapMethod(nil, "workspace/symbol", "workspace symbols"); got == "" {
		t.Error("absent provider and no registration is not a gap")
	}
	ls.registerCapabilities(json.RawMessage(
		`{"registrations":[{"id":"1","method":"workspace/symbol"}]}`))
	if got := ls.capabilityGapMethod(nil, "workspace/symbol", "workspace symbols"); got != "" {
		t.Errorf("dynamically registered workspace/symbol was gated: %q", got)
	}
	ls.unregisterCapabilities(json.RawMessage(
		`{"unregisterations":[{"id":"1","method":"workspace/symbol"}]}`))
	if got := ls.capabilityGapMethod(nil, "workspace/symbol", "workspace symbols"); got == "" {
		t.Error("unregistration left the gate open")
	}
}

// An unknown server request gets method-not-found rather than an empty success,
// which is what a handler that fell through a switch would produce. The code is
// the same one the connection sends with no handler at all.
func TestUnknownServerRequestIsRefused(t *testing.T) {
	ls := &langServer{}
	_, rerr := ls.handleServerRequest(&lsp.Message{Method: "textDocument/futureFeature"})
	if rerr == nil || rerr.Code != -32601 {
		t.Errorf("error = %+v, want -32601", rerr)
	}
}

// showMessage is user-facing and always reaches the status line; logMessage is
// the log channel and only errors and warnings are worth the status line, since
// info/log chatter would overwrite what other features put there.
func TestServerMessagesHitTheStatusLine(t *testing.T) {
	h := newHarness(t, "package main\n")
	h.applyServerMessage(lsp.ServerMessage{
		Method: "window/showMessage", Type: lsp.MessageInfo, Message: "hello",
	})
	if got := h.Status(); got != "hello" {
		t.Errorf("showMessage status = %q, want hello", got)
	}
	h.applyServerMessage(lsp.ServerMessage{
		Method: "window/logMessage", Type: lsp.MessageInfo, Message: "chatter",
	})
	if got := h.Status(); got != "hello" {
		t.Errorf("log info overwrote the status line: %q", got)
	}
	h.applyServerMessage(lsp.ServerMessage{
		Method: "window/logMessage", Type: lsp.MessageWarning, Message: "careful",
	})
	if got := h.Status(); got != "careful" {
		t.Errorf("log warning status = %q, want careful", got)
	}
}

// Completion richness has to be announced or a server will not send it: the
// documentation format, the resolve properties, and the 3.16/3.17 item shapes
// the decoder reads.
func TestClientCapabilitiesAdvertiseCompletionRichness(t *testing.T) {
	caps := clientCapabilities()
	td, _ := caps["textDocument"].(map[string]any)
	comp, _ := td["completion"].(map[string]any)
	ci, _ := comp["completionItem"].(map[string]any)
	if ci == nil {
		t.Fatal("no completionItem capabilities")
	}
	if ci["snippetSupport"] != true {
		t.Errorf("snippetSupport = %v; the engine is not advertised", ci["snippetSupport"])
	}
	formats, _ := ci["documentationFormat"].([]string)
	if len(formats) == 0 || formats[0] != "markdown" {
		t.Errorf("documentationFormat = %v, want markdown first", formats)
	}
	rs, _ := ci["resolveSupport"].(map[string]any)
	if rs == nil {
		t.Fatal("resolveSupport is not advertised")
	}
	props, _ := rs["properties"].([]string)
	if len(props) == 0 || props[0] != "documentation" {
		t.Errorf("resolveSupport.properties = %v, want documentation", props)
	}
	if ci["labelDetailsSupport"] != true {
		t.Errorf("labelDetailsSupport = %v, want true", ci["labelDetailsSupport"])
	}
	if ci["insertReplaceSupport"] != true {
		t.Errorf("insertReplaceSupport = %v, want true", ci["insertReplaceSupport"])
	}
	list, _ := comp["completionList"].(map[string]any)
	defaults, _ := list["itemDefaults"].([]string)
	if len(defaults) == 0 {
		t.Errorf("completionList.itemDefaults = %v, want the applied defaults", defaults)
	}
}

// The popup shows the documentation a server sent with the item, through the
// hover panel's markdown renderer. Buffer words carry none, so this only
// appears for a server answer.
func TestCompletionDocumentationIsShown(t *testing.T) {
	h := newHarness(t, "handoff()\n\n")
	h.press("ctrl+g")
	h.typeText("2")
	h.press("enter")
	h.typeText("hand")
	parkAnswer(h, "hand", []lsp.CompletionItem{{
		Label:         "handleRequest",
		Insert:        "handleRequest",
		Documentation: "Does a thing.",
	}})
	// Draw exercises the popup's documentation renderer, so this covers the
	// path that places and wraps the panel and not only the text it holds.
	h.Draw()
	if got := h.Complete.DocText(); !strings.Contains(got, "Does a thing.") {
		t.Errorf("popup documentation = %q", got)
	}
}

// A completionItem/resolve answer is applied to the item it names. The item's
// data is the opaque key, so the answer finds the candidate and is memoised
// against a later request for the same item.
func TestResolvedCompletionDocumentationApplies(t *testing.T) {
	h := newHarness(t, "handoff()\n\n")
	h.press("ctrl+g")
	h.typeText("2")
	h.press("enter")
	h.typeText("hand")
	item := lsp.CompletionItem{
		Label:  "handleRequest",
		Insert: "handleRequest",
		Data:   json.RawMessage(`{"i":1}`),
	}
	parkAnswer(h, "hand", []lsp.CompletionItem{item})
	if got := h.Complete.DocText(); got != "" {
		t.Fatalf("setup: documentation already present: %q", got)
	}

	h.park(lspAnswer{
		kind: answerCompletionDoc, resolveKey: item.ResolveKey(),
		doc: "Resolved docs.",
	})
	h.applyAnswer()

	if got := h.Complete.DocText(); !strings.Contains(got, "Resolved docs.") {
		t.Errorf("documentation = %q, want the resolved text", got)
	}
	if doc := h.completionDocs[item.ResolveKey()]; doc != "Resolved docs." {
		t.Errorf("memo = %q, want the answer kept", doc)
	}
}

// A language with no override still runs the built-in command. Configuring a
// server is an addition, not a replacement, so an untouched install keeps
// gopls and a file type with no built-in and no override has none.
func TestLSPCommandFallsBackToBuiltIn(t *testing.T) {
	s := newServers([]string{"/w"})
	argv, ok := s.argvFor("go")
	if !ok || len(argv) != 1 || argv[0] != "gopls" {
		t.Errorf("argvFor(go) = %q, %v; want the built-in gopls", argv, ok)
	}
	if argv, ok := s.argvFor("java"); ok {
		t.Errorf("argvFor(java) = %q, %v; want no server", argv, ok)
	}
}

// for_ resolves through argvFor, so an installed resolver reaches the spawn
// decision. The language has no built-in command, so without the override the
// state would be serverNone; an empty PATH makes the overridden binary missing
// without starting anything.
func TestLSPCommandOverrideReachesFor(t *testing.T) {
	t.Setenv("PATH", "")
	s := newServers([]string{"/w"})
	s.resolveCommand = func(id string) ([]string, bool) {
		if id == "java" {
			return []string{"jdtls", "--stdio"}, true
		}
		return nil, false
	}
	if _, st := s.for_("/w/a.java", nil); st != serverMissing {
		t.Errorf("state = %d, want serverMissing for the overridden command", st)
	}
	if n := len(s.byID); n != 0 {
		t.Errorf("a missing override registered %d server(s), want none", n)
	}
}

// A disabled server is not a missing binary: for_ reports serverNone and
// registers nothing, so a settings override that disables a language never
// reaches the spawn attempt.
func TestLSPCommandDisabledReportsNone(t *testing.T) {
	t.Setenv("PATH", "")
	s := newServers([]string{"/w"})
	s.resolveCommand = func(id string) ([]string, bool) { return nil, false }
	if _, st := s.for_("/w/a.py", nil); st != serverNone {
		t.Errorf("state = %d, want serverNone for a disabled server", st)
	}
	if n := len(s.byID); n != 0 {
		t.Errorf("a disabled server registered %d entry(ies), want none", n)
	}
}

// stubHandshakeGopls puts a fake gopls on PATH that completes the initialize
// handshake and then stays alive, so for_ reaches the live state a real server
// would and running/live can be observed. stubGopls exits before the handshake,
// which is enough to read the registration but not liveness.
func stubHandshakeGopls(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	body := `{"jsonrpc":"2.0","id":1,"result":{"capabilities":{}}}`
	script := "#!/bin/sh\n" +
		"body='" + body + "'\n" +
		"printf 'Content-Length: %d\\r\\n\\r\\n%s' \"${#body}\" \"$body\"\n" +
		"cat >/dev/null\n"
	if err := os.WriteFile(filepath.Join(dir, "gopls"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// waitLiveServers waits for the handshakes for_ started to finish and returns
// the server running for each path. A bounded poll is the only way to observe
// the asynchronous start; the deadline fails the test rather than hanging it if
// the stub never answers.
func waitLiveServers(t *testing.T, h *harness, paths ...string) []*langServer {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		out := make([]*langServer, len(paths))
		ready := true
		for i, p := range paths {
			out[i] = h.servers.live(p)
			if out[i] == nil {
				ready = false
			}
		}
		if ready {
			return out
		}
		if time.Now().After(deadline) {
			t.Fatalf("language server handshake did not complete: %v", out)
		}
		time.Sleep(time.Millisecond)
	}
}

// A file under the second root is served by a server started in that root. The
// failure this pins: servers held one workspace root, so a file under root2 was
// answered by a server whose working directory and workspace URI were root1's.
func TestServerRootFollowsTheFilePath(t *testing.T) {
	stubGopls(t)
	h, _, rootB := newRootsHarness(t)
	path := filepath.Join(rootB, "b.go")
	if _, st := h.servers.for_(path, func() {}); st != serverStarting {
		t.Fatalf("for_(%s) state = %d, want serverStarting", path, st)
	}
	ls := h.servers.byID[serverKey{root: rootB, lang: "go"}]
	if ls == nil {
		t.Fatalf("no server keyed by root2; servers = %v", h.servers.byID)
	}
	if ls.srv.Dir != rootB {
		t.Errorf("server Dir = %q, want root2 %q", ls.srv.Dir, rootB)
	}
	if ls.root != rootB {
		t.Errorf("server root = %q, want root2 %q", ls.root, rootB)
	}
}

// Two roots with a Go file each get two Go servers, and running/live find the
// one for their own root. The failure this pins: a single byID["go"] handed the
// file under root2 root1's server, so root2's diagnostics, hints and hovers
// were answered from the wrong workspace.
func TestServersArePerRootForRunningAndLive(t *testing.T) {
	stubHandshakeGopls(t)
	h, rootA, rootB := newRootsHarness(t)
	t.Cleanup(h.servers.stopAll)
	pathA := filepath.Join(rootA, "a.go")
	pathB := filepath.Join(rootB, "b.go")
	h.servers.for_(pathA, func() {})
	h.servers.for_(pathB, func() {})

	live := waitLiveServers(t, h, pathA, pathB)
	lsA, lsB := live[0], live[1]
	if lsA == lsB {
		t.Fatal("one server was reused for two roots")
	}
	if lsA.srv.Dir != rootA {
		t.Errorf("root1 server Dir = %q, want %q", lsA.srv.Dir, rootA)
	}
	if lsB.srv.Dir != rootB {
		t.Errorf("root2 server Dir = %q, want %q", lsB.srv.Dir, rootB)
	}
	if got := h.servers.running(pathA); got != lsA {
		t.Errorf("running(root1) = %p, want root1's server %p", got, lsA)
	}
	if got := h.servers.running(pathB); got != lsB {
		t.Errorf("running(root2) = %p, want root2's server %p", got, lsB)
	}
	if n := len(h.servers.byID); n != 2 {
		t.Errorf("registered %d servers, want one per (root, language)", n)
	}
}

// stopAll reaches every root's server, not just the primary's. The failure this
// pins: quitting stopped the root1 server and left root2's subprocess running.
func TestStopAllStopsEveryRoot(t *testing.T) {
	stubHandshakeGopls(t)
	h, rootA, rootB := newRootsHarness(t)
	t.Cleanup(h.servers.stopAll)
	pathA := filepath.Join(rootA, "a.go")
	pathB := filepath.Join(rootB, "b.go")
	h.servers.for_(pathA, func() {})
	h.servers.for_(pathB, func() {})

	live := waitLiveServers(t, h, pathA, pathB)
	h.servers.stopAll()

	if n := len(h.servers.byID); n != 0 {
		t.Errorf("stopAll left %d entr(ies) in the table", n)
	}
	for i, ls := range live {
		// A stopped server refuses a further start, which is the observable
		// half of "this process is not coming back".
		if _, err := ls.srv.Start(context.Background(), "file:///x", nil); !errors.Is(err, lsp.ErrClosed) {
			t.Errorf("server %d started after stopAll: err = %v, want ErrClosed", i, err)
		}
	}
}

// WarmServers starts one server per (language, root). A two-root launch with a
// Go file in each must start two Go servers, while a second Go file under the
// same root shares the first. The failure this pins: the warm path took any one
// path of the language, so only the primary root got a server.
func TestWarmServersStartsOnePerLanguagePerRoot(t *testing.T) {
	stubGopls(t)
	h, rootA, rootB := newRootsHarness(t)
	extra := filepath.Join(rootA, "a2.go")
	if err := os.WriteFile(extra, []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.OpenFile(filepath.Join(rootA, "a.go"))
	h.OpenFile(extra)
	h.OpenFile(filepath.Join(rootB, "b.go"))

	h.WarmServers()

	if n := len(h.servers.byID); n != 2 {
		t.Fatalf("WarmServers registered %d servers, want one per (language, root); servers = %v", n, h.servers.byID)
	}
	for _, root := range []string{rootA, rootB} {
		ls := h.servers.byID[serverKey{root: root, lang: "go"}]
		if ls == nil {
			t.Errorf("no Go server for root %q", root)
			continue
		}
		if ls.srv.Dir != root {
			t.Errorf("server for %q has Dir %q, want its own root", root, ls.srv.Dir)
		}
	}
}

// One root keeps the old behaviour exactly: one server per language, with that
// root as the working directory, however many files of the language are open.
func TestWarmServersSingleRootIsOneServerPerLanguage(t *testing.T) {
	stubGopls(t)
	h := newWorkspace(t, 120, 30)
	root := h.primaryRoot()
	h.OpenFile(filepath.Join(root, "main.go"))
	h.OpenFile(filepath.Join(root, "pkg", "helper.go"))

	h.WarmServers()

	if n := len(h.servers.byID); n != 1 {
		t.Fatalf("WarmServers registered %d servers for one root, want 1", n)
	}
	ls := h.servers.byID[serverKey{root: root, lang: "go"}]
	if ls == nil {
		t.Fatalf("servers = %v, want the go server keyed by the one root", h.servers.byID)
	}
	if ls.srv.Dir != root {
		t.Errorf("server Dir = %q, want the one root %q", ls.srv.Dir, root)
	}
}
