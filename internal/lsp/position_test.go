package lsp

import (
	"strings"
	"testing"
	"unicode/utf16"
	"unicode/utf8"
)

// The disagreement in one table. ASCII agrees, everything else does not, and
// this is where a jump to the wrong column comes from.
func TestToUTF16(t *testing.T) {
	cases := []struct {
		line string
		byte int
		want int
	}{
		{"hello", 0, 0},
		{"hello", 5, 5},
		{"héllo", 3, 2}, // é is two bytes, one unit
		{"日本語", 3, 1},   // each CJK character is three bytes, one unit
		{"日本語", 9, 3},
		{"a😀b", 1, 1}, // an emoji is four bytes and two units
		{"a😀b", 5, 3},
		{"a😀b", 6, 4},
		{"", 0, 0},
		{"abc", 99, 3}, // past the end clamps
		{"abc", -1, 0},
		{"日本語", 2, 0}, // mid-rune rounds down to the rune start
		{"a😀b", 3, 1},
	}
	for _, c := range cases {
		if got := ToUTF16(c.line, c.byte); got != c.want {
			t.Errorf("ToUTF16(%q, %d) = %d, want %d", c.line, c.byte, got, c.want)
		}
	}
}

func TestFromUTF16(t *testing.T) {
	cases := []struct {
		line  string
		units int
		want  int
	}{
		{"hello", 3, 3},
		{"héllo", 2, 3},
		{"日本語", 1, 3},
		{"日本語", 3, 9},
		{"a😀b", 1, 1},
		{"a😀b", 3, 5},
		{"a😀b", 2, 1}, // the low half of a surrogate pair: the rune's start
		{"", 5, 0},
		{"abc", 99, 3},
		{"abc", -1, 0},
	}
	for _, c := range cases {
		if got := FromUTF16(c.line, c.units); got != c.want {
			t.Errorf("FromUTF16(%q, %d) = %d, want %d", c.line, c.units, got, c.want)
		}
	}
}

const mixed = "package main\n" +
	"// héllo wörld — an em dash\n" +
	"var 日本語 = \"文字列\"\n" +
	"// 😀😀 emoji are two units each\n" +
	"\n" +
	"func main() {}\n"

// A round trip through a position must land back on the same byte offset, at
// every offset in a document full of the characters that make the two schemes
// disagree.
func TestRoundTripAcrossADocument(t *testing.T) {
	d := NewDocument(mixed)
	for off := 0; off <= len(mixed); off++ {
		if !utf8.RuneStart(mixed[min(off, len(mixed)-1):][0]) && off < len(mixed) {
			continue // mid-rune offsets are not positions
		}
		p := d.Position(off)
		if got := d.Offset(p); got != off {
			t.Errorf("offset %d -> %+v -> %d", off, p, got)
		}
	}
}

// Line and character have to match what a server computes independently, so
// the reference here is Go's own UTF-16 encoder rather than this package's
// arithmetic restated.
func TestAgreesWithTheStandardEncoder(t *testing.T) {
	d := NewDocument(mixed)
	lines := strings.Split(mixed, "\n")
	off := 0
	for ln, line := range lines {
		for i := 0; i <= len(line); {
			p := d.Position(off + i)
			if p.Line != ln {
				t.Fatalf("offset %d reported line %d, want %d", off+i, p.Line, ln)
			}
			want := len(utf16.Encode([]rune(line[:i])))
			if p.Character != want {
				t.Errorf("line %d byte %d: character %d, want %d", ln, i, p.Character, want)
			}
			if i == len(line) {
				break
			}
			_, size := utf8.DecodeRuneInString(line[i:])
			i += size
		}
		off += len(line) + 1
	}
}

func TestDocumentLines(t *testing.T) {
	cases := map[string]int{
		"":           1,
		"one":        1,
		"one\n":      2, // a trailing newline leaves a final empty line
		"one\ntwo":   2,
		"one\ntwo\n": 3,
		"\n\n\n":     4,
	}
	for text, want := range cases {
		if got := NewDocument(text).Lines(); got != want {
			t.Errorf("Lines(%q) = %d, want %d", text, got, want)
		}
	}
}

// Out-of-range input clamps rather than erroring. A server working from a
// version raj has edited past will send positions that no longer exist, and
// dropping the whole response is worse than aiming at the nearest real place.
func TestOutOfRangeClamps(t *testing.T) {
	d := NewDocument("one\ntwo\n")
	cases := []struct {
		p    Position
		want int
	}{
		{Position{Line: -1, Character: 0}, 0},
		{Position{Line: 0, Character: -5}, 0},
		{Position{Line: 99, Character: 0}, len("one\ntwo\n")},
		{Position{Line: 1, Character: 99}, 7},
	}
	for _, c := range cases {
		if got := d.Offset(c.p); got != c.want {
			t.Errorf("Offset(%+v) = %d, want %d", c.p, got, c.want)
		}
	}
	if got := d.Position(-1); got != (Position{}) {
		t.Errorf("Position(-1) = %+v, want the origin", got)
	}
	if got := d.Position(9999); got.Line != 2 {
		t.Errorf("Position past the end = %+v, want the last line", got)
	}
}

// A range comes back ordered, even if the server sent the ends the other way
// round — an unordered span passed to an edit would delete backwards.
func TestSpanIsOrdered(t *testing.T) {
	d := NewDocument("one\ntwo\n")
	forward := Range{Position{0, 1}, Position{1, 2}}
	backward := Range{Position{1, 2}, Position{0, 1}}
	lo1, hi1 := d.Span(forward)
	lo2, hi2 := d.Span(backward)
	if lo1 != lo2 || hi1 != hi2 {
		t.Errorf("forward gave (%d,%d), backward gave (%d,%d)", lo1, hi1, lo2, hi2)
	}
	if lo1 > hi1 {
		t.Errorf("span is inverted: (%d,%d)", lo1, hi1)
	}
}

// The invariants anything built on this depends on, against arbitrary text and
// arbitrary offsets: a position is always inside the document, converting back
// lands on a rune boundary at or before where it started, and nothing panics.
func FuzzPositionRoundTrip(f *testing.F) {
	f.Add("hello world", 5)
	f.Add(mixed, 40)
	f.Add("日本語\n😀", 7)
	f.Add("", 0)
	f.Add("\n\n\n", 2)

	f.Fuzz(func(t *testing.T, text string, off int) {
		if len(text) > 8000 || !utf8.ValidString(text) {
			return
		}
		d := NewDocument(text)
		p := d.Position(off)

		if p.Line < 0 || p.Line >= d.Lines() {
			t.Fatalf("line %d outside 0..%d for %q", p.Line, d.Lines(), text)
		}
		if p.Character < 0 {
			t.Fatalf("negative character %d", p.Character)
		}
		back := d.Offset(p)
		if back < 0 || back > len(text) {
			t.Fatalf("offset %d outside the document (%d bytes)", back, len(text))
		}
		if back < len(text) && !utf8.RuneStart(text[back]) {
			t.Fatalf("offset %d is mid-rune in %q", back, text)
		}
		// Clamping is allowed, drifting is not: a round trip never moves past
		// where it started.
		want := off
		if want < 0 {
			want = 0
		}
		if want > len(text) {
			want = len(text)
		}
		if back > want {
			t.Fatalf("round trip moved forward: %d -> %+v -> %d", off, p, back)
		}
	})
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
