import {
  type CollectionBulkDeleteResponse,
  type CollectionQuerySchema,
  collectionListURL,
  collectionRequest,
} from "@/api/collection"
import { launcherFetch } from "@/api/http"

export type MCPTransport = "stdio" | "http" | "sse"

export interface MCPDiscoverySettings {
  enabled: boolean
  ttl: number
  max_search_results: number
  use_bm25: boolean
  use_regex: boolean
}

export interface MCPAuthSummary {
  type: string
  configured: boolean
  expired?: boolean
}

export interface MCPServer {
  name: string
  enabled: boolean
  deferred: boolean | null
  type: MCPTransport
  url: string
  command: string
  args: string[]
  env_file: string
  env_keys: string[]
  header_keys: string[]
  auth: MCPAuthSummary
}

export interface MCPConfigResponse {
  enabled: boolean
  discovery: MCPDiscoverySettings
  servers: MCPServer[]
}

export interface MCPServerCollectionResponse {
  servers: MCPServer[]
  total: number
  next_cursor?: string
  canonical_query: string
  query_schema: CollectionQuerySchema
  config_revision: string
}

export interface MCPServerDetailResponse {
  server: MCPServer
  config_revision: string
}

export interface MCPServerInput {
  name: string
  enabled: boolean
  deferred: boolean | null
  type: MCPTransport
  url?: string
  command?: string
  args?: string[]
  env_file?: string
  env?: Record<string, string>
  env_keys?: string[]
  headers?: Record<string, string>
  header_keys?: string[]
  auth_mode?: "none" | "oauth" | "bearer" | "custom"
}

export interface MCPProbeTool {
  name: string
  description?: string
}

export interface MCPProbeResponse {
  ok: boolean
  tool_count: number
  tools: Array<MCPProbeTool | string>
  error?: string
  auth_required?: boolean
}

export type MCPOAuthFlowStatus = "pending" | "success" | "error" | "expired"

export interface MCPOAuthFlow {
  flow_id: string
  server_name: string
  status: MCPOAuthFlowStatus
  expires_at: string
  error?: string
  tool_count?: number
  tools?: string[]
}

export interface MCPStartOAuthResponse {
  status: string
  flow_id: string
  server_name: string
  auth_url: string
  expires_at: string
}

interface MCPActionResponse {
  status: string
}

async function request<T>(path: string, options?: RequestInit): Promise<T> {
  const response = await launcherFetch(path, options)
  if (!response.ok) {
    throw new Error(await extractErrorMessage(response))
  }
  return response.json() as Promise<T>
}

export function getMCPConfig(): Promise<MCPConfigResponse> {
  return request<MCPConfigResponse>("/api/mcp")
}

export function listMCPServers(
  options: { query?: string; cursor?: string; limit?: number } = {},
  signal?: AbortSignal,
): Promise<MCPServerCollectionResponse> {
  return collectionRequest<MCPServerCollectionResponse>(
    collectionListURL("/api/mcp/servers", options),
    undefined,
    signal,
  )
}

export function getMCPServer(
  name: string,
  signal?: AbortSignal,
): Promise<MCPServerDetailResponse> {
  return collectionRequest<MCPServerDetailResponse>(
    `/api/mcp/servers/${encodeURIComponent(name)}`,
    undefined,
    signal,
  )
}

export function updateMCPSettings(
  payload: Pick<MCPConfigResponse, "enabled" | "discovery">,
): Promise<MCPConfigResponse> {
  return request<MCPConfigResponse>("/api/mcp/settings", {
    method: "PATCH",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload),
  })
}

export function addMCPServer(
  server: MCPServerInput,
  expectedConfigRevision?: string,
): Promise<MCPConfigResponse> {
  return request<MCPConfigResponse>("/api/mcp/servers", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      ...server,
      ...(expectedConfigRevision
        ? { expected_config_revision: expectedConfigRevision }
        : {}),
    }),
  })
}

export function updateMCPServer(
  currentName: string,
  server: MCPServerInput,
  expectedConfigRevision?: string,
): Promise<MCPConfigResponse> {
  return request<MCPConfigResponse>(
    `/api/mcp/servers/${encodeURIComponent(currentName)}`,
    {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        ...server,
        ...(expectedConfigRevision
          ? { expected_config_revision: expectedConfigRevision }
          : {}),
      }),
    },
  )
}

export function deleteMCPServer(name: string): Promise<MCPActionResponse> {
  return request<MCPActionResponse>(
    `/api/mcp/servers/${encodeURIComponent(name)}`,
    {
      method: "DELETE",
    },
  )
}

export function bulkDeleteMCPServers(
  ids: string[],
  configRevision: string,
): Promise<CollectionBulkDeleteResponse> {
  return collectionRequest("/api/mcp/servers/bulk-delete", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ ids, config_revision: configRevision }),
  })
}

export function testMCPServer(
  server: MCPServerInput,
  currentName?: string,
): Promise<MCPProbeResponse> {
  return request<MCPProbeResponse>("/api/mcp/servers/test", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      ...(currentName ? { name: currentName } : {}),
      server,
    }),
  })
}

export function setMCPServerCredential(
  name: string,
  token: string,
): Promise<MCPActionResponse> {
  return request<MCPActionResponse>(
    `/api/mcp/servers/${encodeURIComponent(name)}/credential`,
    {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ token, auth_type: "bearer" }),
    },
  )
}

export function deleteMCPServerCredential(
  name: string,
): Promise<MCPActionResponse> {
  return request<MCPActionResponse>(
    `/api/mcp/servers/${encodeURIComponent(name)}/credential`,
    {
      method: "DELETE",
    },
  )
}

export function startMCPServerOAuth(
  name: string,
): Promise<MCPStartOAuthResponse> {
  return request<MCPStartOAuthResponse>(
    `/api/mcp/servers/${encodeURIComponent(name)}/oauth`,
    {
      method: "POST",
    },
  )
}

export function getMCPOAuthFlow(flowID: string): Promise<MCPOAuthFlow> {
  return request<MCPOAuthFlow>(
    `/api/mcp/oauth/flows/${encodeURIComponent(flowID)}`,
  )
}

async function extractErrorMessage(response: Response): Promise<string> {
  const fallback = `API error: ${response.status} ${response.statusText}`
  try {
    const raw = await response.text()
    if (!raw.trim()) return fallback
    try {
      const body = JSON.parse(raw) as {
        error?: string
        errors?: string[]
      }
      if (Array.isArray(body.errors) && body.errors.length > 0) {
        return body.errors.join("; ")
      }
      if (typeof body.error === "string" && body.error.trim()) {
        return body.error.trim()
      }
    } catch {
      return raw.trim()
    }
  } catch {
    // Keep the HTTP fallback when the body cannot be read.
  }
  return fallback
}
