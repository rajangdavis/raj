package editor

// Document highlights are the language server's answer to "where else does the
// symbol under the caret appear". They are not colours: the semantic overlay
// already owns the token colour, and a highlight has to layer over it as a
// background so the occurrence is visible without hiding what the token is.
//
// The set is caret-dependent, which is the difference from SemanticSet. It
// carries the caret and document version it was measured at, and the renderer
// reads it only while those still match. A highlight for a caret that has moved
// is not a slightly wrong emphasis, it is an emphasis on the wrong word, so it
// is dropped at paint time rather than trusting the application to clear it
// between the move and the next answer.

// HighlightRun is one highlighted byte range within a line. Write records
// whether the server called the occurrence a write, so the renderer can give it
// the stronger emphasis; a read, or an occurrence the server did not classify,
// uses the read emphasis.
type HighlightRun struct {
	Start, End int // line-relative bytes, end-exclusive
	Write      bool
}

// HighlightSet is one document's document-highlight ranges, grouped by line and
// sorted by start offset within a line, together with the caret and document
// version the server answered for.
//
// It is installed and read only on the event thread, like SemanticSet, so it
// needs no mutex.
type HighlightSet struct {
	byLine  map[int][]HighlightRun
	version int
	caret   int
}

// NewHighlightSet wraps a line-keyed map of highlight runs and the request
// coordinates they describe. A nil or empty map yields a nil set, which draws
// nothing rather than an empty overlay and is what a server's "no occurrences"
// answer leaves behind.
func NewHighlightSet(byLine map[int][]HighlightRun, version, caret int) *HighlightSet {
	if len(byLine) == 0 {
		return nil
	}
	return &HighlightSet{byLine: byLine, version: version, caret: caret}
}

// At is the runs covering a line, or nil when the line has none. A nil set is
// safe to read, which is what lets the renderer take the fast path for every
// line without a nil check of its own.
func (s *HighlightSet) At(line int) []HighlightRun {
	if s == nil {
		return nil
	}
	return s.byLine[line]
}

// Live reports whether the set still describes the document and caret in front
// of the renderer. A false answer means the text moved or the caret did, so the
// set must not paint.
func (s *HighlightSet) Live(version, caret int) bool {
	return s != nil && s.version == version && s.caret == caret
}

// SetHighlight installs an overlay on the file, replacing whatever was there. A
// nil set is an answer with no occurrences and clears the overlay, exactly as
// ClearHighlight does.
func (f *File) SetHighlight(s *HighlightSet) { f.Highlight = s }

// ClearHighlight drops the overlay for the file, for an edit or caret move that
// has moved the bytes it was measured on, or for a server that stopped
// advertising document highlights. The next answer reinstalls it.
func (f *File) ClearHighlight() { f.Highlight = nil }
