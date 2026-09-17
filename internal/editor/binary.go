package editor

import "errors"

// ErrBinary is returned when a file is not text. raj declines rather than
// opening it: a binary buffer is unreadable, unsaveable without corrupting the
// file, and its bytes are meaningless as lines or columns.
var ErrBinary = errors.New("binary file")

// ErrTooLarge is returned for files past MaxFileSize.
var ErrTooLarge = errors.New("file too large")

// ErrIsDir is returned when a path that should be a file is a directory. Its
// own value because a reload has to distinguish it from a read error: a file
// replaced by a directory is a statement about the path, not a transient
// failure to retry.
var ErrIsDir = errors.New("is a directory")

// MaxFileSize is the largest file raj will open. The piece table handles far
// more, but reading it means holding the whole file in memory, and a stray
// return on a disk image should not be how you find that out.
const MaxFileSize = 256 << 20

// sniffLen is how much of a file the single-byte heuristic examines. A binary
// file almost always reveals itself immediately, and looking deeper costs time
// on every open.
const sniffLen = 8192

// IsBinary reports whether content should be treated as binary.
//
// It shares charset with decode, so the verdict here and the decode that
// follows cannot disagree. A BOM-marked UTF-16 file is text (false); a file
// with a NUL, or with the control bytes binary formats are made of, is binary
// (true). A text encoding raj recognises but cannot reproduce is neither:
// IsBinary is false for it, and decode refuses it by name.
//
// Once this sniffed only for a NUL and invalid UTF-8, which made every
// single-byte encoding binary as a side effect. The decision now lives in
// charset, which also recognises the encodings raj can round-trip.
func IsBinary(content string) bool {
	_, _, err := charset([]byte(content))
	return errors.Is(err, ErrBinary)
}
