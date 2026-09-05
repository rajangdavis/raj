package editor

import (
	"os"

	"raj/internal/piecetable"
	"raj/internal/syntax"
	"raj/internal/view"
)

// Reload replaces the buffer with what is on disk now.
//
// This is the answer to "changed on disk" that takes the other writer's work
// instead of raj's, so it is a discard, and the caller is responsible for
// having asked. File does not ask: it has no idea whether the buffer it is
// throwing away was typed a second ago or opened untouched an hour back.
//
// **The undo journal does not survive.** Reversals are recorded as offsets into
// the document they were applied to, and that document is gone — an undo across
// a reload would apply a position from one file's history to another file's
// text, which is the precise failure the rebase work exists to prevent. So the
// session is replaced rather than appended to, and undo stops at the reload.
// The alternative, keeping the journal and rebasing through a wholesale
// replacement, is not a smaller change: it is asking the rebase walk to carry
// positions across an edit that shares no pieces with what came before.
//
// Everything keyed on the content is rebuilt for the same reason: the line
// index, the highlighter, the saved digest, and the encoding, which may itself
// have changed — a file rewritten by a Windows tool comes back CRLF.
func (f *File) Reload() error {
	if f.Path == "" {
		return os.ErrInvalid
	}
	// The same refusals Open makes, because a file can become something raj
	// will not open while a tab on it is still there: a build can drop a
	// binary onto a path that held source, and reloading that into a text
	// buffer would put bytes on screen that cannot be edited or written back.
	if info, err := os.Stat(f.Path); err == nil {
		if info.IsDir() {
			return ErrIsDir
		}
		if info.Size() > MaxFileSize {
			return ErrTooLarge
		}
	}
	data, err := os.ReadFile(f.Path)
	if err != nil {
		return err
	}
	if IsBinary(string(data)) {
		return ErrBinary
	}

	text, enc := decode(string(data))
	f.Enc = enc
	// The style is re-detected, because the content it was read from is not
	// the content any more. The current style is the fallback, so a file with
	// nothing to detect keeps what the tab already had rather than snapping
	// back to the flag default.
	f.Indent, f.indentFrom = IndentFor(f.Path, text, f.Indent)

	f.sess = piecetable.NewSession(piecetable.NewDoc(text, 0))
	f.idx = view.NewIndex(text)
	f.Syntax = syntax.New(f.Path, f.dark)
	f.applied = f.sess.Version()
	f.newline = piecetable.PieceRec{} // pointed into the store that just went away
	f.nlBuf = f.nlBuf[:0]
	f.markSaved(text)
	f.stampDisk()
	return nil
}

// Reload replaces the pane's document with what is on disk and puts the caret
// somewhere defensible.
//
// Line and column rather than byte offset: an offset into the old text means
// nothing in the new one — a file rewritten by a formatter can move every byte
// while leaving the line you were looking at exactly where it was — whereas a
// line number usually still points at the same code. Both are clamped, so a
// file that shrank puts the caret at the end rather than out of bounds.
//
// Extra cursors and selections do not survive. A multi-cursor set is a claim
// about several specific places in a document that no longer exists, and
// restoring it approximately would leave edits landing at positions nobody
// chose. One caret, no selection, is the honest state after a reload.
func (p *Pane) Reload() error {
	line, col := p.File.LineCol(p.Cursors.Primary().Head)
	if err := p.File.Reload(); err != nil {
		return err
	}
	if last := p.File.Lines() - 1; line > last {
		line = last
	}
	if line < 0 {
		line = 0
	}
	off := p.File.OffsetAt(line, col)
	p.Cursors.Clear()
	p.Cursors.Set(off, off)
	p.FollowCursor()
	return nil
}
