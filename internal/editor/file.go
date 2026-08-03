// Package editor is the editing pane: an open file, its cursors, and the
// actions that mutate it. It renders into a ui.Screen and never touches a
// terminal, so the whole package tests headlessly.
package editor

import (
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

	// applied is the version whose ops have been mirrored into the line index.
	applied piecetable.Version
	newline piecetable.PieceRec
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

// sync catches the index up on the most recent op.
func (f *File) sync() {
	if op, ok := f.sess.LastOp(); ok {
		f.applyToIndex(op)
	}
	f.applied = f.sess.Version()
}

// applyToIndex mirrors one op into the line index. The inserted text is read
// back from the buffer after the fact rather than threaded through the op,
// because ops carry piece records rather than strings — and reading InsLen
// bytes is cheaper than making every op carry a copy.
func (f *File) applyToIndex(op piecetable.Op) {
	f.Syntax.Invalidate()
	f.idx.Delete(op.Pos, op.DelLen())
	if n := op.InsLen(); n > 0 {
		f.idx.Insert(op.Pos, f.Slice(op.Pos, n))
	}
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
	f.Syntax = syntax.New(f.Path, dark)
	f.Syntax.Invalidate()
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
