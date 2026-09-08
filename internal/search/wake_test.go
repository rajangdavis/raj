package search

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A search that finishes while nobody is typing has to say so. Installing the
// result is event-thread work, so without a nudge the pane sat on a finished
// answer until the 150 ms tick happened to come round — visible as a pause
// between stopping typing and the results appearing.
func TestFinishedSearchNotifies(t *testing.T) {
	p := NewPane(t.TempDir())
	p.Debounce = time.Nanosecond

	woke := make(chan struct{}, 4)
	p.Notify = func() { woke <- struct{}{} }
	release := make(chan struct{})
	p.search = func(context.Context, string, Query) Result {
		<-release
		return Result{Files: 2}
	}

	typeQuery(p, "needle")
	select {
	case <-woke:
		t.Fatal("notified before the search finished")
	case <-time.After(20 * time.Millisecond):
	}

	close(release)
	select {
	case <-woke:
	case <-time.After(2 * time.Second):
		t.Fatal("a finished search never notified; the result would wait for a tick")
	}

	// The notification is only worth anything if the result is already parked
	// when it arrives — a wake that lands before the answer does is a wasted
	// pass, and the pane would still be waiting on the tick.
	p.apply()
	if p.Result.Files != 2 {
		t.Errorf("result not installed after the wake: files = %d", p.Result.Files)
	}
}

// A superseded search must stay quiet. Its result is dropped, so waking the
// loop for it would be a repaint that changes nothing — and on a large tree the
// abandoned walks outnumber the ones that count.
func TestSupersededSearchDoesNotNotify(t *testing.T) {
	p := NewPane(t.TempDir())
	p.Debounce = time.Nanosecond

	var mu sync.Mutex
	var wakes int
	p.Notify = func() { mu.Lock(); wakes++; mu.Unlock() }

	var calls int32
	gate := make(chan struct{})
	p.search = func(ctx context.Context, _ string, _ Query) Result {
		if atomic.AddInt32(&calls, 1) == 1 {
			// The first walk is still running when the next keystroke lands,
			// so it is cancelled and its answer must be thrown away.
			<-gate
			return Result{Files: 1, Stopped: ctx.Err() != nil}
		}
		return Result{Files: 9}
	}

	typeQuery(p, "a")
	waitFor(t, "the first walk to start", func() bool { return atomic.LoadInt32(&calls) == 1 })
	typeQuery(p, "b")
	close(gate)
	p.Settle(2 * time.Second)

	mu.Lock()
	got := wakes
	mu.Unlock()
	if got != 1 {
		t.Errorf("woke the loop %d times for one surviving result, want 1", got)
	}
	if p.Result.Files != 9 {
		t.Errorf("installed the abandoned result: files = %d, want 9", p.Result.Files)
	}
}

// waitFor polls a condition until it holds. The deadline is a backstop against
// a hang rather than a performance assertion, so it is generous and scaled for
// the race detector, which makes the walk this waits on roughly an order of
// magnitude slower. A real failure is a hang, and Go's own test timeout reports
// that with the stack of whatever is stuck.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(Slow(5 * time.Second))
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// Notify is optional. Tests and any other caller that drives the pane directly
// must not have to supply one.
func TestNilNotifyIsFine(t *testing.T) {
	p := NewPane(t.TempDir())
	p.Debounce = time.Nanosecond
	p.search = func(context.Context, string, Query) Result { return Result{Files: 4} }

	typeQuery(p, "x")
	if !p.Settle(2 * time.Second) {
		t.Fatal("search did not settle")
	}
	if p.Result.Files != 4 {
		t.Errorf("files = %d, want 4", p.Result.Files)
	}
}
