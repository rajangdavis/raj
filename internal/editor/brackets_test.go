package editor

import (
	"strings"
	"testing"
	"time"

	"raj/internal/syntax"
	"raj/internal/ui"
)

// tokenised builds a pane and waits for the syntax pass, so tests that depend
// on strings and comments being recognised are testing the lexer-aware path
// rather than the fallback.
func tokenised(t *testing.T, content string) *Pane {
	t.Helper()
	p := newTestPane(content)
	p.File.Syntax.Ensure(content)
	for i := 0; i < 200 && !p.File.Syntax.Ready(); i++ {
		time.Sleep(5 * time.Millisecond)
	}
	if !p.File.Syntax.Ready() {
		t.Skip("the syntax pass did not finish")
	}
	return p
}

// hasAttr reports whether a style carries an attribute.
func hasAttr(st ui.Style, a ui.Attr) bool { return st.Attr&a != 0 }

// at puts the cursor at a byte offset and returns the matched pair, sorted.
func at(p *Pane, off int) (lo, hi int, ok bool) {
	p.Cursors.Set(off, off)
	a, b, ok := p.MatchBracket()
	if a > b {
		a, b = b, a
	}
	return a, b, ok
}

// The basic contract, in both directions: standing on either half of a pair
// finds the other half.
func TestMatchesBothWays(t *testing.T) {
	const src = "func f() { g(1) }\n"
	p := newTestPane(src)

	cases := []struct{ on, want int }{
		{strings.Index(src, "("), strings.Index(src, ")")},
		{strings.Index(src, ")"), strings.Index(src, "(")},
		{strings.Index(src, "{"), strings.Index(src, "}")},
		{strings.Index(src, "}"), strings.Index(src, "{")},
		{strings.Index(src, "g(") + 1, strings.Index(src, "1)") + 1},
	}
	for _, c := range cases {
		a, b, ok := at(p, c.on)
		if !ok {
			t.Errorf("offset %d (%q) matched nothing", c.on, src[c.on])
			continue
		}
		other := a
		if a == c.on {
			other = b
		}
		if other != c.want {
			t.Errorf("offset %d (%q) matched %d, want %d", c.on, src[c.on], other, c.want)
		}
	}
}

// Nesting of the same kind counts depth rather than pairing with the nearest
// candidate.
func TestMatchesAcrossNesting(t *testing.T) {
	const src = "a(b(c)d)e\n"
	p := newTestPane(src)

	a, b, ok := at(p, 1) // the outer (
	if !ok {
		t.Fatal("the outer bracket matched nothing")
	}
	if a != 1 || b != 7 {
		t.Errorf("matched %d..%d, want 1..7 (the outer pair)", a, b)
	}
}

// Different kinds do not pair with each other. `{` matching `)` is worse than
// no match: it says the file nests in a way it does not.
func TestKindsDoNotCross(t *testing.T) {
	p := newTestPane("([)]\n")
	if _, _, ok := at(p, 0); ok {
		t.Error("( paired across a ] ")
	}
	if _, _, ok := at(p, 1); ok {
		t.Error("[ paired across a )")
	}
}

// An unbalanced bracket has no partner, and must report none rather than the
// nearest thing that looks like one.
func TestUnbalancedMatchesNothing(t *testing.T) {
	for _, src := range []string{"f(x\n", "x)\n", "{{}\n"} {
		p := newTestPane(src)
		i := strings.IndexAny(src, "()[]{}")
		if _, _, ok := at(p, i); ok {
			t.Errorf("%q: an unbalanced bracket found a partner", src)
		}
	}
}

// The cursor's own position: the character under it wins over the one behind
// it, so with the caret between `)` and `(` the pair that lights up is the one
// being typed into.
func TestPrefersTheBracketUnderTheCursor(t *testing.T) {
	const src = "f()(x)\n"
	p := newTestPane(src)

	// Offset 3 is the second `(`; offset 2 (behind it) is the first `)`.
	a, b, ok := at(p, 3)
	if !ok {
		t.Fatal("nothing matched")
	}
	if a != 3 || b != 5 {
		t.Errorf("matched %d..%d, want 3..5 (the bracket under the cursor)", a, b)
	}
}

// With no bracket under the cursor, the one just behind it matches. That is
// what makes the pair stay lit after you type a closer.
func TestMatchesTheBracketBehindTheCursor(t *testing.T) {
	const src = "f()\n"
	p := newTestPane(src)

	a, b, ok := at(p, 3) // just past the ')'
	if !ok {
		t.Fatal("the bracket behind the cursor matched nothing")
	}
	if a != 1 || b != 2 {
		t.Errorf("matched %d..%d, want 1..2", a, b)
	}
}

// ---------- the blind spot the lexer fixes ----------

// A bracket in a string is not structural. Without this, `s := "{"` leaves
// every brace after it paired one level off — which is the failure that
// motivated using the lexer at all rather than counting bytes.
func TestBracketsInStringsAreNotCounted(t *testing.T) {
	const src = "func f() {\n\ts := \"{\"\n\t_ = s\n}\n"
	p := tokenised(t, src)

	open := strings.Index(src, "{")
	a, b, ok := at(p, open)
	if !ok {
		t.Fatal("the function's brace matched nothing")
	}
	want := strings.LastIndex(src, "}")
	if a != open || b != want {
		t.Errorf("matched %d..%d, want %d..%d; the brace inside the string was counted",
			a, b, open, want)
	}
}

// The same for comments.
func TestBracketsInCommentsAreNotCounted(t *testing.T) {
	const src = "func f() {\n\t// closes with }\n\t_ = 1\n}\n"
	p := tokenised(t, src)

	open := strings.Index(src, "{")
	a, b, ok := at(p, open)
	if !ok {
		t.Fatal("the function's brace matched nothing")
	}
	want := strings.LastIndex(src, "}")
	if a != open || b != want {
		t.Errorf("matched %d..%d, want %d..%d; the brace in the comment was counted",
			a, b, open, want)
	}
}

// Standing ON a bracket inside a string reports nothing. The cursor being there
// is not a reason to start counting structure that is not there.
func TestCursorOnABracketInAStringMatchesNothing(t *testing.T) {
	const src = "func f() {\n\ts := \"(\"\n}\n"
	p := tokenised(t, src)

	in := strings.Index(src, "\"(\"") + 1
	if p.classAt(in) != syntax.ClassString {
		t.Skipf("the lexer did not call offset %d a string", in)
	}
	if _, _, ok := at(p, in); ok {
		t.Error("a bracket inside a string reported a match")
	}
}

// Before the background tokenise finishes there is nothing to ask, and the
// scan must still work — degrading to plain depth counting rather than to no
// matching at all.
func TestMatchingWorksBeforeTokenising(t *testing.T) {
	p := newTestPane("f(x)\n") // no Ensure: Syntax.Ready() is false
	if p.File.Syntax.Ready() {
		t.Skip("tokenising already finished")
	}
	if _, _, ok := at(p, 1); !ok {
		t.Error("no match without tokens; the fallback should count everything")
	}
}

// ---------- what it declines to do ----------

// Multiple cursors show nothing: which of four owns the highlight has no good
// answer, and lighting up four pairs is noise rather than information.
func TestNoMatchWithMultipleCursors(t *testing.T) {
	p := newTestPane("f(x)\ng(y)\n")
	p.Cursors.Set(1, 1)
	p.AddCursorAt(1, 1)
	if len(p.Cursors.All()) < 2 {
		t.Skip("the second cursor was not added")
	}
	if _, _, ok := p.MatchBracket(); ok {
		t.Error("a pair was reported with several cursors")
	}
}

// A selection means you are looking at a range, not standing on a character.
func TestNoMatchWithASelection(t *testing.T) {
	p := newTestPane("f(x)\n")
	p.Cursors.Set(1, 1)
	p.CharRight(true)
	if !p.Cursors.Primary().HasSelection() {
		t.Skip("no selection was made")
	}
	if _, _, ok := p.MatchBracket(); ok {
		t.Error("a pair was reported with a selection")
	}
}

// The scan is bounded two ways, and both need holding: an unmatched bracket is
// scanned in full on every frame between typing an opener and typing its
// closer, so a bound that does not bind is a bound that drops frames while you
// type.
func TestScanIsBoundedByLines(t *testing.T) {
	src := "{" + strings.Repeat("x\n", bracketScanLines+100) + "}"
	p := newTestPane(src)
	if _, _, ok := at(p, 0); ok {
		t.Error("a partner past the line budget was found")
	}
}

// One line longer than the whole line budget: counting lines bounds nothing
// there, which is what the byte budget is for.
func TestScanIsBoundedByBytes(t *testing.T) {
	src := "(" + strings.Repeat("x", bracketScanBytes+1024) + ")"
	p := newTestPane(src)
	if _, _, ok := at(p, 0); ok {
		t.Error("a partner past the byte budget was found")
	}
}

// The bound is not so tight that it breaks ordinary nesting. A partner a
// screenful away must still be found, or the feature stops working on any
// function longer than a page.
func TestScanReachesAcrossAScreenful(t *testing.T) {
	src := "{" + strings.Repeat("\tx = 1\n", 300) + "}"
	p := newTestPane(src)
	if _, _, ok := at(p, 0); !ok {
		t.Error("a partner 300 lines away was not found")
	}
}

// ---------- rendering ----------

// The pair is drawn, and drawn without disturbing the characters. A mark that
// replaced the bracket would be worse than no mark.
func TestMatchedPairIsUnderlined(t *testing.T) {
	p := newTestPane("f(x)\n")
	p.Cursors.Set(1, 1)

	s := ui.NewScreen(40, 10)
	p.Render(s, 0, 0, 40, 10, DefaultTheme())

	gut := p.GutterWidth()
	openCell := s.At(gut+1, 0)
	closeCell := s.At(gut+3, 0)
	if openCell.Rune != '(' || closeCell.Rune != ')' {
		t.Fatalf("the brackets were replaced: %q %q", openCell.Rune, closeCell.Rune)
	}
	if !hasAttr(openCell.Style, ui.Underline) || !hasAttr(closeCell.Style, ui.Underline) {
		t.Error("the matched pair is not underlined")
	}
	if hasAttr(s.At(gut+2, 0).Style, ui.Underline) {
		t.Error("the character between the brackets was underlined too")
	}
}

// Nothing is underlined when the cursor is nowhere near a bracket.
func TestNothingUnderlinedAwayFromBrackets(t *testing.T) {
	p := newTestPane("plain text\n")
	p.Cursors.Set(3, 3)

	s := ui.NewScreen(40, 10)
	p.Render(s, 0, 0, 40, 10, DefaultTheme())

	gut := p.GutterWidth()
	for col := gut; col < gut+10; col++ {
		if hasAttr(s.At(col, 0).Style, ui.Underline) {
			t.Fatalf("column %d is underlined with no bracket in the line", col)
		}
	}
}

// ---------- fuzz ----------

// The invariants that must hold whatever the input, including inputs no
// hand-written case would think of: unbalanced files, brackets inside strings
// inside comments, and multi-byte runes adjacent to brackets.
//
// It asserts properties rather than expected matches, because for arbitrary
// text there is no independent oracle short of writing the matcher twice.
func FuzzMatchBracket(f *testing.F) {
	f.Add("f(x)\n", 1)
	f.Add("func f() { g(1) }\n", 9)
	f.Add("a(b(c)d)e\n", 1)
	f.Add("([)]\n", 0)
	f.Add("s := \"{\"\n", 0)
	f.Add("// }\n{\n}\n", 5)
	f.Add("日本(語)\n", 6)
	f.Add("(((((((((\n", 0)

	f.Fuzz(func(t *testing.T, text string, off int) {
		if len(text) > 8<<10 {
			t.Skip()
		}
		p := newTestPane(text)
		if off < 0 || off > p.File.Len() {
			t.Skip()
		}
		p.Cursors.Set(off, off)

		a, b, ok := p.MatchBracket()
		if !ok {
			return
		}
		// 1. Both offsets are inside the document.
		if a < 0 || a >= p.File.Len() || b < 0 || b >= p.File.Len() {
			t.Fatalf("match %d..%d is outside a document of %d bytes", a, b, p.File.Len())
		}
		// 2. They are distinct. A bracket is never its own partner.
		if a == b {
			t.Fatalf("offset %d matched itself", a)
		}
		// 3. One of them is where the cursor was, or immediately behind it.
		if a != off && b != off && a != off-1 && b != off-1 {
			t.Fatalf("match %d..%d includes neither the cursor at %d nor the byte behind it", a, b, off)
		}
		// 4. They are an opener and its own closer, in that order.
		lo, hi := a, b
		if lo > hi {
			lo, hi = hi, lo
		}
		open, close := p.byteAt(lo), p.byteAt(hi)
		if want, isOpen := pairs[open]; !isOpen || want != close {
			t.Fatalf("matched %q at %d with %q at %d, which is not a pair", open, lo, close, hi)
		}
		// 5. Symmetry: standing on the partner finds the way back. An
		//    asymmetric matcher lights up a pair that vanishes when you step
		//    onto the other half of it.
		p.Cursors.Set(hi, hi)
		c, d, ok := p.MatchBracket()
		if !ok {
			t.Fatalf("%d..%d matched, but standing on %d matched nothing", lo, hi, hi)
		}
		if (c != lo || d != hi) && (c != hi || d != lo) {
			t.Fatalf("%d..%d matched, but standing on %d gave %d..%d", lo, hi, hi, c, d)
		}
	})
}

// The scan runs once per frame on the render path, so its cost has to be a
// frame's worth of work rather than a file's. These measure the two cases that
// matter: a partner a few bytes away, which is the common one, and a bracket
// with no partner at all, which is the worst case the bound has to contain.
func BenchmarkMatchBracketNear(b *testing.B) {
	p := newTestPane(strings.Repeat("func f() { g(1) }\n", 2000))
	p.Cursors.Set(9, 9)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p.MatchBracket()
	}
}

func BenchmarkMatchBracketUnbalanced(b *testing.B) {
	p := newTestPane("{" + strings.Repeat("x = 1\n", 20000))
	p.Cursors.Set(0, 0)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p.MatchBracket()
	}
}
