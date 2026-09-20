package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"raj/internal/session"
	"raj/internal/store"
)

// A tab closed on the phone must stay closed across reattach: the client view
// is persisted, so a fresh client with the same key restores its closed set
// instead of mirroring the daemon tabs again. Without the saved view the
// reattach mirrors the host and the closed tab comes back.
func TestClientViewKeepsAClosedTabClosed(t *testing.T) {
	root := t.TempDir()
	srv := controlHarness(t, "hello\n")
	path := srv.Tabs.Active().File.Path

	first := attachClientAtRoot(t, srv, Options{}, 120, 12, root)
	first.cli.drain()
	if got := first.cli.Tabs.Count(); got != 1 {
		t.Fatalf("first attach tabs = %d, want the mirrored tab", got)
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
	runClient(t, first, func() { first.cli.OpenFile(fresh) })
	first.cli.drain()
	if got := first.cli.Tabs.Count(); got != 2 {
		t.Fatalf("first attach tabs = %d, want the mirrored plus the opened path", got)
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

	phone := attachClientAtRoot(t, srv, Options{Name: "phone"}, 120, 12, root)
	phone.cli.drain()
	phone.cli.closeTabAt(phone.cli.Tabs.Index())
	phone.cli.CloseClient()

	laptop := attachClientAtRoot(t, srv, Options{Name: "laptop"}, 120, 12, root)
	laptop.cli.drain()
	if got := laptop.cli.Tabs.Count(); got != 1 {
		t.Errorf("laptop tabs = %d, want its own first view with the mirrored tab", got)
	}
}

// A first attach with no saved view still mirrors the daemon real tabs.
func TestClientViewFirstAttachMirrors(t *testing.T) {
	srv := controlHarness(t, "hello\n")
	ch := attachClientAtRoot(t, srv, Options{}, 120, 12, t.TempDir())
	ch.cli.drain()
	if got := ch.cli.Tabs.Count(); got != 1 {
		t.Errorf("first attach tabs = %d, want the mirrored tab", got)
	}
}

// An old saved view stored the closed set as bare paths. Those marks carry no
// facts to compare against, so the path is re-added rather than staying closed
// forever, which is the migration the snooze semantics want.
func TestClientViewReadsLegacyClosedPaths(t *testing.T) {
	root := t.TempDir()
	dir := session.StateDir(root)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	srv := controlHarness(t, "hello\n")
	path := srv.Tabs.Active().File.Path
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

// A saved path that no longer loads is dropped with a note, not an error, and
// the daemon real tabs are still mirrored: membership converges to the daemon
// rather than to the saved view.
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
		`{"open":["`+bogus+`"],"closed":[]}`); err != nil {
		t.Fatal(err)
	}
	st.Close()

	srv := controlHarness(t, "hello\n")
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
