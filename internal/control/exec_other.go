//go:build !unix

package control

import "os/exec"

// setProcessGroup is a no-op where there are no POSIX process groups. The
// cancel still kills the direct child through exec.CommandContext's default;
// there is no portable way to reach the children it may have started, and
// pretending otherwise would be worse than saying so here.
func setProcessGroup(cmd *exec.Cmd) {}

// killProcessGroup has no group to signal without process groups.
func killProcessGroup(cmd *exec.Cmd) {}
