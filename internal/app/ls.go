package app

import (
	"context"
	"os"
	"path/filepath"
	"sort"

	"raj/internal/control"
	"raj/internal/hidden"
	"raj/internal/search"
)

// Ls lists the immediate children of a directory. It exists because content
// search cannot discover a directory's contents: a directory holding only
// binaries, or only hidden entries, is invisible to `search`, and the tree and
// picker are interactive. The listing applies the same internal/hidden policy
// the tree, search and picker use, so all four agree about what exists; all
// drops that policy and includes hidden entries.
//
// The directory is named by the Guard's canonical path. A path that is not a
// directory is os.ReadDir's error, so ls of a file is refused rather than read
// as an empty directory.
func (h host) Ls(path string, all bool) ([]control.Entry, error) {
	if path == "" {
		// No path names the workspace itself. One root means its immediate
		// children, exactly as before; several means one top-level entry per
		// root, in supplied order, so every root is visible and none is
		// mistaken for the primary's contents.
		if h.a.visible.Len() > 1 {
			return h.rootEntries(), nil
		}
		path = h.a.visible.Primary()
	}
	dir := h.canonicalPath(path)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	// all is the -hidden switch: Everything() hides nothing, where the loaded
	// rules are the defaults plus the user's and the workspace's configuration.
	// The workspace config is one file per root set (hidden.WorkspaceFile), so
	// it is loaded from the whole set; root still names the root the directory
	// lives under, for making paths relative to it.
	root := h.a.rootFor(dir)
	rules := hidden.Load(h.a.visible.All())
	if all {
		rules = hidden.Everything()
	}
	out := make([]control.Entry, 0, len(ents))
	for _, d := range ents {
		name := d.Name()
		full := filepath.Join(dir, name)
		isDir := d.IsDir()
		if rules.HiddenPath(root, full, isDir) {
			continue
		}
		e := control.Entry{Name: name, Path: full, Dir: isDir}
		if !isDir {
			// Size is stated only for a regular file. A symlink's size is the
			// link's, not its target's, and a device or fifo has none worth
			// reporting, so all of those omit it the way a directory does.
			if info, ierr := d.Info(); ierr == nil && info.Mode().IsRegular() {
				size := info.Size()
				e.Size = &size
			}
		}
		out = append(out, e)
	}
	// os.ReadDir is already sorted by name; sorting again is cheap and states
	// the order the verb promises rather than relying on the stdlib.
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// rootEntries lists the workspace roots as directory entries, one per root in
// supplied order (the primary first). A root is named by its base name when
// that name is unique in the set, and by its absolute path when two roots share
// one, so two directories called "app" stay distinguishable rather than being
// merged or silently renamed.
func (h host) rootEntries() []control.Entry {
	roots := h.a.visible.All()
	out := make([]control.Entry, 0, len(roots))
	for i, r := range roots {
		name := filepath.Base(r)
		for j, other := range roots {
			if j != i && filepath.Base(other) == name {
				name = r
				break
			}
		}
		out = append(out, control.Entry{Name: name, Path: r, Dir: true})
	}
	return out
}

// SearchHidden is the -hidden switch's walk: it reaches .git, node_modules and
// vendor where the default refuses to descend, by running the same walk as
// Search with the hidden policy dropped. The control layer's guardedSearcher
// reaches it through the hiddenSearcher capability, so a control.Searcher with
// no snapshot to walk falls back to the ordinary walk rather than failing the
// query.
func (s snapshotSearcher) SearchHidden(ctx context.Context, q control.SearchQuery,
	emit func([]control.SearchMatch)) (int, int, bool, []control.TruncatedFile, error) {
	return s.runSearch(ctx, q, hidden.Everything(), emit)
}

// runSearch is the one walk behind Search and SearchHidden. rules is the hidden
// policy: nil leaves search.Query.Hidden unset, which the search package reads
// as the configured defaults, and hidden.Everything() drops the policy for
// -hidden. The two verbs differ only in that argument, so keeping one body is
// what stops them drifting apart.
func (s snapshotSearcher) runSearch(ctx context.Context, q control.SearchQuery,
	rules *hidden.Rules, emit func([]control.SearchMatch)) (int, int, bool, []control.TruncatedFile, error) {
	roots, err := s.walkRoots(q.Path)
	if err != nil {
		return 0, 0, false, nil, err
	}
	res := search.RunStreamRoots(ctx, roots, search.Query{
		Text: q.Text, Include: q.Include, Exclude: q.Exclude,
		Regex: q.Regex, Case: q.Case, Word: q.Word,
		Hidden: rules, Context: q.Context,
	}, s.docs, s.versions, func(batch []search.Match) {
		out := make([]control.SearchMatch, 0, len(batch))
		for _, m := range batch {
			out = append(out, control.SearchMatch{
				Path: m.Path, Line: m.Line, Col: m.Col, Len: m.Len,
				LineStart: m.LineStart, LineEnd: m.LineEnd, ByteStart: m.ByteStart, ByteEnd: m.ByteEnd,
				Version: m.Version, Text: m.Text, Context: m.Context})
		}
		emit(out)
	})
	var truncated []control.TruncatedFile
	for _, f := range res.Truncated() {
		truncated = append(truncated, control.TruncatedFile{Path: f.Path, Shown: f.Shown, Total: f.Total})
	}
	return res.Files, res.Considered, res.Capped, truncated, res.Err
}
