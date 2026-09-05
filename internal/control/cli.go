package control

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"raj/internal/prog"
)

// `raj ctl` — the command-line face of the control socket.
//
// This is what an agent harness uses. It needs no protocol support on the
// harness's side, no long-lived bridge process, and no integration beyond the
// shell tool every harness already has: the agent runs a command and reads the
// output, which is the one interface they all share.
//
// # apply is the real verb; edit is a convenience
//
// `apply` takes byte offsets and the version they were measured in, which is
// what the editor's write surface actually wants. Staleness is then handled by
// rebasing the hunks onto the current document, not by matching text: there is
// no old string to get wrong, no whitespace sensitivity, and hunks are rejected
// one at a time so a five-site change does not lose four because one moved.
// That is strictly better than string replacement and it is what an agent
// should use — `read -json` hands back the bytes and the version together for
// exactly this.
//
// `edit` remains for the hand-typed case, where quoting a line is easier than
// counting to it. It is string replacement with all of string replacement's
// weaknesses, implemented as a read, an offset calculation, and an `apply`
// against the version it just read — so it is a caller of the real verb rather
// than a second write path.
//
// # Exit codes
//
// 0 success, 1 refusal, 2 usage. A shell tool reports the code, so a refusal
// has to be a non-zero exit and not a cheerful message: an agent that reads
// "could not find that text" on exit 0 will carry on as though it had worked.

const ctlUsage = `usage: raj ctl <command> [options]

  list                       running editors and their workspaces
  buffers                    files open in the editor
  read [path]                full text of a buffer, unsaved changes included
  open <path>                open a file in the editor
  whoami                     the author id this connection writes as
  who                        everyone writing in this workspace
  recv                       wait for the user to say something, then print it
  groups [path]              change sets in a buffer, and their state
  accept [path] -group N     agree to a change set
  reject [path] -group N     back one out
  search -q PATTERN          search the workspace, unsaved edits included
  version [path]             the version a later apply bases on
  apply [path] -base N -start N -end N [-text S | -text-file F]
                             replace bytes [start,end) with text
  edit [path] -old S -new S  replace an exact string (convenience over apply)
  save [path]                write a buffer to disk
  exec -- CMD [ARGS...]      run a command; refused while buffers are unsaved
  stats                      what the exec policy has cost this session
  run -prog BYTES            run a program of opcodes; @FILE or - for stdin
  disasm -prog BYTES         print a program as text, without sending it

Path may be omitted for the buffer the user is looking at.

The editor is found automatically, or named with -addr or RAJ_CONTROL_ADDR:
a socket path, or tcp://host:port for a raj outside this container. A TCP
editor also wants RAJ_CONTROL_TOKEN set to the token it printed on startup.
`

// CLI runs one command and returns a process exit code.
func CLI(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, ctlUsage)
		return 2
	}
	cmd, rest := args[0], args[1:]

	fs := flag.NewFlagSet("raj ctl "+cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	socket := fs.String("socket", "", "path to the editor's control socket")
	addr := fs.String("addr", "", "the editor's control address: a socket path, or tcp://host:port")
	asJSON := fs.Bool("json", false, "machine-readable output")
	group := fs.Uint64("group", 0, "accept/reject: the change set id, from `groups`")
	identity := fs.String("as", "", "identity to write as; the same one reconnecting keeps its author id")
	name := fs.String("name", "", "display name for this participant")
	dir := fs.String("dir", "", "exec: directory to run in, inside the workspace")
	query := fs.String("q", "", "search: the pattern")
	include := fs.String("include", "", "search: comma-separated globs to search")
	exclude := fs.String("exclude", "", "search: comma-separated globs to skip")
	regex := fs.Bool("regex", false, "search: treat the pattern as a regular expression")
	matchCase := fs.Bool("case", false, "search: match case")
	word := fs.Bool("word", false, "search: whole words only")
	base := fs.Uint64("base", 0, "apply: the version the offsets were measured in")
	start := fs.Int("start", -1, "apply: first byte of the span to replace")
	end := fs.Int("end", -1, "apply: one past the last byte of the span")
	textArg := fs.String("text", "", "apply: replacement text")
	progArg := fs.String("prog", "", "run/disasm: the program itself, or @FILE, or - for stdin")
	progHex := fs.String("hex", "", "run/disasm: the program as hex, for one whose payloads contain a zero byte")
	textFile := fs.String("text-file", "", "apply: read -text from a file, or - for stdin")
	old := fs.String("old", "", "edit: the exact existing text to replace")
	newText := fs.String("new", "", "edit: the replacement text")
	oldFile := fs.String("old-file", "", "edit: read -old from a file, or - for stdin")
	newFile := fs.String("new-file", "", "edit: read -new from a file, or - for stdin")
	all := fs.Bool("all", false, "edit: replace every occurrence instead of requiring exactly one")
	wait := fs.Duration("wait", 0, "recv: give up after this long; zero waits indefinitely")
	// Everything after "--" is another program's argv and must reach it intact:
	// `raj ctl exec -- go test -run X` has to give go its own -run, not have it
	// parsed as ours or shuffled by reorder.
	var argv []string
	if i := indexOf(rest, "--"); i >= 0 {
		argv, rest = rest[i+1:], rest[:i]
	}
	if err := fs.Parse(reorder(fs, rest)); err != nil {
		return 2
	}
	path := fs.Arg(0)

	if cmd == "help" || cmd == "-h" || cmd == "--help" {
		fmt.Fprint(stdout, ctlUsage)
		return 0
	}
	if cmd == "list" {
		return list(stdout, stderr, *asJSON)
	}
	// disasm reads a file and prints it. Answered before the editor is located
	// so that inspecting a program works when raj is not running — which is
	// exactly when you are most likely to be looking at one, because something
	// it did was wrong.
	if cmd == "disasm" {
		return disasmProgram(*progArg, *progHex, stdout, stderr)
	}

	cwd, _ := os.Getwd()
	sock, err := Locate(firstOf(*addr, *socket), cwd)
	if err != nil {
		fmt.Fprintln(stderr, "raj ctl:", err)
		return 1
	}
	c, err := Dial(sock)
	if err != nil {
		fmt.Fprintf(stderr, "raj ctl: cannot reach raj on %s: %v\n", sock, err)
		return 1
	}
	defer c.Close()
	// Before anything with a path in it. The editor may be on the other side of
	// a container boundary, where the paths this process can see are not the
	// paths it has open.
	if _, err := c.ResolveRoots(cwd); err != nil {
		fmt.Fprintln(stderr, "raj ctl:", err)
		return 1
	}

	switch cmd {
	case "buffers":
		return buffers(c, stdout, stderr, *asJSON)
	case "read":
		return read(c, path, stdout, stderr, *asJSON)
	case "open":
		if path == "" {
			fmt.Fprintln(stderr, "raj ctl open: needs a path")
			return 2
		}
		return simple(c, Request{Op: "open", Path: path}, "opened "+path, stdout, stderr, *asJSON)
	case "save":
		return simple(c, Request{Op: "save", Path: path}, "saved", stdout, stderr, *asJSON)
	case "exec":
		return doExec(c, argv, *dir, stdout, stderr, *asJSON)
	case "run":
		return runProgram(c, *progArg, *progHex, stdout, stderr, *asJSON)
	case "stats":
		res, err := c.Do(Request{Op: "stats"})
		if code := fail(stderr, res, err); code != 0 {
			return code
		}
		if *asJSON {
			return emit(stdout, res.Stats)
		}
		fmt.Fprintf(stdout, "runs %d\nran-against-stale-files %d\nstale-but-agent-only %d\n",
			res.Stats.Runs, res.Stats.Stale, res.Stats.AgentOnly)
		return 0
	case "groups":
		res, err := c.Do(Request{Op: "groups", Path: path})
		if code := fail(stderr, res, err); code != 0 {
			return code
		}
		if *asJSON {
			return emit(stdout, res.Groups)
		}
		for _, g := range res.Groups {
			fmt.Fprintf(stdout, "%d\tauthor %d\t%s\t%d ops\t%+d bytes\n",
				g.ID, g.Author, g.State, g.Ops, g.Bytes)
		}
		return 0
	case "accept", "reject":
		return simple(c, Request{Op: cmd, Path: path, Group: *group}, cmd+"ed",
			stdout, stderr, *asJSON)
	case "who":
		res, err := c.Do(Request{Op: "hello", Identity: identityOf(*identity), Name: *name})
		if code := fail(stderr, res, err); code != 0 {
			return code
		}
		if *asJSON {
			return emit(stdout, res.Participants)
		}
		for _, p := range res.Participants {
			state := "gone"
			if p.Connected {
				state = "here"
			}
			fmt.Fprintf(stdout, "%d\t%s\t%s\t%s\n", p.ID, p.Kind, p.Name, state)
		}
		return 0
	case "recv":
		return recv(c, *identity, *name, *wait, stdout, stderr, *asJSON)
	case "whoami":
		res, err := c.Do(Request{Op: "ping"})
		if code := fail(stderr, res, err); code != 0 {
			return code
		}
		if *asJSON {
			out := map[string]any{"author": res.Author, "root": res.Root}
			if m := c.Mapper(); m.Active() {
				// Reported rather than assumed: if paths are being rewritten,
				// the one thing a caller needs to be able to check is what to.
				out["root_map"] = m.String()
			}
			return emit(stdout, out)
		}
		fmt.Fprintf(stdout, "%d\n", res.Author)
		return 0
	case "search":
		return doSearch(c, SearchQuery{Text: *query, Include: *include, Exclude: *exclude,
			Regex: *regex, Case: *matchCase, Word: *word}, stdout, stderr, *asJSON)
	case "version":
		return simple(c, Request{Op: "version", Path: path}, "", stdout, stderr, *asJSON)
	case "apply":
		return apply(c, path, *base, *start, *end, *textArg, *textFile, fs, stdout, stderr, *asJSON)
	case "edit":
		o, n, code := editText(*old, *newText, *oldFile, *newFile, stderr)
		if code != 0 {
			return code
		}
		return edit(c, path, o, n, *all, stdout, stderr, *asJSON)
	}
	fmt.Fprintf(stderr, "raj ctl: unknown command %q\n\n%s", cmd, ctlUsage)
	return 2
}

// firstOf picks the first non-empty of its arguments.
func firstOf(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// identityOf falls back to the environment, so a harness sets RAJ_IDENTITY once
// rather than passing -as on every call — and a harness that forgets both is
// anonymous rather than broken.
func identityOf(flagVal string) string {
	if flagVal != "" {
		return flagVal
	}
	if env := os.Getenv("RAJ_IDENTITY"); env != "" {
		return env
	}
	return "anon-cli"
}

// indexOf finds a literal argument.
func indexOf(args []string, want string) int {
	for i, a := range args {
		if a == want {
			return i
		}
	}
	return -1
}

// reorder moves operands after the flags.
//
// Go's flag package stops parsing at the first non-flag argument, so
// `edit path -old X` would treat every flag as an operand and then complain
// that -old was missing — a confusing failure for the natural word order, and
// the natural word order is what an agent reading the help will write.
func reorder(fs *flag.FlagSet, args []string) []string {
	var flags, operands []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			operands = append(operands, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			operands = append(operands, a)
			continue
		}
		flags = append(flags, a)
		// A flag in "-name=value" form carries its own value; otherwise a
		// non-boolean flag takes the next argument, and a boolean does not.
		name := strings.TrimLeft(a, "-")
		if strings.Contains(name, "=") || isBool(fs, name) {
			continue
		}
		if i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	return append(flags, operands...)
}

func isBool(fs *flag.FlagSet, name string) bool {
	f := fs.Lookup(name)
	if f == nil {
		return false
	}
	b, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok && b.IsBoolFlag()
}

// editText resolves -old/-new against their file forms. Flags carry a one-line
// replacement fine; anything with newlines in it is painful to quote through a
// shell, and an agent writing a heredoc into a file is the shape that works.
func editText(old, newText, oldFile, newFile string, stderr io.Writer) (string, string, int) {
	readArg := func(flagVal, file, name string) (string, int) {
		if file == "" {
			return flagVal, 0
		}
		if flagVal != "" {
			fmt.Fprintf(stderr, "raj ctl edit: -%s and -%s-file are alternatives\n", name, name)
			return "", 2
		}
		var data []byte
		var err error
		if file == "-" {
			data, err = io.ReadAll(os.Stdin)
		} else {
			data, err = os.ReadFile(file)
		}
		if err != nil {
			fmt.Fprintln(stderr, "raj ctl edit:", err)
			return "", 1
		}
		return string(data), 0
	}
	o, code := readArg(old, oldFile, "old")
	if code != 0 {
		return "", "", code
	}
	n, code := readArg(newText, newFile, "new")
	if code != 0 {
		return "", "", code
	}
	if o == "" {
		fmt.Fprintln(stderr, "raj ctl edit: -old is required and must not be empty.\n"+
			"To insert text, include a surrounding line in -old and repeat it in -new.")
		return "", "", 2
	}
	return o, n, 0
}

// doExec runs a command and relays its output as it arrives.
//
// The exit status is this command's exit status, so a caller's shell sees what
// the command itself returned. A refusal is 2 instead, so "the tests failed" and
// "the tests never ran" are distinguishable — an agent that could not tell them
// apart would report a passing build as broken.
func doExec(c *Client, argv []string, dir string, stdout, stderr io.Writer, asJSON bool) int {
	if len(argv) == 0 {
		fmt.Fprintln(stderr, "raj ctl exec: needs a command, after --")
		return 2
	}
	var outBuf, errBuf []byte
	relay := func(stream uint8, b string) {
		switch {
		case asJSON && stream == StreamStderr:
			errBuf = append(errBuf, b...)
		case asJSON:
			outBuf = append(outBuf, b...)
		case stream == StreamStderr:
			io.WriteString(stderr, b)
		default:
			io.WriteString(stdout, b)
		}
	}
	res, err := c.DoExec(Request{Op: "exec", Argv: argv, Dir: dir}, relay)
	if err != nil {
		fmt.Fprintln(stderr, "raj ctl exec:", err)
		return 2
	}
	if res.Err != "" {
		fmt.Fprintln(stderr, "raj ctl exec:", res.Err)
		return 2
	}
	// A warning, not a refusal. The command already ran; this is what the
	// caller needs in order to judge the result, because a subprocess reads
	// files and these files did not match the buffers.
	if len(res.Dirty) > 0 && !asJSON {
		fmt.Fprintln(stderr, "raj ctl exec: warning — these buffers are unsaved, so the "+
			"command read older text:")
		for _, d := range res.Dirty {
			note := "  (the user has unsaved edits here)"
			if d.AgentOnly {
				note = "  (only your own edits are unsaved)"
			}
			fmt.Fprintf(stderr, "  %s%s\n", d.Path, note)
		}
		fmt.Fprintln(stderr, "If the result looks wrong, save what is yours and run it again. "+
			"Do not save the user's work for them.")
	}
	if asJSON {
		emit(stdout, map[string]any{
			"exit": res.Exit, "stdout": string(outBuf), "stderr": string(errBuf),
			"stale": res.Dirty})
	}
	return res.Exit
}

// doSearch prints hits in the grep format every tool already parses:
// path:line:col:text. The overlay means a hit can be in a buffer the user has
// not saved, which is the point — an agent that grepped the filesystem would
// see stale text and edit against it.
func doSearch(c *Client, q SearchQuery, stdout, stderr io.Writer, asJSON bool) int {
	// Print as they arrive rather than at the end. A walk over a large tree
	// takes seconds, and a caller — a person at a terminal or an agent reading
	// a pipe — should not wait for the last file to see the first hit.
	// Ctrl+C closes the connection, which cancels the walk in the editor.
	n := 0
	stream := func(batch []SearchMatch) {
		if asJSON {
			return // JSON is emitted whole, so it stays parseable
		}
		for _, m := range batch {
			fmt.Fprintf(stdout, "%s:%d:%d:%s\n", m.Path, m.Line, m.Col, m.Text)
			n++
		}
	}
	res, err := c.DoStream(Request{Op: "search", Query: &q}, stream)
	if code := fail(stderr, res, err); code != 0 {
		return code
	}
	if asJSON {
		return emit(stdout, map[string]any{
			"matches": res.Matches, "files": res.Files, "capped": res.Capped})
	}
	if res.Capped {
		fmt.Fprintln(stderr, "note: results were capped; narrow the query with -include")
	}
	if len(res.Matches) == 0 {
		return 1 // no hits is a non-zero exit, as grep has always had it
	}
	return 0
}

// apply is the direct write: offsets, and the version they were measured in.
func apply(c *Client, path string, base uint64, start, end int, textArg, textFile string,
	fs *flag.FlagSet, stdout, stderr io.Writer, asJSON bool) int {
	if !flagSet(fs, "base") {
		fmt.Fprintln(stderr, "raj ctl apply: -base is required.\n"+
			"Offsets only mean something in the coordinates of a version you have read;\n"+
			"get one with `raj ctl read -json` or `raj ctl version`.")
		return 2
	}
	if start < 0 || end < start {
		fmt.Fprintf(stderr, "raj ctl apply: -start %d -end %d is not a span\n", start, end)
		return 2
	}
	text := textArg
	if textFile != "" {
		if textArg != "" {
			fmt.Fprintln(stderr, "raj ctl apply: -text and -text-file are alternatives")
			return 2
		}
		var data []byte
		var err error
		if textFile == "-" {
			data, err = io.ReadAll(os.Stdin)
		} else {
			data, err = os.ReadFile(textFile)
		}
		if err != nil {
			fmt.Fprintln(stderr, "raj ctl apply:", err)
			return 1
		}
		text = string(data)
	}
	res, err := c.Do(Request{Op: "apply", Path: path, Base: &base,
		Hunks: []Hunk{{Start: start, End: end, Text: text}}})
	return reportApply(res, err, 1, stdout, stderr, asJSON)
}

// reportApply is shared by apply and edit so the two cannot describe the same
// outcome differently.
func reportApply(res Response, err error, hunks int, stdout, stderr io.Writer, asJSON bool) int {
	if err != nil {
		fmt.Fprintln(stderr, "raj ctl apply:", err)
		return 1
	}
	if len(res.Conflicts) > 0 {
		fmt.Fprintf(stderr, "raj ctl apply: %d of %d hunks could not be placed on the current\n"+
			"version — the buffer moved further than a rebase could carry them. Read it\n"+
			"again and redo those hunks against the version you get back.\n",
			len(res.Conflicts), hunks)
		return 1
	}
	if res.Err != "" {
		fmt.Fprintln(stderr, "raj ctl apply:", res.Err)
		return 1
	}
	if asJSON {
		return emit(stdout, map[string]any{"ok": true, "applied": hunks, "version": res.Version})
	}
	fmt.Fprintf(stdout, "applied %d hunk(s) at version %d; the buffer has unsaved changes\n",
		hunks, res.Version)
	return 0
}

// flagSet reports whether a flag was given, so a required one with a valid zero
// value can tell "0" from "not supplied". Version 0 is a real version.
func flagSet(fs *flag.FlagSet, name string) bool {
	seen := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			seen = true
		}
	})
	return seen
}

func list(stdout, stderr io.Writer, asJSON bool) int {
	found := Discover()
	if asJSON {
		return emit(stdout, found)
	}
	if len(found) == 0 {
		fmt.Fprintln(stderr, "no running raj found; start one with --control")
		return 1
	}
	for _, in := range found {
		fmt.Fprintf(stdout, "%s\t%s\n", in.Socket, in.Root)
	}
	return 0
}

func buffers(c *Client, stdout, stderr io.Writer, asJSON bool) int {
	res, err := c.Do(Request{Op: "buffers"})
	if code := fail(stderr, res, err); code != 0 {
		return code
	}
	if asJSON {
		return emit(stdout, res.Buffers)
	}
	if len(res.Buffers) == 0 {
		fmt.Fprintln(stdout, "no files open; use `raj ctl open <path>`")
		return 0
	}
	for _, b := range res.Buffers {
		name := b.Path
		if name == "" {
			name = "(unnamed buffer, not addressable)"
		}
		state := "saved"
		if b.Dirty {
			state = "unsaved-changes"
		}
		fmt.Fprintf(stdout, "%s\t%d bytes\t%d lines\t%s\n", name, b.Bytes, b.Lines, state)
	}
	return 0
}

func read(c *Client, path string, stdout, stderr io.Writer, asJSON bool) int {
	res, err := c.Do(Request{Op: "text", Path: path})
	if code := fail(stderr, res, err); code != 0 {
		return code
	}
	if asJSON {
		// Spans are annotated rather than raw: the useful question is "is this
		// mine", and making every caller re-derive it from two integers is how
		// it gets derived wrong.
		spans := make([]map[string]any, 0, len(res.Spans))
		for _, sp := range res.Spans {
			spans = append(spans, map[string]any{
				"text": sp.Text, "author": sp.Author,
				"mine": sp.Mine(c.Author()), "by_user": sp.ByUser(),
			})
		}
		return emit(stdout, map[string]any{
			"text": res.Text(), "version": res.Version,
			"author": c.Author(), "spans": spans,
		})
	}
	io.WriteString(stdout, res.Text())
	return 0
}

// runProgram sends a program and prints one line per verb it ran.
//
// The flags stay: `raj ctl apply -base N -start N -end N` is a human
// affordance, and a person at a shell should not have to hand-assemble bytes.
// This is the other door, for a caller that already thinks in programs — an
// agent emitting a batch, or a test replaying a recorded one.
func runProgram(c *Client, file, hexed string, stdout, stderr io.Writer, asJSON bool) int {
	program, err := readProgram(file, hexed)
	if err != nil {
		fmt.Fprintln(stderr, "raj ctl run:", err)
		return 2
	}
	res, err := c.Do(Request{Op: "prog", Program: program})
	if code := fail(stderr, res, err); code != 0 {
		// A compile error names the opcode, so the disassembly is the useful
		// next thing to look at rather than a hex dump of the file.
		fmt.Fprint(stderr, prog.Disasm(program))
		return code
	}
	if asJSON {
		return emit(stdout, res)
	}
	fmt.Fprintln(stdout, "ok")
	return 0
}

// disasmProgram prints a program without sending it.
//
// The JSON header this encoding replaces was chosen for inspectability —
// serialisation is unmeasurable against a model round trip, so there was
// nothing to buy by making it opaque. That reasoning did not stop being true,
// so the property moved into a tool rather than being spent.
func disasmProgram(file, hexed string, stdout, stderr io.Writer) int {
	program, err := readProgram(file, hexed)
	if err != nil {
		fmt.Fprintln(stderr, "raj ctl disasm:", err)
		return 2
	}
	fmt.Fprint(stdout, prog.Disasm(program))
	return 0
}

// readProgram takes a program from wherever the caller has one.
//
// The bytes themselves are the ordinary case: `raj ctl run -prog "$(gen)"`.
// That works because the encoding has no zero byte in its framing — see
// internal/prog — and argv carries every byte except that one, since the kernel
// delimits argv strings with it. A caller that wants a file says @FILE, and one
// piping from a generator says -.
//
// -hex remains for the case the framing cannot help with: a payload that itself
// contains a zero byte. Document text in a buffer raj will open never does, so
// this is rare, and the error says which door to use rather than leaving it to
// be worked out.
func readProgram(arg, hexed string) ([]byte, error) {
	switch {
	case hexed != "" && arg != "":
		return nil, errors.New("pass -prog or -hex, not both")
	case hexed != "":
		b, err := hex.DecodeString(strings.TrimSpace(hexed))
		if err != nil {
			return nil, fmt.Errorf("-hex: %w", err)
		}
		return b, nil
	case arg == "":
		return nil, errors.New("needs -prog BYTES, -prog @FILE, or -prog - for stdin")
	case arg == "-":
		return io.ReadAll(os.Stdin)
	case strings.HasPrefix(arg, "@"):
		return os.ReadFile(arg[1:])
	}
	return []byte(arg), nil
}

func simple(c *Client, req Request, ok string, stdout, stderr io.Writer, asJSON bool) int {
	res, err := c.Do(req)
	if code := fail(stderr, res, err); code != 0 {
		return code
	}
	if asJSON {
		return emit(stdout, map[string]any{"ok": true, "version": res.Version})
	}
	if ok == "" {
		fmt.Fprintln(stdout, res.Version) // version: the number is the answer
		return 0
	}
	fmt.Fprintln(stdout, ok)
	return 0
}

// edit is the reason this file exists: a string replacement, resolved to
// offsets against the version it was read at.
func edit(c *Client, path, old, newText string, all bool, stdout, stderr io.Writer, asJSON bool) int {
	res, err := c.Do(Request{Op: "text", Path: path})
	if code := fail(stderr, res, err); code != 0 {
		return code
	}
	offsets := IndexAll(res.Text(), old)
	switch {
	case len(offsets) == 0:
		fmt.Fprintln(stderr, "raj ctl edit: that text does not appear in the buffer.\n"+
			"Read it again and copy the text exactly, including indentation — this file\n"+
			"may indent with tabs where you assumed spaces.")
		return 1
	case len(offsets) > 1 && !all:
		fmt.Fprintf(stderr, "raj ctl edit: that text appears %d times. Include more surrounding\n"+
			"context to make it unique, or pass -all to replace every occurrence.\n", len(offsets))
		return 1
	}

	hunks := make([]Hunk, 0, len(offsets))
	for _, off := range offsets {
		hunks = append(hunks, Hunk{Start: off, End: off + len(old), Text: newText})
	}
	base := res.Version
	ap, err := c.Do(Request{Op: "apply", Path: path, Base: &base, Hunks: hunks})
	return reportApply(ap, err, len(hunks), stdout, stderr, asJSON)
}

// fail turns a transport error or an editor refusal into an exit code. They are
// both failures to the caller but different things: one means retry.
func fail(stderr io.Writer, res Response, err error) int {
	if err != nil {
		fmt.Fprintln(stderr, "raj ctl:", err)
		return 1
	}
	if res.Err != "" {
		fmt.Fprintln(stderr, "raj ctl:", res.Err)
		return 1
	}
	return 0
}

func emit(w io.Writer, v any) int {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return 1
	}
	return 0
}

// IndexAll returns every non-overlapping occurrence of needle in hay.
func IndexAll(hay, needle string) []int {
	if needle == "" {
		return nil
	}
	var out []int
	for off := 0; ; {
		i := strings.Index(hay[off:], needle)
		if i < 0 {
			return out
		}
		out = append(out, off+i)
		off += i + len(needle)
	}
}

// recv waits for the user to say something and prints it.
//
// It says hello first, unconditionally. The mailbox is keyed on the author id,
// and an anonymous connection gets a fresh id every time it dials — so a `recv`
// that skipped the handshake would park on a mailbox nothing has ever been
// posted to, and wait forever while the user watches their message go nowhere.
// Binding the identity is what makes "the message I sent while it was
// restarting" arrive.
//
// A driver wanting both this and ordinary requests needs two connections:
// Client.Do holds a mutex for the length of a call, so a parked recv would
// block every other verb on the same client. Two connections saying hello with
// the same identity are the same participant and share one mailbox, which is
// what makes that split free.
func recv(c *Client, identity, name string, wait time.Duration,
	stdout, stderr io.Writer, asJSON bool) int {
	hi, err := c.Do(Request{Op: "hello", Identity: identityOf(identity), Name: name})
	if code := fail(stderr, hi, err); code != 0 {
		return code
	}

	if wait > 0 {
		// Cancel rather than closing the connection: the server answers a
		// cancelled recv with a frame, so the client leaves the conversation
		// tidily instead of the editor discovering a dead socket.
		//
		// The retry is for a very short -wait: CancelCurrent needs the request
		// to be in flight, and the timer is armed just before Do writes it. One
		// re-arm covers the gap without a second synchronisation point.
		stop := make(chan struct{})
		defer close(stop)
		go func() {
			t := time.NewTimer(wait)
			defer t.Stop()
			for {
				select {
				case <-stop:
					return
				case <-t.C:
					// CancelCurrent is a no-op when nothing is in flight, and
					// reports no error for it, so the check is on the id.
					if int(c.cur.Load()) != 0 {
						_ = c.CancelCurrent()
						return
					}
					t.Reset(10 * time.Millisecond)
				}
			}
		}()
	}

	res, err := c.Do(Request{Op: "recv"})
	if err != nil {
		fmt.Fprintln(stderr, "raj ctl:", err)
		return 1
	}
	if res.Err == "cancelled" {
		// Nothing arrived in time. Distinguished from a refusal by the exit
		// code: 3 means "no message", so a polling loop can tell "the user said
		// nothing" from "the editor said no".
		if asJSON {
			return emit(stdout, []Message{})
		}
		return 3
	}
	if code := fail(stderr, res, err); code != 0 {
		return code
	}
	if asJSON {
		return emit(stdout, res.Messages)
	}
	for _, m := range res.Messages {
		fmt.Fprintln(stdout, m.Text)
	}
	return 0
}
