package app

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"raj/internal/intent"
	"raj/internal/piecetable"
)

// intentWalkFixture commits a.go and b.go, proposes one member group on each,
// and stores an intention whose members are ordered b then a: the opposite of
// file order. It is what makes the walk order and the file order
// distinguishable, which is the whole point of the owner's first answer.
func intentWalkFixture(t *testing.T) (string, *harness) {
	t.Helper()
	dir, h := intentRoot(t)
	gA := proposeOn(t, h, "a.go", piecetable.Hunk{Start: 0, End: len("package a\n"), Text: "package a2\n"})
	gB := proposeOn(t, h, "b.go", piecetable.Hunk{Start: 0, End: len("package b\n"), Text: "package b2\n"})
	doIntent(t, h, intent.Command{Mode: "new", Name: "i", Base: "HEAD", Groups: []uint64{gB, gA}})
	return dir, h
}

// A review walks the intention's sets in the intention's own order, not in file
// order, and the answer records both orders: Sets follows the intention (b then
// a), Files is file-sorted (a then b) for a diff renderer. It fails if the walk
// re-sorts to file order. It also pins the owner's complaint: no read-only seam
// tab per file -- the walk follows files one at a time, in the real buffers.
func TestIntentReviewWalksSetsInIntentionOrder(t *testing.T) {
	dir, h := intentWalkFixture(t)
	// Make the other file active first, so moving to the first set's file is
	// the walk's doing and not a leftover from the fixture.
	h.OpenFile(filepath.Join(dir, "a.go"))

	res := doIntent(t, h, intent.Command{Mode: "review", Name: "i"})
	if res.Walk == nil {
		t.Fatal("intent review returned no walk")
	}
	if len(res.Walk.Sets) != 2 {
		t.Fatalf("Walk.Sets = %+v, want the two members", res.Walk.Sets)
	}
	if res.Walk.Sets[0].Path != "b.go" || res.Walk.Sets[1].Path != "a.go" {
		t.Errorf("walk order = %+v, want the intention's order b.go then a.go", res.Walk.Sets)
	}
	if want := []string{"a.go", "b.go"}; !reflect.DeepEqual(res.Walk.Files, want) {
		t.Errorf("Walk.Files = %v, want file order %v", res.Walk.Files, want)
	}
	if h.App.mode != ModeReview {
		t.Errorf("mode = %v, want Review", h.App.mode)
	}
	if got := h.Pane().File.Path; got != filepath.Join(dir, "b.go") {
		t.Errorf("active file = %s, want the first set's file b.go", got)
	}
	for _, p := range h.Tabs.All() {
		if p.File.IsReadOnly() {
			t.Errorf("walk opened read-only tab %q; a review must not open a tab per file", p.Label)
		}
	}
}

// Accepting the set in front advances the walk and follows it to the next set's
// file. The accepted text stays (a decision is not an edit); no set is marked
// red because nothing was rejected.
func TestIntentReviewAcceptFollowsToTheNextFile(t *testing.T) {
	dir, h := intentWalkFixture(t)
	doIntent(t, h, intent.Command{Mode: "review", Name: "i"}) // starts on b.go

	h.press("ctrl+super+m")
	if h.App.walk == nil {
		t.Fatal("accepting the last-but-one set must not end the walk")
	}
	if h.App.walk.Cursor != 1 {
		t.Fatalf("cursor = %d, want the walk advanced to 1", h.App.walk.Cursor)
	}
	if got := h.Pane().File.Path; got != filepath.Join(dir, "a.go") {
		t.Errorf("active file after accept = %s, want the next set's file a.go", got)
	}
	if got := paneAt(t, h, "b.go").File.Text(); got != "package b2\n" {
		t.Errorf("accepted b.go = %q, accepting must not change it", got)
	}
	if len(h.App.walk.red) != 0 {
		t.Errorf("red = %v, want none: nothing was rejected", h.App.walk.red)
	}
	if !strings.Contains(h.Status(), "a.go") {
		t.Errorf("status = %q, want the new position", h.Status())
	}
}

// Rejecting mid-walk keeps going, and a rejection the probe calls red is marked
// rather than stopping the walk. Reject on this review surface also takes the
// rejected set's text out, because the probe reads the composition without it:
// b.go is back to its base after the reject. The probe is injected, so the
// marking is proven without a language server.
func TestIntentReviewRejectKeepsWalkingAndMarksRed(t *testing.T) {
	dir, h := intentWalkFixture(t)
	doIntent(t, h, intent.Command{Mode: "review", Name: "i"}) // starts on b.go

	probed := ""
	h.App.walk.probe = func(paths []string) []string {
		probed = strings.Join(paths, ",")
		return paths
	}
	h.press("ctrl+super+/")

	if probed != filepath.Join(dir, "b.go") {
		t.Errorf("probe files = %q, want the rejected set's file b.go", probed)
	}
	if !h.App.walk.red[0] {
		t.Error("a rejection the probe called red must be marked on the set")
	}
	if h.App.walk.Cursor != 1 {
		t.Fatalf("cursor = %d, want the walk to keep going to 1", h.App.walk.Cursor)
	}
	if got := h.Pane().File.Path; got != filepath.Join(dir, "a.go") {
		t.Errorf("active file after reject = %s, want a.go", got)
	}
	if !strings.Contains(h.Status(), "red") {
		t.Errorf("status = %q, want the red rejection surfaced", h.Status())
	}
	if got := paneAt(t, h, "b.go").File.Text(); got != "package b\n" {
		t.Errorf("rejected b.go = %q, want the base: a walk reject takes the text out", got)
	}
}

// A rejection the probe calls clean is not marked, and the walk still advances.
func TestIntentReviewRejectCleanKeepsWalkingUnmarked(t *testing.T) {
	_, h := intentWalkFixture(t)
	doIntent(t, h, intent.Command{Mode: "review", Name: "i"})
	h.App.walk.probe = func([]string) []string { return nil }

	h.press("ctrl+super+/")
	if len(h.App.walk.red) != 0 {
		t.Errorf("red = %v, want none for a clean reject", h.App.walk.red)
	}
	if h.App.walk.Cursor != 1 {
		t.Fatalf("cursor = %d, want the walk to keep going", h.App.walk.Cursor)
	}
}

// The waiting list, while a walk runs, shows the intention first and its sets
// in the intention's order; accepting the intention row exports it. Export is
// inert (objects only), so it is the safe action the review surface can offer;
// this is the "see the intention and act on it" half.
func TestIntentWalkWaitingListShowsTheIntentionAndExports(t *testing.T) {
	_, h := intentWalkFixture(t)
	doIntent(t, h, intent.Command{Mode: "review", Name: "i"})

	h.press("ctrl+super+v")
	if !h.App.Picker.Open {
		t.Fatal("the waiting list did not open")
	}
	if len(h.App.pendingList) != 3 {
		t.Fatalf("rows = %d, want the intention plus two sets: %+v", len(h.App.pendingList), h.App.pendingList)
	}
	if h.App.pendingList[0].Kind != "intent" {
		t.Fatalf("first row = %+v, want the intention row", h.App.pendingList[0])
	}
	if h.App.pendingList[1].Kind != "walk-set" || h.App.pendingList[1].Path == "" {
		t.Errorf("second row = %+v, want the first walk set", h.App.pendingList[1])
	}

	h.press("ctrl+super+m") // accept the selected row: export the intention
	show := doIntent(t, h, intent.Command{Mode: "show", Name: "i"})
	if show.Intention == nil || show.Intention.State != intent.Exported {
		t.Fatalf("intention = %+v, want an exported state after the action row", show.Intention)
	}
	if !strings.Contains(h.Status(), "exported") {
		t.Errorf("status = %q, want the export reported", h.Status())
	}
}

// Next and previous wrap within the walk, so a review never dead-ends and a
// rejected set can be revisited.
func TestIntentReviewStepWraps(t *testing.T) {
	_, h := intentWalkFixture(t)
	doIntent(t, h, intent.Command{Mode: "review", Name: "i"}) // b.go
	h.press("ctrl+super+,")                                   // prev from set 0 wraps to the last
	if h.App.walk.Cursor != 1 {
		t.Fatalf("cursor after prev from 0 = %d, want 1 (wrap)", h.App.walk.Cursor)
	}
	h.press("ctrl+super+.") // next from the last wraps to the first
	if h.App.walk.Cursor != 0 {
		t.Fatalf("cursor after next from the last = %d, want 0 (wrap)", h.App.walk.Cursor)
	}
}

// Leaving Review ends the walk, so its chords stop intercepting accept and
// reject in Edit mode.
func TestLeavingReviewEndsTheWalk(t *testing.T) {
	_, h := intentWalkFixture(t)
	doIntent(t, h, intent.Command{Mode: "review", Name: "i"})
	h.press("super+r")
	if h.App.walk != nil {
		t.Error("leaving Review must end the walk")
	}
	if h.App.mode != ModeEdit {
		t.Errorf("mode = %v, want Edit", h.App.mode)
	}
}

// The empty and unknown refusals keep their wording, so a typo is refused by
// name rather than walked as an empty review.
func TestIntentReviewEmptyAndUnknownRefused(t *testing.T) {
	_, h := intentRoot(t)
	doIntent(t, h, intent.Command{Mode: "new", Name: "empty", Base: "HEAD"})
	if _, err := h.App.runIntent(context.Background(), intent.Command{Mode: "review", Name: "empty"}); err == nil || !strings.Contains(err.Error(), "member") {
		t.Errorf("reviewing an empty intention = %v, want a refusal naming its empty membership", err)
	}
	if _, err := h.App.runIntent(context.Background(), intent.Command{Mode: "review", Name: "missing"}); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Errorf("reviewing an unknown intention = %v, want a refusal naming it", err)
	}
}

// A walk resolving a member no live buffer numbers is refused rather than
// silently dropping the set: a review must not skip a set it cannot show.
func TestIntentReviewMissingMemberRefused(t *testing.T) {
	_, h := intentWalkFixture(t)
	// Store a member on a path no buffer holds: the walk must refuse it.
	doIntent(t, h, intent.Command{Mode: "new", Name: "ghost", Base: "HEAD",
		Members: []intent.Member{{ID: 99, Path: "missing.go"}}})
	_, err := h.App.runIntent(context.Background(), intent.Command{Mode: "review", Name: "ghost"})
	if err == nil || !strings.Contains(err.Error(), "not in any live buffer") {
		t.Errorf("reviewing an intention with a missing member = %v, want a refusal", err)
	}
}
