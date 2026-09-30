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
type Review struct {
	Name  string   `json:"name"`
	Base  string   `json:"base"`
	Files []string `json:"files"`
}
