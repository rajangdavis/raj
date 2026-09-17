// Package docsindex guards the documentation index in docs/README.md against
// drift: a doc on disk with no row, a row naming a file that is not there, or a
// retired row whose file still exists.
//
// This is a test-only package. It has no production code, only the guard. It
// is separate from internal/keys, whose staleness test guards a generated file
// against its own table; this guard is about the docs directory and its index,
// which is not the key table concern. The repo-root resolution (../../docs) is
// the same one internal/keys/doc_test.go uses for docs/KEYBINDINGS.md.
package docsindex

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// docsDir and readmeName are the documentation directory and its index,
// relative to this package.
const (
	docsDir    = "../../docs"
	readmeName = "README.md"
)

// row is one index table row: the file it names and the cells that can mark it
// retired. Only lines beginning with "| [" count, so the header, the separator
// and prose mentions of a path are all excluded.
type row struct {
	line    int    // 1-based line in README.md, for the failure message
	target  string // the file named in the link, e.g. "TODO.md"
	kind    string // the kind cell, e.g. "retired record"
	purpose string // the purpose cell, which can say "delete pending"
}

// readIndex returns every doc-index row in docs/README.md.
func readIndex(t *testing.T) []row {
	t.Helper()
	path := filepath.Join(docsDir, readmeName)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s is missing: %v", path, err)
	}
	var rows []row
	for i, line := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(line, "| [") {
			continue
		}
		target, ok := linkTarget(line)
		if !ok {
			t.Errorf("%s:%d: table row has no [name](target) link: %s", readmeName, i+1, line)
			continue
		}
		// cells[0] is the empty run before the leading bar and cells[1] is
		// the doc link; the kind and purpose columns follow.
		cells := strings.Split(line, "|")
		r := row{line: i + 1, target: target}
		if len(cells) > 2 {
			r.kind = strings.TrimSpace(cells[2])
		}
		if len(cells) > 3 {
			r.purpose = strings.TrimSpace(cells[3])
		}
		rows = append(rows, r)
	}
	return rows
}

// linkTarget pulls the path out of the first [name](target) in a row.
func linkTarget(line string) (string, bool) {
	_, rest, ok := strings.Cut(line, "](")
	if !ok {
		return "", false
	}
	target, _, ok := strings.Cut(rest, ")")
	if !ok {
		return "", false
	}
	return target, true
}

// retired reports whether a row is marked as a folded source whose file is
// meant to be gone: the kind says "retired", or the purpose says
// "delete pending".
func (r row) retired() bool {
	return strings.Contains(strings.ToLower(r.kind), "retired") ||
		strings.Contains(strings.ToLower(r.purpose), "delete pending")
}

// Every doc on disk has an index row. A document nobody can find from the
// index is one the placement rule has silently dropped.
func TestEveryDocHasARow(t *testing.T) {
	named := map[string]bool{}
	for _, r := range readIndex(t) {
		named[filepath.Base(r.target)] = true
	}
	entries, err := os.ReadDir(docsDir)
	if err != nil {
		t.Fatalf("reading %s: %v", docsDir, err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		if !named[e.Name()] {
			t.Errorf("docs/%s has no row in %s.\nadd a row to the index, or delete the file",
				e.Name(), readmeName)
		}
	}
}

// Every row names a file that exists. A row pointing at a deleted doc is a
// link the reader follows into nothing.
func TestEveryRowNamesAFileThatExists(t *testing.T) {
	for _, r := range readIndex(t) {
		path := filepath.Join(docsDir, filepath.Base(r.target))
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s:%d: row names docs/%s, which is not on disk: %v.\ndelete the row, or restore the file",
				readmeName, r.line, r.target, err)
		}
	}
}

// retiredRowsWithFiles returns the retired rows whose named file is still on
// disk. Naming the predicate lets a fixture exercise it while the on-disk
// index has no retired row to check.
func retiredRowsWithFiles(rows []row) []row {
	var out []row
	for _, r := range rows {
		if !r.retired() {
			continue
		}
		if _, err := os.Stat(filepath.Join(docsDir, filepath.Base(r.target))); err == nil {
			out = append(out, r)
		}
	}
	return out
}

// A retired row and its file go together: once the content is folded, the file
// is gone. A retired row whose file is still on disk is the duplication the
// fold was meant to end.
//
// The current index has no retired row -- the three folded sources were both
// unlisted and deleted -- so the loop below is dormant on the tree itself.
// TestRetiredRowPredicate pins the predicate so the guard is exercised rather
// than merely present.
func TestRetiredRowsHaveNoFile(t *testing.T) {
	for _, r := range retiredRowsWithFiles(readIndex(t)) {
		t.Errorf("%s:%d: row is retired but docs/%s is still on disk.\ndelete the file, then the row",
			readmeName, r.line, r.target)
	}
}

// A retired row naming a file that is still on disk is found, and both words
// that mark a row retired are recognised: a "retired" kind and a "delete
// pending" purpose. The last row is retired but names no file, so it is not a
// violation -- the file is already gone.
func TestRetiredRowPredicate(t *testing.T) {
	rows := []row{
		{line: 1, target: "README.md", kind: "retired record", purpose: "-"},
		{line: 2, target: "README.md", kind: "record", purpose: "delete pending"},
		{line: 3, target: "README.md", kind: "record", purpose: "live"},
		{line: 4, target: "no-such-doc.md", kind: "retired record", purpose: "-"},
	}
	got := retiredRowsWithFiles(rows)
	if len(got) != 2 || got[0].line != 1 || got[1].line != 2 {
		t.Fatalf("retiredRowsWithFiles = %+v, want rows 1 and 2", got)
	}
}
