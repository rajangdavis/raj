package ui

import (
	"testing"
	"time"
)

// The headless host satisfies the seam at a fixed size, discards frames, drives
// the idle tick, and closes cleanly. Without it a daemon had no host to run.
func TestHeadlessHostSatisfiesTheSeam(t *testing.T) {
	h := newHeadlessHost(80, 24, time.Millisecond)
	t.Cleanup(func() { h.Close() })
	if cols, rows := h.Size(); cols != 80 || rows != 24 {
		t.Errorf("Size = %d,%d, want 80,24", cols, rows)
	}
	if !h.Theme().Known {
		t.Error("theme is unknown; the daemon should report a default")
	}
	if err := h.Present(NewScreen(80, 24)); err != nil {
		t.Errorf("Present = %v, want nil", err)
	}
	// The idle tick arrives on its own.
	select {
	case e := <-h.Events():
		if _, ok := e.(Tick); !ok {
			t.Errorf("first event = %T, want Tick", e)
		}
	case <-time.After(time.Second):
		t.Fatal("no idle tick arrived")
	}
	// Post injects an event, which is how control wakes the loop.
	h.Post(Wake{})
	woke := false
	deadline := time.After(time.Second)
	for !woke {
		select {
		case e := <-h.Events():
			if _, ok := e.(Wake); ok {
				woke = true
			}
		case <-deadline:
			t.Fatal("the posted event did not arrive")
		}
	}
	// The terminal-side gestures are no-ops and Close is idempotent.
	h.Invalidate()
	h.Repaint()
	if err := h.Suspend(); err != nil {
		t.Errorf("Suspend = %v", err)
	}
	h.SetClipboard("x")
	if err := h.Close(); err != nil {
		t.Errorf("Close = %v", err)
	}
	if err := h.Close(); err != nil {
		t.Errorf("second Close = %v", err)
	}
	if _, ok := <-h.Events(); ok {
		t.Error("Events stayed open after Close")
	}
}

// The default constructor reports the daemon size.
func TestNewHeadlessHostSize(t *testing.T) {
	h := NewHeadlessHost()
	t.Cleanup(func() { h.Close() })
	if cols, rows := h.Size(); cols != HeadlessCols || rows != HeadlessRows {
		t.Errorf("Size = %d,%d, want %d,%d", cols, rows, HeadlessCols, HeadlessRows)
	}
}
