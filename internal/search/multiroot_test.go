package search

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// RunStreamRoots walks every root, so a hit in the second root surfaces. The
// failure mode this pins: the walk used to start at one root, so a hit under
// the second workspace root was invisible to search.
func TestRunStreamRootsFindsHitsInEveryRoot(t *testing.T) {
	a := fixture(t)
	b := t.TempDir()
	if err := os.MkdirAll(filepath.Join(b, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	second := filepath.Join(b, "sub", "second.go")
	if err := os.WriteFile(second, []byte("package second\n// needle in the second root\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res := RunStreamRoots(context.Background(), []string{a, b}, Query{Text: "needle"}, nil, nil, nil)
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	var got []string
	for _, m := range res.Matches {
		got = append(got, m.Path)
	}
	sort.Strings(got)
	found := false
	for _, p := range got {
		if p == second {
			found = true
		}
	}
	if !found {
		t.Fatalf("no hit in the second root; paths = %v", got)
	}
	if len(got) < 2 {
		t.Fatalf("the first root lost its hits: %v", got)
	}
}

// Ordering is deterministic: roots in supplied order, and within a root the
// lexical walk the single-root search has always used. A result list that
// reshuffles between identical searches would be worse than incomplete.
func TestRunStreamRootsOrderIsDeterministic(t *testing.T) {
	a := t.TempDir()
	b := t.TempDir()
	first := filepath.Join(a, "z.go")
	second := filepath.Join(b, "y.go")
	if err := os.WriteFile(first, []byte("needle z\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("needle y\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res := RunStreamRoots(context.Background(), []string{a, b}, Query{Text: "needle"}, nil, nil, nil)
	if len(res.Matches) != 2 {
		t.Fatalf("matches = %+v", res.Matches)
	}
	if res.Matches[0].Path != first || res.Matches[1].Path != second {
		t.Errorf("order = %s, %s; want the first root first", res.Matches[0].Path, res.Matches[1].Path)
	}
}
