package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"raj/internal/piecetable"
)

// The persistent removal surface: a pending deletion or dir-removal that the
// gate asked about once and the user ignored stays visible in the status line,
// and ctrl+alt+d re-raises the decision for the oldest one. Approving and
// withdrawing both go through the gate's own prompt -- the only place a removal
// is carried out or retracted -- so this surface cannot drift from the gate.

// A dismissed deletion keeps its note, and the re-raise chord brings the gate
// back; approving it removes the file through the same answer the prompt uses.
func TestPendingDeletionNoteAndReopenApprove(t *testing.T) {
	t.Setenv("RAJ_TRASH", "")
	h := newHarness(t, "hello\n")
	path := proposeDeletion(t, h)

	h.press("enter") // Ignore for now: the proposal stays pending
	if got := h.pendingRemovalNote(); !strings.Contains(got, "1 pending removal") {
		t.Fatalf("note = %q, want the pending proposal named", got)
	}
	h.Draw()
	if !strings.Contains(h.host.Text(), "1 pending removal") {
		t.Fatalf("the status line does not carry the note:\n%s", h.host.Text())
	}

	h.press("ctrl+alt+d")
	if !h.Prompt.Open {
		t.Fatal("ctrl+alt+d did not re-raise the pending deletion")
	}
	if got := h.Prompt.Title(); got != "Delete file" {
		t.Fatalf("title = %q, want Delete file", got)
	}
	h.press("right", "enter") // Remove forever

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("file still on disk after approving the re-raised gate (err=%v)", err)
	}
	if got := h.Deletions(); len(got) != 0 {
		t.Errorf("pending deletions = %+v, want none", got)
	}
	h.Draw()
	if strings.Contains(h.host.Text(), "pending removal") {
		t.Errorf("the note survived the pending set emptying:\n%s", h.host.Text())
	}
}

// Withdraw is the third answer the gate now offers: it retracts the proposal
// without touching disk, through the same WithdrawDeletion path the wire uses.
func TestPendingDeletionWithdrawRetracts(t *testing.T) {
	h := newHarness(t, "hello\n")
	path := proposeDeletion(t, h)

	h.press("enter")                   // Ignore
	h.press("ctrl+alt+d")              // re-raise
	h.press("right", "right", "enter") // Remove forever -> Withdraw

	if _, err := os.Stat(path); err != nil {
		t.Errorf("withdraw removed the file: %v", err)
	}
	if got := h.Deletions(); len(got) != 0 {
		t.Errorf("pending deletions = %+v, want the proposal retracted", got)
	}
	if got := h.pendingRemovalNote(); got != "" {
		t.Errorf("note = %q after withdrawing, want none", got)
	}
}

// The same surface carries a dir-removal, which has no pane to focus and so
// only ever raised its prompt at proposal time until now.
func TestPendingDirRemovalNoteAndReopenApprove(t *testing.T) {
	t.Setenv("RAJ_TRASH", "")
	h := newHarness(t, "hello\n")
	dir := mkTree(t, h)
	proposeDirRemoval(t, h, dir)

	h.press("enter") // Ignore
	if got := h.pendingRemovalNote(); !strings.Contains(got, "1 pending removal") {
		t.Fatalf("note = %q, want the pending proposal named", got)
	}

	h.press("ctrl+alt+d")
	if !h.Prompt.Open {
		t.Fatal("ctrl+alt+d did not re-raise the pending dir-removal")
	}
	if got := h.Prompt.Title(); got != "Remove directory" {
		t.Fatalf("title = %q, want Remove directory", got)
	}
	h.press("right", "enter") // Remove forever

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("directory still on disk after approving the re-raised gate (err=%v)", err)
	}
	if got := h.DirRemovals(); len(got) != 0 {
		t.Errorf("pending dir-removals = %+v, want none", got)
	}
	if got := h.pendingRemovalNote(); got != "" {
		t.Errorf("note = %q after the directory went, want none", got)
	}
}

// Withdraw retracts a dir-removal too, leaving the subtree on disk.
func TestPendingDirRemovalWithdrawRetracts(t *testing.T) {
	h := newHarness(t, "hello\n")
	dir := mkTree(t, h)
	proposeDirRemoval(t, h, dir)

	h.press("enter")                   // Ignore
	h.press("ctrl+alt+d")              // re-raise
	h.press("right", "right", "enter") // Withdraw

	if _, err := os.Stat(dir); err != nil {
		t.Errorf("withdraw removed the directory: %v", err)
	}
	if got := h.DirRemovals(); len(got) != 0 {
		t.Errorf("pending dir-removals = %+v, want the proposal retracted", got)
	}
	if got := h.pendingRemovalNote(); got != "" {
		t.Errorf("note = %q after withdrawing, want none", got)
	}
}

// The re-raise chord presents the oldest proposal first, across both lists: a
// file deletion recorded before a dir-removal is the one the key raises.
func TestReopenRemovalPresentsTheOldestFirst(t *testing.T) {
	h := newHarness(t, "hello\n")
	other := filepath.Join(h.root, "other.go")
	if err := os.WriteFile(other, []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A deletion for a file nobody has open records without raising.
	if err := h.App.ProposeDeletion(other, uint8(piecetable.Agent+7)); err != nil {
		t.Fatal(err)
	}
	dir := mkTree(t, h)
	proposeDirRemoval(t, h, dir) // raises its own prompt first
	h.press("enter")             // Ignore it

	h.press("ctrl+alt+d")
	if !h.Prompt.Open {
		t.Fatal("no prompt after the re-raise")
	}
	if got := h.Prompt.Title(); got != "Delete file" {
		t.Errorf("title = %q, want the oldest proposal, Delete file", got)
	}
}

// With nothing pending the surface is invisible: no note, no stray indicator.
func TestNoPendingRemovalsIsInvisible(t *testing.T) {
	h := newHarness(t, "hello\n")
	if got := h.pendingRemovalNote(); got != "" {
		t.Errorf("note = %q with nothing pending, want empty", got)
	}
	h.Draw()
	if strings.Contains(h.host.Text(), "pending removal") {
		t.Errorf("a stray indicator with nothing pending:\n%s", h.host.Text())
	}
}
