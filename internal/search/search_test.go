package search

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{
		"a.go":              "package a\nfunc Needle() {}\n",
		"b.md":              "needle in markdown\n",
		"sub/c.go":          "package sub\n// needle comment\n",
		".git/d.go":         "needle hidden\n",
		"node_modules/e.go": "needle vendored\n",
		".gitlab-ci.yml":    "needle in CI config\n",
		"bin.dat":           "needle\x00binary\n",
	} {
		p := filepath.Join(dir, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(body), 0o644)
	}
	return dir
}

func TestRunLiteralIsCaseInsensitive(t *testing.T) {
	res := Run(fixture(t), Query{Text: "needle"})
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	if len(res.Matches) < 3 {
		t.Fatalf("got %d matches, want at least 3", len(res.Matches))
	}
	for _, m := range res.Matches {
		if strings.Contains(m.Path, "node_modules") || strings.Contains(m.Path, ".git/") {
			t.Errorf("should have skipped %s", m.Path)
		}
	}
	// A dotfile is not automatically hidden: .gitlab-ci.yml, .github/ and the
	// rest are files people edit, and a search that cannot see them is the bug
	// this replaced.
	var sawDotfile bool
	for _, m := range res.Matches {
		if strings.HasSuffix(m.Path, ".gitlab-ci.yml") {
			sawDotfile = true
		}
	}
	if !sawDotfile {
		t.Error("dotfile .gitlab-ci.yml was not searched")
	}
}

// Binary files are skipped at the first NUL, so a match earlier on the line
// still must not be reported once the file is known to be binary.
func TestRunSkipsBinary(t *testing.T) {
	for _, m := range Run(fixture(t), Query{Text: "needle"}).Matches {
		if strings.HasSuffix(m.Path, "bin.dat") {
			t.Error("binary file matched")
		}
	}
}

func TestRunCaseSensitive(t *testing.T) {
	res := Run(fixture(t), Query{Text: "Needle", Case: true})
	if len(res.Matches) != 1 {
		t.Fatalf("got %d matches, want 1", len(res.Matches))
	}
	if !strings.HasSuffix(res.Matches[0].Path, "a.go") {
		t.Errorf("matched %s", res.Matches[0].Path)
	}
}

// Literal mode must not interpret regex metacharacters.
func TestRunLiteralQuotesMetacharacters(t *testing.T) {
	if res := Run(fixture(t), Query{Text: "a(b"}); res.Err != nil {
		t.Errorf("literal search reported %v", res.Err)
	}
	if res := Run(fixture(t), Query{Text: "a(b", Regex: true}); res.Err == nil {
		t.Error("regex search should reject an unclosed group")
	}
}

func TestRunGlobs(t *testing.T) {
	dir := fixture(t)
	only := Run(dir, Query{Text: "needle", Include: "*.md"})
	if len(only.Matches) != 1 || !strings.HasSuffix(only.Matches[0].Path, "b.md") {
		t.Errorf("include *.md gave %v", only.Matches)
	}
	without := Run(dir, Query{Text: "needle", Exclude: "*.md"})
	for _, m := range without.Matches {
		if strings.HasSuffix(m.Path, ".md") {
			t.Errorf("exclude *.md let %s through", m.Path)
		}
	}
}

// Path-scoped globs match relative to the search root, not just the filename.
func TestRunGlobsMatchPath(t *testing.T) {
	dir := fixture(t)
	got := Run(dir, Query{Text: "needle", Include: "sub/*.go"})
	if len(got.Matches) != 1 || !strings.HasSuffix(got.Matches[0].Path, "sub/c.go") {
		t.Errorf("include sub/*.go gave %v", got.Matches)
	}
	exc := Run(dir, Query{Text: "needle", Exclude: "sub/*.go"})
	for _, m := range exc.Matches {
		if strings.HasSuffix(m.Path, "sub/c.go") {
			t.Errorf("exclude sub/*.go let %s through", m.Path)
		}
	}
	// A bare extension glob still reaches files in subdirectories because it
	// is answered by the extension, not by the glob matcher.
	allGo := Run(dir, Query{Text: "needle", Include: "*.go"})
	want := map[string]bool{"a.go": true, "sub/c.go": true}
	gotNames := map[string]bool{}
	for _, m := range allGo.Matches {
		for base := range want {
			if strings.HasSuffix(m.Path, base) {
				gotNames[base] = true
			}
		}
	}
	if len(gotNames) != len(want) {
		t.Errorf("*.go matched %v, want both %v", gotNames, want)
	}
}

func TestRunWholeWord(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "x.txt"), []byte("needles\nneedle\n"), 0o644)
	res := Run(dir, Query{Text: "needle", Word: true})
	if len(res.Matches) != 1 || res.Matches[0].Line != 2 {
		t.Errorf("whole-word gave %v", res.Matches)
	}
}

func TestRunReportsMatchColumn(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "x.txt"), []byte("aa needle bb\n"), 0o644)
	res := Run(dir, Query{Text: "needle"})
	if len(res.Matches) != 1 {
		t.Fatalf("got %d matches", len(res.Matches))
	}
	if m := res.Matches[0]; m.Col != 3 || m.Len != 6 {
		t.Errorf("col=%d len=%d, want 3 and 6", m.Col, m.Len)
	}
}

// Byte offsets count from the start of the file, not the start of the line.
func TestRunReportsMatchByteOffsets(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "x.txt"), []byte("line one\nneedle here\nline three\n"), 0o644)
	res := Run(dir, Query{Text: "needle"})
	if len(res.Matches) != 1 {
		t.Fatalf("got %d matches", len(res.Matches))
	}
	m := res.Matches[0]
	if m.Line != 2 {
		t.Errorf("line=%d, want 2", m.Line)
	}
	if m.Col != 0 || m.Len != 6 {
		t.Errorf("col=%d len=%d, want 0 and 6", m.Col, m.Len)
	}
	// "line one\n" is 9 bytes; line 2 starts at byte 9.
	if m.ByteStart != 9 || m.ByteEnd != 15 {
		t.Errorf("byte_start=%d byte_end=%d, want 9 and 15", m.ByteStart, m.ByteEnd)
	}
}

func TestRunEmptyQuery(t *testing.T) {
	if res := Run(fixture(t), Query{}); len(res.Matches) != 0 {
		t.Error("an empty query should match nothing")
	}
}
