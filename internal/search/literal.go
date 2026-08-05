package search

import (
	"bytes"
	"encoding/binary"
	"regexp"
)

// A matcher finds the first hit in one line. Both implementations return byte
// offsets into the line exactly as regexp.FindIndex does, so scan does not care
// which one it holds.
type matcher interface {
	find(line []byte) (start, end int, ok bool)
}

type regexMatcher struct{ re *regexp.Regexp }

func (m regexMatcher) find(line []byte) (int, int, bool) {
	loc := m.re.FindIndex(line)
	if loc == nil {
		return 0, 0, false
	}
	return loc[0], loc[1], true
}

// literalMatcher answers a plain substring query with bytes.Index, which is
// hand-written SIMD assembly in the standard library, instead of building a
// regexp program for text that contains no metacharacters.
//
// Case-insensitive queries fold the line into a scratch buffer first. The fold
// is ASCII-only, so any line containing a non-ASCII byte is handed to the
// regexp fallback — that keeps (?i) Unicode semantics exactly, at the cost of
// the slow path on the small fraction of source lines that need it.
type literalMatcher struct {
	pat      []byte // already folded when fold is true
	fold     bool
	word     bool
	fallback *regexp.Regexp
	scratch  []byte
}

// newMatcher picks the fast path when the query is a plain literal whose
// semantics bytes.Index can reproduce exactly, and the regexp otherwise.
func newMatcher(q Query, re *regexp.Regexp) matcher {
	if q.Regex || q.Text == "" {
		return regexMatcher{re}
	}
	pat := []byte(q.Text)
	// \b is defined against ASCII word bytes, so a literal whose own first or
	// last byte is not a word byte inverts the boundary test. Rare, and not
	// worth reproducing by hand.
	if q.Word && (!isWordByte(pat[0]) || !isWordByte(pat[len(pat)-1])) {
		return regexMatcher{re}
	}
	m := &literalMatcher{word: q.Word, fallback: re}
	if !q.Case {
		if !isASCII(pat) {
			return regexMatcher{re} // non-ASCII query: let regexp fold it
		}
		foldASCII(pat)
		m.fold = true
	}
	m.pat = pat
	return m
}

func (m *literalMatcher) find(line []byte) (int, int, bool) {
	hay := line
	if m.fold {
		if cap(m.scratch) < len(line) {
			m.scratch = make([]byte, len(line)+64)
		}
		hay = m.scratch[:len(line)]
		copy(hay, line)
		if !foldASCII(hay) {
			return regexMatcher{m.fallback}.find(line) // non-ASCII line
		}
	}
	from := 0
	for {
		i := bytes.Index(hay[from:], m.pat)
		if i < 0 {
			return 0, 0, false
		}
		start := from + i
		end := start + len(m.pat)
		if !m.word || isWordBoundary(hay, start, end) {
			return start, end, true
		}
		from = start + 1
		if from > len(hay)-len(m.pat) {
			return 0, 0, false
		}
	}
}

func isWordByte(c byte) bool {
	return c == '_' || (c >= '0' && c <= '9') || (c|0x20 >= 'a' && c|0x20 <= 'z')
}

func isWordBoundary(line []byte, start, end int) bool {
	if start > 0 && isWordByte(line[start-1]) {
		return false
	}
	if end < len(line) && isWordByte(line[end]) {
		return false
	}
	return true
}

const (
	swarOnes = 0x0101010101010101
	swarHigh = 0x8080808080808080
)

func isASCII(b []byte) bool {
	var hi uint64
	i := 0
	for ; i+8 <= len(b); i += 8 {
		hi |= binary.LittleEndian.Uint64(b[i:])
	}
	for ; i < len(b); i++ {
		hi |= uint64(b[i])
	}
	return hi&swarHigh == 0
}

// foldASCII lowercases A-Z in place, eight bytes at a time, and reports whether
// the input was pure ASCII. When it returns false the buffer has been mangled
// and the caller must use another path.
//
// Go's compiler does not auto-vectorize, so this SWAR form is the portable
// stand-in: it is ~10x the naive byte loop. See BENCHMARKS.md.
func foldASCII(b []byte) bool {
	var hi uint64
	i := 0
	for ; i+8 <= len(b); i += 8 {
		w := binary.LittleEndian.Uint64(b[i:])
		hi |= w
		d := w &^ swarHigh
		ge := (d + (0x80-'A')*swarOnes) & swarHigh   // byte >= 'A'
		gt := (d + (0x80-'Z'-1)*swarOnes) & swarHigh // byte > 'Z'
		binary.LittleEndian.PutUint64(b[i:], w|((ge&^gt)>>2))
	}
	for ; i < len(b); i++ {
		c := b[i]
		hi |= uint64(c)
		if c-'A' < 26 { // unsigned wraparound: true only for 'A'..'Z'
			b[i] = c | 0x20
		}
	}
	return hi&swarHigh == 0
}
