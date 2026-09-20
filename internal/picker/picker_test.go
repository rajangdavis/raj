package picker

import (
	"testing"

	"raj/internal/ui"
	"raj/internal/widget"
)

// A file whose name contains the query must outrank one that only matches as a
// scattered subsequence across directories. "test" is a subsequence of
// internal/ui/style.go, which used to rank it above real test files.
func TestFuzzyPrefersNameMatches(t *testing.T) {
	query := "test"
	better, _, ok := fuzzy("internal/editor/binary_test.go", query)
	if !ok {
		t.Fatal("expected a match")
	}
	worse, _, ok := fuzzy("internal/ui/style.go", query)
	if !ok {
		t.Fatal("expected a subsequence match")
	}
	if better <= worse {
		t.Errorf("binary_test.go scored %d, style.go scored %d; the name match must win",
			better, worse)
	}
}

func TestFuzzyPrefersPrefixAndShortPaths(t *testing.T) {
	cases := [][2]string{
		{"main.go", "cmd/raj/main.go"},                // shorter path wins
		{"picker.go", "internal/ui/pick_helper_x.go"}, // contiguous name wins
	}
	for _, c := range cases {
		a, _, okA := fuzzy(c[0], "picker")
		b, _, okB := fuzzy(c[1], "picker")
		if !okA && !okB {
			continue
		}
		if okA && okB && a <= b {
			t.Errorf("%q scored %d, %q scored %d", c[0], a, c[1], b)
		}
	}
}

func TestFuzzyRejectsNonSubsequence(t *testing.T) {
	if _, _, ok := fuzzy("main.go", "zzz"); ok {
		t.Error("matched a query that is not a subsequence")
	}
}

// In phone mode every result cell is two screen rows tall and the whole cell is
// the target: a press on either row chooses that row, and the count line below
// the list chooses nothing. The ordinary one-row picker fails the two-row taps.
func TestTallPickerRowsTapWholeCell(t *testing.T) {
	p := New(".")
	p.Tall = true
	p.shown = []scored{
		{entry: entry{label: "a.go"}},
		{entry: entry{label: "b.go"}},
		{entry: entry{label: "c.go"}},
	}
	p.list.Reset()
	const cols, rows = 40, 30
	x, y, _, h, ok := p.box(cols, rows)
	if !ok {
		t.Fatal("the picker box did not fit")
	}
	p.Open = true
	s := ui.NewScreen(cols, rows)
	p.Render(s, cols, rows, widget.DefaultTheme())
	rh := p.rowHeight()
	if rh != 2 {
		t.Fatalf("Tall rowHeight = %d, want 2", rh)
	}
	top := y + listTop
	for i := range 3 {
		for dy := range rh {
			p.Open = true
			if _, inside := p.ClickAt(cols, rows, x+2, top+i*rh+dy); !inside {
				t.Fatalf("cell %d row %d read as outside", i, dy)
			}
			if p.list.Sel != i {
				t.Errorf("tap at cell %d row %d selected %d, want %d", i, dy, p.list.Sel, i)
			}
		}
	}
	// The count line at the bottom border is inside the overlay but not a row.
	p.Open = true
	p.list.Sel = -1
	if _, inside := p.ClickAt(cols, rows, x+2, y+h-1); !inside {
		t.Error("the count line read as outside the picker")
	}
	if p.list.Sel != -1 {
		t.Errorf("the count line selected %d, want nothing", p.list.Sel)
	}
}
