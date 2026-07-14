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
  score?: number
}

export interface CreateThreadInput {
  id?: string
  type?: ThreadType
  title?: string
  context?: Record<string, string>
  source_query?: string
}

export async function getThreads({
  query = "",
  type = "",
  offset = 0,
  limit = 50,
}: {
  query?: string
  type?: ThreadType | ""
  offset?: number
  limit?: number
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

  const res = await launcherFetch(`/api/threads?${params.toString()}`)
  if (!res.ok) {
    throw new Error(`Failed to fetch threads: ${res.status}`)
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
