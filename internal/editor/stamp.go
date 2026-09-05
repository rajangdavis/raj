package editor

import (
	"errors"
	"os"
	"time"
)

// ErrDiskChanged reports that the file moved under the buffer: it was written
// by something else — git checkout, a formatter, an agent, another editor —
// since raj last read or wrote it.
//
// It is returned instead of saving, because a plain save would silently discard
// whatever that other writer did. The caller is expected to ask.
var ErrDiskChanged = errors.New("file changed on disk since it was opened")

// stamp is what raj last saw on disk. Size and modification time rather than a
// digest: the check runs on every save, and reading the file back to hash it
// would double the cost of saving a large buffer to answer a question that is
// almost always no.
//
// Path is part of the stamp because a save-as writes somewhere the stamp says
// nothing about. Comparing against the previous file's mtime would either
// refuse a legitimate save or, worse, permit one it had not actually checked.
type stamp struct {
	path  string
	mod   time.Time
	size  int64
	known bool
}

func stampOf(path string) stamp {
	info, err := os.Stat(path)
	if err != nil {
		// A file that is not there has nothing to conflict with. Recorded as
		// known so that a file appearing underneath a new buffer is caught:
		// "create a file the tab was about to be saved as" is a real
		// collision, and treating a missing file as unknown would wave it
		// through.
		return stamp{path: path, known: true}
	}
	return stamp{path: path, mod: info.ModTime(), size: info.Size(), known: true}
}

// changed reports whether the file at path differs from what the stamp records.
//
// An unknown stamp — an in-memory buffer that has never touched a disk — is
// never a conflict, and neither is a path the stamp is not about, because in
// both cases there is nothing this check has any evidence for. Save-as does its
// own overwrite confirmation for that case.
func (s stamp) changed(path string) bool {
	if !s.known || s.path != path {
		return false
	}
	now := stampOf(path)
	return !now.mod.Equal(s.mod) || now.size != s.size
}

// DiskChanged reports whether the file has been written by something else since
// raj last read or wrote it. Cheap enough to call from a tick.
func (f *File) DiskChanged() bool {
	if f.Path == "" {
		return false
	}
	return f.disk.changed(f.Path)
}

// stampDisk records the current state of the file as what raj knows about.
func (f *File) stampDisk() {
	if f.Path == "" {
		f.disk = stamp{}
		return
	}
	f.disk = stampOf(f.Path)
}
