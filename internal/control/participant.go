package control

import (
	"fmt"
	"sort"
	"sync"
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
	// Connected is whether anything is currently attached as this participant.
	// A disconnected one is still listed: its text is in the document, so a
	// reader still needs to know whose it is.
	Connected bool `json:"connected"`
}

// LocalHuman is the person at the keyboard, and the first participant. Nothing
// checks against this constant — it is the first row rather than a special
// case, which is what lets a second human be an ordinary row too.
const LocalHuman uint8 = 1

// MaxParticipants is the ceiling the one-byte author id imposes.
const MaxParticipants = 255

// Registry maps identities to author ids.
type Registry struct {
	mu         sync.Mutex
	byID       map[uint8]*Participant
	byIdentity map[string]uint8
	next       uint8
}

// NewRegistry returns a registry holding only the local human.
func NewRegistry() *Registry {
	r := &Registry{byID: map[uint8]*Participant{}, byIdentity: map[string]uint8{}, next: LocalHuman + 1}
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
func (r *Registry) Join(identity, name string, kind Kind) (uint8, error) {
	if identity == "" {
		return 0, fmt.Errorf("participant: an identity is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if id, ok := r.byIdentity[identity]; ok {
		p := r.byID[id]
		p.Connected = true
		if name != "" {
			p.Name = name
		}
		return id, nil
	}
	if name == "" {
		name = identity
	}
	id := r.next
	if int(r.next) > MaxParticipants || r.next == 0 {
		// The one-byte space is full — of rows, not of writers. A participant
		// that disconnected left its row behind so its text still has an owner
		// to name, and that row is the one id that can be handed out again:
		// the lowest gone id, so two joins racing the cap pick the same one.
		// The old identity is forgotten and its text now reads as the new
		// writer. Refuse only when nobody is gone.
		gone, ok := lowestGone(r.byID)
		if !ok {
			return 0, fmt.Errorf("participant: no author ids left (%d used)", len(r.byID))
		}
		delete(r.byIdentity, r.byID[gone].Identity)
		id = gone
	} else {
		r.next++
	}
	r.byID[id] = &Participant{ID: id, Identity: identity, Name: name, Kind: kind, Connected: true}
	r.byIdentity[identity] = id
	return id, nil
}

// lowestGone finds the lowest recyclable author id: one whose participant has
// disconnected. The scan starts past the local human — an agent handed id 1
// would write text indistinguishable from typed — and a connected row is
// never taken, because that id is somebody writing right now.
func lowestGone(byID map[uint8]*Participant) (uint8, bool) {
	for id := uint8(LocalHuman + 1); ; id++ {
		if p, ok := byID[id]; ok && !p.Connected {
			return id, true
		}
		if id == MaxParticipants {
			return 0, false
		}
	}
}

// Leave marks a participant disconnected. The row stays: its text is still in
// the document, so a reader still needs to know whose it is.
func (r *Registry) Leave(id uint8) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p := r.byID[id]; p != nil {
		p.Connected = false
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

// IsAgent reports whether an author id belongs to an agent.
//
// A lookup, not `id >= 2`. That comparison is what made a second human
// impossible: the id number cannot say what kind of participant it is once more
// than one person can write.
func (r *Registry) IsAgent(id uint8) bool {
	p, ok := r.Get(id)
	return ok && p.Kind == KindAgent
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
