package app

import (
	"testing"
	"time"

	"raj/internal/ui"
)

// The application must wire the search pane's notification to the host, or the
// seam exists and nothing uses it. Draining here is deliberate: the test asserts
// that a Wake ARRIVES without a Tick having been delivered.
func TestFinishedSearchPostsAWake(t *testing.T) {
	h := newHarness(t, "package main\n")
	h.press("shift+super+f") // focus search
	h.typeText("package")

	// drain already settled the search, so anything queued now is what the
	// worker posted rather than what the keystrokes produced.
	deadline := time.Now().Add(2 * time.Second)
	for {
		select {
		case e := <-h.host.Events():
			if _, ok := e.(ui.Wake); ok {
				return
			}
			if _, ok := e.(ui.Tick); ok {
				t.Fatal("a tick arrived first; the search is still waiting on the clock")
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
