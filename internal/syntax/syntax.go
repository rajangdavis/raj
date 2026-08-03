// Package syntax turns buffer text into per-line colour spans using chroma.
//
// The design constraint is that highlighting must never be on the keystroke
// path. Chroma tokenises whole documents, so raj tokenises once per document
// version, lazily, the first time a frame needs it — and skips the work
// entirely for files large enough that the cost would be felt.
package syntax

import (
	"strings"
	"sync"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"

	"raj/internal/ui"
)

// MaxSize is the largest document raj will highlight. Above it, tokenising
// after every edit costs more than the colour is worth, and plain text remains
// perfectly readable.
const MaxSize = 1 << 20

// Span is a run of one style within a line, in byte offsets from the line start.
type Span struct {
	Start, End int
	Style      ui.Style
}

// Highlighter holds the tokens for one document.
//
// Tokenising is asynchronous. Chroma costs on the order of 80 ms for a
// 500-line Go file, which is five frames — doing it inline would stall on every
// pause in typing. Instead an edit marks the cache stale, a background pass
// retokenises, and rendering uses whatever is currently available. Colours lag
// a keystroke or two behind the text, which is invisible in practice and far
// better than the editor freezing.
type Highlighter struct {
	lexer   chroma.Lexer
	dark    bool
	enabled bool

	mu      sync.Mutex
	lines   [][]Span
	stale   bool
	running bool
	pending string
}

// New picks a lexer from the file name. Colours come from the terminal's own
// 16-colour palette rather than from a chroma style, so raj inherits whatever
// theme Ghostty is configured with — change the terminal theme and the syntax
// colours follow, with no raj-side configuration at all.
//
// dark is retained for the cases where a palette index is genuinely a poor
// choice on one background.
func New(path string, dark bool) *Highlighter {
	h := &Highlighter{dark: dark}
	lexer := lexers.Match(path)
	if lexer == nil {
		return h // unknown language: no highlighting, not an error
	}
	h.lexer = chroma.Coalesce(lexer)
	h.enabled = true
	// Start stale so the first idle tick tokenises. Waiting for an edit meant a
	// file opened and never touched — anything reached through reopen-tab, for
	// instance — stayed uncoloured.
	h.stale = true
	return h
}

// Enabled reports whether this document is highlighted at all.
func (h *Highlighter) Enabled() bool { return h != nil && h.enabled }

// Invalidate marks the cached tokens stale. Called on every edit, so it does
// nothing but set a flag.
func (h *Highlighter) Invalidate() {
	if !h.Enabled() {
		return
	}
	h.mu.Lock()
	h.stale = true
	h.mu.Unlock()
}

// Ensure starts a background retokenise if the cache is stale and no pass is
// already running. Call it from the application's idle tick, never from the
// render path.
func (h *Highlighter) Ensure(text string) {
	if !h.Enabled() {
		return
	}
	h.mu.Lock()
	if !h.stale || h.running {
		// A newer edit during a running pass is recorded, so the pass that
		// finishes will immediately be followed by another with fresh text.
		h.pending = text
		h.mu.Unlock()
		return
	}
	h.stale, h.running = false, true
	h.mu.Unlock()

	go h.tokenise(text)
}

// Line returns the spans currently known for a line. It never blocks and never
// tokenises: a stale answer now beats a correct answer after a stall.
func (h *Highlighter) Line(n int) []Span {
	if !h.Enabled() {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if n < 0 || n >= len(h.lines) {
		return nil
	}
	return h.lines[n]
}

// Ready reports whether any tokens have been computed yet.
func (h *Highlighter) Ready() bool {
	if !h.Enabled() {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.lines != nil
}

// tokenise runs the lexer and slices its output into per-line spans. It runs on
// its own goroutine and touches shared state only at the end.
func (h *Highlighter) tokenise(text string) {
	defer h.finish()

	if len(text) > MaxSize {
		h.enabled = false
		return
	}
	it, err := h.lexer.Tokenise(nil, text)
	if err != nil {
		h.enabled = false
		return
	}
	var out [][]Span

	line, col := []Span{}, 0
	for _, tok := range it.Tokens() {
		st := h.styleFor(tok.Type)
		// A token may span newlines (comments, strings, whitespace), so each
		// segment between newlines becomes a span on its own line.
		for {
			i := strings.IndexByte(tok.Value, '\n')
			if i < 0 {
				break
			}
			if i > 0 {
				line = append(line, Span{col, col + i, st})
			}
			out = append(out, line)
			line, col = []Span{}, 0
			tok.Value = tok.Value[i+1:]
		}
		if tok.Value != "" {
			line = append(line, Span{col, col + len(tok.Value), st})
			col += len(tok.Value)
		}
	}
	out = append(out, line)

	h.mu.Lock()
	h.lines = out
	h.mu.Unlock()
}

// finish releases the running flag and picks up any edit that arrived mid-pass,
// so the cache converges rather than getting stuck one edit behind.
func (h *Highlighter) finish() {
	h.mu.Lock()
	h.running = false
	next := h.pending
	h.pending = ""
	stale := h.stale
	h.mu.Unlock()
	if next != "" || stale {
		h.Invalidate()
		if next != "" {
			h.Ensure(next)
		}
	}
}

// styleFor maps a chroma token type to one of the terminal's 16 palette
// colours.
//
// Naming RGB values would override the user's carefully chosen scheme and
// clash with terminal transparency; palette indices let Ghostty decide what
// "green" means. The cost is a coarser palette than a full chroma theme, which
// for code is barely noticeable — syntax highlighting needs about eight
// distinctions, not two hundred.
//
// The bright half (8-15) carries the load. Most themes make 1-6 close in
// luminance to the default foreground, so a highlighter built on them reads as
// flat; the bright slots are where the contrast lives. Identifiers keep the
// terminal foreground so the eye has a baseline to measure against.
func (h *Highlighter) styleFor(t chroma.TokenType) ui.Style {
	st := ui.DefaultStyle
	switch {
	case t.InCategory(chroma.Comment):
		return st.With(ui.Ansi(8)).Plus(ui.Italic) // dim, recedes
	case t.InCategory(chroma.LiteralString):
		return st.With(ui.Ansi(10)) // bright green
	case t.InCategory(chroma.LiteralNumber):
		return st.With(ui.Ansi(14)) // bright cyan
	case t.InCategory(chroma.Literal):
		return st.With(ui.Ansi(14))
	case t == chroma.KeywordType, t.InSubCategory(chroma.NameClass),
		t == chroma.NameNamespace, t == chroma.NameBuiltin, t == chroma.KeywordConstant:
		return st.With(ui.Ansi(11)) // bright yellow
	case t.InCategory(chroma.Keyword):
		return st.With(ui.Ansi(13)).Plus(ui.Bold) // bright magenta, the anchor
	case t == chroma.NameFunction, t == chroma.NameAttribute:
		return st.With(ui.Ansi(12)).Plus(ui.Bold) // bright blue
	case t == chroma.NameDecorator, t == chroma.NameLabel:
		return st.With(ui.Ansi(11))
	case t.InCategory(chroma.Operator), t.InCategory(chroma.Punctuation):
		return st.With(ui.Ansi(7)) // slightly muted against identifiers
	case t.InCategory(chroma.Error):
		return st.With(ui.Ansi(9)).Plus(ui.Bold) // bright red
	case t == chroma.GenericHeading, t == chroma.GenericSubheading:
		return st.With(ui.Ansi(12)).Plus(ui.Bold)
	case t == chroma.GenericEmph:
		return st.Plus(ui.Italic)
	case t == chroma.GenericStrong:
		return st.Plus(ui.Bold)
	}
	return st // identifiers keep the terminal foreground
}

// StyleAt resolves the style covering a byte offset within a line, falling back
// to the default where no token claims it.
func StyleAt(spans []Span, off int) (ui.Style, bool) {
	for _, s := range spans {
		if off >= s.Start && off < s.End {
			return s.Style, true
		}
	}
	return ui.DefaultStyle, false
}
