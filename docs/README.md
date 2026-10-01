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
| [ARCHITECTURE.md](ARCHITECTURE.md) | reference | what raj is made of and what each thing is called, described from the tree; spec/code disagreements listed, not decided | you need the parts and their names before touching an unfamiliar subsystem |

## Active but not this push

Open designs that are not part of the current push.

| doc | kind | purpose | read it when |
| --- | --- | --- | --- |
| [AGENT-COORDINATION-SPEC.md](AGENT-COORDINATION-SPEC.md) | direction | chat, tasks, a roster and agent supervision inside raj, built from the primitives it already has; bring-your-own workflow | you are designing agent chat, tasks, or raj starting agents |
| [MOBILE-REVIEW-SPEC.md](MOBILE-REVIEW-SPEC.md) | spec | the `--phone` profile (which implies `--attach`): touch tabs, transient status, a review bar, ctrl aliases for super | you are changing the phone layout or its controls |

## This file

| doc | kind | purpose | read it when |
| --- | --- | --- | --- |
| [README.md](README.md) | index | this file: what each doc is for and where a new document or note belongs | you are deciding where a new document or note belongs |

## Where new material goes

NEXT-STEPS.md is the master: direction, sequencing and design live there until a
piece is scheduled. A new file is the exception, not the default.

- something that happened (a fix, a measurement, a decision, feedback) → a
  dated entry in COMPLETED / BENCHMARKS / INVESTIGATIONS, or in
  `docs/dev/AGENT-FEEDBACK.md` for feedback, never a new file;
- open work → TODO;
- direction and design, scheduled or not → NEXT-STEPS.md;
- a scheduled subsystem's contract → carve a `*-SPEC.md` out of NEXT-STEPS when
  that wave starts, and add a row here. Never create a file for an unscheduled
  idea;
- one-shot audit/review → a dated section in `docs/dev/AGENT-FEEDBACK.md`, not a
  file;- one-shot audit/review → a dated section in AGENT-FEEDBACK, not a file;
- a landed spec or design stays in this index (grouped under Landed) until its
  content is folded into a record; then the file is deleted and the row marked
  `retired` — never moved into a subdirectory, which the flat index cannot name.

Dev-process documents live under `docs/dev/` and are deliberately outside this
index: they are not part of the product, and a row here cannot name a path in a
subdirectory.
- a landed spec or design stays in this index under Reference until its open
  remainder is folded into a record or the TODO; then the file moves to
  `docs/archive/` and its row is deleted. `docs/archive/` has no index.

Dev-process documents live under `docs/dev/` and are deliberately outside this
index: they are not part of the product, and a row here cannot name a path in a
subdirectory. A landed dev document moves to `docs/dev/archive/`, not to
`docs/archive/`: `docs/dev/` is git-ignored and `docs/archive/` is tracked.
