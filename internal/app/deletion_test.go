package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"raj/internal/control"
	"raj/internal/piecetable"
	"raj/internal/session"
	"raj/internal/ui"
)

// The deletion gate, driven through the same event path a keystroke takes: a
// pending proposal is recorded, the prompt appears when the path is opened or
// focused, and the answer decides whether the file survives.

// proposeDeletion records a pending deletion for the active buffer's path,
// proposed by an agent, and returns the path. The gate raises immediately when
// that path is the one on screen, so a test can either answer here or focus
// away first.
func proposeDeletion(t *testing.T, h *harness) string {
	t.Helper()
	p := h.Pane()
	if p == nil {
		t.Fatal("no active buffer")
	}
	if err := h.App.ProposeDeletion(p.File.Path, uint8(piecetable.Agent+7)); err != nil {
		t.Fatal(err)
	}
	return p.File.Path
}

// A pending deletion for the file already on screen raises the gate at once,
// and the clean buffer is offered both answers.
func TestDeletionProposalForOpenFileRaisesPrompt(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "hello\n")
	proposeDeletion(t, h)
	if !h.Prompt.Open {
		t.Fatal("a deletion proposal for the open file did not raise the gate")
	}
	if got := h.Prompt.Title(); got != "Delete file" {
		t.Errorf("title = %q, want Delete file", got)
	}
	h.drain()
	screen := h.host.Text()
	if !strings.Contains(screen, "proposed deleting test.go") {
		t.Errorf("the prompt does not name the file:\n%s", screen)
	}
	if !strings.Contains(screen, removeForever) {
		t.Errorf("a clean buffer did not offer %q:\n%s", removeForever, screen)
	}
	if !strings.Contains(screen, ignoreForNow) {
		t.Errorf("the prompt does not offer %q:\n%s", ignoreForNow, screen)
	}
}

// A path nobody has open waits for an open: the gate depends on the file being
// shown, not on a timer.
func TestDeletionPromptWaitsForOpen(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "one\n")
	other := filepath.Join(h.primaryRoot(), "other.go")
	if err := os.WriteFile(other, []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := h.App.ProposeDeletion(other, uint8(piecetable.Agent)); err != nil {
		t.Fatal(err)
	}
	if h.Prompt.Open {
		t.Fatal("a proposal for a file nobody has open raised the gate too early")
	}

	h.OpenFile(other)
	if !h.Prompt.Open {
		t.Fatal("opening the path with a pending deletion did not raise the gate")
	}
	h.drain()
	if !strings.Contains(h.host.Text(), "proposed deleting other.go") {
		t.Errorf("the prompt does not name the opened file:\n%s", h.host.Text())
	}
}

// Ignore is the whole answer: the file keeps working and the proposal stays,
// and the gate does not reappear until the focus moves.
func TestIgnoreLeavesFileAndProposal(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "hello\n")
	path := proposeDeletion(t, h)
	if !h.Prompt.Open {
		t.Fatal("setup: no gate")
	}
	if got := h.Prompt.Selected(); got != ignoreForNow {
		t.Errorf("default answer = %q, want %q", got, ignoreForNow)
	}
	h.press("enter")

	if h.Prompt.Open {
		t.Fatal("Ignore left the dialog open")
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("Ignore removed the file: %v", err)
	}
	if got := h.Pane().File.Text(); got != "hello\n" {
		t.Errorf("buffer = %q, want it untouched", got)
	}
	got := h.Deletions()
	if len(got) != 1 || got[0].Path != path {
		t.Errorf("pending deletions after Ignore = %+v, want the proposal kept", got)
	}
	// Same focus: the question does not come straight back.
	h.maybePromptDeletion()
	if h.Prompt.Open {
		t.Error("the gate reappeared without a focus change")
	}
}

// Remove forever moves the file into the workspace trash, drops the buffer and
// clears the proposal.
func TestRemoveForeverUnlinksAndDropsBuffer(t *testing.T) {
	t.Setenv("RAJ_TRASH", "")
	h := newHarness(t, "hello\n")
	path := proposeDeletion(t, h)
	if !h.Prompt.Open {
		t.Fatal("setup: no gate")
	}
	h.press("right") // select Remove forever
	if got := h.Prompt.Selected(); got != removeForever {
		t.Fatalf("selected = %q, want %q", got, removeForever)
	}
	h.press("enter")

	if h.Prompt.Open {
		t.Fatal("the gate stayed open after Remove forever")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("file still on disk after Remove forever (err=%v)", err)
	}
	if got := h.Tabs.Count(); got != 0 {
		t.Errorf("tab count = %d, want the buffer gone", got)
	}
	if got := h.Deletions(); len(got) != 0 {
		t.Errorf("pending deletions after Remove forever = %+v, want none", got)
	}
}

// A dirty buffer offers only Ignore: there is no force path, and the file
// survives even though the prompt was answered.
func TestRemoveForeverRefusedWhenDirty(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "hello\n")
	h.typeText("x")
	path := proposeDeletion(t, h)
	if !h.Prompt.Open {
		t.Fatal("setup: no gate")
	}
	if safe, why := deletionSafe(h.Pane()); safe || why == "" {
		t.Errorf("deletionSafe(dirty) = %v, %q; want a refusal with a reason", safe, why)
	}
	h.drain()
	screen := h.host.Text()
	if strings.Contains(screen, removeForever) {
		t.Errorf("a dirty buffer offered %q:\n%s", removeForever, screen)
	}
	if !strings.Contains(screen, "Unsaved changes") {
		t.Errorf("the prompt does not say why Remove forever is missing:\n%s", screen)
	}
	// Ignore is the default; enter answers it. Withdraw is the other answer
	// a refused removal still offers, but this test only needs the file kept.
	h.press("enter")
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the file was removed despite unsaved changes: %v", err)
	}
	if got := h.Deletions(); len(got) != 1 {
		t.Errorf("pending deletions = %+v, want the proposal kept", got)
	}
}

// A buffer with a change set still awaiting a decision is not removable
// either; the listing names the set rather than only counting it.
func TestRemoveForeverRefusedWithPendingSet(t *testing.T) {
	t.Parallel()
	h := newHarness(t, reviewFixture)
	propose(t, h, piecetable.Hunk{Start: reviewAt, End: reviewAt + len(reviewOld), Text: reviewNew})
	path := proposeDeletion(t, h)
	if !h.Prompt.Open {
		t.Fatal("setup: no gate")
	}
	if safe, why := deletionSafe(h.Pane()); safe || why == "" {
		t.Errorf("deletionSafe(pending) = %v, %q; want a refusal", safe, why)
	}
	h.drain()
	screen := h.host.Text()
	if strings.Contains(screen, removeForever) {
		t.Errorf("a buffer with a pending set offered %q:\n%s", removeForever, screen)
	}
	if !strings.Contains(screen, "pending set") {
		t.Errorf("the prompt does not name the pending set:\n%s", screen)
	}
	h.press("enter") // Ignore
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the file was removed despite a pending set: %v", err)
	}
}

// The gate returns when the path is focused again after the focus moved away.
func TestDeletionPromptReturnsOnNextFocus(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "one\n")
	other := filepath.Join(h.primaryRoot(), "other.go")
	if err := os.WriteFile(other, []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.OpenFile(other) // test.go is now a background tab
	target := h.Tabs.All()[0]
	if target.File.Path == other {
		target = h.Tabs.All()[1]
	}
	if err := h.App.ProposeDeletion(target.File.Path, uint8(piecetable.Agent)); err != nil {
		t.Fatal(err)
	}
	if h.Prompt.Open {
		t.Fatal("a proposal for a background tab raised the gate over another file")
	}

	// Focus the path; the gate is evaluated after the event, the same way a
	// tab click or chord would reach it.
	h.Tabs.Focus(target)
	h.Handle(ui.Tick{})
	if !h.Prompt.Open {
		t.Fatal("focusing the path with a pending deletion did not raise the gate")
	}
	h.press("enter") // Ignore for now

	// Focus away and back: the question returns.
	for _, p := range h.Tabs.All() {
		if p != target {
			h.Tabs.Focus(p)
		}
	}
	h.Handle(ui.Tick{})
	h.Tabs.Focus(target)
	h.Handle(ui.Tick{})
	if !h.Prompt.Open {
		t.Error("the gate did not return on the next focus")
	}
}

// The safety predicate on a genuinely clean buffer is what makes the removal
// path reachable at all.
func TestDeletionSafeOnCleanBuffer(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "hello\n")
	if safe, why := deletionSafe(h.Pane()); !safe || why != "" {
		t.Errorf("deletionSafe(clean) = %v, %q; want true and no reason", safe, why)
	}
}

// A removal parks the bytes in the workspace trash under a timestamped name
// that keeps the original basename. It still drops the buffer and clears the
// proposal.
func TestRemoveForeverTrashesWhenEnabled(t *testing.T) {
	t.Setenv("RAJ_TRASH", "1")
	h := newHarness(t, "hello\n")
	path := proposeDeletion(t, h)
	if !h.Prompt.Open {
		t.Fatal("setup: no gate")
	}
	h.press("right") // select Remove forever
	if got := h.Prompt.Selected(); got != removeForever {
		t.Fatalf("selected = %q, want %q", got, removeForever)
	}
	h.press("enter")

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("file still at the original path after a trashing removal (err=%v)", err)
	}
	trash := filepath.Join(session.StateDir(h.primaryRoot()), "trash")
	entries, err := os.ReadDir(trash)
	if err != nil {
		t.Fatalf("reading trash dir %s: %v", trash, err)
	}
	if len(entries) != 1 {
		t.Fatalf("trash holds %d file(s), want exactly 1: %+v", len(entries), entries)
	}
	data, err := os.ReadFile(filepath.Join(trash, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != "hello\n" {
		t.Errorf("trashed content = %q, want %q", got, "hello\n")
	}
	if got := h.Tabs.Count(); got != 0 {
		t.Errorf("tab count = %d, want the buffer gone", got)
	}
	if got := h.Deletions(); len(got) != 0 {
		t.Errorf("pending deletions = %+v, want none", got)
	}
}

// A removal always parks the bytes in the workspace trash; RAJ_TRASH no longer
// decides. D-G1 makes the trash the safety net a delete relies on and the
// restore chord its undo, so no value of the variable produces a bare unlink.
func TestRemovalTrashesRegardlessOfEnv(t *testing.T) {
	for _, val := range []string{"", "0", "true", "1"} {
		t.Run("RAJ_TRASH="+val, func(t *testing.T) {
			t.Setenv("RAJ_TRASH", val)
			h := newHarness(t, "hello\n")
			path := proposeDeletion(t, h)
			if !h.Prompt.Open {
				t.Fatal("setup: no gate")
			}
			h.press("right", "enter")

			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Errorf("file still on disk after Remove forever (err=%v)", err)
			}
			trash := filepath.Join(session.StateDir(h.primaryRoot()), "trash")
			entries, err := os.ReadDir(trash)
			if err != nil {
				t.Fatalf("reading trash dir %s: %v", trash, err)
			}
			if len(entries) != 1 {
				t.Fatalf("trash holds %d entr(ies) for RAJ_TRASH=%q, want 1: %+v", len(entries), val, entries)
			}
		})
	}
}

// moveOrCopy is the cross-filesystem fallback the trash needs now that it lives
// under $XDG_STATE_HOME. The destination's parent does not exist here, so the
// rename fails and the helper must create the directory, copy the bytes and the
// mode, then remove the source.
func TestMoveOrCopyFallsBackToCopy(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")
	if err := os.WriteFile(src, []byte("payload"), 0o640); err != nil {
		t.Fatal(err)
	}
	// WriteFile's mode is filtered through the umask; set it explicitly so the
	// assertion does not depend on the test machine's.
	if err := os.Chmod(src, 0o640); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "nested", "deep", "dst.txt")
	if err := moveOrCopy(src, dst); err != nil {
		t.Fatalf("moveOrCopy: %v", err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Errorf("source survived the move: %v", err)
	}
	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}
	if string(data) != "payload" {
		t.Errorf("destination = %q, want payload", data)
	}
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0o640); got != want {
		t.Errorf("mode = %o, want %o", got, want)
	}
}

// A move that cannot complete leaves the source exactly where it was, so the
// caller keeping the file on error never loses bytes.
func TestMoveOrCopyKeepsSourceOnFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")
	if err := os.WriteFile(src, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A regular file where the destination's parent needs to be makes the copy
	// fail after the rename already has.
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := moveOrCopy(src, filepath.Join(blocker, "dst.txt")); err == nil {
		t.Fatal("moveOrCopy succeeded despite an unusable destination")
	}
	if b, err := os.ReadFile(src); err != nil || string(b) != "payload" {
		t.Errorf("source lost after a failed move: %q, %v", b, err)
	}
}

// The control approve is the human answer carried over the socket. It takes the
// same removal path as the prompt Remove forever, so the file leaves disk, the
// pending entry clears and the buffer closes.
func TestApproveDeletionRemovesTheFile(t *testing.T) {
	t.Setenv("RAJ_TRASH", "")
	h := newHarness(t, "hello\n")
	path := proposeDeletion(t, h)
	if err := h.App.ApproveDeletion(path); err != nil {
		t.Fatalf("ApproveDeletion: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("file still on disk after approve (err=%v)", err)
	}
	if got := h.Tabs.Count(); got != 0 {
		t.Errorf("tab count = %d, want the buffer gone", got)
	}
	if got := h.Deletions(); len(got) != 0 {
		t.Errorf("pending deletions after approve = %+v, want none", got)
	}
}

// Approving a path with no pending proposal is refused by name, and nothing is
// removed.
func TestApproveDeletionRefusesANotPendingPath(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "hello\n")
	path := h.Pane().File.Path
	err := h.App.ApproveDeletion(path)
	if err == nil || !strings.Contains(err.Error(), "no pending deletion") {
		t.Fatalf("approve of a not-pending path = %v, want a naming refusal", err)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Errorf("the not-pending file was removed: %v", statErr)
	}
}

// An agent may withdraw only its own pending deletion: another agent's proposal
// survives the attempt. Precondition: a deletion proposed by one agent, a
// withdraw naming a different agent. The human branch must not widen the gate
// for agents, so a peer is still refused.
func TestWithdrawDeletionRefusesAPeerAgent(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "hello\n")
	path := proposeDeletion(t, h)

	peer := uint8(piecetable.Agent + 1)
	err := h.App.WithdrawDeletion(path, peer)
	if err == nil || !strings.Contains(err.Error(), "not this writer") {
		t.Fatalf("an agent withdrew a peer's deletion = %v, want the owner refusal", err)
	}
	if got := h.Deletions(); len(got) != 1 || got[0].Path != path {
		t.Errorf("pending deletions = %+v, want the proposal kept", got)
	}
}

// The proposer withdraws its own deletion: the entry clears and the file stays.
// Precondition: one agent's proposal, withdrawn by that same agent. This is the
// agent half of the rule; TestWithdrawDeletionAllowsAJoinedHuman is the human
// half.
func TestWithdrawDeletionByItsAgentClears(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "hello\n")
	path := proposeDeletion(t, h)

	owner := uint8(piecetable.Agent + 7)
	if err := h.App.WithdrawDeletion(path, owner); err != nil {
		t.Fatalf("WithdrawDeletion(owner): %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("withdraw removed the file: %v", err)
	}
	if got := h.Deletions(); len(got) != 0 {
		t.Errorf("pending deletions = %+v, want none", got)
	}
}

// A durable joined human (an attached client) may withdraw any pending removal,
// including an agent's. Precondition: a seeded Registry with a KindHuman row
// attached to the app's guard, and a deletion proposed by an agent. Without the
// human branch the owner check compares the human's id with the agent's and the
// proposal survives; the file must stay either way.
func TestWithdrawDeletionAllowsAJoinedHuman(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "hello\n")
	path := proposeDeletion(t, h)

	reg := control.NewRegistry()
	human, err := reg.Join("client:desk", "desk", control.KindHuman)
	if err != nil {
		t.Fatal(err)
	}
	if human == control.LocalHuman {
		t.Fatalf("fixture: the joined human reused the local row %d", control.LocalHuman)
	}
	h.App.guard = control.NewGuard(hostOf(h.App))
	h.App.guard.Participants = reg

	if err := h.App.WithdrawDeletion(path, human); err != nil {
		t.Fatalf("a joined human's withdraw was refused: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("withdraw removed the file: %v", err)
	}
	if got := h.Deletions(); len(got) != 0 {
		t.Errorf("pending deletions = %+v, want none", got)
	}
}
