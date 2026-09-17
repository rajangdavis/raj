package piecetable

import (
	"fmt"
	"testing"
)

// Benchmarks for Session.Compact, the whole-session pass App.compactTick runs
// on the idle tick.
//
// The tick debounces to one attempt per CompactInterval, and it skips a pane
// whose (Version, DecisionGeneration) pair is unchanged since the last
// attempt. Under sustained typing the Version moves on every keystroke, so the
// memo spares only a buffer nobody is typing in: roughly every 2 s the tick
// calls Compact(File.SavedVersion()) again. Compact leaves Version where it
// found it, so the next keystroke moves it and the next tick scans again.
//
// The scan is what these benchmarks measure, not the fold. Both fixtures are
// built so Compact has nothing to remove, and the timed loop calls it anyway.
// Two shapes stress different parts of the pass:
//
//   - already compacted: the buffer holds two pieces, but the journal still
//     holds every contributing op. The pass rebuilds allOrigins over the whole
//     journal and runs pieceStable once per piece, so the tick is O(journal)
//     however idle the buffer looks. This is a synthetic fixed point a
//     compaction pass can leave behind.
//   - uncompactable: every inserted piece is its own change set, which is what
//     plain typing produces -- one keystroke, one group, one piece. Nothing may
//     merge, so the pass removes nothing and still runs pieceStable once per
//     piece, each walking the whole journal: O(pieces x journal).
//
// saved is the live version, as the existing Compact tests pass it. Every op
// is then stable (Seq is zero-based, so the last op sits one below Version),
// and pieceStable walks the journal to its end rather than returning early.

// benchCompactBase is the unedited text the session starts from, so the first
// piece belongs to no change set and stays put.
const benchCompactBase = "package main\n"

// benchCompactLine is the text of each synthetic piece: an ordinary source
// line, not a byte, so piece lengths and store bytes are realistic.
const benchCompactLine = "if err := handler(ctx, req); err != nil {\n"

// benchCompactRemoved sinks Compact's return value so the call is not elided.
var benchCompactRemoved int

// BenchmarkCompactAlreadyCompacted times a no-op Compact on a session whose n
// same-change-set inserts have already folded into one piece. Setup runs the
// folding pass -- O(n^2) on this fixture, since each merge re-walks the
// journal, and excluded from the measurement -- and the timed call rebuilds
// the origin index over the n-op journal and walks it for each of the two
// remaining pieces.
func BenchmarkCompactAlreadyCompacted(b *testing.B) {
	for _, n := range []int{1000, 2000, 4000} {
		b.Run(fmt.Sprintf("journal=%d", n), func(b *testing.B) {
			s := buildCompactedSession(n)
			saved := s.Version()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				benchCompactRemoved = s.Compact(saved)
			}
		})
	}
}

// BenchmarkCompactUncompactable times a no-op Compact on a session whose n
// inserted pieces are each their own change set, so no two neighbours share
// an origin and nothing is compactable. The pass removes nothing; per piece
// it still walks the whole journal.
func BenchmarkCompactUncompactable(b *testing.B) {
	for _, n := range []int{500, 1000, 2000, 4000} {
		b.Run(fmt.Sprintf("pieces=%d", n), func(b *testing.B) {
			s := buildUncompactableSession(n)
			saved := s.Version()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				benchCompactRemoved = s.Compact(saved)
			}
		})
	}
}

// buildCompactedSession inserts n lines of Agent text at the end inside one
// change set, then compacts once. Every insert appends to the Agent store, so
// the pieces are document-adjacent and store-contiguous and the one pass folds
// all n into a single piece; the base line stays a second piece. The journal
// still holds n ops, which is the point.
func buildCompactedSession(n int) *Session {
	s := NewSession(NewDoc(benchCompactBase, 0))
	s.Begin()
	for i := 0; i < n; i++ {
		s.Insert(Agent, s.Buffer().Len(), benchCompactLine)
	}
	s.End()
	s.Compact(s.Version())
	return s
}

// buildUncompactableSession inserts n lines of Agent text, each in its own
// change set, so every inserted piece is a distinct origin. Compact refuses to
// pair pieces whose owners differ before it looks at store adjacency, so
// nothing merges and the session stays fragmented.
func buildUncompactableSession(n int) *Session {
	s := NewSession(NewDoc(benchCompactBase, 0))
	for i := 0; i < n; i++ {
		s.Begin()
		s.Insert(Agent, s.Buffer().Len(), benchCompactLine)
		s.End()
	}
	return s
}
