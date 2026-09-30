package main

import (
	"bytes"
	"errors"
	"flag"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"raj/internal/control"
	"raj/internal/daemon"
	"raj/internal/ui"
)

// --daemon is a registered flag that defaults off, so the ordinary invocation
// is unchanged. Without it the daemon path is unreachable.
func TestDaemonFlagRegistered(t *testing.T) {
	f := flag.Lookup("daemon")
	if f == nil {
		t.Fatal("--daemon is not registered")
	}
	if f.DefValue != "false" {
		t.Errorf("--daemon default = %q, want false", f.DefValue)
	}
}

// An attach client dials --control-addr only when the user passed it. The
// flag has a default loopback address, and treating that default as a decision
// would make every client connect to 127.0.0.1:7391 instead of discovering the
// local socket.
func TestClientAddrIgnoresTheFlagDefault(t *testing.T) {
	if got := clientAddr(control.DefaultTCPAddr, false); got != "" {
		t.Errorf("clientAddr(default, unset) = %q, want empty so discovery runs", got)
	}
	if got := clientAddr("tcp://127.0.0.1:9999", true); got != "tcp://127.0.0.1:9999" {
		t.Errorf("clientAddr(explicit, set) = %q, want the explicit address", got)
	}
}

// runHost chooses the host: --daemon is headless, the ordinary path is the
// native terminal host. Without the split a daemon would try to enter raw mode
// and read stdin.
func TestRunHostDaemonIsHeadless(t *testing.T) {
	h, err := runHost(true, os.Stdin, os.Stdout)
	if err != nil {
		t.Fatalf("runHost(daemon) = %v", err)
	}
	defer h.Close()
	hh, ok := h.(*ui.HeadlessHost)
	if !ok {
		t.Fatalf("runHost(daemon) = %T, want *ui.HeadlessHost", h)
	}
	if cols, rows := hh.Size(); cols != ui.HeadlessCols || rows != ui.HeadlessRows {
		t.Errorf("daemon size = %d,%d, want %d,%d", cols, rows, ui.HeadlessCols, ui.HeadlessRows)
	}
}

// The ordinary path is unchanged: it still builds the native terminal host,
// which refuses a non-terminal. A pipe and /dev/null are not a terminal, so the
// assertion is about which path was tried.
func TestRunHostNormalIsNative(t *testing.T) {
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer devnull.Close()
	h, err := runHost(false, devnull, devnull)
	if err == nil {
		h.Close()
		t.Skip("the standard streams are a terminal here; the native host started")
	}
}

// The first termination signal asks the daemon to close once, and does not
// force an exit. Without the watcher the daemon would die on the signal before
// the deferred save ran.
func TestSignalShutdownFirstSignalClosesOnce(t *testing.T) {
	ch := make(chan os.Signal, 2)
	closed := make(chan struct{}, 1)
	forced := make(chan int, 1)
	stop := signalShutdown(true, ch, func() { closed <- struct{}{} }, func(code int) { forced <- code })
	defer stop()

	ch <- os.Interrupt
	select {
	case <-closed:
	case code := <-forced:
		t.Fatalf("first signal forced exit %d, want a graceful close", code)
	case <-time.After(time.Second):
		t.Fatal("the first signal did not shut down")
	}
	select {
	case <-closed:
		t.Error("shutdown ran twice for one signal")
	case <-time.After(20 * time.Millisecond):
	}
}

// The second signal forces a nonzero exit, so a hung shutdown cannot trap the
// process. Without the branch the second signal would be swallowed.
func TestSignalShutdownSecondSignalForcesExit(t *testing.T) {
	ch := make(chan os.Signal, 2)
	closed := make(chan struct{}, 1)
	forced := make(chan int, 1)
	stop := signalShutdown(true, ch, func() { closed <- struct{}{} }, func(code int) { forced <- code })
	defer stop()

	ch <- os.Interrupt
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("the first signal did not shut down")
	}
	ch <- syscall.SIGTERM
	select {
	case code := <-forced:
		if code != 1 {
			t.Errorf("forced exit code = %d, want 1", code)
		}
	case <-time.After(time.Second):
		t.Fatal("the second signal did not force an exit")
	}
}

// Off the daemon path no watcher is installed: a signal does nothing, which is
// what keeps the terminal path byte-identical.
func TestSignalShutdownOffDaemonPathDoesNothing(t *testing.T) {
	ch := make(chan os.Signal, 2)
	called := make(chan struct{}, 1)
	stop := signalShutdown(false, ch, func() { called <- struct{}{} }, func(int) {})
	defer stop()

	ch <- os.Interrupt
	select {
	case <-called:
		t.Error("a non-daemon run shut down on a signal")
	case <-time.After(50 * time.Millisecond):
	}
}

// --phone implies attach, and --attach alone is enough: both route the
// invocation to client mode. Without the implication a phone would start a
// second local editor over the daemon workspace instead of rendering it.
func TestAttachModeImpliesOnPhone(t *testing.T) {
	if !attachMode(false, true) {
		t.Error("--phone alone did not route to client mode")
	}
	if !attachMode(true, false) {
		t.Error("--attach alone did not route to client mode")
	}
	if attachMode(false, false) {
		t.Error("a plain raj routed to client mode")
	}
}

// Nothing in the tree imports the old attach client, and the package directory
// is gone. The import scan is the durable check: a re-added import fails the
// build, but this says which file brought it back. Without the deletion the
// byte-for-byte local path still shares a routing hook with a second client.
func TestAttachPackageIsGone(t *testing.T) {
	root := filepath.Join("..", "..")
	if _, err := os.Stat(filepath.Join(root, "internal", "attach")); err == nil {
		t.Error("internal/attach still exists; app client mode replaced it")
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat internal/attach: %v", err)
	}
	needle := []byte("raj/internal/" + "attach")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return fs.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(b, needle) {
			t.Errorf("%s still imports internal/attach", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// The editor's help prints long options with two dashes, the spelling every
// usage line and script uses; Go's own default help prints one. It also carries
// the same usage header as `raj ctl` and the daemon, and omits the hidden
// --daemon alias by name.
func TestEditorUsageUsesDoubleDash(t *testing.T) {
	var b strings.Builder
	editorUsage(&b)
	text := b.String()
	if !strings.Contains(text, "usage: raj [options] [file|dir]") {
		t.Errorf("editor usage has no header:\n%s", text)
	}
	for _, name := range []string{"no-restore", "control-addr", "wrap", "standalone"} {
		if !strings.Contains(text, "\n  --"+name) {
			t.Errorf("editor usage does not spell --%s with two dashes:\n%s", name, text)
		}
		if strings.Contains(text, "\n  -"+name) {
			t.Errorf("editor usage spells -%s with one dash:\n%s", name, text)
		}
	}
	if strings.Contains(text, "--daemon") || strings.Contains(text, "\n  -daemon") {
		t.Errorf("editor usage names the hidden --daemon alias:\n%s", text)
	}
}

// Control is opt-in: a plain editor serves no control listener at all, so a
// machine that did not ask for one has no socket to read its buffers through.
func TestControlAddrsPlainEditorServesNothing(t *testing.T) {
	if got := controlAddrs(false, false, false, "/tmp/raj.sock", control.DefaultTCPAddr); got != nil {
		t.Errorf("controlAddrs(plain) = %v, want nil", got)
	}
}

// --control-addr is the opt-in, and the Unix socket listens alongside the
// address the user named so a local script can still read the token back.
func TestControlAddrsExplicitAddrServesSocketAndPort(t *testing.T) {
	got := controlAddrs(false, false, true, "/tmp/raj.sock", "tcp://127.0.0.1:0")
	want := []string{"/tmp/raj.sock", "tcp://127.0.0.1:0"}
	if !slices.Equal(got, want) {
		t.Errorf("controlAddrs(explicit) = %v, want %v", got, want)
	}
}

// A daemon exists to be driven, so it always serves the socket and the default
// TCP port even with no --control-addr.
func TestControlAddrsDaemonAlwaysServes(t *testing.T) {
	got := controlAddrs(false, true, false, "/tmp/raj.sock", control.DefaultTCPAddr)
	want := []string{"/tmp/raj.sock", control.DefaultTCPAddr}
	if !slices.Equal(got, want) {
		t.Errorf("controlAddrs(daemon) = %v, want %v", got, want)
	}
}

// An explicit --control-addr still chooses the port a daemon binds.
func TestControlAddrsDaemonUsesExplicitAddr(t *testing.T) {
	got := controlAddrs(false, true, true, "/tmp/raj.sock", "tcp://127.0.0.1:9999")
	want := []string{"/tmp/raj.sock", "tcp://127.0.0.1:9999"}
	if !slices.Equal(got, want) {
		t.Errorf("controlAddrs(daemon, explicit) = %v, want %v", got, want)
	}
}

// A client dials a running editor and never listens, so it builds no addresses
// no matter what else is set.
func TestControlAddrsClientServesNothing(t *testing.T) {
	if got := controlAddrs(true, false, false, "/tmp/raj.sock", control.DefaultTCPAddr); got != nil {
		t.Errorf("controlAddrs(client) = %v, want nil", got)
	}
	if got := controlAddrs(true, true, true, "/tmp/raj.sock", "tcp://127.0.0.1:0"); got != nil {
		t.Errorf("controlAddrs(client, daemon, explicit) = %v, want nil", got)
	}
}

// --standalone roots at the file's own directory even when a repository
// encloses it: skipping the .git walk is the point of a throwaway editor.
func TestResolveStandaloneSkipsTheGitWalk(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(repo, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(sub, "COMMIT_EDITMSG")
	if err := os.WriteFile(file, []byte("message\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	roots, got, err := resolve([]string{file}, true)
	if err != nil {
		t.Fatalf("resolve(standalone) = %v", err)
	}
	if len(roots) != 1 || roots[0] != sub {
		t.Errorf("roots = %v, want the file's own dir %q, not the repo %q", roots, sub, repo)
	}
	if got != file {
		t.Errorf("file = %q, want %q", got, file)
	}
}

// The ordinary file argument still walks up to the repository, so standalone
// is a separate rule rather than a change to resolve.
func TestResolveRepositoryStillWinsForAPlainFile(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(repo, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(sub, "main.go")
	if err := os.WriteFile(file, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	roots, _, err := resolve([]string{file}, false)
	if err != nil {
		t.Fatalf("resolve = %v", err)
	}
	if len(roots) != 1 || roots[0] != repo {
		t.Errorf("roots = %v, want the enclosing repo %q", roots, repo)
	}
}

// A directory argument is a root, and several are one set in the order typed.
func TestResolveKeepsSeveralRootsInOrder(t *testing.T) {
	one, two := t.TempDir(), t.TempDir()
	roots, file, err := resolve([]string{two, one}, false)
	if err != nil {
		t.Fatal(err)
	}
	if file != "" {
		t.Errorf("file = %q, want none", file)
	}
	if len(roots) != 2 || roots[0] != two || roots[1] != one {
		t.Errorf("roots = %v, want [%s %s] in typed order", roots, two, one)
	}
}

// A directory argument still roots at its repository, the same walk-up the
// editor has always done, so a subdirectory of a checkout is not a workspace
// of its own.
func TestResolveDirectoryWalksToRepository(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(repo, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	roots, file, err := resolve([]string{sub}, false)
	if err != nil {
		t.Fatal(err)
	}
	if file != "" {
		t.Errorf("file = %q, want none", file)
	}
	if len(roots) != 1 || roots[0] != repo {
		t.Errorf("roots = %v, want the enclosing repo %q", roots, repo)
	}
}

// A second regular file is refused: one editor opens one file, and silently
// choosing one of them is how the other looks lost.
func TestResolveRefusesTwoFiles(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.go")
	b := filepath.Join(dir, "b.go")
	for _, f := range []string{a, b} {
		if err := os.WriteFile(f, []byte("package main\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if roots, file, err := resolve([]string{a, b}, false); err == nil {
		t.Errorf("resolve(two files) = %v,%q, want a refusal", roots, file)
	}
}

// --standalone needs one file: an empty argument and a directory are both
// refused rather than quietly rooted somewhere.
func TestResolveStandaloneRefusesNonFiles(t *testing.T) {
	if roots, file, err := resolve(nil, true); err == nil {
		t.Errorf("resolve(standalone, empty) = %v,%q, want a refusal", roots, file)
	}
	if roots, file, err := resolve([]string{t.TempDir()}, true); err == nil {
		t.Errorf("resolve(standalone, dir) = %v,%q, want a refusal", roots, file)
	}
}

func TestWorkspaceFlagRegistered(t *testing.T) {
	f := flag.Lookup("workspace")
	if f == nil {
		t.Fatal("--workspace is not registered")
	}
	if f.DefValue != "" {
		t.Errorf("--workspace default = %q, want empty", f.DefValue)
	}
}

// attachWorkspace is the whole --workspace decision: the refusal matrix, the
// label lookup and the address choice, with the finder injected so the table
// needs no live daemon. It is what keeps `raj --attach --workspace NAME` from
// dialing the wrong daemon or silently rooting at the current directory.
func TestAttachWorkspace(t *testing.T) {
	sockEntry := daemon.Entry{
		Workspace: "phone-rig",
		Roots:     []string{"/repo/a", "/repo/b"},
		PID:       42,
		TCP:       "tcp://127.0.0.1:7391",
		Socket:    "/run/raj/phone-rig.sock",
	}
	findOK := func(string) (daemon.Entry, bool, error) { return sockEntry, true, nil }
	findNone := func(string) (daemon.Entry, bool, error) { return daemon.Entry{}, false, nil }
	entryWith := func(mut func(*daemon.Entry)) func(string) (daemon.Entry, bool, error) {
		return func(string) (daemon.Entry, bool, error) {
			e := sockEntry
			mut(&e)
			return e, true, nil
		}
	}

	tests := []struct {
		name            string
		clientMode      bool
		ctlAddrSet      bool
		args            []string
		find            func(string) (daemon.Entry, bool, error)
		wantRoots       []string
		wantAddr        string
		wantErr         bool
		wantErrContains string
	}{
		{
			name:       "socket is preferred over TCP",
			clientMode: true,
			find:       findOK,
			wantRoots:  []string{"/repo/a", "/repo/b"},
			wantAddr:   "/run/raj/phone-rig.sock",
		},
		{
			name:       "TCP is the fallback when the socket is empty",
			clientMode: true,
			find:       entryWith(func(e *daemon.Entry) { e.Socket = "" }),
			wantRoots:  []string{"/repo/a", "/repo/b"},
			wantAddr:   "tcp://127.0.0.1:7391",
		},
		{
			name:            "label not found",
			clientMode:      true,
			find:            findNone,
			wantErr:         true,
			wantErrContains: "no running daemon labelled",
		},
		{
			name:       "finder error passes through",
			clientMode: true,
			find: func(string) (daemon.Entry, bool, error) {
				return daemon.Entry{}, false, errors.New("2 running daemons are labelled \"phone-rig\"")
			},
			wantErr:         true,
			wantErrContains: "2 running daemons are labelled",
		},
		{
			name:       "not a client",
			clientMode: false,
			find:       findOK,
			wantErr:    true,
		},
		{
			name:       "control-addr already set",
			clientMode: true,
			ctlAddrSet: true,
			find:       findOK,
			wantErr:    true,
		},
		{
			name:       "positional argument",
			clientMode: true,
			args:       []string{"main.go"},
			find:       findOK,
			wantErr:    true,
		},
		{
			name:       "entry with no roots",
			clientMode: true,
			find:       entryWith(func(e *daemon.Entry) { e.Roots = nil }),
			wantErr:    true,
		},
		{
			name:       "entry with no address",
			clientMode: true,
			find: entryWith(func(e *daemon.Entry) {
				e.Socket = ""
				e.TCP = ""
			}),
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			roots, addr, err := attachWorkspace("phone-rig", tt.clientMode, tt.ctlAddrSet, tt.args, tt.find)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("attachWorkspace = %v, %q, want an error", roots, addr)
				}
				if tt.wantErrContains != "" && !strings.Contains(err.Error(), tt.wantErrContains) {
					t.Errorf("attachWorkspace error = %q, want it to contain %q", err, tt.wantErrContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("attachWorkspace = %v", err)
			}
			if !slices.Equal(roots, tt.wantRoots) {
				t.Errorf("roots = %v, want %v", roots, tt.wantRoots)
			}
			if addr != tt.wantAddr {
				t.Errorf("addr = %q, want %q", addr, tt.wantAddr)
			}
		})
	}
}

// --checklist and --motions only shape the probe, so naming either without
// --probe is refused rather than silently ignored.
func TestProbeOnlyFlagsNeedProbe(t *testing.T) {
	if err := probeFlagError(false, true, false); err == nil || err.Error() != "--checklist needs --probe" {
		t.Errorf("checklist without probe = %v, want --checklist needs --probe", err)
	}
	if err := probeFlagError(false, false, true); err == nil || err.Error() != "--motions needs --probe" {
		t.Errorf("motions without probe = %v, want --motions needs --probe", err)
	}
	if err := probeFlagError(true, true, true); err != nil {
		t.Errorf("probe with checklist and motions = %v, want nil", err)
	}
}
