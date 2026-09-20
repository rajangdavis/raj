package probe

import (
	"fmt"
	"strings"
	"time"

	"raj/internal/keys"
)

// Motions: an interactive segment that measures what a terminal actually
// delivers for touch (phone) and trackpad (Mac) gestures. Both arrive as the
// same 1002/1003/1006 mouse reports, so one probe covers both platforms. It is
// a diagnostic, not a keybinding change: nothing here alters what raj binds.
//
// Thresholds are heuristics from the wire, not promises. A swipe has to clear a
// few cells along one axis, a tap is a quick down and up on one cell, a
// double-tap is two of those in the OS's own window, and a long-press holds
// past a beat.
const (
	minSwipeCells = 3
	minDragCells  = 2
	tapMaxHold    = 400 * time.Millisecond
	longPressMin  = 600 * time.Millisecond
	// doubleTapWindow is the gap from the first release to the second press;
	// 450 ms sits between the X11 (400 ms) and Windows (500 ms) defaults.
	doubleTapWindow = 450 * time.Millisecond
	// doubleTapSlop is how far the second tap may land from the first, in
	// cells: a finger never returns to the exact same cell.
	doubleTapSlop = 2
)

// motionKind is the classifier a step is measured with.
type motionKind int

const (
	motionSwipe        motionKind = iota // one-finger bare-motion run
	motionScroll                         // two-finger: wheel notches, or bare motion
	motionTwoFingerTap                   // often delivered as a right-click
	motionPinch                          // not representable in the mouse protocol
	motionTap
	motionDoubleTap
	motionLongPress
	motionDrag
)

// motionStep is one prompt. dir is the expected direction for the swipe and
// scroll kinds; note is an extra line that sets expectations.
type motionStep struct {
	kind  motionKind
	label string
	dir   string
	note  string
}

// motionResult is one measured step: what we asked for and what came back.
type motionResult struct {
	label  string
	seen   bool
	detail string
}

// buttonPair is one press and its release, the unit a tap, double-tap,
// long-press or drag is built from. samples counts the button-motion reports
// between down and up.
type buttonPair struct {
	down    keys.Mouse
	up      keys.Mouse
	downAt  time.Time
	upAt    time.Time
	samples int
}

func (p buttonPair) held() time.Duration { return p.upAt.Sub(p.downAt) }

// sameCell reports a release on the exact cell of its press.
func (p buttonPair) sameCell() bool {
	return p.down.Col == p.up.Col && p.down.Row == p.up.Row
}

// motionObs is the observation for the step in progress. It is reset at each
// advance, so one gesture cannot leak into the next prompt.
type motionObs struct {
	trace mouseTrace

	down    keys.Mouse
	downAt  time.Time
	isDown  bool
	samples int

	pairs  []buttonPair
	wheels []keys.MouseButton
}

// motionSession walks the steps in order. One per run; no package state.
type motionSession struct {
	steps []motionStep
	idx   int
	res   []motionResult
	obs   motionObs
}

// newMotionSession lists the gestures in the order the user will be asked for
// them. The vocabulary is deliberately wider than one platform: touch and
// trackpad both land here, and a gesture the OS claims is as useful to record
// as one that arrives.
func newMotionSession() *motionSession {
	m := &motionSession{}
	add := func(kind motionKind, label, dir, note string) {
		m.steps = append(m.steps, motionStep{kind: kind, label: label, dir: dir, note: note})
	}
	add(motionSwipe, "one-finger swipe left", "left", "")
	add(motionSwipe, "one-finger swipe right", "right", "")
	add(motionSwipe, "one-finger swipe up", "up", "")
	add(motionSwipe, "one-finger swipe down", "down", "")
	add(motionScroll, "two-finger swipe left", "left", "expect wheel notches; bare motion is also a finding")
	add(motionScroll, "two-finger swipe right", "right", "expect wheel notches; bare motion is also a finding")
	add(motionScroll, "two-finger swipe up", "up", "expect wheel-up notches")
	add(motionScroll, "two-finger swipe down", "down", "expect wheel-down notches")
	add(motionTwoFingerTap, "two-finger tap", "", "often arrives as a right-click; record what it is")
	add(motionSwipe, "three-finger swipe left", "left", "the OS usually claims this; NOT DELIVERED is expected")
	add(motionSwipe, "three-finger swipe right", "right", "the OS usually claims this; NOT DELIVERED is expected")
	add(motionPinch, "pinch (out or in)", "", "not representable in the mouse protocol; NOT DELIVERED is expected")
	add(motionTap, "tap", "", "")
	add(motionDoubleTap, "double-tap", "", "two taps within the OS window")
	add(motionLongPress, "long-press", "", "")
	add(motionDrag, "long-press-drag", "", "press, drag, release")
	add(motionSwipe, "edge swipe left", "left", "from the screen edge; the OS may claim it")
	add(motionSwipe, "edge swipe right", "right", "from the screen edge; the OS may claim it")
	return m
}

func (m *motionSession) done() bool { return m.idx >= len(m.steps) }

// prompt prints the next action. ctrl+n is the advance/skip key, the same
// convention the checklist uses, and ctrl+c quits at any point.
func (m *motionSession) prompt() {
	if m.idx == 0 {
		fmt.Print("\r\n--- MOTIONS: touch (phone) and trackpad (Mac) gestures, over the same mouse reports.\r\n")
		fmt.Print("    Perform each prompt, then ctrl+n. NOT DELIVERED is a useful result: the OS or terminal claimed it.\r\n")
	}
	if m.done() {
		fmt.Print("\r\n--- motions complete. ctrl+c prints the report.\r\n")
		return
	}
	s := m.steps[m.idx]
	fmt.Printf("\r\n[%d/%d] perform: %-26s [ctrl+n next, ctrl+c quit]\r\n", m.idx+1, len(m.steps), s.label)
	if s.note != "" {
		fmt.Printf("        note: %s\r\n", s.note)
	}
}

// observe folds one mouse event into the step in progress. Key events are not
// measured: the segment is motions only, and ctrl+n/ctrl+c stay the probe's own.
func (m *motionSession) observe(e keys.Event, now time.Time) {
	if m.done() || e.Kind != keys.MouseEvent {
		return
	}
	o := &m.obs
	em := e.Mouse
	switch {
	case em.IsWheel:
		o.wheels = append(o.wheels, em.Button)
	case em.Button == keys.MouseNone:
		// Bare motion: a swipe or hover. mouseTrace keeps the runs.
	case !em.Press:
		if o.isDown {
			o.pairs = append(o.pairs, buttonPair{
				down: o.down, up: em,
				downAt: o.downAt, upAt: now, samples: o.samples,
			})
			o.isDown = false
		}
	case em.Motion:
		// A held button moving: a drag sample, or a drag whose press was
		// missed arriving as the first sample.
		if !o.isDown {
			o.isDown, o.down, o.downAt, o.samples = true, em, now, 0
		}
		o.samples++
	default:
		// The initial button press.
		o.isDown, o.down, o.downAt, o.samples = true, em, now, 0
	}
	o.trace.track(em, now)
}

// result classifies the step in progress. It is pure in the session's
// observation, so tests can drive it without a terminal.
func (m *motionSession) result() motionResult {
	if m.done() {
		return motionResult{}
	}
	s := m.steps[m.idx]
	r := motionResult{label: s.label}
	o := &m.obs
	switch s.kind {
	case motionSwipe:
		if run, ok := matchSwipe(o.trace.allRuns(), s.dir); ok {
			r.seen, r.detail = true, swipeDetail(run)
		}
	case motionScroll:
		if n := countWheel(o.wheels, scrollButton(s.dir)); n > 0 {
			r.seen, r.detail = true, fmt.Sprintf("wheel-%s x%d", s.dir, n)
		} else if run, ok := matchSwipe(o.trace.allRuns(), s.dir); ok {
			r.seen, r.detail = true, "arrived as bare motion: "+swipeDetail(run)
		}
	case motionTwoFingerTap:
		if ok, detail := classifyTwoFingerTap(o.pairs, tapMaxHold); ok {
			r.seen, r.detail = true, detail
		}
	case motionPinch:
		if detail, ok := pinchArrived(o); ok {
			r.seen, r.detail = true, detail
		}
	case motionTap:
		if ok, detail := classifyTap(o.pairs, tapMaxHold); ok {
			r.seen, r.detail = true, detail
		}
	case motionDoubleTap:
		if ok, detail := classifyDoubleTap(o.pairs, doubleTapWindow, doubleTapSlop, tapMaxHold); ok {
			r.seen, r.detail = true, detail
		}
	case motionLongPress:
		if ok, detail := classifyLongPress(o.pairs, longPressMin); ok {
			r.seen, r.detail = true, detail
		}
	case motionDrag:
		if ok, detail := classifyDrag(o.pairs, minDragCells); ok {
			r.seen, r.detail = true, detail
		}
	}
	return r
}

// swipeDetail renders one bare-motion run as a swipe reading.
func swipeDetail(run mouseRun) string {
	return fmt.Sprintf("%s, %d cells, %d samples, %d ms",
		mouseDir(run.To.Col-run.From.Col, run.To.Row-run.From.Row),
		runCells(run), run.N, run.Dur.Milliseconds())
}

// advance records the step in progress and prompts the next. It returns true
// when the last step has been recorded, which the run loop treats as a quit.
func (m *motionSession) advance() bool {
	if m.done() {
		return true
	}
	m.res = append(m.res, m.result())
	m.idx++
	m.obs = motionObs{}
	if m.done() {
		return true
	}
	m.prompt()
	return false
}

// report renders the paste-ready summary. A step still in progress when the
// user quits is classified too, so an early ctrl+c loses nothing.
func (m *motionSession) report() string {
	res := m.res
	if !m.done() {
		res = append(append([]motionResult(nil), res...), m.result())
	}
	return motionsReport(res)
}

// motionsReport renders one line per step: what arrived with its detail, or
// NOT DELIVERED.
func motionsReport(res []motionResult) string {
	if len(res) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n--- MOTIONS ---\n")
	for _, r := range res {
		if r.seen {
			fmt.Fprintf(&sb, "%-26s seen: %s\n", r.label, r.detail)
		} else {
			fmt.Fprintf(&sb, "%-26s NOT DELIVERED\n", r.label)
		}
	}
	return sb.String()
}

// pinchArrived reports whatever a pinch delivered, if anything. The mouse
// protocol cannot represent a pinch, so "nothing" is the expected answer; the
// point is to name it and to flag a terminal that synthesised something.
func pinchArrived(o *motionObs) (string, bool) {
	if len(o.wheels) > 0 {
		return fmt.Sprintf("unexpected %s x%d", mouseButtonName(o.wheels[0]), len(o.wheels)), true
	}
	if run, ok := pickRun(o.trace.allRuns()); ok {
		return fmt.Sprintf("unexpected bare motion, %d cells, %d samples", runCells(run), run.N), true
	}
	if len(o.pairs) > 0 {
		p := o.pairs[len(o.pairs)-1]
		return fmt.Sprintf("unexpected %s click at (%d,%d)", mouseButtonName(p.down.Button), p.down.Col, p.down.Row), true
	}
	return "", false
}

// matchSwipe returns the largest bare-motion run that clears minSwipeCells and
// travels in want ("left", "right", "up" or "down"). Runs come from mouseTrace,
// which records no-button motion only, so a drag can never satisfy a swipe.
func matchSwipe(runs []mouseRun, want string) (mouseRun, bool) {
	var best mouseRun
	ok := false
	for _, r := range runs {
		if runCells(r) < minSwipeCells {
			continue
		}
		if mouseDir(r.To.Col-r.From.Col, r.To.Row-r.From.Row) != want {
			continue
		}
		if !ok || runCells(r) > runCells(best) {
			best, ok = r, true
		}
	}
	return best, ok
}

// scrollButton maps a motion direction to the wheel notch it expects.
func scrollButton(dir string) keys.MouseButton {
	switch dir {
	case "left":
		return keys.WheelLeft
	case "right":
		return keys.WheelRight
	case "up":
		return keys.WheelUp
	case "down":
		return keys.WheelDown
	}
	return keys.MouseNone
}

// countWheel counts wheel notches in want.
func countWheel(wheels []keys.MouseButton, want keys.MouseButton) int {
	n := 0
	for _, b := range wheels {
		if b == want {
			n++
		}
	}
	return n
}

// classifyTap reports a tap: a release on the same cell as the press, held for
// less than maxHold. A press without its release is not a tap.
func classifyTap(pairs []buttonPair, maxHold time.Duration) (bool, string) {
	if len(pairs) == 0 {
		return false, ""
	}
	p := pairs[len(pairs)-1]
	if !p.sameCell() || p.held() >= maxHold {
		return false, ""
	}
	return true, fmt.Sprintf("(%d,%d) down+up %d ms", p.down.Col, p.down.Row, p.held().Milliseconds())
}

// classifyDoubleTap reports two taps whose down cells are within slop and whose
// second press follows the first release within window.
func classifyDoubleTap(pairs []buttonPair, window time.Duration, slop int, maxHold time.Duration) (bool, string) {
	if len(pairs) < 2 {
		return false, ""
	}
	a, b := pairs[len(pairs)-2], pairs[len(pairs)-1]
	if !a.sameCell() || !b.sameCell() || a.held() >= maxHold || b.held() >= maxHold {
		return false, ""
	}
	if mouseAbs(a.down.Col-b.down.Col) > slop || mouseAbs(a.down.Row-b.down.Row) > slop {
		return false, ""
	}
	gap := b.downAt.Sub(a.upAt)
	if gap < 0 || gap > window {
		return false, ""
	}
	return true, fmt.Sprintf("(%d,%d)+(%d,%d), %d ms apart",
		a.down.Col, a.down.Row, b.down.Col, b.down.Row, gap.Milliseconds())
}

// classifyLongPress reports one cell held for at least minHold with no drag.
func classifyLongPress(pairs []buttonPair, minHold time.Duration) (bool, string) {
	if len(pairs) == 0 {
		return false, ""
	}
	p := pairs[len(pairs)-1]
	if p.samples > 0 || !p.sameCell() {
		return false, ""
	}
	if p.held() < minHold {
		return false, ""
	}
	return true, fmt.Sprintf("(%d,%d) held %d ms", p.down.Col, p.down.Row, p.held().Milliseconds())
}

// classifyDrag reports a press that moved before release: at least one
// button-motion sample and a net displacement clearing minCells.
func classifyDrag(pairs []buttonPair, minCells int) (bool, string) {
	if len(pairs) == 0 {
		return false, ""
	}
	p := pairs[len(pairs)-1]
	if p.samples == 0 {
		return false, ""
	}
	dx, dy := p.up.Col-p.down.Col, p.up.Row-p.down.Row
	if mouseAbs(dx) < minCells && mouseAbs(dy) < minCells {
		return false, ""
	}
	run := mouseRun{From: p.down, To: p.up}
	return true, fmt.Sprintf("%s drag, %d cells, %d samples, %d ms",
		mouseDir(dx, dy), runCells(run), p.samples, p.held().Milliseconds())
}

// classifyTwoFingerTap reports a tap that arrived on a button rather than as
// bare motion. It names the button because that is the finding: a two-finger
// tap usually shows up as a right-click, but the terminal may choose another.
func classifyTwoFingerTap(pairs []buttonPair, maxHold time.Duration) (bool, string) {
	if len(pairs) == 0 {
		return false, ""
	}
	p := pairs[len(pairs)-1]
	if !p.sameCell() || p.held() >= maxHold {
		return false, ""
	}
	return true, fmt.Sprintf("%s click at (%d,%d), %d ms",
		mouseButtonName(p.down.Button), p.down.Col, p.down.Row, p.held().Milliseconds())
}
