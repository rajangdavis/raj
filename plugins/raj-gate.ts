import type { Plugin } from "@opencode-ai/plugin"
import { appendFileSync } from "node:fs"
import { homedir } from "node:os"
import { join } from "node:path"

const ledgerPath = process.env.RAJ_TOOL_LEDGER ?? join(process.env.XDG_DATA_HOME ?? join(homedir(), ".local", "share"), "opencode", "raj-tool-ledger.jsonl")
function ledger(entry: Record<string, unknown>) {
  try {
    appendFileSync(ledgerPath, JSON.stringify(entry) + String.fromCharCode(10))
  } catch {}
}

// Identity is explicit in raj now: an agent runs `raj ctl register` to mint a
// short key, then passes `-as <key>` on every later call. This plugin no longer
// captures, injects or scrubs RAJ_IDENTITY — the per-session token map, the
// adopt-line regexes and the leak guard are all gone. What remains is (a) the
// one thing only the plugin can enforce: a swarmer may only spawn raj/review
// agents, and (b) the task a session is working under, so a review can be scoped
// without grepping command text. The prompt itself is deliberately not recorded:
// the session's first user message id is the task key, and `scripts/call-runs.mjs`
// attributes every change to it straight from opencode.db.
const SWARMERS = new Set(["raj", "review", "worker", "orchestrated"])
const SPAWNABLE = new Set(["raj", "review"])

// A raj key in `raj ctl ... -as raj-<hex>`, `... --as raj-<hex>`, or a shell
// assignment such as `KEY=raj-<hex>` that a later `--as "$KEY"` uses. A bare
// raj-<hex> elsewhere is not the caller's key: agents search for each other's.
const RAJ_KEY = /(?:(?:^|\s)-{1,2}as[\s=]+["']?|\b[A-Z_]*KEY=["']?)(raj-[0-9a-f]{6,})/

// A `raj ctl save` invocation — the one call an agent may never run. Saving is
// the user's gesture; this is the early, clear half of the law, and raj itself
// refuses a save from anything but the human's own connection, so a miss here
// is not a hole. The matcher anchors on a command *position*: start of the
// string or just after a shell separator (`;`, `&&`, `||`, `|`, newline), so a
// command that merely *mentions* the string — `echo "raj ctl save"`, a heredoc,
// a comment — is not blocked. The over-broad first cut matched any mention and
// blocked the agent's own diagnostic commands, hiding their output; that is
// worse than the small risk it covered, and the server is the authority anyway.
const RAJ_SAVE = /(?:^|[;&|\n]\s*)(?:[^\s;&|]*\/)?raj\s+ctl\s+save(?:\s|$)/

// A session's task is its first user message id, fetched once per session and
// cached. `--continue` reuses sessions, so a later refinement may track the
// latest user message instead; `scripts/call-runs.mjs` joins on the task from
// opencode.db and is exact either way.
const taskCache = new Map<string, string>()

export default (async ({ client }) => {
  // The SDK takes the session id under `path` and answers with `{ info, parts }`
  // rows; callers want the message info (role, agent, id). The old flat
  // `{ id }` call was rejected, and the catch below turned that into an empty
  // list, so every task was null and the swarmer spawn rule never fired.
  async function messagesOf(sessionID: string): Promise<unknown[]> {
    try {
      const res = await client.session.messages({ path: { id: sessionID } })
      const rows = (res.data ?? []) as { info?: unknown }[]
      return rows.map((r) => r.info ?? r)
    } catch {}
    return []
  }

  async function callerAgent(sessionID: string): Promise<string | undefined> {
    const messages = await messagesOf(sessionID)
    for (let i = messages.length - 1; i >= 0; i--) {
      const m = messages[i] as { role?: string; agent?: unknown }
      if (m.role === "user" && m.agent) {
        return String(m.agent)
      }
    }
    return undefined
  }

  async function taskOf(sessionID: string): Promise<string | undefined> {
    const cached = taskCache.get(sessionID)
    if (cached) return cached
    const messages = await messagesOf(sessionID)
    const first = messages.find((m) => (m as { role?: string }).role === "user")
    if (!first) return undefined
    const id = (first as { id?: unknown }).id
    const task = typeof id === "string" ? id : undefined
    if (task) taskCache.set(sessionID, task)
    return task
  }

  return {
    "tool.execute.before": async (input, output) => {
      // Saving is the user's own gesture, never the agent's: `raj ctl save`
      // writes the user's buffers to disk, so an agent that runs it turns the
      // user's review into a formality and can bypass the gate. This is a hard
      // law, enforced here rather than left to discipline. The agent asks the
      // user and lets them save.
      const saveArgs = (output.args ?? {}) as Record<string, unknown>
      const saveCmd = typeof saveArgs.command === "string" ? saveArgs.command : undefined
      if (input.tool === "bash" && saveCmd && RAJ_SAVE.test(saveCmd)) {
        throw new Error(
          "blocked: `raj ctl save` is the user's own action, and an agent must never save for them. " +
            "Ask the user for explicit permission and let them save; do not retry this call.",
        )
      }
      try {
        const args = (output.args ?? {}) as Record<string, unknown>
        const cmd = typeof args.command === "string" ? args.command : undefined
        const task = await taskOf(input.sessionID)
        const rajKey = cmd?.match(RAJ_KEY)?.[1]
        ledger({
          ts: Date.now(),
          session: input.sessionID,
          tool: input.tool,
          cmd: cmd ? cmd.slice(0, 400) : null,
          task: task ?? null,
          rajKey: rajKey ?? null,
        })
      } catch {}
      if (input.tool !== "task") return
      const caller = await callerAgent(input.sessionID)
      if (caller === undefined || !SWARMERS.has(caller)) return
      const type = (output.args as { subagent_type?: string })?.subagent_type
      if (type === undefined || !SPAWNABLE.has(type)) {
        throw new Error(`the ${caller} agent may only spawn ${[...SPAWNABLE].join(" or ")} subagents (got: ${type ?? "unspecified"})`)
      }
    },
    "tool.execute.after": async (input, output) => {
      try {
        const meta = (input ?? {}) as Record<string, unknown>
        const out = (output ?? {}) as Record<string, unknown>
        const session = typeof meta.sessionID === "string" ? meta.sessionID : ""
        const task = await taskOf(session)
        ledger({
          ts: Date.now(),
          session: meta.sessionID ?? null,
          tool: meta.tool ?? null,
          callID: meta.callID ?? null,
          phase: "after",
          task: task ?? null,
          exit: typeof out.exit === "number" ? out.exit : null,
          durationMs: typeof out.duration === "number" ? out.duration : null,
          bytes: typeof out.output === "string" ? out.output.length : null,
        })
      } catch {}
    },
  }
}) satisfies Plugin
