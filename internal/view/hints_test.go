package view

import "testing"

// baseCase pins an un-hinted conversion, used to prove the hint-aware methods
// agree with the old behaviour when handed no hints.
type baseCase struct {
	line string
	off  int
	col  int
	w    int
}

// The hint-aware methods must be byte-for-byte identical to the plain ones when
// there are no hints: nil and an empty slice must both mean "no hints", not
// "no hints after this point" or anything else.
func TestColumnsNilHintsMatchUnhinted(t *testing.T) {
	c := NewColumns(4)
	cases := []baseCase{
		{"", 0, 0, 0},
		{"\tx", 1, 4, 5},
		{"ab\tx", 3, 4, 5},
		{"abcd\tx", 5, 8, 9},
		{"日本x", 6, 4, 5},
		{"plain", 5, 5, 5},
	}
	for _, tc := range cases {
		if got := c.ColOfHints(tc.line, tc.off, nil); got != tc.col {
			t.Errorf("ColOfHints(%q, %d, nil) = %d, want %d", tc.line, tc.off, got, tc.col)
		}
		if got := c.ColOfHints(tc.line, tc.off, []HintCol{}); got != tc.col {
			t.Errorf("ColOfHints(%q, %d, empty) = %d, want %d", tc.line, tc.off, got, tc.col)
		}
		if got := c.WidthHints(tc.line, nil); got != tc.w {
			t.Errorf("WidthHints(%q, nil) = %d, want %d", tc.line, got, tc.w)
		}
		if got := c.WidthHints(tc.line, []HintCol{}); got != tc.w {
			t.Errorf("WidthHints(%q, empty) = %d, want %d", tc.line, got, tc.w)
		}
		// The exported methods delegate to the hint-aware ones with nil.
		if got := c.ColOf(tc.line, tc.off); got != tc.col {
			t.Errorf("ColOf(%q, %d) = %d, want %d", tc.line, tc.off, got, tc.col)
		}
		if got := c.Width(tc.line); got != tc.w {
			t.Errorf("Width(%q) = %d, want %d", tc.line, got, tc.w)
		}
	}
	// OffsetOf over the whole column range must agree for both spellings.
	for _, line := range []string{"", "\tx", "ab\tx", "日本x", "a\tb\tc"} {
		for col := 0; col <= c.Width(line); col++ {
			want := c.OffsetOf(line, col)
			if got := c.OffsetOfHints(line, col, nil); got != want {
				t.Errorf("OffsetOfHints(%q, %d, nil) = %d, OffsetOf = %d", line, col, got, want)
			}
			if got := c.OffsetOfHints(line, col, []HintCol{}); got != want {
				t.Errorf("OffsetOfHints(%q, %d, empty) = %d, OffsetOf = %d", line, col, got, want)
			}
		}
	}
}

// A hint anchored at offset Off starts at the boundary Off; the caret for that
// boundary sits BEFORE the hint. So ColOfHints counts only hints anchored
// STRICTLY BEFORE off, and WidthHints counts them all.
func TestColumnsHintBoundaryStrictlyBefore(t *testing.T) {
	c := NewColumns(4)
	// One hint at byte 3 of "abcdef": columns 0 1 2 [h h] 3 4 5.
	one := []HintCol{{Off: 3, Width: 2}}
	for _, tc := range []struct {
		off int
		col int
	}{
		{0, 0}, {1, 1}, {2, 2},
		{3, 3}, // anchor: the hint is not counted, the caret is before it
		{4, 6}, {5, 7}, {6, 8},
	} {
		if got := c.ColOfHints("abcdef", tc.off, one); got != tc.col {
			t.Errorf("one hint: ColOfHints(off=%d) = %d, want %d", tc.off, got, tc.col)
		}
	}
	// Two hints: at 1 (width 2) and at 3 (width 2).
	two := []HintCol{{Off: 1, Width: 2}, {Off: 3, Width: 2}}
	for _, tc := range []struct {
		off int
		col int
	}{
		{0, 0}, {1, 1},
		{2, 4}, // first hint is strictly before
		{3, 5}, // second anchor: only the first is counted
		{4, 8}, {6, 10},
	} {
		if got := c.ColOfHints("abcdef", tc.off, two); got != tc.col {
			t.Errorf("two hints: ColOfHints(off=%d) = %d, want %d", tc.off, got, tc.col)
		}
	}
	if got := c.WidthHints("abcdef", two); got != 10 {
		t.Errorf("WidthHints(abcdef, two) = %d, want 10", got)
	}
}

// A hint at end of line has no later offset it could be strictly before, so it
// is excluded from ColOfHints there but must be included in WidthHints.
func TestColumnsHintAtEndOfLineCountsInWidthOnly(t *testing.T) {
	c := NewColumns(4)
	h := []HintCol{{Off: 3, Width: 4}}
	if got := c.ColOfHints("abc", 3, h); got != 3 {
		t.Errorf("caret at end-of-line hint anchor = %d, want 3 (before the hint)", got)
	}
	if got := c.WidthHints("abc", h); got != 7 {
		t.Errorf("WidthHints with end-of-line hint = %d, want 7", got)
	}
	if got := c.OffsetOfHints("abc", 3, h); got != 3 {
		t.Errorf("column 3 inside end-of-line hint -> offset %d, want 3", got)
	}
}

// A hint at offset 0 starts at the very first column: the caret for offset 0 is
// still before it.
func TestColumnsHintAtZero(t *testing.T) {
	c := NewColumns(4)
	h := []HintCol{{Off: 0, Width: 3}}
	if got := c.ColOfHints("abcd", 0, h); got != 0 {
		t.Errorf("ColOfHints(0) with hint at 0 = %d, want 0", got)
	}
	if got := c.OffsetOfHints("abcd", 0, h); got != 0 {
		t.Errorf("OffsetOfHints(0) with hint at 0 = %d, want 0", got)
	}
	if got := c.OffsetOfHints("abcd", 2, h); got != 0 {
		t.Errorf("column 2 inside hint at 0 -> offset %d, want 0", got)
	}
	if got := c.WidthHints("abcd", h); got != 7 {
		t.Errorf("WidthHints = %d, want 7", got)
	}
}

// A zero-width hint occupies no columns and shadows no byte, so it changes
// nothing at all.
func TestColumnsZeroWidthHintIsInert(t *testing.T) {
	c := NewColumns(4)
	h := []HintCol{{Off: 2, Width: 0}}
	if got := c.ColOfHints("abcd", 2, h); got != 2 {
		t.Errorf("ColOfHints through a zero-width hint = %d, want 2", got)
	}
	if got := c.OffsetOfHints("abcd", 2, h); got != 2 {
		t.Errorf("OffsetOfHints at a zero-width hint = %d, want 2", got)
	}
	if got := c.WidthHints("abcd", h); got != 4 {
		t.Errorf("WidthHints with a zero-width hint = %d, want 4", got)
	}
	// A zero-width hint after a real one must not disturb the real one's span.
	both := []HintCol{{Off: 1, Width: 2}, {Off: 2, Width: 0}}
	if got := c.OffsetOfHints("abcd", 1, both); got != 1 {
		t.Errorf("OffsetOfHints inside a real hint = %d, want 1", got)
	}
	if got := c.OffsetOfHints("abcd", 4, both); got != 2 {
		t.Errorf("OffsetOfHints at the zero-width anchor = %d, want 2", got)
	}
	if got := c.WidthHints("abcd", both); got != 6 {
		t.Errorf("WidthHints with both hints = %d, want 6", got)
	}
}

// For a hint anchor, the column of the anchor must invert back to the anchor,
// never to a byte inside the hint. This must hold with tabs and wide runes,
// whose base columns already differ from their byte offsets.
func TestColumnsHintAnchorRoundTrip(t *testing.T) {
	c := NewColumns(4)
	cases := []struct {
		name    string
		line    string
		hints   []HintCol
		anchors []int
	}{
		{"offset-zero", "abcdef", []HintCol{{Off: 0, Width: 3}}, []int{0}},
		{"middle", "abcdef", []HintCol{{Off: 3, Width: 2}}, []int{3}},
		{"end-of-line", "abcdef", []HintCol{{Off: 6, Width: 3}}, []int{6}},
		{"two", "abcdef", []HintCol{{Off: 1, Width: 2}, {Off: 3, Width: 2}}, []int{1, 3}},
		{"tab", "\tabc", []HintCol{{Off: 1, Width: 3}}, []int{1}},
		{"wide", "日本語", []HintCol{{Off: 3, Width: 2}}, []int{3}},
		{"tab-wide", "\t日本x", []HintCol{{Off: 1, Width: 3}, {Off: 7, Width: 1}}, []int{1, 7}},
		{"zero-width", "abcd", []HintCol{{Off: 2, Width: 0}}, []int{2}},
	}
	for _, tc := range cases {
		for _, off := range tc.anchors {
			col := c.ColOfHints(tc.line, off, tc.hints)
			if got := c.OffsetOfHints(tc.line, col, tc.hints); got != off {
				t.Errorf("%s: %q anchor %d -> col %d -> offset %d, want %d",
					tc.name, tc.line, off, col, got, off)
			}
		}
	}
}

// A column landing inside a hint's span resolves to that hint's anchor and
// never to a byte offset deeper into the line (there is no such byte).
func TestColumnsHintColumnInsideResolvesToAnchor(t *testing.T) {
	c := NewColumns(4)
	cases := []struct {
		name  string
		line  string
		hints []HintCol
	}{
		{"middle", "abcdef", []HintCol{{Off: 3, Width: 2}}},
		{"two", "abcdef", []HintCol{{Off: 1, Width: 2}, {Off: 3, Width: 2}}},
		{"tab-wide", "\t日本x", []HintCol{{Off: 1, Width: 3}, {Off: 7, Width: 1}}},
	}
	for _, tc := range cases {
		for _, h := range tc.hints {
			start := c.ColOfHints(tc.line, h.Off, tc.hints)
			for d := 0; d < h.Width; d++ {
				if got := c.OffsetOfHints(tc.line, start+d, tc.hints); got != h.Off {
					t.Errorf("%s: %q col %d inside hint at %d -> offset %d, want %d",
						tc.name, tc.line, start+d, h.Off, got, h.Off)
				}
			}
		}
	}
}

// Outside hint spans the hint-aware round trip must still recover the byte
// offset on lines whose base columns differ from their byte offsets.
func TestColumnsHintRoundTripTabsAndWideRunes(t *testing.T) {
	c := NewColumns(4)
	cases := []struct {
		line  string
		hints []HintCol
	}{
		{"\t日本x", []HintCol{{Off: 4, Width: 2}}},
		{"a\tb日本c", []HintCol{{Off: 2, Width: 1}, {Off: 6, Width: 3}}},
		{"日本語", []HintCol{{Off: 0, Width: 1}, {Off: 6, Width: 2}}},
	}
	for _, tc := range cases {
		spans := hintSpans(c, tc.line, tc.hints)
		for off := 0; off <= len(tc.line); off++ {
			if off > 0 && off < len(tc.line) && !isBoundary(tc.line, off) {
				continue
			}
			col := c.ColOfHints(tc.line, off, tc.hints)
			if inSpan(spans, col) && !isAnchor(tc.hints, off) {
				// A hint can legitimately shadow the column of a
				// neighbouring offset; the anchor round trip pins that.
				continue
			}
			if got := c.OffsetOfHints(tc.line, col, tc.hints); got != off {
				t.Errorf("%q offset %d -> col %d -> offset %d (hints %v)",
					tc.line, off, col, got, tc.hints)
			}
		}
	}
}

type colSpan struct{ start, end int }

func hintSpans(c Columns, line string, hints []HintCol) []colSpan {
	spans := make([]colSpan, 0, len(hints))
	for _, h := range hints {
		start := c.ColOfHints(line, h.Off, hints)
		spans = append(spans, colSpan{start, start + h.Width})
	}
	return spans
}

func inSpan(spans []colSpan, col int) bool {
	for _, s := range spans {
		if col >= s.start && col < s.end {
			return true
		}
	}
	return false
}

func isAnchor(hints []HintCol, off int) bool {
	for _, h := range hints {
		if h.Off == off {
			return true
		}
	}
	return false
}
