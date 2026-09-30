package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"raj/internal/git"
	"raj/internal/hooks"
	"raj/internal/intent"
)

// intentProve proves one named intention's seam: materialise it alone over its
// base (its members only, no other uncommitted change) and run the workspace's
// check hook on that tree. It is read-only -- nothing publishes and nothing
// writes an intention. A failing proof reports the hook's output tail and the
// user fixes the selection; there is no graph to consult.
func (a *App) intentProve(ctx context.Context, svc *git.Service, set intent.Set, cmd intent.Command) (intent.Result, error) {
	materialise := func(in intent.Intention) (string, func(), error) {
		m, err := a.materialiseIntention(ctx, svc, set, in)
		if err != nil {
			return "", nil, err
		}
		return m.dir, m.Remove, nil
	}
	check := func(dir string) (int, string, error) {
		return a.runCheckHook(ctx, dir)
	}
	proof, err := intent.ProveNamed(set, cmd.Name, materialise, check)
	if err != nil {
		return intent.Result{}, err
	}
	return intent.Result{Proofs: []intent.Proof{proof}}, nil
}

// materialisedIntention is one intention materialised alone over its base: the
// scratch directory holding the checkout, the tree object it was built from,
// and the commit the tree sits on. Remove deletes the scratch directory.
type materialisedIntention struct {
	dir  string
	tree string
	base string
}

// Remove deletes the scratch directory. It is safe on a nil receiver and on a
// value whose directory was never created, so a caller may defer it freely.
func (m *materialisedIntention) Remove() {
	if m != nil && m.dir != "" {
		_ = os.RemoveAll(m.dir)
	}
}

// materialiseIntention materialises in alone over its base: its member groups
// only, no other uncommitted change, into a fresh scratch directory. It is the
// one materialiser `intent prove` and `intent diff` share -- prove wraps it as
// an intent.Materialiser and runs a hook on the directory, diff reads the tree
// against the base. It returns the scratch directory, its tree object and the
// base commit; every failure removes any directory it created, and on success
// the caller owns it through Remove.
func (a *App) materialiseIntention(ctx context.Context, svc *git.Service, set intent.Set, in intent.Intention) (*materialisedIntention, error) {
	base, err := a.baseCommit(ctx, svc, in)
	if err != nil {
		return nil, err
	}
	proj, err := a.intentProjection(in, set)
	if err != nil {
		return nil, err
	}
	tree, err := intent.Materialise(ctx, svc, base, proj)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "raj-seam-")
	if err != nil {
		return nil, err
	}
	if err := svc.MaterialiseTree(ctx, tree, dir); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	return &materialisedIntention{dir: dir, tree: tree, base: base}, nil
}

// runCheckHook reads the workspace's check hook and runs its argv in dir. It is
// the real hook, so a proof and a gate run the same command.
func (a *App) runCheckHook(ctx context.Context, dir string) (int, string, error) {
	row, ok, err := a.state.Hook("check")
	if err != nil {
		return 0, "", err
	}
	if !ok {
		return 0, "", errors.New("intent prove: no check hook is registered")
	}
	h, err := hooks.Parse(hooks.Raw{
		Name: row.Name, Action: row.Action, Trigger: row.Trigger, Tree: row.Tree,
		Agent: row.Agent, CooldownMS: row.CooldownMS, TimeoutMS: row.TimeoutMS,
		MayWrite: row.MayWrite, Detach: row.Detach, Enabled: row.Enabled,
	})
	if err != nil {
		return 0, "", fmt.Errorf("intent prove: check hook: %w", err)
	}
	if len(h.Argv) == 0 {
		return 0, "", errors.New("intent prove: the check hook has no argv to run")
	}
	cmd := exec.CommandContext(ctx, h.Argv[0], h.Argv[1:]...)
	cmd.Dir = dir
	var buf strings.Builder
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err = cmd.Run()
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), buf.String(), nil
	}
	if err != nil {
		return 0, buf.String(), err
	}
	return 0, buf.String(), nil
}
