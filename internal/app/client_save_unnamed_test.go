package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"raj/internal/editor"
)

// clientUnnamedPane makes the attached client's active buffer an unnamed one
// and types into it. Leaving Review is the client's edit path, so the mode is
// set directly, exactly as TestAttachedClientEditsOutsideReview does; the typed
// text is what the old empty-path save sent nowhere and then clobbered.
func clientUnnamedPane(t *testing.T, ch *clientHarness) *editor.Pane {
	t.Helper()
	runClient(t, ch, func() { ch.cli.newFile() })
	p := ch.cli.Tabs.Active()
	if p == nil || p.File.Path != "" {
		t.Fatalf("setup: active client buffer = %+v, want an unnamed buffer", p)
	}
	ch.cli.mode = ModeEdit
	ch.cli.focusEditor()
	ch.cli.typeText("precious")
	if got := p.File.Text(); got != "precious" {
		t.Fatalf("setup: typed text = %q, want precious", got)
	}
	return p
}

// The data-loss bug: an attached client's save on an unnamed buffer sent an
// empty path, which the daemon resolved to the buffer the user was looking at
// -- some other file -- saving that and then refetching it over the pane. The
// typed text, which flushClientEdit never forwarded because the path was empty,
// was gone. The save must instead answer for a path: no daemon file is written
// and the pane text is left as typed.
func TestAttachedUnnamedSaveNeverTouchesDaemonFile(t *testing.T) {
	srv := controlHarness(t, "hello\n")
	srv.typeText("X") // the daemon buffer is dirty, so an empty-path save would write it
	ch := attachClient(t, srv)
	ch.cli.drain()
	daemonPath := srv.Tabs.Active().File.Path
	diskBefore, err := os.ReadFile(daemonPath)
	if err != nil {
		t.Fatal(err)
	}

	p := clientUnnamedPane(t, ch)
	runClient(t, ch, func() { ch.cli.saveActive(nil) })

	// No daemon file was written. The old empty-path save wrote the daemon's
	// dirty text over disk; the fix must leave the disk alone.
	diskAfter, err := os.ReadFile(daemonPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(diskAfter) != string(diskBefore) {
		t.Errorf("the client's unnamed save wrote the daemon file: disk = %q, want %q", diskAfter, diskBefore)
	}
	// The pane still shows the typed text. The old read-back replaced it with
	// the daemon file's content.
	if got := p.File.Text(); got != "precious" {
		t.Errorf("pane text = %q, want precious -- the save replaced it", got)
	}
	// The user was given a path answer: a Save as prompt or a refusal naming
	// the missing path. Silence would be the one unacceptable outcome.
	if ch.cli.Prompt.Open {
		if got := ch.cli.Prompt.Title(); got != "Save as" {
			t.Errorf("prompt = %q, want Save as", got)
		}
	} else if !strings.Contains(ch.cli.status, "path") {
		t.Errorf("save gave no path answer: prompt=%v status=%q", ch.cli.Prompt.Open, ch.cli.status)
	}
}

// The local behaviour is unchanged: an unnamed buffer asks for a path and
// writes there. This is the save-as the attach branch used to skip.
func TestLocalUnnamedSaveStillPromptsForAPath(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "base\n")
	h.newFile()
	h.typeText("keep me")
	if got := h.Pane().File.Path; got != "" {
		t.Fatalf("setup: active buffer = %q, want unnamed", got)
	}

	h.press("super+s")

	if !h.Prompt.Open {
		t.Fatal("a local unnamed save did not ask for a path")
	}
	if got := h.Prompt.Title(); got != "Save as" {
		t.Fatalf("prompt = %q, want Save as", got)
	}
	h.typeText("kept.txt")
	h.press("enter")

	data, err := os.ReadFile(filepath.Join(h.primaryRoot(), "kept.txt"))
	if err != nil {
		t.Fatalf("file not written: %v", err)
	}
	if string(data) != "keep me" {
		t.Errorf("on disk = %q, want keep me", data)
	}
}
