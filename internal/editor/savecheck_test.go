package editor

import (
	"errors"
	"os"
	"testing"

	"raj/internal/piecetable"
)

// Off is the setting's escape hatch and the default: nothing is refused,
// however broken.
func TestCheckSaveTextOffAcceptsAnything(t *testing.T) {
	t.Parallel()
	for _, text := range []string{"{\n", "x := \"abc\n", "f(a]\n", "str(a)\tstr(b)\n"} {
		if err := CheckSaveText("t.go", text, SaveCheckOff); err != nil {
			t.Errorf("CheckSaveText(%q, off) = %v, want nil", text, err)
		}
	}
}

// Parse refuses what Go cannot parse and passes what it can; a non-Go path is
// never parsed, so a file the parser would reject is accepted. Without the
// suffix check a .txt would be refused for not being Go.
func TestCheckSaveTextParse(t *testing.T) {
	t.Parallel()
	valid := "package p\n\nfunc f() {\n\treturn\n}\n"
	if err := CheckSaveText("t.go", valid, SaveCheckParse); err != nil {
		t.Errorf("valid Go = %v, want nil", err)
	}
	// Balanced brackets and quotes, but not a Go statement: only the parser
	// can see this.
	invalid := "package p\n\nfunc f() {\n\t1 +\n}\n"
	if err := CheckSaveText("t.go", invalid, SaveCheckParse); err == nil {
		t.Error("invalid Go = nil, want the parser's refusal")
	}
	// Two calls on one line are not valid Go; parse is where that judgement
	// belongs now that no byte heuristic guesses at it.
	fused := "package p\n\nfunc f() {\n\tstr(a)\tstr(b)\n}\n"
	if err := CheckSaveText("t.go", fused, SaveCheckParse); err == nil {
		t.Error("parse on the fused example = nil, want the parser's refusal")
	}
	// Not Go: parse is not attempted, so a path the parser would reject is
	// accepted.
	if err := CheckSaveText("note.txt", "hello world\n", SaveCheckParse); err != nil {
		t.Errorf(".txt path = %v, want nil", err)
	}
	if err := CheckSaveText("script.py", "def f(:\n", SaveCheckParse); err != nil {
		t.Errorf(".py path = %v, want nil; a non-Go file is unchecked", err)
	}
}

// ParseSaveCheck maps the two setting spellings and reports an unknown one
// rather than guessing; the value is the default either way.
func TestParseSaveCheck(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		want SaveCheck
		ok   bool
	}{
		"off":        {SaveCheckOff, true},
		"parse":      {SaveCheckParse, true},
		" PARSE ":    {SaveCheckParse, true},
		" Off ":      {SaveCheckOff, true},
		"structural": {SaveCheckOff, false},
		"bogus":      {SaveCheckOff, false},
		"":           {SaveCheckOff, false},
	}
	for raw, want := range cases {
		got, ok := ParseSaveCheck(raw)
		if got != want.want || ok != want.ok {
			t.Errorf("ParseSaveCheck(%q) = %v, %v; want %v, %v", raw, got, ok, want.want, want.ok)
		}
	}
}

// A save whose accepted composition is invalid Go must refuse, roll the
// acceptance back and write nothing. Parse is the only refusing mode, so this
// runs with save_check=parse; an unmatched ')' is one thing go/parser rejects.
func TestSaveRefusesUnbalancedCompositionAndRollsBack(t *testing.T) {
	t.Parallel()
	const original = "package p\n\nfunc f() {\n\tx()\n\ty()\n}\n"
	p := savedPane(t, original)
	f := p.File
	f.SaveCheck = SaveCheckParse
	// Append a ')' so the composition is not valid Go.
	proposeAt(t, f, len(original), len(original), ")")
	if got := len(f.Session().Pending()); got != 1 {
		t.Fatalf("setup: pending = %d, want 1", got)
	}
	err := f.SaveOver()
	var sc *SaveCheckError
	if !errors.As(err, &sc) {
		t.Fatalf("SaveOver() = %v, want a SaveCheckError", err)
	}
	if sc.Reason == "" {
		t.Error("reason is empty, want the parser's message")
	}
	if got := len(f.Session().Pending()); got != 1 {
		t.Errorf("pending after a refused save = %d, want the set rolled back", got)
	}
	var id uint64
	for _, g := range f.Session().Pending() {
		id = g.ID
	}
	if got := f.Session().GroupState(id); got != piecetable.Proposed {
		t.Errorf("state after a refused save = %v, want Proposed", got)
	}
	got, readErr := os.ReadFile(f.Path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != original {
		t.Errorf("a refused save changed the file: %q", got)
	}
}

// The same composition writes when the guard is off, which is the default: the
// setting is the escape hatch, not a suggestion.
func TestSaveCheckOffWritesUnbalancedComposition(t *testing.T) {
	t.Parallel()
	const original = "package p\n\nfunc f() {\n\tx()\n\ty()\n}\n"
	p := savedPane(t, original)
	f := p.File
	f.SaveCheck = SaveCheckOff
	proposeAt(t, f, len(original), len(original), ")")
	if err := f.SaveOver(); err != nil {
		t.Fatalf("SaveOver() with save_check=off = %v, want a write", err)
	}
	got, err := os.ReadFile(f.Path)
	if err != nil {
		t.Fatal(err)
	}
	if want := original + ")"; string(got) != want {
		t.Errorf("on disk = %q, want %q", got, want)
	}
	if len(f.Session().Pending()) != 0 {
		t.Error("an off save still left the set pending")
	}
}

// Parse refuses balanced text that is not Go, and the refusal rolls back the
// same way; the same composition writes once the guard is off, proving the
// parser was the check that refused.
func TestSaveParseRefusesBalancedButInvalidGo(t *testing.T) {
	t.Parallel()
	const original = "package p\n\nfunc f() {\n}\n"
	p := savedPane(t, original)
	f := p.File
	f.SaveCheck = SaveCheckParse
	// Insert an incomplete expression before the closing brace: brackets stay
	// balanced, so only the parser can refuse it.
	proposeAt(t, f, 22, 22, "\t1 +\n")
	if err := f.SaveOver(); err == nil {
		t.Fatal("SaveOver() under parse = nil, want a refusal")
	}
	if got := len(f.Session().Pending()); got != 1 {
		t.Errorf("pending after a parse refusal = %d, want the set rolled back", got)
	}
	got, err := os.ReadFile(f.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Errorf("a refused parse save changed the file: %q", got)
	}
	// With the guard off the same composition is accepted.
	f.SaveCheck = SaveCheckOff
	if err := f.SaveOver(); err != nil {
		t.Fatalf("SaveOver() under off = %v, want the write", err)
	}
	if got := len(f.Session().Pending()); got != 0 {
		t.Error("an off save left the set pending")
	}
}
