package app

import (
	"errors"
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
	done    chan struct{}
	closed  atomic.Bool
}

func newScriptedConn(gen uint64, buffers []control.Buffer, snaps map[string]control.Response) *scriptedConn {
	return &scriptedConn{gen: gen, buffers: buffers, snaps: snaps, done: make(chan struct{})}
}

func (s *scriptedConn) Do(req control.Request) (control.Response, error) {
	switch req.Op {
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
