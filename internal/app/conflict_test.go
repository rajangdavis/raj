package app

import (
	"os"
	"testing"
	"time"

	"raj/internal/prompt"
)

// rewriteOnDisk stands in for git, a formatter or another editor: it changes
// the file under the open tab, far enough into the future that the mtime
// comparison cannot depend on filesystem timestamp granularity.
func rewriteOnDisk(t *testing.T, h *harness, content string) {
	t.Helper()
	path := h.Pane().File.Path
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
}

// A dirty buffer gets Overwrite first, because reloading would throw away work
// that exists nowhere else and a dialog should not default to that.
func TestConflictOnADirtyBufferDefaultsToOverwrite(t *testing.T) {
	h := newHarness(t, "original\n")
	h.typeText("mine ")
	rewriteOnDisk(t, h, "theirs\n")
	h.press("super+s")

	if !h.Prompt.Open {
		t.Fatal("saving over a changed file did not ask")
	}
	if got := h.Prompt.Selected(); got != prompt.Overwrite {
		t.Errorf("default answer = %q, want %q", got, prompt.Overwrite)
	}
	h.press("enter")

	data, err := os.ReadFile(h.Pane().File.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "mine original\n" {
		t.Errorf("on disk = %q, want the buffer written", data)
	}
}

// A clean buffer has nothing to lose, so reloading is offered first.
func TestConflictOnACleanBufferDefaultsToReload(t *testing.T) {
	h := newHarness(t, "original\n")
	rewriteOnDisk(t, h, "theirs\n")
	h.press("super+s")

	if !h.Prompt.Open {
		t.Fatal("saving over a changed file did not ask")
	}
	if got := h.Prompt.Selected(); got != prompt.Reload {
		t.Errorf("default answer = %q, want %q", got, prompt.Reload)
	}
	h.press("enter")

	if h.Prompt.Open {
		t.Fatal("a clean reload asked a second question")
	}
	if got := h.Pane().File.Text(); got != "theirs\n" {
		t.Errorf("buffer = %q, want the disk version", got)
	}
	data, err := os.ReadFile(h.Pane().File.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "theirs\n" {
		t.Errorf("on disk = %q — a reload must not write", data)
	}
}

// Reloading over unsaved work asks again. It is the one answer that destroys
// the only copy of something.
func TestReloadOverADirtyBufferAsksTwice(t *testing.T) {
	for _, tc := range []struct {
		name     string
		second   []string
		wantText string
	}{
		{"confirmed", []string{"enter"}, "theirs\n"},
		{"cancelled", []string{"right", "enter"}, "mine original\n"},
		{"escaped", []string{"esc"}, "mine original\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, "original\n")
			h.typeText("mine ")
			rewriteOnDisk(t, h, "theirs\n")
			h.press("super+s")

			// Move off Overwrite onto Reload and take it.
			h.press("right", "enter")
			if !h.Prompt.Open {
				t.Fatal("reloading over unsaved changes did not ask again")
			}
			if got := h.Prompt.Selected(); got != prompt.Discard {
				t.Errorf("default answer = %q, want %q", got, prompt.Discard)
			}
			h.press(tc.second...)

			if got := h.Pane().File.Text(); got != tc.wantText {
				t.Errorf("buffer = %q, want %q", got, tc.wantText)
			}
		})
	}
}

// Cancelling leaves everything alone: the buffer keeps its edits and the file
// keeps whatever the other writer put there.
func TestConflictCancelWritesNothing(t *testing.T) {
	h := newHarness(t, "original\n")
	h.typeText("mine ")
	rewriteOnDisk(t, h, "theirs\n")
	h.press("super+s")
	h.press("esc")

	if got := h.Pane().File.Text(); got != "mine original\n" {
		t.Errorf("buffer = %q, want the edits kept", got)
	}
	data, err := os.ReadFile(h.Pane().File.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "theirs\n" {
		t.Errorf("on disk = %q, want the other writer's version kept", data)
	}
	if !h.Pane().File.Dirty() {
		t.Error("a cancelled save marked the buffer clean")
	}
}

// cmd+r takes the disk version deliberately, instead of the only route being to
// attempt a save you did not want.
func TestReloadBindingOnACleanBuffer(t *testing.T) {
	h := newHarness(t, "original\n")
	rewriteOnDisk(t, h, "theirs\n")
	h.press("super+r")

	if h.Prompt.Open {
		t.Fatal("reloading a clean buffer asked a question")
	}
	if got := h.Pane().File.Text(); got != "theirs\n" {
		t.Errorf("buffer = %q, want the disk version", got)
	}
}

func TestReloadBindingOnADirtyBufferAsks(t *testing.T) {
	for _, tc := range []struct {
		name     string
		answer   []string
		wantText string
	}{
		{"discard", []string{"enter"}, "theirs\n"},
		{"cancel", []string{"right", "enter"}, "mine original\n"},
		{"escape", []string{"esc"}, "mine original\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, "original\n")
			h.typeText("mine ")
			rewriteOnDisk(t, h, "theirs\n")
			h.press("super+r")

			if !h.Prompt.Open {
				t.Fatal("reloading over unsaved changes did not ask")
			}
			h.press(tc.answer...)
			if got := h.Pane().File.Text(); got != tc.wantText {
				t.Errorf("buffer = %q, want %q", got, tc.wantText)
			}
		})
	}
}

// Reload never writes: an unnamed buffer has nothing to reload from and must
// not be turned into an error the user has to dismiss.
func TestReloadBindingOnAnUnnamedBuffer(t *testing.T) {
	h := newHarness(t, "original\n")
	h.press("super+n")
	h.typeText("scratch")
	h.press("super+r")

	if h.Prompt.Open {
		t.Fatal("reloading a scratch buffer opened a dialog")
	}
	if got := h.Pane().File.Text(); got != "scratch" {
		t.Errorf("buffer = %q, want it untouched", got)
	}
}
