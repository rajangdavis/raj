import type { Plugin } from "@opencode-ai/plugin"

// Identity is explicit in raj now: an agent runs `raj ctl register` to mint a
// short key, then passes `-as <key>` on every later call. This plugin no longer
// captures, injects or scrubs RAJ_IDENTITY — the per-session token map, the
// adopt-line regexes and the leak guard are all gone. What remains is the one
// thing only the plugin can enforce: a swarmer may only spawn raj/review agents.
// The agents that may spawn subagents, and the types they may spawn. The review
// agent runs between waves (docs/REVIEW-AGENT.md) and delegates fixes to raj
// subagents, so both may spawn both.
const SWARMERS = new Set(["raj", "review"])
const SPAWNABLE = new Set(["raj", "review"])

export default (async ({ client }) => {
  async function callerAgent(sessionID: string): Promise<string | undefined> {
    try {
      const res = await client.session.messages({ id: sessionID })
      const messages = res.data ?? []
      for (let i = messages.length - 1; i >= 0; i--) {
        const m = messages[i]
        if (m.role === "user" && "agent" in m && m.agent) {
          return m.agent
        }
      }
    } catch {}
    return undefined
  }

  return {
    "tool.execute.before": async (input, output) => {
      if (input.tool !== "task") return
      const caller = await callerAgent(input.sessionID)
      if (caller === undefined || !SWARMERS.has(caller)) return
      const type = (output.args as { subagent_type?: string })?.subagent_type
      if (type === undefined || !SPAWNABLE.has(type)) {
        throw new Error(`the ${caller} agent may only spawn ${[...SPAWNABLE].join(" or ")} subagents (got: ${type ?? "unspecified"})`)
      }
    },
  }
}) satisfies Plugin
