package hooks

import "sync"

// DefaultLogSize is how many completed runs a Log keeps. The run log is a
// convenience for an unattended agent and the person watching it: enough recent
// history to cite a result, bounded so a long session cannot grow without end.
const DefaultLogSize = 100

// Result is one completed hook run as the log records it: the stamp the final
// frame carries, plus who asked for it. It is the in-memory answer to "what did
// the last run say", so a caller can cite a run without re-running it.
type Result struct {
	ID         uint64 `json:"id"`
	Hook       string `json:"hook"`
	Author     uint8  `json:"author"`
	Revision   uint64 `json:"revision"`
	Head       string `json:"head"`
	Dirty      string `json:"dirty"`
	Exit       int    `json:"exit"`
	DurationMS int64  `json:"duration_ms"`
	Truncated  bool   `json:"truncated"`
	// Detach marks a run started in its own session, whose output went to
	// LogPath rather than the reply; LogPath and PID name the file and the
	// process a caller can inspect or stop.
	Detach  bool   `json:"detach,omitempty"`
	LogPath string `json:"log,omitempty"`
	PID     int    `json:"pid,omitempty"`
	// Recovered marks a result read from a run's exit file after the editor
	// restarted under it; Lost marks a detached run whose process is gone with
	// no exit file.
	Recovered bool `json:"recovered,omitempty"`
	Lost      bool `json:"lost,omitempty"`
	// Err is how a run ended when it did not simply exit: a timeout, a
	// cancel, or a start failure. Empty for a run that exited on its own,
	// whatever its status, so a timed-out run is never read as exit N.
	Err string `json:"err,omitempty"`
}

// Log is a bounded, oldest-first ring of completed runs. It is safe for
// concurrent use: a run finishes on the connection goroutine that carried it
// while a `hook log` read may arrive on another.
type Log struct {
	mu      sync.Mutex
	max     int
	entries []Result
}

// NewLog builds a Log that keeps at most max runs; a non-positive max takes
// DefaultLogSize, so a caller that wants the shipped size can pass zero.
func NewLog(max int) *Log {
	if max <= 0 {
		max = DefaultLogSize
	}
	return &Log{max: max}
}

// Add appends r, dropping the oldest entries once the ring is full.
func (l *Log) Add(r Result) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, r)
	if n := len(l.entries) - l.max; n > 0 {
		l.entries = l.entries[n:]
	}
}

// List returns a snapshot of the logged runs, oldest first. It is never nil.
func (l *Log) List() []Result {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]Result, len(l.entries))
	copy(out, l.entries)
	return out
}

// Len returns how many runs the log currently holds.
func (l *Log) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.entries)
}
