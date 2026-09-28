package intent

import (
	"context"
	"fmt"

	"raj/internal/git"
)

// Projection is the content an intention contributes, keyed by
// workspace-relative path. A nil value removes the path from the base tree, so
// a deletion is expressible without a second argument.
type Projection map[string][]byte

// Select resolves the member groups of in through find and returns only their
// buffer content from proj. A member the finder does not know is the D2 hard
// error; a member whose buffer is absent from proj contributes nothing, since
// the base tree already carries it.
func (in Intention) Select(proj Projection, find func(Member) (Member, bool)) (Projection, error) {
	members, err := in.ResolveMembers(find)
	if err != nil {
		return nil, err
	}
	out := make(Projection, len(members))
	for _, m := range members {
		data, ok := proj[m.Path]
		if !ok {
			continue
		}
		out[m.Path] = data
	}
	return out, nil
}

// Materialise writes a tree for base with proj overlaid and returns its id. It
// delegates to git's object plumbing (a temporary index, blob and tree
// objects) and touches neither the worktree nor the user's index. Unsaved
// proposals in proj are included by construction. The same base and proj always
// give the same tree id (D3).
func Materialise(ctx context.Context, svc *git.Service, base string, proj Projection) (string, error) {
	return svc.TreeFromProjection(ctx, base, git.Projection(proj))
}

// Drift reports a warning when in's pinned base ref has moved past BaseSHA.
// The pinned SHA is kept either way (D3): a moved base changes nothing about
// what this intention was created over, it is only worth saying so. An
// intention whose base is another intention, or whose BaseSHA is empty, does
// not drift.
func Drift(ctx context.Context, svc *git.Service, in Intention) (string, error) {
	if in.BaseSHA == "" {
		return "", nil
	}
	now, err := svc.RevParse(ctx, in.Base)
	if err != nil {
		return "", err
	}
	if now == in.BaseSHA {
		return "", nil
	}
	return fmt.Sprintf("base %s has moved from pinned %s to %s; keeping %s",
		in.Base, in.BaseSHA, now, in.BaseSHA), nil
}
