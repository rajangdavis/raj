package app

import (
	"path/filepath"
	"testing"

	"raj/internal/piecetable"
)

// The watched workspace generation is the value clients watch: it must move on
// a decision and on a workspace-level removal, neither of which moves a buffer
// session version. These tests drive the real App state and decision methods —
// the pending-removal maps, File.AcceptGroup — and assert through
// reviewGeneration itself, so they pin the watch's trigger rather than a fake
// hash input.

// TestReviewGenerationMovesOnADecision pins the gap this slice closes: accept
// is a decision, not an edit, so it leaves Session().Version() alone while
// flipping the agreed composition. The watch must still wake.
func TestReviewGenerationMovesOnADecision(t *testing.T) {
	h := newHarness(t, reviewFixture)
	p := h.Pane()
	sess := p.File.Session()

	versionBefore := sess.Version()
	decisionBefore := p.File.DecisionGeneration()
	id := propose(t, h, piecetable.Hunk{Start: reviewAt, End: reviewAt + len(reviewOld), Text: reviewNew})
	if id == 0 {
		t.Fatal("propose returned no change set id")
	}
	if sess.Version() == versionBefore {
		t.Fatal("setup: the proposal did not advance the session version")
	}
	// The bare propose helper marks the run on the session directly. The real
	// app routes an agent's apply through File.ProposeGroup, so route it here
	// too; only that call moves the decision generation, which is the state the
	// decision under test then moves again.
	p.File.ProposeGroup(id)
	if p.File.DecisionGeneration() == decisionBefore {
		t.Fatal("setup: the proposal did not advance the decision generation")
	}

	before := h.reviewGeneration()
	decidedVersion := sess.Version()
	decidedDecision := p.File.DecisionGeneration()

	p.File.AcceptGroup(id)

	if p.File.DecisionGeneration() == decidedDecision {
		t.Error("AcceptGroup did not advance the decision generation")
	}
	if sess.Version() != decidedVersion {
		t.Errorf("AcceptGroup moved the session version %d -> %d; a decision must not be an edit",
			decidedVersion, sess.Version())
	}
	if got := h.reviewGeneration(); got == before {
		t.Error("reviewGeneration did not move on a decision; the watch would stay asleep")
	}
}

// TestReviewGenerationMovesOnRemovals pins the other half of the gap: a pending
// deletion or dir-removal lives on the app, not in any buffer, so nothing in a
// buffer version moves when it arrives or is withdrawn. It still has to wake
// every watcher, or a client's removal list goes stale until some unrelated
// edit happens.
func TestReviewGenerationMovesOnRemovals(t *testing.T) {
	h := newHarness(t, reviewFixture)
	path := filepath.Join(h.root, "ghost.go") // unopened: the file proposal raises no gate

	beforeDelete := h.reviewGeneration()
	if err := h.App.ProposeDeletion(path, uint8(piecetable.Agent)); err != nil {
		t.Fatal(err)
	}
	deleteProposed := h.reviewGeneration()
	if deleteProposed == beforeDelete {
		t.Error("ProposeDeletion did not move the review generation")
	}
	if err := h.App.WithdrawDeletion(path, uint8(piecetable.Agent)); err != nil {
		t.Fatal(err)
	}
	if got := h.reviewGeneration(); got == deleteProposed {
		t.Error("WithdrawDeletion did not move the review generation")
	}

	dir := mkTree(t, h)
	beforeDir := h.reviewGeneration()
	if err := h.App.ProposeDirRemoval(dir, uint8(piecetable.Agent)); err != nil {
		t.Fatal(err)
	}
	dirProposed := h.reviewGeneration()
	if dirProposed == beforeDir {
		t.Error("ProposeDirRemoval did not move the review generation")
	}
	if err := h.App.WithdrawDirRemoval(dir, uint8(piecetable.Agent)); err != nil {
		t.Fatal(err)
	}
	if got := h.reviewGeneration(); got == dirProposed {
		t.Error("WithdrawDirRemoval did not move the review generation")
	}
}

// TestReviewGenerationStableWhenIdle pins that the generation is a function of
// state, not of when or how often it is read. Two removals are pending so Go's
// per-iteration map order would show through if the keys were not sorted.
func TestReviewGenerationStableWhenIdle(t *testing.T) {
	h := newHarness(t, reviewFixture)

	first := h.reviewGeneration()
	if second := h.reviewGeneration(); second != first {
		t.Fatalf("reviewGeneration changed with no mutation: %d then %d", first, second)
	}

	for _, name := range []string{"a.go", "b.go"} {
		if err := h.App.ProposeDeletion(filepath.Join(h.root, name), uint8(piecetable.Agent)); err != nil {
			t.Fatal(err)
		}
	}
	first = h.reviewGeneration()
	if second := h.reviewGeneration(); second != first {
		t.Errorf("reviewGeneration changed with two pending removals and no mutation: %d then %d", first, second)
	}
}

// TestReviewGenerationMovesOnABareSave pins the third gap: a save clears Dirty
// without moving the session version or the decision generation, so a watcher
// that folded only those two would stay parked. The dirty flag is part of the
// generation for exactly this case.
func TestReviewGenerationMovesOnABareSave(t *testing.T) {
	h := newHarness(t, reviewFixture)
	p := h.Pane()
	p.InsertText("X")
	if !p.File.ViewDirty() {
		t.Fatal("setup: the buffer is not dirty after an edit")
	}
	before := h.reviewGeneration()
	version := p.File.Session().Version()

	if err := p.File.Save(); err != nil {
		t.Fatalf("save = %v", err)
	}
	if p.File.ViewDirty() {
		t.Fatal("the buffer is still dirty after a save")
	}
	if p.File.Session().Version() != version {
		t.Fatalf("a bare save moved the version %d -> %d", version, p.File.Session().Version())
	}
	if got := h.reviewGeneration(); got == before {
		t.Error("reviewGeneration did not move when a bare save cleared dirty; the watch would stay asleep")
	}
}
