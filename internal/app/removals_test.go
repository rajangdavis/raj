package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"raj/internal/intent"
	"raj/internal/piecetable"
)

// The waiting list: everything an agent has proposed -- change sets, file
// deletions, folder removals and publishes -- is one list on ctrl+alt+v, with a
// count in the status line. It replaces the old ctrl+alt+d removal queue, so
// there is no separate removal surface any more. Reject still only marks a text
// change; clear is the second gesture that purges it.

// Every kind of proposal is one row, with who asked and a size, and the status
// line counts the lot.
func TestWaitingListShowsEveryKind(t *testing.T) {
	t.Parallel()
	// A tall screen so every row draws: the list scrolls on a short one, and
	// this test asserts the labels of all four kinds.
	h := newHarnessSize(t, "hello\n", 120, 24)
	propose(t, h, piecetable.Hunk{Start: 0, End: 5, Text: "hey"})

	path := proposeDeletion(t, h)
	h.press("enter") // Ignore for now: the deletion stays pending
	dir := mkTree(t, h)
	proposeDirRemoval(t, h, dir)
	h.press("enter") // Ignore the rmdir gate
	h.App.pendingPublishes = map[string]intent.Publish{
		"task-1": {Name: "task-1", Author: uint8(piecetable.Agent + 7)},
	}

	h.Draw()
	if got := h.host.Text(); !strings.Contains(got, "4 waiting for you (ctrl+alt+v)") {
		t.Fatalf("status line = %q, want the waiting count:\n%s", got, h.host.Text())
	}

	h.press("ctrl+alt+v")
	if !h.Picker.Open || h.Focused() != FocusPicker {
		t.Fatal("ctrl+alt+v did not open the waiting list")
	}
	if got := h.Picker.Results(); got != 4 {
		t.Fatalf("list rows = %d, want 4", got)
	}
	h.Draw()
	screen := h.host.Text()
	for _, want := range []string{
		"change set", "test.go",
		"delete", filepath.Base(path),
		"remove folder", "2 files",
		"publish", "task-1",
	} {
		if !strings.Contains(screen, want) {
			t.Errorf("waiting list does not carry %q:\n%s", want, screen)
		}
	}

	h.press("esc")
	if h.Picker.Open {
		t.Error("esc did not close the waiting list")
	}
}

// Enter goes to the row: a proposal in a file nobody has open opens it.
func TestWaitingListEnterOpensTheProposalFile(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "hello\n")
	other := filepath.Join(h.primaryRoot(), "other.go")
	if err := os.WriteFile(other, []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := h.App.ProposeDeletion(other, uint8(piecetable.Agent+7)); err != nil {
		t.Fatal(err)
	}

	h.press("ctrl+alt+v")
	if h.Picker.Results() != 1 {
		t.Fatalf("list rows = %d, want 1", h.Picker.Results())
	}
	h.press("enter")
	if h.Picker.Open {
		t.Error("entering a row should close the list")
	}
	if got := h.Pane().File.Path; got != other {
		t.Errorf("active file = %q, want the row path %q", got, other)
	}
}

// Accepting a deletion row carries the removal out, the same answer the gate
// prompt gives, and the row leaves the list.
func TestWaitingListAcceptDeletionRemovesTheFile(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "hello\n")
	path := proposeDeletion(t, h)
	h.press("enter") // Ignore

	h.press("ctrl+alt+v")
	h.press("ctrl+super+m") // accept

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("file still on disk after accepting the row (err=%v)", err)
	}
	if got := h.Deletions(); len(got) != 0 {
		t.Errorf("pending deletions = %+v, want none", got)
	}
	if h.Picker.Open {
		t.Error("a decision should close the waiting list")
	}
}

// Rejecting a deletion row withdraws the proposal without touching disk.
func TestWaitingListRejectDeletionWithdraws(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "hello\n")
	path := proposeDeletion(t, h)
	h.press("enter") // Ignore

	h.press("ctrl+alt+v")
	h.press("ctrl+super+/") // reject

	if _, err := os.Stat(path); err != nil {
		t.Errorf("rejecting the row removed the file: %v", err)
	}
	if got := h.Deletions(); len(got) != 0 {
		t.Errorf("pending deletions = %+v, want the proposal retracted", got)
	}
}

// Reject on a text change stays two-step: it marks the set Rejected and the
// text stays visible, and ctrl+alt+k is the second gesture that purges it. The
// list must not make reject purge.
func TestWaitingListRejectChangeSetStaysTwoStep(t *testing.T) {
	t.Parallel()
	h := newHarness(t, reviewFixture)
	id := propose(t, h, piecetable.Hunk{Start: reviewAt, End: reviewAt + len(reviewOld), Text: reviewNew})

	h.press("ctrl+alt+v")
	if h.Picker.Results() != 1 {
		t.Fatalf("list rows = %d, want 1", h.Picker.Results())
	}
	h.press("ctrl+super+/") // reject from the list

	if got := h.text(); got != "hello socket\n" {
		t.Fatalf("after reject = %q, rejecting must not change the text", got)
	}
	if st := h.Pane().File.Session().GroupState(id); st != piecetable.Rejected {
		t.Errorf("state after reject = %v, want Rejected", st)
	}
	if !strings.Contains(h.Status(), "clears it") {
		t.Errorf("status = %q, want the clear gesture named", h.Status())
	}

	// The second gesture: clear at the caret purges the marked text.
	h.Pane().Cursors.Set(reviewAt+1, reviewAt+1)
	h.press("ctrl+super+k")
	if got := h.text(); got != reviewFixture {
		t.Errorf("after clear = %q, want the rejected text purged", got)
	}
}

// With nothing waiting the surface is invisible: no note, no stray indicator.
func TestNoWaitingProposalsIsInvisible(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "hello\n")
	if got := h.App.waitingNote(); got != "" {
		t.Errorf("note = %q with nothing pending, want empty", got)
	}
	h.Draw()
	if strings.Contains(h.host.Text(), "waiting for you") {
		t.Errorf("a stray indicator with nothing pending:\n%s", h.host.Text())
	}
}
