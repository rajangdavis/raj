package control

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"
)

// Messages from the user to a connected driver.
//
// The protocol is request/response, which means everything on this wire happens
// because a client asked for it and there is no path by which the editor speaks
// first. That is the right shape for reads and edits, where the client is the
// one with a question. It leaves out the one thing a user sitting in front of a
// working agent actually wants: telling it something.
//
// # Why a parked request rather than a push
//
// The obvious answer is a server-initiated frame, and it is the wrong one to
// reach for first. Push means the editor owns delivery — a queue per connection
// that the event thread writes into, a policy for a reader that has stopped
// reading, and a story for what a reconnecting client missed. That is the
// `subscribe` design, and it is worth building for buffer changes, which are
// high-volume and coalescible.
//
// A message is neither. It is a discrete thing a person wrote: it must not be
// coalesced, there will be a handful of them a session, and losing one is worse
// than delivering it late. So `recv` is an ordinary request that simply does
// not answer until there is something to say. No new direction on the wire, no
// subscription state, and cancellation already works — `cancel` names an
// in-flight id and is answered on the reading goroutine, which is exactly what
// abandoning a parked read needs.
//
// # Addressed by author id, not by connection
//
// A mailbox belongs to a participant, and participants outlive connections: a
// harness that reconnects with the same identity gets the same author id back,
// so a message sent while it was restarting is still there when it returns.
// Keying on the connection instead would drop it, which is the failure the
// participant registry was built to avoid for text and is no more acceptable
// here.
//
// # Only one parked reader at a time
//
// A channel with several goroutines blocked on the same receive delivers each
// value to exactly one of them, chosen at random. A harness that has, by
// accident of its own process model, more than one connection parked on `recv`
// for the same identity (2026-09-27: several opencode plugin instances each
// parking a reader for one driver key) is not fanned out to — it is a lottery,
// and the instances that lose never see the message at all. That failure mode
// looks identical to a dropped message and cost a long incident to diagnose.
//
// The fix is not to fan a message out to every parked reader (a driver that
// cannot tell which of its own instances is "the" session would just process
// it more than once) but to guarantee there is at most one: a new `recv` for an
// identity preempts an earlier one still parked for the same identity, handing
// it ErrSuperseded instead of silence. A driver that sees ErrSuperseded knows
// unambiguously that a newer connection has taken over its mailbox and it
// should stop reading, rather than a plain "cancelled" that looks identical to
// its own shutdown.

// Message is one thing the user said, and who it is for is implied by the
// mailbox it sits in.
type Message struct {
	// From is the author id of the sender: AuthorUser when the human typed it,
	// AuthorOriginal for an automatic notice the editor enqueued, and a peer
	// driver's own id for a `send` (Server.SendFrom, which takes it from the
	// connection, never the frame). Recorded rather than assumed, so a driver
	// can tell a person's message from the editor's or another agent's, and a
	// second human is still an ordinary participant.
	From uint8  `json:"from"`
	Text string `json:"text"`

	// FromKey and FromName are the sender's durable reply target: the identity
	// a later `send --to` names and the display name to label it with. They
	// are recorded on the message rather than looked up from the participant
	// list at recv time, because the sender may have gone, renamed, or been
	// replayed from the store by the time the message is handed over.
	FromKey  string `json:"from_key,omitempty"`
	FromName string `json:"from_name,omitempty"`

	// rowID is the durable row this message was loaded from, zero for one that
	// was never persisted. It rides with the value so recv can name the rows it
	// handed out and confirm them when the same identity parks again.
	rowID int64
}

// MailboxDepth is how many unread messages one participant can hold.
//
// Small on purpose. A driver that is reading drains this immediately, so depth
// only matters for one that is not — and a user who has typed sixteen unread
// messages at a silent agent has a problem that a larger buffer would hide
// rather than solve. Refusing the seventeenth, visibly, is the honest answer.
const MailboxDepth = 16

// StoredMessage is one persisted mail row as the mailbox reads it back, with
// the durable reply target the wire Message needs. It is the control package's
// own shape so the store stays a leaf and the app converts.
type StoredMessage struct {
	ID       int64
	From     uint8
	FromKey  string
	FromName string
	Text     string
}

// MailStore is the mailbox's durable backing: one row per undelivered message,
// keyed by the recipient's durable identity. The app sets it when the
// workspace has a store; a nil store leaves the mailbox exactly as it was, in
// memory, which is what tests and a storeless workspace get.
type MailStore interface {
	// InsertMail stores one message for toIdentity and returns its row id.
	InsertMail(toIdentity string, from uint8, fromKey, fromName, text string, createdMS int64) (int64, error)
	// MarkMailDelivered stamps the rows ids as handed to their recipient.
	MarkMailDelivered(ids []int64) error
	// LoadUndeliveredMail returns identity's unread mail, oldest first.
	LoadUndeliveredMail(toIdentity string) ([]StoredMessage, error)
	// PruneDeliveredMail removes rows a recipient has already confirmed.
	PruneDeliveredMail() error
}

// ErrSuperseded is the reason a parked recv is cancelled when a later recv
// arrives for the same author id. It is distinct from an ordinary `cancel` so
// the loser can tell "someone else is reading my mail now" from "I was asked
// to stop" — the first means a peer connection has taken over and this one
// should not re-park, the second means nothing about ownership changed.
var ErrSuperseded = errors.New("superseded by a newer recv for this identity")

// Mailbox holds undelivered messages per recipient.
//
// A buffered channel per participant rather than a slice and a condition
// variable: Post must never block, because it is called from the editor's event
// thread, and Wait must be cancellable, which is a select. Both fall out of a
// channel; neither is free with a mutex alone.
type Mailbox struct {
	mu    sync.Mutex
	boxes map[uint8]chan Message
	// waiters holds the sole active parked reader for each author id that has
	// one, so a second Park for the same id can preempt the first instead of
	// leaving two goroutines racing one channel. gen breaks the ABA case where
	// the reader Unpark is releasing has already been preempted and replaced:
	// Unpark only clears an entry it still recognises as its own.
	waiters map[uint8]waiterEntry
	gen     uint64
	// store and reg are the durable half: a nil store means the mailbox is
	// the in-memory one it has always been, and reg resolves a recipient id
	// to its identity on Post and an identity to its id on replay.
	store MailStore
	reg   *Registry
	// handed holds the row ids most recently handed to each identity, waiting
	// for that identity to park again and confirm them. inflight holds every
	// row id loaded into a box or handed but not yet confirmed, so a reconnect
	// does not replay a row this process is still holding.
	handed   map[uint8][]int64
	inflight map[uint8]map[int64]bool
}

type waiterEntry struct {
	gen    uint64
	cancel context.CancelCauseFunc
}

func (m *Mailbox) box(to uint8) chan Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.boxes == nil {
		m.boxes = map[uint8]chan Message{}
	}
	ch, ok := m.boxes[to]
	if !ok {
		ch = make(chan Message, MailboxDepth)
		m.boxes[to] = ch
	}
	return ch
}

// Park registers cancel as the sole parked reader for to, preempting — with
// ErrSuperseded — whatever reader was parked for the same author id before it.
// The returned token identifies this reader to Unpark, which must be called
// once the read ends, win or lose, or a preempted reader's stale entry would
// never be replaced by a call that has nothing left to preempt.
func (m *Mailbox) Park(to uint8, cancel context.CancelCauseFunc) uint64 {
	m.mu.Lock()
	// A fresh park for this identity confirms the batch it was handed before:
	// that is the at-least-once marker. The rows leave inflight under the lock
	// and the store update happens after it, off the event thread's path.
	confirm := m.handed[to]
	delete(m.handed, to)
	if len(confirm) > 0 {
		if set := m.inflight[to]; set != nil {
			for _, id := range confirm {
				delete(set, id)
			}
			if len(set) == 0 {
				delete(m.inflight, to)
			}
		}
	}
	if prev, ok := m.waiters[to]; ok {
		prev.cancel(ErrSuperseded)
	}
	if m.waiters == nil {
		m.waiters = map[uint8]waiterEntry{}
	}
	m.gen++
	token := m.gen
	m.waiters[to] = waiterEntry{gen: token, cancel: cancel}
	m.mu.Unlock()
	if len(confirm) > 0 && m.store != nil {
		if err := m.store.MarkMailDelivered(confirm); err != nil {
			log.Printf("raj: mailbox: confirm delivery: %v", err)
		}
	}
	return token
}

// Unpark releases a reader Park returned token for. It is a no-op if a later
// Park has already preempted and replaced this entry — the newer reader owns
// the slot now, and clearing it out from under that reader would let a third,
// even later Park skip the preemption it is owed.
func (m *Mailbox) Unpark(to uint8, token uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cur, ok := m.waiters[to]; ok && cur.gen == token {
		delete(m.waiters, to)
	}
}

// Post queues a message. It never blocks.
//
// A full mailbox is an error and not a silent drop. The caller is the editor,
// which has a status line and a person looking at it, and "the agent has not
// read the last sixteen things you said" is information that person wants.
func (m *Mailbox) Post(to uint8, msg Message) error {
	ch := m.box(to)
	if len(ch) >= MailboxDepth {
		return fmt.Errorf("mailbox for author %d is full (%d unread)", to, MailboxDepth)
	}
	// A notice is the editor talking to itself and is deliberately ephemeral;
	// a send is a durable thing a person or peer wrote. Persistence is keyed on
	// the recipient's identity, not the id, because a restart re-seeds ids.
	if m.store != nil && msg.From != AuthorOriginal {
		if identity := m.identityFor(to); identity != "" {
			rowID, err := m.store.InsertMail(identity, msg.From, msg.FromKey, msg.FromName, msg.Text, time.Now().UnixMilli())
			if err != nil {
				// Delivery still happens in process; only a restart loses it.
				log.Printf("raj: mailbox: persist message for %s: %v", identity, err)
			} else {
				msg.rowID = rowID
			}
		}
	}
	select {
	case ch <- msg:
		if msg.rowID != 0 {
			m.noteInflight(to, msg.rowID)
		}
		return nil
	default:
		// The box filled between the check above and the send. The row was
		// already persisted, so discard it rather than let a message this
		// mailbox refused replay later.
		if msg.rowID != 0 {
			if err := m.store.MarkMailDelivered([]int64{msg.rowID}); err != nil {
				log.Printf("raj: mailbox: discard refused message: %v", err)
			}
		}
		return fmt.Errorf("mailbox for author %d is full (%d unread)", to, MailboxDepth)
	}
}

// Wait blocks until at least one message is available for to, then returns
// everything queued for it.
//
// Everything, not one: a driver that has been busy should learn what it missed
// in a single round trip, and the messages were written in an order that is
// worth preserving rather than interleaving with its own work. It returns false
// when ctx ends first, which is a cancelled or dropped request rather than an
// error worth reporting.
func (m *Mailbox) Wait(ctx context.Context, to uint8) ([]Message, bool) {
	ch := m.box(to)
	select {
	case first := <-ch:
		out := []Message{first}
		for {
			select {
			case next := <-ch:
				out = append(out, next)
			default:
				m.noteHanded(to, out)
				return out, true
			}
		}
	case <-ctx.Done():
		return nil, false
	}
}

// Unread is how many messages are waiting for a recipient. For the status line
// and for tests; nothing about delivery depends on it.
func (m *Mailbox) Unread(to uint8) int { return len(m.box(to)) }

// SetStore installs the durable backing and the registry that resolves
// identities to author ids. A nil store leaves the mailbox in memory, which is
// the case for a storeless workspace and for every unit test. It is called
// once, before any connection is served.
func (m *Mailbox) SetStore(store MailStore, reg *Registry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.store, m.reg = store, reg
}

// identityFor returns the durable identity for an author id, or "" when the id
// has no registry row (a provisional connection) or there is no registry.
func (m *Mailbox) identityFor(id uint8) string {
	if m.reg == nil {
		return ""
	}
	if p, ok := m.reg.Get(id); ok {
		return p.Identity
	}
	return ""
}

// idFor resolves a durable identity to its author id through the registry.
func (m *Mailbox) idFor(identity string) (uint8, bool) {
	if m.reg == nil {
		return 0, false
	}
	for _, p := range m.reg.List() {
		if p.Identity == identity {
			return p.ID, true
		}
	}
	return 0, false
}

// noteInflight records row ids now sitting in an identity's box.
func (m *Mailbox) noteInflight(to uint8, ids ...int64) {
	if len(ids) == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.inflight == nil {
		m.inflight = map[uint8]map[int64]bool{}
	}
	set := m.inflight[to]
	if set == nil {
		set = map[int64]bool{}
		m.inflight[to] = set
	}
	for _, id := range ids {
		set[id] = true
	}
}

// noteHanded records the row ids just handed to the client so the next Park
// for the same identity can confirm them. It takes the ids from the messages
// themselves rather than the whole inflight set, so a message posted after the
// drain is not confirmed before it is read.
func (m *Mailbox) noteHanded(to uint8, msgs []Message) {
	var ids []int64
	for _, msg := range msgs {
		if msg.rowID != 0 {
			ids = append(ids, msg.rowID)
		}
	}
	if len(ids) == 0 {
		return
	}
	m.mu.Lock()
	if m.handed == nil {
		m.handed = map[uint8][]int64{}
	}
	m.handed[to] = append(m.handed[to], ids...)
	m.mu.Unlock()

}

// Replay fills the mailboxes from the store. It runs once at start, after the
// participant registry is seeded, so an editor restart hands a participant its
// unread mail when it next parks. A row for an identity the registry does not
// know yet is left until ReplayIdentity sees that identity join.
func (m *Mailbox) Replay() {
	if m.store == nil || m.reg == nil {
		return
	}
	for _, p := range m.reg.List() {
		m.ReplayIdentity(p.Identity)
	}
}

// ReplayIdentity loads and queues the undelivered mail for one identity. It is
// called at start for each seeded participant and again when an identity joins,
// so mail whose recipient was unknown at start is delivered when it arrives.
// Rows already loaded into a box (or handed and not yet confirmed) are skipped,
// so a reconnect does not replay what this process is still holding, while a
// restart does.
func (m *Mailbox) ReplayIdentity(identity string) {
	if m.store == nil || m.reg == nil || identity == "" {
		return
	}
	to, ok := m.idFor(identity)
	if !ok {
		return
	}
	rows, err := m.store.LoadUndeliveredMail(identity)
	if err != nil {
		log.Printf("raj: mailbox: load mail for %s: %v", identity, err)
		return
	}
	m.mu.Lock()
	held := m.inflight[to]
	fresh := make([]StoredMessage, 0, len(rows))
	for _, r := range rows {
		if held[r.ID] {
			continue
		}
		fresh = append(fresh, r)
	}
	room := MailboxDepth - len(held)
	m.mu.Unlock()
	if room < 0 {
		room = 0
	}
	if len(fresh) > room {
		// The bound holds on restore as it does on insert: rows beyond what a
		// box can hold are dropped oldest-first and marked delivered, so they
		// are pruned rather than replayed and dropped again at the next start.
		drop := fresh[:len(fresh)-room]
		fresh = fresh[len(fresh)-room:]
		ids := make([]int64, 0, len(drop))
		for _, r := range drop {
			ids = append(ids, r.ID)
		}
		log.Printf("raj: mailbox: dropped %d oldest undelivered message(s) for %s over the %d bound", len(ids), identity, MailboxDepth)
		if err := m.store.MarkMailDelivered(ids); err != nil {
			log.Printf("raj: mailbox: drop overflow for %s: %v", identity, err)
		}
	}
	for _, r := range fresh {
		msg := Message{From: r.From, FromKey: r.FromKey, FromName: r.FromName, Text: r.Text, rowID: r.ID}
		select {
		case m.box(to) <- msg:
			m.noteInflight(to, r.ID)
		default:
			// A concurrent poster filled the box; the row stays undelivered
			// and replays at the next start rather than being lost.
			return
		}
	}
}

// Prune drops the rows a recipient has already confirmed. It runs at start,
// before Replay, so a delivered marker that outlived its process does not
// accumulate.
func (m *Mailbox) Prune() {
	if m.store == nil {
		return
	}
	if err := m.store.PruneDeliveredMail(); err != nil {
		log.Printf("raj: mailbox: prune delivered mail: %v", err)
	}
}
