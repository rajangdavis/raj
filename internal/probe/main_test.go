package probe

import (
	"strings"
	"testing"
	"time"

	"raj/internal/keys"
)

// mouseLine is the whole user-visible contract of the mouse readout, so the
// table covers every event kind the decoder produces: a press, bare motion, a
// drag, a release and a wheel notch. Each case asserts the fields a user reads
// a swipe by, including the time and delta that only appear on later events.
func TestMouseLine(t *testing.T) {
	prev := keys.Mouse{Button: keys.MouseLeft, Col: 10, Row: 5, Press: true}
	cases := []struct {
		name     string
		m        keys.Mouse
		raw      []byte
		prev     keys.Mouse
		havePrev bool
		dt       time.Duration
		want     []string
	}{
		{
			name: "press",
			m:    keys.Mouse{Button: keys.MouseLeft, Col: 4, Row: 2, Press: true},
			raw:  []byte("\x1b[<0;5;3M"),
			want: []string{"MOUSE press", "btn=left", "wheel=no", "col=4", "row=2", "first", `\e[<0;5;3M`},
		},
		{
			name:     "bare motion",
			m:        keys.Mouse{Button: keys.MouseNone, Col: 13, Row: 4, Press: true, Motion: true},
			raw:      []byte("\x1b[<35;14;5M"),
			prev:     prev,
			havePrev: true,
			dt:       34 * time.Millisecond,
			want:     []string{"MOUSE motion", "btn=none", "wheel=no", "col=13", "row=4", "dt=34ms d=(+3,-1)", `\e[<35;14;5M`},
		},
		{
			name:     "drag",
			m:        keys.Mouse{Button: keys.MouseLeft, Col: 9, Row: 9, Press: true, Motion: true},
			raw:      []byte("\x1b[<32;10;10M"),
			prev:     prev,
			havePrev: true,
			dt:       8 * time.Millisecond,
			want:     []string{"MOUSE motion", "btn=left", "wheel=no", "dt=8ms d=(-1,+4)"},
		},
		{
			name:     "release",
			m:        keys.Mouse{Button: keys.MouseLeft, Col: 4, Row: 2},
			raw:      []byte("\x1b[<0;5;3m"),
			prev:     prev,
			havePrev: true,
			dt:       12 * time.Millisecond,
			want:     []string{"MOUSE release", "btn=left", "wheel=no", "dt=12ms d=(-6,-3)", `\e[<0;5;3m`},
		},
		{
			name:     "wheel",
			m:        keys.Mouse{Button: keys.WheelUp, Col: 1, Row: 1, Press: true, IsWheel: true},
			raw:      []byte("\x1b[<64;2;2M"),
			prev:     prev,
			havePrev: true,
			dt:       3 * time.Millisecond,
			want:     []string{"MOUSE press", "btn=wheel-up", "wheel=yes", "dt=3ms d=(-9,-4)", `\e[<64;2;2M`},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			line := mouseLine(c.m, c.raw, c.prev, c.havePrev, c.dt)
			for _, want := range c.want {
				if !strings.Contains(line, want) {
					t.Errorf("mouseLine = %q\nmissing %q", line, want)
				}
			}
		})
	}
}

// The delta and the elapsed time come from the trace's previous event, not from
// the formatter's caller, so this pins the state that feeds them.
func TestMouseTraceTiming(t *testing.T) {
	tr := &mouseTrace{}
	t0 := time.Unix(1000, 0)
	first := tr.note(keys.Mouse{Button: keys.MouseLeft, Col: 1, Row: 1, Press: true}, nil, t0)
	if !strings.Contains(first, "first") {
		t.Errorf("first event should have no delta: %q", first)
	}
	second := tr.note(keys.Mouse{Button: keys.MouseNone, Col: 6, Row: 1, Press: true, Motion: true}, nil, t0.Add(40*time.Millisecond))
	if !strings.Contains(second, "dt=40ms d=(+5,+0)") {
		t.Errorf("delta against the previous event is wrong: %q", second)
	}
	if tr.events != 2 {
		t.Errorf("events = %d, want 2", tr.events)
	}
}

// A run is a maximal no-button motion stream. A press between two swipes must
// split them, and a swipe still under way when the user quits must not be lost.
func TestMouseTraceBareMotionRuns(t *testing.T) {
	tr := &mouseTrace{}
	base := time.Unix(1000, 0)
	at := func(ms int) time.Time { return base.Add(time.Duration(ms) * time.Millisecond) }
	bare := func(col int) keys.Mouse {
		return keys.Mouse{Button: keys.MouseNone, Col: col, Press: true, Motion: true}
	}

	tr.note(keys.Mouse{Button: keys.MouseLeft, Col: 0, Row: 0, Press: true}, nil, at(0))
	tr.note(bare(1), nil, at(10))
	tr.note(bare(2), nil, at(20))
	tr.note(bare(3), nil, at(30))
	tr.note(keys.Mouse{Button: keys.MouseLeft, Col: 3, Row: 0, Press: true}, nil, at(40))
	tr.note(bare(3), nil, at(50)) // never ended before quit

	runs := tr.allRuns()
	if len(runs) != 2 {
		t.Fatalf("got %d runs, want 2", len(runs))
	}
	if runs[0].From.Col != 1 || runs[0].To.Col != 3 || runs[0].N != 3 {
		t.Errorf("first run = %+v, want cols 1..3 in 3 samples", runs[0])
	}
	if runs[0].Dur != 20*time.Millisecond {
		t.Errorf("first run duration = %v, want 20ms", runs[0].Dur)
	}
	if runs[1].N != 1 || runs[1].From.Col != 3 || runs[1].From.Col != runs[1].To.Col {
		t.Errorf("open run = %+v, want one sample at col 3", runs[1])
	}
}

// The summary reports the gesture that moved furthest, not the one with the
// most samples: a hand resting in place must not outrank a real swipe.
func TestMouseSummaryPicksFurthestRun(t *testing.T) {
	runs := []mouseRun{
		{From: keys.Mouse{Col: 5, Row: 5}, To: keys.Mouse{Col: 6, Row: 5}, N: 20, Dur: 500 * time.Millisecond},
		{From: keys.Mouse{Col: 1, Row: 1}, To: keys.Mouse{Col: 31, Row: 2}, N: 4, Dur: 30 * time.Millisecond},
	}
	got := mouseSummary(7, runs)
	for _, want := range []string{"7 event(s)", "swipe: right", "30 cells", "30 ms", "4 samples", "d=(+30,+1)"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary missing %q:\n%s", want, got)
		}
	}
}

// A tie on displacement goes to the longer gesture, so the pick is stable.
func TestPickRunTieBreak(t *testing.T) {
	short := mouseRun{From: keys.Mouse{}, To: keys.Mouse{Col: 10}, N: 2, Dur: 5 * time.Millisecond}
	long := mouseRun{From: keys.Mouse{}, To: keys.Mouse{Col: 10}, N: 9, Dur: 90 * time.Millisecond}
	if got, _ := pickRun([]mouseRun{short, long}); got.N != 9 {
		t.Errorf("pickRun chose N=%d, want the longer-duration run", got.N)
	}
}

// With no mouse events at all there is nothing to say, and the quit path relies
// on that to print the summary unconditionally.
func TestMouseSummaryWithoutMouse(t *testing.T) {
	if got := mouseSummary(0, nil); got != "" {
		t.Errorf("summary with no events = %q, want empty", got)
	}
	if got := mouseSummary(2, nil); !strings.Contains(got, "no bare") {
		t.Errorf("summary without bare motion should say so:\n%s", got)
	}
}
