package app

import (
	"os"
	"strings"
	"testing"
	"time"

	"raj/internal/control"
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

// The idle tick asks each open tab whether its file changed on disk. A dirty
// buffer keeps the mark so the save prompt stops being a surprise, and its
// unsaved text is not thrown away.
func TestIdleTickMarksDirtyDiskChangedTab(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "original\n")
	h.typeText("mine ") // dirty: the tick must not discard it
	rewriteOnDisk(t, h, "theirs\n")
	if h.Pane().DiskStale() {
		t.Fatal("tab marked before any tick ran")
	}

	h.Handle(ui.Tick{})
	h.Draw()
	if !h.Pane().DiskStale() {
		t.Fatal("dirty tab not marked after the file changed on disk")
	}
	if got := h.Pane().File.Text(); got != "mine original\n" {
		t.Errorf("buffer = %q, want the unsaved text kept", got)
	}
	if !strings.Contains(h.host.Text(), "!") {
		t.Errorf("changed-on-disk mark missing from the tab bar:\n%s", h.host.Text())
	}
}

// A clean buffer has nothing to lose, so the idle tick takes the disk version
// instead of marking a conflict the user would have to resolve.
func TestIdleTickReloadsCleanDiskChangedTab(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "original\n")
	rewriteOnDisk(t, h, "theirs\n")

	h.Handle(ui.Tick{})
	h.Draw()
	if h.Pane().DiskStale() {
		t.Error("a clean tab was marked instead of reloaded")
	}
	if got := h.Pane().File.Text(); got != "theirs\n" {
		t.Errorf("buffer = %q, want the disk version", got)
	}
	if h.Prompt.Open {
		t.Error("a clean reload asked a question")
	}
	if !strings.Contains(h.Status(), "reloaded") {
		t.Errorf("status = %q, want a reloaded note", h.Status())
	}
}

// Saving the buffer clears the mark: the bytes now match what the tab shows.
func TestSaveClearsDiskChangedMark(t *testing.T) {
	t.Parallel()
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

// deleteOnDisk stands in for another tool removing the file under the open tab.
// It returns the path, now absent, whose buffer holds the only copy left.
func deleteOnDisk(t *testing.T, h *harness) string {
	t.Helper()
	path := h.Pane().File.Path
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	return path
}

// A clean buffer whose file was removed is marked, not reloaded: there is no
// version on disk to take, and the old clean-buffer reload failed with a status
// only this screen saw, leaving a tab that still read as live.
func TestDeletedOnDiskMarksACleanTabAndStatus(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "original\n")
	deleteOnDisk(t, h)

	h.Handle(ui.Tick{})
	h.Draw()

	if !h.Pane().DiskStale() {
		t.Fatal("a deleted file left its tab unmarked")
	}
	if !h.Pane().DiskDeleted() {
		t.Error("the tab is marked but the deletion is not recorded as the reason")
	}
	if got := h.Pane().File.Text(); got != "original\n" {
		t.Errorf("buffer = %q, want the text kept readable", got)
	}
	if !strings.Contains(h.host.Text(), "!") {
		t.Errorf("changed-on-disk mark missing from the tab bar:\n%s", h.host.Text())
	}
	if got := h.Status(); !strings.Contains(got, "test.go was deleted on disk") {
		t.Errorf("status = %q, want the deletion named", got)
	}
	if h.Prompt.Open {
		t.Error("the idle tick asked a question instead of marking the tab")
	}
}

// The same with unsaved text: the text is not thrown away and the mark keeps
// the reason, so the save question will not be a surprise.
func TestDeletedOnDiskMarksADirtyTabAndKeepsText(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "original\n")
	h.typeText("mine ")
	deleteOnDisk(t, h)

	h.Handle(ui.Tick{})
	h.Draw()

	if !h.Pane().DiskDeleted() {
		t.Fatal("a deleted file left an unsaved tab unmarked")
	}
	if got := h.Pane().File.Text(); got != "mine original\n" {
		t.Errorf("buffer = %q, want the unsaved text kept", got)
	}
	if !strings.Contains(h.Status(), "was deleted on disk") {
		t.Errorf("status = %q, want the deletion named", h.Status())
	}
}

// Saving a deleted file asks, and the only answers are to write it back or
// leave it deleted. There is no disk version to reload and nothing on disk to
// overwrite.
func TestSaveDeletedFileOffersWriteBack(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "original\n")
	path := deleteOnDisk(t, h)

	h.press("super+s")
	if !h.Prompt.Open {
		t.Fatal("saving a file that was deleted did not ask")
	}
	if got := h.Prompt.Selected(); got != prompt.WriteBack {
		t.Errorf("default answer = %q, want %q", got, prompt.WriteBack)
	}
	h.press("enter")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("write it back did not recreate the file: %v", err)
	}
	if string(data) != "original\n" {
		t.Errorf("on disk = %q, want the buffer written", data)
	}
	if h.Pane().DiskDeleted() {
		t.Error("the deleted mark survived the write that put the file back")
	}
}

// Cancel is the other answer, and it must leave the file gone: an accidental
// save on a buffer whose file someone removed must not recreate it.
func TestSaveDeletedFileCancelLeavesItDeleted(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "original\n")
	h.typeText("mine ")
	path := deleteOnDisk(t, h)

	h.press("super+s")
	if !h.Prompt.Open {
		t.Fatal("saving a file that was deleted did not ask")
	}
	h.press("esc")

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("cancel left a file behind: stat = %v", err)
	}
	if !h.Pane().DiskDeleted() {
		t.Error("cancel dropped the deleted mark")
	}
	if got := h.Pane().File.Text(); got != "mine original\n" {
		t.Errorf("buffer = %q, want the edits kept", got)
	}
	if !strings.Contains(h.Status(), "deleted on disk") {
		t.Errorf("status = %q, want the cancel named against the deletion", h.Status())
	}
}

// The deletion is visible to a reader with no screen: buffers reports it and a
// read of the path carries the same fact, while still returning the text the
// buffer holds -- with unsaved text and without, since a dirty buffer is the one
// whose loss would matter.
func TestDeletedFileSaysGoneOverBuffersAndRead(t *testing.T) {
	for _, tc := range []struct {
		name  string
		dirty bool
	}{
		{"clean", false},
		{"unsaved", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := controlHarness(t, "original\n")
			if tc.dirty {
				h.typeText("mine ")
			}
			path := h.Pane().File.Path
			c := h.dial(t)
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}

			h.Handle(ui.Tick{})

			bs := c.do(h, control.Request{Op: "buffers"})
			if !bs.OK || len(bs.Buffers) != 1 {
				t.Fatalf("buffers = %+v", bs)
			}
			if !bs.Buffers[0].Deleted {
				t.Errorf("buffers = %+v, want the deleted fact", bs.Buffers[0])
			}
			if bs.Buffers[0].Dirty != tc.dirty {
				t.Errorf("buffers = %+v, want dirty=%v", bs.Buffers[0], tc.dirty)
			}

			txt := c.do(h, control.Request{Op: "text"})
			if !txt.OK {
				t.Fatalf("read = %+v", txt)
			}
			if len(txt.Buffers) != 1 || !txt.Buffers[0].Deleted {
				t.Errorf("read = %+v, want the deleted fact on the reply", txt.Buffers)
			}
			if got := txt.Text(); got != "original\n" && got != "mine original\n" {
				t.Errorf("read text = %q, want the buffer kept readable", got)
			}
		})
	}
}

// The attached client mirrors the daemon tab and must carry the same mark:
// it cannot stat the daemon filesystem, so the fact has to come over the wire.
func TestDeletedFileReachesTheAttachedClient(t *testing.T) {
	srv := controlHarness(t, "original\n")
	srv.typeText("mine ") // unsaved work is what makes the client mirror the tab
	ch := attachClient(t, srv)
	ch.cli.drain()
	if got := ch.cli.Tabs.Count(); got != 1 {
		t.Fatalf("client tabs = %d, want the mirrored tab", got)
	}

	deleteOnDisk(t, srv)
	srv.Handle(ui.Tick{})
	srv.Draw()
	if !srv.Pane().DiskDeleted() {
		t.Fatal("setup: the daemon did not mark its own deleted tab")
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		if p := ch.cli.Tabs.Active(); p != nil && p.DiskDeleted() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the client never showed the deleted mark")
		}
		srv.drain()
		ch.cli.drain()
		time.Sleep(time.Millisecond)
	}
	ch.cli.Draw()
	if p := ch.cli.Tabs.Active(); p == nil || !p.DiskStale() {
		t.Errorf("client pane = %+v, want the changed-on-disk mark", p)
	}
	if !strings.Contains(ch.cli.host.Text(), "!") {
		t.Errorf("client tab bar missing the mark:\n%s", ch.cli.host.Text())
	}
	if got := ch.cli.Status(); !strings.Contains(got, "was deleted on disk") {
		t.Errorf("client status = %q, want the deletion named", got)
	}
}
