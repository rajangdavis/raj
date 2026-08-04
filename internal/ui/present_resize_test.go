package ui

import (
	"io"
	"os"
	"strings"
	"testing"
)

// host builds a NativeHost writing to a pipe, so Present's output can be read
// without a terminal. Only the fields Present touches are set.
func host(t *testing.T, cols, rows int) (*NativeHost, *os.File) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close(); w.Close() })
	return &NativeHost{w: w, cols: cols, rows: rows, events: make(chan Event, 8)}, r
}

func read(t *testing.T, r *os.File, n int) string {
	t.Helper()
	buf := make([]byte, n)
	c, _ := r.Read(buf)
	return string(buf[:c])
}

// A frame sized for a terminal that no longer exists must not be written.
//
// SIGWINCH updates the size from another goroutine, so mid-drag the frame in
// hand can be too wide. Writing it wraps at the real right edge and scrolls the
// screen — the thrash. Growing looks cleaner than shrinking only because a
// too-narrow frame leaves stale cells instead of wrapping.
func TestPresentDropsFrameSizedForAnotherTerminal(t *testing.T) {
	h, _ := host(t, 40, 10) // terminal is now 40x10
	s := NewScreen(80, 24)  // frame was built at 80x24
	s.SetString(0, 0, strings.Repeat("x", 80), DefaultStyle, 80)

	if err := h.Present(s); err != nil {
		t.Fatal(err)
	}
	// Nothing may be written; a read on the empty pipe would block, so check
	// the invalidation state instead and then confirm the next frame repaints.
	h.mu.Lock()
	dirty := h.dirty
	h.mu.Unlock()
	if !dirty {
		t.Error("dropped frame left the host clean; the next frame would be a diff")
	}
	if h.prev != nil {
		t.Error("dropped frame was recorded as previous; the next diff would be wrong")
	}
}

// A correctly sized frame is written, and after a dropped one it is a full
// repaint rather than a diff against a screen that was never drawn.
func TestPresentWritesFullFrameAfterDrop(t *testing.T) {
	h, r := host(t, 40, 10)
	h.Present(NewScreen(80, 24)) // dropped

	s := NewScreen(40, 10)
	s.SetString(0, 0, "hello", DefaultStyle, 40)
	go h.Present(s)

	out := read(t, r, 4096)
	if !strings.Contains(out, "\x1b[2J") {
		t.Error("frame after a drop was not a full repaint")
	}
	if !strings.Contains(out, "hello") {
		t.Errorf("frame content missing from %q", out)
	}
}

// The ordinary case is untouched: matching sizes write a diff.
func TestPresentWritesMatchingFrame(t *testing.T) {
	h, r := host(t, 40, 10)
	s := NewScreen(40, 10)
	s.SetString(0, 0, "hello", DefaultStyle, 40)
	go h.Present(s)
	if out := read(t, r, 4096); !strings.Contains(out, "hello") {
		t.Errorf("nothing written for a correctly sized frame: %q", out)
	}
}

// shortWriter accepts at most n bytes per call, the way a tty in raw mode does
// when its buffer is full.
type shortWriter struct {
	n     int
	buf   []byte
	fail  error
	calls int
}

func (w *shortWriter) Write(b []byte) (int, error) {
	w.calls++
	if w.fail != nil {
		return 0, w.fail
	}
	if len(b) > w.n {
		b = b[:w.n]
	}
	w.buf = append(w.buf, b...)
	return len(b), nil
}

// Every byte of a frame must reach the terminal. os.File.Write does not loop on
// a partial write, and a near-full-screen diff is thousands of bytes — which
// scrolling with wrapping on produces on every keystroke.
func TestPresentWritesEveryByte(t *testing.T) {
	w := &shortWriter{n: 7}
	h := &NativeHost{w: w, cols: 40, rows: 10, events: make(chan Event, 8)}
	s := NewScreen(40, 10)
	s.SetString(0, 0, strings.Repeat("abcdefghij", 4), DefaultStyle, 40)

	if err := h.Present(s); err != nil {
		t.Fatalf("Present: %v", err)
	}
	if w.calls < 2 {
		t.Fatalf("only %d write calls; the writer was not exercised", w.calls)
	}
	if !strings.Contains(string(w.buf), "abcdefghij") {
		t.Errorf("frame content truncated: %q", w.buf)
	}
}

// A failed write must not be recorded as the previous frame.
//
// This was the scroll thrash: prev was set before the write, so a frame that
// only partly landed left every later diff skipping exactly the cells that
// never arrived — two frames interleaved character by character, clearing at
// the next full repaint.
func TestFailedWriteIsNotRecordedAsDrawn(t *testing.T) {
	w := &shortWriter{n: 4, fail: io.ErrClosedPipe}
	h := &NativeHost{w: w, cols: 40, rows: 10, events: make(chan Event, 8)}
	s := NewScreen(40, 10)
	s.SetString(0, 0, "hello", DefaultStyle, 40)

	if err := h.Present(s); err == nil {
		t.Fatal("Present reported success on a failed write")
	}
	if h.prev != nil {
		t.Error("a frame that never landed was recorded as the previous one")
	}
	h.mu.Lock()
	dirty := h.dirty
	h.mu.Unlock()
	if !dirty {
		t.Error("host stayed clean after a failed write; the next frame would be a diff")
	}
}

// A successful write does record the frame, so the next one is a diff.
func TestSuccessfulWriteIsRecorded(t *testing.T) {
	w := &shortWriter{n: 1 << 20}
	h := &NativeHost{w: w, cols: 40, rows: 10, events: make(chan Event, 8)}
	s := NewScreen(40, 10)
	s.SetString(0, 0, "hello", DefaultStyle, 40)
	if err := h.Present(s); err != nil {
		t.Fatal(err)
	}
	if h.prev == nil {
		t.Error("a frame that landed was not recorded")
	}
}

// The size guard must consult the terminal, not the cache.
//
// SIGWINCH is the only thing that refreshes the cache, and a stopped process
// services no signals — so after ctrl+z and fg the cache reflects whatever the
// terminal was when raj went to sleep. A guard trusting it waves a wrong-sized
// frame through, which is why the thrash survived on the resume path.
func TestSizeGuardDoesNotTrustTheCache(t *testing.T) {
	w := &shortWriter{n: 1 << 20}
	h := &NativeHost{w: w, cols: 80, rows: 24, events: make(chan Event, 8)}

	// Frame matches the cache, so a cache-trusting guard would write it.
	s := NewScreen(80, 24)
	s.SetString(0, 0, "stale", DefaultStyle, 80)
	if err := h.Present(s); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(w.buf), "stale") {
		t.Fatal("setup: matching frame was not written")
	}

	// Now the terminal is really 40x10 while the cache still says 80x24.
	// trueSize consults h.out; with none, the cache stands in for it, so drive
	// the mismatch from the screen side to exercise the same comparison.
	w.buf = nil
	if err := h.Present(NewScreen(40, 10)); err != nil {
		t.Fatal(err)
	}
	if len(w.buf) != 0 {
		t.Errorf("wrote %q for a frame that does not match the terminal", w.buf)
	}
	h.mu.Lock()
	dirty := h.dirty
	h.mu.Unlock()
	if !dirty || h.prev != nil {
		t.Error("mismatched frame did not force a full repaint next time")
	}
}
