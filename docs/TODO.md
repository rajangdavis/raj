# TODO

Open work only, collapsed 2026-09-17. Measured numbers live in BENCHMARKS.md;
root causes, terminal findings and decisions live in INVESTIGATIONS.md. Raw,
dated agent feedback lives in docs/dev/AGENT-FEEDBACK.md (local, git-ignored); its still-open items are the
last group under Later. An item states the symptom and why it is worth doing —
if it needs the history it belongs in a spec or INVESTIGATIONS.

## Now

Nothing is listed here on purpose. What is live, in flight, next and waiting on the owner is in `docs/NEXT-STEPS.md` ("Where we are"); this file is the backlog only.

## Later

One line per item, grouped by theme; nothing here is scheduled. Numbers live in
BENCHMARKS.md and decisions in INVESTIGATIONS.md.

### Hooks — a projected check cannot see shell scripts (2026-09-30, user)

- **`make check` skips `sh-parse` and `shellcheck` in a projected tree.** Both
  lint the tracked shell scripts (`git ls-files` filtered to `*.sh`), and a
  scratch tree has no `.git`, so a projected run prints "no tracked shell
  scripts" and passes — while the workspace `cycle` lints them and can fail.
  That is what happened on 2026-09-30: strand 3.5 went through check run 162
  green, then cycle 163 died at `shellcheck` (SC1007 in
  `examples/hooks/no-ignored-source.test.sh`). A full cycle afterwards, run
  169, reported check and build ok (reported by the orchestrator 2026-10-01;
  the reviewer cannot read the run log), so that line is not failing now. The
  blind spot is still there. Fix one of: give the projected tree enough git
  provenance for its tracked-file query to answer, or have the projected check
  lint the workspace's tracked scripts directly (they are pinned to the
  accepted tree anyway, per H7). *A gate that skips half its work in the tree
  agents verify is the half that bites at the cycle.*

### Harness — measure call counts, do not ask for them (2026-09-30, user)

- **Self-reported call counts are wrong in both directions.** The `raj-gate`
  ledger (`~/.local/share/opencode/raj-tool-ledger.jsonl`) records every tool
  call with its session id, so the true number is a query away; but every brief
  asks the agent for "your call count" and the agent estimates. One wave of
  evidence: G1 said ~95, measured **203** (2.1x under); G3 said ~180, measured
  216; the Track G reviewer said ~146, measured 121. The tool-budget discipline
  is built on these numbers, so the orchestrator should read them from the
  ledger by session id and report the measured figure. `call-runs.mjs` is not
  the answer here: it cannot read this container's `opencode.db` (no `part`
  table), so the ledger is the only reliable source.

### Hooks — see a run while it runs (2026-09-30, user)

- **A run is readable but not pushable.** `raj hook log` (the last 100 runs,
  with exit, duration and `recovered`), `raj hook ps` (in flight) and
  `raj hook log --show <id> --tail N` (a live tail, even for a detached run)
  are all reads that cross TCP, so an agent in a container can already watch a
  cycle. What is missing is ergonomics: no `--follow` on `--show`; no
  `hook-finished` event on the bus, so a waker must poll; no opencode tool for
  `ps`/`log` (the agent shells out); and nothing surfaces runs in flight in
  `raj ctl status`, the status line or the debug overlay. Add those four, and
  settle the durable run log (H3: memory plus journal, or SQL) so a history
  survives a restart rather than only a `recovered` entry.

### Editor — record the drawn screen over time (2026-09-30, user)

- **`screen` reads one frame and nothing records a run of them.** The verb is
  `screen [--until TEXT]` — the drawn screen as text, `--json` adds the cursor
  — and that is the whole surface: no `--save`, no frame log, no timestamps.
  Nor is there a keystroke-injection verb, so an agent can read the screen
  between buffer-level actions but cannot script an interaction. The gap
  matters because the flicker fixed 2026-09-30 was invisible to every
  instrument: no test failed, no diagnostic fired, and it was found only
  because the user watched the debug overlay field flip. Add `screen --follow`
  (or `--record`): emit a frame whenever the drawn screen changes, timestamped,
  so a recording is compact and a temporal or visual bug is answerable.
  `--until` is already the predicate wait; this is the timeline half. Keep it a
  read: a key-injection verb would let an agent drive the UI as the user, which
  crosses the propose/decide line (`accept` and `save` are human-only) and
  would have to be local-human-only if it is ever added.

### Owner gestures (Track G) — review fallout (2026-09-30)

- **The generated keybinding reference omits `Native` chords.** `keys.Doc()`
  renders only `Bindings` + `Reclaim`, so the new waiting-list chord
  `ctrl+alt+v` (`keys.ReviewProposed`, a Native) has no `docs/KEYBINDINGS.md`
  row. Decide whether `Doc()` should render `Natives`.
- **The waiting list's enter opens a non-text row's path.** A publish row
  carries the wave name, so `openFromPicker` tries to open a file named after
  the wave; a delete or rmdir row has a path but no line to jump to. Make
  enter a no-op for a row with no text, or gate it on the row kind.
- **Overlap reporting compares bounding spans.** A large multi-hunk set flags
  every set inside its span, so this wave produced three "conflicts" that were
  disjoint hunks (`conflict_test.go` 3/2, `host.go` 2/5, `table.go` 5/3).
  Compare hunks, or say "encloses" rather than "overlaps".
- **An invalid deletion-only set is invisible.** `Pending()` drops it, so
  neither the waiting list nor `raj ctl proposals` can show it, and its
  projection is unpinned (`AGENT-FEEDBACK`, invalid-set wave). Pin the
  projection and give it a review row.
- **Author ids are reused while their pending sets are live.** A fresh
  `register` was assigned author 130, writer G1's id, so `--mine` adopted G1's
  pending sets and a `revert --author 130` from that connection would have
  discarded them. Do not reuse an id any pending journal op names.

### Seams (intent) — trial findings (2026-10-01)

- **A seam is only as durable as the journal it indexes.** The intention row
  (name/owner/base/members) survives a daemon restart, but the change sets its
  members name do not, so after a restart `intent prove <name>` refuses with
  `dependent group(s) [...] need member group N in <path>, which is missing`
  and every pre-restart seam is dangling by construction. Until the durable
  journal lands (`Durable log`, below), treat a seam as a single-session object.
  Detail: `docs/dev/AGENT-FEEDBACK.md`, "seam-layer trial (2026-10-01)".
- **`intent prove` may wedge the editor it drives.** One prove ran the `check`
  hook (`gofmt -w cmd internal`) inside the projected tree and was still in
  `go vet`/`make` when the control socket stopped answering (`i/o timeout`; a
  daemon restart cleared it). The fast-failing prove did not wedge, so the
  suspect is a prove that reaches materialise + a write-hook/build — the
  "a gate that can block the thing it runs in" shape. Reproduce with a clean
  passing prove on a durable seam before fixing. Root cause read 2026-10-02:
  `host.Intent` runs `runIntent(context.Background(), ...)` on the event thread
  (`internal/app/intent.go:43`) and `runCheckHook` is a synchronous `cmd.Run()`
  with no timeout (`internal/app/seam.go:113`), so the editor is blocked for the
  whole check while the client is told "timed out waiting for the editor" after
  5 s. See the 2026-10-02 audit section below.
- **`intent group --task T` cannot build a seam from agent sets.** `groups
  --task <session>` answers `N change set(s) belong to another task` because a
  set's `Task` is not its session id, so the documented seam builder has
  nothing to select; the workaround is `intent new --ref BASE <PATH=N...>`
  by hand. Same root as the D-6 "plugin should inject `--task`" item.
- **`intent new` requires `--ref BASE` with no default** and no hint until it is
  passed. Decide a default (the current HEAD, as `materialise` resolves) or
  state the requirement in the usage.
- **CLI `--json` shapes for the intent verbs are not uniform.** `intent show
  --json` nests under `{"intention": ...}` (singular), `intent list --json`
  under `{"intentions": ...}`, `groups --json` is a bare array. Two writers and
  the orchestrator each guessed wrong once; document the shapes or normalise
  them.

### Layered proposals

- One jump path: move `host.Goto` onto `jumpToSessionLine`, or state why the
  column-addressed jump stays separate.
- **The deletion-only classification is written twice.** `deferredDeletions`
  and the head of `deletionLeases` (`internal/piecetable/groups.go`) each walk
  the journal for the live Proposed sets that have members and no insertions;
  one ordered helper would keep the two from drifting. `foldProjectOracle`
  (`project_test.go`) calling `deferredDeletions` also gives the fuzz oracle the
  same predicate production uses, so it cannot catch a wrong classification.
- Change gutter vs `HEAD`, annotated `read`/`exec`/LSP composition, git verbs
  (read-only diff first).
- Groups carry rebased ranges; the change-gutter and diff-style rendering
  consumers do not use them yet.
- `raj ctl diff -vs HEAD` (git, stretch): needs the range-rebase walk and a
  read-only `git show HEAD:<path>`.
- Diff-style rendering: green additions, red deletions; the gutter carries who.
- A rejected group can be wedged by a later overlap; name what overlapped so the
  caller can re-propose.
- An app-level advisory-lease test with two identities (only the Rejected
  refusal is covered today; the wire path is unpinned).
- **A zero-width insertion inside an excluded run is not covered by the
  overlap projection rule.** `projOracle.overlap` detects only an included
  edit's *replaced* span entering an excluded inserted run, so an included
  insertion placed strictly between the excluded run's bytes neither sets the
  fuzz witness nor takes `unapplyRemoveIns`'s partially-consumed branch; the
  run is removed whole and the included text can fuse with what follows. Decide
  whether the boundary re-anchor is intended (pin it) or fold the case into the
  structural-separator rule (detect an included piece interleaved between two
  owned pieces). See `docs/dev/AGENT-FEEDBACK.md` (2026-09-26).
- Durable log: Phase 1 (the SQLite session/positions/settings store,
  `internal/store`) landed 2026-09-17 and is now wired into the app (the
  session blob, per-file positions and resolved settings); still to do: fold
  the journal op log behind the record interface, compaction and checkpoints,
  and the attachment model (loaded vs announced, headless read).

### Editor and LSP
- Symbols are found by leading keyword, not parsed; per-language scanning is the
  next step (tree-sitter is the direction).
- The reserved-chord tables are short; extend them whenever another collision is
  found the hard way.
- Autoscroll at the 150 ms idle tick is visibly stepped; a faster tick while a
  drag is held would smooth it.
- A press on a list does not drag it; rubber-band selection and drag-to-reorder
  are undecided.
- The problems pane filters by severity and open files only; "current package"
  needs a package notion the pane does not have.
- Blinking secondary carets: nice, and a long way down (needs a blink-rate
  tick).
- Mouse-hover tooltips for inlay hints; `cmd+.` apply-hint-edit.
- A stale chord in a test passes vacuously (`TestDefinitionWithoutAServerIsHarmless`
  presses the retired `alt+super+d`); decide whether the harness fails loudly
  and tests read `keys.Bindings`.
- The LSP campaign's deliberate follow-ons: `semanticTokens/range` and delta,
  refresh requests, `codeAction/resolve`, lazy `codeLens/resolve`,
  `documentLink` rendering, `signatureHelp` triggers, `onTypeFormatting`
  multi-cursor, dynamic registration beyond `workspace/symbol`, a format-on-save
  setting, `$/progress` and `$/trace`.
- File lifecycle remaining: directory rename and `run --prog` reachability for
  the new verbs.
- Small and split panes, and how they resize.
- No Bubbletea adapter yet (the `ui.Host` surface keeps growing).

- `Tabs.Paths` now has no production caller — every session write goes through
  `SessionState` (`internal/app/session.go`), which iterates `Tabs.All()` itself
  — so it duplicates the preview-skip rule and is kept alive only by its test.
  Retire it or make it the one copy of the rule.
- **Warm-on-save-as does not sync a pane the user has left.** `warmSaved`
  (`internal/app/lsp.go`) starts the server, but `for_` returns nil until the
  handshake finishes so the immediate `syncDoc` is skipped, and the idle-tick
  sync only covers the active pane (`maybeRequestHints`, gated on inlay hints).
  A saved-as buffer that is clean and not active is never `didOpen`'d, so its
  diagnostics still wait for a hover or reopen. Remember the warmed path and
  sync it once on the next idle tick.

- **`servers.stopAll` can block on a handshake still in progress (2026-09-25).**
  `internal/app/lsp.go` `stopAll` calls `srv.Stop()` on every server in `byID`,
  including one whose `starting` flag is set and whose `start` context has a
  60 s timeout, so a daemon shutting down mid-handshake can wait out that
  context. Give `Stop` a bounded grace or cancel the start. *A shutdown that
  waits on a 60 s context is not a shutdown.*

### Workspace and search

- **Workspace/search-pane replace is deferred** pending a definition of writing
  unopened files: a replace across results would edit buffers that have no tab,
  and the write target and undo story are unstated. *Deferred with a named
  blocker.*
- Save-as has no directory listing; the natural shape is the completion popup
  anchored under the field.
- Unnamed buffers have nowhere to persist (needs the dirty-buffer journal).
- The buffer overlay copies each dirty document on every search; scanning
  `Spans` in place needs a boundary-crossing matcher.
- Parallel walk: 51% of a search is syscalls; needs streaming results or
  deterministic truncation before a worker pool, and a multicore box to measure.
- Re-run the call census after the next wave against the success criteria in
  AGENT-FEEDBACK (read share from 32.5%, `search→read` from 2,761).
- `search --path` into a hidden directory ignores `--hidden`; decide and state it.
- Multi-target read: `--json` shape differs from the single read (no author, no
  per-file spans); decide whether it carries authorship.
- Multi-target read: a shared span that overruns one target refuses the whole
  call while a shared `--lines` clamps per file; decide clamp or refuse.
- `Buffer.Bytes` means document length in a `buffers` reply and contributed
  bytes in a multi-read; document it.
- `read --json` now echoes a line span as `line_start`/`line_end`, so a driver
  re-derives the line range it asked for without a second call; a byte-span
  read (`--start`/`--end`) still echoes no `start`/`end`. Echo the byte span
  too, so every span read is self-describing.
- **`read --json` gives no per-line byte index, so a line-addressed `apply`
  needs a second `search --json` (2026-09-22).** A whole-file read returns
  `text` with `bytes`/`lines` but no byte offset for each line, so a driver
  editing at a line it just read must search for the line to get a `byte_start`
  (or do the shift arithmetic the skill forbids). Echo a line-start offset
  array (or one byte offset per line) with the text, so a read is an offset
  source on its own. *A read should be enough to edit where it read.*
- **The per-target `read --at` and multi-`-q` `search` transports are N client
  calls inside one CLI call (2026-09-22).** `read --at` groups targets by
  identical span but still issues one `c.Do` per distinct span, and
  `search -q A -q B` issues one `DoStream` per pattern, so a batched tool call
  is still N round trips. Convert both to a single `run --prog` frame;
  `knownOps`/`verbNames` already carry `OpRead`/`OpSearch` with `OpSpan`/
  `OpQuery`. *One tool call should be one round trip.*
- `run --prog` payload paths are unmapped (`Client.toEditor` maps the field verbs
  only).
- `braceTally` counts markdown fences as code; a per-language "is this source"
  answer is the real fix.

### Saving, buffer and journal

- Owner and group are not preserved (needs a root-capable machine: `tmp.Chown`,
  `EPERM` ignored, a root-gated test).
- `IsBinary` has no production caller; delete it and its test, or document it as
  the shared predicate.
- A 16 ms coalescing window for streaming agent hunks.
- The idle-tick `Compact` scan cost is benchmarked but the numbers are pending
  the host run; then decide a fragmentation trigger or a cheaper `pieceStable`.
- Cursor offsets are not remapped across reverted bytes (a cursor past EOF
  survives `revert`).
- A dangling symlink defeats the resolved-root check (deferred 2026-09-16):
  decide whether to detect `ModeSymlink` and refuse, or keep the lexical
  fallback.
- **The `.git` transient skip is over-broad (2026-09-18).** `isGitPath`
  (`internal/app/session.go`) skips any path with a `.git` component from the
  session and from every journal site, so a deliberately opened `.git/config`
  is never restored. The trade is deliberate (git writes COMMIT_EDITMSG/
  MERGE_MSG there and a reappearing message is noise); decide whether to narrow
  the predicate to the git-written transient names or keep the broad skip.
  *A file the person opened is not the same as a commit message.*

### Control socket and agent surface

- **After a daemon restart a named identity can send under a throwaway token key (2026-09-28).** The cycle announced as key `raj-cycle` (author 130) before the restart and closed as the SAME author 130 but under a `tok_...` key after it: the author row survived the restart (L3.3) but the named key did not rebind for the post-restart send, so a reply to the closing report targets a discarded key. Rebind the key on reconnect, or re-hello the `--as` key after a dial failure, or make the client refuse to send under an adopted token when an explicit `--as` was given. *A restart must not change who a name points at.*
- **The registry leaks a durable row per anonymous handshake (2026-09-27).** `who` shows 122 participants after a day of agent traffic, most `tok_...` rows: a `raj ctl` invocation that arrives with no identity appears to leave a durable participant row rather than the reserved, row-less id the skill documents, so it grows without bound and inflates `who`/`Drivers()`. Confirm and reap, or make the adopted-token path row-less. *An anonymous handshake is not a participant.*
- **Mail is confirmed at the next Park, not after the prompt (2026-09-28).** The store marks a batch delivered when the identity parks again, so a reader that exits 0 and dies before prompting the session loses that batch while the daemon lives, and a daemon restart between hand-off and confirm replays it for a duplicate. There is no client ack for the hand-off. Carry the delivered row ids in the recv response and add a `confirm` op the client sends after the prompt succeeds, or let a re-park on the same connection re-request an unconfirmed batch. *A hand-off is not a delivery.*
- Three nested header strings are still JSON (`DiffJSON`, `LSPJSON`,
  `StatesJSON`).
- The request header can go once recv, hello and cancel have opcodes, or once
  they are decided to stay JSON forever.
- Every agent shares one tint; a user watching two agents cannot tell them
  apart.
- Journal persistence of `claim` remains; the client `watch` push landed (see COMPLETED).
- Nothing reads the `exec` stale-run counter yet.
- No flush mechanics for v2 (temp-file-plus-rename per dirty file).
- Nothing is encrypted; the token authenticates but frames are plaintext.
- Path inference is a single question; a real answer resolves paths relative to
  the editor's root.
- A workspace-visibility flag (`--workspace`/`--allow`) to scope what agents can
  reach.
- Dump snapshots are keyed by author id, not identity; `dump` to `patch` fails
  across a reconnect.
- Nothing in the editor calls `App.Tell` except saves; the user-facing prompt,
  its chord and a multi-driver picker are missing.
- A full mailbox is reported to nobody (same missing caller).
- Still no notifications for buffer changes; a `subscribe` op wants a version
  cursor and a slow-reader recovery story.
- `apply` cannot create or reach an unopened file.
- A connection-scoped "editor restarted" marker (a generation counter on
  `whoami`).
- A saved wave has no enumerable diff for the review pass: add a
  `history`/`changes` verb, or make the brief's file list the explicit contract.

- The installed `raj-editor` skill
  (`~/.config/opencode/skills/raj-editor/SKILL.md`) still names
  `--control-exec`; only the repo copy was updated. The orchestrator edits
  opencode config directly.
- **A second `SetHookDir` can double-adopt a detached run (2026-09-26).**
  Recovery is not idempotent: calling it twice on the same directory adopts each
  live `<id>.pid` again and starts a second watcher, so a completion can be
  logged twice and two cancellers race. Guard adoption against an id already in
  the registry, or make `SetHookDir` one-shot. *A recover that runs twice is a
  duplicate, not a no-op.*
- **A malformed `<id>.pid` is ignored but never reaped (2026-09-26).** A legacy
  or corrupt pid record is neither adopted nor logged lost, so its files linger
  until a prune that may never come (a live run below the keep window is never
  pruned). Treat an unreadable pid record as lost, or remove it during recovery.
  *Ignoring a file is not cleaning it up.*

- **The mailbox bound drops mail silently (2026-09-27).** `MailboxDepth` (16)
  drops the oldest undelivered messages when a reader is dark and mail piles
  past it, and only the daemon logs it ("dropped N oldest undelivered
  message(s)"); the recipient is told nothing. One save is one message, so a
  short dark window overflows the box - and the plugin's 60s backoffs made
  exactly that window. Decide: coalesce repetitive notices (N saves in one
  message), surface a drop notice to the recipient, or raise or remove the
  bound. *Silent loss is the second path behind the missing oc2 notices.*

- **The CLI never presented itself as human (2026-09-27).** Every `raj ctl`
  connection joined as an agent, so save / save --all / land / approve refused
  the user's own shell. Fixed client-side on unix (Kind:human in the hello;
  TCP stays agent-only and the server downgrades). Re-examine the help wording
  that advertises `save --all` and `land` as human gestures, and the
  TCP-requesting-human path.
- **One generic op-scoped JSON slot on the wire (2026-09-27).** Hook, Git, Diff
  and LSP each carry their own JSON field and response code; collapse them onto
  one op-scoped slot and free three to four response codes.
- **The 0x01-0x7f header space is full (2026-09-28).** `hHookParams = 0x1f`
  took the last free request code: request fields use 0x01-0x1f, response fields
  0x20-0x7f, and 0x00 is unrepresentable in the framing. (The original premise
  was wrong - a code at or above 0x80 does *not* read as a verb in a header;
  that is a program rule, and `decodeHeader` passes no known set, so an unknown
  header code at or above 0x80 is skipped.) A new header field needs a freed
  code (the op-scoped slot above frees three or four), a nested field carrying
  its own sub-codes, or a change to the range rule;
  `TestUnknownHeaderFieldIsSkipped` splices 0x80 because no free argument code
  remains. *A full code space is a design problem, not a numbering one.*
- **`hGenOut 0x5e` is a duplicate, free once nothing emits it (2026-09-29).**
  `encodeHeader` wrote `h.Gen` twice - as `hGen` 0x1a and as `hGenOut` 0x5e -
  and `decodeHeader` read both into the same field. The `hGenOut` write is
  dropped, so nothing emits it; decoding it stays, so a peer that still sends
  it keeps working. What frees the code is that drop: once no build that emits
  0x5e remains in the field, the response code can be reused.
- **`hFindCount 0x58` is written but never read (2026-09-29).** `host.go` fills
  it in find's answer and `wire.go` carries it, but no `raj ctl` client reads
  it (`cli.go` and `client.go` hold no reference), so the response code is
  reclaimable. What frees it is dropping the `FindCount` write in `host.go`
  (or giving `find` a reader such as `--count`, which would keep the code).
- **The builtin in-process environment is two parallel mechanisms (2026-09-28).**
  `builtin.WithStepEnv`/`StepEnv` and `builtin.WithParamEnv`/`ParamEnv` are the
  same context-value pattern twice, carrying the same `[]string` of `KEY=value`
  entries; a builtin leaf reads both, and `runChainStep` concatenates them for a
  shell step. One `WithEnv`/`Env` channel carrying a single combined slice would
  serve both, leave a shell step nothing to merge, and give a third source
  nowhere to duplicate. Leaves in `internal/control/hookbuiltin_test.go` read
  each getter.
- **`intent prove` runs the check hook's argv directly, bypassing parameter
  validation (2026-09-28).** `app.runCheckHook` parses the stored row and runs
  `h.Argv`, so a required parameter on `check` would be silently omitted where
  the server would refuse; it is safe today only because `check` is
  `["make","check"]` with no declarations. Resolve through the same path, or
  refuse a `check` hook that declares a required parameter, so prove and the
  gate cannot diverge. *One hook, two runners, one contract.*
- **S3's carve must name the qualified member type (2026-09-28).** The
  qualified member type now exists (`store.IntentionMember`, checked
  2026-10-01). What is left is for S3's carve: take a task's members from
  `groups --task`'s qualified listing and name that type.
- **A rejected set pins a buffer dirty that a save cannot clean (2026-09-28).**
  A buffer held one accepted set and two rejected sets; saves wrote the agreed
  composition, `raj ctl diff` was empty and disk matched, yet `status` stayed
  dirty across two saves, so the old publish-single.sh readiness gate refused
  forever (that gate is retired; the dirtiness is not). Rejected text stays in
  the view only, and `clear` (claim-gated: path positional, id via `--group`)
  is the only disposal. Now `status` names the count; `reject` still does not
  reverse text.
- **Retroactive wave-to-MR discovery (2026-09-28).** The publish result is NOT
  stored; the mapping is recovered instead. The branch name is deterministic
  (`raj/wave-<wave>`), the pushed sha is `git rev-parse origin/raj/wave-<wave>`
  after a fetch (the remote-tracking ref IS the record), and the MR URL is a
  lookup: `gh pr list --head raj/wave-<wave>` (or the GitHub API
  `GET /repos/<owner>/<repo>/pulls?head=<owner>:raj/wave-<wave>`), with
  `.../pull/new/<branch>` to create one. Add `intent show <wave>
  --remote-status` (or a discovery subcommand) that derives the branch name,
  asks the remote whether it exists, and prints sha + URL. Record nothing till
  then.
- **The container ctl can predate the editor (2026-09-28).** The `raj ctl` in a
  harness container is a build from an older commit, so verbs exist on one side
  only (`--action`, `intent`) and `exec` is refused over TCP - there is no
  workaround from inside. Fix: a build/redeploy step that rebuilds the container
  image AND the editor from the same commit, and/or make the existing
  commit-skew warning actionable - fail loudly with "rebuild the container"
  instead of "unknown command".

- **`MaterialiseTree` has no direct test (2026-09-28).** It is the core of both
  export and prove yet is only compile-covered. Add a direct git unit test next
  pass - a temp repo and a projection, asserting the tree object and that the
  index and worktree are untouched. Not a probe.

### Client mode and attach

- **A snapshot cannot capture mid-transaction.** `SnapshotState` omits
  `Session.depth`, so a snapshot taken between `Begin` and `End` loses the open
  undo transaction and the restored session groups those edits differently.
  Decide whether to refuse a snapshot while a transaction is open or capture
  the depth. *A snapshot is exact or it says it is not.*
- **No test exercises `Compact` before a snapshot.** The compacted-origin path
  is captured (`Snapshot.Compacted`) and restored, but every snapshot test seeds
  a fresh session; a compaction-origin round trip is unpinned. *The subtle path
  is the one that breaks silently.*
- **A client's local undo resets on a re-sync.** Undo in an attached client is
  client-local; the forward is a debounced apply, and a watch install replaces
  the pane's journal, so cmd+z after a refresh cannot reverse the edit it just
  forwarded. Decide whether undo is mirrored across the wire or the pane marks
  the reset. *A gesture that silently stops working is worse than one not
  offered.*
- **A forwarded edit's conflict has no surface.** A local edit the daemon
  cannot place warns on the status line and re-fetches, discarding the local
  text with no diff and no retry. The editor deliberately has no conflict UI
  (`REVIEW-AGENT.md`), so decide the smallest honest surface: keep the refused
  text visible, or name the group that moved. *A refusal the user cannot
  inspect loses work.*
- **Multi-mutator coverage is thin.** The human-write path is pinned one test
  per branch (forward span/base, dirty-pane skip, claim overlap), but nothing
  drives a second attached human, or a human and a fast agent on one buffer,
  so the interaction the feature exists for is untested end to end. *The
  multi-writer case is the reason the feature exists.*
- **`Guard.Patch` still refuses a joined human.** The wave admitted a durable
  human to `Guard.Apply` but left `Patch` agent-only because a snapshot is an
  agent tool. Revisit only if a human path ever wants patch; no test drives a
  human patch today. *A refusal should be a decision, not an oversight.*
- **A TCP attach client does not read back its granted kind, so it forwards edits that can only be proposals.** `hello` downgrades a human request over anything but the unix socket to an agent (`internal/control/control.go`), but the client still forwards its local edits as the person; `host.Apply` admits the agent text as a proposal, so an edit the client believes it accepted lands pending for review. Read the granted kind back from the hello reply (the participant row for the client author) and keep the client read-only when it is not `KindHuman`, or label the forward as a proposal. *A client must not offer an edit the daemon will not land.*
- **The LSP servers are not re-rooted on attach.** `adoptVisibleRoots` rebuilds the explorer, search and picker over the daemon set, but `newServers` is only called from the constructor (`internal/app/app.go`), so a client whose daemon primary differs starts servers against a workspace it is not showing. Decide whether adoption re-resolves the servers or LSP stays launch-rooted by design. *A client should not offer a workspace it does not render.*
- **An attached client runs its own language servers, and nothing is mirrored (2026-09-29).** `cmd/raj/main.go` skips only the eager warm-up in attach mode (`if !opts.Attach { a.WarmServers() }`); a server still starts lazily when the client's idle tick calls `servers.for_` (`internal/app/lsp.go`), which spawns from the client's own table, roots and PATH. None of the ~25 `a.attach` branches in `internal/app` gate LSP, so the client's diagnostics and inlay stores are filled by its own server and the client wire carries neither. Consequences: a client with no server on PATH draws no marks while the daemon has a full set, and a clean buffer is read from the client's disk (`syncDirtyPane` opens only dirty ones) - which on a remote or phone client is not the daemon's. Fix shape: the daemon owns LSP for attached clients - push its diagnostics and hints over the watch and stop calling `for_` when attached. That is also the only route to a comparable client-frame rebuild (client-mirror plan): today a frame whose client drew LSP marks answers `NOT COMPARABLE` with the reason `client-owned language server`, and moving ownership turns that into a real comparison as a side effect. No stamp compares across the two processes regardless - document versions are per sync connection and the server publishes without one, dated only by a process-local arrival sequence. Open: whether `adoptVisibleRoots` hands the client the daemon's absolute paths, so a same-host client's server reads the same disk. *A client should not compute what the daemon already knows.*

### UI, terminals and rough edges

- **Phone profile: audit the remainder.** The `--phone` profile is largely in
  place (`docs/MOBILE-REVIEW-SPEC.md`); reconcile the spec against the shipped
  profile and file what is actually left.
- **The document-highlight backgrounds keep the syntax foreground, so a
  comment on a highlighted occurrence can vanish (2026-09-22).**
  `HighlightRead` (`ui.Ansi(236)`) and `HighlightWrite` (`ui.Ansi(54)`) are
  applied with `style.On(bg)`, which preserves the token foreground; the
  comment grey (`ui.Ansi(8)`) all but disappears on the read grey just as it
  did on the proposal green. The `ui.LegibleOn(bg)` pairing the tints now use
  would fix it, but a highlight that recolours the token is a different look
  from one that emphasises it, and the two highlight colours might change
  instead — decide. *A mark that hides the token it marks is not an emphasis.*
- **The legible tint foreground drops syntax colour on accepted agent text (2026-09-22, user).** The proposed/agent tints now force `ui.LegibleOn(bg)`; the review green reads well, but accepted agent text loses syntax colour and reads worse. Decide: keep it on both, gate the override to comment-class tokens, or choose a tint foreground that stays legible. *The proposal fix should not make accepted text uglier.*
- The smoke suite is Linux and macOS only, and only Linux is proven.
- Smoke scenarios wait on 400 ms of wall clock; a control-socket "queue
  drained?" answer would replace the sleeps.
- Tabs re-anchor at each wrap point (self-consistent, looks slightly off).
- Every OSC raj sends is swallowed under tmux; DCS passthrough belongs in one
  writer wrapper, not at each call site.
- Profile switching in iTerm2 is not clean; deferred, what is there works.
- `cmd+shift+r` to reopen closed tabs, only if Ghostty actually binds it (check
  `+list-keybinds` first).

### Tests and workflow

- **The control fake `ping`/`buffers` replies drift from the shipped `Dispatch` (2026-09-22).** `fakeEditor.run` hand-writes the reply literals, so when `Dispatch` learned `Roots` the fake did not and `TestRootsClearsOnSetLessReply` failed the host gate. Build the fake `ping`/`buffers` replies from one helper (or the same reply constructor) so a new header field cannot land in `Dispatch` alone. *A fixture hand-copied from the wire drifts from it.*
- **`TestStopAllStopsEveryRoot` does not assert its two servers are distinct.** A fixture that reused one server for both roots would still pass (both `live` lookups return the same pointer, `stopAll` stops it, both `Start` calls return `ErrClosed`), so the test pins "the servers we saw are stopped", not "every root was stopped". Assert `live[0] != live[1]` and `len(h.servers.byID) == 2` before stopping. *A regression test that a reused server passes is half a test.*

- **Pin the first Draw's erase.** `TestResizeInvalidates` now drives a real size
  change through `FakeHost.SetSize`, and `TestLayoutChangeRepaints` the
  same-size toggle, but nothing asserts the very first frame invalidates; the
  size-aware guard makes that frame clear, and a test should pin it.
- Test the active-target naming path (`targetName`/`activePath`) with an
  `active` fake buffer.
- The invalidation mapping (`host.Groups`/`host.Diff`) has no app-layer test.
- A coordinate-convention guard so a 1-based/0-based assertion fails where it is
  written.
- A format-on-save ordering regression test through the wake path.
- `TestGuardRefusesSymlinkEscape`'s text/open/apply assertions are vacuous;
  make them reach the check or drop them.
- **The settings pane's click hit test is read-verified only.**
  `settingsPane.ClickAt`'s `dy-1` matches `Render`'s `y+1+i`, but no test
  clicks a row; add one for a row's activation (and that the heading does
  nothing), plus the `w < 8 || h < 2` and height clamp.
- The `.git` restore skip (`restoreLog`) has no test that an already-existing
  log for a `.git` path is ignored rather than replayed; seed a log with
  unsaved ops so the assertion fails without the `isGitPath` guard.
- `markInvalid` adds an O(ops²) journal pass to `Groups()`; measure before
  optimising.
- A superseded warning names the first intersecting run, not the bounding span
  (escalated, not decided).
- A heartbeat tick can enqueue a contentless frame after the final (benign,
  stale-id frames are ignored); decide whether to accept it or sequence the
  final against the tick.
- The container `raj` can lag the editor; rework the release/rebuild step or
  make an empty `srcVersion` detectable. Named again 2026-09-24: `raj ctl git`
  was unknown in-container after the `git` verb landed in source (the image has
  since been rebuilt). Named again 2026-09-28: `raj hook run --param`/RAJ_PARAM
  was absent in-container during the declared-parameters review, so the changed
  CLI could not be driven, and `raj ctl exec` is refused over TCP so the
  host-built binary is not a fallback. Hit again
  2026-09-28 by `intent diff` (the between-wave review could not drive the new
  surface).
- **`scripts/call-runs.mjs` cannot read the opencode V2 store (2026-09-28).**
  It queries the V1 `part`/`message` tables ("no such table: part"); the store
  is the V2 `session_message`/`session_v2` schema, so the review contract's
  per-run tool-use metrics cannot be produced by the documented command. Point
  it at `session_message.data` or pin the opencode version.
- The standing between-wave reconciler needs a thin wrapper so the pass is
  invoked rather than remembered.
- A saved buffer can still carry proposed sets after a rebuild and restart;
  needs a reproduction.
- A buffer that is entirely another author's pending proposal was uneditable;
  the overlap/reconciliation case.
- New `raj ctl` against an old server warns falsely on `--include` (the shipped
  skew warning does not suppress the zero-`Considered` message).
- Subagent transcripts feed the efficiency loop (mine task outputs for wasted
  tool calls and brief-quality patterns).
- **`TestHookRunDetachedReturnsAndLogs` is load-flaky (2026-09-27).** It
  asserts a detached hook run returns within 1s wall clock; on a loaded machine
  it measured 1.183s and failed (the user hit this). Assert the property - the
  caller was not blocked on the command - rather than a wall-clock budget.

### Agent feedback — actionable (context in docs/dev/AGENT-FEEDBACK.md)

- **`TestClientViewReadsLegacyClosedPaths` is vacuous (2026-09-20).** A failed
  legacy parse and a legacy mark both re-add the same daemon tab, so the test
  passes without the `closedMarks.UnmarshalJSON` legacy branch it names; seed a
  case where the two diverge. *A test that passes for the bug it guards is not a
  test.*

- Name the search hit's offsets so a row cannot be mistaken for a byte range:
  `line` is a line number while `line_start`/`line_end` are byte offsets; the
  `SearchMatch` doc comment also says `ByteStart..ByteEnd` bound the match
  "within" the line while the fields are file offsets and `text` is the whole
  line, so the comment contradicts its own fields.
- **`read --json`'s `line_end` is a line number, not a byte end (2026-09-23).**
  A whole-line span reports `line_end` as the last line while the returned
  `text` includes that line's trailing newline, so a driver that treats
  `line_end` as a byte offset lands before the newline; the two units are not
  named distinctly. State the unit, or echo a one-past byte end. (Reported by
  the 1.1/1.2 sessions; reproduced here: `lines` counts a trailing empty line,
  so `--lines <lines>,<lines>` reads `""`.)
- **`diff` accepts one path, so a multi-file review is N calls (2026-09-23).**
  Take several paths or `--all` over the pending sets, as `read A B C` and
  `lsp diagnostics --all` now do, so a wave's review is one round trip.
- **No deterministic seam drives `hover()`/`signatureHelp()` (2026-09-23).**
  Only the pointer tooltip is drivable, so the request-time anchor capture in
  `hover()` (the `DispPos(head)` line in `internal/app/lsp.go`) has no test that
  does not need a live language server; `park`/`applyAnswer` install an answer
  but never drive the request. Add a seam as the inlay and completion paths have.
- State or fix the scope split between `groups` (one buffer) and `proposals`
  (whole workspace), so `groups --mine` with no focused buffer does not read as
  "no sets".
- **`NativeHost` never closes its `events` channel (2026-09-21).** The `Host`
  interface says the channel closes when the host shuts down, and `FakeHost`/
  `HeadlessHost` close it, but `NativeHost.Close` does not, so a `range` over
  `Events()` would hang and the contract comment is false. Close it, or correct
  the comment and say why the terminal host differs. *A channel contract only
  two of three hosts honour is not a contract.*
- **`daemon.log` grows without bound (2026-09-21).** `Runner.logWriter`
  (`internal/daemon/daemon.go`) opens the workspace log append-only and never
  closes or rotates it; `logTail` scoping fixed the wrong-run output, not the
  growth. Cap or rotate it, or state why an append-only log is acceptable.
- **`edit --old ... --all` is an unbounded substring replace (2026-09-21).**
  It replaces every occurrence, including one inside a larger identifier — a
  rename of `.root` to `.primaryRoot()` would also rewrite
  `snapshotSearcher.root` and `servers.root` — so it can corrupt a different
  identifier silently. Warn or refuse when an occurrence is flanked by
  identifier bytes, or make `--word` the default with `--all`. *An identifier
  rename is not a substring replacement.*
- **The skill says `search` is case-sensitive by default; the editor is not
  (2026-09-21).** `raj ctl search -q PRIMARYROOT` matches `primaryRoot` with no
  flag and `--case` is what makes it case-sensitive, so the skill text
  "literal and case-sensitive unless `--regex` or `--case`" is backwards.
  Correct the skill (or the default) so a case-sensitive search is what a
  driver gets.
- **Find's smart-case fold can shift byte offsets (medium).** `hasUpper`
  only sees ASCII `A-Z`, so any all-lowercase query takes the `strings.ToLower`
  branch; Go's simple fold is not byte-length-preserving for runes such as
  `İ` (U+0130, 2 bytes → `i`, 1) or `ẞ` (U+1E9E, 3 → `ß`, 2). The haystack is
  folded too, so a document containing such a rune before a match shifts every
  later `matches` offset, and `replaceCurrent`/`replaceAll` now edit at those
  offsets. Fold ASCII-only or track each match's byte length.
- **Review mode over-refuses cmd+enter in the find bar.** `WouldEdit` returns
  true for `keys.LineBelow` even when the replace row is hidden and
  `Find.Handle` would ignore it; the gate should be `return f.replaceShown`,
  so the read-only note matches what would happen.
- **`raj ctl` run from outside the workspace root mistranslates paths.**
  `inferMapper` roots the local side at `WorkspaceRoot(cwd)`, so a driver run
  from `/tmp` and handed `/work/...` is refused (wave 2, 2026-09-17);
  `RAJ_ROOT_MAP` is the workaround. Make root inference cwd-independent or
  state the run-from-root requirement.
- `exec` in a program cannot set a working directory (no `OpDir`).
- The hover-anchor cross-cutting test is blocked on a fake-LSP-server seam.
- Two encoding-classifier edge cases: `ff fe 00 00` checked as UTF-32LE before
  the UTF-16LE BOM, and unmarked UTF-16 with no NUL decoding as Latin-1;
  detect-and-refuse or document.
- Line/col in `apply`/`edit` replies needs a wire and coordinate decision; the
  reply names the new version but not the new change-set id, so a driver must
  call `groups`/`proposals` to reject, clear or `goto` the set it just applied.
- Make the write target explicit (a mandatory path or `--active`/`--here`).
- Consolidate `read`/`version`/`dump` and collapse the write verbs onto one
  door.
- Raw-LSP passthrough: decide whether it earns its surface, then file it or drop
  it.
- Define or drop the Stream A/B/C/D labels; the user's definitions are needed.
- No `ping`/health-check CLI verb (the protocol op exists).
- `help`/`-h` are reachable but absent from the usage list.
- `whoami --as X` printed a fresh anon id once; confirm before trusting `whoami`
  as the bind check.
- **`--as` must follow the verb.** `raj ctl --as KEY search` fails with `unknown
  command "--as"` and prints the whole usage, so a driver that puts the identity
  flag first is stuck; accept it before the verb too, or state the placement in
  the usage text.

- **`search`'s regex-metachar hint misleads on a literal miss.** The hint
  (`internal/control/cli.go` `hasRegexMeta`) fires whenever a zero-match
  literal pattern contains regex syntax and says "retry with --regex"; when the
  literal really is absent, `--regex` is the wrong fix. It also exits nonzero as
  if the pattern were refused, so a caller cannot tell a literal absence from a
  rejected query (`search -q 'zzq[unlikely'` prints the hint and exits 1).
  Reword to offer both readings (pass `--regex` if you meant a regex; otherwise
  the literal matched nothing) and keep the nonzero exit for "no matches" only.
- **`edit --old` that matches nothing gives no nearest-line hint (2026-09-22).**
  The refusal says the text does not appear and to copy it exactly; it does not
  say where the closest match is, so a block off by one line costs a re-read to
  locate the seam. Name the nearest line, or the longest matching prefix, in the
  refusal. *A failed match should show where the text diverges.*
- **`search --include` and `--path` are two doors with different rules
  (2026-09-22).** `--include` is a comma-separated glob list (a bare `cli.go`
  matches by basename, two files) while `--path` is documented as a directory
  but also accepts a file and searches just it. The usage hint says to use
  `--include` or `--path` without saying which is for one file. Document the
  split, or make `--path` accept a file explicitly and say so.

- **`search`'s per-file cap is not configurable and `capped` stays false
  (2026-09-24).** A file with more matches than the per-file limit reports the
  limit (`"truncated": [{path, shown, total}]` plus a stderr note) while
  `"capped": false`, so a caller that reads `capped` alone reads a cut file as
  complete; two wave agents were misled. Add `--max-per-file N`, or make
  `capped` true when `truncated` is non-empty. *A per-file cap is not the
  global one.*
- **A numeric positional path on `read --at` reads as a missing buffer
  (2026-09-24).** `read --at PATH=LO,HI 380,580` refuses with `no open buffer
  for that path: 380,580` rather than naming the span malformed; the flag's own
  refusal is good, so the positional path should recognise a bare `LO,HI`.
  (Reproduced against the running editor.)
- **A bulk decision line names only a per-buffer group id (2026-09-24).**
  `decideAll` prints `accepted change set 3`; under `--all --everywhere` the id
  is per-buffer, so two buffers' set 3 are indistinguishable in the output.
  Include the path in the line.
- **The sanctioned scratch dir `/tmp/opencode` is not writable
  (2026-09-24).** It is `root:root 0755` in the container (probed: `touch`
  denied) while `/tmp` works, so a subagent following the skill's
  already-created, pre-approved path stalls. Fix the image ownership, or have
  the skill name `/tmp` as the fallback. *A pre-approved path that is
  unwritable is worse than no path named.*

- **The opencode plugin computes the task but does not inject `--task`
  (2026-09-24).** `plugins/raj-gate.ts` reads `taskOf(sessionID)` for the
  ledger; putting `--task <session>` on the `raj ctl register` call (or setting
  it per session) would populate `Group.Task` without an agent remembering to.
  Decide whether the plugin should, given it deliberately stopped injecting
  identity.
- **The repo `skills/raj-editor/SKILL.md` lags the new agent surfaces
  (2026-09-24).** It documents `register --as` but not `register --task`,
  `accept`/`reject --all` but not `--all --everywhere`, and none of the
  file-lifecycle verbs (`mkdir`/`delete`/`deletions`/`rename`/`rmdir`/
  `rmdirs`); its `clear` text still describes the invalid-only workspace scope.
  Sync the repo copy (and the installed copy) with the live usage.
- **An agent cannot amend its own pending change set (2026-09-24).** The only
  route is `reject --all --mine`, `clear --all --mine`, re-read the version and
  re-apply; a "replace my own set" verb (or `apply --replace`) would make
  re-drafting one gesture. Promoted from the Wave 4 feedback block.
- **`apply --hunks` is impractical for a large Go block (2026-09-24).** A
  function or whole-file move is one JSONL hunk of kilobytes of JSON-escaped
  Go, so a driver falls back to `edit --old-file` or `dump`/`patch`; make
  `--hunks` accept a raw body, or point the briefs at `dump`/`patch` for a
  structural rewrite.

### Flag usage printing


- **A custom `flag.Value` prints the placeholder `value` in `-h` (2026-09-22).**
  `-q` and `--at` became `flag.Value`s, and `flag.UnquoteUsage` names an
  unknown Value type `value`, so `raj ctl read -h` says `--at value` and
  `search -h` says `-q value` where the hand-written usage says `PATH=LO,HI`
  and `PATTERN`. (A one-letter name already prints with one dash, so the old
  `--q` note is retired.) Put a backquoted placeholder in the usage string,
  or special-case `PrintFlagUsage`, and assert it.
- **Backquoted words in a usage string leak into the placeholder.**
  `flag.UnquoteUsage` treats a backquoted word as the value name, so
  `raj ctl apply -h` prints `--group groups` and `--dump dump` where the value
  is a uint; the same mechanism renders `--daemon daemon run` in the editor
  help. Drop the backquotes or accept the rendering.
- **`PrintFlagUsage` has no panic guard on a default text.** `flag.isZeroValue`
  wraps the zero `String()` call in `recover`; `flagDefaultText` does not, so a
  custom `flag.Value` that panics on a zero receiver would panic the help
  path. Latent: no custom `Value` is registered today. Mirror the stdlib
  recover, or state why not.

### Claim surface

- **`claim` with a bare path silently replaces the working set.** `claim` with
  operands replaces the set unless `--add` is given (documented), so an agent
  that claims a later path loses the earlier ones and its writes are refused
  until it re-claims; both a subagent and this review pass paid a retry for it.
  Warn when a replace would drop a non-empty set, or make `--add` the default
  with an explicit `--replace`. Escalated 2026-09-20.

### Daemon list and labels

- **`restartCLI`'s label carry-over has no test.** The carry-versus-override
  decision lives in the CLI function, which has no `Ops`/`Runner` seam and can
  only be driven by a real spawn, so `restart --workspace` and the label
  carried across a plain `restart` are unpinned; extract the decision into a
  pure helper (as `resolveTarget` is) and test it. *A carried label is a
  decision; pin it.*

## Direction (documented, not scheduled)

- **An explicit tab width now overrides a file's detected indentation
  (escalated, 2026-09-17).** Previously `--tab` was only a fallback; an explicit
  width (flag or stored `tab_width`) now pins both the display advance and the
  indent unit and survives Reload re-detection, while a default launch still
  lets detection win. Confirm this is the wanted semantics, or revert the pin
  to a fallback. Do not decide from the review.
- **The settings key set is open; the pane's default scope is settled
  (2026-09-18).** The settings pane (`internal/app/settings_pane.go`) is built
  and writes to the workspace scope by default, with a scope row to switch a
  change to the user scope; the resolver still knows exactly `tab_width`/
  `tabs`/`wrap`/`auto_pairs`/`inlay_hints`/`save_check` and leaves an unknown key alone
  (`SetSetting` refuses one), so a newer build can add a setting without an
  older one misreading it. The remaining question is the key set, and the LSP
  section the pane sketch reserves a place for.
- **LSP server choice should be configurable.** The language-to-process table
  (`internal/app/lsp.go` `command`) is hardcoded, so a `ty`/`pyright` user gets
  `pylsp` and an unlisted server is unreachable; this also answers "only servers
  that run with no configuration are listed". Lightest shape: per-language
  candidate argv lists, then a per-workspace config, then a settings pane.
- **Tree-sitter is the decided direction for the syntactic layer.** Auto-indent,
  symbol navigation and string/comment classification move there; semantic
  queries stay on LSP. Grammar management is its own milestone before the
  feature.
- **Inlay type hints are deliberately out of scope.** Drawing text that is not
  in the document perturbs column maths, caret positioning and wrapping — a
  renderer project, not an LSP one.
- **Conflict navigation over git diffs** (not scheduled): navigation in terms of
  git diffs, with resolution staying native — no work trees or branch hackery.
- **`edit --base` does not reject a stale base; it ignores it (2026-09-23).**
  `edit` re-reads the buffer and bases the apply on the version it just read, so
  a second edit deliberately based on v2 applies cleanly on top of v3; `--base`
  is accepted and never consulted. `apply` refuses a stale base and rebases
  hunks, so the read-gate the docs promise holds for `apply` but not for `edit`.
  Decide: honour `--base` in `edit` (refuse or rebase a stale one) or drop the
  flag from `edit`'s surface, rather than letting a caller believe the base was
  checked. Under discussion with the user; not scheduled.

- **East Asian Ambiguous arrow width is terminal-dependent (escalated,
  2026-09-18).** `RuneWidth` now counts `←`/`→`/`↔` two cells, confirmed only
  on the terminal that drifted (Ghostty). A terminal that draws Ambiguous arrows
  one cell wide would regress the same caret by one column per arrow in the
  other direction. Confirm the intended terminals, or gate the exception on a
  terminal capability.
- **Should a Rejected deletion-only set also be leased? (escalated,
  2026-09-18).** `deletionLeases` leases only Proposed gaps, so a Rejected
  deletion's restored bytes have no caret-side lease even though `Leased`
  already refuses edits inside Rejected insertions. The wave left `Rejected`
  behavior unchanged deliberately; decide whether a rejection deserves the same
  gap lease.

## Deliberately not doing

- Horizontal scrolling while wrapped: there is nothing off to the right to
  scroll to, so `Viewport.Left` is pinned at 0 when wrapping is on.
- The container build/run stays the external `bldraj`/`oc` shell functions, not
  folded into the binary as `raj box build`/`raj box run`.
- `exec` over TCP is refused by design — the command would run on the editor's
  machine, outside the driver's container.

## New-file flows are hostile to a human (2026-09-28)

Found while landing the H3 leaves; all three hit in one sitting.

- **A new file is invisible until it is saved.** A buffer proposed for a path
  that does not exist on disk appears in `buffers` but in no tree or explorer
  view, so a human cannot find the thing they are being asked to review. The
  explorer should list pending-new buffers, or `status` should mark them.
- **Save applies to the active buffer only, and there is no save-all.** Across
  a batch review the human saves whichever tab is active; the others stay
  accepted-but-unsaved and look identical to work still in progress. A save-all
  gesture, or a status line naming how many buffers hold accepted-unsaved
  changes, would close it.

- **A proposed removal never surfaced in the phone client (2026-09-28).**
  `raj ctl delete scratch-l7-ui.md` proposed a removal; the phone client
  showed no trace of it, so the human had no way to see or approve it there -
  and the file would have ridden into a public commit. If a client is
  deliberately read-only, the refusal has to be legible (a note, or the
  removals list in the action drawer, or in the status bar); a proposal that
  silently never appears is worse than a gesture that is refused. Decide
  which it is and make one of them true. The same question applies to
  `rmdir` and to any future approval that is not a save.

## Between-wave review — `intent diff` follow-ups (2026-09-28)

Found by the review pass on the `raj ctl intent diff` wave; none blocks the
wave, which stands.

- **A truncated `intent diff` is not pinned through `--json`.** `capIntentDiff`
  is unit-tested and `truncated`/`omitted_bytes` are `omitempty`, so the two
  forms are distinguishable, but nothing asserts the JSON keys; the new
  `printIntent` test pins the printed note only. Add a marshal assertion.

## Between-wave review — shellcheck/gates + publish artifact (2026-09-29)

Found by the review pass on the two waves that made `make check` green and
moved publish onto the exported artifact; neither blocks the waves, which stand.

- **`docs/ARCHITECTURE.md` §6 still lists a resolved disagreement.** Its bullet
  ("The `publish-single.sh` header describes building its own commit from the
  working tree on disk; `intent/land.go` says 'publish only pushes a commit that
  already exists'. Neither says which commit ships when both apply.") is no
  longer true: the header now pushes the artifact (the export's commit) and the
  working-tree build is gone. Remove the bullet. (This file belongs to another
  writer; filed here rather than edited.)
- **The publish PR target is auto-detected from `@{upstream}` with no fallback
  (2026-09-29).** `publish-single.sh` takes `base_branch` from the current
  branch's upstream; the machine path always passes `--base` as a raw SHA, so
  the `--base <branch-name>` fallback never fires and a checkout with no
  upstream leaves it empty, making `gh pr create --base ""` fail (exit 9) after
  the branch is already pushed. Escalated, not decided: derive the target from
  the remote default (`refs/remotes/<remote>/HEAD`), carry the export's base
  *ref name* (not only its SHA) through `intent.Publish`, or refuse before the
  push with a named diagnostic — but do not hardcode `main`. See
  `docs/dev/AGENT-FEEDBACK.md`.
- **`Publish.BaseRef` is now a copy of `BaseSHA`.** `proposePublish` sets
  `BaseRef: rec.BaseSHA, BaseSHA: rec.BaseSHA`, so the JSON `base_ref` field
  carries a SHA and the only reader (`CheckPins`' `CheckBranch`) sees the
  base-equality rule become a no-op. Collapse the two fields (drop `base_ref`)
  or carry the export's base ref name. (Low.)

## Quality gates — duplication and cyclomatic complexity (2026-09-29, user)

- **Complexity and duplication analysers: the gate half is left.** The `make
  cyclo`, `make dupl` and `make quality` targets exist (`Makefile:143-189`,
  checked 2026-10-01). Left: measure the tree, set the thresholds, then put
  them in `check`; and the hook rows, which are the owner's to add. Whether
  rows exist was not checked (the reviewer cannot list hooks).

## Restart and CI follow-ups (2026-09-30)

Found while briefing the runner image. That image has since been dropped
(`harness/` holds no `Dockerfile.runner`, checked 2026-10-01); these outlived it.

- **A new-path proposal is lost or invisible after a restart.** With the
  journal off (`RAJ_JOURNAL` unset, the default) a restart drops pending sets
  outright. With it on, the sets survive but `proposals` does not enumerate a
  journal-only path and `read` refuses it ("no open buffer for that path")
  until some other action loads the path; then the journal restores every set,
  so recovery depends on someone happening to touch the path and an agent
  cannot find its own reviewed work. Seen with the X1 runner Dockerfile
  (groups 1-3 restored, then superseded by group 4). Loss by default,
  discoverability with the journal; related to "A new file is invisible until
  it is saved" above.
- **`scripts/raj-cycle.sh` still allows the dropped `runner` image
  (2026-10-01).** Its header comment (line 27) and its allow-list (lines
  273-274) name `runner`, but the image's Dockerfile is gone, so naming it in
  `RAJ_CYCLE_IMAGES` would pass the check and then fail the build. Remove it
  from both. This replaces two older items here (the cycle-image list in
  `docs/HOOKS-SPEC.md` and the `RAJ_CYCLE_IMAGES` default), which were about
  adding the image and are moot.
- **A reveal can be delivered twice on a generation race (2026-09-25, low).**
  `connection.watch` (`internal/control/control.go:2291-2298`) reads the
  generation once before the buffer list (the 2026-10-01 fix), but still calls
  `revealsSince(req.Gen)` afterwards, so a `PublishReveal` landing between the
  two rides this reply and the next. The effect is idempotent (caret and
  focus). Fix: have `revealsSince` return the generation it snapshotted under
  the same lock and send that one.

## Between-wave review — review-tabs strand 3.5 (`intent next`) (2026-09-30)

Found by the review pass on strand 3.5; the wave stands (check run 162 exit 0).

- **The hook run-stamp `dirty` is the workspace status digest, not the projected
  tree the run built (2026-09-30).** The final frame (`HookDirty`) and the "ran
  against the projected tree ... (dirty ...)" line print
  `Provenance.DirtyDigest`, and for a projected run that is
  `git status --porcelain --untracked-files=all` of the *workspace root*
  (`materialiseWith`, internal/control/materialise.go; `Service.StatusDigest`,
  internal/git/git.go) — not the projection the run verified. On a clean
  workspace it is `e3b0c442...` (sha256 of the empty string), so a projected
  run that verified a projection carrying proposals reads as "nothing was
  verified". Evidence: check run 161 reported `HEAD 702a0aaa... dirty
  e3b0c442...` while verifying a projection with proposals; run 162, the same
  HEAD and saved tree, reported `dirty 61dcd448...` once the workspace had
  acquired unrelated dirt, which a projection digest could not do. Either make
  the field reflect the projection the run built, or rename/document it (for
  example `workspace-dirty`) so it cannot be read as the verified content's
  state. (Low, but misleading at the gate.)

  **Decided 2026-09-30 (user):** rename the commit field to `base-commit-sha`
  (the value `HEAD` carries today) and drop `dirty` entirely; the workspace
  digest pins nothing a run needs. Add `intention-sha` — a hash over the seam
  base commit plus its members — but only for seam-scoped runs (today `intent
  prove`), so a proof names the reviewed slice and the publish step can cite it.
  The larger half: `intent prove` does not go through the hook runner at all
  (`runCheckHook`, internal/app/seam.go, execs the check hook argv directly), so
  a proof has no receipt today and needs one first. No hook script changes: the
  stamp is printed by internal/control/control.go, and no `*.sh` reads it.

## Folded from landed design docs (2026-10-01)

Open remainders read against the tree before their source documents are
archived. Proposed by the reviewer; nothing here is scheduled.

- **Lease checks rerun the projection on every edit (from
  `docs/F3B-II-DESIGN.md` §3, D2).** The design moves lease enforcement to the
  `File.Begin()`/`End()` change-set boundary, reading a cached projection. That
  did not land: `Pane.leaseBlocks` (`internal/editor/pane.go:844`) and
  `File.Insert`/`Delete` (`internal/editor/file.go:696`, `:710`) each call
  `File.Leased`, and `Session.Leased` (`internal/piecetable/project.go`) calls
  `s.Project(Annotated)` on every call once the session has decisions. Check
  once per user action at the change-set boundary against the cached
  projection; keep a cheap guard in `File.Insert`/`Delete`. Not measured.
- **The projection fuzz does not cover wrap (from `docs/F3B-II-DESIGN.md` §3,
  "D1 fuzz oracle").** `FuzzProjectionInvariants`
  (`internal/view/projection_test.go:547`) drives random sessions and
  segmentations through `checkProjectionInvariants`. The design also asks for
  wrap on and off and a real journal of proposed sets with random accept and
  reject; the file has no mention of wrap. The other D1 leftovers landed:
  `Pane.DispPos`, `Pane.DocAt`, `Pane.DisplayLines`
  (`internal/editor/pane.go:404`, `:425`, `:288`) and
  `File.DecisionGeneration` (`internal/editor/file.go:886`).
- **How long must the journal retain old versions? (from
  `docs/HARNESS-BROKER-AGENT.md`, "Open questions").** `ApplyDiff` rebases
  from an arbitrary base, so the journal has to keep everything back to the
  oldest version any agent still holds. Compaction has to answer it. No entry
  in `docs/INVESTIGATIONS.md` covers it (searched 2026-10-01: "retention",
  "retain", "arbitrary base").

- **Fold summary (from `docs/F3B-II-DESIGN.md` §4).** Is `⋯ rejected · N bytes ⋯`
  the right marker, or should it preview the first hidden line, name the author,
  or say `N hidden lines`? Should an `Invalid` fold (phase 1c) read differently?
- **Search across hidden text (from `docs/F3B-II-DESIGN.md` §4).** `search` runs
  over the buffer view (`Search.Buffers` snapshots `File.Text()`), so it returns
  hits inside rejected/hidden runs. Skip them, or return them labelled with
  their state (spec §6 says label, not silently return)? Which composition
  should a whole-workspace search index?
- **Selection across a fold (from `docs/F3B-II-DESIGN.md` §4).** A selection
  whose anchor is before a fold and head after it spans hidden bytes. Does
  copy/cut include them or exclude them, and is a delete across a fold refused
  by the lease (it should be)? May a drag endpoint rest on a fold row?
- **Mouse on a fold row (from `docs/F3B-II-DESIGN.md` §4).** A click today lands
  at the hidden run's session start. Should it instead do nothing, or
  reveal/decide the set (for example, jump to Review)? What do drag and
  autoscroll past a fold do?

## Review pass — verb surface and doc drift (2026-10-01)

Raw findings in `docs/dev/AGENT-FEEDBACK.md`, "2026-10-01 — between-wave review".

- **Confirm and fix `raj ctl edit` targeting the wrong buffer.** A wave
  reported `edit` applying against the focused buffer instead of its named
  path. Four review probes resolved the named path correctly, so capture the
  exact invocation first; if real it is a silent wrong-file write.
- **`claim` state is keyed by author id, which two live connections can
  share.** A parallel connection's `claim` replaced this one's set mid-run.
  Make the claim set per-connection, or refuse a second live owner of an author
  id, so a co-tenant cannot silently drop claims. Related to the existing
  "claim with a bare path silently replaces the working set".
- **Re-read the archived design docs for open remainders that were not
  folded.** `HARNESS-BROKER-AGENT.md` questions 1 and 4, and ATTACH-DESIGN's
  phone-input/review-console analysis, are not in TODO or COMPLETED; the
  archived sources retain them.

## Folded from landed spec remainders (2026-10-01)

Open remainders read out of CURSOR-VIEWPORT-SPEC, FILE-LIFECYCLE-SPEC and
CLAIM-SPEC before those specs were archived (2026-10-01). Proposed by the
review pass; nothing here is scheduled. Directory rename and `run --prog`
reachability for the file-lifecycle verbs are already Later items above, and the
CLAIM-SPEC §3 spec-vs-code contradiction is the "Claim surface" item above.

- **A redo can split a rune (from `docs/archive/CURSOR-VIEWPORT-SPEC.md`).**
  Reproduce with `specSeeds` at 400: wrap=false, seed 245, step 51. The spec
  pointed at TODO for this but no item existed.
- **Mouse positioning is unspecified (from
  `docs/archive/CURSOR-VIEWPORT-SPEC.md`).** No mouse exists yet; decide when
  one lands.
- **The viewport rule for an agent edit that lands off screen is undecided
  (from `docs/archive/CURSOR-VIEWPORT-SPEC.md`).** A keystroke follows the
  cursor; an agent hunk should probably not yank the view. Nothing decides it.
- **File-lifecycle leftovers (from `docs/archive/FILE-LIFECYCLE-SPEC.md`
  §9/§11).** Per-entry (partial) directory removal; a permanent dismiss/reject
  answer (v1 is Ignore only); whether a driver may accept/reject over the
  socket; and where pending deletions surface (`who`, the status line, or both).
- **Claim-set durability across a restart (from
  `docs/archive/CLAIM-SPEC.md` §7).** Persist the per-identity claim set through
  the op log / `Registry.Seed`, with an expiry/invalidation decision; today it
  is in-memory and resets.
- **Is a socket `save` gated? (from `docs/archive/CLAIM-SPEC.md` §10).** The
  recommended treatment is recorded (not gated: it writes the accepted
  composition and is the user's gesture), but it was never made an explicit
  decision.

## Security audit — control plane (2026-10-02)

Read-only audit at HEAD `c79329eb` by `raj-claude`; nothing was built or run
(no Go toolchain, no source mount). Only the `git --rev` item was confirmed
live; the rest are read from the code. Detail and the list of what was not
read: `docs/dev/AGENT-FEEDBACK.md`, "Code audit — control plane (2026-10-02)".
Plan: `docs/dev/WAVE-PLAN.md`, Track A. Proposed by the auditor; nothing here
is scheduled.

- **A crafted frame crashes the editor before the token check (critical).**
  `Frame.Split` tests `off+n > len(f.Body)` (`internal/control/wire.go:485`),
  which overflows for a hunk length near 2^63 and then slices out of range.
  `DecodeRequest` runs before `authorised`, on a bare `go s.serve`
  (`internal/control/control.go:1710`), so the process dies without restoring
  the terminal. Compare `n > len(f.Body)-off`, and run `serve` under
  `safe.Go` or a per-connection recover. *A length field is input.*
- **A TCP `hello` naming identity `local` becomes the local human (critical).**
  The registry seeds the human row as `local`
  (`internal/control/participant.go:166`) and `join` returns the existing row
  for a matching identity (`:215`); the kind downgrade in `serve` applies only
  to a new row. Author 1 then passes `humanAuthor` for save, accept, land and
  removal approval. Refuse reserved identities and refuse joining a human row
  from TCP.
- **The `git` verb passes `--rev` to git as an option (high, confirmed).**
  `Diff`, `Show` and `NumStat` put the revision before `--`
  (`internal/git/git.go:404`, `:426`, `:440`); `raj ctl git show --rev
  '--version'` answered `fatal: unrecognized argument: --version`. With
  `--output=<path>` a diff is written to any file the user can write. Refuse a
  revision that starts with `-`, or pass `--end-of-options`.
- **`rename` and `mkdir` change the disk at once, `.git` included (high).**
  `Guard.Rename` (`internal/control/host.go:715`) needs only a self-granted
  claim and `host.Rename` is an `os.Rename`; nothing excludes `.git`, so an
  executable script can be moved to `.git/hooks/pre-commit`. Make rename a
  proposal like delete, and refuse `.git` as a destination.
- **`reload` and `close --discard` have no author gate (high).**
  `Guard.Reload` (`internal/control/host.go:1479`) and `Guard.Discard`
  (`:1023`) check the path only, so any agent can drop the user's unsaved
  text and other writers' pending sets; discard also removes the journal.
  Gate both on the human, or refuse a buffer holding text the caller did not
  write.
- **`intent prove` and `intent land` are open to agents (high).**
  `dispatchIntent` (`internal/control/host.go:1876`) gates only
  `publish --approve`. `prove` runs the `check` argv outside admission — the
  enabled flag, the agent flag, `hook off`, the timeout and the log
  (`internal/app/seam.go:89`) — and `land` commits a task's sets, proposed
  ones included, and moves `raj/baseline` without the human gate `raj ctl
  land` has. Route prove through the hook runner and gate the `land` mode.
  Extends "`intent prove` runs the check hook's argv directly" above.
- **A checkout's `.raj/state.db` is adopted on first open (high).**
  `migrateState` (`internal/app/session.go:263`) moves the legacy database
  into the state directory when none exists there, which is every fresh
  clone, so a repository can ship hook rows marked agent-callable. Ask before
  adopting, or drop the `hooks` rows on migration.
- **A dead writer wedges the connection (medium).** The writer goroutine
  returns on the first `WriteFrame` error (`internal/control/control.go:1753`)
  and stops draining; a handler blocked in `c.out <-` (`:1986`) never returns,
  so `wg.Wait` hangs, the participant stays connected and a hook run keeps its
  gate. A reply over 64 MiB triggers it every time. Keep draining after the
  error, or select on `done` in `send`.
- **Author ids can be exhausted without a token (medium).** `reserveAuthor`
  runs at accept (`internal/control/control.go:1722`), there is no read
  deadline, and `ReadFrame` allocates up to 64 MiB before the token check
  (`internal/control/wire.go:530`). About 250 idle connections refuse every
  new client and force recycling of disconnected writers' rows. Reserve after
  the first authorised frame and set a handshake deadline.
- **A stale pid file can kill an unrelated process group (medium).** An
  adopted run is matched by pid alone (`internal/control/hookruns.go:280`) and
  killed with `kill(-pid, SIGKILL)` on cancel or a passed deadline (`:313`,
  `:356`); `daemon stop` sends SIGTERM the same way
  (`internal/daemon/daemon.go:486`). Record and compare the process start
  time.
- **An identity is a label, not a credential (medium).** Any token holder can
  `hello` as another agent's key, read its mail, write as it and withdraw its
  proposals. Same family as "`claim` state is keyed by author id" above.
  Decide whether a key is bound to its first connection or carries a secret.
- **The `git` verb runs on the event thread with no timeout (medium).**
  `dispatchGit` (`internal/control/host.go:2841`) uses `context.Background()`,
  so a large diff, or `log` with no count, stalls the editor.
- **A request that timed out still runs (medium).** `submit` answers "timed
  out waiting for the editor" after 5 s (`internal/control/control.go:3183`)
  but the queued request executes later, so a client is told an apply failed
  that then lands. Drop a timed-out request at `Take`, or say "still queued".
- **Small ones.** Hook string parameters match unanchored
  (`internal/hooks/hooks.go:610`), so `[a-z]+` accepts `abc; rm`; `accept` on
  an unknown group id answers OK (`internal/app/control.go:1945`); `cmd` in
  `runHookDetached` is read by the cancel closure without a lock
  (`internal/control/hookruns.go:459`); `Materialise` skips symlinks, so a
  projected tree differs from the worktree; the socket falls back to
  `/tmp/raj-<uid>` without checking who owns the directory
  (`internal/control/control.go:1541`); the LSP reader runs outside
  `safe.Recover` (`internal/lsp/conn.go:147`).

## Feedback sweep leftovers (2026-10-02)

Found by reading every section of `docs/dev/AGENT-FEEDBACK.md` against this
file on 2026-10-02. Each was reported there and had no item here. Not
re-verified against the tree unless a file and line is given; re-check before
briefing.

- **Clearing a superseded overlapping set can drop the surviving set's hunks
  (2026-09-30, high).** On `internal/app/app.go` a 17-hunk keeper reconciled
  to 3 after fourteen byte-identical duplicates were rejected and cleared; the
  saved file lost the overlapping hunks. Until fixed, diff the keeper against
  disk after any disposal. *A cleared set must not take hunks out of the set
  that survives it.*
- **A projected run does not see pending deletions or renames (2026-09-25).**
  `Project` composes buffer text only, so a `delete`/`rmdir` proposal is
  absent from the scratch tree a hook or `exec --projected` verifies. Decide
  whether the projection takes a removal input.
- **`diff`'s `old` side is not one base for a multi-op set (2026-09-17).**
  Hunks of one set showed `old` blocks from different versions, so a rewrite
  cannot be judged from `diff`. Decide what `old` means when a set's members
  were recorded at different versions.
- **cmd+s on a seam review tab offers to create `.raj-seam/<seam>/`
  (2026-09-30).** `savePane` does not consult `readOnly`, so the save reaches
  `ensureParent` before `ErrReadOnly` refuses. Short-circuit a read-only pane.
- **The waiting count can disagree with the waiting list (2026-09-30).**
  `waitingCount()` sums `Pending()`, which drops invalid sets, while
  `Proposals()` lists them. Count the invalid Proposed sets too, or say why
  not.
- **A forwarded human edit lands over an agent's proposal (2026-09-22,
  decision).** An attached human gets the agent's advisory lease, while the
  local keyboard is refused by `EditLeased`. Decide which rule an attached
  human takes.
- **`intent next --dry-run` reports a branch for a commit it did not write
  (2026-09-30, low).** Refuse `--dry-run` for `next`, or omit the branch when
  the commit is empty.
- **The own-draft refusal names "another writer" when every overlapping set is
  the caller's (2026-09-23, low).** The wording at
  `internal/control/cli.go:3129` assumes a peer.
- **A whole-line paste is chosen by a trailing newline (2026-09-17,
  decision).** A characterwise selection that ends at a line boundary pastes
  linewise. Carry a `Linewise` flag on `Clip`, or keep the suffix rule.
- **Settings pane loose ends (2026-09-18, low).** Closing it with escape does
  not `TouchSession()`; the wrap chord updates `WrapDefault` but not
  `a.settings.Wrap`; `settingOrigins` ignores a launch flag; a failed settings
  read is silent.
- **Weak tests named by reviews and never filed.**
  `TestReviewGenerationStableWhenIdle` passes by chance half the time
  (2026-09-20); `TestStateKeyMatchesSessionStateDir` is a tautology
  (2026-09-21); `TestPingRootsEmptyWithoutARoot` also passes with the wiring
  missing (2026-09-22); `TestClientLocalEditForwardsOneApply` checks only the
  last apply (2026-09-22); `TestAttachWorkspace` does not assert the ambiguity
  text (2026-09-22); no test pins that a reconnect does not replay reveals
  (2026-09-25); `projectUnapply`, the benchmark prototype, does not fold the
  overlap separator rule its doc claims (2026-09-26);
  `scripts/raj-cycle.test.sh` has no test for the image allow-list
  (2026-09-30).
