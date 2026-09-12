package app

import (
	"encoding/json"
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

	// A second hunk overlapping that span is refused, not rebased through it.
	read = c.do(h, control.Request{Op: "text"})
	base = read.Version
	res := c.do(h, control.Request{Op: "apply", Path: path, Base: &base,
		Hunks: []control.Hunk{{Start: 0, End: 11, Text: "goodbye"}}})
	if res.OK || len(res.Conflicts) != 1 {
		t.Fatalf("apply = %+v, want one lease refusal", res)
	}
	if got := res.Conflicts[0].Group; got != owner {
		t.Errorf("conflict group = %d, want the leasing set %d", got, owner)
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
