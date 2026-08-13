package lsp

import (
	"context"
	"os/exec"
	"sync"
	"time"
)

// Server is a language server process and its connection.
//
// Restarting is deliberately not automatic and not immediate. A server that
// crashes during startup — a missing toolchain, an unreadable config, a version
// mismatch — crashes every time, and an editor that respawns it on exit turns
// that into a fork bomb that is very hard to diagnose from inside the editor.
// So restarts are counted, spaced by a growing delay, and give up.
type Server struct {
	Command string
	Args    []string
	Dir     string

	// Notify wakes the event loop when the server has something to say.
	Notify func()

	mu       sync.Mutex
	conn     *Conn
	cmd      *exec.Cmd
	failures int
	nextTry  time.Time
	gone     bool
}

// Restart policy. The first retry is quick because a one-off crash is worth
// recovering from invisibly; the delay grows because a repeated one is not a
// one-off, and the cap exists so the editor stops trying rather than retrying
// forever at a leisurely pace.
const (
	MaxRestarts  = 4
	baseBackoff  = 500 * time.Millisecond
	maxBackoff   = 30 * time.Second
	startTimeout = 60 * time.Second // indexing a large repository is slow, not broken
)

// Start spawns the process and performs the handshake.
//
// A failure to start is not an error the caller has to handle specially: the
// editor works without a language server, so every caller treats "no server" as
// a state rather than a problem, and this returns the reason for logging rather
// than for recovery.
func (s *Server) Start(ctx context.Context, rootURI string, caps any) (*InitializeResult, error) {
	s.mu.Lock()
	if s.gone {
		s.mu.Unlock()
		return nil, ErrClosed
	}
	s.mu.Unlock()

	cmd := exec.Command(s.Command, s.Args...)
	cmd.Dir = s.Dir
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	// Stderr is deliberately discarded rather than merged into stdout: servers
	// log freely there, and a single stray line on the protocol stream would
	// desynchronise the framing permanently.
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	conn := NewConn(stdin, stdout, func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}, s.Notify)

	s.mu.Lock()
	s.conn, s.cmd = conn, cmd
	s.mu.Unlock()

	res, err := conn.Initialize(ctx, rootURI, caps)
	if err != nil {
		conn.Close()
		s.mu.Lock()
		s.failures++
		s.nextTry = time.Now().Add(s.backoffLocked())
		s.mu.Unlock()
		return nil, err
	}
	s.mu.Lock()
	// A successful handshake clears the history: a server that has just worked
	// is not one that has been failing, and carrying the count forward would
	// make a long session progressively less willing to recover. The pending
	// delay goes too — leaving it would make a crash hours later wait out a
	// backoff earned before the server ever worked.
	s.failures, s.nextTry = 0, time.Time{}
	s.mu.Unlock()
	return res, nil
}

// Conn is the live connection, or nil when there is none.
func (s *Server) Conn() *Conn {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil || s.conn.Closed() {
		return nil
	}
	return s.conn
}

// ShouldRestart reports whether the server is worth starting again now.
//
// Two conditions, and both matter. Enough time has to have passed, or a crash
// loop runs at the speed of the event loop. And the failure count has to be
// under the cap, or a server that cannot start keeps being started for the rest
// of the session.
func (s *Server) ShouldRestart(now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gone || s.failures >= MaxRestarts {
		return false
	}
	if s.conn != nil && !s.conn.Closed() {
		return false // still alive
	}
	return !now.Before(s.nextTry)
}

// NoteCrash records that the connection died, so the next restart is spaced.
func (s *Server) NoteCrash(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures++
	s.nextTry = now.Add(s.backoffLocked())
}

// Failures is how many times starting has failed since the last success.
func (s *Server) Failures() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failures
}

// GaveUp reports that the server will not be started again. The editor shows
// this once rather than retrying silently, because a language server that is
// never coming back is something the user should be able to find out.
func (s *Server) GaveUp() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.gone && s.failures >= MaxRestarts
}

// backoffLocked doubles per failure, capped. Caller holds mu.
func (s *Server) backoffLocked() time.Duration {
	d := baseBackoff
	for i := 1; i < s.failures && d < maxBackoff; i++ {
		d *= 2
	}
	if d > maxBackoff {
		d = maxBackoff
	}
	return d
}

// Stop shuts the server down for good. Further starts are refused, since this
// is called when the editor is quitting or the workspace is closing.
func (s *Server) Stop() {
	s.mu.Lock()
	conn := s.conn
	s.gone = true
	s.conn = nil
	s.mu.Unlock()
	if conn != nil {
		conn.Close()
	}
}
