package control

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"raj/internal/syntax"
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
  buffers                    files open in the editor; a headless buffer has no tab
  read [path]...             one or more buffer views, loaded on demand; -annotated prints a run line per change set after the text; -start/-end/-lines for a span, shared by every target, and an out-of-range byte span refuses the call rather than clamping
  open <path>                show a file: load it and focus a tab; -create makes a buffer for a path not on disk; prints opened or created
  ls [path]                  list a directory's immediate children; a trailing / marks a directory, -hidden includes hidden entries
  mkdir <dir>                create a directory, and any missing parents, under the workspace root
  rename <old> <new>         move a file within the workspace, carrying an open clean buffer; alias: mv
  delete <path>              propose deleting a file for the user to review; claim-gated
  delete -withdraw <path>    retract a pending deletion you proposed
  deletions                  list the pending deletions (path and author)
  rmdir <dir>                propose removing a directory and its subtree for review; claim-gated
  rmdir -withdraw <dir>      retract a pending dir-removal you proposed
  rmdirs                     list the pending dir-removals (dir and author)
  proposals [-mine]          every pending proposal: change sets, deletions, dir-removals

  claim [path]...            set the working set; -add extends it, -clear releases it
  goto [path] LINE[:COL]      move the editor's cursor; out-of-range clamps
  close [path]                close a buffer; refused while it has unsaved work, -discard drops it anyway and reports whether the file remains
  whoami                     the author id this connection writes as
  register [-as KEY]         mint an explicit identity key; -as binds a chosen one
  token                      the running server's TCP token, read from a local socket
  who [-live]                everyone writing in this workspace; -live filters to connected
  recv                       wait for the user to say something, then print it
  groups [path]              change sets in a buffer, and their state
  accept [path] -group N     agree to a change set; -all for every pending one
  reject [path] -group N     mark a set rejected; -all for every pending one
  clear [path] -group N      hard-purge a rejected change set; -all for every rejected one
  revert [path] [-author N]  discard your own pieces; the inverse of attribution
  diff [path]                pending change sets as old→new text, for review
  review [path]              enter review mode and list pending change sets; -json lists without entering
  search -q PATTERN          search the workspace, unsaved edits included; -hidden includes hidden paths; -context N adds N lines either side of each hit
  search -q PATTERN -path DIR
                             search only DIR, under the workspace root
  version [path]             the version a later apply bases on
  dump [path]                snapshot a span (or the whole file) for later patch
  patch [path] -dump N       replace a snapshot's text; the editor diffs and applies
  lsp MODE [path] LINE:COL   hover, definition, declaration, type-definition, implementation, references, completion, signature, diagnostics, inlay-hints, symbols, format, range-format or document-symbols
  lsp symbols [path] LINE:COL [query]
                             project-wide symbols matching query, from the language server
  lsp document-symbols [path]
                             one file's declarations from the language server, as a tree
  lsp inlay-hints [path] [-lines A,B]
                             inlay hints for a file or a 1-based inclusive line range
  apply [path] -base N [-start N -end N [-text S | -text-file F | -- TEXT]]
                             replace bytes [start,end) with text; -- passes a
                             body beginning with "-"
  apply [path] -base N -hunks F
                             replace each span in a JSON Lines file: one
                             {"start":S,"end":E,"text":"..."} per line, - for stdin
  edit [path] -old S -new S  replace an exact string (convenience over apply);
                             -- OLD NEW passes strings beginning with "-", and
                             -verbatim strips one trailing newline from a file
  save [path]                write a buffer to disk
  exec -- CMD [ARGS...]      run a command; refused while buffers are unsaved
  stats                      what the exec policy has cost this session
  run -prog BYTES            run a program of opcodes; @FILE or - for stdin

read, version and lsp diagnostics load a file on demand, so they need no open
first and leave no tab; open is a request to show a file to the user, and a
pending proposal is what puts a tab on a buffer nobody asked to see.

Path may be omitted for the buffer the user is looking at.

Rejecting a change set is a decision, not an edit: reject marks the set
rejected and the text stays in the document. clear is the hard purge, reversing
the set out so the same text can be applied again — so redo work that came back
rejected with reject, clear, then apply once more. open -create is how a path
that is not on disk yet gets a buffer.

Attribution has an inverse: revert discards the live pieces one writer wrote,
reversing them out of the document and recording the reversal in the journal
rather than a second forward edit. It acts on your own pieces only (-mine, or
-author naming your own id); another writer's accepted text is dropped with
reject then clear, which is the user's decision to make.

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
	fs.Usage = func() {
		out := fs.Output()
		fmt.Fprintf(out, "usage: raj ctl %s", cmd)
		if op := verbOperand[cmd]; op != "" {
			fmt.Fprintf(out, " %s", op)
		}
		fmt.Fprintln(out, " [options]")
		if strings.Contains(verbOperand[cmd], "[path]") && cmd != "claim" {
			note := "\nwith no path, targets the buffer the user is looking at."
			if cmd == "ls" {
				note = "\nwith no path, lists the workspace root."
			}
			fmt.Fprintln(out, note)
		}

		fmt.Fprintln(out)
		fs.PrintDefaults()
	}
	socket := fs.String("socket", "", "path to the editor's control socket")
	addr := fs.String("addr", "", "the editor's control address: a socket path, or tcp://host:port")
	asJSON := fs.Bool("json", false, "machine-readable output")
	group := fs.Uint64("group", 0, "accept/reject/clear: the change set id, from `groups`")
	dumpID := fs.Uint64("dump", 0, "patch: the snapshot id, from `dump`")
	identity := fs.String("as", "", "identity to write as; the same one reconnecting keeps its author id")
	name := fs.String("name", "", "display name for this participant")
	dir := fs.String("dir", "", "exec: directory to run in, inside the workspace")
	query := fs.String("q", "", "search: the pattern")
	include := fs.String("include", "", "search: comma-separated globs to search")
	exclude := fs.String("exclude", "", "search: comma-separated globs to skip")
	searchPath := fs.String("path", "", "search: limit the walk to a directory under the workspace root; the scope itself is searched even when hidden")
	regex := fs.Bool("regex", false, "search: treat the pattern as a regular expression")
	matchCase := fs.Bool("case", false, "search: match case")
	word := fs.Bool("word", false, "search: whole words only")
	hiddenFlag := fs.Bool("hidden", false, "ls/search: include hidden files and directories")
	jsonl := fs.Bool("jsonl", false, "search: print each hit as one JSON object per line, as it arrives")
	base := fs.Uint64("base", 0, "apply: the version the offsets were measured in")
	start := fs.Int("start", -1, "apply/read: first byte of the span")
	end := fs.Int("end", -1, "apply/read: one past the last byte of the span")
	lines := fs.String("lines", "", "read: a line range, A or A,B (1-based inclusive); wins over -start/-end")
	context := fs.Int("context", 0, "search: lines of context before and after each hit; 0 is the hit line alone")
	annotated := fs.Bool("annotated", false, "read: report the change set and state of each run; -json adds them as states, plain adds run lines after the text")
	create := fs.Bool("create", false, "open: make a new buffer for a path that is not on disk yet")
	discard := fs.Bool("discard", false, "close: discard unsaved changes instead of refusing the close")
	withdraw := fs.Bool("withdraw", false, "delete: retract your pending deletion instead of proposing one")
	claimAdd := fs.Bool("add", false, "claim: extend the current set instead of replacing it")
	claimClear := fs.Bool("clear", false, "claim: release the whole set")
	textArg := fs.String("text", "", "apply: replacement text")
	progArg := fs.String("prog", "", "run: the program itself, or @FILE, or - for stdin")
	progHex := fs.String("hex", "", "run: the program as hex, for one whose payloads contain a zero byte")
	textFile := fs.String("text-file", "", "apply: read -text from a file, or - for stdin")
	hunksFile := fs.String("hunks", "", "apply: read hunks from a file of JSON Lines, one {start,end,text} per line, or - for stdin")
	old := fs.String("old", "", "edit: the exact existing text to replace")
	newText := fs.String("new", "", "edit: the replacement text")
	oldFile := fs.String("old-file", "", "edit: read -old from a file, or - for stdin")
	newFile := fs.String("new-file", "", "edit: read -new from a file, or - for stdin")
	all := fs.Bool("all", false, "edit: replace every occurrence; accept/reject/clear: every change set")
	mine := fs.Bool("mine", false, "groups, proposals, revert: only the connection's own work")
	revertAuthor := fs.Uint("author", 0, "revert: the writer whose pieces to drop; must be your own author id")
	state := fs.String("state", "", "groups: only sets in this state: proposed, accepted or rejected")
	pendingOnly := fs.Bool("pending", false, "groups: only sets still awaiting a decision")
	verbatim := fs.Bool("verbatim", false, "apply/edit: strip one trailing newline read from a file or stdin")
	live := fs.Bool("live", false, "who: only participants connected right now, not every id the process has minted")
	wait := fs.Duration("wait", 0, "recv: give up after this long; zero waits indefinitely")
	// Everything after "--" is another program's argv and must reach it intact:
	// `raj ctl exec -- go test -run X` has to give go its own -run, not have it
	// parsed as ours or shuffled by reorder.
	var argv []string
	if cmd == "exec" {
		if i := indexOf(rest, "--"); i >= 0 {
			argv, rest = rest[i+1:], rest[:i]
		}
	}
	// edit and apply take a replacement body that may begin with "-" or "--".
	// Everything after a "--" is that body, taken literally: it never reaches
	// reorder or the flag package, so it cannot be read as a flag nor refused
	// as a stray operand.
	var textOperands []string
	if cmd == "edit" || cmd == "apply" {
		if i := indexOf(rest, "--"); i >= 0 {
			textOperands = append([]string(nil), rest[i+1:]...)
			rest = rest[:i]
		}
	}
	if err := fs.Parse(reorder(fs, rest)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	path := fs.Arg(0)
	if code := refuseExtraArgs(cmd, fs, stderr); code != 0 {
		return code
	}

	if cmd == "help" || cmd == "-h" || cmd == "--help" {
		fmt.Fprint(stdout, ctlUsage)
		return 0
	}
	if cmd == "list" {
		return list(stdout, stderr, *asJSON)
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

	// Bind-first: hello before any other verb, so the connection writes as
	// the durable identity the harness pinned with RAJ_IDENTITY (or -as)
	// rather than as a fresh anonymous id every invocation. Nothing pinned
	// goes out empty, and a TCP server mints a token and hands it back; adopt
	// it for the rest of the process and say so once, so the harness can pin
	// it next run. A failed hello changes nothing: the connection works on
	// anonymously, exactly as it did before this handshake.
	// register is the explicit path: it mints and binds its own key below, so
	// the generic bind is skipped and no anonymous adopted token is printed.
	if cmd != "register" {
		bind := firstOf(*identity, os.Getenv("RAJ_IDENTITY"))
		if hi, err := c.Do(Request{Op: "hello", Identity: bind, Name: *name}); err == nil && hi.Err == "" {
			if bind == "" && hi.Identity != "" {
				adoptedIdentity = hi.Identity
				fmt.Fprintf(stderr, "set RAJ_IDENTITY=%s\n", hi.Identity)
			}
			warnVersionSkew(stderr, hi.SrcVersion)
		}
	}

	switch cmd {
	case "buffers":
		return buffers(c, stdout, stderr, *asJSON)
	case "read":
		return read(c, fs.Args(), *start, *end, *lines, *annotated, stdout, stderr, *asJSON)
	case "open":
		if path == "" {
			fmt.Fprintln(stderr, "raj ctl open: needs a path")
			return 2
		}
		return openCmd(c, path, *create, stdout, stderr, *asJSON)
	case "mkdir":
		if path == "" {
			fmt.Fprintln(stderr, "raj ctl mkdir: needs a path")
			return 2
		}
		return simple(c, Request{Op: "mkdir", Path: path}, "created "+path, stdout, stderr, *asJSON)
	case "ls":
		return lsCmd(c, path, *hiddenFlag, stdout, stderr, *asJSON)
	case "rename", "mv":
		if path == "" || fs.Arg(1) == "" {
			fmt.Fprintf(stderr, "raj ctl %s: needs an old and a new path\n", cmd)
			return 2
		}
		return renameCmd(c, path, fs.Arg(1), stdout, stderr, *asJSON)
	case "delete":
		if path == "" {
			fmt.Fprintln(stderr, "raj ctl delete: needs a path")
			return 2
		}
		return deleteCmd(c, path, *withdraw, stdout, stderr, *asJSON)
	case "deletions":
		return deletionsCmd(c, stdout, stderr, *asJSON)
	case "rmdir":
		if path == "" {
			fmt.Fprintln(stderr, "raj ctl rmdir: needs a directory path")
			return 2
		}
		return rmdirCmd(c, path, *withdraw, stdout, stderr, *asJSON)
	case "rmdirs":
		return rmdirsCmd(c, stdout, stderr, *asJSON)
	case "proposals":
		res, err := c.Do(Request{Op: "proposals"})
		if code := fail(stderr, res, err); code != 0 {
			return code
		}
		props := res.Proposals
		if props == nil {
			props = []Proposal{}
		}
		if *mine {
			props = mineProposals(props, c.Author())
			if props == nil {
				props = []Proposal{}
			}
		}
		if *asJSON {
			return emit(stdout, props)
		}
		if len(props) == 0 {
			fmt.Fprintln(stdout, "no pending proposals")
			return 0
		}
		for _, k := range []struct{ kind, label string }{
			{"set", "change sets:"},
			{"delete", "pending deletions:"},
			{"rmdir", "pending dir-removals:"},
		} {
			head := false
			for _, p := range props {
				if p.Kind != k.kind {
					continue
				}
				if !head {
					fmt.Fprintln(stdout, k.label)
					head = true
				}
				switch {
				case p.Kind == "set" && p.Start >= 0:
					fmt.Fprintf(stdout, "%s\tgroup %d\tauthor %d\tbytes %d..%d\n", p.Path, p.Group, p.Author, p.Start, p.End)
				case p.Kind == "set":
					fmt.Fprintf(stdout, "%s\tgroup %d\tauthor %d\n", p.Path, p.Group, p.Author)
				default:
					fmt.Fprintf(stdout, "%s\tauthor %d\n", p.Path, p.Author)
				}
			}
		}
		return 0
	case "goto":
		if pos := fs.Arg(1); pos != "" {
			line, col, ok := ctlPosition(pos)
			if !ok {
				fmt.Fprintln(stderr, "raj ctl goto: expects LINE[:COL]")
				return 2
			}
			res, err := c.Do(Request{Op: "goto", Path: path, Line: line, Col: col})
			if code := fail(stderr, res, err); code != 0 {
				return code
			}
			if *asJSON {
				return emit(stdout, map[string]any{"ok": true, "line": line, "col": col})
			}
			where := targetName(c, path, "active buffer")
			if line > 0 {
				colText := ""
				if col > 0 {
					colText = fmt.Sprintf(":%d", col)
				}
				fmt.Fprintf(stdout, "moved %s cursor to %d%s\n", where, line, colText)
			} else {
				fmt.Fprintf(stdout, "moved %s cursor to column %d\n", where, col)
			}
			return 0
		}
		fmt.Fprintln(stderr, "raj ctl goto: expects LINE[:COL]")
		return 2
	case "close":
		return closeCmd(c, path, *discard, stdout, stderr, *asJSON)
	case "claim":
		return claimCmd(c, fs.Args(), *claimAdd, *claimClear, stdout, stderr, *asJSON)
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
		if *state != "" {
			switch *state {
			case "proposed", "accepted", "rejected":
			default:
				fmt.Fprintf(stderr, "raj ctl groups: -state must be proposed, accepted or rejected, not %q\n", *state)
				return 2
			}
		}
		if *pendingOnly {
			if *state != "" && *state != "proposed" {
				fmt.Fprintln(stderr, "raj ctl groups: -pending and -state conflict unless -state is proposed")
				return 2
			}
			*state = "proposed"
		}
		res, err := c.Do(Request{Op: "groups", Path: path})
		if code := fail(stderr, res, err); code != 0 {
			return code
		}
		if *mine {
			res.Groups = mineOnly(res.Groups, c.Author())
		}
		if res.Groups == nil {
			res.Groups = []Group{}
		}
		if *state != "" {
			kept := make([]Group, 0, len(res.Groups))
			for _, g := range res.Groups {
				if g.State == *state {
					kept = append(kept, g)
				}
			}
			res.Groups = kept
		}
		if *asJSON {
			return emit(stdout, res.Groups)
		}
		for _, g := range res.Groups {
			fmt.Fprintf(stdout, "%d\tauthor %d\t%s\t%d ops\t%+d bytes\t%d hunks\t%d moved\n",
				g.ID, g.Author, g.State, g.Ops, g.Bytes, g.Hunks, g.Moved)
			if g.Overlaps != nil {
				// An overlap is reported, not resolved: two sets still awaiting
				// a decision that claim the same bytes is a fact the person
				// deciding them has to see.
				for _, o := range g.Overlaps.Sets {
					fmt.Fprintf(stdout, "  overlaps change set %d (author %d) at bytes %d..%d\n",
						o.Group, o.Author, o.Start, o.End)
				}
			}
			if g.Invalid {
				// Superseded, not decided: the set is still proposed but every
				// hunk is gone, so there is nothing to accept. Name the live
				// edit that consumed it, so clearing that is an actionable next
				// step rather than a bare state.
				if g.InvalidBy != nil {
					fmt.Fprintf(stdout, "  invalid: superseded by change set %d (author %d) at bytes %d..%d\n",
						g.InvalidBy.Group, g.InvalidBy.Author, g.InvalidBy.Start, g.InvalidBy.End)
				} else {
					fmt.Fprintln(stdout, "  invalid: superseded; no colliding set can be named")
				}
			}
		}
		return 0
	case "accept", "reject":
		if *all {
			return decideAll(c, cmd, path, *mine, *group, stdout, stderr, *asJSON)
		}
		return simple(c, Request{Op: cmd, Path: path, Group: *group}, cmd+"ed",
			stdout, stderr, *asJSON)
	case "clear":
		if *all {
			return decideAll(c, cmd, path, *mine, *group, stdout, stderr, *asJSON)
		}
		return clearCmd(c, path, *group, stdout, stderr, *asJSON)
	case "revert":
		return revertCmd(c, path, uint8(*revertAuthor), *mine, stdout, stderr, *asJSON)
	case "diff":
		return diffCmd(c, path, stdout, stderr, *asJSON)
	case "review":
		return reviewCmd(c, path, *asJSON, stdout, stderr)
	case "who":
		res, err := c.Do(Request{Op: "hello", Identity: identityOf(*identity), Name: *name})
		if code := fail(stderr, res, err); code != 0 {
			return code
		}
		if *live {
			// Client-side on purpose: the registry's full listing is the
			// attribution record — a gone participant's text is still in the
			// document — so the wire answer keeps every row and the flag is a
			// view, not a change to what the server reports.
			here := make([]Participant, 0, len(res.Participants))
			for _, p := range res.Participants {
				if p.Connected {
					here = append(here, p)
				}
			}
			res.Participants = here
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
	case "token":
		// The secret is the server's, not this connection's, so there is
		// nothing to resolve and no path involved. Printing it alone on the
		// line is the point: `TOKEN=$(raj ctl token)` should not have to strip
		// prose. A Unix-only server has no secret and prints an empty line.
		res, err := c.Do(Request{Op: "token"})
		if code := fail(stderr, res, err); code != 0 {
			return code
		}
		if *asJSON {
			return emit(stdout, map[string]any{"token": res.Token})
		}
		fmt.Fprintln(stdout, res.Token)
		return 0
	case "register":
		return registerCmd(c, *identity, *name, stdout, stderr, *asJSON)
	case "search":
		// A bare positional is never a path here — search takes none — so it
		// is the pattern typed in the wrong place, and accepting it silently
		// would run a different search than the one that was meant.
		if arg := fs.Arg(0); arg != "" {
			fmt.Fprintf(stderr, "search: unexpected argument %q — the pattern goes to -q; to limit paths use -include or -path\n", arg)
			return 2
		}
		if *context < 0 {
			fmt.Fprintln(stderr, "raj ctl search: -context cannot be negative")
			return 2
		}
		return doSearch(c, SearchQuery{Text: *query, Include: *include, Exclude: *exclude,
			Path: *searchPath, Hidden: *hiddenFlag, Context: *context,
			Regex: *regex, Case: *matchCase, Word: *word}, *jsonl, *asJSON, stdout, stderr)
	case "version":
		res, err := c.Do(Request{Op: "version", Path: path})
		if code := fail(stderr, res, err); code != 0 {
			return code
		}
		if *asJSON {
			return emit(stdout, map[string]any{
				"ok": true, "version": res.Version,
				"bytes": res.Bytes, "lines": res.Lines,
			})
		}
		fmt.Fprintln(stdout, res.Version) // the number is the answer
		return 0
	case "dump":
		return dumpCmd(c, path, *start, *end, stdout, stderr, *asJSON)
	case "patch":
		return patchCmd(c, path, *dumpID, *textArg, *textFile, stdout, stderr, *asJSON)
	case "lsp":
		return doLSP(c, fs.Arg(0), fs.Arg(1), fs.Arg(2), *lines, fs.Arg(3), stdout, stderr, *asJSON)
	case "apply":
		return apply(c, path, *base, *start, *end, *textArg, *textFile, *hunksFile,
			textOperands, *verbatim, fs, stdout, stderr, *asJSON)
	case "edit":
		o, n, code := editText(*old, *newText, *oldFile, *newFile, textOperands, *verbatim, stderr)
		if code != 0 {
			return code
		}
		return edit(c, path, o, n, *all, stdout, stderr, *asJSON)
	}
	fmt.Fprintf(stderr, "raj ctl: unknown command %q\n\n%s", cmd, ctlUsage)
	return 2
}

// claimCmd sets, extends, clears or reports this identity's claim set: the
// files it declares it is working on. Claims are not locks — another writer
// may hold the same file — so an overlap is reported rather than refused, and a
// path that is neither on disk nor an open buffer is warned about and skipped,
// leaving the rest of the command to land.
func claimCmd(c *Client, paths []string, add, clear bool, stdout, stderr io.Writer, asJSON bool) int {
	if add && clear {
		fmt.Fprintln(stderr, "raj ctl claim: -add and -clear are alternatives")
		return 2
	}
	res, err := c.Do(Request{Op: "claim", Paths: paths, ClaimAdd: add, ClaimClear: clear})
	if code := fail(stderr, res, err); code != 0 {
		return code
	}
	claims := res.Claims
	if claims == nil {
		claims = []string{}
	}
	if asJSON {
		out := map[string]any{"ok": true, "claims": claims}
		if len(res.ClaimWarnings) > 0 {
			out["warnings"] = res.ClaimWarnings
		}
		if len(res.ClaimOverlaps) > 0 {
			out["overlaps"] = res.ClaimOverlaps
		}
		return emit(stdout, out)
	}
	if len(claims) == 0 {
		fmt.Fprintln(stdout, "claim set is empty")
	} else {
		fmt.Fprintf(stdout, "claimed %d file(s):\n", len(claims))
		for _, p := range claims {
			fmt.Fprintf(stdout, "  %s\n", p)
		}
	}
	for _, w := range res.ClaimWarnings {
		fmt.Fprintf(stderr, "raj ctl claim: warning — %s\n", w)
	}
	for _, o := range res.ClaimOverlaps {
		who := o.Identity
		if who == "" {
			who = fmt.Sprintf("author %d", o.Author)
		}
		fmt.Fprintf(stderr, "raj ctl claim: %s is also claimed by %s\n", o.Path, who)
	}
	return 0
}

// deleteCmd proposes a pending deletion, or withdraws one this identity
// proposed. The file is not removed: delete is a review primitive, and the
// user approves the removal in the editor. The path is claim-gated in the
// Guard, so an agent can only propose removing a file it declared.
func deleteCmd(c *Client, path string, withdraw bool, stdout, stderr io.Writer, asJSON bool) int {
	res, err := c.Do(Request{Op: "delete", Path: path, Withdraw: withdraw})
	if code := fail(stderr, res, err); code != 0 {
		return code
	}
	if asJSON {
		return emit(stdout, map[string]any{"ok": true, "path": path, "withdraw": withdraw})
	}
	if withdraw {
		fmt.Fprintf(stdout, "withdrew the pending deletion of %s\n", path)
		return 0
	}
	fmt.Fprintf(stdout, "proposed deleting %s; the file stays until the user approves\n", path)
	return 0
}

// deletionsCmd lists the pending deletions, naming the path and the author who
// proposed it, so a driver can see them without opening the file. The listing
// is ungated; whether a caller may withdraw one is the Guard's business.
func deletionsCmd(c *Client, stdout, stderr io.Writer, asJSON bool) int {
	res, err := c.Do(Request{Op: "deletions"})
	if code := fail(stderr, res, err); code != 0 {
		return code
	}
	deletions := res.Deletions
	if deletions == nil {
		deletions = []Deletion{}
	}
	if asJSON {
		return emit(stdout, deletions)
	}
	if len(deletions) == 0 {
		fmt.Fprintln(stdout, "no pending deletions")
		return 0
	}
	for _, d := range deletions {
		fmt.Fprintf(stdout, "%s\t(proposed by author %d)\n", d.Path, d.Author)
	}
	return 0
}

// rmdirCmd proposes a pending dir-removal, or withdraws one this identity
// proposed. Nothing is removed: rmdir is a review primitive, and the user
// approves the removal in the editor's review tab. The path is claim-gated in
// the Guard, with the directory itself as the claim entry (§11).
func rmdirCmd(c *Client, path string, withdraw bool, stdout, stderr io.Writer, asJSON bool) int {
	res, err := c.Do(Request{Op: "rmdir", Path: path, Withdraw: withdraw})
	if code := fail(stderr, res, err); code != 0 {
		return code
	}
	if asJSON {
		return emit(stdout, map[string]any{"ok": true, "path": path, "withdraw": withdraw})
	}
	if withdraw {
		fmt.Fprintf(stdout, "withdrew the pending dir-removal of %s\n", path)
		return 0
	}
	fmt.Fprintf(stdout, "proposed removing %s; the directory stays until the user approves\n", path)
	return 0
}

// rmdirsCmd lists the pending dir-removals, naming the directory and the
// author who proposed it, so a driver can see them without opening anything.
// The listing is ungated; whether a caller may withdraw one is the Guard's
// business.
func rmdirsCmd(c *Client, stdout, stderr io.Writer, asJSON bool) int {
	res, err := c.Do(Request{Op: "rmdirs"})
	if code := fail(stderr, res, err); code != 0 {
		return code
	}
	removals := res.DirRemovals
	if removals == nil {
		removals = []DirRemoval{}
	}
	if asJSON {
		return emit(stdout, removals)
	}
	if len(removals) == 0 {
		fmt.Fprintln(stdout, "no pending dir-removals")
		return 0
	}
	for _, d := range removals {
		fmt.Fprintf(stdout, "%s\t(proposed by author %d)\n", d.Path, d.Author)
	}
	return 0
}

// lsCmd lists a directory's children, one per line, with a trailing slash on
// each directory so the two kinds are told apart without a stat. The listing
// applies the shared hidden policy unless -hidden drops it. The JSON form
// carries the name, path, kind and a regular file's size; a directory, a
// symlink and a special file omit the size rather than reporting a number that
// is not their content.
func lsCmd(c *Client, path string, all bool, stdout, stderr io.Writer, asJSON bool) int {
	res, err := c.Do(Request{Op: "ls", Path: path, Hidden: all})
	if code := fail(stderr, res, err); code != 0 {
		return code
	}
	entries := res.Entries
	if entries == nil {
		entries = []Entry{}
	}
	if asJSON {
		return emit(stdout, entries)
	}
	for _, e := range entries {
		if e.Dir {
			fmt.Fprintf(stdout, "%s/\n", e.Name)
			continue
		}
		fmt.Fprintln(stdout, e.Name)
	}
	return 0
}

// renameCmd moves a file within the workspace, carrying an open clean buffer
// with it. Two paths cross the wire: the old in Path and the new in NewPath.
// The Guard gates the old on the caller's claim set and refuses a destination
// that already exists, so this is the same working-set discipline as a write.
func renameCmd(c *Client, old, newPath string, stdout, stderr io.Writer, asJSON bool) int {
	res, err := c.Do(Request{Op: "rename", Path: old, NewPath: newPath})
	if code := fail(stderr, res, err); code != 0 {
		return code
	}
	if asJSON {
		return emit(stdout, map[string]any{"ok": true, "old": old, "new": newPath})
	}
	fmt.Fprintf(stdout, "renamed %s to %s\n", old, newPath)
	return 0
}

// argLimit is how many positional operands a verb takes, and what to say about
// one it does not. A verb absent from the map takes any number; goto and lsp
// are absent too, because their extra operands are positions rather than a
// stray span, and read is absent because extra operands are extra targets
// rather than a stray span.
//
// The case that matters is a byte span written as operands. `raj ctl dump path
// 940 1000` used to read the whole file and look like it worked, with two
// numbers that look like offsets dump accepts; refusing the second operand and
// naming the flags turns a silently wrong read into a usage error.
var argLimit = map[string]struct {
	n    int
	hint string
}{
	"dump":      {1, "dump takes a path; the span goes to -start/-end"},
	"version":   {1, "version takes a path and nothing else"},
	"open":      {1, "open takes a path and nothing else"},
	"mkdir":     {1, "mkdir takes a directory path and nothing else"},
	"delete":    {1, "delete takes a path; pass -withdraw to retract a proposal"},
	"deletions": {0, "deletions takes no operands; it lists every pending deletion"},
	"rmdir":     {1, "rmdir takes a directory path; pass -withdraw to retract a proposal"},
	"rmdirs":    {0, "rmdirs takes no operands; it lists every pending dir-removal"},
	"ls":        {1, "ls takes a directory path and nothing else"},
	"proposals": {0, "proposals takes no operands; -mine limits the list to this identity's own"},
	"rename":    {2, "rename takes <old> and <new> paths"},
	"mv":        {2, "mv takes <old> and <new> paths"},
	"close":     {1, "close takes a path and nothing else"},
	"save":      {1, "save takes a path and nothing else"},
	"groups":    {1, "groups takes a path and nothing else"},
	"accept":    {1, "accept takes a path; the change set id goes to -group"},
	"reject":    {1, "reject takes a path; the change set id goes to -group"},
	"clear":     {1, "clear takes a path; the change set id goes to -group"},
	"revert":    {1, "revert takes a path; -mine or -author selects the writer"},
	"diff":      {1, "diff takes a path and nothing else"},
	"review":    {1, "review takes a path and nothing else"},
	"patch":     {1, "patch takes a path; the snapshot id goes to -dump"},
	"apply":     {1, "apply takes a path; offsets go to -base/-start/-end, and text to -text/-text-file or after --"},
	"edit":      {1, "edit takes a path; the strings go to -old/-new, or both after --"},
	"register":  {0, "register takes no path; the key goes to -as, or one is minted"},
	"token":     {0, "token takes no operands; it reads the running server's TCP token"},
}

// verbOperand is each verb's positional operands, spelled as the top-level
// usage spells them. It exists for the per-verb usage: the flag package prints
// only flags, so without it `raj ctl edit -h` never shows that edit can take a
// path, nor that omitting one targets the buffer on screen. A verb absent from
// the map takes no positional.
var verbOperand = map[string]string{
	"read":     "[path]...",
	"open":     "<path>",
	"mkdir":    "<dir>",
	"rename":   "<old> <new>",
	"mv":       "<old> <new>",
	"delete":   "<path>",
	"rmdir":    "<dir>",
	"ls":       "[path]",
	"claim":    "[path]...",
	"goto":     "[path] LINE[:COL]",
	"close":    "[path]",
	"groups":   "[path]",
	"accept":   "[path]",
	"reject":   "[path]",
	"clear":    "[path]",
	"revert":   "[path]",
	"diff":     "[path]",
	"review":   "[path]",
	"version":  "[path]",
	"dump":     "[path]",
	"patch":    "[path]",
	"apply":    "[path] [TEXT]",
	"edit":     "[path] [OLD NEW]",
	"save":     "[path]",
	"lsp":      "MODE [path] LINE:COL [query]",
	"register": "[-as KEY]",
}

// refuseExtraArgs rejects a positional operand the verb does not take, so a
// stray one cannot be ignored while the command appears to succeed.
func refuseExtraArgs(cmd string, fs *flag.FlagSet, stderr io.Writer) int {
	lim, ok := argLimit[cmd]
	if !ok || fs.NArg() <= lim.n {
		return 0
	}
	fmt.Fprintf(stderr, "%s: unexpected argument %q — %s\n", cmd, fs.Arg(lim.n), lim.hint)
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

// adoptedIdentity is the token the server minted for this process when
// nothing was pinned, kept so a later hello on another connection — recv,
// say — rebinds to the same participant instead of minting again.
var adoptedIdentity string

// identityOf falls back to the environment, so a harness sets RAJ_IDENTITY once
// rather than passing -as on every call, then to the token the server minted
// this run; a harness that forgets all three stays anonymous rather than
// broken.
func identityOf(flagVal string) string {
	if flagVal != "" {
		return flagVal
	}
	if env := os.Getenv("RAJ_IDENTITY"); env != "" {
		return env
	}
	if adoptedIdentity != "" {
		return adoptedIdentity
	}
	return "anon-cli"
}

// registerCmd mints an explicit short identity key and binds it with a hello,
// so the caller can pass -as <key> on every later call and keep one author id.
// A minted key is drawn client-side, short, lowercase hex and shell-safe, and
// is checked against the registry so it cannot land on an existing identity; a
// caller-chosen key (-as) binds that instead, deliberately and without the
// check, which makes register idempotent for that key.
func registerCmd(c *Client, chosen, name string, stdout, stderr io.Writer, asJSON bool) int {
	if name == "" {
		name = "raj"
	}
	key := chosen
	if key == "" {
		minted, err := mintRegisterKey(c, randomRegisterKey)
		if err != nil {
			fmt.Fprintln(stderr, "raj ctl register:", err)
			return 1
		}
		key = minted
	}
	res, err := c.Do(Request{Op: "hello", Identity: key, Name: name})
	if code := fail(stderr, res, err); code != 0 {
		return code
	}
	warnVersionSkew(stderr, res.SrcVersion)
	if asJSON {
		return emit(stdout, map[string]any{"key": key, "author": res.Author, "name": name})
	}
	fmt.Fprintf(stdout, "key: %s\nauthor: %d\nuse it on every call: raj ctl <verb> -as %s ...\n",
		key, res.Author, key)
	return 0
}

// registerMintAttempts bounds how many random keys register will draw before
// giving up, so a registry that somehow owns every candidate cannot spin.
const registerMintAttempts = 5

// randomRegisterKey draws one short key: raj- plus four random bytes as
// lowercase hex.
func randomRegisterKey() (string, error) {
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "raj-" + hex.EncodeToString(buf), nil
}

// mintRegisterKey draws a key no listed participant already owns. It reads the
// registry with the same hello the who verb sends — the only op that carries
// the participant list — and redraws on a collision, up to
// registerMintAttempts times. The listing hello carries no identity of its
// own, so it does not join or adopt one just to look; on a local socket an
// empty identity is how the server lists without binding.
func mintRegisterKey(c *Client, draw func() (string, error)) (string, error) {
	for attempt := 0; attempt < registerMintAttempts; attempt++ {
		key, err := draw()
		if err != nil {
			return "", err
		}
		res, err := c.Do(Request{Op: "hello"})
		if err != nil {
			return "", err
		}
		if res.Err != "" {
			return "", errors.New(res.Err)
		}
		taken := false
		for _, p := range res.Participants {
			if p.Identity == key {
				// A gone row counts too: joining it would hand back an author
				// id another writer already used.
				taken = true
				break
			}
		}
		if !taken {
			return key, nil
		}
	}
	return "", fmt.Errorf("could not find an unused key in %d attempts; pass -as KEY to choose one", registerMintAttempts)
}

// warnVersionSkew notes a ctl/editor build mismatch. A warning, not a refusal:
// a stale pair still works for the verbs that did not change, and which those
// are is for the user to decide. Empty on either side means unknown, and the
// check stays silent.
func warnVersionSkew(stderr io.Writer, server string) {
	if srcVersion == "" || server == "" || srcVersion == server {
		return
	}
	fmt.Fprintf(stderr, "raj ctl: warning — ctl is built from %s, the editor from %s; "+
		"verbs that changed between the two may misbehave\n", srcVersion, server)
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

// ctlLines parses a read line range: "A" reads from line A to the end, and
// "A,B" reads the inclusive 1-based range. hasEnd is false for the bare "A"
// form, so the editor reads to the end of the file.
func ctlLines(s string) (start, end int, hasEnd, ok bool) {
	a, b, found := strings.Cut(s, ",")
	if a == "" {
		return 0, 0, false, false
	}
	var e int
	if start, ok = ctlAtoi(strings.TrimSpace(a)); !ok || start < 1 {
		return 0, 0, false, false
	}
	if !found {
		return start, 0, false, true
	}
	if e, ok = ctlAtoi(strings.TrimSpace(b)); !ok || e < start {
		return 0, 0, false, false
	}
	return start, e, true, true
}

// ctlPosition reads "line", "line:col" or ":col" for goto. A missing part is
// 0, meaning "the editor decides": a bare ":40" is a column on the line the
// cursor is already on, exactly as in the editor's own goto-line prompt.
func ctlPosition(s string) (line, col int, ok bool) {
	lineText, colText := s, ""
	if i := strings.IndexByte(s, ':'); i >= 0 {
		lineText, colText = s[:i], s[i+1:]
	}
	if lineText != "" {
		if line, ok = ctlAtoi(lineText); !ok {
			return 0, 0, false
		}
	}
	if colText != "" {
		if col, ok = ctlAtoi(colText); !ok {
			return 0, 0, false
		}
	}
	return line, col, line > 0 || col > 0
}

// ctlAtoi accepts a non-negative decimal and nothing else, like the prompt
// parser it mirrors.
func ctlAtoi(s string) (int, bool) {
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		n = n*10 + int(s[i]-'0')
		if n > 1<<30 {
			return 1 << 30, true // clamped below anyway
		}
	}
	return n, len(s) > 0
}

// readTextFile reads a -text-file/-old-file/-new-file argument, with "-"
// meaning stdin. verbatim strips exactly the one trailing newline a heredoc or
// echo appends; without it the bytes are handed over untouched, so callers
// that already account for the newline see no change.
func readTextFile(name string, verbatim bool) (string, error) {
	var data []byte
	var err error
	if name == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(name)
	}
	if err != nil {
		return "", err
	}
	s := string(data)
	if verbatim {
		s = strings.TrimSuffix(s, "\n")
	}
	return s, nil
}

// editText resolves -old/-new against their file forms. Flags carry a one-line
// replacement fine; anything with newlines in it is painful to quote through a
// shell, and an agent writing a heredoc into a file is the shape that works.
//
// pos are the operands after the path, which fill -old then -new when those
// are not given; they are how a body beginning with "-" gets in past the flag
// parser, as `edit PATH -- OLD NEW`. verbatim strips one trailing newline from
// a file or stdin read, the one a heredoc or echo appends.
func editText(old, newText, oldFile, newFile string, pos []string, verbatim bool, stderr io.Writer) (string, string, int) {
	next := 0
	take := func() string {
		if next >= len(pos) {
			return ""
		}
		s := pos[next]
		next++
		return s
	}
	if old == "" && oldFile == "" {
		old = take()
	}
	if newText == "" && newFile == "" {
		newText = take()
	}
	if next < len(pos) {
		fmt.Fprintf(stderr, "raj ctl edit: unexpected argument %q — the strings go to -old/-new, or as OLD NEW operands after --\n", pos[next])
		return "", "", 2
	}
	readArg := func(flagVal, file, name string) (string, int) {
		if file == "" {
			return flagVal, 0
		}
		if flagVal != "" {
			fmt.Fprintf(stderr, "raj ctl edit: -%s and -%s-file are alternatives\n", name, name)
			return "", 2
		}
		s, err := readTextFile(file, verbatim)
		if err != nil {
			fmt.Fprintln(stderr, "raj ctl edit:", err)
			return "", 1
		}
		return s, 0
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

// bulkOutcome is one change set's decision in a bulk accept or reject, in the
// order it was attempted. Error is empty for a decision that landed.
type bulkOutcome struct {
	ID    uint64 `json:"id"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// decideAll accepts, rejects or clears the path's change sets in one command.
// It is the client-side bulk form: one listing, then one frame per set, so
// deciding k sets is one invocation rather than 1+k. The server still decides
// each set on its own terms — one refusal does not take the rest with it, and a
// set that fails is named rather than only counted.
//
// Accepting is order-independent: the text is already in the document, so the
// one listing `groups` gives is decided as it comes. Rejecting is not.
// Reversing a set rebases its inverse through everything that landed after it,
// so a set can only come out once every later edit that overlaps it is already
// gone. The pending projection (`diff`) is walked newest-first and re-read
// after each reversal, so the next attempt sees the document as it now is. It
// is `diff`, not `groups`, because `groups` also lists accepted sets and sets a
// later edit has fully overwritten; attempting either is the refusal that made
// a bulk reject look wedged when its work was already done.
//
// Clearing is a reversal too, so it also unwinds newest-first, but it works
// from `groups` filtered to the rejected sets rather than from `diff`: a
// rejected set is exactly what ClearRejected acts on, and `diff` reports only
// what is still proposed.
func decideAll(c *Client, op, path string, mine bool, group uint64,
	stdout, stderr io.Writer, asJSON bool) int {
	if group != 0 {
		fmt.Fprintf(stderr, "raj ctl %s: -all and -group are alternatives\n", op)
		return 2
	}
	verb := op + "ed"
	var out []bulkOutcome
	var failed []uint64
	// decide performs one decision and records it, printing each as it lands so
	// a person watching sees the outcomes rather than one batch at the end.
	decide := func(id uint64) {
		dres, derr := c.Do(Request{Op: op, Path: path, Group: id})
		switch {
		case derr != nil:
			out = append(out, bulkOutcome{ID: id, Error: derr.Error()})
			failed = append(failed, id)
			if !asJSON {
				fmt.Fprintf(stderr, "raj ctl %s: change set %d: %v\n", op, id, derr)
			}
		case dres.Err != "":
			out = append(out, bulkOutcome{ID: id, Error: dres.Err})
			failed = append(failed, id)
			if !asJSON {
				fmt.Fprintf(stderr, "raj ctl %s: change set %d: %s\n", op, id, dres.Err)
			}
		default:
			out = append(out, bulkOutcome{ID: id, OK: true})
			if !asJSON {
				fmt.Fprintf(stdout, "%s change set %d\n", verb, id)
			}
		}
	}
	if op == "reject" {
		// Newest-first, re-reading the pending list after each reversal. A set
		// already attempted is not retried in this invocation: if it could not
		// come out when it was newest, nothing older can free it.
		attempted := map[uint64]bool{}
		for {
			pending, code := pendingRejections(c, path, mine, stderr)
			if code != 0 {
				return code
			}
			id, ok := newestRejection(pending, attempted)
			if !ok {
				break
			}
			attempted[id] = true
			decide(id)
		}
	} else {
		res, err := c.Do(Request{Op: "groups", Path: path})
		if code := fail(stderr, res, err); code != 0 {
			return code
		}
		groups := res.Groups
		if mine {
			groups = mineOnly(groups, c.Author())
		}
		if op == "clear" {
			// Clearing reverses text, so it unwinds newest-first like a bulk
			// reject: a later rejected set that overlaps an earlier one has to
			// come out before the earlier one can. Only rejected sets are
			// attempted — clearing anything else is the refusal the single
			// form reports, and the groups listing is where the state is read.
			for i := len(groups) - 1; i >= 0; i-- {
				if groups[i].State != "rejected" {
					continue
				}
				decide(groups[i].ID)
			}
		} else {
			for _, g := range groups {
				decide(g.ID)
			}
		}
	}
	if len(out) == 0 {
		// Nothing to decide is an answer, not a failure, exactly as an empty
		// `groups` listing is.
		if asJSON {
			return emit(stdout, map[string]any{"ok": true, "decided": []uint64{}})
		}
		kind := "pending"
		if op == "clear" {
			kind = "rejected"
		}
		fmt.Fprintf(stdout, "%s: no %s change sets\n", firstOf(path, "active buffer"), kind)
		return 0
	}
	if asJSON {
		if code := emit(stdout, out); code != 0 {
			return code
		}
		if len(failed) > 0 {
			return 1
		}
		return 0
	}
	if len(failed) > 0 {
		fmt.Fprintf(stderr, "raj ctl %s: %d of %d change set(s) could not be %s:",
			op, len(failed), len(out), verb)
		for _, id := range failed {
			fmt.Fprintf(stderr, " %d", id)
		}
		fmt.Fprintln(stderr)
		return 1
	}
	return 0
}

// pendingRejections is the pending projection a bulk reject works from: the
// change sets with surviving text, which are the only ones a reversal can act
// on. `groups` also lists accepted sets and sets a later edit has fully
// overwritten, so attempting either reports a refusal for work already done —
// the state that looked like a wedge.
func pendingRejections(c *Client, path string, mine bool, stderr io.Writer) ([]DiffGroup, int) {
	res, err := c.Do(Request{Op: "diff", Path: path})
	if code := fail(stderr, res, err); code != 0 {
		return nil, code
	}
	var diffs []DiffGroup
	if err := json.Unmarshal([]byte(res.DiffJSON), &diffs); err != nil {
		fmt.Fprintln(stderr, "raj ctl reject:", err)
		return nil, 1
	}
	kept := diffs[:0]
	for _, d := range diffs {
		if len(d.Hunks) == 0 {
			continue // no surviving text: auto-rejected, nothing to back out
		}
		if mine && d.Author != c.Author() {
			continue
		}
		kept = append(kept, d)
	}
	return kept, 0
}

// newestRejection picks the latest change set not yet attempted, by journal
// position. A later edit can overlap an earlier set and block its inverse, so
// the newest has to come out first; the ID tie-break keeps the choice total so
// the loop cannot stall.
func newestRejection(pending []DiffGroup, attempted map[uint64]bool) (uint64, bool) {
	var best DiffGroup
	found := false
	for _, d := range pending {
		if attempted[d.ID] {
			continue
		}
		if !found || d.First > best.First ||
			(d.First == best.First && (d.Last > best.Last ||
				(d.Last == best.Last && d.ID > best.ID))) {
			best, found = d, true
		}
	}
	return best.ID, found
}

// mineOnly keeps the change sets this connection wrote. `groups` is the
// attribution record — a gone participant's record is not discarded — so the
// filter is a view applied here, not a change to what the server reports.
func mineOnly(groups []Group, author uint8) []Group {
	var kept []Group
	for _, g := range groups {
		if g.Author == author {
			kept = append(kept, g)
		}
	}
	return kept
}

// mineProposals keeps the proposals this connection wrote, the `proposals
// -mine` view over the unified listing, mirroring mineOnly for `groups`.
func mineProposals(props []Proposal, author uint8) []Proposal {
	var kept []Proposal
	for _, p := range props {
		if p.Author == author {
			kept = append(kept, p)
		}
	}
	return kept
}

// doSearch prints hits in the grep format every tool already parses:
// path:line:col:text. The overlay means a hit can be in a buffer the user has
// not saved, which is the point — an agent that grepped the filesystem would
// see stale text and edit against it.
func doSearch(c *Client, q SearchQuery, jsonl, asJSON bool, stdout, stderr io.Writer) int {
	// Print as they arrive rather than at the end. A walk over a large tree
	// takes seconds, and a caller — a person at a terminal or an agent reading
	// a pipe — should not wait for the last file to see the first hit.
	// Ctrl+C closes the connection, which cancels the walk in the editor.
	n := 0
	stream := func(batch []SearchMatch) {
		if jsonl {
			// NDJSON: one object per line, as each hit arrives.
			enc := json.NewEncoder(stdout)
			for _, m := range batch {
				obj := map[string]any{
					"path": m.Path, "line": m.Line, "col": m.Col, "len": m.Len,
					"byte_start": m.ByteStart, "byte_end": m.ByteEnd, "line_start": m.LineStart, "line_end": m.LineEnd,
					"version": m.Version, "text": m.Text,
				}
				// Context is absent, not empty, when the flag was not given:
				// the key must not appear for a plain -jsonl, whose bytes are
				// unchanged from before the option existed.
				if m.Context != "" {
					obj["context"] = m.Context
				}
				enc.Encode(obj)
				n++
			}
			return
		}
		if asJSON {
			return // JSON is emitted whole, so it stays parseable
		}
		for _, m := range batch {
			if m.Context != "" {
				// The context block already contains the hit line, so the
				// header names where the hit is and the block shows it in
				// place, with the version the hit was found at.
				fmt.Fprintf(stdout, "%s:%d:%d version %d\n%s\n", m.Path, m.Line, m.Col, m.Version, m.Context)
			} else {
				fmt.Fprintf(stdout, "%s:%d:%d:%s\n", m.Path, m.Line, m.Col, m.Text)
			}
			n++
		}
	}
	res, err := c.DoStream(Request{Op: "search", Query: &q}, stream)
	if code := fail(stderr, res, err); code != 0 {
		return code
	}
	// An -include that admits no file is almost always a mistyped glob, and
	// its output is indistinguishable from "no matches" without this — but
	// the two call for opposite fixes, so say which one happened.
	if q.Include != "" && res.Considered == 0 {
		fmt.Fprintln(stderr, "search: warning: -include pattern(s) matched no files")
	}
	// A literal search that finds nothing but whose pattern carries regex
	// syntax is usually a regex typed without -regex: the engine treats the
	// metacharacters as text and matches nothing. Say so once, and only when
	// files really were searched, so it cannot fire on a genuinely empty walk.
	if !q.Regex && res.Considered > 0 && len(res.Matches) == 0 && hasRegexMeta(q.Text) {
		fmt.Fprintf(stderr, "search: no matches; %q contains regex metacharacters — retry with -regex\n", q.Text)
	}
	if !jsonl && asJSON {
		return emit(stdout, map[string]any{
			"matches": res.Matches, "files": res.Files, "capped": res.Capped,
			"truncated": res.Truncated})
	}
	if res.Capped {
		fmt.Fprintln(stderr, "note: results were capped; narrow the query with -include")
	}
	// A per-file cap is not the global one: a file with more matches than the
	// limit reports the limit and leaves capped false, so without this it is
	// indistinguishable from a file holding exactly the limit. Name the files,
	// rather than a count the caller has to go and resolve.
	if n := len(res.Truncated); n > 0 {
		fmt.Fprintf(stderr, "note: %d file(s) hold more matches than the per-file limit shows:\n", n)
		for _, f := range res.Truncated {
			fmt.Fprintf(stderr, "  %s (%d of %d)\n", f.Path, f.Shown, f.Total)
		}
	}
	if len(res.Matches) == 0 {
		return 1 // no hits is a non-zero exit, as grep has always had it
	}
	return 0
}

// hasRegexMeta reports whether a literal search pattern carries characters a
// regex engine would read as syntax, which is the tell that a caller meant
// -regex and forgot it.
func hasRegexMeta(s string) bool {
	return strings.ContainsAny(s, `\.+*?()|[]{}^$`)
}

// apply is the direct write: offsets, and the version they were measured in.
func apply(c *Client, path string, base uint64, start, end int, textArg, textFile, hunksFile string,
	pos []string, verbatim bool, fs *flag.FlagSet, stdout, stderr io.Writer, asJSON bool) int {
	if !flagSet(fs, "base") {
		fmt.Fprintln(stderr, "raj ctl apply: -base is required.\n"+
			"Offsets only mean something in the coordinates of a version you have read;\n"+
			"get one with `raj ctl read -json` or `raj ctl version`.")
		return 2
	}
	if len(pos) > 1 {
		fmt.Fprintf(stderr, "raj ctl apply: expected at most one replacement operand, got %d\n", len(pos))
		return 2
	}
	if hunksFile != "" {
		if len(pos) == 1 {
			fmt.Fprintln(stderr, "raj ctl apply: -hunks carries its own text; "+
				"a replacement operand cannot go with it")
			return 2
		}
		return applyHunks(c, path, base, hunksFile, fs, stdout, stderr, asJSON)
	}
	if start < 0 || end < start {
		fmt.Fprintf(stderr, "raj ctl apply: -start %d -end %d is not a span\n", start, end)
		return 2
	}
	text := textArg
	if len(pos) == 1 {
		if textArg != "" || textFile != "" {
			fmt.Fprintln(stderr, "raj ctl apply: an operand and -text/-text-file are alternatives")
			return 2
		}
		text = pos[0]
	}
	if textFile != "" {
		if textArg != "" {
			fmt.Fprintln(stderr, "raj ctl apply: -text and -text-file are alternatives")
			return 2
		}
		data, err := readTextFile(textFile, verbatim)
		if err != nil {
			fmt.Fprintln(stderr, "raj ctl apply:", err)
			return 1
		}
		text = data
	}
	if start == end && text == "" {
		// A zero-width span with no text is a no-op. Refusing it here is
		// clearer than sending it and reporting a change set that changed
		// nothing; a deletion is a non-empty span with empty text and still
		// goes through.
		fmt.Fprintln(stderr, "raj ctl apply: nothing to apply: -start and -end are equal and\n"+
			"no text was given, so the hunk would change nothing. A deletion needs a\n"+
			"non-empty span (-end greater than -start); an insertion needs text.")
		return 2
	}
	res, err := c.Do(Request{Op: "apply", Path: path, Base: &base,
		Hunks: []Hunk{{Start: start, End: end, Text: text}}})
	return reportApply(res, err, 1, []hunkEcho{{Start: start, End: end, Text: text}}, c.Author(),
		stdout, stderr, asJSON)
}

// applyHunks sends every hunk in a JSON Lines file as one change set at one
// base. This is the k-site edit as a single call: the caller reads the buffer
// and the version once, hands over k hunks, and the editor rebases them
// together. The file's shape is the program's opcodes without the framing —
// {"start":S,"end":E,"text":"..."}, one object per line — so a caller
// producing it from a diff never assembles offsets into a program by hand.
func applyHunks(c *Client, path string, base uint64, file string, fs *flag.FlagSet,
	stdout, stderr io.Writer, asJSON bool) int {
	for _, other := range []string{"start", "end", "text", "text-file"} {
		if flagSet(fs, other) {
			fmt.Fprintf(stderr, "raj ctl apply: -hunks carries its own spans and text; "+
				"it cannot be mixed with -%s\n", other)
			return 2
		}
	}
	var data []byte
	var err error
	if file == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(file)
	}
	if err != nil {
		fmt.Fprintln(stderr, "raj ctl apply:", err)
		return 1
	}
	hunks, echo, code := parseHunks(string(data), stderr)
	if code != 0 {
		return code
	}
	res, derr := c.Do(Request{Op: "apply", Path: path, Base: &base, Hunks: hunks})
	return reportApply(res, derr, len(hunks), echo, c.Author(), stdout, stderr, asJSON)
}

// parseHunks decodes JSON Lines: one {start,end,text} object per line. A blank
// line is skipped, so the trailing newline every text file ends with is not an
// error; anything else is refused with its line number, because a hunk that
// cannot be placed is worse than a hunk never sent. A no-op hunk -- an empty
// text at a zero-width span -- is dropped, so a batch mixing real and no-op
// hunks sends only the real ones and a batch of only no-ops is refused rather
// than recorded as a change set that changed nothing.
func parseHunks(text string, stderr io.Writer) ([]Hunk, []hunkEcho, int) {
	var hunks []Hunk
	var echo []hunkEcho
	noops := 0
	for line := 1; len(text) > 0; line++ {
		raw := text
		if nl := strings.IndexByte(text, '\n'); nl >= 0 {
			raw, text = text[:nl], text[nl+1:]
		} else {
			text = ""
		}
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		var h struct {
			Start int    `json:"start"`
			End   int    `json:"end"`
			Text  string `json:"text"`
		}
		if err := json.Unmarshal([]byte(raw), &h); err != nil {
			fmt.Fprintf(stderr, "raj ctl apply: -hunks line %d: %v\n", line, err)
			return nil, nil, 1
		}
		if h.Start < 0 || h.End < h.Start {
			fmt.Fprintf(stderr, "raj ctl apply: -hunks line %d: -start %d -end %d is not a span\n",
				line, h.Start, h.End)
			return nil, nil, 1
		}
		if h.Start == h.End && h.Text == "" {
			noops++
			continue
		}
		hunks = append(hunks, Hunk{Start: h.Start, End: h.End, Text: h.Text})
		echo = append(echo, hunkEcho{Start: h.Start, End: h.End, Text: h.Text})
	}
	if len(hunks) == 0 {
		if noops > 0 {
			fmt.Fprintln(stderr, "raj ctl apply: every hunk is a no-op: a zero-width\n"+
				"span with empty text changes nothing")
		} else {
			fmt.Fprintln(stderr, "raj ctl apply: -hunks file holds no hunks")
		}
		return nil, nil, 1
	}
	return hunks, echo, 0
}

// dumpCmd captures a span (or the whole file) as an editable snapshot. The text
// goes to stdout, exactly as read prints it; the id, version and hash go to
// stderr so a driver that only wants the bytes is not handed a stray line.
func dumpCmd(c *Client, path string, start, end int, stdout, stderr io.Writer, asJSON bool) int {
	var sp, ep *int
	if start >= 0 {
		sp = &start
	}
	if end >= 0 {
		ep = &end
	}
	res, err := c.Do(Request{Op: "dump", Path: path, Start: sp, End: ep})
	if code := fail(stderr, res, err); code != 0 {
		return code
	}
	if asJSON {
		spans := make([]map[string]any, 0, len(res.Spans))
		for _, sp := range res.Spans {
			spans = append(spans, map[string]any{
				"text": sp.Text, "author": sp.Author,
				"mine": sp.Mine(c.Author()), "by_user": sp.ByUser(),
			})
		}
		return emit(stdout, map[string]any{
			"id": res.DumpID, "version": res.Version, "hash": res.Hash,
			"text": res.Text(), "spans": spans,
		})
	}
	io.WriteString(stdout, res.Text())
	fmt.Fprintf(stderr, "note: snapshot id %d, version %d, hash %s\n", res.DumpID, res.Version, res.Hash)
	return 0
}

// patchCmd replaces a snapshot text with the whole edited text, and the editor
// diffs and rebases — the caller never states an offset or matches old text, so
// there is no anchor to get wrong.
func patchCmd(c *Client, path string, dumpID uint64, textArg, textFile string, stdout, stderr io.Writer, asJSON bool) int {
	if dumpID == 0 {
		fmt.Fprintln(stderr, "raj ctl patch: -dump is required; get an id from dump")
		return 2
	}
	text := textArg
	if textFile != "" {
		if textArg != "" {
			fmt.Fprintln(stderr, "raj ctl patch: -text and -text-file are alternatives")
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
			fmt.Fprintln(stderr, "raj ctl patch:", err)
			return 1
		}
		text = string(data)
	}
	res, err := c.Do(Request{Op: "patch", Path: path, DumpID: dumpID, PatchText: text})
	if err != nil {
		fmt.Fprintln(stderr, "raj ctl patch:", err)
		return 1
	}
	if len(res.Conflicts) > 0 {
		// The same lease-versus-stale split apply and edit use: a conflict that
		// names an owner asks for a decision about another writer's text, while
		// one with no group is the ordinary moved snapshot. The old wording
		// told every caller to dump again, which is wrong for a lease.
		return reportConflicts("patch", res.Conflicts, res.Warnings, len(res.Conflicts), c.Author(), stdout, stderr, asJSON)
	}
	if res.Err != "" {
		fmt.Fprintln(stderr, "raj ctl patch:", res.Err)
		return 1
	}
	if asJSON {
		out := map[string]any{"ok": true, "version": res.Version}
		if len(res.Warnings) > 0 {
			out["warnings"] = res.Warnings
		}
		return emit(stdout, out)
	}
	fmt.Fprintf(stdout, "patched snapshot %d at version %d; the buffer has unsaved changes\n", dumpID, res.Version)
	noteOverlaps(stdout, res.Warnings)
	return 0
}

// reviewCmd enters Review mode and lists the pending change sets, or with
// -json returns the list without touching the mode. The list is the change
// sets `groups` lists filtered to those still awaiting a decision; entering
// the mode is the same enter path the cmd+r chord takes.
func reviewCmd(c *Client, path string, listOnly bool, stdout, stderr io.Writer) int {
	res, err := c.Do(Request{Op: "review", Path: path, ReviewList: listOnly})
	if code := fail(stderr, res, err); code != 0 {
		return code
	}
	if listOnly {
		return emit(stdout, res.Groups)
	}
	if len(res.Groups) == 0 {
		fmt.Fprintf(stdout, "%s: no proposed changes\n", targetName(c, path, "active buffer"))
		return 0
	}
	fmt.Fprintf(stdout, "%s: review mode, %d proposed change set(s)\n",
		targetName(c, path, "active buffer"), len(res.Groups))
	for _, g := range res.Groups {
		fmt.Fprintf(stdout, "%d\tauthor %d\t%s\t%d ops\t%+d bytes\n",
			g.ID, g.Author, g.State, g.Ops, g.Bytes)
	}
	return 0
}

// diffCmd prints the pending change sets as old→new text: the review surface
// for the proposals `groups` only lists. An empty pending set is a clean
// buffer, reported on stdout with a zero exit — review found nothing to do,
// which is an answer, not a failure.
func diffCmd(c *Client, path string, stdout, stderr io.Writer, asJSON bool) int {
	res, err := c.Do(Request{Op: "diff", Path: path})
	if code := fail(stderr, res, err); code != 0 {
		return code
	}
	var diffs []DiffGroup
	if err := json.Unmarshal([]byte(res.DiffJSON), &diffs); err != nil {
		fmt.Fprintln(stderr, "raj ctl diff:", err)
		return 1
	}
	if asJSON {
		return emit(stdout, diffs)
	}
	if len(diffs) == 0 {
		fmt.Fprintf(stdout, "%s: no pending changes\n", targetName(c, path, "active buffer"))
		return 0
	}
	for _, g := range diffs {
		fmt.Fprintf(stdout, "group %d\tauthor %d\t%s\t%d ops\t%+d bytes\n",
			g.ID, g.Author, g.State, g.Ops, g.Bytes)
		if g.Overlaps != nil {
			for _, o := range g.Overlaps.Sets {
				fmt.Fprintf(stdout, "  overlaps change set %d (author %d) at bytes %d..%d\n",
					o.Group, o.Author, o.Start, o.End)
			}
		}
		if g.Invalid {
			// The set is still listed because it is still proposed; the flag
			// says no hunk survives. Name the collider so the next step is
			// clear rather than a bare "moved" note.
			if g.InvalidBy != nil {
				fmt.Fprintf(stdout, "  invalid: superseded by change set %d (author %d) at bytes %d..%d\n",
					g.InvalidBy.Group, g.InvalidBy.Author, g.InvalidBy.Start, g.InvalidBy.End)
			} else {
				fmt.Fprintln(stdout, "  invalid: superseded; no colliding set can be named")
			}
		}
		for _, h := range g.Hunks {
			fmt.Fprintf(stdout, "@@ %s @@\n", diffHeader(h))
			writeDiffLines(stdout, "-", h.Old)
			writeDiffLines(stdout, "+", h.New)
		}
		for _, h := range g.MovedHunks {
			fmt.Fprintln(stdout, "@@ moved: no current span, as written @@")
			writeDiffLines(stdout, "-", h.Old)
			writeDiffLines(stdout, "+", h.New)
		}
		if g.Moved > 0 {
			if len(g.MovedHunks) > 0 {
				fmt.Fprintf(stdout, "  note: %d op(s) moved past what a rebase can carry; "+
					"shown above as written, not where they are now\n", g.Moved)
			} else {
				fmt.Fprintf(stdout, "  note: %d op(s) moved past what a rebase can carry\n", g.Moved)
			}
		}
	}
	return 0
}

// diffHeader names a hunk for the @@ line. A hunk with line coordinates prints
// the 1-based range and the byte span; one from a server that did not compute
// lines falls back to bytes alone rather than claiming line 0.
func diffHeader(h DiffHunk) string {
	if h.Line <= 0 || h.EndLine <= 0 {
		return fmt.Sprintf("bytes %d..%d", h.Start, h.End)
	}
	return fmt.Sprintf("L%d..L%d (bytes %d..%d)", h.Line, h.EndLine, h.Start, h.End)
}

// writeDiffLines prints one diff line per line of text. An empty side prints
// nothing at all: a pure insertion has no - lines and a pure deletion no +
// lines, so neither gets a bare marker.
func writeDiffLines(w io.Writer, prefix, text string) {
	if text == "" {
		return
	}
	for _, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		fmt.Fprintf(w, "%s%s\n", prefix, line)
	}
}

// doLSP asks the language server for hover text, a definition, a declaration,
// a type definition, an implementation, references, completions, the signature
// of the call at a position, the cached diagnostics for a path, the inlay hints
// over a whole file or a line range, the project-wide symbols matching a query,
// or the formatting edits for a file or a line range. The answer is JSON
// already; -json prints it whole and the plain path pretty-prints it, so a
// script and a person read the same result. inlay-hints is the one mode whose
// plain form is line-oriented: a hint is one line of text, and a JSON object
// per line reads worse.
//
// format and range-format are the formatting pair: `lsp format PATH` asks for
// the whole buffer to be formatted and `lsp range-format PATH -lines A,B` for
// the named lines. They report the server's TextEdit list as `edits`, in 1-based
// editor coordinates, rather than applying it — the control surface asks and
// does not edit — and the human chords are what apply a format, through the
// editor's one-undo application path.
//
// symbols is the one mode that takes a query: `lsp symbols PATH LINE:COL [query]`
// asks the server's project index for declarations matching the query, and the
// answer's `symbols` field carries them. document-symbols asks about the file
// itself and returns its outline as `documentSymbols`, nested.
func doLSP(c *Client, mode, path, pos, lines, query string, stdout, stderr io.Writer, asJSON bool) int {
	switch mode {
	case "hover", "definition", "declaration", "type-definition", "implementation",
		"references", "completion", "signature", "diagnostics", "inlay-hints", "symbols",
		"format", "range-format", "document-symbols":
	default:
		fmt.Fprintln(stderr, "raj ctl lsp: mode must be hover, definition, declaration, type-definition, implementation, references, completion, signature, diagnostics, inlay-hints, symbols, format, range-format or document-symbols")
		return 2
	}
	line, col := 0, 0
	var lineStart, lineEnd *int
	switch mode {
	case "diagnostics", "format", "document-symbols":
		// No position: diagnostics, formatting and a file outline are asked
		// about a file, not a place.
	case "range-format":
		// The range form: like inlay-hints, a bare A reads from A to the end
		// and A,B is the 1-based inclusive range. Unlike inlay-hints it has no
		// useful whole-file default, because a collapsed range would ask the
		// server to format nothing, so the lines are required.
		if lines == "" {
			fmt.Fprintln(stderr, "raj ctl lsp range-format: needs -lines A or A,B")
			return 2
		}
		a, b, hasEnd, ok := ctlLines(lines)
		if !ok {
			fmt.Fprintln(stderr, "raj ctl lsp: -lines wants A or A,B, 1-based inclusive")
			return 2
		}
		lineStart = &a
		if hasEnd {
			lineEnd = &b
		}
	case "inlay-hints":
		// A range request. A bare A reads from A to the end; A,B is the
		// 1-based inclusive range, mirroring read -lines.
		if lines != "" {
			a, b, hasEnd, ok := ctlLines(lines)
			if !ok {
				fmt.Fprintln(stderr, "raj ctl lsp: -lines wants A or A,B, 1-based inclusive")
				return 2
			}
			lineStart = &a
			if hasEnd {
				lineEnd = &b
			}
		}
	default:
		if pos == "" {
			fmt.Fprintln(stderr, "raj ctl lsp: needs a position, LINE:COL")
			return 2
		}
		l, cl, ok := ctlPosition(pos)
		if !ok || l < 1 || cl < 1 {
			fmt.Fprintln(stderr, "raj ctl lsp: needs a position, LINE:COL")
			return 2
		}
		line, col = l, cl
	}
	res, err := c.Do(Request{Op: "lsp", Path: path, Line: line, Col: col, LSPMode: mode,
		Query: lspQuery(query), LineStart: lineStart, LineEnd: lineEnd})
	if code := fail(stderr, res, err); code != 0 {
		return code
	}
	// The answer is structured even though it crosses the wire as one JSON
	// string, so a diagnostics status can be read rather than guessed at. An
	// empty list is only an answer when the status says a server produced it:
	// otherwise the call is refused, because a per-hunk check that reads
	// "server missing" as "no problems" is worse than no check at all.
	var out LSPResult
	if uerr := json.Unmarshal([]byte(res.LSPJSON), &out); uerr != nil {
		// An answer this build cannot parse is still the server's answer.
		fmt.Fprintln(stdout, res.LSPJSON)
		return 0
	}
	if mode == "diagnostics" && out.Status != LSPStatusOK {
		if asJSON {
			fmt.Fprintln(stdout, res.LSPJSON)
		} else {
			fmt.Fprintln(stderr, "raj ctl lsp diagnostics: "+
				firstOf(out.Detail, out.Status,
					"the editor did not report a diagnostics status; rebuild it to match this ctl"))
		}
		return 1
	}
	if asJSON {
		fmt.Fprintln(stdout, res.LSPJSON)
		return 0
	}
	if mode == "inlay-hints" {
		// One hint per line — its editor position and the label text — because
		// a hint is one line of text and a JSON object per line reads worse.
		for _, h := range out.Hints {
			fmt.Fprintf(stdout, "%d:%d %s\n", h.Line, h.Col, h.Text)
		}
		return 0
	}
	return emit(stdout, out)
}

// lspQuery carries a workspace/symbol query on the request's generic Query
// field. An empty query is nil rather than an empty SearchQuery, so the field
// is absent on the wire exactly when there is nothing to say; the server's
// symbols branch reads "" either way.
func lspQuery(q string) *SearchQuery {
	if q == "" {
		return nil
	}
	return &SearchQuery{Text: q}
}

// reportApply is shared by apply and edit so the two cannot describe the same
// outcome differently.
// hunkEcho is what an apply or edit reports it wrote, so the -json reply is
// itself the check that the hunk landed where the caller meant — the
// "applied 1 hunk(s)" that says nothing about what changed.
type hunkEcho struct {
	Start, End int
	Old        string // the text that was matched, when known (edit)
	Text       string // the replacement that was written
}

func reportApply(res Response, err error, hunks int, echo []hunkEcho, self uint8, stdout, stderr io.Writer, asJSON bool) int {
	if err != nil {
		fmt.Fprintln(stderr, "raj ctl apply:", err)
		return 1
	}
	if len(res.Conflicts) > 0 {
		return reportConflicts("apply", res.Conflicts, res.Warnings, hunks, self, stdout, stderr, asJSON)
	}
	if res.Err != "" {
		fmt.Fprintln(stderr, "raj ctl apply:", res.Err)
		return 1
	}
	if asJSON {
		out := map[string]any{"ok": true, "applied": hunks, "version": res.Version}
		if len(res.Warnings) > 0 {
			out["warnings"] = res.Warnings
		}
		if len(echo) > 0 {
			spans := make([]map[string]any, 0, len(echo))
			for _, h := range echo {
				m := map[string]any{"start": h.Start, "end": h.End}
				if h.Old != "" {
					m["matched"] = h.Old
				}
				if h.Text != "" {
					m["text"] = h.Text
				}
				spans = append(spans, m)
			}
			out["spans"] = spans
		}
		return emit(stdout, out)
	}
	fmt.Fprintf(stdout, "applied %d hunk(s) at version %d; the buffer has unsaved changes\n",
		hunks, res.Version)
	noteOverlaps(stdout, res.Warnings)
	return 0
}

// noteOverlaps prints one note per superseded Proposed set. A warning is not a
// refusal: the hunk landed, but over another writer's Proposed span, so naming
// the set it moved past tells the caller rather than leaving them to find it in
// groups/diff later. Shared by apply, edit and patch so the wording cannot
// drift between the verbs.
func noteOverlaps(w io.Writer, warnings []GroupOverlap) {
	for _, wn := range warnings {
		fmt.Fprintf(w, "note: overlapped change set %d (author %d, bytes %d..%d)\n",
			wn.Group, wn.Author, wn.Start, wn.End)
	}
}

// conflictView is one refused hunk in the -json reply: the wire Conflict plus
// the reason worded for a driver, so a script and a person read the same thing.
// Start and End are pointers so a lease span that begins at byte zero is
// distinguishable from no span at all.
type conflictView struct {
	Index   int    `json:"index"`
	At      uint64 `json:"at"`
	Group   uint64 `json:"group,omitempty"`
	Author  uint8  `json:"author,omitempty"`
	Start   *int   `json:"start,omitempty"`
	End     *int   `json:"end,omitempty"`
	Message string `json:"message"`
	Hunk    Hunk   `json:"hunk"`
}

// leaseView fills in the owner fields a lease refusal carries and words the
// decision it asks for. A conflict with no group is the ordinary stale offset,
// which is the caller's cue to re-read rather than to decide about someone
// else's text.
func leaseView(c Conflict, self uint8) conflictView {
	v := conflictView{Index: c.Index, At: c.At, Group: c.Group, Hunk: c.Hunk}
	if c.Group == 0 {
		return v
	}
	start, end := c.Start, c.End
	v.Author = c.Author
	v.Start, v.End = &start, &end
	if c.Author == self {
		// A conflict the caller owns is necessarily own+other: a hunk whose
		// only caught Proposed run is the caller's own joins that set instead
		// of refusing (Session.ApplyDiff -> commitInto), so the refusal means a
		// peer's run is in the way as well. The caller cannot accept or
		// reject its own draft, so name the draft and the remedy that is
		// actually available: narrow the hunk off the peer's text.
		v.Message = fmt.Sprintf("change set %d is your own draft (bytes %d..%d); "+
			"this hunk also crosses another writer's text — narrow it to avoid their span",
			c.Group, c.Start, c.End)
		return v
	}
	v.Message = fmt.Sprintf("change set %d owns this text (author %d, bytes %d..%d); "+
		"accept or reject it first", c.Group, c.Author, c.Start, c.End)
	return v
}

// reportConflicts describes hunks that could not be placed. A conflict that
// carries a group is a lease refusal — a pending or rejected change set owns
// the text and has to be decided before the hunk can land — and names the set,
// its author and the span it holds; when that set is the caller's own draft
// the wording says so and names the remedy the caller actually has (narrow the
// hunk off the peer text), because a writer cannot decide its own proposal. One
// with no group is the ordinary stale offset and keeps the message that tells
// the caller to re-read and resubmit. The two call for opposite actions, which
// is why they no longer share a sentence.
//
// Warnings ride along: a batch can refuse one hunk and still land another over
// a Proposed set, so the landed overlap is reported on the failure reply too
// rather than lost to the early return that reports the conflicts.
func reportConflicts(verb string, conflicts []Conflict, warnings []GroupOverlap, hunks int, self uint8, stdout, stderr io.Writer, asJSON bool) int {
	// verb names the caller — apply or patch — so a refusal points at the
	// command the driver actually ran rather than at whichever verb first grew
	// this reporting.
	const stale = "could not be placed on the current version — the buffer moved " +
		"further than a rebase could carry them; read it again and redo the hunk " +
		"against the version you get back"
	views := make([]conflictView, 0, len(conflicts))
	staleCount := 0
	for _, c := range conflicts {
		v := leaseView(c, self)
		if c.Group == 0 {
			v.Message = stale
			staleCount++
		}
		views = append(views, v)
	}
	if asJSON {
		out := map[string]any{"ok": false, "conflicts": views}
		// A warned hunk landed even though another was refused, so the warning
		// rides the ok:false reply: dropping it would read as a clean refusal
		// and lose the fact that the batch changed another writer's set.
		if len(warnings) > 0 {
			out["warnings"] = warnings
		}
		emit(stdout, out)
		return 1
	}
	for _, v := range views {
		if v.Group != 0 {
			fmt.Fprintf(stderr, "raj ctl %s: %s\n", verb, v.Message)
		}
	}
	if staleCount > 0 {
		fmt.Fprintf(stderr, "raj ctl %s: %d of %d hunks could not be placed on the current\n"+
			"version — the buffer moved further than a rebase could carry them. Read it\n"+
			"again and redo those hunks against the version you get back.\n",
			verb, staleCount, hunks)
	}
	// The warned hunks did land: say so on the same stream as the refusal, so
	// a caller reading only the failure still learns what the batch changed.
	noteOverlaps(stderr, warnings)
	return 1
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
	// An empty set is an empty list, not JSON null: every other listing
	// guards this, so a script can index an empty result without a special case.
	if res.Buffers == nil {
		res.Buffers = []Buffer{}
	}
	if asJSON {
		// Health, not just size: a per-buffer brace balance, computed from the
		// live text (unsaved edits included). One text read per named buffer —
		// a diagnostic cost the plain listing does not pay, and the reason this
		// stays -json only. A buffer that will not read keeps a nil tally
		// rather than a false zero.
		for i := range res.Buffers {
			b := &res.Buffers[i]
			if b.Path == "" {
				continue
			}
			txt, terr := c.Do(Request{Op: "text", Path: b.Path})
			if terr != nil || !txt.OK {
				continue
			}
			tally := braceTally(b.Path, txt.Text())
			b.Tally = &tally
		}
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
		mark := ""
		if b.Active {
			mark = "\tactive"
		}
		if b.Headless {
			mark += "\theadless"
		}
		fmt.Fprintf(stdout, "%s\t%d bytes\t%d lines\t%s%s\n", name, b.Bytes, b.Lines, state, mark)

	}
	return 0
}

// braceTally is the buffer checking itself, no toolchain needed: a missing }
// and a stray } both survive a text-level read, and gofmt masks the real break
// behind cascading "expected declaration" echoes at later clean lines. A net
// balance per bracket kind flags them immediately. It is string- and
// comment-aware through the same ClassAt seam bracket matching uses: a brace
// inside a string or comment is not structural and does not count.
//
// The lexer is asynchronous and version-keyed, so a one-shot tally Ensures and
// waits, bounded. When it has nothing to say — a language it does not know, a
// file over MaxSize, or the wait running out — every bracket counts, degrading
// to plain depth counting the way the matcher does rather than to no answer.
func braceTally(path, text string) BraceTally {
	var t BraceTally
	var hl *syntax.Highlighter
	if len(text) <= syntax.MaxSize {
		hl = syntax.New(path, true)
		if hl.Enabled() {
			hl.Ensure(text, 0)
			for i := 0; i < 500 && !hl.Ready(); i++ {
				time.Sleep(2 * time.Millisecond)
			}
		}
	}
	// Walk line by line so the spans, which are line-relative, line up with the
	// byte offset. A nil line of spans makes ClassAt answer ClassCode, which is
	// the plain-counting degrade rather than a special case here.
	lineNo := 0
	for start := 0; start <= len(text); lineNo++ {
		eol := strings.IndexByte(text[start:], '\n')
		var line string
		if eol < 0 {
			line = text[start:]
		} else {
			line = text[start : start+eol]
		}
		var spans []syntax.Span
		if hl != nil && hl.Ready() {
			spans = hl.Line(lineNo)
		}
		for col := 0; col < len(line); col++ {
			if syntax.ClassAt(spans, col) != syntax.ClassCode {
				continue
			}
			switch line[col] {
			case '{':
				t.Braces++
			case '}':
				t.Braces--
			case '(':
				t.Parens++
			case ')':
				t.Parens--
			case '[':
				t.Brackets++
			case ']':
				t.Brackets--
			}
		}
		if eol < 0 {
			break
		}
		start += eol + 1
	}
	return t
}

func read(c *Client, paths []string, start, end int, lines string, annotated bool, stdout, stderr io.Writer, asJSON bool) int {
	var sp, ep *int
	if start >= 0 {
		sp = &start
	}
	if end >= 0 {
		ep = &end
	}
	var lsp, lep *int
	if a, b, hasEnd, ok := ctlLines(lines); ok {
		lsp, lep = &a, nil
		if hasEnd {
			lep = &b
		}
	}
	if len(paths) > 1 {
		if annotated {
			// The state runs are relative to one buffer's text, and the plain
			// form prints them after that text; there is no per-file shape for
			// them yet, so a single target is still the annotated read.
			fmt.Fprintln(stderr, "raj ctl read: -annotated takes one path")
			return 2
		}
		return readMany(c, paths, sp, ep, lsp, lep, annotated, stdout, stderr, asJSON)
	}
	path := ""
	if len(paths) == 1 {
		path = paths[0]
	}
	res, err := c.Do(Request{Op: "text", Path: path, Start: sp, End: ep,
		LineStart: lsp, LineEnd: lep, Annotated: annotated})
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
		out := map[string]any{
			"text": res.Text(), "version": res.Version,
			"author": c.Author(), "spans": spans,
		}
		// An annotated read carries the state of every run in the returned
		// text, relative to that text. It rides as raw JSON so the run list
		// stays exactly what the editor computed.
		if res.StatesJSON != "" {
			out["states"] = json.RawMessage(res.StatesJSON)
		}
		return emit(stdout, out)
	}
	text := res.Text()
	io.WriteString(stdout, text)
	if !annotated || res.StatesJSON == "" {
		return 0
	}
	// Plain -annotated prints the text, then one line per run, so the change
	// set and state of every byte is visible at a shell. The offsets are
	// relative to the text just printed, exactly as the -json states are.
	var runs []StateRun
	if err := json.Unmarshal([]byte(res.StatesJSON), &runs); err != nil {
		fmt.Fprintln(stderr, "raj ctl read: annotated states:", err)
		return 1
	}
	if len(runs) > 0 && !strings.HasSuffix(text, "\n") {
		io.WriteString(stdout, "\n")
	}
	for _, r := range runs {
		fmt.Fprintf(stdout, "run off=%d len=%d group=%d state=%s\n", r.Off, r.Len, r.Group, r.State)
	}
	return 0
}

// readMany prints the answer to a read that named several targets. The wire
// reuses the buffers and spans shapes, so the per-file split is the running
// sum of each buffer's byte length; a file is printed as its text under an
// `==> path <==` header, the convention head and tail use, which keeps two
// files' bytes from running together. -json emits one object per file.
func readMany(c *Client, paths []string, sp, ep, lsp, lep *int, annotated bool,
	stdout, stderr io.Writer, asJSON bool) int {
	res, err := c.Do(Request{Op: "text", Paths: paths, Start: sp, End: ep,
		LineStart: lsp, LineEnd: lep, Annotated: annotated})
	if code := fail(stderr, res, err); code != 0 {
		return code
	}
	all := res.Text()
	files := make([]map[string]any, 0, len(res.Buffers))
	off := 0
	for _, b := range res.Buffers {
		end := off + b.Bytes
		if end > len(all) {
			end = len(all)
		}
		files = append(files, map[string]any{
			"path": b.Path, "text": all[off:end], "version": b.Version,
		})
		off = end
	}
	if len(files) == 0 {
		// A server that does not know the multi-target form answered with a
		// single target's text. Print it rather than nothing, and let the
		// version-skew warning name the mismatch.
		if asJSON {
			return emit(stdout, map[string]any{"text": all, "version": res.Version})
		}
		io.WriteString(stdout, all)
		return 0
	}
	if asJSON {
		return emit(stdout, map[string]any{"files": files})
	}
	for i, f := range files {
		if i > 0 {
			fmt.Fprintln(stdout)
		}
		fmt.Fprintf(stdout, "==> %s <==\n", f["path"])
		io.WriteString(stdout, f["text"].(string))
	}
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
		// The error names the opcode it choked on, which is what a caller
		// needs; the program itself is something they generated and can print.
		return code
	}
	if asJSON {
		return emit(stdout, res)
	}
	fmt.Fprintln(stdout, "ok")
	return 0
}

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

// clearCmd hard-purges a rejected change set. Unlike accept and reject, clear
// edits the document, so it can fail on state rather than on the address: a
// later live set can overlap a member the reversal has to remove, and the
// refusal then names that set, its author and the span as a block, so the
// caller knows what to clear first instead of retrying blind.
func clearCmd(c *Client, path string, group uint64, stdout, stderr io.Writer, asJSON bool) int {
	res, err := c.Do(Request{Op: "clear", Path: path, Group: group})
	if err != nil {
		fmt.Fprintln(stderr, "raj ctl clear:", err)
		return 1
	}
	if res.Err != "" {
		if asJSON {
			out := map[string]any{"ok": false, "error": res.Err}
			if len(res.Conflicts) > 0 {
				b := res.Conflicts[0]
				out["block"] = map[string]any{
					"group": b.Group, "author": b.Author,
					"start": b.Start, "end": b.End,
				}
			}
			emit(stdout, out)
			return 1
		}
		fmt.Fprintln(stderr, "raj ctl clear:", res.Err)
		return 1
	}
	if asJSON {
		return emit(stdout, map[string]any{"ok": true, "version": res.Version})
	}
	fmt.Fprintf(stdout, "cleared change set %d\n", group)
	return 0
}

// revertCmd discards a writer's own live pieces, the inverse of attribution.
// The writer is the connection's own author — -mine, or -author naming that
// same id; a different author is refused here and again at the socket, because
// dropping a peer's accepted pieces is the user's decision, exercised through
// reject and clear. Like clear, a revert really edits, so a later live set can
// wedge a member; that refusal names the blocking set, its author and span.
func revertCmd(c *Client, path string, wantAuthor uint8, mine bool, stdout, stderr io.Writer, asJSON bool) int {
	me := c.Author()
	if wantAuthor != 0 && mine && wantAuthor != me {
		fmt.Fprintln(stderr, "raj ctl revert: -mine and -author name different writers")
		return 2
	}
	target := wantAuthor
	if target == 0 {
		target = me
	}
	if me != 0 && target != me {
		fmt.Fprintf(stderr, "raj ctl revert: author %d is not this connection (you write as %d); "+
			"another writer's pieces are dropped with reject then clear\n", target, me)
		return 1
	}
	res, err := c.Do(Request{Op: "revert", Path: path})
	if err != nil {
		fmt.Fprintln(stderr, "raj ctl revert:", err)
		return 1
	}
	if res.Err != "" {
		if asJSON {
			out := map[string]any{"ok": false, "error": res.Err}
			if len(res.Conflicts) > 0 {
				b := res.Conflicts[0]
				out["block"] = map[string]any{
					"group": b.Group, "author": b.Author,
					"start": b.Start, "end": b.End,
				}
			}
			emit(stdout, out)
			return 1
		}
		fmt.Fprintln(stderr, "raj ctl revert:", res.Err)
		return 1
	}
	if asJSON {
		return emit(stdout, map[string]any{"ok": true, "version": res.Version})
	}
	fmt.Fprintf(stdout, "reverted author %d's pieces\n", me)
	return 0
}

// openCmd shows a file and says which of the two things open can do happened:
// created makes a new buffer, opened focuses one that was already loaded. A
// driver writing a file for the first time needs to tell them apart — a typo
// with -create and a focus of the buffer an earlier call made both look like
// success otherwise. The word is the plain-text answer; -json carries the
// boolean the editor reported.
func openCmd(c *Client, path string, create bool, stdout, stderr io.Writer, asJSON bool) int {
	res, err := c.Do(Request{Op: "open", Path: path, Create: create})
	if code := fail(stderr, res, err); code != 0 {
		return code
	}
	verb := "opened"
	if res.Created {
		verb = "created"
	}
	if asJSON {
		return emit(stdout, map[string]any{"ok": true, "version": res.Version,
			"created": res.Created})
	}
	fmt.Fprintln(stdout, verb+" "+path)
	return 0
}

// closeCmd closes a buffer and, with -discard, says whether a file is still on
// disk at its path. Discarding drops the buffer and leaves the file exactly as
// it was, so a driver that recreated the content under a new name would
// otherwise leave a silent duplicate behind; the boolean is the machine answer
// and the plain-text note is the person's.
func closeCmd(c *Client, path string, discard bool, stdout, stderr io.Writer, asJSON bool) int {
	// The name is resolved before the close: closing the buffer the user is
	// looking at moves the active tab, so asking afterwards would name its
	// successor. A pathful close, or one that leaves no file behind, never
	// needs the extra round trip.
	name := path
	if discard && path == "" {
		name = targetName(c, "", "the buffer")
	}
	res, err := c.Do(Request{Op: "close", Path: path, Discard: discard})
	if code := fail(stderr, res, err); code != 0 {
		return code
	}
	if asJSON {
		return emit(stdout, map[string]any{"ok": true, "version": res.Version,
			"remains": res.Remains})
	}
	if res.Remains {
		fmt.Fprintln(stdout, "closed "+name+"; the file is still on disk")
		return 0
	}
	fmt.Fprintln(stdout, "closed")
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
		fmt.Fprintf(stderr, "raj ctl edit: that text does not appear in %s.\n"+
			"Read it again and copy the text exactly, including indentation — this file\n"+
			"may indent with tabs where you assumed spaces.", editTarget(c, path))
		return 1
	case len(offsets) > 1 && !all:
		fmt.Fprintf(stderr, "raj ctl edit: that text appears %d times in %s. Include more surrounding\n"+
			"context to make it unique, or pass -all to replace every occurrence.\n", len(offsets), editTarget(c, path))
		return 1
	}

	hunks := make([]Hunk, 0, len(offsets))
	echo := make([]hunkEcho, 0, len(offsets))
	for _, off := range offsets {
		hunks = append(hunks, Hunk{Start: off, End: off + len(old), Text: newText})
		echo = append(echo, hunkEcho{Start: off, End: off + len(old), Old: old, Text: newText})
	}
	base := res.Version
	ap, err := c.Do(Request{Op: "apply", Path: path, Base: &base, Hunks: hunks})
	return reportApply(ap, err, len(hunks), echo, c.Author(), stdout, stderr, asJSON)
}

// editTarget names the buffer an edit ran against, for an error message. An
// omitted path means the focused buffer; asking the editor which buffer that
// is lets the message name the file rather than the convention.
func editTarget(c *Client, path string) string {
	return targetName(c, path, "the buffer the user is looking at")
}

// activePath names the buffer the user is looking at, for a message about a
// verb that was given no path. Only the editor knows which buffer an empty
// path resolves to, so this asks `buffers`: one round trip in the pathless
// case, and the message can say the file rather than "the buffer". It is a
// label for a message, never a target — the request still carries the empty
// path, so the editor resolves the same buffer it would have anyway — and an
// editor that cannot answer falls back to the convention.
func activePath(c *Client) string {
	res, err := c.Do(Request{Op: "buffers"})
	if err != nil || res.Err != "" {
		return ""
	}
	for _, b := range res.Buffers {
		if b.Active && b.Path != "" {
			return b.Path
		}
	}
	return ""
}

// targetName is the name to print for the buffer a verb acted on: the path the
// caller gave, or the buffer an omitted path resolved to. fallback covers the
// case where nothing is focused or the editor cannot be asked.
func targetName(c *Client, path, fallback string) string {
	if path != "" {
		return path
	}
	return firstOf(activePath(c), fallback)
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
		// nothing" from "the editor said no". The JSON form still writes the
		// empty array so `-json` always parses, but it exits 3 too: a loop
		// reads the code, and the code must not depend on the output shape.
		if asJSON {
			emit(stdout, []Message{})
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
