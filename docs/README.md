# docs — index

Every document in this directory, grouped by the role it plays, and the rule
for where new material goes.

## Read first

| doc | kind | purpose | read it when |
| --- | --- | --- | --- |
| [NEXT-STEPS.md](NEXT-STEPS.md) | master | the master direction doc: where each thread stands, the next push, its sequencing, and the design until a wave is scheduled | you are deciding what to do next, or designing something not yet scheduled |
| [TODO.md](TODO.md) | work list | open, active and unscheduled work | you are picking the next thing to do |

## Record

Dated entries only; the facts a decision rests on.

| doc | kind | purpose | read it when |
| --- | --- | --- | --- |
| [COMPLETED.md](COMPLETED.md) | record | what works and what has been fixed, dated | you need to know whether something already landed |
| [BENCHMARKS.md](BENCHMARKS.md) | record | measured numbers | you need a figure, not an impression |
| [INVESTIGATIONS.md](INVESTIGATIONS.md) | record | terminal findings, root causes, decisions and unscheduled directions | you are about to relitigate a design or a fix |

## Active specs (workspace + git)

The specs the current push is being built from.

| doc | kind | purpose | read it when |
| --- | --- | --- | --- |
| [LAYERED-PROPOSALS-SPEC.md](LAYERED-PROPOSALS-SPEC.md) | spec | the layered-proposals model, projection, decisions and the `proposals` rollup | you are touching proposals, composition or review state |
| [HOOKS-SPEC.md](HOOKS-SPEC.md) | spec | user-authored host actions with triggers: `hook` authoring, the agent `run` verb, gating and git integration | you are touching hooks, format-on-save, the pre-gate diagnostics sweep or host command execution |
| [GIT-COMPATIBILITY.md](GIT-COMPATIBILITY.md) | spec | `GitView`, `Materialise` and the git capability set hooks consume; the first carved spec (`NEXT-STEPS` step 1), with an open hooks sketch | you are touching git read, materialisation or the git hook capabilities |
| [CHANGES-ORGANIZATION.md](CHANGES-ORGANIZATION.md) | spec | units, tension, and the workflow actions — export, publish, summon, resolve — over the journal and git | you are touching how changes are grouped, reviewed, exported or resolved |

## Reference

| doc | kind | purpose | read it when |
| --- | --- | --- | --- |
| [KEYBINDINGS.md](KEYBINDINGS.md) | reference | every chord, generated from `keys.Bindings` | you are adding or looking up a keybinding |

## Active but not this push

Referenced by open TODO Now items, but not part of the current push.

| doc | kind | purpose | read it when |
| --- | --- | --- | --- |
| [AGENT-COORDINATION-SPEC.md](AGENT-COORDINATION-SPEC.md) | direction | chat, tasks, a roster and agent supervision inside raj, built from the primitives it already has; bring-your-own workflow | you are designing agent chat, tasks, or raj starting agents |
| [MOBILE-REVIEW-SPEC.md](MOBILE-REVIEW-SPEC.md) | spec | the `--phone` profile (which implies `--attach`): touch tabs, transient status, a review bar, ctrl aliases for super | you are changing the phone layout or its controls |
| [F3B-II-DESIGN.md](F3B-II-DESIGN.md) | design | the presentation half: folds and the display map | you are changing how proposals are rendered |

## Landed — kept for reference

Behaviour that shipped; retained for the design record. A landed doc is deleted
once its content is folded into a dated record — git keeps the history, and
`internal/docsindex` enforces that a `retired` row has no file.

| doc | kind | purpose | read it when |
| --- | --- | --- | --- |
| [ATTACH-DESIGN.md](ATTACH-DESIGN.md) | landed design | workspace-scoped host and the local-render attach client that landed, plus the remaining detach/reattach and concurrent-view direction | you are designing multi-instance, a shared session or phone+laptop views |
| [HARNESS-BROKER-AGENT.md](HARNESS-BROKER-AGENT.md) | landed design | how an agent authors changes against the live buffer | you are designing the agent/broker boundary |
| [CURSOR-VIEWPORT-SPEC.md](CURSOR-VIEWPORT-SPEC.md) | landed spec | cursor and viewport position invariants | you are touching movement, scroll or their tests |
| [FILE-LIFECYCLE-SPEC.md](FILE-LIFECYCLE-SPEC.md) | landed spec | create/delete/rename design and verbs | you are changing file-lifecycle verbs |
| [CLAIM-SPEC.md](CLAIM-SPEC.md) | landed spec | the claim gate and its verb | you are changing claims or any claim-gated verb |

## This file

| doc | kind | purpose | read it when |
| --- | --- | --- | --- |
| [README.md](README.md) | index | this file: what each doc is for and where a new document or note belongs | you are deciding where a new document or note belongs |

## Where new material goes

NEXT-STEPS.md is the master: direction, sequencing and design live there until a
piece is scheduled. A new file is the exception, not the default.

- something that happened (a fix, a measurement, a decision, feedback) → a
  dated entry in COMPLETED / BENCHMARKS / INVESTIGATIONS / AGENT-FEEDBACK,
  never a new file;
- open work → TODO;
- direction and design, scheduled or not → NEXT-STEPS.md;
- a scheduled subsystem's contract → carve a `*-SPEC.md` out of NEXT-STEPS when
  that wave starts, and add a row here. Never create a file for an unscheduled
  idea;
- one-shot audit/review → a dated section in AGENT-FEEDBACK, not a file;
- a landed spec or design stays in this index (grouped under Landed) until its
  content is folded into a record; then the file is deleted and the row marked
  `retired` — never moved into a subdirectory, which the flat index cannot name.

Dev-process documents live under `docs/dev/` and are deliberately outside this
index: they are not part of the product, and a row here cannot name a path in a
subdirectory.
