package app

import (
	"encoding/json"
	"strings"
	"testing"

	"raj/internal/lsp"
)

const documentSymbolSrc = `package main

type Cat struct{}

func (c Cat) Meow() {}

func main() {
	c := Cat{}
	c.Meow()
}
`

// The capability is read from the initialize reply, with the same rule and
// voice every other provider uses: absent and false are both "does not support
// document symbols", and an options object is support. Without the gate the
// chord would ask a server that answers method-not-found and show a transport
// error where the scanner should have been consulted.
func TestDocumentSymbolCapabilityGate(t *testing.T) {
	if got := capabilityGap(nil, "document symbols"); got != "language server does not support document symbols" {
		t.Errorf("absent provider: gap = %q", got)
	}
	if got := capabilityGap(json.RawMessage(`false`), "document symbols"); got != "language server does not support document symbols" {
		t.Errorf("false provider: gap = %q", got)
	}
	if got := capabilityGap(json.RawMessage(`{"workDoneProgress":true}`), "document symbols"); got != "" {
		t.Errorf("advertised provider: gap = %q, want none", got)
	}
}

// canAnswerDocumentSymbols is the LSP-vs-scanner decision: a server that is not
// there, or is there without the capability, leaves the scanner in charge, and
// only one that advertised the provider takes the request. Reading the wrong
// capability here would send every symbol jump to a method the server refuses.
func TestCanAnswerDocumentSymbols(t *testing.T) {
	cases := []struct {
		name string
		ls   *langServer
		want bool
	}{
		{"no server", nil, false},
		{"server with no capabilities", &langServer{}, false},
		{"provider false", &langServer{caps: lsp.InitializeResult{Capabilities: lsp.ServerCapabilities{
			DocumentSymbolProvider: json.RawMessage(`false`),
		}}}, false},
		{"provider true", &langServer{caps: lsp.InitializeResult{Capabilities: lsp.ServerCapabilities{
			DocumentSymbolProvider: json.RawMessage(`true`),
		}}}, true},
		{"provider options", &langServer{caps: lsp.InitializeResult{Capabilities: lsp.ServerCapabilities{
			DocumentSymbolProvider: json.RawMessage(`{"workDoneProgress":true}`),
		}}}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := canAnswerDocumentSymbols(c.ls); got != c.want {
				t.Errorf("canAnswerDocumentSymbols = %v, want %v", got, c.want)
			}
		})
	}
}

// With a server registered but not live — a handshake still in flight, or a
// connection that has died — the chord must use the keyword scanner, the
// instant path. The entry advertises the capability, so a decision that ignored
// liveness would take the LSP path against a connection that is not there and
// open no overlay at all.
func TestGotoSymbolFallsBackToTheScannerWithoutALiveServer(t *testing.T) {
	h := newHarness(t, goSrc)
	id := lsp.LanguageID(h.Pane().File.Path)
	h.servers.byID[id] = &langServer{
		srv:  &lsp.Server{},
		sync: lsp.NewSync(nil, lsp.SyncFull),
		caps: lsp.InitializeResult{Capabilities: lsp.ServerCapabilities{
			DocumentSymbolProvider: json.RawMessage(`true`),
		}},
	}
	h.press("shift+super+o")
	if !h.Picker.Open {
		t.Fatal("the chord opened nothing")
	}
	if got := h.Picker.Results(); got != 4 {
		t.Errorf("listed %d symbols, want the scanner's 4", got)
	}
	if got := h.Picker.Top(); !strings.HasPrefix(got, "Cat") {
		t.Errorf("first row %q, want the scanner's Cat", got)
	}
}

// A document-symbol answer becomes picker rows, nested children indented under
// their parent and each row carrying the identifier's own line and column. A
// reply flattened into a flat list loses the nesting, and one that used the
// whole-declaration range would land the cursor on the `type` keyword rather
// than the name. Enter opens the chosen declaration through the same path a
// reference uses.
func TestDocumentSymbolsOpenThePicker(t *testing.T) {
	h := newHarness(t, documentSymbolSrc)
	path := h.Pane().File.Path
	h.lspGen = 1
	h.park(lspAnswer{
		gen:  1,
		kind: answerDocumentSymbols,
		file: path,
		docSyms: []lsp.DocumentSymbol{
			{
				Name: "Cat", Kind: 23, Path: path, HasRange: true,
				Range:          lsp.Range{Start: lsp.Position{Line: 2, Character: 0}},
				SelectionRange: lsp.Range{Start: lsp.Position{Line: 2, Character: 5}},
				Children: []lsp.DocumentSymbol{{
					Name: "Meow", Kind: 6, Path: path, HasRange: true,
					Range:          lsp.Range{Start: lsp.Position{Line: 4, Character: 0}},
					SelectionRange: lsp.Range{Start: lsp.Position{Line: 4, Character: 13}},
				}},
			},
			{
				Name: "main", Kind: 12, Path: path, HasRange: true,
				Range:          lsp.Range{Start: lsp.Position{Line: 6, Character: 0}},
				SelectionRange: lsp.Range{Start: lsp.Position{Line: 6, Character: 5}},
			},
		},
	})
	h.applyAnswer()

	if !h.Picker.Open || h.Focused() != FocusPicker {
		t.Fatal("document symbols did not open the picker")
	}
	if got := h.Picker.Results(); got != 3 {
		t.Fatalf("picker results = %d, want 3 (Cat, its child Meow, and main)", got)
	}
	// Files() is the rows in insertion order: parent, indented child, sibling.
	rows := h.Picker.Files()
	if len(rows) != 3 {
		t.Fatalf("rows = %q, want three", rows)
	}
	if !strings.HasPrefix(rows[0], "Cat") || !strings.Contains(rows[0], "struct") {
		t.Errorf("first row = %q, want Cat and its kind", rows[0])
	}
	if !strings.HasPrefix(rows[1], "  Meow") {
		t.Errorf("child row = %q, want it indented under Cat", rows[1])
	}
	if !strings.Contains(rows[1], "method") {
		t.Errorf("child row = %q, want its kind", rows[1])
	}
	h.press("enter") // Cat is first; its identifier is line 3, column 6
	if h.Picker.Open {
		t.Error("choosing a symbol left the overlay open")
	}
	line, col := cursorLine(h), cursorCol(h)
	if line != 3 || col != 6 {
		t.Errorf("cursor at %d:%d, want 3:6 (the identifier)", line, col)
	}
}

// No symbols is a word on the status line, not an empty overlay — the same
// answer the scanner gives, so the two sources behave alike when empty.
func TestDocumentSymbolsNotFound(t *testing.T) {
	h := newHarness(t, "package main\n")
	h.lspGen = 1
	h.park(lspAnswer{gen: 1, kind: answerDocumentSymbols})
	h.applyAnswer()
	if h.Picker.Open {
		t.Error("an empty answer opened the picker")
	}
	if !strings.Contains(h.Status(), "no symbols") {
		t.Errorf("status = %q", h.Status())
	}
}

// A stale document-symbol answer is dropped like any other position answer: the
// generation counter guards it so a reply for a chord the user has passed is
// not shown as though it answered the current one.
func TestDocumentSymbolsStaleAnswerDropped(t *testing.T) {
	h := newHarness(t, documentSymbolSrc)
	path := h.Pane().File.Path
	h.lspGen = 5
	h.park(lspAnswer{
		gen:  4,
		kind: answerDocumentSymbols,
		file: path,
		docSyms: []lsp.DocumentSymbol{{
			Name: "Cat", Kind: 23, Path: path, HasRange: true,
			SelectionRange: lsp.Range{Start: lsp.Position{Line: 2, Character: 5}},
		}},
	})
	h.applyAnswer()
	if h.Picker.Open {
		t.Error("a superseded answer opened the picker")
	}
}

// Raj reads hierarchy, so it tells the server so at initialize; a server that
// is not told sends the flat SymbolInformation shape instead, which loses the
// nesting the picker indents.
func TestDocumentSymbolCapabilityAsksForHierarchy(t *testing.T) {
	caps := clientCapabilities()
	td, _ := caps["textDocument"].(map[string]any)
	sym, ok := td["documentSymbol"].(map[string]any)
	if !ok {
		t.Fatalf("documentSymbol = %T, want an options object", td["documentSymbol"])
	}
	if sym["hierarchicalDocumentSymbolSupport"] != true {
		t.Errorf("documentSymbol = %v, want hierarchicalDocumentSymbolSupport true", sym)
	}
}
