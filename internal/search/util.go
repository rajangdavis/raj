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

// trimIndent removes leading whitespace from a result line. Search results are
// almost always indented code, and reproducing that indentation in a narrow
// sidebar wastes the columns that would show the match.
func trimIndent(s string) string { return strings.TrimLeft(s, " \t") }
