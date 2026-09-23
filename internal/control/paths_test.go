package control

import "testing"

// A multi-pair RAJ_ROOT_MAP maps each local mount to its own editor root, so a
// container with two bind mounts does not have to pretend the tree is one.
func TestMapperFromEnvMultiplePairs(t *testing.T) {
	t.Setenv(RootMapEnv, "/work/a=/Users/rajan/src/projA, /work/b=/Users/rajan/src/projB")
	m, err := MapperFromEnv()
	if err != nil {
		t.Fatalf("MapperFromEnv: %v", err)
	}
	if !m.Active() {
		t.Fatal("a two-pair mapper should be active")
	}
	if got, want := m.String(), "/work/a=/Users/rajan/src/projA,/work/b=/Users/rajan/src/projB"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
	if got, want := len(m.pairs), 2; got != want {
		t.Fatalf("pairs = %d, want %d", got, want)
	}
}

// A malformed entry anywhere in the list is an error, not a silently dropped
// pair: half a map is the failure that looks like the editor having the wrong
// files open.
func TestMapperFromEnvRejectsMalformedPairs(t *testing.T) {
	for _, bad := range []string{
		"/a=/b,/c",
		"/a=/b,/c=",
		"/a=/b,=/d",
		"/a=/b,/c=relative",
		"a=/b,/c=/d",
		"/a=/b,",
	} {
		t.Setenv(RootMapEnv, bad)
		if _, err := MapperFromEnv(); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

// Each path is rebased by the pair that contains it, and a path under neither
// is left alone rather than guessed at.
func TestMapperPicksThePairPerPath(t *testing.T) {
	m := NewMapper(
		Pair{Local: "/work/a", Editor: "/Users/rajan/src/projA"},
		Pair{Local: "/work/b", Editor: "/Users/rajan/src/projB"},
	)
	for _, c := range []struct{ local, editor string }{
		{"/work/a/main.go", "/Users/rajan/src/projA/main.go"},
		{"/work/b/pkg/x.go", "/Users/rajan/src/projB/pkg/x.go"},
		{"/work/a", "/Users/rajan/src/projA"},
		{"/work/b", "/Users/rajan/src/projB"},
	} {
		if got := m.ToEditor(c.local); got != c.editor {
			t.Errorf("ToEditor(%q) = %q, want %q", c.local, got, c.editor)
		}
		if got := m.FromEditor(c.editor); got != c.local {
			t.Errorf("FromEditor(%q) = %q, want %q", c.editor, got, c.local)
		}
	}
	// A sibling whose name starts with a mapped root's characters is not under
	// it, and a path under neither mount is identity.
	for _, p := range []string{"/work/a-other/x.go", "/etc/passwd", "/work/c/x.go"} {
		if got := m.ToEditor(p); got != p {
			t.Errorf("ToEditor(%q) rewrote to %q", p, got)
		}
	}
}

// Inference can only place one root at a boundary: the first is paired with the
// local workspace root and the rest are left identity, because nothing on this
// side says which local directory stands for a second mount.
func TestInferMapperMapsOnlyTheInferableRoot(t *testing.T) {
	local := t.TempDir()
	m := inferMapper(local, []string{"/Users/rajan/src/projA", "/Users/rajan/src/projB"})
	if !m.Active() || len(m.pairs) != 1 {
		t.Fatalf("inferred %+v", m)
	}
	if got := m.pairs[0]; got != (Pair{Local: local, Editor: "/Users/rajan/src/projA"}) {
		t.Fatalf("pair = %+v, want the first root mapped", got)
	}
	if got := m.ToEditor("/Users/rajan/src/projB/x.go"); got != "/Users/rajan/src/projB/x.go" {
		t.Errorf("the second root was rewritten to %q", got)
	}
}

// Roots reports the set the current peer last named. A reply that names none
// clears it rather than leaving a stale set from an earlier reply, because the
// attached app keeps its own adopted copy and the wire cache must not claim a
// workspace this peer never reported.
func TestRootsClearsOnSetLessReply(t *testing.T) {
	ed := newTCPEditor(t, map[string]string{"/w/a.go": "x"})
	t.Setenv(TokenEnv, ed.srv.Token())
	c, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	if _, err := c.Do(Request{Op: "ping"}); err != nil {
		t.Fatal(err)
	}
	if got := c.Roots(); len(got) == 0 {
		t.Fatal("ping did not populate Roots()")
	}
	// text is not a root-carrying reply, so it names no set.
	if _, err := c.Do(Request{Op: "text", Path: "/w/a.go"}); err != nil {
		t.Fatal(err)
	}
	if got := c.Roots(); got != nil {
		t.Errorf("Roots() = %q after a set-less reply, want nil", got)
	}
}
