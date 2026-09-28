# AGENT-COORDINATION-SPEC — chat, tasks and supervision inside raj

Status: direction, not scheduled (2026-09-26). Written down so it is not lost;
the current push is the git integration and review flow
(`CHANGES-ORGANIZATION.md`, `GIT-COMPATIBILITY.md`).

## 1. Why raj

raj is already the hub every agent talks through. It has, today:

- **messages** — `raj ctl send --to KEY|NAME|ID|all` into per-participant
  mailboxes that `recv` reads; the sender is stamped from the connection;
- **presence** — `who`, connection-counted, with a task per participant;
- **attributed work** — proposals and change sets carrying author and task;
- **verification** — hooks, the run log, stamps (revision, HEAD, dirty), `check`
  on the projected tree;
- **lifecycle** — detached hooks that survive an editor restart.

What is missing is a surface for the person to *see and steer* all of this
from the editor, and one object — the task — that ties a brief to the work and
the checks it produced. Everything below is product: generic, harness-agnostic,
and useful with one agent or twenty.

## 2. Principles

- **Bring your own workflow.** raj provides primitives — messages, tasks,
  presence, hooks, proposals — and never a fixed process. A team that wants
  waves, a reviewer family, stacked PRs or none of it composes those from hooks
  and its own agents; the process lives in the user's hooks and harness config,
  not in raj.
- **Harness-agnostic.** Nothing names opencode, Claude or Codex. Harness glue is
  a plugin or a hook the user brings (`harness/` and `plugins/` are examples).
- **Only the person speaks as the person.** A message, approval or decision
  attributed to the human comes only from the editor itself or its local
  socket — never over TCP. The same boundary as save, hook authoring and accept.
- **Cheap for agents.** Messages and tasks are small structured records an agent
  queries, not documents it re-reads.

## 3. Stages

### 3.1 Chat pane and durable mail

- A pane listing every message — person↔agent and agent↔agent — as a timeline,
  filterable by participant or task.
- A compose line that sends **as the person** (From = the human). An agent's
  harness can therefore treat an editor message as the user's instruction, and
  "go" typed in raj is a verifiable approval: no second confirmation in the
  agent's own UI.
- Mail persists in the store (bounded), so an editor restart loses nothing and
  mail to a known key that is not connected yet still queues.
- Replaces the unused `App.Tell` prompt and makes a full mailbox visible.

### 3.2 Tasks

A task is a record: id, title, brief, assignee (participant or role), state
(queued → assigned → working → review → done → landed, or blocked), the change
sets whose Task field names it, the hook runs (stamps) that verified it, and the
messages about it.

- `raj ctl task add|assign|show|list|done|block` for agents and scripts; the
  same actions in a tasks pane for the person.
- `done` requires the stamp of a passing check at the task's revision, if the
  workspace defines a check hook (policy, not law — the user's hook decides).
- `land <task>`: the person's one gesture — accept the task's pending sets and
  save the buffers they touch.
- Tasks are where an orchestrator (an agent, a script, or the person) keeps
  state, so orchestration is no longer tied to one model's memory.

### 3.3 Working states and the roster pane

Every participant has a **working state**, so the person, an orchestrator and
raj's own lifecycle hooks know who is busy, idle, stuck or waiting.

- **Derived** from what raj already observes, with no agent change: `listening`
  (a parked `recv`, no other request for ~30 s), `working` (recent requests, or a
  hook run in flight under its id), `waiting` (quiet while holding pending
  proposals), `gone` (no open connection).
- **Declared** by the agent or its harness: `raj ctl state set
  working|blocked|review|idle [--task T] [--on user|KEY] [--note "…"]`. A harness
  plugin can report it for free (opencode's busy/idle session events). Declared
  wins over derived; derived still catches an agent that stopped reporting
  (`working` with no activity for a configurable while reads as `stale`).
- **Uses:** `who` and the roster show state, task, and time since last activity;
  a restart hook waits for every agent to be `listening`/`idle` (or asks them to
  checkpoint) instead of restarting mid-work; a dispatcher assigns only to idle
  workers; "waiting on the person" becomes a visible list instead of a prompt in
  a tab nobody is looking at.

The roster pane lists who is connected with their state, task, claim set,
pending proposals and last hook result; send-to and task-assign from the row.

**Status (W1): derived + declared working states landed.** The registry derives
`listening`, `working`, `waiting`, `stale` and `gone` from activity it already
sees, `raj ctl state set` declares one of `working|blocked|review|idle`, `who`
shows the state, and `scripts/raj-cycle.sh` step 4 waits for every connected
agent to be listening or idle before it announces a restart.

### 3.4 Supervision: raj starts agents

- Harness launchers become hooks the user authors (`agent.start`,
  `agent.stop`), detached, so raj can bring agents up and down without knowing
  what they are.
- An agent started by raj is told its identity and task on start and registers
  itself; a dispatcher mode runs one stateless job per task
  (`opencode run …`-style) instead of a long-lived session.
- **Container orchestration is the user's, plugged in through hooks.** A local
  `docker compose` file (one service per agent kind, `raj` as the shared
  endpoint, replicas for workers) or a Kubernetes Job per task are both just
  launcher hooks; raj needs only: start with an identity and a brief, report
  when it registers, report when it exits. Auto-registration is the agent's
  `register` on start with the key raj handed it.
- Budgets and a kill switch: at most N agents, a per-task time and token budget
  when the harness reports it, and one switch that stops every agent raj
  started.

## 4. Open questions

- Is a task a new store table, or a projection over the journal's task field
  plus a small metadata record?
- Do roles (reviewer, implementer) belong in raj, or only in the user's hooks?
- How much of the chat pane survives on the phone profile?
- Does supervision own restart-on-crash, or leave it to compose/Kubernetes?


## Direction: chat as a pane, and booting the harness from raj (2026-09-27)

The user's ask: the review loop should live *in the editor* — this spec's chat
pane, plus the ability to boot/attach the docker harness from within raj, so a
person reviews proposals and talks to the agents in one place instead of juggling
terminals. Draws on the same surfaces as the harness adapters
(`docs/dev/HARNESS-ADAPTERS.md`), the waker (`docs/dev/RAJ-DEV-NOTES.md`, T1:
`ocw`/`cc`/`ccw`), and the lifecycle containers (WAVE-PLAN Track L: L4 detached,
named containers).

Shape to settle before it is scheduled:
- the pane is a view over the existing mailboxes, not a second transport;
- a boot button/verb starts the harness container and binds its key (one
  consumer per key, the `ccw` rule), and the pane shows its `who` state;
- the review flow stays the editor's: chat coordinates, `accept`/`save` land.
Open: who supervises a booted harness (restart, key re-bind), and whether a
pane starts the waker or attaches to a running one.

Backends, in order of arrival (user, 2026-09-27): plain Docker first, then Compose,
then Swarm/Kubernetes/Rancher only if the swarm actually scales — the same
sequencing `docs/dev/HARNESS-ADAPTERS.md` §5 already states ("Compose today; k8s
if the swarm scales"). Keep raj backend-agnostic: one harness-lifecycle seam
(start, stop, status, bind-key) that a Compose or a k8s backend implements, so the
editor's boot action names an intention, not an orchestrator.
