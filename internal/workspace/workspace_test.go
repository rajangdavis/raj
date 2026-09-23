package workspace_test

import (
	"path/filepath"
	"strings"
	"testing"

	"raj/internal/session"
	"raj/internal/workspace"
)

// New folds exact duplicates and keeps the first occurrence's position.
func TestNewDedupesAndPreservesOrder(t *testing.T) {
	r, err := workspace.New("/repo/alpha", "/repo/beta", "/repo/alpha")
	if err != nil {
		t.Fatal(err)
	}
	if got := r.All(); len(got) != 2 || got[0] != "/repo/alpha" || got[1] != "/repo/beta" {
		t.Errorf("All() = %v, want the first occurrence kept: [/repo/alpha /repo/beta]", got)
	}
	if r.Len() != 2 {
		t.Errorf("Len() = %d, want 2", r.Len())
	}
}

// New rejects a root nested inside another in either argument order, naming
// both roots in the error.
func TestNewRejectsNesting(t *testing.T) {
	for _, tc := range []struct {
		name  string
		paths []string
	}{
		{"parent then child", []string{"/repo", "/repo/sub"}},
		{"child then parent", []string{"/repo/sub", "/repo"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := workspace.New(tc.paths...)
			if err == nil {
				t.Fatalf("New(%v) accepted a nested pair", tc.paths)
			}
			for _, p := range tc.paths {
				if !strings.Contains(err.Error(), p) {
					t.Errorf("error %q does not name %q", err, p)
				}
			}
		})
	}
}

// Equal paths are a duplicate to fold, not a nesting to reject.
func TestNewEqualPathsAreDedupedNotRejected(t *testing.T) {
	r, err := workspace.New("/repo/same", "/repo/same")
	if err != nil {
		t.Fatalf("New rejected equal paths: %v", err)
	}
	if r.Len() != 1 || r.Primary() != "/repo/same" {
		t.Errorf("Len=%d Primary=%q, want one root", r.Len(), r.Primary())
	}
}

// No non-empty path is the zero Roots and no error: the same "no workspace
// root" state the old empty root string meant.
func TestNewEmptyIsZeroRoots(t *testing.T) {
	for _, tc := range []struct {
		name  string
		paths []string
	}{
		{"no arguments", nil},
		{"only empty", []string{"", ""}},
		{"only whitespace", []string{"  ", "\t"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := workspace.New(tc.paths...)
			if err != nil {
				t.Fatalf("New(%v) = %v, want no error", tc.paths, err)
			}
			if r.Len() != 0 || r.Primary() != "" {
				t.Errorf("Len=%d Primary=%q, want the zero Roots", r.Len(), r.Primary())
			}
			if got := r.All(); got != nil {
				t.Errorf("All() = %v, want nil", got)
			}
			if got := r.Names(); got != nil {
				t.Errorf("Names() = %v, want nil", got)
			}
			if r.Contains("/repo") {
				t.Error("the zero Roots contains a path")
			}
		})
	}
}

// Primary, All and Names keep caller-supplied order, and All is a copy.
func TestPrimaryAllNames(t *testing.T) {
	r, err := workspace.New("/repo/alpha", "/repo/beta")
	if err != nil {
		t.Fatal(err)
	}
	if r.Primary() != "/repo/alpha" {
		t.Errorf("Primary() = %q, want /repo/alpha", r.Primary())
	}
	if got := r.Names(); len(got) != 2 || got[0] != "alpha" || got[1] != "beta" {
		t.Errorf("Names() = %v, want [alpha beta]", got)
	}
	all := r.All()
	all[0] = "/mutated"
	if r.Primary() != "/repo/alpha" {
		t.Errorf("mutating All() changed the Roots: Primary = %q", r.Primary())
	}
}

// Contains answers by path components, not string prefix: a root, a child, a
// sibling, a parent and a lookalike are all distinguished.
func TestContains(t *testing.T) {
	r, err := workspace.New("/repo/alpha", "/repo/beta")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		path string
		want bool
	}{
		{"equal to a root", "/repo/alpha", true},
		{"equal to the second root", "/repo/beta", true},
		{"inside a root", "/repo/alpha/pkg/deep.go", true},
		{"sibling outside", "/repo/gamma", false},
		{"parent of a root", "/repo", false},
		{"structurally similar but not nested", "/repo/alpha2", false},
		{"relative path", "repo/alpha/file.go", false},
		{"empty path", "", false},
	} {
		if got := r.Contains(tc.path); got != tc.want {
			t.Errorf("%s: Contains(%q) = %v, want %v", tc.name, tc.path, got, tc.want)
		}
	}
}

// RootFor picks the root containing a path, component-wise, and reports none
// for a path outside every root or a relative one. Roots are not nested, so the
// answer is unique, and the zero Roots answers none.
func TestRootFor(t *testing.T) {
	r, err := workspace.New("/repo/alpha", "/repo/beta")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		path string
		want string
	}{
		{"exact first root", "/repo/alpha", "/repo/alpha"},
		{"exact second root", "/repo/beta", "/repo/beta"},
		{"child of the second root", "/repo/beta/pkg/deep.go", "/repo/beta"},
		{"a sibling prefix is not inside", "/repo/alpha2", ""},
		{"a parent of a root is not inside", "/repo", ""},
		{"relative path", "repo/alpha/file.go", ""},
		{"empty path", "", ""},
	} {
		if got := r.RootFor(tc.path); got != tc.want {
			t.Errorf("%s: RootFor(%q) = %q, want %q", tc.name, tc.path, got, tc.want)
		}
	}

	zero, err := workspace.New()
	if err != nil {
		t.Fatal(err)
	}
	if got := zero.RootFor("/repo/alpha"); got != "" {
		t.Errorf("the zero Roots RootFor = %q, want \"\"", got)
	}
}

// TestStateKeyGolden pins the key algorithm against a hand-derived literal. A
// change to the slug, the digest input or the digest width shifts this string
// and fails, which is the point. The value is
// "raj-" + slug("raj") + "-" + the first four bytes of sha256 of the path.
func TestStateKeyGolden(t *testing.T) {
	const path = "/Users/rajandavis/Desktop/projects/raj"
	const want = "raj-raj-89862ebb"
	if got := workspace.StateKey([]string{path}); got != want {
		t.Errorf("StateKey(%q) = %q, want %q", path, got, want)
	}
}

// A multi-root key is independent of the order the roots were supplied, and
// takes its readable slug from the first root after sorting.
func TestStateKeyOrderIndependent(t *testing.T) {
	a, b := "/repo/alpha", "/repo/zeta"
	if got, want := workspace.StateKey([]string{a, b}), workspace.StateKey([]string{b, a}); got != want {
		t.Errorf("StateKey depends on argument order: %q vs %q", got, want)
	}
	if key := workspace.StateKey([]string{b, a}); !strings.Contains(key, "alpha") {
		t.Errorf("StateKey(%q, %q) = %q, want the sorted-first root's slug", b, a, key)
	}
}

// The extracted key must be byte-for-byte the one session.StateDir has always
// produced, so a single-root workspace keeps the same state directory after the
// move. This is the equivalence pin between the two packages.
func TestStateKeyMatchesSessionStateDir(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	if got, want := filepath.Base(session.StateDir(root)), workspace.StateKey([]string{root}); got != want {
		t.Errorf("session key %q != workspace.StateKey %q", got, want)
	}
}
