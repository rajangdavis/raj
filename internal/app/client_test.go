package app

import (
	"strings"
	"testing"
	"time"

	"raj/internal/control"
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
