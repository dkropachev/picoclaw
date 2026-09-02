import {
  type CollectionQuerySchema,
  collectionListURL,
  collectionRequest,
} from "@/api/collection"
import { launcherFetch } from "@/api/http"

export interface ToolSupportItem {
  id: string
  name: string
  description: string
  category: string
  config_key: string
  status: "enabled" | "disabled" | "blocked"
  reason?: string
  reason_code?: string
}

export interface ToolsCollectionResponse {
  tools: ToolSupportItem[]
  total: number
  next_cursor?: string
  canonical_query: string
  query_schema: CollectionQuerySchema
}

export interface ToolDetailResponse {
  tool: ToolSupportItem
}

interface ToolActionResponse {
  status: string
}

export interface WebSearchProviderOption {
  id: string
  label: string
  configured: boolean
  current: boolean
  requires_auth: boolean
}

export interface WebSearchProviderConfig {
  enabled: boolean
  max_results: number
  base_url?: string
  api_key?: string
  api_keys?: string[]
  model_alias?: string
  api_key_set?: boolean
}

export interface WebSearchConfigResponse {
  provider: string
  current_service: string
  prefer_native: boolean
  proxy?: string
  providers: WebSearchProviderOption[]
  model_aliases: string[]
  settings: Record<string, WebSearchProviderConfig>
}

export type ThreadPolicyMode = "auto" | "tool" | "suggest" | "off"
export type ThreadAttachStrategy =
  | "search_then_create"
  | "search_then_ask"
  | "never"
export type ThreadPolicyThresholdLogic = "any" | "all"
export type ThreadPolicyRuleType =
  | "general"
  | "coding"
  | "reviewing"
  | "investigating"

export interface ThreadPolicyRule {
  type: ThreadPolicyRuleType
  description: string
  mode?: ThreadPolicyMode
  attach_strategy?: ThreadAttachStrategy
  min_messages?: number
  min_text_chars?: number
  threshold_logic?: ThreadPolicyThresholdLogic
  min_auto_confidence?: number
  confirm_if_multiple?: boolean
}

export interface ThreadAgentPolicy {
  mode?: ThreadPolicyMode
  attach_strategy?: ThreadAttachStrategy
}

export interface ThreadPolicyConfig {
  enabled: boolean
  mode: ThreadPolicyMode
  instructions: string
  rules: ThreadPolicyRule[]
  agents?: Record<string, ThreadAgentPolicy>
}

export type VisibleToolSurface = "auto" | "codex" | "picoclaw" | "simple"
export type RuntimeAdaptationPolicy = "auto" | "never" | "allow"
export type CacheSensitivityPolicy = "auto" | "never" | "always"
export type VisibleChangePolicy =
  | "never"
  | "next_session"
  | "context_boundary"
  | "immediate"

export interface ToolAdaptationConfig {
  enabled: boolean
  visible_tool_surface: VisibleToolSurface
  learn_from_tool_calls: boolean
  run_model_probes: boolean
  allow_runtime_downgrade: RuntimeAdaptationPolicy
  allow_runtime_promotion: RuntimeAdaptationPolicy
  apply_visible_changes: VisibleChangePolicy
  cache_sensitive_apis: CacheSensitivityPolicy
  cache_breaking_downgrade: boolean
  profile_overrides?: ToolAdaptationProfileOverride[]
  resolved?: ToolAdaptationResolvedState
  observation?: ToolAdaptationObservation
  outcomes?: ToolAdaptationToolOutcome[]
  profiles?: ToolAdaptationProfileState[]
}

export interface ToolAdaptationProfileOverride {
  provider: string
  model: string
  visible_tool_surface?: VisibleToolSurface
  cache_sensitive_apis?: CacheSensitivityPolicy
}

export interface ToolAdaptationResolvedState {
  provider: string
  model: string
  store_id: string
  visible_tool_surface: VisibleToolSurface
  pinned_tool_surface: VisibleToolSurface
  surface_evidence: "disabled" | "config" | "heuristic" | "learned" | string
  runtime_downgrade: boolean
  runtime_promotion: boolean
  apply_visible_changes: VisibleChangePolicy
  cache_sensitive: boolean
  cache_evidence: "disabled" | "config" | "heuristic" | "sniffed" | string
}

export interface ToolAdaptationProfileState {
  id: string
  label: string
  source: string
  is_default: boolean
  is_override: boolean
  probe_available: boolean
  probe_account_ref?: string
  probe_model_alias?: string
  resolved: ToolAdaptationResolvedState
  observation?: ToolAdaptationObservation
  outcomes?: ToolAdaptationToolOutcome[]
}

export interface ToolAdaptationObservation {
  profile: {
    provider: string
    model: string
  }
  visible_tool_surface: string
  tool_schema_hash: string
  prompt_tokens: number
  cached_tokens: number
  cache_hit_ratio: number
  cache_sensitive: boolean
  sniffed: boolean
  observed_at: string
}

export interface ToolAdaptationToolOutcome {
  profile: {
    provider: string
    model: string
  }
  visible_tool_surface: string
  tool_name: string
  successes: number
  failures: number
  last_error?: string
  last_duration_ms: number
  updated_at: string
}

export interface ToolAdaptationProbeResult {
  profile: {
    provider: string
    model: string
  }
  visible_tool_surface: string
  tool_name: string
  success: boolean
  error?: string
  duration_ms: number
  ran_at: string
}

export interface ToolAdaptationProbeTarget {
  account_ref: string
  model_alias: string
}

async function request<T>(path: string, options?: RequestInit): Promise<T> {
  const res = await launcherFetch(path, options)
  if (!res.ok) {
    let message = `API error: ${res.status} ${res.statusText}`
    const responseText = await res.text()
    if (responseText.trim() !== "") {
      try {
        const body = JSON.parse(responseText) as {
          error?: string
          errors?: string[]
        }
        if (Array.isArray(body.errors) && body.errors.length > 0) {
          message = body.errors.join("; ")
        } else if (typeof body.error === "string" && body.error.trim() !== "") {
          message = body.error
        }
      } catch {
        message = responseText.trim()
      }
    }
    throw new Error(message)
  }
  return res.json() as Promise<T>
}

export function listTools(
  options: { query?: string; cursor?: string; limit?: number } = {},
  signal?: AbortSignal,
): Promise<ToolsCollectionResponse> {
  return collectionRequest<ToolsCollectionResponse>(
    collectionListURL("/api/tools", options),
    undefined,
    signal,
  )
}

/** @deprecated Collection UIs should use listTools. */
export function getTools(): Promise<ToolsCollectionResponse> {
  return listTools()
}

export function getTool(
  id: string,
  signal?: AbortSignal,
): Promise<ToolDetailResponse> {
  return collectionRequest<ToolDetailResponse>(
    `/api/tools/${encodeURIComponent(id)}`,
    undefined,
    signal,
  )
}

export async function setToolEnabled(
  name: string,
  enabled: boolean,
): Promise<ToolActionResponse> {
  return request<ToolActionResponse>(
    `/api/tools/${encodeURIComponent(name)}/state`,
    {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ enabled }),
    },
  )
}

export async function getWebSearchConfig(): Promise<WebSearchConfigResponse> {
  return request<WebSearchConfigResponse>("/api/tools/web-search-config")
}

export async function updateWebSearchConfig(
  payload: WebSearchConfigResponse,
): Promise<WebSearchConfigResponse> {
  return request<WebSearchConfigResponse>("/api/tools/web-search-config", {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload),
  })
}

export async function getThreadPolicy(): Promise<ThreadPolicyConfig> {
  return request<ThreadPolicyConfig>("/api/tools/thread-policy")
}

export async function updateThreadPolicy(
  payload: ThreadPolicyConfig,
): Promise<ThreadPolicyConfig> {
  return request<ThreadPolicyConfig>("/api/tools/thread-policy", {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload),
  })
}

export async function getToolAdaptation(): Promise<ToolAdaptationConfig> {
  return request<ToolAdaptationConfig>("/api/tools/adaptation")
}

export async function updateToolAdaptation(
  payload: ToolAdaptationConfig,
): Promise<ToolAdaptationConfig> {
  return request<ToolAdaptationConfig>("/api/tools/adaptation", {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload),
  })
}

export async function runToolAdaptationProbe(
  profile?: ToolAdaptationProbeTarget,
): Promise<ToolAdaptationProbeResult> {
  return request<ToolAdaptationProbeResult>("/api/tools/adaptation/probe", {
    method: "POST",
    ...(profile
      ? {
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(profile),
        }
      : {}),
  })
}
