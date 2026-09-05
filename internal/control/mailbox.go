package control

import (
	"context"
	"fmt"
	"sync"
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

// Message is one thing the user said, and who it is for is implied by the
// mailbox it sits in.
type Message struct {
	// From is the author id of the sender. Always the local human today —
	// nothing else can reach Post — but recorded rather than assumed, because
	// a second human is an ordinary participant and this should not be the
	// place that has to change when one arrives.
	From uint8 `json:"from"`
	Text string `json:"text"`
}

// MailboxDepth is how many unread messages one participant can hold.
//
// Small on purpose. A driver that is reading drains this immediately, so depth
// only matters for one that is not — and a user who has typed sixteen unread
// messages at a silent agent has a problem that a larger buffer would hide
// rather than solve. Refusing the seventeenth, visibly, is the honest answer.
const MailboxDepth = 16

// Mailbox holds undelivered messages per recipient.
//
// A buffered channel per participant rather than a slice and a condition
// variable: Post must never block, because it is called from the editor's event
// thread, and Wait must be cancellable, which is a select. Both fall out of a
// channel; neither is free with a mutex alone.
type Mailbox struct {
	mu    sync.Mutex
	boxes map[uint8]chan Message
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

// Post queues a message. It never blocks.
//
// A full mailbox is an error and not a silent drop. The caller is the editor,
// which has a status line and a person looking at it, and "the agent has not
// read the last sixteen things you said" is information that person wants.
func (m *Mailbox) Post(to uint8, msg Message) error {
	select {
	case m.box(to) <- msg:
		return nil
	default:
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
