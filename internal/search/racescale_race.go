//go:build race

package search

// See racescale.go. Ten is the rough cost of the detector on this walk, not a
// tuned number: the deadline only has to be longer than a slow success, and it
// is a backstop for a hang rather than a performance assertion.
const raceScale = 10
