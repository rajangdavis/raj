package term

import (
	"os"
	"os/signal"
	"syscall"
)

// Suspend backgrounds raj the way ctrl+z backgrounds any process, but unwinds
// the terminal first and rebuilds it on resume.
//
// The order matters. While raj is stopped it cannot service the tty, so a
// terminal still holding raj's KKP flags would swallow the host's own
// keybindings with nothing left running to release them — the user gets a shell
// where cmd+w does nothing and no obvious way out. Verified against a patched
// Ghostty: after the pop, cmd+w closes the tab normally, and `fg` restores.
//
// onResume runs after the terminal is rebuilt, for callers that need to redraw
// or re-query the theme. It may be nil.
func (t *Terminal) Suspend(onResume func()) error {
	flags := t.flags
	t.Leave()

	cont := make(chan os.Signal, 1)
	signal.Notify(cont, syscall.SIGCONT)
	defer signal.Stop(cont)

	// Signal the whole process group, not just this pid. Under `go run` the
	// binary is a child of the go tool: stopping only ourselves leaves the
	// parent running, the shell never registers a stopped job, and `fg` has
	// nothing to resume.
	if err := syscall.Kill(0, syscall.SIGTSTP); err != nil {
		return err
	}
	<-cont

	if err := t.Enter(flags); err != nil {
		return err
	}
	if onResume != nil {
		onResume()
	}
	return nil
}

// HandleFatalSignals unwinds the terminal on SIGTERM/SIGHUP and exits. Without
// it, a kill leaves the shell with raj's KKP flags still pushed.
//
// It returns a stop function; callers should also `defer t.Leave()` for the
// ordinary path and for panics.
func (t *Terminal) HandleFatalSignals() (stop func()) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM, syscall.SIGHUP)
	done := make(chan struct{})
	go func() {
		select {
		case <-ch:
			t.Leave()
			os.Exit(1)
		case <-done:
		}
	}()
	return func() {
		signal.Stop(ch)
		close(done)
	}
}
