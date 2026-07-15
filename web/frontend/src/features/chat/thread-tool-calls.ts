import type { ThreadType } from "@/api/threads"
import type { ChatToolCall } from "@/store/chat"

const DEFAULT_THREAD_TOOL_SEARCH_LIMIT = 8
const MAX_THREAD_TOOL_SEARCH_LIMIT = 100
const THREAD_SEARCH_ACTIONS = ["find", "search"] as const
const THREAD_SWITCH_ACTIONS = [
  "attach_current",
  "create",
  "register_current",
  "switch",
  "update_metadata",
] as const
const THREAD_TYPES = [
  "general",
  "coding",
  "reviewing",
  "investigating",
] as const satisfies readonly ThreadType[]

export type ThreadToolCardMode = "proposal" | "search" | "switch"

export interface ThreadToolCardRequest {
  mode: ThreadToolCardMode
  id?: string
  query: string
  type: ThreadType | ""
  context?: Record<string, string>
  limit: number
}

function isThreadType(value: string): value is ThreadType {
  return THREAD_TYPES.includes(value as ThreadType)
}

function isThreadsToolName(name: string | undefined): boolean {
  const normalized = name?.trim().toLowerCase() ?? ""
  return (
    normalized === "threads" ||
    normalized.endsWith(".threads") ||
    normalized.endsWith("/threads") ||
    normalized.endsWith("__threads")
  )
}

function parseJSONArguments(argumentsText: string | undefined) {
  const trimmed = argumentsText?.trim() ?? ""
  if (!trimmed) {
    return null
  }
  const fenced = /^```(?:json)?\s*([\s\S]*?)\s*```$/i.exec(trimmed)
  const source = fenced?.[1]?.trim() ?? trimmed

  try {
    const parsed = JSON.parse(source) as unknown
    return parsed && typeof parsed === "object" && !Array.isArray(parsed)
      ? (parsed as Record<string, unknown>)
      : null
  } catch {
    return null
  }
}

function stringValue(value: unknown): string {
  return typeof value === "string" ? value.trim() : ""
}

function normalizeLimit(value: unknown): number {
  const parsed =
    typeof value === "number"
      ? value
      : typeof value === "string"
        ? Number.parseInt(value, 10)
        : Number.NaN
  if (!Number.isFinite(parsed) || parsed <= 0) {
    return DEFAULT_THREAD_TOOL_SEARCH_LIMIT
  }
  return Math.min(Math.trunc(parsed), MAX_THREAD_TOOL_SEARCH_LIMIT)
}

function normalizeContext(value: unknown): Record<string, string> | undefined {
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    return undefined
  }

  const context: Record<string, string> = {}
  for (const [key, rawValue] of Object.entries(value)) {
    const normalizedKey = key.trim().toLowerCase()
    const normalizedValue =
      typeof rawValue === "string" ? rawValue.trim() : String(rawValue).trim()
    if (normalizedKey && normalizedValue) {
      context[normalizedKey] = normalizedValue
    }
  }

  return Object.keys(context).length > 0 ? context : undefined
}

export function parseThreadToolSearchRequest(
  toolCalls: ChatToolCall[] | undefined,
): ThreadToolCardRequest | null {
  for (const toolCall of toolCalls ?? []) {
    if (!isThreadsToolName(toolCall.function?.name)) {
      continue
    }

    const args = parseJSONArguments(toolCall.function?.arguments)
    if (!args) {
      continue
    }

    const action = stringValue(args.action).toLowerCase()
    const requestedType = stringValue(args.type).toLowerCase()
    const query = stringValue(args.query)
    const title = stringValue(args.title)
    const baseRequest: Omit<ThreadToolCardRequest, "mode"> = {
      id: stringValue(args.id) || undefined,
      query: query || title,
      type: isThreadType(requestedType) ? requestedType : "",
      context: normalizeContext(args.context),
      limit: normalizeLimit(args.limit),
    }

    if (
      THREAD_SEARCH_ACTIONS.includes(
        action as (typeof THREAD_SEARCH_ACTIONS)[number],
      )
    ) {
      return {
        ...baseRequest,
        mode: "search",
      }
    }
    if (action === "propose_switch") {
      return {
        ...baseRequest,
        mode: "proposal",
      }
    }
    if (
      THREAD_SWITCH_ACTIONS.includes(
        action as (typeof THREAD_SWITCH_ACTIONS)[number],
      )
    ) {
      return {
        ...baseRequest,
        mode: "switch",
      }
    }
  }

  return null
}
