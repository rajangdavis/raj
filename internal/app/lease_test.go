package app

import (
	"fmt"
	"strings"
	"testing"

	"raj/internal/piecetable"
)

// A pending change set is a read-only lease: typing inside it changes nothing
// and the status line names the set that has to be decided first.
func TestLeaseRefusesTyping(t *testing.T) {
	h := newHarness(t, reviewFixture)
	id := propose(t, h, piecetable.Hunk{Start: reviewAt, End: reviewAt + len(reviewOld), Text: reviewNew})
	p := h.Pane()
	p.Cursors.Set(reviewAt+1, reviewAt+1) // inside the inserted "socket"

	h.typeText("X")

	if got := h.text(); got != "hello socket\n" {
		t.Fatalf("text = %q, want the leased text unchanged", got)
	}
	if !strings.Contains(h.Status(), "read-only") ||
		!strings.Contains(h.Status(), fmt.Sprintf("change set %d", id)) {
		t.Errorf("status = %q, want a lease refusal naming set %d", h.Status(), id)
	}
}

// Backspace reaching into a lease is refused too, so a deletion cannot take
// leased bytes even when the caret itself sits outside the run.
func TestLeaseRefusesBackspaceIntoTheRun(t *testing.T) {
	h := newHarness(t, reviewFixture)
	propose(t, h, piecetable.Hunk{Start: reviewAt, End: reviewAt + len(reviewOld), Text: reviewNew})
	p := h.Pane()
	p.Cursors.Set(reviewAt+1, reviewAt+1)

	h.press("backspace")

	if got := h.text(); got != "hello socket\n" {
		t.Fatalf("text = %q, want backspace refused", got)
	}
	if !strings.Contains(h.Status(), "read-only") {
		t.Errorf("status = %q, want the lease refusal", h.Status())
	}
}

// An insertion flush with a run's first byte is before the run, so it is not
// refused; the boundary belongs to neither side.
func TestLeaseAllowsABoundaryInsert(t *testing.T) {
	h := newHarness(t, reviewFixture)
	propose(t, h, piecetable.Hunk{Start: reviewAt, End: reviewAt + len(reviewOld), Text: reviewNew})
	p := h.Pane()
	p.Cursors.Set(reviewAt, reviewAt) // flush with the run start

	h.typeText(">")

	if got := h.text(); got != "hello >socket\n" {
		t.Fatalf("text = %q, want the boundary insert to land", got)
	}
}

// Accepting the set ends the lease: the bytes become ordinary text and typing
// at the same caret now lands.
func TestLeaseEndsWhenTheSetIsAccepted(t *testing.T) {
	h := newHarness(t, reviewFixture)
	id := propose(t, h, piecetable.Hunk{Start: reviewAt, End: reviewAt + len(reviewOld), Text: reviewNew})
	p := h.Pane()
	p.Cursors.Set(reviewAt+1, reviewAt+1)

	h.typeText("X") // refused
	h.Pane().File.AcceptGroup(id)
	h.typeText("Y")

	if got := h.text(); got != "hello sYocket\n" {
		t.Fatalf("text = %q, want typing to land once the lease is accepted", got)
	}
}
