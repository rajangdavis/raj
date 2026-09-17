package editor

import "raj/internal/syntax"

// Semantic tokens are the language server's colour overlay for a document: the
// typed runs the server knows and the local highlighter only guessed. This file
// is the renderer-facing model, so the editor never imports the language-server
// client; the application converts each decoded token into a span with a
// line-relative byte offset, exactly the coordinates the chroma highlighter
// uses.
//
// The set is an overlay, not a replacement. The renderer consults it first and
// falls back to Syntax where it has no span, so chroma keeps the base colour
// and the server only refines the tokens it has an opinion about.

// SemanticSet is one document's semantic-token spans, grouped by line and
// sorted by start offset within a line.
//
// It is mutated and read only on the event thread, like Hints and Lenses, so it
// needs no mutex. That is unlike the application's store, which async answers
// fill from another goroutine.
type SemanticSet struct {
	byLine map[int][]syntax.Span
}

// NewSemanticSet wraps a line-keyed map of overlay spans. A nil or empty map
// yields a nil set, which draws nothing rather than an empty overlay and is the
// state a server's "no tokens here" answer leaves behind.
func NewSemanticSet(byLine map[int][]syntax.Span) *SemanticSet {
	if len(byLine) == 0 {
		return nil
	}
	return &SemanticSet{byLine: byLine}
}

// At is the overlay spans covering a line, or nil when the line has none. A nil
// set is safe to read, which is what lets the renderer take the fast path for
// every line without a nil check of its own.
func (s *SemanticSet) At(line int) []syntax.Span {
	if s == nil {
		return nil
	}
	return s.byLine[line]
}

// SetSemantic installs an overlay on the file, replacing whatever was there. A
// nil set is an answer with no tokens and clears the overlay, exactly as
// ClearSemantic does.
func (f *File) SetSemantic(s *SemanticSet) { f.Semantic = s }

// ClearSemantic drops the overlay for the file, for an edit that has moved the
// bytes it was measured on, or for a server that stopped advertising semantic
// tokens. The next answer reinstalls it.
func (f *File) ClearSemantic() { f.Semantic = nil }
