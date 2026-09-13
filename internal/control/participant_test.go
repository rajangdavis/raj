package control

import (
	"fmt"
	"testing"
	"time"
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

// A registry rebuilt from a persisted author table seeds explicit ids, so a
// restored op's author resolves to the identity it was written under rather
// than whatever a fresh join order would hand out, and a later join for a new
// identity does not collide with a seeded one.
func TestSeedRestoresExplicitAuthors(t *testing.T) {
	r := NewRegistry()
	if !r.Seed(Participant{ID: 5, Identity: "tok_a", Name: "claude-a", Kind: KindAgent}) {
		t.Fatal("Seed refused a fresh explicit row")
	}
	if !r.Seed(Participant{ID: 7, Identity: "tok_b", Name: "claude-b", Kind: KindAgent}) {
		t.Fatal("Seed refused a second explicit row")
	}
	// Idempotent and first-wins, and impossible rows are refused.
	if r.Seed(Participant{ID: 5, Identity: "other", Name: "x", Kind: KindAgent}) {
		t.Error("Seed overwrote an existing id")
	}
	if r.Seed(Participant{ID: 0, Identity: "zero", Name: "x", Kind: KindAgent}) {
		t.Error("Seed installed author 0")
	}
	if r.Seed(Participant{ID: 9, Identity: "", Name: "x", Kind: KindAgent}) {
		t.Error("Seed installed an empty identity")
	}

	got, ok := r.Get(5)
	if !ok || got.Identity != "tok_a" || got.Name != "claude-a" || got.Kind != KindAgent {
		t.Errorf("id 5 = %+v, want the seeded row", got)
	}
	if got.Connected {
		t.Error("a seeded row reads as live; nothing is attached across a restart")
	}
	// The same identity reconnecting resolves to the seeded id, and a new
	// identity lands past both seeded ids rather than on one of them.
	if again, err := r.Join("tok_a", "", KindAgent); err != nil || again != 5 {
		t.Errorf("Join(tok_a) = %d, %v, want the seeded id 5", again, err)
	}
	if fresh, err := r.Join("fresh", "", KindAgent); err != nil || fresh <= 7 {
		t.Errorf("Join(fresh) = %d, %v, want an id past the seeded 7", fresh, err)
	}
}

// A registry restored with an id at the very top must not wedge the counter.
// Seed used to pin next to 0, a second "full" sentinel, so a registry holding
// one restored row behaved as though every id were taken.
func TestSeedAtTheTopDoesNotWedge(t *testing.T) {
	r := NewRegistry()
	if !r.Seed(Participant{ID: MaxParticipants, Identity: "tok_top", Name: "claude-top", Kind: KindAgent}) {
		t.Fatal("Seed refused id MaxParticipants")
	}
	if _, err := r.Join("fresh", "claude-new", KindAgent); err != nil {
		t.Fatalf("Join after seeding id %d refused: %v", MaxParticipants, err)
	}
}

// The top id is a valid author byte like any other. A gone row at 255 is the
// lowest recyclable gap when it is the only one, so Join hands it back rather
// than scanning past it and reporting the table full.
func TestGoneTopIDIsRecycled(t *testing.T) {
	r := NewRegistry()
	if !r.Seed(Participant{ID: MaxParticipants, Identity: "tok_top", Name: "claude-top", Kind: KindAgent}) {
		t.Fatal("Seed refused id MaxParticipants")
	}
	id, err := r.Join("newcomer", "claude-new", KindAgent)
	if err != nil {
		t.Fatalf("Join with id %d gone: %v", MaxParticipants, err)
	}
	if id != MaxParticipants {
		t.Errorf("recycled id %d, want the gone top id %d", id, MaxParticipants)
	}
	p, ok := r.Get(id)
	if !ok || !p.Connected || p.Identity != "newcomer" || p.Name != "claude-new" {
		t.Errorf("row %d = %+v, want the newcomer live under it", id, p)
	}
}

// A connection minted a provisional id rebinds to its durable identity on
// hello. The provisional row must be released as it moves off it, not held
// live until the connection ends, and the deferred release must name the row
// the connection is bound to at the end rather than the one it was minted.
func TestRebindReleasesTheProvisionalAndBoundAuthors(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "hello\n"})
	first, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Do(Request{Op: "ping"}); err != nil {
		t.Fatal(err)
	}
	provisional := first.Author()

	res, err := first.Do(Request{Op: "hello", Identity: "harness-abc", Name: "claude-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK {
		t.Fatalf("hello = %+v", res)
	}
	bound := first.Author()
	if bound == provisional {
		t.Fatalf("hello kept the provisional id %d", provisional)
	}
	// The provisional row is released as the connection moves off it, so it
	// is recyclable at once instead of leaking one id per reconnect.
	if p, ok := ed.srv.Participants.Get(provisional); ok && p.Connected {
		t.Errorf("provisional id %d stayed connected after the rebind", provisional)
	}

	first.Close()
	// The row actually bound at the end is the one the defer releases.
	deadline := time.Now().Add(5 * time.Second)
	for {
		p, ok := ed.srv.Participants.Get(bound)
		if ok && !p.Connected {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("bound id %d still connected after the connection closed: %+v", bound, p)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
