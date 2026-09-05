package prog

import (
	"fmt"
	"strings"
)

// Encode writes ops as a program, choosing the narrowest width that addresses
// the largest payload in it.
//
// Width is per program rather than fixed at four bytes because that is what the
// piece table does, and because the alternative was to guess: BenchmarkWidth in
// this package measures both, so the choice is a number rather than a taste.
func Encode(ops []Op) []byte {
	longest := 0
	for _, op := range ops {
		if len(op.Payload) > longest {
			longest = len(op.Payload)
		}
	}
	w := WidthFor(longest)

	size := 3
	for _, op := range ops {
		size += 1 + w + len(op.Payload)
	}
	out := make([]byte, 0, size)
	out = append(out, Magic, Version, byte(w))
	buf := make([]byte, 8)
	for _, op := range ops {
		out = append(out, op.Code)
		putUintW(buf, w, len(op.Payload))
		out = append(out, buf[:w]...)
		out = append(out, op.Payload...)
	}
	return out
}

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
	if len(b) < 3 {
		return nil, ErrShort
	}
	if b[0] != Magic {
		return nil, ErrMagic
	}
	if b[1] != Version {
		return nil, fmt.Errorf("%w: %d", ErrVersion, b[1])
	}
	w := int(b[2])
	if w < 1 || w > 8 {
		return nil, fmt.Errorf("%w: %d", ErrWidth, w)
	}

	var ops []Op
	for i := 3; i < len(b); {
		code := b[i]
		i++
		if i+w > len(b) {
			return nil, ErrShort
		}
		n := getUintW(b[i:], w)
		i += w
		// n is attacker-controlled and w can express more than the frame holds,
		// so this is a bounds check rather than an assertion. Checked as a
		// remaining-length comparison, not i+n, because that sum can overflow.
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

// Disasm renders a program as text.
//
// The JSON header this replaces was chosen for inspectability: "serialisation
// is unmeasurable against a model round trip, so there is nothing to buy by
// making it opaque." That reasoning still holds, so the property has to survive
// somewhere — it survives here, in a tool, rather than in the encoding. A test
// that fails while comparing programs should print this, not hex.
func Disasm(b []byte) string {
	var sb strings.Builder
	if len(b) < 3 {
		return "<truncated program>"
	}
	fmt.Fprintf(&sb, "program v%d width=%d (%d bytes)\n", b[1], b[2], len(b))
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
		return fmt.Sprint(number(op.Payload))
	case OpSpan:
		if half := len(op.Payload) / 2; half > 0 {
			return fmt.Sprintf("[%d,%d)", number(op.Payload[:half]), number(op.Payload[half:]))
		}
	case OpAuthor, OpFlags:
		if len(op.Payload) == 1 {
			return fmt.Sprint(op.Payload[0])
		}
	case OpToken:
		return fmt.Sprintf("<%d bytes, redacted>", len(op.Payload))
	}
	return excerpt(op.Payload)
}

func number(b []byte) int { return getUintW(b, len(b)) }

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
