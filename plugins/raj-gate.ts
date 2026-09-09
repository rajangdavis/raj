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

  return {
    "tool.execute.before": async (input, output) => {
      if (input.tool !== "task") return
      if (!(await callerIsRaj(input.sessionID))) return
      const type = (output.args as { subagent_type?: string })?.subagent_type
      if (type !== "raj") {
        throw new Error(`the raj agent may only spawn raj subagents (got: ${type ?? "unspecified"})`)
      }
    },
  }
}) satisfies Plugin