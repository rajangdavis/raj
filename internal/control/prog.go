package control

import (
	"errors"

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
// silently drops unknown arguments and refuses unknown verbs. It holds no
// opcode of its own: init fills it from argOps here and the verb registry in
// verbs.go, so it cannot disagree with the tables that act on a program.
var knownOps = map[byte]bool{}

// verbByOp maps a verb opcode to its registry row, so a compiled program reads
// the name and finish of a verb from the one place a verb is spelled. It is
// filled in init from the registry in verbs.go, one row per program verb.
var verbByOp map[byte]verb

// progAcc is the accumulator a program's argument ops write into before the
// verb that consumes them. It carries the two lifetimes a request field has:
// path, base, author and token describe the caller and persist across verbs,
// while a hunk and an argv are built for one verb and reset by it.
type progAcc struct {
	sticky  Request // path, base, author, token: kept across verbs
	pending Request // sticky plus the arguments this verb will consume
	hunk    *Hunk
	argv    []string
}

// argOps is every argument opcode and what it accumulates. init builds the
// argument half of knownOps from these keys, so a program cannot name an
// argument the accumulator does not handle.
var argOps = map[byte]func(*progAcc, []byte){}

func init() {
	argOps[prog.OpPath] = func(a *progAcc, p []byte) {
		a.sticky.Path = string(p)
		a.pending.Path = a.sticky.Path
	}
	argOps[prog.OpBase] = func(a *progAcc, p []byte) {
		v := uint64(prog.ReadNumber(p))
		a.sticky.Base = &v
		a.pending.Base = &v
	}
	argOps[prog.OpAuthor] = func(a *progAcc, p []byte) {
		if len(p) == 1 {
			a.sticky.Author = p[0]
			a.pending.Author = p[0]
		}
	}
	argOps[prog.OpToken] = func(a *progAcc, p []byte) {
		a.sticky.Token = string(p)
		a.pending.Token = a.sticky.Token
	}
	argOps[prog.OpID] = func(a *progAcc, p []byte) {
		a.pending.ID = prog.ReadNumber(p)
	}
	argOps[prog.OpGroup] = func(a *progAcc, p []byte) {
		a.pending.Group = uint64(prog.ReadNumber(p))
	}
	argOps[prog.OpDumpID] = func(a *progAcc, p []byte) {
		a.pending.DumpID = uint64(prog.ReadNumber(p))
	}
	argOps[prog.OpReviewList] = func(a *progAcc, _ []byte) {
		a.pending.ReviewList = true
	}
	argOps[prog.OpLSPMode] = func(a *progAcc, p []byte) {
		a.pending.LSPMode = string(p)
	}
	argOps[prog.OpGitMode] = func(a *progAcc, p []byte) {
		a.pending.GitMode = string(p)
	}
	argOps[prog.OpGitRev] = func(a *progAcc, p []byte) {
		a.pending.GitRev = string(p)
	}
	argOps[prog.OpGitCount] = func(a *progAcc, p []byte) {
		a.pending.GitCount = prog.ReadNumber(p)
	}
	argOps[prog.OpLine] = func(a *progAcc, p []byte) {
		a.pending.Line = prog.ReadNumber(p)
	}
	argOps[prog.OpCol] = func(a *progAcc, p []byte) {
		a.pending.Col = prog.ReadNumber(p)
	}
	argOps[prog.OpArg] = func(a *progAcc, p []byte) {
		a.argv = append(a.argv, string(p))
	}
	argOps[prog.OpQuery] = func(a *progAcc, p []byte) {
		a.pending.query().Text = string(p)
	}
	argOps[prog.OpInclude] = func(a *progAcc, p []byte) {
		a.pending.query().Include = string(p)
	}
	argOps[prog.OpExclude] = func(a *progAcc, p []byte) {
		a.pending.query().Exclude = string(p)
	}
	argOps[prog.OpFlags] = func(a *progAcc, p []byte) {
		if len(p) == 1 {
			q := a.pending.query()
			q.Regex = p[0]&prog.FlagRegex != 0
			q.Case = p[0]&prog.FlagCase != 0
			q.Word = p[0]&prog.FlagWord != 0
		}
	}
	argOps[prog.OpSpan] = func(a *progAcc, p []byte) {
		if a.hunk == nil {
			a.hunk = &Hunk{}
		}
		a.hunk.Start, a.hunk.End = prog.ReadPair(p)
	}
	argOps[prog.OpText] = func(a *progAcc, p []byte) {
		if a.hunk == nil {
			a.hunk = &Hunk{}
		}
		a.hunk.Text = string(p)
	}
	for op := range argOps {
		knownOps[op] = true
	}
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
//   - hook stays out too: list and show are reads, and put/rm/enable/disable
//     are local-only authoring whose put carries a whole HookRow as JSON, so
//     the JSON request path and `raj hook` are the surfaces it speaks. The
//     verb-code table still carries it for a direct frame.
//
// exec is reachable now that an argument op can accumulate its argv, but the
// remote-execution gate still applies. The serve loop only sees the outer
// "prog" request, so connection.one re-checks the gate for every exec and a
// batch is not a way around the flag path's refusal.
//
// raj ctl still reaches recv, hello and cancel, and `raj hook` reaches hook.

var errNoVerb = errors.New("program ended with arguments and no verb")

// Requests compiles a program.
//
// Arguments accumulate and a verb consumes them, which is what makes a batch
// short: fifty applies against one path state the path once. The accumulator is
// reset after each verb except for the settings that describe the caller rather
// than the call — path, base, author and token — because an agent splicing
// fifty times into one file at one version should not have to repeat itself,
// and because those are exactly the ones a mistake in would be caught
// downstream by the base check rather than landing silently.
func Requests(program []byte, connAuthor uint8) ([]Request, error) {
	ops, err := prog.Decode(program, knownOps)
	if err != nil {
		return nil, err
	}

	var out []Request
	var acc progAcc
	acc.sticky.Author = connAuthor
	acc.pending = acc.sticky

	for _, op := range ops {
		if v, ok := verbByOp[op.Code]; ok {
			req := acc.pending
			req.Op = v.name
			finish := v.finish
			if finish == nil {
				finish = finishVerb
			}
			finish(&req, acc.hunk, acc.argv)
			out = append(out, req)
			// Reset everything the verb consumed; keep what describes the
			// caller.
			acc.hunk = nil
			acc.argv = nil
			acc.pending = acc.sticky
			continue
		}
		if arg, ok := argOps[op.Code]; ok {
			arg(&acc, op.Payload)
		}
	}

	if len(out) == 0 {
		return nil, errNoVerb
	}
	return out, nil
}

// finishVerb is the default finish: a hunk, if the arguments accumulated one,
// becomes the single replacement.
func finishVerb(r *Request, hunk *Hunk, _ []string) {
	if hunk != nil {
		r.Hunks = []Hunk{*hunk}
	}
}

// finishArgv, finishSpan and finishPatch are the finishes of the verbs whose
// arguments do not land in Hunks: exec consumes the accumulated argv, and dump,
// reveal and patch take a span or the text itself.
func finishArgv(r *Request, _ *Hunk, argv []string) {
	r.Argv = argv
}

func finishSpan(r *Request, hunk *Hunk, _ []string) {
	if hunk != nil {
		s, e := hunk.Start, hunk.End
		r.Start, r.End = &s, &e
	}
}

func finishPatch(r *Request, hunk *Hunk, _ []string) {
	if hunk != nil {
		r.PatchText = hunk.Text
	}
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
