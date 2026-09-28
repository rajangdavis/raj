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
