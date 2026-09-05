package control

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The behavioural spec for talking to an editor that is not on this filesystem.
//
// Every test here drives the shipped server and the shipped client, on a real
// loopback socket, because the things that break across this boundary — a
// missing token, a path that means something else at the other end — are
// exactly the things a fake transport would not reproduce.

func newTCPEditor(t *testing.T, docs map[string]string) *fakeEditor {
	t.Helper()
	return newFakeEditorAt(t, "tcp://127.0.0.1:0", docs)
}

func TestParseAddr(t *testing.T) {
	for _, c := range []struct{ in, network, address string }{
		{"/run/user/1000/raj/1.sock", "unix", "/run/user/1000/raj/1.sock"},
		{"tcp://127.0.0.1:7391", "tcp", "127.0.0.1:7391"},
		{"tcp://host.docker.internal:7391", "tcp", "host.docker.internal:7391"},
		{"tcp://:7391", "tcp", ":7391"},
		{"", "unix", ""},
	} {
		n, a := ParseAddr(c.in)
		if n != c.network || a != c.address {
			t.Errorf("ParseAddr(%q) = %q,%q; want %q,%q", c.in, n, a, c.network, c.address)
		}
	}
}

func TestLoopback(t *testing.T) {
	for _, c := range []struct {
		in   string
		want bool
	}{
		{"127.0.0.1:7391", true},
		{"localhost:7391", true},
		{"[::1]:7391", true},
		{"0.0.0.0:7391", false},
		{":7391", false},
		{"192.168.1.10:7391", false},
		{"nonsense", false},
	} {
		if got := Loopback(c.in); got != c.want {
			t.Errorf("Loopback(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// A TCP listener reports an address a client can dial, with the port it was
// actually given rather than the zero it asked for.
func TestTCPListenerReportsADialableAddress(t *testing.T) {
	ed := newTCPEditor(t, map[string]string{"/w/a.go": "package main\n"})
	addr := ed.srv.Path()
	if !IsTCP(addr) || strings.HasSuffix(addr, ":0") {
		t.Fatalf("address %q is not dialable", addr)
	}
	if ed.srv.Token() == "" {
		t.Fatal("a TCP listener must mint a token")
	}
}

// The round trip itself: everything the protocol does over a socket, over TCP.
func TestTCPRoundTrip(t *testing.T) {
	ed := newTCPEditor(t, map[string]string{"/w/a.go": "package main\n"})
	t.Setenv(TokenEnv, ed.srv.Token())

	c, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	res, err := c.Do(Request{Op: "text", Path: "/w/a.go"})
	if err != nil || res.Err != "" {
		t.Fatalf("read: %v %q", err, res.Err)
	}
	if res.Text() != "package main\n" {
		t.Errorf("read %q", res.Text())
	}
	base := res.Version
	res, err = c.Do(Request{Op: "apply", Path: "/w/a.go", Base: &base,
		Hunks: []Hunk{{Start: 0, End: 7, Text: "PACKAGE"}}})
	if err != nil || res.Err != "" {
		t.Fatalf("apply: %v %q", err, res.Err)
	}
	if got := ed.docs["/w/a.go"]; got != "PACKAGE main\n" {
		t.Errorf("document is %q", got)
	}
}

// No token, or the wrong one, is refused — and the refusal names the variable
// to set, since the person reading it is an agent with no other way to find out.
func TestTCPRefusesAWrongToken(t *testing.T) {
	ed := newTCPEditor(t, map[string]string{"/w/a.go": "x"})
	for _, tok := range []string{"", "not-the-token", ed.srv.Token() + "x"} {
		t.Setenv(TokenEnv, tok)
		c, err := Dial(ed.srv.Path())
		if err != nil {
			t.Fatal(err)
		}
		res, err := c.Do(Request{Op: "buffers"})
		if err == nil && res.Err == "" {
			t.Fatalf("token %q was accepted", tok)
		}
		if res.Err != "" && !strings.Contains(res.Err, TokenEnv) {
			t.Errorf("refusal %q does not say what to set", res.Err)
		}
		c.Close()
	}
	// And the right one still works, so the check is not simply refusing
	// everything.
	t.Setenv(TokenEnv, ed.srv.Token())
	c, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if res, err := c.Do(Request{Op: "buffers"}); err != nil || res.Err != "" {
		t.Fatalf("the real token was refused: %v %q", err, res.Err)
	}
}

// A refused connection is over. A client that could keep guessing on one
// connection would make the token's length the only defence.
func TestTCPHangsUpOnAWrongToken(t *testing.T) {
	ed := newTCPEditor(t, map[string]string{"/w/a.go": "x"})
	t.Setenv(TokenEnv, "wrong")
	c, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Do(Request{Op: "buffers"}); err != nil {
		return // already gone, which is also a hangup
	}
	if _, err := c.Do(Request{Op: "buffers"}); err == nil {
		t.Error("a second request was served on a connection that failed to authenticate")
	}
}

// The socket is authorised by the filesystem, so a token in the environment is
// neither required nor an obstacle there.
func TestUnixIgnoresTheToken(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "x"})
	if ed.srv.Token() != "" {
		t.Error("a Unix listener should not mint a token")
	}
	t.Setenv(TokenEnv, "irrelevant")
	c, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if res, err := c.Do(Request{Op: "buffers"}); err != nil || res.Err != "" {
		t.Fatalf("a token broke the socket: %v %q", err, res.Err)
	}
}

// exec over TCP runs on the editor's machine, which for a driver in a container
// is outside the container. Refused unless the user asked for it, and the
// refusal says where to run the command instead.
func TestTCPRefusesExecUnlessAllowed(t *testing.T) {
	ed := newTCPEditor(t, map[string]string{"/w/a.go": "x"})
	t.Setenv(TokenEnv, ed.srv.Token())
	c, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	res, err := c.DoExec(Request{Op: "exec", Argv: []string{"true"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Err == "" {
		t.Fatal("exec was allowed over TCP by default")
	}
	if !strings.Contains(res.Err, "sandbox") || !strings.Contains(res.Err, "--control-exec") {
		t.Errorf("refusal %q does not say why, or how to allow it", res.Err)
	}

	ed.srv.AllowRemoteExec = true
	if res, err = c.DoExec(Request{Op: "exec", Argv: []string{"true"}}, nil); err != nil {
		t.Fatal(err)
	}
	if res.Err != "" {
		t.Errorf("exec still refused once allowed: %q", res.Err)
	}
}

// The socket has always allowed exec: the caller could run the command itself.
func TestUnixAllowsExec(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "x"})
	c, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	res, err := c.DoExec(Request{Op: "exec", Argv: []string{"true"}}, nil)
	if err != nil || res.Err != "" {
		t.Fatalf("exec over the socket: %v %q", err, res.Err)
	}
}

// ---------- path translation ----------

func TestMapperRewritesInsideTheTreeOnly(t *testing.T) {
	m := Mapper{Local: "/workspace", Editor: "/Users/rajan/src/raj"}
	for _, c := range []struct{ local, editor string }{
		{"/workspace/internal/app.go", "/Users/rajan/src/raj/internal/app.go"},
		{"/workspace", "/Users/rajan/src/raj"},
	} {
		if got := m.ToEditor(c.local); got != c.editor {
			t.Errorf("ToEditor(%q) = %q, want %q", c.local, got, c.editor)
		}
		if got := m.FromEditor(c.editor); got != c.local {
			t.Errorf("FromEditor(%q) = %q, want %q", c.editor, got, c.local)
		}
	}
	// A path outside the tree, a relative path and the empty path — which means
	// "the buffer the user is looking at" — are all left exactly as they are.
	for _, p := range []string{"/etc/passwd", "internal/app.go", "", "/workspace-other/a.go"} {
		if got := m.ToEditor(p); got != p {
			t.Errorf("ToEditor(%q) rewrote to %q", p, got)
		}
	}
}

func TestMapperZeroValueRewritesNothing(t *testing.T) {
	var m Mapper
	if m.Active() {
		t.Fatal("the zero mapper should be inactive")
	}
	if got := m.ToEditor("/a/b.go"); got != "/a/b.go" {
		t.Errorf("got %q", got)
	}
}

func TestMapperFromEnv(t *testing.T) {
	t.Setenv(RootMapEnv, "/workspace=/Users/rajan/src/raj")
	m, err := MapperFromEnv()
	if err != nil || m.Local != "/workspace" || m.Editor != "/Users/rajan/src/raj" {
		t.Fatalf("got %+v, %v", m, err)
	}
	// A value that does not parse is an error, not an ignored setting: silently
	// not mapping looks exactly like the editor having the wrong files open.
	for _, bad := range []string{"/workspace", "=/x", "/workspace=", "workspace=/x", "/w=x"} {
		t.Setenv(RootMapEnv, bad)
		if _, err := MapperFromEnv(); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func TestInferMapper(t *testing.T) {
	local := t.TempDir()
	// A root that exists here means one filesystem, so nothing is rewritten —
	// this is the case that made inference dangerous on a Unix socket.
	if m := inferMapper(local, local); m.Active() {
		t.Errorf("mapped across a shared filesystem: %+v", m)
	}
	if m := inferMapper(local, filepath.Dir(local)); m.Active() {
		t.Errorf("mapped onto an existing parent: %+v", m)
	}
	// A root that does not exist here is the other side of a boundary.
	m := inferMapper(local, "/Users/rajan/src/raj")
	if !m.Active() || m.Local != local || m.Editor != "/Users/rajan/src/raj" {
		t.Errorf("got %+v", m)
	}
}

func TestWorkspaceRootFindsTheRepository(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "internal", "app")
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := WorkspaceRoot(sub); got != root {
		t.Errorf("got %q, want %q", got, root)
	}
	// No repository: the directory itself, which is what raj does with its own
	// argument.
	bare := t.TempDir()
	if got := WorkspaceRoot(bare); got != bare {
		t.Errorf("got %q, want %q", got, bare)
	}
}

// The container case end to end. The editor holds /w, which does not exist on
// this filesystem; the client is working in a directory that stands for the
// same tree mounted somewhere else. Paths must cross in both directions
// without either end knowing.
func TestPathsAreTranslatedOverTCP(t *testing.T) {
	ed := newTCPEditor(t, map[string]string{"/w/a.go": "needle here\n"})
	t.Setenv(TokenEnv, ed.srv.Token())
	local := t.TempDir()

	c, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	m, err := c.ResolveRoots(local)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Active() || m.Local != local || m.Editor != "/w" {
		t.Fatalf("inferred %+v", m)
	}

	// Outbound: a path the caller can see reaches the buffer the editor has.
	res, err := c.Do(Request{Op: "text", Path: filepath.Join(local, "a.go")})
	if err != nil || res.Err != "" {
		t.Fatalf("read: %v %q", err, res.Err)
	}
	if res.Text() != "needle here\n" {
		t.Errorf("read %q", res.Text())
	}

	// Inbound: what comes back is in the caller's coordinates, so it can be
	// handed to the caller's own tools.
	res, err = c.Do(Request{Op: "buffers"})
	if err != nil || len(res.Buffers) != 1 {
		t.Fatalf("buffers: %v %+v", err, res.Buffers)
	}
	if want := filepath.Join(local, "a.go"); res.Buffers[0].Path != want {
		t.Errorf("buffer path %q, want %q", res.Buffers[0].Path, want)
	}
	if res.Root != local {
		t.Errorf("root %q, want %q", res.Root, local)
	}

	sres, err := c.DoStream(Request{Op: "search", Query: &SearchQuery{Text: "needle"}}, nil)
	if err != nil || len(sres.Matches) == 0 {
		t.Fatalf("search: %v %+v", err, sres.Matches)
	}
	if want := filepath.Join(local, "a.go"); sres.Matches[0].Path != want {
		t.Errorf("match path %q, want %q", sres.Matches[0].Path, want)
	}
}

// An explicit map is honoured on any transport, and beats inference.
func TestExplicitRootMapWins(t *testing.T) {
	ed := newTCPEditor(t, map[string]string{"/w/a.go": "x"})
	t.Setenv(TokenEnv, ed.srv.Token())
	t.Setenv(RootMapEnv, "/mnt/code=/w")

	c, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if m, err := c.ResolveRoots("/somewhere/else"); err != nil || m.Local != "/mnt/code" {
		t.Fatalf("got %+v, %v", m, err)
	}
	res, err := c.Do(Request{Op: "text", Path: "/mnt/code/a.go"})
	if err != nil || res.Err != "" {
		t.Fatalf("read: %v %q", err, res.Err)
	}
}

// Nothing is inferred over a Unix socket, whatever the editor's root says: a
// socket is a filesystem object, so reaching one proves the filesystem is
// shared.
func TestNoInferenceOverTheSocket(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "x"})
	c, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	m, err := c.ResolveRoots(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if m.Active() {
		t.Errorf("inferred %+v over a socket", m)
	}
}

// The token rides in the header, so a frame that carries one still decodes to a
// request that carries the same one.
func TestTokenSurvivesTheWire(t *testing.T) {
	h, body := EncodeRequest(Request{ID: 1, Op: "buffers", Token: "s3cret"})
	got, err := DecodeRequest(Frame{Header: h, Body: body})
	if err != nil {
		t.Fatal(err)
	}
	if got.Token != "s3cret" {
		t.Errorf("token %q", got.Token)
	}
}
