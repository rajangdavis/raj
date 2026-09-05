package control

import (
	"bytes"
	"context"
	"os"
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
	stop chan struct{}
}

func newFakeEditor(t *testing.T, docs map[string]string) *fakeEditor {
	t.Helper()
	return newFakeEditorAt(t, filepath.Join(t.TempDir(), "c.sock"), docs)
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
				Lines: strings.Count(text, "\n")})
		}
		return Response{OK: true, Root: "/w", Buffers: bufs}
	case "text":
		text, ok := f.docs[path]
		if !ok {
			return Response{Err: "no open buffer for " + path}
		}
		return Response{OK: true, Spans: []Span{{Text: text, Author: FirstAgent}}, Version: f.vers[path]}
	case "open":
		if _, ok := f.docs[path]; !ok {
			f.docs[path], f.vers[path] = "", 1
		}
		return Response{OK: true, Version: f.vers[path]}
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
			return Response{Err: "stale", Conflicts: []Conflict{{Index: 0, Hunk: req.Hunks[0]}}}
		}
		for i := len(req.Hunks) - 1; i >= 0; i-- { // back to front: offsets stay valid
			h := req.Hunks[i]
			text = text[:h.Start] + h.Text + text[h.End:]
		}
		f.docs[path], f.vers[path] = text, f.vers[path]+1
		return Response{OK: true, Version: f.vers[path]}
	case "save":
		return Response{OK: true, Version: f.vers[path]}
	}
	return Response{Err: "unknown op " + req.Op}
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

func TestCLIUsage(t *testing.T) {
	if _, errs, code := run(t); code != 2 || !strings.Contains(errs, "usage") {
		t.Errorf("no args: code %d, stderr %q", code, errs)
	}
	if out, _, code := run(t, "help"); code != 0 || !strings.Contains(out, "edit") {
		t.Errorf("help: code %d, stdout %q", code, out)
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
func (f *fakeEditor) Search(ctx context.Context, q SearchQuery, emit func([]SearchMatch)) (int, bool, error) {
	f.mu.Lock()
	docs := make(map[string]string, len(f.docs))
	for k, v := range f.docs {
		docs[k] = v
	}
	gate := f.gate
	f.mu.Unlock()

	files := 0
	for p, t := range docs {
		if gate != nil {
			// Emit one batch, then wait: the caller gets a partial result and
			// can cancel while the rest is outstanding.
			emit([]SearchMatch{{Path: p, Line: 1, Text: t}})
			select {
			case <-ctx.Done():
				f.mu.Lock()
				f.cancelled = true
				f.mu.Unlock()
				return files, false, ctx.Err()
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
	return files, false, nil
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
	if !strings.Contains(out, "Stale") || !strings.Contains(out, "AgentOnly") {
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
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "x\n"})
	dir := filepath.Dir(DefaultPath())
	os.MkdirAll(dir, 0o700)

	// A file that looks like a socket but answers nothing.
	dead := filepath.Join(dir, "9999.sock")
	if err := os.WriteFile(dead, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// And a live one, in the same directory discovery scans.
	live := filepath.Join(dir, "live.sock")
	os.Remove(live)
	if err := os.Symlink(ed.srv.Path(), live); err != nil {
		t.Skipf("cannot link a live socket into the scan directory: %v", err)
	}
	t.Cleanup(func() { os.Remove(live) })

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
