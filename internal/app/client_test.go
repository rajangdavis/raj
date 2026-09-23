package app

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"raj/internal/control"
	"raj/internal/editor"
	"raj/internal/session"
	"raj/internal/ui"
)

// clientHarness is a daemon app plus a client app rendering its document.
type clientHarness struct {
	srv *harness
	cli *harness
}

// attachClient dials an already-running daemon and returns the attached client.
// StartClient blocks while the daemon event thread answers buffers and
// snapshots, and that thread is this test goroutine, so it runs beside a pump.
func attachClient(t *testing.T, srv *harness) *clientHarness {
	t.Helper()
	return attachClientAt(t, srv, Options{}, 120, 12)
}

// attachClientAt is attachClient with an explicit profile and terminal size, so
// a test can drive the phone drawer the connection dot lives in.
func attachClientAt(t *testing.T, srv *harness, o Options, cols, rows int) *clientHarness {
	t.Helper()
	return attachClientAtRoot(t, srv, o, cols, rows, t.TempDir())
}

// attachClientAtRoot is attachClientAt with an explicit client workspace root,
// so two attaches can share one state store and exercise the saved view.
func attachClientAtRoot(t *testing.T, srv *harness, o Options, cols, rows int, root string) *clientHarness {
	t.Helper()
	host := ui.NewFakeHost(cols, rows)
	t.Cleanup(func() { host.Close() })
	o.Attach = true
	o.AttachAddr = srv.ControlPath()
	ca := NewWithOptions(host, root, o)
	t.Cleanup(ca.CloseClient)
	cli := &harness{App: ca, host: host}
	done := make(chan struct{})
	go func() {
		ca.StartClient()
		close(done)
	}()
	deadline := time.After(3 * time.Second)
	started := false
	for !started {
		select {
		case <-done:
			started = true
		case <-deadline:
			t.Fatal("client start never finished")
		default:
			srv.drain()
			time.Sleep(time.Millisecond)
		}
	}
	return &clientHarness{srv: srv, cli: cli}
}

// newClientHarness starts a local editor, gives it a proposed change set, and
// attaches a second app to it. The proposal is what makes the client frame
// prove it carried the daemon review state, not only the agreed text.
func newClientHarness(t *testing.T) *clientHarness {
	t.Helper()
	srv := controlHarness(t, "hello\nworld\n")
	c := srv.dial(t)
	base := c.do(srv, control.Request{Op: "text"}).Version
	if r := c.do(srv, control.Request{Op: "apply", Base: &base,
		Hunks: []control.Hunk{{Start: 0, End: 0, Text: "// proposed\n"}}}); !r.OK {
		t.Fatalf("apply = %+v", r)
	}
	return attachClient(t, srv)
}

// The client builds its tabs from the daemon snapshots: the text is the agreed
// view, the pending proposal rides with it, and Review is the default mode.
// Without client mode the app would open the path from the local temp root,
// where no such file exists, and show nothing.
func TestClientLoadsDaemonDocument(t *testing.T) {
	ch := newClientHarness(t)
	ch.cli.drain()

	p := ch.cli.Tabs.Active()
	if p == nil {
		t.Fatal("client attached with no tab")
	}
	if got, want := p.File.Text(), "// proposed\nhello\nworld\n"; got != want {
		t.Errorf("text = %q, want %q", got, want)
	}
	if ch.cli.mode != ModeReview {
		t.Errorf("mode = %v, want Review", ch.cli.mode)
	}
	if pending := p.File.Session().Pending(); len(pending) == 0 {
		t.Error("the daemon proposal did not ride with the snapshot")
	}
	frame := ch.cli.host.Text()
	if !strings.Contains(frame, "// proposed") || !strings.Contains(frame, "hello") {
		t.Errorf("frame does not show the daemon text:\n%s", frame)
	}
}

// A watch wake re-fetches the changed buffer and updates the frame while the
// cursor keeps its line and column. Without the watch the client would show
// the document it first loaded forever; without the line/column carry the
// rebuild would reset the caret to the top.
func TestClientWatchUpdatesAndKeepsCursor(t *testing.T) {
	ch := newClientHarness(t)
	ch.cli.drain()

	p := ch.cli.Tabs.Active()
	if p == nil {
		t.Fatal("client attached with no tab")
	}
	// Sit on the last line, so an edit at the top leaves the line the cursor
	// is on unchanged.
	off := p.File.OffsetAt(2, 1)
	p.Cursors.Set(off, off)
	line, col := p.File.LineCol(off)

	ch.srv.typeText("X")
	ch.srv.Handle(ui.Tick{})

	deadline := time.After(3 * time.Second)
	for !strings.Contains(p.File.Text(), "X") {
		select {
		case <-deadline:
			t.Fatalf("client never saw the daemon edit; text = %q", p.File.Text())
		default:
		}
		ch.srv.drain()
		ch.cli.drain()
		time.Sleep(time.Millisecond)
	}

	gotLine, gotCol := p.File.LineCol(p.Cursors.Primary().Head)
	if gotLine != line || gotCol != col {
		t.Errorf("cursor = %d:%d, want %d:%d", gotLine, gotCol, line, col)
	}
	if frame := ch.cli.host.Text(); !strings.Contains(frame, "X") {
		t.Errorf("wake did not reach the frame:\n%s", frame)
	}
}

// A lost connection marks the client down (the dot) and keeps the last
// document; the retry loop keeps trying while the daemon is gone. A loss must
// not clear the tabs, and an orderly CloseClient is what ends the loop.
func TestClientLostConnectionKeepsDocument(t *testing.T) {
	ch := newClientHarness(t)
	ch.cli.drain()

	p := ch.cli.Tabs.Active()
	if p == nil {
		t.Fatal("client attached with no tab")
	}
	want := p.File.Text()
	before := ch.cli.Tabs.Count()

	// Every re-dial fails, so the client stays down and the document is what
	// the test observes while it waits.
	failClientDial(t)
	ch.cli.dropWatch()

	deadline := time.After(3 * time.Second)
	for !ch.cli.clientIsDown() {
		select {
		case <-deadline:
			t.Fatalf("a lost connection left the client healthy; status = %q", ch.cli.status)
		default:
		}
		ch.cli.drain()
		time.Sleep(time.Millisecond)
	}
	if ch.cli.Tabs.Count() != before || p.File.Text() != want {
		t.Errorf("a lost connection changed the tabs: %d/%q, want %d/%q",
			ch.cli.Tabs.Count(), p.File.Text(), before, want)
	}
}

// handleDot is the collapsed bar connection dot cell.
func handleDot(t *testing.T, h *harness) ui.Cell {
	t.Helper()
	row := h.drawerHandle.y + h.drawerHandle.h - 1
	return h.host.Last().At(0, row)
}

// The attached client phone bar shows a green dot while the link is healthy
// and a red one after the watch transport fails; the failure is no longer a
// status sentence.
func TestClientConnectionDot(t *testing.T) {
	srv := controlHarness(t, "hello\n")
	ch := attachClientAt(t, srv, Options{Phone: true}, 30, 30)
	ch.cli.drain()

	dot := handleDot(t, ch.cli)
	if dot.Rune != '●' {
		t.Fatalf("connected handle has no dot: %q", ch.cli.host.Last().Row(ch.cli.drawerHandle.y+ch.cli.drawerHandle.h-1))
	}
	if dot.Style.Fg != ui.Ansi(2) {
		t.Errorf("connected dot fg = %v, want green", dot.Style.Fg)
	}

	// Drop the transport under the parked watch with every re-dial failing,
	// so the dot stays red: the same failure the loss test drives.
	failClientDial(t)
	ch.cli.dropWatch()
	deadline := time.After(3 * time.Second)
	for !ch.cli.clientIsDown() {
		select {
		case <-deadline:
			t.Fatalf("the dot stayed green; status = %q", ch.cli.status)
		default:
		}
		ch.cli.drain()
		time.Sleep(time.Millisecond)
	}
	ch.cli.drain()
	if dot := handleDot(t, ch.cli); dot.Style.Fg != ui.Ansi(1) {
		t.Errorf("lost dot fg = %v, want red", dot.Style.Fg)
	}
	if strings.Contains(ch.cli.status, "connection lost") {
		t.Errorf("status = %q, want no textual loss", ch.cli.status)
	}
}

// The dot lives only in the phone drawer of an attached client: the ordinary
// editor and a non-phone client draw none.
func TestConnectionDotOnlyInPhoneClient(t *testing.T) {
	srv := controlHarness(t, "hello\n")
	ch := attachClient(t, srv) // a non-phone client
	ch.cli.drain()
	if strings.Contains(ch.cli.host.Text(), "●") {
		t.Error("a non-phone client drew the connection dot")
	}
	n := newHarness(t, "one\ntwo\n")
	n.Draw()
	if strings.Contains(n.host.Text(), "●") {
		t.Error("the ordinary editor drew a connection dot")
	}
}

// On a narrow phone bar the dot, a live status and the [actions] label all
// render, with the count truncated into the space between the dot and label.
func TestClientDotOnNarrowBar(t *testing.T) {
	srv := controlHarness(t, "hello\n")
	ch := attachClientAt(t, srv, Options{Phone: true}, 14, 30)
	ch.cli.drain()
	row := ch.cli.drawerHandle.y + ch.cli.drawerHandle.h - 1
	frame := ch.cli.host.Last()
	if frame.At(0, row).Rune != '●' {
		t.Fatalf("narrow handle has no dot: %q", frame.Row(row))
	}
	if !strings.Contains(frame.Row(row), "[actions]") {
		t.Errorf("narrow handle = %q, want the label", frame.Row(row))
	}
	if frame.At(2, row).Rune == 0 {
		t.Errorf("narrow handle = %q, want the count beside the dot", frame.Row(row))
	}
	ch.cli.status = "hi"
	ch.cli.Draw()
	if top := ch.cli.host.Last().Row(ch.cli.drawerHandle.y); !strings.Contains(top, "hi") {
		t.Errorf("status row = %q, want the status", top)
	}
}

// sameStrings reports whether two root lists are equal in order.
func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// scriptedAttach starts an app attached through a scripted daemon connection
// that reports roots (nil for an old server), so a test can drive the attach
// handshake without a real server. launch is the client's own root.
func scriptedAttach(t *testing.T, launch string, roots []string) *App {
	t.Helper()
	conn := newScriptedConn(1, nil, nil)
	conn.roots = roots
	setClientDial(t, func(string) (clientConn, error) { return conn, nil })
	host := ui.NewFakeHost(120, 30)
	t.Cleanup(func() { host.Close() })
	a := NewWithOptions(host, launch, Options{Attach: true, AttachAddr: "scripted"})
	t.Cleanup(a.CloseClient)
	a.StartClient()
	return a
}

// An attach adopts the daemon's workspace: the explorer, search and containment
// render the daemon's roots, not the directory the client was launched in. The
// failure mode this pins: the client showed its launch directory's tree while
// attached to a daemon serving two other roots.
func TestAttachAdoptsDaemonRoots(t *testing.T) {
	rootA, rootB := t.TempDir(), t.TempDir()
	launch := t.TempDir()
	a := scriptedAttach(t, launch, []string{rootA, rootB})

	if got := a.visible.All(); !sameStrings(got, []string{rootA, rootB}) {
		t.Errorf("visible roots = %q, want the daemon's %q", got, []string{rootA, rootB})
	}
	if got := a.Explorer.Tree.Roots; !sameStrings(got, []string{rootA, rootB}) {
		t.Errorf("explorer roots = %q, want the daemon's %q", got, []string{rootA, rootB})
	}
	if got := a.Search.Roots; !sameStrings(got, []string{rootA, rootB}) {
		t.Errorf("search roots = %q, want the daemon's %q", got, []string{rootA, rootB})
	}
	// The view roots — the store key — are still the client's launch root.
	if got := a.roots.All(); !sameStrings(got, []string{launch}) {
		t.Errorf("view roots = %q, want the launch root %q", got, launch)
	}
	if !a.visible.Contains(filepath.Join(rootB, "b.go")) {
		t.Error("a daemon root is not contained by the visible workspace")
	}
}

// The view store stays put: the client's saved tabs are keyed by the roots it
// was launched with, not the daemon's. The failure mode this pins: moving the
// store under the daemon's state dir would have two processes open one SQLite
// database.
func TestAttachViewStoreStaysKeyedByLaunchRoots(t *testing.T) {
	rootA, rootB := t.TempDir(), t.TempDir()
	launch := t.TempDir()
	scriptedAttach(t, launch, []string{rootA, rootB})

	launchDir := session.StateDirForRoots([]string{launch})
	daemonDir := session.StateDirForRoots([]string{rootA, rootB})
	if launchDir == daemonDir {
		t.Fatal("fixture: the launch and daemon state dirs collide")
	}
	if _, err := os.Stat(filepath.Join(launchDir, "state.db")); err != nil {
		t.Errorf("the store is not at the launch-root state dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(daemonDir, "state.db")); err == nil {
		t.Error("the client opened the daemon's state database")
	}
}

// An old server reports only Root and no root set, so the client keeps the
// launch-root workspace exactly as before. The failure mode: adopting an empty
// set would root the explorer at nothing.
func TestAttachOldServerKeepsLaunchRoots(t *testing.T) {
	launch := t.TempDir()
	a := scriptedAttach(t, launch, nil)

	if got := a.visible.All(); !sameStrings(got, []string{launch}) {
		t.Errorf("visible roots = %q, want the launch root %q", got, launch)
	}
	if got := a.Explorer.Tree.Roots; !sameStrings(got, []string{launch}) {
		t.Errorf("explorer roots = %q, want the launch root %q", got, launch)
	}
}

// recordingConn is the decision connection a forwarded local edit talks to. It
// records the verbs, answers a claim with any scripted overlap and a who list,
// and advances the version on apply, so a test can assert the span, the base and
// the adopted version without a real daemon.
type recordingConn struct {
	mu       sync.Mutex
	applied  []control.Request
	version  uint64
	overlaps []control.ClaimOverlap
	who      []control.Participant
}

func (r *recordingConn) Do(req control.Request) (control.Response, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch req.Op {
	case "claim":
		return control.Response{OK: true, ClaimOverlaps: r.overlaps}, nil
	case "who":
		return control.Response{OK: true, Participants: r.who}, nil
	case "version":
		return control.Response{OK: true, Version: r.version}, nil
	case "apply":
		r.applied = append(r.applied, req)
		r.version++
		return control.Response{OK: true, Version: r.version}, nil
	default:
		return control.Response{OK: true}, nil
	}
}

func (r *recordingConn) ResolveRoots(string) (control.Mapper, error) { return control.Mapper{}, nil }
func (r *recordingConn) Roots() []string                             { return nil }
func (r *recordingConn) Close() error                                { return nil }

func (r *recordingConn) lastApply() (control.Request, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.applied) == 0 {
		return control.Request{}, false
	}
	return r.applied[len(r.applied)-1], true
}

// useDecide swaps the client's decision connection for a scripted one, so a
// forward a test triggers does not reach the real daemon.
func useDecide(t *testing.T, ch *clientHarness, c clientConn) {
	t.Helper()
	ch.cli.clientConnMu.Lock()
	old := ch.cli.clientDecide
	ch.cli.clientDecide = c
	ch.cli.clientConnMu.Unlock()
	if old != nil {
		_ = old.Close()
	}
}

// fastClientForward shortens the forward debounce for one test. It is restored
// on cleanup, after the test has waited for forwarding to settle.
func fastClientForward(t *testing.T) {
	t.Helper()
	prev := clientForwardDebounce
	clientForwardDebounce = time.Millisecond
	t.Cleanup(func() { clientForwardDebounce = prev })
}

// waitClientIdle blocks until no forward is pending or running for path, so a
// test does not tear down the host under a live forward goroutine.
func waitClientIdle(t *testing.T, a *App, path string) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		if !a.clientEditBusy(path) {
			return
		}
		select {
		case <-deadline:
			t.Fatal("forwarding did not settle")
		default:
		}
		time.Sleep(time.Millisecond)
	}
}

// Review is the only read-only state: a client that leaves it is a normal
// editor, so a typed rune lands locally (and is forwarded) instead of being
// refused. The precondition is asserted before the action so a passing test
// cannot be the wrong mode.
func TestAttachedClientEditsOutsideReview(t *testing.T) {
	ch := newClientHarness(t)
	ch.cli.drain()
	fastClientForward(t)
	p := ch.cli.Tabs.Active()
	if p == nil {
		t.Fatal("client attached with no tab")
	}
	path := p.File.Path
	useDecide(t, ch, &recordingConn{})

	if ch.cli.mode != ModeReview {
		t.Fatalf("mode = %v, want Review", ch.cli.mode)
	}
	ch.cli.focusEditor()
	before := p.File.Text()
	ch.cli.typeText("x")
	if p.File.Text() != before {
		t.Errorf("Review accepted an edit: %q", p.File.Text())
	}
	if ch.cli.status == "" {
		t.Error("a Review refusal set no status")
	}

	ch.cli.mode = ModeEdit
	ch.cli.focusEditor()
	p.Cursors.Set(0, 0)
	ch.cli.typeText("x")
	if !strings.Contains(p.File.Text(), "x") {
		t.Errorf("an editable client refused a typed rune: %q", p.File.Text())
	}
	waitClientIdle(t, ch.cli.App, path)
}

// A local edit forwards exactly one apply on the decision connection, against
// the version the pane was synced from and with the changed span alone. The
// reply's version is adopted as the new base so the next edit rebases on it.
func TestClientLocalEditForwardsOneApply(t *testing.T) {
	ch := newClientHarness(t)
	ch.cli.drain()
	fastClientForward(t)
	p := ch.cli.Tabs.Active()
	if p == nil {
		t.Fatal("client attached with no tab")
	}
	path := p.File.Path

	ch.cli.clientMu.Lock()
	base := ch.cli.clientEdits[path].version
	ch.cli.clientMu.Unlock()

	conn := &recordingConn{version: base}
	useDecide(t, ch, conn)

	ch.cli.mode = ModeEdit
	ch.cli.focusEditor()
	p.Cursors.Set(0, 0)
	ch.cli.typeText("X")

	deadline := time.After(3 * time.Second)
	var req control.Request
	for {
		if r, ok := conn.lastApply(); ok {
			req = r
			break
		}
		select {
		case <-deadline:
			t.Fatal("no apply was forwarded for the local edit")
		default:
		}
		time.Sleep(time.Millisecond)
	}
	if req.Base == nil || *req.Base != base {
		t.Errorf("apply base = %v, want %d", req.Base, base)
	}
	if len(req.Hunks) != 1 {
		t.Fatalf("hunks = %+v, want one", req.Hunks)
	}
	if h := req.Hunks[0]; h.Start != 0 || h.End != 0 || h.Text != "X" {
		t.Errorf("hunk = %+v, want an insert of X at 0", h)
	}
	// Wait for the adopted version, which the forward records off-thread.
	deadline = time.After(3 * time.Second)
	for {
		ch.cli.clientMu.Lock()
		got := ch.cli.clientEdits[path].version
		ch.cli.clientMu.Unlock()
		if got > base {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("adopted version = %d, want past the base %d", got, base)
		default:
		}
		time.Sleep(time.Millisecond)
	}
	waitClientIdle(t, ch.cli.App, path)
}

// A watch snapshot must not replace a pane whose local edit has not been
// acknowledged; once the edit is forwarded and the pane is clean the same
// snapshot installs.
func TestClientWatchSkipsDirtyPane(t *testing.T) {
	ch := newClientHarness(t)
	ch.cli.drain()
	p := ch.cli.Tabs.Active()
	if p == nil {
		t.Fatal("client attached with no tab")
	}
	path := p.File.Path
	before := p.File.Text()

	ch.cli.clientMu.Lock()
	ch.cli.clientEdits[path].dirty = true
	ch.cli.clientMu.Unlock()

	if got := ch.cli.installClientFile(clientFile{path: path, file: editor.NewFile(path, "replaced\n", 4)}); got != nil {
		t.Error("a dirty pane was replaced by a daemon snapshot")
	}
	if p.File.Text() != before {
		t.Errorf("dirty pane text = %q, want unchanged", p.File.Text())
	}

	ch.cli.clientMu.Lock()
	ch.cli.clientEdits[path].dirty = false
	ch.cli.clientMu.Unlock()

	if got := ch.cli.installClientFile(clientFile{path: path, file: editor.NewFile(path, "replaced\n", 4)}); got == nil {
		t.Error("a clean pane was not replaced by the daemon snapshot")
	}
	if p.File.Text() != "replaced\n" {
		t.Errorf("clean pane text = %q, want the snapshot text", p.File.Text())
	}
}

// A claim overlap with another writer who is an agent becomes the short status
// warning. The kind comes from the who list, not the author id, so a second
// human above the agent base is not misreported.
func TestClientClaimOverlapNotesAnAgent(t *testing.T) {
	ch := newClientHarness(t)
	ch.cli.drain()
	p := ch.cli.Tabs.Active()
	if p == nil {
		t.Fatal("client attached with no tab")
	}
	path := p.File.Path

	conn := &recordingConn{
		overlaps: []control.ClaimOverlap{{Path: path, Identity: "claude", Author: 9}},
		who:      []control.Participant{{ID: 9, Identity: "claude", Name: "claude", Kind: control.KindAgent}},
	}
	useDecide(t, ch, conn)

	text := p.File.Text()
	ch.cli.forwardApply(path, 1, text, text)

	ch.cli.clientMu.Lock()
	note := ch.cli.clientNote
	ch.cli.clientMu.Unlock()
	if !strings.Contains(note, "agent claude has claimed") {
		t.Errorf("note = %q, want the agent overlap warning", note)
	}
}
