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

	// cleanVer memoises the last content comparison, because Dirty is asked
	// once per tab per frame and the comparison reads the whole document.
	cleanVer  piecetable.Version
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
	f.stampDisk() // what was just read is what raj knows about
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

// markSaved records what is now on disk, so a later edit that puts the buffer
// back to it reads as clean.
func (f *File) markSaved(content string) {
	f.saved = f.sess.Version()
	f.savedLen = len(content)
	f.savedSum = sha256.Sum256([]byte(content))
	f.cleanKnow = false
}

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
	return line, f.Cols.ColOf(f.Line(line), off-f.idx.LineStart(line))
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
	return f.idx.LineStart(line) + f.Cols.OffsetOf(f.Line(line), col)
}

// Insert adds text attributed to author.
func (f *File) Insert(author piecetable.Author, pos int, text string) {
	if text == "" {
		return
	}
	f.sess.Insert(author, pos, text)
	f.sync()
}

// Delete removes length bytes.
func (f *File) Delete(author piecetable.Author, pos, length int) {
	if length <= 0 {
		return
	}
	f.sess.Delete(author, pos, length)
	f.sync()
}

// ApplyDiff routes an agent's diff through the session and catches the index
// up on every hunk that landed.
func (f *File) ApplyDiff(author piecetable.Author, base piecetable.Version, hunks []piecetable.Hunk) []piecetable.Conflict {
	before := f.sess.Version()
	_, conflicts := f.sess.ApplyDiff(author, base, hunks)
	for _, op := range f.sess.OpsSince(before) {
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

// Save writes the document to disk and marks the current version clean.
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

// SaveOver writes unconditionally, discarding whatever else was written to the
// file. Only for a caller that has asked and been told to go ahead.
func (f *File) SaveOver() error {
	if f.Path == "" {
		return os.ErrInvalid
	}
	content := f.Text()
	if err := writeAtomic(f.Path, encode(content, f.Enc)); err != nil {
		return err
	}
	f.markSaved(content)
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

// InsertPieces splices captured pieces at pos, appending no text.
func (f *File) InsertPieces(author piecetable.Author, pos int, recs []piecetable.PieceRec) {
	if len(recs) == 0 {
		return
	}
	f.sess.InsertPieces(author, pos, recs)
	f.sync()
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
