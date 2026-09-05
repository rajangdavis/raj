// Package prog is the control protocol's byte encoding: a program of opcodes
// rather than a struct of named fields.
//
// # Why
//
// The JSON header it replaces had a field per possible argument, so every
// caller carried every argument's name whether it used it or not, and adding an
// argument meant adding a field to a struct shared by every op. The flag
// surface on `raj ctl` mirrored that: nine flags to express one splice.
//
// Under a program the arguments are ops of their own, so a request carries what
// it says and nothing else, and a batch is a repetition rather than a new
// shape. Fifty splices are fifty APPLY ops in one frame and one round trip,
// where before they were fifty frames.
//
// It also removes a special case. The JSON header kept PathLen because a
// filename on Linux is arbitrary bytes and Go's encoder replaces anything that
// is not valid UTF-8 with U+FFFD — which addresses a file that does not exist.
// Here everything is bytes and the question does not arise.
//
// # The shape, and what it borrows
//
//	program := magic 'R' | version u8 | width u8 | op*
//	op      := opcode u8 | length uW | payload[length]
//
// The framing idea is MIDI SysEx's: a status byte selecting a payload, and a
// stream of small parameter messages rather than one large struct. What is
// deliberately NOT borrowed is SysEx's 7-bit data encoding and its EOX
// terminator. Those exist because MIDI reserves the high bit for status and a
// receiver on a lossy serial line has to resynchronise with no length
// information. A Unix socket is a reliable ordered stream: length prefixes give
// skip-ahead, cheap validation and no escaping, while 7-bit data would inflate
// every document byte by a seventh — and document bytes are the bulk of this
// wire.
//
// Width is flatpieces.WidthFor applied to the largest payload in the program,
// so the offsets in a small file cost two bytes rather than eight. This is the
// same fixed-width little-endian encoding the piece table already stores
// records in, which is the point: the wire and the buffer agree about how a
// span is written down.
//
// # Forward compatibility
//
// Every op is length-prefixed, so a reader can step over one it does not know.
// Whether it should is not uniform:
//
//   - An unknown ARGUMENT op is skipped. Arguments qualify a verb, and a
//     qualification you do not understand is at worst a request you handle less
//     precisely than the sender hoped.
//   - An unknown VERB op is refused. A verb is the thing being asked for, and
//     skipping one means silently not doing what was asked — the failure mode
//     where a caller believes an edit landed and it did not.
//
// The split is why opcodes are allocated in two ranges rather than one, and why
// the range is part of the encoding rather than a lookup table: a reader from
// before an opcode existed still has to know which way to fail.
package prog

import (
	"errors"
	"fmt"
)

// Magic and Version head every program.
const (
	Magic   = 'R'
	Version = 1
)

// Opcodes. The high bit of the opcode byte is the range: arguments below 0x80,
// verbs at or above it. A reader that has never heard of an opcode still knows
// from the byte alone whether to skip it or refuse the program.
const (
	// arguments (0x00–0x7f): skipped when unknown
	OpPath   = 0x01 // the file a verb acts on, as raw bytes
	OpBase   = 0x02 // uW: the version an APPLY's offsets were measured in
	OpSpan   = 0x03 // uW uW: start and end of the span to replace
	OpText   = 0x04 // replacement bytes
	OpAuthor = 0x05 // u8: the writer
	OpToken  = 0x06 // shared secret, for a TCP listener
	OpGroup  = 0x07 // uW: a change set id
	OpQuery  = 0x08 // search pattern
	OpFlags  = 0x09 // u8 bitfield: regex, case, word
	OpID     = 0x0a // uW: request id, echoed in the response

	// verbs (0x80–0xff): refused when unknown
	OpPing    = 0x80
	OpBuffers = 0x81
	OpRead    = 0x82
	OpOpen    = 0x83
	OpApply   = 0x84
	OpSave    = 0x85
	OpVersion = 0x86
	OpSearch  = 0x87
	OpGroups  = 0x88
	OpAccept  = 0x89
	OpReject  = 0x8a
	OpReload  = 0x8b
)

// IsVerb reports which range an opcode is in. This is the whole of the
// forward-compatibility rule, and it is deliberately arithmetic rather than a
// table: a reader cannot fail to have been updated.
func IsVerb(op byte) bool { return op >= 0x80 }

var names = map[byte]string{
	OpPath: "path", OpBase: "base", OpSpan: "span", OpText: "text",
	OpAuthor: "author", OpToken: "token", OpGroup: "group", OpQuery: "query",
	OpFlags: "flags", OpID: "id",
	OpPing: "ping", OpBuffers: "buffers", OpRead: "read", OpOpen: "open",
	OpApply: "apply", OpSave: "save", OpVersion: "version", OpSearch: "search",
	OpGroups: "groups", OpAccept: "accept", OpReject: "reject", OpReload: "reload",
}

// Name is the opcode's spelling, for disassembly and error messages. An
// unknown opcode still gets a useful name, because the whole point of the
// range rule is that unknown opcodes are ordinary.
func Name(op byte) string {
	if n, ok := names[op]; ok {
		return n
	}
	kind := "arg"
	if IsVerb(op) {
		kind = "verb"
	}
	return fmt.Sprintf("unknown-%s-%#02x", kind, op)
}

// Op is one instruction.
type Op struct {
	Code    byte
	Payload []byte
}

// Errors a reader can produce. All of them mean the program is malformed or
// asks for something this build cannot do; none of them are retryable.
var (
	ErrShort       = errors.New("prog: truncated program")
	ErrMagic       = errors.New("prog: not a program")
	ErrVersion     = errors.New("prog: unsupported version")
	ErrWidth       = errors.New("prog: width out of range")
	ErrUnknownVerb = errors.New("prog: unknown verb")
)

// WidthFor picks the smallest byte width that can address n. Same rule as
// piecetable.WidthFor, restated rather than imported: the wire should not
// depend on the buffer's package, and the two agreeing is a property worth
// asserting in a test rather than arranging by coupling.
func WidthFor(n int) int {
	w := 1
	for w < 8 && (uint64(1)<<(8*w)) <= uint64(n) {
		w++
	}
	return w
}

func putUintW(b []byte, w int, v int) {
	u := uint64(v)
	for i := 0; i < w; i++ {
		b[i] = byte(u)
		u >>= 8
	}
}

func getUintW(b []byte, w int) int {
	var u uint64
	for i := w - 1; i >= 0; i-- {
		u = u<<8 | uint64(b[i])
	}
	return int(u)
}
