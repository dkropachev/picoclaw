import { launcherFetch } from "@/api/http"

export type ThreadType = "general" | "coding" | "reviewing" | "investigating"

export interface ThreadSummary {
  id: string
  ui_session_id?: string
  session_key?: string
  primary_session_key?: string
  agent_id?: string
  owner_identity?: string
  title: string
  preview: string
  type: ThreadType
  context?: Record<string, string>
  message_count: number
  created: string
  updated: string
  source_query?: string
  discoverable?: boolean
  dropped_at?: string
  score?: number
}

export interface CreateThreadInput {
  id?: string
  type?: ThreadType
  title?: string
  context?: Record<string, string>
  source_query?: string
  discoverable?: boolean
}

export async function getThreads({
  query = "",
  type = "",
  context,
  offset = 0,
  limit = 50,
  includeDropped = false,
}: {
  query?: string
  type?: ThreadType | ""
  context?: Record<string, string>
  offset?: number
  limit?: number
  includeDropped?: boolean
} = {}): Promise<ThreadSummary[]> {
  const params = new URLSearchParams({
    offset: offset.toString(),
    limit: limit.toString(),
  })
  if (query.trim()) {
    params.set("query", query.trim())
  }
  if (type) {
    params.set("type", type)
  }
  const contextFilter = Object.entries(context ?? {})
    .map(([key, value]) => [key.trim().toLowerCase(), value.trim()] as const)
    .filter(([key, value]) => key && value)
    .sort(([left], [right]) => left.localeCompare(right))
  if (contextFilter.length > 0) {
    params.set(
      "context",
      contextFilter.map(([key, value]) => `${key}:${value}`).join(","),
    )
  }
  if (includeDropped) {
    params.set("include_dropped", "true")
  }

  const res = await launcherFetch(`/api/threads?${params.toString()}`)
  if (!res.ok) {
    throw new Error(`Failed to fetch threads: ${res.status}`)
  }
  return res.json()
}

export async function getThread(id: string): Promise<ThreadSummary | null> {
  const normalizedID = id.trim()
  if (!normalizedID) {
    return null
  }

  const res = await launcherFetch(
    `/api/threads/${encodeURIComponent(normalizedID)}`,
  )
  if (res.status === 404) {
    return null
  }
  if (!res.ok) {
    throw new Error(`Failed to fetch thread ${normalizedID}: ${res.status}`)
  }
  return res.json()
}

export async function createThread(
  input: CreateThreadInput = {},
): Promise<ThreadSummary> {
  const res = await launcherFetch("/api/threads", {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
    },
    body: JSON.stringify(input),
  })
  if (!res.ok) {
    throw new Error(`Failed to create thread: ${res.status}`)
  }
  return res.json()
}

export async function dropThread(id: string): Promise<ThreadSummary> {
  const normalizedID = id.trim()
  if (!normalizedID) {
    throw new Error("Thread id is required")
  }

  const res = await launcherFetch(
    `/api/threads/${encodeURIComponent(normalizedID)}`,
    {
      method: "DELETE",
    },
  )
  if (!res.ok) {
    throw new Error(`Failed to drop thread ${normalizedID}: ${res.status}`)
  }
  return res.json()
}
