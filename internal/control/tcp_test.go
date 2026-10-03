package control

import (
	"errors"
	"math"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"testing"
	"time"

	"raj/internal/prog"
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
	if paths := ed.srv.Paths(); len(paths) != 1 || paths[0] != addr {
		t.Errorf("Paths() = %v, want just %q", paths, addr)
	}
}

// SocketEnv overrides DefaultPath verbatim, which is what lets a fixed socket
// outlive a restart and lets a client that cannot guess a pid find the editor.
// An empty value is not an override, so the per-process convention still wins.
func TestDefaultPathHonorsSocketEnv(t *testing.T) {
	want := controlSock(t, "env.sock")
	t.Setenv(SocketEnv, want)
	if got := DefaultPath(); got != want {
		t.Errorf("DefaultPath() = %q, want the %s override %q", got, SocketEnv, want)
	}
	t.Setenv(SocketEnv, "")
	if got := DefaultPath(); got == want {
		t.Errorf("DefaultPath() = %q, want the per-process convention when %s is empty", got, SocketEnv)
	}
}

// A port already in use fails the bind rather than moving to the next port: the
// address a client was told is the address it gets, and a silent move would
// strand every client that knew the old one. The error names the flag that
// changes the address.
func TestTCPPortInUseFailsWithoutBumping(t *testing.T) {
	first := newTCPEditor(t, map[string]string{"/w/a.go": "x"})
	addr := first.srv.Path()
	if !IsTCP(addr) {
		t.Fatalf("first listener address %q is not TCP", addr)
	}
	srv, err := ListenAll([]string{addr}, func() {})
	if err == nil {
		srv.Close()
		t.Fatal("a second bind on the same port succeeded")
	}
	for _, want := range []string{addr, "already in use", "--control-addr"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("bind error %q does not mention %q", err, want)
		}
	}
}

// A server can listen on a Unix socket and a TCP port at once, and both
// listeners drain the same queue and answer from the same document. The write
// that lands over the port is read back over the socket.
func TestBothListenersShareOneQueue(t *testing.T) {
	sock := controlSock(t, "both.sock")
	ed := newFakeEditorAddrs(t, []string{sock, "tcp://127.0.0.1:0"},
		map[string]string{"/w/a.go": "package main\n"})

	paths := ed.srv.Paths()
	if len(paths) != 2 {
		t.Fatalf("Paths() = %v, want two addresses", paths)
	}
	if ed.srv.Path() != paths[0] || paths[0] != sock {
		t.Errorf("primary = %q, Paths()[0] = %q, want the socket %q", ed.srv.Path(), paths[0], sock)
	}
	if !IsTCP(paths[1]) {
		t.Errorf("second address %q is not TCP", paths[1])
	}
	if ed.srv.Token() == "" {
		t.Fatal("a server with a TCP listener must have a token")
	}

	// Local: no token, over the socket.
	local, err := Dial(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()

	// Remote: the token, over the port.
	t.Setenv(TokenEnv, ed.srv.Token())
	remote, err := Dial(paths[1])
	if err != nil {
		t.Fatal(err)
	}
	defer remote.Close()

	// Apply over the port, read back over the socket: one queue, one document,
	// two transports.
	base := uint64(1)
	res, err := remote.Do(Request{Op: "apply", Path: "/w/a.go", Base: &base,
		Hunks: []Hunk{{Start: 0, End: 7, Text: "PACKAGE"}}})
	if err != nil || res.Err != "" {
		t.Fatalf("apply over TCP: %v %q", err, res.Err)
	}
	res, err = local.Do(Request{Op: "text", Path: "/w/a.go"})
	if err != nil || res.Err != "" {
		t.Fatalf("read over the socket: %v %q", err, res.Err)
	}
	if res.Text() != "PACKAGE main\n" {
		t.Errorf("read back %q, want the write made over TCP", res.Text())
	}
}

// The token op hands back the server's TCP secret to a caller on the local
// socket, which is how a script passes it to a container without scraping
// startup stderr.
func TestTokenOpReadsTheSecretOverTheSocket(t *testing.T) {
	sock := controlSock(t, "tok.sock")
	ed := newFakeEditorAddrs(t, []string{sock, "tcp://127.0.0.1:0"},
		map[string]string{"/w/a.go": "x"})

	c, err := Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	res, err := c.Do(Request{Op: "token"})
	if err != nil || res.Err != "" {
		t.Fatalf("token over the socket: %v %q", err, res.Err)
	}
	if res.Token == "" || res.Token != ed.srv.Token() {
		t.Errorf("token = %q, want %q", res.Token, ed.srv.Token())
	}
}

// Over TCP the op is answered too: the request already passed the token check,
// so the caller is holding the secret it would be handed back.
func TestTokenOpOverTCP(t *testing.T) {
	ed := newTCPEditor(t, map[string]string{"/w/a.go": "x"})
	t.Setenv(TokenEnv, ed.srv.Token())
	c, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	res, err := c.Do(Request{Op: "token"})
	if err != nil || res.Err != "" {
		t.Fatalf("token over TCP: %v %q", err, res.Err)
	}
	if res.Token != ed.srv.Token() {
		t.Errorf("token = %q, want %q", res.Token, ed.srv.Token())
	}
}

// A TCP caller without the token cannot reach the token op either: the refusal
// happens before any handler runs.
func TestTokenOpRefusesWithoutTheToken(t *testing.T) {
	ed := newTCPEditor(t, map[string]string{"/w/a.go": "x"})
	t.Setenv(TokenEnv, "wrong")
	c, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	res, err := c.Do(Request{Op: "token"})
	if err == nil && res.Err == "" {
		t.Fatal("the token op answered a caller with the wrong token")
	}
}

// A Unix-only server has no secret to hand out, so the op answers with nothing
// rather than inventing one.
func TestTokenOpOnAUnixOnlyServerIsEmpty(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "x"})
	c, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	res, err := c.Do(Request{Op: "token"})
	if err != nil || res.Err != "" {
		t.Fatalf("token: %v %q", err, res.Err)
	}
	if res.Token != "" {
		t.Errorf("a Unix-only server handed out %q", res.Token)
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
// is outside the container. It is refused unconditionally, and the refusal says
// where to run the command instead.
func TestTCPRefusesExec(t *testing.T) {
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
		t.Fatal("exec was allowed over TCP")
	}
	// The projected flag rides the same op, so it must hit the same refusal
	// rather than opening a second path to running on the editor's machine.
	res, err = c.DoExec(Request{Op: "exec", ExecProjected: true, Argv: []string{"true"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Err == "" {
		t.Fatal("projected exec was allowed over TCP")
	}
	if !strings.Contains(res.Err, "sandbox") || !strings.Contains(res.Err, "your own shell") {
		t.Errorf("projected refusal %q does not say why, or where to run it", res.Err)
	}
	if !strings.Contains(res.Err, "sandbox") || !strings.Contains(res.Err, "your own shell") {
		t.Errorf("refusal %q does not say why, or where to run it", res.Err)
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

// The remote-exec refusal is not bypassed by a batch. A program arrives as one
// "prog" frame, so the serve loop's direct-exec check never sees the exec
// inside it; connection.one re-checks it for every exec it runs. Without the
// re-check, a container driver could run a command on the host by wrapping it
// in a program — the sandbox escape the refusal exists to prevent.
func TestTCPRefusesExecInsideAProgram(t *testing.T) {
	ed := newTCPEditor(t, map[string]string{"/w/a.go": "x"})
	t.Setenv(TokenEnv, ed.srv.Token())
	c, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	p := prog.Encode([]prog.Op{
		{Code: prog.OpArg, Payload: []byte("true")},
		{Code: prog.OpExec},
	})
	res, err := c.Do(Request{Op: "prog", Program: p})
	if err != nil {
		t.Fatal(err)
	}
	if res.Err == "" {
		t.Fatal("exec inside a program was allowed over TCP")
	}
	if !strings.Contains(res.Err, "sandbox") || !strings.Contains(res.Err, "your own shell") {
		t.Errorf("refusal %q does not say why, or where to run it", res.Err)
	}
}

// exec is program-reachable on the socket, the same as a direct exec: arg ops
// accumulate an argv and the batch runs it through the ordinary path. Over a
// Unix socket the caller could run the command itself, so it is allowed.
func TestUnixAllowsExecInsideAProgram(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "x"})
	c, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	p := prog.Encode([]prog.Op{
		{Code: prog.OpPath, Payload: []byte("/w/a.go")},
		{Code: prog.OpArg, Payload: []byte("true")},
		{Code: prog.OpExec},
	})
	res, err := c.Do(Request{Op: "prog", Program: p})
	if err != nil {
		t.Fatal(err)
	}
	if res.Err != "" || !res.OK {
		t.Fatalf("exec inside a program over the socket: %+v", res)
	}
}

// ---------- path translation ----------

func TestMapperRewritesInsideTheTreeOnly(t *testing.T) {
	m := NewMapper(Pair{Local: "/workspace", Editor: "/Users/rajan/src/raj"})
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
	if err != nil || m.String() != "/workspace=/Users/rajan/src/raj" {
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
	if m := inferMapper(local, []string{local}); m.Active() {
		t.Errorf("mapped across a shared filesystem: %+v", m)
	}
	if m := inferMapper(local, []string{filepath.Dir(local)}); m.Active() {
		t.Errorf("mapped onto an existing parent: %+v", m)
	}
	// A root that does not exist here is the other side of a boundary.
	m := inferMapper(local, []string{"/Users/rajan/src/raj"})
	if !m.Active() || len(m.pairs) != 1 || m.pairs[0] != (Pair{Local: local, Editor: "/Users/rajan/src/raj"}) {
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
	if !m.Active() || len(m.pairs) != 1 || m.pairs[0] != (Pair{Local: local, Editor: "/w"}) {
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
	if m, err := c.ResolveRoots("/somewhere/else"); err != nil || len(m.pairs) != 1 || m.pairs[0].Local != "/mnt/code" {
		t.Fatalf("got %+v, %v", m, err)
	}
	res, err := c.Do(Request{Op: "text", Path: "/mnt/code/a.go"})
	if err != nil || res.Err != "" {
		t.Fatalf("read: %v %q", err, res.Err)
	}

	// search -path is a path operand like any other: the caller's spelling is
	// mapped, so it scopes the walk rather than being refused as outside the
	// editor's root.
	if _, err := c.DoStream(Request{Op: "search",
		Query: &SearchQuery{Text: "x", Path: "/mnt/code/internal"}}, nil); err != nil {
		t.Fatal(err)
	}
	ed.mu.Lock()
	got := ed.searchPath
	ed.mu.Unlock()
	if got != "/w/internal" {
		t.Errorf("search path = %q, want the editor spelling /w/internal", got)
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

// hello can declare a human, but only over the local socket. The control
// token is shared with agents in a container, so over TCP a caller could
// otherwise self-declare human and bypass the proposal gate: the token
// proves the caller reached the editor, not that it is the person at the
// keyboard. A TCP human request is downgraded to an agent — it still
// connects and works, just proposal-only.
func TestHelloOverTCPDowngradesAHuman(t *testing.T) {
	ed := newTCPEditor(t, map[string]string{"/w/a.go": "x\n"})
	t.Setenv(TokenEnv, ed.srv.Token())

	human, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer human.Close()
	res, err := human.Do(Request{Op: "hello", Identity: "client:desk", Name: "desk", Kind: string(KindHuman)})
	if err != nil || !res.OK {
		t.Fatalf("human hello: res=%+v err=%v", res, err)
	}
	if id := human.Author(); id == 0 || !ed.srv.Participants.IsAgent(id) {
		t.Errorf("a human request over TCP bound author %d, want an agent", id)
	}
	if res.Kind != string(KindAgent) {
		t.Errorf("a human request over TCP was granted kind %q, want %q", res.Kind, KindAgent)
	}
	if got := human.Kind(); got != KindAgent {
		t.Errorf("client kind after a TCP human hello = %q, want %q", got, KindAgent)
	}

	agent, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	res, err = agent.Do(Request{Op: "hello", Identity: "harness-1", Name: "claude"})
	if err != nil || !res.OK {
		t.Fatalf("agent hello: res=%+v err=%v", res, err)
	}
	if id := agent.Author(); !ed.srv.Participants.IsAgent(id) {
		t.Errorf("absent kind bound author %d, which does not read as an agent", id)
	}
	if res.Kind != string(KindAgent) {
		t.Errorf("an agent hello was granted kind %q, want %q", res.Kind, KindAgent)
	}
	if got := agent.Kind(); got != KindAgent {
		t.Errorf("client kind after an agent hello = %q, want %q", got, KindAgent)
	}
}

// Over the local socket the human is the person at the keyboard: the
// filesystem authorises the connection, so a requested human joins as one and
// its edits land accepted rather than proposed. The fixture is a control
// socket, and the transport is confirmed unix so the test cannot pass by
// accidentally driving a port instead.
func TestHelloOverTheSocketDeclaresAHuman(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "x\n"})
	if network, _ := ParseAddr(ed.srv.Path()); network != "unix" {
		t.Fatalf("fixture transport %q, want unix", network)
	}

	human, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer human.Close()
	res, err := human.Do(Request{Op: "hello", Identity: "client:desk", Name: "desk", Kind: string(KindHuman)})
	if err != nil || !res.OK {
		t.Fatalf("human hello: res=%+v err=%v", res, err)
	}
	if id := human.Author(); id == 0 || ed.srv.Participants.IsAgent(id) {
		t.Errorf("human hello over the socket bound author %d, which reads as an agent", id)
	}
	if res.Kind != string(KindHuman) {
		t.Errorf("a human hello over the socket was granted kind %q, want %q", res.Kind, KindHuman)
	}
	if got := human.Kind(); got != KindHuman {
		t.Errorf("client kind after a socket human hello = %q, want %q", got, KindHuman)
	}
}

// Every `raj ctl` call is a fresh connection. A provisional id must not leave a
// registry row behind, or a long-lived editor's who list fills with dead anon-N
// rows and, at the cap, durable identities start sharing recycled ids. N
// connections that bind one identity and leave must leave exactly one durable
// row — not one per connection — and that identity must bind the same author id
// on every connection.
func TestConnectionsDoNotLeakParticipants(t *testing.T) {
	ed := newTCPEditor(t, map[string]string{"/w/a.go": "x\n"})
	t.Setenv(TokenEnv, ed.srv.Token())

	const n = 50
	var id uint8
	for i := 0; i < n; i++ {
		c, err := Dial(ed.srv.Path())
		if err != nil {
			t.Fatal(err)
		}
		res, err := c.Do(Request{Op: "hello", Identity: "harness-abc", Name: "claude-1"})
		if err != nil || !res.OK {
			c.Close()
			t.Fatalf("hello %d: res=%+v err=%v", i, res, err)
		}
		if i == 0 {
			id = c.Author()
		} else if c.Author() != id {
			t.Errorf("connection %d bound to %d, want the stable %d", i, c.Author(), id)
		}
		c.Close()
	}

	// Every serve goroutine must have released or left its id by now. The
	// registry should hold the local human and the one durable identity — no
	// provisional row per connection, connected or not.
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := ed.srv.Participants.List()
		if len(got) == 2 && got[0].ID == LocalHuman && !got[1].Connected {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("after %d connections the registry holds %+v, want the human and one durable row", n, got)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A revert names the writer whose pieces to drop, but it can only ever be the
// connection's own author. The refusal lives on the reading goroutine, where
// the connection's real id is known rather than the claimed field, so a frame
// cannot impersonate a peer and discard their accepted text.
func TestRevertRefusesAForeignAuthor(t *testing.T) {
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
	own := res.Author
	res, err = c.Do(Request{Op: "revert", Path: "/w/a.go", Author: own + 1})
	if err != nil {
		t.Fatalf("revert: %v", err)
	}
	if !strings.Contains(res.Err, "revert discards only your own pieces") {
		t.Fatalf("a foreign-author revert was not refused: err=%q", res.Err)
	}
}

// Save is the user's gesture, so a frame must not be able to claim the human's
// id. The serve loop only stamps a zero author, so a raw client can send
// Author: LocalHuman; connection.one compares the claimed id with the
// connection's real one and refuses the imposture with the same wording
// Guard.Save uses. Precondition: a TCP connection the socket gate has already
// bound as an agent (TestHelloOverTCPDowngradesAHuman), carrying a save that
// names LocalHuman. Without the check in connection.one the request reaches
// the event thread and the fake records the save, so both the refusal
// assertion and the empty save log fail.
func TestSaveRefusesAClaimedHumanAuthor(t *testing.T) {
	ed := newTCPEditor(t, map[string]string{"/w/a.go": "x\n"})
	t.Setenv(TokenEnv, ed.srv.Token())
	c, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	res, err := c.Do(Request{Op: "save", Path: "/w/a.go", Author: LocalHuman})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if res.OK || res.Err == "" {
		t.Fatalf("a save claiming the human's id was admitted: %+v", res)
	}
	if res.Err != errSaveNotHuman {
		t.Errorf("refusal = %q, want the one save wording %q", res.Err, errSaveNotHuman)
	}
	if len(ed.saves) != 0 {
		t.Errorf("the spoofed save reached the event thread: %+v", ed.saves)
	}
}

// A program is the other way a claimed author can ride a save: Requests(prog,
// req.Author) builds each sub-request from the outer frame (or an author op),
// and a batch never passes the serve loop. connection.one runs the
// authenticity check on every sub-request, so a save inside a program is
// refused the same as a direct one. Preconditions: an agent connection, and a
// program whose save carries LocalHuman, either as the outer prog's author or
// via an author op. Without the check the sub-save reaches the event thread
// and the fake records it.
func TestSaveSpoofInsideAProgramIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ops   []prog.Op
		outer uint8
	}{
		{
			name:  "outer frame claims the human",
			ops:   []prog.Op{{Code: prog.OpPath, Payload: []byte("/w/a.go")}, {Code: prog.OpSave}},
			outer: LocalHuman,
		},
		{
			name: "author op claims the human",
			ops: []prog.Op{
				{Code: prog.OpPath, Payload: []byte("/w/a.go")},
				{Code: prog.OpAuthor, Payload: []byte{LocalHuman}},
				{Code: prog.OpSave},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ed := newTCPEditor(t, map[string]string{"/w/a.go": "x\n"})
			t.Setenv(TokenEnv, ed.srv.Token())
			c, err := Dial(ed.srv.Path())
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()

			res, err := c.Do(Request{Op: "prog", Program: prog.Encode(tc.ops), Author: tc.outer})
			if err != nil {
				t.Fatalf("prog: %v", err)
			}
			if res.OK || res.Err == "" {
				t.Fatalf("a save claiming the human's id inside a program was admitted: %+v", res)
			}
			if res.Err != errSaveNotHuman {
				t.Errorf("refusal = %q, want the one save wording %q", res.Err, errSaveNotHuman)
			}
			if len(ed.saves) != 0 {
				t.Errorf("the spoofed save reached the event thread: %+v", ed.saves)
			}
		})
	}
}

// A save with no author field is stamped from the connection, so the
// authenticity check compares it against itself and admits it; Guard.Save
// decides by kind (TestSaveRefusesNonHumans pins that a durable joined human
// is admitted and an agent refused). This drives the real serve loop and
// connection.one and would fail if the new check compared against anything but
// the connection's own id. Precondition: a Unix-socket connection helloed as a
// durable human (TestHelloOverTheSocketDeclaresAHuman) and a save with no
// Author. The event thread records the connection's own id, not LocalHuman.
func TestSaveWithNoAuthorIsStampedFromTheConnection(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "x\n"})
	if network, _ := ParseAddr(ed.srv.Path()); network != "unix" {
		t.Fatalf("fixture transport %q, want unix", network)
	}

	c, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	hi, err := c.Do(Request{Op: "hello", Identity: "client:desk", Name: "desk",
		Kind: string(KindHuman)})
	if err != nil || !hi.OK {
		t.Fatalf("hello as human: res=%+v err=%v", hi, err)
	}
	human := c.Author()
	if human == 0 || human == LocalHuman || ed.srv.Participants.IsAgent(human) {
		t.Fatalf("the human hello bound author %d, want a durable human", human)
	}

	res, err := c.Do(Request{Op: "save", Path: "/w/a.go"})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if !res.OK {
		t.Fatalf("an ordinary human save was refused: %+v", res)
	}
	if len(ed.saves) != 1 {
		t.Fatalf("saves = %d, want the one save at the event thread", len(ed.saves))
	}
	if got := ed.saves[0].Author; got != human {
		t.Errorf("save author = %d, want the connection's own %d", got, human)
	}
}

// An approve is the user's answer, so a frame must not be able to claim the
// human's id for it any more than for a save. The serve loop only stamps a zero
// author, so a raw client can send Author: LocalHuman; connection.one compares
// the claimed id with the connection's real one and refuses. Precondition: a
// TCP connection the socket gate has already bound as an agent, carrying a
// delete that both claims LocalHuman and sets Approve. Without the check the
// request reaches the event thread and the fake records the delete, so both the
// refusal assertion and the empty log fail.
func TestDeleteApproveRefusesAClaimedHumanAuthor(t *testing.T) {
	ed := newTCPEditor(t, map[string]string{"/w/a.go": "x\n"})
	t.Setenv(TokenEnv, ed.srv.Token())
	c, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	res, err := c.Do(Request{Op: "delete", Path: "/w/a.go", Author: LocalHuman, Approve: true})
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if res.OK || res.Err == "" {
		t.Fatalf("a delete approve claiming the human's id was admitted: %+v", res)
	}
	if res.Err != errApproveDeletionNotHuman {
		t.Errorf("refusal = %q, want the removal-gate wording %q", res.Err, errApproveDeletionNotHuman)
	}
	if ed.lastDelete.Op != "" {
		t.Errorf("the spoofed approve reached the event thread: %+v", ed.lastDelete)
	}
}

// The dir-removal variant carries its own wording and the same chokepoint.
func TestRmdirApproveRefusesAClaimedHumanAuthor(t *testing.T) {
	ed := newTCPEditor(t, map[string]string{"/w/a.go": "x\n"})
	t.Setenv(TokenEnv, ed.srv.Token())
	c, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	res, err := c.Do(Request{Op: "rmdir", Path: "/w/pkg", Author: LocalHuman, Approve: true})
	if err != nil {
		t.Fatalf("rmdir: %v", err)
	}
	if res.OK || res.Err == "" {
		t.Fatalf("an rmdir approve claiming the human's id was admitted: %+v", res)
	}
	if res.Err != errApproveDirNotHuman {
		t.Errorf("refusal = %q, want the dir removal-gate wording %q", res.Err, errApproveDirNotHuman)
	}
	if ed.lastRmdir.Op != "" {
		t.Errorf("the spoofed dir approve reached the event thread: %+v", ed.lastRmdir)
	}
}

// A withdraw runs as the connection's own writer, so a frame must not name a
// peer's id to retract their proposal: the host's owner check trusts the
// claimed author, and before this check in connection.one any connection could
// retract another writer's pending removal. Precondition: a TCP connection the
// socket gate has already bound as an agent, carrying a delete or rmdir with
// Withdraw set and Author set to another writer's id. Without the check the
// request reaches the event thread and the fake records it, so both the
// refusal assertion and the empty log fail.
func TestDeleteWithdrawRefusesAClaimedAuthor(t *testing.T) {
	for _, tc := range []struct{ op, path string }{
		{"delete", "/w/a.go"},
		{"rmdir", "/w/pkg"},
	} {
		t.Run(tc.op, func(t *testing.T) {
			ed := newTCPEditor(t, map[string]string{"/w/a.go": "x\n"})
			t.Setenv(TokenEnv, ed.srv.Token())
			c, err := Dial(ed.srv.Path())
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()

			res, err := c.Do(Request{Op: tc.op, Path: tc.path, Author: FirstAgent + 7, Withdraw: true})
			if err != nil {
				t.Fatalf("%s: %v", tc.op, err)
			}
			if res.OK || res.Err == "" {
				t.Fatalf("a %s withdraw claiming another author was admitted: %+v", tc.op, res)
			}
			if res.Err != errWithdrawSpoof {
				t.Errorf("refusal = %q, want the withdraw wording %q", res.Err, errWithdrawSpoof)
			}
			if ed.lastDelete.Op != "" || ed.lastRmdir.Op != "" {
				t.Errorf("the spoofed withdraw reached the event thread: delete=%+v rmdir=%+v",
					ed.lastDelete, ed.lastRmdir)
			}

			// With no claimed author the frame is stamped from the connection
			// and admitted, so the check refuses only the imposture.
			ok, err := c.Do(Request{Op: tc.op, Path: tc.path, Withdraw: true})
			if err != nil {
				t.Fatalf("%s (no author): %v", tc.op, err)
			}
			if !ok.OK {
				t.Fatalf("a %s withdraw with no claimed author was refused: %+v", tc.op, ok)
			}
			if got := ok.Author; got != c.Author() {
				t.Errorf("withdraw author = %d, want the connection's own %d", got, c.Author())
			}
		})
	}
}

// Every verb acts as its connection. The author a frame carries is a claim, not
// the connection, so connection.one refuses a request that names another writer
// before any handler sees it. save, revert, approve and withdraw keep their own
// contract wordings (pinned in their tests); these are the write and decision
// verbs that used to trust req.Author, and errAuthorSpoof is their one refusal.
func TestWriteAndDecisionVerbsRefuseAClaimedAuthor(t *testing.T) {
	for _, op := range []string{"apply", "accept", "reject", "patch"} {
		t.Run(op, func(t *testing.T) {
			ed := newTCPEditor(t, map[string]string{"/w/a.go": "x\n"})
			t.Setenv(TokenEnv, ed.srv.Token())
			c, err := Dial(ed.srv.Path())
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()

			res, err := c.Do(Request{Op: op, Path: "/w/a.go", Group: 4, Author: FirstAgent + 7})
			if err != nil {
				t.Fatalf("%s: %v", op, err)
			}
			if res.OK || res.Err != errAuthorSpoof {
				t.Fatalf("%s claiming another author = %+v, want %q", op, res, errAuthorSpoof)
			}
			if len(ed.decisions) != 0 {
				t.Errorf("the spoofed %s reached the handler: %+v", op, ed.decisions)
			}
			if got := ed.docs["/w/a.go"]; got != "x\n" {
				t.Errorf("the spoofed %s touched the buffer: %q", op, got)
			}
		})
	}
}

// A program sub-request is the other way a claimed author rides a verb: the
// outer prog frame names the author and Requests stamps it onto every verb. A
// batch never passes the serve loop's checks, so connection.one refuses each
// sub-request that names another writer.
func TestProgramSubRequestRefusesAClaimedAuthor(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ops   []prog.Op
		outer uint8
	}{
		{
			name:  "outer frame claims the human",
			ops:   []prog.Op{{Code: prog.OpPath, Payload: []byte("/w/a.go")}, {Code: prog.OpApply}},
			outer: LocalHuman,
		},
		{
			name: "author op claims the human",
			ops: []prog.Op{
				{Code: prog.OpPath, Payload: []byte("/w/a.go")},
				{Code: prog.OpAuthor, Payload: []byte{LocalHuman}},
				{Code: prog.OpApply},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ed := newTCPEditor(t, map[string]string{"/w/a.go": "x\n"})
			t.Setenv(TokenEnv, ed.srv.Token())
			c, err := Dial(ed.srv.Path())
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()

			res, err := c.Do(Request{Op: "prog", Program: prog.Encode(tc.ops), Author: tc.outer})
			if err != nil {
				t.Fatalf("prog: %v", err)
			}
			if res.OK || res.Err != errAuthorSpoof {
				t.Fatalf("an apply claiming the human inside a program = %+v, want %q", res, errAuthorSpoof)
			}
			if got := ed.docs["/w/a.go"]; got != "x\n" {
				t.Errorf("the spoofed sub-request touched the buffer: %q", got)
			}
		})
	}
}

// A request that names no author is stamped from the connection by the serve
// loop and runs as it; the chokepoint refuses only a positive other claim.
func TestRequestWithNoAuthorRunsAsTheConnection(t *testing.T) {
	ed := newTCPEditor(t, map[string]string{"/w/a.go": "x\n"})
	t.Setenv(TokenEnv, ed.srv.Token())
	c, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	ver, err := c.Do(Request{Op: "version", Path: "/w/a.go"})
	if err != nil || !ver.OK {
		t.Fatalf("version: %v %+v", err, ver)
	}
	base := ver.Version
	res, err := c.Do(Request{Op: "apply", Path: "/w/a.go", Base: &base,
		Hunks: []Hunk{{Start: 0, End: 0, Text: "y"}}})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !res.OK {
		t.Fatalf("an apply with no author was refused: %+v", res)
	}
	if got := ed.docs["/w/a.go"]; got != "yx\n" {
		t.Errorf("apply landed as %q, want the edit made by the connection", got)
	}
}

// Accept is the person's decision; reject is not. An agent that accepts is
// refused with the same humanAuthor predicate save uses, while an agent may
// still reject its own set and a peer's.
func TestDispatchAcceptIsTheUsersDecision(t *testing.T) {
	g, h := guarded(t)
	path := filepath.Join(h.root, "a.go")
	reg := NewRegistry()
	g.Participants = reg
	agent, err := reg.Join("raj-aaaa0001", "alpha", KindAgent)
	if err != nil {
		t.Fatal(err)
	}
	human, err := reg.Join("client:desk", "desk", KindHuman)
	if err != nil {
		t.Fatal(err)
	}

	h.groups = []Group{{ID: 4, Path: path, State: "proposed", Author: agent}}
	res := Dispatch(g, Request{Op: "accept", Path: path, Group: 4, Author: agent})
	if res.OK || res.Err != errAcceptNotHuman {
		t.Fatalf("an agent's accept = %+v, want %q", res, errAcceptNotHuman)
	}
	if h.groups[0].State != "proposed" {
		t.Errorf("a refused accept moved the set to %q", h.groups[0].State)
	}

	// The local keyboard row is the person, so its accept lands the set.
	if res := Dispatch(g, Request{Op: "accept", Path: path, Group: 4, Author: LocalHuman}); !res.OK {
		t.Fatalf("the local human's accept was refused: %+v", res)
	}
	if h.groups[0].State != "accepted" {
		t.Errorf("the accepted set is %q, want accepted", h.groups[0].State)
	}

	// A durable joined human (an attached client) accepts as its own person.
	h.groups[0].State = "proposed"
	if res := Dispatch(g, Request{Op: "accept", Path: path, Group: 4, Author: human}); !res.OK {
		t.Fatalf("a joined human's accept was refused: %+v", res)
	}
	if h.groups[0].State != "accepted" {
		t.Errorf("the joined human's accepted set is %q, want accepted", h.groups[0].State)
	}
}

func TestDispatchRejectStaysOpenToAgents(t *testing.T) {
	g, h := guarded(t)
	path := filepath.Join(h.root, "a.go")
	reg := NewRegistry()
	g.Participants = reg
	agent, err := reg.Join("raj-aaaa0001", "alpha", KindAgent)
	if err != nil {
		t.Fatal(err)
	}
	peer, err := reg.Join("raj-bbbb0002", "beta", KindAgent)
	if err != nil {
		t.Fatal(err)
	}

	// Reject backs an agent's own set out and a peer's alike: it leaves the
	// text pending, so it is not the human's accept decision.
	h.groups = []Group{
		{ID: 4, Path: path, State: "proposed", Author: agent},
		{ID: 5, Path: path, State: "proposed", Author: peer},
	}
	for _, group := range []uint64{4, 5} {
		res := Dispatch(g, Request{Op: "reject", Path: path, Group: group, Author: agent})
		if !res.OK {
			t.Fatalf("an agent's reject of set %d was refused: %+v", group, res)
		}
	}
	for _, grp := range h.groups {
		if grp.State != "rejected" {
			t.Errorf("group %d state = %q, want rejected", grp.ID, grp.State)
		}
	}
}

// The Unix socket is the local human's trust boundary: the user's own
// `raj ctl accept` from a shell arrives there undeclared and must accept as
// the person, while a connection that registered as an agent keeps its own
// identity (and the Guard then refuses its accept). Without the rewrite an
// undeclared local accept reaches the host as a provisional agent id and the
// human-only accept gate refuses the user's own command.
func TestUnixUndeclaredAcceptRunsAsTheLocalHuman(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "x\n"})
	lastAccept := func() Request {
		t.Helper()
		ed.mu.Lock()
		defer ed.mu.Unlock()
		if len(ed.decisions) == 0 {
			t.Fatal("no accept reached the editor")
		}
		return ed.decisions[len(ed.decisions)-1]
	}

	anon, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer anon.Close()
	if _, err := anon.Do(Request{Op: "accept", Path: "/w/a.go", Group: 1}); err != nil {
		t.Fatal(err)
	}
	if got := lastAccept().Author; got != LocalHuman {
		t.Errorf("undeclared unix accept ran as author %d, want the local human %d", got, LocalHuman)
	}

	agent, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	if hi, err := agent.Do(Request{Op: "hello", Identity: "raj-local-agent"}); err != nil || !hi.OK {
		t.Fatalf("hello: %v %+v", err, hi)
	}
	if _, err := agent.Do(Request{Op: "accept", Path: "/w/a.go", Group: 1}); err != nil {
		t.Fatal(err)
	}
	if got := lastAccept().Author; got != agent.Author() || got == LocalHuman {
		t.Errorf("a registered agent's unix accept ran as author %d, want its own %d", got, agent.Author())
	}

}

// A program sub-request whose OpAuthor is zero is stamped with the
// connection's own author, like a direct frame: zero is AuthorOriginal, and a
// verb run as it would write text that reads as the file's own. Precondition:
// a TCP agent sending a program that names author 0 before an accept. Without
// the stamp in connection.one the accept reaches the editor as author 0.
func TestProgramZeroAuthorIsStampedFromTheConnection(t *testing.T) {
	ed := newTCPEditor(t, map[string]string{"/w/a.go": "x\n"})
	t.Setenv(TokenEnv, ed.srv.Token())
	c, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ops := []prog.Op{
		{Code: prog.OpPath, Payload: []byte("/w/a.go")},
		{Code: prog.OpGroup, Payload: prog.Number(1)},
		{Code: prog.OpAuthor, Payload: []byte{0}},
		{Code: prog.OpAccept},
	}
	if _, err := c.Do(Request{Op: "prog", Program: prog.Encode(ops)}); err != nil {
		t.Fatalf("prog: %v", err)
	}
	ed.mu.Lock()
	defer ed.mu.Unlock()
	if len(ed.decisions) == 0 {
		t.Fatal("the program's accept never reached the editor")
	}
	if got := ed.decisions[len(ed.decisions)-1].Author; got == 0 || got != c.Author() {
		t.Errorf("program accept ran as author %d, want the connection's %d", got, c.Author())
	}
}

// A connection that says hello twice under the same identity — the CLI's
// bind-first hello, then recv's or register's own — must still leave the
// participant disconnected when it closes. Registry counts connections, so an
// unbalanced second Join kept finished agents listed as connected forever,
// which `who --live`, `send --to all` and raj-cycle's "wait for the agents to
// come back" all read.
func TestRepeatHelloOnOneConnectionDoesNotLeakPresence(t *testing.T) {
	ed := newFakeEditor(t, map[string]string{"/w/a.go": "x\n"})
	c, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if hi, err := c.Do(Request{Op: "hello", Identity: "raj-twice", Name: "twice"}); err != nil || !hi.OK {
			t.Fatalf("hello %d: %v %+v", i, err, hi)
		}
	}
	id := c.Author()
	if p, _ := ed.srv.Participants.Get(id); !p.Connected {
		t.Fatal("not connected after hello")
	}
	c.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if p, _ := ed.srv.Participants.Get(id); !p.Connected {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("a closed connection that said hello twice still reads as connected")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The derived state across a real socket: a recent request reads working, and
// a parked recv after activeWindow with no other request reads listening. The
// clock is injected so the window moves without a sleep.
func TestDerivedListeningOverASocket(t *testing.T) {
	ed := newTCPEditor(t, map[string]string{"/w/a.go": "x\n"})
	t.Setenv(TokenEnv, ed.srv.Token())
	var clock int64
	ed.srv.Participants.now = func() time.Time { return time.Unix(0, atomic.LoadInt64(&clock)) }

	c, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	if res, err := c.Do(Request{Op: "hello", Identity: "raj-worker", Name: "claude"}); err != nil || !res.OK {
		t.Fatalf("hello: %v %+v", err, res)
	}
	id := c.Author()

	// A recent request keeps the participant working.
	if res, err := c.Do(Request{Op: "ping"}); err != nil || !res.OK {
		t.Fatalf("ping: %v %+v", err, res)
	}
	if got := rosterState(t, ed.srv, id); got != StateWorking {
		t.Fatalf("recent request = %q, want working", got)
	}

	// Park a recv, then move the clock past activeWindow. With no later request
	// the parked recv alone reads listening.
	done := make(chan error, 1)
	go func() {
		_, err := c.Do(Request{Op: "recv"})
		done <- err
	}()
	waitListening(t, ed.srv, id)
	atomic.StoreInt64(&clock, int64(activeWindow+time.Second))
	if got := rosterState(t, ed.srv, id); got != StateListening {
		t.Fatalf("parked recv = %q, want listening", got)
	}

	c.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("closing the client did not end the parked recv")
	}
}

// rosterState returns id's derived+declared state, or fails.
func rosterState(t *testing.T, s *Server, id uint8) string {
	t.Helper()
	for _, p := range s.Roster() {
		if p.ID == id {
			return p.State
		}
	}
	t.Fatalf("participant %d missing from the roster", id)
	return ""
}

// waitListening waits until a recv is parked for id, which is the fact the
// derived listening state reads.
func waitListening(t *testing.T, s *Server, id uint8) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.Participants.mu.Lock()
		n := s.Participants.listening[id]
		s.Participants.mu.Unlock()
		if n > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("recv never parked for participant %d", id)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Guard.Land accepts the task's pending sets in every buffer and saves each
// buffer whose pending sets all belong to that task; a buffer that also holds
// another task's pending set is held, not saved, and its task's sets are still
// accepted. The report names each outcome, so a buffer a save refused for
// another reason is never silent.
func TestDispatchLandAcceptsByTaskAndReports(t *testing.T) {
	g, h := guarded(t)
	h.docs["/w/b.go"] = "b\n"
	h.vers["/w/b.go"] = 1
	h.groupsByPath = map[string][]Group{
		"/w/a.go": {{ID: 1, Path: "/w/a.go", State: "proposed", Task: "task-1", Author: 2}},
		"/w/b.go": {
			{ID: 2, Path: "/w/b.go", State: "proposed", Task: "task-1", Author: 2},
			{ID: 3, Path: "/w/b.go", State: "proposed", Task: "task-2", Author: 2},
		},
	}
	res := Dispatch(g, Request{Op: "land", LandTask: "task-1", Author: LocalHuman})
	if !res.OK {
		t.Fatalf("land: %+v", res)
	}
	byPath := map[string]LandFile{}
	for _, f := range res.Land {
		byPath[f.Path] = f
	}
	if f := byPath["/w/a.go"]; !f.Saved || f.Sets != 1 {
		t.Errorf("a.go = %+v, want one set saved", f)
	}
	if f := byPath["/w/b.go"]; !f.Held || f.Saved || f.Sets != 1 {
		t.Errorf("b.go = %+v, want one set accepted and held", f)
	}
	if h.groupsByPath["/w/a.go"][0].State != "accepted" {
		t.Errorf("a.go task-1 set was not accepted")
	}
	if h.groupsByPath["/w/b.go"][0].State != "accepted" {
		t.Errorf("b.go task-1 set was not accepted even though the buffer is held")
	}
	if h.groupsByPath["/w/b.go"][1].State != "proposed" {
		t.Errorf("the foreign task-2 set moved")
	}
	if h.saves != 1 {
		t.Errorf("saves = %d, want only the fully landed buffer", h.saves)
	}
}

// A buffer whose pending sets are all the task's is saved, but a save the
// editor refuses for another reason is named in the report rather than
// silently skipped: the sets are still accepted, and the caller learns why the
// buffer did not reach disk.
func TestDispatchLandNamesARefusedSave(t *testing.T) {
	g, h := guarded(t)
	path := filepath.Join(h.root, "a.go")
	h.groupsByPath = map[string][]Group{
		path: {{ID: 1, Path: path, State: "proposed", Task: "task-1", Author: 2}},
	}
	h.saveErr = map[string]string{path: "disk changed"}
	res := Dispatch(g, Request{Op: "land", LandTask: "task-1", Author: LocalHuman})
	if !res.OK {
		t.Fatalf("land: %+v", res)
	}
	if len(res.Land) != 1 {
		t.Fatalf("land report = %+v, want one buffer", res.Land)
	}
	f := res.Land[0]
	if f.Saved || f.Held || f.Err != "disk changed" {
		t.Errorf("report = %+v, want the refused save named and not saved", f)
	}
	if h.groupsByPath[path][0].State != "accepted" {
		t.Errorf("the refused save did not leave the sets accepted")
	}
}

// A wave whose overlaps form a merge is a non-linear dependency: a set inherits
// from two others, so no order can be derived. The land reports it rather than
// guessing one; the merge is group 3.
func TestDispatchLandReportsANonLinearDependency(t *testing.T) {
	g, h := guarded(t)
	h.groupsByPath = map[string][]Group{
		"/w/a.go": {
			{ID: 1, Path: "/w/a.go", State: "proposed", Task: "task-1", Author: 2,
				Overlaps: &GroupOverlaps{Sets: []GroupOverlap{{Group: 3, Author: 2}}}},
			{ID: 2, Path: "/w/a.go", State: "proposed", Task: "task-1", Author: 2,
				Overlaps: &GroupOverlaps{Sets: []GroupOverlap{{Group: 3, Author: 2}}}},
			{ID: 3, Path: "/w/a.go", State: "proposed", Task: "task-1", Author: 2,
				Overlaps: &GroupOverlaps{Sets: []GroupOverlap{{Group: 1, Author: 2}, {Group: 2, Author: 2}}}},
		},
	}
	res := Dispatch(g, Request{Op: "land", LandTask: "task-1", Author: LocalHuman})
	if !res.OK {
		t.Fatalf("land: %+v", res)
	}
	if !strings.Contains(res.HookJSON, "non-linear") {
		t.Errorf("land export = %q, want a non-linear dependency report", res.HookJSON)
	}
}

// land is the human's own gesture -- accept by task and save -- so an agent
// connection that asks for it is refused before any set is accepted or any
// buffer saved, with the same humanAuthor predicate save uses.
func TestLandRefusesAnAgentOverTCP(t *testing.T) {
	ed := newTCPEditor(t, map[string]string{"/w/a.go": "x\n"})
	t.Setenv(TokenEnv, ed.srv.Token())
	ed.policyMem.docs = map[string]string{"/w/a.go": "x\n"}
	ed.policyMem.vers = map[string]uint64{"/w/a.go": 1}
	ed.policyMem.groupsByPath = map[string][]Group{
		"/w/a.go": {{ID: 1, Path: "/w/a.go", State: "proposed", Task: "task-1", Author: 2}},
	}
	c, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	res, err := c.Do(Request{Op: "land", LandTask: "task-1"})
	if err != nil {
		t.Fatalf("land: %v", err)
	}
	if res.OK || res.Err != errLandNotHuman {
		t.Fatalf("an agent's land = %+v, want %q", res, errLandNotHuman)
	}
	if ed.policyMem.groupsByPath["/w/a.go"][0].State != "proposed" {
		t.Errorf("the refused land accepted a set")
	}
	if ed.policyMem.saves != 0 {
		t.Errorf("the refused land reached the host: %d save(s)", ed.policyMem.saves)
	}
}

// A crafted frame whose second hunk length is near 2^63 must not crash the
// server. Frame.Split tested off+n > len(f.Body); off+n wraps negative for a
// large enough n, so the test passed and the slice below panicked. The frame
// is sent without a token on purpose: DecodeRequest runs before the token
// check, which is why the crash needed no credentials. After the fix the frame
// is answered with an error and the same connection, and the server, carries
// on.
func TestServerSurvivesAnOverflowingHunkLength(t *testing.T) {
	ed := newTCPEditor(t, map[string]string{"/w/a.go": "x\n"})
	_, addr := ParseAddr(ed.srv.Path())
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// Two hunks: the first consumes two body bytes so off==2, the second names
	// a length that makes off+n wrap. 2^63-2 is the largest positive value the
	// varint carries (prog.Varint).
	bad := Header{ID: 7, Op: "apply", Hunks: []HunkMeta{
		{Start: 0, End: 0, Len: 2},
		{Start: 0, End: 0, Len: math.MaxInt64 - 1},
	}}
	if err := WriteFrame(conn, bad, []byte{0, 0}); err != nil {
		t.Fatalf("writing the crafted frame: %v", err)
	}
	f, err := ReadFrame(conn)
	if err != nil {
		t.Fatalf("the server did not answer the crafted frame: %v", err)
	}
	res, err := DecodeResponse(f)
	if err != nil {
		t.Fatal(err)
	}
	if res.ID != 7 || res.Err == "" {
		t.Fatalf("answer to the crafted frame = %+v, want an error for id 7", res)
	}

	// The connection and the server are still alive: a valid request on the
	// same socket is answered.
	h, body := EncodeRequest(Request{ID: 8, Op: "ping", Token: ed.srv.Token()})
	if err := WriteFrame(conn, h, body); err != nil {
		t.Fatal(err)
	}
	f, err = ReadFrame(conn)
	if err != nil {
		t.Fatalf("the server stopped serving after the crafted frame: %v", err)
	}
	res, err = DecodeResponse(f)
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || res.ID != 8 {
		t.Fatalf("ping after the crafted frame = %+v, want OK", res)
	}
}

// The human row is seeded with identity "local". A TCP caller that says hello
// as "local" must be refused: joining the existing row returns author 1, and
// author 1 passes the human gate for save, accept and land. Before the fix the
// hello returns OK and the connection writes as the local human.
func TestHelloOverTCPRefusesTheReservedLocalIdentity(t *testing.T) {
	ed := newTCPEditor(t, map[string]string{"/w/a.go": "x\n"})
	t.Setenv(TokenEnv, ed.srv.Token())
	c, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	res, err := c.Do(Request{Op: "hello", Identity: "local", Name: "impostor"})
	if err != nil {
		t.Fatalf("hello: %v", err)
	}
	if res.OK || res.Err == "" {
		t.Fatalf("a TCP hello as the reserved identity succeeded: %+v", res)
	}
	if id := c.Author(); id == LocalHuman {
		t.Fatalf("the connection bound the local human author %d", id)
	}
	// The human row is still the local keyboard, connected.
	if p, ok := ed.srv.Participants.Get(LocalHuman); !ok || !p.Connected {
		t.Fatalf("the local human row = %+v ok=%v, want it connected and untouched", p, ok)
	}
}

// A durable human row is not joinable from TCP either, even under an identity
// that is not reserved: joining the row returns its author id and passes the
// human gate. The row is created over the local socket, which is the human
// trust boundary; the same identity over TCP must be refused.
func TestHelloOverTCPCannotJoinAHumanRow(t *testing.T) {
	sock := controlSock(t, "human.sock")
	ed := newFakeEditorAddrs(t, []string{sock, "tcp://127.0.0.1:0"},
		map[string]string{"/w/a.go": "x\n"})
	paths := ed.srv.Paths()
	if len(paths) != 2 {
		t.Fatalf("Paths() = %v, want two addresses", paths)
	}

	desk, err := Dial(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	defer desk.Close()
	hi, err := desk.Do(Request{Op: "hello", Identity: "client:desk", Name: "desk",
		Kind: string(KindHuman)})
	if err != nil || !hi.OK {
		t.Fatalf("human hello over the socket: res=%+v err=%v", hi, err)
	}
	humanID := desk.Author()
	if humanID == 0 || ed.srv.Participants.IsAgent(humanID) {
		t.Fatalf("the socket human bound author %d, which reads as an agent", humanID)
	}

	t.Setenv(TokenEnv, ed.srv.Token())
	remote, err := Dial(paths[1])
	if err != nil {
		t.Fatal(err)
	}
	defer remote.Close()
	res, err := remote.Do(Request{Op: "hello", Identity: "client:desk", Name: "desk"})
	if err != nil {
		t.Fatalf("hello: %v", err)
	}
	if res.OK || res.Err == "" {
		t.Fatalf("a TCP hello joined the human row: %+v", res)
	}
	if id := remote.Author(); id == humanID {
		t.Fatalf("the TCP connection bound the human author %d", id)
	}
	if p, ok := ed.srv.Participants.Get(humanID); !ok || ed.srv.Participants.IsAgent(humanID) || !p.Connected {
		t.Fatalf("the human row = %+v ok=%v, want a connected human", p, ok)
	}
}

// A crafted frame must not take other connections down with it. The overflow
// test above proves the server and the offending connection carry on; this pins
// the isolation the per-connection recover exists for: a connection already in
// conversation and one opened afterwards both keep serving. Before the fix the
// panic ran on the reading goroutine with no recover, so it killed the whole
// process and every connection died with it.
func TestCraftedFrameLeavesOtherConnectionsServing(t *testing.T) {
	ed := newTCPEditor(t, map[string]string{"/w/a.go": "x\n"})
	_, addr := ParseAddr(ed.srv.Path())
	t.Setenv(TokenEnv, ed.srv.Token())

	// A bystander bound and used before the crafted frame arrives.
	bystander, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer bystander.Close()
	if res, err := bystander.Do(Request{Op: "ping"}); err != nil || !res.OK {
		t.Fatalf("bystander ping before the crafted frame: %v %+v", err, res)
	}

	bad, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer bad.Close()
	// The same crafted frame as TestServerSurvivesAnOverflowingHunkLength: the
	// second hunk length makes off+n wrap in the unfixed Split, so the bounds
	// test passed and the slice below panicked on the reading goroutine.
	header := Header{ID: 7, Op: "apply", Hunks: []HunkMeta{
		{Start: 0, End: 0, Len: 2},
		{Start: 0, End: 0, Len: math.MaxInt64 - 1},
	}}
	if err := WriteFrame(bad, header, []byte{0, 0}); err != nil {
		t.Fatalf("writing the crafted frame: %v", err)
	}
	f, err := ReadFrame(bad)
	if err != nil {
		t.Fatalf("the server did not answer the crafted frame: %v", err)
	}
	res, err := DecodeResponse(f)
	if err != nil {
		t.Fatal(err)
	}
	if res.ID != 7 || res.Err == "" {
		t.Fatalf("answer to the crafted frame = %+v, want an error for id 7", res)
	}

	// The connection that was already open is untouched by the other one's bad
	// frame; the server is still accepting, so a client that connects now is
	// served too.
	if res, err := bystander.Do(Request{Op: "ping"}); err != nil || !res.OK {
		t.Fatalf("bystander ping after the crafted frame: %v %+v", err, res)
	}
	fresh, err := Dial(ed.srv.Path())
	if err != nil {
		t.Fatalf("the server stopped accepting after the crafted frame: %v", err)
	}
	defer fresh.Close()
	if res, err := fresh.Do(Request{Op: "ping"}); err != nil || !res.OK {
		t.Fatalf("fresh connection ping after the crafted frame: %v %+v", err, res)
	}
}

// failWriteListener hands the server a real TCP connection whose writes fail at
// once. A real socket can absorb every frame of a small response before it
// notices the peer is gone, so the writer's first failure has to be injected to
// be deterministic; the read path and the request are still the shipped ones.
type failWriteListener struct{ net.Listener }

func (l failWriteListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return failWriteConn{Conn: c}, nil
}

type failWriteConn struct{ net.Conn }

var errTestWriteFailed = errors.New("test: write failed")

func (c failWriteConn) Write([]byte) (int, error) { return 0, errTestWriteFailed }

// A connection whose socket has failed must not wedge its handler. The writer
// used to return on the first WriteFrame error, so a handler still emitting
// frames blocked on the out channel, wg.Wait never returned, and the connection
// never released its author id. With the writer draining past the failure, the
// handler finishes and the id comes back.
func TestAFailedWriterKeepsDraining(t *testing.T) {
	ed := newTCPEditor(t, map[string]string{"/w/a.go": "x\n"})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	wrapped := failWriteListener{Listener: ln}
	ed.srv.lns = append(ed.srv.lns, wrapped)
	go ed.srv.accept(wrapped, "tcp")

	raw, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()

	// A program of pings: one frame each, well past the writer's 64-frame
	// buffer, so a writer that stops draining leaves the program blocked in
	// send. The connection is authenticated but not bound, so its id is a
	// reservation with no row; only teardown frees it.
	ops := make([]prog.Op, 200)
	for i := range ops {
		ops[i] = prog.Op{Code: prog.OpPing}
	}
	h, body := EncodeRequest(Request{Op: "prog", Program: prog.Encode(ops), Token: ed.srv.Token()})
	if err := WriteFrame(raw, h, body); err != nil {
		t.Fatal(err)
	}

	// Wait until the whole program has run. Only a writer that keeps draining
	// past the failed socket lets all 200 replies out; one that returned on the
	// first write error leaves the program blocked on the 64-frame out channel,
	// so this wait times out with the handler wedged in send.
	completed := false
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(2 * time.Millisecond) {
		ed.mu.Lock()
		n := len(ed.authors)
		ed.mu.Unlock()
		if n >= 200 {
			completed = true
			break
		}
	}
	if !completed {
		ed.mu.Lock()
		n := len(ed.authors)
		ed.mu.Unlock()
		t.Fatalf("the program stalled after %d of 200 pings: a writer that stops draining on a failed socket wedges the handler in send", n)
	}

	// A completed program means the handler returned; teardown must then free
	// the provisional id.
	raw.Close()
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(2 * time.Millisecond) {
		id, rerr := ed.srv.Participants.Reserve()
		if rerr != nil {
			t.Fatal(rerr)
		}
		ed.srv.Participants.Release(id)
		if id == FirstAgent {
			return
		}
	}
	t.Fatal("a connection whose writer failed did not tear down: its author id is still reserved after the peer left; a handler is wedged in send")
}

// An unauthenticated connection must not consume an author id. serve used to
// reserve one at accept, so an idle TCP peer — one that connects and never
// sends a frame — held a byte of the one-byte space until it disconnected; a
// few hundred of them refuse every real writer. The reservation now waits for
// the token check, so the first free id is still FirstAgent afterward.
func TestAnIdleUnauthenticatedConnectionDoesNotBurnAnAuthorID(t *testing.T) {
	ed := newTCPEditor(t, map[string]string{"/w/a.go": "x\n"})
	_, addr := ParseAddr(ed.srv.Path())

	idle, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer idle.Close()
	// The server accepts on its own goroutine; give it a moment to take the
	// idle connection before probing, so the test is about the reservation and
	// not about accept ordering.
	time.Sleep(200 * time.Millisecond)

	id, err := ed.srv.Participants.Reserve()
	if err != nil {
		t.Fatal(err)
	}
	ed.srv.Participants.Release(id)
	if id != FirstAgent {
		t.Fatalf("an idle unauthenticated connection reserved author id %d: an attacker can burn the one-byte space without the token", id)
	}
}

// A request the caller was told had timed out must not run later. submit used
// to answer the timeout and leave the request parked, so the event thread took
// and executed it afterwards — an apply reported as failed would still land.
// This server has no event thread, so the request is still parked when the
// deadline passes; the test then plays the event thread and shows Take hands
// nothing back.
func TestATimedOutRequestDoesNotRunLater(t *testing.T) {
	srv, err := ListenAll([]string{"tcp://127.0.0.1:0"}, func() {})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	srv.mu.Lock()
	srv.replyTimeout = 100 * time.Millisecond
	srv.mu.Unlock()
	t.Setenv(TokenEnv, srv.Token())

	c, err := Dial(srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	res, err := c.Do(Request{Op: "ping"})
	if err != nil {
		t.Fatalf("ping: %v", err)
	}
	if !strings.Contains(res.Err, "timed out waiting for the editor") {
		t.Fatalf("reply = %+v, want the reply timeout", res)
	}
	// The event thread's only chance to run it is Take; it must not be there.
	if took := srv.Take(); len(took) != 0 {
		t.Fatalf("the timed-out request is still parked and would execute later: %+v", took[0].Req)
	}
}
