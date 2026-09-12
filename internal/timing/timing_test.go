package timing

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The instrument must be silent with the gate shut, so a normal run writes
// nothing to stderr and the hooks are safe to leave in the save and draw paths.
func TestLogIsSilentWhenOff(t *testing.T) {
	oldOn, oldOut := On, Out
	On, Out = false, &bytes.Buffer{}
	defer func() { On, Out = oldOn, oldOut }()

	Log("draw", 2*time.Millisecond, "pending", time.Millisecond, "calls", 3)

	if buf := Out.(*bytes.Buffer); buf.Len() != 0 {
		t.Fatalf("wrote %q with the gate shut, want nothing", buf.String())
	}
}

// With the gate open the line names the span and carries the phases, so a
// captured log is self-describing.
func TestLogWritesTheSpanAndPhases(t *testing.T) {
	oldOn, oldOut := On, Out
	var buf bytes.Buffer
	On, Out = true, &buf
	defer func() { On, Out = oldOn, oldOut }()

	Log("write-atomic", 3*time.Millisecond, "fsync", time.Millisecond, "calls", 2)

	got := buf.String()
	for _, want := range []string{"raj timing:", "write-atomic", "fsync=1ms", "calls=2"} {
		if !strings.Contains(got, want) {
			t.Errorf("line %q missing %q", got, want)
		}
	}
}

// The env rule is the only thing that decides the default, and an unset or
// empty variable must leave the instrument off.
func TestEnabledOnlyForNonEmpty(t *testing.T) {
	if enabled("") {
		t.Error("empty env enabled the instrument")
	}
	if !enabled("1") {
		t.Error("a set env did not enable the instrument")
	}
}

// An empty value must leave the gate shut and drop every line.
func TestOpenOutEmptyIsOff(t *testing.T) {
	on, out := openOut("")
	if on {
		t.Error("empty env enabled the instrument")
	}
	if out != io.Discard {
		t.Errorf("Out = %v, want io.Discard", out)
	}
}

// A path value is honoured: lines land in that file and never on stderr.
func TestOpenOutPathWritesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "timing.log")
	on, out := openOut(path)
	if !on {
		t.Fatalf("openOut(%q) = off, want on", path)
	}
	f, ok := out.(*os.File)
	if !ok {
		t.Fatalf("Out = %T, want *os.File", out)
	}
	t.Cleanup(func() { f.Close() })
	if out == os.Stderr || out == os.Stdout {
		t.Fatalf("Out = %v, want the log file", out)
	}

	oldOn, oldOut := On, Out
	On, Out = on, out
	defer func() { On, Out = oldOn, oldOut }()

	Log("draw", 2*time.Millisecond, "calls", 3)

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	for _, want := range []string{"draw", "calls=3"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("log %q missing %q", got, want)
		}
	}
}

// "1" selects the documented default; defaultPath is a seam so this test can
// point it at a temp path.
func TestOpenOutOneUsesDefaultPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "raj", "timing.log")
	oldPath := defaultPath
	defaultPath = func() string { return path }
	defer func() { defaultPath = oldPath }()

	on, out := openOut("1")
	if !on {
		t.Fatal(`openOut("1") = off, want on`)
	}
	f, ok := out.(*os.File)
	if !ok {
		t.Fatalf("Out = %T, want *os.File", out)
	}
	t.Cleanup(func() { f.Close() })
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("default log not created: %v", err)
	}
}

// An unopenable path shuts the gate: a silent no-op, never a terminal fallback.
func TestOpenOutUnopenableIsOff(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "timing.log")
	on, out := openOut(path)
	if on {
		t.Fatalf("openOut(%q) = on, want off", path)
	}
	if out != io.Discard {
		t.Errorf("Out = %v, want io.Discard", out)
	}
}

// The per-frame accumulator must clear between frames so work done outside a
// frame is not billed to one.
func TestPendingAccumulatorResets(t *testing.T) {
	AddPending(time.Millisecond)
	AddPending(2 * time.Millisecond)
	if d, n := TakePending(); d != 3*time.Millisecond || n != 2 {
		t.Fatalf("TakePending = %v, %d; want 3ms, 2", d, n)
	}
	if d, n := TakePending(); d != 0 || n != 0 {
		t.Fatalf("second TakePending = %v, %d; want zero", d, n)
	}
}
