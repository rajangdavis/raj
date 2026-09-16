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
)

// BufferHost is the vocabulary, with no transport in it.
//
// docs/HARNESS-BROKER-AGENT.md puts the transport last and the interface second, for
// a reason worth restating: a socket is one adapter over this, an in-process
// call is another, and adding either must change nothing above this line. The
// tests use the in-process path, so what they exercise is the same code the
// socket reaches.
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

	// WithdrawDeletion removes author's pending deletion for path. A path
	// that is not pending is a no-op; one proposed by another writer is
	// refused, so an agent cannot retract a peer's proposal.
	WithdrawDeletion(path string, author uint8) error

	// Deletions lists the pending deletions.
	Deletions() []Deletion

	// ProposeDirRemoval records a pending dir-removal for path, proposed by
	// author, and removes nothing. It is the rmdir analogue of
	// ProposeDeletion: the user decides whether the subtree goes. Idempotent,
	// so a second proposal for the same path is a no-op.
	ProposeDirRemoval(path string, author uint8) error

	// WithdrawDirRemoval removes author's pending dir-removal for path. A
	// path that is not pending is a no-op; one proposed by another writer is
	// refused, so an agent cannot retract a peer's proposal.
	WithdrawDirRemoval(path string, author uint8) error

	// DirRemovals lists the pending dir-removals.
	DirRemovals() []DirRemoval

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
	// that moved a draft, and a refusal stay distinguishable.
	Apply(path string, author uint8, base uint64, hunks []Hunk) (version uint64, conflicts []Conflict, warnings []GroupOverlap, err error)

	// Save writes a buffer to disk.
	Save(path string) (uint64, error)

	// Groups lists the change sets in a buffer, oldest first.
	Groups(path string) ([]Group, error)

	// Decide accepts or rejects a change set. Both are pure state flips: a
	// rejection marks the set rejected and the text stays in the document,
	// dropping out of the agreed composition; it cannot fail or be wedged.
	// Clear is the operation that reverses a rejected set out.
	Decide(path string, group uint64, accept bool) error

	// Clear hard-purges a rejected change set: the set's ops are reversed out
	// of the document and the decision dropped, so the text and the mark both
	// leave the view. Unlike Decide this really edits, so it can fail; the
	// error names the group.
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
	Patch(path string, author uint8, id uint64, newText string) (version uint64, conflicts []Conflict, warnings []GroupOverlap, err error)

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
	// and save is deliberately not gated; the reads never consult it.
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
	root := filepath.Clean(g.Host.Root())
	clean := filepath.Clean(path)
	rel, err := filepath.Rel(root, clean)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%w: %s is not under %s", ErrOutsideoot, clean, root)
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
	root := filepath.Clean(g.Host.Root())
	if r, rerr := filepath.EvalSymlinks(root); rerr == nil {
		root = r
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%w: %s resolves to %s, outside %s", ErrOutsideoot, path, resolved, root)
	}
	return nil
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

// Delete proposes a pending deletion for a claimed path, or withdraws this
// identity's proposal. It is claim-gated like a text write — the path must be
// in the caller's claim set — because the claim is the record of what an agent
// declared it would touch. It deliberately does NOT require read-before-write:
// deleting is not a text write and no offset is at stake. The path is
// canonicalised by claimPath, which checks it in-root without loading a buffer,
// so proposing a deletion does not have to open the file it names.
func (g *Guard) Delete(path string, author uint8, withdraw bool) error {
	if path == "" {
		return errors.New("delete needs a path")
	}
	name, err := g.claimPath(path)
	if err != nil {
		return err
	}
	if err := g.claimCheck(author, name); err != nil {
		return err
	}
	if withdraw {
		return g.Host.WithdrawDeletion(name, author)
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

// Rmdir proposes a pending dir-removal for a claimed directory, or withdraws
// this identity's proposal. It is the rmdir analogue of Delete: the directory
// itself is a claim entry (claim <dir> records it, per §11), so the gate is one
// claimCheck on the dir. Like Delete it deliberately does NOT require
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
	if err := g.claimCheck(author, name); err != nil {
		return err
	}
	if withdraw {
		return g.Host.WithdrawDirRemoval(name, author)
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

// Ls lists a directory's immediate children. It is a read, ungated like
// Deletions, but the path still has to resolve inside the workspace: ls is not
// a way to read the filesystem outside the tree. An empty path names the
// workspace root, which is the verb's default. claimPath is the canonicaliser
// rather than canonical, because the latter goes through Host.Resolve and a
// directory is not a buffer to load; the same split Rmdir uses.
func (g *Guard) Ls(path string, all bool) ([]Entry, error) {
	if path == "" {
		path = g.Host.Root()
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

// Proposals rolls the three pending listings into one flat tagged list: each
// open buffer's pending change sets, the pending file deletions and the
// pending dir-removals. It is a read-only view, ungated like Deletions, and
// the order is deterministic — kind (set, delete, rmdir), then path, then
// group id — so a caller can compare two listings directly.
func (g *Guard) Proposals() []Proposal {
	out := g.Host.Proposals()
	rank := func(kind string) int {
		switch kind {
		case "set":
			return 0
		case "delete":
			return 1
		default:
			return 2
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

// Patch writes, so it runs the same author check as Apply: a socket may not
// write as the file-as-loaded nor as a person, or its text becomes
// indistinguishable from typed text. The offsets are implicit — the snapshot's
// span told the editor where the chunk lives — so there is no hunk span to
// validate here; the host refuses a snapshot this writer does not own.
func (g *Guard) Patch(path string, author uint8, id uint64, newText string) (uint64, []Conflict, []GroupOverlap, error) {
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
	return g.Host.Patch(name, author, id, newText)
}

func (g *Guard) Apply(path string, author uint8, base uint64, hunks []Hunk) (uint64, []Conflict, []GroupOverlap, error) {
	// A socket may not write as the file-as-loaded, and may not write as a
	// human — attribution is what the tint, the per-author undo stacks and the
	// exec staleness split all read, so a connection able to claim a person
	// would make its text indistinguishable from something typed.
	//
	// A registry lookup rather than `author >= 2`: once a second person can
	// edit the same workspace, the id number cannot say what kind of writer it
	// belongs to. Without a registry the old rule stands, so a Guard built by
	// hand still refuses 0 and 1.
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
	return g.Host.Apply(name, author, base, hunks)
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

func (g *Guard) Save(path string) (uint64, error) {
	name, err := g.canonical(path)
	if err != nil {
		return 0, err
	}
	return g.Host.Save(name)
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
// So: run it, report the stale files, count them. When layered proposals land,
// this becomes "materialise the accepted composition and run against that",
// and the count is what says how much that is worth.

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

// Dispatch turns one decoded request into a response. It is the only place the
// verbs are interpreted, so the socket adapter and an in-process caller cannot
// diverge about what an op means.
func Dispatch(g *Guard, req Request) Response {
	switch req.Op {
	case "ping":
		return Response{OK: true, Root: g.Root()}
	case "buffers":
		return Response{OK: true, Root: g.Root(), Buffers: g.Buffers()}
	case "open":
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
	case "mkdir":
		if err := g.Mkdir(req.Path); err != nil {
			return Response{Err: err.Error()}
		}
		return Response{OK: true}
	case "rename":
		if err := g.Rename(req.Path, req.NewPath, req.Author); err != nil {
			return Response{Err: err.Error()}
		}
		return Response{OK: true}
	case "goto":
		name, err := g.canonical(req.Path)
		if err != nil {
			return Response{Err: err.Error()}
		}
		if err := g.Goto(name, req.Line, req.Col); err != nil {
			return Response{Err: err.Error()}
		}
		return Response{OK: true}
	case "close":
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
	case "find":
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

	case "text":

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
		spans, states, v, err := g.Read(req.Path, req.Author, start, end, lineStart, lineEnd, req.Annotated)
		if err != nil {
			return Response{Err: err.Error()}
		}
		res := Response{OK: true, Spans: spans, Version: v}
		if len(states) > 0 {
			data, err := json.Marshal(states)
			if err != nil {
				return Response{Err: err.Error()}
			}
			res.StatesJSON = string(data)
		}
		return res
	case "groups":
		name, err := g.canonical(req.Path)
		if err != nil {
			return Response{Err: err.Error()}
		}
		groups, err := g.Host.Groups(name)
		if err != nil {
			return Response{Err: err.Error()}
		}
		return Response{OK: true, Groups: groups}
	case "diff":
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
	case "review":
		groups, err := g.Review(req.Path, req.ReviewList)
		if err != nil {
			return Response{Err: err.Error()}
		}
		if groups == nil {
			groups = []Group{}
		}
		return Response{OK: true, Groups: groups}
	case "accept", "reject":
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
	case "clear":
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
	case "revert":
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
	case "delete":
		if err := g.Delete(req.Path, req.Author, req.Withdraw); err != nil {
			return Response{Err: err.Error()}
		}
		return Response{OK: true}
	case "deletions":
		return Response{OK: true, Deletions: g.Deletions()}
	case "rmdir":
		if err := g.Rmdir(req.Path, req.Author, req.Withdraw); err != nil {
			return Response{Err: err.Error()}
		}
		return Response{OK: true}
	case "rmdirs":
		return Response{OK: true, DirRemovals: g.Rmdirs()}
	case "ls":
		entries, err := g.Ls(req.Path, req.Hidden)
		if err != nil {
			return Response{Err: err.Error()}
		}
		return Response{OK: true, Entries: entries}
	case "proposals":
		return Response{OK: true, Proposals: g.Proposals()}
	case "claim":
		claims, warnings, overlaps, err := g.Claim(req.Author, req.Paths, req.ClaimAdd, req.ClaimClear)
		if err != nil {
			return Response{Err: err.Error()}
		}
		return Response{OK: true, Claims: claims, ClaimWarnings: warnings, ClaimOverlaps: overlaps}
	case "stats":
		return Response{OK: true, Stats: g.Stats()}
	case "execcheck":
		// Internal: the socket asks on the event thread, then runs the command
		// off it. Splitting the check from the run is what lets a long command
		// be cancelled without the editor waiting on it.
		dirty, err := g.CheckExec(req.Argv, req.Dir)
		if err != nil {
			return Response{Err: err.Error(), Stats: g.Stats()}
		}
		// Stale buffers ride along with the go-ahead, so the runner can attach
		// them to the result rather than the caller having to ask separately.
		return Response{OK: true, Dirty: dirty}
	case "snapshot":
		// Internal: the socket's search path asks for this on the event thread
		// and then walks off it. It has no wire representation.
		return Response{OK: true, Searcher: g.Snapshot()}
	case "lspprep":
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
		} else {
			caller, err = g.Host.LSP(name, req.Line, req.Col, req.LSPMode)
		}
		if err != nil {
			return Response{Err: err.Error()}
		}
		return Response{OK: true, LSP: caller}
	case "version":
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
	case "save":
		v, err := g.Save(req.Path)
		return done(v, err)
	case "apply":
		if req.Base == nil {
			return Response{Err: "apply needs a base version; read the buffer or ask for its version first"}
		}
		if len(req.Hunks) == 0 {
			v, err := g.Version(req.Path, req.Author)
			return done(v, err)
		}
		v, conflicts, warnings, err := g.Apply(req.Path, req.Author, *req.Base, req.Hunks)
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
	case "dump":
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
	case "patch":
		v, conflicts, warnings, err := g.Patch(req.Path, req.Author, req.DumpID, req.PatchText)
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
	return Response{Err: "unknown op " + req.Op}
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
func DiffLines(oldText, newText string) []Hunk {
	if oldText == newText {
		return nil
	}
	aLines := lineStarts(oldText)
	bLines := lineStarts(newText)
	n, m := len(aLines), len(bLines)

	// A span-scoped chunk is bounded by construction, but guard the O(n·m) DP
	// against a pathological whole-file dump anyway: past the cap the answer
	// degrades to one coarse hunk, which is always correct if not minimal.
	if n*m > 1<<20 {
		return []Hunk{{Start: 0, End: len(oldText), Text: newText}}
	}

	// LCS length over lines, computed backwards so the forward walk below can
	// read dp[i+1][j] and dp[i][j+1] as "which side does the LCS skip".
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		ai := lineAt(oldText, aLines, i)
		for j := m - 1; j >= 0; j-- {
			switch {
			case ai == lineAt(newText, bLines, j):
				dp[i][j] = dp[i+1][j+1] + 1
			case dp[i+1][j] >= dp[i][j+1]:
				dp[i][j] = dp[i+1][j]
			default:
				dp[i][j] = dp[i][j+1]
			}
		}
	}

	// Walk the backtrace, delimited by matches, one hunk per maximal region of
	// unmatched lines. A match ends a region without itself advancing either
	// side; the outer loop consumes matches by moving i and j together.
	var hunks []Hunk
	i, j := 0, 0
	for i < n || j < m {
		if i < n && j < m && lineAt(oldText, aLines, i) == lineAt(newText, bLines, j) {
			i++
			j++
			continue
		}
		di, dj := i, j
		for i < n || j < m {
			if i < n && j < m && lineAt(oldText, aLines, i) == lineAt(newText, bLines, j) {
				break
			}
			if i < n && (j >= m || dp[i+1][j] >= dp[i][j+1]) {
				i++
			} else {
				j++
			}
		}
		hunks = append(hunks, Hunk{
			Start: at(aLines, len(oldText), di),
			End:   at(aLines, len(oldText), i),
			Text:  newText[at(bLines, len(newText), dj):at(bLines, len(newText), j)],
		})
	}
	return hunks
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
