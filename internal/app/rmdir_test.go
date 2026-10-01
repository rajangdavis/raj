package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"raj/internal/control"
	"raj/internal/piecetable"
	"raj/internal/session"
)

// The dir-removal gate, driven through the same event path a keystroke takes:
// a pending dir-removal is recorded, the review appears immediately (a
// directory has no pane to focus), and the answer decides whether the subtree
// survives.

// proposeDirRemoval records a pending dir-removal for a directory, proposed by
// an agent.
func proposeDirRemoval(t *testing.T, h *harness, dir string) {
	t.Helper()
	if err := h.App.ProposeDirRemoval(dir, uint8(piecetable.Agent+7)); err != nil {
		t.Fatal(err)
	}
}

// mkTree makes a directory holding a file and a nested file, so the review has
// a real subtree to list. It returns the directory path.
func mkTree(t *testing.T, h *harness) string {
	t.Helper()
	dir := filepath.Join(h.primaryRoot(), "pkg")
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package pkg\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "b.go"), []byte("package pkg\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// A pending dir-removal raises the review at once, listing the subtree paths
// and offering both answers.
func TestDirRemovalProposalRaisesReview(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "hello\n")
	dir := mkTree(t, h)
	proposeDirRemoval(t, h, dir)
	if !h.Prompt.Open {
		t.Fatal("a dir-removal proposal did not raise the review")
	}
	if got := h.Prompt.Title(); got != "Remove directory" {
		t.Errorf("title = %q, want Remove directory", got)
	}
	h.drain()
	screen := h.host.Text()
	if !strings.Contains(screen, "proposed removing pkg") {
		t.Errorf("the review does not name the directory:\n%s", screen)
	}
	// The rows are absolute paths, so a long one wraps and the exact tail may
	// land on either side of a wrap. What the screen must show is the review
	// listing more than one path and offering both answers; TestDirRemovalPaths
	// asserts the paths themselves exactly.
	if got := strings.Count(screen, "pkg"); got < 1 {
		t.Errorf("the review does not name the directory's subtree:\n%s", screen)
	}
	if !strings.Contains(screen, removeForever) {
		t.Errorf("the review does not offer %q:\n%s", removeForever, screen)
	}
	if !strings.Contains(screen, ignoreForNow) {
		t.Errorf("the review does not offer %q:\n%s", ignoreForNow, screen)
	}
}

// dirRemovalPaths names every path under the directory in stable order, as
// absolute paths. The rows used to be relative to the directory to survive the
// old one-truncated-line prompt; the listing wraps now, so the workaround is
// gone and a row is openable as it stands.
func TestDirRemovalPathsAreAbsoluteAndSorted(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "hello\n")
	dir := mkTree(t, h)

	got := dirRemovalPaths(dir)
	want := []string{
		filepath.Join(dir, "a.go"),
		filepath.Join(dir, "sub"),
		filepath.Join(dir, "sub", "b.go"),
	}
	if len(got) != len(want) {
		t.Fatalf("dirRemovalPaths(%s) = %v, want %v", dir, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// Remove forever removes the whole directory, drops the buffers open under it
// and clears the proposal.
func TestRemoveForeverRemovesDirAndClosesBuffers(t *testing.T) {
	t.Setenv("RAJ_TRASH", "")
	h := newHarness(t, "hello\n")
	dir := mkTree(t, h)
	h.OpenFile(filepath.Join(dir, "a.go")) // a buffer under the directory
	proposeDirRemoval(t, h, dir)
	if !h.Prompt.Open {
		t.Fatal("setup: no review")
	}
	h.press("right") // select Remove forever
	if got := h.Prompt.Selected(); got != removeForever {
		t.Fatalf("selected = %q, want %q", got, removeForever)
	}
	h.press("enter")

	if h.Prompt.Open {
		t.Fatal("the review stayed open after Remove forever")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("directory still on disk after Remove forever (err=%v)", err)
	}
	if got := h.Tabs.Count(); got != 1 {
		t.Errorf("tab count = %d, want only the original test.go left", got)
	}
	if got := h.DirRemovals(); len(got) != 0 {
		t.Errorf("pending dir-removals after Remove forever = %+v, want none", got)
	}
}

// A removal moves the whole directory into the workspace trash under a
// timestamped name, recoverable as a unit.
func TestRemoveForeverTrashesDir(t *testing.T) {
	t.Setenv("RAJ_TRASH", "1")
	h := newHarness(t, "hello\n")
	dir := mkTree(t, h)
	proposeDirRemoval(t, h, dir)
	if !h.Prompt.Open {
		t.Fatal("setup: no review")
	}
	h.press("right", "enter")

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("directory still at the original path after a trashing removal (err=%v)", err)
	}
	trash := filepath.Join(session.StateDir(h.primaryRoot()), "trash")
	entries, err := os.ReadDir(trash)
	if err != nil {
		t.Fatalf("reading trash dir %s: %v", trash, err)
	}
	if len(entries) != 1 {
		t.Fatalf("trash holds %d entry(s), want exactly 1: %+v", len(entries), entries)
	}
	if _, err := os.Stat(filepath.Join(trash, entries[0].Name(), "a.go")); err != nil {
		t.Errorf("the moved directory lost a.go: %v", err)
	}
}

// A dirty buffer under the directory offers only Ignore: there is no force
// path, and the subtree survives even though the prompt was answered.
func TestRemoveForeverRefusedWhenSubtreeDirty(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "hello\n")
	dir := mkTree(t, h)
	h.OpenFile(filepath.Join(dir, "a.go"))
	h.typeText("x") // a.go is active; make it dirty
	proposeDirRemoval(t, h, dir)
	if !h.Prompt.Open {
		t.Fatal("setup: no review")
	}
	if safe, why := h.dirRemovalSafe(dir); safe || why == "" {
		t.Errorf("dirRemovalSafe(dirty subtree) = %v, %q; want a refusal with a reason", safe, why)
	}
	h.drain()
	screen := h.host.Text()
	if strings.Contains(screen, removeForever) {
		t.Errorf("a dirty subtree offered %q:\n%s", removeForever, screen)
	}
	if !strings.Contains(screen, "Unsaved changes") {
		t.Errorf("the prompt does not say why Remove forever is missing:\n%s", screen)
	}
	// With only Ignore offered, enter answers it.
	h.press("enter")
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("the directory was removed despite unsaved changes: %v", err)
	}
	if got := h.DirRemovals(); len(got) != 1 {
		t.Errorf("pending dir-removals = %+v, want the proposal kept", got)
	}
}

// A buffer holding a change set still awaiting a decision offers only Ignore
// too; there is no force path, and the prompt names the set.
func TestRemoveForeverRefusedWithPendingSetUnder(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "hello\n")
	dir := mkTree(t, h)
	h.OpenFile(filepath.Join(dir, "a.go"))
	// a.go is "package pkg\n"; replace "pkg" with "zzz" as a proposed set.
	propose(t, h, piecetable.Hunk{Start: 8, End: 11, Text: "zzz"})
	proposeDirRemoval(t, h, dir)
	if !h.Prompt.Open {
		t.Fatal("setup: no review")
	}
	h.drain()
	screen := h.host.Text()
	if strings.Contains(screen, removeForever) {
		t.Errorf("a subtree with a pending set offered %q:\n%s", removeForever, screen)
	}
	if !strings.Contains(screen, "pending set") {
		t.Errorf("the prompt does not name the pending set:\n%s", screen)
	}
	h.press("enter") // Ignore
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("the directory was removed despite a pending set: %v", err)
	}
	if got := h.DirRemovals(); len(got) != 1 {
		t.Errorf("pending dir-removals = %+v, want the proposal kept", got)
	}
}

// Ignore for now is the whole answer: the directory keeps working and the
// proposal stays pending.
func TestIgnoreDirRemovalKeepsProposal(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "hello\n")
	dir := mkTree(t, h)
	proposeDirRemoval(t, h, dir)
	if !h.Prompt.Open {
		t.Fatal("setup: no review")
	}
	if got := h.Prompt.Selected(); got != ignoreForNow {
		t.Errorf("default answer = %q, want %q", got, ignoreForNow)
	}
	h.press("enter")

	if h.Prompt.Open {
		t.Fatal("Ignore left the review open")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("Ignore removed the directory: %v", err)
	}
	if got := h.DirRemovals(); len(got) != 1 || got[0].Path != dir {
		t.Errorf("pending dir-removals after Ignore = %+v, want the proposal kept", got)
	}
}

// The control approve is the human answer for a whole subtree: it takes the
// same removeDirDeleted path as the review's Remove forever, so the directory
// leaves disk, the buffers under it close and the pending entry clears.
func TestApproveDirRemovalRemovesTheTree(t *testing.T) {
	t.Setenv("RAJ_TRASH", "")
	h := newHarness(t, "hello\n")
	dir := mkTree(t, h)
	h.OpenFile(filepath.Join(dir, "a.go"))
	proposeDirRemoval(t, h, dir)
	if err := h.App.ApproveDirRemoval(dir); err != nil {
		t.Fatalf("ApproveDirRemoval: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("directory still on disk after approve (err=%v)", err)
	}
	if got := h.DirRemovals(); len(got) != 0 {
		t.Errorf("pending dir-removals after approve = %+v, want none", got)
	}
	if got := h.Tabs.Count(); got != 1 {
		t.Errorf("tab count = %d, want only the original test.go left", got)
	}
}

// Approving a directory with no pending proposal is refused by name.
func TestApproveDirRemovalRefusesANotPendingPath(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "hello\n")
	missing := filepath.Join(h.primaryRoot(), "nope")
	err := h.App.ApproveDirRemoval(missing)
	if err == nil || !strings.Contains(err.Error(), "no pending dir-removal") {
		t.Fatalf("approve of a not-pending dir = %v, want a naming refusal", err)
	}
}

// An agent may withdraw only its own pending dir-removal: a peer's proposal
// survives. Precondition: a dir-removal proposed by one agent, a withdraw
// naming a different agent.
func TestWithdrawDirRemovalRefusesAPeerAgent(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "hello\n")
	dir := mkTree(t, h)
	proposeDirRemoval(t, h, dir)

	peer := uint8(piecetable.Agent + 1)
	err := h.App.WithdrawDirRemoval(dir, peer)
	if err == nil || !strings.Contains(err.Error(), "not this writer") {
		t.Fatalf("an agent withdrew a peer's dir-removal = %v, want the owner refusal", err)
	}
	if got := h.DirRemovals(); len(got) != 1 || got[0].Path != dir {
		t.Errorf("pending dir-removals = %+v, want the proposal kept", got)
	}
}

// The proposer withdraws its own dir-removal: the entry clears and the subtree
// stays. Precondition: one agent's proposal, withdrawn by that same agent.
func TestWithdrawDirRemovalByItsAgentClears(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "hello\n")
	dir := mkTree(t, h)
	proposeDirRemoval(t, h, dir)

	owner := uint8(piecetable.Agent + 7)
	if err := h.App.WithdrawDirRemoval(dir, owner); err != nil {
		t.Fatalf("WithdrawDirRemoval(owner): %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("withdraw removed the directory: %v", err)
	}
	if got := h.DirRemovals(); len(got) != 0 {
		t.Errorf("pending dir-removals = %+v, want none", got)
	}
}

// A durable joined human may withdraw an agent's pending dir-removal too.
// Precondition: a seeded Registry with a KindHuman row attached to the app's
// guard, and a dir-removal proposed by an agent. Without the human branch the
// owner check refuses and the proposal survives.
func TestWithdrawDirRemovalAllowsAJoinedHuman(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "hello\n")
	dir := mkTree(t, h)
	proposeDirRemoval(t, h, dir)

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

	if err := h.App.WithdrawDirRemoval(dir, human); err != nil {
		t.Fatalf("a joined human's withdraw was refused: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("withdraw removed the directory: %v", err)
	}
	if got := h.DirRemovals(); len(got) != 0 {
		t.Errorf("pending dir-removals = %+v, want none", got)
	}
}
