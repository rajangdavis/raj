package control

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

// Who is writing, as distinct from which author id their text carries.
//
// Author was identity: an id was minted per connection, so a harness that
// reconnected became a different writer and its earlier text became somebody
// else's. It also fixed the cast — id 1 is the human, everything above is an
// agent — which leaves no room for a second person editing the same workspace.
//
// Splitting the two costs one table. The piece table keeps Author as an opaque
// byte and nothing in the store, the rebase, the undo stacks or the spans
// changes. Beside it sits a registry mapping a durable identity to that byte,
// so the same identity reconnecting gets the same id back, and a row says what
// kind of participant it is rather than the number implying it.
//
// Three consequences fall out, which is the sign the cut is in the right place:
// ids stop being exhausted by reconnections, attribution can outlive the
// process because the table is small enough to persist, and "is this an agent"
// becomes a lookup instead of `>= 2` — which is what makes a second human
// possible at all.
//
// Author 0 is not in here. It is the file as loaded: not a person, with no name
// and no session, unable to own a proposal or appear in a list of who is here.
// Participants start at 1, where people start.

// Kind is what a participant is. It is stored rather than inferred from the id,
// so that adding a second human is a row and not a renumbering.
type Kind string

const (
	KindHuman Kind = "human"
	KindAgent Kind = "agent"
)

// The working-state vocabulary. A participant declares working, blocked,
// review or idle through `state set`; the rest raj derives from activity it
// already sees. Declared wins over derived, except that a declared working
// state gone quiet reads stale and a closed connection reads gone.
const (
	StateListening = "listening"
	StateWorking   = "working"
	StateWaiting   = "waiting"
	StateStale     = "stale"
	StateGone      = "gone"
	StateIdle      = "idle"
	StateBlocked   = "blocked"
	StateReview    = "review"
)

// The windows a derived state reads. activeWindow is how long after its last
// request a participant still counts as working; staleWindow is how long a
// participant that declared itself working may then go quiet before the
// declaration itself is doubted.
const (
	activeWindow = 30 * time.Second
	staleWindow  = 10 * time.Minute
)

// declared is the state a participant reported through `state set`, with the
// optional counterpart and note that go with it.
type declared struct {
	state string
	on    string
	note  string
}

// Participant is one writer.
type Participant struct {
	// ID is the author byte its text carries in the piece table.
	ID uint8 `json:"id"`
	// Identity is durable across connections: a harness session id, or a user
	// name. Reconnecting with the same identity returns the same ID.
	Identity string `json:"identity"`
	// Name is for display — a gutter label, a legend. Short.
	Name string `json:"name"`
	Kind Kind   `json:"kind"`
	// Task is the work this participant's writes belong to, set by `hello` and
	// carried on the groups those writes open. It is empty for the local human
	// and for a connection that registered no task.
	Task string `json:"task,omitempty"`
	// Connected is whether anything is currently attached as this participant.
	// A disconnected one is still listed: its text is in the document, so a
	// reader still needs to know whose it is.
	Connected bool `json:"connected"`
	// State is the working state shown for this participant: the declared one
	// when it has one, else the state raj derives from activity. It is filled
	// by States, not stored on the row, and empty on a plain List.
	State string `json:"state,omitempty"`
	// Declared is the state the participant reported, empty when none.
	Declared string `json:"declared,omitempty"`
	// SinceMS is milliseconds since the participant's last request, or -1
	// when it has made none.
	SinceMS int64 `json:"since_ms"`
	// Note and On are the declared state's optional text and counterpart
	// (the user or another participant's key).
	Note string `json:"note,omitempty"`
	On   string `json:"on,omitempty"`
}

// LocalHuman is the person at the keyboard, and the first participant. Nothing
// checks against this constant — it is the first row rather than a special
// case, which is what lets a second human be an ordinary row too.
const LocalHuman uint8 = 1

// MaxParticipants is the ceiling the one-byte author id imposes.
const MaxParticipants = 255

// reservedIdentities are the names the registry keeps for the local human. The
// keyboard row is seeded with identity "local" and display name "you"; a hello
// naming either is refused before any lookup, so a client holding the shared
// TCP token cannot rebind itself onto the human row and then pass the human
// gate for save, accept and land.
var reservedIdentities = map[string]bool{
	"local": true,
	"you":   true,
}

// Registry is the author table, and it holds two kinds of row.
//
// The first is a durable identity — a harness session id, a user name — mapped
// to an author id with a Participant row. Reconnecting with the same identity
// claims that same id, so its text keeps an owner that is named.
//
// The second is a reserved author id: a live connection that has not declared
// an identity yet. It has no Participant row, because it is not a writer — no
// name, no kind, nothing in the list of who is here — only a byte held for the
// connection until it either says hello and rebinds to a durable row, or goes
// away. That is what keeps a one-off client from leaving a row behind for every
// invocation.
type Registry struct {
	mu         sync.Mutex
	byID       map[uint8]*Participant
	byIdentity map[string]uint8
	reserved   map[uint8]bool
	// conns counts the live connections bound to each durable row. One
	// participant routinely holds several at once — an agent's parked recv
	// plus every short `raj ctl` call under the same key — so Connected is
	// "at least one is open", not "the last one to report". Kept off
	// Participant so the wire record does not change.
	conns map[uint8]int
	next  uint16
	// The state surface. lastActive is when each participant last sent a
	// request and declared is what it reported; listening counts its parked
	// recvs and pending says it holds a proposal. All are the raw facts state
	// derivation reads, kept off Participant so a plain List does not compute
	// them. now is the clock, replaced in a test to move the windows without
	// sleeping.
	lastActive map[uint8]time.Time
	declared   map[uint8]declared
	listening  map[uint8]int
	pending    map[uint8]bool
	now        func() time.Time
	// joined, when set, is told the author id of a Join that installs a new row
	// or changes one. It is how the editor persists the author table without
	// the control package having to reach into it: Join runs on a connection
	// goroutine and only reports the id, and the editor writes the row on its
	// own thread.
	joined func(uint8)
}

// NewRegistry returns a registry holding only the local human.
func NewRegistry() *Registry {
	r := &Registry{byID: map[uint8]*Participant{}, byIdentity: map[string]uint8{},
		reserved: map[uint8]bool{}, conns: map[uint8]int{},
		lastActive: map[uint8]time.Time{}, declared: map[uint8]declared{},
		listening: map[uint8]int{}, pending: map[uint8]bool{},
		now: time.Now, next: uint16(LocalHuman) + 1}

	local := &Participant{ID: LocalHuman, Identity: "local", Name: "you",
		Kind: KindHuman, Connected: true}
	r.byID[LocalHuman] = local
	r.byIdentity[local.Identity] = LocalHuman
	return r
}

// Join returns the author id for an identity, creating a row the first time.
//
// The same identity always gets the same id back, which is the whole point: a
// harness that restarts mid-session continues to own the text it already wrote,
// instead of orphaning it under an id nothing will ever claim again.
//
// A new identity arriving when the one-byte space is full reuses the lowest
// row a disconnected participant left behind, rather than failing: full of
// rows is not full of writers. Recycling forgets the evicted identity — its
// text now reads as the new writer — so it stays the pressure valve, not the
// first choice: fresh ids go out in order for as long as there are any.
//
// A join that installs a new row, or changes one, is announced to the registry
// joined hook, if one is set, so the editor can persist the author table; see
// setJoined. An ordinary reconnect announces nothing.
func (r *Registry) Join(identity, name string, kind Kind) (uint8, error) {
	if identity == "" {
		return 0, fmt.Errorf("participant: an identity is required")
	}
	r.mu.Lock()
	id, changed, err := r.join(identity, name, kind)
	joined := r.joined
	r.mu.Unlock()
	if err == nil && changed && joined != nil {
		joined(id)
	}
	return id, err
}

// setJoined installs the hook Join announces to, once, when the server is built.
// It takes the registry lock, so a join racing the installation sees either the
// hook or none and never a torn read.
func (r *Registry) setJoined(fn func(uint8)) {
	r.mu.Lock()
	r.joined = fn
	r.mu.Unlock()
}

// join is the body of Join, with the caller holding the lock. The announce
// happens in Join, after the unlock, because the hook reads the registry back
// and would otherwise deadlock on the lock it was called under.
func (r *Registry) join(identity, name string, kind Kind) (uint8, bool, error) {
	if reservedIdentities[identity] {
		return 0, false, fmt.Errorf("participant: %q is a reserved identity", identity)
	}
	if id, ok := r.byIdentity[identity]; ok {
		p := r.byID[id]
		// A human row is the person at the keyboard or an attached human
		// client; a caller that is not claiming to be human (a TCP caller is
		// always forced to an agent kind by serve) must not inherit it. The
		// request kind is otherwise ignored for an existing row, which is the
		// defect: joining "local" or a declared human identity returned the
		// human row and its author id.
		if p.Kind == KindHuman && kind != KindHuman {
			return 0, false, fmt.Errorf("participant: %q is a human identity; only the local socket may join it", identity)
		}
		r.conns[id]++
		p.Connected = true
		// Announce only a row the editor does not already have: an ordinary
		// reconnect changes nothing and must not touch the hot path.
		changed := name != "" && name != p.Name
		if name != "" {
			p.Name = name
		}
		return id, changed, nil
	}
	if name == "" {
		name = identity
	}
	id, ok := r.nextFree()
	if !ok {
		// The one-byte space is full — of rows, not of writers. A participant
		// that disconnected left its row behind so its text still has an owner
		// to name, and that row is the one id that can be handed out again:
		// the lowest gone id, so two joins racing the cap pick the same one.
		// The old identity is forgotten and its text now reads as the new
		// writer. Refuse only when nobody is gone.
		//
		// A reserved id is skipped without a second thought: it has no row,
		// and lowestGone only ever returns one. A reserved id belongs to a
		// live connection, so a durable identity must never be handed it.
		gone, ok := lowestGone(r.byID)
		if !ok {
			return 0, false, fmt.Errorf("participant: no author ids left (%d used)", len(r.byID))
		}
		delete(r.byIdentity, r.byID[gone].Identity)
		id = gone
	}
	r.byID[id] = &Participant{ID: id, Identity: identity, Name: name, Kind: kind, Connected: true}
	r.byIdentity[identity] = id
	// A recycled id starts its count afresh: the row it replaced was gone.
	r.conns[id] = 1
	return id, true, nil
}

// nextFree finds the next author id a durable identity may take: the first id
// at or above next that is neither an existing row nor reserved. A reserved id
// is skipped even though it has no row — it is a live connection that has not
// declared an identity, so handing it to a durable identity would put two
// writers on one author byte. next advances past every id it checks, occupied,
// reserved or free, so a later Join does not scan them again.
func (r *Registry) nextFree() (uint8, bool) {
	for r.next <= MaxParticipants {
		id := uint8(r.next)
		_, occupied := r.byID[id]
		if !occupied && !r.reserved[id] {
			r.next++
			return id, true
		}
		r.next++
	}
	return 0, false
}

// Reserve hands out an author id for a live connection that has not declared an
// identity. The id has no Participant row: it is a connection, not a writer, so
// it does not appear in List, owns no text and cannot be claimed by a durable
// identity. The connection releases it with Release when it either binds an
// identity or ends, which is what keeps a one-off client from leaving a row
// behind per invocation.
//
// It returns the lowest id that is neither an existing row nor already
// reserved. Join's recycling pressure valve does not apply here: a reserved id
// belongs to a live connection and cannot be evicted, so running out is an
// error rather than a collision.
func (r *Registry) Reserve() (uint8, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id := uint16(FirstAgent); id <= MaxParticipants; id++ {
		if _, occupied := r.byID[uint8(id)]; occupied || r.reserved[uint8(id)] {
			continue
		}
		r.reserved[uint8(id)] = true
		return uint8(id), nil
	}
	return 0, fmt.Errorf("participant: no author ids available")
}

// Release gives back an id handed out by Reserve: the provisional id of a
// connection that has bound a durable identity, or one that is ending. It is a
// no-op when the id was not reserved. Release is not Leave: it must never be
// used on a durable row, which stays for attribution when its writer goes.
func (r *Registry) Release(id uint8) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.reserved, id)
}

// Seed installs a participant row under an explicit author id, for a registry
// rebuilt from a persisted author table. It is not Join: the id is the one the
// log already used, so restored text resolves to the identity and tint it was
// written under instead of whatever join order a fresh registry would hand out.
//
// A row for the id or the identity already present is left as it is, a
// restored row is never Connected, and next moves past the highest seeded id.
// It returns whether the row was installed.
func (r *Registry) Seed(p Participant) bool {
	if p.ID == 0 || p.Identity == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.byID[p.ID]; exists {
		return false
	}
	if _, exists := r.byIdentity[p.Identity]; exists {
		return false
	}
	p.Connected = false
	r.byID[p.ID] = &p
	r.byIdentity[p.Identity] = p.ID
	// next is one past the highest seeded id. Seeding id MaxParticipants
	// makes it MaxParticipants+1, which Join reads as full — the counter can
	// hold that now, so fullness needs no second sentinel.
	if uint16(p.ID) >= r.next {
		r.next = uint16(p.ID) + 1
	}
	return true
}

// lowestGone finds the lowest recyclable author id: one whose participant has
// disconnected. The scan starts past the local human — an agent handed id 1
// would write text indistinguishable from typed — and a connected row is
// never taken, because that id is somebody writing right now.
//
// A reserved id is skipped for free: it has no row at all, and this only ever
// returns one. That is the invariant that lets Join hand a recycled id to a
// durable identity without colliding with a live, undeclared connection.
func lowestGone(byID map[uint8]*Participant) (uint8, bool) {
	for id := uint16(LocalHuman) + 1; id <= MaxParticipants; id++ {
		if p, ok := byID[uint8(id)]; ok && !p.Connected {
			return uint8(id), true
		}
	}
	return 0, false
}

// Leave marks a participant disconnected. The row stays: its text is still in
// the document, so a reader still needs to know whose it is.
//
// A reserved id has no row, so Leave on a provisional connection's id is a
// harmless no-op — Release is what frees a reservation.
func (r *Registry) Leave(id uint8) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p := r.byID[id]; p != nil {
		// One connection closing is not the participant leaving: an agent's
		// parked recv outlives every short call made under the same key, and
		// marking the row gone on the first close made `send --to all` find
		// nobody while the agent was still listening.
		if r.conns[id] > 0 {
			r.conns[id]--
		}
		p.Connected = r.conns[id] > 0
	}
}

// Get returns a participant by author id.
func (r *Registry) Get(id uint8) (Participant, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.byID[id]
	if !ok {
		return Participant{}, false
	}
	return *p, true
}

// SetTask records the task this participant's writes belong to. An empty task
// leaves the row as it is: every ordinary command reconnects and says hello
// with no task, and that reconnect must not erase the task its register call
// pinned. A non-empty task replaces it, so a register for a different task
// moves the participant's future writes under it; sets already recorded keep
// the task they were opened under, which is the journal's own record.
func (r *Registry) SetTask(id uint8, task string) {
	if task == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if p := r.byID[id]; p != nil {
		p.Task = task
	}
}

// IsAgent reports whether an author id writes as an agent.
//
// A registry lookup, not `id >= 2`: the id number cannot say what kind of
// participant it is once more than one person can write. A reserved id — a live
// connection that has not declared an identity — counts as an agent, because it
// is a socket client rather than a person; classifying it as the human would
// compose its edit into the document instead of holding it as a proposal. A
// durable row's own Kind decides.
func (r *Registry) IsAgent(id uint8) bool {
	if p, ok := r.Get(id); ok {
		return p.Kind == KindAgent
	}
	return r.IsProvisional(id)
}

// IsProvisional reports whether an author id is a live connection that has not
// declared an identity. It has no registry row, so it is not listed and is not
// a durable writer, but it is still an agent for the write gate: the local
// socket's permissions, or the TCP token, already decided it may connect, and a
// connection that never declared itself is the pre-registry anonymous case that
// the write gate used to admit.
func (r *Registry) IsProvisional(id uint8) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reserved[id]
}

// List returns every participant, connected or not, by id.
func (r *Registry) List() []Participant {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Participant, 0, len(r.byID))
	for _, p := range r.byID {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Touch records a request from id at the registry's clock. A reserved id has
// no row, so touching one is a no-op: it is a connection, not a writer.
func (r *Registry) Touch(id uint8) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.byID[id]; ok {
		r.lastActive[id] = r.now()
	}
}

// SetListening marks a parked recv starting (on) or finishing (!on) for id.
// The count is what lets a participant hold several connections at once and
// only read as listening while at least one is parked.
func (r *Registry) SetListening(id uint8, on bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if on {
		r.listening[id]++
		return
	}
	if r.listening[id] > 0 {
		r.listening[id]--
	}
}

// SetState records the state id declared through `state set`, with its
// optional counterpart and note. An empty state clears the declaration.
func (r *Registry) SetState(id uint8, state, on, note string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.byID[id]; !ok {
		return
	}
	if state == "" {
		delete(r.declared, id)
		return
	}
	r.declared[id] = declared{state: state, on: on, note: note}
}

// SetPending replaces the set of authors that hold a pending proposal, the
// fact behind the derived waiting state. The app owns the pending set and
// pushes it here; connection goroutines only read it.
func (r *Registry) SetPending(held map[uint8]bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pending = make(map[uint8]bool, len(held))
	for id := range held {
		r.pending[id] = true
	}
}

// States returns every participant with its working state filled, for who and
// the hello reply. hookRunning reports whether id has a hook run in flight;
// nil means no run can be observed.
func (r *Registry) States(hookRunning func(uint8) bool) []Participant {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	out := make([]Participant, 0, len(r.byID))
	for _, p := range r.byID {
		q := *p
		q.State, q.Declared, q.SinceMS, q.On, q.Note = r.stateOf(p, now, hookRunning)
		out = append(out, q)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// stateOf computes one participant's state. A closed connection is gone
// whatever it declared; a declared working state gone quiet is stale; anything
// else declared wins over what activity would say. Derived, in order: a hook
// run in flight or a request inside activeWindow is working, a parked recv is
// listening, a held proposal is waiting, and the rest is idle.
func (r *Registry) stateOf(p *Participant, now time.Time, hookRunning func(uint8) bool) (state, declaredState string, sinceMS int64, on, note string) {
	sinceMS = -1
	if last, ok := r.lastActive[p.ID]; ok {
		sinceMS = now.Sub(last).Milliseconds()
	}
	d := r.declared[p.ID]
	declaredState, on, note = d.state, d.on, d.note
	if !p.Connected {
		return StateGone, declaredState, sinceMS, on, note
	}
	if d.state == StateWorking && sinceMS >= int64(staleWindow/time.Millisecond) {
		return StateStale, declaredState, sinceMS, on, note
	}
	if d.state != "" {
		return d.state, declaredState, sinceMS, on, note
	}
	if hookRunning != nil && hookRunning(p.ID) {
		return StateWorking, declaredState, sinceMS, on, note
	}
	if sinceMS >= 0 && sinceMS < int64(activeWindow/time.Millisecond) {
		return StateWorking, declaredState, sinceMS, on, note
	}
	if r.listening[p.ID] > 0 {
		return StateListening, declaredState, sinceMS, on, note
	}
	if r.pending[p.ID] {
		return StateWaiting, declaredState, sinceMS, on, note
	}
	return StateIdle, declaredState, sinceMS, on, note
}
