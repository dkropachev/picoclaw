import type { AgentActivityEvent } from "@/api/agents"

export function mergeActivityEvents(
  current: AgentActivityEvent[],
  incoming: AgentActivityEvent[],
  limit: number,
) {
  const bySequence = new Map(
    current.map((event) => [`${event.agent_id}:${event.sequence}`, event]),
  )
  for (const event of incoming) {
    bySequence.set(`${event.agent_id}:${event.sequence}`, event)
  }
  return [...bySequence.values()]
    .sort((left, right) => compareDecimal(left.sequence, right.sequence))
    .slice(-limit)
}

function compareDecimal(left: string, right: string) {
  if (left.length !== right.length) return left.length - right.length
  return left.localeCompare(right)
}
