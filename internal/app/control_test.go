package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"raj/internal/control"
	"raj/internal/piecetable"
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

// do sends a request, claiming the write target first when the Guard gates it.
// A socket apply, patch or clear needs the claim set of the author to hold its
// path — that rule is enforced in internal/control, so the tests here are about
// what a write does, not about the gate. The claim rides this same connection,
// so it binds to the author the write will use; a pathless write targets the
// active buffer, which is what the host resolves an empty path to. The pumping
// is the point: a reply that needed no event thread would mean the request was
// executed somewhere it should not have been.
func (c *client) do(h *harness, req control.Request) control.Response {
	if req.Op == "apply" || req.Op == "patch" || req.Op == "clear" ||
		req.Op == "rename" || req.Op == "revert" {
		path := req.Path
		if path == "" {
			path = h.Tabs.Active().File.Path
		}
		if res := c.roundtrip(h, control.Request{Op: "claim", Paths: []string{path}}); !res.OK {
			c.t.Fatalf("claim %q: %+v", path, res)
		}
	}
	return c.roundtrip(h, req)
}

// roundtrip is one request over the socket, pumping the event loop until the
// answer arrives.
func (c *client) roundtrip(h *harness, req control.Request) control.Response {
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
	sock := controlSock(t, "c.sock")
	if err := h.StartControl(sock, false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.StopControl)
	return h
}

// controlSock returns a unix socket address short enough on every platform.
// filepath.Join(t.TempDir(), "c.sock") crosses the 104-byte sockaddr_un
// sun_path limit on macOS, where TMPDIR is long and t.TempDir() appends the
// whole test name; bind then fails with "invalid argument". A single short
// component under os.TempDir() stays well under it.
func controlSock(t *testing.T, id string) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "raj-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, id)
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

// Every path-taking verb resolves the spellings alike: the absolute path the
// editor reports, a workspace-relative spelling, and the editor's own
// canonical spelling all reach the same buffer. This is the resolution seam
// the Guard and the app host share, so a verb added later cannot join relative
// paths against the process's working directory or refuse a path it has
// already canonicalised.
func TestControlPathSpellingsResolveAlike(t *testing.T) {
	h := controlHarness(t, "alpha\nbeta\n")
	c := h.dial(t)
	abs := h.Tabs.Active().File.Path
	rel, err := filepath.Rel(h.root, abs)
	if err != nil {
		t.Fatal(err)
	}
	spellings := []struct{ name, path string }{
		{"absolute", abs},
		{"relative", rel},
		{"canonical", h.Tabs.Active().File.Path},
	}
	for _, op := range []string{"text", "version", "groups", "diff", "dump"} {
		for _, s := range spellings {
			res := c.roundtrip(h, control.Request{Op: op, Path: s.path})
			if !res.OK {
				t.Errorf("%s %s (%q) = %+v", op, s.name, s.path, res)
			}
		}
	}
	// review lists without entering the mode, and a caret move resolves through
	// the same seam.
	for _, s := range spellings {
		if res := c.roundtrip(h, control.Request{Op: "review", Path: s.path, ReviewList: true}); !res.OK {
			t.Errorf("review %s (%q) = %+v", s.name, s.path, res)
		}
		if res := c.roundtrip(h, control.Request{Op: "goto", Path: s.path, Line: 2}); !res.OK {
			t.Errorf("goto %s (%q) = %+v", s.name, s.path, res)
		}
	}
	// A relative claim is stored canonically, so a later write spelled either
	// way passes the same membership check.
	claim := c.roundtrip(h, control.Request{Op: "claim", Paths: []string{rel}})
	if !claim.OK || len(claim.Claims) != 1 || claim.Claims[0] != abs {
		t.Fatalf("claim %q = %+v, want the canonical %q", rel, claim, abs)
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

	// An agent's own save is refused while its change set is proposed: two
	// calls in a row is not the moment to look that Save exists to provide.
	s := c.do(h, control.Request{Op: "save"})
	if s.OK {
		t.Fatal("save succeeded with the change still proposed")
	}
	if !strings.Contains(s.Err, "approval") {
		t.Errorf("refusal = %q, want it to say what is pending", s.Err)
	}
	if data, err := os.ReadFile(h.Tabs.Active().File.Path); err == nil && strings.Contains(string(data), "socket") {
		t.Error("the proposed text reached disk anyway")
	}

	// Accepted, the same save goes through.
	groups := c.do(h, control.Request{Op: "groups"})
	if !groups.OK || len(groups.Groups) == 0 {
		t.Fatalf("groups = %+v", groups)
	}
	var id uint64
	for _, g := range groups.Groups {
		if g.State == "proposed" {
			id = g.ID
		}
	}
	if id == 0 {
		t.Fatalf("no proposed group in %+v", groups.Groups)
	}
	if a := c.do(h, control.Request{Op: "accept", Group: id}); !a.OK {
		t.Fatalf("accept = %+v", a)
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

// revert is the inverse of attribution: it discards the connection's own pieces
// — here a change set still proposed — reversing them out of the buffer and
// leaving the text as it was before the apply.
func TestControlRevertsItsOwnPieces(t *testing.T) {
	h := controlHarness(t, "hello world\n")
	c := h.dial(t)

	base := c.do(h, control.Request{Op: "text"}).Version
	if r := c.do(h, control.Request{Op: "apply", Base: &base,
		Hunks: []control.Hunk{{Start: 6, End: 11, Text: "socket"}}}); !r.OK {
		t.Fatalf("apply = %+v", r)
	}
	if got := h.text(); got != "hello socket\n" {
		t.Fatalf("buffer after apply = %q", got)
	}

	if r := c.do(h, control.Request{Op: "revert"}); !r.OK {
		t.Fatalf("revert = %+v", r)
	}
	if got := h.text(); got != "hello world\n" {
		t.Errorf("buffer after revert = %q, want the applied pieces gone", got)
	}
}

// A writer with no live pieces has nothing to drop, so revert refuses rather
// than reporting a success that changed nothing.
func TestControlRevertWithNothingToDropIsRefused(t *testing.T) {
	h := controlHarness(t, "hello\n")
	c := h.dial(t)

	r := c.do(h, control.Request{Op: "revert"})
	if r.OK {
		t.Fatalf("revert with no pieces = %+v, want a refusal", r)
	}
	if got := h.text(); got != "hello\n" {
		t.Errorf("buffer = %q, want it unchanged", got)
	}
}

// A span past the end of the buffer is refused, not clamped to an empty read:
// for one driver that is a typo, for several writing concurrently it is silent
// corruption. The read verbs and the write surface share the one check.
func TestSpanBoundsAreChecked(t *testing.T) {
	h := controlHarness(t, "hello\n")
	c := h.dial(t)

	const want = "offset out of range"
	read := c.do(h, control.Request{Op: "text", Start: intPtr(9999), End: intPtr(9999)})
	if read.OK || !strings.Contains(read.Err, want) {
		t.Errorf("text past the end = OK %v err %q, want %q", read.OK, read.Err, want)
	}
	dump := c.do(h, control.Request{Op: "dump", Start: intPtr(9999), End: intPtr(9999)})
	if dump.OK || !strings.Contains(dump.Err, want) {
		t.Errorf("dump past the end = OK %v err %q, want %q", dump.OK, dump.Err, want)
	}
	base := c.do(h, control.Request{Op: "text"}).Version
	ap := c.do(h, control.Request{Op: "apply", Base: &base,
		Hunks: []control.Hunk{{Start: 9999, End: 9999, Text: "x"}}})
	if ap.OK || !strings.Contains(ap.Err, want) {
		t.Errorf("apply past the end = OK %v err %q, want %q", ap.OK, ap.Err, want)
	}
	if got := h.text(); got != "hello\n" {
		t.Errorf("buffer = %q, want the refused writes to have changed nothing", got)
	}
}

func intPtr(v int) *int { return &v }

// The user's own save is the approval gesture: it writes the file and clears
// every pending mark, so a later agent save is not refused for work that is
// already committed.
func TestUserSaveAcceptsPendingChanges(t *testing.T) {
	h := controlHarness(t, "hello world\n")
	c := h.dial(t)

	read := c.do(h, control.Request{Op: "text"})
	base := read.Version
	if r := c.do(h, control.Request{
		Op:    "apply",
		Base:  &base,
		Hunks: []control.Hunk{{Start: 6, End: 11, Text: "socket"}},
	}); !r.OK {
		t.Fatalf("apply = %+v", r)
	}

	p := h.Tabs.Active()
	if got := p.File.Session().Pending(); len(got) != 1 {
		t.Fatalf("pending = %+v, want the agent's change set", got)
	}

	h.press("super+s")
	// The save reviews the pending set rather than accepting it blind, so the
	// gesture now lands in the review dialog; enter answers Save.
	if !h.Prompt.Open {
		t.Fatal("super+s with a pending set did not open the save review")
	}
	if got := p.File.Session().Pending(); len(got) != 1 {
		t.Fatalf("pending = %+v while the review is open, want the set still there", got)
	}
	h.press("enter")

	if got := p.File.Session().Pending(); len(got) != 0 {
		t.Errorf("pending = %+v after the user saved, want none", got)
	}
	data, err := os.ReadFile(p.File.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello socket\n" {
		t.Errorf("on disk = %q", string(data))
	}
	if s := c.do(h, control.Request{Op: "save"}); !s.OK {
		t.Errorf("save refused after the user accepted: %+v", s)
	}
}

// A UI save tells every connected driver, so a harness learns the file reached
// disk without polling. The message rides the same mailbox `recv` parks on;
// a driver that is gone or has no room must not fail the save.
func TestUserSaveNotifiesConnectedDrivers(t *testing.T) {
	h := controlHarness(t, "hello\n")

	// Two connections, one identity: the parked recv needs its own client
	// because Client.Do holds a lock for the length of a call.
	c := h.dial(t)
	if hi := c.do(h, control.Request{Op: "hello", Identity: "driver-1", Name: "claude"}); !hi.OK {
		t.Fatalf("hello = %+v", hi)
	}
	parked := h.dial(t)
	parked.do(h, control.Request{Op: "hello", Identity: "driver-1"})

	got := make(chan control.Response, 1)
	go func() {
		res, err := parked.c.Do(control.Request{Op: "recv"})
		if err != nil {
			got <- control.Response{Err: err.Error()}
			return
		}
		got <- res
	}()
	select {
	case res := <-got:
		t.Fatalf("recv answered %+v before the save", res)
	case <-time.After(50 * time.Millisecond):
	}

	path := h.Tabs.Active().File.Path
	h.press("super+s")

	select {
	case res := <-got:
		if !res.OK {
			t.Fatalf("recv = %+v", res)
		}
		if len(res.Messages) != 1 || res.Messages[0].Text != "saved "+path {
			t.Fatalf("messages = %+v, want a saved notice for %s", res.Messages, path)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a save did not reach the connected driver")
	}
}

// A save writes the agreed composition, never text a reviewer rejected: the
// buffer keeps the rejected bytes, read returns them by default as the view,
// and the file on disk holds only the agreed bytes.
func TestControlSaveWritesTheAgreedComposition(t *testing.T) {
	h := controlHarness(t, "hello world\n")
	c := h.dial(t)
	path := h.Tabs.Active().File.Path

	base := c.do(h, control.Request{Op: "text"}).Version
	if r := c.do(h, control.Request{
		Op:    "apply",
		Base:  &base,
		Hunks: []control.Hunk{{Start: 6, End: 11, Text: "socket"}},
	}); !r.OK {
		t.Fatalf("apply = %+v", r)
	}
	var id uint64
	for _, g := range c.do(h, control.Request{Op: "groups", Path: path}).Groups {
		if g.State == "proposed" {
			id = g.ID
		}
	}
	if id == 0 {
		t.Fatal("no proposal to reject")
	}
	if r := c.do(h, control.Request{Op: "reject", Path: path, Group: id}); !r.OK {
		t.Fatalf("reject = %+v", r)
	}
	// The rejected set no longer blocks a save, and it is not written.
	if s := c.do(h, control.Request{Op: "save", Path: path}); !s.OK {
		t.Fatalf("save = %+v", s)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello world\n" {
		t.Errorf("on disk = %q, want the agreed composition", string(data))
	}
	if got := h.text(); got != "hello socket\n" {
		t.Errorf("buffer = %q, want the rejected text still in the buffer", got)
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

// A no-op hunk — empty text at a zero-width span — is not a change. A batch of
// only no-ops advances nothing and opens no change set; a batch mixing real and
// no-op hunks applies only the real one and opens exactly one.
func TestControlNoOpHunksCreateNoChangeSet(t *testing.T) {
	h := controlHarness(t, "hello world\n")
	c := h.dial(t)
	base := c.do(h, control.Request{Op: "text"}).Version

	r := c.do(h, control.Request{Op: "apply", Base: &base,
		Hunks: []control.Hunk{{Start: 0, End: 0, Text: ""}}})
	if !r.OK {
		t.Fatalf("no-op apply = %+v", r)
	}
	if r.Version != base {
		t.Errorf("no-op apply advanced version %d -> %d", base, r.Version)
	}
	if got := h.text(); got != "hello world\n" {
		t.Errorf("buffer = %q, want unchanged", got)
	}
	if groups := c.do(h, control.Request{Op: "groups"}); len(groups.Groups) != 0 {
		t.Errorf("no-op apply opened change sets: %+v", groups.Groups)
	}

	// A mixed batch applies the real hunk and opens exactly one change set.
	base = c.do(h, control.Request{Op: "text"}).Version
	r = c.do(h, control.Request{Op: "apply", Base: &base, Hunks: []control.Hunk{
		{Start: 0, End: 0, Text: ""},
		{Start: 6, End: 11, Text: "socket"},
	}})
	if !r.OK {
		t.Fatalf("mixed apply = %+v", r)
	}
	if got := h.text(); got != "hello socket\n" {
		t.Errorf("buffer = %q, want only the real hunk applied", got)
	}
	groups := c.do(h, control.Request{Op: "groups"})
	if len(groups.Groups) != 1 {
		t.Fatalf("mixed apply opened %d change sets, want 1: %+v", len(groups.Groups), groups.Groups)
	}
	if groups.Groups[0].State != "proposed" {
		t.Errorf("change set state = %q, want proposed", groups.Groups[0].State)
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

// -path limits the walk to one subtree under the workspace root, and a path
// that reaches outside it is refused rather than walked.
func TestControlSearchPathScopesAndRefusesEscape(t *testing.T) {
	h := controlHarness(t, "root needle\n")
	c := h.dial(t)
	root := filepath.Dir(h.Tabs.Active().File.Path)

	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "inside.go"), []byte("inside needle\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "outside.go"), []byte("outside needle\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Unscoped, every file that holds the term reports it.
	all := c.do(h, control.Request{Op: "search", Query: &control.SearchQuery{Text: "needle"}})
	if !all.OK || len(all.Matches) < 3 {
		t.Fatalf("unscoped search = %+v, want the root file and the subtree", all)
	}

	// Scoped, only the subtree.
	scoped := c.do(h, control.Request{Op: "search", Query: &control.SearchQuery{Text: "needle", Path: "sub"}})
	if !scoped.OK || len(scoped.Matches) == 0 {
		t.Fatalf("scoped search = %+v", scoped)
	}
	for _, m := range scoped.Matches {
		if filepath.Dir(m.Path) != filepath.Join(root, "sub") {
			t.Errorf("scoped search returned %s, outside sub/", m.Path)
		}
	}

	// An escape is refused, not walked.
	out := c.do(h, control.Request{Op: "search", Query: &control.SearchQuery{Text: "needle", Path: "../elsewhere"}})
	if out.OK {
		t.Fatalf("a path outside the root was searched: %+v", out)
	}
	if !strings.Contains(out.Err, "not under") {
		t.Errorf("refusal = %q, want it to name the root boundary", out.Err)
	}
}

// mkdir makes a directory, with any missing parents, under the root, so an
// agent can create a package before it has a file to put in it. It is not
// claim-gated: a directory is not a file and does not hold a buffer yet. An
// existing directory is a no-op, and an outside-the-root path is refused
// without touching the filesystem.
func TestControlMkdirCreatesDirectories(t *testing.T) {
	h := controlHarness(t, "root\n")
	c := h.dial(t)

	res := c.do(h, control.Request{Op: "mkdir", Path: filepath.Join("newpkg", "inner")})
	if !res.OK {
		t.Fatalf("mkdir = %+v", res)
	}
	dir := filepath.Join(h.root, "newpkg", "inner")
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		t.Fatalf("mkdir did not create %s: stat = %v, %v", dir, st, err)
	}

	// An existing directory is not an error: a caller cannot cheaply know
	// whether the directory it is about to write into is already there.
	if res := c.do(h, control.Request{Op: "mkdir", Path: filepath.Join("newpkg", "inner")}); !res.OK {
		t.Errorf("mkdir of an existing directory = %+v, want success", res)
	}

	// Out of root is a refusal, and nothing is created.
	outside := filepath.Join(h.root, "..", "escaped")
	res = c.do(h, control.Request{Op: "mkdir", Path: outside})
	if res.OK || !strings.Contains(res.Err, "not under") {
		t.Fatalf("mkdir outside the root = %+v, want a refusal naming the root", res)
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Errorf("an out-of-root mkdir created %s (err=%v)", outside, err)
	}
}

// delete records a workspace-level pending deletion over the real socket: the
// proposal survives across requests and lists with its author, and withdraw
// removes it. The file on disk is untouched throughout — W4b-1 records; the
// prompt and the unlink are W4b-2.
func TestControlDeleteProposesWithoutUnlinking(t *testing.T) {
	h := controlHarness(t, "hello\n")
	c := h.dial(t)
	path := h.Tabs.Active().File.Path

	// Claim first: delete is claim-gated, like a text write.
	if res := c.roundtrip(h, control.Request{Op: "claim", Paths: []string{path}}); !res.OK {
		t.Fatalf("claim = %+v", res)
	}
	if res := c.roundtrip(h, control.Request{Op: "delete", Path: path}); !res.OK {
		t.Fatalf("delete = %+v", res)
	}
	// Idempotent: the second proposal is a no-op, so the list still holds one.
	if res := c.roundtrip(h, control.Request{Op: "delete", Path: path}); !res.OK {
		t.Fatalf("second delete = %+v", res)
	}
	list := c.roundtrip(h, control.Request{Op: "deletions"})
	if !list.OK || len(list.Deletions) != 1 {
		t.Fatalf("deletions = %+v, want one", list)
	}
	if d := list.Deletions[0]; d.Path != path || d.Author != c.c.Author() {
		t.Errorf("pending = %+v, want %s proposed by %d", d, path, c.c.Author())
	}
	// The file is still on disk: delete only records a proposal.
	if _, err := os.Stat(path); err != nil {
		t.Errorf("delete removed the file: %v", err)
	}
	if got := h.Pane().File.Text(); got != "hello\n" {
		t.Errorf("buffer = %q, want it untouched", got)
	}

	// The proposer withdraws; the pending set empties.
	if res := c.roundtrip(h, control.Request{Op: "delete", Path: path, Withdraw: true}); !res.OK {
		t.Fatalf("withdraw = %+v", res)
	}
	list = c.roundtrip(h, control.Request{Op: "deletions"})
	if len(list.Deletions) != 0 {
		t.Errorf("after withdraw, deletions = %+v, want none", list.Deletions)
	}
}

// delete is claim-gated even over the socket: an unclaimed path is refused and
// nothing is recorded. The refusal is the strict one the write verbs give when
// the set is empty.
func TestControlDeleteNeedsAClaim(t *testing.T) {
	h := controlHarness(t, "hello\n")
	c := h.dial(t)
	path := h.Tabs.Active().File.Path

	res := c.roundtrip(h, control.Request{Op: "delete", Path: path})
	if res.OK || !strings.Contains(res.Err, "claim a file first") {
		t.Errorf("delete with no claim = %+v, want the strict refusal", res)
	}
	if list := c.roundtrip(h, control.Request{Op: "deletions"}); len(list.Deletions) != 0 {
		t.Errorf("a refused delete recorded something: %+v", list.Deletions)
	}
}

// rmdir records a workspace-level pending dir-removal over the real socket: the
// proposal survives across requests and lists with its author, and withdraw
// removes it. Nothing on disk is touched throughout — W4c-2 records; the
// review tab and the actual removal are a later wave.
func TestControlRmdirProposesWithoutRemoving(t *testing.T) {
	h := controlHarness(t, "hello\n")
	c := h.dial(t)

	dir := filepath.Join(h.root, "pkg")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package pkg\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Claim the directory first: rmdir is claim-gated, and claiming a dir
	// records the dir itself as the entry the gate checks.
	if res := c.roundtrip(h, control.Request{Op: "claim", Paths: []string{dir}}); !res.OK {
		t.Fatalf("claim = %+v", res)
	}
	if res := c.roundtrip(h, control.Request{Op: "rmdir", Path: dir}); !res.OK {
		t.Fatalf("rmdir = %+v", res)
	}
	// Idempotent: the second proposal is a no-op.
	if res := c.roundtrip(h, control.Request{Op: "rmdir", Path: dir}); !res.OK {
		t.Fatalf("second rmdir = %+v", res)
	}
	list := c.roundtrip(h, control.Request{Op: "rmdirs"})
	if !list.OK || len(list.DirRemovals) != 1 {
		t.Fatalf("rmdirs = %+v, want one", list)
	}
	if d := list.DirRemovals[0]; d.Path != dir || d.Author != c.c.Author() {
		t.Errorf("pending = %+v, want %s proposed by %d", d, dir, c.c.Author())
	}
	// The directory and its file are still on disk: rmdir only records.
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("rmdir removed the directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "a.go")); err != nil {
		t.Errorf("rmdir removed a file under the directory: %v", err)
	}

	// The proposer withdraws; the pending set empties.
	if res := c.roundtrip(h, control.Request{Op: "rmdir", Path: dir, Withdraw: true}); !res.OK {
		t.Fatalf("withdraw = %+v", res)
	}
	list = c.roundtrip(h, control.Request{Op: "rmdirs"})
	if len(list.DirRemovals) != 0 {
		t.Errorf("after withdraw, rmdirs = %+v, want none", list.DirRemovals)
	}
}

// End to end: an agent's edit arrives as a proposal, is listed as one, and can
// be rejected — a decision that keeps the text in the document and drops the
// set from the agreed composition, unlike the clear gesture that purges it.
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
	// The listing answers how big the set is without a second diff call.
	if proposal.Hunks != 1 || proposal.Moved != 0 {
		t.Errorf("proposal hunks/moved = %d/%d, want 1/0", proposal.Hunks, proposal.Moved)
	}

	if r := c.do(h, control.Request{Op: "reject", Path: path, Group: proposal.ID}); !r.OK {
		t.Fatalf("reject = %+v", r)
	}
	// A reject is a decision, not an edit: the text stays in the buffer and
	// only its standing changes. read returns the view — the buffer's own
	// text — by default, so the rejected bytes are present either way; only
	// -annotated adds the per-run state.
	if got := h.text(); got != "hello socket\n" {
		t.Errorf("buffer after rejecting = %q, want the text to stay", got)
	}
	if got := c.do(h, control.Request{Op: "text"}).Text(); got != "hello socket\n" {
		t.Errorf("default read after rejecting = %q, want the buffer view", got)
	}
	ann := c.do(h, control.Request{Op: "text", Annotated: true})
	if got := ann.Text(); got != "hello socket\n" {
		t.Errorf("annotated read after rejecting = %q, want the buffer", got)
	}
	var runs []control.StateRun
	if err := json.Unmarshal([]byte(ann.StatesJSON), &runs); err != nil {
		t.Fatalf("annotated states are not JSON: %v", err)
	}
	var rejected bool
	for _, r := range runs {
		if r.State == "rejected" {
			rejected = true
		}
	}
	if !rejected {
		t.Errorf("annotated states = %+v, want a rejected run", runs)
	}
}

// groupByState returns the single change set in state st, failing the test
// when there is not exactly one. A test about one set's counts should not
// guess which of several it got.
func groupByState(t *testing.T, h *harness, c *client, path, state string) control.Group {
	t.Helper()
	var found []control.Group
	for _, g := range c.do(h, control.Request{Op: "groups", Path: path}).Groups {
		if g.State == state {
			found = append(found, g)
		}
	}
	if len(found) != 1 {
		t.Fatalf("groups in state %q = %+v, want exactly one", state, found)
	}
	return found[0]
}

// A decided set still reports its counts. An accepted set's members are live in
// the journal, so a later edit that overwrites one counts as moved exactly as
// it would while the set was pending; a rejected set is projected the same way
// because the decision changes its standing, not whether its bytes are there.
// This is the decided counterpart to the proposed-set case above.
func TestControlGroupsCountsDecidedSets(t *testing.T) {
	h := controlHarness(t, "hello world\n")
	c := h.dial(t)
	path := h.Tabs.Active().File.Path

	read := c.do(h, control.Request{Op: "text"})
	base := read.Version
	if r := c.do(h, control.Request{Op: "apply", Path: path, Base: &base,
		Hunks: []control.Hunk{{Start: 6, End: 11, Text: "socket"}}}); !r.OK {
		t.Fatalf("apply = %+v", r)
	}
	first := groupByState(t, h, c, path, "proposed")
	if first.Hunks != 1 || first.Moved != 0 {
		t.Fatalf("proposed hunks/moved = %d/%d, want 1/0", first.Hunks, first.Moved)
	}

	if r := c.do(h, control.Request{Op: "accept", Path: path, Group: first.ID}); !r.OK {
		t.Fatalf("accept = %+v", r)
	}
	if g := groupByState(t, h, c, path, "accepted"); g.ID != first.ID || g.Hunks != 1 || g.Moved != 0 {
		t.Errorf("accepted group = %+v, want id %d with 1 hunk and 0 moved", g, first.ID)
	}

	// A later edit replaces every byte of the accepted member's text, so it is
	// moved rather than placed.
	read = c.do(h, control.Request{Op: "text"})
	base = read.Version
	if r := c.do(h, control.Request{Op: "apply", Path: path, Base: &base,
		Hunks: []control.Hunk{{Start: 6, End: 12, Text: "there"}}}); !r.OK {
		t.Fatalf("second apply = %+v", r)
	}
	if g := groupByState(t, h, c, path, "accepted"); g.ID != first.ID || g.Hunks != 0 || g.Moved != 1 {
		t.Errorf("accepted group after overwrite = %+v, want 0 hunks and 1 moved", g)
	}

	// Rejecting the second set does not zero its counts either.
	second := groupByState(t, h, c, path, "proposed")
	if r := c.do(h, control.Request{Op: "reject", Path: path, Group: second.ID}); !r.OK {
		t.Fatalf("reject = %+v", r)
	}
	if g := groupByState(t, h, c, path, "rejected"); g.ID != second.ID || g.Hunks != 1 || g.Moved != 0 {
		t.Errorf("rejected group = %+v, want id %d with 1 hunk and 0 moved", g, second.ID)
	}
}

// The diff verb is the review surface: the proposal that `groups` only lists
// comes back as old→new text with the span it covers now, and a clean buffer
// answers with an empty list rather than an error.
func TestControlDiffRendersPendingChanges(t *testing.T) {
	h := controlHarness(t, "hello world\n")
	c := h.dial(t)
	path := h.Tabs.Active().File.Path

	// Nothing proposed yet: a clean, explicit empty answer.
	res := c.do(h, control.Request{Op: "diff", Path: path})
	if !res.OK {
		t.Fatalf("diff = %+v", res)
	}
	var diffs []control.DiffGroup
	if err := json.Unmarshal([]byte(res.DiffJSON), &diffs); err != nil {
		t.Fatalf("diff payload is not JSON: %v", err)
	}
	if len(diffs) != 0 {
		t.Fatalf("diffs = %+v, want none before any proposal", diffs)
	}

	read := c.do(h, control.Request{Op: "text"})
	base := read.Version
	if r := c.do(h, control.Request{Op: "apply", Path: path, Base: &base,
		Hunks: []control.Hunk{{Start: 6, End: 11, Text: "socket"}}}); !r.OK {
		t.Fatal("apply failed")
	}

	res = c.do(h, control.Request{Op: "diff", Path: path})
	if !res.OK {
		t.Fatalf("diff = %+v", res)
	}
	if err := json.Unmarshal([]byte(res.DiffJSON), &diffs); err != nil {
		t.Fatalf("diff payload is not JSON: %v", err)
	}
	if len(diffs) != 1 {
		t.Fatalf("diffs = %+v, want the one proposal", diffs)
	}
	d := diffs[0]
	if d.State != "proposed" || len(d.Hunks) != 1 {
		t.Fatalf("diff group = %+v, want the proposed set with one hunk", d)
	}
	if d.Hunks[0].Old != "world" || d.Hunks[0].New != "socket" {
		t.Errorf("hunk old/new = %q/%q, want world/socket", d.Hunks[0].Old, d.Hunks[0].New)
	}
	if d.Hunks[0].Start != 6 || d.Hunks[0].End != 12 {
		t.Errorf("hunk span = %d..%d, want 6..12", d.Hunks[0].Start, d.Hunks[0].End)
	}
	if d.Hunks[0].Line != 1 || d.Hunks[0].EndLine != 1 {
		t.Errorf("hunk lines = L%d..L%d, want L1..L1", d.Hunks[0].Line, d.Hunks[0].EndLine)
	}

	// Rejected, the set is no longer pending and the diff is clean again.
	if r := c.do(h, control.Request{Op: "reject", Path: path, Group: d.ID}); !r.OK {
		t.Fatalf("reject = %+v", r)
	}
	res = c.do(h, control.Request{Op: "diff", Path: path})
	if err := json.Unmarshal([]byte(res.DiffJSON), &diffs); err != nil {
		t.Fatalf("diff payload is not JSON: %v", err)
	}
	if len(diffs) != 0 {
		t.Errorf("diffs after reject = %+v, want none", diffs)
	}
}

// A member a later edit overwrote whole has no current span, but the review
// surface still shows what it wrote: dropping it to a bare count left a
// reviewer with nothing to look at.
func TestControlDiffKeepsMovedMemberAsWritten(t *testing.T) {
	h := controlHarness(t, "hello world\n")
	c := h.dial(t)
	path := h.Tabs.Active().File.Path

	read := c.do(h, control.Request{Op: "text"})
	base := read.Version
	if r := c.do(h, control.Request{Op: "apply", Path: path, Base: &base,
		Hunks: []control.Hunk{{Start: 6, End: 11, Text: "socket"}}}); !r.OK {
		t.Fatalf("first apply = %+v", r)
	}
	// Replace the whole inserted run: nothing of the first member survives, so
	// it is moved, not shown at a span that no longer means anything.
	read = c.do(h, control.Request{Op: "text"})
	base = read.Version
	if r := c.do(h, control.Request{Op: "apply", Path: path, Base: &base,
		Hunks: []control.Hunk{{Start: 6, End: 12, Text: "there"}}}); !r.OK {
		t.Fatalf("second apply = %+v", r)
	}

	res := c.do(h, control.Request{Op: "diff", Path: path})
	var diffs []control.DiffGroup
	if err := json.Unmarshal([]byte(res.DiffJSON), &diffs); err != nil {
		t.Fatalf("diff payload is not JSON: %v", err)
	}
	var found bool
	for _, d := range diffs {
		for _, mh := range d.MovedHunks {
			if mh.Old == "world" && mh.New == "socket" && mh.Start == -1 {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("diffs = %+v, want the first member kept as written", diffs)
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

// A relative path resolves against the workspace root for every read verb, the
// same way open does, instead of against the process working directory. The
// harness root is a temp directory, so a bare file name can only mean the open
// buffer if the host joins it to the root.
func TestControlRelativePathsResolveAgainstTheRoot(t *testing.T) {
	h := controlHarness(t, "hello\nworld\n")
	c := h.dial(t)

	abs := h.Tabs.Active().File.Path
	rel := filepath.Base(abs)

	want := c.do(h, control.Request{Op: "text", Path: abs})
	got := c.do(h, control.Request{Op: "text", Path: rel})
	if !got.OK || got.Text() != "hello\nworld\n" {
		t.Fatalf("read %q = %+v", rel, got)
	}
	if got.Version != want.Version {
		t.Errorf("relative read is version %d, absolute is %d", got.Version, want.Version)
	}
	if v := c.do(h, control.Request{Op: "version", Path: rel}); !v.OK || v.Version != want.Version {
		t.Errorf("version %q = %+v", rel, v)
	}
	if g := c.do(h, control.Request{Op: "groups", Path: rel}); !g.OK {
		t.Errorf("groups %q = %+v", rel, g)
	}
	if g := c.do(h, control.Request{Op: "goto", Path: rel, Line: 1, Col: 1}); !g.OK {
		t.Errorf("goto %q = %+v", rel, g)
	}
}

// A relative path that names no open buffer is refused with the same answer an
// unopened absolute path gets: the seam resolves the spelling, it does not
// invent a buffer.
func TestControlRelativePathWithNoBufferRefuses(t *testing.T) {
	h := controlHarness(t, "hello\n")
	c := h.dial(t)
	r := c.do(h, control.Request{Op: "text", Path: "not-open.go"})
	if r.OK || !strings.Contains(r.Err, "no open buffer") {
		t.Errorf("read of an unopened relative path = OK %v err %q", r.OK, r.Err)
	}
}

// The absolute spelling is unchanged by the seam.
func TestControlAbsolutePathStillNamesTheBuffer(t *testing.T) {
	h := controlHarness(t, "hello\n")
	c := h.dial(t)
	abs := h.Tabs.Active().File.Path
	if r := c.do(h, control.Request{Op: "text", Path: abs}); !r.OK || r.Text() != "hello\n" {
		t.Fatalf("read %q = %+v", abs, r)
	}
}

// open accepts the relative spelling too, so a driver never joins the path
// itself; reopening by the relative name focuses the tab the harness already
// opened rather than stacking a duplicate.
func TestControlOpenAcceptsARelativePath(t *testing.T) {
	h := controlHarness(t, "hello\n")
	c := h.dial(t)
	before := h.Tabs.Count()
	rel := filepath.Base(h.Tabs.Active().File.Path)
	if r := c.do(h, control.Request{Op: "open", Path: rel}); !r.OK {
		t.Fatalf("open %q = %+v", rel, r)
	}
	if got := h.Tabs.Count(); got != before {
		t.Errorf("tab count = %d, want %d — the relative path opened a duplicate", got, before)
	}
}

// open without -create refuses a path that is neither a buffer nor a file, so a
// typo cannot quietly become a phantom tab. -create is the caller saying it
// means to make a new buffer, and only then does the tab appear.
func TestControlOpenRefusesAMissingPathWithoutCreate(t *testing.T) {
	h := controlHarness(t, "root\n")
	c := h.dial(t)
	dir := filepath.Dir(h.Tabs.Active().File.Path)
	missing := filepath.Join(dir, "typo.go")
	before := h.Tabs.Count()

	r := c.do(h, control.Request{Op: "open", Path: missing})
	if r.OK {
		t.Fatalf("open of a missing path without -create = %+v, want a refusal", r)
	}
	if !strings.Contains(r.Err, "pass -create") {
		t.Errorf("refusal = %q, want it to point at -create", r.Err)
	}
	if got := h.Tabs.Count(); got != before {
		t.Errorf("tab count = %d after the refusal, want %d — a phantom buffer was made", got, before)
	}

	if r := c.do(h, control.Request{Op: "open", Path: missing, Create: true}); !r.OK {
		t.Fatalf("open -create = %+v", r)
	}
	if got := h.Tabs.Active().File.Path; got != missing {
		t.Errorf("active path = %q after open -create, want %q", got, missing)
	}
}

// open's wire answer says which of the two things it did: made a new buffer or
// focused one already loaded. A driver writing a file for the first time needs
// to tell a create from a focus, or a name it only meant to make looks like a
// file that was there all along.
func TestControlOpenReportsCreated(t *testing.T) {
	h := controlHarness(t, "root\n")
	c := h.dial(t)
	path := h.Tabs.Active().File.Path

	// An already-open buffer is focused, not created.
	if r := c.do(h, control.Request{Op: "open", Path: path}); !r.OK {
		t.Fatalf("open = %+v", r)
	} else if r.Created {
		t.Error("open of an existing buffer reported a create")
	}

	// A new buffer with -create is reported as made.
	dir := filepath.Dir(path)
	fresh := filepath.Join(dir, "fresh.go")
	r := c.do(h, control.Request{Op: "open", Path: fresh, Create: true})
	if !r.OK {
		t.Fatalf("open -create = %+v", r)
	}
	if !r.Created {
		t.Error("open -create did not report the buffer as created")
	}

	// A second open of it focuses: not created again.
	if r := c.do(h, control.Request{Op: "open", Path: fresh}); !r.OK {
		t.Fatalf("second open = %+v", r)
	} else if r.Created {
		t.Error("a second open reported a create")
	}
}

// A path spelled through ".." names the same file: the seam cleans it before
// it compares, so climbing out of a directory and back in is not a different
// buffer.
func TestControlPathThroughDotDotNamesTheSameBuffer(t *testing.T) {
	h := controlHarness(t, "hello\n")
	c := h.dial(t)
	abs := h.Tabs.Active().File.Path
	climb := filepath.Join("..", filepath.Base(filepath.Dir(abs)), filepath.Base(abs))
	if r := c.do(h, control.Request{Op: "text", Path: climb}); !r.OK || r.Text() != "hello\n" {
		t.Fatalf("read %q = %+v", climb, r)
	}
}

// A rejected change set has to leave the line index where the text is. Reject
// used to reach the session directly, so the reversal never reached the index;
// the next apply anchored its catch-up at the current version and advanced
// `applied` past the unmirrored reversal, and the lines from every rejected
// insertion stayed indexed for good. The count then ran away from the text and
// read -lines near EOF walked past the buffer.
func TestControlLineIndexSurvivesApplyRejectCycles(t *testing.T) {
	h := controlHarness(t, "one\ntwo\nthree\nfour\n")
	c := h.dial(t)
	path := h.Tabs.Active().File.Path
	f := h.Tabs.Active().File

	// check asserts the index agrees with a rescan of the text, on the version
	// answer a driver reads and on the editor's own count, and that the end of
	// the index still addresses the end of the document.
	check := func(step string) {
		t.Helper()
		text := f.Text()
		want := strings.Count(text, "\n") + 1
		v := c.do(h, control.Request{Op: "version", Path: path})
		if !v.OK {
			t.Fatalf("%s: version = %+v", step, v)
		}
		if v.Lines != want || f.Lines() != want {
			t.Fatalf("%s: index has %d lines (version says %d), text has %d",
				step, f.Lines(), v.Lines, want)
		}
		last := f.Lines() - 1
		if start := f.LineStart(last); start > len(text) {
			t.Fatalf("%s: last line starts at %d, past %d bytes", step, start, len(text))
		}
		if r := c.do(h, control.Request{Op: "text", Path: path, LineStart: intPtr(last + 1)}); !r.OK {
			t.Fatalf("%s: read of the last line = %+v", step, r)
		}
	}

	// reject backs out the one proposed set the buffer is holding.
	reject := func() {
		t.Helper()
		res := c.do(h, control.Request{Op: "groups", Path: path})
		var id uint64
		for _, g := range res.Groups {
			if g.State == "proposed" {
				id = g.ID
			}
		}
		if id == 0 {
			t.Fatal("no proposed change set to reject")
		}
		if r := c.do(h, control.Request{Op: "reject", Path: path, Group: id}); !r.OK {
			t.Fatalf("reject = %+v", r)
		}
	}

	for i := 0; i < 25; i++ {
		base := c.do(h, control.Request{Op: "text"}).Version
		if r := c.do(h, control.Request{Op: "apply", Path: path, Base: &base,
			Hunks: []control.Hunk{{Start: 0, End: 0, Text: "a\nb\nc\nd\ne\n"}}}); !r.OK {
			t.Fatalf("apply %d = %+v", i, r)
		}
		check("after apply")
		reject()
		check("after reject")

		// patch is the other writer's path into the same catch-up: dump the
		// whole file, hand back edited text, then reject the set it proposes.
		d := c.do(h, control.Request{Op: "dump", Path: path})
		if !d.OK {
			t.Fatalf("dump %d = %+v", i, d)
		}
		if r := c.do(h, control.Request{Op: "patch", Path: path, DumpID: d.DumpID,
			PatchText: d.Text() + "extra\n"}); !r.OK {
			t.Fatalf("patch %d = %+v", i, r)
		}
		check("after patch")
		reject()
		check("after reject patch")
	}
}

// A hunk that would land in a proposed change set is refused and carries the
// group that owns the text, so the caller can tell a lease from a stale offset
// and knows which decision has to come first.
func TestControlLeaseRefusalCarriesTheGroup(t *testing.T) {
	h := controlHarness(t, "hello world\n")
	c := h.dial(t)
	path := h.Tabs.Active().File.Path

	// The first proposal replaces "world", which leases offsets 6..11.
	read := c.do(h, control.Request{Op: "text"})
	base := read.Version
	if !c.do(h, control.Request{Op: "apply", Path: path, Base: &base,
		Hunks: []control.Hunk{{Start: 6, End: 11, Text: "socket"}}}).OK {
		t.Fatal("the first apply failed")
	}
	gs := c.do(h, control.Request{Op: "groups", Path: path})
	var owner uint64
	for _, g := range gs.Groups {
		if g.State == "proposed" {
			owner = g.ID
		}
	}
	if owner == 0 {
		t.Fatalf("no proposal listed: %+v", gs.Groups)
	}

	// Reject it first: a rejection is the human's decision and stays locked, so
	// a second writer's hunk over it is refused. (A still-proposed set is only
	// advisory now — another writer may land over it and own the result, with
	// the superseded set reported as moved; that is covered in the piece table's
	// tests.)
	if r := c.do(h, control.Request{Op: "reject", Path: path, Group: owner}); !r.OK {
		t.Fatalf("reject = %+v", r)
	}
	c2 := h.dial(t)
	read = c2.do(h, control.Request{Op: "text"})
	base = read.Version
	res := c2.do(h, control.Request{Op: "apply", Path: path, Base: &base,
		Hunks: []control.Hunk{{Start: 0, End: 11, Text: "goodbye"}}})
	if res.OK || len(res.Conflicts) != 1 {
		t.Fatalf("apply = %+v, want one lease refusal", res)
	}
	if got := res.Conflicts[0].Group; got != owner {
		t.Errorf("conflict group = %d, want the leasing set %d", got, owner)
	}
	// The refusal names the owning author and the owner's current span, so the
	// caller does not need a second groups call to act on it.
	if got := res.Conflicts[0].Author; got != c.c.Author() {
		t.Errorf("conflict author = %d, want the leasing writer %d", got, c.c.Author())
	}
	if got, want := res.Conflicts[0].Start, 6; got != want {
		t.Errorf("conflict start = %d, want %d", got, want)
	}
	if got, want := res.Conflicts[0].End, 12; got != want {
		t.Errorf("conflict end = %d, want %d", got, want)
	}

	// The one live set has nothing to overlap, so it carries no overlap marker;
	// the lease's refusal behaviour is otherwise unchanged.
	gs = c.do(h, control.Request{Op: "groups", Path: path})
	var ownerGroup control.Group
	for _, g := range gs.Groups {
		if g.ID == owner {
			ownerGroup = g
		}
	}
	if ownerGroup.Overlaps != nil {
		t.Errorf("a single live set reported an overlap: %+v", ownerGroup.Overlaps)
	}
}

// proposeOver pushes one proposal into path as a fresh identity and returns the
// connection, the author id it bound, and the proposed set's id. The two-
// identity tests share it so the setup is not repeated and cannot drift.
func proposeOver(t *testing.T, h *harness, path, identity, name string, start, end int, text string) (*client, uint8, uint64) {
	t.Helper()
	c := h.dial(t)
	hi := c.do(h, control.Request{Op: "hello", Identity: identity, Name: name})
	if !hi.OK {
		t.Fatalf("hello %s = %+v", identity, hi)
	}
	read := c.do(h, control.Request{Op: "text", Path: path})
	base := read.Version
	if r := c.do(h, control.Request{Op: "apply", Path: path, Base: &base,
		Hunks: []control.Hunk{{Start: start, End: end, Text: text}}}); !r.OK {
		t.Fatalf("proposal apply = %+v", r)
	}
	var id uint64
	for _, g := range c.do(h, control.Request{Op: "groups", Path: path}).Groups {
		if g.State == "proposed" {
			id = g.ID
		}
	}
	if id == 0 {
		t.Fatalf("no proposed set after the proposal apply")
	}
	return c, hi.Author, id
}

// A second identity's apply over a first identity's Proposed span is the real
// advisory-lease path through host.Apply: it lands, and the warning names the
// run's actual set, its author and the span it held. The Dispatch tests use a
// canned memHost, so this pins the semantics rather than the plumbing. Modelled
// on TestControlLeaseRefusalCarriesTheGroup (two identities over the real host).
func TestControlApplyOverAProposalWarnsTheRealSet(t *testing.T) {
	h := controlHarness(t, "hello world\n")
	path := h.Tabs.Active().File.Path

	_, owner, id := proposeOver(t, h, path, "occupant", "claude-a", 6, 11, "socket")

	editor := h.dial(t)
	if hi := editor.do(h, control.Request{Op: "hello", Identity: "editor", Name: "claude-b"}); !hi.OK {
		t.Fatalf("hello = %+v", hi)
	}
	read := editor.do(h, control.Request{Op: "text", Path: path})
	base := read.Version
	res := editor.do(h, control.Request{Op: "apply", Path: path, Base: &base,
		Hunks: []control.Hunk{{Start: 6, End: 12, Text: "port"}}})
	if len(res.Conflicts) != 0 {
		t.Fatalf("apply = %+v, want the advisory proposal to allow it", res)
	}
	if len(res.Warnings) != 1 {
		t.Fatalf("warnings = %+v, want the one superseded set", res.Warnings)
	}
	w := res.Warnings[0]
	if w.Group != id {
		t.Errorf("warning group = %d, want %d", w.Group, id)
	}
	if w.Author != owner {
		t.Errorf("warning author = %d, want the occupant %d", w.Author, owner)
	}
	if w.Start != 6 || w.End != 12 {
		t.Errorf("warning span = %d..%d, want the superseded run 6..12", w.Start, w.End)
	}
}

// patch is the other writer's path into the same diff, so the real host.Patch
// mapping reports the same warning shape with the same owner and span. Modelled
// on TestControlApplyOverAProposalWarnsTheRealSet and on
// TestControlLeaseRefusalCarriesTheGroup.
func TestControlPatchOverAProposalWarnsTheRealSet(t *testing.T) {
	h := controlHarness(t, "hello world\n")
	path := h.Tabs.Active().File.Path

	_, owner, id := proposeOver(t, h, path, "occupant", "claude-a", 6, 11, "socket")

	editor := h.dial(t)
	if hi := editor.do(h, control.Request{Op: "hello", Identity: "editor", Name: "claude-b"}); !hi.OK {
		t.Fatalf("hello = %+v", hi)
	}
	d := editor.do(h, control.Request{Op: "dump", Path: path})
	if !d.OK {
		t.Fatalf("dump = %+v", d)
	}
	res := editor.do(h, control.Request{Op: "patch", Path: path, DumpID: d.DumpID,
		PatchText: "hello port\n"})
	if len(res.Conflicts) != 0 {
		t.Fatalf("patch = %+v, want the advisory proposal to allow it", res)
	}
	if len(res.Warnings) != 1 {
		t.Fatalf("warnings = %+v, want the one superseded set", res.Warnings)
	}
	w := res.Warnings[0]
	if w.Group != id || w.Author != owner {
		t.Errorf("warning = %+v, want set %d by the occupant %d", w, id, owner)
	}
	if w.Start != 6 || w.End != 12 {
		t.Errorf("warning span = %d..%d, want 6..12", w.Start, w.End)
	}
}

// A patch whose every hunk is refused commits nothing, so the host must not
// mark a change set it did not open. host.Patch guards that with
// Version() > before because LastGroup names the group Begin reserved, which no
// hunk joined; without the guard ProposeGroup would leave that empty set
// proposed and could flip the state named after an all-conflict patch. Modelled
// on TestControlLeaseRefusalCarriesTheGroup (two identities over the real host)
// and TestControlPatchOverAProposalWarnsTheRealSet (the real-host patch path).
func TestControlPatchAllRefusedDoesNotReproposePriorSet(t *testing.T) {
	h := controlHarness(t, "hello world\n")
	path := h.Tabs.Active().File.Path
	sess := h.Pane().File.Session()

	// The occupant proposes over "world", and the human rejects it. Rejected is
	// a locked lease, so every hunk of a later patch over those bytes is refused.
	occupant, _, prior := proposeOver(t, h, path, "occupant", "claude-a", 6, 11, "socket")
	if r := occupant.do(h, control.Request{Op: "reject", Path: path, Group: prior}); !r.OK {
		t.Fatalf("reject = %+v", r)
	}

	// A second identity patches the whole file, so its one diff lands on the
	// rejected run and conflicts.
	editor := h.dial(t)
	if hi := editor.do(h, control.Request{Op: "hello", Identity: "editor", Name: "claude-b"}); !hi.OK {
		t.Fatalf("hello = %+v", hi)
	}
	d := editor.do(h, control.Request{Op: "dump", Path: path})
	if !d.OK {
		t.Fatalf("dump = %+v", d)
	}
	res := editor.do(h, control.Request{Op: "patch", Path: path, DumpID: d.DumpID,
		PatchText: "hello port\n"})
	if res.OK || len(res.Conflicts) != 1 {
		t.Fatalf("patch = %+v, want every hunk refused", res)
	}
	if got := res.Conflicts[0].Group; got != prior {
		t.Errorf("conflict group = %d, want the rejected set %d", got, prior)
	}
	if res.Version != d.Version {
		t.Errorf("version = %d on an all-refused patch, want %d", res.Version, d.Version)
	}

	// The guard: the reserved group was never joined, so it is not left
	// Proposed, and the prior set keeps its decision.
	if got := sess.GroupState(prior); got != piecetable.Rejected {
		t.Errorf("prior set state = %v after an all-refused patch, want Rejected", got)
	}
	if got := sess.GroupState(sess.LastGroup()); got == piecetable.Proposed {
		t.Errorf("all-refused patch left reserved set %d proposed", sess.LastGroup())
	}
}

// Landing over a writer's Proposed span notifies that writer through the
// mailbox -- spec 7b's "notified (mailbox), never summoned" -- naming the
// editing author and the superseded set. Modelled on
// TestRecvDeliversWhatWasSaidWhileAway (the box keeps for a durable identity)
// and TestControlApplyOverAProposalWarnsTheRealSet (the real host apply path).
func TestControlSupersededAuthorIsNotified(t *testing.T) {
	h := controlHarness(t, "hello world\n")
	path := h.Tabs.Active().File.Path

	occupant, _, id := proposeOver(t, h, path, "occupant", "claude-a", 6, 11, "socket")

	editor := h.dial(t)
	hi := editor.do(h, control.Request{Op: "hello", Identity: "editor", Name: "claude-b"})
	if !hi.OK {
		t.Fatalf("hello = %+v", hi)
	}
	read := editor.do(h, control.Request{Op: "text", Path: path})
	base := read.Version
	res := editor.do(h, control.Request{Op: "apply", Path: path, Base: &base,
		Hunks: []control.Hunk{{Start: 6, End: 12, Text: "port"}}})
	if len(res.Warnings) != 1 {
		t.Fatalf("warnings = %+v, want the superseded set", res.Warnings)
	}

	got := occupant.do(h, control.Request{Op: "recv"})
	if !got.OK || len(got.Messages) != 1 {
		t.Fatalf("recv = %+v, want the supersession notice", got)
	}
	msg := got.Messages[0]
	if msg.From != control.AuthorOriginal {
		t.Errorf("notice from = %d, want the editor (AuthorOriginal)", msg.From)
	}
	for _, want := range []string{
		fmt.Sprintf("change set %d", id),
		fmt.Sprintf("by author %d", hi.Author),
		"landed over",
	} {
		if !strings.Contains(msg.Text, want) {
			t.Errorf("notice = %q, want it to contain %q", msg.Text, want)
		}
	}
}

// The supersession notice ships no path: the server only knows req.Path in the
// editor's spelling, and the recipient is another writer whose view of the tree
// is not known there, so an absolute editor path would be one the recipient
// cannot resolve. The set, span and author still ride. Modelled on
// TestControlSupersededAuthorIsNotified (the same mailbox delivery over the real
// host).
func TestControlSupersededNoticeShipsNoPath(t *testing.T) {
	h := controlHarness(t, "hello world\n")
	path := h.Tabs.Active().File.Path

	occupant, _, id := proposeOver(t, h, path, "occupant", "claude-a", 6, 11, "socket")

	editor := h.dial(t)
	hi := editor.do(h, control.Request{Op: "hello", Identity: "editor", Name: "claude-b"})
	if !hi.OK {
		t.Fatalf("hello = %+v", hi)
	}
	read := editor.do(h, control.Request{Op: "text", Path: path})
	base := read.Version
	res := editor.do(h, control.Request{Op: "apply", Path: path, Base: &base,
		Hunks: []control.Hunk{{Start: 6, End: 12, Text: "port"}}})
	if len(res.Warnings) != 1 {
		t.Fatalf("warnings = %+v, want the superseded set", res.Warnings)
	}

	got := occupant.do(h, control.Request{Op: "recv"})
	if !got.OK || len(got.Messages) != 1 {
		t.Fatalf("recv = %+v, want the supersession notice", got)
	}
	msg := got.Messages[0]
	if strings.Contains(msg.Text, path) {
		t.Errorf("notice = %q, want it to name no editor-spelled path", msg.Text)
	}
	if strings.Contains(msg.Text, "/") {
		t.Errorf("notice = %q, want it to ship no path at all", msg.Text)
	}
	// The message otherwise keeps its shape: the set, its span and the editing
	// author.
	for _, want := range []string{
		fmt.Sprintf("change set %d", id),
		"(bytes 6..12)",
		fmt.Sprintf("by author %d", hi.Author),
	} {
		if !strings.Contains(msg.Text, want) {
			t.Errorf("notice = %q, want it to contain %q", msg.Text, want)
		}
	}
}

// findGroup returns the group with id from a listing, failing if it is absent.
func findGroup(t *testing.T, groups []control.Group, id uint64) control.Group {
	t.Helper()
	for _, g := range groups {
		if g.ID == id {
			return g
		}
	}
	t.Fatalf("no change set %d in %+v", id, groups)
	return control.Group{}
}

// Two live change sets whose rebased ranges intersect are reported on both, in
// groups and in diff, with the other's author and span. The editor does not
// arbitrate: nothing is clamped, merged or refused here.
func TestControlGroupsReportLiveOverlaps(t *testing.T) {
	h := controlHarness(t, "hello world\n")
	c := h.dial(t)
	path := h.Tabs.Active().File.Path
	p := h.Pane()
	sess := p.File.Session()

	// First proposal inserts "AA" at 0.
	p.File.Begin()
	p.File.ApplyDiff(piecetable.Agent, sess.Version(), []piecetable.Hunk{{Start: 0, End: 0, Text: "AA"}})
	p.File.End()
	a := sess.LastGroup()
	sess.MarkGroup(a, piecetable.Proposed)

	// A second writer's proposal overlaps it. The op is inserted straight on
	// the session, which builds the overlap without routing it through
	// ApplyDiff; what is under test is the report, not how the overlap arose.
	// (An ApplyDiff over A's Proposed run would land now too — a Proposed lease
	// is advisory — and report the same overlap.)
	sess.Begin()
	sess.Insert(piecetable.Agent+1, 1, "BB")
	sess.End()
	b := sess.LastGroup()
	sess.MarkGroup(b, piecetable.Proposed)
	// Bring the file's line index up to the op inserted straight on the
	// session, so diff's line mapping is honest.
	p.File.ApplyDiff(piecetable.Agent, sess.Version(), nil)

	gs := c.do(h, control.Request{Op: "groups", Path: path})
	aGroup, bGroup := findGroup(t, gs.Groups, a), findGroup(t, gs.Groups, b)
	if aGroup.Overlaps == nil || len(aGroup.Overlaps.Sets) != 1 {
		t.Fatalf("group %d overlaps = %+v, want one", a, aGroup.Overlaps)
	}
	if o := aGroup.Overlaps.Sets[0]; o.Group != b || o.Author != uint8(piecetable.Agent+1) {
		t.Errorf("group %d overlaps %+v, want set %d by agent %d", a, o, b, piecetable.Agent+1)
	}
	if bGroup.Overlaps == nil || len(bGroup.Overlaps.Sets) != 1 || bGroup.Overlaps.Sets[0].Group != a {
		t.Errorf("group %d overlaps = %+v, want set %d", b, bGroup.Overlaps, a)
	}

	d := c.do(h, control.Request{Op: "diff", Path: path})
	var diffs []control.DiffGroup
	if err := json.Unmarshal([]byte(d.DiffJSON), &diffs); err != nil {
		t.Fatalf("diff json: %v", err)
	}
	carried := false
	for _, dg := range diffs {
		if dg.ID != b || dg.Overlaps == nil || len(dg.Overlaps.Sets) != 1 {
			continue
		}
		carried = true
		if dg.Overlaps.Sets[0].Group != a {
			t.Errorf("diff overlap = %+v, want set %d", dg.Overlaps, a)
		}
	}
	if !carried {
		t.Errorf("diff = %+v, want set %d to carry the overlap", diffs, b)
	}
}

// A rejected set a later edit has wedged is refused with the live set whose
// span overlaps it -- group, author and span -- so the caller can clear that
// one first instead of guessing.
func TestControlClearReportsTheBlockingOverlap(t *testing.T) {
	h := controlHarness(t, "hello world\n")
	c := h.dial(t)
	path := h.Tabs.Active().File.Path
	p := h.Pane()
	sess := p.File.Session()

	// A rejected set deletes "world" (6..11). A pure deletion owns no inserted
	// run, so it is not leased and a later edit may overwrite the bytes its
	// reversal would have to put back.
	p.File.Begin()
	if !p.File.Delete(piecetable.Agent, 6, 5) {
		t.Fatal("the rejected set's delete was refused")
	}
	p.File.End()
	id := sess.LastGroup()
	sess.MarkGroup(id, piecetable.Rejected)

	// A later user edit removes the whole line, so the reversal can no longer
	// place "world".
	p.File.Begin()
	if !p.File.Delete(piecetable.User, 0, 11) {
		t.Fatal("the later delete was refused")
	}
	p.File.End()

	res := c.do(h, control.Request{Op: "clear", Path: path, Group: id})
	if res.OK || len(res.Conflicts) != 1 {
		t.Fatalf("clear = %+v, want a structured refusal", res)
	}
	b := res.Conflicts[0]
	if b.Group == 0 {
		t.Errorf("block = %+v, want the blocking set named", b)
	}
	if b.Author != uint8(piecetable.User) {
		t.Errorf("block author = %d, want the user's %d", b.Author, piecetable.User)
	}
	if res.Err == "" || !strings.Contains(res.Err, "overlaps") {
		t.Errorf("error = %q, want it to name the overlap", res.Err)
	}
	if got := sess.GroupState(id); got != piecetable.Rejected {
		t.Errorf("state after a wedged clear = %v, want it kept Rejected", got)
	}
}

// The review verb is the socket form of the cmd+r toggle: it returns the
// pending change sets and, unless the caller asked for the list only, enters
// Review mode so the document is read-only while the sets are decided.
func TestControlReviewEntersTheMode(t *testing.T) {
	h := controlHarness(t, reviewFixture)
	c := h.dial(t)
	propose(t, h, piecetable.Hunk{Start: reviewAt, End: reviewAt + len(reviewOld), Text: reviewNew})

	r := c.do(h, control.Request{Op: "review"})
	if !r.OK {
		t.Fatalf("review = %+v", r)
	}
	if len(r.Groups) != 1 || r.Groups[0].State != "proposed" {
		t.Errorf("groups = %+v, want the one proposed set", r.Groups)
	}
	if h.mode != ModeReview {
		t.Errorf("mode = %v after review, want Review", h.mode)
	}

	// The list-only form leaves the mode alone.
	h.mode = ModeEdit
	l := c.do(h, control.Request{Op: "review", ReviewList: true})
	if !l.OK || len(l.Groups) != 1 {
		t.Fatalf("review -json = %+v", l)
	}
	if h.mode != ModeEdit {
		t.Errorf("mode = %v after review -json, want it left in Edit", h.mode)
	}
}

// review [path] enters Review mode on the named buffer, not whatever was in
// front: it focuses the buffer first, so the mode badge and the jump belong to
// the same tab as the listed sets. Without this, reviewing a background file
// listed its sets while the caret jumped in the active tab.
func TestControlReviewFocusesTheNamedBuffer(t *testing.T) {
	h := controlHarness(t, reviewFixture)
	c := h.dial(t)
	first := h.Tabs.Active().File.Path

	// A second file, with a proposal of its own, becomes active by its own
	// open. Propose against it, then bring the first back in front so the
	// review asks for a background buffer.
	second := filepath.Join(filepath.Dir(first), "second.go")
	h.OpenFile(second)
	h.typeText("hello world\n")
	propose(t, h, piecetable.Hunk{Start: reviewAt, End: reviewAt + len(reviewOld), Text: reviewNew})
	h.drain()

	h.OpenFile(first)
	h.drain()
	if got := h.Tabs.Active().File.Path; got != first {
		t.Fatalf("setup: active = %q, want the first buffer", got)
	}

	r := c.do(h, control.Request{Op: "review", Path: second})
	if !r.OK {
		t.Fatalf("review of a background path = %+v", r)
	}
	if len(r.Groups) != 1 || r.Groups[0].Path != second {
		t.Fatalf("groups = %+v, want the second buffer's set", r.Groups)
	}
	if h.mode != ModeReview {
		t.Errorf("mode = %v after review, want Review", h.mode)
	}
	if got := h.Tabs.Active().File.Path; got != second {
		t.Errorf("active = %q after review of %q, want the named buffer focused", got, second)
	}
}

// A read of a path nobody opened loads it headlessly: the buffer is tracked and
// addressable, but no tab appears in front of the user. This is the spec's
// loaded-versus-announced split at the socket.
func TestHeadlessReadDoesNotAddATab(t *testing.T) {
	h := controlHarness(t, "root\n")
	c := h.dial(t)
	dir := filepath.Dir(h.Tabs.Active().File.Path)
	path := filepath.Join(dir, "other.go")
	if err := os.WriteFile(path, []byte("other file\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	before := h.Tabs.Count()
	r := c.do(h, control.Request{Op: "text", Path: path})
	if !r.OK || r.Text() != "other file\n" {
		t.Fatalf("read of a closed path = %+v", r)
	}
	if got := h.Tabs.Count(); got != before {
		t.Errorf("tab count = %d, want %d: a read announced a tab", got, before)
	}
	if _, ok := h.findHeadless(path); !ok {
		t.Errorf("read path is not tracked headlessly")
	}
	for _, p := range h.Tabs.All() {
		if sameFile(path, p.File.Path) {
			t.Errorf("read path got a tab")
		}
	}
}

// version and lsp diagnostics reach a closed-but-readable path the same way:
// loaded and served, no tab.
func TestHeadlessVersionAndDiagnosticsDoNotAddTabs(t *testing.T) {
	h := controlHarness(t, "root\n")
	c := h.dial(t)
	dir := filepath.Dir(h.Tabs.Active().File.Path)
	path := filepath.Join(dir, "other.go")
	if err := os.WriteFile(path, []byte("package other\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := h.Tabs.Count()

	if r := c.do(h, control.Request{Op: "version", Path: path}); !r.OK {
		t.Fatalf("version of a closed path = %+v", r)
	}
	if got := h.Tabs.Count(); got != before {
		t.Errorf("version tab count = %d, want %d", got, before)
	}

	call, err := hostOf(h.App).LSP(path, 0, 0, "diagnostics")
	if err != nil {
		t.Fatalf("diagnostics of a closed path = %v", err)
	}
	if call == nil {
		t.Fatal("diagnostics answered with no caller")
	}
	if got := h.Tabs.Count(); got != before {
		t.Errorf("diagnostics tab count = %d, want %d", got, before)
	}
}

// A proposal must be visible: applying to a buffer that was only inspected
// announces it as a tab before the change set lands.
func TestHeadlessApplyAnnounces(t *testing.T) {
	h := controlHarness(t, "root\n")
	c := h.dial(t)
	dir := filepath.Dir(h.Tabs.Active().File.Path)
	path := filepath.Join(dir, "other.go")
	if err := os.WriteFile(path, []byte("hello world\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Read first so the buffer is loaded headlessly, then propose against it.
	read := c.do(h, control.Request{Op: "text", Path: path})
	base := read.Version
	before := h.Tabs.Count()
	if r := c.do(h, control.Request{Op: "apply", Path: path, Base: &base,
		Hunks: []control.Hunk{{Start: 0, End: 5, Text: "howdy"}}}); !r.OK {
		t.Fatalf("apply = %+v", r)
	}
	if got := h.Tabs.Count(); got != before+1 {
		t.Fatalf("tab count = %d, want %d: the proposal stayed hidden", got, before+1)
	}
	if _, ok := h.findHeadless(path); ok {
		t.Errorf("the pane is still headless after a proposal")
	}
	var pending int
	for _, p := range h.Tabs.All() {
		if sameFile(path, p.File.Path) {
			pending = len(p.File.Session().Pending())
		}
	}
	if pending != 1 {
		t.Errorf("pending = %d, want the one proposed set", pending)
	}
}

// The buffers listing carries headless buffers, marked so a caller can tell
// them from tabs.
func TestBuffersReportsHeadless(t *testing.T) {
	h := controlHarness(t, "root\n")
	c := h.dial(t)
	dir := filepath.Dir(h.Tabs.Active().File.Path)
	path := filepath.Join(dir, "other.go")
	if err := os.WriteFile(path, []byte("other\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if r := c.do(h, control.Request{Op: "text", Path: path}); !r.OK {
		t.Fatalf("read = %+v", r)
	}

	r := c.do(h, control.Request{Op: "buffers"})
	var sawHeadless, sawTab bool
	for _, b := range r.Buffers {
		switch {
		case b.Path == path:
			sawHeadless = true
			if !b.Headless {
				t.Errorf("headless buffer %s is not marked headless", b.Path)
			}
		case b.Headless:
			t.Errorf("tab %s is marked headless", b.Path)
		default:
			sawTab = true
		}
	}
	if !sawHeadless || !sawTab {
		t.Errorf("buffers = %+v, want both a tab and a headless buffer", r.Buffers)
	}
}

// The registry is bounded; a clean headless buffer is dropped and re-read on
// demand rather than accumulating for a whole inspection sweep.
func TestHeadlessEvictionReloads(t *testing.T) {
	h := newHarness(t, "root\n")
	hh := hostOf(h.App)
	dir := filepath.Dir(h.Tabs.Active().File.Path)

	paths := make([]string, 0, headlessMax+1)
	for i := 0; i < headlessMax+1; i++ {
		path := filepath.Join(dir, fmt.Sprintf("headless%d.go", i))
		if err := os.WriteFile(path, []byte("package h\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}
	for _, path := range paths {
		if _, _, _, err := hh.Read(path, control.FirstAgent, -1, -1, 0, 0, false); err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
	}
	if got := len(h.headless); got != headlessMax {
		t.Fatalf("headless registry = %d, want the cap %d", got, headlessMax)
	}
	if _, ok := h.findHeadless(paths[0]); ok {
		t.Errorf("the oldest headless buffer survived eviction")
	}
	if _, _, _, err := hh.Read(paths[0], control.FirstAgent, -1, -1, 0, 0, false); err != nil {
		t.Fatalf("reload %s: %v", paths[0], err)
	}
	if _, ok := h.findHeadless(paths[0]); !ok {
		t.Errorf("a dropped buffer did not reload on demand")
	}
}

// A close over the socket is a change to the session's tab set, so it must
// mark the session for rewriting the way the UI closeTabAt does. Without this
// a tab an agent closed over the socket reappears on the next editor start.
func TestControlCloseTouchesTheSession(t *testing.T) {
	h := controlHarness(t, "hello\n")
	path := h.Tabs.Active().File.Path
	h.sessionDirty = false

	if err := hostOf(h.App).Close(path); err != nil {
		t.Fatalf("close = %v", err)
	}
	if !h.sessionDirty {
		t.Error("a socket close left the session untouched; the tab will reappear on restart")
	}
	for _, p := range h.Tabs.All() {
		if sameFile(path, p.File.Path) {
			t.Errorf("tab for %s is still open after close", path)
		}
	}
}

// A close refused because the buffer is dirty is not a change to the session:
// nothing moved, so marking it dirty would write a session that never changed.
func TestControlRefusedCloseLeavesSessionUntouched(t *testing.T) {
	h := controlHarness(t, "hello\n")
	path := h.Tabs.Active().File.Path
	h.typeText("x") // unsaved, so the close is refused
	h.sessionDirty = false

	if err := hostOf(h.App).Close(path); err == nil {
		t.Fatal("close accepted a buffer with unsaved changes")
	}
	if h.sessionDirty {
		t.Error("a refused close marked the session dirty")
	}
}

// Goto moves the cursor and scrolls the viewport — both view state the session
// records — so it must mark the session for rewriting like the editor's own
// goto.
func TestControlGotoTouchesTheSession(t *testing.T) {
	h := controlHarness(t, "one\ntwo\nthree\n")
	path := h.Tabs.Active().File.Path
	h.sessionDirty = false

	if err := hostOf(h.App).Goto(path, 2, 1); err != nil {
		t.Fatalf("goto = %v", err)
	}
	if !h.sessionDirty {
		t.Error("a socket goto left the session untouched")
	}
}

// A hunk's offsets are in the base version's coordinates, so the bounds check
// has to measure against the length the base had, not the current text. A
// driver that read the empty version 0 and then submitted an offset past it
// must be refused: the buffer is 11 bytes now, and a current-length check is
// what let the out-of-range offset be rebased to EOF instead.
func TestApplyValidatesAgainstTheBaseLength(t *testing.T) {
	h := controlHarness(t, "")
	c := h.dial(t)

	base := c.do(h, control.Request{Op: "text"}).Version // 0: the empty document
	if r := c.do(h, control.Request{
		Op:    "apply",
		Base:  &base,
		Hunks: []control.Hunk{{Start: 0, End: 0, Text: "hello world"}},
	}); !r.OK {
		t.Fatalf("setup apply = %+v", r)
	}

	// Offset 5 is out of range for base 0, though it is inside the current
	// "hello world". The same base pointer: the base did not move.
	r := c.do(h, control.Request{
		Op:    "apply",
		Base:  &base,
		Hunks: []control.Hunk{{Start: 5, End: 5, Text: "X"}},
	})
	if r.OK || !strings.Contains(r.Err, "offset out of range") {
		t.Fatalf("stale offset = OK %v err %q, want offset out of range", r.OK, r.Err)
	}
	if got := h.text(); got != "hello world" {
		t.Errorf("buffer = %q, want the refused hunk to have changed nothing", got)
	}
}

// A rename carries an open clean buffer: the file moves, the pane keeps its
// text and version, and the path it is keyed on follows. The tab is the same
// object, so the session records the new name rather than a reopened one.
func TestControlRenameCarriesACleanBuffer(t *testing.T) {
	h := controlHarness(t, "hello\n")
	c := h.dial(t)
	p := h.Tabs.Active()
	old := p.File.Path
	newPath := filepath.Join(filepath.Dir(old), "renamed.go")
	version := p.File.Session().Version()
	h.sessionDirty = false

	r := c.do(h, control.Request{Op: "rename", Path: old, NewPath: newPath})
	if !r.OK {
		t.Fatalf("rename = %+v", r)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("old path still on disk (err=%v)", err)
	}
	if data, err := os.ReadFile(newPath); err != nil || string(data) != "hello\n" {
		t.Errorf("new path on disk = %q, err %v", data, err)
	}
	if p.File.Path != newPath {
		t.Errorf("pane path = %q, want %q", p.File.Path, newPath)
	}
	if got := p.File.Session().Version(); got != version {
		t.Errorf("version = %d, want %d — the buffer was reopened, not carried", got, version)
	}
	if h.Tabs.Active() != p {
		t.Error("the tab did not follow the rename")
	}
	if !h.sessionDirty {
		t.Error("a carried rename did not mark the session for rewriting")
	}
}

// A dirty buffer is refused: the rename would move the file out from under
// unsaved text, and there is no force path. Nothing moves.
func TestControlRenameRefusesADirtyBuffer(t *testing.T) {
	h := controlHarness(t, "hello\n")
	c := h.dial(t)
	h.typeText("x")
	p := h.Tabs.Active()
	old := p.File.Path
	newPath := filepath.Join(filepath.Dir(old), "renamed.go")

	r := c.do(h, control.Request{Op: "rename", Path: old, NewPath: newPath})
	if r.OK {
		t.Fatal("a dirty buffer was renamed")
	}
	if !strings.Contains(strings.ToLower(r.Err), "unsaved") {
		t.Errorf("refusal = %q, want it to name the unsaved changes", r.Err)
	}
	if _, err := os.Stat(old); err != nil {
		t.Errorf("the refused rename moved the file: %v", err)
	}
}

// A buffer holding a pending change set is refused the same way: the proposed
// text exists only in the buffer, and renaming the file does not materialise it.
func TestControlRenameRefusesPendingSets(t *testing.T) {
	h := controlHarness(t, "hello world\n")
	c := h.dial(t)
	read := c.do(h, control.Request{Op: "text"})
	base := read.Version
	if r := c.do(h, control.Request{
		Op:    "apply",
		Base:  &base,
		Hunks: []control.Hunk{{Start: 0, End: 0, Text: "x"}},
	}); !r.OK {
		t.Fatalf("setup apply = %+v", r)
	}
	p := h.Tabs.Active()
	old := p.File.Path
	newPath := filepath.Join(filepath.Dir(old), "renamed.go")

	r := c.do(h, control.Request{Op: "rename", Path: old, NewPath: newPath})
	if r.OK {
		t.Fatal("a buffer with pending change sets was renamed")
	}
	if !strings.Contains(r.Err, "pending") {
		t.Errorf("refusal = %q, want it to name the pending sets", r.Err)
	}
	if _, err := os.Stat(old); err != nil {
		t.Errorf("the refused rename moved the file: %v", err)
	}
}

// A buffer whose file is not on disk — open -create makes one — has nothing to
// move, so the rename is the pane's path alone and works even while the buffer
// is dirty: the work stays in the piece table and follows the name. Refusing it
// forced a driver to close -discard and leave the old name behind.
func TestControlRenameCarriesANotYetSavedBuffer(t *testing.T) {
	h := controlHarness(t, "root\n")
	c := h.dial(t)
	dir := filepath.Dir(h.Tabs.Active().File.Path)
	old := filepath.Join(dir, "fresh.go")

	if r := c.do(h, control.Request{Op: "open", Path: old, Create: true}); !r.OK {
		t.Fatalf("open -create = %+v", r)
	}
	p := h.Tabs.Active()
	h.typeText("draft\n")
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("open -create wrote a file: %v", err)
	}

	newPath := filepath.Join(dir, "renamed.go")
	r := c.do(h, control.Request{Op: "rename", Path: old, NewPath: newPath})
	if !r.OK {
		t.Fatalf("rename of a not-yet-saved buffer = %+v, want it carried", r)
	}
	if p.File.Path != newPath {
		t.Errorf("pane path = %q, want %q", p.File.Path, newPath)
	}
	if got := p.File.Text(); got != "draft" {
		t.Errorf("buffer text = %q, want the unsaved draft preserved", got)
	}
	if h.Tabs.Active() != p {
		t.Error("the tab did not follow the rename")
	}
	if _, err := os.Stat(newPath); !os.IsNotExist(err) {
		t.Errorf("rename wrote a file for a buffer that had none: %v", err)
	}
}

// close -discard drops the buffer but leaves the file on disk, and the editor
// says so, so a driver that recreated the content under a new name does not
// leave a duplicate behind silently.
func TestControlCloseDiscardNotesTheFileThatRemains(t *testing.T) {
	h := controlHarness(t, "hello\n")
	c := h.dial(t)
	path := h.Tabs.Active().File.Path
	h.typeText("x") // dirty, so a plain close would refuse

	r := c.do(h, control.Request{Op: "close", Path: path, Discard: true})
	if !r.OK {
		t.Fatalf("close -discard = %+v", r)
	}
	if !r.Remains {
		t.Error("close -discard did not report that a file remains")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("discard removed the file: %v", err)
	}
	if got := h.Status(); !strings.Contains(got, "still on disk") {
		t.Errorf("status = %q, want it to say the file remains", got)
	}
}

// A discarded buffer that never reached disk has nothing to report: there is no
// file to leave behind.
func TestControlCloseDiscardQuietWhenNothingRemains(t *testing.T) {
	h := controlHarness(t, "root\n")
	c := h.dial(t)
	dir := filepath.Dir(h.Tabs.Active().File.Path)
	fresh := filepath.Join(dir, "fresh.go")
	if r := c.do(h, control.Request{Op: "open", Path: fresh, Create: true}); !r.OK {
		t.Fatalf("open -create = %+v", r)
	}
	if r := c.do(h, control.Request{Op: "close", Path: fresh, Discard: true}); !r.OK {
		t.Fatalf("close -discard = %+v", r)
	} else if r.Remains {
		t.Error("close -discard of a buffer with no file reported a remainder")
	}
	if got := h.Status(); strings.Contains(got, "still on disk") {
		t.Errorf("status = %q, want no remainder note for a buffer with no file", got)
	}
}

// A file nobody has open is a plain filesystem move: no tab appears, and the
// bytes are at the new name.
func TestControlRenameMovesAnUnopenedFile(t *testing.T) {
	h := controlHarness(t, "root\n")
	c := h.dial(t)
	dir := filepath.Dir(h.Tabs.Active().File.Path)
	old := filepath.Join(dir, "loose.go")
	if err := os.WriteFile(old, []byte("loose\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	newPath := filepath.Join(dir, "moved.go")

	r := c.do(h, control.Request{Op: "rename", Path: old, NewPath: newPath})
	if !r.OK {
		t.Fatalf("rename = %+v", r)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("old path still on disk (err=%v)", err)
	}
	if data, err := os.ReadFile(newPath); err != nil || string(data) != "loose\n" {
		t.Errorf("new path on disk = %q, err %v", data, err)
	}
	if got := h.Tabs.Count(); got != 1 {
		t.Errorf("tab count = %d, want 1 — an unopened rename opened a tab", got)
	}
}

// The old name has to be in the caller's claim set, so an agent cannot move a
// file it never declared.
func TestControlRenameRefusesAnUnclaimedOld(t *testing.T) {
	h := controlHarness(t, "hello\n")
	c := h.dial(t)
	old := h.Tabs.Active().File.Path
	newPath := filepath.Join(filepath.Dir(old), "renamed.go")

	r := c.roundtrip(h, control.Request{Op: "rename", Path: old, NewPath: newPath})
	if r.OK {
		t.Fatal("a rename of an unclaimed file was allowed")
	}
	if _, err := os.Stat(old); err != nil {
		t.Errorf("the refused rename moved the file: %v", err)
	}
}

// A case-only rename goes through the two-step in renameFile even on a
// case-sensitive filesystem, so the destination spelling is what lands on disk.
// The assertion does not depend on the filesystem folding case: it only checks
// that the two-hop move arrived where it was asked to and the pane followed.
func TestControlRenameCaseOnly(t *testing.T) {
	h := controlHarness(t, "hello\n")
	c := h.dial(t)
	p := h.Tabs.Active()
	old := p.File.Path
	newPath := filepath.Join(filepath.Dir(old), "TEST.GO")

	r := c.do(h, control.Request{Op: "rename", Path: old, NewPath: newPath})
	if !r.OK {
		t.Fatalf("case-only rename = %+v", r)
	}
	// The old spelling is deliberately not asserted absent: on a
	// case-insensitive filesystem it still resolves to the same file. What
	// matters is that the new spelling reads back and the pane followed.
	if data, err := os.ReadFile(newPath); err != nil || string(data) != "hello\n" {
		t.Errorf("new path on disk = %q, err %v", data, err)
	}
	if p.File.Path != newPath {
		t.Errorf("pane path = %q, want %q", p.File.Path, newPath)
	}
}

// Goto names a session position, and a rejected fold above it must not
// renumber the request: the caret lands file-true on the named session line,
// while the viewport centres on the display row that draws it.
func TestControlGotoIsFileTrueUnderAFold(t *testing.T) {
	h := newHarness(t, strings.Repeat("xxxxxxxxxx\n", 40))
	p := h.Pane()
	hidden := propose(t, h, piecetable.Hunk{Start: 0, End: 0, Text: "X\nY\n"})
	if !p.File.RejectGroup(hidden) {
		t.Fatal("rejecting the fold's set failed")
	}
	h.Draw()

	// Session line 31 (1-based 32) is original line 29; the fold draws it one
	// row up, at display row 30.
	if err := hostOf(h.App).Goto(p.File.Path, 32, 1); err != nil {
		t.Fatalf("goto = %v", err)
	}
	caret := p.Cursors.Primary().Head
	if line := p.File.LineOf(caret); line != 31 {
		t.Fatalf("caret line = %d, want the named session line 31", line)
	}
	row, _ := p.DispPos(caret)
	if row != 30 {
		t.Fatalf("caret display row = %d, want 30", row)
	}
	want := row - p.Viewport.Rows/2
	if want < 0 {
		want = 0
	}
	if got := p.Viewport.Top; got != want {
		t.Errorf("viewport top = %d, want %d: centred on the display row, not session line 31",
			got, want)
	}
}
