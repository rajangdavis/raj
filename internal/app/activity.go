package app

import (
	"fmt"
	"sort"
	"strings"

	"raj/internal/control"
	"raj/internal/editor"
	"raj/internal/piecetable"
)

// activitySnapshot is the agent-working indicator's data: the other-author
// change sets awaiting a decision and the live roster behind them.
// refreshActivity rebuilds it on the idle tick, and the desktop status line,
// the phone review bar and the save confirm all read it, so the surfaces
// cannot disagree about who is working or how much is pending.
type activitySnapshot struct {
	// agent and state name the first agent in an active state, by id; empty
	// when no agent is working, blocked or reviewing.
	agent string
	state string
	// pending is how many other-author change sets await a decision across
	// every open and headless buffer.
	pending int
	// authors is the distinct names behind those sets and states maps an
	// author id to its roster state, both for the save confirm's wording.
	authors []string
	states  map[uint8]string
}

// activeState reports whether a roster state is one the indicator names. Idle,
// listening and waiting agents are quiet: a waiting one is already counted by
// the pending number, and gone or stale is not something to show.
func activeState(state string) bool {
	switch state {
	case control.StateWorking, control.StateBlocked, control.StateReview:
		return true
	}
	return false
}

// refreshActivity rebuilds the snapshot. It runs on the same idle tick the
// control generation moves on, so it adds no timer of its own.
func (a *App) refreshActivity() {
	var snap activitySnapshot
	seen := map[uint8]bool{}
	panes := append(append([]*editor.Pane{}, a.Tabs.All()...), a.headless...)
	for _, p := range panes {
		for _, g := range p.File.Session().Pending() {
			au := uint8(g.Author)
			// The user's own pending work is not something to warn about:
			// only another writer's set is what a save approves unseen.
			if a.authorIsHuman(au) {
				continue
			}
			snap.pending++
			seen[au] = true
		}
	}
	ids := make([]int, 0, len(seen))
	for id := range seen {
		ids = append(ids, int(id))
	}
	sort.Ints(ids)
	for _, id := range ids {
		snap.authors = append(snap.authors, a.participantName(piecetable.Author(id)))
	}
	if a.control != nil {
		snap.states = map[uint8]string{}
		for _, p := range a.control.Roster() {
			snap.states[p.ID] = p.State
			if snap.agent == "" && p.Connected && p.Kind == control.KindAgent && activeState(p.State) {
				snap.agent, snap.state = p.Name, p.State
			}
		}
	}
	if a.phone {
		a.flashAgentChange(snap.agent, snap.state)
	}
	a.activity = snap
}

// flashAgentChange puts a one-line note in the transient overlay when the
// active agent starts or stops. The phone profile has no status bar, so the
// overlay is where a transition is seen; a desktop has the persistent segment
// instead. It never overwrites a message the app just set.
func (a *App) flashAgentChange(agent, state string) {
	if a.activity.agent == agent && a.activity.state == state {
		return
	}
	if a.status != "" {
		return
	}
	switch {
	case agent != "":
		a.status = agent + " " + state
	case a.activity.agent != "":
		a.status = a.activity.agent + " stopped"
	}
}

// activitySegment is the desktop status line's agent segment: the active agent
// and the pending count, or empty when neither is present.
func (a *App) activitySegment() string {
	var parts []string
	if a.activity.agent != "" {
		parts = append(parts, "● "+a.activity.agent+" "+a.activity.state)
	}
	if a.activity.pending > 0 {
		parts = append(parts, fmt.Sprintf("pending %d", a.activity.pending))
	}
	return strings.Join(parts, " · ")
}

// phoneActivityText is the phone review bar's wording: the pending count
// first, because that is what the bar is for, then the active agent.
func (a *App) phoneActivityText() string {
	var parts []string
	if a.activity.pending > 0 {
		parts = append(parts, fmt.Sprintf("pending %d", a.activity.pending))
	}
	if a.activity.agent != "" {
		parts = append(parts, a.activity.agent+" "+a.activity.state)
	}
	return strings.Join(parts, " · ")
}

// otherAuthorPending keeps the pending sets a save would approve but the user
// did not author, so the save confirm warns only about work the user has not
// already seen decided.
func (a *App) otherAuthorPending(pending []piecetable.Group) []piecetable.Group {
	var out []piecetable.Group
	for _, g := range pending {
		// A superseded or already-decided set is not approvable work: it has no
		// surviving hunk to approve, so a save must not prompt about it.
		if g.State != piecetable.Proposed || g.Invalid {
			continue
		}
		if a.authorIsHuman(uint8(g.Author)) {
			continue
		}
		out = append(out, g)
	}
	return out
}

// saveConfirmMessage names who a save would approve unseen, with the roster
// state in parentheses when one is known.
func (a *App) saveConfirmMessage(other []piecetable.Group) string {
	seen := map[uint8]bool{}
	var names []string
	var state string
	for _, g := range other {
		au := uint8(g.Author)
		if seen[au] {
			continue
		}
		seen[au] = true
		names = append(names, a.participantName(g.Author))
		if state == "" && a.activity.states != nil {
			state = a.activity.states[au]
		}
	}
	suffix := ""
	if state != "" {
		suffix = " (" + state + ")"
	}
	return fmt.Sprintf("%d pending from %s%s — saving approves them.", len(other), strings.Join(names, ", "), suffix)
}
