package piecetable

// PieceBTree is an order-statistics B-tree whose LEAVES hold the piece
// descriptors directly, packed as fixed-width byte records — no separate flat
// array, no duplicated length. This is the "descriptors inside the leaves"
// design the earlier benchmarks pointed at.
//
// Leaf record layout (little-endian, width W chosen at construction):
//
//	[ 1 byte : buffer id (0=original, 1=add) ]
//	[ W bytes: start offset into that buffer  ]
//	[ W bytes: length of the run              ]
//
// rec = 1 + 2*W bytes. A leaf holds up to maxE records in one contiguous
// []byte blob; the length lives in that blob and NOWHERE else, so it is the
// single source of truth. Internal nodes cache, per child, the subtree total
// (sum of lengths) and subcount (number of records) — the same order-statistics
// machinery as btree.go — which keeps position<->index O(log n).
//
// The win over "flat array + separate index":
//   - one structure, so length is stored once (no duplication)
//   - structural insert/delete is O(log n) + a bounded in-leaf memmove of at
//     most maxE records, NOT an O(n) memmove of the whole document
//   - leaves stay contiguous and pointer-free, so memory density and cache
//     behaviour of the flat idea are preserved inside each leaf
//
// It reuses putUintW/getUintW and the maxE/minE constants from the package.

type pnode struct {
	leaf bool
	// leaf only:
	data []byte // packed records, each t.rec bytes, count records
	// internal only:
	children []*pnode
	subtotal []int // subtotal[i] == children[i].total
	subcount []int // subcount[i] == children[i].count
	// both:
	total int // sum of lengths in this subtree
	count int // number of records in this subtree
}

type PieceBTree struct {
	root *pnode
	w    int // field width
	rec  int // record stride = 1 + 2*w
}

// ---- leaf record codec ---------------------------------------------------

func (t *PieceBTree) recAt(data []byte, j int) (buf, start, length int) {
	o := j * t.rec
	return int(data[o]), getUintW(data[o+1:], t.w), getUintW(data[o+1+t.w:], t.w)
}
func (t *PieceBTree) recLen(data []byte, j int) int {
	return getUintW(data[j*t.rec+1+t.w:], t.w)
}
func (t *PieceBTree) writeRec(data []byte, o, buf, start, length int) {
	data[o] = byte(buf)
	putUintW(data[o+1:], t.w, start)
	putUintW(data[o+1+t.w:], t.w, length)
}
func (t *PieceBTree) sumLeaf(data []byte) (s int) {
	for j := 0; j < len(data)/t.rec; j++ {
		s += t.recLen(data, j)
	}
	return
}

// ---- construction --------------------------------------------------------

// NewPieceBTree bulk-loads records bottom-up in O(n).
func NewPieceBTree(width int, recs []PieceRec) *PieceBTree {
	t := &PieceBTree{w: width, rec: 1 + 2*width}
	if len(recs) == 0 {
		t.root = &pnode{leaf: true}
		return t
	}
	var nodes []*pnode
	off := 0
	for _, sz := range groupSizes(len(recs)) {
		n := &pnode{leaf: true, data: make([]byte, sz*t.rec), count: sz}
		for k := 0; k < sz; k++ {
			r := recs[off+k]
			t.writeRec(n.data, k*t.rec, r.Buf, r.Start, r.Length)
			n.total += r.Length
		}
		nodes = append(nodes, n)
		off += sz
	}
	for len(nodes) > 1 {
		var next []*pnode
		o := 0
		for _, sz := range groupSizes(len(nodes)) {
			grp := nodes[o : o+sz]
			in := &pnode{
				children: append([]*pnode(nil), grp...),
				subtotal: make([]int, sz),
				subcount: make([]int, sz),
			}
			for k, c := range grp {
				in.subtotal[k] = c.total
				in.subcount[k] = c.count
				in.total += c.total
				in.count += c.count
			}
			next = append(next, in)
			o += sz
		}
		nodes = next
	}
	t.root = nodes[0]
	return t
}

// ---- queries -------------------------------------------------------------

func (t *PieceBTree) find(pos int) int {
	n := t.root
	idx := 0
	for !n.leaf {
		i := 0
		for i < len(n.children)-1 && pos >= n.subtotal[i] {
			pos -= n.subtotal[i]
			idx += n.subcount[i]
			i++
		}
		n = n.children[i]
	}
	cur := 0
	for j := 0; j < n.count; j++ {
		cur += t.recLen(n.data, j)
		if pos < cur {
			return idx + j
		}
	}
	if n.count == 0 {
		return idx
	}
	return idx + n.count - 1
}

// At maps a document byte position to (buffer, offset within that buffer) in a
// single O(log n) descent — no separate prefix() pass.
func (t *PieceBTree) At(pos int) (buf, bufOffset int) {
	n := t.root
	for !n.leaf {
		i := 0
		for i < len(n.children)-1 && pos >= n.subtotal[i] {
			pos -= n.subtotal[i]
			i++
		}
		n = n.children[i]
	}
	cur := 0
	for j := 0; j < n.count; j++ {
		b, s, l := t.recAt(n.data, j)
		if pos < cur+l {
			return b, s + (pos - cur)
		}
		cur += l
	}
	// clamp past end to the last record's final byte
	j := n.count - 1
	b, s, l := t.recAt(n.data, j)
	return b, s + l - 1
}

// Get returns the descriptor at logical index idx.
func (t *PieceBTree) Get(idx int) PieceRec {
	n := t.root
	for !n.leaf {
		i := 0
		for i < len(n.children)-1 && idx >= n.subcount[i] {
			idx -= n.subcount[i]
			i++
		}
		n = n.children[i]
	}
	b, s, l := t.recAt(n.data, idx)
	return PieceRec{Buf: b, Start: s, Length: l}
}

// prefix returns the sum of lengths of records [0, idx).
func (t *PieceBTree) prefix(idx int) int {
	n := t.root
	sum := 0
	for !n.leaf {
		i := 0
		for i < len(n.children)-1 && idx > n.subcount[i] {
			idx -= n.subcount[i]
			sum += n.subtotal[i]
			i++
		}
		n = n.children[i]
	}
	for j := 0; j < idx && j < n.count; j++ {
		sum += t.recLen(n.data, j)
	}
	return sum
}

// ---- length edit ---------------------------------------------------------

func (t *PieceBTree) addLen(idx, delta int) {
	n := t.root
	for {
		n.total += delta
		if n.leaf {
			o := idx * t.rec
			cur := getUintW(n.data[o+1+t.w:], t.w)
			putUintW(n.data[o+1+t.w:], t.w, cur+delta)
			return
		}
		i := 0
		for i < len(n.children)-1 && idx >= n.subcount[i] {
			idx -= n.subcount[i]
			i++
		}
		n.subtotal[i] += delta
		n = n.children[i]
	}
}

// ---- insert --------------------------------------------------------------

func (t *PieceBTree) InsertPiece(idx int, r PieceRec) {
	if right := t.insertAt(t.root, idx, r); right != nil {
		old := t.root
		t.root = &pnode{
			children: []*pnode{old, right},
			subtotal: []int{old.total, right.total},
			subcount: []int{old.count, right.count},
			total:    old.total + right.total,
			count:    old.count + right.count,
		}
	}
}

func (t *PieceBTree) insertAt(n *pnode, idx int, r PieceRec) *pnode {
	n.total += r.Length
	n.count++
	if n.leaf {
		o := idx * t.rec
		n.data = append(n.data, make([]byte, t.rec)...)
		copy(n.data[o+t.rec:], n.data[o:len(n.data)-t.rec])
		t.writeRec(n.data, o, r.Buf, r.Start, r.Length)
		if n.count > maxE {
			return t.splitLeaf(n)
		}
		return nil
	}
	i := 0
	for i < len(n.children)-1 && idx > n.subcount[i] {
		idx -= n.subcount[i]
		i++
	}
	child := n.children[i]
	right := t.insertAt(child, idx, r)
	n.subtotal[i] = child.total
	n.subcount[i] = child.count
	if right != nil {
		n.children = insertPnode(n.children, i+1, right)
		n.subtotal = insertInt(n.subtotal, i+1, right.total)
		n.subcount = insertInt(n.subcount, i+1, right.count)
		if len(n.children) > maxE {
			return t.splitInternal(n)
		}
	}
	return nil
}

func (t *PieceBTree) splitLeaf(n *pnode) *pnode {
	mid := n.count / 2
	r := &pnode{leaf: true, data: append([]byte(nil), n.data[mid*t.rec:]...)}
	n.data = n.data[:mid*t.rec]
	n.count = mid
	r.count = len(r.data) / t.rec
	n.total = t.sumLeaf(n.data)
	r.total = t.sumLeaf(r.data)
	return r
}

func (t *PieceBTree) splitInternal(n *pnode) *pnode {
	mid := len(n.children) / 2
	r := &pnode{
		children: append([]*pnode(nil), n.children[mid:]...),
		subtotal: append([]int(nil), n.subtotal[mid:]...),
		subcount: append([]int(nil), n.subcount[mid:]...),
	}
	n.children = n.children[:mid]
	n.subtotal = n.subtotal[:mid]
	n.subcount = n.subcount[:mid]
	n.total, n.count = sumInts(n.subtotal), sumInts(n.subcount)
	r.total, r.count = sumInts(r.subtotal), sumInts(r.subcount)
	return r
}

// ---- delete --------------------------------------------------------------

func (t *PieceBTree) DeletePiece(idx int) {
	t.deleteAt(t.root, idx)
	for !t.root.leaf && len(t.root.children) == 1 {
		t.root = t.root.children[0]
	}
}

func (t *PieceBTree) deleteAt(n *pnode, idx int) (removed int) {
	if n.leaf {
		o := idx * t.rec
		removed = t.recLen(n.data, idx)
		copy(n.data[o:], n.data[o+t.rec:])
		n.data = n.data[:len(n.data)-t.rec]
		n.total -= removed
		n.count--
		return
	}
	i := 0
	for i < len(n.children)-1 && idx >= n.subcount[i] {
		idx -= n.subcount[i]
		i++
	}
	removed = t.deleteAt(n.children[i], idx)
	n.total -= removed
	n.count--
	n.subtotal[i] = n.children[i].total
	n.subcount[i] = n.children[i].count
	if t.childSize(n, i) < minE {
		t.rebalance(n, i)
	}
	return
}

func (t *PieceBTree) childSize(n *pnode, i int) int {
	c := n.children[i]
	if c.leaf {
		return c.count
	}
	return len(c.children)
}

func (t *PieceBTree) refresh(n *pnode, i int) {
	n.subtotal[i] = n.children[i].total
	n.subcount[i] = n.children[i].count
}

func (t *PieceBTree) rebalance(n *pnode, i int) {
	if i > 0 && t.childSize(n, i-1) > minE {
		t.borrowFromLeft(n, i)
		return
	}
	if i < len(n.children)-1 && t.childSize(n, i+1) > minE {
		t.borrowFromRight(n, i)
		return
	}
	if i > 0 {
		t.merge(n, i-1)
	} else {
		t.merge(n, i)
	}
}

func (t *PieceBTree) borrowFromLeft(n *pnode, i int) {
	left, cur := n.children[i-1], n.children[i]
	if cur.leaf {
		rec, length := t.leafPopBack(left)
		t.leafPushFront(cur, rec, length)
	} else {
		cur.pushFrontChild(left.popBackChild())
	}
	t.refresh(n, i-1)
	t.refresh(n, i)
}

func (t *PieceBTree) borrowFromRight(n *pnode, i int) {
	right, cur := n.children[i+1], n.children[i]
	if cur.leaf {
		rec, length := t.leafPopFront(right)
		t.leafPushBack(cur, rec, length)
	} else {
		cur.pushBackChild(right.popFrontChild())
	}
	t.refresh(n, i)
	t.refresh(n, i+1)
}

func (t *PieceBTree) merge(n *pnode, j int) {
	a, b := n.children[j], n.children[j+1]
	if a.leaf {
		a.data = append(a.data, b.data...)
		a.count += b.count
		a.total += b.total
	} else {
		a.children = append(a.children, b.children...)
		a.subtotal = append(a.subtotal, b.subtotal...)
		a.subcount = append(a.subcount, b.subcount...)
		a.total += b.total
		a.count += b.count
	}
	n.children = removePnode(n.children, j+1)
	n.subtotal = removeInt(n.subtotal, j+1)
	n.subcount = removeInt(n.subcount, j+1)
	t.refresh(n, j)
}

// ---- leaf-local record moves (raw bytes) ---------------------------------

func (t *PieceBTree) leafPopBack(n *pnode) (rec []byte, length int) {
	o := (n.count - 1) * t.rec
	rec = append([]byte(nil), n.data[o:o+t.rec]...)
	length = getUintW(rec[1+t.w:], t.w)
	n.data = n.data[:o]
	n.count--
	n.total -= length
	return
}
func (t *PieceBTree) leafPopFront(n *pnode) (rec []byte, length int) {
	rec = append([]byte(nil), n.data[:t.rec]...)
	length = getUintW(rec[1+t.w:], t.w)
	copy(n.data, n.data[t.rec:])
	n.data = n.data[:len(n.data)-t.rec]
	n.count--
	n.total -= length
	return
}
func (t *PieceBTree) leafPushBack(n *pnode, rec []byte, length int) {
	n.data = append(n.data, rec...)
	n.count++
	n.total += length
}
func (t *PieceBTree) leafPushFront(n *pnode, rec []byte, length int) {
	n.data = append(n.data, make([]byte, t.rec)...)
	copy(n.data[t.rec:], n.data[:len(n.data)-t.rec])
	copy(n.data[:t.rec], rec)
	n.count++
	n.total += length
}

// ---- internal-node child moves (identical shape to btree.go) -------------

func (n *pnode) popBackChild() *pnode {
	k := len(n.children) - 1
	c := n.children[k]
	n.children = n.children[:k]
	n.subtotal = n.subtotal[:k]
	n.subcount = n.subcount[:k]
	n.total -= c.total
	n.count -= c.count
	return c
}
func (n *pnode) popFrontChild() *pnode {
	c := n.children[0]
	n.children = removePnode(n.children, 0)
	n.subtotal = removeInt(n.subtotal, 0)
	n.subcount = removeInt(n.subcount, 0)
	n.total -= c.total
	n.count -= c.count
	return c
}
func (n *pnode) pushBackChild(c *pnode) {
	n.children = append(n.children, c)
	n.subtotal = append(n.subtotal, c.total)
	n.subcount = append(n.subcount, c.count)
	n.total += c.total
	n.count += c.count
}
func (n *pnode) pushFrontChild(c *pnode) {
	n.children = insertPnode(n.children, 0, c)
	n.subtotal = insertInt(n.subtotal, 0, c.total)
	n.subcount = insertInt(n.subcount, 0, c.count)
	n.total += c.total
	n.count += c.count
}

func insertPnode(a []*pnode, i int, v *pnode) []*pnode {
	a = append(a, nil)
	copy(a[i+1:], a[i:])
	a[i] = v
	return a
}
func removePnode(a []*pnode, i int) []*pnode {
	copy(a[i:], a[i+1:])
	return a[:len(a)-1]
}

// Count is the number of pieces in the document.
func (t *PieceBTree) Count() int { return t.root.count }

// Total is the document length in bytes — the root's cached subtree sum, so
// it is O(1) rather than a walk.
func (t *PieceBTree) Total() int { return t.root.total }

// Each visits pieces from index `from` in document order, stopping when fn
// returns false.
//
// This exists because Get descends from the root every call. Walking a
// screenful with Get is O(k log n); one descent that then walks the leaves is
// O(log n + k), which is what the renderer needs once a long agent session has
// fragmented the document into hundreds of thousands of pieces.
func (t *PieceBTree) Each(from int, fn func(idx int, r PieceRec) bool) {
	if from < 0 {
		from = 0
	}
	var walk func(n *pnode, skip, base int) bool
	walk = func(n *pnode, skip, base int) bool {
		if n.leaf {
			for i := skip; i < n.count; i++ {
				b, s, l := t.recAt(n.data, i)
				if !fn(base+i, PieceRec{Buf: b, Start: s, Length: l}) {
					return false
				}
			}
			return true
		}
		for i, child := range n.children {
			// Skip whole subtrees that lie before the starting index, using the
			// cached counts rather than descending into them.
			if skip >= n.subcount[i] {
				skip -= n.subcount[i]
				base += n.subcount[i]
				continue
			}
			if !walk(child, skip, base) {
				return false
			}
			base += n.subcount[i]
			skip = 0
		}
		return true
	}
	walk(t.root, from, 0)
}
