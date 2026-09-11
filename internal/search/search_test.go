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

// A grep user means ^ and $ to anchor to a line, not to the whole file. The
// sweep in scanData matches each file as one text, so without per-line mode an
// anchored pattern matched only the first and last byte of the file.
func TestRunRegexAnchorsPerLine(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "x.md"),
		[]byte("intro\n## one\ntext\n## two\n## three\n"), 0o644)

	res := Run(dir, Query{Text: "^## ", Regex: true, Case: true})
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	if len(res.Matches) != 3 {
		t.Fatalf("^ matched %d lines, want 3: %+v", len(res.Matches), res.Matches)
	}
	for i, want := range []int{2, 4, 5} {
		if got := res.Matches[i].Line; got != want {
			t.Errorf("match %d on line %d, want %d", i, got, want)
		}
	}
}

// $ is per-line too: the same whole-file sweep would otherwise find only the
// last line.
func TestRunRegexEndAnchorPerLine(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "x.txt"),
		[]byte("one here\ntwo\nthe last here\n"), 0o644)

	res := Run(dir, Query{Text: "here$", Regex: true, Case: true})
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	if len(res.Matches) != 2 || res.Matches[0].Line != 1 || res.Matches[1].Line != 3 {
		t.Fatalf("here$ matched %+v, want lines 1 and 3", res.Matches)
	}
}

// The literal fast path must keep an anchor literal: a search for the caret
// character finds carets, it does not become an anchor. Force the regexp path
// too, because that is where quoting and per-line mode meet.
func TestRunLiteralCaretStaysLiteral(t *testing.T) {
	defer func() { forceRegexp = false }()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "x.txt"), []byte("a^b\n## heading\n"), 0o644)

	for _, force := range []bool{false, true} {
		forceRegexp = force
		res := Run(dir, Query{Text: "^"})
		if res.Err != nil {
			t.Fatalf("forceRegexp=%v: %v", force, res.Err)
		}
		if len(res.Matches) != 1 || res.Matches[0].Line != 1 || res.Matches[0].Col != 1 {
			t.Errorf("forceRegexp=%v: caret search = %+v, want one hit on line 1 col 1",
				force, res.Matches)
		}
	}
}

// Per-line mode must not make a dot cross a newline. In Go a dot never matches
// a newline without (?s), and the anchor fix deliberately leaves that alone, so
// a later helpful (?s) would be a visible change rather than a silent one.
func TestRunRegexDotDoesNotCrossLines(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "x.txt"), []byte("a\nb\n"), 0o644)

	res := Run(dir, Query{Text: "a.b", Regex: true, Case: true})
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	if len(res.Matches) != 0 {
		t.Errorf("a.b matched across a newline: %+v", res.Matches)
	}
}

// TestRunIncludeBasenameGlobMatchesNested covers the grep --include meaning of
// a pattern with no separator: it is a filename glob, so *_test.go must reach
// a test file in a subdirectory even though filepath.Match's * does not cross
// a separator.
func TestRunIncludeBasenameGlobMatchesNested(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("top_test.go", "needle top test\n")
	write("sub/nested_test.go", "needle nested test\n")
	write("sub/impl.go", "needle impl\n")

	res := Run(dir, Query{Text: "needle", Include: "*_test.go"})
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	got := map[string]bool{}
	for _, m := range res.Matches {
		got[filepath.Base(m.Path)] = true
	}
	if len(got) != 2 || !got["top_test.go"] || !got["nested_test.go"] {
		t.Errorf("include *_test.go matched %v, want both test files", got)
	}
}

// TestRunIncludeExtensionGlobTreeWide pins the fast path: *.go keeps selecting
// Go files at every depth, not just at the root.
func TestRunIncludeExtensionGlobTreeWide(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.go", "sub/b.go", "sub/deep/c.go", "sub/d.md"} {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("needle\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	res := Run(dir, Query{Text: "needle", Include: "*.go"})
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	if len(res.Matches) != 3 {
		t.Fatalf("include *.go matched %d files, want 3: %+v", len(res.Matches), res.Matches)
	}
}

// TestRunIncludeBareBasenameMatches pins that a bare filename is a valid scope
// and reaches a file below the root, not only one sitting at it.
func TestRunIncludeBareBasenameMatches(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"search.go", "sub/search.go", "sub/other.go"} {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("needle\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	res := Run(dir, Query{Text: "needle", Include: "search.go"})
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	if len(res.Matches) != 2 {
		t.Fatalf("include search.go matched %d files, want 2: %+v", len(res.Matches), res.Matches)
	}
}

// TestRunIncludePathGlobStaysPathScoped pins that a pattern containing a
// separator is still matched against the relative path alone, so a glob does
// not leak into a same-named file elsewhere in the tree.
func TestRunIncludePathGlobStaysPathScoped(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"sub/a.go", "other/sub/a.go"} {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("needle\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	res := Run(dir, Query{Text: "needle", Include: "sub/*.go"})
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	if len(res.Matches) != 1 || !strings.HasSuffix(res.Matches[0].Path, filepath.FromSlash("sub/a.go")) {
		t.Fatalf("include sub/*.go matched %+v, want only sub/a.go", res.Matches)
	}
}

// TestMatchesBasenameAndPathSemantics is the matcher itself, apart from the
// walk: a no-slash pattern tries the basename, a path-scoped one does not.
func TestMatchesBasenameAndPathSemantics(t *testing.T) {
	cases := []struct {
		path string
		pat  string
		want bool
	}{
		{"internal/search/search.go", "*_test.go", false},
		{"internal/search/search_test.go", "*_test.go", true},
		{"internal/search/search.go", "search.go", true},
		{"internal/search/search.go", "*.go", true},
		{"internal/search/search.go", "internal/search/*.go", true},
		{"internal/search/search.go", "other/*.go", false},
		{"internal/search/search.go", "internal/*/*.go", true},
	}
	for _, c := range cases {
		if got := matches(filepath.FromSlash(c.path), []string{c.pat}, false); got != c.want {
			t.Errorf("matches(%q, %q) = %v, want %v", c.path, c.pat, got, c.want)
		}
	}
}

// TestRunIncludeNoMatch is the no-hit case the CLI turns into its "matched no
// files" warning: the matcher reports an empty result and the walk opens
// nothing.
func TestRunIncludeNoMatch(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("needle\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, include := range []string{"*.rs", "*_test.go", "sub/*.go"} {
		res := Run(dir, Query{Text: "needle", Include: include})
		if res.Err != nil {
			t.Fatalf("include %q: %v", include, res.Err)
		}
		if len(res.Matches) != 0 {
			t.Errorf("include %q matched %v, want none", include, res.Matches)
		}
	}
}
