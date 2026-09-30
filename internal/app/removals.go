package app

import (
	"path/filepath"

	"raj/internal/control"
)

// Removal decisions that cross the attach boundary. The pending proposals
// themselves live in the app workspace maps (pendingDeletions,
// pendingDirRemovals) and are listed by the waiting list alongside everything
// else; this file only holds the attached-client half of answering one, where a
// viewer does not own the filesystem and must forward the human answer to the
// daemon.

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

// approveDeletionRemote forwards the Remove forever answer to the daemon for an
// attached client. The client never unlinks the file itself: the daemon owns
// the filesystem for a viewer, and its ApproveDeletion is the one removal path.
// The mirrored entry clears only on an OK answer, so a refusal (say, the
// proposal was withdrawn meanwhile) leaves the pending surface honest and the
// reason in the status. A transport failure is the status too, like saveRemote.
func (a *App) approveDeletionRemote(d control.Deletion) {
	res, err := a.sendRemovalDecision("delete", d.Path, true)
	if err != nil {
		a.status = "attach: " + err.Error()
		return
	}
	if !res.OK {
		a.status = res.Err
		return
	}
	a.dropClientRemoval("delete", d.Path)
	delete(a.pendingDeletions, d.Path)
	a.deletionPromptPane = nil
	a.status = "approved the removal of " + filepath.Base(d.Path)
}

// withdrawDeletionRemote forwards the Withdraw answer to the daemon, retracting
// the proposal through the wire's `delete --withdraw` without touching disk. It
// sends no author: the daemon admits the client's own durable human, who may
// retract any pending removal.
func (a *App) withdrawDeletionRemote(d control.Deletion) {
	res, err := a.sendRemovalDecision("delete", d.Path, false)
	if err != nil {
		a.status = "attach: " + err.Error()
		return
	}
	if !res.OK {
		a.status = res.Err
		return
	}
	a.dropClientRemoval("delete", d.Path)
	delete(a.pendingDeletions, d.Path)
	a.deletionPromptPane = nil
	a.status = "withdrew the deletion of " + filepath.Base(d.Path)
}

// approveDirRemovalRemote is approveDeletionRemote for a directory: the Remove
// forever answer becomes rmdir --approve on the daemon.
func (a *App) approveDirRemovalRemote(d control.DirRemoval) {
	res, err := a.sendRemovalDecision("rmdir", d.Path, true)
	if err != nil {
		a.status = "attach: " + err.Error()
		return
	}
	if !res.OK {
		a.status = res.Err
		return
	}
	a.dropClientRemoval("rmdir", d.Path)
	delete(a.pendingDirRemovals, d.Path)
	a.status = "approved the removal of " + filepath.Base(d.Path)
}

// withdrawDirRemovalRemote is withdrawDeletionRemote for a directory.
func (a *App) withdrawDirRemovalRemote(d control.DirRemoval) {
	res, err := a.sendRemovalDecision("rmdir", d.Path, false)
	if err != nil {
		a.status = "attach: " + err.Error()
		return
	}
	if !res.OK {
		a.status = res.Err
		return
	}
	a.dropClientRemoval("rmdir", d.Path)
	delete(a.pendingDirRemovals, d.Path)
	a.status = "withdrew the removal of " + filepath.Base(d.Path)
}
