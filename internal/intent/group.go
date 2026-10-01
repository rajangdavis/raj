package intent

// Grouping is the answer to `intent group`: the seam built from one task's
// change sets. Added is the delta this call made and Members is the seam's
// whole membership after it, so a re-run reports the same members. Skipped
// names every set the task has that the call did not take, with the reason;
// Overlaps names the files a different seam also touches -- reported, never
// guessed into a stack (Q10).
type Grouping struct {
	Name     string    `json:"name"`
	Task     string    `json:"task,omitempty"`
	Base     string    `json:"base,omitempty"`
	BaseSHA  string    `json:"base_sha,omitempty"`
	Created  bool      `json:"created,omitempty"`
	Changed  bool      `json:"changed"`
	DryRun   bool      `json:"dry_run,omitempty"`
	Added    []Member  `json:"added,omitempty"`
	Members  []Member  `json:"members,omitempty"`
	Skipped  []Skip    `json:"skipped,omitempty"`
	Overlaps []Overlap `json:"overlaps,omitempty"`
}

// Skip reasons. A set the task owns that the call did not take carries exactly
// one: it already belongs to Seam, its state is not pending (State names it),
// or its buffer path could not be qualified to one live buffer.
const (
	// SkipOtherSeam: the set is a member of a different seam.
	SkipOtherSeam = "other-seam"
	// SkipNotPending: the set's state is not Proposed -- it is a decision
	// already made, not a pending proposal a seam pass groups.
	SkipNotPending = "not-pending"
	// SkipAmbiguousPath: the set could not be qualified to one live buffer
	// path, so a member could not name it without guessing.
	SkipAmbiguousPath = "ambiguous-path"
)

// Skip is one set the task owns that a grouping call left out.
type Skip struct {
	Member Member `json:"member"`
	Reason string `json:"reason"`
	// Seam is the other seam holding the member, for SkipOtherSeam.
	Seam string `json:"seam,omitempty"`
	// State is the set's state, for SkipNotPending.
	State string `json:"state,omitempty"`
}

// Overlap names a file two seams both touch. The plan's rule is to report an
// overlap, never to guess a stacking or a dependency: which seam goes first is
// a review agent's decision, not this call's.
type Overlap struct {
	Path string `json:"path"`
	Seam string `json:"seam"`
}
