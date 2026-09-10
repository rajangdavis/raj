package app

import (
	"strings"
	"testing"

	"raj/internal/piecetable"
)

// The fixture and the expected spans below are one scenario: the hunk replaces
// "world" at byte 6, and every assertion derives from these constants rather
// than restating the numbers.
const (
	reviewFixture = "hello world\n"
	reviewAt      = 6
	reviewOld     = "world"
	reviewNew     = "socket"
)

// propose writes hunks as an agent and marks the group proposed, mirroring
// what the socket host does with an agent apply: the text lands, and the
// decision about it is still outstanding.
func propose(t *testing.T, h *harness, hunks ...piecetable.Hunk) uint64 {
	t.Helper()
	p := h.Pane()
	p.File.Begin()
	p.File.ApplyDiff(piecetable.Agent, p.File.Session().Version(), hunks)
	p.File.End()
	id := p.File.Session().LastGroup()
	p.File.Session().MarkGroup(id, piecetable.Proposed)
	return id
}

// ctrl+alt+a on a proposed change accepts it: the mark clears, the text stays,
// and the status line says what was decided.
func TestAcceptProposedAtCaret(t *testing.T) {
	h := newHarness(t, reviewFixture)
	propose(t, h, piecetable.Hunk{Start: reviewAt, End: reviewAt + len(reviewOld), Text: reviewNew})
	h.Pane().Cursors.Set(reviewAt+1, reviewAt+1) // inside the hunk

	h.press("ctrl+alt+a")
	if got := h.text(); got != "hello socket\n" {
		t.Errorf("text = %q, accepting must not change it", got)
	}
	if marks := h.Pane().PendingMarks(); len(marks) != 0 {
		t.Errorf("marks after accept = %+v, want none", marks)
	}
	if !strings.Contains(h.Status(), "accepted") {
		t.Errorf("status = %q, want the decision reported", h.Status())
	}
}

// ctrl+alt+x backs the change out: the document reads as though it was never
// written, and the pending list is empty.
func TestRejectProposedAtCaret(t *testing.T) {
	h := newHarness(t, reviewFixture)
	propose(t, h, piecetable.Hunk{Start: reviewAt, End: reviewAt + len(reviewOld), Text: reviewNew})
	h.Pane().Cursors.Set(reviewAt+1, reviewAt+1)

	h.press("ctrl+alt+x")
	if got := h.text(); got != reviewFixture {
		t.Errorf("after reject = %q, want the original back", got)
	}
	if marks := h.Pane().PendingMarks(); len(marks) != 0 {
		t.Errorf("marks after reject = %+v, want none", marks)
	}
	if !strings.Contains(h.Status(), "rejected") {
		t.Errorf("status = %q, want the decision reported", h.Status())
	}
}

// With the caret off every change, the chord decides everything on screen
// and says how much that was.
func TestReviewFallsBackToAllVisible(t *testing.T) {
	h := newHarness(t, reviewFixture+"second line\n")
	propose(t, h, piecetable.Hunk{Start: reviewAt, End: reviewAt + len(reviewOld), Text: reviewNew})
	h.Pane().Cursors.Set(len(reviewFixture)+1, len(reviewFixture)+1) // line 1, off the hunk (byte 12 is the newline, which line 0 owns)
	h.drain()                                                        // lay the pane out first: Run draws before the first event, and without a frame the viewport has 0 rows and nothing is visible

	h.press("ctrl+alt+a")
	if marks := h.Pane().PendingMarks(); len(marks) != 0 {
		t.Errorf("marks after the visible accept = %+v, want none", marks)
	}
	if !strings.Contains(h.Status(), "1 of 1") {
		t.Errorf("status = %q, want the count of what was decided", h.Status())
	}
}

// A caret on the line a hunk touches decides that hunk even when it is not
// inside the span: line covering, not byte covering.
func TestCaretOnTheLineIsEnough(t *testing.T) {
	h := newHarness(t, reviewFixture)
	propose(t, h, piecetable.Hunk{Start: reviewAt, End: reviewAt + len(reviewOld), Text: reviewNew})
	h.Pane().Cursors.Set(0, 0) // start of the same line, outside the span

	h.press("ctrl+alt+x")
	if got := h.text(); got != reviewFixture {
		t.Errorf("after reject = %q, want the original back", got)
	}
}

// ctrl+alt+v lists the pending change sets in the picker, and choosing one
// lands the caret on the line the change sits on.
func TestReviewPickerJumpsToTheChange(t *testing.T) {
	h := newHarness(t, reviewFixture)
	propose(t, h, piecetable.Hunk{Start: reviewAt, End: reviewAt + len(reviewOld), Text: reviewNew})

	h.press("ctrl+alt+v")
	if !h.Picker.Open || h.Focused() != FocusPicker {
		t.Fatal("ctrl+alt+v did not open the review picker")
	}
	if h.Picker.Results() != 1 {
		t.Fatalf("picker results = %d, want 1", h.Picker.Results())
	}
	h.press("enter")
	if h.Picker.Open {
		t.Error("choosing should close the picker")
	}
	line, _ := h.Pane().File.LineCol(h.Pane().Cursors.Primary().Head)
	if line != 0 {
		t.Errorf("caret line = %d, want 0, the line the change is on", line)
	}
}

// The gutter carries the review mark: the writer initial in the added colour
// on every line a proposed change touches, gone once the change is decided.
func TestProposalGutterMarks(t *testing.T) {
	h := newHarness(t, reviewFixture)
	propose(t, h, piecetable.Hunk{Start: reviewAt, End: reviewAt + len(reviewOld), Text: reviewNew})
	h.drain()

	l := computeLayout(120, 12, h.App.sidebar, h.App.focus)
	cell := h.host.Last().At(l.EditorX, l.TopY)
	if cell.Style.Fg != h.App.theme.ProposedAdd {
		t.Errorf("gutter mark fg = %v, want the added colour %v", cell.Style.Fg, h.App.theme.ProposedAdd)
	}
	if cell.Rune == ' ' || cell.Rune == 0 {
		t.Errorf("gutter mark rune = %q, want the writer initial", cell.Rune)
	}

	h.Pane().Cursors.Set(reviewAt+1, reviewAt+1)
	h.press("ctrl+alt+a")
	h.drain()
	cell = h.host.Last().At(l.EditorX, l.TopY)
	if cell.Style.Fg == h.App.theme.ProposedAdd {
		t.Error("the review mark survived the decision")
	}
}
