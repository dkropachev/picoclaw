import {
  type CollectionConfigBulkDeleteResponse,
  type CollectionMutationEffects,
  type CollectionQuerySchema,
  collectionListURL,
  collectionRequest,
} from "@/api/collection"

export type EventSourceKind = "webhook" | "channel"
export type EventSourceFormat = "standard" | "github" | "deltachat"
export type EventSourceStatus =
  | "available"
  | "disabled"
  | "unconfigured"
  | "unreachable"
  | "invalid"
export type EventWebhookFormat = "standard" | "github"
export type EventChannelMode = "mirror" | "event_only"
export type EventSecretUpdate = "preserve" | "replace" | "clear"

export interface EventSourceSummary {
  id: string
  name: string
  kind: EventSourceKind
  enabled: boolean
  format: EventSourceFormat
  status: EventSourceStatus
  repositories: number
  poll_notifications: boolean
}

interface EventSourceDetailBase {
  id: string
  name: string
  enabled: boolean
  status: EventSourceStatus
  poll_notifications: boolean
}

export interface EventWebhookSource extends EventSourceDetailBase {
  kind: "webhook"
  format: EventWebhookFormat
  repositories: string[]
  target_user: string
  secret_configured: boolean
  endpoint?: string
}

export interface EventChannelSource extends EventSourceDetailBase {
  kind: "channel"
  format: "deltachat"
  source: "email"
  mode: EventChannelMode
  allow_unverified_email: boolean
  channel_enabled: boolean
  channel_type: string
}

export type EventSource = EventWebhookSource | EventChannelSource

export interface EventSourcesCollectionResponse {
  event_sources: EventSourceSummary[]
  total: number
  next_cursor?: string
  canonical_query: string
  query_schema: CollectionQuerySchema
  config_revision: string
}

export interface EventSourceDetailResponse {
  event_source: EventSource
  config_revision: string
}

export interface EventSourceMutationResponse extends EventSourceDetailResponse {
  effects: CollectionMutationEffects
}

export type EventSourceDeleteResponse = CollectionConfigBulkDeleteResponse

export interface EventWebhookSourceInput {
  kind: "webhook"
  name: string
  enabled: boolean
  format: EventWebhookFormat
  repositories: string[]
  target_user: string
  poll_notifications: boolean
  secret_update: EventSecretUpdate
  secret?: string
}

export interface EventChannelSourceInput {
  kind: "channel"
  name: string
  enabled: boolean
  source: "email"
  mode: EventChannelMode
  allow_unverified_email: boolean
}

export type EventSourceInput = EventWebhookSourceInput | EventChannelSourceInput

export interface EventSourceSettings {
  enabled: boolean
  database_path: string
  retention_days: number
  max_payload_bytes: number
  redact_fields: string[]
}

export interface EligibleEventChannelAdapter {
  name: string
  channel_type: string
  channel_enabled: boolean
}

export interface EventSourceSettingsResponse {
  event_source_settings: EventSourceSettings
  eligible_channel_adapters: EligibleEventChannelAdapter[]
  config_revision: string
}

export interface EventSourceSettingsMutationResponse extends EventSourceSettingsResponse {
  effects: CollectionMutationEffects
}

export function listEventSources(
  options: { query?: string; cursor?: string; limit?: number } = {},
  signal?: AbortSignal,
): Promise<EventSourcesCollectionResponse> {
  return collectionRequest<EventSourcesCollectionResponse>(
    collectionListURL("/api/event-sources", options),
    undefined,
    signal,
  )
}

export function getEventSource(
  id: string,
  signal?: AbortSignal,
): Promise<EventSourceDetailResponse> {
  return collectionRequest<EventSourceDetailResponse>(
    `/api/event-sources/${encodeURIComponent(id)}`,
    undefined,
    signal,
  )
}

export function createEventSource(
  eventSource: EventSourceInput,
  expectedConfigRevision: string,
): Promise<EventSourceMutationResponse> {
  return collectionRequest<EventSourceMutationResponse>("/api/event-sources", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      expected_config_revision: expectedConfigRevision,
      event_source: eventSource,
    }),
  })
}

export function updateEventSource(
  id: string,
  eventSource: EventSourceInput,
  expectedConfigRevision: string,
): Promise<EventSourceMutationResponse> {
  return collectionRequest<EventSourceMutationResponse>(
    `/api/event-sources/${encodeURIComponent(id)}`,
    {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        expected_config_revision: expectedConfigRevision,
        event_source: eventSource,
      }),
    },
  )
}

export function deleteEventSource(
  id: string,
  expectedConfigRevision: string,
): Promise<EventSourceDeleteResponse> {
  return collectionRequest<EventSourceDeleteResponse>(
    `/api/event-sources/${encodeURIComponent(id)}`,
    {
      method: "DELETE",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        expected_config_revision: expectedConfigRevision,
      }),
    },
  )
}

export function bulkDeleteEventSources(
  ids: string[],
  configRevision: string,
): Promise<CollectionConfigBulkDeleteResponse> {
  return collectionRequest<CollectionConfigBulkDeleteResponse>(
    "/api/event-sources/bulk-delete",
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ ids, config_revision: configRevision }),
    },
  )
}

export function getEventSourceSettings(
  signal?: AbortSignal,
): Promise<EventSourceSettingsResponse> {
  return collectionRequest<EventSourceSettingsResponse>(
    "/api/event-source-settings",
    undefined,
    signal,
  )
}

export function updateEventSourceSettings(
  settings: EventSourceSettings,
  expectedConfigRevision: string,
): Promise<EventSourceSettingsMutationResponse> {
  return collectionRequest<EventSourceSettingsMutationResponse>(
    "/api/event-source-settings",
    {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        expected_config_revision: expectedConfigRevision,
        event_source_settings: settings,
      }),
    },
  )
}
