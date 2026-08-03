package editor

import (
	"errors"
	"unicode/utf8"
)

// ErrBinary is returned when a file is not text. raj declines rather than
// opening it: a binary buffer is unreadable, unsaveable without corrupting the
// file, and its bytes are meaningless as lines or columns.
var ErrBinary = errors.New("binary file")

// ErrTooLarge is returned for files past MaxFileSize.
var ErrTooLarge = errors.New("file too large")

// MaxFileSize is the largest file raj will open. The piece table handles far
// more, but reading it means holding the whole file in memory, and a stray
// return on a disk image should not be how you find that out.
const MaxFileSize = 256 << 20

// sniffLen is how much of a file is examined to classify it. A binary file
// almost always reveals itself immediately, and reading more costs time on
// every open.
const sniffLen = 8192

// IsBinary reports whether content should be treated as binary.
//
// Two signals, in the order they are cheap: a NUL byte, which text effectively
// never contains and every binary format does; and invalid UTF-8, which catches
// formats that avoid NUL. Neither alone is sufficient — UTF-16 text is full of
// NULs and would be misclassified either way, which is acceptable, since raj
// could not edit it correctly anyway.
func IsBinary(content string) bool {
	head := content
	if len(head) > sniffLen {
		head = head[:sniffLen]
	}
	for i := 0; i < len(head); i++ {
		if head[i] == 0 {
			return true
		}
	}
	if utf8.ValidString(head) {
		return false
	}
	// Truncating the sniff window can split a multi-byte rune, so a single
	// invalid sequence at the very end is not evidence of anything.
	if len(content) > sniffLen {
		trimmed := head
		for len(trimmed) > 0 && !utf8.ValidString(trimmed) {
			trimmed = trimmed[:len(trimmed)-1]
			if len(head)-len(trimmed) > utf8.UTFMax {
				return true
			}
		}
		return false
	}
	return true
}
