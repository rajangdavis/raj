// Package fslist holds the filesystem listing and the snapshot-backed search
// walk the editor's control surface exposes. Neither carries App state: List
// and RootEntries read the disk, and SnapshotSearcher walks a copy of the open
// buffers handed to it when the snapshot was taken, so all of it can be tested
// without an editor.
package fslist

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"raj/internal/control"
	"raj/internal/hidden"
	"raj/internal/search"
)

// List lists the immediate children of dir. It exists because content search
// cannot discover a directory's contents: a directory holding only binaries,
// or only hidden entries, is invisible to `search`, and the tree and picker
// are interactive. The listing applies the same internal/hidden policy the
// tree, search and picker use, so all four agree about what exists; all drops
// that policy and includes hidden entries.
//
// root is the workspace root dir lives under, for making an entry's path
// relative to it when the hidden policy is applied; roots is the whole root
// set, whose configuration the policy is loaded from. The caller resolves and
// canonicalises the directory first. A path that is not a directory is
// os.ReadDir's error, so ls of a file is refused rather than read as an empty
// directory.
func List(dir string, all bool, root string, roots []string) ([]control.Entry, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	// all is the -hidden switch: Everything() hides nothing, where the loaded
	// rules are the defaults plus the user's and the workspace's configuration.
	// The workspace config is one file per root set (hidden.WorkspaceFile), so
	// it is loaded from the whole set; root still names the root the directory
	// lives under, for making paths relative to it.
	rules := hidden.Load(roots)
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

// RootEntries lists the workspace roots as directory entries, one per root in
// supplied order (the primary first). A root is named by its base name when
// that name is unique in the set, and by its absolute path when two roots share
// one, so two directories called "app" stay distinguishable rather than being
// merged or silently renamed.
func RootEntries(roots []string) []control.Entry {
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

// SnapshotSearcher walks off the event thread against the copy it was handed,
// so the editor stays responsive for the seconds a search takes and a cancel
// can be serviced while it runs. It carries the whole root set, so a hit in any
// root surfaces.
type SnapshotSearcher struct {
	Roots    []string
	Docs     search.Docs
	Versions search.DocVersions
}

// Search runs the ordinary walk. nil rules leave search's configured hidden
// policy in place, which is what the default search verb asks for.
func (s SnapshotSearcher) Search(ctx context.Context, q control.SearchQuery,
	emit func([]control.SearchMatch)) (int, int, bool, []control.TruncatedFile, error) {
	// SearchHidden is the same walk with the policy dropped, so the one body
	// below is what stops the two from drifting apart.
	return s.runSearch(ctx, q, nil, emit)
}

// SearchHidden is the -hidden switch's walk: it reaches .git, node_modules and
// vendor where the default refuses to descend, by running the same walk as
// Search with the hidden policy dropped. The control layer's guardedSearcher
// reaches it through the hiddenSearcher capability, so a control.Searcher with
// no snapshot to walk falls back to the ordinary walk rather than failing the
// query.
func (s SnapshotSearcher) SearchHidden(ctx context.Context, q control.SearchQuery,
	emit func([]control.SearchMatch)) (int, int, bool, []control.TruncatedFile, error) {
	return s.runSearch(ctx, q, hidden.Everything(), emit)
}

// runSearch is the one walk behind Search and SearchHidden. rules is the hidden
// policy: nil leaves search.Query.Hidden unset, which the search package reads
// as the configured defaults, and hidden.Everything() drops the policy for
// -hidden. The two verbs differ only in that argument, so keeping one body is
// what stops them drifting apart.
func (s SnapshotSearcher) runSearch(ctx context.Context, q control.SearchQuery,
	rules *hidden.Rules, emit func([]control.SearchMatch)) (int, int, bool, []control.TruncatedFile, error) {
	roots, err := s.walkRoots(q.Path)
	if err != nil {
		return 0, 0, false, nil, err
	}
	res := search.RunStreamRoots(ctx, roots, search.Query{
		Text: q.Text, Include: q.Include, Exclude: q.Exclude,
		Regex: q.Regex, Case: q.Case, Word: q.Word,
		Hidden: rules, Context: q.Context,
	}, s.Docs, s.Versions, func(batch []search.Match) {
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

// walkRoots resolves a query's -path against the workspace root set. With no
// path every root is walked, in supplied order. A relative path is joined to
// the primary root; an absolute one is taken as given. Either way it must stay
// inside some root, the same rule the Guard applies to exec's -dir: a walk is
// refused rather than allowed to read outside the tree. The Guard validates the
// query before it reaches here, so this check is the backstop for a Searcher
// driven directly.
func (s SnapshotSearcher) walkRoots(path string) ([]string, error) {
	if path == "" {
		return s.Roots, nil
	}
	primary := ""
	if len(s.Roots) > 0 {
		primary = s.Roots[0]
	}
	dir := path
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(primary, dir)
	}
	dir = filepath.Clean(dir)
	for _, root := range s.Roots {
		rel, err := filepath.Rel(filepath.Clean(root), dir)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		return []string{dir}, nil
	}
	return nil, fmt.Errorf("search path %q is outside the workspace", path)
}
