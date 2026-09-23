package search

import (
	"path/filepath"
	"strings"
)

// relative shortens a path for display, falling back to the full path when it
// lies outside the search root.
func relative(root, path string) string {
	if rel, err := filepath.Rel(root, path); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return path
}

// relativeAny shortens a path against the first root that contains it. With one
// root it is relative; with several it spells a hit under the second root
// relative to that root rather than falling back to the absolute path.
func relativeAny(roots []string, path string) string {
	for _, r := range roots {
		if rel, err := filepath.Rel(r, path); err == nil && !strings.HasPrefix(rel, "..") {
			return rel
		}
	}
	return path
}

// trimIndent removes leading whitespace from a result line. Search results are
// almost always indented code, and reproducing that indentation in a narrow
// sidebar wastes the columns that would show the match.
func trimIndent(s string) string { return strings.TrimLeft(s, " \t") }
