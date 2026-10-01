package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"raj/internal/editor"
	"raj/internal/git"
	"raj/internal/intent"
	"raj/internal/piecetable"
	"raj/internal/store"
)

// groupRef is one change set seen across the workspace's buffers. The id is
// the session's own; a task's groups are collected by walking every buffer.
type groupRef struct {
	ID     uint64
	Path   string
	Task   string
	Author uint8
	State  piecetable.GroupState
}

// Intent answers an `intent` control request. The request and its answer are
// JSON payloads carried on the existing hook JSON field: the header's field
// codes are a closed, nearly full set, and an intention request is text by
// construction. The op has its own verb code, so it is not a hook.
//
// It runs on the event thread like the other host methods; the object writes go
// through internal/git, which moves no ref and never touches the worktree or
// the user's index.
func (h host) Intent(payload string) (string, error) {
	var cmd intent.Command
	if err := json.Unmarshal([]byte(payload), &cmd); err != nil {
		return "", fmt.Errorf("intent: %w", err)
	}
	res, err := h.a.runIntent(context.Background(), cmd)
	if err != nil {
		return "", err
	}
	out, err := json.Marshal(res)
	if err != nil {
		return "", fmt.Errorf("intent: %w", err)
	}
	return string(out), nil
}

// runIntent dispatches one intent subcommand. Intentions live in the workspace
// store; the projection and the object writes come from the live buffers and
// internal/git, so an export sees unsaved proposals.
func (a *App) runIntent(ctx context.Context, cmd intent.Command) (intent.Result, error) {
	if a.state == nil {
		return intent.Result{}, errors.New("intent: the workspace store is not open")
	}
	svc := git.New(a.visible.Primary())
	set, err := a.intentSet()
	if err != nil {
		return intent.Result{}, err
	}
	switch cmd.Mode {
	case "new":
		return a.intentNew(ctx, svc, set, cmd)
	case "add", "remove":
		return a.intentEdit(set, cmd)
	case "list":
		rows, err := a.state.Intentions()
		if err != nil {
			return intent.Result{}, err
		}
		out := make([]intent.Intention, 0, len(rows))
		for _, r := range rows {
			out = append(out, rowToIntention(r))
		}
		return intent.Result{Intentions: out}, nil
	case "show":
		in, ok := set[cmd.Name]
		if !ok {
			return intent.Result{}, fmt.Errorf("intent: no such intention %q", cmd.Name)
		}
		in.Members = append([]intent.Member(nil), in.Members...)
		return intent.Result{Intention: &in}, nil

	case "prove":
		return a.intentProve(ctx, svc, set, cmd)

	case "diff":
		return a.intentDiff(ctx, svc, set, cmd)

	case "review":
		return a.intentReview(ctx, svc, set, cmd)

	case "group":
		return a.intentGroup(ctx, svc, set, cmd)

	case "materialise":
		return a.intentMaterialise(ctx, svc, set, cmd)
	case "export":
		return a.intentExport(ctx, svc, set, cmd, map[string]bool{})
	case "next":
		return a.intentNext(ctx, svc, set, cmd)
	case "land":
		return a.intentLand(ctx, svc, cmd)
	case "publish":
		return a.intentPublish(ctx, svc, cmd)
	default:
		return intent.Result{}, fmt.Errorf("intent: unknown subcommand %q", cmd.Mode)
	}
}

// intentNew creates an intention over a base, pinning the base ref's commit and
// the creation time (D3). When no groups are named and a task is, the task's
// groups are selected (D6).
func (a *App) intentNew(ctx context.Context, svc *git.Service, set intent.Set, cmd intent.Command) (intent.Result, error) {
	if cmd.Name == "" {
		return intent.Result{}, errors.New("intent new: needs a name")
	}
	if cmd.Base == "" {
		return intent.Result{}, errors.New("intent new: needs --ref BASE")
	}
	if _, ok := set[cmd.Name]; ok {
		return intent.Result{}, fmt.Errorf("intent new: %q already exists", cmd.Name)
	}
	members := append([]intent.Member(nil), cmd.Members...)
	if len(cmd.Groups) > 0 {
		qualified, err := a.qualifyGroups(cmd.Groups)
		if err != nil {
			return intent.Result{}, err
		}
		members = append(members, qualified...)
	}
	if len(members) == 0 && cmd.Task != "" {
		for _, g := range a.allGroups() {
			if g.Task != cmd.Task {
				continue
			}
			if m, ok := a.memberFor(g); ok {
				members = append(members, m)
			}
		}
	}
	baseSHA := ""
	if _, ok := set[cmd.Base]; !ok {
		sha, err := svc.RevParse(ctx, cmd.Base)
		if err != nil {
			return intent.Result{}, fmt.Errorf("intent new: base %q is neither an intention nor a git ref: %w", cmd.Base, err)
		}
		baseSHA = sha
	}
	in := intent.New(cmd.Name, cmd.Owner, cmd.Base, baseSHA, members, time.Now())
	in.Task = cmd.Task
	if err := a.state.PutIntention(intentionToRow(in)); err != nil {
		return intent.Result{}, err
	}
	return intent.Result{Intention: &in, BaseSHA: baseSHA}, nil
}

// intentEdit adds or removes member groups.
func (a *App) intentEdit(set intent.Set, cmd intent.Command) (intent.Result, error) {
	in, ok := set[cmd.Name]
	if !ok {
		return intent.Result{}, fmt.Errorf("intent: no such intention %q", cmd.Name)
	}
	members := append([]intent.Member(nil), cmd.Members...)
	if len(cmd.Groups) > 0 {
		qualified, err := a.qualifyGroups(cmd.Groups)
		if err != nil {
			return intent.Result{}, err
		}
		members = append(members, qualified...)
	}
	if len(members) == 0 {
		return intent.Result{}, fmt.Errorf("intent %s: needs at least one group", cmd.Mode)
	}
	if cmd.Mode == "add" {
		in.Add(members...)
	} else {
		in.Remove(members...)
	}
	if err := a.state.PutIntention(intentionToRow(in)); err != nil {
		return intent.Result{}, err
	}
	return intent.Result{Intention: &in}, nil
}

// intentMaterialise writes the intention's tree (objects only) and returns its
// id. It is the dry half of export: no commit, no ref.
func (a *App) intentMaterialise(ctx context.Context, svc *git.Service, set intent.Set, cmd intent.Command) (intent.Result, error) {
	in, ok := set[cmd.Name]
	if !ok {
		return intent.Result{}, fmt.Errorf("intent: no such intention %q", cmd.Name)
	}
	base, err := a.baseCommit(ctx, svc, in)
	if err != nil {
		return intent.Result{}, err
	}
	proj, err := a.intentProjection(in, set)
	if err != nil {
		return intent.Result{}, err
	}
	tree, err := intent.Materialise(ctx, svc, base, proj)
	if err != nil {
		return intent.Result{}, err
	}
	warn, _ := intent.Drift(ctx, svc, in)
	return intent.Result{Tree: tree, BaseSHA: base, Warning: warn}, nil
}

// intentExport materialises the intention and writes one inert commit parented
// on the base ref's head, then records the export. It is one commit per
// intention, not a growing chain (no stacks: one MR per wave), and no ref
// moves. The export record is bookkeeping for that commit; provenance stays in
// the raj journal.
func (a *App) intentExport(ctx context.Context, svc *git.Service, set intent.Set, cmd intent.Command, stack map[string]bool) (intent.Result, error) {
	in, ok := set[cmd.Name]
	if !ok {
		return intent.Result{}, fmt.Errorf("intent: no such intention %q", cmd.Name)
	}
	base, err := a.baseCommit(ctx, svc, in)
	if err != nil {
		return intent.Result{}, err
	}
	proj, err := a.intentProjection(in, set)
	if err != nil {
		return intent.Result{}, err
	}
	if cmd.DryRun {
		tree, err := intent.Materialise(ctx, svc, base, proj)
		if err != nil {
			return intent.Result{}, err
		}
		warn, _ := intent.Drift(ctx, svc, in)
		return intent.Result{Tree: tree, Parent: base, BaseSHA: base, Warning: warn}, nil
	}
	res, err := intent.Export(ctx, svc, in, base, proj, intent.Message{Title: cmd.Title, Body: cmd.Body})
	if err != nil {
		return intent.Result{}, err
	}
	rec := intent.Record{
		Intention: in.Name, Groups: in.Members,
		CommitSHA: res.Commit, ParentSHA: res.Parent, BaseSHA: base,
		TreeSHA: res.Tree, Time: time.Now().UTC(),
	}
	if err := intent.SaveRecord(a.state, rec); err != nil {
		return intent.Result{}, err
	}
	in.State = intent.Exported
	if err := a.state.PutIntention(intentionToRow(in)); err != nil {
		return intent.Result{}, err
	}
	warn, _ := intent.Drift(ctx, svc, in)
	return intent.Result{
		Tree: res.Tree, Commit: res.Commit, Parent: res.Parent, BaseSHA: base, Warning: warn,
	}, nil
}

// intentNext builds a named seam artifact for the push. It exports the
// intention through the one export path intent export uses - materialise the
// members alone over the pinned base, write one inert commit and record the
// export row - taking the commit title and body from the caller, then reports
// the branch publish would name and the commit it would carry. It is strand
// 3.5: it builds the export; the push itself is a separate confirm.
//
// An empty seam is refused before anything is written: a commit of nothing
// would record an artifact with no slice. The message comes over the socket
// now (--title/--body); the editor-side prompt lands with the seam pane.
func (a *App) intentNext(ctx context.Context, svc *git.Service, set intent.Set, cmd intent.Command) (intent.Result, error) {
	in, ok := set[cmd.Name]
	if !ok {
		return intent.Result{}, fmt.Errorf("intent next: no such intention %q", cmd.Name)
	}
	if len(in.Members) == 0 {
		return intent.Result{}, fmt.Errorf("intent next: intention %q has no members, so there is no artifact to export", cmd.Name)
	}
	res, err := a.intentExport(ctx, svc, set, cmd, map[string]bool{})
	if err != nil {
		return intent.Result{}, err
	}
	return intent.Result{
		Warning: res.Warning,
		Next: &intent.Next{
			Name:    in.Name,
			Branch:  intent.BranchForWave(in.Name),
			Commit:  res.Commit,
			Parent:  res.Parent,
			BaseSHA: res.BaseSHA,
			Tree:    res.Tree,
		},
	}, nil
}

// baseCommit resolves in's base to the commit its ref pinned at creation. An
// intention's base is a ref only (no stacks: one MR per wave); a row without a
// pinned SHA falls back to reading the ref now.
func (a *App) baseCommit(ctx context.Context, svc *git.Service, in intent.Intention) (string, error) {
	if in.BaseSHA != "" {
		return in.BaseSHA, nil
	}
	sha, err := svc.RevParse(ctx, in.Base)
	if err != nil {
		return "", fmt.Errorf("intent: %s: base %q does not resolve to a commit: %w", in.Name, in.Base, err)
	}
	return sha, nil
}

// resolveBaseCommit is DEFERRED (no stacks: one MR per wave): it resolves an
// intention whose base names another intention. Nothing calls it any more; it
// is kept inert for a future stacked path.

// resolveBaseCommit resolves in's base to a commit: the pinned base SHA for a
// ref, or the base intention's head commit. A real export of a stacked
// intention exports the parent first so a stack is never built on a parent
// with no commit. A dry walk (materialise, or export --dry-run) must write
// nothing, so it refuses when the parent has no export yet rather than
// committing one behind the caller's back.
func (a *App) resolveBaseCommit(ctx context.Context, svc *git.Service, set intent.Set, in intent.Intention, stack map[string]bool, dry bool) (string, error) {
	if _, ok := set[in.Base]; ok {
		if stack[in.Base] {
			return "", fmt.Errorf("intent: base cycle at %q", in.Base)
		}
		stack[in.Base] = true
		prev, found, err := a.state.LastExport(in.Base)
		if err != nil {
			return "", err
		}
		if found {
			return prev.CommitSHA, nil
		}
		if dry {
			return "", fmt.Errorf("intent: %s: base intention %q has no export yet; export it before a dry run", in.Name, in.Base)
		}
		res, err := a.intentExport(ctx, svc, set, intent.Command{Mode: "export", Name: in.Base, Owner: in.Owner}, stack)
		if err != nil {
			return "", err
		}
		return res.Commit, nil
	}
	if in.BaseSHA != "" {
		return in.BaseSHA, nil
	}
	return svc.RevParse(ctx, in.Base)
}

// intentProjection is the member group content over the base. Each path a
// member group touches is composed from the buffer's base text plus the member
// groups only: every other live set, accepted as well as proposed, is excluded,
// so no other uncommitted change can leak into the tree. The seam's own members
// are admitted whether they are accepted or still proposed -- `intent diff`
// shows the seam either way. For a stacked intention the parent's groups are
// admitted too — the parent's composition is already in the base tree, and
// overlaying a shared file without it would revert the parent's hunks. A
// member the live buffers do not know is the D2 hard error.
func (a *App) intentProjection(in intent.Intention, set intent.Set) (intent.Projection, error) {
	find := a.memberFinder()
	own, err := in.ResolveMembers(find)
	if err != nil {
		return nil, err
	}
	// Admit the whole chain's members: set.Chain returns the parents deepest
	// first and in last, and a parent's proposed groups in a shared file must
	// be composed back in or the child's overlay reverts them.
	chain, err := set.Chain(in.Name)
	if err != nil {
		return nil, err
	}
	admit := map[intent.Member]bool{}
	for _, ancestor := range chain {
		resolved, err := ancestor.ResolveMembers(find)
		if err != nil {
			return nil, err
		}
		for _, m := range resolved {
			admit[m] = true
		}
	}
	touched := make(map[string]bool, len(own))
	for _, m := range own {
		touched[m.Path] = true
	}
	out := intent.Projection{}
	panes := append(append([]*editor.Pane{}, a.Tabs.All()...), a.headless...)
	for _, pane := range panes {
		if pane.File.Path == "" {
			continue
		}
		rel, ok := a.workspaceRel(pane.File.Path)
		if !ok || !touched[rel] {
			continue
		}
		// Each buffer numbers its own sets, so admit only the chain members
		// qualified by this buffer's path. Passing the bare ids would admit
		// another buffer's set that happens to share the number.
		paneAdmit := make(map[uint64]bool, len(admit))
		for m := range admit {
			if m.Path == rel {
				paneAdmit[m.ID] = true
			}
		}
		text, err := memberSlice(pane.File.Session(), paneAdmit)
		if err != nil {
			return nil, err
		}
		out[rel] = []byte(text)
	}
	return out, nil
}

// memberSlice composes a session's own base with only the admitted change sets.
// Every set not admitted is treated as rejected for this composition -- whether
// it is accepted or still proposed -- so the result is the base plus the admitted
// sets and nothing else the workspace has committed to. The session is cloned
// because the projection policy admits accepted sets unconditionally; there is
// no way to narrow it to a chosen set without changing the live session's
// decisions, and the clone's decisions are its own.
func memberSlice(sess *piecetable.Session, admit map[uint64]bool) (string, error) {
	snap, err := sess.SnapshotState()
	if err != nil {
		return "", err
	}
	clone, err := piecetable.Restore(snap)
	if err != nil {
		return "", err
	}
	for _, g := range clone.Groups() {
		if admit[g.ID] {
			continue
		}
		clone.MarkGroup(g.ID, piecetable.Rejected)
	}
	return clone.Project(piecetable.AcceptedAndProposed).Text(), nil
}

// memberFinder validates a qualified member against the live buffers and
// resolves a legacy member that carries no path to the one live buffer that
// numbers its id. A group outside the workspace has no selection and reads as
// missing. An id two buffers both number is never silently resolved to one of
// them: a bare id that collides reads as missing rather than composing the
// wrong buffer, so the caller must name the path.
func (a *App) memberFinder() func(intent.Member) (intent.Member, bool) {
	live := map[intent.Member]bool{}
	byID := map[uint64]intent.Member{}
	ambiguous := map[uint64]bool{}
	for _, g := range a.allGroups() {
		m, ok := a.memberFor(g)
		if !ok {
			continue
		}
		live[m] = true
		if prev, seen := byID[m.ID]; seen && prev != m {
			ambiguous[m.ID] = true
			continue
		}
		byID[m.ID] = m
	}
	return func(want intent.Member) (intent.Member, bool) {
		if want.Path != "" {
			if live[want] {
				return want, true
			}
			return intent.Member{}, false
		}
		if ambiguous[want.ID] {
			return intent.Member{}, false
		}
		m, ok := byID[want.ID]
		return m, ok
	}
}

// memberFor qualifies a listed group with the workspace-relative path of the
// buffer that holds it. A group outside the workspace has no path and is not a
// member candidate.
func (a *App) memberFor(g groupRef) (intent.Member, bool) {
	rel, ok := a.workspaceRel(g.Path)
	if !ok {
		return intent.Member{}, false
	}
	return intent.Member{ID: g.ID, Path: rel}, true
}

// qualifyGroups turns bare session-local group ids into qualified members by
// resolving each id to the one live buffer that numbers it. An id two buffers
// both number is refused rather than silently resolved to one of them, and an
// id no live buffer holds is refused by name; membership is qualified so a
// collision cannot hide.
func (a *App) qualifyGroups(ids []uint64) ([]intent.Member, error) {
	byID := make(map[uint64]intent.Member, len(ids))
	ambiguous := map[uint64]bool{}
	for _, g := range a.allGroups() {
		m, ok := a.memberFor(g)
		if !ok {
			continue
		}
		if prev, seen := byID[m.ID]; seen && prev != m {
			ambiguous[m.ID] = true
			continue
		}
		byID[m.ID] = m
	}
	out := make([]intent.Member, 0, len(ids))
	for _, id := range ids {
		if ambiguous[id] {
			return nil, fmt.Errorf("intent: group %d is numbered by more than one buffer; name the member by path", id)
		}
		m, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("intent: group %d is not in any live buffer", id)
		}
		out = append(out, m)
	}
	return out, nil
}

// workspaceRel returns p relative to the workspace root, and false when p is
// outside it or the root cannot be computed.
func (a *App) workspaceRel(p string) (string, bool) {
	rel, err := filepath.Rel(a.visible.Primary(), p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// intentAgents is DEFERRED (H4 revision): the export no longer writes a note,
// so nothing calls it. It is kept inert only because a deletion of another
// writer's proposed text is held back from the edit projection until accept;
// remove it with the surrounding proposals once the accept lands.
// The note joins git history to the agents, so it carries the participant key
// (falling back to the display name, then the numeric id): an opaque integer
// names no one.
func (a *App) intentAgents(members []intent.Member) []string {
	want := make(map[intent.Member]bool, len(members))
	bare := make(map[uint64]bool, len(members))
	for _, m := range members {
		want[m] = true
		if m.Path == "" {
			bare[m.ID] = true
		}
	}
	seen := map[uint8]bool{}
	var out []string
	for _, g := range a.allGroups() {
		m, ok := a.memberFor(g)
		if !ok || seen[g.Author] {
			continue
		}
		if !want[m] && !bare[m.ID] {
			continue
		}
		seen[g.Author] = true
		out = append(out, a.intentAgentName(g.Author))
	}
	sort.Strings(out)
	return out
}

// intentAgentName resolves an author id to the participant key, then its
// display name, then the numeric id when no registry is up (a test harness or
// a session nobody drove).
func (a *App) intentAgentName(author uint8) string {
	if a.control != nil && a.control.Participants != nil {
		if p, ok := a.control.Participants.Get(author); ok {
			if p.Identity != "" {
				return p.Identity
			}
			if p.Name != "" {
				return p.Name
			}
		}
	}
	return fmt.Sprintf("author %d", author)
}

// allGroups lists every change set in every buffer, for task selection and for
// the member finder.
func (a *App) allGroups() []groupRef {
	panes := append(append([]*editor.Pane{}, a.Tabs.All()...), a.headless...)
	var out []groupRef
	for _, pane := range panes {
		if pane.File.Path == "" {
			continue
		}
		for _, g := range pane.File.Session().Groups() {
			out = append(out, groupRef{ID: g.ID, Path: pane.File.Path, Task: g.Task, State: g.State, Author: uint8(g.Author)})
		}
	}
	return out
}

// intentSet loads every stored intention as a resolve Set.
func (a *App) intentSet() (intent.Set, error) {
	rows, err := a.state.Intentions()
	if err != nil {
		return nil, err
	}
	set := make(intent.Set, len(rows))
	for _, r := range rows {
		set[r.Name] = rowToIntention(r)
	}
	return set, nil
}

func rowToIntention(r store.IntentionRow) intent.Intention {
	members := make([]intent.Member, 0, len(r.Members))
	for _, m := range r.Members {
		members = append(members, intent.Member{ID: m.ID, Path: m.Path})
	}
	return intent.Intention{
		Name: r.Name, Owner: r.Owner, Base: r.Base, Task: r.Task,
		Members: members,
		State:   intent.State(r.State), BaseSHA: r.BaseSHA,
		Created: time.UnixMilli(r.Created).UTC(),
	}
}

func intentionToRow(in intent.Intention) store.IntentionRow {
	members := make([]store.IntentionMember, 0, len(in.Members))
	for _, m := range in.Members {
		members = append(members, store.IntentionMember{ID: m.ID, Path: m.Path})
	}
	return store.IntentionRow{
		Name: in.Name, Owner: in.Owner, Base: in.Base, Task: in.Task,
		Members: members,
		State:   string(in.State), BaseSHA: in.BaseSHA, Created: in.Created.UnixMilli(),
	}
}

// intentGroup creates or extends a seam from one task's change sets. It is the
// review agent's grouping gesture (Q16): the task is the seam's reason (Q1), so
// `intent group NAME --task T` takes the task's pending sets across the
// workspace, each qualified by its buffer path.
//
// Scope is PENDING (Proposed) sets only: a grouping pass runs before approval
// and groups the proposals still awaiting a decision, so an accepted or
// rejected set is reported as not-pending rather than re-opened. It is
// idempotent -- a re-run adds nothing and reports the same members -- and it
// never guesses a dependency: a set already in another seam is reported, not
// moved, and two seams that touch one file are reported as an overlap, never
// stacked (Q10).
func (a *App) intentGroup(ctx context.Context, svc *git.Service, set intent.Set, cmd intent.Command) (intent.Result, error) {
	if cmd.Name == "" {
		return intent.Result{}, errors.New("intent group: needs a name")
	}
	if cmd.Task == "" {
		return intent.Result{}, errors.New("intent group: needs --task T; a seam is one task's sets")
	}
	if len(cmd.Members) > 0 || len(cmd.Groups) > 0 {
		return intent.Result{}, errors.New("intent group: takes a task, not explicit members")
	}
	existing, exists := set[cmd.Name]
	if exists && existing.Task != "" && existing.Task != cmd.Task {
		return intent.Result{}, fmt.Errorf("intent group: %q is the task %q and cannot also be %q", cmd.Name, existing.Task, cmd.Task)
	}
	// A task with no sets at all has nothing to group: refuse by name rather
	// than create an empty seam nobody can explain.
	var refs []groupRef
	for _, g := range a.allGroups() {
		if g.Task == cmd.Task {
			refs = append(refs, g)
		}
	}
	if len(refs) == 0 {
		return intent.Result{}, fmt.Errorf("intent group: task %q has no change sets to group", cmd.Task)
	}

	// A set another seam already names is not this seam's to move, however the
	// task reads. A bare member (no path) is the same set when this buffer
	// numbers its id, so it counts too.
	held := map[intent.Member]string{}
	heldBare := map[uint64]string{}
	for name, in := range set {
		if name == cmd.Name {
			continue
		}
		for _, m := range in.Members {
			if m.Path == "" {
				if prev, ok := heldBare[m.ID]; !ok || name < prev {
					heldBare[m.ID] = name
				}
				continue
			}
			if prev, ok := held[m]; !ok || name < prev {
				held[m] = name
			}
		}
	}
	// Two live panes numbering the same {id, path} cannot be told apart, so the
	// member is refused rather than resolved to one of them.
	count := map[intent.Member]int{}
	for _, g := range refs {
		if m, ok := a.memberFor(g); ok {
			count[m]++
		}
	}

	group := intent.Grouping{Name: cmd.Name, Task: cmd.Task, Created: !exists, DryRun: cmd.DryRun}
	for _, g := range refs {
		m, ok := a.memberFor(g)
		if !ok || count[m] > 1 {
			group.Skipped = append(group.Skipped, intent.Skip{
				Member: intent.Member{ID: g.ID},
				Reason: intent.SkipAmbiguousPath,
			})
			continue
		}
		if existing.Has(m) {
			continue // already a member: a re-run adds nothing
		}
		if seam, ok := held[m]; ok {
			group.Skipped = append(group.Skipped, intent.Skip{Member: m, Reason: intent.SkipOtherSeam, Seam: seam})
			continue
		}
		if seam, ok := heldBare[m.ID]; ok {
			group.Skipped = append(group.Skipped, intent.Skip{Member: m, Reason: intent.SkipOtherSeam, Seam: seam})
			continue
		}
		if g.State != piecetable.Proposed {
			group.Skipped = append(group.Skipped, intent.Skip{Member: m, Reason: intent.SkipNotPending, State: g.State.String()})
			continue
		}
		group.Added = append(group.Added, m)
	}
	group.Members = append(append([]intent.Member(nil), existing.Members...), group.Added...)
	group.Changed = len(group.Added) > 0
	group.Overlaps = seamOverlaps(group.Members, set, cmd.Name)

	// The base: an existing seam keeps the base it was pinned to, a new one
	// takes --ref or auto-detects the primary branch. A base that names another
	// intention would be a stack, which is deferred, so it is refused rather
	// than guessed.
	base := cmd.Base
	baseSHA := ""
	if exists {
		base, baseSHA = existing.Base, existing.BaseSHA
		if cmd.Base != "" && cmd.Base != existing.Base {
			return intent.Result{}, fmt.Errorf("intent group: %q is over base %q; --ref %q cannot move it", cmd.Name, existing.Base, cmd.Base)
		}
	} else {
		if base == "" {
			detected, err := a.detectBaseRef(ctx, svc)
			if err != nil {
				return intent.Result{}, err
			}
			base = detected
		}
		if _, ok := set[base]; ok {
			return intent.Result{}, fmt.Errorf("intent group: base %q names an intention; a stacked seam is deferred, so pass a git ref", base)
		}
		sha, err := svc.RevParse(ctx, base)
		if err != nil {
			return intent.Result{}, fmt.Errorf("intent group: base %q does not resolve to a commit: %w", base, err)
		}
		baseSHA = sha
	}
	group.Base, group.BaseSHA = base, baseSHA

	if cmd.DryRun {
		return intent.Result{Group: &group}, nil
	}
	if exists {
		existing.Add(group.Added...)
		if existing.Task == "" {
			existing.Task = cmd.Task
		}
		if err := a.state.PutIntention(intentionToRow(existing)); err != nil {
			return intent.Result{}, err
		}
		return intent.Result{Group: &group}, nil
	}
	in := intent.New(cmd.Name, cmd.Owner, base, baseSHA, group.Added, time.Now())
	in.Task = cmd.Task
	if err := a.state.PutIntention(intentionToRow(in)); err != nil {
		return intent.Result{}, err
	}
	return intent.Result{Group: &group}, nil
}

// detectBaseRef names the seam's base when the caller gives no --ref: the
// current branch's upstream leaf, then the checked-out branch. It never
// hardcodes a trunk name (Q9).
func (a *App) detectBaseRef(ctx context.Context, svc *git.Service) (string, error) {
	if up, err := svc.Upstream(ctx); err == nil && up != "" {
		if i := strings.LastIndex(up, "/"); i >= 0 && i+1 < len(up) {
			return up[i+1:], nil
		}
		return up, nil
	}
	view, err := svc.View(ctx)
	if err != nil {
		return "", fmt.Errorf("intent group: no --ref and no primary branch to detect: %w", err)
	}
	if view.Branch != "" {
		return view.Branch, nil
	}
	return "", errors.New("intent group: no --ref and no primary branch to detect; pass --ref BASE")
}

// seamOverlaps names every file this seam touches that another seam also
// touches. It reports only: the plan's rule is that a legitimate conflict is a
// review agent's job to catch, so an overlap is never silently stacked (Q10).
func seamOverlaps(members []intent.Member, set intent.Set, self string) []intent.Overlap {
	paths := make(map[string]bool, len(members))
	for _, m := range members {
		paths[m.Path] = true
	}
	var out []intent.Overlap
	seen := map[intent.Overlap]bool{}
	for name, in := range set {
		if name == self {
			continue
		}
		for _, m := range in.Members {
			if m.Path == "" || !paths[m.Path] {
				continue
			}
			o := intent.Overlap{Path: m.Path, Seam: name}
			if seen[o] {
				continue
			}
			seen[o] = true
			out = append(out, o)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Seam < out[j].Seam
	})
	return out
}
