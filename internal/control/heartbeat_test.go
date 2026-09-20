package control

import (
	"net"
	"testing"
	"time"
)

// The heartbeat is the server's answer to a request that is alive but quiet: a
// contentless, non-final frame carrying the request's id, emitted on a ticker
// while the request is in flight. It is an ordinary non-final frame, so a client
// that loops on those consumes it with no protocol change; its whole job is to
// reset the client's per-frame idle deadline.
//
// The tests split the two halves. The connection-level ones drive
// startHeartbeat/stopHeartbeat against a fake out channel, where the ordering is
// deterministic; the end-to-end one drives a real Server and reads frames off
// the socket.

// heartbeatConn has only what the heartbeat path reads: an out channel, a
// heartbeat map and this connection's tick interval. running is left nil, which
// send and cancelAll range over without dereferencing. The interval is a
// per-connection field, not a package var, so a test shortens its own
// connection without racing a ticker another test left running.
func heartbeatConn(out chan outFrame, every time.Duration) *connection {
	return &connection{out: out, heartbeat: every,
		heartbeats: map[int]chan struct{}{}}
}

// decodeOut decodes a frame as it sits on the connection's out channel, before
// the writer goroutine has turned it into bytes.
func decodeOut(f outFrame) (Response, error) {
	return DecodeResponse(Frame{Header: f.h, Body: f.body})
}

// readFrameWithin pulls one frame off a connection's out channel and decodes it,
// failing if none arrives in time.
func readFrameWithin(t *testing.T, out chan outFrame, within time.Duration) Response {
	t.Helper()
	select {
	case f := <-out:
		res, err := decodeOut(f)
		if err != nil {
			t.Fatalf("heartbeat frame did not decode: %v", err)
		}
		return res
	case <-time.After(within):
		t.Fatal("no frame arrived")
		return Response{}
	}
}

// TestHeartbeatEmitsUntilFinal covers the two guarantees of one heartbeat: while
// the request is in flight a contentless non-final frame with its id appears,
// and the final frame stops the tick so nothing for that id follows.
func TestHeartbeatEmitsUntilFinal(t *testing.T) {
	const every = 100 * time.Millisecond

	const id = 7
	c := heartbeatConn(make(chan outFrame, 8), every)
	c.startHeartbeat(id)
	defer c.stopHeartbeat(id)

	hb := readFrameWithin(t, c.out, time.Second)
	if hb.ID != id {
		t.Errorf("heartbeat id = %d, want %d", hb.ID, id)
	}
	if hb.Final {
		t.Error("heartbeat was marked final; a heartbeat must never end a request")
	}
	if hb.Out != "" || len(hb.Matches) != 0 || hb.Err != "" || hb.Stream != 0 {
		t.Errorf("heartbeat carried payload: %+v", hb)
	}

	// send stops the heartbeat on the final frame before that frame is queued,
	// so no tick still waiting on the ticker can follow it. (A tick already
	// inside send can land after the final -- send is best-effort about that --
	// but the 100ms period means none is in flight here.)
	c.send(Response{ID: id, Final: true})
	fin := readFrameWithin(t, c.out, time.Second)
	if fin.ID != id || !fin.Final {
		t.Fatalf("frame after heartbeat = %+v, want id %d marked final", fin, id)
	}
	select {
	case f := <-c.out:
		res, _ := decodeOut(f)
		t.Errorf("a frame arrived after the final: %+v", res)
	case <-time.After(3 * every):
	}
}

// TestHeartbeatStopIdempotent pins the repeated stop: send stops on the final
// frame and the dispatch's deferred stop runs again, and closing a channel twice
// panics. An unknown id is the same no-op.
func TestHeartbeatStopIdempotent(t *testing.T) {
	c := heartbeatConn(make(chan outFrame, 8), 10*time.Millisecond)
	c.startHeartbeat(3)
	c.stopHeartbeat(3)
	c.stopHeartbeat(3)  // must not panic
	c.stopHeartbeat(99) // unknown id, same no-op
	c.mu.Lock()
	left := len(c.heartbeats)
	c.mu.Unlock()
	if left != 0 {
		t.Errorf("%d heartbeat(s) remained after stopHeartbeat", left)
	}
}

// TestHeartbeatCancelAllStops covers connection teardown: cancelAll ends every
// heartbeat so a dropped connection leaves no ticker running, and a later start
// on the closed connection is refused rather than leaking one.
func TestHeartbeatCancelAllStops(t *testing.T) {
	const every = 10 * time.Millisecond

	c := heartbeatConn(make(chan outFrame, 8), every)
	c.startHeartbeat(1)
	c.startHeartbeat(2)
	readFrameWithin(t, c.out, time.Second) // prove one was running

	c.cancelAll()
	c.mu.Lock()
	left := len(c.heartbeats)
	c.mu.Unlock()
	if left != 0 {
		t.Errorf("%d heartbeat(s) survived cancelAll", left)
	}
	c.startHeartbeat(9) // after cancelAll the connection is closed
	c.mu.Lock()
	left = len(c.heartbeats)
	c.mu.Unlock()
	if left != 0 {
		t.Errorf("startHeartbeat after cancelAll registered a ticker")
	}
}

// TestServerHeartbeatReachesClient drives the shipped server: a search that
// hangs after its first batch keeps the request in flight, and the connection
// must emit a contentless non-final frame with that id while it is stuck. When
// the search is released and the final arrives, the heartbeat stops.
func TestServerHeartbeatReachesClient(t *testing.T) {
	const every = 100 * time.Millisecond

	ed := newFakeEditor(t, map[string]string{"/w/a.go": "needle\n"})
	// Shorten this server's heartbeat for the test. The seam lives on the
	// server, and setting it under the server's mutex gives the read in serve a
	// happens-before edge, so no test races the accept goroutines that the
	// fixture already started.
	ed.srv.mu.Lock()
	ed.srv.heartbeat = every
	ed.srv.mu.Unlock()
	ed.mu.Lock()
	ed.gate = make(chan struct{})
	ed.mu.Unlock()

	c, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	const id = 11
	h, body := EncodeRequest(Request{Op: "search", ID: id, Query: &SearchQuery{Text: "needle"}})
	if err := WriteFrame(c.conn, h, body); err != nil {
		t.Fatal(err)
	}

	// The first frame is the batch the fake emits before it waits; then the
	// heartbeats. Read until the contentless one shows up.
	for {
		if err := c.conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatal(err)
		}
		f, err := ReadFrame(c.r)
		if err != nil {
			t.Fatalf("no heartbeat frame arrived: %v", err)
		}
		res, err := DecodeResponse(f)
		if err != nil {
			t.Fatal(err)
		}
		if res.ID != id {
			continue
		}
		if res.Final {
			t.Fatalf("the search finished before a heartbeat: %+v", res)
		}
		if res.Out == "" && res.Stream == 0 && len(res.Matches) == 0 && res.Err == "" {
			break
		}
	}
	if err := c.conn.SetReadDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}

	// Release the search; the handler streams the rest and marks the last frame
	// final, which must stop the heartbeat.
	ed.mu.Lock()
	gate := ed.gate
	ed.mu.Unlock()
	close(gate)

	for {
		if err := c.conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
			t.Fatal(err)
		}
		f, err := ReadFrame(c.r)
		if err != nil {
			t.Fatalf("no final frame after releasing the search: %v", err)
		}
		res, err := DecodeResponse(f)
		if err != nil {
			t.Fatal(err)
		}
		if res.ID == id && res.Final {
			break
		}
	}
	if err := c.conn.SetReadDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}

	// stop-on-final: nothing more may come for this id.
	if err := c.conn.SetReadDeadline(time.Now().Add(3 * every)); err != nil {
		t.Fatal(err)
	}
	if f, err := ReadFrame(c.r); err == nil {
		res, _ := DecodeResponse(f)
		t.Errorf("a frame for id %d arrived after the final: %+v", id, res)
	}
}

// TestHeartbeatKeepsQuietStreamAlive is the point of the feature: a stream that
// sends only heartbeats outlasts the idle window because each one resets the
// per-frame deadline. The peer here sends nothing but empty non-final frames
// until the final, and the call must still succeed.
func TestHeartbeatKeepsQuietStreamAlive(t *testing.T) {
	const (
		beats = 10
		gap   = 20 * time.Millisecond
		idle  = 100 * time.Millisecond
	)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		f, err := ReadFrame(conn)
		if err != nil {
			return
		}
		req, err := DecodeRequest(f)
		if err != nil {
			return
		}
		for i := 0; i < beats; i++ {
			time.Sleep(gap)
			h, body := EncodeResponse(Response{ID: req.ID})
			if WriteFrame(conn, h, body) != nil {
				return
			}
		}
		time.Sleep(gap)
		h, body := EncodeResponse(Response{ID: req.ID, Final: true})
		WriteFrame(conn, h, body)
	}()

	c, err := Dial(TCPAddr(ln.Addr()))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.idleTimeout = idle

	start := time.Now()
	res, err := c.DoExec(Request{Op: "exec", Argv: []string{"true"}},
		func(stream uint8, b string) {})
	if err != nil {
		t.Fatalf("a heartbeat-fed stream failed after %s with idle %s: %v",
			time.Since(start), idle, err)
	}
	if !res.Final {
		t.Error("the final frame was not marked final")
	}
	if elapsed := time.Since(start); elapsed <= idle {
		t.Errorf("stream finished in %s, inside one idle %s; it did not run long enough to prove a reset", elapsed, idle)
	}
}

// TestStreamIdleOutlastsHeartbeat guards the coupling. The client only learns a
// stream is dead after a whole idle with no frame; if that idle were not longer
// than the server's heartbeat interval, a healthy peer between beats would be
// reported as silent.
func TestStreamIdleOutlastsHeartbeat(t *testing.T) {
	if streamIdle <= heartbeatEvery {
		t.Fatalf("streamIdle = %s must exceed heartbeatEvery = %s", streamIdle, heartbeatEvery)
	}
	if want := 3 * heartbeatEvery; streamIdle != want {
		t.Errorf("streamIdle = %s, want 3*heartbeatEvery = %s", streamIdle, want)
	}
}
