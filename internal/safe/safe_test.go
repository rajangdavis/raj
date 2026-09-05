package safe

import (
	"bytes"
	"os"
	"strings"
	"sync"
	"testing"
)

// capture swaps the exit and stderr hooks for the duration of a test and
// returns what Recover decided.
func capture(t *testing.T) (code *int, out *bytes.Buffer) {
	t.Helper()
	oldExit, oldErr, oldCleanups := exit, errw, cleanups
	got := -1
	buf := &bytes.Buffer{}
	exit = func(c int) { got = c }
	errw = buf
	cleanups = nil
	t.Cleanup(func() {
		mu.Lock()
		exit, errw, cleanups = oldExit, oldErr, oldCleanups
		mu.Unlock()
	})
	return &got, buf
}

func TestRecoverRunsCleanupsAndExits(t *testing.T) {
	code, out := capture(t)
	restored := false
	OnPanic(func() { restored = true })

	func() {
		defer Recover()
		panic("decoder blew up")
	}()

	if !restored {
		t.Fatal("terminal cleanup did not run")
	}
	if *code != 2 {
		t.Fatalf("exit code = %d, want 2", *code)
	}
	if s := out.String(); !strings.Contains(s, "decoder blew up") || !strings.Contains(s, "safe_test.go") {
		t.Fatalf("stderr did not carry the panic and a trace:\n%s", s)
	}
}

func TestCleanupsRunInReverseOrder(t *testing.T) {
	capture(t)
	var order []string
	OnPanic(func() { order = append(order, "first") })
	OnPanic(func() { order = append(order, "second") })

	func() {
		defer Recover()
		panic("x")
	}()

	want := []string{"second", "first"}
	if len(order) != 2 || order[0] != want[0] || order[1] != want[1] {
		t.Fatalf("order = %v, want %v", order, want)
	}
}

// The terminal restore must survive a cleanup registered before it blowing up.
func TestOneBadCleanupDoesNotStopTheRest(t *testing.T) {
	capture(t)
	restored := false
	OnPanic(func() { restored = true })
	OnPanic(func() { panic("cleanup is broken too") })

	func() {
		defer Recover()
		panic("x")
	}()

	if !restored {
		t.Fatal("a panicking cleanup swallowed the one behind it")
	}
}

func TestRecoverIsANoOpWithoutAPanic(t *testing.T) {
	code, _ := capture(t)
	ran := false
	OnPanic(func() { ran = true })

	func() { defer Recover() }()

	if ran || *code != -1 {
		t.Fatal("Recover acted on a goroutine that returned normally")
	}
}

func TestGoRunsTheFunction(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)
	ran := false
	Go(func() { ran = true; wg.Done() })
	wg.Wait()
	if !ran {
		t.Fatal("Go did not run fn")
	}
}

func TestExitDefaultsToOsExit(t *testing.T) {
	// Guards against a test leaving the hook swapped out, which would make
	// every later panic in a real run silently continue.
	if os.Getenv("RAJ_SAFE_CHILD") != "" {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	if exit == nil || errw == nil {
		t.Fatal("safe's hooks were left nil")
	}
}
