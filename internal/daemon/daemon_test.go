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
	"raj/internal/session"
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
	in := State{PID: 7, Socket: "/run/x.sock", TCP: "tcp://127.0.0.1:7391", Token: "secret", Workspace: "alpha", Roots: []string{"/ws"}}
	if err := WriteState(p, in); err != nil {
		t.Fatal(err)
	}
	got, ok := ReadState(p)
	if !ok {
		t.Fatal("ReadState = false, want the record")
	}
	// Compared field by field, not as a struct: Roots is a slice, so State is
	// not comparable with == any more.
	if got.PID != in.PID || got.Socket != in.Socket || got.TCP != in.TCP || got.Token != in.Token ||
		got.Workspace != in.Workspace || strings.Join(got.Roots, ",") != strings.Join(in.Roots, ",") {
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
	r := Runner{Roots: []string{root}, Ops: Ops{Alive: func(int) bool { return false }}}
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
	r := Runner{Roots: []string{root}, Ops: Ops{Alive: func(int) bool { return true }}}
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
	r := Runner{Roots: []string{root}, Log: io.Discard, Ops: Ops{
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
	r := Runner{Roots: []string{root}, Exe: "/bin/raj", Log: io.Discard, Ops: Ops{
		Alive: func(int) bool { return false },
		Spawn: spawnRecording(root, 12),
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
	r := Runner{Roots: []string{root}, Exe: "/bin/raj", Args: []string{"--control-addr", "tcp://127.0.0.1:9"}, Log: io.Discard, Ops: Ops{
		Alive: func(int) bool { return false },
		Spawn: spawnRecording(root, 5150, func(cmd *exec.Cmd) { got = cmd }),
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
	r := Runner{Roots: []string{root}, Ops: Ops{
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
	r := Runner{Roots: []string{root}, Wait: 50 * time.Millisecond, Step: time.Millisecond, Ops: Ops{
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
	r := Runner{Roots: []string{root}, Wait: 15 * time.Millisecond, Step: time.Millisecond, Ops: Ops{
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
	if err := Record([]string{root}, State{PID: 99, Socket: "/run/x.sock", Token: "t"}); err != nil {
		t.Fatal(err)
	}
	if pid, ok := ReadPID(ForRoot(root).PID); !ok || pid != 99 {
		t.Errorf("pidfile = %d/%v, want 99/true", pid, ok)
	}
	s, ok := ReadState(ForRoot(root).State)
	if !ok || s.PID != 99 || s.Socket != "/run/x.sock" || s.Token != "t" {
		t.Errorf("state = %+v/%v", s, ok)
	}
	if len(s.Roots) != 1 || s.Roots[0] != root {
		t.Errorf("Record roots = %v, want [%s]", s.Roots, root)
	}
	Clear([]string{root})
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

// ForRoots keys the daemon files by the whole root set: a two-root daemon has a
// different state directory from either member, so a subset command never
// reaches its record, and the order the roots were typed does not matter.
func TestForRootsKeysByTheWholeSet(t *testing.T) {
	stateRoot(t)
	a, b := t.TempDir(), t.TempDir()

	both := ForRoots([]string{a, b})
	if both.PID == "" {
		t.Fatal("no daemon files for a two-root set")
	}
	if got, want := filepath.Dir(both.PID), session.StateDirForRoots([]string{a, b}); got != want {
		t.Errorf("daemon dir = %q, want session.StateDirForRoots of the set %q", got, want)
	}
	if both == ForRoot(a) || both == ForRoot(b) {
		t.Errorf("two-root files %+v collide with a single-root one", both)
	}
	if ForRoots([]string{a, b}) != ForRoots([]string{b, a}) {
		t.Error("root order changed the daemon files")
	}
}

// ResolveRoots canonicalises each argument, keeps caller order, drops an exact
// duplicate, and rejects a root nested inside another in either order. Without
// the workspace.New pass a daemon could be started for a set that is really one
// tree.
func TestResolveRootsOrderDedupeAndNesting(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	got, err := ResolveRoots([]string{b, a, b})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != b || got[1] != a {
		t.Errorf("roots = %v, want [%s %s] in typed order with the duplicate dropped", got, b, a)
	}

	parent := t.TempDir()
	child := filepath.Join(parent, "sub")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveRoots([]string{parent, child}); err == nil {
		t.Error("ResolveRoots accepted a root nested inside another")
	}
	if _, err := ResolveRoots([]string{child, parent}); err == nil {
		t.Error("ResolveRoots accepted a nested root given first")
	}
}

// With no arguments a daemon command acts on the working directory, the same
// default as before.
func TestResolveRootsNoArgsIsTheWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(old)
	want, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	got, err := ResolveRoots(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != want {
		t.Errorf("ResolveRoots() = %v, want the working directory [%s]", got, want)
	}
}

// The label a start or restart writes is the explicit one or the running
// daemon's; a start has nothing to carry. Pure, so the choice is pinned without
// a spawn.
func TestDaemonLabelCarriesOnlyWhenAsked(t *testing.T) {
	if got := daemonLabel("new", "old"); got != "new" {
		t.Errorf("daemonLabel(explicit) = %q, want new", got)
	}
	if got := daemonLabel("", "old"); got != "old" {
		t.Errorf("daemonLabel(carry) = %q, want old", got)
	}
	if got := daemonLabel("", ""); got != "" {
		t.Errorf("daemonLabel(none) = %q, want empty", got)
	}
}

// The detached `daemon run` argv carries every root in the typed order after the
// forwarded flags, so the child serves exactly the set the parent resolved.
func TestStartSpawnsEveryRootInOrder(t *testing.T) {
	one, two := stateRoot(t), t.TempDir()
	var got *exec.Cmd
	r := Runner{Roots: []string{one, two}, Exe: "/bin/raj", Args: []string{"--no-restore"}, Log: io.Discard, Ops: Ops{
		Alive: func(int) bool { return false },
		Spawn: spawnRecordingRoots([]string{one, two}, 6001, func(cmd *exec.Cmd) { got = cmd }),
	}}
	pid, err := r.Start()
	if err != nil {
		t.Fatal(err)
	}
	if pid != 6001 {
		t.Fatalf("pid = %d, want 6001", pid)
	}
	if got == nil {
		t.Fatal("spawn was not called")
	}
	want := []string{"/bin/raj", "daemon", "run", "--no-restore", one, two}
	if strings.Join(got.Args, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("argv = %q, want %q", got.Args, want)
	}
}

// stop and status act on the exact root set: a daemon recorded for {a,b} is
// running for `status a b` and stopped for the subset `status a`, because the
// subset keys a different state directory.
func TestStatusCLIUsesTheExactRootSet(t *testing.T) {
	a, b := stateRoot(t), t.TempDir()
	paths := ForRoots([]string{a, b})
	if err := WritePID(paths.PID, os.Getpid()); err != nil {
		t.Fatal(err)
	}
	if err := WriteState(paths.State, State{PID: os.Getpid(), Socket: "/run/x.sock"}); err != nil {
		t.Fatal(err)
	}

	var out, errb bytes.Buffer
	if code := statusCLI([]string{a, b}, &out, &errb); code != 0 {
		t.Fatalf("status for the exact set = %d, stderr = %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "running") {
		t.Errorf("status output = %q, want running", out.String())
	}

	out.Reset()
	errb.Reset()
	if code := statusCLI([]string{a}, &out, &errb); code == 0 {
		t.Fatal("status for a subset reported running")
	}
	if !strings.Contains(out.String(), "stopped") {
		t.Errorf("subset status output = %q, want stopped", out.String())
	}
}

// An unlabelled daemon shows "-" in the LABEL column: a derived display name
// would read as a label addressable with --workspace, which it is not.
func TestListCLIShowsDashForUnlabelled(t *testing.T) {
	stateRoot(t)
	root := t.TempDir()
	if err := Record([]string{root}, State{PID: os.Getpid(), TCP: "tcp://127.0.0.1:7391"}); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := CLI([]string{"list"}, &out, &errb); code != 0 {
		t.Fatalf("list = %d, stderr = %s", code, errb.String())
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) < 2 {
		t.Fatalf("list output has no daemon row: %q", out.String())
	}
	if fields := strings.Fields(lines[1]); len(fields) == 0 || fields[0] != "-" {
		t.Errorf("LABEL = %v, want - for an unlabelled daemon", fields)
	}
}

// list --json with no daemons is [], not null, so a script can index the result
// without special-casing an empty list.
func TestListCLIJSONEmptyIsArray(t *testing.T) {
	stateRoot(t)
	var out, errb bytes.Buffer
	if code := CLI([]string{"list", "--json"}, &out, &errb); code != 0 {
		t.Fatalf("list --json = %d, stderr = %s", code, errb.String())
	}
	if got := strings.TrimSpace(out.String()); got != "[]" {
		t.Errorf("list --json empty = %q, want []", got)
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
	r := Runner{Roots: []string{root}, Exe: "/bin/raj", Log: io.Discard,
		Args: controlForward("tcp://127.0.0.1:9", true), Ops: Ops{
			Alive: func(int) bool { return false },
			Spawn: spawnRecording(root, 7, func(cmd *exec.Cmd) { got = cmd }),
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
	if err := Record([]string{root}, State{PID: 4242, Socket: "/run/x.sock", TCP: "tcp://127.0.0.1:7391", Token: "tok"}); err != nil {
		t.Fatal(err)
	}
	live := true
	var events []string
	var sig syscall.Signal
	var got *exec.Cmd
	r := Runner{Roots: []string{root}, Exe: "/bin/raj", Log: io.Discard, Wait: 50 * time.Millisecond, Step: time.Millisecond, Ops: Ops{
		Alive: func(int) bool { return live },
		Signal: func(_ int, s syscall.Signal) error {
			sig = s
			live = false
			events = append(events, "stop")
			return nil
		},
		Spawn: spawnRecording(root, 777, func(cmd *exec.Cmd) { got = cmd; events = append(events, "spawn") }),
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
	if err := Record([]string{root}, State{PID: 4242, Socket: "/run/x.sock", Token: "recorded"}); err != nil {
		t.Fatal(err)
	}
	live := true
	var got *exec.Cmd
	r := Runner{Roots: []string{root}, Exe: "/bin/raj", Log: io.Discard, Ops: Ops{
		Alive:  func(int) bool { return live },
		Signal: func(int, syscall.Signal) error { live = false; return nil },
		Spawn:  spawnRecording(root, 1, func(cmd *exec.Cmd) { got = cmd }),
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
	if err := Record([]string{root}, State{PID: 4242, Socket: "/run/x.sock", TCP: "tcp://127.0.0.1:7391"}); err != nil {
		t.Fatal(err)
	}
	live := true
	var got *exec.Cmd
	r := Runner{Roots: []string{root}, Exe: "/bin/raj", Log: io.Discard, Ops: Ops{
		Alive:  func(int) bool { return live },
		Signal: func(int, syscall.Signal) error { live = false; return nil },
		Spawn:  spawnRecording(root, 1, func(cmd *exec.Cmd) { got = cmd }),
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
	r := Runner{Roots: []string{root}, Exe: "/bin/raj", Log: io.Discard, Ops: Ops{
		Alive:  func(int) bool { return false },
		Signal: func(int, syscall.Signal) error { signalled = true; return nil },
		Spawn:  spawnRecording(root, 42, func(cmd *exec.Cmd) { got = cmd }),
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

// spawnRecording fakes an Ops.Spawn that returns pid and writes the state
// record the real daemon would once its listener is bound, so Start sees the
// readiness signal. The observe callbacks run first, for tests that inspect the
// constructed command.
func spawnRecording(root string, pid int, observe ...func(*exec.Cmd)) func(*exec.Cmd) (int, error) {
	return func(cmd *exec.Cmd) (int, error) {
		for _, f := range observe {
			f(cmd)
		}
		if err := Record([]string{root}, State{PID: pid, TCP: "tcp://127.0.0.1:7391"}); err != nil {
			return 0, err
		}
		return pid, nil
	}
}

// spawnRecordingRoots is spawnRecording for a multi-root daemon: the record it
// writes is keyed by the whole set, so Start's readiness wait finds it.
func spawnRecordingRoots(roots []string, pid int, observe ...func(*exec.Cmd)) func(*exec.Cmd) (int, error) {
	return func(cmd *exec.Cmd) (int, error) {
		for _, f := range observe {
			f(cmd)
		}
		if err := Record(roots, State{PID: pid, TCP: "tcp://127.0.0.1:7391"}); err != nil {
			return 0, err
		}
		return pid, nil
	}
}

// spawnAppendingLog fakes an Ops.Spawn that appends text to the workspace log,
// the way the detached child writes its own failure there, and records no
// readiness state so Start fails its wait. It is how a test builds a log that
// holds a previous run's output plus this run's.
func spawnAppendingLog(root, text string, pid int) func(*exec.Cmd) (int, error) {
	paths := ForRoot(root)
	return func(*exec.Cmd) (int, error) {
		f, err := os.OpenFile(paths.Log, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return 0, err
		}
		if _, err := f.WriteString(text); err != nil {
			f.Close()
			return 0, err
		}
		if err := f.Close(); err != nil {
			return 0, err
		}
		return pid, nil
	}
}

// zombieState reads the process state out of a /proc/<pid>/stat line. The comm
// field is the trap: it is parenthesised and may contain spaces and parens, so
// the state is the byte after the LAST ')'. A line with no ')' is unknown, not
// a zombie.
func TestZombieStateParsing(t *testing.T) {
	cases := []struct {
		name string
		stat string
		want bool
		ok   bool
	}{
		{"running", "123 (raj) S 1 2 3\n", false, true},
		{"zombie", "123 (raj) Z 1 2 3\n", true, true},
		{"comm with spaces", "123 (raj daemon) Z 1 2 3\n", true, true},
		{"comm with parens", "123 (a)b)c) Z 1 2 3\n", true, true},
		{"comm paren then running", "123 (a)b c) R 1 2 3\n", false, true},
		{"malformed", "not a stat line", false, false},
		{"empty", "", false, false},
	}
	for _, tc := range cases {
		zombie, ok := zombieState([]byte(tc.stat))
		if zombie != tc.want || ok != tc.ok {
			t.Errorf("%s: zombieState(%q) = %v/%v, want %v/%v", tc.name, tc.stat, zombie, ok, tc.want, tc.ok)
		}
	}
}

// Start must not report a daemon that never ran: when the spawned child never
// writes a matching state record, Start fails, removes the pidfile it wrote,
// and the error names the failure. Before the readiness wait this returned nil,
// so the CLI printed "started pid N" for a process that was already gone.
func TestStartFailsWhenChildNeverRecords(t *testing.T) {
	root := stateRoot(t)
	paths := ForRoot(root)
	r := Runner{Roots: []string{root}, Exe: "/bin/raj", Log: io.Discard, Wait: 20 * time.Millisecond, Step: time.Millisecond, Ops: Ops{
		Alive: func(int) bool { return true },
		Spawn: func(*exec.Cmd) (int, error) { return 7001, nil },
	}}
	if _, err := r.Start(); err == nil {
		t.Fatal("Start = nil error, want a readiness failure")
	} else if !strings.HasPrefix(err.Error(), "daemon: ") {
		t.Errorf("Start error = %q, want it to start with daemon: ", err)
	}
	if _, err := os.Stat(paths.PID); !os.IsNotExist(err) {
		t.Error("failed Start left the pidfile")
	}
	if _, err := os.Stat(paths.State); !os.IsNotExist(err) {
		t.Error("failed Start left the state record")
	}
}

// Start accepts a child that records its bound listener: a matching daemon.json
// with an address is the readiness signal, and the pid comes back.
func TestStartSucceedsWhenChildRecords(t *testing.T) {
	root := stateRoot(t)
	r := Runner{Roots: []string{root}, Exe: "/bin/raj", Log: io.Discard, Ops: Ops{
		Alive: func(int) bool { return true },
		Spawn: spawnRecording(root, 7002),
	}}
	pid, err := r.Start()
	if err != nil {
		t.Fatal(err)
	}
	if pid != 7002 {
		t.Errorf("pid = %d, want 7002", pid)
	}
	s, ok := ReadState(ForRoot(root).State)
	if !ok || s.PID != 7002 || s.TCP == "" {
		t.Errorf("state = %+v/%v, want a recorded listener for 7002", s, ok)
	}
}

// A stale daemon.json from a previous run is not the new child's readiness:
// Start clears it before spawning, so a spawn that records nothing still fails
// even when the old record names the same pid, as a recycled pid would.
func TestStartStaleStateIsNotReadiness(t *testing.T) {
	root := stateRoot(t)
	paths := ForRoot(root)
	if err := WriteState(paths.State, State{PID: 7003, TCP: "tcp://127.0.0.1:7391"}); err != nil {
		t.Fatal(err)
	}
	r := Runner{Roots: []string{root}, Exe: "/bin/raj", Log: io.Discard, Wait: 20 * time.Millisecond, Step: time.Millisecond, Ops: Ops{
		Alive: func(int) bool { return false },
		Spawn: func(*exec.Cmd) (int, error) { return 7003, nil },
	}}
	if _, err := r.Start(); err == nil {
		t.Fatal("Start = nil error, want the stale record rejected as readiness")
	}
	if _, err := os.Stat(paths.State); !os.IsNotExist(err) {
		t.Error("failed Start left the stale state record")
	}
}

// A start failure carries the child's own output, the only clue why it never
// bound a listener, and the message stays bounded.
func TestStartFailureCarriesLogTail(t *testing.T) {
	root := stateRoot(t)
	r := Runner{Roots: []string{root}, Exe: "/bin/raj", Wait: 10 * time.Millisecond, Step: time.Millisecond, Ops: Ops{
		Alive: func(int) bool { return true },
		Spawn: spawnAppendingLog(root, strings.Repeat("noise\n", 1000)+"boom: address already in use\n", 7004),
	}}
	_, err := r.Start()
	if err == nil {
		t.Fatal("Start = nil error, want a readiness failure")
	}
	if !strings.Contains(err.Error(), "boom: address already in use") {
		t.Errorf("Start error does not carry the log tail: %q", err)
	}
	if len(err.Error()) > 4096 {
		t.Errorf("Start error is %d bytes, want it bounded", len(err.Error()))
	}
}

// A failed start reports only the current run's output. The log is append-only
// and never truncated, so a previous run's panic — here a send on a closed
// channel — must not be read back as if the new child had printed it.
func TestStartFailureLogTailExcludesPreviousRun(t *testing.T) {
	root := stateRoot(t)
	paths := ForRoot(root)
	if err := os.MkdirAll(filepath.Dir(paths.Log), 0o700); err != nil {
		t.Fatal(err)
	}
	// Short enough to sit inside logTail's 2 KB window, which is the case that
	// misled the user: the stale panic would otherwise be the whole tail.
	stale := "previous run: panic: send on closed channel\n"
	if err := os.WriteFile(paths.Log, []byte(stale), 0o600); err != nil {
		t.Fatal(err)
	}
	const fresh = "daemon: tcp://127.0.0.1:7391 is already in use\n"
	r := Runner{Roots: []string{root}, Exe: "/bin/raj", Wait: 10 * time.Millisecond, Step: time.Millisecond, Ops: Ops{
		Alive: func(int) bool { return true },
		Spawn: spawnAppendingLog(root, fresh, 7005),
	}}
	_, err := r.Start()
	if err == nil {
		t.Fatal("Start = nil error, want a readiness failure")
	}
	if !strings.Contains(err.Error(), "already in use") {
		t.Errorf("Start error does not carry this run's output: %q", err)
	}
	if strings.Contains(err.Error(), "send on closed channel") {
		t.Errorf("Start error carries a previous run's output: %q", err)
	}
}

// Stop accepts a record removed mid-shutdown as proof the daemon exited, even
// while a liveness probe still says it is alive: a zombie that has run
// daemon.Clear would otherwise make Stop spin to its deadline and report a
// timeout for a daemon that is already gone.
func TestStopSucceedsWhenRecordClearedDespiteLife(t *testing.T) {
	root := stateRoot(t)
	paths := ForRoot(root)
	if err := WritePID(paths.PID, 8001); err != nil {
		t.Fatal(err)
	}
	if err := WriteState(paths.State, State{PID: 8001, Socket: "/run/x.sock"}); err != nil {
		t.Fatal(err)
	}
	r := Runner{Roots: []string{root}, Wait: 50 * time.Millisecond, Step: time.Millisecond, Ops: Ops{
		Alive:  func(int) bool { return true },
		Signal: func(int, syscall.Signal) error { os.Remove(paths.PID); os.Remove(paths.State); return nil },
	}}
	stopped, err := r.Stop()
	if err != nil {
		t.Fatalf("Stop = %v, want the vanished record accepted", err)
	}
	if !stopped {
		t.Error("Stop = false, want it to report the signalled daemon")
	}
}

// Restart refuses to start a second daemon when the old one will not stop: the
// Stop error is returned and Spawn must not run, so a wedged daemon cannot be
// silently replaced.
func TestRestartStopFailureDoesNotStart(t *testing.T) {
	root := stateRoot(t)
	if err := Record([]string{root}, State{PID: 4242, Socket: "/run/x.sock", TCP: "tcp://127.0.0.1:7391"}); err != nil {
		t.Fatal(err)
	}
	spawned := false
	r := Runner{Roots: []string{root}, Exe: "/bin/raj", Log: io.Discard, Wait: 10 * time.Millisecond, Step: time.Millisecond, Ops: Ops{
		Alive:  func(int) bool { return true },
		Signal: func(int, syscall.Signal) error { return nil },
		Spawn:  func(*exec.Cmd) (int, error) { spawned = true; return 9001, nil },
	}}
	if _, _, err := r.Restart("", false); err == nil {
		t.Fatal("Restart = nil error, want the Stop timeout")
	}
	if spawned {
		t.Error("Restart started a new daemon despite the old one not stopping")
	}
}

// writeDaemon lays down the daemon.json and pidfile a running daemon leaves in
// <dir>/<key>, the shape ListDir reads. withPID writes the pidfile so a test
// can check stale cleanup removes both files.
func writeDaemon(t *testing.T, dir, key string, s State, withPID bool) string {
	t.Helper()
	keyDir := filepath.Join(dir, key)
	if err := os.MkdirAll(keyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if withPID {
		if err := WritePID(filepath.Join(keyDir, "daemon.pid"), s.PID); err != nil {
			t.Fatal(err)
		}
	}
	if err := WriteState(filepath.Join(keyDir, "daemon.json"), s); err != nil {
		t.Fatal(err)
	}
	return keyDir
}

// ListDir reports a live daemon with the label, root, pid and address its record
// carries. Without this a user has no way to see what is running.
func TestListDirReturnsLiveEntry(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(t.TempDir(), "myrepo")
	writeDaemon(t, dir, "raj-myrepo-abcd",
		State{PID: 4242, Workspace: "alpha", Roots: []string{root}, TCP: "tcp://127.0.0.1:7391"}, true)

	entries, err := listDir(dir, Ops{Alive: func(int) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %+v, want one", entries)
	}
	e := entries[0]
	if e.displayLabel() != "alpha" {
		t.Errorf("label = %q, want alpha", e.displayLabel())
	}
	if len(e.Roots) != 1 || e.Roots[0] != root {
		t.Errorf("roots = %v, want [%s]", e.Roots, root)
	}
	if e.PID != 4242 {
		t.Errorf("pid = %d, want 4242", e.PID)
	}
	if e.Address() != "tcp://127.0.0.1:7391" {
		t.Errorf("address = %q, want the TCP address", e.Address())
	}
}

// A record whose pid is dead is not a running daemon: it is skipped and its
// files removed, so a crashed daemon does not linger in `daemon list`.
func TestListDirSkipsDeadAndRemovesFiles(t *testing.T) {
	dir := t.TempDir()
	keyDir := writeDaemon(t, dir, "raj-gone-abcd", State{PID: 999999, Roots: []string{"/gone"}}, true)

	entries, err := listDir(dir, Ops{Alive: func(int) bool { return false }})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("entries = %+v, want none", entries)
	}
	for _, name := range []string{"daemon.json", "daemon.pid"} {
		if _, err := os.Stat(filepath.Join(keyDir, name)); !os.IsNotExist(err) {
			t.Errorf("stale %s was not removed", name)
		}
	}
}

// A workspaces directory that does not exist is an empty list, not an error: on
// a machine where no daemon has ever run there is nothing wrong.
func TestListDirMissingDirIsEmpty(t *testing.T) {
	entries, err := ListDir(filepath.Join(t.TempDir(), "nope"))
	if err != nil {
		t.Fatalf("ListDir = %v, want empty", err)
	}
	if len(entries) != 0 {
		t.Errorf("entries = %+v, want none", entries)
	}
	if entries, err := ListDir(""); err != nil || len(entries) != 0 {
		t.Errorf("ListDir(\"\") = %v/%v, want empty/nil", entries, err)
	}
}

// ListDir sorts by display label, then by root, so the table is stable.
func TestListDirSortsByLabelThenRoot(t *testing.T) {
	dir := t.TempDir()
	writeDaemon(t, dir, "k1", State{PID: 1, Workspace: "beta", Roots: []string{"/b"}}, false)
	writeDaemon(t, dir, "k2", State{PID: 2, Workspace: "alpha", Roots: []string{"/z"}}, false)
	writeDaemon(t, dir, "k3", State{PID: 3, Workspace: "alpha", Roots: []string{"/a"}}, false)

	entries, err := listDir(dir, Ops{Alive: func(int) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.displayLabel()+"@"+primaryRoot(e))
	}
	want := []string{"alpha@/a", "alpha@/z", "beta@/b"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("order = %v, want %v", got, want)
	}
}

// A label set after Start round-trips through the record and resolves through
// FindWorkspace, while the pid, addresses and token the daemon published are
// preserved. The recorded pid is this test process, so the real liveness check
// finds a running daemon without a spawn.
func TestWorkspaceLabelRoundTripsThroughSetWorkspace(t *testing.T) {
	root := stateRoot(t)
	if err := Record([]string{root}, State{PID: os.Getpid(), Socket: "/run/x.sock", TCP: "tcp://127.0.0.1:7391", Token: "tok"}); err != nil {
		t.Fatal(err)
	}
	if err := SetWorkspace([]string{root}, "alpha"); err != nil {
		t.Fatal(err)
	}
	s, ok := ReadState(ForRoot(root).State)
	if !ok {
		t.Fatal("no record after SetWorkspace")
	}
	if s.Workspace != "alpha" {
		t.Errorf("workspace = %q, want alpha", s.Workspace)
	}
	if s.PID != os.Getpid() || s.Socket != "/run/x.sock" || s.TCP != "tcp://127.0.0.1:7391" || s.Token != "tok" {
		t.Errorf("SetWorkspace lost record fields: %+v", s)
	}
	if len(s.Roots) != 1 || s.Roots[0] != root {
		t.Errorf("roots = %v, want [%s]", s.Roots, root)
	}
	e, found, err := FindWorkspace("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if !found || e.PID != os.Getpid() || primaryRoot(e) != root {
		t.Errorf("FindWorkspace = %+v/%v, want the labelled daemon", e, found)
	}
	if _, found, err := FindWorkspace("beta"); err != nil || found {
		t.Errorf("FindWorkspace(beta) = %v/%v, want not found/nil", found, err)
	}
}

// start refuses a label a different running daemon already carries, so a label
// always resolves to one daemon. The label's own root is allowed: that daemon is
// the one being restarted, or is caught by the already-running check.
func TestClaimWorkspaceRefusesRunningLabel(t *testing.T) {
	a := stateRoot(t)
	b := t.TempDir()
	if err := Record([]string{a}, State{PID: os.Getpid(), TCP: "tcp://127.0.0.1:1"}); err != nil {
		t.Fatal(err)
	}
	if err := SetWorkspace([]string{a}, "alpha"); err != nil {
		t.Fatal(err)
	}
	if err := claimWorkspace("alpha", []string{b}); err == nil {
		t.Error("claimWorkspace accepted a label used by another running daemon")
	}
	if err := claimWorkspace("alpha", []string{a}); err != nil {
		t.Errorf("claimWorkspace refused the label's own root: %v", err)
	}
	if err := claimWorkspace("beta", []string{b}); err != nil {
		t.Errorf("claimWorkspace refused an unused label: %v", err)
	}
	if err := claimWorkspace("", []string{b}); err != nil {
		t.Errorf("claimWorkspace refused an empty label: %v", err)
	}
}

// stop --workspace resolves through the daemon label to the root it recorded,
// without spawning or signalling anything. An unknown label and a label mixed
// with a directory are both refused rather than guessed at.
func TestStopWorkspaceResolvesThroughLabel(t *testing.T) {
	rootA := stateRoot(t)
	if err := Record([]string{rootA}, State{PID: os.Getpid(), TCP: "tcp://127.0.0.1:7391"}); err != nil {
		t.Fatal(err)
	}
	if err := SetWorkspace([]string{rootA}, "alpha"); err != nil {
		t.Fatal(err)
	}
	got, err := resolveTargets(nil, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != rootA {
		t.Errorf("resolveTargets = %v, want the labelled root %q", got, rootA)
	}
	if _, err := resolveTargets(nil, "missing"); err == nil {
		t.Error("resolveTargets accepted an unknown label")
	}
	if _, err := resolveTargets([]string{rootA}, "alpha"); err == nil {
		t.Error("resolveTargets accepted a label with a directory")
	}
}

// list takes no directory: a positional argument is a usage error, so a script
// cannot think it listed one workspace when it listed all of them.
func TestListCLIRejectsDirectory(t *testing.T) {
	var out, errb bytes.Buffer
	if code := CLI([]string{"list", "/ws"}, &out, &errb); code != 2 {
		t.Fatalf("list with a dir = %d, want 2", code)
	}
	if code := CLI([]string{"ps", "/ws"}, &out, &errb); code != 2 {
		t.Fatalf("ps with a dir = %d, want 2", code)
	}
}

// list prints a running daemon's label, root, pid, address and running status,
// and --json carries the same facts for a script.
func TestListCLIRendersRunningDaemon(t *testing.T) {
	root := stateRoot(t)
	if err := Record([]string{root}, State{PID: os.Getpid(), TCP: "tcp://127.0.0.1:7391"}); err != nil {
		t.Fatal(err)
	}
	if err := SetWorkspace([]string{root}, "alpha"); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := CLI([]string{"list"}, &out, &errb); code != 0 {
		t.Fatalf("list = %d, stderr = %s", code, errb.String())
	}
	for _, want := range []string{"alpha", root, "running", "tcp://127.0.0.1:7391"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("list output %q does not mention %q", out.String(), want)
		}
	}
	var jout, jerrb bytes.Buffer
	if code := CLI([]string{"list", "--json"}, &jout, &jerrb); code != 0 {
		t.Fatalf("list --json = %d, stderr = %s", code, jerrb.String())
	}
	for _, want := range []string{"\"workspace\": \"alpha\"", "\"pid\": ", "\"tcp\": \"tcp://127.0.0.1:7391\""} {
		if !strings.Contains(jout.String(), want) {
			t.Errorf("list --json %q does not mention %q", jout.String(), want)
		}
	}
}

// list with no daemons running says so rather than printing an empty table.
func TestListCLIEmpty(t *testing.T) {
	stateRoot(t)
	var out, errb bytes.Buffer
	if code := CLI([]string{"list"}, &out, &errb); code != 0 {
		t.Fatalf("list = %d, stderr = %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "none running") {
		t.Errorf("list output = %q, want none running", out.String())
	}
}

// A label two running daemons display is ambiguous: FindWorkspace errors rather
// than returning one of them, so stop and restart cannot act on the wrong
// daemon. start refuses to create such a pair, so this is a hand-edited record.
func TestFindWorkspaceRefusesAmbiguousLabel(t *testing.T) {
	a := stateRoot(t)
	b := t.TempDir()
	for _, root := range []string{a, b} {
		if err := Record([]string{root}, State{PID: os.Getpid(), TCP: "tcp://127.0.0.1:1"}); err != nil {
			t.Fatal(err)
		}
		if err := SetWorkspace([]string{root}, "dup"); err != nil {
			t.Fatal(err)
		}
	}
	if _, found, err := FindWorkspace("dup"); err == nil || found {
		t.Errorf("FindWorkspace(dup) = %v/%v, want an ambiguity error", found, err)
	}
}

// An unlabelled daemon is displayed by a derived name but is not addressable by
// --workspace: only a label the user set resolves, so a derived name can never
// be mistaken for one the user chose.
func TestFindWorkspaceIgnoresUnlabelledDaemon(t *testing.T) {
	root := stateRoot(t)
	if err := Record([]string{root}, State{PID: os.Getpid(), Roots: []string{root}}); err != nil {
		t.Fatal(err)
	}
	if e, found, err := FindWorkspace(filepath.Base(root)); err != nil {
		t.Fatal(err)
	} else if found {
		t.Errorf("FindWorkspace(%q) = %+v, want not found for an unlabelled daemon", filepath.Base(root), e)
	}
}
