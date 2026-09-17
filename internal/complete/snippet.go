package complete

import (
	"sort"
	"strings"
)

// Snippet expansion.
//
// A language server can answer with a snippet template instead of a word:
// `fmt.Errorf(${1:format})` names the text to insert and the places the caret
// should visit. Inserting the template verbatim would type `${1:format}` into
// the buffer, which is why snippets were refused outright before there was an
// engine. ParseSnippet is that engine's front half: it turns a template into
// the literal text to insert and the stops to visit, both measured in the text
// it returns, so the caller never re-derives an offset.
//
// The grammar is the subset completion needs, not the whole snippet language:
//
//	$1  ${1}  ${1:default}  ${1|a,b|}  $0   tab stops and placeholders
//	\$  \\  \}                              escapes
//	${nested ${2:placeholders}}             in a placeholder's default
//
// Anything else is literal text. A variable (`${TM_FILENAME}`), an unterminated
// construct, a choice with no closing `|` — each is copied through whole, and
// the parse resumes after it, so no input can panic or leave a half-expanded
// template in the buffer.

// TabStop is one stop in a parsed snippet. Offset and Length index the literal
// text ParseSnippet returned: a caller selects [Offset, Offset+Length) to show
// the placeholder's default. Length is zero for a bare stop (`$1`, `$0`).
type TabStop struct {
	Offset int
	Length int
}

// ParseSnippet expands an LSP snippet template into the literal text to insert
// and the tab stops to visit, in order.
//
// The stops are sorted the way a session visits them — by number, with $0 (or
// the implicit final stop) last — not the order they appear in the template, so
// a template that writes `$2 ... $1` still reaches the 1 before the 2. Offsets
// and lengths are byte positions in the returned text.
//
// The list is empty when the template names no stop at all: there is nothing to
// visit and the caret belongs at the end, as with any insert. When stops exist
// but the template never names $0, a zero-length final stop is appended at the
// end of the text so the caret has a resting place after the last placeholder.
func ParseSnippet(src string) (string, []TabStop) {
	p := &snippetParser{}
	p.parse(src, false)
	return p.text.String(), p.stops()
}

// snippetParser accumulates the literal text and the stops seen while scanning
// a template. The text builder is also the coordinate system: a stop's offset
// is the builder's length at the moment it is recorded.
type snippetParser struct {
	text  strings.Builder
	found []snippetStop
}

// snippetStop is a stop before it is ordered and trimmed to the public shape.
// index is the number written in the template; offset and length are positions
// in p.text.
type snippetStop struct {
	index  int
	offset int
	length int
}

// parse scans s, appending literal text and recording stops. When stopAtBrace,
// it returns at the first unescaped `}` that closes the enclosing construct
// rather than taking it as literal; the construct scanner has already found
// that brace, so a nested construct cannot end the scan early.
func (p *snippetParser) parse(s string, stopAtBrace bool) int {
	i := 0
	for i < len(s) {
		c := s[i]
		switch {
		case c == '\\' && i+1 < len(s) && isSnippetEscape(s[i+1]):
			p.text.WriteByte(s[i+1])
			i += 2
		case c == '}' && stopAtBrace:
			return i
		case c == '$':
			i += p.dollar(s[i:])
		default:
			p.text.WriteByte(c)
			i++
		}
	}
	return i
}

// isSnippetEscape reports the characters a backslash protects. The spec escapes
// only `$`, `}` and `\`; anything else keeps the backslash, which is how a
// Windows path in a template survives.
func isSnippetEscape(b byte) bool {
	return b == '$' || b == '\\' || b == '}'
}

// dollar consumes one `$...` construct and returns the bytes it consumed,
// appending its literal text and any stop it contains.
func (p *snippetParser) dollar(s string) int {
	if len(s) < 2 {
		p.text.WriteByte('$')
		return 1
	}
	if s[1] == '{' {
		return p.braced(s)
	}
	// `$N`: a bare tab stop. A `$` followed by anything that is not a digit is
	// not a construct this engine knows (a variable such as `$FOO`), so the `$`
	// is literal and the scan resumes after it.
	if n, width := digits(s[1:]); width > 0 {
		p.record(n, p.text.Len(), 0)
		return 1 + width
	}
	p.text.WriteByte('$')
	return 1
}

// braced consumes a `${...}` construct. The matching brace is found first, so
// the construct is either parsed whole or — when it is unterminated or its
// contents are not one of the known shapes — copied through whole. There is no
// path that appends part of a construct and drops the rest.
func (p *snippetParser) braced(s string) int {
	end := matchingBrace(s, 1)
	if end < 0 {
		p.text.WriteString(s)
		return len(s)
	}
	consumed := end + 1
	inner := s[2:end]
	n, width := digits(inner)
	switch {
	case width > 0 && width == len(inner):
		// ${N}
		p.record(n, p.text.Len(), 0)
	case width > 0 && inner[width] == ':':
		// ${N:default} — the default is itself a template, so it may carry
		// nested stops; they are recorded against the text being built.
		start := p.text.Len()
		p.parse(inner[width+1:], false)
		p.record(n, start, p.text.Len()-start)
	case width > 0 && inner[width] == '|' && len(inner) >= width+2 &&
		inner[len(inner)-1] == '|':
		// ${N|a,b|}. The engine has no chooser, so the first option becomes the
		// placeholder default and is selected like any other; the rest are
		// dropped rather than inserted.
		first := ""
		if opts := splitChoices(inner[width+1 : len(inner)-1]); len(opts) > 0 {
			first = opts[0]
		}
		start := p.text.Len()
		p.text.WriteString(first)
		p.record(n, start, len(first))
	default:
		// A variable or a malformed construct. Literal, whole.
		p.text.WriteString(s[:consumed])
	}
	return consumed
}

// matchingBrace returns the index of the `}` that closes the `{` at open, or -1
// when there is none. Only a `{` that follows a `$` opens a construct; a bare
// `{` is literal text and cannot swallow a following `}`.
func matchingBrace(s string, open int) int {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++ // the escaped byte can neither open nor close a construct
		case '{':
			if i > 0 && s[i-1] == '$' {
				depth++
			}
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// digits reads a run of decimal digits and returns its value and byte width.
// The value is clamped: it only orders stops, and an overflowed int would order
// them unpredictably.
func digits(s string) (int, int) {
	const limit = 1 << 30
	n, i := 0, 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		if n < limit {
			n = n*10 + int(s[i]-'0')
		}
		i++
	}
	if n > limit {
		n = limit
	}
	return n, i
}

// splitChoices splits a choice construct's body on unescaped commas. `\,` and
// `\|` inside an option are unescaped, since a comma or pipe in the option text
// has to be written escaped.
func splitChoices(s string) []string {
	var out []string
	var b strings.Builder
	escaped := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if escaped {
			b.WriteByte(c)
			escaped = false
			continue
		}
		switch c {
		case '\\':
			escaped = true
		case ',':
			out = append(out, b.String())
			b.Reset()
		default:
			b.WriteByte(c)
		}
	}
	return append(out, b.String())
}

// record notes a stop at the current end of the literal text.
func (p *snippetParser) record(index, offset, length int) {
	p.found = append(p.found, snippetStop{index: index, offset: offset, length: length})
}

// stops orders the recorded stops for visiting and adds the implicit final one.
func (p *snippetParser) stops() []TabStop {
	if len(p.found) == 0 {
		return nil
	}
	found := p.found
	sort.SliceStable(found, func(i, j int) bool {
		a, b := found[i].index, found[j].index
		if a == b {
			return false
		}
		if a == 0 {
			return false // $0 is always last
		}
		if b == 0 {
			return true
		}
		return a < b
	})
	out := make([]TabStop, 0, len(found)+1)
	zero := false
	for _, s := range found {
		if s.index == 0 {
			zero = true
		}
		out = append(out, TabStop{Offset: s.offset, Length: s.length})
	}
	if !zero {
		// The caret rests at the end only when the last stop is not already
		// there; a template ending in `$1` needs no duplicate final stop.
		if last := out[len(out)-1]; last.Offset+last.Length < p.text.Len() {
			out = append(out, TabStop{Offset: p.text.Len()})
		}
	}
	return out
}
