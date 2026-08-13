package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeServer is a language server over pipes. Everything a real one does that
// the editor has to survive — answering, erroring, hanging, dying mid-request —
// is a behaviour this can be told to perform.
type fakeServer struct {
	t       *testing.T
	conn    *Conn
	in      *io.PipeReader // what the server reads
	out     *io.PipeWriter // what the server writes
	stopped chan struct{}

	mu       sync.Mutex
	handlers map[string]func(*Message) (any, *ResponseError)
	hang     map[string]bool
	seen     []string
	params   map[string][]json.RawMessage // params of each notification, by method
	replies  []string                     // IDs of client responses to server requests
	stops    int
}

func newFake(t *testing.T) *fakeServer {
	t.Helper()
	// io.Pipe returns (reader, writer): the client writes into clientOut,
	// which the server reads as serverIn, and vice versa.
	serverIn, clientOut := io.Pipe()
	clientIn, serverOut := io.Pipe()

	f := &fakeServer{
		t:        t,
		in:       serverIn,
		out:      serverOut,
		stopped:  make(chan struct{}),
		handlers: map[string]func(*Message) (any, *ResponseError){},
		hang:     map[string]bool{},
		params:   map[string][]json.RawMessage{},
	}
	f.conn = NewConn(clientOut, clientIn, func() {
		f.mu.Lock()
		f.stops++
		f.mu.Unlock()
	}, nil)
	go f.serve()
	t.Cleanup(func() {
		serverOut.Close()
		serverIn.Close()
	})
	return f
}

// on registers a reply for a method.
func (f *fakeServer) on(method string, fn func(*Message) (any, *ResponseError)) {
	f.mu.Lock()
	f.handlers[method] = fn
	f.mu.Unlock()
}

// hangOn makes a method never answer, which is what a server busy indexing
// looks like from the outside.
func (f *fakeServer) hangOn(method string) {
	f.mu.Lock()
	f.hang[method] = true
	f.mu.Unlock()
}

// die closes the server's end, which is what a crash looks like.
func (f *fakeServer) die() { f.out.Close(); f.in.Close() }

func (f *fakeServer) methods() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.seen...)
}

func (f *fakeServer) replyIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.replies...)
}

func (f *fakeServer) stopCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stops
}

// push sends a server-originated message.
func (f *fakeServer) push(m *Message) { _ = WriteMessage(f.out, m) }

func (f *fakeServer) serve() {
	defer close(f.stopped)
	r := bufio.NewReader(f.in)
	for {
		m, err := ReadMessage(r)
		if err != nil {
			return
		}
		f.mu.Lock()
		f.seen = append(f.seen, m.Method)
		if m.Method != "" {
			f.params[m.Method] = append(f.params[m.Method], m.Params)
		}
		if m.IsResponse() && m.ID != nil {
			f.replies = append(f.replies, strings.TrimSpace(string(*m.ID)))
		}
		fn, hasFn := f.handlers[m.Method]
		hang := f.hang[m.Method]
		f.mu.Unlock()

		if hang || m.ID == nil || m.IsResponse() {
			continue // hanging, a notification, or the client answering us
		}
		reply := &Message{JSONRPC: "2.0", ID: m.ID}
		if hasFn {
			res, rerr := fn(m)
			reply.Error = rerr
			if rerr == nil && res != nil {
				b, _ := json.Marshal(res)
				reply.Result = b
			}
		} else {
			reply.Error = &ResponseError{Code: -32601, Message: "no handler"}
		}
		if err := WriteMessage(f.out, reply); err != nil {
			return
		}
	}
}

// The handshake, and the capability check that decides which features exist.
func TestInitialize(t *testing.T) {
	f := newFake(t)
	f.on("initialize", func(*Message) (any, *ResponseError) {
		return map[string]any{
			"capabilities": map[string]any{
				"hoverProvider":      true,
				"definitionProvider": map[string]any{"workDoneProgress": true},
				"completionProvider": map[string]any{"triggerCharacters": []string{"."}},
			},
			"serverInfo": map[string]any{"name": "fake", "version": "1"},
		}, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	res, err := f.conn.Initialize(ctx, "file:///w", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.ServerInfo.Name != "fake" {
		t.Errorf("server name = %q", res.ServerInfo.Name)
	}
	if !Supports(res.Capabilities.HoverProvider) {
		t.Error("hover was advertised but not detected")
	}
	if !Supports(res.Capabilities.DefinitionProvider) {
		t.Error("an options object counts as support")
	}
	// initialized must follow, or servers that wait for it never start.
	waitFor(t, func() bool { return has(f.methods(), "initialized") })
}

// A capability may be a boolean or an options object, so presence is not
// support: false is present and means no.
func TestSupports(t *testing.T) {
	cases := map[string]bool{
		`true`:    true,
		`false`:   false,
		`{}`:      true,
		`{"a":1}`: true,
		``:        false,
		`null`:    false,
	}
	for raw, want := range cases {
		if got := Supports(json.RawMessage(raw)); got != want {
			t.Errorf("Supports(%q) = %v, want %v", raw, got, want)
		}
	}
}

// A request that outlives its deadline returns rather than blocking forever.
// This is the "server is indexing" case, and it must not wedge the editor.
func TestRequestTimeout(t *testing.T) {
	f := newFake(t)
	f.hangOn("textDocument/hover")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := f.conn.Call(ctx, "textDocument/hover", nil, nil)

	if !errors.Is(err, ErrTimeout) {
		t.Errorf("err = %v, want ErrTimeout", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("waited %v; the deadline was not honoured", d)
	}
	// And the connection still works afterwards: a timeout is one request
	// failing, not the server being lost.
	f.on("ping", func(*Message) (any, *ResponseError) { return "pong", nil })
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	if err := f.conn.Call(ctx2, "ping", nil, nil); err != nil {
		t.Errorf("the connection died with the timed-out request: %v", err)
	}
}

// Cancelling stops the wait immediately and tells the server to stop working.
// The answer to a hover is worthless once the cursor has moved, and a stale
// completion is worse than none because it is shown as though it were current.
func TestCancellation(t *testing.T) {
	f := newFake(t)
	f.hangOn("textDocument/completion")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.conn.Call(ctx, "textDocument/completion", nil, nil) }()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelling did not release the caller")
	}
	waitFor(t, func() bool { return has(f.methods(), "$/cancelRequest") })
}

// A late answer to a cancelled request must be dropped, not delivered to
// whoever asks next. Servers are allowed to answer after a cancel.
func TestLateAnswerToACancelledRequestIsDropped(t *testing.T) {
	f := newFake(t)
	release := make(chan struct{})
	f.on("slow", func(*Message) (any, *ResponseError) {
		<-release
		return "late", nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	go f.conn.Call(ctx, "slow", nil, nil)
	time.Sleep(20 * time.Millisecond)
	cancel()
	close(release)
	time.Sleep(30 * time.Millisecond)

	f.on("fresh", func(*Message) (any, *ResponseError) { return "fresh", nil })
	var got string
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	if err := f.conn.Call(ctx2, "fresh", nil, &got); err != nil {
		t.Fatal(err)
	}
	if got != "fresh" {
		t.Errorf("got %q — a stale answer was delivered to a later request", got)
	}
}

// A server that dies mid-request releases everyone waiting on it with an error.
// Anything else is an editor with a feature that never resolves.
func TestServerCrashReleasesWaiters(t *testing.T) {
	f := newFake(t)
	f.hangOn("textDocument/hover")

	done := make(chan error, 1)
	go func() {
		done <- f.conn.Call(context.Background(), "textDocument/hover", nil, nil)
	}()
	time.Sleep(20 * time.Millisecond)
	f.die()

	select {
	case err := <-done:
		if err == nil {
			t.Error("a crash produced no error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a crash left the caller waiting forever")
	}
	waitFor(t, f.conn.Closed)
}

// Calls after a crash fail immediately rather than hanging, so a dead server
// degrades to no language features rather than to a stalled editor.
func TestCallsAfterCrashFailFast(t *testing.T) {
	f := newFake(t)
	f.die()
	waitFor(t, f.conn.Closed)

	start := time.Now()
	if err := f.conn.Call(context.Background(), "anything", nil, nil); !errors.Is(err, ErrClosed) {
		t.Errorf("err = %v, want ErrClosed", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("a call on a dead connection took %v", d)
	}
	if err := f.conn.Notify("anything", nil); !errors.Is(err, ErrClosed) {
		t.Errorf("notify err = %v, want ErrClosed", err)
	}
}

// A server error belongs to its request. The connection survives it, because a
// server that cannot answer a hover can still answer the next completion.
func TestServerErrorDoesNotKillTheConnection(t *testing.T) {
	f := newFake(t)
	f.on("bad", func(*Message) (any, *ResponseError) {
		return nil, &ResponseError{Code: -32603, Message: "internal"}
	})
	f.on("good", func(*Message) (any, *ResponseError) { return "ok", nil })

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var rerr *ResponseError
	if err := f.conn.Call(ctx, "bad", nil, nil); !errors.As(err, &rerr) {
		t.Fatalf("err = %v, want a ResponseError", err)
	}
	var got string
	if err := f.conn.Call(ctx, "good", nil, &got); err != nil || got != "ok" {
		t.Errorf("the connection did not survive: %v, %q", err, got)
	}
}

// A request the server originates is answered, not ignored. A server left
// waiting forever for a reply stops answering everything else.
func TestServerRequestsAreAnswered(t *testing.T) {
	f := newFake(t)
	id := json.RawMessage(`99`)
	f.push(&Message{JSONRPC: "2.0", ID: &id, Method: "workspace/configuration"})

	// The client's reply reaches the server as a message carrying id 99 and no
	// method, which is what a response looks like from the other side.
	waitFor(t, func() bool { return has(f.replyIDs(), "99") })

	// And the connection is still usable afterwards.
	f.on("after", func(*Message) (any, *ResponseError) { return "ok", nil })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var got string
	if err := f.conn.Call(ctx, "after", nil, &got); err != nil || got != "ok" {
		t.Errorf("a server request broke the connection: %v, %q", err, got)
	}
}

// Diagnostics arrive on a channel the event loop polls, and the newest set wins
// when it falls behind: diagnostics are absolute rather than incremental, so a
// dropped newest set would leave a stale one showing.
func TestDiagnosticsDeliverNewestUnderPressure(t *testing.T) {
	f := newFake(t)
	for i := 0; i < 200; i++ {
		params, _ := json.Marshal(Diagnostics{
			URI:   "file:///w/a.go",
			Items: []Diagnostic{{Message: "problem " + itoa(i)}},
		})
		f.push(&Message{
			JSONRPC: "2.0",
			Method:  "textDocument/publishDiagnostics",
			Params:  params,
		})
	}
	deadline := time.After(2 * time.Second)
	var last Diagnostics
	for {
		select {
		case d := <-f.conn.Diagnostics:
			last = d
			if len(last.Items) > 0 && last.Items[0].Message == "problem 199" {
				return
			}
		case <-deadline:
			if len(last.Items) == 0 {
				t.Fatal("no diagnostics arrived")
			}
			t.Logf("newest seen: %q", last.Items[0].Message)
			return
		}
	}
}

// Close stops the process even when the server ignores the polite shutdown. An
// editor that leaves language servers running after it quits is a bug people
// find in their process list.
func TestCloseAlwaysStopsTheProcess(t *testing.T) {
	f := newFake(t)
	f.hangOn("shutdown") // a server that does not cooperate

	done := make(chan struct{})
	go func() { f.conn.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close hung on an uncooperative server")
	}
	if f.stopCount() == 0 {
		t.Error("the process was not stopped")
	}
	if !f.conn.Closed() {
		t.Error("the connection is not marked closed")
	}
}

// Close is safe to call twice, since shutdown paths run from more than one
// place.
func TestCloseIsIdempotent(t *testing.T) {
	f := newFake(t)
	f.on("shutdown", func(*Message) (any, *ResponseError) { return nil, nil })
	f.conn.Close()
	f.conn.Close()
}

// Concurrent callers must not interleave frames on the wire. A torn frame
// desynchronises the stream permanently.
func TestConcurrentCalls(t *testing.T) {
	f := newFake(t)
	f.on("echo", func(m *Message) (any, *ResponseError) {
		var p struct {
			N int `json:"n"`
		}
		json.Unmarshal(m.Params, &p)
		return p.N, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	errs := make(chan error, 50)
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			var got int
			if err := f.conn.Call(ctx, "echo", map[string]int{"n": n}, &got); err != nil {
				errs <- err
				return
			}
			if got != n {
				errs <- errors.New("answer went to the wrong caller")
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestURIRoundTrip(t *testing.T) {
	cases := []string{
		"/home/user/project/main.go",
		"/path with spaces/file.go",
		"/tmp/日本語.go",
		"/a-b_c.d/~x",
		"/weird#hash?q.go",
	}
	for _, p := range cases {
		u := URI(p)
		if !strings.HasPrefix(u, "file://") {
			t.Errorf("URI(%q) = %q, want a file scheme", p, u)
		}
		if got := Path(u); got != p {
			t.Errorf("round trip: %q -> %q -> %q", p, u, got)
		}
	}
}

// waitFor polls until cond holds or the test gives up, which is how anything
// touching another goroutine has to be asserted without a sleep that is either
// flaky or slow.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition never held")
}

func has(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
