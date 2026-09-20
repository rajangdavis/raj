package probe

import (
	"strings"
	"testing"
	"time"

	"raj/internal/keys"
)

// A swipe is classified from a bare-motion run: it must clear minSwipeCells and
// its dominant axis must match the asked direction. Runs come from mouseTrace,
// which records no-button motion only, so a drag can never satisfy one.
func TestMatchSwipe(t *testing.T) {
	run := func(x0, y0, x1, y1, n int) mouseRun {
		return mouseRun{
			From: keys.Mouse{Col: x0, Row: y0},
			To:   keys.Mouse{Col: x1, Row: y1},
			N:    n,
			Dur:  time.Duration(n) * 10 * time.Millisecond,
		}
	}
	runs := []mouseRun{
		run(10, 5, 4, 5, 6), // 6 cells left
		run(1, 1, 2, 2, 2),  // 1 cell, under the threshold
		run(3, 3, 3, 12, 9), // 9 cells down
	}
	if got, ok := matchSwipe(runs, "left"); !ok || runCells(got) != 6 {
		t.Errorf("matchSwipe left = (%+v, %v), want the 6-cell run", got, ok)
	}
	if got, ok := matchSwipe(runs, "down"); !ok || runCells(got) != 9 {
		t.Errorf("matchSwipe down = (%+v, %v), want the 9-cell run", got, ok)
	}
	if _, ok := matchSwipe(runs, "right"); ok {
		t.Error("matchSwipe found a right swipe where there is none")
	}
	if _, ok := matchSwipe([]mouseRun{run(0, 0, 2, 2, 3)}, "right"); ok {
		t.Error("a 2-cell run must not count as a swipe")
	}
}

// A tap is a press and release on the exact same cell, held under tapMaxHold.
func TestClassifyTap(t *testing.T) {
	base := time.Unix(1000, 0)
	cell := keys.Mouse{Col: 7, Row: 3}
	pair := func(down, up keys.Mouse, held time.Duration) buttonPair {
		return buttonPair{down: down, up: up, downAt: base, upAt: base.Add(held)}
	}
	if ok, detail := classifyTap([]buttonPair{pair(cell, cell, 80*time.Millisecond)}, tapMaxHold); !ok || detail != "(7,3) down+up 80 ms" {
		t.Errorf("tap = (%v, %q), want seen 80 ms", ok, detail)
	}
	if ok, _ := classifyTap([]buttonPair{pair(cell, keys.Mouse{Col: 8, Row: 3}, 80*time.Millisecond)}, tapMaxHold); ok {
		t.Error("a release on another cell must not be a tap")
	}
	if ok, _ := classifyTap([]buttonPair{pair(cell, cell, 500*time.Millisecond)}, tapMaxHold); ok {
		t.Error("a 500 ms hold must not be a tap")
	}
	if ok, _ := classifyTap(nil, tapMaxHold); ok {
		t.Error("no pair must not be a tap")
	}
}

// A double-tap is two taps whose cells are within doubleTapSlop and whose
// second press lands within doubleTapWindow of the first release.
func TestClassifyDoubleTap(t *testing.T) {
	base := time.Unix(1000, 0)
	cell := keys.Mouse{Col: 4, Row: 4}
	pair := func(down keys.Mouse, downAt time.Time) buttonPair {
		return buttonPair{down: down, up: down, downAt: downAt, upAt: downAt.Add(80 * time.Millisecond)}
	}
	first := pair(cell, base)
	second := pair(keys.Mouse{Col: 5, Row: 4}, base.Add(280*time.Millisecond)) // 200 ms after the first release
	if ok, detail := classifyDoubleTap([]buttonPair{first, second}, doubleTapWindow, doubleTapSlop, tapMaxHold); !ok || !strings.Contains(detail, "200 ms apart") {
		t.Errorf("double tap = (%v, %q), want seen 200 ms apart", ok, detail)
	}
	slow := pair(cell, base.Add(600*time.Millisecond))
	if ok, _ := classifyDoubleTap([]buttonPair{first, slow}, doubleTapWindow, doubleTapSlop, tapMaxHold); ok {
		t.Error("taps 520 ms apart must not be a double-tap")
	}
	far := pair(keys.Mouse{Col: 12, Row: 4}, base.Add(200*time.Millisecond))
	if ok, _ := classifyDoubleTap([]buttonPair{first, far}, doubleTapWindow, doubleTapSlop, tapMaxHold); ok {
		t.Error("taps on distant cells must not be a double-tap")
	}
	if ok, _ := classifyDoubleTap([]buttonPair{first}, doubleTapWindow, doubleTapSlop, tapMaxHold); ok {
		t.Error("one tap must not be a double-tap")
	}
}

// A long-press is one cell held past longPressMin without moving; a drag is not
// a long-press even when it was held a long time.
func TestClassifyLongPress(t *testing.T) {
	base := time.Unix(1000, 0)
	cell := keys.Mouse{Col: 4, Row: 9}
	pair := func(held time.Duration, samples int) buttonPair {
		return buttonPair{down: cell, up: cell, downAt: base, upAt: base.Add(held), samples: samples}
	}
	if ok, detail := classifyLongPress([]buttonPair{pair(900*time.Millisecond, 0)}, longPressMin); !ok || !strings.Contains(detail, "held 900 ms") {
		t.Errorf("long press = (%v, %q), want seen held 900 ms", ok, detail)
	}
	if ok, _ := classifyLongPress([]buttonPair{pair(300*time.Millisecond, 0)}, longPressMin); ok {
		t.Error("a 300 ms hold must not be a long-press")
	}
	if ok, _ := classifyLongPress([]buttonPair{pair(900*time.Millisecond, 3)}, longPressMin); ok {
		t.Error("a hold that moved must not be a long-press")
	}
}

// A drag is a press with button-motion samples and a net displacement clearing
// minDragCells; a press that never moved is not a drag.
func TestClassifyDrag(t *testing.T) {
	base := time.Unix(1000, 0)
	down := keys.Mouse{Col: 10, Row: 6}
	pair := buttonPair{down: down, up: keys.Mouse{Col: 4, Row: 6}, downAt: base, upAt: base.Add(300 * time.Millisecond), samples: 5}
	if ok, detail := classifyDrag([]buttonPair{pair}, minDragCells); !ok || !strings.Contains(detail, "left drag") || !strings.Contains(detail, "6 cells") {
		t.Errorf("drag = (%v, %q), want a left drag of 6 cells", ok, detail)
	}
	still := pair
	still.samples = 0
	if ok, _ := classifyDrag([]buttonPair{still}, minDragCells); ok {
		t.Error("a press with no motion samples must not be a drag")
	}
	tiny := buttonPair{down: down, up: keys.Mouse{Col: 11, Row: 6}, downAt: base, upAt: base.Add(50 * time.Millisecond), samples: 1}
	if ok, _ := classifyDrag([]buttonPair{tiny}, minDragCells); ok {
		t.Error("a one-cell move must not be a drag")
	}
}

// A two-finger tap usually arrives on a button; the classifier names the button
// so the report says exactly what the terminal chose.
func TestClassifyTwoFingerTap(t *testing.T) {
	base := time.Unix(1000, 0)
	cell := keys.Mouse{Col: 3, Row: 3, Button: keys.MouseRight}
	p := buttonPair{down: cell, up: cell, downAt: base, upAt: base.Add(90 * time.Millisecond)}
	if ok, detail := classifyTwoFingerTap([]buttonPair{p}, tapMaxHold); !ok || !strings.Contains(detail, "right click") {
		t.Errorf("two-finger tap = (%v, %q), want a right click", ok, detail)
	}
	if ok, _ := classifyTwoFingerTap(nil, tapMaxHold); ok {
		t.Error("no pair must not be a two-finger tap")
	}
}

// Two-finger motion is read from wheel notches, including horizontal ones, and
// the direction maps to the expected notch.
func TestCountWheel(t *testing.T) {
	wheels := []keys.MouseButton{keys.WheelUp, keys.WheelUp, keys.WheelDown, keys.WheelLeft}
	if n := countWheel(wheels, keys.WheelUp); n != 2 {
		t.Errorf("wheel-up = %d, want 2", n)
	}
	if n := countWheel(wheels, keys.WheelDown); n != 1 {
		t.Errorf("wheel-down = %d, want 1", n)
	}
	if n := countWheel(wheels, keys.WheelLeft); n != 1 {
		t.Errorf("wheel-left = %d, want 1", n)
	}
	if n := countWheel(wheels, keys.WheelRight); n != 0 {
		t.Errorf("wheel-right = %d, want 0", n)
	}
	if b := scrollButton("left"); b != keys.WheelLeft {
		t.Errorf("scrollButton(left) = %v, want WheelLeft", b)
	}
}

// The report is the paste-back artifact, so each step is one line: seen with
// its detail, or NOT DELIVERED.
func TestMotionsReport(t *testing.T) {
	got := motionsReport([]motionResult{
		{label: "one-finger swipe left", seen: true, detail: "left, 12 cells, 9 samples, 96 ms"},
		{label: "three-finger swipe right"},
	})
	for _, want := range []string{
		"--- MOTIONS ---",
		"one-finger swipe left",
		"seen: left, 12 cells, 9 samples, 96 ms",
		"three-finger swipe right",
		"NOT DELIVERED",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report missing %q:\n%s", want, got)
		}
	}
	if got := motionsReport(nil); got != "" {
		t.Errorf("empty report = %q, want empty", got)
	}
}

// A gesture observed during a step is classified when ctrl+n advances, and the
// observation resets so one gesture cannot leak into the next prompt.
func TestMotionSessionAdvance(t *testing.T) {
	m := newMotionSession()
	base := time.Unix(1000, 0)
	at := func(ms int) time.Time { return base.Add(time.Duration(ms) * time.Millisecond) }
	bare := func(col, ms int) {
		m.observe(keys.Event{
			Kind: keys.MouseEvent,
			Mouse: keys.Mouse{
				Button: keys.MouseNone, Col: col, Row: 5,
				Press: true, Motion: true,
			},
			Raw: []byte("\x1b[<35;1;1M"),
		}, at(ms))
	}
	bare(20, 0)
	bare(10, 10)
	if m.advance() {
		t.Fatal("advancing the first of several steps must not finish the session")
	}
	got := m.res[0]
	if !got.seen || !strings.Contains(got.detail, "left") {
		t.Errorf("first step = %+v, want a left swipe", got)
	}
	if m.obs.pairs != nil || m.obs.wheels != nil || m.obs.isDown {
		t.Error("observation leaked into the next step")
	}
}

// A two-finger swipe is read from wheel notches first; a terminal that sends
// bare motion instead is recorded as that, because the difference is the point.
func TestMotionScrollResult(t *testing.T) {
	m := newMotionSession()
	m.steps = m.steps[4:6] // two-finger swipe left, then right
	m.idx = 0
	base := time.Unix(1000, 0)

	m.observe(keys.Event{
		Kind:  keys.MouseEvent,
		Mouse: keys.Mouse{IsWheel: true, Button: keys.WheelLeft, Press: true},
	}, base)
	if got := m.result(); !got.seen || !strings.Contains(got.detail, "wheel-left x1") {
		t.Errorf("two-finger left = %+v, want wheel-left x1", got)
	}
	m.advance()

	m.observe(keys.Event{
		Kind:  keys.MouseEvent,
		Mouse: keys.Mouse{Button: keys.MouseNone, Col: 1, Row: 1, Press: true, Motion: true},
	}, base)
	m.observe(keys.Event{
		Kind:  keys.MouseEvent,
		Mouse: keys.Mouse{Button: keys.MouseNone, Col: 9, Row: 1, Press: true, Motion: true},
	}, base.Add(20*time.Millisecond))
	if got := m.result(); !got.seen || !strings.Contains(got.detail, "arrived as bare motion") {
		t.Errorf("two-finger right = %+v, want the bare-motion fallback", got)
	}
}
