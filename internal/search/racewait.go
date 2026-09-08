package search

import "time"

// Slow stretches a test deadline by the same factor Settle applies, so a test
// that waits on the search worker directly is subject to one rule about the
// detector rather than its own guess.
//
// Untagged on purpose: the constant it multiplies is what varies between a
// race build and a normal one, not this.
func Slow(d time.Duration) time.Duration { return d * raceScale }
