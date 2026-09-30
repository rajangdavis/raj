// Package builtin holds the process-local registry of in-process hook actions.
//
// An external hook names a command the editor runs; a builtin hook names a Go
// value the editor already carries, so it needs no process, no shell and no
// PATH. The registry maps the name a stored action declares to the leaf that
// runs it, so internal/hooks can resolve the name at parse time -- a stored
// row with an unknown builtin is refused when it is validated, not when it is
// later run -- and the runner can find the same leaf by the name it admitted.
//
// A leaf also declares its agent policy here and never decides it for itself:
// the registry records the Policy the registration supplies, internal/hooks
// carries it on the validated action, and Admit refuses an agent caller a
// HumanOnly leaf.
//
// It imports only the standard library. The leaves live in packages that
// register here at init time, and internal/hooks imports this package to
// validate against it; neither has to know what any leaf does. Registration
// happens at init, so a binary that calls hooks.Parse must link every package
// whose leaves it expects to resolve: a leaf package the build does not import
// makes its names unknown to Parse, exactly as if they had never registered.
package builtin

import (
	"context"
	"fmt"
	"sync"
)

// Result is what a leaf returns. Output is the text a caller captures exactly
// where an external hook's stdout would arrive; Exit is the status the caller
// reads as the action's exit code. A non-nil error from Run marks the run
// failed the way an exec failure does; Exit is then a best-effort status.
type Result struct {
	Output string
	Exit   int
}

// Policy is a leaf's agent policy, declared at registration. A leaf never
// chooses it for itself; the registration supplies it and Admit enforces it
// against the caller.
type Policy uint8

const (
	// AgentDefault is a leaf an agent may run when the hook's own agent flag
	// allows it. It is the zero value, so the common read leaves register
	// without ceremony.
	AgentDefault Policy = iota
	// HumanOnly is a leaf an agent may never run, whatever the hook's agent
	// flag says: an outward action such as a ref update belongs to the human.
	HumanOnly
)

// Leaf is one in-process hook action. Name is the identifier a stored action
// declares and must equal the name it is registered under; Run executes it
// with the run's context and the action's args and returns the result.
type Leaf interface {
	Name() string
	Run(ctx context.Context, args map[string]any) (Result, error)
}

// registration is one registered leaf and the policy it was declared with.
type registration struct {
	leaf   Leaf
	policy Policy
}

var (
	mu     sync.RWMutex
	leaves = map[string]registration{}
)

// Register adds leaf under name with the agent policy policy. A duplicate name
// is refused: one name has one implementation, so a later registration cannot
// silently shadow an earlier one. A leaf whose Name does not match name is
// refused too: the registry key is the authority, and a mismatch would let one
// name invoke another leaf. Registration happens at init time and is safe for
// concurrent use.
func Register(name string, leaf Leaf, policy Policy) error {
	if name == "" {
		return fmt.Errorf("builtin name must not be empty")
	}
	if leaf == nil {
		return fmt.Errorf("builtin %q: leaf must not be nil", name)
	}
	if got := leaf.Name(); got != name {
		return fmt.Errorf("builtin %q: leaf reports name %q", name, got)
	}
	if policy != AgentDefault && policy != HumanOnly {
		return fmt.Errorf("builtin %q: unknown policy %d", name, uint8(policy))
	}
	mu.Lock()
	defer mu.Unlock()
	if _, dup := leaves[name]; dup {
		return fmt.Errorf("builtin %q is already registered", name)
	}
	leaves[name] = registration{leaf: leaf, policy: policy}
	return nil
}

// Lookup returns the leaf registered under name, its agent policy, and whether
// there is one.
func Lookup(name string) (Leaf, Policy, bool) {
	mu.RLock()
	defer mu.RUnlock()
	reg, ok := leaves[name]
	return reg.leaf, reg.policy, ok
}

// WithRoot returns ctx carrying the workspace root the run was admitted with.
// The dispatcher stamps it before running a leaf, so a leaf resolves the
// workspace it acts on rather than the process working directory. It is a
// run-scoped seam any leaf may read, and the registry does not know what a leaf
// does with it.
func WithRoot(ctx context.Context, root string) context.Context {
	return context.WithValue(ctx, rootKey{}, root)
}

// Root returns the workspace root the run was admitted with, and whether there
// is one. A leaf reads it to resolve the workspace it was admitted for; where a
// leaf also accepts an explicit root arg, that arg still wins.
func Root(ctx context.Context) (string, bool) {
	root, ok := ctx.Value(rootKey{}).(string)
	return root, ok && root != ""
}

// rootKey is the context key WithRoot stores under. It is unexported so only
// this package writes it.
type rootKey struct{}

// WithStepEnv returns ctx carrying the composite chain environment a run
// builds: the RAJ_STEP_<name>_OUT and RAJ_STEP_<name>_OUT_FILE entries of the
// steps that already ran. A shell step receives them as its process
// environment; a builtin step reads them with StepEnv, because a leaf has no
// process to inherit them. The entries are the "KEY=value" strings os/exec
// takes, so a caller can pass the same slice to either kind of step.
func WithStepEnv(ctx context.Context, env []string) context.Context {
	return context.WithValue(ctx, stepEnvKey{}, env)
}

// StepEnv returns the chain environment WithStepEnv stamped, or nil when the
// action is not a composite.
func StepEnv(ctx context.Context) []string {
	env, _ := ctx.Value(stepEnvKey{}).([]string)
	return env
}

// WithParamEnv returns ctx carrying a run's resolved RAJ_PARAM_<name> entries:
// the parameters a hook declares, with the values the caller supplied or the
// declared defaults. A shell action receives them as its process environment;
// a builtin leaf reads them with ParamEnv, because a leaf is in-process and
// has no environment of its own. The entries are the "KEY=value" strings
// os/exec takes, so the same slice feeds either kind of action.
func WithParamEnv(ctx context.Context, env []string) context.Context {
	return context.WithValue(ctx, paramEnvKey{}, env)
}

// ParamEnv returns the RAJ_PARAM_<name> entries WithParamEnv stamped, or nil
// when the run declared no parameters. It is the in-process channel a builtin
// leaf reads its run parameters from.
func ParamEnv(ctx context.Context) []string {
	env, _ := ctx.Value(paramEnvKey{}).([]string)
	return env
}

// paramEnvKey is the context key WithParamEnv stores under. It is unexported
// so only this package writes it.
type paramEnvKey struct{}

// stepEnvKey is the context key WithStepEnv stores under. It is unexported so
// only this package writes it.
type stepEnvKey struct{}
