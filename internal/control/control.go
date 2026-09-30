// Package control exposes the open buffers over a Unix domain socket, so that
// something outside raj can read and edit them.
//
// This is the seam the in-editor agent pane was replaced by. An editor that
// hosts a model owns that model's lifecycle, configuration, failure modes and
// version skew, and none of that is editing. A socket puts the driver in
// another process, where it can be restarted, replaced, or written in another
// language without touching the editor — and where a crash in it is a crash in
// it rather than in the thing holding your unsaved work.
//
// # Threading
//
// The one hard constraint. Every mutation in raj happens on the goroutine that
// owns the model, between File.Begin and File.End; a socket handler is not that
// goroutine. So a handler does not touch the model at all. It parks the request,
// posts a Wake, and blocks on a reply channel; the event loop drains the queue,
// executes each request where it is safe to, and answers. This is the same
// park-then-Notify shape the search pane uses for results, for the same reason.
//
// The package therefore contains no editor types and cannot mutate anything. It
// moves bytes and requests; internal/app decides what a request means. That
// split is what keeps the unsafe version — "just take a lock and edit from the
// accept goroutine" — from being writable by accident.
//
// # Protocol
//
// Line-delimited JSON, one object per line, request and response. The
// alternative is a framed binary format that cannot be debugged with nc, and
// this socket exists to be driven by things that do not exist yet.
package control

import (
	"bufio"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"raj/internal/git"
	"raj/internal/hooks"
	"raj/internal/hooks/builtin"
	"raj/internal/safe"
)

// Hunk is a replacement of [Start,End) with Text, in bytes, against the version
// named by a request's Base.
type Hunk struct {
	Start int    `json:"start"`
	End   int    `json:"end"`
	Text  string `json:"text"`
}

// Request is one line in.
//
// Base is a pointer so that "not supplied" is distinguishable from version 0:
// an apply with no base is refused rather than silently rebased against
// whatever the buffer happens to be, which is the mistake that corrupts a file
// while looking like it worked.
type Request struct {
	ID   int
	Op   string
	Path string
	// Gen is a watch's starting point: the generation the caller last saw.
	// The watch answers as soon as the editor's generation differs from it,
	// so a driver that reconnects and watches from zero is told to re-read
	// rather than waiting for the next edit.
	Gen uint64
	// NewPath is rename's destination: the second path operand, which no other
	// verb carries. It is encoded sparsely like every other string field, so a
	// peer that does not know the verb omits it and a peer that does reads it
	// alongside Path.
	NewPath string
	// Author is the writer this request claims to be. Zero means "whatever the
	// connection was assigned", and connection.one stamps it so. Any other
	// value must equal the connection's real author: every verb, direct or a
	// program sub-request, is refused otherwise (errAuthorSpoof, or the
	// contract wording save, revert, approve and withdraw pin). send, hook and
	// exec read it as inert data and act as the connection regardless.
	Author uint8
	Base   *uint64
	Hunks  []Hunk
	Query  *SearchQuery
	// Cancel names an in-flight request id to abandon.
	Cancel int
	// Line and Col are a goto: the 1-based position the editor's cursor
	// should move to. Zero means absent; the editor defaults a missing line to
	// the one the cursor is already on, and a missing column to the margin.
	Line int
	Col  int
	// Start and End are a read span in byte offsets. Nil means read the whole
	// file; a missing End (but present Start) reads to the end.
	Start *int
	End   *int
	// LineStart and LineEnd select a read by 1-based inclusive line numbers
	// instead of bytes, for the line a compiler or a search reports. Nil means
	// the byte span decides; the host translates lines to bytes so a driver
	// never re-implements the editor's byte model.
	LineStart *int
	LineEnd   *int

	// GitMode names the git sub-operation: status, diff, show, numstat or
	// log. GitRev is the revision it reads (empty means HEAD) and GitCount
	// caps log. They are read-only: git owns durable history and raj moves
	// no ref through them.
	GitMode  string
	GitRev   string
	GitCount int

	// HookMode names the hook sub-operation: list, show, run, put, rm, enable
	// or disable. HookName is the hook that show, run, rm, enable and disable
	// address, and HookJSON is a put's HookRow as JSON. Authoring is refused
	// over TCP, exactly as exec is; run crosses, because running a hook is what
	// crosses the line, but admission still bounds it: an agent caller needs
	// the hook's Agent flag. list and show are reads that cross freely.
	HookMode string
	HookName string
	HookJSON string
	// HookParams is a run's supplied parameters as NAME=value assignments, in
	// the caller's order. They are validated against the hook's declarations on
	// the event thread and never become argv: the server turns each resolved
	// value into a RAJ_PARAM_<name> environment entry.
	HookParams []string
	// HookRunID names an in-flight run for `hook cancel`. It is the id the
	// final frame's stamp reported and `hook ps` lists.
	HookRunID uint64

	// LSPMode is the sub-operation of an lsp request: hover, definition,
	// references, completion, diagnostics, inlay-hints, symbols or
	// document-symbols. Line and Col name the position to ask about (1-based),
	// which the host maps to the server's UTF-16 coordinates; document-symbols
	// and diagnostics are asked about a file, so they send no position.
	//
	// For symbols, Query carries the workspace/symbol text. It is the generic
	// run of text the wire already knows how to send, which is why this mode
	// rides it rather than a field of its own; the position is still required
	// by the CLI's grammar and is what selects the document's server, but
	// workspace/symbol itself takes only the query.
	LSPMode string
	// ReviewList is set on a review request to return the pending change sets
	// without entering Review mode. Its absence enters the mode, which is what
	// a plain `raj ctl review` asks for; `--json` sets it.
	ReviewList bool
	// Annotated is set on a read to return the per-run change set and state of
	// the review view — every live edit with its owner and its state. The text
	// is the buffer's view either way; the agreed composition as the default is
	// a future change.
	Annotated bool
	// Create, on an open, says a path that is not already a buffer and not on
	// disk is a name to make a new empty buffer for rather than a typo to
	// refuse. Absent means open reaches only something that already exists.
	Create bool

	// Discard, on a close, says to drop a buffer even though it has unsaved
	// changes: the machine form of the editor's close-without-save. Absent is
	// the ordinary close, which refuses a dirty buffer. It is not a text
	// write, so it is deliberately not gated on claims or a prior read.
	Discard bool
	// Force, on a save, answers the disk-changed prompt with Overwrite: the
	// buffer is written over a file that changed since raj last wrote it.
	// Absent is the ordinary save, which refuses a stale disk stamp.
	Force bool
	// Paths, ClaimAdd and ClaimClear are the claim op: the file-level working
	// set an identity declares it is editing. Paths is the set, replaced by
	// default and extended when ClaimAdd is set; ClaimClear releases it. A
	// claim that carries none of the three reports the current set. The state
	// is per identity, keyed by Author.
	//
	// Paths is also a read's operand list: a text request with paths reads each
	// one in a single call, and the Start/End and LineStart/LineEnd span fields
	// select the same span in every one. It is the list shape claim already
	// crosses with, so a peer that does not know the read form reads no targets
	// and falls back to Path.
	Paths      []string
	ClaimAdd   bool
	ClaimClear bool
	// Withdraw, on a delete, retracts this identity's pending deletion for
	// Path instead of proposing one. It crosses as a presence flag like
	// Create, so a peer that does not know it omits it and keeps the
	// proposing default.
	Withdraw bool
	// Approve carries out a pending removal for delete and rmdir: it is the
	// human answer to a proposal. It crosses as a presence flag like Withdraw,
	// so a peer that does not know it omits it and keeps the proposing default.
	Approve bool
	// Hidden, on ls, includes entries the hidden policy would skip — the same
	// include-hidden switch search carries in its query. It crosses as a
	// presence flag like Create, so a peer that does not know it omits it and
	// keeps the filtered default.
	Hidden bool

	// Identity and Name introduce a participant. Identity is durable across
	// connections; Name is for display.
	Identity string
	Name     string
	// Kind is what a hello joins as. Empty means an agent, which is the
	// pre-existing wire default; "human" joins KindHuman, so an attached
	// client can be the second person at the workspace rather than another
	// agent.
	Kind string
	// Task is the work this participant's writes belong to. It rides hello,
	// which stores it on the participant row; a later write does not send it,
	// because the server derives it from the connection's author. Absent means
	// none, the default for the local human and every ordinary connection.
	Task string // State, StateOn and StateNote are a `state set`: the declared working
	// LandTask is a land request's task: the change sets to accept and, when
	// nothing of another task remains pending, save. It is separate from Task,
	// which names the work this connection's own writes belong to, because a
	// land may select work the caller did not author.
	LandTask string
	// state and its optional counterpart and note. An empty State is a read
	// of the connection's own state.
	State     string
	StateOn   string
	StateNote string

	// To and Message are a send: the recipient, named by author id, identity
	// key, display name or "all", and the text for its mailbox. The sender is
	// never a field — it is the connection's own author — so a frame cannot
	// put words in another participant's mouth.
	To      string
	Message string
	// Group addresses a change set for accept, reject and clear.
	Group uint64
	// DumpID addresses a snapshot for patch: the id a prior dump returned.
	// PatchText is the whole edited text the caller hands back, which the
	// editor diffs against the snapshot and rebases onto the current document,
	// so a driver never re-derives offsets from old text.
	DumpID    uint64
	PatchText string
	// Argv is the command for exec, and Dir the directory to run it in.
	Argv []string
	Dir  string
	// ExecProjected runs the command against a materialised projection of the
	// live buffers rather than the worktree: accepted and proposed text
	// included, in a scratch tree. It is exec's switch, so it rides the same
	// op and inherits exec's refusal, streaming and cancellation. Dir is
	// refused with it, because the run directory is the scratch root.
	ExecProjected bool
	// Token authenticates a TCP client. Ignored on a Unix socket, where the
	// filesystem permissions have already decided.
	Token string
	// Program is a batch of requests encoded as opcodes; see prog.go. Present
	// only on the "prog" op, which compiles it and runs the results.
	Program []byte
}

// HookRow is one stored hook as it crosses the control surface. Action is the
// stored JSON: an argv array, {"shell":"..."} or a builtin object.
type HookRow struct {
	Name   string `json:"name"`
	Action string `json:"action"`
	// Params is the stored JSON array of declared parameters; omitted or empty
	// means the hook declares none.
	Params     string `json:"params,omitempty"`
	Trigger    string `json:"trigger"`
	Tree       string `json:"tree"`
	Agent      bool   `json:"agent"`
	CooldownMS int    `json:"cooldown_ms"`
	TimeoutMS  int    `json:"timeout_ms"`
	MayWrite   bool   `json:"may_write"`
	Detach     bool   `json:"detach"`
	Enabled    bool   `json:"enabled"`
}

// Group is a change set: what one apply, or one user action, did. Reviewable as
// a unit because that is already the unit undo reverses. Hunks and Moved are
// the pending projection: how many surviving runs its members cover now, and
// how many a later edit moved past entirely; both are zero for a decided set.
// Invalid names a still-proposed set with no surviving hunk at all, and
// InvalidBy the live set whose edit consumed it.
type Group struct {
	ID     uint64 `json:"id"`
	Path   string `json:"path"`
	Author uint8  `json:"author"`
	State  string `json:"state"` // proposed, accepted, rejected
	// Task is the work this set was opened under, populated by the host so
	// `land` can select by it. It is in-process only: the hGroups wire record
	// is positional, so the field does not cross and a remote `groups` reads
	// it empty.
	Task  string `json:"task,omitempty"`
	Ops   int    `json:"ops"`
	Bytes int    `json:"bytes"`
	First uint64 `json:"first"`
	Last  uint64 `json:"last"`
	Hunks int    `json:"hunks"`
	Moved int    `json:"moved"`
	// Overlaps names the other live change sets whose projected ranges
	// intersect this one's, with the bytes where they meet. Two sets awaiting a
	// decision that claim the same text is a fact the editor reports and does
	// not resolve: the lease still decides what may be written and a person
	// still decides each set. It is a pointer so Group stays comparable, and
	// nil when nothing overlaps.
	Overlaps *GroupOverlaps `json:"overlaps,omitempty"`
	// Invalid marks a still-Proposed set every live member of which a later
	// edit has moved past, so no hunk survives to accept. It is derived, not a
	// fourth state: clear the colliding edit and it clears with it. InvalidBy
	// names the live set whose edit now occupies the range, or is nil when no
	// single collider can be named. Both fields are sparse on the wire; see
	// hGroupInvalid in header.go.
	Invalid   bool          `json:"invalid,omitempty"`
	InvalidBy *GroupOverlap `json:"invalid_by,omitempty"`
}

// GroupOverlap names another live change set whose projected range intersects a
// set's, and the bytes where the two meet in the buffer's current coordinates.
type GroupOverlap struct {
	Group  uint64 `json:"group"`
	Author uint8  `json:"author"`
	Start  int    `json:"start"`
	End    int    `json:"end"`
}

// GroupOverlaps is one change set's overlap list, wrapped so the Group field
// that carries it stays a comparable pointer.
type GroupOverlaps struct {
	Sets []GroupOverlap `json:"sets"`
}

// LandFile is one buffer's outcome in a land report: how many of the task's
// pending sets were accepted there, and whether the buffer was saved, held
// back because another task's pending sets remain, or refused a save for
// another reason. Err names a refusal so a skipped buffer is never silent.
type LandFile struct {
	Path  string `json:"path"`
	Sets  int    `json:"sets"`
	Saved bool   `json:"saved"`
	Held  bool   `json:"held"`
	Err   string `json:"error,omitempty"`
}

// LandExport is the commit-on-land outcome: the one commit on raj/baseline a
// land exported (or found already exported), its base parent and tree, and a
// non-linear dependency warning when the wave's overlap graph could not be
// ordered. Err names an export that failed after the land already accepted and
// saved the work, so the failure is reported rather than hidden. It is absent
// on a land that accepted no set.
type LandExport struct {
	Commit  string `json:"commit,omitempty"`
	Parent  string `json:"parent,omitempty"`
	BaseSHA string `json:"base_sha,omitempty"`
	Tree    string `json:"tree,omitempty"`
	Ref     string `json:"ref,omitempty"`
	Warning string `json:"warning,omitempty"`
	Err     string `json:"error,omitempty"`
}

// DiffGroup is one pending change set rendered for review: the Group as
// `groups` lists it, plus one hunk per member op that still sits where it was
// written. Moved counts members the buffer has moved past — later edits
// reached inside their text, so no honest span exists; MovedHunks carries
// those members recorded old→new text anyway, without coordinates, so a
// reviewer still sees what was written rather than a bare count.
type DiffGroup struct {
	Group
	Hunks      []DiffHunk `json:"hunks"`
	Moved      int        `json:"moved"`
	MovedHunks []DiffHunk `json:"moved_hunks"`
}

// DiffHunk is one member of a change set as old→new text. Start and End are
// byte offsets in the buffer's current coordinates, locating the replacement
// now; Line and EndLine are the 1-based lines that span covers, so a hunk can
// be named without another read. Old is the text the op removed and New the
// text it added. A pure insertion has Old empty; a pure deletion has New empty
// and Start == End. A moved hunk, one no rebase can locate, has Start and End
// both -1 and Line and EndLine both 0.
type DiffHunk struct {
	Start   int    `json:"start"`
	End     int    `json:"end"`
	Line    int    `json:"line"`
	EndLine int    `json:"end_line"`
	Old     string `json:"old"`
	New     string `json:"new"`
}

// DirtyBuffer is an unsaved buffer, and whether every unsaved run in it was
// written by an agent rather than by the human.
type DirtyBuffer struct {
	Path string `json:"path"`
	// AgentOnly is true when no dirty span is the user's. It is the measurement
	// the exec policy is waiting on, not yet a licence to flush.
	AgentOnly bool `json:"agent_only"`
}

// ExecStats counts how often a command ran against files that did not match the
// buffers, which is the measurement that will decide what to do about it.
//
// Stale is runs with at least one unsaved buffer; AgentOnly is the subset where
// the human had written none of the unsaved text, so writing it out would only
// have been flushing an agent's own work. Neither is acted on: the command runs
// either way and the caller is told.
type ExecStats struct {
	Runs      int `json:"runs"`
	Stale     int `json:"stale"`
	AgentOnly int `json:"agent_only"`
}

// SearchQuery is a search over the workspace. The fields mirror the editor's
// own, because it is the same engine: one search means an agent's grep sees
// unsaved edits and cannot disagree with what the user is looking at.
type SearchQuery struct {
	Text    string
	Include string // comma-separated globs
	Exclude string
	// Path limits the walk to a directory, resolved against the workspace
	// root. Empty means the whole workspace. Like exec -dir it is validated
	// against the root before the walk starts, so ".." cannot escape it.
	Path  string
	Regex bool
	Case  bool
	Word  bool
	// Hidden includes paths the internal/hidden policy would skip, for the
	// -hidden switch; false is the policy in force. It is a bool rather than
	// the rules themselves because the rules are the editor's to load, not the
	// caller's to send. Sparse by absence, so a peer that does not know it
	// keeps the filtered default.
	Hidden bool
	// Context is how many full lines before and after each hit the engine
	// returns with it, so a caller sees a hit in place without a follow-up
	// read. Zero is the hit line alone, which is the behaviour before the
	// option existed; a negative value is clamped to zero rather than refused.
	// Sparse by absence, so a peer that does not know it searches without
	// context.
	Context int
}

// SearchMatch is one hit. LineStart..LineEnd bound the whole line the hit was
// found on, and ByteStart..ByteEnd bound the match within it. Text is that line
// trimmed of trailing space, so it can end before LineEnd.
//
// Version is the buffer revision of the open document the hit was found in, or
// 0 for a file read from disk with no buffer behind it. It is what lets a
// caller tell that the document moved between the search and a later read: a
// hit whose file now reports a different version is stale and must be re-found.
type SearchMatch struct {
	Path      string `json:"path"`
	Line      int    `json:"line"` // 1-based
	Col       int    `json:"col"`  // byte offset of the match within Text
	Len       int    `json:"len"`
	LineStart int    `json:"line_start"` // byte offset of the start of the hit line within the file
	LineEnd   int    `json:"line_end"`   // one past the last byte of the hit line, excluding the newline
	ByteStart int    `json:"byte_start"` // byte offset of the match within the file
	ByteEnd   int    `json:"byte_end"`   // one past the last byte of the match within the file
	Version   uint64 `json:"version"`    // buffer version the hit was found in, or 0 for disk
	Text      string `json:"text"`
	// Context is the hit's line plus the requested number of lines either
	// side, when search -context asked for them, and empty otherwise. It is
	// the neighbouring text itself rather than a range, because the caller
	// that asked for context wants it without a second call. Sparse by
	// absence, so a peer that does not know the field reads no context.
	Context string `json:"context,omitempty"`
}

// TruncatedFile is one file the per-file cap cut down: Shown is how many rows
// the search reported, Total how many matches the file holds. It is what makes
// a capped file distinguishable from one that holds exactly the cap.
type TruncatedFile struct {
	Path  string `json:"path"`
	Shown int    `json:"shown"`
	Total int    `json:"total"`
}

// Span is one authored run of the document. Reads come back as spans rather
// than as one string because that is how the document is stored: pieces carry
// an author, so a caller learns who wrote each byte without a second call.
type Span struct {
	Text   string
	Author uint8
}

// Mine reports whether this span was written by the given author. It exists so
// the comparison is written once: an agent asking "is this my text" is the
// question the whole attribution scheme is for, and getting it slightly wrong
// — comparing against the constant Agent rather than against the id you were
// assigned — silently answers for the wrong writer.
func (s Span) Mine(author uint8) bool { return s.Author == author }

// ByUser reports text the human typed, which is the span kind an agent must
// never quietly discard.
func (s Span) ByUser() bool { return s.Author == AuthorUser }

// StateRun is one run of an annotated read: the bytes [Off, Off+Len) of the
// returned text, the change set that put them there (0 is the file as loaded),
// and what was decided about it. Offsets are relative to the text the read
// returned, so they line up with the spans a caller was handed.
type StateRun struct {
	Off   int    `json:"off"`
	Len   int    `json:"len"`
	Group uint64 `json:"group"`
	State string `json:"state"`
}

// AuthorOriginal is the file as loaded and AuthorUser is the human. Agents
// start at FirstAgent. These are the piece table's own numbers, deliberately:
// attribution is a property of the store, not something the transport invents.
const (
	AuthorOriginal uint8 = 0
	AuthorUser     uint8 = 1
)

// Buffer describes one buffer the editor is tracking: a tab the user can see,
// or a headless one loaded over the control socket for reading alone.
type Buffer struct {
	Path    string `json:"path"`
	Version uint64 `json:"version"`
	Dirty   bool   `json:"dirty"`
	// Bytes is the buffer's length in a `buffers` reply; in a multi-target
	// `read` it is the bytes that target contributed to the concatenation.
	Bytes  int  `json:"bytes"`
	Lines  int  `json:"lines"`
	Active bool `json:"active"`
	// Headless is true when the buffer is loaded and addressable over the
	// socket but has no tab: `read`, `version` and `lsp diagnostics` load on
	// demand, so an inspection leaves nothing in front of the user. A
	// headless buffer can never hold a pending proposal — a proposal
	// announces it, which is what puts a tab on screen for review.
	Headless bool `json:"headless"`
	// Deleted is true when the file backing this buffer is gone from disk: it
	// was there when raj read or wrote it and it is not there now. It tells a
	// buffer that is merely unsaved from one whose file someone removed
	// underneath it, and it rides its own sparse wire field rather than a
	// field on the positional hBuffers record, for the same reason Pending,
	// Superseded and Headless do.
	Deleted bool `json:"deleted,omitempty"`
	// Pending is how many change sets in this buffer are still proposed, and
	// Moved how many of their members a later edit has moved past so no honest
	// span can be projected. They let one `buffers` call answer "which open
	// files hold decisions" instead of 1 + N `groups` calls. Both travel in
	// their own sparse header field rather than as two more hBuffers fields:
	// records are positional, so appending would make an older reader read a
	// pending count as the next buffer's path.
	Pending int `json:"pending"`
	Moved   int `json:"moved"`
	// Superseded is how many of this buffer's proposed sets are invalid: a
	// still-Proposed set with no surviving hunk, which Pending drops and accept
	// or reject cannot reach, and which a save disposes deliberately rather
	// than writing. It is a field of its own rather than folded into Pending
	// because such a file reports no pending set and yet is not clean, and
	// "holds a set a save will discard" has to be legible. It travels in its
	// own sparse header field, for the same positional-record reason Pending
	// and Moved do.
	Superseded int `json:"superseded,omitempty"`
	// Tally is the buffer's brace balance, string- and comment-aware, or nil
	// when the buffer is unnamed or unreadable. It is computed CLI-side from
	// the live text (unsaved edits included), not carried on the wire header:
	// moving it host-side wants the hBuffers encoding, which is frozen pending
	// the flat-record conversion.
	Tally *BraceTally `json:"tally,omitempty"`
}

// BraceTally is the net balance of one buffer's brackets: openers minus
// closers, per kind. A missing closer reads positive, a stray one negative, so
// a balanced file is all zero and a broken one points at the kind that broke.
// Brackets inside strings and comments do not count.
type BraceTally struct {
	Braces   int `json:"braces"`
	Parens   int `json:"parens"`
	Brackets int `json:"brackets"`
}

// Conflict is a hunk that could not be rebased onto the current version. At is
// the version of the op that invalidated the range, not a byte offset — it tells
// a driver what it missed, so it can re-read from there rather than resubmitting
// the whole diff blind. A lease refusal names no invalidating op, so At is zero
// there: it is not a real version, and the owner below is what the caller acts
// on instead.
//
// Group names the change set whose read-only lease refused the hunk, when that
// is why it could not land; zero means the ordinary stale-offset conflict. It is
// the difference between "read again and resubmit" and "someone has to accept or
// reject this text first", which are opposite instructions for a driver.
//
// Author and Start/End describe that owning set when Group is non-zero: who
// wrote the text and the current rebased span of the run that refused the hunk.
// They are zero for a stale-offset conflict, where there is no owner to name.
// Carrying them here saves the caller a second `groups` call just to learn who
// holds the bytes and where they are.
type Conflict struct {
	Index  int    `json:"index"`
	At     uint64 `json:"at"`
	Group  uint64 `json:"group,omitempty"`
	Author uint8  `json:"author,omitempty"`
	Start  int    `json:"start,omitempty"`
	End    int    `json:"end,omitempty"`
	Hunk   Hunk   `json:"hunk"`
}

// BlockError reports that a verb was refused because a live change set overlaps
// the work it was trying to do. Clear returns it when a rejected set cannot be
// reversed out because a later live set sits on top of its members; Conflict
// carries the blocking set in the same owner-and-span shape a lease refusal
// uses, so the socket reports it rather than flattening it to a sentence.
type BlockError struct {
	Conflict Conflict
	Message  string
}

func (e *BlockError) Error() string { return e.Message }

// LSPResult is one language-server answer, exactly one field set per mode. Its
// JSON tags matter: the CLI marshals it straight to a driver, so the field
// names are the protocol a script reads.
type LSPResult struct {
	Text      string        `json:"text,omitempty"`      // hover
	Locations []LSPLocation `json:"locations,omitempty"` // definition/references
	Items     []LSPItem     `json:"items,omitempty"`     // completion
	Diags     []LSPDiag     `json:"diagnostics,omitempty"`
	Hints     []LSPHint     `json:"hints,omitempty"`
	// Symbols is a workspace/symbol answer: the project-wide declarations the
	// server's index matched against the query. Line and Col are the editor's
	// 1-based position, or zero when the server named only the file.
	Symbols []LSPSymbol `json:"symbols,omitempty"`
	// DocumentSymbols is a textDocument/documentSymbol answer: the declarations
	// of one file, kept as a tree so a hierarchical reply retains its nesting.
	// It is a sibling of Symbols, not a replacement: Symbols is the
	// project-wide index answer, this is the one file's outline.
	DocumentSymbols []LSPDocumentSymbol `json:"documentSymbols,omitempty"`
	// Edits is a formatting answer: the server's text edits in the editor's
	// 1-based line and column coordinates. The ctl mode reports them rather
	// than applying them — the control surface asks, it does not edit — and
	// the human chord is what applies a format through the editor's one-undo
	// application path.
	Edits []LSPEdit `json:"edits,omitempty"`
	// Signatures is a signature-help answer: the candidate signatures and the
	// index the server says is active. ActiveSignature is that top-level index
	// and ActiveParameter its parameter index; a signature's own
	// activeParameter override travels on the signature itself.
	Signatures      []LSPSignature `json:"signatures,omitempty"`
	ActiveSignature int            `json:"activeSignature,omitempty"`
	ActiveParameter int            `json:"activeParameter,omitempty"`
	// Status says whether a diagnostics answer is a real reading of the
	// server's state. It is set only for diagnostics, and never left empty
	// there: "ok" means the list is current, so an empty list means no
	// problems, while anything else means there is no published state to read
	// — no server, or a ready one that has not published about this file yet —
	// and the absent list is not a clean bill of health.
	Status string `json:"status,omitempty"`
	// Detail is the reason for a non-ok status, already worded for a person.
	Detail string `json:"detail,omitempty"`
}

// LSPStatus values for a diagnostics answer, so a caller can tell "no
// problems" from "not a reading of the current text". ok is the only one that
// licenses reading an empty list as clean.
const (
	LSPStatusOK          = "ok"
	LSPStatusUnpublished = "unpublished"
	LSPStatusStale       = "stale"
	LSPStatusStarting    = "starting"
	LSPStatusNotStarted  = "not-started"
	LSPStatusMissing     = "missing"
	LSPStatusNoServer    = "no-server"
	LSPStatusGaveUp      = "gave-up"

	// LSPStatusUnassociated: the server answered, but it has no package or
	// workspace association for the document, so the list is not a reading of
	// the text.
	LSPStatusUnassociated = "unassociated"
)

// LSPLocation is a file and a 1-based line and column — the editor's own
// coordinates rather than the server's UTF-16 ones.
type LSPLocation struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Col  int    `json:"col"`
}

// LSPSymbol is one workspace/symbol answer: a project-wide declaration the
// server's index matched to a query. Container names the enclosing type or
// package when the server sends one. Path is the file it was declared in; Line
// and Col are the editor's 1-based position, or zero when the server named only
// the file and no place in it.
type LSPSymbol struct {
	Name      string `json:"name"`
	Kind      string `json:"kind,omitempty"`
	Container string `json:"container,omitempty"`
	Path      string `json:"path"`
	Line      int    `json:"line,omitempty"`
	Col       int    `json:"col,omitempty"`
}

// LSPDocumentSymbol is one declaration from a textDocument/documentSymbol
// answer, with the editor's 1-based line and column of its identifier. Children
// is the nested list a hierarchical server sent; a flat SymbolInformation reply
// has no nesting and leaves it empty, so a driver must not assume a depth.
type LSPDocumentSymbol struct {
	Name   string `json:"name"`
	Detail string `json:"detail,omitempty"`
	Kind   string `json:"kind,omitempty"`
	// Container is the SymbolInformation containerName. A hierarchical reply
	// names the parent by position in Children instead, so this is usually
	// empty for one.
	Container string              `json:"container,omitempty"`
	Path      string              `json:"path"`
	Line      int                 `json:"line,omitempty"`
	Col       int                 `json:"col,omitempty"`
	Children  []LSPDocumentSymbol `json:"children,omitempty"`
}

// LSPItem is one completion suggestion.
type LSPItem struct {
	Label  string `json:"label"`
	Detail string `json:"detail,omitempty"`
	Kind   string `json:"kind,omitempty"`
}

// LSPSignature is one candidate call signature from a signature-help answer,
// with its documentation already flattened to plain text. ActiveParameter is
// the server's per-signature override, or nil when it sent none and the
// top-level index applies.
type LSPSignature struct {
	Label           string         `json:"label"`
	Documentation   string         `json:"documentation,omitempty"`
	Parameters      []LSPParameter `json:"parameters,omitempty"`
	ActiveParameter *int           `json:"activeParameter,omitempty"`
}

// LSPParameter is one parameter of a signature. Start and End are the server's
// UTF-16 offsets into the signature label, present only when Offsets is true;
// otherwise the label is a plain string the server chose and the parameter
// cannot be located in the signature text.
type LSPParameter struct {
	Label   string `json:"label,omitempty"`
	Start   int    `json:"start,omitempty"`
	End     int    `json:"end,omitempty"`
	Offsets bool   `json:"offsets,omitempty"`
}

// LSPHint is one inlay hint, flattened to the fields a driver needs: a 1-based
// editor position to anchor it at, the label text, and the server's own
// corrections to the document. Padding flags are carried as the server sent
// them because they are part of the label's spacing rather than an editor
// preference; a driver that only prints labels can ignore them.
type LSPHint struct {
	Line         int           `json:"line"`
	Col          int           `json:"col"`
	Text         string        `json:"text"`
	Kind         int           `json:"kind,omitempty"`
	PaddingLeft  bool          `json:"paddingLeft,omitempty"`
	PaddingRight bool          `json:"paddingRight,omitempty"`
	Tooltip      string        `json:"tooltip,omitempty"`
	Edits        []LSPHintEdit `json:"textEdits,omitempty"`
}

// LSPHintEdit is one of a hint's text edits, as an editor byte span and the
// text that replaces it, so a driver never resolves an LSP position itself.
type LSPHintEdit struct {
	Start int    `json:"start"`
	End   int    `json:"end"`
	Text  string `json:"text"`
}

// LSPEdit is one server text edit: a 1-based start line and column, a 1-based
// end line and column (end-exclusive, as the protocol's ranges are), and the
// text that replaces the span. It is the formatting answer's shape, kept in the
// editor's own coordinates so a driver does not re-implement the UTF-16
// conversion the host already did.
type LSPEdit struct {
	Line    int    `json:"line"`
	Col     int    `json:"col"`
	EndLine int    `json:"endLine"`
	EndCol  int    `json:"endCol"`
	Text    string `json:"text"`
}

// LSPDiag is one problem in a file.
type LSPDiag struct {
	Line     int    `json:"line"`
	Col      int    `json:"col"`
	Severity int    `json:"severity"`
	Message  string `json:"message"`
}

// LSPCaller runs one blocking language-server request. The editor hands one
// back after it has synced the document and located the server — both
// event-thread operations — and the caller runs on the connection's goroutine,
// off the event thread, so a slow server never stalls the editor. It never
// crosses the wire, like Searcher.
type LSPCaller interface {
	// Run performs the request and returns the JSON-encoded LSPResult. May
	// block for the server's answer.
	Run(ctx context.Context) (json []byte, err error)
}

// ClaimOverlap names another identity that has claimed a path this one also
// claims. Claims are not locks, so two writers may hold the same file; this
// is how the second one learns who else is there. Identity is resolved from
// the participant registry when one is available and is empty otherwise.
type ClaimOverlap struct {
	Path     string `json:"path"`
	Identity string `json:"identity"`
	Author   uint8  `json:"author"`
}

// Deletion is one pending deletion: a path an agent has proposed to remove and
// the author who proposed it. It is not a change set — a deletion is a
// path-level fact, not text inside one buffer — so it rides its own list
// rather than appearing as a Group.
type Deletion struct {
	Path   string `json:"path"`
	Author uint8  `json:"author"`
}

// DirRemoval is one pending dir-removal: a directory an agent has proposed to
// remove, and the author who proposed it. It is the rmdir analogue of a
// Deletion: a subtree-level fact, not text in one buffer, so it rides its own
// list. rmdir only records the proposal — nothing is removed until the user
// approves in the review tab.
type DirRemoval struct {
	Path   string `json:"path"`
	Author uint8  `json:"author"`
}

// Entry is one child in a directory listing: its name, its absolute path and
// whether it is a directory. Size is set only for a regular file, so a
// directory, a symlink, a device and a fifo all omit it rather than reporting
// the directory's or the link's own byte count as though it were content.
type Entry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Dir  bool   `json:"dir"`
	Size *int64 `json:"size,omitempty"`
}

// Proposal is one entry in the unified pending surface: a change set still
// awaiting a decision, a pending file deletion, a pending directory removal, or
// a pending publish. Kind names which. Group and Start/End describe a change
// set; Start/End are -1 when no honest span exists (a set every member of which
// a later edit has moved past), so a caller knows to ask `diff` instead. Size
// is the row magnitude and is kind-dependent: the net byte change for a set,
// the file byte count for a deletion, the number of files a folder removal
// would take, and zero for a publish, which names a wave rather than content.
type Proposal struct {
	Kind   string `json:"kind"` // "set" | "delete" | "rmdir" | "invalid" | "publish"
	Path   string `json:"path"`
	Author uint8  `json:"author"`
	Group  uint64 `json:"group"`
	Size   int    `json:"size"`
	Start  int    `json:"start"`
	End    int    `json:"end"`
}

// Reveal is one user-initiated request to put a buffer in front of the user,
// optionally at a byte span: the unit the watch answer carries so every
// attached client can mount the path. Start and End are byte offsets, both -1
// for the whole file. It rides the watch answer rather than the reveal request
// (which names the span with the ordinary Start/End fields) because a reveal is
// a broadcast to every client, not a reply to the one that asked.
type Reveal struct {
	Path  string `json:"path"`
	Start int    `json:"start"`
	End   int    `json:"end"`
}

// Response is one line out. Err is a string rather than a code because the
// consumer is a human at a socket at least as often as it is a program.
type Response struct {
	ID   int
	OK   bool
	Err  string
	Root string
	// Roots is the whole workspace root set, primary first, on a ping or
	// buffers reply. It is sparse: a server that does not know the field sends
	// none, and a client reads absence as "no set" and keeps its own root. It
	// is what an attach client adopts as its visible workspace, so the tree
	// and search show the daemon's roots rather than the launch directory.
	Roots []string
	PID   int
	// Projection is the live composition a projected exec materialises, keyed
	// by absolute path. It is in-process only: EncodeResponse builds its
	// header field by field, so a field with no encoder line is dropped, and
	// this one is never encoded. It carries the bytes from the event thread to
	// the connection that builds the scratch tree, without a second round trip.
	// The json tag keeps it out of runProgram --json, the one path that
	// marshals a whole Response.
	Projection map[string][]byte `json:"-"`

	Buffers []Buffer
	// Reveals are the user-initiated reveals a watch answer carries: paths the
	// agent asked to surface, with the byte span to place the caret at (-1,-1
	// for the whole file). Sparse like the other response lists, so a peer that
	// does not know the field reads none and every other reply is unchanged.
	Reveals []Reveal
	Matches []SearchMatch
	Files   int
	// Considered is how many files the walk opened and scanned, as distinct
	// from Files, which held a match: a search under an -include glob that
	// matched no file leaves it zero, and the CLI warns on exactly that.
	Considered int
	Capped     bool
	// Truncated names the files whose rows are fewer than the matches found,
	// each with both numbers. It is independent of Capped, which is the global
	// MaxMatches flag: a per-file cap trips without it. Sent only when
	// nonempty, so a server that does not know the field omits it and a client
	// must read absence as unknown rather than as "nothing was truncated".
	Truncated []TruncatedFile
	Spans     []Span
	Version   uint64
	// Gen is the editor's whole-workspace generation counter. Every answer
	// carries it, so a client can snapshot and arm a watch from the same
	// number; a watch wake carries the generation it woke at, so the client
	// can tell a real change from a repeated notice.
	Gen uint64
	// Bytes and Lines describe the buffer a version answers for, so a driver
	// can size an apply span or find the end of a file without a read.
	Bytes int
	Lines int
	// Line and Col are the caret's 1-based position on the drawn screen, the
	// screen verb's cursor. They reuse hLine/hCol, the goto fields, so a
	// read-only view spends no new header code; zero means no caret is drawn,
	// because it has scrolled out of view or the screen is not showing one.
	Line int
	Col  int
	// Found, FindStart, FindEnd and FindCount are find's answer: whether the
	// pattern occurred at all, the first match's byte span, and how many
	// matches the buffer holds (including the first). Found is sparse, so its
	// absence reads as not found; the span is taken from the omitted-zero
	// defaults, and Version is the revision the span was measured in.
	Found     bool
	FindStart int
	FindEnd   int
	FindCount int
	Conflicts []Conflict
	// Warnings names the other writers' Proposed change sets a successful
	// apply landed over: the advisory-lease half of the reply. Each warning is
	// the overlapped set's group, author and the span it holds now, the same
	// owner-and-span shape a Conflict carries for a refusal. It is sparse: a
	// clean apply sends none, so a peer that does not know the field reads no
	// warnings rather than an error, and a warning is never a refusal.
	Warnings []GroupOverlap
	// DumpID is the snapshot id a dump returned, and Hash the hash of the
	// snapshot's text, so a caller can verify the bytes it holds before editing
	// them and name them back to a patch.
	DumpID uint64
	Hash   string
	// SnapshotJSON is a document snapshot's encoded session, EncodingJSON the
	// file encoding a client needs to rebuild it byte-for-byte, and
	// SnapshotPath the buffer's own absolute path. They are sparse: a response
	// that is not a snapshot sends none, so a peer that does not know the
	// verb reads no document rather than an error.
	SnapshotJSON string
	EncodingJSON string
	SnapshotPath string
	// Author is the id the editor assigned this connection. It rides on every
	// response, not just a handshake, because a client that reconnects or is
	// restarted mid-session would otherwise be holding a stale one — and an
	// agent that thinks it is author 3 when it is author 4 will read its own
	// text as somebody else's.
	Author uint8
	// Searcher is returned by the internal "searchsnapshot" op. It never crosses the
	// wire: it is how the event thread hands a consistent view of the open
	// buffers to a walk that runs off it.
	Searcher Searcher
	// Exit is a finished command's status, and Dirty the buffers that stopped
	// one from starting.
	Exit  int
	Dirty []DirtyBuffer
	Stats ExecStats
	// RetryAfterMS is a refused hook run's retry hint in milliseconds: how
	// long until the in-flight or cooldown reason lifts. Zero means the
	// refusal carries no duration (a policy or unknown-hook refusal).
	RetryAfterMS int `json:"retry_after_ms,omitempty"`
	// HookArgv, HookShell and HookTimeoutMS carry the admitted hook from the
	// event thread to the connection that runs it, in-process, like
	// Projection. EncodeResponse builds its header field by field, so none is
	// wire-encoded, and the json tags keep them out of runProgram --json, the
	// one path that marshals a whole Response.
	HookArgv      []string `json:"-"`
	HookShell     string   `json:"-"`
	HookTree      string   `json:"-"`
	HookMayWrite  bool     `json:"-"`
	HookDetach    bool     `json:"-"`
	HookTimeoutMS int      `json:"-"`

	// HookBuiltin and HookBuiltinArgs carry a builtin action from the event
	// thread to the connection that runs it, on the same in-process route as
	// the fields above. They are sparse: an external hook sends neither.
	HookBuiltin     string         `json:"-"`
	HookBuiltinArgs map[string]any `json:"-"`
	// HookSteps carries a composite action's ordered steps from the event thread
	// to the connection that runs them, on the same in-process route. It is
	// sparse: a single builtin or an external hook sends none.
	HookSteps []hooks.Step `json:"-"`

	// HookParamValues carries a run's resolved declared parameters from the
	// event thread to the connection that runs it, on the same in-process route.
	// The run path renders them as RAJ_PARAM_<name> environment entries and
	// records them in the run log. It is sparse: a hook that declares none sends
	// none.
	HookParamValues []hooks.ParamValue `json:"-"`

	// HookRevision is the projection revision the run was admitted at. It
	// crosses on the final frame too, for a caller that only reads the reply.
	HookRevision uint64
	// HookRunID, HookName, HookHead, HookDirty, HookDurationMS and
	// HookTruncated are the run stamp the final frame carries: what ran, at
	// which revision and git state, how it ended and how long it took. They
	// are sparse: a frame that is not a hook run's final frame sends none, so
	// a peer that does not know them reads zero values.
	HookRunID      uint64
	HookName       string
	HookHead       string
	HookDirty      string
	HookDurationMS int64
	HookTruncated  bool
	// HookOff reports the workspace's hook panic switch; HookLogJSON and
	// HookPSJSON carry the run log and the in-flight list as JSON arrays. All
	// three are reads: they cross either transport.
	HookOff     bool
	HookLogJSON string
	HookPSJSON  string

	Participants []Participant
	Groups       []Group
	// Claims is an identity's claim set in stable order, and ClaimWarnings the
	// per-path notes for operands that were skipped (a claim path that is not
	// on disk). ClaimOverlaps names the other writers sharing one of those
	// paths. All three are the claim verb's answer.
	Claims        []string
	ClaimWarnings []string
	ClaimOverlaps []ClaimOverlap
	// Deletions is the pending-deletion list: each path an agent has proposed
	// to remove and who proposed it. Sparse like the claim lists, so a peer
	// that does not know the field reads no proposals rather than an error.
	Deletions []Deletion
	// DirRemovals is the pending-dir-removal list, the rmdir analogue of
	// Deletions: each directory an agent has proposed to remove and who
	// proposed it. Sparse the same way, so a peer that does not know the
	// field reads no proposals.
	DirRemovals []DirRemoval
	// Proposals is one flat tagged list over the three pending kinds: every
	// open buffer's proposed change sets, the pending file deletions and the
	// pending dir-removals. It is a read-only rollup, ungated like Deletions.
	Proposals []Proposal
	// Entries is ls's answer: the immediate children of the directory the
	// request named, sorted by name. Sparse like the lists above, so an empty
	// directory sends none and a peer that does not know the field reads no
	// entries rather than an error.
	Entries []Entry
	// Land is a land gesture's per-buffer outcome, one record per buffer that
	// held the task's pending sets. Sparse: a response that is not a land
	// sends none.
	Land []LandFile

	// SrcVersion is the revision the server was built from. connection.send
	// stamps it on every response, so a client learns it on any frame, not
	// just the handshake. Identity is on the hello reply only: the token the
	// server minted for an anonymous client, for the client to adopt.
	SrcVersion string
	Identity   string
	// Messages is what recv returns: everything the user has said to this
	// participant since it last asked.
	Messages []Message
	// Stream is output from a running command: 1 stdout, 2 stderr.
	Stream uint8
	// Out is the bytes of a Stream frame.
	Out string
	// Final marks the last frame of a response. Callers that do not stream can
	// ignore it; Client.Do reads until it is set.
	Final bool

	// LSPJSON is the JSON-encoded answer an lsp request returns, and LSP is the
	// blocking caller the internal "lspprep" op hands the connection so the
	// request can run off the event thread. LSP never crosses the wire.
	LSPJSON string
	LSP     LSPCaller
	// GitJSON is the JSON-encoded git.Result the git verb returns: the
	// structured repo read or a patch, nested like LSPJSON so it crosses as
	// one header string.
	GitJSON string
	// HookJSON carries a hook verb's answer or an intent request/answer: a
	// hook list returns every HookRow as a JSON array, show returns one as a
	// JSON object, and the intent op carries its Command in and its Result
	// out. Nested like GitJSON so it crosses as one header string.
	HookJSON string
	// DiffJSON is the JSON-encoded []DiffGroup a diff request returns: the
	// pending change sets as old→new text. Nested like LSPJSON, so it
	// crosses the wire as one header string rather than as flat records.
	DiffJSON string
	// StatesJSON is the JSON-encoded []StateRun an annotated read returns: the
	// per-run owner and state of the returned text. Nested like DiffJSON, so
	// the run list crosses as one header string.
	StatesJSON string

	// Remains is close -discard's answer: whether a file is still on disk at
	// the path the discarded buffer held. Discarding drops the buffer and
	// leaves the file exactly as it was, so a driver that recreated the
	// content under a new name learns here that the old name is still there,
	// rather than only from the editor's status line. It is sparse by absence:
	// a close that leaves nothing (or an ordinary close) sends no field, and a
	// client that does not know it reads no remainder rather than an error.
	Remains bool
	// Created is open's answer: whether the call made a new buffer rather than
	// focusing one that was already loaded. `raj ctl open` prints "created"
	// versus "opened" from it, so a driver that expected a create can tell the
	// two apart. Sparse like Remains: a view that predates it reads no field.
	Created bool
	// Token is the running server's TCP secret, returned by the "token" op.
	// It is how a driver in a container gets the secret without scraping the
	// editor's startup stderr: a local script on the Unix socket reads it
	// and hands it over. Empty on a Unix-only server, which has no secret to
	// give. Sparse by absence, so a peer that predates the verb reads none.
	Token string
}

// Text flattens the spans, for callers that do not care who wrote what.
func (r Response) Text() string {
	if len(r.Spans) == 1 {
		return r.Spans[0].Text
	}
	var b strings.Builder
	for _, s := range r.Spans {
		b.WriteString(s.Text)
	}
	return b.String()
}

// Author ids mirror the piece table's: 0 is the file as loaded, 1 the human,
// and agents start at 2. They are the same numbers, deliberately — attribution
// is a property of the store, not something the transport invents.
const FirstAgent uint8 = 2

// Pending is a parked request awaiting the event thread.
type Pending struct {
	Req  Request
	done chan Response
	once sync.Once
}

// Reply answers a request. Safe to call more than once and from any goroutine;
// only the first answer is sent, so a handler that both errors and returns
// cannot deadlock the connection.
func (p *Pending) Reply(r Response) {
	p.once.Do(func() {
		r.ID = p.Req.ID
		p.done <- r
	})
}

// Fail answers with an error.
func (p *Pending) Fail(format string, args ...any) {
	p.Reply(Response{Err: fmt.Sprintf(format, args...)})
}

// ReplyTimeout bounds how long a handler waits for the event thread. Without it
// a request submitted while raj is quitting, or while a modal dialog is open and
// the loop is elsewhere, hangs the client for ever with no way to tell whether
// it was applied.
const ReplyTimeout = 5 * time.Second

// Server accepts connections and parks their requests.
type Server struct {
	// Notify wakes the event thread. Required: without it a parked request
	// waits for the next idle tick, which is 150 ms of latency for no reason.
	Notify func()

	// Participants maps durable identities to author ids. One per editor, so a
	// harness reconnecting is the same writer it was before.
	Participants *Registry

	// Mail carries messages from the user to a connected driver. It is on the
	// server rather than behind the Host interface because nothing about it
	// touches the document: the editor posts from the event thread and never
	// blocks, and a parked recv reads on its own connection goroutine. Routing
	// it through the event thread would mean parking a request there, which is
	// the one thing that layer must never do.
	Mail Mailbox

	// HookGate and HookRuns are the workspace's hook run state: the gate that
	// coalesces runs and applies the cooldown and per-revision policy, and the
	// registry of runs in flight. They live on the Server, not on a connection,
	// because they are properties of the one running editor: two sockets asking
	// to run the same hook must serialize against each other, and a second
	// `raj hook run` has to see the first's in-flight run. Initialised once in
	// ListenAll and shared by every connection for the server's life.
	HookGate *hookGate
	HookRuns *hooks.Registry

	// HookLog is the in-memory log of the last hooks.DefaultLogSize completed
	// runs, oldest first, and the hookMu-guarded hookOff is the global panic
	// switch: while set, every run is refused before admission. Both live on
	// the Server for the same reason HookGate and HookRuns do -- they are
	// properties of the one running editor, not of a connection -- and both
	// are in memory by design.
	HookLog *hooks.Log

	// HookDir is the directory detached runs write their logs and exit files
	// to, alongside the journal and trash in the workspace state directory. It
	// is empty when the app has no state directory, which leaves detached runs
	// unable to start rather than the editor failing to start.
	HookDir string

	hookMu  sync.Mutex
	hookOff bool

	// heartbeat is a test seam: the per-connection heartbeat interval, with
	// the zero value leaving a connection on heartbeatEvery. It is a server
	// field rather than a package var so a test shortens its own server's
	// connections without a live ticker reading state another test can write.
	// Read under mu in serve and written under mu by a test, so the seam has a
	// happens-before edge even when it is set after the accept loops start.
	heartbeat time.Duration

	// watch generation. gen moves on the event thread; a parked watch compares
	// it against the Gen its request carried and wakes when they differ. The
	// lock is separate from mu because a bump happens on the event thread
	// while mu may be held by a submit, and waits on neither.
	watchMu  sync.Mutex
	gen      uint64
	watchers map[uint64]chan struct{}
	watchSeq uint64
	// reveals is the recent broadcast history: each reveal tagged with the
	// generation it was published at. A watcher takes the entries newer than
	// the generation its request named, so every attached client sees a reveal
	// once and a reconnect does not replay old ones.
	reveals []revealAt

	path  string
	paths []string
	token string
	// lns is every listener the server accepts on, and socks the Unix paths it
	// must unlink when it closes. More than one listener is how one session is
	// driven from both sides of a container boundary — the socket where the
	// filesystem authorises, the port where a shared token does — and every
	// listener accepts into the same queue and participant registry.
	lns    []net.Listener
	socks  []string
	remote bool

	mu     sync.Mutex
	queue  []*Pending
	closed bool
	// joins holds the author ids whose rows have joined since the event thread
	// last persisted them. Join runs on a connection goroutine, so it queues
	// the id here and wakes the editor; the journal write happens on the
	// editor own thread. Keyed by id, so a reconnecting hello does not queue
	// the same row twice. Guarded by mu.
	joins map[uint8]bool
}

// Send queues a message from the person at the keyboard to a participant.
//
// The editor's half of the mailbox. It is here rather than on Mailbox so that
// the checks live with the registry that can answer them: a message addressed
// to an id nobody holds is a bug worth reporting, not an entry in a map that
// nothing will ever read.
//
// A disconnected recipient is allowed on purpose. Its mailbox is keyed on the
// author id, which is durable across reconnects, so telling a harness something
// while it is restarting is delivered when it comes back rather than lost.
func (s *Server) Send(to uint8, text string) error {
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("nothing to send")
	}
	p, ok := s.Participants.Get(to)
	if !ok {
		return fmt.Errorf("no participant with author id %d", to)
	}
	if p.Kind != KindAgent {
		return fmt.Errorf("%s is not a driver", p.Name)
	}
	return s.Mail.Post(to, Message{From: AuthorUser, Text: text})
}

// PostNotice queues an automatic notice from the editor to a participant: the
// other writer into the same mailbox Send fills. It is not something a person
// said, so it names AuthorOriginal as the sender and skips Send's registry
// checks. The recipient is a superseded proposal's author, which may be a
// provisional connection with no registry row, and the mailbox is keyed on the
// author id alone. Best-effort like Send's callers -- a full box is an error
// the caller drops, because the write it describes has already landed.
func (s *Server) PostNotice(to uint8, text string) error {
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("nothing to send")
	}
	return s.Mail.Post(to, Message{From: AuthorOriginal, Text: text})
}

// Roster returns every participant with its working state filled, for who and
// the hello reply. A hook run in flight under an author keeps it working even
// while it sends no requests.
func (s *Server) Roster() []Participant {
	return s.Participants.States(s.hookRunInFlight)
}

// noteJoin queues an author id whose row is new or changed, for the editor to
// persist on its own thread, and wakes it. It is the registry joined hook, so it
// runs on the connection goroutine that answered the hello and must stay cheap:
// it only touches this map. Nothing here reads the document or the filesystem.
func (s *Server) noteJoin(id uint8) {
	s.mu.Lock()
	if s.joins == nil {
		s.joins = map[uint8]bool{}
	}
	fresh := !s.joins[id]
	s.joins[id] = true
	s.mu.Unlock()
	if fresh && s.Notify != nil {
		s.Notify()
	}
}

// TakeJoins returns and clears the author ids queued since the last call, in id
// order. The editor drains it on the event thread and persists each row; a row
// unchanged since it was last written appends nothing.
func (s *Server) TakeJoins() []uint8 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.joins) == 0 {
		return nil
	}
	out := make([]uint8, 0, len(s.joins))
	for id := range s.joins {
		out = append(out, id)
	}
	s.joins = nil
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// hookRunInFlight reports whether author has a hook run in flight. A nil
// registry is no runs, which is the case before ListenAll wires one.
func (s *Server) hookRunInFlight(author uint8) bool {
	if s.HookRuns == nil {
		return false
	}
	for _, run := range s.HookRuns.List() {
		if run.Author == author {
			return true
		}
	}
	return false
}

// participant returns one participant with its working state filled.
func (s *Server) participant(id uint8) (Participant, bool) {
	for _, p := range s.Roster() {
		if p.ID == id {
			return p, true
		}
	}
	return Participant{}, false
}

// Drivers lists the participants a message can be sent to, connected first.
// The disconnected are still listed, because their mail keeps.
func (s *Server) Drivers() []Participant {
	var out []Participant
	for _, p := range s.Participants.List() {
		if p.Kind == KindAgent {
			out = append(out, p)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Connected && !out[j].Connected })
	return out
}

// MaxMessage is the longest text one send may carry. A message is a note to a
// peer — paths, group ids, a few lines — and the mailbox holds sixteen of them
// in memory, so a caller with more to say belongs in a document it points at.
const MaxMessage = 16 << 10

// SendFrom queues a message from one participant to another: the agent half of
// the mailbox Send fills for the person at the keyboard.
//
// to names the recipient by author id, durable identity (the key register
// minted), or display name, and "all" (or "*") is every connected driver but
// the sender. A recipient must be a driver, as with Send: a person reads the
// editor, not a mailbox. Sending to yourself is refused rather than delivered,
// because it is always a mistake in the name and never a message.
//
// from is the caller's real author, never a request field. It returns the
// participants the message was queued for.
func (s *Server) SendFrom(from uint8, to, text string) ([]Participant, error) {
	// A sender that never declared an identity holds a reserved id with no
	// registry row, so the recipient's recv cannot resolve a reply target and
	// its answer has nowhere to go. Refuse rather than deliver a dead end.
	if s.Participants.IsProvisional(from) {
		return nil, fmt.Errorf("send needs a registered identity: run `raj ctl register` (or pass --as KEY) first")
	}
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("nothing to send")
	}
	if len(text) > MaxMessage {
		return nil, fmt.Errorf("message is %d bytes, over the %d-byte limit; put the detail in a file and send its path", len(text), MaxMessage)
	}
	to = strings.TrimSpace(to)
	if to == "" {
		return nil, fmt.Errorf("send needs a recipient: --to KEY, NAME, ID or all")
	}
	var targets []Participant
	if to == "all" || to == "*" {
		for _, p := range s.Participants.List() {
			if p.Kind == KindAgent && p.Connected && p.ID != from {
				targets = append(targets, p)
			}
		}
		if len(targets) == 0 {
			return nil, fmt.Errorf("no other driver is connected")
		}
	} else {
		p, err := s.resolveRecipient(to)
		if err != nil {
			return nil, err
		}
		if p.ID == from {
			return nil, fmt.Errorf("%s is you; name another participant", to)
		}
		if p.Kind != KindAgent {
			return nil, fmt.Errorf("%s is not a driver; a person reads the editor, not a mailbox", p.Name)
		}
		targets = []Participant{p}
	}
	// The reply target is the sender's durable identity and name, captured
	// before the post so a replayed message can still name where to reply
	// after the sender has gone or the id has been re-seeded.
	senderKey, senderName := "", ""
	if p, ok := s.Participants.Get(from); ok {
		senderKey, senderName = p.Identity, p.Name
	}
	var sent []Participant
	for _, p := range targets {
		if err := s.Mail.Post(p.ID, Message{From: from, FromKey: senderKey, FromName: senderName, Text: text}); err != nil {
			if len(targets) == 1 {
				return nil, err
			}
			continue
		}
		sent = append(sent, p)
	}

	if len(sent) == 0 {
		return nil, fmt.Errorf("every recipient's mailbox is full")
	}
	return sent, nil
}

// resolveRecipient finds one participant by id, identity or name, in that
// order. A name two rows share is resolved to the connected one, and refused
// when that still leaves more than one: guessing would deliver to the wrong
// driver, which is worse than asking for the key.
func (s *Server) resolveRecipient(to string) (Participant, error) {
	rows := s.Participants.List()
	if n, err := strconv.Atoi(to); err == nil && n > 0 && n <= MaxParticipants {
		if p, ok := s.Participants.Get(uint8(n)); ok {
			return p, nil
		}
		return Participant{}, fmt.Errorf("no participant with author id %d", n)
	}
	for _, p := range rows {
		if p.Identity == to {
			return p, nil
		}
	}
	var named, live []Participant
	for _, p := range rows {
		if p.Name == to {
			named = append(named, p)
			if p.Connected {
				live = append(live, p)
			}
		}
	}
	switch {
	case len(named) == 1:
		return named[0], nil
	case len(live) == 1:
		return live[0], nil
	case len(named) == 0:
		return Participant{}, fmt.Errorf("no participant named %q; `raj ctl who` lists them", to)
	}
	return Participant{}, fmt.Errorf("%d participants are named %q; send to a key or an id instead", len(named), to)
}

// state records or reports a connection's declared working state. It runs on
// the connection goroutine like send: the registry is on the server and no
// document state is involved.
func (c *connection) state(req Request, emit func(Response)) {
	c.mu.Lock()
	id := c.author
	c.mu.Unlock()
	switch req.State {
	case "":
		// A read: report the connection's own state without changing it.
	case StateWorking, StateBlocked, StateReview, StateIdle:
		c.srv.Participants.SetState(id, req.State, req.StateOn, req.StateNote)
	default:
		emit(Response{ID: req.ID, Err: "unknown state " + req.State + "; use working, blocked, review or idle", Final: true})
		return
	}
	p, ok := c.srv.participant(id)
	if !ok {
		emit(Response{ID: req.ID, Err: "no participant row for this connection", Final: true})
		return
	}
	emit(Response{ID: req.ID, OK: true, Final: true, Participants: []Participant{p}})
}

// sendMail answers a send off the event thread, like recv: the mailbox is on
// the server and nothing about a message touches the document. The sender is
// the connection's own author, read under the lock, so a frame claiming
// another id still sends as itself.
func (c *connection) sendMail(req Request, emit func(Response)) {
	c.mu.Lock()
	from := c.author
	c.mu.Unlock()
	sent, err := c.srv.SendFrom(from, req.To, req.Message)
	if err != nil {
		emit(Response{ID: req.ID, Err: err.Error(), Final: true})
		return
	}
	emit(Response{ID: req.ID, OK: true, Final: true, Author: from, Participants: sent})
}

// DefaultPath is the Unix socket for this process: one per raj, so two editors do
// not collide and neither inherits a dead one's path.
//
// Per-process rather than per-workspace because a workspace path has to be
// guessed at by both sides and is wrong the moment two raj instances open the
// same repository. Discovery is the other direction instead: list the directory
// and ask each socket what root it holds, which is one round trip and cannot be
// stale.
func DefaultPath() string {
	if path := os.Getenv(SocketEnv); path != "" {
		return path
	}
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "raj-"+strconv.Itoa(os.Getuid()))
	} else {
		dir = filepath.Join(dir, "raj")
	}
	return filepath.Join(dir, strconv.Itoa(os.Getpid())+".sock")
}

// Listen starts a server on a Unix path or a `tcp://host:port` address.
//
// For a Unix socket the directory is created 0700 and the socket 0600:
// authorisation is the filesystem, so anything the user can run can drive the
// editor — the same trust boundary as the user's own shell.
//
// A TCP listener has no filesystem to lean on, so it mints a token instead and
// refuses any request that does not carry it. See addr.go for why that is the
// same check rather than a new one.
func Listen(addr string, notify func()) (*Server, error) {
	return ListenAll([]string{addr}, notify)
}

// ListenAll starts a server on every address at once, sharing one request
// queue, one participant registry and one token.
//
// The point is to let a session be driven from more than one place: a local
// script on the Unix socket, where the filesystem authorises, and a driver in a
// container on the port, where a token stands in for it. Every listener
// accepts into the same queue, so a request does not care which transport
// carried it except where it must — the token check, the remote-exec refusal
// and anonymous identity minting are per-connection, keyed on the transport
// the connection arrived on.
//
// One address is the common case and behaves exactly as it did before.
func ListenAll(addrs []string, notify func()) (*Server, error) {
	if notify == nil {
		return nil, errors.New("control: Notify is required")
	}
	s := &Server{
		Notify:       notify,
		Participants: NewRegistry(),
		HookGate:     newHookGate(hooks.NewGate(hooks.Options{})),
		HookRuns:     hooks.NewRegistry(),
		HookLog:      hooks.NewLog(0),
	}
	// Every successful join is announced here; the server only queues the id
	// and wakes the editor, which persists the author table on its own thread.
	s.Participants.setJoined(s.noteJoin)
	for _, addr := range addrs {
		if addr == "" {
			s.Close()
			return nil, errors.New("control: empty listen address")
		}
		if err := s.add(addr); err != nil {
			s.Close()
			return nil, err
		}
	}
	if len(s.lns) == 0 {
		return nil, errors.New("control: no listen address")
	}
	return s, nil
}

// add opens one listener and starts its accept loop. The first listener to open
// is the primary: it is what Path reports and what a client given a single
// address is handed.
func (s *Server) add(addr string) error {
	if network, address := ParseAddr(addr); network == "tcp" {
		return s.addTCP(address)
	}
	return s.addUnix(addr)
}

func (s *Server) addUnix(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	// A leftover socket from a crashed process of the same pid would make Listen
	// fail. Removing one that is still live would steal it, so probe first.
	if _, err := os.Stat(path); err == nil {
		if c, derr := net.DialTimeout("unix", path, 200*time.Millisecond); derr == nil {
			c.Close()
			return fmt.Errorf("control: %s is already in use", path)
		}
		os.Remove(path)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return err
	}
	s.lns = append(s.lns, ln)
	s.paths = append(s.paths, path)
	s.socks = append(s.socks, path)
	if s.path == "" {
		s.path = path
	}
	go s.accept(ln, "unix")
	return nil
}

// listenTCP is the one-address form on a bare host:port; ListenAll is the
// general form.
func listenTCP(address string, notify func()) (*Server, error) {
	return ListenAll([]string{tcpScheme + address}, notify)
}

func (s *Server) addTCP(address string) error {
	// The environment wins so the token can be pinned: a container is started
	// with its environment already fixed, and a token the editor invented after
	// the fact cannot be got into one without restarting it. One token for the
	// whole server: a second TCP listener is the same secret, not a second one.
	if s.token == "" {
		s.token = os.Getenv(TokenEnv)
		if s.token == "" {
			var err error
			if s.token, err = NewToken(); err != nil {
				return err
			}
		}
	}
	ln, err := net.Listen("tcp", address)
	if err != nil {
		if errors.Is(err, syscall.EADDRINUSE) {
			return fmt.Errorf("%s is already in use; pass --control-addr tcp://127.0.0.1:<other>",
				tcpScheme+address)
		}
		return err
	}
	// Path reports the resolved address, not the requested one, so a port of 0
	// — which is what a test wants, and what avoids a collision — comes back as
	// something a client can actually dial.
	dial := TCPAddr(ln.Addr())
	s.lns = append(s.lns, ln)
	s.paths = append(s.paths, dial)
	if s.path == "" {
		s.path = dial
	}
	s.remote = true
	go s.accept(ln, "tcp")
	return nil
}

// Path is where the server is listening, in the form a client can dial: the
// primary listener, the first address it opened.
func (s *Server) Path() string { return s.path }

// Paths is every address the server is listening on, primary first. It is a
// copy, so a caller cannot reorder or truncate what the server holds.
func (s *Server) Paths() []string { return append([]string(nil), s.paths...) }

// Token is the secret a TCP client must present, and empty for a Unix socket.
func (s *Server) Token() string { return s.token }

// Remote reports whether this server has a listener reachable off this
// process's filesystem, which is what makes a request untrusted enough to
// need a token and an `exec` worth refusing. A given request may still have
// arrived on the local socket of a two-listener server; the transport a
// connection arrived on is what decides its gates.
func (s *Server) Remote() bool { return s.remote }

func (s *Server) accept(ln net.Listener, network string) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return // closed
		}
		go s.serve(conn, network)
	}
}

func (s *Server) serve(conn net.Conn, network string) {
	defer conn.Close()
	// Each connection is one writer, assigned an author id for its lifetime.
	// Attribution is therefore a property of who is connected rather than of
	// what a request claims, and two agents cannot land in one another's spans.
	// Provisional until the client identifies itself. A connection that never
	// says who it is still gets an id, so an anonymous one-off client works —
	// it just does not survive a reconnect as the same writer.
	author, err := s.reserveAuthor()
	if err != nil {
		// No id to attribute this connection to. Refusing is the only safe
		// answer: falling back to a shared id would put two writers on one
		// author, which is the collision the registry exists to prevent.
		return
	}
	// bound is whether hello has rebound this connection to a durable identity.
	// Until then author is a reserved id with no row and Release is what frees
	// it; after, Leave marks the durable row disconnected and keeps it for
	// attribution. The defer reads both when it runs.
	bound := false
	defer func() {
		if bound {
			s.Participants.Leave(author)
		} else {
			s.Participants.Release(author)
		}
	}()

	// Reading and writing are separated because a streamed search takes
	// seconds and a cancel has to arrive during it. A loop that read a frame,
	// handled it, and wrote the reply could not do that: it would be inside
	// the handler when the cancel came. So the reader only reads, handlers run
	// concurrently, and one writer goroutine owns the socket — concurrent
	// writes would interleave two frames into one unparseable stream.
	out := make(chan outFrame, 64)
	done := make(chan struct{})
	safe.Go(func() {
		defer close(done)
		for f := range out {
			if WriteFrame(conn, f.h, f.body) != nil {
				return
			}
		}
	})

	s.mu.Lock()
	every := s.heartbeat
	s.mu.Unlock()
	c := &connection{srv: s, out: out, author: author, network: network,
		running:    map[int]context.CancelFunc{},
		heartbeats: map[int]chan struct{}{},
		heartbeat:  every}
	r := bufio.NewReader(conn)
	var wg sync.WaitGroup
	for {
		f, err := ReadFrame(r)
		if err != nil {
			break // EOF, or a length that did not parse: the connection is over
		}
		req, derr := DecodeRequest(f)
		if derr != nil {
			// A frame that arrived whole but decoded badly is answered rather
			// than dropped: the client is waiting, and the id is readable even
			// when the body is not.
			c.send(Response{ID: f.Header.ID, Err: derr.Error(), Final: true})
			continue
		}
		// Every request stamps the participant's last activity, so the
		// derived working state reads this connection's own traffic and the
		// serve loop stays a cheap lock-guarded map write.
		s.Participants.Touch(author)
		if !s.authorised(req, network) {
			// One refusal, then the connection ends. The token is 32 random
			// bytes, so this is not rate limiting against a guesser — it is
			// refusing to stay in a conversation with something that cannot
			// say who it is.
			c.send(Response{ID: req.ID, Err: errUnauthorised, Final: true})
			break
		}
		if req.Op == "token" {
			// "token" is answered on the reading goroutine, like hello and
			// cancel: the secret is the server's own and no document state is
			// involved. It is reachable on both transports on purpose. On a
			// Unix socket the filesystem has already authorised the caller,
			// which is how a local script reads the token to hand to a
			// container. Over TCP the request has already passed the check
			// above, so the caller holds the secret it would be handed back
			// and refusing it would protect nothing.
			c.send(Response{ID: req.ID, OK: true, Final: true, Token: s.token})
			continue
		}
		if network == "tcp" && localOnly(req) {
			c.send(Response{ID: req.ID, Err: remoteRefusal(req), Final: true})
			continue
		}
		if req.Op == "hello" {
			// Answered on the reading goroutine: it rebinds the author id this
			// connection writes as, and touches no document state.
			identity := req.Identity
			var minted string
			if identity == "" && network == "tcp" {
				// An anonymous TCP client gets a server-minted token: durable,
				// so a reconnect can own the text it already wrote, and
				// server-chosen, so no two agents collide on a name. The
				// reply carries it for the client to adopt.
				var err error
				if minted, err = mintIdentity(); err != nil {
					c.send(Response{ID: req.ID, Err: err.Error(), Final: true})
					continue
				}
				identity = minted
			}
			if identity == "" {
				// Anonymous on a local socket keeps its provisional id: the
				// filesystem permissions already decided who may connect.
				c.send(Response{ID: req.ID, OK: true, Final: true,
					Participants: s.Roster()})
				continue
			}
			// The control token is shared with agents in a container, so a TCP
			// caller could otherwise self-declare human and bypass the proposal
			// gate. The token proves the caller reached the editor, not that it
			// is the person at the keyboard: only the local Unix socket is the
			// human's own connection. Anywhere else a requested human is
			// downgraded to an agent, which still connects and works — just
			// proposal-only.
			kind := KindAgent
			if req.Kind == string(KindHuman) && network == "unix" {
				kind = KindHuman
			}
			id, err := s.Participants.Join(identity, req.Name, kind)
			if err != nil {
				c.send(Response{ID: req.ID, Err: err.Error(), Final: true})
				continue
			}
			// Mail persisted for this identity while it was away is queued the
			// moment it rejoins, so a message sent to a gone participant is
			// delivered on its next hello rather than waiting for a restart.
			s.Mail.ReplayIdentity(identity)

			// The task is stored on the participant, so a later write on this or
			// a reconnecting connection derives it from the author rather than
			// carrying it on every frame. An empty task leaves the row as it is,
			// so the ordinary bind-first hello does not erase what register set.
			s.Participants.SetTask(id, req.Task)
			if id != author {
				if bound {
					// A second hello on one connection rebinding it to a
					// different durable identity: the old row stays for
					// attribution but is no longer attached.
					s.Participants.Leave(author)
				} else {
					// The provisional id is reserved, not a row: release it
					// now, not at disconnect, so it is recyclable at once.
					s.Participants.Release(author)
				}
			} else if bound {
				// A repeat hello naming the identity this connection already
				// holds (the CLI's bind-first hello, then recv's or register's
				// own) joined the same row twice. Registry counts connections,
				// and this one closes once, so give back the extra count now or
				// the participant reads as connected forever after it leaves.
				s.Participants.Leave(id)
			}
			author = id
			bound = true
			c.mu.Lock()
			c.author = id
			c.mu.Unlock()
			c.send(Response{ID: req.ID, OK: true, Final: true, Identity: minted,
				Participants: s.Roster()})
			continue
		}
		if req.Op == "cancel" {
			// Handled here, on the reading goroutine, without the editor: the
			// point of a cancel is that it does not queue behind the work it
			// is cancelling.
			c.cancel(req.Cancel)
			c.send(Response{ID: req.ID, OK: true, Final: true})
			continue
		}
		if req.Author == 0 {
			req.Author = author
		}
		wg.Add(1)
		safe.Go(func() {
			defer wg.Done()
			// A request that sends no frames of its own for a while would look
			// dead to the client's per-frame idle deadline; the heartbeat keeps
			// it alive. recv is exempt: the client already arms no deadline for
			// the long poll, so a heartbeat would only be noise.
			if req.Op != "recv" && req.Op != "watch" {
				c.startHeartbeat(req.ID)
				defer c.stopHeartbeat(req.ID)
			}
			c.handle(req)
		})
	}
	c.cancelAll()
	wg.Wait()
	close(out)
	<-done
}

// heartbeatEvery is how often a connection emits a contentless heartbeat frame
// while a request is in flight, and it is the production default. A connection
// carries its own copy, set from the server at creation and shortenable by a
// test, so a live ticker never reads shared state a later test can write. The
// client's streamIdle is three of these, coupling the two so an idle deadline
// can never fall inside a heartbeat interval. A long quiet operation - a build
// with a silent phase longer than the client's idle - would otherwise look like
// a dead peer to the client's per-frame idle deadline.
const heartbeatEvery = 5 * time.Second

type outFrame struct {
	h    Header
	body []byte
}

// connection is one client's state: its author id, its in-flight cancellations,
// and the single channel its frames leave by.
type connection struct {
	srv    *Server
	out    chan outFrame
	author uint8
	// network is the transport this connection arrived on: "unix" or "tcp".
	// One server can have both listeners, so the token check, the remote-exec
	// refusal and anonymous minting key on this rather than on the server.
	network string

	mu         sync.Mutex
	running    map[int]context.CancelFunc
	heartbeats map[int]chan struct{}
	// heartbeat is this connection's tick interval: a copy of the server's
	// seam, or heartbeatEvery when that is zero, captured before the ticker
	// starts so the goroutine reads no shared mutable state.
	heartbeat time.Duration
	closed    bool
}

func (c *connection) send(res Response) {
	// Stamped here rather than by each handler, so no verb can forget it and
	// none can claim a different one. SrcVersion rides every frame for the
	// same reason: a client that missed the handshake still learns what it is
	// talking to.
	c.mu.Lock()
	res.Author = c.author
	c.mu.Unlock()
	res.SrcVersion = currentSrcVersion()
	h, body := EncodeResponse(res)
	// A final frame ends the request, so stop the heartbeat before the frame can
	// reach the socket. This is best-effort, not strict ordering: a tick already
	// inside send can still land after the final, because closing done cannot
	// retract a send in flight. That is harmless -- the client returns on the
	// final and ignores a stale id. This runs without holding mu, and
	// stopHeartbeat is idempotent, so the dispatch's deferred stop is a harmless
	// no-op and this cannot deadlock.
	if res.Final {
		c.stopHeartbeat(res.ID)
	}
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return
	}
	defer func() {
		// The writer goroutine closes out when the connection ends; a handler
		// still streaming into it would panic. Dropping the frame is right:
		// there is nobody to read it.
		_ = recover()
	}()
	c.out <- outFrame{h, body}
}

// startHeartbeat begins emitting a contentless, non-final frame with id every
// heartbeatEvery (or this connection's shorter test interval), until
// stopHeartbeat(id) closes its done channel or the connection closes. The frame
// is ordinary and empty, so any client that loops on non-final frames consumes
// it without knowing it is a heartbeat; its only job is to reset the client's
// per-frame idle deadline while a long request has nothing of its own to say.
func (c *connection) startHeartbeat(id int) {
	done := make(chan struct{})
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	if c.heartbeats == nil {
		c.heartbeats = map[int]chan struct{}{}
	}
	c.heartbeats[id] = done
	every := c.heartbeat
	c.mu.Unlock()
	if every <= 0 {
		every = heartbeatEvery
	}
	safe.Go(func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				// send tolerates the writer closing out mid-flight; a frame
				// racing the connection's end is dropped, not fatal.
				c.send(Response{ID: id, Final: false})
			}
		}
	})
}

// stopHeartbeat ends the heartbeat for id, if one is running. It is idempotent:
// send calls it on the final frame and the dispatch calls it again on the way
// out, so the second call must find nothing and do nothing. Closing the channel
// wakes the ticker goroutine; the map entry is removed under mu so no second
// close can race. It cannot retract a send already in flight, so the ordering
// against a final frame is best-effort (see send).
func (c *connection) stopHeartbeat(id int) {
	c.mu.Lock()
	done := c.heartbeats[id]
	if done != nil {
		delete(c.heartbeats, id)
		close(done)
	}
	c.mu.Unlock()
}

func (c *connection) cancel(id int) {
	c.mu.Lock()
	stop := c.running[id]
	c.mu.Unlock()
	if stop != nil {
		stop()
	}
}

func (c *connection) cancelAll() {
	c.mu.Lock()
	c.closed = true
	for _, stop := range c.running {
		stop()
	}
	c.running = map[int]context.CancelFunc{}
	for _, done := range c.heartbeats {
		close(done)
	}
	c.heartbeats = map[int]chan struct{}{}
	c.mu.Unlock()
}

// handle runs one request. A streaming one emits several frames; everything
// else emits one.
func (c *connection) handle(req Request) {
	if req.Op == "prog" {
		c.program(req)
		return
	}
	c.one(req, c.send)
}

// one runs a single request and emits its responses through emit.
//
// The indirection exists for batching. A streaming verb marks its own last
// frame Final, which is right when it is the whole request and wrong when it is
// the third of five in a program — a client that saw Final would stop reading
// while four verbs were still to come. So who gets to say Final is the caller's
// decision, and the streaming handlers no longer reach for the socket directly.
func (c *connection) one(req Request, emit func(Response)) {
	// The local-only gate is re-checked here because the serve loop only sees
	// the outer request: a program arrives as one "prog" frame and its exec
	// verb would otherwise slip past the refusal that a direct exec gets.
	// Re-checking at the single chokepoint every request passes through keeps a
	// batch from being a way around the refusal.
	if c.network == "tcp" && localOnly(req) {
		emit(Response{ID: req.ID, Err: remoteRefusal(req), Final: true})
		return
	}
	// A request runs as the connection's own writer, so a frame may not name
	// another author. The author a request carries is a claimed field, not the
	// connection: a raw client can send Author: LocalHuman, and a program's
	// sub-requests are built by Requests(prog, req.Author) from the outer
	// frame's claimed id, so they never pass the serve loop's own checks. The
	// refusal lives here, at the one chokepoint every direct request and every
	// program sub-request reaches, and compares the claim with c.author, the
	// real author: set when the connection is accepted and rebound only by a
	// hello on the reading goroutine. A zero author carries no claim — it is
	// stamped from the connection — and hello and register never reach here, so
	// both pass. save, revert, approve and withdraw keep their own contract
	// wording, which their tests pin; every other verb is refused with
	// errAuthorSpoof. The verbs whose Author is inert data rather than identity
	// (send, hook, exec) are exempt: they already act as the connection.
	c.mu.Lock()
	realAuthor := c.author
	c.mu.Unlock()
	if req.Author != 0 && req.Author != realAuthor && !authorIsInert(req.Op) {
		emit(Response{ID: req.ID, Err: authorSpoofRefusal(req), Final: true})
		return
	}
	// A zero author is stamped here too, not only in the serve loop: a program
	// sub-request carries whatever its OpAuthor said, and a zero there passes
	// the check above but would reach the handlers as AuthorOriginal and write
	// text that reads as the file's own.
	if req.Author == 0 {
		req.Author = realAuthor
	}
	// Accepting and landing are the person's decisions, and the Unix socket is
	// the person's trust boundary — the same rule hook run and hook authoring
	// use. An undeclared connection there is the user's own `raj ctl accept`
	// or `raj ctl land` from a shell, so it acts as the local human; a
	// connection that registered as an agent keeps its identity and the Guard
	// refuses it like any agent. Save deliberately stays as it was (the
	// connection's own author, so an undeclared socket client is refused):
	// widening it is WAVE-PLAN L7.
	if (req.Op == "accept" || req.Op == "land") && c.network == "unix" {
		if p, ok := c.srv.Participants.Get(realAuthor); !ok || p.Kind != KindAgent {
			req.Author = LocalHuman
		}
	}
	switch req.Op {
	case "search":
		c.search(req, emit)
		return
	case "exec":
		c.exec(req, emit)
		return
	case "recv":
		c.recv(req, emit)
		return
	case "send":
		c.sendMail(req, emit)
		return
	case "state":
		c.state(req, emit)
		return
	case "watch":
		c.watch(req, emit)
		return
	case "lsp":
		c.lsp(req, emit)
		return
	case "hook":
		// hook run has the same two-phase shape as exec: the decision is made
		// on the event thread, the command runs off it. log, ps, cancel and
		// the panic switch never touch the document, so they are answered on
		// the connection goroutine; the authoring and read modes have no
		// connection handler and fall through to the event thread below. The
		// modes are hookModes, the same table dispatchHook and the transport
		// guards read.
		if m, ok := hookModeByName[req.HookMode]; ok && m.conn != nil {
			m.conn(c, req, emit)
			return
		}
	}
	res := c.srv.submit(req)
	// A hook list reports the panic switch alongside the rows, so a reader
	// learns hooks are off from the list it was already asking for.
	if req.Op == "hook" && req.HookMode == "list" {
		res.HookOff = c.srv.hooksOff()
	}
	res.Final = true
	emit(res)
}

// program compiles a batch and runs it in order.
//
// Each sub-request goes through the ordinary path, so a program cannot reach
// anything a JSON frame could not and needs no second set of handlers. They run
// sequentially rather than concurrently, which is the whole reason to batch:
// fifty splices against one version have to land in the order they were written
// or the offsets in the later ones mean nothing.
//
// One response per verb, with Final on the last, so a client can match answers
// to verbs positionally. A compile error is one response and nothing runs:
// half a batch is the outcome a caller can neither detect nor undo.
func (c *connection) program(req Request) {
	reqs, err := Requests(req.Program, req.Author)
	if err != nil {
		c.send(Response{ID: req.ID, Err: err.Error(), Final: true})
		return
	}
	for i, sub := range reqs {
		if sub.ID == 0 {
			sub.ID = req.ID
		}
		sub.Token = req.Token
		last := i == len(reqs)-1
		c.one(sub, func(res Response) {
			// Only the batch's last frame ends it. A streaming verb in the
			// middle still emits every batch it found; what it does not get to
			// do is tell the client the conversation is over.
			res.Final = res.Final && last
			c.send(res)
		})
	}
}

// recv parks until the user has something to say to this connection.
//
// It never reaches the event thread. There is nothing to ask the editor — the
// mailbox is on the server and the editor writes into it — and a request that
// parked on the event thread would park the editor with it.
//
// The recipient is the connection's own author id, read at the moment it parks.
// A connection that says `hello` afterwards has changed identity, and the fix
// for that is to say hello first, which every driver does anyway: rebinding a
// request that is already waiting would deliver one participant's mail to
// another.
func (c *connection) recv(req Request, emit func(Response)) {
	ctx, stop := context.WithCancelCause(context.Background())
	stopFn := func() { stop(nil) }
	c.mu.Lock()
	c.running[req.ID] = stopFn
	to := c.author
	c.mu.Unlock()
	// A parked recv is the sole reader for its author id: Park preempts
	// whatever recv was already parked for the same identity (see mailbox.go)
	// rather than leaving two goroutines racing one channel.
	token := c.srv.Mail.Park(to, stop)
	// A parked recv is what the derived listening state reads; the count is
	// per participant, so a short call closing does not clear it.
	c.srv.Participants.SetListening(to, true)
	defer func() {
		stopFn()
		c.srv.Mail.Unpark(to, token)
		c.srv.Participants.SetListening(to, false)
		c.mu.Lock()
		delete(c.running, req.ID)
		c.mu.Unlock()
	}()

	msgs, ok := c.srv.Mail.Wait(ctx, to)
	if !ok {
		// Cancelled, the connection went away, or a newer recv for this
		// identity preempted this one (ErrSuperseded) — distinguished so a
		// driver can tell "someone else is reading my mail now" from an
		// ordinary cancel and knows not to re-park.
		reason := "cancelled"
		if errors.Is(context.Cause(ctx), ErrSuperseded) {
			reason = ErrSuperseded.Error()
		}
		emit(Response{ID: req.ID, Err: reason, Final: true})
		return
	}
	emit(Response{ID: req.ID, OK: true, Final: true, Messages: msgs})
}

// watch parks until the open buffers' generation differs from the one the
// request carried. Like recv it never waits on the event thread — a parked
// watch that did would park the editor with it — but unlike recv it asks the
// event thread for the buffer list once the generation has moved, so the wake
// carries a consistent view rather than a bare "something changed". The
// register-then-compare loop closes the race where the generation moves between
// the compare and the park: the bump would find no watcher and the watch would
// sleep through it.
func (c *connection) watch(req Request, emit func(Response)) {
	ctx, stop := context.WithCancel(context.Background())
	c.mu.Lock()
	c.running[req.ID] = stop
	c.mu.Unlock()
	defer func() {
		stop()
		c.mu.Lock()
		delete(c.running, req.ID)
		c.mu.Unlock()
	}()

	for {
		// Register before the compare, then compare again above the wait: a
		// bump that landed between the two would otherwise find no watcher to
		// wake and be slept through. In this order the bump either fills the
		// buffered channel or the compare above the wait sees the new
		// generation, so neither window can lose it.
		_, ch, cancel := c.srv.watchRegister()
		if c.srv.Gen() != req.Gen {
			cancel()
			list := c.srv.submit(Request{ID: req.ID, Op: "buffers"})
			if list.Err != "" {
				emit(Response{ID: req.ID, Err: list.Err, Final: true})
				return
			}
			emit(Response{ID: req.ID, OK: true, Gen: c.srv.Gen(),
				Buffers: list.Buffers, Reveals: c.srv.revealsSince(req.Gen), Final: true})
			return
		}
		select {
		case <-ch:
			cancel()
		case <-ctx.Done():
			cancel()
			// Answered rather than dropped, like recv: the client that
			// cancelled is waiting for the frame that finishes its id.
			emit(Response{ID: req.ID, Err: "cancelled", Final: true})
			return
		}
	}
}

// exec runs a command, in the same two phases as search: the decision is made
// on the event thread, because it reads the buffers, and the command then runs
// here so a slow one neither blocks the editor nor becomes uncancellable.
func (c *connection) exec(req Request, emit func(Response)) {
	check := c.srv.submit(Request{ID: req.ID, Op: "execcheck", Argv: req.Argv, Dir: req.Dir, ExecProjected: req.ExecProjected})
	if check.Err != "" {
		check.ID, check.Final = req.ID, true
		emit(check)
		return
	}

	ctx, stop := context.WithCancel(context.Background())
	c.mu.Lock()
	c.running[req.ID] = stop
	c.mu.Unlock()
	defer func() {
		stop()
		c.mu.Lock()
		delete(c.running, req.ID)
		c.mu.Unlock()
	}()

	runDir := req.Dir
	if req.ExecProjected {
		// The event thread handed us the projection and the root; build the
		// scratch tree here, off it, and run with that as the command's cwd.
		// A materialise failure emits the final error and runs nothing.
		m, prov, err := materialiseWith(ctx, git.New(check.Root), check.Projection, git.MaterialiseOptions{})
		if err != nil {
			emit(Response{ID: req.ID, Err: err.Error(), Final: true})
			return
		}
		defer m.Remove()
		emit(Response{ID: req.ID, OK: true, Stream: StreamStderr,
			Out: fmt.Sprintf("raj: exec --projected ran against the projected tree at HEAD %s (dirty %s); accepted and proposed text included\n",
				prov.Head, prov.DirtyDigest)})
		runDir = m.Dir
	}

	code, err := Run(ctx, req.Argv, runDir, func(stream uint8, b []byte) {
		emit(Response{ID: req.ID, OK: true, Stream: stream, Out: string(b)})
	})
	// Dirty is carried to the result, not used to refuse: the caller needs it
	// to judge whether a failure is about the code it is looking at.
	final := Response{ID: req.ID, OK: err == nil, Exit: code, Dirty: check.Dirty, Final: true}
	if err != nil {
		final.Err = err.Error()
	}
	if ctx.Err() != nil {
		final.Err = "cancelled"
		final.OK = false
	}
	emit(final)
}

// runHook runs an admitted hook, in the same two phases as exec: admission is
// decided on the event thread, because it reads the hook set and the live
// projection, and the command then runs here so a slow hook neither blocks the
// editor nor becomes uncancellable.
//
// Admission already marked the hook in flight on the shared Gate; the deferred
// End releases it however this attempt finishes, including a failure to prepare
// the run, so a failed attempt cannot wedge the hook off for the process's
// life. Reruns are not queued in v0 -- an in-flight or cooling hook is refused
// with a retry, and the caller decides whether to ask again. Revision dedupe
// and cached results are deferred too: every admitted run prepares its tree,
// then executes.
// hookOutputCap bounds the bytes one run streams back. There is no per-hook
// override: the cap protects the unattended agent and the log, not the hook
// author, so one number is enough. The command still runs to completion after
// the cap is reached -- only the output is cut -- so the exit status is the
// hook's own either way.
const hookOutputCap = 1 << 20

func (c *connection) runHook(req Request, emit func(Response)) {
	// The panic switch is checked before admission: while it is on, no run
	// starts, and the refusal names the state rather than a hook policy.
	if c.srv.hooksOff() {
		emit(Response{ID: req.ID, Err: errHooksOff, Final: true})
		return
	}
	// On the Unix socket the caller is the local human -- the same trust
	// boundary authoring uses -- so `raj hook run` may run a hook that is not
	// agent-callable, and the run is attributed to the person. The exception
	// is a connection that registered as an agent (`--as KEY` on a host-side
	// driver): it declared itself, so it keeps agent admission and its own
	// attribution in the log. Over TCP the caller is whatever agent it
	// registered as, and admission applies the hook's Agent flag.
	caller := c.author
	if c.network == "unix" {
		if p, ok := c.srv.Participants.Get(c.author); !ok || p.Kind != KindAgent {
			caller = LocalHuman
		}
	}
	prep := c.srv.submit(Request{ID: req.ID, Op: "hookprep", HookName: req.HookName, HookParams: req.HookParams, Author: caller})
	if prep.Err != "" {
		prep.ID, prep.Final = req.ID, true
		emit(prep)
		return
	}
	// The event thread admitted the hook and marked it in flight. This one
	// defer ends the gate before it forgets the run, whatever happens from here
	// -- a failure to prepare the run included -- so a caller that sees the
	// registry empty also sees the hook free to run again.
	var runID uint64
	defer func() {
		if prep.HookDetach {
			// A detached run owns its own lifetime: the completion goroutine
			// ends the gate and removes the registry entry, however long it
			// takes. The request's return must not free the hook.
			return
		}
		c.srv.HookGate.End(req.HookName)
		if runID != 0 {
			c.srv.HookRuns.Remove(runID)
		}
	}()

	ctx, stop := context.WithCancel(context.Background())
	c.mu.Lock()
	c.running[req.ID] = stop
	c.mu.Unlock()
	defer func() {
		stop()
		c.mu.Lock()
		delete(c.running, req.ID)
		c.mu.Unlock()
	}()

	// A workspace hook runs against the saved tree: no scratch tree is built,
	// so it sees exactly what the user saved, which the readiness gate above has
	// already guaranteed has nothing unsaved to miss. A projected hook keeps
	// v0's behaviour and runs in a scratch tree built here, off the event
	// thread. A builtin leaf instead runs in-process with no tree, so neither
	// the scratch materialisation nor the workspace git view and status is
	// built for it: there is nothing to hand it and no working directory to
	// stamp.
	runDir := prep.Root
	var prov Provenance
	// cleanup removes a projected run's scratch tree; a detached run hands it
	// to its completion goroutine, a synchronous one defers it here.
	var cleanup func()
	// A builtin leaf runs in-process with no tree. A composite needs one only
	// when a step is a shell action; a chain of leaves resolves the root from
	// the run context and needs no working directory either.
	noTree := prep.HookBuiltin != "" || (len(prep.HookSteps) > 0 && !stepsNeedTree(prep.HookSteps))
	switch {
	case noTree:
		// No tree: an in-process action receives its args and the run context,
		// nothing else.
	case prep.HookTree == string(hooks.TreeWorkspace):
		svc := git.New(prep.Root)
		view, verr := svc.View(ctx)
		if verr != nil {
			emit(Response{ID: req.ID, Err: verr.Error(), Final: true})
			return
		}
		dirty, derr := svc.StatusDigest(ctx)
		if derr != nil {
			emit(Response{ID: req.ID, Err: derr.Error(), Final: true})
			return
		}
		prov = Provenance{Head: view.Head, DirtyDigest: dirty}
	default:
		m, p, merr := materialiseWith(ctx, git.New(prep.Root), prep.Projection, git.MaterialiseOptions{})
		if merr != nil {
			emit(Response{ID: req.ID, Err: merr.Error(), Final: true})
			return
		}
		if prep.HookDetach {
			// The scratch tree must outlive the request for a detached run, so
			// the completion goroutine removes it; a synchronous run removes it
			// on return.
			cleanup = func() { _ = m.Remove() }
		} else {
			defer m.Remove()
		}
		prov, runDir = p, m.Dir
	}

	if prep.HookDetach {
		c.runHookDetached(req, emit, prep, prov, runDir, caller, cleanup)
		return
	}

	started := time.Now()
	// The registry records the run so `hook ps` can show it and `hook cancel`
	// can stop it. Cancel is the connection context cancel: it reaches the
	// command through runCtx and kills the process group. RunReport fills the
	// pid and pgid once the command has started.
	runID = c.srv.HookRuns.Add(hooks.Run{Hook: req.HookName, Author: caller,
		Started: started, Cancel: stop})

	argv := prep.HookArgv
	if prep.HookShell != "" {
		argv = []string{"/bin/sh", "-c", prep.HookShell}
	}
	timeout := time.Duration(prep.HookTimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = hooks.DefaultTimeout
	}
	// The timeout is a context deadline so Run's process-group kill fires on
	// it, not just a timer around the call.
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// The resolved parameters ride the run context as the builtin in-process
	// channel, so a leaf reads RAJ_PARAM_<name> the way it reads the chain
	// environment; a shell or argv action gets the same entries as its process
	// environment below.
	runCtx = builtin.WithParamEnv(runCtx, hooks.ParamEnv(prep.HookParamValues))

	if prep.HookBuiltin != "" {
		emit(Response{ID: req.ID, OK: true, Stream: StreamStderr,
			Out: fmt.Sprintf("raj: hook %s ran builtin %s\n", req.HookName, prep.HookBuiltin)})
	} else if len(prep.HookSteps) > 0 {
		emit(Response{ID: req.ID, OK: true, Stream: StreamStderr,
			Out: fmt.Sprintf("raj: hook %s ran a composite of %d step(s)\n", req.HookName, len(prep.HookSteps))})
	} else if prep.HookTree == string(hooks.TreeWorkspace) {
		emit(Response{ID: req.ID, OK: true, Stream: StreamStderr,
			Out: fmt.Sprintf("raj: hook %s ran against the saved workspace root at HEAD %s (dirty %s)\n",
				req.HookName, prov.Head, prov.DirtyDigest)})
	} else {
		emit(Response{ID: req.ID, OK: true, Stream: StreamStderr,
			Out: fmt.Sprintf("raj: hook %s ran against the projected tree at HEAD %s (dirty %s); accepted and proposed text included, with the build harness pinned to the accepted tree\n",
				req.HookName, prov.Head, prov.DirtyDigest)})
	}

	var outMu sync.Mutex
	var outBytes int
	truncated := false
	// The cap is per run and shared by both streams. The pumps run
	// concurrently, so the running total is behind a mutex. Once the cap is
	// reached the callback drops chunks but the process keeps running, which is
	// what lets the stamp still report its exit status. The cut flag is read
	// back under the same mutex after RunReport returns, so a still-draining
	// cancelled run cannot race the read.
	stream := func(stream uint8, b []byte) {
		outMu.Lock()
		remaining := hookOutputCap - outBytes
		if remaining <= 0 {
			truncated = true
			outMu.Unlock()
			return
		}
		if len(b) > remaining {
			b = b[:remaining]
			truncated = true
		}
		outBytes += len(b)
		outMu.Unlock()
		emit(Response{ID: req.ID, OK: true, Stream: stream, Out: string(b)})
	}
	onStart := func(pid, pgid int) {
		c.srv.HookRuns.SetProcess(runID, pid, pgid)
	}
	// A builtin action never reaches the exec path: the leaf runs in-process
	// here, under the same Admit and Gate the external path already passed, and
	// its output streams through the same relay, so a downstream
	// RAJ_STEP_<name>_OUT sees it exactly as it would see a command's stdout.
	var code int
	var rerr error
	switch {
	case prep.HookBuiltin != "":
		// The leaf gets the workspace root the run was admitted with, so it
		// resolves prep.Root rather than the process working directory.
		code, rerr = runBuiltinLeaf(builtin.WithRoot(runCtx, prep.Root), prep.HookBuiltin, prep.HookBuiltinArgs, stream)
	case len(prep.HookSteps) > 0:
		// A composite runs its steps in order with the admitted root and tree,
		// and stops at the first nonzero step.
		code, rerr = runChain(runCtx, prep.HookSteps, prep.Root, runDir, stream)
	default:
		// runReportEnv appends the resolved parameters to the process
		// environment; an empty slice inherits the editor's environment exactly
		// as before, so a hook that declares none is unchanged.
		code, rerr = runReportEnv(runCtx, argv, runDir, hooks.ParamEnv(prep.HookParamValues), onStart, stream)
	}

	outMu.Lock()
	truncatedFinal := truncated
	outMu.Unlock()

	// A may_write=0 workspace hook must leave the tree alone: git status is the
	// cheap definition of "the tree changed", so a run that moves it fails with
	// a named error even when the command exited zero. Projected hooks keep
	// v0's behaviour and are not checked, and a builtin leaf has no tree to move.
	writeErr := ""
	if !noTree && prep.HookTree == string(hooks.TreeWorkspace) && !prep.HookMayWrite {
		after, derr := git.New(prep.Root).StatusDigest(ctx)
		switch {
		case derr != nil:
			writeErr = fmt.Sprintf("hook %q may not write, but its workspace status could not be read: %v", req.HookName, derr)
		case after != prov.DirtyDigest:
			writeErr = fmt.Sprintf("hook %q modified the workspace", req.HookName)
		}
	}

	final := Response{ID: req.ID, OK: rerr == nil, Exit: code, Final: true,
		HookRunID: runID, HookName: req.HookName, HookRevision: prep.HookRevision,
		HookHead: prov.Head, HookDirty: prov.DirtyDigest, HookTruncated: truncatedFinal,
		HookDurationMS: time.Since(started).Milliseconds()}
	if rerr != nil {
		final.Err = rerr.Error()
	}
	if writeErr != "" {
		final.Err = writeErr
		final.OK = false
	}
	if runCtx.Err() != nil {
		// A timeout or a cancel is a refusal, never a zero exit: the command
		// did not finish, so the status Run cancel path returned must not be
		// read as success.
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			final.Err = fmt.Sprintf("hook %q timed out after %s", req.HookName, timeout)
		} else {
			final.Err = "cancelled"
		}
		final.OK = false
	}
	if c.srv.HookLog != nil {
		c.srv.HookLog.Add(hooks.Result{
			ID: runID, Hook: req.HookName, Author: caller,
			Revision: prep.HookRevision, Head: prov.Head, Dirty: prov.DirtyDigest,
			Exit: code, DurationMS: time.Since(started).Milliseconds(),
			Truncated: truncatedFinal, Err: final.Err,
			Params: prep.HookParamValues,
		})
	}
	emit(final)
}

// hookLog answers `raj hook log`: the in-memory run log, oldest first, as
// JSON. It is a read, so it crosses either transport.
func (c *connection) hookLog(req Request, emit func(Response)) {
	// A run id turns the read into `hook log --show`: the tail of that one run's
	// file rather than the in-memory listing. The text rides in HookLogJSON,
	// the field a log answer already uses.
	if req.HookRunID != 0 {
		text, err := tailHookRunLog(c.srv.HookDir, req.HookRunID)
		if err != nil {
			emit(Response{ID: req.ID, Err: err.Error(), Final: true})
			return
		}
		emit(Response{ID: req.ID, OK: true, HookLogJSON: text, Final: true})
		return
	}
	var entries []hooks.Result
	if c.srv.HookLog != nil {
		entries = c.srv.HookLog.List()
	}
	data, err := json.Marshal(entries)
	if err != nil {
		emit(Response{ID: req.ID, Err: err.Error(), Final: true})
		return
	}
	emit(Response{ID: req.ID, OK: true, HookLogJSON: string(data),
		HookOff: c.srv.hooksOff(), Final: true})
}

// hookPS answers `raj hook ps`: the runs in flight right now, as JSON. It is a
// read like the log. The panic switch rides along, so a ps also says whether
// runs are currently refused.
func (c *connection) hookPS(req Request, emit func(Response)) {
	var runs []hooks.Run
	if c.srv.HookRuns != nil {
		runs = c.srv.HookRuns.List()
	}
	data, err := json.Marshal(runs)
	if err != nil {
		emit(Response{ID: req.ID, Err: err.Error(), Final: true})
		return
	}
	emit(Response{ID: req.ID, OK: true, HookPSJSON: string(data),
		HookOff: c.srv.hooksOff(), Final: true})
}

// hookCancel stops one in-flight run by id. It is local-only -- the process
// lives on the editor host -- and it never touches the document, so it is
// answered on the connection goroutine.
func (c *connection) hookCancel(req Request, emit func(Response)) {
	if req.HookRunID == 0 {
		emit(Response{ID: req.ID, Err: "hook cancel needs a run id", Final: true})
		return
	}
	var run hooks.Run
	var ok bool
	if c.srv.HookRuns != nil {
		run, ok = c.srv.HookRuns.Get(req.HookRunID)
	}
	if !ok {
		emit(Response{ID: req.ID, Err: fmt.Sprintf("no in-flight hook run %d", req.HookRunID), Final: true})
		return
	}
	if run.Cancel != nil {
		run.Cancel()
	}
	emit(Response{ID: req.ID, OK: true, HookRunID: run.ID, HookName: run.Hook, PID: run.PID, Final: true})
}

// hookSwitch flips the workspace global panic switch. It is local-only and in
// memory: it lives only as long as the editor process.
func (c *connection) hookSwitch(req Request, emit func(Response)) {
	off := req.HookMode == "off"
	c.srv.setHooksOff(off)
	emit(Response{ID: req.ID, OK: true, HookOff: off, Final: true})
}

// lsp asks the language server. Two phases, like search: the event thread syncs
// the document and locates the server, handing back a blocking caller, and the
// request then runs here, off it. Diagnostics never blocks the server — the
// caller already carries the cached state, so its Run returns without asking.
func (c *connection) lsp(req Request, emit func(Response)) {
	prep := c.srv.submit(Request{ID: req.ID, Op: "lspprep", Path: req.Path,
		Line: req.Line, Col: req.Col, LSPMode: req.LSPMode,
		LineStart: req.LineStart, LineEnd: req.LineEnd, Query: req.Query})
	if prep.Err != "" {
		emit(Response{ID: req.ID, Err: prep.Err, Final: true})
		return
	}
	if prep.LSP == nil {
		emit(Response{ID: req.ID, Err: "no language server for this file type", Final: true})
		return
	}

	ctx, stop := context.WithCancel(context.Background())
	c.mu.Lock()
	c.running[req.ID] = stop
	c.mu.Unlock()
	defer func() {
		stop()
		c.mu.Lock()
		delete(c.running, req.ID)
		c.mu.Unlock()
	}()

	data, err := prep.LSP.Run(ctx)
	if err != nil {
		emit(Response{ID: req.ID, Err: err.Error(), Final: true})
		return
	}
	emit(Response{ID: req.ID, OK: true, LSPJSON: string(data), Final: true})
}

// search is the streaming path, and the only op that leaves the event thread.
//
// Two phases. The snapshot is taken on the event thread, because reading the
// open buffers is reading the model. The walk then runs here, off it — which is
// why a multi-second search does not freeze the editor, and why a cancel can be
// serviced while it runs.
func (c *connection) search(req Request, emit func(Response)) {
	if req.Query == nil {
		emit(Response{ID: req.ID, Err: "search needs a query", Final: true})
		return
	}
	snap := c.srv.submit(Request{ID: req.ID, Op: "searchsnapshot"})
	if snap.Err != "" || snap.Searcher == nil {
		if snap.Err == "" {
			snap.Err = "search is not available"
		}
		emit(Response{ID: req.ID, Err: snap.Err, Final: true})
		return
	}

	ctx, stop := context.WithCancel(context.Background())
	c.mu.Lock()
	c.running[req.ID] = stop
	c.mu.Unlock()
	defer func() {
		stop()
		c.mu.Lock()
		delete(c.running, req.ID)
		c.mu.Unlock()
	}()

	files, considered, capped, truncated, err := snap.Searcher.Search(ctx, *req.Query, func(batch []SearchMatch) {
		emit(Response{ID: req.ID, OK: true, Matches: batch})
	})
	final := Response{ID: req.ID, OK: err == nil, Files: files, Considered: considered,
		Capped: capped, Truncated: truncated, Final: true}
	if err != nil {
		final.Err = err.Error()
	}
	if ctx.Err() != nil {
		final.Err = "cancelled"
		final.OK = false
	}
	emit(final)
}

// The refusals a remote client can hit that a local one cannot. They are
// strings a person will read at a terminal, and each says what to do.
const (
	errUnauthorised = "unauthorized: set " + TokenEnv + " to the token raj printed when it started"
	errRemoteExec   = "exec is refused over TCP: the command would run on the editor's machine, " +
		"outside your sandbox — run it with your own shell instead"
	errRemoteHook = "hook authoring is refused over TCP: hooks are host state, and only the " +
		"local human may create, change or remove one — run `raj hook` on the editor's machine"

	// errRemoteHookControl is the refusal for the hook verbs that need the local
	// host: cancel stops a process and off/on flips the panic switch. They are
	// local-only for the same reason authoring is.
	errRemoteHookControl = "hook cancel and the off/on panic switch are refused over TCP: " +
		"the run and its process live on the editor host — run `raj hook` there"

	// errHooksOff is the global panic switch refusal. It names the state and the
	// local verb that lifts it, because the switch is deliberately local-only.
	errHooksOff = "hooks are off: the workspace panic switch is engaged; " +
		"run `raj hook on` on the editor machine to re-enable"

	// errSaveNotHuman is the save contract's single wording. Guard.Save refuses
	// an agent by kind; connection.one refuses a request that claims another
	// writer's id, the human's included. One string, so a client cannot tell
	// which layer said no.
	errSaveNotHuman = "save is the user's action: an agent's edit is written " +
		"when the user saves it; it is in the buffer and will reach disk then"

	// errApproveDeletionNotHuman is the removal gate's wording for a file. It
	// is the same contract as errSaveNotHuman: carrying out a removal is the
	// user's action. connection.one uses it for a forged author too, so a
	// client cannot tell which layer said no.
	errApproveDeletionNotHuman = "removing a file is the user's action: an agent's deletion proposal is carried out when the user approves it; withdraw it to leave the file alone"

	// errApproveDirNotHuman is the dir-removal variant, naming the directory.
	errApproveDirNotHuman = "removing a directory is the user's action: an agent's dir-removal proposal is carried out when the user approves it; withdraw it to leave the directory alone"

	// errApproveWithdraw is the conflict refusal for a request that both
	// approves and withdraws: they name opposite answers to one removal.
	errApproveWithdraw = "approve and withdraw are opposite answers to a removal; pass one"

	// errWithdrawSpoof is the withdraw contract's single wording. A withdraw
	// runs as the connection's own writer, so a frame may not retract a
	// removal as another author; Guard.Delete/Rmdir own the human-or-proposer
	// rule, and connection.one refuses the imposture before either is asked.
	errWithdrawSpoof = "a withdraw runs as the connection's own writer: a frame may not retract a removal as another author"

	// errAuthorSpoof is the refusal for a request that names another writer.
	// A request runs as the connection's own writer, so the author a frame
	// carries is a claim, not a credential; it is refused before any handler
	// sees it. The verbs whose Author is inert data (send, hook, exec) are
	// exempt because they act as the connection regardless of the field.
	errAuthorSpoof = "a request runs as the connection's own writer; a frame may not name another author"

	// errRevertForeign is the revert contract's wording. It lived in the serve
	// loop until connection.one held every author check; a revert discards the
	// writer's own pieces, so naming a different author is a refusal rather
	// than a fallback, and a batch cannot carry one.
	errRevertForeign = "revert discards only your own pieces; " +
		"another writer's text is dropped with reject then clear"

	// errAcceptNotHuman is the refusal for an accept that is not the person's.
	// Accepting is the same decision as saving or approving a removal, so an
	// agent's accept is refused before the group is touched.
	errAcceptNotHuman = "accepting a change set is the user's decision: " +
		"an agent's proposal is landed when the user accepts it, not when the writer asks"

	// errLandNotHuman is the refusal for a land that is not the person's.
	// Landing is one gesture that accepts and saves the change sets of a task,
	// the same decision save makes, so an agent's land is refused before any
	// set is accepted or any buffer saved.
	errLandNotHuman = "land is the user's one-gesture approval: it accepts and saves " +
		"a task's change sets; an agent's sets are landed when the user runs it, not when the writer asks"
)

// approveRefusal names the removal gate's wording for the op, so a forged
// approve is refused in the same words the Guard uses for a non-human.
func approveRefusal(op string) string {
	if op == "rmdir" {
		return errApproveDirNotHuman
	}
	return errApproveDeletionNotHuman
}

// authorSpoofRefusal names the wording for a request that names another writer.
// Save, revert, approve and withdraw keep the contract wording their tests pin,
// so a client cannot tell the chokepoint's refusal from the Guard's own kind
// refusal; every other verb is refused with errAuthorSpoof.
func authorSpoofRefusal(req Request) string {
	switch {
	case req.Op == "save":
		return errSaveNotHuman
	case req.Op == "revert":
		return errRevertForeign
	case req.Approve && (req.Op == "delete" || req.Op == "rmdir"):
		return approveRefusal(req.Op)
	case req.Withdraw && (req.Op == "delete" || req.Op == "rmdir"):
		return errWithdrawSpoof
	}
	return errAuthorSpoof
}

// authorIsInert reports whether a verb reads Author as data rather than as
// identity. send takes the sender, hook the caller and exec the run owner from
// the connection and ignores the field, so a frame that names another writer
// there cannot make the verb act as it; the claim is inert, and they stay
// admitted rather than refused. Every other verb acts on the author it is
// given, so it must be the connection's own.
func authorIsInert(op string) bool {
	switch op {
	case "send", "hook", "exec":
		return true
	}
	return false
}

// localOnly reports whether a request may only be served on a local transport.
// exec runs a command on the editor's machine, and hook authoring writes host
// policy; both are refused on a TCP connection, while hook list, show, run, log
// and ps cross. cancel stops a process and off/on flips the panic switch, so
// those are local too, for the same reason authoring is. run is not refused
// here on purpose: running is what crosses the line, and its boundary is
// admission (an agent may only run a hook with its Agent flag set), not the
// transport. This is the one predicate both chokepoints use, so the rule cannot
// drift between the serve loop and connection.one.
func localOnly(req Request) bool {
	if req.Op == "exec" {
		return true
	}
	if req.Op != "hook" {
		return false
	}
	if m, ok := hookModeByName[req.HookMode]; ok && m.local {
		return true
	}
	return false
}

// remoteRefusal is the message for a local-only request refused on TCP. The
// predicate localOnly decides which requests; this only says why.
func remoteRefusal(req Request) string {
	if req.Op == "hook" {
		if m, ok := hookModeByName[req.HookMode]; ok && m.remoteCtl {
			return errRemoteHookControl
		}
		return errRemoteHook
	}
	return errRemoteExec
}

// hooksOff reports the hook panic switch. While true, every hook run is
// refused before admission.
func (s *Server) hooksOff() bool {
	s.hookMu.Lock()
	defer s.hookMu.Unlock()
	return s.hookOff
}

// setHooksOff flips the hook panic switch.
func (s *Server) setHooksOff(off bool) {
	s.hookMu.Lock()
	defer s.hookMu.Unlock()
	s.hookOff = off
}

// authorised checks a request's token against the server's. A Unix connection
// is already authorised by the filesystem and needs no token; only a TCP one
// presents a token, and that is per-connection because one server can hold
// both listeners. Constant time, so the comparison does not leak the token a
// byte at a time; free, since it runs once per request against 64 characters.
func (s *Server) authorised(req Request, network string) bool {
	if network != "tcp" {
		return true
	}
	if s.token == "" {
		return true
	}
	return subtle.ConstantTimeCompare([]byte(req.Token), []byte(s.token)) == 1
}

// reserveAuthor reserves a provisional author id for a connection that has not
// declared an identity yet. Unlike nextAuthor, which minted a durable anon-N row
// per connection and leaked one for every `raj ctl` invocation, a reserved id
// has no row: it is freed the moment the connection binds an identity or ends.
// The error is returned rather than swallowed — with no id available there is
// no safe way to attribute the connection, so the caller must refuse it rather
// than put a second writer on a fallback id.
func (s *Server) reserveAuthor() (uint8, error) {
	return s.Participants.Reserve()
}

// srcVersion is the version this binary reports. It is stamped at build time
// with
//
//	go build -ldflags "-X raj/internal/control.srcVersion=v1.2.3" ./cmd/raj
//
// and falls back to the vcs.revision the go tool embeds when it builds inside
// a checkout. A tree with no .git has no revision to fall back to, and that is
// the tree the gate builds on: there the stamp is the only version the binary
// can name. Empty means unknown, and an unknown on either side keeps the
// handshake check silent.
//
// The variable has no initializer on purpose: -X cannot reach a variable whose
// initializer is a function call, and an initializer would overwrite the stamp
// besides. versionOrRevision only fills an empty one, so a stamp survives.
var srcVersion string

func init() {
	srcVersion = versionOrRevision(srcVersion, buildSettings())
}

// buildSettings is the build settings the go tool embedded, or nil when it
// embedded none — a build from a tree with no .git has no vcs.* setting.
func buildSettings() []debug.BuildSetting {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return nil
	}
	return info.Settings
}

// versionOrRevision returns stamped when it is non-empty — the value an -X
// stamp put in srcVersion — and otherwise the vcs.revision among settings. A
// test calls it directly, because a link-time value cannot be set from a test
// process.
func versionOrRevision(stamped string, settings []debug.BuildSetting) string {
	if stamped != "" {
		return stamped
	}
	for _, s := range settings {
		if s.Key == "vcs.revision" {
			return s.Value
		}
	}
	return ""
}

// srcVersion is read on every response (connection.send) and by the CLI's skew
// warning, and a test swaps it to pin the stamp; guard it so a test that pins
// it cannot race a live server goroutine (seen under -race, 2026-09-27).
var srcVersionMu sync.RWMutex

func currentSrcVersion() string {
	srcVersionMu.RLock()
	defer srcVersionMu.RUnlock()
	return srcVersion
}

func setSrcVersion(v string) {
	srcVersionMu.Lock()
	srcVersion = v
	srcVersionMu.Unlock()
}

// mintIdentity returns a fresh identity token for an anonymous TCP client.
// Server-minted rather than client-chosen: no collisions, and no two agents
// both arriving as claude-1.
func mintIdentity() (string, error) {
	tok, err := NewToken()
	if err != nil {
		return "", err
	}
	return "tok_" + tok, nil
}

// Gen is the current generation counter, the value a watch compares against.
func (s *Server) Gen() uint64 {
	s.watchMu.Lock()
	defer s.watchMu.Unlock()
	return s.gen
}

// BumpGen records a new generation and wakes every parked watch. It runs on
// the event thread, so the fan-out only takes the watch lock and never blocks:
// a full or empty channel just means that watcher is already awake and will
// read the new generation when it next runs.
func (s *Server) BumpGen(gen uint64) {
	s.watchMu.Lock()
	if gen == s.gen {
		s.watchMu.Unlock()
		return
	}
	s.gen = gen
	for _, ch := range s.watchers {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	s.watchMu.Unlock()
}

// revealHistory bounds the reveal broadcast kept for watchers that have not run
// yet. A watcher asks only for reveals newer than the generation it last saw,
// so an old entry is never delivered to an up-to-date client; the cap keeps a
// long-lived server from accumulating them.
const revealHistory = 32

// revealAt is one published reveal with the generation it was posted at, so a
// watcher receives exactly the reveals that happened after the generation its
// request named.
type revealAt struct {
	gen uint64
	rev Reveal
}

// PublishReveal records a reveal for every parked watch and wakes them,
// advancing the generation. It returns the new generation so the caller can
// keep its response counter in step. A reveal is a reason to wake a watcher
// like any other, and tagging it with the generation is what lets a watcher
// take only what it has not already seen.
func (s *Server) PublishReveal(rev Reveal) uint64 {
	s.watchMu.Lock()
	s.gen++
	gen := s.gen
	s.reveals = append(s.reveals, revealAt{gen: gen, rev: rev})
	if len(s.reveals) > revealHistory {
		s.reveals = s.reveals[len(s.reveals)-revealHistory:]
	}
	for _, ch := range s.watchers {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	s.watchMu.Unlock()
	return gen
}

// revealsSince returns the reveals published after the generation a watch named,
// without consuming them, so every watcher that has not yet seen them receives
// them exactly once.
func (s *Server) revealsSince(gen uint64) []Reveal {
	s.watchMu.Lock()
	defer s.watchMu.Unlock()
	var out []Reveal
	for _, r := range s.reveals {
		if r.gen > gen {
			out = append(out, r.rev)
		}
	}
	return out
}

// watchRegister parks one watcher and returns a cancel that removes it. The
// channel is buffered so a bump never waits on the watcher to catch up.
func (s *Server) watchRegister() (uint64, chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	s.watchMu.Lock()
	if s.watchers == nil {
		s.watchers = map[uint64]chan struct{}{}
	}
	s.watchSeq++
	id := s.watchSeq
	s.watchers[id] = ch
	s.watchMu.Unlock()
	cancel := func() {
		s.watchMu.Lock()
		delete(s.watchers, id)
		s.watchMu.Unlock()
	}
	return id, ch, cancel
}

// submit parks a request and waits for the event thread to answer it.
func (s *Server) submit(req Request) Response {
	p := &Pending{Req: req, done: make(chan Response, 1)}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return Response{ID: req.ID, Err: "editor is shutting down"}
	}
	s.queue = append(s.queue, p)
	s.mu.Unlock()

	s.Notify()
	select {
	case r := <-p.done:
		return r
	case <-time.After(ReplyTimeout):
		// Mark it answered so the event thread's later Reply does not block on
		// a channel nobody is reading.
		p.once.Do(func() {})
		return Response{ID: req.ID, Err: "timed out waiting for the editor"}
	}
}

// Take removes and returns every parked request. Called from the event thread.
func (s *Server) Take() []*Pending {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.queue) == 0 {
		return nil
	}
	out := s.queue
	s.queue = nil
	return out
}

// Close stops listening, removes the socket, and fails anything still parked so
// no client is left waiting on an editor that has gone.
func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	parked := s.queue
	s.queue = nil
	s.mu.Unlock()

	var err error
	for _, ln := range s.lns {
		if cerr := ln.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}
	for _, path := range s.socks {
		os.Remove(path)
	}
	for _, p := range parked {
		p.Fail("editor is shutting down")
	}
	return err
}
