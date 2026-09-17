package lsp

import (
	"encoding/json"
	"testing"
)

// A hierarchical reply nests through children, and the tree must survive
// decoding: flattening it would put a method beside the type that declares it,
// and dropping the children would lose every nested declaration. The selection
// range (the identifier) is kept apart from the whole-declaration range, because
// that is the place a jump lands on.
func TestDocumentSymbolHierarchicalKeepsTheTree(t *testing.T) {
	const raw = `[
		{
			"name": "Cat",
			"detail": "type Cat struct",
			"kind": 23,
			"range": {"start": {"line": 4, "character": 0}, "end": {"line": 8, "character": 1}},
			"selectionRange": {"start": {"line": 4, "character": 5}, "end": {"line": 4, "character": 8}},
			"children": [
				{
					"name": "Name",
					"kind": 8,
					"range": {"start": {"line": 5, "character": 1}, "end": {"line": 5, "character": 13}},
					"selectionRange": {"start": {"line": 5, "character": 1}, "end": {"line": 5, "character": 5}}
				},
				{
					"name": "Meow",
					"kind": 6,
					"range": {"start": {"line": 7, "character": 0}, "end": {"line": 9, "character": 1}},
					"selectionRange": {"start": {"line": 7, "character": 9}, "end": {"line": 7, "character": 13}}
				}
			]
		},
		{
			"name": "main",
			"kind": 12,
			"range": {"start": {"line": 11, "character": 0}, "end": {"line": 14, "character": 1}},
			"selectionRange": {"start": {"line": 11, "character": 5}, "end": {"line": 11, "character": 9}}
		}
	]`
	syms, err := requestDocumentSymbols(t, "/w/a.go", raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(syms) != 2 {
		t.Fatalf("top level = %d symbols, want 2: %+v", len(syms), syms)
	}
	cat := syms[0]
	if cat.Name != "Cat" || cat.Kind.String() != "struct" {
		t.Errorf("first = %q/%q, want Cat/struct", cat.Name, cat.Kind)
	}
	if cat.Path != "/w/a.go" {
		t.Errorf("hierarchical path = %q, want the requested document", cat.Path)
	}
	if len(cat.Children) != 2 {
		t.Fatalf("Cat children = %d, want 2 (flattening the reply drops them)", len(cat.Children))
	}
	if cat.Children[1].Name != "Meow" || cat.Children[1].Kind.String() != "method" {
		t.Errorf("child[1] = %q/%q, want Meow/method", cat.Children[1].Name, cat.Children[1].Kind)
	}
	// The two ranges stay apart: the selection is the identifier (column 5),
	// the range is the whole declaration (column 0).
	if cat.SelectionRange.Start.Character != 5 || cat.Range.Start.Character != 0 {
		t.Errorf("ranges collapsed: selection %+v, range %+v", cat.SelectionRange, cat.Range)
	}
	if !cat.Children[1].HasRange || cat.Children[1].SelectionRange.Start.Character != 9 {
		t.Errorf("Meow selection = %+v, want character 9", cat.Children[1].SelectionRange)
	}
	if !cat.HasRange || cat.Range.Start.Line != 4 {
		t.Errorf("Cat range = %+v, want line 4", cat.Range)
	}
}

// A flat SymbolInformation reply has no children and carries a location; it
// must not be read as a hierarchical node (which would lose every place) and a
// symbol whose location names only the file must still be listed. The
// URI-without-a-range form is the same one the workspace-symbol decoder keeps,
// and this reuses that decoder rather than inventing a second reading.
func TestDocumentSymbolFlatSymbolInformation(t *testing.T) {
	const raw = `[
		{
			"name": "Reader",
			"kind": 11,
			"containerName": "bufio",
			"location": {
				"uri": "file:///w/a.go",
				"range": {"start": {"line": 8, "character": 5}, "end": {"line": 10, "character": 1}}
			}
		},
		{
			"name": "NoRange",
			"kind": 12,
			"location": {"uri": "file:///w/a.go"}
		}
	]`
	syms, err := requestDocumentSymbols(t, "/w/a.go", raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(syms) != 2 {
		t.Fatalf("got %d symbols, want 2", len(syms))
	}
	r := syms[0]
	if r.Name != "Reader" || r.Kind.String() != "interface" {
		t.Errorf("first = %q/%q, want Reader/interface", r.Name, r.Kind)
	}
	if !r.HasRange || r.Range.Start.Line != 8 || r.Range.Start.Character != 5 {
		t.Errorf("Reader place = %+v (has range %v), want 8:5", r.Range, r.HasRange)
	}
	if r.Container != "bufio" {
		t.Errorf("container = %q, want bufio", r.Container)
	}
	if r.Path != "/w/a.go" {
		t.Errorf("path = %q, want the location's document", r.Path)
	}
	if len(r.Children) != 0 {
		t.Errorf("flat symbol grew %d children", len(r.Children))
	}
	// The range-less symbol is kept with no place rather than dropped.
	if syms[1].Name != "NoRange" || syms[1].HasRange {
		t.Errorf("range-less symbol = %+v, want it listed with HasRange false", syms[1])
	}
}

// Both empty shapes are "nothing here", not an error: null is the protocol's
// short reply, and [] is a file that declares nothing. A malformed answer is
// likewise no symbols rather than a failure that breaks the editor, and a node
// with no name is skipped rather than listed as a blank row.
func TestDocumentSymbolEmptyReplies(t *testing.T) {
	for _, raw := range []string{"null", "[]", "not json", `[{"kind":12}]`} {
		syms, err := requestDocumentSymbols(t, "/w/a.go", raw)
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if len(syms) != 0 {
			t.Errorf("%s: got %d symbols, want 0", raw, len(syms))
		}
	}
}

// The request names the document by URI and asks the one method; a server that
// never sees the URI answers about the wrong file, or nothing.
func TestDocumentSymbolSendsTheDocumentURI(t *testing.T) {
	f := newFake(t)
	var got map[string]any
	f.on("textDocument/documentSymbol", func(m *Message) (any, *ResponseError) {
		json.Unmarshal(m.Params, &got)
		return json.RawMessage(`[]`), nil
	})
	ctx, cancel := ctx1s(t)
	defer cancel()
	if _, err := RequestDocumentSymbols(ctx, f.conn, "/w/a.go"); err != nil {
		t.Fatal(err)
	}
	td, ok := got["textDocument"].(map[string]any)
	if !ok || td["uri"] != URI("/w/a.go") {
		t.Errorf("textDocument = %v, want uri %q", got["textDocument"], URI("/w/a.go"))
	}
}

// requestDocumentSymbols drives RequestDocumentSymbols through a fake server
// that answers with raw, so a decode test does not depend on a real process.
func requestDocumentSymbols(t *testing.T, path, raw string) ([]DocumentSymbol, error) {
	t.Helper()
	f := newFake(t)
	f.on("textDocument/documentSymbol", func(*Message) (any, *ResponseError) {
		return json.RawMessage(raw), nil
	})
	ctx, cancel := ctx1s(t)
	defer cancel()
	return RequestDocumentSymbols(ctx, f.conn, path)
}
