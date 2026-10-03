package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"raj/internal/editor"
)

// clientUnnamedPane makes the attached client's active buffer an unnamed one
// and types into it. The client starts in Edit, so the typed text lands locally;
// it is never forwarded because an unnamed buffer has no path. The typed text is
// what the old empty-path save sent nowhere and then clobbered.
func clientUnnamedPane(t *testing.T, ch *clientHarness) *editor.Pane {
	t.Helper()
	runClient(t, ch, func() { ch.cli.newFile() })
	p := ch.cli.Tabs.Active()
	if p == nil || p.File.Path != "" {
		t.Fatalf("setup: active client buffer = %+v, want an unnamed buffer", p)
	}
	ch.cli.focusEditor()
	ch.cli.typeText("precious")
	if got := p.File.Text(); got != "precious" {
		t.Fatalf("setup: typed text = %q, want precious", got)
	}
	return p
}

// An attached client's save on an unnamed buffer asks where the file should go
// and writes it on the daemon: the client owns no bytes, so the create and the
// write run in the daemon's workspace. The data-loss bug it replaces sent an
// empty path, which the daemon resolved to the buffer the user was looking at --
// some other file -- saving that and then refetching it over the pane. No daemon
// file is touched before the path answer, and the pane keeps the typed text.
func TestAttachedUnnamedSaveAsWritesDaemonFile(t *testing.T) {
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

	// The save answers with a Save as prompt before writing anything. The old
	// refusal named the missing path; the rule now is the prompt.
	if !ch.cli.Prompt.Open {
		t.Fatalf("attached unnamed save did not ask for a path; status = %q", ch.cli.status)
	}
	if got := ch.cli.Prompt.Title(); got != "Save as" {
		t.Fatalf("prompt = %q, want Save as", got)
	}
	newPath := filepath.Join(srv.primaryRoot(), "typed.txt")
	if _, err := os.Stat(newPath); !os.IsNotExist(err) {
		t.Errorf("a daemon file existed before the path answer: %v", err)
	}
	diskMid, err := os.ReadFile(daemonPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(diskMid) != string(diskBefore) {
		t.Errorf("the prompt alone touched the daemon file: disk = %q, want %q", diskMid, diskBefore)
	}

	// Answering the prompt creates the file in the daemon's workspace with the
	// typed text, and leaves the pane showing that text under the new path.
	ch.cli.typeText("typed.txt")
	runClient(t, ch, func() { ch.cli.press("enter") })

	data, err := os.ReadFile(newPath)
	if err != nil {
		t.Fatalf("the daemon workspace has no saved file: %v", err)
	}
	if string(data) != "precious" {
		t.Errorf("daemon file = %q, want precious", data)
	}
	if p.File.Path != newPath {
		t.Errorf("pane path = %q, want the daemon path %q", p.File.Path, newPath)
	}
	if got := p.File.Text(); got != "precious" {
		t.Errorf("pane text = %q, want precious -- the save replaced it", got)
	}
	// The daemon's own dirty buffer was not the save target.
	diskAfter, err := os.ReadFile(daemonPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(diskAfter) != string(diskBefore) {
		t.Errorf("the client's unnamed save wrote the daemon's active file: disk = %q, want %q", diskAfter, diskBefore)
	}
	if !strings.Contains(ch.cli.status, "saved") {
		t.Errorf("status = %q, want a save confirmation", ch.cli.status)
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
