import { canonicalAgentIDPattern } from "./agent-form"

export const agentDetailTabs = ["overview", "capabilities", "activity"] as const

export type AgentDetailTab = (typeof agentDetailTabs)[number]

export interface AgentsRouteSearch {
  agent?: string
  tab?: AgentDetailTab
}

export function normalizeAgentsSearch(
  input: Record<string, unknown>,
): AgentsRouteSearch {
  const agent = typeof input.agent === "string" ? input.agent : ""
  if (!canonicalAgentIDPattern.test(agent)) return {}

  const tab = agentDetailTabs.includes(input.tab as AgentDetailTab)
    ? (input.tab as AgentDetailTab)
    : "overview"

  return { agent, tab }
}

export function agentsSearchIsCanonical(
  input: Record<string, unknown>,
  normalized: AgentsRouteSearch,
): boolean {
  const keys = Object.keys(input).filter((key) => input[key] !== undefined)
  const normalizedKeys = Object.keys(normalized)
  if (
    keys.length !== normalizedKeys.length ||
    keys.some((key) => !normalizedKeys.includes(key))
  ) {
    return false
  }
  return input.agent === normalized.agent && input.tab === normalized.tab
}
