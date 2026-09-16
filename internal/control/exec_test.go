//go:build unix

package control

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Cancelling an exec has to reap the whole process tree, not just the shell.
// `sh -c 'sleep 30 & echo $! > pid; wait'` is a shell with a child: killing
// only the shell leaves the child running and holding the pipes, which is the
// bug the process group exists to fix. The child's pid is read back from the
// command itself and checked gone after the cancel.
func TestRunCancelKillsTheProcessGroup(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := Run(ctx, []string{"sh", "-c",
			"sleep 30 & echo $! > " + pidFile + "; wait"}, dir, nil)
		done <- err
	}()

	// Wait for the shell to start the child and record its pid.
	var pid int
	start := time.Now()
	for pid == 0 && time.Since(start) < 5*time.Second {
		if b, err := os.ReadFile(pidFile); err == nil {
			if p, perr := strconv.Atoi(strings.TrimSpace(string(b))); perr == nil && p > 0 {
				pid = p
			}
		}
		if pid == 0 {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if pid == 0 {
		t.Fatal("the command never recorded its child's pid")
	}

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run after cancel = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after the cancel")
	}

	// The child must be gone. Poll, because the group kill races Run's return.
	gone := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			gone = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !gone {
		t.Errorf("process %d survived the cancel; the process group was not killed", pid)
	}
}
