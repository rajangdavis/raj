//go:build !linux

package daemon

// processZombie is a no-op off Linux: there is no portable zombie probe, so
// Alive falls back to signal 0. A process owned by another user still answers
// EPERM, which proves it exists.
func processZombie(int) (zombie, known bool) { return false, false }
