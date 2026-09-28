package control

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestMailboxDeliversInOrder(t *testing.T) {
	var m Mailbox
	for _, s := range []string{"one", "two", "three"} {
		if err := m.Post(7, Message{From: AuthorUser, Text: s}); err != nil {
			t.Fatalf("post %q: %v", s, err)
		}
	}
	if got := m.Unread(7); got != 3 {
		t.Errorf("unread = %d, want 3", got)
	}

	// One Wait drains everything queued: a driver that was busy learns what it
	// missed in a single round trip, in the order it was written.
	msgs, ok := m.Wait(context.Background(), 7)
	if !ok {
		t.Fatal("wait reported nothing")
	}
	if len(msgs) != 3 || msgs[0].Text != "one" || msgs[2].Text != "three" {
		t.Fatalf("messages = %+v", msgs)
	}
	if got := m.Unread(7); got != 0 {
		t.Errorf("unread = %d after draining", got)
	}
}

// Wait parks. This is the whole mechanism: the request answers when there is
// something to say, not when it was asked.
func TestMailboxWaitBlocksUntilPosted(t *testing.T) {
	var m Mailbox
	done := make(chan []Message, 1)
	go func() {
		msgs, _ := m.Wait(context.Background(), 2)
		done <- msgs
	}()

	select {
	case msgs := <-done:
		t.Fatalf("wait returned %+v before anything was posted", msgs)
	case <-time.After(20 * time.Millisecond):
	}

	if err := m.Post(2, Message{From: AuthorUser, Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	select {
	case msgs := <-done:
		if len(msgs) != 1 || msgs[0].Text != "hello" {
			t.Errorf("messages = %+v", msgs)
		}
	case <-time.After(time.Second):
		t.Fatal("wait did not wake")
	}
}

// A cancelled wait reports no message rather than an error. It is a request the
// caller abandoned, not a failure.
func TestMailboxWaitIsCancellable(t *testing.T) {
	var m Mailbox
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if msgs, ok := m.Wait(ctx, 3); ok || msgs != nil {
		t.Errorf("wait = %+v, %v; want nothing", msgs, ok)
	}
}

// Post never blocks, so a full mailbox has to be an error. Silently dropping
// would lose something a person typed.
func TestMailboxRefusesWhenFull(t *testing.T) {
	var m Mailbox
	for i := 0; i < MailboxDepth; i++ {
		if err := m.Post(4, Message{Text: "x"}); err != nil {
			t.Fatalf("post %d: %v", i, err)
		}
	}
	if err := m.Post(4, Message{Text: "one too many"}); err == nil {
		t.Error("a full mailbox accepted another message")
	}
	// The queued ones are untouched: refusing must not cost a message that was
	// already accepted.
	if got := m.Unread(4); got != MailboxDepth {
		t.Errorf("unread = %d, want %d", got, MailboxDepth)
	}
}

// Mailboxes are per recipient. Two drivers connected at once must not read one
// another's mail.
func TestMailboxesAreSeparate(t *testing.T) {
	var m Mailbox
	if err := m.Post(2, Message{Text: "for two"}); err != nil {
		t.Fatal(err)
	}
	if got := m.Unread(3); got != 0 {
		t.Fatalf("author 3 has %d messages", got)
	}
	msgs, ok := m.Wait(context.Background(), 2)
	if !ok || len(msgs) != 1 || msgs[0].Text != "for two" {
		t.Errorf("messages = %+v, %v", msgs, ok)
	}
}

// A second Park for the same author id preempts the first with ErrSuperseded,
// rather than leaving both goroutines racing one channel where a Post would
// reach whichever happened to win the select. This is the incident from
// 2026-09-27: several parked readers under one identity silently dropped
// messages for whichever of them did not get lucky.
func TestMailboxParkPreemptsAnEarlierWaiter(t *testing.T) {
	var m Mailbox
	ctx1, stop1 := context.WithCancelCause(context.Background())
	defer stop1(nil)
	token1 := m.Park(9, stop1)

	ctx2, stop2 := context.WithCancelCause(context.Background())
	defer stop2(nil)
	_ = m.Park(9, stop2)

	select {
	case <-ctx1.Done():
	case <-time.After(time.Second):
		t.Fatal("the earlier parked reader was not preempted")
	}
	if err := context.Cause(ctx1); !errors.Is(err, ErrSuperseded) {
		t.Errorf("cause = %v, want ErrSuperseded", err)
	}
	select {
	case <-ctx2.Done():
		t.Fatal("the newer reader was cancelled too")
	default:
	}

	// The preempted reader's Unpark must not clear the newer reader's slot: a
	// third Park still has something to preempt.
	m.Unpark(9, token1)
	_, stop3 := context.WithCancelCause(context.Background())
	defer stop3(nil)
	m.Park(9, stop3)
	select {
	case <-ctx2.Done():
	case <-time.After(time.Second):
		t.Fatal("the second reader was not preempted by the third Park")
	}
}

func TestSendFromRefusesAProvisionalSender(t *testing.T) {
	s := &Server{Participants: NewRegistry()}
	recipient, err := s.Participants.Join("raj-recipient", "peer", KindAgent)
	if err != nil {
		t.Fatalf("join recipient: %v", err)
	}
	provisional, err := s.Participants.Reserve()
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if _, err := s.SendFrom(provisional, "raj-recipient", "hello"); err == nil {
		t.Fatal("a provisional sender was allowed to send; its reply would have no target")
	}
	sender, err := s.Participants.Join("raj-sender", "sender", KindAgent)
	if err != nil {
		t.Fatalf("join sender: %v", err)
	}
	sent, err := s.SendFrom(sender, "raj-recipient", "hello")
	if err != nil {
		t.Fatalf("a registered sender was refused: %v", err)
	}
	if len(sent) != 1 || sent[0].ID != recipient {
		t.Fatalf("sent = %+v, want the recipient %d", sent, recipient)
	}
	if msgs, ok := s.Mail.Wait(context.Background(), recipient); !ok || len(msgs) != 1 || msgs[0].From != sender {
		t.Fatalf("mailbox = %+v, %v; want one message from %d", msgs, ok, sender)
	}
}

// fakeMailStore is an in-memory MailStore for the mailbox tests: the same
// shape the workspace store implements, with row ids a test can watch being
// confirmed.
type fakeMailStore struct {
	mu        sync.Mutex
	next      int64
	rows      []fakeMailRow
	delivered map[int64]bool
}

type fakeMailRow struct {
	to  string
	msg StoredMessage
}

func newFakeMailStore() *fakeMailStore {
	return &fakeMailStore{delivered: map[int64]bool{}}
}

func (f *fakeMailStore) InsertMail(toIdentity string, from uint8, fromKey, fromName, text string, createdMS int64) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	f.rows = append(f.rows, fakeMailRow{to: toIdentity, msg: StoredMessage{
		ID: f.next, From: from, FromKey: fromKey, FromName: fromName, Text: text,
	}})
	return f.next, nil
}

func (f *fakeMailStore) MarkMailDelivered(ids []int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, id := range ids {
		f.delivered[id] = true
	}
	return nil
}

func (f *fakeMailStore) LoadUndeliveredMail(toIdentity string) ([]StoredMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []StoredMessage
	for _, r := range f.rows {
		if r.to != toIdentity || f.delivered[r.msg.ID] {
			continue
		}
		out = append(out, r.msg)
	}
	return out, nil
}

func (f *fakeMailStore) PruneDeliveredMail() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	kept := f.rows[:0]
	for _, r := range f.rows {
		if !f.delivered[r.msg.ID] {
			kept = append(kept, r)
		}
	}
	f.rows = kept
	return nil
}

func (f *fakeMailStore) isDelivered(id int64) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.delivered[id]
}

// With no store the mailbox is the in-memory one it has always been: Post and
// Wait work, and nothing tries to persist.
func TestMailboxNilStoreIsInMemory(t *testing.T) {
	var m Mailbox
	if err := m.Post(4, Message{From: AuthorUser, Text: "hi"}); err != nil {
		t.Fatalf("post: %v", err)
	}
	msgs, ok := m.Wait(context.Background(), 4)
	if !ok || len(msgs) != 1 || msgs[0].Text != "hi" {
		t.Fatalf("wait = %+v, %v; want the posted message", msgs, ok)
	}
}

// A message persisted for an identity that is gone is delivered when a new
// server over the same store sees that identity rejoin. This is the editor
// restart the durable queue exists for.
func TestMailboxReplaysToARejoiningIdentity(t *testing.T) {
	store := newFakeMailStore()

	before := &Server{Participants: NewRegistry()}
	if _, err := before.Participants.Join("raj-away", "away", KindAgent); err != nil {
		t.Fatalf("join away: %v", err)
	}
	sender, err := before.Participants.Join("raj-b", "b", KindAgent)
	if err != nil {
		t.Fatalf("join sender: %v", err)
	}
	before.Mail.SetStore(store, before.Participants)
	if _, err := before.SendFrom(sender, "raj-away", "held for you"); err != nil {
		t.Fatalf("send: %v", err)
	}

	// A restart: a fresh registry, the same store. The identity is not seeded,
	// so the row waits for it to rejoin.
	after := &Server{Participants: NewRegistry()}
	after.Mail.SetStore(store, after.Participants)
	after.Mail.Replay()
	// The row is still in the store: Replay had no registry row to resolve it
	// to, so it is kept for the identity to claim when it rejoins.
	if rows, err := store.LoadUndeliveredMail("raj-away"); err != nil || len(rows) != 1 {
		t.Fatalf("mail for the absent identity = %+v, %v; want the row kept", rows, err)
	}

	rejoined, err := after.Participants.Join("raj-away", "away", KindAgent)
	if err != nil {
		t.Fatalf("rejoin: %v", err)
	}
	after.Mail.ReplayIdentity("raj-away")
	msgs, ok := after.Mail.Wait(context.Background(), rejoined)
	if !ok || len(msgs) != 1 || msgs[0].Text != "held for you" {
		t.Fatalf("replayed mailbox = %+v, %v; want the held message", msgs, ok)
	}
	if msgs[0].FromKey != "raj-b" || msgs[0].FromName != "b" {
		t.Errorf("replayed reply target = %q/%q, want raj-b/b", msgs[0].FromKey, msgs[0].FromName)
	}
}

// A batch is confirmed by the identity parking again. Once confirmed, a
// restart does not replay it: the delivered marker is durable.
func TestMailboxDeliveredBatchIsNotReplayedAfterRestart(t *testing.T) {
	store := newFakeMailStore()

	before := &Server{Participants: NewRegistry()}
	recipient, err := before.Participants.Join("raj-a", "a", KindAgent)
	if err != nil {
		t.Fatalf("join recipient: %v", err)
	}
	sender, err := before.Participants.Join("raj-b", "b", KindAgent)
	if err != nil {
		t.Fatalf("join sender: %v", err)
	}
	before.Mail.SetStore(store, before.Participants)
	if _, err := before.SendFrom(sender, "raj-a", "once"); err != nil {
		t.Fatalf("send: %v", err)
	}

	// First park hands the batch; it is not confirmed yet.
	_, stop := context.WithCancelCause(context.Background())
	before.Mail.Park(recipient, stop)
	if msgs, ok := before.Mail.Wait(context.Background(), recipient); !ok || len(msgs) != 1 {
		t.Fatalf("handed = %+v, %v; want the one message", msgs, ok)
	}
	stop(nil)

	// The same identity parks again: that confirms the previous batch.
	_, stop2 := context.WithCancelCause(context.Background())
	before.Mail.Park(recipient, stop2)
	stop2(nil)

	after := &Server{Participants: NewRegistry()}
	rejoined, err := after.Participants.Join("raj-a", "a", KindAgent)
	if err != nil {
		t.Fatalf("rejoin: %v", err)
	}
	after.Mail.SetStore(store, after.Participants)
	after.Mail.Replay()
	if got := after.Mail.Unread(rejoined); got != 0 {
		t.Fatalf("confirmed mail was replayed after restart: %d unread", got)
	}
}

// The MailboxDepth bound holds on restore as it does on insert: rows beyond
// what a box can hold are dropped oldest-first and marked delivered.
func TestMailboxReplayDropsOverflowOldestFirst(t *testing.T) {
	store := newFakeMailStore()
	s := &Server{Participants: NewRegistry()}
	recipient, err := s.Participants.Join("raj-a", "a", KindAgent)
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	s.Mail.SetStore(store, s.Participants)
	for i := 0; i < MailboxDepth+4; i++ {
		if _, err := store.InsertMail("raj-a", 2, "raj-b", "b", fmt.Sprintf("m%d", i), int64(i+1)); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	s.Mail.Replay()
	if got := s.Mail.Unread(recipient); got != MailboxDepth {
		t.Fatalf("unread after replay = %d, want %d", got, MailboxDepth)
	}
	msgs, ok := s.Mail.Wait(context.Background(), recipient)
	if !ok || len(msgs) != MailboxDepth {
		t.Fatalf("restored batch = %d messages, %v; want %d", len(msgs), ok, MailboxDepth)
	}
	if msgs[0].Text != "m4" {
		t.Errorf("first restored = %q, want m4 (the four oldest dropped)", msgs[0].Text)
	}
	// The dropped rows are marked delivered so the next start prunes them
	// rather than replaying and dropping them again.
	if !store.isDelivered(1) {
		t.Error("the oldest dropped row was left undelivered")
	}
	if store.isDelivered(5) {
		t.Error("a kept row was marked delivered")
	}
}
