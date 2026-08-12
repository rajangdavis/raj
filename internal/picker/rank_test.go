package picker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"raj/internal/keys"
)

// realistic is a tree shaped like a Go project, including the collisions that
// make ranking hard: several files whose names contain another file's whole
// name, and directories whose letters overlap the names inside them.
var realistic = []string{
	"README.md",
	"cmd/raj/main.go",
	"cmd/keyprobe/README.md",
	"internal/app/app.go",
	"internal/app/render.go",
	"internal/app/search_buffer_test.go",
	"internal/piecetable/buffer_test.go",
	"internal/piecetable/piecetable.go",
	"internal/picker/picker.go",
	"internal/picker/picker_test.go",
	"internal/search/search.go",
	"internal/search/pane.go",
	"internal/keys/table.go",
	"internal/keys/keymap.go",
	"internal/symbols/symbols.go",
	"internal/tabs/tabs.go",
}

func project(t *testing.T) *Picker {
	t.Helper()
	root := t.TempDir()
	for _, f := range realistic {
		full := filepath.Join(root, f)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	p := New(root)
	p.Show()
	return p
}

func typed(p *Picker, q string) {
	p.Show()
	for _, ch := range strings.Split(q, "") {
		p.Handle(keys.None, ch)
	}
}

// The contract, and the single most common thing anyone does in a file picker:
// type a file's name and have that file first.
//
// This is what greedy leftmost matching got wrong. Scoring ran over the whole
// path from the left, so a query character could be spent on a directory —
// "buffer_test.go" gave its b to the b in "piecetable", splitting the real name
// into two runs, while search_buffer_test.go had no b before "buffer" and
// matched as one. Contiguity is quadratic and unbounded, so the longer
// accidental run beat the exact name.
func TestExactNameRanksFirst(t *testing.T) {
	p := project(t)
	for _, want := range realistic {
		base := filepath.Base(want)
		if strings.Count(strings.Join(realistic, " "), "/"+base) > 1 || base == "README.md" {
			continue // genuinely ambiguous: two files really are called this
		}
		typed(p, base)
		if p.Results() == 0 {
			t.Errorf("%q: typing its own name matched nothing", want)
			continue
		}
		if got := p.Top(); got != filepath.FromSlash(want) {
			t.Errorf("typing %q ranked %q first, want %q", base, got, want)
		}
	}
}

// The specific collision, called out on its own so a regression names itself.
func TestNameContainedInAnotherName(t *testing.T) {
	p := project(t)
	typed(p, "buffer_test.go")
	if got := p.Top(); got != filepath.FromSlash("internal/piecetable/buffer_test.go") {
		t.Errorf("top = %q, want the file actually called buffer_test.go", got)
	}
	// The longer name is still a match, just not the best one.
	if p.Results() != 2 {
		t.Errorf("results = %d, want both files", p.Results())
	}
}

// A prefix of a name ranks that name above files that merely contain the
// letters somewhere.
func TestPrefixesRankFirst(t *testing.T) {
	p := project(t)
	cases := map[string]string{
		"symb":    "internal/symbols/symbols.go",
		"tabs":    "internal/tabs/tabs.go",
		"keymap":  "internal/keys/keymap.go",
		"render":  "internal/app/render.go",
		"pane":    "internal/search/pane.go",
		"main.go": "cmd/raj/main.go",
	}
	for q, want := range cases {
		typed(p, q)
		if got := p.Top(); got != filepath.FromSlash(want) {
			t.Errorf("typing %q ranked %q first, want %q", q, got, want)
		}
	}
}

// Anchoring to the name must not cost path queries: a query with a separator,
// or one whose letters are not all in the name, still matches across the path.
// That is what makes typing a directory fragment work.
func TestPathQueriesStillMatch(t *testing.T) {
	p := project(t)
	cases := map[string]string{
		"picker/picker": "internal/picker/picker.go",
		"keys/table":    "internal/keys/table.go",
		"cmd/main":      "cmd/raj/main.go",
		"piecetablepie": "internal/piecetable/piecetable.go",
	}
	for q, want := range cases {
		typed(p, q)
		if p.Results() == 0 {
			t.Errorf("typing %q matched nothing", q)
			continue
		}
		if got := p.Top(); got != filepath.FromSlash(want) {
			t.Errorf("typing %q ranked %q first, want %q", q, got, want)
		}
	}
}

// Case is ignored, in both directions.
func TestCaseInsensitive(t *testing.T) {
	p := project(t)
	for _, q := range []string{"readme.md", "README.MD", "ReAdMe"} {
		typed(p, q)
		if p.Results() == 0 {
			t.Errorf("typing %q matched nothing", q)
		}
	}
}
