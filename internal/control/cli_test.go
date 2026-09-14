package control

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
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
	// makes the refusal an ordinary stale offset.
	lease uint64
	// searchPath records the -path the last search carried, so a CLI test can
	// assert the flag reaches the wire without a real walker.
	searchPath string
	// diffJSON is the canned diff answer; empty means no pending changes.
	diffJSON string
	// mode records the app mode a request switched to, so a test can assert
	// that `review -json` did not enter review while a plain `review` did.
	// Empty means still editing.
	mode string
	// lspJSON is the canned answer an lsp request returns, verbatim, so a test
	// can drive the CLI's diagnostics handling without a language server.
	lspJSON string
	// truncated is the per-file truncation the fake search reports, so the CLI
	// can be tested on a walk that cut a file down without a real one.
	truncated []TruncatedFile
	// headless names docs the fake reports as loaded with no tab, so the CLI
	// buffers output can be tested on the field that says so.
	headless map[string]bool
	// claims is the fake's working set, and lastClaim the request that last
	// touched it, so a CLI test can assert the wire fields. claimWarnings and
	// claimOverlaps are canned answers the CLI can be tested on.
	claims        []string
	lastClaim     Request
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
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	f := &fakeEditor{docs: map[string]string{}, vers: map[string]uint64{}, stop: make(chan struct{})}
	for k, v := range docs {
		f.docs[k], f.vers[k] = v, 1
	}
	wake := make(chan struct{}, 64)
	srv, err := Listen(addr, func() {
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
	case "snapshot":
		return Response{OK: true, Searcher: f}
	case "ping":
		f.authors = append(f.authors, req.Author)
		return Response{OK: true, Root: "/w", PID: 1}
	case "buffers":
		var bufs []Buffer
		for p, text := range f.docs {
			bufs = append(bufs, Buffer{Path: p, Version: f.vers[p], Bytes: len(text),
				Lines: strings.Count(text, "\n"), Headless: f.headless[p]})
		}
		return Response{OK: true, Root: "/w", Buffers: bufs}
	case "text":
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
		if start < 0 {
			start, end = 0, len(text)
		} else {
			if end < 0 || end > len(text) {
				end = len(text)
			}
			if start > len(text) {
				start = len(text)
			}
		}
		res := Response{OK: true, Spans: []Span{{Text: text[start:end], Author: FirstAgent}}, Version: f.vers[path]}
		if req.Annotated {
			res.StatesJSON = fmt.Sprintf(`[{"off":0,"len":%d,"group":0,"state":"accepted"}]`, end-start)
		}
		return res
	case "open":
		if _, ok := f.docs[path]; !ok {
			if !req.Create {
				// The fake has no disk, so not-in-docs is the missing file the
				// real host would stat for.
				return Response{Err: "no open buffer or file at " + path}
			}
			f.docs[path], f.vers[path] = "", 1
		}
		return Response{OK: true, Version: f.vers[path]}
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
			return Response{Err: "stale", Conflicts: []Conflict{{Index: 0, Group: f.lease, Hunk: req.Hunks[0]}}}
		}
		for i := len(req.Hunks) - 1; i >= 0; i-- { // back to front: offsets stay valid
			h := req.Hunks[i]
			text = text[:h.Start] + h.Text + text[h.End:]
		}
		f.docs[path], f.vers[path] = text, f.vers[path]+1
		return Response{OK: true, Version: f.vers[path]}
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
	case "lspprep":
		return Response{OK: true, LSP: fakeLSP{json: f.lspJSON}}
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
	}
	return Response{Err: "unknown op " + req.Op}
}

// fakeLSP is a language-server answer the CLI can be handed without a server:
// the JSON is exactly what a real lspCaller would have produced.
type fakeLSP struct{ json string }

func (f fakeLSP) Run(context.Context) ([]byte, error) { return []byte(f.json), nil }

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

func TestCLIReadSpan(t *testing.T) {
	newFakeEditor(t, map[string]string{"/w/a.go": "package a\n\nfunc f() {}\n"})

	out, _, code := run(t, "read", "-start", "11", "-end", "17", "/w/a.go")
	if code != 0 || out != "func f" {
		t.Errorf("read span = %q, code %d", out, code)
	}
}

// -annotated reaches the server and its state runs reach the JSON output; the
// plain form still prints just the text.
func TestCLIReadAnnotated(t *testing.T) {
	newFakeEditor(t, map[string]string{"/w/a.go": "package a\n\nfunc f() {}\n"})

	out, _, code := run(t, "read", "-annotated", "/w/a.go")
	if code != 0 || out != "package a\n\nfunc f() {}\n" {
		t.Errorf("annotated read = %q, code %d", out, code)
	}

	out, _, code = run(t, "read", "-annotated", "-json", "/w/a.go")
	if code != 0 {
		t.Fatalf("annotated json read: code %d", code)
	}
	if !strings.Contains(out, `"states"`) || !strings.Contains(out, `"accepted"`) {
		t.Errorf("annotated json = %q, want states", out)
	}
}

// A span written as positional operands used to read the whole file and look
// like it worked, with two numbers that look like offsets read does take. The
// refusal names the flags that carry them.
func TestCLIRefusesPositionalSpan(t *testing.T) {
	newFakeEditor(t, map[string]string{"/w/a.go": "package a\n\nfunc f() {}\n"})

	out, errs, code := run(t, "read", "/w/a.go", "940", "1000")
	if code != 2 {
		t.Errorf("read with positional offsets: code = %d, want 2 (usage); stdout %q", code, out)
	}
	if out != "" {
		t.Errorf("a refused read still wrote to stdout: %q", out)
	}
	if !strings.Contains(errs, `unexpected argument "940"`) || !strings.Contains(errs, "-start/-end") {
		t.Errorf("stderr = %q, want the stray operand and the flags that take it", errs)
	}

	out, errs, code = run(t, "dump", "/w/a.go", "0", "10")
	if code != 2 || out != "" || !strings.Contains(errs, "-start/-end") {
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

// A bare positional is the pattern typed in the wrong place: search takes
// no path, so accepting it silently would run a different search than was
// meant — the refusal names both flags the caller might have wanted.
func TestSearchRefusesBareArgument(t *testing.T) {
	newFakeEditor(t, map[string]string{"/w/a.go": "needle\n"})
	out, errs, code := run(t, "search", "needle")
	if code != 2 {
		t.Errorf("code = %d, want 2 (usage); stdout %q", code, out)
	}
	want := `search: unexpected argument "needle" — the pattern goes to -q; to limit paths use -include or -path`
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
	if !strings.Contains(errs, "search: warning: -include pattern(s) matched no files") {
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
	if strings.Contains(errs, "-include") {
		t.Errorf("stderr = %q, want no include warning", errs)
	}

	_, errs, code = run(t, "search", "-q", "absent")
	if code != 1 {
		t.Errorf("code = %d, want 1 (no hits)", code)
	}
	if strings.Contains(errs, "-include") {
		t.Errorf("stderr = %q, want no include warning", errs)
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
	ed.lease = 7
	ed.bump = func() { ed.vers["/w/a.go"]++ } // force the apply path to conflict

	_, errs, code := run(t, "edit", "-old", "world", "-new", "socket")
	if code == 0 {
		t.Fatalf("a leased edit reported success")
	}
	if !strings.Contains(errs, "change set 7 owns this text") {
		t.Errorf("message %q does not name the lease owner", errs)
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
		`"bytes":1,"first":1,"last":1,"hunks":[{"start":6,"end":11,"old":"world","new":"earth"}],"moved":0}]`

	out, errs, code := run(t, "diff", "/w/a.go")
	if code != 0 {
		t.Fatalf("code %d: %s", code, errs)
	}
	for _, want := range []string{"group 7", "author 2", "@@ 6..11 @@", "-world", "+earth"} {
		if !strings.Contains(out, want) {
			t.Errorf("diff output %q missing %q", out, want)
		}
	}

	out, _, code = run(t, "diff", "-json", "/w/a.go")
	if code != 0 || !strings.Contains(out, `"old": "world"`) || !strings.Contains(out, `"new": "earth"`) {
		t.Errorf("json diff = %q, code %d", out, code)
	}

	// No pending change sets is a clean, explicit answer on exit 0.
	ed.diffJSON = ""
	out, _, code = run(t, "diff", "/w/a.go")
	if code != 0 || !strings.Contains(out, "no pending changes") {
		t.Errorf("clean diff = %q, code %d", out, code)
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
	os.Unsetenv("RAJ_SOCKET")
	os.Unsetenv("RAJ_CONTROL_ADDR")
	_, errs, code := run(t, "buffers")
	if code == 0 || !strings.Contains(errs, "--control") {
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
	if got, _ := Locate("/explicit.sock", ""); got != "/explicit.sock" {
		t.Errorf("got %q, want the explicit path", got)
	}
	if got, _ := Locate("", ""); got != "/from/env.sock" {
		t.Errorf("got %q, want the environment", got)
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
	gate := f.gate
	truncated := append([]TruncatedFile(nil), f.truncated...)
	f.searchPath = q.Path
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
			emit([]SearchMatch{{Path: p, Line: 1, Text: t}})
			select {
			case <-ctx.Done():
				f.mu.Lock()
				f.cancelled = true
				f.mu.Unlock()
				return files, considered, false, nil, ctx.Err()
			case <-gate:
			}
		}
		for i, line := range strings.Split(t, "\n") {
			if c := strings.Index(line, q.Text); c >= 0 {
				emit([]SearchMatch{{Path: p, Line: i + 1, Col: c, Len: len(q.Text), Text: line}})
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

// localise rewrites every path in a response into the caller's view. The
// fields that hide a path are the ones a nested document or a prose error
// carries: the truncated file list, the encoded diff, and an editor refusal.
// Each is covered here, and the diff's hunk text is checked to prove a
// path-like Old or New is not rewritten along with the Group path beside it.
func TestLocaliseRewritesNestedAndProsePaths(t *testing.T) {
	var c Client
	c.SetMapper(Mapper{Local: "/workspace", Editor: "/Users/rajan/src/raj"})

	res := Response{
		Root:      "/Users/rajan/src/raj",
		Truncated: []TruncatedFile{{Path: "/Users/rajan/src/raj/big.md", Shown: 2, Total: 9}},
		DiffJSON: `[{"id":7,"path":"/Users/rajan/src/raj/a.go","author":2,` +
			`"state":"proposed","ops":1,"bytes":1,"first":1,"last":1,` +
			`"hunks":[{"start":0,"end":0,"old":"/Users/rajan/src/raj in text","new":""}],"moved":0}]`,
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
}

// A root that is only a character prefix of a longer directory name is not a
// path inside the mapped tree, and rewriting it would corrupt the message.
func TestLocaliseErrLeavesNonBoundaryAlone(t *testing.T) {
	var c Client
	c.SetMapper(Mapper{Local: "/workspace", Editor: "/Users/rajan/src/raj"})
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
	if !strings.Contains(out, "key: raj-") || !strings.Contains(out, "-as raj-") {
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
	if !strings.Contains(errs, "-hunks") || !strings.Contains(errs, "-start") {
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
	if code != 2 || !strings.Contains(errs, "-all and -group are alternatives") {
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
