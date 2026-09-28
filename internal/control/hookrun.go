package control

import (
	"context"
	"fmt"
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
