package app

import (
	"context"
	"fmt"
	"strings"

	"raj/internal/git"
	"raj/internal/intent"
)

// intentDiffCap is the most patch bytes `intent diff` carries. A diff over it
// is cut on a line boundary and the answer says so rather than dropping the
// tail silently.
const intentDiffCap = 256 * 1024

// intentDiff materialises the named intention alone over its base and returns
// the change between that tree and the base: a unified patch and a per-file
// churn summary, read from git's objects. It is the reading twin of
// intentProve -- it shares the same materialiser and writes nothing, moving no
// ref and touching no worktree or index. The diff is the slice: the
// intention's members composed over the base, and nothing of the workspace's
// other uncommitted state.
//
// An intention with no members is refused rather than shown as an empty diff:
// nothing selected and nothing changed would read the same, and the caller
// should fix the selection, not take a blank as an answer.
func (a *App) intentDiff(ctx context.Context, svc *git.Service, set intent.Set, cmd intent.Command) (intent.Result, error) {
	in, ok := set[cmd.Name]
	if !ok {
		return intent.Result{}, fmt.Errorf("intent diff: no such intention %q", cmd.Name)
	}
	if len(in.Members) == 0 {
		return intent.Result{}, fmt.Errorf("intent diff: intention %q has no members, so there is no slice to show", cmd.Name)
	}
	m, err := a.materialiseIntention(ctx, svc, set, in)
	if err != nil {
		return intent.Result{}, err
	}
	defer m.Remove()
	patch, numstat, err := svc.DiffTrees(ctx, m.base, m.tree)
	if err != nil {
		return intent.Result{}, err
	}
	out := &intent.Diff{
		Name: in.Name,
		Base: m.base,
		Stat: diffStat(numstat),
		Diff: patch,
	}
	capIntentDiff(out, intentDiffCap)
	return intent.Result{Diff: out}, nil
}

// diffStat totals the numstat rows into the wire summary. A binary file has no
// line counts, so it counts toward the file total and carries no churn.
func diffStat(entries []git.NumStatEntry) intent.Stat {
	st := intent.Stat{Entries: make([]intent.DiffEntry, 0, len(entries))}
	for _, e := range entries {
		st.Files++
		if e.Binary {
			st.Entries = append(st.Entries, intent.DiffEntry{Path: e.Path, Binary: true})
			continue
		}
		st.Additions += e.Additions
		st.Deletions += e.Deletions
		st.Entries = append(st.Entries, intent.DiffEntry{
			Path: e.Path, Additions: e.Additions, Deletions: e.Deletions,
		})
	}
	return st
}

// capIntentDiff keeps at most limit bytes of d.Diff, cutting back to the last
// line boundary so the shown patch is whole lines, and records the omitted
// byte count. A diff at or under the limit, a non-positive limit, or a nil d
// is left untouched. Truncated is what makes the cut visible: the tail is
// dropped only alongside a statement that it was.
func capIntentDiff(d *intent.Diff, limit int) {
	if d == nil || limit <= 0 || len(d.Diff) <= limit {
		return
	}
	head := d.Diff[:limit]
	if i := strings.LastIndexByte(head, '\n'); i >= 0 {
		head = head[:i+1]
	}
	d.OmittedBytes = len(d.Diff) - len(head)
	d.Diff = head
	d.Truncated = true
}
