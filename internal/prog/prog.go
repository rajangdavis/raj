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
//	program := 'R' | version | op*
//	op      := opcode | length | payload[length]
//
// The framing is MIDI SysEx's: a status byte selecting a payload, and a stream
// of small parameter messages rather than one large struct.
//
// It also borrows the property that makes SysEx passable anywhere — no byte of
// the framing is ever zero. SysEx buys that with 7-bit data, because MIDI
// reserves the high bit for status. Here it is bought with a bias instead:
// every length and every number is a LEB128 varint of the value plus one, so
// the encoded form is always at least 1 and no varint byte is ever 0x00. Full
// eight-bit payloads are kept, and a program costs nothing per byte for the
// property.
//
// That is what lets a whole program travel as an ordinary command-line
// argument. argv carries every byte except NUL — the kernel delimits argv
// strings with it — so a format with no zero bytes in its framing goes through
// exec untouched, and `raj ctl run -prog "$(...)"` works without hex or a temp
// file. The earlier fixed-width lengths could not: a length of 3 at width 2 is
// 03 00, and every bare verb encoded its empty payload as a zero byte.
//
// Varints replaced fixed-width lengths to get this, which gives up the
// symmetry with the piece table's flat records. The trade is worth naming: the
// records are a storage format read by offset arithmetic, where fixed width is
// what makes the arithmetic possible, and a program is a stream read
// sequentially, where it buys nothing. They agreed by taste rather than by
// need.
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

// Magic and Version head every program. Both are non-zero, like everything
// else in the framing.
const (
	Magic   = 'R'
	Version = 1
)

// Opcodes. The high bit of the opcode byte is the range: arguments below 0x80,
// verbs at or above it. A reader that has never heard of an opcode still knows
// from the byte alone whether to skip it or refuse the program.
const (
	// arguments (0x00–0x7f): skipped when unknown
	OpPath    = 0x01 // the file a verb acts on, as raw bytes
	OpBase    = 0x02 // uW: the version an APPLY's offsets were measured in
	OpSpan    = 0x03 // uW uW: start and end of the span to replace
	OpText    = 0x04 // replacement bytes
	OpAuthor  = 0x05 // u8: the writer
	OpToken   = 0x06 // shared secret, for a TCP listener
	OpGroup   = 0x07 // uW: a change set id
	OpQuery   = 0x08 // search pattern
	OpFlags   = 0x09 // u8 bitfield: see FlagRegex, FlagCase, FlagWord
	OpID      = 0x0a // varint: request id, echoed in the response
	OpInclude = 0x0b // comma-separated globs a search is limited to
	OpExclude = 0x0c // comma-separated globs a search skips

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
	OpStats   = 0x8c
)

// Search flags, the bits of an OpFlags payload. A bitfield rather than three
// argument ops because they are one setting with three switches, and because a
// caller that sets none should send nothing at all.
const (
	FlagRegex = 1 << 0
	FlagCase  = 1 << 1
	FlagWord  = 1 << 2
)

// IsVerb reports which range an opcode is in. This is the whole of the
// forward-compatibility rule, and it is deliberately arithmetic rather than a
// table: a reader cannot fail to have been updated.
func IsVerb(op byte) bool { return op >= 0x80 }

var names = map[byte]string{
	OpPath: "path", OpBase: "base", OpSpan: "span", OpText: "text",
	OpAuthor: "author", OpToken: "token", OpGroup: "group", OpQuery: "query",
	OpFlags: "flags", OpID: "id", OpInclude: "include", OpExclude: "exclude",
	OpPing: "ping", OpBuffers: "buffers", OpRead: "read", OpOpen: "open",
	OpApply: "apply", OpSave: "save", OpVersion: "version", OpSearch: "search",
	OpGroups: "groups", OpAccept: "accept", OpReject: "reject", OpReload: "reload",
	OpStats: "stats",
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
	ErrBadVarint   = errors.New("prog: malformed varint")
	ErrNulByte     = errors.New("prog: zero byte in the framing")
	ErrUnknownVerb = errors.New("prog: unknown verb")
)

// PutVarint appends v as a LEB128 varint of v+1.
//
// The bias is the whole trick. LEB128 of a value of at least 1 never emits a
// zero byte: continuation bytes have the high bit set, so they exceed 0x7f, and
// the final byte holds the highest non-zero group. Encoding v+1 moves zero —
// the one value that would produce 0x00 — out of range, at a cost of nothing
// for values under 127 and one byte at each power-of-128 boundary.
func PutVarint(dst []byte, v int) []byte {
	u := uint64(v) + 1
	for u >= 0x80 {
		dst = append(dst, byte(u)|0x80)
		u >>= 7
	}
	return append(dst, byte(u))
}

// Varint reads what PutVarint wrote, returning the value and the bytes
// consumed. A zero byte where a varint is expected is malformed by
// construction, which is a useful thing to be able to detect: it means someone
// truncated a program at a NUL, which is exactly what a C string would do.
func Varint(b []byte) (v int, n int, err error) {
	var u uint64
	for i := 0; i < len(b); i++ {
		if i == 0 && b[i] == 0 {
			return 0, 0, ErrNulByte
		}
		if i >= 9 {
			return 0, 0, ErrBadVarint
		}
		u |= uint64(b[i]&0x7f) << (7 * i)
		if b[i]&0x80 == 0 {
			if u == 0 {
				return 0, 0, ErrBadVarint // the bias makes zero unrepresentable
			}
			return int(u - 1), i + 1, nil
		}
	}
	return 0, 0, ErrShort
}

// VarintLen is what PutVarint would append, without appending it.
func VarintLen(v int) int {
	n, u := 1, uint64(v)+1
	for u >= 0x80 {
		n++
		u >>= 7
	}
	return n
}
