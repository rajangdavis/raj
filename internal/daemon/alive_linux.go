//go:build linux

package daemon

import (
	"os"
	"strconv"
)

// processZombie reports whether pid is a zombie, and whether /proc could answer
// at all. Signal 0 cannot distinguish a zombie from a live process, so on Linux
// the process state comes from /proc/<pid>/stat: an exited-but-unreaped child
// stays in state Z and signal 0 still succeeds for it. known is false where
// /proc is not readable, and Alive then falls back to signal 0.
func processZombie(pid int) (zombie, known bool) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false, false
	}
	return zombieState(b)
}
