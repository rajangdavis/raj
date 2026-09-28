package intent

import (
	"context"
	"fmt"
	"time"

	"raj/internal/git"
	"raj/internal/store"
)

// Landed is one wave's landing export: the commit and tree it wrote, the base
// it parented on, and the ref it moved. Skipped is true when an export of the
// same tree already existed, so a second land of an unchanged wave moves
// nothing and adds no row.
type Landed struct {
	Intention string
	Commit    string
	Tree      string
	Parent    string
	BaseSHA   string
	Ref       string
	Skipped   bool
}

// Land exports one wave's reviewed composition as a single commit parented on
// baseSHA (the base ref's head) and moves git.BaselineRef to it, recording the
// export row through s.
//
// Why this and not a clean-tree status check: a clean tree only proves disk ==
// projection at check time; it does not bind the commit to what the gate saw.
// Exporting the reviewed projection and pointing a ref at it makes the reviewed
// projection the thing that ships, and publish only pushes a commit that
// already exists.
//
// One commit per wave, never a chain: the commit parents on the base head, not
// on a previous export, so a re-export is a sibling (H4). A second land of an
// unchanged wave finds its tree already exported and is a no-op -- no commit,
// no ref move, no new row. The only ref it moves is BaselineRef, refused by
// name for any other. The worktree, the user's index and HEAD are untouched:
// the tree is built with a temporary index and the ref move is a local
// update-ref.
func Land(ctx context.Context, svc *git.Service, s *store.Store, in Intention, baseSHA string, proj Projection) (Landed, error) {
	if in.Name == "" {
		return Landed{}, fmt.Errorf("intent: land needs a wave name")
	}
	tree, err := Materialise(ctx, svc, baseSHA, proj)
	if err != nil {
		return Landed{}, err
	}
	out := Landed{Intention: in.Name, Tree: tree, Parent: baseSHA, BaseSHA: baseSHA, Ref: git.BaselineRef}
	if prev, found, err := s.LastExport(in.Name); err != nil {
		return Landed{}, err
	} else if found && prev.TreeSHA == tree {
		out.Commit = prev.CommitSHA
		out.Skipped = true
		return out, nil
	}
	var parents []string
	if baseSHA != "" {
		parents = append(parents, baseSHA)
	}
	commit, err := svc.CommitTreeAt(ctx, tree, parents, "wave "+in.Name, in.Created)
	if err != nil {
		return Landed{}, err
	}
	out.Commit = commit
	old := ""
	if cur, err := svc.RevParse(ctx, git.BaselineRef); err == nil {
		old = cur
	}
	if err := svc.UpdateRefCAS(ctx, git.BaselineRef, commit, old); err != nil {
		return Landed{}, err
	}
	if err := SaveRecord(s, Record{
		Intention: in.Name, Groups: in.Members,
		CommitSHA: commit, ParentSHA: baseSHA, BaseSHA: baseSHA,
		TreeSHA: tree, Time: time.Now().UTC(),
	}); err != nil {
		return Landed{}, err
	}
	return out, nil
}
