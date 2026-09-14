package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"raj/internal/control"
	"raj/internal/editor"
	"raj/internal/journal"
	"raj/internal/piecetable"
	"raj/internal/ui"
)

// The op-log spike, exercised through the real harness and real File and
// Session objects rather than fakes. The gate is an environment variable, so
// every test that wants capture sets it before the app is built.

// readLog opens a buffer's log by the name the app would give it.
func readLog(t *testing.T, h *harness, path string) *journal.Log {
	t.Helper()
	l, err := journal.Open(filepath.Join(h.journalDir(), journalName(path)))
	if err != nil {
		t.Fatalf("opening log: %v", err)
	}
	return l
}

// archivedLogs lists the logs a mismatch rotated out of the restore path.
func archivedLogs(t *testing.T, a *App) []os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(a.journalDir(), "archive"))
	if err != nil {
		return nil
	}
	return entries
}

// TestJournalCaptureWritesBaseStoreAndOps captures one edit and asserts the
// three records the format promises: the origin, the store growth the inserted
// pieces address, and the op itself.
func TestJournalCaptureWritesBaseStoreAndOps(t *testing.T) {
	t.Setenv(JournalEnv, "1")
	h := newHarness(t, "hello\n")
	defer h.closeJournals()

	p := h.Pane()
	h.typeText("X")
	h.flushJournal(p)

	l := readLog(t, h, p.File.Path)
	var base *journal.Base
	stores := 0
	ops := 0
	for _, r := range l.Records {
		switch v := r.(type) {
		case journal.Base:
			base = &v
		case journal.StoreAppend:
			stores++
		case journal.Op:
			ops++
		}
	}
	if base == nil {
		t.Fatal("no base record was written")
	}
	if base.Path != p.File.Path {
		t.Errorf("base path = %q, want %q", base.Path, p.File.Path)
	}
	if base.Hash != hashBytes([]byte("hello\n")) {
		t.Errorf("base hash = %q, want the origin's digest", base.Hash)
	}
	if stores == 0 {
		t.Error("no store append was written for the inserted text")
	}
	if ops == 0 {
		t.Error("no op was written for the edit")
	}
}

// TestJournalRestoreRebuildsTextAndVersion is the round trip: capture, rebuild
// a fresh session from the log, and get the same text at the same version.
func TestJournalRestoreRebuildsTextAndVersion(t *testing.T) {
	t.Setenv(JournalEnv, "1")
	h := newHarness(t, "abc")
	defer h.closeJournals()

	p := h.Pane()
	h.typeText("XY")
	h.flushJournal(p)

	want := p.File.Text()
	wantVer := p.File.Session().Version()

	sess := buildSession(readLog(t, h, p.File.Path))
	if sess == nil {
		t.Fatal("buildSession refused the log")
	}
	if got := sess.Buffer().Slice(0, sess.Buffer().Len()); got != want {
		t.Errorf("restored text = %q, want %q", got, want)
	}
	if sess.Version() != wantVer {
		t.Errorf("restored version = %d, want %d", sess.Version(), wantVer)
	}

	// The restored buffer presents as dirty, because the log's ops changed the
	// base it was opened against.
	f := editor.NewRestoredFile(p.File.Path, sess, 2, editor.RestoredWrite{})
	if f.Text() != want {
		t.Errorf("wrapped file text = %q, want %q", f.Text(), want)
	}
	if !f.Dirty() {
		t.Error("a restored buffer with edits should read dirty")
	}
}

// TestJournalRestoresDecision round-trips a review decision: a proposed change
// set is still proposed after the log is replayed.
func TestJournalRestoresDecision(t *testing.T) {
	t.Setenv(JournalEnv, "1")
	h := newHarness(t, reviewFixture)
	defer h.closeJournals()

	p := h.Pane()
	id := propose(t, h, piecetable.Hunk{Start: reviewAt, End: reviewAt + len(reviewOld), Text: reviewNew})
	h.flushJournal(p)

	sess := buildSession(readLog(t, h, p.File.Path))
	if sess == nil {
		t.Fatal("buildSession refused the log")
	}
	if got := sess.GroupState(id); got != piecetable.Proposed {
		t.Errorf("restored group state = %v, want proposed", got)
	}
}

// TestJournalRestoreSkipsChangedFile is the safety property: when the file
// moved on disk the log is not replayed and not deleted, and the user is told.
func TestJournalRestoreSkipsChangedFile(t *testing.T) {
	t.Setenv(JournalEnv, "1")
	h := newHarness(t, "base\n")
	defer h.closeJournals()

	p := h.Pane()
	h.typeText("X")
	h.flushJournal(p)
	logPath := filepath.Join(h.journalDir(), journalName(p.File.Path))

	if err := os.WriteFile(p.File.Path, []byte("different\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	host := ui.NewFakeHost(120, 12)
	t.Cleanup(func() { host.Close() })
	a := New(host, h.root, 2)
	defer a.closeJournals()
	a.RestoreSession()

	if a.Pane() == nil {
		t.Fatal("the changed file did not open")
	}
	if !strings.Contains(a.Status(), "changed on disk") {
		t.Errorf("status = %q, want a note about the changed file", a.Status())
	}
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Errorf("the mismatched log still sits in the restore path (err=%v)", err)
	}
	if got := archivedLogs(t, a); len(got) != 1 {
		t.Errorf("archived logs = %d, want the mismatched one", len(got))
	}
}

// TestJournalGateOffWritesNothing is the default path: with the gate off, an
// edit and a flush create no directory and no log.
func TestJournalGateOffWritesNothing(t *testing.T) {
	t.Setenv(JournalEnv, "")
	h := newHarness(t, "abc")
	defer h.closeJournals()

	p := h.Pane()
	h.typeText("X")
	h.journalTick(time.Now())
	h.flushJournal(p)

	if _, err := os.Stat(h.journalDir()); !os.IsNotExist(err) {
		t.Errorf("journal directory exists with the gate off (err=%v)", err)
	}
}

// A save keeps the history and appends a Written marker, so a restart whose
// disk matches the last write restores the layered state instead of skipping it
// as "changed on disk". This is the case the origin-only check used to lose.
func TestJournalRestoreAfterSaveUsesWrittenHash(t *testing.T) {
	t.Setenv(JournalEnv, "1")
	h := newHarness(t, "base\n")
	defer h.closeJournals()

	p := h.Pane()
	h.typeText("X")
	h.press("super+s")
	saved := p.File.Text()
	logPath := filepath.Join(h.journalDir(), journalName(p.File.Path))

	l, err := journal.Open(logPath)
	if err != nil {
		t.Fatalf("opening log: %v", err)
	}
	wrote := false
	for _, r := range l.Records {
		if _, ok := r.(journal.Written); ok {
			wrote = true
		}
	}
	if !wrote {
		t.Fatal("no Written marker after a save")
	}

	// A real restart has the tab in the session, so record it: without that the
	// log reads as a clean leftover and is dropped before it can attach.
	h.sessionTick(time.Now())

	host := ui.NewFakeHost(120, 12)
	t.Cleanup(func() { host.Close() })
	a := New(host, h.root, 2)
	defer a.closeJournals()
	a.RestoreSession()

	if a.Pane() == nil {
		t.Fatal("the saved file did not open")
	}
	if got := a.Pane().File.Text(); got != saved {
		t.Errorf("restored text = %q, want the saved composition %q", got, saved)
	}
	if strings.Contains(a.Status(), "changed on disk") {
		t.Errorf("restore skipped a log whose disk matches the last write: %q", a.Status())
	}
}

// A save's marker must not paper over a later external edit: the disk matches
// neither the origin nor the last write, so restore skips and leaves the log.
func TestJournalRestoreSkipsExternalEditAfterSave(t *testing.T) {
	t.Setenv(JournalEnv, "1")
	h := newHarness(t, "base\n")
	defer h.closeJournals()

	p := h.Pane()
	h.typeText("X")
	h.press("super+s")
	logPath := filepath.Join(h.journalDir(), journalName(p.File.Path))

	if err := os.WriteFile(p.File.Path, []byte("different\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	host := ui.NewFakeHost(120, 12)
	t.Cleanup(func() { host.Close() })
	a := New(host, h.root, 2)
	defer a.closeJournals()
	a.RestoreSession()

	if a.Pane() == nil {
		t.Fatal("the changed file did not open")
	}
	if got := a.Pane().File.Text(); got != "different\n" {
		t.Errorf("restored text = %q, want the file on disk", got)
	}
	if !strings.Contains(a.Status(), "changed on disk") {
		t.Errorf("status = %q, want a note about the changed file", a.Status())
	}
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Errorf("the mismatched log still sits in the restore path (err=%v)", err)
	}
	if got := archivedLogs(t, a); len(got) != 1 {
		t.Errorf("archived logs = %d, want the mismatched one", len(got))
	}
}

// Two authors' ops round-trip: the log carries an author row for each, and
// StartControl seeds the registry from it, so each id resolves to the same
// identity, name and kind it had when written.
func TestJournalRestoresAuthorTable(t *testing.T) {
	t.Setenv(JournalEnv, "1")
	h := controlHarness(t, "hello\n")
	defer h.closeJournals()

	p := h.Pane()
	first, err := h.control.Participants.Join("agent-a", "claude-a", control.KindAgent)
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.control.Participants.Join("agent-b", "claude-b", control.KindAgent)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("the two identities share an author id")
	}
	p.File.Begin()
	p.File.ApplyDiff(piecetable.Author(first), p.File.Session().Version(),
		[]piecetable.Hunk{{Start: 0, End: 0, Text: "A"}})
	p.File.End()
	p.File.Begin()
	p.File.ApplyDiff(piecetable.Author(second), p.File.Session().Version(),
		[]piecetable.Hunk{{Start: 0, End: 0, Text: "B"}})
	p.File.End()
	h.flushJournal(p)

	host := ui.NewFakeHost(120, 12)
	t.Cleanup(func() { host.Close() })
	a := New(host, h.root, 2)
	defer a.closeJournals()
	a.RestoreSession()

	sock := controlSock(t, "c-restore.sock")
	if err := a.StartControl(sock, false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.StopControl)

	for _, want := range []struct {
		id   uint8
		name string
	}{{first, "claude-a"}, {second, "claude-b"}} {
		got, ok := a.control.Participants.Get(want.id)
		if !ok {
			t.Errorf("author %d missing from the restored registry", want.id)
			continue
		}
		if got.Name != want.name {
			t.Errorf("author %d name = %q, want %q", want.id, got.Name, want.name)
		}
		if got.Kind != control.KindAgent {
			t.Errorf("author %d kind = %q, want agent", want.id, got.Kind)
		}
	}
	// The same identity reconnecting keeps its id rather than taking a fresh
	// one from the join order.
	if again, err := a.control.Participants.Join("agent-a", "", control.KindAgent); err != nil || again != first {
		t.Errorf("Join(agent-a) = %d, %v, want the restored id %d", again, err, first)
	}
}

// A log whose base no longer matches the disk is rotated into logs/archive and
// the file opens clean; the next edit starts a fresh log whose base is the
// current bytes, rather than refusing or appending to the archived one.
func TestJournalArchivesMismatchedLogAndStartsFreshBase(t *testing.T) {
	t.Setenv(JournalEnv, "1")
	h := newHarness(t, "base\n")
	defer h.closeJournals()

	p := h.Pane()
	h.typeText("X")
	h.flushJournal(p)
	logPath := filepath.Join(h.journalDir(), journalName(p.File.Path))

	if err := os.WriteFile(p.File.Path, []byte("external\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	host := ui.NewFakeHost(120, 12)
	t.Cleanup(func() { host.Close() })
	a := New(host, h.root, 2)
	defer a.closeJournals()
	a.RestoreSession()

	if a.Pane() == nil {
		t.Fatal("the changed file did not open")
	}
	if a.Pane().File.Dirty() {
		t.Error("the buffer opened dirty; the clean disk should stand")
	}
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Errorf("the mismatched log still sits in the restore path (err=%v)", err)
	}
	if got := archivedLogs(t, a); len(got) != 1 {
		t.Fatalf("archived logs = %d, want the mismatched one", len(got))
	}

	// The first mutation after the rotation lays a fresh base against the bytes
	// now on disk, not the archived log.
	f := a.Pane().File
	f.Begin()
	f.ApplyDiff(piecetable.User, f.Session().Version(),
		[]piecetable.Hunk{{Start: 0, End: 0, Text: "Y"}})
	f.End()
	a.flushJournal(a.Pane())

	l, err := journal.Open(logPath)
	if err != nil {
		t.Fatalf("opening the fresh log: %v", err)
	}
	base, ok := baseOf(l)
	if !ok {
		t.Fatal("the fresh log has no base record")
	}
	if got := string(base.Bytes); got != "external\n" {
		t.Errorf("fresh base bytes = %q, want the current disk bytes", got)
	}
	if base.Hash != hashBytes([]byte("external\n")) {
		t.Errorf("fresh base hash = %q, want the current disk bytes' digest", base.Hash)
	}
}

// A save records the session version its bytes were written at, so a restart
// whose disk matches the write restores a clean buffer, not a dirty one.
func TestJournalSaveThenRestoreIsClean(t *testing.T) {
	t.Setenv(JournalEnv, "1")
	h := newHarness(t, "base\n")
	defer h.closeJournals()

	p := h.Pane()
	h.typeText("X")
	h.press("super+s")
	saved := p.File.Text()
	if p.File.Dirty() {
		t.Fatal("the buffer is dirty immediately after a save")
	}

	// A real restart has the tab in the session, so record it: without that the
	// log reads as a clean leftover and is dropped before it can attach.
	h.sessionTick(time.Now())

	host := ui.NewFakeHost(120, 12)
	t.Cleanup(func() { host.Close() })
	a := New(host, h.root, 2)
	defer a.closeJournals()
	a.RestoreSession()

	if a.Pane() == nil {
		t.Fatal("the saved file did not open")
	}
	if got := a.Pane().File.Text(); got != saved {
		t.Errorf("restored text = %q, want the saved composition %q", got, saved)
	}
	if a.Pane().File.Dirty() {
		t.Error("a restored buffer whose disk matches the last write should read clean")
	}
}

// With the gate off, restore neither reads nor rotates a mismatched log: the
// feature is inert.
func TestJournalGateOffLeavesLogsAlone(t *testing.T) {
	t.Setenv(JournalEnv, "1")
	h := newHarness(t, "base\n")
	defer h.closeJournals()

	p := h.Pane()
	h.typeText("X")
	h.flushJournal(p)
	logPath := filepath.Join(h.journalDir(), journalName(p.File.Path))

	if err := os.WriteFile(p.File.Path, []byte("external\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv(JournalEnv, "")
	host := ui.NewFakeHost(120, 12)
	t.Cleanup(func() { host.Close() })
	a := New(host, h.root, 2)
	defer a.closeJournals()
	a.RestoreSession()

	if _, err := os.Stat(logPath); err != nil {
		t.Errorf("the gate off rotated a log: %v", err)
	}
	if got := archivedLogs(t, a); len(got) != 0 {
		t.Errorf("archived logs with the gate off = %d, want 0", len(got))
	}
}

// An explicit close removes the buffer's log: the user decided not to keep the
// buffer, so there is nothing left to recover and nothing to reopen later.
func TestJournalCloseRemovesLog(t *testing.T) {
	t.Setenv(JournalEnv, "1")
	h := newHarness(t, "base\n")
	defer h.closeJournals()

	p := h.Pane()
	h.typeText("X")
	h.press("super+s") // save first, so the close is not the unsaved prompt
	logPath := filepath.Join(h.journalDir(), journalName(p.File.Path))
	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("no log before the close: %v", err)
	}

	h.closeTabAt(0)

	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Errorf("the closed buffer's log is still on disk (err=%v)", err)
	}
}

// A socket close runs the same bookkeeping as the UI close: closeDoc forgets
// the journal (and the LSP document) before the tab is removed, so the tab does
// not come back on the next start.
func TestJournalSocketCloseRemovesLog(t *testing.T) {
	t.Setenv(JournalEnv, "1")
	h := controlHarness(t, "base\n")
	defer h.closeJournals()

	p := h.Pane()
	h.typeText("X")
	h.press("super+s")
	logPath := filepath.Join(h.journalDir(), journalName(p.File.Path))

	c := h.dial(t)
	r := c.do(h, control.Request{Op: "close", Path: p.File.Path})
	if !r.OK {
		t.Fatalf("close = %+v", r)
	}
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Errorf("a socket close left the journal behind (err=%v)", err)
	}
	if n := len(h.Tabs.All()); n != 0 {
		t.Errorf("tabs after close = %d, want 0", n)
	}
}

// A log whose replay is already on disk and holds nothing to decide is a
// leftover from a close, not work to recover: restore removes it rather than
// opening a tab for it, so a tab the user closed does not come back.
func TestJournalRestoreDropsCleanLeftover(t *testing.T) {
	t.Setenv(JournalEnv, "1")
	h := newHarness(t, "base\n")
	defer h.closeJournals()

	p := h.Pane()
	h.typeText("X")
	h.press("super+s")
	logPath := filepath.Join(h.journalDir(), journalName(p.File.Path))

	// No session save: the tab is not in the restored set, so paneFor finds
	// nothing and the log stands on its own.
	host := ui.NewFakeHost(120, 12)
	t.Cleanup(func() { host.Close() })
	a := New(host, h.root, 2)
	defer a.closeJournals()
	a.RestoreSession()

	if a.Pane() != nil {
		t.Errorf("a clean leftover log reopened a tab: %q", a.Pane().File.Path)
	}
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Errorf("the clean leftover log still sits in the restore path (err=%v)", err)
	}
}

// A log with unsaved work is crash recovery, not a leftover: restore still
// opens it as a dirty tab carrying the edits the log recorded.
func TestJournalRestoreKeepsUnsavedLog(t *testing.T) {
	t.Setenv(JournalEnv, "1")
	h := newHarness(t, "base\n")
	defer h.closeJournals()

	p := h.Pane()
	h.typeText("X")
	h.flushJournal(p)
	want := p.File.Text()
	logPath := filepath.Join(h.journalDir(), journalName(p.File.Path))

	host := ui.NewFakeHost(120, 12)
	t.Cleanup(func() { host.Close() })
	a := New(host, h.root, 2)
	defer a.closeJournals()
	a.RestoreSession()

	if a.Pane() == nil {
		t.Fatal("a log with unsaved work did not reopen")
	}
	if got := a.Pane().File.Text(); got != want {
		t.Errorf("restored text = %q, want %q", got, want)
	}
	if !a.Pane().File.Dirty() {
		t.Error("a restored unsaved buffer should read dirty")
	}
	if _, err := os.Stat(logPath); err != nil {
		t.Errorf("the log backing a restored tab went missing: %v", err)
	}
}

// A log whose base matches the buffer's origin but whose session was not the
// replay of that log is stale -- the leftover an earlier close used to leave.
// Editing the buffer must archive it and start a fresh log rather than refuse
// to capture for the rest of the buffer's life.
func TestJournalStaleLogIsArchivedAndCaptureResumes(t *testing.T) {
	t.Setenv(JournalEnv, "1")
	h := newHarness(t, "base\n")
	defer h.closeJournals()

	p := h.Pane()
	h.typeText("XY")
	h.flushJournal(p)
	logPath := filepath.Join(h.journalDir(), journalName(p.File.Path))

	// A second app over the same workspace opens the file fresh. Its origin is
	// the disk bytes the log's base names, but its session is not the replay
	// the log recorded, so startTap must not reuse the log.
	host := ui.NewFakeHost(120, 12)
	t.Cleanup(func() { host.Close() })
	a := New(host, h.root, 2)
	defer a.closeJournals()
	a.OpenFile(p.File.Path)

	q := a.Pane()
	f := q.File
	f.Begin()
	f.ApplyDiff(piecetable.User, f.Session().Version(),
		[]piecetable.Hunk{{Start: 0, End: 0, Text: "Z"}})
	f.End()
	a.flushJournal(q)

	if got := a.Status(); !strings.Contains(got, "had a stale op log; archived, capturing fresh") {
		t.Errorf("status = %q, want the stale-log note", got)
	}
	if got := archivedLogs(t, a); len(got) != 1 {
		t.Errorf("archived logs = %d, want the stale one", len(got))
	}
	if a.journals[q.File.Path] == nil {
		t.Fatal("no fresh tap after archiving the stale log")
	}
	l, err := journal.Open(logPath)
	if err != nil {
		t.Fatalf("opening the fresh log: %v", err)
	}
	base, ok := baseOf(l)
	if !ok {
		t.Fatal("the fresh log has no base record")
	}
	if base.Hash != hashBytes([]byte("base\n")) {
		t.Errorf("fresh base hash = %q, want the origin digest", base.Hash)
	}
	sess := buildSession(l)
	if sess == nil {
		t.Fatal("buildSession refused the fresh log")
	}
	if got, want := sess.Buffer().Slice(0, sess.Buffer().Len()), q.File.Text(); got != want {
		t.Errorf("captured text = %q, want %q (capture did not resume)", got, want)
	}
}

// closeJournal must remove the log even when the buffer has no live tap: a
// clean buffer can carry a log from an earlier session, and leaving it makes a
// later reopen hit the stale-log refusal.
func TestJournalCloseRemovesLogWithoutTap(t *testing.T) {
	t.Setenv(JournalEnv, "1")
	h := newHarness(t, "base\n")
	defer h.closeJournals()

	p := h.Pane()
	h.typeText("X")
	h.press("super+s") // save first, so the buffer is clean
	logPath := filepath.Join(h.journalDir(), journalName(p.File.Path))
	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("no log before the close: %v", err)
	}

	// Model the clean buffer that carries a log but has no live tap.
	delete(h.journals, p.File.Path)
	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("log vanished before closeJournal: %v", err)
	}

	h.closeJournal(p)

	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Errorf("closeJournal with no tap left the log on disk (err=%v)", err)
	}
}

// The ordinary restore path still works: a log that matches the restored
// session is reused and appended to, not archived, and capture continues.
func TestJournalRestoredLogIsReusedNotArchived(t *testing.T) {
	t.Setenv(JournalEnv, "1")
	h := newHarness(t, "base\n")
	defer h.closeJournals()

	p := h.Pane()
	h.typeText("X")
	h.flushJournal(p)
	logPath := filepath.Join(h.journalDir(), journalName(p.File.Path))
	// A real restart has the tab in the session, so record it.
	h.sessionTick(time.Now())

	host := ui.NewFakeHost(120, 12)
	t.Cleanup(func() { host.Close() })
	a := New(host, h.root, 2)
	defer a.closeJournals()
	a.RestoreSession()

	q := a.Pane()
	if q == nil {
		t.Fatal("the log did not restore")
	}
	if got := q.File.Text(); got != "Xbase\n" {
		t.Fatalf("restored text = %q, want Xbase\\n", got)
	}
	before := readLog(t, h, q.File.Path)

	// The first edit after a restore goes through startTap with no live tap:
	// the matching log must be reused rather than rotated away.
	f := q.File
	f.Begin()
	f.ApplyDiff(piecetable.User, f.Session().Version(),
		[]piecetable.Hunk{{Start: 0, End: 0, Text: "Y"}})
	f.End()
	a.flushJournal(q)

	if got := archivedLogs(t, a); len(got) != 0 {
		t.Errorf("reuse archived the log: %d entries", len(got))
	}
	if a.journals[q.File.Path] == nil {
		t.Fatal("no tap after editing a restored buffer")
	}
	after, err := journal.Open(logPath)
	if err != nil {
		t.Fatalf("opening the reused log: %v", err)
	}
	sess := buildSession(after)
	if sess == nil {
		t.Fatal("buildSession refused the reused log")
	}
	if got, want := sess.Buffer().Slice(0, sess.Buffer().Len()), q.File.Text(); got != want {
		t.Errorf("reused log text = %q, want %q", got, want)
	}
	if len(after.Records) <= len(before.Records) {
		t.Errorf("records after = %d, want more than %d (capture did not append)",
			len(after.Records), len(before.Records))
	}
}
