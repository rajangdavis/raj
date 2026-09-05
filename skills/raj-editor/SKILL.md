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

## Read before you edit, every time

```
raj ctl read /abs/path/to/file.go            # the text
raj ctl read -json /abs/path/to/file.go      # text, version, and authorship
```

Do this even if you read the same file a moment ago: the user is typing in it.
The editor refuses a write from a caller that has not read the buffer, because
byte offsets only mean something in the coordinates of a version somebody has
actually seen.

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

```
raj ctl hello -as "$RAJ_IDENTITY" -name claude-1
```

Do this once when you start. It binds your connection to a durable identity, so
if you reconnect you keep the same author id and the text you already wrote
stays yours. Without it you are anonymous — everything still works, but a
restart makes you a different writer and orphans your earlier edits.

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

- **Say `hello` with the same identity everywhere.** The mailbox belongs to your
  participant, not to a connection. `raj ctl recv` does this for you from
  `-as` or `RAJ_IDENTITY`; if those differ between calls you are a different
  participant each time and will wait on an empty mailbox forever.
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
when you are reaching one over TCP — there is no directory to look in. In that
case `RAJ_CONTROL_ADDR` already names the one editor you can reach.
