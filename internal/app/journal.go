package app

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"time"

	"raj/internal/control"
	"raj/internal/editor"
	"raj/internal/journal"
	"raj/internal/piecetable"
	"raj/internal/session"
)

// Op-log capture and restore.
//
// This is the spike that lets unsaved buffers survive a restart. It is behind
// an env gate and off by default, so a normal run behaves exactly as before:
// every entry point checks journalEnabled first and nothing below touches the
// filesystem otherwise.
//
// The split of responsibility is deliberate. piecetable owns the document and
// the journal of ops that produced it; internal/journal owns the on-disk
// format; this file is the only place that knows both, converting one to the
// other at the seam and deciding when to pull. Writing on the keystroke path is
// what the whole design avoids, so capture happens on the idle tick, and only
// save, close and quit force a flush.

// JournalEnv gates the whole feature. It is unset (or a recognized "off" word)
// in a normal run; anything else turns capture and restore on.
const JournalEnv = "RAJ_JOURNAL"

// JournalInterval bounds how often a dirty buffer's log is appended while the
// editor is idle. It matches the session debounce in spirit: a crash loses at
// most this much, and nothing lands on a keystroke. The appends are ordinary
// writes; fsync happens only on the explicit flush.
const JournalInterval = time.Second

// journalEnabled reports whether capture and restore are on for this process.
// It reads the environment each time rather than caching at startup, so a test
// can turn the gate on and off around a call.
func journalEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(JournalEnv))) {
	case "", "0", "false", "off", "no":
		return false
	}
	return true
}

// logTap is one buffer's connection to its log file: the writer, and how far
// the log has been told about. version and store are the persisted frontier,
// and state is the decisions already recorded, so a flush appends only what is
// new. All three are read and written on the event thread only.
type logTap struct {
	w       *journal.Writer
	version piecetable.Version
	store   map[uint8]int
	state   map[uint64]piecetable.GroupState
	authors map[uint8]bool
}

// journalTick appends any new ops and store growth for the dirty buffers. It is
// the debounced pull the design asks for: it runs on the idle tick, snapshots
// the session on this thread, and never fsyncs, because a tick is not a
// commitment.
func (a *App) journalTick(now time.Time) {
	if !journalEnabled() || a.root == "" || a.NoRestore {
		return
	}
	if !a.journalSaved.IsZero() && now.Sub(a.journalSaved) < JournalInterval {
		return
	}
	a.journalSaved = now
	for _, p := range a.Tabs.Dirty() {
		a.appendJournal(p)
	}
}

// journalDir is the directory the per-buffer logs live under, beside the
// session file that shares the same scratch-state directory.
func (a *App) journalDir() string {
	if a.root == "" {
		return ""
	}
	return filepath.Join(session.Dir(a.root), "logs")
}

// journalName is the log file for a buffer path. A short digest makes it unique
// and filesystem-safe whatever the path holds; the base name is kept in front
// so a logs directory can be read by eye. The authoritative path is the one in
// the log's Base record, so this is a convenience rather than the key.
func journalName(path string) string {
	sum := sha256.Sum256([]byte(path))
	base := filepath.Base(path)
	if base == "" || base == "." || base == string(filepath.Separator) {
		base = "buffer"
	}
	return base + "-" + hex.EncodeToString(sum[:8]) + ".log"
}

// archiveLog moves a log that no longer describes a buffer — its base hash
// does not match the disk, or it cannot be read at all — out of the restore
// path and into logs/archive, timestamped so repeated rotations do not
// overwrite each other. The log is kept for inspection, never deleted. It
// returns the archived path, or "" when the move failed and the caller must not
// fall back to appending.
func (a *App) archiveLog(logPath string) string {
	dir := filepath.Join(a.journalDir(), "archive")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		a.status = "journal: " + err.Error()
		return ""
	}
	name := filepath.Base(logPath)
	ext := filepath.Ext(name)
	stamp := time.Now().UTC().Format("20060102T150405.000000000")
	dst := filepath.Join(dir, strings.TrimSuffix(name, ext)+"."+stamp+ext)
	if err := os.Rename(logPath, dst); err != nil {
		a.status = "journal: " + err.Error()
		return ""
	}
	return dst
}

// appendJournal writes whatever the buffer has done since its log was last
// told: store growth first, then the ops, then any changed decisions. The order
// is not cosmetic -- an op's inserted pieces address bytes in a store, so the
// bytes have to be in the log before the op that points at them.
//
// It creates the log on the first call, when the buffer is dirty. A buffer that
// is clean and has no log is left alone, so browsing files writes nothing.
func (a *App) appendJournal(p *editor.Pane) {
	if !journalEnabled() || a.root == "" || a.NoRestore || p == nil || p.File.Path == "" {
		return
	}
	tap := a.journals[p.File.Path]
	if tap == nil {
		if !p.File.Dirty() {
			return
		}
		tap = a.startTap(p)
		if tap == nil {
			return
		}
	}
	if err := a.appendTap(tap, p); err != nil {
		a.status = "journal: " + err.Error()
	}
}

// startTap opens or creates the log for a buffer's path and returns the
// frontier it already holds, so the next append knows what is new. An existing
// log whose frontier does not line up with the live session is refused rather
// than appended to (it was not replayed into this buffer, so appending would
// misaddress the store). A log whose base hash does not match the buffer's
// origin is rotated out of the way first and a fresh one created against the
// current bytes, because its ops no longer describe this document.
func (a *App) startTap(p *editor.Pane) *logTap {
	path := p.File.Path
	store := p.File.Session().Store()
	orig := store.Slice(piecetable.Original, 0, store.Len(piecetable.Original))
	want := hashBytes(orig)

	logPath := filepath.Join(a.journalDir(), journalName(path))
	l, lerr := journal.Open(logPath)
	if lerr != nil && !os.IsNotExist(lerr) {
		// The log exists but cannot be read — an older format, a foreign file
		// or a corrupt header. It cannot describe this buffer, so rotate it out
		// of the way and fall through to a fresh base below.
		if a.archiveLog(logPath) == "" {
			return nil
		}
	}
	if lerr == nil && len(l.Records) > 0 {
		base, ok := baseOf(l)
		if !ok {
			a.status = "journal: " + path + " has no base record; not capturing"
			return nil
		}
		if base.Hash == want {
			tap := tapFromLog(l)
			if !tap.matches(p.File.Session()) {
				a.status = path + " has an op log that was not restored; not capturing"
				return nil
			}
			if l.Damaged {
				if err := l.Truncate(); err != nil {
					a.status = "journal: " + err.Error()
					return nil
				}
			}
			w, err := journal.OpenWriter(logPath)
			if err != nil {
				a.status = "journal: " + err.Error()
				return nil
			}
			tap.w = w
			a.putTap(path, tap)
			return tap
		}
		// The log describes bytes this buffer no longer has. Rotate it out of
		// the way and lay a fresh base under the current origin below, rather
		// than appending ops into a frame that never saw them.
		if a.archiveLog(logPath) == "" {
			return nil
		}
	}

	// Fresh log. Create the directory and the file, then write the base the
	// ops will be recorded against.
	if err := os.MkdirAll(a.journalDir(), 0o700); err != nil {
		a.status = "journal: " + err.Error()
		return nil
	}
	w, err := journal.Create(logPath, journal.Header{Root: a.root, Identity: a.localIdentity()})
	if err != nil {
		a.status = "journal: " + err.Error()
		return nil
	}
	if err := w.Append(journal.Base{Path: path, Hash: want, Bytes: append([]byte(nil), orig...)}); err != nil {
		_ = w.Close()
		a.status = "journal: " + err.Error()
		return nil
	}
	tap := &logTap{
		w:       w,
		version: 0,
		store:   map[uint8]int{uint8(piecetable.Original): len(orig)},
		state:   map[uint64]piecetable.GroupState{},
		authors: map[uint8]bool{},
	}
	a.putTap(path, tap)
	return tap
}

// putTap records a tap for a path, creating the map on first use.
func (a *App) putTap(path string, tap *logTap) {
	if a.journals == nil {
		a.journals = map[string]*logTap{}
	}
	a.journals[path] = tap
}

// appendTap writes a buffer's new store blobs, ops and decisions into its log.
// An author row is written before the first thing that names the id, so a
// restored op has an identity and kind to resolve to.
func (a *App) appendTap(t *logTap, p *editor.Pane) error {
	sess := p.File.Session()
	store := sess.Store()
	for id := 0; id < store.Authors(); id++ {
		author := piecetable.Author(id)
		from := t.store[uint8(id)]
		if n := store.Len(author); n > from {
			if err := a.recordAuthor(t, uint8(id)); err != nil {
				return err
			}
			if err := t.w.Append(journal.StoreAppend{
				Author: uint8(id),
				Start:  uint64(from),
				Blob:   append([]byte(nil), store.Slice(author, from, n-from)...),
			}); err != nil {
				return err
			}
			t.store[uint8(id)] = n
		}
	}
	for _, o := range sess.OpsSince(t.version) {
		if err := a.recordOpAuthors(t, o); err != nil {
			return err
		}
		if err := t.w.Append(toJournalOp(o)); err != nil {
			return err
		}
		t.version = o.Seq + 1
	}
	for _, g := range sess.Groups() {
		got, known := t.state[g.ID]
		if known && got == g.State {
			continue
		}
		if !known && g.State == piecetable.Accepted {
			continue // accepted is the default; nothing to record
		}
		if err := t.w.Append(journal.Decision{Group: g.ID, State: journal.GroupState(g.State)}); err != nil {
			return err
		}
		t.state[g.ID] = g.State
	}
	return nil
}

// recordOpAuthors writes a row for every author an op names: the writer, and
// the store buffers its pieces address, because an undo by one author can
// delete text another author owns.
func (a *App) recordOpAuthors(t *logTap, o piecetable.Op) error {
	if err := a.recordAuthor(t, uint8(o.Author)); err != nil {
		return err
	}
	for _, p := range o.Del {
		if err := a.recordAuthor(t, uint8(p.Buf)); err != nil {
			return err
		}
	}
	for _, p := range o.Ins {
		if err := a.recordAuthor(t, uint8(p.Buf)); err != nil {
			return err
		}
	}
	return nil
}

// recordAuthor appends one Author row the first time an id is seen, when the
// registry knows who it is. Author 0 is the file as loaded, not a person.
func (a *App) recordAuthor(t *logTap, id uint8) error {
	if id == uint8(piecetable.Original) || t.authors[id] {
		return nil
	}
	rec, ok := a.authorRecord(id)
	if !ok {
		return nil
	}
	if err := t.w.Append(rec); err != nil {
		return err
	}
	if t.authors == nil {
		t.authors = map[uint8]bool{}
	}
	t.authors[id] = true
	return nil
}

// authorRecord is the journal row for a live participant, or false when there
// is no registry to ask.
func (a *App) authorRecord(id uint8) (journal.Author, bool) {
	if a.control == nil || a.control.Participants == nil {
		return journal.Author{}, false
	}
	p, ok := a.control.Participants.Get(id)
	if !ok {
		return journal.Author{}, false
	}
	kind := journal.Agent
	if p.Kind == control.KindHuman {
		kind = journal.Human
	}
	return journal.Author{ID: id, Identity: p.Identity, Name: p.Name, Kind: kind}, true
}

// localIdentity is the identity the log header carries. The registry names the
// local human; without a listener there is still a local writer.
func (a *App) localIdentity() string {
	if a.control != nil && a.control.Participants != nil {
		if p, ok := a.control.Participants.Get(control.LocalHuman); ok && p.Identity != "" {
			return p.Identity
		}
	}
	return "local"
}

// matches reports whether a log's frontier is exactly where a live session is:
// the same version and the same bytes in each author's store. If it is not, the
// session was not built by replaying this log, and merging new ops into it
// would point pieces at the wrong offsets.
func (t *logTap) matches(sess *piecetable.Session) bool {
	if t.version != sess.Version() {
		return false
	}
	for a := 0; a < sess.Store().Authors(); a++ {
		if sess.Store().Len(piecetable.Author(a)) != t.store[uint8(a)] {
			return false
		}
	}
	return true
}

// recordWritten appends the digest of the bytes a save just put on disk and the
// session version they were written at; the caller's flush fsyncs it. It is
// additive: the origin base stays, because history is kept, and restore accepts
// a log whose disk matches either.
func (a *App) recordWritten(p *editor.Pane) {
	if !journalEnabled() || a.root == "" || a.NoRestore || p == nil || p.File.Path == "" {
		return
	}
	a.appendJournal(p)
	t := a.journals[p.File.Path]
	if t == nil {
		return
	}
	if err := t.w.Append(journal.Written{
		Path:    p.File.Path,
		Hash:    digestOf(p.File.SavedDigest()),
		Version: uint64(p.File.SavedVersion()),
	}); err != nil {
		a.status = "journal: " + err.Error()
	}
}

// flushJournal catches the log up and fsyncs it. It is the durable form, for
// save, close and quit; the idle tick appends without it.
func (a *App) flushJournal(p *editor.Pane) {
	a.appendJournal(p)
	if p == nil || p.File.Path == "" {
		return
	}
	if t := a.journals[p.File.Path]; t != nil {
		if err := t.w.Flush(); err != nil {
			a.status = "journal: " + err.Error()
		}
	}
}

// flushJournals fsyncs every open log, for a caller that is not quitting.
func (a *App) flushJournals() {
	if !journalEnabled() {
		return
	}
	for _, p := range a.Tabs.All() {
		a.flushJournal(p)
	}
}

// closeJournal flushes and closes one buffer's log. The file is left on disk: a
// closed dirty buffer's edits may still be worth restoring, and the log is what
// carries them.
func (a *App) closeJournal(p *editor.Pane) {
	if !journalEnabled() || p == nil || p.File.Path == "" {
		return
	}
	a.appendJournal(p)
	t := a.journals[p.File.Path]
	if t == nil {
		return
	}
	if err := t.w.Close(); err != nil {
		a.status = "journal: " + err.Error()
	}
	delete(a.journals, p.File.Path)
}

// closeJournals flushes and closes every open log, on quit.
func (a *App) closeJournals() {
	for path, t := range a.journals {
		if t.w != nil {
			_ = t.w.Close()
		}
		delete(a.journals, path)
	}
}

// restoreJournals rebuilds dirty buffers from their logs at startup. It runs
// from RestoreSession and replaces the clean File a tab was opened with when a
// log for that path matches its base.
func (a *App) restoreJournals() {
	if !journalEnabled() || a.root == "" || a.NoRestore {
		return
	}
	entries, err := os.ReadDir(a.journalDir())
	if err != nil {
		return // no logs yet: the common case
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		a.restoreLog(filepath.Join(a.journalDir(), e.Name()))
	}
}

// restoreLog tries to turn one log back into a dirty tab. Every refusal is
// quiet except the base mismatch, which is the one the user has to know about:
// it means the file moved under the editor, so the log's ops no longer describe
// it. The log is archived for inspection rather than deleted, and the clean
// file stands; a later edit starts a fresh base against the current bytes.
func (a *App) restoreLog(logPath string) {
	l, err := journal.Open(logPath)
	if err != nil {
		// A log this build cannot read — an older format, a foreign file or a
		// corrupt header — is rotated out of the restore path and kept. A log
		// that vanished between the directory walk and this open is left alone.
		if !os.IsNotExist(err) {
			a.archiveLog(logPath)
		}
		return
	}
	if l.Damaged {
		return
	}
	base, ok := baseOf(l)
	if !ok {
		return
	}
	a.collectAuthors(l)
	p := a.paneFor(base.Path)
	if p == nil {
		opened, err := a.Tabs.Open(base.Path)
		if err != nil {
			return // deleted, binary or too large: nothing to attach the log to
		}
		a.settle(opened)
		p = opened
	}
	store := p.File.Session().Store()
	orig := store.Slice(piecetable.Original, 0, store.Len(piecetable.Original))
	disk := hashBytes(orig)
	mark, wrote := lastWritten(l)
	// The disk is ours if it matches the origin the log started from, or the
	// bytes the last save wrote: history is kept, so a save does not truncate
	// the log, and the disk after a save is a log to restore, not one to skip.
	// Neither match means the file moved under the editor: the log's ops no
	// longer describe this document, so rotate it out of the restore path and
	// let the clean file stand. A later edit starts a fresh base.
	if disk != base.Hash && (!wrote || disk != mark.Hash) {
		archived := a.archiveLog(logPath)
		a.status = filepath.Base(base.Path) + ": unsaved changes were not restored because the file changed on disk"
		if archived != "" {
			a.status += " (op log archived to " + archived + ")"
		}
		return
	}
	sess := buildSession(l)
	if sess == nil {
		return
	}
	// A disk matching the last write is a buffer that can baseline clean at the
	// version those bytes were written at, rather than dirty against the origin.
	saved := editor.RestoredWrite{}
	if wrote && disk == mark.Hash {
		saved = editor.RestoredWrite{
			Content: savedContent(base, sess, int(mark.Version)),
			Version: piecetable.Version(mark.Version),
			Disk:    digestBytes(mark.Hash),
			OK:      true,
		}
	}
	p.File = editor.NewRestoredFile(base.Path, sess, a.tabWidth, saved)
	p.File.SetDark(a.host.Theme().Dark())
	if at := p.Cursors.Primary().Head; at > p.File.Len() {
		p.Cursors.Set(p.File.Len(), p.File.Len())
	}
	a.TouchSession()
}

// paneFor returns the open tab for a path, if any.
func (a *App) paneFor(path string) *editor.Pane {
	for _, p := range a.Tabs.All() {
		if p.File.Path == path {
			return p
		}
	}
	return nil
}

// collectAuthors stashes a log's author table for the control registry, which
// StartControl builds after RestoreSession has already run. The ids are
// explicit, so a restored op's author resolves to the identity, name and kind
// it was written under rather than a fresh join order.
func (a *App) collectAuthors(l *journal.Log) {
	for _, r := range l.Records {
		row, ok := r.(journal.Author)
		if !ok || row.ID == uint8(piecetable.Original) {
			continue
		}
		a.restoredAuthors = append(a.restoredAuthors, control.Participant{
			ID:       row.ID,
			Identity: row.Identity,
			Name:     row.Name,
			Kind:     participantKind(row.Kind),
		})
	}
}

// participantKind maps a persisted kind to the live registry's.
func participantKind(k journal.ParticipantKind) control.Kind {
	if k == journal.Human {
		return control.KindHuman
	}
	return control.KindAgent
}

// seedParticipants installs the author table read from the logs into the
// control registry, once it exists. A row for an id already present is left
// alone, so the local human is never overwritten.
func (a *App) seedParticipants() {
	if a.control == nil || a.control.Participants == nil {
		return
	}
	for _, p := range a.restoredAuthors {
		a.control.Participants.Seed(p)
	}
}

// buildSession turns a scanned log into a live session: a document over the
// base, the store blobs appended in order, then the ops replayed. It returns
// nil for a log whose appends do not line up, rather than building a document
// that would read the wrong bytes.
func buildSession(l *journal.Log) *piecetable.Session {
	base, ok := baseOf(l)
	if !ok {
		return nil
	}
	doc := piecetable.NewDoc(string(base.Bytes), 0)
	var ops []piecetable.Op
	decisions := map[uint64]piecetable.GroupState{}
	for _, r := range l.Records {
		switch v := r.(type) {
		case journal.StoreAppend:
			if int(v.Start) != doc.Store().Len(piecetable.Author(v.Author)) {
				return nil
			}
			doc.Store().Append(piecetable.Author(v.Author), v.Blob)
		case journal.Op:
			ops = append(ops, fromJournalOp(v))
		case journal.Decision:
			decisions[v.Group] = piecetable.GroupState(v.State)
		}
	}
	for i, o := range ops {
		if int(o.Seq) != i {
			return nil
		}
	}
	return piecetable.NewRestoredSession(doc, ops, decisions, len(base.Bytes))
}

// savedContent reconstructs the text a restored session held at version at, for
// baselining a buffer whose disk matches a write recorded there. A session with
// no ops past the write is already at that text; otherwise the op prefix is
// replayed over a copy of the store, so the baseline digest names the exact
// bytes that were written rather than a later composition.
func savedContent(base journal.Base, sess *piecetable.Session, at int) string {
	if at >= int(sess.Version()) {
		return sess.Buffer().Slice(0, sess.Buffer().Len())
	}
	doc := piecetable.NewDoc(string(base.Bytes), 0)
	store := sess.Store()
	for a := 1; a < store.Authors(); a++ {
		author := piecetable.Author(a)
		if n := store.Len(author); n > 0 {
			doc.Store().Append(author, append([]byte(nil), store.Slice(author, 0, n)...))
		}
	}
	prefix := piecetable.NewRestoredSession(doc, sess.Journal()[:at], nil, len(base.Bytes))
	return prefix.Buffer().Slice(0, prefix.Buffer().Len())
}

// tapFromLog rebuilds a tap's frontier from a scanned log: the version after
// the last op, each author's persisted byte count, and the decisions already
// recorded. The base's bytes count for the origin store, which no StoreAppend
// carries.
func tapFromLog(l *journal.Log) *logTap {
	t := &logTap{
		version: 0,
		store:   map[uint8]int{},
		state:   map[uint64]piecetable.GroupState{},
		authors: map[uint8]bool{},
	}
	for _, r := range l.Records {
		switch v := r.(type) {
		case journal.Base:
			t.store[uint8(piecetable.Original)] = len(v.Bytes)
		case journal.StoreAppend:
			if end := int(v.Start) + len(v.Blob); end > t.store[v.Author] {
				t.store[v.Author] = end
			}
		case journal.Op:
			if next := piecetable.Version(v.Seq) + 1; next > t.version {
				t.version = next
			}
		case journal.Decision:
			t.state[v.Group] = piecetable.GroupState(v.State)
		case journal.Author:
			t.authors[v.ID] = true
		}
	}
	return t
}

// baseOf returns the log's origin record. A log without one is not something
// this spike can restore, so the caller leaves it alone.
func baseOf(l *journal.Log) (journal.Base, bool) {
	for _, r := range l.Records {
		if b, ok := r.(journal.Base); ok {
			return b, true
		}
	}
	return journal.Base{}, false
}

// lastWritten returns the most recent Written marker and whether the log has
// one. It is the second half of the restore check: a save does not truncate the
// log, so the disk matching the last write is a log to restore, not one to
// skip, and the marker's version lets the restored buffer baseline clean.
func lastWritten(l *journal.Log) (journal.Written, bool) {
	var mark journal.Written
	found := false
	for _, r := range l.Records {
		if w, ok := r.(journal.Written); ok {
			mark = w
			found = true
		}
	}
	return mark, found
}

// hashBytes is the digest a Base or Written record carries.
func hashBytes(b []byte) string {
	return digestOf(sha256.Sum256(b))
}

// digestOf renders a SHA-256 sum in the form the log's hashes carry. The prefix
// names the algorithm so a later format can change it and a reader can tell.
func digestOf(sum [sha256.Size]byte) string {
	return "sha256:" + hex.EncodeToString(sum[:])
}

// digestBytes decodes the "sha256:<hex>" form digestOf writes back to its sum,
// so a restored File's SavedDigest names the same bytes the log recorded.
func digestBytes(s string) [sha256.Size]byte {
	var sum [sha256.Size]byte
	h, err := hex.DecodeString(strings.TrimPrefix(s, "sha256:"))
	if err == nil && len(h) == sha256.Size {
		copy(sum[:], h)
	}
	return sum
}

// toJournalOp converts a live op to its persisted form.
func toJournalOp(o piecetable.Op) journal.Op {
	return journal.Op{
		Seq:    uint64(o.Seq),
		Author: uint8(o.Author),
		Pos:    uint64(o.Pos),
		Del:    toJournalPieces(o.Del),
		Ins:    toJournalPieces(o.Ins),
		Undoes: uint64(o.Undoes),
		Group:  o.Group,
		Kind:   journal.OpKind(o.Kind),
	}
}

// fromJournalOp converts a persisted op back to the live form.
func fromJournalOp(o journal.Op) piecetable.Op {
	return piecetable.Op{
		Seq:    piecetable.Version(o.Seq),
		Author: piecetable.Author(o.Author),
		Pos:    int(o.Pos),
		Del:    fromJournalPieces(o.Del),
		Ins:    fromJournalPieces(o.Ins),
		Undoes: piecetable.Version(o.Undoes),
		Group:  o.Group,
		Kind:   piecetable.OpKind(o.Kind),
	}
}

func toJournalPieces(recs []piecetable.PieceRec) []journal.Piece {
	if len(recs) == 0 {
		return nil
	}
	out := make([]journal.Piece, len(recs))
	for i, r := range recs {
		out[i] = journal.Piece{Buf: uint8(r.Buf), Start: uint64(r.Start), Length: uint64(r.Length)}
	}
	return out
}

func fromJournalPieces(ps []journal.Piece) []piecetable.PieceRec {
	if len(ps) == 0 {
		return nil
	}
	out := make([]piecetable.PieceRec, len(ps))
	for i, p := range ps {
		out[i] = piecetable.PieceRec{Buf: int(p.Buf), Start: int(p.Start), Length: int(p.Length)}
	}
	return out
}
