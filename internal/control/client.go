package control

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
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
}

// Dial connects to a socket.
func Dial(path string) (*Client, error) {
	conn, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		return nil, err
	}
	return &Client{conn: conn, r: bufio.NewReader(conn)}, nil
}

func (c *Client) Close() error { return c.conn.Close() }

// Do sends one request and returns the editor's answer. A transport failure is
// an error; a refusal by the editor is a Response with Err set, because those
// are different things to a caller: one means retry, the other means do not.
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
	// A streamed response is several frames sharing an id, the last marked
	// Final. Do collects them, so a caller that does not care about progress
	// writes nothing extra; DoStream is for one that does.
	res, err := c.collect(req.ID, nil)
	if err != nil {
		return Response{}, err
	}
	return res, nil
}

// DoStream sends a request and calls onBatch with each streamed batch of
// matches as it arrives, returning the final frame. Cancel abandons it.
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
	return c.collect(req.ID, onBatch)
}

// write serialises one frame onto the socket. Separate from mu so a cancel can
// overtake the request it cancels.
func (c *Client) write(req Request) error {
	h, body := EncodeRequest(req)
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return WriteFrame(c.conn, h, body)
}

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
func (c *Client) collect(id int, onBatch func([]SearchMatch)) (Response, error) {
	return c.collectAll(id, onBatch, nil)
}

// DoExec runs a command, calling onOutput with each chunk as it arrives.
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
	return c.collectAll(req.ID, nil, onOutput)
}

func (c *Client) collectAll(id int, onBatch func([]SearchMatch),
	onOutput func(stream uint8, b string)) (Response, error) {
	var acc Response
	for {
		f, err := ReadFrame(c.r)
		if err != nil {
			return Response{}, err
		}
		res, err := DecodeResponse(f)
		if err != nil {
			return Response{}, err
		}
		if res.Author != 0 {
			c.author.Store(uint32(res.Author))
		}
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
// each one what it holds.
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
// Explicit beats implicit throughout: an argument, then RAJ_SOCKET, then
// discovery. Discovery prefers an editor whose root contains the working
// directory, since a bridge is normally started inside the project it is meant
// to drive. Several matches is an error rather than a guess — editing the wrong
// repository is not a mistake worth being convenient about.
func Locate(explicit, cwd string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if env := os.Getenv("RAJ_SOCKET"); env != "" {
		return env, nil
	}
	found := Discover()
	switch len(found) {
	case 0:
		return "", fmt.Errorf("no running raj found in %s; start one with --control, "+
			"or set RAJ_SOCKET", filepath.Dir(DefaultPath()))
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
	msg := "several raj instances are running; set RAJ_SOCKET to one of:"
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
