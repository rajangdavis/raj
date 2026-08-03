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
		".hidden/d.go":      "needle hidden\n",
		"node_modules/e.go": "needle vendored\n",
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
		if strings.Contains(m.Path, "node_modules") || strings.Contains(m.Path, ".hidden") {
			t.Errorf("should have skipped %s", m.Path)
		}
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

func TestRunEmptyQuery(t *testing.T) {
	if res := Run(fixture(t), Query{}); len(res.Matches) != 0 {
		t.Error("an empty query should match nothing")
	}
}
