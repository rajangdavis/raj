package app

import (
	"context"
	"fmt"
	"path/filepath"

	"raj/internal/editor"
	"raj/internal/git"
	"raj/internal/intent"
)

// intentReview opens the named intention's seam as tabs in the running editor:
// one read-only tab per file the seam changes, each holding that file's diff
// for this seam alone. It is strand 3.1-3.3 -- the seam is an intention for
// now, and the review agent's grouping plugs in later.
//
// The diff is the same slice `intent diff` prints: the intention materialised
// alone over its base, so no other seam's hunks and none of the workspace's
// other state can appear. It writes nothing: no ref, index or worktree moves.
func (a *App) intentReview(ctx context.Context, svc *git.Service, set intent.Set, cmd intent.Command) (intent.Result, error) {
	in, ok := set[cmd.Name]
	if !ok {
		return intent.Result{}, fmt.Errorf("intent review: no such intention %q", cmd.Name)
	}
	if len(in.Members) == 0 {
		return intent.Result{}, fmt.Errorf("intent review: intention %q has no members, so there is no seam to open", cmd.Name)
	}
	m, err := a.materialiseIntention(ctx, svc, set, in)
	if err != nil {
		return intent.Result{}, err
	}
	defer m.Remove()
	// git's own numstat names exactly the files the seam changes, in order.
	// Each file's text is then read through the same object-only diff scoped
	// to that path, so a pane holds one file's slice without parsing the whole
	// patch.
	_, numstat, err := svc.DiffTrees(ctx, m.base, m.tree)
	if err != nil {
		return intent.Result{}, err
	}
	files := make([]string, 0, len(numstat))
	for _, e := range numstat {
		text, err := svc.DiffTreesPath(ctx, m.base, m.tree, e.Path)
		if err != nil {
			return intent.Result{}, fmt.Errorf("intent review: %s: %w", e.Path, err)
		}
		a.openSeamDiff(in.Name, e.Path, text)
		files = append(files, e.Path)
	}
	a.status = fmt.Sprintf("seam %s: opened %d file(s)", in.Name, len(files))
	return intent.Result{Review: &intent.Review{Name: in.Name, Base: m.base, Files: files}}, nil
}

// openSeamDiff shows one file's slice of a seam in a read-only tab. rel is the
// file's workspace-relative path -- the tab's label -- and text is that file's
// unified diff for this seam alone.
//
// The pane is keyed on a synthetic path rather than rel, because a second pane
// carrying rel would shadow the real buffer in every path-keyed lookup: find
// and paneFor return the first match, and intentProjection composes each
// touched path from the pane's session, so a diff pane at rel would have its
// diff text materialised as the file's content. The synthetic path keeps the
// diff pane out of all of them, and Pane.Label is what makes the tab read as
// the file.
func (a *App) openSeamDiff(seam, rel, text string) {
	// The name carries no language extension, so the pane is plain text
	// everywhere: no highlighter colours the code inside the diff, and no
	// language server is asked about a file that does not exist.
	vpath := filepath.Join(".raj-seam", seam, filepath.FromSlash(rel)+".seamdiff")
	// A second review of the same seam refreshes the tab rather than stacking
	// a duplicate. The pane holds no decisions, so replacing the document in
	// place leaves the display projection the identity it already is.
	if old := a.paneFor(vpath); old != nil {
		old.File = editor.NewReadOnlyFile(vpath, text, a.tabWidth)
		old.Cursors.Set(0, 0)
		return
	}
	p := editor.NewPane(editor.NewReadOnlyFile(vpath, text, a.tabWidth))
	p.Label = rel
	a.announceQuiet(p)
}
