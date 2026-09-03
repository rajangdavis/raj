package editor

import (
	"reflect"
	"strings"
	"testing"

	"raj/internal/syntax"
)

// tokenTexts is the text every cached span claims to describe, per line. It is
// the end-to-end version of the invariant the highlighter's splice logic keeps:
// colours may be a version out of date, but they must sit on the characters
// they were computed for.
func tokenTexts(p *Pane) [][]string {
	lines := strings.Split(p.File.Text(), "\n")
	out := make([][]string, len(lines))
	for n, line := range lines {
		for _, s := range p.File.Syntax.Line(n) {
			if s.Start < 0 || s.End > len(line) || s.Start > s.End {
				out[n] = append(out[n], "<out of range>")
				continue
			}
			out[n] = append(out[n], line[s.Start:s.End])
		}
	}
	return out
}

// Indenting and out-denting is the reported symptom: the colours used to stay
// one tab to the left of the text until something else forced a retokenise.
func TestIndentKeepsHighlightingAligned(t *testing.T) {
	p := lexed(t, "func f() {\nx := \"hi\" // note\n}\n")
	before := tokenTexts(p)

	p.Cursors.Set(p.File.LineStart(1), p.File.LineStart(1))
	p.Indent()
	if got := tokenTexts(p); !reflect.DeepEqual(got[1], before[1]) {
		t.Errorf("after indent, line 1 tokens cover %q, want %q", got[1], before[1])
	}
	p.Outdent()
	if got := tokenTexts(p); !reflect.DeepEqual(got, before) {
		t.Errorf("after out-dent, tokens cover %q, want %q", got, before)
	}
}

// Typing a line comment marker in front of a line shifts the whole line; the
// tokens after it have to shift with it.
func TestCommentingKeepsHighlightingAligned(t *testing.T) {
	p := lexed(t, "package p\n\nvar x = 1\n")
	before := tokenTexts(p)

	p.Cursors.Set(p.File.LineStart(2), p.File.LineStart(2))
	p.InsertText("// ")
	if got := tokenTexts(p); !reflect.DeepEqual(got[2], before[2]) {
		t.Errorf("after commenting, line 2 tokens cover %q, want %q", got[2], before[2])
	}
}

// Splitting a line has to carry the second half's tokens onto the new line, and
// keep every line below it addressed by the right number.
func TestNewlineKeepsHighlightingAligned(t *testing.T) {
	p := lexed(t, "var x = 1; var y = 2\nvar z = \"tail\"\n")
	tail := tokenTexts(p)[1]

	at := strings.Index(p.File.Text(), "var y")
	p.Cursors.Set(at, at)
	p.InsertText("\n")
	got := tokenTexts(p)
	if !reflect.DeepEqual(got[2], tail) {
		t.Errorf("after the split, line 2 tokens cover %q, want %q", got[2], tail)
	}
}

// Undo runs the edit backwards through the same path, so the cache has to come
// back with it.
func TestUndoKeepsHighlightingAligned(t *testing.T) {
	p := lexed(t, "func f() {\n\ts := \"hi\"\n}\n")
	before := tokenTexts(p)

	p.Cursors.Set(p.File.LineStart(1), p.File.LineStart(1))
	p.InsertText("\t\tnoise")
	p.history(p.File.Undo(p.Author))
	if got := tokenTexts(p); !reflect.DeepEqual(got, before) {
		t.Errorf("after undo, tokens cover %q, want %q", got, before)
	}
}

// A string's class has to travel with its text too, since bracket matching and
// indentation both ask the highlighter what an offset is inside.
func TestClassStaysWithItsTextAfterIndent(t *testing.T) {
	p := lexed(t, "func f() {\nx := \"{{{\"\n}\n")
	line := p.File.Line(1)
	quote := strings.Index(line, "{")
	if syntax.ClassAt(p.File.Syntax.Line(1), quote) != syntax.ClassString {
		t.Fatal("setup: the braces should be inside a string")
	}
	p.Cursors.Set(p.File.LineStart(1), p.File.LineStart(1))
	p.Indent()
	line = p.File.Line(1)
	if got := strings.Index(line, "{"); syntax.ClassAt(p.File.Syntax.Line(1), got) != syntax.ClassString {
		t.Error("after indent, the braces in the string are no longer classed as string")
	}
}
