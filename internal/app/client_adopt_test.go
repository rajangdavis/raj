package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"raj/internal/control"
	"raj/internal/editor"
	"raj/internal/keys"
	"raj/internal/ui"
)

// hostPropose opens a fresh path on the host and lands a proposed change set in
// it, returning the path.
func hostPropose(t *testing.T, srv *harness) string {
	t.Helper()
	dir := filepath.Dir(srv.Tabs.Active().File.Path)
	path := filepath.Join(dir, "agent.go")
	if err := os.WriteFile(path, []byte("package agent\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := srv.dial(t)
	if r := c.do(srv, control.Request{Op: "open", Path: path}); !r.OK {
		t.Fatalf("open = %+v", r)
	}
	base := c.do(srv, control.Request{Op: "text", Path: path}).Version
	if r := c.do(srv, control.Request{Op: "apply", Path: path, Base: &base,
		Hunks: []control.Hunk{{Start: 0, End: 0, Text: "// x\n"}}}); !r.OK {
		t.Fatalf("apply = %+v", r)
	}
	return path
}

// pumpUntilAdopted pumps both event loops until the client shows path.
func pumpUntilAdopted(t *testing.T, ch *clientHarness, path string) *editor.Pane {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		for _, p := range ch.cli.Tabs.All() {
			if p.File.Path == path {
				return p
			}
		}
		select {
		case <-deadline:
			t.Fatalf("the client never adopted %s; tabs = %+v", path, ch.cli.Tabs.All())
		default:
		}
		ch.srv.drain()
		ch.cli.drain()
		time.Sleep(time.Millisecond)
	}
}

func clientHasTab(ch *clientHarness, path string) bool {
	for _, p := range ch.cli.Tabs.All() {
		if p.File.Path == path {
			return true
		}
	}
	return false
}

// pumpFor drains both loops for a fixed span, for a test that asserts something
// does not happen.
func pumpFor(ch *clientHarness, d time.Duration) {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		ch.srv.drain()
		ch.cli.drain()
		time.Sleep(time.Millisecond)
	}
}

// A host buffer with a pending set that the client never opened becomes a phone
// tab, carrying the host text and the pending set; its review controls show.
// Without adoption the client only syncs its own tabs and the proposal stays
// invisible.
func TestClientAdoptsHostProposal(t *testing.T) {
	srv := controlHarness(t, "hello\n")
	ch := attachClientAt(t, srv, Options{Phone: true}, 120, 30)
	ch.cli.drain()
	if got := ch.cli.Tabs.Count(); got != 1 {
		t.Fatalf("setup: client tabs = %d, want the mirrored tab", got)
	}

	path := hostPropose(t, srv)
	srv.Handle(ui.Tick{})
	pane := pumpUntilAdopted(t, ch, path)
	if !strings.Contains(pane.File.Text(), "// x") {
		t.Errorf("adopted text = %q, want the host version", pane.File.Text())
	}
	if pending := pane.File.Session().Pending(); len(pending) == 0 {
		t.Error("the adopted tab has no pending set")
	}
	// Make the adopted tab active, draw so the handle has a row, then open the
	// drawer: the review gate reads the active pane pending count, and the
	// panel only exists while the drawer is open.
	ch.cli.Tabs.Focus(pane)
	ch.cli.Draw()
	openPhoneDrawer(ch.cli)
	found := false
	for _, b := range ch.cli.drawerPanel {
		if b.action == keys.AcceptProposed {
			found = true
		}
	}
	if !found {
		t.Errorf("review controls not shown for the adopted tab: %+v", ch.cli.drawerPanel)
	}
}

// A path the user closed stays closed while the daemon buffer is unchanged,
// and comes back once a new proposal moves it: a close is a snooze, not a
// permanent mute. Without the snooze the later proposal would never show up.
func TestClientClosedPathSnoozesUntilItChanges(t *testing.T) {
	srv := controlHarness(t, "hello\n")
	ch := attachClientAt(t, srv, Options{Phone: true}, 120, 30)
	ch.cli.drain()
	path := srv.Tabs.Active().File.Path
	ch.cli.closeTabAt(ch.cli.Tabs.Index())
	if got := ch.cli.Tabs.Count(); got != 0 {
		t.Fatalf("tabs after close = %d, want 0", got)
	}
	// Nothing has moved, so a wake must not bring it back.
	srv.Handle(ui.Tick{})
	pumpFor(ch, 200*time.Millisecond)
	if got := ch.cli.Tabs.Count(); got != 0 {
		t.Fatalf("a closed path came back with nothing changed: %+v", ch.cli.Tabs.All())
	}
	if !ch.cli.clientTabClosed(path) {
		t.Fatal("the closed mark was lost")
	}

	// A new proposal moves the daemon facts, so the snooze is spent and the
	// path is re-adopted.
	c := srv.dial(t)
	base := c.do(srv, control.Request{Op: "text"}).Version
	if r := c.do(srv, control.Request{Op: "apply", Base: &base,
		Hunks: []control.Hunk{{Start: 0, End: 0, Text: "// x\n"}}}); !r.OK {
		t.Fatalf("apply = %+v", r)
	}
	// A proposal only wakes a parked watch when the daemon's idle tick advances
	// its generation, so deliver the tick the other adoption tests do.
	srv.Handle(ui.Tick{})
	pane := pumpUntilAdopted(t, ch, path)
	if !strings.Contains(pane.File.Text(), "// x") {
		t.Errorf("re-adopted text = %q, want the new proposal", pane.File.Text())
	}
}

// Deciding the set leaves the adopted tab in place: it becomes a normal phone
// tab rather than vanishing on the decision.
func TestAdoptedTabStaysAfterDecision(t *testing.T) {
	srv := controlHarness(t, "hello\n")
	ch := attachClientAt(t, srv, Options{Phone: true}, 120, 30)
	ch.cli.drain()
	path := hostPropose(t, srv)
	srv.Handle(ui.Tick{})
	pane := pumpUntilAdopted(t, ch, path)

	ch.cli.Tabs.Focus(pane)
	off := ch.cli.Pane().File.OffsetAt(0, 0)
	ch.cli.Pane().Cursors.Set(off, off)
	runClient(t, ch, func() { ch.cli.reviewProposed(true) })
	ch.cli.drain()
	if !clientHasTab(ch, path) {
		t.Fatalf("the adopted tab vanished after the decision: %+v", ch.cli.Tabs.All())
	}
	for _, p := range ch.cli.Tabs.All() {
		if p.File.Path == path && len(p.File.Session().Pending()) != 0 {
			t.Errorf("the set is still pending after the accept")
		}
	}
	srv.Handle(ui.Tick{})
	pumpFor(ch, 100*time.Millisecond)
	if !clientHasTab(ch, path) {
		t.Errorf("the adopted tab vanished on a later reconcile")
	}
}

// An adopted path keeps syncing: a later host edit reaches the adopted tab.
func TestAdoptedTabKeepsSyncing(t *testing.T) {
	srv := controlHarness(t, "hello\n")
	ch := attachClientAt(t, srv, Options{Phone: true}, 120, 30)
	ch.cli.drain()
	path := hostPropose(t, srv)
	srv.Handle(ui.Tick{})
	pane := pumpUntilAdopted(t, ch, path)

	for _, p := range srv.Tabs.All() {
		if p.File.Path == path {
			srv.Tabs.Focus(p)
		}
	}
	srv.typeText("Z") // edit the adopted host buffer
	srv.Handle(ui.Tick{})
	deadline := time.After(3 * time.Second)
	for !strings.Contains(pane.File.Text(), "Z") {
		select {
		case <-deadline:
			t.Fatalf("the adopted tab did not sync; text = %q", pane.File.Text())
		default:
		}
		srv.drain()
		ch.cli.drain()
		time.Sleep(time.Millisecond)
	}
}

// A plain attach adopts a host proposal in a file it never opened: mirroring
// the daemon is what attach means, with no flag. Without the unconditional
// adoption the proposal would stay invisible on the default client.
func TestPlainAttachAdoptsHostProposal(t *testing.T) {
	srv := controlHarness(t, "hello\n")
	ch := attachClientAt(t, srv, Options{}, 120, 30)
	ch.cli.drain()
	path := hostPropose(t, srv)
	srv.Handle(ui.Tick{})
	pane := pumpUntilAdopted(t, ch, path)
	if pending := pane.File.Session().Pending(); len(pending) == 0 {
		t.Error("the adopted tab has no pending set")
	}
}
