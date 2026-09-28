package control

import (
	"strconv"
	"strings"
	"testing"
)

// joinAs dials the fake editor and says hello as a driver with a durable
// identity, the way `raj ctl --as KEY` does, so the connection's author is the
// participant a mailbox is keyed on.
func joinAs(t *testing.T, ed *fakeEditor, key, name string) *Client {
	t.Helper()
	c, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	res, err := c.Do(Request{Op: "hello", Identity: key, Name: name})
	if err != nil || !res.OK {
		t.Fatalf("hello %s: %v %+v", key, err, res)
	}
	return c
}

// recvNow reads the mail already queued for c. The send happened first, so the
// parked recv answers at once.
func recvNow(t *testing.T, c *Client) []Message {
	t.Helper()
	res, err := c.Do(Request{Op: "recv"})
	if err != nil || !res.OK {
		t.Fatalf("recv: %v %+v", err, res)
	}
	return res.Messages
}

// A send lands in the recipient's mailbox, and its recv reports who sent it.
// The recipient is addressable by key, by display name and by author id, which
// are the three handles `raj ctl who` shows.
func TestSendDeliversToAPeersRecv(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "x\n"})
	a := joinAs(t, ed, "raj-aaaa0001", "alpha")
	b := joinAs(t, ed, "raj-bbbb0002", "beta")

	for i, to := range []string{"raj-bbbb0002", "beta", strconv.Itoa(int(b.Author()))} {
		res, err := a.Do(Request{Op: "send", To: to, Message: "review ready"})
		if err != nil || !res.OK {
			t.Fatalf("send to %q: %v %+v", to, err, res)
		}
		if len(res.Participants) != 1 || res.Participants[0].ID != b.Author() {
			t.Fatalf("send to %q reached %+v, want only author %d", to, res.Participants, b.Author())
		}
		got := recvNow(t, b)
		if len(got) != 1 || got[0].From != a.Author() || got[0].Text != "review ready" {
			t.Fatalf("case %d: recv = %+v, want one message from %d", i, got, a.Author())
		}
	}
}

// The sender is the connection, never a field: a frame claiming the
// recipient's own id still arrives as from the connection that sent it, so one
// agent cannot put words in another's mouth.
func TestSendIgnoresAClaimedAuthor(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "x\n"})
	a := joinAs(t, ed, "raj-aaaa0001", "alpha")
	b := joinAs(t, ed, "raj-bbbb0002", "beta")
	c := joinAs(t, ed, "raj-cccc0003", "gamma")

	res, err := a.Do(Request{Op: "send", To: "gamma", Message: "hi", Author: b.Author()})
	if err != nil || !res.OK {
		t.Fatalf("send: %v %+v", err, res)
	}
	if got := recvNow(t, c); len(got) != 1 || got[0].From != a.Author() {
		t.Fatalf("recv = %+v, want From %d (the real sender), not the claimed %d", got, a.Author(), b.Author())
	}
}

// all reaches every other connected driver and not the sender.
func TestSendAllReachesEveryOtherDriver(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "x\n"})
	a := joinAs(t, ed, "raj-aaaa0001", "alpha")
	b := joinAs(t, ed, "raj-bbbb0002", "beta")
	c := joinAs(t, ed, "raj-cccc0003", "gamma")

	res, err := a.Do(Request{Op: "send", To: "all", Message: "wave 2 starts"})
	if err != nil || !res.OK {
		t.Fatalf("send all: %v %+v", err, res)
	}
	if len(res.Participants) != 2 {
		t.Fatalf("send all reached %+v, want beta and gamma", res.Participants)
	}
	for _, r := range []*Client{b, c} {
		if got := recvNow(t, r); len(got) != 1 || got[0].From != a.Author() {
			t.Errorf("author %d recv = %+v", r.Author(), got)
		}
	}
	if n := ed.srv.Mail.Unread(a.Author()); n != 0 {
		t.Errorf("the sender got its own broadcast: %d unread", n)
	}
}

// Every refusal names the problem and queues nothing.
func TestSendRefusals(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "x\n"})
	a := joinAs(t, ed, "raj-aaaa0001", "alpha")
	b := joinAs(t, ed, "raj-bbbb0002", "beta")

	for _, tc := range []struct {
		name, to, msg, want string
	}{
		{"no recipient", "", "hi", "needs a recipient"},
		{"empty", "beta", "  ", "nothing to send"},
		{"self", "raj-aaaa0001", "hi", "is you"},
		{"unknown", "nobody", "hi", "no participant named"},
		{"unknown id", "200", "hi", "no participant with author id"},
		{"the person", "you", "hi", "not a driver"},
		{"too long", "beta", strings.Repeat("x", MaxMessage+1), "over the"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := a.Do(Request{Op: "send", To: tc.to, Message: tc.msg})
			if err != nil {
				t.Fatal(err)
			}
			if res.OK || !strings.Contains(res.Err, tc.want) {
				t.Fatalf("send = %+v, want a refusal containing %q", res, tc.want)
			}
		})
	}
	if n := ed.srv.Mail.Unread(b.Author()); n != 0 {
		t.Errorf("a refused send queued mail: %d unread", n)
	}
}
