package app

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"raj/internal/explorer"
	"raj/internal/picker"
	"raj/internal/search"
)

// The three views of the workspace — the tree, quick-open and search — each ran
// their own copy of "skip anything starting with a dot", which is why a file
// like .gitlab-ci.yml was unreachable from all three at once and configurable
// in none. They now share internal/hidden, and this test is what says so: it
// asserts the same visible set through all three, so a fourth walk written
// later cannot quietly diverge.

// tree lists every file the explorer reaches, expanding everything, as paths
// relative to the root.
func visibleTree(t *testing.T, root string) []string {
	t.Helper()
	tr := explorer.NewTree(root)
	// Expand repeatedly: each pass reveals the next level down, and a fixture
	// is only a few deep.
	for i := 0; i < 8; i++ {
		for _, e := range tr.Entries() {
			if e.Dir && !tr.Expanded(e.Path) {
				tr.Toggle(e.Path)
			}
		}
	}
	var out []string
	for _, e := range tr.Entries() {
		if !e.Dir {
			out = append(out, filepath.ToSlash(tr.Rel(e.Path)))
		}
	}
	sort.Strings(out)
	return out
}

func visiblePicker(t *testing.T, root string) []string {
	t.Helper()
	p := picker.New(root)
	p.Show()
	out := make([]string, 0, len(p.Files()))
	for _, f := range p.Files() {
		out = append(out, filepath.ToSlash(f))
	}
	sort.Strings(out)
	return out
}

// visibleSearch searches for a token every fixture file contains, so the set of
// files with a match is the set of files the walk was willing to open.
func visibleSearch(t *testing.T, root string) []string {
	t.Helper()
	res := search.Run(root, search.Query{Text: "token", Hidden: nil})
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	seen := map[string]bool{}
	var out []string
	for _, m := range res.Matches {
		rel, err := filepath.Rel(root, m.Path)
		if err != nil || seen[rel] {
			continue
		}
		seen[rel] = true
		out = append(out, filepath.ToSlash(rel))
	}
	sort.Strings(out)
	return out
}

func fixtureWorkspace(t *testing.T, files map[string]string) string {
	t.Helper()
	// Point the user-level configuration at an empty directory: the answer
	// under test is the defaults plus the workspace, not the developer's own
	// ~/.config/raj/hidden.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	for name, body := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

var workspaceFiles = map[string]string{
	"README.md":               "token\n",
	"src/main.go":             "token\n",
	".gitlab-ci.yml":          "token\n",
	".github/workflows/c.yml": "token\n",
	".gitignore":              "token\n",
	".git/config":             "token\n",
	"node_modules/dep/i.js":   "token\n",
	"vendor/lib/v.go":         "token\n",
	"build/out.txt":           "token\n",
}

func TestAllThreeWalksAgree(t *testing.T) {
	root := fixtureWorkspace(t, workspaceFiles)
	want := []string{
		".github/workflows/c.yml",
		".gitignore",
		".gitlab-ci.yml",
		"README.md",
		"build/out.txt",
		"src/main.go",
	}
	for _, c := range []struct {
		name string
		got  []string
	}{
		{"explorer", visibleTree(t, root)},
		{"picker", visiblePicker(t, root)},
		{"search", visibleSearch(t, root)},
	} {
		if !equal(c.got, want) {
			t.Errorf("%s sees %v, want %v", c.name, c.got, want)
		}
	}
}

// The reported bug, stated as its own case so a regression names itself.
func TestDotfilesAreVisibleByDefault(t *testing.T) {
	root := fixtureWorkspace(t, workspaceFiles)
	for _, got := range [][]string{visibleTree(t, root), visiblePicker(t, root), visibleSearch(t, root)} {
		if !contains(got, ".gitlab-ci.yml") {
			t.Errorf(".gitlab-ci.yml missing from %v", got)
		}
		if contains(got, ".git/config") {
			t.Errorf(".git contents leaked into %v", got)
		}
	}
}

// A workspace file changes all three at once, which is the point of sharing the
// policy rather than three copies of it.
func TestWorkspaceConfigAppliesEverywhere(t *testing.T) {
	root := fixtureWorkspace(t, workspaceFiles)
	os.MkdirAll(filepath.Join(root, ".raj"), 0o755)
	os.WriteFile(filepath.Join(root, ".raj", "hidden"),
		// The comment carries the fixture's token so this file counts as
		// visible for the search the same way it does for the other two.
		[]byte("# token\nbuild/\n!vendor/\n.gitignore\n"), 0o644)

	// .raj/hidden lists itself among the visible files on purpose: it is a
	// file the user wrote and will want to edit, and a configuration file you
	// cannot open from the editor it configures is a trap.
	want := []string{
		".github/workflows/c.yml",
		".gitlab-ci.yml",
		".raj/hidden",
		"README.md",
		"src/main.go",
		"vendor/lib/v.go",
	}
	for _, c := range []struct {
		name string
		got  []string
	}{
		{"explorer", visibleTree(t, root)},
		{"picker", visiblePicker(t, root)},
		{"search", visibleSearch(t, root)},
	} {
		if !equal(c.got, want) {
			t.Errorf("%s sees %v, want %v", c.name, c.got, want)
		}
	}
}

func equal(a, b []string) bool {
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

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
