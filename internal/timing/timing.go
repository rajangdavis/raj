// Package timing is the environment-gated instrument for the save-review lag.
//
// It exists to answer one question with wall-clock numbers before anyone
// proposes a fix: where the beat between a save clearing a buffer's proposals
// and the tab losing its dirty mark actually goes. With the gate shut every
// hook is a single boolean test and the editor writes nothing; point RAJ_TIMING
// at a file and every line goes there as
//
//	raj timing: <what> <duration> <key=value ...>
//
// The instrument never writes to stdout or stderr, which in the full-screen
// editor are the terminal the TUI paints on. The gate is read once at startup
// so the hot path never calls os.Getenv.
package timing

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

// Env names the gate, following the RAJ_ naming of RAJ_IDENTITY and
// RAJ_CONTROL_TOKEN. Unset or empty is off; "1" opens the default log file;
// any other value is the log path. The gate is read once at startup, so each
// hook is one boolean test.
const Env = "RAJ_TIMING"

// defaultPath is the file RAJ_TIMING=1 logs to. It lives under the user cache
// dir so a run leaves a known artifact, and falls back to the temp dir when the
// cache dir is unavailable. It is a var so a test can substitute a temp path.
var defaultPath = func() string {
	if dir, err := os.UserCacheDir(); err == nil {
		return filepath.Join(dir, "raj", "timing.log")
	}
	return filepath.Join(os.TempDir(), "raj-timing.log")
}

// On reports whether the instrument is enabled, and Out is where its lines go.
// Both are read once at startup so each hook is one boolean test; they stay
// vars so a test can flip them without a process boundary.
//
// If the chosen file cannot be opened, On is false and Out is io.Discard: the
// instrument never falls back to stderr, stdout or the terminal, because in the
// full-screen editor those are the terminal the TUI paints on. A silent no-op
// is the only acceptable failure.
var On, Out = openOut(os.Getenv(Env))

// openOut resolves the gate and the writer. An empty value is off; "1" uses
// defaultPath and creates its parent; any other value is taken as the log path.
// The file is opened O_CREATE|O_WRONLY|O_TRUNC with mode 0o644 so each run is a
// fresh log. Any error yields (false, io.Discard).
func openOut(v string) (bool, io.Writer) {
	if !enabled(v) {
		return false, io.Discard
	}
	path, mkdir := v, false
	if v == "1" {
		path, mkdir = defaultPath(), true
	}
	if mkdir {
		if dir := filepath.Dir(path); dir != "" {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return false, io.Discard
			}
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return false, io.Discard
	}
	return true, f
}

// enabled parses the gate. Split out so a test can check the env rule directly
// rather than only through a value fixed at init.
func enabled(v string) bool { return v != "" }

// Log writes one timing line when the instrument is on. what names the span
// ("draw", "write-atomic", "save"); d is its wall-clock length; kv is an even
// list of key/value pairs for the phases inside it, with time.Duration values
// rounded to the microsecond. A call with the gate shut does nothing, which is
// why a cold path may call it without its own guard.
func Log(what string, d time.Duration, kv ...any) {
	if !On {
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "raj timing: %-12s %10s", what, round(d))
	for i := 0; i+1 < len(kv); i += 2 {
		fmt.Fprintf(&b, "  %v=", kv[i])
		if v, ok := kv[i+1].(time.Duration); ok {
			b.WriteString(round(v))
		} else {
			fmt.Fprintf(&b, "%v", kv[i+1])
		}
	}
	b.WriteByte('\n')
	io.WriteString(Out, b.String())
}

// round trims a duration to microseconds: the instrument is measuring
// millisecond beats, and nanoseconds past that only make the lines noisy.
func round(d time.Duration) string { return d.Round(time.Microsecond).String() }

// pendingNanos and pendingCalls accumulate the pending-change journal walk
// inside one frame. PendingMarks adds as it runs; Draw resets before it paints
// and takes after, so work done between frames is not billed to the frame.
//
// The counters are process-global and atomic. Rendering is single-threaded in
// production, but the accumulator is shared by every app in the process: a
// client's start-up can walk pending marks on another goroutine while a second
// app (a test, a harness) draws, so a plain pair is not enough.
var (
	pendingNanos atomic.Int64
	pendingCalls atomic.Int64
)

// AddPending records one pending-marks walk. Callers guard with On so a shut
// gate pays nothing.
func AddPending(d time.Duration) {
	pendingNanos.Add(int64(d))
	pendingCalls.Add(1)
}

// ResetPending clears the accumulator at the top of a frame.
func ResetPending() {
	pendingNanos.Store(0)
	pendingCalls.Store(0)
}

// TakePending returns and clears the accumulated pending-walk time and count.
// Each swap is take-and-clear in one step, so an Add racing it lands in the
// next frame rather than being lost.
func TakePending() (time.Duration, int) {
	d := pendingNanos.Swap(0)
	n := pendingCalls.Swap(0)
	return time.Duration(d), int(n)
}
