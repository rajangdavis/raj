package app

import (
	"os"
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

// ctrl+super+m on a proposed change accepts it: the mark clears, the text stays,
// and the status line says what was decided.
func TestAcceptProposedAtCaret(t *testing.T) {
	h := newHarness(t, reviewFixture)
	propose(t, h, piecetable.Hunk{Start: reviewAt, End: reviewAt + len(reviewOld), Text: reviewNew})
	h.Pane().Cursors.Set(reviewAt+1, reviewAt+1) // inside the hunk

	h.press("ctrl+super+m")
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

// ctrl+super+/ backs the change out: the document reads as though it was never
// written, and the pending list is empty.
func TestRejectProposedAtCaret(t *testing.T) {
	h := newHarness(t, reviewFixture)
	propose(t, h, piecetable.Hunk{Start: reviewAt, End: reviewAt + len(reviewOld), Text: reviewNew})
	h.Pane().Cursors.Set(reviewAt+1, reviewAt+1)

	h.press("ctrl+super+/")
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

	h.press("ctrl+super+m")
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

	h.press("ctrl+super+/")
	if got := h.text(); got != reviewFixture {
		t.Errorf("after reject = %q, want the original back", got)
	}
}

// NextProposed steps forward through the pending sets in document order and
// wraps from the last back to the first, reporting where it landed.
func TestNextProposedCyclesForwardAndWraps(t *testing.T) {
	h := newHarness(t, "one\ntwo\nthree\n")
	propose(t, h, piecetable.Hunk{Start: 0, End: 3, Text: "uno"})
	propose(t, h, piecetable.Hunk{Start: 8, End: 13, Text: "tres"})
	p := h.Pane()
	p.Cursors.Set(4, 4) // line 1, between the two sets

	h.press("ctrl+super+.")
	if line := p.File.LineOf(p.Cursors.Primary().Head); line != 2 {
		t.Fatalf("caret line = %d after next, want 2", line)
	}
	if got := h.Status(); got != "proposal 2 of 2" {
		t.Errorf("status = %q, want proposal 2 of 2", got)
	}

	h.press("ctrl+super+.")
	if line := p.File.LineOf(p.Cursors.Primary().Head); line != 0 {
		t.Fatalf("caret line = %d after wrapping, want 0", line)
	}
	if got := h.Status(); got != "proposal 1 of 2" {
		t.Errorf("status = %q, want proposal 1 of 2", got)
	}
}

// PrevProposed is the same walk backwards, wrapping from the first to the last.
func TestPrevProposedCyclesBackwardAndWraps(t *testing.T) {
	h := newHarness(t, "one\ntwo\nthree\n")
	propose(t, h, piecetable.Hunk{Start: 0, End: 3, Text: "uno"})
	propose(t, h, piecetable.Hunk{Start: 8, End: 13, Text: "tres"})
	p := h.Pane()
	p.Cursors.Set(4, 4) // line 1, between the two sets

	h.press("ctrl+super+,")
	if line := p.File.LineOf(p.Cursors.Primary().Head); line != 0 {
		t.Fatalf("caret line = %d after prev, want 0", line)
	}
	if got := h.Status(); got != "proposal 1 of 2" {
		t.Errorf("status = %q, want proposal 1 of 2", got)
	}

	h.press("ctrl+super+,")
	if line := p.File.LineOf(p.Cursors.Primary().Head); line != 2 {
		t.Fatalf("caret line = %d after wrapping, want 2", line)
	}
	if got := h.Status(); got != "proposal 2 of 2" {
		t.Errorf("status = %q, want proposal 2 of 2", got)
	}
}

// Stepping from a caret already inside a set moves to the neighbouring set
// rather than staying put.
func TestNextPrevFromInsideASet(t *testing.T) {
	h := newHarness(t, "one\ntwo\nthree\n")
	propose(t, h, piecetable.Hunk{Start: 0, End: 3, Text: "uno"})
	propose(t, h, piecetable.Hunk{Start: 8, End: 13, Text: "tres"})
	p := h.Pane()
	p.Cursors.Set(1, 1) // inside the first set

	h.press("ctrl+super+.")
	if line := p.File.LineOf(p.Cursors.Primary().Head); line != 2 {
		t.Fatalf("caret line = %d after next, want 2", line)
	}
	h.press("ctrl+super+,")
	if line := p.File.LineOf(p.Cursors.Primary().Head); line != 0 {
		t.Fatalf("caret line = %d after prev, want 0", line)
	}
	if got := h.Status(); got != "proposal 1 of 2" {
		t.Errorf("status = %q, want proposal 1 of 2", got)
	}
}

// The walk follows the file, not the journal: sets written bottom-up still
// cycle top-down, so proposal 1 is always the first one in the document.
func TestProposalCycleFollowsDocumentOrder(t *testing.T) {
	h := newHarness(t, "one\ntwo\nthree\n")
	propose(t, h, piecetable.Hunk{Start: 8, End: 13, Text: "tres"})
	propose(t, h, piecetable.Hunk{Start: 0, End: 3, Text: "uno"})
	p := h.Pane()
	p.Cursors.Set(0, 0) // on the first set in the document

	h.press("ctrl+super+.")
	if line := p.File.LineOf(p.Cursors.Primary().Head); line != 2 {
		t.Fatalf("caret line = %d after next, want 2", line)
	}
	if got := h.Status(); got != "proposal 2 of 2" {
		t.Errorf("status = %q, want proposal 2 of 2", got)
	}
}

// With nothing pending the cycle has nowhere to go and says so rather than
// silently doing nothing.
func TestProposalCycleWithoutProposals(t *testing.T) {
	h := newHarness(t, reviewFixture)

	h.press("ctrl+super+.")
	if got := h.Status(); !strings.Contains(got, "no proposed changes") {
		t.Errorf("status = %q, want the no-proposals note", got)
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
	h.press("ctrl+super+m")
	h.drain()
	cell = h.host.Last().At(l.EditorX, l.TopY)
	if cell.Style.Fg == h.App.theme.ProposedAdd {
		t.Error("the review mark survived the decision")
	}
}

// The save gesture no longer accepts pending change sets sight-unseen: with a
// proposal in the buffer, super+s opens a review listing instead of writing.
func TestSaveWithPendingOpensReview(t *testing.T) {
	h := newHarness(t, reviewFixture)
	propose(t, h, piecetable.Hunk{Start: reviewAt, End: reviewAt + len(reviewOld), Text: reviewNew})

	h.press("super+s")

	if !h.Prompt.Open {
		t.Fatal("super+s with a pending set did not open the save review")
	}
	if got := len(h.Pane().File.Session().Pending()); got != 1 {
		t.Errorf("pending = %d while the review is open, want the set still there", got)
	}
	if !strings.Contains(h.host.Text(), "Save with proposed changes") {
		t.Errorf("the review is not on screen:\n%s", h.host.Text())
	}
	// Nothing written yet: the gesture's answer is still open.
	data, err := os.ReadFile(h.Pane().File.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != reviewFixture {
		t.Errorf("on disk = %q before the review was answered", string(data))
	}
}

// Answering Save in the review accepts every pending set and writes, which is
// the deliberate version of what the old all-or-nothing save did.
func TestSaveReviewConfirmAcceptsAllAndSaves(t *testing.T) {
	h := newHarness(t, reviewFixture)
	propose(t, h, piecetable.Hunk{Start: reviewAt, End: reviewAt + len(reviewOld), Text: reviewNew})

	h.press("super+s", "enter")

	if h.Prompt.Open {
		t.Error("the review stayed open after it was answered")
	}
	if got := len(h.Pane().File.Session().Pending()); got != 0 {
		t.Errorf("pending = %d after the review was answered, want none", got)
	}
	data, err := os.ReadFile(h.Pane().File.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello socket\n" {
		t.Errorf("on disk = %q, want the accepted text saved", string(data))
	}
}

// Escape cancels the review: no save, no accept, and the buffer is exactly
// where the user left it.
func TestSaveReviewCancelChangesNothing(t *testing.T) {
	h := newHarness(t, reviewFixture)
	propose(t, h, piecetable.Hunk{Start: reviewAt, End: reviewAt + len(reviewOld), Text: reviewNew})

	h.press("super+s", "esc")

	if h.Prompt.Open {
		t.Error("the review stayed open after escape")
	}
	if got := len(h.Pane().File.Session().Pending()); got != 1 {
		t.Errorf("pending = %d after the review was cancelled, want the set still there", got)
	}
	data, err := os.ReadFile(h.Pane().File.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != reviewFixture {
		t.Errorf("on disk = %q, want the cancelled save to have written nothing", string(data))
	}
}

// Stepping through the listing moves the caret to the set under it, so the
// review is read where each change actually sits.
func TestSaveReviewCyclesThroughTheSets(t *testing.T) {
	h := newHarness(t, "one\ntwo\nthree\n")
	propose(t, h, piecetable.Hunk{Start: 0, End: 3, Text: "uno"})
	propose(t, h, piecetable.Hunk{Start: 8, End: 13, Text: "tres"})
	p := h.Pane()

	h.press("super+s")
	if line := p.File.LineOf(p.Cursors.Primary().Head); line != 0 {
		t.Errorf("caret on line %d with the first set current, want 0", line)
	}
	h.press("down")
	if line := p.File.LineOf(p.Cursors.Primary().Head); line != 2 {
		t.Errorf("caret on line %d with the second set current, want 2", line)
	}
	h.press("up")
	if line := p.File.LineOf(p.Cursors.Primary().Head); line != 0 {
		t.Errorf("caret on line %d after stepping back, want 0", line)
	}
}

// Nothing pending: the gesture stays a plain save, with no review.
func TestSaveWithoutPendingSkipsReview(t *testing.T) {
	h := newHarness(t, reviewFixture)
	h.typeText("x")

	h.press("super+s")

	if h.Prompt.Open {
		t.Error("a save with nothing pending opened the review anyway")
	}
	if !strings.Contains(h.Status(), "saved") {
		t.Errorf("status = %q, want the save to have gone through", h.Status())
	}
}
