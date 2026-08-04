package view

import (
	"strings"
	"testing"
	"unicode/utf8"
)

var policies = map[string]BreakPolicy{
	"char":   BreakAnywhere,
	"word":   BreakOnSpace,
	"hybrid": BreakHybrid,
}

// checkWrapInvariants is the oracle. Every property here is something the
// viewport relies on: if any fails, the caret can land on a row the renderer
// never drew.
func checkWrapInvariants(t *testing.T, name, s string, w, tab int, opp BreakPolicy) {
	t.Helper()
	c := NewColumns(tab)
	breaks := c.AppendWrap(nil, s, w, opp)

	prev := 0
	for i, b := range breaks {
		if b <= prev && !(i == 0 && b == 0) {
			t.Fatalf("%s w=%d: break %d at %d not after %d: %q", name, w, i, b, prev, s)
		}
		if b < 0 || b > len(s) {
			t.Fatalf("%s w=%d: break %d out of range for len %d", name, w, b, len(s))
		}
		if b < len(s) && !utf8.RuneStart(s[b]) {
			t.Fatalf("%s w=%d: break at %d lands mid-rune: %q", name, w, b, s)
		}
		prev = b
	}

	// Rows must reconstruct the line exactly: no byte dropped or duplicated.
	var sb strings.Builder
	start := 0
	for _, b := range breaks {
		sb.WriteString(s[start:b])
		start = b
	}
	sb.WriteString(s[start:])
	if sb.String() != s {
		t.Fatalf("%s w=%d: rows do not reassemble the line", name, w)
	}

	// No row may exceed the pane, unless it holds a single unfittable rune.
	start = 0
	for _, b := range append(append([]int{}, breaks...), len(s)) {
		row := s[start:b]
		if width := c.colOfFrom(row, 0); width > w && utf8.RuneCountInString(row) > 1 {
			t.Fatalf("%s w=%d: row %q is %d cols wide", name, w, row, width)
		}
		start = b
	}

	if got, want := c.WrapRows(s, w, opp), len(breaks)+1; got != want {
		t.Fatalf("%s: WrapRows=%d but AppendWrap gives %d", name, got, want)
	}
}

// checkWrapGreedy is the property the invariants above miss entirely: they are
// all satisfied by a layout that puts one rune on every row.
//
// A row is greedy when it ends at the overflow point, or retreats from it to a
// break opportunity with no later opportunity available. Stated as "the last
// opportunity inside the row" it is wrong for character wrap, where every
// position is an opportunity and the overflow point is itself the answer.
func checkWrapGreedy(t *testing.T, name, s string, w, tab int, opp BreakPolicy) {
	t.Helper()
	c := NewColumns(tab)
	breaks := c.AppendWrap(nil, s, w, opp)
	bounds := append([]int{0}, breaks...)
	bounds = append(bounds, len(s))

	for i := 0; i+1 < len(bounds); i++ {
		start, end := bounds[i], bounds[i+1]
		if start == end && start != len(s) {
			t.Fatalf("%s w=%d: empty row at %d in %q", name, w, start, s)
		}
		if end == len(s) {
			break // the last row need not be full
		}
		hard, col := len(s), 0
		for p := start; p < len(s); {
			r, n := utf8.DecodeRuneInString(s[p:])
			rw := c.runeCols(r, col)
			if col+rw > w && col > 0 {
				hard = p
				break
			}
			col += rw
			p += n
		}
		if end > hard {
			t.Fatalf("%s w=%d: row [%d,%d) overruns overflow point %d: %q", name, w, start, end, hard, s)
		}
		if end == hard {
			continue
		}
		pr, _ := utf8.DecodeLastRuneInString(s[start:end])
		if !opp(pr) {
			t.Fatalf("%s w=%d: row [%d,%d) is short but does not end on an opportunity: %q",
				name, w, start, end, s)
		}
		for p := end; p < hard; {
			r, n := utf8.DecodeRuneInString(s[p:])
			if opp(r) && p+n <= hard && p+n > end {
				t.Fatalf("%s w=%d: row [%d,%d) retreated past a usable opportunity at %d: %q",
					name, w, start, end, p+n, s)
			}
			p += n
		}
	}
}

// WrapRowOf must agree with the layout it is derived from, or the caret is
// placed on a row the renderer did not draw. This is the conversion the whole
// feature rests on.
func checkWrapRowOf(t *testing.T, name, s string, w, tab int, opp BreakPolicy) {
	t.Helper()
	c := NewColumns(tab)
	breaks := c.AppendWrap(nil, s, w, opp)
	for off := 0; off <= len(s); off++ {
		if off < len(s) && !utf8.RuneStart(s[off]) {
			continue
		}
		row, col := c.RowOfBreaks(breaks, s, off)
		if row < 0 || row > len(breaks) {
			t.Fatalf("%s w=%d off=%d: row %d outside 0..%d", name, w, off, row, len(breaks))
		}
		// The row reported must be the one whose byte range contains off.
		lo := 0
		if row > 0 {
			lo = breaks[row-1]
		}
		hi := len(s)
		if row < len(breaks) {
			hi = breaks[row]
		}
		if off < lo || off > hi {
			t.Fatalf("%s w=%d off=%d: reported row %d spans [%d,%d)", name, w, off, row, lo, hi)
		}
		if want := c.colOfFrom(s[lo:off], 0); col != want {
			t.Fatalf("%s w=%d off=%d: col %d, want %d", name, w, off, col, want)
		}
	}
}

func FuzzWrap(f *testing.F) {
	f.Add("hello world", 5, 2)
	f.Add(strings.Repeat("x", 500), 80, 2)
	f.Add("/very/long/path/"+strings.Repeat("segment/", 40), 20, 2)
	f.Add(strings.Repeat("a,", 200), 10, 2)
	f.Add("\t\t\t\tdeeply indented and then some text", 8, 4)
	f.Add(strings.Repeat("　", 100), 9, 2)
	f.Add("aGVsbG8gd29ybGQ="+strings.Repeat("QUJD", 300), 40, 2)
	f.Add(strings.Repeat("word ", 50), 1, 2)
	f.Add("émoji 🙂 and combining é", 7, 2)
	f.Add(strings.Repeat(".", 1000), 3, 2)
	f.Add(" 0\t", 2, 4) // the elastic-tab retreat that fuzzing found
	f.Add("", 10, 2)

	f.Fuzz(func(t *testing.T, s string, w int, tab int) {
		// Kept short on purpose: the greedy oracle recomputes each row's
		// overflow point, so it is quadratic in line length, and long inputs
		// starve the fuzzer of executions without exploring new shapes.
		// TestWrapOverflowTerminates covers the multi-kilobyte cases.
		if w < 1 || w > 120 || tab < 1 || tab > 16 || len(s) > 400 {
			return
		}
		if !utf8.ValidString(s) {
			return
		}
		for name, opp := range policies {
			checkWrapInvariants(t, name, s, w, tab, opp)
			checkWrapGreedy(t, name, s, w, tab, opp)
			checkWrapRowOf(t, name, s, w, tab, opp)
		}
	})
}

// Layout must terminate and advance on content with no break opportunity.
func TestWrapOverflowTerminates(t *testing.T) {
	cases := map[string]string{
		"one long token": strings.Repeat("x", 5000),
		"base64":         strings.Repeat("QUJDRA==", 500),
		"minified":       strings.Repeat("a=1;b=2;c(d,e);", 300),
		"deep path":      strings.Repeat("/segment", 400),
		"cjk":            strings.Repeat("あ", 2000),
		"emoji":          strings.Repeat("🙂", 1000),
		"tabs":           strings.Repeat("\t", 2000),
	}
	for name, s := range cases {
		for _, w := range []int{1, 2, 3, 7, 80} {
			for pname, opp := range policies {
				if got := NewColumns(2).WrapRows(s, w, opp); got < 1 {
					t.Errorf("%s/%s w=%d: %d rows", name, pname, w, got)
				}
				checkWrapInvariants(t, pname, s, w, 2, opp)
			}
		}
	}
}

// The regression fuzzing found: retreating to a break opportunity can make an
// elastic tab wider, so the retreat has to be re-checked rather than assumed.
func TestElasticTabRetreat(t *testing.T) {
	c := NewColumns(4)
	s := " 0\t"
	breaks := c.AppendWrap(nil, s, 2, BreakOnSpace)
	start := 0
	for _, b := range append(append([]int{}, breaks...), len(s)) {
		if width := c.colOfFrom(s[start:b], 0); width > 2 && utf8.RuneCountInString(s[start:b]) > 1 {
			t.Errorf("row %q is %d columns in a 2-column pane", s[start:b], width)
		}
		start = b
	}
}

// A line with no wrapping is one row, and the caret conversion agrees with the
// unwrapped column mapping.
func TestWrapDegeneratesWhenItFits(t *testing.T) {
	c := NewColumns(2)
	s := "package view"
	if got := c.WrapRows(s, 80, BreakHybrid); got != 1 {
		t.Errorf("rows = %d, want 1", got)
	}
	for off := 0; off <= len(s); off++ {
		row, col := c.RowOfBreaks(nil, s, off)
		if row != 0 || col != c.ColOf(s, off) {
			t.Errorf("off=%d: row=%d col=%d, want 0 and %d", off, row, col, c.ColOf(s, off))
		}
	}
}
