package app

import "testing"

// A notice is for something the App cannot know about — a stale terminal
// config — so it must reach the status line at startup.
func TestNoticeReachesTheStatusLine(t *testing.T) {
	h := newHarness(t, "")
	h.Notice("configs are out of date")
	if got := h.Status(); got != "configs are out of date" {
		t.Errorf("status = %q, want the notice", got)
	}
}

// It must not shout over something the App already had to say. A bad pattern
// in .raj/hidden is about the workspace in front of the user; a stale config is
// about their terminal, and it can wait.
func TestNoticeYieldsToAnExistingStatus(t *testing.T) {
	h := newHarness(t, "")
	h.status = "ignoring 1 bad pattern"
	h.Notice("configs are out of date")
	if got := h.Status(); got != "ignoring 1 bad pattern" {
		t.Errorf("status = %q, want the existing message", got)
	}
}
