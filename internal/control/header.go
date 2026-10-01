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
// Codes are in the argument range, below 0x80, except where that range is
// exhausted -- see the note below -- which makes the forward-compatibility rule
// right for free: an unknown field is skipped rather than refused. A header is a
// description, not a request, and a description with a field this build has
// never heard of is still one it can act on.
//
// The sub-0x80 range is now full. Request fields use 0x01-0x1f, response
// fields 0x20-0x7f, and 0x00 is unrepresentable in the framing. A code at or
// above 0x80 is not a verb in a header: decodeHeader passes no known set, so
// an unknown header code is skipped by the same forward-compatibility rule
// (the verb rule belongs to programs; see prog.go). The next field needs a
// freed code, a nested field carrying its own sub-codes, or a change to the
// range rule; see TODO.md.
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
	// than reading a task as the next record's id.
	hTasks = 0x1e

	// hHookParams carries a hook run's supplied NAME=value parameters, one
	// record per assignment. It is a list like Paths, so it needs no body. It
	// took the last free code below 0x80; see the range note at the top.
	hHookParams = 0x1f

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
	hProposals        = 0x4f // proposals: unified pending list, one {kind, path, author, group, size, start, end} per record
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

	// hBufferDeleted is a sparse field parallel to hBuffers: one path per
	// buffer whose file is gone from disk. It rides outside the hBuffers
	// record for the positional-record reason the other per-buffer facts do,
	// and a reader that does not know the field reads every buffer as still
	// present, which is all an old build can express.
	hBufferDeleted = 0x80
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
//
// The table is built in init from the registry in verbs.go, one row per verb,
// so a header and a program cannot disagree about what a verb is. header.go
// owns only how a code is written into a frame.
var verbCodes = map[string]byte{}

// verbNamesByCode inverts verbCodes, for decoding a frame.
var verbNamesByCode = map[byte]string{}

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
//
// Both directions are driven by headerFields below, so a field cannot be
// written by one half and forgotten by the other. The table is in emission
// order: encodeHeader walks it top to bottom, which is what keeps a frame's
// bytes identical to the per-field form it replaced.
func encodeHeader(h Header) []byte {
	var ops []prog.Op
	for _, f := range headerFields {
		f.encode(&h, &ops)
	}
	return prog.Encode(ops)
}

// fieldKind is how a field's value is shaped on the wire.
type fieldKind uint8

const (
	kindNum     fieldKind = iota // a sparse number: zero is absent
	kindStr                      // a sparse string: empty is absent
	kindFlag                     // a presence flag: false is absent
	kindOptNum                   // a number that is present even at zero
	kindStrList                  // a []string in one payload
	kindList                     // a record list, with its companions, via enc/dec
	kindOp                       // the verb: a code, or hOpName text
)

// field is one row of the header codec table.
//
// at returns the address of the field it names, so one row drives both
// encodeHeader and decodeHeader: the encoder reads through it and the decoder
// writes through it. A kindList row leaves at nil and does its work through
// enc and dec, which live side by side so the two halves of a list are read
// together.
//
// name labels the field in a malformed-record error. decodeOnly marks a row
// that exists so an older peer's field is still read: the encoder skips it.
type field struct {
	code       byte
	kind       fieldKind
	at         func(*Header) any
	enc        func(*Header, *[]prog.Op)
	dec        func(*headerDecoder, *prog.Reader) error
	name       string
	decodeOnly bool
}

// headerFields is the one source of truth for the header's fields, in the
// order encodeHeader emits them.
var headerFields = []field{
	{code: hID, kind: kindNum, at: func(h *Header) any { return &h.ID }},
	// h.Op is the one field that is not a plain value: a verb the registry
	// knows goes out as its code, a name it does not as hOpName text, and both
	// decode into h.Op.
	{code: hOp, kind: kindOp, at: func(h *Header) any { return &h.Op }},
	{code: hOpName, kind: kindStr, at: func(h *Header) any { return &h.Op }, decodeOnly: true},
	{code: hPath, kind: kindStr, at: func(h *Header) any { return &h.Path }},
	{code: hNewPath, kind: kindStr, at: func(h *Header) any { return &h.NewPath }},
	{code: hAuthor, kind: kindNum, at: func(h *Header) any { return &h.Author }},
	{code: hGen, kind: kindNum, at: func(h *Header) any { return &h.Gen }},
	// hGenOut is read from older servers. Before the duplicate write was
	// dropped, encodeHeader wrote h.Gen twice — as hGen and as hGenOut — and
	// decodeHeader read both. A peer still in the field may send 0x5e, so the
	// alias stays decodable; this encoder never emits it, which is what frees
	// the code once no such peer remains.
	{code: hGenOut, kind: kindNum, at: func(h *Header) any { return &h.Gen }, decodeOnly: true},
	{code: hToken, kind: kindStr, at: func(h *Header) any { return &h.Token }},
	// A pointer because zero is a real version and "not stated" is not the
	// same as version zero: an apply with no base is refused, an apply based on
	// version zero is the first one against a fresh buffer.
	{code: hBase, kind: kindOptNum, at: func(h *Header) any { return &h.Base }},
	{code: hCancel, kind: kindNum, at: func(h *Header) any { return &h.Cancel }},
	{code: hLine, kind: kindNum, at: func(h *Header) any { return &h.Line }},
	{code: hCol, kind: kindNum, at: func(h *Header) any { return &h.Col }},
	{code: hReviewList, kind: kindFlag, at: func(h *Header) any { return &h.ReviewList }},
	{code: hAnnotated, kind: kindFlag, at: func(h *Header) any { return &h.Annotated }},
	{code: hCreate, kind: kindFlag, at: func(h *Header) any { return &h.Create }},
	{code: hDiscard, kind: kindFlag, at: func(h *Header) any { return &h.Discard }},
	{code: hForce, kind: kindFlag, at: func(h *Header) any { return &h.Force }},
	{code: hExecProjected, kind: kindFlag, at: func(h *Header) any { return &h.ExecProjected }},
	{code: hClaimAdd, kind: kindFlag, at: func(h *Header) any { return &h.ClaimAdd }},
	{code: hClaimClear, kind: kindFlag, at: func(h *Header) any { return &h.ClaimClear }},
	{code: hWithdraw, kind: kindFlag, at: func(h *Header) any { return &h.Withdraw }},
	{code: hApprove, kind: kindFlag, at: func(h *Header) any { return &h.Approve }},
	{code: hHidden, kind: kindFlag, at: func(h *Header) any { return &h.Hidden }},
	// The four span fields are pointers for the same reason as Base: zero is a
	// real offset and "not stated" is not the same as offset zero — a read or
	// dump with -start 0 asks for the head of the file, an absent one asks for
	// the whole of it. The optional kind keeps a stated zero on the wire.
	{code: hStart, kind: kindOptNum, at: func(h *Header) any { return &h.Start }},
	{code: hEnd, kind: kindOptNum, at: func(h *Header) any { return &h.End }},
	{code: hLineStart, kind: kindOptNum, at: func(h *Header) any { return &h.LineStart }},
	{code: hLineEnd, kind: kindOptNum, at: func(h *Header) any { return &h.LineEnd }},
	{code: hGroup, kind: kindNum, at: func(h *Header) any { return &h.Group }},
	{code: hIdentity, kind: kindStr, at: func(h *Header) any { return &h.Identity }},
	{code: hName, kind: kindStr, at: func(h *Header) any { return &h.Name }},
	{code: hKind, kind: kindStr, at: func(h *Header) any { return &h.Kind }},
	{code: hTask, kind: kindStr, at: func(h *Header) any { return &h.Task }},
	{code: hLandTask, kind: kindStr, at: func(h *Header) any { return &h.LandTask }},
	{code: hState, kind: kindStr, at: func(h *Header) any { return &h.State }},
	{code: hStateOn, kind: kindStr, at: func(h *Header) any { return &h.StateOn }},
	{code: hStateNote, kind: kindStr, at: func(h *Header) any { return &h.StateNote }},
	{code: hTo, kind: kindStr, at: func(h *Header) any { return &h.To }},
	{code: hMessage, kind: kindStr, at: func(h *Header) any { return &h.Message }},
	{code: hDir, kind: kindStr, at: func(h *Header) any { return &h.Dir }},
	{code: hExit, kind: kindNum, at: func(h *Header) any { return &h.Exit }},
	{code: hStream, kind: kindNum, at: func(h *Header) any { return &h.Stream }},
	{code: hOutLen, kind: kindNum, at: func(h *Header) any { return &h.OutLen }},
	{code: hFinal, kind: kindFlag, at: func(h *Header) any { return &h.Final }},
	{code: hOK, kind: kindFlag, at: func(h *Header) any { return &h.OK }},
	{code: hRemains, kind: kindFlag, at: func(h *Header) any { return &h.Remains }},
	{code: hCreated, kind: kindFlag, at: func(h *Header) any { return &h.Created }},
	{code: hErr, kind: kindStr, at: func(h *Header) any { return &h.Err }},
	{code: hRoot, kind: kindStr, at: func(h *Header) any { return &h.Root }},
	{code: hRoots, kind: kindStrList, at: func(h *Header) any { return &h.Roots }, name: "roots"},
	{code: hPID, kind: kindNum, at: func(h *Header) any { return &h.PID }},
	{code: hVersion, kind: kindNum, at: func(h *Header) any { return &h.Version }},
	{code: hBytes, kind: kindNum, at: func(h *Header) any { return &h.Bytes }},
	{code: hLines, kind: kindNum, at: func(h *Header) any { return &h.Lines }},
	{code: hFound, kind: kindFlag, at: func(h *Header) any { return &h.Found }},
	{code: hFindStart, kind: kindNum, at: func(h *Header) any { return &h.FindStart }},
	{code: hFindEnd, kind: kindNum, at: func(h *Header) any { return &h.FindEnd }},
	{code: hFindCount, kind: kindNum, at: func(h *Header) any { return &h.FindCount }},
	{code: hFiles, kind: kindNum, at: func(h *Header) any { return &h.Files }},
	{code: hConsidered, kind: kindNum, at: func(h *Header) any { return &h.Considered }},
	{code: hCapped, kind: kindFlag, at: func(h *Header) any { return &h.Capped }},
	{code: hDump, kind: kindNum, at: func(h *Header) any { return &h.DumpID }},
	{code: hHash, kind: kindStr, at: func(h *Header) any { return &h.Hash }},
	{code: hSnapshotJSON, kind: kindStr, at: func(h *Header) any { return &h.SnapshotJSON }},
	{code: hEncodingJSON, kind: kindStr, at: func(h *Header) any { return &h.EncodingJSON }},
	{code: hSnapshotPath, kind: kindStr, at: func(h *Header) any { return &h.SnapshotPath }},
	{code: hLSPMode, kind: kindStr, at: func(h *Header) any { return &h.LSPMode }},
	{code: hLSPJSON, kind: kindStr, at: func(h *Header) any { return &h.LSPJSON }},
	{code: hGitMode, kind: kindStr, at: func(h *Header) any { return &h.GitMode }},
	{code: hGitRev, kind: kindStr, at: func(h *Header) any { return &h.GitRev }},
	{code: hGitCount, kind: kindNum, at: func(h *Header) any { return &h.GitCount }},
	{code: hGitJSON, kind: kindStr, at: func(h *Header) any { return &h.GitJSON }},
	{code: hHookMode, kind: kindStr, at: func(h *Header) any { return &h.HookMode }},
	{code: hHookName, kind: kindStr, at: func(h *Header) any { return &h.HookName }},
	{code: hHookJSON, kind: kindStr, at: func(h *Header) any { return &h.HookJSON }},
	{code: hRetryAfterMS, kind: kindNum, at: func(h *Header) any { return &h.RetryAfterMS }},
	{code: hHookRunID, kind: kindNum, at: func(h *Header) any { return &h.HookRunID }},
	{code: hHookRevision, kind: kindNum, at: func(h *Header) any { return &h.HookRevision }},
	{code: hHookHead, kind: kindStr, at: func(h *Header) any { return &h.HookHead }},
	{code: hHookDirty, kind: kindStr, at: func(h *Header) any { return &h.HookDirty }},
	{code: hHookDurationMS, kind: kindNum, at: func(h *Header) any { return &h.HookDurationMS }},
	{code: hHookTruncated, kind: kindFlag, at: func(h *Header) any { return &h.HookTruncated }},
	{code: hHookOff, kind: kindFlag, at: func(h *Header) any { return &h.HookOff }},
	{code: hHookLogJSON, kind: kindStr, at: func(h *Header) any { return &h.HookLogJSON }},
	{code: hHookPSJSON, kind: kindStr, at: func(h *Header) any { return &h.HookPSJSON }},
	{code: hDiffJSON, kind: kindStr, at: func(h *Header) any { return &h.DiffJSON }},
	{code: hStatesJSON, kind: kindStr, at: func(h *Header) any { return &h.StatesJSON }},
	{code: hSrcVersion, kind: kindStr, at: func(h *Header) any { return &h.SrcVersion }},

	// List fields, in emission order. The record lists name their paired
	// enc/dec; a []string list is one row. A companion field carries only dec
	// and is decodeOnly, because its main list's encoder emits it.
	{code: hArgv, kind: kindStrList, at: func(h *Header) any { return &h.Argv }, name: "argv"},
	{code: hPaths, kind: kindStrList, at: func(h *Header) any { return &h.Paths }, name: "paths"},
	{code: hHookParams, kind: kindStrList, at: func(h *Header) any { return &h.HookParams }, name: "hook params"},
	{code: hQuery, kind: kindList, enc: encodeQuery, dec: decodeQuery, name: "search query"},
	{code: hHunks, kind: kindList, enc: encodeHunks, dec: decodeHunks, name: "hunks"},
	{code: hDirty, kind: kindList, enc: encodeDirty, dec: decodeDirty, name: "dirty"},
	{code: hStats, kind: kindList, enc: encodeStats, dec: decodeStats, name: "stats"},
	{code: hParticipants, kind: kindList, enc: encodeParticipants, dec: decodeParticipants, name: "participants"},
	{code: hParticipantState, kind: kindList, dec: decodeParticipantState, name: "participant state", decodeOnly: true},
	{code: hGroups, kind: kindList, enc: encodeGroups, dec: decodeGroups, name: "groups"},
	{code: hGroupOverlaps, kind: kindList, dec: decodeGroupOverlaps, name: "group overlaps", decodeOnly: true},
	{code: hGroupInvalid, kind: kindList, dec: decodeGroupInvalid, name: "group invalid", decodeOnly: true},
	{code: hTasks, kind: kindList, enc: encodeTasks, dec: decodeTasks, name: "tasks"},
	{code: hMessages, kind: kindList, enc: encodeMessages, dec: decodeMessages, name: "messages"},
	{code: hMessageFrom, kind: kindList, dec: decodeMessageFrom, name: "message from", decodeOnly: true},
	{code: hLand, kind: kindList, enc: encodeLand, dec: decodeLand, name: "land"},
	{code: hClaims, kind: kindStrList, at: func(h *Header) any { return &h.Claims }, name: "claims"},
	{code: hClaimWarnings, kind: kindStrList, at: func(h *Header) any { return &h.ClaimWarnings }, name: "claim warnings"},
	{code: hClaimOverlaps, kind: kindList, enc: encodeClaimOverlaps, dec: decodeClaimOverlaps, name: "claim overlaps"},
	{code: hDeletions, kind: kindList, enc: encodeDeletions, dec: decodeDeletions, name: "deletions"},
	{code: hDirRemovals, kind: kindList, enc: encodeDirRemovals, dec: decodeDirRemovals, name: "dir removals"},
	{code: hProposals, kind: kindList, enc: encodeProposals, dec: decodeProposals, name: "proposals"},
	{code: hReveals, kind: kindList, enc: encodeReveals, dec: decodeReveals, name: "reveals"},
	{code: hEntries, kind: kindList, enc: encodeEntries, dec: decodeEntries, name: "entries"},
	{code: hBuffers, kind: kindList, enc: encodeBuffers, dec: decodeBuffers, name: "buffers"},
	{code: hBufferState, kind: kindList, dec: decodeBufferState, name: "buffer state", decodeOnly: true},
	{code: hBufferSuperseded, kind: kindList, dec: decodeBufferSuperseded, name: "buffer superseded", decodeOnly: true},
	{code: hBufferHeadless, kind: kindList, dec: decodeBufferHeadless, name: "buffer headless", decodeOnly: true},
	{code: hBufferDeleted, kind: kindList, dec: decodeBufferDeleted, name: "buffer deleted", decodeOnly: true},
	{code: hTruncated, kind: kindList, enc: encodeTruncated, dec: decodeTruncated, name: "truncated"},
	{code: hMatches, kind: kindList, enc: encodeMatches, dec: decodeMatches, name: "matches"},
	{code: hMatchLineStart, kind: kindList, dec: decodeMatchLineStart, name: "match line starts", decodeOnly: true},
	{code: hMatchLineEnd, kind: kindList, dec: decodeMatchLineEnd, name: "match line ends", decodeOnly: true},
	{code: hMatchVersion, kind: kindList, dec: decodeMatchVersion, name: "match versions", decodeOnly: true},
	{code: hMatchContext, kind: kindList, dec: decodeMatchContext, name: "match context", decodeOnly: true},
	{code: hConflicts, kind: kindList, enc: encodeConflicts, dec: decodeConflicts, name: "conflicts"},
	{code: hConflictGroup, kind: kindList, dec: decodeConflictGroup, name: "conflict groups", decodeOnly: true},
	{code: hConflictLease, kind: kindList, dec: decodeConflictLease, name: "conflict lease", decodeOnly: true},
	{code: hApplyWarnings, kind: kindList, enc: encodeWarnings, dec: decodeWarnings, name: "apply warnings"},
	{code: hSpans, kind: kindList, enc: encodeSpans, dec: decodeSpans, name: "spans"},
}

// headerFieldsByCode indexes the table for decoding, so decodeHeader looks a
// code up rather than switching on it. Unknown codes are absent and skipped,
// which is the header's forward-compatibility rule.
var headerFieldsByCode = func() map[byte]field {
	m := make(map[byte]field, len(headerFields))
	for _, f := range headerFields {
		m[f.code] = f
	}
	return m
}()

// encode writes one field's op, if it has something to say. It is the whole of
// a scalar's encode side, and a kindList row hands off to its own encoder.
func (f field) encode(h *Header, ops *[]prog.Op) {
	if f.decodeOnly {
		return
	}
	switch f.kind {
	case kindNum:
		if v := numAt(f.at(h)); v != 0 {
			*ops = append(*ops, prog.Op{Code: f.code, Payload: prog.Number(v)})
		}
	case kindStr:
		if s := strAt(f.at(h)); s != "" {
			*ops = append(*ops, prog.Op{Code: f.code, Payload: []byte(s)})
		}
	case kindFlag:
		if flagAt(f.at(h)) {
			*ops = append(*ops, prog.Op{Code: f.code})
		}
	case kindOptNum:
		if v, ok := optNumAt(f.at(h)); ok {
			*ops = append(*ops, prog.Op{Code: f.code, Payload: prog.Number(v)})
		}
	case kindStrList:
		xs := strListAt(f.at(h))
		if len(xs) > 0 {
			var w prog.Writer
			for _, s := range xs {
				w.Str(s)
			}
			*ops = append(*ops, prog.Op{Code: f.code, Payload: w.Done()})
		}
	case kindOp:
		s := strAt(f.at(h))
		if c, ok := code(verbCodes, s); ok {
			*ops = append(*ops, prog.Op{Code: hOp, Payload: prog.Number(int(c))})
		} else if s != "" {
			*ops = append(*ops, prog.Op{Code: hOpName, Payload: []byte(s)})
		}
	case kindList:
		f.enc(h, ops)
	}
}

// The scalar accessors read and write the concrete field a row points at. A
// row's at returns one of the pointer types below; anything else is a table
// mistake, so it panics here rather than putting a wrong value on the wire.
func numAt(p any) int {
	switch v := p.(type) {
	case *int:
		return *v
	case *int64:
		return int(*v)
	case *uint8:
		return int(*v)
	case *uint64:
		return int(*v)
	}
	panic("control: header field is not a number")
}

func setNum(p any, n int) {
	switch v := p.(type) {
	case *int:
		*v = n
	case *int64:
		*v = int64(n)
	case *uint8:
		*v = uint8(n)
	case *uint64:
		*v = uint64(n)
	default:
		panic("control: header field is not a number")
	}
}

func strAt(p any) string       { return *p.(*string) }
func flagAt(p any) bool        { return *p.(*bool) }
func strListAt(p any) []string { return *p.(*[]string) }

func optNumAt(p any) (int, bool) {
	switch v := p.(type) {
	case **int:
		if *v == nil {
			return 0, false
		}
		return **v, true
	case **uint64:
		if *v == nil {
			return 0, false
		}
		return int(**v), true
	}
	panic("control: header optional field is not a pointer")
}

func setOptNum(p any, n int) {
	switch v := p.(type) {
	case **int:
		x := n
		*v = &x
	case **uint64:
		x := uint64(n)
		*v = &x
	default:
		panic("control: header optional field is not a pointer")
	}
}

// decodeHeader reads what encodeHeader wrote.
//
// The scalar table decodes by code lookup. An unknown field is dropped rather
// than refused: nil is passed as the known set to prog.Decode, which reads nil
// as "every opcode is known", so it returns an op for a code this function has
// no row for and the lookup misses it. Header codes are allocated in the
// argument range below 0x80, so the frame is kept rather than refused.
//
// The sparse companion fields — a list's extra facts that would misalign an
// older reader if appended to its positional record — are collected in
// headerDecoder and merged into the header once every op has been read, so
// their position relative to their main list does not matter.
func decodeHeader(b []byte) (Header, error) {
	ops, err := prog.Decode(b, nil)
	if err != nil {
		return Header{}, fmt.Errorf("%w: %v", errBadFrame, err)
	}
	var d headerDecoder
	for _, op := range ops {
		f, ok := headerFieldsByCode[op.Code]
		if !ok {
			continue
		}
		if err := f.decodeField(&d, op.Payload); err != nil {
			return Header{}, err
		}
	}
	return d.finish(), nil
}

// headerDecoder is the in-progress header plus the sparse companion fields'
// payloads. Each is merged into the header by finish after every op is read.
type headerDecoder struct {
	h Header

	participantStates []participantState
	participantTasks  map[int]string
	groupTasks        map[int]string
	groupOverlaps     [][]GroupOverlap
	groupInvalid      []invalidCollider
	messageFroms      []messageFrom
	bufferStates      []bufferState
	supersededStates  []bufferSuperseded
	headless          []string
	deletedBuffers    []string
	lineStarts        []int
	lineEnds          []int
	matchVersions     []int
	matchContexts     []string
	conflictGroups    []int
	conflictLeases    []conflictLease
}

// decodeField writes one field's payload through its row. A scalar row writes
// the field in place; a kindList row calls its paired decoder.
func (f field) decodeField(d *headerDecoder, payload []byte) error {
	switch f.kind {
	case kindNum:
		setNum(f.at(&d.h), prog.ReadNumber(payload))
	case kindStr:
		*f.at(&d.h).(*string) = string(payload)
	case kindFlag:
		*f.at(&d.h).(*bool) = true
	case kindOptNum:
		setOptNum(f.at(&d.h), prog.ReadNumber(payload))
	case kindStrList:
		p := f.at(&d.h).(*[]string)
		r := prog.NewReader(payload)
		for r.More() {
			*p = append(*p, r.Str())
		}
		return recordsOK(r, f.name)
	case kindOp:
		d.h.Op = nameFor(verbNamesByCode, prog.ReadNumber(payload))
	case kindList:
		return f.dec(d, prog.NewReader(payload))
	}
	return nil
}

// finish merges every sparse companion field into the header. Each companion
// is keyed to its main list by position or by path, so its op may arrive before
// or after the list it belongs to.
//
// The two keyed lists get an index built once here — one buffer lookup by path,
// one participant lookup by id — rather than a scan per companion, and each
// merge then walks its own sparse field in one pass.
func (d *headerDecoder) finish() Header {
	buffers := indexByPath(d.h.Buffers)
	participants := indexByID(d.h.Participants)
	d.mergeParticipants(participants)
	d.mergeBuffers(buffers)
	d.mergeMatches()
	d.mergeConflicts()
	d.mergeGroups()
	d.mergeMessages()
	return d.h
}

// indexByPath maps each buffer's path to its position. The buffer companions
// travel by path, so indexing once keeps each of them a single pass. The first
// row for a path wins, as the scan it replaces did.
func indexByPath(bs []Buffer) map[string]int {
	m := make(map[string]int, len(bs))
	for i, b := range bs {
		if _, ok := m[b.Path]; !ok {
			m[b.Path] = i
		}
	}
	return m
}

// indexByID maps each participant's id to its position, for the same reason:
// the state and task companions name a participant by id.
func indexByID(ps []Participant) map[uint8]int {
	m := make(map[uint8]int, len(ps))
	for i, p := range ps {
		if _, ok := m[p.ID]; !ok {
			m[p.ID] = i
		}
	}
	return m
}

// mergeParticipants folds the working state and the task companions into the
// roster, by id.
func (d *headerDecoder) mergeParticipants(idx map[uint8]int) {
	for _, ps := range d.participantStates {
		if i, ok := idx[ps.id]; ok {
			d.h.Participants[i].State = ps.state
			d.h.Participants[i].Declared = ps.declared
			d.h.Participants[i].SinceMS = ps.sinceMS
			d.h.Participants[i].Note = ps.note
			d.h.Participants[i].On = ps.on
		}
	}
	for i, task := range d.participantTasks {
		if i < len(d.h.Participants) {
			d.h.Participants[i].Task = task
		}
	}
}

// mergeBuffers folds the pending/moved, superseded, headless and deleted
// companions into the buffer list, by path.
func (d *headerDecoder) mergeBuffers(idx map[string]int) {
	for _, st := range d.bufferStates {
		if i, ok := idx[st.path]; ok {
			d.h.Buffers[i].Pending, d.h.Buffers[i].Moved = st.pending, st.moved
		}
	}
	for _, st := range d.supersededStates {
		if i, ok := idx[st.path]; ok {
			d.h.Buffers[i].Superseded = st.count
		}
	}
	for _, p := range d.headless {
		if i, ok := idx[p]; ok {
			d.h.Buffers[i].Headless = true
		}
	}
	for _, p := range d.deletedBuffers {
		if i, ok := idx[p]; ok {
			d.h.Buffers[i].Deleted = true
		}
	}
}

// mergeMatches folds the four positional companions into the hit list in one
// pass. A companion is as long as the list, or shorter when the sender omitted
// a field that was zero throughout.
func (d *headerDecoder) mergeMatches() {
	for i := range d.h.Matches {
		if i < len(d.lineStarts) {
			d.h.Matches[i].LineStart = d.lineStarts[i]
		}
		if i < len(d.lineEnds) {
			d.h.Matches[i].LineEnd = d.lineEnds[i]
		}
		if i < len(d.matchVersions) {
			d.h.Matches[i].Version = uint64(d.matchVersions[i])
		}
		if i < len(d.matchContexts) {
			d.h.Matches[i].Context = d.matchContexts[i]
		}
	}
}

// mergeConflicts folds the lease owner and its author/span into the conflict
// list in one pass.
func (d *headerDecoder) mergeConflicts() {
	for i := range d.h.Conflicts {
		if i < len(d.conflictGroups) {
			d.h.Conflicts[i].Group = uint64(d.conflictGroups[i])
		}
		if i < len(d.conflictLeases) {
			l := d.conflictLeases[i]
			d.h.Conflicts[i].Author = uint8(l.author)
			d.h.Conflicts[i].Start = l.start
			d.h.Conflicts[i].End = l.end
		}
	}
}

// mergeGroups folds the overlap, invalid and task companions into the group
// list in one pass.
func (d *headerDecoder) mergeGroups() {
	for i := range d.h.Groups {
		if i < len(d.groupOverlaps) && len(d.groupOverlaps[i]) > 0 {
			d.h.Groups[i].Overlaps = &GroupOverlaps{Sets: d.groupOverlaps[i]}
		}
		if i < len(d.groupInvalid) && d.groupInvalid[i].invalid {
			d.h.Groups[i].Invalid = true
			d.h.Groups[i].InvalidBy = d.groupInvalid[i].by
		}
		if task, ok := d.groupTasks[i]; ok {
			d.h.Groups[i].Task = task
		}
	}
}

// mergeMessages folds the sender reply target into the message list in one
// pass.
func (d *headerDecoder) mergeMessages() {
	for i := range d.h.Messages {
		if i < len(d.messageFroms) {
			d.h.Messages[i].FromKey = d.messageFroms[i].key
			d.h.Messages[i].FromName = d.messageFroms[i].name
		}
	}
}

// --- Query ---

func encodeQuery(h *Header, ops *[]prog.Op) {
	q := h.Query
	if q == nil {
		return
	}
	var w prog.Writer
	w.Str(q.Text).Str(q.Include).Str(q.Exclude).Bool(q.Regex).Bool(q.Case).Bool(q.Word)
	// Path is appended rather than carved into the middle of the record: a
	// reader that predates it reads the six fields it knows and ignores the
	// trailing bytes, and a reader that knows it reads the seventh. No element
	// count to disagree about, so the addition is invisible to the old end.
	//
	// Hidden is appended after Path for the same reason: a reader that knows it
	// reads one more field, and one that predates it reads what it knows and
	// leaves the flag false, which is the default walk.
	w.Str(q.Path).Bool(q.Hidden).Num(q.Context)
	*ops = append(*ops, prog.Op{Code: hQuery, Payload: w.Done()})
}

func decodeQuery(d *headerDecoder, r *prog.Reader) error {
	q := SearchQuery{Text: r.Str(), Include: r.Str(), Exclude: r.Str()}
	q.Regex, q.Case, q.Word = r.Bool(), r.Bool(), r.Bool()
	// The path is the last field, so a payload from before it existed leaves
	// q.Path empty rather than an error: the value is absent, which is the same
	// thing as no scope. Hidden and Context are newer still, and their absence
	// reads the same way.
	q.Path = r.Str()
	q.Hidden = r.Bool()
	q.Context = r.Num()
	d.h.Query = &q
	return nil
}

// --- Hunks ---

func encodeHunks(h *Header, ops *[]prog.Op) {
	if len(h.Hunks) == 0 {
		return
	}
	var w prog.Writer
	for _, x := range h.Hunks {
		w.Num(x.Start).Num(x.End).Num(x.Len)
	}
	*ops = append(*ops, prog.Op{Code: hHunks, Payload: w.Done()})
}

func decodeHunks(d *headerDecoder, r *prog.Reader) error {
	for r.More() {
		d.h.Hunks = append(d.h.Hunks, HunkMeta{Start: r.Num(), End: r.Num(), Len: r.Num()})
	}
	return recordsOK(r, "hunks")
}

// --- Dirty ---

func encodeDirty(h *Header, ops *[]prog.Op) {
	if len(h.Dirty) == 0 {
		return
	}
	var w prog.Writer
	for _, x := range h.Dirty {
		w.Str(x.Path).Bool(x.AgentOnly)
	}
	*ops = append(*ops, prog.Op{Code: hDirty, Payload: w.Done()})
}

func decodeDirty(d *headerDecoder, r *prog.Reader) error {
	for r.More() {
		d.h.Dirty = append(d.h.Dirty, DirtyBuffer{Path: r.Str(), AgentOnly: r.Bool()})
	}
	return recordsOK(r, "dirty")
}

// --- Stats ---

func encodeStats(h *Header, ops *[]prog.Op) {
	s := h.Stats
	if s == (ExecStats{}) {
		return
	}
	var w prog.Writer
	w.Num(s.Runs).Num(s.Stale).Num(s.AgentOnly)
	*ops = append(*ops, prog.Op{Code: hStats, Payload: w.Done()})
}

func decodeStats(d *headerDecoder, r *prog.Reader) error {
	d.h.Stats = ExecStats{Runs: r.Num(), Stale: r.Num(), AgentOnly: r.Num()}
	return nil
}

// --- Participants, with their working state ---

func encodeParticipants(h *Header, ops *[]prog.Op) {
	if len(h.Participants) == 0 {
		return
	}
	var w prog.Writer
	for _, p := range h.Participants {
		kind, ok := code(kindCodes, string(p.Kind))
		w.Num(int(p.ID)).Str(p.Identity).Str(p.Name).Num(int(kind)).Bool(p.Connected)
		if !ok {
			// A kind this build does not know still has to arrive; the zero
			// code means "read the name that follows".
			w.Str(string(p.Kind))
		}
	}
	*ops = append(*ops, prog.Op{Code: hParticipants, Payload: w.Done()})
	// The working state rides in a field of its own rather than inside the
	// hParticipants record: records are positional, so an older reader would
	// read a state code as the next record's id. Every participant gets a
	// record, so a reader that knows the field reads state for all of them and
	// one that does not skips it whole.
	var sts prog.Writer
	for _, p := range h.Participants {
		st, _ := code(workCodes, p.State)
		decl, _ := code(workCodes, p.Declared)
		sts.Num(int(p.ID)).Num(int(st)).Num(int(decl)).Num(int(p.SinceMS)).Str(p.Note).Str(p.On)
	}
	*ops = append(*ops, prog.Op{Code: hParticipantState, Payload: sts.Done()})
}

func decodeParticipants(d *headerDecoder, r *prog.Reader) error {
	for r.More() {
		p := Participant{ID: uint8(r.Num()), Identity: r.Str(), Name: r.Str()}
		kind := r.Num()
		p.Connected = r.Bool()
		if kind == 0 {
			p.Kind = Kind(r.Str())
		} else {
			p.Kind = Kind(nameFor(kindNamesByCode, kind))
		}
		d.h.Participants = append(d.h.Participants, p)
	}
	return recordsOK(r, "participants")
}

func decodeParticipantState(d *headerDecoder, r *prog.Reader) error {
	for r.More() {
		d.participantStates = append(d.participantStates, participantState{
			id:       uint8(r.Num()),
			state:    nameFor(workNamesByCode, r.Num()),
			declared: nameFor(workNamesByCode, r.Num()),
			sinceMS:  int64(r.Num()),
			note:     r.Str(),
			on:       r.Str(),
		})
	}
	return recordsOK(r, "participant state")
}

// --- Groups, with their overlap lists and Invalid flags ---

func encodeGroups(h *Header, ops *[]prog.Op) {
	if len(h.Groups) == 0 {
		return
	}
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
	*ops = append(*ops, prog.Op{Code: hGroups, Payload: w.Done()})

	// Overlap lists ride in their own sparse field, keyed to the groups by
	// position: one count per group, then that many {group, author, start, end}
	// records. A count is written for every group so the order matches hGroups,
	// and the field is emitted only when some set overlaps, so a response with
	// nothing to report is unchanged. A separate field rather than a wider
	// hGroups record keeps an older reader from reading an overlap count as the
	// next group's id.
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
		*ops = append(*ops, prog.Op{Code: hGroupOverlaps, Payload: ovs.Done()})
	}

	// Invalid flags ride the same positional, sparse style: a flag per group,
	// then -- only when the flag is set -- the collider's group, author and
	// span. The field is emitted only when some group is invalid, so a response
	// with none is unchanged, and a separate field rather than a wider hGroups
	// record keeps an older reader from reading a flag as the next group's id.
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
		*ops = append(*ops, prog.Op{Code: hGroupInvalid, Payload: invs.Done()})
	}
}

func decodeGroups(d *headerDecoder, r *prog.Reader) error {
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
		d.h.Groups = append(d.h.Groups, g)
	}
	return recordsOK(r, "groups")
}

func decodeGroupOverlaps(d *headerDecoder, r *prog.Reader) error {
	for r.More() {
		n := r.Num()
		sets := make([]GroupOverlap, 0, n)
		for k := 0; k < n; k++ {
			sets = append(sets, GroupOverlap{
				Group: uint64(r.Num()), Author: uint8(r.Num()),
				Start: r.Num(), End: r.Num()})
		}
		d.groupOverlaps = append(d.groupOverlaps, sets)
	}
	return recordsOK(r, "group overlaps")
}

func decodeGroupInvalid(d *headerDecoder, r *prog.Reader) error {
	for r.More() {
		ic := invalidCollider{invalid: r.Num() != 0}
		if ic.invalid {
			by := GroupOverlap{Group: uint64(r.Num()), Author: uint8(r.Num()),
				Start: r.Num(), End: r.Num()}
			// A zero group means the set is invalid but no single collider can
			// be named, not a collider with id zero.
			if by.Group != 0 {
				ic.by = &by
			}
		}
		d.groupInvalid = append(d.groupInvalid, ic)
	}
	return recordsOK(r, "group invalid")
}

// --- Tasks, which span participants and groups ---

func encodeTasks(h *Header, ops *[]prog.Op) {
	var w prog.Writer
	any := false
	for _, p := range h.Participants {
		any = any || p.Task != ""
	}
	for _, g := range h.Groups {
		any = any || g.Task != ""
	}
	if !any {
		return
	}
	// The task each writer's changes belong to rides in a sparse field of its
	// own: a sequence of {index, kind, task} records, kind 0 a participant and
	// 1 a group. A header with none is byte-for-byte what it always was, and a
	// reader that does not know the field skips it whole.
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
	*ops = append(*ops, prog.Op{Code: hTasks, Payload: w.Done()})
}

func decodeTasks(d *headerDecoder, r *prog.Reader) error {
	if d.participantTasks == nil {
		d.participantTasks = map[int]string{}
		d.groupTasks = map[int]string{}
	}
	for r.More() {
		index, kind := r.Num(), r.Num()
		task := r.Str()
		if kind == 0 {
			d.participantTasks[index] = task
		} else {
			d.groupTasks[index] = task
		}
	}
	return recordsOK(r, "tasks")
}

// --- Messages, with their durable reply targets ---

func encodeMessages(h *Header, ops *[]prog.Op) {
	if len(h.Messages) == 0 {
		return
	}
	var w prog.Writer
	for _, m := range h.Messages {
		w.Num(int(m.From)).Str(m.Text)
	}
	*ops = append(*ops, prog.Op{Code: hMessages, Payload: w.Done()})

	// The sender's reply target rides in its own sparse field, one record per
	// message, so a reader that knows only hMessages skips it whole and still
	// reads the text from the participant list.
	var froms prog.Writer
	var anyFrom bool
	for _, m := range h.Messages {
		froms.Str(m.FromKey).Str(m.FromName)
		if m.FromKey != "" || m.FromName != "" {
			anyFrom = true
		}
	}
	if anyFrom {
		*ops = append(*ops, prog.Op{Code: hMessageFrom, Payload: froms.Done()})
	}
}

func decodeMessages(d *headerDecoder, r *prog.Reader) error {
	for r.More() {
		d.h.Messages = append(d.h.Messages, Message{From: uint8(r.Num()), Text: r.Str()})
	}
	return recordsOK(r, "messages")
}

func decodeMessageFrom(d *headerDecoder, r *prog.Reader) error {
	for r.More() {
		d.messageFroms = append(d.messageFroms, messageFrom{key: r.Str(), name: r.Str()})
	}
	return recordsOK(r, "message from")
}

// --- Land ---

func encodeLand(h *Header, ops *[]prog.Op) {
	if len(h.Land) == 0 {
		return
	}
	var w prog.Writer
	for _, f := range h.Land {
		w.Str(f.Path).Num(f.Sets).Bool(f.Saved).Bool(f.Held).Str(f.Err)
	}
	*ops = append(*ops, prog.Op{Code: hLand, Payload: w.Done()})
}

func decodeLand(d *headerDecoder, r *prog.Reader) error {
	for r.More() {
		d.h.Land = append(d.h.Land, LandFile{Path: r.Str(), Sets: r.Num(),
			Saved: r.Bool(), Held: r.Bool(), Err: r.Str()})
	}
	return recordsOK(r, "land")
}

// --- ClaimOverlaps ---

func encodeClaimOverlaps(h *Header, ops *[]prog.Op) {
	if len(h.ClaimOverlaps) == 0 {
		return
	}
	var w prog.Writer
	for _, o := range h.ClaimOverlaps {
		w.Str(o.Path).Str(o.Identity).Num(int(o.Author))
	}
	*ops = append(*ops, prog.Op{Code: hClaimOverlaps, Payload: w.Done()})
}

func decodeClaimOverlaps(d *headerDecoder, r *prog.Reader) error {
	for r.More() {
		d.h.ClaimOverlaps = append(d.h.ClaimOverlaps, ClaimOverlap{
			Path: r.Str(), Identity: r.Str(), Author: uint8(r.Num())})
	}
	return recordsOK(r, "claim overlaps")
}

// --- Deletions ---

func encodeDeletions(h *Header, ops *[]prog.Op) {
	if len(h.Deletions) == 0 {
		return
	}
	var w prog.Writer
	for _, x := range h.Deletions {
		w.Str(x.Path).Num(int(x.Author))
	}
	*ops = append(*ops, prog.Op{Code: hDeletions, Payload: w.Done()})
}

func decodeDeletions(d *headerDecoder, r *prog.Reader) error {
	for r.More() {
		d.h.Deletions = append(d.h.Deletions, Deletion{Path: r.Str(), Author: uint8(r.Num())})
	}
	return recordsOK(r, "deletions")
}

// --- DirRemovals ---

func encodeDirRemovals(h *Header, ops *[]prog.Op) {
	if len(h.DirRemovals) == 0 {
		return
	}
	var w prog.Writer
	for _, x := range h.DirRemovals {
		w.Str(x.Path).Num(int(x.Author))
	}
	*ops = append(*ops, prog.Op{Code: hDirRemovals, Payload: w.Done()})
}

func decodeDirRemovals(d *headerDecoder, r *prog.Reader) error {
	for r.More() {
		d.h.DirRemovals = append(d.h.DirRemovals, DirRemoval{Path: r.Str(), Author: uint8(r.Num())})
	}
	return recordsOK(r, "dir removals")
}

// --- Proposals ---

func encodeProposals(h *Header, ops *[]prog.Op) {
	if len(h.Proposals) == 0 {
		return
	}
	var w prog.Writer
	for _, p := range h.Proposals {
		w.Str(p.Kind).Str(p.Path).Num(int(p.Author)).Num(int(p.Group)).Num(p.Size).Num(p.Start).Num(p.End)
	}
	*ops = append(*ops, prog.Op{Code: hProposals, Payload: w.Done()})
}

func decodeProposals(d *headerDecoder, r *prog.Reader) error {
	for r.More() {
		d.h.Proposals = append(d.h.Proposals, Proposal{
			Kind: r.Str(), Path: r.Str(), Author: uint8(r.Num()),
			Group: uint64(r.Num()), Size: r.Num(), Start: r.Num(), End: r.Num()})
	}
	return recordsOK(r, "proposals")
}

// --- Reveals ---

func encodeReveals(h *Header, ops *[]prog.Op) {
	if len(h.Reveals) == 0 {
		return
	}
	var w prog.Writer
	for _, x := range h.Reveals {
		w.Str(x.Path).Num(x.Start).Num(x.End)
	}
	*ops = append(*ops, prog.Op{Code: hReveals, Payload: w.Done()})
}

func decodeReveals(d *headerDecoder, r *prog.Reader) error {
	for r.More() {
		d.h.Reveals = append(d.h.Reveals, Reveal{Path: r.Str(), Start: r.Num(), End: r.Num()})
	}
	return recordsOK(r, "reveals")
}

// --- Entries ---

func encodeEntries(h *Header, ops *[]prog.Op) {
	if len(h.Entries) == 0 {
		return
	}
	var w prog.Writer
	for _, e := range h.Entries {
		// Size is -1 when absent, so a zero-byte regular file keeps its zero
		// and a directory stays distinguishable from an empty one.
		size := -1
		if e.Size != nil {
			size = int(*e.Size)
		}
		w.Str(e.Name).Str(e.Path).Bool(e.Dir).Num(size)
	}
	*ops = append(*ops, prog.Op{Code: hEntries, Payload: w.Done()})
}

func decodeEntries(d *headerDecoder, r *prog.Reader) error {
	for r.More() {
		e := Entry{Name: r.Str(), Path: r.Str(), Dir: r.Bool()}
		if size := r.Num(); size >= 0 {
			s := int64(size)
			e.Size = &s
		}
		d.h.Entries = append(d.h.Entries, e)
	}
	return recordsOK(r, "entries")
}

// --- Buffers, with pending/moved, superseded, headless and deleted ---

func encodeBuffers(h *Header, ops *[]prog.Op) {
	if len(h.Buffers) == 0 {
		return
	}
	var w prog.Writer
	for _, b := range h.Buffers {
		w.Str(b.Path).Num(int(b.Version)).Bool(b.Dirty).Num(b.Bytes).Num(b.Lines).Bool(b.Active)
	}
	*ops = append(*ops, prog.Op{Code: hBuffers, Payload: w.Done()})

	// Pending and Moved ride in their own sparse field, one record per buffer
	// that has either, rather than as two more fields on each hBuffers record.
	// Records are positional — no element count and no per-field opcode — so an
	// older reader would read a nonzero pending count as the next record's
	// path. An unknown argument field, by contrast, is skipped: an old reader
	// loses the counts and keeps the buffers, and an old server omits the field
	// so the counts read zero.
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
		*ops = append(*ops, prog.Op{Code: hBufferState, Payload: counts.Done()})
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
		*ops = append(*ops, prog.Op{Code: hBufferSuperseded, Payload: superseded.Done()})
	}

	// The path of a headless buffer rides in a sparse field of its own too, one
	// record per headless buffer. The reason matches the counts above: an
	// hBuffers record is positional, so a field added inside one would be read
	// as the next record path by an older reader. An absent field is skipped
	// whole, so an old client reads every buffer as tabbed — the only state an
	// old build could make.
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
		*ops = append(*ops, prog.Op{Code: hBufferHeadless, Payload: headless.Done()})
	}

	// The paths of deleted files ride in a sparse field of their own too, one
	// record per buffer whose file is gone. The reason is the same as the
	// fields above -- a flag added inside an hBuffers record would be read as
	// the next record's path by an older reader -- and the absence is read as
	// "still on disk", which is what every buffer was before this field.
	var deleted prog.Writer
	var anyDeleted bool
	for _, b := range h.Buffers {
		if !b.Deleted {
			continue
		}
		anyDeleted = true
		deleted.Str(b.Path)
	}
	if anyDeleted {
		*ops = append(*ops, prog.Op{Code: hBufferDeleted, Payload: deleted.Done()})
	}
}

func decodeBuffers(d *headerDecoder, r *prog.Reader) error {
	for r.More() {
		d.h.Buffers = append(d.h.Buffers, Buffer{
			Path: r.Str(), Version: uint64(r.Num()), Dirty: r.Bool(),
			Bytes: r.Num(), Lines: r.Num(), Active: r.Bool()})
	}
	return recordsOK(r, "buffers")
}

func decodeBufferState(d *headerDecoder, r *prog.Reader) error {
	for r.More() {
		d.bufferStates = append(d.bufferStates, bufferState{path: r.Str(), pending: r.Num(), moved: r.Num()})
	}
	return recordsOK(r, "buffer state")
}

func decodeBufferSuperseded(d *headerDecoder, r *prog.Reader) error {
	for r.More() {
		d.supersededStates = append(d.supersededStates, bufferSuperseded{path: r.Str(), count: r.Num()})
	}
	return recordsOK(r, "buffer superseded")
}

func decodeBufferHeadless(d *headerDecoder, r *prog.Reader) error {
	for r.More() {
		d.headless = append(d.headless, r.Str())
	}
	return recordsOK(r, "buffer headless")
}

func decodeBufferDeleted(d *headerDecoder, r *prog.Reader) error {
	for r.More() {
		d.deletedBuffers = append(d.deletedBuffers, r.Str())
	}
	return recordsOK(r, "buffer deleted")
}

// --- Truncated ---

func encodeTruncated(h *Header, ops *[]prog.Op) {
	if len(h.Truncated) == 0 {
		return
	}
	var w prog.Writer
	for _, t := range h.Truncated {
		w.Str(t.Path).Num(t.Shown).Num(t.Total)
	}
	*ops = append(*ops, prog.Op{Code: hTruncated, Payload: w.Done()})
}

func decodeTruncated(d *headerDecoder, r *prog.Reader) error {
	for r.More() {
		d.h.Truncated = append(d.h.Truncated, TruncatedFile{
			Path: r.Str(), Shown: r.Num(), Total: r.Num()})
	}
	return recordsOK(r, "truncated")
}

// --- Matches, with line start/end, version and context ---

func encodeMatches(h *Header, ops *[]prog.Op) {
	if len(h.Matches) == 0 {
		return
	}
	var w prog.Writer
	for _, m := range h.Matches {
		w.Num(m.Line).Num(m.Col).Num(m.Len).Num(m.PathLen).Num(m.TextLen).
			Num(m.ByteStart).Num(m.ByteEnd)
	}
	*ops = append(*ops, prog.Op{Code: hMatches, Payload: w.Done()})

	// LineStart rides in its own sparse field rather than as another Num in the
	// positional hMatches record: appending to a record-shaped field would
	// misalign an older reader. It is sent only when some hit is not at offset
	// zero, the field default, so an omitted field means every line start was
	// zero.
	var starts prog.Writer
	anyStart := false
	for _, m := range h.Matches {
		if m.LineStart != 0 {
			anyStart = true
		}
		starts.Num(m.LineStart)
	}
	if anyStart {
		*ops = append(*ops, prog.Op{Code: hMatchLineStart, Payload: starts.Done()})
	}

	// LineEnd rides the same sparse way, for the same reason.
	var ends prog.Writer
	anyEnd := false
	for _, m := range h.Matches {
		if m.LineEnd != 0 {
			anyEnd = true
		}
		ends.Num(m.LineEnd)
	}
	if anyEnd {
		*ops = append(*ops, prog.Op{Code: hMatchLineEnd, Payload: ends.Done()})
	}

	// Version rides the same sparse way, one number per hit, sent only when
	// some hit names a nonzero buffer revision. A hit on a file read from disk
	// carries zero, which is not a revision any buffer can hold, so an omitted
	// field means every hit was the disk's.
	var vers prog.Writer
	anyVersion := false
	for _, m := range h.Matches {
		if m.Version != 0 {
			anyVersion = true
		}
		vers.Num(int(m.Version))
	}
	if anyVersion {
		*ops = append(*ops, prog.Op{Code: hMatchVersion, Payload: vers.Done()})
	}

	// Context rides the same sparse way, one string per hit, in hit order. It
	// is sent only when some hit carries context, so an omitted field means
	// every hit was the hit line alone.
	var contexts prog.Writer
	anyContext := false
	for _, m := range h.Matches {
		if m.Context != "" {
			anyContext = true
		}
		contexts.Str(m.Context)
	}
	if anyContext {
		*ops = append(*ops, prog.Op{Code: hMatchContext, Payload: contexts.Done()})
	}
}

func decodeMatches(d *headerDecoder, r *prog.Reader) error {
	for r.More() {
		d.h.Matches = append(d.h.Matches, MatchMeta{
			Line: r.Num(), Col: r.Num(), Len: r.Num(),
			PathLen: r.Num(), TextLen: r.Num(),
			ByteStart: r.Num(), ByteEnd: r.Num()})
	}
	return recordsOK(r, "matches")
}

func decodeMatchLineStart(d *headerDecoder, r *prog.Reader) error {
	for r.More() {
		d.lineStarts = append(d.lineStarts, r.Num())
	}
	return recordsOK(r, "match line starts")
}

func decodeMatchLineEnd(d *headerDecoder, r *prog.Reader) error {
	for r.More() {
		d.lineEnds = append(d.lineEnds, r.Num())
	}
	return recordsOK(r, "match line ends")
}

func decodeMatchVersion(d *headerDecoder, r *prog.Reader) error {
	for r.More() {
		d.matchVersions = append(d.matchVersions, r.Num())
	}
	return recordsOK(r, "match versions")
}

func decodeMatchContext(d *headerDecoder, r *prog.Reader) error {
	for r.More() {
		d.matchContexts = append(d.matchContexts, r.Str())
	}
	return recordsOK(r, "match context")
}

// --- Conflicts, with their lease owner and span ---

func encodeConflicts(h *Header, ops *[]prog.Op) {
	if len(h.Conflicts) == 0 {
		return
	}
	var w prog.Writer
	for _, c := range h.Conflicts {
		w.Num(c.Index).Num(int(c.At)).Num(c.Hunk.Start).Num(c.Hunk.End).Str(c.Hunk.Text)
	}
	*ops = append(*ops, prog.Op{Code: hConflicts, Payload: w.Done()})

	// The lease owner rides in its own sparse field rather than as another
	// field on the record. hConflicts records are positional, so an older
	// reader would read a sixth field as the next record index and misalign
	// every conflict after it; a separate argument field is skipped whole. One
	// number per conflict, in order; the field is absent when every owner is
	// zero, which is the common stale-offset case.
	var groups prog.Writer
	var any bool
	for _, c := range h.Conflicts {
		if c.Group != 0 {
			any = true
		}
		groups.Num(int(c.Group))
	}
	if any {
		*ops = append(*ops, prog.Op{Code: hConflictGroup, Payload: groups.Done()})
	}

	// The lease owner's author and span ride together in one sparse field,
	// three numbers per conflict, in conflict order, so each pair stays with
	// its own refusal. It is emitted whenever any conflict names a lease; the
	// stale-offset conflicts write zeros.
	var lease prog.Writer
	var anyLease bool
	for _, c := range h.Conflicts {
		if c.Group != 0 {
			anyLease = true
		}
		lease.Num(int(c.Author)).Num(c.Start).Num(c.End)
	}
	if anyLease {
		*ops = append(*ops, prog.Op{Code: hConflictLease, Payload: lease.Done()})
	}
}

func decodeConflicts(d *headerDecoder, r *prog.Reader) error {
	for r.More() {
		d.h.Conflicts = append(d.h.Conflicts, Conflict{
			Index: r.Num(), At: uint64(r.Num()),
			Hunk: Hunk{Start: r.Num(), End: r.Num(), Text: r.Str()}})
	}
	return recordsOK(r, "conflicts")
}

func decodeConflictGroup(d *headerDecoder, r *prog.Reader) error {
	for r.More() {
		d.conflictGroups = append(d.conflictGroups, r.Num())
	}
	return recordsOK(r, "conflict groups")
}

func decodeConflictLease(d *headerDecoder, r *prog.Reader) error {
	for r.More() {
		d.conflictLeases = append(d.conflictLeases, conflictLease{
			author: r.Num(), start: r.Num(), end: r.Num()})
	}
	return recordsOK(r, "conflict lease")
}

// --- Apply warnings ---

func encodeWarnings(h *Header, ops *[]prog.Op) {
	if len(h.Warnings) == 0 {
		return
	}
	var w prog.Writer
	for _, x := range h.Warnings {
		w.Num(int(x.Group)).Num(int(x.Author)).Num(x.Start).Num(x.End)
	}
	*ops = append(*ops, prog.Op{Code: hApplyWarnings, Payload: w.Done()})
}

func decodeWarnings(d *headerDecoder, r *prog.Reader) error {
	for r.More() {
		d.h.Warnings = append(d.h.Warnings, GroupOverlap{
			Group: uint64(r.Num()), Author: uint8(r.Num()),
			Start: r.Num(), End: r.Num()})
	}
	return recordsOK(r, "apply warnings")
}

// --- Spans ---

func encodeSpans(h *Header, ops *[]prog.Op) {
	if len(h.Spans) == 0 {
		return
	}
	var w prog.Writer
	for _, s := range h.Spans {
		w.Num(s.Len).Num(int(s.Author))
	}
	*ops = append(*ops, prog.Op{Code: hSpans, Payload: w.Done()})
}

func decodeSpans(d *headerDecoder, r *prog.Reader) error {
	for r.More() {
		d.h.Spans = append(d.h.Spans, SpanMeta{Len: r.Num(), Author: uint8(r.Num())})
	}
	return recordsOK(r, "spans")
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
