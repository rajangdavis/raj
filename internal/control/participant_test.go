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

// A participant's task is set by its register call and survives the ordinary
// reconnect that carries no task, which is what lets a later command's writes
// still be filed under it. A non-empty task updates the row.
func TestTaskSurvivesReconnectAndUpdates(t *testing.T) {
	r := NewRegistry()
	id, err := r.Join("harness-abc", "claude-1", KindAgent)
	if err != nil {
		t.Fatal(err)
	}
	r.SetTask(id, "task-1")
	if p, _ := r.Get(id); p.Task != "task-1" {
		t.Fatalf("task = %q, want task-1", p.Task)
	}

	// A reconnect with no task -- the bind-first hello every later command
	// sends -- keeps the row's task rather than clearing it.
	r.Leave(id)
	again, err := r.Join("harness-abc", "claude-1", KindAgent)
	if err != nil {
		t.Fatal(err)
	}
	r.SetTask(again, "")
	if p, _ := r.Get(again); p.Task != "task-1" {
		t.Errorf("task after a taskless reconnect = %q, want task-1", p.Task)
	}

	// A register for a different task moves the participant's future writes.
	r.SetTask(again, "task-2")
	if p, _ := r.Get(again); p.Task != "task-2" {
		t.Errorf("task after re-register = %q, want task-2", p.Task)
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

// One participant holds several connections at once — an agent's parked recv
// and every short `raj ctl` call under the same key — so one of them closing
// must not mark the row gone. Before connections were counted, the first
// short call to finish cleared Connected while the recv still listened, and
// `send --to all` refused with "no other driver is connected".
func TestLeaveKeepsARowWithAnotherConnectionLive(t *testing.T) {
	r := NewRegistry()
	recv, _ := r.Join("raj-agent", "claude", KindAgent)
	call, _ := r.Join("raj-agent", "", KindAgent)
	if call != recv {
		t.Fatalf("second connection got id %d, want %d", call, recv)
	}
	r.Leave(call)
	if p, _ := r.Get(recv); !p.Connected {
		t.Error("closing one of two connections marked the participant gone")
	}
	r.Leave(recv)
	if p, _ := r.Get(recv); p.Connected {
		t.Error("closing the last connection left the participant connected")
	}
	// A stray extra Leave does not drive the count negative, so the next
	// connection still reads as live.
	r.Leave(recv)
	again, _ := r.Join("raj-agent", "", KindAgent)
	if p, _ := r.Get(again); !p.Connected {
		t.Error("a reconnect after an extra Leave did not read as connected")
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

// Join announces a row to the joined hook only when it is new or its name
// changes, so the editor can persist the author table and an ordinary
// per-command hello costs it nothing. A refused join announces nothing.
func TestJoinAnnouncesChangedRows(t *testing.T) {
	r := NewRegistry()
	var got []uint8
	r.setJoined(func(id uint8) { got = append(got, id) })

	first, err := r.Join("h", "claude", KindAgent)
	if err != nil {
		t.Fatal(err)
	}
	// Reconnects that change nothing announce nothing.
	if _, err := r.Join("h", "", KindAgent); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Join("h", "claude", KindAgent); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != first {
		t.Fatalf("joined = %v, want one announce of %d for the new row", got, first)
	}
	// A rename is a row the editor has to persist.
	if _, err := r.Join("h", "claude-2", KindAgent); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1] != first {
		t.Errorf("joined after a rename = %v, want a second announce of %d", got, first)
	}
	// A refused join never reaches the hook.
	if _, err := r.Join("", "nameless", KindAgent); err == nil {
		t.Fatal("an empty identity was accepted")
	}
	if len(got) != 2 {
		t.Errorf("a refused join announced %v", got[2:])
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

// A seeded row keeps its display name when the same identity reconnects with
// an empty-name hello: after a restart the harness comes back under its key
// with no name, so the seeded name is what `chat --to` and `who` resolve. A
// later hello that carries a name still replaces the seeded one.
func TestSeedNameSurvivesAnEmptyHello(t *testing.T) {
	r := NewRegistry()
	if !r.Seed(Participant{ID: 5, Identity: "tok_a", Name: "claude-a", Kind: KindAgent}) {
		t.Fatal("Seed refused a fresh explicit row")
	}
	if _, err := r.Join("tok_a", "", KindAgent); err != nil {
		t.Fatal(err)
	}
	if p, ok := r.Get(5); !ok || p.Name != "claude-a" {
		t.Errorf("name after an empty-name hello = %q, want claude-a", p.Name)
	}
	if _, err := r.Join("tok_a", "claude-b", KindAgent); err != nil {
		t.Fatal(err)
	}
	if p, _ := r.Get(5); p.Name != "claude-b" {
		t.Errorf("name after a named hello = %q, want claude-b", p.Name)
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
	// The provisional id was reserved, not a row: it is released as the
	// connection moves off it, so no participant row exists under it and it
	// is recyclable at once instead of leaking one id per reconnect.
	if p, ok := ed.srv.Participants.Get(provisional); ok {
		t.Errorf("provisional id %d left a participant row behind: %+v", provisional, p)
	}
	if again, err := ed.srv.Participants.Reserve(); err != nil || again != provisional {
		t.Errorf("Reserve after rebind = %d, %v; want the released provisional id %d",
			again, err, provisional)
	} else {
		ed.srv.Participants.Release(again)
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

// A reserved id is a connection's provisional byte, not a participant. It has
// no row, so it is absent from List and Get, and Release hands the lowest one
// back for the next connection.
func TestReserveCarvesOutIdsWithoutRows(t *testing.T) {
	r := NewRegistry()
	a, err := r.Reserve()
	if err != nil {
		t.Fatal(err)
	}
	b, err := r.Reserve()
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatalf("Reserve handed out %d twice", a)
	}
	if a < FirstAgent {
		t.Errorf("reserved id %d is below FirstAgent", a)
	}
	for _, id := range []uint8{a, b} {
		if p, ok := r.Get(id); ok {
			t.Errorf("reserved id %d has a participant row: %+v", id, p)
		}
		for _, p := range r.List() {
			if p.ID == id {
				t.Errorf("reserved id %d appears in List", id)
			}
		}
	}
	r.Release(a)
	again, err := r.Reserve()
	if err != nil {
		t.Fatal(err)
	}
	if again != a {
		t.Errorf("Reserve after Release = %d, want the freed %d", again, a)
	}
}

// A durable identity must never be handed a reserved id, even though a
// reserved id has no row for Join's occupancy check to see. next is still just
// past the local human here, so without the reserved check Join would land on
// the lowest reserved byte.
func TestJoinSkipsReservedIDs(t *testing.T) {
	r := NewRegistry()
	reserved := map[uint8]bool{}
	for i := 0; i < 5; i++ {
		id, err := r.Reserve()
		if err != nil {
			t.Fatal(err)
		}
		reserved[id] = true
	}
	id, err := r.Join("harness", "claude", KindAgent)
	if err != nil {
		t.Fatal(err)
	}
	if reserved[id] {
		t.Errorf("Join returned reserved id %d", id)
	}
}

// At the cap Join recycles only a row whose participant disconnected. A
// reserved id has no row, so it is never a recycling candidate: with only a
// reservation free, Join refuses rather than putting a durable identity on a
// live connection's byte.
func TestRecyclingNeverTakesAReservedID(t *testing.T) {
	r := NewRegistry()
	held, err := r.Reserve()
	if err != nil {
		t.Fatal(err)
	}
	fill := make([]uint8, 0, MaxParticipants-2)
	for i := 0; i < MaxParticipants-2; i++ {
		id, err := r.Join(fmt.Sprintf("agent-%d", i), "", KindAgent)
		if err != nil {
			t.Fatalf("join %d: %v", i, err)
		}
		fill = append(fill, id)
	}
	// Every agent byte is a connected row except the reservation: nothing is
	// gone, so there is no honest id left.
	if id, err := r.Join("blocked", "", KindAgent); err == nil {
		t.Fatalf("Join took id %d while the only free byte %d was reserved", id, held)
	}
	// One row disconnects: Join recycles that, never the reserved byte.
	r.Leave(fill[38])
	id, err := r.Join("newcomer", "", KindAgent)
	if err != nil {
		t.Fatal(err)
	}
	if id != fill[38] {
		t.Errorf("recycled %d, want the gone row %d", id, fill[38])
	}
	if id == held {
		t.Errorf("recycled the reserved id %d", held)
	}
}

// The leak this fixes: a one-off invocation used to Join an anon-N row per
// connection, so who grew to the cap full of dead rows. A reservation releases
// without a trace, so any number of connections leave the registry as it was.
func TestReserveReleaseDoesNotGrowTheRegistry(t *testing.T) {
	r := NewRegistry()
	for i := 0; i < 1000; i++ {
		id, err := r.Reserve()
		if err != nil {
			t.Fatalf("Reserve %d: %v", i, err)
		}
		r.Release(id)
	}
	if got := r.List(); len(got) != 1 || got[0].ID != LocalHuman {
		t.Fatalf("after 1000 reserve/release cycles List = %+v, want just the local human", got)
	}
}

// A durable identity survives a disconnect and a rejoin even while other
// connections hold reservations, so the provisional bytes moving underneath do
// not disturb attribution.
func TestDurableIdentitySurvivesReservations(t *testing.T) {
	r := NewRegistry()
	id, err := r.Join("harness-abc", "claude-1", KindAgent)
	if err != nil {
		t.Fatal(err)
	}
	r.Leave(id)
	var held []uint8
	for i := 0; i < 10; i++ {
		got, err := r.Reserve()
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, got)
	}
	for _, got := range held {
		r.Release(got)
	}
	again, err := r.Join("harness-abc", "claude-1", KindAgent)
	if err != nil {
		t.Fatal(err)
	}
	if again != id {
		t.Errorf("rejoined as %d, was %d", again, id)
	}
}

// The derived working states, and the declared state winning over them, at an
// injected clock so the windows move without sleeping. A request inside
// activeWindow is working; quiet with nothing held is idle; a parked recv is
// listening; a held proposal is waiting; a declared state wins; a declared
// working state gone past staleWindow reads stale; a closed row reads gone.
func TestWorkingStatesDerivedAndDeclared(t *testing.T) {
	r := NewRegistry()
	now := time.Unix(1000, 0)
	r.now = func() time.Time { return now }
	id, err := r.Join("harness", "claude", KindAgent)
	if err != nil {
		t.Fatal(err)
	}
	stateOf := func(hookRunning func(uint8) bool) string {
		t.Helper()
		for _, p := range r.States(hookRunning) {
			if p.ID == id {
				return p.State
			}
		}
		t.Fatalf("participant %d missing from the roster", id)
		return ""
	}

	r.Touch(id)
	if got := stateOf(nil); got != StateWorking {
		t.Errorf("recent request = %q, want working", got)
	}

	now = now.Add(activeWindow + time.Second)
	if got := stateOf(nil); got != StateIdle {
		t.Errorf("quiet with nothing held = %q, want idle", got)
	}
	if got := stateOf(func(uint8) bool { return true }); got != StateWorking {
		t.Errorf("hook run in flight = %q, want working", got)
	}

	r.SetListening(id, true)
	if got := stateOf(nil); got != StateListening {
		t.Errorf("parked recv = %q, want listening", got)
	}

	r.SetListening(id, false)
	r.SetPending(map[uint8]bool{id: true})
	if got := stateOf(nil); got != StateWaiting {
		t.Errorf("quiet while holding a proposal = %q, want waiting", got)
	}
	r.SetPending(nil)

	r.SetState(id, StateReview, "user", "checking")
	for _, p := range r.States(nil) {
		if p.ID != id {
			continue
		}
		if p.State != StateReview || p.Declared != StateReview || p.On != "user" || p.Note != "checking" {
			t.Errorf("declared row = %+v, want review on user with its note", p)
		}
	}

	now = now.Add(staleWindow)
	r.SetState(id, StateWorking, "", "")
	if got := stateOf(nil); got != StateStale {
		t.Errorf("declared working gone quiet = %q, want stale", got)
	}

	r.Leave(id)
	if got := stateOf(nil); got != StateGone {
		t.Errorf("closed connection = %q, want gone", got)
	}
}

// The local human row is seeded as identity "local" with display name "you".
// Neither is a durable identity a hello may claim: before the fix
// Join("local", ...) returned author 1, which passes the human gate. The
// refusal applies whatever kind is requested, because the keyboard is not a
// socket.
func TestJoinRefusesAReservedIdentity(t *testing.T) {
	r := NewRegistry()
	for _, identity := range []string{"local", "you"} {
		if _, err := r.Join(identity, "impostor", KindAgent); err == nil {
			t.Errorf("Join(%q) as an agent succeeded; reserved identities must be refused", identity)
		}
		if _, err := r.Join(identity, "impostor", KindHuman); err == nil {
			t.Errorf("Join(%q) as a human succeeded; reserved identities must be refused", identity)
		}
	}
	// The seeded human row is untouched.
	if p, ok := r.Get(LocalHuman); !ok || p.Identity != "local" || !p.Connected {
		t.Errorf("the local human row = %+v ok=%v, want the seeded connected row", p, ok)
	}
}

// An existing human row is not inherited by a join that does not claim to be
// human. serve forces a TCP caller to KindAgent, so this is the rule that keeps
// a durable human (a second human or an attached client) off the TCP path, and
// an agent from taking a human identity.
func TestJoinRefusesAHumanRowAsAnAgent(t *testing.T) {
	r := NewRegistry()
	human, err := r.Join("client:desk", "desk", KindHuman)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Join("client:desk", "desk", KindAgent); err == nil {
		t.Error("an agent-kind join took over the human row")
	}
	if p, ok := r.Get(human); !ok || p.Kind != KindHuman {
		t.Errorf("the human row changed: %+v ok=%v", p, ok)
	}
	// A human claim still rejoins its own row, which an attached client needs.
	again, err := r.Join("client:desk", "desk", KindHuman)
	if err != nil || again != human {
		t.Errorf("a human rejoin = id %d err %v, want the same row %d", again, err, human)
	}
}
