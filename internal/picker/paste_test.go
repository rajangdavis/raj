package picker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"raj/internal/keys"
)

// tree builds a workspace and a picker over it.
func tree(t *testing.T, files ...string) *Picker {
	t.Helper()
	root := t.TempDir()
	for _, f := range files {
		full := filepath.Join(root, f)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	p := New(root)
	p.Show()
	return p
}

// A pasted path is a hint, not a literal query. Each of these forms names a
// file in the tree, and each used to match nothing: the fuzzy score is a
// subsequence test, so one byte the path does not contain is enough to empty
// the list while the field still shows what was pasted.
func TestPasteNarrowsUntilItMatches(t *testing.T) {
	p := tree(t, "internal/app/app.go", "cmd/raj/main.go")
	root := p.Root

	cases := []struct {
		name  string
		paste string
	}{
		{"relative", "internal/app/app.go"},
		{"dot slash", "./internal/app/app.go"},
		{"absolute", filepath.Join(root, "internal/app/app.go")},
		{"grep line", "internal/app/app.go:464"},
		{"compiler line and column", "internal/app/app.go:464:12"},
		{"absolute with position", filepath.Join(root, "internal/app/app.go") + ":464:12"},
		{"outside the workspace", "/elsewhere/checkout/internal/app/app.go"},
		{"surrounding whitespace", "  internal/app/app.go\t"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p.Show()
			p.Paste(c.paste)
			if p.Results() == 0 {
				t.Fatalf("pasting %q matched nothing; query is %q", c.paste, p.Query())
			}
			if got := p.Top(); got != filepath.FromSlash("internal/app/app.go") {
				t.Errorf("pasting %q ranked %q first", c.paste, got)
			}
		})
	}
}

// Narrowing stops at the first form that matches, so a paste that is already a
// good query is left alone rather than reduced to its base name.
func TestPasteKeepsTheMostSpecificMatchingForm(t *testing.T) {
	p := tree(t, "internal/app/app.go", "cmd/app/app.go")
	p.Paste("internal/app/app.go")
	if got := p.Query(); got != "internal/app/app.go" {
		t.Errorf("query = %q, want the pasted path unchanged", got)
	}
	if got := p.Top(); got != filepath.FromSlash("internal/app/app.go") {
		t.Errorf("top result = %q, want the pasted path", got)
	}
}

// When nothing matches, the field holds what was pasted, unchanged.
func TestPasteRestoresTheOriginalWhenNothingMatches(t *testing.T) {
	p := tree(t, "internal/app/app.go")
	p.Paste("/other/place/nothing_like_it.rb:12")
	if p.Results() != 0 {
		t.Fatalf("expected no matches, got %d", p.Results())
	}
	if got := p.Query(); got != "/other/place/nothing_like_it.rb:12" {
		t.Errorf("query = %q, want the payload as pasted", got)
	}
}

// A colon is legal in a file name, so only an all-digit tail is a position.
// The numbers are kept, not just cut off: a single one is a line, because that
// is what a tool prints when it has only one to give.
func TestSplitPosition(t *testing.T) {
	cases := []struct {
		in   string
		path string
		line int
		col  int
	}{
		{"app.go", "app.go", 0, 0},
		{"app.go:12", "app.go", 12, 0},
		{"app.go:12:4", "app.go", 12, 4},
		{"app.go:12:4:9", "app.go:12", 4, 9}, // two segments is the most a position has
		{"weird:name.go", "weird:name.go", 0, 0},
		{"weird:name.go:3", "weird:name.go", 3, 0},
		{"app.go:", "app.go:", 0, 0},
		{":12", ":12", 0, 0}, // nothing before the colon to keep
		{"/abs/path/app.go:8", "/abs/path/app.go", 8, 0},
	}
	for _, c := range cases {
		path, line, col := splitPosition(c.in)
		if path != c.path || line != c.line || col != c.col {
			t.Errorf("splitPosition(%q) = %q, %d, %d; want %q, %d, %d",
				c.in, path, line, col, c.path, c.line, c.col)
		}
	}
}

// The position rides along with the paste so a compiler line opens where the
// compiler was pointing.
func TestPasteKeepsThePosition(t *testing.T) {
	p := tree(t, "internal/app/app.go")
	p.Paste(filepath.Join(p.Root, "internal/app/app.go") + ":464:12")

	pos, ok := p.PositionFor(filepath.FromSlash("internal/app/app.go"))
	if !ok {
		t.Fatal("the pasted position was dropped")
	}
	if pos.Line != 464 || pos.Col != 12 {
		t.Errorf("position = %d:%d, want 464:12", pos.Line, pos.Col)
	}
}

// The app asks with what Confirm handed it, which is resolved and absolute.
// PositionFor has to answer for that form as well as for the indexed one, or
// the position is dropped at exactly the seam it exists to cross.
func TestPositionForAcceptsAResolvedPath(t *testing.T) {
	p := tree(t, "internal/app/app.go")
	p.Paste("internal/app/app.go:464:12")

	pos, ok := p.PositionFor(p.Resolve(filepath.FromSlash("internal/app/app.go")))
	if !ok {
		t.Fatal("an absolute chosen path lost its position")
	}
	if pos.Line != 464 || pos.Col != 12 {
		t.Errorf("position = %d:%d, want 464:12", pos.Line, pos.Col)
	}
}

// Narrowing to a base name widens the query, so the position must not follow
// whichever same-named file is chosen instead.
func TestPositionDoesNotFollowADifferentFile(t *testing.T) {
	p := tree(t, "internal/app/app.go", "cmd/app/app.go")
	p.Paste("/other/checkout/internal/app/app.go:464")
	// The field keeps what arrived; only the matching is narrowed. Rewriting
	// the field under the user would be intolerable while typing, and the same
	// code path now serves both.
	if p.Results() == 0 {
		t.Fatalf("nothing matched for %q", p.Query())
	}
	if _, ok := p.PositionFor(filepath.FromSlash("internal/app/app.go")); !ok {
		t.Error("the named file lost its position")
	}
	if _, ok := p.PositionFor(filepath.FromSlash("cmd/app/app.go")); !ok {
		t.Error("a base-name paste should apply to any file with that name")
	}
	if _, ok := p.PositionFor(filepath.FromSlash("internal/app/other.go")); ok {
		t.Error("the position followed a file the paste did not name")
	}
}

// Editing the query detaches the position: what is being looked for is no
// longer what was pasted.
func TestTypingClearsThePastedPosition(t *testing.T) {
	p := tree(t, "internal/app/app.go", "internal/app/apple.go")
	p.Paste("internal/app/app.go:464")
	if _, ok := p.PositionFor(filepath.FromSlash("internal/app/app.go")); !ok {
		t.Fatal("setup: no position after the paste")
	}
	p.Handle(keys.None, "le")
	if _, ok := p.PositionFor(filepath.FromSlash("internal/app/apple.go")); ok {
		t.Error("typing left the pasted position attached")
	}
}

// A paste that matches nothing carries no position either — there is no file
// for it to belong to.
func TestUnmatchedPasteCarriesNoPosition(t *testing.T) {
	p := tree(t, "internal/app/app.go")
	p.Paste("/other/place/nothing_like_it.rb:12")
	if _, ok := p.PositionFor("internal/app/app.go"); ok {
		t.Error("an unmatched paste kept a position")
	}
}

// An empty or whitespace-only paste must not clear a query the user typed.
func TestPasteIgnoresEmptyPayloads(t *testing.T) {
	p := tree(t, "internal/app/app.go")
	p.Paste("app")
	before := p.Query()
	p.Paste("   \t ")
	if got := p.Query(); got != before {
		t.Errorf("query = %q, want it left as %q", got, before)
	}
}

// The field shows what arrived, in full. Narrowing is a matching strategy, not
// an edit: rewriting the query under the user hid the position they pasted and
// made it look as though `:464:12` had been thrown away.
func TestTheQueryIsNeverRewritten(t *testing.T) {
	p := tree(t, "internal/app/app.go")
	for _, q := range []string{
		"app.go:464:12",
		"internal/app/app.go:464",
		filepath.Join(p.Root, "internal/app/app.go") + ":464:12",
		"./internal/app/app.go",
	} {
		p.Show()
		p.Paste(q)
		if got := p.Query(); got != q {
			t.Errorf("pasted %q, field shows %q", q, got)
		}
		if p.Results() == 0 {
			t.Errorf("pasted %q and nothing matched", q)
		}
	}
}

// Typing a path with a position matches the same way pasting one does, because
// the editor cannot tell how the bytes arrived and should not need to.
func TestTypedPathWithAPositionMatches(t *testing.T) {
	p := tree(t, "internal/app/app.go", "internal/app/render.go")
	p.Show()
	for _, ch := range strings.Split("app.go:464:12", "") {
		p.Handle(keys.None, ch)
	}
	if p.Results() == 0 {
		t.Fatalf("typing %q matched nothing", p.Query())
	}
	if got := p.Top(); got != filepath.FromSlash("internal/app/app.go") {
		t.Errorf("top = %q, want internal/app/app.go", got)
	}
	pos, ok := p.PositionFor(p.Top())
	if !ok || pos.Line != 464 || pos.Col != 12 {
		t.Errorf("position = %+v (%v), want 464:12", pos, ok)
	}
}

// A query that matches on its own is never narrowed, so a file whose name
// really does contain a colon stays reachable.
func TestALiteralMatchIsNeverNarrowed(t *testing.T) {
	p := tree(t, "weird/name:2.go", "weird/name.go")
	p.Show()
	p.Paste("name:2.go")
	if got := p.Top(); got != filepath.FromSlash("weird/name:2.go") {
		t.Errorf("top = %q, want the file that literally matches", got)
	}
	if _, ok := p.PositionFor(p.Top()); ok {
		t.Error("a literal match should carry no position")
	}
}
