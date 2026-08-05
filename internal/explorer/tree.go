// Package explorer is the file tree sidebar.
package explorer

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Entry is one visible row in the tree.
type Entry struct {
	Path  string
	Name  string
	Depth int
	Dir   bool
	Open  bool
}

// Tree is a lazily expanded directory listing.
//
// Only expanded directories are read, so opening raj in a repository with a
// hundred thousand files costs one readdir of the root rather than a full walk.
type Tree struct {
	Root     string
	expanded map[string]bool
	entries  []Entry

	// ChangedOnly filters to files differing from git HEAD, plus anything
	// edited this session. Sessions without a repository fall back to the
	// session set alone.
	ChangedOnly bool
	changed     map[string]bool
	session     map[string]bool
}

// NewTree opens a directory.
func NewTree(root string) *Tree {
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	t := &Tree{
		Root:     abs,
		expanded: map[string]bool{abs: true},
		session:  map[string]bool{},
	}
	t.Refresh()
	return t
}

func (t *Tree) Entries() []Entry { return t.entries }

// Rel is a path relative to the tree root, for display. An unrelated path is
// returned as-is rather than as a chain of "..", which would be longer than the
// absolute path it is trying to shorten.
func (t *Tree) Rel(path string) string {
	if path == "" {
		return ""
	}
	rel, err := filepath.Rel(t.Root, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return path
	}
	return rel
}

// MarkChanged records a file edited this session, so it shows under the filter
// even when it has not been saved and git cannot see it yet.
func (t *Tree) MarkChanged(path string) {
	if abs, err := filepath.Abs(path); err == nil {
		t.session[abs] = true
	}
}

// Toggle expands or collapses a directory.
func (t *Tree) Toggle(path string) {
	t.expanded[path] = !t.expanded[path]
	t.Refresh()
}

// Expanded reports a directory's state.
func (t *Tree) Expanded(path string) bool { return t.expanded[path] }

// Refresh rebuilds the visible entries, re-reading git status when filtering.
func (t *Tree) Refresh() {
	if t.ChangedOnly {
		t.changed = gitChanged(t.Root)
	}
	t.entries = t.entries[:0]
	t.walk(t.Root, 0)
}

func (t *Tree) walk(dir string, depth int) {
	items, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].IsDir() != items[j].IsDir() {
			return items[i].IsDir() // directories first
		}
		return items[i].Name() < items[j].Name()
	})
	for _, it := range items {
		name := it.Name()
		if strings.HasPrefix(name, ".") && name != ".." {
			continue // dotfiles stay hidden; .git especially
		}
		path := filepath.Join(dir, name)
		if it.IsDir() {
			if t.ChangedOnly && !t.hasChanged(path) {
				continue
			}
			open := t.expanded[path]
			t.entries = append(t.entries, Entry{path, name, depth, true, open})
			if open {
				t.walk(path, depth+1)
			}
			continue
		}
		if t.ChangedOnly && !t.isChanged(path) {
			continue
		}
		t.entries = append(t.entries, Entry{path, name, depth, false, false})
	}
}

func (t *Tree) isChanged(path string) bool {
	return t.changed[path] || t.session[path]
}

// hasChanged reports whether a directory contains anything changed, so the
// filter shows the path down to a modified file rather than hiding its parents.
func (t *Tree) hasChanged(dir string) bool {
	prefix := dir + string(filepath.Separator)
	for p := range t.changed {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	for p := range t.session {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}

// gitChanged returns the absolute paths git reports as modified.
//
// Shelling out rather than linking a git library: one subprocess gets
// submodules, sparse checkouts, and ignore rules exactly right, where a library
// is a large dependency to approximate them. Failure is not an error — a
// directory that is not a repository simply has no git-changed files.
func gitChanged(root string) map[string]bool {
	out := map[string]bool{}
	cmd := exec.Command("git", "-C", root, "status", "--porcelain")
	data, err := cmd.Output()
	if err != nil {
		return out
	}
	top, err := exec.Command("git", "-C", root, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return out
	}
	base := strings.TrimSpace(string(top))
	for _, line := range strings.Split(string(data), "\n") {
		if len(line) < 4 {
			continue
		}
		name := line[3:]
		// Renames are reported as "old -> new"; the new path is the one that
		// exists on disk.
		if i := strings.Index(name, " -> "); i >= 0 {
			name = name[i+4:]
		}
		out[filepath.Join(base, strings.Trim(name, `"`))] = true
	}
	return out
}
