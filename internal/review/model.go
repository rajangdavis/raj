// Package review is the `raj --review` console: a separate client process that
// lists a running raj's pending change sets and takes decisions on them over
// the control socket.
//
// It owns no document. The host owns the model and the wire; this package only
// reads the queue and sends accept, reject and clear. The transport lives
// behind the small Transport interface so the item model and its ordering can
// be tested without a socket, and no code here ever touches document text.
package review

import (
	"fmt"
	"sort"
	"strings"

	"raj/internal/control"
)

// Kind separates the two things a queued item can be. A change set edits a
// buffer's text; a deletion (or dir-removal) removes a file. The wire tags the
// latter "delete" and "rmdir"; both arrive here as KindDelete because the
// console decides them the same way — not at all, until the delete verbs grow a
// review form.
type Kind string

const (
	KindEdit   Kind = "edit"
	KindDelete Kind = "delete"
)

// State is where a queued set stands. Invalid and superseded are both still
// Proposed on the wire; they are split here to match the two reasons the host
// can give. Superseded is an invalid set whose collider can be named, and
// invalid is one that cannot.
type State string

const (
	StateProposed   State = "proposed"
	StateRejected   State = "rejected"
	StateInvalid    State = "invalid"
	StateSuperseded State = "superseded"
)

// stateAccepted is not a queue state: `groups` still lists a set somebody has
// decided, and the console skips it rather than offering it for decision again.
const stateAccepted State = "accepted"

// Size is a set's compact added/removed byte count. For a set whose hunks the
// server rendered it is summed from the old and new text; for a rejected or
// invalid set, which has no rendered hunks, it falls back to the net byte
// count on the group.
type Size struct {
	Added   int
	Removed int
}

func (s Size) String() string {
	switch {
	case s.Added == 0 && s.Removed == 0:
		return "0"
	case s.Removed == 0:
		return fmt.Sprintf("+%d", s.Added)
	case s.Added == 0:
		return fmt.Sprintf("-%d", s.Removed)
	}
	return fmt.Sprintf("+%d -%d", s.Added, s.Removed)
}

// Item is one row of the review queue: the decision target and enough context
// to recognise it before the decision commits.
type Item struct {
	File    string
	Author  uint8
	Kind    Kind
	Size    Size
	State   State
	Excerpt string

	// Group is the wire change-set id. Zero for a deletion, which has no set.
	Group uint64
	// Reason explains an invalid or superseded state, and carries a clear
	// refusal's blocker when one was reported.
	Reason string
	// Diff is the compact old to new body, already split into display lines. It
	// is empty for a set the server did not render a diff for.
	Diff []string
}

// Target names the item the way the decision row does, so a mis-keyed decision
// is visible before it commits.
func (it Item) Target() string {
	name := it.File
	if name == "" {
		name = "(active buffer)"
	}
	return fmt.Sprintf("%s (author %d, %s)", name, it.Author, it.Kind)
}

// Row is the one-line queue entry.
func (it Item) Row() string {
	return fmt.Sprintf("%s  %s  author %d  %s  %s  %s",
		it.State, it.File, it.Author, it.Kind, it.Size.String(), it.Excerpt)
}

// stateRank orders the queue: proposed sets are the decisions, rejected sets
// are shown until they are cleared, and invalid/superseded sets are shown but
// never silently dropped.
func stateRank(s State) int {
	switch s {
	case StateProposed:
		return 0
	case StateRejected:
		return 1
	default:
		return 2
	}
}

// Queue is the ordered list the console renders and decides.
type Queue struct {
	Items []Item
}

// Sort puts proposed first, then rejected, then invalid/superseded, and within a
// band orders by file and set id so two fetches of an unchanged world read the
// same.
func (q *Queue) Sort() {
	sort.SliceStable(q.Items, func(i, j int) bool {
		a, b := q.Items[i], q.Items[j]
		if ra, rb := stateRank(a.State), stateRank(b.State); ra != rb {
			return ra < rb
		}
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Group != b.Group {
			return a.Group < b.Group
		}
		return a.Author < b.Author
	})
}

// AuthorCount is one author's share of the queue.
type AuthorCount struct {
	Author uint8
	Count  int
}

// Header is the queue's summary counts: total, per author, and the
// invalid/superseded count.
type Header struct {
	Total      int
	Authors    []AuthorCount
	Invalid    int
	Superseded int
}

func (q Queue) Header() Header {
	h := Header{Total: len(q.Items)}
	byAuthor := map[uint8]int{}
	for _, it := range q.Items {
		byAuthor[it.Author]++
		switch it.State {
		case StateInvalid:
			h.Invalid++
		case StateSuperseded:
			h.Superseded++
		}
	}
	for author, n := range byAuthor {
		h.Authors = append(h.Authors, AuthorCount{Author: author, Count: n})
	}
	sort.Slice(h.Authors, func(i, j int) bool { return h.Authors[i].Author < h.Authors[j].Author })
	return h
}

// Line is the header's compact one-line form.
func (h Header) Line() string {
	parts := []string{fmt.Sprintf("%d item(s)", h.Total)}
	for _, a := range h.Authors {
		parts = append(parts, fmt.Sprintf("author %d: %d", a.Author, a.Count))
	}
	parts = append(parts,
		fmt.Sprintf("invalid: %d", h.Invalid),
		fmt.Sprintf("superseded: %d", h.Superseded))
	return strings.Join(parts, "  ")
}

// itemState maps a wire group to a queue state. Invalid is derived, not a wire
// state: a still-Proposed set with no surviving hunk. The collider decides
// whether it reads as superseded or as invalid with no name.
func itemState(g control.Group) State {
	switch g.State {
	case "rejected":
		return StateRejected
	case "accepted":
		return stateAccepted
	}
	if g.Invalid {
		if g.InvalidBy != nil {
			return StateSuperseded
		}
		return StateInvalid
	}
	return StateProposed
}

// invalidReason words the two invalid cases the way the host's `groups` does.
func invalidReason(g control.Group) string {
	if g.InvalidBy != nil {
		return fmt.Sprintf("superseded by set %d (author %d)", g.InvalidBy.Group, g.InvalidBy.Author)
	}
	return "superseded; no colliding set can be named"
}

// itemFrom builds one Item from a group and, when the server rendered it, the
// set's diff. The diff supplies the true added/removed split and the excerpt;
// a rejected or invalid set has neither, so its size falls back to the net byte
// count and its excerpt is the state.
func itemFrom(path string, g control.Group, d *control.DiffGroup) Item {
	it := Item{
		File:   path,
		Author: g.Author,
		Kind:   KindEdit,
		State:  itemState(g),
		Group:  g.ID,
	}
	if it.State == StateInvalid || it.State == StateSuperseded {
		it.Reason = invalidReason(g)
	}
	if d != nil {
		it.Diff = diffLines(d)
		it.Size = diffSize(d)
	}
	if it.Size.Added == 0 && it.Size.Removed == 0 && g.Bytes != 0 {
		if g.Bytes > 0 {
			it.Size.Added = g.Bytes
		} else {
			it.Size.Removed = -g.Bytes
		}
	}
	if s := diffExcerpt(d); s != "" {
		it.Excerpt = s
	} else {
		it.Excerpt = "(" + string(it.State) + ")"
	}
	return it
}

func diffSize(d *control.DiffGroup) Size {
	var s Size
	hunks := append(append([]control.DiffHunk{}, d.Hunks...), d.MovedHunks...)
	for _, h := range hunks {
		s.Added += len(h.New)
		s.Removed += len(h.Old)
	}
	return s
}

// diffExcerpt is the first non-empty line of the first new side, falling back
// to the first old side for a pure deletion.
func diffExcerpt(d *control.DiffGroup) string {
	if d == nil {
		return ""
	}
	hunks := append(append([]control.DiffHunk{}, d.Hunks...), d.MovedHunks...)
	for _, h := range hunks {
		if s := firstLine(h.New); s != "" {
			return s
		}
		if s := firstLine(h.Old); s != "" {
			return s
		}
	}
	return ""
}

func firstLine(s string) string {
	s = strings.TrimRight(s, "\n")
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimRight(s, " \t\r")
}

// diffLines renders a set's hunks compactly: an @@ header naming the span and
// one line per old and new text line. An empty side prints nothing, so a pure
// insertion has no - lines and a pure deletion no + lines.
func diffLines(d *control.DiffGroup) []string {
	if d == nil {
		return nil
	}
	var out []string
	for _, h := range d.Hunks {
		out = append(out, "@@ "+hunkHeader(h)+" @@")
		out = append(out, sideLines("-", h.Old)...)
		out = append(out, sideLines("+", h.New)...)
	}
	for _, h := range d.MovedHunks {
		out = append(out, "@@ moved: no current span, as written @@")
		out = append(out, sideLines("-", h.Old)...)
		out = append(out, sideLines("+", h.New)...)
	}
	return out
}

func hunkHeader(h control.DiffHunk) string {
	if h.Line > 0 && h.EndLine > 0 {
		return fmt.Sprintf("L%d..L%d (bytes %d..%d)", h.Line, h.EndLine, h.Start, h.End)
	}
	return fmt.Sprintf("bytes %d..%d", h.Start, h.End)
}

func sideLines(prefix, text string) []string {
	if text == "" {
		return nil
	}
	var out []string
	for _, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		out = append(out, prefix+line)
	}
	return out
}
