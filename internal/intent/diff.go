package intent

// DiffEntry is one file's churn in an intention's diff.
type DiffEntry struct {
	Path      string `json:"path"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Binary    bool   `json:"binary,omitempty"`
}

// Stat is the churn summary of an intention's diff: the totals across the
// files the slice touches, and one row per file.
type Stat struct {
	Files     int         `json:"files"`
	Additions int         `json:"additions"`
	Deletions int         `json:"deletions"`
	Entries   []DiffEntry `json:"entries"`
}

// Diff is the `intent diff` answer: the named intention materialised alone
// over its base, and the change between that tree and the base as a unified
// patch plus a churn summary.
//
// Base is the commit the materialised tree was built on. The patch is read
// from git's objects, so it holds the intention's slice and nothing from the
// workspace's uncommitted state. Truncated says the patch was capped and
// OmittedBytes how much of it the cap left out; both are zero for a diff that
// fits.
type Diff struct {
	Name         string `json:"name"`
	Base         string `json:"base"`
	Stat         Stat   `json:"stat"`
	Diff         string `json:"diff"`
	Truncated    bool   `json:"truncated,omitempty"`
	OmittedBytes int    `json:"omitted_bytes,omitempty"`
}

// Review is the `intent review` answer: the seam's diff tabs opened in the
// running editor, one per file the seam changes, named by their
// workspace-relative paths in the order they were opened.
//
// Deprecated: `intent review` now starts a Walk over the intention's sets
// (below) instead of opening a tab per file. The type stays for callers that
// still read it; the walk answer is Walk.
type Review struct {
	Name  string   `json:"name"`
	Base  string   `json:"base"`
	Files []string `json:"files"`
}

// Walk is the `intent review` answer for the review walk: the intention's
// change sets in the intention's own order (the walk order), the distinct
// files they touch in file order (the order a diff renders), and the base the
// walk is over. Sets[0] is the set the walk opened on.
//
// The two orders are deliberately different. A walk follows the intention:
// the order the owner chose when the intention was built, which carries the
// dependencies between sets. A diff reads in file order: git's own numstat
// order, so two hunks in one file sit together. Files records the second so a
// renderer can present one and walk the other without re-deriving either.
type Walk struct {
	Name  string    `json:"name"`
	Base  string    `json:"base"`
	Sets  []WalkSet `json:"sets"`
	Files []string  `json:"files"`
}

// WalkSet is one change set in a Walk: the group id qualified by the
// workspace-relative path of the buffer that numbers it, and the group's
// lifecycle state when the walk opened.
type WalkSet struct {
	Path  string `json:"path"`
	Group uint64 `json:"group"`
	State string `json:"state,omitempty"`
}

// Next is the `intent next` answer: the artifact a seam would push. Commit is
// the export the command just wrote - the same row publish reads from the
// store - and Branch is the name publish would push it under.
type Next struct {
	Name    string `json:"name"`
	Branch  string `json:"branch"`
	Commit  string `json:"commit"`
	Parent  string `json:"parent,omitempty"`
	BaseSHA string `json:"base_sha,omitempty"`
	Tree    string `json:"tree,omitempty"`
}
