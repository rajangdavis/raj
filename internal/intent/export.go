package intent

import (
	"context"

	"raj/internal/git"
)

// Objects is one export's object ids: the tree, the commit, the commit it
// parented on, and the base it was materialised over. An export writes these
// objects and nothing else — no note, no ref, no index or worktree move.
type Objects struct {
	Tree    string
	Commit  string
	Parent  string
	BaseSHA string
}

// Export materialises in over base with proj and writes one commit parented on
// base (the base ref's head). It writes objects only — no ref moves, HEAD is
// untouched, the worktree is untouched — and is repeatable. The commit date is
// pinned to in.Created, so the same inputs give the same commit id (D3). There
// is no note: provenance lives in the raj journal, not in git.
func Export(ctx context.Context, svc *git.Service, in Intention, base string, proj Projection) (Objects, error) {
	tree, err := Materialise(ctx, svc, base, proj)
	if err != nil {
		return Objects{}, err
	}
	var parents []string
	if base != "" {
		parents = append(parents, base)
	}
	commit, err := svc.CommitTreeAt(ctx, tree, parents, "intention "+in.Name, in.Created)
	if err != nil {
		return Objects{}, err
	}
	return Objects{Tree: tree, Commit: commit, Parent: base, BaseSHA: base}, nil
}
