package editor

import (
	"errors"
	"os"
	"strings"
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

// Another writer's apply over a *rejected* lease fails as a conflict naming the
// colliding set and lands nothing; a hunk outside every lease still applies. (A
// merely Proposed set is advisory now, so an apply over it lands and leaves the
// set moved; that is covered in the piece table's own tests.)
func TestApplyDiffRefusedOverALease(t *testing.T) {
	f := NewFile("lease.go", "hello world\n", 8)
	id := proposeAt(t, f, 6, 11, "socket") // a lease over [6,12)
	if !f.RejectGroup(id) {
		t.Fatal("reject failed")
	}
	before := f.Text()

	conflicts, _ := f.ApplyDiff(piecetable.User, f.Session().Version(),
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
	if cs, _ := f.ApplyDiff(piecetable.User, f.Session().Version(),
		[]piecetable.Hunk{{Start: 6, End: 6, Text: ">"}}); len(cs) != 0 {
		t.Fatalf("boundary insert conflicted: %+v", cs)
	}
	if got := f.Text(); got != "hello >socket\n" {
		t.Errorf("text = %q, want the boundary insert to land", got)
	}
}

// A save whose agreed composition would drop a superseded set that the edit
// view still shows is refused, and the refusal is a no-op: no bytes written, no
// pending set accepted. The historical case is a declaration that exists only
// in a superseded run -- a rejected collider restores it to the edit view while
// AcceptedOnly omits it -- which a plain save would have written out, leaving a
// file that does not compile while the buffer still showed the declaration.
// Without the check the save succeeds and the file is truncated; without the
// rollback the successful AcceptGroup is left behind on a write that never
// happened. Sibling: TestSaveFailedEncodeLeavesPendingProposed, which pins the
// same rollback for the encoding refusal.
func TestSaveRefusesSupersededText(t *testing.T) {
	p := savedPane(t, "hello world\n")
	f := p.File
	superseded := proposeAt(t, f, 6, 11, "socket")

	// A later user deletion consumes the set's insertion; rejecting the
	// deletion restores its bytes to the session, so the projection puts them
	// back into the edit view while AcceptedOnly stays without them.
	f.Begin()
	// The caret Delete path refuses an edit that intersects a Proposed run, so
	// the collider has to land the way a real overwrite does: through the diff
	// path, which treats a Proposed run as advisory.
	f.ApplyDiff(piecetable.User, f.Session().Version(),
		[]piecetable.Hunk{{Start: 0, End: 12, Text: ""}})
	f.End()
	collider := f.Session().LastGroup()
	if !f.RejectGroup(collider) {
		t.Fatal("reject of the collider failed")
	}
	if g, _ := sessionGroupOf(f, superseded); !g.Invalid {
		t.Fatalf("setup: set %d is not invalid", superseded)
	}
	if len(f.Session().Pending()) != 0 {
		t.Fatalf("Pending = %+v, want none; the refusal is for a set Pending drops", f.Session().Pending())
	}
	if edit, agreed := f.Session().Project(piecetable.AcceptedAndProposed).Text(),
		f.Session().Project(piecetable.AcceptedOnly).Text(); edit == agreed {
		t.Fatalf("setup did not separate the compositions: %q", edit)
	}

	err := f.SaveOver()
	var refused *UnsavedProposedError
	if !errors.As(err, &refused) {
		t.Fatalf("SaveOver() = %v, want *UnsavedProposedError", err)
	}
	if len(refused.Groups) != 1 || refused.Groups[0].ID != superseded {
		t.Errorf("refused groups = %+v, want just set %d", refused.Groups, superseded)
	}
	if !strings.Contains(refused.Error(), "accept them to keep the text") ||
		!strings.Contains(refused.Error(), "clear them to discard it") {
		t.Errorf("refusal = %q, want both exits named", refused.Error())
	}
	if data, rerr := os.ReadFile(f.Path); rerr != nil || string(data) != "hello world\n" {
		t.Errorf("file = %q err %v, want the refused save to have written nothing", data, rerr)
	}
	if got := f.Session().GroupState(superseded); got != piecetable.Proposed {
		t.Errorf("state after the refused save = %v, want the proposal still pending", got)
	}
	if f.Dirty() && !f.ViewDirty() {
		t.Error("a refused save must not report the buffer clean")
	}
}

// A superseded set can be disposed in one gesture through File.ClearGroup, and
// the decision generation moves with it so Dirty and ViewDirty see the change.
// Without the method the clear verb has no path to an invalid Proposed set --
// ClearRejectedBlock refuses it as not rejected -- which is the wedge. Sibling:
// TestSaveWritesTheAgreedComposition for the rejected path through Save.
func TestClearGroupDisposesASupersededSet(t *testing.T) {
	p := savedPane(t, "hello world\n")
	f := p.File
	superseded := proposeAt(t, f, 6, 11, "socket")
	f.Begin()
	// The caret Delete path refuses an edit that intersects a Proposed run, so
	// land the collider through the diff path, which treats it as advisory.
	f.ApplyDiff(piecetable.User, f.Session().Version(),
		[]piecetable.Hunk{{Start: 0, End: 12, Text: ""}})
	f.End()
	collider := f.Session().LastGroup()
	if !f.RejectGroup(collider) {
		t.Fatal("reject of the collider failed")
	}
	if g, _ := sessionGroupOf(f, superseded); !g.Invalid {
		t.Fatalf("setup: set %d is not invalid", superseded)
	}
	gen := f.DecisionGeneration()

	ok, block := f.ClearGroup(superseded)
	if !ok {
		t.Fatalf("ClearGroup = false (block %+v), want one-gesture disposal", block)
	}
	if got := f.Session().GroupState(superseded); got != piecetable.Rejected {
		t.Errorf("state after clear = %v, want Rejected", got)
	}
	if f.DecisionGeneration() == gen {
		t.Error("clear did not advance the decision generation")
	}
	if edit := f.Session().Project(piecetable.AcceptedAndProposed).Text(); strings.Contains(edit, "socket") {
		t.Errorf("edit composition = %q, want the restored superseded run gone", edit)
	}
}

// A save retires the Invalid sets it accepted past: a set whose every member a
// later accepted edit moved past holds no text, is in neither projection, and
// Pending and UnsavedProposed both drop it. Left Proposed it keeps a buffer
// that matches disk reporting a decision forever and its groups listing never
// settles; the save retires it as Rejected and the bytes it writes are
// unchanged. Without the retirement the state assertion fails -- the set stays
// Proposed after the save. Sibling: TestSaveAcceptsPendingProposals for the
// hunk-carrying case and TestSaveRefusesSupersededText for the refusal.
func TestSaveRetiresMemberlessInvalidSets(t *testing.T) {
	p := savedPane(t, "hello world\n")
	f := p.File
	superseded := proposeAt(t, f, 0, 0, "AB")
	// An accepted edit removes the insertion, so the proposed set is invalid
	// with no surviving hunk, nothing restored to the edit view, and no removal
	// of its own for retirement to put back.
	f.Begin()
	f.ApplyDiff(piecetable.User, f.Session().Version(),
		[]piecetable.Hunk{{Start: 0, End: 2, Text: ""}})
	f.End()

	if g, _ := sessionGroupOf(f, superseded); !g.Invalid {
		t.Fatalf("setup: set %d = %+v, want invalid", superseded, g)
	}
	if got := f.Session().InvalidWithoutMembers(); len(got) != 1 {
		t.Fatalf("setup: InvalidWithoutMembers = %+v, want the dead set", got)
	}
	agreed := f.Session().Project(piecetable.AcceptedOnly).Text()

	if err := f.SaveOver(); err != nil {
		t.Fatal(err)
	}
	if got := f.Session().GroupState(superseded); got != piecetable.Rejected {
		t.Errorf("state after save = %v, want the dead set retired as Rejected", got)
	}
	for _, g := range f.Session().Groups() {
		if g.State == piecetable.Proposed {
			t.Errorf("set %+v is still proposed after the save", g)
		}
	}
	if got := len(f.Session().Pending()); got != 0 {
		t.Errorf("pending after save = %d, want none", got)
	}
	if got := f.Session().UnsavedProposed(); len(got) != 0 {
		t.Errorf("UnsavedProposed after save = %+v, want none", got)
	}
	data, err := os.ReadFile(f.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != agreed {
		t.Errorf("on disk = %q, want the agreed composition %q unchanged by retirement", data, agreed)
	}
	if f.Dirty() {
		t.Error("the buffer matched disk, so Dirty is false after a save that retired the dead sets")
	}
	if f.ViewDirty() {
		t.Error("retirement writes nothing new to the view, so ViewDirty is false")
	}
}

// sessionGroupOf finds a piecetable listing by id for a File, the editor-side
// sibling of the control test's findGroup.
func sessionGroupOf(f *File, id uint64) (piecetable.Group, bool) {
	for _, g := range f.Session().Groups() {
		if g.ID == id {
			return g, true
		}
	}
	return piecetable.Group{}, false
}
