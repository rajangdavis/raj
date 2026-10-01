package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"raj/internal/git"
	"raj/internal/hooks"
	"raj/internal/intent"
)

// BufferHost is the vocabulary, with no transport in it.
//
// docs/archive/HARNESS-BROKER-AGENT.md puts the transport last and the
// interface second, for a reason worth restating: a socket is one adapter over
// this, an in-process call is another, and adding either must change nothing
// above this line. The tests use the in-process path, so what they exercise is
// the same code the socket reaches.
//
// The document's three parties are compressed to two here. The broker was a
// separate process that validated tool calls and forwarded them; it holds no
// state beyond in-flight requests, and every operation it performs needs the
// document it does not own. Splitting them would mean a second process, a
// second failure mode and a second hop for every read, to enforce rules that
// can be enforced at the same chokepoint inside the editor. So the editor is
// the broker: Guard is the validation layer, and it wraps the host rather than
// living behind another socket.
//
// What is NOT compressed is the agent. That stays a separate process — the
// argument for it was never validation, it was that an editor hosting a model
// owns that model's lifecycle, configuration and version skew.
type BufferHost interface {
	// Root is the workspace. Everything else is validated against it.
	Root() string

	// Buffers lists what is open.
	Buffers() []Buffer

	// Screen returns the rows currently drawn on the visible screen, top to
	// bottom, and the caret's 1-based line and column in cells. Rows are the
	// drawn screen, not the document: the renderer trims trailing spaces, and
	// a hidden or off-screen caret reports zero, which the wire reads as
	// absent. It is a read-only view for verifying what the UI actually draws.
	Screen() (rows []string, line, col int)

	// Resolve turns a request's path into the buffer's own. An empty path
	// means the buffer the user is looking at, and only the host knows which
	// that is — so every check the Guard makes has to happen on the resolved
	// name. Keying read-before-write on the literal string instead means
	// reading "" and writing "/w/a.go" look like two different buffers.
	Resolve(path string) (string, error)

	// Open puts a path in a tab and returns its version. create says a path
	// that is neither a buffer nor a file on disk is a name to make a new
	// empty buffer for; without it such a path is refused rather than
	// silently becoming a buffer nothing can read. created reports whether
	// this call made a new buffer rather than focusing one that was already
	// loaded, so a driver that meant to create a file can tell the two apart.
	Open(path string, create bool) (version uint64, created bool, err error)

	// Goto moves the cursor in a buffer to a 1-based line and column. Zero
	// means "the editor decides": a missing line keeps the cursor's own, a
	// missing column the margin.
	Goto(path string, line, col int) error

	// Reveal puts a buffer in front of the user and every attached client,
	// optionally at a byte span. start and end are byte offsets with both -1
	// meaning the whole file; the Guard refuses any other negative or a
	// reversed span. It loads a path that is not open, because a reveal is a
	// request to look at something.
	Reveal(path string, start, end int) error

	// Close removes a buffer. One with unsaved changes is refused: a silent
	// close is a silent data loss, and the editor's close-anyway prompt has no
	// machine form.
	Close(path string) error

	// CloseDiscard removes a buffer without saving, discarding unsaved
	// changes and any pending change sets. It is the machine form of the
	// editor's close-without-save, for a driver that has decided not to keep
	// the work; the file on disk is left exactly as it is. remains reports
	// whether that file is still on disk afterwards, so a driver that
	// recreated the content under a new name learns the old name is left
	// behind rather than only from the editor's status line.
	CloseDiscard(path string) (remains bool, err error)

	// Mkdir creates a directory and any missing parents under the workspace
	// root. It is a filesystem change rather than a buffer one, so it names a
	// path directly and never loads one; an existing directory is not an
	// error, matching os.MkdirAll. The host refreshes what lists the tree so
	// the new directory appears.
	Mkdir(path string) error

	// Rename moves a file within the workspace. An open clean buffer is
	// carried: its pane path, journal, LSP document and session tab follow the
	// new name rather than being reopened under it. A dirty buffer, or one
	// holding a pending change set, is refused with the reason — there is no
	// force path. A path nobody has open is an ordinary filesystem rename.
	Rename(old, new string) error

	// ProposeDeletion records a pending deletion for path, proposed by
	// author, and does not unlink anything. Delete is a review primitive: the
	// user decides whether the file goes. Idempotent, so a second proposal
	// for the same path is a no-op.
	ProposeDeletion(path string, author uint8) error

	// WithdrawDeletion retracts the pending deletion for path. A path that is
	// not pending is a no-op; a human may retract any pending removal, while
	// an agent may retract only its own.
	WithdrawDeletion(path string, author uint8) error

	// ApproveDeletion carries out the pending deletion for path: the human
	// answer to a proposal, the same removal the prompt Remove forever runs.
	// The Guard admits only a human here; the host performs the removal.
	ApproveDeletion(path string) error

	// Deletions lists the pending deletions.
	Deletions() []Deletion

	// ProposeDirRemoval records a pending dir-removal for path, proposed by
	// author, and removes nothing. It is the rmdir analogue of
	// ProposeDeletion: the user decides whether the subtree goes. Idempotent,
	// so a second proposal for the same path is a no-op.
	ProposeDirRemoval(path string, author uint8) error

	// WithdrawDirRemoval retracts the pending dir-removal for path. A path
	// that is not pending is a no-op; a human may retract any pending removal,
	// while an agent may retract only its own.
	WithdrawDirRemoval(path string, author uint8) error

	// ApproveDirRemoval carries out the pending dir-removal for path: the
	// rmdir analogue of ApproveDeletion.
	ApproveDirRemoval(path string) error

	// DirRemovals lists the pending dir-removals.
	DirRemovals() []DirRemoval

	// Hooks lists every stored hook, sorted by name. It is empty-not-nil when
	// there are none. Hooks are host state: list and show are reads, and the
	// authoring methods below are refused on a TCP connection by the server.
	Hooks() ([]HookRow, error)

	// PutHook validates row with the hooks domain and stores it, replacing any
	// row of the same name. An invalid row is refused before the store is
	// touched.
	PutHook(row HookRow) error

	// DeleteHook removes the named hook. Deleting an absent name is not an
	// error.
	DeleteHook(name string) error

	// SetHookEnabled flips the named hook's enabled flag. A name that is not
	// stored is refused.
	SetHookEnabled(name string, enabled bool) error

	// Ls lists the immediate children of a directory, sorted by name.
	// Directories are entries too, marked as such; Size is set only for a
	// regular file. all drops the internal/hidden policy and includes hidden
	// entries. It is a read, so it is ungated; the Guard resolves the path
	// before it reaches here.
	Ls(path string, all bool) ([]Entry, error)
	// Proposals is a read-only rollup over every open buffer's pending change
	// sets plus the pending file deletions and dir-removals. It is ungated,
	// like Deletions: a driver may see what is waiting without a claim.
	Proposals() []Proposal

	// Projection composes the live buffers under policy, keyed by absolute
	// editor path. Called on the event thread; the result is a snapshot.
	Projection(policy ProjectionPolicy) map[string][]byte

	// Intent answers an `intent` request: the JSON payload in, the JSON result
	// out. It is the intention workflow — new, add, remove, list, show,
	// materialise, export — and writes objects only. The op crosses TCP; the
	// object writes move no ref and never touch the worktree or the user's
	// index.
	Intent(payload string) (string, error)

	// Read returns the document as authored spans AND the version they were
	// read at.
	//
	// The version is half the point. An agent that reads at V submits offsets
	// in V's coordinates and ApplyDiff replays the journal from V forward, so
	// there is no old_str to get wrong and no whitespace sensitivity.
	//
	// The spans are the other half. Pieces already carry an author, so handing
	// back runs rather than one string tells a caller which bytes are its own,
	// which are the user's, and which belong to another agent — for free, in
	// the shape the store already holds. That is what the exec dirty-state
	// policy needs, and what stops an agent reverting text a human just typed.
	//
	// Start and End select a byte span; both -1 means the whole file, and a
	// missing End with a present Start reads to the end. LineStart and LineEnd
	// select a 1-based inclusive line range instead, which the host translates
	// to bytes — so a driver can read the line a compiler or a search reports
	// without re-implementing the editor's byte model. Both nil means read the
	// whole file. The returned spans are still in document order and may be
	// clipped at the boundaries. The text is the buffer's view,
	// Project(Annotated), because apply's hunks are in the view frame;
	// Annotated additionally returns the per-run owner and state alongside the
	// spans. The agreed composition as the default is a future change.
	Read(path string, author uint8, start, end, lineStart, lineEnd int, annotated bool) (spans []Span, states []StateRun, version uint64, err error)

	// DocSnapshot returns the whole document a client needs to render one
	// buffer itself: the encoded piecetable session, the version it answers
	// for, the file encoding as JSON, and the buffer's own canonical path.
	// Unlike Read it is not a view or a span — it is the model, so a client
	// that holds it can show unsaved text, pending change sets and the exact
	// byte lengths an offset depends on without another round trip. It does
	// not mark a read: a snapshot is for display, not for writing.
	DocSnapshot(path string) (encoded []byte, version uint64, encJSON []byte, name string, err error)

	// Find searches one buffer for a pattern and answers with the first
	// match's span, the total number of matches it holds, and the version the
	// search ran against. It is the single-document half of the search verb:
	// the matching is the editor's own engine, the same one the search pane
	// and the workspace walk use, so a find cannot disagree with a search over
	// the same text. The returned match always carries Path and Version; the
	// position fields are set only when found, and a pattern that does not
	// occur is a clean found=false rather than an error.
	Find(path string, author uint8, q SearchQuery) (match SearchMatch, count int, found bool, err error)

	// Version is what a later Apply bases on, without moving the bytes. It
	// records the read under author, so asking for a version counts as that
	// writer having seen the buffer.
	Version(path string, author uint8) (uint64, error)

	// Apply rebases hunks written against base onto the current document.
	// Hunks are rejected independently; the conflicts say which. A hunk that
	// lands over another writer's Proposed run is allowed -- the advisory
	// lease -- and that set is named in warnings, so a clean apply, an apply
	// that moved a draft, and a refusal stay distinguishable. task is the work
	// the connection registered under and is recorded on the change set this
	// call opens, so a manifest derives its rows without a caller naming one.
	Apply(path string, author uint8, base uint64, hunks []Hunk, task string) (version uint64, conflicts []Conflict, warnings []GroupOverlap, err error)

	// Save writes a buffer to disk. force answers the disk-changed prompt with
	// Overwrite, writing over a file that changed since the write raj last
	// made; without it a stale stamp is refused.
	Save(path string, force bool) (uint64, error)

	// Reload takes the buffer version on disk, matching the editor Reload
	// gesture. There is no human at the socket to ask, so it never prompts: a
	// dirty buffer is reloaded and its unsaved changes are discarded.
	Reload(path string) error

	// Groups lists the change sets in a buffer, oldest first.
	Groups(path string) ([]Group, error)

	// Decide accepts or rejects a change set. Both are pure state flips: a
	// rejection marks the set rejected and the text stays in the document,
	// dropping out of the agreed composition; it cannot fail or be wedged.
	// Clear is the operation that reverses a rejected set out.
	Decide(path string, group uint64, accept bool) error

	// Clear disposes a set no accept or reject can reach: a rejected set is
	// reversed out of the document and its decision dropped, and an invalid
	// Proposed set is marked rejected, which leaves the view with it. Unlike
	// Decide this can fail on a wedged reversal; the error names the group and
	// the blocking set.
	Clear(path string, group uint64) error

	// Revert discards a writer's own live pieces: every op the author wrote is
	// reversed out of the document and the reversal recorded in the journal,
	// the inverse of attribution. It is not gated on a decision, because it
	// removes the caller's own work rather than agreeing to someone else's; a
	// set it empties has its decision dropped. Unlike a decision this really
	// edits, so it can fail, and a member a later edit has wedged is reported
	// as a block rather than clamped.
	Revert(path string, author uint8) error

	// Diff renders the buffer's pending change sets as old→new hunks in
	// current coordinates: the review surface for what Groups only lists.
	// An empty result means no changes await a decision. Like Read, it records
	// the read under author.
	Diff(path string, author uint8) ([]DiffGroup, error)

	// Review returns the buffer's pending change sets and, unless listOnly,
	// enters Review mode at the first one. The list is what `groups` shows
	// filtered to the sets still awaiting a decision.
	Review(path string, listOnly bool) ([]Group, error)

	// Dump captures a snapshot of [start,end) at the buffer's current version,
	// returning its id, the version it was taken at, and the text. The id is
	// held per-author on the editor side, so a later Patch names it back; the
	// text is the whole chunk, so a driver edits it and returns it whole. Hash
	// is of the text, so a caller can verify the bytes it received.
	Dump(path string, start, end int, author uint8) (id uint64, version uint64, text string, hash string, err error)

	// Patch replaces a snapshot's text, diffing old against new on the editor
	// side and rebasing the result onto the current document. The id must have
	// been minted by this writer; one that has been evicted, belongs to another
	// author, or whose buffer has moved is refused rather than guessed at. A
	// hunk that lands over another writer's Proposed run is allowed -- the
	// advisory lease -- and that set is named in warnings, exactly as Apply.
	// task is recorded on the change set this call opens, as Apply's is.
	Patch(path string, author uint8, id uint64, newText string, task string) (version uint64, conflicts []Conflict, warnings []GroupOverlap, err error)

	// LSP prepares a language-server request for a 1-based line and column.
	// Mode is hover, definition, completion or diagnostics. The returned
	// caller runs the request off the event thread — see LSPCaller — and for
	// diagnostics it carries the last-known cached state rather than asking
	// the server, which is the mode that never blocks. A nil caller with a
	// non-nil error is a clean "no server" or "server not ready" answer.
	LSP(path string, line, col int, mode string) (LSPCaller, error)

	// LSPInlayHints prepares a range-scoped inlay-hint request for a file.
	// lineStart and lineEnd are 1-based inclusive lines; zero means the start
	// or the end of the file, so both zero asks about the whole file. Like LSP
	// it syncs the document first, so the hints describe unsaved text, and
	// returns a caller that runs the blocking request off the event thread. A
	// nil caller with a non-nil error is a clean "no server" or "not ready"
	// answer to retry.
	LSPInlayHints(path string, lineStart, lineEnd int) (LSPCaller, error)

	// LSPFormat prepares a whole-document or range formatting request. lineStart
	// and lineEnd are 1-based inclusive lines; both zero asks about the whole
	// document, so the range form is "some lines were named". It syncs the
	// document first, builds FormattingOptions from the buffer's indent style,
	// and returns a caller that runs the blocking request off the event thread.
	// A nil caller with a non-nil error is a clean "no server", "not ready" or
	// "this server does not format" answer.
	LSPFormat(path string, lineStart, lineEnd int) (LSPCaller, error)

	// LSPWorkspaceSymbols prepares a project-wide symbol query. There is no
	// position: the query is the whole request, and the path names the document
	// whose server should answer it. Like LSP it syncs the document first, so
	// the answer can name a symbol in unsaved text, and returns a caller that
	// runs the blocking request off the event thread. A nil caller with a
	// non-nil error is a clean "no server", "not ready" or "this server has no
	// workspace symbols" answer.
	LSPWorkspaceSymbols(path, query string) (LSPCaller, error)

	// Dirty lists unsaved buffers and whether the human wrote any of the
	// unsaved text in each. Called on the event thread, immediately before a
	// command runs.
	Dirty() []DirtyBuffer

	// Snapshot captures the open buffers for a search. It is called ON the
	// event thread; the Searcher it returns runs OFF it.
	//
	// That split is the whole design. Reading the buffers is reading the model
	// and must happen where the model is owned; walking the tree takes seconds
	// and must not. The document puts it as "snapshots, not locks" — stores
	// never erase, so a consistent view is a cheap thing to hand out.
	Snapshot() Searcher
}

// Searcher walks the workspace against a snapshot of the open buffers, calling
// emit with each file's new matches as it reaches them.
//
// One engine, not a shell-out to something faster: a second matcher would be a
// second regex dialect answering the same query differently, and against model
// latency the speed is free while coherence is not.
type Searcher interface {
	// files is how many files held a match; considered is how many were
	// opened and scanned at all — the number a too-narrow -include glob
	// leaves at zero, which is how the CLI can say the glob matched nothing
	// rather than implying the pattern did. truncated names the files the
	// per-file cap cut down, which is independent of capped: the global
	// MaxMatches flag.
	Search(ctx context.Context, q SearchQuery, emit func([]SearchMatch)) (files, considered int, capped bool, truncated []TruncatedFile, err error)
}

// hiddenSearcher is the optional half of Searcher: a searcher that can run the
// same walk with the hidden policy dropped, which is what the -hidden switch
// asks for. It is separate from Searcher so an implementation with no snapshot
// to walk need not pretend to have one, and guardedSearcher falls back to the
// ordinary Search when the inner one does not implement it.
type hiddenSearcher interface {
	SearchHidden(ctx context.Context, q SearchQuery, emit func([]SearchMatch)) (files, considered int, capped bool, truncated []TruncatedFile, err error)
}

var (
	ErrNoBuffer   = errors.New("no open buffer for that path")
	ErrOutsideoot = errors.New("path is outside the workspace")
)

// SymlinkEscapeEnv is the opt-out from the resolved-root check: any non-empty
// value other than "0" or "false" restores the behaviour before the check,
// where a symlink inside the workspace may name a target outside it. Unset or
// empty keeps the default, which resolves the path and refuses the escape. It
// is named with the other RAJ_* gates (TokenEnv, AddrEnv, RootMapEnv).
const SymlinkEscapeEnv = "RAJ_ALLOW_SYMLINK_ESCAPE"

// symlinkEscapeAllowed reports whether RAJ_ALLOW_SYMLINK_ESCAPE has turned the
// resolved-root refusal off. Every non-empty value but the two false spellings
// enables it; unset and empty keep the default.
func symlinkEscapeAllowed() bool {
	v := os.Getenv(SymlinkEscapeEnv)
	return v != "" && v != "0" && v != "false"
}

// Guard is the validation layer. Every check is a rejection, never a guess.
//
// Two of the document's checks are deliberately absent: hunk overlap and
// staleness. ApplyDiff already handles both, correctly, and a second
// implementation of the same rule in front of it is a way for the two to
// disagree.
type Guard struct {
	Host BufferHost
	// Participants resolves author ids to who they are. Optional: a Guard
	// without one falls back to the id-range rule.
	Participants *Registry

	// Git is the read-only git service the git verb runs against. Optional:
	// nil builds one over the workspace root, so a test can inject a fake
	// runner or a service aimed at a temp repository.
	Git *git.Service

	// HookGate is the server's shared hook run gate, pointed at here so the
	// event-thread admission in dispatchHook can reach it. The Server owns it;
	// this is the same ordering seam as Participants, also a pointer to
	// server state. Optional: a Guard built by hand with no gate refuses a
	// hook run rather than inventing a per-guard one.
	HookGate *hookGate

	// read records, per writer, which paths that writer has read, and at what
	// version. Read-before-write is the check that stops a blind edit: offsets
	// are meaningless except in the coordinates of a version somebody looked
	// at, and a caller that never read is submitting numbers it invented. It
	// is keyed by the durable author rather than the connection or the app, so
	// a read by one writer never satisfies another writer's write gate.
	//
	// claims is the claim op's per-identity working set: the files each author
	// has declared it is editing. It sits beside read under the same mutex
	// because both are per-writer state the Guard owns, and a claim is keyed
	// the same way — by durable author, not by connection. It is in-memory and
	// resets with the process. The write verbs enforce it through claimTarget,
	// and save is not claim-gated; it is identity-gated (see Save), because
	// only the user's own gesture writes a proposal out. The reads never consult it.
	mu     sync.Mutex
	read   map[uint8]map[string]bool
	claims map[uint8]map[string]bool
	stats  ExecStats
}

func NewGuard(h BufferHost) *Guard {
	return &Guard{Host: h, read: map[uint8]map[string]bool{}, claims: map[uint8]map[string]bool{}}
}

// markRead records that author has seen name, so that author's later write is
// not blind. It is keyed by the durable author, not the connection: a read by
// one writer must never satisfy the write gate for another.
func (g *Guard) markRead(author uint8, name string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.read == nil {
		g.read = map[uint8]map[string]bool{}
	}
	if g.read[author] == nil {
		g.read[author] = map[string]bool{}
	}
	g.read[author][name] = true
}

func (g *Guard) Root() string      { return g.Host.Root() }
func (g *Guard) Buffers() []Buffer { return g.Host.Buffers() }

// inRoot rejects a path outside the workspace, including one that reaches
// outside via "..". Cleaned first: the string form is not the check, the
// resolved form is.
func (g *Guard) inRoot(path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("%w: a path must be absolute", ErrOutsideoot)
	}
	clean := filepath.Clean(path)
	for _, root := range g.roots() {
		rel, err := filepath.Rel(filepath.Clean(root), clean)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil
		}
	}
	return fmt.Errorf("%w: %s is not under %s", ErrOutsideoot, clean, g.Host.Root())
}

// roots is the workspace root set when the host exposes one, and the single
// Root otherwise. Everything is validated against every root, so a path under
// the second root is in the workspace exactly as a path under the primary is.
func (g *Guard) roots() []string {
	if m, ok := g.Host.(interface{ Roots() []string }); ok {
		if r := m.Roots(); len(r) > 0 {
			return r
		}
	}
	if r := g.Host.Root(); r != "" {
		return []string{r}
	}
	return nil
}

// inRootResolved is inRoot with symlinks resolved. The lexical check runs
// first and always, so a ".." escape is refused exactly as it was. The path is
// then resolved with EvalSymlinks and its target checked against the resolved
// root, so a symlink inside the workspace that points outside it is refused
// before any verb reads or writes the target. The root is resolved too, so a
// workspace that is itself reached through a symlink (a temp directory under
// /var on macOS, say) is not mistaken for an escape.
//
// A path that does not exist yet has no target of its own, but its deepest
// existing ancestor is resolved as well, so creating a file through a
// symlinked parent that leaves the workspace is refused too. Only a path none
// of whose ancestors exist passes on the lexical check alone, which is what
// keeps open -create and a forward claim working. RAJ_ALLOW_SYMLINK_ESCAPE
// skips only the resolved half; the lexical refusal still applies.
func (g *Guard) inRootResolved(path string) error {
	if err := g.inRoot(path); err != nil {
		return err
	}
	if symlinkEscapeAllowed() {
		return nil
	}
	resolved, ok := resolveExistingPrefix(path)
	if !ok {
		// Nothing along the path exists, so there is no target to resolve
		// and the lexical check above is the whole gate.
		return nil
	}
	for _, root := range g.roots() {
		root = filepath.Clean(root)
		if r, rerr := filepath.EvalSymlinks(root); rerr == nil {
			root = r
		}
		rel, err := filepath.Rel(root, resolved)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil
		}
	}
	return fmt.Errorf("%w: %s resolves to %s, outside %s", ErrOutsideoot, path, resolved, g.Host.Root())
}

// resolveExistingPrefix resolves path's symlinks as far as it exists and
// re-appends the not-yet-existing remainder, so a path whose leaf is new but
// whose parent is a symlink out of the tree is checked against the directory it
// would be created in. ok is false when no prefix of the path exists, which
// leaves the lexical check as the whole gate.
func resolveExistingPrefix(path string) (string, bool) {
	clean := filepath.Clean(path)
	var missing []string
	for {
		if resolved, err := filepath.EvalSymlinks(clean); err == nil {
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return resolved, true
		}
		parent := filepath.Dir(clean)
		if parent == clean {
			return "", false
		}
		missing = append(missing, filepath.Base(clean))
		clean = parent
	}
}

// inRootDir is inRoot for a directory a caller names relative to the workspace
// root, which is how search -path and exec -dir are spelled. A relative name
// is resolved against the root first; the check that follows is the same one,
// so a relative ".." cannot reach outside any more than an absolute one can.
func (g *Guard) inRootDir(dir string) error {
	if dir == "" {
		return nil
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(g.Host.Root(), dir)
	}
	return g.inRootResolved(dir)
}

// canonical resolves and validates in one step, so no method can accidentally
// check one name and act on another.
func (g *Guard) canonical(path string) (string, error) {
	// Resolve and check the root BEFORE Host.Resolve loads the file, so a
	// symlink that points outside is refused before its target is read. Open
	// is the exception: its path names a file that is not a buffer yet, so it
	// has its own gate and never comes through here.
	if path != "" {
		check := path
		if !filepath.IsAbs(check) {
			check = filepath.Join(g.Host.Root(), check)
		}
		if err := g.inRootResolved(check); err != nil {
			return "", err
		}
	}
	resolved, err := g.Host.Resolve(path)
	if err != nil {
		return "", err
	}
	// A second look at what Resolve named, this time resolved: it catches a
	// host that answered with a name outside the root, including the pathless
	// case where the resolved name is the buffer the user is looking at.
	if err := g.inRootResolved(resolved); err != nil {
		return "", err
	}
	return resolved, nil
}

// claimTarget resolves the file a text write may touch and enforces the claim
// set of the writing author. It is the gate for the write verbs, not for reads:
// the explicit path is canonicalised exactly as it was before claims existed
// (resolved, then checked in-root) and must be in the set, while a pathless
// write targets the sole claimed file, so it never falls back to the buffer the
// user is looking at.
//
// The refusals are concrete on purpose: the set is named so a caller can see
// what it did claim, and an empty set says what to do first. Read, Version,
// Diff and Dump never consult a claim and keep the focused-buffer default a
// read has always had.
func (g *Guard) claimTarget(author uint8, path string) (string, error) {
	if path != "" {
		name, err := g.canonical(path)
		if err != nil {
			return "", err
		}
		if err := g.claimCheck(author, name); err != nil {
			return "", err
		}
		return name, nil
	}

	g.mu.Lock()
	claims := sortedPaths(g.claims[author])
	g.mu.Unlock()
	switch len(claims) {
	case 0:
		return "", errors.New("claim a file first")
	case 1:
		return claims[0], nil
	default:
		return "", fmt.Errorf("claim set has %d files; name one", len(claims))
	}
}

// claimCheck reports whether name is in author's claim set, naming the set in
// its refusal exactly as claimTarget does. It is the membership half of the
// gate, split from the resolve step so delete can run it against a path
// canonicalised without loading a buffer: deleting a file is not a text write,
// and recording a proposal must not have to read the file it names.
func (g *Guard) claimCheck(author uint8, name string) error {
	g.mu.Lock()
	claims := sortedPaths(g.claims[author])
	g.mu.Unlock()
	if len(claims) == 0 {
		return errors.New("claim a file first")
	}
	for _, p := range claims {
		if p == name {
			return nil
		}
	}
	return fmt.Errorf("not in your claim set (%s); claim -add <path>", strings.Join(claims, ", "))
}

func (g *Guard) Open(path string, create bool) (uint64, bool, error) {
	// Open names a file that may not be a buffer yet, so unlike every other
	// verb there is no Resolve to canonicalise it. A relative path is made
	// absolute against the root here, the rule the host applies to every other
	// verb, so open accepts the same spelling as read; the root check below
	// then runs on the path it names rather than on how it was spelled.
	if path != "" && !filepath.IsAbs(path) {
		path = filepath.Join(g.Host.Root(), path)
	}
	if err := g.inRootResolved(path); err != nil {
		return 0, false, err
	}
	return g.Host.Open(path, create)
}

// Mkdir creates a directory, with any missing parents, under the workspace
// root. A directory is not text and is not a buffer, so it is canonicalised
// without loading one: a relative name is joined against the root, cleaned, and
// checked in-root with the same check every other path gets. The claim gate is
// deliberately absent — a claim is about a file, and a new directory has no
// file to claim yet — so a caller may make a package directory before it has a
// file to put in it. Out of root is a refusal, not a clamp.
func (g *Guard) Mkdir(path string) error {
	if path == "" {
		return errors.New("mkdir needs a path")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(g.Host.Root(), path)
	}
	path = filepath.Clean(path)
	if err := g.inRootResolved(path); err != nil {
		return err
	}
	return g.Host.Mkdir(path)
}

// Rename moves a claimed path within the workspace.
//
// It is claim-gated on the OLD name like a text write — the path must be in the
// caller's set — but deliberately does NOT require read-before-write: no
// offset is at stake, and the host carries an open clean buffer across the
// rename rather than reopening it. Both names are canonicalised by claimPath,
// which checks them in-root without loading a buffer, so a rename does not have
// to open the file it moves. Out of root is a refusal, not a clamp.
//
// The destination must not already exist: a rename onto another file would
// clobber a path the caller never claimed, and no verb may do that silently.
// The exception is a case-only rename on a case-insensitive filesystem, where
// the destination and the source resolve to one file; refusing that would make
// `a.go -> A.go` impossible there, so it is allowed and the host uses the
// two-step rename. On success the caller's claim set follows old to new, so the
// working set names the file that now exists; on failure it is left untouched.
func (g *Guard) Rename(old, new string, author uint8) error {
	if old == "" {
		return errors.New("rename needs an old path")
	}
	if new == "" {
		return errors.New("rename needs a new path")
	}
	src, err := g.claimPath(old)
	if err != nil {
		return err
	}
	dst, err := g.claimPath(new)
	if err != nil {
		return err
	}
	if err := g.claimCheck(author, src); err != nil {
		return err
	}
	if dstInfo, derr := os.Stat(dst); derr == nil {
		// Refuse unless the destination is the same file as the source, which
		// is what a case-only rename looks like on a case-insensitive
		// filesystem. There the two spellings resolve to one file, and
		// refusing would make the case change impossible to express.
		srcInfo, serr := os.Stat(src)
		if serr != nil || !os.SameFile(srcInfo, dstInfo) {
			return fmt.Errorf("%s already exists", dst)
		}
	} else if !os.IsNotExist(derr) {
		return derr
	}
	if err := g.Host.Rename(src, dst); err != nil {
		return err
	}
	g.renameClaim(author, src, dst)
	return nil
}

// Delete proposes a pending deletion for a claimed path, or withdraws a
// proposal. A proposal is claim-gated like a text write — the path must be in
// the caller's claim set — because the claim is the record of what an agent
// declared it would touch. A withdraw is not claim-gated: the claim set belongs
// to the agent that proposed, and the person retracting it need not hold one,
// the same rule ApproveDeletion follows. It deliberately does NOT require
// read-before-write: deleting is not a text write and no offset is at stake.
// The path is canonicalised by claimPath, which checks it in-root without
// loading a buffer, so proposing a deletion does not have to open the file it
// names.
func (g *Guard) Delete(path string, author uint8, withdraw bool) error {
	if path == "" {
		return errors.New("delete needs a path")
	}
	name, err := g.claimPath(path)
	if err != nil {
		return err
	}
	if withdraw {
		return g.Host.WithdrawDeletion(name, author)
	}
	if err := g.claimCheck(author, name); err != nil {
		return err
	}
	if err := g.checkOperandKind(name, false); err != nil {
		return err
	}
	return g.Host.ProposeDeletion(name, author)
}

// Deletions lists the pending deletions in stable path order. It is ungated: a
// driver may see what is waiting without holding a claim on any of it.
func (g *Guard) Deletions() []Deletion {
	out := g.Host.Deletions()
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// Rmdir proposes a pending dir-removal for a claimed directory, or withdraws a
// proposal. It is the rmdir analogue of Delete: a proposal is claim-gated — the
// directory itself is a claim entry (claim <dir> records it, per §11), so the
// gate is one claimCheck on the dir — while a withdraw is not, for the same
// reason Delete's is not. Like Delete it deliberately does NOT require
// read-before-write: a dir-removal is not a text write and no offset is at
// stake. The path is canonicalised by claimPath, which checks it in-root
// without loading a buffer.
func (g *Guard) Rmdir(path string, author uint8, withdraw bool) error {
	if path == "" {
		return errors.New("rmdir needs a path")
	}
	name, err := g.claimPath(path)
	if err != nil {
		return err
	}
	if withdraw {
		return g.Host.WithdrawDirRemoval(name, author)
	}
	if err := g.claimCheck(author, name); err != nil {
		return err
	}
	if err := g.checkOperandKind(name, true); err != nil {
		return err
	}
	return g.Host.ProposeDirRemoval(name, author)
}

// Rmdirs lists the pending dir-removals in stable path order. It is ungated,
// like Deletions.
func (g *Guard) Rmdirs() []DirRemoval {
	out := g.Host.DirRemovals()
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// ApproveDeletion carries out the pending deletion for path. It is the human
// answer to a proposal, so only the local keyboard row or a durable joined
// human may ask; an agent, a provisional connection and the file-as-loaded are
// refused in the same shape as Save. The path is canonicalised the way Delete
// canonicalises it -- in-root, without loading a buffer -- but it is not
// claim-gated: the claim set belongs to the agent that proposed, and the
// person answering need not hold one.
func (g *Guard) ApproveDeletion(path string, author uint8) error {
	if path == "" {
		return errors.New("approve needs a path")
	}
	if !g.humanAuthor(author) {
		return errors.New(errApproveDeletionNotHuman)
	}
	name, err := g.claimPath(path)
	if err != nil {
		return err
	}
	return g.Host.ApproveDeletion(name)
}

// ApproveDirRemoval is the rmdir analogue of ApproveDeletion: the human answer
// to a pending dir-removal, canonically checked in-root and not claim-gated.
func (g *Guard) ApproveDirRemoval(path string, author uint8) error {
	if path == "" {
		return errors.New("approve needs a path")
	}
	if !g.humanAuthor(author) {
		return errors.New(errApproveDirNotHuman)
	}
	name, err := g.claimPath(path)
	if err != nil {
		return err
	}
	return g.Host.ApproveDirRemoval(name)
}

// Hooks, PutHook, DeleteHook and SetHookEnabled pass the hook surface down to
// the host. They add no policy of their own: validation lives in the hooks
// domain, which the host applies, and the transport rule lives in localOnly,
// so the Guard is only the ordering seam.
func (g *Guard) Hooks() ([]HookRow, error) { return g.Host.Hooks() }

func (g *Guard) PutHook(row HookRow) error { return g.Host.PutHook(row) }

func (g *Guard) DeleteHook(name string) error { return g.Host.DeleteHook(name) }

func (g *Guard) SetHookEnabled(name string, enabled bool) error {
	return g.Host.SetHookEnabled(name, enabled)
}

// Ls lists a directory's immediate children. It is a read, ungated like
// Deletions, but the path still has to resolve inside the workspace: ls is not
// a way to read the filesystem outside the tree. An empty path names the
// workspace itself, which is the verb's default: the host answers with one
// root's children, or a top-level entry per root when there are several. For an
// explicit path claimPath is the canonicaliser rather than canonical, because
// the latter goes through Host.Resolve and a directory is not a buffer to load;
// the same split Rmdir uses.
func (g *Guard) Ls(path string, all bool) ([]Entry, error) {
	if path == "" {
		// Empty names the workspace, and the host is what knows whether that
		// is one root's children or a row per root. It also validates its
		// own default, so nothing is read outside a root.
		return g.Host.Ls("", all)
	}
	name, err := g.claimPath(path)
	if err != nil {
		return nil, err
	}
	return g.Host.Ls(name, all)
}

// checkOperandKind stats a file-operation operand and refuses the wrong kind:
// delete names a file, rmdir names a directory. Without this the record was made
// without looking, so a path that is really a file could be proposed for
// removal as a directory and vice versa, and only the human prompt would catch
// the mistake. wantDir says which kind the verb is for.
//
// A path that is not on disk is allowed. A proposal is forward-looking — a
// claim may name a file the writer is about to create, and open -create makes a
// buffer before any bytes exist — so there is no file type to check yet, and
// refusing would make the verb unusable in exactly the window the claim set
// covers. The human prompt is the real gate once the path exists.
//
// Only the proposing path checks. A withdrawal has to keep working after the
// file is gone, so it never reaches here.
func (g *Guard) checkOperandKind(name string, wantDir bool) error {
	info, err := os.Stat(name)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if wantDir {
		if !info.IsDir() {
			return fmt.Errorf("%s is not a directory; rmdir removes directories (use delete for a file)", name)
		}
		return nil
	}
	if info.IsDir() {
		return fmt.Errorf("%s is a directory; delete removes files (use rmdir for a directory)", name)
	}
	return nil
}

// Proposals rolls the pending listings into one flat tagged list: each open
// buffer's pending change sets and invalid sets, the pending file deletions
// and the pending dir-removals. It is a read-only view, ungated like
// Deletions, and the order is deterministic — kind (set, delete, rmdir,
// invalid), then path, then group id — so a caller can compare two listings
// directly.
func (g *Guard) Proposals() []Proposal {
	out := g.Host.Proposals()
	rank := func(kind string) int {
		switch kind {
		case "set":
			return 0
		case "delete":
			return 1
		case "rmdir":
			return 2
		case "invalid":
			return 3
		default:
			return 4
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if ri, rj := rank(out[i].Kind), rank(out[j].Kind); ri != rj {
			return ri < rj
		}
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Group < out[j].Group
	})
	return out
}

// Goto routes a cursor move through the same resolution every other verb
// uses, so "goto 40" and "goto /w/a.go 40" cannot mean different buffers.
func (g *Guard) Goto(path string, line, col int) error {
	name, err := g.canonical(path)
	if err != nil {
		return err
	}
	return g.Host.Goto(name, line, col)
}

// Reveal routes a reveal through the same resolution every other verb uses,
// and validates a byte span the way apply validates a hunk: both -1 means the
// whole file, and any other negative or a reversed span is refused before the
// host is asked.
func (g *Guard) Reveal(path string, start, end int) error {
	if err := validateRevealSpan(start, end); err != nil {
		return err
	}
	name, err := g.canonical(path)
	if err != nil {
		return err
	}
	return g.Host.Reveal(name, start, end)
}

// validateRevealSpan is the reveal span rule: (-1,-1) is the whole file, and
// anything else must be a non-negative, ordered byte span. A lone -1 is
// refused rather than read as "to the end", because a single default is a
// caller that said nothing about one end.
func validateRevealSpan(start, end int) error {
	if start == -1 && end == -1 {
		return nil
	}
	if start < 0 || end < 0 {
		return fmt.Errorf("reveal span: start %d, end %d; both -1 means the whole file", start, end)
	}
	if end < start {
		return fmt.Errorf("reveal span: start %d, end %d; end is before start", start, end)
	}
	return nil
}

// Close refuses a dirty buffer before the host is asked, so the refusal
// reads the same for every implementation of BufferHost.
func (g *Guard) Close(path string) error {
	name, err := g.canonical(path)
	if err != nil {
		return err
	}
	return g.Host.Close(name)
}

// Discard closes a buffer without saving, discarding unsaved changes and any
// pending change sets. It is not a text write — nothing is written and no
// offset is checked — so it does not run the claim gate; the file on disk is
// left exactly as it is.
func (g *Guard) Discard(path string) (bool, error) {
	name, err := g.canonical(path)
	if err != nil {
		return false, err
	}
	return g.Host.CloseDiscard(name)
}

func (g *Guard) Read(path string, author uint8, start, end, lineStart, lineEnd int, annotated bool) ([]Span, []StateRun, uint64, error) {
	name, err := g.canonical(path)
	if err != nil {
		return nil, nil, 0, err
	}
	spans, states, v, err := g.Host.Read(name, author, start, end, lineStart, lineEnd, annotated)
	if err == nil {
		g.markRead(author, name)
	}
	return spans, states, v, err
}

// readBuffer is the buffer record a read reply carries for a target, or false
// when the name is not open. Its Bytes and Lines are the whole-buffer numbers a
// driver needs to append without a second version call, exactly as the version
// verb reports them, and its Deleted flag is what lets a read of a file that is
// gone say so on the same reply that carries the text rather than handing the
// text back as though the file were still there.
func (g *Guard) readBuffer(path string) (Buffer, bool) {
	name, err := g.canonical(path)
	if err != nil {
		return Buffer{}, false
	}
	for _, b := range g.Buffers() {
		if b.Path == name {
			return b, true
		}
	}
	return Buffer{}, false
}

func (g *Guard) DocSnapshot(path string) ([]byte, uint64, []byte, string, error) {
	name, err := g.canonical(path)
	if err != nil {
		return nil, 0, nil, "", err
	}
	return g.Host.DocSnapshot(name)
}

func (g *Guard) Version(path string, author uint8) (uint64, error) {
	name, err := g.canonical(path)
	if err != nil {
		return 0, err
	}
	v, err := g.Host.Version(name, author)
	if err == nil {
		// Asking for a version is asking for coordinates, which is the thing
		// read-before-write is checking for. A caller that took the version
		// deliberately is not editing blind.
		g.markRead(author, name)
	}
	return v, err
}

// Dump is a read in the read-before-write sense: it hands back a chunk of text
// and records that the caller has seen the buffer, so a later patch or apply is
// not blind.
func (g *Guard) Dump(path string, start, end int, author uint8) (uint64, uint64, string, string, error) {
	name, err := g.canonical(path)
	if err != nil {
		return 0, 0, "", "", err
	}
	id, v, text, hash, err := g.Host.Dump(name, start, end, author)
	if err == nil {
		g.markRead(author, name)
	}
	return id, v, text, hash, err
}

// Diff is a read in the read-before-write sense, like Dump: it hands back
// chunks of the buffer's text and records that the caller has seen them.
func (g *Guard) Diff(path string, author uint8) ([]DiffGroup, error) {
	name, err := g.canonical(path)
	if err != nil {
		return nil, err
	}
	diffs, err := g.Host.Diff(name, author)
	if err == nil {
		g.markRead(author, name)
	}
	return diffs, err
}

// Review canonicalises the path and passes through, entering the mode unless
// listOnly. It deliberately does not satisfy read-before-write: the pending
// sets carry no text or offsets, so a caller that has only reviewed has not
// seen the coordinates an edit would need.
func (g *Guard) Review(path string, listOnly bool) ([]Group, error) {
	name, err := g.canonical(path)
	if err != nil {
		return nil, err
	}
	return g.Host.Review(name, listOnly)
}

// Patch is patch with no task: the in-process form, for a caller that has no
// connection identity. A socket's task comes from its participant, and
// Dispatch reaches the task-aware patch below.
func (g *Guard) Patch(path string, author uint8, id uint64, newText string) (uint64, []Conflict, []GroupOverlap, error) {
	return g.patch(path, author, id, newText, "")
}

// patch writes, so it runs the same author check as Apply: a socket may not
// write as the file-as-loaded nor as a person, or its text becomes
// indistinguishable from typed text. The offsets are implicit — the snapshot's
// span told the editor where the chunk lives — so there is no hunk span to
// validate here; the host refuses a snapshot this writer does not own.
func (g *Guard) patch(path string, author uint8, id uint64, newText, task string) (uint64, []Conflict, []GroupOverlap, error) {
	if author == AuthorOriginal {
		return 0, nil, nil, fmt.Errorf("author %d is the file as loaded, not a writer", author)
	}
	if g.Participants != nil {
		if !g.Participants.IsAgent(author) && !g.Participants.IsProvisional(author) {
			return 0, nil, nil, fmt.Errorf("author %d is not an agent", author)
		}
	} else if author < FirstAgent {
		return 0, nil, nil, fmt.Errorf("author %d is not an agent id", author)
	}
	name, err := g.claimTarget(author, path)
	if err != nil {
		return 0, nil, nil, err
	}
	return g.Host.Patch(name, author, id, newText, task)
}

// Apply is apply with no task: the in-process form, for a caller that has no
// connection identity. A socket's task comes from its participant, and
// Dispatch reaches the task-aware apply below.
func (g *Guard) Apply(path string, author uint8, base uint64, hunks []Hunk) (uint64, []Conflict, []GroupOverlap, error) {
	return g.apply(path, author, base, hunks, "")
}

func (g *Guard) apply(path string, author uint8, base uint64, hunks []Hunk, task string) (uint64, []Conflict, []GroupOverlap, error) {
	// A socket may not write as the file-as-loaded. It may write as an agent,
	// as an undeclared (provisional) connection, or as a durable joined human
	// other than the local one: an attached client joins as a second human, and
	// its text is the person's own accepted edit rather than a proposal,
	// while the local keyboard row stays unclaimable by a socket.
	//
	// A registry lookup rather than `author >= 2`: once a second person can
	// edit the same workspace, the id number cannot say what kind of writer it
	// belongs to. Without a registry the old rule stands, so a Guard built by
	// hand still refuses 0 and 1.
	if author == AuthorOriginal {
		return 0, nil, nil, fmt.Errorf("author %d is the file as loaded, not a writer", author)
	}
	if g.Participants != nil {
		if !g.Participants.IsAgent(author) && !g.Participants.IsProvisional(author) &&
			!g.writesAsHuman(author) {
			return 0, nil, nil, fmt.Errorf("author %d is not an agent or a joined human", author)
		}
	} else if author < FirstAgent {
		return 0, nil, nil, fmt.Errorf("author %d is not an agent id", author)
	}
	name, err := g.claimTarget(author, path)
	if err != nil {
		return 0, nil, nil, err
	}
	g.mu.Lock()
	seen := g.read[author][name]
	g.mu.Unlock()
	if !seen {
		return 0, nil, nil, errors.New("read the buffer before writing it: offsets only mean " +
			"something in the coordinates of a version you have seen")
	}
	for i, h := range hunks {
		if h.Start < 0 || h.End < h.Start {
			return 0, nil, nil, fmt.Errorf("hunk %d: start %d, end %d", i, h.Start, h.End)
		}
	}
	return g.Host.Apply(name, author, base, hunks, task)
}

// writesAsHuman reports whether id is a durable joined human other than the
// local keyboard row. An attached client hellos as a second human, and its
// writes are that person's own accepted text; the local row is excluded so a
// socket cannot claim to be the person at the keyboard.
func (g *Guard) writesAsHuman(id uint8) bool {
	if g.Participants == nil || id == AuthorOriginal || id == LocalHuman {
		return false
	}
	p, ok := g.Participants.Get(id)
	return ok && p.Kind == KindHuman
}

// humanAuthor reports whether author may run a human-only gesture: the local
// keyboard row, or a durable joined human (an attached client). An agent, an
// undeclared connection and the file-as-loaded are refused. Without a registry
// the id rule still stands, so a Guard built by hand refuses an agent.
func (g *Guard) humanAuthor(author uint8) bool {
	if author == LocalHuman {
		return true
	}
	if g.Participants == nil {
		return author != AuthorOriginal && author < FirstAgent
	}
	return g.writesAsHuman(author)
}

// IsHuman reports whether author may run a human-only gesture: the local
// keyboard row, or a durable joined human (an attached client). It is
// humanAuthor exposed so the app's removal owner check can share the exact
// predicate the Guard applies rather than reimplementing the id rule and
// drifting from it.
func (g *Guard) IsHuman(author uint8) bool { return g.humanAuthor(author) }

// taskOf is the work a writer's writes belong to: the task its connection
// registered with, empty for the local human and for a connection that
// registered none. It is derived from the registry by author rather than
// carried on every frame, so an ordinary reconnect does not have to restate
// it and a program's sub-requests inherit it from the connection's author.
func (g *Guard) taskOf(author uint8) string {
	if g.Participants == nil {
		return ""
	}
	if p, ok := g.Participants.Get(author); ok {
		return p.Task
	}
	return ""
}

// escapingGlob reports a glob that reaches outside the workspace. The document
// names this separately from the path check because it IS separate: validating
// `path` and forgetting `Include`/`Exclude` leaves the same hole open under a
// different field name.
func escapingGlob(patterns string) error {
	for _, p := range strings.Split(patterns, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if filepath.IsAbs(p) {
			return fmt.Errorf("%w: glob %q is absolute", ErrOutsideoot, p)
		}
		for _, part := range strings.Split(filepath.ToSlash(p), "/") {
			if part == ".." {
				return fmt.Errorf("%w: glob %q reaches outside", ErrOutsideoot, p)
			}
		}
	}
	return nil
}

// CheckQuery validates a search before it starts. Separate from running one
// because the two now happen on different goroutines.
func (g *Guard) CheckQuery(q SearchQuery) error {
	if strings.TrimSpace(q.Text) == "" {
		return errors.New("search needs a pattern")
	}
	// The document names glob escape separately from path escape because it IS
	// separate: validating `path` and forgetting Include/Exclude leaves the
	// same hole open under a different field name.
	if err := escapingGlob(q.Include); err != nil {
		return err
	}
	if err := escapingGlob(q.Exclude); err != nil {
		return err
	}
	// The scope is a path like any other: it must resolve inside the workspace
	// before a walk starts from it, or a search becomes a way to read files the
	// editor would refuse to open.
	return g.inRootDir(q.Path)
}

// Snapshot hands out a searcher wrapped so that a query is validated at the
// point it runs, whichever goroutine that is.
func (g *Guard) Snapshot() Searcher { return guardedSearcher{g, g.Host.Snapshot()} }

type guardedSearcher struct {
	g     *Guard
	inner Searcher
}

func (s guardedSearcher) Search(ctx context.Context, q SearchQuery, emit func([]SearchMatch)) (int, int, bool, []TruncatedFile, error) {
	if err := s.g.CheckQuery(q); err != nil {
		return 0, 0, false, nil, err
	}
	// The include-hidden switch is a walk with the policy dropped, and only
	// the inner searcher can run it: it holds the open-buffer snapshot the
	// walk overlays. One that cannot — a test fake, a future adapter — falls
	// back to the ordinary walk rather than failing the query.
	if q.Hidden {
		if hs, ok := s.inner.(hiddenSearcher); ok {
			return hs.SearchHidden(ctx, q, emit)
		}
	}
	return s.inner.Search(ctx, q, emit)
}

// Save writes a buffer to disk for the person at the keyboard. It is the one
// verb an agent cannot run: a proposal becomes disk bytes through the user's
// save, never the writer's own, and a socket that could save would make every
// review gate decorative. The local keyboard row is allowed by id; a durable
// joined human is an attached client saving as its own person; an agent, an
// undeclared connection and the file-as-loaded are refused. Without a registry
// the id rule still stands, so a Guard built by hand refuses an agent.
func (g *Guard) Save(path string, author uint8, force bool) (uint64, error) {
	if !g.humanAuthor(author) {
		return 0, errors.New(errSaveNotHuman)
	}
	name, err := g.canonical(path)
	if err != nil {
		return 0, err
	}
	return g.Host.Save(name, force)
}

// // Land is the human's one-gesture landing: accept every pending change set
// whose task is task, in every buffer, then save each buffer whose pending
// sets all belonged to that task. A buffer that also holds another task's
// pending sets has this task's accepted but is not saved -- save is the
// approval gesture, so writing it would approve work this land did not name;
// such a buffer is reported held. A buffer whose save is refused for another
// reason carries that error in the report rather than being skipped silently.
// The caller has already passed the human gate; Save re-checks it, so a land
// is no weaker than the save it performs.
//
// A set's overlap report is gone once it is accepted, so the wave's dependency
// graph is read before anything is accepted (DependencyWarning): a non-linear
// dependency is reported, never guessed. After the saves the accepted wave is
// exported as one commit on raj/baseline (landExport); an export failure is
// reported rather than failing the gesture that already approved the work.
func (g *Guard) Land(task string, author uint8) ([]LandFile, *LandExport, error) {
	if task == "" {
		return nil, nil, errors.New("land needs a task; pass the task its change sets were written under")
	}
	// Which buffer holds which set, gathered first so the overlap edges (only
	// reported for still-proposed sets) can be read before accepting anything.
	type landJob struct {
		path    string
		mine    []uint64
		foreign bool
		err     string
	}
	var jobs []landJob
	var wave []intent.WaveGroup
	for _, b := range g.Host.Buffers() {
		if b.Path == "" {
			// An unnamed buffer has no path to save or to name in a report.
			continue
		}
		name, err := g.canonical(b.Path)
		if err != nil {
			jobs = append(jobs, landJob{path: b.Path, err: err.Error()})
			continue
		}
		groups, err := g.Host.Groups(name)
		if err != nil {
			jobs = append(jobs, landJob{path: name, err: err.Error()})
			continue
		}
		job := landJob{path: name}
		for _, gr := range groups {
			if gr.State != "proposed" || gr.Invalid {
				continue
			}
			if gr.Task != task {
				job.foreign = true
				continue
			}
			job.mine = append(job.mine, gr.ID)
			wg := intent.WaveGroup{ID: gr.ID, Path: name}
			if gr.Overlaps != nil {
				for _, ov := range gr.Overlaps.Sets {
					wg.Overlaps = append(wg.Overlaps, ov.Group)
				}
			}
			wave = append(wave, wg)
		}
		jobs = append(jobs, job)
	}
	warning := intent.DependencyWarning(wave)

	var out []LandFile
	accepted := 0
	for _, job := range jobs {
		if job.err != "" {
			out = append(out, LandFile{Path: job.path, Err: job.err})
			continue
		}
		if len(job.mine) == 0 {
			continue
		}
		ok := true
		for _, id := range job.mine {
			if err := g.Host.Decide(job.path, id, true); err != nil {
				out = append(out, LandFile{Path: job.path, Err: err.Error()})
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		accepted += len(job.mine)
		f := LandFile{Path: job.path, Sets: len(job.mine)}
		if job.foreign {
			f.Held = true
			out = append(out, f)
			continue
		}
		if _, err := g.Save(job.path, author, false); err != nil {
			f.Err = err.Error()
			out = append(out, f)
			continue
		}
		f.Saved = true
		out = append(out, f)
	}
	if accepted == 0 {
		return out, nil, nil
	}
	exp, err := g.landExport(task, warning)
	if err != nil {
		// The sets are accepted and the buffers saved; the commit did not
		// happen. Report the export failure rather than undoing a gesture the
		// person already made.
		return out, &LandExport{Ref: git.BaselineRef, Warning: warning, Err: err.Error()}, nil
	}
	return out, exp, nil
}

// landExport asks the host to export the landed wave as one commit on
// raj/baseline. The overlap warning was computed before the sets were accepted
// (their overlap reports are gone once accepted), so it rides along. The
// caller reports an error rather than failing the land that already saved.
func (g *Guard) landExport(task, warning string) (*LandExport, error) {
	payload, err := json.Marshal(intent.Command{Mode: "land", Task: task})
	if err != nil {
		return nil, err
	}
	out, err := g.Host.Intent(string(payload))
	if err != nil {
		return nil, err
	}
	var res intent.Result
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		return nil, err
	}
	return &LandExport{
		Commit: res.Commit, Parent: res.Parent, BaseSHA: res.BaseSHA,
		Tree: res.Tree, Ref: git.BaselineRef, Warning: warning,
	}, nil
}

// Reload takes the on-disk version for a buffer. It is a read of the disk into
// the model, not a text write, so it runs no claim gate; the host owns the
// reload and the dirty-buffer wording.
func (g *Guard) Reload(path string) error {
	name, err := g.canonical(path)
	if err != nil {
		return err
	}
	return g.Host.Reload(name)
}

// Clear hard-purges a rejected change set, addressed by group rather than by
// the caret. It is a write, so it runs the claim gate like Apply and Patch:
// only a claimed file can have text reversed out over the socket. The host
// owns the rejection state and reports a set that is not rejected or that a
// later edit has wedged.
func (g *Guard) Clear(path string, author uint8, group uint64) error {
	name, err := g.claimTarget(author, path)
	if err != nil {
		return err
	}
	return g.Host.Clear(name, group)
}

// Revert discards the calling writer's own live pieces. It is a write, so it
// runs the claim gate like Apply and Clear: only a claimed file can have text
// reversed out over the socket. The author is the connection's own id and
// nothing else: dropping another writer's accepted pieces is the user's
// decision, taken through reject and clear, so no verb here names a peer. The
// host owns the journal and reports a reversal a later edit has wedged.
func (g *Guard) Revert(path string, author uint8) error {
	name, err := g.claimTarget(author, path)
	if err != nil {
		return err
	}
	return g.Host.Revert(name, author)
}

// Exec policy: run, and say what is stale.
//
// A subprocess reads files, not buffers. When an agent runs `go test`, the
// processes that open the source are `go build`, the compiler and the test
// binary, all calling open(2) — there is no seam to interpose on. So with an
// unsaved buffer the command is answering a question about text nobody is
// looking at, and its failures may be about code that no longer exists.
//
// That is a correctness problem for the CALLER, and the fix is to tell it.
// Refusing was considered and rejected: it turns a solvable interpretation
// problem into a blocked one, and the common path becomes exec, refusal, save,
// exec — while an agent that could not run anything until every buffer was
// saved would spend its time pressing save on the user's behalf.
//
// Flushing is the other alternative and is worse. Writing the user's unsaved
// edits to disk as a side effect of an agent's action is not destructive in
// itself — the buffer keeps its undo — but everything downstream of the
// filesystem reacts: formatters, watchers, `git status`, a dev server
// reloading. Nothing here should cause those without being asked.
//
// So: run it, report the stale files, count them. `exec --projected` is the
// other answer: it materialises the accepted-and-proposed composition into a
// scratch tree and runs there, so the stale count is what says how much the
// ordinary worktree run still costs.

// CheckExec validates a command and reports which buffers are stale. Called on
// the event thread, since it reads the buffers. A non-nil error refuses the
// command; a stale buffer is not one.
func (g *Guard) CheckExec(argv []string, dir string) ([]DirtyBuffer, error) {
	if len(argv) == 0 || strings.TrimSpace(argv[0]) == "" {
		return nil, errors.New("exec needs a command")
	}
	if dir != "" {
		// inRootDir, not the absolute-only resolved check: exec -dir is
		// documented to take a relative name, and inRootDir resolves against
		// the root before running the same symlink-aware check.
		if err := g.inRootDir(dir); err != nil {
			return nil, err
		}
	}
	dirty := g.Host.Dirty()
	g.mu.Lock()
	defer g.mu.Unlock()
	g.stats.Runs++
	if len(dirty) == 0 {
		return nil, nil
	}
	g.stats.Stale++
	agentOnly := true
	for _, d := range dirty {
		if !d.AgentOnly {
			agentOnly = false
			break
		}
	}
	if agentOnly {
		g.stats.AgentOnly++
	}
	return dirty, nil
}

// Stats reports what the policy has cost this session.
func (g *Guard) Stats() ExecStats {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.stats
}

// Claim sets, extends, clears or reports an author's claim set: the files that
// identity declares it is working on. A path is made absolute against the
// workspace root and checked in-root without loading a buffer — a claim says
// what is about to be edited, so it must not make the editor open anything. A
// syntactically valid in-root path is claimed whether or not it is on disk yet:
// a claim is forward-looking, so the buffer open -create makes and the file a
// writer is about to create are both claimable. Only a stat that fails for a
// reason other than absence — a permission error, say — is named in warnings
// and skipped, so the rest of the command still lands.
//
// The set comes back in stable (sorted) order, along with the other writers
// whose sets share a path, resolved to a display name (or the identity key
// when no name is set) when a registry is available. Claims are not locks, so an overlap is a report and not a refusal.
// The write verbs enforce the set in claimTarget: apply, patch and clear each
// resolve their target through it, so a socket write cannot touch a file the
// caller has not claimed. save is the human decision and is not gated.
func (g *Guard) Claim(author uint8, paths []string, add, clear bool) (claims, warnings []string, overlaps []ClaimOverlap, err error) {
	if len(paths) == 0 && !add && !clear {
		claims = g.claimSet(author)
		return claims, nil, g.claimOverlaps(author, claims), nil
	}
	if clear {
		g.clearClaims(author)
		return nil, nil, nil, nil
	}
	kept := make([]string, 0, len(paths))
	seen := make(map[string]bool, len(paths))
	keep := func(p string) {
		if seen[p] {
			return
		}
		seen[p] = true
		kept = append(kept, p)
	}
	for _, raw := range paths {
		p, cerr := g.claimPath(raw)
		if cerr != nil {
			return nil, nil, nil, cerr
		}
		info, serr := os.Stat(p)
		if serr != nil && !os.IsNotExist(serr) {
			// A stat that failed for a reason other than absence is a real
			// problem — a permission error on a path that may well exist —
			// so it is named and skipped rather than claimed blind.
			warnings = append(warnings, fmt.Sprintf("%s skipped: %v", p, serr))
			continue
		}
		// The directory operand rule (§11, §3): a claimed directory is itself
		// a set entry — so rmdir <dir> is one claimCheck on the dir — and its
		// subtree is claimed too, as a snapshot: each regular file under it is
		// claimed now, and a file created afterwards is not. The walk does not
		// follow symlinks, and subdirectories are walked but not claimed. A
		// path not on disk has no subtree yet: it is claimed on its own and
		// the walk waits for it to exist.
		keep(p)
		if serr == nil && info.IsDir() {
			filepath.WalkDir(p, func(sub string, d fs.DirEntry, err error) error {
				if err != nil || sub == p {
					return nil
				}
				if d.Type().IsRegular() {
					keep(sub)
				}
				return nil
			})
		}
	}
	if add {
		claims = g.addClaims(author, kept)
	} else {
		g.setClaims(author, kept)
		claims = g.claimSet(author)
	}
	return claims, warnings, g.claimOverlaps(author, claims), nil
}

// claimPath canonicalises a claim operand the way every other verb spells a
// path: a relative name is made absolute against the workspace root, cleaned,
// and checked in-root. It deliberately does not go through Host.Resolve, which
// loads or opens a buffer; a claim is a declaration about a file, not a request
// to read it.
func (g *Guard) claimPath(path string) (string, error) {
	if path == "" {
		return "", errors.New("claim needs a path")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(g.Host.Root(), path)
	}
	path = filepath.Clean(path)
	if err := g.inRootResolved(path); err != nil {
		return "", err
	}
	return path, nil
}

// claimSet returns an author's set in stable order. It takes the lock, so it
// is also the read side the add and set helpers below defer to.
func (g *Guard) claimSet(author uint8) []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return sortedPaths(g.claims[author])
}

func (g *Guard) setClaims(author uint8, paths []string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.claims == nil {
		g.claims = map[uint8]map[string]bool{}
	}
	set := make(map[string]bool, len(paths))
	for _, p := range paths {
		set[p] = true
	}
	g.claims[author] = set
}

func (g *Guard) addClaims(author uint8, paths []string) []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.claims == nil {
		g.claims = map[uint8]map[string]bool{}
	}
	if g.claims[author] == nil {
		g.claims[author] = map[string]bool{}
	}
	for _, p := range paths {
		g.claims[author][p] = true
	}
	return sortedPaths(g.claims[author])
}

func (g *Guard) clearClaims(author uint8) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.claims, author)
}

// renameClaim replaces old with new in author's working set, after a rename
// the host reported as done. Putting new in and old out is the point: the set
// is a record of what the caller is editing, and after the rename the file it
// names is new. A set that does not hold old is left alone rather than having a
// claim invented for it, and a failed rename never reaches here, so the set is
// untouched on every failure path.
func (g *Guard) renameClaim(author uint8, old, new string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	set := g.claims[author]
	if set == nil || !set[old] {
		return
	}
	delete(set, old)
	set[new] = true
}

// claimOverlaps names the other authors holding any of paths. Names are
// resolved from the registry after the set is snapshotted under the lock, so a
// registry lookup never nests inside it; the display name is preferred over the
// identity key, which is a random token for a registered harness.
func (g *Guard) claimOverlaps(author uint8, paths []string) []ClaimOverlap {
	if len(paths) == 0 {
		return nil
	}
	g.mu.Lock()
	var out []ClaimOverlap
	for other, set := range g.claims {
		if other == author {
			continue
		}
		for _, p := range paths {
			if set[p] {
				out = append(out, ClaimOverlap{Path: p, Author: other})
			}
		}
	}
	g.mu.Unlock()
	if g.Participants != nil {
		for i := range out {
			if p, ok := g.Participants.Get(out[i].Author); ok {
				// Prefer the display name when one is set: a registered key is a
				// random token, and a person reading an overlap wants the name
				// the harness introduced itself under.
				if p.Name != "" {
					out[i].Identity = p.Name
				} else {
					out[i].Identity = p.Identity
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Author < out[j].Author
	})
	return out
}

// sortedPaths flattens a claim set into the stable order the wire reports.
func sortedPaths(set map[string]bool) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// readPaths answers a read that names several targets in one call. Each path is
// read exactly as a single-target read is, and every target is marked read only
// once the whole call has succeeded, so the read-before-write gate is satisfied
// for all of them together or none. The results share the response's body
// rather than a new shape: Buffers carries each target's path and version and
// the byte length of the text it contributed, and
// Spans carries the concatenated text in request order. Both are shapes a
// buffers reply and a single read already use, so no new wire field is needed
// and an older peer that does not know the multi-target form reads the
// concatenated text as it would have read a single target's.
//
// StatesJSON, when annotated, is the state runs of every target with their
// offsets rebased onto the concatenation.
func readPaths(g *Guard, req Request, start, end, lineStart, lineEnd int) Response {
	res := Response{OK: true}
	var states []StateRun
	var names []string
	off := 0
	for _, p := range req.Paths {
		name, err := g.canonical(p)
		if err != nil {
			return Response{Err: err.Error()}
		}
		spans, st, v, err := g.Host.Read(name, req.Author, start, end, lineStart, lineEnd, req.Annotated)
		if err != nil {
			// Name the target: a shared byte span can be out of range for one
			// target and fine for the others, and resolveSpan refuses rather
			// than clamps (a clamp across concurrent writers is silent
			// corruption), so the caller needs to know which file to fix.
			return Response{Err: name + ": " + err.Error()}
		}
		// A target's byte length and line count are the text it contributed,
		// which for a whole-file read is the file's own size and lines. The
		// byte length is also what splits the concatenated body back into
		// files, so it stays the contributed length. The line count follows
		// view.Index: one per newline plus the final (possibly empty) line.
		n, lines := 0, 1
		for _, sp := range spans {
			n += len(sp.Text)
			lines += strings.Count(sp.Text, "\n")
		}
		for _, r := range st {
			r.Off += off
			states = append(states, r)
		}
		res.Spans = append(res.Spans, spans...)
		// Carry the buffer's deleted fact along with its size, so a read of a
		// file that is gone says so rather than handing back text as though
		// the file were still there.
		nb, _ := g.readBuffer(name)
		res.Buffers = append(res.Buffers, Buffer{Path: name, Version: v, Bytes: n, Lines: lines, Deleted: nb.Deleted})
		names = append(names, name)
		off += n
	}
	// Mark every target read only now: a failure partway through returned
	// without marking anything, so an apply to an earlier target is still
	// refused as blind rather than satisfied by text the caller never got.
	for _, name := range names {
		g.markRead(req.Author, name)
	}
	if len(states) > 0 {
		data, err := json.Marshal(states)
		if err != nil {
			return Response{Err: err.Error()}
		}
		res.StatesJSON = string(data)
	}
	return res
}

// errIntentPublishNotHuman is the refusal for an agent's intent publish
// --approve. Proposing a publish is inert (it pins a step and runs a dry run),
// so an agent may prepare one; running it moves a ref or a remote and is the
// user's decision (H5).
var errIntentPublishNotHuman = "intent publish --approve is the outward step and is the user's decision: an agent may propose and export, not run a publish"

// dispatchIntent routes the intention op through its explicit admission policy.
// The owner is stamped from the connection on the server, so a payload that
// names another writer cannot make the intention theirs. new/add/remove/list/
// show and export are agent-allowed -- object-writing changes nothing outward
// and preparing an export for review is the point -- while publish --approve
// is the outward step and stays human-only; proposing a publish is inert and
// allowed (H5). No intent mode is ungated.
func dispatchIntent(g *Guard, req Request) Response {
	var cmd intent.Command
	if err := json.Unmarshal([]byte(req.HookJSON), &cmd); err != nil {
		return Response{Err: "intent: " + err.Error()}
	}
	if cmd.Mode == "publish" && cmd.Approve && !g.IsHuman(req.Author) {
		return Response{Err: errIntentPublishNotHuman}
	}
	cmd.Owner = fmt.Sprintf("%d", req.Author)
	payload, err := json.Marshal(cmd)
	if err != nil {
		return Response{Err: "intent: " + err.Error()}
	}
	out, err := g.Host.Intent(string(payload))
	if err != nil {
		return Response{Err: err.Error()}
	}
	return Response{OK: true, HookJSON: out}
}

// Dispatch turns one decoded request into a response. It is the only place the
// verbs are interpreted, so the socket adapter and an in-process caller cannot
// diverge about what an op means.
//
// The verbs themselves live in verbs.go, one row each; this looks the name up
// and runs the row's handler. A name the registry does not know — or one
// answered before Dispatch, whose row carries no handler — is refused here.
// That is the answer the old string switch gave for a name that fell off its
// end, but the set of names is the registry's now, so there is no second list
// to drift from it.
func Dispatch(g *Guard, req Request) Response {
	v, ok := verbByName[req.Op]
	if !ok || v.handle == nil {
		return Response{Err: "unknown op " + req.Op}
	}
	return v.handle(g, req)
}

// dispatchHookPrep answers the "hookprep" verb.
func dispatchHookPrep(g *Guard, req Request) Response {
	// Internal: connection.runHook asks the event thread to admit a hook
	// and hand back its command, projection and root, then runs the
	// command off the thread. The wire form is the "hook" op with mode
	// run; this op is the in-process half and has no wire representation.
	return dispatchHook(g, Request{HookMode: "run", HookName: req.HookName, HookParams: req.HookParams, Author: req.Author})
}

// dispatchPing answers the "ping" verb.
func dispatchPing(g *Guard, req Request) Response {
	return Response{OK: true, Root: g.Root(), Roots: g.roots()}
}

// dispatchBuffers answers the "buffers" verb.
func dispatchBuffers(g *Guard, req Request) Response {
	return Response{OK: true, Root: g.Root(), Roots: g.roots(), Buffers: g.Buffers()}
}

// dispatchOpen answers the "open" verb.
func dispatchOpen(g *Guard, req Request) Response {
	if req.Path == "" {
		return Response{Err: "open needs a path"}
	}
	v, created, err := g.Open(req.Path, req.Create)
	if err != nil {
		return done(v, err)
	}
	res := done(v, nil)
	res.Created = created
	if req.Create {
		// A create is the declaration of intent the spec names: extend
		// the caller's claim with the file it just made, in the same
		// canonical spelling claim stores, so no second command is
		// needed. Canonicalisation cannot normally fail here — Open
		// already checked the same path in-root — but if it does, the
		// open still stands and the claim simply did not extend; that
		// is a warning, not a failed open.
		name, cerr := g.claimPath(req.Path)
		if cerr != nil {
			return Response{OK: true, Version: v, Created: created,
				ClaimWarnings: []string{"claim not extended: " + cerr.Error()}}
		}
		g.addClaims(req.Author, []string{name})
	}
	return res
}

// dispatchMkdir answers the "mkdir" verb.
func dispatchMkdir(g *Guard, req Request) Response {
	if err := g.Mkdir(req.Path); err != nil {
		return Response{Err: err.Error()}
	}
	return Response{OK: true}
}

// dispatchRename answers the "rename" verb.
func dispatchRename(g *Guard, req Request) Response {
	if err := g.Rename(req.Path, req.NewPath, req.Author); err != nil {
		return Response{Err: err.Error()}
	}
	return Response{OK: true}
}

// dispatchGoto answers the "goto" verb.
func dispatchGoto(g *Guard, req Request) Response {
	name, err := g.canonical(req.Path)
	if err != nil {
		return Response{Err: err.Error()}
	}
	if err := g.Goto(name, req.Line, req.Col); err != nil {
		return Response{Err: err.Error()}
	}
	return Response{OK: true}
}

// dispatchReveal answers the "reveal" verb.
func dispatchReveal(g *Guard, req Request) Response {
	start, end := -1, -1
	if req.Start != nil {
		start = *req.Start
	}
	if req.End != nil {
		end = *req.End
	}
	if err := g.Reveal(req.Path, start, end); err != nil {
		return Response{Err: err.Error()}
	}
	return Response{OK: true}
}

// dispatchClose answers the "close" verb.
func dispatchClose(g *Guard, req Request) Response {
	if req.Discard {
		remains, err := g.Discard(req.Path)
		if err != nil {
			return Response{Err: err.Error()}
		}
		return Response{OK: true, Remains: remains}
	}
	name, err := g.canonical(req.Path)
	if err != nil {
		return Response{Err: err.Error()}
	}
	if err := g.Close(name); err != nil {
		return Response{Err: err.Error()}
	}
	return Response{OK: true}
}

// dispatchFind answers the "find" verb.
func dispatchFind(g *Guard, req Request) Response {
	// Find is a read: it locates a pattern in one buffer and reports its
	// byte span, so a program can find a position, read the version and
	// apply against it without a separate search round trip. The query is
	// validated like a search's, and the matching is the editor's own.
	if req.Query == nil {
		return Response{Err: "find needs a query"}
	}
	if err := g.CheckQuery(*req.Query); err != nil {
		return Response{Err: err.Error()}
	}
	name, err := g.canonical(req.Path)
	if err != nil {
		return Response{Err: err.Error()}
	}
	m, count, found, err := g.Host.Find(name, req.Author, *req.Query)
	if err != nil {
		return Response{Err: err.Error()}
	}
	// Found is sparse and the span reads from the omitted-zero defaults,
	// so a match at offset 0 costs no field. Version is always carried: a
	// caller that found nothing still learns the revision it looked at.
	return Response{OK: true, Found: found, FindStart: m.ByteStart,
		FindEnd: m.ByteEnd, FindCount: count, Version: m.Version}
}

// dispatchScreen answers the "screen" verb.
func dispatchScreen(g *Guard, req Request) Response {
	// The drawn screen is a read-only view: the visible rows top to
	// bottom and the caret's 1-based cell. The rows ride in the body as
	// one Spans run, the same as text, and the cursor reuses the goto
	// line/col fields, so seeing the UI spends no new header field.
	rows, line, col := g.Host.Screen()
	res := Response{OK: true, Line: line, Col: col}
	if len(rows) > 0 {
		res.Spans = []Span{{Text: strings.Join(rows, "\n")}}
	}
	return res
}

// dispatchText answers the "text" verb.
func dispatchText(g *Guard, req Request) Response {

	start, end, lineStart, lineEnd := -1, -1, 0, 0
	if req.Start != nil {
		start = *req.Start
	}
	if req.End != nil {
		end = *req.End
	}
	if req.LineStart != nil {
		lineStart = *req.LineStart
	}
	if req.LineEnd != nil {
		lineEnd = *req.LineEnd
	}
	if len(req.Paths) > 0 {
		return readPaths(g, req, start, end, lineStart, lineEnd)
	}
	spans, states, v, err := g.Read(req.Path, req.Author, start, end, lineStart, lineEnd, req.Annotated)
	if err != nil {
		return Response{Err: err.Error()}
	}
	res := Response{OK: true, Spans: spans, Version: v}
	// The whole file's size, not the returned span's: a driver that read
	// the text needs the file length to append, and carrying it here is
	// what removes the second version call. The numbers come from the
	// buffer list exactly as the version verb reads them, and a file that is
	// gone adds its record so the reply says so.
	if b, ok := g.readBuffer(req.Path); ok {
		res.Bytes, res.Lines = b.Bytes, b.Lines
		// Only a deleted file adds a record: the ordinary single read keeps
		// the shape it always had, and this one record is what lets a reader
		// with no screen know the path it just read is gone from disk.
		if b.Deleted {
			res.Buffers = []Buffer{b}
		}
	}
	if len(states) > 0 {
		data, err := json.Marshal(states)
		if err != nil {
			return Response{Err: err.Error()}
		}
		res.StatesJSON = string(data)
	}
	return res
}

// dispatchGroups answers the "groups" verb.
func dispatchGroups(g *Guard, req Request) Response {
	name, err := g.canonical(req.Path)
	if err != nil {
		return Response{Err: err.Error()}
	}
	groups, err := g.Host.Groups(name)
	if err != nil {
		return Response{Err: err.Error()}
	}
	return Response{OK: true, Groups: groups}
}

// dispatchDiff answers the "diff" verb.
func dispatchDiff(g *Guard, req Request) Response {
	diffs, err := g.Diff(req.Path, req.Author)
	if err != nil {
		return Response{Err: err.Error()}
	}
	if diffs == nil {
		diffs = []DiffGroup{}
	}
	data, err := json.Marshal(diffs)
	if err != nil {
		return Response{Err: err.Error()}
	}
	return Response{OK: true, DiffJSON: string(data)}
}

// dispatchReview answers the "review" verb.
func dispatchReview(g *Guard, req Request) Response {
	groups, err := g.Review(req.Path, req.ReviewList)
	if err != nil {
		return Response{Err: err.Error()}
	}
	if groups == nil {
		groups = []Group{}
	}
	return Response{OK: true, Groups: groups}
}

// dispatchDecide answers the "accept" verb.
func dispatchDecide(g *Guard, req Request) Response {
	// Accepting lands an agent's proposal, which is the person's decision,
	// not the writer's, so only the human the save gate admits may accept.
	// Reject leaves the text pending and stays open to an agent, recorded
	// under the connection's own author by the chokepoint in connection.one.
	if req.Op == "accept" && !g.humanAuthor(req.Author) {
		return Response{Err: errAcceptNotHuman}
	}
	name, err := g.canonical(req.Path)
	if err != nil {
		return Response{Err: err.Error()}
	}
	if req.Group == 0 {
		return Response{Err: req.Op + " needs a group id; list them with `groups`"}
	}
	if err := g.Host.Decide(name, req.Group, req.Op == "accept"); err != nil {
		return Response{Err: err.Error()}
	}
	return Response{OK: true}
}

// dispatchClear answers the "clear" verb.
func dispatchClear(g *Guard, req Request) Response {
	if req.Group == 0 {
		return Response{Err: "clear needs a group id; list them with `groups`"}
	}
	if err := g.Clear(req.Path, req.Author, req.Group); err != nil {
		// A wedged clear names the live set that blocked it, in the same
		// shape a lease refusal uses, so the caller can re-propose instead
		// of only learning that it failed.
		var be *BlockError
		if errors.As(err, &be) {
			return Response{Err: err.Error(), Conflicts: []Conflict{be.Conflict}}
		}
		return Response{Err: err.Error()}
	}
	return Response{OK: true}
}

// dispatchRevert answers the "revert" verb.
func dispatchRevert(g *Guard, req Request) Response {
	if err := g.Revert(req.Path, req.Author); err != nil {
		// A wedged revert names the live set that blocked it, in the same
		// shape a lease refusal uses, so the caller learns what to clear
		// first instead of retrying blind.
		var be *BlockError
		if errors.As(err, &be) {
			return Response{Err: err.Error(), Conflicts: []Conflict{be.Conflict}}
		}
		return Response{Err: err.Error()}
	}
	return Response{OK: true}
}

// dispatchDelete answers the "delete" verb.
func dispatchDelete(g *Guard, req Request) Response {
	if req.Withdraw && req.Approve {
		return Response{Err: errApproveWithdraw}
	}
	if req.Approve {
		if err := g.ApproveDeletion(req.Path, req.Author); err != nil {
			return Response{Err: err.Error()}
		}
		return Response{OK: true}
	}
	if err := g.Delete(req.Path, req.Author, req.Withdraw); err != nil {
		return Response{Err: err.Error()}
	}
	return Response{OK: true}
}

// dispatchDeletions answers the "deletions" verb.
func dispatchDeletions(g *Guard, req Request) Response {
	return Response{OK: true, Deletions: g.Deletions()}
}

// dispatchRmdir answers the "rmdir" verb.
func dispatchRmdir(g *Guard, req Request) Response {
	if req.Withdraw && req.Approve {
		return Response{Err: errApproveWithdraw}
	}
	if req.Approve {
		if err := g.ApproveDirRemoval(req.Path, req.Author); err != nil {
			return Response{Err: err.Error()}
		}
		return Response{OK: true}
	}
	if err := g.Rmdir(req.Path, req.Author, req.Withdraw); err != nil {
		return Response{Err: err.Error()}
	}
	return Response{OK: true}
}

// dispatchRmdirs answers the "rmdirs" verb.
func dispatchRmdirs(g *Guard, req Request) Response {
	return Response{OK: true, DirRemovals: g.Rmdirs()}
}

// dispatchLs answers the "ls" verb.
func dispatchLs(g *Guard, req Request) Response {
	entries, err := g.Ls(req.Path, req.Hidden)
	if err != nil {
		return Response{Err: err.Error()}
	}
	return Response{OK: true, Entries: entries}
}

// dispatchProposals answers the "proposals" verb.
func dispatchProposals(g *Guard, req Request) Response {
	return Response{OK: true, Proposals: g.Proposals()}
}

// dispatchClaim answers the "claim" verb.
func dispatchClaim(g *Guard, req Request) Response {
	claims, warnings, overlaps, err := g.Claim(req.Author, req.Paths, req.ClaimAdd, req.ClaimClear)
	if err != nil {
		return Response{Err: err.Error()}
	}
	return Response{OK: true, Claims: claims, ClaimWarnings: warnings, ClaimOverlaps: overlaps}
}

// dispatchStats answers the "stats" verb.
func dispatchStats(g *Guard, req Request) Response {
	return Response{OK: true, Stats: g.Stats()}
}

// dispatchExecCheck answers the "execcheck" verb.
func dispatchExecCheck(g *Guard, req Request) Response {
	// Internal: the socket asks on the event thread, then runs the command
	// off it. Splitting the check from the run is what lets a long command
	// be cancelled without the editor waiting on it.
	if req.ExecProjected && req.Dir != "" {
		return Response{Err: "exec --projected runs at the projected tree root; --dir is not supported"}
	}
	dir := req.Dir
	if req.ExecProjected {
		// The run directory is the scratch tree the runner builds, not a
		// workspace directory, so the -dir check has nothing to validate.
		dir = ""
	}
	dirty, err := g.CheckExec(req.Argv, dir)
	if err != nil {
		return Response{Err: err.Error(), Stats: g.Stats()}
	}
	// Stale buffers ride along with the go-ahead, so the runner can attach
	// them to the result rather than the caller having to ask separately.
	res := Response{OK: true, Dirty: dirty}
	if req.ExecProjected {
		// The projection and the root ride in-process to the connection
		// that materialises them; neither is wire-encoded.
		// Deliberately the display composition: exec --projected runs against the
		// tree the editor shows, deferral included. The verification surface is
		// the hook path, whose overlay and revision use ProjectionVerifying.
		res.Projection = g.Projection(ProjectionWithProposed)
		res.Root = g.Root()
		if p, ok := firstOutsideRoot(res.Projection, res.Root); ok {
			return Response{Err: fmt.Sprintf("exec --projected materialises the primary root %s only; %s is under another workspace root, and multi-root projected exec is not supported", res.Root, p)}
		}
	}
	return res
}

// dispatchSnapshot answers the "snapshot" verb.
func dispatchSnapshot(g *Guard, req Request) Response {
	// The document a client renders itself from. The payload travels as a
	// JSON string like diff and lsp, because the wire's one text field is
	// how a structured answer already crosses.
	encoded, version, encJSON, name, err := g.DocSnapshot(req.Path)
	if err != nil {
		return Response{Err: err.Error()}
	}
	res := Response{OK: true, Version: version, SnapshotPath: name,
		SnapshotJSON: string(encoded), EncodingJSON: string(encJSON)}
	// A snapshot of a buffer whose file is gone carries its record, so a client
	// that renders the snapshot directly knows to mark the tab. Sparse: an
	// ordinary snapshot adds nothing.
	if b, ok := g.readBuffer(name); ok && b.Deleted {
		res.Buffers = []Buffer{b}
	}
	return res
}

// dispatchSearchSnapshot answers the "searchsnapshot" verb.
func dispatchSearchSnapshot(g *Guard, req Request) Response {
	// Internal: the socket's search path asks for this on the event thread
	// and then walks off it. It has no wire representation.
	return Response{OK: true, Searcher: g.Snapshot()}
}

// dispatchLSPPrep answers the "lspprep" verb.
func dispatchLSPPrep(g *Guard, req Request) Response {
	// Internal: the lsp path asks the event thread to sync the document and
	// locate the server, then runs the request off it. No wire
	// representation, like snapshot.
	// The guard gates the path before the host loads it for the server:
	// without this, lsp was the one verb that reached the filesystem
	// without the resolved-root check, and a diagnostics request could
	// read through a symlink escape.
	name, cerr := g.canonical(req.Path)
	if cerr != nil {
		return Response{Err: cerr.Error()}
	}
	var caller LSPCaller
	var err error
	if req.LSPMode == "inlay-hints" {
		// A range request: LineStart/LineEnd carry the 1-based inclusive
		// lines, zero meaning the start or the end of the file. The
		// position fields are not used.
		lineStart, lineEnd := 0, 0
		if req.LineStart != nil {
			lineStart = *req.LineStart
		}
		if req.LineEnd != nil {
			lineEnd = *req.LineEnd
		}
		caller, err = g.Host.LSPInlayHints(name, lineStart, lineEnd)
	} else if req.LSPMode == "format" || req.LSPMode == "range-format" {
		// Formatting shares the range encoding with inlay-hints: both zero
		// means the whole document, and any lines named mean a range over
		// them. The capability gate and the FormattingOptions live in the
		// host, which is also where the buffer's indent style is read.
		lineStart, lineEnd := 0, 0
		if req.LineStart != nil {
			lineStart = *req.LineStart
		}
		if req.LineEnd != nil {
			lineEnd = *req.LineEnd
		}
		caller, err = g.Host.LSPFormat(name, lineStart, lineEnd)
	} else if req.LSPMode == "symbols" {
		// The query rides the request's generic Query field rather than a
		// field of its own: the wire already carries a run of query text,
		// and no other part of an lsp request uses it. The position is not
		// sent — workspace/symbol is not asked about a place — but the CLI
		// still requires one for the lsp verb's shape.
		query := ""
		if req.Query != nil {
			query = req.Query.Text
		}
		caller, err = g.Host.LSPWorkspaceSymbols(name, query)
	} else {
		caller, err = g.Host.LSP(name, req.Line, req.Col, req.LSPMode)
	}
	if err != nil {
		return Response{Err: err.Error()}
	}
	return Response{OK: true, LSP: caller}
}

// dispatchVersion answers the "version" verb.
func dispatchVersion(g *Guard, req Request) Response {
	name, err := g.canonical(req.Path)
	if err != nil {
		return Response{Err: err.Error()}
	}
	v, err := g.Version(name, req.Author)
	if err != nil {
		return Response{Err: err.Error()}
	}
	// Bytes and lines ride on the same answer so a driver can size a span
	// or find the end of a file without a read. They come from the buffer
	// list rather than a new host method, so the interface stays as is.
	res := Response{OK: true, Version: v}
	for _, b := range g.Buffers() {
		if b.Path == name {
			res.Bytes, res.Lines = b.Bytes, b.Lines
			break
		}
	}
	return res
}

// dispatchSave answers the "save" verb.
func dispatchSave(g *Guard, req Request) Response {
	v, err := g.Save(req.Path, req.Author, req.Force)
	return done(v, err)
}

// dispatchLand answers the "land" verb.
func dispatchLand(g *Guard, req Request) Response {
	// Landing accepts by task and then saves, so it is the same human
	// gesture save is; an agent is refused before any set is touched.
	if !g.humanAuthor(req.Author) {
		return Response{Err: errLandNotHuman}
	}
	files, exp, err := g.Land(req.LandTask, req.Author)
	if err != nil {
		return Response{Err: err.Error()}
	}
	res := Response{OK: true, Land: files}
	if exp != nil {
		// The export rides as JSON in the generic payload field: the land
		// report has no field for the object ids, and a client that does
		// not ask for them still reads the per-buffer list.
		if data, err := json.Marshal(exp); err == nil {
			res.HookJSON = string(data)
		}
	}
	return res
}

// dispatchReload answers the "reload" verb.
func dispatchReload(g *Guard, req Request) Response {
	if err := g.Reload(req.Path); err != nil {
		return Response{Err: err.Error()}
	}
	return Response{OK: true}
}

// dispatchApply answers the "apply" verb.
func dispatchApply(g *Guard, req Request) Response {
	if req.Base == nil {
		return Response{Err: "apply needs a base version; read the buffer or ask for its version first"}
	}
	if len(req.Hunks) == 0 {
		v, err := g.Version(req.Path, req.Author)
		return done(v, err)
	}
	v, conflicts, warnings, err := g.apply(req.Path, req.Author, *req.Base, req.Hunks, g.taskOf(req.Author))
	if err != nil {
		return Response{Err: err.Error()}
	}
	res := Response{OK: len(conflicts) == 0, Version: v, Conflicts: conflicts,
		Warnings: warnings}
	if len(conflicts) > 0 {
		res.Err = fmt.Sprintf("%d of %d hunks could not be placed on the current version",
			len(conflicts), len(req.Hunks))
	}
	return res
}

// dispatchDump answers the "dump" verb.
func dispatchDump(g *Guard, req Request) Response {
	start, end := -1, -1
	if req.Start != nil {
		start = *req.Start
	}
	if req.End != nil {
		end = *req.End
	}
	id, v, text, hash, err := g.Dump(req.Path, start, end, req.Author)
	if err != nil {
		return Response{Err: err.Error()}
	}
	return Response{OK: true, DumpID: id, Version: v, Hash: hash,
		Spans: []Span{{Text: text, Author: req.Author}}}
}

// dispatchPatch answers the "patch" verb.
func dispatchPatch(g *Guard, req Request) Response {
	v, conflicts, warnings, err := g.patch(req.Path, req.Author, req.DumpID, req.PatchText, g.taskOf(req.Author))
	if err != nil {
		return Response{Err: err.Error()}
	}
	res := Response{OK: len(conflicts) == 0, Version: v, Conflicts: conflicts,
		Warnings: warnings}
	if len(conflicts) > 0 {
		res.Err = fmt.Sprintf("%d of the snapshot's changes could not be placed on the current version",
			len(conflicts))
	}
	return res
}

// hookMode is one row of the hook-mode table, the one place a hook mode is
// spelled and the one place its transport rule and its handler live. The names
// used to be repeated across four switches -- dispatchHook, the connection
// routing in connection.one, localOnly and remoteRefusal -- with nothing tying
// them together, so a mode could be added to one and not the others and reach
// the wrong transport or answer as "unknown mode". One row per mode ends that.
type hookMode struct {
	name      string
	local     bool
	remoteCtl bool
	dispatch  func(*Guard, Request) Response
	conn      func(*connection, Request, func(Response))
}

// hookModes is every hook mode the server knows. The event-thread handler
// answers the modes Dispatch serves; the connection handler answers the modes
// addressed on the connection goroutine before Dispatch. run is the one mode
// with both: the wire request routes to runHook, and hookprep reaches the
// event-thread half through dispatchHook. local is the TCP rule authoring and
// the host-process modes share; remoteCtl marks the ones whose refusal names
// the control message rather than authoring.
var hookModes = []hookMode{
	{name: "list", dispatch: dispatchHookList},
	{name: "show", dispatch: dispatchHookShow},
	{name: "put", local: true, dispatch: dispatchHookPut},
	{name: "rm", local: true, dispatch: dispatchHookRemove},
	{name: "enable", local: true, dispatch: dispatchHookEnable},
	{name: "disable", local: true, dispatch: dispatchHookDisable},
	{name: "run", dispatch: dispatchHookRun, conn: (*connection).runHook},
	{name: "log", conn: (*connection).hookLog},
	{name: "ps", conn: (*connection).hookPS},
	{name: "cancel", local: true, remoteCtl: true, conn: (*connection).hookCancel},
	{name: "off", local: true, remoteCtl: true, conn: (*connection).hookSwitch},
	{name: "on", local: true, remoteCtl: true, conn: (*connection).hookSwitch},
}

// hookModeByName is the lookup the dispatcher, the transport guards and the
// connection router all share, so none of them can drift about what a mode is.
var hookModeByName = func() map[string]hookMode {
	m := make(map[string]hookMode, len(hookModes))
	for _, h := range hookModes {
		m[h.name] = h
	}
	return m
}()

// dispatchHook answers the hook verb. It is a lookup: the mode names, their
// transport rules and their handlers live once, in hookModes. list and show
// read the stored rows, put validates and stores one, and rm, enable and
// disable author. run is the event-thread half of `raj hook run`: it admits
// the hook, snapshots the projection, and reserves the run on the shared Gate.
// Authoring is local-only: localOnly refuses put/rm/enable/disable on a TCP
// connection before Dispatch is reached, while list, show and run cross; run's
// own boundary is admission, not the transport. Put validates through the
// hooks domain so an invalid row is refused before the host is asked to store
// it.
func dispatchHook(g *Guard, req Request) Response {
	m, ok := hookModeByName[req.HookMode]
	if !ok || m.dispatch == nil {
		return Response{Err: "hook: unknown mode " + req.HookMode}
	}
	return m.dispatch(g, req)
}

// dispatchHookList answers hook list: every stored row as JSON, an empty array
// rather than null when there are none.
func dispatchHookList(g *Guard, req Request) Response {
	rows, err := g.Hooks()
	if err != nil {
		return Response{Err: err.Error()}
	}
	if rows == nil {
		rows = []HookRow{}
	}
	data, err := json.Marshal(rows)
	if err != nil {
		return Response{Err: err.Error()}
	}
	return Response{OK: true, HookJSON: string(data)}
}

// dispatchHookShow answers hook show: the named row, or a refusal naming it.
func dispatchHookShow(g *Guard, req Request) Response {
	if req.HookName == "" {
		return Response{Err: "hook show needs a name"}
	}
	rows, err := g.Hooks()
	if err != nil {
		return Response{Err: err.Error()}
	}
	for _, row := range rows {
		if row.Name != req.HookName {
			continue
		}
		data, err := json.Marshal(row)
		if err != nil {
			return Response{Err: err.Error()}
		}
		return Response{OK: true, HookJSON: string(data)}
	}
	return Response{Err: fmt.Sprintf("no such hook %q", req.HookName)}
}

// dispatchHookPut answers hook put: an invalid row is refused by the hooks
// domain before the host is asked to store it.
func dispatchHookPut(g *Guard, req Request) Response {
	var row HookRow
	if err := json.Unmarshal([]byte(req.HookJSON), &row); err != nil {
		return Response{Err: "hook put: " + err.Error()}
	}
	hook, err := hooks.Parse(hookRaw(row))
	if err != nil {
		return Response{Err: err.Error()}
	}
	// Parse owns the default: an omitted tree is stored as projected, so the
	// column never holds a value the runner would have to guess at.
	row.Tree = string(hook.Tree)
	if err := g.PutHook(row); err != nil {
		return Response{Err: err.Error()}
	}
	return Response{OK: true}
}

// dispatchHookRemove answers hook rm.
func dispatchHookRemove(g *Guard, req Request) Response {
	if req.HookName == "" {
		return Response{Err: "hook rm needs a name"}
	}
	if err := g.DeleteHook(req.HookName); err != nil {
		return Response{Err: err.Error()}
	}
	return Response{OK: true}
}

// dispatchHookEnable and dispatchHookDisable answer hook enable and disable.
// The missing-name refusal names the mode through req.HookMode, the mode the
// table routed here.
func dispatchHookEnable(g *Guard, req Request) Response {
	return dispatchHookSetEnabled(g, req, true)
}

func dispatchHookDisable(g *Guard, req Request) Response {
	return dispatchHookSetEnabled(g, req, false)
}

func dispatchHookSetEnabled(g *Guard, req Request, on bool) Response {
	if req.HookName == "" {
		return Response{Err: "hook " + req.HookMode + " needs a name"}
	}
	if err := g.SetHookEnabled(req.HookName, on); err != nil {
		return Response{Err: err.Error()}
	}
	return Response{OK: true}
}

// dispatchHookRun answers hook run on the event thread: it admits the hook
// against the Agent flag, resolves declared parameters, refuses a workspace
// hook whose tree is not what the editor shows, and reserves the run on the
// shared Gate. The command runs on the connection goroutine; this half never
// spawns it.
func dispatchHookRun(g *Guard, req Request) Response {
	if req.HookName == "" {
		return Response{Err: "hook run needs a name"}
	}
	rows, err := g.Hooks()
	if err != nil {
		return Response{Err: err.Error()}
	}
	// Derived per request, never cached: a put or rm between two runs has
	// to be visible to the next one, and the store is the authority on what
	// a hook is.
	raws := make([]hooks.Raw, len(rows))
	for i, row := range rows {
		raws[i] = hookRaw(row)
	}
	set, _ := hooks.NewSet(raws)
	hook, err := set.Admit(req.HookName, g.hookCallerAgent(req.Author))
	if err != nil {
		return Response{Err: err.Error()}
	}
	// Declared parameters are resolved and validated here, on the event
	// thread, before the Gate reserves the run: a refusal must not consume a
	// run and the action must not start. The resolved values carry to the
	// connection as the RAJ_PARAM_<name> environment of section 4, never as
	// argv, so an agent names a choice rather than injecting a command.
	paramValues, err := hook.ResolveParams(req.HookParams)
	if err != nil {
		return Response{Err: err.Error()}
	}
	if g.HookGate == nil {
		return Response{Err: "hook run is not available"}
	}
	root := g.Root()
	// A workspace hook runs in the saved root itself, so it must not start
	// while any buffer is unsaved or holds a pending change set: the run would
	// test a tree that is not what the editor shows. The check is the same
	// predicate `raj ctl status` applies, decided here on the event thread. A
	// projected hook materialises the live composition instead and needs no
	// such gate.
	var proj map[string][]byte
	revision := uint64(0)
	if hook.Tree == hooks.TreeWorkspace {
		if blockers := hookWorkspaceBlockers(g.Buffers()); len(blockers) > 0 {
			return Response{Err: fmt.Sprintf("hook %q runs on the saved workspace, but it is not ready: %s",
				hook.Name, strings.Join(blockers, "; "))}
		}
		// The provenance stamp and the may_write check both need git, so the
		// work-tree refusal belongs here, before the command starts. The probe
		// is a filesystem check, not a git call: it runs on the event thread,
		// which must not spawn a process. A `.git` directory is an ordinary
		// checkout and a `.git` file is a linked worktree's gitdir pointer;
		// either is a work tree. The full `git view` still runs off-thread in
		// the run path.
		dotGit := filepath.Join(root, ".git")
		if info, err := os.Stat(dotGit); err != nil || (!info.IsDir() && !info.Mode().IsRegular()) {
			return Response{Err: fmt.Sprintf("hook %q runs on the saved workspace, but it is not a git work tree: %s", hook.Name, root)}
		}
		// When ready, the projection is the saved text; hashing it still keys
		// the per-revision cap on the composition the run saw.
		revision = projectionRevision(g.Projection(ProjectionVerifying))
	} else {
		// The build harness is taken from the accepted tree and only the
		// sources are projected, so a proposed Makefile or script cannot run:
		// see hookProjection.
		proj = g.hookProjection()
		// A projection that reaches a second workspace root cannot be
		// materialised from the primary one alone, exactly as for a projected
		// exec: the scratch tree would silently omit those buffers, so the run
		// is refused by name rather than run against a partial tree.
		if p, ok := firstOutsideRoot(proj, root); ok {
			return Response{Err: fmt.Sprintf("hook run materialises the primary root %s only; %s is under another workspace root, and multi-root projected hook runs are not supported", root, p)}
		}
		revision = projectionRevision(proj)
	}
	now := time.Now()
	retryAfter, ok, reason := g.HookGate.Allow(hook.Name, revision, now)
	if !ok {
		return Response{Err: fmt.Sprintf("hook %q: %s", hook.Name, reason),
			RetryAfterMS: int(retryAfter / time.Millisecond)}
	}
	// Begin on the event thread, so the very next admission -- this
	// connection or another -- sees the run in flight. End runs on the
	// connection goroutine once the command finishes.
	g.HookGate.Begin(hook.Name, revision, now)
	return Response{OK: true, Root: root, Projection: proj, HookRevision: revision,
		HookArgv: hook.Argv, HookShell: hook.Shell, HookTree: string(hook.Tree),
		HookMayWrite: hook.MayWrite, HookDetach: hook.Detach,
		HookTimeoutMS: int(hook.Timeout / time.Millisecond),
		HookBuiltin:   hook.Builtin, HookBuiltinArgs: hook.BuiltinArgs,
		HookSteps: hook.Steps, HookParamValues: paramValues}
}

// blockingBuffers returns the buffers that make a workspace unready for a gate:
// an unsaved buffer or one holding a pending change set. It is the one
// definition `raj ctl status` and workspace-hook admission both apply, so the
// client answer and the server refusal cannot drift.
func blockingBuffers(buffers []Buffer) []Buffer {
	var out []Buffer
	for _, b := range buffers {
		if b.Dirty || b.Pending > 0 {
			out = append(out, b)
		}
	}
	return out
}

// hookWorkspaceBlockers names the buffers blocking a workspace-tree hook, one
// per line and worded like the `raj ctl status` listing so the refusal and the
// status command point at the same fix. An empty result means the saved tree is
// exactly what the editor shows.
func hookWorkspaceBlockers(buffers []Buffer) []string {
	var out []string
	for _, b := range blockingBuffers(buffers) {
		name := b.Path
		if name == "" {
			name = "(unnamed buffer, not addressable)"
		}
		var reasons []string
		if b.Dirty {
			reasons = append(reasons, "dirty")
		}
		if b.Pending > 0 {
			reasons = append(reasons, fmt.Sprintf("%d pending", b.Pending))
		}
		out = append(out, name+": "+strings.Join(reasons, ", "))
	}
	return out
}

// hookCallerAgent reports whether author is an agent driver rather than the
// local human or a joined human. The local keyboard row is LocalHuman; a joined
// human is a durable participant of KindHuman, the same predicate writesAsHuman
// applies to a write. Everything else -- a durable agent, a provisional
// connection -- counts as an agent, because the hook Agent flag exists to gate
// exactly those drivers. Without a registry, the id rule still excludes the
// local human and treats every other writer as an agent.
func (g *Guard) hookCallerAgent(author uint8) bool {
	return author != LocalHuman && !g.writesAsHuman(author)
}

// hookRaw converts a control row to the hooks domain's raw input, so put's
// validation goes through the same Parse the host applies.
func hookRaw(row HookRow) hooks.Raw {
	return hooks.Raw{
		Name: row.Name, Action: row.Action, Params: row.Params, Trigger: row.Trigger, Tree: row.Tree, Agent: row.Agent,
		CooldownMS: row.CooldownMS, TimeoutMS: row.TimeoutMS,
		MayWrite: row.MayWrite, Detach: row.Detach, Enabled: row.Enabled,
	}
}

// dispatchGit answers the read-only git verb. Git runs host-side against the
// workspace root and moves no ref: status, diff, show, numstat and log only
// read. The service rides on the Guard so a test can inject a fake runner or a
// service aimed at a temp repository; a nil one builds a real service over the
// workspace root.
func dispatchGit(g *Guard, req Request) Response {
	svc := g.Git
	if svc == nil {
		svc = git.New(g.Root())
	}
	result, err := svc.Call(context.Background(), git.Query{
		Mode: req.GitMode, Path: req.Path, Rev: req.GitRev, Count: req.GitCount})
	if err != nil {
		return Response{Err: err.Error()}
	}
	data, err := json.Marshal(result)
	if err != nil {
		return Response{Err: err.Error()}
	}
	return Response{OK: true, GitJSON: string(data)}
}

func done(v uint64, err error) Response {
	if err != nil {
		return Response{Err: err.Error()}
	}
	return Response{OK: true, Version: v}
}

// DiffLines returns the byte hunks that turn old into new, line-granular but
// byte-precise. Each hunk replaces old[start:end] with text drawn from new.
//
// It is the editor-side half of patch: a driver returns whole edited text and
// the editor turns old→new into offsets it can rebase onto the current
// document, so the driver never re-derives an offset and never matches old
// text. Lines are the unit because that is where edits land, and whole-line
// hunks rebase cleanly across concurrent edits elsewhere in the file.
//
// The diff is patience-style: trim the common prefix and suffix, match the
// lines that occur exactly once on both sides, keep a longest run of those
// anchors whose order agrees, and recurse in the gaps between them. A region
// with no such anchor becomes one hunk. That is O((n+m)·log(n+m)) on
// realistic text. A per-region work budget (diffWorkCeiling, diffRegionCeiling)
// keeps an adversarial or duplicate-heavy file from paying that on every
// region: once it is spent the remaining regions each become one hunk, so the
// work is bounded without collapsing the whole file the way the old line LCS
// table did.
func DiffLines(oldText, newText string) []Hunk {
	return diffLines(oldText, newText, diffWorkCeiling)
}

// diffLines is DiffLines with the work ceiling as a parameter, so tests can
// drive the fallback path with a small budget instead of building a region
// large enough to exhaust the real one.
func diffLines(oldText, newText string, workCeiling int) []Hunk {
	if oldText == newText {
		return nil
	}
	aStarts := lineStarts(oldText)
	bStarts := lineStarts(newText)
	a := make([]string, len(aStarts))
	for i := range aStarts {
		a[i] = lineAt(oldText, aStarts, i)
	}
	b := make([]string, len(bStarts))
	for i := range bStarts {
		b[i] = lineAt(newText, bStarts, i)
	}
	var hunks []Hunk
	remaining := workCeiling
	diffRegion(a, b, aStarts, bStarts, oldText, newText, 0, len(a), 0, len(b), &remaining, &hunks)
	return hunks
}

// The ceilings below bound the diff's work. They are work limits, not
// correctness switches: a region that trips one is still diffed, as a single
// coarse hunk, so the editor can show a larger change than it otherwise would
// but never a wrong one. They sit far above any hand-edited file — a region
// must be tens of thousands of lines on both sides, or the whole diff about a
// million charged line-slots, before either bites — so realistic diffs keep
// their fine hunks.
const (
	// diffWorkCeiling is the total line-work one DiffLines call may spend on
	// anchor matching before every remaining region becomes one hunk.
	diffWorkCeiling = 1 << 20

	// diffRegionCeiling is the largest region, in lines on each side, still
	// worth splitting into anchors; a larger region becomes one hunk even when
	// budget remains.
	diffRegionCeiling = 1 << 15
)

// diffFallback charges region a[aLo:aHi] vs b[bLo:bHi] to the remaining work
// budget and reports whether it should be emitted as one coarse hunk instead
// of anchor-matched. Once the budget is spent it stays spent, so the recursion
// cannot revive deep work later in the file.
func diffFallback(remaining *int, aLo, aHi, bLo, bHi int) bool {
	*remaining -= (aHi - aLo) + (bHi - bLo)
	if *remaining < 0 {
		return true
	}
	return aHi-aLo > diffRegionCeiling && bHi-bLo > diffRegionCeiling
}

// diffRegion appends the hunks that turn a[aLo:aHi] into b[bLo:bHi]. Outside
// the region the caller has already accounted for everything: the lines before
// aLo are equal, and a[an.a] == b[an.b] at each anchor the caller chose.
// remaining is the work budget shared by the whole recursion; a region that
// exhausts it becomes one hunk instead of being anchor-matched.
func diffRegion(a, b []string, aStarts, bStarts []int, oldText, newText string, aLo, aHi, bLo, bHi int, remaining *int, out *[]Hunk) {
	for aLo < aHi && bLo < bHi && a[aLo] == b[bLo] {
		aLo++
		bLo++
	}
	for aHi > aLo && bHi > bLo && a[aHi-1] == b[bHi-1] {
		aHi--
		bHi--
	}
	if aLo == aHi && bLo == bHi {
		return
	}
	if aLo == aHi || bLo == bHi {
		*out = append(*out, diffHunk(aStarts, bStarts, oldText, newText, aLo, aHi, bLo, bHi))
		return
	}
	if diffFallback(remaining, aLo, aHi, bLo, bHi) {
		*out = append(*out, diffHunk(aStarts, bStarts, oldText, newText, aLo, aHi, bLo, bHi))
		return
	}
	anchors := uniqueAnchors(a, b, aLo, aHi, bLo, bHi)
	if len(anchors) == 0 {
		*out = append(*out, diffHunk(aStarts, bStarts, oldText, newText, aLo, aHi, bLo, bHi))
		return
	}
	aPrev, bPrev := aLo, bLo
	for _, an := range anchors {
		if aPrev < an.a || bPrev < an.b {
			diffRegion(a, b, aStarts, bStarts, oldText, newText, aPrev, an.a, bPrev, an.b, remaining, out)
		}
		aPrev, bPrev = an.a+1, an.b+1
	}
	if aPrev < aHi || bPrev < bHi {
		diffRegion(a, b, aStarts, bStarts, oldText, newText, aPrev, aHi, bPrev, bHi, remaining, out)
	}
}

// diffHunk builds the hunk that replaces a[aLo:aHi] with b[bLo:bHi]. An empty
// a span is an insertion, an empty b span a deletion.
func diffHunk(aStarts, bStarts []int, oldText, newText string, aLo, aHi, bLo, bHi int) Hunk {
	return Hunk{
		Start: at(aStarts, len(oldText), aLo),
		End:   at(aStarts, len(oldText), aHi),
		Text:  newText[at(bStarts, len(newText), bLo):at(bStarts, len(newText), bHi)],
	}
}

// diffAnchor is one line matched at a[i] and b[j]. The bytes at the two
// positions are equal; they are kept as indices so the anchor search handles
// positions, not the strings a second time.
type diffAnchor struct{ a, b int }

// uniqueAnchors pairs each line that occurs exactly once in both a[aLo:aHi]
// and b[bLo:bHi], then keeps a longest run of those pairs whose b indices
// increase. The paired line is unambiguous by construction, so matching it
// cannot mislead the way a repeated line can, and the LIS drops the crossings
// a greedy pairing would introduce.
func uniqueAnchors(a, b []string, aLo, aHi, bLo, bHi int) []diffAnchor {
	countA := make(map[string]int, aHi-aLo)
	for i := aLo; i < aHi; i++ {
		countA[a[i]]++
	}
	countB := make(map[string]int, bHi-bLo)
	for j := bLo; j < bHi; j++ {
		countB[b[j]]++
	}
	idxB := make(map[string]int, len(countB))
	for j := bLo; j < bHi; j++ {
		if countB[b[j]] == 1 {
			idxB[b[j]] = j
		}
	}
	as := make([]int, 0, len(idxB))
	bs := make([]int, 0, len(idxB))
	for i := aLo; i < aHi; i++ {
		if countA[a[i]] != 1 {
			continue
		}
		if j, ok := idxB[a[i]]; ok {
			as = append(as, i)
			bs = append(bs, j)
		}
	}
	if len(as) == 0 {
		return nil
	}
	return longestIncreasing(as, bs)
}

// longestIncreasing returns a longest strictly increasing subsequence of bs,
// carrying as alongside it. tails[k] is the index of the smallest tail of an
// increasing run of length k+1, and prev reconstructs the chosen run.
func longestIncreasing(as, bs []int) []diffAnchor {
	prev := make([]int, len(bs))
	tails := make([]int, 0, len(bs))
	for i, v := range bs {
		prev[i] = -1
		lo, hi := 0, len(tails)
		for lo < hi {
			mid := int(uint(lo+hi) >> 1)
			if bs[tails[mid]] < v {
				lo = mid + 1
			} else {
				hi = mid
			}
		}
		if lo > 0 {
			prev[i] = tails[lo-1]
		}
		if lo == len(tails) {
			tails = append(tails, i)
		} else {
			tails[lo] = i
		}
	}
	out := make([]diffAnchor, len(tails))
	for k, i := len(tails)-1, tails[len(tails)-1]; k >= 0; k-- {
		out[k] = diffAnchor{a: as[i], b: bs[i]}
		i = prev[i]
	}
	return out
}

// lineStarts returns the byte offset where each line begins. A trailing newline
// does not open an empty final line, so "a\n" is one line, not two.
func lineStarts(s string) []int {
	if s == "" {
		return nil
	}
	starts := []int{0}
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			starts = append(starts, i+1)
		}
	}
	if starts[len(starts)-1] == len(s) {
		starts = starts[:len(starts)-1]
	}
	return starts
}

// lineAt returns line i, from its start to the next line's start (or the end
// of the string), newline included.
func lineAt(s string, starts []int, i int) string {
	return s[starts[i]:at(starts, len(s), i+1)]
}

// at maps a line index to its byte offset, clamping a one-past-the-end index
// to the length of the underlying text — which is how a run that ends at the
// final line, or a pure insertion at the end, is expressed.
func at(starts []int, total, idx int) int {
	if idx < len(starts) {
		return starts[idx]
	}
	return total
}
