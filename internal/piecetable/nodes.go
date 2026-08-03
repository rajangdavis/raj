package piecetable

// Fanout for the leaf-embedded tree. A leaf holds up to maxE fixed-width
// records in one contiguous blob; an internal node holds up to maxE children
// with cached subtree totals and counts.
//
// 32 keeps a leaf's record blob in the low hundreds of bytes (11-13 B/record at
// W=5/6), so an in-leaf memmove is a handful of cache lines and structural
// edits stay O(log n) plus a bounded shift rather than an O(n) rewrite.
const (
	maxE = 32
	minE = maxE / 2
)

// groupSizes splits m items into balanced groups, each sized within
// [minE, maxE] — except that the whole thing may be one undersized group when
// m <= maxE, which is the legal root case. Used to bulk-load bottom-up in O(n).
func groupSizes(m int) []int {
	if m <= maxE {
		return []int{m}
	}
	g := (m + maxE - 1) / maxE
	base, rem := m/g, m%g
	sizes := make([]int, g)
	for i := range sizes {
		sizes[i] = base
		if i < rem {
			sizes[i]++
		}
	}
	return sizes
}

// sumInts totals a slice.
func sumInts(a []int) (s int) {
	for _, v := range a {
		s += v
	}
	return
}

// insertInt inserts v at index i, growing a by one.
func insertInt(a []int, i, v int) []int {
	a = append(a, 0)
	copy(a[i+1:], a[i:])
	a[i] = v
	return a
}

// removeInt deletes index i from a.
func removeInt(a []int, i int) []int {
	copy(a[i:], a[i+1:])
	return a[:len(a)-1]
}
