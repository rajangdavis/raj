package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

// A language server is a subprocess that can crash, hang, or spend ten seconds
// indexing before it answers anything. All three are normal, and the editor has
// to stay usable through each: a dead server degrades to no language features,
// never to a broken editor. Every entry point here either answers, fails, or
// times out — none of them block the caller indefinitely.
//
// Threading: Call and Notify are safe from any goroutine, and the reader owns
// the connection. The caller is expected to be the event thread, which is why
// results come back through a channel it already polls rather than through a
// callback that would run on the reader's goroutine and touch buffers it must
// not.

// ErrClosed is returned once the client has shut down or the server has exited.
var ErrClosed = errors.New("lsp: connection closed")

// ErrTimeout is returned when a request outlives its deadline. It is a normal
// outcome rather than a failure of the connection: a server busy indexing will
// answer the next request fine.
var ErrTimeout = errors.New("lsp: request timed out")

// Conn is a framed JSON-RPC connection to a server process.
type Conn struct {
	w      io.WriteCloser
	r      *bufio.Reader
	stop   func() // terminates the process
	notify func() // wakes the event loop when something arrives

	// writeMu serialises frames on the wire, and is deliberately separate from
	// mu. Holding the state lock across a write deadlocks under concurrent
	// calls: the write blocks until the server reads, the server blocks
	// writing its reply because the client's reader cannot take the state lock
	// to dispatch it, and the server therefore stops reading. Two locks with
	// no ordering between them is the fix, and it is only safe because nothing
	// ever needs both.
	writeMu sync.Mutex

	mu      sync.Mutex
	nextID  int
	pending map[int]chan *Message
	closed  bool
	// serverRequests holds requests the server originated. They are answered
	// with a method-not-found error rather than ignored, because a server that
	// waits forever for a reply it will never get is a server that stops
	// answering everything else.
	handler func(*Message) (any, *ResponseError)

	Diagnostics chan Diagnostics
}

// Diagnostics is a published diagnostic set for one document.
type Diagnostics struct {
	URI   string       `json:"uri"`
	Items []Diagnostic `json:"diagnostics"`
}

// Diagnostic is one problem in a document.
type Diagnostic struct {
	Range    Range  `json:"range"`
	Severity int    `json:"severity"`
	Source   string `json:"source"`
	Message  string `json:"message"`
}

// NewConn starts serving a connection over the given pipes. stop terminates the
// process and is called on shutdown; notify wakes the event loop.
func NewConn(w io.WriteCloser, r io.Reader, stop, notify func()) *Conn {
	c := &Conn{
		w:           w,
		r:           bufio.NewReaderSize(r, 64*1024),
		stop:        stop,
		notify:      notify,
		pending:     map[int]chan *Message{},
		Diagnostics: make(chan Diagnostics, 64),
	}
	if c.stop == nil {
		c.stop = func() {}
	}
	if c.notify == nil {
		c.notify = func() {}
	}
	go c.read()
	return c
}

// Call sends a request and waits for its answer.
//
// The context is the cancellation seam, and it matters more here than it looks:
// the answer to a hover is worthless once the cursor has moved, and a
// completion computed for a prefix three keystrokes ago is worse than nothing
// because it will be shown as though it were current. A cancelled call stops
// waiting immediately and tells the server to stop working via $/cancelRequest,
// which is advisory — servers are permitted to answer anyway, so the pending
// entry is removed regardless.
func (c *Conn) Call(ctx context.Context, method string, params, result any) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return ErrClosed
	}
	c.nextID++
	id := c.nextID
	ch := make(chan *Message, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	req, err := NewRequest(id, method, params)
	if err != nil {
		c.forget(id)
		return err
	}
	if err := c.write(req); err != nil {
		c.forget(id)
		return err
	}

	select {
	case m := <-ch:
		if m.Error != nil {
			return m.Error
		}
		if result == nil || len(m.Result) == 0 {
			return nil
		}
		return json.Unmarshal(m.Result, result)
	case <-ctx.Done():
		c.forget(id)
		c.cancelRequest(id)
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return ErrTimeout
		}
		return ctx.Err()
	}
}

// Notify sends a notification, which has no reply.
func (c *Conn) Notify(method string, params any) error {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return ErrClosed
	}
	m, err := NewNotification(method, params)
	if err != nil {
		return err
	}
	return c.write(m)
}

// cancelRequest is best effort: the server may have answered already, and a
// failure to send it is not worth reporting to a caller who has already given
// up on the request.
func (c *Conn) cancelRequest(id int) {
	_ = c.Notify("$/cancelRequest", map[string]any{"id": id})
}

func (c *Conn) forget(id int) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

func (c *Conn) write(m *Message) error {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return ErrClosed
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return WriteMessage(c.w, m)
}

// read is the reader goroutine. It owns the connection and exits when the
// server does.
func (c *Conn) read() {
	defer c.fail(ErrClosed)
	for {
		m, err := ReadMessage(c.r)
		if err != nil {
			return
		}
		c.dispatch(m)
	}
}

func (c *Conn) dispatch(m *Message) {
	switch {
	case m.IsResponse():
		id, ok := m.RequestID()
		if !ok {
			return // a response to something we did not send
		}
		c.mu.Lock()
		ch := c.pending[id]
		delete(c.pending, id)
		c.mu.Unlock()
		if ch != nil {
			ch <- m
		}

	case m.ID != nil:
		// A request from the server. Answering with an error is deliberate:
		// ignoring it leaves the server waiting forever for a reply, and a
		// server blocked on one request stops answering all the others.
		c.replyToServer(m)

	case m.Method == "textDocument/publishDiagnostics":
		var d Diagnostics
		if json.Unmarshal(m.Params, &d) != nil {
			return
		}
		select {
		case c.Diagnostics <- d:
			c.notify()
		default:
			// The event loop is behind. Dropping the oldest would be wrong —
			// diagnostics are absolute, not incremental, so the newest set is
			// the true one and dropping this would leave a stale set showing.
			// Draining one and retrying keeps the freshest.
			select {
			case <-c.Diagnostics:
			default:
			}
			select {
			case c.Diagnostics <- d:
				c.notify()
			default:
			}
		}
	}
}

// replyToServer answers a server-originated request. It runs on the reader
// goroutine and writes, which is safe only because write no longer holds the
// state lock — see writeMu.
func (c *Conn) replyToServer(m *Message) {
	var result any
	var rerr *ResponseError
	if c.handler != nil {
		result, rerr = c.handler(m)
	} else {
		rerr = &ResponseError{Code: -32601, Message: "method not supported: " + m.Method}
	}
	reply := &Message{JSONRPC: "2.0", ID: m.ID, Error: rerr}
	if rerr == nil && result != nil {
		if b, err := json.Marshal(result); err == nil {
			reply.Result = b
		}
	}
	_ = c.write(reply)
}

// Handle sets the responder for requests the server originates. Without one
// they are all refused, which is correct for a client that supports no
// server-initiated methods.
//
// Set before the connection is used; it is not swapped at runtime.
func (c *Conn) Handle(h func(*Message) (any, *ResponseError)) { c.handler = h }

// fail closes the connection and releases everyone waiting on it. A caller
// blocked on a request to a server that just died must get an error rather than
// wait forever.
func (c *Conn) fail(err error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	pending := c.pending
	c.pending = map[int]chan *Message{}
	c.mu.Unlock()

	for id, ch := range pending {
		ch <- &Message{Error: &ResponseError{Code: -32000, Message: err.Error()}}
		_ = id
	}
	c.notify()
}

// Close shuts the server down politely and then makes sure it is gone.
//
// The protocol asks for a shutdown request followed by an exit notification,
// and a well-behaved server exits on its own. A misbehaving one does not, which
// is why stop is called regardless: an editor that leaves language servers
// running after it quits is a bug people notice in their process list, not in
// the editor.
func (c *Conn) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = c.Call(ctx, "shutdown", nil, nil)
	_ = c.Notify("exit", nil)

	if c.w != nil {
		_ = c.w.Close()
	}
	c.fail(ErrClosed)
	c.stop()
}

// Closed reports whether the connection is finished.
func (c *Conn) Closed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// InitializeResult is the part of the handshake raj acts on: what the server
// says it can do.
type InitializeResult struct {
	Capabilities struct {
		HoverProvider      json.RawMessage `json:"hoverProvider"`
		DefinitionProvider json.RawMessage `json:"definitionProvider"`
		CompletionProvider *struct {
			TriggerCharacters []string `json:"triggerCharacters"`
		} `json:"completionProvider"`
		TextDocumentSync json.RawMessage `json:"textDocumentSync"`
	} `json:"capabilities"`
	ServerInfo struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"serverInfo"`
}

// Initialize performs the handshake.
//
// The timeout is generous because indexing a large repository genuinely takes
// tens of seconds and a server that is slow to start is not a server that is
// broken. It is finite because one that never answers must not leave the
// feature permanently pending with no way to tell.
func (c *Conn) Initialize(ctx context.Context, rootURI string, caps any) (*InitializeResult, error) {
	var res InitializeResult
	params := map[string]any{
		"processId":    nil,
		"rootUri":      rootURI,
		"capabilities": caps,
		"clientInfo":   map[string]string{"name": "raj"},
	}
	if err := c.Call(ctx, "initialize", params, &res); err != nil {
		return nil, err
	}
	if err := c.Notify("initialized", map[string]any{}); err != nil {
		return nil, err
	}
	return &res, nil
}

// Supports reports whether a capability was advertised. The protocol allows
// either a boolean or an options object for most of them, so a bare presence
// check is wrong: `false` is present and means no.
func Supports(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var b bool
	if json.Unmarshal(raw, &b) == nil {
		return b
	}
	var obj map[string]any
	return json.Unmarshal(raw, &obj) == nil
}

// URI converts an absolute path to a file URI, which is how LSP names
// documents. Only the characters that actually break parsing are escaped —
// over-escaping produces URIs some servers fail to match against their own.
func URI(path string) string {
	var out []byte
	out = append(out, "file://"...)
	for i := 0; i < len(path); i++ {
		b := path[i]
		switch {
		case b == '/' || b == '.' || b == '-' || b == '_' || b == '~' ||
			b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9':
			out = append(out, b)
		default:
			out = append(out, fmt.Sprintf("%%%02X", b)...)
		}
	}
	return string(out)
}

// Path converts a file URI back to a path, undoing URI.
func Path(uri string) string {
	s := uri
	if len(s) >= 7 && s[:7] == "file://" {
		s = s[7:]
	}
	var out []byte
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			if v, ok := unhex(s[i+1], s[i+2]); ok {
				out = append(out, v)
				i += 2
				continue
			}
		}
		out = append(out, s[i])
	}
	return string(out)
}

func unhex(a, b byte) (byte, bool) {
	hi, ok1 := hexVal(a)
	lo, ok2 := hexVal(b)
	return hi<<4 | lo, ok1 && ok2
}

func hexVal(b byte) (byte, bool) {
	switch {
	case b >= '0' && b <= '9':
		return b - '0', true
	case b >= 'a' && b <= 'f':
		return b - 'a' + 10, true
	case b >= 'A' && b <= 'F':
		return b - 'A' + 10, true
	}
	return 0, false
}
