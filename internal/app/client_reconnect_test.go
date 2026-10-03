package app

import (
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"raj/internal/control"
	"raj/internal/piecetable"
	"raj/internal/ui"
)

// scriptedConn is a control connection that plays canned answers, for the
// reconnect tests. It answers buffers and snapshot from its tables and parks on
// watch until closed, so a reconnected client settles instead of spinning.
type scriptedConn struct {
	gen     uint64
	buffers []control.Buffer
	snaps   map[string]control.Response
	// roots is the workspace root set this scripted daemon reports, so a test
	// can drive the attach adoption path without a real server.
	roots []string
	// kind is the participant kind this scripted daemon grants on hello, so a
	// test can drive the attach mode selection without a real transport. Empty
	// stands for an older daemon that grants nothing.
	kind   control.Kind
	done   chan struct{}
	closed atomic.Bool
}

func newScriptedConn(gen uint64, buffers []control.Buffer, snaps map[string]control.Response) *scriptedConn {
	return &scriptedConn{gen: gen, buffers: buffers, snaps: snaps, done: make(chan struct{})}
}

func (s *scriptedConn) Do(req control.Request) (control.Response, error) {
	switch req.Op {
	case "hello":
		return control.Response{OK: true, Kind: string(s.kind)}, nil
	case "buffers":
		return control.Response{OK: true, Gen: s.gen, Buffers: s.buffers}, nil
	case "snapshot":
		if r, ok := s.snaps[req.Path]; ok {
			return r, nil
		}
		return control.Response{OK: false, Err: "no such buffer"}, nil
	case "watch":
		<-s.done
		return control.Response{}, errors.New("scripted connection closed")
	default:
		return control.Response{OK: true}, nil
	}
}

func (s *scriptedConn) ResolveRoots(string) (control.Mapper, error) { return control.Mapper{}, nil }

func (s *scriptedConn) Roots() []string { return s.roots }

func (s *scriptedConn) Kind() control.Kind { return s.kind }

func (s *scriptedConn) Close() error {
	if s.closed.CompareAndSwap(false, true) {
		close(s.done)
	}
	return nil
}

// setClientDial swaps the client dialer for one test and restores it after.
func setClientDial(t *testing.T, f func(string) (clientConn, error)) {
	t.Helper()
	clientDialMu.Lock()
	prev := clientDial
	clientDial = f
	clientDialMu.Unlock()
	t.Cleanup(func() {
		clientDialMu.Lock()
		clientDial = prev
		clientDialMu.Unlock()
	})
}

// failClientDial makes every reconnect attempt fail, so a test can observe the
// down state without the client recovering.
func failClientDial(t *testing.T) {
	t.Helper()
	setClientDial(t, func(string) (clientConn, error) {
		return nil, errors.New("daemon is gone")
	})
}

// dropWatch closes the client's live watch connection under the same mutex
// publishClient uses, so a test can script a loss without racing the swap.
func (h *harness) dropWatch() {
	h.clientConnMu.Lock()
	c := h.client
	h.clientConnMu.Unlock()
	if c != nil {
		_ = c.Close()
	}
}

// A watch transport error re-dials and re-derives the view: the dot goes red
// while the first retry fails, then green once a connection succeeds, the tab
// text is refreshed, and no duplicate tab appears.
func TestClientReconnectsAndResyncs(t *testing.T) {
	srv := controlHarness(t, "hello\n")
	srv.typeText("X") // unsaved work is what makes the daemon tab mirror
	ch := attachClientAt(t, srv, Options{Phone: true}, 120, 30)
	ch.cli.drain()
	p := ch.cli.Tabs.Active()
	if p == nil {
		t.Fatal("client attached with no tab")
	}
	path := p.File.Path

	// The reconnected daemon describes the same path at a new version with new
	// text, so the resync has something to install.
	sess := piecetable.NewSession(piecetable.NewDoc("reconnected\n", 0))
	snap, err := sess.SnapshotState()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := snap.Encode()
	if err != nil {
		t.Fatal(err)
	}
	conn := newScriptedConn(7,
		[]control.Buffer{{Path: path, Version: 1}},
		map[string]control.Response{path: {OK: true, Version: 1, SnapshotJSON: string(encoded)}})

	// Fail the first re-dial so the red dot is observable, then serve the
	// scripted daemon on every later dial. calls is only touched by the watch
	// goroutine and the test, so it is atomic.
	var calls atomic.Int32
	setClientDial(t, func(string) (clientConn, error) {
		if calls.Add(1) == 1 {
			return nil, errors.New("daemon is still down")
		}
		return conn, nil
	})

	ch.cli.dropWatch()

	deadline := time.After(3 * time.Second)
	for !ch.cli.clientIsDown() {
		select {
		case <-deadline:
			t.Fatalf("the dot never went red; text = %q", p.File.Text())
		default:
		}
		ch.cli.drain()
		time.Sleep(time.Millisecond)
	}

	for p.File.Text() != "reconnected\n" {
		select {
		case <-deadline:
			t.Fatalf("the client never resynced; text = %q", p.File.Text())
		default:
		}
		ch.cli.drain()
		time.Sleep(time.Millisecond)
	}
	if ch.cli.clientIsDown() {
		t.Error("the dot stayed red after a successful reconnect")
	}
	if got := ch.cli.Tabs.Count(); got != 1 {
		t.Errorf("tabs = %d, want 1 (no duplicate on reconnect)", got)
	}
	ch.cli.drain()
	if dot := handleDot(t, ch.cli); dot.Style.Fg != ui.Ansi(2) {
		t.Errorf("reconnected dot fg = %v, want green", dot.Style.Fg)
	}
	// The decision connection is re-established too: a decision must reach the
	// scripted daemon rather than the closed one.
	if _, err := ch.cli.sendDecision("claim", path, 0); err != nil {
		t.Errorf("the decision connection was not re-established: %v", err)
	}
}

// CloseClient is an orderly quit, not a failure: it must stop the retry loop
// rather than reconnect forever.
func TestCloseClientStopsReconnect(t *testing.T) {
	srv := controlHarness(t, "hello\n")
	ch := attachClient(t, srv)
	ch.cli.drain()

	var calls atomic.Int32
	setClientDial(t, func(string) (clientConn, error) {
		calls.Add(1)
		return nil, errors.New("daemon is gone")
	})
	ch.cli.dropWatch()

	deadline := time.After(3 * time.Second)
	for !ch.cli.clientIsDown() {
		select {
		case <-deadline:
			t.Fatal("the client never went down")
		default:
		}
		ch.cli.drain()
		time.Sleep(time.Millisecond)
	}

	ch.cli.CloseClient()
	select {
	case <-ch.cli.clientDone:
	case <-time.After(time.Second):
		t.Fatal("CloseClient did not stop the watch loop")
	}
	stopped := calls.Load()
	time.Sleep(2 * clientRetryMin)
	if got := calls.Load(); got != stopped {
		t.Errorf("the retry loop kept dialing after CloseClient: %d -> %d", stopped, got)
	}
}

// A reconnect re-adopts a daemon that comes back with a different root set, so
// the explorer, search and containment follow the peer actually there instead
// of rendering the workspace from before the restart.
func TestClientReconnectAdoptsChangedRoots(t *testing.T) {
	rootA, rootB, rootC := t.TempDir(), t.TempDir(), t.TempDir()
	launch := t.TempDir()

	first := newScriptedConn(1, nil, nil)
	first.roots = []string{rootA}
	setClientDial(t, func(string) (clientConn, error) { return first, nil })
	fh := ui.NewFakeHost(120, 30)
	t.Cleanup(func() { fh.Close() })
	a := NewWithOptions(fh, launch, Options{Attach: true, AttachAddr: "scripted"})
	t.Cleanup(a.CloseClient)
	a.StartClient()
	if got := a.visible.All(); !sameStrings(got, []string{rootA}) {
		t.Fatalf("attach visible roots = %q, want %q", got, []string{rootA})
	}
	h := &harness{App: a, host: fh}

	second := newScriptedConn(2, nil, nil)
	second.roots = []string{rootB, rootC}
	setClientDial(t, func(string) (clientConn, error) { return second, nil })

	// Drop the live watch so the loop reconnects to the daemon that reports a
	// different root set.
	a.clientConnMu.Lock()
	c := a.client
	a.clientConnMu.Unlock()
	if c != nil {
		_ = c.Close()
	}

	// The reconnect queues the daemon root set for the event thread rather
	// than adopting it on the watch goroutine, so the drain that installs
	// queued client work is what makes the adoption visible. Drive it before
	// reading the visible workspace.
	deadline := time.After(3 * time.Second)
	for {
		h.drain()
		if sameStrings(a.visible.All(), []string{rootB, rootC}) {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("the reconnect never re-adopted the daemon roots; visible = %q", a.visible.All())
		default:
			time.Sleep(time.Millisecond)
		}
	}
	h.drain()
	if got := (host{a}).Roots(); !sameStrings(got, []string{rootB, rootC}) {
		t.Errorf("host roots = %q, want the reconnected daemon's %q", got, []string{rootB, rootC})
	}
}

// attachScripted starts an attached app against a scripted connection, so a
// test can drive the attach mode selection without a real daemon or transport.
// The fake's kind is the grant the app reads, standing in for a Unix or TCP
// hello the in-process harness cannot tell apart here.
func attachScripted(t *testing.T, conn *scriptedConn) *harness {
	t.Helper()
	setClientDial(t, func(string) (clientConn, error) { return conn, nil })
	host := ui.NewFakeHost(120, 30)
	t.Cleanup(func() { host.Close() })
	a := NewWithOptions(host, t.TempDir(), Options{Attach: true, AttachAddr: "scripted"})
	t.Cleanup(a.CloseClient)
	a.StartClient()
	return &harness{App: a, host: host}
}

// scriptedSnapshot builds the one snapshot a scripted daemon serves, so a mode
// test has a tab to type into without a real buffer on disk.
func scriptedSnapshot(t *testing.T, text string) control.Response {
	t.Helper()
	sess := piecetable.NewSession(piecetable.NewDoc(text, 0))
	snap, err := sess.SnapshotState()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := snap.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return control.Response{OK: true, Version: 1, SnapshotJSON: string(encoded)}
}

// attachStartMode is the whole decision: only a kind the daemon positively
// granted as non-human starts read-only. An unknown kind -- an older daemon
// that sends none, or a name this build does not know -- fails open to Edit,
// which is what keeps a missing field from locking a local client out.
func TestAttachStartMode(t *testing.T) {
	cases := []struct {
		name  string
		kind  control.Kind
		mode  Mode
		agent bool
	}{
		{"human grant", control.KindHuman, ModeEdit, false},
		{"agent grant", control.KindAgent, ModeReview, true},
		{"unknown kind name", control.Kind("robot"), ModeEdit, false},
		{"no kind from an older daemon", "", ModeEdit, false},
	}
	for _, c := range cases {
		mode, agent := attachStartMode(c.kind)
		if mode != c.mode || agent != c.agent {
			t.Errorf("%s: attachStartMode(%q) = (%v, %v), want (%v, %v)",
				c.name, c.kind, mode, agent, c.mode, c.agent)
		}
	}
}

// A non-human grant -- the TCP client's downgraded kind -- starts in Review,
// the client's read-only gate, with a status that names it, and refuses typing.
// The scripted connection stands in for the TCP transport the in-process
// harness cannot fake: what the app does is a function of the granted kind, and
// the granted kind is what is scripted.
func TestAttachedAgentClientStartsReadOnly(t *testing.T) {
	const path = "/w/a.go"
	conn := newScriptedConn(1,
		[]control.Buffer{{Path: path, Version: 1, Dirty: true}},
		map[string]control.Response{path: scriptedSnapshot(t, "package a\n")})
	conn.kind = control.KindAgent
	h := attachScripted(t, conn)

	if h.mode != ModeReview {
		t.Fatalf("mode = %v, want Review for an agent grant", h.mode)
	}
	if !h.attachedAsAgent {
		t.Error("an agent grant was not recorded on the app")
	}
	if !strings.Contains(h.status, "attached as an agent") {
		t.Errorf("status = %q, want the agent attach note", h.status)
	}
	p := h.Tabs.Active()
	if p == nil {
		t.Fatal("the scripted agent attach showed no tab")
	}
	h.focusEditor()
	before := p.File.Text()
	h.typeText("x")
	if p.File.Text() != before {
		t.Errorf("Review accepted an edit from an agent client: %q", p.File.Text())
	}

	// Leaving the read-only gate is allowed, but the status must still say the
	// edits arrive as proposals rather than as this client's own text.
	h.toggleReview()
	if h.mode != ModeEdit {
		t.Fatalf("mode = %v after leaving Review, want Edit", h.mode)
	}
	if !strings.Contains(h.status, "edits land as proposals") {
		t.Errorf("status after leaving Review = %q, want the proposals note", h.status)
	}
}

// An unknown granted kind -- the empty kind an older daemon's hello reply
// carries -- must keep today's editable behaviour rather than going read-only
// on a guess, so a message this build cannot read can never silently lock a
// local client out of editing.
func TestAttachUnknownKindFailsOpenToEdit(t *testing.T) {
	const path = "/w/a.go"
	conn := newScriptedConn(1,
		[]control.Buffer{{Path: path, Version: 1, Dirty: true}},
		map[string]control.Response{path: scriptedSnapshot(t, "package a\n")})
	h := attachScripted(t, conn)

	if h.mode != ModeEdit {
		t.Fatalf("mode = %v, want Edit for an unknown grant", h.mode)
	}
	if h.attachedAsAgent {
		t.Error("an unknown grant was flagged as an agent")
	}
	if strings.Contains(h.status, "attached as an agent") {
		t.Errorf("an unknown grant shows the agent note: %q", h.status)
	}
}
