package piecetable

import (
	"fmt"
	"testing"
)

// Benchmarks for the layered-proposals projection surface added in phase
// 1a/1b: Session.Project(Annotated) and Session.Leased, which File.Leased
// calls on the keystroke path.
//
// Two workloads are compared because the projection cost is supposed to
// disappear for a single user and dominate for a swarm:
//
//   - single-user: every change set is the user's and none carries a decision,
//     so HasDecisions is false and Leased returns without projecting.
//   - swarm: eight agent authors, every set Proposed and every third Rejected,
//     so every Leased call falls through to a full Project(Annotated), which
//     walks the whole journal. That is the O(journal) cost being measured.
//
// ApplyDiff is measured only for the swarm shape. Its refusal path exists to
// protect a lease, and the single-user shape has no lease: with no decisions
// there is nothing for a hunk to collide with, so the hunk would land and
// mutate the session the timed loop reads.
//
// The 100k-swarm cases are deliberately kept. The append-heavy journal makes
// Project's offset map reallocate once per op and its Naive composition rescan
// for every splice, so a single 100k Project is seconds rather than
// milliseconds. That is the finding, not an accident of the harness; drop the
// largest size from sizes if you want the suite to finish quickly.
func BenchmarkProjection(b *testing.B) {
	sizes := []int{1000, 10000, 100000}
	shapes := []struct {
		name  string
		swarm bool
	}{
		{"single-user", false},
		{"swarm", true},
	}
	for _, sh := range shapes {
		for _, n := range sizes {
			s, inside, outside := buildProjectionSession(sh.swarm, n)
			b.Run(fmt.Sprintf("%s/ops=%d", sh.name, n), func(b *testing.B) {
				b.Run("Project/Annotated", func(b *testing.B) {
					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						benchProject = s.Project(Annotated)
					}
				})
				b.Run("Leased/inside", func(b *testing.B) {
					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						benchLeaseGroup, benchLeaseOK = s.Leased(inside, 0)
					}
				})
				b.Run("Leased/outside", func(b *testing.B) {
					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						benchLeaseGroup, benchLeaseOK = s.Leased(outside, 0)
					}
				})
				if sh.swarm {
					b.Run("ApplyDiff/lease-refused", func(b *testing.B) {
						base := s.Version()
						hunks := []Hunk{{Start: inside, End: inside + 1, Text: "X"}}
						b.ReportAllocs()
						b.ResetTimer()
						for i := 0; i < b.N; i++ {
							_, conflicts := s.ApplyDiff(User, base, hunks)
							if len(conflicts) != 1 {
								b.Fatalf("hunk did not hit a lease: %d conflicts", len(conflicts))
							}
							benchConflicts = len(conflicts)
						}
					})
				}
			})
		}
	}
}

// Sinks keep the timed calls from being optimised away.
var (
	benchProject    DerivedProject
	benchLeaseGroup uint64
	benchLeaseOK    bool
	benchConflicts  int
)

// projectionBenchBase is the unedited text every session starts from, so a
// position inside it is never leased. Its length sets the base run.
const projectionBenchBase = "base text for projection benchmarks\n"

// buildProjectionSession builds a session over projectionBenchBase holding n
// single-op change sets, each an append of the same four-byte chunk so the
// journal is a flat run of sibling pieces with one group per op.
//
// swarm spreads the sets over eight distinct agent Authors, marks every set
// Proposed and every third Rejected. Otherwise every set is the User's and
// carries no decision.
//
// It returns the session, a position strictly inside an inserted run (a leased
// run for swarm; merely an inserted run when nothing is leased) and a position
// on base text that is never leased. The positions come from the build counter,
// not a random seed, so the journal is identical run to run.
func buildProjectionSession(swarm bool, n int) (s *Session, inside, outside int) {
	const chunk = "abcd"
	s = NewSession(NewDoc(projectionBenchBase, 5))
	outside = 1 // inside the base run: Accepted, so never leased

	authors := []Author{User}
	if swarm {
		// Author 2 is Agent, so 2..9 are eight distinct agent ids.
		authors = []Author{Agent, Agent + 1, Agent + 2, Agent + 3,
			Agent + 4, Agent + 5, Agent + 6, Agent + 7}
	}

	for i := 0; i < n; i++ {
		author := authors[i%len(authors)]
		pos := s.Buffer().Len()
		s.Begin()
		s.Insert(author, pos, chunk)
		s.End()
		if swarm {
			id := s.LastGroup()
			if i%3 == 0 {
				s.MarkGroup(id, Rejected)
			} else {
				s.MarkGroup(id, Proposed)
			}
			// Track the last set, so the probe lands deep in the journal
			// rather than on the first run the scan can return.
			inside = pos + 1
		} else if i == n/2 {
			inside = pos + 1
		}
	}
	return s, inside, outside
}
