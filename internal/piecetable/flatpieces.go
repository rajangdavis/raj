package piecetable

// FlatPieces stores piece descriptors as fixed-width records packed into one
// contiguous []byte, instead of a []struct or a tree of *struct nodes.
//
// Record layout (little-endian), width W chosen at construction:
//
//	[ 1 byte : buffer id (0=original, 1=add) ]
//	[ W bytes: start offset into that buffer  ]
//	[ W bytes: length of the run              ]
//
// So one record is rec = 1 + 2*W bytes. With W=5 that's 11 bytes and addresses
// buffers up to 2^40-1 (~1 TiB); with W=6, 13 bytes and 2^48-1 (~256 TiB).
//
// Why bother: a []struct{buf,start,len int} is 24 bytes/piece; a pointer-linked
// order-statistics BST node is ~56-72 bytes/piece and scatters across the heap
// (cache misses + GC pointer scanning). A flat record is 11-13 bytes, fully
// contiguous, and holds zero pointers, so the whole descriptor array is one GC
// no-op allocation that streams through cache.
//
// Note: `length` is duplicated here and in the index tree. If your index (the
// B-tree/Fenwick) already owns lengths, you can drop the length field and store
// just [buf][start] for rec = 1 + W bytes. Kept here so the flat buffer is
// self-describing, matching the "everything in the byte string" idea.

type FlatPieces struct {
	w   int // width of start & length fields
	rec int // record stride = 1 + 2*w
	n   int // number of records
	buf []byte
}

// WidthFor returns the smallest field width (bytes) that can address maxLen.
func WidthFor(maxLen int) int {
	w := 1
	for w < 8 && (uint64(1)<<(8*w)) <= uint64(maxLen) {
		w++
	}
	return w
}

func NewFlatPieces(width, capacityHint int) *FlatPieces {
	return &FlatPieces{
		w:   width,
		rec: 1 + 2*width,
		buf: make([]byte, 0, (1+2*width)*capacityHint),
	}
}

// fixed-width little-endian codecs (correct for any width 1..8) ------------

func putUintW(b []byte, w int, v int) {
	u := uint64(v)
	for i := 0; i < w; i++ {
		b[i] = byte(u)
		u >>= 8
	}
}
func getUintW(b []byte, w int) int {
	var u uint64
	for i := w - 1; i >= 0; i-- {
		u = u<<8 | uint64(b[i])
	}
	return int(u)
}

func (f *FlatPieces) Count() int    { return f.n }
func (f *FlatPieces) RecSize() int  { return f.rec }
func (f *FlatPieces) Bytes() int    { return len(f.buf) }
func (f *FlatPieces) off(i int) int { return i * f.rec }

func (f *FlatPieces) Get(i int) (bufID, start, length int) {
	o := f.off(i)
	return int(f.buf[o]), getUintW(f.buf[o+1:], f.w), getUintW(f.buf[o+1+f.w:], f.w)
}

func (f *FlatPieces) Set(i, bufID, start, length int) {
	o := f.off(i)
	f.buf[o] = byte(bufID)
	putUintW(f.buf[o+1:], f.w, start)
	putUintW(f.buf[o+1+f.w:], f.w, length)
}

func (f *FlatPieces) Append(bufID, start, length int) {
	o := len(f.buf)
	f.buf = append(f.buf, make([]byte, f.rec)...)
	f.buf[o] = byte(bufID)
	putUintW(f.buf[o+1:], f.w, start)
	putUintW(f.buf[o+1+f.w:], f.w, length)
	f.n++
}

// InsertAt splices a record at logical index i (O(n) byte memmove).
func (f *FlatPieces) InsertAt(i, bufID, start, length int) {
	o := f.off(i)
	f.buf = append(f.buf, make([]byte, f.rec)...)
	copy(f.buf[o+f.rec:], f.buf[o:len(f.buf)-f.rec])
	f.buf[o] = byte(bufID)
	putUintW(f.buf[o+1:], f.w, start)
	putUintW(f.buf[o+1+f.w:], f.w, length)
	f.n++
}

// DeleteAt removes the record at logical index i (O(n) byte memmove).
func (f *FlatPieces) DeleteAt(i int) {
	o := f.off(i)
	copy(f.buf[o:], f.buf[o+f.rec:])
	f.buf = f.buf[:len(f.buf)-f.rec]
	f.n--
}

// ---- a piece table composed from FlatPieces + the B-tree index -----------
//
// The B-tree owns lengths (so position<->index is O(log n)); FlatPieces owns
// the (buf,start,length) descriptors, addressed by that index. Structural edits
// touch both: O(log n) in the tree + O(n) memmove in the flat array. See notes
// in the benchmark output about keeping descriptors *inside* leaves to make
// edits fully O(log n).

// PieceRec is one piece: a span of Length bytes starting at Start within text
// store Buf. Buf is an author id — 0 is the original file, 1 the user, 2+ each
// agent — which is what makes attribution free: every piece already records who
// wrote it, in a byte the record layout already reserved.
type PieceRec struct{ Buf, Start, Length int }

// ---- batch multi-edit (single-pass) --------------------------------------
//
// The payoff of a flat fixed-width buffer under MULTI-EDIT: apply N edits in
// one sequential pass instead of N independent O(log n) tree descents. Fixed
// width means record i is at byte i*rec with no traversal, so a whole batch is
// a merge that streams through memory once — bandwidth-bound, prefetcher-
// friendly — rather than N pointer-chasing, cache-missing walks.

// BatchIns is one insertion in a batch: place (Buf,Start,Length) BEFORE the
// original record at logical index At. Batches must be sorted by At ascending
// (stable for equal At).
type BatchIns struct {
	At, Buf, Start, Length int
}

// ApplyBatchInserts splices N records using at most N+1 bulk memmoves — one per
// run of untouched records between insertion points — instead of N independent
// shifts. Records before the first insertion never move. This is the flat
// buffer's multi-edit advantage: the whole batch streams through memory
// sequentially (bandwidth-bound), not as N pointer-chasing descents.
func (f *FlatPieces) ApplyBatchInserts(ops []BatchIns) {
	N := len(ops)
	if N == 0 {
		return
	}
	oldN := f.n
	rec := f.rec
	f.buf = append(f.buf, make([]byte, N*rec)...)
	srcEnd := oldN // exclusive end of the current old run
	shift := N     // how far right the current run moves (rec units)
	for j := N - 1; j >= 0; j-- {
		runStart := ops[j].At
		if srcEnd > runStart { // bulk-move the whole run in one memmove
			copy(f.buf[(runStart+shift)*rec:(srcEnd+shift)*rec], f.buf[runStart*rec:srcEnd*rec])
		}
		p := (runStart + shift - 1) * rec // slot just left of the moved run
		f.buf[p] = byte(ops[j].Buf)
		putUintW(f.buf[p+1:], f.w, ops[j].Start)
		putUintW(f.buf[p+1+f.w:], f.w, ops[j].Length)
		shift--
		srcEnd = runStart
	}
	// records [0, ops[0].At) keep shift 0 — untouched, no copy
	f.n = oldN + N
}

// ApplyBatchDeletes removes records at the given indices (sorted ascending,
// distinct) with at most N+1 bulk memmoves compacting the runs between them.
func (f *FlatPieces) ApplyBatchDeletes(idxs []int) {
	if len(idxs) == 0 {
		return
	}
	rec := f.rec
	dst := idxs[0]
	for k := 0; k < len(idxs); k++ {
		runStart := idxs[k] + 1
		runEnd := f.n
		if k+1 < len(idxs) {
			runEnd = idxs[k+1]
		}
		if runEnd > runStart {
			copy(f.buf[dst*rec:], f.buf[runStart*rec:runEnd*rec])
			dst += runEnd - runStart
		}
	}
	f.n -= len(idxs)
	f.buf = f.buf[:f.n*rec]
}
