package editor

import (
	"raj/internal/ui"
	"raj/internal/view"
)

// Inlay hints. A hint is text the language server wants shown inline without
// being part of the document: a parameter name before an argument, an inferred
// type after a declaration.
//
// This is the renderer-facing model, not the protocol one, and it lives in the
// editor package so the editor never imports the language-server client. The
// application converts each lsp.InlayHint into a Hint when it installs an
// answer, resolving the server's line and character position to a byte offset
// and each text edit to absolute document offsets.

// HintEdit is one of the corrections attached to a hint, as absolute byte
// offsets in the document.
//
// Absolute rather than line-relative because the apply path works on the whole
// document and an edit can span lines. Installing a hint does not change the
// text, so the offsets stay valid until the next edit, which clears the hints.
type HintEdit struct {
	Start int
	End   int
	Text  string
}

// Hint is one hint to draw inline.
//
// Off is the anchor byte offset measured within its line: the renderer places
// the hint in the line's display columns, and a line-relative offset is what
// survives being handed one line's text. Edits are absolute for the reason
// above. The two are deliberately different coordinate systems.
type Hint struct {
	Off     int
	Text    string
	Left    bool
	Right   bool
	Kind    int
	Tooltip string
	Edits   []HintEdit
}

// LineHint is one hint and the line it is anchored on.
//
// A Hint's Off is measured within its line, so a slice of hints for a whole
// document cannot recover which line each belongs to. The application's store
// keeps pairs for exactly that reason: an answer is converted on one goroutine
// and installed on another, and the line has to survive the trip without being
// re-derived from text that may have changed.
type LineHint struct {
	Line int
	Hint Hint
}

// HintSet is the hints for one document, grouped by line and sorted by offset
// within a line.
//
// It is mutated and read only on the event thread, where installs and edits
// happen, so it needs no mutex. That is unlike the application's store, which
// async answers fill from another goroutine.
type HintSet struct {
	byLine map[int][]Hint
}

// Add appends a hint to a line, keeping the line's hints ordered by Off. Ties
// keep insertion order, which is what the server sent and is stable enough for
// two hints anchored at the same offset.
func (h *HintSet) Add(line int, hint Hint) {
	if h.byLine == nil {
		h.byLine = map[int][]Hint{}
	}
	lineHints := h.byLine[line]
	// Sorted insertion. A line holds a handful of hints at most, so the linear
	// scan costs less than a sort on every call.
	i := len(lineHints)
	for i > 0 && lineHints[i-1].Off > hint.Off {
		i--
	}
	// Grow and shift, so the hints after the insertion point keep their order.
	lineHints = append(lineHints, Hint{})
	copy(lineHints[i+1:], lineHints[i:])
	lineHints[i] = hint
	h.byLine[line] = lineHints
}

// At is the hints anchored on a line, or nil when the line has none.
//
// The nil answer is the common case — most lines have no hint — and it is the
// fast path the renderer takes for every line it draws. It is also what lets a
// nil set be read without a check at the call site.
func (h *HintSet) At(line int) []Hint {
	if h == nil {
		return nil
	}
	return h.byLine[line]
}

// Len is how many hints the set holds across every line.
func (h *HintSet) Len() int {
	if h == nil {
		return 0
	}
	n := 0
	for _, lineHints := range h.byLine {
		n += len(lineHints)
	}
	return n
}

// Empty reports whether the set holds no hints. A nil set is empty.
func (h *HintSet) Empty() bool { return h.Len() == 0 }

// Width is the display width the hint occupies, padding included: one column
// for each padding flag plus the display width of the text. It is RuneWidth,
// not len, because hint text may contain wide characters.
func (h Hint) Width() int {
	w := 0
	for _, r := range h.Text {
		w += ui.RuneWidth(r)
	}
	if h.Left {
		w++
	}
	if h.Right {
		w++
	}
	return w
}

// HintCols converts a line's hints into the view-only column form the layout
// code walks: the line-relative anchor and the display width, nothing else.
// A nil or empty slice returns nil, which is what keeps an empty hint set
// byte-identical to no hints at all.
func HintCols(hints []Hint) []view.HintCol {
	if len(hints) == 0 {
		return nil
	}
	cols := make([]view.HintCol, len(hints))
	for i, h := range hints {
		cols[i] = view.HintCol{Off: h.Off, Width: h.Width()}
	}
	return cols
}

// HintsThatFit builds the hint set for a file from an answer, keeping only the
// hints on lines whose whole line, hints included, fits in width display
// columns. A line that does not fit contributes none of its hints: a hint that
// would fit by itself is still anchored to a line the pane cannot show in one
// row, and a half-kept set is a column map that disagrees with the renderer.
//
// A width below one means the pane has not been laid out yet, so every hint is
// kept and the width-aware refilter at the next frame trims them.
func HintsThatFit(f *File, lineHints []LineHint, width int) *HintSet {
	if len(lineHints) == 0 {
		return nil
	}
	byLine := map[int][]Hint{}
	for _, lh := range lineHints {
		byLine[lh.Line] = append(byLine[lh.Line], lh.Hint)
	}
	var set HintSet
	for line, hs := range byLine {
		if width >= 1 && f.Cols.WidthHints(f.Line(line), HintCols(hs)) > width {
			continue
		}
		for _, h := range hs {
			set.Add(line, h)
		}
	}
	if set.Empty() {
		return nil
	}
	return &set
}
