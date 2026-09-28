package control

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"raj/internal/git"
)

// TestGuardProjectionAndMaterialise drives the seam this change adds: the
// Guard.Projection snapshot and Guard.Materialise, which lays that snapshot
// over a real worktree and stamps the git provenance it was built from.
//
// Precondition: controlGitRepo(t) makes a committed repository whose only
// tracked file a.go holds "package a\n", and no new.go exists. The fake host
// projection overrides a.go with new bytes and adds new.go. Without the change
// there is neither Guard.Projection nor Guard.Materialise, so this does not
// compile; a Materialise that read the worktree rather than the projection
// would return the committed a.go bytes, and one that dropped provenance would
// leave prov.Head empty, failing those assertions.
func TestGuardProjectionAndMaterialise(t *testing.T) {
	repo := controlGitRepo(t)
	a := filepath.Join(repo, "a.go")
	b := filepath.Join(repo, "new.go")
	h := newMemHost(repo, nil)
	h.projection = map[string][]byte{
		a: []byte("package a\n\nfunc f() {}\n"),
		b: []byte("package a\n\nvar x = 1\n"),
	}
	g := NewGuard(h)

	got := g.Projection(ProjectionWithProposed)
	if len(got) != 2 {
		t.Fatalf("Projection = %v, want the two canned paths", got)
	}
	for p, want := range h.projection {
		if string(got[p]) != string(want) {
			t.Errorf("Projection[%s] = %q, want %q", p, got[p], want)
		}
	}

	m, prov, err := g.Materialise(context.Background(), g.Projection(ProjectionWithProposed), git.MaterialiseOptions{})
	if err != nil {
		t.Fatalf("Materialise: %v", err)
	}
	if m == nil {
		t.Fatal("Materialise returned no scratch tree")
	}

	for _, name := range []string{"a.go", "new.go"} {
		data, err := os.ReadFile(filepath.Join(m.Dir, name))
		if err != nil {
			t.Fatalf("read materialised %s: %v", name, err)
		}
		if want := h.projection[filepath.Join(repo, name)]; string(data) != string(want) {
			t.Errorf("materialised %s = %q, want the projected %q", name, data, want)
		}
	}

	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = repo
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse HEAD: %v", err)
	}
	if head := strings.TrimSpace(string(out)); prov.Head != head {
		t.Errorf("Provenance.Head = %q, want %q", prov.Head, head)
	}
	if prov.DirtyDigest == "" {
		t.Error("Provenance.DirtyDigest is empty")
	}

	if err := m.Remove(); err != nil {
		t.Fatalf("Materialised.Remove: %v", err)
	}
	if _, err := os.Stat(m.Dir); !os.IsNotExist(err) {
		t.Errorf("scratch dir %s still exists after Remove (stat err %v)", m.Dir, err)
	}
}

// TestProjectionRevisionStableAndContentSensitive pins the digest the hook
// per-revision cap keys on: the same projection must hash the same however the
// map was built, a changed payload or path must change it, and two distinct
// projections must not concatenate to one byte stream. Without the sorted
// paths the random map order would make it unstable; without the length
// prefixes the ambiguous pair below collides on the concatenated bytes.
func TestProjectionRevisionStableAndContentSensitive(t *testing.T) {
	base := map[string][]byte{
		"/w/a.go": []byte("package a\n"),
		"/w/b.go": []byte("package b\n"),
	}
	same := map[string][]byte{
		"/w/b.go": []byte("package b\n"),
		"/w/a.go": []byte("package a\n"),
	}
	if projectionRevision(base) != projectionRevision(same) {
		t.Error("projectionRevision changed with map insertion order")
	}

	changedBytes := map[string][]byte{
		"/w/a.go": []byte("package a\n\nvar x = 1\n"),
		"/w/b.go": []byte("package b\n"),
	}
	if projectionRevision(base) == projectionRevision(changedBytes) {
		t.Error("projectionRevision ignored a changed payload")
	}

	changedPath := map[string][]byte{
		"/w/a.go": []byte("package a\n"),
		"/w/c.go": []byte("package b\n"),
	}
	if projectionRevision(base) == projectionRevision(changedPath) {
		t.Error("projectionRevision ignored a changed path")
	}

	// "ab"+"c" and "a"+"bc" concatenate to the same stream; the length prefixes
	// must keep them distinct.
	ambiguousA := map[string][]byte{"/x/ab": []byte("c")}
	ambiguousB := map[string][]byte{"/x/a": []byte("bc")}
	if projectionRevision(ambiguousA) == projectionRevision(ambiguousB) {
		t.Error("projectionRevision concatenated two distinct projections to one stream")
	}
}

// TestDispatchExecProjectedExeccheck pins the execcheck half of a projected
// exec: the event thread answers with the live projection and the workspace
// root so the runner can materialise off it, and refuses --dir outright.
//
// Precondition: a Guard over memHost with a canned projection for a real
// repository. Without the Dispatch branch res.Projection is nil and Root is
// empty; without the --dir guard the second request is accepted and the
// caller's directory is silently ignored, which is the misdirection the
// refusal exists to prevent.
func TestDispatchExecProjectedExeccheck(t *testing.T) {
	repo := controlGitRepo(t)
	a := filepath.Join(repo, "a.go")
	h := newMemHost(repo, nil)
	h.projection = map[string][]byte{a: []byte("package projected\n")}
	g := NewGuard(h)

	res := Dispatch(g, Request{Op: "execcheck", ExecProjected: true, Argv: []string{"true"}})
	if !res.OK || res.Err != "" {
		t.Fatalf("projected execcheck = %+v", res)
	}
	if len(res.Projection) == 0 {
		t.Error("projected execcheck returned no projection for the runner to materialise")
	}
	if string(res.Projection[a]) != "package projected\n" {
		t.Errorf("projection[a.go] = %q, want the projected bytes", res.Projection[a])
	}
	if res.Root != repo {
		t.Errorf("Root = %q, want %q", res.Root, repo)
	}

	res = Dispatch(g, Request{Op: "execcheck", ExecProjected: true, Dir: "/w", Argv: []string{"true"}})
	if res.Err == "" || !strings.Contains(res.Err, "--dir is not supported") {
		t.Errorf("projected execcheck with a dir = %+v, want the --dir refusal", res)
	}
}

// TestExecProjectedRunsInScratchTree drives the whole loop through the real
// entry path: a real server and socket, a real git repository, and
// connection.exec materialising the live projection into a scratch tree,
// running the command with that tree as its cwd, and removing the tree after.
//
// Precondition: controlGitRepo(t) commits a.go with "package a\n" and nothing
// else; the fake host's projection replaces a.go with "package projected\n".
// Without the projected path the command would run in the worktree and print
// its cwd there, and read the committed "package a\n"; with it the cwd is a
// raj-materialise- scratch tree and the bytes are the projected ones.
func TestExecProjectedRunsInScratchTree(t *testing.T) {
	repo := controlGitRepo(t)
	a := filepath.Join(repo, "a.go")
	proj := map[string][]byte{a: []byte("package projected\n")}

	ed := newFakeEditor(t, nil)
	ed.mu.Lock()
	ed.policyMem.root = repo
	ed.policyMem.projection = proj
	ed.mu.Unlock()

	c, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	marker := filepath.Join(t.TempDir(), "cwd.txt")
	script := "pwd > '" + marker + "'; cat a.go"
	var out, errOut strings.Builder
	res, err := c.DoExec(Request{Op: "exec", ExecProjected: true,
		Argv: []string{"sh", "-c", script}},
		func(stream uint8, b string) {
			if stream == StreamStderr {
				errOut.WriteString(b)
			} else {
				out.WriteString(b)
			}
		})
	if err != nil {
		t.Fatalf("DoExec: %v", err)
	}
	if res.Err != "" || !res.OK {
		t.Fatalf("projected exec = %+v", res)
	}
	if got := strings.TrimSpace(out.String()); got != "package projected" {
		t.Errorf("command saw %q, want the projected bytes", got)
	}
	if stamp := errOut.String(); !strings.Contains(stamp, "HEAD") ||
		!strings.Contains(stamp, "accepted and proposed text included") {
		t.Errorf("provenance stamp = %q, want the HEAD and inclusion line", stamp)
	}

	raw, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read the command's cwd: %v", err)
	}
	scratch := strings.TrimSpace(string(raw))
	if scratch == repo || !strings.Contains(filepath.Base(scratch), "raj-materialise-") {
		t.Errorf("command cwd = %q, want a raj-materialise scratch tree", scratch)
	}
	// Remove runs in a defer, after the final frame is emitted, so the client
	// can return a moment before the directory is gone. Poll rather than race.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(scratch); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("scratch tree %s still exists after the run", scratch)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestDispatchExecProjectedRefusesSecondaryRoot pins the multi-root admission
// check: a projection that holds a path under a workspace root other than the
// primary one is refused by name at execcheck, before the runner can
// materialise a tree that silently omits it.
//
// Precondition: controlGitRepo(t) makes a real committed repository, and the
// memHost projection holds one path under it (repo/a.go) and one absolute path
// elsewhere (/elsewhere/other.go). Without the firstOutsideRoot check the
// execcheck is accepted and returns both paths, so the runner would build a
// tree from the primary root alone and drop the other; the refusal names the
// offending path instead.
func TestDispatchExecProjectedRefusesSecondaryRoot(t *testing.T) {
	repo := controlGitRepo(t)
	h := newMemHost(repo, nil)
	h.projection = map[string][]byte{
		filepath.Join(repo, "a.go"): []byte("package projected\n"),
		"/elsewhere/other.go":       []byte("package other\n"),
	}
	g := NewGuard(h)

	res := Dispatch(g, Request{Op: "execcheck", ExecProjected: true, Argv: []string{"true"}})
	if res.Err == "" {
		t.Fatalf("projected execcheck over a secondary root = %+v, want a refusal", res)
	}
	if !strings.Contains(res.Err, "/elsewhere/other.go") {
		t.Errorf("refusal = %q, want it to name /elsewhere/other.go", res.Err)
	}
	if !strings.Contains(res.Err, "multi-root projected exec is not supported") {
		t.Errorf("refusal = %q, want the multi-root wording", res.Err)
	}
}
