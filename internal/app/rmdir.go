package app

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"raj/internal/control"
	"raj/internal/editor"
)

// rmdir: the deletion gate widened from one file to a directory. The propose-
// then-review discipline, the pending-state shape and the prompt-gate question
// are the same as delete (see deletion.go); what differs is the unit -- a
// subtree whose contents are offered whole -- and the approval surface, which
// lists every path at stake rather than naming one file. A directory has no
// pane to focus, so the gate raises immediately on proposal instead of waiting
// for a focus change that never comes.

// dirRemovalPaths lists every path under dir, one per row, in stable sorted
// order. It is computed at raise time for the review listing so it names what
// is actually there, and sorted so the reading order does not depend on the
// order the filesystem hands directory entries back in.
//
// The rows are the absolute paths the removal would take. The listing wraps
// now, so a long path is read whole across rows rather than cut mid-word; the
// relative spelling this used to return was a workaround for the old
// one-truncated-line prompt and is no longer needed.
func dirRemovalPaths(dir string) []string {
	var paths []string
	filepath.WalkDir(dir, func(path string, _ os.DirEntry, err error) error {
		if err != nil || path == dir {
			// The directory itself is the unit being removed, not a row in
			// the listing; naming its contents is what the review is for.
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	sort.Strings(paths)
	return paths
}

// underDir reports whether path names something inside dir. filepath.Rel gives
// the answer directly: "." or a child spelling is inside, ".." or a path that
// starts with it is outside.
func underDir(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// panesUnder returns every open pane -- a tab or a headless buffer -- whose
// file sits inside dir. It is the subtree the safety check and the removal
// both have to reach, and it returns a fresh slice so closing panes while the
// caller walks it cannot disturb the loop.
func (a *App) panesUnder(dir string) []*editor.Pane {
	var out []*editor.Pane
	for _, p := range a.Tabs.All() {
		if p.File != nil && underDir(dir, p.File.Path) {
			out = append(out, p)
		}
	}
	for _, p := range a.headless {
		if p.File != nil && underDir(dir, p.File.Path) {
			out = append(out, p)
		}
	}
	return out
}

// dirRemovalSafe reports whether dir can be removed without discarding work
// the human or an agent has not committed, and the sentence that says why not.
// The rule is the same one the file gate uses, widened from one buffer to the
// subtree: every open buffer inside the directory must pass deletionSafe, and
// the first one that fails is the reason the whole removal fails. An empty
// directory has no buffers, so it passes vacuously.
func (a *App) dirRemovalSafe(dir string) (bool, string) {
	for _, p := range a.panesUnder(dir) {
		if safe, why := deletionSafe(p); !safe {
			return false, filepath.Base(p.File.Path) + ": " + why
		}
	}
	return true, ""
}

// removeDir is the disk half of a dir removal. RAJ_TRASH=1 and only that value
// renames the whole directory into the workspace trash under a timestamped
// name; any other value, or unset, is os.RemoveAll.
func (a *App) removeDir(path string) error {
	if os.Getenv("RAJ_TRASH") == "1" {
		return a.moveToTrash(path)
	}
	return os.RemoveAll(path)
}

// promptDirRemoval asks the human what to do about a pending dir-removal.
// Ignore is always offered and is the default; Remove forever appears only when
// dirRemovalSafe says the subtree holds nothing that exists nowhere else. When
// it does not, the message says what is in the way -- which buffer, and which
// unsaved text or undecided change set -- so the reason is shown with the
// question rather than as a status line after the answer. The listing names
// every path the removal would take, computed now so it matches what is on disk
// at the moment of asking.
func (a *App) promptDirRemoval(d control.DirRemoval) {
	rows := dirRemovalPaths(d.Path)
	msg := fmt.Sprintf("%s proposed removing %s.",
		a.deletionProposer(d.Author), filepath.Base(d.Path))
	options := []string{ignoreForNow}
	if safe, why := a.dirRemovalSafe(d.Path); safe {
		options = []string{ignoreForNow, removeForever}
	} else {
		msg += " " + why
	}
	done := func(answer string, ok bool) {
		if !ok || answer != removeForever {
			return
		}
		a.removeDirDeleted(d)
	}
	a.beforePrompt()
	a.Prompt.Review("Remove directory", msg, rows, options, nil, done)
}

// removeDirDeleted carries out a Remove forever answer: the directory leaves
// its place on disk -- RemoveAll-ed, or moved to the trash under RAJ_TRASH=1 --
// every open buffer inside it is dropped, the proposal is cleared, and the tree
// is refreshed so the names are gone from it too.
//
// A failed removal keeps the proposal rather than dropping buffers whose files
// are still there; the next proposal asks again.
func (a *App) removeDirDeleted(d control.DirRemoval) {
	if err := a.removeDir(d.Path); err != nil && !os.IsNotExist(err) {
		a.status = "cannot remove " + filepath.Base(d.Path) + ": " + err.Error()
		return
	}
	for _, p := range a.panesUnder(d.Path) {
		a.closeDeletedPane(p)
	}
	delete(a.pendingDirRemovals, d.Path)
	a.Explorer.Tree.Refresh()
	a.status = "removed " + filepath.Base(d.Path)
	a.TouchSession()
}
