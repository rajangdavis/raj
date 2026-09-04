package editor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectIndent(t *testing.T) {
	cases := []struct {
		name  string
		text  string
		want  Indent
		wantD bool
	}{
		{"empty", "", Indent{}, false},
		{"no indentation", "package a\n\nfunc f() {}\n", Indent{}, false},
		{"blank lines are not indentation", "a\n\n   \n\t\nb\n", Indent{}, false},

		{"go, tabs", "package a\nfunc f() {\n\treturn\n}\n", Indent{Tabs: true}, true},
		{"tabs, nested", "a\n\tb\n\t\tc\n\tb\n", Indent{Tabs: true}, true},

		{"two spaces", "a\n  b\n  c\n    d\n", Indent{Width: 2}, true},
		{"four spaces", "a\n    b\n    c\n        d\n", Indent{Width: 4}, true},
		{"three spaces", "a\n   b\n      c\n   b\n", Indent{Width: 3}, true},

		// One stray tab-indented line in a space file must not redefine the
		// file, which is the failure mode of deciding on presence.
		{"mostly spaces, one tab", "a\n  b\n  c\n  d\n\tstray\n", Indent{Width: 2}, true},
		// ...and the same the other way.
		{"mostly tabs, one space line", "a\n\tb\n\tc\n\td\n  stray\n", Indent{Tabs: true}, true},
		// A tie goes to tabs: a file with equal evidence is one raj should not
		// be adding spaces to.
		{"tie", "a\n\tb\n  c\n", Indent{Tabs: true}, true},

		// Continuation alignment is not an indentation level. Deciding on the
		// smallest indent would call this file 2-wide because of the 22.
		{"aligned continuation", strings.Join([]string{
			"func f(",
			"    a int,",
			"    b int,",
			") {",
			"    call(one,",
			"         two)",
			"}",
		}, "\n"), Indent{Width: 4}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := DetectIndent(c.text)
			if ok != c.wantD {
				t.Fatalf("detected = %v, want %v", ok, c.wantD)
			}
			if ok && (got.Tabs != c.want.Tabs || got.Width != c.want.Width) {
				t.Errorf("got %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestUnit(t *testing.T) {
	if u := (Indent{Tabs: true, Width: 8}).Unit(); u != "\t" {
		t.Errorf("tabs unit = %q, want one tab", u)
	}
	if u := (Indent{Width: 3}).Unit(); u != "   " {
		t.Errorf("spaces unit = %q", u)
	}
	if u := (Indent{}).Unit(); len(u) != DefaultIndentWidth {
		t.Errorf("zero width did not fall back: %q", u)
	}
}

// The reported bug: a tab-indented file grew spaces the moment it was edited.
func TestIndentingATabFileInsertsTabs(t *testing.T) {
	p := NewPane(NewFile("t.go", "package a\nfunc f() {\n\treturn\n}\n", 4))
	if !p.File.Indent.Tabs {
		t.Fatalf("style = %v, want tabs", p.File.Indent)
	}
	p.Cursors.Set(0, 0)
	p.Indent()
	if got := p.File.Line(0); got != "\tpackage a" {
		t.Errorf("line = %q, want a leading tab", got)
	}
}

// And the bytes reach disk unchanged, which is the half of it a user actually
// checks. Save writes verbatim, so this is a regression guard on that too.
func TestTabsSurviveASaveRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.go")
	if err := os.WriteFile(path, []byte("func f() {\n\treturn\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := Open(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	p := NewPane(f)
	p.Cursors.Set(0, 0)
	p.Indent()
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), "\tfunc") {
		t.Errorf("saved file starts %q, want a tab", firstLine(string(data)))
	}
	if strings.Contains(string(data), "    ") {
		t.Errorf("spaces reached disk: %q", string(data))
	}
}

// A space-indented file must not gain tabs, which is the symmetric mistake.
func TestIndentingASpaceFileInsertsSpaces(t *testing.T) {
	p := NewPane(NewFile("t.js", "function f() {\n  return\n}\n", 4))
	if p.File.Indent.Tabs || p.File.Indent.Width != 2 {
		t.Fatalf("style = %+v, want two spaces", p.File.Indent)
	}
	p.Cursors.Set(0, 0)
	p.Indent()
	if got := p.File.Line(0); got != "  function f() {" {
		t.Errorf("line = %q", got)
	}
}

// Outdent already understood a literal tab; this pins it, since a Tab that
// inserts one and a shift+Tab that cannot remove it is worse than neither.
func TestOutdentRemovesATab(t *testing.T) {
	p := NewPane(NewFile("t.go", "func f() {\n\t\treturn\n}\n", 4))
	p.Cursors.Set(p.File.LineStart(1), p.File.LineStart(1))
	p.Outdent()
	if got := p.File.Line(1); got != "\treturn" {
		t.Errorf("line = %q, want one tab left", got)
	}
}

// The default applies only where the file had nothing to say. A flag that
// overrode a detected style would make the first keystroke mix the file.
func TestSetIndentDefaultYieldsToTheFile(t *testing.T) {
	detected := NewFile("t.go", "func f() {\n\treturn\n}\n", 4)
	detected.SetIndentDefault(Indent{Width: 2})
	if !detected.Indent.Tabs {
		t.Error("a default overrode a detected style")
	}

	// .txt on purpose: a .go file with nothing to detect now falls to the Go
	// convention, which is stronger than a default.
	blank := NewFile("t.txt", "hello\n", 4)
	if blank.IndentDetected() {
		t.Fatal("nothing to detect was reported as detected")
	}
	blank.SetIndentDefault(Indent{Tabs: true})
	if !blank.Indent.Tabs {
		t.Error("default not applied to a file with no indentation")
	}
	if blank.Indent.Width != 4 {
		t.Errorf("width = %d, want the configured 4 carried through", blank.Indent.Width)
	}
}

// A detected tab style still takes the configured display width, since a tab
// has to be drawn as something and that flag is the only preference about it.
func TestTabFileKeepsConfiguredDisplayWidth(t *testing.T) {
	f := NewFile("t.go", "func f() {\n\treturn\n}\n", 8)
	if f.Indent.Width != 8 || f.Cols.Tab != 8 {
		t.Errorf("width = %d, cols = %d, want 8", f.Indent.Width, f.Cols.Tab)
	}
}

// Detection must not read a whole large file to answer a whitespace question.
func TestDetectIsBounded(t *testing.T) {
	var b strings.Builder
	for i := 0; i < maxDetectLines*2; i++ {
		b.WriteString("\tx\n")
	}
	b.WriteString(strings.Repeat("        y\n", 100)) // past the bound, ignored
	if got, _ := DetectIndent(b.String()); !got.Tabs {
		t.Errorf("got %+v, want tabs; the scan read past its bound", got)
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// Indenting with a detected style must reproduce that style: text built by
// indenting with a unit, detected and re-indented, must not mix whitespace.
// This is the property the bug violated, stated so a fuzzer can look for it.
func FuzzIndentIsIdempotent(f *testing.F) {
	f.Add("a\nb\nc\n", 0, 4)
	f.Add("x\ny\n", 1, 2)
	f.Fuzz(func(t *testing.T, body string, style, width int) {
		if width < 1 || width > 8 || len(body) > 4096 {
			t.Skip()
		}
		unit := strings.Repeat(" ", width)
		if style%2 == 1 {
			unit = "\t"
		}
		// Build a document indented with unit, one level per line.
		var b strings.Builder
		lines := strings.Split(body, "\n")
		if len(lines) > 64 {
			lines = lines[:64]
		}
		for _, ln := range lines {
			ln = strings.TrimLeft(ln, " \t")
			if strings.TrimSpace(ln) == "" {
				continue
			}
			b.WriteString(unit + ln + "\n")
		}
		text := b.String()
		if text == "" {
			t.Skip()
		}
		got, ok := DetectIndent(text)
		if !ok {
			t.Fatalf("no style detected from %q", text)
		}
		if got.Unit() != unit {
			t.Fatalf("built with %q, detected %q (%+v)", unit, got.Unit(), got)
		}
		// Indenting again must use the same whitespace character, so the file
		// never becomes mixed.
		p := NewPane(NewFile("t.go", text, width))
		p.Cursors.Set(0, 0)
		p.Indent()
		// Only the LEADING whitespace is the indentation; a tab inside the
		// line's own text is the document's business, not raj's.
		line := p.File.Line(0)
		lead := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		if lead != strings.Repeat(unit, 2) {
			t.Fatalf("indent %q, want two levels of %q", lead, unit)
		}
	})
}

// The reported failure. A Makefile's tab is syntax, not style: a recipe line
// indented with spaces is "missing separator. Stop.". Both of these produced a
// broken file when the style was detected from content alone — the first
// because a broken Makefile reads as space-indented, the second because a
// Makefile being written has nothing to read.
func TestMakefileAlwaysIndentsWithTabs(t *testing.T) {
	cases := []struct {
		name    string
		path    string
		content string
	}{
		{"already broken", "Makefile", ".PHONY: build\nbuild:\n  go build ./cmd/raj\n"},
		{"being typed", "Makefile", ".PHONY: build\nbuild:\n"},
		{"correct", "Makefile", ".PHONY: build\nbuild:\n\tgo build\n"},
		{"lowercase", "makefile", "build:\n  go build\n"},
		{"gnu", "GNUmakefile", "build:\n  go build\n"},
		{"automake", "Makefile.am", "build:\n  go build\n"},
		{"include", "rules.mk", "build:\n  go build\n"},
		{"nested path", "build/sub/Makefile", "build:\n  go build\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := NewFile(c.path, c.content, 2)
			if !f.Indent.Tabs {
				t.Fatalf("style = %v, want tabs", f.Indent)
			}
			if f.IndentSource() != IndentFromFormat {
				t.Errorf("source = %v, want the format to have decided", f.IndentSource())
			}
			p := NewPane(f)
			p.Cursors.Set(0, 0)
			p.Indent()
			if got := f.Line(0); !strings.HasPrefix(got, "\t") {
				t.Errorf("line = %q, want a leading tab", got)
			}
		})
	}
}

// End to end, as reported: type the recipe line into a fresh Makefile, save,
// and make must be able to read it.
func TestFreshMakefileRecipeGetsATab(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Makefile")
	f := NewFile(path, ".PHONY: build\nbuild:\n", 2)
	p := NewPane(f)
	p.DocEnd(false)
	p.Indent()
	p.InsertText("go build -o ./bin/ ./cmd/raj")
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := ".PHONY: build\nbuild:\n\tgo build -o ./bin/ ./cmd/raj"
	if string(data) != want {
		t.Errorf("saved %q,\n want %q", string(data), want)
	}
}

// A format mandate must not be overridden by the flag, in either direction.
func TestMakefileIgnoresTheDefault(t *testing.T) {
	f := NewFile("Makefile", "build:\n  go build\n", 2)
	f.SetIndentDefault(Indent{Width: 4})
	if !f.Indent.Tabs {
		t.Error("--tab overrode a format requirement")
	}
}

// The existing broken lines are left alone — rewriting a file on open is not
// something an editor should do unasked — so the warning is the only thing that
// says the rest of the file is still broken.
func TestIndentWarning(t *testing.T) {
	broken := NewFile("Makefile", ".PHONY: b\nbuild:\n  go build\n", 2)
	if w := broken.IndentWarning(); !strings.Contains(w, "1 line") || !strings.Contains(w, "tabs") {
		t.Errorf("warning = %q", w)
	}
	fine := NewFile("Makefile", "build:\n\tgo build\n", 2)
	if w := fine.IndentWarning(); w != "" {
		t.Errorf("warned about a correct file: %q", w)
	}
	notMandated := NewFile("t.js", "function f() {\n  return\n}\n", 2)
	if w := notMandated.IndentWarning(); w != "" {
		t.Errorf("warned about a format with no requirement: %q", w)
	}
}

// A Go file with nothing to detect gets tabs by convention, but a Go file that
// uses spaces keeps them: unusual is not broken, so the content still wins.
func TestGoConventionYieldsToContent(t *testing.T) {
	fresh := NewFile("new.go", "package a\n", 4)
	if !fresh.Indent.Tabs || fresh.IndentSource() != IndentFromLanguage {
		t.Errorf("fresh .go: %+v from %v, want tabs by convention", fresh.Indent, fresh.IndentSource())
	}
	spaced := NewFile("old.go", "package a\nfunc f() {\n    return\n}\n", 4)
	if spaced.Indent.Tabs || spaced.IndentSource() != IndentFromContent {
		t.Errorf("space-indented .go: %+v from %v, want spaces from content", spaced.Indent, spaced.IndentSource())
	}
}
