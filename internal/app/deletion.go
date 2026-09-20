package app

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"raj/internal/control"
	"raj/internal/editor"
	"raj/internal/piecetable"
	"raj/internal/session"
)

// The deletion gate: an agent proposes, the human disposes.
//
// An edit is reviewable as a text diff, so an agent's apply lands in the
// document and waits. A deletion has no such representation -- there is
// nothing to read that says what is about to be gone -- so `delete` records a
// pending path instead, and this file is the prompt that turns that record
// into either an unlink the human asked for or a proposal that comes back the
// next time the file is focused. The folder verbs are a later pass.

// The two answers the deletion prompt offers. Remove forever is offered only
// when the buffer is clean of both unsaved text and undecided change sets;
// there is deliberately no force path.
const (
	ignoreForNow    = "Ignore for now"
	removeForever   = "Remove forever"
	withdrawRemoval = "Withdraw"
)

// deletionSafe reports whether p can be removed without discarding work the
// human or an agent has not committed, and the sentence that says why not.
//
// The rule is the spec's: no unsaved changes (ViewDirty) and no change sets
// still awaiting a decision (Session.Pending). Either one is work that exists
// nowhere else, and discarding it to satisfy a deletion is the failure the
// gate exists to prevent.
func deletionSafe(p *editor.Pane) (bool, string) {
	if p == nil || p.File == nil {
		return true, ""
	}
	pending := len(p.File.Session().Pending())
	switch {
	case p.File.ViewDirty() && pending > 0:
		return false, "Unsaved and " + pendingSets(pending) + "."
	case p.File.ViewDirty():
		return false, "Unsaved changes."
	case pending > 0:
		return false, pendingSets(pending) + "."
	}
	return true, ""
}

// pendingSets names a count of undecided change sets for the refusal line. The
// prompt wraps the message now, so the phrase survives a long file name; the
// short wording is kept because a refusal reads better as a sentence than as a
// paragraph.
func pendingSets(n int) string {
	if n == 1 {
		return "1 pending set"
	}
	return fmt.Sprintf("%d pending sets", n)
}

// deletionProposer names who asked, preferring the participant table's name
// over a bare number. A name is what the user needs to answer "who wants this
// gone"; the numeric id is the honest fallback when no registry is up, as in a
// test harness or a session nobody drove.
func (a *App) deletionProposer(author uint8) string {
	if a.control != nil && a.control.Participants != nil {
		if p, ok := a.control.Participants.Get(author); ok && p.Name != "" {
			return p.Name
		}
	}
	kind := "author"
	if piecetable.Author(author).IsAgent() {
		kind = "agent"
	}
	return fmt.Sprintf("%s %d", kind, author)
}

// openDeletionPane is the tab showing path, if there is one. A headless buffer
// is not "open" in the sense the gate cares about: nothing on screen shows it,
// so the prompt waits for an open or a focus rather than appearing over a
// different file.
func (a *App) openDeletionPane(path string) *editor.Pane {
	for _, p := range a.Tabs.All() {
		if sameFile(path, p.File.Path) {
			return p
		}
	}
	return nil
}

// maybePromptDeletion raises the gate for the active pane when its path has a
// pending deletion. It is called after every event and after an open, so a
// focus change is enough to bring the question back, and the tracked pane is
// what keeps it from being asked twice for one focus. A question already on
// screen is never interrupted; the check runs again once it closes.
func (a *App) maybePromptDeletion() {
	if a.Prompt.Open {
		return
	}
	p := a.Tabs.Active()
	if p == a.deletionPromptPane {
		return
	}
	a.deletionPromptPane = p
	if p == nil || p.File == nil || p.File.Path == "" {
		return
	}
	d, ok := a.pendingDeletionFor(p.File.Path)
	if !ok {
		return
	}
	a.promptDeletion(p, d)
}

// pendingDeletionFor finds the pending deletion for a path. The exact key is
// the common case -- ProposeDeletion and the tabs key on the same canonical
// name -- and the sameFile scan absorbs a symlink spelling that slipped
// through one of them.
func (a *App) pendingDeletionFor(path string) (control.Deletion, bool) {
	if d, ok := a.pendingDeletions[path]; ok {
		return d, true
	}
	for key, d := range a.pendingDeletions {
		if sameFile(key, path) {
			return d, true
		}
	}
	return control.Deletion{}, false
}

// promptDeletion asks the human what to do about a pending deletion. Ignore is
// always offered and is the default; Remove forever appears only when
// deletionSafe says the buffer holds nothing that exists nowhere else. When it
// does not, the message says what is in the way, and the pending change sets
// are listed so the reason is concrete rather than a count.
func (a *App) promptDeletion(p *editor.Pane, d control.Deletion) {
	msg := fmt.Sprintf("%s proposed deleting %s.", a.deletionProposer(d.Author), p.File.Name())
	options := []string{ignoreForNow, withdrawRemoval}
	if safe, why := deletionSafe(p); safe {
		options = []string{ignoreForNow, removeForever, withdrawRemoval}
	} else {
		msg += " " + why
	}
	done := func(answer string, ok bool) {
		// Ignore for now -- and a dismissed dialog, which means the same
		// thing -- leaves the proposal pending. The file works normally and
		// the question returns on the next focus. Withdraw retracts the
		// proposal; Remove forever is the only answer that touches the file.
		switch {
		case !ok, answer == ignoreForNow:
			return
		case answer == removeForever:
			a.removeDeleted(p, d.Path)
		case answer == withdrawRemoval:
			a.withdrawDeletion(d)
		}
	}
	if pending := p.File.Session().Pending(); len(pending) > 0 {
		rows, lines := a.reviewRows(p, pending)
		a.beforePrompt()
		a.Prompt.Review("Delete file", msg, rows, options, func(row int) {
			if row < len(lines) && lines[row] > 0 {
				a.jumpTo(lines[row])
			}
		}, done)
		return
	}
	a.confirm("Delete file", msg, options, done)
}

// trashDir is where RAJ_TRASH=1 sends a removed file: a trash/ directory in
// the workspace's XDG state dir, alongside the store and the op logs.
func (a *App) trashDir() string {
	if a.root == "" {
		return ""
	}
	return filepath.Join(session.StateDir(a.root), "trash")
}

// moveToTrash moves path into the workspace trash under a timestamped name
// rather than unlinking it, so a RAJ_TRASH=1 removal can be recovered by hand.
// The name keeps the original basename so the directory reads by eye and
// carries a UTC nanosecond stamp so removing the same name twice does not
// clobber the earlier copy.
//
// The trash lives in the XDG state dir now, which is commonly a different
// filesystem from the workspace, so this goes through moveOrCopy rather than a
// bare rename. On failure the file is left where it is, the caller reports the
// error and keeps the proposal, and no bytes are lost.
func (a *App) moveToTrash(path string) error {
	dir := a.trashDir()
	if dir == "" {
		return errors.New("no workspace root")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	name := filepath.Base(path) + "." + time.Now().UTC().Format("20060102T150405.000000000")
	return moveOrCopy(path, filepath.Join(dir, name))
}

// moveOrCopy moves src to dst, falling back to a copy when a rename cannot do
// the job. A bare rename is preferred: it is atomic and costs nothing. It
// cannot cross a filesystem boundary, though, and the trash lives under
// $XDG_STATE_HOME, which is commonly a different mount from the workspace, so
// a rename there fails with EXDEV.
//
// The fallback is deliberately broader than EXDEV alone: any rename failure
// except a destination that already exists is retried as a copy. A copy that
// cannot succeed reports its own error and leaves the source untouched, so the
// retry can lose nothing; a destination collision is the one failure a copy
// must not paper over, because it would clobber bytes that are not ours.
func moveOrCopy(src, dst string) error {
	err := os.Rename(src, dst)
	if err == nil {
		return nil
	}
	if errors.Is(err, os.ErrExist) {
		return err
	}
	if errors.Is(err, syscall.EXDEV) {
		return copyThenRemove(src, dst)
	}
	// Not a collision and not reported as cross-device: retry as a copy all
	// the same, and let the copy report the real failure if it cannot.
	return copyThenRemove(src, dst)
}

// copyThenRemove copies src's bytes and permission bits to dst, creating the
// destination's parent directories, and removes src only after the copy has
// been written and closed. A failure at any point leaves src in place and
// removes a partial dst, so a caller that keeps the source on error never
// loses bytes.
func copyThenRemove(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if info.IsDir() {
		// The fallback copies a single file's contents; a directory still needs
		// a rename, so report it rather than writing a half-destination.
		return fmt.Errorf("moveOrCopy: %s is a directory", src)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(dst)
		return err
	}
	if err := os.Chmod(dst, info.Mode().Perm()); err != nil {
		os.Remove(dst)
		return err
	}
	return os.Remove(src)
}

// removeFile is the disk half of a removal. RAJ_TRASH=1 and only that value
// moves the file into the workspace trash; any other value, or unset, is the
// ordinary unlink the editor has always done.
func (a *App) removeFile(path string) error {
	if os.Getenv("RAJ_TRASH") == "1" {
		return a.moveToTrash(path)
	}
	return os.Remove(path)
}

// removeDeleted carries out a Remove forever answer: the file leaves its place
// on disk -- unlinked, or moved to the trash under RAJ_TRASH=1 -- the buffer is
// dropped the way any close drops one, the proposal is cleared, and the tree is
// refreshed so the name is gone from it too.
//
// A failed removal keeps the proposal rather than dropping a buffer whose file
// is still there; the next focus asks again.
func (a *App) removeDeleted(p *editor.Pane, path string) {
	if path == "" && p != nil && p.File != nil {
		path = p.File.Path
	}
	if err := a.removeFile(path); err != nil && !os.IsNotExist(err) {
		a.status = "cannot remove " + filepath.Base(path) + ": " + err.Error()
		return
	}
	a.closeDeletedPane(p)
	delete(a.pendingDeletions, path)
	a.clearPendingRemoval(path, false)
	a.deletionPromptPane = nil
	a.Explorer.Tree.Refresh()
	a.status = "removed " + filepath.Base(path)
	a.TouchSession()
}

// closeDeletedPane drops the buffer: closeDoc forgets the journal, the LSP
// document and the diagnostics, and removing the tab (or the headless entry)
// is what makes the pane unreachable. It mirrors Close and CloseDiscard, which
// is the pair that already knows how a buffer goes away in both cases.
func (a *App) closeDeletedPane(p *editor.Pane) {
	if p == nil {
		return
	}
	if a.dropHeadless(p) {
		return
	}
	for i, q := range a.Tabs.All() {
		if q == p {
			a.rememberPosition(p)
			a.closeDoc(p)
			a.Tabs.CloseIndex(i)
			return
		}
	}
}
