package intent

import (
	"fmt"
	"sort"
	"strings"
)

// WaveGroup is one change set of a wave as the dependency check sees it: the
// group id, the buffer path that qualifies it, and the other groups its
// projected span overlaps. The slice a caller passes is in wave order, oldest
// set first; overlapping sets are the derived dependency edges.
//
// The id alone is session-local, so a group is keyed by path and id, exactly as
// a Member is.
type WaveGroup struct {
	ID       uint64
	Path     string
	Overlaps []uint64
}

// waveKey identifies a group within one wave.
type waveKey struct {
	path string
	id   uint64
}

// DependencyWarning reports a non-linear dependency among a wave's groups, or
// "" when the graph is a linear order (or has no edges at all). An edge runs
// from the earlier group to each later group it overlaps: the later set's bytes
// sit on the earlier one's, so the later depends on the earlier. A group with
// two predecessors (a merge) or two successors (a branch) cannot be ordered
// without guessing, so the report names it and the caller surfaces it;
// independent groups with no edges are fine. The order comes from the slice, so
// the graph can never be circular.
func DependencyWarning(groups []WaveGroup) string {
	index := make(map[waveKey]int, len(groups))
	for i, g := range groups {
		index[waveKey{g.Path, g.ID}] = i
	}
	preds := make([]map[waveKey]bool, len(groups))
	succs := make([]map[waveKey]bool, len(groups))
	for i := range groups {
		preds[i] = map[waveKey]bool{}
		succs[i] = map[waveKey]bool{}
	}
	for i, g := range groups {
		for _, ov := range g.Overlaps {
			j, ok := index[waveKey{g.Path, ov}]
			if !ok || j == i {
				// An overlap outside the wave is another intention's; the two
				// sets are not both part of this commit's order.
				continue
			}
			from, to := i, j
			if from > to {
				from, to = to, from
			}
			succs[from][waveKey{groups[to].Path, groups[to].ID}] = true
			preds[to][waveKey{groups[from].Path, groups[from].ID}] = true
		}
	}
	for i, g := range groups {
		if len(preds[i]) > 1 {
			return fmt.Sprintf("wave dependency is non-linear: group %d in %s inherits from %s",
				g.ID, g.Path, formatWaveKeys(preds[i]))
		}
		if len(succs[i]) > 1 {
			return fmt.Sprintf("wave dependency is non-linear: group %d in %s branches to %s",
				g.ID, g.Path, formatWaveKeys(succs[i]))
		}
	}
	return ""
}

// formatWaveKeys renders a dependency set in a stable order, so a report is
// reproducible.
func formatWaveKeys(keys map[waveKey]bool) string {
	out := make([]string, 0, len(keys))
	for k := range keys {
		out = append(out, fmt.Sprintf("%d@%s", k.id, k.path))
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}
