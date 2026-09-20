package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"raj/internal/piecetable"
	"raj/internal/store"
)

// persistNow runs the journal tick once, resetting the debounce so a test does
// not wait on a wall clock.
func (h *harness) persistNow() {
	h.journalPersisted = time.Time{}
	h.persistTick(time.Now())
}

// journalRow reads the persisted row for path.
func (h *harness) journalRow(t *testing.T, path string) (store.JournalEntry, bool) {
	t.Helper()
	rec, ok, err := h.state.Journal(path)
	if err != nil {
		t.Fatal(err)
	}
	return rec, ok
}

// A proposed set is not part of the agreed composition, but it is part of the
// view and exactly what a restart has to bring back. Without the journal the
// reopened buffer comes from disk and both the proposed text and the set are
// gone.
func TestJournalRestoresAProposedSetAcrossRestart(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "test.go")
	if err := os.WriteFile(path, []byte("hello world\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	first := newHarnessAt(t, root)
	first.OpenFile(path)
	first.drain()
	if id := propose(t, first, piecetable.Hunk{Start: 6, End: 11, Text: "socket"}); id == 0 {
		t.Fatal("setup: no proposed set")
	}
	first.persistNow()
	rec, ok := first.journalRow(t, path)
	if !ok {
		t.Fatal("a proposed set was not persisted")
	}
	if len(rec.Snapshot) == 0 {
		t.Error("the persisted snapshot is empty")
	}
	if len(rec.Digest) != 32 {
		t.Errorf("digest length = %d, want 32", len(rec.Digest))
	}
	first.CloseState()

	second := newHarnessAt(t, root)
	second.OpenFile(path)
	second.drain()
	p := second.Tabs.Active()
	if p == nil {
		t.Fatal("reopen produced no tab")
	}
	if got := p.File.Text(); got != "hello socket\n" {
		t.Errorf("restored text = %q, want the proposed view", got)
	}
	if got := len(p.File.Session().Pending()); got != 1 {
		t.Errorf("restored pending sets = %d, want 1", got)
	}
	if p.File.IsSnapshot() {
		t.Error("a journal restore must be a writable local buffer, not a snapshot")
	}
}

// A save is the moment disk becomes the state, so the row is deleted and the
// next open is clean. Without the delete the saved bytes would be replaced on
// reopen by the snapshot from just before the save.
func TestJournalSaveClearsTheRow(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "test.go")
	if err := os.WriteFile(path, []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	first := newHarnessAt(t, root)
	first.OpenFile(path)
	first.drain()
	first.typeText("two")
	first.persistNow()
	if _, ok := first.journalRow(t, path); !ok {
		t.Fatal("setup: the dirty buffer was not persisted")
	}
	first.press("super+s")
	if _, ok := first.journalRow(t, path); ok {
		t.Error("a save left the journal row behind")
	}
	first.CloseState()

	second := newHarnessAt(t, root)
	second.OpenFile(path)
	second.drain()
	p := second.Tabs.Active()
	if got := p.File.Text(); got != "twoone\n" {
		t.Errorf("reopened text = %q, want the saved bytes", got)
	}
	if p.File.ViewDirty() {
		t.Error("a reopened saved buffer is dirty")
	}
	if got := len(p.File.Session().Groups()); got != 0 {
		t.Errorf("reopened buffer has %d change sets, want none", got)
	}
}

// A file that changed between the persist and the reopen no longer matches the
// row's digest, so the row is dropped and the disk version opens. Without the
// digest check the stale unsaved text would silently overwrite the new file.
func TestJournalDropsARowWhenTheDiskMoved(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "test.go")
	if err := os.WriteFile(path, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	first := newHarnessAt(t, root)
	first.OpenFile(path)
	first.drain()
	first.typeText("X")
	first.persistNow()
	if _, ok := first.journalRow(t, path); !ok {
		t.Fatal("setup: the dirty buffer was not persisted")
	}
	first.CloseState()

	if err := os.WriteFile(path, []byte("rewritten on disk\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	second := newHarnessAt(t, root)
	second.OpenFile(path)
	second.drain()
	if got := second.Tabs.Active().File.Text(); got != "rewritten on disk\n" {
		t.Errorf("text = %q, want the disk version", got)
	}
	if _, ok := second.journalRow(t, path); ok {
		t.Error("the stale row survived the external rewrite")
	}
}

// The encoding is stored beside the snapshot, so a BOM and CRLF file comes back
// in the shape it was read in rather than as bare UTF-8 LF.
func TestJournalRoundTripsTheEncoding(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "test.go")
	if err := os.WriteFile(path, []byte("\xef\xbb\xbfone\r\ntwo\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	first := newHarnessAt(t, root)
	first.OpenFile(path)
	first.drain()
	if got := first.Tabs.Active().File.Enc; !got.BOM || !got.CRLF {
		t.Fatalf("setup: encoding = %+v, want a BOM and CRLF", got)
	}
	first.typeText("X")
	first.persistNow()
	first.CloseState()

	second := newHarnessAt(t, root)
	second.OpenFile(path)
	second.drain()
	if got := second.Tabs.Active().File.Enc; !got.BOM || !got.CRLF {
		t.Errorf("restored encoding = %+v, want a BOM and CRLF", got)
	}
}

// A client owns no document: every tab is the daemon's snapshot, so a tick must
// never write a local row. The attach guard is the second line of defence behind
// IsSnapshot.
func TestJournalIsNotWrittenForAnAttachedClient(t *testing.T) {
	srv := controlHarness(t, "hello\n")
	ch := attachClient(t, srv)
	ch.cli.drain()
	path := srv.Tabs.Active().File.Path
	ch.cli.persistNow()
	if _, ok := ch.cli.journalRow(t, path); ok {
		t.Error("an attached client wrote a journal row")
	}
}

// Browsing writes nothing: a clean buffer has no row, and a tick leaves it that
// way. Without the dirty gate the tick would snapshot every open file.
func TestJournalWritesNothingForACleanBuffer(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "test.go")
	if err := os.WriteFile(path, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := newHarnessAt(t, root)
	h.OpenFile(path)
	h.drain()
	h.persistNow()
	if _, ok := h.journalRow(t, path); ok {
		t.Error("a clean buffer wrote a journal row")
	}
}
