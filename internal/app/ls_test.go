package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"raj/internal/control"
)

// mkLsFile makes a file under root, creating parents, and returns its path.
func mkLsFile(t *testing.T, root, name, body string) string {
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

// ls lists a directory's immediate children, marks directories and sizes
// regular files, and applies the same hidden policy the tree and search do.
// The fixture mirrors internal/app/hidden_test.go's workspace: a dotfile is
// visible by default and node_modules is not.
func TestLsListsVisibleChildren(t *testing.T) {
	// Point the user-level hidden configuration at an empty directory, so the
	// test measures the defaults and not the developer's own ~/.config.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	h := newHarness(t, "hello\n")
	dir := filepath.Join(h.root, "lsdir")
	mkLsFile(t, dir, "sub/b.go", "package sub\n")
	mkLsFile(t, dir, ".dot", "secret\n")
	mkLsFile(t, dir, "plain.txt", "plain\n")
	mkLsFile(t, dir, "node_modules/dep/i.js", "zqxhidden\n")

	got, err := host{h.App}.Ls(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range got {
		names = append(names, e.Name)
	}
	// Sorted, directories and files together; node_modules is hidden.
	want := []string{".dot", "plain.txt", "sub"}
	if !equal(names, want) {
		t.Fatalf("ls names = %v, want %v", names, want)
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
	got, err = host{h.App}.Ls(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	names = names[:0]
	for _, e := range got {
		names = append(names, e.Name)
	}
	if !contains(names, "node_modules") {
		t.Errorf("ls -hidden names = %v, want node_modules included", names)
	}
}

// ls refuses a file and a missing path, rather than reading either as an empty
// directory.
func TestLsRefusesNonDirectory(t *testing.T) {
	h := newHarness(t, "hello\n")
	file := mkLsFile(t, h.root, "plain.txt", "plain\n")
	if _, err := (host{h.App}).Ls(file, false); err == nil {
		t.Error("ls of a file succeeded")
	}
	if _, err := (host{h.App}).Ls(filepath.Join(h.root, "nope"), false); err == nil {
		t.Error("ls of a missing path succeeded")
	}
}

// The -hidden search switch reaches hidden directories, which the default walk
// refuses. The flag rides on the query, guardedSearcher dispatches it to the
// app's include-hidden walk, and that walk sets the search package's Hidden
// policy to "everything".
func TestSearchHiddenReachesHiddenDirectories(t *testing.T) {
	// Isolate the hidden policy the same way, so node_modules is hidden by the
	// defaults rather than by whatever the developer configured.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	h := newHarness(t, "hello\n")
	mkLsFile(t, h.root, "node_modules/dep/i.js", "zqxhidden\n")
	mkLsFile(t, h.root, "src/main.go", "zqxhidden\n")

	// Go through the guard, as the control path does: the include-hidden
	// dispatch lives in guardedSearcher.Search (host.go), and the host's own
	// Snapshot returns the raw searcher with no hidden switch.
	searcher := control.NewGuard(host{h.App}).Snapshot()
	find := func(hidden bool) []control.SearchMatch {
		var got []control.SearchMatch
		if _, _, _, _, err := searcher.Search(context.Background(),
			control.SearchQuery{Text: "zqxhidden", Hidden: hidden},
			func(b []control.SearchMatch) { got = append(got, b...) }); err != nil {
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

// ls does not follow a symlinked directory: a child that is a symlink is an
// ordinary entry, its Dir flag stays false even when the target is a
// directory, and its target is never stat-ed (so the link is not sized as the
// regular file it points at). Modelled on TestLsListsVisibleChildren, which
// pins the same shape for a real directory.
func TestLsMarksSymlinkWithoutFollowing(t *testing.T) {
	// Isolate the hidden policy the way the sibling does.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	h := newHarness(t, "hello\n")
	dir := filepath.Join(h.root, "lsdir")
	real := filepath.Join(dir, "realdir")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "linkdir")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	got, err := host{h.App}.Ls(dir, false)
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
		t.Fatalf("ls did not list the symlink: %+v", got)
	}
	if e.Dir {
		t.Errorf("linkdir = %+v, want Dir false for a symlink to a directory", e)
	}
	if e.Size != nil {
		t.Errorf("linkdir size = %d, want none for a symlink", *e.Size)
	}
}
