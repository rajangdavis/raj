package control

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"raj/internal/hooks"
	"raj/internal/hooks/builtin"
	_ "raj/internal/hooks/builtin/gitleaves"
)

// controlTestLeaf is the in-process action the builtin dispatch tests register.
// Its output names the arg it was called with, so a test can prove the leaf
// actually ran and that its text arrived on the run's stdout stream.
type controlTestLeaf struct{ name string }

func (l controlTestLeaf) Name() string { return l.name }

func (l controlTestLeaf) Run(ctx context.Context, args map[string]any) (builtin.Result, error) {
	return builtin.Result{Output: fmt.Sprintf("leaf %s ran: %v\n", l.name, args["who"])}, nil
}

// controlTestPanicLeaf panics from Run, so a test can prove the dispatch
// recovers a leaf rather than letting the panic reach the connection goroutine.
type controlTestPanicLeaf struct{ name string }

func (l controlTestPanicLeaf) Name() string { return l.name }

func (l controlTestPanicLeaf) Run(ctx context.Context, args map[string]any) (builtin.Result, error) {
	panic("controltest.panic boom")
}

// controlTestBlockLeaf blocks until its context ends, so a test can prove Run
// receives the run context the hook timeout cancels.
type controlTestBlockLeaf struct{ name string }

func (l controlTestBlockLeaf) Name() string { return l.name }

func (l controlTestBlockLeaf) Run(ctx context.Context, args map[string]any) (builtin.Result, error) {
	<-ctx.Done()
	return builtin.Result{}, ctx.Err()
}

// controlTestRootLeaf reports the workspace root the dispatcher stamped on the
// run context, so a test can prove a leaf resolves the admitted workspace
// rather than the process working directory.
type controlTestRootLeaf struct{ name string }

func (l controlTestRootLeaf) Name() string { return l.name }

func (l controlTestRootLeaf) Run(ctx context.Context, args map[string]any) (builtin.Result, error) {
	root, ok := builtin.Root(ctx)
	if !ok {
		return builtin.Result{Output: "no run root\n", Exit: 1}, nil
	}
	return builtin.Result{Output: root + "\n"}, nil
}

// controlTestEnvLeaf reports one chain-environment entry by name, so a chain
// test can prove a builtin step saw an earlier step's output.
type controlTestEnvLeaf struct{ name string }

func (l controlTestEnvLeaf) Name() string { return l.name }

func (l controlTestEnvLeaf) Run(ctx context.Context, args map[string]any) (builtin.Result, error) {
	want, _ := args["var"].(string)
	for _, kv := range builtin.StepEnv(ctx) {
		if k, v, ok := strings.Cut(kv, "="); ok && k == want {
			return builtin.Result{Output: v}, nil
		}
	}
	return builtin.Result{Output: "no chain var " + want + "\n", Exit: 1}, nil
}

// controlTestFailLeaf fails with output, so a chain test can prove the first
// nonzero step stops the chain and is the failure the run names.
type controlTestFailLeaf struct{ name string }

func (l controlTestFailLeaf) Name() string { return l.name }

func (l controlTestFailLeaf) Run(ctx context.Context, args map[string]any) (builtin.Result, error) {
	return builtin.Result{Output: "boom stderr\n", Exit: 1}, nil
}

// controlTestCatLeaf reads the file a chain exported by path-variable name and
// reports its length, so a spill test can prove the whole output reached disk.
type controlTestCatLeaf struct{ name string }

func (l controlTestCatLeaf) Name() string { return l.name }

func (l controlTestCatLeaf) Run(ctx context.Context, args map[string]any) (builtin.Result, error) {
	want, _ := args["path_var"].(string)
	for _, kv := range builtin.StepEnv(ctx) {
		if k, v, ok := strings.Cut(kv, "="); ok && k == want && v != "" {
			data, err := os.ReadFile(v)
			if err != nil {
				return builtin.Result{Output: err.Error() + "\n", Exit: 1}, nil
			}
			return builtin.Result{Output: fmt.Sprintf("len=%d", len(data))}, nil
		}
	}
	return builtin.Result{Output: "no path in " + want + "\n", Exit: 1}, nil
}

// controlTestBigLeaf emits more than the step output cap, so a chain test can
// prove the output spills to a file rather than the environment.
type controlTestBigLeaf struct{ name string }

func (l controlTestBigLeaf) Name() string { return l.name }

func (l controlTestBigLeaf) Run(ctx context.Context, args map[string]any) (builtin.Result, error) {
	return builtin.Result{Output: strings.Repeat("x", stepOutCap+1024)}, nil
}

// controlTestParamLeaf reports one resolved run-parameter entry by its
// environment name, so a builtin test can prove a leaf reads the run's
// RAJ_PARAM_<NAME> entries from its context the way a shell action reads its
// process environment.
type controlTestParamLeaf struct{ name string }

func (l controlTestParamLeaf) Name() string { return l.name }

func (l controlTestParamLeaf) Run(ctx context.Context, args map[string]any) (builtin.Result, error) {
	want, _ := args["var"].(string)
	for _, kv := range builtin.ParamEnv(ctx) {
		if k, v, ok := strings.Cut(kv, "="); ok && k == want {
			return builtin.Result{Output: v}, nil
		}
	}
	return builtin.Result{Output: "no param " + want + "\n", Exit: 1}, nil
}

func init() {
	if err := builtin.Register("controltest.echo", controlTestLeaf{name: "controltest.echo"}, builtin.AgentDefault); err != nil {
		panic(err)
	}
	if err := builtin.Register("controltest.panic", controlTestPanicLeaf{name: "controltest.panic"}, builtin.AgentDefault); err != nil {
		panic(err)
	}
	if err := builtin.Register("controltest.block", controlTestBlockLeaf{name: "controltest.block"}, builtin.AgentDefault); err != nil {
		panic(err)
	}
	if err := builtin.Register("controltest.root", controlTestRootLeaf{name: "controltest.root"}, builtin.AgentDefault); err != nil {
		panic(err)
	}
	if err := builtin.Register("controltest.env", controlTestEnvLeaf{name: "controltest.env"}, builtin.AgentDefault); err != nil {
		panic(err)
	}
	if err := builtin.Register("controltest.fail", controlTestFailLeaf{name: "controltest.fail"}, builtin.AgentDefault); err != nil {
		panic(err)
	}
	if err := builtin.Register("controltest.cat", controlTestCatLeaf{name: "controltest.cat"}, builtin.AgentDefault); err != nil {
		panic(err)
	}
	if err := builtin.Register("controltest.big", controlTestBigLeaf{name: "controltest.big"}, builtin.AgentDefault); err != nil {
		panic(err)
	}
	if err := builtin.Register("controltest.param", controlTestParamLeaf{name: "controltest.param"}, builtin.AgentDefault); err != nil {
		panic(err)
	}
}

// TestDispatchHookBuiltinGating pins that a builtin action is gated by the same
// shared Gate as an external hook: the second run while the first is in flight
// is refused, and the next inside the cooldown floor is refused with a retry.
func TestDispatchHookBuiltinGating(t *testing.T) {
	row := testHookRow("leaf")
	row.Agent = true
	row.Action = `{"builtin":"controltest.echo"}`
	g, _ := hookRunFixture(t, row, hooks.Options{Floor: time.Hour, PerRevision: 5})
	run := func() Response {
		return Dispatch(g, Request{Op: "hook", HookMode: "run", HookName: "leaf", Author: FirstAgent})
	}
	first := run()
	if !first.OK || first.Err != "" {
		t.Fatalf("first builtin run = %+v, want admitted", first)
	}
	if first.HookBuiltin != "controltest.echo" {
		t.Errorf("HookBuiltin = %q, want the admitted leaf carried through the prep", first.HookBuiltin)
	}
	if second := run(); second.OK || !strings.Contains(second.Err, "in flight") {
		t.Fatalf("overlapping builtin run = %+v, want the in-flight refusal", second)
	}
	g.HookGate.End("leaf")
	if third := run(); third.OK || !strings.Contains(third.Err, "cooldown") {
		t.Fatalf("builtin run during cooldown = %+v, want the cooldown refusal", third)
	}
}

// TestHookRunBuiltin drives a builtin action through the real socket and entry
// path: connection.runHook sees the action kind before the exec path, runs the
// leaf in-process, and streams its output exactly where an external command's
// stdout would go.
//
// Precondition: a real repository and a hook whose action is a registered
// builtin. Without the builtin branch the empty argv would reach Run and fail;
// without the stream relay the caller would see no output.
func TestHookRunBuiltin(t *testing.T) {
	repo := controlGitRepo(t)
	ed := newFakeEditor(t, nil)
	gate := newHookGate(hooks.NewGate(hooks.Options{Floor: time.Nanosecond, PerRevision: 2}))
	ed.mu.Lock()
	ed.policyMem.root = repo
	ed.policyMem.projection = map[string][]byte{}
	ed.policyMem.hooks = []HookRow{{Name: "leaf", Action: `{"builtin":"controltest.echo","args":{"who":"builder"}}`, Trigger: "agent", Agent: true, Enabled: true}}
	ed.policy.HookGate = gate
	ed.srv.HookGate = gate
	ed.mu.Unlock()

	c, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	var out strings.Builder
	res, err := c.DoExec(Request{Op: "hook", HookMode: "run", HookName: "leaf"},
		func(stream uint8, b string) {
			if stream != StreamStderr {
				out.WriteString(b)
			}
		})
	if err != nil {
		t.Fatalf("builtin hook run: %v", err)
	}
	if res.Err != "" || !res.OK {
		t.Fatalf("builtin hook run = %+v, want it to run", res)
	}
	if res.Exit != 0 {
		t.Errorf("Exit = %d, want 0", res.Exit)
	}
	if got := strings.TrimSpace(out.String()); got != "leaf controltest.echo ran: builder" {
		t.Errorf("stdout = %q, want the leaf's output where the command's would stream", got)
	}
}

// TestHookRunComposite drives a composite action through the real socket and
// entry path: dispatchHook carries the parsed steps through the prep, runHook
// sees them before the single-builtin and exec branches, and each step's output
// is chained to the next. Precondition: controltest.echo and controltest.env.
func TestHookRunComposite(t *testing.T) {
	repo := controlGitRepo(t)
	ed := newFakeEditor(t, nil)
	gate := newHookGate(hooks.NewGate(hooks.Options{Floor: time.Nanosecond, PerRevision: 2}))
	ed.mu.Lock()
	ed.policyMem.root = repo
	ed.policyMem.projection = map[string][]byte{}
	action := `{"steps":[{"name":"first","builtin":"controltest.echo","args":{"who":"one"}},` +
		`{"name":"second","builtin":"controltest.env","args":{"var":"RAJ_STEP_FIRST_OUT"}}]}`
	ed.policyMem.hooks = []HookRow{{Name: "chain", Action: action, Trigger: "agent", Agent: true, Enabled: true}}
	ed.policy.HookGate = gate
	ed.srv.HookGate = gate
	ed.mu.Unlock()

	c, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	var out strings.Builder
	res, err := c.DoExec(Request{Op: "hook", HookMode: "run", HookName: "chain"},
		func(stream uint8, b string) {
			if stream != StreamStderr {
				out.WriteString(b)
			}
		})
	if err != nil {
		t.Fatalf("composite hook run: %v", err)
	}
	if res.Err != "" || !res.OK {
		t.Fatalf("composite hook run = %+v, want it to run", res)
	}
	if got := strings.Count(out.String(), "ran: one"); got != 2 {
		t.Errorf("composite stdout = %q; want the first step's line and the env step's echo", out.String())
	}
}

// TestRunBuiltinLeafPanics pins the recover in the builtin dispatch: a leaf
// that panics becomes the run's error instead of escaping onto the connection
// goroutine, which would crash the editor.
//
// Precondition: controltest.panic registered. Without the recover the panic
// unwinds the test itself, so the assertion never runs.
func TestRunBuiltinLeafPanics(t *testing.T) {
	code, err := runBuiltinLeaf(context.Background(), "controltest.panic", nil, func(uint8, []byte) {})
	if err == nil {
		t.Fatalf("runBuiltinLeaf on a panicking leaf returned no error")
	}
	if !strings.Contains(err.Error(), "panicked") {
		t.Fatalf("runBuiltinLeaf error = %q; want it to name the panic", err)
	}
	if code != 0 {
		t.Fatalf("runBuiltinLeaf code = %d; want 0", code)
	}
}

// TestHookRunBuiltinTimeout proves the run context reaches a leaf: one that
// blocks until the context ends is stopped by the hook timeout, and the run
// reports a timeout rather than hanging the connection.
//
// Precondition: controltest.block registered and a 50 ms hook timeout. Without
// ctx in Run the leaf would block forever and the test would time out.
func TestHookRunBuiltinTimeout(t *testing.T) {
	repo := controlGitRepo(t)
	ed := newFakeEditor(t, nil)
	gate := newHookGate(hooks.NewGate(hooks.Options{Floor: time.Nanosecond, PerRevision: 2}))
	ed.mu.Lock()
	ed.policyMem.root = repo
	ed.policyMem.projection = map[string][]byte{}
	ed.policyMem.hooks = []HookRow{{Name: "block", Action: `{"builtin":"controltest.block"}`, Trigger: "agent", Agent: true, TimeoutMS: 50, Enabled: true}}
	ed.policy.HookGate = gate
	ed.srv.HookGate = gate
	ed.mu.Unlock()

	c, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	res, err := c.DoExec(Request{Op: "hook", HookMode: "run", HookName: "block"},
		func(stream uint8, b string) {})
	if err != nil {
		t.Fatalf("blocking builtin hook run: %v", err)
	}
	if res.OK || !strings.Contains(res.Err, "timed out") {
		t.Fatalf("blocking builtin run = %+v, want a timeout refusal", res)
	}
}

// TestHookRunBuiltinUsesWorkspaceRoot drives a builtin leaf through the real
// socket and entry path and pins that the dispatcher stamps the workspace root
// the run was admitted with onto the run context, so the leaf resolves
// prep.Root rather than falling back to the process working directory.
//
// Precondition: a real repository whose root differs from the test binary's cwd,
// and a hook whose action is controltest.root, which reports the root it read
// off the run context. Without the wiring the leaf would see os.Getwd().
func TestHookRunBuiltinUsesWorkspaceRoot(t *testing.T) {
	repo := controlGitRepo(t)
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if repo == wd {
		t.Fatalf("fixture root %q equals the process cwd; the test could not tell prep.Root from the fallback", repo)
	}
	ed := newFakeEditor(t, nil)
	gate := newHookGate(hooks.NewGate(hooks.Options{Floor: time.Nanosecond, PerRevision: 2}))
	ed.mu.Lock()
	ed.policyMem.root = repo
	ed.policyMem.projection = map[string][]byte{}
	ed.policyMem.hooks = []HookRow{{Name: "leaf", Action: `{"builtin":"controltest.root"}`, Trigger: "agent", Agent: true, Enabled: true}}
	ed.policy.HookGate = gate
	ed.srv.HookGate = gate
	ed.mu.Unlock()

	c, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	var out strings.Builder
	res, err := c.DoExec(Request{Op: "hook", HookMode: "run", HookName: "leaf"},
		func(stream uint8, b string) {
			if stream != StreamStderr {
				out.WriteString(b)
			}
		})
	if err != nil {
		t.Fatalf("builtin hook run: %v", err)
	}
	if res.Err != "" || !res.OK {
		t.Fatalf("builtin hook run = %+v, want it to run", res)
	}
	if got := strings.TrimSpace(out.String()); got != repo {
		t.Errorf("leaf root = %q, want the admitted workspace root %q (process cwd is %q)", got, repo, wd)
	}
}

// TestCompositeChainsOut drives the step chain directly: a step's output is
// exported as RAJ_STEP_<NAME>_OUT and the next step reads it off its context.
// Precondition: controltest.echo and controltest.env registered. Without the
// chain environment the env step finds no variable, exits 1 and the chain
// fails, so the doubled output assertion is the proof it was threaded.
func TestCompositeChainsOut(t *testing.T) {
	h, err := hooks.Parse(hooks.Raw{Name: "chain", Trigger: "agent", Enabled: true,
		Action: `{"steps":[
			{"name":"first","builtin":"controltest.echo","args":{"who":"one"}},
			{"name":"second","builtin":"controltest.env","args":{"var":"RAJ_STEP_FIRST_OUT"}}]}`})
	if err != nil {
		t.Fatalf("Parse(chain): %v", err)
	}
	var out strings.Builder
	code, rerr := runChain(context.Background(), h.Steps, "/w", t.TempDir(),
		func(_ uint8, b []byte) { out.Write(b) })
	if rerr != nil || code != 0 {
		t.Fatalf("runChain = exit %d, err %v; want success", code, rerr)
	}
	if got := strings.Count(out.String(), "ran: one"); got != 2 {
		t.Fatalf("output = %q; want the first step's line streamed and then re-emitted by the env step", out.String())
	}
}

// TestCompositeFailFast pins the fail-fast contract: the first nonzero step
// stops the chain, the run names the failed step and its stderr, and no later
// step runs. Precondition: controltest.fail exits 1 with "boom stderr".
func TestCompositeFailFast(t *testing.T) {
	h, err := hooks.Parse(hooks.Raw{Name: "chain", Trigger: "agent", Enabled: true,
		Action: `{"steps":[
			{"name":"first","builtin":"controltest.echo","args":{"who":"one"}},
			{"name":"second","builtin":"controltest.fail"},
			{"name":"third","builtin":"controltest.env","args":{"var":"RAJ_STEP_FIRST_OUT"}}]}`})
	if err != nil {
		t.Fatalf("Parse(chain): %v", err)
	}
	var out strings.Builder
	code, rerr := runChain(context.Background(), h.Steps, "/w", t.TempDir(),
		func(_ uint8, b []byte) { out.Write(b) })
	if code != 1 || rerr == nil {
		t.Fatalf("runChain = exit %d, err %v; want the failure of the second step", code, rerr)
	}
	if !strings.Contains(rerr.Error(), `"second"`) || !strings.Contains(rerr.Error(), "boom stderr") {
		t.Fatalf("failure = %q; want it to name step second and its stderr", rerr.Error())
	}
	if got := strings.Count(out.String(), "ran: one"); got != 1 {
		t.Fatalf("output = %q; want the chain stopped, so the third step never re-emits the first", out.String())
	}
}

// TestLargeOutSpills pins the 64 KiB spill: a step whose output is over the cap
// exports RAJ_STEP_<NAME>_OUT_FILE pointing at a temp file holding the whole
// output, and the text variable is not the channel. Precondition:
// controltest.big emits stepOutCap+1024 bytes; controltest.cat reads the path
// variable back. Without the spill the env entry would carry the whole output
// and the file would be absent.
func TestLargeOutSpills(t *testing.T) {
	const want = stepOutCap + 1024
	h, err := hooks.Parse(hooks.Raw{Name: "big", Trigger: "agent", Enabled: true,
		Action: `{"steps":[
			{"name":"big","builtin":"controltest.big"},
			{"name":"cat","builtin":"controltest.cat","args":{"path_var":"RAJ_STEP_BIG_OUT_FILE"}},
			{"name":"out","builtin":"controltest.env","args":{"var":"RAJ_STEP_BIG_OUT"}}]}`})
	if err != nil {
		t.Fatalf("Parse(big): %v", err)
	}
	var out strings.Builder
	code, rerr := runChain(context.Background(), h.Steps, "/w", t.TempDir(),
		func(_ uint8, b []byte) { out.Write(b) })
	if rerr != nil || code != 0 {
		t.Fatalf("runChain = exit %d, err %v; want success", code, rerr)
	}
	if !strings.Contains(out.String(), fmt.Sprintf("len=%d", want)) {
		t.Fatalf("spilled file was not the full output: %q", out.String())
	}
}

// TestCompositeGitContext runs the first shipped composite over a real
// repository: git.status, git.diff and git.log chain in order, and the empty
// diff is a success rather than a failure that stops the chain.
func TestCompositeGitContext(t *testing.T) {
	repo := controlGitRepo(t)
	h, err := hooks.Parse(hooks.Raw{Name: "git-context", Trigger: "agent", Enabled: true,
		Action: `{"steps":[
			{"name":"status","builtin":"git.status"},
			{"name":"diff","builtin":"git.diff"},
			{"name":"log","builtin":"git.log","args":{"count":20}}]}`})
	if err != nil {
		t.Fatalf("Parse(git-context): %v", err)
	}
	var out strings.Builder
	code, rerr := runChain(context.Background(), h.Steps, repo, repo,
		func(_ uint8, b []byte) { out.Write(b) })
	if rerr != nil || code != 0 {
		t.Fatalf("git-context = exit %d, err %v; want success on an empty diff", code, rerr)
	}
	got := out.String()
	if !strings.Contains(got, `"root"`) {
		t.Errorf("git-context output = %q; want the git.status JSON", got)
	}
	if !strings.Contains(got, `"sha"`) {
		t.Errorf("git-context output = %q; want the git.log JSON", got)
	}
}

// TestHookRunDeliversParamEnvToBuiltin proves the in-process channel: a builtin
// leaf reads the resolved parameter from builtin.ParamEnv, the same way a shell
// action reads it from its process environment, and a declared default fills an
// omitted value.
func TestHookRunDeliversParamEnvToBuiltin(t *testing.T) {
	row := HookRow{Name: "leaf", Action: `{"builtin":"controltest.param","args":{"var":"RAJ_PARAM_PHASE"}}`,
		Trigger: "agent", Enabled: true, Params: `["PHASE=enum(check,race)=check"]`}
	res, out, _ := hookRunWithParams(t, row, nil)
	if res.Err != "" || !res.OK {
		t.Fatalf("builtin run = %+v, want it to run", res)
	}
	if got := strings.TrimSpace(out); got != "check" {
		t.Fatalf("builtin saw %q, want the declared default check", got)
	}
	res, out, _ = hookRunWithParams(t, row, []string{"PHASE=race"})
	if res.Err != "" || !res.OK {
		t.Fatalf("builtin run = %+v, want it to run", res)
	}
	if got := strings.TrimSpace(out); got != "race" {
		t.Fatalf("builtin saw %q, want the explicit race", got)
	}
}
