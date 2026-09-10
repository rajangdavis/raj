package control

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// A BufferHost with no editor behind it. The point of the interface is that
// the rules can be tested without a terminal, an event loop or a socket — the
// document puts the transport last precisely so this is possible.
type memHost struct {
	root   string
	docs   map[string]string
	vers   map[string]uint64
	opens  []string
	dirty  []DirtyBuffer
	groups []Group
	diffs  []DiffGroup
	snaps  map[uint64]snapEntry
	seq    uint64
}

// snapEntry is memHost's dump record: the text captured, and where it was.
type snapEntry struct {
	author  uint8
	path    string
	version uint64
	start   int
	end     int
	text    string
}

func newMemHost(root string, docs map[string]string) *memHost {
	h := &memHost{root: root, docs: map[string]string{}, vers: map[string]uint64{},
		snaps: map[uint64]snapEntry{}}
	for k, v := range docs {
		h.docs[k], h.vers[k] = v, 1
	}
	return h
}

func (h *memHost) Root() string { return h.root }

func (h *memHost) Buffers() []Buffer {
	var out []Buffer
	for p, t := range h.docs {
		out = append(out, Buffer{Path: p, Version: h.vers[p],
			Bytes: len(t), Lines: strings.Count(t, "\n") + 1})
	}
	return out
}

func (h *memHost) Resolve(path string) (string, error) {
	if path == "" {
		for k := range h.docs {
			return k, nil
		}
		return "", ErrNoBuffer
	}
	if _, ok := h.docs[path]; !ok {
		return path, nil // let inRoot and the op itself report it
	}
	return path, nil
}

func (h *memHost) Open(path string) (uint64, error) {
	h.opens = append(h.opens, path)
	if _, ok := h.docs[path]; !ok {
		h.docs[path], h.vers[path] = "", 1
	}
	return h.vers[path], nil
}

func (h *memHost) Read(path string, start, end, lineStart, lineEnd int) ([]Span, uint64, error) {
	t, ok := h.docs[path]
	if !ok {
		return nil, 0, ErrNoBuffer
	}
	if lineStart > 0 {
		starts := []int{0}
		for i := 0; i < len(t); i++ {
			if t[i] == '\n' {
				starts = append(starts, i+1)
			}
		}
		lines := len(starts)
		first := lineStart - 1
		if first < 0 {
			first = 0
		}
		if first >= lines {
			first = lines - 1
		}
		start = starts[first]
		if lineEnd > 0 && lineEnd < lines {
			end = starts[lineEnd]
		} else {
			end = len(t)
		}
	}
	if start < 0 {
		start, end = 0, len(t)
	} else {
		if end < 0 || end > len(t) {
			end = len(t)
		}
		if start > len(t) {
			start = len(t)
		}
	}
	return []Span{{Text: t[start:end], Author: FirstAgent}}, h.vers[path], nil
}

func (h *memHost) Version(path string) (uint64, error) {
	if _, ok := h.docs[path]; !ok {
		return 0, ErrNoBuffer
	}
	return h.vers[path], nil
}

func (h *memHost) Apply(path string, author uint8, base uint64, hunks []Hunk) (uint64, []Conflict, error) {
	t, ok := h.docs[path]
	if !ok {
		return 0, nil, ErrNoBuffer
	}
	if base != h.vers[path] {
		return h.vers[path], []Conflict{{Index: 0, At: h.vers[path], Hunk: hunks[0]}}, nil
	}
	for i := len(hunks) - 1; i >= 0; i-- {
		x := hunks[i]
		if x.End > len(t) {
			return 0, nil, ErrNoBuffer
		}
		t = t[:x.Start] + x.Text + t[x.End:]
	}
	h.docs[path], h.vers[path] = t, h.vers[path]+1
	return h.vers[path], nil, nil
}

func (h *memHost) Save(path string) (uint64, error) { return h.vers[path], nil }

func (h *memHost) Dump(path string, start, end int, author uint8) (uint64, uint64, string, string, error) {
	t, ok := h.docs[path]
	if !ok {
		return 0, 0, "", "", ErrNoBuffer
	}
	if start < 0 {
		start, end = 0, len(t)
	} else {
		if end < 0 || end > len(t) {
			end = len(t)
		}
		if start > len(t) {
			start = len(t)
		}
		if start > end {
			start = end
		}
	}
	h.seq++
	h.snaps[h.seq] = snapEntry{author: author, path: path, version: h.vers[path],
		start: start, end: end, text: t[start:end]}
	return h.seq, h.vers[path], t[start:end], "", nil
}

func (h *memHost) Patch(path string, author uint8, id uint64, newText string) (uint64, []Conflict, error) {
	snap, ok := h.snaps[id]
	if !ok || snap.author != author {
		return 0, nil, fmt.Errorf("no snapshot %d for this writer", id)
	}
	t, ok := h.docs[path]
	if !ok {
		return 0, nil, ErrNoBuffer
	}
	diffs := DiffLines(snap.text, newText)
	if len(diffs) == 0 {
		return h.vers[path], nil, nil
	}
	// The memHost has no journal, so it can only rebase what is still current;
	// the real host replays the journal from the snapshot's version forward.
	for i := len(diffs) - 1; i >= 0; i-- {
		d := diffs[i]
		s, e := snap.start+d.Start, snap.start+d.End
		if e > len(t) {
			return h.vers[path], []Conflict{{Index: i, At: h.vers[path], Hunk: d}}, nil
		}
		t = t[:s] + d.Text + t[e:]
	}
	h.docs[path], h.vers[path] = t, h.vers[path]+1
	return h.vers[path], nil, nil
}

// Goto is a cursor move, and memHost has no cursor: the state it keeps is
// whether the buffer exists, which is also the one thing the real host can get
// wrong before the position is even considered.
func (h *memHost) Goto(path string, line, col int) error {
	if _, ok := h.docs[path]; !ok {
		return ErrNoBuffer
	}
	return nil
}

// Close removes the buffer. A memHost is never dirty, so the unsaved-work
// refusal — which lives in the real host, where the piece table can answer —
// has nothing to test against here.
// The Guard's dirty check is what
// tests exercise (see TestGuardRefusesCloseOfDirtyBuffer).
func (h *memHost) Close(path string) error {
	if _, ok := h.docs[path]; !ok {
		return ErrNoBuffer
	}
	delete(h.docs, path)
	delete(h.vers, path)
	return nil
}

func guarded(t *testing.T) (*Guard, *memHost) {
	t.Helper()
	root := filepath.Join(string(filepath.Separator), "w")
	h := newMemHost(root, map[string]string{filepath.Join(root, "a.go"): "hello world\n"})
	return NewGuard(h), h
}

// Escaping the workspace is a rejection, not a guess — including via "..",
// which is why the check is on the cleaned path rather than on the string.
func TestGuardRejectsPathsOutsideRoot(t *testing.T) {
	g, h := guarded(t)
	outside := []string{
		filepath.Join(string(filepath.Separator), "etc", "passwd"),
		filepath.Join(h.root, "..", "etc", "passwd"),
		filepath.Join(h.root, "sub", "..", "..", "escape"),
		"relative/path.go",
	}
	for _, p := range outside {
		if _, err := g.Open(p); err == nil {
			t.Errorf("Open(%q) was allowed", p)
		}
		if _, _, err := g.Read(p, -1, -1, 0, 0); err == nil {
			t.Errorf("Read(%q) was allowed", p)
		}
		if _, _, err := g.Apply(p, FirstAgent, 1, []Hunk{{}}); err == nil {
			t.Errorf("Apply(%q) was allowed", p)
		}
	}
	if len(h.opens) != 0 {
		t.Errorf("a rejected path still reached the host: %v", h.opens)
	}
}

// Read-before-write. Offsets mean nothing except in the coordinates of a
// version somebody looked at, so a caller that never read is submitting numbers
// it invented.
func TestGuardRequiresAReadBeforeAWrite(t *testing.T) {
	g, h := guarded(t)
	path := filepath.Join(h.root, "a.go")

	if _, _, err := g.Apply(path, FirstAgent, 1, []Hunk{{Start: 0, End: 5, Text: "x"}}); err == nil {
		t.Fatal("a blind write was allowed")
	}
	if h.docs[path] != "hello world\n" {
		t.Fatalf("buffer changed anyway: %q", h.docs[path])
	}

	if _, _, err := g.Read(path, -1, -1, 0, 0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := g.Apply(path, FirstAgent, 1, []Hunk{{Start: 0, End: 5, Text: "howdy"}}); err != nil {
		t.Fatalf("write after read was refused: %v", err)
	}
	if h.docs[path] != "howdy world\n" {
		t.Errorf("buffer = %q", h.docs[path])
	}
}

// Asking for the version is asking for coordinates, which is the thing
// read-before-write is checking for — so it counts, and an agent that only
// wants to append does not have to pull the whole document first.
func TestVersionCountsAsARead(t *testing.T) {
	g, h := guarded(t)
	path := filepath.Join(h.root, "a.go")
	if _, err := g.Version(path); err != nil {
		t.Fatal(err)
	}
	if _, _, err := g.Apply(path, FirstAgent, 1, []Hunk{{Start: 0, End: 0, Text: "// "}}); err != nil {
		t.Errorf("write after version was refused: %v", err)
	}
}

func TestGuardRejectsMalformedSpans(t *testing.T) {
	g, h := guarded(t)
	path := filepath.Join(h.root, "a.go")
	g.Read(path, -1, -1, 0, 0)
	for _, hk := range []Hunk{{Start: -1, End: 0}, {Start: 5, End: 2}} {
		if _, _, err := g.Apply(path, FirstAgent, 1, []Hunk{hk}); err == nil {
			t.Errorf("%+v was allowed", hk)
		}
	}
}

// Dispatch is the only place the verbs are interpreted, so the socket and an
// in-process caller cannot disagree about what an op means.
func TestDispatchVerbs(t *testing.T) {
	g, h := guarded(t)
	path := filepath.Join(h.root, "a.go")

	if res := Dispatch(g, Request{Op: "ping"}); !res.OK || res.Root != h.root {
		t.Errorf("ping = %+v", res)
	}
	if res := Dispatch(g, Request{Op: "buffers"}); !res.OK || len(res.Buffers) != 1 {
		t.Errorf("buffers = %+v", res)
	}
	res := Dispatch(g, Request{Op: "text", Path: path})
	if !res.OK || res.Text() != "hello world\n" || res.Version == 0 {
		t.Fatalf("text = %+v", res)
	}
	// The version comes back with the bytes, which is what lets the next call
	// use offsets instead of matching text.
	base := res.Version
	res = Dispatch(g, Request{Op: "apply", Path: path, Author: FirstAgent, Base: &base,
		Hunks: []Hunk{{Start: 6, End: 11, Text: "socket"}}})
	if !res.OK {
		t.Fatalf("apply = %+v", res)
	}
	if h.docs[path] != "hello socket\n" {
		t.Errorf("buffer = %q", h.docs[path])
	}
	v := Dispatch(g, Request{Op: "version", Path: path})
	if !v.OK || v.Version != res.Version {
		t.Fatalf("version = %+v, want %d", v, res.Version)
	}
	if wantB, wantL := len(h.docs[path]), strings.Count(h.docs[path], "\n")+1; v.Bytes != wantB || v.Lines != wantL {
		t.Errorf("version = %+v, want bytes %d lines %d", v, wantB, wantL)
	}
	// goto moves a cursor the host can verify; a position on a buffer that is
	// not open is refused rather than silently parked.
	if r := Dispatch(g, Request{Op: "goto", Path: path, Line: 2, Col: 3}); !r.OK {
		t.Errorf("goto = %+v", r)
	}
	if r := Dispatch(g, Request{Op: "goto", Path: filepath.Join(h.root, "gone.go"), Line: 1}); r.OK {
		t.Errorf("goto on a missing buffer was allowed: %+v", r)
	}
	// close removes the tab; the second close finds nothing to remove.
	if c := Dispatch(g, Request{Op: "close", Path: path}); !c.OK {
		t.Errorf("close = %+v", c)
	}
	if _, ok := h.docs[path]; ok {
		t.Error("close left the buffer open")
	}
	if c := Dispatch(g, Request{Op: "close", Path: path}); c.OK {
		t.Errorf("second close was allowed: %+v", c)
	}
	if u := Dispatch(g, Request{Op: "frobnicate"}); u.OK || !strings.Contains(u.Err, "unknown op") {
		t.Errorf("unknown op = %+v", u)
	}
}

// A read can be asked for by 1-based line numbers instead of bytes: the host
// translates, so a driver holding a compiler's or a search's line never
// re-derives byte offsets.
func TestDispatchReadsByLines(t *testing.T) {
	g, h := guarded(t)
	path := filepath.Join(h.root, "a.go")
	h.docs[path] = "one\ntwo\nthree\n"
	h.vers[path] = 1

	one, three := 1, 3
	if res := Dispatch(g, Request{Op: "text", Path: path, LineStart: &one, LineEnd: &three}); !res.OK || res.Text() != "one\ntwo\nthree\n" {
		t.Fatalf("lines 1,3 = %q", res.Text())
	}
	two := 2
	if res := Dispatch(g, Request{Op: "text", Path: path, LineStart: &two, LineEnd: &two}); !res.OK || res.Text() != "two\n" {
		t.Fatalf("line 2 = %q, want %q", res.Text(), "two\n")
	}
	// A missing end reads to the end of the file.
	if res := Dispatch(g, Request{Op: "text", Path: path, LineStart: &two}); !res.OK || res.Text() != "two\nthree\n" {
		t.Fatalf("line 2.. = %q", res.Text())
	}
	// A line past the document clamps to the (empty) phantom last line rather
	// than failing or wrapping around.
	late := 99
	if res := Dispatch(g, Request{Op: "text", Path: path, LineStart: &late}); !res.OK || res.Text() != "" {
		t.Fatalf("line 99 = %q, want empty", res.Text())
	}
}

// An apply with no base is refused before anything else looks at it. It is the
// one request that can silently corrupt: offsets measured against some version,
// applied to whatever the buffer is now, looking like success.
func TestDispatchRefusesApplyWithNoBase(t *testing.T) {
	g, h := guarded(t)
	path := filepath.Join(h.root, "a.go")
	Dispatch(g, Request{Op: "text", Path: path})
	res := Dispatch(g, Request{Op: "apply", Path: path, Hunks: []Hunk{{Start: 0, End: 5, Text: "x"}}})
	if res.OK || !strings.Contains(res.Err, "base") {
		t.Errorf("got %+v, want a refusal naming the base", res)
	}
	if h.docs[path] != "hello world\n" {
		t.Errorf("buffer changed anyway: %q", h.docs[path])
	}
}

// A stale base is a per-hunk conflict, not a failure of the whole call and not
// a silent write at the offset it was measured against.
func TestDispatchReportsConflicts(t *testing.T) {
	g, h := guarded(t)
	path := filepath.Join(h.root, "a.go")
	Dispatch(g, Request{Op: "text", Path: path})
	stale := uint64(999)
	res := Dispatch(g, Request{Op: "apply", Path: path, Author: FirstAgent, Base: &stale,
		Hunks: []Hunk{{Start: 0, End: 5, Text: "x"}}})
	if res.OK || len(res.Conflicts) != 1 {
		t.Fatalf("got %+v, want one conflict", res)
	}
	if res.Conflicts[0].Index != 0 {
		t.Errorf("conflict index = %d, want the hunk that failed", res.Conflicts[0].Index)
	}
	if h.docs[path] != "hello world\n" {
		t.Errorf("buffer changed anyway: %q", h.docs[path])
	}
}

// An empty hunk list is a version query rather than an error: it is what a
// caller sending a computed-empty diff produces, and failing it would make the
// caller special-case its own emptiness.
func TestDispatchEmptyApplyIsAVersionQuery(t *testing.T) {
	g, h := guarded(t)
	path := filepath.Join(h.root, "a.go")
	Dispatch(g, Request{Op: "text", Path: path})
	base := uint64(1)
	if res := Dispatch(g, Request{Op: "apply", Path: path, Author: FirstAgent, Base: &base}); !res.OK || res.Version != 1 {
		t.Errorf("got %+v", res)
	}
	if h.docs[path] != "hello world\n" {
		t.Errorf("buffer changed: %q", h.docs[path])
	}
}

// Authorship is enforced at the chokepoint, not trusted from the request. The
// file as loaded and the human are not writers a socket may claim: the tint,
// the per-author undo stacks and the exec dirty-state split all read the
// author, so a request able to name User could make agent text look typed.
func TestGuardRefusesNonAgentAuthors(t *testing.T) {
	g, h := guarded(t)
	path := filepath.Join(h.root, "a.go")
	g.Read(path, -1, -1, 0, 0)
	for _, a := range []uint8{0, 1} {
		if _, _, err := g.Apply(path, a, 1, []Hunk{{Start: 0, End: 1, Text: "x"}}); err == nil {
			t.Errorf("author %d was accepted", a)
		}
	}
	if h.docs[path] != "hello world\n" {
		t.Errorf("buffer changed anyway: %q", h.docs[path])
	}
	if _, _, err := g.Apply(path, FirstAgent+3, 1, []Hunk{{Start: 0, End: 1, Text: "H"}}); err != nil {
		t.Errorf("a later agent id was refused: %v", err)
	}
}

// A read comes back as authored runs, which is what lets a caller tell its own
// text from the user's without a second call.
func TestReadCarriesAuthorship(t *testing.T) {
	g, h := guarded(t)
	spans, _, err := g.Read(filepath.Join(h.root, "a.go"), -1, -1, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(spans) == 0 {
		t.Fatal("no spans")
	}
	var total int
	for _, s := range spans {
		total += len(s.Text)
	}
	if total != len("hello world\n") {
		t.Errorf("spans total %d bytes, want %d", total, len("hello world\n"))
	}
}

func (h *memHost) Snapshot() Searcher { return h }

// LSP has no language server to talk to in a memory host, so the caller is
// nil and the error is the clean "no server" answer a driver would see. The
// real host's implementation is exercised on the editor's machine.
func (h *memHost) LSP(path string, line, col int, mode string) (LSPCaller, error) {
	return nil, fmt.Errorf("no language server for this file type")
}

func (h *memHost) Dirty() []DirtyBuffer { return h.dirty }

func (h *memHost) Groups(path string) ([]Group, error) { return h.groups, nil }

// Diff mirrors Groups: a memory host has no journal to rebase, so the pending
// diffs are fixture data rather than a walk. The real host's walk is
// exercised in internal/piecetable and internal/app instead.
func (h *memHost) Diff(path string) ([]DiffGroup, error) {
	if _, ok := h.docs[path]; !ok {
		return nil, ErrNoBuffer
	}
	return h.diffs, nil
}

func (h *memHost) Decide(path string, group uint64, accept bool) error {
	for i := range h.groups {
		if h.groups[i].ID != group {
			continue
		}
		if accept {
			h.groups[i].State = "accepted"
		} else {
			h.groups[i].State = "rejected"
		}
		return nil
	}
	return ErrNoBuffer
}

func (h *memHost) Search(ctx context.Context, q SearchQuery, emit func([]SearchMatch)) (int, bool, error) {
	var out []SearchMatch
	for p, t := range h.docs {
		for i, line := range strings.Split(t, "\n") {
			if c := strings.Index(line, q.Text); c >= 0 {
				out = append(out, SearchMatch{Path: p, Line: i + 1, Col: c,
					Len: len(q.Text), Text: line})
			}
		}
	}
	if emit != nil && len(out) > 0 {
		emit(out)
	}
	return len(h.docs), false, nil
}

// The document names glob escape separately from path escape because it is a
// separate hole: validating `path` and forgetting `Include`/`Exclude` leaves
// the same door open under a different field name.
func TestGuardRejectsEscapingGlobs(t *testing.T) {
	g, _ := guarded(t)
	for _, bad := range []string{"../*.go", "a,../../etc/*", "/etc/*", "sub/../../x"} {
		if err := g.CheckQuery(SearchQuery{Text: "x", Include: bad}); err == nil {
			t.Errorf("Include %q was allowed", bad)
		}
		if err := g.CheckQuery(SearchQuery{Text: "x", Exclude: bad}); err == nil {
			t.Errorf("Exclude %q was allowed", bad)
		}
	}
	for _, ok := range []string{"", "*.go", "*.go,*.md", "sub/*.go", "a/b/c.txt"} {
		if err := g.CheckQuery(SearchQuery{Text: "hello", Include: ok}); err != nil {
			t.Errorf("Include %q was refused: %v", ok, err)
		}
	}
}

func TestSearchNeedsAPattern(t *testing.T) {
	g, _ := guarded(t)
	if err := g.CheckQuery(SearchQuery{Text: "   "}); err == nil {
		t.Error("a blank pattern was accepted")
	}
	if res := Dispatch(g, Request{Op: "search"}); res.OK {
		t.Error("search with no query was accepted")
	}
}

// The snapshot is what the event thread hands out; the walk happens on whoever
// holds it. A query is validated at the point it runs, so a bad one cannot slip
// through by being submitted on a different goroutine.
func TestSnapshotSearcherValidates(t *testing.T) {
	g, _ := guarded(t)
	s := g.Snapshot()
	if _, _, err := s.Search(context.Background(), SearchQuery{Text: "x", Include: "../*"}, nil); err == nil {
		t.Error("an escaping glob was allowed through the snapshot")
	}
	var got []SearchMatch
	if _, _, err := s.Search(context.Background(), SearchQuery{Text: "world"},
		func(b []SearchMatch) { got = append(got, b...) }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Line != 1 || got[0].Col != 6 {
		t.Errorf("matches = %+v", got)
	}
}

// Reading the active buffer must authorise writing it by name. Keying
// read-before-write on the literal string meant "" and "/w/a.go" looked like
// two different buffers, so a read-then-write against the same file was refused.
func TestReadOfActiveBufferAuthorisesWriteByName(t *testing.T) {
	g, h := guarded(t)
	path := filepath.Join(h.root, "a.go")
	if _, _, err := g.Read("", -1, -1, 0, 0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := g.Apply(path, FirstAgent, 1, []Hunk{{Start: 0, End: 5, Text: "howdy"}}); err != nil {
		t.Errorf("write after reading the same buffer was refused: %v", err)
	}
}

// An agent that does not know its own id cannot tell its text from anyone
// else's, which makes the whole attribution scheme half usable: it can see
// "somebody wrote this" but not "I wrote this".
func TestSpanOwnership(t *testing.T) {
	mine := Span{Text: "a", Author: FirstAgent + 1}
	if !mine.Mine(FirstAgent+1) || mine.Mine(FirstAgent) || mine.ByUser() {
		t.Errorf("own span misreported: %+v", mine)
	}
	user := Span{Text: "b", Author: AuthorUser}
	if user.Mine(FirstAgent) || !user.ByUser() {
		t.Errorf("user span misreported: %+v", user)
	}
	// Comparing against the FirstAgent constant instead of the assigned id is
	// the mistake this guards: it answers for the wrong writer whenever more
	// than one agent is connected.
	other := Span{Text: "c", Author: FirstAgent + 5}
	if other.Mine(FirstAgent + 1) {
		t.Error("another agent's span read as mine")
	}
	if orig := (Span{Author: AuthorOriginal}); orig.Mine(FirstAgent) || orig.ByUser() {
		t.Error("the file as loaded was attributed to a writer")
	}
}

// v1 of the exec policy: refuse while anything is unsaved, and name the files.
//
// The alternative is flushing, and flushing writes the user's unsaved edits to
// disk as a side effect of an agent's action — the one outcome here that cannot
// be undone. Refusing costs a round trip.
// An unsaved buffer is reported, never a refusal. Refusing turns a solvable
// interpretation problem into a blocked one; flushing writes the user's work to
// disk as a side effect, which everything downstream of the filesystem reacts
// to. Running and saying so does neither.
func TestExecRunsDespiteUnsavedBuffers(t *testing.T) {
	g, h := guarded(t)
	if _, err := g.CheckExec([]string{"go", "test"}, ""); err != nil {
		t.Fatalf("a clean workspace was refused: %v", err)
	}

	h.dirty = []DirtyBuffer{{Path: filepath.Join(h.root, "a.go"), AgentOnly: false}}
	dirty, err := g.CheckExec([]string{"go", "test"}, "")
	if err != nil {
		t.Fatalf("an unsaved buffer refused the command: %v", err)
	}
	if len(dirty) != 1 || dirty[0].Path != filepath.Join(h.root, "a.go") {
		t.Errorf("the stale file was not reported: %+v", dirty)
	}
}

// The counter is what turns the policy from an argument into a measurement:
// Blocked says how often it got in the way, AgentOnly how many of those were
// cases where flushing would have been safe.
func TestExecStatsRecordStaleRuns(t *testing.T) {
	g, h := guarded(t)
	a := filepath.Join(h.root, "a.go")

	g.CheckExec([]string{"true"}, "")                   // clean: a run, no block
	h.dirty = []DirtyBuffer{{Path: a, AgentOnly: true}} // agent's own work
	g.CheckExec([]string{"true"}, "")
	h.dirty = []DirtyBuffer{{Path: a, AgentOnly: false}} // the human's
	g.CheckExec([]string{"true"}, "")
	h.dirty = []DirtyBuffer{{Path: a, AgentOnly: true}, {Path: a, AgentOnly: false}}
	g.CheckExec([]string{"true"}, "") // any user span makes the whole thing not agent-only

	st := g.Stats()
	if st.Runs != 4 || st.Stale != 3 || st.AgentOnly != 1 {
		t.Errorf("stats = %+v, want 4 runs, 3 stale, 1 agent-only", st)
	}
}

func TestExecValidatesTheCommandAndDirectory(t *testing.T) {
	g, h := guarded(t)
	if _, err := g.CheckExec(nil, ""); err == nil {
		t.Error("an empty command was accepted")
	}
	if _, err := g.CheckExec([]string{"  "}, ""); err == nil {
		t.Error("a blank command was accepted")
	}
	if _, err := g.CheckExec([]string{"true"}, filepath.Join(h.root, "..", "elsewhere")); err == nil {
		t.Error("a directory outside the workspace was accepted")
	}
}

// Change sets are addressable, and a decision is validated like any other verb.
func TestDispatchGroups(t *testing.T) {
	g, h := guarded(t)
	path := filepath.Join(h.root, "a.go")
	h.groups = []Group{{ID: 7, Path: path, Author: FirstAgent, State: "proposed", Ops: 2}}

	res := Dispatch(g, Request{Op: "groups", Path: path})
	if !res.OK || len(res.Groups) != 1 || res.Groups[0].ID != 7 {
		t.Fatalf("groups = %+v", res)
	}
	if res := Dispatch(g, Request{Op: "accept", Path: path, Group: 7}); !res.OK {
		t.Fatalf("accept = %+v", res)
	}
	if h.groups[0].State != "accepted" {
		t.Errorf("state = %q", h.groups[0].State)
	}
	// A decision without a group id is a refusal rather than a guess at which
	// change set was meant.
	if res := Dispatch(g, Request{Op: "reject", Path: path}); res.OK {
		t.Error("reject with no group id was accepted")
	}
	if res := Dispatch(g, Request{Op: "reject", Path: "/etc/passwd", Group: 7}); res.OK {
		t.Error("a path outside the workspace was accepted")
	}
}

// diff is the review surface for what groups only lists: each pending change
// set comes back as old→new hunks in current coordinates, and an empty
// pending set is a clean answer rather than an error.
func TestDispatchDiff(t *testing.T) {
	g, h := guarded(t)
	path := filepath.Join(h.root, "a.go")
	h.diffs = []DiffGroup{{
		Group: Group{ID: 7, Path: path, Author: FirstAgent, State: "proposed", Ops: 1, Bytes: 1},
		Hunks: []DiffHunk{{Start: 6, End: 11, Old: "world", New: "earth"}},
	}}

	res := Dispatch(g, Request{Op: "diff", Path: path})
	if !res.OK {
		t.Fatalf("diff = %+v", res)
	}
	var diffs []DiffGroup
	if err := json.Unmarshal([]byte(res.DiffJSON), &diffs); err != nil {
		t.Fatalf("the diff payload is not JSON: %v", err)
	}
	if len(diffs) != 1 || diffs[0].ID != 7 || len(diffs[0].Hunks) != 1 {
		t.Fatalf("decoded diffs = %+v, want the one seeded group and hunk", diffs)
	}
	if diffs[0].Hunks[0].Old != "world" || diffs[0].Hunks[0].New != "earth" {
		t.Errorf("hunk = %+v", diffs[0].Hunks[0])
	}

	// Nothing pending is a clean buffer: OK with an empty list, not an error.
	h.diffs = nil
	res = Dispatch(g, Request{Op: "diff", Path: path})
	if !res.OK || res.DiffJSON != "[]" {
		t.Errorf("empty diff = %+v, want OK with an empty list", res)
	}

	// A path outside the workspace is refused, as for every other verb.
	if res := Dispatch(g, Request{Op: "diff", Path: "/etc/passwd"}); res.OK {
		t.Error("a path outside the workspace was accepted")
	}
}

// A dump hands back a whole chunk and an id; a patch returns the edited whole
// text and the editor diffs and applies it, so a driver never derives offsets
// nor matches old text.
func TestDumpAndPatchRoundTrip(t *testing.T) {
	g, h := guarded(t)
	path := filepath.Join(h.root, "a.go")
	h.docs[path] = "one\ntwo\nthree\nfour\n"
	h.vers[path] = 1

	s, e := 4, 14 // "two\nthree\n"
	res := Dispatch(g, Request{Op: "dump", Path: path, Author: FirstAgent, Start: &s, End: &e})
	if !res.OK || res.DumpID == 0 || res.Text() != "two\nthree\n" {
		t.Fatalf("dump = %+v", res)
	}
	id := res.DumpID

	patched := Dispatch(g, Request{Op: "patch", Path: path, Author: FirstAgent,
		DumpID: id, PatchText: "TWO\nthree\n"})
	if !patched.OK {
		t.Fatalf("patch = %+v", patched)
	}
	if h.docs[path] != "one\nTWO\nthree\nfour\n" {
		t.Errorf("buffer = %q", h.docs[path])
	}

	// A patch naming a snapshot another writer holds is refused, not applied.
	other := Dispatch(g, Request{Op: "patch", Path: path, Author: FirstAgent + 1,
		DumpID: id, PatchText: "x"})
	if other.OK {
		t.Errorf("a foreign snapshot was patched: %+v", other)
	}

	// A dump without a span captures the whole file.
	whole := Dispatch(g, Request{Op: "dump", Path: path, Author: FirstAgent})
	if !whole.OK || whole.Text() != "one\nTWO\nthree\nfour\n" {
		t.Errorf("whole-file dump = %q", whole.Text())
	}
}

// An lsp request reaches the host, which in a memory host has no server — a
// clean error rather than a hang. The real host's blocking path is exercised
// only on the editor's machine, where a language server can run.
func TestDispatchLSPNoServer(t *testing.T) {
	g, h := guarded(t)
	path := filepath.Join(h.root, "a.go")
	if res := Dispatch(g, Request{Op: "lspprep", Path: path, Line: 1, Col: 1, LSPMode: "hover"}); res.OK {
		t.Errorf("lsp without a server was OK: %+v", res)
	}
	_ = h
}

// DiffLines turns old text into new as byte hunks; the round trip through the
// memHost is the same code the real host's Patch runs, so this pins the diff
// before any LSP or diff verb leans on it.
func TestDiffLines(t *testing.T) {
	for _, tc := range []struct{ a, b string }{
		{"a\nb\nc\n", "a\nB\nc\n"},
		{"a\nb\nc\n", "a\nX\nY\nc\n"},
		{"a\nb\n", "a\n"},
		{"a\nb\n", "a\nb\nc\n"},
		{"", "x\n"},
		{"x\n", ""},
		{"same\n", "same\n"},
		{"func f() {\n\treturn 1\n}\n", "func f() {\n\treturn 2\n}\n"},
	} {
		hunks := DiffLines(tc.a, tc.b)
		// Hunks are non-overlapping and in ascending byte order, so a forward
		// apply works, but only if they do not overlap. Apply in reverse to be
		// safe, mirroring how Apply diff is written.
		got := tc.a
		for i := len(hunks) - 1; i >= 0; i-- {
			h := hunks[i]
			got = got[:h.Start] + h.Text + got[h.End:]
		}
		if got != tc.b {
			t.Errorf("DiffLines(%q, %q): applied to %q, want %q (hunks %+v)", tc.a, tc.b, got, tc.b, hunks)
		}
	}
}
