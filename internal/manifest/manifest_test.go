package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"testing"

	"raj/internal/editor"
	"raj/internal/piecetable"
)

// sessionGroup returns the piecetable listing of the change set with id, or
// false when the journal holds none. It is the session-layer sibling of
// findGroup, which addresses the control.Group the socket returns.
func sessionGroup(t *testing.T, s *piecetable.Session, id uint64) (piecetable.Group, bool) {
	t.Helper()
	for _, g := range s.Groups() {
		if g.ID == id {
			return g, true
		}
	}
	return piecetable.Group{}, false
}

// manifestBlob is the digest a row is expected to carry, computed here from
// first principles rather than through the code under test.
func manifestBlob(text string) string {
	sum := sha256.Sum256([]byte(text))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// manifestRow returns the one row for a change set, so a test reads the
// manifest the way a caller does rather than by index.
func manifestRow(t *testing.T, m Manifest, group uint64) ManifestRow {
	t.Helper()
	for _, r := range m.All() {
		if r.Group == group {
			return r
		}
	}
	t.Fatalf("manifest has no row for change set %d", group)
	return ManifestRow{}
}

// A fixture with one accepted set, one rejected set and one cleared set: the
// states the journal distinguishes that all keep a row. The preconditions
// assert the journal really holds the states the expected rows name.
func TestManifestDecisionsFollowTheJournal(t *testing.T) {
	t.Parallel()
	f := editor.NewFile("a.go", "one\ntwo\nthree\n", 0)
	sess := f.Session()

	f.ApplyDiff(piecetable.Agent, sess.Version(), []piecetable.Hunk{{Start: 0, End: 3, Text: "ONE"}})
	applied := sess.LastGroup()

	f.ApplyDiff(piecetable.Agent, sess.Version(), []piecetable.Hunk{{Start: 8, End: 13, Text: "THREE"}})
	rejected := sess.LastGroup()
	f.ProposeGroup(rejected)
	if !f.RejectGroup(rejected) {
		t.Fatalf("reject of change set %d failed", rejected)
	}

	f.ApplyDiff(piecetable.Agent, sess.Version(), []piecetable.Hunk{{Start: 4, End: 7, Text: "TWO"}})
	cleared := sess.LastGroup()
	f.ProposeGroup(cleared)
	if !f.RejectGroup(cleared) {
		t.Fatalf("reject of change set %d failed", cleared)
	}
	if !f.ClearRejected(cleared) {
		t.Fatalf("clear of change set %d failed", cleared)
	}

	// Each set was opened under the same task, which the manifest reads off
	// the group rather than a caller's argument.
	for _, id := range []uint64{applied, rejected, cleared} {
		sess.SetGroupTask(id, "task-1")
	}

	if g, ok := sessionGroup(t, sess, applied); !ok || g.State != piecetable.Accepted || g.Ops == 0 {
		t.Fatalf("applied set = %+v, want a live accepted set", g)
	}
	if g, ok := sessionGroup(t, sess, rejected); !ok || g.State != piecetable.Rejected || g.Ops == 0 {
		t.Fatalf("rejected set = %+v, want a live rejected set", g)
	}
	if g, ok := sessionGroup(t, sess, cleared); !ok || g.Ops != 0 {
		t.Fatalf("cleared set = %+v, want every member reversed", g)
	}
	if got := f.Text(); got != "ONE\ntwo\nTHREE\n" {
		t.Fatalf("text = %q, want the cleared set reversed back out", got)
	}

	m, err := BuildManifest(f)
	if err != nil {
		t.Fatalf("BuildManifest: %v", err)
	}
	rows := m.ForTask("task-1")
	if len(rows) != 3 {
		t.Fatalf("rows = %+v, want one per change set", rows)
	}
	want := []struct {
		group    uint64
		decision ManifestDecision
		before   string
		after    string
	}{
		{applied, DecisionAccepted, "one\ntwo\nthree\n", "ONE\ntwo\nthree\n"},
		{rejected, DecisionRejected, "ONE\ntwo\nthree\n", "ONE\ntwo\nTHREE\n"},
		{cleared, DecisionCleared, "ONE\ntwo\nTHREE\n", "ONE\nTWO\nTHREE\n"},
	}
	for _, w := range want {
		got := manifestRow(t, m, w.group)
		if got.Decision != w.decision {
			t.Errorf("group %d decision = %q, want %q", w.group, got.Decision, w.decision)
		}
		if got.BlobBefore != manifestBlob(w.before) {
			t.Errorf("group %d blob_before = %q, want the digest of %q", w.group, got.BlobBefore, w.before)
		}
		if got.BlobAfter != manifestBlob(w.after) {
			t.Errorf("group %d blob_after = %q, want the digest of %q", w.group, got.BlobAfter, w.after)
		}
		if got.Path != "a.go" {
			t.Errorf("group %d path = %q, want a.go", w.group, got.Path)
		}
	}
}

// An invalid set -- still Proposed with no surviving hunk, because a later
// edit consumed its inserted run -- is a decision the manifest must name
// rather than drop.
func TestManifestRepresentsAnInvalidSet(t *testing.T) {
	t.Parallel()
	f := editor.NewFile("a.go", "hello world\n", 0)
	sess := f.Session()

	f.ApplyDiff(piecetable.Agent, sess.Version(), []piecetable.Hunk{{Start: 6, End: 11, Text: "socket"}})
	superseded := sess.LastGroup()
	f.ProposeGroup(superseded)
	sess.SetGroupTask(superseded, "task-1")
	f.ApplyDiff(piecetable.User, sess.Version(), []piecetable.Hunk{{Start: 6, End: 12, Text: "port"}})

	g, ok := sessionGroup(t, sess, superseded)
	if !ok || !g.Invalid || g.InvalidBy == nil {
		t.Fatalf("precondition: superseded set = %+v, want invalid with a named collider", g)
	}

	m, err := BuildManifest(f)
	if err != nil {
		t.Fatalf("BuildManifest: %v", err)
	}
	if got := manifestRow(t, m, superseded).Decision; got != DecisionInvalid {
		t.Errorf("invalid set decision = %q, want %q", got, DecisionInvalid)
	}
}

// The manifest is a projection of the journal, so two calls over an unchanged
// journal are equal -- no cache and no second document to drift.
func TestManifestIsReproducible(t *testing.T) {
	t.Parallel()
	f := editor.NewFile("a.go", "hello world\n", 0)
	f.ApplyDiff(piecetable.Agent, f.Session().Version(), []piecetable.Hunk{{Start: 6, End: 11, Text: "socket"}})
	id := f.Session().LastGroup()
	f.ProposeGroup(id)
	f.RejectGroup(id)

	first, err := BuildManifest(f)
	if err != nil {
		t.Fatalf("first BuildManifest: %v", err)
	}
	second, err := BuildManifest(f)
	if err != nil {
		t.Fatalf("second BuildManifest: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Errorf("two manifests over one journal differ:\n%+v\n%+v", first.All(), second.All())
	}
}

// Rows are filed under the task each set was opened under and carry the path
// they changed, so a caller can ask per task, per path, or for one pair.
func TestManifestIsKeyedByTaskAndPath(t *testing.T) {
	t.Parallel()
	a := editor.NewFile("a.go", "aaa\n", 0)
	b := editor.NewFile("b.go", "bbb\n", 0)
	a.ApplyDiff(piecetable.Agent, a.Session().Version(), []piecetable.Hunk{{Start: 0, End: 3, Text: "AAA"}})
	b.ApplyDiff(piecetable.Agent, b.Session().Version(), []piecetable.Hunk{{Start: 0, End: 3, Text: "BBB"}})
	a.Session().SetGroupTask(a.Session().LastGroup(), "t1")
	b.Session().SetGroupTask(b.Session().LastGroup(), "t2")

	var m Manifest
	if err := m.Add(a); err != nil {
		t.Fatalf("Add a: %v", err)
	}
	if err := m.Add(b); err != nil {
		t.Fatalf("Add b: %v", err)
	}
	if got := len(m.ForTask("t1")); got != 1 {
		t.Errorf("ForTask(t1) = %d rows, want 1", got)
	}
	if got := len(m.ForTask("t2")); got != 1 {
		t.Errorf("ForTask(t2) = %d rows, want 1", got)
	}
	if got := len(m.ForPath("a.go")); got != 1 {
		t.Errorf("ForPath(a.go) = %d rows, want 1", got)
	}
	if got := len(m.For("t1", "b.go")); got != 0 {
		t.Errorf("For(t1, b.go) = %d rows, want none", got)
	}
	if got := m.All(); len(got) != 2 {
		t.Errorf("All() = %d rows, want 2", len(got))
	}
	if m.Empty() {
		t.Error("Empty() = true with rows present")
	}
}

// Revisions carry the applied version, the digest of the text there, and the
// task their group was opened under; the one field the journal cannot supply,
// the timestamp, is left zero rather than invented.
func TestRevisionsRecordTheAppliedVersion(t *testing.T) {
	t.Parallel()
	f := editor.NewFile("a.go", "hello world\n", 0)
	f.ApplyDiff(piecetable.Agent, f.Session().Version(), []piecetable.Hunk{{Start: 6, End: 11, Text: "socket"}})
	id := f.Session().LastGroup()
	f.Session().SetGroupTask(id, "task-1")

	revs, err := BuildRevisions(f)
	if err != nil {
		t.Fatalf("BuildRevisions: %v", err)
	}
	if len(revs) != 1 {
		t.Fatalf("revisions = %+v, want one per accepted set", revs)
	}
	g, _ := sessionGroup(t, f.Session(), id)
	r := revs[0]
	if r.Path != "a.go" || r.Version != g.Last+1 {
		t.Errorf("revision = %+v, want path a.go at version %d", r, g.Last+1)
	}
	if r.BlobSHA != manifestBlob("hello socket\n") {
		t.Errorf("blob sha = %q, want the digest of the applied text", r.BlobSHA)
	}
	if r.Algorithm != "sha256" || r.Task != "task-1" {
		t.Errorf("revision = %+v, want algorithm sha256 and task-1", r)
	}
	if !r.AppliedAt.IsZero() {
		t.Errorf("applied_at = %v, want zero: the journal records no timestamp", r.AppliedAt)
	}

	// A set that is not in the agreed composition is the manifest's row, not
	// a revision.
	f.ProposeGroup(id)
	f.RejectGroup(id)
	revs, err = BuildRevisions(f)
	if err != nil {
		t.Fatalf("BuildRevisions after reject: %v", err)
	}
	if len(revs) != 0 {
		t.Errorf("revisions after reject = %+v, want none", revs)
	}
}
