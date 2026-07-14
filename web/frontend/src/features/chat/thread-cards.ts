import type { ThreadSummary } from "@/api/threads"

export interface ThreadSearchCardPayload {
  type: "picoclaw.thread_search.v1"
  query: string
  threads: ThreadSummary[]
  total: number
}

export interface ThreadSwitchCardPayload {
  type: "picoclaw.thread_switch.v1"
  query?: string
  auto_switch: boolean
  thread: ThreadSummary
}

export type ThreadCardPayload =
  | ThreadSearchCardPayload
  | ThreadSwitchCardPayload

function isThreadSummary(value: unknown): value is ThreadSummary {
  if (!value || typeof value !== "object") {
    return false
  }
  const item = value as Record<string, unknown>
  return (
    typeof item.id === "string" &&
    typeof item.title === "string" &&
    typeof item.preview === "string" &&
    typeof item.type === "string" &&
    typeof item.message_count === "number" &&
    typeof item.created === "string" &&
    typeof item.updated === "string"
  )
}

export function parseThreadCardPayload(
  content: string,
): ThreadCardPayload | null {
  const trimmed = content.trim()
  if (!trimmed.startsWith("{") || !trimmed.endsWith("}")) {
    return null
  }

  try {
    const raw = JSON.parse(trimmed) as Record<string, unknown>
    if (raw.type === "picoclaw.thread_search.v1") {
      const threads = Array.isArray(raw.threads)
        ? raw.threads.filter(isThreadSummary)
        : []
      return {
        type: "picoclaw.thread_search.v1",
        query: typeof raw.query === "string" ? raw.query : "",
        threads,
        total: typeof raw.total === "number" ? raw.total : threads.length,
      }
    }
    if (
      raw.type === "picoclaw.thread_switch.v1" &&
      isThreadSummary(raw.thread)
    ) {
      return {
        type: "picoclaw.thread_switch.v1",
        query: typeof raw.query === "string" ? raw.query : undefined,
        auto_switch: raw.auto_switch === true,
        thread: raw.thread,
      }
    }
  } catch {
    return null
  }

  return null
}
