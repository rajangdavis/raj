package lsp

import (
	"context"
	"encoding/json"
)

// Folding ranges. A server answers textDocument/foldingRange with the regions
// of one document a reader can collapse — a function body, a block, an import
// list. The ranges are named in lines, and the character offsets are optional:
// the specification permits a server to send a line-only range, and zero is a
// legal character, so presence has to be tracked separately from the value.
//
// This is a point-less document request like document links: there is no range
// parameter, so the whole file is the question and the client decides which
// range the caret is inside. The result is an array or null, and both empty
// forms mean "no folds", which is the normal answer for a file with none.

// FoldingRange is one region the server offers to fold. StartLine and EndLine
// are 0-based session lines. StartCharacter and EndCharacter are the optional
// ends within those lines: HasStartChar and HasEndChar are false when the
// server sent the line-only form, so a legitimate zero is not confused with
// absence. Kind names the region's shape ("comment", "imports", ...), or is
// empty when the server did not say; this client does not branch on it, but
// keeps it so a caller can.
type FoldingRange struct {
	StartLine      int
	EndLine        int
	StartCharacter int
	HasStartChar   bool
	EndCharacter   int
	HasEndChar     bool
	Kind           string
}

// wireFoldingRange is the optional-character form: each character end is a
// pointer so a missing field is distinguishable from an explicit zero.
type wireFoldingRange struct {
	StartLine      int    `json:"startLine"`
	EndLine        int    `json:"endLine"`
	StartCharacter *int   `json:"startCharacter"`
	EndCharacter   *int   `json:"endCharacter"`
	Kind           string `json:"kind"`
}

// RequestFoldingRanges asks for every foldable range in a document.
//
// The request names the document and nothing else; the protocol has no range
// parameter on it. The result is an array or null; both empty forms mean "no
// folds", which is the normal answer for most documents.
func RequestFoldingRanges(ctx context.Context, c *Conn, path string) ([]FoldingRange, error) {
	if c == nil {
		return nil, ErrClosed
	}
	params := map[string]any{"textDocument": map[string]any{"uri": URI(path)}}
	var raw json.RawMessage
	if err := c.Call(ctx, "textDocument/foldingRange", params, &raw); err != nil {
		return nil, err
	}
	return DecodeFoldingRanges(raw), nil
}

// DecodeFoldingRanges reads the array, or null. An entry whose end comes before
// its start is dropped rather than failing the whole answer, the same
// permissiveness the document-link and code-lens decoders apply: one malformed
// entry must not hide the ranges beside it. The remaining entries are kept in
// the order the server sent them.
func DecodeFoldingRanges(raw json.RawMessage) []FoldingRange {
	if isNull(raw) {
		return nil
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return nil
	}
	out := make([]FoldingRange, 0, len(items))
	for _, it := range items {
		fr, ok := decodeFoldingRange(it)
		if ok {
			out = append(out, fr)
		}
	}
	return out
}

// decodeFoldingRange reads one wire range. ok is false when the entry is null,
// malformed, or names an impossible range — a line before the document starts,
// or an end before its start — because such a range has no line to fold.
func decodeFoldingRange(raw json.RawMessage) (FoldingRange, bool) {
	if isNull(raw) {
		return FoldingRange{}, false
	}
	var w wireFoldingRange
	if json.Unmarshal(raw, &w) != nil {
		return FoldingRange{}, false
	}
	if w.StartLine < 0 || w.EndLine < w.StartLine {
		return FoldingRange{}, false
	}
	fr := FoldingRange{
		StartLine: w.StartLine,
		EndLine:   w.EndLine,
		Kind:      w.Kind,
	}
	if w.StartCharacter != nil {
		fr.StartCharacter = *w.StartCharacter
		fr.HasStartChar = true
	}
	if w.EndCharacter != nil {
		fr.EndCharacter = *w.EndCharacter
		fr.HasEndChar = true
	}
	return fr, true
}
