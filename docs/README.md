# docs — index

Every document in this directory, and the rule for where new material goes.

| doc | kind | purpose | read it when |
| --- | --- | --- | --- |
| [TODO.md](TODO.md) | work list | open, active and unscheduled work | you are picking the next thing to do |
| [COMPLETED.md](COMPLETED.md) | record | what works and what has been fixed, dated | you need to know whether something already landed |
| [BENCHMARKS.md](BENCHMARKS.md) | record | measured numbers | you need a figure, not an impression |
| [INVESTIGATIONS.md](INVESTIGATIONS.md) | record / direction | terminal findings, root causes, decisions and unscheduled directions | you are about to relitigate a design or a fix |
| [AGENT-FEEDBACK.md](AGENT-FEEDBACK.md) | record | raw, dated agent feedback, reviews and one-shot audits | you are working the agent surface or the review loop |
| [KEYBINDINGS.md](KEYBINDINGS.md) | reference | every chord, generated from `keys.Bindings` | you are adding or looking up a keybinding |
| [RECURSIVE-RAJ.md](RECURSIVE-RAJ.md) | workflow | the standing workflow for a Raj agent improving raj | you are a Raj agent starting a session |
| [REVIEW-AGENT.md](REVIEW-AGENT.md) | workflow | the between-wave review pass contract | you are running the review between implementation waves |
| [CLAIM-SPEC.md](CLAIM-SPEC.md) | spec | the claim gate and its verb | you are changing claims or any claim-gated verb |
| [CURSOR-VIEWPORT-SPEC.md](CURSOR-VIEWPORT-SPEC.md) | spec | cursor and viewport position invariants | you are touching movement, scroll or their tests |
| [FILE-LIFECYCLE-SPEC.md](FILE-LIFECYCLE-SPEC.md) | spec | create/delete/rename design and verbs | you are changing file-lifecycle verbs |
| [LAYERED-PROPOSALS-SPEC.md](LAYERED-PROPOSALS-SPEC.md) | spec | the layered-proposals model, projection, decisions and the `proposals` rollup | you are touching proposals, composition or review state |
| [F3B-II-DESIGN.md](F3B-II-DESIGN.md) | design | the presentation half: folds and the display map | you are changing how proposals are rendered |
| [HARNESS-BROKER-AGENT.md](HARNESS-BROKER-AGENT.md) | design record | how an agent authors changes against the live buffer | you are designing the agent/broker boundary |
| [README.md](README.md) | index | this file: what each doc is for | you are deciding where a new document or note belongs |

## Where new material goes

- something that happened (a fix, a measurement, a decision, feedback) → a
  dated entry in COMPLETED / BENCHMARKS / INVESTIGATIONS / AGENT-FEEDBACK,
  never a new file;
- open work → TODO;
- required behaviour for a subsystem → `*-SPEC.md` (and add a README row);
- unscheduled direction → `*-DESIGN.md` or INVESTIGATIONS "Direction", retired
  when scheduled or rejected;
- one-shot audit/review → a dated section in AGENT-FEEDBACK, not a file.
