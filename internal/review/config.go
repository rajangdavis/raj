package review

import (
	"flag"
	"fmt"
	"io"
	"strings"
)

// Options is a parsed `raj --review` invocation.
type Options struct {
	Addr  string
	Phone bool
	List  bool
}

// Requested reports whether args ask for the review console. main uses it to
// route --review before the editor's own global flags parse, because the
// console's grammar — a positional address, --phone, --list — is not the
// editor's.
func Requested(args []string) bool {
	for _, a := range args {
		if a == "--review" || a == "-review" {
			return true
		}
		// Stop at the first non-flag operand, matching Go's flag grammar:
		// `raj somefile --review` opens a file, it does not start a console.
		if !strings.HasPrefix(a, "-") {
			return false
		}
	}
	return false
}

// ParseConfig parses the arguments of a `raj --review` invocation, excluding the
// program name. It is a private FlagSet rather than the global flag package so
// every --review/--phone/address combination is testable without a terminal.
func ParseConfig(args []string) (Options, error) {
	fs := flag.NewFlagSet("review", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	want := fs.Bool("review", false, "review the workspace's pending change sets")
	phone := fs.Bool("phone", false, "phone profile: one item per screen")
	list := fs.Bool("list", false, "print the queue and exit without a terminal")
	if err := fs.Parse(args); err != nil {
		return Options{}, err
	}
	if !*want {
		return Options{}, fmt.Errorf("--review is required")
	}
	if fs.NArg() > 1 {
		return Options{}, fmt.Errorf("--review takes at most one address")
	}
	opts := Options{Phone: *phone, List: *list}
	if fs.NArg() == 1 {
		opts.Addr = fs.Arg(0)
	}
	return opts, nil
}
