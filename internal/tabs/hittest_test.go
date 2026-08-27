package tabs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"raj/internal/ui"
	"raj/internal/widget"
)

// open builds a tab set over n temporary files with distinguishable names.
func open(t *testing.T, names ...string) *Tabs {
	t.Helper()
	dir := t.TempDir()
	tb := New(2)
	for _, n := range names {
		p := filepath.Join(dir, n)
		if err := os.WriteFile(p, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := tb.Open(p); err != nil {
			t.Fatal(err)
		}
	}
	return tb
}

// draw renders the bar and returns the row as it appears on screen.
func draw(tb *Tabs, w int) (*ui.Screen, string) {
	s := ui.NewScreen(w, 1)
	tb.Render(s, 0, 0, w, widget.DefaultTheme())
	return s, s.Row(0)
}

// Across every width, hit-testing left to right must return tab indices that
// only ever increase, and never one that does not exist. A mapping that is not
// monotonic is one where two tabs overlap, which no bar can draw — so it would
// mean the spans and the renderer had come apart even though each looks sane
// on its own.
func TestHitTestIsMonotonicAtEveryWidth(t *testing.T) {
	for _, w := range []int{120, 60, 40, 24, 12, 6} {
		tb := open(t, "alpha.go", "beta.go", "gamma.go", "delta.go")
		for _, active := range []int{1, 2, 3, 4} {
			tb.Goto(active)
			draw(tb, w)

			last := -1
			for col := 0; col < w; col++ {
				i, ok := tb.HitTest(0, w, col)
				if !ok {
					continue
				}
				if i < 0 || i >= tb.Count() {
					t.Fatalf("w=%d col=%d: tab %d of %d", w, col, i, tb.Count())
				}
				if i < last {
					t.Fatalf("w=%d: column %d hit tab %d after tab %d", w, col, i, last)
				}
				last = i
			}
		}
	}
}

// A press must never select a tab whose label does not reach that column. This
// is the assertion that fails on an off-by-one: the boundary between two tabs
// is one column wide and both sides of it are plausible.
func TestHitTestBoundariesMatchTheLabels(t *testing.T) {
	tb := open(t, "alpha.go", "beta.go", "gamma.go")
	const w = 120
	_, row := draw(tb, w)

	for i, p := range tb.All() {
		name := p.File.Name()
		start := colOf(row, name)
		if start < 0 {
			t.Fatalf("%s is not on the bar: %q", name, row)
		}
		for col := start; col < start+len(name); col++ {
			got, ok := tb.HitTest(0, w, col)
			if !ok || got != i {
				t.Errorf("column %d of %s hit (%d, %v), want (%d, true)",
					col, name, got, ok, i)
			}
		}
		// The separator after a tab belongs to neither side.
		if sep := start + len(name) + 1; i < tb.Count()-1 {
			if _, ok := tb.HitTest(0, w, sep); ok {
				t.Errorf("the separator at column %d was claimed by a tab", sep)
			}
		}
	}
}

// Hit-testing an overflowing bar has to account for the scroll, which is set by
// which tab is active. A bar scrolled to reveal the last tab must resolve a
// press on it to that tab and not to whatever used to be drawn there.
func TestHitTestFollowsTheScroll(t *testing.T) {
	tb := open(t, "aaaaaaaa.go", "bbbbbbbb.go", "cccccccc.go", "dddddddd.go", "eeeeeeee.go")
	const w = 30
	tb.Goto(tb.Count()) // activate the last tab, forcing the bar to scroll
	_, row := draw(tb, w)

	name := tb.All()[tb.Count()-1].File.Name()
	start := colOf(row, name)
	if start < 0 {
		t.Fatalf("the active tab is not visible on a scrolled bar: %q", row)
	}
	got, ok := tb.HitTest(0, w, start)
	if !ok || got != tb.Count()-1 {
		t.Errorf("column %d hit (%d, %v), want the last tab", start, got, ok)
	}
}

// Presses outside the bar belong to nothing.
func TestHitTestRejectsOutside(t *testing.T) {
	tb := open(t, "a.go")
	if _, ok := tb.HitTest(10, 20, 5); ok {
		t.Error("a press left of the bar was claimed")
	}
	if _, ok := tb.HitTest(10, 20, 30); ok {
		t.Error("a press right of the bar was claimed")
	}
	if _, ok := New(2).HitTest(0, 20, 5); ok {
		t.Error("an empty bar claimed a press")
	}
}

// CloseIndex leaves the same tab active that Close would, whichever side of the
// active tab was closed. The pointer can close any tab, so this is no longer
// only a question about the active one.
func TestCloseIndexKeepsTheRightTabActive(t *testing.T) {
	cases := []struct {
		name   string
		active int
		close  int
		want   string
	}{
		{"before the active one", 2, 0, "c.go"},
		{"the active one", 1, 1, "c.go"},
		{"after the active one", 1, 2, "b.go"},
		{"the last one while active", 2, 2, "b.go"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tb := open(t, "a.go", "b.go", "c.go")
			tb.Goto(tc.active + 1)
			tb.CloseIndex(tc.close)
			if got := tb.Active().File.Name(); got != tc.want {
				t.Errorf("active is %s, want %s", got, tc.want)
			}
		})
	}
}

// colOf is the display column a name starts at on a rendered row. The bar draws
// multi-byte separators, so a byte index is not a column.
func colOf(row, name string) int {
	i := strings.Index(row, name)
	if i < 0 {
		return -1
	}
	col := 0
	for _, r := range row[:i] {
		col += ui.RuneWidth(r)
	}
	return col
}
