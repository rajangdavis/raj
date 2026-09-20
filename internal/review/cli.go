package review

import (
	"fmt"
	"io"
	"os"
	"time"

	"raj/internal/ui"
)

// CLI runs the review console and returns a process exit status. args is
// os.Args[1:], including --review. in and out must be the terminal; stderr
// carries diagnostics and parse errors.
func CLI(args []string, in, out *os.File, stderr io.Writer) int {
	opts, err := ParseConfig(args)
	if err != nil {
		fmt.Fprintln(stderr, "raj --review:", err)
		fmt.Fprintln(stderr, "usage: raj --review [--phone] [--list] [addr]")
		return 2
	}
	t, err := Dial(opts.Addr)
	if err != nil {
		fmt.Fprintln(stderr, "raj --review:", err)
		return 1
	}
	defer t.Close()
	ctl := NewController(t)
	if err := ctl.Refresh(); err != nil {
		fmt.Fprintln(stderr, "raj --review:", err)
		return 1
	}
	if opts.List {
		printQueue(ctl.Queue, out)
		return 0
	}
	host, err := ui.NewNativeHost(in, out, 150*time.Millisecond)
	if err != nil {
		fmt.Fprintln(stderr, "raj --review:", err)
		return 1
	}
	defer host.Close()
	if err := Run(host, ctl, opts.Phone); err != nil {
		fmt.Fprintln(stderr, "raj --review:", err)
		return 1
	}
	return 0
}

// printQueue is the `--list` form: the header and one line per item, no
// terminal. It is the fallback when the interactive console cannot run and the
// shape automated checks read.
func printQueue(q Queue, w io.Writer) {
	fmt.Fprintln(w, q.Header().Line())
	for _, it := range q.Items {
		fmt.Fprintln(w, it.Row())
		if it.Reason != "" {
			fmt.Fprintln(w, "  reason: "+it.Reason)
		}
	}
}
