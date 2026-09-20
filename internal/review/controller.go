package review

import (
	"fmt"

	"raj/internal/control"
)

// Fetcher turns a Transport's raw replies into the ordered queue. It remembers
// every buffer it has swept, so a set rejected during the session stays on the
// queue after `proposals` stops listing its file; `groups` still knows it, and
// the console shows it until it is cleared.
type Fetcher struct {
	t     Transport
	seen  map[string]struct{}
	order []string
}

func NewFetcher(t Transport) *Fetcher {
	return &Fetcher{t: t, seen: map[string]struct{}{}}
}

func (f *Fetcher) remember(path string) {
	if _, ok := f.seen[path]; ok {
		return
	}
	f.seen[path] = struct{}{}
	f.order = append(f.order, path)
}

// Fetch asks for the workspace's proposals and, for every buffer it has ever
// swept, that buffer's groups and rendered diff. A buffer that has gone away is
// dropped rather than failing the whole fetch.
func (f *Fetcher) Fetch() (Queue, error) {
	props, err := f.t.Proposals()
	if err != nil {
		return Queue{}, err
	}
	for _, p := range props {
		if p.Kind == "set" {
			f.remember(p.Path)
		}
	}
	// A rejected or invalid set is not in `proposals`, but its text still sits
	// in the buffer, which keeps the buffer dirty. Sweeping the dirty open
	// buffers reaches those sets so they are shown and undecided rather than
	// silently missing on a cold start.
	if buffers, berr := f.t.Buffers(); berr == nil {
		for _, b := range buffers {
			if b.Dirty {
				f.remember(b.Path)
			}
		}
	}
	var items []Item
	for _, path := range f.order {
		groups, err := f.t.Groups(path)
		if err != nil {
			continue
		}
		diffs, err := f.t.Diff(path)
		if err != nil {
			diffs = nil
		}
		rendered := map[uint64]*control.DiffGroup{}
		for i := range diffs {
			rendered[diffs[i].ID] = &diffs[i]
		}
		for _, g := range groups {
			st := itemState(g)
			if st == stateAccepted {
				continue
			}
			it := itemFrom(path, g, rendered[g.ID])
			it.State = st
			items = append(items, it)
		}
	}
	for _, p := range props {
		switch p.Kind {
		case "delete", "rmdir":
			excerpt := "delete file"
			if p.Kind == "rmdir" {
				excerpt = "remove directory"
			}
			items = append(items, Item{
				File: p.Path, Author: p.Author, Kind: KindDelete,
				State: StateProposed, Excerpt: excerpt,
			})
		}
	}
	q := Queue{Items: items}
	q.Sort()
	return q, nil
}

// Action is one of the three decisions the console can send.
type Action int

const (
	Accept Action = iota
	Reject
	Clear
)

func (a Action) String() string {
	switch a {
	case Accept:
		return "accept"
	case Reject:
		return "reject"
	case Clear:
		return "clear"
	}
	return "unknown"
}

// DriftError reports that the set a decision named is no longer in the state the
// screen showed — or is gone, or has changed under it. It is returned instead
// of sending the decision, so a stale screen cannot decide blind.
type DriftError struct {
	File  string
	Group uint64
	Want  State
	Got   State
	Gone  bool

	// Detail, when set, is the drift that is not a state change — the rendered
	// change itself moved.
	Detail string
}

func (e *DriftError) Error() string {
	switch {
	case e.Gone:
		return fmt.Sprintf("change set %d in %s is gone; re-rendering", e.Group, e.File)
	case e.Detail != "":
		return fmt.Sprintf("change set %d in %s %s; re-rendering", e.Group, e.File, e.Detail)
	default:
		return fmt.Sprintf("change set %d in %s changed from %s to %s; re-rendering",
			e.Group, e.File, e.Want, e.Got)
	}
}

// Controller owns the fetched queue and sends decisions against it. Every
// decision re-asks the target's buffer first, so the state the decision commits
// is the state on screen. There is deliberately no pending-decision queue: a
// decision that cannot reach the host is reported, never buffered.
type Controller struct {
	t     Transport
	f     *Fetcher
	Queue Queue
}

func NewController(t Transport) *Controller {
	return &Controller{t: t, f: NewFetcher(t)}
}

// Refresh replaces the queue with a fresh fetch.
func (c *Controller) Refresh() error {
	q, err := c.f.Fetch()
	if err != nil {
		return err
	}
	c.Queue = q
	return nil
}

// Decide sends one decision for it, first confirming with the host that the set
// still exists and still holds the state and shape the console showed. A file
// deletion has no change set and no decision here; it is refused by name rather
// than silently ignored.
func (c *Controller) Decide(a Action, it Item) error {
	if it.Kind != KindEdit {
		return fmt.Errorf("%s is a %s, not a change set; decide it from the host", it.File, it.Kind)
	}
	groups, err := c.t.Groups(it.File)
	if err != nil {
		return err
	}
	g, ok := findGroup(groups, it.Group)
	if !ok {
		return &DriftError{File: it.File, Group: it.Group, Want: it.State, Gone: true}
	}
	got := itemState(g)
	if got != it.State {
		return &DriftError{File: it.File, Group: it.Group, Want: it.State, Got: got}
	}
	if diffs, derr := c.t.Diff(it.File); derr == nil {
		var d *control.DiffGroup
		for i := range diffs {
			if diffs[i].ID == it.Group {
				d = &diffs[i]
				break
			}
		}
		if fresh := itemFrom(it.File, g, d); !sameChange(it, fresh) {
			return &DriftError{File: it.File, Group: it.Group,
				Want: it.State, Got: got, Detail: "changed since it was listed"}
		}
	}
	switch a {
	case Accept:
		return c.t.Accept(it.File, it.Group)
	case Reject:
		return c.t.Reject(it.File, it.Group)
	case Clear:
		return c.t.Clear(it.File, it.Group)
	}
	return fmt.Errorf("unknown decision %d for %s#%d", a, it.File, it.Group)
}

// sameChange compares the parts of two Items that describe the change itself:
// state, size and the rendered body. It is what decides that a target the world
// moved past is re-rendered rather than decided blind.
func sameChange(a, b Item) bool {
	if a.State != b.State || a.Size != b.Size || len(a.Diff) != len(b.Diff) {
		return false
	}
	for i := range a.Diff {
		if a.Diff[i] != b.Diff[i] {
			return false
		}
	}
	return true
}

// AcceptAll accepts every current item for which match is true, stopping at the
// first decision the host refuses — a moved target is reported, not skipped.
func (c *Controller) AcceptAll(match func(Item) bool) (int, error) {
	var targets []Item
	for _, it := range c.Queue.Items {
		if it.Kind == KindEdit && match(it) {
			targets = append(targets, it)
		}
	}
	n := 0
	for _, it := range targets {
		if err := c.Decide(Accept, it); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func findGroup(groups []control.Group, id uint64) (control.Group, bool) {
	for _, g := range groups {
		if g.ID == id {
			return g, true
		}
	}
	return control.Group{}, false
}
