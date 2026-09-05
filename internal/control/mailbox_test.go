package control

import (
	"context"
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
