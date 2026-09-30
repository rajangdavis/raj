package control

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"raj/internal/hooks"
)

// runHook runs HookCLI against the fake editor and returns its streams and exit
// code.
func runHook(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	var out, errb strings.Builder
	code = HookCLI(args, &out, &errb)
	return out.String(), errb.String(), code
}

// TestHookCLIAddArgv pins the argv grammar: everything after "--" is the hook's
// argv, the request is a put carrying the row as JSON, and the defaults are
// trigger agent, agent false and enabled true. Precondition: the fake editor
// answers the hook op through Dispatch over a memHost. Without the add parser
// the argv would land in the positionals and no argv would be stored.
func TestHookCLIAddArgv(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "x\n"})
	out, errs, code := runHook(t, "add", "check", "--", "go", "test", "./...")
	if code != 0 {
		t.Fatalf("add exited %d: %s", code, errs)
	}
	if !strings.Contains(out, "added check") {
		t.Errorf("add stdout = %q, want an added line", out)
	}
	if ed.lastHook.Op != "hook" || ed.lastHook.HookMode != "put" {
		t.Fatalf("request = %+v, want a hook put", ed.lastHook)
	}
	var row HookRow
	if err := json.Unmarshal([]byte(ed.lastHook.HookJSON), &row); err != nil {
		t.Fatalf("HookJSON %q: %v", ed.lastHook.HookJSON, err)
	}
	if row.Name != "check" || row.Action != `["go","test","./..."]` {
		t.Errorf("row = %+v, want name check and the argv action", row)
	}
	if row.Trigger != "agent" || row.Tree != "projected" || row.Agent || !row.Enabled || row.CooldownMS != 0 || row.TimeoutMS != 0 || row.MayWrite {
		t.Errorf("row defaults = %+v, want trigger agent, agent false, enabled true, zero durations", row)
	}
}

// TestHookCLIAddAction pins the --action form: the raw JSON reaches the put
// verbatim, and a composite action stored this way parses back as its steps.
// This is the only CLI path to a builtin or composite action, so without it a
// composite could only be authored by hand-editing the store.
// Precondition: the fake editor answers the hook op through Dispatch, which
// validates the row with the hooks domain before storing it.
func TestHookCLIAddAction(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "x\n"})
	const action = `{"steps":[{"name":"status","builtin":"git.status"},{"name":"diff","builtin":"git.diff"},{"name":"log","builtin":"git.log","args":{"count":20}}]}`
	out, errs, code := runHook(t, "add", "git-context", "--agent", "--action", action)
	if code != 0 {
		t.Fatalf("add --action exited %d: %s", code, errs)
	}
	if !strings.Contains(out, "added git-context") {
		t.Errorf("add stdout = %q, want an added line", out)
	}
	var row HookRow
	if err := json.Unmarshal([]byte(ed.lastHook.HookJSON), &row); err != nil {
		t.Fatalf("HookJSON %q: %v", ed.lastHook.HookJSON, err)
	}
	if row.Name != "git-context" || row.Action != action || !row.Agent {
		t.Errorf("row = %+v, want git-context, the raw steps action and agent true", row)
	}
	h, err := hooks.Parse(hooks.Raw{Name: row.Name, Action: row.Action, Trigger: row.Trigger, Enabled: row.Enabled})
	if err != nil {
		t.Fatalf("stored action does not parse as a composite: %v", err)
	}
	if len(h.Steps) != 3 || h.Steps[0].Builtin != "git.status" || h.Steps[2].Builtin != "git.log" {
		t.Errorf("steps = %+v, want git.status, git.diff, git.log", h.Steps)
	}
}

// TestHookCLIAddShellAndFlags pins the --shell form and every add flag.
// Precondition: the same fake. Without the flag wiring the row's fields stay
// at their defaults and the assertions fail.
func TestHookCLIAddShellAndFlags(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "x\n"})
	_, errs, code := runHook(t, "add", "check", "--shell", "echo hi",
		"--agent", "--cooldown-ms", "2000", "--timeout-ms", "30000", "--may-write", "--tree", "workspace", "--detach", "--disabled")
	if code != 0 {
		t.Fatalf("add --shell exited %d: %s", code, errs)
	}
	var row HookRow
	if err := json.Unmarshal([]byte(ed.lastHook.HookJSON), &row); err != nil {
		t.Fatalf("HookJSON %q: %v", ed.lastHook.HookJSON, err)
	}
	if row.Action != `{"shell":"echo hi"}` {
		t.Errorf("action = %q, want the shell object", row.Action)
	}
	if !row.Agent || row.CooldownMS != 2000 || row.TimeoutMS != 30000 || row.Tree != "workspace" || !row.MayWrite || !row.Detach || row.Enabled {
		t.Errorf("row = %+v, want the flag values", row)
	}
}

// TestHookCLIAddRefusals pins the local grammar refusals: no action, both
// forms at once, and a missing name. None of them sends a request.
func TestHookCLIAddRefusals(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "x\n"})
	if _, errs, code := runHook(t, "add", "check"); code != 2 || !strings.Contains(errs, "needs an argv") {
		t.Errorf("add with no action: code %d err %q", code, errs)
	}
	if _, errs, code := runHook(t, "add", "check", "--shell", "s", "--", "x"); code != 2 || !strings.Contains(errs, "mutually exclusive") {
		t.Errorf("add with both forms: code %d err %q", code, errs)
	}
	if _, errs, code := runHook(t, "add", "check", "--action", `{"shell":"x"}`, "--shell", "s"); code != 2 || !strings.Contains(errs, "mutually exclusive") {
		t.Errorf("add with --action and --shell: code %d err %q", code, errs)
	}
	if _, _, code := runHook(t, "add"); code != 2 {
		t.Errorf("add with no name exited %d, want 2", code)
	}
	if ed.lastHook.Op != "" {
		t.Errorf("a refused add still sent a request: %+v", ed.lastHook)
	}
}

// TestHookCLIList pins list's request and both output forms, including the JSON
// passthrough. Precondition: a canned hook in the fake's memHost.
func TestHookCLIList(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "x\n"})
	ed.policyMem.hooks = []HookRow{{Name: "check", Action: `["true"]`, Trigger: "agent", Enabled: true}}

	out, errs, code := runHook(t, "list")
	if code != 0 {
		t.Fatalf("list exited %d: %s", code, errs)
	}
	if !strings.Contains(out, "check") || !strings.Contains(out, "enabled") {
		t.Errorf("list = %q, want the hook and its state", out)
	}
	if ed.lastHook.HookMode != "list" {
		t.Errorf("request mode = %q, want list", ed.lastHook.HookMode)
	}

	out, _, code = runHook(t, "list", "-json")
	if code != 0 || !strings.Contains(out, `"name":"check"`) {
		t.Errorf("list -json = %q, want the row JSON", out)
	}
}

// TestHookCLIShow pins show's request shape and its hit and miss. The miss is
// the server's refusal printed verbatim, with no local prefix.
func TestHookCLIShow(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "x\n"})
	ed.policyMem.hooks = []HookRow{{Name: "check", Action: `["true"]`, Trigger: "agent", Enabled: true}}

	out, errs, code := runHook(t, "show", "check")
	if code != 0 {
		t.Fatalf("show exited %d: %s", code, errs)
	}
	if !strings.Contains(out, "check") {
		t.Errorf("show = %q, want the hook", out)
	}
	if ed.lastHook.HookMode != "show" || ed.lastHook.HookName != "check" {
		t.Errorf("request = %+v, want show check", ed.lastHook)
	}

	_, errs, code = runHook(t, "show", "absent")
	if code == 0 {
		t.Fatal("show absent exited 0")
	}
	if got := strings.TrimSpace(errs); got != `no such hook "absent"` {
		t.Errorf("refusal = %q, want the server's message verbatim", got)
	}
	if strings.Contains(errs, "raj hook:") {
		t.Errorf("refusal %q carries a local prefix", errs)
	}
}

// TestHookCLIRmEnableDisable pins the three named authoring modes and that each
// mutates the fake's stored row.
func TestHookCLIRmEnableDisable(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "x\n"})
	ed.policyMem.hooks = []HookRow{{Name: "check", Action: `["true"]`, Trigger: "agent", Enabled: true}}

	if _, errs, code := runHook(t, "disable", "check"); code != 0 {
		t.Fatalf("disable exited %d: %s", code, errs)
	}
	if ed.lastHook.HookMode != "disable" || ed.lastHook.HookName != "check" {
		t.Errorf("disable request = %+v", ed.lastHook)
	}
	if ed.policyMem.hooks[0].Enabled {
		t.Error("disable did not flip the stored row")
	}

	if _, errs, code := runHook(t, "enable", "check"); code != 0 {
		t.Fatalf("enable exited %d: %s", code, errs)
	}
	if !ed.policyMem.hooks[0].Enabled {
		t.Error("enable did not flip the stored row")
	}

	if _, errs, code := runHook(t, "rm", "check"); code != 0 {
		t.Fatalf("rm exited %d: %s", code, errs)
	}
	if ed.lastHook.HookMode != "rm" {
		t.Errorf("rm request = %+v", ed.lastHook)
	}
	if len(ed.policyMem.hooks) != 0 {
		t.Errorf("rm left rows: %+v", ed.policyMem.hooks)
	}
}

// TestHookCLIUsage pins the no-args and unknown-subcommand exits, so a typo
// shows the grammar instead of dialling.
func TestHookCLIUsage(t *testing.T) {
	_, errs, code := runHook(t)
	if code != 2 || !strings.Contains(errs, "usage: raj hook") {
		t.Errorf("no args: code %d err %q", code, errs)
	}
	_, errs, code = runHook(t, "bogus")
	if code != 2 || !strings.Contains(errs, "unknown command") {
		t.Errorf("bogus: code %d err %q", code, errs)
	}
}

// TestHookCLIRefusalIsVerbatimOverTCP pins the reporting rule end to end: an
// authoring call against a TCP editor is refused by the server, and the CLI
// prints that refusal with no local prefix. Precondition: a TCP fake and the
// token in the environment. Without verbatim printing the message would carry
// a "raj hook:" prefix and the exact-match assertion would fail.
func TestHookCLIRefusalIsVerbatimOverTCP(t *testing.T) {
	ed := newTCPEditor(t, map[string]string{"/w/a.go": "x\n"})
	t.Setenv(TokenEnv, ed.srv.Token())
	_, errs, code := runHook(t, "add", "check", "--", "true")
	if code == 0 {
		t.Fatal("a TCP authoring add exited 0")
	}
	if got := strings.TrimSpace(errs); got != errRemoteHook {
		t.Errorf("refusal = %q, want %q", got, errRemoteHook)
	}
}

// TestHookCLIRunStreamsAndLandsModeRun pins `raj hook run`: the request lands
// as the hook op with mode run, the command's output streams to stdout, the
// exit status is the hook's own, and the fake server saw mode run.
//
// Precondition: a real repository and a projection on the fake host, so the
// run can materialise, and a hook with Agent true because the CLI dials as an
// agent. Without connection.runHook the op would be an unknown hook mode.
func TestHookCLIRunStreamsAndLandsModeRun(t *testing.T) {
	repo := controlGitRepo(t)
	a := filepath.Join(repo, "a.go")
	ed := newFakeEditor(t, nil)
	gate := newHookGate(hooks.NewGate(hooks.Options{Floor: time.Nanosecond, PerRevision: 2}))
	ed.mu.Lock()
	ed.policyMem.root = repo
	ed.policyMem.projection = map[string][]byte{a: []byte("package projected\n")}
	ed.policyMem.hooks = []HookRow{{Name: "check", Action: `["sh","-c","echo from-hook"]`, Trigger: "agent", Agent: true, Enabled: true}}
	ed.policy.HookGate = gate
	ed.srv.HookGate = gate
	ed.mu.Unlock()

	out, errs, code := runHook(t, "run", "check")
	if code != 0 {
		t.Fatalf("run exited %d: %s", code, errs)
	}
	if !strings.Contains(out, "from-hook") {
		t.Errorf("run stdout = %q, want the hook's output", out)
	}
	ed.mu.Lock()
	got := ed.lastHook
	ed.mu.Unlock()
	if got.Op != "hook" || got.HookMode != "run" || got.HookName != "check" {
		t.Errorf("request = %+v, want hook mode run for check", got)
	}
}

// TestHookCLIRunRefusalExitsTwo pins that a refusal is not the hook's status: an
// unknown hook exits 2 with the server's message, and a missing name is refused
// locally before dialling.
func TestHookCLIRunRefusalExitsTwo(t *testing.T) {
	newFakeEditor(t, map[string]string{"/w/a.go": "x\n"})
	_, errs, code := runHook(t, "run", "absent")
	if code != 2 {
		t.Fatalf("run absent exited %d, want 2", code)
	}
	if !strings.Contains(errs, "no such hook") {
		t.Errorf("refusal = %q, want the server's message", errs)
	}
	if _, _, code := runHook(t, "run"); code != 2 {
		t.Errorf("run with no name exited %d, want 2", code)
	}
}

// TestHookCLILogPSAndCancel drives the new CLI verbs against the real server:
// log renders the stored run and passes the server JSON through under -json, ps
// is an empty read when nothing is in flight, and cancel refuses a missing,
// malformed or unknown run id. Precondition: the fake editor Server carries one
// canned log entry. Without the log/ps/cancel subcommands HookCLI treats them
// as unknown commands and exits 2.
func TestHookCLILogPSAndCancel(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "x\n"})
	ed.srv.HookLog.Add(hooks.Result{ID: 7, Hook: "check", Revision: 3, Head: "abc", Exit: 0})

	out, errs, code := runHook(t, "log")
	if code != 0 {
		t.Fatalf("log exited %d: %s", code, errs)
	}
	if !strings.Contains(out, "7\tcheck") || !strings.Contains(out, "revision=3") {
		t.Errorf("log = %q, want the logged run id, hook and revision", out)
	}
	out, _, code = runHook(t, "log", "-json")
	if code != 0 || !strings.Contains(out, `"id":7`) {
		t.Errorf("log -json = %q, want the server JSON", out)
	}

	out, errs, code = runHook(t, "ps")
	if code != 0 {
		t.Fatalf("ps exited %d: %s", code, errs)
	}
	if strings.TrimSpace(out) != "" {
		t.Errorf("ps with nothing in flight = %q, want an empty read", out)
	}

	if _, errs, code := runHook(t, "cancel"); code != 2 || !strings.Contains(errs, "needs a run id") {
		t.Errorf("cancel with no id: code %d err %q", code, errs)
	}
	if _, errs, code := runHook(t, "cancel", "abc"); code != 2 || !strings.Contains(errs, "not a run id") {
		t.Errorf("cancel with a bad id: code %d err %q", code, errs)
	}
	if _, errs, code := runHook(t, "cancel", "99"); code != 1 || !strings.Contains(errs, "no in-flight hook run 99") {
		t.Errorf("cancel an unknown id: code %d err %q", code, errs)
	}
}

// TestHookCLILogShowTail pins `hook log --show`: the run id reaches the wire,
// the server returns the run's log tail, and --tail keeps only the last N
// lines. Precondition: a real server with a hook run directory holding one log
// file. Without the show branch the request is the ordinary log read and the
// output is the empty run list.
func TestHookCLILogShowTail(t *testing.T) {
	ed := newFakeEditor(t, nil)
	dir := t.TempDir()
	ed.srv.SetHookDir(dir)
	if err := os.WriteFile(filepath.Join(dir, "7.log"), []byte("a\nb\nc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, errs, code := runHook(t, "log", "--show", "7", "--tail", "2")
	if code != 0 {
		t.Fatalf("log --show exited %d: %s", code, errs)
	}
	if strings.Contains(out, "a\n") || !strings.Contains(out, "b\n") || !strings.Contains(out, "c\n") {
		t.Errorf("log --show = %q, want the last two lines", out)
	}
	if _, errs, code := runHook(t, "log", "--show", "abc"); code != 2 || !strings.Contains(errs, "not a run id") {
		t.Errorf("log --show bad id: code %d err %q", code, errs)
	}
}

// TestHookCLIRunAsBindsIdentity pins --as on `raj hook`: the CLI says hello
// with the identity before running, so the run is admitted and logged as that
// participant rather than as a provisional id (the live H1 verification saw
// every TCP run logged as author 2). Precondition: a fake editor with one
// agent-callable hook. Without the bind the log's author is not the
// registered id.
func TestHookCLIRunAsBindsIdentity(t *testing.T) {
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

	if _, errs, code := runHook(t, "run", "check", "--as", "raj-cli-agent"); code != 0 {
		t.Fatalf("run --as exited %d: %s", code, errs)
	}
	var want uint8
	for _, p := range ed.srv.Participants.List() {
		if p.Identity == "raj-cli-agent" {
			want = p.ID
		}
	}
	if want == 0 {
		t.Fatal("--as did not register the identity")
	}
	logged := ed.srv.HookLog.List()
	if len(logged) == 0 || logged[len(logged)-1].Author != want {
		t.Errorf("hook log = %+v, want the run attributed to author %d", logged, want)
	}
}

// TestHookCLIRunDetachedPrintsStartOnly pins what a detached start prints: the
// started line streams with the run id, pid and log path, and no completion
// stamp follows it, because the run has not ended -- exit and duration belong
// to the completion `raj hook log` records later. Precondition: a fake editor
// with a detached, agent-callable hook and a run directory. Without the
// detached branch hookRun prints "exit 0 duration 0ms", a completion this
// request never observed.
func TestHookCLIRunDetachedPrintsStartOnly(t *testing.T) {
	repo := controlGitRepo(t)
	ed := newFakeEditor(t, nil)
	gate := newHookGate(hooks.NewGate(hooks.Options{Floor: time.Nanosecond, PerRevision: 5}))
	ed.mu.Lock()
	ed.policyMem.root = repo
	ed.policyMem.hooks = []HookRow{{Name: "cycle", Action: `["true"]`, Trigger: "agent", Agent: true, Detach: true, Enabled: true}}
	ed.policy.HookGate = gate
	ed.srv.HookGate = gate
	ed.mu.Unlock()
	ed.srv.SetHookDir(t.TempDir())

	_, errs, code := runHook(t, "run", "cycle")
	if code != 0 {
		t.Fatalf("detached run exited %d: %s", code, errs)
	}
	if !strings.Contains(errs, "started detached") {
		t.Fatalf("stderr = %q, want the started-detached line", errs)
	}
	if strings.Contains(errs, "duration") || strings.Contains(errs, "exit ") {
		t.Errorf("stderr = %q, want no completion stamp for a detached start", errs)
	}

	// The detached child is still running when the CLI returns, writing its
	// log and exit files into the hook dir. Wait until the run is logged —
	// the server logs only after the child has exited and its markers are
	// gone — so t.TempDir's cleanup does not race the child ("directory not
	// empty", seen under -race in a cycle run, 2026-09-26).
	deadline := time.Now().Add(10 * time.Second)
	for len(ed.srv.HookLog.List()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the detached run was never logged as finished")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestHookCLIParamAuthoringAndRun pins the two ends of declared parameters: the
// repeatable --param flag reaches the stored row as one JSON array, and
// `raj hook run name NAME=value` carries the assignments in the request the
// server validates before the action starts.
func TestHookCLIParamAuthoringAndRun(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "x\n"})
	_, errs, code := runHook(t, "add", "cycle", "--shell", "true",
		"--param", "PHASE=enum(check,race)=check", "--param", "N=uint")
	if code != 0 {
		t.Fatalf("add --param exited %d: %s", code, errs)
	}
	var row HookRow
	if err := json.Unmarshal([]byte(ed.lastHook.HookJSON), &row); err != nil {
		t.Fatalf("HookJSON %q: %v", ed.lastHook.HookJSON, err)
	}
	if row.Params != `["PHASE=enum(check,race)=check","N=uint"]` {
		t.Fatalf("row.Params = %q; want the two declarations in order", row.Params)
	}
	if _, perr := hooks.Parse(hooks.Raw{Name: row.Name, Action: row.Action, Params: row.Params,
		Trigger: row.Trigger, Enabled: row.Enabled}); perr != nil {
		t.Fatalf("stored declarations do not parse: %v", perr)
	}

	// run NAME=value: the assignment crosses to the request.
	repo := controlGitRepo(t)
	a := filepath.Join(repo, "a.go")
	ed2 := newFakeEditor(t, nil)
	gate := newHookGate(hooks.NewGate(hooks.Options{Floor: time.Nanosecond, PerRevision: 5}))
	ed2.mu.Lock()
	ed2.policyMem.root = repo
	ed2.policyMem.projection = map[string][]byte{a: []byte("package projected\n")}
	ed2.policyMem.hooks = []HookRow{{Name: "cycle", Action: `["true"]`, Trigger: "agent", Agent: true,
		Enabled: true, Params: `["PHASE=enum(check,race)=check"]`}}
	ed2.policy.HookGate = gate
	ed2.srv.HookGate = gate
	ed2.mu.Unlock()

	if _, errs, code := runHook(t, "run", "cycle", "PHASE=race"); code != 0 {
		t.Fatalf("run with a param exited %d: %s", code, errs)
	}
	ed2.mu.Lock()
	got := ed2.lastHook
	ed2.mu.Unlock()
	if got.HookMode != "run" || got.HookName != "cycle" {
		t.Fatalf("request = %+v, want hook run cycle", got)
	}
	if len(got.HookParams) != 1 || got.HookParams[0] != "PHASE=race" {
		t.Fatalf("HookParams = %q; want [PHASE=race]", got.HookParams)
	}
}
