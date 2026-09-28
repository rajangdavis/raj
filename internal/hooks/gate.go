package hooks

import "time"

// DefaultFloor is the wall-clock cooldown floor a Gate enforces at minimum,
// and the value a zero Options.Floor takes. It stops a burst of triggers from
// running the same hook back to back; a hook's own Cooldown is applied by the
// runner on top.
const DefaultFloor = 2 * time.Second

// DefaultPerRevision is the run cap per revision a Gate enforces when
// Options.PerRevision is zero, so a single revision cannot run a hook without
// bound.
const DefaultPerRevision = 3

// Options tune a Gate; a zero field takes the default.
type Options struct {
	Floor       time.Duration // default DefaultFloor = 2 * time.Second
	PerRevision int           // default DefaultPerRevision = 3
}

// Gate is the in-memory run policy for one workspace: a wall-clock cooldown
// floor, a per-revision run cap, and coalescing to at most one run in flight
// plus one pending rerun per hook. It is not safe for concurrent use.
type Gate struct {
	floor       time.Duration
	perRevision int
	hooks       map[string]*gateState
}

// gateState is one hook's counters. revision is the revision count belongs to;
// a run at a different revision starts the count over.
type gateState struct {
	revision uint64
	count    int
	lastRun  time.Time
	hasRun   bool
	inFlight bool
	pending  bool
}

// NewGate builds a Gate from o, replacing a zero Floor or PerRevision with its
// default. A negative field takes the default too, so a misconfigured caller
// cannot invert the policy.
func NewGate(o Options) *Gate {
	if o.Floor <= 0 {
		o.Floor = DefaultFloor
	}
	if o.PerRevision <= 0 {
		o.PerRevision = DefaultPerRevision
	}
	return &Gate{floor: o.Floor, perRevision: o.PerRevision, hooks: make(map[string]*gateState)}
}

// state returns the counters for hook, making a fresh zero state as needed.
// Only the mutating verbs call it; Allow and TakePending read the map without
// creating an entry.
func (g *Gate) state(hook string) *gateState {
	st, ok := g.hooks[hook]
	if !ok {
		st = &gateState{}
		g.hooks[hook] = st
	}
	return st
}

// Allow reports whether a run of hook may start at revision now. On a refusal
// it returns a reason, and for the cooldown the retryAfter until the floor
// lifts. In-flight refuses first, then the per-revision cap, then the cooldown;
// no refusal mutates state.
func (g *Gate) Allow(hook string, revision uint64, now time.Time) (retryAfter time.Duration, ok bool, reason string) {
	st := g.hooks[hook]
	if st == nil {
		return 0, true, ""
	}
	if st.inFlight {
		return 0, false, "run already in flight"
	}
	// A count from an earlier revision does not apply to this one.
	if st.revision == revision && st.count >= g.perRevision {
		return 0, false, "per-revision run cap reached; the count resets at a new revision, so change the projected buffers (or wait for a peer to) before running again"
	}
	if st.hasRun {
		if elapsed := now.Sub(st.lastRun); elapsed < g.floor {
			return g.floor - elapsed, false, "cooldown active"
		}
	}
	return 0, true, ""
}

// Begin records that a run of hook at revision started at now: it counts
// toward the per-revision cap, stamps the cooldown, and clears a pending
// rerun. A revision different from the last one resets the count first.
func (g *Gate) Begin(hook string, revision uint64, now time.Time) {
	st := g.state(hook)
	if st.revision != revision {
		st.revision = revision
		st.count = 0
	}
	st.count++
	st.lastRun = now
	st.hasRun = true
	st.inFlight = true
	st.pending = false
}

// End clears the in-flight flag for hook. A pending rerun, if any, survives so
// the caller can TakePending once the run it belongs to has ended.
func (g *Gate) End(hook string) {
	if st := g.hooks[hook]; st != nil {
		st.inFlight = false
	}
}

// Request queues at most one pending rerun for a hook that is in flight and
// reports whether this request was the one queued. A hook not in flight, or
// one that already has a pending rerun, returns false.
func (g *Gate) Request(hook string) bool {
	st := g.hooks[hook]
	if st == nil || !st.inFlight || st.pending {
		return false
	}
	st.pending = true
	return true
}

// TakePending consumes and reports hook's pending-rerun flag.
func (g *Gate) TakePending(hook string) bool {
	st := g.hooks[hook]
	if st == nil || !st.pending {
		return false
	}
	st.pending = false
	return true
}
