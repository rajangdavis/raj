package control

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"raj/internal/hooks"
)

// testHookRow is one canned row for the Dispatch tests.
func testHookRow(name string) HookRow {
	return HookRow{Name: name, Action: `["true"]`, Trigger: "agent", Enabled: true}
}

// TestDispatchHookList drives list through Dispatch over a memHost with canned
// rows, and pins that they come back as the JSON array the CLI reads.
// Precondition: a memHost with two rows in name order. Without the Dispatch
// "hook" case the op is unknown, and without the Marshal the reply carries no
// HookJSON, so both the OK and the decode fail.
func TestDispatchHookList(t *testing.T) {
	h := newMemHost("/w", nil)
	h.hooks = []HookRow{testHookRow("alpha"), testHookRow("zeta")}
	g := NewGuard(h)
	res := Dispatch(g, Request{Op: "hook", HookMode: "list"})
	if !res.OK {
		t.Fatalf("hook list = %+v", res)
	}
	var rows []HookRow
	if err := json.Unmarshal([]byte(res.HookJSON), &rows); err != nil {
		t.Fatalf("HookJSON %q: %v", res.HookJSON, err)
	}
	if len(rows) != 2 || rows[0].Name != "alpha" || rows[1].Name != "zeta" {
		t.Errorf("list = %+v, want alpha then zeta", rows)
	}
}

// TestDispatchHookListEmptyIsAnArray pins that an empty workspace answers []
// rather than null, so a script ranging over the answer needs no special case.
// Precondition: a memHost with no hooks. Without the empty-not-nil guard the
// marshal of a nil slice is "null" and the assertion fails.
func TestDispatchHookListEmptyIsAnArray(t *testing.T) {
	g := NewGuard(newMemHost("/w", nil))
	res := Dispatch(g, Request{Op: "hook", HookMode: "list"})
	if !res.OK || res.HookJSON != "[]" {
		t.Fatalf("empty hook list = %+v, want HookJSON []", res)
	}
}

// TestDispatchHookShow covers a hit and a miss. Precondition: one canned row.
// The hit returns that row's JSON; the miss refuses and names the name asked
// for rather than reporting an empty success.
func TestDispatchHookShow(t *testing.T) {
	h := newMemHost("/w", nil)
	h.hooks = []HookRow{testHookRow("check")}
	g := NewGuard(h)

	res := Dispatch(g, Request{Op: "hook", HookMode: "show", HookName: "check"})
	if !res.OK {
		t.Fatalf("hook show check = %+v", res)
	}
	var row HookRow
	if err := json.Unmarshal([]byte(res.HookJSON), &row); err != nil {
		t.Fatalf("HookJSON %q: %v", res.HookJSON, err)
	}
	if row.Name != "check" {
		t.Errorf("show = %+v, want check", row)
	}

	res = Dispatch(g, Request{Op: "hook", HookMode: "show", HookName: "absent"})
	if res.OK || !strings.Contains(res.Err, "absent") {
		t.Errorf("show absent = %+v, want a refusal naming it", res)
	}

	res = Dispatch(g, Request{Op: "hook", HookMode: "show"})
	if res.OK || !strings.Contains(res.Err, "needs a name") {
		t.Errorf("show with no name = %+v, want a needs-a-name refusal", res)
	}
}

// TestDispatchHookPut covers the validating path: a valid row is stored, an
// invalid one is refused before the host is asked. Precondition: a memHost with
// no hooks. The valid row appears in the host set; the invalid row leaves it
// untouched, so a refusal that stored anyway would fail the length check.
func TestDispatchHookPut(t *testing.T) {
	h := newMemHost("/w", nil)
	g := NewGuard(h)

	valid, err := json.Marshal(HookRow{Name: "check", Action: `["go","test","./..."]`, Trigger: "agent", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if res := Dispatch(g, Request{Op: "hook", HookMode: "put", HookJSON: string(valid)}); !res.OK {
		t.Fatalf("hook put valid = %+v", res)
	}
	if len(h.hooks) != 1 || h.hooks[0].Name != "check" {
		t.Fatalf("PutHook did not store the valid row: %+v", h.hooks)
	}

	invalid, err := json.Marshal(HookRow{Name: "bad", Action: "not json", Trigger: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	res := Dispatch(g, Request{Op: "hook", HookMode: "put", HookJSON: string(invalid)})
	if res.OK {
		t.Fatalf("an invalid action was accepted: %+v", res)
	}
	if len(h.hooks) != 1 {
		t.Errorf("the invalid row reached the host: %+v", h.hooks)
	}

	if res := Dispatch(g, Request{Op: "hook", HookMode: "put", HookJSON: "{"}); res.OK {
		t.Error("malformed put JSON was accepted")
	}
}

// TestDispatchHookRmEnableDisable drives the authoring modes through Dispatch.
// Precondition: one enabled canned row. disable then enable flips the stored
// flag; an absent name is refused for enable; rm removes the row; an unknown
// mode is refused.
func TestDispatchHookRmEnableDisable(t *testing.T) {
	h := newMemHost("/w", nil)
	h.hooks = []HookRow{testHookRow("check")}
	g := NewGuard(h)

	if res := Dispatch(g, Request{Op: "hook", HookMode: "disable", HookName: "check"}); !res.OK {
		t.Fatalf("disable = %+v", res)
	}
	if h.hooks[0].Enabled {
		t.Error("disable left the hook enabled")
	}
	if res := Dispatch(g, Request{Op: "hook", HookMode: "enable", HookName: "check"}); !res.OK {
		t.Fatalf("enable = %+v", res)
	}
	if !h.hooks[0].Enabled {
		t.Error("enable left the hook disabled")
	}
	if res := Dispatch(g, Request{Op: "hook", HookMode: "enable", HookName: "absent"}); res.OK {
		t.Error("enabling an absent hook succeeded")
	}
	if res := Dispatch(g, Request{Op: "hook", HookMode: "rm", HookName: "check"}); !res.OK {
		t.Fatalf("rm = %+v", res)
	}
	if len(h.hooks) != 0 {
		t.Errorf("rm left rows: %+v", h.hooks)
	}
	if res := Dispatch(g, Request{Op: "hook", HookMode: "bogus"}); res.OK || !strings.Contains(res.Err, "unknown mode") {
		t.Errorf("bogus mode = %+v, want an unknown-mode refusal", res)
	}
}

// TestTCPRefusesHookAuthoring pins the transport rule: put, rm, enable and
// disable are refused on a TCP connection with the server's own message, while
// list is a read and is served. Precondition: a TCP editor whose fake answers
// hook through a Guard over a memHost. Without localOnly at the serve loop the
// authoring request is dispatched and succeeds, so the refusal assertion fails;
// the list assertion pins that the predicate did not overreach to the reads.
func TestTCPRefusesHookAuthoring(t *testing.T) {
	ed := newTCPEditor(t, map[string]string{"/w/a.go": "x"})
	t.Setenv(TokenEnv, ed.srv.Token())
	c, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	put, err := json.Marshal(HookRow{Name: "check", Action: `["true"]`, Trigger: "agent", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	authoring := []Request{
		{Op: "hook", HookMode: "put", HookJSON: string(put)},
		{Op: "hook", HookMode: "rm", HookName: "check"},
		{Op: "hook", HookMode: "enable", HookName: "check"},
		{Op: "hook", HookMode: "disable", HookName: "check"},
	}
	for _, req := range authoring {
		res, err := c.Do(req)
		if err != nil {
			t.Fatalf("hook %s over TCP: %v", req.HookMode, err)
		}
		if res.Err == "" {
			t.Errorf("hook %s was allowed over TCP: %+v", req.HookMode, res)
			continue
		}
		if !strings.Contains(res.Err, "refused over TCP") {
			t.Errorf("hook %s refusal %q does not name the transport", req.HookMode, res.Err)
		}
	}

	res, err := c.Do(Request{Op: "hook", HookMode: "list"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || res.Err != "" {
		t.Errorf("hook list over TCP = %+v, want a read allowed", res)
	}
}

// hookRunFixture builds a Guard over a memHost holding one hook, wired to a
// fresh shared Gate the way the server's guard is wired in the app. The host's
// Root is "/w" and its projection is empty unless a test sets one.
func hookRunFixture(t *testing.T, row HookRow, opts hooks.Options) (*Guard, *memHost) {
	t.Helper()
	h := newMemHost("/w", nil)
	h.hooks = []HookRow{row}
	g := NewGuard(h)
	g.HookGate = newHookGate(hooks.NewGate(opts))
	return g, h
}

// TestDispatchHookRunAdmission pins the caller-kind rule and the policy
// refusals at the event-thread prep. A hook with Agent false is refused to an
// agent and admitted to the local human or a joined human; an agent-callable
// hook is admitted to an agent; a disabled hook and an unknown name are
// refused.
//
// Precondition: a memHost with one row and a Guard wired to a fresh Gate.
// Without Admit the Agent flag is ignored and every case admits; without the
// caller-kind decision a joined human is misread as an agent; without the run
// case the op is an unknown hook mode.
func TestDispatchHookRunAdmission(t *testing.T) {
	t.Run("agent needs Agent true", func(t *testing.T) {
		g, _ := hookRunFixture(t, testHookRow("check"), hooks.Options{})
		res := Dispatch(g, Request{Op: "hook", HookMode: "run", HookName: "check", Author: FirstAgent})
		if res.OK || !strings.Contains(res.Err, "not callable by an agent") {
			t.Fatalf("agent on Agent=false = %+v, want the not-agent-callable refusal", res)
		}
	})
	t.Run("agent admitted when Agent true", func(t *testing.T) {
		row := testHookRow("check")
		row.Agent = true
		g, _ := hookRunFixture(t, row, hooks.Options{})
		res := Dispatch(g, Request{Op: "hook", HookMode: "run", HookName: "check", Author: FirstAgent})
		if !res.OK || res.Err != "" {
			t.Fatalf("agent on Agent=true = %+v, want admitted", res)
		}
		if len(res.HookArgv) != 1 || res.HookArgv[0] != "true" {
			t.Errorf("HookArgv = %q, want the hook's argv", res.HookArgv)
		}
		if res.Root != "/w" {
			t.Errorf("Root = %q, want /w", res.Root)
		}
	})
	t.Run("local human admitted on Agent false", func(t *testing.T) {
		g, _ := hookRunFixture(t, testHookRow("check"), hooks.Options{})
		res := Dispatch(g, Request{Op: "hook", HookMode: "run", HookName: "check", Author: LocalHuman})
		if !res.OK || res.Err != "" {
			t.Fatalf("local human = %+v, want admitted", res)
		}
	})
	t.Run("joined human admitted on Agent false", func(t *testing.T) {
		g, _ := hookRunFixture(t, testHookRow("check"), hooks.Options{})
		g.Participants = NewRegistry()
		id, err := g.Participants.Join("client:desk", "desk", KindHuman)
		if err != nil {
			t.Fatal(err)
		}
		res := Dispatch(g, Request{Op: "hook", HookMode: "run", HookName: "check", Author: id})
		if !res.OK || res.Err != "" {
			t.Fatalf("joined human = %+v, want admitted", res)
		}
	})
	t.Run("disabled refused", func(t *testing.T) {
		row := testHookRow("check")
		row.Enabled = false
		g, _ := hookRunFixture(t, row, hooks.Options{})
		res := Dispatch(g, Request{Op: "hook", HookMode: "run", HookName: "check", Author: LocalHuman})
		if res.OK || !strings.Contains(res.Err, "disabled") {
			t.Fatalf("disabled = %+v, want the disabled refusal", res)
		}
	})
	t.Run("unknown refused", func(t *testing.T) {
		g, _ := hookRunFixture(t, testHookRow("check"), hooks.Options{})
		res := Dispatch(g, Request{Op: "hook", HookMode: "run", HookName: "absent", Author: LocalHuman})
		if res.OK || !strings.Contains(res.Err, "no such hook") {
			t.Fatalf("unknown = %+v, want the no-such-hook refusal", res)
		}
	})
	t.Run("hookprep routes to run admission", func(t *testing.T) {
		g, _ := hookRunFixture(t, testHookRow("check"), hooks.Options{})
		res := Dispatch(g, Request{Op: "hookprep", HookName: "check", Author: LocalHuman})
		if !res.OK || res.Err != "" {
			t.Fatalf("hookprep = %+v, want it admitted through the run path", res)
		}
	})
}

// TestDispatchHookRunGating pins the shared Gate's two refusals through the
// prep: a second run while the first is in flight is refused, and once the run
// has ended inside the cooldown floor the next is refused with a retry
// duration. Precondition: a fresh Gate with a large floor; the first prep
// admits and Begins.
func TestDispatchHookRunGating(t *testing.T) {
	row := testHookRow("check")
	row.Agent = true
	g, _ := hookRunFixture(t, row, hooks.Options{Floor: time.Hour, PerRevision: 5})
	run := func() Response {
		return Dispatch(g, Request{Op: "hook", HookMode: "run", HookName: "check", Author: FirstAgent})
	}
	if first := run(); !first.OK {
		t.Fatalf("first run = %+v, want admitted", first)
	}
	if second := run(); second.OK || !strings.Contains(second.Err, "in flight") {
		t.Fatalf("overlapping run = %+v, want the in-flight refusal", second)
	}
	g.HookGate.End("check")
	third := run()
	if third.OK || !strings.Contains(third.Err, "cooldown") {
		t.Fatalf("run during cooldown = %+v, want the cooldown refusal", third)
	}
	if third.RetryAfterMS <= 0 {
		t.Errorf("cooldown RetryAfterMS = %d, want a positive retry duration", third.RetryAfterMS)
	}
}

// TestDispatchHookRunRefusesSecondaryRoot pins the multi-root admission check
// on the run path: a projection that holds a path under a workspace root other
// than the primary one is refused by name before the Gate admits the run, the
// same rule the projected exec path enforces. Without the firstOutsideRoot
// check the run would materialise the primary root only and silently omit the
// other buffer.
//
// Precondition: a real committed repository and a memHost projection holding
// one path under it and one absolute path elsewhere. The Gate is a fresh one
// so the refusal cannot be confused with admission.
func TestDispatchHookRunRefusesSecondaryRoot(t *testing.T) {
	repo := controlGitRepo(t)
	h := newMemHost(repo, nil)
	h.hooks = []HookRow{{Name: "check", Action: `["true"]`, Trigger: "agent", Agent: true, Enabled: true}}
	h.projection = map[string][]byte{
		filepath.Join(repo, "a.go"): []byte("package projected\n"),
		"/elsewhere/other.go":       []byte("package other\n"),
	}
	g := NewGuard(h)
	g.HookGate = newHookGate(hooks.NewGate(hooks.Options{}))

	res := Dispatch(g, Request{Op: "hook", HookMode: "run", HookName: "check", Author: FirstAgent})
	if res.Err == "" {
		t.Fatalf("hook run over a secondary root = %+v, want a refusal", res)
	}
	if !strings.Contains(res.Err, "/elsewhere/other.go") {
		t.Errorf("refusal = %q, want it to name /elsewhere/other.go", res.Err)
	}
	if !strings.Contains(res.Err, "multi-root projected hook runs are not supported") {
		t.Errorf("refusal = %q, want the multi-root wording", res.Err)
	}
}

// TestHookRunRunsInScratchTree drives the whole loop through the real entry
// path: a real server and socket, a real git repository, and connection.runHook
// materialising the live projection into a scratch tree, running the hook with
// that tree as its cwd, and lifting the gate afterwards.
//
// Precondition: controlGitRepo commits a.go with "package a\n" and nothing
// else; the fake host's projection replaces a.go with "package projected\n" and
// its hook cats a.go, records its cwd and exits 5. Without the run path the op
// would be an unknown hook mode; without the projection the command would read
// the committed bytes; without the deferred End the second run would be refused
// as still in flight.
func TestHookRunRunsInScratchTree(t *testing.T) {
	repo := controlGitRepo(t)
	a := filepath.Join(repo, "a.go")
	proj := map[string][]byte{a: []byte("package projected\n")}
	marker := filepath.Join(t.TempDir(), "cwd.txt")
	action, err := json.Marshal([]string{"sh", "-c", "pwd > '" + marker + "'; cat a.go; exit 5"})
	if err != nil {
		t.Fatal(err)
	}

	ed := newFakeEditor(t, nil)
	gate := newHookGate(hooks.NewGate(hooks.Options{Floor: time.Nanosecond, PerRevision: 2}))
	ed.mu.Lock()
	ed.policyMem.root = repo
	ed.policyMem.projection = proj
	ed.policyMem.hooks = []HookRow{{Name: "check", Action: string(action), Trigger: "agent", Agent: true, Enabled: true}}
	ed.policy.HookGate = gate
	ed.srv.HookGate = gate
	ed.mu.Unlock()

	c, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	run := func() (Response, string, string) {
		t.Helper()
		var out, errOut strings.Builder
		res, err := c.DoExec(Request{Op: "hook", HookMode: "run", HookName: "check"},
			func(stream uint8, b string) {
				if stream == StreamStderr {
					errOut.WriteString(b)
				} else {
					out.WriteString(b)
				}
			})
		if err != nil {
			t.Fatalf("hook run: %v", err)
		}
		return res, out.String(), errOut.String()
	}

	res, out, errOut := run()
	if res.Err != "" || !res.OK {
		t.Fatalf("hook run = %+v", res)
	}
	if res.Exit != 5 {
		t.Errorf("Exit = %d, want the hook's status 5", res.Exit)
	}
	if got := strings.TrimSpace(out); got != "package projected" {
		t.Errorf("hook saw %q, want the projected bytes", got)
	}
	if !strings.Contains(errOut, "HEAD") || !strings.Contains(errOut, "hook check") ||
		!strings.Contains(errOut, "accepted and proposed text included") {
		t.Errorf("provenance stamp = %q, want HEAD and the hook name", errOut)
	}

	raw, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read the hook's cwd: %v", err)
	}
	scratch := strings.TrimSpace(string(raw))
	if scratch == repo || !strings.Contains(filepath.Base(scratch), "raj-materialise-") {
		t.Errorf("hook cwd = %q, want a raj-materialise scratch tree", scratch)
	}

	// The registry is cleared in a defer after the final frame, so poll rather
	// than race the client's return.
	deadline := time.Now().Add(2 * time.Second)
	for ed.srv.HookRuns.Len() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the run registry never emptied after the hook finished")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// A second run starts: the first run's deferred End cleared the in-flight
	// flag, so the Gate admits again at the same revision (the per-revision cap
	// allows two).
	res2, _, _ := run()
	if res2.Err != "" || !res2.OK || res2.Exit != 5 {
		t.Fatalf("second hook run = %+v, want it admitted and run", res2)
	}
}

// TestHookRunOverlapRefused drives two overlapping runs of one hook through the
// real socket: the first command blocks (after touching a marker) while the
// second is submitted, and the shared Gate refuses it. Precondition: a hook
// whose shell touches a marker then sleeps, so the marker's existence proves
// the first run is in flight. The in-flight refusal carries no retry duration;
// the cooldown refusal does (see TestDispatchHookRunGating).
func TestHookRunOverlapRefused(t *testing.T) {
	repo := controlGitRepo(t)
	a := filepath.Join(repo, "a.go")
	started := filepath.Join(t.TempDir(), "started.txt")
	action, err := json.Marshal([]string{"sh", "-c", "touch '" + started + "'; sleep 0.5"})
	if err != nil {
		t.Fatal(err)
	}
	ed := newFakeEditor(t, nil)
	gate := newHookGate(hooks.NewGate(hooks.Options{Floor: time.Nanosecond, PerRevision: 5}))
	ed.mu.Lock()
	ed.policyMem.root = repo
	ed.policyMem.projection = map[string][]byte{a: []byte("package a\n")}
	ed.policyMem.hooks = []HookRow{{Name: "check", Action: string(action), Trigger: "agent", Agent: true, Enabled: true}}
	ed.policy.HookGate = gate
	ed.srv.HookGate = gate
	ed.mu.Unlock()

	c1, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer c1.Close()
	c2, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()

	first := make(chan error, 1)
	go func() {
		_, err := c1.DoExec(Request{Op: "hook", HookMode: "run", HookName: "check"}, nil)
		first <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the first hook run never started")
		}
		time.Sleep(10 * time.Millisecond)
	}

	res, err := c2.DoExec(Request{Op: "hook", HookMode: "run", HookName: "check"}, nil)
	if err != nil {
		t.Fatalf("overlapping run: %v", err)
	}
	if res.OK || !strings.Contains(res.Err, "in flight") {
		t.Fatalf("overlapping run = %+v, want the in-flight refusal", res)
	}
	if err := <-first; err != nil {
		t.Fatalf("first run: %v", err)
	}
}

// TestTCPAllowsAgentHookRun pins the transport rule the other way: run is not
// refused over TCP, because running a hook is what crosses the line. A
// non-agent-callable hook is refused by admission, pinned in
// TestDispatchHookRunAdmission, and authoring is still refused over TCP,
// pinned in TestTCPRefusesHookAuthoring. Here the agent-callable hook reaches
// the run path and executes. Precondition: a real git repo and a projection on
// the fake host, and a hook with Agent true. Without run removed from
// localOnly the request is refused with the transport message and never runs,
// so the exit and output assertions fail.
func TestTCPAllowsAgentHookRun(t *testing.T) {
	repo := controlGitRepo(t)
	a := filepath.Join(repo, "a.go")
	ed := newTCPEditor(t, nil)
	gate := newHookGate(hooks.NewGate(hooks.Options{Floor: time.Nanosecond, PerRevision: 2}))
	ed.mu.Lock()
	ed.policyMem.root = repo
	ed.policyMem.projection = map[string][]byte{a: []byte("package projected\n")}
	ed.policyMem.hooks = []HookRow{{Name: "check", Action: `["sh","-c","cat a.go; exit 5"]`, Trigger: "agent", Agent: true, Enabled: true}}
	ed.policy.HookGate = gate
	ed.srv.HookGate = gate
	ed.mu.Unlock()
	t.Setenv(TokenEnv, ed.srv.Token())

	c, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	var out strings.Builder
	res, err := c.DoExec(Request{Op: "hook", HookMode: "run", HookName: "check"},
		func(stream uint8, b string) {
			if stream != StreamStderr {
				out.WriteString(b)
			}
		})
	if err != nil {
		t.Fatalf("hook run over TCP: %v", err)
	}
	if strings.Contains(res.Err, "refused over TCP") {
		t.Fatalf("an agent hook run was refused by transport: %+v", res)
	}
	if res.Err != "" || !res.OK {
		t.Fatalf("hook run over TCP = %+v, want it to run", res)
	}
	if res.Exit != 5 {
		t.Errorf("Exit = %d, want the hook's status 5", res.Exit)
	}
	if got := strings.TrimSpace(out.String()); got != "package projected" {
		t.Errorf("hook saw %q, want the projected bytes", got)
	}
}

// TestResponseRetryAfterMSRoundTrips pins the new wire field: a hook run
// refusal's retry duration crosses as hRetryAfterMS and comes back intact.
func TestResponseRetryAfterMSRoundTrips(t *testing.T) {
	h, body := EncodeResponse(Response{ID: 3, Err: `hook "check": cooldown active`, RetryAfterMS: 1500})
	got, err := DecodeResponse(Frame{Header: h, Body: body})
	if err != nil {
		t.Fatal(err)
	}
	if got.RetryAfterMS != 1500 {
		t.Errorf("RetryAfterMS = %d, want 1500", got.RetryAfterMS)
	}
	if got.Err != `hook "check": cooldown active` {
		t.Errorf("Err = %q, want it intact", got.Err)
	}
}

// TestHookRunOutputCapTruncates pins the per-run output cap: a hook that writes
// more than the cap streams exactly the cap, the final frame sets
// HookTruncated, and the exit status is still the hook own. Precondition: a real
// repo, a projection, and a hook that writes 2 MiB and exits 5. Without the cap
// wrapper the full output streams and HookTruncated is false; without reading
// the flag back the final frame would not carry it.
func TestHookRunOutputCapTruncates(t *testing.T) {
	repo := controlGitRepo(t)
	a := filepath.Join(repo, "a.go")
	ed := newFakeEditor(t, nil)
	gate := newHookGate(hooks.NewGate(hooks.Options{Floor: time.Nanosecond, PerRevision: 2}))
	ed.mu.Lock()
	ed.policyMem.root = repo
	ed.policyMem.projection = map[string][]byte{a: []byte("package projected\n")}
	ed.policyMem.hooks = []HookRow{{Name: "check", Action: `["sh","-c","yes aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa | head -c 2097152; exit 5"]`, Trigger: "agent", Agent: true, Enabled: true}}
	ed.policy.HookGate = gate
	ed.srv.HookGate = gate
	ed.mu.Unlock()

	c, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	var n int
	res, err := c.DoExec(Request{Op: "hook", HookMode: "run", HookName: "check"},
		func(stream uint8, b string) {
			if stream == StreamStdout {
				n += len(b)
			}
		})
	if err != nil {
		t.Fatalf("hook run: %v", err)
	}
	if res.Err != "" || !res.OK {
		t.Fatalf("hook run = %+v", res)
	}
	if !res.HookTruncated {
		t.Errorf("HookTruncated = false, want true after the cap was exceeded")
	}
	if res.Exit != 5 {
		t.Errorf("Exit = %d, want the hook status 5 even after the output was cut", res.Exit)
	}
	if n != hookOutputCap {
		t.Errorf("streamed %d stdout bytes, want exactly the cap %d", n, hookOutputCap)
	}
}

// TestHookRunFinalStamp pins the final frame run stamp: run id, hook name,
// projection revision, HEAD, dirty digest, exit and duration. Precondition: a
// real repo, a projection that differs from HEAD, and a hook that exits 0.
// Without the stamp fields the final response carries zero values.
func TestHookRunFinalStamp(t *testing.T) {
	repo := controlGitRepo(t)
	a := filepath.Join(repo, "a.go")
	ed := newFakeEditor(t, nil)
	gate := newHookGate(hooks.NewGate(hooks.Options{Floor: time.Nanosecond, PerRevision: 2}))
	ed.mu.Lock()
	ed.policyMem.root = repo
	ed.policyMem.projection = map[string][]byte{a: []byte("package projected\n")}
	ed.policyMem.hooks = []HookRow{{Name: "check", Action: `["true"]`, Trigger: "agent", Agent: true, Enabled: true}}
	ed.policy.HookGate = gate
	ed.srv.HookGate = gate
	ed.mu.Unlock()

	c, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	res, err := c.DoExec(Request{Op: "hook", HookMode: "run", HookName: "check"}, nil)
	if err != nil {
		t.Fatalf("hook run: %v", err)
	}
	if res.Err != "" || !res.OK {
		t.Fatalf("hook run = %+v", res)
	}
	if res.HookRunID == 0 {
		t.Error("HookRunID = 0, want the run id")
	}
	if res.HookName != "check" {
		t.Errorf("HookName = %q, want check", res.HookName)
	}
	if res.HookRevision == 0 {
		t.Error("HookRevision = 0, want the projection revision")
	}
	if res.HookHead == "" {
		t.Error("HookHead is empty, want the HEAD sha")
	}
	if res.HookDirty == "" {
		t.Error("HookDirty is empty, want the dirty digest")
	}
	if res.Exit != 0 {
		t.Errorf("Exit = %d, want 0", res.Exit)
	}
	if res.HookDurationMS < 0 {
		t.Errorf("HookDurationMS = %d, want non-negative", res.HookDurationMS)
	}
	if res.HookTruncated {
		t.Error("HookTruncated = true, want false for a tiny run")
	}
}

// TestHookLogKeepsLastN pins the run log ring and that the log verb crosses
// TCP: 101 runs leave the last 100, oldest dropped. Precondition: a TCP editor
// with the token in the environment and entries added directly to the server
// log. Without the ring the log grows past the bound; without the log verb the
// request is an unknown hook mode.
func TestHookLogKeepsLastN(t *testing.T) {
	ed := newTCPEditor(t, nil)
	t.Setenv(TokenEnv, ed.srv.Token())
	c, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	const extra = 1
	for i := 1; i <= hooks.DefaultLogSize+extra; i++ {
		ed.srv.HookLog.Add(hooks.Result{ID: uint64(i), Hook: "check", Exit: i})
	}
	res, err := c.Do(Request{Op: "hook", HookMode: "log"})
	if err != nil {
		t.Fatalf("hook log: %v", err)
	}
	if !res.OK || res.Err != "" {
		t.Fatalf("hook log = %+v", res)
	}
	var rows []hooks.Result
	if err := json.Unmarshal([]byte(res.HookLogJSON), &rows); err != nil {
		t.Fatalf("HookLogJSON %q: %v", res.HookLogJSON, err)
	}
	if len(rows) != hooks.DefaultLogSize {
		t.Fatalf("log holds %d runs, want %d", len(rows), hooks.DefaultLogSize)
	}
	if rows[0].ID != 2 {
		t.Errorf("oldest logged run id = %d, want 2: the first was dropped", rows[0].ID)
	}
	if last := rows[len(rows)-1].ID; last != hooks.DefaultLogSize+extra {
		t.Errorf("newest logged run id = %d, want %d", last, hooks.DefaultLogSize+extra)
	}
}

// TestTCPRefusesHookControl pins the transport rule for the new control verbs:
// cancel and off/on are local-only and refused over TCP with the server
// message, while the ps and log reads cross. Precondition: a TCP editor whose
// fake answers hook through a Guard over a memHost. Without cancel/off/on in
// localOnly they are served over TCP and the refusal assertions fail.
func TestTCPRefusesHookControl(t *testing.T) {
	ed := newTCPEditor(t, map[string]string{"/w/a.go": "x"})
	t.Setenv(TokenEnv, ed.srv.Token())
	c, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	for _, req := range []Request{
		{Op: "hook", HookMode: "cancel", HookRunID: 1},
		{Op: "hook", HookMode: "off"},
		{Op: "hook", HookMode: "on"},
	} {
		res, err := c.Do(req)
		if err != nil {
			t.Fatalf("hook %s over TCP: %v", req.HookMode, err)
		}
		if res.Err == "" {
			t.Errorf("hook %s was allowed over TCP: %+v", req.HookMode, res)
			continue
		}
		if !strings.Contains(res.Err, "refused over TCP") {
			t.Errorf("hook %s refusal %q does not name the transport", req.HookMode, res.Err)
		}
	}

	for _, mode := range []string{"log", "ps"} {
		res, err := c.Do(Request{Op: "hook", HookMode: mode})
		if err != nil {
			t.Fatalf("hook %s over TCP: %v", mode, err)
		}
		if !res.OK || res.Err != "" {
			t.Errorf("hook %s over TCP = %+v, want a read allowed", mode, res)
		}
	}
}

// TestHookPSAndCancel drives process visibility and control end to end: a long
// run appears in hook ps with a pid and pgid, and hook cancel stops it, so the
// original run reports cancelled rather than a zero exit. Precondition: a real
// repo and a hook that touches a marker then sleeps, so the marker proves the
// run is in flight and RunReport has filled the pid. Without SetProcess the
// pid/pgid stay zero; without Get/Cancel the cancel is refused and the run
// would sleep to completion.
func TestHookPSAndCancel(t *testing.T) {
	repo := controlGitRepo(t)
	a := filepath.Join(repo, "a.go")
	started := filepath.Join(t.TempDir(), "started")
	action := `["sh","-c","touch '` + started + `'; sleep 30"]`
	ed := newFakeEditor(t, nil)
	gate := newHookGate(hooks.NewGate(hooks.Options{Floor: time.Nanosecond, PerRevision: 5}))
	ed.mu.Lock()
	ed.policyMem.root = repo
	ed.policyMem.projection = map[string][]byte{a: []byte("package a\n")}
	ed.policyMem.hooks = []HookRow{{Name: "check", Action: action, Trigger: "agent", Agent: true, Enabled: true}}
	ed.policy.HookGate = gate
	ed.srv.HookGate = gate
	ed.mu.Unlock()

	runner, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Close()
	ctl, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer ctl.Close()

	type result struct {
		res Response
		err error
	}
	done := make(chan result, 1)
	go func() {
		res, err := runner.DoExec(Request{Op: "hook", HookMode: "run", HookName: "check"}, nil)
		done <- result{res, err}
	}()

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the hook run never started")
		}
		time.Sleep(10 * time.Millisecond)
	}

	pres, err := ctl.Do(Request{Op: "hook", HookMode: "ps"})
	if err != nil {
		t.Fatalf("hook ps: %v", err)
	}
	var runs []hooks.Run
	if err := json.Unmarshal([]byte(pres.HookPSJSON), &runs); err != nil {
		t.Fatalf("HookPSJSON %q: %v", pres.HookPSJSON, err)
	}
	var run hooks.Run
	found := false
	for _, r := range runs {
		if r.Hook == "check" {
			run, found = r, true
		}
	}
	if !found {
		t.Fatalf("hook ps = %+v, want the in-flight check run", runs)
	}
	if run.PID <= 0 || run.PGID <= 0 {
		t.Errorf("run pid/pgid = %d/%d, want both positive", run.PID, run.PGID)
	}

	cres, err := ctl.Do(Request{Op: "hook", HookMode: "cancel", HookRunID: run.ID})
	if err != nil {
		t.Fatalf("hook cancel: %v", err)
	}
	if !cres.OK || cres.Err != "" {
		t.Fatalf("hook cancel = %+v, want it to stop the run", cres)
	}

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("cancelled run: %v", got.err)
		}
		if got.res.Err != "cancelled" {
			t.Errorf("cancelled run Err = %q, want cancelled", got.res.Err)
		}
		if got.res.OK {
			t.Error("cancelled run OK = true, want a refusal")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the cancelled run did not return")
	}

	// The log records how the run ended, so a cancel is not read as an exit
	// status. Without Result.Err the entry carries only the killed exit code.
	lres, err := ctl.Do(Request{Op: "hook", HookMode: "log"})
	if err != nil {
		t.Fatalf("hook log: %v", err)
	}
	var logged []hooks.Result
	if err := json.Unmarshal([]byte(lres.HookLogJSON), &logged); err != nil {
		t.Fatalf("HookLogJSON %q: %v", lres.HookLogJSON, err)
	}
	if len(logged) == 0 || logged[len(logged)-1].Err != "cancelled" {
		t.Errorf("hook log = %+v, want the last entry's err to be cancelled", logged)
	}
}

// TestHookOffRefusesRun pins the panic switch: hook off refuses every run with
// a named reason, hook list reports it, and hook on lifts it. Precondition: a
// fake editor with one enabled hook. Without the switch check the run proceeds;
// without HookOff on the list answer the state is invisible.
func TestHookOffRefusesRun(t *testing.T) {
	repo := controlGitRepo(t)
	a := filepath.Join(repo, "a.go")
	ed := newFakeEditor(t, nil)
	gate := newHookGate(hooks.NewGate(hooks.Options{Floor: time.Nanosecond, PerRevision: 5}))
	ed.mu.Lock()
	ed.policyMem.root = repo
	ed.policyMem.projection = map[string][]byte{a: []byte("package a\n")}
	ed.policyMem.hooks = []HookRow{{Name: "check", Action: `["true"]`, Trigger: "agent", Agent: true, Enabled: true}}
	ed.policy.HookGate = gate
	ed.srv.HookGate = gate
	ed.mu.Unlock()

	if _, errs, code := runHook(t, "off"); code != 0 {
		t.Fatalf("hook off exited %d: %s", code, errs)
	}
	out, _, code := runHook(t, "list")
	if code != 0 {
		t.Fatalf("hook list exited %d", code)
	}
	if !strings.Contains(out, "hooks are off") {
		t.Errorf("hook list = %q, want it to report the panic switch", out)
	}
	_, errs, code := runHook(t, "run", "check")
	if code != 2 {
		t.Fatalf("run with hooks off exited %d, want 2", code)
	}
	if !strings.Contains(errs, "hooks are off") {
		t.Errorf("refusal = %q, want the named off reason", errs)
	}

	if _, errs, code := runHook(t, "on"); code != 0 {
		t.Fatalf("hook on exited %d: %s", code, errs)
	}
	if _, errs, code := runHook(t, "run", "check"); code != 0 {
		t.Fatalf("run after on exited %d: %s", code, errs)
	}
}

// TestUnixHookRunAdmitsNonAgent pins the B3 escalation fix: a run driven over
// the Unix socket binds the local human, so a hook that is not agent-callable
// runs; a TCP agent is refused it by admission (pinned by
// TestDispatchHookRunAdmission). Precondition: a fake editor on a Unix socket
// with an Agent=false hook. Without the unix-human binding the provisional
// connection counts as an agent and admission refuses with not-callable.
func TestUnixHookRunAdmitsNonAgent(t *testing.T) {
	repo := controlGitRepo(t)
	a := filepath.Join(repo, "a.go")
	ed := newFakeEditor(t, nil)
	gate := newHookGate(hooks.NewGate(hooks.Options{Floor: time.Nanosecond, PerRevision: 2}))
	ed.mu.Lock()
	ed.policyMem.root = repo
	ed.policyMem.projection = map[string][]byte{a: []byte("package projected\n")}
	ed.policyMem.hooks = []HookRow{{Name: "check", Action: `["sh","-c","echo human-only"]`, Trigger: "agent", Agent: false, Enabled: true}}
	ed.policy.HookGate = gate
	ed.srv.HookGate = gate
	ed.mu.Unlock()

	out, errs, code := runHook(t, "run", "check")
	if code != 0 {
		t.Fatalf("unix run of a non-agent hook exited %d: %s", code, errs)
	}
	if !strings.Contains(out, "human-only") {
		t.Errorf("run stdout = %q, want the hook output", out)
	}

	// A connection that registered as an agent keeps agent admission even on
	// the Unix socket: it declared itself, so the socket does not promote it
	// to the person. Without the registry check this run is admitted.
	c, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if hi, err := c.Do(Request{Op: "hello", Identity: "raj-local-agent", Name: "local"}); err != nil || !hi.OK {
		t.Fatalf("hello: %v %+v", err, hi)
	}
	res, err := c.DoExec(Request{Op: "hook", HookMode: "run", HookName: "check"}, nil)
	if err != nil {
		t.Fatalf("registered agent run: %v", err)
	}
	if res.OK || res.Err == "" {
		t.Errorf("a registered agent on the Unix socket ran a non-agent hook: %+v", res)
	}
}

// blockingHost is a memHost whose Buffers reply reports one blocking buffer, so
// the workspace-tree readiness refusal can be driven with the fields `raj ctl
// status` reads. The embedded host still answers every other BufferHost method.
type blockingHost struct {
	*memHost
	dirty   bool
	pending int
}

func (h *blockingHost) Buffers() []Buffer {
	out := h.memHost.Buffers()
	if len(out) == 0 {
		out = append(out, Buffer{Path: h.root + "/a.go"})
	}
	out[0].Dirty = h.dirty
	out[0].Pending = h.pending
	return out
}

// TestDispatchHookRunWorkspaceReadiness pins the workspace-tree gate: a
// workspace hook is refused while any buffer is dirty or holds a pending change
// set, naming the blocker exactly as `raj ctl status` would, and admitted when
// the predicate is clean. Precondition: a Guard over a host whose Buffers reply
// can be made blocking, and one workspace hook. Without the readiness check the
// run is admitted over unsaved work.
func TestDispatchHookRunWorkspaceReadiness(t *testing.T) {
	row := testHookRow("check")
	row.Tree = "workspace"
	run := func(root string, dirty bool, pending int) Response {
		h := &blockingHost{memHost: newMemHost(root, nil), dirty: dirty, pending: pending}
		h.hooks = []HookRow{row}
		g := NewGuard(h)
		g.HookGate = newHookGate(hooks.NewGate(hooks.Options{}))
		return Dispatch(g, Request{Op: "hook", HookMode: "run", HookName: "check", Author: LocalHuman})
	}
	// A .git directory is an ordinary checkout and a .git file is a linked
	// worktree's gitdir pointer; the admission probe accepts either and never
	// spawns git.
	dirRepo := t.TempDir()
	if err := os.Mkdir(filepath.Join(dirRepo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	fileRepo := t.TempDir()
	if err := os.WriteFile(filepath.Join(fileRepo, ".git"), []byte("gitdir: /elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The readiness refusal lands before the work-tree check, so its root need
	// not be a repository.
	if res := run("/w", true, 0); res.Err == "" || !strings.Contains(res.Err, "not ready") ||
		!strings.Contains(res.Err, "dirty") || !strings.Contains(res.Err, "/w/a.go") {
		t.Fatalf("dirty workspace = %+v, want a not-ready refusal naming the dirty buffer", res)
	}
	if res := run("/w", false, 2); res.Err == "" || !strings.Contains(res.Err, "not ready") ||
		!strings.Contains(res.Err, "2 pending") {
		t.Fatalf("pending change set = %+v, want a not-ready refusal naming the pending count", res)
	}
	// A clean root that is not a git work tree is refused at admission, naming
	// the hook, rather than passing prep and failing off-thread.
	if res := run("/w", false, 0); res.Err == "" || !strings.Contains(res.Err, "not a git work tree") ||
		!strings.Contains(res.Err, "check") {
		t.Fatalf("clean non-repository = %+v, want a not-a-git-work-tree refusal naming the hook", res)
	}
	for _, root := range []string{dirRepo, fileRepo} {
		res := run(root, false, 0)
		if !res.OK || res.Err != "" {
			t.Fatalf("clean work tree %s = %+v, want the run admitted", root, res)
		}
		if res.HookTree != "workspace" {
			t.Errorf("HookTree = %q, want workspace", res.HookTree)
		}
	}
}

// TestHookRunWorkspaceUsesTheRoot drives the whole loop for a workspace hook
// through a real server and socket: it runs with the primary workspace root as
// its cwd, against the committed bytes rather than the live projection, and the
// stamp HEAD/dirty come from the worktree.
//
// Precondition: a real committed repository, a fake host whose projection
// differs from disk and whose buffers are clean, and a workspace hook that
// records its cwd and cats a.go. Without the tree branch the run materialises a
// scratch tree and sees the projected bytes.
func TestHookRunWorkspaceUsesTheRoot(t *testing.T) {
	repo := controlGitRepo(t)
	a := filepath.Join(repo, "a.go")
	proj := map[string][]byte{a: []byte("package projected\n")}
	marker := filepath.Join(t.TempDir(), "cwd.txt")
	action, err := json.Marshal([]string{"sh", "-c", "pwd > '" + marker + "'; cat a.go"})
	if err != nil {
		t.Fatal(err)
	}

	ed := newFakeEditor(t, nil)
	gate := newHookGate(hooks.NewGate(hooks.Options{Floor: time.Nanosecond, PerRevision: 2}))
	ed.mu.Lock()
	ed.policyMem.root = repo
	ed.policyMem.projection = proj
	ed.policyMem.hooks = []HookRow{{Name: "check", Action: string(action), Trigger: "agent", Tree: "workspace", Agent: true, Enabled: true}}
	ed.policy.HookGate = gate
	ed.srv.HookGate = gate
	ed.mu.Unlock()

	c, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	var out, errOut strings.Builder
	res, err := c.DoExec(Request{Op: "hook", HookMode: "run", HookName: "check"},
		func(stream uint8, b string) {
			if stream == StreamStderr {
				errOut.WriteString(b)
			} else {
				out.WriteString(b)
			}
		})
	if err != nil {
		t.Fatalf("hook run: %v", err)
	}
	if res.Err != "" || !res.OK {
		t.Fatalf("workspace hook run = %+v, want it to run", res)
	}
	if res.Exit != 0 {
		t.Errorf("Exit = %d, want 0", res.Exit)
	}
	if got := strings.TrimSpace(out.String()); got != "package a" {
		t.Errorf("hook saw %q, want the committed bytes", got)
	}
	if !strings.Contains(errOut.String(), "saved workspace root") {
		t.Errorf("provenance stamp = %q, want the saved-workspace wording", errOut.String())
	}
	if res.HookHead == "" || res.HookDirty == "" {
		t.Errorf("stamp = HEAD %q dirty %q, want both from the worktree", res.HookHead, res.HookDirty)
	}
	raw, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read the hook's cwd: %v", err)
	}
	if got := strings.TrimSpace(string(raw)); got != repo {
		t.Errorf("hook cwd = %q, want the workspace root %q", got, repo)
	}
}

// TestHookRunWorkspaceWriteGuard pins may_write enforcement for a workspace
// hook: a run that moves git status fails with a named error and the log
// records it even when the command exited zero, while the same run with
// may_write=true succeeds. A real repository and socket carry the run. Without
// the after-digest the zero-exit run is reported as a success.
func TestHookRunWorkspaceWriteGuard(t *testing.T) {
	repo := controlGitRepo(t)
	action := `["sh","-c","echo x >> b.go"]`

	run := func(mayWrite bool) (Response, []hooks.Result) {
		ed := newFakeEditor(t, nil)
		gate := newHookGate(hooks.NewGate(hooks.Options{Floor: time.Nanosecond, PerRevision: 2}))
		ed.mu.Lock()
		ed.policyMem.root = repo
		ed.policyMem.hooks = []HookRow{{Name: "check", Action: action, Trigger: "agent", Tree: "workspace", Agent: true, MayWrite: mayWrite, Enabled: true}}
		ed.policy.HookGate = gate
		ed.srv.HookGate = gate
		ed.mu.Unlock()
		c, err := Dial(ed.srv.Path())
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		res, err := c.DoExec(Request{Op: "hook", HookMode: "run", HookName: "check"}, nil)
		if err != nil {
			t.Fatalf("hook run: %v", err)
		}
		var rows []hooks.Result
		if lres, lerr := c.Do(Request{Op: "hook", HookMode: "log"}); lerr == nil && lres.Err == "" {
			if uerr := json.Unmarshal([]byte(lres.HookLogJSON), &rows); uerr != nil {
				t.Fatalf("HookLogJSON %q: %v", lres.HookLogJSON, uerr)
			}
		}
		return res, rows
	}

	res, rows := run(false)
	if res.OK || !strings.Contains(res.Err, "modified the workspace") {
		t.Fatalf("may_write=0 that wrote = %+v, want a modified-the-workspace failure", res)
	}
	if len(rows) == 0 || !strings.Contains(rows[len(rows)-1].Err, "modified the workspace") {
		t.Fatalf("hook log = %+v, want the last entry to record the write failure", rows)
	}

	if res, _ := run(true); !res.OK || res.Err != "" {
		t.Fatalf("may_write=1 that wrote = %+v, want it admitted", res)
	}
}

// TestHookRunDetachedReturnsAndLogs drives a detached run through the real
// server and socket: the reply returns before the command ends, carries the run
// id and pid, streams the log path, and the completion is recorded in the run
// log with the command's exit. Precondition: a real repository, a fake host
// whose buffers are clean, and a detached hook that writes a line, sleeps and
// exits 7. Without the detached branch the call would block for the sleep.
func TestHookRunDetachedReturnsAndLogs(t *testing.T) {
	repo := controlGitRepo(t)
	ed := newFakeEditor(t, nil)
	gate := newHookGate(hooks.NewGate(hooks.Options{Floor: time.Nanosecond, PerRevision: 5}))
	ed.mu.Lock()
	ed.policyMem.root = repo
	ed.policyMem.hooks = []HookRow{{Name: "cycle", Action: `["sh","-c","echo detached-output; sleep 1.5; exit 7"]`, Trigger: "agent", Agent: true, Detach: true, Enabled: true}}
	ed.policy.HookGate = gate
	ed.srv.HookGate = gate
	ed.mu.Unlock()
	ed.srv.SetHookDir(t.TempDir())

	c, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	var errOut strings.Builder
	start := time.Now()
	res, err := c.DoExec(Request{Op: "hook", HookMode: "run", HookName: "cycle"},
		func(stream uint8, b string) {
			if stream == StreamStderr {
				errOut.WriteString(b)
			}
		})
	if err != nil {
		t.Fatalf("detached run: %v", err)
	}
	if res.Err != "" || !res.OK {
		t.Fatalf("detached run = %+v, want it started", res)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("detached run returned after %v, want at once", elapsed)
	}
	if res.PID <= 0 {
		t.Errorf("PID = %d, want the detached process id", res.PID)
	}
	if !strings.Contains(errOut.String(), "started detached") {
		t.Errorf("stream = %q, want the started-detached line", errOut.String())
	}

	deadline := time.Now().Add(5 * time.Second)
	var logged hooks.Result
	for {
		rows := ed.srv.HookLog.List()
		if len(rows) > 0 {
			logged = rows[len(rows)-1]
			if logged.Exit != 0 || logged.Err != "" {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("the detached run's completion was never logged")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !logged.Detach || logged.Exit != 7 {
		t.Fatalf("logged result = %+v, want detach with exit 7", logged)
	}
	if logged.PID != res.PID {
		t.Errorf("logged PID = %d, want %d", logged.PID, res.PID)
	}
	raw, err := os.ReadFile(logged.LogPath)
	if err != nil {
		t.Fatalf("read the run log: %v", err)
	}
	if !strings.Contains(string(raw), "detached-output") {
		t.Errorf("run log = %q, want the command output", raw)
	}
}

// TestHookRunDetachedCancel proves a detached run's process group is reachable:
// `hook cancel` by run id kills it, the registry empties, and the completion is
// recorded. Precondition: a detached hook that sleeps, and a run directory.
func TestHookRunDetachedCancel(t *testing.T) {
	repo := controlGitRepo(t)
	ed := newFakeEditor(t, nil)
	gate := newHookGate(hooks.NewGate(hooks.Options{Floor: time.Nanosecond, PerRevision: 5}))
	ed.mu.Lock()
	ed.policyMem.root = repo
	ed.policyMem.hooks = []HookRow{{Name: "cycle", Action: `["sh","-c","sleep 30"]`, Trigger: "agent", Agent: true, Detach: true, Enabled: true}}
	ed.policy.HookGate = gate
	ed.srv.HookGate = gate
	ed.mu.Unlock()
	ed.srv.SetHookDir(t.TempDir())

	c, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	res, err := c.DoExec(Request{Op: "hook", HookMode: "run", HookName: "cycle"}, nil)
	if err != nil {
		t.Fatalf("detached run: %v", err)
	}
	if res.Err != "" || !res.OK || res.HookRunID == 0 {
		t.Fatalf("detached run = %+v, want a run id", res)
	}
	cres, err := c.Do(Request{Op: "hook", HookMode: "cancel", HookRunID: res.HookRunID})
	if err != nil || !cres.OK || cres.Err != "" {
		t.Fatalf("cancel = %+v, err %v", cres, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for ed.srv.HookRuns.Len() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the cancelled detached run stayed registered")
		}
		time.Sleep(10 * time.Millisecond)
	}
	rows := ed.srv.HookLog.List()
	if len(rows) == 0 || !rows[len(rows)-1].Detach {
		t.Fatalf("log = %+v, want the cancelled detached run recorded", rows)
	}
}

// TestRecoverHookRunsOnSetHookDir covers editor restart: an exit file becomes a
// recovered run, a pid file with no live process becomes a lost one, both rows
// carry the start stamp the pid record held -- hook, author, revision, HEAD and
// dirty, not zeros -- the registry is seeded above the files it read, and the
// consumed marker files are removed so a later start does not log them twice.
func TestRecoverHookRunsOnSetHookDir(t *testing.T) {
	dir := t.TempDir()
	write := func(name, text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("5.log", "done\n")
	write("5.exit", "3\n")
	write("5.pid", `{"pid":12345,"hook":"check","author":7,"revision":42,"head":"abc123","dirty":"M a.go"}`)
	write("9.log", "gone\n")
	write("9.pid", `{"pid":999999,"hook":"cycle","author":9,"revision":51,"head":"def456"}`)

	srv := &Server{HookRuns: hooks.NewRegistry(), HookLog: hooks.NewLog(0)}
	srv.SetHookDir(dir)

	rows := srv.HookLog.List()
	var recovered, lost *hooks.Result
	for i := range rows {
		switch rows[i].ID {
		case 5:
			recovered = &rows[i]
		case 9:
			lost = &rows[i]
		}
	}
	if recovered == nil || !recovered.Recovered || recovered.Exit != 3 {
		t.Fatalf("recovered run = %+v, want id 5 exit 3", recovered)
	}
	if recovered.Hook != "check" || recovered.Author != 7 || recovered.Revision != 42 ||
		recovered.Head != "abc123" || recovered.Dirty != "M a.go" {
		t.Errorf("recovered stamp = %+v, want the start stamp the pid file recorded", recovered)
	}
	if lost == nil || !lost.Lost {
		t.Fatalf("lost run = %+v, want id 9 marked lost", lost)
	}
	if lost.Hook != "cycle" || lost.Author != 9 || lost.Revision != 51 || lost.Head != "def456" {
		t.Errorf("lost stamp = %+v, want the start stamp the pid file recorded", lost)
	}
	if _, err := os.Stat(filepath.Join(dir, "5.exit")); !os.IsNotExist(err) {
		t.Errorf("the consumed exit file survived: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "9.pid")); !os.IsNotExist(err) {
		t.Errorf("the consumed pid file survived: %v", err)
	}
	if id := srv.HookRuns.Add(hooks.Run{Hook: "new"}); id <= 9 {
		t.Errorf("next run id = %d, want above the recovered 9", id)
	}
}

// TestPruneHookRunsKeepsLive pins the prune rule: a finished run (exit file) and
// a lost run (dead pid, no exit) with low ids are dropped, but a live run is
// never pruned whatever its id, because its wrapper still needs the file paths.
func TestPruneHookRunsKeepsLive(t *testing.T) {
	dir := t.TempDir()
	write := func(name, text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Id 1 is this test process: alive, so not prunable.
	write("1.pid", `{"pid":`+strconv.Itoa(os.Getpid())+`,"hook":"live"}`)
	write("1.log", "live\n")
	// Id 2 finished; id 3 is gone with no exit.
	write("2.log", "done\n")
	write("2.exit", "0\n")
	write("3.log", "gone\n")
	write("3.pid", `{"pid":999999}`)

	pruneHookRunDir(dir, 500) // keepFrom = 401, so all three are candidates
	for _, name := range []string{"1.pid", "1.log"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("the live run's %s was pruned: %v", name, err)
		}
	}
	for _, name := range []string{"2.log", "2.exit", "3.log", "3.pid"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("prunable %s survived: %v", name, err)
		}
	}
}

// TestRecoverHookRunsAdoptsLive covers the restart that matters for a detached
// cycle: a run still alive when a new editor opens the same directory is
// re-registered under its id so `hook ps` and `cancel` reach it, and its exit is
// logged when the wrapper writes it.
func TestRecoverHookRunsAdoptsLive(t *testing.T) {
	dir := t.TempDir()
	exitPath := filepath.Join(dir, "7.exit")
	cmd := exec.Command("/bin/sh", "-c", "sleep 0.3; printf '0\\n' > '"+exitPath+"'")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go cmd.Wait()
	record := `{"pid":` + strconv.Itoa(cmd.Process.Pid) +
		`,"start":` + strconv.FormatInt(time.Now().UnixNano(), 10) +
		`,"hook":"cycle","author":7,"revision":42,"head":"abc123","dirty":"M a.go"}`
	if err := os.WriteFile(filepath.Join(dir, "7.pid"), []byte(record), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "7.log"), []byte("out\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	old := hookRunWatchPoll
	hookRunWatchPoll = 20 * time.Millisecond
	defer func() { hookRunWatchPoll = old }()

	srv := &Server{HookRuns: hooks.NewRegistry(), HookLog: hooks.NewLog(0)}
	srv.SetHookDir(dir)

	run, ok := srv.HookRuns.Get(7)
	if !ok || run.PID != cmd.Process.Pid || run.Hook != "cycle" {
		t.Fatalf("adopted run = %+v, ok %v; want id 7 pid %d hook cycle", run, ok, cmd.Process.Pid)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		rows := srv.HookLog.List()
		if len(rows) > 0 {
			got := rows[len(rows)-1]
			if got.ID != 7 || got.Exit != 0 || !got.Detach || !got.Recovered {
				t.Fatalf("completion = %+v, want id 7 exit 0 detached recovered", got)
			}
			if got.Author != 7 || got.Revision != 42 || got.Head != "abc123" || got.Dirty != "M a.go" {
				t.Errorf("completion stamp = %+v, want the pid record start stamp", got)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the re-adopted run's completion was never logged")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(dir, "7.pid")); !os.IsNotExist(err) {
		t.Errorf("the pid record survived completion: %v", err)
	}
}

// TestHookRunDetachedTimeout pins the detached timeout: the watcher kills the
// run's process group at the hook's deadline and records the run as timed out.
// Precondition: a detached hook that sleeps far past a 200ms timeout.
func TestHookRunDetachedTimeout(t *testing.T) {
	repo := controlGitRepo(t)
	ed := newFakeEditor(t, nil)
	gate := newHookGate(hooks.NewGate(hooks.Options{Floor: time.Nanosecond, PerRevision: 5}))
	ed.mu.Lock()
	ed.policyMem.root = repo
	ed.policyMem.hooks = []HookRow{{Name: "cycle", Action: `["sh","-c","sleep 30"]`, Trigger: "agent", Agent: true, Detach: true, TimeoutMS: 200, Enabled: true}}
	ed.policy.HookGate = gate
	ed.srv.HookGate = gate
	ed.mu.Unlock()
	ed.srv.SetHookDir(t.TempDir())

	c, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	res, err := c.DoExec(Request{Op: "hook", HookMode: "run", HookName: "cycle"}, nil)
	if err != nil || res.Err != "" || !res.OK {
		t.Fatalf("detached run = %+v, err %v", res, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		rows := ed.srv.HookLog.List()
		if len(rows) > 0 {
			got := rows[len(rows)-1]
			if !strings.Contains(got.Err, "timed out") {
				t.Fatalf("completion = %+v, want a timed-out error", got)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the timed-out run was never recorded")
		}
		time.Sleep(10 * time.Millisecond)
	}
	deadline = time.Now().Add(5 * time.Second)
	for ed.srv.HookRuns.Len() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the timed-out run stayed registered")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
