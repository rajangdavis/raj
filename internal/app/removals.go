package app

import (
	"fmt"
	"path/filepath"

	"raj/internal/control"
	"raj/internal/keys"
)

// Pending removals need a surface that outlives the gate prompt. A delete or
// rmdir proposal raises a one-time question; ignore or dismiss it and the
// workspace maps still hold the proposal, but nothing on screen offers a way
// back to the decision. This file is that surface: a status-line note while
// anything is pending, and a bound key that re-raises the oldest proposal
// through the same prompt the gate uses.
//
// The prompt is the only place a removal is carried out or retracted, so
// re-raising is a recovery gesture rather than a second decision path: the
// approve answer still runs removeDeleted/removeDirDeleted, and the withdraw
// answer still runs WithdrawDeletion/WithdrawDirRemoval.

// pendingRemoval is one queued proposal: a path and whether it names a
// directory. The maps are the truth about what is pending; this queue only
// remembers the order proposals arrived in, so the re-raise key can always
// offer the oldest first.
type pendingRemoval struct {
	path string
	dir  bool
}

// notePendingRemoval appends a proposal to the arrival queue. ProposeDeletion
// and ProposeDirRemoval are idempotent, so an already-queued path is left alone
// rather than queued twice.
func (a *App) notePendingRemoval(path string, dir bool) {
	for _, r := range a.pendingRemovals {
		if r.path == path && r.dir == dir {
			return
		}
	}
	a.pendingRemovals = append(a.pendingRemovals, pendingRemoval{path: path, dir: dir})
}

// clearPendingRemoval drops a decided proposal from the arrival queue.
func (a *App) clearPendingRemoval(path string, dir bool) {
	for i, r := range a.pendingRemovals {
		if r.path == path && r.dir == dir {
			a.pendingRemovals = append(a.pendingRemovals[:i], a.pendingRemovals[i+1:]...)
			return
		}
	}
}

// oldestPendingRemoval is the next proposal the re-raise key presents: the
// earliest arrival whose map still holds it. A queue entry whose proposal was
// cleared by another route is pruned as the walk passes it, so a stale entry
// cannot wedge the gesture.
func (a *App) oldestPendingRemoval() (pendingRemoval, bool) {
	for len(a.pendingRemovals) > 0 {
		r := a.pendingRemovals[0]
		present := false
		if r.dir {
			_, present = a.pendingDirRemovals[r.path]
		} else {
			_, present = a.pendingDeletions[r.path]
		}
		if present {
			return r, true
		}
		a.pendingRemovals = a.pendingRemovals[1:]
	}
	return pendingRemoval{}, false
}

// pendingRemovalNote is the persistent status-line indicator: a count and the
// chord that re-opens the decision. It is empty with nothing pending, so the
// no-pending case draws nothing.
func (a *App) pendingRemovalNote() string {
	n := len(a.pendingDeletions) + len(a.pendingDirRemovals)
	if n == 0 {
		return ""
	}
	word := "removals"
	if n == 1 {
		word = "removal"
	}
	return fmt.Sprintf("%d pending %s (%s)", n, word, chordFor(keys.PendingRemovals))
}

// reopenPendingRemoval re-raises the decision for the oldest pending deletion
// or dir-removal through the gate's own prompt. A question already on screen is
// never interrupted; the chord can be pressed again once it closes.
func (a *App) reopenPendingRemoval() {
	if a.Prompt.Open {
		return
	}
	r, ok := a.oldestPendingRemoval()
	if !ok {
		a.status = "no pending removals"
		return
	}
	if r.dir {
		a.promptDirRemoval(a.pendingDirRemovals[r.path])
		return
	}
	d := a.pendingDeletions[r.path]
	p := a.openDeletionPane(d.Path)
	if p == nil {
		// No pane to prompt against yet. Opening the path raises the gate on
		// its own once the buffer is active, so the walk to the prompt stays
		// the same one a focus change takes.
		a.OpenFile(d.Path)
		return
	}
	// The prompt is being raised outside maybePromptDeletion, so mark the pane
	// as seen: otherwise the next event would ask again the moment the answer
	// closes.
	a.deletionPromptPane = p
	a.promptDeletion(p, d)
}

// withdrawDeletion answers a gate prompt with Withdraw: the proposal is
// retracted through the same method the wire's `delete -withdraw` uses. The
// editor passes the proposal's own author, which is what the owner check wants
// -- the human is the backstop, not a peer retracting someone else's work.
func (a *App) withdrawDeletion(d control.Deletion) {
	if err := a.WithdrawDeletion(d.Path, d.Author); err != nil {
		a.status = err.Error()
		return
	}
	a.deletionPromptPane = nil
	a.status = "withdrew the deletion of " + filepath.Base(d.Path)
}

// withdrawDirRemoval is withdrawDeletion for a directory.
func (a *App) withdrawDirRemoval(d control.DirRemoval) {
	if err := a.WithdrawDirRemoval(d.Path, d.Author); err != nil {
		a.status = err.Error()
		return
	}
	a.status = "withdrew the removal of " + filepath.Base(d.Path)
}
