package app

import (
	"fmt"
	"strings"
	"testing"

	"raj/internal/piecetable"
)

// cmd+r toggles the application between Edit and Review mode.
func TestToggleReviewMode(t *testing.T) {
	h := newHarness(t, reviewFixture)
	if h.mode != ModeEdit {
		t.Fatalf("mode = %v at startup, want Edit", h.mode)
	}
	h.press("super+r")
	if h.mode != ModeReview {
		t.Fatalf("mode = %v after cmd+r, want Review", h.mode)
	}
	h.press("super+r")
	if h.mode != ModeEdit {
		t.Fatalf("mode = %v after a second cmd+r, want Edit", h.mode)
	}
}

// A text mutation in Review mode is refused and the document is untouched.
func TestReviewModeRefusesTyping(t *testing.T) {
	h := newHarness(t, reviewFixture)
	h.press("super+r")
	before := h.text()
	h.typeText("X")
	if got := h.text(); got != before {
		t.Fatalf("text = %q, want the read-only buffer unchanged", got)
	}
	if !strings.Contains(h.Status(), "read-only") {
		t.Errorf("status = %q, want a read-only note", h.Status())
	}
}

// Structural edits are refused too: backspace at the caret must not delete.
func TestReviewModeRefusesBackspace(t *testing.T) {
	h := newHarness(t, reviewFixture)
	p := h.Pane()
	p.Cursors.Set(len("hello"), len("hello"))
	h.press("super+r")
	h.press("backspace")
	if got := h.text(); got != reviewFixture {
		t.Fatalf("text = %q, want backspace refused", got)
	}
}

// Movement and selection stay live in Review mode.
func TestReviewModeKeepsMovement(t *testing.T) {
	h := newHarness(t, "one\ntwo\nthree\n")
	h.press("super+r")
	h.press("down")
	if line := h.Pane().File.LineOf(h.Pane().Cursors.Primary().Head); line != 1 {
		t.Fatalf("caret line = %d after down in Review, want 1", line)
	}
}

// Entering Review jumps to the first pending set; entering with none is an
// allowed read-only browse and says so.
func TestReviewEntersAtTheFirstSet(t *testing.T) {
	h := newHarness(t, "one\ntwo\nthree\n")
	propose(t, h, piecetable.Hunk{Start: 8, End: 13, Text: "tres"})
	propose(t, h, piecetable.Hunk{Start: 0, End: 3, Text: "uno"})
	h.press("super+r")
	if line := h.Pane().File.LineOf(h.Pane().Cursors.Primary().Head); line != 0 {
		t.Fatalf("caret line = %d on entering Review, want the first set at 0", line)
	}
	if !strings.Contains(h.reviewBar(), "1/2") {
		t.Errorf("bar = %q, want progress 1/2", h.reviewBar())
	}
}

func TestReviewEntersWithNoProposals(t *testing.T) {
	h := newHarness(t, reviewFixture)
	h.press("super+r")
	if h.mode != ModeReview {
		t.Fatal("Review mode was refused with nothing pending")
	}
	if !strings.Contains(h.Status(), "no proposed changes") {
		t.Errorf("status = %q, want the no-proposals note", h.Status())
	}
	if !strings.Contains(h.reviewBar(), "no proposed changes") {
		t.Errorf("bar = %q, want the no-proposals note", h.reviewBar())
	}
}

// Accept and reject are decisions, not edits, so they stay live in Review mode.
func TestReviewModeKeepsAccept(t *testing.T) {
	h := newHarness(t, reviewFixture)
	propose(t, h, piecetable.Hunk{Start: reviewAt, End: reviewAt + len(reviewOld), Text: reviewNew})
	h.press("super+r")
	h.press("ctrl+super+m")
	if got := h.text(); got != "hello socket\n" {
		t.Fatalf("text = %q, accepting must keep the text", got)
	}
	if marks := h.Pane().PendingMarks(); len(marks) != 0 {
		t.Fatalf("marks = %d after accepting in Review, want none", len(marks))
	}
}

func TestReviewModeKeepsReject(t *testing.T) {
	h := newHarness(t, reviewFixture)
	propose(t, h, piecetable.Hunk{Start: reviewAt, End: reviewAt + len(reviewOld), Text: reviewNew})
	h.press("super+r")
	h.press("ctrl+super+/")
	if got := h.text(); got != reviewFixture {
		t.Fatalf("text = %q, want the proposal backed out", got)
	}
}

// Reload moved off cmd+r: shift+super+r still reloads in Edit mode.
func TestReloadIsOnShiftSuperR(t *testing.T) {
	h := newHarness(t, "original\n")
	rewriteOnDisk(t, h, "theirs\n")
	h.press("shift+super+r")
	if h.Prompt.Open {
		t.Fatal("reloading a clean buffer asked a question")
	}
	if got := h.text(); got != "theirs\n" {
		t.Errorf("buffer = %q, want the disk version", got)
	}
}

// Reload is refused in Review mode: it would discard the sets under review.
func TestReviewModeRefusesReload(t *testing.T) {
	h := newHarness(t, "original\n")
	rewriteOnDisk(t, h, "theirs\n")
	h.press("super+r")
	h.press("shift+super+r")
	if got := h.text(); got != "original\n" {
		t.Errorf("buffer = %q, want reload refused in Review", got)
	}
	if !strings.Contains(h.Status(), "read-only") {
		t.Errorf("status = %q, want a read-only note", h.Status())
	}
}

// The keybar is derived from the binding table: it names the real chords, not
// the design shorthand letters.
func TestReviewBarNamesTheRealChords(t *testing.T) {
	h := newHarness(t, reviewFixture)
	h.press("super+r")
	bar := h.reviewBar()
	for _, want := range []string{
		"super+r edit",
		"ctrl+super+m accept",
		"ctrl+super+/ reject",
		"ctrl+super+,/ctrl+super+. move",
	} {
		if !strings.Contains(bar, want) {
			t.Errorf("bar = %q, want it to name %q", bar, want)
		}
	}
}

// An additive edit inside a proposed span fragments the member into surviving
// runs, so the set stays pending and reachable rather than becoming a moved
// whole; the bar progress total and next/prev own count still agree.
func TestReviewBarProgressMatchesTheCycle(t *testing.T) {
	h := newHarness(t, "one\ntwo\nthree\n")
	propose(t, h, piecetable.Hunk{Start: 0, End: 3, Text: "uno"})
	propose(t, h, piecetable.Hunk{Start: 8, End: 13, Text: "tres"})
	// Type inside the second set: the runs on either side survive, so it is
	// fragmented, not moved, and both sets remain reachable.
	h.Pane().Cursors.Set(9, 9)
	h.typeText("X")
	if got := len(h.Pane().File.Session().Pending()); got != 2 {
		t.Fatalf("pending = %d, want both sets still pending", got)
	}
	reachable := len(proposalGroups(h.Pane()))
	if reachable != 2 {
		t.Fatalf("reachable = %d, want both fragmented sets reachable", reachable)
	}

	h.press("super+r")
	h.press("ctrl+super+.")
	if got := h.Status(); got != fmt.Sprintf("proposal 2 of %d", reachable) {
		t.Errorf("cycle status = %q, want it to count the %d reachable set(s)", got, reachable)
	}
	bar := h.reviewBar()
	if !strings.Contains(bar, fmt.Sprintf("2/%d", reachable)) {
		t.Errorf("bar = %q, want progress 2/%d matching the cycle", bar, reachable)
	}
	if strings.Contains(bar, "not shown") {
		t.Errorf("bar = %q, want no unplaced-member note when every member survives", bar)
	}
}

// A proposed set the buffer has entirely overwritten is auto-rejected under
// option A: it is not pending, does not block a save, and the bar does not
// claim to have moved past it.
func TestReviewBarAutoRejectedSetDropsOut(t *testing.T) {
	h := newHarness(t, reviewFixture)
	propose(t, h, piecetable.Hunk{Start: reviewAt, End: reviewAt + len(reviewOld), Text: reviewNew})
	// Replace every byte of the inserted text with the user's own.
	h.Pane().Cursors.Set(reviewAt, reviewAt+len(reviewNew))
	h.typeText("port")
	if marks := h.Pane().PendingMarks(); len(marks) != 0 {
		t.Fatalf("marks = %d, want the overwritten set to have no projection", len(marks))
	}
	if got := len(h.Pane().File.Session().Pending()); got != 0 {
		t.Fatalf("pending = %d, want the overwritten set auto-rejected", got)
	}

	h.press("super+r")
	if !strings.Contains(h.Status(), "no proposed changes") {
		t.Errorf("status = %q, want no-proposals once the set is auto-rejected", h.Status())
	}
	bar := h.reviewBar()
	if !strings.Contains(bar, "no proposed changes") {
		t.Errorf("bar = %q, want no-proposals once the set is auto-rejected", bar)
	}
	if strings.Contains(bar, "moved past") || strings.Contains(bar, "not shown") {
		t.Errorf("bar = %q, want no moved wording for an auto-rejected set", bar)
	}
}

// A set with one member overwritten and one surviving stays pending, and the
// bar names the unplaced member rather than dropping the set or calling the
// whole set moved.
func TestReviewBarNamesUnplacedMembers(t *testing.T) {
	h := newHarness(t, "aaa bbb ccc\n")
	propose(t, h,
		piecetable.Hunk{Start: 0, End: 3, Text: "AAA"},
		piecetable.Hunk{Start: 8, End: 11, Text: "CCC"},
	)
	// Overwrite the second member whole; the first survives untouched.
	h.Pane().Cursors.Set(8, 11)
	h.typeText("zzz")
	if got := len(h.Pane().File.Session().Pending()); got != 1 {
		t.Fatalf("pending = %d, want the set kept by its surviving member", got)
	}
	if reachable := len(proposalGroups(h.Pane())); reachable != 1 {
		t.Fatalf("reachable = %d, want the one surviving set", reachable)
	}

	h.press("super+r")
	bar := h.reviewBar()
	if !strings.Contains(bar, "1/1") {
		t.Errorf("bar = %q, want progress 1/1", bar)
	}
	if !strings.Contains(bar, "1 member(s) not shown") {
		t.Errorf("bar = %q, want the unplaced member named", bar)
	}
	if strings.Contains(bar, "set(s) moved past") {
		t.Errorf("bar = %q, want per-member wording, not a whole-set moved count", bar)
	}
}

// The rendered status line carries the badge, not just the app field.
func TestReviewBadgeIsDrawn(t *testing.T) {
	h := newHarness(t, reviewFixture)
	h.press("super+r")
	frame := h.host.Text()
	if !strings.Contains(frame, "Review") {
		t.Errorf("status line does not carry the Review badge:\n%s", frame)
	}
}
