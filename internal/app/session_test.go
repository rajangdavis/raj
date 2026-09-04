package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"raj/internal/session"
	"raj/internal/ui"
)

// The whole point, end to end: leave a workspace somewhere, come back, land
// there. Two Apps over one root, because a session that only round-trips
// through one process is not restoring anything.
func TestSessionRestoresWhereYouWere(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "a.go")
	b := filepath.Join(root, "sub", "b.go")
	os.MkdirAll(filepath.Dir(b), 0o755)
	os.WriteFile(a, []byte("package a\n\nfunc one() {}\nfunc two() {}\n"), 0o644)
	os.WriteFile(b, []byte("package b\n\nfunc three() {}\n"), 0o644)

	first := newHarnessAt(t, root)
	first.OpenFile(a)
	first.OpenFile(b)
	first.Explorer.Tree.Expand(filepath.Dir(b))
	if p := first.Tabs.Active(); p != nil {
		p.Cursors.Set(11, 11)
		p.Viewport.Top = 1
	}
	if err := first.SaveSession(); err != nil {
		t.Fatal(err)
	}

	second := newHarnessAt(t, root)
	second.RestoreSession()
	if second.Tabs.Count() < 2 {
		t.Fatalf("restored %d tabs, want both", second.Tabs.Count())
	}
	active := second.Tabs.Active()
	if active == nil || active.File.Path != b {
		t.Fatalf("active tab = %v, want %s", active, b)
	}
	if got := active.Cursors.Primary().Head; got != 11 {
		t.Errorf("cursor = %d, want 11", got)
	}
	if !second.Explorer.Tree.Expanded(filepath.Dir(b)) {
		t.Error("the expanded directory was not restored")
	}
}

// --no-restore has to disable both directions. One that still wrote would
// overwrite the session a person was deliberately not using.
func TestNoRestoreIsInert(t *testing.T) {
	root := t.TempDir()
	f := filepath.Join(root, "a.go")
	os.WriteFile(f, []byte("x\n"), 0o644)

	seed := newHarnessAt(t, root)
	seed.OpenFile(f)
	if err := seed.SaveSession(); err != nil {
		t.Fatal(err)
	}

	skip := newHarnessAt(t, root)
	skip.NoRestore = true
	skip.RestoreSession()
	if skip.Tabs.Count() != 0 {
		t.Errorf("--no-restore reopened %d tabs", skip.Tabs.Count())
	}
	if err := skip.SaveSession(); err != nil {
		t.Fatal(err)
	}
	if st := session.Load(root); len(st.Tabs) != 1 {
		t.Errorf("--no-restore overwrote the saved session: %+v", st.Tabs)
	}
}

// A tab whose file vanished between sessions is skipped, and the rest still
// open. Restoring is best-effort by design: a workspace that moved on should
// still start.
func TestRestoreSkipsMissingFiles(t *testing.T) {
	root := t.TempDir()
	keep := filepath.Join(root, "keep.go")
	gone := filepath.Join(root, "gone.go")
	os.WriteFile(keep, []byte("x\n"), 0o644)
	os.WriteFile(gone, []byte("y\n"), 0o644)

	first := newHarnessAt(t, root)
	first.OpenFile(gone)
	first.OpenFile(keep)
	first.SaveSession()
	os.Remove(gone)

	second := newHarnessAt(t, root)
	second.RestoreSession()
	if second.Tabs.Count() != 1 {
		t.Fatalf("restored %d tabs, want just the surviving one", second.Tabs.Count())
	}
	if p := second.Tabs.Active(); p == nil || p.File.Path != keep {
		t.Errorf("active = %v, want %s", p, keep)
	}
}

// An unnamed buffer is keyed on a path it does not have, so it is not saved —
// and its absence must not shift which tab comes back active.
func TestUnnamedBuffersDoNotShiftTheActiveTab(t *testing.T) {
	root := t.TempDir()
	f := filepath.Join(root, "a.go")
	os.WriteFile(f, []byte("x\n"), 0o644)

	first := newHarnessAt(t, root)
	first.Tabs.NewFile() // unnamed, becomes active
	first.OpenFile(f)
	first.Tabs.NewFile()
	first.OpenFile(f)

	st := first.SessionState()
	for _, tab := range st.Tabs {
		if tab.Path == "" {
			t.Fatal("an unnamed buffer was saved with an empty path")
		}
	}
	if st.Active < 0 || st.Active >= len(st.Tabs) {
		t.Errorf("active = %d over %d saved tabs", st.Active, len(st.Tabs))
	}
	if len(st.Tabs) > 0 && st.Tabs[st.Active].Path != f {
		t.Errorf("active points at %q, want %s", st.Tabs[st.Active].Path, f)
	}
}

// A restored tab must be configured like one the person opened themselves,
// rather than missing the theme and wrap defaults every other tab gets.
func TestRestoredTabsGetTheUsualDefaults(t *testing.T) {
	root := t.TempDir()
	f := filepath.Join(root, "a.go")
	os.WriteFile(f, []byte("x\n"), 0o644)

	first := newHarnessAt(t, root)
	first.OpenFile(f)
	first.SaveSession()

	second := newHarnessAt(t, root)
	second.WrapDefault = true
	second.RestoreSession()
	p := second.Tabs.Active()
	if p == nil {
		t.Fatal("nothing restored")
	}
	if !p.Wrap {
		t.Error("a restored tab did not pick up the wrap default")
	}
}

// newHarnessAt builds an app over an existing directory with nothing open, so a
// test can run two of them against one workspace the way two runs of raj would.
func newHarnessAt(t *testing.T, root string) *harness {
	t.Helper()
	host := ui.NewFakeHost(120, 24)
	t.Cleanup(func() { host.Close() })
	a := New(host, root, 2)
	a.Search.Debounce = time.Nanosecond
	return &harness{App: a, host: host}
}

// Saving only at exit meant a crash or a kill lost the whole session and looked
// like the editor had forgotten. The tick writes it while running instead.
func TestSessionSavesWhileRunning(t *testing.T) {
	root := t.TempDir()
	f := filepath.Join(root, "a.go")
	os.WriteFile(f, []byte("x\n"), 0o644)

	a := newHarnessAt(t, root)
	a.OpenFile(f) // touches the session
	a.sessionTick(time.Now())

	// Read it back without any clean exit having happened.
	if st := session.Load(root); len(st.Tabs) != 1 || st.Tabs[0].Path != f {
		t.Errorf("nothing was written before exit: %+v", st.Tabs)
	}
}

// Debounced: this is view state, not work, and a write per keystroke is the
// wrong trade.
func TestSessionSaveIsDebounced(t *testing.T) {
	root := t.TempDir()
	f := filepath.Join(root, "a.go")
	os.WriteFile(f, []byte("x\n"), 0o644)

	a := newHarnessAt(t, root)
	a.OpenFile(f)
	now := time.Now()
	a.sessionTick(now)

	// A second change immediately after must not write again.
	a.TouchSession()
	a.sessionTick(now.Add(SessionSaveInterval / 2))
	if !a.sessionDirty {
		t.Error("wrote again inside the debounce window")
	}
	a.sessionTick(now.Add(SessionSaveInterval * 2))
	if a.sessionDirty {
		t.Error("did not write once the window had passed")
	}
}

// Nothing to save means no writes at all, so an idle editor does not rewrite a
// file every few seconds forever.
func TestSessionTickIsInertWhenNothingChanged(t *testing.T) {
	root := t.TempDir()
	a := newHarnessAt(t, root)
	a.sessionTick(time.Now())
	if _, err := os.Stat(session.File(root)); !os.IsNotExist(err) {
		t.Error("an idle editor wrote a session file")
	}
}

// --no-restore still disables writing, including from the tick.
func TestSessionTickRespectsNoRestore(t *testing.T) {
	root := t.TempDir()
	f := filepath.Join(root, "a.go")
	os.WriteFile(f, []byte("x\n"), 0o644)

	a := newHarnessAt(t, root)
	a.NoRestore = true
	a.OpenFile(f)
	a.sessionTick(time.Now())
	if _, err := os.Stat(session.File(root)); !os.IsNotExist(err) {
		t.Error("--no-restore wrote a session file")
	}
}
