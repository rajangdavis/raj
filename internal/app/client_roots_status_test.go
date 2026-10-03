package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"raj/internal/ui"
)

// A client that adopts a daemon workspace different from the one it was
// launched in says which workspace it is showing. Without the note the tree
// silently changes under the user and the only evidence is the paths in it.
func TestAttachNamesAdoptedWorkspace(t *testing.T) {
	launch := t.TempDir()
	workspace := filepath.Join(launch, "A")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	a := scriptedAttach(t, launch, []string{workspace})

	if got := a.Status(); !strings.Contains(got, "attached to "+workspace) {
		t.Errorf("status = %q, want it to name the adopted workspace %q", got, workspace)
	}
}

// A client that adopts the workspace it was already launched in stays quiet:
// the launch root is already what the user sees, so a note would be noise.
func TestAttachQuietWhenWorkspaceMatches(t *testing.T) {
	launch := t.TempDir()
	a := scriptedAttach(t, launch, []string{launch})

	if got := a.Status(); strings.Contains(got, "attached to ") {
		t.Errorf("status = %q, want no workspace note when the sets agree", got)
	}
}

// A reconnect that re-adopts a different daemon workspace says so too: the
// note is not only an attach-time fact, because a restarted daemon can come
// back serving another tree.
func TestReconnectNamesReAdoptedWorkspace(t *testing.T) {
	launch, firstRoot, secondRoot := t.TempDir(), t.TempDir(), t.TempDir()

	first := newScriptedConn(1, nil, nil)
	first.roots = []string{firstRoot}
	setClientDial(t, func(string) (clientConn, error) { return first, nil })
	fh := ui.NewFakeHost(120, 30)
	t.Cleanup(func() { fh.Close() })
	a := NewWithOptions(fh, launch, Options{Attach: true, AttachAddr: "scripted"})
	t.Cleanup(a.CloseClient)
	a.StartClient()
	if got := a.Status(); !strings.Contains(got, "attached to "+firstRoot) {
		t.Fatalf("attach status = %q, want the first workspace named", got)
	}
	h := &harness{App: a, host: fh}

	second := newScriptedConn(2, nil, nil)
	second.roots = []string{secondRoot}
	setClientDial(t, func(string) (clientConn, error) { return second, nil })

	// Drop the live watch so the retry loop reconnects to the daemon that
	// reports the other workspace.
	a.clientConnMu.Lock()
	c := a.client
	a.clientConnMu.Unlock()
	if c != nil {
		_ = c.Close()
	}

	deadline := time.After(3 * time.Second)
	for {
		h.drain()
		if strings.Contains(a.Status(), "attached to "+secondRoot) {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("reconnect status = %q, want %q named", a.Status(), secondRoot)
		default:
			time.Sleep(time.Millisecond)
		}
	}
}
