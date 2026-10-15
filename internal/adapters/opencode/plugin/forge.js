// forge-managed: installed by `forge setup`, removed by `forge setup --remove`.
// Sends usage metadata of completed assistant messages (ids, model, tokens,
// cost, working directory) to the local forge daemon. Never prompts or code.
const FORGE_URL = "__FORGE_INGEST_URL__"
const sent = new Set()

async function post(body) {
  try {
    await fetch(FORGE_URL + "/ingest/opencode", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
      signal: AbortSignal.timeout(1500),
    })
  } catch {
    // The daemon may be down; forge must never disturb the session.
  }
}

export const ForgePlugin = async ({ directory, worktree }) => {
  const fallbackCwd = directory || worktree || ""
  return {
    event: async ({ event }) => {
      const props = event?.properties ?? {}
      if (event?.type === "session.created" && props.info?.id) {
        await post({
          kind: "session",
          sessionID: props.info.id,
          cwd: props.info.directory || fallbackCwd,
        })
        return
      }
      if (event?.type !== "message.updated") return
      const m = props.info
      if (!m || m.role !== "assistant" || !m.time?.completed || sent.has(m.id)) return
      sent.add(m.id)
      if (sent.size > 5000) sent.clear()
      const t = m.tokens ?? {}
      await post({
        kind: "message",
        id: m.id,
        sessionID: m.sessionID,
        providerID: m.providerID ?? m.model?.providerID ?? "",
        modelID: m.modelID ?? m.model?.id ?? "",
        cost: m.cost ?? 0,
        tokens: {
          input: t.input ?? 0,
          output: t.output ?? 0,
          reasoning: t.reasoning ?? 0,
          cache: { read: t.cache?.read ?? 0, write: t.cache?.write ?? 0 },
        },
        time: { created: m.time.created, completed: m.time.completed },
        cwd: m.path?.cwd || fallbackCwd,
      })
    },
  }
}
