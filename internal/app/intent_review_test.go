package app

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"raj/internal/editor"
	"raj/internal/intent"
	"raj/internal/piecetable"
)

// reviewTabPanes returns the seam diff panes among the open tabs, keyed by the
// file label they carry.
func reviewTabPanes(t *testing.T, h *harness) map[string]*editor.Pane {
	t.Helper()
	out := map[string]*editor.Pane{}
	for _, p := range h.Tabs.All() {
		if p.File.IsReadOnly() {
			out[p.Label] = p
		}
	}
	return out
}

// A seam whose members touch two files opens one read-only tab per file, each
// holding only its own file's hunks. `intent review` did not exist before this
// change, so the call is the entry-point pin; it also fails if a file's pane is
// writable (a save succeeds) or if one file's tab shows another's hunks.
func TestIntentReviewOpensOneReadOnlyTabPerFile(t *testing.T) {
	_, h := intentDiffFixture(t)
	before := h.Tabs.Count()

	res := doIntent(t, h, intent.Command{Mode: "review", Name: "i"})
	if res.Review == nil {
		t.Fatal("intent review returned no review")
	}
	if len(res.Review.Files) != 2 {
		t.Fatalf("Review.Files = %v, want the two seam files", res.Review.Files)
	}
	if got := h.Tabs.Count(); got != before+2 {
		t.Fatalf("tabs = %d, want %d: one per seam file", got, before+2)
	}
	panes := reviewTabPanes(t, h)
	aPane, ok := panes["a.go"]
	if !ok {
		t.Fatalf("no read-only tab labelled a.go: %v", panes)
	}
	bPane, ok := panes["b.go"]
	if !ok {
		t.Fatalf("no read-only tab labelled b.go: %v", panes)
	}
	// The pane is read-only: its save is refused with the read-only reason,
	// and it is keyed on a synthetic path rather than the file it shows.
	if err := aPane.File.Save(); !errors.Is(err, editor.ErrReadOnly) {
		t.Errorf("Save on a seam diff = %v, want editor.ErrReadOnly", err)
	}
	if !strings.Contains(aPane.File.Text(), "-package a") || !strings.Contains(aPane.File.Text(), "+package a2") {
		t.Errorf("a.go tab does not hold its own hunk:\n%s", aPane.File.Text())
	}
	if strings.Contains(aPane.File.Text(), "package b2") {
		t.Errorf("a.go tab shows b.go's hunk:\n%s", aPane.File.Text())
	}
	if !strings.Contains(bPane.File.Text(), "-package b") || !strings.Contains(bPane.File.Text(), "+package b2") {
		t.Errorf("b.go tab does not hold its own hunk:\n%s", bPane.File.Text())
	}
	if strings.Contains(bPane.File.Text(), "package a2") {
		t.Errorf("b.go tab shows a.go's hunk:\n%s", bPane.File.Text())
	}
}

// A file in two seams shows only the seam being reviewed: another ACCEPTED set
// in the same file is not a member, so its text must not reach the tab. It
// fails if the composition starts from the agreed text instead of the base plus
// the seam's members -- the defect memberSlice closed -- and it fails before
// the change because the subcommand does not exist.
func TestIntentReviewTabShowsOnlyItsSeamHunks(t *testing.T) {
	_, h := intentDiffFixture(t)
	// A second set on b.go, accepted but not a member of seam i. It is landed
	// after the intention was created, exactly as another agent's accepted
	// work would arrive.
	b := paneAt(t, h, "b.go")
	other := proposeOn(t, h, "b.go", piecetable.Hunk{
		Start: b.File.Len(), End: b.File.Len(), Text: "// other seam\n",
	})
	b.File.Session().AcceptGroup(other)

	doIntent(t, h, intent.Command{Mode: "review", Name: "i"})
	panes := reviewTabPanes(t, h)
	bPane, ok := panes["b.go"]
	if !ok {
		t.Fatalf("no read-only tab labelled b.go: %v", panes)
	}
	if !strings.Contains(bPane.File.Text(), "+package b2") {
		t.Errorf("b.go tab is missing the seam hunk:\n%s", bPane.File.Text())
	}
	if strings.Contains(bPane.File.Text(), "other seam") {
		t.Errorf("b.go tab shows another seam's accepted hunk:\n%s", bPane.File.Text())
	}
}

// The seam diff tabs obey the announced-tab rule: they are the agent's view,
// so the session keeps them only while they hold pending work or unsaved text.
// They hold neither, so they do not accumulate into the restored tab set. It
// fails before the change because the subcommand does not exist, and it fails
// if the panes are not marked announced.
func TestIntentReviewTabsDoNotAccumulate(t *testing.T) {
	_, h := intentDiffFixture(t)
	before := h.SessionState().Tabs

	res := doIntent(t, h, intent.Command{Mode: "review", Name: "i"})
	if res.Review == nil || len(res.Review.Files) != 2 {
		t.Fatalf("review = %+v, want two files", res.Review)
	}
	panes := reviewTabPanes(t, h)
	if len(panes) != 2 {
		t.Fatalf("read-only tabs = %d, want 2", len(panes))
	}
	for label, p := range panes {
		if !h.announced[p.File.Path] {
			t.Errorf("seam tab %s is not marked announced, so it would outlive the review", label)
		}
		if sessionHasTab(h.SessionState(), p.File.Path) {
			t.Errorf("seam tab %s was written into the session", label)
		}
	}
	if got := len(h.SessionState().Tabs); got != len(before) {
		t.Fatalf("session tabs = %d, want the %d user tabs unchanged: %+v",
			got, len(before), h.SessionState().Tabs)
	}
}

// A save gesture on a read-only view is refused where the save path decides,
// before it can offer to create the synthetic path's parent directory. It fails
// if cmd+s reaches ensureParent: that opens a "Create directory" confirm for
// .raj-seam/<seam>, so the status is not the read-only note, a prompt is left
// open, and the directory is one keystroke from being made for a save that can
// never happen.
func TestIntentReviewSaveGestureRefusedCleanly(t *testing.T) {
	dir, h := intentDiffFixture(t)
	doIntent(t, h, intent.Command{Mode: "review", Name: "i"})
	panes := reviewTabPanes(t, h)
	p, ok := panes["a.go"]
	if !ok {
		t.Fatalf("no read-only tab labelled a.go: %v", panes)
	}
	h.Tabs.Focus(p)
	h.press("super+s")

	if got := h.Status(); got != "read-only: this is a seam diff, not the file" {
		t.Errorf("status after cmd+s on a seam pane = %q, want the read-only note", got)
	}
	if h.Prompt.Open {
		t.Errorf("cmd+s on a seam pane opened %q; the save must be refused before any parent-directory offer", h.Prompt.Title())
	}
	if _, err := os.Stat(filepath.Join(dir, ".raj-seam")); !os.IsNotExist(err) {
		t.Errorf(".raj-seam exists (%v) after a save on a read-only view; nothing may be created", err)
	}
}

// A read-only view is a local artifact of the diff pane: its synthetic
// .raj-seam path names nothing a socket client could read. `buffers` is the
// socket's view of the workspace, so it must carry only real files, and those
// byte-for-byte unchanged. It fails if host.Buffers lists the seam panes.
func TestIntentReviewBuffersHideSeamPanes(t *testing.T) {
	_, h := intentDiffFixture(t)
	before := hostOf(h.App).Buffers()
	if len(before) != 2 {
		t.Fatalf("fixture: buffers = %+v, want the two real buffers", before)
	}

	doIntent(t, h, intent.Command{Mode: "review", Name: "i"})
	after := hostOf(h.App).Buffers()
	if len(after) != len(before) {
		t.Fatalf("buffers = %d after intent review, want the %d real buffers: %+v", len(after), len(before), after)
	}
	for _, b := range after {
		if strings.Contains(b.Path, ".raj-seam") {
			t.Errorf("buffers lists the read-only view %q; a client cannot use that path", b.Path)
		}
	}
	if !reflect.DeepEqual(before, after) {
		t.Errorf("real buffers changed across intent review:\nbefore %+v\nafter  %+v", before, after)
	}
}
