package editor

import "errors"

// ErrReadOnly refuses every save of a buffer that is not a file's text. A
// read-only view -- a seam's diff, shown in a tab -- has no file behind it:
// the bytes are the seam's, and writing them anywhere would put the diff
// itself on disk. It is deliberately not ErrSnapshotReadOnly: a snapshot is
// the daemon's document, whose save-as is a legitimate door, while a read-only
// view has nothing worth writing at all.
var ErrReadOnly = errors.New("this buffer is a read-only view, not a file; there is nothing to save")

// NewReadOnlyFile builds a File for text that is not a file's bytes. path is
// the pane's synthetic identity, not somewhere on disk, and content is shown
// as plain text. Save and SaveOver refuse with ErrReadOnly, and IsReadOnly
// reports true so the application can refuse an edit before it reaches the
// buffer.
func NewReadOnlyFile(path, content string, tab int) *File {
	f := NewFile(path, content, tab)
	f.readOnly = true
	return f
}

// IsReadOnly reports whether this buffer refuses edits and saves: a synthetic
// view rather than a document.
func (f *File) IsReadOnly() bool { return f.readOnly }
