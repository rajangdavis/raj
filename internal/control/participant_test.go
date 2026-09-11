package control

import (
	"fmt"
	"testing"
)

// The bug this replaces: an id was minted per connection, so a harness that
// restarted became a different writer and orphaned the text it had already
// written under an id nothing would ever claim again.
func TestSameIdentityKeepsItsAuthorID(t *testing.T) {
	r := NewRegistry()
	first, err := r.Join("harness-abc", "claude-1", KindAgent)
	if err != nil {
		t.Fatal(err)
	}
	r.Leave(first)
	again, err := r.Join("harness-abc", "claude-1", KindAgent)
	if err != nil {
		t.Fatal(err)
	}
	if again != first {
		t.Errorf("reconnected as %d, was %d; its earlier text is now orphaned", again, first)
	}
	other, _ := r.Join("harness-xyz", "claude-2", KindAgent)
	if other == first {
		t.Error("two identities share one author id")
	}
}

// A disconnected participant stays listed: its text is still in the document,
// so a reader still needs to know whose it is.
func TestLeaveKeepsTheRow(t *testing.T) {
	r := NewRegistry()
	id, _ := r.Join("h", "claude", KindAgent)
	r.Leave(id)
	p, ok := r.Get(id)
	if !ok {
		t.Fatal("the row was dropped on disconnect")
	}
	if p.Connected {
		t.Error("still marked connected")
	}
	if p.Name != "claude" {
		t.Errorf("name = %q", p.Name)
	}
}

// Kind is stored, not inferred from the number. That is what makes a second
// human a row rather than a renumbering.
func TestKindIsALookupNotAnIDRange(t *testing.T) {
	r := NewRegistry()
	second, _ := r.Join("alice", "alice", KindHuman)
	agent, _ := r.Join("harness", "claude", KindAgent)

	if r.IsAgent(LocalHuman) {
		t.Error("the local human reads as an agent")
	}
	if r.IsAgent(second) {
		t.Errorf("a second human (id %d) reads as an agent; the id-range rule "+
			"would have said yes because it is >= 2", second)
	}
	if !r.IsAgent(agent) {
		t.Error("an agent does not read as one")
	}
	if r.IsAgent(AuthorOriginal) {
		t.Error("the file as loaded reads as an agent")
	}
}

// Author 0 is the file as loaded: not a person, so not a participant.
func TestOriginalIsNotAParticipant(t *testing.T) {
	r := NewRegistry()
	if _, ok := r.Get(AuthorOriginal); ok {
		t.Error("author 0 has a participant row")
	}
	for _, p := range r.List() {
		if p.ID == AuthorOriginal {
			t.Error("author 0 appears in the list of who is here")
		}
	}
	if len(r.List()) != 1 || r.List()[0].ID != LocalHuman {
		t.Errorf("a fresh registry holds %+v, want just the local human", r.List())
	}
}

func TestJoinRequiresAnIdentity(t *testing.T) {
	r := NewRegistry()
	if _, err := r.Join("", "nameless", KindAgent); err == nil {
		t.Error("an empty identity was accepted")
	}
}

// Ids are one byte. Reconnections no longer burn them, but distinct identities
// still do, and running out must be an error rather than a wrapped id landing
// on somebody else's text.
func TestIDsRunOutCleanly(t *testing.T) {
	r := NewRegistry()
	var err error
	for i := 0; i < MaxParticipants+10 && err == nil; i++ {
		_, err = r.Join(string(rune('a'+i%26))+string(rune('0'+i/26)), "", KindAgent)
	}
	if err == nil {
		t.Fatal("ids never ran out")
	}
	for _, p := range r.List() {
		if p.ID == AuthorOriginal {
			t.Fatal("an id wrapped onto the file-as-loaded")
		}
	}
}

// The contract -live filters on: a gone participant stays listed (its text is
// still in the document) but reads not-connected, and re-joining flips the
// same row back to live rather than minting a new one.
func TestLeaveMarksGoneNotLive(t *testing.T) {
	r := NewRegistry()
	id, _ := r.Join("harness-abc", "claude-1", KindAgent)

	isLive := func(want bool) {
		t.Helper()
		for _, p := range r.List() {
			if p.ID == id && p.Connected != want {
				t.Errorf("id %d connected = %v, want %v", id, p.Connected, want)
			}
		}
	}

	isLive(true)
	r.Leave(id)
	isLive(false)
	if _, ok := r.Get(id); !ok {
		t.Error("the row was dropped on disconnect; -live must filter, not lose it")
	}
	again, err := r.Join("harness-abc", "claude-1", KindAgent)
	if err != nil {
		t.Fatal(err)
	}
	if again != id {
		t.Errorf("reconnect minted %d, want the same row %d back", again, id)
	}
	isLive(true)
}

// Full of rows is not full of writers. With every id handed out, a new
// identity reuses the lowest row whose participant has disconnected — the
// lowest, so two joins racing the cap agree on which id each one got.
func TestGoneIDsAreRecycledAtTheCap(t *testing.T) {
	r := NewRegistry()
	fill := make([]uint8, 0, MaxParticipants-1)
	for i := 0; i < MaxParticipants-1; i++ {
		id, err := r.Join(fmt.Sprintf("agent-%d", i), "", KindAgent)
		if err != nil {
			t.Fatalf("join %d: %v", i, err)
		}
		fill = append(fill, id)
	}
	// With nobody gone the cap still refuses.
	if _, err := r.Join("newcomer", "", KindAgent); err == nil {
		t.Fatal("a full registry with nobody gone accepted a join")
	}
	r.Leave(fill[38])
	r.Leave(fill[10])
	r.Leave(fill[23])
	id, err := r.Join("newcomer", "claude-9", KindAgent)
	if err != nil {
		t.Fatal(err)
	}
	if want := fill[10]; id != want {
		t.Errorf("recycled id %d, want the lowest gone id %d", id, want)
	}
	p, ok := r.Get(id)
	if !ok {
		t.Fatal("the recycled row is missing")
	}
	if p.Identity != "newcomer" || p.Name != "claude-9" || p.Kind != KindAgent || !p.Connected {
		t.Errorf("recycled row = %+v, want the new identity live under it", p)
	}
}

// Recycling forgets who the row was: the id belongs to the new writer now, so
// the evicted identity must not get it back. Under a still-full cap it takes
// the next gone row instead, and a missing name defaults to the identity,
// exactly as on a fresh id.
func TestRecycledIDForgetsItsOldIdentity(t *testing.T) {
	r := NewRegistry()
	var recycled, spare uint8
	for i := 0; i < MaxParticipants-1; i++ {
		id, err := r.Join(fmt.Sprintf("agent-%d", i), "", KindAgent)
		if err != nil {
			t.Fatalf("join %d: %v", i, err)
		}
		switch i {
		case 10:
			recycled = id
		case 40:
			spare = id
		}
	}
	r.Leave(recycled)
	r.Leave(spare)

	id, err := r.Join("newcomer", "", KindAgent)
	if err != nil {
		t.Fatal(err)
	}
	if id != recycled {
		t.Fatalf("took %d, want the lower gone id %d", id, recycled)
	}
	again, err := r.Join("agent-10", "", KindAgent)
	if err != nil {
		t.Fatal(err)
	}
	if again == recycled {
		t.Errorf("the evicted identity got id %d back; that row is another writer now", recycled)
	}
	if again != spare {
		t.Errorf("the evicted identity took %d, want the remaining gone id %d", again, spare)
	}
	if p, _ := r.Get(again); p.Name != "agent-10" {
		t.Errorf("name = %q, want the identity as the default name", p.Name)
	}
}
