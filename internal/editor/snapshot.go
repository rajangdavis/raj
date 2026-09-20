package editor

import (
	"crypto/sha256"
	"errors"

	"raj/internal/piecetable"
	"raj/internal/view"
)

// ErrSnapshotReadOnly refuses a plain save for a buffer built from a session
// snapshot. The buffer is the daemon's text, not this process's file: writing
// it back to disk would silently replace whatever is at the path with another
// process's bytes. An explicit save-as (SaveOver with a chosen path) is the
// only way such a buffer reaches disk.
var ErrSnapshotReadOnly = errors.New("this buffer came from a session snapshot; use save-as to write it")

// OpenSnapshot builds a File from a session snapshot instead of from disk: the
// daemon's document, its attribution and its review state, without reading the
// file at all. The line index, columns and highlighter are built from the
// restored text, exactly as a disk-open builds them from the decoded bytes.
//
// The buffer is left unsaved (no saved baseline matches its content) and its
// Save refuses with ErrSnapshotReadOnly, so a stray cmd+s cannot overwrite the
// file with the daemon's bytes; SaveOver is the explicit save-as path.
func OpenSnapshot(path string, snap piecetable.Snapshot, enc Encoding, tab int) (*File, error) {
	sess, err := piecetable.Restore(snap)
	if err != nil {
		return nil, err
	}
	return OpenSnapshotSession(path, sess, enc, tab), nil
}

// OpenSnapshotSession builds a File from a session already restored from a
// snapshot. It is the half of OpenSnapshot a client needs after it has decoded
// the encoded session itself with piecetable.DecodeSnapshot, so the decode and
// the build are not done twice and a client shares the disk-open's
// indentation, index and decision handling.
func OpenSnapshotSession(path string, sess *piecetable.Session, enc Encoding, tab int) *File {
	text := sess.Buffer().Slice(0, sess.Buffer().Len())
	f := NewFile(path, "", tab)
	f.sess = sess
	f.idx = view.NewIndex(text)
	// Indentation is detected from the restored text, not from the empty string
	// NewFile started with, so the pane looks the same as a disk-open would.
	style, from := IndentFor(path, text, Indent{Width: tab})
	f.Indent, f.indentFrom = style, from
	f.applied = sess.Version()
	f.Enc = enc
	f.snapshot = true
	// A restored session may already carry decisions; without this Dirty would
	// compare history rather than the agreed composition.
	f.noteDecisions()
	return f
}

// RestoreJournal rebuilds a local File from a stored session snapshot. It is
// OpenSnapshotSession plus a disk baseline: unlike OpenSnapshot the buffer is
// deliberately not a snapshot, so Save works, and its dirty state measures the
// restored session against the bytes on disk. diskText is the decoded text of
// those bytes and diskDigest their SHA-256, both from the fresh disk open the
// stored row was matched against; saved is the version whose agreed composition
// those bytes carry.
func RestoreJournal(path string, encoded []byte, enc Encoding, tab int, saved piecetable.Version, diskText string, diskDigest [sha256.Size]byte) (*File, error) {
	sess, err := piecetable.DecodeSnapshot(encoded)
	if err != nil {
		return nil, err
	}
	f := OpenSnapshotSession(path, sess, enc, tab)
	f.snapshot = false
	f.saved = saved
	f.savedLen = len(diskText)
	f.savedSum = sha256.Sum256([]byte(diskText))
	f.savedDisk = diskDigest
	f.savedGen = f.decisionGen
	f.cleanKnow = false
	return f, nil
}
