package search

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func docsTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func run(root string, q Query, open Docs) Result {
	return RunDocs(context.Background(), root, q, open)
}

func lines(res Result) []int {
	out := make([]int, 0, len(res.Matches))
	for _, m := range res.Matches {
		out = append(out, m.Line)
	}
	return out
}

// The point of the whole thing: an edit that has not been written yet is still
// findable. Before this, a search opened the file and scanned it, so what was
// on screen and what was searched were different documents.
func TestUnsavedEditIsFound(t *testing.T) {
	root := docsTree(t, map[string]string{"a.go": "package a\n"})
	path := filepath.Join(root, "a.go")

	if got := run(root, Query{Text: "needle"}, nil).Total(); got != 0 {
		t.Fatalf("setup: disk already holds %d matches", got)
	}
	res := run(root, Query{Text: "needle"}, Docs{path: "package a\n\nvar needle = 1\n"})
	if got := res.Total(); got != 1 {
		t.Fatalf("found %d matches in the buffer, want 1", got)
	}
	if res.Matches[0].Line != 3 {
		t.Errorf("match on line %d, want 3", res.Matches[0].Line)
	}
}

// The other half, and the one that is easy to forget: a match that is only on
// disk is stale. Reporting it sends you to a line that no longer says that.
func TestDeletedTextIsNotReported(t *testing.T) {
	root := docsTree(t, map[string]string{"a.go": "package a\n\nvar needle = 1\n"})
	path := filepath.Join(root, "a.go")

	if got := run(root, Query{Text: "needle"}, nil).Total(); got != 1 {
		t.Fatal("setup: the disk copy should match")
	}
	if got := run(root, Query{Text: "needle"}, Docs{path: "package a\n"}).Total(); got != 0 {
		t.Errorf("found %d stale matches from disk", got)
	}
}

// Line numbers come from the buffer, so a hit lands where the text actually is
// rather than where it was when the file was last written.
func TestLineNumbersComeFromTheBuffer(t *testing.T) {
	root := docsTree(t, map[string]string{"a.go": "needle\n"})
	path := filepath.Join(root, "a.go")

	res := run(root, Query{Text: "needle"}, Docs{path: "\n\n\n\nneedle\n"})
	if got := lines(res); len(got) != 1 || got[0] != 5 {
		t.Errorf("lines = %v, want [5]", got)
	}
}

// A file that exists only as a tab is not on the walk at all, so it has to be
// swept up afterwards rather than missed.
func TestBufferWithNothingOnDiskIsSearched(t *testing.T) {
	root := docsTree(t, map[string]string{"a.go": "package a\n"})
	ghost := filepath.Join(root, "never_written.go")

	res := run(root, Query{Text: "needle"}, Docs{ghost: "var needle = 1\n"})
	if res.Total() != 1 {
		t.Fatalf("found %d matches, want 1", res.Total())
	}
	if got := res.Matches[0].Path; got != ghost {
		t.Errorf("path = %q, want %q", got, ghost)
	}
}

// Unwritten documents are visited in a stable order. The walk is lexical, and a
// result list that reshuffles between identical searches is worse than one that
// is merely incomplete.
func TestUnwrittenBuffersAreOrdered(t *testing.T) {
	root := docsTree(t, map[string]string{"a.go": "package a\n"})
	open := Docs{}
	for _, n := range []string{"z.go", "m.go", "b.go", "q.go"} {
		open[filepath.Join(root, n)] = "needle\n"
	}
	want := []string{"b.go", "m.go", "q.go", "z.go"}
	for i := 0; i < 5; i++ {
		res := run(root, Query{Text: "needle"}, open)
		got := make([]string, 0, len(res.Matches))
		for _, m := range res.Matches {
			got = append(got, filepath.Base(m.Path))
		}
		for j := range want {
			if j >= len(got) || got[j] != want[j] {
				t.Fatalf("run %d: order %v, want %v", i, got, want)
			}
		}
	}
}

// An open document is included or excluded on the same terms as a saved one.
// Looser rules would make a glob mean one thing for the file you are editing
// and another for its neighbours.
func TestOpenDocumentsObeyTheSameFilters(t *testing.T) {
	root := docsTree(t, map[string]string{"a.go": "package a\n"})
	at := func(p string) string { return filepath.Join(root, p) }

	cases := []struct {
		name string
		path string
		q    Query
		want int
	}{
		{"include glob", at("new.md"), Query{Text: "needle", Include: "*.go"}, 0},
		{"include glob, matching", at("new.go"), Query{Text: "needle", Include: "*.go"}, 1},
		{"exclude glob", at("new.go"), Query{Text: "needle", Exclude: "*.go"}, 0},
		{"hidden file", at(".secret.go"), Query{Text: "needle"}, 0},
		{"hidden directory", at(".git/x.go"), Query{Text: "needle"}, 0},
		{"vendored", at("vendor/x.go"), Query{Text: "needle"}, 0},
		{"outside the root", "/elsewhere/x.go", Query{Text: "needle"}, 0},
		{"relative path", "x.go", Query{Text: "needle"}, 0},
		{"nested, plain", at("pkg/deep/x.go"), Query{Text: "needle"}, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := run(root, c.q, Docs{c.path: "needle\n"}).Total()
			if got != c.want {
				t.Errorf("found %d, want %d", got, c.want)
			}
		})
	}
}

// A document open at a path the walk also reaches must be counted once, from
// the buffer.
func TestOpenDocumentIsNotSearchedTwice(t *testing.T) {
	root := docsTree(t, map[string]string{"a.go": "needle\n"})
	path := filepath.Join(root, "a.go")

	res := run(root, Query{Text: "needle"}, Docs{path: "needle\n"})
	if got := res.Total(); got != 1 {
		t.Errorf("total = %d, want 1", got)
	}
	if res.Files != 1 {
		t.Errorf("files = %d, want 1", res.Files)
	}
}

// A buffer holding a NUL byte is binary by the same rule a file is, and one
// past the size cap is skipped by the same rule too. Neither should be a
// special case that only disk contents pass through.
func TestBufferBinaryAndSizeRules(t *testing.T) {
	root := docsTree(t, map[string]string{"a.go": "package a\n"})
	at := filepath.Join(root, "b.go")

	if got := run(root, Query{Text: "needle"}, Docs{at: "needle\x00\n"}).Total(); got != 0 {
		t.Errorf("scanned a buffer containing NUL: %d matches", got)
	}
	big := make([]byte, MaxFileSize+1)
	for i := range big {
		big[i] = 'x'
	}
	copy(big, "needle")
	if got := run(root, Query{Text: "needle"}, Docs{at: string(big)}).Total(); got != 0 {
		t.Errorf("scanned an oversized buffer: %d matches", got)
	}
}

// Nothing open is the old behaviour exactly, so the disk path cannot have
// regressed on the way through.
func TestNoOpenDocumentsMatchesRunContext(t *testing.T) {
	root := docsTree(t, map[string]string{
		"a.go": "needle one\nneedle two\n",
		"b.md": "needle three\n",
	})
	q := Query{Text: "needle"}
	want := RunContext(context.Background(), root, q)
	for _, got := range []Result{run(root, q, nil), run(root, q, Docs{})} {
		if got.Total() != want.Total() || got.Files != want.Files ||
			got.Considered != want.Considered {
			t.Errorf("got %d/%d/%d, want %d/%d/%d",
				got.Total(), got.Files, got.Considered,
				want.Total(), want.Files, want.Considered)
		}
	}
}
