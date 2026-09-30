package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"raj/internal/git"
	"raj/internal/hooks"
)

// The persistent side of detached runs.
//
// A detached hook outlives the request that started it and, because it is put
// in its own session, can outlive the editor too. Its output therefore goes to
// a file rather than the reply, and a shell wrapper writes the exit status
// beside it, so a later editor can recover a run that finished while nobody was
// waiting. The layout is <dir>/<run-id>.log, <run-id>.exit and <run-id>.pid;
// run ids are seeded from the directory so a restart does not reuse a name.

// hookRunTailLines and hookRunTailBytes bound what `hook log --show` reads from
// one run's log file. The reply is a single frame either way, so the cap is what
// keeps a chatty run from turning a read into a flood.
const (
	hookRunTailLines = 400
	hookRunTailBytes = 256 << 10
)

// hookRunWatchPoll is how often an adopted run's exit file is polled. It is a
// variable so a test can shorten it.
var hookRunWatchPoll = 2 * time.Second

// SetHookDir points the server at the directory detached runs use, recovering
// the files a previous editor left and pruning the directory to the log's
// bound. Best-effort: a directory that cannot be made or read leaves detached
// runs unable to start rather than stopping the editor from starting.
func (s *Server) SetHookDir(dir string) {
	s.HookDir = dir
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	s.recoverHookRuns()
}

func hookRunLogPath(dir string, id uint64) string {
	return filepath.Join(dir, strconv.FormatUint(id, 10)+".log")
}

func hookRunExitPath(dir string, id uint64) string {
	return filepath.Join(dir, strconv.FormatUint(id, 10)+".exit")
}

func hookRunPidPath(dir string, id uint64) string {
	return filepath.Join(dir, strconv.FormatUint(id, 10)+".pid")
}

// hookRunID reads the run id out of a file named <id><ext>; ok is false for any
// other name, so the recovery scan ignores stray files.
func hookRunID(name, ext string) (uint64, bool) {
	base, found := strings.CutSuffix(name, ext)
	if !found || base == "" {
		return 0, false
	}
	id, err := strconv.ParseUint(base, 10, 64)
	if err != nil {
		return 0, false
	}
	return id, true
}

// readHookRunExit reads the status a run's wrapper recorded. An absent or
// malformed file reports ok false, which recovery reads as "no exit".
func readHookRunExit(dir string, id uint64) (int, bool) {
	data, err := os.ReadFile(hookRunExitPath(dir, id))
	if err != nil {
		return 0, false
	}
	code, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, false
	}
	return code, true
}

// hookRunProcess is the record a detached run writes to <id>.pid: the process
// id, the start time and absolute deadline in unix nanoseconds (deadline zero
// when the hook set no timeout), the hook name, and the run start stamp -- who
// asked, at which revision and git state. A later editor reads it to re-adopt a
// run that outlived it, keep the run deadline, report the true duration when it
// ends, and, when the editor restarted under a run, fill the recovered log
// entry from the stamp the run recorded rather than from zeros.
type hookRunProcess struct {
	PID      int    `json:"pid"`
	Start    int64  `json:"start,omitempty"`
	Deadline int64  `json:"deadline,omitempty"`
	Hook     string `json:"hook,omitempty"`
	Author   uint8  `json:"author,omitempty"`
	Revision uint64 `json:"revision,omitempty"`
	Head     string `json:"head,omitempty"`
	Dirty    string `json:"dirty,omitempty"`
	// Params are the run's resolved parameter values, recorded so a run
	// recovered after a restart still names what it ran with.
	Params []hooks.ParamValue `json:"params,omitempty"`
}

// readHookRunProcess reads the process record for id. ok is false for an absent
// or malformed file.
func readHookRunProcess(dir string, id uint64) (hookRunProcess, bool) {
	data, err := os.ReadFile(hookRunPidPath(dir, id))
	if err != nil {
		return hookRunProcess{}, false
	}
	var p hookRunProcess
	if err := json.Unmarshal(data, &p); err != nil || p.PID <= 0 {
		return hookRunProcess{}, false
	}
	return p, true
}

// writeHookRunProcess records p for id. A failed write is not fatal: the run
// still executes; only restart-adoption is lost.
func writeHookRunProcess(dir string, id uint64, p hookRunProcess) {
	data, err := json.Marshal(p)
	if err != nil {
		return
	}
	_ = os.WriteFile(hookRunPidPath(dir, id), append(data, '\n'), 0o600)
}

// uniqueIDs returns ids with duplicates removed, order preserved.
func uniqueIDs(ids []uint64) []uint64 {
	seen := make(map[uint64]bool, len(ids))
	out := ids[:0:0]
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// pruneHookRunDir drops the files of finished and lost runs older than the
// log's bound, so the directory cannot grow without end. maxID is the highest
// id seen; ids below maxID-keep+1 are candidates. A live run — a pid record
// whose process is still alive with no exit file — is never pruned, whatever
// its id, because its wrapper still needs the path to write the exit to.
func pruneHookRunDir(dir string, maxID uint64) {
	if dir == "" || maxID == 0 {
		return
	}
	keepFrom := uint64(1)
	if maxID > hooks.DefaultLogSize {
		keepFrom = maxID - uint64(hooks.DefaultLogSize) + 1
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	ids := map[uint64]bool{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		for _, ext := range []string{".log", ".exit", ".pid"} {
			if id, ok := hookRunID(e.Name(), ext); ok {
				ids[id] = true
			}
		}
	}
	for id := range ids {
		if id >= keepFrom {
			continue
		}
		if _, hasExit := readHookRunExit(dir, id); !hasExit {
			if p, hasPid := readHookRunProcess(dir, id); hasPid && hookRunProcessAlive(p.PID) {
				continue
			}
		}
		_ = os.Remove(hookRunLogPath(dir, id))
		_ = os.Remove(hookRunExitPath(dir, id))
		_ = os.Remove(hookRunPidPath(dir, id))
	}
}

// tailHookRunLog returns the tail of one run's log file, bounded by both a byte
// cap and a line cap. A missing file is the named refusal `hook log --show`
// reports.
func tailHookRunLog(dir string, id uint64) (string, error) {
	if dir == "" {
		return "", errors.New("hook runs have no log directory")
	}
	data, err := os.ReadFile(hookRunLogPath(dir, id))
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("no log for hook run %d", id)
		}
		return "", err
	}
	text := string(data)
	if len(text) > hookRunTailBytes {
		text = text[len(text)-hookRunTailBytes:]
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			text = text[i+1:]
		}
	}
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) > hookRunTailLines {
		lines = lines[len(lines)-hookRunTailLines:]
	}
	if len(lines) == 1 && lines[0] == "" {
		return "", nil
	}
	return strings.Join(lines, "\n") + "\n", nil
}

// addHookResult appends to the run log when there is one; the recovery path
// runs before a caller could have asked, so a nil log is a no-op.
func (s *Server) addHookResult(r hooks.Result) {
	if s.HookLog != nil {
		s.HookLog.Add(r)
	}
}

// recoverHookRuns reads what a previous editor left in the run directory. An
// exit file becomes a recovered run; a pid record whose process is gone with no
// exit becomes a lost run; a run still alive under its own session is re-adopted
// and watched. Processed exit and pid files are removed so a later start does
// not log them twice, the directory is pruned, and the registry is seeded above
// the highest id seen so new runs do not reuse old file names.
func (s *Server) recoverHookRuns() {
	dir := s.HookDir
	if dir == "" {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var ids []uint64
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		for _, ext := range []string{".log", ".exit", ".pid"} {
			if id, ok := hookRunID(e.Name(), ext); ok {
				ids = append(ids, id)
			}
		}
	}
	maxID := uint64(0)
	for _, id := range ids {
		if id > maxID {
			maxID = id
		}
	}
	for _, id := range uniqueIDs(ids) {
		exit, hasExit := readHookRunExit(dir, id)
		p, hasPid := readHookRunProcess(dir, id)
		switch {
		case hasExit:
			s.addHookResult(hooks.Result{ID: id, Hook: p.Hook, Author: p.Author,
				Revision: p.Revision, Head: p.Head, Dirty: p.Dirty, Exit: exit,
				Detach: true, Recovered: true, LogPath: hookRunLogPath(dir, id), PID: p.PID,
				Params: p.Params})
			_ = os.Remove(hookRunExitPath(dir, id))
			_ = os.Remove(hookRunPidPath(dir, id))
		case hasPid && hookRunProcessAlive(p.PID):
			// The run outlived the editor that started it: re-register it so
			// `hook ps` and `cancel` reach it, and watch its exit file so its
			// completion is logged when it ends.
			s.adoptHookRun(dir, id, p)
		case hasPid:
			s.addHookResult(hooks.Result{ID: id, Hook: p.Hook, Author: p.Author,
				Revision: p.Revision, Head: p.Head, Dirty: p.Dirty, Detach: true, Lost: true,
				Err:     "lost: the editor restarted and the run left no exit status",
				LogPath: hookRunLogPath(dir, id), PID: p.PID, Params: p.Params})
			_ = os.Remove(hookRunPidPath(dir, id))
		}
	}
	if s.HookRuns != nil {
		s.HookRuns.Seed(maxID)
	}
	pruneHookRunDir(dir, maxID)
}

// adoptHookRun re-registers a detached run a previous editor started and left
// running, under its original id so its file name, `hook ps` row and cancel
// target all keep the id the run was given, then watches its exit file so its
// completion reaches the run log. The run's recorded deadline is honoured; a
// run with no deadline is watched until it ends.
func (s *Server) adoptHookRun(dir string, id uint64, p hookRunProcess) {
	if s.HookRuns == nil {
		return
	}
	var cancelled atomic.Bool
	s.HookRuns.Adopt(id, hooks.Run{
		Hook: p.Hook, Started: time.Unix(0, p.Start), PID: p.PID, PGID: p.PID,
		Cancel: func() {
			cancelled.Store(true)
			killProcessGroupID(p.PID)
		},
	})
	go s.watchAdoptedHookRun(dir, id, p, &cancelled)
}

// watchAdoptedHookRun polls an adopted run's exit file until the run finishes,
// then logs the completion. A run whose process is gone with no exit is logged
// as lost, or as cancelled/timed out when this editor caused the death; a
// deadline in the record kills the group when it passes.
func (s *Server) watchAdoptedHookRun(dir string, id uint64, p hookRunProcess, cancelled *atomic.Bool) {
	timedOut := false
	for {
		if exit, ok := readHookRunExit(dir, id); ok {
			s.logAdoptedCompletion(dir, id, p, exit, "")
			return
		}
		if !hookRunProcessAlive(p.PID) {
			// The wrapper writes the exit file as it exits; give it one poll
			// interval to land before settling the run.
			time.Sleep(hookRunWatchPoll)
			exit, ok := readHookRunExit(dir, id)
			errText := "lost: the run ended without writing an exit status"
			switch {
			case cancelled.Load():
				errText = "cancelled"
			case timedOut:
				errText = fmt.Sprintf("hook %q timed out", p.Hook)
			}
			if !ok {
				exit = -1
			}
			s.logAdoptedCompletion(dir, id, p, exit, errText)
			return
		}
		if p.Deadline > 0 && !timedOut && time.Now().UnixNano() >= p.Deadline {
			// The exit file and the deadline can cross: if the run finished just
			// as the deadline passed, the loop's next pass logs the real exit.
			// Only a run still without one is killed as timed out.
			if _, ok := readHookRunExit(dir, id); ok {
				continue
			}
			timedOut = true
			killProcessGroupID(p.PID)
		}
		time.Sleep(hookRunWatchPoll)
	}
}

// logAdoptedCompletion records an adopted run's end, taking the duration from
// its recorded start, and removes the files a later editor would otherwise
// re-adopt or re-recover.
func (s *Server) logAdoptedCompletion(dir string, id uint64, p hookRunProcess, code int, errText string) {
	duration := int64(0)
	if p.Start > 0 {
		duration = time.Since(time.Unix(0, p.Start)).Milliseconds()
	}
	// Remove the marker files before the log row exists, so "logged" implies
	// "cleaned up" and a reader that sees the row can never race the cleanup.
	_ = os.Remove(hookRunExitPath(dir, id))
	_ = os.Remove(hookRunPidPath(dir, id))
	if s.HookLog != nil {
		s.HookLog.Add(hooks.Result{
			ID: id, Hook: p.Hook, Author: p.Author, Revision: p.Revision,
			Head: p.Head, Dirty: p.Dirty, Exit: code, DurationMS: duration, Detach: true,
			LogPath: hookRunLogPath(dir, id), PID: p.PID, Recovered: true, Err: errText,
			Params: p.Params,
		})
	}
	pruneHookRunDir(dir, id)
	if s.HookRuns != nil {
		s.HookRuns.Remove(id)
	}
}

// detachedArgv wraps a hook's command so its exit status survives the editor:
// the wrapper runs the command, writes the status to $RAJ_HOOK_EXIT and exits
// with the command's own status. The argv form passes the command positionally
// so no argument is re-quoted; the shell form runs the author's string as
// written, in a subshell so an `exit` inside it does not skip the write.
func detachedArgv(argv []string, shell string) []string {
	const write = `; ec=$?; printf '%s\n' "$ec" > "$RAJ_HOOK_EXIT"; exit $ec`
	if shell != "" {
		return []string{"/bin/sh", "-c", "(\n" + shell + "\n)" + write}
	}
	return append([]string{"/bin/sh", "-c", `"$@"` + write, "raj-hook"}, argv...)
}

// startDetachedRun starts argv in its own session with its output going to the
// run's log file. It returns the started command so the caller can install a
// cancel that kills its process group and reap it when it ends.
func startDetachedRun(dir string, id uint64, argv []string, runDir string, env []string) (*exec.Cmd, string, error) {
	if len(argv) == 0 {
		return nil, "", errors.New("detached hook has no command")
	}
	logPath := hookRunLogPath(dir, id)
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, "", err
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = runDir
	cmd.Stdout, cmd.Stderr = f, f
	cmd.Env = append(os.Environ(), env...)
	cmd.Env = append(cmd.Env, "RAJ_HOOK_EXIT="+hookRunExitPath(dir, id))
	setDetachedSession(cmd)
	if err := cmd.Start(); err != nil {
		f.Close()
		return nil, "", err
	}
	// The child holds its own descriptor now; the editor's copy can go.
	f.Close()
	return cmd, logPath, nil
}

// runHookDetached starts an admitted run in its own session and returns at
// once. The gate stays held and the run stays registered until the completion
// goroutine reaps it, so a second run is refused while it is in flight and
// `hook cancel` can reach its process group. cleanup, when set, removes the
// scratch tree a projected run materialised, once the run has finished.
func (c *connection) runHookDetached(req Request, emit func(Response), prep Response,
	prov Provenance, runDir string, caller uint8, cleanup func()) {
	dir := c.srv.HookDir
	if dir == "" {
		if cleanup != nil {
			cleanup()
		}
		emit(Response{ID: req.ID, Err: fmt.Sprintf("hook %q is detached, but the workspace has no run directory", req.HookName), Final: true})
		return
	}
	startedAt := time.Now()
	runID := c.srv.HookRuns.Add(hooks.Run{Hook: req.HookName, Author: caller, Started: startedAt})
	// A detached run honours an explicit hook timeout; a zero timeout means it
	// runs to completion, because a detached hook is often the long one.
	timeout := time.Duration(prep.HookTimeoutMS) * time.Millisecond
	deadline := int64(0)
	if timeout > 0 {
		deadline = startedAt.Add(timeout).UnixNano()
	}
	var cmd *exec.Cmd
	var cancelled atomic.Bool
	// Cancel is installed before the id is visible and reads the command once
	// it is set; a cancel that arrives first is a no-op against a nil command
	// rather than a race.
	c.srv.HookRuns.SetCancel(runID, func() {
		cancelled.Store(true)
		if cmd != nil {
			killProcessGroup(cmd)
		}
	})

	argv := detachedArgv(prep.HookArgv, prep.HookShell)
	paramEnv := hooks.ParamEnv(prep.HookParamValues)
	proc, logPath, serr := startDetachedRun(dir, runID, argv, runDir, paramEnv)
	if serr != nil {
		if cleanup != nil {
			cleanup()
		}
		c.srv.HookGate.End(req.HookName)
		c.srv.HookRuns.Remove(runID)
		emit(Response{ID: req.ID, Err: fmt.Sprintf("hook %q could not start detached: %v", req.HookName, serr), Final: true})
		return
	}
	cmd = proc
	c.srv.HookRuns.SetProcess(runID, proc.Process.Pid, processGroupID(proc))
	writeHookRunProcess(dir, runID, hookRunProcess{
		PID: proc.Process.Pid, Start: startedAt.UnixNano(), Deadline: deadline, Hook: req.HookName,
		Author: caller, Revision: prep.HookRevision, Head: prov.Head, Dirty: prov.DirtyDigest,
		Params: prep.HookParamValues,
	})

	emit(Response{ID: req.ID, OK: true, Stream: StreamStderr,
		Out: fmt.Sprintf("raj: hook %s started detached as run %d pid %d revision %d HEAD %s (dirty %s); log %s\n",
			req.HookName, runID, proc.Process.Pid, prep.HookRevision, prov.Head, prov.DirtyDigest, logPath)})
	emit(Response{ID: req.ID, OK: true, Exit: 0, Final: true,
		HookRunID: runID, HookName: req.HookName, HookRevision: prep.HookRevision,
		HookHead: prov.Head, HookDirty: prov.DirtyDigest, PID: proc.Process.Pid})

	srv := c.srv
	hookName := req.HookName
	root := prep.Root
	workspace, mayWrite := prep.HookTree == string(hooks.TreeWorkspace), prep.HookMayWrite
	pid := proc.Process.Pid
	go func() {
		timedOut := make(chan struct{})
		var timer *time.Timer
		if timeout > 0 {
			timer = time.AfterFunc(timeout, func() {
				close(timedOut)
				killProcessGroup(proc)
			})
		}
		werr := proc.Wait()
		if timer != nil {
			timer.Stop()
		}
		if cleanup != nil {
			cleanup()
		}
		code := 0
		if werr != nil {
			var ee *exec.ExitError
			if errors.As(werr, &ee) {
				code = ee.ExitCode()
			} else {
				code = -1
			}
		}
		recorded, hasExit := readHookRunExit(dir, runID)
		if hasExit {
			code = recorded
		}
		errText := ""
		if werr != nil {
			errText = werr.Error()
		}
		// The same write guard the synchronous path applies, off the event
		// thread because the run is already over.
		if workspace && !mayWrite {
			after, derr := git.New(root).StatusDigest(context.Background())
			switch {
			case derr != nil:
				errText = fmt.Sprintf("hook %q may not write, but its workspace status could not be read: %v", hookName, derr)
			case after != prov.DirtyDigest:
				errText = fmt.Sprintf("hook %q modified the workspace", hookName)
			}
		}
		select {
		case <-timedOut:
			// The deadline and the run's own exit can race: a wrapper that
			// finished just as the timer fired wrote the real status, so only a
			// run that had to be killed is a timeout.
			if !hasExit {
				errText = fmt.Sprintf("hook %q timed out after %s", hookName, timeout)
			}
		default:
			if cancelled.Load() {
				errText = "cancelled"
			}
		}
		// Remove the marker files before the log row exists, so "logged" implies
		// "cleaned up" and a reader that sees the row can never race the cleanup.
		_ = os.Remove(hookRunExitPath(dir, runID))
		_ = os.Remove(hookRunPidPath(dir, runID))
		if srv.HookLog != nil {
			srv.HookLog.Add(hooks.Result{
				ID: runID, Hook: hookName, Author: caller, Revision: prep.HookRevision,
				Head: prov.Head, Dirty: prov.DirtyDigest, Exit: code,
				DurationMS: time.Since(startedAt).Milliseconds(), Detach: true,
				LogPath: logPath, PID: pid, Err: errText, Params: prep.HookParamValues,
			})
		}
		pruneHookRunDir(dir, runID)
		srv.HookGate.End(hookName)
		srv.HookRuns.Remove(runID)
	}()
}
