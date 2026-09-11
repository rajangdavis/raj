package app

import (
	"os"
	"strings"
	"testing"
	"time"

	"raj/internal/prompt"
	"raj/internal/ui"
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

// cmd+shift+r takes the disk version deliberately, instead of the only route being to
// attempt a save you did not want.
func TestReloadBindingOnACleanBuffer(t *testing.T) {
	h := newHarness(t, "original\n")
	rewriteOnDisk(t, h, "theirs\n")
	h.press("shift+super+r")

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
			h.press("shift+super+r")

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
	h.press("shift+super+r")

	if h.Prompt.Open {
		t.Fatal("reloading a scratch buffer opened a dialog")
	}
	if got := h.Pane().File.Text(); got != "scratch" {
		t.Errorf("buffer = %q, want it untouched", got)
	}
}

// The idle tick asks each open tab whether its file changed on disk, so the
// save prompt stops being a surprise: the tab is marked before the user presses
// save, and a reload clears the mark.
func TestIdleTickMarksDiskChangedTab(t *testing.T) {
	h := newHarness(t, "original\n")
	rewriteOnDisk(t, h, "theirs\n")
	if h.Pane().DiskStale() {
		t.Fatal("tab marked before any tick ran")
	}

	h.Handle(ui.Tick{})
	h.Draw()
	if !h.Pane().DiskStale() {
		t.Fatal("tab not marked after the file changed on disk")
	}
	if !strings.Contains(h.host.Text(), "!") {
		t.Errorf("changed-on-disk mark missing from the tab bar:\n%s", h.host.Text())
	}

	h.press("shift+super+r")
	if h.Pane().DiskStale() {
		t.Error("mark still set after reload")
	}
	if got := h.Pane().File.Text(); got != "theirs\n" {
		t.Errorf("buffer = %q, want the disk version", got)
	}
}

// Saving the buffer clears the mark: the bytes now match what the tab shows.
func TestSaveClearsDiskChangedMark(t *testing.T) {
	h := newHarness(t, "original\n")
	h.typeText("mine ")
	rewriteOnDisk(t, h, "theirs\n")
	h.Handle(ui.Tick{})
	if !h.Pane().DiskStale() {
		t.Fatal("setup: tab not marked")
	}

	// A dirty buffer's conflict defaults to Overwrite; a second enter takes it.
	h.press("super+s", "enter")
	if h.Pane().DiskStale() {
		t.Error("mark not cleared after save")
	}
	data, err := os.ReadFile(h.Pane().File.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "mine original\n" {
		t.Errorf("on disk = %q, want the buffer", data)
	}
}
