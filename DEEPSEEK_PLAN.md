# DeepSeek Plan

Working plan derived from TODO.md, captured before implementation. Horizons are
ordered by value, risk, and dependency.

## Horizon 1 — small, fully specified, low risk, do now

- [x] **Search include/exclude globs match the path, not the basename (Stream B).**
  Already implemented and tested — no change needed. Both call sites pass the
  relative path from `filepath.Rel(root, path)` (the walk filter at
  search.go:219 and the open-doc eligibility check at search.go:321), and
  `TestRunGlobsMatchPath` verifies path-scoped globs (`sub/*.go`) while
  `*.go` still reaches subdirectories via the `plainExt` fast path. Verified
  live: `-include 'internal/search/*.go'` matches, `-include 'search.go'`
  matches nothing.
- [x] **Scroll restore as a ratio (Stream C).** Implemented: `session.Tab`
  gains `Ratio float64` (`json:"ratio,omitempty"`); the save side records
  `Top/Lines()` (`scrollRatio` in app/session.go) while still writing `Top`
  for legacy compat; restore uses `Ratio` when > 0, computed against the
  just-opened buffer and clamped to its last line (the first-resize clamp
  bounds it too). `validate` keeps clamping plain `Top` for legacy JSON.
  Tests: `TestRatioRoundTrip`, `TestLegacyJSONWithoutRatio` (session);
  `TestScrollRestoresSameSize`, `...ProportionallyWhenFileGrows`,
  `...ClampedWhenFileShrinks`, `...EmptyFile`, `...IndependentOfTerminalSize`,
  `...LegacyJSONByTop` (app). Host-side verification pending:
  `go test ./internal/session/ ./internal/app/`.
- [x] **TODO reconciliation.** The two shipped items (`search -json` byte
      offsets, `read -start/-end`) are folded into TODO.md (struck, with
      verification notes). Still open: the Stream A/B/C/D index — the labels
      are used throughout but never defined; it needs the user's definitions
      to write.

## Horizon 2 — the agent-driver socket surface

Items I can drive and test end-to-end over the socket. They remove the need to
reach past the buffer into host shell tools.

- [ ] Response-header JSON -> flat-record conversion, verb by verb. (Needs a definition — see Session feedback; the wire header is already opcodes.)
- [x] **`-jsonl` NDJSON search mode** — one JSON object per line per hit, as
      it arrives; the whole-buffer emit is gated off in jsonl mode and the
      no-hits exit-1 is preserved. (Implemented by a raj subagent.)
- [x] **`groups -mine`** — a `-mine` flag filters the listing to the
      connection's own author id, before both the JSON and text paths.
      (Implemented by a raj subagent.)
- [x] **`version -json` returns byte/line counts** — closes the "learn a
      document's length without a read" gap; the `stats [path]` alternative
      is then unnecessary. `Response`/`Header` gain `Bytes`/`Lines`, carried
      as `hBytes`/`hLines` on the wire; Dispatch fills them from `Buffers()`
      (no interface change); the CLI emits them in `-json` mode, plain mode
      unchanged. Covered by `TestEveryHeaderFieldRoundTrips` +
      `TestHeaderRoundTrip` (wire) and `TestDispatchVerbs` (response).
      Host-side verification pending: `go test ./internal/control/`.
- [x] **`read -lines A,B`** — the `text` op accepts 1-based inclusive line
      numbers (`hLineStart`/`hLineEnd` on the wire); the host translates to
      bytes via its own index, so a driver never re-implements the byte
      model. Covers `Request`/`Header`/encode/decode, Dispatch, the real
      host, `memHost`, the CLI flag, and `TestDispatchReadsByLines`.
- [x] **Hunk echo in `edit`/`apply -json`** — the reply now carries `spans`
      with `start`/`end`, the matched `-old` text (edit) and the written
      replacement (both), so a driver verifies the splice from the reply
      instead of re-reading.
- [x] **`dump` / `patch` — editor-side scratch, no local files.**
      `raj ctl dump <path> [-start -end]` returns a named, hashed snapshot
      `{id, version, text}`; `raj ctl patch <path> -dump <id> -text-file -`
      takes the whole edited text back, and the editor computes the old→new
      diff itself, rebases, and applies. The agent never computes offsets or
      matches text — it returns whole text. Kills the `-old` prefix traps,
      the heredoc-newline gotchas and the offset arithmetic; the hash makes
      drift detectable (`patch` states when the buffer moved past the
      snapshot). Snapshots are per-author and evicted on restart; span-scoped
      dumps keep it to the structural chunk being changed. Shares the rebase
      walk with the `diff` verb; `find` op and hunk-echo compose with it.
- [x] **`lsp` verb family over the socket.** Let a driver ask the editor's
      existing LSP client directly: `raj ctl lsp hover <path> <line:col>`,
      `lsp definition`, `lsp completion`, `lsp diagnostics <path>`. The seams
      already exist — `search.Docs` is the didChange snapshot,
      `Session.Version()` is the document version, position mapping is fuzzed,
      and `openFromPicker` already consumes a definition's path+position.
      Shape: hover/definition/completion are blocking request/response;
      diagnostics return the last-known cached state (the pane's
      worker-parks-result pattern), never block on the server. One verb means
      the eight-layer treatment (opcode table, prog.go, wire.go, header.go,
      control.go, host.go, app/control.go, cli.go, plus memHost in
      host_test.go). "No server for this file type" is a clean error, not a
      hang. (Complement to the Reading-A shell/LSP front-end; that front-end
      becomes a *client* of this.)

## Horizon 3 — strategic, do after the above

- [ ] **Position fuzzing for ranges** as its own milestone. Unlocks incremental
      sync and completion `textEdit`.
- [ ] **Incremental document sync** — biggest correctness/performance payoff,
      blocked on the fuzzing.
- [ ] **Completion `textEdit` honoring** — needs the range-coordinate decision
      first.
- [ ] **Layered-proposals remainder** (group ranges, in-editor accept/reject,
      diff-style rendering) — share the range-rebase walk; one effort, and
      pairs with the accept/reject + picker bindings.

## Stretch — `diff` verb, git as a second mode

- [ ] **Core `raj ctl diff [path]` (no git).** Pending change sets as old→new
  text — the review surface "A hunk lands unverified" and "Groups have no
  ranges" ask for. Pure editor state: group spans plus the rebase walk.
  Testable end-to-end over the socket from the container.
- [ ] **`raj ctl diff -vs HEAD [path]` (git, stretch).** The accepted
  composition vs git HEAD, distinct from the pending-set view. Depends on the
  range-rebase walk, deferred deletions (a deleted span leaves no piece), and
  the base: raj runs read-only `git show HEAD:<path>` on the host (editor has
  the filesystem; no `checkout`/`apply` — far smaller surface than `exec`),
  then diffs internally. The programmatic twin of the change gutter versus git
  HEAD and of diff-style rendering; one rebase walk feeds all three.
  Container cannot test the git half (no shared filesystem).

## Deliberately deferred (decisions, not backlog)

- LSP inlay type hints — renderer project, perturb column/caret/wrap maths.
- Socket two-encoding coexistence — needs an explicit decision.
- **[Decision] Tree-sitter for the syntactic layer, not a per-language table.**
  Auto-indent, symbol navigation and string/comment classification move to
  tree-sitter (in-process, synchronous, deterministic — never a missing server),
  while semantic queries (hover / definition / completion / diagnostics) stay on
  LSP. The grammar-management question — one compiled grammar per language, and
  a C or WASM dependency in a terminal editor — is answered as its own milestone
  before the feature, not slipped in behind another. Replaces the old
  "per-language auto-indent table" deferred item.

## Session feedback (gaps found while implementing)

Written by the agent that implemented `dump`/`patch` and the `lsp` family this
session. These are gaps and decisions, not code — the user decides and answers.

1. **The running `raj` predates the source.** The editor reached over TCP is
   older than the working tree: its `raj ctl` has no `hello`, no `read -lines`,
   no `-jsonl`, no `version -json` byte/line counts, and none of the new verbs
   (`dump`, `patch`, `lsp`). No socket verb in this plan is verifiable until the
   binary is rebuilt. All edits landed as proposals in buffers; nothing is on
   disk.

2. **No Go toolchain in the container**, no shared filesystem, `exec` refused
   over TCP. The host-side contract stands: `gofmt -w && go test
   ./internal/control/ ./internal/app/ ./internal/prog/` is the only
   verification. State this every session.

3. **`hello` is not a `raj ctl` command.** Identity binding from the CLI goes
   through `who` (which sends the hello op); there is no direct
   `raj ctl hello -as X -name Y`. Add it, or document `who -as X -name Y` as the
   identity verb.

4. **`dump`/`patch` and `lsp` shipped as described**, with these decisions made
   where the plan was silent (review): `lsp` ships its result as a JSON string
   header field rather than per-field opcodes (a small retreat from the
   "no JSON in the header" rule, taken because the result is text by
   construction); `dump` hash is informational and drift detection is delegated
   to `ApplyDiff` conflict reporting; snapshots are a plain in-memory map keyed
   by a monotonic id, per-author by construction.

5. **The flat-record item (Horizon 2, top) needs the user's definition** before
   it is worth implementing — see the note on that line.

6. **Stream A/B/C/D index still undefined.** The three Horizon-1 items each cite
   their stream, but no file defines what A, B, C and D are. Needs the user.

