package app

import (
	gobytes "bytes"
	"encoding/json"
	"path/filepath"
	"time"

	"raj/internal/editor"
	"raj/internal/piecetable"
	"raj/internal/store"
)

// Store-backed journal persistence: each locally dirty buffer's exact session
// is snapshotted into the workspace store, so a restart reopens it with its
// proposals and unsaved text. The codec is piecetable's exact-session snapshot
// (SnapshotState/Encode/DecodeSnapshot), the same one the client wire carries,
// so what comes back is indistinguishable from the session that was running.
//
// This is the companion to the op-log spike in journal.go, but it persists the
// whole session rather than the ops that built it, and it is not behind an
// environment gate: an unsaved buffer is worth restoring on every run.
//
// The rules:
//   - the idle tick writes a dirty local buffer, debounced, and only when the
//     buffer has moved since its last write;
//   - a successful save deletes the row, because disk is the state then;
//   - the open path restores a row only when the digest of the bytes just read
//     still matches, so a file that changed underneath the journal opens clean.

// JournalPersistInterval bounds how often a dirty buffer's snapshot is written
// while the editor is idle. It matches the session debounce in spirit: a crash
// loses at most this much, and nothing lands on a keystroke.
const JournalPersistInterval = time.Second

// journalStamp is the change token a persisted row records: the session version
// and the decision generation. Either moving means the session moved and the
// row is stale. The version alone would miss a decision, which changes the
// agreed composition and the view without appending an op.
type journalStamp struct {
	version piecetable.Version
	gen     uint64
}

// persistTick writes the session snapshot of every dirty local buffer,
// debounced and only when the buffer has moved since its last write. It runs on
// the idle tick, where the encode cannot land on a keystroke.
func (a *App) persistTick(now time.Time) {
	if a.attach || a.roots.Len() == 0 || a.NoRestore || a.state == nil {
		return
	}
	if !a.journalPersisted.IsZero() && now.Sub(a.journalPersisted) < JournalPersistInterval {
		return
	}
	a.journalPersisted = now
	for _, p := range a.Tabs.All() {
		a.persistPane(p)
	}
}

// persistPane persists one buffer, or drops a remembered row once the buffer is
// clean again. A proposal alone leaves the agreed composition equal to disk, so
// the view is the test: a buffer showing a proposed set is exactly what a
// restart has to bring back.
func (a *App) persistPane(p *editor.Pane) {
	if p == nil {
		return
	}
	path := p.File.Path
	if path == "" || p.File.IsSnapshot() || isGitPath(path) {
		return
	}
	if !p.File.ViewDirty() {
		if _, ok := a.journalWritten[path]; ok {
			a.dropJournal(path)
		}
		return
	}
	stamp := journalStamp{version: p.File.Session().Version(), gen: p.File.DecisionGeneration()}
	if prev, ok := a.journalWritten[path]; ok && prev == stamp {
		return
	}
	snap, err := p.File.Session().SnapshotState()
	if err != nil {
		a.status = "journal: " + err.Error()
		return
	}
	encoded, err := snap.Encode()
	if err != nil {
		a.status = "journal: " + err.Error()
		return
	}
	enc, err := json.Marshal(p.File.Enc)
	if err != nil {
		a.status = "journal: " + err.Error()
		return
	}
	digest := p.File.SavedDigest()
	if err := a.state.PutJournal(store.JournalEntry{
		Path:     path,
		Snapshot: encoded,
		Saved:    uint64(p.File.SavedVersion()),
		Encoding: enc,
		Digest:   digest[:],
	}); err != nil {
		a.status = "journal: " + err.Error()
		return
	}
	a.journalWritten[path] = stamp
}

// restoreJournal replaces a freshly read File with the stored session when the
// row's disk digest still matches the bytes just read. A mismatch means the
// file moved under the journal, so the row is dropped and the disk open stands.
// The restored buffer is a normal local file: Save works, and its dirty state
// is measured against the disk.
func (a *App) restoreJournal(f *editor.File, tab int) *editor.File {
	if a.state == nil || a.attach || f == nil || f.Path == "" {
		return f
	}
	rec, ok, err := a.state.Journal(f.Path)
	if err != nil || !ok {
		return f
	}
	digest := f.SavedDigest()
	if !gobytes.Equal(rec.Digest, digest[:]) {
		a.dropJournal(f.Path)
		a.journalNote = "journal: " + filepath.Base(f.Path) + " changed on disk; opening the disk version"
		return f
	}
	var enc editor.Encoding
	if err := json.Unmarshal(rec.Encoding, &enc); err != nil {
		a.dropJournal(f.Path)
		a.journalNote = "journal: " + err.Error()
		return f
	}
	restored, err := editor.RestoreJournal(f.Path, rec.Snapshot, enc, tab, piecetable.Version(rec.Saved), f.Text(), digest)
	if err != nil {
		a.dropJournal(f.Path)
		a.journalNote = "journal: " + err.Error()
		return f
	}
	a.journalWritten[f.Path] = journalStamp{version: restored.Session().Version(), gen: restored.DecisionGeneration()}
	return restored
}

// loadFile is the Tabs loader for the local open path: read from disk, then
// restore a matching journal row over it. Every tab and preview open shares it,
// so the restore rule is stated once.
func (a *App) loadFile(path string, tab int) (*editor.File, error) {
	f, err := editor.Open(path, tab)
	if err != nil {
		return nil, err
	}
	return a.restoreJournal(f, tab), nil
}

// journalStatus overlays a pending journal note on the status a caller just
// set, so a row dropped as stale or unreadable is not erased by the open that
// followed it. It clears the note either way, so it shows once.
func (a *App) journalStatus(status string) string {
	if a.journalNote == "" {
		return status
	}
	note := a.journalNote
	a.journalNote = ""
	return note
}

// dropJournal removes a persisted row and forgets it, best-effort: disk is the
// state once a buffer is saved, and a stale row is worse than none.
func (a *App) dropJournal(path string) {
	if path == "" {
		return
	}
	delete(a.journalWritten, path)
	if a.state == nil {
		return
	}
	if err := a.state.DeleteJournal(path); err != nil {
		a.status = "journal: " + err.Error()
	}
}
