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
	// prepare returns the haystack to sweep for one file's contents. A
	// case-insensitive matcher folds here, once per file, rather than once per
	// call. When a file cannot be folded in place it returns a matcher to run
	// line by line instead, so the fallback costs that one file rather than
	// the whole search.
	prepare(data []byte) (hay []byte, lineFallback matcher)
}

type regexMatcher struct{ re *regexp.Regexp }

func (m regexMatcher) prepare(data []byte) ([]byte, matcher) { return data, nil }

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
	pat  []byte // already folded when fold is true
	fold bool
	word bool
	// crossPlane records that the query contains k or s, the only ASCII
	// letters a non-ASCII rune folds to. It gates a check that would otherwise
	// run over every non-ASCII file for no reason.
	crossPlane bool
	fallback   *regexp.Regexp
	scratch    []byte
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
		foldLower(pat)
		m.fold = true
		m.crossPlane = bytes.ContainsAny(pat, "ks")
	}
	m.pat = pat
	return m
}

// prepare folds a whole file once. Folding inside find would refold the
// remaining bytes on every match, and would send an entire file to the regexp
// for one non-ASCII byte in it.
func (m *literalMatcher) prepare(data []byte) ([]byte, matcher) {
	if !m.fold {
		return data, nil
	}
	if cap(m.scratch) < len(data) {
		m.scratch = make([]byte, len(data)+1024)
	}
	hay := m.scratch[:len(data)]
	copy(hay, data)
	// foldLower leaves bytes above 0x7f alone, so a file with non-ASCII in it
	// is still swept rather than handed to the regexp. Offsets are preserved
	// because no byte changes width.
	if foldLower(hay) && m.crossPlane {
		// The file has non-ASCII AND the query contains a letter that some
		// non-ASCII rune folds to. Only then does byte-wise folding disagree
		// with (?i), and only then is the slow path worth taking.
		if bytes.Contains(data, kelvinSign) || bytes.Contains(data, longS) {
			return nil, regexMatcher{m.fallback}
		}
	}
	return hay, nil
}

// The only runes outside ASCII that case-fold to an ASCII letter: U+212A
// KELVIN SIGN folds to k, and U+017F LATIN SMALL LETTER LONG S folds to s.
// Byte-wise ASCII folding cannot find them, so a query containing k or s over
// a file containing one of them has to go through the regexp instead. Every
// other case-insensitive literal is exact.
var (
	kelvinSign = []byte("\u212A")
	longS      = []byte("\u017F")
)

func (m *literalMatcher) find(hay []byte) (int, int, bool) {
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

// foldLower lowercases A-Z in place, eight bytes at a time, leaving every byte
// above 0x7f exactly as it was, and reports whether any such byte was present.
//
// Leaving high bytes alone is what lets a file with non-ASCII in it be swept
// like any other: no byte changes value unless it is an ASCII capital, so match
// offsets still refer to the original text. An earlier version cleared the high
// bit before the range test, which mangled UTF-8 and forced every file with one
// accented character in it onto a much slower path.
//
// Go's compiler does not auto-vectorize, so this SWAR form is the portable
// stand-in: it is ~3x the naive byte loop and ~12x strings.ToLower. See
// BENCHMARKS.md.
func foldLower(b []byte) (sawHigh bool) {
	var hi uint64
	i := 0
	for ; i+8 <= len(b); i += 8 {
		w := binary.LittleEndian.Uint64(b[i:])
		hi |= w
		d := w &^ swarHigh
		ge := (d + (0x80-'A')*swarOnes) & swarHigh   // byte >= 'A'
		gt := (d + (0x80-'Z'-1)*swarOnes) & swarHigh // byte > 'Z'
		// Mask out any lane whose original high bit was set: those are UTF-8
		// continuation or lead bytes and must not be touched.
		up := ge &^ gt &^ w
		binary.LittleEndian.PutUint64(b[i:], w|(up>>2))
	}
	for ; i < len(b); i++ {
		c := b[i]
		hi |= uint64(c)
		if c-'A' < 26 { // unsigned wraparound: true only for 'A'..'Z'
			b[i] = c | 0x20
		}
	}
	return hi&swarHigh != 0
}
