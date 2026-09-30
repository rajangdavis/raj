package app

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
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

// pumpUntilAdopted pumps both event loops until the client shows path carrying
// its host proposal. The tab alone is not enough: with the immediate generation
// bump the mirror can show the tab from the daemon open before the apply's
// snapshot lands, so a caller that inspects the pending set would race it.
func pumpUntilAdopted(t *testing.T, ch *clientHarness, path string) *editor.Pane {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		for _, p := range ch.cli.Tabs.All() {
			if p.File.Path == path && len(p.File.Session().Pending()) > 0 {
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
	if got := ch.cli.Tabs.Count(); got != 0 {
		t.Fatalf("setup: client tabs = %d, want a clean daemon to mirror nothing", got)
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
	srv.typeText("X") // unsaved work is what makes the tab mirror, so there is one to close
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

// pumpUntilClientTab pumps both event loops until the client shows path. It is
// the tab-level sibling of pumpUntilAdopted: a reveal has no pending set, so
// the assertion is membership plus the caret.
func pumpUntilClientTab(t *testing.T, ch *clientHarness, path string) *editor.Pane {
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
			t.Fatalf("the client never mounted %s; tabs = %+v", path, ch.cli.Tabs.All())
		default:
		}
		ch.srv.drain()
		ch.cli.drain()
		time.Sleep(time.Millisecond)
	}
}

// A reveal of a path the client does not show makes the client mount it at the
// span, riding the parked watch with no reconnect in between, and joins the
// owned set so it persists. Without the broadcast the phone never sees it.
func TestClientRevealMountsTabWithoutReconnect(t *testing.T) {
	srv := controlHarness(t, "one\ntwo\nthree\n")
	ch := attachClientAt(t, srv, Options{Phone: true}, 120, 30)
	ch.cli.drain()

	// Count re-dials after the attach. A reveal must ride the live watch, so
	// this stays zero; a reconnect would increment it.
	var dials atomic.Int32
	setClientDial(t, func(addr string) (clientConn, error) {
		dials.Add(1)
		return control.Dial(addr)
	})

	dir := filepath.Dir(srv.Tabs.Active().File.Path)
	path := filepath.Join(dir, "surfaced.go")
	if err := os.WriteFile(path, []byte("alpha\nbeta\ngamma\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, e := 6, 10
	c := srv.dial(t)
	if r := c.do(srv, control.Request{Op: "reveal", Path: path, Start: &s, End: &e}); !r.OK {
		t.Fatalf("reveal = %+v", r)
	}
	pane := pumpUntilClientTab(t, ch, path)
	if got := dials.Load(); got != 0 {
		t.Errorf("the client reconnected %d time(s); a reveal must use the parked watch", got)
	}
	if got := pane.Cursors.Primary().Head; got != s {
		t.Errorf("client caret = %d, want the span start %d", got, s)
	}
	ch.cli.clientTabMu.Lock()
	owned := ch.cli.clientOwned[path]
	ch.cli.clientTabMu.Unlock()
	if !owned {
		t.Error("the revealed path did not join the owned set")
	}
}

// A reveal of a path the client already shows moves the client caret to the span
// start; the tab is re-pointed rather than remounted, and no reconnect happens
// either.
func TestClientRevealMovesCaretOnShownPath(t *testing.T) {
	srv := controlHarness(t, "one\ntwo\nthree\n")
	srv.typeText("X") // unsaved work is what makes the daemon tab mirror
	ch := attachClientAt(t, srv, Options{Phone: true}, 120, 30)
	ch.cli.drain()
	path := srv.Tabs.Active().File.Path
	pane := pumpUntilClientTab(t, ch, path)

	// Park the client caret at the top of the last line, so a reveal that does
	// nothing is distinguishable from one that moves it.
	off := pane.File.OffsetAt(2, 0)
	pane.Cursors.Set(off, off)

	s, e := 4, 7
	c := srv.dial(t)
	if r := c.do(srv, control.Request{Op: "reveal", Path: path, Start: &s, End: &e}); !r.OK {
		t.Fatalf("reveal = %+v", r)
	}
	deadline := time.After(3 * time.Second)
	for pane.Cursors.Primary().Head != s {
		select {
		case <-deadline:
			t.Fatalf("client caret = %d, want %d", pane.Cursors.Primary().Head, s)
		default:
		}
		srv.drain()
		ch.cli.drain()
		time.Sleep(time.Millisecond)
	}
}

// A reveal reaches every attached client, not just the one that asked: each
// parked watch is woken and carries the same path and span. Without the
// per-watcher reveal history only the first client to run would see it.
func TestClientRevealReachesEveryAttachedClient(t *testing.T) {
	srv := controlHarness(t, "one\ntwo\nthree\n")
	phone := attachClientAt(t, srv, Options{Phone: true}, 120, 30)
	laptop := attachClientAt(t, srv, Options{Name: "laptop"}, 120, 12)
	phone.cli.drain()
	laptop.cli.drain()

	dir := filepath.Dir(srv.Tabs.Active().File.Path)
	path := filepath.Join(dir, "both.go")
	if err := os.WriteFile(path, []byte("alpha\nbeta\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, e := 6, 10
	c := srv.dial(t)
	if r := c.do(srv, control.Request{Op: "reveal", Path: path, Start: &s, End: &e}); !r.OK {
		t.Fatalf("reveal = %+v", r)
	}
	for _, ch := range []*clientHarness{phone, laptop} {
		pane := pumpUntilClientTab(t, ch, path)
		if got := pane.Cursors.Primary().Head; got != s {
			t.Errorf("client caret = %d, want %d", got, s)
		}
	}
}

// A human delete on the attached client removes the daemon's file into the
// daemon's workspace trash. The client sends propose and approve back to back
// over the decision connection, which is the one removal path; without that the
// client would record a local proposal and the daemon would refuse the approve
// as not pending.
func TestClientDeleteForwardsToDaemonAndTrashes(t *testing.T) {
	srv := controlHarness(t, "hello\n")
	ch := attachClientAt(t, srv, Options{}, 120, 12)
	ch.cli.drain()
	path := srv.Tabs.Active().File.Path

	runClient(t, ch, func() { ch.cli.OpenFile(path) })
	ch.cli.drain()
	pumpUntilClientTab(t, ch, path)

	runClient(t, ch, func() { ch.cli.deletePath(path) })
	if !ch.cli.Prompt.Open {
		t.Fatal("the client's delete opened no confirm")
	}
	if got := ch.cli.Prompt.Selected(); got != deleteNow {
		t.Fatalf("client answer = %q, want %q", got, deleteNow)
	}
	runClient(t, ch, func() { ch.cli.press("enter") })

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("daemon file still on disk after the client's delete (err=%v)", err)
	}
	entries, err := os.ReadDir(srv.trashDir())
	if err != nil {
		t.Fatalf("reading the daemon trash: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("daemon trash holds %d entr(ies), want 1: %+v", len(entries), entries)
	}
	if got := srv.App.Deletions(); len(got) != 0 {
		t.Errorf("daemon pending deletions = %+v, want the decision carried out", got)
	}
}

// An attached rename moves the file on the daemon, not on the client's machine:
// the client sends the existing rename command over the decision connection.
// Without renameRemote the client ran host.Rename against its own root, where
// the daemon path does not exist.
func TestClientRenameMovesTheDaemonFile(t *testing.T) {
	srv := controlHarness(t, "hello\n")
	ch := attachClientAt(t, srv, Options{}, 120, 12)
	ch.cli.drain()
	path := srv.Tabs.Active().File.Path

	runClient(t, ch, func() { ch.cli.OpenFile(path) })
	ch.cli.drain()
	pumpUntilClientTab(t, ch, path)

	next := filepath.Join(filepath.Dir(path), "renamed.go")
	runClient(t, ch, func() { ch.cli.renameRemote(path, next) })

	if _, err := os.Stat(next); err != nil {
		t.Errorf("the daemon rename did not move the file: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the old daemon path survived the rename (err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(ch.cli.primaryRoot(), "renamed.go")); !os.IsNotExist(err) {
		t.Errorf("the rename landed on the client's machine (err=%v)", err)
	}
}

// hostProposeDeletion lands a pending deletion on the daemon through the
// socket, claim-gated the way an agent's delete is.
func hostProposeDeletion(t *testing.T, srv *harness, path string) {
	t.Helper()
	c := srv.dial(t)
	if r := c.roundtrip(srv, control.Request{Op: "claim", Paths: []string{path}}); !r.OK {
		t.Fatalf("claim %q: %+v", path, r)
	}
	if r := c.do(srv, control.Request{Op: "delete", Path: path}); !r.OK {
		t.Fatalf("delete %q: %+v", path, r)
	}
}

// hostProposeDirRemoval is hostProposeDeletion for a directory.
func hostProposeDirRemoval(t *testing.T, srv *harness, dir string) {
	t.Helper()
	c := srv.dial(t)
	if r := c.roundtrip(srv, control.Request{Op: "claim", Paths: []string{dir}}); !r.OK {
		t.Fatalf("claim %q: %+v", dir, r)
	}
	if r := c.do(srv, control.Request{Op: "rmdir", Path: dir}); !r.OK {
		t.Fatalf("rmdir %q: %+v", dir, r)
	}
}

// pumpUntilDeletion pumps both loops until the client mirrors path as a pending
// deletion.
func pumpUntilDeletion(t *testing.T, ch *clientHarness, path string) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		if _, ok := ch.cli.pendingDeletions[path]; ok {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("the client never mirrored the deletion of %s", path)
		default:
		}
		ch.srv.drain()
		ch.cli.drain()
		time.Sleep(time.Millisecond)
	}
}

// pumpUntilDirRemoval is pumpUntilDeletion for a pending dir-removal.
func pumpUntilDirRemoval(t *testing.T, ch *clientHarness, dir string) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		if _, ok := ch.cli.pendingDirRemovals[dir]; ok {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("the client never mirrored the dir-removal of %s", dir)
		default:
		}
		ch.srv.drain()
		ch.cli.drain()
		time.Sleep(time.Millisecond)
	}
}

// A pending deletion on the daemon is mirrored to the client: after a watch
// cycle the pending map holds the path and the removal note names it. Without
// the mirror adoptPending drops the delete proposal and the viewer can neither
// see nor answer it.
func TestClientMirrorsPendingDeletion(t *testing.T) {
	srv := controlHarness(t, "hello\n")
	ch := attachClientAt(t, srv, Options{}, 120, 12)
	ch.cli.drain()
	path := srv.Tabs.Active().File.Path
	if _, ok := ch.cli.pendingDeletions[path]; ok {
		t.Fatal("setup: the client already holds a deletion")
	}
	hostProposeDeletion(t, srv, path)
	srv.Handle(ui.Tick{})
	pumpUntilDeletion(t, ch, path)
	if got := ch.cli.waitingNote(); !strings.Contains(got, "1 waiting for you") {
		t.Errorf("note = %q, want the mirrored deletion named", got)
	}
}

// The rmdir twin: a pending directory removal is mirrored too, so the note and
// the re-raise chord can reach it on the viewer.
func TestClientMirrorsPendingDirRemoval(t *testing.T) {
	srv := controlHarness(t, "hello\n")
	ch := attachClientAt(t, srv, Options{}, 120, 12)
	ch.cli.drain()
	dir := mkTree(t, srv)
	if _, ok := ch.cli.pendingDirRemovals[dir]; ok {
		t.Fatal("setup: the client already holds a dir-removal")
	}
	hostProposeDirRemoval(t, srv, dir)
	srv.Handle(ui.Tick{})
	pumpUntilDirRemoval(t, ch, dir)
	if got := ch.cli.waitingNote(); !strings.Contains(got, "1 waiting for you") {
		t.Errorf("note = %q, want the mirrored dir-removal named", got)
	}
}

// Two watch cycles over the same undecided proposal enqueue it once: the
// mirror checks the map before enqueuing and the arrival queue is keyed by
// (path, dir), so a viewer cannot accumulate duplicates.
func TestClientMirrorRemovalIsIdempotent(t *testing.T) {
	srv := controlHarness(t, "hello\n")
	ch := attachClientAt(t, srv, Options{}, 120, 12)
	ch.cli.drain()
	path := srv.Tabs.Active().File.Path
	hostProposeDeletion(t, srv, path)
	srv.Handle(ui.Tick{})
	pumpUntilDeletion(t, ch, path)

	srv.Handle(ui.Tick{})
	pumpFor(ch, 200*time.Millisecond)
	if n := len(ch.cli.pendingDeletions); n != 1 {
		t.Errorf("pending deletions after two cycles = %d, want 1", n)
	}
	if n := ch.cli.waitingCount(); n != 1 {
		t.Errorf("waiting count after two cycles = %d, want 1", n)
	}
}

// The client's Remove forever answer forwards delete --approve to the daemon,
// which is the one removal path: the daemon's file leaves disk and its pending
// entry clears, while the client's own pane survives. The surviving pane is the
// proof the client did not unlink locally -- the local path would have closed
// it.
func TestClientApproveForwardsRemovalToDaemon(t *testing.T) {
	t.Setenv("RAJ_TRASH", "")
	srv := controlHarness(t, "hello\n")
	ch := attachClientAt(t, srv, Options{}, 120, 12)
	ch.cli.drain()
	path := srv.Tabs.Active().File.Path

	// The viewer opens the daemon file, so it has a pane the gate can raise
	// the question against.
	runClient(t, ch, func() { ch.cli.OpenFile(path) })
	ch.cli.drain()
	pumpUntilClientTab(t, ch, path)

	hostProposeDeletion(t, srv, path)
	srv.Handle(ui.Tick{})
	pumpUntilDeletion(t, ch, path)

	if !ch.cli.Prompt.Open {
		t.Fatalf("the mirrored deletion did not raise the gate on the open pane")
	}
	if got := ch.cli.Prompt.Title(); got != "Delete file" {
		t.Fatalf("prompt title = %q, want Delete file", got)
	}
	before := ch.cli.Tabs.Count()

	runClient(t, ch, func() { ch.cli.press("right", "enter") })

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("daemon file still on disk after approve (err=%v)", err)
	}
	if _, ok := ch.cli.pendingDeletions[path]; ok {
		t.Error("the client kept the pending deletion after the approve")
	}
	if !clientHasTab(ch, path) {
		t.Error("the client closed its own pane: the removal was local, not forwarded")
	}
	if got := ch.cli.Tabs.Count(); got != before {
		t.Errorf("client tab count = %d, want %d (no local close)", got, before)
	}
}

// Withdraw forwards delete --withdraw with no author: the daemon admits the
// client's own durable human, who may retract any pending removal, and the
// agent's proposal is retracted without touching the file. Finding 1: before
// the human rule the client forged the proposer's id, and without the author
// check in connection.one a peer connection could retract it.
func TestClientWithdrawForwardsToDaemon(t *testing.T) {
	srv := controlHarness(t, "hello\n")
	ch := attachClientAt(t, srv, Options{}, 120, 12)
	ch.cli.drain()
	path := srv.Tabs.Active().File.Path
	runClient(t, ch, func() { ch.cli.OpenFile(path) })
	ch.cli.drain()
	pumpUntilClientTab(t, ch, path)

	hostProposeDeletion(t, srv, path)
	srv.Handle(ui.Tick{})
	pumpUntilDeletion(t, ch, path)
	if !ch.cli.Prompt.Open {
		t.Fatalf("the mirrored deletion did not raise the gate")
	}

	// Ignore for now / Remove forever / Withdraw: two rights select Withdraw.
	runClient(t, ch, func() { ch.cli.press("right", "right", "enter") })

	if _, err := os.Stat(path); err != nil {
		t.Errorf("withdraw removed the daemon file: %v", err)
	}
	if got := srv.App.Deletions(); len(got) != 0 {
		t.Errorf("daemon pending deletions = %+v, want the proposal retracted", got)
	}
	if _, ok := ch.cli.pendingDeletions[path]; ok {
		t.Error("the client kept the pending deletion after the withdraw")
	}
}

// A mirrored dir-removal is answered by Remove forever on the client, which
// forwards rmdir --approve: the daemon removes the whole tree and the client's
// pending map and arrival queue clear. Finding 3: the delete twin was tested;
// without the rmdir forward the client's answer did nothing and the tree
// stayed.
func TestClientApproveDirRemovalForwardsToDaemon(t *testing.T) {
	t.Setenv("RAJ_TRASH", "")
	srv := controlHarness(t, "hello\n")
	ch := attachClientAt(t, srv, Options{}, 120, 12)
	ch.cli.drain()
	dir := mkTree(t, srv)

	hostProposeDirRemoval(t, srv, dir)
	srv.Handle(ui.Tick{})
	pumpUntilDirRemoval(t, ch, dir)

	// A directory has no pane to focus, so the mirrored proposal is answered
	// from the waiting list: ctrl+alt+v opens it and accept forwards the human
	// answer to the daemon.
	runClient(t, ch, func() { ch.cli.press("ctrl+alt+v") })
	if !ch.cli.Picker.Open {
		t.Fatalf("ctrl+alt+v did not open the waiting list on the client")
	}
	if got := ch.cli.Picker.Results(); got != 1 {
		t.Fatalf("waiting list rows = %d, want the mirrored dir-removal", got)
	}
	runClient(t, ch, func() { ch.cli.press("ctrl+super+m") })

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("daemon directory still on disk after approve (err=%v)", err)
	}
	if _, ok := ch.cli.pendingDirRemovals[dir]; ok {
		t.Error("the client kept the pending dir-removal after the approve")
	}
}

// A stale mirror is refused honestly: the client mirrors a deletion, the agent
// withdraws it on the daemon, and the client's later Remove forever is refused
// as not pending. The client keeps its entry rather than silently clearing it,
// so the pending surface still reflects something the daemon no longer holds.
// Finding 2, pinning the current behaviour.
func TestClientApproveOfAStaleMirrorIsRefused(t *testing.T) {
	srv := controlHarness(t, "hello\n")
	ch := attachClientAt(t, srv, Options{}, 120, 12)
	ch.cli.drain()
	path := srv.Tabs.Active().File.Path

	// The viewer opens the daemon file, so it has a pane the gate can raise
	// the question against.
	runClient(t, ch, func() { ch.cli.OpenFile(path) })
	ch.cli.drain()
	pumpUntilClientTab(t, ch, path)

	// Propose and keep the agent connection, so the withdraw below is by the
	// proposal's own author.
	c := srv.dial(t)
	if r := c.roundtrip(srv, control.Request{Op: "claim", Paths: []string{path}}); !r.OK {
		t.Fatalf("claim %q: %+v", path, r)
	}
	if r := c.do(srv, control.Request{Op: "delete", Path: path}); !r.OK {
		t.Fatalf("delete %q: %+v", path, r)
	}
	srv.Handle(ui.Tick{})
	pumpUntilDeletion(t, ch, path)
	if !ch.cli.Prompt.Open {
		t.Fatalf("the mirrored deletion did not raise the gate")
	}

	// Withdraw it on the daemon; the client's mirror is now stale.
	if r := c.do(srv, control.Request{Op: "delete", Path: path, Withdraw: true}); !r.OK {
		t.Fatalf("withdraw = %+v", r)
	}
	if got := srv.App.Deletions(); len(got) != 0 {
		t.Fatalf("setup: the daemon still holds the deletion: %+v", got)
	}

	// The client's Remove forever is refused as not pending, and the mirrored
	// entry stays rather than being cleared by a refusal.
	runClient(t, ch, func() { ch.cli.press("right", "enter") })
	if _, ok := ch.cli.pendingDeletions[path]; !ok {
		t.Error("the client dropped its stale mirror without a daemon OK")
	}
	if !strings.Contains(ch.cli.status, "no pending deletion") {
		t.Errorf("status = %q, want the daemon's not-pending refusal", ch.cli.status)
	}
}

// The attached client learns about a publish proposal too: mirrorRemoval stores
// the wave name so the waiting list can show it, and an accept would forward
// the decision to the daemon rather than running the pinned outward step
// locally.
func TestClientMirrorsPendingPublish(t *testing.T) {
	srv := controlHarness(t, "hello\n")
	ch := attachClientAt(t, srv, Options{}, 120, 12)
	ch.cli.drain()

	ch.cli.mirrorRemoval(control.Proposal{Kind: "publish", Path: "task-1", Author: 7})
	if _, ok := ch.cli.pendingPublishes["task-1"]; !ok {
		t.Fatal("the client did not mirror the publish proposal")
	}
	if got := ch.cli.waitingNote(); !strings.Contains(got, "1 waiting for you") {
		t.Errorf("note = %q, want the mirrored publish named", got)
	}
}
