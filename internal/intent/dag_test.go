package intent

import (
	"strings"
	"testing"
)

// A linear chain of overlapping sets has one predecessor each and is not
// reported.
func TestDependencyWarningAllowsALinearChain(t *testing.T) {
	t.Parallel()
	groups := []WaveGroup{
		{ID: 1, Path: "a.go"},
		{ID: 2, Path: "a.go", Overlaps: []uint64{1}},
		{ID: 3, Path: "a.go", Overlaps: []uint64{2}},
	}
	if got := DependencyWarning(groups); got != "" {
		t.Fatalf("DependencyWarning = %q, want a linear chain to be silent", got)
	}
}

// Independent groups with no edges are also fine: a wave may touch several
// files with no dependency between them.
func TestDependencyWarningAllowsIndependentGroups(t *testing.T) {
	t.Parallel()
	groups := []WaveGroup{
		{ID: 1, Path: "a.go"},
		{ID: 2, Path: "b.go"},
	}
	if got := DependencyWarning(groups); got != "" {
		t.Fatalf("DependencyWarning = %q, want independent groups to be silent", got)
	}
}

// A merge (two predecessors) cannot be ordered and is reported, not guessed.
func TestDependencyWarningReportsAMerge(t *testing.T) {
	t.Parallel()
	groups := []WaveGroup{
		{ID: 1, Path: "a.go", Overlaps: []uint64{3}},
		{ID: 2, Path: "a.go", Overlaps: []uint64{3}},
		{ID: 3, Path: "a.go", Overlaps: []uint64{1, 2}},
	}
	got := DependencyWarning(groups)
	if got == "" || !strings.Contains(got, "non-linear") || !strings.Contains(got, "3") {
		t.Fatalf("DependencyWarning = %q, want a non-linear report naming group 3", got)
	}
}

// A branch (two successors) is reported too.
func TestDependencyWarningReportsABranch(t *testing.T) {
	t.Parallel()
	groups := []WaveGroup{
		{ID: 1, Path: "a.go", Overlaps: []uint64{2, 3}},
		{ID: 2, Path: "a.go", Overlaps: []uint64{1}},
		{ID: 3, Path: "a.go", Overlaps: []uint64{1}},
	}
	got := DependencyWarning(groups)
	if got == "" || !strings.Contains(got, "non-linear") || !strings.Contains(got, "1") {
		t.Fatalf("DependencyWarning = %q, want a non-linear report naming group 1", got)
	}
}

// An overlap that names a group outside the wave is ignored, and a duplicated
// overlap edge is not mistaken for a branch.
func TestDependencyWarningIgnoresForeignAndDuplicateEdges(t *testing.T) {
	t.Parallel()
	groups := []WaveGroup{
		{ID: 1, Path: "a.go", Overlaps: []uint64{2, 2, 99}},
		{ID: 2, Path: "a.go", Overlaps: []uint64{1}},
	}
	if got := DependencyWarning(groups); got != "" {
		t.Fatalf("DependencyWarning = %q, want duplicates and foreign ids ignored", got)
	}
}
