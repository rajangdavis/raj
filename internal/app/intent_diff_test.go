package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"raj/internal/control"
	"raj/internal/git"
	"raj/internal/intent"
	"raj/internal/piecetable"
)

// intentDiffFixture commits a.go and b.go, proposes one member group on each,
// and stores an intention over the two. It is the slice the diff verbs are
// about: the workspace's buffers hold the members, and the caller dirties the
// worktree separately to prove the diff shows the members and not the disk.
func intentDiffFixture(t *testing.T) (string, *harness) {
	t.Helper()
	dir, h := intentRoot(t)
	gA := proposeOn(t, h, "a.go", piecetable.Hunk{Start: 0, End: len("package a\n"), Text: "package a2\n"})
	gB := proposeOn(t, h, "b.go", piecetable.Hunk{Start: 0, End: len("package b\n"), Text: "package b2\n"})
	doIntent(t, h, intent.Command{Mode: "new", Name: "i", Base: "HEAD", Groups: []uint64{gA, gB}})
	return dir, h
}

// TestIntentDiffShowsMemberSliceNotWorkspace is the key property: an intention
// whose members touch two files is diffed against its base, while the working
// tree also holds an unrelated edit and an untracked file. The member files
// appear; nothing unrelated does, and the b.go hunk is the buffer's composition
// rather than the bytes on disk.
func TestIntentDiffShowsMemberSliceNotWorkspace(t *testing.T) {
	dir, h := intentDiffFixture(t)
	ctx := context.Background()
	base, err := git.New(dir).RevParse(ctx, "HEAD")
	if err != nil {
		t.Fatal(err)
	}

	// Unrelated worktree state: a tracked file edited on disk and an untracked
	// file. Neither is in the intention, so neither may reach the diff.
	if err := os.WriteFile(filepath.Join(dir, "b.go"), []byte("package b\n// on disk, unrelated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "unrelated.txt"), []byte("unrelated\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res := doIntent(t, h, intent.Command{Mode: "diff", Name: "i"})
	if res.Diff == nil {
		t.Fatal("intent diff returned nothing")
	}
	paths := map[string]bool{}
	for _, e := range res.Diff.Stat.Entries {
		paths[e.Path] = true
	}
	if len(paths) != 2 || !paths["a.go"] || !paths["b.go"] {
		t.Fatalf("stat paths = %v, want exactly a.go and b.go", paths)
	}
	if res.Diff.Stat.Files != 2 {
		t.Errorf("stat.Files = %d, want 2", res.Diff.Stat.Files)
	}
	if res.Diff.Name != "i" || res.Diff.Base != base {
		t.Errorf("diff names %q over %s, want i over the pinned base %s", res.Diff.Name, res.Diff.Base, base)
	}
	if strings.Contains(res.Diff.Diff, "unrelated") || strings.Contains(res.Diff.Diff, "on disk") {
		t.Errorf("diff shows a workspace change that is not a member:\n%s", res.Diff.Diff)
	}
	for _, want := range []string{"-package a", "+package a2", "-package b", "+package b2"} {
		if !strings.Contains(res.Diff.Diff, want) {
			t.Errorf("diff is missing %q:\n%s", want, res.Diff.Diff)
		}
	}
}

// TestIntentDiffLeavesRepositoryUntouched pins the read-only promise: after the
// call, HEAD, the index and every worktree file are byte-identical. The index
// is warmed with one status first, because a status refresh may rewrite it and
// that is git's doing, not the verb's.
func TestIntentDiffLeavesRepositoryUntouched(t *testing.T) {
	dir, h := intentDiffFixture(t)
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(dir, "b.go"), []byte("package b\n// workspace edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "unrelated.txt"), []byte("unrelated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := git.New(dir).StatusDigest(ctx); err != nil {
		t.Fatal(err)
	}
	names := []string{"a.go", "b.go", "unrelated.txt"}
	before := repoDigest(t, dir, names...)
	doIntent(t, h, intent.Command{Mode: "diff", Name: "i"})
	after := repoDigest(t, dir, names...)
	if before != after {
		t.Errorf("the repository moved: %s -> %s", before, after)
	}
}

// repoDigest hashes HEAD, the index and the named worktree files. It reads the
// bytes itself and runs no git beyond rev-parse, so nothing in the measurement
// can move the thing being measured.
func repoDigest(t *testing.T, dir string, names ...string) string {
	t.Helper()
	h := sha256.New()
	head, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	h.Write([]byte("HEAD="))
	h.Write(head)
	for _, name := range append([]string{".git/HEAD", ".git/index"}, names...) {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		h.Write([]byte(name))
		h.Write([]byte{0})
		h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// TestIntentDiffRemovesScratchDir points TMPDIR at a fresh directory and proves
// the materialised checkout is gone once the verb returns.
func TestIntentDiffRemovesScratchDir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	_, h := intentDiffFixture(t)
	doIntent(t, h, intent.Command{Mode: "diff", Name: "i"})
	left, err := filepath.Glob(filepath.Join(tmp, "raj-seam-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Errorf("scratch directories left behind: %v", left)
	}
}

// TestIntentDiffUnknownIntentionRefused: a typo must be refused by name rather
// than read as an empty diff of nothing.
func TestIntentDiffUnknownIntentionRefused(t *testing.T) {
	_, h := intentDiffFixture(t)
	_, err := h.App.runIntent(context.Background(), intent.Command{Mode: "diff", Name: "missing"})
	if err == nil {
		t.Fatal("diffing an unknown intention must be refused")
	}
	if !strings.Contains(err.Error(), "missing") {
		t.Errorf("refusal %q must name the intention", err)
	}
}

// TestIntentDiffEmptyIntentionRefused pins the decision: an intention with no
// members is refused, not shown as an empty diff, because nothing selected and
// nothing changed would otherwise read the same.
func TestIntentDiffEmptyIntentionRefused(t *testing.T) {
	_, h := intentDiffFixture(t)
	doIntent(t, h, intent.Command{Mode: "new", Name: "empty", Base: "HEAD"})
	_, err := h.App.runIntent(context.Background(), intent.Command{Mode: "diff", Name: "empty"})
	if err == nil {
		t.Fatal("an intention with no members must be refused, not shown as an empty diff")
	}
	if !strings.Contains(err.Error(), "empty") || !strings.Contains(err.Error(), "member") {
		t.Errorf("refusal %q must name the intention and its empty membership", err)
	}
}

// TestIntentDiffJSONShape pins the one object --json carries: a diff object
// with the name, base, stat and patch.
func TestIntentDiffJSONShape(t *testing.T) {
	_, h := intentDiffFixture(t)
	res := doIntent(t, h, intent.Command{Mode: "diff", Name: "i"})
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	diffRaw, ok := doc["diff"]
	if !ok {
		t.Fatalf("json = %s, want a top-level diff object", raw)
	}
	var d struct {
		Name string `json:"name"`
		Base string `json:"base"`
		Stat struct {
			Files     int `json:"files"`
			Additions int `json:"additions"`
			Deletions int `json:"deletions"`
			Entries   []struct {
				Path      string `json:"path"`
				Additions int    `json:"additions"`
				Deletions int    `json:"deletions"`
			} `json:"entries"`
		} `json:"stat"`
		Diff string `json:"diff"`
	}
	if err := json.Unmarshal(diffRaw, &d); err != nil {
		t.Fatal(err)
	}
	if d.Name != "i" || d.Base == "" || d.Diff == "" {
		t.Fatalf("diff json = %+v, want a name, a base and the patch", d)
	}
	if d.Stat.Files != 2 || d.Stat.Additions != 2 || d.Stat.Deletions != 2 || len(d.Stat.Entries) != 2 {
		t.Fatalf("stat json = %+v, want two files at 2/2", d.Stat)
	}
}

// TestCapIntentDiffCutsOnALineAndSaysSo is the truncation rule as a unit: the
// kept patch ends at a whole line, the omitted count is exact, and Truncated
// makes the cut visible.
func TestCapIntentDiffCutsOnALineAndSaysSo(t *testing.T) {
	d := &intent.Diff{Diff: "line one\nline two\nline three\n"}
	capIntentDiff(d, len("line one\nline two\n")+3) // inside "line three"
	if !d.Truncated {
		t.Fatal("an over-limit diff must be marked truncated")
	}
	if d.Diff != "line one\nline two\n" {
		t.Errorf("kept %q, want whole lines up to the cut", d.Diff)
	}
	if d.OmittedBytes != len("line three\n") {
		t.Errorf("omitted %d bytes, want %d", d.OmittedBytes, len("line three\n"))
	}
	small := &intent.Diff{Diff: "one\n"}
	capIntentDiff(small, 1024)
	if small.Truncated || small.Diff != "one\n" {
		t.Errorf("a small diff changed: %+v", small)
	}
}

// TestIntentDiffExcludesOtherAcceptedSeam is the two-seam case the one-member
// fixture cannot see: two seams are accepted in the SAME file, so the buffer's
// agreed text holds both, and A is materialised and diffed. Its slice is the
// base plus A's members only; B's accepted hunk must be absent. The composed
// text is asserted directly because that is what the seam ships and what both
// `materialise` and `diff` derive from; the diff's patch is checked beside it
// so a leak in either verb is caught.
func TestIntentDiffExcludesOtherAcceptedSeam(t *testing.T) {
	dir, h := intentRoot(t)
	ctx := context.Background()
	svc := git.New(dir)

	sess := paneAt(t, h, "a.go").File.Session()
	gA := proposeOn(t, h, "a.go", piecetable.Hunk{Start: 0, End: len("package a\n"), Text: "package aA\n"})
	gB := proposeOn(t, h, "a.go", piecetable.Hunk{Start: len("package aA\n"), End: len("package aA\n"), Text: "// seam B\n"})
	sess.AcceptGroup(gA)
	sess.AcceptGroup(gB)
	doIntent(t, h, intent.Command{Mode: "new", Name: "A", Base: "HEAD", Groups: []uint64{gA}})
	doIntent(t, h, intent.Command{Mode: "new", Name: "B", Base: "HEAD", Groups: []uint64{gB}})

	// The agreed text really does hold both seams; otherwise the fixture would
	// not exercise the defect.
	if agreed := sess.Project(piecetable.AcceptedOnly).Text(); !strings.Contains(agreed, "seam B") || !strings.Contains(agreed, "package aA") {
		t.Fatalf("fixture: agreed text %q must hold both accepted seams", agreed)
	}

	// A's slice: the base plus A's member, and not B's accepted hunk.
	mA := doIntent(t, h, intent.Command{Mode: "materialise", Name: "A"})
	gotA, err := svc.Show(ctx, mA.Tree, "a.go")
	if err != nil {
		t.Fatal(err)
	}
	if gotA != "package aA\n" {
		t.Errorf("A's materialised a.go = %q, want only A's slice", gotA)
	}
	if strings.Contains(gotA, "seam B") {
		t.Errorf("A's materialised a.go = %q holds seam B's accepted change", gotA)
	}

	dA := doIntent(t, h, intent.Command{Mode: "diff", Name: "A"})
	if dA.Diff == nil {
		t.Fatal("intent diff returned nothing")
	}
	if strings.Contains(dA.Diff.Diff, "seam B") {
		t.Errorf("A's diff shows seam B's accepted change:\n%s", dA.Diff.Diff)
	}
	if !strings.Contains(dA.Diff.Diff, "+package aA") {
		t.Errorf("A's diff is missing its own hunk:\n%s", dA.Diff.Diff)
	}

	// Symmetry: B's slice holds B's change and not A's.
	mB := doIntent(t, h, intent.Command{Mode: "materialise", Name: "B"})
	gotB, err := svc.Show(ctx, mB.Tree, "a.go")
	if err != nil {
		t.Fatal(err)
	}
	if gotB != "package a\n// seam B\n" {
		t.Errorf("B's materialised a.go = %q, want only B's slice", gotB)
	}
}

// TestIntentProveExcludesOtherAcceptedSeam is the proof half of the two-seam
// pin: two seams are accepted in the same file, so the buffer's agreed text
// carries both, and A's `intent prove` must materialise A's slice alone before
// the check hook runs over it. The hook is the probe -- it passes only when the
// tree holds A's hunk and none of B's -- so a leak turns A's proof into a
// failure. Its twin, TestIntentDiffExcludesOtherAcceptedSeam, pins the same
// slice through `intent diff`.
func TestIntentProveExcludesOtherAcceptedSeam(t *testing.T) {
	_, h := intentRoot(t)

	sess := paneAt(t, h, "a.go").File.Session()
	gA := proposeOn(t, h, "a.go", piecetable.Hunk{Start: 0, End: len("package a\n"), Text: "package aA\n"})
	gB := proposeOn(t, h, "a.go", piecetable.Hunk{Start: len("package aA\n"), End: len("package aA\n"), Text: "// seam B\n"})
	sess.AcceptGroup(gA)
	sess.AcceptGroup(gB)
	doIntent(t, h, intent.Command{Mode: "new", Name: "A", Base: "HEAD", Groups: []uint64{gA}})
	doIntent(t, h, intent.Command{Mode: "new", Name: "B", Base: "HEAD", Groups: []uint64{gB}})

	// The agreed text really does hold both seams; otherwise the fixture would
	// not exercise the defect.
	if agreed := sess.Project(piecetable.AcceptedOnly).Text(); !strings.Contains(agreed, "package aA") || !strings.Contains(agreed, "seam B") {
		t.Fatalf("fixture: agreed text %q must hold both accepted seams", agreed)
	}

	// The check hook probes A's materialised tree: it must hold A's hunk and
	// none of B's.
	script := `grep -q 'package aA' a.go || { echo 'A proof is missing A hunk'; exit 1; }; ` +
		`grep -q 'seam B' a.go && { echo 'seam B leaked into A proof'; exit 1; }; exit 0`
	action, err := json.Marshal([]string{"sh", "-c", script})
	if err != nil {
		t.Fatal(err)
	}
	if err := hostOf(h.App).PutHook(control.HookRow{Name: "check", Action: string(action), Trigger: "agent", Enabled: true}); err != nil {
		t.Fatal(err)
	}

	res := doIntent(t, h, intent.Command{Mode: "prove", Name: "A"})
	if len(res.Proofs) != 1 {
		t.Fatalf("proofs = %+v, want exactly one", res.Proofs)
	}
	if p := res.Proofs[0]; p.Check != "pass" {
		t.Errorf("A's proof = %+v, want pass; a fail means B's accepted change reached A's tree", p)
	}
}
