// Package probe verifies empirically what a terminal delivers for each chord
// raj owns, and whether it hands those chords back on suspend and focus loss.
//
// It runs on the same internal/keys and internal/term packages raj uses, so a
// passing probe is evidence about raj rather than about a parallel decoder. It
// is terminal-agnostic: what arrives depends on the terminal's own bindings and
// on whether it speaks the Kitty protocol, which is exactly what it is for
// finding out.
package probe

import (
	"fmt"
	"os"
	"strings"
	"time"

	"raj/internal/keys"
	"raj/internal/term"
)

// Run starts the probe. checklist walks every binding in order and prints a
// measured keymap at the end; otherwise it reports each event as it arrives.
// flags is the KKP flag set to push; 0 uses raj's own.
func Run(flags int, checklist, motions bool) error {
	if flags == 0 {
		flags = term.DefaultFlags
	}
	if motions && checklist {
		fmt.Print("--- NOTE: --motions and --checklist both set; --motions wins, the checklist is skipped.\r\n")
	}
	return run(flags, checklist && !motions, motions)
}

func run(flags int, useList, useMotions bool) error {
	t := term.New(os.Stdin, os.Stdout)
	// -flags 1 is a deliberate test of the gate, so bypass Enter's guard.
	if flags&term.FlagReportAll == 0 {
		fmt.Printf("NOTE: flags %d omit report_all; cmd chords should stay with the terminal.\r\n", flags)
	}
	if err := enter(t, flags); err != nil {
		return err
	}
	defer t.Leave()
	defer t.HandleFatalSignals()()

	banner(flags)
	query(t)

	var list *checklist
	if useList {
		list = newChecklist()
		list.prompt()
	}
	var mo *motionSession
	if useMotions {
		mo = newMotionSession()
		mo.prompt()
	}

	km := keys.NewKeymap()
	mt := &mouseTrace{}
	var buf []byte
	chunk := make([]byte, 1024)
	for {
		n, err := t.Read(chunk)
		if n > 0 {
			buf = append(buf, chunk[:n]...)
		}
		if err != nil {
			return nil
		}
		for {
			e, used := keys.Parse(buf)
			if used == 0 {
				break
			}
			buf = buf[used:]
			if handle(t, e, km, list, mt, mo) {
				// Leave the alt screen first: the deferred Leave would
				// otherwise erase the summary printed on it.
				t.Leave()
				if list != nil {
					fmt.Print(list.report())
				}
				if mo != nil {
					fmt.Print(mo.report())
				}
				fmt.Print(mouseSummary(mt.events, mt.allRuns()))
				return nil
			}
		}
	}
}

// enter pushes flags directly when report_all is absent, so the gate itself can
// be tested. Everywhere else raj goes through Terminal.Enter's guard.
func enter(t *term.Terminal, flags int) error {
	if flags&term.FlagReportAll != 0 {
		return t.Enter(flags)
	}
	if err := t.Enter(term.DefaultFlags); err != nil {
		return err
	}
	fmt.Printf("\x1b[<u\x1b[>%du", flags)
	return nil
}

func query(t *term.Terminal) {
	fmt.Print(term.QueryKKPFlags)
	t.QueryTheme(4)
	fmt.Print("\x1b[c")
}

func banner(flags int) {
	fmt.Printf("keyprobe — KKP flags pushed: %d (report_all=%v)\r\n", flags, flags&term.FlagReportAll != 0)
	fmt.Print("ctrl+z suspends (tests the handoff back to the terminal),\r\n")
	fmt.Print("ctrl+c quits, ctrl+r re-queries state.\r\n")
	fmt.Print("--- MOUSE: no-button motion is a swipe/hover; held-button motion is a drag.\r\n")
	fmt.Print("    Swipe in your terminal app. Each event prints type, button, wheel,\r\n")
	fmt.Print("    col/row, dt and d=(dx,dy). ctrl+c prints a summary if any mouse events arrived.\r\n")
	fmt.Print(strings.Repeat("-", 72) + "\r\n")
}

// mouseTrace carries the previous mouse event so each line can report the
// delta from it and the time since it, and remembers the bare-motion runs for
// the closing summary. handle is otherwise stateless; run makes one per run.
type mouseTrace struct {
	last   keys.Mouse
	lastAt time.Time
	events int

	inRun    bool
	runFrom  keys.Mouse
	runTo    keys.Mouse
	runStart time.Time
	runEnd   time.Time
	runN     int
	runs     []mouseRun
}

// mouseRun is a maximal sequence of no-button motion events: a swipe or a
// hover. From and To are its first and last positions, N its sample count.
type mouseRun struct {
	From keys.Mouse
	To   keys.Mouse
	N    int
	Dur  time.Duration
}

// note formats one mouse event against the previous one and folds it into the
// run bookkeeping. now is passed in so the timing is testable without a clock.
func (tr *mouseTrace) note(m keys.Mouse, raw []byte, now time.Time) string {
	line := mouseLine(m, raw, tr.last, tr.events > 0, now.Sub(tr.lastAt))
	tr.track(m, now)
	tr.last, tr.lastAt = m, now
	tr.events++
	return line
}

// track extends or ends the bare-motion run the event belongs to. A button
// press, a release, a wheel notch and a drag all end it.
func (tr *mouseTrace) track(m keys.Mouse, now time.Time) {
	if m.Motion && !m.IsWheel && m.Button == keys.MouseNone {
		if !tr.inRun {
			tr.inRun = true
			tr.runFrom, tr.runStart, tr.runN = m, now, 0
		}
		tr.runTo, tr.runEnd, tr.runN = m, now, tr.runN+1
		return
	}
	tr.finishRun()
}

// allRuns returns every bare-motion run seen, closing one still in progress so
// its final position counts. It is called once, on quit.
func (tr *mouseTrace) allRuns() []mouseRun {
	tr.finishRun()
	return tr.runs
}

func (tr *mouseTrace) finishRun() {
	if !tr.inRun {
		return
	}
	tr.inRun = false
	tr.runs = append(tr.runs, mouseRun{
		From: tr.runFrom,
		To:   tr.runTo,
		N:    tr.runN,
		Dur:  tr.runEnd.Sub(tr.runStart),
	})
}

// mouseLine renders one mouse event. prev and dt describe the event before it;
// havePrev is false for the first, which has nothing to diff against. It is
// pure so the format and the delta/time maths are testable without a tty.
func mouseLine(m keys.Mouse, raw []byte, prev keys.Mouse, havePrev bool, dt time.Duration) string {
	kind := "press"
	switch {
	case !m.Press:
		kind = "release"
	case m.Motion:
		kind = "motion"
	}
	wheel := "no"
	if m.IsWheel {
		wheel = "yes"
	}
	when := "first"
	if havePrev {
		when = fmt.Sprintf("dt=%dms d=(%+d,%+d)", dt.Milliseconds(), m.Col-prev.Col, m.Row-prev.Row)
	}
	return fmt.Sprintf("    MOUSE %-7s btn=%-9s wheel=%-3s col=%-4d row=%-4d %-22s %s",
		kind, mouseButtonName(m.Button), wheel, m.Col, m.Row, when, keys.Escape(raw))
}

// mouseButtonName names the button, including the four wheel directions that
// share the field. A no-button report is the bare-motion/hover case.
func mouseButtonName(b keys.MouseButton) string {
	switch b {
	case keys.MouseLeft:
		return "left"
	case keys.MouseMiddle:
		return "middle"
	case keys.MouseRight:
		return "right"
	case keys.MouseNone:
		return "none"
	case keys.WheelUp:
		return "wheel-up"
	case keys.WheelDown:
		return "wheel-down"
	case keys.WheelLeft:
		return "wheel-left"
	case keys.WheelRight:
		return "wheel-right"
	}
	return fmt.Sprintf("button(%d)", int(b))
}

// mouseSummary renders the closing report: the event count and the net
// displacement, duration and direction of the longest bare-motion run. It
// returns "" when no mouse events arrived, so the quit path can print it
// unconditionally.
func mouseSummary(events int, runs []mouseRun) string {
	if events == 0 {
		return ""
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "--- MOUSE SUMMARY: %d event(s) seen\r\n", events)
	best, ok := pickRun(runs)
	if !ok {
		sb.WriteString("    no bare (no-button) motion: a swipe needs 1003 motion reports\r\n")
		return sb.String()
	}
	dx, dy := best.To.Col-best.From.Col, best.To.Row-best.From.Row
	fmt.Fprintf(&sb, "    swipe: %s, %d cells in %d ms (%d samples, (%d,%d)->(%d,%d) d=(%+d,%+d))\r\n",
		mouseDir(dx, dy), runCells(best), best.Dur.Milliseconds(), best.N,
		best.From.Col, best.From.Row, best.To.Col, best.To.Row, dx, dy)
	return sb.String()
}

// pickRun returns the run with the greatest net displacement, the longer
// duration breaking a tie, so the summary reports the gesture that moved
// furthest rather than a hand resting in place. ok is false when there is none.
func pickRun(runs []mouseRun) (mouseRun, bool) {
	var best mouseRun
	ok := false
	for _, r := range runs {
		if !ok || runCells(r) > runCells(best) ||
			(runCells(r) == runCells(best) && r.Dur > best.Dur) {
			best, ok = r, true
		}
	}
	return best, ok
}

// runCells is a run's net displacement in cells along its dominant axis, so a
// mostly-one-axis swipe reads as a single number rather than a diagonal.
func runCells(r mouseRun) int {
	dx, dy := mouseAbs(r.To.Col-r.From.Col), mouseAbs(r.To.Row-r.From.Row)
	if dx > dy {
		return dx
	}
	return dy
}

// mouseDir names the dominant axis of a displacement, so a swipe reads as a
// direction rather than as two numbers. Rows grow downward. A run that returned
// to where it started is stationary.
func mouseDir(dx, dy int) string {
	switch {
	case dx == 0 && dy == 0:
		return "stationary"
	case mouseAbs(dx) >= mouseAbs(dy):
		if dx > 0 {
			return "right"
		}
		return "left"
	default:
		if dy > 0 {
			return "down"
		}
		return "up"
	}
}

func mouseAbs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// handle prints one event and applies the control chords. Returns true to quit.
func handle(t *term.Terminal, e keys.Event, km *keys.Keymap, list *checklist, mt *mouseTrace, mo *motionSession) bool {
	switch e.Kind {
	case keys.FocusIn:
		fmt.Print("--- FOCUS IN  (raj should re-take the bindings here)\r\n")
		return false
	case keys.FocusOut:
		fmt.Print("--- FOCUS OUT (the terminal should own the bindings here)\r\n")
		return false
	case keys.KKPFlags, keys.OSCReply, keys.OtherReply:
		fmt.Printf("    reply %s\r\n", keys.Escape(e.Raw))
		return false
	case keys.PasteEvent:
		// Worth reporting on its own line: a terminal that has not honoured
		// mode 2004 shows the payload here as a stream of key events instead,
		// which is the whole difference and is otherwise invisible.
		fmt.Printf("--- PASTE %d bytes, %d line(s)\r\n",
			len(e.Text), strings.Count(e.Text, "\n")+1)
		return false
	case keys.MouseEvent:
		now := time.Now()
		if mo != nil {
			mo.observe(e, now)
		}
		fmt.Printf("%s\r\n", mt.note(e.Mouse, e.Raw, now))
		return false
	case keys.Partial:
		return false
	}
	// Filter releases FIRST. Under flag 2 every chord reports press and
	// release, so a control chord checked before this fires twice.
	if e.Type == keys.Release {
		if list == nil && mo == nil {
			fmt.Printf("    %-18s %-24s [RELEASE — raj must ignore]\r\n", keys.Escape(e.Raw), e.Chord())
		}
		return false
	}
	if e.IsModifierKey() {
		return false
	}

	switch action := km.Lookup(keys.Global, e.Chord()); {
	case action == keys.Quit:
		return true
	case action == keys.Suspend:
		suspend(t)
		return false
	case e.Chord() == "ctrl+n" && mo != nil:
		return mo.advance()
	case e.Chord() == "ctrl+n" && list != nil:
		list.skip()
		return false
	case e.Chord() == "ctrl+r":
		query(t)
		return false
	}

	if list != nil {
		list.record(e)
		return false
	}
	action := km.Lookup(keys.Global, e.Chord())
	label := "unmapped"
	if action != keys.None {
		label = string(action)
	} else if txt := e.Insertable(); txt != "" {
		label = fmt.Sprintf("text %q", txt)
	}
	fmt.Printf("    %-18s %-24s %-8s %s\r\n",
		keys.Escape(e.Raw), e.Chord(), keys.TypeName(e.Type), label)
	return false
}

func suspend(t *term.Terminal) {
	fmt.Print("\r\n--- suspending: KKP popped, the terminal owns the keys.\r\n")
	fmt.Print("--- Try cmd+w now, then `fg` to come back.\r\n")
	err := t.Suspend(func() {
		fmt.Print("\r\n--- resumed: KKP re-pushed, raj owns the keys again.\r\n")
	})
	if err != nil {
		fmt.Printf("--- suspend failed: %v\r\n", err)
		return
	}
	query(t)
}
