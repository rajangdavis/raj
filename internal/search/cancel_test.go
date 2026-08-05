package search

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"raj/internal/keys"
)

// The bug this file exists for: typing N characters used to leave N walks
// running, because dropping a stale result is not the same as not computing it.
//
// The invariant is not "one at a time" — cancelling is not instantaneous, so a
// replacement can start while its predecessor is still noticing. It is that
// concurrency stays bounded by a small constant no matter how much is typed.
// Before cancellation existed, peak tracked the keystroke count exactly.
func TestTypingDoesNotPileUpWalks(t *testing.T) {
	peakFor := func(keystrokes int) int64 {
		p := NewPane(t.TempDir())
		p.Debounce = time.Nanosecond
		var live, peak int64
		var mu sync.Mutex
		p.search = func(ctx context.Context, _ string, _ Query) Result {
			n := atomic.AddInt64(&live, 1)
			mu.Lock()
			if n > peak {
				peak = n
			}
			mu.Unlock()
			defer atomic.AddInt64(&live, -1)
			select {
			case <-ctx.Done():
				return Result{Stopped: true}
			case <-time.After(2 * time.Second):
				return Result{Files: 1}
			}
		}
		for i := 0; i < keystrokes; i++ {
			p.Handle(keys.None, "a")
			time.Sleep(5 * time.Millisecond) // let each walk actually start
		}
		p.stopForTest()
		mu.Lock()
		defer mu.Unlock()
		return peak
	}

	// A predecessor can still be winding down as its replacement starts, so
	// the bound is two rather than one. Whether a given run reaches it is
	// scheduling noise; what matters is that twenty-four keystrokes cost the
	// same as three. Before cancellation, peakFor(24) was 24.
	const handoff = 2
	for _, tc := range []struct {
		keystrokes int
		peak       int64
	}{
		{3, peakFor(3)},
		{24, peakFor(24)},
	} {
		if tc.peak > handoff {
			t.Errorf("%d keystrokes left %d walks running, want <= %d",
				tc.keystrokes, tc.peak, handoff)
		}
	}
}

// A cancelled search must not install its result, even if it somehow finishes
// first: the pane is showing the answer to a query the user has moved on from.
func TestCancelledSearchDoesNotInstall(t *testing.T) {
	p := NewPane(t.TempDir())
	p.Debounce = time.Nanosecond

	release := make(chan struct{})
	p.search = func(ctx context.Context, _ string, q Query) Result {
		if q.Text == "slow" {
			<-release
			return Result{Files: 99, Stopped: ctx.Err() != nil}
		}
		return Result{Files: 1}
	}

	typeQuery(p, "slow")
	time.Sleep(20 * time.Millisecond)
	typeQuery(p, "!")
	close(release)

	if !p.Settle(2 * time.Second) {
		t.Fatal("searches did not settle")
	}
	if p.Result.Files == 99 {
		t.Fatal("the abandoned search installed its result")
	}
}

// The searcher must actually observe cancellation, or nothing above it matters.
func TestSearcherSeesCancellation(t *testing.T) {
	p := NewPane(t.TempDir())
	p.Debounce = time.Nanosecond

	seen := make(chan struct{}, 1)
	p.search = func(ctx context.Context, _ string, _ Query) Result {
		<-ctx.Done()
		select {
		case seen <- struct{}{}:
		default:
		}
		return Result{Stopped: true}
	}

	typeQuery(p, "a")
	time.Sleep(20 * time.Millisecond)
	typeQuery(p, "b")

	select {
	case <-seen:
	case <-time.After(time.Second):
		t.Fatal("the running search was never cancelled")
	}
	p.stopForTest()
}

// RunContext itself must stop mid-walk, not only between searches.
func TestRunContextStopsEarly(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 400; i++ {
		sub := filepath.Join(dir, "d", "e")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		name := filepath.Join(sub, "f"+string(rune('a'+i%26))+itoa(i)+".go")
		if err := os.WriteFile(name, []byte("package p\n// NEEDLE\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	full := Run(dir, Query{Text: "NEEDLE", Case: true})
	if full.Stopped {
		t.Fatal("an uncancelled search reported Stopped")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got := RunContext(ctx, dir, Query{Text: "NEEDLE", Case: true})
	if !got.Stopped {
		t.Fatal("a cancelled search did not report Stopped")
	}
	if len(got.Matches) >= len(full.Matches) {
		t.Fatalf("cancelled search scanned %d of %d files: it did not stop",
			len(got.Matches), len(full.Matches))
	}
}

// The window tracks measured cost: fast trees stay responsive, slow ones stop
// starting walks faster than they can be thrown away.
func TestAdaptiveDebounce(t *testing.T) {
	p := NewPane(t.TempDir())
	for _, tc := range []struct {
		last time.Duration
		want time.Duration
	}{
		{0, DefaultDebounce}, // nothing measured yet
		{10 * time.Millisecond, MinDebounce},
		{200 * time.Millisecond, 100 * time.Millisecond},
		{3 * time.Second, MaxDebounce}, // clamped, not unbounded
	} {
		p.lastDur = tc.last
		if got := p.debounce(); got != tc.want {
			t.Errorf("lastDur=%v: debounce=%v want %v", tc.last, got, tc.want)
		}
	}
	p.Debounce = 42 * time.Millisecond
	p.lastDur = time.Hour
	if got := p.debounce(); got != 42*time.Millisecond {
		t.Errorf("explicit Debounce ignored: got %v", got)
	}
}

// A cancelled search must not teach the pane that this tree is fast, or the
// window collapses to the floor after the first abandoned walk.
func TestCancelledSearchDoesNotShrinkTheWindow(t *testing.T) {
	p := NewPane(t.TempDir())
	p.Debounce = time.Nanosecond
	p.search = func(ctx context.Context, _ string, _ Query) Result {
		select {
		case <-ctx.Done():
			return Result{Stopped: true}
		case <-time.After(time.Second):
			return Result{}
		}
	}
	p.lastDur = 400 * time.Millisecond

	typeQuery(p, "a")
	time.Sleep(20 * time.Millisecond)
	typeQuery(p, "b")
	time.Sleep(50 * time.Millisecond)
	p.stopForTest()
	time.Sleep(50 * time.Millisecond)

	if got := p.LastDuration(); got != 400*time.Millisecond {
		t.Fatalf("lastDur = %v, want it unchanged at 400ms", got)
	}
	if p.Abandoned() == 0 {
		t.Fatal("abandoned searches were not counted")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
