package app

import (
	"strings"
	"testing"

	"raj/internal/ui"
)

// tick delivers one idle tick, which is what drives a drag held still.
func tick(h *harness) {
	h.Handle(ui.Tick{})
	h.drain()
}

// longDoc is taller than any harness pane, so there is somewhere to scroll to.
func longDoc() string {
	var b strings.Builder
	for i := 0; i < 200; i++ {
		b.WriteString("line of text\n")
	}
	return b.String()
}

// The gesture this exists for: hold the pointer below the pane and the document
// scrolls under it, so a selection can exceed a screenful by pointer alone.
func TestDragBelowScrollsAndExtends(t *testing.T) {
	h := newHarness(t, longDoc())
	x, y := editorOrigin(h)
	click(h, x, y, 0)

	_, rows := h.screen.Size()
	dragTo(h, x, rows) // past the bottom of everything

	before := h.Pane().Viewport.Top
	tick(h)
	after := h.Pane().Viewport.Top
	if after <= before {
		t.Fatalf("viewport %d -> %d; holding below the pane should scroll down", before, after)
	}
	if !h.Pane().Cursors.Primary().HasSelection() {
		t.Error("the selection was not extended with the scroll")
	}
}

// And upwards, which needs the view to have somewhere above it to go.
func TestDragAboveScrollsUp(t *testing.T) {
	h := newHarness(t, longDoc())
	h.Pane().ScrollRows(50)
	h.drain()

	x, y := editorOrigin(h)
	click(h, x, y+2, 0)
	dragTo(h, x, 0) // above the text area, over the tab bar

	before := h.Pane().Viewport.Top
	tick(h)
	if after := h.Pane().Viewport.Top; after >= before {
		t.Errorf("viewport %d -> %d; holding above the pane should scroll up", before, after)
	}
}

// The selection has to keep pace with the scroll rather than staying where the
// pointer last was. A view that scrolls without extending is worse than one
// that does neither: it looks like it is working.
func TestSelectionGrowsWithEachTick(t *testing.T) {
	h := newHarness(t, longDoc())
	x, y := editorOrigin(h)
	click(h, x, y, 0)
	_, rows := h.screen.Size()
	dragTo(h, x, rows)

	tick(h)
	first := selLen(h)
	tick(h)
	second := selLen(h)
	if second <= first {
		t.Errorf("selection %d -> %d bytes; it should grow on every tick", first, second)
	}
}

// Speed is proportional to how far past the edge the pointer is held, which is
// the convention everywhere and is what makes a 150 ms tick usable: the way to
// ask for faster is to push further.
func TestFartherIsFaster(t *testing.T) {
	near := newHarness(t, longDoc())
	far := newHarness(t, longDoc())
	_, rows := near.screen.Size()

	for _, c := range []struct {
		h   *harness
		row int
	}{{near, rows}, {far, rows + 20}} {
		x, y := editorOrigin(c.h)
		click(c.h, x, y, 0)
		dragTo(c.h, x, c.row)
		tick(c.h)
	}
	if far.Pane().Viewport.Top <= near.Pane().Viewport.Top {
		t.Errorf("held further out scrolled %d rows, no more than %d held just past the edge",
			far.Pane().Viewport.Top, near.Pane().Viewport.Top)
	}
}

// Speed is capped, or flinging the pointer to the far corner would jump the
// length of the document in one tick.
func TestSpeedIsCapped(t *testing.T) {
	h := newHarness(t, longDoc())
	x, y := editorOrigin(h)
	click(h, x, y, 0)
	dragTo(h, x, 5000)

	// Measured across the tick, not from zero: the drag event itself moves the
	// cursor to the bottom edge and the view follows it, which is ordinary
	// drag behaviour and not part of what the cap governs. The tick's own step
	// is exactly the cap, which is what ExtendTo buys — following the cursor
	// there would scroll again on top of it.
	before := h.Pane().Viewport.Top
	tick(h)
	if got := h.Pane().Viewport.Top - before; got > maxAutoscrollRows {
		t.Errorf("one tick scrolled %d rows, want at most %d", got, maxAutoscrollRows)
	}
}

// A drag inside the pane does not autoscroll. Dragging is how a selection is
// made, and a view that crept while the pointer sat in the middle of the text
// would make an ordinary selection impossible to place.
func TestDragInsideDoesNotScroll(t *testing.T) {
	h := newHarness(t, longDoc())
	x, y := editorOrigin(h)
	click(h, x, y, 0)
	dragTo(h, x+3, y+2)

	before := h.Pane().Viewport.Top
	tick(h)
	if h.Pane().Viewport.Top != before {
		t.Error("a drag inside the pane scrolled the view")
	}
}

// Releasing stops it. A view that kept scrolling after the button came up
// would be a runaway with no way to stop it but clicking again.
func TestReleaseStopsScrolling(t *testing.T) {
	h := newHarness(t, longDoc())
	x, y := editorOrigin(h)
	click(h, x, y, 0)
	_, rows := h.screen.Size()
	dragTo(h, x, rows)
	tick(h)
	release(h)

	before := h.Pane().Viewport.Top
	tick(h)
	tick(h)
	if h.Pane().Viewport.Top != before {
		t.Error("the view kept scrolling after the button was released")
	}
}

// At the end of the document there is nothing to scroll to, and the ticks must
// simply stop rather than spinning or extending past the last line.
func TestStopsAtTheEndOfTheDocument(t *testing.T) {
	h := newHarness(t, "one\ntwo\nthree\n")
	x, y := editorOrigin(h)
	click(h, x, y, 0)
	_, rows := h.screen.Size()
	dragTo(h, x, rows)

	for i := 0; i < 20; i++ {
		tick(h)
	}
	if top := h.Pane().Viewport.Top; top >= h.Pane().File.Lines() {
		t.Errorf("viewport top %d is past the document's %d lines", top, h.Pane().File.Lines())
	}
	if head := h.Pane().Cursors.Primary().Head; head > h.Pane().File.Len() {
		t.Errorf("cursor %d is past the document's %d bytes", head, h.Pane().File.Len())
	}
}

// A tick with no drag in progress must not touch the view: the tick also drives
// retokenising and runs constantly.
func TestIdleTicksDoNotScroll(t *testing.T) {
	h := newHarness(t, longDoc())
	h.Pane().ScrollRows(10)
	h.drain()
	before := h.Pane().Viewport.Top
	for i := 0; i < 5; i++ {
		tick(h)
	}
	if h.Pane().Viewport.Top != before {
		t.Error("an idle tick scrolled the view")
	}
}

func selLen(h *harness) int {
	lo, hi := h.Pane().Cursors.Primary().Range()
	return hi - lo
}
