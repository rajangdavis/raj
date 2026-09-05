package prog

import (
	"bytes"
	"fmt"
	"strings"
)

// Encode writes ops as a program.
//
// No width to choose any more: every length is a biased varint, so a short
// payload costs one byte for its length and a long one costs as many as it
// needs. That is the same saving the per-program width byte used to buy on a
// batch of small ops, without the program-wide compromise a single width forced
// — a batch that ends with one large apply no longer pays four bytes on each of
// the forty-nine small ones before it.
func Encode(ops []Op) []byte {
	size := 2
	for _, op := range ops {
		size += 1 + VarintLen(len(op.Payload)) + len(op.Payload)
	}
	out := make([]byte, 0, size)
	out = append(out, Magic, Version)
	for _, op := range ops {
		out = append(out, op.Code)
		out = PutVarint(out, len(op.Payload))
		out = append(out, op.Payload...)
	}
	return out
}

// Number encodes an integer as an op payload: a biased varint, the same
// encoding lengths use, so a numeric argument never introduces a zero byte
// either. A base of 256 as two fixed-width bytes would have been 00 01.
func Number(v int) []byte { return PutVarint(nil, v) }

// ReadNumber reads what Number wrote. A malformed or empty payload reads as
// zero rather than erroring: a number is an argument, and the rule for an
// argument that does not parse is the same as for one this build has never
// heard of — carry out the request less precisely, do not refuse it.
func ReadNumber(b []byte) int {
	v, _, err := Varint(b)
	if err != nil {
		return 0
	}
	return v
}

// ReadPair reads two numbers from one payload, which is how a span carries its
// start and end.
func ReadPair(b []byte) (int, int) {
	first, n, err := Varint(b)
	if err != nil {
		return 0, 0
	}
	second, _, err := Varint(b[n:])
	if err != nil {
		return first, 0
	}
	return first, second
}

// Pair encodes two numbers into one payload.
func Pair(a, b int) []byte { return PutVarint(PutVarint(nil, a), b) }

// Decode reads a program.
//
// known says which opcodes this build understands; a nil map means all of them,
// which is what the codec's own tests want. An unknown argument is dropped and
// an unknown verb is an error, which is the rule the package comment sets out:
// a qualification you do not understand costs precision, a request you do not
// understand costs correctness.
//
// Payloads alias the input rather than copying it. The caller owns the bytes it
// passed in, which is the same contract the piece table's stores have, and it
// means reading a megabyte of replacement text costs no copy.
func Decode(b []byte, known map[byte]bool) ([]Op, error) {
	if len(b) < 2 {
		return nil, ErrShort
	}
	if b[0] != Magic {
		return nil, ErrMagic
	}
	if b[1] != Version {
		return nil, fmt.Errorf("%w: %d", ErrVersion, b[1])
	}

	var ops []Op
	for i := 2; i < len(b); {
		code := b[i]
		if code == 0 {
			// Not reachable through Encode, and worth its own error rather
			// than a generic one: a zero byte here usually means a program was
			// carried through something that treats it as a C string.
			return nil, ErrNulByte
		}
		i++
		n, adv, err := Varint(b[i:])
		if err != nil {
			return nil, err
		}
		i += adv
		// n comes off a socket, so this is a bounds check rather than an
		// assertion. Written as a remaining-length comparison because i+n can
		// overflow.
		if n < 0 || n > len(b)-i {
			return nil, ErrShort
		}
		payload := b[i : i+n : i+n]
		i += n

		if known != nil && !known[code] {
			if IsVerb(code) {
				return nil, fmt.Errorf("%w: %s", ErrUnknownVerb, Name(code))
			}
			continue // an argument this build does not know
		}
		ops = append(ops, Op{Code: code, Payload: payload})
	}
	return ops, nil
}

// HasNul reports whether a program contains a zero byte anywhere, framing or
// payload. The framing never does; a payload can, if a caller puts one there.
// Callers that are about to hand a program to something which stops at NUL —
// argv, most obviously — check this first and fall back to stdin.
func HasNul(program []byte) bool { return bytes.IndexByte(program, 0) >= 0 }

// Disasm renders a program as text.
//
// The JSON header this replaces was chosen for inspectability: "serialisation
// is unmeasurable against a model round trip, so there is nothing to buy by
// making it opaque." That reasoning still holds, so the property has to survive
// somewhere — it survives here, in a tool, rather than in the encoding. A test
// that fails while comparing programs should print this, not hex.
func Disasm(b []byte) string {
	var sb strings.Builder
	if len(b) < 2 {
		return "<truncated program>"
	}
	fmt.Fprintf(&sb, "program v%d (%d bytes)\n", b[1], len(b))
	ops, err := Decode(b, nil)
	if err != nil {
		fmt.Fprintf(&sb, "  !! %v\n", err)
		return sb.String()
	}
	for _, op := range ops {
		fmt.Fprintf(&sb, "  %-8s %s\n", Name(op.Code), describe(op))
	}
	return sb.String()
}

// describe renders a payload the way that op means it: a number as a number, a
// path as a path, document bytes as a quoted excerpt with a length.
func describe(op Op) string {
	switch op.Code {
	case OpBase, OpGroup, OpID:
		return fmt.Sprint(ReadNumber(op.Payload))
	case OpSpan:
		start, end := ReadPair(op.Payload)
		return fmt.Sprintf("[%d,%d)", start, end)
	case OpAuthor, OpFlags:
		if len(op.Payload) == 1 {
			return fmt.Sprint(op.Payload[0])
		}
	case OpToken:
		return fmt.Sprintf("<%d bytes, redacted>", len(op.Payload))
	}
	return excerpt(op.Payload)
}

// excerpt keeps a dump readable when a payload is a megabyte of replacement
// text: enough to recognise, never enough to scroll.
func excerpt(b []byte) string {
	const max = 48
	if len(b) == 0 {
		return `""`
	}
	if len(b) <= max {
		return fmt.Sprintf("%q", b)
	}
	return fmt.Sprintf("%q… (%d bytes)", b[:max], len(b))
}
