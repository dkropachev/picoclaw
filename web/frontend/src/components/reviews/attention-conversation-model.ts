import type { ExactJSONValue } from "@/api/review-attention-json"

export type AttentionConversationStatus =
  | "none"
  | "queued"
  | "processing"
  | "checking"
  | "waiting"
  | "continuing"
  | "recovery_required"
  | "completed"
  | "not_required"
  | "failed"

export type AttentionConversationTurnStatus =
  | "answered"
  | "waiting"
  | "continuing"
  | "recovery_required"
  | "canceled"

export interface AttentionConversationTurn {
  status: AttentionConversationTurnStatus
  title: string
  questions: ExactJSONValue
  response?: string
  response_token?: string
}

export interface AttentionConversationProjection {
  case_version: number
  status: AttentionConversationStatus
  can_respond: boolean
  turns: AttentionConversationTurn[]
}

export const ATTENTION_ACTIVE_POLL_INTERVAL_MS = 1_500
export const ATTENTION_STABLE_POLL_INTERVAL_MS = 5_000

export function attentionProjectionPolls(
  projection?: AttentionConversationProjection,
): boolean {
  const status = projection?.status
  return (
    status === "queued" ||
    status === "processing" ||
    status === "checking" ||
    status === "continuing" ||
    (status === "waiting" && projection?.can_respond === false) ||
    (status === "recovery_required" &&
      projection?.can_respond === false &&
      projection.turns.length > 0)
  )
}

// Stable projections still refresh at a lower rate so a selected case can
// discover a newly emitted occurrence or retire a question superseded by a
// later review without relying on a private runtime event in the browser.
export function attentionProjectionPollInterval(
  projection?: AttentionConversationProjection,
): number {
  return attentionProjectionPolls(projection)
    ? ATTENTION_ACTIVE_POLL_INTERVAL_MS
    : ATTENTION_STABLE_POLL_INTERVAL_MS
}

export function attentionProjectionIsVisible(
  projection?: AttentionConversationProjection,
): boolean {
  return Boolean(
    projection &&
    (projection.turns.length > 0 ||
      (projection.status !== "none" && projection.status !== "not_required")),
  )
}

export function findActionableAttentionTurn(
  projection?: AttentionConversationProjection,
): AttentionConversationTurn | undefined {
  if (!projection?.can_respond) return undefined
  return [...projection.turns]
    .reverse()
    .find(
      (turn) =>
        Boolean(turn.response_token) &&
        (turn.status === "waiting" || turn.status === "recovery_required"),
    )
}

export function attentionProjectionContainsResponse(
  projection: AttentionConversationProjection | undefined,
  turnIndex: number,
  response: string,
  originalStatus: AttentionConversationTurnStatus,
): boolean {
  const turn = projection?.turns[turnIndex]
  return (
    turn?.response === response &&
    (originalStatus === "waiting" || turn.status !== "recovery_required")
  )
}
