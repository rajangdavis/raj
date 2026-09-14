package editor

import (
	"os"
	"testing"

	"raj/internal/piecetable"
)

// proposeAt applies an agent replacement and marks it proposed, returning the
// change set id. It mirrors what the control layer does with an agent apply.
func proposeAt(t *testing.T, f *File, start, end int, text string) uint64 {
	t.Helper()
	f.Begin()
	f.ApplyDiff(piecetable.Agent, f.Session().Version(),
		[]piecetable.Hunk{{Start: start, End: end, Text: text}})
	f.End()
	id := f.Session().LastGroup()
	f.ProposeGroup(id)
	return id
}

// A rejected set stays in the buffer — rejecting is a decision, not an edit —
// but it never reaches disk, and the buffer reads clean because the agreed
// composition is exactly what was last written.
func TestSaveWritesTheAgreedComposition(t *testing.T) {
	p := savedPane(t, "hello world\n")
	id := proposeAt(t, p.File, 6, 11, "socket")
	if !p.File.RejectGroup(id) {
		t.Fatal("reject did not change the state")
	}
	// The rejected text is not agreed, so it is not a change to write: the
	// buffer is already clean even though the screen shows the set.
	if p.File.Dirty() {
		t.Error("a rejected set does not change the agreed composition, so the buffer is clean")
	}
	if err := p.File.Save(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(p.File.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello world\n" {
		t.Errorf("on disk = %q, want the agreed composition", string(data))
	}
	if got := p.File.Text(); got != "hello socket\n" {
		t.Errorf("buffer = %q, want the rejected text to stay", got)
	}
	if p.File.Dirty() {
		t.Error("after a save the buffer is clean even though the rejected text is not on disk")
	}
}

// A decision moves the agreed composition without moving the session version,
// so the clean comparison has to notice it: un-rejecting a saved set makes the
// buffer dirty again, re-rejecting it makes it clean.
func TestDecisionMovesTheCleanBaseline(t *testing.T) {
	p := savedPane(t, "hello world\n")
	id := proposeAt(t, p.File, 6, 11, "socket")
	p.File.RejectGroup(id)
	if err := p.File.Save(); err != nil {
		t.Fatal(err)
	}
	if p.File.Dirty() {
		t.Fatal("a just-saved buffer is clean")
	}
	p.File.AcceptGroup(id) // un-reject: the set is agreed now
	if !p.File.Dirty() {
		t.Error("un-rejecting a saved set changes the agreed composition, so the buffer is dirty")
	}
	p.File.RejectGroup(id)
	if p.File.Dirty() {
		t.Error("re-rejecting restores the agreed composition, so the buffer is clean")
	}
}

// ViewDirty is the view question, and it stays true while Dirty is false: a
// rejected set is not part of the agreed composition, so a save would not write
// it, but search and the tab marker must still see the buffer.
func TestViewDirtySeesRejectedSets(t *testing.T) {
	p := savedPane(t, "hello world\n")
	id := proposeAt(t, p.File, 6, 11, "socket")
	if !p.File.ViewDirty() {
		t.Fatal("a proposed set is unsaved view work")
	}
	if !p.File.RejectGroup(id) {
		t.Fatal("reject failed")
	}
	if p.File.Dirty() {
		t.Fatal("the agreed composition matches disk, so Dirty is false")
	}
	if !p.File.ViewDirty() {
		t.Error("the rejected text is still in the view, so ViewDirty is true")
	}
	if err := p.File.Save(); err != nil {
		t.Fatal(err)
	}
	if p.File.Dirty() {
		t.Fatal("after the save the agreed composition is clean")
	}
	if !p.File.ViewDirty() {
		t.Error("the rejected text is still in the view after the save")
	}
}

// The save gesture is the approval: a pending proposal is accepted and written,
// which is what the human answering Save in the review popup means.
func TestSaveAcceptsPendingProposals(t *testing.T) {
	p := savedPane(t, "hello world\n")
	proposeAt(t, p.File, 6, 11, "socket")
	if got := len(p.File.Session().Pending()); got != 1 {
		t.Fatalf("pending = %d, want 1", got)
	}
	if err := p.File.Save(); err != nil {
		t.Fatal(err)
	}
	if got := len(p.File.Session().Pending()); got != 0 {
		t.Errorf("pending after save = %d, want none", got)
	}
	data, err := os.ReadFile(p.File.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello socket\n" {
		t.Errorf("on disk = %q, want the accepted proposal", string(data))
	}
	if p.File.Dirty() {
		t.Error("a buffer saved with its proposal accepted is clean")
	}
}

// Another writer's apply over a lease fails as a conflict naming the colliding
// set and lands nothing; a hunk outside every lease still applies. (The lease
// owner amending its own proposal is the one case the span lease allows, and
// that is covered in the piece table's own tests.)
func TestApplyDiffRefusedOverALease(t *testing.T) {
	f := NewFile("lease.go", "hello world\n", 8)
	id := proposeAt(t, f, 6, 11, "socket") // a proposed lease over [6,12)
	before := f.Text()

	conflicts := f.ApplyDiff(piecetable.User, f.Session().Version(),
		[]piecetable.Hunk{{Start: 7, End: 9, Text: "XY"}})
	if len(conflicts) != 1 {
		t.Fatalf("conflicts = %+v, want one lease refusal", conflicts)
	}
	if conflicts[0].Group != id {
		t.Errorf("conflict group = %d, want %d", conflicts[0].Group, id)
	}
	if got := f.Text(); got != before {
		t.Errorf("text = %q, want the leased text unchanged", got)
	}

	// An insertion flush with the run start is outside it and still lands.
	if cs := f.ApplyDiff(piecetable.User, f.Session().Version(),
		[]piecetable.Hunk{{Start: 6, End: 6, Text: ">"}}); len(cs) != 0 {
		t.Fatalf("boundary insert conflicted: %+v", cs)
	}
	if got := f.Text(); got != "hello >socket\n" {
		t.Errorf("text = %q, want the boundary insert to land", got)
	}
}
