package ui

import (
	"sync"
	"time"

	"raj/internal/safe"
)

// A daemon has no terminal, so it needs a Host shaped like one: a size, an
// events channel, a theme, and the idle tick the application timers run on.
// HeadlessHost is that host. It presents frames by discarding them — nobody is
// watching — and its only input is the tick, because a daemon is driven over
// the control socket, not by keys.
//
// It is the second production host beside NativeHost, and the reason the Host
// seam is not a test-only abstraction: the same event loop runs a terminal raj
// and a headless one.

// HeadlessCols and HeadlessRows are the size a daemon reports. The model does
// not depend on a real screen, but a generous fixed size keeps viewport and
// layout maths in the regime the terminal paths are tested in.
const (
	HeadlessCols = 120
	HeadlessRows = 40
)

// headlessTick is how often the daemon event loop wakes, matching the terminal
// host pace so the timers — compaction, disk checks, LSP sync, session save —
// run at the same cadence either way.
const headlessTick = 150 * time.Millisecond

// HeadlessHost is a Host with no terminal.
type HeadlessHost struct {
	events chan Event
	cols   int
	rows   int
	theme  Theme

	done    chan struct{}
	stopped chan struct{}
	once    sync.Once
}

// NewHeadlessHost returns a host of the default daemon size, ticking the idle
// event the way a terminal host does.
func NewHeadlessHost() *HeadlessHost {
	return newHeadlessHost(HeadlessCols, HeadlessRows, headlessTick)
}

func newHeadlessHost(cols, rows int, tick time.Duration) *HeadlessHost {
	h := &HeadlessHost{
		events:  make(chan Event, 256),
		cols:    cols,
		rows:    rows,
		theme:   Theme{Background: RGBColor(0, 0, 0), Foreground: RGBColor(0xd0, 0xd0, 0xd0), Known: true},
		done:    make(chan struct{}),
		stopped: make(chan struct{}),
	}
	safe.Go(func() { h.tick(tick) })
	return h
}

// tick emits the idle event on a fixed cadence until Close. It does not read
// input: a daemon input is the control socket.
func (h *HeadlessHost) tick(every time.Duration) {
	defer close(h.stopped)
	t := time.NewTicker(every)
	defer t.Stop()
	var count uint64
	for {
		select {
		case <-h.done:
			return
		case <-t.C:
			count++
			h.emit(Tick{Count: count})
		}
	}
}

// emit posts an event without blocking the producer. The application loop is
// the only consumer, and a full channel means it is busy; dropping a tick or a
// wake costs at most one cadence of latency, and the next tick is coming.
func (h *HeadlessHost) emit(e Event) {
	select {
	case h.events <- e:
	default:
	}
}

func (h *HeadlessHost) Events() <-chan Event { return h.events }
func (h *HeadlessHost) Size() (int, int)     { return h.cols, h.rows }
func (h *HeadlessHost) Theme() Theme         { return h.theme }

// Present discards the frame: the model still draws, but there is no terminal
// to hold the cells, and keeping them would leak one screen per frame for as
// long as the daemon runs.
func (h *HeadlessHost) Present(*Screen) error { return nil }

// Post injects an event from outside the input path, as a worker goroutine
// would. Control requests use it to wake the loop they parked on.
func (h *HeadlessHost) Post(e Event) { h.emit(e) }

// Invalidate, Repaint and Suspend are terminal-side gestures with nothing
// behind them headless.
func (h *HeadlessHost) Invalidate()         {}
func (h *HeadlessHost) Repaint()            {}
func (h *HeadlessHost) Suspend() error      { return nil }
func (h *HeadlessHost) SetClipboard(string) {}

// Close stops the tick and closes the event channel, which ends the event loop
// range. It is safe to call twice.
func (h *HeadlessHost) Close() error {
	h.once.Do(func() {
		close(h.done)
		<-h.stopped
		close(h.events)
	})
	return nil
}

// HeadlessHost implements the host seam.
var _ Host = (*HeadlessHost)(nil)
