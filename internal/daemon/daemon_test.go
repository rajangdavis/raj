package daemon

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// stateRoot is a temp workspace root with a temp XDG state home, so a test's
// daemon files never touch the real state directory.
func stateRoot(t *testing.T) string {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	return t.TempDir()
}

// A pidfile round-trips, is created 0600, and a missing or malformed one reads
// as "no pid" rather than as pid 0.
func TestPIDFileRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "daemon.pid")
	if pid, ok := ReadPID(p); ok {
		t.Fatalf("missing pidfile read as pid %d", pid)
	}
	if err := WritePID(p, 4321); err != nil {
		t.Fatal(err)
	}
	pid, ok := ReadPID(p)
	if !ok || pid != 4321 {
		t.Fatalf("ReadPID = %d/%v, want 4321/true", pid, ok)
	}
	if fi, err := os.Stat(p); err != nil {
		t.Fatal(err)
	} else if fi.Mode().Perm() != 0o600 {
		t.Errorf("pidfile perm = %v, want 0600", fi.Mode().Perm())
	}
	if err := os.WriteFile(p, []byte("not-a-pid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if pid, ok := ReadPID(p); ok {
		t.Errorf("malformed pidfile read as pid %d", pid)
	}
}

// The JSON state carries the socket, TCP address and token, and is written
// 0600 because the token is a secret.
func TestStateRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "daemon.json")
	in := State{PID: 7, Socket: "/run/x.sock", TCP: "tcp://127.0.0.1:7391", Token: "secret"}
	if err := WriteState(p, in); err != nil {
		t.Fatal(err)
	}
	got, ok := ReadState(p)
	if !ok {
		t.Fatal("ReadState = false, want the record")
	}
	if got != in {
		t.Errorf("state = %+v, want %+v", got, in)
	}
	if fi, err := os.Stat(p); err != nil {
		t.Fatal(err)
	} else if fi.Mode().Perm() != 0o600 {
		t.Errorf("state perm = %v, want 0600", fi.Mode().Perm())
	}
}

// A pidfile whose process is gone is a stale record: status reports stopped
// and removes it, which is what lets a crashed daemon restart.
func TestStatusStaleRecordReadsStoppedAndCleans(t *testing.T) {
	root := stateRoot(t)
	paths := ForRoot(root)
	if paths.PID == "" {
		t.Fatal("no state dir for a temp root")
	}
	if err := WritePID(paths.PID, 999999); err != nil {
		t.Fatal(err)
	}
	if err := WriteState(paths.State, State{PID: 999999, Socket: "/run/x.sock"}); err != nil {
		t.Fatal(err)
	}
	r := Runner{Root: root, Ops: Ops{Alive: func(int) bool { return false }}}
	if _, running := r.Status(); running {
		t.Fatal("a stale pidfile read as running")
	}
	if _, err := os.Stat(paths.PID); !os.IsNotExist(err) {
		t.Error("stale pidfile was not removed")
	}
	if _, err := os.Stat(paths.State); !os.IsNotExist(err) {
		t.Error("stale state was not removed")
	}
}

// A running daemon's record reads back with its socket, TCP address and token;
// a stale worker record alone never blocks a restart.
func TestStatusRunningReadsState(t *testing.T) {
	root := stateRoot(t)
	paths := ForRoot(root)
	if err := WritePID(paths.PID, 606); err != nil {
		t.Fatal(err)
	}
	if err := WriteState(paths.State, State{PID: 606, Socket: "/run/x.sock", TCP: "tcp://127.0.0.1:7391", Token: "tok"}); err != nil {
		t.Fatal(err)
	}
	r := Runner{Root: root, Ops: Ops{Alive: func(int) bool { return true }}}
	s, running := r.Status()
	if !running {
		t.Fatal("Status = stopped, want running")
	}
	if s.PID != 606 || s.Socket != "/run/x.sock" || s.TCP != "tcp://127.0.0.1:7391" || s.Token != "tok" {
		t.Errorf("status = %+v", s)
	}
}

// Start refuses a live daemon and never spawns a second one. Without the
// liveness check the second Start would overwrite the pidfile and orphan the
// first daemon.
func TestStartRefusesLiveDaemon(t *testing.T) {
	root := stateRoot(t)
	if err := WritePID(ForRoot(root).PID, 4242); err != nil {
		t.Fatal(err)
	}
	spawned := false
	r := Runner{Root: root, Log: io.Discard, Ops: Ops{
		Alive: func(int) bool { return true },
		Spawn: func(*exec.Cmd) (int, error) { spawned = true; return 1, nil },
	}}
	_, err := r.Start()
	if err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("Start = %v, want already running", err)
	}
	if spawned {
		t.Error("Start spawned despite a live daemon")
	}
}

// A stale pidfile from a crashed daemon does not block a restart: Start
// removes it and spawns.
func TestStartStaleRecordDoesNotBlock(t *testing.T) {
	root := stateRoot(t)
	if err := WritePID(ForRoot(root).PID, 999999); err != nil {
		t.Fatal(err)
	}
	r := Runner{Root: root, Exe: "/bin/raj", Log: io.Discard, Ops: Ops{
		Alive: func(int) bool { return false },
		Spawn: func(*exec.Cmd) (int, error) { return 12, nil },
	}}
	if _, err := r.Start(); err != nil {
		t.Fatalf("Start with a stale pidfile = %v", err)
	}
}

// Start builds the detached `daemon run` command with the forwarded control
// flags and the root, spawns it, and writes its pidfile. Inspecting the
// constructed command is why Start is split from the OS spawn.
func TestStartSpawnsDetachedAndWritesPID(t *testing.T) {
	root := stateRoot(t)
	var got *exec.Cmd
	r := Runner{Root: root, Exe: "/bin/raj", Args: []string{"--control-addr", "tcp://127.0.0.1:9"}, Log: io.Discard, Ops: Ops{
		Alive: func(int) bool { return false },
		Spawn: func(cmd *exec.Cmd) (int, error) { got = cmd; return 5150, nil },
	}}
	pid, err := r.Start()
	if err != nil {
		t.Fatal(err)
	}
	if pid != 5150 {
		t.Fatalf("pid = %d, want 5150", pid)
	}
	if got == nil {
		t.Fatal("spawn was not called")
	}
	want := []string{"/bin/raj", "daemon", "run", "--control-addr", "tcp://127.0.0.1:9", root}
	if strings.Join(got.Args, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("argv = %q, want %q", got.Args, want)
	}
	if got.SysProcAttr == nil || !got.SysProcAttr.Setsid {
		t.Error("daemon command is not in its own session")
	}
	if got.Stdin != nil {
		t.Error("daemon command does not use the null device for stdin")
	}
	if read, ok := ReadPID(ForRoot(root).PID); !ok || read != 5150 {
		t.Errorf("pidfile = %d/%v, want 5150/true", read, ok)
	}
}

// Detached is the command shape itself: argv, a new session, null stdin, and
// the log for both streams.
func TestDetachedCommand(t *testing.T) {
	var log bytes.Buffer
	cmd := Detached("/bin/raj", []string{"daemon", "run", "/ws"}, &log)
	if got := strings.Join(cmd.Args, " "); got != "/bin/raj daemon run /ws" {
		t.Errorf("argv = %q", got)
	}
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setsid {
		t.Error("command is not in its own session")
	}
	if cmd.Stdin != nil {
		t.Error("command does not use the null device for stdin")
	}
	if cmd.Stdout != &log || cmd.Stderr != &log {
		t.Error("command output does not go to the log")
	}
}

// Stop on a workspace with no daemon is a clean no-op: no signal, no error.
func TestStopNoopWhenNotRunning(t *testing.T) {
	root := stateRoot(t)
	signalled := false
	r := Runner{Root: root, Ops: Ops{
		Alive:  func(int) bool { return false },
		Signal: func(int, syscall.Signal) error { signalled = true; return nil },
	}}
	stopped, err := r.Stop()
	if err != nil {
		t.Fatal(err)
	}
	if stopped {
		t.Error("Stop reported a stop with no daemon")
	}
	if signalled {
		t.Error("Stop signalled with no daemon")
	}
}

// Stop signals SIGTERM, waits for the process to go, and removes the record.
func TestStopSignalsAndRemoves(t *testing.T) {
	root := stateRoot(t)
	paths := ForRoot(root)
	if err := WritePID(paths.PID, 31337); err != nil {
		t.Fatal(err)
	}
	if err := WriteState(paths.State, State{PID: 31337, Socket: "/run/x.sock"}); err != nil {
		t.Fatal(err)
	}
	live := true
	var sig syscall.Signal
	r := Runner{Root: root, Wait: 50 * time.Millisecond, Step: time.Millisecond, Ops: Ops{
		Alive:  func(int) bool { return live },
		Signal: func(_ int, s syscall.Signal) error { sig = s; live = false; return nil },
	}}
	stopped, err := r.Stop()
	if err != nil {
		t.Fatal(err)
	}
	if !stopped {
		t.Error("Stop = false, want a reported stop")
	}
	if sig != syscall.SIGTERM {
		t.Errorf("signal = %v, want SIGTERM", sig)
	}
	if _, err := os.Stat(paths.PID); !os.IsNotExist(err) {
		t.Error("pidfile was not removed")
	}
	if _, err := os.Stat(paths.State); !os.IsNotExist(err) {
		t.Error("state was not removed")
	}
}

// A process that ignores SIGTERM cannot trap Stop forever: it gives up after
// Wait and says so.
func TestStopBoundsWait(t *testing.T) {
	root := stateRoot(t)
	if err := WritePID(ForRoot(root).PID, 31337); err != nil {
		t.Fatal(err)
	}
	r := Runner{Root: root, Wait: 15 * time.Millisecond, Step: time.Millisecond, Ops: Ops{
		Alive:  func(int) bool { return true },
		Signal: func(int, syscall.Signal) error { return nil },
	}}
	stopped, err := r.Stop()
	if err == nil {
		t.Fatal("Stop did not time out on a process that never exits")
	}
	if !stopped {
		t.Error("Stop = false, want it to report it signalled one")
	}
}

// Record and Clear are what the running daemon uses: the pair round-trips and
// then leaves nothing behind.
func TestRecordAndClear(t *testing.T) {
	root := stateRoot(t)
	if err := Record(root, State{PID: 99, Socket: "/run/x.sock", Token: "t"}); err != nil {
		t.Fatal(err)
	}
	if pid, ok := ReadPID(ForRoot(root).PID); !ok || pid != 99 {
		t.Errorf("pidfile = %d/%v, want 99/true", pid, ok)
	}
	s, ok := ReadState(ForRoot(root).State)
	if !ok || s.PID != 99 || s.Socket != "/run/x.sock" || s.Token != "t" {
		t.Errorf("state = %+v/%v", s, ok)
	}
	Clear(root)
	if _, err := os.Stat(ForRoot(root).PID); !os.IsNotExist(err) {
		t.Error("Clear left the pidfile")
	}
	if _, err := os.Stat(ForRoot(root).State); !os.IsNotExist(err) {
		t.Error("Clear left the state")
	}
}

// ForRoot is empty without a state home, so a caller can treat "no state dir"
// as "no daemon" rather than building a relative path.
func TestForRootNoStateHome(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "")
	if p := ForRoot("/ws"); p.PID != "" || p.State != "" || p.Log != "" {
		t.Errorf("ForRoot without a state home = %+v, want empty", p)
	}
}

// status reports a live daemon's pid, socket, TCP address and token. It uses
// the test process's own pid as the recorded one, so the real liveness check
// succeeds without a spawned daemon.
func TestStatusCLIReportsRunning(t *testing.T) {
	root := stateRoot(t)
	paths := ForRoot(root)
	if err := WritePID(paths.PID, os.Getpid()); err != nil {
		t.Fatal(err)
	}
	if err := WriteState(paths.State, State{PID: os.Getpid(), Socket: "/run/x.sock", TCP: "tcp://127.0.0.1:7391", Token: "tok"}); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := statusCLI([]string{root}, &out, &errb); code != 0 {
		t.Fatalf("status exit = %d, stderr = %s", code, errb.String())
	}
	for _, want := range []string{"running", "/run/x.sock", "tcp://127.0.0.1:7391", "tok"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("status output %q does not mention %q", out.String(), want)
		}
	}
}

// status with no daemon says stopped and exits non-zero, so a script can
// branch on it.
func TestStatusCLIStopped(t *testing.T) {
	root := stateRoot(t)
	var out, errb bytes.Buffer
	if code := statusCLI([]string{root}, &out, &errb); code == 0 {
		t.Fatalf("status exit = 0 for a stopped daemon")
	}
	if !strings.Contains(out.String(), "stopped") {
		t.Errorf("status output = %q, want stopped", out.String())
	}
}

// The directory may be typed before or after the control flags; flagsFirst
// moves it to the positional flag.Parse expects while keeping each value-taking
// flag beside its value.
func TestFlagsFirstKeepsDirPositional(t *testing.T) {
	got := flagsFirst([]string{"/ws", "--control-addr", "tcp://127.0.0.1:9"})
	want := []string{"--control-addr", "tcp://127.0.0.1:9", "/ws"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("flagsFirst = %q, want %q", got, want)
	}
	got = flagsFirst([]string{"--control-addr=tcp://x", "/ws"})
	if strings.Join(got, "\x00") != strings.Join([]string{"--control-addr=tcp://x", "/ws"}, "\x00") {
		t.Errorf("flagsFirst with = form = %q", got)
	}
}
