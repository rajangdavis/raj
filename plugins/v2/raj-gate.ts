import { appendFileSync } from "node:fs"
import { homedir } from "node:os"
import { join } from "node:path"

// The opencode V2 raj gate. Kept in plugins/v2/ and out of plugins/ so the
// verified v1 plugin (plugins/raj-gate.ts) keeps its exact shape until v2 is
// proven on a host. The pure rules are duplicated from the v1 file on purpose:
// the v1 image copies only plugins/*.ts, so a shared plugins/lib/*.ts could not
// be resolved where the v1 plugin loads.
const ledgerPath =
  process.env.RAJ_TOOL_LEDGER ?? join(process.env.XDG_DATA_HOME ?? join(homedir(), ".local", "share"), "opencode", "raj-tool-ledger.jsonl")
function ledger(entry: Record<string, unknown>) {
  try {
    appendFileSync(ledgerPath, JSON.stringify(entry) + String.fromCharCode(10))
  } catch {}
}

const SPAWNABLE = new Set(["raj", "review"])
const RAJ_KEY = /(?:(?:^|\s)-{1,2}as[\s=]+["']?|\b[A-Z_]*KEY=["']?)(raj-[0-9a-f]{6,})/
const RAJ_SAVE = /(?:^|[;&|\n]\s*)(?:[^\s;&|]*\/)?raj\s+ctl\s+save(?:\s|$)/

export default {
  id: "raj-gate",
  async setup(ctx: any) {
    await ctx.tool.hook("execute.before", (event: any) => {
      const tool = String(event?.tool ?? "")
      const input = (event?.input ?? {}) as Record<string, unknown>
      const cmd = typeof input.command === "string" ? input.command : undefined
      if ((tool === "bash" || tool === "shell") && cmd && RAJ_SAVE.test(cmd)) {
        throw new Error(
          "blocked: `raj ctl save` is the user's own action, and an agent must never save for them. " +
            "Ask the user for explicit permission and let them save; do not retry this call.",
        )
      }
      try {
        ledger({
          ts: Date.now(),
          session: event?.sessionID ?? null,
          tool,
          cmd: cmd ? cmd.slice(0, 400) : null,
          task: null,
          rajKey: cmd?.match(RAJ_KEY)?.[1] ?? null,
        })
      } catch {}
      // Fail closed. A spawn carries an agent id (v1 `subagent_type`, or v2's
      // `agent` field); if the tool is a spawn tool OR the input carries such a
      // field, it must name raj or review. An unknown tool name with an
      // agent-ish input is still refused rather than silently allowed.
      const type = input.subagent_type ?? input.agent
      if (tool === "task" || tool === "subagent" || type !== undefined) {
        const id = String(type ?? "")
        if (!SPAWNABLE.has(id)) {
          throw new Error(`the agent may only spawn ${[...SPAWNABLE].join(" or ")} subagents (got: ${id || "unspecified"})`)
        }
      }
    })
    await ctx.tool.hook("execute.after", (event: any) => {
      try {
        const meta = (event ?? {}) as Record<string, unknown>
        ledger({
          ts: Date.now(),
          session: meta.sessionID ?? null,
          tool: meta.tool ?? null,
          phase: "after",
          task: null,
          exit: meta.status === "error" ? 1 : 0,
          bytes: typeof meta.result === "string" ? meta.result.length : null,
        })
      } catch {}
    })
  },
}
