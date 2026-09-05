// Package syntax turns buffer text into per-line colour spans using chroma.
//
// The design constraint is that highlighting must never be on the keystroke
// path. Chroma tokenises whole documents — tens of milliseconds even for a
// small file — so raj tokenises off-thread, once per document version, and the
// renderer draws with whatever is cached.
//
// Everything awkward here follows from that one gap between the text on screen
// and the tokens describing it. Two rules close it:
//
//   - The cache is keyed by document version, never by a stale/fresh flag. A
//     flag cannot say WHICH text it went stale against, so a pass finishing
//     late could overwrite a newer result with an older one and nothing would
//     schedule a correction: the colours simply stayed wrong until the next
//     keystroke happened to kick the cache again.
//   - Edits are spliced into the cached spans as they arrive. Colours may be a
//     version behind, but they are never misaligned — press tab and the line's
//     colours move with the text instead of sitting a byte to the left.
package syntax

import (
	"sort"
	"strings"
	"sync"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"

	"raj/internal/ui"

	"raj/internal/safe"
)

// MaxSize is the largest document raj will highlight. Above it, tokenising
// after every edit costs more than the colour is worth, and plain text remains
// perfectly readable.
const MaxSize = 1 << 20

// Class is what kind of thing a span is, as opposed to what colour it is.
//
// It exists because more than one feature needs to know whether an offset is
// inside a string or a comment, and none of them should have to infer it from
// the colour. Bracket matching must not pair a brace in a comment with a real
// one; the symbol scanner must not report a `func` at the start of a line
// inside a block comment. Both are the same question, and the lexer has
// already answered it by the time anything asks.
//
// Deriving it from Style instead would tie every such feature to the palette:
// re-map comments to a different ANSI index and bracket matching would quietly
// start counting them.
type Class uint8

const (
	ClassCode Class = iota // anything the lexer did not call a string or a comment
	ClassString
	ClassComment
)

// Span is a run of one style within a line, in byte offsets from the line start.
type Span struct {
	Start, End int
	Style      ui.Style
	Class      Class
}

// edit is one text change, kept so it can be replayed onto the result of a pass
// that was already running when the change happened.
type edit struct {
	pos, del, ins int
	nl            []int  // document offsets of the line starts the insertion made
	ver           uint64 // document version after this edit
}

// Highlighter holds the tokens for one document.
type Highlighter struct {
	lexer chroma.Lexer
	dark  bool

	mu      sync.Mutex
	enabled bool

	lines  [][]Span // spans per line, offsets relative to the line start
	starts []int    // line starts, kept in step with the document by Edit
	have   bool     // lines and starts hold a real result
	lexVer uint64   // version the lexer actually saw
	ver    uint64   // version the cache is aligned to, after splicing

	running bool   // a tokenise pass is in flight
	runVer  uint64 // the version that pass is tokenising
	replay  []edit // edits made since that pass took its snapshot

	pendText string // newest text seen while a pass was running
	pendVer  uint64
	pending  bool
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
	return h
}

// Enabled reports whether this document is highlighted at all.
func (h *Highlighter) Enabled() bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.enabled
}

// Ensure starts a background retokenise unless the lexer has already seen this
// version. Call it after every edit: the work happens on its own goroutine, so
// starting it immediately costs nothing on the keystroke path.
//
// A fresh Highlighter has no cache at all, so the first call always tokenises,
// which is what colours a file that is opened and never touched.
func (h *Highlighter) Ensure(text string, ver uint64) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.enabled {
		return
	}
	if h.running {
		// Park the newest text; the pass in flight picks it up when it
		// finishes, so the cache converges instead of stopping an edit behind.
		if ver != h.runVer {
			h.pendText, h.pendVer, h.pending = text, ver, true
		}
		return
	}
	if h.have && h.lexVer == ver {
		return // the lexer has already seen exactly this text
	}
	h.start(text, ver)
}

// start launches a pass. The caller holds the lock.
func (h *Highlighter) start(text string, ver uint64) {
	h.running, h.runVer = true, ver
	h.pending, h.pendText = false, ""
	// Edits at or before this snapshot are already in `text`; only later ones
	// still need replaying onto the result.
	keep := h.replay[:0]
	for _, e := range h.replay {
		if e.ver > ver {
			keep = append(keep, e)
		}
	}
	h.replay = keep
	safe.Go(func() { h.tokenise(text, ver) })
}

// Line returns the spans currently known for a line. It never blocks and never
// tokenises: a stale answer now beats a correct answer after a stall.
func (h *Highlighter) Line(n int) []Span {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.enabled || n < 0 || n >= len(h.lines) {
		return nil
	}
	return h.lines[n]
}

// Ready reports whether any tokens have been computed yet.
func (h *Highlighter) Ready() bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.enabled && h.have
}

// Version is the document version the cached spans are aligned to, counting the
// edits spliced into them since the lexer last ran.
func (h *Highlighter) Version() uint64 {
	if h == nil {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.ver
}

// Edit keeps the cached spans aligned with the text after ins bytes replaced
// del bytes at pos, bringing the cache to version ver. nl holds the document
// offsets of the line starts the insertion created — one past each newline —
// which the line index already computes, so no text is materialised to find
// them.
//
// Splicing rather than invalidating is the point. A token's colour is usually
// still right after an edit; its position is not, and a position wrong by a tab
// is far more visible than a colour wrong for two frames. Where an edit lands
// inside a token the token simply grows, so typing in the middle of a string
// keeps the string colour.
func (h *Highlighter) Edit(pos, del, ins int, nl []int, ver uint64) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.enabled || (del == 0 && ins == 0) {
		return
	}
	if h.running {
		// The pass in flight lexed the text as it was before this edit, so its
		// result will need the same splice applied when it lands.
		h.replay = append(h.replay, edit{pos, del, ins, append([]int(nil), nl...), ver})
	}
	if !h.have {
		return
	}
	h.splice(edit{pos, del, ins, nl, ver})
}

// tokenise runs the lexer and installs the result. It runs on its own goroutine
// and touches shared state only at the end.
func (h *Highlighter) tokenise(text string, ver uint64) {
	lines, starts, ok := h.lex(text)

	h.mu.Lock()
	defer h.mu.Unlock()
	if !ok {
		h.enabled, h.running = false, false
		return
	}
	h.lines, h.starts = lines, starts
	h.have, h.lexVer, h.ver = true, ver, ver
	// Edits that arrived while the lexer was working describe text this result
	// has never seen. Replaying them realigns the snapshot with the document.
	for _, e := range h.replay {
		h.splice(e)
	}
	h.replay = h.replay[:0]

	h.running = false
	if h.pending {
		h.start(h.pendText, h.pendVer)
	}
}

// lex tokenises off-lock. It reports false if the document cannot or should not
// be highlighted.
func (h *Highlighter) lex(text string) ([][]Span, []int, bool) {
	if len(text) > MaxSize {
		return nil, nil, false
	}
	it, err := h.lexer.Tokenise(nil, text)
	if err != nil {
		return nil, nil, false
	}

	var out [][]Span
	line, col := []Span{}, 0
	for _, tok := range it.Tokens() {
		st := h.styleFor(tok.Type)
		cl := classOf(tok.Type)
		// A token may span newlines (comments, strings, whitespace), so each
		// segment between newlines becomes a span on its own line.
		for {
			i := strings.IndexByte(tok.Value, '\n')
			if i < 0 {
				break
			}
			if i > 0 {
				line = append(line, Span{Start: col, End: col + i, Style: st, Class: cl})
			}
			out = append(out, line)
			line, col = []Span{}, 0
			tok.Value = tok.Value[i+1:]
		}
		if tok.Value != "" {
			line = append(line, Span{Start: col, End: col + len(tok.Value), Style: st, Class: cl})
			col += len(tok.Value)
		}
	}
	out = append(out, line)

	// Line starts come from the text, not from the token stream. Several chroma
	// lexers append a trailing newline the document does not have, which would
	// leave the cache a line longer than the buffer and every later splice
	// working on the wrong line.
	starts := lineStarts(text)
	for len(out) < len(starts) {
		out = append(out, nil)
	}
	return out[:len(starts)], starts, true
}

func lineStarts(text string) []int {
	starts := []int{0}
	for i := 0; i < len(text); i++ {
		if text[i] == '\n' {
			starts = append(starts, i+1)
		}
	}
	return starts
}

// splice applies one edit to the cached lines and line starts. The caller holds
// the lock and has checked that a cache exists.
func (h *Highlighter) splice(e edit) {
	if e.del > 0 {
		h.cut(e.pos, e.del)
	}
	if e.ins > 0 {
		h.grow(e.pos, e.ins, e.nl)
	}
	h.ver = e.ver
}

// lineAt is the cache's own view of which line an offset falls on. It is
// deliberately not the editor's line index: during a replay the two disagree,
// because the cache is still catching up.
func (h *Highlighter) lineAt(off int) int {
	if off <= 0 {
		return 0
	}
	return sort.SearchInts(h.starts, off+1) - 1
}

// cut removes n bytes at pos, joining the lines the deletion spanned.
func (h *Highlighter) cut(pos, n int) {
	l0, l1 := h.lineAt(pos), h.lineAt(pos+n)
	c0, c1 := pos-h.starts[l0], pos+n-h.starts[l1]

	joined := append(clipTo(h.lines[l0], c0), clipFrom(h.lines[l1], c1, c0-c1)...)
	h.lines[l0] = joined
	if l1 > l0 {
		h.lines = append(h.lines[:l0+1], h.lines[l1+1:]...)
	}

	// The line starts that disappear are exactly those in (pos, pos+n], which
	// is the same rule the editor's line index uses.
	from := sort.SearchInts(h.starts, pos+1)
	to := sort.SearchInts(h.starts, pos+n+1)
	if to > from {
		h.starts = append(h.starts[:from], h.starts[to:]...)
	}
	for i := from; i < len(h.starts); i++ {
		h.starts[i] -= n
	}
}

// grow makes room for n bytes inserted at pos, splitting the line when the
// insertion contained newlines.
func (h *Highlighter) grow(pos, n int, nl []int) {
	l0 := h.lineAt(pos)
	c0 := pos - h.starts[l0]

	if len(nl) == 0 {
		h.lines[l0] = shiftFrom(h.lines[l0], c0, n)
	} else {
		// Text after the insertion point moves to a new last line, at the
		// column left by the bytes following the final newline.
		last := n - (nl[len(nl)-1] - pos)
		tail := clipFrom(h.lines[l0], c0, last-c0)
		h.lines[l0] = clipTo(h.lines[l0], c0)

		added := make([][]Span, len(nl))
		added[len(added)-1] = tail
		h.lines = append(h.lines, added...)
		copy(h.lines[l0+1+len(added):], h.lines[l0+1:len(h.lines)-len(added)])
		copy(h.lines[l0+1:], added)
	}

	first := sort.SearchInts(h.starts, pos+1)
	for i := first; i < len(h.starts); i++ {
		h.starts[i] += n
	}
	if len(nl) > 0 {
		h.starts = append(h.starts, nl...)
		copy(h.starts[first+len(nl):], h.starts[first:len(h.starts)-len(nl)])
		copy(h.starts[first:], nl)
	}
}

// clipTo keeps the part of a line's spans lying before column c.
func clipTo(spans []Span, c int) []Span {
	out := make([]Span, 0, len(spans))
	for _, s := range spans {
		if s.Start >= c {
			break
		}
		if s.End > c {
			s.End = c
		}
		out = append(out, s)
	}
	return out
}

// clipFrom keeps the part of a line's spans from column c on, rebased by delta.
func clipFrom(spans []Span, c, delta int) []Span {
	out := make([]Span, 0, len(spans))
	for _, s := range spans {
		if s.End <= c {
			continue
		}
		if s.Start < c {
			s.Start = c
		}
		s.Start += delta
		s.End += delta
		out = append(out, s)
	}
	return out
}

// shiftFrom moves spans aside for n bytes inserted at column c. A span that
// contains c grows instead, so text typed inside a token keeps that token's
// colour until the lexer says otherwise.
func shiftFrom(spans []Span, c, n int) []Span {
	out := make([]Span, 0, len(spans))
	for _, s := range spans {
		switch {
		case s.End <= c: // wholly before the insertion
		case s.Start >= c:
			s.Start += n
			s.End += n
		default:
			s.End += n
		}
		out = append(out, s)
	}
	return out
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

// classOf sorts a token into the three categories anything downstream cares
// about. Everything that is not a string or a comment is code, including
// whitespace and errors: a bracket in either is a real bracket.
//
// Character and escape literals fall under LiteralString in chroma, which is
// what we want — a brace inside '{' is not structural either.
func classOf(t chroma.TokenType) Class {
	switch {
	case t.InCategory(chroma.Comment):
		return ClassComment
	case t.InCategory(chroma.LiteralString):
		return ClassString
	}
	return ClassCode
}

// ClassAt is what kind of token covers a byte offset within a line.
//
// Offsets no span claims are code. An unclaimed offset means either that
// tokenising has not run yet or that the lexer emitted nothing there, and
// treating an unknown offset as a string would make bracket matching silently
// stop working on every freshly opened file.
func ClassAt(spans []Span, off int) Class {
	for _, s := range spans {
		if off >= s.Start && off < s.End {
			return s.Class
		}
	}
	return ClassCode
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
