# Workspace host and attach — design note

Status: draft, 2026-09-18; stage 2 landed 2026-09-18. `raj --attach` is now a **local-render client** against a daemon-owned document (a subset of option B below, not the thin screen-diff client of option A); detach/reattach and concurrent views remain unscheduled.

## Goal

A workspace-scoped **host**: the first `raj` started in a directory owns the
model, and later `raj` instances in the same directory attach to it instead of
starting a second editor. Detach/reattach is the mobile story (an SSH drop or a
screen lock becomes a detach, not a lost session), and concurrent clients are
the phone-and-laptop story.

## What exists

- One process owns the model and the terminal. The control socket carries a
  **document** protocol (`read`, `apply`, `search`, `run`, …) — verbs and bytes,
  no keystrokes and no frames. Agents already attach through it.
- A **local-render client** already exists: `raj --attach` (with `--phone`
  implying it) loads the daemon's tabs from the new `snapshot` op, follows the
  `watch` push, and owns its cursor and viewport but no model
  (`internal/app/client.go`); decisions proxy back to the daemon. It mirrors the
  daemon real tabs continuously — a tab opened after the attach appears, one
  closed disappears — adopts a buffer with a pending change set even when the
  client never opened it, and treats a local close as a snooze until the daemon
  buffer changes.
- `session.StateDir(root)` is a stable per-workspace key, a natural home for a
  stable socket path.
- Proposals and leases already tolerate multiple writers, so a second *writer*
  is not the hard part; a second *viewer* is.
- KKP and chords are per-terminal, and raj under tmux loses them (README). That
  is an argument for raj owning the lifetime rather than delegating to tmux.
- The heartbeat/byte-budget work on the control connection is a prerequisite for
  a long-lived host.

## Requirements

- **Discovery.** A second `raj` in the same workspace finds the host, and a
  stale socket is told apart from a live one by a handshake, not by mtime.
- **Detach/reattach** with the model untouched: nothing about the session lives
  in the client.
- **Input and render** channels: keys/mouse/motions in, frames out.
- **Per-client size.** A phone and a laptop are different terminals; the host
  must render at the attached client's size.
- **Agents coexist.** The control socket keeps serving while a client is
  attached, and a host with no client still runs so agents continue.
- **Trust boundary** unchanged: the local Unix socket is the anchor; a TCP
  attach is a different decision.

## Options

**A. tmux-like I/O multiplexing.** The host owns the model and the render; the
attached client's terminal receives frames and its input is forwarded. Reuses
`ui.Screen`/`Diff`; one attached client mirrors the host's viewport at the
client's size. Smallest path to detach/reattach, and it needs no per-client
view state. **Superseded** for the landed client: `--attach` renders locally
from `snapshot`/`watch` (option B) rather than mirroring frames.

**B. Thin clients that render locally.** The host owns documents and proposals;
each client owns its cursor, viewport and projection, and syncs through ops.
Most power (independent cursors, no frame streaming) but the largest surface:
every view feature must exist and stay consistent on each client, and
coordination for decorations is new work.

**C. One host, many sized views.** A extended so several clients watch at once
at different sizes. Powerful and genuinely hard — per-client viewports and
projections are a renderer project, not plumbing.

## Recommendation and staging

1. **Discovery, no attach.** A workspace-scoped socket keyed by
   `session.StateDir`. A second `raj` in the same workspace refuses to start a
   second editor and says how to attach. This alone prevents the split-brain of
   two processes editing one workspace with no shared state, and it is cheap.
2. **Single-client attach/detach.** Landed in part (2026-09-18) as a
   local-render client rather than option A's screen mirror: the host owns the
   model, `raj --attach` loads its documents from `snapshot` and follows
   `watch`, and decisions proxy back. Detach/reattach and resume remain.
3. **Concurrent views (option B or C)** only after the input/render protocol
   exists and has been lived with.

## Protocol sketch (stage 2)

- A second connection type on the same socket (or a sibling socket): `attach`
  (token, cols, rows, maybe terminal capabilities), `input` (decoded events),
  `resize`, `detach`; host to client: framed screen diffs.
- Output frames go through the same bounded-queue discipline as control: a slow
  client is dropped rather than blocking the event loop.
- Input reuses `keys.Parse`; output reuses `ui.Screen.Diff`.
- KKP is pushed on the client's tty by the attach handshake, so focus/suspend
  map onto attach/detach rather than to process signals.

## Open questions

- **Socket home.** `XDG_RUNTIME_DIR` (per-login, cleared on logout) versus the
  XDG state dir (survives). A stale socket must fail loudly, not hang.
- **Host with no client.** Keep owning the model for agents, or exit? The
  headless-read path already points at "a host need not have a terminal".
- **One client or many** in v1. One with a clear refusal is the safe default;
  a read-only mirror is the cheap second step.
- **Detach gesture.** A chord on the laptop, a motion on the phone — the
  motions probe is what tells us which gesture is available.
- **TCP attach.** A remote console is a different security posture; do not
  assume it.
- **Startup.** `raj` becomes the host when none exists; `raj --attach` fails
  when none does. Whether the host is the process that owns a client terminal
  or a separate daemon is the first thing to decide.

## Not in scope

tmux parity (windows, panes), remote multi-host, collaborative cursors, and
session branching/forking.

## Phone input and the review console

What a phone terminal actually delivers decides the interaction model. On
Termius the probe found that chords are unreliable (some swallowed, some
delivered as unrelated sequences) and most gestures are either not delivered
or arrive as something else; the ones that do arrive come through the mouse
report — a two-finger swipe as wheel notches, or bare motion. No Charm library
recognizes gestures: Bubble Tea offers high-fidelity keyboard and mouse
handling (`MouseMsg`, `MouseEvent.IsWheel`, `EnableMouseAllMotion`,
`EnableMouseCellMotion`) and BubbleZone does hit testing, but a swipe is only
ever a pattern over the events the terminal sends, so the classification is
ours — the probe's `matchSwipe`/`countWheel` already are that classifier.

The reliable phone input is therefore **printable text**, and the review
surface should be designed around it:

- The review surface is the attached **client's own Review mode**, not a
  separate console program: the client proxies `accept`/`reject`/`clear` to the
  daemon and refetches, so the phone reads the workspace the client already
  renders. A standalone typed console remains an option for a terminal that
  cannot run the full client, but it is not the direction the attach client
  took.
- Typed-first: single keys or a one-line `review>` prompt (`a`/`r`/`c`, `n`/`p`)
  works on any terminal, with no chords and no gestures. Large tap targets can
  be added only if taps arrive as clicks.
- Motion is an optional layer on top, and only for events the probe proves
  arrive: a wheel burst or a motion run classified with the probe's own
  thresholds. It must degrade to the typed keys.
- Two flavors: reuse `internal/term`/`internal/ui` for a dependency-light
  console in the repo's idiom, or build on Bubble Tea/Bubbles for faster UI
  iteration and free mouse/resize plumbing at the cost of a second renderer and
  a dependency. The host/attach model is orthogonal: attach shares the full
  editor; the console only needs the control socket.

## Decided direction (2026-09-18)

The host is an **explicit daemon**, not the first interactive raj:

- `raj --daemon` runs a workspace headless and owns the model, session and
  control socket, with no terminal, so agents can drive it and clients can
  attach. `--visible DIR` (repeatable, or `--workspace DIR`) scopes what the
  workspace exposes to agents and clients.
- `raj --attach [addr]` is a client that **renders locally against a
  daemon-owned document**: it loads each daemon buffer from the `snapshot` op
  and follows the `watch` push, owning its cursor and viewport but not the
  model; decisions proxy back. `--phone` implies `--attach` and selects the
  phone profile.
- The **phone profile** puts review first — the queue of pending change sets, a
  compact diff and a decision row — with printable-key commands and large
  targets, never a chord or a swipe.
- One attached client to start; whether a second takes over or attaches
  read-only is open.
- The socket lives under `$XDG_RUNTIME_DIR/raj/`, keyed by the workspace state
  key; a stale socket is told apart from a live one by handshake, not mtime.

Staging:

1. **headless daemon** plus `--visible`/`--workspace`;
2. **attach protocol and client** (one client) — landed 2026-09-18 as the
   local-render client (`snapshot` + `watch`);
3. **phone profile and review surface** — the profile and the client-side
   review proxy landed with it; touch tabs and the review bar remain.
