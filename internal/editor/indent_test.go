package editor

import (
	"strings"
	"testing"
	"time"
)

// indented builds a pane with auto-pairing on and the cursor at the end.
func indented(t *testing.T, text string) *Pane {
	t.Helper()
	p := newTestPane(text)
	p.AutoPairs = true
	p.DocEnd(false)
	return p
}

// lexed is indented with the syntax pass finished, for the cases that depend on
// strings and comments being recognised.
func lexed(t *testing.T, text string) *Pane {
	t.Helper()
	p := indented(t, text)
	p.File.Syntax.Ensure(text)
	for i := 0; i < 200 && !p.File.Syntax.Ready(); i++ {
		time.Sleep(5 * time.Millisecond)
	}
	if !p.File.Syntax.Ready() {
		t.Skip("the syntax pass did not finish")
	}
	return p
}

// lastLine is the line the cursor ended on.
func lastLine(p *Pane) string {
	lines := strings.Split(p.File.Text(), "\n")
	return lines[len(lines)-1]
}

// ---------- opening a level ----------

// The case the previous rule could not reach: a line that opens a block and
// does not close it wants the next line indented further, not the same.
func TestNewlineAfterAnOpenBraceIndents(t *testing.T) {
	p := indented(t, "func f() {")
	typing(p, "\n")
	if got := lastLine(p); got != "  " {
		t.Errorf("indent = %q, want one level", got)
	}
}

// It compounds with the existing indent rather than replacing it.
func TestIndentCompoundsWithTheExistingOne(t *testing.T) {
	p := indented(t, "    if x {")
	typing(p, "\n")
	if got := lastLine(p); got != "      " {
		t.Errorf("indent = %q, want the line's four plus one level", got)
	}
}

// Brackets that open and close on the same line leave nothing outstanding.
func TestBalancedLineDoesNotIndent(t *testing.T) {
	p := indented(t, "  x := f(1)")
	typing(p, "\n")
	if got := lastLine(p); got != "  " {
		t.Errorf("indent = %q, want the line's own two", got)
	}
}

// One level, not one per bracket: a line ending in two openers is still one
// block to the eye, and two levels would put the next line somewhere nobody
// expects.
func TestSeveralOpenersAddOneLevel(t *testing.T) {
	p := indented(t, "x := map[k]v{{")
	typing(p, "\n")
	if got := lastLine(p); got != "  " {
		t.Errorf("indent = %q, want exactly one level", got)
	}
}

// A brace in a string is not structure. Without the lexer this adds a level to
// everything after it, which is the failure that makes naive auto-indent worse
// than none.
func TestBraceInAStringDoesNotIndent(t *testing.T) {
	p := lexed(t, "func f() {\n\ts := \"{\"")
	typing(p, "\n")
	if got := lastLine(p); got != "\t" {
		t.Errorf("indent = %q, want the line's own tab and no more", got)
	}
}

// And in a comment.
func TestBraceInACommentDoesNotIndent(t *testing.T) {
	p := lexed(t, "func f() {\n\t// opens with {")
	typing(p, "\n")
	if got := lastLine(p); got != "\t" {
		t.Errorf("indent = %q, want the line's own tab and no more", got)
	}
}

// Before the background tokenise finishes there is nothing to ask, and the rule
// still has to work — degrading to counting every bracket, which is what it
// would have done anyway.
func TestIndentWorksBeforeTokenising(t *testing.T) {
	p := indented(t, "func f() {")
	if p.File.Syntax.Ready() {
		t.Skip("tokenising already finished")
	}
	typing(p, "\n")
	if got := lastLine(p); got != "  " {
		t.Errorf("indent = %q without tokens, want one level", got)
	}
}

// Tabs stay tabs. The unit added is raj's, but the line's own indent is copied
// verbatim, so a tab-indented file does not acquire spaces at the front.
func TestTabIndentIsPreserved(t *testing.T) {
	p := indented(t, "\t\tif x {")
	typing(p, "\n")
	if got := lastLine(p); !strings.HasPrefix(got, "\t\t") {
		t.Errorf("indent = %q, want the tabs kept", got)
	}
}

// ---------- closing a level ----------

// The other half: a closer typed on a line of its own lines up with whatever
// opened it.
func TestClosingBraceOutdents(t *testing.T) {
	p := indented(t, "func f() {\n  x := 1\n  ")
	typing(p, "}")
	if got := lastLine(p); got != "}" {
		t.Errorf("line = %q, want the brace at the opener's indent", got)
	}
}

// Nested, so the answer is not simply column zero.
func TestClosingBraceMatchesItsOwnOpener(t *testing.T) {
	p := indented(t, "func f() {\n  if x {\n    y()\n    ")
	typing(p, "}")
	if got := lastLine(p); got != "  }" {
		t.Errorf("line = %q, want the inner opener's two spaces", got)
	}
}

// A closer with code before it is part of an expression. Moving the line would
// be rearranging code nobody asked about.
func TestClosingBraceAfterCodeDoesNotMove(t *testing.T) {
	p := indented(t, "func f() {\n      x := g(1")
	typing(p, ")")
	if got := lastLine(p); got != "      x := g(1)" {
		t.Errorf("line = %q, want it left where it was", got)
	}
}

// An unmatched closer has nothing to line up with, and must be left alone
// rather than snapped to column zero.
func TestUnmatchedCloserDoesNotMove(t *testing.T) {
	p := indented(t, "    ")
	typing(p, "}")
	if got := lastLine(p); got != "    }" {
		t.Errorf("line = %q, want it left where it was", got)
	}
}

// The caret stays after the bracket that was just typed, rather than jumping to
// wherever the byte count landed once the line got shorter.
func TestCaretStaysAfterTheCloser(t *testing.T) {
	p := indented(t, "func f() {\n      ")
	typing(p, "}")
	head := p.Cursors.Primary().Head
	if head != p.File.Len() {
		t.Errorf("caret at %d, want the end of the document at %d", head, p.File.Len())
	}
	if got := p.byteBefore(head); got != '}' {
		t.Errorf("the caret is after %q, want the brace just typed", got)
	}
}

// With several cursors the closers land on several lines, each wanting a
// different indent. Doing it for one and not the others is worse than none.
func TestSeveralCursorsDoNotOutdent(t *testing.T) {
	p := indented(t, "func f() {\n    \n    ")
	p.Cursors.Set(p.File.Len(), p.File.Len())
	p.AddCursorAt(4, 1)
	if len(p.Cursors.All()) < 2 {
		t.Skip("the second cursor was not added")
	}
	typing(p, "}")
	if strings.Contains(p.File.Text(), "\n}") {
		t.Errorf("a line was outdented with several cursors:\n%q", p.File.Text())
	}
}

// Typing over an auto-inserted closer moves the caret and edits nothing, so it
// must not reindent either — the reindent runs off the byte behind the cursor,
// and after a type-over that byte is a closer somebody else put there.
func TestTypingOverACloserDoesNotOutdent(t *testing.T) {
	p := indented(t, "func f() {\n    x := ")
	typing(p, "(")
	if got := lastLine(p); got != "    x := ()" {
		t.Fatalf("setup: auto-pairing gave %q", got)
	}
	typing(p, ")") // over the one already there
	if got := lastLine(p); got != "    x := ()" {
		t.Errorf("line = %q, want it unmoved by the type-over", got)
	}
}

// The two halves compose: open a brace, get a level; close it, get the level
// back. This is the round trip somebody actually types.
func TestOpenAndCloseRoundTrip(t *testing.T) {
	p := indented(t, "func f() {")
	p.AutoPairs = false // so the closer is not inserted for us
	typing(p, "\nx := 1\n}")

	want := "func f() {\n  x := 1\n}"
	if got := p.File.Text(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
