import {
  type CollectionConfigBulkDeleteResponse,
  type CollectionMutationEffects,
  type CollectionQuerySchema,
  collectionListURL,
  collectionRequest,
} from "@/api/collection"
import type { AccountRouterBlock } from "@/api/models"

export interface AccountSummary {
  id: string
  provider: string
  account: string
  status: "connected" | "expired" | "needs_refresh" | "not_logged_in"
  auth_method: string
  expires_at: string
}

export interface AccountsCollectionResponse {
  accounts: AccountSummary[]
  total: number
  next_cursor?: string
  canonical_query: string
  query_schema: CollectionQuerySchema
}

export interface AccountDetailResponse {
  account: AccountSummary
}

export interface AccountRouterSummary {
  id: string
  name: string
  enabled: boolean
  is_default: boolean
  status: string
  entry: string
  accounts: number
  blocks: number
}

export interface AccountRouter {
  id: string
  name: string
  enabled: boolean
  is_default: boolean
  status: string
  entry: string
  accounts: string[]
  blocks: AccountRouterBlock[]
  refresh_interval_seconds?: number
}

export interface AccountRoutersCollectionResponse {
  account_routers: AccountRouterSummary[]
  total: number
  next_cursor?: string
  canonical_query: string
  query_schema: CollectionQuerySchema
  config_revision: string
}

export interface AccountRouterDetailResponse {
  account_router: AccountRouter
  config_revision: string
}

export interface AccountRouterMutationResponse extends AccountRouterDetailResponse {
  effects: CollectionMutationEffects
}

export interface AccountRouterInput {
  name: string
  enabled?: boolean
  entry?: string
  refresh_interval_seconds?: number
  blocks?: AccountRouterBlock[]
}

export function listAccounts(
  options: { query?: string; cursor?: string; limit?: number } = {},
  signal?: AbortSignal,
): Promise<AccountsCollectionResponse> {
  return collectionRequest<AccountsCollectionResponse>(
    collectionListURL("/api/accounts", options),
    undefined,
    signal,
  )
}

export function getAccount(
  id: string,
  signal?: AbortSignal,
): Promise<AccountDetailResponse> {
  return collectionRequest<AccountDetailResponse>(
    `/api/accounts/${encodeURIComponent(id)}`,
    undefined,
    signal,
  )
}

export function listAccountRouters(
  options: { query?: string; cursor?: string; limit?: number } = {},
  signal?: AbortSignal,
): Promise<AccountRoutersCollectionResponse> {
  return collectionRequest<AccountRoutersCollectionResponse>(
    collectionListURL("/api/account-routers", options),
    undefined,
    signal,
  )
}

export function getAccountRouter(
  id: string,
  signal?: AbortSignal,
): Promise<AccountRouterDetailResponse> {
  return collectionRequest<AccountRouterDetailResponse>(
    `/api/account-routers/${encodeURIComponent(id)}`,
    undefined,
    signal,
  )
}

export function createAccountRouter(
  accountRouter: AccountRouterInput,
  expectedConfigRevision: string,
): Promise<AccountRouterMutationResponse> {
  return collectionRequest<AccountRouterMutationResponse>(
    "/api/account-routers",
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        expected_config_revision: expectedConfigRevision,
        account_router: accountRouter,
      }),
    },
  )
}

export function updateAccountRouter(
  id: string,
  accountRouter: AccountRouterInput,
  expectedConfigRevision: string,
): Promise<AccountRouterMutationResponse> {
  return collectionRequest<AccountRouterMutationResponse>(
    `/api/account-routers/${encodeURIComponent(id)}`,
    {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        expected_config_revision: expectedConfigRevision,
        account_router: accountRouter,
      }),
    },
  )
}

export function setDefaultAccountRouter(
  id: string,
  expectedConfigRevision: string,
): Promise<AccountRouterMutationResponse> {
  return collectionRequest<AccountRouterMutationResponse>(
    `/api/account-routers/${encodeURIComponent(id)}/default`,
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        expected_config_revision: expectedConfigRevision,
      }),
    },
  )
}

export function bulkDeleteAccountRouters(
  ids: string[],
  configRevision: string,
): Promise<CollectionConfigBulkDeleteResponse> {
  return collectionRequest<CollectionConfigBulkDeleteResponse>(
    "/api/account-routers/bulk-delete",
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ ids, config_revision: configRevision }),
    },
  )
}
