package piecetable

import "math"

// Strategy selection for applying a batch of edits (e.g. an agent's diff).
//
// Two paths with crossing cost curves:
//   - FLAT single pass: one bulk memmove over records from the first edit to the
//     end. Cost ~ (n - firstEditIdx) regardless of how many edits — cheap when
//     edits are many or clustered late, expensive when few and near the front.
//   - PER-OP tree: one O(log n) descent per edit. Cost ~ numEdits * log2(n) —
//     cheap when edits are few, expensive when many.
//
// The router estimates both from three cheap-to-read signals (n, edit count,
// index of the first edit) and picks the smaller. Constants are calibrated to
// the measured benchmarks: a bulk record move is ~0.8 ns, and an insert+delete
// tree op pair is ~260 ns including its log-n descent.
//
// This is the "introsort" pattern for edit application: switch algorithm based
// on the shape of the input, so you always ride the faster curve.

const (
	flatNsPerRecord   = 0.8   // bulk memmove, ns per record touched
	treeNsPerEditPair = 260.0 // insert+delete tree op incl. O(log n) descent
)

// UseFlatBatch reports whether the flat single-pass strategy is predicted faster
// than per-op tree descents for this batch. firstEditIdx is the smallest piece
// index the batch touches (its distance from the front sets the flat tail cost).
func UseFlatBatch(n, numEdits, firstEditIdx int) bool {
	tail := n - firstEditIdx
	if tail < 0 {
		tail = 0
	}
	flatCost := float64(tail) * flatNsPerRecord
	treeCost := float64(numEdits) * math.Log2(float64(n)+2) * (treeNsPerEditPair / 20.0)
	return flatCost < treeCost
}

// StrategyName is a human label for logging/inspection.
func StrategyName(useFlat bool) string {
	if useFlat {
		return "flat-batch"
	}
	return "per-op-tree"
}
