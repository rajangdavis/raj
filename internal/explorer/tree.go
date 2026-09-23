// Package explorer is the file tree sidebar.
package explorer

import (
	"hash/fnv"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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
	// Root is the primary root, kept for the single-root readers and identical
	// to Roots[0] when there is one. Roots is the whole set in supplied order;
	// with more than one, each is drawn as a top-level row.
	Root     string
	Roots    []string
	expanded map[string]bool
	entries  []Entry
	// expandAll names the directories whose whole subtree the current
	// Refresh is opening. ExpandAll sets it; the walk consumes it, opening
	// each directory it meets and writing the result into expanded, then
	// Refresh clears it. See ExpandAll.
	expandAll map[string]bool

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

// NewTree opens a directory. It is NewTreeRoots for a single root, so the
// one-root callers and tests keep the old behaviour exactly: the root itself is
// not a row and its children start at depth zero.
func NewTree(root string) *Tree { return NewTreeRoots([]string{root}) }

// NewTreeRoots opens a set of roots. With one root the tree is exactly what it
// always was. With several, each root is a top-level directory row and each
// expands and refreshes independently; a root row is labelled by its base name
// when that name is unique in the set, and by its absolute path when two roots
// share one, so two directories called "app" cannot be silently merged.
func NewTreeRoots(roots []string) *Tree {
	abs := canonicalRoots(roots)
	if len(abs) == 0 {
		// No workspace root: keep the old NewTree("") reading, which listed
		// the process directory, rather than an empty tree.
		if cwd, err := filepath.Abs(""); err == nil {
			abs = []string{cwd}
		}
	}
	primary := ""
	if len(abs) > 0 {
		primary = abs[0]
	}
	t := &Tree{
		Root:      primary,
		Roots:     abs,
		expanded:  map[string]bool{},
		expandAll: map[string]bool{},
		session:   map[string]bool{},
		Hidden:    hidden.Load(abs),
	}
	for _, r := range abs {
		t.expanded[r] = true
	}
	t.Refresh()
	return t
}

// canonicalRoots makes each non-empty root absolute and cleaned, drops exact
// duplicates keeping the first, and removes one root nested inside another (the
// outer one already reaches the inner one, so drawing both would double it).
func canonicalRoots(roots []string) []string {
	var out []string
	for _, r := range roots {
		if r == "" {
			continue
		}
		abs, err := filepath.Abs(r)
		if err != nil {
			abs = filepath.Clean(r)
		}
		if slices.Contains(out, abs) {
			continue
		}
		nested := false
		for _, prev := range out {
			if abs == prev || strings.HasPrefix(abs, prev+string(filepath.Separator)) ||
				strings.HasPrefix(prev, abs+string(filepath.Separator)) {
				nested = true
				break
			}
		}
		if nested {
			continue
		}
		out = append(out, abs)
	}
	return out
}

// isRoot reports whether path is one of the tree's root directories.
func (t *Tree) isRoot(path string) bool { return slices.Contains(t.Roots, path) }

// rootOf returns the root containing path: the deepest one, though roots are
// not nested, so it is simply the one that holds it. The primary is the
// fallback for a path no root contains.
func (t *Tree) rootOf(path string) string {
	best := ""
	for _, r := range t.Roots {
		if path == r || strings.HasPrefix(path, r+string(filepath.Separator)) {
			if len(r) > len(best) {
				best = r
			}
		}
	}
	if best == "" {
		return t.Root
	}
	return best
}

// rootLabel is the row label for root i: its base name when that name is unique
// in the set, and its absolute path when it collides with another root.
func (t *Tree) rootLabel(i int) string {
	name := filepath.Base(t.Roots[i])
	for j, r := range t.Roots {
		if j != i && filepath.Base(r) == name {
			return t.Roots[i]
		}
	}
	return name
}

func (t *Tree) Entries() []Entry { return t.entries }

// Rel is a path relative to the root that contains it, for display. An
// unrelated path is returned as-is rather than as a chain of "..", which would
// be longer than the absolute path it is trying to shorten. For a root row
// itself the label is returned, so the path line names the root rather than
// reading ".". With one root this is exactly the old relative spelling.
func (t *Tree) Rel(path string) string {
	if path == "" {
		return ""
	}
	if len(t.Roots) > 1 {
		for i, r := range t.Roots {
			if path == r {
				return t.rootLabel(i)
			}
		}
		for _, r := range t.Roots {
			rel, err := filepath.Rel(r, path)
			if err == nil && !strings.HasPrefix(rel, "..") {
				return rel
			}
		}
		return path
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

// ExpandAll opens path and every directory under it. The subtree is loaded by
// the same walk that draws the rows: path is marked in expandAll, and walk
// opens the directories it meets from there down, materialising each into
// expanded as it goes. A recursive expand therefore costs one walk of the
// subtree -- the walk Refresh already does for expanded directories -- rather
// than one walk to find the directories and a second to draw them. The mark is
// consumed and cleared by that Refresh, so a later manual Toggle sees ordinary
// state and can close a single directory again.
//
// The work is bounded by the subtree: one ReadDir per directory the expansion
// actually brings into view. It is synchronous, like every other Refresh; a
// subtree too large to load is too large to display, and the alternative -- a
// background walk feeding rows in later -- would be a second loader.
func (t *Tree) ExpandAll(path string) {
	path = filepath.Clean(path)
	t.Expand(path) // parents too, or a collapsed parent hides the subtree
	t.expanded[path] = true
	if t.expandAll == nil {
		t.expandAll = map[string]bool{}
	}
	t.expandAll[path] = true
	t.Refresh()
}

// CollapseAll closes every directory under path, leaving path itself open so
// the children it directly holds stay visible. That is what makes the gesture
// a toggle: the selected directory stays put and the subtree folds beneath it.
func (t *Tree) CollapseAll(path string) {
	path = filepath.Clean(path)
	prefix := path + string(filepath.Separator)
	for p := range t.expanded {
		if strings.HasPrefix(p, prefix) {
			delete(t.expanded, p)
		}
	}
	delete(t.expandAll, path)
	t.Refresh()
}

// ToggleExpandAll is one gesture with two states: if the whole subtree under
// path is already open it collapses, otherwise it expands. Deciding from the
// state of the subtree rather than the selected directory's own flag is what
// makes a partially collapsed subtree expand on the first press instead of
// folding the rest of it away.
func (t *Tree) ToggleExpandAll(path string) {
	if t.allExpanded(path) {
		t.CollapseAll(path)
		return
	}
	t.ExpandAll(path)
}

// allExpanded reports whether path and every directory the tree has loaded
// under it are open. Because walk loads children only of an open directory, a
// closed descendant that exists is loaded the moment its parent opens; so an
// expanded path with no closed loaded descendant has its whole subtree open.
// No filesystem walk is needed to answer it -- the question is about the state
// the tree already holds.
func (t *Tree) allExpanded(path string) bool {
	if !t.expanded[path] {
		return false
	}
	prefix := path + string(filepath.Separator)
	for _, e := range t.entries {
		if e.Dir && !e.Open && strings.HasPrefix(e.Path, prefix) {
			return false
		}
	}
	return true
}

// ExpandedDirs lists the open directories, for a session to remember. Sorted so
// a session file does not churn between saves that changed nothing. The roots
// are always open -- they are drawn as themselves, not as a directory inside
// one -- so they are left out.
func (t *Tree) ExpandedDirs() []string {
	out := make([]string, 0, len(t.expanded))
	for p, open := range t.expanded {
		if open && !t.isRoot(p) {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// Expand opens a directory without toggling, so restoring a session cannot
// close something by replaying a path twice. Parents are opened too: a child
// marked open under a closed parent would never be reached by the walk. The
// walk stops at the root that contains path, so a session path under the second
// root opens the second root rather than walking up to the primary.
func (t *Tree) Expand(path string) {
	root := t.rootOf(filepath.Clean(path))
	for p := filepath.Clean(path); len(p) >= len(root); p = filepath.Dir(p) {
		t.expanded[p] = true
		if p == root || filepath.Dir(p) == p {
			break
		}
	}
	if root != "" {
		t.expanded[root] = true
	}
}

// Refresh rebuilds the visible entries, re-reading git status when filtering.
// With one root this is exactly the old walk from the root. With several, each
// root is emitted as a top-level row and its subtree walked beneath it when
// open, so the roots expand and refresh independently.
func (t *Tree) Refresh() {
	if t.ChangedOnly {
		t.changed = map[string]bool{}
		for _, r := range t.Roots {
			for p := range gitChanged(r) {
				t.changed[p] = true
			}
		}
	}
	t.entries = t.entries[:0]
	if len(t.Roots) <= 1 {
		t.walk(t.Root, 0, t.expandAll[t.Root])
	} else {
		for i, root := range t.Roots {
			open := t.expanded[root]
			t.entries = append(t.entries, Entry{Path: root, Name: t.rootLabel(i), Depth: 0, Dir: true, Open: open})
			if open {
				t.walk(root, 1, t.expandAll[root])
			}
		}
	}
	t.expandAll = map[string]bool{}
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
	// The workspace's hidden policy is one config keyed by the whole root set,
	// so it is loaded once in NewTreeRoots. The path is still made relative to
	// the root the directory lives under, so a rule rooted at the top of one
	// root is matched against that root's tree.
	root := t.rootOf(dir)
	for _, it := range items {
		if t.Hidden.HiddenPath(root, filepath.Join(dir, it.Name()), it.IsDir()) {
			continue
		}
		kept = append(kept, it)
	}
	return kept
}

// walk lists dir's children. all carries an in-progress ExpandAll: when an
// ancestor of dir was the target, every directory from there down opens, and
// each is written into expanded as it is met, so the state is ordinary once
// the walk finishes.
func (t *Tree) walk(dir string, depth int, all bool) {
	for _, it := range t.children(dir) {
		name := it.Name()
		path := filepath.Join(dir, name)
		if it.IsDir() {
			if t.ChangedOnly && !t.hasChanged(path) {
				continue
			}
			open := all || t.expanded[path] || t.expandAll[path]
			if open {
				t.expanded[path] = true
			}
			t.entries = append(t.entries, Entry{path, name, depth, true, open})
			if open {
				t.walk(path, depth+1, all || t.expandAll[path])
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
	if len(t.Roots) <= 1 {
		walk(t.Root)
	} else {
		for _, root := range t.Roots {
			io.WriteString(h, root)
			h.Write([]byte{0, 'd'})
			if t.expanded[root] {
				walk(root)
			}
		}
	}
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
