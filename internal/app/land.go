package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"raj/internal/editor"
	"raj/internal/git"
	"raj/internal/intent"
)

// intentLand is commit-on-land: after the L7 gesture accepted and saved a
// wave's change sets, export the wave's reviewed composition as one commit
// parented on the base ref's head and move raj/baseline to it. Why this and not
// a clean-tree status check: a clean tree only proves disk == projection at
// check time; it does not bind the commit to what the gate saw. Exporting the
// reviewed projection and pointing a ref at it makes the reviewed projection
// the thing that ships, and publish then only pushes a commit that already
// exists. One commit per wave, never a chain; a re-land of an unchanged wave
// writes nothing new (intent.Land).
func (a *App) intentLand(ctx context.Context, svc *git.Service, cmd intent.Command) (intent.Result, error) {
	task := cmd.Task
	if task == "" {
		task = cmd.Name
	}
	if task == "" {
		return intent.Result{}, errors.New("intent land: needs the task whose wave is landing")
	}
	members := a.waveMembers(task)
	if len(members) == 0 {
		return intent.Result{}, nil
	}
	baseRef := cmd.Base
	if baseRef == "" {
		baseRef = a.landBaseRef(ctx, svc)
	}
	base, err := svc.RevParse(ctx, baseRef)
	if err != nil {
		return intent.Result{}, fmt.Errorf("intent land: base %q: %w", baseRef, err)
	}
	proj, err := a.waveProjection(members)
	if err != nil {
		return intent.Result{}, err
	}
	res, err := intent.Land(ctx, svc, a.state, intent.Intention{
		Name: task, Base: baseRef, BaseSHA: base, Members: members, Created: time.Now().UTC(),
	}, base, proj)
	if err != nil {
		return intent.Result{}, err
	}
	return intent.Result{Tree: res.Tree, Commit: res.Commit, Parent: res.Parent, BaseSHA: base}, nil
}

// waveMembers gathers a task's change sets across every buffer, oldest first,
// qualified by the buffer path that numbers each id.
func (a *App) waveMembers(task string) []intent.Member {
	panes := append(append([]*editor.Pane{}, a.Tabs.All()...), a.headless...)
	var out []intent.Member
	for _, pane := range panes {
		if pane.File.Path == "" {
			continue
		}
		rel, ok := a.workspaceRel(pane.File.Path)
		if !ok {
			continue
		}
		for _, g := range pane.File.Session().Groups() {
			if g.Task != task {
				continue
			}
			out = append(out, intent.Member{ID: g.ID, Path: rel})
		}
	}
	return out
}

// waveProjection composes the wave's member buffers, admitting only the wave's
// member ids per buffer, so a non-member set sharing a file stays out of the
// landed tree. Like intentProjection it composes each path from the buffer's
// base text plus the admitted sets only, so an accepted set belonging to
// another wave cannot leak into what lands; only non-members are excluded,
// accepted or otherwise, and a proposed member is still included.
func (a *App) waveProjection(members []intent.Member) (intent.Projection, error) {
	admit := make(map[string]map[uint64]bool)
	for _, m := range members {
		ids := admit[m.Path]
		if ids == nil {
			ids = make(map[uint64]bool)
			admit[m.Path] = ids
		}
		ids[m.ID] = true
	}
	out := intent.Projection{}
	panes := append(append([]*editor.Pane{}, a.Tabs.All()...), a.headless...)
	for _, pane := range panes {
		if pane.File.Path == "" {
			continue
		}
		rel, ok := a.workspaceRel(pane.File.Path)
		if !ok {
			continue
		}
		ids, ok := admit[rel]
		if !ok {
			continue
		}
		text, err := memberSlice(pane.File.Session(), ids)
		if err != nil {
			return nil, err
		}
		out[rel] = []byte(text)
	}
	return out, nil
}

// landBaseRef chooses the ref a landed wave parents on. D-S1 (the forge/trunk
// name) is undecided, so no trunk name is hardcoded: the caller's --base wins,
// then the current branch's upstream, then raj/baseline, and finally HEAD so
// the first land in a repository with neither still has a parent. The caller
// reports which ref was used.
func (a *App) landBaseRef(ctx context.Context, svc *git.Service) string {
	if up, err := svc.Upstream(ctx); err == nil && up != "" {
		return up
	}
	if _, err := svc.RevParse(ctx, git.BaselineRef); err == nil {
		return git.BaselineRef
	}
	return "HEAD"
}
