package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"time"

	"raj/internal/editor"
	"raj/internal/piecetable"
)

// The manifest is a derived view over the journal the session already keeps.
//
// Nothing here is stored: every row is recomputed from the ops, groups,
// authors and decisions the piecetable holds, so the manifest cannot drift
// from the document it describes and two calls over an unchanged journal are
// equal. The digest is the same SHA-256 the op log records for a buffer's
// bytes (File.EncodeText then sha256), not a git object id, so the manifest
// needs no repository.
//
// One field of the spec's revision record the journal cannot supply: it
// carries no timestamp, so Revision.AppliedAt is left zero for the caller to
// stamp when it persists the row, and no placeholder is invented. The task is
// no longer one of them: the journal records the task each change set was
// opened under, so the manifest keys its rows by that rather than by a
// caller's argument.
//
// The decisions are the ones the journal already distinguishes: a set whose
// live members were never marked (accepted), a set still awaiting a decision
// (proposed), a rejected set whose text stays in the document, a still-Proposed
// set no hunk of which survives (invalid), and a set every live member of which
// has been reversed out (cleared). A cleared set is deliberately still a row:
// the journal holds its ops, so dropping it would make the audit silent about
// work that was discarded.

// ManifestDecision names where a change set stands in the journal's record.
type ManifestDecision string

const (
	// DecisionAccepted is a set nobody marked proposed or rejected: the
	// ordinary case, and what a save writes.
	DecisionAccepted ManifestDecision = "accepted"
	// DecisionProposed is a set awaiting the user's decision. Its text is in
	// the view but not in the agreed composition.
	DecisionProposed ManifestDecision = "proposed"
	// DecisionRejected is a set the user rejected. Rejecting is a decision,
	// not an edit, so the text stays in the document.
	DecisionRejected ManifestDecision = "rejected"
	// DecisionInvalid is a still-Proposed set every live member of which a
	// later edit has moved past, so no hunk survives to accept. It is derived,
	// not stored: clear the colliding edit and it clears with it.
	DecisionInvalid ManifestDecision = "invalid"
	// DecisionCleared is a set whose live members have all been reversed out
	// of the document. The journal records the reversal but not the gesture,
	// so a cleared rejected set and a set the user undid read the same here.
	DecisionCleared ManifestDecision = "cleared"
)

// ManifestRow is one change set's audit line: what it touched, the text on
// either side of it, and how it stands.
//
// BlobBefore is the text at the version just before the set's first op and
// BlobAfter the text at the version just after its last, both in the session's
// one timeline -- every op, whatever its state. The values carry the same
// algorithm-prefixed form the op log uses ("sha256:<hex>").
type ManifestRow struct {
	Group      uint64           `json:"group"`
	Path       string           `json:"path"`
	BlobBefore string           `json:"blob_before"`
	BlobAfter  string           `json:"blob_after"`
	Decision   ManifestDecision `json:"decision"`
}

// Revision is the spec's per-applied-version record. Version is the session
// version the set's last op produced, BlobSHA the digest of the text at that
// version, Algorithm names the digest, and Task is the task the set's group
// was opened under. AppliedAt is the zero time: the journal records no
// timestamps.
type Revision struct {
	Path      string             `json:"path"`
	Version   piecetable.Version `json:"version"`
	BlobSHA   string             `json:"blob_sha"`
	Algorithm string             `json:"algorithm"`
	Task      string             `json:"task"`
	AppliedAt time.Time          `json:"applied_at"`
}

// Manifest is the derived audit view, filed under the task that produced each
// row and carrying the path each row changed. The task comes from the change
// set's own record in the journal, so a manifest groups rows by it and lets a
// caller ask per task, per path, or for one row without naming a task.
type Manifest struct {
	byTask map[string][]ManifestRow
	tasks  []string // tasks in first-added order, for a stable All
}

// BuildManifest derives one buffer's manifest, filing each row under the task
// its own change set was opened under. f's Path is the path every row carries.
func BuildManifest(f *editor.File) (Manifest, error) {
	var m Manifest
	if err := m.Add(f); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

// Add derives f's rows and files each under its own change set's task: a
// buffer may hold sets from several tasks, and the group is the record of
// which. A buffer may be added more than once; the rows are recomputed each
// time and never cached.
func (m *Manifest) Add(f *editor.File) error {
	if f == nil {
		return nil
	}
	groups := f.Session().Groups()
	before, after, err := journalBlobs(f, groups)
	if err != nil {
		return err
	}
	if m.byTask == nil {
		m.byTask = map[string][]ManifestRow{}
	}
	for i, g := range groups {
		task := g.Task
		if _, seen := m.byTask[task]; !seen {
			m.tasks = append(m.tasks, task)
		}
		m.byTask[task] = append(m.byTask[task], ManifestRow{
			Group:      g.ID,
			Path:       f.Path,
			BlobBefore: before[i],
			BlobAfter:  after[i],
			Decision:   manifestDecision(g),
		})
	}
	return nil
}

// ForTask returns a copy of the rows filed under task, in journal order.
func (m Manifest) ForTask(task string) []ManifestRow {
	return append([]ManifestRow(nil), m.byTask[task]...)
}

// ForPath returns the rows for one path across every task, task-major then
// journal order, so an unchanged manifest reads the same every time.
func (m Manifest) ForPath(path string) []ManifestRow {
	var out []ManifestRow
	for _, task := range m.tasks {
		for _, r := range m.byTask[task] {
			if r.Path == path {
				out = append(out, r)
			}
		}
	}
	return out
}

// For returns the rows for one task and path together.
func (m Manifest) For(task, path string) []ManifestRow {
	var out []ManifestRow
	for _, r := range m.byTask[task] {
		if r.Path == path {
			out = append(out, r)
		}
	}
	return out
}

// All returns every row, task-major then journal order.
func (m Manifest) All() []ManifestRow {
	var out []ManifestRow
	for _, task := range m.tasks {
		out = append(out, m.byTask[task]...)
	}
	return out
}

// Tasks lists the task keys in the order they were first added.
func (m Manifest) Tasks() []string {
	return append([]string(nil), m.tasks...)
}

// Empty reports whether the manifest holds no row.
func (m Manifest) Empty() bool {
	for _, task := range m.tasks {
		if len(m.byTask[task]) > 0 {
			return false
		}
	}
	return true
}

// BuildRevisions derives one revision per accepted change set, oldest first.
//
// A revision is the version a set's last op produced and the digest of the
// text there, so a replay to that version can be checked against BlobSHA. A
// set not in the agreed composition -- proposed, rejected, invalid or cleared
// -- is left to the manifest, which carries the decision the revision shape
// deliberately does not.
func BuildRevisions(f *editor.File) ([]Revision, error) {
	if f == nil {
		return nil, nil
	}
	groups := f.Session().Groups()
	_, after, err := journalBlobs(f, groups)
	if err != nil {
		return nil, err
	}
	var out []Revision
	for i, g := range groups {
		if g.State != piecetable.Accepted || g.Ops == 0 {
			continue
		}
		out = append(out, Revision{
			Path:      f.Path,
			Version:   g.Last + 1,
			BlobSHA:   after[i],
			Algorithm: "sha256",
			Task:      g.Task,
		})
	}
	return out, nil
}

// manifestDecision reads one set's standing off the journal. Precedence
// matters: a rejection is a recorded decision even when its text has since
// been undone, and an invalid set is a Proposed set whose text is gone from
// the compositions, so Invalid must be asked before the surviving-member test.
func manifestDecision(g piecetable.Group) ManifestDecision {
	switch {
	case g.State == piecetable.Rejected:
		return DecisionRejected
	case g.State == piecetable.Proposed && g.Invalid:
		return DecisionInvalid
	case g.Ops == 0:
		return DecisionCleared
	case g.State == piecetable.Proposed:
		return DecisionProposed
	default:
		return DecisionAccepted
	}
}

// journalBlobs replays the session's journal once and returns, for each group,
// the digest of the text just before its first op and just after its last.
//
// It starts from the base bytes the Original store holds and folds each op in
// turn, exactly as Session does, so the text it measures is the session's view
// frame: every op counts, whatever its state. The walk is sequential and the
// digest is taken only at a boundary some group needs, so a version that is
// one set's end and the next set's start is hashed once.
func journalBlobs(f *editor.File, groups []piecetable.Group) (before, after []string, err error) {
	sess := f.Session()
	store := sess.Store()
	before = make([]string, len(groups))
	after = make([]string, len(groups))

	wantBefore := map[piecetable.Version][]int{}
	wantAfter := map[piecetable.Version][]int{}
	for i, g := range groups {
		wantBefore[g.First] = append(wantBefore[g.First], i)
		wantAfter[g.Last+1] = append(wantAfter[g.Last+1], i)
	}

	origLen, _ := sess.LengthAt(0)
	text := append([]byte(nil), store.Slice(piecetable.Original, 0, origLen)...)

	record := func(v piecetable.Version) error {
		if len(wantBefore[v])+len(wantAfter[v]) == 0 {
			return nil
		}
		digest, derr := manifestDigest(f, text)
		if derr != nil {
			return derr
		}
		for _, i := range wantBefore[v] {
			before[i] = digest
		}
		for _, i := range wantAfter[v] {
			after[i] = digest
		}
		return nil
	}

	if err := record(0); err != nil {
		return nil, nil, err
	}
	for i, o := range sess.Journal() {
		text = applyOpText(store, text, o)
		if err := record(piecetable.Version(i + 1)); err != nil {
			return nil, nil, err
		}
	}
	return before, after, nil
}

// applyOpText folds one op into the reconstructed text. The op's Pos is in the
// coordinates its predecessors produced, so a sequential replay is exact; the
// bounds are clamped only so a malformed journal yields a defined digest
// rather than a panic.
func applyOpText(store *piecetable.Store, text []byte, o piecetable.Op) []byte {
	pos := o.Pos
	if pos < 0 {
		pos = 0
	}
	if pos > len(text) {
		pos = len(text)
	}
	del := o.DelLen()
	if del > len(text)-pos {
		del = len(text) - pos
	}
	ins := recsBytes(store, o.Ins)
	out := make([]byte, 0, len(text)-del+len(ins))
	out = append(out, text[:pos]...)
	out = append(out, ins...)
	out = append(out, text[pos+del:]...)
	return out
}

// recsBytes reads the bytes a piece list spans out of the append-only stores.
// Nothing is ever erased, so the pieces an op recorded still read back exactly
// however many later edits moved them.
func recsBytes(store *piecetable.Store, recs []piecetable.PieceRec) []byte {
	var out []byte
	for _, r := range recs {
		out = append(out, store.Slice(piecetable.Author(r.Buf), r.Start, r.Length)...)
	}
	return out
}

// manifestDigest is the digest the op log records for a buffer's bytes: the
// text encoded the way this file would write it, then SHA-256, in the log's
// algorithm-prefixed form. Using EncodeText rather than the in-memory string
// keeps the manifest agreeing with a Base or Written record on a file whose
// shape is not plain UTF-8.
func manifestDigest(f *editor.File, text []byte) (string, error) {
	enc, err := f.EncodeText(string(text))
	if err != nil {
		return "", err
	}
	return hashBytes(enc), nil
}

// hashBytes is the digest a Base or Written record carries: SHA-256 in the
// log's algorithm-prefixed form.
func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}
