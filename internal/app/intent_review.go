package app

import (
	"context"
	"fmt"

	"raj/internal/git"
	"raj/internal/intent"
)

// intentReview starts the review walk over the named intention's change sets.
// It is the replacement for the per-file diff tabs this command used to open:
// instead of one read-only tab per file, it puts the review surface into a walk
// whose cursor is the intention's own member order. The set in front is shown
// in its real file, accept and reject decide it, and next/prev step to the next
// set -- opening that set's file when it lives in another one.
//
// The walk is app state, not an artifact: it writes nothing (no ref, index or
// worktree moves) and materialises nothing. The answer records the walk's shape
// -- the sets in the intention's order and the files in file order -- so a
// client can render the same two orders the editor is using.
func (a *App) intentReview(ctx context.Context, svc *git.Service, set intent.Set, cmd intent.Command) (intent.Result, error) {
	in, ok := set[cmd.Name]
	if !ok {
		return intent.Result{}, fmt.Errorf("intent review: no such intention %q", cmd.Name)
	}
	if len(in.Members) == 0 {
		return intent.Result{}, fmt.Errorf("intent review: intention %q has no members, so there is no seam to walk", cmd.Name)
	}
	walk, err := a.startIntentWalk(in)
	if err != nil {
		return intent.Result{}, err
	}
	return intent.Result{Walk: walk}, nil
}
