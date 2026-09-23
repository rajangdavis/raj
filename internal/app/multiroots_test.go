package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"raj/internal/control"
	"raj/internal/keys"
	"raj/internal/session"
	"raj/internal/ui"
)

// newRootsHarness builds an app over two workspace roots, each holding one
// file, with nothing open. It is NewWithRoots rather than New so the constructor
// under test is the one the daemon uses for several roots.
func newRootsHarness(t *testing.T) (*harness, string, string) {
	t.Helper()
	rootA := t.TempDir()
	rootB := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootA, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootB, "b.go"), []byte("package b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	host := ui.NewFakeHost(120, 24)
	t.Cleanup(func() { host.Close() })
	a := NewWithRoots(host, []string{rootA, rootB}, Options{TabWidth: 2})
	t.Cleanup(a.CloseState)
	a.Search.Debounce = time.Nanosecond
	return &harness{App: a, host: host}, rootA, rootB
}

// A path under the second root is in the workspace and one outside every root
// is not. The failure mode this pins: every containment check was written
// against primaryRoot, so the second root was invisible to the Guard and to the
// app's own lexical backstop.
func TestMultiRootPathIsInWorkspace(t *testing.T) {
	h, _, rootB := newRootsHarness(t)
	inB := filepath.Join(rootB, "b.go")
	outside := filepath.Join(t.TempDir(), "outside.go")

	if !h.roots.Contains(inB) || h.roots.Contains(outside) {
		t.Fatalf("roots.Contains: root2=%v outside=%v", h.roots.Contains(inB), h.roots.Contains(outside))
	}
	if got := h.rootFor(inB); got != rootB {
		t.Errorf("rootFor(%s) = %q, want %q", inB, got, rootB)
	}
	if !h.pathInRoot(inB) || h.pathInRoot(outside) {
		t.Errorf("pathInRoot: root2=%v outside=%v", h.pathInRoot(inB), h.pathInRoot(outside))
	}

	g := control.NewGuard(host{h.App})
	// A directory under the second root is readable; one outside every root is
	// refused by the Guard's in-root check.
	if _, err := g.Ls(rootB, false); err != nil {
		t.Errorf("ls under the second root: %v", err)
	}
	if _, err := g.Ls(t.TempDir(), false); !errors.Is(err, control.ErrOutsideoot) {
		t.Errorf("ls outside every root = %v, want a root refusal", err)
	}
	// delete of a root2 path passes the in-workspace gate and then fails on the
	// claim, while an outside path fails at the root gate. The error identity is
	// the point: only the outside one is a workspace refusal.
	if err := g.Delete(inB, 7, false); errors.Is(err, control.ErrOutsideoot) {
		t.Errorf("delete under the second root was refused as outside: %v", err)
	}
	if err := g.Delete(outside, 7, false); !errors.Is(err, control.ErrOutsideoot) {
		t.Errorf("delete outside every root = %v, want a root refusal", err)
	}
}

// ls with no path lists the workspace roots, one per root, and two roots with
// the same base name stay distinguishable rather than being merged. The failure
// mode: the old default listed only the primary root's children, so the second
// root did not exist to the verb at all.
func TestLsNoPathListsRootsAndDisambiguatesSameName(t *testing.T) {
	ra := filepath.Join(t.TempDir(), "app")
	rb := filepath.Join(t.TempDir(), "app")
	for _, d := range []string{ra, rb} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	fh := ui.NewFakeHost(120, 24)
	t.Cleanup(func() { fh.Close() })
	a := NewWithRoots(fh, []string{ra, rb}, Options{TabWidth: 2})
	t.Cleanup(a.CloseState)

	entries, err := (host{a}).Ls("", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("ls with no path = %+v, want one entry per root", entries)
	}
	if entries[0].Path != ra || entries[1].Path != rb || !entries[0].Dir || !entries[1].Dir {
		t.Errorf("root entries = %+v, want [%s %s] in supplied order", entries, ra, rb)
	}
	if entries[0].Name == entries[1].Name {
		t.Errorf("same-named roots share the name %q; they must be distinguishable", entries[0].Name)
	}
	if entries[0].Name != ra || entries[1].Name != rb {
		t.Errorf("colliding roots should be labelled by absolute path, got %q and %q", entries[0].Name, entries[1].Name)
	}

	// The Guard's empty path now reaches the host, so `raj ctl ls` with no path
	// sees the same roots.
	g := control.NewGuard(host{a})
	guarded, err := g.Ls("", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(guarded) != 2 || guarded[0].Path != ra || guarded[1].Path != rb {
		t.Errorf("guard ls with no path = %+v", guarded)
	}
}

// The explorer draws one top-level node per root and expands each
// independently, and Rel spells a path under the second root relative to that
// root. The failure mode: a single Tree.Root meant the second root had no node
// and Rel fell back to the absolute path.
func TestExplorerShowsOneNodePerRoot(t *testing.T) {
	h, rootA, rootB := newRootsHarness(t)

	roots := 0
	for _, e := range h.Explorer.Tree.Entries() {
		if e.Depth == 0 {
			roots++
			if !e.Dir {
				t.Errorf("root row %s is not a directory", e.Path)
			}
		}
	}
	if roots != 2 {
		t.Fatalf("explorer has %d top-level entries, want one per root", roots)
	}
	if !h.Explorer.Tree.Expanded(rootA) || !h.Explorer.Tree.Expanded(rootB) {
		t.Fatal("a root row did not start expanded")
	}

	fileB := filepath.Join(rootB, "b.go")
	visible := func() bool {
		for _, e := range h.Explorer.Tree.Entries() {
			if e.Path == fileB {
				return true
			}
		}
		return false
	}
	if !visible() {
		t.Fatalf("b.go under the second root was not shown")
	}
	h.Explorer.Tree.Toggle(rootB)
	if visible() {
		t.Error("collapsing the second root still showed its children")
	}
	h.Explorer.Tree.Toggle(rootB)
	if !visible() {
		t.Error("expanding the second root did not bring its child back")
	}

	if got := h.Explorer.Tree.Rel(fileB); got != "b.go" {
		t.Errorf("Rel(%s) = %q, want b.go", fileB, got)
	}
}

// A search finds a hit in the second root, --path scoping into that root works,
// and a --path outside every root is refused. The failure mode: RunStream took
// one root, so the second root's files were never walked.
func TestSearchReachesTheSecondRoot(t *testing.T) {
	h, _, rootB := newRootsHarness(t)
	fileB := filepath.Join(rootB, "b.go")
	if err := os.WriteFile(fileB, []byte("package b\n// needle in the second root\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(rootB, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	fileSub := filepath.Join(sub, "c.go")
	if err := os.WriteFile(fileSub, []byte("package sub\n// needle under root two/sub\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := (host{h.App}).Snapshot()
	collect := func() []string {
		var paths []string
		_, _, _, _, err := s.Search(context.Background(), control.SearchQuery{Text: "needle"},
			func(batch []control.SearchMatch) {
				for _, m := range batch {
					paths = append(paths, m.Path)
				}
			})
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		return paths
	}

	if got := collect(); !containsString(got, fileB) || !containsString(got, fileSub) {
		t.Fatalf("search did not find the second root; paths = %v", got)
	}

	var scoped []string
	_, _, _, _, err := s.Search(context.Background(), control.SearchQuery{Text: "needle", Path: sub},
		func(batch []control.SearchMatch) {
			for _, m := range batch {
				scoped = append(scoped, m.Path)
			}
		})
	if err != nil {
		t.Fatalf("scoped search: %v", err)
	}
	if !containsString(scoped, fileSub) || containsString(scoped, fileB) {
		t.Errorf("--path %s = %v, want only its subtree", sub, scoped)
	}

	if _, _, _, _, err := s.Search(context.Background(), control.SearchQuery{Text: "needle", Path: t.TempDir()},
		func([]control.SearchMatch) {}); err == nil {
		t.Error("--path outside every root was accepted")
	}
}

// A file in the second root round-trips through the session: saved, then
// restored by a fresh app over the same roots. The failure mode: Load validated
// every saved path against primaryRoot and dropped the second root's tabs.
func TestSessionRoundTripsSecondRoot(t *testing.T) {
	h, rootA, rootB := newRootsHarness(t)
	fileB := filepath.Join(rootB, "b.go")
	h.OpenFile(fileB)
	if err := h.SaveSession(); err != nil {
		t.Fatal(err)
	}

	host2 := ui.NewFakeHost(120, 24)
	t.Cleanup(func() { host2.Close() })
	second := NewWithRoots(host2, []string{rootA, rootB}, Options{TabWidth: 2})
	t.Cleanup(second.CloseState)
	second.RestoreSession()

	p := second.Tabs.Active()
	if p == nil || p.File.Path != fileB {
		t.Fatalf("restored active tab = %v, want %s", p, fileB)
	}
}

// The journal, trash and log directories are keyed by the whole root set, the
// same key the store uses, so state lives beside the store and two sets never
// share it. The failure mode: these derived from session.StateDir(primary),
// which for a set named a directory no store ever opened.
func TestStatePathsKeyedByTheRootSet(t *testing.T) {
	h, rootA, rootB := newRootsHarness(t)
	want := session.StateDirForRoots([]string{rootA, rootB})
	if got := h.trashDir(); got != filepath.Join(want, "trash") {
		t.Errorf("trashDir = %q, want %q", got, filepath.Join(want, "trash"))
	}
	if got := h.journalDir(); got != filepath.Join(want, "logs") {
		t.Errorf("journalDir = %q, want %q", got, filepath.Join(want, "logs"))
	}
	if want == session.StateDirForRoots([]string{rootA}) {
		t.Errorf("the set key equals the primary-only key %q", want)
	}
}

// Single root regression: one root gets no root row, ls with no path lists its
// children, and the state paths are exactly the old ones. The multi-root work
// must not have changed the one-root reading.
func TestSingleRootVisibilityUnchanged(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "test.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fh := ui.NewFakeHost(120, 24)
	t.Cleanup(func() { fh.Close() })
	a := New(fh, root, 2)
	t.Cleanup(a.CloseState)

	for _, e := range a.Explorer.Tree.Entries() {
		if e.Depth != 0 {
			t.Errorf("a single-root tree grew a root row or nesting: %+v", e)
		}
		if e.Path == root {
			t.Errorf("the root itself is drawn as a row with one root: %+v", e)
		}
	}
	entries, err := (host{a}).Ls("", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Path != filepath.Join(root, "test.go") {
		t.Errorf("single-root ls with no path = %+v, want its children", entries)
	}
	if got, want := a.trashDir(), filepath.Join(session.StateDir(root), "trash"); got != want {
		t.Errorf("single-root trashDir = %q, want %q", got, want)
	}
	if got, want := a.journalDir(), filepath.Join(session.StateDir(root), "logs"); got != want {
		t.Errorf("single-root journalDir = %q, want %q", got, want)
	}
}

// A non-attach editor's visible and view roots are the same set, so routing
// visibility through the visible set changes nothing for the ordinary single
// root. The failure mode: a separate visible set that started empty would root
// the explorer at nothing.
func TestVisibleRootsEqualViewRootsForANormalEditor(t *testing.T) {
	root := t.TempDir()
	fh := ui.NewFakeHost(120, 24)
	t.Cleanup(func() { fh.Close() })
	a := New(fh, root, 2)
	t.Cleanup(a.CloseState)

	if a.visible.Len() != a.roots.Len() || a.visible.Primary() != a.roots.Primary() {
		t.Fatalf("visible = %q, view = %q; want identical", a.visible.All(), a.roots.All())
	}
	if got := a.Explorer.Tree.Roots; !sameStrings(got, []string{root}) {
		t.Errorf("explorer roots = %q, want [%s]", got, root)
	}
	if got := (host{a}).Roots(); !sameStrings(got, []string{root}) {
		t.Errorf("host Roots = %q, want [%s]", got, root)
	}
}

// The quick-open picker indexes every root, and a chosen row from the second
// root resolves under that root. The failure this pins: Picker.Root was one
// string, so cmd+p only ever saw the primary root's files.
func TestPickerIndexesEveryRoot(t *testing.T) {
	h, _, rootB := newRootsHarness(t)
	h.Picker.Show()
	if !containsString(h.Picker.Files(), "a.go") || !containsString(h.Picker.Files(), "b.go") {
		t.Fatalf("picker index = %v, want a file from each root", h.Picker.Files())
	}
	h.Picker.Paste("b.go")
	if got := h.Picker.Top(); got != "b.go" {
		t.Fatalf("top = %q, want b.go", got)
	}
	if got, want := h.Picker.Handle(keys.Confirm, ""), filepath.Join(rootB, "b.go"); got != want {
		t.Errorf("chose %q, want the second root's %q", got, want)
	}
}

// An attach adopts the daemon's root set, and the picker is rebuilt over it so
// cmd+p reaches the second root. The failure this pins: adoptVisibleRoots
// rebuilt the explorer and search but left the picker on the launch root.
func TestAdoptVisibleRootsRebuildsThePicker(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	for dir, name := range map[string]string{rootA: "a.go", rootB: "b.go"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("package x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fh := ui.NewFakeHost(120, 24)
	t.Cleanup(func() { fh.Close() })
	a := New(fh, rootA, 2)
	t.Cleanup(a.CloseState)

	// Precondition: the launch-root picker has never heard of b.go.
	a.Picker.Show()
	if containsString(a.Picker.Files(), "b.go") {
		t.Fatal("setup: the launch picker already indexed a second root")
	}

	if !a.adoptVisibleRoots([]string{rootA, rootB}) {
		t.Fatal("adoptVisibleRoots refused a valid set")
	}
	a.Picker.Show()
	if !containsString(a.Picker.Files(), "b.go") {
		t.Fatalf("the adopted picker index = %v, want the second root's b.go", a.Picker.Files())
	}
	a.Picker.Paste("b.go")
	if got, want := a.Picker.Handle(keys.Confirm, ""), filepath.Join(rootB, "b.go"); got != want {
		t.Errorf("chose %q, want the adopted root's %q", got, want)
	}
}

func containsString(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
