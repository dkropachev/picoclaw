import { launcherFetch } from "@/api/http"

export interface AgentModelPolicy {
  primary: string
  fallbacks: string[] | null
}

export interface AgentDelegationPolicy {
  allow_agents: string[]
}

export interface AgentInfo {
  id: string
  name: string
  workspace: string
  model: AgentModelPolicy | null
  skills: string[] | null
  subagents: AgentDelegationPolicy | null
  is_default: boolean
  default_configured: boolean
  implicit: boolean
}

export interface AgentMutationInput {
  id: string
  name: string
  workspace: string
  model: AgentModelPolicy | null
  skills: string[] | null
  subagents: AgentDelegationPolicy | null
}

export interface AgentMutationEffects {
  launcher_effect: "applied"
  catalog_effect: "applied"
  gateway_effect: "applied" | "restart_required"
}

export interface AgentsResponse {
  agents: AgentInfo[]
  default_agent_id: string
  config_revision: string
  effects: AgentMutationEffects
}

export interface AgentResponse {
  agent: AgentInfo
  default_agent_id: string
  config_revision: string
  effects: AgentMutationEffects
}

export interface AgentMutationRequest {
  expected_config_revision: string
  agent: AgentMutationInput
}

export interface AgentDeleteBlocker {
  kind: string
  name?: string
  agent_id?: string
}

export class AgentsAPIError extends Error {
  readonly status: number
  readonly code?: string
  readonly blockers?: AgentDeleteBlocker[]

  constructor(
    message: string,
    status: number,
    options: { code?: string; blockers?: AgentDeleteBlocker[] } = {},
  ) {
    super(message)
    this.name = "AgentsAPIError"
    this.status = status
    this.code = options.code
    this.blockers = options.blockers
  }
}

async function request<T>(path: string, options?: RequestInit): Promise<T> {
  const response = await launcherFetch(path, options)
  if (!response.ok) {
    const text = await response.text()
    let message = text.trim()
    let code: string | undefined
    let blockers: AgentDeleteBlocker[] | undefined

    try {
      const body = JSON.parse(text) as {
        error?: unknown
        message?: unknown
        code?: unknown
        blockers?: unknown
      }
      if (typeof body.message === "string" && body.message.trim() !== "") {
        message = body.message
      } else if (typeof body.error === "string" && body.error.trim() !== "") {
        message = body.error
      }
      if (typeof body.code === "string" && body.code.trim() !== "") {
        code = body.code
      } else if (
        typeof body.error === "string" &&
        /^[a-z0-9_]+$/.test(body.error)
      ) {
        code = body.error
      }
      if (Array.isArray(body.blockers)) {
        const projected = body.blockers
          .map(projectBlocker)
          .filter((item): item is AgentDeleteBlocker => item != null)
        if (projected.length === body.blockers.length) {
          blockers = projected
        }
      }
    } catch {
      // Preserve a plain-text API error.
    }

    throw new AgentsAPIError(
      message || `API error: ${response.status} ${response.statusText}`,
      response.status,
      { code, blockers },
    )
  }

  return response.json() as Promise<T>
}

const jsonHeaders = { "Content-Type": "application/json" }

export async function getAgents(): Promise<AgentsResponse> {
  return request<AgentsResponse>("/api/agents")
}

export async function getAgent(id: string): Promise<AgentResponse> {
  return request<AgentResponse>(`/api/agents/${encodeURIComponent(id)}`)
}

function projectBlocker(value: unknown): AgentDeleteBlocker | null {
  if (value == null || typeof value !== "object" || Array.isArray(value)) {
    return null
  }
  const blocker = value as Record<string, unknown>
  if (typeof blocker.kind !== "string" || blocker.kind.trim() === "") {
    return null
  }
  if (blocker.name != null && typeof blocker.name !== "string") return null
  if (blocker.agent_id != null && typeof blocker.agent_id !== "string") {
    return null
  }
  return {
    kind: blocker.kind,
    ...(typeof blocker.name === "string" ? { name: blocker.name } : {}),
    ...(typeof blocker.agent_id === "string"
      ? { agent_id: blocker.agent_id }
      : {}),
  }
}

export async function createAgent(
  expectedConfigRevision: string,
  agent: AgentMutationInput,
): Promise<AgentsResponse> {
  return request<AgentsResponse>("/api/agents", {
    method: "POST",
    headers: jsonHeaders,
    body: JSON.stringify({
      expected_config_revision: expectedConfigRevision,
      agent,
    } satisfies AgentMutationRequest),
  })
}

export async function updateAgent(
  id: string,
  expectedConfigRevision: string,
  agent: AgentMutationInput,
): Promise<AgentsResponse> {
  return request<AgentsResponse>(`/api/agents/${encodeURIComponent(id)}`, {
    method: "PUT",
    headers: jsonHeaders,
    body: JSON.stringify({
      expected_config_revision: expectedConfigRevision,
      agent,
    } satisfies AgentMutationRequest),
  })
}

export async function deleteAgent(
  id: string,
  expectedConfigRevision: string,
): Promise<AgentsResponse> {
  return request<AgentsResponse>(`/api/agents/${encodeURIComponent(id)}`, {
    method: "DELETE",
    headers: jsonHeaders,
    body: JSON.stringify({
      expected_config_revision: expectedConfigRevision,
    }),
  })
}

export async function setDefaultAgent(
  id: string,
  expectedConfigRevision: string,
): Promise<AgentsResponse> {
  return request<AgentsResponse>(
    `/api/agents/${encodeURIComponent(id)}/default`,
    {
      method: "POST",
      headers: jsonHeaders,
      body: JSON.stringify({
        expected_config_revision: expectedConfigRevision,
      }),
    },
  )
}
