// Package editor is the editing pane: an open file, its cursors, and the
// actions that mutate it. It renders into a ui.Screen and never touches a
// terminal, so the whole package tests headlessly.
package editor

import (
	"bytes"
	"errors"
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
	Path   string
	Cols   view.Columns
	Syntax *syntax.Highlighter
	sess   *piecetable.Session
	idx    *view.Index
	saved  piecetable.Version

	// dark is the background the highlighter was built for. Kept so a rename
	// can rebuild it without asking the application which terminal it is in.
	dark bool

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
			return nil, errors.New("is a directory")
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
	return NewFile(path, string(data), tab), nil
}

// NewFile wraps content that is already in memory.
func NewFile(path, content string, tab int) *File {
	return &File{
		Path:   path,
		Cols:   view.NewColumns(tab),
		Syntax: syntax.New(path, true),
		dark:   true,
		sess:   piecetable.NewSession(piecetable.NewDoc(content, 0)),
		idx:    view.NewIndex(content),
	}
}

func (f *File) Len() int                           { return f.sess.Buffer().Len() }
func (f *File) Lines() int                         { return f.idx.Lines() }
func (f *File) Text() string                       { return f.sess.Buffer().Slice(0, f.Len()) }
func (f *File) Slice(pos, n int) string            { return f.sess.Buffer().Slice(pos, n) }
func (f *File) Spans(pos, n int) []piecetable.Span { return f.sess.Buffer().Spans(pos, n) }
func (f *File) Session() *piecetable.Session       { return f.sess }
func (f *File) Pieces() int                        { return f.sess.Buffer().Pieces() }

// Dirty reports unsaved changes. Comparing versions rather than tracking a flag
// means undoing back to the saved state correctly clears it.
func (f *File) Dirty() bool { return f.sess.Version() != f.saved }

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
	f.Syntax.Invalidate()
	f.idx.Delete(op.Pos, op.DelLen())
	n := op.InsLen()
	if n == 0 {
		return
	}
	f.nlBuf = f.nlBuf[:0]
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

// Save writes the document to disk and marks the current version clean.
func (f *File) Save() error {
	if f.Path == "" {
		return os.ErrInvalid
	}
	if err := os.WriteFile(f.Path, []byte(f.Text()), 0o644); err != nil {
		return err
	}
	f.saved = f.sess.Version()
	return nil
}

// RefreshSyntax starts a background retokenise if the text has changed. Called
// from the application's idle tick, never from rendering.
func (f *File) RefreshSyntax() {
	if f.Syntax.Enabled() {
		f.Syntax.Ensure(f.Text())
	}
}

// SetDark rebuilds the highlighter for the terminal's actual background, once
// the OSC query has answered.
func (f *File) SetDark(dark bool) {
	f.dark = dark
	f.Syntax = syntax.New(f.Path, dark)
	f.Syntax.Invalidate()
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
