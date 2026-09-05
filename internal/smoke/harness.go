//go:build smoke

// Package smoke drives the real raj binary, in a real process, on a real
// terminal.
//
// Everything in internal/app runs against ui.FakeHost: no tty, no decoder, no
// process. That is the right seam for testing behaviour, and it stubs out
// exactly the things that break in the field — terminal setup, decoding actual
// byte sequences, teardown, and whether the binary as shipped does what the
// packages severally claim. This closes that gap.
//
// The shape is a pty for input and the control socket for observation. Keys go
// in as the CSI-u sequences internal/keys says the terminal emits, and
// assertions come back out of `raj ctl`, so a scenario reads as "press this,
// then the buffer holds that" without parsing a rendered screen.
//
// It is not part of `make check`. It builds a binary, spawns processes and
// waits on wall-clock time, so it is slower and more fragile than a unit test
// by construction. `make smoke` is a pre-tag gate, not a per-commit one.
package smoke

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// Timings. Generous, because a slow machine failing a smoke test teaches people
// to ignore it.
const (
	startTimeout = 15 * time.Second
	settle       = 400 * time.Millisecond
	pollEvery    = 25 * time.Millisecond
)

// editor is one running raj, with the two handles a scenario needs.
type editor struct {
	t    *testing.T
	bin  string
	root string
	pty  *os.File
	sock string
	cmd  *exec.Cmd
}

// start builds raj once per run, then launches it on a fresh workspace.
func start(t *testing.T, files map[string]string) *editor {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	master, slave, err := openPTY()
	if err != nil {
		t.Fatalf("no pty: %v", err)
	}
	errPath := filepath.Join(t.TempDir(), "stderr")
	errFile, err := os.Create(errPath)
	if err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(binary(t), "--control", "--no-restore", root)
	cmd.Dir = root
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, errFile
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start raj: %v", err)
	}
	slave.Close()

	// Nothing reads the screen, but something must: an undrained pty fills its
	// buffer and the renderer blocks on the write, which looks exactly like a
	// hang in whatever the scenario did last.
	go func() {
		buf := make([]byte, 64<<10)
		for {
			if _, err := master.Read(buf); err != nil {
				return
			}
		}
	}()

	e := &editor{t: t, root: root, pty: master, cmd: cmd}
	t.Cleanup(e.stop)

	// The control address is printed on stderr before the alternate screen is
	// entered, which is the documented way a harness learns it.
	deadline := time.Now().Add(startTimeout)
	re := regexp.MustCompile(`control (\S+)`)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(errPath); err == nil {
			if m := re.FindSubmatch(b); m != nil {
				e.sock = string(m[1])
				return e
			}
		}
		time.Sleep(pollEvery)
	}
	out, _ := os.ReadFile(errPath)
	t.Fatalf("raj never printed a control address in %v; stderr:\n%s", startTimeout, out)
	return nil
}

func (e *editor) stop() {
	if e.cmd != nil && e.cmd.Process != nil {
		e.cmd.Process.Kill()
		e.cmd.Wait()
	}
	if e.pty != nil {
		e.pty.Close()
	}
}

// press sends chords as the CSI-u sequences internal/keys pins, so a scenario
// exercises the decoder rather than bypassing it.
func (e *editor) press(seqs ...string) {
	e.t.Helper()
	for _, s := range seqs {
		if _, err := e.pty.WriteString("\x1b[" + s); err != nil {
			e.t.Fatalf("write %q to pty: %v", s, err)
		}
		time.Sleep(settle)
	}
}

// typeText sends literal bytes, which is what a terminal sends for plain keys.
func (e *editor) typeText(s string) {
	e.t.Helper()
	if _, err := e.pty.WriteString(s); err != nil {
		e.t.Fatalf("type %q: %v", s, err)
	}
	time.Sleep(settle)
}

// ctl runs the shipped client against this editor.
func (e *editor) ctl(args ...string) string {
	e.t.Helper()
	cmd := exec.Command(binary(e.t), append([]string{"ctl"}, args...)...)
	cmd.Dir = e.root
	cmd.Env = append(os.Environ(), "RAJ_CONTROL_ADDR="+e.sock)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.Run() // a non-zero exit is data; the caller asserts on the text
	return strings.TrimRight(out.String(), "\n")
}

// text is the buffer's contents, unsaved edits included.
func (e *editor) text(name string) string {
	e.t.Helper()
	return e.ctl("read", filepath.Join(e.root, name))
}

// onDisk is what another process would see.
func (e *editor) onDisk(name string) string {
	e.t.Helper()
	b, err := os.ReadFile(filepath.Join(e.root, name))
	if err != nil {
		e.t.Fatal(err)
	}
	return string(b)
}

// rewrite stands in for git, a formatter or a second editor. The future mtime
// keeps the change detectable on a filesystem with coarse timestamps.
func (e *editor) rewrite(name, content string) {
	e.t.Helper()
	path := filepath.Join(e.root, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		e.t.Fatal(err)
	}
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		e.t.Fatal(err)
	}
}

func (e *editor) open(name string) {
	e.t.Helper()
	if got := e.ctl("open", filepath.Join(e.root, name)); !strings.Contains(got, "opened") {
		e.t.Fatalf("open %s: %s", name, got)
	}
	time.Sleep(settle)
}

// binary builds raj once and caches the path for the whole run.
var builtBinary string

func binary(t *testing.T) string {
	t.Helper()
	if builtBinary != "" {
		return builtBinary
	}
	dir, err := os.MkdirTemp("", "raj-smoke")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "raj")
	cmd := exec.Command("go", "build", "-o", path, "./cmd/raj")
	cmd.Dir = repoRoot(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build raj: %v\n%s", err, out)
	}
	builtBinary = path
	return path
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test directory")
		}
		dir = parent
	}
}

func must(t *testing.T, what, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %q, want %q", what, got, want)
	}
}

var _ = fmt.Sprintf
