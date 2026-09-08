//go:build !race

package search

// raceScale stretches the settle deadlines when the race detector is on.
//
// Settle's deadline exists to turn a hung search into a test failure rather
// than a hang. But the detector instruments every memory access, and a search
// walk is nothing but memory access, so the same work takes roughly an order of
// magnitude longer under `go test -race` — enough that a deadline sized for a
// normal run reports "the search never settled" on a machine where the search
// was merely slow. That failure says the editor is broken when the truth is
// that the runner was busy.
//
// Scaling rather than raising the constant keeps the fast path fast: a real
// hang still fails in seconds under `go test`, and only the instrumented run
// waits longer.
const raceScale = 1
