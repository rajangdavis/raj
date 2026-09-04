package control

import "testing"

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
