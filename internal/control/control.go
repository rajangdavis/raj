// Package control exposes the open buffers over a Unix domain socket, so that
// something outside raj can read and edit them.
//
// This is the seam the in-editor agent pane was replaced by. An editor that
// hosts a model owns that model's lifecycle, configuration, failure modes and
// version skew, and none of that is editing. A socket puts the driver in
// another process, where it can be restarted, replaced, or written in another
// language without touching the editor — and where a crash in it is a crash in
// it rather than in the thing holding your unsaved work.
//
// # Threading
//
// The one hard constraint. Every mutation in raj happens on the goroutine that
// owns the model, between File.Begin and File.End; a socket handler is not that
// goroutine. So a handler does not touch the model at all. It parks the request,
// posts a Wake, and blocks on a reply channel; the event loop drains the queue,
// executes each request where it is safe to, and answers. This is the same
// park-then-Notify shape the search pane uses for results, for the same reason.
//
// The package therefore contains no editor types and cannot mutate anything. It
// moves bytes and requests; internal/app decides what a request means. That
// split is what keeps the unsafe version — "just take a lock and edit from the
// accept goroutine" — from being writable by accident.
//
// # Protocol
//
// Line-delimited JSON, one object per line, request and response. The
// alternative is a framed binary format that cannot be debugged with nc, and
// this socket exists to be driven by things that do not exist yet.
package control

import (
	"bufio"
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"raj/internal/safe"
)

// Hunk is a replacement of [Start,End) with Text, in bytes, against the version
// named by a request's Base.
type Hunk struct {
	Start int    `json:"start"`
	End   int    `json:"end"`
	Text  string `json:"text"`
}

// Request is one line in.
//
// Base is a pointer so that "not supplied" is distinguishable from version 0:
// an apply with no base is refused rather than silently rebased against
// whatever the buffer happens to be, which is the mistake that corrupts a file
// while looking like it worked.
type Request struct {
	ID   int
	Op   string
	Path string
	// Author is the writer this request claims to be. Zero means "whatever the
	// connection was assigned"; naming a different one is refused, which is the
	// check that stops one agent's text being attributed to another.
	Author uint8
	Base   *uint64
	Hunks  []Hunk
	Query  *SearchQuery
	// Cancel names an in-flight request id to abandon.
	Cancel int
	// Line and Col are a goto: the 1-based position the editor's cursor
	// should move to. Zero means absent; the editor defaults a missing line to
	// the one the cursor is already on, and a missing column to the margin.
	Line int
	Col  int

	// Identity and Name introduce a participant. Identity is durable across
	// connections; Name is for display.
	Identity string
	Name     string
	// Group addresses a change set for accept and reject.
	Group uint64
	// Argv is the command for exec, and Dir the directory to run it in.
	Argv []string
	Dir  string
	// Token authenticates a TCP client. Ignored on a Unix socket, where the
	// filesystem permissions have already decided.
	Token string
	// Program is a batch of requests encoded as opcodes; see prog.go. Present
	// only on the "prog" op, which compiles it and runs the results.
	Program []byte
}

// Group is a change set: what one apply, or one user action, did. Reviewable as
// a unit because that is already the unit undo reverses.
type Group struct {
	ID     uint64 `json:"id"`
	Path   string `json:"path"`
	Author uint8  `json:"author"`
	State  string `json:"state"` // proposed, accepted, rejected
	Ops    int    `json:"ops"`
	Bytes  int    `json:"bytes"`
	First  uint64 `json:"first"`
	Last   uint64 `json:"last"`
}

// DirtyBuffer is an unsaved buffer, and whether every unsaved run in it was
// written by an agent rather than by the human.
type DirtyBuffer struct {
	Path string
	// AgentOnly is true when no dirty span is the user's. It is the measurement
	// the exec policy is waiting on, not yet a licence to flush.
	AgentOnly bool
}

// ExecStats counts how often a command ran against files that did not match the
// buffers, which is the measurement that will decide what to do about it.
//
// Stale is runs with at least one unsaved buffer; AgentOnly is the subset where
// the human had written none of the unsaved text, so writing it out would only
// have been flushing an agent's own work. Neither is acted on: the command runs
// either way and the caller is told.
type ExecStats struct {
	Runs      int
	Stale     int
	AgentOnly int
}

// SearchQuery is a search over the workspace. The fields mirror the editor's
// own, because it is the same engine: one search means an agent's grep sees
// unsaved edits and cannot disagree with what the user is looking at.
type SearchQuery struct {
	Text    string
	Include string // comma-separated globs
	Exclude string
	Regex   bool
	Case    bool
	Word    bool
}

// SearchMatch is one hit. Text is the whole line it was found on.
type SearchMatch struct {
	Path      string
	Line      int // 1-based
	Col       int // byte offset of the match within Text
	Len       int
	ByteStart int // byte offset of the match within the file
	ByteEnd   int // one past the last byte of the match within the file
	Text      string
}

// Span is one authored run of the document. Reads come back as spans rather
// than as one string because that is how the document is stored: pieces carry
// an author, so a caller learns who wrote each byte without a second call.
type Span struct {
	Text   string
	Author uint8
}

// Mine reports whether this span was written by the given author. It exists so
// the comparison is written once: an agent asking "is this my text" is the
// question the whole attribution scheme is for, and getting it slightly wrong
// — comparing against the constant Agent rather than against the id you were
// assigned — silently answers for the wrong writer.
func (s Span) Mine(author uint8) bool { return s.Author == author }

// ByUser reports text the human typed, which is the span kind an agent must
// never quietly discard.
func (s Span) ByUser() bool { return s.Author == AuthorUser }

// AuthorOriginal is the file as loaded and AuthorUser is the human. Agents
// start at FirstAgent. These are the piece table's own numbers, deliberately:
// attribution is a property of the store, not something the transport invents.
const (
	AuthorOriginal uint8 = 0
	AuthorUser     uint8 = 1
)

// Buffer describes one open tab.
type Buffer struct {
	Path    string `json:"path"`
	Version uint64 `json:"version"`
	Dirty   bool   `json:"dirty"`
	Bytes   int    `json:"bytes"`
	Lines   int    `json:"lines"`
	Active  bool   `json:"active"`
}

// Conflict is a hunk that could not be rebased onto the current version. At is
// the version of the op that invalidated the range, not a byte offset — it tells
// a driver what it missed, so it can re-read from there rather than resubmitting
// the whole diff blind.
type Conflict struct {
	Index int    `json:"index"`
	At    uint64 `json:"at"`
	Hunk  Hunk   `json:"hunk"`
}

// Response is one line out. Err is a string rather than a code because the
// consumer is a human at a socket at least as often as it is a program.
type Response struct {
	ID        int
	OK        bool
	Err       string
	Root      string
	PID       int
	Buffers   []Buffer
	Matches   []SearchMatch
	Files     int
	Capped    bool
	Spans     []Span
	Version   uint64
	Conflicts []Conflict
	// Author is the id the editor assigned this connection. It rides on every
	// response, not just a handshake, because a client that reconnects or is
	// restarted mid-session would otherwise be holding a stale one — and an
	// agent that thinks it is author 3 when it is author 4 will read its own
	// text as somebody else's.
	Author uint8
	// Searcher is returned by the internal "snapshot" op. It never crosses the
	// wire: it is how the event thread hands a consistent view of the open
	// buffers to a walk that runs off it.
	Searcher Searcher
	// Exit is a finished command's status, and Dirty the buffers that stopped
	// one from starting.
	Exit         int
	Dirty        []DirtyBuffer
	Stats        ExecStats
	Participants []Participant
	Groups       []Group
	// Messages is what recv returns: everything the user has said to this
	// participant since it last asked.
	Messages []Message
	// Stream is output from a running command: 1 stdout, 2 stderr.
	Stream uint8
	// Out is the bytes of a Stream frame.
	Out string
	// Final marks the last frame of a response. Callers that do not stream can
	// ignore it; Client.Do reads until it is set.
	Final bool
}

// Text flattens the spans, for callers that do not care who wrote what.
func (r Response) Text() string {
	if len(r.Spans) == 1 {
		return r.Spans[0].Text
	}
	var b strings.Builder
	for _, s := range r.Spans {
		b.WriteString(s.Text)
	}
	return b.String()
}

// Author ids mirror the piece table's: 0 is the file as loaded, 1 the human,
// and agents start at 2. They are the same numbers, deliberately — attribution
// is a property of the store, not something the transport invents.
const FirstAgent uint8 = 2

// Pending is a parked request awaiting the event thread.
type Pending struct {
	Req  Request
	done chan Response
	once sync.Once
}

// Reply answers a request. Safe to call more than once and from any goroutine;
// only the first answer is sent, so a handler that both errors and returns
// cannot deadlock the connection.
func (p *Pending) Reply(r Response) {
	p.once.Do(func() {
		r.ID = p.Req.ID
		p.done <- r
	})
}

// Fail answers with an error.
func (p *Pending) Fail(format string, args ...any) {
	p.Reply(Response{Err: fmt.Sprintf(format, args...)})
}

// ReplyTimeout bounds how long a handler waits for the event thread. Without it
// a request submitted while raj is quitting, or while a modal dialog is open and
// the loop is elsewhere, hangs the client for ever with no way to tell whether
// it was applied.
const ReplyTimeout = 5 * time.Second

// Server accepts connections and parks their requests.
type Server struct {
	// Notify wakes the event thread. Required: without it a parked request
	// waits for the next idle tick, which is 150 ms of latency for no reason.
	Notify func()

	// Participants maps durable identities to author ids. One per editor, so a
	// harness reconnecting is the same writer it was before.
	Participants *Registry

	// Mail carries messages from the user to a connected driver. It is on the
	// server rather than behind the Host interface because nothing about it
	// touches the document: the editor posts from the event thread and never
	// blocks, and a parked recv reads on its own connection goroutine. Routing
	// it through the event thread would mean parking a request there, which is
	// the one thing that layer must never do.
	Mail Mailbox

	// AllowRemoteExec permits `exec` from a TCP client. Off by default, and
	// deliberately: an agent in a container asking the editor to run a command
	// gets it run in the editor's process, on the host, outside the sandbox
	// that was the reason for the container. That is a sandbox escape wearing
	// the clothes of a convenience, so it has to be asked for.
	//
	// It has no effect on a Unix socket, where the caller could already run
	// the command itself.
	AllowRemoteExec bool

	path    string
	network string
	token   string
	ln      net.Listener

	mu     sync.Mutex
	anon   int
	queue  []*Pending
	closed bool
}

// Send queues a message from the person at the keyboard to a participant.
//
// The editor's half of the mailbox. It is here rather than on Mailbox so that
// the checks live with the registry that can answer them: a message addressed
// to an id nobody holds is a bug worth reporting, not an entry in a map that
// nothing will ever read.
//
// A disconnected recipient is allowed on purpose. Its mailbox is keyed on the
// author id, which is durable across reconnects, so telling a harness something
// while it is restarting is delivered when it comes back rather than lost.
func (s *Server) Send(to uint8, text string) error {
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("nothing to send")
	}
	p, ok := s.Participants.Get(to)
	if !ok {
		return fmt.Errorf("no participant with author id %d", to)
	}
	if p.Kind != KindAgent {
		return fmt.Errorf("%s is not a driver", p.Name)
	}
	return s.Mail.Post(to, Message{From: AuthorUser, Text: text})
}

// Drivers lists the participants a message can be sent to, connected first.
// The disconnected are still listed, because their mail keeps.
func (s *Server) Drivers() []Participant {
	var out []Participant
	for _, p := range s.Participants.List() {
		if p.Kind == KindAgent {
			out = append(out, p)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Connected && !out[j].Connected })
	return out
}

// DefaultPath is the Unix socket for this process: one per raj, so two editors do
// not collide and neither inherits a dead one's path.
//
// Per-process rather than per-workspace because a workspace path has to be
// guessed at by both sides and is wrong the moment two raj instances open the
// same repository. Discovery is the other direction instead: list the directory
// and ask each socket what root it holds, which is one round trip and cannot be
// stale.
func DefaultPath() string {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "raj-"+strconv.Itoa(os.Getuid()))
	} else {
		dir = filepath.Join(dir, "raj")
	}
	return filepath.Join(dir, strconv.Itoa(os.Getpid())+".sock")
}

// Listen starts a server on a Unix path or a `tcp://host:port` address.
//
// For a Unix socket the directory is created 0700 and the socket 0600:
// authorisation is the filesystem, so anything the user can run can drive the
// editor — the same trust boundary as the user's own shell, and the reason this
// is off unless asked for.
//
// A TCP listener has no filesystem to lean on, so it mints a token instead and
// refuses any request that does not carry it. See addr.go for why that is the
// same check rather than a new one.
func Listen(addr string, notify func()) (*Server, error) {
	if notify == nil {
		return nil, errors.New("control: Notify is required")
	}
	if network, address := ParseAddr(addr); network == "tcp" {
		return listenTCP(address, notify)
	}
	return listenUnix(addr, notify)
}

func listenUnix(path string, notify func()) (*Server, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	// A leftover socket from a crashed process of the same pid would make Listen
	// fail. Removing one that is still live would steal it, so probe first.
	if _, err := os.Stat(path); err == nil {
		if c, derr := net.DialTimeout("unix", path, 200*time.Millisecond); derr == nil {
			c.Close()
			return nil, fmt.Errorf("control: %s is already in use", path)
		}
		os.Remove(path)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	s := &Server{Notify: notify, path: path, network: "unix", ln: ln, Participants: NewRegistry()}
	go s.accept()
	return s, nil
}

func listenTCP(address string, notify func()) (*Server, error) {
	// The environment wins so the token can be pinned: a container is started
	// with its environment already fixed, and a token the editor invented after
	// the fact cannot be got into one without restarting it.
	token := os.Getenv(TokenEnv)
	if token == "" {
		var err error
		if token, err = NewToken(); err != nil {
			return nil, err
		}
	}
	ln, err := net.Listen("tcp", address)
	if err != nil {
		return nil, err
	}
	// Path reports the resolved address, not the requested one, so a port of 0
	// — which is what a test wants, and what avoids a collision — comes back as
	// something a client can actually dial.
	s := &Server{Notify: notify, path: TCPAddr(ln.Addr()), network: "tcp",
		token: token, ln: ln, Participants: NewRegistry()}
	go s.accept()
	return s, nil
}

// Path is where the server is listening, in the form a client can dial.
func (s *Server) Path() string { return s.path }

// Token is the secret a TCP client must present, and empty for a Unix socket.
func (s *Server) Token() string { return s.token }

// Remote reports whether this server is reachable off this process's
// filesystem, which is what makes a request untrusted enough to need a token
// and an `exec` worth refusing.
func (s *Server) Remote() bool { return s.network == "tcp" }

func (s *Server) accept() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return // closed
		}
		go s.serve(conn)
	}
}

func (s *Server) serve(conn net.Conn) {
	defer conn.Close()
	// Each connection is one writer, assigned an author id for its lifetime.
	// Attribution is therefore a property of who is connected rather than of
	// what a request claims, and two agents cannot land in one another's spans.
	// Provisional until the client identifies itself. A connection that never
	// says who it is still gets an id, so an anonymous one-off client works —
	// it just does not survive a reconnect as the same writer.
	author := s.nextAuthor()
	defer s.Participants.Leave(author)

	// Reading and writing are separated because a streamed search takes
	// seconds and a cancel has to arrive during it. A loop that read a frame,
	// handled it, and wrote the reply could not do that: it would be inside
	// the handler when the cancel came. So the reader only reads, handlers run
	// concurrently, and one writer goroutine owns the socket — concurrent
	// writes would interleave two frames into one unparseable stream.
	out := make(chan outFrame, 64)
	done := make(chan struct{})
	safe.Go(func() {
		defer close(done)
		for f := range out {
			if WriteFrame(conn, f.h, f.body) != nil {
				return
			}
		}
	})

	c := &connection{srv: s, out: out, author: author, running: map[int]context.CancelFunc{}}
	r := bufio.NewReader(conn)
	var wg sync.WaitGroup
	for {
		f, err := ReadFrame(r)
		if err != nil {
			break // EOF, or a length that did not parse: the connection is over
		}
		req, derr := DecodeRequest(f)
		if derr != nil {
			// A frame that arrived whole but decoded badly is answered rather
			// than dropped: the client is waiting, and the id is readable even
			// when the body is not.
			c.send(Response{ID: f.Header.ID, Err: derr.Error(), Final: true})
			continue
		}
		if !s.authorised(req) {
			// One refusal, then the connection ends. The token is 32 random
			// bytes, so this is not rate limiting against a guesser — it is
			// refusing to stay in a conversation with something that cannot
			// say who it is.
			c.send(Response{ID: req.ID, Err: errUnauthorised, Final: true})
			break
		}
		if req.Op == "exec" && s.Remote() && !s.AllowRemoteExec {
			c.send(Response{ID: req.ID, Err: errRemoteExec, Final: true})
			continue
		}
		if req.Op == "hello" {
			// Answered on the reading goroutine: it rebinds this connection's
			// author id and touches no document state.
			id, err := s.Participants.Join(req.Identity, req.Name, KindAgent)
			if err != nil {
				c.send(Response{ID: req.ID, Err: err.Error(), Final: true})
				continue
			}
			s.Participants.Leave(author)
			author = id
			c.mu.Lock()
			c.author = id
			c.mu.Unlock()
			c.send(Response{ID: req.ID, OK: true, Final: true,
				Participants: s.Participants.List()})
			continue
		}
		if req.Op == "cancel" {
			// Handled here, on the reading goroutine, without the editor: the
			// point of a cancel is that it does not queue behind the work it
			// is cancelling.
			c.cancel(req.Cancel)
			c.send(Response{ID: req.ID, OK: true, Final: true})
			continue
		}
		if req.Author == 0 {
			req.Author = author
		}
		wg.Add(1)
		safe.Go(func() { defer wg.Done(); c.handle(req) })
	}
	c.cancelAll()
	wg.Wait()
	close(out)
	<-done
}

type outFrame struct {
	h    Header
	body []byte
}

// connection is one client's state: its author id, its in-flight cancellations,
// and the single channel its frames leave by.
type connection struct {
	srv    *Server
	out    chan outFrame
	author uint8

	mu      sync.Mutex
	running map[int]context.CancelFunc
	closed  bool
}

func (c *connection) send(res Response) {
	// Stamped here rather than by each handler, so no verb can forget it and
	// none can claim a different one.
	c.mu.Lock()
	res.Author = c.author
	c.mu.Unlock()
	h, body := EncodeResponse(res)
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return
	}
	defer func() {
		// The writer goroutine closes out when the connection ends; a handler
		// still streaming into it would panic. Dropping the frame is right:
		// there is nobody to read it.
		_ = recover()
	}()
	c.out <- outFrame{h, body}
}

func (c *connection) cancel(id int) {
	c.mu.Lock()
	stop := c.running[id]
	c.mu.Unlock()
	if stop != nil {
		stop()
	}
}

func (c *connection) cancelAll() {
	c.mu.Lock()
	c.closed = true
	for _, stop := range c.running {
		stop()
	}
	c.running = map[int]context.CancelFunc{}
	c.mu.Unlock()
}

// handle runs one request. A streaming one emits several frames; everything
// else emits one.
func (c *connection) handle(req Request) {
	if req.Op == "prog" {
		c.program(req)
		return
	}
	c.one(req, c.send)
}

// one runs a single request and emits its responses through emit.
//
// The indirection exists for batching. A streaming verb marks its own last
// frame Final, which is right when it is the whole request and wrong when it is
// the third of five in a program — a client that saw Final would stop reading
// while four verbs were still to come. So who gets to say Final is the caller's
// decision, and the streaming handlers no longer reach for the socket directly.
func (c *connection) one(req Request, emit func(Response)) {
	switch req.Op {
	case "search":
		c.search(req, emit)
		return
	case "exec":
		c.exec(req, emit)
		return
	case "recv":
		c.recv(req, emit)
		return
	}
	res := c.srv.submit(req)
	res.Final = true
	emit(res)
}

// program compiles a batch and runs it in order.
//
// Each sub-request goes through the ordinary path, so a program cannot reach
// anything a JSON frame could not and needs no second set of handlers. They run
// sequentially rather than concurrently, which is the whole reason to batch:
// fifty splices against one version have to land in the order they were written
// or the offsets in the later ones mean nothing.
//
// One response per verb, with Final on the last, so a client can match answers
// to verbs positionally. A compile error is one response and nothing runs:
// half a batch is the outcome a caller can neither detect nor undo.
func (c *connection) program(req Request) {
	reqs, err := Requests(req.Program, req.Author)
	if err != nil {
		c.send(Response{ID: req.ID, Err: err.Error(), Final: true})
		return
	}
	for i, sub := range reqs {
		if sub.ID == 0 {
			sub.ID = req.ID
		}
		sub.Token = req.Token
		last := i == len(reqs)-1
		c.one(sub, func(res Response) {
			// Only the batch's last frame ends it. A streaming verb in the
			// middle still emits every batch it found; what it does not get to
			// do is tell the client the conversation is over.
			res.Final = res.Final && last
			c.send(res)
		})
	}
}

// recv parks until the user has something to say to this connection.
//
// It never reaches the event thread. There is nothing to ask the editor — the
// mailbox is on the server and the editor writes into it — and a request that
// parked on the event thread would park the editor with it.
//
// The recipient is the connection's own author id, read at the moment it parks.
// A connection that says `hello` afterwards has changed identity, and the fix
// for that is to say hello first, which every driver does anyway: rebinding a
// request that is already waiting would deliver one participant's mail to
// another.
func (c *connection) recv(req Request, emit func(Response)) {
	ctx, stop := context.WithCancel(context.Background())
	c.mu.Lock()
	c.running[req.ID] = stop
	to := c.author
	c.mu.Unlock()
	defer func() {
		stop()
		c.mu.Lock()
		delete(c.running, req.ID)
		c.mu.Unlock()
	}()

	msgs, ok := c.srv.Mail.Wait(ctx, to)
	if !ok {
		// Cancelled, or the connection went away. Answered rather than
		// dropped: a client that cancelled is waiting for the frame that says
		// its id is finished, and one that hung up will never see this.
		emit(Response{ID: req.ID, Err: "cancelled", Final: true})
		return
	}
	emit(Response{ID: req.ID, OK: true, Final: true, Messages: msgs})
}

// exec runs a command, in the same two phases as search: the decision is made
// on the event thread, because it reads the buffers, and the command then runs
// here so a slow one neither blocks the editor nor becomes uncancellable.
func (c *connection) exec(req Request, emit func(Response)) {
	check := c.srv.submit(Request{ID: req.ID, Op: "execcheck", Argv: req.Argv, Dir: req.Dir})
	if check.Err != "" {
		check.ID, check.Final = req.ID, true
		emit(check)
		return
	}

	ctx, stop := context.WithCancel(context.Background())
	c.mu.Lock()
	c.running[req.ID] = stop
	c.mu.Unlock()
	defer func() {
		stop()
		c.mu.Lock()
		delete(c.running, req.ID)
		c.mu.Unlock()
	}()

	code, err := Run(ctx, req.Argv, req.Dir, func(stream uint8, b []byte) {
		emit(Response{ID: req.ID, OK: true, Stream: stream, Out: string(b)})
	})
	// Dirty is carried to the result, not used to refuse: the caller needs it
	// to judge whether a failure is about the code it is looking at.
	final := Response{ID: req.ID, OK: err == nil, Exit: code, Dirty: check.Dirty, Final: true}
	if err != nil {
		final.Err = err.Error()
	}
	if ctx.Err() != nil {
		final.Err = "cancelled"
		final.OK = false
	}
	emit(final)
}

// search is the streaming path, and the only op that leaves the event thread.
//
// Two phases. The snapshot is taken on the event thread, because reading the
// open buffers is reading the model. The walk then runs here, off it — which is
// why a multi-second search does not freeze the editor, and why a cancel can be
// serviced while it runs.
func (c *connection) search(req Request, emit func(Response)) {
	if req.Query == nil {
		emit(Response{ID: req.ID, Err: "search needs a query", Final: true})
		return
	}
	snap := c.srv.submit(Request{ID: req.ID, Op: "snapshot"})
	if snap.Err != "" || snap.Searcher == nil {
		if snap.Err == "" {
			snap.Err = "search is not available"
		}
		emit(Response{ID: req.ID, Err: snap.Err, Final: true})
		return
	}

	ctx, stop := context.WithCancel(context.Background())
	c.mu.Lock()
	c.running[req.ID] = stop
	c.mu.Unlock()
	defer func() {
		stop()
		c.mu.Lock()
		delete(c.running, req.ID)
		c.mu.Unlock()
	}()

	files, capped, err := snap.Searcher.Search(ctx, *req.Query, func(batch []SearchMatch) {
		emit(Response{ID: req.ID, OK: true, Matches: batch})
	})
	final := Response{ID: req.ID, OK: err == nil, Files: files, Capped: capped, Final: true}
	if err != nil {
		final.Err = err.Error()
	}
	if ctx.Err() != nil {
		final.Err = "cancelled"
		final.OK = false
	}
	emit(final)
}

// The refusals a remote client can hit that a local one cannot. Both are
// strings a person will read at a terminal, and both say what to do.
const (
	errUnauthorised = "unauthorized: set " + TokenEnv + " to the token raj printed when it started"
	errRemoteExec   = "exec is refused over TCP: the command would run on the editor's machine, " +
		"outside your sandbox — run it with your own shell instead, or start raj with --control-exec"
)

// authorised checks a request's token against the server's. Constant time, so
// the comparison does not leak the token a byte at a time; free, since it runs
// once per request against 64 characters.
func (s *Server) authorised(req Request) bool {
	if s.token == "" {
		return true
	}
	return subtle.ConstantTimeCompare([]byte(req.Token), []byte(s.token)) == 1
}

// nextAuthor hands out ids from Agent upward. Author 0 is the file as loaded
// and 1 is the human, so a connection never gets either.
// nextAuthor gives an unidentified connection a provisional id.
func (s *Server) nextAuthor() uint8 {
	id, err := s.Participants.Join(fmt.Sprintf("anon-%d", s.anonSeq()), "", KindAgent)
	if err != nil {
		return FirstAgent
	}
	return id
}

func (s *Server) anonSeq() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.anon++
	return s.anon
}

// submit parks a request and waits for the event thread to answer it.
func (s *Server) submit(req Request) Response {
	p := &Pending{Req: req, done: make(chan Response, 1)}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return Response{ID: req.ID, Err: "editor is shutting down"}
	}
	s.queue = append(s.queue, p)
	s.mu.Unlock()

	s.Notify()
	select {
	case r := <-p.done:
		return r
	case <-time.After(ReplyTimeout):
		// Mark it answered so the event thread's later Reply does not block on
		// a channel nobody is reading.
		p.once.Do(func() {})
		return Response{ID: req.ID, Err: "timed out waiting for the editor"}
	}
}

// Take removes and returns every parked request. Called from the event thread.
func (s *Server) Take() []*Pending {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.queue) == 0 {
		return nil
	}
	out := s.queue
	s.queue = nil
	return out
}

// Close stops listening, removes the socket, and fails anything still parked so
// no client is left waiting on an editor that has gone.
func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	parked := s.queue
	s.queue = nil
	s.mu.Unlock()

	err := s.ln.Close()
	if s.network != "tcp" {
		os.Remove(s.path)
	}
	for _, p := range parked {
		p.Fail("editor is shutting down")
	}
	return err
}
