// Package explorer is the file tree sidebar.
package explorer

import (
	"hash/fnv"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"raj/internal/hidden"
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

	// sig digests the names and kinds of the children of every expanded
	// directory -- the listing the tree was last built from. ChangedOnDisk
	// compares a fresh digest against it. Names, not mtimes: the tree shows
	// names, and a file whose contents changed keeps its name, so no refresh
	// is needed for it (an open buffer's disk change is diskCheck's job).
	sig uint64

	// ChangedOnly filters to files differing from git HEAD, plus anything
	// edited this session. Sessions without a repository fall back to the
	// session set alone.
	ChangedOnly bool
	changed     map[string]bool
	session     map[string]bool

	// Hidden is the visibility policy. Shared shape with the search and the
	// picker so a pattern written once applies to all three; never nil after
	// NewTree, but nil is treated as "hide nothing" rather than panicking.
	Hidden *hidden.Rules
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
		Hidden:   hidden.Load(abs),
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

// ExpandedDirs lists the open directories, for a session to remember. Sorted so
// a session file does not churn between saves that changed nothing.
func (t *Tree) ExpandedDirs() []string {
	out := make([]string, 0, len(t.expanded))
	for p, open := range t.expanded {
		if open && p != t.Root {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// Expand opens a directory without toggling, so restoring a session cannot
// close something by replaying a path twice. Parents are opened too: a child
// marked open under a closed parent would never be reached by the walk.
func (t *Tree) Expand(path string) {
	for p := filepath.Clean(path); len(p) >= len(t.Root); p = filepath.Dir(p) {
		t.expanded[p] = true
		if p == t.Root || filepath.Dir(p) == p {
			break
		}
	}
}

// Refresh rebuilds the visible entries, re-reading git status when filtering.
func (t *Tree) Refresh() {
	if t.ChangedOnly {
		t.changed = gitChanged(t.Root)
	}
	t.entries = t.entries[:0]
	t.walk(t.Root, 0)
	t.sig = t.signature()
}

// children lists a directory's visible entries, directories first and each
// group sorted by name, with the hidden policy applied. Listing and ordering
// live in one place so walk and signature cannot disagree about what the tree
// considers a child.
func (t *Tree) children(dir string) []os.DirEntry {
	items, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].IsDir() != items[j].IsDir() {
			return items[i].IsDir() // directories first
		}
		return items[i].Name() < items[j].Name()
	})
	kept := items[:0]
	for _, it := range items {
		if t.Hidden.HiddenPath(t.Root, filepath.Join(dir, it.Name()), it.IsDir()) {
			continue
		}
		kept = append(kept, it)
	}
	return kept
}

func (t *Tree) walk(dir string, depth int) {
	for _, it := range t.children(dir) {
		name := it.Name()
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

// ChangedOnDisk reports whether the host filesystem moved under the tree since
// it was last built: a name added, removed or renamed in an expanded directory.
// It reads directory listings only -- no file stats, no git -- so the idle tick
// can afford to ask. A file's changed contents do not count: the tree shows
// names, and an open buffer's disk change is diskCheck's business.
func (t *Tree) ChangedOnDisk() bool { return t.signature() != t.sig }

// signature digests the names and kinds of the children of every expanded
// directory, recursively. It is deliberately independent of the ChangedOnly
// filter: that filter is git state, and recomputing it on every idle tick is
// the cost this check exists to avoid.
func (t *Tree) signature() uint64 {
	h := fnv.New64a()
	var walk func(dir string)
	walk = func(dir string) {
		for _, it := range t.children(dir) {
			path := filepath.Join(dir, it.Name())
			io.WriteString(h, path)
			h.Write([]byte{0})
			if it.IsDir() {
				h.Write([]byte{'d'})
				if t.expanded[path] {
					walk(path)
				}
				continue
			}
			h.Write([]byte{'f'})
		}
	}
	walk(t.Root)
	return h.Sum64()
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
