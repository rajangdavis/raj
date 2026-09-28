//go:build unix

package control

import (
	"os/exec"
	"syscall"
)

// Putting a detached hook in its own session.
//
// A hook that restarts the editor would otherwise kill its own run: the run is
// a child of the editor's process group, so whatever takes the editor down
// takes the run with it. Setsid makes the run a session and process-group
// leader of its own, so it survives the editor's exit and can be killed as a
// group later. This file is the platform half and is split by build tag because
// the fields and the signal are POSIX; detach_other.go carries the no-op.

// setDetachedSession starts cmd in a new session rather than just a new process
// group, so it outlives the editor. Setsid also makes it a group leader, which
// is what a later negative-pid signal addresses.
func setDetachedSession(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
}

// killProcessGroupID signals the process group a detached run leads. A
// detached run is a session leader, so its pid is its group id; a negative pid
// addresses the group. It is the pid-only counterpart of killProcessGroup,
// which needs the exec.Cmd a live run holds.
func killProcessGroupID(pgid int) {
	if pgid <= 0 {
		return
	}
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
}

// hookRunProcessAlive reports whether a process with pid still exists. Signal
// zero performs the existence and permission check without delivering
// anything.
func hookRunProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}
