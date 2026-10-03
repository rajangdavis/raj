package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"raj/internal/piecetable"
	"raj/internal/session"
	"raj/internal/ui"
)

// storedSession is the session the app store holds. With no store there is
// nothing to read.
func storedSession(t *testing.T, a *App) session.State {
	t.Helper()
	if a.state == nil {
		return session.State{}
	}
	blob, ok, err := a.state.Session()
	if err != nil {
		t.Fatalf("read stored session: %v", err)
	}
	if !ok {
		return session.State{}
	}
	return session.Decode(blob, a.primaryRoot())
}

// sessionHasTab reports whether the captured session would restore path as a
// tab.
func sessionHasTab(st session.State, path string) bool {
	for _, tab := range st.Tabs {
		if tab.Path == path {
			return true
		}
	}
	return false
}

// A tab that exists only because a buffer was announced -- a reveal, a goto, an
// agent open -- is the agent view, not the user tab set. It is on screen, but
// the session does not keep it, so a long agent run cannot grow the set a
// restart restores.
func TestReadOnlyAnnouncementLeavesNoSessionTab(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "root\n")
	user := h.Tabs.Active().File.Path

	other := filepath.Join(h.primaryRoot(), "other.go")
	if err := os.WriteFile(other, []byte("package other\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.openFileQuiet(other) // what `raj ctl open` does

	p := h.paneFor(other)
	if p == nil || !h.Tabs.Contains(p) {
		t.Fatal("the agent open did not put the buffer on screen")
	}
	if sessionHasTab(h.SessionState(), other) {
		t.Fatalf("an agent open was written into the session: %+v",
			h.SessionState().Tabs)
	}
	if got := h.SessionState().Tabs; len(got) != 1 || got[0].Path != user {
		t.Fatalf("session tabs = %+v, want only the user-opened %s", got, user)
	}
}

// A tab that holds a pending change set is the review the user has to decide,
// so it is a real tab and the session keeps it. Once the review is resolved the
// tab drops out of the session, so review tabs do not accumulate either.
func TestReviewTabSurvivesWhilePending(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "root\n")

	other := filepath.Join(h.primaryRoot(), "other.go")
	if err := os.WriteFile(other, []byte("package other\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := h.loadHeadless(other)
	if err != nil {
		t.Fatal(err)
	}
	h.announceQuiet(p) // the announcement a proposal makes
	p.File.Begin()
	p.File.ApplyDiff(piecetable.Agent, p.File.Session().Version(),
		[]piecetable.Hunk{{Start: 0, End: 0, Text: "// review\n"}})
	p.File.End()
	id := p.File.Session().LastGroup()
	p.File.Session().MarkGroup(id, piecetable.Proposed)

	// While the review is pending the tab is a real tab.
	if !sessionHasTab(h.SessionState(), other) {
		t.Fatalf("a review tab was not kept: %+v", h.SessionState().Tabs)
	}
	// Accepting is not saving: the accepted text is still unsaved work, so the
	// tab stays and the session is what reopens it.
	p.File.Session().AcceptGroup(id)
	if !sessionHasTab(h.SessionState(), other) {
		t.Fatalf("an accepted-but-unsaved review lost its session tab: %+v",
			h.SessionState().Tabs)
	}
	// Once the text is on disk the tab is clean and drops out of the session.
	if err := p.File.SaveOver(); err != nil {
		t.Fatal(err)
	}
	if sessionHasTab(h.SessionState(), other) {
		t.Fatalf("the saved review still holds a session tab: %+v",
			h.SessionState().Tabs)
	}
}

// The whole point, end to end: leave a workspace somewhere, come back, land
// there. Two Apps over one root, because a session that only round-trips
// through one process is not restoring anything.
func TestSessionRestoresWhereYouWere(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	if st := storedSession(t, skip.App); len(st.Tabs) != 1 {
		t.Errorf("--no-restore overwrote the saved session: %+v", st.Tabs)
	}
}

// A tab whose file vanished between sessions is skipped, and the rest still
// open. Restoring is best-effort by design: a workspace that moved on should
// still start.
func TestRestoreSkipsMissingFiles(t *testing.T) {
	t.Parallel()
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

// A state directory that cannot be created is not fatal: the editor still
// starts, just without persistence.
func TestStateDirFailureDoesNotStopStartup(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	// A regular file where the workspace's state directory belongs makes
	// MkdirAll fail, standing in for an unwritable state home.
	blocker := session.StateDir(root)
	if err := os.MkdirAll(filepath.Dir(blocker), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}

	host := ui.NewFakeHost(80, 24)
	t.Cleanup(func() { host.Close() })
	a := New(host, root, 2)
	t.Cleanup(a.CloseState)
	if a == nil {
		t.Fatal("New returned nil")
	}
	if a.state != nil {
		t.Error("a store opened despite an unusable state dir")
	}
}

// A fresh launch over a clean project writes no state into it: everything the
// editor persists lives under the XDG state home.
func TestFreshLaunchLeavesProjectClean(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	f := filepath.Join(root, "a.go")
	if err := os.WriteFile(f, []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := dirEntries(t, root)

	host := ui.NewFakeHost(80, 24)
	t.Cleanup(func() { host.Close() })
	a := New(host, root, 2)
	t.Cleanup(a.CloseState)
	a.OpenFile(f)
	if err := a.SaveSession(); err != nil {
		t.Fatal(err)
	}

	if after := dirEntries(t, root); strings.Join(after, "\n") != strings.Join(before, "\n") {
		t.Errorf("a fresh launch changed the project directory: %v -> %v", before, after)
	}
	if _, err := os.Stat(filepath.Join(session.StateDir(root), "state.db")); err != nil {
		t.Errorf("the saved state is not under the XDG state dir: %v", err)
	}
}

// dirEntries lists the names directly under dir, in ReadDir's sorted order, so
// a test can compare a project directory before and after a launch.
func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// A save populates the database, and a second app over the same root restores
// the tab and cursor from it. This is the ordinary-use round trip through the
// store.
func TestSaveSessionWritesTheStore(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	f := filepath.Join(root, "a.go")
	os.WriteFile(f, []byte("line one\nline two\nline three\n"), 0o644)

	first := newHarnessAt(t, root)
	first.OpenFile(f)
	first.Tabs.Active().Cursors.Set(4, 4)
	first.Tabs.Active().Viewport.Top = 2
	if err := first.SaveSession(); err != nil {
		t.Fatal(err)
	}
	st := storedSession(t, first.App)
	if len(st.Tabs) != 1 || st.Tabs[0].Path != f || st.Tabs[0].Cursor != 4 {
		t.Fatalf("stored session = %+v, want the open tab at cursor 4", st.Tabs)
	}

	second := newHarnessAt(t, root)
	second.RestoreSession()
	p := second.Tabs.Active()
	if p == nil || p.File.Path != f {
		t.Fatalf("restore from the store = %v, want %s", p, f)
	}
	if got := p.Cursors.Primary().Head; got != 4 {
		t.Errorf("restored cursor = %d, want 4", got)
	}
}

// Closing a committed tab remembers its cursor, and opening the file again
// lands there. The preview path deliberately does not: arrowing through the
// tree is a glance, not a request to move the caret.
func TestReopenRestoresClosedPositionButPreviewDoesNot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	f := filepath.Join(root, "a.go")
	os.WriteFile(f, []byte("line one\nline two\nline three\n"), 0o644)

	a := newHarnessAt(t, root)

	a.OpenFile(f)
	a.Tabs.Active().Cursors.Set(5, 5)
	a.closeTabAt(a.Tabs.Index())
	a.OpenFile(f)
	if got := a.Tabs.Active().Cursors.Primary().Head; got != 5 {
		t.Errorf("reopened cursor = %d, want the closed tab position 5", got)
	}

	// Re-opening a file that is already on screen leaves the caret alone: the
	// stored position is for a new pane, not a focus change.
	a.Tabs.Active().Cursors.Set(7, 7)
	a.OpenFile(f)
	if got := a.Tabs.Active().Cursors.Primary().Head; got != 7 {
		t.Errorf("re-opening an open file moved the caret to %d, want 7", got)
	}

	a.Tabs.Active().Cursors.Set(3, 3)
	a.closeTabAt(a.Tabs.Index())
	a.previewFile(f)
	if p := a.Tabs.Preview(); p == nil || p.File.Path != f {
		t.Fatalf("preview not showing %s: %v", f, a.Tabs.Preview())
	}
	if got := a.Tabs.Active().Cursors.Primary().Head; got != 0 {
		t.Errorf("preview cursor = %d, want 0 (a preview must not jump)", got)
	}
}

// CloseState is nil-safe and idempotent, so main and the test harness can both
// defer it without coordinating.
func TestCloseStateIsIdempotent(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	a := newHarnessAt(t, root)
	a.CloseState()
	a.CloseState()
	// Nil-safe: an App that never opened a store closes cleanly.
	(&App{}).CloseState()
}

// An unnamed buffer is keyed on a path it does not have, so it is not saved —
// and its absence must not shift which tab comes back active.
func TestUnnamedBuffersDoNotShiftTheActiveTab(t *testing.T) {
	t.Parallel()
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

// A preview is view state, not a tab the user committed to, so it must not be
// written to the session: arrowing through the explorer should not rewrite the
// session or reopen a file nobody opened. Without the preview skip in
// SessionState the transient tab is persisted and restored.
func TestPreviewIsNotSavedInTheSession(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	f := filepath.Join(root, "a.go")
	g := filepath.Join(root, "b.go")
	os.WriteFile(f, []byte("x\n"), 0o644)
	os.WriteFile(g, []byte("y\n"), 0o644)

	a := newHarnessAt(t, root)
	a.OpenFile(f)
	if _, err := a.Tabs.OpenPreview(g); err != nil {
		t.Fatal(err)
	}
	st := a.SessionState()
	if len(st.Tabs) != 1 || st.Tabs[0].Path != f {
		t.Fatalf("SessionState saved %+v, want just the committed %s", st.Tabs, f)
	}
	if st.Active != 0 {
		t.Errorf("active = %d, want 0 (the preview is not in the saved list)", st.Active)
	}
}

// A restored tab must be configured like one the person opened themselves,
// rather than missing the theme and wrap defaults every other tab gets.
func TestRestoredTabsGetTheUsualDefaults(t *testing.T) {
	t.Parallel()
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
	t.Cleanup(a.CloseState)
	a.Search.Debounce = time.Nanosecond
	return &harness{App: a, host: host}
}

// Saving only at exit meant a crash or a kill lost the whole session and looked
// like the editor had forgotten. The tick writes it while running instead.
func TestSessionSavesWhileRunning(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	f := filepath.Join(root, "a.go")
	os.WriteFile(f, []byte("x\n"), 0o644)

	a := newHarnessAt(t, root)
	a.OpenFile(f) // touches the session
	a.sessionTick(time.Now())

	// Read it back without any clean exit having happened.
	if st := storedSession(t, a.App); len(st.Tabs) != 1 || st.Tabs[0].Path != f {
		t.Errorf("nothing was written before exit: %+v", st.Tabs)
	}
}

// Debounced: this is view state, not work, and a write per keystroke is the
// wrong trade.
func TestSessionSaveIsDebounced(t *testing.T) {
	t.Parallel()
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

// A tab set change is not view churn: quitting inside the debounce window
// would reopen a tab the user just closed, so the next tick writes it. Opening
// and closing are both structural; a cursor move is not.
func TestSessionTabChangeBypassesTheDebounce(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	f := filepath.Join(root, "a.go")
	g := filepath.Join(root, "b.go")
	os.WriteFile(f, []byte("x\n"), 0o644)
	os.WriteFile(g, []byte("y\n"), 0o644)

	a := newHarnessAt(t, root)
	now := time.Now()
	a.OpenFile(f)
	a.sessionTick(now) // the first write records one tab
	if st := storedSession(t, a.App); len(st.Tabs) != 1 {
		t.Fatalf("setup wrote %d tabs, want 1", len(st.Tabs))
	}

	// Opening a tab well inside the window must not wait it out.
	a.OpenFile(g)
	a.sessionTick(now.Add(SessionSaveInterval / 4))
	if st := storedSession(t, a.App); len(st.Tabs) != 2 {
		t.Fatalf("opening a tab was debounced: %d tabs on disk, want 2", len(st.Tabs))
	}

	// Closing one is the same: the next tick writes the smaller set.
	a.closeTabAt(1)
	a.sessionTick(now.Add(SessionSaveInterval / 2))
	if st := storedSession(t, a.App); len(st.Tabs) != 1 || st.Tabs[0].Path != f {
		t.Fatalf("closing a tab was debounced: %+v", st.Tabs)
	}
}

// Cursor and scroll movement is the churn the interval exists to absorb; a tab
// set that did not change still waits it out.
func TestSessionCursorChangeWaitsOutTheInterval(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	f := filepath.Join(root, "a.go")
	os.WriteFile(f, []byte("abcdef\n"), 0o644)

	a := newHarnessAt(t, root)
	now := time.Now()
	a.OpenFile(f)
	a.sessionTick(now)

	// Move the cursor: same tab set, so this is not a structural change.
	if p := a.Tabs.Active(); p != nil {
		p.Cursors.Set(3, 3)
	}
	a.TouchSession()
	a.sessionTick(now.Add(SessionSaveInterval / 2))
	if !a.sessionDirty {
		t.Fatal("a cursor-only change did not wait out the interval")
	}
	a.sessionTick(now.Add(SessionSaveInterval * 2))
	if st := storedSession(t, a.App); len(st.Tabs) != 1 || st.Tabs[0].Cursor != 3 {
		t.Errorf("cursor change not written after the interval: %+v", st.Tabs)
	}
}

// Run's exit path flushes the session, so the last change lands even when the
// quit comes inside the debounce window.
func TestRunSavesSessionOnExit(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	f := filepath.Join(root, "a.go")
	g := filepath.Join(root, "b.go")
	os.WriteFile(f, []byte("x\n"), 0o644)
	os.WriteFile(g, []byte("y\n"), 0o644)

	h := newHarnessAt(t, root)
	h.OpenFile(f)
	h.sessionTick(time.Now()) // the first write records one tab on disk

	// A second tab well inside the window: a tick would hold it back, so only
	// the exit flush can put it on disk.
	h.OpenFile(g)
	h.sessionSaved = time.Now()
	h.sessionDirty = true

	h.host.Press("ctrl+c") // queued; Run consumes it and quits
	if err := h.Run(); err != nil {
		t.Fatal(err)
	}
	if st := storedSession(t, h.App); len(st.Tabs) != 2 {
		t.Errorf("Run did not flush the session on exit: %+v", st.Tabs)
	}
}

// Nothing to save means no writes at all, so an idle editor does not rewrite a
// file every few seconds forever.
func TestSessionTickIsInertWhenNothingChanged(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	a := newHarnessAt(t, root)
	a.sessionTick(time.Now())
	if _, ok, err := a.state.Session(); err != nil || ok {
		t.Errorf("an idle editor wrote a session (ok=%v, err=%v)", ok, err)
	}
}

// --no-restore still disables writing, including from the tick.
func TestSessionTickRespectsNoRestore(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	f := filepath.Join(root, "a.go")
	os.WriteFile(f, []byte("x\n"), 0o644)

	a := newHarnessAt(t, root)
	a.NoRestore = true
	a.OpenFile(f)
	a.sessionTick(time.Now())
	if _, ok, err := a.state.Session(); err != nil || ok {
		t.Errorf("--no-restore wrote a session (ok=%v, err=%v)", ok, err)
	}
}

// The idle tick is where fragmentation is folded back: two adjacent pieces by
// one author in one change set merge, the composed text is byte-identical, and
// a second tick has nothing left to fold. Modelled on
// TestCompactMergesAdjacentSameAuthorPieces and the sessionTick tests above.
func TestCompactTickMergesAdjacentSameAuthorPieces(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "")
	p := h.Pane()
	p.File.Begin()
	p.File.Insert(p.Author, 0, "hello ")
	p.File.Insert(p.Author, 6, "world")
	p.File.End()
	if got := p.File.Pieces(); got != 2 {
		t.Fatalf("precondition pieces = %d, want 2", got)
	}
	before := p.File.Text()
	now := time.Now()
	h.compactTick(now)
	if got := p.File.Pieces(); got != 1 {
		t.Fatalf("after the tick pieces = %d, want 1", got)
	}
	if got := p.File.Text(); got != before {
		t.Fatalf("text changed to %q, want %q", got, before)
	}
	// Compact is idempotent: a later tick folds nothing further.
	h.compactTick(now.Add(CompactInterval))
	if got := p.File.Pieces(); got != 1 {
		t.Fatalf("a second tick changed pieces to %d, want 1", got)
	}
}

// An empty buffer has nothing to fold, so the tick touches neither its pieces
// nor its version. Modelled on TestSessionTickIsInertWhenNothingChanged.
func TestCompactTickLeavesAQuietBufferAlone(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "")
	p := h.Pane()
	beforePieces, beforeVersion := p.File.Pieces(), p.File.Session().Version()
	now := time.Now()
	h.compactTick(now)
	h.compactTick(now.Add(CompactInterval))
	if got := p.File.Pieces(); got != beforePieces {
		t.Fatalf("pieces = %d, want %d", got, beforePieces)
	}
	if got := p.File.Session().Version(); got != beforeVersion {
		t.Fatalf("version = %d, want %d", got, beforeVersion)
	}
	// The piece-count guard is what keeps a buffer with nothing to fold off the
	// origin-index walk, so it must not even be marked as attempted.
	if len(h.compacted) != 0 {
		t.Fatalf("quiet buffer was marked compacted: %v", h.compacted)
	}
}

// The compaction memo is keyed on the pane, so a closed pane entry outlives
// its buffer and Go can hand that pointer to a new buffer whose (version,
// generation) matches, skipping the compaction it needs. A tick must drop the
// entries that are not in Tabs.All before it reads any of them. Modelled on
// TestCompactTickMergesAdjacentSameAuthorPieces, which builds the recorded
// state this one then closes.
func TestCompactTickPrunesClosedPaneEntries(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "")
	p := h.Pane()
	p.File.Begin()
	p.File.Insert(p.Author, 0, "hello ")
	p.File.Insert(p.Author, 6, "world")
	p.File.End()
	if got := p.File.Pieces(); got != 2 {
		t.Fatalf("precondition pieces = %d, want 2", got)
	}
	now := time.Now()
	h.compactTick(now)
	if _, ok := h.compacted[p]; !ok {
		t.Fatal("precondition: the pane state was not recorded")
	}
	// Close the tab. A saved buffer closes without the unsaved prompt, and the
	// pane pointer is stale in the map afterwards.
	h.press("super+s")
	h.closeTabAt(0)
	if got := len(h.Tabs.All()); got != 0 {
		t.Fatalf("tabs after close = %d, want 0", got)
	}
	h.compactTick(now.Add(CompactInterval))
	if _, ok := h.compacted[p]; ok {
		t.Error("a closed pane compaction state outlived the tab")
	}
}

// writeLines writes n lines so a test can grow and shrink the file between
// sessions.
func writeLines(t *testing.T, path string, n int) {
	t.Helper()
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Same file, same terminal: a proportional restore lands exactly on the saved
// line, so nothing about the old behaviour is lost.
func TestScrollRestoresSameSize(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	f := filepath.Join(root, "a.go")
	writeLines(t, f, 100)

	first := newHarnessAt(t, root)
	first.OpenFile(f)
	first.Tabs.Active().Viewport.Top = 40
	if err := first.SaveSession(); err != nil {
		t.Fatal(err)
	}

	second := newHarnessAt(t, root)
	second.RestoreSession()
	second.drain()
	p := second.Tabs.Active()
	if p == nil {
		t.Fatal("nothing restored")
	}
	if p.Viewport.Top != 40 {
		t.Errorf("same-size restore top = %d, want 40", p.Viewport.Top)
	}
}

// A file that grew between sessions must reopen at the same place in the
// document, not at the same line number — the line number is the top of a
// now much larger file.
func TestScrollRestoresProportionallyWhenFileGrows(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	f := filepath.Join(root, "a.go")
	writeLines(t, f, 100)

	first := newHarnessAt(t, root)
	first.OpenFile(f)
	first.Tabs.Active().Viewport.Top = 40
	first.SaveSession()

	// Overnight, the file doubles.
	writeLines(t, f, 200)

	second := newHarnessAt(t, root)
	second.RestoreSession()
	second.drain()
	p := second.Tabs.Active()
	if p == nil {
		t.Fatal("nothing restored")
	}
	if got, want := p.Viewport.Top, 80; got != want {
		t.Errorf("top = %d, want %d (40/100 of 200 lines, not the saved 40)",
			got, want)
	}
}

// A file that shrank clamps at its end rather than pointing past it.
func TestScrollRestoresClampedWhenFileShrinks(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	f := filepath.Join(root, "a.go")
	writeLines(t, f, 100)

	// 100 content lines are 101 document lines (the trailing newline leaves an
	// empty last line). Top = 100 is the very last line, so the saved ratio
	// is 100/101 — a position that, recomputed against 11 lines, rounds to 11
	// and must be clamped to the new last line, 10.
	first := newHarnessAt(t, root)
	first.OpenFile(f)
	first.Tabs.Active().Viewport.Top = 100
	first.SaveSession()

	writeLines(t, f, 10)

	second := newHarnessAt(t, root)
	second.RestoreSession()
	second.drain()
	p := second.Tabs.Active()
	if p == nil {
		t.Fatal("nothing restored")
	}
	if got, want := p.Viewport.Top, 10; got != want {
		t.Errorf("top = %d, want %d (clamped to the last line)", got, want)
	}
}

// An empty file has nothing to be proportional to: the ratio is zero, restore
// lands at the top, and nothing divides by zero.
func TestScrollRestoresEmptyFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	f := filepath.Join(root, "a.go")
	os.WriteFile(f, []byte(""), 0o644)

	first := newHarnessAt(t, root)
	first.OpenFile(f)
	first.SaveSession()
	if st := storedSession(t, first.App); len(st.Tabs) != 1 || st.Tabs[0].Ratio != 0 {
		t.Fatalf("empty file saved ratio = %+v, want 0", st.Tabs)
	}

	second := newHarnessAt(t, root)
	second.RestoreSession()
	second.drain()
	if p := second.Tabs.Active(); p == nil || p.Viewport.Top != 0 {
		t.Errorf("empty-file restore top = %v, want 0", second.Tabs.Active())
	}
}

// A taller terminal must not change the restored position: the ratio is
// document-relative, so the same file opens at the same line no matter the
// pane height.
func TestScrollRestoresIndependentOfTerminalSize(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	f := filepath.Join(root, "a.go")
	writeLines(t, f, 100)

	first := newHarnessAt(t, root)
	first.OpenFile(f)
	first.Tabs.Active().Viewport.Top = 40
	first.SaveSession()

	host := ui.NewFakeHost(120, 48)
	t.Cleanup(func() { host.Close() })
	a := New(host, root, 2)
	a.Search.Debounce = time.Nanosecond
	a.RestoreSession()
	(&harness{App: a, host: host}).drain()
	p := a.Tabs.Active()
	if p == nil {
		t.Fatal("nothing restored")
	}
	if p.Viewport.Top != 40 {
		t.Errorf("taller-terminal top = %d, want 40", p.Viewport.Top)
	}
}

// A stored session written before the ratio existed restores by its plain
// Top, so a saved position from an older build still lands where it used to.
func TestScrollRestoresTopWhenRatioAbsent(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	f := filepath.Join(root, "a.go")
	writeLines(t, f, 100)

	body := []byte(`{"version":1,"tabs":[{"path":"` + f + `","cursor":0,"top":40}],"active":0}`)
	a := newHarnessAt(t, root)
	if err := a.state.PutSession(body); err != nil {
		t.Fatal(err)
	}
	a.RestoreSession()
	a.drain()
	if got := a.Tabs.Active().Viewport.Top; got != 40 {
		t.Errorf("old-format restore top = %d, want 40", got)
	}
}

// A per-pane hints choice survives a save and restore: the session carries the
// pane flag, not the app default, so a tab left with hints off comes back off
// while the default still governs tabs with no saved choice.
func TestSessionPersistsPaneHints(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	f := filepath.Join(root, "a.go")
	os.WriteFile(f, []byte("x\n"), 0o644)

	first := newHarnessAt(t, root)
	first.OpenFile(f)
	first.Tabs.Active().Hints = false
	if err := first.SaveSession(); err != nil {
		t.Fatal(err)
	}

	second := newHarnessAt(t, root)
	second.InlayHints = true
	second.RestoreSession()
	p := second.Tabs.Active()
	if p == nil {
		t.Fatal("nothing restored")
	}
	if p.Hints {
		t.Error("the saved per-pane hints-off did not survive the restore")
	}
}

// A store session is the only source of session state: a save writes the tab
// set, and a file's position is retrievable from its own row for the path (the
// rows outlive the tab set, so a closed tab is still remembered).
func TestStoreSessionIsTheOnlySource(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	f := filepath.Join(root, "a.go")
	os.WriteFile(f, []byte("line one\nline two\n"), 0o644)

	first := newHarnessAt(t, root)
	first.OpenFile(f)
	first.Tabs.Active().Cursors.Set(4, 4)
	if err := first.SaveSession(); err != nil {
		t.Fatal(err)
	}
	st := storedSession(t, first.App)
	if len(st.Tabs) != 1 || st.Tabs[0].Path != f {
		t.Fatalf("the tab set did not reach the store: %+v", st.Tabs)
	}
	if st.Tabs[0].Cursor != 4 {
		t.Fatalf("the session blob did not carry the cursor: %+v", st.Tabs)
	}
	cursor, _, ok, err := first.state.Position(f)
	if err != nil || !ok || cursor != 4 {
		t.Fatalf("position row for %s = %d/%v, err %v; want cursor 4", f, cursor, ok, err)
	}
	second := newHarnessAt(t, root)
	second.RestoreSession()
	if got := second.Tabs.Active(); got == nil || got.File.Path != f {
		t.Fatalf("restore from the store = %v, want %s", got, f)
	}
}

// The sidebar kind and its open/closed state survive a restart.
func TestSessionRestoresSidebar(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	f := filepath.Join(root, "a.go")
	if err := os.WriteFile(f, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	first := newHarnessAt(t, root)
	first.OpenFile(f)
	first.sidebar = SidebarNone
	first.focus = FocusEditor
	if err := first.SaveSession(); err != nil {
		t.Fatal(err)
	}
	second := newHarnessAt(t, root)
	second.RestoreSession()
	if got := second.SidebarMode(); got != SidebarNone {
		t.Errorf("sidebar = %v, want closed", got)
	}
}

// restoreSidebar is tri-state: a session written before the field (nil) keeps
// the startup default pane, an explicit "" closes the sidebar, and a named
// pane selects it and re-enters it only when the session's focus was the
// sidebar. Without the tri-state the nil case would overwrite the explorer
// default and a named pane could not re-focus.
func TestRestoreSidebarTriState(t *testing.T) {
	t.Parallel()
	defaults := newHarnessAt(t, t.TempDir())
	defaults.restoreSidebar(nil)
	if defaults.sidebar != SidebarExplorer {
		t.Errorf("absent sidebar = %v, want the explorer default", defaults.sidebar)
	}

	closed := ""
	defaults.focus = FocusEditor
	defaults.restoreSidebar(&closed)
	if defaults.sidebar != SidebarNone {
		t.Errorf("closed sidebar = %v, want closed", defaults.sidebar)
	}

	problems := "problems"
	defaults.focus = FocusEditor
	defaults.restoreSidebar(&problems)
	if defaults.sidebar != SidebarProblems {
		t.Errorf("named sidebar = %v, want problems", defaults.sidebar)
	}
	if defaults.focus != FocusEditor {
		t.Errorf("focus = %v, want it left on the editor", defaults.focus)
	}

	bogus := "bogus"
	defaults.sidebar = SidebarSearch
	defaults.restoreSidebar(&bogus)
	if defaults.sidebar != SidebarSearch {
		t.Errorf("unknown sidebar name changed the pane to %v", defaults.sidebar)
	}
}

// isGitPath is any component named .git, so a checkout file and a worktree
// both count and a .github directory does not.
func TestIsGitPath(t *testing.T) {
	t.Parallel()
	for _, p := range []string{"/w/.git/COMMIT_EDITMSG", "/w/sub/.git/MERGE_MSG", "/w/.git"} {
		if !isGitPath(p) {
			t.Errorf("isGitPath(%q) = false, want true", p)
		}
	}
	for _, p := range []string{"/w/git/COMMIT_EDITMSG", "/w/.github/x", "/w/a.go", ""} {
		if isGitPath(p) {
			t.Errorf("isGitPath(%q) = true, want false", p)
		}
	}
}

// A git transient buffer is neither a session tab nor a journal log, so a
// commit message does not come back on the next launch. The journal gate is
// turned on deliberately: with it off appendJournal returns before the .git
// check, so the journal half would pass for any path at all.
func TestGitTransientIsNotPersisted(t *testing.T) {
	t.Setenv(JournalEnv, "1")
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	msg := filepath.Join(root, ".git", "COMMIT_EDITMSG")
	if err := os.WriteFile(msg, []byte("subject\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := newHarnessAt(t, root)
	h.OpenFile(msg)
	h.Pane().HandleText("more")
	for _, tab := range h.SessionState().Tabs {
		if tab.Path == msg {
			t.Fatalf("a git transient was saved as a session tab")
		}
	}
	h.appendJournal(h.Pane())
	if _, err := os.Stat(filepath.Join(h.journalDir(), journalName(msg))); err == nil {
		t.Errorf("a journal log was written for a git transient")
	}
}
