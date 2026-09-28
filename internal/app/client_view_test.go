package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"raj/internal/control"
	"raj/internal/session"
	"raj/internal/store"
	"raj/internal/ui"
)

// A tab closed on the phone must stay closed across reattach: the client view
// is persisted, so a fresh client with the same key restores its closed set
// instead of mirroring the daemon tabs again. Without the saved view the
// reattach mirrors the host and the closed tab comes back.
func TestClientViewKeepsAClosedTabClosed(t *testing.T) {
	root := t.TempDir()
	srv := controlHarness(t, "hello\n")
	path := srv.Tabs.Active().File.Path
	srv.typeText("X") // unsaved work is what makes the daemon tab mirror

	first := attachClientAtRoot(t, srv, Options{}, 120, 12, root)
	first.cli.drain()
	if got := first.cli.Tabs.Count(); got != 1 {
		t.Fatalf("first attach tabs = %d, want the mirrored dirty tab", got)
	}
	first.cli.closeTabAt(first.cli.Tabs.Index())
	if got := first.cli.Tabs.Count(); got != 0 {
		t.Fatalf("tabs after close = %d, want 0", got)
	}
	first.cli.CloseClient()

	second := attachClientAtRoot(t, srv, Options{}, 120, 12, root)
	second.cli.drain()
	if got := second.cli.Tabs.Count(); got != 0 {
		t.Errorf("reattach restored %d tab(s), want the closed set kept", got)
	}
	if !second.cli.clientTabClosed(path) {
		t.Errorf("the saved closed set was not restored for %s", path)
	}
}

// A path the client opened is persisted and reopens on the next attach, even
// though the daemon only has it headless. Without persistence the second
// attach mirrors the daemon real tabs and the client path is lost.
func TestClientViewReopensAClientOpenedPath(t *testing.T) {
	root := t.TempDir()
	srv := controlHarness(t, "hello\n")
	dir := filepath.Dir(srv.Tabs.Active().File.Path)
	fresh := filepath.Join(dir, "fresh.go")
	if err := os.WriteFile(fresh, []byte("package fresh\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	first := attachClientAtRoot(t, srv, Options{}, 120, 12, root)
	first.cli.drain()
	if got := first.cli.Tabs.Count(); got != 0 {
		t.Fatalf("first attach tabs = %d, want a clean daemon to mirror nothing", got)
	}
	runClient(t, first, func() { first.cli.OpenFile(fresh) })
	first.cli.drain()
	if got := first.cli.Tabs.Count(); got != 1 {
		t.Fatalf("after the client open, tabs = %d, want the opened path alone", got)
	}
	first.cli.CloseClient()

	second := attachClientAtRoot(t, srv, Options{}, 120, 12, root)
	second.cli.drain()
	has := false
	for _, p := range second.cli.Tabs.All() {
		if p.File.Path == fresh {
			has = true
		}
	}
	if !has {
		t.Errorf("reattach did not reopen %s: %+v", fresh, second.cli.Tabs.All())
	}
}

// Two client keys sharing a workspace keep separate views: one closing its tab
// does not close the other.
func TestClientViewIsKeyedByClientName(t *testing.T) {
	root := t.TempDir()
	srv := controlHarness(t, "hello\n")
	srv.typeText("X") // a dirty daemon tab is what a first view mirrors

	phone := attachClientAtRoot(t, srv, Options{Name: "phone"}, 120, 12, root)
	phone.cli.drain()
	phone.cli.closeTabAt(phone.cli.Tabs.Index())
	phone.cli.CloseClient()

	laptop := attachClientAtRoot(t, srv, Options{Name: "laptop"}, 120, 12, root)
	laptop.cli.drain()
	if got := laptop.cli.Tabs.Count(); got != 1 {
		t.Errorf("laptop tabs = %d, want its own first view with the mirrored dirty tab", got)
	}
}

// A first attach on a clean daemon mirrors nothing: a clean tab has no unsaved
// work, so the viewer does not adopt it and cannot later persist a stale tab.
// The dirty and pending arms of the mirror predicate are pinned separately.
func TestClientViewFirstAttachSkipsCleanTab(t *testing.T) {
	srv := controlHarness(t, "hello\n")
	ch := attachClientAtRoot(t, srv, Options{}, 120, 12, t.TempDir())
	ch.cli.drain()
	if got := ch.cli.Tabs.Count(); got != 0 {
		t.Errorf("first attach tabs = %d, want a clean daemon to mirror nothing", got)
	}
}

// A daemon tab mirrored because it was dirty stays after it goes clean, and
// stays across a reattach: ownership is sticky, so the mirror is not
// re-derived away once the daemon buffer is saved. Without stickiness the tab
// would drop on the next reconcile and a view the user had seen would vanish.
func TestClientMirrorsDirtyTabAndKeepsItClean(t *testing.T) {
	root := t.TempDir()
	srv := controlHarness(t, "hello\n")
	path := srv.Tabs.Active().File.Path
	srv.typeText("X")
	ch := attachClientAtRoot(t, srv, Options{}, 120, 12, root)
	ch.cli.drain()
	if got := ch.cli.Tabs.Count(); got != 1 {
		t.Fatalf("dirty attach tabs = %d, want the mirrored tab", got)
	}
	// Saving clears dirty without a text change.
	if err := srv.Pane().File.Save(); err != nil {
		t.Fatalf("daemon save = %v", err)
	}
	srv.Handle(ui.Tick{})
	pumpFor(ch, 200*time.Millisecond)
	if got := ch.cli.Tabs.Count(); got != 1 {
		t.Fatalf("after saving clean, tabs = %d, want the sticky mirror", got)
	}
	if p := ch.cli.Tabs.Active(); p == nil || p.File.Path != path {
		t.Fatalf("client active = %+v, want the sticky %s", p, path)
	}
	ch.cli.CloseClient()

	// The owned set persists, so a clean daemon reattach restores the tab
	// rather than re-deriving the mirror away.
	second := attachClientAtRoot(t, srv, Options{}, 120, 12, root)
	second.cli.drain()
	if got := second.cli.Tabs.Count(); got != 1 {
		t.Errorf("reattach tabs = %d, want the owned sticky tab", got)
	}
}

// A pending change set also mirrors: the reviewer sees the proposal even
// though no text is dirty. This is the other arm of the mirror predicate.
func TestClientMirrorsPendingTab(t *testing.T) {
	srv := controlHarness(t, "hello\nworld\n")
	proposeAt(t, srv, 0, 0, "// x\n")
	ch := attachClient(t, srv)
	ch.cli.drain()
	if got := ch.cli.Tabs.Count(); got != 1 {
		t.Fatalf("pending attach tabs = %d, want the mirrored proposal", got)
	}
	p := ch.cli.Tabs.Active()
	if p == nil || len(p.File.Session().Pending()) == 0 {
		t.Fatalf("client tab = %+v, want the pending set", p)
	}
}

// The old flat view persisted every daemon tab. Migration keeps an open path
// only while the daemon buffer still has unsaved work, so the stale clean path
// drops instead of being re-adopted as a client-owned tab, and the rewritten
// view names owned rather than open.
func TestClientViewMigratesOldOpenShape(t *testing.T) {
	root := t.TempDir()
	dir := session.StateDir(root)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	srv := controlHarness(t, "hello\n")
	dirty := srv.Tabs.Active().File.Path
	srv.typeText("X") // only the active tab has unsaved work
	clean := filepath.Join(filepath.Dir(dirty), "clean.go")
	if err := os.WriteFile(clean, []byte("package clean\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := srv.dial(t)
	if r := c.do(srv, control.Request{Op: "open", Path: clean}); !r.OK {
		t.Fatalf("open = %+v", r)
	}

	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(store.ScopeClient, "attach",
		`{"open":["`+dirty+`","`+clean+`"],"closed":[]}`); err != nil {
		t.Fatal(err)
	}
	st.Close()

	ch := attachClientAtRoot(t, srv, Options{}, 120, 12, root)
	ch.cli.drain()
	if got := ch.cli.Tabs.Count(); got != 1 {
		t.Fatalf("migrated tabs = %d, want only the dirty open path", got)
	}
	if p := ch.cli.Tabs.Active(); p == nil || p.File.Path != dirty {
		t.Fatalf("client active = %+v, want the dirty %s", p, dirty)
	}
	all, err := ch.cli.state.Settings(store.ScopeClient)
	if err != nil {
		t.Fatal(err)
	}
	raw := all["attach"]
	if !strings.Contains(raw, `"owned"`) {
		t.Errorf("saved view = %s, want an owned field", raw)
	}
	if strings.Contains(raw, `"open"`) {
		t.Errorf("saved view = %s, want no legacy open field", raw)
	}
}

// An old saved view stored the closed set as bare paths. Those marks carry no
// facts to compare against, so the path is not snoozed; a daemon buffer that
// still has unsaved work is re-mirrored, which is the migration the snooze
// semantics want.
func TestClientViewReadsLegacyClosedPaths(t *testing.T) {
	root := t.TempDir()
	dir := session.StateDir(root)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	srv := controlHarness(t, "hello\n")
	path := srv.Tabs.Active().File.Path
	srv.typeText("X") // a legacy mark never snoozes, so the dirty tab re-mirrors
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(store.ScopeClient, "attach",
		`{"open":[],"closed":["`+path+`"]}`); err != nil {
		t.Fatal(err)
	}
	st.Close()

	ch := attachClientAtRoot(t, srv, Options{}, 120, 12, root)
	ch.cli.drain()
	if got := ch.cli.Tabs.Count(); got != 1 {
		t.Fatalf("client tabs = %d, want the legacy closed path re-added", got)
	}
	if p := ch.cli.Tabs.Active(); p == nil || p.File.Path != path {
		t.Fatalf("client active = %+v, want %s", p, path)
	}
}

// A saved owned path that no longer loads is dropped with a note, not an error,
// and the daemon unsaved tabs are still mirrored: membership converges to the
// daemon rather than to the saved view.
func TestClientViewDropsAPathThatNoLongerLoads(t *testing.T) {
	root := t.TempDir()
	dir := session.StateDir(root)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	bogus := filepath.Join(t.TempDir(), "gone.go")
	if err := st.SetSetting(store.ScopeClient, "attach",
		`{"owned":["`+bogus+`"],"closed":[]}`); err != nil {
		t.Fatal(err)
	}
	st.Close()

	srv := controlHarness(t, "hello\n")
	srv.typeText("X") // the daemon tab mirrors, so it is a real tab to compare
	ch := attachClientAtRoot(t, srv, Options{}, 120, 12, root)
	ch.cli.drain()
	if got := ch.cli.Tabs.Count(); got != 1 {
		t.Errorf("client tabs = %d, want the daemon tab mirrored", got)
	}
	if p := ch.cli.Tabs.Active(); p == nil || p.File.Path != srv.Tabs.Active().File.Path {
		t.Errorf("client active = %+v, want the daemon tab", p)
	}
	if !strings.Contains(ch.cli.status, "dropped") {
		t.Errorf("status = %q, want a dropped note", ch.cli.status)
	}
}
