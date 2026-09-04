package app

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"raj/internal/control"
)

// A client speaking the real protocol over a real socket, using the same
// control.Client anything else would. Nothing here reaches into the app: if the
// wiring between the accept goroutine and the event thread is wrong, these tests
// hang or race rather than passing on a shortcut.
type client struct {
	t *testing.T
	c *control.Client
}

func (h *harness) dial(t *testing.T) *client {
	t.Helper()
	c, err := control.Dial(h.ControlPath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return &client{t: t, c: c}
}

// do sends a request and pumps the event loop until the answer arrives. The
// pumping is the point: a reply that needed no event thread would mean the
// request was executed somewhere it should not have been.
func (c *client) do(h *harness, req control.Request) control.Response {
	c.t.Helper()
	type result struct {
		res control.Response
		err error
	}
	got := make(chan result, 1)
	go func() {
		res, err := c.c.Do(req)
		got <- result{res, err}
	}()
	deadline := time.After(control.ReplyTimeout)
	for {
		select {
		case r := <-got:
			if r.err != nil {
				c.t.Fatalf("%s: %v", req.Op, r.err)
			}
			return r.res
		case <-deadline:
			c.t.Fatalf("no reply to %q", req.Op)
		default:
			h.drain() // the event thread, which is where requests execute
			time.Sleep(time.Millisecond)
		}
	}
}

func controlHarness(t *testing.T, content string) *harness {
	t.Helper()
	h := newHarness(t, content)
	sock := filepath.Join(t.TempDir(), "c.sock")
	if err := h.StartControl(sock); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.StopControl)
	return h
}

func TestControlReadsBuffers(t *testing.T) {
	h := controlHarness(t, "hello\nworld\n")
	c := h.dial(t)

	if r := c.do(h, control.Request{Op: "ping"}); !r.OK || r.Root == "" {
		t.Fatalf("ping = %+v", r)
	}
	r := c.do(h, control.Request{Op: "buffers"})
	if !r.OK || len(r.Buffers) != 1 {
		t.Fatalf("buffers = %+v", r)
	}
	if r.Buffers[0].Bytes != len("hello\nworld\n") || r.Buffers[0].Dirty {
		t.Errorf("buffer = %+v", r.Buffers[0])
	}
	if txt := c.do(h, control.Request{Op: "text"}); txt.Text() != "hello\nworld\n" {
		t.Errorf("text = %q", txt.Text())
	}
}

func TestControlAppliesAnEdit(t *testing.T) {
	h := controlHarness(t, "hello world\n")
	c := h.dial(t)

	read := c.do(h, control.Request{Op: "text"})
	base := read.Version
	r := c.do(h, control.Request{
		Op:    "apply",
		Base:  &base,
		Hunks: []control.Hunk{{Start: 6, End: 11, Text: "socket"}},
	})
	if !r.OK {
		t.Fatalf("apply = %+v", r)
	}
	if got := h.text(); got != "hello socket\n" {
		t.Errorf("buffer = %q", got)
	}
	if r.Version == base {
		t.Error("version did not advance")
	}

	if s := c.do(h, control.Request{Op: "save"}); !s.OK {
		t.Fatalf("save = %+v", s)
	}
	data, err := os.ReadFile(h.Tabs.Active().File.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello socket\n" {
		t.Errorf("on disk = %q", string(data))
	}
}

// An apply with no base is refused. It is the one request that can silently
// corrupt a file: the offsets were computed against some version, and applying
// them to whatever the buffer is now looks like success.
func TestControlRefusesAnApplyWithNoBase(t *testing.T) {
	h := controlHarness(t, "hello world\n")
	c := h.dial(t)
	r := c.do(h, control.Request{Op: "apply", Hunks: []control.Hunk{{Start: 0, End: 5, Text: "x"}}})
	if r.OK || r.Err == "" {
		t.Fatalf("apply without base was accepted: %+v", r)
	}
	if h.text() != "hello world\n" {
		t.Errorf("buffer changed anyway: %q", h.text())
	}
}

// A stale base is reported as a conflict rather than applied at the offset it
// was written against.
func TestControlReportsConflicts(t *testing.T) {
	h := controlHarness(t, "hello world\n")
	c := h.dial(t)
	base := c.do(h, control.Request{Op: "text"}).Version

	// The user types, moving everything the stale hunk was measured against.
	h.press("super+a")
	h.press("tab")

	r := c.do(h, control.Request{
		Op:    "apply",
		Base:  &base,
		Hunks: []control.Hunk{{Start: 6, End: 11, Text: "socket"}},
	})
	if len(r.Conflicts) == 0 && r.OK {
		// Rebasing may still place it, and here it does: the indent shifted
		// every offset by one and ApplyDiff carries the hunk with it. What must
		// never happen is a silent success that wrote at the raw offset, which
		// would have replaced "ello " instead.
		if got := h.text(); !strings.Contains(got, "hello socket") {
			t.Fatalf("stale hunk applied to the wrong span: %q", got)
		}
	}
	if len(r.Conflicts) > 0 && r.Err == "" {
		t.Error("conflicts reported with no error message")
	}
}

func TestControlErrors(t *testing.T) {
	h := controlHarness(t, "x\n")
	c := h.dial(t)
	for _, req := range []control.Request{
		{Op: "nonsense"},
		{Op: "text", Path: "/nope/missing.go"},
		{Op: "save", Path: "/nope/missing.go"},
		{Op: "open"},
	} {
		if r := c.do(h, req); r.OK || r.Err == "" {
			t.Errorf("%+v was accepted: %+v", req, r)
		}
	}
	base := uint64(0)
	r := c.do(h, control.Request{Op: "apply", Base: &base, Hunks: []control.Hunk{{Start: -1, End: 0}}})
	if r.OK || r.Err == "" {
		t.Errorf("negative offset accepted: %+v", r)
	}
}

// Edits arriving over the socket are the Agent's, not the user's, so cmd+z does
// not swallow them and the tint shows where they came from.
func TestControlEditsAreAttributedToTheAgent(t *testing.T) {
	h := controlHarness(t, "hello world\n")
	c := h.dial(t)
	base := c.do(h, control.Request{Op: "text"}).Version
	c.do(h, control.Request{Op: "apply", Base: &base, Hunks: []control.Hunk{{Start: 0, End: 5, Text: "HI"}}})
	if got := h.text(); got != "HI world\n" {
		t.Fatalf("buffer = %q", got)
	}
	h.press("super+z") // the user's undo
	if got := h.text(); got != "HI world\n" {
		t.Errorf("user undo reversed an agent edit: %q", got)
	}
}

// The reason the package is split the way it is: many connections editing at
// once must serialise through the event thread. Run this under -race; a design
// that touched the model from the accept goroutine fails here rather than in
// someone's unsaved work.
func TestControlConcurrentClientsSerialise(t *testing.T) {
	h := controlHarness(t, "\n")
	const clients = 8
	const each = 5

	var wg sync.WaitGroup
	for i := 0; i < clients; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := control.Dial(h.ControlPath())
			if err != nil {
				t.Error(err)
				return
			}
			defer c.Close()
			for j := 0; j < each; j++ {
				if _, err := c.Do(control.Request{Op: "buffers"}); err != nil {
					return
				}
			}
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	for {
		select {
		case <-done:
			return
		case <-time.After(10 * time.Second):
			t.Fatal("clients did not finish; the event thread never drained")
		default:
			h.drain()
			time.Sleep(time.Millisecond)
		}
	}
}

// A request that the event thread never picks up must fail rather than hang the
// client for ever.
func TestControlTimesOutWhenTheEditorNeverDrains(t *testing.T) {
	h := controlHarness(t, "x\n")
	c, err := control.Dial(h.ControlPath())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	// Never drain. Close instead: shutdown must answer what is parked, rather
	// than leaving the client waiting on an editor that has gone.
	got := make(chan control.Response, 1)
	go func() {
		res, _ := c.Do(control.Request{Op: "buffers"})
		got <- res
	}()
	time.Sleep(50 * time.Millisecond)
	h.StopControl()

	select {
	case r := <-got:
		if r.OK || r.Err == "" {
			t.Errorf("got %+v, want an error", r)
		}
	case <-time.After(control.ReplyTimeout + 2*time.Second):
		t.Fatal("no answer on shutdown")
	}
}

// Closing removes the socket file; a path left behind answers nothing and looks
// like a live editor.
func TestControlCleansUpItsSocket(t *testing.T) {
	h := controlHarness(t, "x\n")
	path := h.ControlPath()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("socket not created: %v", err)
	}
	h.StopControl()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("socket still present after close: %v", err)
	}
	if h.ControlPath() != "" {
		t.Error("ControlPath still reports a socket")
	}
}

// Off unless asked for.
func TestControlIsOffByDefault(t *testing.T) {
	h := newHarness(t, "x\n")
	if h.ControlPath() != "" {
		t.Error("a socket exists without --control")
	}
	h.drainControl() // must be a no-op rather than a nil dereference
}

// The reason search belongs on this interface rather than an agent shelling out
// to grep: an open buffer can differ from the file on disk, and a driver that
// searched the filesystem would find stale text and edit against it.
func TestControlSearchSeesUnsavedEdits(t *testing.T) {
	h := controlHarness(t, "package a\n\nfunc needle() {}\n")
	c := h.dial(t)
	path := h.Tabs.Active().File.Path

	// On disk and in the buffer: found.
	res := c.do(h, control.Request{Op: "search", Query: &control.SearchQuery{Text: "needle"}})
	if !res.OK || len(res.Matches) == 0 {
		t.Fatalf("search = %+v", res)
	}

	// Now change it in the buffer only. Disk still says "needle".
	read := c.do(h, control.Request{Op: "text"})
	base := read.Version
	off := strings.Index(read.Text(), "needle")
	ap := c.do(h, control.Request{Op: "apply", Path: path, Base: &base,
		Hunks: []control.Hunk{{Start: off, End: off + len("needle"), Text: "haystack"}}})
	if !ap.OK {
		t.Fatalf("apply = %+v", ap)
	}
	if disk, err := os.ReadFile(path); err != nil || !strings.Contains(string(disk), "needle") {
		t.Fatalf("test is not testing what it claims; disk = %q", disk)
	}

	// The old text is gone from the search even though it is still on disk...
	res = c.do(h, control.Request{Op: "search", Query: &control.SearchQuery{Text: "needle"}})
	for _, m := range res.Matches {
		if m.Path == path {
			t.Errorf("stale on-disk text still matched: %s:%d %q", m.Path, m.Line, m.Text)
		}
	}
	// ...and the unsaved text is findable.
	res = c.do(h, control.Request{Op: "search", Query: &control.SearchQuery{Text: "haystack"}})
	var found bool
	for _, m := range res.Matches {
		if m.Path == path {
			found = true
		}
	}
	if !found {
		t.Errorf("unsaved edit was not searchable: %+v", res.Matches)
	}
}

// End to end: an agent's edit arrives as a proposal, is listed as one, and can
// be backed out — leaving the document as if it had never been written.
func TestAgentEditsArriveAsProposals(t *testing.T) {
	h := controlHarness(t, "hello world\n")
	c := h.dial(t)
	path := h.Tabs.Active().File.Path

	read := c.do(h, control.Request{Op: "text"})
	base := read.Version
	if !c.do(h, control.Request{Op: "apply", Path: path, Base: &base,
		Hunks: []control.Hunk{{Start: 6, End: 11, Text: "socket"}}}).OK {
		t.Fatal("apply failed")
	}
	if h.text() != "hello socket\n" {
		t.Fatalf("buffer = %q", h.text())
	}

	res := c.do(h, control.Request{Op: "groups", Path: path})
	if !res.OK || len(res.Groups) == 0 {
		t.Fatalf("groups = %+v", res)
	}
	var proposal control.Group
	for _, g := range res.Groups {
		if g.State == "proposed" {
			proposal = g
		}
	}
	if proposal.ID == 0 {
		t.Fatalf("no proposal listed: %+v", res.Groups)
	}

	if r := c.do(h, control.Request{Op: "reject", Path: path, Group: proposal.ID}); !r.OK {
		t.Fatalf("reject = %+v", r)
	}
	if got := h.text(); got != "hello world\n" {
		t.Errorf("after rejecting = %q, want the original back", got)
	}
}

// The user's own typing is not a proposal. Marking it would make the person
// approve their own keystrokes.
func TestUserEditsAreNotProposals(t *testing.T) {
	h := controlHarness(t, "hello\n")
	c := h.dial(t)
	h.press("x")

	res := c.do(h, control.Request{Op: "groups"})
	for _, g := range res.Groups {
		if g.State == "proposed" {
			t.Errorf("the user's own edit was marked proposed: %+v", g)
		}
	}
}

// Accepting leaves the text alone — it is already in the document — and only
// clears the pending mark.
func TestAcceptClearsTheMarkWithoutChangingText(t *testing.T) {
	h := controlHarness(t, "hello world\n")
	c := h.dial(t)
	path := h.Tabs.Active().File.Path
	base := c.do(h, control.Request{Op: "text"}).Version
	c.do(h, control.Request{Op: "apply", Path: path, Base: &base,
		Hunks: []control.Hunk{{Start: 0, End: 5, Text: "howdy"}}})

	var id uint64
	for _, g := range c.do(h, control.Request{Op: "groups", Path: path}).Groups {
		if g.State == "proposed" {
			id = g.ID
		}
	}
	before := h.text()
	if r := c.do(h, control.Request{Op: "accept", Path: path, Group: id}); !r.OK {
		t.Fatalf("accept = %+v", r)
	}
	if h.text() != before {
		t.Errorf("accepting changed the text: %q", h.text())
	}
	for _, g := range c.do(h, control.Request{Op: "groups", Path: path}).Groups {
		if g.ID == id && g.State != "accepted" {
			t.Errorf("state = %q after accepting", g.State)
		}
	}
}
