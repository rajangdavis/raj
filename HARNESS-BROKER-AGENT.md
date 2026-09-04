# Agent harness: broker and buffer access

How an agent authors changes against the live buffer instead of the filesystem.
Design record, not implemented. Numbers live in BENCHMARKS.md; terminal
findings and root causes live in INVESTIGATIONS.md; open work lives in TODO.md.

## Shape

Three parties, two boundaries:

```
  agent  --(provider API)-->  the model
    |
    |  tool calls
    v
  broker --(RPC over a unix socket)--> raj --> Session --> Doc
```

**Agent** owns the conversation and emits tool calls. Holds no buffer state.

**Broker** validates every tool call, translates it into `Session` operations,
forwards it. Holds no state beyond in-flight requests.

**raj** owns the document. One goroutine owns the tree; everyone else sends
intents and awaits replies.

The broker is roughly the *client* role in Zed's Agent Client Protocol, with a
private extension for diffs. ACP's `fs/write_text_file` is whole-file and
string-typed, which is exactly the shape the piece table exists to avoid, so
writes go to `ApplyDiff` instead.

## What already exists

Most of the hard part is built, which changes what the harness has to be.

`Session.ApplyDiff(author, base Version, hunks) (Version, []Conflict)` is the
write surface. It is better suited to an agent than the anchored-patch schemes
other tools use:

- **Staleness is handled by rebasing, not by matching text.** An agent reads at
  version V and submits offsets in V coordinates; `ApplyDiff` replays the
  journal from V forward. No `old_str` to get wrong, no whitespace sensitivity.
- **Hunks are rejected independently.** An agent fixing five call sites does not
  lose four because one moved, and `Conflict.Index` says exactly which to retry.
- **Overlapping hunks inside one diff conflict with each other**, which is the
  intra-batch overlap check the broker would otherwise have to implement.
- **Authorship is already modelled.** `Original`/`User`/`Agent`, with subsequent
  agents taking 3, 4, and so on. Attribution is a property of the store, not
  something the harness bolts on.
- **`Begin`/`End` group ops**, so one diff can be one undo entry.
- **`OpsSince(version)`** gives a client everything it missed, which is the
  natural basis for streaming updates to a connected agent.

So the harness does not need to invent an edit protocol. It needs a transport, a
validation layer, and a decision about exec.

**Caveat.** The rebase bug at the top of TODO.md is directly load-bearing here:
a resurrected op counted in the wrong coordinate frame can place a reversal at
an offset that splits a rune. Agent traffic is the workload most likely to hit
it, since agents submit against older versions than a human typing does. That
bug is a prerequisite, not a parallel track.

## Transport

**A unix socket with length-prefixed frames.**

The brittleness usually blamed on JSON-RPC is really newline-delimited JSON over
stdio: one stray `printf` from a library desyncs the frame stream permanently. A
socket cannot be polluted by a subprocess's stdout, and length prefixes remove
the newline dependency. The encoding was never the problem.

**Not shared memory.** Hunks are bytes; the document is megabytes; the model
round trip is ~10^8 ns. Wire serialization is unmeasurable next to it. Optimize
the wire for inspectability instead, and keep the single validation chokepoint
that shared memory would destroy.

**Transport is the last decision.** Define a Go interface — `BufferHost` — with
the operations wanted, and make the socket one adapter over it. In-process is
another. Adding the socket must change nothing above the interface.

## Verbs

`search` first, `apply_diff` last. The ordering is deliberate: search is
read-only, so a bug costs a wrong answer rather than a mangled file.

| verb | maps to | notes |
| --- | --- | --- |
| `search` | `search.Run` + open-buffer overlay | read-only, cancellable, capped |
| `read` | `Doc.Slice` / `Doc.Spans` | returns bytes plus the `Version` read at |
| `version` | `Session.Version` | what a later `apply_diff` bases on |
| `ops_since` | `Session.OpsSince` | streaming what changed |
| `exec` | see below | the hard one |
| `apply_diff` | `Session.ApplyDiff` | returns the new version and conflicts |

### search

The first verb to build, and the one with the clearest payoff: one engine serves
the UI and the agent, so an agent's grep sees unsaved edits and cannot disagree
with what the user is looking at.

Shape: `Query` in (the existing struct), a stream of `Match` out, terminated by
`{files, capped}`. Three requirements the current implementation does not meet:

- **Disk plus a dirty overlay.** Walk disk for the whole tree, scan open
  buffers directly, and let buffer results shadow disk results for the same
  path. Disk stays the main path because it has to: 9,502 files against maybe a
  dozen buffers, and files have no owner so they parallelize freely. The overlay
  is small by construction.
- **Streaming and cancellation.** A multi-second walk with no progress is
  unusable, and a query will change mid-flight. This is the same seam TODO.md
  already names for getting a finished search onto the event thread. Do it once
  for both.
- **Snapshots, not locks.** A search walks while the owner goroutine may be
  applying edits. Stores never erase, so a `Version` is already a cheap
  immutable snapshot — hand the searcher a version rather than locking the tree.
  This is a structural advantage over an editor with a mutable buffer, and it
  should be used rather than worked around.

Matching against buffer content is the one genuinely new piece: `bytes.Index` is
the stdlib's assembly and cannot run across a rope, so a match straddling a
piece boundary needs either a per-line materialization or explicit boundary
handling. Line-at-a-time materialization is almost certainly right — it keeps
the fast matcher and bounds the copy to one line.

**Do not shell out to ripgrep instead.** It would be roughly ten times faster
than raj's walk, and it would give two engines with different regex dialects
returning different results for the same query. Against model latency the
difference is free; coherence is not.

## Validation

The broker's reason to exist. Every one is a rejection, never a guess.

| check | what it prevents |
| --- | --- |
| path inside root | escaping the workspace |
| **glob patterns inside root** | `../` in `Include`/`Exclude` is a second hole |
| base version present and not garbage-collected | rebasing against nothing |
| author id belongs to the calling agent | misattributed text |
| read-before-write | blind edits |
| write permission for the target buffer | unreviewed changes to what the user sees |

Note that hunk overlap and staleness are *not* on this list: `ApplyDiff` already
handles both, correctly, and duplicating them in the broker would mean two
implementations of the same rule.

**Transactions.** One diff is one `Begin`/`End` group, so one undo reverses one
agent edit. Without it a broker crash mid-batch leaves a half-applied file that
`ctrl+z` cannot undo.

## Exec and dirty state

Reads are solvable by RPC. **Exec is not.** When an agent runs `go test`, the
processes that open source files are `go build`, the compiler, and the test
binary. They call `open(2)`. There is no seam to interpose on. Same for `git`,
linters, formatters, and `grep`.

### Decision: refuse first, measure, then relax

**v1 — refuse.** If any buffer is dirty, `exec` fails and names the dirty files.
Cheapest to build, and the failure is loud and reversible.

Refusing buys exactly one property, and it is the one that cannot be recovered
from: **an agent action never silently writes the user's unsaved edits.**
Refusing is reversible; writing someone's buffer is not.

**Instrument it.** Count per session: execs blocked, and how many of those were
dirty only in agent-authored spans. That counter decides v2 with data instead of
argument. If blocks are near zero, the annoyance was theoretical.

### v2, if the counter warrants: split on authorship

Pieces already carry an `Author`, and `Spans` reports it. So:

- dirty spans **all agent-authored** — flush and exec; the agent is flushing its
  own work
- **any** dirty span user-authored — refuse and name the file

Strict where strictness matters, silent where it does not.

### Rejected

- **Agent must call `save` before `exec`.** Same bytes reach disk as an
  automatic flush, but models forget preconditions, so the common path becomes
  exec, error, save, exec — roughly double latency on the most frequent
  operation in a test-fix loop, for no additional safety.
- **Materialize into a worktree.** Requires mapping test output paths back and
  fights the toolchain's module and cache assumptions.
- **FUSE over the buffer.** The genuinely correct "never touch disk" answer, and
  the leaf layout could support it. Also a platform-specific dependency and a
  per-syscall tax on a compiler that opens thousands of files. Interesting, not
  next.

### Flush mechanics, when flushing lands

Temp file plus rename per file, so no subprocess reads a half-written file and
no crash mid-flush corrupts the tree. Cost is proportional to dirty files, not
repo size, and is paid at exec boundaries rather than per edit.

### Exec belongs in the broker regardless

It is the natural place to attach cancellation, which the tool-call path does
not have today. That is worth doing independently of the dirty-state policy.

## Order of work

The socket is last. Each step is useful on its own.

1. **The rebase bug.** A prerequisite, not a parallel track.
2. **`BufferHost` interface** — the vocabulary, no transport.
3. **`search` against buffers**, with streaming and cancellation. Delivers a
   real improvement to the editor on its own: unsaved edits become searchable.
4. **In-process adapter** — direct calls to the owner goroutine over a channel.
   Zero serialization.
5. **`exec`**, refuse-on-dirty, with the counter.
6. **Socket adapter** — must change nothing above it.
7. **`apply_diff`** over the wire, wrapping the existing `ApplyDiff`.
8. **(If the counter warrants)** authorship-split flush.

## Measurement

In order of value.

- **Transport equivalence (fuzz).** A diff applied over the socket is
  byte-identical to the same diff applied in-process.
- **Search overlay correctness.** For a dirty buffer, search results equal what
  a search of the flushed file would return, and shadow the on-disk results for
  that path.
- **Snapshot isolation (fuzz).** A search running against version V returns
  results consistent with V while edits land concurrently — no torn lines.
- **Transaction atomicity.** Kill the broker mid-diff; the buffer is either
  fully before or fully after, and one undo reverses one agent edit.
- **Exec coherence.** After a successful `exec`, for every flushed buffer,
  on-disk bytes equal the document. Fuzz with edits arriving during flush.
- **The eval that actually matters.** First-attempt apply rate and output tokens
  per successful edit, against a `str_replace` baseline on a fixed corpus. An
  eval, not a benchmark — ns/op is the wrong instrument.
- **Broker overhead** as a share of turn latency. Measure once to confirm it is
  noise; do not tune it.

## Open questions

- **Do subagents get buffers at all?** Given the exec problem, keeping subagents
  on disk and reserving buffer access for the agent editing what the user is
  looking at remains defensible.
- **Version retention.** `ApplyDiff` rebases from an arbitrary base, so the
  journal has to retain everything back to the oldest version any agent still
  holds. That is a lifetime question compaction will have to answer.
- **Author id exhaustion.** Ids are one byte and double as buffer id. The
  pressure is accumulation across invocations, not concurrency. Reclaim at
  compaction, and guard reuse-while-live with a sweep rather than refcounting,
  which would touch allocation-free hot paths.
- **Reconnect.** What happens if raj exits with unsaved buffers, or the broker
  dies with a diff in flight. Grouping covers the second; the first needs a
  decision, and overlaps with session persistence in TODO.md.
