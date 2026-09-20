// Package daemon manages a raj daemon: the headless host of `daemon run`,
// started detached by `daemon start`, with a pidfile and a small state record
// so `daemon stop` and `daemon status` find it and a client can read its
// token.
//
// The OS operations sit behind Ops so the decisions that matter — refuse a
// live daemon, treat a stale pidfile as stopped, spawn detached, wait bounded
// on stop — are testable without starting or signalling a real process.
package daemon

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"raj/internal/control"
	"raj/internal/session"
)

// State is the per-workspace daemon record. It is written 0600 because Token
// is a secret: anything that can read it can drive a TCP control listener.
type State struct {
	PID    int    `json:"pid"`
	Socket string `json:"socket,omitempty"`
	TCP    string `json:"tcp,omitempty"`
	Token  string `json:"token,omitempty"`
}

// Paths are the daemon files for one workspace state dir. Every field is empty
// when the workspace has no state dir, which a caller treats as "no daemon".
type Paths struct {
	PID   string
	State string
	Log   string
}

// ForRoot derives the daemon files for a workspace root from the same state
// directory the session store uses, so a workspace owns exactly one daemon.
func ForRoot(root string) Paths {
	dir := session.StateDir(root)
	if dir == "" {
		return Paths{}
	}
	return Paths{
		PID:   filepath.Join(dir, "daemon.pid"),
		State: filepath.Join(dir, "daemon.json"),
		Log:   filepath.Join(dir, "daemon.log"),
	}
}

// removeAll deletes the record files, best-effort: a record already known
// stale is cleanup, not an operation worth failing a command over.
func (p Paths) removeAll() {
	for _, f := range []string{p.PID, p.State} {
		if f != "" {
			os.Remove(f)
		}
	}
}

// WritePID records pid in path, 0600. It is separate from the JSON state so a
// reader that only needs liveness (stop) does not decode anything.
func WritePID(path string, pid int) error {
	return writeFile(path, []byte(strconv.Itoa(pid)+"\n"))
}

// ReadPID reads a pidfile. ok is false for a missing or malformed file, both
// of which mean "not running".
func ReadPID(path string) (pid int, ok bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// WriteState records the daemon state, 0600. The running daemon writes it once
// its control listener is up, when it knows the real socket, token and port.
func WriteState(path string, s State) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return writeFile(path, append(b, '\n'))
}

// ReadState reads the daemon state. ok is false when there is none or it does
// not decode, which status reports as a running daemon with unknown details
// rather than inventing fields.
func ReadState(path string) (State, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return State{}, false
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return State{}, false
	}
	return s, true
}

func writeFile(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Alive reports whether pid names a live process. Signal 0 is the liveness
// probe: it runs the permission check without delivering anything, and a
// process owned by another user answers EPERM — it exists.
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// Detached builds the command that runs a daemon in the background: the
// re-exec argv, a new session so a terminal signal does not reach it, /dev/null
// as stdin, and the workspace log for both output streams. It opens nothing
// and starts nothing, so a test can inspect the construction directly.
func Detached(exe string, args []string, log io.Writer) *exec.Cmd {
	cmd := exec.Command(exe, args...)
	cmd.Stdin = nil // the null device, per os/exec
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd
}

// Ops is the OS seam. A nil field falls back to the real operation, so
// production passes Ops{} and a test passes fakes.
type Ops struct {
	Alive  func(pid int) bool
	Signal func(pid int, sig syscall.Signal) error
	Spawn  func(cmd *exec.Cmd) (int, error)
}

func (o Ops) life() func(int) bool {
	if o.Alive != nil {
		return o.Alive
	}
	return Alive
}

func (o Ops) signal() func(int, syscall.Signal) error {
	if o.Signal != nil {
		return o.Signal
	}
	return func(pid int, sig syscall.Signal) error {
		p, err := os.FindProcess(pid)
		if err != nil {
			return err
		}
		return p.Signal(sig)
	}
}

func (o Ops) spawn() func(*exec.Cmd) (int, error) {
	if o.Spawn != nil {
		return o.Spawn
	}
	return func(cmd *exec.Cmd) (int, error) {
		if err := cmd.Start(); err != nil {
			return 0, err
		}
		return cmd.Process.Pid, nil
	}
}

// Runner starts, stops and reports the daemon for one workspace root. A test
// varies Root (a temp dir), Ops (fakes) and Wait/Step (a short stop bound);
// production leaves everything but Root zero.
type Runner struct {
	Root string
	// Exe is the binary to re-exec; empty is os.Executable.
	Exe string
	// Args are the extra arguments between "daemon run" and the root: the
	// control flags start forwards.
	Args []string
	// Log receives the daemon stdout and stderr; nil opens Paths.Log.
	Log io.Writer
	// Ops is the OS seam.
	Ops Ops
	// Wait bounds how long Stop waits for the process to exit; Step is the
	// poll interval. Zero is the production default.
	Wait time.Duration
	Step time.Duration
}

const (
	defaultWait = 5 * time.Second
	defaultStep = 20 * time.Millisecond
)

// Pid returns the recorded pid, without judging liveness. ok is false when no
// pidfile exists.
func (r Runner) Pid() (pid int, ok bool) { return ReadPID(ForRoot(r.Root).PID) }

// Start spawns the daemon unless one is already live, writes the pidfile and
// returns the new pid. A stale record from a crashed daemon is removed first,
// so it never blocks a restart.
func (r Runner) Start() (int, error) {
	paths := ForRoot(r.Root)
	if paths.PID == "" {
		return 0, errors.New("daemon: no state directory for this workspace")
	}
	if err := os.MkdirAll(filepath.Dir(paths.PID), 0o700); err != nil {
		return 0, err
	}
	if pid, ok := ReadPID(paths.PID); ok {
		if r.Ops.life()(pid) {
			return 0, fmt.Errorf("daemon: already running (pid %d)", pid)
		}
		paths.removeAll()
	}
	exe := r.Exe
	if exe == "" {
		var err error
		if exe, err = os.Executable(); err != nil {
			return 0, err
		}
	}
	log, err := r.logWriter(paths)
	if err != nil {
		return 0, err
	}
	argv := append([]string{"daemon", "run"}, r.Args...)
	argv = append(argv, r.Root)
	cmd := Detached(exe, argv, log)
	pid, err := r.Ops.spawn()(cmd)
	if err != nil {
		return 0, err
	}
	if err := WritePID(paths.PID, pid); err != nil {
		return 0, err
	}
	return pid, nil
}

// logWriter picks the daemon's output sink: a test-supplied writer, else the
// workspace log opened append-only. The file is deliberately never closed: the
// spawned daemon inherits the descriptor, and this process exits immediately
// after Start.
func (r Runner) logWriter(paths Paths) (io.Writer, error) {
	if r.Log != nil {
		return r.Log, nil
	}
	if paths.Log == "" {
		return nil, errors.New("daemon: no log path")
	}
	f, err := os.OpenFile(paths.Log, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return f, nil
}

// Stop signals the recorded daemon and waits for it to exit. It reports
// whether one was running: a missing or stale record is a clean no-op, and the
// stale files are removed on the way.
func (r Runner) Stop() (bool, error) {
	paths := ForRoot(r.Root)
	if paths.PID == "" {
		return false, nil
	}
	pid, ok := ReadPID(paths.PID)
	if !ok {
		paths.removeAll()
		return false, nil
	}
	life := r.Ops.life()
	if !life(pid) {
		paths.removeAll()
		return false, nil
	}
	if err := r.Ops.signal()(pid, syscall.SIGTERM); err != nil {
		return false, err
	}
	wait, step := r.Wait, r.Step
	if wait <= 0 {
		wait = defaultWait
	}
	if step <= 0 {
		step = defaultStep
	}
	deadline := time.Now().Add(wait)
	for life(pid) {
		if !time.Now().Before(deadline) {
			return true, fmt.Errorf("daemon: pid %d did not exit within %s", pid, wait)
		}
		time.Sleep(step)
	}
	paths.removeAll()
	return true, nil
}

// Status reports the daemon for the workspace. A stale record is cleaned up
// and reported as stopped, so status is also what unblocks a restart after a
// crash.
func (r Runner) Status() (State, bool) {
	paths := ForRoot(r.Root)
	if paths.PID == "" {
		return State{}, false
	}
	pid, ok := ReadPID(paths.PID)
	if !ok || !r.Ops.life()(pid) {
		paths.removeAll()
		return State{}, false
	}
	s, _ := ReadState(paths.State)
	s.PID = pid
	return s, true
}

// Record writes the running daemon's own files: the pidfile and the state
// record naming its control address and token. Start wrote the pidfile early;
// Record completes the pair once the listener is bound.
func Record(root string, s State) error {
	paths := ForRoot(root)
	if paths.PID == "" {
		return errors.New("daemon: no state directory for this workspace")
	}
	if err := WritePID(paths.PID, s.PID); err != nil {
		return err
	}
	return WriteState(paths.State, s)
}

// Clear removes the daemon files on the way out. Nil-safe for a workspace with
// no state dir.
func Clear(root string) { ForRoot(root).removeAll() }

// ResolveRoot is the workspace a daemon command acts on: the repository
// containing arg, or arg itself, the same walk-up the editor uses. An empty
// arg uses the working directory; a file arg uses its directory.
func ResolveRoot(arg string) (string, error) {
	if arg == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		return control.WorkspaceRoot(cwd), nil
	}
	abs, err := filepath.Abs(arg)
	if err != nil {
		return "", err
	}
	if info, err := os.Stat(abs); err == nil && !info.IsDir() {
		abs = filepath.Dir(abs)
	}
	return control.WorkspaceRoot(abs), nil
}

const usage = `usage: raj daemon <command> [dir] [options]

  start [dir] [--control-addr ADDR] [--control-socket PATH] [--control-exec]
                             run the headless host in the background; refused
                             while one already runs for the workspace
  stop [dir]                 stop the background daemon; a no-op when none runs
  status [dir]               report whether the daemon runs, and its socket,
                             TCP address and token

dir is the workspace (default: the current directory), resolved to the
repository the editor would choose. The pidfile, daemon.json and daemon.log
live in the workspace state directory, so one workspace has one daemon.
`

// CLI runs one daemon subcommand and returns a process exit code.
//
// `daemon run` is the foreground worker itself, so main strips it before
// flag.Parse and the whole editor flag set still applies; it never reaches
// here.
func CLI(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "start":
		return startCLI(args[1:], stdout, stderr)
	case "stop":
		return stopCLI(args[1:], stdout, stderr)
	case "status":
		return statusCLI(args[1:], stdout, stderr)
	case "run":
		fmt.Fprintln(stderr, "raj daemon run: internal; use `raj daemon start` or `raj --daemon`")
		return 2
	default:
		fmt.Fprintf(stderr, "raj daemon: unknown command %q\n\n%s", args[0], usage)
		return 2
	}
}

func startCLI(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("raj daemon start", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: raj daemon start [dir] [--control-addr ADDR] [--control-socket PATH] [--control-exec]")
		fs.PrintDefaults()
	}
	addr := fs.String("control-addr", "", "also listen on tcp://host:port")
	sock := fs.String("control-socket", "", "path for the control socket")
	remote := fs.Bool("control-exec", false, "with --control-addr: let a remote driver run commands here")
	if err := fs.Parse(flagsFirst(args)); err != nil {
		return 2
	}
	root, err := ResolveRoot(fs.Arg(0))
	if err != nil {
		fmt.Fprintln(stderr, "raj daemon start:", err)
		return 1
	}
	var forward []string
	if *addr != "" {
		forward = append(forward, "--control-addr", *addr)
	}
	if *sock != "" {
		forward = append(forward, "--control-socket", *sock)
	}
	if *remote {
		forward = append(forward, "--control-exec")
	}
	pid, err := Runner{Root: root, Args: forward}.Start()
	if err != nil {
		fmt.Fprintln(stderr, "raj daemon start:", err)
		return 1
	}
	fmt.Fprintf(stdout, "raj daemon: started pid %d\n", pid)
	if log := ForRoot(root).Log; log != "" {
		fmt.Fprintf(stdout, "raj daemon: log %s\n", log)
	}
	return 0
}

func stopCLI(args []string, stdout, stderr io.Writer) int {
	root, err := commandRoot("stop", args, stderr)
	if err != nil {
		return 2
	}
	stopped, err := (Runner{Root: root}).Stop()
	if err != nil {
		fmt.Fprintln(stderr, "raj daemon stop:", err)
		return 1
	}
	if stopped {
		fmt.Fprintln(stdout, "raj daemon: stopped")
	} else {
		fmt.Fprintln(stdout, "raj daemon: not running")
	}
	return 0
}

func statusCLI(args []string, stdout, stderr io.Writer) int {
	root, err := commandRoot("status", args, stderr)
	if err != nil {
		return 2
	}
	s, running := (Runner{Root: root}).Status()
	if !running {
		fmt.Fprintln(stdout, "raj daemon: stopped")
		return 1
	}
	fmt.Fprintf(stdout, "raj daemon: running (pid %d)\n", s.PID)
	if s.Socket != "" {
		fmt.Fprintf(stdout, "  socket %s\n", s.Socket)
	}
	if s.TCP != "" {
		fmt.Fprintf(stdout, "  tcp %s\n", s.TCP)
	}
	if s.Token != "" {
		fmt.Fprintf(stdout, "  token %s\n", s.Token)
	}
	return 0
}

// flagsFirst moves the one bare [dir] argument to the end, so flag.Parse sees
// the flags first and the directory as its positional whichever order the user
// typed them in: `raj daemon start --control-addr X dir` and `... dir
// --control-addr X` both work. Value-taking flags keep the value beside them.
func flagsFirst(args []string) []string {
	takesValue := map[string]bool{"control-addr": true, "control-socket": true}
	var flags, dirs []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			flags = append(flags, a)
			name := strings.TrimLeft(strings.SplitN(a, "=", 2)[0], "-")
			if takesValue[name] && !strings.Contains(a, "=") && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
			continue
		}
		dirs = append(dirs, a)
	}
	return append(flags, dirs...)
}

// commandRoot parses the lone optional dir argument the stop and status
// subcommands take.
func commandRoot(name string, args []string, stderr io.Writer) (string, error) {
	fs := flag.NewFlagSet("raj daemon "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "usage: raj daemon %s [dir]\n", name)
	}
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	root, err := ResolveRoot(fs.Arg(0))
	if err != nil {
		fmt.Fprintln(stderr, "raj daemon "+name+":", err)
		return "", err
	}
	return root, nil
}
