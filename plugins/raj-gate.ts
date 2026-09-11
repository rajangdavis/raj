import type { Plugin } from "@opencode-ai/plugin"

export default (async ({ client }) => {
  async function callerIsRaj(sessionID: string): Promise<boolean> {
    try {
      const res = await client.session.messages({ id: sessionID })
      const messages = res.data ?? []
      for (let i = messages.length - 1; i >= 0; i--) {
        const m = messages[i]
        if (m.role === "user" && "agent" in m && m.agent) {
          return m.agent === "raj"
        }
      }
    } catch {}
    return false
  }

  // --- RAJ_IDENTITY absorption: distinct-per-session ----------------------
  //
  // DESIGN (swarm rework). The server mints a DURABLE tok_… for every
  // anonymous hello (internal/control: Registry.Join maps it to one stable
  // author id for the process lifetime) and the CLI binds with
  // firstOf(-as, $RAJ_IDENTITY, adopt-from-server). So per-session
  // capture+reinject is sufficient on its own, and the old process-wide
  // fallback (sharedToken + process.env.RAJ_IDENTITY) is REMOVED. Three
  // parts:
  //
  //   capture:  "tool.execute.after" sees the adopt line in bash output,
  //             stores the token under THIS session, and scrubs the line
  //             so it never reaches the model's context.
  //   inject:   "shell.env" reinjects the session's OWN captured token
  //             into its later shells. A session with no captured token
  //             gets NOTHING: its first `raj ctl` runs unpinned, the
  //             server mints a fresh tok_…, the CLI prints the adopt line,
  //             and capture stores it. Distinct-per-session falls out of
  //             the adopt path for free — a first-call inject could only
  //             ever inject SOMEONE ELSE'S token. Weighed "inject shared
  //             on first call" vs "let the first call adopt fresh, then
  //             capture": the latter, and it is the actual swarm fix.
  //   persist:  the per-session Map, and nothing else. The plugin never
  //             sets process.env.RAJ_IDENTITY: a process-wide pin binds
  //             every later session's shells via the CLI's env fallback
  //             BEFORE adoption can happen — that was the swarm bug (the
  //             parent captures, every spawned child silently inherits
  //             the parent's author id, one tint for the whole swarm).
  //
  // TENSION RESOLVED: sharedToken existed so two SEQUENTIAL sessions of
  // one agent share an author id (the TODO success case). A spawned
  // sibling subagent is a DIFFERENT session and must be distinct, and at
  // this API surface the plugin cannot tell "same agent resumed" from
  // "sibling spawned" — both are just a new sessionID. Distinct-per-
  // session wins (a swarm needs distinctness); to resume an id, re-pin
  // explicitly ($RAJ_IDENTITY export or -as).
  //
  // KNOWN-RAJ GATING: the old rajSessions runtime check existed solely to
  // gate the shared fallback, so another agent running `raj ctl` would
  // not inherit the raj author id. With the fallback gone the property is
  // structural — the only value ever injected is a token the SAME
  // session minted, so no session can receive a foreign id — and the
  // check would be dead code. Capture+scrub stay UNGATED on purpose: if
  // a subagent session's chat.message is not observed before its first
  // tool call, gating capture on known-raj would leak the adopt line
  // (and the token) to the model — the one failure this plugin exists
  // to prevent. The live runtime gate that remains is callerIsRaj on the
  // task tool (raj may only spawn raj), below, unchanged.
  //
  // EXPLICIT PIN STILL WINS: an already-set RAJ_IDENTITY (box env, user
  // export, earlier inject) is left untouched, and the CLI's firstOf
  // order keeps -as ahead of everything. Starting the box with
  // RAJ_IDENTITY exported pins every session to it — the user opting out
  // of per-session distinctness, deliberately.
  //
  // HOST VERIFICATION (proposed from inside the container; not run here —
  // plugins load at opencode start, so this needs a restart on the host):
  //   1. Accept + save this file, restart opencode with RAJ_IDENTITY unset
  //      and no -as anywhere.
  //   2. As the raj agent, run `raj ctl whoami` twice (separate shells),
  //      then `raj ctl who`. Expect: no `set RAJ_IDENTITY=tok_…` line in
  //      any tool result; both whoami calls print the SAME author id.
  //   3. Spawn TWO raj subagents (task tool); have each run
  //      `raj ctl whoami` twice. Expect THREE DIFFERENT author ids across
  //      parent + children; each child's second call repeats its own
  //      first id (per-session continuity); `who` shows three live
  //      distinct entries. That is the swarm fix.
  //   4. Regression, single agent: in a fresh raj session run
  //      `raj ctl whoami` — expect a NEW author id. The old two-
  //      sessions-share-one-id behavior is deliberately retired; re-pin
  //      explicitly to resume an id.
  //   5. Explicit-pin check: restart with RAJ_IDENTITY=tok_test exported.
  //      Every session's whoami prints tok_test's author id and no adopt
  //      line is ever printed or captured.
  //   6. If step 2 still shows the adopt line, the bash tool does not
  //      route result text through "tool.execute.after" as assumed —
  //      report it; capture would have to move to wrapping the command
  //      in "tool.execute.before" instead.
  //
  // API surface relied on (Hooks in @opencode-ai/plugin dist/index.d.ts,
  // as installed under ~/.config/opencode/node_modules — NOT vendored in
  // this repo, so these are verified-against-the-installed-package
  // assumptions):
  //   "shell.env":          (input: { cwd: string; sessionID?: string;
  //                          callID?: string },
  //                          output: { env: Record<string, string> })
  //   "tool.execute.after": (input: { tool: string; sessionID: string;
  //                          callID: string; args: any },
  //                          output: { title: string; output: string;
  //                          metadata: any })
  //   "tool.execute.before" is also used (the task spawn-gate below;
  //   output.args is inspected for subagent_type). "chat.message" is no
  //   longer used: known-raj tracking only fed the removed fallback.
  //
  // The CLI side is already durable: `raj ctl` binds with
  // firstOf(-as, $RAJ_IDENTITY) and, when both are empty, adopts the token
  // the server mints and prints `set RAJ_IDENTITY=tok_…` once on stderr
  // (internal/control/cli.go, hello before the verb switch). What the
  // agent used to do with that line — export it, pass -as forever — is
  // the chore this plugin absorbs. Durable-across-restart storage of
  // per-session tokens (project config) remains a deliberate follow-up;
  // without it a server restart simply starts every session on a fresh
  // id. The dead anon-N rows in `who` are editor-side (the Registry
  // never recycles provisional ids) and out of scope here.

  // sessionID → token that session itself adopted. Precise attribution:
  // reinjecting a session's own token is never wrong, whoever the session
  // belongs to — it is the only value this plugin ever injects.
  const tokenBySession = new Map<string, string>()

  // The adopt line, exactly as cli.go prints it (stderr, once per adopting
  // process). \S+ tolerates a token-format change; the line prefix is the
  // specific part. Two regexes, not one reused: a global one for the scrub
  // (a `;`-chained command can adopt on several invocations) and a plain
  // one for the capture, so no lastIndex state leaks between them.
  const adoptLine = /^set RAJ_IDENTITY=(\S+)/m
  const adoptLineAll = /^set RAJ_IDENTITY=\S+[ \t]*(?:\r?\n|$)/gm

  return {
    "tool.execute.before": async (input, output) => {
      if (input.tool !== "task") return
      if (!(await callerIsRaj(input.sessionID))) return
      const type = (output.args as { subagent_type?: string })?.subagent_type
      if (type !== "raj") {
        throw new Error(`the raj agent may only spawn raj subagents (got: ${type ?? "unspecified"})`)
      }
    },

    "shell.env": async (input, output) => {
      // Already pinned (by raj box, the user, or an earlier inject): leave it.
      if (output.env.RAJ_IDENTITY) return
      // Reinject this session's own adopted token (continuity across its
      // shells). No token yet: inject NOTHING — the first call adopts a
      // fresh server-minted token, captured below. Never inject another
      // session's token: that was the sharedToken fallback, removed because
      // it collapsed a whole swarm onto the parent's author id.
      const own = input.sessionID ? tokenBySession.get(input.sessionID) : undefined
      if (own) output.env.RAJ_IDENTITY = own
    },

    "tool.execute.after": async (input, output) => {
      if (input.tool !== "bash") return
      const m = adoptLine.exec(output.output)
      if (!m) return
      const token = m[1]
      // Per-session persistence only. Deliberately NO process.env write: a
      // process-wide pin binds every future session's shells before they
      // can adopt their own token (the swarm bug). A shell path that skips
      // shell.env would adopt fresh per call; none is known in current
      // opencode — if one appears, route it through the hook rather than
      // re-adding a process pin.
      if (input.sessionID) tokenBySession.set(input.sessionID, token)
      // Scrub: the token never enters the model's context.
      output.output = output.output.replace(adoptLineAll, "")
    },
  }
}) satisfies Plugin