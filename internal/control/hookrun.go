package control

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"raj/internal/hooks"
	"raj/internal/hooks/builtin"
)

// hookGate serialises access to the workspace's one hooks.Gate.
//
// The run policy lives in the hooks package; what it does not carry is a lock,
// and its callers straddle two threads. Admission -- Allow then Begin -- runs
// on the event thread inside dispatchHook, while End runs on the connection
// goroutine that just finished the command. A Gate touched from both without a
// lock is a data race on its in-flight flag, so this wrapper is the one place
// every call goes through and the mutex is the seam. There is still exactly one
// Gate per editor; the mutex does not make it per-connection.
type hookGate struct {
	mu   sync.Mutex
	gate *hooks.Gate
}

// newHookGate wraps a Gate. The Server owns the wrapper and every connection
// shares it, so a second `raj hook run` sees the first run's in-flight flag.
func newHookGate(g *hooks.Gate) *hookGate { return &hookGate{gate: g} }

// Allow asks the Gate whether hook may run at revision now. The return shape is
// the Gate's: a retry duration for a cooldown refusal, false and a reason when
// it refuses.
func (h *hookGate) Allow(hook string, revision uint64, now time.Time) (time.Duration, bool, string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.gate.Allow(hook, revision, now)
}

// Begin records that admission just allowed a run of hook at revision.
func (h *hookGate) Begin(hook string, revision uint64, now time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.gate.Begin(hook, revision, now)
}

// End clears hook's in-flight flag once its run has finished, whatever the
// run's outcome.
func (h *hookGate) End(hook string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.gate.End(hook)
}

// runBuiltinLeaf runs a registered builtin action in-process and streams its
// output through the same relay an external hook's stdout uses. It is the
// builtin half of the one hook-run path, and its caller has already passed the
// same Admit and Gate admission an external hook does: a builtin leaf cannot
// bypass the agent flag, the trigger, the policy, the cooldown or the in-flight
// coalescing, and a builtin action is never detached.
//
// Run receives the run's context, so the hook timeout and `hook cancel` reach a
// leaf exactly as the process-group kill reaches an external command. A panic
// is recovered here and returned as the run's error: without it a panicking
// leaf would kill the connection goroutine and no reply would ever be sent.
func runBuiltinLeaf(ctx context.Context, name string, args map[string]any, stream func(uint8, []byte)) (code int, err error) {
	defer func() {
		if r := recover(); r != nil {
			code, err = 0, fmt.Errorf("builtin %q panicked: %v", name, r)
		}
	}()
	leaf, _, ok := builtin.Lookup(name)
	if !ok {
		return 0, fmt.Errorf("builtin %q is not registered", name)
	}
	res, runErr := leaf.Run(ctx, args)
	if res.Output != "" {
		stream(StreamStdout, []byte(res.Output))
	}
	return res.Exit, runErr
}

// stepOutCap is how many bytes of one step's output are kept in the chain
// environment before the output spills to a file. The environment is not a
// place for a megabyte, and a downstream step can still read the file; the cap
// is well above any output a summary step should produce.
const stepOutCap = 64 << 10

// stepsNeedTree reports whether a composite has a shell step. A builtin step
// runs in-process with no working directory, so a chain of leaves needs no
// scratch or workspace tree; a shell step does, exactly as an external hook.
func stepsNeedTree(steps []hooks.Step) bool {
	for _, s := range steps {
		if s.Shell != "" {
			return true
		}
	}
	return false
}

// chainError is a composite step's failure: the step that failed and its
// stderr, or its output when a leaf failed without a separate stderr stream.
// The run's final frame reports both, so the caller can name the failed seam.
type chainError struct {
	Step   string
	Stderr string
}

func (e *chainError) Error() string {
	stderr := strings.TrimSpace(e.Stderr)
	if stderr == "" {
		return fmt.Sprintf("failed step %q", e.Step)
	}
	return fmt.Sprintf("failed step %q: %s", e.Step, stderr)
}

// runChain runs a composite's steps in order, chaining each step's output into
// the environment of the next. A builtin step reads the chain environment off
// its context; a shell step receives it as its process environment. The first
// nonzero step stops the chain and is returned as a chainError naming the step
// and its stderr; a step whose output exceeds stepOutCap spills to a temp file
// in a per-run directory and exports the path instead of the text. The
// directory is removed when the chain ends, so a spill is a within-chain
// channel, not a durable artifact.
func runChain(ctx context.Context, steps []hooks.Step, root, runDir string, stream func(uint8, []byte)) (int, error) {
	dir, err := os.MkdirTemp("", "raj-steps-")
	if err != nil {
		return 1, fmt.Errorf("composite: %w", err)
	}
	defer os.RemoveAll(dir)

	var env []string
	for _, step := range steps {
		var out, errOut bytes.Buffer
		code, rerr := runChainStep(ctx, step, root, runDir, env, stream, &out, &errOut)
		if rerr != nil || code != 0 {
			stderr := errOut.String()
			if stderr == "" {
				stderr = out.String()
			}
			if stderr == "" && rerr != nil {
				stderr = rerr.Error()
			}
			return code, &chainError{Step: step.Name, Stderr: stderr}
		}
		env = append(env, chainStepEnv(dir, step, out.String())...)
	}
	return 0, nil
}

// chainStepEnv is the environment one finished step contributes: its output, or
// the path of the temp file it spilled to when the output is over the cap. A
// spill that cannot be written leaves the step with an empty OUT and no
// OUT_FILE rather than failing a step that already succeeded.
func chainStepEnv(dir string, step hooks.Step, out string) []string {
	if len(out) > stepOutCap {
		path := filepath.Join(dir, hooks.NormaliseStepName(step.Name)+".out")
		if err := os.WriteFile(path, []byte(out), 0o600); err != nil {
			return []string{hooks.StepOutVar(step.Name) + "="}
		}
		return []string{hooks.StepOutVar(step.Name) + "=", hooks.StepOutFileVar(step.Name) + "=" + path}
	}
	return []string{hooks.StepOutVar(step.Name) + "=" + out}
}

// runChainStep runs one step, forwarding its output to the run's stream and
// capturing it for the chain. A shell step runs in runDir with the chain
// environment appended; a builtin step gets the root and the chain environment
// on its context.
func runChainStep(ctx context.Context, step hooks.Step, root, runDir string, env []string, stream func(uint8, []byte), out, errOut *bytes.Buffer) (int, error) {
	capture := func(streamID uint8, b []byte) {
		if streamID == StreamStderr {
			errOut.Write(b)
		} else {
			out.Write(b)
		}
		if stream != nil {
			stream(streamID, b)
		}
	}
	if step.Shell != "" {
		// The resolved parameters lead the chain environment: a shell step
		// receives RAJ_PARAM_<name> beside the RAJ_STEP_<name>_OUT entries, and
		// the builtin branch below inherits them on the context.
		stepEnv := append(append([]string{}, builtin.ParamEnv(ctx)...), env...)
		return runReportEnv(ctx, []string{"/bin/sh", "-c", step.Shell}, runDir, stepEnv, nil, capture)
	}
	stepCtx := builtin.WithRoot(ctx, root)
	stepCtx = builtin.WithStepEnv(stepCtx, env)
	return runBuiltinLeaf(stepCtx, step.Builtin, step.Args, capture)
}
