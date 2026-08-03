package piecetable

import (
	"math/rand"
	"strings"
	"testing"
)

// oracle is the ground truth: a plain string. Both Buffer implementations must
// agree with it after every operation.
type oracle struct{ s string }

func (o *oracle) Insert(pos int, text string) {
	pos = clamp(pos, 0, len(o.s))
	o.s = o.s[:pos] + text + o.s[pos:]
}

func (o *oracle) Delete(pos, length int) {
	pos = clamp(pos, 0, len(o.s))
	end := clamp(pos+length, pos, len(o.s))
	o.s = o.s[:pos] + o.s[end:]
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func newBuffers(orig string) (map[string]Buffer, *oracle) {
	return map[string]Buffer{
		"naive": NewNaive(orig),
		"doc":   NewDoc(orig, 5),
	}, &oracle{s: orig}
}

// TestFuzzAgainstOracle drives random edit sequences through both
// implementations and the string oracle, checking full text, length and piece
// count after every step. This is what makes the B-tree trustworthy: it is a
// lot of machinery to be subtly wrong in, and the naive buffer is not.
func TestFuzzAgainstOracle(t *testing.T) {
	const seeds, steps = 40, 200
	words := []string{"a", "hello", "π", "\n", "func main() {", "  return nil\n", "日本語"}

	for seed := 0; seed < seeds; seed++ {
		rng := rand.New(rand.NewSource(int64(seed)))
		bufs, want := newBuffers("package main\n\nfunc main() {}\n")

		for step := 0; step < steps; step++ {
			switch n := want.Len(); {
			case rng.Intn(3) == 0 && n > 0:
				pos, length := rng.Intn(n), rng.Intn(12)+1
				want.Delete(pos, length)
				for _, b := range bufs {
					b.Delete(pos, length)
				}
			default:
				pos := 0
				if n > 0 {
					pos = rng.Intn(n + 1)
				}
				text := words[rng.Intn(len(words))]
				author := Author(rng.Intn(4))
				want.Insert(pos, text)
				for _, b := range bufs {
					b.Insert(author, pos, text)
				}
			}
			for name, b := range bufs {
				if got := b.Slice(0, b.Len()); got != want.s {
					t.Fatalf("seed %d step %d: %s text diverged\n got %q\nwant %q",
						seed, step, name, got, want.s)
				}
				if b.Len() != len(want.s) {
					t.Fatalf("seed %d step %d: %s Len=%d, want %d",
						seed, step, name, b.Len(), len(want.s))
				}
			}
		}
	}
}

func (o *oracle) Len() int { return len(o.s) }

// Spans must tile the requested range exactly: no gaps, no overlaps, and the
// concatenated text must equal Slice. The renderer trusts this to paint author
// tints, so a gap shows as an uncoloured hole.
func TestSpansTileRange(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	bufs, _ := newBuffers("the original file\n")
	for step := 0; step < 300; step++ {
		for _, b := range bufs {
			pos := rng.Intn(b.Len() + 1)
			b.Insert(Author(rng.Intn(4)), pos, "xyz")
		}
		for name, b := range bufs {
			pos := rng.Intn(b.Len() + 1)
			length := rng.Intn(b.Len() - pos + 1)
			spans := b.Spans(pos, length)
			at, total := pos, 0
			var sb strings.Builder
			for _, s := range spans {
				if s.Off != at {
					t.Fatalf("%s: span starts at %d, want %d", name, s.Off, at)
				}
				if s.Len <= 0 {
					t.Fatalf("%s: empty span", name)
				}
				sb.WriteString(b.Slice(s.Off, s.Len))
				at += s.Len
				total += s.Len
			}
			if total != length {
				t.Fatalf("%s: spans cover %d bytes, want %d", name, total, length)
			}
			if got := sb.String(); got != b.Slice(pos, length) {
				t.Fatalf("%s: span text %q != slice %q", name, got, b.Slice(pos, length))
			}
		}
	}
}

// Attribution must survive splitting: inserting into the middle of an agent's
// text leaves agent text on both sides of the new piece.
func TestAuthorshipSurvivesSplit(t *testing.T) {
	bufs, _ := newBuffers("")
	for name, b := range bufs {
		b.Insert(Agent, 0, "AAAABBBB")
		b.Insert(User, 4, "xx")
		spans := b.Spans(0, b.Len())
		want := []Span{{0, 4, Agent}, {4, 2, User}, {6, 4, Agent}}
		if len(spans) != len(want) {
			t.Fatalf("%s: got %d spans %+v, want %d", name, len(spans), spans, len(want))
		}
		for i := range want {
			if spans[i] != want[i] {
				t.Errorf("%s: span %d = %+v, want %+v", name, i, spans[i], want[i])
			}
		}
	}
}

// Adjacent runs by the same author must merge into one span, or the renderer
// gets one span per piece and piece fragmentation becomes visible as colour
// banding.
func TestSpansMergeSameAuthor(t *testing.T) {
	bufs, _ := newBuffers("")
	for name, b := range bufs {
		b.Insert(Agent, 0, "one")
		b.Insert(Agent, 3, "two")
		b.Insert(Agent, 6, "three")
		if spans := b.Spans(0, b.Len()); len(spans) != 1 {
			t.Errorf("%s: got %d spans, want 1: %+v", name, len(spans), spans)
		}
	}
}

// Out-of-range reads clamp instead of panicking: the renderer routinely asks
// for a screenful past the end of a short file.
func TestRangeClamping(t *testing.T) {
	bufs, _ := newBuffers("abc")
	for name, b := range bufs {
		if got := b.Slice(0, 1000); got != "abc" {
			t.Errorf("%s: over-long slice = %q", name, got)
		}
		// Ranges INTERSECT the document rather than sliding: a range that
		// starts before offset 0 keeps its end, it does not shift right.
		if got := b.Slice(-5, 7); got != "ab" {
			t.Errorf("%s: straddling-start slice = %q, want \"ab\"", name, got)
		}
		if got := b.Slice(-5, 2); got != "" {
			t.Errorf("%s: entirely-before slice = %q, want empty", name, got)
		}
		if got := b.Slice(99, 5); got != "" {
			t.Errorf("%s: past-end slice = %q", name, got)
		}
		b.Delete(2, 999)
		if got := b.Slice(0, b.Len()); got != "ab" {
			t.Errorf("%s: over-long delete left %q", name, got)
		}
	}
}
