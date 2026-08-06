package search

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fixture builds a tree where one lexically-early file holds far more matches
// than the per-file cap, and the interesting content is in a directory the walk
// reaches afterwards. This is the shape that made the old global-only cap
// report a common term as if it lived in a handful of root files.
func capFixture(t *testing.T, hogLines int) string {
	t.Helper()
	root := t.TempDir()
	write := func(rel, body string) {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var hog strings.Builder
	for i := 0; i < hogLines; i++ {
		hog.WriteString("needle here\n")
	}
	write("AAAA_hog.txt", hog.String()) // sorts first
	write("zzz_dir/late.txt", "needle in a later directory\n")
	write("zzz_dir/deeper/deepest.txt", "needle at the bottom\n")
	return root
}

func TestOneBigFileDoesNotEatTheBudget(t *testing.T) {
	root := capFixture(t, MaxPerFile*4)
	res := Run(root, Query{Text: "needle"})

	if res.Files != 3 {
		t.Errorf("files = %d, want 3 — later directories were never reached", res.Files)
	}
	var late bool
	for _, m := range res.Matches {
		if strings.Contains(m.Path, "deepest.txt") {
			late = true
		}
	}
	if !late {
		t.Error("the deepest file produced no match; the walk stopped early")
	}
}

func TestPerFileCapLimitsRowsButNotTheCount(t *testing.T) {
	const hits = MaxPerFile * 4
	root := capFixture(t, hits)
	res := Run(root, Query{Text: "needle"})

	var shown int
	for _, m := range res.Matches {
		if strings.Contains(m.Path, "AAAA_hog") {
			shown++
		}
	}
	if shown != MaxPerFile {
		t.Errorf("showed %d rows for the big file, want %d", shown, MaxPerFile)
	}

	hog := filepath.Join(root, "AAAA_hog.txt")
	if got := res.Counts[hog]; got != hits {
		t.Errorf("counted %d matches in the big file, want %d", got, hits)
	}
	if got, want := res.Total(), hits+2; got != want {
		t.Errorf("Total() = %d, want %d", got, want)
	}
}

// A file under the cap must be unaffected — the cap is a ceiling, not a
// rewrite of how ordinary results are counted.
func TestSmallFilesAreUnaffected(t *testing.T) {
	root := capFixture(t, 3)
	res := Run(root, Query{Text: "needle"})

	if got, want := len(res.Matches), 5; got != want {
		t.Errorf("matches = %d, want %d", got, want)
	}
	if got, want := res.Total(), 5; got != want {
		t.Errorf("Total() = %d, want %d", got, want)
	}
	if res.Capped {
		t.Error("a small search reported itself capped")
	}
}

// The header is where the difference becomes visible. Showing the row count
// would turn a display limit into an understatement of the search itself.
func TestHeaderReportsTheTrueTotal(t *testing.T) {
	const hits = MaxPerFile * 4
	p := NewPane(capFixture(t, hits))
	p.Debounce = 0
	typeQuery(p, "needle")
	if !p.Settle(5 * time.Second) {
		t.Fatal("search did not settle")
	}

	screen := draw(p, 60, 30)
	// The results header states both numbers: how many rows are on screen and
	// how many the search actually found. Asserting the joined string matters,
	// because the per-file header nearby also carries an "of N" and would
	// otherwise satisfy a looser check on its own.
	shown := MaxPerFile + 2 // the capped file, plus one match in each small one
	total := hits + 2
	if want := fmt.Sprintf("%d of %d results in 3 files", shown, total); !strings.Contains(screen, want) {
		t.Errorf("results header missing %q:\n%s", want, screen)
	}
	// And the file that was cut down says so on its own row.
	if want := fmt.Sprintf("(%d of %d)", MaxPerFile, hits); !strings.Contains(screen, want) {
		t.Errorf("file header missing %q:\n%s", want, screen)
	}
}
