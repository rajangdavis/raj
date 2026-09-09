package editor

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

// FuzzMoveLines drives a random walk of MoveLines against a brute-force oracle:
// a []string that swaps the same rows. Two properties are checked after every
// press.
//
//   - The document is a permutation of the lines it started with. A move that
//     duplicates or drops a line is the failure mode that reads as "text got
//     lost in the buffer".
//   - The selected text is unchanged. The block a user highlighted is the block
//     that moves, press after press, so the bytes between anchor and head must
//     be the same bytes throughout.
//
// The oracle deliberately does not model the cursor: it says where the lines
// should end up, and the selection property says the cursor followed them.
func FuzzMoveLines(f *testing.F) {
	f.Add(6, 1, 2, uint16(0b101010))
	f.Add(4, 0, 0, uint16(0xFFFF))
	f.Add(9, 7, 8, uint16(0x0F0F))

	f.Fuzz(func(t *testing.T, lineCount, selFrom, selTo int, moves uint16) {
		if lineCount < 2 || lineCount > 64 {
			t.Skip()
		}
		norm := func(v int) int {
			v %= lineCount
			if v < 0 {
				v += lineCount
			}
			return v
		}
		selFrom, selTo = norm(selFrom), norm(selTo)

		oracle := make([]string, lineCount)
		for i := range oracle {
			oracle[i] = "line" + string(rune('a'+i%26)) + strings.Repeat("x", i%3)
		}
		p := newTestPane(strings.Join(oracle, "\n"))

		p.Cursors.Set(
			p.File.OffsetAt(selTo, 0),
			p.File.OffsetAt(selFrom, 0),
		)
		want := selectedText(p)

		for i := 0; i < 16; i++ {
			delta := -1
			if moves&(1<<i) != 0 {
				delta = +1
			}

			lines := p.touchedLines()
			// A downward move that would push the excluded column-0 boundary
			// line into its own block is refused, so the oracle must not move
			// those lines either.
			refused := len(lines) > 0 && delta > 0 && p.boundaryBlocksDown(lines)
			if !refused {
				moveOracle(oracle, lines, delta)
			}
			p.MoveLines(delta)

			if got := p.File.Text(); got != strings.Join(oracle, "\n") {
				t.Fatalf("press %d (delta %d): text = %q, oracle = %q",
					i, delta, got, strings.Join(oracle, "\n"))
			}
			if got := selectedText(p); got != want {
				t.Fatalf("press %d (delta %d): selection = %q, want %q",
					i, delta, got, want)
			}
		}
	})
}

// moveOracle applies the same shift MoveLines should, on a plain slice.
func moveOracle(lines []string, rows []int, delta int) {
	if len(rows) == 0 {
		return
	}
	if delta < 0 && rows[0] == 0 {
		return
	}
	if delta > 0 && rows[len(rows)-1] >= len(lines)-1 {
		return
	}
	swap := func(a, b int) { lines[a], lines[b] = lines[b], lines[a] }
	if delta > 0 {
		for i := len(rows) - 1; i >= 0; i-- {
			swap(rows[i], rows[i]+1)
		}
		return
	}
	for _, r := range rows {
		swap(r-1, r)
	}
}

func selectedText(p *Pane) string {
	lo, hi := p.Cursors.Primary().Range()
	return p.File.Slice(lo, hi-lo)
}

// TestMoveLinesPermutation is the cheap always-on version of the fuzz property:
// whatever the presses, the multiset of lines never changes.
func TestMoveLinesPermutation(t *testing.T) {
	const start = "a\nb\nc\nd\ne\nf"
	p := newTestPane(start)
	p.Cursors.Set(p.File.OffsetAt(3, 0), p.File.OffsetAt(1, 0))

	for _, delta := range []int{1, 1, -1, 1, -1, -1, -1, 1} {
		p.MoveLines(delta)
	}

	got := strings.Split(p.File.Text(), "\n")
	want := strings.Split(start, "\n")
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("lines = %v, want a permutation of %v", got, want)
	}
}

// TestMoveLinesExcludesLineTouchedAtColumn0 pins the column-0 exclusion: a
// selection ending at column 0 of line 3 covers only lines 1-2, so moving up
// must leave line 3's text in place.
func TestMoveLinesExcludesLineTouchedAtColumn0(t *testing.T) {
	const start = "a\nb\nc\nd\ne\nf"
	p := newTestPane(start)
	p.Cursors.Set(p.File.OffsetAt(3, 0), p.File.OffsetAt(1, 0))

	if got := p.touchedLines(); !reflect.DeepEqual(got, []int{1, 2}) {
		t.Fatalf("touchedLines = %v, want [1 2]", got)
	}

	p.MoveLines(-1)
	if want := "b\nc\na\nd\ne\nf"; p.File.Text() != want {
		t.Errorf("move up = %q, want %q", p.File.Text(), want)
	}
}

// TestMoveLinesWontPushTheExcludedBoundary pins the downward guard: moving the
// same selection down would push the column-0 boundary line into its own block,
// so MoveLines refuses rather than displace text the selection spared.
func TestMoveLinesWontPushTheExcludedBoundary(t *testing.T) {
	const start = "a\nb\nc\nd\ne\nf"
	p := newTestPane(start)
	p.Cursors.Set(p.File.OffsetAt(3, 0), p.File.OffsetAt(1, 0))

	if got := p.touchedLines(); !reflect.DeepEqual(got, []int{1, 2}) {
		t.Fatalf("touchedLines = %v, want [1 2]", got)
	}

	p.MoveLines(1)
	if got := p.File.Text(); got != start {
		t.Errorf("move down = %q, want %q (the boundary must stay where it is)", got, start)
	}
	if got := selectedText(p); got != "b\nc\n" {
		t.Errorf("selection = %q, want still %q", got, "b\nc\n")
	}
}
