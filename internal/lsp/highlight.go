package lsp

import (
	"context"
	"encoding/json"
)

// textDocument/documentHighlight. A server that advertises
// documentHighlightProvider answers with the ranges of every occurrence of the
// symbol at a position, each optionally tagged as a read or a write. It is a
// position query like hover or references, so it shares positionParams and its
// result is decoded permissively.
//
// The kind is advisory. The protocol defaults it to Text when a server omits
// it, and servers differ in how much they distinguish, so an absent or unknown
// number reads as Text rather than dropping the range: the range is the useful
// half, the emphasis is a refinement.

// DocumentHighlightKind is the protocol's documentHighlight kind number:
// 1 Text, 2 Read, 3 Write.
type DocumentHighlightKind int

const (
	// HighlightText is the default kind: the server names the occurrence but
	// does not say whether it reads or writes.
	HighlightText DocumentHighlightKind = 1
	// HighlightRead marks an occurrence that reads the symbol.
	HighlightRead DocumentHighlightKind = 2
	// HighlightWrite marks an occurrence that writes the symbol.
	HighlightWrite DocumentHighlightKind = 3
)

// DocumentHighlight is one occurrence of the symbol at the queried position.
type DocumentHighlight struct {
	Range Range
	Kind  DocumentHighlightKind
}

// wireHighlight is the response entry. Range is a pointer so an entry that
// omits it is recognisable and skipped rather than decoded as a zero range at
// the top of the file.
type wireHighlight struct {
	Range *Range `json:"range"`
	Kind  int    `json:"kind"`
}

// RequestDocumentHighlights asks for every occurrence of the symbol at a
// position in a document.
//
// A null result and an empty array both mean "no occurrences", the normal
// answer over whitespace or punctuation; neither is an error.
func RequestDocumentHighlights(ctx context.Context, c *Conn, path string, p Position) ([]DocumentHighlight, error) {
	if c == nil {
		return nil, ErrClosed
	}
	var raw json.RawMessage
	if err := c.Call(ctx, "textDocument/documentHighlight", positionParams(path, p), &raw); err != nil {
		return nil, err
	}
	return DecodeDocumentHighlights(raw), nil
}

// DecodeDocumentHighlights reads the array, or null. The specification says the
// result is an array, but a single object is accepted too because some servers
// send one when there is exactly one occurrence, and reading it as nothing
// would silently lose the only highlight there is.
//
// Entries are decoded one at a time so a malformed entry — no range, a range
// with equal ends, a kind of the wrong type — drops only itself; a whole-array
// decode would let one broken entry take the good ones with it. An absent or
// unknown kind reads as Text, the protocol's own default.
func DecodeDocumentHighlights(raw json.RawMessage) []DocumentHighlight {
	if isNull(raw) {
		return nil
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		items = []json.RawMessage{raw}
	}
	out := make([]DocumentHighlight, 0, len(items))
	for _, it := range items {
		if h, ok := decodeDocumentHighlight(it); ok {
			out = append(out, h)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// decodeDocumentHighlight reads one entry. It reports false for anything with
// no paintable range: a missing one would decode to the top of the file, and an
// empty one has no byte to emphasise.
func decodeDocumentHighlight(raw json.RawMessage) (DocumentHighlight, bool) {
	if isNull(raw) {
		return DocumentHighlight{}, false
	}
	var w wireHighlight
	if json.Unmarshal(raw, &w) != nil {
		return DocumentHighlight{}, false
	}
	if w.Range == nil || w.Range.Start == w.Range.End {
		return DocumentHighlight{}, false
	}
	return DocumentHighlight{Range: *w.Range, Kind: highlightKind(w.Kind)}, true
}

// highlightKind maps the wire number to a kind, defaulting anything unknown to
// Text.
func highlightKind(n int) DocumentHighlightKind {
	switch DocumentHighlightKind(n) {
	case HighlightRead:
		return HighlightRead
	case HighlightWrite:
		return HighlightWrite
	}
	return HighlightText
}
