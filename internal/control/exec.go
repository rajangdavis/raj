package control

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os/exec"
	"sync"
)

// Running a command for a caller.
//
// This is in the editor rather than left to the agent's own shell for one
// reason: the editor is the only place that knows whether the files the command
// is about to read are stale. An agent with a shell tool can already run
// anything; what it cannot do from out there is notice that the buffer it just
// edited has not reached disk, so `go test` is testing the old text and the
// failure it reports is about code nobody has.
//
// Cancellation is the other half, and the doc names it as worth having on its
// own: a tool-call path with no way to stop a hung test is a session that has
// to be killed.

// Stream ids for output frames.
const (
	StreamStdout uint8 = 1
	StreamStderr uint8 = 2
)

// execChunk bounds one output frame. Line-oriented would be friendlier to read
// but hangs on a command that prints a prompt without a newline, and a build
// tool that draws a progress bar does exactly that.
const execChunk = 8 << 10

// Run executes argv and calls emit with output as it arrives, returning the
// exit status.
//
// A non-zero exit is not an error: a failing test is the answer to the question
// the caller asked, not a failure to answer it. Only being unable to start the
// process, or being cancelled, is an error.
func Run(ctx context.Context, argv []string, dir string,
	emit func(stream uint8, b []byte)) (int, error) {
	if len(argv) == 0 {
		return 0, errors.New("exec needs a command")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 0, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return 0, err
	}
	if err := cmd.Start(); err != nil {
		return 0, err
	}

	// Both pipes are drained concurrently. Reading one to completion first
	// deadlocks the moment the other fills its buffer, which for a compiler
	// writing warnings to stderr is immediate.
	var wg sync.WaitGroup
	pump := func(r io.Reader, id uint8) {
		defer wg.Done()
		br := bufio.NewReaderSize(r, execChunk)
		buf := make([]byte, execChunk)
		for {
			n, err := br.Read(buf)
			if n > 0 && emit != nil {
				// Copied: the frame it becomes outlives this iteration.
				out := make([]byte, n)
				copy(out, buf[:n])
				emit(id, out)
			}
			if err != nil {
				return
			}
		}
	}
	wg.Add(2)
	go pump(stdout, StreamStdout)
	go pump(stderr, StreamStderr)

	// Waiting is done off to the side so a cancellation can return without it.
	//
	// CommandContext kills the process it started, but that process may have
	// children — `sh -c "go test"` is two processes, and killing the shell
	// leaves the test binary holding the pipes open. The pumps then never see
	// EOF and a plain wg.Wait() blocks for as long as the orphan runs, which
	// makes cancel silently do nothing. Returning on ctx instead means a
	// cancel is prompt; the leftover goroutines exit when the pipes finally
	// close, and they write to nothing but a closed channel guard.
	waited := make(chan struct{})
	var waitErr error
	go func() {
		wg.Wait()
		waitErr = cmd.Wait()
		close(waited)
	}()
	select {
	case <-waited:
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	err = waitErr
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), nil
	}
	if err != nil {
		return 0, err
	}
	return 0, nil
}
