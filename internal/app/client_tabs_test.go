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

// headlessListed reports whether the daemon has path in its headless registry,
// which is what a read of a file nobody opened produces.
func headlessListed(srv *harness, path string) bool {
	for _, p := range srv.headless {
		if p.File.Path == path {
			return true
		}
	}
	return false
}

// The client shows the daemon real tabs, not the buffers an agent loaded to
// read. Without the Headless filter StartClient gives a tab to every buffer in
// the reply, and a phone shows files nobody has open in the workspace.
func TestClientSkipsHeadlessBuffersOnAttach(t *testing.T) {
	srv := controlHarness(t, "hello\n")
	dir := filepath.Dir(srv.Tabs.Active().File.Path)
	probe := filepath.Join(dir, "probe.go")
	if err := os.WriteFile(probe, []byte("package probe\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A driver read loads probe.go headlessly: addressable, but not a tab.
	c := srv.dial(t)
	if r := c.do(srv, control.Request{Op: "text", Path: probe}); !r.OK {
		t.Fatalf("read probe = %+v", r)
	}
	if !headlessListed(srv, probe) {
		t.Fatal("setup: the probe was not registered headless")
	}

	ch := attachClient(t, srv)
	ch.cli.drain()
	if got := ch.cli.Tabs.Count(); got != 1 {
		t.Fatalf("client tabs = %d, want only the daemon real tab", got)
	}
	if p := ch.cli.Tabs.Active(); p == nil || p.File.Path != srv.Tabs.Active().File.Path {
		t.Fatalf("client active = %+v, want the daemon tab", p)
	}
}

// A headless buffer that appears on a watch wake does not become a tab, even
// when the same wake re-fetches a real tab. Without the filter the reconcile
// would add the agent-read file to the client.
func TestClientWatchIgnoresHeadlessBuffers(t *testing.T) {
	ch := newClientHarness(t)
	ch.cli.drain()
	if got := ch.cli.Tabs.Count(); got != 1 {
		t.Fatalf("client tabs = %d, want 1", got)
	}
	dir := filepath.Dir(ch.srv.Tabs.Active().File.Path)
	probe := filepath.Join(dir, "probe.go")
	if err := os.WriteFile(probe, []byte("package probe\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := ch.srv.dial(t)
	if r := c.do(ch.srv, control.Request{Op: "text", Path: probe}); !r.OK {
		t.Fatalf("read probe = %+v", r)
	}
	// A real edit rides the same wake, so the loop is proven to have run its
	// reconcile with the headless buffer in the answer.
	ch.srv.typeText("Z")
	ch.srv.Handle(ui.Tick{})

	p := ch.cli.Tabs.Active()
	deadline := time.After(3 * time.Second)
	for !strings.Contains(p.File.Text(), "Z") {
		select {
		case <-deadline:
			t.Fatalf("client never saw the wake; text = %q", p.File.Text())
		default:
		}
		ch.srv.drain()
		ch.cli.drain()
		time.Sleep(time.Millisecond)
	}
	if got := ch.cli.Tabs.Count(); got != 1 {
		t.Errorf("client tabs = %d after a headless buffer appeared, want 1", got)
	}
}

// A tab the daemon closes is closed on the client too. Without the removal
// reconcile the client keeps showing a document the daemon no longer has open.
func TestClientWatchClosesRemovedTab(t *testing.T) {
	srv := controlHarness(t, "hello\n")
	dir := filepath.Dir(srv.Tabs.Active().File.Path)
	second := filepath.Join(dir, "second.go")
	if err := os.WriteFile(second, []byte("package second\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := srv.dial(t)
	if r := c.do(srv, control.Request{Op: "open", Path: second}); !r.OK {
		t.Fatalf("open = %+v", r)
	}
	ch := attachClient(t, srv)
	ch.cli.drain()
	if got := ch.cli.Tabs.Count(); got != 2 {
		t.Fatalf("client tabs = %d, want 2", got)
	}
	// Close the clean second tab on the daemon: the file stays, the tab goes.
	if r := c.do(srv, control.Request{Op: "close", Path: second}); !r.OK {
		t.Fatalf("close = %+v", r)
	}
	srv.Handle(ui.Tick{})

	deadline := time.After(3 * time.Second)
	for ch.cli.Tabs.Count() != 1 {
		select {
		case <-deadline:
			t.Fatalf("client tabs = %d, want 1 after the daemon closed one", ch.cli.Tabs.Count())
		default:
		}
		srv.drain()
		ch.cli.drain()
		time.Sleep(time.Millisecond)
	}
	for _, p := range ch.cli.Tabs.All() {
		if p.File.Path == second {
			t.Errorf("client kept a tab for the closed path %s", second)
		}
	}
}

// A daemon tab opened after the client attached appears without a relaunch:
// the reconcile mirrors every daemon real tab the client does not already show.
// Without the continuous mirror a file opened on the laptop after the attach
// stayed invisible on the phone.
func TestClientWatchAddsDaemonTabOpenedAfterAttach(t *testing.T) {
	srv := controlHarness(t, "hello\n")
	ch := attachClient(t, srv)
	ch.cli.drain()
	if got := ch.cli.Tabs.Count(); got != 1 {
		t.Fatalf("setup: client tabs = %d, want 1", got)
	}
	dir := filepath.Dir(srv.Tabs.Active().File.Path)
	second := filepath.Join(dir, "second.go")
	if err := os.WriteFile(second, []byte("package second\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := srv.dial(t)
	if r := c.do(srv, control.Request{Op: "open", Path: second}); !r.OK {
		t.Fatalf("open = %+v", r)
	}
	// Precondition: the daemon really shows the new tab before the client is
	// asked to converge on it.
	found := false
	for _, p := range srv.Tabs.All() {
		if p.File.Path == second {
			found = true
		}
	}
	if !found {
		t.Fatal("setup: the daemon has no tab for the new path")
	}
	srv.Handle(ui.Tick{})

	deadline := time.After(3 * time.Second)
	for !clientHasTab(ch, second) {
		select {
		case <-deadline:
			t.Fatalf("the client never mirrored %s; tabs = %+v", second, ch.cli.Tabs.All())
		default:
		}
		srv.drain()
		ch.cli.drain()
		time.Sleep(time.Millisecond)
	}
}

// Save in client mode acts on the daemon: the wire save writes the file and the
// client refetches. Without the proxy the snapshot File refuses with
// ErrSnapshotReadOnly, so the phone save does nothing.
func TestClientSaveWritesOnTheDaemon(t *testing.T) {
	srv := controlHarness(t, "hello\n")
	// A local edit leaves the daemon dirty with no pending set, which is the
	// state a wire save writes.
	srv.typeText("X")
	ch := attachClient(t, srv)
	ch.cli.drain()
	p := ch.cli.Tabs.Active()
	if p == nil {
		t.Fatal("client attached with no tab")
	}
	if !strings.Contains(p.File.Text(), "X") {
		t.Fatalf("client text = %q, want the daemon edit", p.File.Text())
	}
	before := p.File

	runClient(t, ch, func() { ch.cli.saveActive(nil) })

	if ch.srv.Pane().File.Dirty() {
		t.Error("daemon buffer is still dirty after the client save")
	}
	onDisk, err := os.ReadFile(ch.srv.Pane().File.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(onDisk) != ch.srv.Pane().File.Text() {
		t.Errorf("disk = %q, want %q", onDisk, ch.srv.Pane().File.Text())
	}
	if p.File == before {
		t.Error("client did not refetch after the save")
	}
	if !strings.Contains(ch.cli.status, "saved") {
		t.Errorf("status = %q, want a save confirmation", ch.cli.status)
	}
	ch.cli.drain()
}

// A host refusal to save is the status, and the local snapshot is untouched.
// The daemon refuses while proposals await the user; the phone save must show
// that rather than forcing a local write.
func TestClientSaveRefusalIsStatus(t *testing.T) {
	ch := newClientHarness(t) // carries a pending proposal on the daemon
	ch.cli.drain()
	p := ch.cli.Tabs.Active()
	before := p.File
	runClient(t, ch, func() { ch.cli.saveActive(nil) })

	if p.File != before {
		t.Error("a refused save refetched the buffer anyway")
	}
	if !strings.Contains(ch.cli.status, "proposed change set") {
		t.Errorf("status = %q, want the daemon save refusal", ch.cli.status)
	}
}

// A daemon with no real tabs attaches to an empty client, and the client says
// so rather than presenting the empty editor as the daemon workspace. Without
// the note the attach looks like a workspace with nothing in it.
func TestClientWithNoDaemonTabsSaysSo(t *testing.T) {
	srv := controlHarness(t, "hello\n")
	c := srv.dial(t)
	if r := c.do(srv, control.Request{Op: "close", Path: srv.Tabs.Active().File.Path}); !r.OK {
		t.Fatalf("close = %+v", r)
	}
	ch := attachClient(t, srv)
	ch.cli.drain()
	if got := ch.cli.Tabs.Count(); got != 0 {
		t.Fatalf("client tabs = %d, want 0", got)
	}
	if !strings.Contains(ch.cli.status, "no open tabs") {
		t.Errorf("status = %q, want the no-tabs note", ch.cli.status)
	}
}

// Opening a path the daemon has not opened fetches its snapshot, which loads it
// headlessly in the daemon: no daemon tab appears and the client installs its
// own. Without the change the client would have refused, and with the open verb
// the file would have appeared on the laptop.
func TestClientOpenLoadsThroughTheDaemon(t *testing.T) {
	srv := controlHarness(t, "hello\n")
	dir := filepath.Dir(srv.Tabs.Active().File.Path)
	fresh := filepath.Join(dir, "fresh.go")
	if err := os.WriteFile(fresh, []byte("package fresh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ch := attachClient(t, srv)
	ch.cli.drain()
	if got := ch.cli.Tabs.Count(); got != 1 {
		t.Fatalf("setup: client tabs = %d, want 1", got)
	}
	runClient(t, ch, func() { ch.cli.OpenFile(fresh) })
	ch.cli.drain()

	if got := ch.cli.Tabs.Count(); got != 2 {
		t.Fatalf("client tabs = %d, want the daemon-open file added", got)
	}
	p := ch.cli.Tabs.Active()
	if p == nil || p.File.Path != fresh || p.File.Text() != "package fresh\n" {
		t.Fatalf("client active = %+v, want %s with the snapshot text", p, fresh)
	}
	if !headlessListed(srv, fresh) {
		t.Error("the daemon did not load the client open headlessly")
	}
	if got := srv.Tabs.Count(); got != 1 {
		t.Errorf("daemon real tabs = %d, want the client open to add none", got)
	}
}

// A path already among the client tabs focuses rather than re-opening: the
// already-open branch never reaches the daemon, so no second tab appears.
func TestClientOpenAlreadyOpenFocuses(t *testing.T) {
	srv := controlHarness(t, "hello\n")
	dir := filepath.Dir(srv.Tabs.Active().File.Path)
	other := filepath.Join(dir, "other.go")
	if err := os.WriteFile(other, []byte("package other\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := srv.dial(t)
	if r := c.do(srv, control.Request{Op: "open", Path: other}); !r.OK {
		t.Fatalf("open = %+v", r)
	}
	ch := attachClient(t, srv)
	ch.cli.drain()
	if got := ch.cli.Tabs.Count(); got != 2 {
		t.Fatalf("setup: client tabs = %d, want 2", got)
	}
	first := srv.Tabs.Active().File.Path
	runClient(t, ch, func() { ch.cli.OpenFile(first) })
	if got := ch.cli.Tabs.Count(); got != 2 {
		t.Errorf("client tabs = %d, want no duplicate from a re-open", got)
	}
	if p := ch.cli.Tabs.Active(); p == nil || p.File.Path != first {
		t.Errorf("client active = %+v, want the focused existing tab", p)
	}
}

// A path the daemon refuses surfaces its error and adds no tab. Without the
// remote open there would be no daemon refusal to show.
func TestClientOpenOutsideRootIsRefused(t *testing.T) {
	ch := newClientHarness(t)
	ch.cli.drain()
	before := ch.cli.Tabs.Count()
	runClient(t, ch, func() { ch.cli.OpenFile("/definitely/not/in/workspace.go") })
	if got := ch.cli.Tabs.Count(); got != before {
		t.Errorf("client tabs = %d, want no tab for a refused path", got)
	}
	if ch.cli.status == "" || strings.HasPrefix(ch.cli.status, "attach: ") {
		t.Errorf("status = %q, want the daemon refusal", ch.cli.status)
	}
}

// Closing a client tab closes the view only: no unsaved prompt, no save call,
// and the daemon still holds the buffer. Without the close fix ViewDirty would
// prompt and a Discard answer would drop the tab through the save path.
func TestClientCloseRemovesTabWithoutPromptOrSave(t *testing.T) {
	srv := controlHarness(t, "hello\n")
	srv.typeText("X") // the daemon buffer is dirty, so a save would write
	ch := attachClient(t, srv)
	ch.cli.drain()
	if got := ch.cli.Tabs.Count(); got != 1 {
		t.Fatalf("setup: client tabs = %d, want 1", got)
	}
	ch.cli.dispatch(keys.CloseTab, "")
	if got := ch.cli.Tabs.Count(); got != 0 {
		t.Errorf("client tabs = %d, want the tab closed", got)
	}
	if ch.cli.Prompt.Open {
		t.Error("closing a client tab raised the unsaved prompt")
	}
	if ch.srv.Tabs.Count() != 1 {
		t.Errorf("daemon tabs = %d, want the daemon buffer untouched", ch.srv.Tabs.Count())
	}
	onDisk, err := os.ReadFile(ch.srv.Pane().File.Path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(onDisk), "X") {
		t.Errorf("closing the client tab saved the daemon buffer: %q", onDisk)
	}
}

// A tab the user closed locally is not re-added by a later reconcile while its
// own daemon buffer is unchanged, even when another tab moves. Without the
// snooze mark the watch would re-add it on the next wake.
func TestClientClosedTabStaysClosedWhileUnchanged(t *testing.T) {
	srv := controlHarness(t, "hello\n")
	primary := srv.Tabs.Active().File.Path
	dir := filepath.Dir(primary)
	other := filepath.Join(dir, "other.go")
	if err := os.WriteFile(other, []byte("package other\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := srv.dial(t)
	if r := c.do(srv, control.Request{Op: "open", Path: other}); !r.OK {
		t.Fatalf("open = %+v", r)
	}
	ch := attachClient(t, srv)
	ch.cli.drain()
	if got := ch.cli.Tabs.Count(); got != 2 {
		t.Fatalf("setup: client tabs = %d, want 2", got)
	}
	// Close the primary tab; the edit below lands in other, so the closed
	// path daemon facts do not move.
	closed := -1
	for i, p := range ch.cli.Tabs.All() {
		if p.File.Path == primary {
			closed = i
		}
	}
	if closed < 0 {
		t.Fatalf("no client tab for %s", primary)
	}
	ch.cli.closeTabAt(closed)
	if got := ch.cli.Tabs.Count(); got != 1 {
		t.Fatalf("client tabs = %d after the local close, want 1", got)
	}

	for _, p := range srv.Tabs.All() {
		if p.File.Path == other {
			srv.Tabs.Focus(p)
		}
	}
	srv.typeText("Z")
	srv.Handle(ui.Tick{})
	deadline := time.After(3 * time.Second)
	for !strings.Contains(ch.cli.Tabs.All()[0].File.Text(), "Z") {
		select {
		case <-deadline:
			t.Fatalf("client never saw the wake; text = %q", ch.cli.Tabs.All()[0].File.Text())
		default:
		}
		srv.drain()
		ch.cli.drain()
		time.Sleep(time.Millisecond)
	}
	if got := ch.cli.Tabs.Count(); got != 1 {
		t.Fatalf("client tabs = %d, want the locally closed tab to stay closed", got)
	}
	if ch.cli.Tabs.All()[0].File.Path == primary {
		t.Error("the locally closed tab came back")
	}
}

// A daemon-side version change to a client-loaded headless buffer still
// updates the client tab. The client tracks the path itself, so the headless
// filter that hides an agent read does not hide the client own tab.
func TestClientSyncsItsHeadlessBuffer(t *testing.T) {
	srv := controlHarness(t, "hello\n")
	dir := filepath.Dir(srv.Tabs.Active().File.Path)
	fresh := filepath.Join(dir, "fresh.go")
	if err := os.WriteFile(fresh, []byte("package fresh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ch := attachClient(t, srv)
	ch.cli.drain()
	runClient(t, ch, func() { ch.cli.OpenFile(fresh) })
	ch.cli.drain()
	p := ch.cli.Tabs.Active()
	if p == nil || p.File.Path != fresh {
		t.Fatalf("client active = %+v, want %s", p, fresh)
	}

	// Edit the daemon headless pane directly, so this is a version change to a
	// buffer the daemon does not list as a real tab.
	edited := false
	for _, hp := range srv.headless {
		if hp.File.Path == fresh {
			hp.InsertText("Z")
			edited = true
		}
	}
	if !edited {
		t.Fatalf("the daemon has no headless pane for %s", fresh)
	}
	srv.Handle(ui.Tick{})

	deadline := time.After(3 * time.Second)
	for !strings.Contains(p.File.Text(), "Z") {
		select {
		case <-deadline:
			t.Fatalf("client never saw the headless change; text = %q", p.File.Text())
		default:
		}
		srv.drain()
		ch.cli.drain()
		time.Sleep(time.Millisecond)
	}
}

// Closing a client-loaded tab is local: the daemon keeps no tab for it (it was
// headless) and the file on disk is unchanged. Without the local close the
// client would have to ask the daemon to close a tab it never made.
func TestClientCloseLoadedBufferLeavesNoDaemonTab(t *testing.T) {
	srv := controlHarness(t, "hello\n")
	dir := filepath.Dir(srv.Tabs.Active().File.Path)
	fresh := filepath.Join(dir, "fresh.go")
	if err := os.WriteFile(fresh, []byte("package fresh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ch := attachClient(t, srv)
	ch.cli.drain()
	runClient(t, ch, func() { ch.cli.OpenFile(fresh) })
	ch.cli.drain()
	if got := ch.cli.Tabs.Count(); got != 2 {
		t.Fatalf("setup: client tabs = %d, want 2", got)
	}
	idx := -1
	for i, p := range ch.cli.Tabs.All() {
		if p.File.Path == fresh {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatalf("no client tab for %s", fresh)
	}
	ch.cli.closeTabAt(idx)
	if got := ch.cli.Tabs.Count(); got != 1 {
		t.Errorf("client tabs = %d after close, want 1", got)
	}
	if got := srv.Tabs.Count(); got != 1 {
		t.Errorf("daemon real tabs = %d, want the client open to have added none", got)
	}
	onDisk, err := os.ReadFile(fresh)
	if err != nil {
		t.Fatal(err)
	}
	if string(onDisk) != "package fresh\n" {
		t.Errorf("client close changed the file: %q", onDisk)
	}
}

// The ordinary editor still opens from disk; the attach branch must not touch
// that path.
func TestLocalOpenStillReadsFromDisk(t *testing.T) {
	h := newHarness(t, "hello\n")
	dir := filepath.Dir(h.Tabs.Active().File.Path)
	fresh := filepath.Join(dir, "fresh.go")
	if err := os.WriteFile(fresh, []byte("package fresh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.OpenFile(fresh)
	if p := h.Tabs.Active(); p == nil || p.File.Path != fresh || p.File.Text() != "package fresh\n" {
		t.Fatalf("local open = %+v, want the disk file", p)
	}
}

// A close is durable against a fetch already in flight: a snapshot staged
// before the close, whose daemon facts still match the close mark, is dropped
// by the install re-check rather than re-adding the tab. Without the snooze the
// install adds the tab back.
func TestClientClosedPathDropsAStaleInstall(t *testing.T) {
	srv := controlHarness(t, "hello\n")
	ch := attachClient(t, srv)
	ch.cli.drain()
	p := ch.cli.Tabs.Active()
	if p == nil {
		t.Fatal("client attached with no tab")
	}
	path := p.File.Path
	// A file the watch fetched before the close.
	stale := editor.NewFile(path, "hello\n", 2)

	ch.cli.closeTabAt(ch.cli.Tabs.Index())
	if got := ch.cli.Tabs.Count(); got != 0 {
		t.Fatalf("tabs after close = %d, want 0", got)
	}
	if got := ch.cli.installClientFile(clientFile{path: path, file: stale}); got != nil {
		t.Error("a stale install re-added the closed tab")
	}
	if got := ch.cli.Tabs.Count(); got != 0 {
		t.Errorf("tabs after the stale install = %d, want still 0", got)
	}
}

// A client tab with a pending proposal shows the marker, so review work is
// visible in the tab bar; a clean client tab shows none. The client marker is
// the mirrored review state, not a local save state, and the local editor's
// save-oriented dot is unchanged.
func TestClientTabShowsProposalMarker(t *testing.T) {
	ch := newClientHarness(t) // carries a pending proposal
	ch.cli.drain()
	if got := ch.cli.host.Text(); !strings.Contains(got, " •") {
		t.Errorf("a client tab with a proposal showed no marker:\n%s", got)
	}

	clean := attachClientAt(t, controlHarness(t, "hello\n"), Options{}, 120, 30)
	clean.cli.drain()
	if got := clean.cli.host.Text(); strings.Contains(got, " •") {
		t.Errorf("a clean client tab showed the marker:\n%s", got)
	}

	h := newHarness(t, "one\ntwo\n")
	h.typeText("x")
	h.Draw()
	if got := h.host.Text(); !strings.Contains(got, " •") {
		t.Errorf("the local editor lost its dirty dot:\n%s", got)
	}
}
