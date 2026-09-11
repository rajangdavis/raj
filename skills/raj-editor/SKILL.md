---
name: raj-editor
description: Read, search and edit the files open in the user's running raj editor, including their unsaved changes. Use this instead of the normal read, edit and grep tools whenever the user refers to what they are looking at, what is on their screen, their current buffer or tab, or asks for a change to a file they say they have open. Also use it when a normal read shows a file that does not match what the user is describing, because the difference is usually unsaved edits that exist only in the editor. This works when you are running in a container and the editor is not: see the section on that below.
---

# Working in the user's open buffers

`raj` is the user's terminal editor. Run with `--control` it exposes a control
channel — a Unix socket, or a TCP port when you are somewhere that cannot reach
a socket — and `raj ctl` talks to it.

This matters for one reason: **a buffer open in raj can differ from the file on
disk.** Reading the file with normal tools shows you the saved version. Writing
the file with normal tools puts bytes underneath the user's unsaved work, where
they will be silently overwritten the next time the user presses save — and
nothing tells them that happened.

So: when the user is talking about something they have open, go through
`raj ctl`. For files they do not have open, and for anything that is not reading
or writing text — running tests, git, installing things — use your normal tools.
This does not replace them.

## Start here

```
raj ctl buffers
```

Lists what is open, with sizes and whether each has unsaved changes. If it exits
non-zero, raj is not running with `--control`; fall back to your normal file
tools rather than telling the user to restart their editor.

Nothing else works on a file that is not in that list. `raj ctl open
<absolute-path>` adds one — which also puts it on the user's screen, so do it
when you mean to, not to paper over a wrong path.

Every command takes an optional path; omit it for the buffer the user is
currently looking at. Add `-json` to any command for machine-readable output.

`buffers -json` adds `pending` (change sets still awaiting a decision) and
`moved` (their members a later edit has moved past) per buffer, so one call
tells you which open files hold review work.

## If you are in a container and the editor is not

This is the normal arrangement, not an exotic one. raj runs in the terminal on
the user's own machine, because that is the only place a terminal delivers its
keybindings; you run in a sandbox, because that is where it is safe to let you
run commands. There is no shared filesystem and no shared socket.

Two variables are set for you, and if they are you need do nothing else:

```
RAJ_CONTROL_ADDR=tcp://host.docker.internal:7391
RAJ_CONTROL_TOKEN=<the token raj printed when it started>
```

If `raj ctl buffers` fails with `no running raj found`, the address is not set
and there is nothing you can guess — the port and the token are on the other
side of the boundary. Use your normal file tools in the meantime, and give the
user the setup below rather than a vague instruction to "enable the socket".

### The setup, to hand to the user

No tmux, and no raj inside the container. raj stays in the user's own terminal
because that is the only place its chords are delivered — a raj under tmux loses
them, and a raj inside the container never sees them at all.

On their machine, in the workspace:

```
export RAJ_CONTROL_TOKEN=$(openssl rand -hex 32)
raj --control-addr tcp://127.0.0.1:7391 .
```

Then the sandbox, with the same token:

```
docker run --add-host=host.docker.internal:host-gateway \
  -e RAJ_CONTROL_ADDR=tcp://host.docker.internal:7391 \
  -e RAJ_CONTROL_TOKEN="$RAJ_CONTROL_TOKEN" \
  -v "$PWD:/workspace" -w /workspace <image>
```

`--add-host` is what makes the host's loopback port reachable from inside; the
container's own `127.0.0.1` is its own. If that still cannot connect, the bind
has to widen to `tcp://0.0.0.0:7391`, and raj will warn that the port is no
longer loopback-only. The warning is worth repeating to the user rather than
talking them past: the frames carry their unsaved work in plaintext, which is a
fine trade for a container bridge on a laptop and a bad one on a shared network.

The token is printed to stderr once when raj starts and stored nowhere, so a
user who has scrolled past it has to restart. Setting `RAJ_CONTROL_TOKEN`
before starting raj, as above, is what makes it reproducible.

**Use the paths you can see.** `raj ctl` translates between your filesystem and
the editor's, so if the repository is `/workspace` to you and
`/Users/them/src/proj` to raj, you pass `/workspace/main.go` and that is what
comes back from `buffers` and `search`. Do not try to construct the editor's
paths yourself. `raj ctl whoami -json` prints `root_map` when a translation is
in force, which is worth checking once if paths are being refused.

If the mapping is wrong — an unusual mount layout — the user can set
`RAJ_ROOT_MAP=/workspace=/Users/them/src/proj` in your environment. Ask; do not
work around it by guessing at paths.

**`raj ctl exec` does not work here, by design.** It is refused over TCP,
because the command would run on the user's machine rather than in your sandbox
— outside the isolation that is the reason you are in one. Run tests, builds
and git with your own shell tool, in your own environment, as you normally
would. The one thing you lose is the unsaved-buffer warning, so check
`raj ctl buffers` yourself before you trust a test result: if a file you are
about to test has unsaved changes, the command is reading older text.

## Searching

```
raj ctl search -q 'func handleRequest' -include '*.go'
```

Prints `path:line:col:text`. Use this rather than grep or ripgrep whenever the
user has files open: it is the editor's own engine with the open buffers laid
over the disk, so a hit reflects unsaved edits and a line the user just deleted
will not come back. Grepping the filesystem finds text that is no longer there
and sends you off to edit against it.

Hits print as they are found, so a slow search over a large tree is still
usable, and ctrl+C stops the walk inside the editor rather than just detaching
from it. `-regex`, `-case` and `-word` are available. `-include` and `-exclude`
take comma-separated globs and must stay inside the workspace.

Exit is non-zero when there are no matches, as with grep.

A pattern with no `/` matches the basename as well as the relative path, like
`grep --include`: `-include '*_test.go'` finds test files at any depth and
`-include 'search.go'` finds that file wherever it sits, while a pattern
containing `/` stays path-scoped. A bare positional argument is refused — the
pattern goes to `-q` — and a search whose `-include` matched no files says so
rather than returning an authoritative empty result.

## Read before you edit, every time

```
raj ctl read /abs/path/to/file.go                  # the text
raj ctl read -start 120 -end 148 /abs/path/to/file.go  # a byte span
raj ctl read -json /abs/path/to/file.go            # text, version, and authorship
```

Do this even if you read the same file a moment ago: the user is typing in it.
The editor refuses a write from a caller that has not read the buffer, because
byte offsets only mean something in the coordinates of a version somebody has
actually seen.

`-start` and `-end` are byte offsets, half-open like `apply`: `-start 120
-end 148` returns the 28 bytes starting at offset 120. Use them to verify a
splice without reading the whole file.

If you only need coordinates and not the whole document — appending, say —
`raj ctl version` is enough and satisfies that check too.

## Edit by offsets

```
raj ctl read -json /abs/path/to/file.go
# {"text": "...", "version": 41, "author": 3, "spans": [...]}

raj ctl apply /abs/path/to/file.go -base 41 -start 120 -end 148 -text 'func g() {'
```

`-start` and `-end` are byte offsets into the text you just read, counted from
zero, replacing the half-open range `[start, end)`. `-base` is the version that
read returned.

This is the verb to prefer. The editor rebases your hunk onto whatever the
document is now, so if the user typed since your read, the hunk moves with the
text instead of landing where the text used to be. There is no string to match,
so indentation and whitespace cannot make an edit fail.

For replacement text containing newlines, write it to a file and use
`-text-file /tmp/new.txt`, or `-text-file -` for stdin.

### Apply several hunks at once

```
raj ctl apply /abs/path/to/file.go -base 41 -hunks /tmp/hunks.jsonl
```

`-hunks` reads JSON Lines — one `{"start":S,"end":E,"text":"..."}` per line,
`-` for stdin — and applies every hunk as one change set at one `-base`. A
k-site edit is then one call instead of k reads and k applies. Each hunk is
rebased against `-base`, so order does not matter; the flag is exclusive with
`-start`/`-end`/`-text`/`-text-file`. Blank lines are skipped.

## Or edit by quoting text

```
raj ctl edit /abs/path/to/file.go -old 'func f() {' -new 'func g() {'
```

`edit` is string replacement built on top of `apply`, for when quoting a line is
easier than counting to it. It inherits string replacement's weaknesses, so
prefer `apply` when you have offsets.

`-old` must match the buffer **exactly** and appear **exactly once**. Copy it
out of what `read` printed rather than retyping it — in particular, copy the
indentation. raj indents each file the way that file is already indented, so a
Go file or a Makefile uses tabs where you may have assumed spaces, and a leading
space where a tab belongs is the most common reason this fails.

Multi-line replacements go through files:

```
cat > /tmp/old.txt <<'EOF'
	if err != nil {
		return err
	}
EOF
cat > /tmp/new.txt <<'EOF'
	if err != nil {
		return fmt.Errorf("reading config: %w", err)
	}
EOF
raj ctl edit /abs/path/to/file.go -old-file /tmp/old.txt -new-file /tmp/new.txt
```

To delete, pass an empty `-new`. To insert, include a surrounding line in `-old`
and repeat it in `-new` — there is no insert-at-position for `edit`,
deliberately, since a line you can quote is one you have actually read. Use
`apply` with `-start N -end N` for a true insertion at an offset.

## Editor-side scratch: dump and patch

For a structural rewrite — a whole function, a comment block, a config stanza —
`apply` makes you compute byte offsets and `edit -old` makes you quote the
exact text. `dump`/`patch` removes both: the editor holds the snapshot and
computes the diff itself.

```
raj ctl dump /abs/path/to/file.go                          # whole buffer
raj ctl dump -start 120 -end 1480 /abs/path/to/file.go     # one structural span
# -> {"DumpID": 7, "Hash": "...", "text": "...", ...}

# edit the text locally, then hand the whole thing back:
raj ctl patch /abs/path/to/file.go -dump 7 -text-file /tmp/new.go
raj ctl patch /abs/path/to/file.go -dump 7 -text-file -    # from stdin
```

`dump` returns a snapshot id, a hash of the text, and the text. You edit that
text — freely, with no offset bookkeeping — and `patch` sends the whole edited
version back. The editor diffs old against new, rebases onto whatever the
buffer has become, and applies; if the buffer has moved past the snapshot it
reports the conflict rather than guessing. You never re-derive an offset from
stale text, which is the mistake that corrupts a file while looking like it
worked.

Snapshots are per-author and evaporate on restart. Prefer a span-scoped `dump`
for one function; dump the whole buffer only for a genuinely file-wide change.

## Batching: one frame instead of fifty


Every command above is one round trip. When you have many edits for one file,
that is fifty frames, fifty rebases and fifty replies for what is conceptually
one change. `raj ctl run` takes a *program* — a byte string of opcodes — and
runs it in order in a single frame.

```
raj ctl run -prog "$(gen-edits)"   # the program itself, as an argument
raj ctl run -prog @edits.bin       # from a file
gen-edits | raj ctl run -prog -    # from stdin
```

The program is passed as an ordinary argument. That works because no byte of
the framing is ever zero — see below — and a command line carries every byte
except zero. If a *payload* of yours contains a zero byte, which text raj will
open never does, use `-hex` instead.

The encoding, which you can emit directly:

```
program := 'R' | version | op*
op      := opcode | length | payload[length]
```

Lengths are LEB128 varints of **the length plus one**, and numeric payloads are
encoded the same way. The bias is what keeps zero bytes out: a varint of a
value of at least one never emits a zero, so nothing in the framing can be
mistaken for the end of a string. Encode `n+1`, decode `v-1`.

Arguments come first and the verb consumes them:

| op | code | payload |
|---|---|---|
| path | `0x01` | the file, as raw bytes |
| base | `0x02` | version as a varint, from `read` or `version` |
| span | `0x03` | two varints, start then end |
| text | `0x04` | replacement bytes |
| group | `0x07` | change set id |
| id | `0x0a` | request id, echoed back |
| include | `0x0b` | search: comma-separated globs to limit to |
| exclude | `0x0c` | search: comma-separated globs to skip |
| flags | `0x09` | search: bit 0 regex, bit 1 case, bit 2 whole word |
| read | `0x82` | — |
| open | `0x83` | — |
| apply | `0x84` | — |
| save | `0x85` | — |
| version | `0x86` | — |
| search | `0x87` | — |
| groups / accept / reject | `0x88` / `0x89` / `0x8a` | — |
| stats | `0x8c` | — |

**`path`, `base` and `author` persist across verbs; everything else is consumed
by the verb that follows it.** So fifty splices into one file at one version
state the path and base once, then repeat `span`, `text`, `apply`. That is what
makes the batch short.

**The rules you already know still apply, because a program runs through the
same path a flag does.** It cannot reach anything the flags cannot. In
particular you must still read the buffer before an apply in the same
connection — put a `read` verb at the top of the program, or the applies are
refused with the usual message about coordinates you have not seen. Your applies
are still attributed to you and still arrive as a proposed change set.

**A search inside a program streams.** Its matches arrive as they are found,
and only the batch's last verb ends the conversation — so a program of
`query, search, path, read` gives you match frames, then the read, then the
frame marked final. Put the search last if you would rather not read past it.

**exec, recv, hello and cancel are not available in a program.** recv parks
until the user speaks, which would hold every verb behind it; hello and cancel
act on the connection rather than a document, and a cancel queued behind the
search it means to interrupt would never arrive in time. Use the flags for
those.

**A program is all-or-nothing at compile time and sequential at run time.** If
any opcode fails to compile, nothing runs. Once it is running the verbs execute
in order, and an individual verb can still fail on its own merits — so read the
replies rather than assuming a zero exit meant every verb succeeded.

Two forward-compatibility rules worth knowing, because they decide what happens
when you emit an opcode this raj predates:

- An **argument** it does not recognise is silently skipped. Your request is
  carried out less precisely than you meant, not refused.
- A **verb** it does not recognise refuses the whole program. Skipping a verb
  would mean silently not doing what you asked.

The high bit is the difference: arguments are below `0x80`, verbs at or above.

When a program is refused, the error names the opcode it choked on.

**Prefer the flags for one edit.** `raj ctl apply -base 41 -start 120 -end 148
-text '...'` is one round trip and is easier to get right. Reach for a program
when you have a batch, or when you are replaying one you recorded.

## Say who you are

You do not have to — identity is automatic. The host plugin
(plugins/raj-gate.ts) injects, captures and scrubs `RAJ_IDENTITY` per session:
the first `raj ctl` runs unpinned, the server mints a durable `tok_...`, and
the plugin captures the adopt line before it reaches the model, reinjecting
the token on later shells. Do NOT pass `-as`, export `RAJ_IDENTITY`, or run
`who -as X -name Y`: each session and subagent already gets a distinct author
id and its own tint.

Why it still works: **every `raj ctl` invocation is a fresh connection**, and
an anonymous connection mints a fresh author id from a `uint8` space capped at
256 — per-author state (dump snapshots) does not survive between invocations,
proposals scatter across dead ids, and the space itself drains (one delegated
session burned ~130). The durable token the plugin manages is what keeps your
author id stable across those connections, so per-author state like dump
snapshots and your attribution stick. The plugin handles this; you just work.

`raj ctl who` lists everyone writing in this workspace. When more than one agent
is connected, that is how you tell whose text is whose.

## Listening for the user

The user can send you a message from inside the editor while you are working.
It arrives here:

```
raj ctl recv                 # waits until they say something, then prints it
raj ctl recv -wait 5s        # gives up after five seconds; exit 3 means nothing
raj ctl recv -json           # the messages as JSON
```

`recv` blocks. That is the point — there is no polling to do and no interval to
pick. Exit 0 with output means they said something, exit 3 means the wait
elapsed with nothing, and anything else is an ordinary failure.

Check it at natural pauses: between steps of a long task, after a test run,
before starting something expensive. A message is the user redirecting you, so
read it before committing to the next thing rather than after.

Two things to get right:

- **Keep one identity across calls.** The mailbox belongs to your
  participant, not to a connection — `recv` only finds your messages if every
  call carries the same stable author id. The plugin now provides that
  automatically (see "Say who you are"), so this takes no action on your part;
  it is why you must not override it with `-as`.
- **Messages keep while you are gone.** Anything said while you were restarting
  is delivered when you come back, so a `recv` after a reconnect may return
  several at once, oldest first.

If you are speaking the protocol directly rather than through `raj ctl`, park
`recv` on a second connection: a client serialises its requests, so a parked
recv on the same one blocks every other verb.

## Your edits are attributed, not merged in

Text you write is stored as your own pieces in the editor's document, tagged
with an author id, and shown to the user tinted so they can see what did not
come from them. Their undo will not reverse your edits, and yours will not
reverse theirs.

`raj ctl whoami` prints the id you write as. `raj ctl read -json` returns the
document split into runs, each marked `mine`, `by_user`, and with its raw
`author`, so you can tell your own text from the user's and from another agent's
before touching anything.

Use it. If a run comes back `by_user`, the human typed it since you last looked;
rewriting it destroys work they have not saved anywhere. Edit around it, or say
what you found and let them decide.

## Your changes are proposals

Each `apply` you make is one change set, marked *proposed*: it is in the
document and the user can see it, but it is flagged as awaiting their decision.

```
raj ctl groups /abs/path/to/file.go     # id, author, state, size
raj ctl reject /abs/path/to/file.go -group 12
```

`reject` backs your change out as if it had never been written — use it when you
decide your own edit was wrong, rather than computing a reverse diff, which
would leave both edits in the record. It can fail if a later edit overlaps
yours; that is not retryable, so re-read and propose against the current text.

Leave accepting to the user. It is their decision, the text is already there
either way, and it is what unlocks saving the file — see below.

## Reviewing the agent's changes

An agent's edits land as attributed *proposals*, not committed text. Reviewing
them is a short loop; run through it before saving, and the agent should have
already jumped you to the first change and closed the files that have none.

- **See every pending change set.** `raj ctl groups <path>` lists them with
  author, state and size. `groups -mine` filters to the agent's own. This is
  the list, not the first hunk in the file.
- **Read the actual edits, not just the summary.** `raj ctl diff <path>`
  renders each pending group as old→new text with byte-offset anchors. Read
  this; do not accept on the group's one-line size summary alone.
- **Jump to each change.** `raj ctl goto <path> LINE` moves the cursor to
  where the review matters. Ask the agent to goto its hunks as it makes them;
  if it did not, the group's byte offsets in `diff` tell you where.
- **Accept deliberately, or back out.** `raj ctl accept <path> -group N`
  approves one change set; `raj ctl reject <path> -group N` backs it out like
  it was never written. The plain `save` accepts everything pending in that
  file at once — use it when the whole pending set is reviewed, not as the
  discovery step.
- **When it's wrong, reject rather than edit-over.** `reject` removes the
  proposal from the record; editing over it leaves both your fix and the
  original diff in the history.

- **Decide a whole file at once.** `accept` and `reject` take `-all`: every
  pending set for the path is decided in one command (honor `-mine` to keep to
  your own). A set wedged behind a later edit is named and skipped rather than
  silently counted, and the rest still proceed.

## Saving

**You cannot save your own unapproved work, and should not try.** While your
change set is still proposed, `raj ctl save` is refused:

```
raj ctl save /abs/path/to/file.go
raj: 1 proposed change set(s) await the user's approval; the edit is in the
     buffer and will reach disk when they save
```

That is the design, not a misconfiguration. The edit lives in the buffer,
attributed and visible, and the user's own save is what puts it on disk — their
save accepts everything pending in that file at once. Do not go looking for a
way around it, and in particular do not accept your own change set to unblock a
save: `accept` exists for the user's decision, and using it on your own work
converts their review into a formality.

Two cases where the save legitimately goes through, and both are worth naming
rather than assuming:

- The user accepted your change, in the editor or with `raj ctl accept`. Then
  there is nothing pending and the save is ordinary.
- You need the bytes on disk to run something against them. Say so and ask,
  rather than saving; and remember `exec` already tells you which buffers are
  stale, so you often do not need the save at all.

## Asking the language server

The editor already runs an LSP client for the open buffers. `raj ctl lsp` asks
it directly, so a driver gets hover, definitions, completion and diagnostics
without standing up its own server:

```
raj ctl lsp hover       /abs/path/to/file.go 42:17
raj ctl lsp definition  /abs/path/to/file.go 42:17
raj ctl lsp completion  /abs/path/to/file.go 42:17
raj ctl lsp diagnostics /abs/path/to/file.go
```

Positions are 1-based `line:col` in the editor's own coordinates; the host maps
them to the server's UTF-16 grid for you. Hover, definition and completion
block for the server's answer; `diagnostics` returns the last-known cached state and never blocks, and its
answer carries a `status`: only `ok` is a real reading, so an empty list is
"no problems" only when the status says so — `starting`/`missing`/`no-server`
is refused rather than read as clean. A file type with no server is a clean error, not a
hang. Results come back as JSON — add `-json` to read the structured form.

Use this rather than parsing compiler output or grepping for a definition: the
answer reflects the buffer as it is now, unsaved edits included.

## Running commands


```
raj ctl exec -- go test ./...
```

Output streams back and the exit status is the command's own, so a failing test
looks like a failing test. A refusal exits 2 instead, so you can tell "the tests
failed" from "the tests never ran". Everything after `--` goes to the command
untouched.

**It warns when buffers are unsaved, and names them.** The command still runs.
Read the warning: a test run, build or linter reads the *file*, not the buffer,
so with unsaved changes it is checking older text and a failure it reports may
be about code that no longer exists.

If the result looks wrong and the unsaved edits are yours, save them and run
again. If they are the user's, say so and let them decide — do not save their
work for them. The warning marks which is which.

You may also use your own shell tool for commands. Prefer `raj ctl exec` when
the command reads source files, for the staleness check; either is fine for
things that do not, like `git log`.

None of this applies if you are reaching the editor over TCP: `exec` is refused
there and your own shell is the only option. See the container section above.

## When something is refused

Every refusal is a non-zero exit with a reason on stderr. None should be retried
unchanged.

| message | what to do |
| --- | --- |
| `does not appear in the buffer` | Your `-old` does not match. Re-read and copy it exactly; check the indentation first. |
| `appears N times` | Add surrounding context until it is unique, or pass `-all` if you truly mean every occurrence. |
| `read the buffer before writing it` | Run `read` or `version` on that path first. |
| `apply needs a base version` | Pass `-base` from the version `read` returned. |
| `hunks could not be placed` | The user typed while you worked; nothing was written. Re-read and redo against the new version. |
| `no open buffer for ...` | `raj ctl open` it first, or check `buffers` for the exact path. |
| `is not under <root>` | The path or glob leaves the workspace. Not permitted. |
| `unauthorized` | `RAJ_CONTROL_TOKEN` is unset or wrong. You cannot recover from this yourself; tell the user. |
| `exec is refused over TCP` | Run the command with your own shell instead. |
| `await the user's approval` | Your change is in the buffer and needs the user's decision. Do not accept it yourself; tell them it is ready and leave it. |

## Several editors

If the user has more than one raj running, `raj ctl` says so and lists them
rather than guessing which to edit. Pick one:

```
raj ctl list
RAJ_CONTROL_ADDR=/run/user/1000/raj/4821.sock raj ctl buffers
```

`list` finds editors by looking in the socket directory, so it finds nothing
when you are reaching one over TCP — there is no directory to look in.

## Field notes: agent-driving raj ctl

Things that cost a real session when an agent, not a human, drove `raj ctl`
from inside a container against an editor on the other side of a network
boundary. These are notes, not new rules: the reference above is the rules.

### Byte offsets, not string offsets

Offsets for `apply` are BYTES in the editor's own byte model, and nothing else
counts. Character-count indexing silently disagrees: any multi-byte character
before the anchor — an em-dash in a comment, a non-ASCII name — shifts the
numbers, and `apply` happily splices at the wrong byte because it validates the
range, not the text. A LINE number is not an offset either: feeding the output
of a `grep -n` dump to `-start`/`-end` lands in the wrong place — a small line
number reads the top of the file. Take offsets from `read -json` or
`search -json`, never from a line count.

The two verbs that make offsets unnecessary in the common case are `raj ctl
edit -old S -new S`, the exact-string replacement, and `raj ctl search -q
PATTERN`, which locates text in the editor's own coordinates. Use them first.

For the search path, `raj ctl search -q PATTERN -json` now reports
`byte_start` and `byte_end` per hit, so an agent can turn a search result
directly into an `apply` span without recomputing offsets itself.

Only a structural hunk — a whole function, a comment block — genuinely wants
`apply`, and then the offsets are measured against the bytes the `read`
returned; deriving them is left to the driver, out of scope for this skill.
Read-gate the `apply`, and re-read the seam after it lands.

### Read-gate every apply, and let -base do the rebasing

A chain that never writes blind is: read the file, and only if that succeeds
apply against the version you just read, with the replacement piped in on
stdin:

```
raj ctl read F >/dev/null && raj ctl apply F -base N -start S -end E -text-file -
```

`-base` is the version the offsets were measured against — almost always the
version you just read. After a hunk lands the version moves, but applying the
next hunk with the SAME base still works: the tool rebases later hunks on top
of whatever the buffer has become, exactly like the edit path agents use.
Re-reading between hunks is equally fine; just carry the new version forward.

### A heredoc ends with a newline

`-text-file -` reads stdin to EOF, so replacement text carries a trailing
newline. If your anchor was a single line followed by an empty line, the result
is a doubled blank line. Fix by anchoring the line AND what follows it when
spacing matters, then re-read the edited region — the same check catches the
worse failure: an anchor that is only a prefix of its line splits a comment or
a declaration in two and will not compile. A final gotcha in the same family:
do not put a bare delimiter line (the word that ends your shell heredoc) inside
the replacement text, or the shell ends the heredoc early and the rest of your
text runs as commands.

### Replacing code: verify both ends of the hunk

Every structural hunk — a function, a comment block — has two boundaries, and
both are where mistakes live. Fingerprints:

- An offset shift added by hand between hunks (an estimated 40 when the earlier
  edit really added 37) makes the hunk start inside a token — `funfunc (h host)
  Open...` — and end past the function, eating the following comment's `//` so
  a doc line becomes bare source. Recompute offsets from a FRESH read after
  every hunk; never add shift estimates by arithmetic.
- A hunk whose span ends short of the next comment produces exactly the
  complaint "a line in the comment that is uncommented": the `//` is what got
  eaten or split off. Same family: replace a substring that is only the PREFIX
  of its line and the tail glues onto your replacement (`return case "text":`
  from editing `done(v, err)\n\tcase "text":` but leaving `\t\treturn `).
- A hunk that ends on a `}` which belongs to the following function leaves a
  duplicate brace (`}\n}\n`) between funcs; ending it on a line whose newline
  you already consumed makes the next line climb into the previous one.
  Anchor whole lines: span start at the line start, span end at the line end
  INCLUDING its newline.
- `edit -old` that ends one line short of the block applies cleanly and leaves
  the tail as a dangling line — the reply says "applied 1 hunk(s)" and nothing
  warns you. Replacing a bullet up to its second-to-last line left its final
  line behind, reading as a duplicate of the replacement's last line. Quote the
  WHOLE block including its final line; only a re-read of the seam catches it.

The one check that catches all of these and the encoding trap above: after
every hunk, read the file and print the lines AROUND the edit — one function
before through one after — and study the seams, not the center. Splitting the
read on "\n" is encoding-safe even when byte offsets are not.

### include/exclude globs: no-slash matches the basename too

A pattern containing `/` is matched against each file's path relative to the
search root, so a path-scoped glob works: `-include 'internal/control/*.go'`
matches only files under `internal/control/`. A pattern with NO `/` is matched
against the basename as well, like `grep --include`: `-include '*_test.go'`
matches every test file in the tree and `-include 'search.go'` matches that file
wherever it sits. `filepath.Match`'s `*` does NOT cross `/`, so `*.go` matches
tree-wide only through the extension fast path; rely on the no-slash basename
rule rather than assuming a bare `*` crosses directories.
(Updated 2026-09-11; the basename rule was previously absent.)

### -q is the pattern; -regex is a flag

`raj ctl search -regex 'x'` reads `-regex` as an operand and fails: the search
pattern lives under `-q` only. Patterns are literal and case-sensitive unless
`-regex` or `-case` is given.

### Every apply is a proposal, and the version moves

Each applied hunk bumps the buffer version and is an attributed but UNACCEPTED
proposal. Do not accept your own proposals; tell the user it is ready and leave
the decision to them. A version taken before an apply describes the pre-edit
text — if later offsets build on the edit, re-read first.

### New verbs touch eight layers

Adding one verb means touching: the opcode table in internal/prog/prog.go; the
knownOps and verbNames maps and the Requests compiler in
internal/control/prog.go; the Header struct and EncodeRequest/DecodeRequest in
internal/control/wire.go; the request-field codes, verbCodes, encodeHeader and
decodeHeader in internal/control/header.go; the Request and Buffer structs in
internal/control/control.go; the BufferHost interface, Guard pass-throughs and
Dispatch in internal/control/host.go; the real host in internal/app/control.go;
and the CLI in internal/control/cli.go. Wire fields are sent only when nonzero,
which is what keeps an old client talking to a new server. And anything
implementing BufferHost — test fakes included, such as memHost in
internal/control/host_test.go — must gain the new method or the package stops
compiling.

### No shared filesystem means host-side verification

When the repo lives only on the editor's machine, nothing in the container can
compile. The contract is: proposals in the buffers, the user accepts and saves,
and `gofmt -w && go test ./... && make check` run on the host is the
verification step. State that contract out loud every session that hits it.

The rebuild boundary is part of that contract. New verbs and wire changes are
compiled into the binary: buffer edits cannot make them live, and the running
editor keeps serving the old surface until the user rebuilds and restarts it
— the container image, which bakes in a `raj` binary, needs the same rebuild
or the skill and the CLI drift from the server again. The loop is: propose in
buffers → user accepts and saves → host rebuilds (`make`, and the container
image if the CLI changed) → verify over the socket against the NEW process.
Verify semantics against the running editor and state the host-side test
contract, rather than assuming a proposal took effect because it landed.

### Revealing the agent's edits to the user

After an agent applies a hunk, the human is left to find it themselves. The
buffer shows the change tinted, but the editor's viewport does not jump to it,
and the user may be looking at an unrelated part of the file. Two conventions
make the review surface what actually changed:

- **Goto the change, always.** When a batch of edits is done, run
  `raj ctl goto <path> LINE` for every file with changes, so the viewport
  jumps to where the user needs to look. Several hunks in one file: jump to
  the first (or the most consequential); the user scrolls from there. `goto`
  confirms with "moved ... cursor to LINE", so a landed jump is verifiable
  rather than assumed.
- **Close the files that have no changes.** Files opened for the work but left
  with nothing to show get `raj ctl close <path>`, so the tab bar ends up
  listing exactly the files awaiting review. This is safe by construction:
  `close` is refused while a buffer has unsaved work, so a file with pending
  proposals cannot be closed — only change-free files actually close. Check
  `raj ctl buffers` before closing that a file really has nothing pending.

What is still missing is a way to reveal a *span* or to make the jump
automatic: either `raj ctl reveal <path> -start N -end N`, or an option on
`apply`/`edit` that returns or jumps to the affected line, would make reviewing
agent changes feel direct rather than archaeological.

This matters most when the agent is making several small edits across a large
file: without a reveal step, each change set is invisible until the user
remembers to search for it. The span is already known to the editor when the
hunk lands, so the transport cost is small.

### Prefer raj ctl verbs over container tools

A locked-down container will not have `sed`, `head`, `tail`, `grep`, `awk`, or
`cat` available, and an agent that relies on them will stop working the moment
the image is hardened. Every file inspection should go through a native raj ctl
verb, and when one is missing the right move is to propose it rather than to
reach for a shell workaround.

A delegated subagent starts with no skill context, so the same rule has to be
written into its brief: "use only raj ctl verbs — no node, no jq, no grep or
sed over files, no /tmp scratch." The workarounds this kills are the ones that
drift: parsing `read -json` with node re-implements the editor's JSON by hand,
and grepping a `/tmp` dump reads text that is already stale. If a verb is
genuinely missing, the subagent should stop and report the gap — that is how
this list grows — rather than reach for a host tool.

The prohibition has to name the temptation, not just the file access: "use only
raj ctl verbs" invites the reading "for file access", and JSON post-processing
slips in under "just parsing tool output". Briefs should say: no interpreters
(python/node/jq) anywhere in the pipeline, including on `raj ctl` output; if
the output is hard to consume, that is a verb-surface gap to report. A query
flag on the verb (`read -json -field text`, in the spirit of the flat-record
item) is the sanctioned shape, not a pipe to an interpreter.

Concrete gaps, current as of the live server (commit `42f9c653`; verify with
`raj ctl version` rather than trusting this list's age):

- **Pretty-printing JSON.** `jq` is the usual suspect, and is absent from a
  hardened container. `raj ctl ... -json` is already machine-readable, but it
  is one object per reply, not pretty-printed; consume it directly rather than
  re-formatting it. A driver that needs byte offsets uses `search -json`
  (`ByteStart`/`ByteEnd` per hit) instead of recomputing them.

Closed gaps — kept here so a stale skills file does not send an agent back to
a shell tool for something `raj ctl` already does:

- **Reading a line range.** `raj ctl read -lines A,B` (1-based inclusive; a
  bare `A` reads to the end) is live. Use it instead of `sed -n` / `head | tail`.
- **Counting bytes or lines.** `raj ctl version -json` returns `bytes` and
  `lines` alongside the version. Use it instead of `wc`.
- **Editor-side scratch.** `raj ctl dump <path> [-start -end]` snapshots a span
  and `raj ctl patch <path> -dump <id> -text-file -` takes the edited text back
  and lets the editor diff and rebase it — the agent never re-derives offsets.
  Use these instead of a `/tmp` copy plus hand-computed `apply` spans.
- **Language-server queries.** `raj ctl lsp hover|definition|completion
  <path> <line:col>` and `lsp diagnostics <path>` ask the editor's own LSP
  client over the socket. Diagnostics return the cached state and never block.

Rule of thumb: if an agent — or a subagent it spawned — pipes anything other
than `raj ctl`, it is a candidate for a new flag or verb. Document the gap and
keep the container surface small.
