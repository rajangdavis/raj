// Package daemon manages a raj daemon: the headless host of `daemon run`,
// started detached by `daemon start`, with a pidfile and a small state record
// so `daemon stop` and `daemon status` find it and a client can read its
// token.
//
// The OS operations sit behind Ops so the decisions that matter — refuse a
// live daemon, treat a stale pidfile as stopped, spawn detached, wait bounded
// on start readiness and on stop — are testable without starting or signalling
// a real process.
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
	"sort"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"raj/internal/control"
	"raj/internal/session"
	"raj/internal/workspace"
)

// State is the per-workspace daemon record. It is written 0600 because Token
// is a secret: anything that can read it can drive a TCP control listener.
type State struct {
	PID    int    `json:"pid"`
	Socket string `json:"socket,omitempty"`
	TCP    string `json:"tcp,omitempty"`
	Token  string `json:"token,omitempty"`

	// Workspace is the label a user gave this daemon with --workspace, used
	// to find it by name. Empty means unlabelled, and the LABEL column shows "-".
	Workspace string `json:"workspace,omitempty"`
	// Roots is the workspace roots the daemon serves, stored so `daemon list`
	// can name the workspace without guessing from the state-dir key.
	Roots []string `json:"roots,omitempty"`
}

// Paths are the daemon files for one workspace state dir. Every field is empty
// when the workspace has no state dir, which a caller treats as "no daemon".
type Paths struct {
	PID   string
	State string
	Log   string
}

// ForRoots derives the daemon files for a workspace root set from the same
// state directory the session store uses, so a workspace set owns exactly one
// daemon. The key covers every root, so two daemons with different sets never
// share a record even when one set contains the other's first root.
func ForRoots(roots []string) Paths {
	dir := session.StateDirForRoots(roots)
	if dir == "" {
		return Paths{}
	}
	return Paths{
		PID:   filepath.Join(dir, "daemon.pid"),
		State: filepath.Join(dir, "daemon.json"),
		Log:   filepath.Join(dir, "daemon.log"),
	}
}

// ForRoot is ForRoots for the common single-root daemon.
func ForRoot(root string) Paths { return ForRoots([]string{root}) }

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

// zombieState reports whether Linux /proc/<pid>/stat contents name a zombie,
// and whether the contents could be parsed at all. The comm field is
// parenthesised and may itself contain spaces and parentheses, so the process
// state is the byte after the last ')' and the single space that follows it.
func zombieState(stat []byte) (zombie, known bool) {
	s := string(stat)
	i := strings.LastIndexByte(s, ')')
	if i < 0 || i+2 >= len(s) {
		return false, false
	}
	return s[i+2] == 'Z', true
}

// Alive reports whether pid names a live process. Signal 0 is the liveness
// probe: it runs the permission check without delivering anything, and a
// process owned by another user answers EPERM — it exists. A zombie is the
// exception: it has exited but is not reaped, and signal 0 still succeeds for
// it, so the platform probe must answer first.
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if zombie, known := processZombie(pid); known && zombie {
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

// Runner starts, stops and reports the daemon for a workspace root set. A test
// varies Roots (temp dirs), Ops (fakes) and Wait/Step (a short stop bound);
// production leaves everything but Roots zero.
type Runner struct {
	// Roots is the workspace root set the daemon serves, in caller-supplied
	// order. The daemon's state directory and every record are keyed by the
	// whole set, so a different set is a different daemon.
	Roots []string
	// Exe is the binary to re-exec; empty is os.Executable.
	Exe string
	// Args are the extra arguments between "daemon run" and the roots: the
	// control flags start forwards.
	Args []string
	// Env is extra environment for the spawned daemon, layered over the process
	// environment. A key the process already sets is replaced, not duplicated:
	// Go passes the slice through and two RAJ_CONTROL_TOKEN entries would leave
	// which one wins unspecified.
	Env []string
	// Log receives the daemon stdout and stderr; nil opens Paths.Log.
	Log io.Writer
	// Ops is the OS seam.
	Ops Ops
	// Wait bounds how long Start waits for readiness and Stop waits for the
	// process to exit; Step is the poll interval. Zero is the production
	// default.
	Wait time.Duration
	Step time.Duration
}

const (
	defaultWait = 5 * time.Second
	defaultStep = 20 * time.Millisecond
)

// withEnv layers extra over base, replacing a key base already carries rather
// than appending a second copy. os/exec passes a duplicate RAJ_CONTROL_TOKEN
// straight to the OS, and which one wins is then unspecified, so replacing is
// the only reliable way to fix a spawned child's token.
func withEnv(base, extra []string) []string {
	out := append([]string(nil), base...)
	for _, kv := range extra {
		key, _, _ := strings.Cut(kv, "=")
		replaced := false
		for i, have := range out {
			if k, _, _ := strings.Cut(have, "="); k == key {
				out[i] = kv
				replaced = true
				break
			}
		}
		if !replaced {
			out = append(out, kv)
		}
	}
	return out
}

// Pid returns the recorded pid, without judging liveness. ok is false when no
// pidfile exists.
func (r Runner) Pid() (pid int, ok bool) { return ReadPID(ForRoots(r.Roots).PID) }

// Start spawns the daemon unless one is already live, writes the pidfile and
// returns the new pid once the child has published its state record. A stale
// record from a crashed daemon is removed first, so it never blocks a restart.
//
// The state record is the readiness signal: the child writes it only after its
// control listener is bound, so a start that would die on a port already in use
// fails here instead of reporting a daemon that never ran.
func (r Runner) Start() (int, error) {
	paths := ForRoots(r.Roots)
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
	}
	// Clear both files before spawning: a daemon.json left by a previous run
	// must never be mistaken for the new child's readiness. The pidfile is
	// rewritten after the spawn.
	paths.removeAll()
	exe := r.Exe
	if exe == "" {
		var err error
		if exe, err = os.Executable(); err != nil {
			return 0, err
		}
	}
	log, logStart, err := r.logWriter(paths)
	if err != nil {
		return 0, err
	}
	argv := append([]string{"daemon", "run"}, r.Args...)
	argv = append(argv, r.Roots...)
	cmd := Detached(exe, argv, log)
	if len(r.Env) > 0 {
		cmd.Env = withEnv(os.Environ(), r.Env)
	}
	pid, err := r.Ops.spawn()(cmd)
	if err != nil {
		return 0, err
	}
	if err := WritePID(paths.PID, pid); err != nil {
		paths.removeAll()
		return 0, err
	}
	if err := r.waitReady(paths, pid, logStart); err != nil {
		paths.removeAll()
		return 0, err
	}
	return pid, nil
}

// waitReady waits, bounded by Wait and Step, for the spawned daemon to publish
// the state record that says its control listener is up. A pidfile alone only
// proves the process started, so a child that died before binding — or one that
// removed its own record on the way out — must surface as a start failure, with
// the log tail as the only clue why.
func (r Runner) waitReady(paths Paths, pid int, logStart int64) error {
	life := r.Ops.life()
	wait, step := r.Wait, r.Step
	if wait <= 0 {
		wait = defaultWait
	}
	if step <= 0 {
		step = defaultStep
	}
	deadline := time.Now().Add(wait)
	for {
		if s, ok := ReadState(paths.State); ok && s.PID == pid && (s.TCP != "" || s.Socket != "") {
			return nil
		}
		if !life(pid) || !recordPresent(paths) {
			return fmt.Errorf("daemon: pid %d exited before its control listener was ready%s", pid, r.logTail(paths, logStart))
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("daemon: pid %d did not open its control listener within %s%s", pid, wait, r.logTail(paths, logStart))
		}
		time.Sleep(step)
	}
}

// logTail is the part of the daemon log this run wrote, attached to a failed
// start so the child's own output ("address already in use") reaches the user.
// logStart is the log's size before the child was spawned: the log is
// append-only and never truncated, so reading from there keeps a previous run's
// panic from being reported as this one's. It is empty when the caller supplied
// its own Log, which has no file path to read, and truncated to keep the error
// message bounded.
func (r Runner) logTail(paths Paths, logStart int64) string {
	if r.Log != nil || paths.Log == "" {
		return ""
	}
	f, err := os.Open(paths.Log)
	if err != nil {
		return ""
	}
	defer f.Close()
	if _, err := f.Seek(logStart, io.SeekStart); err != nil {
		return ""
	}
	b, err := io.ReadAll(f)
	if err != nil || len(b) == 0 {
		return ""
	}
	const max = 2048
	if len(b) > max {
		b = b[len(b)-max:]
		if i := strings.IndexByte(string(b), '\n'); i >= 0 {
			b = b[i+1:]
		}
	}
	return "\n" + strings.TrimRight(string(b), "\n")
}

// Restart stops the running daemon and starts it again, carrying the running
// daemon's TCP address and token across unless the caller overrides the
// address. A workspace with no daemon is the no-op Stop already is, so this
// then behaves like a first start.
//
// The socket path is not carried: it is per-process, so the new daemon binds
// its own DefaultPath (or the SocketEnv override it inherits). An empty addr
// keeps the recorded address.
func (r Runner) Restart(addr string, noRestore bool) (int, State, error) {
	old, running := r.Status()
	if addr == "" {
		addr = old.TCP
	}
	token := ""
	var env []string
	if running && old.Token != "" {
		token = old.Token
		env = []string{control.TokenEnv + "=" + old.Token}
	}
	if _, err := r.Stop(); err != nil {
		return 0, State{}, err
	}
	start := r
	start.Args = controlForward(addr, noRestore)
	start.Env = env
	pid, err := start.Start()
	if err != nil {
		return 0, State{}, err
	}
	return pid, State{PID: pid, TCP: addr, Token: token}, nil
}

// logWriter picks the daemon's output sink: a test-supplied writer, else the
// workspace log opened append-only. The second result is the log's size at
// open, the offset this run's output starts at; logTail reads only from there
// so a previous run's output is never reported as the new child's. The file is
// deliberately never closed: the spawned daemon inherits the descriptor, and
// this process exits immediately after Start.
func (r Runner) logWriter(paths Paths) (io.Writer, int64, error) {
	if r.Log != nil {
		return r.Log, 0, nil
	}
	if paths.Log == "" {
		return nil, 0, errors.New("daemon: no log path")
	}
	f, err := os.OpenFile(paths.Log, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, 0, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, err
	}
	return f, fi.Size(), nil
}

// Stop signals the recorded daemon and waits for it to exit. It reports
// whether one was running: a missing or stale record is a clean no-op, and the
// stale files are removed on the way.
//
// The daemon's own graceful shutdown removes the record (daemon.Clear), so a
// vanished record means it has exited even while the liveness probe still says
// otherwise: an exited-but-unreaped child is a zombie, and a zombie still
func (r Runner) Stop() (bool, error) {
	paths := ForRoots(r.Roots)
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
	for life(pid) && recordPresent(paths) {
		if !time.Now().Before(deadline) {
			return true, fmt.Errorf("daemon: pid %d did not exit within %s", pid, wait)
		}
		time.Sleep(step)
	}
	paths.removeAll()
	return true, nil
}

// recordPresent reports whether either daemon file is still on disk. The daemon
// removes both together (daemon.Clear) when it begins its graceful shutdown, so
// their absence means it has exited and Stop need not wait for the process to
// be reaped.
func recordPresent(paths Paths) bool {
	for _, f := range []string{paths.PID, paths.State} {
		if f != "" {
			if _, err := os.Stat(f); err == nil {
				return true
			}
		}
	}
	return false
}

// Status reports the daemon for the workspace. A stale record is cleaned up
// and reported as stopped, so status is also what unblocks a restart after a
// crash.
func (r Runner) Status() (State, bool) {
	paths := ForRoots(r.Roots)
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
func Record(roots []string, s State) error {
	paths := ForRoots(roots)
	if paths.PID == "" {
		return errors.New("daemon: no state directory for this workspace")
	}
	if len(roots) > 0 {
		s.Roots = append([]string(nil), roots...)
	}
	if err := WritePID(paths.PID, s.PID); err != nil {
		return err
	}
	return WriteState(paths.State, s)
}

// Clear removes the daemon files on the way out. Nil-safe for a workspace with
// no state dir.
func Clear(roots []string) { ForRoots(roots).removeAll() }

// Entry is one running daemon as `daemon list` sees it: the label a user gave
// it, the workspace roots it serves, its pid and the address a client reaches.
type Entry struct {
	Workspace string   `json:"workspace,omitempty"`
	Roots     []string `json:"roots,omitempty"`
	PID       int      `json:"pid"`
	TCP       string   `json:"tcp,omitempty"`
	Socket    string   `json:"socket,omitempty"`
}

// displayLabel is the LABEL column: the label the user gave the daemon, or "-"
// when there is none. A derived name is never shown, because it is not
// addressable with --workspace and showing it would read as one.
func (e Entry) displayLabel() string {
	if e.Workspace != "" {
		return e.Workspace
	}
	return "-"
}

// Address is the address a client would dial: the TCP address when the daemon
// serves one, else the Unix socket.
func (e Entry) Address() string {
	if e.TCP != "" {
		return e.TCP
	}
	return e.Socket
}

// primaryRoot is the first recorded root, cleaned. Record stamps every record
// written now; a record from before that has no roots and names no workspace.
func primaryRoot(e Entry) string {
	if len(e.Roots) == 0 {
		return ""
	}
	return filepath.Clean(e.Roots[0])
}

// List lists every running daemon across all workspaces. A missing workspaces
// directory is an empty list, not an error: no daemon has ever run.
func List() ([]Entry, error) {
	return ListDir(session.WorkspacesDir())
}

// ListDir lists the running daemons recorded directly under dir, one
// <dir>/<workspaceKey>/daemon.json per daemon. A record whose pid is not alive
// is stale: its files are removed and it is not listed. Entries are sorted by
// display label, then by primary root. A missing dir is an empty list.
//
// The liveness check is the Ops/Alive seam, so a test drives it without
// signalling a process.
func ListDir(dir string) ([]Entry, error) {
	return listDir(dir, Ops{})
}

func listDir(dir string, ops Ops) ([]Entry, error) {
	if dir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	life := ops.life()
	var out []Entry
	for _, de := range entries {
		if !de.IsDir() {
			continue
		}
		keyDir := filepath.Join(dir, de.Name())
		statePath := filepath.Join(keyDir, "daemon.json")
		s, ok := ReadState(statePath)
		if !ok {
			continue
		}
		if s.PID <= 0 || !life(s.PID) {
			os.Remove(statePath)
			os.Remove(filepath.Join(keyDir, "daemon.pid"))
			continue
		}
		out = append(out, Entry{
			Workspace: s.Workspace,
			Roots:     s.Roots,
			PID:       s.PID,
			TCP:       s.TCP,
			Socket:    s.Socket,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if li, lj := out[i].displayLabel(), out[j].displayLabel(); li != lj {
			return li < lj
		}
		return primaryRoot(out[i]) < primaryRoot(out[j])
	})
	return out, nil
}

// FindWorkspace returns the running daemon whose explicit label is name. Only a
// label the user set resolves; a derived display name is not addressable. A
// label two running daemons carry is an error rather than a coin toss: start
// and restart refuse to create a second one, so an ambiguity means a record
// written by hand, and the caller must resolve it rather than pick one.
func FindWorkspace(name string) (Entry, bool, error) {
	if name == "" {
		return Entry{}, false, nil
	}
	entries, err := List()
	if err != nil {
		return Entry{}, false, err
	}
	var found Entry
	matches := 0
	for _, e := range entries {
		if e.Workspace == name {
			found = e
			matches++
		}
	}
	if matches > 1 {
		return Entry{}, false, fmt.Errorf("%d running daemons are labelled %q", matches, name)
	}
	return found, matches == 1, nil
}

// SetWorkspace records the label of a daemon Start has already launched. It
// rewrites daemon.json only, preserving the pid, addresses and token the
// running daemon published, so the parent process can name a daemon without the
// editor binary needing a label flag of its own. The record is keyed by the
// whole root set, so the caller passes the same set Start was given.
func SetWorkspace(roots []string, name string) error {
	paths := ForRoots(roots)
	if paths.State == "" {
		return errors.New("daemon: no state directory for this workspace")
	}
	s, ok := ReadState(paths.State)
	if !ok {
		return errors.New("daemon: no running daemon to label")
	}
	s.Workspace = name
	if len(s.Roots) == 0 {
		s.Roots = append([]string(nil), roots...)
	}
	return WriteState(paths.State, s)
}

// claimWorkspace refuses a label a different running daemon already carries, so
// labels keep resolving to exactly one daemon. The label's own daemon is
// allowed: restarting it is not a collision.
func claimWorkspace(label string, roots []string) error {
	if label == "" {
		return nil
	}
	e, found, err := FindWorkspace(label)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	if sameRootSet(e.Roots, roots) {
		return nil
	}
	return fmt.Errorf("workspace %q is already used by the daemon at %s", label, primaryRoot(e))
}

// sameRootSet reports whether two root slices name the same set. Order does not
// matter and each path is cleaned first, so a daemon recognised by a different
// spelling or ordering of its own roots is not mistaken for a collision.
func sameRootSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	as, bs := cleanRoots(a), cleanRoots(b)
	sort.Strings(as)
	sort.Strings(bs)
	for i := range as {
		if as[i] != bs[i] {
			return false
		}
	}
	return true
}

func cleanRoots(roots []string) []string {
	out := make([]string, len(roots))
	for i, r := range roots {
		out[i] = filepath.Clean(strings.TrimSpace(r))
	}
	return out
}

// resolveTargets turns a stop, status or restart invocation into the workspace
// root set to act on. Without a label it is the positional [dir...] set,
// resolved exactly as before; with a label it is the root set the labelled
// running daemon recorded, so the paths never have to be remembered or retyped.
func resolveTargets(dirs []string, label string) ([]string, error) {
	if label == "" {
		return ResolveRoots(dirs)
	}
	if len(dirs) > 0 {
		return nil, fmt.Errorf("cannot combine --workspace %q with a directory", label)
	}
	e, found, err := FindWorkspace(label)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("no running daemon labelled %q", label)
	}
	if len(e.Roots) == 0 {
		return nil, fmt.Errorf("daemon %q has no recorded workspace root", label)
	}
	return append([]string(nil), e.Roots...), nil
}

// daemonLabel is the label a start or restart writes into the record: an
// explicit --workspace wins, otherwise a restart carries the label the running
// daemon already had and a start has none to carry. It is pure, so the
// carry-versus-override choice is testable without spawning a daemon.
func daemonLabel(explicit, existing string) string {
	if explicit != "" {
		return explicit
	}
	return existing
}

// ResolveRoots is the workspace root set a daemon command acts on: each
// non-empty argument resolved to the repository containing it, or to itself,
// the same walk-up the editor uses (a file argument uses its directory). With
// no arguments the set is the working directory, as before. The result is
// validated by workspace.New, so the roots are canonical absolute paths, exact
// duplicates are dropped keeping the first occurrence's position, and a nesting
// argument is an error rather than a daemon that serves one tree twice.
func ResolveRoots(args []string) ([]string, error) {
	dirs := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == "" {
			continue
		}
		abs, err := filepath.Abs(arg)
		if err != nil {
			return nil, err
		}
		if info, err := os.Stat(abs); err == nil && !info.IsDir() {
			abs = filepath.Dir(abs)
		}
		dirs = append(dirs, control.WorkspaceRoot(abs))
	}
	if len(dirs) == 0 {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		dirs = append(dirs, control.WorkspaceRoot(cwd))
	}
	roots, err := workspace.New(dirs...)
	if err != nil {
		return nil, err
	}
	return roots.All(), nil
}

const usage = `usage: raj daemon <command> [dir...] [options]

  start [dir...] [--control-addr ADDR] [--no-restore] [--workspace NAME]
                             run the headless host in the background; refused
                             while one already runs for the workspace; --no-restore
                             starts it without the previous session; --workspace
                             labels the daemon
  restart [dir...] [--control-addr ADDR] [--no-restore] [--workspace NAME]
                             stop the daemon and start a fresh one, keeping its
                             TCP address and token unless --control-addr
                             overrides the address; the socket path is always
                             the new process default (RAJ_CONTROL_SOCKET/XDG);
                             --workspace alone names the daemon to restart and
                             with a dir overrides its label
  stop [dir...] [--workspace NAME]
                             stop the background daemon; a no-op when none runs
  status [dir...] [--workspace NAME]
                             report whether the daemon runs, and its socket,
                             TCP address and token
  list                       list the running daemons across all workspaces,
                             one row each: label, roots, pid, address, status
  ps                         alias for list

dir is one of the workspace roots (default: the current directory), resolved to
the repository the editor would choose. Several directories are one root set;
stop, status and restart must name the same set the daemon was started with,
never a subset or a member. --workspace NAME targets a running daemon by the
label it was started with instead of by [dir...]. A label is unique among
running daemons. The pidfile, daemon.json and daemon.log live in the workspace
state directory, so one workspace set has one daemon.
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
	case "list", "ps":
		return listCLI(args[1:], stdout, stderr)
	case "restart":
		return restartCLI(args[1:], stdout, stderr)
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

// controlForward builds the daemon run flags for the control listener and the
// session choice from the start and restart CLI values. An empty address is
// omitted so the child keeps its own default rather than being handed an empty
// one; --no-restore is a bool and carries no value.
func controlForward(addr string, noRestore bool) []string {
	var args []string
	if addr != "" {
		args = append(args, "--control-addr", addr)
	}
	if noRestore {
		args = append(args, "--no-restore")
	}
	return args
}

// listCLI prints one row per running daemon: its label, roots, pid, the address
// it listens on and its status. --json emits the same entries for a script.
func listCLI(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("raj daemon list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: raj daemon list [--json]")
	}
	asJSON := fs.Bool("json", false, "print the running daemons as JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "raj daemon list: unexpected argument %q; list takes no directory\n", fs.Arg(0))
		return 2
	}
	entries, err := List()
	if err != nil {
		fmt.Fprintln(stderr, "raj daemon list:", err)
		return 1
	}
	if *asJSON {
		if entries == nil {
			entries = []Entry{}
		}
		b, err := json.MarshalIndent(entries, "", "  ")
		if err != nil {
			fmt.Fprintln(stderr, "raj daemon list:", err)
			return 1
		}
		fmt.Fprintln(stdout, string(b))
		return 0
	}
	if len(entries) == 0 {
		fmt.Fprintln(stdout, "raj daemon: none running")
		return 0
	}
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "LABEL\tROOTS\tPID\tADDRESS\tSTATUS")
	for _, e := range entries {
		roots := strings.Join(e.Roots, ",")
		if roots == "" {
			roots = "-"
		}
		addr := e.Address()
		if addr == "" {
			addr = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\trunning\n", e.displayLabel(), roots, e.PID, addr)
	}
	if err := tw.Flush(); err != nil {
		fmt.Fprintln(stderr, "raj daemon list:", err)
		return 1
	}
	return 0
}

func startCLI(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("raj daemon start", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		out := fs.Output()
		fmt.Fprintln(out, "usage: raj daemon start [dir...] [--control-addr ADDR] [--no-restore] [--workspace NAME]")
		control.PrintFlagUsage(out, fs)
	}
	addr := fs.String("control-addr", control.DefaultTCPAddr, "TCP control address for the daemon; "+control.DefaultTCPAddr+" is the default")
	noRestore := fs.Bool("no-restore", false, "start the daemon without restoring the previous session")
	workspace := fs.String("workspace", "", "label this daemon so stop, status and restart can name it")
	prepared, err := flagsFirst(args, startSpec)
	if err != nil {
		fmt.Fprintln(stderr, "raj daemon start:", err)
		return 2
	}
	if err := fs.Parse(prepared); err != nil {
		return 2
	}
	roots, err := ResolveRoots(fs.Args())
	if err != nil {
		fmt.Fprintln(stderr, "raj daemon start:", err)
		return 1
	}
	if err := claimWorkspace(*workspace, roots); err != nil {
		fmt.Fprintln(stderr, "raj daemon start:", err)
		return 1
	}
	forward := controlForward(*addr, *noRestore)
	pid, err := Runner{Roots: roots, Args: forward}.Start()
	if err != nil {
		fmt.Fprintln(stderr, "raj daemon start:", err)
		return 1
	}
	if label := daemonLabel(*workspace, ""); label != "" {
		if err := SetWorkspace(roots, label); err != nil {
			fmt.Fprintln(stderr, "raj daemon start:", err)
			return 1
		}
	}
	fmt.Fprintf(stdout, "raj daemon: started pid %d\n", pid)
	if log := ForRoots(roots).Log; log != "" {
		fmt.Fprintf(stdout, "raj daemon: log %s\n", log)
	}
	return 0
}

func stopCLI(args []string, stdout, stderr io.Writer) int {
	roots, err := commandTargets("stop", args, stderr)
	if err != nil {
		return 2
	}
	stopped, err := (Runner{Roots: roots}).Stop()
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
	roots, err := commandTargets("status", args, stderr)
	if err != nil {
		return 2
	}
	s, running := (Runner{Roots: roots}).Status()
	if !running {
		fmt.Fprintln(stdout, "raj daemon: stopped")
		return 1
	}
	fmt.Fprintf(stdout, "raj daemon: running (pid %d)\n", s.PID)
	printState(stdout, s)
	return 0
}

func restartCLI(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("raj daemon restart", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		out := fs.Output()
		fmt.Fprintln(out, "usage: raj daemon restart [dir...] [--control-addr ADDR] [--no-restore] [--workspace NAME]")
		fmt.Fprintln(out, "restart the daemon, keeping the running one's TCP address and token")
		fmt.Fprintln(out, "unless --control-addr overrides the address. The socket path is not")
		fmt.Fprintln(out, "recorded, so the new daemon binds its own default.")
		control.PrintFlagUsage(out, fs)
	}
	addr := fs.String("control-addr", control.DefaultTCPAddr, "TCP control address; default: the running daemon's")
	noRestore := fs.Bool("no-restore", false, "restart the daemon without restoring the previous session")
	workspace := fs.String("workspace", "", "the label to carry across, or with a dir, to give the daemon")
	prepared, err := flagsFirst(args, restartSpec)
	if err != nil {
		fmt.Fprintln(stderr, "raj daemon restart:", err)
		return 2
	}
	if err := fs.Parse(prepared); err != nil {
		return 2
	}
	// Only a flag the user typed overrides the recorded address: a default is
	// not a decision, and passing it would move a daemon off the address its
	// clients already know on every restart.
	var addrSet bool
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "control-addr" {
			addrSet = true
		}
	})
	dirs := fs.Args()
	var roots []string
	if *workspace != "" && len(dirs) == 0 {
		// --workspace alone names the running daemon to restart, resolved by
		// its recorded root set so the paths never have to be retyped.
		roots, err = resolveTargets(nil, *workspace)
		if err != nil {
			fmt.Fprintln(stderr, "raj daemon restart:", err)
			return 1
		}
	} else {
		roots, err = ResolveRoots(dirs)
		if err != nil {
			fmt.Fprintln(stderr, "raj daemon restart:", err)
			return 1
		}
		// A label alongside a directory overrides the label of the target, so
		// it must not collide with a different running daemon.
		if err := claimWorkspace(*workspace, roots); err != nil {
			fmt.Fprintln(stderr, "raj daemon restart:", err)
			return 1
		}
	}
	existing := ""
	if *workspace == "" {
		if old, running := (Runner{Roots: roots}).Status(); running {
			existing = old.Workspace
		}
	}
	label := daemonLabel(*workspace, existing)
	preserve := ""
	if addrSet {
		preserve = *addr
	}
	pid, s, err := (Runner{Roots: roots}).Restart(preserve, *noRestore)
	if err != nil {
		fmt.Fprintln(stderr, "raj daemon restart:", err)
		return 1
	}
	if label != "" {
		if err := SetWorkspace(roots, label); err != nil {
			fmt.Fprintln(stderr, "raj daemon restart:", err)
			return 1
		}
	}
	fmt.Fprintf(stdout, "raj daemon: restarted pid %d\n", pid)
	printState(stdout, s)
	return 0
}

// printState writes the control address and token lines status and restart
// share, so a restart reports the same facts a status would.
func printState(w io.Writer, s State) {
	if s.Socket != "" {
		fmt.Fprintf(w, "  socket %s\n", s.Socket)
	}
	if s.TCP != "" {
		fmt.Fprintf(w, "  tcp %s\n", s.TCP)
	}
	if s.Token != "" {
		fmt.Fprintf(w, "  token %s\n", s.Token)
	}
}

// flagSpec is one daemon subcommand's flag surface: known is every flag name,
// so a bare token that matches one is refused, and takesValue marks the flags
// that consume the next argument so flagsFirst keeps a value beside its flag.
type flagSpec struct {
	known      map[string]bool
	takesValue map[string]bool
}

var (
	startSpec = flagSpec{
		known:      map[string]bool{"control-addr": true, "no-restore": true, "workspace": true},
		takesValue: map[string]bool{"control-addr": true, "workspace": true},
	}
	restartSpec = startSpec
	stopSpec    = flagSpec{
		known:      map[string]bool{"workspace": true},
		takesValue: map[string]bool{"workspace": true},
	}
)

// flagsFirst moves the bare [dir...] arguments to the end, so flag.Parse sees
// the flags first and the directories as its positionals whichever order the
// user typed them in: `raj daemon start --control-addr X dir` and `... dir
// --control-addr X` both work. Value-taking flags keep the value beside them.
//
// A bare token that names a flag is refused rather than taken for the
// directory, with the double-dash spelling named: `start no-restore` would
// otherwise quietly start a daemon rooted at ./no-restore.
func flagsFirst(args []string, spec flagSpec) ([]string, error) {
	var flags, dirs []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			flags = append(flags, a)
			name := strings.TrimLeft(strings.SplitN(a, "=", 2)[0], "-")
			if spec.takesValue[name] && !strings.Contains(a, "=") && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
			continue
		}
		if spec.known[a] {
			return nil, fmt.Errorf("unknown argument %q; did you mean --%s?", a, a)
		}
		dirs = append(dirs, a)
	}
	return append(flags, dirs...), nil
}

// commandTargets parses the optional [dir...] set and the optional --workspace
// label the stop and status subcommands take, and returns the workspace root
// set to act on: the labelled running daemon's recorded roots, or [dir...] as
// before.
func commandTargets(name string, args []string, stderr io.Writer) ([]string, error) {
	fs := flag.NewFlagSet("raj daemon "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "usage: raj daemon %s [dir...] [--workspace NAME]\n", name)
	}
	workspace := fs.String("workspace", "", "target the running daemon with this label instead of [dir...]")
	prepared, err := flagsFirst(args, stopSpec)
	if err != nil {
		fmt.Fprintln(stderr, "raj daemon "+name+":", err)
		return nil, err
	}
	if err := fs.Parse(prepared); err != nil {
		return nil, err
	}
	roots, err := resolveTargets(fs.Args(), *workspace)
	if err != nil {
		fmt.Fprintln(stderr, "raj daemon "+name+":", err)
		return nil, err
	}
	return roots, nil
}
