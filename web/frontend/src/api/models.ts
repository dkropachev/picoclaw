import { launcherFetch } from "@/api/http"
import { refreshGatewayState } from "@/store/gateway"

// API client for model list management.

export interface ModelInfo {
  index: number
  model_name: string
  provider?: string
  model: string
  api_base?: string
  api_key: string
  proxy?: string
  auth_method?: string
  credential_id?: string
  router?: AccountRouterConfig
  model_router?: ModelRouterConfig
  // Advanced fields
  connect_mode?: string
  workspace?: string
  rpm?: number
  max_tokens_field?: string
  request_timeout?: number
  thinking_level?: string
  reasoning_effort?: string
  tool_schema_transform?: string
  streaming?: {
    enabled?: boolean
  }
  extra_body?: Record<string, unknown>
  custom_headers?: Record<string, string>
  // Meta
  enabled: boolean
  available: boolean
  status: "available" | "unconfigured" | "unreachable"
  is_default: boolean
  is_virtual: boolean
}

export interface AccountRouterConfig {
  name?: string
  enabled?: boolean
  entry?: string
  refresh_interval_seconds?: number
  blocks?: AccountRouterBlock[]
}

export interface AccountRouterBlock {
  id: string
  type: "account" | "load_balance" | "branch"
  account?: string
  accounts?: string[]
  fallback?: string
  strategy?: "blind" | "tokens_spent" | "closest_limit"
  refresh_interval_seconds?: number
  condition?: AccountRouterCondition
  then?: string
  else?: string
}

export interface AccountRouterCondition {
  left: AccountRouterExpression
  operator: "gt" | "gte" | "lt" | "lte" | "eq" | "neq"
  right: AccountRouterExpression
}

export interface AccountRouterExpression {
  account?: string
  metric?: string
  value?: number
  op?: "add" | "subtract" | "multiply" | "divide" | "modulo"
  left?: AccountRouterExpression
  right?: AccountRouterExpression
}

export interface ModelRouterConfig {
  name?: string
  enabled?: boolean
  entry?: string
  blocks?: ModelRouterBlock[]
}

export interface ModelRouterBlock {
  id: string
  type: "model" | "rules"
  model?: string
  rules?: ModelRouterRule[]
  fallback?: string
}

export interface ModelRouterRule {
  match: "contains" | "regex" | "has_code" | "has_media"
  value?: string
  target: string
}

export interface ModelProviderOption {
  id: string
  display_name?: string
  icon_slug?: string
  domain?: string
  default_api_base: string
  empty_api_key_allowed: boolean
  create_allowed: boolean
  supports_fetch?: boolean
  default_auth_method?: string
  auth_method_locked?: boolean
  local?: boolean
  priority?: number
  common_models?: string[]
  aliases?: string[]
}

export interface ModelAlias {
  name: string
  model: string
  account_overrides?: Record<string, string>
}

interface ModelsListResponse {
  models: ModelInfo[]
  model_aliases: ModelAlias[]
  total: number
  default_model: string
  default_account_ref: string
  revision: string
  provider_options: ModelProviderOption[]
}

interface ModelActionResponse {
  status: string
  index?: number
  default_model?: string
  default_account_ref?: string
}

export type ModelMutation = Partial<ModelInfo> & {
  set_as_default?: boolean
}

const BASE_URL = ""

async function request<T>(path: string, options?: RequestInit): Promise<T> {
  const res = await launcherFetch(`${BASE_URL}${path}`, options)
  if (!res.ok) {
    let detail = ""
    try {
      detail = await res.text()
    } catch {
      // ignore
    }
    throw new Error(detail || `API error: ${res.status} ${res.statusText}`)
  }
  return res.json() as Promise<T>
}

export async function getModels(): Promise<ModelsListResponse> {
  return request<ModelsListResponse>("/api/accounts/models")
}

export async function addModel(
  model: ModelMutation,
): Promise<ModelActionResponse> {
  return request<ModelActionResponse>("/api/accounts/models", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(model),
  })
}

export async function updateModel(
  index: number,
  revision: string,
  model: ModelMutation,
): Promise<ModelActionResponse> {
  return request<ModelActionResponse>(
    `/api/accounts/models/${index}?revision=${encodeURIComponent(revision)}`,
    {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(model),
    },
  )
}

export async function deleteModel(
  index: number,
  revision: string,
): Promise<ModelActionResponse> {
  return request<ModelActionResponse>(
    `/api/accounts/models/${index}?revision=${encodeURIComponent(revision)}`,
    {
      method: "DELETE",
    },
  )
}

export async function setDefaultSelection(
  accountRef: string,
  modelAlias: string,
): Promise<ModelActionResponse> {
  const response = await request<ModelActionResponse>(
    "/api/accounts/models/default",
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        account_ref: accountRef,
        model_name: modelAlias,
      }),
    },
  )

  await refreshGatewayState()
  return response
}

export async function setDefaultAccount(
  accountRef: string,
): Promise<ModelActionResponse> {
  const current = await getModels()
  if (!current.default_model?.trim()) {
    throw new Error("no model configured")
  }
  return setDefaultSelection(accountRef, current.default_model)
}

export async function setDefaultModelAlias(
  modelAlias: string,
): Promise<ModelActionResponse> {
  const current = await getModels()
  if (!current.default_account_ref?.trim()) {
    throw new Error("no account configured")
  }
  return setDefaultSelection(current.default_account_ref, modelAlias)
}

export async function addModelAlias(
  alias: ModelAlias,
): Promise<ModelActionResponse> {
  return request<ModelActionResponse>("/api/accounts/model-aliases", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(alias),
  })
}

export async function updateModelAlias(
  index: number,
  revision: string,
  alias: ModelAlias,
): Promise<ModelActionResponse> {
  return request<ModelActionResponse>(
    `/api/accounts/model-aliases/${index}?revision=${encodeURIComponent(revision)}`,
    {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(alias),
    },
  )
}

export async function deleteModelAlias(
  index: number,
  revision: string,
): Promise<ModelActionResponse> {
  return request<ModelActionResponse>(
    `/api/accounts/model-aliases/${index}?revision=${encodeURIComponent(revision)}`,
    {
      method: "DELETE",
    },
  )
}

export interface TestModelResponse {
  success: boolean
  latency_ms: number
  status: string
  error?: string
}

export async function testModel(index: number): Promise<TestModelResponse> {
  return request<TestModelResponse>(`/api/accounts/models/${index}/test`, {
    method: "POST",
  })
}

export interface TestModelInlineRequest {
  provider: string
  model: string
  api_base?: string
  api_key?: string
  auth_method?: string
  credential_id?: string
  model_index?: number
}

export async function testModelInline(
  params: TestModelInlineRequest,
): Promise<TestModelResponse> {
  return request<TestModelResponse>("/api/accounts/models/test-inline", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(params),
  })
}

export interface UpstreamModel {
  id: string
  owned_by?: string
  extra?: Record<string, unknown>
}

interface FetchModelsByProviderRequest {
  provider: string
  account_ref?: never
  api_key?: string
  api_base?: string
  auth_method?: string
  credential_id?: string
  model_index?: number
}

interface FetchModelsByAccountRequest {
  account_ref: string
  provider?: never
  api_key?: never
  api_base?: never
  auth_method?: never
  credential_id?: never
  model_index?: never
}

export type FetchModelsRequest =
  | FetchModelsByProviderRequest
  | FetchModelsByAccountRequest

export interface FetchModelsResponse {
  models: UpstreamModel[]
  total: number
  issues?: Array<{
    account_ref: string
    error: string
  }>
}

export async function fetchUpstreamModels(
  req: FetchModelsRequest,
): Promise<FetchModelsResponse> {
  return request<FetchModelsResponse>("/api/accounts/models/fetch", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(req),
  })
}

// --- Model Catalog API ---

export interface CatalogModel {
  id: string
  owned_by?: string
  extra?: Record<string, unknown>
}

export interface CatalogEntry {
  id: string
  provider: string
  api_base: string
  api_key_mask: string
  models: CatalogModel[]
  fetched_at: string
}

interface CatalogListResponse {
  entries: CatalogEntry[]
  total: number
}

export async function getCatalogs(): Promise<CatalogListResponse> {
  return request<CatalogListResponse>("/api/accounts/models/catalog")
}

export async function deleteCatalog(id: string): Promise<void> {
  await request<Record<string, never>>(
    `/api/accounts/models/catalog/${encodeURIComponent(id)}`,
    {
      method: "DELETE",
    },
  )
}

export type { ModelsListResponse, ModelActionResponse }
