package control

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"raj/internal/prog"
)

// A BufferHost with no editor behind it. The point of the interface is that
// the rules can be tested without a terminal, an event loop or a socket — the
// document puts the transport last precisely so this is possible.
type memHost struct {
	root   string
	docs   map[string]string
	vers   map[string]uint64
	opens  []string
	reads  []bool
	dirty  []DirtyBuffer
	groups []Group
	diffs  []DiffGroup
	snaps  map[uint64]snapEntry
	seq    uint64

	// unsaved marks a buffer dirty or holding a pending change set, which is
	// what plain Close refuses and CloseDiscard drops. disk stands in for the
	// filesystem: Save writes docs into it, so a test can tell a close that
	// discarded the buffer from one that wrote it out.
	unsaved map[string]bool
	disk    map[string]string
	saves   int
	// mkdirs records every directory Mkdir was asked to create. memHost has no
	// filesystem, so this is what the guard tests assert: the resolved path
	// arrived, and a refused one never did.
	mkdirs []string
	// renames records every Rename the Guard passed down, so the guard tests
	// can assert the canonical old and new arrived without a real filesystem.
	renames []renameCall
	// deletions is memHost's pending-deletion set, keyed by path, standing in
	// for the app's workspace-level state. A second proposal is idempotent and
	// keeps the first proposer, the same as the app.
	deletions map[string]Deletion
	// dirRemovals is memHost's pending dir-removal set, the rmdir analogue of
	// deletions, keyed by directory path.
	dirRemovals map[string]DirRemoval
	// entries is the canned ls answer, and lastLs records the request the Guard
	// passed down, so Dispatch tests can assert the resolved path and the
	// -hidden flag without a filesystem.
	entries []Entry
	lastLs  Request
	// hiddenSearch is set when SearchHidden runs, so a Guard test can tell the
	// include-hidden walk from the ordinary one. Nothing else observes it.
	hiddenSearch bool
	// proposals is a canned unified pending list, so a Dispatch test can prove
	// the rollup without an editor behind it.
	proposals []Proposal
	// hintsJSON is the canned inlay-hint answer LSPInlayHints hands back, and
	// hintLines records the 1-based range the request carried, so a Dispatch
	// test can assert the plumbing without a language server.
	hintsJSON string
	hintLines [2]int
	// symbolsJSON is the canned workspace/symbol answer LSPWorkspaceSymbols
	// hands back, and symbolsQuery records the query the request carried, so a
	// Dispatch test can assert the plumbing without a language server.
	symbolsJSON  string
	symbolsQuery string
	// formatJSON is the canned formatting answer LSPFormat hands back, and
	// formatLines records the 1-based range the request carried, so a Dispatch
	// test can assert the plumbing without a language server.
	formatJSON  string
	formatLines [2]int
	// lease, leaseAuthor, leaseStart and leaseEnd let a test make Apply answer
	// a stale base with a lease refusal carrying the owner and span the real
	// app reports, so Dispatch and the CLI can be checked without a journal
	// behind them.
	lease       uint64
	leaseAuthor uint8
	leaseStart  int
	leaseEnd    int
	// warnings is what Apply answers on success, so a Dispatch or CLI test can
	// drive the advisory-lease warning without a journal behind it.
	warnings []GroupOverlap
	// reverts records every Revert the Guard passed down, so the guard and
	// Dispatch tests can assert the canonical path and author arrived without
	// an editor behind them. revertBlock, when non-zero, makes Revert answer a
	// wedge with the live set that overlaps, the refusal the real host gives.
	reverts     []revertCall
	revertBlock Conflict
}

// revertCall is one memHost.Revert: the canonical path and the author the Guard
// resolved, recorded so a test can assert the pair that crossed.
type revertCall struct {
	path   string
	author uint8
}

// renameCall is one memHost.Rename: the canonical old and new the Guard
// resolved, recorded so a guard test can assert the pair that crossed.
type renameCall struct {
	old string
	new string
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
		snaps: map[uint64]snapEntry{}, unsaved: map[string]bool{}, disk: map[string]string{}}
	for k, v := range docs {
		h.docs[k], h.vers[k] = v, 1
		h.disk[k] = v
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

func (h *memHost) Open(path string, create bool) (uint64, bool, error) {
	h.opens = append(h.opens, path)
	created := false
	if _, ok := h.docs[path]; !ok {
		// The memHost has no disk, so the set of docs stands in for what is
		// reachable: a path that is not already a buffer and not present is
		// the missing file the real host stats for. create is what tells the
		// two apart.
		if !create {
			return 0, false, fmt.Errorf("no open buffer or file at %s; pass --create to make a new buffer", path)
		}
		h.docs[path], h.vers[path] = "", 1
		created = true
	}
	return h.vers[path], created, nil
}

func (h *memHost) Read(path string, author uint8, start, end, lineStart, lineEnd int, annotated bool) ([]Span, []StateRun, uint64, error) {
	t, ok := h.docs[path]
	if !ok {
		return nil, nil, 0, ErrNoBuffer
	}
	h.reads = append(h.reads, annotated)
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
	spans := []Span{{Text: t[start:end], Author: FirstAgent}}
	if annotated {
		// The control tests do not model decisions; a run covering the read
		// text is enough to prove the flag reached the host and the states
		// came back on the response.
		return spans, []StateRun{{Off: 0, Len: end - start, Group: 7, State: "proposed"}}, h.vers[path], nil
	}
	return spans, nil, h.vers[path], nil
}

// Find is the memory host's single-buffer search. It is a literal substring
// scan on purpose: what these tests exercise is the Dispatch plumbing, not the
// matching, and the real host uses the editor's engine. Case-insensitivity is
// honoured by folding both sides; the regex and whole-word flags are accepted
// but only the literal behaviour is modelled.
func (h *memHost) Find(path string, author uint8, q SearchQuery) (SearchMatch, int, bool, error) {
	text, ok := h.docs[path]
	if !ok {
		return SearchMatch{}, 0, false, ErrNoBuffer
	}
	out := SearchMatch{Path: path, Version: h.vers[path]}
	if q.Text == "" {
		return out, 0, false, nil
	}
	hay, needle := text, q.Text
	if !q.Case {
		hay, needle = strings.ToLower(hay), strings.ToLower(needle)
	}
	count := 0
	for off := 0; off <= len(hay)-len(needle); {
		i := strings.Index(hay[off:], needle)
		if i < 0 {
			break
		}
		start := off + i
		if count == 0 {
			out.ByteStart, out.ByteEnd, out.Len = start, start+len(q.Text), len(q.Text)
			out.Line = 1 + strings.Count(text[:start], "\n")
			lineStart := strings.LastIndexByte(text[:start], '\n') + 1
			out.LineStart = lineStart
			out.Col = start - lineStart
			lineEnd := len(text)
			if nl := strings.IndexByte(text[start:], '\n'); nl >= 0 {
				lineEnd = start + nl
			}
			out.LineEnd = lineEnd
			out.Text = strings.TrimRight(text[lineStart:lineEnd], " \t")
		}
		count++
		off = start + len(needle)
	}
	return out, count, count > 0, nil
}

func (h *memHost) Version(path string, author uint8) (uint64, error) {
	if _, ok := h.docs[path]; !ok {
		return 0, ErrNoBuffer
	}
	return h.vers[path], nil
}

func (h *memHost) Apply(path string, author uint8, base uint64, hunks []Hunk) (uint64, []Conflict, []GroupOverlap, error) {
	t, ok := h.docs[path]
	if !ok {
		return 0, nil, nil, ErrNoBuffer
	}
	if base != h.vers[path] {
		return h.vers[path], []Conflict{{Index: 0, At: h.vers[path], Group: h.lease,
			Author: h.leaseAuthor, Start: h.leaseStart, End: h.leaseEnd, Hunk: hunks[0]}}, nil, nil
	}
	for i := len(hunks) - 1; i >= 0; i-- {
		x := hunks[i]
		if x.End > len(t) {
			return 0, nil, nil, ErrNoBuffer
		}
		t = t[:x.Start] + x.Text + t[x.End:]
	}
	h.docs[path], h.vers[path] = t, h.vers[path]+1
	return h.vers[path], nil, h.warnings, nil
}

func (h *memHost) Save(path string, force bool) (uint64, error) {
	h.saves++
	h.disk[path] = h.docs[path]
	return h.vers[path], nil
}

// Reload answers the reload verb with the in-memory disk: memHost has no
// filesystem, so disk stands in for the bytes raj last read or wrote.
func (h *memHost) Reload(path string) error {
	if _, ok := h.docs[path]; !ok {
		return ErrNoBuffer
	}
	if disk, ok := h.disk[path]; ok {
		h.docs[path] = disk
	}
	return nil
}

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

func (h *memHost) Patch(path string, author uint8, id uint64, newText string) (uint64, []Conflict, []GroupOverlap, error) {
	snap, ok := h.snaps[id]
	if !ok || snap.author != author {
		return 0, nil, nil, fmt.Errorf("no snapshot %d for this writer", id)
	}
	t, ok := h.docs[path]
	if !ok {
		return 0, nil, nil, ErrNoBuffer
	}
	diffs := DiffLines(snap.text, newText)
	if len(diffs) == 0 {
		return h.vers[path], nil, nil, nil
	}
	// The memHost has no journal, so it can only rebase what is still current;
	// the real host replays the journal from the snapshot's version forward.
	for i := len(diffs) - 1; i >= 0; i-- {
		d := diffs[i]
		s, e := snap.start+d.Start, snap.start+d.End
		if e > len(t) {
			return h.vers[path], []Conflict{{Index: i, At: h.vers[path], Hunk: d}}, nil, nil
		}
		t = t[:s] + d.Text + t[e:]
	}
	h.docs[path], h.vers[path] = t, h.vers[path]+1
	return h.vers[path], nil, h.warnings, nil
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

// Mkdir records the directory, since memHost has no filesystem: the guard tests
// care that the resolved path arrived and that a refused one did not.
func (h *memHost) Mkdir(path string) error {
	h.mkdirs = append(h.mkdirs, path)
	return nil
}

// Rename moves a buffer's name. memHost has no journal, LSP document or session,
// so the carry the real host does is a key move here: the text, version and
// unsaved flag follow old to new. A name that is not a buffer is still
// recorded — the real host would move the file on disk — so a Guard test can
// assert the resolved pair arrived even for a path nobody has open.
func (h *memHost) Rename(old, new string) error {
	h.renames = append(h.renames, renameCall{old: old, new: new})
	text, ok := h.docs[old]
	if !ok {
		return nil
	}
	version, unsaved := h.vers[old], h.unsaved[old]
	delete(h.docs, old)
	delete(h.vers, old)
	delete(h.unsaved, old)
	h.docs[new], h.vers[new] = text, version
	if unsaved {
		h.unsaved[new] = true
	}
	if d, ok := h.disk[old]; ok {
		delete(h.disk, old)
		h.disk[new] = d
	}
	return nil
}

// ProposeDeletion, WithdrawDeletion and Deletions are memHost's half of the
// pending-deletion state. memHost never unlinks — there is no filesystem — so
// these record exactly what the Guard passed down and no more.
func (h *memHost) ProposeDeletion(path string, author uint8) error {
	if h.deletions == nil {
		h.deletions = map[string]Deletion{}
	}
	if _, ok := h.deletions[path]; ok {
		return nil
	}
	h.deletions[path] = Deletion{Path: path, Author: author}
	return nil
}

func (h *memHost) WithdrawDeletion(path string, author uint8) error {
	d, ok := h.deletions[path]
	if !ok {
		return nil
	}
	if d.Author != author {
		return fmt.Errorf("pending deletion of %s was proposed by author %d, not this writer", path, d.Author)
	}
	delete(h.deletions, path)
	return nil
}

func (h *memHost) Deletions() []Deletion {
	out := make([]Deletion, 0, len(h.deletions))
	for _, d := range h.deletions {
		out = append(out, d)
	}
	return out
}

// ProposeDirRemoval, WithdrawDirRemoval and DirRemovals are memHost's half of
// the pending dir-removal state, mirroring the deletion triple. memHost never
// removes anything, so these record what the Guard passed down and no more.
func (h *memHost) ProposeDirRemoval(path string, author uint8) error {
	if h.dirRemovals == nil {
		h.dirRemovals = map[string]DirRemoval{}
	}
	if _, ok := h.dirRemovals[path]; ok {
		return nil
	}
	h.dirRemovals[path] = DirRemoval{Path: path, Author: author}
	return nil
}

func (h *memHost) WithdrawDirRemoval(path string, author uint8) error {
	d, ok := h.dirRemovals[path]
	if !ok {
		return nil
	}
	if d.Author != author {
		return fmt.Errorf("pending dir-removal of %s was proposed by author %d, not this writer", path, d.Author)
	}
	delete(h.dirRemovals, path)
	return nil
}

func (h *memHost) DirRemovals() []DirRemoval {
	out := make([]DirRemoval, 0, len(h.dirRemovals))
	for _, d := range h.dirRemovals {
		out = append(out, d)
	}
	return out
}

// Ls is memHost's half of the ls verb: it records the request the Guard
// resolved and returns the canned entries. memHost has no filesystem, so the
// listing itself — the hidden policy, the sort and which files carry a size —
// is covered in internal/app.
func (h *memHost) Ls(path string, all bool) ([]Entry, error) {
	h.lastLs = Request{Op: "ls", Path: path, Hidden: all}
	return append([]Entry(nil), h.entries...), nil
}

// Proposals returns memHost's canned unified pending list. The real rollup
// lives in internal/app; this is only what the Guard's sort and Dispatch need.
func (h *memHost) Proposals() []Proposal {
	return append([]Proposal(nil), h.proposals...)
}

// Close removes the buffer, refusing one marked unsaved — dirty or holding a
// pending change set — the way the real host refuses one whose piece table is
// dirty. memHost answers that from its unsaved set; CloseDiscard is the way
// past it, and the pair is what TestDispatchCloseDiscard exercises.
func (h *memHost) Close(path string) error {
	if _, ok := h.docs[path]; !ok {
		return ErrNoBuffer
	}
	if h.unsaved[path] {
		return fmt.Errorf("%s has unsaved changes; save or reject them first", path)
	}
	delete(h.docs, path)
	delete(h.vers, path)
	delete(h.unsaved, path)
	return nil
}

// CloseDiscard removes the buffer and the unsaved work with it, mirroring the
// real host's close-without-save. It writes nothing: disk keeps the bytes it
// had, and no Save is called, which is what makes discarding safe for the file
// the buffer was loaded from.
func (h *memHost) CloseDiscard(path string) (bool, error) {
	if _, ok := h.docs[path]; !ok {
		return false, ErrNoBuffer
	}
	delete(h.docs, path)
	delete(h.vers, path)
	delete(h.unsaved, path)
	// remains says a file is still on disk where the buffer's path was. The
	// fake's disk map stands in for the filesystem, and CloseDiscard never
	// writes it, so a doc that was loaded from (or saved into) disk reports
	// one.
	_, remains := h.disk[path]
	return remains, nil
}

func guarded(t *testing.T) (*Guard, *memHost) {
	t.Helper()
	root := filepath.Join(string(filepath.Separator), "w")
	h := newMemHost(root, map[string]string{filepath.Join(root, "a.go"): "hello world\n"})
	return NewGuard(h), h
}

// A dirty buffer — or one holding a pending change set — refuses a plain
// close, because a silent discard would lose work. close -discard is the way
// past it: the buffer and its proposals go, and the file on disk keeps the
// bytes it had, because nothing is written.
func TestDispatchCloseDiscard(t *testing.T) {
	g, h := guarded(t)
	path := filepath.Join(h.root, "a.go")
	// The buffer holds an edit a save would write and a pending set nobody
	// accepted; either way it is unsaved, and plain close must refuse.
	h.docs[path] = "hello socket\n"
	h.unsaved[path] = true

	if c := Dispatch(g, Request{Op: "close", Path: path}); c.OK {
		t.Fatalf("close of a dirty buffer was allowed: %+v", c)
	}
	if _, ok := h.docs[path]; !ok {
		t.Fatal("a refused close dropped the buffer")
	}

	c := Dispatch(g, Request{Op: "close", Path: path, Discard: true})
	if !c.OK {
		t.Fatalf("close -discard = %+v", c)
	}
	if !c.Remains {
		t.Error("close -discard of a buffer that was loaded from disk did not report a remainder")
	}
	if _, ok := h.docs[path]; ok {
		t.Error("close -discard left the buffer open")
	}
	for _, b := range h.Buffers() {
		if b.Path == path {
			t.Errorf("close -discard left %s in Buffers()", path)
		}
	}
	if h.saves != 0 {
		t.Errorf("close -discard saved the buffer (%d save(s)); the file on disk must be untouched", h.saves)
	}
	if got := h.disk[path]; got != "hello world\n" {
		t.Errorf("on-disk file = %q, want the text it was loaded with", got)
	}
	if c := Dispatch(g, Request{Op: "close", Path: path}); c.OK {
		t.Errorf("second close was allowed: %+v", c)
	}
}

// A discarded buffer that never reached disk has no remainder to report: there
// is no file to leave behind. The wire answer's absence of the field is what a
// driver reads as "nothing remains", which is why it must stay false here.
func TestDispatchCloseDiscardNoRemainderForAnUnsavedBuffer(t *testing.T) {
	g, h := guarded(t)
	path := filepath.Join(h.root, "fresh.go")
	if _, _, err := g.Open(path, true); err != nil {
		t.Fatalf("open -create = %v", err)
	}
	delete(h.disk, path) // it exists only as a buffer

	c := Dispatch(g, Request{Op: "close", Path: path, Discard: true})
	if !c.OK {
		t.Fatalf("close -discard = %+v", c)
	}
	if c.Remains {
		t.Error("close -discard of a buffer with no file reported a remainder")
	}
}

// open says which of the two things it did. A create makes a buffer that was
// not there; a focus reaches one already loaded. Both are success, and a driver
// that meant to create a file needs to tell them apart.
func TestDispatchOpenReportsCreated(t *testing.T) {
	g, h := guarded(t)
	path := filepath.Join(h.root, "a.go")

	// Already a buffer: focusing it is not a create.
	res := Dispatch(g, Request{Op: "open", Path: path, Author: FirstAgent})
	if !res.OK || res.Created {
		t.Errorf("open of an existing buffer = %+v, want ok and not created", res)
	}

	// A new path with --create reports the buffer as made.
	fresh := filepath.Join(h.root, "new.go")
	res = Dispatch(g, Request{Op: "open", Path: fresh, Author: FirstAgent, Create: true})
	if !res.OK || !res.Created {
		t.Errorf("open -create = %+v, want ok and created", res)
	}

	// A second open of it focuses: not created again.
	res = Dispatch(g, Request{Op: "open", Path: fresh, Author: FirstAgent, Create: true})
	if !res.OK || res.Created {
		t.Errorf("second open -create = %+v, want ok and not created", res)
	}
}

// find answers with the first match's byte span and the total count, and a
// pattern that does not occur is a clean not-found rather than an error.
func TestDispatchFindReturnsASpan(t *testing.T) {
	g, h := guarded(t)
	path := filepath.Join(h.root, "a.go")
	h.docs[path] = "alpha needle beta\nneedle again\n"

	res := Dispatch(g, Request{Op: "find", Path: path, Author: FirstAgent,
		Query: &SearchQuery{Text: "needle"}})
	if !res.OK || !res.Found {
		t.Fatalf("find = %+v, want ok and found", res)
	}
	if res.FindStart != 6 || res.FindEnd != 12 {
		t.Errorf("span = [%d,%d), want [6,12)", res.FindStart, res.FindEnd)
	}
	if res.FindCount != 2 {
		t.Errorf("count = %d, want 2", res.FindCount)
	}
	if res.Version != h.vers[path] {
		t.Errorf("version = %d, want %d", res.Version, h.vers[path])
	}

	miss := Dispatch(g, Request{Op: "find", Path: path, Author: FirstAgent,
		Query: &SearchQuery{Text: "absent"}})
	if !miss.OK || miss.Found {
		t.Errorf("missing pattern = %+v, want ok and not found", miss)
	}
	if miss.FindCount != 0 {
		t.Errorf("missing count = %d, want 0", miss.FindCount)
	}
}

// find needs a pattern, the same way search does, and validates the query
// before touching the host.
func TestDispatchFindNeedsAPattern(t *testing.T) {
	g, _ := guarded(t)
	if res := Dispatch(g, Request{Op: "find"}); res.OK {
		t.Error("find with no query was accepted")
	}
	if res := Dispatch(g, Request{Op: "find", Query: &SearchQuery{Text: "   "}}); res.OK {
		t.Error("find with a blank pattern was accepted")
	}
}

// Escaping the workspace is a rejection, not a guess — including via "..",
// which is why the check is on the cleaned path rather than on the string.
func TestGuardRejectsPathsOutsideRoot(t *testing.T) {
	g, h := guarded(t)
	outside := []string{
		filepath.Join(string(filepath.Separator), "etc", "passwd"),
		filepath.Join(h.root, "..", "etc", "passwd"),
		filepath.Join(h.root, "sub", "..", "..", "escape"),
	}
	for _, p := range outside {
		if _, _, err := g.Open(p, false); err == nil {
			t.Errorf("Open(%q) was allowed", p)
		}
		if _, _, _, err := g.Read(p, FirstAgent, -1, -1, 0, 0, false); err == nil {
			t.Errorf("Read(%q) was allowed", p)
		}
		if _, _, _, err := g.Apply(p, FirstAgent, 1, []Hunk{{}}); err == nil {
			t.Errorf("Apply(%q) was allowed", p)
		}
	}
	if len(h.opens) != 0 {
		t.Errorf("a rejected path still reached the host: %v", h.opens)
	}
}

// A relative path is inside the workspace by construction: it resolves against
// the root, the same step every host verb takes, so open accepts the spelling a
// caller would type. An escaping one is still refused, because the check runs
// on the joined path.
func TestGuardAcceptsRelativePathInsideTheRoot(t *testing.T) {
	g, h := guarded(t)
	want := filepath.Join(h.root, "a.go")
	if _, _, err := g.Open("a.go", false); err != nil {
		t.Fatalf("Open(%q) was refused: %v", "a.go", err)
	}
	if len(h.opens) != 1 || h.opens[0] != want {
		t.Errorf("the host was asked to open %v, want %q", h.opens, want)
	}
	if _, _, err := g.Open(filepath.Join("..", "etc", "passwd"), false); err == nil {
		t.Error("a relative path that escapes the root was accepted")
	}
	if len(h.opens) != 1 {
		t.Errorf("the escaping path reached the host: %v", h.opens)
	}
}

// mkdir resolves a directory the same way every other path-taking verb does:
// relative to the root, cleaned, and checked in-root — without loading a buffer,
// because a directory is not one. It is not claim-gated, so a caller can make
// the package directory before it has a file to claim.
func TestGuardMkdirResolvesAndRefusesEscape(t *testing.T) {
	g, h := guarded(t)

	if err := g.Mkdir("pkg/sub"); err != nil {
		t.Fatalf("Mkdir(%q) = %v", "pkg/sub", err)
	}
	want := filepath.Join(h.root, "pkg", "sub")
	if len(h.mkdirs) != 1 || h.mkdirs[0] != want {
		t.Fatalf("the host was asked to create %v, want [%q]", h.mkdirs, want)
	}

	// An absolute path inside the root names the same directory.
	h.mkdirs = nil
	abs := filepath.Join(h.root, "pkg", "abs")
	if err := g.Mkdir(abs); err != nil {
		t.Fatalf("Mkdir(%q) = %v", abs, err)
	}
	if len(h.mkdirs) != 1 || h.mkdirs[0] != abs {
		t.Errorf("the host was asked to create %v, want [%q]", h.mkdirs, abs)
	}

	// Escaping the root is a refusal, and must not reach the host.
	h.mkdirs = nil
	for _, bad := range []string{
		filepath.Join(h.root, "..", "etc"),
		filepath.Join("..", "etc"),
		filepath.Join(string(filepath.Separator), "etc"),
		"",
	} {
		if err := g.Mkdir(bad); err == nil {
			t.Errorf("Mkdir(%q) was allowed", bad)
		}
	}
	if len(h.mkdirs) != 0 {
		t.Errorf("a refused mkdir reached the host: %v", h.mkdirs)
	}
}

// The dispatch path wires mkdir to the guard: a relative path is created under
// the root and answers OK, an empty path is a refusal, and an escape never
// reaches the host.
func TestDispatchMkdir(t *testing.T) {
	g, h := guarded(t)

	res := Dispatch(g, Request{Op: "mkdir", Path: filepath.Join("pkg", "sub")})
	if !res.OK {
		t.Fatalf("mkdir = %+v", res)
	}
	want := filepath.Join(h.root, "pkg", "sub")
	if len(h.mkdirs) != 1 || h.mkdirs[0] != want {
		t.Errorf("the host was asked to create %v, want [%q]", h.mkdirs, want)
	}

	if res := Dispatch(g, Request{Op: "mkdir"}); res.OK || !strings.Contains(res.Err, "needs a path") {
		t.Errorf("mkdir with no path = %+v, want a refusal", res)
	}

	h.mkdirs = nil
	if res := Dispatch(g, Request{Op: "mkdir", Path: "../escape"}); res.OK {
		t.Errorf("mkdir outside the root was allowed: %+v", res)
	}
	if len(h.mkdirs) != 0 {
		t.Errorf("an escaping mkdir reached the host: %v", h.mkdirs)
	}
}

// open reaches only something that already exists. A path that is neither a
// buffer nor a file is a typo, and refusing it is what keeps a misspelled name
// from silently becoming an empty buffer that is later created. -create is the
// caller saying it means to make one.
func TestOpenRefusesMissingPathWithoutCreate(t *testing.T) {
	g, h := guarded(t)
	missing := filepath.Join(h.root, "new.go")

	if _, created, err := g.Open(missing, false); err == nil {
		t.Errorf("Open(%q) was allowed without -create", missing)
	} else if created {
		t.Errorf("a refused open reported a create")
	}
	if _, ok := h.docs[missing]; ok {
		t.Errorf("a refused open still created a buffer for %q", missing)
	}

	if _, created, err := g.Open(missing, true); err != nil {
		t.Fatalf("Open(%q, create) was refused: %v", missing, err)
	} else if !created {
		t.Errorf("Open(%q, create) did not report the buffer as created", missing)
	}
	if _, ok := h.docs[missing]; !ok {
		t.Errorf("Open(%q, create) created no buffer", missing)
	}
}

// clear purges a rejected set and refuses anything else. The refusal names the
// group and is an editor refusal, not the usage error for a request that named
// no group at all.
func TestDispatchClearOnlyActsOnRejected(t *testing.T) {
	g, h := guarded(t)
	path := filepath.Join(h.root, "a.go")
	g.setClaims(FirstAgent, []string{path})
	h.groups = []Group{{ID: 7, Path: path, State: "rejected"}, {ID: 9, Path: path, State: "proposed"}}

	if res := Dispatch(g, Request{Op: "clear", Path: path, Author: FirstAgent, Group: 7}); !res.OK {
		t.Fatalf("clear of a rejected set = %+v", res)
	}
	if h.groups[0].State != "accepted" {
		t.Errorf("group 7 state = %q, want purged", h.groups[0].State)
	}
	res := Dispatch(g, Request{Op: "clear", Path: path, Author: FirstAgent, Group: 9})
	if res.OK || !strings.Contains(res.Err, "change set 9") {
		t.Errorf("clear of a proposed set = %+v, want a refusal naming it", res)
	}
	if res := Dispatch(g, Request{Op: "clear", Path: path}); res.OK || !strings.Contains(res.Err, "group id") {
		t.Errorf("clear with no group = %+v, want a usage refusal", res)
	}
}

// Read-before-write. Offsets mean nothing except in the coordinates of a
// version somebody looked at, so a caller that never read is submitting numbers
// it invented.
func TestGuardRequiresAReadBeforeAWrite(t *testing.T) {
	g, h := guarded(t)
	path := filepath.Join(h.root, "a.go")
	// The claim gate sits in front of the read gate; satisfy it so this test
	// still exercises the read rule it is named for.
	g.setClaims(FirstAgent, []string{path})

	if _, _, _, err := g.Apply(path, FirstAgent, 1, []Hunk{{Start: 0, End: 5, Text: "x"}}); err == nil {
		t.Fatal("a blind write was allowed")
	}
	if h.docs[path] != "hello world\n" {
		t.Fatalf("buffer changed anyway: %q", h.docs[path])
	}

	if _, _, _, err := g.Read(path, FirstAgent, -1, -1, 0, 0, false); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := g.Apply(path, FirstAgent, 1, []Hunk{{Start: 0, End: 5, Text: "howdy"}}); err != nil {
		t.Fatalf("write after read was refused: %v", err)
	}
	if h.docs[path] != "howdy world\n" {
		t.Errorf("buffer = %q", h.docs[path])
	}
}

// A live connection that has not declared an identity — a reserved, rowless
// author id — is still allowed to write. The socket's permissions, or the TCP
// token, already decided it may connect, and the write gate must not newly
// refuse the anonymous case that the old per-connection row admitted.
func TestApplyAllowsAReservedAuthor(t *testing.T) {
	g, h := guarded(t)
	reg := NewRegistry()
	g.Participants = reg
	id, err := reg.Reserve()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(h.root, "a.go")
	g.setClaims(id, []string{path})
	if _, _, _, err := g.Read(path, id, -1, -1, 0, 0, false); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := g.Apply(path, id, 1, []Hunk{{Start: 0, End: 5, Text: "x"}}); err != nil {
		t.Fatalf("a reserved author's write was refused: %v", err)
	}
	if h.docs[path] != "x world\n" {
		t.Errorf("buffer = %q, want the write applied", h.docs[path])
	}
	// A durable identity must not be handed the reserved id.
	dur, err := reg.Join("someone", "", KindAgent)
	if err != nil {
		t.Fatal(err)
	}
	if dur == id {
		t.Fatalf("Join reused the reserved id %d", id)
	}
}

// Read-before-write is per writer, not per connection or per app. Keying the
// read set by path alone let one participant's read authorise another's blind
// write to the same buffer, inheriting coordinates that writer never saw.
func TestGuardReadBeforeWriteIsPerAuthor(t *testing.T) {
	g, h := guarded(t)
	path := filepath.Join(h.root, "a.go")
	g.setClaims(FirstAgent, []string{path})
	g.setClaims(FirstAgent+1, []string{path})

	// The first writer reads, then writes at the coordinates it saw.
	if _, _, _, err := g.Read(path, FirstAgent, -1, -1, 0, 0, false); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := g.Apply(path, FirstAgent, 1, []Hunk{{Start: 0, End: 5, Text: "howdy"}}); err != nil {
		t.Fatalf("write after own read was refused: %v", err)
	}

	// A second writer has read nothing; the first writer's read must not
	// satisfy its write gate.
	if _, _, _, err := g.Apply(path, FirstAgent+1, 2, []Hunk{{Start: 0, End: 5, Text: "there"}}); err == nil {
		t.Fatal("a write by an author that had not read was allowed")
	} else if !strings.Contains(err.Error(), "read the buffer before writing") {
		t.Errorf("blind write = %v, want the read-before-write refusal", err)
	}
	if h.docs[path] != "howdy world\n" {
		t.Fatalf("buffer changed anyway: %q", h.docs[path])
	}

	// Once the second writer reads for itself, its write is allowed.
	if _, _, _, err := g.Read(path, FirstAgent+1, -1, -1, 0, 0, false); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := g.Apply(path, FirstAgent+1, 2, []Hunk{{Start: 0, End: 5, Text: "there"}}); err != nil {
		t.Fatalf("write after own read was refused: %v", err)
	}
	if h.docs[path] != "there world\n" {
		t.Errorf("buffer = %q", h.docs[path])
	}
}

// Asking for the version is asking for coordinates, which is the thing
// read-before-write is checking for — so it counts, and an agent that only
// wants to append does not have to pull the whole document first.
func TestVersionCountsAsARead(t *testing.T) {
	g, h := guarded(t)
	path := filepath.Join(h.root, "a.go")
	g.setClaims(FirstAgent, []string{path})
	if _, err := g.Version(path, FirstAgent); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := g.Apply(path, FirstAgent, 1, []Hunk{{Start: 0, End: 0, Text: "// "}}); err != nil {
		t.Errorf("write after version was refused: %v", err)
	}
}

func TestGuardRejectsMalformedSpans(t *testing.T) {
	g, h := guarded(t)
	path := filepath.Join(h.root, "a.go")
	g.setClaims(FirstAgent, []string{path})
	g.Read(path, FirstAgent, -1, -1, 0, 0, false)
	for _, hk := range []Hunk{{Start: -1, End: 0}, {Start: 5, End: 2}} {
		if _, _, _, err := g.Apply(path, FirstAgent, 1, []Hunk{hk}); err == nil {
			t.Errorf("%+v was allowed", hk)
		}
	}
}

// Dispatch is the only place the verbs are interpreted, so the socket and an
// in-process caller cannot disagree about what an op means.
func TestDispatchVerbs(t *testing.T) {
	g, h := guarded(t)
	path := filepath.Join(h.root, "a.go")
	g.setClaims(FirstAgent, []string{path})

	if res := Dispatch(g, Request{Op: "ping"}); !res.OK || res.Root != h.root {
		t.Errorf("ping = %+v", res)
	}
	if res := Dispatch(g, Request{Op: "buffers"}); !res.OK || len(res.Buffers) != 1 {
		t.Errorf("buffers = %+v", res)
	}
	res := Dispatch(g, Request{Op: "text", Path: path, Author: FirstAgent})
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
	Dispatch(g, Request{Op: "text", Path: path, Author: FirstAgent})
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
	g.setClaims(FirstAgent, []string{path})
	Dispatch(g, Request{Op: "text", Path: path, Author: FirstAgent})
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
	Dispatch(g, Request{Op: "text", Path: path, Author: FirstAgent})
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
	g.setClaims(FirstAgent+3, []string{path})
	// The read is attributed to the writer whose write the gate must allow
	// below; per-author read-before-write means an earlier read by anyone else
	// would not satisfy it.
	g.Read(path, FirstAgent+3, -1, -1, 0, 0, false)
	for _, a := range []uint8{0, 1} {
		if _, _, _, err := g.Apply(path, a, 1, []Hunk{{Start: 0, End: 1, Text: "x"}}); err == nil {
			t.Errorf("author %d was accepted", a)
		}
	}
	if h.docs[path] != "hello world\n" {
		t.Errorf("buffer changed anyway: %q", h.docs[path])
	}
	if _, _, _, err := g.Apply(path, FirstAgent+3, 1, []Hunk{{Start: 0, End: 1, Text: "H"}}); err != nil {
		t.Errorf("a later agent id was refused: %v", err)
	}
}

// A read comes back as authored runs, which is what lets a caller tell its own
// text from the user's without a second call.
func TestReadCarriesAuthorship(t *testing.T) {
	g, h := guarded(t)
	spans, _, _, err := g.Read(filepath.Join(h.root, "a.go"), FirstAgent, -1, -1, 0, 0, false)
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

// An annotated read must reach the host with the flag set and carry its state
// runs back on the response, or the read verb silently returns the view with
// no state runs attached.
func TestAnnotatedReadCarriesStates(t *testing.T) {
	g, h := guarded(t)
	path := filepath.Join(h.root, "a.go")
	res := Dispatch(g, Request{Op: "text", Path: path, Annotated: true})
	if !res.OK {
		t.Fatalf("text = %+v", res)
	}
	if len(h.reads) == 0 || !h.reads[len(h.reads)-1] {
		t.Errorf("host saw annotated reads %v, want the last one true", h.reads)
	}
	if res.StatesJSON == "" {
		t.Fatal("annotated read returned no states")
	}
	var runs []StateRun
	if err := json.Unmarshal([]byte(res.StatesJSON), &runs); err != nil {
		t.Fatalf("states are not JSON: %v", err)
	}
	if len(runs) != 1 || runs[0].State != "proposed" || runs[0].Len != len("hello world\n") {
		t.Errorf("states = %+v", runs)
	}
}

func (h *memHost) Snapshot() Searcher { return h }

// DocSnapshot answers the whole-document read with the in-memory text. The
// encoding is the UTF-8 default written out as JSON, which is enough for the
// guard and Dispatch tests; the real editor's encoding is covered in app.
func (h *memHost) DocSnapshot(path string) ([]byte, uint64, []byte, string, error) {
	t, ok := h.docs[path]
	if !ok {
		return nil, 0, nil, "", ErrNoBuffer
	}
	return []byte(t), h.vers[path], []byte(`{"Kind":0,"CRLF":false,"BOM":false,"Mixed":false}`), path, nil
}

// SearchHidden records that the include-hidden walk was asked for, then
// answers exactly as Search does. It exists so the Guard's dispatch on the
// -hidden flag can be asserted without a real walker behind it.
func (h *memHost) SearchHidden(ctx context.Context, q SearchQuery, emit func([]SearchMatch)) (int, int, bool, []TruncatedFile, error) {
	h.hiddenSearch = true
	return h.Search(ctx, q, emit)
}

// LSP has no language server to talk to in a memory host, so the caller is
// nil and the error is the clean "no server" answer a driver would see. The
// real host's implementation is exercised on the editor's machine.
func (h *memHost) LSP(path string, line, col int, mode string) (LSPCaller, error) {
	return nil, fmt.Errorf("no language server for this file type")
}

// LSPInlayHints records the range the request carried and hands back the
// canned hint answer, so the Dispatch plumbing can be exercised without a
// language server. An empty hintsJSON is the same clean "no server" answer
// LSP gives.
func (h *memHost) LSPInlayHints(path string, lineStart, lineEnd int) (LSPCaller, error) {
	h.hintLines = [2]int{lineStart, lineEnd}
	if h.hintsJSON == "" {
		return nil, fmt.Errorf("no language server for this file type")
	}
	return fakeLSP{json: h.hintsJSON}, nil
}

// LSPWorkspaceSymbols records the query and hands back the canned symbol
// answer, so the Dispatch plumbing can be exercised without a language server.
// An empty symbolsJSON is the same clean "no server" answer LSP gives.
func (h *memHost) LSPWorkspaceSymbols(path, query string) (LSPCaller, error) {
	h.symbolsQuery = query
	if h.symbolsJSON == "" {
		return nil, fmt.Errorf("no language server for this file type")
	}
	return fakeLSP{json: h.symbolsJSON}, nil
}

// LSPFormat records the range the request carried and hands back the canned
// formatting answer, so the Dispatch plumbing can be exercised without a
// language server. An empty formatJSON is the same clean "no server" answer
// LSP gives.
func (h *memHost) LSPFormat(path string, lineStart, lineEnd int) (LSPCaller, error) {
	h.formatLines = [2]int{lineStart, lineEnd}
	if h.formatJSON == "" {
		return nil, fmt.Errorf("no language server for this file type")
	}
	return fakeLSP{json: h.formatJSON}, nil
}

func (h *memHost) Dirty() []DirtyBuffer { return h.dirty }

func (h *memHost) Groups(path string) ([]Group, error) { return h.groups, nil }

// Diff mirrors Groups: a memory host has no journal to rebase, so the pending
// diffs are fixture data rather than a walk. The real host's walk is
// exercised in internal/piecetable and internal/app instead.
func (h *memHost) Diff(path string, author uint8) ([]DiffGroup, error) {
	if _, ok := h.docs[path]; !ok {
		return nil, ErrNoBuffer
	}
	return h.diffs, nil
}

// Review mirrors Groups over a memory host: the pending sets are fixture data
// and there is no app mode to switch, so listOnly is ignored. The real host's
// EnterReview is exercised in internal/app.
func (h *memHost) Review(path string, listOnly bool) ([]Group, error) {
	if _, ok := h.docs[path]; !ok {
		return nil, ErrNoBuffer
	}
	var out []Group
	for _, g := range h.groups {
		if g.State == "proposed" {
			out = append(out, g)
		}
	}
	return out, nil
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

// Clear models the real ClearRejected: only a currently rejected set can be
// purged, and the refusal names the group. The memHost has no journal to
// reverse, so flipping the state stands in for the edit that removes the text.
func (h *memHost) Clear(path string, group uint64) error {
	for i := range h.groups {
		if h.groups[i].ID != group {
			continue
		}
		if h.groups[i].State != "rejected" {
			return fmt.Errorf("change set %d is not rejected", group)
		}
		h.groups[i].State = "accepted"
		return nil
	}
	return fmt.Errorf("no change set %d", group)
}

// Revert models the real RevertAuthor: it discards the writer's own pieces,
// which a memory host with no journal stands in for by recording the call. A
// non-zero revertBlock makes it answer the wedge the real host reports, so the
// Dispatch and CLI block paths can be exercised without a journal behind them.
func (h *memHost) Revert(path string, author uint8) error {
	if h.revertBlock.Group != 0 {
		return &BlockError{Conflict: h.revertBlock,
			Message: fmt.Sprintf("author %d's pieces cannot be reverted", author)}
	}
	if _, ok := h.docs[path]; !ok {
		return ErrNoBuffer
	}
	h.reverts = append(h.reverts, revertCall{path: path, author: author})
	return nil
}

func (h *memHost) Search(ctx context.Context, q SearchQuery, emit func([]SearchMatch)) (int, int, bool, []TruncatedFile, error) {
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
	return len(h.docs), len(h.docs), false, nil, nil
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

// The search scope is a path, so it gets the path check: a relative name is
// resolved against the root and an escape is refused before the walk starts.
func TestGuardRejectsEscapingSearchPath(t *testing.T) {
	g, _ := guarded(t)
	for _, bad := range []string{"..", "../elsewhere", "sub/../../etc"} {
		if err := g.CheckQuery(SearchQuery{Text: "x", Path: bad}); err == nil {
			t.Errorf("Path %q was allowed", bad)
		}
	}
	for _, ok := range []string{"", "sub", "a/b"} {
		if err := g.CheckQuery(SearchQuery{Text: "hello", Path: ok}); err != nil {
			t.Errorf("Path %q was refused: %v", ok, err)
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
	if _, _, _, _, err := s.Search(context.Background(), SearchQuery{Text: "x", Include: "../*"}, nil); err == nil {
		t.Error("an escaping glob was allowed through the snapshot")
	}
	var got []SearchMatch
	if _, _, _, _, err := s.Search(context.Background(), SearchQuery{Text: "world"},
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
	g.setClaims(FirstAgent, []string{path})
	if _, _, _, err := g.Read("", FirstAgent, -1, -1, 0, 0, false); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := g.Apply(path, FirstAgent, 1, []Hunk{{Start: 0, End: 5, Text: "howdy"}}); err != nil {
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

// claimGuard builds a Guard over a temp directory with real files, because a
// claim's path is checked against the filesystem and memHost's docs are not
// files. names are relative to the temp root; the returned map gives each one's
// absolute spelling.
func claimGuard(t *testing.T, names ...string) (*Guard, *memHost, map[string]string) {
	t.Helper()
	root := t.TempDir()
	h := newMemHost(root, nil)
	paths := map[string]string{}
	for _, n := range names {
		p := filepath.Join(root, n)
		if err := os.WriteFile(p, []byte(n), 0o644); err != nil {
			t.Fatal(err)
		}
		paths[n] = p
	}
	return NewGuard(h), h, paths
}

// A claim set is replaced, extended, cleared and reported, per identity, with
// the resulting set returned in a stable order.
func TestClaimSetAddClearReport(t *testing.T) {
	g, _, p := claimGuard(t, "a.go", "b.go", "c.go")
	a, b, c := p["a.go"], p["b.go"], p["c.go"]

	// set replaces the whole set, regardless of the order given.
	res := Dispatch(g, Request{Op: "claim", Author: FirstAgent, Paths: []string{b, a}})
	if !res.OK {
		t.Fatalf("claim set = %+v", res)
	}
	if got := strings.Join(res.Claims, ","); got != a+","+b {
		t.Errorf("claims = %v, want the set in stable order", res.Claims)
	}
	if len(res.ClaimWarnings) != 0 {
		t.Errorf("warnings = %v, want none", res.ClaimWarnings)
	}

	// report: no operands and no flags names the standing set.
	rep := Dispatch(g, Request{Op: "claim", Author: FirstAgent})
	if !rep.OK || strings.Join(rep.Claims, ",") != a+","+b {
		t.Errorf("report = %v, want the standing set", rep.Claims)
	}

	// add extends it rather than replacing it.
	add := Dispatch(g, Request{Op: "claim", Author: FirstAgent, ClaimAdd: true, Paths: []string{c}})
	if !add.OK || strings.Join(add.Claims, ",") != a+","+b+","+c {
		t.Errorf("add = %v, want the union", add.Claims)
	}

	// clear releases it.
	clr := Dispatch(g, Request{Op: "claim", Author: FirstAgent, ClaimClear: true})
	if !clr.OK || len(clr.Claims) != 0 {
		t.Errorf("clear = %+v, want an empty set", clr.Claims)
	}
	if rep := Dispatch(g, Request{Op: "claim", Author: FirstAgent}); len(rep.Claims) != 0 {
		t.Errorf("report after clear = %v, want empty", rep.Claims)
	}

	// A relative operand resolves against the workspace root, like every verb.
	rel := Dispatch(g, Request{Op: "claim", Author: FirstAgent, Paths: []string{"a.go"}})
	if !rel.OK || len(rel.Claims) != 1 || rel.Claims[0] != a {
		t.Errorf("relative claim = %v, want %s", rel.Claims, a)
	}
}

// A path that is not on disk is claimed all the same: a claim is
// forward-looking, so a writer may claim the file it is about to create
// without an open -create first. The in-root check is the only validation the
// path needs, and the forward claim satisfies the write gate.
func TestClaimAcceptsANotYetOnDiskPath(t *testing.T) {
	g, _, p := claimGuard(t, "a.go")
	a := p["a.go"]
	missing := filepath.Join(g.Root(), "gone.go")

	res := Dispatch(g, Request{Op: "claim", Author: FirstAgent, Paths: []string{a, missing}})
	if !res.OK {
		t.Fatalf("claim = %+v", res)
	}
	if len(res.Claims) != 2 || res.Claims[0] != a || res.Claims[1] != missing {
		t.Errorf("claims = %v, want both paths", res.Claims)
	}
	if len(res.ClaimWarnings) != 0 {
		t.Errorf("warnings = %v, want none for a forward claim", res.ClaimWarnings)
	}
	if name, err := g.claimTarget(FirstAgent, missing); err != nil || name != missing {
		t.Errorf("claimTarget(%s) = %q, %v; want the forward claim to satisfy the write gate", missing, name, err)
	}
}

// A path that exists only as an open buffer is claimable. open -create makes a
// buffer before any file is on disk, so its stat failure is not a missing file;
// claiming it must succeed silently and satisfy the write gate.
func TestClaimAcceptsAnOpenUnsavedBuffer(t *testing.T) {
	g, h, _ := claimGuard(t)
	path := filepath.Join(h.root, "new.go")

	if res := Dispatch(g, Request{Op: "open", Path: path, Author: FirstAgent, Create: true}); !res.OK {
		t.Fatalf("open -create = %+v", res)
	}
	// open -create auto-claims, so drop that and let claim be the thing under
	// test.
	g.clearClaims(FirstAgent)

	res := Dispatch(g, Request{Op: "claim", Author: FirstAgent, Paths: []string{path}})
	if !res.OK {
		t.Fatalf("claim of an open unsaved buffer = %+v", res)
	}
	if len(res.ClaimWarnings) != 0 {
		t.Errorf("warnings = %v, want none for an open buffer", res.ClaimWarnings)
	}
	if len(res.Claims) != 1 || res.Claims[0] != path {
		t.Errorf("claims = %v, want [%s]", res.Claims, path)
	}

	// The claim is what the write gate needed: read, then apply.
	if _, _, _, err := g.Read(path, FirstAgent, -1, -1, 0, 0, false); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := g.Apply(path, FirstAgent, h.vers[path], []Hunk{{Start: 0, End: 0, Text: "x"}}); err != nil {
		t.Fatalf("apply after claim of an open buffer was refused: %v", err)
	}
}

// A stat that fails for a reason other than absence is a real problem — here a
// path component that is a regular file — and is named and skipped rather than
// claimed blind.
func TestClaimWarnsForAnUnusablePath(t *testing.T) {
	g, _, p := claimGuard(t, "a.go")
	broken := filepath.Join(p["a.go"], "child.go")

	res := Dispatch(g, Request{Op: "claim", Author: FirstAgent, Paths: []string{broken}})
	if !res.OK {
		t.Fatalf("claim = %+v", res)
	}
	if len(res.Claims) != 0 {
		t.Errorf("claims = %v, want none for an unusable path", res.Claims)
	}
	if len(res.ClaimWarnings) != 1 || !strings.Contains(res.ClaimWarnings[0], "child.go") {
		t.Errorf("warnings = %v, want one naming the unusable path", res.ClaimWarnings)
	}
}

// An out-of-root operand is a refusal, not a warning: the path check is the
// same one every other verb makes.
func TestClaimRefusesPathsOutsideRoot(t *testing.T) {
	g, _, _ := claimGuard(t, "a.go")
	res := Dispatch(g, Request{Op: "claim", Author: FirstAgent, Paths: []string{"/etc/passwd"}})
	if res.OK || !strings.Contains(res.Err, "outside the workspace") {
		t.Errorf("claim of an outside path = %+v, want a refusal", res)
	}
	if rep := Dispatch(g, Request{Op: "claim", Author: FirstAgent}); len(rep.Claims) != 0 {
		t.Errorf("a refused claim changed the set: %v", rep.Claims)
	}
}

// A rename is claim-gated on the OLD name and moves the working set with it, so
// the claim names the file that exists after the rename. No read-before-write
// is required: no offset is at stake.
func TestGuardRenameMovesTheClaimSet(t *testing.T) {
	g, h, p := claimGuard(t, "a.go")
	old, newPath := p["a.go"], filepath.Join(g.Root(), "b.go")
	if _, _, _, err := g.Claim(FirstAgent, []string{old}, false, false); err != nil {
		t.Fatal(err)
	}
	if err := g.Rename(old, newPath, FirstAgent); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if len(h.renames) != 1 || h.renames[0].old != old || h.renames[0].new != newPath {
		t.Errorf("host got %+v, want %s -> %s", h.renames, old, newPath)
	}
	if got := g.claimSet(FirstAgent); len(got) != 1 || got[0] != newPath {
		t.Errorf("claim set = %v, want [%s]", got, newPath)
	}
}

// The old name must be in the caller's claim set: the claim is the record of
// what an agent declared it would touch, and a rename touches the file.
func TestGuardRenameRefusesUnclaimedOld(t *testing.T) {
	g, h, p := claimGuard(t, "a.go", "b.go")
	err := g.Rename(p["a.go"], filepath.Join(g.Root(), "c.go"), FirstAgent)
	if err == nil || !strings.Contains(err.Error(), "claim") {
		t.Fatalf("rename of an unclaimed file = %v, want a claim refusal", err)
	}
	if len(h.renames) != 0 {
		t.Errorf("the host was asked to rename anyway: %+v", h.renames)
	}
}

// A destination that already exists belongs to somebody else: renaming onto it
// would destroy a file the caller never claimed.
func TestGuardRenameRefusesAnExistingDestination(t *testing.T) {
	g, h, p := claimGuard(t, "a.go", "b.go")
	if _, _, _, err := g.Claim(FirstAgent, []string{p["a.go"]}, false, false); err != nil {
		t.Fatal(err)
	}
	err := g.Rename(p["a.go"], p["b.go"], FirstAgent)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("rename onto an existing file = %v, want a refusal", err)
	}
	if len(h.renames) != 0 {
		t.Errorf("the host was asked to clobber: %+v", h.renames)
	}
	// On failure the claim set is untouched.
	if got := g.claimSet(FirstAgent); len(got) != 1 || got[0] != p["a.go"] {
		t.Errorf("claim set = %v, want the old path kept", got)
	}
}

// Both names are checked in-root without loading a buffer, so an escape is a
// refusal rather than a clamp.
func TestGuardRenameRefusesEscape(t *testing.T) {
	g, h, p := claimGuard(t, "a.go")
	if _, _, _, err := g.Claim(FirstAgent, []string{p["a.go"]}, false, false); err != nil {
		t.Fatal(err)
	}
	if err := g.Rename(p["a.go"], "/etc/passwd", FirstAgent); err == nil {
		t.Error("a destination outside the root was allowed")
	}
	if len(h.renames) != 0 {
		t.Errorf("the host was asked anyway: %+v", h.renames)
	}
}

// A case-only rename is allowed even where the filesystem reports the
// destination as already present, because that destination is the source: the
// two spellings are one file. On a case-sensitive filesystem it is allowed
// trivially. The host is what forces the change with the two-step.
func TestGuardRenameAllowsACaseOnlyChange(t *testing.T) {
	g, h, p := claimGuard(t, "a.go")
	if _, _, _, err := g.Claim(FirstAgent, []string{p["a.go"]}, false, false); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(g.Root(), "A.go")
	if err := g.Rename(p["a.go"], dst, FirstAgent); err != nil {
		t.Fatalf("case-only rename: %v", err)
	}
	if len(h.renames) != 1 || h.renames[0].new != dst {
		t.Errorf("host got %+v, want %s", h.renames, dst)
	}
}

// The socket frame and the in-process Dispatch call must mean the same thing.
func TestDispatchRename(t *testing.T) {
	g, h, p := claimGuard(t, "a.go")
	old, newPath := p["a.go"], filepath.Join(g.Root(), "b.go")
	Dispatch(g, Request{Op: "claim", Author: FirstAgent, Paths: []string{old}})
	res := Dispatch(g, Request{Op: "rename", Path: old, NewPath: newPath, Author: FirstAgent})
	if !res.OK {
		t.Fatalf("dispatch rename = %+v", res)
	}
	if len(h.renames) != 1 || h.renames[0].old != old || h.renames[0].new != newPath {
		t.Errorf("host got %+v", h.renames)
	}
}

// A rename with only one path is refused before the host is asked.
func TestGuardRenameNeedsBothPaths(t *testing.T) {
	g, _, p := claimGuard(t, "a.go")
	if _, _, _, err := g.Claim(FirstAgent, []string{p["a.go"]}, false, false); err != nil {
		t.Fatal(err)
	}
	if err := g.Rename(p["a.go"], "", FirstAgent); err == nil {
		t.Error("a rename with no destination was allowed")
	}
	if err := g.Rename("", p["a.go"], FirstAgent); err == nil {
		t.Error("a rename with no source was allowed")
	}
}

// Claims are not locks, so two identities may hold the same path; each is told
// about the other, named by the display name when one is set so a token key
// does not surface, and by the identity otherwise.
func TestClaimOverlapsBetweenIdentities(t *testing.T) {
	g, _, p := claimGuard(t, "a.go", "b.go")
	a, b := p["a.go"], p["b.go"]

	reg := NewRegistry()
	alice, err := reg.Join("raj-f589cfd7", "Alice", KindAgent)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := reg.Join("bob", "", KindAgent)
	if err != nil {
		t.Fatal(err)
	}
	g.Participants = reg

	if res := Dispatch(g, Request{Op: "claim", Author: alice, Paths: []string{a, b}}); !res.OK {
		t.Fatalf("alice claim = %+v", res)
	}

	// Bob claims only a; the overlap names alice on that path.
	res := Dispatch(g, Request{Op: "claim", Author: bob, Paths: []string{a}})
	if !res.OK || len(res.ClaimOverlaps) != 1 {
		t.Fatalf("bob claim = %+v, want one overlap", res)
	}
	o := res.ClaimOverlaps[0]
	if o.Path != a || o.Author != alice || o.Identity != "Alice" {
		t.Errorf("overlap = %+v, want path a and the display name", o)
	}

	// A report by alice sees the same overlap against bob, so each claimant
	// learns about the other rather than only the later arrival.
	rep := Dispatch(g, Request{Op: "claim", Author: alice})
	if len(rep.ClaimOverlaps) != 1 {
		t.Fatalf("alice report overlaps = %+v, want one", rep.ClaimOverlaps)
	}
	if rep.ClaimOverlaps[0].Identity != "bob" || rep.ClaimOverlaps[0].Path != a {
		t.Errorf("overlap = %+v, want bob on a", rep.ClaimOverlaps[0])
	}
}

// A lease refusal over the socket carries the owning set's author and span,
// not only its group, so a caller does not need a second groups call to learn
// who holds the text and where it is.
func TestDispatchLeaseRefusalCarriesTheOwner(t *testing.T) {
	g, h := guarded(t)
	path := filepath.Join(h.root, "a.go")
	h.lease, h.leaseAuthor, h.leaseStart, h.leaseEnd = 7, 3, 6, 11

	if res := Dispatch(g, Request{Op: "claim", Author: FirstAgent, Paths: []string{path}}); !res.OK {
		t.Fatalf("claim = %+v", res)
	}
	if _, _, _, err := g.Read(path, FirstAgent, -1, -1, 0, 0, false); err != nil {
		t.Fatal(err)
	}
	base := uint64(0)
	res := Dispatch(g, Request{Op: "apply", Path: path, Author: FirstAgent, Base: &base,
		Hunks: []Hunk{{Start: 0, End: 5, Text: "x"}}})
	if len(res.Conflicts) != 1 {
		t.Fatalf("conflicts = %+v, want one", res.Conflicts)
	}
	c := res.Conflicts[0]
	if c.Group != 7 || c.Author != 3 || c.Start != 6 || c.End != 11 {
		t.Errorf("conflict = %+v, want group 7 author 3 span 6..11", c)
	}
	if c.At != h.vers[path] {
		t.Errorf("At = %d, want the stale base %d", c.At, h.vers[path])
	}
}

// A successful apply over another writer Proposed span carries the superseded
// set in the reply warnings, so a Dispatch caller can tell a clean apply, an
// apply with a warning, and a refusal apart. Modelled on
// TestDispatchLeaseRefusalCarriesTheOwner, which is the refusal half.
func TestDispatchApplyCarriesWarnings(t *testing.T) {
	g, h := guarded(t)
	path := filepath.Join(h.root, "a.go")
	h.warnings = []GroupOverlap{{Group: 7, Author: 3, Start: 6, End: 12}}

	if res := Dispatch(g, Request{Op: "claim", Author: FirstAgent, Paths: []string{path}}); !res.OK {
		t.Fatalf("claim = %+v", res)
	}
	if _, _, _, err := g.Read(path, FirstAgent, -1, -1, 0, 0, false); err != nil {
		t.Fatal(err)
	}
	base := uint64(1)
	res := Dispatch(g, Request{Op: "apply", Path: path, Author: FirstAgent, Base: &base,
		Hunks: []Hunk{{Start: 0, End: 0, Text: "x"}}})
	if !res.OK || len(res.Conflicts) != 0 {
		t.Fatalf("apply = %+v, want it to land cleanly", res)
	}
	if len(res.Warnings) != 1 {
		t.Fatalf("warnings = %+v, want one", res.Warnings)
	}
	if w := res.Warnings[0]; w.Group != 7 || w.Author != 3 || w.Start != 6 || w.End != 12 {
		t.Errorf("warning = %+v, want set 7 by author 3 at 6..12", w)
	}

	// A clean apply with no canned warnings reports none.
	h.warnings = nil
	next := res.Version
	res = Dispatch(g, Request{Op: "apply", Path: path, Author: FirstAgent, Base: &next,
		Hunks: []Hunk{{Start: 0, End: 0, Text: "y"}}})
	if !res.OK || len(res.Warnings) != 0 {
		t.Errorf("clean apply = %+v, want no warnings", res)
	}
}

// A successful patch over another writer Proposed span carries the superseded
// set in the reply warnings, exactly as apply does, so a Dispatch caller can
// tell a clean patch, a patch with a warning, and a refusal apart. Modelled on
// TestDispatchApplyCarriesWarnings, the apply half.
func TestDispatchPatchCarriesWarnings(t *testing.T) {
	g, h := guarded(t)
	path := filepath.Join(h.root, "a.go")
	g.setClaims(FirstAgent, []string{path})
	h.docs[path] = "hello world\n"
	h.vers[path] = 1
	h.warnings = []GroupOverlap{{Group: 7, Author: 3, Start: 6, End: 12}}

	dumped := Dispatch(g, Request{Op: "dump", Path: path, Author: FirstAgent})
	if !dumped.OK || dumped.DumpID == 0 {
		t.Fatalf("dump = %+v", dumped)
	}
	res := Dispatch(g, Request{Op: "patch", Path: path, Author: FirstAgent,
		DumpID: dumped.DumpID, PatchText: "hello there\n"})
	if !res.OK || len(res.Conflicts) != 0 {
		t.Fatalf("patch = %+v, want it to land cleanly", res)
	}
	if len(res.Warnings) != 1 {
		t.Fatalf("warnings = %+v, want one", res.Warnings)
	}
	if w := res.Warnings[0]; w.Group != 7 || w.Author != 3 || w.Start != 6 || w.End != 12 {
		t.Errorf("warning = %+v, want set 7 by author 3 at 6..12", w)
	}

	// A clean patch with no canned warnings reports none.
	h.warnings = nil
	res = Dispatch(g, Request{Op: "patch", Path: path, Author: FirstAgent,
		DumpID: dumped.DumpID, PatchText: "hello again\n"})
	if !res.OK || len(res.Warnings) != 0 {
		t.Errorf("clean patch = %+v, want no warnings", res)
	}
}

// `groups` reports an overlap on the set itself, so a reader sees it from the
// listing without a second call, and it survives the wire on that set alone.
func TestDispatchGroupsCarryOverlaps(t *testing.T) {
	g, h := guarded(t)
	path := filepath.Join(h.root, "a.go")
	h.groups = []Group{
		{ID: 4, Path: path, Author: 2, State: "proposed", Ops: 1, Hunks: 1},
		{ID: 5, Path: path, Author: 3, State: "proposed", Ops: 1, Hunks: 1,
			Overlaps: &GroupOverlaps{Sets: []GroupOverlap{{Group: 4, Author: 2, Start: 6, End: 11}}}},
	}
	res := Dispatch(g, Request{Op: "groups", Path: path})
	if len(res.Groups) != 2 || res.Groups[0].Overlaps != nil {
		t.Fatalf("groups = %+v", res.Groups)
	}
	ov := res.Groups[1].Overlaps
	if ov == nil || len(ov.Sets) != 1 || ov.Sets[0].Group != 4 || ov.Sets[0].Author != 2 ||
		ov.Sets[0].Start != 6 || ov.Sets[0].End != 11 {
		t.Errorf("overlaps = %+v, want set 4 by author 2 at 6..11", ov)
	}

	got, err := decodeHeader(encodeHeader(Header{Groups: res.Groups}))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Groups) != 2 || got.Groups[0].Overlaps != nil ||
		got.Groups[1].Overlaps == nil || got.Groups[1].Overlaps.Sets[0].Group != 4 {
		t.Errorf("wire groups = %+v, want the overlap kept on set 5 only", got.Groups)
	}
}

// `diff` carries the same overlap through its nested JSON, so the review
// surface shows it too.
func TestDispatchDiffCarriesOverlaps(t *testing.T) {
	g, h := guarded(t)
	path := filepath.Join(h.root, "a.go")
	h.diffs = []DiffGroup{{Group: Group{ID: 5, Path: path, Author: 3, State: "proposed",
		Ops: 1, Overlaps: &GroupOverlaps{Sets: []GroupOverlap{{Group: 4, Author: 2, Start: 6, End: 11}}}}}}
	res := Dispatch(g, Request{Op: "diff", Path: path})
	if !res.OK {
		t.Fatalf("diff = %+v", res)
	}
	var diffs []DiffGroup
	if err := json.Unmarshal([]byte(res.DiffJSON), &diffs); err != nil {
		t.Fatal(err)
	}
	if len(diffs) != 1 || diffs[0].Overlaps == nil || diffs[0].Overlaps.Sets[0].Group != 4 {
		t.Errorf("diff = %+v, want the overlap carried", diffs)
	}
}

// open -create is the declaration of intent the spec names: it makes the
// buffer AND extends the caller's claim with it, so a write to the file it
// just created needs no second command. From an empty set this is a set of
// one.
func TestOpenCreateAutoClaims(t *testing.T) {
	g, h := guarded(t)
	path := filepath.Join(h.root, "new.go")

	base := uint64(1)
	if res := Dispatch(g, Request{Op: "apply", Author: FirstAgent, Path: path,
		Base: &base, Hunks: []Hunk{{Start: 0, End: 0, Text: "x"}}}); res.OK ||
		!strings.Contains(res.Err, "claim a file first") {
		t.Fatalf("apply with an empty claim set = %+v, want the claim refusal", res)
	}

	if res := Dispatch(g, Request{Op: "open", Path: path, Author: FirstAgent, Create: true}); !res.OK {
		t.Fatalf("open -create = %+v", res)
	}
	rep := Dispatch(g, Request{Op: "claim", Author: FirstAgent})
	if len(rep.Claims) != 1 || rep.Claims[0] != path {
		t.Fatalf("claim set after open -create = %v, want exactly [%s]", rep.Claims, path)
	}

	// The auto-claim is enough to satisfy the write gate: read, then apply.
	if _, _, _, err := g.Read(path, FirstAgent, -1, -1, 0, 0, false); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := g.Apply(path, FirstAgent, h.vers[path], []Hunk{{Start: 0, End: 0, Text: "x"}}); err != nil {
		t.Fatalf("apply after open -create was refused: %v", err)
	}
}

// open -create is additive, like claim -add: a standing set grows by the new
// file rather than being replaced by it.
func TestOpenCreateExtendsAnExistingClaim(t *testing.T) {
	g, h := guarded(t)
	first := filepath.Join(h.root, "a.go")
	second := filepath.Join(h.root, "b.go")
	g.setClaims(FirstAgent, []string{first})

	if res := Dispatch(g, Request{Op: "open", Path: second, Author: FirstAgent, Create: true}); !res.OK {
		t.Fatalf("open -create = %+v", res)
	}
	rep := Dispatch(g, Request{Op: "claim", Author: FirstAgent})
	if len(rep.Claims) != 2 || rep.Claims[0] != first || rep.Claims[1] != second {
		t.Fatalf("claim set = %v, want [%s %s]", rep.Claims, first, second)
	}
}

// A plain open is not a declaration of intent: it must leave the claim set
// exactly as it found it, empty or not.
func TestOpenWithoutCreateLeavesTheClaimSetAlone(t *testing.T) {
	g, h := guarded(t)
	path := filepath.Join(h.root, "a.go")

	if res := Dispatch(g, Request{Op: "open", Path: path, Author: FirstAgent}); !res.OK {
		t.Fatalf("open = %+v", res)
	}
	if rep := Dispatch(g, Request{Op: "claim", Author: FirstAgent}); len(rep.Claims) != 0 {
		t.Fatalf("plain open claimed %v, want none", rep.Claims)
	}

	g.setClaims(FirstAgent, []string{path})
	if res := Dispatch(g, Request{Op: "open", Path: path, Author: FirstAgent}); !res.OK {
		t.Fatalf("open = %+v", res)
	}
	if rep := Dispatch(g, Request{Op: "claim", Author: FirstAgent}); len(rep.Claims) != 1 || rep.Claims[0] != path {
		t.Fatalf("plain open changed the set to %v", rep.Claims)
	}
}

// The auto-claim stores the same canonical spelling claim does, so a relative
// open and an absolute one are one entry, not two aliases of the same file.
func TestOpenCreateClaimUsesTheSameSpellingAsClaim(t *testing.T) {
	g, _, p := claimGuard(t, "new.go")
	abs := p["new.go"]
	rel := "new.go"

	if res := Dispatch(g, Request{Op: "open", Path: rel, Author: FirstAgent, Create: true}); !res.OK {
		t.Fatalf("open -create relative = %+v", res)
	}
	if res := Dispatch(g, Request{Op: "open", Path: abs, Author: FirstAgent, Create: true}); !res.OK {
		t.Fatalf("open -create absolute = %+v", res)
	}
	rep := Dispatch(g, Request{Op: "claim", Author: FirstAgent})
	if len(rep.Claims) != 1 || rep.Claims[0] != abs {
		t.Fatalf("claim set = %v, want exactly [%s]", rep.Claims, abs)
	}

	// claim names the same file the same way, so the verbs agree.
	rep = Dispatch(g, Request{Op: "claim", Author: FirstAgent, Paths: []string{rel}})
	if len(rep.Claims) != 1 || rep.Claims[0] != abs {
		t.Fatalf("claim of the relative name = %v, want [%s]", rep.Claims, abs)
	}
}

// The claim gate on the write verbs. n == 0 refuses every shape; n == 1 resolves
// a pathless write to the sole claim; n > 1 refuses a pathless one and names
// the set for an explicit miss. Each message is asserted exactly because the
// CLI and the docs quote it.
func TestClaimGateOnWrites(t *testing.T) {
	g, h, p := claimGuard(t, "a.go", "b.go", "c.go")
	a, b, c := p["a.go"], p["b.go"], p["c.go"]
	for _, path := range []string{a, b, c} {
		h.docs[path], h.vers[path] = "hello", 1
	}
	base := uint64(1)
	oneIn := []Hunk{{Start: 0, End: 0, Text: "x"}}

	// n == 0: no claim, no write, and the buffer is untouched.
	if res := Dispatch(g, Request{Op: "apply", Path: a, Author: FirstAgent, Base: &base, Hunks: oneIn}); res.OK || res.Err != "claim a file first" {
		t.Errorf("explicit write with no claim = %+v, want the strict refusal", res)
	}
	if res := Dispatch(g, Request{Op: "apply", Author: FirstAgent, Base: &base, Hunks: oneIn}); res.OK || res.Err != "claim a file first" {
		t.Errorf("pathless write with no claim = %+v, want the strict refusal", res)
	}
	if res := Dispatch(g, Request{Op: "patch", Path: a, Author: FirstAgent, DumpID: 1, PatchText: "x"}); res.OK || res.Err != "claim a file first" {
		t.Errorf("patch with no claim = %+v, want the strict refusal", res)
	}
	if h.docs[a] != "hello" {
		t.Fatalf("a refused write changed the buffer: %q", h.docs[a])
	}

	// n == 1: a pathless write targets the sole claim, an explicit write for it
	// is allowed, and an explicit write for another file names the set.
	g.setClaims(FirstAgent, []string{a})
	g.markRead(FirstAgent, a)
	if _, _, _, err := g.Apply("", FirstAgent, base, oneIn); err != nil {
		t.Errorf("pathless write with one claim was refused: %v", err)
	}
	if _, _, _, err := g.Apply(a, FirstAgent, base, oneIn); err != nil {
		t.Errorf("explicit write for the claim was refused: %v", err)
	}
	wantNotIn := "not in your claim set (" + a + "); claim -add <path>"
	if _, _, _, err := g.Apply(b, FirstAgent, base, oneIn); err == nil || err.Error() != wantNotIn {
		t.Errorf("explicit write for another file = %v, want %q", err, wantNotIn)
	}

	// n > 1: a pathless write is refused; explicit-in-set is allowed and
	// explicit-out names the whole set in stable order.
	g.setClaims(FirstAgent, []string{b, a})
	g.markRead(FirstAgent, a)
	g.markRead(FirstAgent, b)
	if _, _, _, err := g.Apply("", FirstAgent, base, oneIn); err == nil || err.Error() != "claim set has 2 files; name one" {
		t.Errorf("pathless write with two claims = %v, want the set-size refusal", err)
	}
	if _, _, _, err := g.Apply(a, FirstAgent, base, oneIn); err != nil {
		t.Errorf("explicit in-set write was refused: %v", err)
	}
	wantNotIn = "not in your claim set (" + a + ", " + b + "); claim -add <path>"
	if _, _, _, err := g.Apply(c, FirstAgent, base, oneIn); err == nil || err.Error() != wantNotIn {
		t.Errorf("explicit out-of-set write = %v, want %q", err, wantNotIn)
	}
}

// Reads never consult a claim: a pathless read keeps the focused-buffer default,
// and an unclaimed path still resolves and records read-before-write.
func TestClaimGateLeavesReadsAlone(t *testing.T) {
	g, h := guarded(t)
	path := filepath.Join(h.root, "a.go")
	if _, _, _, err := g.Read(path, FirstAgent, -1, -1, 0, 0, false); err != nil {
		t.Fatalf("read of an unclaimed path was refused: %v", err)
	}
	if _, _, _, err := g.Read("", FirstAgent, -1, -1, 0, 0, false); err != nil {
		t.Fatalf("pathless read was refused: %v", err)
	}
	if _, err := g.Version(path, FirstAgent); err != nil {
		t.Errorf("version of an unclaimed path was refused: %v", err)
	}
	if _, err := g.Diff(path, FirstAgent); err != nil {
		t.Errorf("diff of an unclaimed path was refused: %v", err)
	}
	if _, _, _, _, err := g.Dump(path, -1, -1, FirstAgent); err != nil {
		t.Errorf("dump of an unclaimed path was refused: %v", err)
	}
}

// A pathless write targets the claim, not the focused tab, so a read of some
// other buffer does not satisfy read-before-write for it.
func TestPathlessClaimWriteStillNeedsARead(t *testing.T) {
	g, h := guarded(t)
	path := filepath.Join(h.root, "a.go")
	other := filepath.Join(h.root, "b.go")
	g.setClaims(FirstAgent, []string{path})
	g.markRead(FirstAgent, other)
	if _, _, _, err := g.Apply("", FirstAgent, 1, []Hunk{{Start: 0, End: 5, Text: "x"}}); err == nil ||
		!strings.Contains(err.Error(), "read the buffer before writing") {
		t.Fatalf("pathless write without reading the claim = %v, want the read gate", err)
	}
	g.markRead(FirstAgent, path)
	if _, _, _, err := g.Apply("", FirstAgent, 1, []Hunk{{Start: 0, End: 5, Text: "x"}}); err != nil {
		t.Fatalf("pathless write after reading the claim was refused: %v", err)
	}
}

// clear reverses text out of the buffer, so it runs the claim gate too.
func TestClaimGateOnClear(t *testing.T) {
	g, h, p := claimGuard(t, "a.go", "b.go")
	a, b := p["a.go"], p["b.go"]
	h.groups = []Group{{ID: 7, Path: a, State: "rejected"}, {ID: 8, Path: b, State: "rejected"}}

	if err := g.Clear(a, FirstAgent, 7); err == nil || err.Error() != "claim a file first" {
		t.Errorf("clear with no claim = %v, want the strict refusal", err)
	}
	g.setClaims(FirstAgent, []string{a})
	if err := g.Clear(b, FirstAgent, 8); err == nil || !strings.Contains(err.Error(), "not in your claim set") {
		t.Errorf("clear of an unclaimed file = %v, want the set refusal", err)
	}
	if err := g.Clear(a, FirstAgent, 7); err != nil {
		t.Errorf("clear of a claimed file was refused: %v", err)
	}
}

// revert reverses text out of the buffer, so it runs the claim gate too. It
// reaches the host with the calling connection's own author, which is the only
// writer it may drop.
func TestClaimGateOnRevert(t *testing.T) {
	g, h, p := claimGuard(t, "a.go", "b.go")
	a, b := p["a.go"], p["b.go"]
	// claimGuard's memHost has no buffers loaded, and Revert — like Apply —
	// refuses a path that is not open, so seed them. The gate under test is the
	// claim, not whether the buffer exists.
	h.docs[a] = "a.go"
	h.docs[b] = "a.go"

	if err := g.Revert(a, FirstAgent); err == nil || err.Error() != "claim a file first" {
		t.Errorf("revert with no claim = %v, want the strict refusal", err)
	}
	g.setClaims(FirstAgent, []string{a})
	if err := g.Revert(b, FirstAgent); err == nil || !strings.Contains(err.Error(), "not in your claim set") {
		t.Errorf("revert of an unclaimed file = %v, want the set refusal", err)
	}
	if err := g.Revert(a, FirstAgent); err != nil {
		t.Errorf("revert of a claimed file was refused: %v", err)
	}
	if len(h.reverts) != 1 || h.reverts[0].path != a || h.reverts[0].author != FirstAgent {
		t.Errorf("reverts = %+v, want the canonical path and author to reach the host", h.reverts)
	}
}

// A revert reaches the host through Dispatch with the request's author, so the
// verb's plumbing and the author it drops are wired end to end.
func TestDispatchRevertReachesTheHost(t *testing.T) {
	g, h := guarded(t)
	path := filepath.Join(h.root, "a.go")
	g.setClaims(FirstAgent, []string{path})

	if res := Dispatch(g, Request{Op: "revert", Path: path, Author: FirstAgent}); !res.OK {
		t.Fatalf("revert = %+v", res)
	}
	if len(h.reverts) != 1 || h.reverts[0].path != path || h.reverts[0].author != FirstAgent {
		t.Errorf("reverts = %+v, want the call to reach the host", h.reverts)
	}
}

// A wedged revert is refused with the live set that overlaps it, in the same
// owner-and-span shape a lease refusal uses, so a driver learns what to clear
// first instead of retrying blind.
func TestDispatchRevertReportsTheBlockingSet(t *testing.T) {
	g, h := guarded(t)
	path := filepath.Join(h.root, "a.go")
	g.setClaims(FirstAgent, []string{path})
	h.revertBlock = Conflict{Group: 9, Author: 3, Start: 6, End: 11}

	res := Dispatch(g, Request{Op: "revert", Path: path, Author: FirstAgent})
	if res.OK {
		t.Fatal("a wedged revert reported success")
	}
	if len(res.Conflicts) != 1 {
		t.Fatalf("conflicts = %+v, want the blocking set", res.Conflicts)
	}
	if c := res.Conflicts[0]; c.Group != 9 || c.Author != 3 || c.Start != 6 || c.End != 11 {
		t.Errorf("conflict = %+v, want the owner and span", c)
	}
}

// delete records a pending deletion for a claimed path and changes nothing on
// disk. It is idempotent: a second proposal, even by another claimant, keeps
// the first proposer so two agents racing to propose one removal do not rewrite
// who asked.
func TestDeleteProposesIdempotentlyAndLists(t *testing.T) {
	g, _, p := claimGuard(t, "a.go", "b.go")
	a, b := p["a.go"], p["b.go"]
	g.setClaims(FirstAgent, []string{a, b})

	if res := Dispatch(g, Request{Op: "delete", Path: a, Author: FirstAgent}); !res.OK {
		t.Fatalf("delete = %+v", res)
	}
	// A second delete of the same path is a no-op and the first author stands.
	other := uint8(FirstAgent + 1)
	g.setClaims(other, []string{a, b})
	if res := Dispatch(g, Request{Op: "delete", Path: a, Author: other}); !res.OK {
		t.Fatalf("idempotent delete = %+v", res)
	}

	list := Dispatch(g, Request{Op: "deletions"})
	if !list.OK || len(list.Deletions) != 1 {
		t.Fatalf("deletions = %+v, want one", list)
	}
	if d := list.Deletions[0]; d.Path != a || d.Author != FirstAgent {
		t.Errorf("pending = %+v, want %s proposed by %d", d, a, FirstAgent)
	}

	// A second path lists too, and the list is in stable path order.
	if res := Dispatch(g, Request{Op: "delete", Path: b, Author: FirstAgent}); !res.OK {
		t.Fatalf("delete b = %+v", res)
	}
	list = Dispatch(g, Request{Op: "deletions"})
	if len(list.Deletions) != 2 || list.Deletions[0].Path != a || list.Deletions[1].Path != b {
		t.Errorf("deletions = %+v, want a then b", list.Deletions)
	}
}

// The proposer withdraws: the entry goes, another writer cannot retract it, and
// withdrawing something that is not pending is a no-op rather than an error.
func TestDeleteWithdrawRemovesProposal(t *testing.T) {
	g, _, p := claimGuard(t, "a.go")
	a := p["a.go"]
	g.setClaims(FirstAgent, []string{a})
	if res := Dispatch(g, Request{Op: "delete", Path: a, Author: FirstAgent}); !res.OK {
		t.Fatalf("delete = %+v", res)
	}

	// Another claimant cannot retract this writer's proposal.
	other := uint8(FirstAgent + 1)
	g.setClaims(other, []string{a})
	if res := Dispatch(g, Request{Op: "delete", Path: a, Author: other, Withdraw: true}); res.OK {
		t.Errorf("withdraw by a non-proposer = %+v, want a refusal", res)
	}

	if res := Dispatch(g, Request{Op: "delete", Path: a, Author: FirstAgent, Withdraw: true}); !res.OK {
		t.Fatalf("withdraw = %+v", res)
	}
	if list := Dispatch(g, Request{Op: "deletions"}); len(list.Deletions) != 0 {
		t.Errorf("after withdraw, deletions = %+v, want none", list.Deletions)
	}
	// Withdrawing again is a no-op, not an error.
	if res := Dispatch(g, Request{Op: "delete", Path: a, Author: FirstAgent, Withdraw: true}); !res.OK {
		t.Errorf("second withdraw = %+v, want a no-op", res)
	}
}

// delete is claim-gated like a text write, and it does not require
// read-before-write: a claimed path with no prior read may be proposed, because
// deleting is not a text write and no offset is at stake.
func TestDeleteRequiresAClaimButNotARead(t *testing.T) {
	g, _, p := claimGuard(t, "a.go", "b.go")
	a, b := p["a.go"], p["b.go"]
	g.setClaims(FirstAgent, []string{a})

	if res := Dispatch(g, Request{Op: "delete", Path: a, Author: FirstAgent}); !res.OK {
		t.Fatalf("delete without a read = %+v", res)
	}
	// The unclaimed path is refused with the set named, like a write.
	if res := Dispatch(g, Request{Op: "delete", Path: b, Author: FirstAgent}); res.OK ||
		!strings.Contains(res.Err, "not in your claim set") {
		t.Errorf("delete of an unclaimed path = %+v, want the set refusal", res)
	}
	// Out of root is refused before the set is consulted.
	if res := Dispatch(g, Request{Op: "delete", Path: "/etc/passwd", Author: FirstAgent}); res.OK ||
		!strings.Contains(res.Err, "outside the workspace") {
		t.Errorf("delete outside the root = %+v, want the root refusal", res)
	}
	// Neither refusal recorded anything.
	if list := Dispatch(g, Request{Op: "deletions"}); len(list.Deletions) != 1 {
		t.Errorf("deletions = %+v, want only the one proposal that landed", list.Deletions)
	}
	// An empty set says what to do first, exactly as a write does.
	g.clearClaims(FirstAgent)
	if res := Dispatch(g, Request{Op: "delete", Path: a, Author: FirstAgent}); res.Err != "claim a file first" {
		t.Errorf("delete with no claim = %+v, want the strict refusal", res)
	}
}

// rmdir records a pending dir-removal for a claimed directory and changes
// nothing on disk. It is idempotent: a second proposal, even by another
// claimant, keeps the first proposer.
func TestRmdirProposesIdempotentlyAndLists(t *testing.T) {
	g, h := guarded(t)
	dir := filepath.Join(h.root, "pkg")
	g.setClaims(FirstAgent, []string{dir})

	if res := Dispatch(g, Request{Op: "rmdir", Path: dir, Author: FirstAgent}); !res.OK {
		t.Fatalf("rmdir = %+v", res)
	}
	// A second rmdir of the same path is a no-op and the first author stands.
	other := uint8(FirstAgent + 1)
	g.setClaims(other, []string{dir})
	if res := Dispatch(g, Request{Op: "rmdir", Path: dir, Author: other}); !res.OK {
		t.Fatalf("idempotent rmdir = %+v", res)
	}

	list := Dispatch(g, Request{Op: "rmdirs"})
	if !list.OK || len(list.DirRemovals) != 1 {
		t.Fatalf("rmdirs = %+v, want one", list)
	}
	if d := list.DirRemovals[0]; d.Path != dir || d.Author != FirstAgent {
		t.Errorf("pending = %+v, want %s proposed by %d", d, dir, FirstAgent)
	}

	// A second directory lists too, and the list is in stable path order.
	dir2 := filepath.Join(h.root, "pkg2")
	g.setClaims(FirstAgent, []string{dir2})
	if res := Dispatch(g, Request{Op: "rmdir", Path: dir2, Author: FirstAgent}); !res.OK {
		t.Fatalf("rmdir dir2 = %+v", res)
	}
	list = Dispatch(g, Request{Op: "rmdirs"})
	if len(list.DirRemovals) != 2 || list.DirRemovals[0].Path != dir || list.DirRemovals[1].Path != dir2 {
		t.Errorf("rmdirs = %+v, want pkg then pkg2", list.DirRemovals)
	}
}

// The proposer withdraws; another writer cannot retract it, and withdrawing
// something that is not pending is a no-op rather than an error.
func TestRmdirWithdrawRemovesProposal(t *testing.T) {
	g, h := guarded(t)
	dir := filepath.Join(h.root, "pkg")
	g.setClaims(FirstAgent, []string{dir})
	if res := Dispatch(g, Request{Op: "rmdir", Path: dir, Author: FirstAgent}); !res.OK {
		t.Fatalf("rmdir = %+v", res)
	}

	other := uint8(FirstAgent + 1)
	g.setClaims(other, []string{dir})
	if res := Dispatch(g, Request{Op: "rmdir", Path: dir, Author: other, Withdraw: true}); res.OK {
		t.Errorf("withdraw by a non-proposer = %+v, want a refusal", res)
	}

	if res := Dispatch(g, Request{Op: "rmdir", Path: dir, Author: FirstAgent, Withdraw: true}); !res.OK {
		t.Fatalf("withdraw = %+v", res)
	}
	if list := Dispatch(g, Request{Op: "rmdirs"}); len(list.DirRemovals) != 0 {
		t.Errorf("after withdraw, rmdirs = %+v, want none", list.DirRemovals)
	}
	if res := Dispatch(g, Request{Op: "rmdir", Path: dir, Author: FirstAgent, Withdraw: true}); !res.OK {
		t.Errorf("second withdraw = %+v, want a no-op", res)
	}
}

// A directory is not a file: delete refuses the operand and records nothing, so
// the pending-deletion list cannot claim a directory was going to be unlinked.
func TestDeleteRefusesADirectory(t *testing.T) {
	g, h, _ := claimGuard(t, "a.go")
	dir := filepath.Join(h.root, "pkg")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	g.setClaims(FirstAgent, []string{dir})

	res := Dispatch(g, Request{Op: "delete", Path: dir, Author: FirstAgent})
	if res.OK || !strings.Contains(res.Err, "directory") {
		t.Fatalf("delete of a directory = %+v, want a kind refusal", res)
	}
	if list := Dispatch(g, Request{Op: "deletions"}); len(list.Deletions) != 0 {
		t.Errorf("deletions = %+v, want none after the refusal", list.Deletions)
	}
}

// A file is not a directory: rmdir refuses the operand, so the pending
// dir-removal list cannot name a file as a directory to remove.
func TestRmdirRefusesAFile(t *testing.T) {
	g, _, p := claimGuard(t, "a.go")
	a := p["a.go"]
	g.setClaims(FirstAgent, []string{a})

	res := Dispatch(g, Request{Op: "rmdir", Path: a, Author: FirstAgent})
	if res.OK || !strings.Contains(res.Err, "not a directory") {
		t.Fatalf("rmdir of a file = %+v, want a kind refusal", res)
	}
	if list := Dispatch(g, Request{Op: "rmdirs"}); len(list.DirRemovals) != 0 {
		t.Errorf("rmdirs = %+v, want none after the refusal", list.DirRemovals)
	}
}

// A path that is not on disk has no kind to check yet. A proposal is
// forward-looking, so both delete and rmdir accept it: the claim set is what
// says the writer may name it, and the human prompt is the real gate.
func TestDeleteAndRmdirAllowANotYetExistingPath(t *testing.T) {
	g, _, _ := claimGuard(t, "a.go")
	missingFile := filepath.Join(g.Root(), "gone.go")
	g.setClaims(FirstAgent, []string{missingFile})
	if res := Dispatch(g, Request{Op: "delete", Path: missingFile, Author: FirstAgent}); !res.OK {
		t.Fatalf("delete of a forward path = %+v, want it allowed", res)
	}

	missingDir := filepath.Join(g.Root(), "gone")
	g.setClaims(FirstAgent, []string{missingDir})
	if res := Dispatch(g, Request{Op: "rmdir", Path: missingDir, Author: FirstAgent}); !res.OK {
		t.Fatalf("rmdir of a forward path = %+v, want it allowed", res)
	}
}

// Only the proposing path checks the kind: a proposal must stay retractable
// after the file it named is gone.
func TestWithdrawSkipsTheKindCheck(t *testing.T) {
	g, _, p := claimGuard(t, "a.go")
	a := p["a.go"]
	g.setClaims(FirstAgent, []string{a})
	if res := Dispatch(g, Request{Op: "delete", Path: a, Author: FirstAgent}); !res.OK {
		t.Fatalf("delete = %+v", res)
	}
	if err := os.Remove(a); err != nil {
		t.Fatal(err)
	}
	if res := Dispatch(g, Request{Op: "delete", Path: a, Author: FirstAgent, Withdraw: true}); !res.OK {
		t.Fatalf("withdraw after removal = %+v, want a no-op", res)
	}
}

// proposals is one flat list over the three pending kinds: a change set, a
// file deletion and a dir-removal come back tagged and in kind order, whatever
// order the host handed them over in.
func TestProposalsRollsUpEveryKind(t *testing.T) {
	g, h := guarded(t)
	h.proposals = []Proposal{
		{Kind: "rmdir", Path: "/w/sub", Author: 3, Start: -1, End: -1},
		{Kind: "set", Path: "/w/a.go", Author: 2, Group: 5, Start: 1, End: 4},
		{Kind: "delete", Path: "/w/b.go", Author: 4, Start: -1, End: -1},
	}
	list := Dispatch(g, Request{Op: "proposals"})
	if !list.OK {
		t.Fatalf("proposals = %+v", list)
	}
	want := []Proposal{
		{Kind: "set", Path: "/w/a.go", Author: 2, Group: 5, Start: 1, End: 4},
		{Kind: "delete", Path: "/w/b.go", Author: 4, Start: -1, End: -1},
		{Kind: "rmdir", Path: "/w/sub", Author: 3, Start: -1, End: -1},
	}
	if len(list.Proposals) != len(want) {
		t.Fatalf("proposals = %+v, want %+v", list.Proposals, want)
	}
	for i := range want {
		if list.Proposals[i] != want[i] {
			t.Errorf("proposal[%d] = %+v, want %+v", i, list.Proposals[i], want[i])
		}
	}
}

// rmdir is claim-gated on the directory itself, like delete is on the file, and
// it does not require read-before-write: a claimed directory with no prior read
// may be proposed, because a dir-removal is not a text write and no offset is
// at stake.
func TestRmdirRequiresAClaimButNotARead(t *testing.T) {
	g, h := guarded(t)
	dir := filepath.Join(h.root, "pkg")
	other := filepath.Join(h.root, "other")
	g.setClaims(FirstAgent, []string{dir})

	if res := Dispatch(g, Request{Op: "rmdir", Path: dir, Author: FirstAgent}); !res.OK {
		t.Fatalf("rmdir without a read = %+v", res)
	}
	if res := Dispatch(g, Request{Op: "rmdir", Path: other, Author: FirstAgent}); res.OK ||
		!strings.Contains(res.Err, "not in your claim set") {
		t.Errorf("rmdir of an unclaimed dir = %+v, want the set refusal", res)
	}
	if res := Dispatch(g, Request{Op: "rmdir", Path: "/etc", Author: FirstAgent}); res.OK ||
		!strings.Contains(res.Err, "outside the workspace") {
		t.Errorf("rmdir outside the root = %+v, want the root refusal", res)
	}
	if list := Dispatch(g, Request{Op: "rmdirs"}); len(list.DirRemovals) != 1 {
		t.Errorf("rmdirs = %+v, want only the one proposal that landed", list.DirRemovals)
	}
	g.clearClaims(FirstAgent)
	if res := Dispatch(g, Request{Op: "rmdir", Path: dir, Author: FirstAgent}); res.Err != "claim a file first" {
		t.Errorf("rmdir with no claim = %+v, want the strict refusal", res)
	}
}

// claim <dir> records the directory itself as a set entry and walks the subtree
// to claim each regular file under it — a snapshot, so a file created
// afterwards is not auto-claimed. Subdirectories are walked but not claimed,
// and symlinks are not followed.
func TestClaimDirExpandsToDirAndFiles(t *testing.T) {
	root := t.TempDir()
	h := newMemHost(root, nil)
	g := NewGuard(h)
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(filepath.Join(sub, "inner"), 0o755); err != nil {
		t.Fatal(err)
	}
	inner := filepath.Join(sub, "inner", "b.go")
	for _, f := range []string{filepath.Join(sub, "a.go"), inner} {
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	res := Dispatch(g, Request{Op: "claim", Author: FirstAgent, Paths: []string{sub}})
	if !res.OK {
		t.Fatalf("claim dir = %+v", res)
	}
	want := []string{sub, filepath.Join(sub, "a.go"), inner}
	if got := strings.Join(res.Claims, ","); got != strings.Join(want, ",") {
		t.Errorf("claims = %v, want the dir plus each file %v", res.Claims, want)
	}
}

// The opcode path is not a second write surface: a program compiles to the same
// Request Dispatch gates, so an apply in a batch cannot bypass a claim.
func TestProgrammaticApplyCannotBypassClaims(t *testing.T) {
	g, h, p := claimGuard(t, "a.go")
	a := p["a.go"]
	h.docs[a], h.vers[a] = "hello", 1

	program := prog.Encode([]prog.Op{
		{Code: prog.OpPath, Payload: []byte(a)},
		{Code: prog.OpBase, Payload: prog.Number(1)},
		{Code: prog.OpSpan, Payload: prog.Pair(0, 5)},
		{Code: prog.OpText, Payload: []byte("howdy")},
		{Code: prog.OpApply},
	})
	reqs, err := Requests(program, FirstAgent)
	if err != nil {
		t.Fatalf("program did not compile: %v", err)
	}
	if len(reqs) != 1 || reqs[0].Op != "apply" {
		t.Fatalf("compiled %+v, want one apply", reqs)
	}
	if res := Dispatch(g, reqs[0]); res.OK || res.Err != "claim a file first" {
		t.Fatalf("programmatic apply with no claim = %+v, want the claim refusal", res)
	}
	if h.docs[a] != "hello" {
		t.Fatalf("the refused program changed the buffer: %q", h.docs[a])
	}

	// Claim and read, and the same opcode path lands.
	g.setClaims(FirstAgent, []string{a})
	g.markRead(FirstAgent, a)
	if res := Dispatch(g, reqs[0]); !res.OK {
		t.Fatalf("programmatic apply after claiming = %+v", res)
	}
	if h.docs[a] != "howdy" {
		t.Errorf("buffer = %q, want the program applied", h.docs[a])
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

// review returns the pending sets and, in its plain form, enters the mode. The
// set list is the pending projection `groups` reports filtered to the sets
// still awaiting a decision; -json (ReviewList) returns them without entering.
// The memHost fake has no app mode, so what this checks is the answer shape and
// that both forms reach the host.
func TestDispatchReview(t *testing.T) {
	g, h := guarded(t)
	path := filepath.Join(h.root, "a.go")
	h.groups = []Group{
		{ID: 7, Path: path, Author: FirstAgent, State: "proposed", Ops: 1, Bytes: 1},
		{ID: 9, Path: path, Author: FirstAgent, State: "accepted", Ops: 1, Bytes: 1},
	}

	res := Dispatch(g, Request{Op: "review", Path: path, ReviewList: true})
	if !res.OK {
		t.Fatalf("review -json = %+v", res)
	}
	if len(res.Groups) != 1 || res.Groups[0].ID != 7 {
		t.Errorf("review groups = %+v, want only the still-proposed set 7", res.Groups)
	}

	// The plain form reports the same list; entering the mode is the app's
	// business and its absence here is what memHost models.
	res = Dispatch(g, Request{Op: "review", Path: path})
	if !res.OK || len(res.Groups) != 1 {
		t.Errorf("review = %+v, want the same one proposed set", res)
	}
}

// A dump hands back a whole chunk and an id; a patch returns the edited whole
// text and the editor diffs and applies it, so a driver never derives offsets
// nor matches old text.
func TestDumpAndPatchRoundTrip(t *testing.T) {
	g, h := guarded(t)
	path := filepath.Join(h.root, "a.go")
	g.setClaims(FirstAgent, []string{path})
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

// An inlay-hints request reaches the host's range variant with the lines the
// request carried, and its caller's canned answer comes back as LSPJSON. The
// real conversion to editor coordinates is exercised on the editor's machine,
// where a language server can run.
func TestDispatchLSPInlayHints(t *testing.T) {
	g, h := guarded(t)
	path := filepath.Join(h.root, "a.go")
	h.hintsJSON = `{"hints":[{"line":2,"col":5,"text":"string","kind":1,"tooltip":"inferred type"}]}`
	a, b := 3, 9
	res := Dispatch(g, Request{Op: "lspprep", Path: path, LSPMode: "inlay-hints",
		LineStart: &a, LineEnd: &b})
	if !res.OK || res.LSP == nil {
		t.Fatalf("lspprep inlay-hints = %+v", res)
	}
	if h.hintLines != [2]int{3, 9} {
		t.Errorf("host saw lines %v, want [3 9]", h.hintLines)
	}
	data, err := res.LSP.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out LSPResult
	if uerr := json.Unmarshal(data, &out); uerr != nil {
		t.Fatalf("answer is not JSON: %v", uerr)
	}
	if len(out.Hints) != 1 || out.Hints[0].Line != 2 || out.Hints[0].Col != 5 ||
		out.Hints[0].Text != "string" || out.Hints[0].Kind != 1 ||
		out.Hints[0].Tooltip != "inferred type" {
		t.Errorf("hints = %+v", out.Hints)
	}
}

// A formatting request reaches the host's formatting variant with the lines the
// request carried, and its caller's canned answer comes back as LSPJSON. The
// real decode of the server's TextEdits is exercised in internal/lsp, and the
// application in internal/app.
func TestDispatchLSPFormat(t *testing.T) {
	g, h := guarded(t)
	path := filepath.Join(h.root, "a.go")
	h.formatJSON = `{"edits":[{"line":1,"col":1,"endLine":1,"endCol":3,"text":"  "}]}`
	res := Dispatch(g, Request{Op: "lspprep", Path: path, LSPMode: "format"})
	if !res.OK || res.LSP == nil {
		t.Fatalf("lspprep format = %+v", res)
	}
	if h.formatLines != [2]int{0, 0} {
		t.Errorf("host saw lines %v, want whole-document [0 0]", h.formatLines)
	}
	data, err := res.LSP.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out LSPResult
	if uerr := json.Unmarshal(data, &out); uerr != nil {
		t.Fatalf("answer is not JSON: %v", uerr)
	}
	if len(out.Edits) != 1 || out.Edits[0].Line != 1 || out.Edits[0].EndCol != 3 {
		t.Errorf("edits = %+v", out.Edits)
	}

	// The range form carries the named lines through to the host.
	a, b := 4, 7
	res = Dispatch(g, Request{Op: "lspprep", Path: path, LSPMode: "range-format", LineStart: &a, LineEnd: &b})
	if !res.OK || res.LSP == nil {
		t.Fatalf("lspprep range-format = %+v", res)
	}
	if h.formatLines != [2]int{4, 7} {
		t.Errorf("host saw lines %v, want [4 7]", h.formatLines)
	}
}

// A workspace-symbol request reaches the host's query variant with the query
// the request carried, and its caller's canned answer comes back as LSPJSON.
// The server's own symbol shapes are decoded in internal/lsp and the
// application of the picker in internal/app.
func TestDispatchLSPWorkspaceSymbols(t *testing.T) {
	g, h := guarded(t)
	path := filepath.Join(h.root, "a.go")
	h.symbolsJSON = `{"symbols":[{"name":"Reader","kind":"interface","path":"/w/a.go","line":1,"col":6}]}`
	res := Dispatch(g, Request{Op: "lspprep", Path: path, LSPMode: "symbols", Query: &SearchQuery{Text: "Read"}})
	if !res.OK || res.LSP == nil {
		t.Fatalf("lspprep symbols = %+v", res)
	}
	if h.symbolsQuery != "Read" {
		t.Errorf("host saw query %q, want Read", h.symbolsQuery)
	}
	data, err := res.LSP.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out LSPResult
	if uerr := json.Unmarshal(data, &out); uerr != nil {
		t.Fatalf("answer is not JSON: %v", uerr)
	}
	if len(out.Symbols) != 1 || out.Symbols[0].Name != "Reader" || out.Symbols[0].Line != 1 {
		t.Errorf("symbols = %+v", out.Symbols)
	}

	// An empty query rides the wire as no query at all, so the host sees the
	// same empty string the CLI sends when the caller names none.
	res = Dispatch(g, Request{Op: "lspprep", Path: path, LSPMode: "symbols"})
	if !res.OK || res.LSP == nil {
		t.Fatalf("lspprep symbols (no query) = %+v", res)
	}
	if h.symbolsQuery != "" {
		t.Errorf("host saw query %q, want empty", h.symbolsQuery)
	}
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

// applyDiffHunks applies hunks the way Patch does, back to front so a hunk's
// offsets never move under one already applied.
func applyDiffHunks(old string, hunks []Hunk) string {
	for i := len(hunks) - 1; i >= 0; i-- {
		h := hunks[i]
		old = old[:h.Start] + h.Text + old[h.End:]
	}
	return old
}

// TestDiffLinesRoundTrip is the contract in both directions: applying the diff
// to one side must yield the other byte for byte, and the hunks must be
// ordered and non-overlapping so back-to-front apply is defined. It covers the
// ends the single-direction test above does not: no trailing newline on either
// side, a newline added or removed, blank lines, and empty inputs.
func TestDiffLinesRoundTrip(t *testing.T) {
	shapes := []struct{ a, b string }{
		{"", ""},
		{"", "x\n"},
		{"x\n", ""},
		{"a\nb\nc\n", "a\nB\nc\n"},
		{"a\nb\nc\n", "a\nX\nY\nc\n"},
		{"a\nb\nc", "a\nB\nc"},
		{"a\nb\nc", "a\nb\nc\n"},
		{"a\nb\nc\n", "a\nb\nc"},
		{"a\n\nb\n", "a\nb\n"},
		{"a\nb\n", "a\n\nb\n"},
		{"a\nb\nc\nd\ne\n", "a\nX\nc\nY\ne\n"},
		{"x\nx\nx\n", "x\nY\nx\n"},
		{"a\nb\nc\n", "c\nb\na\n"},
		{"one\ntwo\nthree\n", "one\nthree\n"},
		{"same\nsame\n", "same\nsame\n"},
	}
	for _, tc := range shapes {
		fwd := DiffLines(tc.a, tc.b)
		if got := applyDiffHunks(tc.a, fwd); got != tc.b {
			t.Errorf("DiffLines(%q, %q) applied = %q, want %q (hunks %+v)", tc.a, tc.b, got, tc.b, fwd)
		}
		rev := DiffLines(tc.b, tc.a)
		if got := applyDiffHunks(tc.b, rev); got != tc.a {
			t.Errorf("DiffLines(%q, %q) applied = %q, want %q (hunks %+v)", tc.b, tc.a, got, tc.a, rev)
		}
		for i, h := range fwd {
			if h.Start > h.End {
				t.Errorf("DiffLines(%q, %q): hunk %d starts at %d past its end %d", tc.a, tc.b, i, h.Start, h.End)
			}
			if i > 0 && fwd[i-1].End > h.Start {
				t.Errorf("DiffLines(%q, %q): hunk %d ends at %d, hunk %d starts at %d",
					tc.a, tc.b, i-1, fwd[i-1].End, i, h.Start)
			}
		}
	}
}

// TestDiffLinesLargeFile is the regression for the old n*m cap. Past a million
// line pairs DiffLines used to give up and return one hunk covering the whole
// text, so a two-thousand-line dump with three edits reviewed as an
// unreviewable wall and its lease blocked every other writer. The same input
// must now come back as the three small hunks the edits actually were.
func TestDiffLinesLargeFile(t *testing.T) {
	const lines = 2000
	body := func(i int) string {
		return fmt.Sprintf("line %04d: the quick brown fox jumps over the lazy dog\n", i)
	}
	var old strings.Builder
	for i := 0; i < lines; i++ {
		old.WriteString(body(i))
	}
	base := old.String()
	if got := strings.Count(base, "\n"); got < 1700 {
		t.Fatalf("test file has %d lines, want at least 1700 to exercise the old cap", got)
	}

	var edited strings.Builder
	for i := 0; i < lines; i++ {
		switch i {
		case 137: // modify one line
			edited.WriteString("line 0137: THE QUICK BROWN FOX\n")
		case 901: // keep the line, then insert another after it
			edited.WriteString(body(i))
			edited.WriteString("line 0901b: a new line\n")
		case 1655: // delete one line
		default:
			edited.WriteString(body(i))
		}
	}
	want := edited.String()
	if base == want {
		t.Fatal("the test edits did not change the text")
	}

	hunks := DiffLines(base, want)
	if len(hunks) != 3 {
		t.Fatalf("DiffLines over %d lines with three edits = %d hunks (%+v), want 3", lines, len(hunks), hunks)
	}
	for i, h := range hunks {
		if h.Start == 0 && h.End == len(base) {
			t.Fatalf("hunk %d covers the whole file: %+v", i, h)
		}
		if h.End-h.Start > 512 {
			t.Errorf("hunk %d replaces %d bytes, want a bounded region: %+v", i, h.End-h.Start, h)
		}
	}
	if got := applyDiffHunks(base, hunks); got != want {
		t.Errorf("applying the hunks did not reconstruct the new text (got %d bytes, want %d)", len(got), len(want))
	}
}

// TestDiffFallback pins the work-ceiling decision without building a region
// large enough to trip it. diffFallback is the seam that decides: it charges
// the region to the shared budget and reports when to stop splitting.
func TestDiffFallback(t *testing.T) {
	remaining := 100
	if diffFallback(&remaining, 0, 10, 0, 10) {
		t.Fatal("a small region with budget to spare fell back")
	}
	if remaining != 80 {
		t.Errorf("remaining after a 10+10 region = %d, want 80", remaining)
	}
	if !diffFallback(&remaining, 0, 90, 0, 90) {
		t.Error("a region that exhausts the budget did not fall back")
	}
	if remaining >= 0 {
		t.Errorf("remaining after an overspend = %d, want negative", remaining)
	}
	if !diffFallback(&remaining, 0, 1, 0, 1) {
		t.Error("a tiny region after the budget was spent did not fall back")
	}

	remaining = 1 << 20
	if !diffFallback(&remaining, 0, diffRegionCeiling+1, 0, diffRegionCeiling+1) {
		t.Error("a region past the region ceiling did not fall back")
	}
	remaining = 1 << 20
	if diffFallback(&remaining, 0, diffRegionCeiling+1, 0, 1) {
		t.Error("a region huge on one side only fell back on the region ceiling")
	}
}

// TestDiffLinesBudget drives the fallback through the diffLines test seam.
// With a zero budget the first region that would need anchors collapses to one
// hunk, while the same input with budget to spare splits at its anchors; the
// text is identical either way. Without the diffFallback check in diffRegion,
// the zero budget would be ignored and both runs would return the split hunks.
func TestDiffLinesBudget(t *testing.T) {
	base := "head\nA\nB\nC\nanchor1\nD\nE\nF\nanchor2\nG\nH\nI\ntail\n"
	edited := "head\nA2\nB2\nC2\nanchor1\nD2\nE2\nF2\nanchor2\nG2\nH2\nI2\ntail\n"

	coarse := diffLines(base, edited, 0)
	if len(coarse) != 1 {
		t.Fatalf("budget 0 diff = %d hunks (%+v), want one coarse hunk", len(coarse), coarse)
	}
	if got := applyDiffHunks(base, coarse); got != edited {
		t.Errorf("budget 0 diff applied = %q, want %q", got, edited)
	}

	fine := diffLines(base, edited, diffWorkCeiling)
	if len(fine) <= len(coarse) {
		t.Errorf("full-budget diff = %d hunks, want more than the coarse %d", len(fine), len(coarse))
	}
	if got := applyDiffHunks(base, fine); got != edited {
		t.Errorf("full-budget diff applied = %q, want %q", got, edited)
	}
}

// TestDiffLinesAdversarial runs the diff over two large pathological shapes.
// The first is anchor-heavy and splits into thousands of fine hunks: every
// line is unique, so uniqueAnchors always pairs them, and the new text is the
// evens followed by the odds, so their order disagrees and the recursion has
// to work the whole file. The second is duplicate-heavy and has no line unique
// on both sides, so it takes the existing anchor-free path. Both must
// round-trip, stay ordered and non-overlapping, and come back bounded rather
// than as one hunk per line. The budgeted run through the seam shows the
// fallback bounding the same input; wall-clock cost is not asserted.
func TestDiffLinesAdversarial(t *testing.T) {
	const n = 20000
	body := func(i int) string { return fmt.Sprintf("line %05d: unique payload\n", i) }
	var oldB, newB strings.Builder
	for i := 0; i < n; i++ {
		oldB.WriteString(body(i))
	}
	for i := 0; i < n; i += 2 {
		newB.WriteString(body(i))
	}
	for i := 1; i < n; i += 2 {
		newB.WriteString(body(i))
	}
	base, want := oldB.String(), newB.String()

	check := func(name string, hunks []Hunk) {
		t.Helper()
		if got := applyDiffHunks(base, hunks); got != want {
			t.Errorf("%s: applying %d hunks to %d bytes gave %d bytes, want %d", name, len(hunks), len(base), len(got), len(want))
		}
		for i, h := range hunks {
			if h.Start > h.End {
				t.Errorf("%s: hunk %d starts at %d past its end %d", name, i, h.Start, h.End)
			}
			if i > 0 && hunks[i-1].End > h.Start {
				t.Errorf("%s: hunk %d ends at %d, hunk %d starts at %d", name, i-1, hunks[i-1].End, i, h.Start)
			}
		}
		if len(hunks) > n {
			t.Errorf("%s: %d hunks, want at most %d", name, len(hunks), n)
		}
	}

	fine := DiffLines(base, want)
	if len(fine) < 2 {
		t.Fatalf("anchor-heavy diff = %d hunks, want it to split", len(fine))
	}
	check("anchor-heavy", fine)

	// A budget too small for the whole file must collapse it to one hunk: that
	// is the fallback path running at scale, and it must still round-trip.
	budgeted := diffLines(base, want, 1000)
	if len(budgeted) >= len(fine) {
		t.Errorf("budgeted diff = %d hunks, want fewer than the fine %d", len(budgeted), len(fine))
	}
	check("budgeted", budgeted)

	// Duplicate-heavy text has no line unique on both sides, so uniqueAnchors
	// is empty and the region already collapses; at size it must still
	// round-trip rather than overflow the budget or the stack.
	half := strings.Repeat("repeat me\n", n/2)
	dupOld := half + half
	dupNew := half + "a unique marker\n" + half
	dup := DiffLines(dupOld, dupNew)
	if got := applyDiffHunks(dupOld, dup); got != dupNew {
		t.Errorf("duplicate-heavy diff applied to %d bytes, want %d", len(got), len(dupNew))
	}
}

// The tally is the buffer checking itself: a balanced file is all zero, a
// missing closer reads positive, a stray one negative.
func TestBraceTallyBalance(t *testing.T) {
	if got := braceTally("ok.go", "package p\n\nfunc f() {\n\tg([]int{1})\n}\n"); got != (BraceTally{}) {
		t.Errorf("balanced = %+v, want zero", got)
	}
	missing := braceTally("bad.go", "package p\n\nfunc f() {\n")
	if missing.Braces != 1 {
		t.Errorf("a missing } = %+v, want braces +1", missing)
	}
	stray := braceTally("stray.go", "package p\n}\n")
	if stray.Braces != -1 {
		t.Errorf("a stray } = %+v, want braces -1", stray)
	}
}

// A bracket inside a string or a comment is not structural and does not count,
// through the same ClassAt seam bracket matching uses.
func TestBraceTallyIgnoresStringsAndComments(t *testing.T) {
	src := "package p\n" +
		"var s = \"{([\"\n" + // one double-quoted string, whole line ignored
		"var t = \"}\"\n" + // a closer inside a string, ignore
		"// a comment with { ( [\n" + // line comment, ignore
		"/* a block with } ) ] */\n" + // block comment, ignore
		"func f() {}\n" // the only structural pair
	if got := braceTally("src.go", src); got != (BraceTally{}) {
		t.Errorf("strings and comments counted: %+v, want zero", got)
	}
}

// When the lexer has nothing to say — here an unknown language — every bracket
// counts: plain depth counting, the same degrade as the matcher.
func TestBraceTallyWithoutALexer(t *testing.T) {
	got := braceTally("data.zzz", "{ [] }\n") // no lexer for .zzz
	if got.Braces != 0 || got.Parens != 0 || got.Brackets != 0 {
		t.Errorf("plain counting got %+v, want all balanced", got)
	}
	unbalanced := braceTally("data.zzz", "{\n")
	if unbalanced.Braces != 1 {
		t.Errorf("plain counting of a lone { = %+v, want braces +1", unbalanced)
	}
}

// ls resolves its directory against the workspace root and passes -hidden
// through to the host. It is ungated, like deletions and rmdirs — seeing a
// directory's children needs no claim — but a path outside the workspace is
// still refused.
func TestDispatchLs(t *testing.T) {
	g, h := guarded(t)
	h.entries = []Entry{
		{Name: "a.go", Path: filepath.Join(h.root, "a.go"), Size: i64p(2)},
		{Name: "pkg", Path: filepath.Join(h.root, "pkg"), Dir: true},
	}

	res := Dispatch(g, Request{Op: "ls", Path: "pkg"})
	if !res.OK {
		t.Fatalf("ls = %+v", res)
	}
	if h.lastLs.Path != filepath.Join(h.root, "pkg") {
		t.Errorf("resolved path = %q, want it joined to the root", h.lastLs.Path)
	}
	if h.lastLs.Hidden {
		t.Error("ls without -hidden carried the include-hidden flag")
	}
	if len(res.Entries) != 2 || res.Entries[0].Name != "a.go" || !res.Entries[1].Dir {
		t.Errorf("entries = %+v, want the host's canned list", res.Entries)
	}

	// An empty path names the workspace root, and -hidden rides along.
	if res := Dispatch(g, Request{Op: "ls", Hidden: true}); !res.OK || h.lastLs.Path != h.root || !h.lastLs.Hidden {
		t.Errorf("ls -hidden = %+v, last = %+v; want the root and the flag", res, h.lastLs)
	}

	// A path outside the workspace is refused, the same as every other verb.
	if res := Dispatch(g, Request{Op: "ls", Path: "../elsewhere"}); res.OK || res.Err == "" {
		t.Errorf("ls outside the root = %+v, want a refusal", res)
	}
}

// The -hidden switch on a search runs the inner searcher's include-hidden walk,
// not the ordinary Search: if it were the latter the flag would silently do
// nothing. memHost records which one ran.
func TestGuardDispatchesHiddenSearch(t *testing.T) {
	g, h := guarded(t)
	searcher := g.Snapshot()

	var got []SearchMatch
	if _, _, _, _, err := searcher.Search(context.Background(), SearchQuery{Text: "hello", Hidden: true},
		func(b []SearchMatch) { got = append(got, b...) }); err != nil {
		t.Fatal(err)
	}
	if !h.hiddenSearch {
		t.Error("a -hidden search did not reach the inner include-hidden walk")
	}
	if len(got) == 0 {
		t.Error("the include-hidden walk returned no hits")
	}

	h.hiddenSearch = false
	if _, _, _, _, err := searcher.Search(context.Background(), SearchQuery{Text: "hello"},
		func(b []SearchMatch) { got = append(got, b...) }); err != nil {
		t.Fatal(err)
	}
	if h.hiddenSearch {
		t.Error("an ordinary search took the include-hidden walk")
	}
}

// symlinkEscapeFixture builds a Guard over a temp root and two links inside it
// that name targets outside: a file link and a directory link. It returns the
// guard and the two spellings. The temp-root harness is claimGuard, the same
// one the claim tests use, because the resolved check needs real symlinks.
func symlinkEscapeFixture(t *testing.T) (*Guard, string, string) {
	t.Helper()
	g, _, _ := claimGuard(t, "a.go")
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fileLink := filepath.Join(g.Root(), "escape.txt")
	if err := os.Symlink(secret, fileLink); err != nil {
		t.Fatal(err)
	}
	dirLink := filepath.Join(g.Root(), "escapedir")
	if err := os.Symlink(outside, dirLink); err != nil {
		t.Fatal(err)
	}
	return g, fileLink, dirLink
}

// A symlink inside the workspace that resolves to a target outside it is
// refused before any verb reads or writes the target: the resolved root check
// is the gate the lexical one alone could not provide. Modelled on
// TestGuardRenameRefusesEscape, the same property for a ".." destination.
func TestGuardRefusesSymlinkEscape(t *testing.T) {
	g, fileLink, dirLink := symlinkEscapeFixture(t)
	// Seed a buffer under the spelled link and satisfy the read gate directly,
	// so each assertion below fails only if the resolved symlink check is
	// absent — not because the fake had no buffer or the read-before-write gate
	// fired. Without the check, the read and the apply would both succeed.
	h := g.Host.(*memHost)
	h.docs[fileLink], h.vers[fileLink] = "hello\n", 1
	g.markRead(FirstAgent, fileLink)

	// Read: the canonical seam resolves and refuses before the host is asked.
	if res := Dispatch(g, Request{Op: "text", Path: fileLink, Author: FirstAgent}); res.OK {
		t.Errorf("read through an escaping symlink = %+v, want a refusal", res)
	}
	// Open names a buffer rather than a read of one, but it reaches the file.
	if res := Dispatch(g, Request{Op: "open", Path: fileLink}); res.OK {
		t.Errorf("open of an escaping symlink = %+v, want a refusal", res)
	}
	// Write: claim the spelled link so the claim gate is satisfied; the
	// resolved-root check still stops the write.
	g.setClaims(FirstAgent, []string{fileLink})
	base := uint64(1)
	if res := Dispatch(g, Request{Op: "apply", Path: fileLink, Author: FirstAgent,
		Base: &base, Hunks: []Hunk{{Start: 0, End: 0, Text: "x"}}}); res.OK {
		t.Errorf("write through an escaping symlink = %+v, want a refusal", res)
	}
	// ls reaches the filesystem through the same claimPath seam, so a
	// symlinked directory that leaves the root is refused too.
	if res := Dispatch(g, Request{Op: "ls", Path: dirLink}); res.OK {
		t.Errorf("ls of an escaping symlink = %+v, want a refusal", res)
	}
}

// A symlink that resolves to a file still inside the workspace is not an
// escape: the target is in-root, so read and open keep working. Modelled on
// TestClaimAcceptsANotYetOnDiskPath, the in-root acceptance sibling.
func TestGuardAllowsSymlinkInsideRoot(t *testing.T) {
	g, h, p := claimGuard(t, "a.go")
	link := filepath.Join(g.Root(), "alias.go")
	if err := os.Symlink(p["a.go"], link); err != nil {
		t.Fatal(err)
	}
	// The fixture is a real file on disk; seed the memory host under the
	// spelled link so the read has a buffer to return.
	h.docs[link], h.vers[link] = "hello\n", 1

	if res := Dispatch(g, Request{Op: "text", Path: link, Author: FirstAgent}); !res.OK {
		t.Errorf("read through an in-root symlink = %+v, want it allowed", res)
	}
	if res := Dispatch(g, Request{Op: "open", Path: link}); !res.OK {
		t.Errorf("open of an in-root symlink = %+v, want it allowed", res)
	}
}

// RAJ_ALLOW_SYMLINK_ESCAPE is the opt-in: a true spelling skips the resolved
// half of the check, while the empty and false spellings keep the default that
// refuses the escape. Modelled on the RAJ_TRASH value tables in
// internal/app/deletion_test.go (TestRemoveForeverTrashesWhenEnabled and
// TestRemoveForeverUnlinksWithoutTrash).
func TestGuardSymlinkEscapeOptIn(t *testing.T) {
	for _, val := range []string{"1", "true", "yes"} {
		t.Run("RAJ_ALLOW_SYMLINK_ESCAPE="+val, func(t *testing.T) {
			t.Setenv(SymlinkEscapeEnv, val)
			g, fileLink, _ := symlinkEscapeFixture(t)
			if _, err := g.canonical(fileLink); err != nil {
				t.Errorf("canonical with the opt-in set = %v, want the escape allowed", err)
			}
		})
	}
	for _, val := range []string{"", "0", "false"} {
		t.Run("RAJ_ALLOW_SYMLINK_ESCAPE="+val, func(t *testing.T) {
			t.Setenv(SymlinkEscapeEnv, val)
			g, fileLink, _ := symlinkEscapeFixture(t)
			if _, err := g.canonical(fileLink); err == nil {
				t.Errorf("canonical with %q set = nil, want the escape refused", val)
			}
		})
	}
}

// A path that does not exist yet has no target to resolve, so it is checked
// lexically and stays allowed: open -create and a forward claim keep working.
// Modelled on TestClaimAcceptsANotYetOnDiskPath, the original forwarding case.
func TestGuardAllowsANotYetOnDiskPath(t *testing.T) {
	g, _, _ := claimGuard(t, "a.go")
	missing := filepath.Join(g.Root(), "new", "gone.go")

	if err := g.inRootResolved(missing); err != nil {
		t.Errorf("inRootResolved(%s) = %v, want nil for a create-forward path", missing, err)
	}
	if res := Dispatch(g, Request{Op: "claim", Author: FirstAgent, Paths: []string{missing}}); !res.OK {
		t.Errorf("claim of a not-yet-on-disk path = %+v, want it accepted", res)
	}
	if res := Dispatch(g, Request{Op: "open", Path: missing, Author: FirstAgent, Create: true}); !res.OK {
		t.Errorf("open -create of a not-yet-on-disk path = %+v, want it accepted", res)
	}
}

// A new leaf under a symlinked parent that leaves the workspace is refused:
// the deepest existing ancestor is resolved, so open -create and a forward
// claim cannot write through the link. Modelled on TestGuardRefusesSymlinkEscape
// (the escape property) with the not-yet-on-disk leaf of
// TestGuardAllowsANotYetOnDiskPath.
func TestGuardRefusesSymlinkParentEscape(t *testing.T) {
	g, _, dirLink := symlinkEscapeFixture(t)
	through := filepath.Join(dirLink, "newfile.txt")

	if err := g.inRootResolved(through); err == nil {
		t.Errorf("inRootResolved(%s) = nil, want the symlinked parent refused", through)
	}
	if res := Dispatch(g, Request{Op: "open", Path: through, Author: FirstAgent, Create: true}); res.OK {
		t.Errorf("open -create through a symlinked parent = %+v, want a refusal", res)
	}
	if res := Dispatch(g, Request{Op: "claim", Author: FirstAgent, Paths: []string{through}}); res.OK {
		t.Errorf("claim through a symlinked parent = %+v, want a refusal", res)
	}
}

// A read can name several targets in one call: each path's text and version
// comes back, so a read-read-read chain is one round trip. Buffers names each
// target's path and version and the byte length of its text, and Spans carries
// the concatenation, which is how the read is split back apart. The
// single-target form is unchanged. Modelled on TestDispatchReadsByLines.
func TestDispatchReadMultipleTargets(t *testing.T) {
	g, h := guarded(t)
	a := filepath.Join(h.root, "a.go")
	b := filepath.Join(h.root, "b.go")
	h.docs[b], h.vers[b] = "second file\n", 7

	res := Dispatch(g, Request{Op: "text", Author: FirstAgent, Paths: []string{a, b}})
	if !res.OK {
		t.Fatalf("multi read = %+v", res)
	}
	if got := res.Text(); got != "hello world\nsecond file\n" {
		t.Fatalf("text = %q, want both files", got)
	}
	if len(res.Buffers) != 2 {
		t.Fatalf("buffers = %+v, want one per target", res.Buffers)
	}
	if res.Buffers[0].Path != a || res.Buffers[0].Version != 1 || res.Buffers[0].Bytes != len("hello world\n") {
		t.Errorf("buffer 0 = %+v", res.Buffers[0])
	}
	if res.Buffers[1].Path != b || res.Buffers[1].Version != 7 || res.Buffers[1].Bytes != len("second file\n") {
		t.Errorf("buffer 1 = %+v", res.Buffers[1])
	}
	// The single-target form still answers the old way: Version set, no
	// per-file list.
	single := Dispatch(g, Request{Op: "text", Author: FirstAgent, Path: a})
	if !single.OK || single.Version != 1 || single.Text() != "hello world\n" || len(single.Buffers) != 0 {
		t.Errorf("single read = %+v", single)
	}
}

// Read-before-write is satisfied for every path a multi-target read named, so a
// later apply to any of them is not refused for a missing read. Modelled on
// TestGuardRequiresAReadBeforeAWrite.
func TestDispatchReadMultipleAllowsLaterWrites(t *testing.T) {
	g, h := guarded(t)
	a := filepath.Join(h.root, "a.go")
	b := filepath.Join(h.root, "b.go")
	h.docs[b], h.vers[b] = "second file\n", 1
	g.setClaims(FirstAgent, []string{a, b})

	if res := Dispatch(g, Request{Op: "text", Author: FirstAgent, Paths: []string{a, b}}); !res.OK {
		t.Fatalf("multi read = %+v", res)
	}
	if _, _, _, err := g.Apply(a, FirstAgent, 1, []Hunk{{Start: 0, End: 0, Text: "x"}}); err != nil {
		t.Fatalf("apply to the first path was refused: %v", err)
	}
	if _, _, _, err := g.Apply(b, FirstAgent, 1, []Hunk{{Start: 0, End: 0, Text: "y"}}); err != nil {
		t.Fatalf("apply to the second path was refused: %v", err)
	}
}

// A multi-target read that fails on a later path must not satisfy the
// read-before-write gate for an earlier one: the response is an error, so the
// caller never received that target's text. Modelled on
// TestDispatchReadMultipleAllowsLaterWrites.
func TestDispatchReadMultipleFailureDoesNotMarkEarlierTargets(t *testing.T) {
	g, h := guarded(t)
	a := filepath.Join(h.root, "a.go")
	missing := filepath.Join(h.root, "missing.go")
	g.setClaims(FirstAgent, []string{a})

	res := Dispatch(g, Request{Op: "text", Author: FirstAgent, Paths: []string{a, missing}})
	if res.OK {
		t.Fatalf("multi read with a missing target = %+v, want a refusal", res)
	}
	// The call failed, so no text reached the caller; the gate must still
	// refuse the write even though the first target read cleanly.
	if _, _, _, err := g.Apply(a, FirstAgent, 1, []Hunk{{Start: 0, End: 0, Text: "x"}}); err == nil {
		t.Error("apply to a target whose text never arrived was allowed")
	}
}
