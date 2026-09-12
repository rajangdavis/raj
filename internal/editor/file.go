// Package editor is the editing pane: an open file, its cursors, and the
// actions that mutate it. It renders into a ui.Screen and never touches a
// terminal, so the whole package tests headlessly.
package editor

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"raj/internal/piecetable"
	"raj/internal/syntax"
	"raj/internal/view"
)

// File is one open document. It owns the piece-table session and keeps the
// line index in step with it.
//
// Every mutation goes through here rather than through the session directly,
// because the index has to be updated from the same op that changed the text.
// Letting a pane edit the session behind the file's back is how line numbers
// silently drift.
type File struct {
	Path string
	Cols view.Columns

	// Hints are the inline hints the language server returned for the text
	// this file had when they were installed, trimmed to the lines whose whole
	// line, hints included, fits the pane text width the set was built for.
	// They are installed and cleared on the event thread, so there is no lock;
	// nil means no hints, which draws nothing rather than an empty overlay.
	Hints *HintSet

	// hintWidth is the text width the installed Hints were filtered for.
	// LineCol and OffsetAt read Hints without knowing the pane, so the
	// application refilters from the full answer when the pane width changes;
	// this is how it tells that the installed set is stale.
	hintWidth int

	// Indent is what one press of Tab inserts, resolved at open from the
	// format, the content and the language in that order. indentFrom records
	// which of them answered, so a weaker default cannot contradict it.
	Indent     Indent
	indentFrom IndentSource

	Syntax *syntax.Highlighter
	sess   *piecetable.Session
	idx    *view.Index

	// saved is the version at the last write, and savedLen/savedSum describe
	// what was written. The version alone cannot answer "is this file
	// changed?", because undo does not rewind the version — it appends the
	// reversing ops — so a buffer edited and put back stayed marked dirty and
	// asked to be saved on close. The digest is what makes "changed" mean
	// changed content rather than changed history.
	saved    piecetable.Version
	savedLen int
	savedSum [sha256.Size]byte

	// savedDisk is the digest of the exact bytes the last write put on disk,
	// encoding included, so the op log can record what a save wrote and a
	// restart can tell a disk that matches it from one that has moved.
	savedDisk [sha256.Size]byte

	// savedGen is decisionGen at the last write, so a save can be trusted
	// clean across frames while a decision that follows it cannot be: a
	// decision moves the agreed composition without moving the version.
	savedGen uint64
	// decisionGen advances on every decision made through this File.
	decisionGen uint64
	// layered records that a change set has been proposed or rejected here,
	// which is the only case where the view and the agreed composition differ.
	layered bool

	// cleanVer memoises the last content comparison, because Dirty is asked
	// once per tab per frame and the comparison reads the whole document.
	// cleanGen is decisionGen at that comparison, so a decision forces a fresh
	// one even though the version did not move.
	cleanVer  piecetable.Version
	cleanGen  uint64
	cleanKnow bool
	cleanDirt bool

	// dark is the background the highlighter was built for. Kept so a rename
	// can rebuild it without asking the application which terminal it is in.
	dark bool

	// Enc is how the file's bytes are shaped around the text — line endings
	// and byte order mark — so a save reproduces what was opened.
	Enc Encoding

	// disk is what the file looked like the last time raj read or wrote it,
	// so a save can refuse to clobber another writer's work.
	disk stamp

	// applied is the version whose ops have been mirrored into the line index.
	applied piecetable.Version
	newline piecetable.PieceRec

	// nlBuf is scratch for applyToIndex: the offsets of the newlines in one
	// insertion. Reused so a paste does not allocate one per op.
	nlBuf []int
}

// Open reads a file from disk. A missing file is not an error — it opens empty,
// the way an editor should when you name a file you intend to create.
//
// Binary files and oversized files are refused. Opening a binary as text used
// to put its bytes on screen, where a stray ESC was executed by the terminal
// rather than displayed; the renderer no longer emits raw control bytes, but
// declining is still the right answer, because such a buffer cannot be edited
// or saved without corrupting the file.
func Open(path string, tab int) (*File, error) {
	if info, err := os.Stat(path); err == nil {
		if info.IsDir() {
			return nil, ErrIsDir
		}
		if info.Size() > MaxFileSize {
			return nil, ErrTooLarge
		}
	}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if IsBinary(string(data)) {
		return nil, ErrBinary
	}
	// Decode before the file is built: indent detection, the line index and
	// the highlighter should all see the text, not the encoding.
	text, enc := decode(string(data))
	f := NewFile(path, text, tab)
	f.Enc = enc
	f.savedDisk = sha256.Sum256(data) // the bytes actually on disk, encoding included
	f.stampDisk()                     // what was just read is what raj knows about
	return f, nil
}

// NewFile wraps content that is already in memory.
func NewFile(path, content string, tab int) *File {
	// A tab still has to be drawn at some width, and the configured one is the
	// only preference there is about that, so it rides along as the fallback.
	style, from := IndentFor(path, content, Indent{Width: tab})
	f := &File{
		Path:       path,
		Indent:     style,
		indentFrom: from,
		Cols:       view.NewColumns(tab),
		Syntax:     syntax.New(path, true),
		dark:       true,
		sess:       piecetable.NewSession(piecetable.NewDoc(content, 0)),
		idx:        view.NewIndex(content),
	}
	f.markSaved(content) // what was opened is what is on disk
	return f
}

// RestoredWrite is the last write a restored buffer's log recorded: Content is
// the exact text that was written, Version the session version it was written
// at, and Disk the digest of the encoded bytes. A zero value — OK false — means
// the log has no usable marker, and the buffer baselines on its origin instead.
type RestoredWrite struct {
	Content string
	Version piecetable.Version
	Disk    [sha256.Size]byte
	OK      bool
}

// NewRestoredFile wraps a session rebuilt from a persisted journal. The session
// already holds the document — base plus replayed ops — so the line index and
// the highlighter are built from it and applied starts at its version, which is
// what makes a restored dirty buffer render correctly on the first frame.
//
// The clean baseline is the last write the log recorded when wrote.OK is set —
// the session version the bytes were written at, so a buffer restored from a log
// whose disk matches that write reads clean rather than dirty. Otherwise the
// baseline is the log's base, not what the session currently holds: the origin
// store is the file as it was when logging began, so a buffer whose log contains
// no ops reads as clean and one whose ops changed the text reads as dirty. A
// restored buffer has never been saved by this process, so its encoding is the
// default until a save writes one.
func NewRestoredFile(path string, sess *piecetable.Session, tab int, wrote RestoredWrite) *File {
	content := sess.Buffer().Slice(0, sess.Buffer().Len())
	style, from := IndentFor(path, content, Indent{Width: tab})
	f := &File{
		Path:       path,
		Indent:     style,
		indentFrom: from,
		Cols:       view.NewColumns(tab),
		Syntax:     syntax.New(path, true),
		dark:       true,
		sess:       sess,
		idx:        view.NewIndex(content),
	}
	f.applied = sess.Version()
	if wrote.OK {
		f.saved = wrote.Version
		f.savedLen = len(wrote.Content)
		f.savedSum = sha256.Sum256([]byte(wrote.Content))
		f.savedDisk = wrote.Disk
	} else {
		store := sess.Store()
		base := store.Slice(piecetable.Original, 0, store.Len(piecetable.Original))
		f.saved = 0
		f.savedLen = len(base)
		f.savedSum = sha256.Sum256(base)
	}
	// A restored log can hold decisions made after the write it records, so
	// the version shortcut is not trustworthy until Dirty has compared the
	// agreed composition once.
	f.noteDecisions()
	if f.layered {
		f.decisionGen = 1 // savedGen stays zero, forcing that comparison
	}
	return f
}

// SetIndentDefault applies a preference to files that had nothing to detect —
// a new buffer, or one with no indentation yet. A file that answered for itself
// keeps its own answer: a flag saying "tabs" must not turn a space-indented file
// into a mixed one on the first keystroke.
func (f *File) SetIndentDefault(i Indent) {
	if f.indentFrom != IndentFromDefault {
		return
	}
	if i.Width <= 0 {
		i.Width = f.Indent.Width
	}
	f.Indent = i
}

// IndentDetected reports whether the style came from something stronger than
// the configured default.
func (f *File) IndentDetected() bool { return f.indentFrom != IndentFromDefault }

// IndentSource reports what decided the style.
func (f *File) IndentSource() IndentSource { return f.indentFrom }

// IndentWarning describes a file whose existing indentation the format does not
// accept. Raj indents the NEXT line correctly and leaves what is already there
// alone — rewriting a file on open is not something an editor should do
// unasked — so this is the only thing that tells you the rest is broken.
func (f *File) IndentWarning() string {
	if f.indentFrom != IndentFromFormat {
		return ""
	}
	n := SpaceIndentedLines(f.Path, f.Text())
	if n == 0 {
		return ""
	}
	lines := "lines"
	if n == 1 {
		lines = "line"
	}
	return fmt.Sprintf("%s: %d %s indented with spaces; this format requires tabs",
		filepath.Base(f.Path), n, lines)
}

func (f *File) Len() int                           { return f.sess.Buffer().Len() }
func (f *File) Lines() int                         { return f.idx.Lines() }
func (f *File) Text() string                       { return f.sess.Buffer().Slice(0, f.Len()) }
func (f *File) Slice(pos, n int) string            { return f.sess.Buffer().Slice(pos, n) }
func (f *File) Spans(pos, n int) []piecetable.Span { return f.sess.Buffer().Spans(pos, n) }
func (f *File) Session() *piecetable.Session       { return f.sess }
func (f *File) Pieces() int                        { return f.sess.Buffer().Pieces() }

// MaxCleanCheck is the largest document raj will re-read to decide whether it
// still matches what was saved. Digesting runs at ~1.6 GB/s, so 8 MB is about
// 5 ms — inside a frame, and only ever paid on a version whose length happens
// to match the saved length. Past it a buffer that has been edited stays marked
// dirty even if it was put back, which costs one needless save prompt on a file
// where the check would cost a visible stall.
const MaxCleanCheck = 8 << 20

// Dirty reports unsaved changes, by content rather than by history.
//
// The version is checked first because it settles the common cases for free: an
// untouched buffer is clean, and the length differing from the saved length
// means changed without reading a byte. Only a buffer that is the right length
// but a different version — which is what undoing back to where you started
// looks like — is worth digesting.
func (f *File) Dirty() bool {
	v := f.sess.Version()
	if !f.layered {
		if v == f.saved {
			return false
		}
		if f.cleanKnow && f.cleanVer == v {
			return f.cleanDirt
		}
		dirty := true
		if n := f.Len(); n == f.savedLen && n <= MaxCleanCheck {
			dirty = f.checksum() != f.savedSum
		}
		f.cleanVer, f.cleanKnow, f.cleanDirt = v, true, dirty
		return dirty
	}
	// A change set is proposed or rejected here, so the buffer can hold text
	// that is not part of the agreed composition. Clean means the agreed
	// composition matches what the last write saved, not that the screen does.
	// A decision moves it without moving the version, which is what
	// decisionGen is for.
	if v == f.saved && f.decisionGen == f.savedGen {
		return false
	}
	if f.cleanKnow && f.cleanVer == v && f.cleanGen == f.decisionGen {
		return f.cleanDirt
	}
	dirty := f.acceptedDirty()
	f.cleanVer, f.cleanGen, f.cleanKnow, f.cleanDirt = v, f.decisionGen, true, dirty
	return dirty
}

// ViewDirty reports whether the buffer on screen differs from the last save.
//
// It is not Dirty. Dirty asks whether the agreed composition — what a save
// would write — matches disk, which is the save-path question. A buffer
// holding only a proposed or rejected set has an agreed composition equal to
// disk, so Dirty is false and a save would be a no-op, while the view still
// shows bytes that are not on disk. Search, the tab marker and the
// unsaved-work prompts need the view question, and answering it with the
// agreed composition is what let search fall back to the stale file.
//
// The version check catches any edit, decisionGen a decision made through this
// File (which moves the view without moving the version), and HasDecisions one
// made straight on the session. Any of them can trip while the bytes still
// match disk — an undone edit is the common one — so a tripped probe falls
// through to a content comparison rather than answering dirty.
func (f *File) ViewDirty() bool {
	// The version and the decision generation are cheap "has anything
	// happened" probes, not the answer. An edit that is undone returns the text
	// to what is on disk without returning the version, and a save writes the
	// agreed composition even while a rejected set still sits in the view, so
	// either probe can be true while the bytes match disk. When one trips,
	// compare the view to the bytes the last write put there, the same content
	// check the old view Dirty used.
	if f.sess.Version() == f.saved && f.decisionGen == f.savedGen && !f.sess.HasDecisions() {
		return false
	}
	n := f.Len()
	if n != f.savedLen || n > MaxCleanCheck {
		return true
	}
	return f.checksum() != f.savedSum
}

// checksum digests the document without materialising it. Reading it as one
// string would allocate a second copy of the file to hash and throw it away.
func (f *File) checksum() [sha256.Size]byte {
	h := sha256.New()
	for pos, n := 0, f.Len(); pos < n; {
		size := 64 << 10
		if n-pos < size {
			size = n - pos
		}
		io.WriteString(h, f.Slice(pos, size))
		pos += size
	}
	var sum [sha256.Size]byte
	h.Sum(sum[:0])
	return sum
}

// acceptedDirty reports whether the agreed composition differs from the bytes
// the last write put on disk. It is the layered form of checksum: the same
// size guard, because past MaxCleanCheck the digest would cost a frame and a
// buffer that cannot be verified cheaply is reported dirty.
func (f *File) acceptedDirty() bool {
	acc := f.sess.Project(piecetable.AcceptedOnly)
	n := acc.Len()
	if n != f.savedLen || n > MaxCleanCheck {
		return true
	}
	h := sha256.New()
	buf := acc.Buffer()
	for pos := 0; pos < n; {
		size := 64 << 10
		if n-pos < size {
			size = n - pos
		}
		io.WriteString(h, buf.Slice(pos, size))
		pos += size
	}
	var sum [sha256.Size]byte
	h.Sum(sum[:0])
	return sum != f.savedSum
}

// markSaved records what is now on disk, so a later edit that puts the buffer
// back to it reads as clean. The encoding is applied so the digest names the
// bytes a save would write.
func (f *File) markSaved(content string) {
	f.markSavedBytes(content, encode(content, f.Enc))
}

// markSavedBytes is markSaved with the encoded bytes already in hand, for a
// save that just wrote them. content is the decoded text and data the exact
// bytes on disk.
func (f *File) markSavedBytes(content string, data []byte) {
	f.saved = f.sess.Version()
	f.savedLen = len(content)
	f.savedSum = sha256.Sum256([]byte(content))
	f.savedDisk = sha256.Sum256(data)
	f.savedGen = f.decisionGen
	f.cleanKnow = false
}

// SavedDigest is the SHA-256 of the exact bytes the last save wrote. It is the
// zero digest before the first save. The op log records it so a restart can
// tell a save's own write from a later external edit.
func (f *File) SavedDigest() [sha256.Size]byte { return f.savedDisk }

// SavedVersion is the session version the last save wrote, paired with
// SavedDigest. A restored buffer sets it from the log's Written marker, so
// the next write records the same version the bytes carry.
func (f *File) SavedVersion() piecetable.Version { return f.saved }

// Name is the file's base name, or a placeholder for an unnamed buffer.
func (f *File) Name() string {
	if f.Path == "" {
		return "untitled"
	}
	return filepath.Base(f.Path)
}

// Line returns a line's text without its newline.
func (f *File) Line(n int) string {
	if n < 0 || n >= f.idx.Lines() {
		return ""
	}
	start := f.idx.LineStart(n)
	return f.Slice(start, f.idx.LineEnd(n, f.Len())-start)
}

// LineStart is the byte offset where a line begins.
func (f *File) LineStart(n int) int { return f.idx.LineStart(n) }

// LineEnd is the offset just past a line's last byte, excluding the newline.
func (f *File) LineEnd(n int) int { return f.idx.LineEnd(n, f.Len()) }

// LineOf reports which line an offset falls on.
func (f *File) LineOf(off int) int { return f.idx.LineOf(off) }

// LineCol converts a byte offset to a line and display column.
func (f *File) LineCol(off int) (line, col int) {
	line = f.idx.LineOf(off)
	text := f.Line(line)
	return line, f.Cols.ColOfHints(text, off-f.idx.LineStart(line), f.HintCols(line))
}

// OffsetAt converts a line and display column back to a byte offset, clamping
// to the line's end so a cursor moving down a ragged file stays in bounds.
func (f *File) OffsetAt(line, col int) int {
	if line < 0 {
		return 0
	}
	if line >= f.idx.Lines() {
		return f.Len()
	}
	text := f.Line(line)
	return f.idx.LineStart(line) + f.Cols.OffsetOfHints(text, col, f.HintCols(line))
}

// HintsAt is the hints anchored on a line, nil when the file has none. This is
// the renderer read path and the nil check is the fast one: most files, and
// most lines, have no hints.
func (f *File) HintsAt(line int) []Hint {
	if f.Hints == nil {
		return nil
	}
	return f.Hints.At(line)
}

// SetHints installs a hint set for the file, replacing whatever was there.
func (f *File) SetHints(h *HintSet) { f.Hints = h }

// HintWidth is the text width the installed Hints were filtered for, or zero
// when nothing has been filtered yet. The application compares it against the
// pane width to decide whether the installed set is still current.
func (f *File) HintWidth() int { return f.hintWidth }

// SetHintsFiltered installs the hints from an answer that fit width, dropping
// every hint on a line whose whole line, hints included, is wider than the
// pane. It records width as the width the installed set describes, which is
// what lets a later width change refilter from the full answer rather than
// trusting stale hints.
func (f *File) SetHintsFiltered(lineHints []LineHint, width int) {
	f.Hints = HintsThatFit(f, lineHints, width)
	f.hintWidth = width
}

// ClearHints drops every hint for the file, for an edit that has moved the
// offsets they were anchored to. The next answer reinstalls them.
func (f *File) ClearHints() { f.Hints = nil }

// HintCols is the line's hints in the view-only column form the layout code
// reads. A file with no hints answers nil, so the hint-aware conversions fall
// back to the un-hinted ones exactly.
func (f *File) HintCols(line int) []view.HintCol {
	return HintCols(f.HintsAt(line))
}

// Leased names the change set that owns the text at [pos,pos+length), if any.
// A pending, rejected or invalidated run is read-only until it is decided, so
// an edit that would touch it is refused; the set is named so the caller can
// point at the decision. It reports false when nothing is leased, which is the
// common case.
func (f *File) Leased(pos, length int) (group uint64, ok bool) {
	return f.sess.Leased(pos, length)
}

// EditLeased reports whether replacing [pos,pos+remove) would intersect a
// lease, naming the set that owns it. A pure insertion has remove == 0, which
// probes the insertion point itself; a replacement is caught by the bytes it
// takes away, since an insertion point strictly inside a lease would put the
// start of the removed span inside it too. The bytes put down need no check of
// their own: an insert flush with a run edge sits beside the run, not in it.
func (f *File) EditLeased(pos, remove int) (group uint64, ok bool) {
	return f.Leased(pos, remove)
}

// Insert adds text attributed to author. It refuses, changing nothing, when
// the insertion would land inside a leased run, and reports whether it landed.
// Returning the outcome rather than panicking is what lets a caller skip the
// cursor shifts an insertion would have caused.
func (f *File) Insert(author piecetable.Author, pos int, text string) bool {
	if text == "" {
		return false
	}
	if _, ok := f.Leased(pos, 0); ok {
		return false
	}
	f.sess.Insert(author, pos, text)
	f.sync()
	return true
}

// Delete removes length bytes. It refuses, changing nothing, when the range
// would touch a leased run, and reports whether it landed.
func (f *File) Delete(author piecetable.Author, pos, length int) bool {
	if length <= 0 {
		return false
	}
	if _, ok := f.Leased(pos, length); ok {
		return false
	}
	f.sess.Delete(author, pos, length)
	f.sync()
	return true
}

// ApplyDiff routes an agent's diff through the session and catches the index
// up on every hunk that landed.
func (f *File) ApplyDiff(author piecetable.Author, base piecetable.Version, hunks []piecetable.Hunk) []piecetable.Conflict {
	_, conflicts := f.sess.ApplyDiff(author, base, hunks)
	// Anchor the catch-up at `applied`, not at the version on entry. An op that
	// reached the journal without going through File — a reject made straight on
	// the session — is otherwise left unmirrored while `applied` jumps past it,
	// and its line starts stay in the index for good.
	for _, op := range f.sess.OpsSince(f.applied) {
		f.applyToIndex(op)
	}
	f.applied = f.sess.Version()
	return conflicts
}

// Undo and Redo reverse edits by one author, leaving other authors' work alone.
// They return the ops that were applied so the caller can carry its cursors
// across them — a buffer that changed under a cursor which did not move is the
// single most destructive kind of desync, because the cursor then edits at an
// offset that means something else entirely.
func (f *File) Undo(author piecetable.Author) ([]piecetable.Op, bool) {
	return f.reverse(f.sess.Undo(author))
}

func (f *File) Redo(author piecetable.Author) ([]piecetable.Op, bool) {
	return f.reverse(f.sess.Redo(author))
}

func (f *File) reverse(ok bool) ([]piecetable.Op, bool) {
	if !ok {
		return nil, false
	}
	before := f.applied
	ops := f.sess.OpsSince(before)
	for _, op := range ops {
		f.applyToIndex(op)
	}
	f.applied = f.sess.Version()
	return ops, true
}

// RejectGroup marks a change set rejected. Rejecting is a decision, not an
// edit, so the text and the line index do not move; the sync is a no-op for it
// and stays only as a guard for an op that reached the session outside File,
// so a later ApplyDiff does not jump `applied` past unmirrored line starts.
func (f *File) RejectGroup(group uint64) bool {
	ok := f.sess.RejectGroup(group)
	if ok {
		f.noteDecision()
	}
	f.sync()
	return ok
}

// ClearRejected hard-purges a rejected change set: it reverses the set out of
// the document and drops the decision. Unlike RejectGroup this really edits,
// so the line index has to mirror the reversal; noteDecision moves the
// decision generation so Dirty and ViewDirty stop trusting the pre-clear
// composition. It reports false when the set was not rejected or a reversal
// genuinely wedged, exactly as the session does.
func (f *File) ClearRejected(group uint64) bool {
	if !f.sess.ClearRejected(group) {
		return false
	}
	f.noteDecision()
	f.sync()
	return true
}

// ProposeGroup marks a change set as awaiting a decision. A proposal is not in
// the agreed composition — it is neither saved nor read back by a driver — and
// routing the mark through here is what lets Dirty know the composition moved.
func (f *File) ProposeGroup(group uint64) {
	f.sess.MarkGroup(group, piecetable.Proposed)
	f.noteDecision()
}

// AcceptGroup agrees to a change set. Accepting is a decision, not an edit: the
// text stays where it is, so no op reaches the index; only the agreed
// composition moves, which is what the clean baseline tracks.
func (f *File) AcceptGroup(group uint64) {
	f.sess.AcceptGroup(group)
	f.noteDecision()
}

// AcceptPending is the bulk accept the save gesture uses. It reports how many
// sets it agreed to, matching Session.AcceptPending.
func (f *File) AcceptPending() int {
	n := f.sess.AcceptPending()
	if n > 0 {
		f.noteDecision()
	}
	return n
}

// noteDecision records that the agreed composition may have changed without the
// session version moving, so Dirty's memo has to be dropped and the buffer has
// to stop taking the view-only cheap path.
func (f *File) noteDecision() {
	f.layered = true
	f.decisionGen++
	f.cleanKnow = false
}

// noteDecisions marks a session that already carries decisions — one restored
// from its journal — as layered, so Dirty compares the agreed composition
// rather than the view.
func (f *File) noteDecisions() {
	for _, g := range f.sess.Groups() {
		if g.State != piecetable.Accepted {
			f.layered = true
			return
		}
	}
}

// sync catches the index up on every op applied since it was last current.
//
// It used to mirror only the most recent one, which is right exactly when a
// call appends a single op to the journal — and several do not. One session
// call can append two (a replace is a delete and an insert), and every op past
// the last was then dropped while `applied` still jumped to the new version, so
// the index was silently wrong from that point on and the damage only surfaced
// later, wherever a line number was next needed. Found by the spec properties
// after a multi-cursor edit and an undo.
func (f *File) sync() {
	for _, op := range f.sess.OpsSince(f.applied) {
		f.applyToIndex(op)
	}
	f.applied = f.sess.Version()
}

// applyToIndex mirrors one op into the line index by scanning the op's own
// inserted pieces.
//
// It used to read the span back out of the document instead, which was both
// slower and wrong. Slower because materialising the span to count its newlines
// allocates a full copy of it and throws the copy away — a megabyte of garbage
// on a megabyte paste, for a handful of integers. Wrong because a caller that
// mirrors several ops at once (a multi-hunk diff, a redo of a multi-cursor
// edit) does so after all of them have landed, so reading at op.Pos returns
// whatever the LATER ops left there rather than what this op inserted, and
// every op but the last got the wrong newline positions.
//
// Pieces do not have that problem: the stores are append-only, so a piece
// records exactly the bytes that op inserted no matter what happened after.
func (f *File) applyToIndex(op piecetable.Op) {
	f.idx.Delete(op.Pos, op.DelLen())
	n := op.InsLen()
	f.nlBuf = f.nlBuf[:0]
	if n > 0 {
		at := op.Pos
		store := f.sess.Store()
		for _, rec := range op.Ins {
			b := store.Slice(piecetable.Author(rec.Buf), rec.Start, rec.Length)
			for i := 0; ; {
				j := bytes.IndexByte(b[i:], '\n')
				if j < 0 {
					break
				}
				i += j + 1
				f.nlBuf = append(f.nlBuf, at+i)
			}
			at += rec.Length
		}
		f.idx.InsertLen(op.Pos, n, f.nlBuf)
	}
	// The highlighter gets the same splice, so its spans stay on the
	// characters they were computed for until the next pass replaces them.
	// op.Seq is the version this op replaced, so the version it produced is
	// one past it.
	f.Syntax.Edit(op.Pos, op.DelLen(), n, f.nlBuf, uint64(op.Seq)+1)
}

// Save writes the agreed composition to disk and marks the current version
// clean.
//
// It writes Project(AcceptedOnly), never the raw view: proposed and rejected
// sets are in the buffer but not on disk, which is what makes deciding a
// proposal a separate gesture from saving one. The save gesture is itself the
// approval, so every pending set is accepted first — the review popup a human
// answers is that gesture, and the control verb refuses while anything is
// proposed before it reaches here.
//
// The write is atomic — see writeAtomic — so an interrupted save leaves the
// previous file rather than a truncated one.
//
// It refuses with ErrDiskChanged if the file was written by something else
// since raj last read or wrote it. Overwriting is still available through
// SaveOver; what is not available is doing it without being asked.
func (f *File) Save() error {
	if f.DiskChanged() {
		return ErrDiskChanged
	}
	return f.SaveOver()
}

// SaveOver is Save without the disk-changed check. Only for a caller that has
// asked and been told to go ahead.
func (f *File) SaveOver() error {
	if f.Path == "" {
		return os.ErrInvalid
	}
	f.AcceptPending()
	content := f.sess.Project(piecetable.AcceptedOnly).Text()
	data := encode(content, f.Enc)
	if err := writeAtomic(f.Path, data); err != nil {
		return err
	}
	f.markSavedBytes(content, data)
	f.stampDisk() // raj is now the last writer
	return nil
}

// RefreshSyntax starts a background retokenise if the text has changed. Called
// from the application's idle tick, never from rendering.
func (f *File) RefreshSyntax() {
	if f.Syntax.Enabled() {
		f.Syntax.Ensure(f.Text(), uint64(f.sess.Version()))
	}
}

// SetDark rebuilds the highlighter for the terminal's actual background, once
// the OSC query has answered.
func (f *File) SetDark(dark bool) {
	f.dark = dark
	f.Syntax = syntax.New(f.Path, dark)
}

// SetPath renames the buffer, which is what a save-as does: the text is
// untouched but everything keyed on the name has to follow it. The highlighter
// is the one that matters — an unnamed buffer has no language, so a scratch
// file saved as .go stays uncoloured until it is rebuilt here.
//
// It deliberately does not mark the buffer clean or touch the saved version.
// Naming a file is not writing it, and Save is still what decides that.
func (f *File) SetPath(path string) {
	if path == f.Path {
		return
	}
	f.Path = path
	f.SetDark(f.dark)
}

// Begin and End bracket an undo transaction. One user action is one undo step,
// however many buffer edits it took.
func (f *File) Begin() { f.sess.Begin() }
func (f *File) End()   { f.sess.End() }

// Snapshot captures the pieces covering a range, for a clipboard that holds
// references rather than text.
func (f *File) Snapshot(pos, length int) []piecetable.PieceRec {
	return f.sess.Snapshot(pos, length)
}

// InsertPieces splices captured pieces at pos, appending no text. It refuses,
// changing nothing, when the insertion point lands inside a leased run, and
// reports whether it landed.
func (f *File) InsertPieces(author piecetable.Author, pos int, recs []piecetable.PieceRec) bool {
	if len(recs) == 0 {
		return false
	}
	if _, ok := f.Leased(pos, 0); ok {
		return false
	}
	f.sess.InsertPieces(author, pos, recs)
	f.sync()
	return true
}

// NewlinePiece returns a piece pointing at a single newline, appending one to
// the author store the first time and reusing it thereafter.
//
// Splicing pieces sometimes needs a separator that is not in the clipboard —
// joining several captured selections into one paste, for instance. One byte
// per file, appended once, keeps that from being a special case everywhere
// else.
func (f *File) NewlinePiece(author piecetable.Author) piecetable.PieceRec {
	if f.newline.Length == 0 {
		start := f.sess.Store().Append(author, []byte("\n"))
		f.newline = piecetable.PieceRec{Buf: int(author), Start: start, Length: 1}
	}
	return f.newline
}
