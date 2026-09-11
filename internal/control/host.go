package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
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

	// Open puts a path in a tab and returns its version.
	Open(path string) (uint64, error)

	// Goto moves the cursor in a buffer to a 1-based line and column. Zero
	// means "the editor decides": a missing line keeps the cursor's own, a
	// missing column the margin.
	Goto(path string, line, col int) error

	// Close removes a buffer. One with unsaved changes is refused: a silent
	// close is a silent data loss, and the editor's close-anyway prompt has no
	// machine form.
	Close(path string) error

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
	// clipped at the boundaries.
	Read(path string, start, end, lineStart, lineEnd int) (spans []Span, version uint64, err error)

	// Version is what a later Apply bases on, without moving the bytes.
	Version(path string) (uint64, error)

	// Apply rebases hunks written against base onto the current document.
	// Hunks are rejected independently; the conflicts say which.
	Apply(path string, author uint8, base uint64, hunks []Hunk) (version uint64, conflicts []Conflict, err error)

	// Save writes a buffer to disk.
	Save(path string) (uint64, error)

	// Groups lists the change sets in a buffer, oldest first.
	Groups(path string) ([]Group, error)

	// Decide accepts or rejects a change set. Rejection is undo addressed by
	// group: the members are rebased through everything that landed after them
	// and rolled back together if any cannot be placed.
	Decide(path string, group uint64, accept bool) error

	// Diff renders the buffer's pending change sets as old→new hunks in
	// current coordinates: the review surface for what Groups only lists.
	// An empty result means no changes await a decision.
	Diff(path string) ([]DiffGroup, error)

	// Dump captures a snapshot of [start,end) at the buffer's current version,
	// returning its id, the version it was taken at, and the text. The id is
	// held per-author on the editor side, so a later Patch names it back; the
	// text is the whole chunk, so a driver edits it and returns it whole. Hash
	// is of the text, so a caller can verify the bytes it received.
	Dump(path string, start, end int, author uint8) (id uint64, version uint64, text string, hash string, err error)

	// Patch replaces a snapshot's text, diffing old against new on the editor
	// side and rebasing the result onto the current document. The id must have
	// been minted by this writer; one that has been evicted, belongs to another
	// author, or whose buffer has moved is refused rather than guessed at.
	Patch(path string, author uint8, id uint64, newText string) (version uint64, conflicts []Conflict, err error)

	// LSP prepares a language-server request for a 1-based line and column.
	// Mode is hover, definition, completion or diagnostics. The returned
	// caller runs the request off the event thread — see LSPCaller — and for
	// diagnostics it carries the last-known cached state rather than asking
	// the server, which is the mode that never blocks. A nil caller with a
	// non-nil error is a clean "no server" or "server not ready" answer.
	LSP(path string, line, col int, mode string) (LSPCaller, error)

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

var (
	ErrNoBuffer   = errors.New("no open buffer for that path")
	ErrOutsideoot = errors.New("path is outside the workspace")
)

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

	// read records which paths this connection has read, and at what version.
	// Read-before-write is the check that stops a blind edit: offsets are
	// meaningless except in the coordinates of a version somebody looked at,
	// and a caller that never read is submitting numbers it invented.
	mu    sync.Mutex
	read  map[string]bool
	stats ExecStats
}

func NewGuard(h BufferHost) *Guard { return &Guard{Host: h, read: map[string]bool{}} }

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

// canonical resolves and validates in one step, so no method can accidentally
// check one name and act on another.
func (g *Guard) canonical(path string) (string, error) {
	// Open is the exception: its path names a file that is not a buffer yet, so
	// there is nothing to resolve against.
	resolved, err := g.Host.Resolve(path)
	if err != nil {
		return "", err
	}
	if err := g.inRoot(resolved); err != nil {
		return "", err
	}
	return resolved, nil
}

func (g *Guard) Open(path string) (uint64, error) {
	// Open names a file that may not be a buffer yet, so unlike every other
	// verb there is no Resolve to canonicalise it. A relative path is made
	// absolute against the root here, the rule the host applies to every other
	// verb, so open accepts the same spelling as read; the root check below
	// then runs on the path it names rather than on how it was spelled.
	if path != "" && !filepath.IsAbs(path) {
		path = filepath.Join(g.Host.Root(), path)
	}
	if err := g.inRoot(path); err != nil {
		return 0, err
	}
	return g.Host.Open(path)
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

func (g *Guard) Read(path string, start, end, lineStart, lineEnd int) ([]Span, uint64, error) {
	name, err := g.canonical(path)
	if err != nil {
		return nil, 0, err
	}
	spans, v, err := g.Host.Read(name, start, end, lineStart, lineEnd)
	if err == nil {
		g.mu.Lock()
		g.read[name] = true
		g.mu.Unlock()
	}
	return spans, v, err
}

func (g *Guard) Version(path string) (uint64, error) {
	name, err := g.canonical(path)
	if err != nil {
		return 0, err
	}
	v, err := g.Host.Version(name)
	if err == nil {
		// Asking for a version is asking for coordinates, which is the thing
		// read-before-write is checking for. A caller that took the version
		// deliberately is not editing blind.
		g.mu.Lock()
		g.read[name] = true
		g.mu.Unlock()
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
		g.mu.Lock()
		g.read[name] = true
		g.mu.Unlock()
	}
	return id, v, text, hash, err
}

// Diff is a read in the read-before-write sense, like Dump: it hands back
// chunks of the buffer's text and records that the caller has seen them.
func (g *Guard) Diff(path string) ([]DiffGroup, error) {
	name, err := g.canonical(path)
	if err != nil {
		return nil, err
	}
	diffs, err := g.Host.Diff(name)
	if err == nil {
		g.mu.Lock()
		g.read[name] = true
		g.mu.Unlock()
	}
	return diffs, err
}

// Patch writes, so it runs the same author check as Apply: a socket may not
// write as the file-as-loaded nor as a person, or its text becomes
// indistinguishable from typed text. The offsets are implicit — the snapshot's
// span told the editor where the chunk lives — so there is no hunk span to
// validate here; the host refuses a snapshot this writer does not own.
func (g *Guard) Patch(path string, author uint8, id uint64, newText string) (uint64, []Conflict, error) {
	if author == AuthorOriginal {
		return 0, nil, fmt.Errorf("author %d is the file as loaded, not a writer", author)
	}
	if g.Participants != nil {
		if !g.Participants.IsAgent(author) {
			return 0, nil, fmt.Errorf("author %d is not an agent", author)
		}
	} else if author < FirstAgent {
		return 0, nil, fmt.Errorf("author %d is not an agent id", author)
	}
	name, err := g.canonical(path)
	if err != nil {
		return 0, nil, err
	}
	return g.Host.Patch(name, author, id, newText)
}

func (g *Guard) Apply(path string, author uint8, base uint64, hunks []Hunk) (uint64, []Conflict, error) {
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
		return 0, nil, fmt.Errorf("author %d is the file as loaded, not a writer", author)
	}
	if g.Participants != nil {
		if !g.Participants.IsAgent(author) {
			return 0, nil, fmt.Errorf("author %d is not an agent", author)
		}
	} else if author < FirstAgent {
		return 0, nil, fmt.Errorf("author %d is not an agent id", author)
	}
	name, err := g.canonical(path)
	if err != nil {
		return 0, nil, err
	}
	g.mu.Lock()
	seen := g.read[name]
	g.mu.Unlock()
	if !seen {
		return 0, nil, errors.New("read the buffer before writing it: offsets only mean " +
			"something in the coordinates of a version you have seen")
	}
	for i, h := range hunks {
		if h.Start < 0 || h.End < h.Start {
			return 0, nil, fmt.Errorf("hunk %d: start %d, end %d", i, h.Start, h.End)
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
	return escapingGlob(q.Exclude)
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
	return s.inner.Search(ctx, q, emit)
}

func (g *Guard) Save(path string) (uint64, error) {
	name, err := g.canonical(path)
	if err != nil {
		return 0, err
	}
	return g.Host.Save(name)
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
		if err := g.inRoot(dir); err != nil {
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
		v, err := g.Open(req.Path)
		return done(v, err)
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
		name, err := g.canonical(req.Path)
		if err != nil {
			return Response{Err: err.Error()}
		}
		if err := g.Close(name); err != nil {
			return Response{Err: err.Error()}
		}
		return Response{OK: true}

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
		spans, v, err := g.Read(req.Path, start, end, lineStart, lineEnd)
		if err != nil {
			return Response{Err: err.Error()}
		}
		return Response{OK: true, Spans: spans, Version: v}
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
		diffs, err := g.Diff(req.Path)
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
		caller, err := g.Host.LSP(req.Path, req.Line, req.Col, req.LSPMode)
		if err != nil {
			return Response{Err: err.Error()}
		}
		return Response{OK: true, LSP: caller}
	case "version":
		name, err := g.canonical(req.Path)
		if err != nil {
			return Response{Err: err.Error()}
		}
		v, err := g.Version(name)
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
			v, err := g.Version(req.Path)
			return done(v, err)
		}
		v, conflicts, err := g.Apply(req.Path, req.Author, *req.Base, req.Hunks)
		if err != nil {
			return Response{Err: err.Error()}
		}
		res := Response{OK: len(conflicts) == 0, Version: v, Conflicts: conflicts}
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
		v, conflicts, err := g.Patch(req.Path, req.Author, req.DumpID, req.PatchText)
		if err != nil {
			return Response{Err: err.Error()}
		}
		res := Response{OK: len(conflicts) == 0, Version: v, Conflicts: conflicts}
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
