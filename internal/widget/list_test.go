package widget

import "testing"

// Scrolling detaches the view from the selection, and moving the selection
// reattaches it. This is the pair that makes a wheel work at all: without the
// detach, a pane that follows the selection when it draws undoes every scroll
// before it reaches the screen — which is invisible from inside the list and
// was exactly the bug.
func TestListScrollDetachesUntilTheSelectionMoves(t *testing.T) {
	l := List{Rows: 5}
	l.Settle(5, 100)

	l.Scroll(20, 100)
	if l.Top != 20 {
		t.Fatalf("Top = %d, want 20", l.Top)
	}
	if l.Sel != 0 {
		t.Errorf("Sel = %d; scrolling must not move the selection", l.Sel)
	}
	l.Settle(5, 100)
	if l.Top != 20 {
		t.Errorf("drawing pulled the view back to %d", l.Top)
	}

	// Reattaching means the selection is visible again, not that the view
	// snaps to the top: Follow scrolls the least it can, so from Top=20 with
	// Sel at 1 it lands at 1. Asserting an exact Top here would be asserting
	// Follow's step size rather than the property that matters.
	l.Move(+1, 100)
	l.Settle(5, 100)
	if l.Sel < l.Top || l.Sel >= l.Top+l.Rows {
		t.Errorf("Sel %d outside the visible rows [%d,%d)", l.Sel, l.Top, l.Top+l.Rows)
	}
	if l.Top > 20 {
		t.Errorf("Top = %d; reattaching should not scroll further away", l.Top)
	}
}

// Scroll clamps at both ends and leaves a full screen of content.
func TestListScrollClamps(t *testing.T) {
	l := List{Rows: 10}
	l.Scroll(-5, 100)
	if l.Top != 0 {
		t.Errorf("scrolled above the top to %d", l.Top)
	}
	l.Scroll(500, 100)
	if l.Top != 90 {
		t.Errorf("Top = %d, want 90 so the last 10 rows fill the pane", l.Top)
	}
}

// A list shorter than the pane has nothing to scroll.
func TestListScrollShortList(t *testing.T) {
	l := List{Rows: 20}
	l.Scroll(5, 3)
	if l.Top != 0 {
		t.Errorf("Top = %d, want 0 for a list that fits", l.Top)
	}
}

// Reset reattaches, since the contents changed wholesale and the old view is
// meaningless.
func TestListResetReattaches(t *testing.T) {
	l := List{Rows: 5}
	l.Scroll(30, 100)
	l.Reset()
	l.Settle(5, 100)
	if l.Top != 0 || l.Sel != 0 {
		t.Errorf("after reset Top=%d Sel=%d, want 0/0", l.Top, l.Sel)
	}
}
