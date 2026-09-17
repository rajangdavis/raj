package hover

import (
	"strings"
	"testing"

	"raj/internal/keys"
	"raj/internal/ui"
	"raj/internal/widget"
)

// draw renders a panel into a screen and returns the rows as strings.
func draw(p *Panel, w, h, topLine int) *ui.Screen {
	s := ui.NewScreen(w, h)
	p.Render(s, 0, 0, w, h, topLine, widget.DefaultTheme())
	return s
}

// frame is every row of a screen joined, for containment checks.
func frame(s *ui.Screen) string {
	_, rows := s.Size()
	var b strings.Builder
	for y := range rows {
		b.WriteString(s.Row(y))
		b.WriteByte('\n')
	}
	return b.String()
}

// styleOf finds the first cell of a word in a rendered frame and returns its
// style, so a markdown test can assert on a styling hook without knowing where
// the renderer placed the word.
func styleOf(s *ui.Screen, word string) (ui.Style, bool) {
	_, rows := s.Size()
	for y := range rows {
		row := s.Row(y)
		if before, _, ok := strings.Cut(row, word); ok {
			// before is a byte offset; a cell is a display column. The border
			// and any wide rune before the word make the two differ.
			return s.At(textCols(before), y).Style, true
		}
	}
	return ui.Style{}, false
}

// lineText is a styled line's characters, for wrapping assertions.
func textCols(s string) (n int) {
	for _, r := range s {
		n += ui.RuneWidth(r)
	}
	return
}

func lineText(l styledLine) string {
	var b strings.Builder
	for _, sp := range l {
		b.WriteString(sp.text)
	}
	return b.String()
}

// ---------- content ----------

// The point of the panel: the lines a server put there survive. Folding them
// onto one row was the status line's limitation.
func TestKeepsItsLines(t *testing.T) {
	var p Panel
	p.Show("func F(x int) error\n\nDoes a thing.", 0, 0)
	if !p.Open {
		t.Fatal("nothing opened")
	}
	if n := strings.Count(p.Text(), "\n"); n < 1 {
		t.Errorf("text = %q; the lines were folded", p.Text())
	}
}

// Empty text opens nothing rather than an empty box, so a caller can pass
// whatever the server returned without checking it first.
func TestEmptyTextOpensNothing(t *testing.T) {
	var p Panel
	p.Show("", 0, 0)
	if p.Open {
		t.Error("an empty answer opened a panel")
	}
	p.Show("   \n\n  \n", 0, 0)
	if p.Open {
		t.Error("whitespace opened a panel")
	}
}

// A fence is part of what the server sent: it is kept literally so the block is
// visibly a block and not code that happens to sit in the prose. Modelled on
// TestFencesAreStripped, the behaviour it replaces.
func TestFencedCodeKeepsItsFence(t *testing.T) {
	var p Panel
	p.Show("```go\nfunc F(x int) error\n```", 0, 0)
	s := draw(&p, 60, 20, 0)
	body := frame(s)
	if !strings.Contains(body, "```go") {
		t.Errorf("the opening fence was stripped:\n%s", body)
	}
	if !strings.Contains(body, "func F(x int) error") {
		t.Errorf("the signature was lost:\n%s", body)
	}
	if !strings.Contains(p.Text(), "```go") {
		t.Errorf("text = %q; the fence was stripped from the text model", p.Text())
	}
	if st, ok := styleOf(s, "func F"); !ok || st != roleStyle(roleCode, widget.DefaultTheme()) {
		t.Errorf("code line style = %+v, want the code hook", st)
	}
}

// A fence closes only against a run at least as long as the one that opened it.
// A four-backtick fence around a three-backtick line keeps that line as code
// and closes at its own four-backtick line. Modelled on
// TestFencedCodeKeepsItsFence.
func TestLongerFenceIsNotClosedByAShorterOne(t *testing.T) {
	var p Panel
	p.Show("````\n```\ninner\n````\nafter", 0, 0)
	s := draw(&p, 60, 20, 0)
	th := widget.DefaultTheme()
	if st, ok := styleOf(s, "inner"); !ok || st != roleStyle(roleCode, th) {
		t.Errorf("inner style = %+v, want the code hook", st)
	}
	if st, ok := styleOf(s, "after"); !ok || st != roleStyle(roleText, th) {
		t.Errorf("after style = %+v, want prose; the fence did not close", st)
	}
}

// A fence with no closing run swallows the rest of the answer as code rather
// than leaking it back into prose. Modelled on TestFencedCodeKeepsItsFence.
func TestUnclosedFenceKeepsTheRestAsCode(t *testing.T) {
	var p Panel
	p.Show("````\n```\ninner", 0, 0)
	s := draw(&p, 60, 20, 0)
	th := widget.DefaultTheme()
	if st, ok := styleOf(s, "inner"); !ok || st != roleStyle(roleCode, th) {
		t.Errorf("inner style = %+v, want the code hook", st)
	}
}

// What the fences contained is code, and code is the part whose spacing
// matters. Rendering the block must not touch the indentation inside it.
func TestIndentationInsideFencesSurvives(t *testing.T) {
	var p Panel
	p.Show("```go\ntype T struct {\n\tField int\n}\n```", 0, 0)
	if !strings.Contains(p.Text(), "\tField int") {
		t.Errorf("text = %q; the indentation was stripped", p.Text())
	}
}

// Removing a fence can leave a blank line where the fence was, next to the
// blank line that was already there. Vertical space is what the panel has
// least of.
func TestBlankRunsCollapse(t *testing.T) {
	var p Panel
	p.Show("a\n\n\n\n\nb", 0, 0)
	if strings.Contains(p.Text(), "\n\n\n") {
		t.Errorf("text = %q; a run of blank lines survived", p.Text())
	}
}

// Inline code and emphasis are written in punctuation; the renderer drops the
// punctuation and tags the run, which is the hook the theme styles.
func TestInlineCodeBoldAndItalic(t *testing.T) {
	var p Panel
	p.Show("Call `Fmt` and be **loud** then *quiet*.", 0, 0)
	s := draw(&p, 60, 20, 0)

	th := widget.DefaultTheme()
	if st, ok := styleOf(s, "Fmt"); !ok || st != roleStyle(roleCode, th) {
		t.Errorf("inline code style = %+v, want the code hook", st)
	}
	if st, ok := styleOf(s, "loud"); !ok || st != roleStyle(roleBold, th) {
		t.Errorf("bold style = %+v, want the bold hook", st)
	}
	if st, ok := styleOf(s, "quiet"); !ok || st != roleStyle(roleItalic, th) {
		t.Errorf("italic style = %+v, want the italic hook", st)
	}
	for _, marker := range []string{"**", "*quiet*", "`Fmt`"} {
		if strings.Contains(p.Text(), marker) {
			t.Errorf("text = %q; %q survived rendering", p.Text(), marker)
		}
	}
}

// An asterisk pair in plain prose is not emphasis. The markers need a word just
// inside them, or `2 * 3 * 4` loses them and reads as `2  3  4`. This is the
// asterisk counterpart of TestUnderscoresInIdentifiersAreNotEmphasis, using the
// same wordByte test.
func TestAsterisksInPlainTextAreNotEmphasis(t *testing.T) {
	var p Panel
	p.Show("2 * 3 * 4", 0, 0)
	if got := p.Text(); got != "2 * 3 * 4" {
		t.Errorf("text = %q; the asterisks were read as emphasis", got)
	}
}

// Asterisk emphasis that does flank a word still renders with its role, and a
// plain-text asterisk in the same line stays put. Modelled on
// TestInlineCodeBoldAndItalic.
func TestAsteriskEmphasisStillRenders(t *testing.T) {
	var p Panel
	p.Show("*italic* and **bold** but 2 * 3 * 4", 0, 0)
	s := draw(&p, 60, 20, 0)
	th := widget.DefaultTheme()
	if st, ok := styleOf(s, "italic"); !ok || st != roleStyle(roleItalic, th) {
		t.Errorf("italic style = %+v, want the italic hook", st)
	}
	if st, ok := styleOf(s, "bold"); !ok || st != roleStyle(roleBold, th) {
		t.Errorf("bold style = %+v, want the bold hook", st)
	}
	if got := p.Text(); got != "italic and bold but 2 * 3 * 4" {
		t.Errorf("text = %q; a plain asterisk was read as emphasis", got)
	}
}

// An asterisk bullet is a list marker, not emphasis: listItem runs before
// inline. Modelled on TestListItemsGetBullets.
func TestAsteriskBulletStillRenders(t *testing.T) {
	var p Panel
	p.Show("* first\n* second\n\n2 * 3 * 4", 0, 0)
	s := draw(&p, 60, 20, 0)
	if !strings.Contains(frame(s), "• first") {
		t.Errorf("the asterisk bullet was lost:\n%s", frame(s))
	}
	if st, ok := styleOf(s, "• first"); !ok || st != roleStyle(roleMarker, widget.DefaultTheme()) {
		t.Errorf("bullet style = %+v, want the marker hook", st)
	}
	if got := p.Text(); !strings.Contains(got, "2 * 3 * 4") {
		t.Errorf("text = %q; a plain asterisk line was mangled", got)
	}
}

// Supporting `_` for italic is only safe if an identifier's underscores are not
// read as emphasis, which is the failure mode it would otherwise have.
func TestUnderscoresInIdentifiersAreNotEmphasis(t *testing.T) {
	var p Panel
	p.Show("call max_width_count now", 0, 0)
	if got := p.Text(); got != "call max_width_count now" {
		t.Errorf("text = %q; an identifier was treated as emphasis", got)
	}
}

// The new asterisk guard must not reach the underscore one: an identifier keeps
// its underscores even in a line that also carries asterisk-flanked text.
// Modelled on TestUnderscoresInIdentifiersAreNotEmphasis.
func TestAsteriskGuardLeavesUnderscoreGuard(t *testing.T) {
	var p Panel
	p.Show("call max_width_count with 2 * 3 * 4", 0, 0)
	if got := p.Text(); got != "call max_width_count with 2 * 3 * 4" {
		t.Errorf("text = %q; a guard over-reached", got)
	}
}

// Lists keep their shape: an unordered item gains a bullet, an ordered item
// keeps its number, and the marker is its own styling hook.
func TestListItemsGetBullets(t *testing.T) {
	var p Panel
	p.Show("Options:\n\n- first\n- second\n\n1. one\n2. two", 0, 0)
	s := draw(&p, 60, 20, 0)
	body := frame(s)
	for _, want := range []string{"• first", "• second", "1. one", "2. two"} {
		if !strings.Contains(body, want) {
			t.Errorf("rendered list is missing %q:\n%s", want, body)
		}
	}
	if st, ok := styleOf(s, "• first"); !ok || st != roleStyle(roleMarker, widget.DefaultTheme()) {
		t.Errorf("bullet style = %+v, want the marker hook", st)
	}
}

// ---------- placement ----------

// Anchored below the cursor when there is room, which is the common case.
func TestDrawsBelowTheAnchor(t *testing.T) {
	var p Panel
	p.Show("hello", 2, 4)
	s := draw(&p, 60, 20, 0)

	// Row 2 is the anchor line, so the box starts at row 3.
	if !strings.Contains(s.Row(4), "hello") {
		t.Errorf("the text is not below the anchor:\n%s", frame(s))
	}
}

// Near the bottom it flips above, because a box that runs off the terminal does
// so exactly when the cursor is near the bottom, which is where people write.
func TestFlipsAboveNearTheBottom(t *testing.T) {
	var p Panel
	p.Show("one\ntwo\nthree", 18, 4)
	s := draw(&p, 60, 20, 0)

	body := frame(s)
	if !strings.Contains(body, "one") {
		t.Fatalf("the panel was not drawn at all:\n%s", body)
	}
	// Everything must land above the anchor row rather than off the screen.
	for y := 18; y < 20; y++ {
		if strings.Contains(s.Row(y), "one") {
			t.Errorf("the panel is still below the anchor:\n%s", body)
		}
	}
}

// Near the right edge it slides left rather than clipping the words, which is
// the whole reason not to just anchor and truncate.
func TestSlidesLeftAtTheRightEdge(t *testing.T) {
	var p Panel
	p.Show("a readable signature here", 2, 55)
	s := draw(&p, 60, 20, 0)

	if !strings.Contains(frame(s), "a readable signature here") {
		t.Errorf("the text was clipped instead of sliding left:\n%s", frame(s))
	}
}

// A pane with no room for a box draws nothing rather than something broken.
func TestTooSmallDrawsNothing(t *testing.T) {
	var p Panel
	p.Show("hello", 0, 0)
	for _, size := range [][2]int{{8, 20}, {60, 2}, {4, 4}} {
		s := draw(&p, size[0], size[1], 0)
		if strings.Contains(frame(s), "hello") {
			t.Errorf("%dx%d drew a panel anyway", size[0], size[1])
		}
	}
}

// ---------- fitting ----------

// Content taller than the box is reachable by scrolling rather than reported as
// lost with a "+N more" that cannot be opened. Modelled on
// TestOverlongContentSaysItIsTruncated, the behaviour it replaces.
func TestOverlongContentIsScrollable(t *testing.T) {
	var p Panel
	var lines []string
	for i := range 40 {
		lines = append(lines, "line "+itoa(i))
	}
	p.Show(strings.Join(lines, "\n"), 0, 0)
	s := draw(&p, 60, 20, 0)

	if !p.Scrollable() {
		t.Fatal("the panel did not notice its content overflows")
	}
	if !strings.Contains(frame(s), "line 0") {
		t.Fatalf("the top was not shown:\n%s", frame(s))
	}
	if strings.Contains(frame(s), "line 39") {
		t.Fatal("the whole answer fit; the fixture is not tall enough")
	}
	if !strings.Contains(frame(s), "more") {
		t.Errorf("no hint that there is more below:\n%s", frame(s))
	}

	// Scrolling reaches the bottom rather than truncating it.
	for range 100 {
		p.Handle(keys.PageDown)
	}
	last := frame(draw(&p, 60, 20, 0))
	if !strings.Contains(last, "line 39") {
		t.Errorf("the last line is unreachable by scrolling:\n%s", last)
	}
	if !strings.Contains(last, "above") {
		t.Errorf("the hint at the bottom does not point up:\n%s", last)
	}
}

// The overflow label has to fit in the panel it is reporting on. The panel is
// narrowest exactly when it is most likely to overflow, so a label that is
// itself truncated is the realistic failure here, not a hypothetical one.
func TestOverflowLabelFits(t *testing.T) {
	var p Panel
	p.Show(strings.TrimRight(strings.Repeat("x\n", 400), "\n"), 0, 0)
	s := draw(&p, MinWidth, 30, 0)
	if !strings.Contains(frame(s), more(391)) {
		t.Errorf("the label did not fit in a minimum-width panel:\n%s", frame(s))
	}
}

// A long paragraph reflows to the panel width rather than being cut at the
// frame.
func TestLongLinesWrap(t *testing.T) {
	var p Panel
	long := "This is a single very long sentence of documentation that must be " +
		"broken across several rows to be readable at all in a narrow box."
	p.Show(long, 0, 0)
	s := draw(&p, 50, 20, 0)

	body := frame(s)
	if !strings.Contains(body, "This is a single") {
		t.Fatalf("nothing was drawn:\n%s", body)
	}
	if !strings.Contains(body, "readable") {
		t.Errorf("the tail of the sentence was dropped rather than wrapped:\n%s", body)
	}
}

// Wrapping breaks on spaces, so words stay whole.
func TestWrapKeepsWordsWhole(t *testing.T) {
	got := wrapLine(styledLine{{"alpha beta gamma delta", roleText}}, 12)
	joined := ""
	for _, line := range got {
		if lineCols(line) > 12 {
			t.Errorf("line %q is wider than the limit", lineText(line))
		}
		joined += lineText(line) + " "
	}
	for _, word := range []string{"alpha", "beta", "gamma", "delta"} {
		if !strings.Contains(joined, word) {
			t.Errorf("%q was split across rows: %q", word, got)
		}
	}
}

// Wrapped code keeps its indentation on continuation rows, which is most of
// what makes a wrapped signature still readable.
func TestWrapPreservesIndent(t *testing.T) {
	got := wrapLine(styledLine{{"\tfunc VeryLongName(alpha int, beta string) error", roleCode}}, 20)
	if len(got) < 2 {
		t.Fatalf("nothing wrapped: %q", got)
	}
	for _, line := range got[1:] {
		if !strings.HasPrefix(lineText(line), "\t") {
			t.Errorf("continuation row %q lost the indent", lineText(line))
		}
	}
}

// A word longer than the whole width has nowhere to break, and must be emitted
// rather than dropped or looped on forever.
func TestWrapHandlesAnUnbreakableWord(t *testing.T) {
	long := strings.Repeat("x", 100)
	got := wrapLine(styledLine{{long, roleText}}, 10)
	if len(got) == 0 {
		t.Fatal("the line vanished")
	}
	if !strings.HasPrefix(lineText(got[0]), "xxx") {
		t.Errorf("unexpected output %q", lineText(got[0]))
	}
}

// ---------- keys ----------

// Escape closes it, whatever else it is doing.
func TestEscapeCloses(t *testing.T) {
	var p Panel
	p.Show("hello", 0, 0)
	if !p.Handle(keys.Cancel) {
		t.Error("escape was not consumed")
	}
	if p.Open {
		t.Error("escape did not close the panel")
	}
}

// A panel whose answer fits claims no scroll key: unlike the completion popup,
// it can sit on screen while you carry on reading, and stealing arrows from the
// document for nothing would be the worst of both.
func TestClaimsNothingElse(t *testing.T) {
	for _, a := range []keys.Action{
		keys.LineUp, keys.LineDown, keys.CharLeft, keys.CharRight,
		keys.PageUp, keys.PageDown, keys.Confirm, keys.Indent,
	} {
		var p Panel
		p.Show("hello", 0, 0)
		draw(&p, 60, 20, 0) // a fitting panel is not scrollable
		if p.Handle(a) {
			t.Errorf("%s was consumed; it belongs to the document", a)
		}
	}
}

// Once the content overflows, the scroll keys move the view and the panel
// claims them, because there is now something of its own to move.
func TestScrollKeysMoveTheOverflowingView(t *testing.T) {
	var p Panel
	p.Show(strings.TrimRight(strings.Repeat("a row\n", 30), "\n"), 0, 0)
	draw(&p, 60, 20, 0)
	if !p.Scrollable() {
		t.Fatal("setup: the panel does not overflow")
	}
	if p.Top() != 0 {
		t.Fatalf("setup: top = %d", p.Top())
	}
	if !p.Handle(keys.LineDown) {
		t.Fatal("down was not consumed while overflowing")
	}
	if p.Top() != 1 {
		t.Errorf("top = %d after one down, want 1", p.Top())
	}
	if !p.Handle(keys.PageDown) {
		t.Fatal("page down was not consumed")
	}
	if p.Top() <= 1 {
		t.Errorf("page down left the view at %d", p.Top())
	}
	if !p.Handle(keys.PageUp) || p.Top() != 1 {
		t.Errorf("page up did not walk back to 1 (top = %d)", p.Top())
	}
}

// Up at the top and down at the bottom are clamped rather than walking off the
// ends of the content.
func TestScrollIsClamped(t *testing.T) {
	var p Panel
	p.Show(strings.TrimRight(strings.Repeat("a row\n", 30), "\n"), 0, 0)
	draw(&p, 60, 20, 0)
	p.Handle(keys.LineUp)
	if p.Top() != 0 {
		t.Errorf("up at the top moved to %d", p.Top())
	}
	for range 100 {
		p.Handle(keys.LineDown)
	}
	max := len(p.body) - p.shown
	if p.Top() != max {
		t.Errorf("top = %d after scrolling past the end, want %d", p.Top(), max)
	}
}

// A closed panel claims nothing, so escape still reaches whatever is behind it.
func TestClosedPanelClaimsNothing(t *testing.T) {
	var p Panel
	if p.Handle(keys.Cancel) {
		t.Error("a closed panel consumed escape")
	}
}
