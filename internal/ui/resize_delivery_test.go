package ui

import (
	"testing"
	"time"
)

// A resize must reach the consumer even when the event channel is already full.
//
// This is the bug the coalescing buffer exists for. emit drops on a full
// channel, which is right for input — a wedged render loop must not also wedge
// the reader, or ctrl+c stops working — and wrong for resize: a drag produces a
// burst, the burst fills the channel, and the drop means no redraw until the
// 150 ms tick, so the window snaps to its new size a beat after you let go.
func resizeHost(t *testing.T, capacity int) *NativeHost {
	t.Helper()
	h := &NativeHost{
		cols: 80, rows: 24,
		events:  make(chan Event, capacity),
		resized: make(chan struct{}, 1),
		done:    make(chan struct{}),
	}
	go h.deliverResize()
	t.Cleanup(func() { close(h.done) })
	return h
}

// signal is what the SIGWINCH handler does: a non-blocking poke.
func (h *NativeHost) signal() {
	select {
	case h.resized <- struct{}{}:
	default:
	}
}

// waitResize takes events until a Resize arrives or the deadline passes.
func waitResize(t *testing.T, h *NativeHost, within time.Duration) Resize {
	t.Helper()
	deadline := time.After(within)
	for {
		select {
		case e := <-h.events:
			if r, ok := e.(Resize); ok {
				return r
			}
		case <-deadline:
			t.Fatal("no resize was delivered")
		}
	}
}

func TestResizeIsDeliveredOnAnEmptyChannel(t *testing.T) {
	h := resizeHost(t, 8)
	h.signal()

	got := waitResize(t, h, time.Second)
	if got.Cols != 80 || got.Rows != 24 {
		t.Errorf("resize = %dx%d, want 80x24", got.Cols, got.Rows)
	}
}

// The case that used to drop. The channel is full when the signal arrives, so
// the old code discarded it; now the deliverer waits and the event lands as
// soon as the consumer catches up.
func TestResizeSurvivesAFullChannel(t *testing.T) {
	h := resizeHost(t, 2)
	h.events <- Tick{}
	h.events <- Tick{}
	h.signal()

	// Nothing can be delivered yet, and nothing should be lost either.
	time.Sleep(20 * time.Millisecond)
	<-h.events // the consumer catches up by one

	if got := waitResize(t, h, time.Second); got.Cols != 80 {
		t.Errorf("resize = %dx%d, want the real size", got.Cols, got.Rows)
	}
}

// A burst coalesces. Only the latest size matters, so queueing one event per
// signal would spend the channel on a drag nobody has finished.
func TestABurstCoalesces(t *testing.T) {
	h := resizeHost(t, 8)
	for i := 0; i < 50; i++ {
		h.signal()
	}
	waitResize(t, h, time.Second)

	// At most one more can be in flight: the deliverer may have taken a second
	// token before the rest were dropped.
	time.Sleep(20 * time.Millisecond)
	extra := 0
	for {
		select {
		case e := <-h.events:
			if _, ok := e.(Resize); ok {
				extra++
			}
			continue
		default:
		}
		break
	}
	if extra > 1 {
		t.Errorf("%d further resizes queued from one burst; it should coalesce", extra)
	}
}

// A queued event can carry a size the window has already left, and the newer
// size still arrives: every further change signals again, so a fresh event is
// behind the stale one. That is what makes the staleness harmless — together
// with Present reading the true size every frame, so the event is a nudge to
// redraw rather than the source of truth for what to draw.
func TestALaterSizeStillArrives(t *testing.T) {
	h := resizeHost(t, 1)
	h.events <- Tick{} // the deliverer will block behind this
	h.signal()
	time.Sleep(20 * time.Millisecond)

	h.mu.Lock()
	h.cols, h.rows = 120, 40 // the window moved again while we waited
	h.mu.Unlock()
	h.signal()
	<-h.events // the consumer catches up

	// Whatever the first event said, the current size must be delivered too.
	deadline := time.After(time.Second)
	for {
		select {
		case e := <-h.events:
			if r, ok := e.(Resize); ok && r.Cols == 120 && r.Rows == 40 {
				return
			}
		case <-deadline:
			t.Fatal("the newer size was never delivered")
		}
	}
}

// Close releases a deliverer waiting on a consumer that will never read again,
// rather than leaking the goroutine for the life of the process.
func TestCloseReleasesABlockedDeliverer(t *testing.T) {
	h := &NativeHost{
		cols: 80, rows: 24,
		events:  make(chan Event, 1),
		resized: make(chan struct{}, 1),
		done:    make(chan struct{}),
	}
	stopped := make(chan struct{})
	go func() { h.deliverResize(); close(stopped) }()

	h.events <- Tick{} // full, so the deliverer will block on the send
	h.signal()
	time.Sleep(20 * time.Millisecond)
	close(h.done)

	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("the deliverer did not stop when the host closed")
	}
}

// Close must be safe on the zero-value hosts the tests build directly, which
// never start a deliverer and so have no done channel.
func TestCloseWithoutADelivererIsHarmless(t *testing.T) {
	h := &NativeHost{events: make(chan Event, 1)}
	h.closed = true // skip the terminal teardown, which needs a real one
	if err := h.Close(); err != nil {
		t.Errorf("Close = %v", err)
	}
}

// Input keeps its drop-rather-than-block policy. The whole reason resize needed
// its own path is that emit must never wait, or a stalled render loop takes the
// keyboard with it.
func TestEmitStillDropsRatherThanBlocking(t *testing.T) {
	h := &NativeHost{events: make(chan Event, 1)}
	h.events <- Tick{}

	done := make(chan struct{})
	go func() { h.emit(Tick{}); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("emit blocked on a full channel")
	}
}
