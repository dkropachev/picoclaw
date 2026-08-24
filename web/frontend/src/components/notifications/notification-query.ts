import type {
  DevelopmentNotificationPriority,
  DevelopmentNotificationStatus,
} from "@/api/notifications"

export const maximumNotificationQueryLength = 4096

export type NotificationSort = "priority" | "updated" | "created" | "repository"

export interface NotificationRouteSearch {
  query?: string
}

export interface NotificationSimpleFilters {
  statuses: DevelopmentNotificationStatus[]
  priorities: DevelopmentNotificationPriority[]
  repository: string
  text: string
  unreadOnly: boolean
  excludeSnoozed: boolean
  sort: NotificationSort
}

export interface NotificationBuiltInView {
  id: "needs-action" | "unread" | "snoozed" | "resolved" | "all"
  name: string
  query: string
}

export const notificationBuiltInViews: NotificationBuiltInView[] = [
  {
    id: "needs-action",
    name: "Needs action",
    query:
      "status = open AND snoozed = false ORDER BY priority DESC, updated DESC",
  },
  {
    id: "unread",
    name: "Unread",
    query:
      "status = open AND read = false ORDER BY priority DESC, updated DESC",
  },
  {
    id: "snoozed",
    name: "Snoozed",
    query: "status = open AND snoozed = true ORDER BY updated DESC",
  },
  {
    id: "resolved",
    name: "Resolved",
    query: "status = resolved ORDER BY updated DESC",
  },
  { id: "all", name: "All", query: "ORDER BY updated DESC" },
]

export const defaultNotificationQuery = notificationBuiltInViews[0].query

export const notificationQuerySuggestions = [
  "status = open",
  "read = false",
  "snoozed = false",
  "priority IN (critical, high)",
  "reason = scope_exception",
  "intent = implement_feature",
  'repository ~ "owner/"',
  "created >= -7d",
] as const

export function normalizeNotificationRouteSearch(
  raw: Record<string, unknown>,
): NotificationRouteSearch {
  const query = typeof raw.query === "string" ? raw.query.trim() : ""
  return query ? { query: truncateNotificationQuery(query) } : {}
}

export function notificationQueryByteLength(value: string): number {
  return new TextEncoder().encode(value).byteLength
}

export function truncateNotificationQuery(value: string): string {
  if (notificationQueryByteLength(value) <= maximumNotificationQueryLength) {
    return value
  }
  let bytes = 0
  let result = ""
  for (const character of value) {
    const characterBytes = notificationQueryByteLength(character)
    if (bytes + characterBytes > maximumNotificationQueryLength) break
    bytes += characterBytes
    result += character
  }
  return result
}

export function notificationInboxHref(
  query: string,
  notificationID?: string,
): string {
  const base = notificationID
    ? `/notifications/${encodeURIComponent(notificationID)}`
    : "/notifications"
  const normalized = query.trim()
  if (!normalized) return base
  return `${base}?${new URLSearchParams({ query: normalized }).toString()}`
}

export function buildNotificationSimpleQuery(
  filters: NotificationSimpleFilters,
): string {
  const predicates: string[] = []
  if (filters.statuses.length === 1) {
    predicates.push(`status = ${filters.statuses[0]}`)
  } else if (filters.statuses.length > 1) {
    predicates.push(`status IN (${filters.statuses.join(", ")})`)
  }
  if (filters.priorities.length === 1) {
    predicates.push(`priority = ${filters.priorities[0]}`)
  } else if (filters.priorities.length > 1) {
    predicates.push(`priority IN (${filters.priorities.join(", ")})`)
  }
  if (filters.repository.trim()) {
    predicates.push(
      `repository ~ "${escapeNotificationQueryString(filters.repository.trim())}"`,
    )
  }
  if (filters.text.trim()) {
    predicates.push(
      `text ~ "${escapeNotificationQueryString(filters.text.trim())}"`,
    )
  }
  if (filters.unreadOnly) predicates.push("read = false")
  if (filters.excludeSnoozed) predicates.push("snoozed = false")
  const order = notificationSortClause(filters.sort)
  return `${predicates.join(" AND ")}${predicates.length ? " " : ""}${order}`
}

function escapeNotificationQueryString(value: string): string {
  return value.replace(/\\/g, "\\\\").replace(/"/g, '\\"')
}

export function withNotificationSort(
  query: string,
  sort: NotificationSort,
): string {
  const predicate = withoutTopLevelOrderBy(query).trim()
  return `${predicate}${predicate ? " " : ""}${notificationSortClause(sort)}`
}

export function insertNotificationQuerySuggestion(
  query: string,
  suggestion: string,
): string {
  const orderIndex = topLevelOrderByIndex(query)
  const predicate = (
    orderIndex >= 0 ? query.slice(0, orderIndex) : query
  ).trim()
  const order = orderIndex >= 0 ? query.slice(orderIndex).trim() : ""
  const nextPredicate = `${predicate}${predicate ? " AND " : ""}${suggestion}`
  return `${nextPredicate}${order ? ` ${order}` : ""}`
}

export function notificationSortClause(sort: NotificationSort): string {
  switch (sort) {
    case "priority":
      return "ORDER BY priority DESC, updated DESC"
    case "created":
      return "ORDER BY created DESC"
    case "repository":
      return "ORDER BY repository ASC, updated DESC"
    case "updated":
      return "ORDER BY updated DESC"
  }
}

function withoutTopLevelOrderBy(query: string): string {
  const index = topLevelOrderByIndex(query)
  return index >= 0 ? query.slice(0, index) : query
}

function topLevelOrderByIndex(query: string): number {
  let quote = ""
  let depth = 0
  for (let index = 0; index < query.length; index += 1) {
    const value = query[index] ?? ""
    if (quote) {
      if (value === "\\") index += 1
      else if (value === quote) quote = ""
      continue
    }
    if (value === '"' || value === "'") {
      quote = value
      continue
    }
    if (value === "(") depth += 1
    if (value === ")") depth = Math.max(0, depth - 1)
    if (
      depth === 0 &&
      query
        .slice(index)
        .toUpperCase()
        .match(/^ORDER\s+BY\b/)
    ) {
      return index
    }
  }
  return -1
}
