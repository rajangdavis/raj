// Package safe runs background work without leaving the terminal wrecked.
//
// A panic on the main goroutine unwinds through main's defers, so raj pops the
// keyboard flags, leaves the alternate screen and restores the profile before
// the trace is printed. A panic on any *other* goroutine does none of that: Go
// prints the trace and calls exit(2) directly, skipping every deferred call in
// the program. The shell that comes back has raj's KKP flags still pushed, the
// alternate screen still active and the cursor still hidden — a terminal where
// cmd+w does nothing and there is no obvious way out.
//
// raj runs the input decoder, the resize watcher, the tick, the tokeniser, the
// LSP reader and the control server on their own goroutines, so that is five
// ways to lose a terminal to a bug that would otherwise be a stack trace.
package safe

import (
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"sync"
)

var (
	mu       sync.Mutex
	cleanups []func()

	// exit is a variable so the tests can observe what Recover decided
	// without ending the test binary.
	exit           = os.Exit
	errw io.Writer = os.Stderr
)

// OnPanic registers a cleanup to run before the process dies from a panic on a
// background goroutine. Registered rather than passed in because the goroutines
// that need it are started deep inside packages that must not know what a
// terminal is.
//
// Cleanups run in reverse order of registration, like defers, and must be safe
// to call from any goroutine and safe to call twice — the process may be
// halfway through an orderly shutdown when this fires.
func OnPanic(f func()) {
	mu.Lock()
	defer mu.Unlock()
	cleanups = append(cleanups, f)
}

// Go runs fn on a new goroutine, restoring the terminal if it panics.
func Go(fn func()) {
	go func() {
		defer Recover()
		fn()
	}()
}

// Recover is the deferred half of Go, for a goroutine that is started
// elsewhere. It re-raises nothing: once the cleanups have run, the trace is
// printed and the process exits, because a decoder or a tokeniser that has
// panicked is not something the editor can carry on without.
func Recover() {
	r := recover()
	if r == nil {
		return
	}
	trace := debug.Stack()
	runCleanups()
	fmt.Fprintf(errw, "raj: panic in a background goroutine: %v\n\n%s", r, trace)
	exit(2)
}

func runCleanups() {
	mu.Lock()
	fns := cleanups
	cleanups = nil // whatever happens next, do not run these twice
	mu.Unlock()
	for i := len(fns) - 1; i >= 0; i-- {
		func() {
			// A cleanup that panics must not stop the ones behind it. The
			// terminal is more important than the tidiness of this path.
			defer func() { recover() }()
			fns[i]()
		}()
	}
}
