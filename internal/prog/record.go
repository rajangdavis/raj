package prog

// Records: a list of things, laid out flat.
//
// A reply is often a list — buffers, change sets, search matches — and each
// element is several fields that belong together. The elements go in one op's
// payload, one after another, each field written in a fixed order that both
// ends know from the same source file.
//
// This is how SysEx does it, and for the same reason: a bulk dump is N records
// of known layout and the receiver counts. It works because both ends implement
// one spec. raj ships `raj` and `raj ctl` from one commit, so that is exactly
// the situation — there is no independent deployment to coordinate and no skew
// to survive.
//
// The alternative considered was a self-describing form, each field carrying
// its own opcode and length so a reader could skip a field it did not know.
// That buys forward compatibility across versions, which costs a length prefix
// and an opcode per field and is worth nothing while there is one user building
// both ends. If that changes, the version byte at the head of every program is
// the place to notice.
//
// A record's layout lives next to the type it encodes, written with Writer and
// read back with Reader, so the two halves sit within a few lines of each other
// and drift is visible rather than latent.

// Writer builds a payload. The zero value is ready to use.
type Writer struct{ b []byte }

// Num appends a varint, the same biased encoding lengths use, so a record can
// hold a number without introducing a zero byte.
func (w *Writer) Num(v int) *Writer {
	w.b = PutVarint(w.b, v)
	return w
}

// Bytes appends a length-prefixed byte string. Length-prefixed rather than
// terminated because document text and paths are arbitrary bytes: there is no
// terminator that could not also be content.
func (w *Writer) Bytes(b []byte) *Writer {
	w.b = PutVarint(w.b, len(b))
	w.b = append(w.b, b...)
	return w
}

// Str is Bytes for a string.
func (w *Writer) Str(s string) *Writer { return w.Bytes([]byte(s)) }

// Bool appends a flag as a number, because a bare 0 or 1 byte would put a zero
// in the framing and the point of the encoding is that nothing does.
func (w *Writer) Bool(v bool) *Writer {
	n := 0
	if v {
		n = 1
	}
	return w.Num(n)
}

// Done returns the payload.
func (w *Writer) Done() []byte { return w.b }

// Reader walks a payload written by Writer.
//
// It has no error return. A short or malformed payload yields zero values from
// then on and sets Bad, which the caller checks once at the end rather than
// after every field — reading a record is a dozen calls in a row, and a version
// of this that returned an error from each would be unreadable at every call
// site to catch a case that means the same thing every time.
type Reader struct {
	b   []byte
	Bad bool
}

// NewReader reads a payload.
func NewReader(b []byte) *Reader { return &Reader{b: b} }

// More reports whether anything is left, which is how a list is walked: the
// element count is the payload, not a number in front of it.
func (r *Reader) More() bool { return !r.Bad && len(r.b) > 0 }

// Num reads a varint.
func (r *Reader) Num() int {
	if r.Bad {
		return 0
	}
	v, n, err := Varint(r.b)
	if err != nil {
		r.Bad = true
		return 0
	}
	r.b = r.b[n:]
	return v
}

// Bytes reads a length-prefixed byte string. The result aliases the payload,
// like every other read in this package.
func (r *Reader) Bytes() []byte {
	n := r.Num()
	if r.Bad || n < 0 || n > len(r.b) {
		r.Bad = true
		return nil
	}
	out := r.b[:n:n]
	r.b = r.b[n:]
	return out
}

// Str is Bytes for a string.
func (r *Reader) Str() string { return string(r.Bytes()) }

// Bool reads a flag.
func (r *Reader) Bool() bool { return r.Num() == 1 }
