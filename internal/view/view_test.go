package view

import (
	"math/rand"
	"reflect"
	"strings"
	"testing"
)

// The incremental index must agree with a full rescan after any edit sequence.
// This is the property the whole design rests on: if it drifts, every line
// number in the editor is wrong and nothing downstream can detect it.
func TestIndexMatchesRescanUnderFuzz(t *testing.T) {
	inserts := []string{"a", "\n", "hello\nworld", "\n\n\n", "日本語", "x\n"}
	for seed := 0; seed < 50; seed++ {
		rng := rand.New(rand.NewSource(int64(seed)))
		doc := "package main\n\nfunc main() {\n}\n"
		ix := NewIndex(doc)

		for step := 0; step < 60; step++ {
			if rng.Intn(3) == 0 && len(doc) > 0 {
				pos := rng.Intn(len(doc))
				n := rng.Intn(9) + 1
				if pos+n > len(doc) {
					n = len(doc) - pos
				}
				ix.Delete(pos, n)
				doc = doc[:pos] + doc[pos+n:]
			} else {
				pos := rng.Intn(len(doc) + 1)
				text := inserts[rng.Intn(len(inserts))]
				ix.Insert(pos, text)
				doc = doc[:pos] + text + doc[pos:]
			}
			want := NewIndex(doc)
			if !reflect.DeepEqual(ix.starts, want.starts) {
				t.Fatalf("seed %d step %d: index drifted\n got %v\nwant %v\ndoc %q",
					seed, step, ix.starts, want.starts, doc)
			}
		}
	}
}

func TestIndexLookups(t *testing.T) {
	doc := "one\ntwo\nthree"
	ix := NewIndex(doc)
	if ix.Lines() != 3 {
		t.Fatalf("lines = %d, want 3", ix.Lines())
	}
	for off, want := range map[int]int{0: 0, 3: 0, 4: 1, 7: 1, 8: 2, 12: 2} {
		if got := ix.LineOf(off); got != want {
			t.Errorf("LineOf(%d) = %d, want %d", off, got, want)
		}
	}
	if got := ix.LineEnd(0, len(doc)); got != 3 {
		t.Errorf("LineEnd(0) = %d, want 3 (newline excluded)", got)
	}
	if got := ix.LineEnd(2, len(doc)); got != 13 {
		t.Errorf("LineEnd(last) = %d, want 13", got)
	}
}

// Text ending in a newline has a trailing empty line, as every editor shows.
func TestIndexTrailingNewline(t *testing.T) {
	ix := NewIndex("a\n")
	if ix.Lines() != 2 {
		t.Errorf("lines = %d, want 2", ix.Lines())
	}
	if got := ix.LineLen(1, 2); got != 0 {
		t.Errorf("trailing line length = %d, want 0", got)
	}
}

func TestColumnsTabsAndWideRunes(t *testing.T) {
	c := NewColumns(4)
	cases := []struct {
		line string
		off  int
		col  int
	}{
		{"\tx", 1, 4},     // tab advances to the next stop
		{"ab\tx", 3, 4},   // partial tab: 2 chars then 2 more columns
		{"abcd\tx", 5, 8}, // full tab from a stop
		{"日本x", 6, 4},     // two wide runes, six bytes, four columns
		{"", 0, 0},
	}
	for _, tc := range cases {
		if got := c.ColOf(tc.line, tc.off); got != tc.col {
			t.Errorf("ColOf(%q, %d) = %d, want %d", tc.line, tc.off, got, tc.col)
		}
	}
}

// Byte offset and display column must round-trip, or the cursor drifts every
// time it moves vertically through indented or CJK text.
func TestColumnsRoundTrip(t *testing.T) {
	c := NewColumns(4)
	for _, line := range []string{"plain", "\tindented", "  two", "日本語text", "a\tb\tc", ""} {
		for off := 0; off <= len(line); off++ {
			if off > 0 && off < len(line) && !isBoundary(line, off) {
				continue
			}
			col := c.ColOf(line, off)
			if got := c.OffsetOf(line, col); got != off {
				t.Errorf("%q: offset %d -> col %d -> offset %d", line, off, col, got)
			}
		}
	}
}

func isBoundary(s string, i int) bool { return s[i]&0xC0 != 0x80 }

// A column landing inside a tab resolves to the tab's start, so the cursor is
// never drawn mid-glyph.
func TestColumnsInsideTab(t *testing.T) {
	c := NewColumns(4)
	for col := 0; col < 4; col++ {
		if got := c.OffsetOf("\tx", col); got != 0 {
			t.Errorf("col %d inside tab -> offset %d, want 0", col, got)
		}
	}
	if got := c.OffsetOf("\tx", 4); got != 1 {
		t.Errorf("col 4 -> offset %d, want 1", got)
	}
}

func TestExpandMapsColumnsToOffsets(t *testing.T) {
	c := NewColumns(4)
	text, offs := c.Expand("\tab")
	if text != "    ab" {
		t.Errorf("expanded = %q, want four spaces then ab", text)
	}
	want := []int{0, 0, 0, 0, 1, 2}
	if !reflect.DeepEqual(offs, want) {
		t.Errorf("offsets = %v, want %v", offs, want)
	}
}

// Scrolling must not move when the target is already comfortably visible.
func TestViewportDoesNotJitter(t *testing.T) {
	v := &Viewport{Rows: 10, Cols: 40, ScrollOff: 2}
	v.ScrollTo(5, 0, 100)
	top := v.Top
	for line := 3; line <= 7; line++ {
		v.ScrollTo(line, 0, 100)
		if v.Top != top {
			t.Errorf("scrolled to %d moved Top from %d to %d", line, top, v.Top)
		}
	}
}

func TestViewportScrollOff(t *testing.T) {
	v := &Viewport{Rows: 10, Cols: 40, ScrollOff: 3}
	v.Top = 20
	v.ScrollTo(21, 0, 100) // within scrolloff of the top edge
	if v.Top != 18 {
		t.Errorf("Top = %d, want 18 (three lines of context above)", v.Top)
	}
	v.Top = 20
	v.ScrollTo(28, 0, 100) // within scrolloff of the bottom edge
	if v.Top != 22 {
		t.Errorf("Top = %d, want 22", v.Top)
	}
}

// A scrolloff taller than the pane must not deadlock the two edge rules
// against each other.
func TestViewportOversizedScrollOff(t *testing.T) {
	v := &Viewport{Rows: 4, Cols: 40, ScrollOff: 10}
	v.ScrollTo(50, 0, 100)
	if !v.Visible(50) {
		t.Errorf("line 50 not visible: Top=%d Rows=%d", v.Top, v.Rows)
	}
}

func TestViewportHorizontal(t *testing.T) {
	v := &Viewport{Rows: 10, Cols: 20}
	v.ScrollTo(0, 45, 10)
	if v.Left != 26 {
		t.Errorf("Left = %d, want 26", v.Left)
	}
	v.ScrollTo(0, 10, 10)
	if v.Left != 10 {
		t.Errorf("Left = %d, want 10 after scrolling back", v.Left)
	}
}

func TestViewportClampsToDocument(t *testing.T) {
	v := &Viewport{Rows: 10, Cols: 40}
	v.ScrollBy(500, 3)
	if v.Top > 2 {
		t.Errorf("Top = %d, want at most 2", v.Top)
	}
	v.ScrollBy(-500, 3)
	if v.Top != 0 {
		t.Errorf("Top = %d, want 0", v.Top)
	}
}

func TestRebuild(t *testing.T) {
	ix := NewIndex("a\nb\n")
	ix.Rebuild(strings.Repeat("x\n", 5))
	if ix.Lines() != 6 {
		t.Errorf("lines = %d, want 6", ix.Lines())
	}
}
