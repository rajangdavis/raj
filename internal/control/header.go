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
	hID         = 0x01
	hOp         = 0x02
	hPath       = 0x03
	hAuthor     = 0x04
	hToken      = 0x05
	hBase       = 0x06
	hHunks      = 0x07
	hQuery      = 0x08
	hCancel     = 0x09
	hGroup      = 0x0a
	hIdentity   = 0x0b
	hName       = 0x0c
	hArgv       = 0x0d
	hDir        = 0x0e
	hOpName     = 0x0f
	hReviewList = 0x10 // review: list the pending sets without entering the mode
	hAnnotated  = 0x11 // read: return the annotated composition and its state runs
	hCreate     = 0x12 // open: make a buffer for a path that is not on disk yet
	hPaths      = 0x13 // claim: the operand paths, one record per file
	hClaimAdd   = 0x14 // claim: extend the set instead of replacing it
	hClaimClear = 0x15 // claim: release the whole set
	hDiscard    = 0x16 // close: drop unsaved changes instead of refusing the close
	hWithdraw   = 0x17 // delete: retract this identity's proposal instead of making one
	hNewPath    = 0x18 // rename: the destination path, alongside Path
	hHidden     = 0x19 // ls: include hidden entries, the -hidden switch
	hGen        = 0x1a // watch: the generation the client last saw
	hForce      = 0x1b // save: overwrite a file that changed on disk

	// hExecProjected runs exec against a materialised projection of the live
	// buffers instead of the worktree. It is a presence flag like Create.
	hExecProjected = 0x1c

	// hLandTask is a land request's task: the change sets to accept and,
	// where nothing of another task remains pending, save. It is separate
	// from hTask, which names the work a participant's own writes belong to.
	hLandTask = 0x1d

	// hTasks carries the task a writer's changes belong to: the participant's
	// current work and the task each change set was opened under. It is one
	// sparse field of its own, a sequence of {index, kind, task} records where
	// kind 0 is a participant and 1 a group, emitted only for records that
	// have a task. A reader that does not know the field skips it whole rather
	// than reading a task as the next record's id. It takes the first of the
	// last two free argument codes below 0x80, leaving 0x1f for a later field.
	hTasks = 0x1e

	// response fields

	hExit             = 0x20
	hDirty            = 0x21
	hStats            = 0x22
	hParticipants     = 0x23
	hGroups           = 0x24
	hMessages         = 0x25
	hStream           = 0x26
	hOutLen           = 0x27
	hFinal            = 0x28
	hOK               = 0x29
	hErr              = 0x2a
	hRoot             = 0x2b
	hPID              = 0x2c
	hVersion          = 0x2d
	hBuffers          = 0x2e
	hFiles            = 0x2f
	hCapped           = 0x30
	hMatches          = 0x31
	hConflicts        = 0x32
	hSpans            = 0x33
	hLine             = 0x34 // 1-based line for goto
	hCol              = 0x35 // 1-based column for goto
	hStart            = 0x36 // byte offset for read span; absent means read whole file
	hEnd              = 0x37 // byte offset for read span; absent means read whole file
	hBytes            = 0x38 // buffer size in bytes, on a version response
	hLines            = 0x39 // buffer size in lines, on a version response
	hLineStart        = 0x3a // read: 1-based first line of the range
	hLineEnd          = 0x3b // read: 1-based last line of the range
	hDump             = 0x3c // patch: snapshot id to replace; dump: the id it returns
	hHash             = 0x3d // dump: hash of the snapshot text
	hLSPMode          = 0x3e // lsp: hover, definition, references, completion or diagnostics
	hLSPJSON          = 0x3f // lsp: the JSON-encoded answer
	hDiffJSON         = 0x40 // diff: the JSON-encoded pending change sets
	hSrcVersion       = 0x41 // the build revision of the server, stamped on every response
	hConsidered       = 0x42 // search: files opened and scanned; zero under an -include that matched nothing
	hTruncated        = 0x43 // search: files the per-file cap cut down, with shown and total
	hBufferState      = 0x44 // buffers: sparse pending and moved counts, one record per buffer that has either
	hMatchLineStart   = 0x45 // search: byte offset of each hit line start within the file
	hStatesJSON       = 0x46 // read -annotated: the JSON-encoded []StateRun
	hConflictGroup    = 0x47 // apply: sparse lease owner per conflict, one number per conflict
	hBufferHeadless   = 0x48 // buffers: sparse paths of buffers with no tab
	hClaims           = 0x49 // claim: the resulting set, in stable order
	hClaimWarnings    = 0x4a // claim: operands skipped, one warning per path
	hClaimOverlaps    = 0x4b // claim: other identities sharing a claimed path
	hDeletions        = 0x4c // deletions: pending removals, one {path, author} per record
	hMatchLineEnd     = 0x4d // search: sparse byte offset one past each hit line end within the file
	hDirRemovals      = 0x4e // rmdirs: pending dir-removals, one {path, author} per record
	hProposals        = 0x4f // proposals: unified pending list, one {kind, path, author, group, start, end} per record
	hMatchVersion     = 0x50 // search: sparse buffer version per hit, in hit order
	hRemains          = 0x51 // close -discard: a file is still on disk at the discarded buffer's path
	hCreated          = 0x52 // open: a new buffer was made rather than an existing one focused
	hConflictLease    = 0x53 // apply: sparse lease owner author and span per conflict, three numbers per conflict
	hGroupOverlaps    = 0x54 // groups: sparse per-group overlap lists, one count then {group, author, start, end} records each
	hFound            = 0x55 // find: the pattern occurred in the buffer
	hFindStart        = 0x56 // find: byte offset of the first match
	hFindEnd          = 0x57 // find: one past the last byte of the first match
	hFindCount        = 0x58 // find: how many matches the buffer holds, including the first
	hEntries          = 0x59 // ls: immediate children, one {name, path, dir, size} per record
	hGroupInvalid     = 0x5a // groups: sparse per-group Invalid flag and collider, a flag then (when set) {group, author, start, end}
	hApplyWarnings    = 0x5b // apply: sparse overlapped-set warnings, one {group, author, start, end} per warning
	hMatchContext     = 0x5c // search: sparse per-hit context block, one string per hit, in hit order
	hBufferSuperseded = 0x5d // buffers: sparse superseded count, one {path, count} per buffer that has one
	hGenOut           = 0x5e // the whole-workspace generation current at this answer
	hSnapshotJSON     = 0x5f // snapshot: the encoded piecetable session a client renders from
	hEncodingJSON     = 0x60 // snapshot: the file encoding, as JSON
	hSnapshotPath     = 0x61 // snapshot: the buffer's own path
	hRoots            = 0x62 // a reply's whole workspace root set, primary first
	hKind             = 0x63 // hello: the participant kind, absent meaning agent
	hGitMode          = 0x64 // git: status, diff, show, numstat or log
	hGitRev           = 0x65 // git: the revision show/diff/numstat read; absent means HEAD
	hGitCount         = 0x66 // git: the most log entries to list
	hGitJSON          = 0x67 // git: the JSON-encoded result
	hTask             = 0x68 // hello: the task this participant's writes belong to; absent means none
	hHookMode         = 0x69 // hook: list, show, run, put, rm, enable or disable
	hHookName         = 0x6a // hook: the name show/run/rm/enable/disable address
	hHookJSON         = 0x6b // hook or intent payload: a put's HookRow JSON, a list/show answer, or an intent Command/Result
	hRetryAfterMS     = 0x6c // hook run refusal: milliseconds until a retry is allowed
	hReveals          = 0x6d // watch: user-initiated reveals to surface on every client

	// hApprove carries out a pending delete or rmdir removal: the human answer
	// to a proposal, beside Withdraw. It is a presence flag like Withdraw.
	hApprove = 0x6e

	hTo      = 0x6f // send: the recipient, by author id, identity, name or "all"
	hMessage = 0x70 // send: the text for the recipient's mailbox

	// Hook run stamp and in-memory state.
	hHookRunID        = 0x71 // hook: a run id, on cancel and the final frame
	hHookRevision     = 0x72 // hook: the projection revision a run was admitted at
	hHookHead         = 0x73 // hook: the HEAD sha a run saw
	hHookDirty        = 0x74 // hook: the dirty-set digest a run saw
	hHookDurationMS   = 0x75 // hook: how long the run took, in milliseconds
	hHookTruncated    = 0x76 // hook: the run output hit the cap and was cut
	hHookOff          = 0x77 // hook: the workspace panic switch is engaged
	hHookLogJSON      = 0x78 // hook: the in-memory run log as JSON
	hHookPSJSON       = 0x79 // hook: the in-flight run list as JSON
	hState            = 0x7a // state: the declared working state to set; empty reads the caller's own
	hStateOn          = 0x7b // state: the declared state's counterpart, the user or a participant key
	hStateNote        = 0x7c // state: the declared state's free-text note
	hParticipantState = 0x7d // roster: sparse per-participant state, one {id, state, declared, since_ms, note, on} each
	hLand             = 0x7e // land: sparse per-buffer outcome, one {path, sets, saved, held, error} record each

	// hMessageFrom is a sparse field parallel to hMessages: one {key, name}
	// record per message, carrying each sender's durable reply target so a
	// message replayed after its sender has gone is still addressable. A
	// reader that does not know the field skips it whole.
	hMessageFrom = 0x7f
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
	"dump": 26, "patch": 27,
	"lsp": 28, "lspprep": 29,
	"diff": 30, "review": 31, "clear": 32,
	"claim":  33,
	"mkdir":  34,
	"delete": 35, "deletions": 36,
	"rename": 37,
	"rmdir":  38, "rmdirs": 39, "proposals": 40,
	"revert": 41,
	"token":  42,
	"ls":     43,
	"watch":  44,
	"git":    45,
	"hook":   46,
	"reveal": 47,
	"state":  48,
	"land":   49,
	"intent": 50,
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

// workCodes is the working-state vocabulary as codes. Like the participant
// kind and the change-set state it is a closed set, so it travels as a number;
// an empty state is code zero, which nameFor reads back as empty.
var workCodes = map[string]byte{
	StateListening: 1, StateWorking: 2, StateWaiting: 3, StateStale: 4,
	StateGone: 5, StateIdle: 6, StateBlocked: 7, StateReview: 8,
}

var workNamesByCode = invert(workCodes)

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
	str(hNewPath, h.NewPath)
	num(hAuthor, int(h.Author))
	num(hGen, int(h.Gen))
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
	flag(hReviewList, h.ReviewList)
	flag(hAnnotated, h.Annotated)
	flag(hCreate, h.Create)
	flag(hDiscard, h.Discard)
	flag(hForce, h.Force)
	flag(hExecProjected, h.ExecProjected)
	flag(hClaimAdd, h.ClaimAdd)
	flag(hClaimClear, h.ClaimClear)
	flag(hWithdraw, h.Withdraw)
	flag(hApprove, h.Approve)
	flag(hHidden, h.Hidden)
	// The four span fields are pointers for the same reason as Base: zero is a
	// real offset and "not stated" is not the same as offset zero — a read or
	// dump with -start 0 asks for the head of the file, an absent one asks for
	// the whole of it. Emit directly so a stated zero survives the trip.
	if h.Start != nil {
		ops = append(ops, Op8{hStart, prog.Number(*h.Start)})
	}
	if h.End != nil {
		ops = append(ops, Op8{hEnd, prog.Number(*h.End)})
	}
	if h.LineStart != nil {
		ops = append(ops, Op8{hLineStart, prog.Number(*h.LineStart)})
	}
	if h.LineEnd != nil {
		ops = append(ops, Op8{hLineEnd, prog.Number(*h.LineEnd)})
	}

	num(hGroup, int(h.Group))
	str(hIdentity, h.Identity)
	str(hName, h.Name)
	str(hKind, h.Kind)
	str(hTask, h.Task)
	str(hLandTask, h.LandTask)
	str(hState, h.State)
	str(hStateOn, h.StateOn)
	str(hStateNote, h.StateNote)

	str(hTo, h.To)
	str(hMessage, h.Message)
	str(hDir, h.Dir)
	num(hExit, h.Exit)
	num(hStream, int(h.Stream))
	num(hOutLen, h.OutLen)
	flag(hFinal, h.Final)
	flag(hOK, h.OK)
	flag(hRemains, h.Remains)
	flag(hCreated, h.Created)
	str(hErr, h.Err)
	str(hRoot, h.Root)
	if len(h.Roots) > 0 {
		var w prog.Writer
		for _, r := range h.Roots {
			w.Str(r)
		}
		ops = append(ops, Op8{hRoots, w.Done()})
	}

	num(hPID, h.PID)
	num(hVersion, int(h.Version))
	num(hBytes, h.Bytes)
	num(hLines, h.Lines)
	flag(hFound, h.Found)
	num(hFindStart, h.FindStart)
	num(hFindEnd, h.FindEnd)
	num(hFindCount, h.FindCount)
	num(hFiles, h.Files)
	num(hConsidered, h.Considered)
	flag(hCapped, h.Capped)
	num(hDump, int(h.DumpID))
	num(hGenOut, int(h.Gen))
	str(hHash, h.Hash)
	str(hSnapshotJSON, h.SnapshotJSON)
	str(hEncodingJSON, h.EncodingJSON)
	str(hSnapshotPath, h.SnapshotPath)
	str(hLSPMode, h.LSPMode)
	str(hLSPJSON, h.LSPJSON)
	str(hGitMode, h.GitMode)
	str(hGitRev, h.GitRev)
	num(hGitCount, h.GitCount)
	str(hGitJSON, h.GitJSON)
	str(hHookMode, h.HookMode)
	str(hHookName, h.HookName)
	str(hHookJSON, h.HookJSON)
	num(hRetryAfterMS, h.RetryAfterMS)
	num(hHookRunID, int(h.HookRunID))
	num(hHookRevision, int(h.HookRevision))
	str(hHookHead, h.HookHead)
	str(hHookDirty, h.HookDirty)
	num(hHookDurationMS, int(h.HookDurationMS))
	flag(hHookTruncated, h.HookTruncated)
	flag(hHookOff, h.HookOff)
	str(hHookLogJSON, h.HookLogJSON)
	str(hHookPSJSON, h.HookPSJSON)
	str(hDiffJSON, h.DiffJSON)
	str(hStatesJSON, h.StatesJSON)
	str(hSrcVersion, h.SrcVersion)

	if len(h.Argv) > 0 {
		var w prog.Writer
		for _, a := range h.Argv {
			w.Str(a)
		}
		ops = append(ops, Op8{hArgv, w.Done()})
	}
	if len(h.Paths) > 0 {
		var w prog.Writer
		for _, p := range h.Paths {
			w.Str(p)
		}
		ops = append(ops, Op8{hPaths, w.Done()})
	}
	if q := h.Query; q != nil {
		var w prog.Writer
		w.Str(q.Text).Str(q.Include).Str(q.Exclude).Bool(q.Regex).Bool(q.Case).Bool(q.Word)
		// Path is appended rather than carved into the middle of the record: a
		// reader that predates it reads the six fields it knows and ignores the
		// trailing bytes, and a reader that knows it reads the seventh. No
		// element count to disagree about, so the addition is invisible to the
		// old end.
		//
		// Hidden is appended after Path for the same reason: a reader that
		// knows it reads one more field, and one that predates it reads what
		// it knows and leaves the flag false, which is the default walk.
		w.Str(q.Path).Bool(q.Hidden).Num(q.Context)
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
		ops = append(ops, Op8{hParticipants, w.Done()}) // The working state rides in a field of its own rather than inside the
		// hParticipants record: records are positional, so an older reader
		// would read a state code as the next record's id. Every participant
		// gets a record, so a reader that knows the field reads state for all
		// of them and one that does not skips it whole.
		var sts prog.Writer
		for _, p := range h.Participants {
			st, _ := code(workCodes, p.State)
			decl, _ := code(workCodes, p.Declared)
			sts.Num(int(p.ID)).Num(int(st)).Num(int(decl)).Num(int(p.SinceMS)).Str(p.Note).Str(p.On)
		}
		ops = append(ops, Op8{hParticipantState, sts.Done()})
	}
	if len(h.Groups) > 0 {

		var w prog.Writer
		for _, g := range h.Groups {
			state, ok := code(stateCodes, g.State)
			w.Num(int(g.ID)).Str(g.Path).Num(int(g.Author)).Num(int(state)).
				Num(g.Ops).Num(g.Bytes).Num(int(g.First)).Num(int(g.Last)).
				Num(g.Hunks).Num(g.Moved)
			if !ok {
				w.Str(g.State)
			}
		}
		ops = append(ops, Op8{hGroups, w.Done()})

		// Overlap lists ride in their own sparse field, keyed to the groups by

		// position: one count per group, then that many {group, author, start,
		// end} records. A count is written for every group so the order matches
		// hGroups, and the field is emitted only when some set overlaps, so a
		// response with nothing to report is unchanged. A separate field rather
		// than a wider hGroups record keeps an older reader from reading an
		// overlap count as the next group's id.
		var ovs prog.Writer
		var anyOverlap bool
		for _, g := range h.Groups {
			var sets []GroupOverlap
			if g.Overlaps != nil {
				sets = g.Overlaps.Sets
			}
			ovs.Num(len(sets))
			for _, o := range sets {
				ovs.Num(int(o.Group)).Num(int(o.Author)).Num(o.Start).Num(o.End)
			}
			if len(sets) > 0 {
				anyOverlap = true
			}
		}
		if anyOverlap {
			ops = append(ops, Op8{hGroupOverlaps, ovs.Done()})
		}

		// Invalid flags ride the same positional, sparse style: a flag per
		// group, then -- only when the flag is set -- the collider's group,
		// author and span. The field is emitted only when some group is
		// invalid, so a response with none is unchanged, and a separate field
		// rather than a wider hGroups record keeps an older reader from reading
		// a flag as the next group's id.
		var invs prog.Writer
		var anyInvalid bool
		for _, g := range h.Groups {
			if !g.Invalid {
				invs.Num(0)
				continue
			}
			invs.Num(1)
			var by GroupOverlap
			if g.InvalidBy != nil {
				by = *g.InvalidBy
			}
			invs.Num(int(by.Group)).Num(int(by.Author)).Num(by.Start).Num(by.End)
			anyInvalid = true
		}
		if anyInvalid {
			ops = append(ops, Op8{hGroupInvalid, invs.Done()})
		}
	}
	// The task each writer's changes belong to rides in a sparse field of its
	// own: a sequence of {index, kind, task} records, kind 0 a participant and
	// 1 a group, emitted only for records that carry a task. A header with
	// none is byte-for-byte what it always was, and a reader that does not
	// know the field skips it whole.
	{
		var w prog.Writer
		any := false
		for _, p := range h.Participants {
			any = any || p.Task != ""
		}
		for _, g := range h.Groups {
			any = any || g.Task != ""
		}
		if any {
			for i, p := range h.Participants {
				if p.Task != "" {
					w.Num(i).Num(0).Str(p.Task)
				}
			}
			for i, g := range h.Groups {
				if g.Task != "" {
					w.Num(i).Num(1).Str(g.Task)
				}
			}
			ops = append(ops, Op8{hTasks, w.Done()})
		}
	}
	if len(h.Messages) > 0 {
		var w prog.Writer
		for _, m := range h.Messages {
			w.Num(int(m.From)).Str(m.Text)
		}
		ops = append(ops, Op8{hMessages, w.Done()})

		// The sender's reply target rides in its own sparse field, one record
		// per message, so a reader that knows only hMessages skips it whole and
		// still reads the text from the participant list.
		var froms prog.Writer
		var anyFrom bool
		for _, m := range h.Messages {
			froms.Str(m.FromKey).Str(m.FromName)
			if m.FromKey != "" || m.FromName != "" {
				anyFrom = true
			}
		}
		if anyFrom {
			ops = append(ops, Op8{hMessageFrom, froms.Done()})
		}
	}

	if len(h.Land) > 0 {
		var w prog.Writer
		for _, f := range h.Land {
			w.Str(f.Path).Num(f.Sets).Bool(f.Saved).Bool(f.Held).Str(f.Err)
		}
		ops = append(ops, Op8{hLand, w.Done()})
	}
	if len(h.Claims) > 0 {
		var w prog.Writer
		for _, p := range h.Claims {
			w.Str(p)
		}
		ops = append(ops, Op8{hClaims, w.Done()})
	}
	if len(h.ClaimWarnings) > 0 {
		var w prog.Writer
		for _, s := range h.ClaimWarnings {
			w.Str(s)
		}
		ops = append(ops, Op8{hClaimWarnings, w.Done()})
	}
	if len(h.ClaimOverlaps) > 0 {
		var w prog.Writer
		for _, o := range h.ClaimOverlaps {
			w.Str(o.Path).Str(o.Identity).Num(int(o.Author))
		}
		ops = append(ops, Op8{hClaimOverlaps, w.Done()})
	}
	if len(h.Deletions) > 0 {
		var w prog.Writer
		for _, d := range h.Deletions {
			w.Str(d.Path).Num(int(d.Author))
		}
		ops = append(ops, Op8{hDeletions, w.Done()})
	}
	if len(h.DirRemovals) > 0 {
		var w prog.Writer
		for _, d := range h.DirRemovals {
			w.Str(d.Path).Num(int(d.Author))
		}
		ops = append(ops, Op8{hDirRemovals, w.Done()})
	}
	if len(h.Proposals) > 0 {
		var w prog.Writer
		for _, p := range h.Proposals {
			w.Str(p.Kind).Str(p.Path).Num(int(p.Author)).Num(int(p.Group)).Num(p.Start).Num(p.End)
		}
		ops = append(ops, Op8{hProposals, w.Done()})
	}
	if len(h.Reveals) > 0 {
		var w prog.Writer
		for _, r := range h.Reveals {
			w.Str(r.Path).Num(r.Start).Num(r.End)
		}
		ops = append(ops, Op8{hReveals, w.Done()})
	}
	if len(h.Entries) > 0 {
		var w prog.Writer
		for _, e := range h.Entries {
			// Size is -1 when absent, so a zero-byte regular file keeps its
			// zero and a directory stays distinguishable from an empty one.
			size := -1
			if e.Size != nil {
				size = int(*e.Size)
			}
			w.Str(e.Name).Str(e.Path).Bool(e.Dir).Num(size)
		}
		ops = append(ops, Op8{hEntries, w.Done()})
	}

	if len(h.Buffers) > 0 {
		var w prog.Writer
		for _, b := range h.Buffers {
			w.Str(b.Path).Num(int(b.Version)).Bool(b.Dirty).Num(b.Bytes).Num(b.Lines).Bool(b.Active)
		}
		ops = append(ops, Op8{hBuffers, w.Done()})

		// Pending and Moved ride in their own sparse field, one record per
		// buffer that has either, rather than as two more fields on each
		// hBuffers record. Records are positional — no element count and no
		// per-field opcode — so an older reader would read a nonzero pending
		// count as the next record's path. An unknown argument field, by
		// contrast, is skipped: an old reader loses the counts and keeps the
		// buffers, and an old server omits the field so the counts read zero.
		var counts prog.Writer
		var any bool
		for _, b := range h.Buffers {
			if b.Pending == 0 && b.Moved == 0 {
				continue
			}
			any = true
			counts.Str(b.Path).Num(b.Pending).Num(b.Moved)
		}
		if any {
			ops = append(ops, Op8{hBufferState, counts.Done()})
		}

		// The superseded count rides in a field of its own for the same
		// positional-record reason, and because it is a different fact from
		// Pending: a buffer can have no pending set and still be one a save
		// refuses. One record per buffer that has a nonzero count.
		var superseded prog.Writer
		var anySuperseded bool
		for _, b := range h.Buffers {
			if b.Superseded == 0 {
				continue
			}
			anySuperseded = true
			superseded.Str(b.Path).Num(b.Superseded)
		}
		if anySuperseded {
			ops = append(ops, Op8{hBufferSuperseded, superseded.Done()})
		}

		// The path of a headless buffer rides in a sparse field of its own
		// too, one record per headless buffer. The reason matches the counts
		// above: an hBuffers record is positional, so a field added inside
		// one would be read as the next record path by an older reader. An
		// absent field is skipped whole, so an old client reads every buffer
		// as tabbed — the only state an old build could make.
		var headless prog.Writer
		var anyHeadless bool
		for _, b := range h.Buffers {
			if !b.Headless {
				continue
			}
			anyHeadless = true
			headless.Str(b.Path)
		}
		if anyHeadless {
			ops = append(ops, Op8{hBufferHeadless, headless.Done()})
		}
	}

	if len(h.Truncated) > 0 {
		var w prog.Writer
		for _, t := range h.Truncated {
			w.Str(t.Path).Num(t.Shown).Num(t.Total)
		}
		ops = append(ops, Op8{hTruncated, w.Done()})
	}
	if len(h.Matches) > 0 {
		var w prog.Writer
		for _, m := range h.Matches {
			w.Num(m.Line).Num(m.Col).Num(m.Len).Num(m.PathLen).Num(m.TextLen).
				Num(m.ByteStart).Num(m.ByteEnd)
		}
		ops = append(ops, Op8{hMatches, w.Done()})

		// LineStart rides in its own sparse field rather than as another Num in
		// the positional hMatches record: appending to a record-shaped field
		// would misalign an older reader, as it would for hBuffers. It is sent
		// only when some hit is not at offset zero, the field default, so an
		// omitted field means every line start was zero.
		var starts prog.Writer
		anyStart := false
		for _, m := range h.Matches {
			if m.LineStart != 0 {
				anyStart = true
			}
			starts.Num(m.LineStart)
		}
		if anyStart {
			ops = append(ops, Op8{hMatchLineStart, starts.Done()})
		}

		// LineEnd rides the same sparse way, for the same reason. It is sent
		// only when some hit's line does not end at offset zero, so an omitted
		// field means every line end was zero.
		var ends prog.Writer
		anyEnd := false
		for _, m := range h.Matches {
			if m.LineEnd != 0 {
				anyEnd = true
			}
			ends.Num(m.LineEnd)
		}
		if anyEnd {
			ops = append(ops, Op8{hMatchLineEnd, ends.Done()})
		}

		// Version rides the same sparse way, one number per hit, sent only when
		// some hit names a nonzero buffer revision. A hit on a file read from
		// disk carries zero, which is not a revision any buffer can hold, so an
		// omitted field means every hit was the disk's.
		var vers prog.Writer
		anyVersion := false
		for _, m := range h.Matches {
			if m.Version != 0 {
				anyVersion = true
			}
			vers.Num(int(m.Version))
		}
		if anyVersion {
			ops = append(ops, Op8{hMatchVersion, vers.Done()})
		}

		// Context rides the same sparse way, one string per hit, in hit order.
		// It is sent only when some hit carries context, so an omitted field
		// means every hit was the hit line alone; an old reader skips the
		// argument whole rather than misreading a positional record.
		var contexts prog.Writer
		anyContext := false
		for _, m := range h.Matches {
			if m.Context != "" {
				anyContext = true
			}
			contexts.Str(m.Context)
		}
		if anyContext {
			ops = append(ops, Op8{hMatchContext, contexts.Done()})
		}
	}
	if len(h.Conflicts) > 0 {
		var w prog.Writer
		for _, c := range h.Conflicts {
			w.Num(c.Index).Num(int(c.At)).Num(c.Hunk.Start).Num(c.Hunk.End).Str(c.Hunk.Text)
		}
		ops = append(ops, Op8{hConflicts, w.Done()})

		// The lease owner rides in its own sparse field rather than as another
		// field on the record. hConflicts records are positional, so an older
		// reader would read a sixth field as the next record index and misalign
		// every conflict after it; a separate argument field is skipped whole.
		// One number per conflict, in order; the field is absent when every
		// owner is zero, which is the common stale-offset case, so an old
		// client loses nothing it would have had.
		var groups prog.Writer
		var any bool
		for _, c := range h.Conflicts {
			if c.Group != 0 {
				any = true
			}
			groups.Num(int(c.Group))
		}
		if any {
			ops = append(ops, Op8{hConflictGroup, groups.Done()})
		}

		// The lease owner's author and span ride together in one sparse field,
		// three numbers per conflict, in conflict order, so each pair stays with
		// its own refusal. It is emitted whenever any conflict names a lease;
		// the stale-offset conflicts write zeros and an old reader loses
		// nothing it would have had. A separate field keeps the span out of the
		// positional hConflicts record, which an older reader would otherwise
		// read as the next conflict's index.
		var lease prog.Writer
		var anyLease bool
		for _, c := range h.Conflicts {
			if c.Group != 0 {
				anyLease = true
			}
			lease.Num(int(c.Author)).Num(c.Start).Num(c.End)
		}
		if anyLease {
			ops = append(ops, Op8{hConflictLease, lease.Done()})
		}
	}
	// Warnings ride in their own sparse field: one {group, author, start, end}
	// record per warning, emitted only when some hunk landed over another
	// writer's Proposed span. A separate argument field rather than another
	// field on the positional hConflicts record, so an older reader that does
	// not know it skips it whole and loses the warnings rather than misreading
	// the next conflict's index.
	if len(h.Warnings) > 0 {
		var w prog.Writer
		for _, x := range h.Warnings {
			w.Num(int(x.Group)).Num(int(x.Author)).Num(x.Start).Num(x.End)
		}
		ops = append(ops, Op8{hApplyWarnings, w.Done()})
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
// Unknown fields are dropped by the switch below, not by prog.Decode: nil is
// passed as the known set, and prog.Decode reads nil as "every opcode is
// known", so it returns an op for a code this function has no case for and the
// switch's lack of a default ignores it. Header codes are allocated in the
// argument range below 0x80, so the frame is kept rather than refused.
func decodeHeader(b []byte) (Header, error) {
	ops, err := prog.Decode(b, nil)
	if err != nil {
		return Header{}, fmt.Errorf("%w: %v", errBadFrame, err)
	}
	var h Header
	// Buffer pending/moved counts arrive in a field of their own. Collect them
	// and merge once every op has been read, so their position relative to
	// hBuffers does not matter.
	var states []bufferState
	// Superseded counts arrive in their own sparse field, collected and merged
	// after every op like the pending counts.
	var supersededStates []bufferSuperseded
	// Match line starts arrive in their own sparse field; collect them and
	// merge after every op, so their position relative to hMatches does not
	// matter.
	var lineStarts []int
	// Match line ends arrive the same sparse way, one number per hit, merged
	// after every op like the starts.
	var lineEnds []int
	// Match buffer versions arrive the same sparse way, one number per hit,
	// merged after every op like the offsets above.
	var matchVersions []int
	// Match context blocks arrive the same sparse way, one string per hit,
	// merged after every op like the versions above.
	var matchContexts []string
	// Conflict lease owners arrive the same way, one number per conflict, so
	// their position relative to hConflicts does not matter either.
	var conflictGroups []int
	// The lease owner's author and span arrive the same sparse way, three
	// numbers per conflict, merged after every op like the owners.
	var conflictLeases []conflictLease
	// Group overlap lists arrive the same way, a count then that many records
	// per group, merged after every op so their position does not matter.
	var groupOverlaps [][]GroupOverlap
	// Invalid flags and their colliders arrive the same way, a flag then (when
	// set) four numbers per group, merged after every op.
	var groupInvalid []invalidCollider
	// Message reply targets arrive the same sparse way, one {key, name} per
	// message, merged after every op like the fields above.
	var messageFroms []messageFrom

	// Headless buffer paths arrive the same sparse way: one path per buffer
	// with no tab, marked on the matching buffers after every op is read.
	var headless []string // Participant working states arrive in their own sparse field, one record
	// per participant, collected and merged after every op like the headless
	// paths above.
	var participantStates []participantState
	// Task records arrive in a sparse field of their own, {index, kind, task}
	// each, so they are collected and applied after every op like the fields
	// above.
	participantTasks := map[int]string{}
	groupTasks := map[int]string{}
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
		case hNewPath:
			h.NewPath = string(op.Payload)
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
		case hLineStart:
			v := prog.ReadNumber(op.Payload)
			h.LineStart = &v
		case hLineEnd:
			v := prog.ReadNumber(op.Payload)
			h.LineEnd = &v
		case hReviewList:
			h.ReviewList = true
		case hAnnotated:
			h.Annotated = true
		case hCreate:
			h.Create = true
		case hDiscard:
			h.Discard = true
		case hForce:
			h.Force = true
		case hExecProjected:
			h.ExecProjected = true
		case hClaimAdd:
			h.ClaimAdd = true
		case hClaimClear:
			h.ClaimClear = true
		case hWithdraw:
			h.Withdraw = true
		case hApprove:
			h.Approve = true
		case hHidden:
			h.Hidden = true

		case hGen:
			h.Gen = uint64(prog.ReadNumber(op.Payload))
		case hGroup:
			h.Group = uint64(prog.ReadNumber(op.Payload))
		case hIdentity:
			h.Identity = string(op.Payload)
		case hName:
			h.Name = string(op.Payload)
		case hTask:
			h.Task = string(op.Payload)
		case hLandTask:
			h.LandTask = string(op.Payload)
		case hState:
			h.State = string(op.Payload)
		case hStateOn:
			h.StateOn = string(op.Payload)
		case hStateNote:
			h.StateNote = string(op.Payload)

		case hTo:
			h.To = string(op.Payload)
		case hMessage:
			h.Message = string(op.Payload)
		case hKind:
			h.Kind = string(op.Payload)
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
		case hRemains:
			h.Remains = true
		case hCreated:
			h.Created = true
		case hErr:
			h.Err = string(op.Payload)
		case hRoot:
			h.Root = string(op.Payload)
		case hRoots:
			r := prog.NewReader(op.Payload)
			for r.More() {
				h.Roots = append(h.Roots, r.Str())
			}
			if err := recordsOK(r, "roots"); err != nil {
				return Header{}, err
			}

		case hPID:
			h.PID = prog.ReadNumber(op.Payload)
		case hVersion:
			h.Version = uint64(prog.ReadNumber(op.Payload))
		case hBytes:
			h.Bytes = prog.ReadNumber(op.Payload)
		case hLines:
			h.Lines = prog.ReadNumber(op.Payload)
		case hFound:
			h.Found = true
		case hFindStart:
			h.FindStart = prog.ReadNumber(op.Payload)
		case hFindEnd:
			h.FindEnd = prog.ReadNumber(op.Payload)
		case hFindCount:
			h.FindCount = prog.ReadNumber(op.Payload)
		case hFiles:
			h.Files = prog.ReadNumber(op.Payload)
		case hConsidered:
			h.Considered = prog.ReadNumber(op.Payload)
		case hCapped:
			h.Capped = true
		case hDump:
			h.DumpID = uint64(prog.ReadNumber(op.Payload))
		case hGenOut:
			h.Gen = uint64(prog.ReadNumber(op.Payload))
		case hHash:
			h.Hash = string(op.Payload)
		case hSnapshotJSON:
			h.SnapshotJSON = string(op.Payload)
		case hEncodingJSON:
			h.EncodingJSON = string(op.Payload)
		case hSnapshotPath:
			h.SnapshotPath = string(op.Payload)
		case hLSPMode:
			h.LSPMode = string(op.Payload)
		case hLSPJSON:
			h.LSPJSON = string(op.Payload)
		case hGitMode:
			h.GitMode = string(op.Payload)
		case hGitRev:
			h.GitRev = string(op.Payload)
		case hGitCount:
			h.GitCount = prog.ReadNumber(op.Payload)
		case hGitJSON:
			h.GitJSON = string(op.Payload)
		case hHookMode:
			h.HookMode = string(op.Payload)
		case hHookName:
			h.HookName = string(op.Payload)
		case hHookJSON:
			h.HookJSON = string(op.Payload)
		case hRetryAfterMS:
			h.RetryAfterMS = prog.ReadNumber(op.Payload)
		case hHookRunID:
			h.HookRunID = uint64(prog.ReadNumber(op.Payload))
		case hHookRevision:
			h.HookRevision = uint64(prog.ReadNumber(op.Payload))
		case hHookHead:
			h.HookHead = string(op.Payload)
		case hHookDirty:
			h.HookDirty = string(op.Payload)
		case hHookDurationMS:
			h.HookDurationMS = int64(prog.ReadNumber(op.Payload))
		case hHookTruncated:
			h.HookTruncated = true
		case hHookOff:
			h.HookOff = true
		case hHookLogJSON:
			h.HookLogJSON = string(op.Payload)
		case hHookPSJSON:
			h.HookPSJSON = string(op.Payload)
		case hDiffJSON:
			h.DiffJSON = string(op.Payload)
		case hStatesJSON:
			h.StatesJSON = string(op.Payload)
		case hSrcVersion:
			h.SrcVersion = string(op.Payload)

		case hArgv:
			r := prog.NewReader(op.Payload)
			for r.More() {
				h.Argv = append(h.Argv, r.Str())
			}
			if err := recordsOK(r, "argv"); err != nil {
				return Header{}, err
			}
		case hPaths:
			r := prog.NewReader(op.Payload)
			for r.More() {
				h.Paths = append(h.Paths, r.Str())
			}
			if err := recordsOK(r, "paths"); err != nil {
				return Header{}, err
			}
		case hQuery:
			r := prog.NewReader(op.Payload)
			q := SearchQuery{Text: r.Str(), Include: r.Str(), Exclude: r.Str()}
			q.Regex, q.Case, q.Word = r.Bool(), r.Bool(), r.Bool()
			// The path is the last field, so a payload from before it existed
			// leaves q.Path empty rather than an error: the value is absent,
			// which is the same thing as no scope.
			q.Path = r.Str()
			// Hidden is the newest field; a payload from before it existed
			// leaves it false rather than a frame error, which is the default
			// walk rather than an include-hidden one.
			q.Hidden = r.Bool()
			// Context is the newest field; a payload from before it existed
			// leaves it zero rather than a frame error, which is the hit line
			// alone rather than a block around it.
			q.Context = r.Num()
			h.Query = &q
		case hHunks:
			r := prog.NewReader(op.Payload)
			for r.More() {
				h.Hunks = append(h.Hunks, HunkMeta{Start: r.Num(), End: r.Num(), Len: r.Num()})
			}
			if err := recordsOK(r, "hunks"); err != nil {
				return Header{}, err
			}
		case hDirty:
			r := prog.NewReader(op.Payload)
			for r.More() {
				h.Dirty = append(h.Dirty, DirtyBuffer{Path: r.Str(), AgentOnly: r.Bool()})
			}
			if err := recordsOK(r, "dirty"); err != nil {
				return Header{}, err
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
			if err := recordsOK(r, "participants"); err != nil {
				return Header{}, err
			}
		case hParticipantState:
			r := prog.NewReader(op.Payload)
			for r.More() {
				participantStates = append(participantStates, participantState{
					id:       uint8(r.Num()),
					state:    nameFor(workNamesByCode, r.Num()),
					declared: nameFor(workNamesByCode, r.Num()),
					sinceMS:  int64(r.Num()),
					note:     r.Str(),
					on:       r.Str(),
				})
			}
			if err := recordsOK(r, "participant state"); err != nil {
				return Header{}, err
			}
		case hTasks:
			r := prog.NewReader(op.Payload)
			for r.More() {
				index, kind := r.Num(), r.Num()
				task := r.Str()
				if kind == 0 {
					participantTasks[index] = task
				} else {
					groupTasks[index] = task
				}
			}
			if err := recordsOK(r, "tasks"); err != nil {
				return Header{}, err
			}
		case hGroups:
			r := prog.NewReader(op.Payload)
			for r.More() {
				g := Group{ID: uint64(r.Num()), Path: r.Str(), Author: uint8(r.Num())}
				state := r.Num()
				g.Ops, g.Bytes = r.Num(), r.Num()
				g.First, g.Last = uint64(r.Num()), uint64(r.Num())
				g.Hunks, g.Moved = r.Num(), r.Num()
				if state == 0 {
					g.State = r.Str()
				} else {
					g.State = nameFor(stateNamesByCode, state)
				}
				h.Groups = append(h.Groups, g)
			}
			if err := recordsOK(r, "groups"); err != nil {
				return Header{}, err
			}
		case hGroupOverlaps:
			r := prog.NewReader(op.Payload)
			for r.More() {
				n := r.Num()
				sets := make([]GroupOverlap, 0, n)
				for k := 0; k < n; k++ {
					sets = append(sets, GroupOverlap{
						Group: uint64(r.Num()), Author: uint8(r.Num()),
						Start: r.Num(), End: r.Num()})
				}
				groupOverlaps = append(groupOverlaps, sets)
			}
			if err := recordsOK(r, "group overlaps"); err != nil {
				return Header{}, err
			}
		case hGroupInvalid:
			r := prog.NewReader(op.Payload)
			for r.More() {
				ic := invalidCollider{invalid: r.Num() != 0}
				if ic.invalid {
					by := GroupOverlap{Group: uint64(r.Num()), Author: uint8(r.Num()),
						Start: r.Num(), End: r.Num()}
					// A zero group means the set is invalid but no single
					// collider can be named, not a collider with id zero.
					if by.Group != 0 {
						ic.by = &by
					}
				}
				groupInvalid = append(groupInvalid, ic)
			}
			if err := recordsOK(r, "group invalid"); err != nil {
				return Header{}, err
			}
		case hApplyWarnings:
			r := prog.NewReader(op.Payload)
			for r.More() {
				h.Warnings = append(h.Warnings, GroupOverlap{
					Group: uint64(r.Num()), Author: uint8(r.Num()),
					Start: r.Num(), End: r.Num()})
			}
			if err := recordsOK(r, "apply warnings"); err != nil {
				return Header{}, err
			}
		case hMessages:
			r := prog.NewReader(op.Payload)
			for r.More() {
				h.Messages = append(h.Messages, Message{From: uint8(r.Num()), Text: r.Str()})
			}
			if err := recordsOK(r, "messages"); err != nil {
				return Header{}, err
			}
		case hMessageFrom:
			r := prog.NewReader(op.Payload)
			for r.More() {
				messageFroms = append(messageFroms, messageFrom{key: r.Str(), name: r.Str()})
			}
			if err := recordsOK(r, "message from"); err != nil {
				return Header{}, err
			}
		case hClaims:
			r := prog.NewReader(op.Payload)
			for r.More() {
				h.Claims = append(h.Claims, r.Str())
			}
			if err := recordsOK(r, "claims"); err != nil {
				return Header{}, err
			}
		case hClaimWarnings:
			r := prog.NewReader(op.Payload)
			for r.More() {
				h.ClaimWarnings = append(h.ClaimWarnings, r.Str())
			}
			if err := recordsOK(r, "claim warnings"); err != nil {
				return Header{}, err
			}
		case hClaimOverlaps:
			r := prog.NewReader(op.Payload)
			for r.More() {
				h.ClaimOverlaps = append(h.ClaimOverlaps, ClaimOverlap{
					Path: r.Str(), Identity: r.Str(), Author: uint8(r.Num())})
			}
			if err := recordsOK(r, "claim overlaps"); err != nil {
				return Header{}, err
			}
		case hDeletions:
			r := prog.NewReader(op.Payload)
			for r.More() {
				h.Deletions = append(h.Deletions, Deletion{
					Path: r.Str(), Author: uint8(r.Num())})
			}
			if err := recordsOK(r, "deletions"); err != nil {
				return Header{}, err
			}
		case hDirRemovals:
			r := prog.NewReader(op.Payload)
			for r.More() {
				h.DirRemovals = append(h.DirRemovals, DirRemoval{
					Path: r.Str(), Author: uint8(r.Num())})
			}
			if err := recordsOK(r, "dir removals"); err != nil {
				return Header{}, err
			}
		case hProposals:
			r := prog.NewReader(op.Payload)
			for r.More() {
				h.Proposals = append(h.Proposals, Proposal{
					Kind: r.Str(), Path: r.Str(), Author: uint8(r.Num()),
					Group: uint64(r.Num()), Start: r.Num(), End: r.Num()})
			}
			if err := recordsOK(r, "proposals"); err != nil {
				return Header{}, err
			}
		case hLand:
			r := prog.NewReader(op.Payload)
			for r.More() {
				h.Land = append(h.Land, LandFile{Path: r.Str(), Sets: r.Num(),
					Saved: r.Bool(), Held: r.Bool(), Err: r.Str()})
			}
			if err := recordsOK(r, "land"); err != nil {
				return Header{}, err
			}
		case hReveals:
			r := prog.NewReader(op.Payload)
			for r.More() {
				h.Reveals = append(h.Reveals, Reveal{Path: r.Str(), Start: r.Num(), End: r.Num()})
			}
			if err := recordsOK(r, "reveals"); err != nil {
				return Header{}, err
			}
		case hEntries:
			r := prog.NewReader(op.Payload)
			for r.More() {
				e := Entry{Name: r.Str(), Path: r.Str(), Dir: r.Bool()}
				if size := r.Num(); size >= 0 {
					s := int64(size)
					e.Size = &s
				}
				h.Entries = append(h.Entries, e)
			}
			if err := recordsOK(r, "entries"); err != nil {
				return Header{}, err
			}
		case hBuffers:
			r := prog.NewReader(op.Payload)
			for r.More() {
				h.Buffers = append(h.Buffers, Buffer{
					Path: r.Str(), Version: uint64(r.Num()), Dirty: r.Bool(),
					Bytes: r.Num(), Lines: r.Num(), Active: r.Bool()})
			}
			if err := recordsOK(r, "buffers"); err != nil {
				return Header{}, err
			}
		case hBufferState:
			r := prog.NewReader(op.Payload)
			for r.More() {
				states = append(states, bufferState{path: r.Str(), pending: r.Num(), moved: r.Num()})
			}
			if err := recordsOK(r, "buffer state"); err != nil {
				return Header{}, err
			}
		case hBufferSuperseded:
			r := prog.NewReader(op.Payload)
			for r.More() {
				supersededStates = append(supersededStates, bufferSuperseded{path: r.Str(), count: r.Num()})
			}
			if err := recordsOK(r, "buffer superseded"); err != nil {
				return Header{}, err
			}
		case hBufferHeadless:
			r := prog.NewReader(op.Payload)
			for r.More() {
				headless = append(headless, r.Str())
			}
			if err := recordsOK(r, "buffer headless"); err != nil {
				return Header{}, err
			}
		case hTruncated:
			r := prog.NewReader(op.Payload)
			for r.More() {
				h.Truncated = append(h.Truncated, TruncatedFile{
					Path: r.Str(), Shown: r.Num(), Total: r.Num()})
			}
			if err := recordsOK(r, "truncated"); err != nil {
				return Header{}, err
			}
		case hMatches:
			r := prog.NewReader(op.Payload)
			for r.More() {
				h.Matches = append(h.Matches, MatchMeta{
					Line: r.Num(), Col: r.Num(), Len: r.Num(),
					PathLen: r.Num(), TextLen: r.Num(),
					ByteStart: r.Num(), ByteEnd: r.Num()})
			}
			if err := recordsOK(r, "matches"); err != nil {
				return Header{}, err
			}
		case hMatchLineStart:
			r := prog.NewReader(op.Payload)
			for r.More() {
				lineStarts = append(lineStarts, r.Num())
			}
			if err := recordsOK(r, "match line starts"); err != nil {
				return Header{}, err
			}
		case hMatchLineEnd:
			r := prog.NewReader(op.Payload)
			for r.More() {
				lineEnds = append(lineEnds, r.Num())
			}
			if err := recordsOK(r, "match line ends"); err != nil {
				return Header{}, err
			}
		case hMatchVersion:
			r := prog.NewReader(op.Payload)
			for r.More() {
				matchVersions = append(matchVersions, r.Num())
			}
			if err := recordsOK(r, "match versions"); err != nil {
				return Header{}, err
			}
		case hMatchContext:
			r := prog.NewReader(op.Payload)
			for r.More() {
				matchContexts = append(matchContexts, r.Str())
			}
			if err := recordsOK(r, "match context"); err != nil {
				return Header{}, err
			}
		case hConflicts:
			r := prog.NewReader(op.Payload)
			for r.More() {
				h.Conflicts = append(h.Conflicts, Conflict{
					Index: r.Num(), At: uint64(r.Num()),
					Hunk: Hunk{Start: r.Num(), End: r.Num(), Text: r.Str()}})
			}
			if err := recordsOK(r, "conflicts"); err != nil {
				return Header{}, err
			}
		case hConflictGroup:
			r := prog.NewReader(op.Payload)
			for r.More() {
				conflictGroups = append(conflictGroups, r.Num())
			}
			if err := recordsOK(r, "conflict groups"); err != nil {
				return Header{}, err
			}
		case hConflictLease:
			r := prog.NewReader(op.Payload)
			for r.More() {
				conflictLeases = append(conflictLeases, conflictLease{
					author: r.Num(), start: r.Num(), end: r.Num()})
			}
			if err := recordsOK(r, "conflict lease"); err != nil {
				return Header{}, err
			}
		case hSpans:
			r := prog.NewReader(op.Payload)
			for r.More() {
				h.Spans = append(h.Spans, SpanMeta{Len: r.Num(), Author: uint8(r.Num())})
			}
			if err := recordsOK(r, "spans"); err != nil {
				return Header{}, err
			}
		}
	}
	for _, ps := range participantStates {
		for i := range h.Participants {
			if h.Participants[i].ID == ps.id {
				h.Participants[i].State = ps.state
				h.Participants[i].Declared = ps.declared
				h.Participants[i].SinceMS = ps.sinceMS
				h.Participants[i].Note = ps.note
				h.Participants[i].On = ps.on
				break
			}
		}
	}
	for _, st := range states {
		for i := range h.Buffers {
			if h.Buffers[i].Path == st.path {
				h.Buffers[i].Pending, h.Buffers[i].Moved = st.pending, st.moved
				break
			}
		}
	}
	for _, st := range supersededStates {
		for i := range h.Buffers {
			if h.Buffers[i].Path == st.path {
				h.Buffers[i].Superseded = st.count
				break
			}
		}
	}
	for _, p := range headless {
		for i := range h.Buffers {
			if h.Buffers[i].Path == p {
				h.Buffers[i].Headless = true
				break
			}
		}
	}
	for i, ls := range lineStarts {
		if i < len(h.Matches) {
			h.Matches[i].LineStart = ls
		}
	}
	for i, le := range lineEnds {
		if i < len(h.Matches) {
			h.Matches[i].LineEnd = le
		}
	}
	for i, v := range matchVersions {
		if i < len(h.Matches) {
			h.Matches[i].Version = uint64(v)
		}
	}
	for i, c := range matchContexts {
		if i < len(h.Matches) {
			h.Matches[i].Context = c
		}
	}
	for i, g := range conflictGroups {
		if i < len(h.Conflicts) {
			h.Conflicts[i].Group = uint64(g)
		}
	}
	for i, l := range conflictLeases {
		if i < len(h.Conflicts) {
			h.Conflicts[i].Author = uint8(l.author)
			h.Conflicts[i].Start = l.start
			h.Conflicts[i].End = l.end
		}
	}
	for i, sets := range groupOverlaps {
		if i < len(h.Groups) && len(sets) > 0 {
			h.Groups[i].Overlaps = &GroupOverlaps{Sets: sets}
		}
	}
	for i, ic := range groupInvalid {
		if i < len(h.Groups) && ic.invalid {
			h.Groups[i].Invalid = true
			h.Groups[i].InvalidBy = ic.by
		}
	}
	for i, task := range participantTasks {
		if i < len(h.Participants) {
			h.Participants[i].Task = task
		}
	}
	for i, task := range groupTasks {
		if i < len(h.Groups) {
			h.Groups[i].Task = task
		}
	}

	for i, mf := range messageFroms {
		if i < len(h.Messages) {
			h.Messages[i].FromKey, h.Messages[i].FromName = mf.key, mf.name
		}
	}
	return h, nil
}

// conflictLease is one lease refusal's owner and span as it crosses the wire:
// three numbers per conflict, carried in the sparse hConflictLease field.
type conflictLease struct {
	author, start, end int
}

// messageFrom is one message's durable reply target as it crosses the wire,
// carried in the sparse hMessageFrom field rather than inside its positional
// hMessages record.
type messageFrom struct {
	key  string
	name string
}

// invalidCollider is one group's Invalid flag and, when set, the live set that
// consumed it, carried in the sparse hGroupInvalid field: a flag then four
// numbers, the collider's group zero when no single collider can be named.
type invalidCollider struct {
	invalid bool
	by      *GroupOverlap
}

// participantState is one participant's working state as it crosses the wire,
// carried in the sparse hParticipantState field rather than inside its
// positional hParticipants record.
type participantState struct {
	id       uint8
	state    string
	declared string
	sinceMS  int64
	note     string
	on       string
}

// bufferState is one buffer's pending and moved counts as they cross the wire,
// carried in the sparse hBufferState field rather than inside its hBuffers
// record.
type bufferState struct {
	path    string
	pending int
	moved   int
}

// bufferSuperseded is one buffer's superseded count as it crosses the wire,
// carried in the sparse hBufferSuperseded field.
type bufferSuperseded struct {
	path  string
	count int
}

// recordsOK turns a Reader that ran off the end of a record list into a named
// frame error.
//
// A record list has no element count — the payload is the list — and a Reader
// reports failure through Bad rather than an error return, so the caller can
// check once. That check is the whole point: without it a payload cut
// mid-record sets Bad, More reports false, and the list silently ends one
// element early. That is how a malformed groups reply listed one change set
// while three existed; this makes it a named frame error instead of a short
// list.
func recordsOK(r *prog.Reader, field string) error {
	if r.Bad {
		return fmt.Errorf("%w: truncated %s record", errBadFrame, field)
	}
	return nil
}
