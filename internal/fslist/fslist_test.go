package fslist

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"raj/internal/control"
)

// mkFile makes a file under root, creating parents, and returns its path.
func mkFile(t *testing.T, root, name, body string) string {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func containsString(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// List lists a directory's immediate children, marks directories and sizes
// regular files, and applies the same hidden policy the tree and search do.
// The fixture mirrors internal/app's hidden tests: a dotfile is visible by
// default and node_modules is not.
func TestListListsVisibleChildren(t *testing.T) {
	// Point the user-level hidden configuration at an empty directory, so the
	// test measures the defaults and not the developer's own ~/.config.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	dir := filepath.Join(root, "lsdir")
	mkFile(t, dir, "sub/b.go", "package sub\n")
	mkFile(t, dir, ".dot", "secret\n")
	mkFile(t, dir, "plain.txt", "plain\n")
	mkFile(t, dir, "node_modules/dep/i.js", "zqxhidden\n")

	got, err := List(dir, false, root, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range got {
		names = append(names, e.Name)
	}
	// Sorted, directories and files together; node_modules is hidden.
	want := []string{".dot", "plain.txt", "sub"}
	if !equalStrings(names, want) {
		t.Fatalf("list names = %v, want %v", names, want)
	}
	by := map[string]control.Entry{}
	for _, e := range got {
		by[e.Name] = e
	}
	if !by["sub"].Dir || by["sub"].Size != nil {
		t.Errorf("sub = %+v, want a directory with no size", by["sub"])
	}
	if by["sub"].Path != filepath.Join(dir, "sub") {
		t.Errorf("sub path = %q, want %q", by["sub"].Path, filepath.Join(dir, "sub"))
	}
	if s := by["plain.txt"].Size; s == nil || *s != int64(len("plain\n")) {
		t.Errorf("plain.txt size = %v, want %d", s, len("plain\n"))
	}
	if by[".dot"].Dir {
		t.Error(".dot was marked a directory")
	}

	// -hidden includes the entry the defaults hide.
	got, err = List(dir, true, root, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	names = names[:0]
	for _, e := range got {
		names = append(names, e.Name)
	}
	if !containsString(names, "node_modules") {
		t.Errorf("list -hidden names = %v, want node_modules included", names)
	}
}

// List refuses a file and a missing path, rather than reading either as an
// empty directory.
func TestListRefusesNonDirectory(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	file := mkFile(t, root, "plain.txt", "plain\n")
	if _, err := List(file, false, root, []string{root}); err == nil {
		t.Error("list of a file succeeded")
	}
	if _, err := List(filepath.Join(root, "nope"), false, root, []string{root}); err == nil {
		t.Error("list of a missing path succeeded")
	}
}

// List does not follow a symlinked directory: a child that is a symlink is an
// ordinary entry, its Dir flag stays false even when the target is a directory,
// and its target is never stat-ed (so the link is not sized as the regular file
// it points at). A sibling test pins the same shape for a real directory.
func TestListMarksSymlinkWithoutFollowing(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	dir := filepath.Join(root, "lsdir")
	real := filepath.Join(dir, "realdir")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "linkdir")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	got, err := List(dir, false, root, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]control.Entry{}
	for _, e := range got {
		by[e.Name] = e
	}
	if e, ok := by["realdir"]; !ok || !e.Dir {
		t.Errorf("realdir = %+v, want a directory", e)
	}
	e, ok := by["linkdir"]
	if !ok {
		t.Fatalf("list did not list the symlink: %+v", got)
	}
	if e.Dir {
		t.Errorf("linkdir = %+v, want Dir false for a symlink to a directory", e)
	}
	if e.Size != nil {
		t.Errorf("linkdir size = %d, want none for a symlink", *e.Size)
	}
}

// The -hidden search switch reaches hidden directories, which the default walk
// refuses. SearchHidden sets the search package's Hidden policy to "everything"
// where Search leaves the configured policy in place.
func TestSearchHiddenReachesHiddenDirectories(t *testing.T) {
	// Isolate the hidden policy the same way, so node_modules is hidden by the
	// defaults rather than by whatever the developer configured.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	mkFile(t, root, "node_modules/dep/i.js", "zqxhidden\n")
	mkFile(t, root, "src/main.go", "zqxhidden\n")

	searcher := SnapshotSearcher{Roots: []string{root}}
	find := func(hidden bool) []control.SearchMatch {
		var got []control.SearchMatch
		emit := func(b []control.SearchMatch) { got = append(got, b...) }
		var err error
		if hidden {
			_, _, _, _, err = searcher.SearchHidden(context.Background(),
				control.SearchQuery{Text: "zqxhidden"}, emit)
		} else {
			_, _, _, _, err = searcher.Search(context.Background(),
				control.SearchQuery{Text: "zqxhidden"}, emit)
		}
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	if got := find(false); len(got) != 1 || filepath.Base(filepath.Dir(got[0].Path)) != "src" {
		t.Errorf("default search found %+v, want only src/main.go", got)
	}
	if got := find(true); len(got) != 2 {
		t.Errorf("hidden search found %d hit(s), want both files: %+v", len(got), got)
	}
}

// search -context reaches the engine through the shared walk: runSearch
// forwards the query's Context, and the hit carries its neighbouring lines.
func TestSearchContextReachesTheEngine(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	mkFile(t, root, "main.go", "line one\nneedle here\nline three\n")
	searcher := SnapshotSearcher{Roots: []string{root}}

	var got []control.SearchMatch
	if _, _, _, _, err := searcher.Search(context.Background(),
		control.SearchQuery{Text: "needle", Context: 1},
		func(b []control.SearchMatch) { got = append(got, b...) }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("hits = %+v, want one", got)
	}
	if want := "line one\nneedle here\nline three"; got[0].Context != want {
		t.Errorf("context = %q, want %q", got[0].Context, want)
	}

	// Without the field the hit is the line alone, the behaviour before the
	// option existed.
	got = nil
	if _, _, _, _, err := searcher.Search(context.Background(),
		control.SearchQuery{Text: "needle"},
		func(b []control.SearchMatch) { got = append(got, b...) }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Context != "" {
		t.Errorf("plain hit = %+v, want no context", got)
	}
}
