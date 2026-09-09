package control

import (
	"fmt"

	"raj/internal/prog"
)

// The header, as opcodes rather than JSON.
//
// It was JSON because the wire was optimised for inspectability — serialisation
// is unmeasurable against a model round trip, so there was nothing to buy by
// making it opaque. Two things changed. The body already bypassed JSON, because
// document bytes are bytes and an encoder that replaces anything invalid in
// them moves every offset in the frame; so the header was the last place the
// two encodings met, and every field that could hold arbitrary bytes needed a
// special case to escape into the body. PathLen was that special case, and it
// is gone: a path is a byte string here like anything else.
//
// The other thing is that requests already arrive as programs. Having the frame
// that carries a program be described by a different encoding was a seam with
// nothing on the other side of it.
//
// # Shape
//
// A header is a program: 'R', version, then one op per field that has something
// to say. Absent means zero, so a request states what it means and nothing
// else — the JSON form carried every field name in every frame whether or not
// the verb used it.
//
// All codes are in the argument range, below 0x80, which makes the
// forward-compatibility rule right for free: an unknown field is skipped rather
// than refused. A header is a description, not a request, and a description
// with a field this build has never heard of is still one it can act on.
//
// # Lists
//
// Lists go in one field's payload, flat: elements one after another, each field
// in a fixed order, written with prog.Writer and read back with prog.Reader.
// The two halves sit next to each other below so drift is visible rather than
// latent.
const (
	// request fields
	hID       = 0x01
	hOp       = 0x02
	hPath     = 0x03
	hAuthor   = 0x04
	hToken    = 0x05
	hBase     = 0x06
	hHunks    = 0x07
	hQuery    = 0x08
	hCancel   = 0x09
	hGroup    = 0x0a
	hIdentity = 0x0b
	hName     = 0x0c
	hArgv     = 0x0d
	hDir      = 0x0e
	hOpName   = 0x0f

	// response fields
	hExit         = 0x20
	hDirty        = 0x21
	hStats        = 0x22
	hParticipants = 0x23
	hGroups       = 0x24
	hMessages     = 0x25
	hStream       = 0x26
	hOutLen       = 0x27
	hFinal        = 0x28
	hOK           = 0x29
	hErr          = 0x2a
	hRoot         = 0x2b
	hPID          = 0x2c
	hVersion      = 0x2d
	hBuffers      = 0x2e
	hFiles        = 0x2f
	hCapped       = 0x30
	hMatches      = 0x31
	hConflicts    = 0x32
	hSpans        = 0x33
	hLine         = 0x34 // 1-based line for goto
	hCol          = 0x35 // 1-based column for goto
	hStart        = 0x36 // byte offset for read span; absent means read whole file
	hEnd          = 0x37 // byte offset for read span; absent means read whole file

)

// Verbs cross the wire as one byte, not as their name.
//
// The name is what the handlers switch on and what a person reads, so it stays
// a string in Go. But sending "buffers" is seven bytes to say something there
// are two dozen of, in the one field every single frame carries — and the
// program compiler had already turned an opcode into that string a moment
// earlier, so the round trip through text was pure loss.
//
// Same for the two enums the header carries. A participant is human or agent; a
// change set is proposed, accepted or rejected. Those are closed sets with a
// handful of members, and a closed set is a number.
//
// What stays text is what is genuinely open: paths, document bytes, search
// patterns, error messages, a participant's identity and display name. Those
// are content, and a table of codes for content is a table that is always out
// of date.
var verbCodes = map[string]byte{
	"ping": 1, "buffers": 2, "text": 3, "open": 4, "apply": 5, "save": 6,
	"version": 7, "search": 8, "groups": 9, "accept": 10, "reject": 11,
	"exec": 12, "execcheck": 13, "stats": 14, "hello": 15, "cancel": 16,
	"recv": 17, "snapshot": 18, "prog": 19, "reload": 20, "whoami": 21,
	"who": 22, "send": 23,
	"goto": 24, "close": 25,
}

var verbNamesByCode = func() map[byte]string {
	m := make(map[byte]string, len(verbCodes))
	for name, code := range verbCodes {
		m[code] = name
	}
	return m
}()

var kindCodes = map[string]byte{string(KindHuman): 1, string(KindAgent): 2}
var stateCodes = map[string]byte{"proposed": 1, "accepted": 2, "rejected": 3}

var kindNamesByCode = invert(kindCodes)
var stateNamesByCode = invert(stateCodes)

func invert(table map[string]byte) map[byte]string {
	m := make(map[byte]string, len(table))
	for name, c := range table {
		m[c] = name
	}
	return m
}

// code looks a name up in a table, reporting whether it was there. A name the
// table does not know falls back to being sent as text rather than as zero —
// an op added without a line here should cost a few bytes, not silently become
// the empty string at the other end.
func code(table map[string]byte, name string) (byte, bool) {
	c, ok := table[name]
	return c, ok
}

func nameFor(table map[byte]string, c int) string { return table[byte(c)] }

// encodeHeader writes a header as a program.
//
// Every field is written only when it has something to say. A bool is presence:
// a false flag is not sent, which is why the payload is empty rather than a
// zero byte — and a zero byte is the one thing this encoding does not put in a
// frame, so that a program still travels through argv.
func encodeHeader(h Header) []byte {
	var ops []Op8
	num := func(code byte, v int) {
		if v != 0 {
			ops = append(ops, Op8{code, prog.Number(v)})
		}
	}
	str := func(code byte, s string) {
		if s != "" {
			ops = append(ops, Op8{code, []byte(s)})
		}
	}
	flag := func(code byte, v bool) {
		if v {
			ops = append(ops, Op8{code, nil})
		}
	}

	num(hID, h.ID)
	if c, ok := code(verbCodes, h.Op); ok {
		num(hOp, int(c))
	} else {
		str(hOpName, h.Op)
	}
	str(hPath, h.Path)
	num(hAuthor, int(h.Author))
	str(hToken, h.Token)
	if h.Base != nil {
		// A pointer because zero is a real version and "not stated" is not the
		// same as version zero: an apply with no base is refused, an apply
		// based on version zero is the first one against a fresh buffer.
		ops = append(ops, Op8{hBase, prog.Number(int(*h.Base))})
	}
	num(hCancel, h.Cancel)
	num(hLine, h.Line)
	num(hCol, h.Col)
	if h.Start != nil {
		num(hStart, *h.Start)
	}
	if h.End != nil {
		num(hEnd, *h.End)
	}

	num(hGroup, int(h.Group))
	str(hIdentity, h.Identity)
	str(hName, h.Name)
	str(hDir, h.Dir)
	num(hExit, h.Exit)
	num(hStream, int(h.Stream))
	num(hOutLen, h.OutLen)
	flag(hFinal, h.Final)
	flag(hOK, h.OK)
	str(hErr, h.Err)
	str(hRoot, h.Root)
	num(hPID, h.PID)
	num(hVersion, int(h.Version))
	num(hFiles, h.Files)
	flag(hCapped, h.Capped)

	if len(h.Argv) > 0 {
		var w prog.Writer
		for _, a := range h.Argv {
			w.Str(a)
		}
		ops = append(ops, Op8{hArgv, w.Done()})
	}
	if q := h.Query; q != nil {
		var w prog.Writer
		w.Str(q.Text).Str(q.Include).Str(q.Exclude).Bool(q.Regex).Bool(q.Case).Bool(q.Word)
		ops = append(ops, Op8{hQuery, w.Done()})
	}
	if len(h.Hunks) > 0 {
		var w prog.Writer
		for _, x := range h.Hunks {
			w.Num(x.Start).Num(x.End).Num(x.Len)
		}
		ops = append(ops, Op8{hHunks, w.Done()})
	}
	if len(h.Dirty) > 0 {
		var w prog.Writer
		for _, d := range h.Dirty {
			w.Str(d.Path).Bool(d.AgentOnly)
		}
		ops = append(ops, Op8{hDirty, w.Done()})
	}
	if s := h.Stats; s != (ExecStats{}) {
		var w prog.Writer
		w.Num(s.Runs).Num(s.Stale).Num(s.AgentOnly)
		ops = append(ops, Op8{hStats, w.Done()})
	}
	if len(h.Participants) > 0 {
		var w prog.Writer
		for _, p := range h.Participants {
			kind, ok := code(kindCodes, string(p.Kind))
			w.Num(int(p.ID)).Str(p.Identity).Str(p.Name).Num(int(kind)).Bool(p.Connected)
			if !ok {
				// A kind this build does not know still has to arrive; the
				// zero code means "read the name that follows".
				w.Str(string(p.Kind))
			}
		}
		ops = append(ops, Op8{hParticipants, w.Done()})
	}
	if len(h.Groups) > 0 {
		var w prog.Writer
		for _, g := range h.Groups {
			state, ok := code(stateCodes, g.State)
			w.Num(int(g.ID)).Str(g.Path).Num(int(g.Author)).Num(int(state)).
				Num(g.Ops).Num(g.Bytes).Num(int(g.First)).Num(int(g.Last))
			if !ok {
				w.Str(g.State)
			}
		}
		ops = append(ops, Op8{hGroups, w.Done()})
	}
	if len(h.Messages) > 0 {
		var w prog.Writer
		for _, m := range h.Messages {
			w.Num(int(m.From)).Str(m.Text)
		}
		ops = append(ops, Op8{hMessages, w.Done()})
	}
	if len(h.Buffers) > 0 {
		var w prog.Writer
		for _, b := range h.Buffers {
			w.Str(b.Path).Num(int(b.Version)).Bool(b.Dirty).Num(b.Bytes).Num(b.Lines).Bool(b.Active)

		}
		ops = append(ops, Op8{hBuffers, w.Done()})
	}
	if len(h.Matches) > 0 {
		var w prog.Writer
		for _, m := range h.Matches {
			w.Num(m.Line).Num(m.Col).Num(m.Len).Num(m.PathLen).Num(m.TextLen).
				Num(m.ByteStart).Num(m.ByteEnd)
		}
		ops = append(ops, Op8{hMatches, w.Done()})
	}
	if len(h.Conflicts) > 0 {
		var w prog.Writer
		for _, c := range h.Conflicts {
			w.Num(c.Index).Num(int(c.At)).Num(c.Hunk.Start).Num(c.Hunk.End).Str(c.Hunk.Text)
		}
		ops = append(ops, Op8{hConflicts, w.Done()})
	}
	if len(h.Spans) > 0 {
		var w prog.Writer
		for _, s := range h.Spans {
			w.Num(s.Len).Num(int(s.Author))
		}
		ops = append(ops, Op8{hSpans, w.Done()})
	}

	out := make([]prog.Op, len(ops))
	for i, op := range ops {
		out[i] = prog.Op{Code: op.code, Payload: op.payload}
	}
	return prog.Encode(out)
}

// Op8 is a field on its way into a header. Named rather than inlined so the
// encode side reads as a list of fields instead of a list of struct literals.
type Op8 struct {
	code    byte
	payload []byte
}

// decodeHeader reads what encodeHeader wrote.
//
// Unknown fields are skipped: nil is passed as the known set, and every header
// code is in the argument range, so prog.Decode drops what it does not
// recognise rather than refusing the frame.
func decodeHeader(b []byte) (Header, error) {
	ops, err := prog.Decode(b, nil)
	if err != nil {
		return Header{}, fmt.Errorf("%w: %v", errBadFrame, err)
	}
	var h Header
	for _, op := range ops {
		switch op.Code {
		case hID:
			h.ID = prog.ReadNumber(op.Payload)
		case hOp:
			h.Op = nameFor(verbNamesByCode, prog.ReadNumber(op.Payload))
		case hOpName:
			h.Op = string(op.Payload)
		case hPath:
			h.Path = string(op.Payload)
		case hAuthor:
			h.Author = uint8(prog.ReadNumber(op.Payload))
		case hToken:
			h.Token = string(op.Payload)
		case hBase:
			v := uint64(prog.ReadNumber(op.Payload))
			h.Base = &v
		case hCancel:
			h.Cancel = prog.ReadNumber(op.Payload)
		case hLine:
			h.Line = prog.ReadNumber(op.Payload)
		case hCol:
			h.Col = prog.ReadNumber(op.Payload)
		case hStart:
			v := prog.ReadNumber(op.Payload)
			h.Start = &v
		case hEnd:
			v := prog.ReadNumber(op.Payload)
			h.End = &v

		case hGroup:
			h.Group = uint64(prog.ReadNumber(op.Payload))
		case hIdentity:
			h.Identity = string(op.Payload)
		case hName:
			h.Name = string(op.Payload)
		case hDir:
			h.Dir = string(op.Payload)
		case hExit:
			h.Exit = prog.ReadNumber(op.Payload)
		case hStream:
			h.Stream = uint8(prog.ReadNumber(op.Payload))
		case hOutLen:
			h.OutLen = prog.ReadNumber(op.Payload)
		case hFinal:
			h.Final = true
		case hOK:
			h.OK = true
		case hErr:
			h.Err = string(op.Payload)
		case hRoot:
			h.Root = string(op.Payload)
		case hPID:
			h.PID = prog.ReadNumber(op.Payload)
		case hVersion:
			h.Version = uint64(prog.ReadNumber(op.Payload))
		case hFiles:
			h.Files = prog.ReadNumber(op.Payload)
		case hCapped:
			h.Capped = true

		case hArgv:
			r := prog.NewReader(op.Payload)
			for r.More() {
				h.Argv = append(h.Argv, r.Str())
			}
		case hQuery:
			r := prog.NewReader(op.Payload)
			q := SearchQuery{Text: r.Str(), Include: r.Str(), Exclude: r.Str()}
			q.Regex, q.Case, q.Word = r.Bool(), r.Bool(), r.Bool()
			h.Query = &q
		case hHunks:
			r := prog.NewReader(op.Payload)
			for r.More() {
				h.Hunks = append(h.Hunks, HunkMeta{Start: r.Num(), End: r.Num(), Len: r.Num()})
			}
		case hDirty:
			r := prog.NewReader(op.Payload)
			for r.More() {
				h.Dirty = append(h.Dirty, DirtyBuffer{Path: r.Str(), AgentOnly: r.Bool()})
			}
		case hStats:
			r := prog.NewReader(op.Payload)
			h.Stats = ExecStats{Runs: r.Num(), Stale: r.Num(), AgentOnly: r.Num()}
		case hParticipants:
			r := prog.NewReader(op.Payload)
			for r.More() {
				p := Participant{ID: uint8(r.Num()), Identity: r.Str(), Name: r.Str()}
				kind := r.Num()
				p.Connected = r.Bool()
				if kind == 0 {
					p.Kind = Kind(r.Str())
				} else {
					p.Kind = Kind(nameFor(kindNamesByCode, kind))
				}
				h.Participants = append(h.Participants, p)
			}
		case hGroups:
			r := prog.NewReader(op.Payload)
			for r.More() {
				g := Group{ID: uint64(r.Num()), Path: r.Str(), Author: uint8(r.Num())}
				state := r.Num()
				g.Ops, g.Bytes = r.Num(), r.Num()
				g.First, g.Last = uint64(r.Num()), uint64(r.Num())
				if state == 0 {
					g.State = r.Str()
				} else {
					g.State = nameFor(stateNamesByCode, state)
				}
				h.Groups = append(h.Groups, g)
			}
		case hMessages:
			r := prog.NewReader(op.Payload)
			for r.More() {
				h.Messages = append(h.Messages, Message{From: uint8(r.Num()), Text: r.Str()})
			}
		case hBuffers:
			r := prog.NewReader(op.Payload)
			for r.More() {
				h.Buffers = append(h.Buffers, Buffer{
					Path: r.Str(), Version: uint64(r.Num()), Dirty: r.Bool(),
					Bytes: r.Num(), Lines: r.Num(), Active: r.Bool()})

			}
		case hMatches:
			r := prog.NewReader(op.Payload)
			for r.More() {
				h.Matches = append(h.Matches, MatchMeta{
					Line: r.Num(), Col: r.Num(), Len: r.Num(),
					PathLen: r.Num(), TextLen: r.Num(),
					ByteStart: r.Num(), ByteEnd: r.Num()})
			}
		case hConflicts:
			r := prog.NewReader(op.Payload)
			for r.More() {
				h.Conflicts = append(h.Conflicts, Conflict{
					Index: r.Num(), At: uint64(r.Num()),
					Hunk: Hunk{Start: r.Num(), End: r.Num(), Text: r.Str()}})
			}
		case hSpans:
			r := prog.NewReader(op.Payload)
			for r.More() {
				h.Spans = append(h.Spans, SpanMeta{Len: r.Num(), Author: uint8(r.Num())})
			}
		}
	}
	return h, nil
}
