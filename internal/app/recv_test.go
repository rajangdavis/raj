package app

import (
	"testing"
	"time"

	"raj/internal/control"
)

// The protocol has no way for the editor to speak first, so a message is
// delivered by a request that parks. This is the whole mechanism end to end,
// over a real socket: the driver asks, nothing answers, the user says
// something, the request wakes.
func TestRecvParksUntilTheUserSays(t *testing.T) {
	h := controlHarness(t, "hello world\n")
	c := h.dial(t)

	// hello binds the connection to a durable identity, which is what the
	// mailbox is keyed on. Without it the driver would park on the mailbox of
	// an anonymous id and the user's message would go to a different one.
	hi := c.do(h, control.Request{Op: "hello", Identity: "driver-1", Name: "claude"})
	if !hi.OK {
		t.Fatalf("hello = %+v", hi)
	}
	to := hi.Author

	// Park the recv on its own client. Client.Do holds a lock for the length of
	// a call, so a driver that wants both this and ordinary requests needs two
	// connections — which is exactly what this models.
	parked := h.dial(t)
	if hi2 := parked.do(h, control.Request{Op: "hello", Identity: "driver-1"}); hi2.Author != to {
		t.Fatalf("second connection got author %d, want %d", hi2.Author, to)
	}

	got := make(chan control.Response, 1)
	go func() {
		res, err := parked.c.Do(control.Request{Op: "recv"})
		if err != nil {
			got <- control.Response{Err: err.Error()}
			return
		}
		got <- res
	}()

	select {
	case res := <-got:
		t.Fatalf("recv answered %+v before anything was said", res)
	case <-time.After(50 * time.Millisecond):
	}

	if err := h.Tell(to, "stop what you are doing"); err != nil {
		t.Fatalf("tell: %v", err)
	}

	select {
	case res := <-got:
		if !res.OK {
			t.Fatalf("recv = %+v", res)
		}
		if len(res.Messages) != 1 || res.Messages[0].Text != "stop what you are doing" {
			t.Fatalf("messages = %+v", res.Messages)
		}
		if res.Messages[0].From != control.AuthorUser {
			t.Errorf("from = %d, want the human", res.Messages[0].From)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("recv never woke")
	}
}

// A message sent while nothing is listening is kept, not lost. The mailbox is
// keyed on the author id, and an identity keeps its id across reconnects, so a
// harness that was restarting still gets what the user said.
func TestRecvDeliversWhatWasSaidWhileAway(t *testing.T) {
	h := controlHarness(t, "x\n")

	first := h.dial(t)
	hi := first.do(h, control.Request{Op: "hello", Identity: "driver-1"})
	to := hi.Author
	if err := h.Tell(to, "while you were out"); err != nil {
		t.Fatalf("tell: %v", err)
	}
	first.c.Close()

	back := h.dial(t)
	if hi2 := back.do(h, control.Request{Op: "hello", Identity: "driver-1"}); hi2.Author != to {
		t.Fatalf("reconnect got author %d, want %d", hi2.Author, to)
	}
	res := back.do(h, control.Request{Op: "recv"})
	if !res.OK || len(res.Messages) != 1 || res.Messages[0].Text != "while you were out" {
		t.Fatalf("recv = %+v", res)
	}
}

// Tell refuses what it cannot deliver rather than queueing into a void: an id
// nobody holds, the human's own id, and an empty message.
func TestTellRefusesBadRecipients(t *testing.T) {
	h := controlHarness(t, "x\n")
	c := h.dial(t)
	hi := c.do(h, control.Request{Op: "hello", Identity: "driver-1"})

	if err := h.Tell(200, "nobody"); err == nil {
		t.Error("sending to an unknown author was allowed")
	}
	if err := h.Tell(control.AuthorUser, "myself"); err == nil {
		t.Error("sending to the local human was allowed")
	}
	if err := h.Tell(hi.Author, "   "); err == nil {
		t.Error("an empty message was allowed")
	}
}

// Drivers is what a prompt would offer. Nothing has connected in a fresh
// editor, which is the case that should be refused rather than asked about.
func TestDriversListsConnectedAgents(t *testing.T) {
	h := controlHarness(t, "x\n")
	if got := h.Drivers(); len(got) != 0 {
		t.Fatalf("drivers = %+v in a fresh editor", got)
	}
	c := h.dial(t)
	c.do(h, control.Request{Op: "hello", Identity: "driver-1", Name: "claude"})

	got := h.Drivers()
	var found bool
	for _, p := range got {
		if p.Name == "claude" && p.Connected {
			found = true
		}
	}
	if !found {
		// A dial mints an anonymous participant before hello rebinds it, so
		// the list is not necessarily one long — but the named one has to be
		// in it, and has to be connected.
		t.Fatalf("drivers = %+v, want a connected claude", got)
	}
}
