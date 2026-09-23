package control

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"raj/internal/prog"
)

// A stand-in editor: a real Server on a real socket whose requests are executed
// by a goroutine playing the event thread. Real socket rather than a fake
// client, because what is most likely to be wrong is the wiring — encoding,
// ids, blocking — and a fake would agree with whatever this file assumed.
type fakeEditor struct {
	srv     *Server
	mu      sync.Mutex
	docs    map[string]string
	vers    map[string]uint64
	authors []uint8
	// active is the path the fake reports as the focused buffer, so a test can
	// drive the CLI's pathless-target naming. Empty means no tab is focused,
	// which keeps every existing test on the fallback wording.
	active string
	// policy is a real Guard over a memHost, so the exec decision under test is
	// the shipped one rather than a second copy written for the test.
	policy    *Guard
	policyMem *memHost
	gate      chan struct{}
	cancelled bool
	// bump is called before each apply, to simulate the user typing between a
	// read and the write that follows it.
	bump func()
	// lease is the change set the fake apply names when it refuses a hunk, so
	// the CLI lease-refusal path can be exercised without a real Session. Zero
	// makes the refusal an ordinary stale offset. leaseAuthor, leaseStart and
	// leaseEnd are the owner and span that ride with it.
	lease       uint64
	leaseAuthor uint8
	leaseStart  int
	leaseEnd    int
	// ownLease makes a refused hunk name the caller's own author (the request
	// author) instead of leaseAuthor: the own+other case, whose refusal the CLI
	// must word as the caller's own draft rather than a peer's set.
	ownLease bool
	// warnings is what the fake apply and patch answer on success, so the CLI
	// warning path can be exercised without a real Session.
	warnings []GroupOverlap
	// patchConflict is the conflict list the fake patch returns, so the CLI's
	// patch conflict reporting can be driven without a real Session. Empty
	// means the patch succeeds.
	patchConflict []Conflict
	// searchPath records the -path the last search carried, so a CLI test can
	// assert the flag reaches the wire without a real walker.
	searchPath string
	// searchHidden records the -hidden flag the last search carried, so a CLI
	// test can assert it reaches the query without a real walker.
	searchHidden bool
	// entries is the canned ls answer, and lastLs records the request the CLI
	// sent, so the verb and its -hidden switch can be asserted on the wire.
	entries []Entry
	lastLs  Request
	// diffJSON is the canned diff answer; empty means no pending changes.
	diffJSON string
	// mode records the app mode a request switched to, so a test can assert
	// that `review -json` did not enter review while a plain `review` did.
	// Empty means still editing.
	mode string
	// lspJSON is the canned answer an lsp request returns, verbatim, so a test
	// can drive the CLI's diagnostics handling without a language server.
	lspJSON string
	// lspJSONByPath is the per-path canned diagnostics answer, so a multi-path
	// sweep can be driven with a distinct status per file. A map with no entry
	// for a path falls back to lspJSON.
	lspJSONByPath map[string]string
	// lspErrByPath makes an lsp request for a path fail, standing in for a file
	// the host cannot load; the path must still appear in a batch answer.
	lspErrByPath map[string]string
	// lastLSP records the lspprep request, so a CLI test can assert that the
	// inlay-hints mode and its -lines range reached the wire.
	lastLSP Request
	// truncated is the per-file truncation the fake search reports, so the CLI
	// can be tested on a walk that cut a file down without a real one.
	truncated []TruncatedFile
	// created and remains are the answers open and close -discard give: whether
	// open made a new buffer, and whether a discarded buffer's file is still on
	// disk. The fake has no app, so these are set by the test.
	created bool
	remains bool
	// headless names docs the fake reports as loaded with no tab, so the CLI
	// buffers output can be tested on the field that says so.
	headless map[string]bool
	// dirty, pending and moved are the per-buffer gate state `buffers` reports,
	// so `status` can be driven against the real Buffer fields without a real
	// Session. An absent key reads as zero, which is a clean buffer.
	dirty        map[string]bool
	pendingCount map[string]int
	moved        map[string]int
	// claims is the fake's working set, and lastClaim the request that last
	// touched it, so a CLI test can assert the wire fields. claimWarnings and
	// claimOverlaps are canned answers the CLI can be tested on.
	claims    []string
	lastClaim Request
	// lastRevert records the revert request, so a CLI test can assert the verb
	// reached the wire with the operand it was given.
	lastRevert Request
	// mkdirs records every mkdir request path, so a CLI test can assert the
	// verb reaches the wire with its operand intact. The fake has no
	// filesystem; the real MkdirAll semantics are covered in internal/app.
	mkdirs        []string
	lastRename    Request
	lastDelete    Request
	deletions     []Deletion
	lastRmdir     Request
	dirRemovals   []DirRemoval
	proposals     []Proposal
	claimWarnings []string
	claimOverlaps []ClaimOverlap
	// groups is the canned change-set list `groups` returns; decided records
	// every id accept/reject was called with, in order; decideErr makes a named
	// group fail, standing in for a reject a later edit wedged.
	groups    []Group
	decided   []uint64
	decideErr map[uint64]string
	// clearErr makes a named clear fail, standing in for a rejected set the
	// journal has wedged behind a later edit.
	clearErr map[uint64]string
	// clearBlock makes a named clear fail with the live set that overlaps it,
	// the structured refusal a real block reports.
	clearBlock map[uint64]Conflict
	// pending, when set, is the projection `diff` reports: the change sets with
	// surviving text. A successful reject removes its entry and an entry named
	// in wedge fails while its blocker is still pending, the two behaviours a
	// bulk reject has to drive. A nil pending falls back to the canned diffJSON
	// the fixed diff tests use.
	pending []Group
	// wedge names a group that cannot be reversed while another is still
	// pending; 0 means wedged outright.
	wedge map[uint64]uint64
	stop  chan struct{}
}

func newFakeEditor(t *testing.T, docs map[string]string) *fakeEditor {
	t.Helper()
	return newFakeEditorAt(t, controlSock(t, "c.sock"), docs)
}

// controlSock returns a unix socket address short enough on every platform.
// filepath.Join(t.TempDir(), "c.sock") crosses the 104-byte sockaddr_un
// sun_path limit on macOS, where TMPDIR is long and t.TempDir() appends the
// whole test name; bind then fails with "invalid argument". A single short
// component under os.TempDir() stays well under it.
func controlSock(t *testing.T, id string) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "raj-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, id)
}

// newFakeEditorAt is the same fixture on a named address, so the TCP tests
// drive the shipped server rather than a second one written for them.
func newFakeEditorAt(t *testing.T, addr string, docs map[string]string) *fakeEditor {
	t.Helper()
	return newFakeEditorAddrs(t, []string{addr}, docs)
}

// newFakeEditorAddrs is the same fixture on several listeners at once, so a
// test can drive one server over both a Unix socket and a TCP port and check
// that they share one queue and one registry.
func newFakeEditorAddrs(t *testing.T, addrs []string, docs map[string]string) *fakeEditor {
	t.Helper()
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	f := &fakeEditor{docs: map[string]string{}, vers: map[string]uint64{}, stop: make(chan struct{})}
	for k, v := range docs {
		f.docs[k], f.vers[k] = v, 1
	}
	wake := make(chan struct{}, 64)
	srv, err := ListenAll(addrs, func() {
		select {
		case wake <- struct{}{}:
		default:
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	mem := newMemHost("/w", nil)
	f.policyMem = mem
	f.policy = NewGuard(mem)
	f.srv = srv
	go func() {
		for {
			select {
			case <-f.stop:
				return
			case <-wake:
				for _, p := range srv.Take() {
					p.Reply(f.run(p.Req))
				}
			}
		}
	}()
	t.Cleanup(func() { close(f.stop); srv.Close() })
	t.Setenv("RAJ_CONTROL_ADDR", srv.Path())
	return f
}

func (f *fakeEditor) only() string {
	for k := range f.docs {
		return k
	}
	return ""
}

func (f *fakeEditor) run(req Request) Response {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := req.Path
	if path == "" {
		path = f.only()
	}
	switch req.Op {
	case "execcheck", "stats":
		return Dispatch(f.policy, req)
	case "searchsnapshot":
		return Response{OK: true, Searcher: f}
	case "ping":
		f.authors = append(f.authors, req.Author)
		return Response{OK: true, Root: "/w", Roots: []string{"/w"}, PID: 1}
	case "version":
		text, ok := f.docs[path]
		if !ok {
			return Response{Err: "no open buffer for " + path}
		}
		return Response{OK: true, Version: f.vers[path], Bytes: len(text), Lines: fakeLineCount(text)}
	case "buffers":
		var bufs []Buffer
		for p, text := range f.docs {
			// run holds f.mu for the whole dispatch, so read the field
			// directly; taking the lock again here deadlocks the fake.
			bufs = append(bufs, Buffer{Path: p, Version: f.vers[p], Bytes: len(text),
				Lines: strings.Count(text, "\n"), Active: p == f.active,
				Headless: f.headless[p],
				Dirty:    f.dirty[p],
				Pending:  f.pendingCount[p],
				Moved:    f.moved[p]})

		}
		return Response{OK: true, Root: "/w", Roots: []string{"/w"}, Buffers: bufs}
	case "text":
		if len(req.Paths) > 0 {
			// The multi-target form, mirroring the real Dispatch: one span per
			// target, Buffers naming each path and version and the bytes it
			// contributed.
			res := Response{OK: true}
			var states []string
			off := 0
			for _, p := range req.Paths {
				text, ok := f.docs[p]
				if !ok {
					return Response{Err: "no open buffer for " + p}
				}
				// The request span selects the same range in every target,
				// mirroring the real Dispatch's shared span.
				out := text
				switch {
				case req.LineStart != nil:
					out = fakeLineSpan(text, *req.LineStart, req.LineEnd)
				case req.Start != nil:
					start, end := *req.Start, len(text)
					if req.End != nil {
						end = *req.End
					}
					if start < 0 {
						start = 0
					}
					if start > len(text) {
						start = len(text)
					}
					if end > len(text) {
						end = len(text)
					}
					if end < start {
						end = start
					}
					out = text[start:end]
				}
				res.Spans = append(res.Spans, Span{Text: out, Author: FirstAgent})
				res.Buffers = append(res.Buffers, Buffer{Path: p, Version: f.vers[p], Bytes: len(out), Lines: fakeLineCount(out)})
				if req.Annotated {
					states = append(states, fmt.Sprintf(`{"off":%d,"len":%d,"group":0,"state":"accepted"}`, off, len(out)))
				}
				off += len(out)
			}
			if len(states) > 0 {
				res.StatesJSON = "[" + strings.Join(states, ",") + "]"
			}
			return res
		}
		text, ok := f.docs[path]
		if !ok {
			return Response{Err: "no open buffer for " + path}
		}
		start, end := -1, -1
		if req.Start != nil {
			start = *req.Start
		}
		if req.End != nil {
			end = *req.End
		}
		if req.LineStart != nil {
			text = fakeLineSpan(text, *req.LineStart, req.LineEnd)
			start, end = 0, len(text)
		} else if start < 0 {
			start, end = 0, len(text)
		} else {
			if end < 0 || end > len(text) {
				end = len(text)
			}
			if start > len(text) {
				start = len(text)
			}
		}
		res := Response{OK: true, Bytes: len(f.docs[path]), Lines: fakeLineCount(f.docs[path]),
			Spans: []Span{{Text: text[start:end], Author: FirstAgent}}, Version: f.vers[path]}
		if req.Annotated {
			res.StatesJSON = fmt.Sprintf(`[{"off":0,"len":%d,"group":0,"state":"accepted"}]`, end-start)
		}
		return res
	case "find":
		// The fake's find is a literal substring scan, enough for the CLI's
		// run -prog end-to-end path. The real matching is the host's.
		text, ok := f.docs[path]
		if !ok {
			return Response{Err: "no open buffer for " + path}
		}
		if req.Query == nil {
			return Response{Err: "find needs a query"}
		}
		hay, needle := text, req.Query.Text
		if !req.Query.Case {
			hay, needle = strings.ToLower(hay), strings.ToLower(needle)
		}
		if needle == "" {
			return Response{OK: true, Version: f.vers[path]}
		}
		i := strings.Index(hay, needle)
		n := 0
		for off := 0; off <= len(hay)-len(needle); {
			j := strings.Index(hay[off:], needle)
			if j < 0 {
				break
			}
			n++
			off += j + len(needle)
		}
		if i < 0 {
			return Response{OK: true, FindCount: n, Version: f.vers[path]}
		}
		return Response{OK: true, Found: true, FindStart: i, FindEnd: i + len(needle),
			FindCount: n, Version: f.vers[path]}
	case "open":
		created := false
		if _, ok := f.docs[path]; !ok {
			if !req.Create {
				// The fake has no disk, so not-in-docs is the missing file the
				// real host would stat for.
				return Response{Err: "no open buffer or file at " + path}
			}
			f.docs[path], f.vers[path] = "", 1
			created = true
		}
		if f.created {
			created = true // a test can force a create answer regardless
		}
		return Response{OK: true, Version: f.vers[path], Created: created}
	case "goto":
		return Response{OK: true}
	case "apply":
		if f.bump != nil {
			f.bump()
		}
		text, ok := f.docs[path]
		if !ok {
			return Response{Err: "no open buffer for " + path}
		}
		if req.Base == nil {
			return Response{Err: "apply needs a base version"}
		}
		if *req.Base != f.vers[path] {
			author := f.leaseAuthor
			if f.ownLease {
				// req.Author is the caller's real id, stamped by the connection,
				// so the refusal names the caller's own set: the own+other case.
				author = req.Author
			}
			return Response{Err: "stale", Conflicts: []Conflict{{Index: 0, Group: f.lease,
				Author: author, Start: f.leaseStart, End: f.leaseEnd, Hunk: req.Hunks[0]}},
				Warnings: f.warnings}
		}
		for i := len(req.Hunks) - 1; i >= 0; i-- { // back to front: offsets stay valid
			h := req.Hunks[i]
			text = text[:h.Start] + h.Text + text[h.End:]
		}
		f.docs[path], f.vers[path] = text, f.vers[path]+1
		return Response{OK: true, Version: f.vers[path], Warnings: f.warnings}
	case "patch":
		if len(f.patchConflict) > 0 {
			return Response{Conflicts: f.patchConflict, Warnings: f.warnings}
		}
		return Response{OK: true, Version: f.vers[path], Warnings: f.warnings}
	case "save":
		return Response{OK: true, Version: f.vers[path]}
	case "groups":
		return Response{OK: true, Groups: append([]Group(nil), f.groups...)}
	case "accept", "reject":
		if req.Op == "reject" && f.pending != nil {
			// A set a newer overlapping set still blocks cannot come out — the
			// same refusal the real Session gives.
			if blocker, ok := f.wedge[req.Group]; ok {
				blocked := blocker == 0
				for _, g := range f.pending {
					if g.ID == blocker {
						blocked = true
					}
				}
				if blocked {
					return Response{Err: fmt.Sprintf("change set %d could not be backed out: "+
						"later edits overlap it", req.Group)}
				}
			}
			for i := range f.pending {
				if f.pending[i].ID == req.Group {
					f.pending = append(f.pending[:i], f.pending[i+1:]...)
					f.decided = append(f.decided, req.Group)
					return Response{OK: true}
				}
			}
			return Response{Err: fmt.Sprintf("no change set %d", req.Group)}
		}
		if msg, ok := f.decideErr[req.Group]; ok {
			return Response{Err: msg}
		}
		f.decided = append(f.decided, req.Group)
		return Response{OK: true}
	case "clear":
		if c, ok := f.clearBlock[req.Group]; ok {
			return Response{Err: fmt.Sprintf("change set %d cannot be cleared: change set %d overlaps it",
				req.Group, c.Group), Conflicts: []Conflict{c}}
		}
		if msg, ok := f.clearErr[req.Group]; ok {
			return Response{Err: msg}
		}
		for i := range f.groups {
			if f.groups[i].ID != req.Group {
				continue
			}
			if f.groups[i].State != "rejected" {
				return Response{Err: fmt.Sprintf("change set %d is not rejected", req.Group)}
			}
			f.groups[i].State = "accepted"
			f.decided = append(f.decided, req.Group)
			return Response{OK: true}
		}
		return Response{Err: fmt.Sprintf("no change set %d", req.Group)}
	case "diff":
		if f.pending != nil {
			diffs := make([]DiffGroup, 0, len(f.pending))
			for _, g := range f.pending {
				// One surviving hunk marks the set pending, which is all the
				// CLI reads the projection for.
				diffs = append(diffs, DiffGroup{Group: g, Hunks: []DiffHunk{{Start: 0, End: 0}}})
			}
			data, err := json.Marshal(diffs)
			if err != nil {
				return Response{Err: err.Error()}
			}
			return Response{OK: true, DiffJSON: string(data)}
		}
		if f.diffJSON == "" {
			return Response{OK: true, DiffJSON: "[]"}
		}
		return Response{OK: true, DiffJSON: f.diffJSON}
	case "review":
		if !req.ReviewList {
			f.mode = "review"
		}
		return Response{OK: true, Groups: append([]Group(nil), f.groups...)}
	case "close":
		// The fake has no app; the remainder answer is what a test sets, so the
		// CLI wording can be driven without a disk.
		delete(f.docs, path)
		return Response{OK: true, Remains: req.Discard && f.remains}
	case "lspprep":
		f.lastLSP = req
		if msg, ok := f.lspErrByPath[req.Path]; ok {
			return Response{Err: msg}
		}
		j := f.lspJSON
		if v, ok := f.lspJSONByPath[req.Path]; ok {
			j = v
		}
		return Response{OK: true, LSP: fakeLSP{json: j}}
	case "claim":
		f.lastClaim = req
		switch {
		case req.ClaimClear:
			f.claims = nil
		case req.ClaimAdd:
			for _, p := range req.Paths {
				dup := false
				for _, q := range f.claims {
					if q == p {
						dup = true
						break
					}
				}
				if !dup {
					f.claims = append(f.claims, p)
				}
			}
		case len(req.Paths) > 0:
			f.claims = append([]string(nil), req.Paths...)
		}
		return Response{OK: true, Claims: append([]string(nil), f.claims...),
			ClaimWarnings: f.claimWarnings, ClaimOverlaps: f.claimOverlaps}
	case "mkdir":
		f.mkdirs = append(f.mkdirs, req.Path)
		return Response{OK: true}
	case "rename":
		f.lastRename = req
		return Response{OK: true}
	case "delete":
		f.lastDelete = req
		return Response{OK: true}
	case "deletions":
		return Response{OK: true, Deletions: append([]Deletion(nil), f.deletions...)}
	case "rmdir":
		f.lastRmdir = req
		return Response{OK: true}
	case "rmdirs":
		return Response{OK: true, DirRemovals: append([]DirRemoval(nil), f.dirRemovals...)}
	case "ls":
		f.lastLs = req
		return Response{OK: true, Entries: append([]Entry(nil), f.entries...)}
	case "proposals":
		return Response{OK: true, Proposals: append([]Proposal(nil), f.proposals...)}
	case "revert":
		f.lastRevert = req
		return Response{OK: true, Version: f.vers[path]}
	}
	return Response{Err: "unknown op " + req.Op}
}

// fakeLSP is a language-server answer the CLI can be handed without a server:
// the JSON is exactly what a real lspCaller would have produced.
type fakeLSP struct{ json string }

func (f fakeLSP) Run(context.Context) ([]byte, error) { return []byte(f.json), nil }

// fakeLineCount is the number of lines a version reply reports: one per newline
// plus the final (possibly empty) line, matching view.Index and File.Lines.
func fakeLineCount(text string) int { return strings.Count(text, "\n") + 1 }

// fakeLineSpan is a 1-based inclusive line range, mirroring the host's own
// translation: a missing end reads to the end and a start past the document
// lands on the final empty line.
func fakeLineSpan(text string, first int, last *int) string {
	starts := []int{0}
	for i := 0; i < len(text); i++ {
		if text[i] == '\n' {
			starts = append(starts, i+1)
		}
	}
	lines := len(starts)
	start := first - 1
	if start < 0 {
		start = 0
	}
	if start >= lines {
		start = lines - 1
	}
	end := len(text)
	if last != nil && *last > 0 && *last < lines {
		end = starts[*last]
	}
	return text[starts[start]:end]
}

func run(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	var out, errb bytes.Buffer
	code = CLI(args, &out, &errb)
	return out.String(), errb.String(), code
}

func TestCLIReads(t *testing.T) {
	newFakeEditor(t, map[string]string{"/w/a.go": "package a\n\nfunc f() {}\n"})

	out, _, code := run(t, "buffers")
	if code != 0 || !strings.Contains(out, "/w/a.go") {
		t.Errorf("buffers = %q, code %d", out, code)
	}
	out, _, code = run(t, "read", "/w/a.go")
	if code != 0 || out != "package a\n\nfunc f() {}\n" {
		t.Errorf("read = %q, code %d", out, code)
	}
}

// `raj ctl token` reads the running server's secret out of the process over the
// local socket and prints it alone, so `TOKEN=$(raj ctl token)` is the whole
// command. The socket is where the filesystem authorises, which is what lets a
// local script fetch a token for the port without having seen startup stderr.
func TestCLIToken(t *testing.T) {
	sock := controlSock(t, "cli-tok.sock")
	ed := newFakeEditorAddrs(t, []string{sock, "tcp://127.0.0.1:0"},
		map[string]string{"/w/a.go": "x"})

	out, errs, code := run(t, "token")
	if code != 0 {
		t.Fatalf("code %d: %s", code, errs)
	}
	if strings.TrimSpace(out) != ed.srv.Token() {
		t.Errorf("token = %q, want %q", strings.TrimSpace(out), ed.srv.Token())
	}
	if ed.srv.Token() == "" || strings.TrimSpace(out) == "" {
		t.Fatal("the mixed server handed out no token")
	}

	out, errs, code = run(t, "token", "-json")
	if code != 0 {
		t.Fatalf("json code %d: %s", code, errs)
	}
	if !strings.Contains(out, ed.srv.Token()) {
		t.Errorf("token -json = %q, want it to carry %q", out, ed.srv.Token())
	}
}

// A headless buffer is loaded and addressable but has no tab. buffers marks it
// in the plain listing and carries headless in the JSON, so a driver can tell
// it from one the user can see.
func TestCLIBuffersReportsHeadless(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "hello\n"})
	ed.headless = map[string]bool{"/w/a.go": true}

	out, errs, code := run(t, "buffers")
	if code != 0 {
		t.Fatalf("code %d: %s", code, errs)
	}
	if !strings.Contains(out, "headless") {
		t.Errorf("buffers output %q does not mark a headless buffer", out)
	}

	out, _, code = run(t, "buffers", "-json")
	if code != 0 || !strings.Contains(out, "\"headless\": true") {
		t.Errorf("json buffers = %q, code %d", out, code)
	}
}

// An empty set is [] in JSON like every other listing, not null: a script that
// indexes the result should not need a nil check for one verb alone.
func TestCLIBuffersEmptyJSONIsAList(t *testing.T) {
	newFakeEditor(t, map[string]string{})

	out, errs, code := run(t, "buffers", "-json")
	if code != 0 {
		t.Fatalf("code %d: %s", code, errs)
	}
	if strings.TrimSpace(out) != "[]" {
		t.Errorf("empty buffers -json = %q, want []", strings.TrimSpace(out))
	}
}

// status turns the per-buffer gate state into one answer for a host `make
// check`: a clean workspace is exit 0 and names nothing, and anything dirty or
// pending is exit 1 with every offending path named. These tests pin the three
// facts the answer is built from -- dirty, pending and moved -- so a regression
// that drops one is caught by the verb rather than by a broken gate.
func TestCLIStatusCleanIsReady(t *testing.T) {
	newFakeEditor(t, map[string]string{"/w/a.go": "package a\n"})

	out, errs, code := run(t, "status")
	if code != 0 {
		t.Fatalf("code %d: %s", code, errs)
	}
	if !strings.Contains(out, "clean") {
		t.Errorf("status on a clean workspace = %q, want a clean summary", out)
	}
	if strings.Contains(out, "/w/a.go") {
		t.Errorf("status on a clean workspace named %q; a clean answer names no path", out)
	}
}

// A dirty buffer blocks the gate even with nothing proposed: the host would
// read the file on disk, which is not the text the editor holds. The path and
// the word dirty are both asserted because a count alone would not say which
// buffer to save.
func TestCLIStatusNamesDirtyBuffer(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "package a\n"})
	ed.dirty = map[string]bool{"/w/a.go": true}

	out, errs, code := run(t, "status")
	if code != 1 {
		t.Fatalf("code %d: %s; want 1 for a dirty buffer", code, errs)
	}
	if !strings.Contains(out, "/w/a.go") || !strings.Contains(out, "dirty") {
		t.Errorf("status = %q, want it to name /w/a.go and say dirty", out)
	}
}

// A pending change set blocks the gate on its own, and moved is reported as
// detail beside it: the counts distinguish "one save away" from "a rebase could
// not carry these", which are two different next steps.
func TestCLIStatusNamesPendingAndMoved(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/b.go": "package b\n"})
	ed.pendingCount = map[string]int{"/w/b.go": 2}
	ed.moved = map[string]int{"/w/b.go": 1}

	out, errs, code := run(t, "status")
	if code != 1 {
		t.Fatalf("code %d: %s; want 1 for a pending buffer", code, errs)
	}
	if !strings.Contains(out, "/w/b.go") {
		t.Errorf("status = %q, want it to name /w/b.go", out)
	}
	if !strings.Contains(out, "2 pending") || !strings.Contains(out, "1 moved") {
		t.Errorf("status = %q, want 2 pending and 1 moved", out)
	}
}

// -json is the same facts for a script. It is decoded into the real Buffer
// type rather than a map, so a field the plain answer names cannot silently
// disappear from the structured one.
func TestCLIStatusJSONCarriesTheSameFacts(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "package a\n", "/w/b.go": "package b\n"})
	ed.dirty = map[string]bool{"/w/a.go": true}
	ed.pendingCount = map[string]int{"/w/b.go": 2}
	ed.moved = map[string]int{"/w/b.go": 1}

	var got struct {
		Ready    bool     `json:"ready"`
		Total    int      `json:"total"`
		Blocking []Buffer `json:"blocking"`
	}
	out, errs, code := run(t, "status", "-json")
	if code != 1 {
		t.Fatalf("code %d: %s; want 1 for a dirty or pending buffer", code, errs)
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("status -json is not parseable: %v (%q)", err, out)
	}
	if got.Ready {
		t.Errorf("ready = true with dirty and pending buffers")
	}
	if got.Total != 2 {
		t.Errorf("total = %d, want 2", got.Total)
	}
	byPath := map[string]Buffer{}
	for _, b := range got.Blocking {
		byPath[b.Path] = b
	}
	if b, ok := byPath["/w/a.go"]; !ok || !b.Dirty {
		t.Errorf("blocking = %+v, want /w/a.go dirty", got.Blocking)
	}
	if b, ok := byPath["/w/b.go"]; !ok || b.Pending != 2 || b.Moved != 1 {
		t.Errorf("blocking = %+v, want /w/b.go pending 2 moved 1", got.Blocking)
	}
}

// The clean -json answer is ready:true with an empty list, so a script can test
// one field and never special-case null.
func TestCLIStatusCleanJSONIsReadyWithAList(t *testing.T) {
	newFakeEditor(t, map[string]string{})

	var got struct {
		Ready    bool     `json:"ready"`
		Total    int      `json:"total"`
		Blocking []Buffer `json:"blocking"`
	}
	out, errs, code := run(t, "status", "-json")
	if code != 0 {
		t.Fatalf("code %d: %s", code, errs)
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("status -json is not parseable: %v (%q)", err, out)
	}
	if !got.Ready || got.Blocking == nil || len(got.Blocking) != 0 {
		t.Errorf("clean status -json = %+v, want ready with an empty list", got)
	}
}

func TestCLIReadSpan(t *testing.T) {
	newFakeEditor(t, map[string]string{"/w/a.go": "package a\n\nfunc f() {}\n"})

	out, _, code := run(t, "read", "-start", "11", "-end", "17", "/w/a.go")
	if code != 0 || out != "func f" {
		t.Errorf("read span = %q, code %d", out, code)
	}
}

// -annotated reaches the server and its state runs reach the JSON output; the
// plain form prints the text and then one run line per run. A plain read
// without the flag is still exactly the text, byte for byte.
func TestCLIReadAnnotated(t *testing.T) {
	newFakeEditor(t, map[string]string{"/w/a.go": "package a\n\nfunc f() {}\n"})
	const text = "package a\n\nfunc f() {}\n"

	out, _, code := run(t, "read", "/w/a.go")
	if code != 0 || out != text {
		t.Errorf("plain read = %q, code %d; want the text alone", out, code)
	}

	out, _, code = run(t, "read", "-annotated", "/w/a.go")
	want := text + "run off=0 len=23 group=0 state=accepted\n"
	if code != 0 || out != want {
		t.Errorf("annotated read = %q, code %d; want %q", out, code, want)
	}

	out, _, code = run(t, "read", "-annotated", "-json", "/w/a.go")
	if code != 0 {
		t.Fatalf("annotated json read: code %d", code)
	}
	if !strings.Contains(out, `"states"`) || !strings.Contains(out, `"accepted"`) {
		t.Errorf("annotated json = %q, want states", out)
	}
}

// read --json carries the whole-file byte length and line count, the same
// numbers version --json reports, so a driver that just read the text does not
// make a second call for the length before appending.
//
// Precondition: /w/a.go holds the 23-byte, 4-line text "package a\n\nfunc
// f() {}\n", and the read is whole-file.
func TestCLIReadJSONCarriesFileSize(t *testing.T) {
	const text = "package a\n\nfunc f() {}\n"
	newFakeEditor(t, map[string]string{"/w/a.go": text})

	out, errs, code := run(t, "read", "-json", "/w/a.go")
	if code != 0 {
		t.Fatalf("read -json code %d: %s", code, errs)
	}
	var got struct {
		Text    string `json:"text"`
		Version uint64 `json:"version"`
		Author  uint8  `json:"author"`
		Spans   []any  `json:"spans"`
		Bytes   int    `json:"bytes"`
		Lines   int    `json:"lines"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("read -json is not parseable: %v (%q)", err, out)
	}
	if got.Text != text {
		t.Fatalf("text = %q, want %q", got.Text, text)
	}
	if got.Bytes != len(text) || got.Lines != fakeLineCount(text) {
		t.Errorf("read bytes/lines = %d/%d, want %d/%d",
			got.Bytes, got.Lines, len(text), fakeLineCount(text))
	}

	// The same file through version --json: the two must agree, or the read
	// still needs the second call this change removes.
	vout, verrs, vcode := run(t, "version", "-json", "/w/a.go")
	if vcode != 0 {
		t.Fatalf("version -json code %d: %s", vcode, verrs)
	}
	var ver struct {
		Version uint64 `json:"version"`
		Bytes   int    `json:"bytes"`
		Lines   int    `json:"lines"`
	}
	if err := json.Unmarshal([]byte(vout), &ver); err != nil {
		t.Fatalf("version -json is not parseable: %v (%q)", err, vout)
	}
	if got.Version != ver.Version || got.Bytes != ver.Bytes || got.Lines != ver.Lines {
		t.Errorf("read %d/%d/%d != version %d/%d/%d",
			got.Version, got.Bytes, got.Lines, ver.Version, ver.Bytes, ver.Lines)
	}
}

// A span read still reports the whole-file bytes and lines, not the returned
// span: the append use case needs the file length even when the text is a
// window. A regression that reported the returned text length would show bytes
// 6 and lines 1 here.
//
// Precondition: /w/a.go is the 23-byte, 4-line text above; the byte span
// [11,17) is "func f" and the line range 3,3 is "func f() {}\n".
func TestCLIReadJSONSpanReportsWholeFile(t *testing.T) {
	const text = "package a\n\nfunc f() {}\n"
	newFakeEditor(t, map[string]string{"/w/a.go": text})

	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"bytes", []string{"read", "-json", "-start", "11", "-end", "17", "/w/a.go"}, "func f"},
		{"lines", []string{"read", "-json", "-lines", "3,3", "/w/a.go"}, "func f() {}\n"},
	} {
		out, errs, code := run(t, tc.args...)
		if code != 0 {
			t.Fatalf("%s: code %d: %s", tc.name, code, errs)
		}
		var got struct {
			Text  string `json:"text"`
			Bytes int    `json:"bytes"`
			Lines int    `json:"lines"`
		}
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("%s: %v (%q)", tc.name, err, out)
		}
		if got.Text != tc.want {
			t.Errorf("%s: text = %q, want %q", tc.name, got.Text, tc.want)
		}
		if got.Bytes != len(text) || got.Lines != fakeLineCount(text) {
			t.Errorf("%s: bytes/lines = %d/%d, want the whole file %d/%d",
				tc.name, got.Bytes, got.Lines, len(text), fakeLineCount(text))
		}
	}
}

// A multi-target read carries per-file bytes and lines, each file its own, so a
// batch read is one call for both text and size.
//
// Precondition: two whole-file reads of different sizes, so a shared value
// could not pass for both.
func TestCLIReadManyJSONCarriesPerFileSizes(t *testing.T) {
	docs := map[string]string{
		"/w/a.go": "package a\n",
		"/w/b.go": "package b\n\n// two\n",
	}
	newFakeEditor(t, docs)

	out, errs, code := run(t, "read", "-json", "/w/a.go", "/w/b.go")
	if code != 0 {
		t.Fatalf("multi read -json code %d: %s", code, errs)
	}
	var got struct {
		Files []struct {
			Path  string `json:"path"`
			Text  string `json:"text"`
			Bytes int    `json:"bytes"`
			Lines int    `json:"lines"`
		} `json:"files"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("multi read -json is not parseable: %v (%q)", err, out)
	}
	if len(got.Files) != 2 {
		t.Fatalf("files = %+v, want two entries", got.Files)
	}
	for _, f := range got.Files {
		text, ok := docs[f.Path]
		if !ok {
			t.Fatalf("unexpected file %q in %+v", f.Path, got.Files)
		}
		if f.Text != text {
			t.Errorf("%s text = %q, want %q", f.Path, f.Text, text)
		}
		if f.Bytes != len(text) || f.Lines != fakeLineCount(text) {
			t.Errorf("%s bytes/lines = %d/%d, want %d/%d",
				f.Path, f.Bytes, f.Lines, len(text), fakeLineCount(text))
		}
	}
}

// The added bytes/lines keys are additive: author, spans, text and version are
// still there, so a driver written against the old shape keeps reading it.
func TestCLIReadJSONKeepsExistingKeys(t *testing.T) {
	newFakeEditor(t, map[string]string{"/w/a.go": "package a\n"})

	out, errs, code := run(t, "read", "-json", "/w/a.go")
	if code != 0 {
		t.Fatalf("read -json code %d: %s", code, errs)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &keys); err != nil {
		t.Fatalf("read -json is not parseable: %v (%q)", err, out)
	}
	for _, k := range []string{"author", "spans", "text", "version", "bytes", "lines"} {
		if _, ok := keys[k]; !ok {
			t.Errorf("read -json is missing %q: %s", k, out)
		}
	}
}

// A byte span written as positional operands used to read the whole file and
// look like it worked, with two numbers that look like offsets the verb
// accepts. read now takes several paths, so the span mistake shows up as a
// missing file rather than a silently ignored offset; dump still refuses the
// extra operand outright and names the flags that carry a span.
func TestCLIRefusesPositionalSpan(t *testing.T) {
	newFakeEditor(t, map[string]string{"/w/a.go": "package a\n\nfunc f() {}\n"})

	out, errs, code := run(t, "read", "/w/a.go", "940", "1000")
	if code == 0 || out != "" || !strings.Contains(errs, "940") {
		t.Errorf("read with positional offsets: code = %d, stdout %q, stderr %q; want the missing-file refusal", code, out, errs)
	}

	out, errs, code = run(t, "dump", "/w/a.go", "0", "10")
	if code != 2 || out != "" || !strings.Contains(errs, "--start/--end") {
		t.Errorf("dump with a positional span: code = %d, stdout %q, stderr %q", code, out, errs)
	}

	// goto legitimately takes LINE[:COL] as a second operand, so the same
	// shape has to keep working there.
	if _, errs, code = run(t, "goto", "/w/a.go", "2:1"); code != 0 {
		t.Errorf("goto with a position: code = %d, stderr %q", code, errs)
	}
}

// The point of the CLI over the raw protocol: a string, not an offset.
func TestCLIEditByString(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "package a\n\nfunc f() {}\n"})
	out, errs, code := run(t, "edit", "/w/a.go", "-old", "func f()", "-new", "func g()")
	if code != 0 {
		t.Fatalf("code %d: %s", code, errs)
	}
	if !strings.Contains(out, "unsaved") {
		t.Errorf("output %q does not say the change is unsaved", out)
	}
	if got := ed.docs["/w/a.go"]; got != "package a\n\nfunc g() {}\n" {
		t.Errorf("buffer = %q", got)
	}
}

// A replacement body beginning with "-" is literal text after --, not an
// operand the parser should refuse; the ordinary flag terminator is how it
// gets past reorder.
func TestCLIEditPositionalTextAfterTerminator(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "package a\n\nfunc f() {}\n"})

	_, errs, code := run(t, "edit", "/w/a.go", "-old", "func f()", "--", "- item")
	if code != 0 {
		t.Fatalf("code %d: %s", code, errs)
	}
	if got := ed.docs["/w/a.go"]; got != "package a\n\n- item {}\n" {
		t.Errorf("buffer = %q", got)
	}
}

// Two operands after -- fill -old and -new in order, without a flag.
func TestCLIEditPositionalOldAndNew(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "a b c\n"})

	if _, errs, code := run(t, "edit", "/w/a.go", "--", "b", "- two"); code != 0 {
		t.Fatalf("code %d: %s", code, errs)
	}
	if got := ed.docs["/w/a.go"]; got != "a - two c\n" {
		t.Errorf("buffer = %q", got)
	}
}

// apply takes its replacement text as the one operand after --.
func TestCLIApplyPositionalTextAfterTerminator(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "hello world\n"})

	_, errs, code := run(t, "apply", "/w/a.go", "-base", "1", "-start", "0", "-end", "5", "--", "- item")
	if code != 0 {
		t.Fatalf("code %d: %s", code, errs)
	}
	if got := ed.docs["/w/a.go"]; got != "- item world\n" {
		t.Errorf("buffer = %q", got)
	}
}

// -text-file ends with the newline the file or heredoc carries; -verbatim
// strips exactly that one, so a one-line replacement does not split a line.
func TestCLIApplyVerbatimTextFile(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "hello world\n"})
	dir := t.TempDir()
	f := filepath.Join(dir, "text")
	if err := os.WriteFile(f, []byte("hi\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, errs, code := run(t, "apply", "/w/a.go", "-base", "1", "-start", "0", "-end", "5",
		"-text-file", f, "-verbatim"); code != 0 {
		t.Fatalf("verbatim code %d: %s", code, errs)
	}
	if got := ed.docs["/w/a.go"]; got != "hi world\n" {
		t.Errorf("buffer = %q, want the trailing newline stripped", got)
	}

	// Without the flag the bytes are untouched, which is the old behaviour.
	ed.docs["/w/a.go"], ed.vers["/w/a.go"] = "hello world\n", 1
	if _, errs, code := run(t, "apply", "/w/a.go", "-base", "1", "-start", "0", "-end", "5",
		"-text-file", f); code != 0 {
		t.Fatalf("plain code %d: %s", code, errs)
	}
	if got := ed.docs["/w/a.go"]; got != "hi\n world\n" {
		t.Errorf("buffer = %q, want the newline kept", got)
	}
}

// The heredoc shape piped on stdin ends with the newline the shell added;
// -verbatim removes exactly it.
func TestCLIApplyVerbatimStdin(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "hello world\n"})
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString("hi\n"); err != nil {
		t.Fatal(err)
	}
	w.Close()
	old := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = old }()

	if _, errs, code := run(t, "apply", "/w/a.go", "-base", "1", "-start", "0", "-end", "5",
		"-text-file", "-", "-verbatim"); code != 0 {
		t.Fatalf("code %d: %s", code, errs)
	}
	if got := ed.docs["/w/a.go"]; got != "hi world\n" {
		t.Errorf("buffer = %q, want the newline stripped", got)
	}
}

// A recv that times out exits 3 in JSON mode too. The empty JSON list is still
// written so `--json` always parses, but the code is what a polling loop reads
// and it must not depend on the output shape. The cancelled frame is the real
// server's, driven with -wait so the parked recv is cancelled. Modelled on
// TestCLIRefusalsExitNonZero (exit-code assertions over the same fake server).
func TestCLIRecvJSONTimeoutExitsThree(t *testing.T) {
	newFakeEditor(t, map[string]string{"/w/a.go": "hello\n"})

	out, errs, code := run(t, "recv", "-wait", "20ms", "-json")
	if code != 3 {
		t.Fatalf("recv -json timeout code = %d, want 3 (stderr %q, stdout %q)", code, errs, out)
	}
	var msgs []Message
	if err := json.Unmarshal([]byte(out), &msgs); err != nil {
		t.Fatalf("recv -json timeout output = %q, want an empty JSON list: %v", out, err)
	}
	if len(msgs) != 0 {
		t.Errorf("recv -json timeout = %+v, want no messages", msgs)
	}
}

// A process squatting the control address accepts the connection and then says
// nothing. The 2s connect timeout cannot catch it: the connect succeeded, so an
// ordinary request must bound its wait for the answer and name the wrong
// process rather than parking the driver forever.
func TestOrdinaryRequestTimesOutOnASilentPeer(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		if conn, err := ln.Accept(); err == nil {
			accepted <- conn // held open and never written to: the squatter
		}
	}()
	t.Cleanup(func() {
		select {
		case conn := <-accepted:
			conn.Close()
		default:
		}
	})

	c, err := Dial(TCPAddr(ln.Addr()))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.readTimeout = 100 * time.Millisecond

	done := make(chan error, 1)
	go func() {
		_, err := c.Do(Request{Op: "ping"})
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a request to a silent peer was answered")
		}
		if !strings.Contains(err.Error(), "no response") ||
			!strings.Contains(err.Error(), "another process") {
			t.Errorf("error = %q, want it to name the silent wrong-process case", err)
		}
		if !isTimeout(err) {
			t.Errorf("error = %v, want it distinguishable as a deadline expiry", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a request to a silent peer hung; the answer deadline was not armed")
	}
}

// recv is a long-poll: it parks until the user says something, which may be
// hours. The answer deadline must not be armed on it, or a working recv becomes
// a spurious timeout. A real server parks the request, so this only has to show
// it is still parked well past the armed wait.
func TestRecvWaitsPastTheAnswerDeadline(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "hello\n"})
	c, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	c.readTimeout = 50 * time.Millisecond
	// An ordinary request first, so this also proves the deadline it armed was
	// cleared: a recv made after it must still park, not inherit it.
	if _, err := c.Do(Request{Op: "ping"}); err != nil {
		t.Fatalf("ping before recv: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := c.Do(Request{Op: "recv"})
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("recv returned within the armed wait; the deadline was not exempted: %v", err)
	case <-time.After(250 * time.Millisecond):
	}
	// Unblock the parked recv so the goroutine and the connection end.
	c.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("closing the client did not end the parked recv")
	}
}

// A deadline expiry and an ordinary transport failure are different diagnoses:
// one names a wrong process, the other a broken connection. isTimeout is what
// tells them apart.
func TestIsTimeoutDistinguishesDeadlineFromTransport(t *testing.T) {
	if !isTimeout(os.ErrDeadlineExceeded) {
		t.Error("os.ErrDeadlineExceeded was not recognised as a timeout")
	}
	if !isTimeout(fmt.Errorf("read: %w", os.ErrDeadlineExceeded)) {
		t.Error("a wrapped deadline expiry was not recognised")
	}
	if isTimeout(fmt.Errorf("connection reset by peer")) {
		t.Error("a transport failure was mistaken for a timeout")
	}
}

// A single budget for every verb was too blunt: the wrong-process identity
// check should be named quickly, while a batch wrapping a long exec and a cold
// language server legitimately take longer. The table is pure, so it is checked
// without a connection.
func TestAnswerBudgetPerVerb(t *testing.T) {
	cases := []struct {
		op   string
		want time.Duration
	}{
		{"ping", 3 * time.Second},
		{"read", 10 * time.Second},
		{"buffers", 10 * time.Second},
		{"apply", 10 * time.Second},
		{"prog", 60 * time.Second},
		{"lsp", 30 * time.Second},
	}
	for _, c := range cases {
		if got := answerBudget(c.op); got != c.want {
			t.Errorf("answerBudget(%q) = %s, want %s", c.op, got, c.want)
		}
	}
	// The ordering is the point: ping must be the quickest, and the heavy verbs
	// must outlast the cheap default.
	if answerBudget("ping") >= answerBudget("read") {
		t.Error("ping is not shorter than the default; a squatter would not be named quickly")
	}
	if answerBudget("prog") <= answerBudget("read") || answerBudget("lsp") <= answerBudget("read") {
		t.Error("a heavy verb is not more generous than the default; a slow answer would be cut off")
	}
}

// answerWait turns the budget into the deadline Do arms, and leaves recv exempt
// from all of them. The readTimeout seam replaces the budget for every bounded
// verb, so a test can prove the silent-peer path in milliseconds.
func TestAnswerWaitUsesBudgetAndSeam(t *testing.T) {
	var c Client
	for _, op := range []string{"ping", "read", "prog", "lsp"} {
		wait, bounded := c.answerWait(op)
		if !bounded {
			t.Errorf("answerWait(%q) armed no deadline, want its budget", op)
			continue
		}
		if want := answerBudget(op); wait != want {
			t.Errorf("answerWait(%q) = %s, want the budget %s", op, wait, want)
		}
	}
	if wait, bounded := c.answerWait("recv"); bounded || wait != 0 {
		t.Errorf("answerWait(recv) = (%s, %v), want the long poll exempt", wait, bounded)
	}

	c.readTimeout = 7 * time.Millisecond
	for _, op := range []string{"ping", "read", "prog", "lsp"} {
		wait, bounded := c.answerWait(op)
		if !bounded || wait != 7*time.Millisecond {
			t.Errorf("answerWait(%q) = (%s, %v), want the readTimeout seam", op, wait, bounded)
		}
	}
	if wait, bounded := c.answerWait("recv"); bounded || wait != 0 {
		t.Errorf("answerWait(recv) = (%s, %v), want the seam to leave the long poll exempt", wait, bounded)
	}
}

// silentStreamServer accepts one connection, reads one request, answers it with
// a single non-final frame, then holds the connection open and silent. It is a
// live peer that stalls rather than a dead one, so the per-frame idle deadline
// is what has to end the wait. The address is a TCP one; cleanup drops it.
func silentStreamServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	var once sync.Once
	stop := func() { once.Do(func() { close(release); ln.Close() }) }
	t.Cleanup(stop)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		f, err := ReadFrame(conn)
		if err != nil {
			return
		}
		req, err := DecodeRequest(f)
		if err != nil {
			return
		}
		res := Response{ID: req.ID}
		if req.Op == "search" {
			res.Matches = []SearchMatch{{Path: "/w/a.go", Line: 1, Version: 1, Text: "needle"}}
		} else {
			res.Stream, res.Out = StreamStdout, "partial"
		}
		h, body := EncodeResponse(res)
		if WriteFrame(conn, h, body) != nil {
			return
		}
		<-release
	}()
	return TCPAddr(ln.Addr())
}

// A streaming verb must not be killed by a total deadline; a long search or
// exec is healthy. A peer that sends one frame and then stops is not, and the
// idle deadline, reset on every frame, must end the wait shortly after the
// configured idle with the same wrong-process wording Do gives a silent peer.
// It is then cleared, so a later recv on the same client still parks.
func TestStreamIdleNamesSilentPeer(t *testing.T) {
	cases := []struct {
		name string
		run  func(*Client, func()) error
	}{
		{"exec", func(c *Client, onFrame func()) error {
			_, err := c.DoExec(Request{Op: "exec", Argv: []string{"true"}},
				func(stream uint8, b string) { onFrame() })
			return err
		}},
		{"stream", func(c *Client, onFrame func()) error {
			_, err := c.DoStream(Request{Op: "search", Query: &SearchQuery{Text: "needle"}},
				func([]SearchMatch) { onFrame() })
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := Dial(silentStreamServer(t))
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			c.idleTimeout = 200 * time.Millisecond

			delivered := make(chan struct{})
			var once sync.Once
			onFrame := func() { once.Do(func() { close(delivered) }) }
			done := make(chan error, 1)
			go func() { done <- tc.run(c, onFrame) }()

			select {
			case <-delivered:
			case <-time.After(2 * time.Second):
				t.Fatal("the one frame never reached the client; the answer was not read")
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("a streamed request against a stalled peer returned no error")
				}
				if !strings.Contains(err.Error(), "no response") ||
					!strings.Contains(err.Error(), "another process") {
					t.Errorf("error = %q, want the silent-peer wording", err)
				}
				if !isTimeout(err) {
					t.Errorf("error = %v, want it distinguishable as a deadline expiry", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("a streamed request hung; the idle deadline was not armed")
			}

			// The idle must be cleared when the stream ends: a long poll on the
			// same client has to park, not inherit the expired deadline.
			parked := make(chan error, 1)
			go func() {
				_, err := c.Do(Request{Op: "recv"})
				parked <- err
			}()
			select {
			case err := <-parked:
				t.Fatalf("recv returned within a stream idle; the deadline was left armed: %v", err)
			case <-time.After(200 * time.Millisecond):
			}
			c.Close()
			select {
			case <-parked:
			case <-time.After(5 * time.Second):
				t.Fatal("closing the client did not end the parked recv")
			}
		})
	}
}

// A live stream must outlast the idle window as long as frames keep arriving:
// the deadline is reset before each frame, not armed once for the whole
// exchange. This server takes longer than the idle to finish, so a total
// deadline would fail it while a per-frame one delivers every frame.
func TestStreamIdleResetsPerFrame(t *testing.T) {
	const (
		frames = 8
		gap    = 30 * time.Millisecond
		idle   = 200 * time.Millisecond
	)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		f, err := ReadFrame(conn)
		if err != nil {
			return
		}
		req, err := DecodeRequest(f)
		if err != nil {
			return
		}
		for i := 0; i < frames; i++ {
			time.Sleep(gap)
			h, body := EncodeResponse(Response{ID: req.ID, Stream: StreamStdout, Out: "x"})
			if WriteFrame(conn, h, body) != nil {
				return
			}
		}
		time.Sleep(gap)
		h, body := EncodeResponse(Response{ID: req.ID, Final: true})
		WriteFrame(conn, h, body)
	}()

	c, err := Dial(TCPAddr(ln.Addr()))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.idleTimeout = idle

	start := time.Now()
	res, err := c.DoExec(Request{Op: "exec", Argv: []string{"true"}},
		func(stream uint8, b string) {})
	if err != nil {
		t.Fatalf("a stream that kept sending failed after %s with idle %s: %v",
			time.Since(start), idle, err)
	}
	if !res.Final {
		t.Errorf("the final frame was not marked final")
	}
	if want := "xxxxxxxx"; res.Out != want {
		t.Errorf("output = %q, want %q (every frame accumulated)", res.Out, want)
	}
	if elapsed := time.Since(start); elapsed <= idle {
		t.Errorf("stream finished in %s, inside one idle %s; it did not run long enough to prove a reset", elapsed, idle)
	}
}

// Every refusal must exit non-zero. An agent's shell tool reports the code, and
// a refusal on exit 0 is read as success — it will carry on as though the edit
// had landed.
func TestCLIRefusalsExitNonZero(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		args []string
		want string
	}{
		{"missing text", "abc\n", []string{"edit", "-old", "zzz", "-new", "x"}, "does not appear"},
		{"ambiguous", "x\nx\n", []string{"edit", "-old", "x", "-new", "y"}, "appears 2 times"},
		{"empty old", "abc\n", []string{"edit", "-old", "", "-new", "y"}, "required"},
		{"unknown buffer", "abc\n", []string{"read", "/w/nope.go"}, "no open buffer"},
		{"unknown command", "abc\n", []string{"frobnicate"}, "unknown command"},
		{"open with no path", "abc\n", []string{"open"}, "needs a path"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ed := newFakeEditor(t, map[string]string{"/w/a.go": c.doc})
			out, errs, code := run(t, c.args...)
			if code == 0 {
				t.Fatalf("exited 0 on a refusal; stdout %q", out)
			}
			if !strings.Contains(errs, c.want) {
				t.Errorf("stderr %q does not mention %q", errs, c.want)
			}
			if ed.docs["/w/a.go"] != c.doc {
				t.Errorf("buffer changed anyway: %q", ed.docs["/w/a.go"])
			}
		})
	}
}

// An empty diagnostics list only means "no problems" when a server produced
// it. A missing or not-yet-running server also has no diagnostics, and the old
// output — `{}` — was indistinguishable from a clean file.
func TestCLILSPDiagnosticsRefusesWhenThereIsNoServer(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "package a\n"})
	ed.lspJSON = `{"status":"no-server","detail":"no language server for this file type"}`
	out, errs, code := run(t, "lsp", "diagnostics", "/w/a.go")
	if code != 1 {
		t.Errorf("code = %d, want 1 when there is no server; stderr %q", code, errs)
	}
	if out != "" {
		t.Errorf("a refused diagnostics wrote %q to stdout", out)
	}
	if !strings.Contains(errs, "no language server for this file type") {
		t.Errorf("stderr = %q, want the reason", errs)
	}
}

// A clean file still exits 0, but says a server said so rather than printing an
// empty object that could mean anything.
func TestCLILSPDiagnosticsCleanCarriesAStatus(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "package a\n"})
	ed.lspJSON = `{"status":"ok"}`
	out, _, code := run(t, "lsp", "diagnostics", "/w/a.go")
	if code != 0 {
		t.Errorf("code = %d, want 0 for a clean file", code)
	}
	if strings.TrimSpace(out) == "{}" || !strings.Contains(out, `"status": "ok"`) {
		t.Errorf("clean diagnostics = %q, want a status and not an empty object", out)
	}
}

// An editor from before the status field answers `{}`; that is exactly the
// ambiguity this call cannot bless, so it is refused with a version-skew hint.
func TestCLILSPDiagnosticsOldServerIsRefused(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "package a\n"})
	ed.lspJSON = `{}`
	_, errs, code := run(t, "lsp", "diagnostics", "/w/a.go")
	if code != 1 {
		t.Errorf("code = %d, want 1 for an ambiguous empty answer", code)
	}
	if !strings.Contains(errs, "did not report a diagnostics status") {
		t.Errorf("stderr = %q, want the version-skew explanation", errs)
	}
}

// Inlay hints come back as JSON like the other lsp modes, and the plain form
// is line-oriented: one hint per line, its 1-based editor position then the
// label.
func TestCLILSPInlayHintsPlainAndJSON(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "package a\n"})
	ed.lspJSON = `{"hints":[{"line":2,"col":5,"text":"string","kind":1,"tooltip":"inferred type"}]}`
	out, errs, code := run(t, "lsp", "inlay-hints", "/w/a.go")
	if code != 0 {
		t.Fatalf("code = %d, stderr %q", code, errs)
	}
	if out != "2:5 string\n" {
		t.Errorf("plain output = %q, want %q", out, "2:5 string\n")
	}

	out, errs, code = run(t, "lsp", "inlay-hints", "/w/a.go", "-json")
	if code != 0 {
		t.Fatalf("-json code = %d, stderr %q", code, errs)
	}
	if !strings.Contains(out, `"text":"string"`) || !strings.Contains(out, `"line":2`) {
		t.Errorf("-json output = %q, want the structured hint", out)
	}
}

// -lines A,B restricts the request to a 1-based inclusive range, and the range
// reaches the host on the lspprep request. A bare A leaves the end unset, so
// the host reads to the end of the file.
func TestCLILSPInlayHintsLinesReachTheWire(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "package a\n"})
	ed.lspJSON = `{"hints":[]}`
	if _, errs, code := run(t, "lsp", "inlay-hints", "/w/a.go", "-lines", "3,9"); code != 0 {
		t.Fatalf("code = %d, stderr %q", code, errs)
	}
	if ed.lastLSP.LSPMode != "inlay-hints" {
		t.Errorf("mode = %q, want inlay-hints", ed.lastLSP.LSPMode)
	}
	if ed.lastLSP.LineStart == nil || *ed.lastLSP.LineStart != 3 {
		t.Errorf("LineStart = %v, want 3", ed.lastLSP.LineStart)
	}
	if ed.lastLSP.LineEnd == nil || *ed.lastLSP.LineEnd != 9 {
		t.Errorf("LineEnd = %v, want 9", ed.lastLSP.LineEnd)
	}

	if _, _, code := run(t, "lsp", "inlay-hints", "/w/a.go", "-lines", "4"); code != 0 {
		t.Fatalf("bare -lines code = %d", code)
	}
	if ed.lastLSP.LineStart == nil || *ed.lastLSP.LineStart != 4 {
		t.Errorf("bare -lines LineStart = %v, want 4", ed.lastLSP.LineStart)
	}
	if ed.lastLSP.LineEnd != nil {
		t.Errorf("bare -lines LineEnd = %v, want nil (read to the end)", ed.lastLSP.LineEnd)
	}
}

// A malformed -lines is a usage error, not a silent whole-file request.
func TestCLILSPInlayHintsBadLines(t *testing.T) {
	newFakeEditor(t, map[string]string{"/w/a.go": "package a\n"})
	_, errs, code := run(t, "lsp", "inlay-hints", "/w/a.go", "-lines", "0")
	if code != 2 {
		t.Errorf("code = %d, want 2 (usage) for -lines 0", code)
	}
	if !strings.Contains(errs, "--lines") {
		t.Errorf("stderr = %q, want the -lines hint", errs)
	}
}

// `lsp format` is a whole-file request: no position and no range, and the
// server's edit list round-trips as the structured `edits` field. Without the
// mode in the switch the call is refused as a usage error before the editor is
// asked.
func TestCLILSPFormatReachesTheWire(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "package a\n"})
	ed.lspJSON = `{"edits":[{"line":1,"col":1,"endLine":1,"endCol":3,"text":"  "}]}`
	out, errs, code := run(t, "lsp", "format", "/w/a.go")
	if code != 0 {
		t.Fatalf("code = %d, stderr %q", code, errs)
	}
	if ed.lastLSP.LSPMode != "format" {
		t.Errorf("mode = %q, want format", ed.lastLSP.LSPMode)
	}
	if ed.lastLSP.LineStart != nil || ed.lastLSP.LineEnd != nil {
		t.Errorf("whole-document format carried a range: %v..%v", ed.lastLSP.LineStart, ed.lastLSP.LineEnd)
	}
	if !strings.Contains(out, `"edits"`) || !strings.Contains(out, `"text": "  "`) {
		t.Errorf("output = %q, want the edit list", out)
	}
}

// `lsp range-format` needs its range: a collapsed range would ask the server to
// format nothing, so the CLI refuses before the host is reached, and the named
// lines then ride the wire as the 1-based inclusive range the range verbs use.
func TestCLILSPRangeFormatNeedsLinesAndSendsThem(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "package a\n"})
	ed.lspJSON = `{"edits":[]}`
	_, errs, code := run(t, "lsp", "range-format", "/w/a.go")
	if code != 2 {
		t.Errorf("code = %d, want 2 (usage) without -lines; stderr %q", code, errs)
	}
	if !strings.Contains(errs, "--lines") {
		t.Errorf("stderr = %q, want the -lines hint", errs)
	}

	if _, errs, code := run(t, "lsp", "range-format", "/w/a.go", "-lines", "3,9"); code != 0 {
		t.Fatalf("code = %d, stderr %q", code, errs)
	}
	if ed.lastLSP.LSPMode != "range-format" {
		t.Errorf("mode = %q, want range-format", ed.lastLSP.LSPMode)
	}
	if ed.lastLSP.LineStart == nil || *ed.lastLSP.LineStart != 3 {
		t.Errorf("LineStart = %v, want 3", ed.lastLSP.LineStart)
	}
	if ed.lastLSP.LineEnd == nil || *ed.lastLSP.LineEnd != 9 {
		t.Errorf("LineEnd = %v, want 9", ed.lastLSP.LineEnd)
	}
}

// The signature mode is a structured position request like the others: it
// carries the 1-based line and column and reaches the wire as its own
// sub-operation. Without the mode in the switch it is refused as a usage error
// before the editor is ever asked — the failure is in raj, not in the answer.
func TestCLILSPSignatureReachesTheWire(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "package a\n"})
	ed.lspJSON = `{"signatures":[{"label":"F(x int)","parameters":[{"label":"x"}]}],"activeSignature":0,"activeParameter":0}`
	out, errs, code := run(t, "lsp", "signature", "/w/a.go", "1:2")
	if code != 0 {
		t.Fatalf("code = %d, stderr %q", code, errs)
	}
	if ed.lastLSP.LSPMode != "signature" {
		t.Errorf("mode = %q, want signature", ed.lastLSP.LSPMode)
	}
	if ed.lastLSP.Line != 1 || ed.lastLSP.Col != 2 {
		t.Errorf("position = %d:%d, want 1:2", ed.lastLSP.Line, ed.lastLSP.Col)
	}
	if !strings.Contains(out, `"signatures"`) || !strings.Contains(out, `"label": "F(x int)"`) {
		t.Errorf("output = %q, want the signature list", out)
	}
}

// `lsp symbols` takes the query as a positional: it rides the request's Query
// field when present and stays absent when the caller names none, so the server
// can tell "match this" from "match nothing in particular".
func TestCLILSPWorkspaceSymbolsSendsQuery(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "package a\n"})
	ed.lspJSON = `{"symbols":[{"name":"Reader","kind":"interface","path":"/w/a.go","line":1,"col":6}]}`
	if _, errs, code := run(t, "lsp", "symbols", "/w/a.go", "1:2"); code != 0 {
		t.Fatalf("code = %d, stderr %q", code, errs)
	}
	if ed.lastLSP.LSPMode != "symbols" {
		t.Errorf("mode = %q, want symbols", ed.lastLSP.LSPMode)
	}
	if ed.lastLSP.Query != nil {
		t.Errorf("query = %+v, want absent for an empty query", ed.lastLSP.Query)
	}
	if _, errs, code := run(t, "lsp", "symbols", "/w/a.go", "1:2", "Reader"); code != 0 {
		t.Fatalf("code = %d, stderr %q", code, errs)
	}
	if ed.lastLSP.Query == nil || ed.lastLSP.Query.Text != "Reader" {
		t.Errorf("query = %+v, want Reader", ed.lastLSP.Query)
	}
}

// `lsp document-symbols` asks about a file, not a place, and its mode reaches
// the wire so the editor runs textDocument/documentSymbol. A mode the CLI did
// not accept would be refused before the editor was ever asked, and a position
// requirement would make the verb unusable for a whole-file request.
func TestCLILSPDocumentSymbolsReachesTheWire(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "package a\n"})
	ed.lspJSON = `{"documentSymbols":[{"name":"Reader","kind":"interface","path":"/w/a.go","line":1,"col":6,"children":[{"name":"Read","kind":"method","path":"/w/a.go","line":3,"col":10}]}]}`
	out, errs, code := run(t, "lsp", "document-symbols", "/w/a.go")
	if code != 0 {
		t.Fatalf("code = %d, stderr %q", code, errs)
	}
	if ed.lastLSP.LSPMode != "document-symbols" {
		t.Errorf("mode = %q, want document-symbols", ed.lastLSP.LSPMode)
	}
	if ed.lastLSP.Line != 0 || ed.lastLSP.Col != 0 {
		t.Errorf("position = %d:%d, want no position", ed.lastLSP.Line, ed.lastLSP.Col)
	}
	if !strings.Contains(out, `"name": "Reader"`) {
		t.Errorf("output = %q, want the symbol", out)
	}
	if !strings.Contains(out, `"name": "Read"`) {
		t.Errorf("output = %q, want the nested child kept in the tree", out)
	}
}

// The sibling jump modes are accepted by the CLI and reach the wire as their
// own sub-operation with the 1-based position. Without them in the mode switch,
// each is refused as a usage error before the editor is ever asked — the
// failure is in raj, not in the answer.
func TestCLILSPSiblingJumpModesReachTheWire(t *testing.T) {
	for _, mode := range []string{"declaration", "type-definition", "implementation"} {
		ed := newFakeEditor(t, map[string]string{"/w/a.go": "package a\n"})
		ed.lspJSON = `{"locations":[]}`
		if _, errs, code := run(t, "lsp", mode, "/w/a.go", "1:2"); code != 0 {
			t.Fatalf("%s: code = %d, stderr %q", mode, code, errs)
		}
		if ed.lastLSP.LSPMode != mode {
			t.Errorf("mode = %q, want %q", ed.lastLSP.LSPMode, mode)
		}
		if ed.lastLSP.Line != 1 || ed.lastLSP.Col != 2 {
			t.Errorf("%s: position = %d:%d, want 1:2", mode, ed.lastLSP.Line, ed.lastLSP.Col)
		}
	}
}

// A bare positional is the pattern typed in the wrong place: search takes
// no path, so accepting it silently would run a different search than was
// meant — the refusal names both flags the caller might have wanted.
func TestSearchRefusesBareArgument(t *testing.T) {
	newFakeEditor(t, map[string]string{"/w/a.go": "needle\n"})
	out, errs, code := run(t, "search", "needle")
	if code != 2 {
		t.Errorf("code = %d, want 2 (usage); stdout %q", code, out)
	}
	want := `search: unexpected argument "needle" — the pattern goes to -q; to limit paths use --include or --path`
	if !strings.Contains(errs, want) {
		t.Errorf("stderr = %q, want %q", errs, want)
	}
}

// An -include glob that matches nothing looks exactly like "no matches"
// without a warning, and the two call for opposite fixes.
func TestSearchWarnsWhenIncludeMatchesNoFiles(t *testing.T) {
	newFakeEditor(t, map[string]string{"/w/a.go": "needle\n"})
	_, errs, code := run(t, "search", "-q", "needle", "-include", "*.zzz")
	if code != 1 {
		t.Errorf("code = %d, want 1 (no hits)", code)
	}
	if !strings.Contains(errs, "search: warning: --include pattern(s) matched no files") {
		t.Errorf("stderr = %q, want the include warning", errs)
	}
}

// The control cases: a glob that does match files stays quiet, and a no-hit
// search without -include stays quiet too.
func TestSearchIncludeWarningStaysQuietWhenFilesWereSearched(t *testing.T) {
	newFakeEditor(t, map[string]string{"/w/a.go": "needle\n"})
	_, errs, code := run(t, "search", "-q", "needle", "-include", "*.go")
	if code != 0 {
		t.Errorf("code = %d, want 0; stderr %q", code, errs)
	}
	if strings.Contains(errs, "--include") {
		t.Errorf("stderr = %q, want no include warning", errs)
	}

	_, errs, code = run(t, "search", "-q", "absent")
	if code != 1 {
		t.Errorf("code = %d, want 1 (no hits)", code)
	}
	if strings.Contains(errs, "--include") {
		t.Errorf("stderr = %q, want no include warning", errs)
	}
}

// A literal pattern with regex metacharacters that matches nothing is usually a
// regex typed without -regex; the hint names the flag rather than leaving the
// caller to guess why a pattern that reads like a regex found nothing.
func TestSearchHintsRegexOnMetacharacterMiss(t *testing.T) {
	newFakeEditor(t, map[string]string{"/w/a.go": "func f() {}\n"})

	_, errs, code := run(t, "search", "-q", `func (a \*App)`)
	if code != 1 {
		t.Errorf("code = %d, want 1 (no hits)", code)
	}
	if !strings.Contains(errs, "--regex") {
		t.Errorf("stderr = %q, want the -regex hint", errs)
	}

	// A plain literal with no metacharacters stays quiet: it really is absent.
	_, errs, code = run(t, "search", "-q", "absent")
	if code != 1 || strings.Contains(errs, "--regex") {
		t.Errorf("plain miss: code %d, stderr %q, want no hint", code, errs)
	}

	// A regex that matches prints no hint.
	_, errs, code = run(t, "search", "-q", "func", "-regex")
	if code != 0 || strings.Contains(errs, "--regex") {
		t.Errorf("-regex hit: code %d, stderr %q, want no hint", code, errs)
	}
}

func TestCLIEditAll(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "x\nx\nx\n"})
	if _, errs, code := run(t, "edit", "-old", "x", "-new", "y", "-all"); code != 0 {
		t.Fatalf("code %d: %s", code, errs)
	}
	if got := ed.docs["/w/a.go"]; got != "y\ny\ny\n" {
		t.Errorf("buffer = %q", got)
	}
}

// The safety property, end to end through the CLI: if the buffer moves between
// the read and the apply, nothing is written and the message says to re-read.
func TestCLIEditRefusesWhenTheBufferMoved(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "hello world\n"})
	orig := ed.docs["/w/a.go"]
	ed.bump = func() { ed.vers["/w/a.go"]++ } // the user types, mid-edit

	out, errs, code := run(t, "edit", "-old", "world", "-new", "socket")
	if code == 0 {
		t.Fatalf("stale edit reported success: %q", out)
	}
	if !strings.Contains(errs, "again") {
		t.Errorf("message %q does not tell the caller to re-read", errs)
	}
	if ed.docs["/w/a.go"] != orig {
		t.Errorf("refused but wrote anyway: %q", ed.docs["/w/a.go"])
	}
}

// A hunk a lease refused names the change set that owns the text — a decision
// for the user rather than a re-read for the driver — and the -json form
// carries the same group and reason.
func TestCLIRefusalNamesTheLeaseOwner(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "hello world\n"})
	ed.lease, ed.leaseAuthor, ed.leaseStart, ed.leaseEnd = 7, 4, 6, 11
	ed.bump = func() { ed.vers["/w/a.go"]++ } // force the apply path to conflict

	_, errs, code := run(t, "edit", "-old", "world", "-new", "socket")
	if code == 0 {
		t.Fatalf("a leased edit reported success")
	}
	if !strings.Contains(errs, "change set 7 owns this text") {
		t.Errorf("message %q does not name the lease owner", errs)
	}
	if !strings.Contains(errs, "bytes 6..11") {
		t.Errorf("message %q does not name the leased span", errs)
	}
	if !strings.Contains(errs, "accept or reject it first") {
		t.Errorf("message %q does not point at the decision", errs)
	}

	out, _, code := run(t, "edit", "-old", "world", "-new", "socket", "-json")
	if code == 0 {
		t.Fatalf("a leased edit reported success in json")
	}
	if !strings.Contains(out, `"group": 7`) || !strings.Contains(out, "change set 7 owns this text") {
		t.Errorf("json = %q, want the group and the lease message", out)
	}
	for _, want := range []string{`"author": 4`, `"start": 6`, `"end": 11`} {
		if !strings.Contains(out, want) {
			t.Errorf("json = %q, want the owner/%s", out, want)
		}
	}
}

// A conflict whose author is the caller is necessarily own+other: a hunk over
// only the caller's own Proposed set joins it instead of refusing
// (Session.ApplyDiff -> commitInto), so a refusal that names the caller means
// a peer's run is in the way too. The caller cannot accept or reject its own
// draft, so the message names the draft and the remedy it has — narrow the
// hunk. Modelled on TestCLIRefusalNamesTheLeaseOwner, the peer half.
func TestCLIRefusalNamesTheCallersOwnDraft(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "hello world\n"})
	ed.lease, ed.leaseStart, ed.leaseEnd, ed.ownLease = 7, 6, 11, true
	ed.bump = func() { ed.vers["/w/a.go"]++ } // force the apply path to conflict

	_, errs, code := run(t, "edit", "-old", "world", "-new", "socket")
	if code == 0 {
		t.Fatalf("an own+other edit reported success")
	}
	for _, want := range []string{
		"change set 7 is your own draft", "bytes 6..11", "narrow it to avoid their span",
	} {
		if !strings.Contains(errs, want) {
			t.Errorf("message %q does not say %q", errs, want)
		}
	}
	if strings.Contains(errs, "accept or reject it first") {
		t.Errorf("own+other message %q tells the caller to decide its own draft", errs)
	}

	out, _, code := run(t, "edit", "-old", "world", "-new", "socket", "-json")
	if code == 0 {
		t.Fatalf("an own+other edit reported success in json")
	}
	for _, want := range []string{`"group": 7`, `"start": 6`, `"end": 11`, "is your own draft"} {
		if !strings.Contains(out, want) {
			t.Errorf("json = %q, want %q", out, want)
		}
	}
	if strings.Contains(out, "accept or reject it first") {
		t.Errorf("own+other json = %q still tells the caller to decide its own draft", out)
	}
}

// A conflict whose author is another writer keeps the peer wording exactly: the
// caller decides a peer's set with accept or reject, and this change must not
// soften or reword that. Modelled on TestCLIRefusalNamesTheLeaseOwner.
//
// This is a guard, not a pin: the peer branch is untouched, so it passes before
// the change too; it fails only if the own-draft rewording leaks into the peer
// path.
func TestCLIRefusalKeepsThePeerWording(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "hello world\n"})
	ed.lease, ed.leaseAuthor, ed.leaseStart, ed.leaseEnd = 7, 4, 6, 11
	ed.bump = func() { ed.vers["/w/a.go"]++ }

	_, errs, code := run(t, "edit", "-old", "world", "-new", "socket")
	if code == 0 {
		t.Fatalf("a peer refusal reported success")
	}
	want := "change set 7 owns this text (author 4, bytes 6..11); accept or reject it first"
	if !strings.Contains(errs, want) {
		t.Errorf("message %q does not carry the peer sentence %q", errs, want)
	}
	if strings.Contains(errs, "your own draft") {
		t.Errorf("peer message %q was reworded as the caller's own draft", errs)
	}
}

// A successful apply over another writer Proposed span prints the superseded set
// as a note, and the -json form carries the warnings beside ok:true: the apply
// landed, and the set it moved past is data the driver can act on. Modelled on
// TestCLIRefusalNamesTheLeaseOwner, which is the refusal half.
func TestCLIApplyNotesOverlapWarning(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "hello world\n"})
	ed.warnings = []GroupOverlap{{Group: 7, Author: 3, Start: 6, End: 12}}

	out, errs, code := run(t, "apply", "/w/a.go", "-base", "1", "-start", "0", "-end", "0", "--", "x")
	if code != 0 {
		t.Fatalf("code %d: %s", code, errs)
	}
	if !strings.Contains(out, "note: overlapped change set 7 (author 3, bytes 6..12)") {
		t.Errorf("output = %q, want the overlap note", out)
	}

	ed.docs["/w/a.go"], ed.vers["/w/a.go"] = "hello world\n", 1
	// -json is a flag, so it goes before -- ; after the terminator it would be
	// read as a positional (the reason the CLI exits 2 on an unexpected arg).
	jout, _, code := run(t, "apply", "/w/a.go", "-base", "1", "-start", "0", "-end", "0", "-json", "--", "x")
	if code != 0 {
		t.Fatalf("json code %d", code)
	}
	if !strings.Contains(jout, `"warnings"`) || !strings.Contains(jout, `"group": 7`) ||
		!strings.Contains(jout, `"author": 3`) {
		t.Errorf("json = %q, want the warning carried", jout)
	}
}

// A batch can refuse one hunk and still land another over a Proposed set. The
// refusal is the exit status, but the landed overlap must not be lost to the
// early return that reports the conflicts: the note prints on stderr and the
// ok:false -json carries both conflicts and warnings. Modelled on
// TestCLIRefusalNamesTheLeaseOwner (the refusal half) and on
// TestCLIApplyNotesOverlapWarning (the warning half).
func TestCLIApplyRefusalStillCarriesTheWarning(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "hello world\n"})
	ed.lease, ed.leaseAuthor, ed.leaseStart, ed.leaseEnd = 7, 4, 6, 11
	ed.warnings = []GroupOverlap{{Group: 9, Author: 3, Start: 2, End: 4}}
	ed.bump = func() { ed.vers["/w/a.go"]++ } // force the apply path to conflict

	_, errs, code := run(t, "apply", "/w/a.go", "-base", "1", "-start", "0", "-end", "3", "--", "x")
	if code == 0 {
		t.Fatalf("a partly refused apply reported success")
	}
	if !strings.Contains(errs, "change set 7 owns this text") {
		t.Errorf("stderr = %q, want the lease refusal", errs)
	}
	if !strings.Contains(errs, "note: overlapped change set 9 (author 3, bytes 2..4)") {
		t.Errorf("stderr = %q, want the note for the hunk that landed", errs)
	}

	out, _, code := run(t, "apply", "/w/a.go", "-base", "1", "-start", "0", "-end", "3", "-json", "--", "x")
	if code == 0 {
		t.Fatalf("a partly refused apply reported success in json")
	}
	for _, want := range []string{`"ok": false`, `"conflicts"`, `"warnings"`, `"group": 9`, `"author": 3`} {
		if !strings.Contains(out, want) {
			t.Errorf("json = %q, want %q", out, want)
		}
	}
}

// A conflict with no lease owner keeps the resubmit wording, in the text and
// in the JSON.
func TestCLIRefusalWithoutALeaseKeepsTheStaleMessage(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "hello world\n"})
	ed.bump = func() { ed.vers["/w/a.go"]++ }

	_, errs, code := run(t, "edit", "-old", "world", "-new", "socket")
	if code == 0 {
		t.Fatalf("a stale edit reported success")
	}
	if !strings.Contains(errs, "could not be placed") {
		t.Errorf("message %q lost the stale-offset wording", errs)
	}

	out, _, code := run(t, "edit", "-old", "world", "-new", "socket", "-json")
	if code == 0 {
		t.Fatalf("a stale edit reported success in json")
	}
	if !strings.Contains(out, "could not be placed") {
		t.Errorf("json = %q lost the stale-offset wording", out)
	}
	if strings.Contains(out, `"group"`) {
		t.Errorf("json = %q invented a lease group", out)
	}
}

// patch routes a lease refusal through the same owner-and-span reporting apply
// and edit use, in the text and in the -json block, rather than the old
// "dump again" wording that a lease does not call for: the caller is being
// asked to decide about another writer's text, not to redo its own edit.
func TestCLIPatchReportsALeaseRefusal(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "hello world\n"})
	ed.patchConflict = []Conflict{{Index: 0, Group: 7, Author: 4, Start: 6, End: 11,
		Hunk: Hunk{Start: 6, End: 11, Text: "socket"}}}

	_, errs, code := run(t, "patch", "/w/a.go", "-dump", "1", "-text", "hello socket\n")
	if code == 0 {
		t.Fatalf("a leased patch reported success")
	}
	for _, want := range []string{"change set 7 owns this text", "bytes 6..11", "accept or reject it first"} {
		if !strings.Contains(errs, want) {
			t.Errorf("message %q does not name %q", errs, want)
		}
	}
	if strings.Contains(strings.ToLower(errs), "dump again") {
		t.Errorf("message %q still tells the caller to dump again", errs)
	}

	out, _, code := run(t, "patch", "/w/a.go", "-dump", "1", "-text", "hello socket\n", "-json")
	if code == 0 {
		t.Fatalf("a leased patch reported success in json")
	}
	for _, want := range []string{`"ok": false`, `"group": 7`, `"author": 4`, `"start": 6`, `"end": 11`, "change set 7 owns this text"} {
		if !strings.Contains(out, want) {
			t.Errorf("json = %q, want %q", out, want)
		}
	}
}

// A patch conflict with no lease owner is the ordinary moved snapshot, and
// keeps the re-read wording in the text and in the -json block.
func TestCLIPatchReportsAStaleConflict(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "hello world\n"})
	ed.patchConflict = []Conflict{{Index: 0, Hunk: Hunk{Start: 6, End: 11, Text: "socket"}}}

	_, errs, code := run(t, "patch", "/w/a.go", "-dump", "1", "-text", "hello socket\n")
	if code == 0 {
		t.Fatalf("a stale patch reported success")
	}
	if !strings.Contains(errs, "could not be placed") {
		t.Errorf("message %q lost the stale-offset wording", errs)
	}
	out, _, code := run(t, "patch", "/w/a.go", "-dump", "1", "-text", "hello socket\n", "-json")
	if code == 0 {
		t.Fatalf("a stale patch reported success in json")
	}
	if !strings.Contains(out, "could not be placed") || !strings.Contains(out, `"ok": false`) {
		t.Errorf("json = %q lost the stale-offset wording", out)
	}
}

// A successful patch over another writer Proposed span prints the superseded
// set as a note, and the -json form carries the warnings beside ok:true: the
// patch landed, and the set it moved past is data the driver can act on.
// Modelled on TestCLIApplyNotesOverlapWarning, the apply half.
func TestCLIPatchNotesOverlapWarning(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "hello world\n"})
	ed.warnings = []GroupOverlap{{Group: 7, Author: 3, Start: 6, End: 12}}

	out, errs, code := run(t, "patch", "/w/a.go", "-dump", "1", "-text", "hello socket\n")
	if code != 0 {
		t.Fatalf("code %d: %s", code, errs)
	}
	if !strings.Contains(out, "note: overlapped change set 7 (author 3, bytes 6..12)") {
		t.Errorf("output = %q, want the overlap note", out)
	}

	jout, _, code := run(t, "patch", "/w/a.go", "-dump", "1", "-text", "hello socket\n", "-json")
	if code != 0 {
		t.Fatalf("json code %d", code)
	}
	if !strings.Contains(jout, `"warnings"`) || !strings.Contains(jout, `"group": 7`) ||
		!strings.Contains(jout, `"author": 3`) {
		t.Errorf("json = %q, want the warning carried", jout)
	}
}

// patch shares the diff path, so a batch with both a refusal and a landed
// warning reports the warning too: the note prints on stderr and the ok:false
// -json carries conflicts and warnings. Modelled on
// TestCLIPatchReportsALeaseRefusal (the refusal half) and on
// TestCLIPatchNotesOverlapWarning (the warning half).
func TestCLIPatchRefusalStillCarriesTheWarning(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "hello world\n"})
	ed.patchConflict = []Conflict{{Index: 0, Group: 7, Author: 4, Start: 6, End: 11,
		Hunk: Hunk{Start: 6, End: 11, Text: "socket"}}}
	ed.warnings = []GroupOverlap{{Group: 9, Author: 3, Start: 2, End: 4}}

	_, errs, code := run(t, "patch", "/w/a.go", "-dump", "1", "-text", "hello socket\n")
	if code == 0 {
		t.Fatalf("a partly refused patch reported success")
	}
	if !strings.Contains(errs, "change set 7 owns this text") {
		t.Errorf("stderr = %q, want the lease refusal", errs)
	}
	if !strings.Contains(errs, "note: overlapped change set 9 (author 3, bytes 2..4)") {
		t.Errorf("stderr = %q, want the note for the hunk that landed", errs)
	}

	out, _, code := run(t, "patch", "/w/a.go", "-dump", "1", "-text", "hello socket\n", "-json")
	if code == 0 {
		t.Fatalf("a partly refused patch reported success in json")
	}
	for _, want := range []string{`"ok": false`, `"conflicts"`, `"warnings"`, `"group": 9`, `"author": 3`} {
		if !strings.Contains(out, want) {
			t.Errorf("json = %q, want %q", out, want)
		}
	}
}

// A zero-width apply with no text changes nothing, so it is refused before it
// can become a change set; an empty -text-file at the same span is the same
// no-op. A real deletion (a non-empty span, empty text) still goes through.
func TestCLIApplyRefusesANoOp(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "hello world\n"})

	out, errs, code := run(t, "apply", "/w/a.go", "-base", "1", "-start", "0", "-end", "0", "-text", "")
	if code != 2 || out != "" {
		t.Errorf("no-op apply = code %d, stdout %q; want a usage refusal", code, out)
	}
	if !strings.Contains(errs, "nothing to apply") {
		t.Errorf("stderr = %q, want the no-op refusal", errs)
	}
	if ed.docs["/w/a.go"] != "hello world\n" || ed.vers["/w/a.go"] != 1 {
		t.Errorf("refused no-op wrote: %q v%d", ed.docs["/w/a.go"], ed.vers["/w/a.go"])
	}

	empty := filepath.Join(t.TempDir(), "empty.txt")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_, errs, code = run(t, "apply", "/w/a.go", "-base", "1", "-start", "3", "-end", "3", "-text-file", empty)
	if code != 2 || !strings.Contains(errs, "nothing to apply") {
		t.Errorf("empty -text-file no-op = code %d, stderr %q", code, errs)
	}

	if _, errs, code = run(t, "apply", "/w/a.go", "-base", "1", "-start", "5", "-end", "6", "-text", ""); code != 0 {
		t.Fatalf("a real deletion was refused: code %d, stderr %q", code, errs)
	}
	if got := ed.docs["/w/a.go"]; got != "helloworld\n" {
		t.Errorf("buffer = %q, want the deletion applied", got)
	}
}

// A batch of only no-op hunks is refused; a batch mixing real and no-op hunks
// sends only the real ones, so a no-op cannot ride in as a change set.
func TestCLIApplyHunksDropsNoOps(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "hello world\n"})

	allNoop := filepath.Join(t.TempDir(), "noop.jsonl")
	if err := os.WriteFile(allNoop, []byte("{\"start\":0,\"end\":0,\"text\":\"\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, errs, code := run(t, "apply", "/w/a.go", "-base", "1", "-hunks", allNoop)
	if code != 1 || !strings.Contains(errs, "every hunk is a no-op") {
		t.Errorf("all-no-op batch = code %d, stderr %q", code, errs)
	}
	if ed.vers["/w/a.go"] != 1 {
		t.Errorf("all-no-op batch advanced the version to %d", ed.vers["/w/a.go"])
	}

	mixed := filepath.Join(t.TempDir(), "mixed.jsonl")
	if err := os.WriteFile(mixed, []byte(
		"{\"start\":0,\"end\":0,\"text\":\"\"}\n"+
			"{\"start\":6,\"end\":11,\"text\":\"socket\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, errs, code = run(t, "apply", "/w/a.go", "-base", "1", "-hunks", mixed)
	if code != 0 {
		t.Fatalf("mixed batch refused: code %d, stderr %q", code, errs)
	}
	if got := ed.docs["/w/a.go"]; got != "hello socket\n" {
		t.Errorf("buffer = %q, want only the real hunk applied", got)
	}
}

// A wedged clear is refused with the live set that overlaps it — its author and
// span, not only the group — so the caller can re-propose, in text and in the
// -json block.
func TestCLIClearReportsTheBlockingOverlap(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "hello world\n"})
	ed.clearBlock = map[uint64]Conflict{
		3: {Group: 9, Author: 4, Start: 6, End: 11},
	}

	_, errs, code := run(t, "clear", "-group", "3")
	if code == 0 {
		t.Fatalf("a wedged clear reported success")
	}
	if !strings.Contains(errs, "cannot be cleared") || !strings.Contains(errs, "change set 9") {
		t.Errorf("message %q does not name the blocking set", errs)
	}

	out, _, code := run(t, "clear", "-group", "3", "-json")
	if code == 0 {
		t.Fatalf("a wedged clear reported success in json")
	}
	for _, want := range []string{`"ok": false`, `"block"`, `"group": 9`, `"author": 4`, `"start": 6`, `"end": 11`} {
		if !strings.Contains(out, want) {
			t.Errorf("json = %q, want %s", out, want)
		}
	}
}

// revert is the inverse of attribution, and the CLI accepts it for the
// connection's own pieces only: -mine, or -author naming the connection's own
// id. Another writer is refused before the request is sent, because dropping a
// peer's accepted pieces is the user's decision (reject then clear).
func TestCLIRevertRefusesAnotherWriter(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "x"})

	// Author 1 is the human; the connection writes as an agent id, so naming it
	// is the peer-drop this verb deliberately refuses.
	_, errs, code := run(t, "revert", "-author", "1", "/w/a.go")
	if code == 0 {
		t.Fatalf("reverting the user's pieces reported success")
	}
	if !strings.Contains(errs, "not this connection") {
		t.Errorf("refusal = %q, want the authorization message", errs)
	}
	if ed.lastRevert.Op != "" {
		t.Errorf("a refused revert reached the wire: %+v", ed.lastRevert)
	}
}

// revert reaches the wire as its own verb, carrying the path it was given.
func TestCLIRevertSendsTheVerb(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "x"})

	out, errs, code := run(t, "revert", "-mine", "/w/a.go")
	if code != 0 {
		t.Fatalf("revert -mine code %d: %s", code, errs)
	}
	if ed.lastRevert.Op != "revert" || ed.lastRevert.Path != "/w/a.go" {
		t.Errorf("wire carried %+v, want a revert of /w/a.go", ed.lastRevert)
	}
	if !strings.Contains(out, "reverted") {
		t.Errorf("revert printed %q, want the confirmation", out)
	}
}

// `groups` shows an overlap on the set that carries it, in the -json listing a
// script reads.
func TestCLIGroupsShowsOverlaps(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "hello world\n"})
	ed.groups = []Group{
		{ID: 4, Path: "/w/a.go", Author: 2, State: "proposed", Ops: 1},
		{ID: 5, Path: "/w/a.go", Author: 3, State: "proposed", Ops: 1,
			Overlaps: &GroupOverlaps{Sets: []GroupOverlap{{Group: 4, Author: 2, Start: 6, End: 11}}}},
	}

	out, _, code := run(t, "groups", "-json")
	if code != 0 {
		t.Fatalf("groups -json: exit %d", code)
	}
	for _, want := range []string{`"id": 5`, `"overlaps"`, `"group": 4`} {
		if !strings.Contains(out, want) {
			t.Errorf("groups json = %q, want %s", out, want)
		}
	}
}

// `groups` marks a superseded proposal invalid and names the live set that
// consumed it, over the wire and in the -json listing a script reads. Modelled
// on TestCLIGroupsShowsOverlaps, which is the same fixture for overlaps.
func TestCLIGroupsShowsInvalid(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "hello world\n"})
	ed.groups = []Group{
		{ID: 4, Path: "/w/a.go", Author: 2, State: "proposed", Ops: 1,
			Invalid: true, InvalidBy: &GroupOverlap{Group: 5, Author: 3, Start: 6, End: 11}},
	}

	out, _, code := run(t, "groups", "-json")
	if code != 0 {
		t.Fatalf("groups -json: exit %d", code)
	}
	for _, want := range []string{`"id": 4`, `"invalid": true`, `"invalid_by"`, `"group": 5`} {
		if !strings.Contains(out, want) {
			t.Errorf("groups json = %q, want %s", out, want)
		}
	}
}

// -path reaches the wire as a query field; the real walk's scoping and its
// refusal of a path outside the root are exercised in the app tests.
func TestCLISearchPathReachesTheQuery(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "needle\n"})
	if _, errs, code := run(t, "search", "-q", "needle", "-path", "internal"); code != 0 {
		t.Fatalf("search -path: code %d: %s", code, errs)
	}
	ed.mu.Lock()
	got := ed.searchPath
	ed.mu.Unlock()
	if got != "internal" {
		t.Errorf("search path = %q, want internal", got)
	}
}

// Multi-line replacements come from files, since quoting them through a shell is
// where an agent's edits get mangled.
func TestCLIEditFromFiles(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "a\n\tif err != nil {\n\t\treturn err\n\t}\nb\n"})
	dir := t.TempDir()
	oldF := filepath.Join(dir, "old")
	newF := filepath.Join(dir, "new")
	os.WriteFile(oldF, []byte("\tif err != nil {\n\t\treturn err\n\t}\n"), 0o644)
	os.WriteFile(newF, []byte("\tif err != nil {\n\t\treturn wrap(err)\n\t}\n"), 0o644)

	if _, errs, code := run(t, "edit", "-old-file", oldF, "-new-file", newF); code != 0 {
		t.Fatalf("code %d: %s", code, errs)
	}
	if !strings.Contains(ed.docs["/w/a.go"], "wrap(err)") {
		t.Errorf("buffer = %q", ed.docs["/w/a.go"])
	}
}

func TestCLIFlagAndFileAreAlternatives(t *testing.T) {
	newFakeEditor(t, map[string]string{"/w/a.go": "x\n"})
	f := filepath.Join(t.TempDir(), "old")
	os.WriteFile(f, []byte("x"), 0o644)
	if _, errs, code := run(t, "edit", "-old", "x", "-old-file", f, "-new", "y"); code != 2 {
		t.Errorf("code %d, want a usage error; stderr %q", code, errs)
	}
}

func TestCLIJSON(t *testing.T) {
	newFakeEditor(t, map[string]string{"/w/a.go": "hello\n"})
	out, _, code := run(t, "buffers", "-json")
	if code != 0 || !strings.Contains(out, `"path"`) {
		t.Errorf("json buffers = %q", out)
	}
	out, _, code = run(t, "read", "-json", "/w/a.go")
	if code != 0 || !strings.Contains(out, `"version"`) {
		t.Errorf("json read = %q", out)
	}
}

// claim sets, extends, clears and reports the working set, and the flags reach
// the wire as the request fields the host reads.
func TestCLIClaim(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "a\n", "/w/b.go": "b\n"})

	out, errs, code := run(t, "claim", "/w/a.go", "/w/b.go")
	if code != 0 {
		t.Fatalf("claim code %d: %s", code, errs)
	}
	if strings.Join(ed.lastClaim.Paths, ",") != "/w/a.go,/w/b.go" ||
		ed.lastClaim.ClaimAdd || ed.lastClaim.ClaimClear {
		t.Errorf("claim request = %+v", ed.lastClaim)
	}
	if !strings.Contains(out, "/w/a.go") || !strings.Contains(out, "/w/b.go") {
		t.Errorf("claim output = %q, want both paths", out)
	}

	// -add extends, and the CLI prints the resulting set.
	out, errs, code = run(t, "claim", "-add", "/w/c.go")
	if code != 0 || !ed.lastClaim.ClaimAdd {
		t.Fatalf("claim -add code %d, request %+v: %s", code, ed.lastClaim, errs)
	}
	if !strings.Contains(out, "/w/c.go") || !strings.Contains(out, "/w/a.go") {
		t.Errorf("claim -add output = %q, want the extended set", out)
	}

	// report: no operands and no flags.
	ed.lastClaim = Request{}
	out, _, code = run(t, "claim")
	if code != 0 || len(ed.lastClaim.Paths) != 0 || ed.lastClaim.ClaimAdd || ed.lastClaim.ClaimClear {
		t.Errorf("claim report request = %+v", ed.lastClaim)
	}
	if !strings.Contains(out, "claimed") {
		t.Errorf("claim report output = %q", out)
	}

	// -clear releases the set.
	out, errs, code = run(t, "claim", "-clear")
	if code != 0 || !ed.lastClaim.ClaimClear {
		t.Fatalf("claim -clear code %d, request %+v: %s", code, ed.lastClaim, errs)
	}
	if !strings.Contains(out, "empty") {
		t.Errorf("claim -clear output = %q, want an empty-set note", out)
	}
}

// The two mode flags are alternatives, and a warning and an overlap are
// surfaced rather than swallowed.
func TestCLIClaimFlagsAndReports(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "a\n"})
	if _, errs, code := run(t, "claim", "-add", "-clear"); code != 2 || !strings.Contains(errs, "alternatives") {
		t.Errorf("claim -add -clear: code %d, stderr %q", code, errs)
	}

	// The per-verb help names the operand, and does not claim a missing path
	// targets the buffer on screen — claim reports instead.
	if _, errs, code := run(t, "claim", "-h"); code != 0 ||
		!strings.Contains(errs, "raj ctl claim [path]...") || strings.Contains(errs, "looking at") {
		t.Errorf("claim -h: code %d, stderr %q", code, errs)
	}

	ed.claimWarnings = []string{"/w/gone.go skipped: no such file"}
	ed.claimOverlaps = []ClaimOverlap{{Path: "/w/a.go", Identity: "bob", Author: 3}}
	out, errs, code := run(t, "claim", "/w/a.go")
	if code != 0 {
		t.Fatalf("claim code %d: %s", code, errs)
	}
	if !strings.Contains(errs, "gone.go") || !strings.Contains(errs, "bob") {
		t.Errorf("claim warnings/overlaps = stdout %q stderr %q", out, errs)
	}

	// -json carries the same three parts structurally.
	out, _, code = run(t, "claim", "-json", "/w/a.go")
	if code != 0 || !strings.Contains(out, `"claims"`) ||
		!strings.Contains(out, `"warnings"`) || !strings.Contains(out, `"overlaps"`) ||
		!strings.Contains(out, `"identity": "bob"`) {
		t.Errorf("json claim = %q, code %d", out, code)
	}
}

// diff is the review surface: each pending group renders as old→new lines
// under its id, author and span; -json returns the structured form.
func TestCLIDiffRendersPendingChanges(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "hello world\n"})
	ed.diffJSON = `[{"id":7,"path":"/w/a.go","author":2,"state":"proposed","ops":1,` +
		`"bytes":1,"first":1,"last":1,"hunks":[{"start":6,"end":11,"line":1,"end_line":1,` +
		`"old":"world","new":"earth"}],"moved":1,` +
		`"moved_hunks":[{"start":-1,"end":-1,"old":"gone","new":"lost"}]}]`

	out, errs, code := run(t, "diff", "/w/a.go")
	if code != 0 {
		t.Fatalf("code %d: %s", code, errs)
	}
	for _, want := range []string{"group 7", "author 2", "@@ L1..L1 (bytes 6..11) @@",
		"-world", "+earth", "@@ moved: no current span, as written @@", "-gone", "+lost"} {
		if !strings.Contains(out, want) {
			t.Errorf("diff output %q missing %q", out, want)
		}
	}

	out, _, code = run(t, "diff", "-json", "/w/a.go")
	if code != 0 || !strings.Contains(out, `"old": "world"`) || !strings.Contains(out, `"new": "earth"`) ||
		!strings.Contains(out, `"moved_hunks"`) || !strings.Contains(out, `"new": "lost"`) {
		t.Errorf("json diff = %q, code %d", out, code)
	}

	// No pending change sets is a clean, explicit answer on exit 0.
	ed.diffJSON = ""
	out, _, code = run(t, "diff", "/w/a.go")
	if code != 0 || !strings.Contains(out, "no pending changes") {
		t.Errorf("clean diff = %q, code %d", out, code)
	}
}

// groups carries each set hunk and moved counts and can be narrowed to the
// sets still awaiting a decision, so a caller need not follow it with diff.
func TestCLIGroupsCountsAndStateFilter(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "hello world\n"})
	ed.groups = []Group{
		{ID: 1, Path: "/w/a.go", Author: 2, State: "proposed", Ops: 1, Bytes: 4, First: 1, Last: 1, Hunks: 1},
		{ID: 2, Path: "/w/a.go", Author: 2, State: "accepted", Ops: 1, Bytes: 4, First: 2, Last: 2, Hunks: 2, Moved: 1},
		{ID: 3, Path: "/w/a.go", Author: 3, State: "rejected", Ops: 1, Bytes: 4, First: 3, Last: 3},
	}

	out, errs, code := run(t, "groups", "/w/a.go")
	if code != 0 {
		t.Fatalf("code %d: %s", code, errs)
	}
	if !strings.Contains(out, "1 hunks") || !strings.Contains(out, "0 moved") {
		t.Errorf("groups output %q does not carry the hunk/moved counts", out)
	}

	out, _, code = run(t, "groups", "-pending", "/w/a.go")
	if code != 0 {
		t.Fatalf("-pending code %d", code)
	}
	if !strings.Contains(out, "1\tauthor 2\tproposed") {
		t.Errorf("-pending dropped the proposed set: %q", out)
	}
	if strings.Contains(out, "accepted") || strings.Contains(out, "rejected") {
		t.Errorf("-pending kept a decided set: %q", out)
	}

	out, _, code = run(t, "groups", "-state", "rejected", "-json", "/w/a.go")
	if code != 0 || !strings.Contains(out, `"state": "rejected"`) ||
		strings.Contains(out, `"state": "proposed"`) {
		t.Errorf("groups -state rejected = %q, code %d", out, code)
	}

	if _, errs, code := run(t, "groups", "-state", "bogus", "/w/a.go"); code != 2 ||
		!strings.Contains(errs, "must be proposed, accepted or rejected") {
		t.Errorf("bad -state: code %d, stderr %q", code, errs)
	}
}

func TestCLIUsage(t *testing.T) {
	if _, errs, code := run(t); code != 2 || !strings.Contains(errs, "usage") {
		t.Errorf("no args: code %d, stderr %q", code, errs)
	}
	if out, _, code := run(t, "help"); code != 0 || !strings.Contains(out, "edit") {
		t.Errorf("help: code %d, stdout %q", code, out)
	}
}

// A verb's -h is the flag package's usage with the positional made explicit.
// The flag package lists only flags, so without that line a caller cannot tell
// that edit takes an optional path, nor what omitting one targets.
func TestVerbHelpNamesTheOptionalPath(t *testing.T) {
	if _, errs, code := run(t, "edit", "-h"); code != 0 {
		t.Fatalf("edit -h exited %d, want 0", code)
	} else if !strings.Contains(errs, "raj ctl edit [path]") ||
		!strings.Contains(errs, "buffer the user is looking at") {
		t.Errorf("edit -h = %q, want the positional and the default target", errs)
	}
	// A verb whose path is required says so, rather than offering a default
	// target it does not have.
	if _, errs, _ := run(t, "open", "-h"); strings.Contains(errs, "with no path") {
		t.Errorf("open -h = %q, but open needs a path", errs)
	}
}

// No editor at all must fail with a message naming the fix, not a stack trace.
func TestCLIWithNoEditorRunning(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv(SocketEnv, "")
	os.Unsetenv("RAJ_SOCKET")
	os.Unsetenv("RAJ_CONTROL_ADDR")
	_, errs, code := run(t, "buffers")
	if code == 0 || !strings.Contains(errs, "no running raj found") {
		t.Errorf("code %d, stderr %q", code, errs)
	}
}

func TestIndexAll(t *testing.T) {
	if got := IndexAll("aaa", "aa"); len(got) != 1 || got[0] != 0 {
		t.Errorf("overlapping matches counted: %v", got)
	}
	if got := IndexAll("abcabc", "abc"); len(got) != 2 {
		t.Errorf("got %v", got)
	}
	if got := IndexAll("abc", ""); got != nil {
		t.Errorf("empty needle matched: %v", got)
	}
}

func TestLocatePrecedence(t *testing.T) {
	t.Setenv("RAJ_SOCKET", "/from/env.sock")
	t.Setenv(SocketEnv, "")
	if got, _ := Locate("/explicit.sock", ""); got != "/explicit.sock" {
		t.Errorf("got %q, want the explicit path", got)
	}
	if got, _ := Locate("", ""); got != "/from/env.sock" {
		t.Errorf("got %q, want the environment", got)
	}
	t.Setenv(SocketEnv, "/from/control-socket.sock")
	if got, _ := Locate("", ""); got != "/from/control-socket.sock" {
		t.Errorf("got %q, want %s to win over RAJ_SOCKET", got, SocketEnv)
	}
}

// A client that was given no address and has no address environment falls
// through to discovery and reaches the socket that is listening, rather than a
// hard-coded default.
func TestLocateDiscoversWithNoAddress(t *testing.T) {
	scan, err := os.MkdirTemp("", "raj-locate-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(scan) })
	dir := filepath.Join(scan, "raj")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	ed := newFakeEditorAt(t, filepath.Join(dir, "live.sock"), map[string]string{"/w/a.go": "x\n"})
	// newFakeEditorAt pins XDG_RUNTIME_DIR at its own temp dir; discovery must
	// scan where the live socket is, so pin it back.
	t.Setenv("XDG_RUNTIME_DIR", scan)
	os.Unsetenv("RAJ_SOCKET")
	t.Setenv(AddrEnv, "")
	t.Setenv(SocketEnv, "")

	got, err := Locate("", "")
	if err != nil {
		t.Fatal(err)
	}
	if got != ed.srv.Path() {
		t.Errorf("Locate discovered %q, want the live socket %q", got, ed.srv.Path())
	}
}

func (f *fakeEditor) Snapshot() Searcher { return f }

// Search blocks until released, so a test can cancel one in flight.
// Include globs are honoured against the full path and the base name —
// enough for the CLI's no-file-matched warning to be exercised without
// re-implementing the real walker's relative-path matching.
func (f *fakeEditor) Search(ctx context.Context, q SearchQuery, emit func([]SearchMatch)) (int, int, bool, []TruncatedFile, error) {
	f.mu.Lock()
	docs := make(map[string]string, len(f.docs))
	for k, v := range f.docs {
		docs[k] = v
	}
	vers := make(map[string]uint64, len(f.vers))
	for k, v := range f.vers {
		vers[k] = v
	}
	gate := f.gate
	truncated := append([]TruncatedFile(nil), f.truncated...)
	f.searchPath = q.Path
	f.searchHidden = q.Hidden
	f.mu.Unlock()

	var inc []string
	if q.Include != "" {
		inc = strings.Split(q.Include, ",")
	}
	included := func(p string) bool {
		if inc == nil {
			return true
		}
		for _, g := range inc {
			if ok, _ := path.Match(g, p); ok {
				return true
			}
			if ok, _ := path.Match(g, filepath.Base(p)); ok {
				return true
			}
		}
		return false
	}

	files, considered := 0, 0
	for p, t := range docs {
		if !included(p) {
			continue
		}
		considered++
		if gate != nil {
			// Emit one batch, then wait: the caller gets a partial result and
			// can cancel while the rest is outstanding.
			emit([]SearchMatch{{Path: p, Line: 1, Version: vers[p], Text: t}})
			select {
			case <-ctx.Done():
				f.mu.Lock()
				f.cancelled = true
				f.mu.Unlock()
				return files, considered, false, nil, ctx.Err()
			case <-gate:
			}
		}
		lines := strings.Split(t, "\n")
		for i, line := range lines {
			if c := strings.Index(line, q.Text); c >= 0 {
				m := SearchMatch{Path: p, Line: i + 1, Col: c, Len: len(q.Text),
					Version: vers[p], Text: line}
				if q.Context > 0 {
					lo := i - q.Context
					if lo < 0 {
						lo = 0
					}
					hi := i + q.Context
					if hi >= len(lines) {
						hi = len(lines) - 1
					}
					m.Context = strings.Join(lines[lo:hi+1], "\n")
				}
				emit([]SearchMatch{m})
			}
		}
		files++
	}
	return files, considered, false, truncated, nil
}

// Streaming: batches arrive as the walk finds them, not all at the end.
func TestSearchStreams(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "needle\nother\nneedle\n"})
	c, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	var batches int
	res, err := c.DoStream(Request{Op: "search", Query: &SearchQuery{Text: "needle"}},
		func(b []SearchMatch) { batches++ })
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || len(res.Matches) != 2 {
		t.Fatalf("res = %+v", res)
	}
	if batches < 2 {
		t.Errorf("arrived in %d batches; the results were accumulated rather than streamed", batches)
	}
	if !res.Final {
		t.Error("the last frame was not marked final")
	}
}

// A per-file cap is invisible in capped, so the CLI must name the files it cut
// down and by how much, rather than let a capped file read as an exact one.
func TestSearchReportsTruncatedFiles(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/COMPLETED.md": "## one\n## two\n"})
	ed.mu.Lock()
	ed.truncated = []TruncatedFile{{Path: "/w/COMPLETED.md", Shown: 2, Total: 1434}}
	ed.mu.Unlock()

	out, errs, code := run(t, "search", "-q", "##")
	if code != 0 {
		t.Fatalf("code %d: %s", code, errs)
	}
	if !strings.Contains(errs, "/w/COMPLETED.md") || !strings.Contains(errs, "2 of 1434") {
		t.Errorf("stderr = %q, want the truncated file and its counts", errs)
	}
	if !strings.Contains(errs, "1 file(s)") {
		t.Errorf("stderr = %q, want the count of truncated files", errs)
	}
	if strings.Contains(out, `"truncated"`) {
		t.Error("plain output should be hits only")
	}

	// The JSON form carries the same fact as data, so a driver does not parse
	// the warning to find it.
	out, errs, code = run(t, "search", "-q", "##", "-json")
	if code != 0 {
		t.Fatalf("json code %d: %s", code, errs)
	}
	if !strings.Contains(out, `"truncated": [`) ||
		!strings.Contains(out, `"total": 1434`) ||
		!strings.Contains(out, `"shown": 2`) {
		t.Errorf("json = %q, want the truncated list", out)
	}
}

// A hit names the buffer version it was found in, so a caller can tell the
// document moved between the search and a later read. Both machine forms carry
// it: the whole JSON object and the streaming NDJSON.
func TestSearchJSONCarriesBufferVersion(t *testing.T) {
	newFakeEditor(t, map[string]string{"/w/a.go": "needle\n"})

	out, errs, code := run(t, "search", "-q", "needle", "-json")
	if code != 0 {
		t.Fatalf("code %d: %s", code, errs)
	}
	if !strings.Contains(out, `"version": 1`) {
		t.Errorf("json = %q, want the hit's buffer version", out)
	}

	out, errs, code = run(t, "search", "-q", "needle", "-jsonl")
	if code != 0 {
		t.Fatalf("jsonl code %d: %s", code, errs)
	}
	if !strings.Contains(out, `"version":1`) {
		t.Errorf("jsonl = %q, want the hit's buffer version", out)
	}
}

// -path is the caller's spelling, not the editor's: the client maps it like
// every other path operand, so an absolute directory under the caller's root
// scopes the walk instead of being refused as outside the workspace.
func TestSearchPathIsRootMapped(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "needle\n"})
	t.Setenv(RootMapEnv, "/workspace=/w")

	_, errs, code := run(t, "search", "-q", "needle", "-path", "/workspace")
	if code != 0 {
		t.Fatalf("code %d: %s", code, errs)
	}
	ed.mu.Lock()
	got := ed.searchPath
	ed.mu.Unlock()
	if got != "/w" {
		t.Errorf("searchPath = %q, want the editor spelling /w", got)
	}
}

// localise rewrites every path in a response into the caller's view. The
// fields that hide a path are the ones a nested document or a prose error
// carries: the truncated file list, the encoded diff, and an editor refusal.
// Each is covered here, and the diff's hunk text is checked to prove a
// path-like Old or New is not rewritten along with the Group path beside it.
func TestLocaliseRewritesNestedAndProsePaths(t *testing.T) {
	var c Client
	c.SetMapper(NewMapper(Pair{Local: "/workspace", Editor: "/Users/rajan/src/raj"}))

	res := Response{
		Root:      "/Users/rajan/src/raj",
		Truncated: []TruncatedFile{{Path: "/Users/rajan/src/raj/big.md", Shown: 2, Total: 9}},
		DiffJSON: `[{"id":7,"path":"/Users/rajan/src/raj/a.go","author":2,` +
			`"state":"proposed","ops":1,"bytes":1,"first":1,"last":1,` +
			`"hunks":[{"start":0,"end":0,"old":"/Users/rajan/src/raj in text","new":""}],"moved":0}]`,
		LSPJSON: `{"locations":[{"path":"/Users/rajan/src/raj/a.go","line":3,"col":4}],` +
			`"symbols":[{"name":"Reader","kind":"interface","path":"/Users/rajan/src/raj/a.go","line":1,"col":6}],` +
			`"text":"see /Users/rajan/src/raj/a.go"}`,
		Err: "no open buffer for /Users/rajan/src/raj/a.go",
	}
	c.localise(&res)

	if res.Root != "/workspace" {
		t.Errorf("Root = %q", res.Root)
	}
	if got := res.Truncated[0].Path; got != "/workspace/big.md" {
		t.Errorf("Truncated.Path = %q", got)
	}
	if got := res.Err; got != "no open buffer for /workspace/a.go" {
		t.Errorf("Err = %q", got)
	}
	var diffs []DiffGroup
	if err := json.Unmarshal([]byte(res.DiffJSON), &diffs); err != nil {
		t.Fatalf("DiffJSON did not parse: %v", err)
	}
	if len(diffs) != 1 {
		t.Fatalf("DiffJSON holds %d groups, want 1", len(diffs))
	}
	if diffs[0].Path != "/workspace/a.go" {
		t.Errorf("DiffGroup.Path = %q", diffs[0].Path)
	}
	// The hunk's Old text is content, not a path, and must survive untouched —
	// the thing a string replace inside the encoded JSON would have corrupted.
	if got := diffs[0].Hunks[0].Old; got != "/Users/rajan/src/raj in text" {
		t.Errorf("DiffHunk.Old = %q, want it left alone", got)
	}
	// An lsp answer nests its locations and symbols the same way the diff nests
	// paths; the locations are rebased, while hover text is content and must
	// not be — the same distinction the hunk text above asserts for a diff.
	var lsp LSPResult
	if err := json.Unmarshal([]byte(res.LSPJSON), &lsp); err != nil {
		t.Fatalf("LSPJSON did not parse: %v", err)
	}
	if got := lsp.Locations[0].Path; got != "/workspace/a.go" {
		t.Errorf("LSPLocation.Path = %q", got)
	}
	if len(lsp.Symbols) != 1 || lsp.Symbols[0].Path != "/workspace/a.go" {
		t.Errorf("LSPSymbol.Path = %+v, want the rebased path", lsp.Symbols)
	}
	if got := lsp.Text; got != "see /Users/rajan/src/raj/a.go" {
		t.Errorf("LSPResult.Text = %q, want it left alone", got)
	}
}

// A root that is only a character prefix of a longer directory name is not a
// path inside the mapped tree, and rewriting it would corrupt the message.
func TestLocaliseErrLeavesNonBoundaryAlone(t *testing.T) {
	var c Client
	c.SetMapper(NewMapper(Pair{Local: "/workspace", Editor: "/Users/rajan/src/raj"}))
	res := Response{Err: "no open buffer for /Users/rajan/src/rajx/a.go"}
	c.localise(&res)
	if res.Err != "no open buffer for /Users/rajan/src/rajx/a.go" {
		t.Errorf("Err = %q, want the out-of-tree path untouched", res.Err)
	}
}

// Cancellation is the reason reading and handling are separate goroutines. A
// cancel has to be readable WHILE the search it stops is running; a loop that
// read a frame, handled it, then replied would be inside the handler.
func TestSearchCancelWhileRunning(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "needle\n"})
	ed.mu.Lock()
	ed.gate = make(chan struct{}) // never closed: the search hangs after batch one
	ed.mu.Unlock()

	c, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	first := make(chan struct{})
	done := make(chan Response, 1)
	go func() {
		var once sync.Once
		res, _ := c.DoStream(Request{Op: "search", Query: &SearchQuery{Text: "needle"}},
			func(b []SearchMatch) { once.Do(func() { close(first) }) })
		done <- res
	}()

	select {
	case <-first:
	case <-time.After(5 * time.Second):
		t.Fatal("no partial result; the search never started streaming")
	}

	// The cancel goes down the same connection the search is streaming on.
	if err := c.CancelCurrent(); err != nil {
		t.Fatal(err)
	}
	select {
	case res := <-done:
		if res.OK || res.Err == "" {
			t.Errorf("cancelled search reported %+v, want a refusal", res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not stop the search")
	}
	ed.mu.Lock()
	cancelled := ed.cancelled
	ed.mu.Unlock()
	if !cancelled {
		t.Error("the walk was abandoned but never saw the cancellation")
	}
}

// Dropping the connection cancels whatever it was running, so a killed client
// does not leave a walk burning CPU in the editor.
func TestDisconnectCancelsInFlightWork(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "needle\n"})
	ed.mu.Lock()
	ed.gate = make(chan struct{})
	ed.mu.Unlock()

	c, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	first := make(chan struct{})
	go func() {
		var once sync.Once
		c.DoStream(Request{Op: "search", Query: &SearchQuery{Text: "needle"}},
			func(b []SearchMatch) { once.Do(func() { close(first) }) })
	}()
	select {
	case <-first:
	case <-time.After(5 * time.Second):
		t.Fatal("search never started")
	}
	c.Close()

	deadline := time.After(5 * time.Second)
	for {
		ed.mu.Lock()
		cancelled := ed.cancelled
		ed.mu.Unlock()
		if cancelled {
			return
		}
		select {
		case <-deadline:
			t.Fatal("a dropped connection left the walk running")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// Other requests are still served while a search streams — the connection is
// not blocked by its own long-running work.
func TestConnectionStaysResponsiveDuringASearch(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "needle\n"})
	ed.mu.Lock()
	ed.gate = make(chan struct{})
	ed.mu.Unlock()

	stream, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	first := make(chan struct{})
	go func() {
		var once sync.Once
		stream.DoStream(Request{Op: "search", Query: &SearchQuery{Text: "needle"}},
			func(b []SearchMatch) { once.Do(func() { close(first) }) })
	}()
	select {
	case <-first:
	case <-time.After(5 * time.Second):
		t.Fatal("search never started")
	}

	other, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	got := make(chan error, 1)
	go func() { _, err := other.Do(Request{Op: "buffers"}); got <- err }()
	select {
	case err := <-got:
		if err != nil {
			t.Errorf("buffers during a search: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the editor was blocked by a running search")
	}
}

// The assigned id rides on every response, not just a handshake, so a client
// cannot be holding a stale one.
func TestClientLearnsItsAuthorID(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "hello\n"})
	a, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if a.Author() != 0 {
		t.Error("an id was reported before any exchange")
	}
	if _, err := a.Do(Request{Op: "buffers"}); err != nil {
		t.Fatal(err)
	}
	if a.Author() < FirstAgent {
		t.Fatalf("author = %d, want an agent id", a.Author())
	}

	// A second connection is a second writer, and must learn a different id.
	b, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if _, err := b.Do(Request{Op: "buffers"}); err != nil {
		t.Fatal(err)
	}
	if b.Author() == a.Author() {
		t.Errorf("both connections report author %d", a.Author())
	}
	// Every response carries it, not only the first.
	if _, err := a.Do(Request{Op: "ping"}); err != nil {
		t.Fatal(err)
	}
	if a.Author() < FirstAgent {
		t.Errorf("author was lost after a later request: %d", a.Author())
	}
}

func TestWhoamiPrintsTheID(t *testing.T) {
	newFakeEditor(t, map[string]string{"/w/a.go": "hello\n"})
	out, errs, code := run(t, "whoami")
	if code != 0 {
		t.Fatalf("code %d: %s", code, errs)
	}
	if strings.TrimSpace(out) == "" || strings.TrimSpace(out) == "0" {
		t.Errorf("whoami printed %q, want an agent id", out)
	}
}

// register mints an explicit key and binds it, and -as pins a caller-chosen
// key so repeated calls are the same writer. The fake editor runs the real
// server, so the hello/author binding under test is the shipped one.
func TestRegisterMintsAndBindsAKey(t *testing.T) {
	t.Setenv("RAJ_IDENTITY", "")
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "hello\n"})

	out, errs, code := run(t, "register", "-json")
	if code != 0 {
		t.Fatalf("register exited %d: %s", code, errs)
	}
	var got struct {
		Key    string `json:"key"`
		Author uint8  `json:"author"`
		Name   string `json:"name"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("register -json = %q: %v", out, err)
	}
	suffix := strings.TrimPrefix(got.Key, "raj-")
	if len(got.Key) != len("raj-")+8 || strings.Trim(suffix, "0123456789abcdef") != "" {
		t.Errorf("key = %q, want raj- plus 8 lowercase hex chars", got.Key)
	}
	if got.Name != "raj" {
		t.Errorf("name = %q, want the default", got.Name)
	}
	if got.Author < FirstAgent {
		t.Errorf("author = %d, want an agent id", got.Author)
	}

	// An explicit -as skips the duplicate-key guard: the key may already
	// belong to a participant and register still binds it, because the choice
	// belongs to the caller. Claim mykey on another connection first.
	owner, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	claim, err := owner.Do(Request{Op: "hello", Identity: "mykey", Name: "claimed"})
	if err != nil || !claim.OK {
		t.Fatalf("claiming mykey: res=%+v err=%v", claim, err)
	}
	inUse := owner.Author()
	if inUse < FirstAgent {
		t.Fatalf("claimed author = %d", inUse)
	}
	reused, _, code := run(t, "register", "-as", "mykey", "-json")
	if code != 0 {
		t.Fatalf("register -as an in-use key exited %d", code)
	}
	var gotReused struct {
		Key    string `json:"key"`
		Author uint8  `json:"author"`
	}
	if err := json.Unmarshal([]byte(reused), &gotReused); err != nil {
		t.Fatal(err)
	}
	if gotReused.Key != "mykey" || gotReused.Author != inUse {
		t.Errorf("register -as mykey = %s, want key mykey and author %d", reused, inUse)
	}

	// The same chosen key on two calls is the same author, and the reply is
	// identical because the key is not random.
	first, _, code := run(t, "register", "-as", "mykey", "-json")
	if code != 0 {
		t.Fatalf("register -as exited %d", code)
	}
	second, _, code := run(t, "register", "-as", "mykey", "-json")
	if code != 0 {
		t.Fatalf("register -as exited %d", code)
	}
	if first != second {
		t.Errorf("register -as mykey is not stable:\n first %s\nsecond %s", first, second)
	}
	var pinned struct {
		Author uint8 `json:"author"`
	}
	if err := json.Unmarshal([]byte(first), &pinned); err != nil {
		t.Fatal(err)
	}
	if pinned.Author < FirstAgent {
		t.Errorf("pinned author = %d, want an agent id", pinned.Author)
	}
}

// The human-readable form of register names the key and how to use it, not the
// anonymous adopt line the generic bind prints for other verbs.
func TestRegisterPrintsTheKey(t *testing.T) {
	t.Setenv("RAJ_IDENTITY", "")
	newFakeEditor(t, map[string]string{"/w/a.go": "hello\n"})
	out, errs, code := run(t, "register")
	if code != 0 {
		t.Fatalf("register exited %d: %s", code, errs)
	}
	if !strings.Contains(out, "key: raj-") || !strings.Contains(out, "--as raj-") {
		t.Errorf("register = %q, want the key and the -as line", out)
	}
	if strings.Contains(errs, "RAJ_IDENTITY") {
		t.Errorf("register printed an adopt line: %q", errs)
	}
}

// A generated key that collides with an existing identity is redrawn, and the
// retry loop is bounded so a registry that owns every candidate cannot spin.
func TestMintRegisterKeyRetriesAndBounds(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "hello\n"})

	// Claim the first candidate so mintRegisterKey has to redraw past it.
	owner, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if _, err := owner.Do(Request{Op: "hello", Identity: "raj-aaaaaaaa", Name: "taken"}); err != nil {
		t.Fatal(err)
	}

	c, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	// First draw collides, second is free: the free one comes back.
	draws := []string{"raj-aaaaaaaa", "raj-bbbbbbbb"}
	key, err := mintRegisterKey(c, func() (string, error) {
		next := draws[0]
		draws = draws[1:]
		return next, nil
	})
	if err != nil {
		t.Fatalf("mintRegisterKey: %v", err)
	}
	if key != "raj-bbbbbbbb" {
		t.Errorf("key = %q, want the second draw", key)
	}

	// Every draw collides: the loop gives up at the bound.
	_, err = mintRegisterKey(c, func() (string, error) { return "raj-aaaaaaaa", nil })
	if err == nil {
		t.Fatal("a permanent collision was accepted")
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("%d attempts", registerMintAttempts)) {
		t.Errorf("error = %q, want the attempt bound", err)
	}
}

// setDirty makes the shared policy see unsaved buffers.
func (f *fakeEditor) setDirty(d ...DirtyBuffer) {
	f.policyMem.dirty = d
}

// A command runs, its output streams back, and its exit status is preserved —
// a failing test is the answer to the question, not a failure to answer it.
func TestExecRunsAndStreams(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "x\n"})
	c, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	var out, errOut string
	res, err := c.DoExec(Request{Op: "exec",
		Argv: []string{"sh", "-c", "echo hello; echo bad >&2; exit 3"}},
		func(stream uint8, b string) {
			if stream == StreamStderr {
				errOut += b
			} else {
				out += b
			}
		})
	if err != nil {
		t.Fatal(err)
	}
	if res.Exit != 3 {
		t.Errorf("exit = %d, want 3", res.Exit)
	}
	if !strings.Contains(out, "hello") {
		t.Errorf("stdout = %q", out)
	}
	if !strings.Contains(errOut, "bad") {
		t.Errorf("stderr = %q", errOut)
	}
}

// Both pipes are drained concurrently. Reading one to completion first
// deadlocks as soon as the other fills its buffer, which for a compiler writing
// warnings to stderr is immediate.
func TestExecDoesNotDeadlockOnBothPipes(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "x\n"})
	c, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	var n int
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.DoExec(Request{Op: "exec", Argv: []string{"sh", "-c",
			"i=0; while [ $i -lt 400 ]; do echo out-$i; echo err-$i >&2; i=$((i+1)); done"}},
			func(stream uint8, b string) { n += len(b) })
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("exec deadlocked writing to both pipes")
	}
	if n == 0 {
		t.Error("no output relayed")
	}
}

// Refusal while anything is unsaved, naming the files.
func TestExecWarnsButRunsWhenDirty(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "x\n"})
	ed.setDirty([]DirtyBuffer{{Path: "/w/a.go"}}...)

	out, errs, code := run(t, "exec", "--", "sh", "-c", "echo ran")
	if code != 0 {
		t.Fatalf("code = %d, want the command's own 0; stderr %q", code, errs)
	}
	if !strings.Contains(out, "ran") {
		t.Errorf("the command did not run: stdout %q", out)
	}
	if !strings.Contains(errs, "/w/a.go") || !strings.Contains(errs, "warning") {
		t.Errorf("stale file not warned about: %q", errs)
	}
}

// A refusal exits 2 and a failing command exits its own status, so "the tests
// failed" and "the tests never ran" are distinguishable. An agent that could
// not tell them apart would report a passing build as broken.
func TestExecExitCodesDistinguishRefusalFromFailure(t *testing.T) {
	newFakeEditor(t, map[string]string{"/w/a.go": "x\n"})
	if _, _, code := run(t, "exec", "--", "sh", "-c", "exit 1"); code != 1 {
		t.Errorf("a failing command exited %d, want its own 1", code)
	}
	if _, _, code := run(t, "exec"); code != 2 {
		t.Errorf("a missing command exited %d, want 2", code)
	}
}

// Flags after -- belong to the other program, not to raj ctl.
func TestExecPassesFlagsThrough(t *testing.T) {
	newFakeEditor(t, map[string]string{"/w/a.go": "x\n"})
	out, errs, code := run(t, "exec", "--", "sh", "-c", "echo $1", "sh", "-json")
	if code != 0 {
		t.Fatalf("code %d: %s", code, errs)
	}
	// -json reached the command rather than switching raj ctl's own output.
	if !strings.Contains(out, "-json") {
		t.Errorf("stdout = %q; the flag was eaten by raj ctl", out)
	}
}

func TestExecCancel(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "x\n"})
	c, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	started := make(chan struct{})
	done := make(chan Response, 1)
	go func() {
		var once sync.Once
		res, _ := c.DoExec(Request{Op: "exec",
			Argv: []string{"sh", "-c", "echo going; sleep 60"}},
			func(stream uint8, b string) { once.Do(func() { close(started) }) })
		done <- res
	}()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("command never produced output")
	}
	if err := c.CancelCurrent(); err != nil {
		t.Fatal(err)
	}
	select {
	case res := <-done:
		if res.OK || res.Err == "" {
			t.Errorf("cancelled exec reported %+v, want a refusal", res)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancel did not stop the command")
	}
}

// stats is how the policy stops being an argument.
func TestExecStatsOverTheWire(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "x\n"})
	ed.setDirty([]DirtyBuffer{{Path: "/w/a.go", AgentOnly: true}}...)
	run(t, "exec", "--", "true")

	out, _, code := run(t, "stats", "-json")
	if code != 0 {
		t.Fatalf("stats exited %d", code)
	}
	if !strings.Contains(out, "stale") || !strings.Contains(out, "agent_only") {
		t.Errorf("stats = %q", out)
	}
}

// hello binds a connection to a durable identity, so a reconnecting harness
// continues to own the text it already wrote.
func TestHelloRebindsTheAuthorID(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "hello\n"})

	first, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	res, err := first.Do(Request{Op: "hello", Identity: "harness-abc", Name: "claude-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK {
		t.Fatalf("hello = %+v", res)
	}
	id := first.Author()
	if id < FirstAgent {
		t.Fatalf("author = %d", id)
	}
	var named bool
	for _, p := range res.Participants {
		if p.ID == id && p.Name == "claude-1" {
			named = true
		}
	}
	if !named {
		t.Errorf("participants = %+v, want this connection named", res.Participants)
	}
	first.Close()

	// A different connection, the same identity: the same author id.
	second, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if _, err := second.Do(Request{Op: "hello", Identity: "harness-abc"}); err != nil {
		t.Fatal(err)
	}
	if second.Author() != id {
		t.Errorf("reconnected as %d, was %d", second.Author(), id)
	}
}

// An anonymous TCP client is handed a server-minted identity token, and
// presenting it on the next connection rebinds to the same author id.
func TestHelloMintsAnIdentityToken(t *testing.T) {
	t.Setenv(TokenEnv, "test-token")
	srv, err := listenTCP("127.0.0.1:0", func() {})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	first, err := Dial(srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	res, err := first.Do(Request{Op: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK {
		t.Fatalf("hello = %+v", res)
	}
	if !strings.HasPrefix(res.Identity, "tok_") {
		t.Fatalf("hello identity = %q, want a server-minted token", res.Identity)
	}
	id := first.Author()
	first.Close()

	// A different connection presenting the token is the same writer, and is
	// not minted another one.
	second, err := Dial(srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	res2, err := second.Do(Request{Op: "hello", Identity: res.Identity})
	if err != nil {
		t.Fatal(err)
	}
	if second.Author() != id {
		t.Errorf("reconnected as %d, was %d", second.Author(), id)
	}
	if res2.Identity != "" {
		t.Errorf("second hello minted %q, want none", res2.Identity)
	}
}

// SrcVersion and the minted identity cross the wire only when set, so a peer
// built before either field still decodes what a new one sends.
func TestSrcVersionAndIdentityRoundTrip(t *testing.T) {
	h, body := EncodeResponse(Response{ID: 9, OK: true, Final: true,
		SrcVersion: "abc123", Identity: "tok_xyz"})
	res, err := DecodeResponse(Frame{Header: h, Body: body})
	if err != nil {
		t.Fatal(err)
	}
	if res.SrcVersion != "abc123" || res.Identity != "tok_xyz" {
		t.Errorf("decoded %+v, want both fields", res)
	}

	h, body = EncodeResponse(Response{ID: 9, OK: true, Final: true})
	res, err = DecodeResponse(Frame{Header: h, Body: body})
	if err != nil {
		t.Fatal(err)
	}
	if res.SrcVersion != "" || res.Identity != "" {
		t.Errorf("decoded %+v, want both empty", res)
	}
}

// The version check is a warning, not a refusal, and stays silent when either
// side cannot name its build.
func TestWarnVersionSkew(t *testing.T) {
	old := srcVersion
	defer func() { srcVersion = old }()
	srcVersion = "ctl-abc"

	var b strings.Builder
	warnVersionSkew(&b, "editor-def")
	if !strings.Contains(b.String(), "ctl-abc") || !strings.Contains(b.String(), "editor-def") {
		t.Errorf("warning = %q, want both revisions named", b.String())
	}

	b.Reset()
	warnVersionSkew(&b, "ctl-abc")
	warnVersionSkew(&b, "")
	if b.String() != "" {
		t.Errorf("warning = %q, want silence on match or unknown", b.String())
	}
}

// A connection that never identifies itself still works; it just does not
// survive a reconnect as the same writer.
func TestAnonymousConnectionsStillGetAnID(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "hello\n"})
	c, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Do(Request{Op: "buffers"}); err != nil {
		t.Fatal(err)
	}
	if c.Author() < FirstAgent {
		t.Errorf("author = %d, want an agent id", c.Author())
	}
}

// A killed editor leaves its socket behind, and nothing else will clean it up.
// Discovery reaps what it finds dead rather than letting them accumulate and
// making every later listing slower and noisier.
func TestDiscoverReapsDeadSockets(t *testing.T) {
	// The discovery directory is deliberately short. macOS rejects unix
	// socket paths past sun_path (104 bytes), and the temp dir under a long
	// test name drifts past it the moment the machine or temp volume changes.
	scan, err := os.MkdirTemp("", "raj-disc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(scan) })
	t.Setenv("XDG_RUNTIME_DIR", scan)
	dir := filepath.Join(scan, "raj")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	// A file that looks like a socket but answers nothing.
	dead := filepath.Join(dir, "9999.sock")
	if err := os.WriteFile(dead, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// And a live one, in the same directory discovery scans. It is a real
	// listener here rather than a symlink to a socket elsewhere: discovery
	// scans for real listeners, and threading one in by symlink assumes the
	// platform dials a socket through a symlink.
	ed := newFakeEditorAt(t, filepath.Join(dir, "live.sock"), map[string]string{"/w/a.go": "x\n"})
	live := ed.srv.Path()
	// newFakeEditorAt points XDG_RUNTIME_DIR at its own temp dir; that would
	// send discovery elsewhere, so pin it back where the fixtures live.
	t.Setenv("XDG_RUNTIME_DIR", scan)
	// A SocketEnv override would send DefaultPath (and so discovery) elsewhere.
	t.Setenv(SocketEnv, "")

	found := Discover()
	if _, err := os.Stat(dead); !os.IsNotExist(err) {
		t.Error("a dead socket was left behind")
	}
	if _, err := os.Stat(live); err != nil {
		t.Errorf("the live socket was reaped: %v", err)
	}
	var sawLive bool
	for _, in := range found {
		if in.Socket == live {
			sawLive = true
		}
	}
	if !sawLive {
		t.Errorf("discovery lost the live editor: %+v", found)
	}
}

// apply -hunks sends a whole change set read from a JSON Lines file: one
// {start,end,text} object per line, all rebased together against one base. A
// k-site edit is one invocation and one re-read instead of k of each.
func TestCLIApplyHunks(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "hello world\n"})
	hunks := filepath.Join(t.TempDir(), "hunks.jsonl")
	err := os.WriteFile(hunks, []byte(
		`{"start":0,"end":5,"text":"goodbye"}`+"\n"+
			`{"start":6,"end":11,"text":"earth"}`+"\n"), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	out, errs, code := run(t, "apply", "/w/a.go", "-base", "1", "-hunks", hunks)
	if code != 0 {
		t.Fatalf("code %d: %s", code, errs)
	}
	if got := ed.docs["/w/a.go"]; got != "goodbye earth\n" {
		t.Errorf("buffer = %q, want both hunks applied", got)
	}
	if !strings.Contains(out, "applied 2 hunk(s)") {
		t.Errorf("output = %q, want the hunk count", out)
	}
}

// parseHunks is the -hunks reader on its own: blank lines are skipped, a
// backwards span and a malformed line are refused, and an empty file is not a
// silent no-op.
func TestParseHunks(t *testing.T) {
	var errs bytes.Buffer
	hunks, echo, code := parseHunks(
		`{"start":6,"end":11,"text":"earth"}`+"\n"+
			"\n"+
			`{"start":0,"end":5,"text":""}`+"\n", &errs)
	if code != 0 {
		t.Fatalf("code %d: %s", code, errs.String())
	}
	if len(hunks) != 2 || hunks[0].Start != 6 || hunks[1].Text != "" {
		t.Errorf("hunks = %+v", hunks)
	}
	if len(echo) != 2 || echo[0].End != 11 {
		t.Errorf("echo = %+v", echo)
	}
	if _, _, code := parseHunks(`{"start":5,"end":1}`, &errs); code != 1 {
		t.Errorf("a backwards span was accepted: %d", code)
	}
	if _, _, code := parseHunks("not json\n", &errs); code != 1 {
		t.Errorf("a malformed line was accepted: %d", code)
	}
	if _, _, code := parseHunks("\n\n", &errs); code != 1 {
		t.Errorf("an empty hunks file was accepted: %d", code)
	}
}

// -hunks is a whole change set; mixing it with the single-hunk flags is a
// usage error rather than a silent preference for one of them.
func TestCLIApplyHunksRefusesMixedFlags(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "x\n"})
	hunks := filepath.Join(t.TempDir(), "hunks.jsonl")
	err := os.WriteFile(hunks, []byte(`{"start":0,"end":0,"text":"y"}`+"\n"), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	out, errs, code := run(t, "apply", "/w/a.go", "-base", "1", "-hunks", hunks, "-start", "0")
	if code != 2 {
		t.Fatalf("code = %d, want 2 (usage); stdout %q, stderr %q", code, out, errs)
	}
	if !strings.Contains(errs, "--hunks") || !strings.Contains(errs, "--start") {
		t.Errorf("stderr = %q, want both flags named", errs)
	}
	if ed.docs["/w/a.go"] != "x\n" {
		t.Errorf("buffer changed anyway: %q", ed.docs["/w/a.go"])
	}
}

// accept -all decides every group the path reports in one command. Each
// decision is its own frame, but the caller makes one invocation and sees
// every outcome.
func TestCLIAcceptAllDecidesEveryGroup(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "x\n"})
	ed.groups = []Group{{ID: 3}, {ID: 7}, {ID: 9}}
	out, errs, code := run(t, "accept", "/w/a.go", "-all")
	if code != 0 {
		t.Fatalf("code %d: %s", code, errs)
	}
	for _, want := range []string{"accepted change set 3", "accepted change set 7", "accepted change set 9"} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q missing %q", out, want)
		}
	}
	if len(ed.decided) != 3 || ed.decided[0] != 3 || ed.decided[1] != 7 || ed.decided[2] != 9 {
		t.Errorf("decided %v, want 3, 7, 9", ed.decided)
	}
}

// reject -all unwinds newest-first: an earlier set a later overlapping one
// wedges can only come out once the later set is gone. Reversing in document
// order would hit the earlier one first, refuse it, and leave it applied.
func TestCLIRejectAllUnwindsAnOverlappingPair(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "x\n"})
	ed.pending = []Group{
		{ID: 3, Path: "/w/a.go", Author: 2, State: "proposed", Ops: 1, First: 1, Last: 1},
		{ID: 9, Path: "/w/a.go", Author: 2, State: "proposed", Ops: 1, First: 8, Last: 8},
	}
	ed.wedge = map[uint64]uint64{3: 9} // 3 cannot come out while 9 is still in
	out, errs, code := run(t, "reject", "/w/a.go", "-all")
	if code != 0 {
		t.Fatalf("code %d: %s", code, errs)
	}
	if len(ed.decided) != 2 || ed.decided[0] != 9 || ed.decided[1] != 3 {
		t.Fatalf("decided %v, want 9 then 3, so 9 frees 3", ed.decided)
	}
	if len(ed.pending) != 0 {
		t.Errorf("pending = %+v, want the stack fully unwound", ed.pending)
	}
	if !strings.Contains(out, "rejected change set 9") || !strings.Contains(out, "rejected change set 3") {
		t.Errorf("stdout = %q, want both outcomes", out)
	}
}

// A set that cannot be reversed even as the newest one is named and skipped,
// and the sets already out are not re-attempted: the bulk form reports the one
// failure and leaves the rest of the stack unwound rather than tripping over a
// set that is already gone — the wedge the old single-listing loop produced.
func TestCLIRejectAllReportsAnUnplaceableSet(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "x\n"})
	ed.pending = []Group{
		{ID: 3, Path: "/w/a.go", Author: 2, State: "proposed", Ops: 1, First: 1, Last: 1},
		{ID: 9, Path: "/w/a.go", Author: 2, State: "proposed", Ops: 1, First: 8, Last: 8},
	}
	ed.wedge = map[uint64]uint64{3: 0} // 3 is wedged outright
	out, errs, code := run(t, "reject", "/w/a.go", "-all")
	if code == 0 {
		t.Fatalf("an unplaceable reject reported success: %q", out)
	}
	if !strings.Contains(errs, "change set 3") || !strings.Contains(errs, "overlap") {
		t.Errorf("stderr = %q, want the unplaceable set named", errs)
	}
	if !strings.Contains(errs, "1 of 2") {
		t.Errorf("stderr = %q, want one of two failed", errs)
	}
	if len(ed.decided) != 1 || ed.decided[0] != 9 {
		t.Errorf("decided %v, want only 9 (3 refused)", ed.decided)
	}
	if len(ed.pending) != 1 || ed.pending[0].ID != 3 {
		t.Errorf("pending = %+v, want only the unplaceable 3", ed.pending)
	}
	if !strings.Contains(out, "rejected change set 9") {
		t.Errorf("stdout = %q, want the outcome that landed", out)
	}
}

// A proposed set a later edit has fully overwritten is not pending — there is
// nothing left to back out — so a bulk reject never attempts it and never
// reports the wedge that attempting it would produce. This is the second live
// run: both sets at 0 ops, and a reject that reported an overlap anyway.
func TestCLIRejectAllSkipsSetsWithNothingLeft(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "x\n"})
	ed.diffJSON = `[{"id":3,"path":"/w/a.go","author":2,"state":"proposed","ops":1,` +
		`"bytes":0,"first":1,"last":1,"hunks":[],"moved":1},` +
		`{"id":9,"path":"/w/a.go","author":2,"state":"proposed","ops":1,` +
		`"bytes":1,"first":8,"last":8,"hunks":[{"start":0,"end":1,"old":"","new":"x"}],"moved":0}]`
	out, errs, code := run(t, "reject", "/w/a.go", "-all")
	if code != 0 {
		t.Fatalf("code %d: %s", code, errs)
	}
	if len(ed.decided) != 1 || ed.decided[0] != 9 {
		t.Errorf("decided %v, want only the pending 9", ed.decided)
	}
	if strings.Contains(errs, "change set 3") {
		t.Errorf("stderr = %q, want no failure for the already-settled 3", errs)
	}
	if !strings.Contains(out, "rejected change set 9") {
		t.Errorf("stdout = %q", out)
	}
}

// -all is bulk and -group is one; passing both is a usage error, because
// guessing which one wins would decide a change set the caller did not name.
func TestCLIAllAndGroupAreAlternatives(t *testing.T) {
	newFakeEditor(t, map[string]string{"/w/a.go": "x\n"})
	_, errs, code := run(t, "accept", "/w/a.go", "-all", "-group", "3")
	if code != 2 || !strings.Contains(errs, "--all and --group are alternatives") {
		t.Errorf("code %d, stderr %q", code, errs)
	}
}

// clear by group removes one rejected set, and the outcome names it.
func TestCLIClearByGroup(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "x\n"})
	ed.groups = []Group{{ID: 5, Path: "/w/a.go", Author: 2, State: "rejected", Ops: 1, Bytes: 1}}
	out, errs, code := run(t, "clear", "/w/a.go", "-group", "5")
	if code != 0 {
		t.Fatalf("code %d: %s", code, errs)
	}
	if !strings.Contains(out, "cleared change set 5") {
		t.Errorf("stdout = %q", out)
	}
	if len(ed.decided) != 1 || ed.decided[0] != 5 {
		t.Errorf("cleared %v, want 5", ed.decided)
	}
}

// A set that is not rejected cannot be cleared. The refusal names the group,
// which is what distinguishes it from the usage error for a missing -group.
func TestCLIClearRefusesNonRejectedSet(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "x\n"})
	ed.groups = []Group{{ID: 5, Path: "/w/a.go", Author: 2, State: "proposed", Ops: 1}}
	out, errs, code := run(t, "clear", "/w/a.go", "-group", "5")
	if code == 0 {
		t.Fatalf("clearing a proposed set reported success: %q", out)
	}
	if !strings.Contains(errs, "change set 5") || !strings.Contains(errs, "not rejected") {
		t.Errorf("stderr = %q, want the group named", errs)
	}
}

// clear -all purges every rejected set and leaves accepted and proposed ones
// alone. It unwinds newest-first, the order a reversal needs.
func TestCLIClearAllPurgesRejectedSets(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "x\n"})
	ed.groups = []Group{
		{ID: 3, Path: "/w/a.go", Author: 2, State: "rejected", Ops: 1, First: 1, Last: 1},
		{ID: 5, Path: "/w/a.go", Author: 2, State: "accepted", Ops: 1, First: 4, Last: 4},
		{ID: 9, Path: "/w/a.go", Author: 2, State: "rejected", Ops: 1, First: 8, Last: 8},
	}
	out, errs, code := run(t, "clear", "/w/a.go", "-all")
	if code != 0 {
		t.Fatalf("code %d: %s", code, errs)
	}
	if len(ed.decided) != 2 || ed.decided[0] != 9 || ed.decided[1] != 3 {
		t.Fatalf("cleared %v, want 9 then 3 (newest-first, accepted 5 skipped)", ed.decided)
	}
	if !strings.Contains(out, "cleared change set 9") || !strings.Contains(out, "cleared change set 3") {
		t.Errorf("stdout = %q, want both outcomes", out)
	}
}

// A rejected set a later edit has wedged is named and skipped; the rest of the
// bulk clear still runs.
func TestCLIClearAllReportsAWedge(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "x\n"})
	ed.groups = []Group{
		{ID: 3, Path: "/w/a.go", Author: 2, State: "rejected", Ops: 1, First: 1, Last: 1},
		{ID: 9, Path: "/w/a.go", Author: 2, State: "rejected", Ops: 1, First: 8, Last: 8},
	}
	ed.clearErr = map[uint64]string{3: "change set 3 could not be cleared: later edits overlap it"}
	out, errs, code := run(t, "clear", "/w/a.go", "-all")
	if code == 0 {
		t.Fatalf("an unplaceable clear reported success: %q", out)
	}
	if !strings.Contains(errs, "change set 3") || !strings.Contains(errs, "overlap") {
		t.Errorf("stderr = %q, want the wedged set named", errs)
	}
	if len(ed.decided) != 1 || ed.decided[0] != 9 {
		t.Errorf("cleared %v, want only 9 (3 refused)", ed.decided)
	}
}

// open without -create refuses a path that is neither a buffer nor a file,
// naming it; -create is the caller saying it means to make a new buffer.
func TestCLIOpenRefusesMissingPathWithoutCreate(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "x\n"})
	out, errs, code := run(t, "open", "/w/new.go")
	if code != 1 {
		t.Fatalf("open of a missing path: code = %d, want 1; stdout %q stderr %q", code, out, errs)
	}
	if !strings.Contains(errs, "/w/new.go") {
		t.Errorf("stderr = %q, want the path named", errs)
	}
	if _, ok := ed.docs["/w/new.go"]; ok {
		t.Errorf("the refused open created a buffer")
	}

	out, errs, code = run(t, "open", "/w/new.go", "-create")
	if code != 0 {
		t.Fatalf("open -create: code = %d: %s", code, errs)
	}
	if _, ok := ed.docs["/w/new.go"]; !ok {
		t.Errorf("open -create created no buffer")
	}
	if !strings.Contains(out, "created /w/new.go") {
		t.Errorf("stdout = %q, want it to say the buffer was created", out)
	}
}

// open says which of the two things it did: created a new buffer, or focused
// one already loaded. The plain word and the -json boolean are the same fact,
// so a driver that meant to write a new file can tell it from a focus.
func TestCLIOpenSaysCreatedVersusOpened(t *testing.T) {
	newFakeEditor(t, map[string]string{"/w/a.go": "x\n"})

	out, errs, code := run(t, "open", "/w/a.go")
	if code != 0 {
		t.Fatalf("open = %d: %s", code, errs)
	}
	if !strings.Contains(out, "opened /w/a.go") {
		t.Errorf("stdout = %q, want opened for an existing buffer", out)
	}

	out, _, code = run(t, "open", "/w/new.go", "-create")
	if code != 0 {
		t.Fatalf("open -create = %d: %s", code, errs)
	}
	if !strings.Contains(out, "created /w/new.go") {
		t.Errorf("stdout = %q, want created for a new buffer", out)
	}

	out, _, code = run(t, "open", "/w/new2.go", "-create", "-json")
	if code != 0 {
		t.Fatalf("open -json = %d: %s", code, errs)
	}
	if !strings.Contains(out, `"created": true`) {
		t.Errorf("json = %q, want created true", out)
	}

	out, _, code = run(t, "open", "/w/a.go", "-json")
	if code != 0 {
		t.Fatalf("open -json = %d: %s", code, errs)
	}
	if !strings.Contains(out, `"created": false`) {
		t.Errorf("json = %q, want created false", out)
	}
}

// close -discard says whether a file remains on disk at the discarded buffer's
// path, so a driver that recreated the content under a new name learns the old
// name is left behind. The word is the person's, the boolean the machine's.
func TestCLICloseDiscardReportsRemainder(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "x\n"})
	ed.remains = true

	out, errs, code := run(t, "close", "/w/a.go", "-discard")
	if code != 0 {
		t.Fatalf("close -discard = %d: %s", code, errs)
	}
	if !strings.Contains(out, "still on disk") {
		t.Errorf("stdout = %q, want it to say the file remains", out)
	}

	out, _, code = run(t, "close", "/w/a.go", "-discard", "-json")
	if code != 0 {
		t.Fatalf("close -json = %d: %s", code, errs)
	}
	if !strings.Contains(out, `"remains": true`) {
		t.Errorf("json = %q, want remains true", out)
	}

	// A close with nothing left behind says nothing extra.
	newFakeEditor(t, map[string]string{"/w/b.go": "y\n"})
	out, _, code = run(t, "close", "/w/b.go", "-discard")
	if code != 0 {
		t.Fatalf("close -discard = %d", code)
	}
	if strings.Contains(out, "still on disk") {
		t.Errorf("stdout = %q, want no remainder note", out)
	}
	if strings.TrimSpace(out) != "closed" {
		t.Errorf("stdout = %q, want a plain closed", out)
	}
}

// mkdir carries its directory operand to the wire and prints what it made. It
// is the fix for open -create on a path whose parent does not exist: make the
// package directory first, then the files inside it.
func TestCLIMkdir(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "x\n"})

	out, errs, code := run(t, "mkdir", "pkg/sub")
	if code != 0 {
		t.Fatalf("mkdir: code = %d: %s", code, errs)
	}
	if !strings.Contains(out, "created pkg/sub") {
		t.Errorf("stdout = %q, want it to name the directory", out)
	}
	if len(ed.mkdirs) != 1 || ed.mkdirs[0] != "pkg/sub" {
		t.Errorf("the wire carried %v, want [pkg/sub]", ed.mkdirs)
	}

	out, errs, code = run(t, "mkdir")
	if code != 2 || !strings.Contains(errs, "needs a path") {
		t.Errorf("mkdir with no operand: code = %d, stderr = %q", code, errs)
	}
}

// rename carries two paths to the wire — the old in Path and the new in
// NewPath — prints what moved, and takes mv as an alias.
func TestCLIRename(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "x\n"})

	out, errs, code := run(t, "rename", "/w/a.go", "/w/b.go")
	if code != 0 {
		t.Fatalf("rename: code = %d: %s", code, errs)
	}
	if !strings.Contains(out, "/w/a.go") || !strings.Contains(out, "/w/b.go") {
		t.Errorf("stdout = %q, want both paths named", out)
	}
	if ed.lastRename.Op != "rename" || ed.lastRename.Path != "/w/a.go" || ed.lastRename.NewPath != "/w/b.go" {
		t.Errorf("the wire carried %+v, want /w/a.go -> /w/b.go", ed.lastRename)
	}

	// mv is the same verb under the other name.
	out, errs, code = run(t, "mv", "/w/a.go", "/w/b.go")
	if code != 0 {
		t.Fatalf("mv: code = %d: %s", code, errs)
	}
	if ed.lastRename.Op != "rename" || ed.lastRename.NewPath != "/w/b.go" {
		t.Errorf("mv carried %+v, want the rename op", ed.lastRename)
	}

	// One operand is a usage error, not a rename to a guessed name.
	_, errs, code = run(t, "rename", "/w/a.go")
	if code != 2 || !strings.Contains(errs, "old and a new") {
		t.Errorf("rename with one operand: code = %d, stderr = %q", code, errs)
	}

	// A third operand is refused rather than ignored.
	_, errs, code = run(t, "rename", "/w/a.go", "/w/b.go", "/w/c.go")
	if code != 2 || !strings.Contains(errs, "unexpected argument") {
		t.Errorf("rename with three operands: code = %d, stderr = %q", code, errs)
	}
}

// delete carries its path and -withdraw to the wire, prints what it did, and
// deletions lists what is pending with the proposing author. The fake records
// the delete request so the flag is asserted on the wire, not only in output.
func TestCLIDeleteAndDeletions(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "package a\n"})

	out, errs, code := run(t, "delete", "/w/a.go")
	if code != 0 {
		t.Fatalf("delete: code = %d: %s", code, errs)
	}
	if !strings.Contains(out, "/w/a.go") {
		t.Errorf("delete output = %q, want it to name the path", out)
	}
	if ed.lastDelete.Op != "delete" || ed.lastDelete.Path != "/w/a.go" || ed.lastDelete.Withdraw {
		t.Errorf("the wire carried %+v, want a propose for /w/a.go", ed.lastDelete)
	}

	out, errs, code = run(t, "delete", "-withdraw", "/w/a.go")
	if code != 0 {
		t.Fatalf("delete -withdraw: code = %d: %s", code, errs)
	}
	if ed.lastDelete.Path != "/w/a.go" || !ed.lastDelete.Withdraw {
		t.Errorf("the wire carried %+v, want a withdraw for /w/a.go", ed.lastDelete)
	}
	if !strings.Contains(out, "withdrew") {
		t.Errorf("withdraw output = %q, want it to say so", out)
	}

	// The listing names the path and the proposing author.
	ed.deletions = []Deletion{{Path: "/w/a.go", Author: 7}}
	out, errs, code = run(t, "deletions")
	if code != 0 {
		t.Fatalf("deletions: code = %d: %s", code, errs)
	}
	if !strings.Contains(out, "/w/a.go") || !strings.Contains(out, "7") {
		t.Errorf("deletions output = %q, want the path and author", out)
	}

	// -json is the machine form.
	out, _, code = run(t, "deletions", "-json")
	if code != 0 || !strings.Contains(out, "\"path\": \"/w/a.go\"") || !strings.Contains(out, "\"author\": 7") {
		t.Errorf("deletions -json = %q (code %d)", out, code)
	}

	// No operand is a usage error.
	if _, _, code = run(t, "delete"); code != 2 {
		t.Errorf("delete with no operand: code = %d, want 2", code)
	}
}

// rmdirs lists pending dir-removals with the proposing author. The fake
// records the rmdir request so the flag is asserted on the wire, not only in
// output.
func TestCLIRmdirAndRmdirs(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "package a\n"})

	out, errs, code := run(t, "rmdir", "/w/pkg")
	if code != 0 {
		t.Fatalf("rmdir: code = %d: %s", code, errs)
	}
	if !strings.Contains(out, "/w/pkg") {
		t.Errorf("rmdir output = %q, want it to name the directory", out)
	}
	if ed.lastRmdir.Op != "rmdir" || ed.lastRmdir.Path != "/w/pkg" || ed.lastRmdir.Withdraw {
		t.Errorf("the wire carried %+v, want a propose for /w/pkg", ed.lastRmdir)
	}

	out, errs, code = run(t, "rmdir", "-withdraw", "/w/pkg")
	if code != 0 {
		t.Fatalf("rmdir -withdraw: code = %d: %s", code, errs)
	}
	if ed.lastRmdir.Path != "/w/pkg" || !ed.lastRmdir.Withdraw {
		t.Errorf("the wire carried %+v, want a withdraw for /w/pkg", ed.lastRmdir)
	}
	if !strings.Contains(out, "withdrew") {
		t.Errorf("withdraw output = %q, want it to say so", out)
	}

	// The listing names the directory and the proposing author.
	ed.dirRemovals = []DirRemoval{{Path: "/w/pkg", Author: 7}}
	out, errs, code = run(t, "rmdirs")
	if code != 0 {
		t.Fatalf("rmdirs: code = %d: %s", code, errs)
	}
	if !strings.Contains(out, "/w/pkg") || !strings.Contains(out, "7") {
		t.Errorf("rmdirs output = %q, want the directory and author", out)
	}

	// -json is the machine form.
	out, _, code = run(t, "rmdirs", "-json")
	if code != 0 || !strings.Contains(out, "\"path\": \"/w/pkg\"") || !strings.Contains(out, "\"author\": 7") {
		t.Errorf("rmdirs -json = %q (code %d)", out, code)
	}

	// No operand is a usage error.
	if _, _, code = run(t, "rmdir"); code != 2 {
		t.Errorf("rmdir with no operand: code = %d, want 2", code)
	}
}

// proposals is the one flat listing over the pending surface: per-kind human
// headers and a tagged JSON list.
func TestCLIProposals(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "package a\n"})

	ed.proposals = []Proposal{
		{Kind: "set", Path: "/w/a.go", Author: 4, Group: 7, Start: 1, End: 4},
		{Kind: "delete", Path: "/w/b.go", Author: 5, Start: -1, End: -1},
		{Kind: "rmdir", Path: "/w/sub", Author: 5, Start: -1, End: -1},
	}

	out, errs, code := run(t, "proposals")
	if code != 0 {
		t.Fatalf("proposals: code = %d: %s", code, errs)
	}
	for _, want := range []string{
		"change sets:", "pending deletions:", "pending dir-removals:",
		"/w/a.go", "group 7", "author 4", "bytes 1..4", "/w/b.go", "/w/sub",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("proposals output %q missing %q", out, want)
		}
	}

	out, _, code = run(t, "proposals", "-json")
	if code != 0 || !strings.Contains(out, "\"kind\": \"set\"") || !strings.Contains(out, "\"group\": 7") {
		t.Errorf("proposals -json = %q (code %d)", out, code)
	}
}

// mineProposals is the `proposals -mine` filter, the proposals analogue of
// mineOnly. It is unit-tested rather than driven over a connection because each
// raj ctl invocation is a fresh connection with its own author id, so a whoami
// in one run cannot predict the id the next run will write as.
func TestMineProposalsKeepsTheCallers(t *testing.T) {
	props := []Proposal{
		{Kind: "set", Path: "/w/a.go", Author: 4, Group: 1},
		{Kind: "delete", Path: "/w/b.go", Author: 5},
		{Kind: "rmdir", Path: "/w/sub", Author: 4},
	}
	got := mineProposals(props, 4)
	if len(got) != 2 || got[0].Path != "/w/a.go" || got[1].Path != "/w/sub" {
		t.Errorf("mineProposals = %+v, want a.go and sub", got)
	}
	if mineProposals(props, 9) != nil {
		t.Errorf("mineProposals with no match = %+v, want nil", mineProposals(props, 9))
	}
}

// mineOnly is the view `groups -mine` and `accept/reject -all -mine` share.
func TestMineOnlyKeepsTheCallersGroups(t *testing.T) {
	groups := []Group{{ID: 1, Author: 4}, {ID: 2, Author: 5}, {ID: 3, Author: 4}}
	got := mineOnly(groups, 4)
	if len(got) != 2 || got[0].ID != 1 || got[1].ID != 3 {
		t.Errorf("mineOnly = %+v, want groups 1 and 3", got)
	}
	if mineOnly(groups, 9) != nil {
		t.Errorf("mineOnly with no match = %+v, want nil", mineOnly(groups, 9))
	}
}

// review -json is the read-only listing: it returns the pending change sets
// without entering Review mode. The plain form enters the mode and lists them
// too, so the two surfaces cannot disagree about what is pending.
func TestCLIReviewListsWithoutEnteringMode(t *testing.T) {
	f := newFakeEditor(t, map[string]string{"/w/a.go": "hello\n"})
	f.groups = []Group{{ID: 7, Path: "/w/a.go", Author: 2, State: "proposed", Ops: 1, Bytes: 4}}

	out, _, code := run(t, "review", "/w/a.go", "-json")
	if code != 0 {
		t.Fatalf("review -json exit = %d\n%s", code, out)
	}
	if f.mode != "" {
		t.Errorf("review -json entered mode %q", f.mode)
	}
	if !strings.Contains(out, `"id": 7`) {
		t.Errorf("review -json output = %q, want the pending set", out)
	}

	out, _, code = run(t, "review", "/w/a.go")
	if code != 0 {
		t.Fatalf("review exit = %d\n%s", code, out)
	}
	if f.mode != "review" {
		t.Errorf("mode = %q after review, want review", f.mode)
	}
	if !strings.Contains(out, "review mode, 1 proposed") {
		t.Errorf("review output = %q, want the mode and count", out)
	}
}

// run -prog end to end for find: one frame locates text and gets back the byte
// span and the match count, so a program can find a position, read against it,
// and apply in the same batch.
func TestCLIRunProgramFind(t *testing.T) {
	newFakeEditor(t, map[string]string{"/w/a.go": "hello world\nhello again\n"})

	p := prog.Encode([]prog.Op{
		{Code: prog.OpPath, Payload: []byte("/w/a.go")},
		{Code: prog.OpQuery, Payload: []byte("hello")},
		{Code: prog.OpFind},
	})
	out, errs, code := run(t, "run", "-prog", string(p), "-json")
	if code != 0 {
		t.Fatalf("code = %d: %s", code, errs)
	}
	var res Response
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if !res.Found || res.FindStart != 0 || res.FindEnd != len("hello") || res.FindCount != 2 {
		t.Errorf("find answer = %+v", res)
	}
}

// A verb given no path names the buffer it actually acted on rather than saying
// "the buffer": the CLI asks the editor which tab is focused, so the refusal
// names the file the user is looking at. The fake reports a focused tab only
// when a test sets one, which is why the fallback stays the default.
func TestPathlessVerbNamesTheActiveBuffer(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "one\n"})
	ed.mu.Lock()
	ed.active = "/w/a.go"
	ed.mu.Unlock()

	_, errs, code := run(t, "edit", "-old", "not present", "-new", "x")
	if code == 0 {
		t.Fatal("a pathless edit replacing absent text succeeded")
	}
	if !strings.Contains(errs, "/w/a.go") {
		t.Errorf("refusal = %q, want it to name the active buffer /w/a.go", errs)
	}
}

// ls lists a directory's children, marks directories with a trailing slash, and
// carries -hidden to the wire. The fake records the request and serves a canned
// entry list, so both the output and the flag are asserted.
func TestCLILs(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "package a\n"})
	ed.entries = []Entry{
		{Name: "main.go", Path: "/w/pkg/main.go", Size: i64p(9)},
		{Name: "sub", Path: "/w/pkg/sub", Dir: true},
	}

	out, errs, code := run(t, "ls", "/w/pkg")
	if code != 0 {
		t.Fatalf("ls: code = %d: %s", code, errs)
	}
	if ed.lastLs.Op != "ls" || ed.lastLs.Path != "/w/pkg" || ed.lastLs.Hidden {
		t.Errorf("the wire carried %+v, want an ls of /w/pkg without -hidden", ed.lastLs)
	}
	if !strings.Contains(out, "main.go") || !strings.Contains(out, "sub/") {
		t.Errorf("ls output = %q, want the file and the marked directory", out)
	}

	// -hidden is the same flag the search carries; with no path it names the
	// workspace root.
	out, errs, code = run(t, "ls", "-hidden")
	if code != 0 {
		t.Fatalf("ls -hidden: code = %d: %s", code, errs)
	}
	if !ed.lastLs.Hidden || ed.lastLs.Path != "" {
		t.Errorf("the wire carried %+v, want -hidden with the default root", ed.lastLs)
	}

	// -json is the machine form, with the directory and the size.
	out, _, code = run(t, "ls", "-json")
	if code != 0 || !strings.Contains(out, `"name": "sub"`) ||
		!strings.Contains(out, `"dir": true`) || !strings.Contains(out, `"size": 9`) ||
		!strings.Contains(out, `"path": "/w/pkg/main.go"`) {
		t.Errorf("ls -json = %q (code %d)", out, code)
	}

	// An empty directory is an empty list, not an error.
	ed.entries = nil
	out, _, code = run(t, "ls", "-json")
	if code != 0 || strings.TrimSpace(out) != "[]" {
		t.Errorf("ls -json of an empty directory = %q (code %d), want []", out, code)
	}
}

// -hidden reaches the wire as a query field; the walk's inclusion of hidden
// paths is exercised in the app and search tests.
func TestSearchHiddenReachesTheQuery(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "needle\n"})
	if _, errs, code := run(t, "search", "-q", "needle", "-hidden"); code != 0 {
		t.Fatalf("search -hidden: code %d: %s", code, errs)
	}
	ed.mu.Lock()
	got := ed.searchHidden
	ed.mu.Unlock()
	if !got {
		t.Error("search -hidden did not reach the query on the wire")
	}

	if _, _, code := run(t, "search", "-q", "needle"); code != 0 {
		t.Fatalf("search: code %d", code)
	}
	ed.mu.Lock()
	got = ed.searchHidden
	ed.mu.Unlock()
	if got {
		t.Error("a plain search carried the include-hidden flag")
	}
}

// A read can name several targets in one call, so a read-read-read chain is one
// round trip. The single-target form is unchanged, and -json gives one object
// per file. Modelled on TestCLIReads.
func TestCLIReadMultipleTargets(t *testing.T) {
	newFakeEditor(t, map[string]string{
		"/w/a.go": "package a\n",
		"/w/b.go": "package b\n",
	})

	out, _, code := run(t, "read", "/w/a.go")
	if code != 0 || out != "package a\n" {
		t.Errorf("single read = %q, code %d", out, code)
	}

	out, _, code = run(t, "read", "/w/a.go", "/w/b.go")
	if code != 0 {
		t.Fatalf("multi read: code %d", code)
	}
	want := "==> /w/a.go <==\npackage a\n\n==> /w/b.go <==\npackage b\n"
	if out != want {
		t.Errorf("multi read = %q, want %q", out, want)
	}

	out, _, code = run(t, "read", "-json", "/w/a.go", "/w/b.go")
	if code != 0 {
		t.Fatalf("multi json: code %d", code)
	}
	var got struct {
		Files []struct {
			Path    string `json:"path"`
			Text    string `json:"text"`
			Version uint64 `json:"version"`
		} `json:"files"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("multi json = %q: %v", out, err)
	}
	if len(got.Files) != 2 {
		t.Fatalf("multi json files = %+v, want two", got.Files)
	}
	if got.Files[0].Path != "/w/a.go" || got.Files[0].Text != "package a\n" || got.Files[0].Version == 0 {
		t.Errorf("file 0 = %+v", got.Files[0])
	}
	if got.Files[1].Path != "/w/b.go" || got.Files[1].Text != "package b\n" || got.Files[1].Version == 0 {
		t.Errorf("file 1 = %+v", got.Files[1])
	}
}

// search -context returns each hit's neighbouring lines and the version it was
// found at, so a hit is actionable without a follow-up read; without the flag
// the grep-style line is unchanged. Modelled on TestCLIReads.
func TestCLISearchContext(t *testing.T) {
	newFakeEditor(t, map[string]string{
		"/w/a.go": "line one\nline two\nneedle here\nline four\n",
	})

	out, _, code := run(t, "search", "-q", "needle")
	if code != 0 || out != "/w/a.go:3:0:needle here\n" {
		t.Errorf("plain search = %q, code %d", out, code)
	}

	out, _, code = run(t, "search", "-q", "needle", "-context", "1")
	if code != 0 {
		t.Fatalf("context search: code %d", code)
	}
	want := "/w/a.go:3:0 version 1\nline two\nneedle here\nline four\n"
	if out != want {
		t.Errorf("context search = %q, want %q", out, want)
	}

	out, _, code = run(t, "search", "-q", "needle", "-context", "1", "-json")
	if code != 0 {
		t.Fatalf("context json: code %d", code)
	}
	var got struct {
		Matches []struct {
			Context string `json:"context"`
			Version uint64 `json:"version"`
		} `json:"matches"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("context json = %q: %v", out, err)
	}
	if len(got.Matches) != 1 {
		t.Fatalf("context json matches = %+v, want one", got.Matches)
	}
	if want := "line two\nneedle here\nline four"; got.Matches[0].Context != want {
		t.Errorf("json context = %q, want %q", got.Matches[0].Context, want)
	}
	if got.Matches[0].Version != 1 {
		t.Errorf("json version = %d, want 1", got.Matches[0].Version)
	}
}

// diagnostics may name several paths: one call sweeps them all rather than a
// shell loop spending a round trip per file.
func TestCLILSPDiagnosticsSweepsMultiplePaths(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "package a\n", "/w/b.go": "package b\n"})
	ed.lspJSON = `{"status":"ok"}`
	out, errs, code := run(t, "lsp", "diagnostics", "/w/a.go", "/w/b.go")
	if code != 0 {
		t.Fatalf("code = %d, stderr %q", code, errs)
	}
	if got := strings.Count(out, `"status": "ok"`); got != 2 {
		t.Errorf("status lines = %d, want one per path (out %q)", got, out)
	}
}

// A multi-path -json sweep is one framed document, not one bare object per
// operand. Each entry names the path it answers, so a reader can attribute a
// status without counting lines; before this the output was N objects with no
// path and json.Unmarshal of the whole stream failed on trailing data.
func TestCLILSPDiagnosticsBatchJSONIsFramed(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "package a\n", "/w/b.go": "package b\n"})
	ed.lspJSONByPath = map[string]string{
		"/w/a.go": `{"status":"ok"}`,
		"/w/b.go": `{"status":"ok"}`,
	}
	out, errs, code := run(t, "lsp", "diagnostics", "/w/a.go", "/w/b.go", "-json")
	if code != 0 {
		t.Fatalf("code = %d, stderr %q", code, errs)
	}
	var got struct {
		Files []struct {
			Path   string `json:"path"`
			Status string `json:"status"`
		} `json:"files"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("batch -json is not one document: %v (%q)", err, out)
	}
	if len(got.Files) != 2 {
		t.Fatalf("files = %+v, want one entry per operand", got.Files)
	}
	for i, want := range []string{"/w/a.go", "/w/b.go"} {
		if got.Files[i].Path != want || got.Files[i].Status != "ok" {
			t.Errorf("file %d = %+v, want path %q status ok", i, got.Files[i], want)
		}
	}
}

// An operand the editor never answered still gets an entry, and a status stays
// with the path that produced it: asking b first and leaving a unanswered must
// not shift b's status onto a's entry, which a positional reader of the old
// per-operand lines would do.
func TestCLILSPDiagnosticsBatchAttributesEachPath(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "package a\n", "/w/b.go": "package b\n"})
	ed.lspJSONByPath = map[string]string{
		"/w/b.go": `{"status":"starting","detail":"starting language server..."}`,
	}
	ed.lspErrByPath = map[string]string{"/w/a.go": "no open buffer for /w/a.go"}
	out, errs, code := run(t, "lsp", "diagnostics", "/w/b.go", "/w/a.go", "-json")
	if code != 1 {
		t.Fatalf("code = %d, want 1 for a refused and a starting path; stderr %q", code, errs)
	}
	var got struct {
		Files []struct {
			Path   string `json:"path"`
			Status string `json:"status"`
			Detail string `json:"detail"`
		} `json:"files"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("batch -json = %q: %v", out, err)
	}
	if len(got.Files) != 2 {
		t.Fatalf("files = %+v, want two entries", got.Files)
	}
	if got.Files[0].Path != "/w/b.go" || got.Files[0].Status != "starting" {
		t.Errorf("entry 0 = %+v, want b starting", got.Files[0])
	}
	if got.Files[1].Path != "/w/a.go" || got.Files[1].Status != "error" {
		t.Errorf("entry 1 = %+v, want a with an error status", got.Files[1])
	}
	if !strings.Contains(got.Files[1].Detail, "no open buffer") {
		t.Errorf("detail = %q, want the refusal", got.Files[1].Detail)
	}
	if !strings.Contains(errs, "no open buffer") {
		t.Errorf("stderr = %q, want the refusal named there too", errs)
	}
}

// A cold start must not corrupt a -json sweep. The message is folded into the
// entry's status and detail rather than interleaved as a bare line, so stdout
// stays one document; before this a bare object per path left the whole stream
// unparseable.
func TestCLILSPDiagnosticsBatchColdStartStaysJSON(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "package a\n", "/w/b.go": "package b\n"})
	ed.lspJSONByPath = map[string]string{
		"/w/a.go": `{"status":"starting","detail":"starting language server..."}`,
		"/w/b.go": `{"status":"ok"}`,
	}
	out, _, code := run(t, "lsp", "diagnostics", "/w/a.go", "/w/b.go", "-json")
	if code != 1 {
		t.Fatalf("code = %d, want 1 while a server is starting", code)
	}
	var got struct {
		Files []struct {
			Path   string `json:"path"`
			Status string `json:"status"`
			Detail string `json:"detail"`
		} `json:"files"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("cold-start -json is not one document: %v (%q)", err, out)
	}
	if len(got.Files) != 2 {
		t.Fatalf("files = %+v, want two entries", got.Files)
	}
	if got.Files[0].Status != "starting" || !strings.Contains(got.Files[0].Detail, "starting language server") {
		t.Errorf("first entry = %+v, want the starting message in its status", got.Files[0])
	}
	if got.Files[1].Status != "ok" {
		t.Errorf("second status = %q, want ok", got.Files[1].Status)
	}
}

// Single-path -json keeps its bare object: no files wrapper is introduced, so
// a driver written against the one-file shape keeps reading it.
func TestCLILSPDiagnosticsSinglePathJSONUnchanged(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "package a\n"})
	ed.lspJSON = `{"status":"ok","diagnostics":[{"line":1,"message":"boom"}]}`
	out, errs, code := run(t, "lsp", "diagnostics", "/w/a.go", "-json")
	if code != 0 {
		t.Fatalf("code = %d, stderr %q", code, errs)
	}
	var one LSPResult
	if err := json.Unmarshal([]byte(out), &one); err != nil {
		t.Fatalf("single-path -json = %q: %v", out, err)
	}
	if one.Status != "ok" || len(one.Diags) != 1 || one.Diags[0].Message != "boom" {
		t.Errorf("single-path result = %+v", one)
	}
	if strings.Contains(out, `"files"`) {
		t.Errorf("single-path -json grew a files wrapper: %q", out)
	}
}

// The plain sweep is still human-readable and now names each path above its
// own status, so two files' answers do not run together unlabelled.
func TestCLILSPDiagnosticsPlainNamesEachPath(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "package a\n", "/w/b.go": "package b\n"})
	ed.lspJSONByPath = map[string]string{
		"/w/a.go": `{"status":"ok"}`,
		"/w/b.go": `{"status":"ok"}`,
	}
	out, errs, code := run(t, "lsp", "diagnostics", "/w/a.go", "/w/b.go")
	if code != 0 {
		t.Fatalf("code = %d, stderr %q", code, errs)
	}
	for _, want := range []string{"==> /w/a.go <==", "==> /w/b.go <=="} {
		if !strings.Contains(out, want) {
			t.Errorf("plain sweep is missing %q: %q", want, out)
		}
	}
	if got := strings.Count(out, `"status": "ok"`); got != 2 {
		t.Errorf("plain sweep statuses = %d, want one per path (out %q)", got, out)
	}
}

// The CLI usage lists the reload verb and the save force flag, so a driver
// reading the help can find both.
func TestCLIUsageListsReloadAndForce(t *testing.T) {
	var out, errs strings.Builder
	if code := CLI([]string{"help"}, &out, &errs); code != 0 {
		t.Fatalf("help exit = %d, stderr %q", code, errs.String())
	}
	usage := out.String()
	if !strings.Contains(usage, "reload") {
		t.Errorf("usage does not list reload:\n%s", usage)
	}
	if !strings.Contains(usage, "--force") {
		t.Errorf("usage does not list -force:\n%s", usage)
	}
}

// PrintFlagUsage is the one printer behind the editor, daemon and ctl help: a
// single-character flag keeps one dash, a bool shows no value placeholder, and
// a value flag shows the placeholder flag.UnquoteUsage derives. The default
// rendering is the existing flagDefaultText, pinned here for the quoted string
// and the non-zero bool.
func TestPrintFlagUsageSpellingAndPlaceholders(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.Bool("q", false, "quiet")
	fs.Bool("force", false, "force the operation")
	fs.Bool("wrap", true, "wrap long lines")
	fs.String("name", "", "a name")
	fs.String("config", "x", "a config file")
	fs.Uint64("group", 0, "the change set id")

	var b strings.Builder
	PrintFlagUsage(&b, fs)
	text := b.String()
	for _, want := range []string{
		"  -q\n    \tquiet\n",
		"  --force\n    \tforce the operation\n",
		"  --wrap\n    \twrap long lines (default true)\n",
		"  --name string\n    \ta name\n",
		"  --config string\n    \ta config file (default \"x\")\n",
		"  --group uint\n    \tthe change set id\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("usage missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "--q") {
		t.Errorf("single-character flag printed with two dashes:\n%s", text)
	}
	for _, placeholder := range []string{"--force bool", "--wrap bool"} {
		if strings.Contains(text, placeholder) {
			t.Errorf("bool flag printed a value placeholder (%s):\n%s", placeholder, text)
		}
	}
}

// --at gives each path its own line span, so one read invocation batches
// different regions of different files. Each -json entry echoes the span it
// read, so a driver re-derives offsets without a second call.
//
// Precondition: /w/a.go is four lines and /w/b.go is five, so a shared span
// could not produce both texts; --at /w/a.go=1,2 /w/b.go=3,4 selects
// different regions.
func TestCLIReadAtPerTargetSpans(t *testing.T) {
	newFakeEditor(t, map[string]string{
		"/w/a.go": "a one\na two\na three\n",
		"/w/b.go": "b one\nb two\nb three\nb four\n",
	})

	out, errs, code := run(t, "read", "-json", "--at", "/w/a.go=1,2", "--at", "/w/b.go=3,4")
	if code != 0 {
		t.Fatalf("read --at: code %d: %s", code, errs)
	}
	var got struct {
		Files []struct {
			Path      string `json:"path"`
			Text      string `json:"text"`
			LineStart int    `json:"line_start"`
			LineEnd   int    `json:"line_end"`
		} `json:"files"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("read --at -json is not parseable: %v (%q)", err, out)
	}
	type want struct {
		path, text string
		lo, hi     int
	}
	wants := []want{
		{"/w/a.go", "a one\na two\n", 1, 2},
		{"/w/b.go", "b three\nb four\n", 3, 4},
	}
	if len(got.Files) != len(wants) {
		t.Fatalf("files = %+v, want %d entries", got.Files, len(wants))
	}
	for i, w := range wants {
		f := got.Files[i]
		if f.Path != w.path || f.Text != w.text || f.LineStart != w.lo || f.LineEnd != w.hi {
			t.Errorf("files[%d] = %+v, want %+v", i, f, w)
		}
	}

	// The plain form keeps the per-target framing, each target its own text.
	pout, perrs, pcode := run(t, "read", "--at", "/w/a.go=1,2", "--at", "/w/b.go=3,4")
	if pcode != 0 {
		t.Fatalf("plain read --at: code %d: %s", pcode, perrs)
	}
	if !strings.Contains(pout, "==> /w/a.go <==\na one\na two\n") ||
		!strings.Contains(pout, "==> /w/b.go <==\nb three\nb four\n") {
		t.Errorf("plain read --at = %q", pout)
	}
}

// The --at split is on the last "=", so a path that itself contains "=" is
// still addressable; the span is what follows the final "=".
func TestCLIReadAtPathWithEquals(t *testing.T) {
	newFakeEditor(t, map[string]string{"/w/a=b.go": "x\ny\nz\n"})

	out, errs, code := run(t, "read", "-json", "--at", "/w/a=b.go=1,2")
	if code != 0 {
		t.Fatalf("read --at with = in path: code %d: %s", code, errs)
	}
	var got struct {
		Text      string `json:"text"`
		LineStart int    `json:"line_start"`
		LineEnd   int    `json:"line_end"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("read -json is not parseable: %v (%q)", err, out)
	}
	if got.Text != "x\ny\n" || got.LineStart != 1 || got.LineEnd != 2 {
		t.Errorf("read = %+v, want text %q lines 1..2", got, "x\ny\n")
	}
}

// A malformed --at is refused by name, not silently ignored or read whole.
func TestCLIReadAtMalformed(t *testing.T) {
	newFakeEditor(t, map[string]string{"/w/a.go": "one\ntwo\nthree\n"})

	for _, bad := range []string{"/w/a.go=1", "/w/a.go=0,2", "/w/a.go=2,1", "/w/a.go=x,2", "/w/a.go"} {
		out, errs, code := run(t, "read", "--at", bad)
		if code != 2 {
			t.Errorf("--at %q: code = %d, want 2 (usage); stdout %q", bad, code, out)
		}
		if !strings.Contains(errs, bad) {
			t.Errorf("--at %q: stderr %q does not name the entry", bad, errs)
		}
	}
}

// A positional path named by --at is read once at that span, and a positional
// path without --at still reads whole — in one invocation.
//
// Precondition: /w/a.go has three content lines, so the 2,2 span is "a two"
// and the whole-file read of /w/b.go is longer than its own file.
func TestCLIReadAtDeduplicatesAndKeepsOthersWhole(t *testing.T) {
	newFakeEditor(t, map[string]string{
		"/w/a.go": "a one\na two\na three\n",
		"/w/b.go": "b one\nb two\n",
	})

	out, errs, code := run(t, "read", "-json", "--at", "/w/a.go=2,2", "/w/a.go", "/w/b.go")
	if code != 0 {
		t.Fatalf("read: code %d: %s", code, errs)
	}
	var got struct {
		Files []struct {
			Path      string `json:"path"`
			Text      string `json:"text"`
			LineStart *int   `json:"line_start"`
			LineEnd   *int   `json:"line_end"`
		} `json:"files"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("read -json is not parseable: %v (%q)", err, out)
	}
	if len(got.Files) != 2 {
		t.Fatalf("files = %+v, want 2 (the named path read once)", got.Files)
	}
	if got.Files[0].Path != "/w/a.go" || got.Files[0].Text != "a two\n" ||
		got.Files[0].LineStart == nil || *got.Files[0].LineStart != 2 ||
		got.Files[0].LineEnd == nil || *got.Files[0].LineEnd != 2 {
		t.Errorf("files[0] = %+v, want /w/a.go at 2,2", got.Files[0])
	}
	if got.Files[1].Path != "/w/b.go" || got.Files[1].Text != "b one\nb two\n" ||
		got.Files[1].LineStart != nil {
		t.Errorf("files[1] = %+v, want /w/b.go whole with no line span", got.Files[1])
	}

	// The same path named once positionally and once by --at is still one
	// target: the single-target plain output is the span text alone.
	sout, serrs, scode := run(t, "read", "--at", "/w/a.go=2,2", "/w/a.go")
	if scode != 0 {
		t.Fatalf("single read --at: code %d: %s", scode, serrs)
	}
	if sout != "a two\n" {
		t.Errorf("single read --at = %q, want the span text once", sout)
	}

	// --at wins for the path it names without erroring, and a path it does
	// not name still reads at the global span.
	gout, gerrs, gcode := run(t, "read", "-json", "-lines", "1,1", "--at", "/w/a.go=3,3", "/w/a.go", "/w/b.go")
	if gcode != 0 {
		t.Fatalf("read --at with global --lines: code %d: %s", gcode, gerrs)
	}
	var mixed struct {
		Files []struct {
			Path string `json:"path"`
			Text string `json:"text"`
		} `json:"files"`
	}
	if err := json.Unmarshal([]byte(gout), &mixed); err != nil {
		t.Fatalf("read -json is not parseable: %v (%q)", err, gout)
	}
	if len(mixed.Files) != 2 || mixed.Files[0].Text != "a three\n" || mixed.Files[1].Text != "b one\n" {
		t.Errorf("mixed spans = %+v, want --at to win for a.go and --lines for b.go", mixed.Files)
	}
}

// The single-target -json now echoes the line span it read, closing the gap
// where the driver had to re-derive it. A byte span keeps the shape it had.
//
// Precondition: /w/a.go is the 23-byte text "package a\n\nfunc f() {}\n"; the
// line range 3,3 is "func f() {}\n" and the byte span [11,17) is "func f".
func TestCLIReadJSONEchoesLineSpan(t *testing.T) {
	newFakeEditor(t, map[string]string{"/w/a.go": "package a\n\nfunc f() {}\n"})

	out, errs, code := run(t, "read", "-json", "-lines", "3,3", "/w/a.go")
	if code != 0 {
		t.Fatalf("read -json -lines: code %d: %s", code, errs)
	}
	var span struct {
		Text      string `json:"text"`
		LineStart *int   `json:"line_start"`
		LineEnd   *int   `json:"line_end"`
	}
	if err := json.Unmarshal([]byte(out), &span); err != nil {
		t.Fatalf("read -json is not parseable: %v (%q)", err, out)
	}
	if span.Text != "func f() {}\n" || span.LineStart == nil || *span.LineStart != 3 ||
		span.LineEnd == nil || *span.LineEnd != 3 {
		t.Errorf("read -lines 3,3 = %+v, want text and span 3..3", span)
	}

	// A byte span carries no line span, as before.
	bout, berrs, bcode := run(t, "read", "-json", "-start", "11", "-end", "17", "/w/a.go")
	if bcode != 0 {
		t.Fatalf("read -json -start: code %d: %s", bcode, berrs)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal([]byte(bout), &keys); err != nil {
		t.Fatalf("read -json is not parseable: %v (%q)", err, bout)
	}
	if _, ok := keys["line_start"]; ok {
		t.Errorf("a byte span grew a line_start: %s", bout)
	}
}

// -q repeats: one search call searches several patterns and labels each hit
// with the pattern that matched it. A single -q is byte-for-byte what it was.
//
// Precondition: /w/a.go holds "alpha\nbeta\ngamma\n", so alpha is line 1 and
// gamma line 3; today a repeated -q would carry only the last pattern.
func TestSearchMultipleQueries(t *testing.T) {
	newFakeEditor(t, map[string]string{"/w/a.go": "alpha\nbeta\ngamma\n"})

	out, errs, code := run(t, "search", "-q", "alpha", "-q", "gamma")
	if code != 0 {
		t.Fatalf("search two patterns: code %d: %s", code, errs)
	}
	if !strings.Contains(out, "alpha\t/w/a.go:1:0:alpha") ||
		!strings.Contains(out, "gamma\t/w/a.go:3:0:gamma") {
		t.Errorf("search two patterns = %q, want both hits labelled with their pattern", out)
	}

	// A single pattern is unchanged: no pattern prefix, exact bytes.
	sout, serrs, scode := run(t, "search", "-q", "alpha")
	if scode != 0 {
		t.Fatalf("single search: code %d: %s", scode, serrs)
	}
	if sout != "/w/a.go:1:0:alpha\n" {
		t.Errorf("single search = %q, want the unprefixed line", sout)
	}

	// The context header names the pattern when several were given.
	cout, cerrs, ccode := run(t, "search", "-q", "alpha", "-q", "gamma", "-context", "1")
	if ccode != 0 {
		t.Fatalf("search -context: code %d: %s", ccode, cerrs)
	}
	if !strings.Contains(cout, "alpha\t/w/a.go:1:0 version 1") ||
		!strings.Contains(cout, "gamma\t/w/a.go:3:0 version 1") {
		t.Errorf("multi -context = %q, want the pattern named in each block header", cout)
	}

	// The whole-JSON form carries the pattern on each match when several were
	// given, and no pattern key for a single one.
	jout, jerrs, jcode := run(t, "search", "-q", "alpha", "-q", "gamma", "-json")
	if jcode != 0 {
		t.Fatalf("search -json: code %d: %s", jcode, jerrs)
	}
	var multi struct {
		Matches []struct {
			Text    string `json:"text"`
			Pattern string `json:"pattern"`
		} `json:"matches"`
	}
	if err := json.Unmarshal([]byte(jout), &multi); err != nil {
		t.Fatalf("search -json is not parseable: %v (%q)", err, jout)
	}
	if len(multi.Matches) != 2 || multi.Matches[0].Pattern != "alpha" || multi.Matches[1].Pattern != "gamma" {
		t.Errorf("multi -json matches = %+v, want the pattern on each", multi.Matches)
	}
	sjout, sjerrs, sjcode := run(t, "search", "-q", "alpha", "-json")
	if sjcode != 0 {
		t.Fatalf("single search -json: code %d: %s", sjcode, sjerrs)
	}
	if strings.Contains(sjout, `"pattern"`) {
		t.Errorf("single -json grew a pattern key: %s", sjout)
	}
}

// Several patterns share one exit code: any match is 0, none is non-zero.
func TestSearchMultipleQueriesExit(t *testing.T) {
	newFakeEditor(t, map[string]string{"/w/a.go": "alpha\n"})

	if _, _, code := run(t, "search", "-q", "alpha", "-q", "zzz"); code != 0 {
		t.Errorf("one pattern matched: code = %d, want 0", code)
	}
	if _, _, code := run(t, "search", "-q", "yyy", "-q", "zzz"); code != 1 {
		t.Errorf("no pattern matched: code = %d, want 1", code)
	}
}

// The flags reach every pattern, and the metacharacter hint speaks only for
// the pattern that missed.
func TestSearchMultipleQueriesFlagsAndHint(t *testing.T) {
	newFakeEditor(t, map[string]string{"/w/a.go": "alpha\nbeta\n"})

	// -case applies to both patterns: alpha matches, BETA is the wrong case.
	out, errs, code := run(t, "search", "-q", "alpha", "-q", "BETA", "-case")
	if code != 0 {
		t.Fatalf("search -case: code %d: %s", code, errs)
	}
	if !strings.Contains(out, "alpha\t/w/a.go:1:0:alpha") || strings.Contains(out, "BETA") {
		t.Errorf("search -case = %q, want only the exact-case hit", out)
	}

	// One pattern with regex metacharacters and no hit gets the hint; the
	// plain no-hit pattern beside it does not.
	_, errs, code = run(t, "search", "-q", "func (", "-q", "absent")
	if code != 1 {
		t.Fatalf("no hits: code = %d, want 1", code)
	}
	if n := strings.Count(errs, "--regex"); n != 1 {
		t.Errorf("stderr = %q, want exactly one --regex hint", errs)
	}
	if !strings.Contains(errs, `"func ("`) {
		t.Errorf("stderr = %q, want the hint to name the metacharacter pattern", errs)
	}
}
