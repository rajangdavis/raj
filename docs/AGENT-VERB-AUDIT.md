# Agent verb-surface audit — `raj ctl`

Read-only audit of the running editor (author 3; root `/work` → `/Users/rajandavis/Desktop/projects/raj`), 2026-09-13.
Sources: `docs/RAJ_FEEDBACK.md`, TODO's "Agent feedback — actionable", `raj ctl -h` plus per-verb `-h`, and `internal/control/{cli,host,control}.go`, `internal/app/control.go`. No code changed.

## Inventory

- **Inspect/size**: `buffers`, `read`, `version`, `dump`, `search`, `lsp`, `whoami`, `who`, `list`, `stats`
- **Write**: `apply`, `edit`, `patch`, `run -prog`, `save`
- **Lifecycle/present**: `open` (`-create`), `close`, `goto`
- **Review/decide**: `groups`, `diff`, `review`, `accept`, `reject`, `clear`
- **Talk/run**: `recv`, `exec`

Protocol-only ops with no CLI verb: `ping` (used by `whoami`), `text` (the `read` op), `execcheck`, `snapshot`, `lspprep`, `prog`, `hello`, `cancel`. `help`/`-h` are handled but absent from the usage list.

## Friction → root cause

1. **Path spelling is not all translated (path resolution).** Relative, `/work`-absolute and editor-spelled paths all resolve *inbound* — `read` verified for all three — and `buffers`/`groups` come back `/work`-spelled. But `diff -json`'s `path`, `search -json`'s `truncated[].Path`, and every error string keep `/Users/...`. Cause: `Client.localise` (client.go) rewrites a fixed field list (`Root`, `Buffers`, `Matches`, `Dirty`, `Groups`) and misses nested `DiffJSON`, `Truncated` and `Err`. One seam, applied unevenly. README claims "what `search` prints is something the agent can open" — `truncated` already contradicts it.
2. **The default target is silent (defaults/discovery).** `apply`/`edit`/`read`/`save`/`close` take an optional positional; omit it and the verb targets whatever tab is active. Verified live: `edit -old <absent>` with no path read the active buffer, and the refusal says "the buffer" without naming it. `-h` prints only the shared FlagSet, so no verb's positional is shown.
3. **Verb redundancy.** `read`/`version`/`dump` overlap; `apply`/`edit`/`patch`/`run` are four write doors; `open -create` is the only create and "create" is not a verb; `groups`/`diff`/`review` list the same sets three ways; `accept`/`reject`/`clear` are three decisions over one state machine.
4. **Lease and error recovery.** Writing over **another writer's** proposal refuses with "change set N owns this text; accept or reject it first"; recovery is reject → `clear` → re-apply. Writing over your **own** `Proposed` set now amends it instead (2026-09-13). `clear` exists but is documented only on its usage line. The session's reported `accepted 0 ops` artifact did not reproduce (the string is absent from the tree).
5. **Output shape — fixed 2026-09-13.** `search -json`, `stats -json` and `run -json` now carry snake_case json tags (`path`/`byte_start`/`line_start`, `runs`/`stale`/`agent_only`), matching `read`/`buffers`/`groups`/`diff`/`version`/`whoami`/`apply`/`lsp` and `search -jsonl`. The contract is snake_case throughout.
6. **Lifecycle gaps.** Create is only `open -create` (no parent-dir handling); no `delete`, no `rename` — those still hand work back to the host.
7. **No region lease.** `apply` rejects a stale hunk after the fact; nothing lets two writers claim a span up front (`claim`/`watch` still unimplemented).

## Ranked simplifications

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

## Safe now (no semantics change)

- Extend `localise` to nested `DiffJSON`/`Truncated` and to `Err` text.
- Per-verb usage text: positionals plus the default target; name the buffer in default-target refusals.
- One `-json` field-naming contract; the three Go-name verbs move to it.
- Document the `clear` recovery path and `open -create` in README and the skill.
- Add the affected line/col to `apply`/`edit` replies.

## Needs a design pass

- Making the write target explicit (mandatory path or `-active`), which decides the footgun permanently.
- Merging `read`/`version`/`dump` and collapsing the write verbs onto one door.
- `delete`/`rename`/parent-dir create, and whether a file op is immediate like `open` or a review decision like text.
- Region leases (`claim`/`watch`) and overlap reporting.

## Leave alone

- `accept`/`reject`/`clear`: a state machine, not redundancy.
- `apply` (offsets) vs `edit` (quoted string) vs `dump`/`patch` (editor-side diff): three real choices for three situations.
- The read-before-write gate; proposals with the save refusal; `exec` refused over TCP; `run -prog` as the batch door.
- `search` refusing a bare positional; `open` refusing a typo without `-create`.

## Already fixed — do not re-file

- Headless read: `read`/`version`/`lsp diagnostics` load on demand with no tab; `open` now means show (headless-read spec).
- A typo'd `open` is refused unless `-create` (verified live).
- Rebuilt client: `-create` is parsed and forwarded; the "no create verb" report was a stale container binary.
- Registry: gone ids recycled at the 255 cap skipping id 1; `Registry.Seed` keeps attribution across restart (COMPLETED.md).
- Relative/absolute/editor-spelled paths all resolve inbound (`read` verified); only response-side spelling lags (cause 1).
- Identity: version handshake, server-minted `tok_...`, per-session `RAJ_IDENTITY` absorption by the plugin.
- `reject -all` newest-first, line-index corruption, search `LineStart` — in tree, host verification pending (TODO `[~]`).

## Undocumented / unreachable

- `clear`: reachable and in the usage block, but absent from README, docs and the skill.
- `help`/`-h`: reachable, not listed in usage.
- No CLI verb is unreachable; the protocol-only ops above are internal by design.
