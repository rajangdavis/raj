package control

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Client speaks the control protocol to a running editor.
//
// It lives here rather than in the consumer so that the request and response
// types have exactly one definition. A bridge that redeclared them would drift
// from the server the first time a field was added, and the failure would be a
// silently missing field rather than a compile error.
type Client struct {
	conn net.Conn
	r    *bufio.Reader

	// Two locks, not one. mu sequences requests — it is held for a whole
	// exchange, which for a streamed search is however long the walk takes.
	// wmu guards writes to the socket only, so Cancel can send while a stream
	// is still collecting under mu. One lock deadlocked exactly there: the
	// cancel could not be written because the thing it was cancelling held it.
	mu  sync.Mutex
	wmu sync.Mutex
	id  int
	// author is the id the editor assigned this connection, learned from the
	// first response rather than asked for.
	author atomic.Uint32
	// cur is the id being collected, readable without either lock so that
	// CancelCurrent works from another goroutine.
	cur atomic.Int64

	// token is presented on every request; empty for a Unix socket.
	token string
	// remote records that this connection is TCP, which is the only case where
	// the two ends can have different filesystems.
	remote bool
	// paths translates between this process's view of the tree and the
	// editor's. Applied here, at the transport edge, rather than at each call
	// site: there are a dozen fields carrying a path in either direction and a
	// caller that forgot one would send an unmapped path that the editor
	// refuses for a reason unrelated to what went wrong.
	paths Mapper
	// readTimeout replaces answerBudget for this connection when non-zero.
	// It is a test seam: production leaves it zero, so the shipped wait is the
	// const, while a test can prove a silent peer is caught in milliseconds.
	readTimeout time.Duration
	// idleTimeout replaces streamIdle for this connection when non-zero. It is
	// the streaming verbs' test seam, so a test can prove a peer that goes
	// silent mid-stream is caught in milliseconds.
	idleTimeout time.Duration
}

// Per-verb answer budgets. A process squatting the control address accepts the
// TCP connection and then says nothing; the connect timeout cannot see that,
// so without a post-connect deadline the client parks in collect forever and
// the driver hangs with no diagnosis. One budget for every verb was too blunt:
// ping is the identity check and should name a squatter quickly, while a batch
// wrapping a long exec or a cold language server legitimately takes longer.
const (
	answerPing    = 3 * time.Second
	answerDefault = 10 * time.Second
	answerProg    = 60 * time.Second
	answerLSP     = 30 * time.Second
)

// answerBudget is how long op may wait for an answer before the
// silent-peer diagnosis fires. Pure and table-driven, so the per-verb policy
// can be tested without a connection.
func answerBudget(op string) time.Duration {
	switch op {
	case "ping":
		return answerPing
	case "prog":
		return answerProg
	case "lsp":
		return answerLSP
	default:
		return answerDefault
	}
}

// longPoll reports whether op is expected to block until something outside the
// editor happens rather than until the editor answers. recv waits for the user,
// which may be hours, so arming the answer deadline on it would turn a working
// long-poll into a spurious timeout. It is the only such verb, and the only
// exemption Do needs: search and exec stream through DoStream and DoExec, which
// arm a per-frame idle instead of a total deadline.
func longPoll(op string) bool { return op == "recv" }

// answerWait returns the read deadline to arm for op, and whether to arm one.
// A long poll gets none, whatever the configured timeout is. The readTimeout
// test seam, when set, stands in for the budget of every bounded verb.
func (c *Client) answerWait(op string) (time.Duration, bool) {
	if longPoll(op) {
		return 0, false
	}
	if c.readTimeout > 0 {
		return c.readTimeout, true
	}
	return answerBudget(op), true
}

// streamIdle bounds the gap between frames on a streaming verb. A search over a
// large tree or a long exec can run for minutes, so a total deadline would kill
// a healthy stream; an idle deadline fires only when the peer actually stops
// sending. Do arms a total answer deadline instead and passes 0 to collectAll.
//
// It is three heartbeat intervals. The server emits a contentless non-final
// frame from heartbeatEvery while a request is in flight, so a quiet-but-live
// stream is kept alive by those frames and a peer that sends nothing at all
// still trips this idle. Derived from heartbeatEvery rather than repeating the
// number so the two cannot drift out of step.
const streamIdle = 3 * heartbeatEvery

// streamWait returns the idle deadline for a streaming verb, with the
// idleTimeout test seam standing in for streamIdle when set.
func (c *Client) streamWait() time.Duration {
	if c.idleTimeout > 0 {
		return c.idleTimeout
	}
	return streamIdle
}

// silentPeerErr names the wrong-process diagnosis a read deadline produces: the
// peer answered no frame in time, which for a connected socket means the
// process on the other end is not the editor. It wraps the deadline error so
// isTimeout still recognises the underlying condition.
func silentPeerErr(wait time.Duration, err error) error {
	return fmt.Errorf("no response from the control address within %s; "+
		"another process may be listening there: %w", wait, err)
}

// isTimeout reports whether err is a read deadline that expired rather than an
// ordinary transport failure. net returns an *net.OpError wrapping
// os.ErrDeadlineExceeded, so the sentinel catches the wrapped form while
// net.Error.Timeout catches a peer that reports the same condition its own way.
func isTimeout(err error) bool {
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// Dial connects to a Unix path or a `tcp://host:port` address.
func Dial(addr string) (*Client, error) {
	network, address := ParseAddr(addr)
	conn, err := net.DialTimeout(network, address, 2*time.Second)
	if err != nil {
		return nil, err
	}
	return &Client{conn: conn, r: bufio.NewReader(conn),
		token: os.Getenv(TokenEnv), remote: network == "tcp"}, nil
}

// SetMapper installs a path translation. Nothing is rewritten by default.
func (c *Client) SetMapper(m Mapper) { c.paths = m }

// Mapper is the translation in force, for a caller that wants to report it.
func (c *Client) Mapper() Mapper { return c.paths }

// ResolveRoots settles how paths should be translated for this connection, and
// returns the mapping it chose.
//
// RAJ_ROOT_MAP wins, on any transport. Otherwise inference runs only over TCP:
// a Unix socket is a filesystem object, so reaching one is proof that both ends
// share the filesystem and that every path already means the same thing. That
// restriction is not a shortcut, it is the correctness argument — inferring on
// a shared filesystem is how `raj ctl read /Users/x/proj/a.go`, run from the
// home directory above it, becomes a path with the project name in it twice.
//
// Over TCP it asks the editor for its root and checks whether that path exists
// here. If it does, nothing is rewritten. If it does not, this process is
// somewhere else — a container — and its own workspace root stands in.
//
// One round trip, on a connection that is about to make several anyway.
func (c *Client) ResolveRoots(cwd string) (Mapper, error) {
	m, err := MapperFromEnv()
	if err != nil {
		return Mapper{}, err
	}
	if m.Active() {
		c.paths = m
		return m, nil
	}
	if !c.remote {
		return Mapper{}, nil
	}
	res, err := c.Do(Request{Op: "ping"})
	if err != nil {
		return Mapper{}, err
	}
	c.paths = inferMapper(cwd, res.Root)
	return c.paths, nil
}

func (c *Client) Close() error { return c.conn.Close() }

// Do sends one request and returns the editor's answer. A transport failure is
// an error; a refusal by the editor is a Response with Err set, because those
// are different things to a caller: one means retry, the other means do not.
//
// An ordinary exchange is bounded by answerBudget, so a silent peer (a wrong
// process on the control address) yields a named error instead of a hang.
// recv is exempt because parking is its purpose, and the deadline is always
// cleared before returning so a later recv on this client still waits.
func (c *Client) Do(req Request) (Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.id++
	req.ID = c.id
	c.cur.Store(int64(req.ID))
	defer c.cur.Store(0)
	if err := c.write(req); err != nil {
		return Response{}, err
	}
	wait, bounded := c.answerWait(req.Op)
	if bounded {
		if err := c.conn.SetReadDeadline(time.Now().Add(wait)); err != nil {
			return Response{}, err
		}
		defer func() { _ = c.conn.SetReadDeadline(time.Time{}) }()
	}
	// A streamed response is several frames sharing an id, the last marked
	// Final. Do collects them, so a caller that does not care about progress
	// writes nothing extra; DoStream is for one that does.
	res, err := c.collect(req.ID, nil, 0)
	if err != nil {
		if bounded && isTimeout(err) {
			return Response{}, silentPeerErr(wait, err)
		}
		return Response{}, err
	}
	return res, nil
}

// DoStream sends a request and calls onBatch with each streamed batch of
// matches as it arrives, returning the final frame. Cancel abandons it. A gap
// between frames longer than the stream idle names the silent peer, so a
// stalled walk is caught while a long but live one survives.
func (c *Client) DoStream(req Request, onBatch func([]SearchMatch)) (Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.id++
	req.ID = c.id
	c.cur.Store(int64(req.ID))
	defer c.cur.Store(0)
	if err := c.write(req); err != nil {
		return Response{}, err
	}
	return c.collect(req.ID, onBatch, c.streamWait())
}

// write serialises one frame onto the socket. Separate from mu so a cancel can
// overtake the request it cancels.
//
// It is also the single outbound path seam: every request field that names a
// path is translated here and nowhere else, so a verb cannot be added that
// forgets one. The three spellings a caller may use all mean the same file — a
// relative path, which the editor resolves against its own workspace root; an
// absolute path in this process's view, rewritten through the map; and one
// already in the editor's spelling, left alone because it is not under the
// local root. Response paths come back the other way in localise.
func (c *Client) write(req Request) error {
	if req.Token == "" {
		req.Token = c.token
	}
	req.Path = c.toEditor(req.Path)
	req.NewPath = c.toEditor(req.NewPath)
	req.Dir = c.toEditor(req.Dir)
	// claim names a list of files rather than the one Path, so each operand is
	// translated too: a container calling the editor at /workspace would
	// otherwise hand over paths the editor refuses as outside its root.
	for i := range req.Paths {
		req.Paths[i] = c.toEditor(req.Paths[i])
	}
	// search -path scopes the walk, and the editor validates it against its own
	// root; a caller's absolute spelling has to be translated exactly as Path
	// is, or it is refused as "outside the workspace".
	if req.Query != nil {
		req.Query.Path = c.toEditor(req.Query.Path)
	}
	h, body := EncodeRequest(req)
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return WriteFrame(c.conn, h, body)
}

// toEditor maps one path from this process's view into the editor's. It is the
// outbound half of the path seam, applied only here so the verbs cannot
// disagree. A relative or empty path is returned unchanged: the editor resolves
// a relative path against its own workspace root, and "" means the buffer the
// user is looking at.
func (c *Client) toEditor(p string) string { return c.paths.ToEditor(p) }

// Cancel abandons an in-flight request by id, without waiting for it — which is
// the whole point, and why it does not take the sequencing lock.
func (c *Client) Cancel(id int) error {
	return c.write(Request{ID: -id, Op: "cancel", Cancel: id})
}

// CancelCurrent abandons whatever this client is collecting, if anything. It is
// what a caller reaching for ctrl+C wants, and it needs no id bookkeeping.
func (c *Client) CancelCurrent() error {
	if id := int(c.cur.Load()); id != 0 {
		return c.Cancel(id)
	}
	return nil
}

// collect reads frames until the one marked Final, merging streamed matches.
// idle, when non-zero, bounds the gap between frames; Do passes 0 because it
// already armed a total answer deadline.
func (c *Client) collect(id int, onBatch func([]SearchMatch), idle time.Duration) (Response, error) {
	return c.collectAll(id, onBatch, nil, idle)
}

// DoExec runs a command, calling onOutput with each chunk as it arrives. A gap
// between frames longer than the stream idle names the silent peer, so a long
// command is not killed while a stalled one is.
func (c *Client) DoExec(req Request, onOutput func(stream uint8, b string)) (Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.id++
	req.ID = c.id
	c.cur.Store(int64(req.ID))
	defer c.cur.Store(0)
	if err := c.write(req); err != nil {
		return Response{}, err
	}
	return c.collectAll(req.ID, nil, onOutput, c.streamWait())
}

func (c *Client) collectAll(id int, onBatch func([]SearchMatch),
	onOutput func(stream uint8, b string), idle time.Duration) (Response, error) {
	// A streaming verb gets an idle deadline, reset before each frame read so a
	// long but live walk is not killed while a stalled peer is. It is cleared
	// on the way out, whatever the outcome, so a later recv on this client
	// still parks. Do passes 0 because it armed a total deadline itself.
	if idle > 0 {
		defer func() { _ = c.conn.SetReadDeadline(time.Time{}) }()
	}
	var acc Response
	for {
		if idle > 0 {
			if err := c.conn.SetReadDeadline(time.Now().Add(idle)); err != nil {
				return Response{}, err
			}
		}
		f, err := ReadFrame(c.r)
		if err != nil {
			if idle > 0 && isTimeout(err) {
				return Response{}, silentPeerErr(idle, err)
			}
			return Response{}, err
		}
		res, err := DecodeResponse(f)
		if err != nil {
			return Response{}, err
		}
		if res.Author != 0 {
			c.author.Store(uint32(res.Author))
		}
		c.localise(&res)
		if res.ID != id {
			// A cancel's own acknowledgement, or a reply to something else.
			// Ignore rather than fail: ids are how frames are matched.
			continue
		}
		if res.Stream != 0 {
			if onOutput != nil {
				onOutput(res.Stream, res.Out)
			}
			acc.Out += res.Out
		}
		if len(res.Matches) > 0 {
			if onBatch != nil {
				onBatch(res.Matches)
			}
			acc.Matches = append(acc.Matches, res.Matches...)
		}
		if res.Final {
			matches, out := acc.Matches, acc.Out
			acc = res
			acc.Matches, acc.Out = matches, out
			return acc, nil
		}
	}
}

// localise rewrites every path in a response back into the caller's view, so
// what comes out of `search` or `buffers` is something the caller can open with
// its own tools.
func (c *Client) localise(res *Response) {
	if !c.paths.Active() {
		return
	}
	res.Root = c.paths.FromEditor(res.Root)
	if res.SnapshotPath != "" {
		res.SnapshotPath = c.paths.FromEditor(res.SnapshotPath)
	}
	for i := range res.Buffers {
		res.Buffers[i].Path = c.paths.FromEditor(res.Buffers[i].Path)
	}
	for i := range res.Matches {
		res.Matches[i].Path = c.paths.FromEditor(res.Matches[i].Path)
	}
	for i := range res.Dirty {
		res.Dirty[i].Path = c.paths.FromEditor(res.Dirty[i].Path)
	}
	for i := range res.Groups {
		res.Groups[i].Path = c.paths.FromEditor(res.Groups[i].Path)
	}
	for i := range res.Truncated {
		res.Truncated[i].Path = c.paths.FromEditor(res.Truncated[i].Path)
	}
	// The claim answer names files the caller asked about, so they come back in
	// its spelling too; a warning is prose built from an editor absolute path,
	// so it goes through the same root-prefix rewrite Err uses.
	for i := range res.Claims {
		res.Claims[i] = c.paths.FromEditor(res.Claims[i])
	}
	for i := range res.ClaimWarnings {
		res.ClaimWarnings[i] = c.localiseErr(res.ClaimWarnings[i])
	}
	for i := range res.ClaimOverlaps {
		res.ClaimOverlaps[i].Path = c.paths.FromEditor(res.ClaimOverlaps[i].Path)
	}
	for i := range res.Deletions {
		res.Deletions[i].Path = c.paths.FromEditor(res.Deletions[i].Path)
	}
	for i := range res.DirRemovals {
		res.DirRemovals[i].Path = c.paths.FromEditor(res.DirRemovals[i].Path)
	}
	// An ls entry's path is an editor path; the caller sees its own spelling
	// of the same file, so it is rebased like every other path a reply names.
	for i := range res.Entries {
		res.Entries[i].Path = c.paths.FromEditor(res.Entries[i].Path)
	}
	for i := range res.Proposals {
		res.Proposals[i].Path = c.paths.FromEditor(res.Proposals[i].Path)
	}
	// DiffJSON is a nested document, not a path: unmarshal it, rebase the
	// Group paths it carries, and marshal it back. A string replace inside the
	// encoded JSON would rewrite a hunk's Old or New text, which is content.
	if res.DiffJSON != "" {
		var diffs []DiffGroup
		if err := json.Unmarshal([]byte(res.DiffJSON), &diffs); err == nil {
			for i := range diffs {
				diffs[i].Path = c.paths.FromEditor(diffs[i].Path)
			}
			if b, err := json.Marshal(diffs); err == nil {
				res.DiffJSON = string(b)
			}
		}
	}
	// LSPJSON nests the same way: a definition, reference or workspace-symbol
	// answer carries caller-visible locations. Unmarshal, rebase the Locations
	// and Symbols, marshal back. Hover text is content, not a path, so only the
	// path-bearing fields are rewritten.
	if res.LSPJSON != "" {
		var lsp LSPResult
		if err := json.Unmarshal([]byte(res.LSPJSON), &lsp); err == nil {
			for i := range lsp.Locations {
				lsp.Locations[i].Path = c.paths.FromEditor(lsp.Locations[i].Path)
			}
			for i := range lsp.Symbols {
				lsp.Symbols[i].Path = c.paths.FromEditor(lsp.Symbols[i].Path)
			}
			if b, err := json.Marshal(lsp); err == nil {
				res.LSPJSON = string(b)
			}
		}
	}
	// Err is prose, not a path, but the editor builds it from absolute paths.
	// Rewrite the mapped root at a path boundary so a refusal names the
	// caller's spelling, and leave a longer name that merely starts with the
	// root's characters alone.
	res.Err = c.localiseErr(res.Err)
}

// localiseErr rewrites the editor root wherever it appears as a whole path
// prefix in a message, leaving every other byte of the prose intact. Only call
// it with an active mapper: it assumes Editor and Local are distinct.
func (c *Client) localiseErr(s string) string {
	root := c.paths.Editor
	if root == "" {
		return s
	}
	var b strings.Builder
	for {
		i := strings.Index(s, root)
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		end := i + len(root)
		b.WriteString(s[:i])
		// A path boundary is the end of the string, a separator, or any byte
		// that cannot extend a path name; anything else means the root is only
		// a character prefix of a longer sibling name.
		if end == len(s) || s[end] == '/' || !pathByte(s[end]) {
			b.WriteString(c.paths.Local)
		} else {
			b.WriteString(s[i:end])
		}
		s = s[end:]
	}
}

// pathByte reports whether b may appear in a path name, so a root followed by
// one is the start of a longer name rather than the root itself.
func pathByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' ||
		b >= '0' && b <= '9' || b == '.' || b == '_' || b == '-' ||
		b == '~' || b == '+'
}

// Author is the id this connection writes as, or 0 before the first exchange.
// A caller compares span authors against it to tell its own text from the
// user's and from another agent's.
func (c *Client) Author() uint8 { return uint8(c.author.Load()) }

// Instance is a running editor found by Discover.
type Instance struct {
	Socket string
	Root   string
	PID    int
}

// Discover finds running editors by listing the socket directory and asking
// each one what it holds. Unix sockets only: a TCP editor is somewhere there is
// no directory to list, which is why an address for one has to be given rather
// than found.
//
// Asking is the point. A socket path cannot say which workspace is behind it —
// a per-workspace naming scheme would have to encode one, and would be wrong the
// moment two editors opened the same repository — so discovery is a round trip
// per candidate instead. Sockets that refuse a connection are skipped rather
// than reported: a crashed editor leaves its path behind, and a caller looking
// for a live editor does not care.
func Discover() []Instance {
	dir := filepath.Dir(DefaultPath())
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []Instance
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".sock" {
			continue
		}
		path := filepath.Join(dir, e.Name())
		c, err := Dial(path)
		if err != nil {
			// A killed editor leaves its socket file behind. Nothing else will
			// ever clean it up — the process that would have is the one that
			// died — so the next caller to notice does it. A unix socket with
			// no listener refuses immediately, so this cannot reap a live one
			// that is merely busy.
			os.Remove(path)
			continue
		}
		res, err := c.Do(Request{Op: "ping"})
		c.Close()
		if err != nil || !res.OK {
			continue
		}
		out = append(out, Instance{Socket: path, Root: res.Root, PID: res.PID})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Socket < out[j].Socket })
	return out
}

// Locate picks the socket to talk to.
//
// Explicit beats implicit throughout: an argument, then RAJ_CONTROL_ADDR or its
// older spelling RAJ_SOCKET, then
// discovery. Discovery prefers an editor whose root contains the working
// directory, since a bridge is normally started inside the project it is meant
// to drive. Several matches is an error rather than a guess — editing the wrong
// repository is not a mistake worth being convenient about.
func Locate(explicit, cwd string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if env := os.Getenv(AddrEnv); env != "" {
		return env, nil
	}
	if env := os.Getenv("RAJ_SOCKET"); env != "" {
		return env, nil
	}
	found := Discover()
	switch len(found) {
	case 0:
		return "", fmt.Errorf("no running raj found in %s; start one with --control, "+
			"or set %s (a path, or tcp://host:port for a raj on another machine "+
			"or outside this container)", filepath.Dir(DefaultPath()), AddrEnv)
	case 1:
		return found[0].Socket, nil
	}
	var matching []Instance
	for _, in := range found {
		if cwd != "" && within(in.Root, cwd) {
			matching = append(matching, in)
		}
	}
	if len(matching) == 1 {
		return matching[0].Socket, nil
	}
	candidates := found
	if len(matching) > 1 {
		candidates = matching
	}
	msg := "several raj instances are running; set " + AddrEnv + " to one of:"
	for _, in := range candidates {
		msg += "\n  " + in.Socket + "  " + in.Root
	}
	return "", fmt.Errorf("%s", msg)
}

// within reports whether path is root or inside it.
func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (len(rel) < 2 || rel[:2] != "..")
}
