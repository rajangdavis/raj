package app

import (
	"testing"
	"time"

	"raj/internal/search"
	"raj/internal/ui"
)

// The application must wire the search pane's notification to the host, or the
// seam exists and nothing uses it. Draining here is deliberate: the test asserts
// that a Wake ARRIVES without a Tick having been delivered.
func TestFinishedSearchPostsAWake(t *testing.T) {
	h := newHarness(t, "package main\n")
	h.press("shift+super+f") // focus search
	h.typeText("package")

	// drain() cannot be trusted to leave the Wake in the queue. It reads
	// every queued event and only then settles, so whether the worker's Wake
	// lands before or after drain's final read is a race between two
	// goroutines — and when drain wins it eats the very event this test is
	// looking for. That is why this failed under load at any deadline rather
	// than at a longer one.
	//
	// So: empty the queue, then start one more search and consume nothing but
	// what it posts.
	for drained := false; !drained; {
		select {
		case <-h.host.Events():
		default:
			drained = true
		}
	}

	// Handled directly rather than through drain, so nothing between this
	// keystroke and the assertion can consume the Wake it produces.
	h.host.Type("x")
	h.Handle(<-h.host.Events())

	deadline := time.Now().Add(search.Slow(5 * time.Second))
	for {
		select {
		case e := <-h.host.Events():
			if _, ok := e.(ui.Wake); ok {
				return
			}
		default:
			if time.Now().After(deadline) {
				t.Fatal("no Wake posted; a finished search still waits for the tick")
			}
			time.Sleep(time.Millisecond)
		}
	}
}

// A Wake must be harmless on its own: it carries nothing, so handling one is
// only worth doing because Run draws afterwards.
func TestWakeIsHandledWithoutSideEffects(t *testing.T) {
	h := newHarness(t, "hello")
	before := h.text()
	h.Handle(ui.Wake{})
	h.Draw()
	if got := h.text(); got != before {
		t.Errorf("a wake changed the buffer: %q", got)
	}
	if h.Status() != "" {
		t.Errorf("a wake set a status: %q", h.Status())
	}
}
