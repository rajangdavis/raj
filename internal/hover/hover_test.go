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
	for y := 0; y < rows; y++ {
		b.WriteString(s.Row(y))
		b.WriteByte('\n')
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

// gopls wraps its answer in markdown fences, which mean "this is code" to a
// renderer and read as three stray backticks in a terminal box.
func TestFencesAreStripped(t *testing.T) {
	var p Panel
	p.Show("```go\nfunc F(x int) error\n```\n\nDoes a thing.", 0, 0)
	if strings.Contains(p.Text(), "```") {
		t.Errorf("text = %q; a fence survived", p.Text())
	}
	if !strings.Contains(p.Text(), "func F(x int) error") {
		t.Errorf("text = %q; the signature was lost with the fence", p.Text())
	}
}

// What the fences contained is code, and code is the part whose spacing
// matters. Stripping the fence must not touch the indentation inside it.
func TestIndentationInsideFencesSurvives(t *testing.T) {
	var p Panel
	p.Show("```go\ntype T struct {\n\tField int\n}\n```", 0, 0)
	if !strings.Contains(p.Text(), "\tField int") {
		t.Errorf("text = %q; the indentation was stripped", p.Text())
	}
}

// Removing a fence leaves a blank line where the fence was, next to the blank
// line that was already there. Vertical space is what the panel has least of.
func TestBlankRunsCollapse(t *testing.T) {
	var p Panel
	p.Show("a\n\n\n\n\nb", 0, 0)
	if strings.Contains(p.Text(), "\n\n\n") {
		t.Errorf("text = %q; a run of blank lines survived", p.Text())
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

// Content longer than the panel says so, rather than ending mid-sentence and
// looking like that was all the server had.
func TestOverlongContentSaysItIsTruncated(t *testing.T) {
	var p Panel
	p.Show(strings.TrimRight(strings.Repeat("line\n", 40), "\n"), 0, 0)
	s := draw(&p, 60, 30, 0)

	if !strings.Contains(frame(s), "more") {
		t.Errorf("no indication that content was cut:\n%s", frame(s))
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
	got := wrap([]string{"alpha beta gamma delta"}, 12)
	for _, line := range got {
		if cols(line) > 12 {
			t.Errorf("line %q is wider than the limit", line)
		}
	}
	joined := strings.Join(got, " ")
	for _, word := range []string{"alpha", "beta", "gamma", "delta"} {
		if !strings.Contains(joined, word) {
			t.Errorf("%q was split across rows: %q", word, got)
		}
	}
}

// Wrapped code keeps its indentation on continuation rows, which is most of
// what makes a wrapped signature still readable.
func TestWrapPreservesIndent(t *testing.T) {
	got := wrap([]string{"\tfunc VeryLongName(alpha int, beta string) error"}, 20)
	if len(got) < 2 {
		t.Fatalf("nothing wrapped: %q", got)
	}
	for _, line := range got[1:] {
		if !strings.HasPrefix(line, "\t") {
			t.Errorf("continuation row %q lost the indent", line)
		}
	}
}

// A word longer than the whole width has nowhere to break, and must be emitted
// rather than dropped or looped on forever.
func TestWrapHandlesAnUnbreakableWord(t *testing.T) {
	long := strings.Repeat("x", 100)
	got := wrap([]string{long}, 10)
	if len(got) == 0 {
		t.Fatal("the line vanished")
	}
	if !strings.HasPrefix(got[0], "xxx") {
		t.Errorf("unexpected output %q", got)
	}
}

// ---------- keys ----------

// Escape closes it. That is the only key it claims: unlike the completion
// popup, this one can sit on screen while you carry on reading, so claiming
// arrows would steal navigation from the document underneath.
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

func TestClaimsNothingElse(t *testing.T) {
	for _, a := range []keys.Action{
		keys.LineUp, keys.LineDown, keys.CharLeft, keys.CharRight,
		keys.Confirm, keys.Indent,
	} {
		var p Panel
		p.Show("hello", 0, 0)
		if p.Handle(a) {
			t.Errorf("%s was consumed; it belongs to the document", a)
		}
	}
}

// A closed panel claims nothing, so escape still reaches whatever is behind it.
func TestClosedPanelClaimsNothing(t *testing.T) {
	var p Panel
	if p.Handle(keys.Cancel) {
		t.Error("a closed panel consumed escape")
	}
}
