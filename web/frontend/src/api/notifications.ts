import { launcherFetch } from "@/api/http"

const BASE = "/api/notifications"
const VIEWS_BASE = "/api/notification-views"
const SETTINGS_BASE = "/api/notification-settings"
const PUSH_SUBSCRIPTIONS_BASE = "/api/push-subscriptions"

export type DevelopmentNotificationIntent = "implement_feature" | "pickup_pr"

export type DevelopmentNotificationSourceKind =
  | "issue"
  | "brief"
  | "pull_request"

export type DevelopmentNotificationReason =
  | "charter_ambiguity"
  | "scope_exception"
  | "steering_scope_change"
  | "implementation_blocked"
  | "provider_outcome_unknown"
  | "publication_approval"

export type DevelopmentNotificationPriority =
  | "critical"
  | "high"
  | "medium"
  | "low"

export type DevelopmentNotificationStatus = "open" | "resolved" | "archived"

export interface DevelopmentNotificationTarget {
  panel: string
  entity_id?: string
}

export interface DevelopmentNotification {
  id: string
  source_key: string
  generation: number
  workspace_id: string
  repository: string
  intent: DevelopmentNotificationIntent
  source_kind: DevelopmentNotificationSourceKind
  phase: string
  reason: DevelopmentNotificationReason
  priority: DevelopmentNotificationPriority
  status: DevelopmentNotificationStatus
  read: boolean
  snoozed_until?: string
  title: string
  summary: string
  target: DevelopmentNotificationTarget
  version: number
  created_at: string
  updated_at: string
  resolved_at?: string
  archived_at?: string
}

export interface DevelopmentNotificationCounts {
  open: number
  unread: number
  snoozed: number
}

export interface DevelopmentNotificationPage {
  notifications: DevelopmentNotification[]
  next_cursor?: string
  total?: number
  counts?: DevelopmentNotificationCounts
}

export interface DevelopmentNotificationNeighbors {
  previous_id?: string
  next_id?: string
}

export type DevelopmentNotificationBulkAction =
  | "mark_read"
  | "mark_unread"
  | "snooze"
  | "clear_snooze"
  | "archive"

export interface DevelopmentNotificationBulkInput {
  action: DevelopmentNotificationBulkAction
  items: Array<{ id: string; expected_version: number }>
  request_id: string
  snoozed_until?: string
}

export interface NotificationSavedView {
  id: string
  name: string
  query: string
  pinned: boolean
  default: boolean
  position: number
  version: number
  created_at: string
  updated_at: string
}

export interface NotificationSavedViewsDocument {
  views: NotificationSavedView[]
  version: number
}

export interface NotificationSavedViewDraft {
  id?: string
  name: string
  query: string
  pinned: boolean
  default: boolean
  position: number
}

export interface NotificationSettings {
  include_repository_in_push: boolean
  vapid_public_key?: string
  version: number
}

export interface PushSubscriptionDevice {
  id: string
  name: string
  enabled: boolean
  version: number
  created_at: string
  updated_at: string
  last_delivered_at?: string
}

export interface PushSubscriptionPage {
  subscriptions: PushSubscriptionDevice[]
}

export interface BrowserPushSubscriptionInput {
  endpoint: string
  expiration_time?: number
  keys: {
    auth: string
    p256dh: string
  }
}

export class NotificationAPIError extends Error {
  readonly status: number
  readonly code?: string
  readonly position?: number

  constructor(
    status: number,
    message: string,
    code?: string,
    position?: number,
  ) {
    super(message)
    this.name = "NotificationAPIError"
    this.status = status
    this.code = code
    this.position = position
  }
}

async function request<T>(
  path: string,
  options?: RequestInit,
  signal?: AbortSignal,
): Promise<T> {
  const response = await launcherFetch(path, { ...options, signal })
  if (!response.ok) {
    const detail = await response.text().catch(() => "")
    let message = detail
    let code: string | undefined
    let position: number | undefined
    if (detail) {
      try {
        const parsed = JSON.parse(detail) as {
          code?: unknown
          message?: unknown
          position?: unknown
        }
        if (typeof parsed.message === "string") message = parsed.message
        if (typeof parsed.code === "string") code = parsed.code
        if (
          typeof parsed.position === "number" &&
          Number.isSafeInteger(parsed.position) &&
          parsed.position >= 0
        ) {
          position = parsed.position
        }
      } catch {
        // Preserve non-JSON server detail.
      }
    }
    throw new NotificationAPIError(
      response.status,
      message || `API error: ${response.status}`,
      code,
      position,
    )
  }
  if (response.status === 204) return undefined as T
  return response.json() as Promise<T>
}

function json(method: string, body: unknown): RequestInit {
  return {
    method,
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  }
}

export function createNotificationRequestID(): string {
  if (typeof globalThis.crypto?.randomUUID === "function") {
    return globalThis.crypto.randomUUID()
  }
  return `notification-${Date.now()}-${Math.random().toString(16).slice(2)}`
}

export async function listDevelopmentNotifications(
  input: { query?: string; cursor?: string; limit?: number } = {},
  signal?: AbortSignal,
): Promise<DevelopmentNotificationPage> {
  const params = new URLSearchParams()
  if (input.query) params.set("query", input.query)
  if (input.cursor) params.set("cursor", input.cursor)
  if (input.limit) params.set("limit", String(input.limit))
  const suffix = params.size > 0 ? `?${params.toString()}` : ""
  return request<DevelopmentNotificationPage>(
    `${BASE}${suffix}`,
    undefined,
    signal,
  )
}

export async function getDevelopmentNotification(
  notificationID: string,
  signal?: AbortSignal,
): Promise<DevelopmentNotification> {
  const result = await request<
    DevelopmentNotification | { notification: DevelopmentNotification }
  >(`${BASE}/${encodeURIComponent(notificationID)}`, undefined, signal)
  return "notification" in result ? result.notification : result
}

export async function getDevelopmentNotificationNeighbors(
  notificationID: string,
  query: string,
  signal?: AbortSignal,
): Promise<DevelopmentNotificationNeighbors> {
  const params = new URLSearchParams({ query })
  return request<DevelopmentNotificationNeighbors>(
    `${BASE}/${encodeURIComponent(notificationID)}/neighbors?${params.toString()}`,
    undefined,
    signal,
  )
}

export async function mutateDevelopmentNotifications(
  input: DevelopmentNotificationBulkInput,
  signal?: AbortSignal,
): Promise<{ notifications: DevelopmentNotification[] }> {
  return request<{ notifications: DevelopmentNotification[] }>(
    `${BASE}/bulk`,
    json("POST", input),
    signal,
  )
}

export async function getNotificationSavedViews(
  signal?: AbortSignal,
): Promise<NotificationSavedViewsDocument> {
  return request<NotificationSavedViewsDocument>(VIEWS_BASE, undefined, signal)
}

export async function updateNotificationSavedViews(
  input: {
    views: NotificationSavedViewDraft[]
    expected_version: number
    request_id: string
  },
  signal?: AbortSignal,
): Promise<NotificationSavedViewsDocument> {
  return request<NotificationSavedViewsDocument>(
    VIEWS_BASE,
    json("PUT", input),
    signal,
  )
}

export async function getNotificationSettings(
  signal?: AbortSignal,
): Promise<NotificationSettings> {
  return request<NotificationSettings>(SETTINGS_BASE, undefined, signal)
}

export async function updateNotificationSettings(
  input: {
    include_repository_in_push: boolean
    expected_version: number
    request_id: string
  },
  signal?: AbortSignal,
): Promise<NotificationSettings> {
  return request<NotificationSettings>(
    SETTINGS_BASE,
    json("PUT", input),
    signal,
  )
}

export async function listPushSubscriptionDevices(
  signal?: AbortSignal,
): Promise<PushSubscriptionPage> {
  return request<PushSubscriptionPage>(
    PUSH_SUBSCRIPTIONS_BASE,
    undefined,
    signal,
  )
}

export async function createPushSubscriptionDevice(
  input: BrowserPushSubscriptionInput & {
    name: string
    request_id: string
  },
  signal?: AbortSignal,
): Promise<PushSubscriptionDevice> {
  return request<PushSubscriptionDevice>(
    PUSH_SUBSCRIPTIONS_BASE,
    json("POST", input),
    signal,
  )
}

export async function updatePushSubscriptionDevice(
  subscriptionID: string,
  input: {
    name: string
    enabled: boolean
    expected_version: number
    request_id: string
  },
  signal?: AbortSignal,
): Promise<PushSubscriptionDevice> {
  return request<PushSubscriptionDevice>(
    `${PUSH_SUBSCRIPTIONS_BASE}/${encodeURIComponent(subscriptionID)}`,
    json("PUT", input),
    signal,
  )
}

export async function deletePushSubscriptionDevice(
  subscriptionID: string,
  input: { expected_version: number; request_id: string },
  signal?: AbortSignal,
): Promise<void> {
  await request<void>(
    `${PUSH_SUBSCRIPTIONS_BASE}/${encodeURIComponent(subscriptionID)}`,
    json("DELETE", input),
    signal,
  )
}

export function openNotificationEventStream(): EventSource | undefined {
  if (typeof EventSource === "undefined") return undefined
  return new EventSource(`${BASE}/events/stream`, { withCredentials: true })
}
