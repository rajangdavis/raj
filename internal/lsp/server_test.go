package lsp

import (
	"context"
	"testing"
	"time"
)

// A server that cannot start must not be started forever. An editor that
// respawns on every exit turns a missing toolchain into a fork bomb, which is
// very hard to diagnose from inside the editor.
func TestRestartsAreBoundedAndSpaced(t *testing.T) {
	s := &Server{Command: "definitely-not-a-real-language-server"}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	now := time.Now()
	if !s.ShouldRestart(now) {
		t.Fatal("a fresh server should be startable")
	}
	for i := 0; i < MaxRestarts; i++ {
		if _, err := s.Start(ctx, "file:///w", nil); err == nil {
			t.Fatal("a missing binary started successfully")
		}
		s.NoteCrash(now)
	}
	if s.ShouldRestart(now) {
		t.Error("still willing to restart past the cap")
	}
	if !s.GaveUp() {
		t.Error("giving up is not reported, so the editor cannot say so")
	}
}

// The delay grows, or a crash loop runs at the speed of the event loop.
func TestBackoffGrows(t *testing.T) {
	s := &Server{}
	now := time.Now()
	var last time.Duration
	for i := 0; i < 3; i++ {
		s.NoteCrash(now)
		s.mu.Lock()
		d := s.nextTry.Sub(now)
		s.mu.Unlock()
		if i > 0 && d <= last {
			t.Errorf("failure %d waited %v, not more than %v", i+1, d, last)
		}
		last = d
	}
	if last > maxBackoff {
		t.Errorf("backoff %v exceeds the cap %v", last, maxBackoff)
	}
}

// Waiting is required as well as counting: a restart before its time is a
// crash loop with extra steps.
func TestRestartWaitsForItsTurn(t *testing.T) {
	s := &Server{}
	now := time.Now()
	s.NoteCrash(now)
	if s.ShouldRestart(now) {
		t.Error("restarted immediately after a crash")
	}
	if !s.ShouldRestart(now.Add(maxBackoff + time.Second)) {
		t.Error("never became willing to restart")
	}
}

// A success clears the history, or a long session becomes progressively less
// willing to recover from an unrelated crash hours later.
func TestSuccessResetsTheCount(t *testing.T) {
	s := &Server{}
	now := time.Now()
	s.NoteCrash(now)
	s.NoteCrash(now)
	if s.Failures() != 2 {
		t.Fatalf("failures = %d, want 2", s.Failures())
	}
	s.mu.Lock()
	s.failures, s.nextTry = 0, time.Time{} // what a successful Start does
	s.mu.Unlock()
	if !s.ShouldRestart(now) {
		t.Error("a server that just worked is not startable")
	}
	// The pending delay has to go with the count. Leaving it would make a
	// crash hours later wait out a backoff earned before the server worked.
	s.NoteCrash(now)
	s.mu.Lock()
	d := s.nextTry.Sub(now)
	s.mu.Unlock()
	if d > baseBackoff {
		t.Errorf("the first crash after a success waits %v, want the base delay", d)
	}
}

// Stop is final: this is called when the editor is quitting, and a restart
// afterwards would outlive it.
func TestStopIsFinal(t *testing.T) {
	s := &Server{}
	s.Stop()
	if s.ShouldRestart(time.Now().Add(time.Hour)) {
		t.Error("a stopped server offered to restart")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := s.Start(ctx, "file:///w", nil); err != ErrClosed {
		t.Errorf("err = %v, want ErrClosed", err)
	}
	s.Stop() // and safe twice
}

// A live server is not restarted, however long ago it started.
func TestLiveServerIsNotRestarted(t *testing.T) {
	f := newFake(t)
	s := &Server{}
	s.mu.Lock()
	s.conn = f.conn
	s.mu.Unlock()

	if s.ShouldRestart(time.Now().Add(time.Hour)) {
		t.Error("offered to restart a connection that is alive")
	}
	if s.Conn() == nil {
		t.Error("a live connection is reported as absent")
	}

	f.die()
	waitFor(t, func() bool { return s.Conn() == nil })
	if !s.ShouldRestart(time.Now().Add(time.Hour)) {
		t.Error("a dead connection is not restartable")
	}
}

// Starting a real process end to end, using this test binary as the server so
// the test needs nothing installed. It exits immediately, which is the "server
// dies during the handshake" case.
func TestStartHandlesAProcessThatExits(t *testing.T) {
	s := &Server{Command: "true"}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	start := time.Now()
	if _, err := s.Start(ctx, "file:///w", nil); err == nil {
		t.Error("a process that exits immediately handshook successfully")
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("took %v to notice the process was gone", d)
	}
	if s.Failures() == 0 {
		t.Error("the failure was not recorded, so backoff will not apply")
	}
}

// A process that starts but never speaks must time out rather than leave the
// feature pending with no way to tell.
func TestStartTimesOutOnASilentServer(t *testing.T) {
	s := &Server{Command: "sleep", Args: []string{"30"}}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := s.Start(ctx, "file:///w", nil)
	if err == nil {
		t.Fatal("a silent server handshook successfully")
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("waited %v past the deadline", d)
	}
	s.Stop()
}
