// Package intent is the editor's minimal intentions: a named, owned selection
// of change sets over a base. An intention materialises to a git tree and
// exports to an inert commit (objects only); it moves no ref.
//
// The package owns the domain type and the resolution rules. Materialisation
// and export go through internal/git's object plumbing, so this package knows
// git capability names, never a second implementation of them.
package intent

import "time"

// State is an intention's lifecycle state.
type State string

const (
	// Open is an intention with no export yet.
	Open State = "open"
	// Exported is an intention whose export has produced a commit.
	Exported State = "exported"
)

// Intention is a named, owned selection of member groups over a base. A member
// is a group id qualified by the buffer path that numbers it, because an id is
// unique only within one buffer/session.
//
// The canonical fields are Name, Owner, Base, Task, Members and State. BaseSHA and
// Created are the deterministic-export metadata (D3): BaseSHA pins the base
// ref's commit so a re-materialisation keeps the same base even if the ref
// moves, and Created pins the commit's author and committer date so the same
// inputs give the same commit id.
type Intention struct {
	Name    string
	Owner   string
	Base    string
	Members []Member
	State   State
	// Task is the work the intention belongs to, pinned at creation from the
	// creating request (D6). It is empty when the caller named none; it selects
	// a task's groups when the caller names no members.
	Task string

	// BaseSHA is the base ref's commit at creation. An intention's base is a
	// ref only (no stacks: one MR per wave); the chain code that would resolve
	// a base intention is left inert and DEFERRED.
	BaseSHA string
	// Created is pinned at creation. A zero value means the caller has no
	// pinned date and export leaves git's own date in place.
	Created time.Time
}

// New builds an intention over a base commit, pinning BaseSHA and Created. It
// touches neither the store nor the repository; the caller supplies the base
// commit it read with git.
func New(name, owner, base, baseSHA string, members []Member, now time.Time) Intention {
	if members == nil {
		members = []Member{}
	}
	return Intention{
		Name:    name,
		Owner:   owner,
		Base:    base,
		Members: members,
		State:   Open,
		BaseSHA: baseSHA,
		Created: now.UTC(),
	}
}

// Has reports whether m is a member.
func (in Intention) Has(m Member) bool {
	for _, member := range in.Members {
		if member == m {
			return true
		}
	}
	return false
}

// Add appends members that are not already members, keeping the existing order.
// It reports whether the membership changed.
func (in *Intention) Add(members ...Member) bool {
	changed := false
	for _, m := range members {
		if in.Has(m) {
			continue
		}
		in.Members = append(in.Members, m)
		changed = true
	}
	return changed
}

// Remove drops members from the membership. It reports whether it changed.
func (in *Intention) Remove(members ...Member) bool {
	drop := make(map[Member]bool, len(members))
	for _, m := range members {
		drop[m] = true
	}
	kept := in.Members[:0]
	changed := false
	for _, m := range in.Members {
		if drop[m] {
			changed = true
			continue
		}
		kept = append(kept, m)
	}
	in.Members = kept
	return changed
}

// Command is one `raj ctl intent` request. It is JSON carried on the control
// wire; Mode names the subcommand. Members are qualified {path, id} pairs;
// Groups are bare session-local ids the host qualifies against the live
// buffers. When both are empty and Task is set, the task's groups are selected
// instead (D6).
type Command struct {
	Mode    string   `json:"mode"`
	Name    string   `json:"name,omitempty"`
	Owner   string   `json:"owner,omitempty"`
	Base    string   `json:"base,omitempty"`
	Groups  []uint64 `json:"groups,omitempty"`
	Members []Member `json:"members,omitempty"`
	Task    string   `json:"task,omitempty"`
	DryRun  bool     `json:"dry_run,omitempty"`
	// Title and Body are the artifact commit message, used by next (and
	// export); the seam pane will prompt for them, and the socket carries them
	// meanwhile. An empty Title falls back to the intention name.
	Title string `json:"title,omitempty"`
	Body  string `json:"body,omitempty"`
	// Approve runs a pending publish proposal (H5). It is the human's
	// decision: refs, remotes and pushes are outward. Withdraw retracts one.
	Approve  bool `json:"approve,omitempty"`
	Withdraw bool `json:"withdraw,omitempty"`
}

// Result is the answer to a Command. Only the fields the subcommand set are
// present, so a list and an export read differently without a kind banner.
type Result struct {
	Intentions []Intention `json:"intentions,omitempty"`
	Intention  *Intention  `json:"intention,omitempty"`
	Tree       string      `json:"tree,omitempty"`
	Commit     string      `json:"commit,omitempty"`
	Parent     string      `json:"parent,omitempty"`
	BaseSHA    string      `json:"base_sha,omitempty"`
	Warning    string      `json:"warning,omitempty"`
	// Publish is set by the publish subcommand: the pinned action proposal,
	// or its result once the human accepts it.
	Publish *Publish `json:"publish,omitempty"`
	// Proofs is the `intent prove` answer: the seam proof of the named
	// intention, pass or fail.
	Proofs []Proof `json:"proofs,omitempty"`
	// Diff is the `intent diff` answer: the named intention materialised
	// alone over its base, and the change between that tree and the base.
	Diff *Diff `json:"diff,omitempty"`
	// Review is the `intent review` answer: the per-file diff tabs the call
	// opened in the running editor.
	Review *Review `json:"review,omitempty"`
	// Next is the `intent next` answer: the export just written for a seam,
	// and the branch publish would push it on.
	Next *Next `json:"next,omitempty"`
	// Group is the `intent group` answer: the seam created or extended from
	// one task's change sets, and what the call left out and why.
	Group *Grouping `json:"group,omitempty"`
}
