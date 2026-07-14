import type { ThreadSummary } from "@/api/threads"

export interface ThreadSearchCardPayload {
  type: "picoclaw.thread_search.v1" | "picoclaw.thread_search.v2"
  query: string
  threads: ThreadSummary[]
  total: number
}

export interface ThreadProposalCardPayload {
  type: "picoclaw.thread_proposal.v1"
  query?: string
  reason?: string
  threads: ThreadSummary[]
  total: number
}

export interface ThreadSwitchCardPayload {
  type: "picoclaw.thread_switch.v1" | "picoclaw.thread_switch.v2"
  query?: string
  auto_switch: boolean
  thread: ThreadSummary
  target_session_id?: string
  handoff?: {
    id: string
    origin_session_key?: string
    origin_session_id?: string
    target_thread_id?: string
    target_session_id?: string
  }
}

export interface ThreadReturnCardPayload {
  type: "picoclaw.thread_return.v1"
  target_session_id: string
  handoff_id: string
}

export type ThreadCardPayload =
  | ThreadSearchCardPayload
  | ThreadProposalCardPayload
  | ThreadSwitchCardPayload
  | ThreadReturnCardPayload

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
    if (
      raw.type === "picoclaw.thread_search.v1" ||
      raw.type === "picoclaw.thread_search.v2"
    ) {
      const threads = Array.isArray(raw.threads)
        ? raw.threads.filter(isThreadSummary)
        : []
      return {
        type: raw.type,
        query: typeof raw.query === "string" ? raw.query : "",
        threads,
        total: typeof raw.total === "number" ? raw.total : threads.length,
      }
    }
    if (raw.type === "picoclaw.thread_proposal.v1") {
      const threads = Array.isArray(raw.threads)
        ? raw.threads.filter(isThreadSummary)
        : []
      return {
        type: "picoclaw.thread_proposal.v1",
        query: typeof raw.query === "string" ? raw.query : undefined,
        reason: typeof raw.reason === "string" ? raw.reason : undefined,
        threads,
        total: typeof raw.total === "number" ? raw.total : threads.length,
      }
    }
    if (
      (raw.type === "picoclaw.thread_switch.v1" ||
        raw.type === "picoclaw.thread_switch.v2") &&
      isThreadSummary(raw.thread)
    ) {
      return {
        type: raw.type,
        query: typeof raw.query === "string" ? raw.query : undefined,
        auto_switch: raw.auto_switch === true,
        thread: raw.thread,
        target_session_id:
          typeof raw.target_session_id === "string"
            ? raw.target_session_id
            : undefined,
        handoff:
          raw.handoff && typeof raw.handoff === "object"
            ? (raw.handoff as ThreadSwitchCardPayload["handoff"])
            : undefined,
      }
    }
    if (
      raw.type === "picoclaw.thread_return.v1" &&
      typeof raw.target_session_id === "string" &&
      typeof raw.handoff_id === "string"
    ) {
      return {
        type: "picoclaw.thread_return.v1",
        target_session_id: raw.target_session_id,
        handoff_id: raw.handoff_id,
      }
    }
  } catch {
    return null
  }

  return null
}
