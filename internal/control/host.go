package control

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
)

// BufferHost is the vocabulary, with no transport in it.
//
// HARNESS-BROKER-AGENT.md puts the transport last and the interface second, for
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
	Read(path string) (spans []Span, version uint64, err error)

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
	Search(ctx context.Context, q SearchQuery, emit func([]SearchMatch)) (files int, capped bool, err error)
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

func (g *Guard) Read(path string) ([]Span, uint64, error) {
	name, err := g.canonical(path)
	if err != nil {
		return nil, 0, err
	}
	spans, v, err := g.Host.Read(name)
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

func (s guardedSearcher) Search(ctx context.Context, q SearchQuery, emit func([]SearchMatch)) (int, bool, error) {
	if err := s.g.CheckQuery(q); err != nil {
		return 0, false, err
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

		spans, v, err := g.Read(req.Path)
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
	case "version":
		v, err := g.Version(req.Path)
		return done(v, err)
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
	}
	return Response{Err: "unknown op " + req.Op}
}

func done(v uint64, err error) Response {
	if err != nil {
		return Response{Err: err.Error()}
	}
	return Response{OK: true, Version: v}
}
