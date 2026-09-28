//go:build !unix

package control

import "os/exec"

// setDetachedSession is a no-op where there are no POSIX sessions: the run is
// still a child of the editor and will not outlive it. The detach flag is
// accepted, but the survival it promises is a POSIX property.
func setDetachedSession(cmd *exec.Cmd) {}

// killProcessGroupID has no group to signal without POSIX process groups.
func killProcessGroupID(pgid int) {}

// hookRunProcessAlive cannot ask about a pid without POSIX signals, so a run
// with no exit file is reported as gone and recovery logs it lost rather than
// leaving it forever unanswered.
func hookRunProcessAlive(pid int) bool { return false }
