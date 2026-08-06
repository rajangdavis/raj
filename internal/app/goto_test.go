package app

import (
	"strings"
	"testing"
)

func doc(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		b.WriteString("line ")
		b.WriteString(itoa(i))
		b.WriteByte('\n')
	}
	return b.String()
}

// line reports the active cursor's 1-based line.
func cursorLine(h *harness) int {
	l, _ := h.Pane().File.LineCol(h.Pane().Cursors.Primary().Head)
	return l + 1
}

func cursorCol(h *harness) int {
	_, c := h.Pane().File.LineCol(h.Pane().Cursors.Primary().Head)
	return c + 1
}

func TestGotoLineJumps(t *testing.T) {
	h := newHarness(t, doc(200))
	h.press("ctrl+g")
	if !h.Prompt.Open {
		t.Fatal("ctrl+g did not open a dialog")
	}
	h.press("super+a")
	h.typeText("120")
	h.press("enter")

	if got := cursorLine(h); got != 120 {
		t.Errorf("cursor on line %d, want 120", got)
	}
	if h.Prompt.Open {
		t.Error("dialog still open")
	}
}

// The field is seeded with where the cursor already is, so the dialog says
// where you are as well as asking where to go.
func TestGotoLineSeedsTheCurrentLine(t *testing.T) {
	h := newHarness(t, doc(50))
	h.press("ctrl+g")
	h.press("super+a")
	h.typeText("30")
	h.press("enter")
	h.press("ctrl+g")

	if got := h.Prompt.Text(); got != "30" {
		t.Errorf("field seeded with %q, want 30", got)
	}
}

// A compiler prints line:column, and pasting one in should not need editing.
func TestGotoLineAcceptsLineColon(t *testing.T) {
	h := newHarness(t, doc(40))
	h.press("ctrl+g")
	h.press("super+a")
	h.typeText("12:4")
	h.press("enter")

	if got := cursorLine(h); got != 12 {
		t.Errorf("line = %d, want 12", got)
	}
	if got := cursorCol(h); got != 4 {
		t.Errorf("column = %d, want 4", got)
	}
}

// Past the end means the end. Refusing would be pedantry.
func TestGotoLineClampsPastTheEnd(t *testing.T) {
	h := newHarness(t, doc(20))
	lines := h.Pane().File.Lines()
	h.press("ctrl+g")
	h.press("super+a")
	h.typeText("9999")
	h.press("enter")

	if got := cursorLine(h); got != lines {
		t.Errorf("line = %d, want the last line %d", got, lines)
	}
	if h.Status() != "" && strings.Contains(h.Status(), "not a line") {
		t.Errorf("clamping reported an error: %q", h.Status())
	}
}

func TestGotoLineRejectsNonsense(t *testing.T) {
	for _, in := range []string{"abc", "-3", "1.5", "12x"} {
		h := newHarness(t, doc(30))
		h.press("ctrl+g")
		h.press("super+a")
		h.typeText(in)
		h.press("enter")

		if got := cursorLine(h); got != 1 {
			t.Errorf("%q moved the cursor to line %d", in, got)
		}
		if !strings.Contains(h.Status(), "not a line number") {
			t.Errorf("%q gave status %q", in, h.Status())
		}
	}
}

// A bare ":col" is a column on the line already showing, which is what the
// seeded field makes the natural thing to type.
func TestGotoColumnOnTheCurrentLine(t *testing.T) {
	h := newHarness(t, doc(40))
	h.press("ctrl+g")
	h.press("super+a")
	h.typeText("15")
	h.press("enter")

	h.press("ctrl+g")
	h.press("super+a")
	h.typeText(":3")
	h.press("enter")

	if got := cursorLine(h); got != 15 {
		t.Errorf("line = %d, want to have stayed on 15", got)
	}
	if got := cursorCol(h); got != 3 {
		t.Errorf("column = %d, want 3", got)
	}
}

func TestGotoLineCancelLeavesTheCursor(t *testing.T) {
	h := newHarness(t, doc(60))
	h.press("ctrl+g")
	h.press("super+a")
	h.typeText("45")
	h.press("esc")

	if got := cursorLine(h); got != 1 {
		t.Errorf("cancelling still jumped, to line %d", got)
	}
	if h.Prompt.Open {
		t.Error("escape did not dismiss the dialog")
	}
}

func TestParsePosition(t *testing.T) {
	for _, tc := range []struct {
		in        string
		line, col int
		ok        bool
	}{
		{"12", 12, 0, true},
		{"12:4", 12, 4, true},
		{":9", 0, 9, true},
		{" 7 ", 7, 0, true},
		{"", 0, 0, false},
		{"0", 0, 0, false},
		{"abc", 0, 0, false},
		{"-3", 0, 0, false},
		{"1.5", 0, 0, false},
		{"12:x", 0, 0, false},
		// A bad line with a good column must still be refused. Without an
		// explicit check the line silently reads as zero and the whole thing
		// looks like a valid bare ":col" for the current line.
		{"12x:5", 0, 0, false},
		{"1:2:3", 0, 0, false},
	} {
		line, col, ok := parsePosition(tc.in)
		if ok != tc.ok || (ok && (line != tc.line || col != tc.col)) {
			t.Errorf("parsePosition(%q) = %d,%d,%v; want %d,%d,%v",
				tc.in, line, col, ok, tc.line, tc.col, tc.ok)
		}
	}
}
