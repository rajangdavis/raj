package control

import (
	"errors"
	"fmt"

	"raj/internal/prog"
)

// A program is a batch of requests in one frame.
//
// The JSON header carries one op with a field per possible argument. A program
// carries argument ops followed by a verb op, repeatedly, so a caller sends
// what it means and nothing else — and fifty splices are one frame and one
// round trip rather than fifty of each.
//
// This is the seam where the two meet. Requests compiles a program into the
// same Request values DecodeRequest produces, so everything downstream —
// authorisation, the event-thread submit, every handler — is unchanged and
// cannot tell which encoding a request arrived in. That is deliberate: a new
// encoding should not get a second, subtly different execution path.

// knownOps is what this build understands. It is passed to prog.Decode, which
// silently drops unknown arguments and refuses unknown verbs.
var knownOps = map[byte]bool{
	prog.OpPath: true, prog.OpBase: true, prog.OpSpan: true, prog.OpText: true,
	prog.OpAuthor: true, prog.OpToken: true, prog.OpGroup: true,
	prog.OpQuery: true, prog.OpFlags: true, prog.OpID: true,
	prog.OpInclude: true, prog.OpExclude: true,
	prog.OpLine: true, prog.OpCol: true,

	prog.OpPing: true, prog.OpBuffers: true, prog.OpRead: true, prog.OpOpen: true,
	prog.OpApply: true, prog.OpSave: true, prog.OpVersion: true,
	prog.OpGroups: true, prog.OpAccept: true, prog.OpReject: true,
	prog.OpSearch: true, prog.OpStats: true,
	prog.OpGoto: true, prog.OpClose: true,
	prog.OpDump: true, prog.OpPatch: true,
	prog.OpDumpID: true,
	prog.OpLSP:    true, prog.OpLSPMode: true,
}

// verbNames maps a verb opcode to the op string the handlers already switch on.
// Deliberately a translation rather than a rename of the handlers: the string
// ops are what `raj ctl` and every existing test speak, and one encoding
// arriving should not churn the other.
var verbNames = map[byte]string{
	prog.OpPing: "ping", prog.OpBuffers: "buffers", prog.OpRead: "text",
	prog.OpOpen: "open", prog.OpApply: "apply", prog.OpSave: "save",
	prog.OpVersion: "version", prog.OpGroups: "groups",
	prog.OpAccept: "accept", prog.OpReject: "reject",
	prog.OpSearch: "search", prog.OpStats: "stats",
	prog.OpGoto: "goto", prog.OpClose: "close",
	prog.OpDump: "dump", prog.OpPatch: "patch",
	prog.OpLSP: "lsp",
}

// Four verbs stay out of programs, and the reasons are different enough to be
// worth separating.
//
//   - recv parks until the user says something, which could be hours. A batch
//     runs in order, so a recv in the middle would hold every verb behind it —
//     and a caller cannot see that it has, because the frames it is waiting for
//     simply do not arrive.
//   - hello and cancel act on the connection rather than on a document: one
//     rebinds who this connection writes as, the other abandons a request by
//     id. Both are answered on the reading goroutine, before dispatch, so that
//     a cancel can arrive during the search it cancels. Putting either in a
//     batch would mean a request queued behind the very thing it is meant to
//     interrupt.
//   - exec runs a command, and its argv is a list, which the opcode table has
//     no repeated-argument shape for yet. It is also the one verb with a
//     remote-execution gate on it, and widening its surface deserves its own
//     change rather than arriving as part of a batching feature.
//
// raj ctl still reaches all four.

var errNoVerb = errors.New("program ended with arguments and no verb")

// Requests compiles a program.
//
// Arguments accumulate and a verb consumes them, which is what makes a batch
// short: fifty applies against one path state the path once. The accumulator is
// reset after each verb except for the settings that describe the caller rather
// than the call — path, base and author — because an agent splicing fifty times
// into one file at one version should not have to repeat itself, and because
// those three are exactly the ones a mistake in would be caught downstream by
// the base check rather than landing silently.
func Requests(program []byte, connAuthor uint8) ([]Request, error) {
	ops, err := prog.Decode(program, knownOps)
	if err != nil {
		return nil, err
	}

	var out []Request
	var sticky Request // path, base, author: kept across verbs
	sticky.Author = connAuthor
	pending := sticky
	var hunk *Hunk

	for _, op := range ops {
		if prog.IsVerb(op.Code) {
			name, ok := verbNames[op.Code]
			if !ok {
				// Unreachable while knownOps and verbNames agree; asserted
				// rather than assumed, because they are two lists.
				return nil, fmt.Errorf("%w: %s", prog.ErrUnknownVerb, prog.Name(op.Code))
			}
			req := pending
			req.Op = name
			switch name {
			case "dump":
				// A span selects the chunk to snapshot; no text rides the
				// request, the chunk comes back in the reply.
				if hunk != nil {
					s, e := hunk.Start, hunk.End
					req.Start, req.End = &s, &e
				}
			case "patch":
				// The text op carries the whole edited chunk; the id op names
				// the snapshot to replace.
				if hunk != nil {
					req.PatchText = hunk.Text
				}
			default:
				if hunk != nil {
					req.Hunks = []Hunk{*hunk}
				}
			}
			out = append(out, req)
			// Reset everything the verb consumed; keep what describes the
			// caller.
			hunk = nil
			pending = sticky
			continue
		}

		switch op.Code {
		case prog.OpPath:
			sticky.Path = string(op.Payload)
			pending.Path = sticky.Path
		case prog.OpBase:
			v := uint64(prog.ReadNumber(op.Payload))
			sticky.Base = &v
			pending.Base = &v
		case prog.OpAuthor:
			if len(op.Payload) == 1 {
				sticky.Author = op.Payload[0]
				pending.Author = op.Payload[0]
			}
		case prog.OpID:
			pending.ID = prog.ReadNumber(op.Payload)
		case prog.OpToken:
			sticky.Token = string(op.Payload)
			pending.Token = sticky.Token
		case prog.OpGroup:
			pending.Group = uint64(prog.ReadNumber(op.Payload))
		case prog.OpDumpID:
			pending.DumpID = uint64(prog.ReadNumber(op.Payload))
		case prog.OpLSPMode:
			pending.LSPMode = string(op.Payload)
		case prog.OpLine:
			pending.Line = prog.ReadNumber(op.Payload)
		case prog.OpCol:
			pending.Col = prog.ReadNumber(op.Payload)

		case prog.OpQuery:
			pending.query().Text = string(op.Payload)
		case prog.OpInclude:
			pending.query().Include = string(op.Payload)
		case prog.OpExclude:
			pending.query().Exclude = string(op.Payload)
		case prog.OpFlags:
			if len(op.Payload) == 1 {
				q := pending.query()
				q.Regex = op.Payload[0]&prog.FlagRegex != 0
				q.Case = op.Payload[0]&prog.FlagCase != 0
				q.Word = op.Payload[0]&prog.FlagWord != 0
			}
		case prog.OpSpan:
			if hunk == nil {
				hunk = &Hunk{}
			}
			hunk.Start, hunk.End = prog.ReadPair(op.Payload)
		case prog.OpText:
			if hunk == nil {
				hunk = &Hunk{}
			}
			hunk.Text = string(op.Payload)
		}
	}

	if len(out) == 0 {
		return nil, errNoVerb
	}
	return out, nil
}

// query returns the request's search query, creating it on first use. The four
// search arguments can arrive in any order, and each of them is optional, so
// none of them can be the one that allocates.
func (r *Request) query() *SearchQuery {
	if r.Query == nil {
		r.Query = &SearchQuery{}
	}
	return r.Query
}
