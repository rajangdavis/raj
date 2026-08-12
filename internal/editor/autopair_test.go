package editor

import (
	"strings"
	"testing"
)

// typing feeds text one rune at a time through the keystroke path, which is
// what makes these tests mean anything: routing the whole string through
// InsertText would bypass exactly the code under test.
func typing(p *Pane, s string) {
	for _, r := range s {
		p.HandleText(string(r))
	}
}

// paired builds a pane with the conveniences on and the cursor at the end.
func paired(t *testing.T, text string) *Pane {
	t.Helper()
	p := newTestPane(text)
	p.AutoPairs = true
	p.DocEnd(false)
	return p
}

func body(p *Pane) string { return p.File.Text() }

// The reason auto-indent exists: a newline inside an indented block starts
// where the previous line started, rather than at column zero.
func TestNewlineCarriesIndent(t *testing.T) {
	cases := []struct{ start, want string }{
		{"        x", "        x\n        "},
		{"\t\tx", "\t\tx\n\t\t"},         // tabs are copied verbatim
		{"x", "x\n"},                     // nothing to carry
		{"    ", "    \n    "},           // a blank indented line still carries
		{"  \tmixed", "  \tmixed\n  \t"}, // mixed leading whitespace, unchanged
	}
	for _, c := range cases {
		p := paired(t, c.start)
		typing(p, "\n")
		if got := body(p); got != c.want {
			t.Errorf("from %q: got %q, want %q", c.start, got, c.want)
		}
	}
}

// The cursor ends at the end of the carried indentation, not before it. If it
// landed at column zero the indent would be cosmetic and the next keystroke
// would sit in front of it.
func TestNewlineLeavesTheCursorAfterTheIndent(t *testing.T) {
	p := paired(t, "    x")
	typing(p, "\ny")
	if got, want := body(p), "    x\n    y"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// A newline between a bracket pair opens a block: the closer moves to its own
// line at the original indentation, and the cursor waits on an indented line
// between them. This is the shape that would otherwise be typed by hand every
// time.
func TestNewlineBetweenBracketsOpensABlock(t *testing.T) {
	// The fixture indents with two spaces (newTestPane uses a tab width of
	// 2), so the middle line is the original indent plus one more unit.
	p := paired(t, "    if x {}")
	p.CharLeft(false) // between { and }
	typing(p, "\n")
	want := "    if x {\n      \n    }"
	if got := body(p); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	typing(p, "y")
	if got, want := body(p), "    if x {\n      y\n    }"; got != want {
		t.Errorf("after typing: got %q, want %q", got, want)
	}
}

// Brackets bring their closers, and the cursor lands between them.
func TestBracketsAutoClose(t *testing.T) {
	for _, c := range []struct{ typed, want string }{
		{"(", "()"},
		{"[", "[]"},
		{"{", "{}"},
		{"([{", "([{}])"},
	} {
		p := paired(t, "")
		typing(p, c.typed)
		if got := body(p); got != c.want {
			t.Errorf("typing %q gave %q, want %q", c.typed, got, c.want)
		}
	}
}

// Typing the closer yourself moves over the one already there instead of
// adding a second. Without this, auto-closing fights muscle memory.
func TestTypingAClosingBracketMovesOverIt(t *testing.T) {
	p := paired(t, "")
	typing(p, "(x)")
	if got, want := body(p), "(x)"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	typing(p, ";")
	if got, want := body(p), "(x);"; got != want {
		t.Errorf("cursor was not past the closer: got %q, want %q", got, want)
	}
}

// A closer with nothing to move over is inserted normally, or an unbalanced
// file could never be repaired by typing.
func TestAClosingBracketWithNothingToSkipIsInserted(t *testing.T) {
	p := paired(t, "foo")
	typing(p, ")")
	if got, want := body(p), "foo)"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// Typing ( before an existing word means wrapping it, so the closer would land
// in the middle of the word. Insert the bare bracket instead.
func TestNoAutoCloseBeforeAWord(t *testing.T) {
	p := paired(t, "value")
	p.DocStart(false)
	typing(p, "(")
	if got, want := body(p), "(value"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// The apostrophe case, which is the one that would make this feature hated:
// contractions must not sprout a second quote.
func TestApostrophesInProse(t *testing.T) {
	p := paired(t, "")
	typing(p, "// don't and it's")
	if got, want := body(p), "// don't and it's"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// A quote in a position where it can only be opening does close itself.
func TestQuotesCloseWhenUnambiguous(t *testing.T) {
	p := paired(t, "")
	typing(p, `x = "`)
	if got, want := body(p), `x = ""`; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// A third quote does not add a fourth: that is a docstring or a fence opening,
// not a pair.
func TestThirdQuoteDoesNotPair(t *testing.T) {
	p := paired(t, "")
	typing(p, `"""`)
	if got, want := body(p), `"""`; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// Typing a quote or bracket with a selection wraps it, which is the case worth
// having the feature for at all.
func TestTypingAPairAroundASelection(t *testing.T) {
	for _, c := range []struct{ typed, want string }{
		{`"`, `x = "name"`},
		{"(", "x = (name)"},
		{"[", "x = [name]"},
	} {
		p := paired(t, "x = name")
		p.DocEnd(false)
		for i := 0; i < 4; i++ {
			p.CharLeft(true) // select "name"
		}
		typing(p, c.typed)
		if got := body(p); got != c.want {
			t.Errorf("typing %q gave %q, want %q", c.typed, got, c.want)
		}
	}
}

// Backspacing over an empty pair removes both halves, or auto-pairing costs a
// keystroke every time a bracket is typed and reconsidered.
func TestBackspaceRemovesAnEmptyPair(t *testing.T) {
	p := paired(t, "")
	typing(p, "foo(")
	p.DeleteBackward()
	if got, want := body(p), "foo"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// A pair with something inside it is not empty, so backspace deletes one
// character as it always did.
func TestBackspaceInsideANonEmptyPairIsNormal(t *testing.T) {
	p := paired(t, "")
	typing(p, "(ab")
	p.DeleteBackward()
	if got, want := body(p), "(a)"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// With the setting off, every keystroke is literal — the escape hatch has to
// actually work, and auto-indent stays on because it is not the same promise.
func TestAutoPairsOff(t *testing.T) {
	p := newTestPane("    x")
	p.AutoPairs = false
	p.DocEnd(false)
	typing(p, "(\"")
	if got, want := body(p), `    x("`; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	typing(p, "\n")
	if !strings.HasSuffix(body(p), "\n    ") {
		t.Errorf("auto-indent should not be gated by AutoPairs: %q", body(p))
	}
}

// A paste is data, not keystrokes. Routing it through the pairing logic would
// corrupt it — every bracket in the pasted text would acquire a partner.
func TestPasteIsNotPaired(t *testing.T) {
	p := paired(t, "")
	p.Paste("func f() { return (1) }")
	if got, want := body(p), "func f() { return (1) }"; got != want {
		t.Errorf("paste was altered: got %q, want %q", got, want)
	}
}

// The same for a programmatic insert, which is the path an agent's edits take.
func TestInsertTextIsNotPaired(t *testing.T) {
	p := paired(t, "")
	p.InsertText("if (x) {")
	if got, want := body(p), "if (x) {"; got != want {
		t.Errorf("insert was altered: got %q, want %q", got, want)
	}
}

// Every convenience here is one undo step, because a keystroke is one thing
// that happened however many bytes it moved.
func TestEachConvenienceIsOneUndo(t *testing.T) {
	cases := []struct{ setup, typed, after string }{
		{"", "(", "()"},
		{"    x", "\n", "    x\n    "},
		{"", `"`, `""`},
	}
	for _, c := range cases {
		p := paired(t, c.setup)
		typing(p, c.typed)
		if got := body(p); got != c.after {
			t.Fatalf("setup %q + %q gave %q, want %q", c.setup, c.typed, got, c.after)
		}
		p.history(p.File.Undo(p.Author))
		if got := body(p); got != c.setup {
			t.Errorf("one undo of %q left %q, want %q", c.typed, got, c.setup)
		}
	}
}

// Block-opening is two insertions in one edit, so it is also one undo.
func TestBlockOpenIsOneUndo(t *testing.T) {
	p := paired(t, "if x {}")
	p.CharLeft(false)
	typing(p, "\n")
	p.history(p.File.Undo(p.Author))
	if got, want := body(p), "if x {}"; got != want {
		t.Errorf("undo left %q, want %q", got, want)
	}
}

// Multiple cursors get the same treatment at each of them, and stay in step.
func TestMultiCursorPairing(t *testing.T) {
	p := paired(t, "a\nb\nc")
	p.DocStart(false)
	p.LineEnd(false)
	p.AddCursorVertical(+1)
	p.AddCursorVertical(+1)
	typing(p, "(")
	if got, want := body(p), "a()\nb()\nc()"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	typing(p, "x")
	if got, want := body(p), "a(x)\nb(x)\nc(x)"; got != want {
		t.Errorf("cursors were not inside their pairs: got %q, want %q", got, want)
	}
}

// Nothing here may lose a keystroke. Whatever the buffer and wherever the
// cursor, typing a character either inserts it or moves over an identical one
// already present — the document never comes out shorter, and never loses the
// character that was typed.
func TestTypingNeverLosesACharacter(t *testing.T) {
	texts := []string{"", "x", "()", "\"\"", "    indented", "a\nb", "{}", "'", "((("}
	chars := []string{"(", ")", "[", "]", "{", "}", `"`, "'", "`", "a", " ", "\n", "_"}
	for _, text := range texts {
		for _, ch := range chars {
			for _, at := range []string{"start", "middle", "end"} {
				p := paired(t, text)
				switch at {
				case "start":
					p.DocStart(false)
				case "middle":
					p.DocStart(false)
					for i := 0; i < len(text)/2; i++ {
						p.CharRight(false)
					}
				}
				before := body(p)
				typing(p, ch)
				after := body(p)
				if len(after) < len(before) {
					t.Errorf("%q at %s + %q shrank the document: %q -> %q",
						text, at, ch, before, after)
				}
				if !strings.Contains(after, ch) && ch != "\n" {
					t.Errorf("%q at %s + %q lost the character: %q",
						text, at, ch, after)
				}
			}
		}
	}
}
