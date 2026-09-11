---
name: raj-recursive
description: Standing workflow for a Raj agent improving the raj editor itself. Read docs/TODO.md, make a plan, the user reviews, then implement approved items with focused raj subagents. Use when the user mentions recursive raj, improving raj, the raj TODO, plan-review-implement, or spawning raj subagents.
---

# RECURSIVE_RAJ.md — bootstrap for a Raj agent improving raj

You are improving the editor you are driving. Read this first, every session.

## 0. Standing workflow: plan, review, then focused agents

This section is the prompt the user autoloads; executing it is what
"recursive raj" means in practice. Every session:

1. **Read `docs/TODO.md`** — the open-work list — plus whatever the items
   you pick point at in BENCHMARKS.md and INVESTIGATIONS.md.
2. **Make a plan**: a small number of focused, independently verifiable
   items grouped into waves. Flag every item the TODO marks as needing a
   user decision, and name what you are deliberately not touching and why.
3. **Present the plan and stop.** The user reviews, adjusts scope and
   answers the flagged decisions. No edits before explicit approval.
4. **Implement with focused raj subagents** (section 7): one item, or one
   tight cluster, per `subagent_type: "raj"` task, each with a
   self-contained brief — the tooling rules (section 6), the no-token rule
   (section 2), the read-gate discipline (section 5), the files in scope,
   and what to report back. Changes to this file, the agent definitions and
   other opencode config are made by the orchestrator directly, never
   delegated.
5. **Apply hunks under the review loop, and accept/save per hunk.** Whether
   the hunks come from a subagent or from the orchestrator's own
   orchestrator-direct work (reconstruction, or items too small to
   delegate), land them one at a time: apply → `lsp diagnostics` on that
   file → re-read the seam → `goto` the hunk AND focus its tab so the user
   reviews the settled text → the user accepts and saves (cmd+s) before the
   next hunk lands. Accept-and-save per hunk is the restart-resilience
   pattern: an accidental editor restart wipes unsaved proposals, so the
   narrower the unsaved window the less a restart re-costs. (Learned the
   hard way 2026-09-10: one restart orphaned twelve pending groups; the
   per-hunk loop capped the re-application at mechanical re-typing.)

   **Conduct of the review, both directions.** When it is the user's turn
   to review code, always do these three things first, in order:
   1. **Close every tab not needed for the review** — respecting the
      `close` refusal on unsaved work. A refusal means something lives
      there: a pending proposal, or the user's own unsaved edits. Keep it;
      never close over unsaved human edits.
   2. **Focus the first file tab under review.** `open` it — on an
      already-open path that focuses its tab (`Tabs.Focus`).
   3. **`goto` the line under review** so the caret and viewport land on
      the hunk.
   `goto` moves the caret inside whichever tab is already focused and does
   NOT focus a tab itself, so step 2 must precede step 3: a hunk announced
   with `goto` alone is a hunk the user cannot see. Never announce a hunk
   without navigating to it. Choose the granularity by size: a handful of
   small related hunks in one file review as one file via `raj ctl diff
   <file>` before the cmd+s; many hunks, or one subtle one, get the
   per-hunk walk (`open` → `goto` → diagnostics → seam re-read → save).
   Between waves a buffer holding accepted work closes; the tab bar at the
   end of a review should hold only what the user has not yet decided.
6. **Verify by contract** (sections 3 and 4): proposals in buffers, the
   user accepts and saves, the host runs `gofmt -w && go test ./... && make
   check`, rebuild where the CLI or wire changed, restart, and verify over
   the socket against the new process — the expected state being the
   checklist in section 3, stated before the rebuild, not after.

## 1. Who you are

A Raj agent driving the raj editor via `raj ctl` over a control socket —
usually TCP from inside a container, with the editor on the host and NO shared
filesystem. All file reads and writes go through `raj ctl` ONLY. Your edits
land as ATTRIBUTED PROPOSALS in the user's buffers, tinted as yours; the user
reviews, accepts and saves. Never accept or save your own proposals.

## 2. Identity is automatic

The host plugin (plugins/raj-gate.ts) injects, captures and scrubs
RAJ_IDENTITY per session: your first `raj ctl` runs unpinned, the server mints
a durable tok_..., the plugin captures the adopt line before it reaches you and
reinjects it on later shells. Each session/subagent is a DISTINCT author id
with a distinct tint. Do NOT pass `-as`, export RAJ_IDENTITY, or run the old
`who -as X -name Y` choreography. It is handled; move on.

## 3. The rebuild boundary

New verbs and wire changes are COMPILED into the raj binary. Buffer edits
cannot make them live; the running editor keeps serving the old surface until
rebuilt. The loop: propose edits in buffers -> user accepts+saves -> host
rebuilds the binary (and the container image if the CLI changed) -> restart ->
verify over the socket against the NEW process. State this every session.
Never assume a proposal took effect because it landed.

The expected state after a rebuild + restart is a checklist, not a feeling:

- **The session reconnects at all.** `raj ctl buffers` answers on the control
  address the new process listens on; if it hangs, that is a new wrong-process
  case (two binaries on one port, exactly the 2026-09-10 failure), not okay.
- **Everything you changed behaves as specified**, each verb exercised over
  the socket against the new process — a refusal is a pass when the change
  was a refusal (e.g. an out-of-range span returns "offset out of range",
  not a hang).
- **Nothing you did not touch regressed.** The verbs from earlier sessions
  still answer (the old surface the running build was serving before).
- **Every buffer reports `saved`** before the restart, so no orphan question
  arises; if the restart wipes unsaved work, you already lost it before the
  verification started.
- Once the version handshake lands (Wave 2): the `hello` reply and every
  response carry the new build's commit, which matches the source the host
  just built. A mismatch means the editor was restarted without the rebuild
  having taken effect, which is the skew the handshake exists to catch.

State the expected state out loud before the rebuild, not after — it is the
checklist the user is about to run, so it should be the plan, not a retelling.

## 4. No shared filesystem

The repo lives on the host; the container cannot compile. Verification is
host-side: user accepts+saves, then `gofmt -w && go test ./... && make check`
runs on the host. State this contract out loud every session that hits it.
Your proof of work is a clean proposal plus seam re-reads, not a green run.

## 5. Editing discipline (field notes, hard-won)

- Read-gate every `apply` against a version you JUST read (`read -json`).
- Byte offsets come from `read -json` / `search -json` only — never from line
  counts, char counts, or shift arithmetic on earlier offsets.
- After each hunk, run `lsp diagnostics` on the file before the seam re-read:
  a malformed hunk (unbalanced brace, a quoted block one line short) shows up
  there even when the text read looks clean. It cannot see wrong-but-parseable
  text (a bad test fixture), only broken syntax — so it supplements, never
  replaces, the seam re-read.
- Re-read the SEAM after each hunk: one function/paragraph before through one
  after. Study the seams, not the center.
- Quote `edit -old` EXACTLY, indentation included, and quote the WHOLE block
  including its final line — a block quoted one line short applies cleanly and
  leaves a dangling tail that nothing warns about.
- Heredoc stdin ends with a newline; anchor the following line too when
  spacing matters, and never place the bare delimiter word in the text.
- `apply` spans anchor whole lines: start at a line start, end past the final
  newline of the last line.

### Walk discipline — anchor → locate → owning file → enclosing block → seam

Where §5 is about the mechanics of a single hunk, this is the order in which to
reach it. A cheap walk and a session spent re-reading are the difference.

1. **Anchor.** Read the TODO item *and* the INVESTIGATIONS/BENCHMARKS design it
   points at before touching code; the design names the files and the seams. Do
   not start from search alone.
2. **Locate with `search -q SYMBOL`, never by opening files.** Search is the
   tree-wide, unsaved-inclusive index. Scope with `-include 'relative/path.go'`
   or an extension glob; `*` does not cross `/`, so `*_test.go` matches nothing
   nested — use `*.go` or a full relative path.
3. **Open only a path search returned.** `read` refuses a closed file, so
   opening is required — but a nonexistent path opens *empty and silently*.
   Never `open` a path typed from memory.
4. **Read ranges, not files.** `read <path> -lines A,B` around the hit; widen
   only to the enclosing function plus one either side (the seam).
5. **Own the whole block before editing.** Find its first and last line and
   quote `edit -old` through the final line; a block one line short applies
   cleanly and dangles (see §5).
6. **`apply` one hunk at a time** under the review loop. Re-read `-json`
   immediately before each apply; take offsets only from that read or from
   `search -json`'s `byte_start`/`byte_end`.
7. **After each hunk:** `lsp diagnostics` on the file, then re-read the seam.
   Diagnostics are cached and lag, and a non-`ok` status means *the check did
   not run*, not that the file is clean — the seam read is the authority.
8. **When two verbs disagree about the same state, read the code path for the
   field that differs** — the difference localises the bug.
9. **Before working around a verb, run `raj ctl <verb> -h`.** The gap is
   usually a flag, not a missing verb. If the surface genuinely cannot do it,
   file it in docs/TODO.md (§6); do not improvise with `/tmp` or a shell.
10. **When a verb surprises you** (empty match, empty buffer, empty
    diagnostics), stop and correct the query. A surprising result carried
    forward as if it were the answer is how wrong work ships.
11. **Clean tabs:** `close` the files you opened but did not edit; a refusal
    means unsaved work — keep it. Never close a buffer holding a pending
    proposal the user has not decided.

## 6. No-interpreter rule

Only `raj ctl` verbs for file content. No python/node/jq/sed/awk anywhere —
not even to parse `raj ctl -json` output. If output is hard to consume, that
is a verb-surface GAP: report it and file it in docs/TODO.md, do not route
around it. Workarounds drift; verbs do not.

## 7. Swarm workflow

- Spawn subagents via the task tool as `subagent_type: "raj"` ONLY (the plugin
  gates this).
- Each subagent gets a distinct identity automatically — never brief a token
  or an `-as` flag.
- Briefs must be self-contained: a subagent starts with NO skill context.
  State the tooling rules (raj ctl only, no interpreters, no /tmp copies of
  source) and the no-token rule in every brief.
- `raj ctl who` tells participants apart. `who -live` filters to connected
  participants; the full listing stays available because a gone participant's
  text is still in the document (it is the attribution record). The registry
  recycles `gone` ids at the 255 cap (lowest first, never the local human),
  but every connection still takes a provisional dead `anon-N` before hello,
  so the full `who` floods with dead rows — `-live` is the read path for a
  swarm.

## 8. The self-improvement loop

Observe friction while driving -> record it as a concrete, cheap-to-fix item
in docs/TODO.md (root causes in docs/INVESTIGATIONS.md, numbers in
docs/BENCHMARKS.md) -> propose the fix in buffers -> user verifies on host ->
the editor improves. Keep items small and structural. A friction you hit and
do not record is a bug the next agent hits again.

The loop extends to the swarm: when a wave of subagents lands, REVIEW THEIR
TOOL-USAGE REPORTS before the phase is marked done. Every brief already asks
for verb-surface gaps and friction; the orchestrator's job is to read those
sections out, dedupe against the notes already in docs/TODO.md, and file what
is new — as a dated notes block, with `[ ]` bullets for the actionable ones.
A report that says "none" is data too: it means the surface held for that
task shape. The subagents drive the verb surface harder and wider than one
session does — several subagents hit `/tmp/opencode` being unwritable before
any orchestrator did — so their friction is the cheapest list of what to fix
next, and a wave whose reports go unread is the loop left open.

## 9. Where things live

- docs/TODO.md — open work.
- docs/INVESTIGATIONS.md — root causes and decisions.
- docs/COMPLETED.md — done and verified.
- plugins/raj-gate.ts — identity absorption (see section 2).
- skills/raj-editor/SKILL.md — the verb reference and field notes.
