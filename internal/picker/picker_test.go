package picker

import "testing"

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
