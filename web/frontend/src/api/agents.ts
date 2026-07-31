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
  account_ref: string
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
  account_ref: string
  model: AgentModelPolicy | null
  skills: string[] | null
  subagents: AgentDelegationPolicy | null
}

export interface AgentMutationEffects {
  launcher_effect: "applied"
  catalog_effect: "applied"
  gateway_effect: "applied" | "restart_required"
}

export type AgentToolsCapabilityMode = "all" | "none" | "selected"
export type AgentSkillsCapabilityMode = "inherit" | "none" | "selected"
export type AgentMCPCapabilityMode = "all" | "none" | "selected"

export interface AgentCapabilityPolicy<Mode extends string> {
  mode: Mode
  values: string[]
}

export interface AgentSkillsCapabilityPolicy extends AgentCapabilityPolicy<AgentSkillsCapabilityMode> {
  inherited_values: string[]
}

export interface AgentToolCatalogItem {
  name: string
  description: string
  category: string
  status: "enabled" | "disabled" | "blocked"
  reason_code: string
}

export interface AgentSkillCatalogItem {
  name: string
  source: string
}

export interface AgentMCPServerCatalogItem {
  name: string
  enabled: boolean
}

export interface AgentCapabilitiesResponse {
  agent_id: string
  source: "agent" | "legacy" | "missing"
  editable: boolean
  issue_code: string
  legacy_upgrade_required: boolean
  capabilities: {
    tools: AgentCapabilityPolicy<AgentToolsCapabilityMode>
    skills: AgentSkillsCapabilityPolicy
    mcp_servers: AgentCapabilityPolicy<AgentMCPCapabilityMode>
  }
  catalogs: {
    tools: AgentToolCatalogItem[]
    skills: AgentSkillCatalogItem[]
    mcp_servers: AgentMCPServerCatalogItem[]
  }
  catalog_truncated: {
    tools: boolean
    skills: boolean
    mcp_servers: boolean
  }
  revision: string
  config_revision: string
  effects: AgentMutationEffects
}

export interface AgentCapabilitiesPatch {
  expected_revision: string
  upgrade_legacy?: boolean
  tools?: AgentCapabilityPolicy<AgentToolsCapabilityMode>
  skills?: AgentCapabilityPolicy<AgentSkillsCapabilityMode>
  mcp_servers?: AgentCapabilityPolicy<AgentMCPCapabilityMode>
}

interface AgentActivityEventBase {
  sequence: string
  agent_id: string
  timestamp: string
  severity: "info" | "warn" | "error"
}

export type AgentActivityEvent =
  | (AgentActivityEventBase & {
      kind: "agent.turn.start"
      details: { media_count: number }
    })
  | (AgentActivityEventBase & {
      kind: "agent.turn.end"
      details: {
        status: "completed" | "error" | "aborted"
        iterations: number
        duration_ms: string
      }
    })
  | (AgentActivityEventBase & {
      kind: "agent.llm.request"
      details: { messages_count: number; tools_count: number }
    })
  | (AgentActivityEventBase & {
      kind: "agent.llm.response"
      details: { tool_calls: number; has_reasoning: boolean }
    })
  | (AgentActivityEventBase & {
      kind: "agent.llm.retry"
      details: {
        attempt: number
        max_retries: number
        backoff_ms: string
      }
    })
  | (AgentActivityEventBase & {
      kind: "agent.context.compress"
      details: {
        reason: "proactive_budget" | "llm_retry" | "summarize"
        dropped_messages: number
        remaining_messages: number
      }
    })
  | (AgentActivityEventBase & {
      kind: "agent.session.summarize"
      details: {
        summarized_messages: number
        kept_messages: number
        omitted_oversized: boolean
      }
    })
  | (AgentActivityEventBase & {
      kind: "agent.tool.exec_start"
      details: { tool_name: string }
    })
  | (AgentActivityEventBase & {
      kind: "agent.tool.exec_end"
      details: {
        tool_name: string
        duration_ms: string
        is_error: boolean
        async: boolean
      }
    })
  | (AgentActivityEventBase & {
      kind: "agent.tool.exec_skipped"
      details: { tool_name: string }
    })
  | (AgentActivityEventBase & {
      kind: "agent.steering.injected"
      details: { count: number }
    })
  | (AgentActivityEventBase & {
      kind: "agent.follow_up.queued"
      details: Record<never, never>
    })
  | (AgentActivityEventBase & {
      kind: "agent.interrupt.received"
      details: {
        interrupt_kind: "steering" | "graceful" | "hard_abort"
        queue_depth: number
      }
    })
  | (AgentActivityEventBase & {
      kind: "agent.subturn.spawn"
      details: { target_agent_id: string }
    })
  | (AgentActivityEventBase & {
      kind: "agent.subturn.end"
      details: {
        target_agent_id: string
        status: "completed" | "error"
      }
    })
  | (AgentActivityEventBase & {
      kind:
        | "agent.subturn.result_delivered"
        | "agent.subturn.orphan"
        | "agent.error"
      details: Record<never, never>
    })

export interface AgentActivityResponse {
  agent_id: string
  events: AgentActivityEvent[]
  next_cursor: string
  reset: boolean
  truncated: boolean
  dropped: {
    subscription: string
    retention: string
    projection: string
  }
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

export async function getAgentCapabilities(
  id: string,
  signal?: AbortSignal,
): Promise<AgentCapabilitiesResponse> {
  const response = await request<unknown>(
    `/api/agents/${encodeURIComponent(id)}/capabilities`,
    { signal },
  )
  return projectAgentCapabilitiesResponse(response, id)
}

export async function patchAgentCapabilities(
  id: string,
  patch: AgentCapabilitiesPatch,
  signal?: AbortSignal,
): Promise<AgentCapabilitiesResponse> {
  const response = await request<unknown>(
    `/api/agents/${encodeURIComponent(id)}/capabilities`,
    {
      method: "PATCH",
      headers: jsonHeaders,
      body: JSON.stringify(patch),
      signal,
    },
  )
  return projectAgentCapabilitiesResponse(response, id)
}

function projectAgentCapabilitiesResponse(
  value: unknown,
  requestedAgentID: string,
): AgentCapabilitiesResponse {
  const response = capabilityRecord(value)
  const agentID = capabilityAgentID(response.agent_id)
  if (agentID !== requestedAgentID) invalidAgentCapabilitiesResponse()

  const capabilities = capabilityRecord(response.capabilities)
  const skills = capabilityRecord(capabilities.skills)
  const catalogs = capabilityRecord(response.catalogs)
  const catalogTruncated = capabilityRecord(response.catalog_truncated)
  const effects = capabilityRecord(response.effects)

  return {
    agent_id: agentID,
    source: capabilityEnum(response.source, [
      "agent",
      "legacy",
      "missing",
    ] as const),
    editable: capabilityBoolean(response.editable),
    issue_code: capabilityCode(response.issue_code),
    legacy_upgrade_required: capabilityBoolean(
      response.legacy_upgrade_required,
    ),
    capabilities: {
      tools: projectCapabilityPolicy(
        capabilities.tools,
        ["all", "none", "selected"] as const,
        true,
      ),
      skills: {
        ...projectCapabilityPolicy(
          skills,
          ["inherit", "none", "selected"] as const,
          false,
        ),
        inherited_values: projectCapabilityValues(
          skills.inherited_values,
          false,
        ),
      },
      mcp_servers: projectCapabilityPolicy(
        capabilities.mcp_servers,
        ["all", "none", "selected"] as const,
        true,
      ),
    },
    catalogs: {
      tools: projectCapabilityCatalog(catalogs.tools, (item) => {
        const tool = capabilityRecord(item)
        return {
          name: capabilityIdentifier(tool.name, true),
          description: capabilityText(tool.description, 4096),
          category: capabilityText(tool.category, 128),
          status: capabilityEnum(tool.status, [
            "enabled",
            "disabled",
            "blocked",
          ] as const),
          reason_code: capabilityCode(tool.reason_code),
        }
      }),
      skills: projectCapabilityCatalog(catalogs.skills, (item) => {
        const skill = capabilityRecord(item)
        return {
          name: capabilityIdentifier(skill.name, false),
          source: capabilityEnum(skill.source, [
            "workspace",
            "global",
            "builtin",
          ] as const),
        }
      }),
      mcp_servers: projectCapabilityCatalog(catalogs.mcp_servers, (item) => {
        const server = capabilityRecord(item)
        return {
          name: capabilityIdentifier(server.name, true),
          enabled: capabilityBoolean(server.enabled),
        }
      }),
    },
    catalog_truncated: {
      tools: capabilityBoolean(catalogTruncated.tools),
      skills: capabilityBoolean(catalogTruncated.skills),
      mcp_servers: capabilityBoolean(catalogTruncated.mcp_servers),
    },
    revision: capabilityOpaqueToken(response.revision),
    config_revision: capabilityOpaqueToken(response.config_revision),
    effects: {
      launcher_effect: capabilityEnum(effects.launcher_effect, [
        "applied",
      ] as const),
      catalog_effect: capabilityEnum(effects.catalog_effect, [
        "applied",
      ] as const),
      gateway_effect: capabilityEnum(effects.gateway_effect, [
        "applied",
        "restart_required",
      ] as const),
    },
  }
}

function projectCapabilityPolicy<const Modes extends readonly string[]>(
  value: unknown,
  modes: Modes,
  lower: boolean,
): AgentCapabilityPolicy<Modes[number]> {
  const policy = capabilityRecord(value)
  const mode = capabilityEnum(policy.mode, modes)
  const values = projectCapabilityValues(policy.values, lower)
  if ((mode === "selected") !== values.length > 0) {
    invalidAgentCapabilitiesResponse()
  }
  return { mode, values }
}

function projectCapabilityValues(value: unknown, lower: boolean): string[] {
  const values = capabilityArray(value)
  if (values.length > 1024) invalidAgentCapabilitiesResponse()

  const seen = new Set<string>()
  return values.map((item) => {
    const projected = capabilityText(item, 1024, false)
    if (
      projected === "" ||
      projected !== projected.trim() ||
      (lower && projected !== projected.toLowerCase())
    ) {
      invalidAgentCapabilitiesResponse()
    }
    const key = projected.toLowerCase()
    if (seen.has(key)) invalidAgentCapabilitiesResponse()
    seen.add(key)
    return projected
  })
}

function projectCapabilityCatalog<Item extends { name: string }>(
  value: unknown,
  project: (value: unknown) => Item,
): Item[] {
  const catalog = capabilityArray(value)
  if (catalog.length > 256) invalidAgentCapabilitiesResponse()

  const seen = new Set<string>()
  return catalog.map((value) => {
    const item = project(value)
    const key = item.name.toLowerCase()
    if (seen.has(key)) invalidAgentCapabilitiesResponse()
    seen.add(key)
    return item
  })
}

function capabilityRecord(value: unknown): Record<string, unknown> {
  if (value == null || typeof value !== "object" || Array.isArray(value)) {
    invalidAgentCapabilitiesResponse()
  }
  return value as Record<string, unknown>
}

function capabilityArray(value: unknown): unknown[] {
  if (!Array.isArray(value)) invalidAgentCapabilitiesResponse()
  return value
}

function capabilityAgentID(value: unknown): string {
  const projected = capabilityString(value)
  if (!/^[a-z0-9][a-z0-9_-]{0,63}$/.test(projected)) {
    invalidAgentCapabilitiesResponse()
  }
  return projected
}

function capabilityIdentifier(value: unknown, lower: boolean): string {
  const projected = capabilityText(value, 128, false)
  if (
    projected === "" ||
    projected !== projected.trim() ||
    /[\s/\\]/u.test(projected) ||
    (lower && projected !== projected.toLowerCase())
  ) {
    invalidAgentCapabilitiesResponse()
  }
  return projected
}

function capabilityCode(value: unknown): string {
  const projected = capabilityString(value)
  if (projected.length > 128 || !/^(?:[a-z0-9_]+)?$/.test(projected)) {
    invalidAgentCapabilitiesResponse()
  }
  return projected
}

function capabilityOpaqueToken(value: unknown): string {
  const projected = capabilityText(value, 512, false)
  if (projected === "" || projected !== projected.trim()) {
    invalidAgentCapabilitiesResponse()
  }
  return projected
}

function capabilityText(
  value: unknown,
  maxBytes: number,
  allowEmpty = true,
): string {
  const projected = capabilityString(value)
  if (
    (!allowEmpty && projected === "") ||
    new TextEncoder().encode(projected).byteLength > maxBytes ||
    hasInvalidCapabilityCharacters(projected)
  ) {
    invalidAgentCapabilitiesResponse()
  }
  return projected
}

function hasInvalidCapabilityCharacters(value: string): boolean {
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index)
    if (code <= 0x1f || (code >= 0x7f && code <= 0x9f)) return true
    if (code >= 0xd800 && code <= 0xdbff) {
      const next = value.charCodeAt(index + 1)
      if (next < 0xdc00 || next > 0xdfff) return true
      index += 1
    } else if (code >= 0xdc00 && code <= 0xdfff) {
      return true
    }
  }
  return false
}

function capabilityString(value: unknown): string {
  if (typeof value !== "string") invalidAgentCapabilitiesResponse()
  return value
}

function capabilityBoolean(value: unknown): boolean {
  if (typeof value !== "boolean") invalidAgentCapabilitiesResponse()
  return value
}

function capabilityEnum<const Values extends readonly string[]>(
  value: unknown,
  values: Values,
): Values[number] {
  if (typeof value !== "string" || !values.includes(value)) {
    invalidAgentCapabilitiesResponse()
  }
  return value as Values[number]
}

function invalidAgentCapabilitiesResponse(): never {
  throw new Error("invalid_agent_capabilities_response")
}

export async function getAgentActivity(
  id: string,
  options: { cursor?: string; limit?: number; signal?: AbortSignal } = {},
): Promise<AgentActivityResponse> {
  const parameters = new URLSearchParams()
  parameters.set("limit", String(options.limit ?? 100))
  if (options.cursor) parameters.set("cursor", options.cursor)
  const response = await request<unknown>(
    `/api/agents/${encodeURIComponent(id)}/activity?${parameters.toString()}`,
    { signal: options.signal },
  )
  const projected = projectAgentActivityResponse(response)
  if (
    projected.agent_id !== id ||
    projected.events.some((event) => event.agent_id !== id)
  ) {
    throw new Error("invalid_agent_activity_response")
  }
  return projected
}

function projectAgentActivityResponse(value: unknown): AgentActivityResponse {
  const response = record(value)
  const agentID = canonicalAgentID(response.agent_id)
  const events = array(response.events).map(projectAgentActivityEvent)
  return {
    agent_id: agentID,
    events,
    next_cursor: stringValue(response.next_cursor),
    reset: booleanValue(response.reset),
    truncated: booleanValue(response.truncated),
    dropped: projectDropped(response.dropped),
  }
}

function projectAgentActivityEvent(value: unknown): AgentActivityEvent {
  const event = record(value)
  const base = {
    sequence: decimalString(event.sequence),
    agent_id: canonicalAgentID(event.agent_id),
    timestamp: utcTimestamp(event.timestamp),
    severity: enumValue(event.severity, ["info", "warn", "error"] as const),
  }
  const details = record(event.details)

  switch (event.kind) {
    case "agent.turn.start":
      return {
        ...base,
        kind: event.kind,
        details: { media_count: integerValue(details.media_count) },
      }
    case "agent.turn.end":
      return {
        ...base,
        kind: event.kind,
        details: {
          status: enumValue(details.status, [
            "completed",
            "error",
            "aborted",
          ] as const),
          iterations: integerValue(details.iterations),
          duration_ms: decimalString(details.duration_ms),
        },
      }
    case "agent.llm.request":
      return {
        ...base,
        kind: event.kind,
        details: {
          messages_count: integerValue(details.messages_count),
          tools_count: integerValue(details.tools_count),
        },
      }
    case "agent.llm.response":
      return {
        ...base,
        kind: event.kind,
        details: {
          tool_calls: integerValue(details.tool_calls),
          has_reasoning: booleanValue(details.has_reasoning),
        },
      }
    case "agent.llm.retry":
      return {
        ...base,
        kind: event.kind,
        details: {
          attempt: integerValue(details.attempt),
          max_retries: integerValue(details.max_retries),
          backoff_ms: decimalString(details.backoff_ms),
        },
      }
    case "agent.context.compress":
      return {
        ...base,
        kind: event.kind,
        details: {
          reason: enumValue(details.reason, [
            "proactive_budget",
            "llm_retry",
            "summarize",
          ] as const),
          dropped_messages: integerValue(details.dropped_messages),
          remaining_messages: integerValue(details.remaining_messages),
        },
      }
    case "agent.session.summarize":
      return {
        ...base,
        kind: event.kind,
        details: {
          summarized_messages: integerValue(details.summarized_messages),
          kept_messages: integerValue(details.kept_messages),
          omitted_oversized: booleanValue(details.omitted_oversized),
        },
      }
    case "agent.tool.exec_start":
    case "agent.tool.exec_skipped":
      return {
        ...base,
        kind: event.kind,
        details: { tool_name: safeToolName(details.tool_name) },
      }
    case "agent.follow_up.queued":
    case "agent.subturn.result_delivered":
    case "agent.subturn.orphan":
    case "agent.error":
      return { ...base, kind: event.kind, details: {} }
    case "agent.tool.exec_end":
      return {
        ...base,
        kind: event.kind,
        details: {
          tool_name: safeToolName(details.tool_name),
          duration_ms: decimalString(details.duration_ms),
          is_error: booleanValue(details.is_error),
          async: booleanValue(details.async),
        },
      }
    case "agent.steering.injected":
      return {
        ...base,
        kind: event.kind,
        details: { count: integerValue(details.count) },
      }
    case "agent.interrupt.received":
      return {
        ...base,
        kind: event.kind,
        details: {
          interrupt_kind: enumValue(details.interrupt_kind, [
            "steering",
            "graceful",
            "hard_abort",
          ] as const),
          queue_depth: integerValue(details.queue_depth),
        },
      }
    case "agent.subturn.spawn":
      return {
        ...base,
        kind: event.kind,
        details: {
          target_agent_id: canonicalAgentID(details.target_agent_id),
        },
      }
    case "agent.subturn.end":
      return {
        ...base,
        kind: event.kind,
        details: {
          target_agent_id: canonicalAgentID(details.target_agent_id),
          status: enumValue(details.status, ["completed", "error"] as const),
        },
      }
    default:
      throw new Error("invalid_agent_activity_response")
  }
}

function projectDropped(value: unknown): AgentActivityResponse["dropped"] {
  const dropped = record(value)
  return {
    subscription: decimalString(dropped.subscription),
    retention: decimalString(dropped.retention),
    projection: decimalString(dropped.projection),
  }
}

function record(value: unknown): Record<string, unknown> {
  if (value == null || typeof value !== "object" || Array.isArray(value)) {
    throw new Error("invalid_agent_activity_response")
  }
  return value as Record<string, unknown>
}

function array(value: unknown): unknown[] {
  if (!Array.isArray(value)) throw new Error("invalid_agent_activity_response")
  return value
}

function stringValue(value: unknown): string {
  if (typeof value !== "string") {
    throw new Error("invalid_agent_activity_response")
  }
  return value
}

function utcTimestamp(value: unknown): string {
  const projected = stringValue(value)
  if (
    !/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?Z$/.test(projected) ||
    Number.isNaN(Date.parse(projected))
  ) {
    throw new Error("invalid_agent_activity_response")
  }
  return projected
}

function decimalString(value: unknown): string {
  const projected = stringValue(value)
  if (!/^(0|[1-9][0-9]*)$/.test(projected)) {
    throw new Error("invalid_agent_activity_response")
  }
  return projected
}

function canonicalAgentID(value: unknown): string {
  const projected = stringValue(value)
  if (!/^[a-z0-9][a-z0-9_-]{0,63}$/.test(projected)) {
    throw new Error("invalid_agent_activity_response")
  }
  return projected
}

function safeToolName(value: unknown): string {
  const projected = stringValue(value)
  if (!/^[A-Za-z0-9_-]{1,64}$/.test(projected)) {
    throw new Error("invalid_agent_activity_response")
  }
  return projected
}

function booleanValue(value: unknown): boolean {
  if (typeof value !== "boolean") {
    throw new Error("invalid_agent_activity_response")
  }
  return value
}

function integerValue(value: unknown): number {
  if (typeof value !== "number" || !Number.isSafeInteger(value) || value < 0) {
    throw new Error("invalid_agent_activity_response")
  }
  return value
}

function enumValue<const Values extends readonly string[]>(
  value: unknown,
  values: Values,
): Values[number] {
  if (typeof value !== "string" || !values.includes(value)) {
    throw new Error("invalid_agent_activity_response")
  }
  return value
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
