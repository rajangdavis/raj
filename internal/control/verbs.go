package control

import "raj/internal/prog"

// The verb registry is the one place a verb is spelled.
//
// A verb has three faces: the name its handlers and `raj ctl` speak, the wire
// code a header carries in its place, and the program opcode a compiled batch
// uses. Those used to live in separate tables — verbCodes in header.go,
// knownOps and verbByOp in prog.go — with the Dispatch switch as a fourth
// spelling and a test as a fifth, and they drifted: watch was in the program
// names and in neither control table, and find had no wire code at all, so it
// crossed as text. One row per verb ends that. The tables are derived from
// this slice in init; a new verb is a row and a handler, and nothing else. The
// finish a program verb applies to its arguments rides the row too.
//
// The faces are absent differently:
//
//   - wire 0 means no header carries the verb, because only this process sends
//     it: hookprep and searchsnapshot are the two. execcheck and lspprep are
//     in-process halves too, but they kept the codes they already had.
//   - op 0 means a program may not name the verb, and the row says why.
//   - handle nil means the verb is answered before Dispatch, on the reading
//     goroutine or by connection.one, and never reaches the event thread.
//
// Wire codes are frozen. A frame's bytes are a protocol, not an implementation
// detail, so a code is never moved or reused; find's 52 is the one new code
// here.
type verb struct {
	name   string
	wire   byte
	op     byte
	handle func(*Guard, Request) Response
	finish func(*Request, *Hunk, []string)
}

// verbs is every verb, ordered by wire code, then the wireless internal ops.
var verbs = []verb{
	{name: "ping", wire: 1, op: prog.OpPing, handle: dispatchPing},
	{name: "buffers", wire: 2, op: prog.OpBuffers, handle: dispatchBuffers},
	{name: "text", wire: 3, op: prog.OpRead, handle: dispatchText},
	{name: "open", wire: 4, op: prog.OpOpen, handle: dispatchOpen},
	{name: "apply", wire: 5, op: prog.OpApply, handle: dispatchApply},
	{name: "save", wire: 6, op: prog.OpSave, handle: dispatchSave},
	{name: "version", wire: 7, op: prog.OpVersion, handle: dispatchVersion},
	// search streams match frames, so connection.one runs it rather than the
	// event thread; it is a program verb all the same.
	{name: "search", wire: 8, op: prog.OpSearch},
	{name: "groups", wire: 9, op: prog.OpGroups, handle: dispatchGroups},
	{name: "accept", wire: 10, op: prog.OpAccept, handle: dispatchDecide},
	{name: "reject", wire: 11, op: prog.OpReject, handle: dispatchDecide},
	// exec runs a command off the event thread once execcheck admits it.
	{name: "exec", wire: 12, op: prog.OpExec, finish: finishArgv},
	// execcheck is the event-thread half of exec: it decides, and the
	// connection runs the command off the thread. No program names it.
	{name: "execcheck", wire: 13, handle: dispatchExecCheck},
	{name: "stats", wire: 14, op: prog.OpStats, handle: dispatchStats},
	// hello, cancel and recv act on the connection, not on a document, so the
	// reading goroutine answers them before Dispatch is reached.
	{name: "hello", wire: 15},
	{name: "cancel", wire: 16},
	{name: "recv", wire: 17},
	{name: "snapshot", wire: 18, op: prog.OpSnapshot, handle: dispatchSnapshot},
	// prog is the outer frame connection.handle compiles; the event thread
	// never sees it as a request.
	{name: "prog", wire: 19},
	{name: "reload", wire: 20, op: prog.OpReload, handle: dispatchReload},
	// whoami and who are legacy codes: raj ctl's verbs of those names send ping
	// and hello, and no request carries them. The codes stay allocated so
	// nothing else can be given their bytes.
	{name: "whoami", wire: 21},
	{name: "who", wire: 22},
	// send writes a mailbox, which connection.one owns.
	{name: "send", wire: 23},
	{name: "goto", wire: 24, op: prog.OpGoto, handle: dispatchGoto},
	{name: "close", wire: 25, op: prog.OpClose, handle: dispatchClose},
	{name: "dump", wire: 26, op: prog.OpDump, handle: dispatchDump, finish: finishSpan},
	{name: "patch", wire: 27, op: prog.OpPatch, handle: dispatchPatch, finish: finishPatch},
	// lsp runs off the event thread; lspprep is its event-thread admission.
	{name: "lsp", wire: 28, op: prog.OpLSP},
	{name: "lspprep", wire: 29, handle: dispatchLSPPrep},
	{name: "diff", wire: 30, op: prog.OpDiff, handle: dispatchDiff},
	{name: "review", wire: 31, op: prog.OpReview, handle: dispatchReview},
	{name: "clear", wire: 32, op: prog.OpClear, handle: dispatchClear},
	{name: "claim", wire: 33, handle: dispatchClaim},
	{name: "mkdir", wire: 34, handle: dispatchMkdir},
	{name: "delete", wire: 35, handle: dispatchDelete},
	{name: "deletions", wire: 36, handle: dispatchDeletions},
	{name: "rename", wire: 37, handle: dispatchRename},
	{name: "rmdir", wire: 38, handle: dispatchRmdir},
	{name: "rmdirs", wire: 39, handle: dispatchRmdirs},
	{name: "proposals", wire: 40, handle: dispatchProposals},
	{name: "revert", wire: 41, handle: dispatchRevert},
	// token is the server's own secret, answered on the reading goroutine.
	{name: "token", wire: 42},
	{name: "ls", wire: 43, handle: dispatchLs},
	// watch parks until the buffers change generation. A program runs in order,
	// so a watch in the middle would hold every verb behind it, and a caller
	// could not see that it had; it is kept out of programs for recv's reason.
	{name: "watch", wire: 44},
	{name: "git", wire: 45, op: prog.OpGit, handle: dispatchGit},
	// hook authoring is local-only and its run is two-phase, so no program
	// names it; the wire form is a direct frame or `raj hook`.
	{name: "hook", wire: 46, handle: dispatchHook},
	{name: "reveal", wire: 47, op: prog.OpReveal, handle: dispatchReveal, finish: finishSpan},
	// state is the participant roster, which connection.one owns.
	{name: "state", wire: 48},
	{name: "land", wire: 49, handle: dispatchLand},
	{name: "intent", wire: 50, handle: dispatchIntent},
	{name: "screen", wire: 51, handle: dispatchScreen},
	// find locates a pattern in one buffer and answers its byte span, so a
	// program can find, read and apply in one frame.
	{name: "find", wire: 52, op: prog.OpFind, handle: dispatchFind},

	// Wireless internal ops. connection.runHook and connection.search ask the
	// event thread for these in-process; no header spells them, so wire 0.
	{name: "hookprep", handle: dispatchHookPrep},
	{name: "searchsnapshot", handle: dispatchSearchSnapshot},
}

// verbByName is the Dispatch lookup. The tables derived below are all built in
// init, so this slice is the only place any of them is written.
var verbByName map[string]verb

func init() {
	verbByName = make(map[string]verb, len(verbs))
	verbCodes = make(map[string]byte, len(verbs))
	verbNamesByCode = make(map[byte]string, len(verbs))
	verbByOp = make(map[byte]verb, len(verbs))
	for _, v := range verbs {
		verbByName[v.name] = v
		if v.wire != 0 {
			verbCodes[v.name] = v.wire
			verbNamesByCode[v.wire] = v.name
		}
		if v.op != 0 {
			knownOps[v.op] = true
			verbByOp[v.op] = v
		}
	}
}
