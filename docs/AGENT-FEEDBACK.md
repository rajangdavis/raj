# Agent feedback — raw agent notes

Raw, dated feedback from raj agents who drove the editor: tool-usage reviews,
friction notes and field reports. This is the evidence record, moved here from
TODO.md with its dates and internal structure intact; actionable items are
tracked in TODO.md and completed ones are archived in COMPLETED.md. Bullets
whose issue is already tracked there have been pruned, so what remains is the
session narrative, observations and friction with no other home.

## Notes from driving `raj ctl` as an agent

One session's worth of friction, recorded where the next driver will find it.
Each item is cheap relative to what it cost to work around.

- **Container ergonomics: no shared filesystem means stale-file checks are
  manual.** `exec` is correctly refused over TCP, but then nothing warns the
  driver that the bytes it is about to compile in its own sandbox do not
  match the buffers. `buffers -json` answers it with a second round trip;
  what is missing is the prompt to make one. A client-side flag (`exec
  -warn-stale`, refusing locally if any buffer is dirty) would move the
  check into the driver's own sandbox where it belongs.
- **An editor restart orphans the driver's read-before-write state.** The
  rebuilt editor kept the buffers open but reset every version to 0, so a
  driver holding offsets from before the restart has coordinates against a
  document that no longer exists. That is correct — anything else would be
  worse — but nothing says so. A connection-scoped "editor restarted" marker
  (a generation counter on `whoami`, bumped per listen) would let a driver
  bin its cached versions instead of discovering staleness one refused
  apply at a time.
- **No Go toolchain in the container, so the host verifies.** No shared
  filesystem and `exec` refused over TCP: agent work lands as proposals in
  buffers, and the contract is that the user accepts and saves, then
  `gofmt -w && go test ./... && make check` runs on the host. State it every
  session.
- **The Stream A/B/C/D labels are used but never defined.** The globs fix is
  Stream B, scroll-ratio restore is Stream C, the driver-round additions ride
  Stream D, and no file says what the streams are. Needs definitions from the
  user to write.
- **`-as` is refused before the verb and accepted after it.** "unknown
  command" in one position, silently fine in the other, and the usage text
  shows neither — first flag after the verb is the only placement that
  works. Also observed: `whoami -as X` printed a fresh anon id rather than
  the bound one. Unreproduced since; treat it as a suspected bug and
  confirm before trusting `whoami` as the bind check.
- **Ten exploratory commands burned twelve author ids.** 26/256 used
  mid-session, 55/256 by session end, almost all of them dead anons. That
  is the live validation for server-minted identity, and the evidence for
  recycling `gone` ids — the open decision on server-minted identity in
  TODO.md.
- **An empty `/work` in the container is expected, and `ls` is the trap.**
  The mount point is empty inside the container while the editor's root
  maps to the host repo, so the reflexive sanity check reads exactly like
  "the files are gone". The check that works is `raj ctl buffers`; the
  container filesystem was never the truth.

Notes from the Wave 1 reconstruction session (2026-09-10), after the
accidental restart wiped the unsaved proposals and they were re-applied
piece by piece:

- **Two raj processes squatted one TCP port and the driver talked to the
  other one.** An accidental editor restart left PID A bound to
  `127.0.0.1:7391` and PID B to `*:7391`; the container reaches the host
  via `host.docker.internal` (non-loopback), so only the wildcard bind was
  reachable, and that process held the connection open while speaking
  nothing. Every verb hung, with no refusal. The token was right all along
  — the port was wrong. `lsof -iTCP:7391 -sTCP:LISTEN` on the host was the
  only way to see it. Both failure modes want a real answer: the still-open
  "one listener, not both" item in TODO.md, and a client-side connect timeout
  that names "the port answers but no control frame came back" as the
  wrong-process case rather than hanging.
- **A restarted editor orphans in-flight proposals, and the recovery is in
  the driver, not the editor.** The orchestrator re-applied Wave 1 hunk by
  hunk from its own task history after the restart. That worked because the
  subagents had reported complete function-level designs. It is still the
  user hand-off gap the dirty-buffer-journal item names; the session notes
  here just add that a clean `raj ctl buffers` read (all `saved`) is what
  confirmed the proposals were really gone rather than merely unaccepted.
- **`lsp diagnostics` as a per-hunk smoke check worked.** The per-hunk loop
  settled into: apply → `lsp diagnostics` → seam re-read → `goto` + focus
  the tab → user review → cmd+s. The diagnostics call caught nothing this
  session, but it occupied the slot where a malformed hunk would have shown
  (the one bad hunk in Wave 1 was a bad test fixture, which diagnostics
  cannot see). Cheap enough to keep in the skill's loop.
- **The `braceTally` fixture failures were bad fixtures, two rounds.** A
  raw-string case and an unbalanced `{ [(] }` both failed; ambiguity about
  which constructs the Go lexer unambiguously classifies means the durable
  fixture is double-quoted strings plus comments, nothing fancier. The
  lesson to keep: when a tally is string/comment-aware, its tests will be
  only as stable as the lexer's classification of the constructs chosen.
- **`exec` refused over TCP still costs the verify loop.** Wave 1 needed
  `go test ./internal/control -run TestBraceTally -v` on the host twice;
  the agent could not run it, and each round was a user hand-off. That is
  the intentionally-recorded trade (the refusal is correct), but the
  per-hunk-loop above now has to lean on `lsp diagnostics` and claim that
  semantic checks move to the user. Correction to an earlier draft of this
  note: there is no `ping` verb (`raj ctl ping` is "unknown command") — ping
  exists only as a protocol op the test harness sends. If a health-check
  verb is wanted, that is a (cheap) gap, not an omission from usage.
- **Color/readability on proposals is a user-reported niggle.** A
  screenshot review of a proposal hunk prompted "the colors are a bit
  difficult to read" — recorded here rather than guessed at, since the tint
  palette is the user's terminal theme and any fix is a renderer design
  question (which palette index proposals use, or a high-contrast mode),
  not a Wave 1 item.
- **`braceTally` counts markdown fences as code.** `buffers -json` on
  TODO.md reports nonzero parens/brackets because unclosed `(`/`[` in prose
  are counted — correct for a string/comment-aware tally that has no
  markdown-fence class, but a false positive the reader has to dismiss. If
  it ever becomes the `lint` verb, "is this file really source" is a
  per-language question it currently cannot answer.

Agent tool-usage review, wave reports 2026-09-10/11 (filed per the new
RECURSIVE-RAJ §8 step — deduped against the notes above; "none reported"
waves were W2a and most of W2b, meaning the surface held):

- **Bash heredoc/`"` nesting in `edit -new` arguments is fragile.** 3c
  reported a chained command that quoted poorly and had to be split into
  separate guarded edits. Partially a discipline note (already in the
  skill: single-quote args, use -old-file/-new-file for big blocks), but a
  `-new-file` path is the documented escape and subagents reached for it
  only after a failure — briefs should name it up front for multi-line
  payloads.

Agent tool-usage review, wave reports 2026-09-10 (second plan instance, waves
0/1/3 — the sync wave reported the surface held; new items below):

- **Truncated tool output spills to a dead-end path** (harness-side, not raj):
  the spill file under /home/oc/.local is outside raj's root and unreadable by
  a subagent. Driver guidance: keep searches narrow.
- **One garbled line on a long `read -lines 1,330`** — unreproduced on re-read
  of the exact span; suspected display-side one-off, recorded in case it
  repeats.

Agent tool-usage review, wave reports 2026-09-10/11 (this session's swarm —
review-flow chords, relative paths, search honesty, and a read-only save-lag
diagnosis; deduped against the notes above):

- **Concurrent writers share the version stream, and rebasing handles it.**
  Two subagents edited `control.go` concurrently; a hunk applied with a stale
  `-base` rebased onto the other writer transparently. Worth knowing, not a
  defect.
- **A stale root `/work/KEYBINDINGS.md` duplicates the tested
  `docs/KEYBINDINGS.md`.** No test reads it; delete it.

## Agent-efficiency review — the walk and the ctl surface (2026-09-11)

Two reviewers read the D1/D2 and H1/H2/H3 subagent reports and the walks they
implied; a follow-up swarm implemented the cheap half. Every actionable item
from this review is tracked in TODO.md and COMPLETED.md, so the bullets are not
repeated here. "Walk discipline" now lives in RECURSIVE-RAJ §5 (anchor →
locate → owning file → enclosing block → seam).

## Agent tool-usage review, 2026-09-11 (inlay hints, fallback)

The inlay-hints rewrite was the substantive work of this session; the defects
it surfaced (line-index corruption, the `search` `ByteStart`/`LineStart`
mismatch, cross-buffer diagnostics staleness, the missing raw-LSP verb, the
`/tmp/opencode` permissions) and the remaining inlay-hints steps are all
tracked in TODO.md and COMPLETED.md.

## Agent tool-usage review, 2026-09-11 (wave A — control surface)

The three fixes (line-index corruption, `reject -all` newest-first, the
`search` line-start offset) are in tree and tracked under TODO.md's `[~]`
items and COMPLETED.md. One friction item is not tracked anywhere:

- **`apply`/`edit` report a size, not the resulting line.** "goto each
  hunk" costs a second `search`; a line offset in the reply would make it
  mechanical.

## File lifecycle verbs — create, delete, rename (agent feedback, 2026-09-13)

Superseded 2026-09-13/14: the requested verbs landed — `mkdir`,
`delete`/`delete -withdraw`/`deletions` (a review primitive with a prompt gate
and `RAJ_TRASH`), `rename`/`mv` for files, and `rmdir`/`rmdirs` — and the open
decisions resolved (parent directories via `mkdir`; recoverable delete via
`.raj/trash/`; immediate, claim-gated verbs that refuse a dirty buffer or one
holding proposals). Directory rename remains open in `docs/TODO.md`
("File lifecycle — remaining"). Design: `docs/FILE-LIFECYCLE-SPEC.md`,
`docs/CLAIM-SPEC.md`.

## Reconciler notes, 2026-09-15 (revert + control transport)

A reconciler read the `revert`/transport batch after it landed green. Verified
clean: `noteDecision` never drops a set's decision (decisions move only in
`Session.RevertAuthor`, behind `hasLiveMembers`), `host.Revert`'s wedge
bookkeeping is complementary to `File.sync`, the `token` wire code is unique,
`Header.Token` genuinely carries both directions, and `listenUnix` had no
references. The findings below are raw; the actionable ones are in TODO.md.

- **`RevertAuthor`'s false return was ambiguous.** `Session.RevertAuthor` can
  drop the newest of an author's sets and then wedge on an older one, returning
  `(false, Block{})` — indistinguishable from "nothing live to reverse", because
  `blockFor(0)` yields the zero `Block` and a clamped inverse reports `at == 0`.
  The first cut only re-synced when `block.Group != 0`, so that path left the
  index behind the journal and the decision generation stale. Now guarded by a
  journal-version comparison (`internal/editor/file.go`).
- **Both revert coverage gaps are closed (2026-09-15).**
  `TestRevertAuthorPartialWedgeLeavesNewerSetReversed` and
  `TestRevertAuthorKeepsASharedGroupsDecision` (`internal/piecetable/groups_test.go`)
  cover the partial wedge and the shared-group decision. No `internal/editor`
  `File.RevertAuthor` test was added: the only extra guard it could reach, the
  unnamed-block version clause, needs an op whose bad offset the public API
  clamps away, so the piecetable test's version assertion pins the fact the
  guard consumes rather than the guard itself.
- **Only `revert` compares the author to the connection — the docs used to
  overstate.** The serve loop enforces a mismatch only for `revert`; `patch`,
  `delete -withdraw` and `rmdir -withdraw` compare the named author to a *stored
  record's owner* instead, and every verb trusts the field for attribution. So a
  hand-built frame can name a peer, be attributed as them, and on the
  claim-gated writes act inside their claim set. The CLI never sends a foreign
  id. `Header.Author`/`Request.Author` and the TODO item now say this exactly
  instead of "only revert refuses".
- **`TestEveryVerbHasACode`'s list is hand-kept and still incomplete.** It omits
  `dump`, `patch`, `lsp`, `lspprep`, `send`, `who`, `reload`, `goto` and
  `close`, so its comment that it "fails when someone adds a verb" only holds if
  someone also edits the list. `token` was added alongside its new code.
- **Cursor offsets are not remapped across reverted bytes.** `File.sync` moves
  the line index and `Cursors.Normalize` sorts and merges, but nothing slides a
  caret across removed text, so a cursor past EOF survives a revert. This
  matches the older `Clear`/`Decide` handlers, so it is pre-existing, not new.
- **A container restart wipes `/tmp`.** Staged patch files for the second
  cleanup pass vanished when the image was rebuilt and the container recreated;
  the orchestrator rebuilt them from task history. Cheap and recoverable, but
  worth knowing before staging anything long-lived under `/tmp`.
- **The reconciler checked identifiers, not arity, and the host caught it.**
  A new test wrote `!s.RevertAuthor(Agent)` — a two-value call in a
  single-value context — and `go vet` stopped the build. Confirming that every
  identifier exists is not the same as checking each call's result count; treat
  multi-return functions as an explicit reconciler checkpoint.

## Agent verb-surface audit — `raj ctl` (2026-09-13)

Read-only audit of the running editor (author 3; root `/work` → `/Users/rajandavis/Desktop/projects/raj`), 2026-09-13.
Sources: `docs/AGENT-FEEDBACK.md`, TODO's "Agent feedback — actionable", `raj ctl -h` plus per-verb `-h`, and `internal/control/{cli,host,control}.go`, `internal/app/control.go`. No code changed.

### Inventory

- **Inspect/size**: `buffers`, `read`, `version`, `dump`, `search`, `lsp`, `whoami`, `who`, `list`, `stats`
- **Write**: `apply`, `edit`, `patch`, `run -prog`, `save`
- **Lifecycle/present**: `open` (`-create`), `close`, `goto`
- **Review/decide**: `groups`, `diff`, `review`, `accept`, `reject`, `clear`
- **Talk/run**: `recv`, `exec`

Protocol-only ops with no CLI verb: `ping` (used by `whoami`), `text` (the `read` op), `execcheck`, `snapshot`, `lspprep`, `prog`, `hello`, `cancel`. `help`/`-h` are handled but absent from the usage list.

### Friction → root cause

1. **Path spelling is not all translated (path resolution).** Relative, `/work`-absolute and editor-spelled paths all resolve *inbound* — `read` verified for all three — and `buffers`/`groups` come back `/work`-spelled. But `diff -json`'s `path`, `search -json`'s `truncated[].Path`, and every error string keep `/Users/...`. Cause: `Client.localise` (client.go) rewrites a fixed field list (`Root`, `Buffers`, `Matches`, `Dirty`, `Groups`) and misses nested `DiffJSON`, `Truncated` and `Err`. One seam, applied unevenly. README claims "what `search` prints is something the agent can open" — `truncated` already contradicts it. (Fixed 2026-09-15: `localise`
now covers the nested `DiffJSON`, `Truncated` and `LSPJSON` shapes and `Err`
text.)
2. **The default target is silent (defaults/discovery).** `apply`/`edit`/`read`/`save`/`close` take an optional positional; omit it and the verb targets whatever tab is active. Verified live: `edit -old <absent>` with no path read the active buffer, and the refusal says "the buffer" without naming it. `-h` prints only the shared FlagSet, so no verb's positional is shown.
3. **Verb redundancy.** `read`/`version`/`dump` overlap; `apply`/`edit`/`patch`/`run` are four write doors; `open -create` is the only create and "create" is not a verb; `groups`/`diff`/`review` list the same sets three ways; `accept`/`reject`/`clear` are three decisions over one state machine.
4. **Lease and error recovery.** Writing over **another writer's** proposal refuses with "change set N owns this text; accept or reject it first"; recovery is reject → `clear` → re-apply. Writing over your **own** `Proposed` set now amends it instead (2026-09-13). `clear` exists but is documented only on its usage line. The session's reported `accepted 0 ops` artifact did not reproduce (the string is absent from the tree).
5. **Output shape — fixed 2026-09-13.** `search -json`, `stats -json` and `run -json` now carry snake_case json tags (`path`/`byte_start`/`line_start`, `runs`/`stale`/`agent_only`), matching `read`/`buffers`/`groups`/`diff`/`version`/`whoami`/`apply`/`lsp` and `search -jsonl`. The contract is snake_case throughout.
6. **Lifecycle gaps.** Create is only `open -create` (no parent-dir handling); no `delete`, no `rename` — those still hand work back to the host.
7. **No region lease.** `apply` rejects a stale hunk after the fact; nothing lets two writers claim a span up front (`claim`/`watch` still unimplemented).

### Ranked simplifications

| # | Change | Removes | Friction | Blast | Safe now? |
|---|---|---|---|---|---|
| 1 | Extend `localise` to `DiffJSON`, `Truncated`, `Err` (or emit caller-relative paths host-side) | `/Users` vs `/work` mismatches | high | low | yes |
| 2 | Per-verb usage showing positionals and the default target; name the buffer in default-target refusals | silent wrong-buffer edits | high | low | yes |
| 3 | One `-json` field contract (snake_case); make `search`/`stats`/`run` match; document it | per-verb parsing special cases | med | low | yes |
| 4 | Document `clear` (reject → clear → re-apply) and `open -create` as create | undiscoverable recovery and creation | med | low | yes |
| 5 | Add resulting line/col to `apply`/`edit` replies | a follow-up `search` to goto each hunk | low | low | yes |
| 6 | Require an explicit target (`-active`/`-here`) or mandatory path for write verbs | the silent-default footgun for good | high | med | needs pass |
| 7 | Consolidate `read`/`version`/`dump`; one write door over `apply`/`edit`/`patch`/`run` | near-duplicate verbs | med | high | needs pass |
| 8 | `create [path]` with `MkdirAll`, plus `delete`/`rename` | host hand-off; phantom-tab creation | med | high | needs pass |
| 9 | Region leases (`claim`/`watch`) + overlap reporting | stale-hunk conflicts after the fact | high | high | needs pass |

"Safe now" = docs/defaults/aliases, or a change that does not alter what a verb means. "Needs pass" = semantics or wire; design before code.

### Safe now (no semantics change)

Status 2026-09-15: bullets 1–4 done, bullet 5 deferred. Raw findings and the
wire/design questions in `docs/AGENT-FEEDBACK.md`.

- [done 2026-09-15] Extend `localise` to nested `DiffJSON`/`Truncated` and to
  `Err` text. Those landed before this date; the 2026-09-15 pass also mapped
  nested `LSPJSON` location paths, the one `Response` shape still missed.
- [done 2026-09-15] Per-verb usage text: positionals plus the default target;
  name the buffer in default-target refusals. The usage half was already in
  place; the naming half is `targetName`/`activePath` in `internal/control/cli.go`.
- [done 2026-09-13, verified 2026-09-15] One `-json` field-naming contract; the
  three Go-name verbs move to it. All exported wire/JSON structs carry
  snake_case tags.
- [done 2026-09-15] Document the `clear` recovery path and `open -create` in
  README and the skill. README already had both; the skill gained the
  `open -create` sentence in its open paragraph.
- [reclassified 2026-09-15: needs a design pass] Add the affected line/col to
  `apply`/`edit` replies. A single sparse field cannot carry per-hunk positions,
  and the wire shape plus the resulting-coordinate semantics need a decision.
  See `docs/AGENT-FEEDBACK.md`.

### Needs a design pass

- Line/col on `apply`/`edit` replies (moved here 2026-09-15): per-hunk versus a
  single span, byte column versus editor column, and what a no-op or stale-base
  hunk reports.

- Making the write target explicit (mandatory path or `-active`), which decides the footgun permanently.
- Merging `read`/`version`/`dump` and collapsing the write verbs onto one door.
- `delete`/`rename`/parent-dir create, and whether a file op is immediate like `open` or a review decision like text.
- Region leases (`claim`/`watch`) and overlap reporting.

### Leave alone

- `accept`/`reject`/`clear`: a state machine, not redundancy.
- `apply` (offsets) vs `edit` (quoted string) vs `dump`/`patch` (editor-side diff): three real choices for three situations.
- The read-before-write gate; proposals with the save refusal; `exec` refused over TCP; `run -prog` as the batch door.
- `search` refusing a bare positional; `open` refusing a typo without `-create`.

### Already fixed — do not re-file

- Headless read: `read`/`version`/`lsp diagnostics` load on demand with no tab; `open` now means show (headless-read spec).
- A typo'd `open` is refused unless `-create` (verified live).
- Rebuilt client: `-create` is parsed and forwarded; the "no create verb" report was a stale container binary.
- Registry: gone ids recycled at the 255 cap skipping id 1; `Registry.Seed` keeps attribution across restart (COMPLETED.md).
- Relative/absolute/editor-spelled paths all resolve inbound (`read` verified), and response-side spelling is translated too (`localise` covers nested `DiffJSON`/`Truncated`/`LSPJSON` and `Err` since 2026-09-15).
- Identity: version handshake, server-minted `tok_...`, per-session `RAJ_IDENTITY` absorption by the plugin.
- `reject -all` newest-first, line-index corruption, search `LineStart` — in tree, host verification pending (TODO `[~]`).

### Undocumented / unreachable

- `clear`: reachable and in the usage block, but absent from README, docs and the skill.
- `help`/`-h`: reachable, not listed in usage.
- No CLI verb is unreachable; the protocol-only ops above are internal by design.

## Verifying the safe simplifications, 2026-09-15 (verb audit)


Re-checked the five "Safe now" bullets in the 2026-09-13 verb-surface audit above against the
tree after two of them had landed. The audit is dated; below is what moved, what
changed now, and what is deliberately left open.

Premises that moved since the audit:

- **Pathless writes no longer default to the active tab.** `Guard.claimTarget`
  (`internal/control/host.go`) sends a pathless `apply`/`patch`/`clear`/`revert`
  to the sole claimed file, or refuses with `claim a file first` / `claim set
  has N files; name one`. Friction #2's "apply targets whatever tab is active"
  now holds only for the read-side verbs, and the claim-set refusals already
  name the set.
- **`localise` had grown well past the audit's field list.** `Truncated`,
  `DiffJSON`, `Err` and every flat path list (`Buffers`, `Matches`, `Dirty`,
  `Groups`, `Claims`, `ClaimOverlaps`, `Deletions`, `DirRemovals`, `Proposals`)
  were already mapped. The one `Response` shape still missed was `LSPJSON`: a
  definition/reference answer carried editor-spelled `locations[].path`. Fixed
  2026-09-15 in `internal/control/client.go`, mirroring the `DiffJSON`
  unmarshal/rebase/marshal, with only `Locations` rewritten so hover `text`
  stays content; test added to `TestLocaliseRewritesNestedAndProsePaths`
  (`internal/control/cli_test.go`). `run -prog` payload paths remain unmapped —
  a program carries raw bytes, not a `Response` field — and stay noted in
  TODO.md.
- **`-json` field names were already unified.** Every exported wire/JSON struct
  (`Hunk`, `Group`, `GroupOverlap`, `GroupOverlaps`, `DiffHunk`, `SearchMatch`,
  `TruncatedFile`, `ExecStats`, `DirtyBuffer`, `Buffer`, `Participant`,
  `Message`, the `wire.go` search records) carries lowercase snake_case keys;
  no `json:"CamelCase"` tag remains. Bullet 3 needed no work.

Bullet 2, the naming half: `editTarget` used to say "the buffer the user is
looking at" with no way to name it — the client cannot see which buffer the
server resolved an empty path to, and the comment said so. Added `activePath`
and `targetName` (`internal/control/cli.go`): when a verb was given no path,
the CLI asks `buffers` once and prints the focused buffer's path in the `edit`
refusal, the `goto`/`diff`/`review` outputs, and the `close -discard` note.
`close` resolves the name before the close because closing the active buffer
moves the active tab. It is a message label, never a target: the request still
carries the empty path, so the editor resolves the same buffer it would have.
Only the pathless case pays the extra round trip, and a failed lookup falls
back to the old wording. `decideAll` was left alone: `accept`/`reject` default
to the active buffer but `clear` defaults to the claim set, so one label cannot
name both.

Bullet 5, deferred rather than done:

- **Line/col in `apply`/`edit` replies needs a wire and a coordinate decision.**
  The host already has the primitive — `diffLines` (`internal/app/control.go`)
  turns a hunk span into 1-based `line`/`end_line` for `diff` — and after an
  agent apply the new set is reachable through `Session.DiffPending()` with its
  rebased spans. But a single sparse scalar cannot describe N hunks: `apply
  -hunks` and `edit -all` are both multi-hunk. Carrying a per-hunk list means a
  new header field (the nested-string hatch `DiffJSON`/`StatesJSON` use) plus
  `encodeHeader`/`decodeHeader`, `Response`, the host and the CLI, and it forces
  decisions the audit does not make: is the reported position the hunk start or
  the whole replaced span; is `col` a byte offset or an editor column; what is
  reported for a no-op hunk (`ApplyDiff` skips them, so no set is created) or
  for a hunk that lands against a stale base. Adding the field before answering
  those would freeze a coordinate contract by accident, so this piece stopped
  and is reported instead.

## Documents-consolidation review pass (2026-09-15, between-wave review)

Superseded 2026-09-16: the review proposed deleting the three folded source docs
(`docs/AGENT-VERB-AUDIT.md`, `docs/PROPOSALS-SPEC.md`,
`docs/RECONCILIATION-UX.md`) and the stale root `/work/KEYBINDINGS.md`; all four
were deleted, their `docs/README.md` rows removed, and
`internal/docsindex/docsindex_test.go` now fails a `docs/*.md` with no row or a
row naming a missing file. See `docs/COMPLETED.md` "Docs retirement and the
index guard (2026-09-16)".

## F3b-ii D2a review pass (2026-09-15, between-wave)

Scope: `internal/editor/pane.go` (`UpdateDisplay` memo), `internal/app/mode.go`
(`displayPolicy`), `internal/app/render.go` (`drawEditor` call site), and the
tests `internal/editor/display_test.go` / `internal/app/mode_test.go`. The wave
was accepted and saved; `proposals` and `groups` were empty and the container
has no git checkout, so the wave was enumerated from the brief file list alone
(see the TODO item that raises).

Verified as correct:

- **The memo is sound.** The key is `(Session.Version(),
  DecisionGeneration(), policy)`; `SetDisplay` stores it beside `disp` and
  `dispKeyValid` covers the zero value, so the first call always builds and no
  caller can refresh the projection without its key. A decision moves the
  generation without the version (`File.AcceptGroup`/`RejectGroup` only mark
  state; `noteDecision` bumps `decisionGen`), an edit moves the version, and a
  mode switch moves the policy. `displayBuilds` is asserted in every rebuild
  and every skip test, so a regression trips it.
- **Mode mapping is exhaustive for the two-value enum** (`ModeEdit`,
  `ModeReview`); only Review yields `Annotated`, and no other state reaches the
  `else`.
- **Call-site ordering holds.** `drawEditor` calls `UpdateDisplay` after the
  nil-pane guard and before `fitHints`/`RenderFocused`; the only production
  render path is `App.Draw → drawEditor → RenderFocused`, and the other
  projection readers (`drawDiagnosticMarks`, `drawProposalMarks`,
  `drawCompletion`) run after `drawEditor` in the same frame.
- **No-decisions identity holds**: `view.Build` returns nil iff there are no
  segments, and the identity tests pass unchanged.

Findings:

- **The intermediate mismatch is real and scoped to D2b.** With folds now live
  in Edit mode, `drawDiagnosticMarks`, `drawProposalMarks`,
  `drawCompletion`/`textArea`, `showCompletion`, `proposalsVisible`,
  `cycleProposed`/`reviewPicker`/`reviewRows`/`proposalGroups` plus `jumpTo`,
  `control.go` `Goto`, `inlay.go` `hintLines`/`hintRange` and `session.go`
  restore still compare session coordinates to `Viewport` display rows. A fold
  above a mark shifts it down by the fold hidden line count (or clips it).
  Named precisely in the D2b TODO item; not fixed here per the brief.
- **The display memo was not invalidated when the document was replaced.**
  `Pane.Reload` built a projection, replaced the session, then followed the
  caret through the old map before the next `UpdateDisplay`; `journal.go:563`
  (startup restore) swaps `Pane.File` the same way but before the first frame,
  so only reload was reachable. Fixed in this pass: `Pane.invalidateDisplay`
  resets `disp` and the key, `Pane.Reload` calls it before `FollowCursor`, and
  `TestPaneReloadInvalidatesTheDisplayProjection` covers it. The journal swap
  still relies on happening pre-frame.

- **The `review` agent is not installed in the container.**
  `opencode/agents/review.md` exists in the repo and `plugins/raj-gate.ts`
  allows the type, but `~/.config/opencode/agents/` holds only `raj.md`, so
  `subagent_type: "review"` fails with "Unknown agent type" and this pass ran
  as a `raj` subagent. The installed `skills/raj-review-agent/SKILL.md` is the
  review contract copied without frontmatter, so it does not register as a
  skill either. Actionable item in TODO.md.
- **The review pass has no way to enumerate a saved wave.** `proposals`,
  `groups` and `diff` report nothing once the user accepts and saves, and the
  container has no repository to diff. The brief file list was the only window.
  Actionable item in TODO.md.

## F3b-ii D2b review pass (2026-09-15, between-wave)

The D2b wave moved the app's position consumers onto the display map. The
host `make check` failed on seven tests. The pass below traces six of them to
their fixtures, not to the production move; the seventh is the gutter
placement already fixed in the buffer.

- **The two completion tests could not produce a completion.** With no
  decisions `DispPos(head) == File.LineCol(head)`, so the anchor move could not
  change the result — the failure was that the only buffer word beginning with
  the typed prefix *was the prefix itself* ("foo" at the end of "delta foo"),
  and `complete` deliberately does not offer the word already typed as its own
  completion. `complete.Cache.Rank("foo")` returns `nil` and `Popup.Show`
  closes, which is exactly "no completion was offered". Both fixtures now carry
  "foobar" so "foo" completes to it; the anchor row is still asserted against
  `DispPos`. No production change was needed: `showCompletion`,
  `requestCompletion`, `applyCompletion` and `completionCache.covers` all key
  on the same display line and column, so the cache and the popup cannot
  disagree.
- **`TestHintLinesMapDisplayWindowToSession` counted one row short.** The
  fixture's trailing newline is a real, empty session line and so a real
  display row: `foldedMarkHarness` yields six rows for seven session lines, not
  five, and the test's own comment stopped at the last "ddd" row. The expected
  count is corrected and `hintLines` still returns `(4,5)`.
- **A proposal that cuts a line in half draws on two rows.** The review
  identity test replaced "world" inside "hello world", so the composition is
  `base "hello " | proposed "socket" | base "\n"`; `view.Build`'s
  `emitContent` lays one row *per segment*, so two session lines become three
  rows. That is D1 behaviour D2b exposed, not a D2b regression — `Build` splits
  mid-line folds the same way and its oracle only promises comp is reproduced,
  not one row per line. The review test now uses a whole-line hunk (boundaries
  on the line's start and end) so it exercises the identity it names; the split
  was later fixed: `view.Build` absorbs the continuation run, so it draws one
  row (COMPLETED, 2026-09-16).
- **The journal restore fixture was never written to disk.** `File.Dirty()` is
  content-based and a rejected set is excluded from the agreed composition, so
  a rejected-only buffer reads clean; `flushJournal` then writes no log at all
  and `RestoreSession` has nothing to reopen. The fold was not lost — the log
  never existed. The fixture now leaves a second, still-pending set so the
  buffer is unsaved work; B's `p.SetDisplay(...)` in `restoreLog` is correct and
  is what the test then verifies.
- **`drawDiagnosticMarks` used a display row as a screen `y`.** The pending
  fix to `l.EditorX, top+(row-first)` matches `drawProposalMarks`; checked by
  hand against `gutterCell`'s `TopY+(row-Top)`.
  `TestDiagnosticMarkInsideFoldIsSkipped` was passing vacuously before (the mark
  was painted outside the rows the test scans) and is now a real skip.
- **Deferred seams, still open.** `App.jumpTo` and `reviewJump` are two copies
  of the same map-aware jump; `render.go`'s `sessionTopFor` hover bridge stays
  only because `hover()` still captures session coordinates; and
  `showCompletion` reads the prefix from session bytes because `Pane` exposes
  no row-text accessor. All three are TODO items.

## `ls` + hidden, and the diagnostics false-`ok` review pass (2026-09-16, between-wave)

Live-verified over the socket against the rebuilt editor. The wave is saved and
green; these are the review pass's observed results and the fixes it proposes.

- **`ls` behaves as specced.** `ls /work` marks directories with `/`; `ls
  /work/.raj` is empty while `ls -hidden /work/.raj` lists `logs/`,
  `session.json`, `trash/`, `w4a/`, `w4b/`; `-json` carries
  `{name,path,dir,size}` and omits `size` for a directory; a file and a missing
  path are both refused (`not a directory`, `no such file or directory`).
  `ls -hidden` reaches `.git`; a root `ls` lists `.raj` itself because the
  defaults hide `.raj/*`, not `.raj`. The brief's `.raj-smoke2`/`.raj-smoke3`
  do not exist in this tree, so the equivalent was checked against `.raj` and
  `.git`.
- **`search -hidden` reaches hidden paths.** `search -q 'refs/heads/main'` from
  the root returns nothing; with `-hidden` it returns `.git/HEAD` and
  `.git/logs/*`. Caveat: `search -path .git` already reached them without
  `-hidden`, because a walk's root is never subject to the hidden policy. That
  may be the intended override; it is filed as a decision in TODO.md.
- **A hidden file that `ls` lists can still refuse `read`.** A `.raj/logs/
  archive` log is listed by `ls -hidden` but `read` answers `no open buffer`
  and `search` finds nothing in it: it is the binary sniffer, and `ls` is a
  filesystem listing that does not sniff. Not a defect.
- **The diagnostics fix is real live.** A throwaway buffer at
  `reviewscratch/broken.go` holding a function that returns a string where an
  `int` is expected answered `status: ok` with the type error and its line
  number, not `ok` with an empty list. The scratch proposal was rejected and
  cleared and its tab closed; a `rmdir /work/reviewscratch` was proposed and the user approved it during the pass, so the scratch tree is gone. The specific
  versionless-publish branch is covered by `internal/app/diagnostics_test.go`
  (`TestDiagnosticsRejectsAPreSyncVersionlessPublish`,
  `TestDiagnosticsRejectsAPostSyncVersionlessPublishForAnEditedBuffer`, and the
  clean counterpart), which the live run could not reach: it needs a clean file
  on disk edited in the buffer, and an agent cannot save its own proposal.
- **Diagnostics of an unsaved multi-file edit compose only once every file is
  registered.** `lsp diagnostics internal/app/control.go` first reported
  `s.runSearch undefined` while the `internal/app/ls.go` buffer had not been
  sent to gopls; after `lsp diagnostics internal/app/ls.go` registered it, both
  reported `ok`. That is the buffer overlay working, but it is a driver gotcha:
  when validating a cross-file change before save, ask diagnostics about every
  file in the change, not just the first.
- **The `ls -h` usage note lied.** The per-verb usage emitted "with no path,
  targets the buffer the user is looking at" for every `[path]` verb, but `ls`
  defaults to the workspace root. Fixed in `internal/control/cli.go`.
- **`/tmp/opencode` is still unwritable** to the uid-501 driver, so the edit
  payloads went to `/tmp`. Already tracked.

## Symlink-guard review pass (2026-09-16, between-wave)

Read-only review of the pending symlink-guard wave: the container cannot build
or run, so these conclusions are from reading and from the brief's stated
falsification, not from a green test run.

- **The `apply -hunks` corruption report is disproved; the tool holds.** The
  symlink agent recorded, as tool friction, that `apply -hunks` corrupts
  non-overlapping hunks. A 2-hunk and a 12-hunk apply (line-internal
  replacements, ascending offsets, all base-relative) both produced
  byte-identical text to the expected result. The claim against the tool is
  withdrawn. The likely cause is hunks computed against a different base than
  the one they were submitted with: the editor rebases each hunk off `-base`,
  so offsets from an earlier read (or a hand-added shift off an earlier hunk)
  splice at the wrong byte while the reply still says "applied". Take offsets
  from the same `read -json` that supplies `-base`, or re-read between batches.
- **The pending `host.go` group is one coarse whole-file hunk.** Reconstructing
  the group's old/new text and diffing shows the net change is exactly the
  symlink guard (`SymlinkEscapeEnv`, `inRootResolved`/`resolveExistingPrefix`,
  the `canonical` pre/post check, `inRootDir`, `Open`, `Mkdir`, `CheckExec`,
  `claimPath`, `lspprep`) with no unrelated edits. It renders as one hunk
  because `DiffLines` degrades to a whole-file replacement past its `n*m` cap —
  filed in TODO.md.
- **`TestGuardRefusesSymlinkEscape` is only half-strong.** The `ls` assertion
  discriminates (without the change `memHost.Ls` returns a canned OK), but the
  `text`, `open` and `apply` assertions refuse for unrelated reasons with the
  fix absent (`memHost.Read` has no buffer; `memHost.Open` without `-create`
  errors; the `apply` read-gate fires), so they are vacuous. The discriminating
  coverage lives in `TestGuardSymlinkEscapeOptIn` (direct `canonical`, both env
  directions) and `TestGuardRefusesSymlinkParentEscape` (direct
  `inRootResolved` plus `open -create`/`claim`). The fixtures are real symlinks
  outside the root, so the state is genuine.
- **Dangling symlinks fall back to the lexical result.** `resolveExistingPrefix`
  cannot `EvalSymlinks` a link whose target is absent, so it treats the name as
  unborn and the in-root lexical check passes. `writeAtomic` also fails to
  resolve a dangling link and renames over the link in-root, so no write
  escapes today; the residual is a TOCTOU window if the outside target appears
  between the check and the save. Filed in TODO.md as a decision.
- **`internal/app/headless.go`'s comment described the old order.** Its doc
  said the Guard "resolves -- and so loads -- before it checks the root";
  `canonical` now pre-checks, so the comment was corrected to name the resolved
  guard check as the gate and `pathInRoot` as the lexical backstop for
  `loadHeadless`'s one caller.

## Advisory-lease + mid-line-merge review pass (2026-09-16, between-wave)

Live-verified over the socket against the rebuilt editor. These are the pass's
observations, the two doc corrections it proposes, and the decisions it
escalates.

- **The advisory lease is live and behaves as spec §12.1 says.** Two identities
  on a scratch buffer: A proposed `world -> socket`; B's `apply` over A's span
  succeeded (version 2 -> 3, no conflict), B's op opened its own group, and A's
  set reported `1 moved` with its surviving runs while `groups`/`diff` showed
  the overlap on both sets. A hunk catching both a Proposed and a Rejected run
  refused, naming the rejecting set (`change set 1 owns this text (author 4,
  bytes 0..6)`). A same-author amendment folded into the original set (2 ops,
  one group). Scratch buffer discarded; it was never on disk.
- **`exec -dir` is unreachable live over TCP.** `raj ctl exec -dir internal --
  true` answers the transport refusal (`exec is refused over TCP`) before
  `CheckExec` runs, so the relative-`-dir` refusal could not be reproduced over
  this transport. By reading: the live `CheckExec` calls
  `inRootResolved(dir)`, whose first act is `inRoot`'s "a path must be
  absolute", and the pending fix (`inRootDir`) joins the root first. The fix is
  right; only its live falsification is blocked by the TCP `exec` gate.
- **`Session.Leased`'s doc claimed the agent diff is refused.** It said "a
  pending or rejected span is atomic: an edit or an agent diff that intersects
  it is refused". The agent path now goes through `leaseSpan`/`rejectedLease`
  and a Proposed span is advisory, so the sentence was false. Corrected in a
  buffer to name the human typing path (`File.Insert`/`File.Delete`) as its
  consumers.
- **`TestControlGroupsReportLiveOverlaps` cited the old refusal.** Its comment
  said a direct session insert was the only way to reach the overlap "because
  ApplyDiff's lease refuses a hunk inside A's run". An `ApplyDiff` would land
  now and report the same overlap; the comment was corrected.
- **A wholly-overwritten Proposed set stays `proposed` with 0 hunks.** Live:
  after B overwrote all of A's inserted run, `groups` listed the set as
  `proposed ... 0 hunks 1 moved`, while `Pending` dropped it so a save is
  unblocked. `Invalid` (phase 1c) is the state that should name it; until then a
  listing cannot distinguish it from a set with surviving work.
- **`read` returns the view, not `AcceptedOnly`.** A rejected set's text still
  came back from plain `read`; `read -annotated` only added the state runs.
  TODO's decided item is `AcceptedOnly` by default (spec §6/§12.3), while the
  F3b-i note says "read = the buffer view with -annotated states". The two
  disagree and the implementation follows the latter. Escalated as the
  composition decision.
- **Mid-line merge is read-only verified.** The merge in `view.Build` absorbs a
  following kept piece only when `np.disp == merged`, `np.doc == docB`,
  `np.length > 0`, and no fold sits at `merged`; `[a,b)` is bounded to one
  composition line and a restore piece is emitted as its own row, so it cannot
  cross a fold, a restore or a newline. `checkProjectionInvariants` still
  asserts the full round trip, monotonicity and `RowText` reconstruction, so the
  oracle is not weakened, and `randomProjection` exercises adjacent kept pieces
  on every fuzz run.

## Phase 1c recomputed-`Invalid` review pass (2026-09-16, between-wave)

Live-verified over the socket against the rebuilt editor, two identities
(`review-p1c-01` author 3, `review-p1c-02` author 4) on a scratch `open
-create` buffer. The wave is saved; these are the review pass's observed
results, corrections and open questions.

- **Invalid is derived and clears with its collider, live.** A proposed
  `hello world\n` (set 1); B wholly overwrote it with `goodbye\n` (set 2).
  `groups -json` reported set 1 `"invalid":true` with
  `"invalid_by":{"group":2,"author":4,"start":0,"end":8}`, `hunks:0`,
  `moved:1`; set 1 was absent from `proposals` (`Pending`), and `read`
  returned `goodbye`. `reject`+`clear` of set 2 returned the text to
  `hello world` and set 1 to `hunks:1`, `invalid:false`, back in
  `proposals`. A partly-overwritten two-member set (one member consumed, one
  surviving) stayed `invalid:false` with `hunks:1 moved:1`, and stayed in
  `proposals`. Scratch buffers discarded; never on disk.
- **The predicate is one rule, live and read.** `markInvalid` decides
  survival with `len(projectMember(o)) > 0`, the same per-member projection
  `DiffPending`/`Pending` use to drop a set; `invalidBy` reuses
  `rebase(Pos, Pos+InsLen, Seq+1)` (the `rebasedInverseAt` walk) and
  `blockFor`, and returns false for a nil collider, so a wedge with no set
  stays invalid without a fabricated `InvalidBy`.
- **The wire field is sparse and forwards-compatible.** `hGroupInvalid`
  (0x5a) is a separate opcode emitted only when some group is invalid, one
  flag per group then (when set) `{group,author,start,end}`; `decodeHeader`
  merges it by position after reading every op, so ordering is not load-bearing
  and unknown fields are dropped (`prog.Decode(b, nil)`), not misread. Risk:
  an old reader drops the whole field and so shows an invalid set as valid --
  a display loss, never positional corruption, because it is not part of the
  `hGroups` record.
- **`read` docs corrected; three more stale claims found and fixed.** The
  wave fixed spec §12 decision 3 and the TODO "which composition does `read`
  return?" item. The same false "read defaults to `AcceptedOnly`" survived in
  spec §6 (consumers), spec §12.2 (resolved 2026-09-12), and TODO's Phase 1b
  description. All three are corrected in this pass; spec §5's
  `AcceptedOnly` definition also wrongly said it included `Proposed`.
  `app/control.go` `host.Read` returns `Project(Annotated)` and
  `Session().Version()`, so the corrected sentence and the coordinate argument
  hold.
- **`Project` does not consult `Invalid`.** For a wholly consumed insertion
  the bytes are gone from the session, so the edit composition is absent by
  construction, and `AcceptedOnly` excludes every `Proposed` set anyway. A
  deletion-only invalid member's `Del` bytes are also already applied to the
  session, so `AcceptedAndProposed` still applies its deletion rather than
  "excluding" it; that overlap is the undefined case, not an exclusion. Decide
  whether to wire `Invalid` into `included` or state the limit in the spec.
- **`review` and `proposals` cannot show an invalid set.** Both read
  `Pending()`, which drops it, so the human's review surface and the driver's
  rollup have no invalid row; `groups`/`diff` are the only surfaces. Decide
  whether the rollup should name it.
- **`markInvalid` adds a journal pass to `Groups()`.** It walks the journal
  and calls `projectMember` (two rebase walks) per live edit, so `Groups()`
  goes from O(ops) to O(ops^2); it is reached by `DiffPending`, `Pending`,
  `PendingOverlaps` and every `GroupDiff`, several of which are on the review
  and `groups` paths. Correctness is fine; measure before optimising, and note
  the existing `projection_bench_test.go` seam if it matters.
- **No app-layer unit test for the invalidation mapping.** `groups_test.go`
  (5 tests), `header_test.go` (`TestHeaderKeepsGroupInvalid`) and
  `cli_test.go` (`TestCLIGroupsShowsInvalid`) cover the session predicate, the
  wire and the CLI; the `host.Groups`/`host.Diff` piecetable-to-`control.Group`
  mapping is only live-verified. Note: `TestPartlyOverwrittenProposalSurvives`
  is a regression guard, not a feature test -- it passes without `markInvalid`;
  `TestInvalidWhollyOverwrittenProposal`, `TestInvalidClearsWhenColliderClears`
  and `TestHeaderKeepsGroupInvalid` fail without the change.

## Advisory-lease reporting (warnings) review pass (2026-09-16, between-wave)

Live-verified over the socket against the rebuilt editor, two identities
(`review-warn-01` author 5, `review-warn-02` author 6) on a scratch `open
-create` buffer that was closed with `-discard` and never reached disk. The
wave's reporting half is live and correct:

- **Overlap warns with group, author and span, live.** A proposed `hello
  world`; B's `apply` over part of A's run succeeded (`ok:true`, the version
  moved) and the reply carried
  `"warnings":[{"group":1,"author":5,"start":0,"end":11}]`; a second setup's
  text mode printed `note: overlapped change set 6 (author 5, bytes 0..3)`.
  The span is the superseded run's full bounds at the moment the hunk landed,
  not the intersection, which matches spec §12.1 "the span it held when the
  hunk landed".
- **Clean, refused and same-author stay distinct, live.** A clean insert at
  the superseded run's end carried no `warnings` key; an apply over a
  `Rejected` run returned only a `conflicts` record naming the owner and no
  warnings; a same-author amendment joined its own set (2 ops) and carried no
  warnings.
- **Wire: separate op, positional conflicts safe.** `hApplyWarnings` (0x5b)
  is a distinct argument-range op emitted after `hConflicts`/
  `hConflictLease`. An old reader's `decodeHeader` switch ignores an unknown
  argument code, so it drops the warnings whole while the positional
  `hConflicts` records still parse; `TestHeaderKeepsApplyWarnings` pins the
  coexistence. Risk: a new reader talking to an old server gets
  `Warnings == nil`, indistinguishable from a clean apply -- there is no
  header version negotiation to say "peer too old".
- **The CLI `-json` ordering fix is right, not hiding a bug.** Live:
  `apply <path> -base 1 -start 0 -end 0 -- -json x` exits 2 with "expected at
  most one replacement operand, got 2" -- after the `--` terminator `-json` is
  a positional. Putting `-json` before `--`, as the test now does, is correct.
- **`patch` silently dropped the warning, live (folded in this pass).**
  Against the pre-fix running binary, B's `patch` over A's proposed run landed
  (`patched snapshot 1 at version 2`) with no note in text mode, and
  `patch -json` returned `{"ok": true, "version": 4}` with no `warnings`;
  A's set went `1 moved` with no signal to B. `host.Patch` calls the same
  `File.ApplyDiff` and discarded the `[]Block`. Now `Patch` returns
  `[]control.GroupOverlap` through `BufferHost`/`Guard`/`memHost`/`Dispatch`,
  and the CLI shares `noteOverlaps` with `apply`. Pending host rebuild.
- **`host.Patch` re-proposed the last group on an all-refused patch (fixed).**
  Unlike `host.Apply`, it marked `LastGroup()` for any agent patch with a
  non-empty diff, even when every hunk conflicted and the version did not
  move; that can flip an accepted or rejected set back to `proposed`. Guarded
  with the same `Version() > before` check `Apply` uses. Pending host rebuild.
- **Mixed apply drops the warnings in text and JSON (CLI gap, filed).** One
  hunk over A's live run landed (a new group appeared and A's run split) while
  a second over a `Rejected` run was refused; the response held both, but the
  CLI printed only the conflict and `-json` emitted
  `{"ok":false,"conflicts":[...]}` with no `warnings`. `reportApply`/
  `patchCmd` return `reportConflicts` as soon as `len(res.Conflicts) > 0`,
  before the warning branch. The same shape makes `run` unable to show it:
  `apply` sets `Response.Err` on any conflict, `runProgram`'s `fail` stops on
  `Err`, and `collectAll` keeps only the final frame, so a program loses the
  warnings from every non-final apply verb.
- **Warnings are per-hunk and first-set.** `Session.ApplyDiff` appends one
  `Block` per landing hunk, so two hunks over the same set warn twice;
  `leaseSpan`/`rejectedLease` return only the *first* intersecting run, so a
  hunk that overlaps two different Proposed sets warns about one and silently
  moves the other. Deduping per set and reporting every intersected set is a
  content choice; escalated.
- **No mailbox notification to the superseded author.** Spec §7b says the
  occupying author "is **notified** (mailbox), never *summoned*"; the
  implementation appends a warning to the caller's reply only and enqueues
  nothing. Spec and code disagree; escalated.
- **`header.go`'s forward-compat comment names the wrong mechanism.** It says
  "nil is passed as the known set... so `prog.Decode` drops what it does not
  recognise"; with a nil `known`, `prog.Decode` returns every op and it is
  `decodeHeader`'s switch (which has no `default`) that ignores unknown codes.
  The conclusion is right; the comment points at the wrong function.
- **Test honesty.** The piecetable tests
  (`TestApplyDiffWarnsOverAnotherAuthorsProposal`, `...AmendDoesNotWarn`,
  `...CleanHasNoWarnings`, `...RefusalIsNotAWarning`) build their state with
  `groupSession` + `ApplyDiff` + `MarkGroup`/`RejectGroup`, exactly the state
  they assert, and fail without the third return. The wire test and the two
  Dispatch tests are canned wiring tests (they set the field and assert it
  round-trips), so they prove the plumbing, not the semantics; the new
  `TestDispatchPatchCarriesWarnings` and `TestCLIPatchNotesOverlapWarning`
  are the same shape. There is still no app-level two-identity test for the
  real `host.Patch` mapping (the `internal/app` gap already filed), and none
  for the all-refused `ProposeGroup` guard.

## Advisory-lease reporting completion review pass (2026-09-16, between-wave)

Live over the socket against the rebuilt editor, two identities
(`review-warn3-01` author 3, `review-warn3-B` author 4) on scratch
`open -create` buffers, all closed with `-discard` and none written to disk.
The wave's three standing decisions are implemented: the reporting half is
live and correct, and the mailbox half is live; the CLI *display* half could
not be exercised because the container client predates the wave.

- **Every intersected set, once each, live.** Two Proposed sets abutted at
  0..3 and 3..6 (authors 3 and 4); one hunk over [0,6) warned both, once
  each: `note: overlapped change set 1 (author 3, bytes 0..3)` and
  `note: overlapped change set 2 (author 4, bytes 3..6)`.
- **One set caught by two hunks warns once, live.** A set with two surviving
  runs at 0..4 and 8..12 (a `patch` opened one set over two disjoint lines,
  and a peer's run between them keeps `stateRuns` from merging them); one
  `apply -hunks` batch with a hunk over each run printed exactly one warning
  (`change set 2 ... bytes 0..4`), while `groups` showed `2 moved`. That is
  the end-to-end pin for the dedupe, and it matches
  `TestApplyDiffWarnsOnceForASetCaughtTwice`.
- **Mailbox notice, live.** The superseded author's `recv` returned
  `{"from":0,"text":"change set N (bytes s..e) in <path> was landed over by
  author M"}`; `from` is `AuthorOriginal`. A clean insert flush against a run
  and a same-author amendment produced none (`recv -wait 1s` exits 3). It
  fired from both `apply` and `patch`, and it fires for `edit` too — `edit`
  is `apply` on the wire — so "should edit notify?" is answered by the
  encoding rather than a separate decision.
- **The partial-refusal CLI display was NOT verifiable live: stale client.**
  A mixed batch (one hunk over a Rejected run, one over a Proposed run)
  landed the second hunk and refused the first; the container's
  `/usr/local/bin/raj` printed only the refusal and emitted
  `{"ok":false,"conflicts":[...]}` with no `warnings`. That is the pre-wave
  behaviour — the binary contains `note: overlapped change set` but not
  `was landed over by author` or `proposedSpans`, so it predates this wave.
  The server half is proven by consequence: the occupant's `recv` showed
  `change set 2 ... partial.txt ... author 3`, and `notifySuperseded` only
  posts when `Response.Warnings` is non-empty. The authoritative check for
  the CLI fix is the host `make check` on the two `cli_test.go` refusal
  tests; the live client must be rebuilt first.
- **The notice channel is immune to the `run`/program blind spot.** Because
  `notifySuperseded` is driven off `res.Warnings` per dispatched request
  (before any frame is chosen), a mixed apply still notifies the superseded
  author even though the program/CLI may drop the caller-side warning. A
  program that applies over a proposal notifies; it just cannot read back
  *why* until the run path keeps non-final frames.
- **`recv -json` timeout exits 0, not the documented 3.** Text mode
  `recv -wait 1s` exits 3 with no message, but `recv -wait 1s -json` prints
  `[]` and exits 0 (the cancelled branch returns `emit`'s code in the JSON
  case). A polling loop can read the empty array, so this is not fatal, but
  the comment claiming "3 means no message" is false in JSON mode.
- **The notice names the editor's path, not the caller's.** The driver worked
  `/work/review-warn3-scratch.txt`; the notice read
  `/Users/rajandavis/Desktop/projects/raj/review-warn3-scratch.txt` — the
  server composes `req.Path` after its own root mapping. Harmless to a human,
  confusing to a script.
- **A pathless apply/patch notice omits the path.** `notifySuperseded` reads
  `req.Path`; the buffer name the guard/resolver chose is not plumbed back,
  so `where` is empty for a request that named no path. The set, span and
  author still ride.
- **First-run vs bounding span is still a choice.** `proposedSpans` reports
  the first intersecting run's bounds per set, not the union of runs the hunk
  caught. The live dedupe warning named `bytes 0..4` while the set also held
  `8..12`. For "which set was superseded" the first run is enough; a bounding
  span is what a human would draw. Escalated before; still escalated.
- **Test honesty.** Semantic pins: `TestApplyDiffWarnsOverEveryProposedSet`
  and `TestApplyDiffWarnsOnceForASetCaughtTwice` (piecetable, modelled on the
  named lease sibling, each fails without the third return / the dedupe), and
  the real-host `TestControlApplyOverAProposalWarnsTheRealSet`,
  `TestControlPatchOverAProposalWarnsTheRealSet` and
  `TestControlSupersededAuthorIsNotified` (controlHarness, two identities,
  modelled on `TestControlLeaseRefusalCarriesTheGroup`). Canned plumbing
  only: the `memHost`/`Dispatch` tests (`TestDispatchApplyCarriesWarnings`,
  `TestDispatchPatchCarriesWarnings`) and the CLI tests. The CLI tests are
  honest pins of the CLI code path — the fake returns `Conflicts` and
  `Warnings` together, so the refusal tests fail without the change — but
  they prove the client logic, not the wire. No vacuous new test was found.
- **Still untested: the all-refused `ProposeGroup` guard.** `host.Patch`'s
  `Version() > before` guard is read-only-verified; no test drives a patch
  whose every hunk conflicts and asserts the previous set's state does not
  flip to proposed. The filed `controlHarness` test request survives.
- **`/tmp/opencode` is still root-owned 0755.** The skill's scratch dir was
  unwritable to uid 501; the `apply -hunks` fixture went to `/tmp` instead.
  Recurrence of the container-image item.
- **Own-set + other-set resolution flips with run order (a real
  inconsistency).** A hunk that spans the editing author's own Proposed set
  *and* another writer's resolves two ways by which run `leaseSpan` meets
  first. Live: B inserted at 0..3 and A at 3..6, then B's hunk over [0,6) was
  refused (`change set 1 owns this text (author 4, bytes 0..3)`); in the
  mirrored buffer (A's run first, multi2) the same shape landed and warned
  about both sets. The `ApplyDiff` comment says own+other "is not a clean
  amendment and still conflicts", so the mirrored case silently supersedes
  the author's own draft instead of refusing. No test pins own+other at all:
  `TestApplyDiffWarnsOverEveryProposedSet` uses two *other* authors and a
  third editor. Escalated: decide conflict-everywhere or
  land-and-warn-everywhere, make the two orderings agree, and pin it.

## Order-independence review pass (2026-09-16, between-wave)

Live over the socket against the rebuilt editor and the matching container
client, three identities (`review-order-01` author 4, `review-order-02` author
5, `review-order-03` author 6) on scratch `open -create` buffers, all closed
with `-discard` and none written to disk. The wave's order-independence fix is
live and correct; the pass removed two functions the fix orphaned and filed one
wording item.

- **Order independence is live in both directions.** Buffer A: own run at
  0..3 (author 4) then a peer's at 3..6 (author 5); author 4's hunk over [0,6)
  refused, naming set 1 / author 4 / bytes 0..3. Buffer B: the peer's run first
  at 0..3, own at 3..6; the same hunk refused, naming set 2 / author 4 / bytes
  3..6. Same outcome, own set named, version unchanged, no warnings.
- **Only-others lands and warns; wholly-own joins.** Buffer C (two peers,
  authors 5 and 6): author 4's hunk over both landed and warned both once each
  (groups 1 and 2), both sets going `0 hunks 1 moved` + invalid. Buffer D
  (author 4's own run): a second hunk wholly inside it joined the same set
  (2 ops) with no `warnings` key.
- **The mixed batch now exercises the CLI fix live.** Buffer E: a `Rejected`
  set at 0..3 and a `Proposed` set at 3..6 (both author 5); author 4's two-hunk
  batch refused the first and landed the second. Text mode exited 1 and printed
  `raj ctl apply: change set 1 owns this text ...` and
  `note: overlapped change set 2 (author 5, bytes 3..6)`; `-json` carried both
  `conflicts` and `warnings`; the superseded author's `recv` delivered the
  notice. This is the check the previous pass could not run for the stale
  client.
- **`recv -json` cancellation.** `recv -wait 500ms -json` exited 3 and printed
  `[]`, matching text mode; the notice text is
  `change set N (bytes s..e) was landed over by author M` -- no path.
- **All-refused patch.** A rejected set at 0..3, a whole-file dump taken
  before the rejection, then a patch into the rejected run: refused, version
  unchanged, and the set stayed `rejected` (not flipped).
- **`leaseSpan` and `leasedElsewhere` were both orphaned by the fix, not just
  the one the brief named.** After `ApplyDiff` moved to
  `proposedSpans`/`rejectedLease`, a tree-wide search found no production or
  test callers for either; `F3B-II-DESIGN.md:196` still named
  `leasedElsewhere`/`commitInto` as the future authority and the `Leased` doc
  comment still named `leaseSpan`. Removed both, repointed the design doc to
  `proposedSpans`/`rejectedLease`/`commitInto`, and dropped the dangling
  `leaseSpan` reference in `proposedSpans`' doc. `lsp diagnostics` clean on
  both files; the removals are in buffers pending the user's save and the next
  `make check`.
- **The own+other conflict message does not describe its own case.** The
  refused hunk's message is the generic lease sentence `change set N owns this
  text (author A, bytes s..e); accept or reject it first`, but the set it names
  is the *writer's own*; the driver cannot accept its own draft, and the
  actionable fix (narrow the hunk off the peer's text) is not stated.
  `Conflict` carries no marker distinguishing own+other from a genuine peer
  lease. Filed as a content decision.
- **The all-refused patch test's discriminating assertion is the second one.**
  Without the `Version() > before` guard, `Begin` has already reserved a new
  group id and `ProposeGroup(LastGroup())` marks *that* empty group proposed,
  not the prior set; the prior set stays rejected either way. The test's first
  assertion (`prior == Rejected`) therefore passes with and without the guard;
  only `GroupState(LastGroup()) != Proposed` fails when the guard is absent.
  The name and comment ("repropose the prior set") overstate what flips. The
  test is honest, not vacuous; the fixture description should name the phantom
  group.
- **No other vacuous new test.** `TestApplyDiffRefusesOwnAndOtherProposalEitherOrder`
  really builds both projection orders (own at 0..3, met first, in one subtest
  and own at 3..6, met second, in the other) and fails without the change in
  the second; `TestCLIRecvJSONTimeoutExitsThree` drives the real server's
  cancelled frame; `TestControlSupersededNoticeShipsNoPath` asserts the message
  contains no `/`.


## Ledger, own+other conflict message and D2b-seam review pass (2026-09-16, between-wave)

Read-only over the socket except the ledger corrections below; the container has
no Go toolchain, so the host `make check` stays the gate. `lsp diagnostics`
returned `ok` on `internal/control/cli.go`, `internal/app/app.go`,
`internal/app/review.go`, `internal/editor/pane.go`, `internal/view/projection.go`,
`internal/piecetable/groups.go` and the four touched test files, so the only
compile check available in the container is clean.

- **The docs index matches the directory, both ways.** `ls /work/docs` returns 18
  files and `docs/README.md` has exactly 18 rows naming them; the three folded
  sources (`AGENT-VERB-AUDIT.md`, `PROPOSALS-SPEC.md`, `RECONCILIATION-UX.md`)
  are still on disk, so their retired rows and "delete pending" notes are
  correct and go only when the files do.
- **`TODO.md` carries no `[x]` and no `[~]` checkbox** (`read -json | jq`, since
  `search` caps at 20): 0 and 0, with 107 `[ ]`. The only `[~]` left was the
  legend sentence in the agent-feedback section, which described a marker no
  entry now uses; removed in this pass.
- **The wave left its own seams and wording stale in the ledger.** `TODO.md`
  still described `App.jumpTo`/`reviewJump` as "two copies" and `Pane.RowText` as
  absent, and still carried the own+other message as an open decision, though all
  three landed in this wave. The `Pane.RowText` and own+other entries were moved
  to `COMPLETED.md`; the jump entry was narrowed to the remaining `host.Goto`.
- **The own+other conflict is live and correctly worded.** Two identities
  (caller author 3, peer author 4) on an `open -create` scratch buffer: the
  caller proposed `AAA` at 0..3, the peer `BBB` at 3..6, then the caller's hunk
  over [0,6) refused with `change set 1 is your own draft (bytes 0..3); this hunk
  also crosses another writer's text — narrow it to avoid their span`; the
  `-json` reply carried the same message and no `accept or reject`. A peer-owned
  `Rejected` set at 6..9 refused the caller's hunk with `change set 5 owns this
  text (author 4, bytes 6..9); accept or reject it first`, byte-for-byte the old
  peer sentence. Scratch closed with `close -discard`, never written to disk.
- **Test honesty.** `TestCLIRefusalNamesTheCallersOwnDraft` builds the own+other
  state through the fake's `ownLease`, which stamps `Author: req.Author` (the
  real connection author, learned from `res.Author` in `control.go`), and fails
  without the `leaseView` branch. `TestCLIRefusalKeepsThePeerWording` passes on
  pre-change code by construction — it is a guard against the new wording leaking
  into the peer path, now labelled as one in `cli_test.go`.
  `TestCompletionPrefixIsTheRowTextBelowAMidLineFold` builds a rejected mid-line
  insertion, asserts the fold exists and the row differs from the session line,
  and would fail on the old session-byte prefix (`fooREJbar`, not `bar`).
  `TestOneJumpPathSkipsHiddenAndCentresVisible` and the two `RowText` tests
  exercise the collapsed jump and the new accessor; the `RowText` tests build a
  real fold through `foldedPane`.
- **The five `[~]`->`[ ]` conversions read as open work, not host-confirmed done
  work.** Without a git checkout in the container the exact five cannot be
  reconstructed; the entries carrying an "in-tree part done / remainder open"
  split are the dangling-symlink decision, the save-review lag diagnosis, the
  app-layer `Invalid` mapping test, run-`prog` path reachability, the
  superseded-warning span choice, the docs-index drift test and the
  request-header count. Each remainder is unstarted or a decision (not merely
  host verification), so `[ ]` is the right taxonomy; the in-tree halves are
  already one-line COMPLETED entries. The one stale premise was the two seam
  entries, fixed in this pass.
- **The ledger's three unsettled flags are filed.** The docs drift check is
  `TODO.md` "Guard the docs index against drift"; the missing app-layer `Invalid`
  mapping test is `TODO.md` "The invalidation mapping has no app-layer test"; the
  request-header count reads three (`recv`, `hello`, `cancel`), matching
  `internal/control/prog.go` and the serve loop's pre-dispatch handlers, and the
  item is its own record.
- **A saved wave still has no enumerable diff.** Already a TODO, but this pass
  felt it: with `proposals`/`groups` empty after the save and no git in the
  container, the wave was reconciled from the brief's file list plus a tree
  search. A `history`/`changes` verb over a saved wave would make the next pass
  cheaper and the ledger harder to leave stale.

## Cut-exploration wave review pass — multi-target read, `search -context` (2026-09-16, between-wave)

Live over the socket against the rebuilt editor and container client, two
identities (`review-explore-01` author 3, `review-explore-02` author 4). The
wave's two additions are live and correct; this pass fixed one read-gate hole,
closed four test gaps, documented the surface in the skill, and re-ran the call
census.

- **Multi-target read is live.** `raj ctl read docs/TODO.md docs/COMPLETED.md
  -lines 1,2 -json` returns one object per file with each text and version; the
  human form prints `==> path <==` headers; a shared `-lines`/`-start` span
  applies to each. The single-target form is unchanged: `read docs/TODO.md
  -lines 1,3 -json` is still `{author,spans,text,version}`, and a one-path read
  no longer goes through `readPaths`. `-annotated` with two paths is refused
  `raj ctl read: -annotated takes one path`, exit 2, with or without `-json`.
- **Read-before-write is satisfied for every target.** After one
  `read review-w2-a.txt review-w2-b.txt`, an `apply -base 0` to B and then to A
  both landed; a fresh scratch buffer with no read refused with `read the buffer
  before writing it: offsets only mean something in the coordinates of a version
  you have seen`. The gate is real, so the both-landed result is the pin. Two
  identities confirm the keying: author 4 claimed the file, author 3 multi-read
  it, and author 4's apply was still refused until author 4 read it itself, so a
  multi-read marks the targets under the request author and no one else.
- **`search -context N` is live and the no-flag output is unchanged.**
  `search -q "Agent call census" -include docs/BENCHMARKS.md -context 1` printed
  the hit under `path:line:col version 0` with its neighbour; `-json` carried
  `"context"` and `"version": 0`; `-jsonl` carried `"context"` only with the
  flag. Without it the human line is exactly `path:line:col:text` and the JSON
  has no `context` key. `-context 0` is byte-identical to an absent flag; a
  negative is refused exit 2. Clipping at the first and last line is correct,
  and unsaved open buffers report their nonzero version.
- **Wire form.** `hMatchContext = 0x5c` is its own sparse argument op, emitted
  only when some hit carries context and one string per hit in hit order; an old
  reader's switch has no case for it and drops it whole (`decodeHeader` has no
  `default`). `Query.Context` is the last field in the `hQuery` record
  (`w.Str(q.Path).Bool(q.Hidden).Num(q.Context)`), so an old reader that stops
  after Hidden ignores the trailing varint (`prog.Reader` reads past the end as
  zero, and the `hQuery` case does not treat `Bad` as a frame error). The brief
  called `hQuery` "JSON"; it is a `prog.Writer` record, so the argument is
  "trailing bytes are ignored", not "unknown JSON keys are ignored" — worth
  correcting on the next brief.
- **`internal/app/ls.go` was the only mapping that mattered.** `runSearch` is
  the one place a `control.SearchQuery` becomes a `search.Query`, and the only
  change there was `Context: q.Context`. `Search` and `SearchHidden` both
  delegate to it (the one-body share landed in the 2026-09-16 ls review pass);
  without the field `-context` is dead end to end. Nothing else in ls.go moved.
- **Test honesty.** `TestDispatchReadMultipleTargets` and
  `TestDispatchReadMultipleAllowsLaterWrites` are real: the fixture seeds the
  second doc and version, the pre-wave Dispatch cannot answer a `Paths` read at
  all, and a fresh buffer is refused by the gate, so the later-writes test is
  discriminating. `TestCLISearchContext` drives the real header codec through
  the fake server, so it pins the query's Context and the response's
  hMatchContext end to end; `TestHeaderKeepsMatchContext` puts a no-context hit
  between two context hits so a positional shift fails; the real-engine
  `TestRunContextIncludesNeighbouringLines` writes its file and asserts exact
  top clipping. No vacuous new test found.

### The read gate was satisfied by a read that failed (fixed in this pass)

`readPaths` read each target through `Guard.Read`, which marks the path read on
success, then returned on the first error. `read a missing` therefore returned
an error but had already marked `a` seen, so a later `apply` to `a` was allowed
without the caller ever receiving its text — the one thing the gate exists to
stop. Fixed: `readPaths` now reads through `g.Host.Read` and calls `g.markRead`
for every target only after the whole loop succeeds, so a mid-list failure marks
none. `TestDispatchReadMultipleFailureDoesNotMarkEarlierTargets` pins it.

### Gaps this pass closed

- `fullHeader` is the every-field round-trip fixture but set neither
  `SearchQuery.Context` nor `MatchMeta.Context`; both are set now, so a dropped
  encode line fails `TestHeaderRoundTrip`.
- `TestOldShapedQueryStillDecodes` was the only old-shape query test and its
  comment claimed Path was newest; a `Path`+`Hidden`-without-`Context` payload
  (the immediately preceding build's shape) had no pin. Added
  `TestQueryWithoutContextStillDecodes` and corrected the comment.
- `TestCLISearchContext`'s JSON half asserted a substring like `"version": 1`,
  a spacing assertion of the class that cost a host cycle earlier; it now
  unmarshals and asserts the context value and version.
- The app-layer `runSearch` forwarding had no test — the CLI test speaks to the
  fake editor — so it is pinned by `TestSearchContextReachesTheEngine` in
  `internal/app/ls_test.go`.

### Escalations (content/design choices, not decided here)

- **The multi-read `-json` shape is inconsistent with the single read.**
  Single: `{author,spans:[...],text,version}`. Multi:
  `{files:[{path,text,version}]}`, no author and no per-file spans. Whether the
  multi form carries authorship (it is available) or stays a path/text/version
  list is the user's choice.
- **A shared byte span that overruns one target refuses the whole call, while a
  shared line range clamps per file.** Live: `read docs/TODO.md review-w2-a.txt
  -start 0 -end 5` returned `offset out of range: [0, 5) is not within [0, 3)`,
  while `-lines 5,9` returned the one-line file's whole line. The byte form
  inherits single-read bounds-checking (right alone, surprising across files);
  clamping per target and refusing are both defensible.
- **`Buffer.Bytes` now means two things.** In a `buffers` reply it is the
  document length; in a multi-read reply it is the bytes this read contributed.
  Reusing the shape is the no-new-field decision; the field's doc does not say
  so.

### Census re-run (2026-09-16, during this review pass, so it includes the review's own calls)

`node scripts/call-census.mjs` over the current store: 26,859 tool calls —
26,320 bash, 250 task, 197 skill, 78 todowrite. Verbs: read 13,085 (32.5%),
search 12,569 (31.2%), edit 3,170, lsp 2,062, open 1,765, version 1,181, apply
1,084, buffers 1,071, groups 847, goto 501, diff 438. Transitions: read→read
4,475, search→search 3,540, search→read 2,761, read→search 2,534. The baseline
in BENCHMARKS.md (26,059 bash; read 12,944; search 12,410; read→read 4,416) is
accurate for the snapshot it names; the store has since grown by the sessions
after it, and the shares are unmoved, which is the expected pre-use reading. The
doc's two read→read figures (4,416 and 4,459) are two runs of the same script,
not two methods, and should be collapsed to one.

### What the next census must show for this wave to count

The tools only pay off if the self-looping explore cluster shrinks. Falsifiable
targets, per session against the baseline's shares: the `read` share falls from
32.5% (one call reads several files), the `search→read` transition falls from
2,761 (a hit's neighbours no longer need a follow-up read), and the combined
`read`+`search` share falls from 64%. The `search` count itself should not fall
— `-context` is still one search — so success is read-count and total
verb-count, not search-count. The census cannot see the win directly: it counts
command strings, so a multi-path read counts once and its benefit shows as fewer
reads, not as a new verb. Compare per-session medians, not aggregates, and gate
on the explore phase (consecutive reads/searches before the first write) getting
shorter.

## Encoding + compaction + hover wave review pass (2026-09-16, between-wave)

Read-only: the container has no Go toolchain, so nothing was built or run; the
host `make check` is the gate. `raj ctl lsp diagnostics` on every touched file
(`internal/editor/{encoding,binary,file,reload}.go`,
`internal/piecetable/{compact,project,groups,session}.go`,
`internal/hover/hover.go`, and the test files) returned `status: ok`; one cold
start answered `starting` and was retried. The wave was accepted and saved, so
there were no pending sets to reconcile.

### Verified by reading

- **The encoding decision is one classifier.** `charset` owns the BOM switch
  (`encoding.go:156`), the NUL / unmarked-UTF-16 / single-byte heuristics, and
  is called by both `decode` (`:133`) and `IsBinary` (`binary.go:40`), which
  only forwards and tests `errors.Is(err, ErrBinary)`. The sniffer and decoder
  cannot disagree.
- **decode/encode is the inverse it claims.** UTF-8 (±BOM), UTF-16LE/BE (±
  surrogate pairs, ±CRLF), Windows-1252 and Latin-1 are bijective; the CP1252
  table maps the five undefined bytes to themselves so the round trip holds.
  `stripCR`/`encode` reproduce `\r\r\n` (the fuzz corpus entry).
  `FuzzEncodingRoundTrip` skips Mixed, the documented lossy case.
- **Refusals are honest.** UTF-32 is checked before the UTF-16 BOM it prefixes
  (`:158`); odd byte counts and unpaired surrogates refuse in `decodeUTF16`;
  `encode` refuses an unrepresentable character before any write; `Open`
  (`file.go:133`) and `Reload` (`reload.go:50`) return the decode error before
  mutating the file, and `SaveOver` (`file.go:806`) returns the encode error
  before `writeAtomic`. A refused reload/re-save leaves the buffer and the
  on-disk bytes alone (`TestReloadRefusesABinaryReplacement`,
  `TestSaveRefusesUnencodableText`).
- **Compaction moves bytes, not history.** `Compact` (`compact.go:47`) only
  rewrites `s.buf`; `Version()` is `len(journal)`, so it is unchanged. The merge
  branch requires the same store buffer (author), the same `originIndex` owner
  and exact store contiguity (`cur.Start+cur.Length == next.Start`), so the
  composed text is character-identical and no group absorbs another's bytes;
  `TestCompactKeepsProjectionOracle` pins text and Annotated states for all
  three policies. Flatten requires `Accepted` and every live claimant's
  `Seq < saved` (`pieceStable`), so Proposed/Rejected and unsaved spans keep
  their pieces (`TestCompactDoesNotFlattenPendingOrRejected`,
  `TestCompactDoesNotFlattenUnsavedSpan`). `Store.Append` is a pure append, so
  the flatten copy's `off - cur.Start` arithmetic is sound.
- **Compaction is inert.** The only callers are `compact_test.go`; nothing in
  `internal/app` or elsewhere calls `Compact`. `allOrigins` now walks
  `s.compacted`, which is empty until a call, so even the projection paths are
  byte-identical until wiring lands.
- **Hover claims keys narrowly.** `Handle` always claims escape, claims the four
  scroll keys only when `Scrollable()` (`shown > 0 && len(body) > shown`), and
  returns false otherwise, so a fitting panel falls through to the old "any
  action dismisses" path in `app.go:1086` and `TestHoverPanelClosesOnAnyAction`
  still holds. `scrollBy` and `Render` both clamp, and `Render` zeroes `shown`
  when there is not room for a box. The app-layer test draws first, so it builds
  the geometry it asserts.
- **Fuzz corpus sweep is clean.** Only two corpus entries exist repo-wide
  (`FuzzEncodingRoundTrip/0cf09c667499f7b7` and
  `FuzzProjectAgainstOracle/ebfe21d7ff53dc71`); both are `[]byte(...)` and match
  their `f.Fuzz` parameter types. No other fuzz target changed signature this
  wave.

### Findings (raw, with severity)

- **High — crash-restore loses the encoding, and the wave widened what that
  costs.** `NewRestoredFile` (`internal/editor/file.go:187`) documents it: a
  restored buffer has never been saved by this process, so its encoding is the
  default until a save writes one. `internal/journal` has no encoding field
  (`Written` carries Path/Hash/Version), so a dirty log replayed after a crash
  comes back as zero `Encoding` (UTF-8/LF/no BOM) and the next save rewrites a
  UTF-16/CRLF/CP1252 file as UTF-8/LF. Pre-existing — CRLF was already exposed —
  but before this wave CP1252/Latin-1/UTF-16 were refused at `Open`, so they
  could not be opened and then corrupted. A widened gap, not a regression in
  the encoding code.
- **Medium — a refused encode is not a no-op.** `SaveOver` calls
  `f.AcceptPending()` before `encode`, so `ErrUnencodableText` leaves every
  proposed set accepted although nothing was written (the brief called it a
  no-op). `App.write` (`app.go:1656`) only reports "save failed".
- **Medium — `EncodingWarning` is dead.** Nothing in `internal/app` calls
  `File.EncodingWarning`, so a mixed-ending file is normalised to the dominant
  ending on save with the warning never shown, contradicting the method's own
  comment. Wire it beside `IndentWarning` (`app.go:364`, `:1357`).
- **Medium — asterisk emphasis mangles plain text.** `inline`
  (`internal/hover/hover.go`) has the `_`-identifier guard but no flanking guard
  for `*`, so `2 * 3 * 4` renders as `2  3  4` (markers dropped). The blanket
  "cannot mangle plain text" claim holds only for underscores; a glob like
  `a/*/b` still italicises the `/`.
- **Low — the UTF-32LE mark shadows a UTF-16LE file whose first unit is
  U+0000.** `ff fe 00 00` is checked as UTF-32 before the UTF-16 BOM, so such a
  file is refused by name rather than decoded. Refusal, not corruption.
- **Low — unmarked UTF-16 with no NUL is decoded as Latin-1.** `looksUTF16`
  only fires when NULs are present, so a BOM-less CJK/emoji UTF-16 file falls
  through to the single-byte branch and displays as mojibake. It still
  round-trips byte-for-byte, and an edit needing a byte the mapping lacks
  refuses at save, so the failure is visible rather than corrupting.
- **Low — `IsBinary` is exported but has no production caller** (`Open`/`Reload`
  call `decode` directly).
- **Low — `OpenFile` sends `ErrUnsupportedEncoding` to the status line**, not the
  refusal dialog its own comment reserves for statements about the file.
- **Low — fence matching is prefix-only.** A four-backtick fence is read as
  three and an interior line starting with three closes it early; acceptable for
  a hover subset but an escalation, not a decision made here.

### Test honesty

- Fault-finders (would fail without the change):
  `TestEncodingRoundTripPreservesBytes`, `TestOpenDecodesUTF16`,
  `TestEditedUTF16FileStaysUTF16`, `TestOpenDecodesWindows1252`,
  `TestOpenDecodesLatin1`, `TestOpenRefusesUnsupportedEncoding`,
  `TestOpenRefusesUnmarkedUTF16`, `TestSaveRefusesUnencodableText`,
  `FuzzEncodingRoundTrip`; `TestCompactMergesAdjacentSameAuthorPieces`,
  `TestCompactFlattensSavedCommittedSpan`,
  `TestCompactDoesNotFlattenPendingOrRejected`,
  `TestCompactDoesNotFlattenUnsavedSpan`, `TestCompactIsIdempotent`,
  `TestCompactKeepsProjectionOracle`; `TestOverlongContentIsScrollable`,
  `TestScrollKeysMoveTheOverflowingView`, `TestScrollIsClamped`,
  `TestFencedCodeKeepsItsFence`, `TestIndentationInsideFencesSurvives`,
  `TestBlankRunsCollapse`, `TestInlineCodeBoldAndItalic`,
  `TestListItemsGetBullets`, `TestHoverPanelScrollsWithoutMovingTheCaret`.
- Guards (pin behaviour that should not change):
  `TestIsBinary`'s Latin-1/CP1252 cases assert the new shared verdict but
  `IsBinary` itself is uncalled; `TestClaimsNothingElse`,
  `TestClosedPanelClaimsNothing`, `TestUnderscoresInIdentifiersAreNotEmphasis`,
  `TestHoverPanelClosesOnAnyAction` (the fitting fall-through).
- Gaps: no app-layer test opens a legacy-encoded file (only the binary refusal
  is covered); no test that a refused `Reload` leaves `Enc` unchanged (the
  ordering guarantees it but does not pin it); `EncodingWarning` is tested only
  at the editor layer.

### Escalations (content/design, not decided here)

- **Whether to wire `Compact` now.** It is safe but inert; the wave left it
  uncalled on purpose. Wiring it is a behavioural change (idle-tick cost,
  interaction with review state) and is the next wave's call.
- **Whether to persist the encoding in the journal now, or accept the restore
  gap** with the new encodings. Persisting is a Version-3 additive record and
  touches `internal/journal` plus `internal/app/journal.go`.
- **The markdown subset's flanking rules and fence matching**: tighten to
  CommonMark-ish rules, or document the subset as deliberately loose.
- **`SaveOver`'s accept-before-encode ordering:** make it atomic, or accept that
  a failed save still approves the pending sets.

## Encoding-through-restore + app-wiring wave review pass (2026-09-16, between-wave)

Read-only: the container has no Go toolchain, so nothing was built or run; the
host `make check` is the gate. `raj ctl lsp diagnostics` on
`internal/app/{journal,app,journal_test,app_test}.go` returned `status: ok`. The
wave was accepted and saved, so there were no pending sets to reconcile.

### Verified by reading

- **The two mappers are total inverses.** `journalEncoding` and
  `editorEncoding` (`internal/app/journal.go`) cover all five `editor.Kind`
  values plus the default, copy `CRLF`/`BOM`, and drop `Mixed` (not persisted,
  by design). `editor.UTF8`/`journal.EncodingUTF8` are both zero, so the
  identity round-trips; `validate` bounds the kind and the decoder refuses a
  non-canonical explicit default.
- **The default path is byte-identical.** The two append sites (`startTap`'s
  fresh `Base`, `recordWritten`'s `Written`) both pass
  `journalEncoding(p.File.Enc)`; a default buffer maps to the zero `Encoding`,
  which `encoder.encoding` omits, so an old log re-encodes to its own bytes.
- **Only two append sites exist** for `journal.Base`/`journal.Written` (plus
  tests), both in `internal/app/journal.go`.
- **A fresh Base records a non-default open.** `editor.Open` sets `File.Enc`
  from `decode`, so `startTap` records it for a path with no prior log — the
  follow-up item is closed, not open.
- **The restore seam is ordered.** `restoreLog` calls `SetEncoding` before
  `startTap`, so the reused or fresh base records the restored shape.
- **CompactTick is off the keystroke path.** Only `ui.Tick` calls it; the 2 s
  `compactAt` debounce and the `Pieces() < 2` skip run before `Compact`, and the
  `(Version, DecisionGeneration)` memo is checked before `Compact` builds its
  origin index. `Compact` does not move `Session.Version`, so the memo hit
  lasts. `SavedVersion()` is passed and `Compact` refuses Proposed/Rejected.
- **Compacting before `journalTick` is safe.** It is not the load-bearing
  guarantee: `appendTap` already writes store growth before ops, so a copy a
  later op addresses is always in the log first. First is the conservative
  ordering, not an accident.
- **Status-clobber audit.** The other `a.status = ""` sites (`gotoSymbol`,
  `applyHover`, `applyDefinition`, `clickSidebar`, `clickEditor`, `reviewList`)
  are success-path clears after a failure branch returned, not set-then-clobber.

### Findings (raw, with severity)

- **High — the restore's Written-hash comparison mixed text and bytes, losing
  post-save edits for every non-default encoding.** `restoreLog` built `disk`
  from the decoded text (`hashBytes(orig)`) and compared it to `mark.Hash`,
  which `recordWritten` fills from `File.SavedDigest()` — the digest of the
  *encoded* bytes. For CRLF/BOM/UTF-16/CP1252 they never match, so a saved file
  with a later unsaved edit was archived as "changed on disk" and the edit was
  lost; the clean baseline (`RestoredWrite`) was never built either.
  `logIsCleanOnDisk` had the same conflation. **Fixed this pass:** compare
  `digestOf(p.File.SavedDigest())`/`digestOf(f.SavedDigest())` to `mark.Hash`
  and the text hash to `base.Hash`; pinned by
  `TestJournalRestoreReplaysUnsavedEditAfterSave`.
- **Medium — compactTick bounds frequency, not cost.** `Compact` runs
  `allOrigins` plus a whole-journal `pieceStable` whenever a pane's version or
  decision generation moved; under sustained typing that is once per 2 s,
  because the memo only spares an unchanged pair. Filed in TODO.
- **Low — `App.compacted` is never pruned.** Filed in TODO.
- **Low — a pre-tail log restores the default encoding.** Filed in TODO.
- **Low — announcing a headless buffer skips `fileWarning`.** Filed in TODO.
- **Low — the `fileWarning` call-site comment is stale.** It says "Clear any
  stale note first", but the assignment is the clear; there is no preceding
  clear. Left as-is; a one-line reword.

### Test honesty

- Fault-finders (would fail without the change):
  `TestJournalRestoreReencodesSavedEncoding` (the base-only subtest),
  `TestJournalRestoreWithoutEncodingKeepsDefault`,
  `TestJournalRecordsBufferEncoding`,
  `TestCompactTickMergesAdjacentSameAuthorPieces`,
  `TestOpenSurfacesMixedEndingWarning`.
- **A test that was not one.** `TestJournalRestoreReencodesSavedEncoding`'s
  `after save` subtest passed for the wrong reason: the log was archived by the
  bug above, so the pane came from disk (already UTF-16) and the encoding
  assertion held with `SetEncoding` doing nothing. The new
  `TestJournalRestoreReplaysUnsavedEditAfterSave` makes the post-save edit
  unmissable and fails on the old code.
- Guard labelling verified: `TestCompactTickLeavesAQuietBufferAlone` names the
  `Pieces() < 2` guard and does fail without it (the empty pane would enter
  `App.compacted`). The normal-ending half of
  `TestOpenSurfacesMixedEndingWarning` is the silence guard and holds.
- Added this pass: `TestOpenSurfacesIndentAndEncodingWarnings` — there was no
  app-layer test that `IndentWarning` reaches the status line, and the
  mixed-endings comment wrongly credited the editor-layer `TestIndentWarning`.
- Remaining gaps: no app-level test that `compactTick` passes `SavedVersion`;
  no test that the 2 s debounce skips a call inside the interval.

### Escalations (content/design, not decided here)

- **Whether to benchmark the 2 s compaction scan before raising the interval or
  adding a fragmentation trigger.** The memo does not help a buffer being typed,
  so a large journal pays a whole-journal `pieceStable` every 2 s. The threshold
  is a measurement, not a guess.
- **The pre-tail-log migration.** Recover the shape by decoding the disk when
  the log is silent, or accept the one-time CRLF→LF conversion.
- **`Base.Hash` is a text hash while `Written.Hash` is a byte hash.** The fix
  makes each comparison its own kind, but an external edit that changes only
  line endings still cannot be told from the base by text alone; making Base a
  byte hash would unify them at the cost of re-reading/encoding at capture.

## Docs retirement, compaction bench, encoding tail and rebind review pass (2026-09-16, between-wave)

Read-only: the container has no Go toolchain, so nothing was built or run; the
wave report says the host `make check` was green, and it stays the gate. `raj ctl
lsp diagnostics` returned `status: ok` on `internal/docsindex/docsindex_test.go`,
`internal/piecetable/compact_bench_test.go`, `internal/keys/table.go`,
`internal/app/{journal,app,headless,journal_test,app_test,session_test}.go` and
`internal/app/removals_test.go`. The wave was accepted and saved, so there were
no pending sets to reconcile. This pass added one test, reworded two comments
and retired three TODO items.

### Verified by reading

- **The docs guard is honest for (a) and (b), and was dormant for (c).**
  `readIndex` only accepts lines starting `| [`, so the header and separator
  cannot be counted; `linkTarget` takes the first link and `cells[2]`/`cells[3]`
  are the kind and purpose columns. `TestEveryDocHasARow` fails an unlisted
  `docs/*.md` and `TestEveryRowNamesAFileThatExists` fails a dangling row;
  `filepath.Base` keeps both inside `docs/`. `docs/README.md` has exactly 15
  rows naming the 15 on-disk `.md` files. `TestRetiredRowsHaveNoFile` iterated
  zero rows after this wave deleted both the retired rows and their files, so
  this pass named the predicate (`retiredRowsWithFiles`) and added
  `TestRetiredRowPredicate`, which exercises both the "retired" and "delete
  pending" markers and both the present-file and absent-file branches.
- **The rebind is complete.** No `ctrl+super+d` or `100;13u` survives anywhere;
  `cmd+ctrl+d` appears only in the note explaining why it was not chosen.
  `100;7u` is `ctrl+alt+d`: the same modifier byte `7` decodes to ctrl+alt in
  `119;7u -> ctrl+alt+w` (`keymap_test.go`) and `118;7u -> ctrl+alt+v`.
  `docs/KEYBINDINGS.md`'s row is byte-for-byte the format `keys.Doc()` emits, and
  `TestCheckedInDocMatchesTheTable` would fail if it were not. The group comment
  now says "pinned in Bindings rather than Natives", true of ctrl+alt+d.
- **The encoding recovery is shape-only, tail-only and startup-only.**
  `restoredEncoding` returns early when `Log.Encoding()` is non-zero and
  otherwise uses only `editor.Open(...).Enc`; an open error returns the zero
  encoding, which `editorEncoding` maps to the default, and the caller's
  `SetEncoding` cannot fail. It runs once from `restoreLog`, which only
  `restoreJournals` calls at startup. The previous review's byte-digest guard
  (`digestOf(p.File.SavedDigest())` against `mark.Hash`, the text hash against
  `base.Hash`, the same split in `logIsCleanOnDisk`) is untouched.
- **`pruneCompacted` is keyed consistently.** `a.compacted` is read only in
  `compactTick`, and `pruneCompacted` runs before that read walk; a live pane is
  retained because `Tabs.All` is the same set the tick then walks, and a closed
  pane cannot be read at all. The cost is one pass per `CompactInterval` on the
  idle tick.
- **The benchmarks build their claimed shapes.** `buildCompactedSession` writes
  n contiguous same-group appends, so the merge branch folds them to two pieces
  (base + one) and `Compact` afterwards is a no-op that still runs `allOrigins`
  over n and `pieceStable` twice; `buildUncompactableSession` gives each insert
  its own Begin/End, so every owner differs and nothing merges. `Agent`,
  `NewSession`, `NewDoc`, `Begin`/`End`/`Insert`, `Buffer().Len`,
  `Version`/`Compact` all exist with the used signatures.
- **`docs/BENCHMARKS.md` carries no compaction figure.** Every table cell is
  "pending host run"; the prose has no ns/us number.

### Findings (raw, with severity)

- **Low -- the retired-row guard was vacuous.** Fixed this pass by naming
  `retiredRowsWithFiles` and adding `TestRetiredRowPredicate`.
- **Low -- the typing-shape explanation was wrong.** Both `docs/BENCHMARKS.md`
  and `compact_bench_test.go` said plain typing reaches `Session.Insert` "with
  no `Begin`/`End`". `Pane.HandleText` does bracket one `InsertRune` in
  `File.Begin`/`End`; the per-keystroke grouping is real either way (a commit at
  depth 0 bumps a fresh group). Fixed the wording in both; the fixture was
  already correct.
- **Low -- the persistent-removals TODO premise was stale.** `removals.go`
  already had the status-line note and `reopenPendingRemoval`; the item still
  read "they surface only as a prompt ... no way to act". Retired to COMPLETED.
- **Low -- the docs-index TODO named three files that are now deleted.** Retired
  to COMPLETED.

### Test honesty

- Fault-finders: `TestJournalRestoreRecoversPreTailEncoding` sets the buffer's
  encoding to the default *before* the first base is written, asserts the log
  and its base carry no tail, and would come back UTF-8/LF without
  `restoredEncoding`; `TestAnnounceSurfacesFileWarning` would read an empty
  status without the `announce` assignment; `TestCompactTickPrunesClosedPaneEntries`
  would still find the closed pane's key. `TestRetiredRowsHaveNoFile` cannot fail
  on the tree (no retired rows), so the new `TestRetiredRowPredicate` is the
  exercised pin.
- The rebind's existing tests are fault-finders: all four `removals_test.go`
  presses of `ctrl+alt+d` would leave the prompt closed if the table still bound
  the old chord.

### Escalations (content/design, not decided here)

- **A shape-only external edit is still silently reverted.** `Base.Hash` is a
  text hash while `Written.Hash` is a byte hash, so a CRLF/LF, BOM or charset
  move with identical text matches the base and the log's recorded shape
  re-encodes over it. Filed in TODO as "A shape-only external edit is silently
  reverted on restore"; the smallest fix is a byte digest on `Base`, a journal
  format change.

## Feedback reconciliation review pass (2026-09-16, between-wave)

The wave reconciled `docs/TODO.md` → `docs/COMPLETED.md` (stale items retired
with code evidence) and `docs/AGENT-FEEDBACK.md` → TODO coverage (26 untracked
findings). This pass verified the retirements by reading the code, filed the
26, resolved the flags the wave left open, and fixed the drift it exposed.
Read-only evidence: the container has no Go toolchain, so nothing was built;
the host `make check` is the gate and was green for the wave.

### Retirements verified by reading

- **The two hover parents are real.** `internal/hover/hover.go` `inline` has
  the flanking guard for `*`/`**` (`wordByte` on both sides of the marker) and
  the `_`-identifier guard, so `2 * 3 * 4` is left as written; `fenceOpen`
  returns `trimmed[:fenceRun(...)]` and `markdown` closes only on
  `strings.HasPrefix(trimmed, fence)`, so a four-backtick fence is not closed
  by three and a code line beginning with three does not close it.
- **The scroll behaviour is real.** `Panel.Scrollable`/`Top`/`Handle` claim
  `keys.LineUp`/`LineDown`/`PageUp`/`PageDown` only while the last `Render`
  showed fewer rows than it had; `scrollBy` clamps; the caret never moves.
- **The three "already in COMPLETED" items are there.** `revert` (the `revert`
  entry plus `Authored-text cleanup`), the completion `textEdit` (the
  inlay-hints entry: "completion `textEdit` plus `additionalTextEdits`"), and
  the review next-hunk jump (the review-flow chords and one-jump-path entries).
  Removing them from TODO was right.

### The 26 findings

Filed into TODO's "Agent feedback — actionable" dated notes: the port-squat
connect timeout; the connection-scoped restart marker; the undefined Stream
A/B/C/D labels; the `whoami -as` suspected bug; the missing `ping` verb; the
proposal tint; `braceTally` fence counting (with its fixture note folded in);
the `TestEveryVerbHasACode` omissions; cursor remapping across a revert; the
two verb-surface design passes (explicit write target; verb consolidation);
`run -prog` unmapped payload paths; the vacuous symlink-escape assertions;
`markInvalid`'s O(ops²) `Groups()`; the two encoding-classifier edges.

Not filed: item 7 (`-new-file` brief discipline — already in the skill), item
17 (mid-line two-row split — fixed by the `view.Build` merge, COMPLETED), item
21 (`header.go` forward-compat comment — already corrected, COMPLETED), item 26
(`hQuery` is a `prog.Writer` record — a next-brief wording note, not editor
work). Item 8 (`braceTally` fixture discipline) is folded into the `braceTally` TODO
bullet; item 18 (`sessionTopFor`) is folded into the D2b consumer list. Item 12
(reconciler arity) was fixed in `docs/REVIEW-AGENT.md` rather than filed; items 22 and 25 were one-line comment corrections in
`internal/app/control_test.go` and `internal/app/app.go`.

### Flags resolved

- **Encoding and compaction cores recorded.** COMPLETED had the follow-ons but
  no dated core entry for either; added ("Encoding core", "Compaction core").
- **The two superseded `## Hover` bullets** carry a dated supersession note now.
- **Editor-side identity durability retired.** `Registry.lowestGone` recycles a
  gone id at the cap (`TestGoneIDsAreRecycledAtTheCap`), `Seed` restores rows
  across a restart, and `who -live` filters; the only remainder (dump snapshots
  keyed by identity) already has its own TODO item, so the swarm-blocker
  narrative was no longer true and was removed.
- **"The user's save is all-or-nothing, but no longer silent" retired.** The
  save-time review popup is host-verified and the review-flow chords (Wave 2)
  landed, so nothing in the bullet remained open.

### Drift fixed

- `docs/COMPLETED.md`'s dangling `run -prog` payload-paths TODO pointer now
  resolves (the item was filed); the mid-line row note in this file (2026-09-15
  D2b) no longer claims a TODO item, because the fix landed.
- TODO's "superseded or already tracked" list no longer calls the `read -lines`
  report fixed (the byte-span half is open), drops the built `find`/file-listing
  verb, and carries `/tmp/opencode` once.
- TODO's verb-surface audit item now states what landed and points at the two
  remaining design-pass items.

## LSP L0–L4 campaign review pass (2026-09-16, between-wave)

Read-only; the container has no Go toolchain, so nothing was built or run and
the host `make check` remains the gate. The five-wave LSP campaign (L0–L4,
~20 features) ran with no review, and most sets are still `proposed`, so the
tree could not be treated as settled. `raj ctl lsp diagnostics` was run on
every touched file and on `internal/control/control.go` so cross-file
references resolved. Diagnostics reported parse errors and undefined symbols,
but the session buffer is the union of overlapping proposed edits, so a read or
diagnostic there describes that union, not necessarily the save composition;
conclusions about what a save writes come from reconstructing
`Project(AcceptedOnly)` out of the annotated runs and the group states.

### Findings (raw, with severity)

- **BLOCKER: the invalid sets are not dead; they hold unique feature text.**
  `Invalid` is computed from `projectMember`; when many agents insert at the
  same anchor the earlier insertion is marked invalid even though its bytes are
  physically present. Reconstructing the agreed composition as the non-invalid
  runs shows declarations living only in invalid runs: in
  `internal/keys/action.go` the `References`, `Complete` and `SignatureHelp`
  declarations sit only in invalid sets 2/3/5 (byte runs 4975..5005,
  4273..4527, 5005..5502), and `internal/keys/table.go`'s `SignatureHelp`
  binding only in invalid set 5. A save accepts `Pending()` (proposed with a
  surviving run) and drops every invalid set, so the saved `action.go` would
  not declare `References`/`Complete`/`SignatureHelp` and `app.go` would not
  compile. Do NOT clear these; only the user can accept the invalid groups, or
  they must be re-derived.
- **`clear` is wedged for every dead set.** Nine invalid sets render no
  physical run at all (pure insertions a later set replaced): `lsp.go` 3/8/13,
  `app.go` 4/7, `picker.go` 4/6, `app/control.go` 2, `KEYBINDINGS.md` 2. Each
  rejects cleanly but `clear` refuses, naming a collider
  (`lsp.go` g8 blocked by g13, g13 by g19; `app.go` g4 by g5, g7 by g8;
  `picker.go` g4 by g14, g6 by g23; `control.go` g2 by g8; `KEYBINDINGS.md` g2
  by g3). Byte deltas were 0 for every file, so the sets were left rejected.
- **Parse errors in the session text.** `internal/control/control.go` has a
  duplicated `Hints` field on one line (`...omitempty"` + tab + `Hints ...`;
  gopls `expected ';', found Hints`), a missing `LSPResult.Signatures` field
  (the doc comment is truncated mid-sentence) and a stray `Col int ... }`
  fragment before `LSPItem`; `client.go` then fails on `LSPResult.Symbols`.
  `internal/control/cli.go` reports `expected '}', found 'case'`.
  `internal/app/lsp.go` reports `expected declaration, found "didSave"` and the
  read around `clientCapabilities`/`capabilityGap` shows a spliced function
  tail. The editor syncs `File.Text()` to gopls, so if the union is what a save
  would write the host gate fails. Reported, not fixed.
- **`applyDocEdits` (rename) bypasses the lease pre-check.** `applyServerEdits`
  (`internal/app/server_edits.go`) checks `EditLeased` on every span before
  applying and refuses the whole batch naming the set; `applyDocEdits`
  (`internal/app/rename.go`) does not, so a rename whose edits touch a
  pending/rejected/invalidated run partially applies — the leased spans are
  refused at the `File` layer and the rest land — with no status word.
- **KEYBINDINGS.md is missing `run_code_lens` and `follow_link`.** The rows it
  has follow table.go's order; those two, added late in the campaign, are
  absent, so the hand-edited file cannot be trusted. Regenerate on the host.

### Verified by reading

- **Stale-data guards all pin a version and a generation.** Semantic tokens
  (`semantic.go`: `semanticVersion` + `semanticGen`), code lenses
  (`codelens.go`: `lensVersion` + `lensGen`), formatting (`format.go`:
  `docVersion` + `lspGen`), on-type formatting (`format.go`: `docVersion` +
  `onTypeGen`), and format-on-save (`willsave.go`: `pendingWrite.version` +
  `saveGen`, plus a proposed set arriving mid-request re-routing the save back
  through review). No path applies a stale answer at shifted offsets.
- **Capability gates are per-feature.** `jump.go` maps declaration/type
  definition/implementation to `DeclarationProvider`/`TypeDefinitionProvider`/
  `ImplementationProvider`; `format.go` maps document formatting to
  `DocumentFormattingProvider` and range formatting to
  `DocumentRangeFormattingProvider`, with `needsRange` driving the selection
  requirement. `TestSiblingJumpCapabilityGates` pins the three jump providers.
- **`workspace/applyEdit` refusal and `workspace/configuration` nulls read as
  intended** (`lsp.go` `handleServerRequest`): applyEdit answers
  `applied:false` with a reason, configuration answers one null per requested
  item in order, `window/showMessageRequest` dismisses with null, and unknown
  requests get `-32601`.

### Not reconciled

- Every file with a unique invalid set: `internal/app/lsp.go` (17 sets),
  `internal/app/app.go` (3), `internal/picker/picker.go` (6),
  `internal/app/control.go` (4), `internal/keys/action.go` (4),
  `internal/keys/table.go` (1), `internal/app/lsp_test.go` (1). Clearing them
  would delete unique text and accepting them is the user's gesture. The union
  buffers also carry the parse errors above, so "accept everything" would save
  the union, not a coherent file: the campaign needs a deliberate
  reconciliation, not a bulk accept.

### Campaign close — landed and repaired (2026-09-17)

The campaign is host-verified and the tree is saved: `go vet`, `go build`,
`go test ./...` and `make check` are green. L0–L4 landed the feature list
recorded in `docs/COMPLETED.md` ("LSP campaign L0–L4 — landed and repaired
(2026-09-17)"). A repair wave then fixed twelve syntax/type defects across
`internal/control/{control,cli,client,cli_test,host_test}.go`,
`internal/app/{lsp,control,app}.go` and `internal/keys/action.go` — fused
lines, a duplicated `Hints` struct field, a doubled `entry` struct, an early
`)` closing a `Mode` const block, and a `clientCapabilities` body spliced onto
another function's tail — restored the lost `References`/`Complete`/
`SignatureHelp` declarations, and corrected three mis-calibrated tests. The
review pass above found the defects by reading the unsaved union and by
`lsp diagnostics`; nothing in the container could compile, so this close is
written from the host-verified result and the repair wave's report, not from a
run in the container.

### Failure modes for the next campaign (2026-09-17)

Raw, dated and kept for the next campaign. Provenance: items 1–5 and 7 come
from the review pass above; 6, 8, 9 and 11 from the campaign and repair-wave
reports; 10 was observed while writing this record; 12 and 13 come from the
2026-09-17 regression-fix session, host-green (`gofmt -w && go test ./... &&
make check`).

1. **A save silently drops invalid/superseded change sets.** The text stays in
   the buffer but is excluded from `Project(AcceptedOnly)`, so `cmd+s` can
   write a file that does not compile while the buffer still shows the text —
   `internal/keys/action.go`'s `References`/`Complete`/`SignatureHelp`
   constants existed **only** in invalid runs.
2. **A permanently dirty buffer looks clean.** `buffers`' `pending` excludes
   invalid sets, so `internal/app/lsp_test.go` reported `pending=0` while no
   save could clear it.
3. **No disposal gesture.** `clear` wedges naming a collider (`clear g8`
   blocked by g13, g13 by g19, …), so an invalid set cannot be resolved in one
   step.
4. **Overlapping advisory landings fuse lines** (the twelve defects above).
5. **Test coordinate conventions drifted.** Three tests asserted 1-based
   line:col from the 0-based `File.LineCol` while sibling tests with identical
   fixtures asserted 0-based; the source could not be changed without breaking
   the compiler-paste path that pins 1-based `Picker.Position`. Fix: use
   `cursorLine`/`cursorCol`. No compile catches this.
6. **A timing bug passed every reading-level check.** `parkSave` posted its own
   `ui.Wake`, so the parked answer was consumed before the keystroke that
   should have invalidated it — the version pin existed and could never fire;
   the request goroutine must wake instead.
7. **No workspace-wide diagnostics check.** Seven files were broken while
   per-file `lsp diagnostics` read `ok`; the command's JSON field is
   `diagnostics` (a check filtering `.items` silently counts nothing — that
   mistake was made twice).
8. **Environment.** The container was started before the image rebuild, so the
   baked `raj ctl` refused campaign-era `lsp` subcommands with a pre-campaign
   mode list and the baked skills were stale; and the container's `/work` is
   empty, so any check that greps `/work` inside the container reads nothing.
9. **Orchestration.** "Orthogonal agents" was wrong (L0's two agents both
   needed `picker.go`); across five waves the same four files were edited by
   nearly every agent. Per-wave ownership of hot files would have prevented
   most of 1–4.
10. **Unsaved proposals do not survive an editor restart** — proven today
    (2026-09-17) when two docs agents' edits to
    `COMPLETED.md`/`AGENT-FEEDBACK.md` vanished while a third file's survived.
    This record is their redo; it must be accepted and saved (`cmd+s`) soon.
11. **Not implemented, by decision or deferral.** `semanticTokens` range/delta,
    refresh requests, `codeAction/resolve`, `codeLens` lazy resolve,
    `documentLink` rendering/non-file targets, `signatureHelp` typing
    triggers, on-type multi-cursor, dynamic registration beyond
    `workspace/symbol`, a format-on-save setting, trace/progress,
    `workspace/applyEdit` (refused).
12. **A shortening undo took the process down.** `Pane.line`
    (`internal/editor/pane.go:263`) served rows out of `p.disp`, the display
    projection built for the pre-edit, longer text. Undo appends its reversing
    ops and `Pane.history` (`internal/editor/actions.go:199`) runs
    `FollowCursor` on the same keystroke, before the frame's `UpdateDisplay`
    (`internal/app/render.go:317` is its one production caller), so a consumer
    sliced the freshly shortened line with the stale `hi` and panicked on the
    event thread. A non-nil projection exists whenever the buffer has any
    decision (`AcceptedAndProposed` is projected), so an ordinary buffer holding
    a pending agent proposal was enough; backspace/delete reached the same
    slice. Fixed 2026-09-17 by clamping `hi`/`lo` in `Pane.line`; pinned by
    `TestUndoOfAShorteningEditClampsTheStaleProjection`
    (`internal/editor/display_test.go:554`).
13. **Copy in one file did not paste into another.** The internal `Clip`
    carried piece records from the source store (`PieceRec{Buf, Start, Length}`,
    `internal/piecetable/flatpieces.go:124`) and `PasteClip`
    (`internal/editor/clip.go:126`) reused them across files, so the foreign
    records named out-of-range bytes in the destination store and nothing — or
    unrelated bytes — inserted; same-file paste worked, which read as "copy is
    broken". Copy also emits OSC 52, so the system clipboard was fine. Fixed
    2026-09-17 with `Clip.Source`/`Clip.Gen` and `Clip.internalTo`
    (`internal/editor/clip.go:33-48`), with `Reload` bumping `docGen`
    (`internal/editor/reload.go:62`); a foreign or stale clip now falls through
    to `Text`. Pinned by `TestClipDoesNotCrossFiles`,
    `TestClipDoesNotCrossReload` (`internal/editor/clipround_test.go:98`, `:126`)
    and `TestPasteAcrossBuffers` (`internal/app/clip_test.go:118`).
    `internal/editor/clip.go` was not touched by the LSP campaign, so this is
    most likely a pre-existing limitation of the internal path.

## Whole-line paste + sidebar inset wave — review pass (2026-09-17, between-wave)

Read-only: the container has no Go toolchain, so nothing was built or run; the
host `gofmt -w && go test ./... && make check` is green for the saved wave and
stays the gate. `raj ctl lsp diagnostics` returned `ok` on every touched file
(`internal/editor/clip.go`, `clip_test.go`, `clipround_test.go`,
`internal/app/layout.go`, `render.go`, `pointer.go`, `menu.go`, `panes_test.go`,
`menu_test.go`, `internal/ui/host.go`, `native.go`, `fake.go`,
`present_resize_test.go`) and on the package peers that reference the changed
symbols (`internal/editor/{file,pane,edit,cursors}.go`,
`internal/app/{app,app_test,wheel_test,pointer_panes_test,render_test}.go`,
`internal/ui/screen.go`, and the explorer, search and problems panes). The wave
was accepted and saved, so `proposals` and `groups` were empty and there was
nothing to dispose of.

### Friction reported by this wave's implementation agents (raw)

- **`search -json`'s `byte_start`/`line_start` disagree on a match with a
  leading tab.** Observed live: `line` is a 1-based line number, `line_start`
  is the byte offset of the line start, and `byte_start` is the byte offset of
  the match, so `line_start` includes the tab and `byte_start` does not; the
  pair reads like a row range. The struct comment
  (`internal/control/control.go:322`) already says `line_start` is a byte
  offset and the skill says to take offsets from `search` only, so this is a
  naming trap rather than wrong data. Filed in TODO as a naming decision.
- **`groups -mine` answered nothing while `raj ctl proposals` listed the
  author's sets.** `groups [path]` is buffer-scoped: with no path and no focused
  buffer it refuses `no open buffer for that path`, so a driver that just saw
  workspace-wide proposals can read the empty answer as "no sets". Possible
  verb-surface gap; filed in TODO.
- **An `apply` insertion at a line start needs an explicit trailing blank line
  to keep declarations separated.** The text lands flush against the next line
  when the inserted payload does not itself end in a newline. This is the
  whole-line anchor rule in RECURSIVE-RAJ section 5 seen from the insertion
  side; a discipline note, not a verb bug.
- **`buffers -json` is a top-level array, not an object.** Correct and already
  documented (the empty case is pinned in COMPLETED, 2026-09-15); recorded here
  only because it cost a jq query.
- **`groups`/`diff` report only pre-edit line ranges; there is no post-edit
  range.** Already tracked as "Groups carry rebased ranges" in TODO; no new
  item.
- **`search` prints `no matches; "..." contains regex metacharacters — retry
  with -regex` on a zero-match literal.** The sentence reads as a rejection in
  the reports even though it is only a hint (the change landed 2026-09-17,
  COMPLETED). The wording could say the pattern was searched literally; low.

### Findings from this pass (raw, with severity)

- **Medium — a terminal resize no longer reached the host's erase. FIXED in
  this pass.** The `ui.Resize` handler calls `a.host.Invalidate()`
  (`internal/app/app.go:1151`), but `Run` draws after every event and `Draw` saw
  a changed layout, so it called `a.host.Repaint()` (`internal/app/render.go`),
  whose `erase=false` (`internal/ui/native.go:110`) cancelled the clear. `Draw`
  is now size-aware: it takes the `Invalidate` branch when `cols`/`rows` differ
  from the last frame (zeros on the first frame) and `Repaint` only for a
  same-size layout change; the new `lastCols`/`lastRows` fields carry the size
  and subsume the old `lastLayout == (Layout{})` first-frame guard. The test gap
  that hid it is closed too: `ui.FakeHost.SetSize` moves the reported size and
  `TestResizeInvalidates` now asserts `Invalidations` rose while `Repaints` did
  not.
- **Low — `TestWholeLinePasteFillsFinalEmptyLine` is a guard, not a
  fault-finder.** Pasting at `File.Len()` with the old `spliceAtPrimary` inserts
  the same `"one\n"` at the same offset, so the test passes with and without
  the linewise dispatch. `TestWholeLinePasteFillsEmptyLine` (the middle empty
  line) is the discriminating sibling; the final-empty-line case only differs
  once the captured span already carries the newline, which is the earlier
  copy-round-trip fix.
- **Low — the first-frame `Invalidate` still has no test.** `Draw` clears the
  screen for the very first frame and Repaints only same-size layout changes,
  but no test asserts the first Draw produces an `Invalidate` and no `Repaint`;
  the resize and toggle tests both start after a setup frame. Filed under Tests
  and workflow.
- **Low — `TestResizeInvalidates` did not exercise the real resize path. FIXED
  in this pass.** The fake host's `Size` was not updated by `Handle(ui.Resize)`,
  so `syncSize` reverted the screen to the host size and the layout never
  changed; the test pinned the handler's `Invalidate` only. `FakeHost.SetSize`
  moves the reported size now, so the test drives the real size-driven layout
  change and asserts both the invalidate and the absence of a repaint.
- **Low — a characterwise selection ending at a line boundary now pastes
  linewise.** The dispatch keys on `strings.HasSuffix(c.Text, "\n")` rather
  than on "this was a whole-line copy", so a single-span selection that happens
  to end exactly after a newline takes the linewise path too. It matches the
  brief, but it is a broader behaviour change than "a whole-line copy pastes as
  a line"; escalated as a design choice (carry a `Linewise` flag on `Clip` set
  only by the `lineCopy` path, or keep the text-suffix rule).

### Test honesty

- Fault-finders: `TestWholeLinePasteGoesBelowNotAtCaret`,
  `TestWholeLinePasteFillsEmptyLine`,
  `TestLinewisePasteCursorAtFirstNonBlank`,
  `TestLinewisePasteInternalMatchesExternal` (the `NewlinePiece` subtests,
  where the old splice inserted the line without its newline while the text
  path added one), and the extended `TestWholeLineCopyStillStoresNothing`.
- `TestResizeInvalidates` (rewritten this pass) is a fault-finder for the
  size-aware erase: under the old unconditional Repaint the repaint counter
  rose, which its second assertion catches; `FakeHost.SetSize` is the seam that
  lets it drive the real size change.
- Guards: `TestWholeLineCopyKeepsItsNewline` and
  `TestWholeLinePasteFillsFinalEmptyLine` (above); `TestLayoutChangeRepaints`
  is a real fault-finder for "a layout change repaints without clearing" but
  does not pin the first-frame guard; `TestRepaintWritesFullFrameWithoutErasing`
  and `TestInvalidateWritesFullFrameWithErase` are the host-level pins.
- `TestLinewisePasteInternalMatchesExternal` really builds both paths: cases
  1-6 capture the source line's newline (captured-newline branch), and
  `final source into empty line` and `final source pasted below` copy a final
  line with no trailing newline, so `haveNL` is false and the `NewlinePiece`
  path runs. The one uncovered combination -- internal, `empty && lineEnd ==
  Len`, `haveNL` false -- is unreachable within one file: a clip with no
  captured newline comes from a document that does not end in a newline, so
  that document has no final empty line to fill.

## In-file find & replace + backspace panic + sidebar pad wave — review pass (2026-09-17, between-wave)

Read-only: the container has no Go toolchain, so nothing was built or run; the
host `gofmt -w && go test ./... && make check` is the saved wave's gate.
`raj ctl lsp diagnostics` returned `ok` on every touched file
(`internal/editor/find.go`, `edit_test.go`, `autopair.go`, `autopair_test.go`;
`internal/app/app.go`, `render.go`, `pointer.go`, `panes_test.go`,
`layout.go`) and on the package peers that reference the changed symbols
(`internal/editor/{pane,edit,cursors,render,clip,proposals_test}.go`,
`internal/app/{mode,control,review,menu,app_test,control_test,render_test,mode_test,review_test,pointer_panes_test}.go`,
`internal/keys/{keymap,table,action,keymap_test}.go`,
`internal/widget/input.go`). The wave was accepted and saved, so `proposals`
returned no pending sets and there was nothing to dispose of. No test was run;
the panic path and every "would fail without" claim below are read from the
code, not observed.

### Friction reported by this wave's implementation agents (raw)

- **`raj ctl` roots at cwd, so a driver run from `/tmp` cannot address
  `/work/...`.** `inferMapper` (`internal/control/client.go`) takes
  `WorkspaceRoot(cwd)` as the local side, so from `/tmp` the map is
  `/tmp=<editorRoot>` and an absolute `/work/...` argument is neither rewritten
  nor under the editor's root — refused outside the workspace. `RAJ_ROOT_MAP`
  is the documented override; running from the mounted workspace root avoids
  it. Promoted to TODO (make root inference cwd-independent, or state the
  requirement).
- **`edit`'s read gate forces a `version` call per write.** Every `edit`/`apply`
  must base on a version the connection has read, so a multi-hunk splice costs
  a fresh `read`/`version` before each write even when the previous reply
  already carried the version. This is the gate working as designed (safety
  over round trips); recorded, not promoted.
- **`lsp diagnostics` gives per-file gopls errors but no package-wide signal.**
  A file can read `ok` while a peer or a test in the same package does not
  compile, and `go vet` findings never appear. The existing TODO sweep item is
  extended with the package-wide gap; the host `go test`/`vet` stays the only
  whole-package check.

### Findings from this pass (raw, with severity)

- **Medium — `Find.run`'s smart-case fold can shift byte offsets.** `hasUpper`
  only detects ASCII `A-Z`, so an all-lowercase query takes the
  `strings.ToLower` branch. The simple case fold is not
  byte-length-preserving for runes such as `İ` (U+0130, 2 bytes → `i`, 1) and
  `ẞ` (U+1E9E, 3 → `ß`, 2). The haystack is folded too, so a document
  containing one of these *before* a match shifts every later offset, not just
  a match that contains the rune; `replaceCurrent`/`replaceAll` edit at those
  offsets, so this is a wrong-bytes edit path, not only a wrong jump. Exact fix:
  fold
  ASCII-only (map bytes outside `A-Z` unchanged) or store each match's
  original byte length. Filed in TODO.
- **Low — find searches the session text, not the drawn projection.** `Find.run`
  reads `File.Text()` (every run, rejected included) while Edit mode draws the
  `AcceptedAndProposed` projection and Review draws `Annotated`; a term inside
  a folded rejected run still matches and `jump` moves the caret to a hidden
  offset. Replace is protected (a rejected run is leased, so `applyEdit`
  refuses); this is the same class as the open F3b-ii D2b item, not new here,
  but the replace path makes it worth naming.
- **Low — Review mode over-refuses `cmd+enter` in an open find bar.**
  `WouldEdit` returns true for `keys.LineBelow` unconditionally, but
  `Find.Handle` ignores it unless `replaceShown`, so with the replace row
  hidden and Review on, `cmd+enter` sets the read-only note although nothing
  would have changed. Exact fix: `case keys.LineBelow: return f.replaceShown`.
  Filed in TODO.

### Test honesty (new and changed tests)

- `TestFindReplaceRefusedByLease` is a fault-finder: `propose` runs `ApplyDiff`
  so `File.Text()` is `"user AGNT here"` and marks the run `Proposed`; the
  query `"AGNT"` matches exactly the proposed span `5..9`. A replace of `4`
  bytes at offset `5` is that same span, so `EditLeased` refuses it. Without
  the lease check the text would become `"user ZZZZ here"` and the
  `TakeLeaseRefusal == id` assertion would fail; the fixture really builds the
  pending run, not a coincidental match.
- `TestFindReplaceThroughKeybindings` is the fault-finder for the enter
  normalization: `Bind(Editor, "enter", None)` makes `Resolve` return
  `keys.None` + `"\n"`, and `widget.Input.Handle(None, "\n")` returns false, so
  without the normalization the buffer would stay `"one two one"`. The `esc`
  before `super+z` is load-bearing, not decoration: the open bar routes every
  key to `Find.Handle`, which does not handle `Undo`, so an undo issued first
  is swallowed. `TestFindReplaceAllThroughKeybindings` drives `cmd+enter`
  (global `LineBelow`, no editor override) and pins the one-undo restore.
- `TestFindReplaceRefusedInReviewMode` is a fault-finder for the bar gate:
  without `WouldEdit`, `Find.Handle(Confirm)` on the replace row would edit
  through the bar, which sits ahead of `reviewRefuses`; the test asserts both
  the unchanged text and the `"read-only in review mode"` status.
- `TestBackspaceOnEmptyDocumentDoesNothing` exercises the default-on
  `AutoPairs` via `paired(t, "")`; both neighbour bytes read `0`, so the old
  `pairs[before] != after` test could not tell "no opener" from a match and
  reached `applyEdit(-1, 2, "")`. The `expect, isOpener :=` form covers every
  non-opener byte, not just offset `0`.
- Guards/non-fault-finders: `TestFindRows` pins `Rows()` across closed/open/
  revealed/shift+tab/closed; `TestFindReplaceCurrent` and `TestFindReplaceAll`
  pin the document edit and match count; `TestFindTabRevealsReplaceRow` pins
  the reveal plus "tab did not indent". Ordinary newline insertion is guarded
  by the pre-existing `TestNewlineIndentsThroughTheKeyPath`
  (`press("enter")`), which is why the normalization cannot have broken the
  closed-bar path. This wave added no newline-in-a-closed-bar test; the
  pre-existing app test is the guard. One small gap: nothing types into the
  query after `shift+tab`, so the focus-returns-to-query path is covered only
  by `Rows()` staying 2.
- `Rows()`/`ClickAt(dx, dy)` consistency: `render.go` draws from
  `p.Find.Rows()` at its four sites, `pointer.go` hit-tests `ny < Rows()` then
  forwards `(ev.Col-l.EditorX, ny)` to `ClickAt`, `editorCell`/`beyondEdge`
  subtract the same count, and the only other `Find.Open` reads are cmd+f
  toggling and the bar gate. No stale one-row height maths remains.

### Arity checkpoint (read from the code)

- `pairs[before]` is now a two-value map read bound to `expect, isOpener`
  (`internal/editor/autopair.go:246`), the change's only new multi-return.
- `File.Delete` returns `bool` and the ignoring call in `applyEdit` is a
  statement; `File.EditLeased`, `Pane.TakeLeaseRefusal` and `File.Undo` are
  two-value and each changed call binds both; `Pane.leaseBlocks` returns `bool`
  and `replaceAll` branches on it; `applyEdit(pos, remove int, insert string)`
  matches both `find.go` call sites.

## Warm-on-save-as + explorer preview wave — review pass (2026-09-17, between-wave)

Read-only: the container has no Go toolchain, so nothing was built or run; the
host `gofmt -w && go test ./... && make check` is the saved wave's gate.
`proposals` returned no pending sets — the wave was accepted and saved — so
there was nothing to dispose of. `raj ctl lsp diagnostics` was run on every
touched file and its package peers and returned `ok` with no diagnostics:
`internal/app/{lsp,app,session,headless,control,pointer,menu,willsave,diagnostics,codeaction,rename}.go`,
`internal/app/{lsp,session,panes,control,preview}_test.go`,
`internal/tabs/{tabs,tabs_test,hittest_test,preview_test}.go`,
`internal/explorer/{pane,tree,pane_test,scroll_test,selected_test}.go` and
`internal/ui/style.go`. Diagnostics are cached and lag, and the container cannot
run `go vet`, so the cross-file and test-only conclusions below are read, not
observed.

### Friction reported by this wave's implementation agents (raw)

- **`/tmp/opencode` is not writable to the sandbox user** (root-owned 0755), so
  the LSP agent used `-text-file -` instead of a scratch file. `/tmp` itself is
  writable; only the pre-approved scratch dir is not. Harness environment, not
  raj: write the payload on stdin or under `/tmp`.
- **No compile/verify verb in the container.** The LSP agent noted there is no
  way to ask raj to build the tree; that is the deliberate no-toolchain
  boundary (the host gate is the check), not a missing verb.
- **`search`'s regex-metachar hint misleads on a literal miss.** A literal
  pattern containing regex syntax that matches nothing prints `no matches; %q
  contains regex metacharacters — retry with -regex` (`internal/control/cli.go`
  `hasRegexMeta`). When the literal really is absent, `-regex` is the wrong
  advice — it changes the semantics rather than fixing the query. Verified
  live: `-q 'a.b'` and `-q 'x*y'` print the hint; `-q 'func (a *App)
  warmSaved'` searches literally and prints nothing. Promoted to TODO.
- **`search -json`'s `line_start`/`line_end` are byte offsets while `text` is
  the trimmed line.** The preview agent read the three together and set two
  `apply` spans wrong (an extra `}` and a stray `.`), both caught only by
  re-reading. The skill states the semantics and TODO already tracks the naming
  fix; recorded here as evidence, deduped against that item.
- **`read -json` carries no `bytes`/`lines`; `version -json` does.** Verified
  live: `read -json`'s keys are `author`, `spans`, `text`, `version`; `version
  -json` returns `bytes` and `lines`. A driver that just read a buffer makes a
  second call for the count. Promoted to TODO.
- **`lsp diagnostics` takes one path.** Same shape as the existing
  workspace-sweep item; deduped, not re-filed.
- **`apply` names no resulting span.** A mis-set hunk lands silently and only a
  re-read catches it; a dry-run or a reply echoing the applied byte span would
  catch it immediately. Deduped against the existing `apply`/`edit` reply item.

### Findings from this pass (raw, with severity)

- **Medium — a duplicated doc comment in `internal/app/lsp.go` was fixed.**
  Two identical four-line blocks preceded `type lspAnswer struct` (lines
  899-906); the second copy was removed. Cosmetic, but the file is in this
  wave and `gofmt` will not remove it.
- **Medium — control `open` does not promote a preview.** `host.Open`
  (`internal/app/control.go`) focuses an already-open tab with `Tabs.Focus` and
  returns, without `Tabs.Promote`, so a socket `open` on the path currently
  held in the preview slot leaves it marked provisional and the next explorer
  arrow replaces the tab the caller just showed. Exact fix: call
  `h.a.Tabs.Promote(p)` in that branch, as `Tabs.Open` does for Enter. Flagged,
  not applied: it is adjacent to the queued quiet-reveal change and the user
  may want `open` to stay non-committal. Filed under that TODO item.
- **Low — `Tabs.Paths` is now production-dead.** Every session write goes
  through `SessionState` (`internal/app/session.go`), which iterates
  `Tabs.All()` itself; `Tabs.Paths` (`internal/tabs/tabs.go`) survives only in
  `preview_test.go` and duplicates the preview-exclusion rule. Retire it or
  make it the one copy of the rule. Filed in TODO.
- **Low — warm-on-save-as may not sync a pane the user has left.** `warmSaved`
  starts the server into `byID` but `for_` returns nil until the handshake
  finishes, so the immediate `syncDoc` is skipped; the eventual sync comes from
  `maybeRequestHints(a.Tabs.Active())` on the idle tick, gated on `InlayHints`
  (default true) and `ModeEdit`. A just-saved-as buffer that is no longer the
  active tab is clean and unopened, so `needsSync` leaves it alone and
  diagnostics wait for a hover/reopen — the symptom the change set out to
  remove, in the non-active case. Exact fix: remember the warmed path and sync
  it once on the next idle tick, regardless of dirty state.
- **Info — selecting a preview tab by click or tab-cycle does not promote it.**
  Only `Tabs.Open` and the explorer's Enter do. A user who clicks the preview
  tab then arrows again loses it. A design call, named for the user rather than
  decided.

### Test honesty (new and changed tests)

- `TestSaveAsWarmsTheLanguageServer` is a fault-finder. In `h.write(p, renamed,
  …)` the prior path is `p.File.Path` and the new path is `…/renamed.go`, so
  `renamed` is true and only the rename branch reaches `warmSaved`. `stubGopls`
  puts a `#!/bin/sh; exit 0` `gopls` first on `PATH`, so `exec.LookPath` finds
  it; the fixture is a real `.go` file so the language is `go`, and `for_`
  inserts the entry into `byID` under `s.mu` *before* returning, so
  `len(byID)==1` and `byID["go"]` are deterministic and do not race the
  `go s.start` goroutine (which never removes the entry). Without `warmSaved`
  the table stays empty.
- `TestOrdinarySaveDoesNotWarmAServer` exercises `renamed=false`:
  `h.write(p, p.File.Path, …)` passes the same path, and the test asserts the
  save landed (status contains "saved") so the empty table means the rename gate
  held rather than that the write failed early. It is the negative half of the
  gate.
- `TestDirtyPreviewIsPromotedNotDiscarded` dirties the pane the way the code
  checks: `dirty.InsertText("edit")` bumps the session version, so
  `dirty.File.ViewDirty()` (the method `installPreview` reads) is true. Without
  the dirty check `installPreview` would overwrite `panes[preview]`, the count
  would be 1 and `Contains(dirty)` false; the text assertion (`"editx\n"`) also
  pins that the promoted pane kept its content.
- `TestExplorerArrowPreviewsWithoutLeavingTheSidebar` asserts focus stays
  `FocusSidebar` after both the first and second arrow, the active pane is the
  previewed path, and `Tabs.Count()==1` after a second file — so a second tab
  or a focus jump fails it. `explorerFile` walks onto a file until
  `SelectedPath` says file and `Tabs.Preview()!=nil`, so it does not depend on
  the fixture's directory/file ordering.
- `TestOpenPreviewReusesOneSlot`, `TestOpenPreviewReusesAnOpenTab`,
  `TestOpenPromotesThePreview` and `TestCloseClearsThePreview` pin the slot
  semantics (append/replace/promote/forget) and all build through `New`, so the
  zero value of `preview` (which would mean index 0) never leaks in; no test
  constructs `Tabs` by literal.
- `TestPathsOmitsThePreview` covers `Tabs.Paths` only, but the same rule now
  lives in `SessionState` — the file the agent also had to change — with no
  app-level test. Added `TestPreviewIsNotSavedInTheSession`, which would see two
  saved tabs without the `p == a.Tabs.Preview()` skip.
- `internal/explorer`'s `TestSelectedPathReportsFilesOnly` /
  `TestSelectedPathWithNoSelection` build the state they assert:
  `selectedFixture` expands `pkg` and refreshes, so `selectEntry` finds both the
  directory and the file, and the no-selection cases set `Sel` to `-1` and past
  the end. Without the bounds and `Dir` checks the first would panic or pass a
  directory path on.
- Gap filled: the `previewFile` headless-adoption branch
  (`findHeadless`/`unregisterHeadless`/`PreviewPane`/`closeDoc`) had no test.
  Added `TestPreviewFileAdoptsAHeadlessPane`, pinning one tab, the same pane
  object, `Preview()==p`, the registry entry gone, and the adopted pane dropped
  by the next preview.

### Arity checkpoint (read from the code)

- `Tabs.OpenPreview` returns `(*editor.Pane, error)`; the only production call
  (`internal/app/app.go:495`) binds both.
- `servers.for_` returns `(*langServer, serverState)`; `warmSaved`
  (`internal/app/lsp.go`) binds both (`ls, _`), matching every other caller.
- `Explorer.SelectedPath` returns `(string, bool)`; the call site at
  `internal/app/app.go:1581` binds both.
- `Tabs.Preview`, `Tabs.Contains`, `Tabs.PreviewPane`/`Promote` are
  single- or zero-value and match their uses.
- `Explorer.Handle` still returns `(string, bool)`; `handleSidebar` binds both.

## Quiet-reveal + client-timeout + rename-lease wave — review pass (2026-09-17, between-wave)

Read-only: the container has no Go toolchain, so nothing was built or run; the
host `gofmt -w && go test ./... && make check` is this wave's gate.

### Friction reported by this wave's implementation agents (raw)

- **The quiet `open` broke a test that assumed it focuses.**
  `TestControlRenameCarriesANotYetSavedBuffer` typed into `h.Tabs.Active()`
  after `open -create`; with the reveal quiet the new tab is not active, so the
  test now focuses the pane it found by path. The assumption was the test's, and
  the fix is the honest one, but a behaviour change whose only failing test is
  one that assumed the old focus is worth recording.
- **The 10s answer deadline now covers `prog` and `lsp`.** `Client.Do` bounds
  every non-`recv` request; `run -prog` (which may run a long `exec` inside the
  program) and `lsp` (which may wait on a cold server) can exceed 10s and be
  reported as the wrong-process hang. Promoted to TODO as a decision.
- **`DoStream`/`DoExec` stay unbounded.** `search` and `exec` do not arm the
  deadline, so the misconfigured-port goal is only half met; promoted to TODO.
- **`search -json`'s `line_end` is the newline offset, not one-past.** The
  rename agent set `apply` spans from `search -json` and got a stray blank line
  because `line_end` points at the `\n`, so `-end line_end` leaves the newline
  inside the replaced range. `read -json` spans are one-past; the two disagree.
  Author 7's note records the byte-offset naming; this is the sharper root
  cause. Deduped against the TODO naming item.
- **`search` rejects a positional path; `version` takes one.** Several agents
  reached for `raj ctl search <dir> <pattern>` (refused) and `raj ctl version
  <path>` (accepted) and had to check `-h` each time. Same shape as author 7's
  "lsp diagnostics takes one path" note; deduped.

### Findings from this pass (raw, with severity)

- **Medium — the rename refactor read the caret after applying the batch.**
  `applyDocEdits` moved `head := p.Cursors.Primary().Head` below the new
  `applyServerEdits` delegation, but `ReplaceRange` sets the cursor as it
  replaces each span, so the offset was already in the edited document and
  `renameShiftOffset` shifted it a second time; the caret dropped to the
  lowest-start edit. Fixed in this pass (read `head` before the batch) and
  pinned by `TestRenameKeepsTheCaretNearWhereItWas`.
- **Info — `host.Open`'s headless branch calls `Tabs.Promote` after
  `announceIfHeadlessQuiet`, which is a no-op** (a headless pane is not the
  preview). Harmless, but the branch where Promote matters is the already-open
  tab loop above it.

## Bounded-diff + undo-path hardening wave — review pass (2026-09-17, between-wave)

Read-only: the container has no Go toolchain, so nothing was built or run; the
host `make check` is this wave's gate. `lsp diagnostics` read `ok` on every
touched file and its package peers.

### Friction reported by this wave (raw, deduped)

- **`read -json` has no `bytes`/`lines`, and `jq .text|length` is the wrong
  substitute.** The obvious workaround for the missing count counts runes, not
  bytes, so it silently under-counts any buffer with a multi-byte rune:
  `jq .text|length` on `docs/TODO.md` gave 20878 while `version -json` `.bytes`
  gave 20911. Use `version -json` for the count, not `jq` on the read text. Same
  gap as author 7's `read -json` note; the byte-vs-rune trap is the sharper
  part, promoted with the existing TODO item.
- **`/tmp/opencode` is root-owned and unwritable** (the agent used `mktemp`
  under `/tmp` instead); already recorded by author 7 for the previous wave —
  deduped.
- **`lsp diagnostics` `ok` is not a compiled signal.** Diagnostics do not run
  `go vet`, and a test-only or unregistered-file error stays invisible; already
  the workspace-sweep TODO item — deduped.

- **`diff`'s `old` side is not a coherent base for a multi-op set.** Reviewing
  the `live` rewrite, the `diff -json` hunks for one three-op set rendered as
  four hunks whose `old` blocks contradicted each other (one ended "the
  recursion is cheap"; the next began "It is iterative rather than recursive"),
  so neither could be trusted as the pre-wave text, and the set is +1445 bytes
  with `ops=3` while the diff showed four hunks. A reviewer cannot judge a
  rewrite from `diff` when `old` is stitched from different versions. Needs a
  decision: is `old` meant to be the single base, and what is it for a set whose
  members were recorded at different versions?

## Per-verb/idle client deadlines + diff work budget — review pass (2026-09-17, between-wave)

Read-only: the container has no Go toolchain, so nothing was built or run; the
host `make check` is this wave's gate. `lsp diagnostics` read `ok` on every
touched file and its package peers.

### Friction reported by this wave (raw, deduped)

- **A `$'...'` payload broke on an apostrophe and inserted a literal `\n`.**
  The implementation agent applied a `-text` body quoted with `$'...'`; the
  quoting lost at an apostrophe inside the comment being written, so the editor
  received the two characters `\` and `n` where a newline was meant, producing
  one very long line. Caught by re-reading the seam and replaced with a second
  apply. Use `-text-file -` (stdin) or a single-quoted body for text that
  contains an apostrophe, and always re-read the seam after an apply.
- **`apply -text-file -` keeps the heredoc's trailing newline.** Replacing only
  a line's content with a span that excludes the newline inserts the stdin
  body's final newline *alongside* the one the span left in place, so the line
  doubles its newline. End the replaced span through the newline (`-end` at the
  start of the next line), or strip the body's final newline. Sharper than the
  existing heredoc-quoting note, which did not name this.
- **`raj ctl search` matches case-insensitively.** Searching for `func diffLines`
  returned `func DiffLines` too (and the reverse), so a hit list cannot separate
  the two identifiers; read the exact spelling off the hit line rather than the
  query. Same family as the literal-vs-regex hint.

Deduped: the `read -json | jq .text|length` byte-vs-rune trap is already
recorded above (the previous pass's `jq .text|length` item), so it is not
repeated. The multi-op `diff` whose `old` side is stitched from different
versions recurred in this wave's `client.go` and `host.go` sets; the prior
pass's item already records it.

## Heartbeat frames — review pass (2026-09-17, between-wave)

Read-only: the container has no Go toolchain, so nothing was built or run; the
host `make check` is this wave's gate. `lsp diagnostics` read `ok` on every
touched file and its package peers.

### Friction reported by this wave (raw, deduped)

- **Never derive an `apply` span from `search -json`'s `text`.** `text` is the
  whole line, not the matched pattern, so an agent sizing a replacement from it
  sized past the match and left an extra brace behind; the pattern's `byte_start`
  and `byte_end` are the only safe range. Same family as the byte-vs-rune trap
  already recorded above, with the range-vs-match confusion as the sharper part.
- **A span that drops the closing brace of a composite literal applies cleanly
  but does not compile.** Replacing the `heartbeats: map[...]{}}` line removed
  both the map's `}` and the literal's `}`; `lsp diagnostics` caught it as
  "missing ',' before newline in composite literal" and the seam re-read
  confirmed it. Do not eyeball a brace at a span's end — run diagnostics before
  reading on.
- **`diff`'s `old` side is still not a coherent base for a multi-op set.** All
  three sets reported `1 moved`, and the `old` hunks overlapped each other; the
  prior pass's item already records it — deduped.

### Fixed in this pass

- **A package var a live ticker read was a `-race` hazard.** `heartbeatEvery`
  was mutated by tests while connection/server goroutines read it (and across
  tests, a ticker goroutine outliving the test that wrote it). It is now a
  `const` production default; each `connection` carries a `heartbeat` field set
  from a per-`Server` seam at creation, and `startHeartbeat` captures it before
  the goroutine starts. No test writes shared state.
- **The heartbeat ticker used a raw `go func()`.** A panic in it would skip the
  terminal-restoring cleanups `safe.Go` exists to run; switched to `safe.Go`.
- **`streamIdle` is a `const` too**, derived from the `const` default, so the
  coupling cannot drift through a global.

### Escalated (product decision)

- **A tick already inside `send` can enqueue a heartbeat after the final frame.**
  `stopHeartbeat` closes the done channel, but a tick past its `select` and
  inside `c.send` still reaches the out channel; the client returns on the final
  and ignores the stale id, so it is benign today. Decide whether to accept it
  (and document the guarantee as best-effort) or sequence the final against the
  tick.

## Line-index read-site sync + SQLite store Phase 1 — review pass (2026-09-17, between-wave)

Read-only: the container has no Go toolchain, so nothing was built or run; the
host `gofmt -w && go test ./... && make check` is green for the saved wave and
stays the gate. `raj ctl lsp diagnostics` read `ok` on every touched file
(`internal/editor/{file,layered_test}.go`, `internal/app/control_test.go`,
`internal/store/{store,schema,driver,store_test}.go`) and on the package peers
that reference the changed getters
(`internal/editor/{reload,pane,actions,proposals}.go`,
`internal/app/{control,review}.go`).

### Fixed in this pass

- **`INSERT OR IGNORE` ignored nothing: the bootstrap `schema` table had no
  uniqueness constraint.** `CREATE TABLE IF NOT EXISTS schema (version INTEGER
  NOT NULL)` plus `INSERT OR IGNORE INTO schema (version) VALUES (0)` appends a
  fresh row on every `Open`, because `OR IGNORE` has no constraint to trip; the
  `migrate` comment claiming the race's loser "inserts nothing" was wrong. With
  two rows of different versions, `SELECT version ... LIMIT 1` is also unordered
  and could read the stray 0 ahead of a future version, after which
  `UPDATE schema SET version = ?` would downgrade every row — the outcome the
  too-new refusal exists to prevent. Fixed by giving the table the same
  singleton shape as `session` (`id INTEGER PRIMARY KEY CHECK (id = 1)`,
  `INSERT OR IGNORE ... VALUES (1, 0)`, `SELECT ... WHERE id = 1`); pinned by
  `TestSchemaTableIsASingleton`. The store is wired into nothing yet, so no
  existing database changes shape.

### Findings (raw, with severity)

- **Info — the read-site `sync()` is defensive, not load-bearing on today's
  production paths.** Every session text mutation in package `editor` already
  goes through a `File` wrapper that syncs; the direct `Session` calls left in
  production are `SaveOver`'s accept/rollback decisions and `App.compactTick`'s
  `Compact`, none of which move text. `File.index()` pays off for the class of
  future caller that reaches the session directly, which is exactly what
  `TestLineIndexCatchesUpOnAnOutOfBandReversal` simulates. Worth stating so the
  guard is not mistaken for a re-fix of the 2026-09-15 bug.
- **Info — `TestControlReadLinesAndSearchAgree` and the per-line `check`
  assertions are characterization, not this wave's fault-finder.** `host.Read`
  translates `-lines` with its own `view.NewIndex` over the projection and
  `search` scans the buffer, and the test's `apply`/`reject` go through
  `File.ApplyDiff`/`File.RejectGroup`, which have synced since the 2026-09-15
  fix — so the test would pass without `File.index()`. The fault-finder is the
  editor test: with a direct `Session.ClearRejected`, `Lines()` reports 6
  against a four-line text pre-fix, and `checkIndexMatchesText` catches it by
  comparing every line's bytes, not the count.

### Escalated (product decisions)

- **An unknown settings `scope` is stored, not refused.** `SetSetting` accepts
  any non-empty scope (`ScopeUser`/`ScopeWorkspace` are documented as
  convention, not a closed set), so a typo like `"workpsace"` becomes a live
  scope that `Settings` returns faithfully. Decide: validate against the known
  scopes and refuse, or keep open and document.
- **Negative cursor/top are refused, not clamped.** `SetPosition` returns
  `errNegative`. A caller restoring a position from a shrunken document may
  prefer a clamp; decide whether the store validates or records.
- **`Close` returns the first call's error on later calls.** It is idempotent
  in that it never touches the database twice, but a caller that sees a
  non-nil error on the second call cannot tell whether that close just failed.
  Decide whether later calls return nil.

## Store in sessions, settings, live tab width — review pass (2026-09-17, between-wave)

Read-only: the container has no Go toolchain, so nothing was built or run; the
host `gofmt -w && go test ./... && make check` is green for the saved wave and
stays the gate. `raj ctl lsp diagnostics` read `ok` on every touched file
(`internal/app/{app,session,settings,headless,journal,control,deletion,rmdir,lsp,menu,codeaction,rename,willsave}.go`,
`internal/tabs/tabs.go`, `internal/editor/{file,reload}.go`,
`internal/session/session.go`, `internal/store/store.go`) and their test peers
(`internal/app/{session,settings,panes,control,app}_test.go`,
`internal/tabs/tabs_test.go`, `internal/tabs/hittest_test.go`,
`internal/editor/{indent_style,reload,layered}_test.go`,
`internal/session/session_test.go`, `internal/store/store_test.go`). `proposals`
returned none: the wave was accepted and saved, so there was no set to dispose.

### Changed in this pass

- Added `TestStoredTabWidthPinsDetection` (`internal/app/settings_test.go`):
  the stored-setting pin at launch had no app-level test (the `SetSetting`
  live path and the `Tabs`/`File` units did), and added a `TabWidthPinned`
  assertion to `TestNewKeepsBuiltInDefaults` so a default launch is pinned as
  leaving detection the winner.
- **`SetSetting` now applies the resolved settings, not the raw write**
  (`internal/app/settings.go`). After the store write, both scopes are re-read
  and layered (`defaults < user < workspace`) and the written key takes that
  resolved value, so a lower-scope write cannot beat a higher scope in the
  running app; a runtime write overrides a launch flag for this session, and
  the flag is layered over the scopes again on the next launch. A value that
  does not parse is persisted but left inert (`settingValueValid`), so it
  cannot pin or flip a live setting.
- **A non-positive explicit `--tab` is ignored** (`internal/app/app.go`,
  `internal/app/settings.go`). `NewWithOptions` only takes `o.TabWidth` when
  it is positive, and `tabWidthExplicit` only treats a positive flag as
  explicit, so the fallback stands and no bad pin is set.
- **A stale `session.json` is removed once the store holds a session**
  (`internal/app/session.go`), so a leftover from an earlier migration cannot
  linger next to the source of truth.

### Resolved from the Phase 1 store review pass (2026-09-17)

The three store escalations recorded in the previous pass were decided and
implemented in this wave, so they are no longer open:

- the settings scope is now a **closed set** (`ScopeUser`/`ScopeWorkspace`);
  `Settings`/`SetSetting`/`DeleteSetting` refuse a typo with `errUnknownScope`
  (empty keeps `errEmptyScope`), pinned by `TestUnknownScopeIsRefused`;
- negative cursor/top are still refused by the store, and the restore path
  **clamps** instead of relying on the store (`applyStoredPosition`);
- `Close` now returns nil after the first call, pinned by
  `TestCloseIsIdempotent`.

### Verified by reading (not compiled)

- **Migration.** `session.json` is removed only inside `PutSession(...) == nil`,
  so a failed write is re-migrated next start; `RestoreSession` short-circuits
  on `root == ""`/`NoRestore` before `loadSession`; `session.Decode` runs
  `validate`, so deleted files are dropped and cursor/top clamped; a
  `state.Session()` error falls back to `session.Load` without writing.
- **Positions.** Every drop path records first (`closeTabAt`,
  `host.Close`/`CloseDiscard`, `closeDeletedPane`, `dropHeadless`,
  `evictHeadless`, preview reuse); `openFile` computes `alreadyOpen` before
  `Tabs.Open` and skips `applyStoredPosition` for a tab or the preview;
  `previewFile` never applies one.
- **Arity.** Each changed multi-return call was read against its callee:
  `store.Open`→`(*Store,error)`, `Settings`→`(map,error)`,
  `Session`→`([]byte,bool,error)`, `Position`→`(int,int,bool,error)`,
  `parseIntSetting`→`(int,bool)`, `parseBoolSetting`→`(bool,bool)`,
  `resolveSettings`→`(ResolvedSettings,[]string)`, `IndentFor`→`(Indent,IndentSource)`.
- **Tests.** The new tests are fault-finders, not characterization:
  `TestReopenRestoresClosedPositionButPreviewDoesNot` fails pre-wave because
  `closeTabAt`/`openFile` did not record/apply a position;
  `TestControlCloseRemembersPosition` fails without the control `Close`
  recording; `TestLegacySessionJSONMigratesToTheStore` and
  `TestSaveSessionWritesTheStore` fail with a file-only session;
  `TestSetSettingTabWidthIsLive`, `TestSetTabWidthOutranksDetectionOnOpen` and
  `TestSetTabWidthSurvivesRedetection` fail without the pin.
  `TestNewKeepsBuiltInDefaults` is the regression guard that `New` is
  unchanged for a store with no settings rows.

### Findings (raw, with severity)

- **Info — a settings read error is silent.** `settingScopes` discards the
  error from `state.Settings`, so a database that answers `Session` but fails
  `Settings` runs on defaults with no line on the status line, where a bad
  *value* does report. Same choice, different visibility; decide.

### Escalated (product decisions)

- **`.raj` is visible in the explorer from the first run.** The store opens
  eagerly, so `<root>/.raj` is created before anything is saved and the
  directory appears in the tree. The defaults hide `.raj/*` but deliberately
  walk `.raj` so `.raj/hidden` stays reachable, so the directory itself shows.
  Decide: hide `.raj` and lose tree access to `.raj/hidden`, or move the store
  to XDG keyed by workspace.
- **An explicit tab width overrides a file's detected indentation.** Previously
  `--tab` was only a fallback; now a flag or stored `tab_width` pins both the
  display advance and the indent unit and survives `Reload` re-detection, while
  a default launch still lets detection win. Confirm this is the wanted
  semantics, or revert the pin to a fallback.
- **The settings key set and the future menu's default scope.** The resolver
  knows exactly `tab_width`/`tabs`/`wrap`/`auto_pairs`/`inlay_hints` and leaves
  an unknown key alone (`SetSetting` refuses one), which keeps a newer build's
  key safe from an older one. Decide the final set, and whether the settings
  pane saves a change to the user or the workspace scope by default.

### Friction (tooling)

- **`/tmp/opencode` is pre-approved but not writable.** It is `root:root 0755`
  and the agent runs as uid 501, so the first `cat >` failed with permission
  denied; the pass fell back to a fresh `/tmp/oc-review`. The skill points
  agents at this exact directory, and the Phase 1 pass recorded the same
  symptom for subagents, so make it sticky (`chmod 1777`) or stop naming it as
  the scratch location.

## Settings pane + session sidebar + `.git` transients + `lsp diagnostics` batching — review pass (2026-09-18, between-wave)

Read-only for compile/test: the container has no Go toolchain, so nothing was
built or run; the host `make check` is this wave's gate and is green. `raj ctl
lsp diagnostics` read `ok` on every touched file
(`internal/app/{settings_pane,settings_pane_test,session,session_test,journal,layout,app,render,pointer}.go`,
`internal/keys/{action,table,settings_test}.go`,
`internal/session/{session,session_test}.go`,
`internal/control/{cli,cli_test}.go`) and on the package peers
(`internal/app/{settings,inlay,review,mode,menu}.go`,
`internal/keys/{keymap,commands,doc}.go`,
`internal/control/{host,client,control}.go`). The verb was driven live over the
socket: the multi-path sweep prints one `-json` status per path, a good path
beside a missing one exits 1 with the good status still printed and the missing
one on stderr, and single-path and no-path are unchanged. The `.git` and
session semantics were reviewed by reading and by the tests, not exercised live.

### Friction reported this wave (raw, deduped)

- **The model pin broke subagent spawning.** `opencode/agents/{raj,review}.md`
  and `opencode/opencode.json` named `opencode-go/deepseek-v4-1-flash`, which
  the provider rejects, so a subagent could not be spawned at all; repinned to
  `deepseek/deepseek-flash`. The failure surfaced only at spawn time, not at
  config load.
- **The image bakes the agent config, so repo edits did nothing until the image
  was rebuilt.** Editing `opencode/agents/*`, `opencode/opencode.json` or
  `plugins/raj-gate.ts` in the repo changes nothing the running opencode reads;
  the installed copies are under `~/.config/opencode/`. Rebuild the container
  image (not just the Go binary) when this config changes, and verify the
  installed copy, not the repo one.
- **Two stale containers held the volume.** A previous run's container still
  existed; the new one could not take the workspace until both were removed.
  Same family as the existing container-lag TODO item.
- **`claim` without `-add` replaces the set.** Already a TODO item under
  "Tests and workflow"; recorded here as hit this wave. Re-claiming by path
  silently dropped the previous set.
- **A claim outlived its gone identity.** `raj ctl claim` warned that
  `internal/app/session_test.go` was still claimed by `raj-7d0cf365`, which
  `who` reports `gone`; the write was still allowed because the file was in my
  set, but a claim the owner can no longer clear (`claim -clear` clears only
  the caller's set) is stale state the next writer cannot tidy. Decide whether
  a gone identity's claims should expire.

### Fixed in this pass

- **`TestGitTransientIsNotPersisted` ran with the journal gate off, so its
  journal half was vacuous.** `newHarnessAt` never sets `RAJ_JOURNAL`, so
  `appendJournal` returned at `!journalEnabled()` before the `.git` check and
  the log-directory scan could never see a log for any path; it also `return`ed
  early when the logs directory was absent. The test now sets `t.Setenv(JournalEnv,
  "1")` and asserts directly that no log file exists for the transient, which
  fails without the `isGitPath` guard in `appendJournal`.
- **Added `TestRestoreSidebarTriState`** (`internal/app/session_test.go`): the
  app layer pinned only the closed case, so nil keeping the explorer default, a
  named pane selecting, and an unknown name leaving the pane alone were
  untested. `session.validate` re-derivation preserving `Sidebar` was confirmed
  by reading (`out := State{Version: st.Version, Focus: st.Focus, Sidebar:
  st.Sidebar}`).

### Findings (raw, with severity)

- **Low — `settingsPane.ClickAt` lacks `Render`'s `w < 8 || h < 2` guard.** A
  click in a squeezed sidebar can activate a row that was not drawn; the
  `minSidebarRows` floor makes it hard to reach. Filed in TODO with the missing
  click test.
- **Low — the escape-close of the settings pane does not `TouchSession()`.**
  `handleSidebar`'s `SidebarSettings` `Cancel` branch (and `clickSidebar`) set
  `a.sidebar`/`a.focus` without marking the session dirty, where
  `openSidebar`/`toggleSidebar` do; the exit save covers it, but a hard kill
  inside the 3 s window loses the close.
- **Low — the `settingsPane.Handle` `keys.Cancel: return true` branch is
  unreachable in production.** `handleSidebar` intercepts escape before
  delegating, so the pane's close lives in the app; the duplicate is dead code
  (or the sign to move the close into the pane).
- **Info — `App.Settings()` can drift from the live app default after the
  `cmd+alt+w` wrap chord.** `keys.ToggleWrap` sets `a.WrapDefault` (and the
  active pane) but not `a.settings.Wrap`, so the pane's "Wrap lines" row shows
  the stored value until the next `SetSetting`. `ToggleInlayHints` is
  deliberately per-pane and does not have the problem. Decide whether the chord
  updates `a.settings` or the pane reads the live defaults.
- **Info — `settingOrigins` ignores an explicit launch flag.** It layers only
  the two store scopes, so a `--tab`-pinned value whose scopes are empty is
  annotated `default` even though the flag won. The margin only; the displayed
  value is right.
- **Info — the repo plugin source is `plugins/raj-gate.ts` but the config
  references `./plugin/raj-gate.ts`.** The running install resolves (the ledger
  file is live), so the extra `s` is a source-vs-install naming trap rather than
  a break; align the two names when next touched.
- **Info — a multi-path diagnostics sweep has no per-path attribution.** `-json`
  prints one bare status object per path in argv order and the plain form
  streams with no `==> path` header, so a mixed sweep needs the caller to count
  positions. Consider a per-path header or a path field.

### Escalated (product decisions)

- **Is `cmd+comma` reclaimable?** The table binds it deliberately (a `kkp_on`
  line plus a note) but `internal/keys/macos_test.go` lists it under macOS
  reserved chords — spelled `super+comma`, which never matches the bound
  `super+,`, so neither `TestNoMacOSSystemShortcuts` nor
  `TestTerminalDefaultsAreAcknowledged` sees it. Decide: treat cmd+comma as
  reclaimable (fix the key to `super+,`, drop it from the reserved set, keep the
  terminal note) or move the chord. Filed in TODO.
- **The settings key set is now the open half only.** The pane defaults writes
  to the workspace scope, which settles the default-scope question; the key set
  (and the future LSP section the pane sketch reserves a place for) remains
  open. Direction in TODO updated to say so.

## Wave A review pass — deferred deletions and ambiguous arrows (2026-09-18, between-wave)

Scope: `internal/piecetable/{groups,project,project_test}.go`,
`internal/editor/layered_test.go`, `internal/app/lease_test.go`,
`internal/ui/width.go`, `internal/ui/width_test.go`,
`internal/view/view_test.go`. The user had accepted and saved both items, so
`proposals`, `groups`, `diff`, `deletions` and `rmdirs` were empty and there was
no set to dispose of; the wave was enumerated from the brief file list.
`lsp diagnostics` on all eight files in one call returned `ok` for every path
(the only messages were severity-4 modernize hints, pre-existing), so the
cross-file symbols `deferredDeletions`/`deletionLeases` resolve in the package
and the test helpers resolve in theirs. Read-only verification: no host build.

- **The composition fixture builds the state it asserts.**
  `TestProjectDefersProposedDeletionUntilAccept` deletes one byte, marks it
  Proposed, and asserts `AcceptedAndProposed` keeps the bytes; without
  `deferredDeletions` that assertion reads "AC" and fails.
- **The gap lease fails without the walk at all three layers.**
  `TestLeasedNamesAPureDeletionSet` (span leased, flush insert/replacement
  allowed, accept ends it), `TestApplyDiffWarnsOverProposedDeletion` (advisory
  warning names the set once),
  `TestDeleteRefusedAcrossAProposedDeletion` (`File.Delete`) and
  `TestLeaseRefusesAWriteAcrossAProposedDeletion` (typed replacement) each
  depend on `deletionLeases`; a pure deletion previously owned no inserted run.
- **The arrow width is pinned through the view layer.**
  `TestColumnsCaretAdvanceMatchesDrawnArrows` goes through `view.NewColumns`
  and `ui.RuneWidth`, so it fails before the table change; the em-dash
  assertions keep the exception from becoming a blanket ambiguous-wide switch.

Raw findings; the actionable ones were promoted to TODO and the already-tracked
friction was not repeated here:

- **The deletion-only classification is written twice.** `deferredDeletions`
  and the head of `deletionLeases` recompute the same live-Proposed,
  no-insertion predicate, and `foldProjectOracle` calls `deferredDeletions`, so
  the fuzz oracle shares the predicate with production and cannot catch a wrong
  classification. Promoted to TODO (Layered proposals).
- **An invalid deletion-only set still lands as before.** `deferredDeletions`
  marks it, but the unapply can only restore removed bytes when the member's
  rebase succeeds; for an invalid member it does not, so the Phase 1c
  "undefined case" note is unchanged. Recorded, not decided.
- **New friction promoted to TODO:** `apply`/`edit` name the new version but not
  the set id; `lsp diagnostics`' multi-path reply prints a non-JSON cold-start
  line and one unframed JSON object per file; `read -json` echoes no byte span
  for a `-start`/`-end` or `-lines` read. The `-as`-after-the-verb placement and
  the literal-pattern regex-metachar miss are already in this file, and the
  latter extends TODO's existing hint item rather than adding a row.
- **`/tmp/opencode` is still root-owned 0755 to uid 501**, so the edit payloads
  went under a `mktemp -d /tmp/waveA.XXXXXX`. Recurrence of the container-image
  item; already tracked.

Escalated to the user rather than decided:

- The arrow exception is confirmed only on the terminal that drifted (Ghostty);
  a terminal drawing Ambiguous arrows narrow would regress the caret the other
  way. TODO Direction.
- Whether a Rejected deletion-only set deserves the same gap lease; the wave
  left `Rejected` unchanged deliberately. TODO Direction.

## Client-is-editor wave — between-wave review (2026-09-18)

Read-only for compile/test: the container has no Go toolchain, so nothing was
built or run; the host `make check` is the gate and this pass expects it green.
`lsp diagnostics` returned `ok` for all 34 touched Go files (one pre-existing
modernize hint in `internal/prog/prog.go`); the markdown docs have no language
server, so their diagnostics are unverified by construction.

Disposed: three provably no-op change sets left by same-author amendments
(`internal/prog/prog.go` group 1 and `internal/control/prog.go` groups 1 and 4,
all `bytes=0`, `moved=1`, each adding an `OpGen` argument op that no code uses —
the watch generation travels as header field `hGen` instead). Cleared after
`reject`; no buffer text changed. `control/prog.go` group 3 keeps a moved member
but still carries live work (removing `OpWatch` from `verbNames`).

Verified as a reader (no build): the snapshot round-trip fixture builds its
state from real `piecetable` calls (insert/Begin/delete/mark/reject/undo) and
the decode test uses real JSON; the watch park/wake tests dial real clients and
assert no answer before an edit and generation/version movement after `Tick`;
the client decision tests propose through real wire `control.Request`/`Hunk`
and assert daemon state, not the client copy. Each changed call's arity was
read against its callee.

Friction (raw, deduped):

- The internal searcher snapshot had to be renamed `searchsnapshot` because the
  new client `snapshot` verb took the name; a verb colliding with an internal
  op is a naming trap that is mechanical once seen and easy to miss before.
- `watch` is deliberately non-batchable like `recv` (it parks), so it is absent
  from `knownOps`/`verbNames` but present in the `names`/verb-code tables. The
  asymmetry is correct but reads as an omission; `prog_test.go` still said the
  table "ends at OpExec" (fixed in this pass).
- Two client connections are required: the watch parks one for the client's
  lifetime, so decisions need a second author/connection (and `clear` claims on
  it). Any future client-side verb must know which connection owns the document.
- `clear` needs the caller to `claim` the file first, even when the set is a
  provably no-op with nothing to reverse; a review pass disposing of another
  author's stale sets must claim files it is not otherwise editing.
- `groups` takes one path only, so sweeping the wave for moved/no-op sets took
  one call per file; `proposals -json` reports neither `moved` nor `ops`.
- A claim outlives a gone identity: `internal/prog/prog.go` and
  `internal/control/prog.go` were still claimed by `raj-4badff8c` (`gone`) and
  the clear warned about it. Recurrence of the stale-claim friction.
- `TestAttachPackageIsGone` reads the tree from disk, so it passes only once the
  pending `rmdir internal/attach` is accepted and saved.

Escalated to the user rather than decided:

- Client review-mode semantics: leaving Review is edit-refused in every mode, so
  the toggle changes only chrome.
- The watch generation hashes buffer versions, so a decision-only change
  (accept/reject/clear) may not wake it; the client refetches its own decisions
  explicitly, which hides the miss for itself but not for a second client.
- The snapshot omits `Session.depth`, so a snapshot taken mid-`Begin`/`End`
  loses the open undo transaction.
- The compacted-origin path is captured and restored but no test calls
  `Compact` before `SnapshotState`.

### Test-integrity misfires across the client/phone waves (2026-09-18)

Five host-gate failures in a row, all from a test that did not build the state
it claimed, or from a shape change whose consumers were not swept:

1. `TestClientRefusesTypingOutsideReview` — the client installed tabs but never
   took editor focus, so the typed rune reached the sidebar and the read-only
   gate never ran; the status assertion read a stale `EnterReview` message. The
   gate was verified by reading, not by driving the real key path.
2. `TestTickLeavesAnUnchangedQueueAlone` — the fixture moved `selected` without
   settling the code view, so the first tick legitimately re-synced; the
   steady-state assertion measured the wrong frame.
3. `TestPhoneChipCentresLabelAndFillsEveryRow` — panicked slicing `Screen.Row`
   to the chip width; `Row` trims trailing spaces, so an all-fill row is empty.
   The test used a raw row where the cell accessor (`At`) is the semantic one.
4. `TestRenderPhoneShowsSummaryAndDecisionRow` — still asserted the pre-drawer
   decision row after the drawer replaced it; no consumer sweep.
5. `TestOrdinaryProfileUpAndTabUnchanged` — the new drawer hook sat in the
   shared key path guarded only inside `drawerKey`, and the test relied on the
   harness's implicit focus/cursor; the ordinary profile is now gated at the
   call site and the test states its fixture.

Root causes, in order: fixtures that build an approximate state; a changed UI
surface with un-swept tests; assumed wire or accessor semantics; and
profile/mode hooks without a call-site guard. None was a reasoning failure
about the feature — all were verification failures about the *path* the test
drives, which the container cannot run.

Rules recorded in `docs/RECURSIVE-RAJ.md` §7: drive the real entry path and
assert its preconditions; sweep every reader of a changed type/function/UI
element; build fixtures from the real wire types; gate profile/mode behaviour
at the call site and inside the handler; assert through semantic accessors
where the surface trims; list each changed or confirmed-unaffected test and the
precondition its fixture constructs; one writer per file per wave.

## Between-wave review — attach mirror (2026-09-20)

Read-only pass over the landed "attach mirrors the daemon" wave (S1 + S1b + S2).
`proposals`/`groups`/`diff` showed no pending sets, so there was nothing to
dispose. Per-file `raj ctl lsp diagnostics` read `ok` on all eleven changed Go
files; a clean per-file reading is not a package check (no `go vet`, no
whole-package compile), so the host `make check` remains the gate. Findings:

- **`control.Buffer.Superseded` has no producer.** `host.Buffers()`
  (`internal/app/control.go`) sets `Pending` and `Moved` but never `Superseded`,
  so the sparse `hBufferSuperseded` (0x5d) field never emits and the
  `bufferMark.superseded`/`closedMark.Superseded` the wave added always compare
  zero — the fact cannot move. The `Superseded` count exists to say "no pending
  set, yet a save would drop text"; populate it from
  `Session.UnsavedProposed()` or remove the field and the mark component. Filed
  in TODO.md.
- **Multi-path `lsp diagnostics -json` loses the path.** The batch prints one
  bare `{"status":"ok"}` per operand, in order, with no path; a driver must
  attribute statuses positionally and cannot tell which named file a non-`ok`
  status belongs to. Filed in TODO.md.

- **The legacy closed-view test does not pin the legacy fallback.** `TestClientViewReadsLegacyClosedPaths` seeds `{"open":[],"closed":["<path>"]}` for a path the daemon also lists as a real tab, then asserts the client shows that one tab. With `closedMarks.UnmarshalJSON`'s legacy branch removed the unmarshal fails, `readClientView` returns false, and the mirror loop adds the same one tab — so the assertion holds with and without the code it names. A discriminating fixture needs a case where a failed parse and a legacy mark diverge: a closed path the daemon does not list as a real tab, or an assertion that the legacy path is not snoozed. `TestReviewGenerationStableWhenIdle` is probabilistic in the same family — with two removal keys, unsorted map iteration can read equal twice by chance, so the stability assertion only catches the missing sort about half the time. Filed in TODO.md.

## Between-wave review — flag spelling and daemon flag forwarding (2026-09-20)

Read-only pass over the landed flag-spelling/daemon-forwarding wave, plus the
two orchestrator-authored agent definitions left pending by design.
`proposals` showed no pending code change sets (the wave is saved); the eight
pending sets are all in `opencode/agents/raj.md` (6) and `review.md` (2), the
deliberate `-` to `--` conversion. Per-file `raj ctl lsp diagnostics` read `ok`
on all six changed Go files; a clean per-file reading is not a package check,
so the host `make check` stays the gate. Findings:

- **`--daemon` is not hidden, and cannot be via the standard `flag` package.**
  The init in `cmd/raj/main.go` guards `MarkHidden` behind an interface
  assertion, and `control.flagHidden` reflects over `flag.Flag`, but neither
  `FlagSet.MarkHidden` nor a `Flag.Hidden`/`hidden` field exists in released Go
  (checked the go1.25.0 source and the pkg.go.dev index through go1.27.1), so
  the assertion never fires and the helper can never return true. Live
  `raj --help` prints `--daemon daemon run`: the flag is visible, and the
  backquoted daemon-run text in its usage string has been taken by
  `flag.UnquoteUsage` as the value placeholder. The comment claiming a hidden
  alias is false. Escalated 2026-09-20; filed in TODO.md.
- **The editor `--help` lost its header.** `editorUsage` calls only
  `control.PrintFlagUsage`, so `raj --help` now opens on `  --attach` with no
  `usage: raj [options] [file|dir]` line where the default `flag.Usage`
  printed `Usage of <path>:`. Escalated 2026-09-20 (UX); filed in TODO.md.
- **`--q` contradicts the short-flag rule.** `PrintFlagUsage` prints every flag
  with two dashes, so `raj ctl search -h` says `--q string` while the
  hand-written `ctlUsage` says `search -q PATTERN` and the wave convention
  keeps single-letter shorts on one dash; the same backquote leak prints
  `--group groups` and `--dump dump` in the ctl help, where the value is a
  uint. Filed in TODO.md.
- **`flagDefaultText` drops the stdlib panic guard.** `flag.isZeroValue` wraps
  the zero `String()` call in `recover`; the new helper does not, so a custom
  `flag.Value` that panics on a zero receiver would panic the help path.
  Latent: no custom `Value` is registered today. Filed in TODO.md.
- **Single-dash long flags survived the conversion in live files** (fixed in
  this pass): `docs/CLAIM-SPEC.md` (`-clear`, two), `docs/TODO.md` (`-hidden`,
  `-active`/`-here`), and code comments in `internal/control/cli.go`,
  `cli_test.go` and `control.go` (`-json`). Historical COMPLETED.md,
  INVESTIGATIONS.md and older AGENT-FEEDBACK.md entries are deliberately left
  as written.
- **`claim --add` is correct.** `Guard.Claim` replaces the set by default and
  extends on `add` (`internal/control/host.go`), the CLI forwards `ClaimAdd`
  over the wire, and `TestCLIClaim` and `TestClaimSetAddClearReport` cover
  both; the usage line and the skill both say `--add` extends. The subagent
  report of a replacing claim was the documented default, not a bug; the same
  footgun (a bare `claim` drops the current set) cost this pass a retry too,
  so a warning or an `--add` default is filed in TODO.md.

Not compile-verified: the wave test changes and the pending agent-definition
conversion were read, not run. `TestStartForwardsNoRestore` (Runner/Spawn
fixture, asserts `--no-restore` in the child argv),
`TestControlForwardCarriesOnlyWhatWasAsked` (pure function, nil versus
`["--no-restore"]`), `TestFlagsFirstRefusesBareFlagName` (bare `no-restore`
refused, a real directory kept), `TestCLIRefusesBareFlagName` (CLI exit 2 and
the suggestion in stderr), `TestEditorUsageUsesDoubleDash` (prints
`--no-restore`/`--control-addr`/`--wrap`) and
`TestUsageNamesOnlyTheLiveControlFlags` (daemon usage names `--control-addr`
and `--no-restore`) are discriminating against their fixtures.
`TestEditorUsageUsesDoubleDash` is presence-only: it does not assert that any
single-dash form is absent, nor the `--daemon` hiddenness, nor the missing
header; and the `Restart` `--no-restore` forwarding has no direct test
(`TestRestartForwardsRecordedControl` passes false).

## Between-wave review — workspace roots as a set (Wave 2, 2026-09-21)

Read-only pass over the landed, inert Wave 2 (`internal/workspace.Roots`, the
`session.StateDir` delegation, `App.root` -> `App.roots`/`primaryRoot()`).
`proposals` was empty and every buffer read `saved` with 0 pending / 0 moved,
so there was nothing to dispose; the wave was enumerated from the brief and the
tree, since the container has no git checkout. Per-file `raj ctl lsp
diagnostics` read `ok` on both new files and every changed production file
under `internal/workspace`, `internal/session` and `internal/app`; a clean
per-file reading is not a package check, so the host `make check` stays the
gate.

Inertness, verified as a reader:

- **The state key is unchanged.** `sha256("/Users/rajandavis/Desktop/projects/raj")`
  is `89862ebb3e8c...` (openssl, in this container), so `StateKey` returns
  `raj-raj-89862ebb` and `session.StateDir` still builds
  `<stateHome>/raj/workspaces/<key>`. `session.StateDir` delegates to
  `ws.StateKey`, and the local `stateKey`/`slug` are gone from
  `internal/session`.
- **`TestStateKeyMatchesSessionStateDir` is a tautology.** Both sides are
  `workspace.StateKey` (`session.StateDir` calls it), so the test cannot fail
  for a wrong key algorithm; it only pins that StateDir's last path component
  is `StateKey`. `TestStateKeyGolden` is the real algorithm pin.
- **The single root is preserved.** `ws.New` canonicalises with `filepath.Abs`
  (identity for an already-absolute clean path), and production's `resolve()`
  always yields one, so `primaryRoot()` equals the old field. The
  construction-time consumers (`explorer.NewPane`, `search.NewPane`,
  `newServers`, `migrateState`, `picker.New`) still receive the constructor's
  `root` parameter directly rather than `primaryRoot()`; equal in production,
  a latent split only if a relative root ever reached the constructor.
- **No `a.root`/`h.root`/`h2.root` reads remain** under `internal/app`
  (case-sensitive regex; the case-insensitive default had listed unrelated
  `s.root`/`Tree.Root`/`Control.Response.Root`).
- **The multi-root surface is unwired**, as intended: `Roots.Contains`/`Names`/
  `All`/`Len` have no production caller (`All`, `Names` and `Contains` are
  test-only; `Primary` is the only production method), `control.ResolveRoots`
  still takes one string, and no parse/state/visibility path was added.

Friction found while reviewing (actionable ones filed in TODO.md):

- **`search` is case-insensitive by default, and the skill says it is not.**
  `raj ctl search -q PRIMARYROOT` returns `primaryRoot` hits with no flag;
  `--case` is what makes it case-sensitive. The skill's "literal and
  case-sensitive unless `--regex` or `--case`" is backwards, and cost this pass
  a re-run to trust a reconciliation search.
- **`SearchMatch`'s doc comment contradicts its fields.** It says
  `ByteStart..ByteEnd` bound the match "within" the line; the fields are file
  offsets (their own comments and a live hit say so) while `Text` is the whole
  line trimmed of trailing space, so a `Text` slice at `ByteStart` is off by
  `LineStart`. Folded into the existing "name the search hit's offsets" item.
- **`edit --old ... --all` is an unbounded substring replace.** A rename like
  `.root` -> `.primaryRoot()` with `--all` would also rewrite
  `snapshotSearcher.root` and `servers.root`; `--all` has no word boundary.
- **`groups` with no path fails** (`no open buffer for that path`) while
  `proposals` answers workspace-wide. Already tracked as the `groups`/
  `proposals` scope split, so not re-filed.
- **`claim A B` replaces the working set; it does not add.** Already tracked
  twice (TODO "Tests and workflow" and "Claim surface"); this pass merged the
  duplicate.

Resolved candidates (not re-filed):

- **A missing parent directory is not a dead end any more.** `saveNamed`
  routes through `ensureParent` (`internal/app/app.go`), which offers to create
  the directory, covered by `TestSaveNamedOffersToCreateAMissingDirectory` and
  `TestSaveNamedDecliningReportsNotSaved` and recorded in COMPLETED.md. The
  residual question is whether `open --create` should refuse or warn up-front
  instead of deferring to the save dialog — a UX choice.
- **`NativeHost` never closes its `events` channel.** The `Host` interface
  comment says the channel closes when the host shuts down and `FakeHost`/
  `HeadlessHost` do close it; `NativeHost.Close` closes `done` and stops the
  reader but leaves `events` open. Filed.
- **`daemon.log` grows unbounded.** `Runner.logWriter` opens it append-only
  and never rotates; `logTail` scoping fixed the wrong-run output, not growth.
  Filed.

Verb surface: the `ls` verb enumerated `internal/workspace` (both files
present); `groups` no-path and `diff` needing a path are the only rough edges,
and the first is already tracked.

Not compile-verified: the wave and its tests were read, not run; the host
`make check` is the gate.

## Between-wave review — daemon discovery and workspace labels (Wave 3a, 2026-09-22)

Read-only pass over the landed, saved Wave 3a. `proposals` was empty and every
open buffer read `saved` with 0 pending / 0 moved, so there was nothing to
dispose; the wave was enumerated from the brief and the tree, since the
container has no git checkout. Per-file `raj ctl lsp diagnostics` read `ok` on
`internal/daemon/daemon.go`, `internal/daemon/daemon_test.go`,
`internal/session/session.go`, `internal/session/session_test.go` and
`cmd/raj/main.go`; a clean per-file reading is not a package check (no
`go vet`, no whole-package compile), so the host `make check` stays the gate.
Everything below is read from source, not run.

Behaviour, verified as a reader:

- **`FindWorkspace` resolves an explicit label only.** It scans `List()` and
  matches `e.Workspace == name`, never `e.Label()`, so a derived display name
  is not addressable; two explicit matches are an error rather than a coin
  toss. `Entry.Label()` has no caller outside the daemon table sort and the
  `LABEL` column.
- **`resolveTarget` is the stop/status/restart front door.** A label with a
  `[dir]` is refused before any lookup, an unknown label is refused, and a
  match resolves to `primaryRoot(e)`. `claimWorkspace` (start, and restart with
  a `dir`) refuses a label another running daemon carries, allows the label's
  own root, and allows the empty label.
- **Restart label carry-over is real, not just documented.** `restartCLI`
  reads the running record's `Workspace` through `Status()` when `--workspace`
  is absent, restarts, then `SetWorkspace` writes that label onto the new
  record; with `--workspace NAME` the name overrides. Ordering is safe: the
  carry is read before `Stop`, and the label is written after `Start` waits for
  the new record's readiness.
- **Stale cleanup and ordering.** `listDir` removes `daemon.json` and
  `daemon.pid` for a dead pid and skips the entry; a missing directory (and the
  empty state home) is `nil`/empty; the sort is `Label()` then `primaryRoot`.
  `sort.Slice` is not a stable sort, but ties on `(label, primaryRoot)` are
  unreachable because a root maps to one state key, so the ordering is
  deterministic.
- **Single-root behaviour is otherwise unchanged.** `Record` only adds `Roots`
  when the caller supplied none; `Start`/`Stop`/`Status` are untouched;
  `cmd/raj/main.go` still calls `daemon.Record(root, rec)` with no `Roots`, so
  the stamp happens at the record layer.

Test integrity (read-only):

- `TestFindWorkspaceIgnoresUnlabelledDaemon` builds the exact state it asserts:
  `stateRoot` points `XDG_STATE_HOME` at a temp dir, `Record` writes
  `<stateHome>/raj/workspaces/<key>/daemon.json` (where `List()` scans) with
  `PID: os.Getpid()` (alive) and `Workspace: ""`, then
  `FindWorkspace(filepath.Base(root))` must miss. It would fail if
  `FindWorkspace` used `Label()`.
- `TestFindWorkspaceRefusesAmbiguousLabel` seeds two explicit `"dup"` labels
  under one state home and asserts an error, not a pick.
- `TestWorkspaceLabelRoundTripsThroughSetWorkspace` preserves pid/socket/tcp/
  token/roots across `SetWorkspace` and resolves through `FindWorkspace`; the
  fields are compared one by one because `Roots` makes `State` non-comparable.
- `TestEntryLabelFallsBackToRootBase` is display-only, and the explicit-only
  rule is pinned by the ignore test above.
- `TestListDirSkipsDeadAndRemovesFiles`, `TestListDirSortsByLabelThenRoot`,
  `TestClaimWorkspaceRefusesRunningLabel` and
  `TestStopWorkspaceResolvesThroughLabel` are discriminating against their
  fixtures.
- Gap: `restartCLI`'s carry-over/override and `startCLI`'s `--workspace`
  labelling have no test. The logic sits in the CLI functions, which expose no
  `Ops`/`Runner` seam, so a test needs a real spawn; the pure piece
  (carry-versus-override) should move into a helper like `resolveTarget`.
  Filed in TODO.md.
- Gap: `list --json` with no daemons marshals a nil `[]Entry` to `null`, a
  shape a script must special-case. Filed in TODO.md.
- Observation, not filed: `TestListCLIRendersRunningDaemon` asserts the value
  kinds but not the five column headers, and `TestListDirSortsByLabelThenRoot`'s
  comment says "stable" where `sort.Slice` is not; neither can fail today.

Friction (raw, deduped):

- **The container image predates this wave.** `/usr/local/bin/raj` answers
  `raj daemon list` with `unknown command "list"` and its usage lacks
  `list`/`ps`/`--workspace`, so the CLI the skill documents is newer than the
  binary in the container. The host editor may be rebuilt, but the container
  image that bakes in `raj` was not, which is the §3 rebuild boundary; a driver
  can neither exercise nor trust `raj daemon` here until it is. No `raj ctl`
  verb changed this wave, so the socket surface is unaffected.
- **Shell quoting around an apostrophe** in an `edit --old`/`apply --text` body
  is already recorded in this file (the `$'...'` apostrophe note); the same
  class recurred and cost a re-apply. Not re-filed.
- **A large insertion with literal tabs reached for `apply --text-file -`
  rather than `apply --hunks`.** The hunks form is JSON Lines with `\t`
  escapes; a body typed with literal tabs is shell-fragile. A skill line
  pointing large or newline/tab-bearing bodies at `--text-file -` would save
  the detour; recorded as an instance of the existing quoting note, not a new
  bug.

Not compile-verified: the wave and its tests were read, not run; the host
`make check` is the gate.

## Between-wave review — workspace roots on the wire, attach adoption (Wave 3c, 2026-09-22)

Read-only pass over the landed, saved Wave 3c. `proposals` was empty and every buffer read saved with 0 pending / 0 moved, so there was nothing to dispose; the wave was enumerated from the brief and the tree, since the container has no git checkout. Per-file `raj ctl lsp diagnostics` read `ok` on every changed production and test file (`internal/control/{header,wire,control,host,header_test,host_test,wire_test}.go`, `internal/app/{app,client,control,ls,headless,deletion,journal,persist,session,client_test,client_reconnect_test,multiroots_test}.go`, `internal/explorer/{tree,pane}.go`, `internal/search/pane.go`, `internal/workspace/workspace.go`, `cmd/raj/main.go`); a clean per-file reading is not a package check, so the host `make check` stays the gate.

Wire, verified as a reader:

- **`hRoots` (0x62) matches an existing list field's encoding exactly.** Encode is `var w prog.Writer; for _, r := range h.Roots { w.Str(r) }; Op8{hRoots, w.Done()}` and decode is `NewReader` + `More()` + `r.Str()` + `recordsOK(r, "roots")`, the same shape as `hClaims`/`hArgv`/`hPaths`. It is emitted only when `len(h.Roots) > 0`, and the code is an argument (<0x80), so `prog.Decode(b, nil)` keeps an unknown opcode and `decodeHeader`'s case-less switch drops it: a peer that does not know the field skips it. Truncation is guarded by `recordsOK` (`errBadFrame: truncated roots record`) rather than silently ending the list early.
- **`Root` still carries the primary.** `Dispatch` sets both `Root: g.Root()` and `Roots: g.roots()`; the real host's `Root()`/`Roots()` read `visible.Primary()`/`visible.All()`, so they agree in production, and a host whose set's first differs from `Root()` still reports `Root` unchanged (the `memHost` fixture exercises exactly that).
- **Both skew directions are safe.** New server/old client: the old client ignores 0x62 and keeps its own root. Old server/new client: no field, so `res.Roots` is nil, `setRoots` is not called, and `StartClient` keeps the launch root. The field is a header argument, not a verb, so it cannot make an old client refuse the frame.
- **Fallback is absence, not `[""]`.** `Guard.roots()` returns the host set when non-empty, else `[]string{Root()}` when `Root() != ""`, else nil.

Visible vs view, and attach, verified as a reader:

- `a.visible.All()` is the only reader in the explorer/search rebuild, `ls` (`Len`/`Primary`/`All`), `host.Root()`/`host.Roots()`, `rootFor`, `pathInRoot`, `snapshotSearcher.roots` and the headless containment check; `a.roots` is the only reader in `session.StateDirForRoots`/`LoadForRoots`/`DecodeForRoots`, `journalDir`/`trashDir`, `persistTick`, `deletion.go` and `primaryRoot()`. The store is opened in the constructor at `session.StateDirForRoots(wsRoots.All())` before any adoption, so it is never the daemon's state dir.
- `StartClient` adopts only after `res.OK` from the `buffers` handshake, and only when `c.Roots()` is non-empty, so an old server keeps the launch root. `ws.New` ignores empty input and rejects nesting; `adoptVisibleRoots` ignores either, so a reply with no set cannot root the explorer at nothing.
- A daemon root absent locally is left as spelled by `localise` (`rebase` returns a path outside the mapped tree unchanged), then adopted; `explorer.children` returns nil on a `ReadDir` error, so the root row has no children rather than a failure. `TestAttachAdoptsDaemonRoots` uses real temp dirs, so this is the path it drives.

Known limitations — all real, all filed in TODO.md:

- The picker (`picker.New(root)`) and the LSP servers (`newServers(root)`) are built from the constructor's launch root, not `primaryRoot()`/`visible`; adoption does not re-root them.
- `resyncClient` re-derives tabs and re-runs adoption of pending buffers but never calls `adoptVisibleRoots`, so a reconnect does not re-adopt a changed daemon set.
- `Client.Roots()` is sticky: `setRoots` is only called for a non-empty reply, so a later reply that names no set leaves the last learned set in place. It also records editor-spelled roots from ResolveRoots's remote `ping`, which runs before the mapper is installed (`localise` returns early with `paths` inactive), so an intervening `Roots()` read would see the daemon spelling; `StartClient` is safe only because the `buffers` reply re-localises them first.
- The hidden policy is loaded from the primary/launch root by `explorer.NewTreeRoots`, `search.NewPaneRoots` and `picker.New`, while `host.Ls` loads the rules of the directory's own root; a `.raj/hidden` in a second root governs `ls` but not the tree or search (the search pane comment already names this as deferred).

Test integrity (read-only; nothing was run):

- `TestPingReplyCarriesRoots` is discriminating: `Dispatch` sets `Roots` from `g.roots()`, and without that line `res.Roots` is nil against a two-root fixture. It also asserts `Root == h.root` while the set's first element is a different temp dir, so "primary stays primary" is pinned.
- `TestPingRootsEmptyWithoutARoot` passes for the *missing-wiring* bug as well as the `[""]` bug: a `Dispatch` that never set `Roots` also reports `len == 0`. It is a real guard for `roots()` returning `[]string{""}` (which would make `len == 1`), but it is not a regression test for the wave. A discriminating fixture would send the no-root ping through `EncodeResponse`/`DecodeResponse` and assert the encoded header does not contain `hRoots`; `TestResponseCarriesRoots` already covers the wire-absence half.
- `TestResponseCarriesRoots` is discriminating for `EncodeResponse`/`DecodeResponse` dropping the field (the decode would be nil) and pins the sparse half (`Root` without `Roots` decodes to nil).
- `TestHeaderRoundTrip` asserts the two-element `Roots` slice element-by-element, and `TestEveryHeaderFieldRoundTrips` is the reflective guard: `fullHeader` now sets `Roots`, so a missing encode line makes `got.Roots` zero against a non-zero `filled.Roots`.
- `TestAttachAdoptsDaemonRoots` is discriminating (without the `adoptVisibleRoots` call `visible` stays the launch root) and asserts all four surfaces: `visible`, `Explorer.Tree.Roots`, `Search.Roots`, containment. `scriptedAttach` drives the real `StartClient` over `scriptedConn.Roots()`.
- `TestAttachViewStoreStaysKeyedByLaunchRoots` builds and checks real on-disk state (the launch state dir's `state.db` exists, the daemon's does not), so it catches an adoption that reopened the store under `visible`; it does not fail against the pre-wave tree, where no adoption exists — it is a guard on the new adoption path, not a baseline regression.
- `TestAttachOldServerKeepsLaunchRoots` pins the observable end-to-end behaviour, but it would still pass with either single guard removed (`StartClient`'s `len > 0` or `adoptVisibleRoots`'s `Len()==0`), because each covers the empty case; it only fails if both accept an empty set.
- `TestVisibleRootsEqualViewRootsForANormalEditor` is discriminating: an unset `visible` (zero `ws.Roots`) fails `Len`/`Primary` against the constructor root, and it pins `Explorer.Tree.Roots` and `host.Roots()`.
- The updated `TestDispatchLs` is discriminating for the follow-up: `memHost.Ls` records the resolved path, and the old guard rewrite to `Root()` would make `h.lastLs.Path` non-empty. The out-of-root refusal is exercised too. The `cli_test.go` comment above the `ls -hidden` assertion ("with no path it names the workspace root") still reads loosely next to an assertion that the wire path is empty; harmless, not filed.

Repo sweep for the old empty-`ls` contract: `Guard.Ls` passes `""` through, `host.Ls` (`internal/app/ls.go`) turns `""` into `visible.Primary()` only in the single-root branch and into `rootEntries()` when several roots, `canonicalPath` returns `""` unchanged, `memHost.Ls` records what it is given, `TestDispatchLs` asserts the empty path, `TestLsNoPathListsRootsAndDisambiguatesSameName` and `TestSingleRootVisibilityUnchanged` assert the new behaviour, and `TestCLILs` asserts the wire path is empty. No production code or other test still rewrites an empty `ls` path to `Root()`.

Deferred 3b-3 candidates: the missing-path warn/skip is now superseded by design — `Guard.Claim` deliberately keeps a path not on disk (a forward claim) and warns only on a non-`IsNotExist` stat error, so `CLAIM-SPEC.md` §3 ("warned per-path and skipped") and §10's open `open --create` parent question no longer describe the code; filed as a spec-reconciliation item, with the forward-claim choice flagged for the user. Per-root `.raj/hidden` is real and filed. The `open --create` parent itself is answered at save by `ensureParent`, so no separate item.

Friction (raw, deduped):

- **`search --json` reports `line` as a 1-based line number while `line_start`/`line_end` are byte offsets.** A `jq` projection that reads `.line_start` as the line and `.line` as the offset is easy to write, and was written in this pass before the raw object was inspected. Folded into the existing "name the search hit's offsets" TODO item, not new.
- **`search -q` is case-insensitive without `--case`.** Already recorded in the Wave 2 block; the same pass hit it again. Not re-filed.
- **`read --json` on several paths returns `{"files":[...]}` while a single path returns the flat record.** A batch projection written as `.[]` silently yields the object's values; the shape split cost this pass a re-read. Already the "Multi-target read: `--json` shape differs" item, not re-filed.
- **`read --json` carries no `bytes`, so appending to a doc needs a separate `version` call.** The single read record is `author`/`spans`/`text`/`version`; the length comes from `version --json`. Same family as the existing "read --json does not echo the span" item, noted not filed.
- **`/tmp/opencode` is root-owned and not writable by the sandbox user (`oc`).** The skill names it as the sanctioned scratch dir, but a heredoc there fails with `Permission denied`; scratch went to `/tmp` instead. Worth fixing the container permissions or the skill text.

Not compile-verified: the wave and its tests were read, not run; the host `make check` is the gate.

## Between-wave review — one language server per (root, language) (Wave 4, 2026-09-22)

Read-only pass over the landed, saved Wave 4 (`internal/app/lsp.go`, `app.go`, `lsp_test.go`, `document_symbol_test.go`). `proposals` was empty and every buffer read saved (0 pending / 0 moved), so there was nothing to dispose. The container has no git checkout, so the changed-file set is the brief's list plus a tree sweep for the new symbols, not an independent diff; that gap is the existing TODO item ("A saved wave has no enumerable diff for the review pass"). `raj ctl lsp diagnostics` read `ok` on all four changed files and on the fourteen other `internal/app` readers of the server table (`diagnostics.go`, `control.go`, `semantic.go`, `folding.go`, `format.go`, `willsave.go`, `codelens.go`, `codeaction.go`, `rename.go`, `documentlink.go`, `jump.go`, `inlay.go`, `menu.go`, `headless.go`). A clean per-file reading is not a package check, so the host `make check` stays the gate.

Keying — verified as a reader:

- `serverKey{root, lang}` is the only key: `for_` inserts `serverKey{root: s.rootFor(path), lang: id}`, and `running`/`live` index the same shape. No lookup, insert or delete keys on `lang` alone; the only direct `byID` indexes outside those three are tests. The two drain loops (`drainDiagnostics`, `drainServerMessages`) iterate the map values and are key-agnostic, so they are correct by construction. Every feature entry point (`closeDoc` via `running`; `syncDirtyPane`/`lspSaved`/`documentSymbols`/`resolveSelectedCompletion`/`maybeRequestHighlight`/`folding`/`semantic`/`format`/`willsave` via `live`; codeaction/codelens/documentlink/jump/inlay/rename and the `control.go` LSP verbs via `for_`) passes a path and lets the resolver choose the root. No reader was left on a language-only key.

Resolution:

- `servers.rootFor` and `App.rootFor` use the identical component-wise rule (`filepath.Rel`, rejecting `..` and `..<sep>`; `workspace.inside` is a third copy), so `/a/bc` is not inside `/a/b`. Roots cannot overlap (`workspace.New` rejects nesting), so at most one matches and the returned root is unique. The fallback is the only rule difference: outside every root `servers.rootFor` returns `roots[0]` (the old single-root behaviour) while `App.rootFor` returns `""` (the copy-path "outside the workspace" case). The two disagree on the *set* under attach: `App.rootFor` reads `visible` (daemon roots) while `servers.rootFor` reads the constructor's launch roots, so a path under a daemon-only root or a launch-only root resolves differently — the already-filed attach item. A benign divergence: `servers.rootFor("")` returns the primary while `App.rootFor("")` returns `""`; every server caller guards an empty path (empty language id) before resolving. `docPath` still joins a relative path against the primary.

Lifecycle — verified as a reader:

- `for_` builds `lsp.Server{Dir: root}` and `start` handshakes `lsp.URI(ls.root)`, so the process's cwd and `rootUri` are the path's own root; `clientCapabilities` sends one `rootUri` per connection, now correct per root.
- `running`/`live` only index; they never call `for_`, never search PATH, and resolve the caller's own path's root.
- `stopAll` copies every `byID` value under the lock, clears the map, then stops each, so it reaches every root.
- `WarmServers` dedupes languages, then steers the first file of each language per distinct root through `for_`; a second file of the same language and root shares the entry.
- Single root: every path resolves to the one root, the key is unique per language, `Dir`/`rootUri` are that root, so behaviour is the old one.

Test integrity (read-only; nothing was run):

- `TestServerRootFollowsTheFilePath` is discriminating: it drives the real constructor (`newRootsHarness`) and `for_` with `stubGopls` (which exits before the handshake, so `for_` records the entry synchronously) and asserts the entry is keyed by root2 with `Dir` and `root` both root2. Under the single-root key the root2 key does not exist. No race on the assertion: `for_` inserts before returning and the goroutine does not touch the map.
- `TestServersArePerRootForRunningAndLive` is discriminating and reaches live state: `stubHandshakeGopls` answers initialize and stays alive, `waitLiveServers` polls `live` to a deadline, and the test asserts two distinct servers with their own `Dir`, that `running` returns each path's own server, and that the table holds two. A single `byID["go"]` makes `lsA == lsB` and `byID == 1`.
- `TestStopAllStopsEveryRoot` reaches live state the same way and checks the table empties and each server refuses a later `Start` with `ErrClosed`. It is weaker than its name: it never asserts the two live servers are distinct, so a regression that reused one server would still pass (both lookups return the same pointer, `stopAll` stops it, both `Start` calls return `ErrClosed`). Not vacuous today (the fixture does create two), but the minimal strengthening is `if live[0] == live[1] { t.Fatal }` plus `len(byID) == 2` before stopping. Filed in TODO.md.
- `TestWarmServersStartsOnePerLanguagePerRoot` is discriminating: `stubGopls` plus three open files (two under rootA, one under rootB) make `WarmServers` produce two entries; the single-root key produces one. It asserts `Dir` per root. The fixture builds the state: `newRootsHarness` has nothing open, so the only panes are the three it opens, and the second rootA file is written before it is opened.
- `TestWarmServersSingleRootIsOneServerPerLanguage` pins the regression half (one root, two files, one server with that root's `Dir`); it does not fail against the pre-wave tree by itself — the single-root result is the same — it guards the new resolver on the single-root path.
- The updated call sites are all correct: `lsp_test.go`/`document_symbol_test.go` build their injected keys with `serverKey{root: h.servers.rootFor(...), lang: lsp.LanguageID(...)}`, and every `newServers(...)` in the tests passes `[]string{"/w"}`. `TestServerSelection`'s empty-path case still returns before resolving a root.

Residuals — verified, not refuted:

- Attach's `servers` stays launch-rooted (`newServers(wsRoots.All())` in the constructor; `adoptVisibleRoots` never rebuilds it). Already in TODO.md ("never re-rooted on attach").
- `docPath` joins a relative path against `a.primaryRoot()`; pre-existing, and no caller hands a relative pane path.
- The "single-root client" comment in `clientCapabilities` is still accurate per connection: each server is sent exactly one `rootUri` and no `workspaceFolders`.

Friction (raw, deduped):

- **`exec` is refused over TCP** (as designed), so this pass could not run `git status` on the host to enumerate the wave's changed files even though the editor and its repo are on the host; the container's `/work` is empty. The saved-wave diff gap in TODO.md is the exact pain, hit again.
- **Multi-path `lsp diagnostics` still prints one unframed `{"status":"ok"}` per operand** with no path, and a cold gopls prints `starting language server…` per path as non-JSON. Already filed (two items), re-observed.
- The 5-second `waitLiveServers` deadline (a `t.Fatalf`, not a hang) is the right shape for an asynchronous start; worth copying for other live-server tests.

Not compile-verified: the wave and its tests were read, not run; the host `make check` is the gate.

## Tool-use baseline (2026-09-22)

First pass of the efficiency loop over the tool ledger
(`~/.local/share/opencode/raj-tool-ledger.jsonl`): 39 sessions, 6,222 recorded
calls, an average of about 160 per session — so over 100 calls is the norm, not
the exception. The count of sessions is not the problem; the call mix is.

- `search` 2,949 (25%) and `read` 2,591 (22%) are nearly half of all calls. The
  batching forms exist and are barely used: `search --context` 149 (5% of
  searches), and the multi-target `read A B C` form is not measured as adopted.
- `edit` 890 against `apply` 169, of which `apply --hunks` is 8 (5%). The
  multi-hunk door is not the default, so a several-hunk change costs several
  round trips.
- `lsp` 408: multi-path batching is blocked because the `--json` form emits one
  unframed object per operand with no path.
- `claim` 210: the replace-by-default set semantics drive re-claims and refused
  applies.
- `version` 105: forced by `read --json` omitting `bytes`/`lines`, so a driver
  that just read the text makes a second call before appending.
- Tools: `bash` 6,038 (91%), `task` 106, `webfetch` 37, `skill` 31. Some 119
  `cat` and 31 `grep` calls run outside `raj ctl`; workspace files read that way
  are a rule gap, container-only files are fine.

The between-wave review pass now reports this mix per wave (duty 6 in
`docs/REVIEW-AGENT.md`). Surface fixes that remove the reason for the extra
call, in priority order: `read --json` carries `bytes`/`lines`; multi-path
`lsp diagnostics --json` is one framed array keyed by path; `claim` warns
before replacing a non-empty set. Discipline (skill and every brief):
`search --context`, `read A B C`, `apply --hunks`, `dump`/`patch`. Measure
calls per wave before and after; the aim is to roughly halve.

## Between-wave review: read size and framed diagnostics (2026-09-22)

First duty-6 pass. The two efficiency waves and the workflow scaffolding that
added the duty were reconciled on a settled tree: `proposals` empty, `status`
clean no dirty or pending buffer, nothing superseded. Source read:
`internal/control/{host.go,cli.go,cli_test.go,host_test.go}`. The three repo
docs carry the additions -- `docs/REVIEW-AGENT.md` duty 6, the
`docs/AGENT-FEEDBACK.md` baseline block, `docs/RECURSIVE-RAJ.md` section 10 --
and they agree on the ledger path and the thresholds. One multi-path
`lsp diagnostics` call over the four Go files returned `ok` for each once gopls
started. Not compile-verified: the change and its tests were read and driven
over the socket, not built; the host `make check` is the gate.

### Verified live

- `read --json docs/TODO.md` returns `bytes` 36274 / `lines` 544 with the keys
  `author/bytes/lines/spans/text/version`; `version --json` returns the same
  36274/544. A `--lines 1,1` span read still reports the whole file
  (36274/544, `text` 7 bytes).
- A multi read of `docs/TODO.md` and `docs/COMPLETED.md` gives each entry its
  own `path/bytes/lines`; `COMPLETED.md` reports the post-edit 178319/2365 that
  `version` reports too.
- `lsp diagnostics A B --json` is one document with `files[]`, each entry
  headed by its own `path`; a cold start put `"status":"starting"` and the
  `starting language server...` detail inside the entry, so stdout stayed pure
  JSON. A missing operand appeared as `{"path":...,"status":"error","detail":
  "no open buffer..."}`, the same refusal on stderr, exit 1; an all-ok sweep
  exits 0. Single-path `--json` stayed a bare object (`["status"]`), and the
  plain sweep printed `==> path <==` per operand.

### Judgements

- The multi-target span split -- a shared span reports each entry's contributed
  bytes/lines, not the whole file's -- is acceptable, not a TODO: `bytes` is
  also what splits the concatenated body, so it must stay contributed, and the
  whole-file size an append needs is one single-target read or `version` away.
  The code comment states the split.
- One gap, filed as a TODO: a multi-path entry whose answer parses but is
  statusless (an old server's `{}`) is emitted with `path` only and exits 1,
  where the single-path form refuses with "the editor did not report a
  diagnostics status; rebuild it to match this ctl".

### Tool use (baseline: 39 sessions, 6222 calls, ~160 each)

| session | calls | verb mix | batching |
|---|---|---|---|
| B1 `ses_f388269e2ffeaS502dXqI6QNxo` | 144 | search 93, read 30, edit 12, lsp 7, reg/claim/buffers 1 | `--context` 93/93, `read A B C` 0/30, `apply --hunks` 0 (no applies), multi-path lsp 1/7 |
| B2 `ses_f381b280cffeiHPXdkIGWYQohP` | 83 | search 32, read 30, edit 10, lsp 4, groups 3, diff 3, version 2, revert 2, reg/claim/buffers/help 1 | `--context` 27/29, `read A B C` 0/30, `apply --hunks` 0 (no applies), multi-path lsp 3/4 |

- B1 is over the ~100 flag (144) though under the ~160 baseline; B2 is clear
  (83).
- `search --context` is the success story: 100% and 93%. Across the ledger,
  excluding this review, it is now 249 of 3075 searches (~8%), up from 149
  (5%) at the baseline -- the count is approximate because one shell command
  can hold several invocations.
- `read A B C` was 0/30 in both. The cause is the surface, not discipline: a
  batch read shares one span, and every read here was a different region of a
  different file. Per-target spans would be the fix if the form is wanted.
- `apply --hunks` 0 is not a discipline flag on these two: neither made an
  apply at all (12 and 10 `edit` calls, each a single block). It is flat
  globally (9 vs 8 at the baseline), so the multi-hunk door is still not the
  default.
- Multi-path `lsp diagnostics`: B1 used it once (the form did not exist in its
  running editor yet); B2 used it 3/4. Each sweep folds N single-path calls into
  one -- B2's three two-path sweeps saved three round trips.
- Claim only what is visible: B1 used `read --json` 30 times and `version` 0
  times, where the baseline's 114 `version` calls were the read-then-size
  pattern this change removes -- consistent with the fix, not proof of a
  wave-wide cut. Both sessions were efficiency-aware, so their rates are not
  the baseline's.

### Test integrity

- B1 CLI tests would fail without the CLI keys (absent -> 0); the multi-target
  test discriminates `lines` only, since a multi-read already carried `bytes`
  for splitting. The host tests use `memHost.Buffers` (+1 lines) and fail on the
  default zero. Non-vacuous.
- B2 framed/attributes/cold-start/plain tests would fail before the change
  (bare objects make `json.Unmarshal` of the stream fail, or the headers are
  absent). `TestCLILSPDiagnosticsSinglePathJSONUnchanged` passes before and after
  and does not discriminate the new code, but it guards the dispatch from
  wrapping a single path (a plausible wrong implementation); likewise
  `TestCLILSPDiagnosticsSweepsMultiplePaths` passes both ways. Both are guards,
  not vacuous.
- Fake seams mirror the real host: `lspJSONByPath`/`lspErrByPath` are read in
  the fake's `lspprep` case, the op the real `connection.lsp` submits to the
  host, so an err stands in for a real refusal; `fakeLineCount` matches
  `view.Index`/`File.Lines`; `fakeLineSpan` mirrors the host line translation.
  One wrinkle: the fake's `buffers` case computes `Lines` without the `+1` its
  `version` case uses, so the two disagree; inert here but a latent fixture
  inconsistency.

## Between-wave review: per-target read spans and repeatable search (2026-09-22)

Wave `internal/control/{cli.go,cli_test.go}`: `read --at` per-target line spans
and repeatable `search -q`. Reconciled on the settled tree: `proposals` empty,
every buffer saved, nothing superseded, and the new symbols (`atSpanFlag`,
`searchPatternFlag`, `readTargets`, `readSpan`, `addLineSpan`) appear only in
the two changed files. The wave's file list is the brief's assertion; the
container has no repo and `exec` is refused over TCP, so it could not be
checked with git.

**The new surface could not be exercised live: the container's
`/usr/local/bin/raj` predates the wave.** It rejects `--at` ("flag provided but
not defined: -at") and its `-q` is a plain string, so a repeated `-q` is
last-pattern-wins with no label (a discriminating `-q alpha -q gamma` search
returned only the gamma hit). The host daemon and the CLI change were built
together, but the container image that carries `raj ctl` was not rebuilt, so
every verdict below is read-only on the source and tests. Rebuild the container
image before the next wave can use `--at` or a repeated `-q`; until then the
running client is the old surface (section 3, "the container image has to match
the build"). The multi-path `text` wire path itself is fine: `raj ctl read A B`
already works against the running daemon.

### Verified (read-only)

- `atSpanFlag.Set` splits on the last `=` and refuses a missing `=`, a
  non-positive or unordered span, or a non-numeric one by name;
  `parseAtSpan`/`ctlAtoi` take digits only. `readTargets` reads a positional
  path named by `--at` once at its span (a repeated positional still reads
  once), an unnamed positional whole or at the global span, then appends the
  `--at` paths with no positional. `readSpan` is comparable, so `readMany`
  groups identical spans into one `c.Do` each; a single target keeps the flat
  shape, and `addLineSpan` echoes `line_start`/`line_end` only for a line span
  (a byte span keeps its old keys).
- `searchPatternFlag.patterns()` keeps the single empty-pattern default;
  `doSearch` copies one `SearchQuery` per pattern and calls `DoStream` once
  each, labels only when `multi`, folds `matched` across patterns, prints the
  include warning once and a metacharacter hint per pattern. The whole-JSON
  path keeps the exact `SearchMatch` shape for one pattern and adds `pattern`
  per match only for several; single-pattern plain and `-jsonl` output is
  unchanged. The transport is as briefed: K distinct spans -> K `c.Do`, P
  patterns -> P `DoStream`, all inside one CLI call (the N round trips are
  filed, not fixed).

### Judgements

- The last-`=` split is acceptable, and recorded rather than filed. It only
  misparses silently when the text after the final `=` is exactly `LO,HI` (a
  file literally named `out=1,2`): `--at out=1,2` reads `out` lines 1..2. Every
  other `=`-bearing path is addressed by writing the span after the final `=`,
  and a bad trailing segment refuses by name rather than reading whole.
- Grouping identical spans into one wire call is a pure round-trip saving and
  is invisible in the output, so no test pins it: a regression to one call per
  target would pass every test. The fixture's multi-target `text` branch now
  applies the shared span, mirroring `host.readPaths`, but the only multi-path
  group any test exercises is the whole-file `read A B`; no test carries a
  group of two paths with a non-whole span. The fake records each dispatch, so
  a call-count assertion would close it.
- A multi-pattern `-json` reply's `files` count sums per pattern, so one file
  matched by two patterns is counted twice. New multi-only shape; acceptable,
  worth stating in the skill if it is documented there.

### Test integrity

- Every `--at` test fails against the old client: the flag is undefined, so the
  CLI exits 2 and the code/name assertions fail (`TestCLIReadAtMalformed` also
  fails its "names the entry" check, because a flag error does not carry the
  value). `TestCLIReadAtPerTargetSpans` and
  `TestCLIReadAtDeduplicatesAndKeepsOthersWhole` assert per-target text a
  single shared span could not produce, plus the dedupe (two files, not three)
  and the no-span key on the unnamed path.
- `TestCLIReadAtPathWithEquals` fails if the split were on the first `=`
  (`/w/a` + `b.go=1,2` -> refusal), so it discriminates the last-`=` rule.
  `TestCLIReadJSONEchoesLineSpan` fails without `addLineSpan`, and its byte-span
  half fails if a byte span grew `line_start`.
- The `search` tests fail without the change: old `-q` is last-wins, so
  `TestSearchMultipleQueries` loses the `alpha` hit and its label,
  `TestSearchMultipleQueriesExit`'s matching case (`alpha`,`zzz`) returns 1
  instead of 0, and `TestSearchMultipleQueriesFlagsAndHint` gets no hint. That
  last test's all-miss half (`yyy`,`zzz`) passes under the old behaviour too,
  but the test as a whole is not vacuous and the matching half discriminates.
- Fixture: `fakeLineSpan` mirrors the host line translation, and the fake
  `text` branch splits the concatenation by each contributed `Bytes`, which is
  what `readMany` reassembles on. No test passes for the bug it names.

### Tool use (baseline: 39 sessions, 6222 calls, ~160 each)

The implementation session is `ses_f37d77739ffe3WiAooe3KEEeyY` (identity
`raj-c84704a5`: `register` once, one `claim` for both files): **104 bash calls**,
over the previous review's B2 (83) and just over the ~100 flag, under the ~160
baseline and B1 (144). Its 150 `raj ctl` verb occurrences: `search` 63, `read`
62, `ls` 5, `edit` 5, `lsp` 4, `apply` 3, `groups` 2, and
register/claim/buffers/list/status/help 1 each. Batching: `search --context`
34 of 40 search calls (85%), multi-path `lsp diagnostics` 4 of 4, `read A B`
3 of 49 reads, `apply --hunks` 0 of 3 (single-block applies). Across the ledger
`search --context` is 387 (from 149 at the baseline), `apply --hunks` 14 (from
8), `raj ctl search` 2,536 (from 2,949) and `read` 2,811.

- The session is efficiency-aware, so its rates are not the baseline's. The
  per-target-span fix could not show up as `read A B` adoption here because the
  agent's client was the old binary: it wrote the feature with `apply` and test
  text, not by driving `--at`.
- The N-round-trip cost the user flagged is invisible to a call count: a
  `read --at` over K spans is one tool call and K `c.Do`. That is why it is
  filed as a surface fix (a single `run --prog` frame), not a discipline note.

Not compile-verified: the source and tests were read, and the changed surface
could not be driven (the container client predates it); the host `make check`
is the gate.

## Between-wave review: attach by workspace label (2026-09-22)

Wave `cmd/raj/main.go`, `internal/app/settings.go`, `cmd/raj/main_test.go`:
attach to a daemon by `--workspace` label. Settled-tree check: `proposals`
empty, `groups` empty, all three buffers `saved`, nothing superseded. All
three files answered `ok` in one multi-path `lsp diagnostics` call. The
implementation session is `ses_f3571c68affeYVQifLZGHMVKiM` (identity
`raj-b9b217db`); the orchestrator session is `ses_f358446f4ffeOEm6Frv330LTXx`
(`raj-3b26589b`), which ran the live `raj --workspace nope` refusals.

### Raw findings (implementation subagent)

- **`/tmp/opencode` is root-owned and not writable in the container.** The
  agent probed it (`ls -ld /tmp/opencode; touch /tmp/opencode/probe`) and fell
  back to `/tmp/raj-w1` for its hunk files and test text; this review confirmed
  the dir is `root:root 0755`. The scratch dir the workflow pre-approves is
  unusable by the agents it is for — fix its ownership/mode, or stop naming it.
- **`read --json` returns whole-file `text`/`bytes`/`lines` but no per-line
  byte index.** Every structural `apply` offset cost an extra `search --json`
  round trip; the session built its hunks from hardcoded offsets (23688, 23817,
  24007) it had to locate separately. Promoted to TODO: echo a line-start
  offset array with the text, so a read is an offset source on its own.

### Other observations (review, low)

- `State.Workspace`'s doc comment (`internal/daemon/daemon.go:41-42`) says an
  empty label means the daemon is "shown by its primary root", but the CLI's
  LABEL column now uses `displayLabel()`, which shows `-` and is pinned by
  `TestListCLIShowsDashForUnlabelled`. The comment contradicts the code;
  update it to say `-`. Recorded, not fixed (source is settled).
- `Entry.Label()` now has no production caller — `displayLabel()` took the
  LABEL column and the sort — and is kept alive only by tests. Retire it, or
  say why the derived name stays as a public helper.
- `TestAttachWorkspace`'s "finder error passes through" case asserts only that
  an error came back, and so does the "label not found" case, so a regression
  that turned the finder's ambiguity error into "no running daemon labelled"
  would still pass. Give the table a `wantErrContains` and assert the
  ambiguity text. The socket-over-TCP, `--control-addr`, positional-argument
  and no-address cases do discriminate.

### Tool use

- implementation `ses_f3571c68affeYVQifLZGHMVKiM`: **29 calls** (28 bash), verb
  occurrences from the recorded (sometimes truncated) commands: `read` 7,
  `search` 7, `goto` 3, `lsp` 2, and register/claim/apply/edit 1 each.
  Batching: `read` multi-target 0 of 7 (whole-file `--json` reads),
  `search --context` 2 of 7 (29%), `apply --hunks` 1 of 1 (`cmd/raj/main.go`)
  plus `edit --old-file/--new-file` for the `run` prologue, multi-path `lsp` 0
  of 2 (per-file).
- orchestrator `ses_f358446f4ffeOEm6Frv330LTXx`: **93 calls** (86 bash) — under
  the ~100 flag but close; `read` 45, `search` 24, `open` 4, `goto` 4,
  register 3, `buffers` 3, version 2, and whoami/proposals/lsp/groups/diff 1
  each. Batching: `search --context` 0 of 24 (0%), multi-path
  `lsp diagnostics` 1 of 1 — the framed three-file sweep.
- Global ledger (cumulative, not wave-scoped): 7,398 before-rows, 7,187 bash;
  `search` 2,511 (`--context` 391, 16%), `read` 2,615 (multi-target ~129, 5%),
  `apply` 210 (`--hunks` 12, 6%), `lsp` 428 (multi-path 124, 29%).
- Flag: the orchestrator never used `search --context` (0 of 24); every
  batching form but multi-path `lsp` is far under a quarter globally.
  Multi-path `lsp diagnostics` does emit framed output (`==> path <==`, and a
  `{"files":[...]}` document with `--json`); encourage it.
- Ledger caveat: the recorded `cmd` is truncated for long commands, so any verb
  that follows a heredoc (most `edit`/`apply` with `--old-file`/`--hunks`) is
  invisible to the verb-mix query — this review's six `edit` calls show as 2,
  and the implementation session's apply/edit pair as one each. Count calls
  (one ledger row per call) rather than verbs when the two disagree.

## Between-wave review — Wave 2 (2026-09-22)

- **The host gate failed on a fixture that did not build the state it asserted.**
  `TestRootsClearsOnSetLessReply` pinged the fake editor and asserted
  `Client.Roots()` became non-empty, but `fakeEditor.run`'s `ping`/`buffers`
  replies carried only `Root` while the shipped `Dispatch` sets `Roots`
  (`internal/control/host.go`). The test was right; the fixture was the bug.
  Fixed the fixture to mirror Dispatch. *A fixture hand-copied from the wire
  drifts from it.*
- **Event-thread state was mutated from the watch goroutine.**
  `resyncClient` called `readoptVisibleRoots` -> `adoptVisibleRoots`, which
  rebuilds `a.visible`, `a.Explorer`, `a.Search` and wires the search pane,
  while `syncClient` correctly queues `clientFile`s for `drainClient`. Fixed by
  queueing the root set and adopting in `drainClient`; `go test` without
  `-race` did not catch it.
- **`apply --hunks` adoption is 0 across the six most recent sessions.** The
  multi-hunk form is never used; edits go through single `edit --old/--new` or
  repeated single `apply`. A multi-hunk edit is one call and one re-read.
- **`search --context` and multi-target `read` are under a quarter in several
  sessions** (0/31, 9/39, 1/27 and 4/59 reads); the sessions that lean on them
  (23/35, 16/20, 51/52) show the payoff is real.
- **`raj ctl groups --pending` with no path prints nothing** on a connection
  with no active buffer (exit 0), so it reads as "no sets" rather than "no
  buffer". Name the missing buffer or default to the workspace.
- Tool use: two recent sessions exceed ~100 calls (126 and 129), one of them
  the orchestrator; `search --context` is used in under a quarter of searches
  in four of the six most recent sessions.

## Between-wave review — an attached client is a normal editor (Wave 3, 2026-09-22)

Read-only pass over the landed, saved Wave 3. `proposals` and `groups` were empty
and every buffer read saved (0 pending / 0 moved), so there was nothing to
dispose; the wave was enumerated from the brief and the tree, since the container
has no git checkout. `raj ctl lsp diagnostics` read `ok` on all seven changed
production files and all five changed test files, and on every `_test.go` in
`internal/control` (16) and `internal/app` (64), run as multi-path sweeps because
a test-only compile error in a file the sweep does not name is invisible. A clean
per-file reading is not a package check, so the host `make check` stays the gate.
No test was run.

Verified as a reader:

- **The write-gate boundary is three-way and pinned in one place.**
  `Guard.Apply` admits `IsAgent || IsProvisional || writesAsHuman`, where
  `writesAsHuman` is a registry lookup (`Participants.Get(id).Kind == KindHuman`)
  excluding `AuthorOriginal` and `LocalHuman`; `Guard.Patch` keeps the old
  agent-only check. `TestApplyAllowsAJoinedHuman` builds each state: a durable
  `KindHuman` row writes and the buffer changes; `LocalHuman` is refused with no
  buffer change (before the claim check, so no claim is needed); an agent writes;
  a reserved (anonymous) id writes.
- **`Request.Kind` crosses sparsely and absent means agent.** `hKind` (0x63) is
  emitted by `str(hKind, h.Kind)`, which skips the empty string, so an old peer
  sends no field and `decodeHeader` leaves `Kind` zero; `hello` maps only
  `string(KindHuman)` to `KindHuman` and everything else to `KindAgent`.
  `TestHelloOverTheSocketDeclaresAHuman` asserts a unix-socket hello joins as a
  human and `TestHelloOverTCPDowngradesAHuman` asserts a TCP one joins as an
  agent while a second hello with no `Kind` is; the field is also covered by
  `TestEveryHeaderFieldRoundTrips` (`fullHeader` sets `Kind: "human"`), so a
  missing encode line fails `make check`.
- **The forward path satisfies claim and read on the decide connection.**
  `forwardApply` sends `claim` (path), then `version` (which `Guard.Version` turns
  into `markRead`), then `apply` with `Base` from the tracked sync; all three go
  on `a.decideClient()`, which `joinClient` has helloed as the same durable
  `client:<key>` human as the watch. The server's `if req.Author == 0 { req.Author
  = author }` attributes the apply to that human, whose registry row is
  `KindHuman`, so `host.isAgent` is false and `host.Apply` does not `ProposeGroup`.
  `TestClientLocalEditForwardsOneApply` records the apply, checks the base equals
  the synced version and the hunk is the changed middle; `recordingConn` emulates
  claim/version/apply, so it pins the request shape, not the daemon's acceptance.
- **A dirty pane is not clobbered by a watch install.** `installClientFile` calls
  `clientEditBusy` and returns nil; `TestClientWatchSkipsDirtyPane` sets dirty,
  asserts the pane text is unchanged, clears dirty and asserts the snapshot
  installs.
- **The two rewritten Review tests still test something.**
  `TestClientRefusesTypingInReview` and `TestClientRefusesEveryEditGestureInReview`
  assert the mode precondition, then that each gesture leaves the text unchanged
  and sets a `read-only` status; the second covers typing, paste, cut, undo and
  redo. They pin the invariant the wave had to preserve; their status assertion
  keeps them from passing on a refusal for the wrong reason.

Flagged:

- **[high, fixed as a proposal] Reload re-opened on an attached client.**
  `readOnly()` used to be `mode == Review || attach`, so `keys.Reload` refused a
  client in both modes; the wave narrowed `readOnly()` to Review only and left the
  branch with a comment promising the client refusal. In Edit mode a client could
  run `File.Reload`, which reads *this machine's* disk, replace the daemon
  snapshot, and let the idle `scanClientEdits` forward the difference as an
  `apply` — a disk revert of a daemon edit. Fix proposed:
  `if a.attach || a.readOnly()` in the `keys.Reload` branch with an
  attach-specific status (`internal/app/app.go`). Not run, not accepted.
- **[low] Comment drift from the same narrowing.** The `paste` doc-comment and
  the decide-connection comment still read as if a client were always refused and
  the decide connection a separate author; both were corrected as proposals.
  Sweep the other `readOnly()` callers (`format.go`, `rename.go`, `codeaction.go`,
  `inlay.go`): they now reach a snapshot buffer whose edits forward only when the
  path has a `clientEdits` entry, so a headlessly loaded rename target has no
  forward path.
- **[low] A forwarded human edit can still land over an agent's proposal.**
  `host.Apply` treats the human like any `ApplyDiff` writer (advisory lease), so a
  client that types over an agent's Proposed run lands and supersedes it with a
  warning; the local keyboard path is stricter (`EditLeased`; see
  INVESTIGATIONS.md "The human typing path stays strict..."). Escalated below.
- **[low] `who`'s connected flag can flap with two connections on one identity.**
  watch and decide both `Join` `client:<key>`, and `Registry.Leave(id)` marks the
  single row disconnected when *either* connection ends, so the row can read
  `connected:false` while the other connection is live. Nothing gates on
  `Connected`, so it is cosmetic; a connection refcount is the fix if it matters.
- **[info] `TestClientLocalEditForwardsOneApply` is named "exactly one apply" but
  asserts only the last recorded apply**, so it would not fail if a second were
  sent; a `len(conn.applied)` check would match the name.

Ledger (the file has no wave tag; the running orchestrator session accumulates
across waves, and the extraction counts `raj ctl` substrings in the review's own
recipe commands and multi-verb shell loops, so these are upper bounds):

- implementation session `ses_f35157443ffeaeyAPTjpeVilCA`: ~171 `raj ctl`-bearing
  bash calls (over the ~100 flag), read 59, search 51 (`--context` 32, ~63%),
  edit 44, lsp 10 (multi-path 9), claim 3, version 2, goto 2, buffers 2,
  whoami/register 1 each. `apply --hunks` 0 of 44 edit/apply calls (0%): the work
  was 44 string edits, not multi-hunk applies.
- orchestrator session `ses_f358446f4ffeOEm6Frv330LTXx`: ~138 calls, read 66,
  search 38 (`--context` 0, 0%), buffers 11, open 8, goto 8, register 5,
  proposals 5, lsp 4 (multi-path 4), whoami/groups 3 each, version/diff 2 each,
  claim/clear/who 1 each. `search --context` 0 of 38 is the discipline flag.
- review session `ses_f34fb5d9fffeaJ84trz1AGc2fX`: the ledger attributes 161
  `raj ctl`-bearing calls, far more than this pass issued (subagent rows appear
  to share a session with other activity), so it is reported for completeness
  only. On the commands it can see: read 73, search 65 (`--context` 35, ~54%),
  lsp 9 (multi-path 9), edit 6, claim 1, ls 2. Self-flag: the two `app.go`
  hunks went out as two `edit` calls rather than one `apply --hunks` (0/6).
- Global (cumulative): `apply --hunks` and multi-target `read` remain the
  low-adoption forms.

Escalated to the user — the `Guard.Apply` human admission:

- Any token holder (the same token every agent uses) can now hello with `Kind:
  "human"` and have its hunks land as accepted text rather than a proposal: no
  `ProposeGroup`, so no accept gesture, and the text is in the agreed composition
  and saveable. Before this wave a durable non-local author was refused, and an
  agent could not declare itself a person. Trade: a second human editing the
  workspace is the point (the user's `--name`/`--phone` decision), but admission
  is self-declared over a shared token, so it is a review-gate bypass for anyone
  who can already connect. If the token already decides who may connect, the code
  is right; if the review gate must hold against a rogue client, the daemon needs
  to mint or confirm the human identity out of band instead of trusting `hello`.
  The `Patch` asymmetry (human admitted to Apply, refused for Patch) is deliberate
  and tested, not a second decision.
- Smaller: should an attached human typing over an agent's Proposed run keep the
  agent's advisory-lease behaviour, or take the local human's strict refusal
  (`EditLeased`)? The current code does the former.

Friction (raw, deduped):

- **The brief named a test file that does not exist**
  (`internal/control/control_test.go`; the control tests are
  `host_test.go`/`tcp_test.go`/`header_test.go`/`wire_test.go`). A review that
  trusted the brief's list would have diagnosed a refused path and could have
  called the package unchecked. Folded into "A saved wave has no enumerable diff
  for the review pass"; briefs should name readable files or the wave should
  enumerate from the tree.
- **One file at two `--at` spans collapses.** `read --at a.go=1,2 --at
  a.go=20,21` returns only the last span (cross-file `--at` returns the framed
  `{"files":[...]}` correctly), so two disjoint spans of one file need two calls.
  A per-span same-path form, or a note in the skill, would close it.
- **The ledger is not wave-scoped.** Every review reports the orchestrator
  session accumulating across waves, so "calls per wave" is approximate; a wave
  or task id on the row would make duty 6 exact.

## Between-wave review — socket gate, phone drawer reset, hidden config to XDG (Wave 3 hardening + Wave 4, 2026-09-22)

The host gate failed once and resumed this pass: `TestHiddenConfigMigrationFailureIsNotFatal`
(`internal/app/session_test.go`). State at entry: no pending proposals, every
buffer saved. A stale working-set claim on `internal/app/session_test.go` left by
the gone `w4-hidden` author did not block a new claim. No set was superseded or
wedged, so nothing needed disposal.

### The failing gate

- **The test's absence assertion, not the migration, was wrong.** `if _, err := os.Stat(dst); !os.IsNotExist(err)` treats any stat error that is not `IsNotExist` as "the destination exists". The fixture puts a regular file where the `workspaces/` directory belongs, so `stat(dst)` returns `ENOTDIR` on POSIX (macOS and Linux alike); `os.IsNotExist(ENOTDIR)` is false and `errors.Is(err, fs.ErrNotExist)` is not it either, so the test failed though nothing was written. Fixed as a proposal: assert `err == nil` (`internal/app/session_test.go:384`), so any stat error means "no destination" and a real write (stat succeeds) still fails. The fixture and the migration are untouched. Modelled on the sibling `TestStateDirFailureDoesNotStopStartup`, which has the same fixture shape and the honest `if _, err := os.Stat(legacy); err != nil` assertion.
- Verified as a reader: `migrateHiddenConfig` reaches `MkdirAll` and returns its error before `WriteFile`, so a failed run cannot write; the test's doc comment ("A migration that cannot write its destination is not fatal") stays true.

### Fixed as proposals (integration fallout in free regions)

- `internal/app/session_test.go:384` — the gate fix above.
- `internal/session/session.go:86,154` — the `StateDir`/`Dir` docs still said `.raj` keeps the workspace config (`.raj/hidden`); now the one-shot migrations are named as the only readers.
- `internal/app/settings.go:139` — "exactly as `.raj/hidden` reports a bad pattern" → "the workspace hide file".
- `README.md:104,405,410` — the Configuration section still told users to write `.raj/hidden` in the workspace; it now names `~/.config/raj/hidden` and the XDG workspace file keyed by the root set.
- `docs/COMPLETED.md`, `docs/AGENT-FEEDBACK.md` — the Wave 3 entry named the removed `TestHelloDeclaresAHuman`; now `TestHelloOverTheSocketDeclaresAHuman`/`TestHelloOverTCPDowngradesAHuman`.

### Verified as a reader (nothing run; no Go toolchain)

- **The XDG keying is per root set and reads in the right order.** `WorkspaceFile` is `configDir()/raj/workspaces/<workspace.StateKey(roots)>/hidden`; `Load` appends the user file then the workspace file over the defaults. `StateKey` sorts and cleans the set, so order does not change the key and two directories do not share one; `TestWorkspaceKeyIsolatesWorkspaces` seeds A and asserts B cannot see it.
- **Every surface loads the whole set.** `explorer.NewPaneRoots` reaches `NewTreeRoots`, which loads `hidden.Load(canonicalRoots(roots))`; `picker.NewRoots`, `search.NewPaneRoots` and `ls` all load the full root set, so the retired per-root `.raj/hidden` divergence cannot recur. In production every caller passes `wsRoots.All()`, which `workspace.New` has already made absolute, deduped and non-nested.
- **The migration runs before any pane builds rules.** `migrateHiddenConfig` is called at `internal/app/app.go:602`; the `&App{...}` that constructs the explorer, search and picker is built later in the same function.
- **No surface reads `<root>/.raj/hidden` any more.** The only production reader is `migrateHiddenConfig` (`internal/app/session.go:276`); the `.raj/` default hides the whole directory and no rule un-hides `.raj/hidden` (`TestRajesOwnStateIsHidden`).
- **A fresh launch writes nothing into the project.** The store opens under `session.StateDirForRoots` and the config under XDG; `TestFreshLaunchLeavesProjectClean` asserts no `<root>/.raj` after open and save.
- **The drawer reset is per-pane and discriminating.** `drawerSelPane` re-resolves `drawerOpenWant()` on the first frame after the active pane changes under an open drawer; `TestPhoneDrawerSelectionResetsOnTabSwitch` moves the selection off the review default, switches to a second pending tab whose panel carries the same buttons, and asserts the default is re-selected, so a clamp cannot mask the reset. `TestClientPhoneDrawerRefusedDecisionKeepsSelection` pins that a refused decision leaves the selection put.
- **The socket gate test is discriminating.** `TestHelloOverTCPDowngradesAHuman` dials TCP, checks the requested human joins as an agent and an absent `Kind` does too; `TestHelloOverTheSocketDeclaresAHuman` asserts the fixture transport is `unix` before checking the human is not an agent, so it cannot pass by driving a port.

### Escalation resolved

- Wave 3 escalated that any token holder could `hello` with `Kind: "human"` and bypass the proposal gate over the shared token. This wave closes it: `hello` grants `KindHuman` only when `network == "unix"`, so a TCP caller is downgraded to an agent (`internal/control/control.go:1338`). The remaining hole the Wave 3 note named — a client that still believes it is human — is the promoted TODO item under "Client mode and attach".

### Friction (raw, deduped)

- **A failing test fixture built the fault with a non-directory ancestor and asserted `IsNotExist`.** `os.Stat` on a child of a file is `ENOTDIR`; the honest absence check is `err == nil`. The container has no Go toolchain, so this class of fixture bug is invisible until the host gate. The test-integrity rules should call out the `stat`-error shape.
- **Brief quality: the hidden→XDG brief named the Go files and tests but not README.md**, whose Configuration section spelled `.raj/hidden` three times. A brief that moves a path should name the prose that spells the old path; the sweep fell to the review pass.
- **An implementer could not amend its own pending draft.** `w4-hidden` found its own proposed set on `internal/hidden/hidden.go` unwanted and had to `reject --all --mine` then `clear --all --mine` before re-applying, re-reading the version after. There is no "replace my own set" surface; the only route is reject+clear+re-apply. Worth a verb (or `apply --replace`) if re-drafting recurs.
- **The ledger is not wave- or agent-scoped.** Rows carry only `session`, so the wave's agents are found by grepping their command text for `--name`; a wave or task id on the row would make duty 6 exact. Already filed in the Wave 3 block.

### Ledger (upper bounds; counts are `raj ctl`-bearing bash rows and substring matches)

- `w4-hidden` session `ses_f34c0bc69ffe2nDGkOGWYyKwdv`: ~83 calls (under the ~100 flag). read 40, search 30, lsp 10, goto 7, edit 7, groups 3, clear 2, buffers 2, register/whoami/claim/apply/reject 1 each. Batching: multi-target read 33/40 (~83%), multi-path `lsp diagnostics` 10/10, `search --context` 8/30 (~27%), `apply --hunks` 1 of 8 edit/apply calls (~13%; the rest were single string edits).
- `w4-drawer` session `ses_f34d11333fferxjYj1oyTyMi4q`: ~47 calls. read 22, search 14, apply 4, lsp 3, goto 3, version/register/claim 1 each. Batching: multi-target read 22/22, multi-path `lsp diagnostics` 3/3, `search --context` 9/14 (~64%), `apply --hunks` 2/4.
- `w4-review` session `ses_f34a5f565ffeMbKGuBYgZDJ4cZ` (this pass): ~82 calls — self-flag on `search --context` 0/34 (0%); multi-target read 44/67 (~66%), multi-path `lsp diagnostics` 5/6, `apply --hunks` 0 (every edit was a single-hunk `edit`). The recipe counts the review's own recipe commands and multi-verb shell loops, so these are upper bounds.
- Global: `apply --hunks` and `search --context` remain the low-adoption forms.

## Between-wave review — proposal-tint contrast (Wave 5, 2026-09-22)

State at entry: no pending proposals, every buffer saved (the wave had been
accepted and saved). No set was superseded or wedged, so nothing needed
disposal. The wave touched `internal/ui/style.go`, `internal/ui/contrast_test.go`,
`internal/editor/render.go` and `internal/editor/proposals_test.go`; all of
`internal/ui` and `internal/editor` read `ok` under `lsp diagnostics`.

### Verified as a reader (nothing run; no Go toolchain)

- **The WCAG math is right.** `linearChannel` uses the 0.03928 threshold and the
  2.4 exponent; `Luminance` weights 0.2126/0.7152/0.0722; `ContrastRatio` is
  `(Lhi+0.05)/(Llo+0.05)`, so black against white is exactly 21. `PaletteRGB` is
  the standard xterm cube (`cubeLevel`: 0, then 55+40v) and grey ramp
  (`8+10(n-232)`), and 0-15 plus `Default` stay unresolved by design. `LegibleOn`
  breaks the tie to black and returns `Default` for an unknown background.
- **The render override is scoped and layered.** In `drawLine` the tint branch
  sets `style.On(bg)`, then for `th.ProposedAdd` and (after this pass)
  `th.AgentTint` — one shared branch — `style.With(ui.LegibleOn(bg))`; the find,
  selection, bracket and cursor layers below still replace or overlay the style
  exactly as before. `authorTints` only ever emits those two colours, so nothing
  else reaches the branch.
- **The guard tests discriminate.** The proposed-comment fixture builds a real
  `//` comment (`ClassAt == ClassComment`) whose syntax colour is `ui.Ansi(8)`.
  The pre-check asks "would the unmodified style pass the final `ok && ratio >=
  4.5` assertion"; for `Ansi(8)` `ContrastRatio` is `ok=false`, so the pre-check
  passes and the final assertion fails without the override. The new
  `TestRenderAgentCommentStaysLegible` is the same shape over `th.AgentTint`.
  Both tints resolve (16-255) and `LegibleOn` picks white: `AgentTint`
  (`Ansi(22)`, `(0,95,0)`) at 7.97 and `ProposedAdd` (`Ansi(28)`, `(0,135,0)`)
  at 4.70, so the review green clears the 4.5 bar narrowly.
- **No existing test pins a tint's syntax foreground.** Searched every
  `AgentTint`/`ProposedAdd` use: the tint tests assert `Bg` only, and the only
  `.Fg` assertions in `internal/editor` are the two new contrast tests, so
  extending the override to `AgentTint` broke no fixture. `layered_test.go` is
  about save/lease/index, not rendered layers.

### Flagged (read-only; not changed)

- **Medium: the syntax palette is entirely 0-15, so `ContrastRatio` can never
  compute a syntax foreground against a tint.** Every `styleFor` case returns
  `ui.Ansi(0..15)` — the terminal-owned half the resolver refuses — and the
  comment is `Ansi(8)`. The fix works by *replacing* the foreground with a
  resolvable 16-255 colour, not by measuring it, which is why both guard tests
  read "unresolvable ⇒ fails the final check" rather than a computed ratio. A
  test asserting a numeric sub-4.5 ratio on the original colour is impossible
  with this palette; a later editor must not "fix" the guard into `r < 4.5`,
  which the unresolvable colour would satisfy wrongly.
- **Medium: `AgentTint` now overrides the foreground for all accepted agent
  text, not only comments.** The hazard class is real, but the trade changes:
  `ProposedAdd` is transient, while accepted agent text is long-lived, so
  agent-written code loses its syntax colours to white-on-green for as long as
  the text stays attributed. `drawLine`'s own comment said "the tint and the
  highlight keep the token's foreground, so agent-written code is still
  syntax-coloured"; this pass makes the two green tints the stated exception.
  The user approved proceeding; recorded as the trade, not a defect.
- **Low: `HighlightRead`/`HighlightWrite` carry the same keep-the-foreground
  hazard.** Both are applied with `style.On(bg)` and are dark (`Ansi(236)`,
  `Ansi(54)`), so a comment token on a highlighted occurrence can disappear the
  way it did on the proposal green. Left unchanged and filed in TODO. Find
  (`FindMatch`/`FindActive`), `Selection` and `Caret` replace the whole style
  including the foreground, so their exposure is the terminal's default
  foreground on those backgrounds — a theme question, not the syntax-grey one.

### Changed (proposals, not accepted)

- `internal/editor/render.go:369,372` — the tint override is one branch for
  `th.ProposedAdd || th.AgentTint`; the stale "the review green is the
  exception" line above now reads "the two green tints".
- `internal/editor/proposals_test.go:180` — `TestRenderAgentCommentStaysLegible`
  (accepted agent comment keeps `AgentTint` and clears 4.5).
- `docs/COMPLETED.md` — the contrast-fix entry; `docs/TODO.md` — retired the
  "The proposal tint is hard to read" item and filed the highlight-foreground
  item; this block.

### Ledger (upper bounds; counts are `raj ctl`-bearing bash rows)

- `w5-contrast` session `ses_f349484c1ffetv1XczgIsovqCj`: ~55 tool calls (55
  distinct bash `callID`s), under the ~100 flag. Verb mix: read 15, search 14,
  lsp 5, goto 4, apply 4, buffers 2, register/open/edit/claim 1 each. Batching:
  multi-target read 15/15 (100%), multi-path `lsp diagnostics` 5/5 (100%),
  `search --context` 6/14 (~43%), `apply --hunks` 3/4 (75%). The best adoption
  any wave has reported; no low-adoption flag.
- `w5-review` session `ses_f3472c58fffeVWzUinILJF5h4Q` (this pass): ~52 tool
  calls as of this block's write, under the ~100 flag. Verb mix: read 15,
  search 13, lsp 6, edit 4, claim 2, register/whoami/status/proposals/groups/
  buffers/version/apply/--help 1 each. Batching: multi-target read 11/11,
  multi-path `lsp diagnostics` 4/4, `apply --hunks` 0 (all single-hunk `edit`s),
  `search --context` 1/6 — self-flag on `search --context`, but the searches
  were tree/symbol discovery and enumeration, not hit-then-read.
- The orchestrator session (`ses_f358446f4ffeOEm6Frv330LTXx`) is cumulative
  across every wave (~346 bash rows) and not wave-scoped, so duty 6 stays
  approximate for it; already filed in the Wave 3 and Wave 4 blocks.

### Friction (raw, deduped)

- **The ledger's row shape is inconsistent, and the documented recipe
  over-counts.** A call's invocation and result are two rows; some invocation
  rows carry `cmd` with no `phase`, others carry `phase:"after"` with no `cmd`.
  `select(.phase != "after")` counted 78 rows for `w5-contrast` against 55
  distinct `callID`s. Counting distinct `callID`s is the honest call count;
  normalise the row shape or note the recipe's bias.
- **`version` takes one path only.** Getting bytes+version for the three doc
  files took three calls; a multi-path `version` (or just the multi-path
  `read --json` already in use) would batch it. Small surface gap.
