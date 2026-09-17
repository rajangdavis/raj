package lsp

import (
	"context"
	"encoding/json"
)

// Document symbols. textDocument/documentSymbol answers with the declarations
// of one file, and the specification gives that answer two legal shapes: a
// hierarchical DocumentSymbol[] whose members nest through children, and a flat
// SymbolInformation[] whose members carry a location. A server picks one, so
// both are decoded here into a single tree — a caller that could not tell them
// apart would flatten a nested outline into nonsense or drop a flat reply
// entirely.

// DocumentSymbol is one declaration in a file: a name, a kind, and where it is.
//
// The two wire shapes differ in how much of that they carry. A hierarchical
// DocumentSymbol separates the whole declaration (Range) from the identifier
// (SelectionRange) and nests children; a flat SymbolInformation names a
// Location and has no children. Both arrive here as the same type: a flat
// symbol is a childless node, and its location's range stands in for both
// ranges. Path is the document the symbol is in.
type DocumentSymbol struct {
	Name   string
	Detail string
	Kind   SymbolKind
	// Container is the SymbolInformation containerName, which is the only
	// nesting a flat reply carries. A hierarchical reply names the parent by
	// position in Children instead, and normally leaves this empty.
	Container string
	// Range is the whole declaration including its doc comment; SelectionRange
	// is the identifier itself. A jump goes to the selection, because that is
	// what "where is this declaration" means.
	Range          Range
	SelectionRange Range
	// Children is the nested list a hierarchical reply sent, empty for a flat
	// symbol and for a leaf.
	Children []DocumentSymbol
	// Path is the document the symbol is in. The request names one document, so
	// a hierarchical symbol always has that path; a flat symbol takes the path
	// its location names.
	Path string
	// HasRange is false for a symbol the server named without a range — the
	// specification's Location | {uri} form, which a flat reply may use. Such a
	// symbol is listed, with no place to jump to, rather than dropped.
	HasRange bool
}

// wireDocumentSymbol is the union of the two result shapes. A hierarchical
// DocumentSymbol fills Range, SelectionRange and Children; a flat
// SymbolInformation fills Location and ContainerName. Location is raw because a
// flat location may name a file with or without a range — the same permissive
// reading the workspace-symbol decoder already does through symbolLocation.
type wireDocumentSymbol struct {
	Name           string               `json:"name"`
	Detail         string               `json:"detail"`
	Kind           int                  `json:"kind"`
	Range          *Range               `json:"range"`
	SelectionRange *Range               `json:"selectionRange"`
	Children       []wireDocumentSymbol `json:"children"`
	ContainerName  string               `json:"containerName"`
	Location       json.RawMessage      `json:"location"`
}

// RequestDocumentSymbols asks a server for one document's declarations.
//
// The result may be a hierarchical array, a flat array, or null. An empty
// answer is "this file declares nothing this server can name", which is normal
// for an empty file and not an error.
func RequestDocumentSymbols(ctx context.Context, c *Conn, path string) ([]DocumentSymbol, error) {
	if c == nil {
		return nil, ErrClosed
	}
	var raw json.RawMessage
	err := c.Call(ctx, "textDocument/documentSymbol",
		map[string]any{"textDocument": map[string]any{"uri": URI(path)}}, &raw)
	if err != nil {
		return nil, err
	}
	return decodeDocumentSymbols(raw, path), nil
}

// decodeDocumentSymbols reads the array, or null. The two shapes are told apart
// per element rather than once for the whole array: the specification says a
// server picks one, but a tolerant reader costs nothing and a server that mixed
// them would otherwise lose half its answer. A hierarchical element has no
// location; a flat one has a location and no children, so the presence of a
// location decides.
func decodeDocumentSymbols(raw json.RawMessage, path string) []DocumentSymbol {
	if isNull(raw) {
		return nil
	}
	var many []wireDocumentSymbol
	if json.Unmarshal(raw, &many) != nil {
		return nil
	}
	out := make([]DocumentSymbol, 0, len(many))
	for _, w := range many {
		if sym, ok := w.toDocumentSymbol(path); ok {
			out = append(out, sym)
		}
	}
	return out
}

func (w wireDocumentSymbol) toDocumentSymbol(path string) (DocumentSymbol, bool) {
	if w.Name == "" {
		return DocumentSymbol{}, false
	}
	// A location is what makes a flat SymbolInformation flat. symbolLocation is
	// the workspace-symbol decoder's reading of that shape, including the
	// URI-without-a-range form; it is reused rather than copied so the two
	// decoders cannot drift apart.
	if !isNull(w.Location) {
		loc, hasRange := symbolLocation(w.Location)
		if loc.Path == "" {
			// A location object that named no document: the request names one,
			// so that is where the symbol is.
			loc.Path = path
		}
		return DocumentSymbol{
			Name:           w.Name,
			Kind:           SymbolKind(w.Kind),
			Container:      w.ContainerName,
			Range:          loc.Range,
			SelectionRange: loc.Range,
			Path:           loc.Path,
			HasRange:       hasRange,
		}, true
	}
	ds := DocumentSymbol{
		Name:      w.Name,
		Detail:    w.Detail,
		Kind:      SymbolKind(w.Kind),
		Container: w.ContainerName,
		Path:      path,
	}
	// Prefer the identifier (selectionRange) as the jump target and keep the
	// whole declaration in Range when the server sent both; a server that sent
	// only one makes that one stand for both.
	switch {
	case w.SelectionRange != nil:
		ds.SelectionRange, ds.HasRange = *w.SelectionRange, true
		if w.Range != nil {
			ds.Range = *w.Range
		} else {
			ds.Range = *w.SelectionRange
		}
	case w.Range != nil:
		ds.Range, ds.SelectionRange, ds.HasRange = *w.Range, *w.Range, true
	}
	for _, ch := range w.Children {
		if child, ok := ch.toDocumentSymbol(path); ok {
			ds.Children = append(ds.Children, child)
		}
	}
	return ds, true
}
