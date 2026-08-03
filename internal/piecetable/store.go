package piecetable

// Author identifies who wrote a piece. It is stored in the byte the record
// layout already reserves for a buffer id, so attribution costs nothing:
// widening it from "original vs add" to "original vs user vs each agent" uses
// the same byte and the same code paths.
//
// This is what the editor renders as an author tint, and what the change list
// filters on.
type Author uint8

const (
	Original  Author = 0 // the file as loaded, read-only
	User      Author = 1 // typed by the human
	Agent     Author = 2 // first agent; subsequent agents take 3, 4, ...
	maxAuthor        = 255
)

// IsAgent reports whether a is any agent rather than the file or the user.
func (a Author) IsAgent() bool { return a >= Agent }

// Store holds the immutable text every piece points into: index 0 is the
// original file, and every other index is an append-only buffer owned by one
// author. Nothing is ever mutated or moved, so a piece's (start, length) stays
// valid for the life of the document — which is what lets undo and the session
// log reference spans instead of copying text.
type Store struct {
	texts [][]byte
}

// NewStore starts a store from the original file contents.
func NewStore(orig []byte) *Store {
	return &Store{texts: [][]byte{orig, {}, {}}}
}

// Append adds text to an author's buffer and returns its start offset.
func (s *Store) Append(a Author, text []byte) int {
	s.grow(a)
	start := len(s.texts[a])
	s.texts[a] = append(s.texts[a], text...)
	return start
}

// Slice returns the bytes an author's buffer holds at [start, start+length).
// The result aliases the store and must not be modified.
func (s *Store) Slice(a Author, start, length int) []byte {
	if int(a) >= len(s.texts) {
		return nil
	}
	b := s.texts[a]
	if start < 0 || start+length > len(b) {
		return nil
	}
	return b[start : start+length]
}

// Len is the size of an author's buffer.
func (s *Store) Len(a Author) int {
	if int(a) >= len(s.texts) {
		return 0
	}
	return len(s.texts[a])
}

// Authors is the number of author slots allocated.
func (s *Store) Authors() int { return len(s.texts) }

// Bytes totals every buffer — the store's real memory cost, which grows with
// edit volume rather than with document size.
func (s *Store) Bytes() (n int) {
	for _, t := range s.texts {
		n += len(t)
	}
	return
}

func (s *Store) grow(a Author) {
	for int(a) >= len(s.texts) {
		s.texts = append(s.texts, []byte{})
	}
}
