import { launcherFetch } from "@/api/http"
import {
  type ExactJSONObject,
  type ExactJSONValue,
  cloneExactJSON,
  isExactJSONNumber,
  isExactJSONObject,
  parseExactJSON,
  stringifyExactJSON,
  trimGoSpace,
} from "@/api/review-attention-json"

export type PRDevelopmentAttentionStatus =
  | "none"
  | "queued"
  | "checking"
  | "waiting"
  | "continuing"
  | "recovery_required"
  | "completed"
  | "not_required"
  | "failed"

export type PRDevelopmentAttentionTurnStatus =
  | "answered"
  | "waiting"
  | "continuing"
  | "recovery_required"
  | "canceled"

export interface PRDevelopmentAttentionTurn {
  status: PRDevelopmentAttentionTurnStatus
  title: string
  questions: ExactJSONValue
  response?: string
  response_token?: string
}

export interface PRDevelopmentAttentionProjection {
  case_version: number
  status: PRDevelopmentAttentionStatus
  can_respond: boolean
  turns: PRDevelopmentAttentionTurn[]
}

export class PRDevelopmentAttentionAPIError extends Error {
  readonly status: number
  readonly code: string

  constructor(code: string, status: number) {
    super(code)
    this.name = "PRDevelopmentAttentionAPIError"
    this.status = status
    this.code = code
  }
}

const responseTokenPattern = /^sha256:[0-9a-f]{64}$/
const attentionStatuses = new Set<PRDevelopmentAttentionStatus>([
  "none",
  "queued",
  "checking",
  "waiting",
  "continuing",
  "recovery_required",
  "completed",
  "not_required",
  "failed",
])
const turnStatuses = new Set<PRDevelopmentAttentionTurnStatus>([
  "answered",
  "waiting",
  "continuing",
  "recovery_required",
  "canceled",
])
const projectionKeys = new Set([
  "case_version",
  "status",
  "can_respond",
  "turns",
])
const turnKeys = new Set([
  "status",
  "title",
  "questions",
  "response",
  "response_token",
])
const maximumTurns = 64
const maximumEnvelopeBytes = 32 << 20
const maximumErrorBytes = 64 << 10
const maximumTitleBytes = 4 << 10
const maximumQuestionsBytes = 256 << 10
const maximumQuestionDepth = 64
const maximumQuestionNodes = 100_000
export const PR_DEVELOPMENT_ATTENTION_RESPONSE_MAXIMUM_BYTES = 32 << 10
const maximumEnvelopeNodes =
  maximumTurns * maximumQuestionNodes + maximumTurns * 8 + 16

export async function getPRDevelopmentAttention(
  caseID: string,
  signal?: AbortSignal,
): Promise<PRDevelopmentAttentionProjection> {
  return requestPRDevelopmentAttention(
    `/api/pr-development/${encodeURIComponent(caseID)}/attention`,
    undefined,
    signal,
  )
}

export async function respondToPRDevelopmentAttention(
  caseID: string,
  expectedCaseVersion: number,
  responseToken: string,
  response: string,
  signal?: AbortSignal,
): Promise<PRDevelopmentAttentionProjection> {
  if (!isNonnegativeSafeInteger(expectedCaseVersion)) {
    throw new PRDevelopmentAttentionAPIError("invalid_attention_response", 400)
  }
  if (!responseTokenPattern.test(responseToken)) {
    throw new PRDevelopmentAttentionAPIError("invalid_attention_response", 400)
  }
  const normalized = trimGoSpace(response)
  if (
    !isBoundedText(
      normalized,
      PR_DEVELOPMENT_ATTENTION_RESPONSE_MAXIMUM_BYTES,
    ) ||
    normalized === ""
  ) {
    throw new PRDevelopmentAttentionAPIError("invalid_attention_response", 400)
  }
  return requestPRDevelopmentAttention(
    `/api/pr-development/${encodeURIComponent(caseID)}/attention/respond`,
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        expected_case_version: expectedCaseVersion,
        response_token: responseToken,
        response: normalized,
      }),
    },
    signal,
  )
}

async function requestPRDevelopmentAttention(
  path: string,
  init?: RequestInit,
  signal?: AbortSignal,
): Promise<PRDevelopmentAttentionProjection> {
  const response = await launcherFetch(path, {
    ...init,
    ...(signal ? { signal } : {}),
  })
  const source = await response.text()
  const maximumBytes = response.ok ? maximumEnvelopeBytes : maximumErrorBytes
  if (byteLength(source) > maximumBytes) {
    throw response.ok
      ? malformedResponse()
      : new PRDevelopmentAttentionAPIError(
          "attention_unavailable",
          response.status,
        )
  }
  if (!response.ok) {
    throw parseErrorResponse(source, response.status)
  }
  try {
    return parseProjection(source)
  } catch {
    throw malformedResponse()
  }
}

function parseProjection(source: string): PRDevelopmentAttentionProjection {
  const value = parseExactJSON(source, {
    maximumBytes: maximumEnvelopeBytes,
    maximumDepth: maximumQuestionDepth + 4,
    maximumNodes: maximumEnvelopeNodes,
  })
  if (
    !isExactJSONObject(value) ||
    !onlyKeys(value, projectionKeys) ||
    !isSetMember(value.status, attentionStatuses) ||
    typeof value.can_respond !== "boolean" ||
    !Array.isArray(value.turns) ||
    value.turns.length > maximumTurns
  ) {
    throw new TypeError("invalid attention projection")
  }
  const caseVersion = exactNonnegativeSafeInteger(value.case_version)
  const turns = value.turns.map(parseTurn)
  const actionable = turns.filter(
    (turn) =>
      (turn.status === "waiting" || turn.status === "recovery_required") &&
      turn.response_token !== undefined,
  )
  if (
    caseVersion === undefined ||
    !projectionTurnHistoryIsValid(value.status, turns) ||
    (value.can_respond &&
      (actionable.length !== 1 || actionable[0].status !== value.status)) ||
    (!value.can_respond && actionable.length !== 0)
  ) {
    throw new TypeError("invalid attention response authority")
  }
  return {
    case_version: caseVersion,
    status: value.status,
    can_respond: value.can_respond,
    turns,
  }
}

function projectionTurnHistoryIsValid(
  status: PRDevelopmentAttentionStatus,
  turns: PRDevelopmentAttentionTurn[],
): boolean {
  const nonAnswered = turns
    .map((turn, index) => ({ turn, index }))
    .filter(({ turn }) => turn.status !== "answered")
  if (
    nonAnswered.length > 1 ||
    (nonAnswered.length === 1 && nonAnswered[0].index !== turns.length - 1)
  ) {
    return false
  }
  switch (status) {
    case "none":
    case "queued":
    case "checking":
    case "not_required":
      return turns.length === 0
    case "completed":
      return turns.every((turn) => turn.status === "answered")
    case "waiting":
    case "continuing":
      return turns.length > 0 && turns[turns.length - 1].status === status
    case "recovery_required":
      return turns.length === 0 || turns[turns.length - 1].status === status
    case "failed":
      return (
        turns.length === 0 ||
        turns[turns.length - 1].status === "answered" ||
        turns[turns.length - 1].status === "canceled"
      )
  }
}

function parseTurn(value: ExactJSONValue): PRDevelopmentAttentionTurn {
  if (
    !isExactJSONObject(value) ||
    !onlyKeys(value, turnKeys) ||
    !isSetMember(value.status, turnStatuses) ||
    !isBoundedText(value.title, maximumTitleBytes) ||
    value.title === "" ||
    !Object.hasOwn(value, "questions") ||
    !isOptionalBoundedText(
      value.response,
      PR_DEVELOPMENT_ATTENTION_RESPONSE_MAXIMUM_BYTES,
    ) ||
    !isOptionalResponseToken(value.response_token)
  ) {
    throw new TypeError("invalid attention turn")
  }
  stringifyExactJSON(value.questions, {
    maximumBytes: maximumQuestionsBytes,
    maximumDepth: maximumQuestionDepth,
    maximumNodes: maximumQuestionNodes,
  })
  const validState =
    (value.status === "waiting" &&
      value.response === undefined &&
      isOptionalResponseToken(value.response_token)) ||
    ((value.status === "answered" || value.status === "continuing") &&
      value.response !== undefined &&
      value.response_token === undefined) ||
    (value.status === "recovery_required" &&
      value.response !== undefined &&
      isOptionalResponseToken(value.response_token)) ||
    (value.status === "canceled" &&
      value.response === undefined &&
      value.response_token === undefined)
  if (!validState) {
    throw new TypeError("invalid attention turn state")
  }
  return {
    status: value.status,
    title: value.title,
    questions: cloneExactJSON(value.questions),
    ...(value.response === undefined ? {} : { response: value.response }),
    ...(value.response_token === undefined
      ? {}
      : { response_token: value.response_token }),
  }
}

function parseErrorResponse(
  source: string,
  status: number,
): PRDevelopmentAttentionAPIError {
  try {
    const value = parseExactJSON(source, {
      maximumBytes: maximumErrorBytes,
      maximumDepth: 4,
      maximumNodes: 16,
    })
    if (
      isExactJSONObject(value) &&
      onlyKeys(value, new Set(["error"])) &&
      isBoundedText(value.error, 256) &&
      value.error !== ""
    ) {
      return new PRDevelopmentAttentionAPIError(value.error, status)
    }
  } catch {
    // Return one fixed safe error below.
  }
  return new PRDevelopmentAttentionAPIError("attention_unavailable", status)
}

function malformedResponse(): PRDevelopmentAttentionAPIError {
  return new PRDevelopmentAttentionAPIError("invalid_attention_response", 502)
}

function exactNonnegativeSafeInteger(
  value: ExactJSONValue | undefined,
): number | undefined {
  if (!isExactJSONNumber(value) || !/^(?:0|[1-9]\d*)$/.test(value.source)) {
    return undefined
  }
  const parsed = Number(value.source)
  return isNonnegativeSafeInteger(parsed) ? parsed : undefined
}

function isNonnegativeSafeInteger(value: number): boolean {
  return Number.isSafeInteger(value) && value >= 0
}

function isOptionalResponseToken(
  value: ExactJSONValue | undefined,
): value is string | undefined {
  return (
    value === undefined ||
    (typeof value === "string" && responseTokenPattern.test(value))
  )
}

function isOptionalBoundedText(
  value: ExactJSONValue | undefined,
  maximumBytes: number,
): value is string | undefined {
  return value === undefined || isBoundedText(value, maximumBytes)
}

function isBoundedText(
  value: ExactJSONValue | string,
  maximumBytes: number,
): value is string {
  return (
    typeof value === "string" &&
    value === trimGoSpace(value) &&
    !value.includes("\0") &&
    byteLength(value) <= maximumBytes
  )
}

function onlyKeys(
  value: ExactJSONObject,
  allowed: ReadonlySet<string>,
): boolean {
  return Object.keys(value).every((key) => allowed.has(key))
}

function isSetMember<T extends string>(
  value: ExactJSONValue | undefined,
  values: ReadonlySet<T>,
): value is T {
  return typeof value === "string" && values.has(value as T)
}

function byteLength(value: string): number {
  return new TextEncoder().encode(value).byteLength
}
