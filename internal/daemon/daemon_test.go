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

	"raj/internal/control"
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

// The removed control flags stay removed in what the daemon advertises.
// Driving CLI, not a hand-built FlagSet, keeps this honest about the shipped
// command: the usage names --control-addr and neither --control-socket nor
// --control-exec.
func TestUsageNamesOnlyTheLiveControlFlags(t *testing.T) {
	var out, errb bytes.Buffer
	if code := CLI(nil, &out, &errb); code != 2 {
		t.Fatalf("CLI with no args = %d, want 2", code)
	}
	text := errb.String()
	if !strings.Contains(text, "--control-addr") {
		t.Errorf("usage does not name --control-addr:\n%s", text)
	}
	if !strings.Contains(text, "--no-restore") {
		t.Errorf("usage does not name --no-restore: %s", text)
	}
	for _, gone := range []string{"--control-socket", "--control-exec"} {
		if strings.Contains(text, gone) {
			t.Errorf("usage still names removed flag %s:\n%s", gone, text)
		}
	}
}

// start and restart refuse the removed flags rather than silently ignoring
// them, so a script carrying an old invocation fails loudly.
func TestRemovedControlFlagsAreRefused(t *testing.T) {
	for _, cmd := range []string{"start", "restart"} {
		for _, gone := range []string{"--control-socket", "--control-exec"} {
			var out, errb bytes.Buffer
			if code := CLI([]string{cmd, gone, "x"}, &out, &errb); code == 0 {
				t.Errorf("%s accepted removed flag %s", cmd, gone)
			}
			if !strings.Contains(errb.String(), "not defined") {
				t.Errorf("%s %s: stderr = %q, want the flag refused", cmd, gone, errb.String())
			}
		}
	}
}

// The directory may be typed before or after the control flags; flagsFirst
// moves it to the positional flag.Parse expects while keeping each value-taking
// flag beside its value.
func TestFlagsFirstKeepsDirPositional(t *testing.T) {
	got, err := flagsFirst([]string{"/ws", "--control-addr", "tcp://127.0.0.1:9"}, startSpec)
	if err != nil {
		t.Fatalf("flagsFirst = %v", err)
	}
	want := []string{"--control-addr", "tcp://127.0.0.1:9", "/ws"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("flagsFirst = %q, want %q", got, want)
	}
	got, err = flagsFirst([]string{"--control-addr=tcp://x", "/ws"}, startSpec)
	if err != nil {
		t.Fatalf("flagsFirst with = form = %v", err)
	}
	if strings.Join(got, "\x00") != strings.Join([]string{"--control-addr=tcp://x", "/ws"}, "\x00") {
		t.Errorf("flagsFirst with = form = %q", got)
	}
}

// --no-restore reaches the spawned `daemon run` argv, so a start typed with the
// flag cannot silently restore the previous session.
func TestStartForwardsNoRestore(t *testing.T) {
	root := stateRoot(t)
	var got *exec.Cmd
	r := Runner{Root: root, Exe: "/bin/raj", Log: io.Discard,
		Args: controlForward("tcp://127.0.0.1:9", true), Ops: Ops{
			Alive: func(int) bool { return false },
			Spawn: func(cmd *exec.Cmd) (int, error) { got = cmd; return 7, nil },
		}}
	if _, err := r.Start(); err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("spawn was not called")
	}
	want := []string{"/bin/raj", "daemon", "run", "--control-addr", "tcp://127.0.0.1:9", "--no-restore", root}
	if strings.Join(got.Args, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("argv = %q, want %q", got.Args, want)
	}
}

// An empty control address drops --control-addr but keeps --no-restore, so a
// start that asks only for a fresh session forwards exactly that.
func TestControlForwardCarriesOnlyWhatWasAsked(t *testing.T) {
	if got := controlForward("", false); got != nil {
		t.Errorf("controlForward(none) = %q, want nil", got)
	}
	got := controlForward("", true)
	want := []string{"--no-restore"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("controlForward(no-restore) = %q, want %q", got, want)
	}
}

// A bare token that names a flag is refused with the double-dash spelling
// rather than taken for the workspace directory: `daemon start no-restore`
// must not quietly start a daemon rooted at ./no-restore. A real directory is
// still a directory.
func TestFlagsFirstRefusesBareFlagName(t *testing.T) {
	if _, err := flagsFirst([]string{"no-restore"}, startSpec); err == nil ||
		!strings.Contains(err.Error(), `did you mean --no-restore?`) {
		t.Errorf("flagsFirst(no-restore) = %v, want the double-dash suggestion", err)
	}
	got, err := flagsFirst([]string{"/ws"}, startSpec)
	if err != nil {
		t.Fatalf("flagsFirst(/ws) = %v, want a directory", err)
	}
	if strings.Join(got, "\x00") != "/ws" {
		t.Errorf("flagsFirst(/ws) = %q, want the directory kept", got)
	}
}

// The refusal reaches the shipped command: `raj daemon start no-restore` exits
// as a usage error naming the flag, before anything is spawned.
func TestCLIRefusesBareFlagName(t *testing.T) {
	var out, errb bytes.Buffer
	if code := CLI([]string{"start", "no-restore"}, &out, &errb); code != 2 {
		t.Fatalf("CLI start no-restore = %d, want 2", code)
	}
	if !strings.Contains(errb.String(), `unknown argument "no-restore"`) ||
		!strings.Contains(errb.String(), "did you mean --no-restore?") {
		t.Errorf("stderr = %q, want the bare flag refused", errb.String())
	}
}

// Restart stops the running daemon and then starts a fresh one with the
// recorded TCP address forwarded, so a client that knew the old address keeps
// reaching the daemon across the restart. The socket is per-process and is not
// carried.
func TestRestartForwardsRecordedControl(t *testing.T) {
	root := stateRoot(t)
	if err := Record(root, State{PID: 4242, Socket: "/run/x.sock", TCP: "tcp://127.0.0.1:7391", Token: "tok"}); err != nil {
		t.Fatal(err)
	}
	live := true
	var events []string
	var sig syscall.Signal
	var got *exec.Cmd
	r := Runner{Root: root, Exe: "/bin/raj", Log: io.Discard, Wait: 50 * time.Millisecond, Step: time.Millisecond, Ops: Ops{
		Alive: func(int) bool { return live },
		Signal: func(_ int, s syscall.Signal) error {
			sig = s
			live = false
			events = append(events, "stop")
			return nil
		},
		Spawn: func(cmd *exec.Cmd) (int, error) { got = cmd; events = append(events, "spawn"); return 777, nil },
	}}
	pid, st, err := r.Restart("", false)
	if err != nil {
		t.Fatal(err)
	}
	if pid != 777 {
		t.Errorf("pid = %d, want 777", pid)
	}
	if sig != syscall.SIGTERM {
		t.Errorf("signal = %v, want SIGTERM", sig)
	}
	if strings.Join(events, ",") != "stop,spawn" {
		t.Errorf("ops = %v, want the stop before the spawn", events)
	}
	if got == nil {
		t.Fatal("spawn was not called")
	}
	want := []string{"/bin/raj", "daemon", "run", "--control-addr", "tcp://127.0.0.1:7391", root}
	if strings.Join(got.Args, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("argv = %q, want %q", got.Args, want)
	}
	if st.Socket != "" || st.TCP != "tcp://127.0.0.1:7391" || st.Token != "tok" {
		t.Errorf("restart state = %+v, want the recorded TCP address and token and no socket", st)
	}
}

// The child sees RAJ_CONTROL_TOKEN fixed to the recorded token, and exactly
// once even when the parent already carried a different one: a duplicate would
// leave which value wins to the OS.
func TestRestartPreservesTokenExactlyOnce(t *testing.T) {
	root := stateRoot(t)
	t.Setenv(control.TokenEnv, "stale-parent")
	if err := Record(root, State{PID: 4242, Socket: "/run/x.sock", Token: "recorded"}); err != nil {
		t.Fatal(err)
	}
	live := true
	var got *exec.Cmd
	r := Runner{Root: root, Exe: "/bin/raj", Log: io.Discard, Ops: Ops{
		Alive:  func(int) bool { return live },
		Signal: func(int, syscall.Signal) error { live = false; return nil },
		Spawn:  func(cmd *exec.Cmd) (int, error) { got = cmd; return 1, nil },
	}}
	if _, _, err := r.Restart("", false); err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("spawn was not called")
	}
	var values []string
	for _, kv := range got.Env {
		if k, v, _ := strings.Cut(kv, "="); k == control.TokenEnv {
			values = append(values, v)
		}
	}
	if len(values) != 1 || values[0] != "recorded" {
		t.Errorf("%s entries = %q, want exactly [recorded]", control.TokenEnv, values)
	}
}

// An explicit control address overrides the recorded one; the per-process
// socket is never carried across.
func TestRestartExplicitAddrOverridesRecord(t *testing.T) {
	root := stateRoot(t)
	if err := Record(root, State{PID: 4242, Socket: "/run/x.sock", TCP: "tcp://127.0.0.1:7391"}); err != nil {
		t.Fatal(err)
	}
	live := true
	var got *exec.Cmd
	r := Runner{Root: root, Exe: "/bin/raj", Log: io.Discard, Ops: Ops{
		Alive:  func(int) bool { return live },
		Signal: func(int, syscall.Signal) error { live = false; return nil },
		Spawn:  func(cmd *exec.Cmd) (int, error) { got = cmd; return 1, nil },
	}}
	_, st, err := r.Restart("tcp://127.0.0.1:9999", false)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("spawn was not called")
	}
	want := []string{"/bin/raj", "daemon", "run", "--control-addr", "tcp://127.0.0.1:9999", root}
	if strings.Join(got.Args, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("argv = %q, want the explicit address", got.Args)
	}
	if st.TCP != "tcp://127.0.0.1:9999" {
		t.Errorf("state TCP = %q, want the explicit address", st.TCP)
	}
}

// With nothing running, restart is a start: it spawns with no control flags and
// never signals, because there is no record to carry across.
func TestRestartNoDaemonBehavesLikeStart(t *testing.T) {
	root := stateRoot(t)
	signalled := false
	var got *exec.Cmd
	r := Runner{Root: root, Exe: "/bin/raj", Log: io.Discard, Ops: Ops{
		Alive:  func(int) bool { return false },
		Signal: func(int, syscall.Signal) error { signalled = true; return nil },
		Spawn:  func(cmd *exec.Cmd) (int, error) { got = cmd; return 42, nil },
	}}
	pid, st, err := r.Restart("", false)
	if err != nil {
		t.Fatal(err)
	}
	if signalled {
		t.Error("restart signalled with no daemon running")
	}
	if pid != 42 {
		t.Errorf("pid = %d, want 42", pid)
	}
	if got == nil {
		t.Fatal("spawn was not called")
	}
	want := []string{"/bin/raj", "daemon", "run", root}
	if strings.Join(got.Args, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("argv = %q, want a plain start", got.Args)
	}
	if st.Socket != "" || st.TCP != "" || st.Token != "" {
		t.Errorf("state = %+v, want no carried control facts", st)
	}
}
