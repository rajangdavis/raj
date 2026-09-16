//go:build unix

package control

import (
	"os/exec"
	"syscall"
)

// Putting a command in its own process group.
//
// exec.CommandContext kills the one process it started. A command a caller
// actually runs is rarely one process -- `sh -c "go test ./..."` is a shell
// with a compiler and a test binary under it -- so killing the shell leaves the
// children running and holding the stdout/stderr pipes open, which is what made
// a cancel hang until they exited on their own. Starting the command as a
// process-group leader and signalling the group reaches all of them.
//
// This file is the platform half and is split by build tag because the fields
// and the signal are POSIX; exec_other.go carries the no-op for platforms
// without process groups. There is no portable way to reap a process tree, so
// the split is the honest expression of that.

// setProcessGroup makes cmd the leader of a new process group, so its pid is
// also the group id a negative-pid signal addresses.
func setProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killProcessGroup signals the group cmd leads. A negative pid is the group,
// which is why the child was made a leader first. An already-reaped process is
// not an error: the caller only wants the tree gone, and Cancel must not turn a
// cancel into a failure of its own.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
