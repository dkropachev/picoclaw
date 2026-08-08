import { launcherFetch } from "@/api/http"

export type PRDevelopmentPullState = "open" | "closed"
export type PRDevelopmentReviewState =
  | "approved"
  | "changes_requested"
  | "commented"
  | "dismissed"
export type PRDevelopmentMessageRole = "user" | "assistant"

export interface PRDevelopmentMessage {
  id: string
  ordinal: number
  role: PRDevelopmentMessageRole
  content: string
  created_at: string
}

export interface PRDevelopmentCaseSummary {
  id: string
  repository: string
  pull_number: number
  pull_url: string
  pull_author: string
  pull_state: PRDevelopmentPullState
  pull_draft: boolean
  pull_merged: boolean
  head_repository: string
  head_ref: string
  head_sha: string
  review_author: string
  submitted_review_state: Exclude<PRDevelopmentReviewState, "dismissed">
  current_review_state: PRDevelopmentReviewState
  review_submitted_at: string
  review_url: string
  captured_at: string
}

export interface PRDevelopmentCase extends PRDevelopmentCaseSummary {
  base_repository: string
  base_ref: string
  base_sha: string
  review_commit_sha: string
  feedback: string
}

export interface PRDevelopmentCasePage {
  cases: PRDevelopmentCaseSummary[]
  next_cursor?: string
}

export interface PRDevelopmentCaseDetail {
  case: PRDevelopmentCase
  conversation_version: number
  messages: PRDevelopmentMessage[]
}

export interface PRDevelopmentListParams {
  repository?: string
  pull_number?: number
  limit?: number
  cursor?: string
}

export class PRDevelopmentAPIError extends Error {
  readonly status: number
  readonly detail?: PRDevelopmentCaseDetail

  constructor(
    message: string,
    status: number,
    detail?: PRDevelopmentCaseDetail,
  ) {
    super(message)
    this.name = "PRDevelopmentAPIError"
    this.status = status
    this.detail = detail
  }
}

const MALFORMED_RESPONSE_MESSAGE =
  "The PR development service returned a malformed response."
const CASE_ID_PATTERN = /^pdc_[0-9a-f]{32}$/
const MESSAGE_ID_PATTERN = /^pdm_[0-9a-f]{32}$/
const GIT_OID_PATTERN = /^(?:[0-9a-f]{40}|[0-9a-f]{64})$/
const REPOSITORY_PATTERN = /^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/
const RFC3339_PATTERN =
  /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.\d{1,9})?(?:Z|([+-])(\d{2}):(\d{2}))$/
const MAXIMUM_CASES = 100
const MAXIMUM_CURSOR_BYTES = 1024
const MAXIMUM_PULL_NUMBER = 2_147_483_647
const MAXIMUM_REPOSITORY_BYTES = 256
const MAXIMUM_LOGIN_BYTES = 128
const MAXIMUM_REF_BYTES = 1024
const MAXIMUM_URL_BYTES = 4096
const MAXIMUM_FEEDBACK_BYTES = 64 << 10
export const MAXIMUM_PR_DEVELOPMENT_CHAT_BYTES = 32 << 10
const MAXIMUM_MESSAGE_BYTES = 64 << 10
const MAXIMUM_MESSAGES = 256
const MAXIMUM_TRANSCRIPT_BYTES = 4 << 20
// Safe conflict/model-failure responses may carry the complete transcript so
// the UI can recover authoritative state. Match the launcher's bounded JSON
// response ceiling, including JSON-escaping headroom.
const MAXIMUM_ERROR_BYTES = 32 << 20
const GO_LEADING_SPACE = /^\p{White_Space}+/u
const GO_TRAILING_SPACE = /\p{White_Space}+$/u

// Match Go strings.TrimSpace exactly so client success binding compares the
// canonical content actually committed by the runtime.
export function normalizePRDevelopmentChatContent(value: string): string {
  return value.replace(GO_LEADING_SPACE, "").replace(GO_TRAILING_SPACE, "")
}

const pullStates = new Set<PRDevelopmentPullState>(["open", "closed"])
const submittedReviewStates = new Set<
  Exclude<PRDevelopmentReviewState, "dismissed">
>(["approved", "changes_requested", "commented"])
const currentReviewStates = new Set<PRDevelopmentReviewState>([
  "approved",
  "changes_requested",
  "commented",
  "dismissed",
])
const messageRoles = new Set<PRDevelopmentMessageRole>(["user", "assistant"])
const summaryKeys = new Set([
  "id",
  "repository",
  "pull_number",
  "pull_url",
  "pull_author",
  "pull_state",
  "pull_draft",
  "pull_merged",
  "head_repository",
  "head_ref",
  "head_sha",
  "review_author",
  "submitted_review_state",
  "current_review_state",
  "review_submitted_at",
  "review_url",
  "captured_at",
])
const detailCaseKeys = new Set([
  ...summaryKeys,
  "base_repository",
  "base_ref",
  "base_sha",
  "review_commit_sha",
  "feedback",
])
const detailKeys = new Set(["case", "conversation_version", "messages"])
const messageKeys = new Set(["id", "ordinal", "role", "content", "created_at"])

export async function listPRDevelopmentCases(
  input: PRDevelopmentListParams = {},
): Promise<PRDevelopmentCasePage> {
  const params = new URLSearchParams()
  setOptionalParam(params, "repository", input.repository)
  setOptionalParam(params, "pull_number", input.pull_number)
  setOptionalParam(params, "limit", input.limit)
  setOptionalParam(params, "cursor", input.cursor)
  const query = params.toString()
  const body = await responseBody(
    await launcherFetch(
      query === "" ? "/api/pr-development" : `/api/pr-development?${query}`,
      undefined,
    ),
  )
  if (
    !isRecord(body) ||
    !onlyKeys(body, new Set(["cases", "next_cursor"])) ||
    !Array.isArray(body.cases) ||
    body.cases.length > MAXIMUM_CASES ||
    !isOptionalBoundedText(body.next_cursor, MAXIMUM_CURSOR_BYTES)
  ) {
    throw malformedResponse()
  }
  return {
    cases: body.cases.map(parseSummary),
    ...(body.next_cursor === undefined
      ? {}
      : { next_cursor: body.next_cursor }),
  }
}

export async function getPRDevelopmentCase(
  caseID: string,
): Promise<PRDevelopmentCaseDetail> {
  if (!CASE_ID_PATTERN.test(caseID)) {
    throw new PRDevelopmentAPIError("Invalid development case.", 400)
  }
  const body = await responseBody(
    await launcherFetch(
      `/api/pr-development/${encodeURIComponent(caseID)}`,
      undefined,
    ),
  )
  return parseDetail(body, { caseID })
}

export async function chatAboutPRDevelopmentCase(
  caseID: string,
  expectedVersion: number,
  content: string,
): Promise<PRDevelopmentCaseDetail> {
  const normalizedContent = normalizePRDevelopmentChatContent(content)
  if (
    !CASE_ID_PATTERN.test(caseID) ||
    !Number.isSafeInteger(expectedVersion) ||
    expectedVersion < 0 ||
    expectedVersion > MAXIMUM_MESSAGES ||
    !isBoundedCanonicalText(
      normalizedContent,
      MAXIMUM_PR_DEVELOPMENT_CHAT_BYTES,
    )
  ) {
    throw new PRDevelopmentAPIError("Invalid development chat message.", 400)
  }
  const body = await responseBody(
    await launcherFetch(
      `/api/pr-development/${encodeURIComponent(caseID)}/chat`,
      {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          expected_version: expectedVersion,
          content: normalizedContent,
        }),
      },
    ),
    { caseID, minimumVersion: expectedVersion },
  )
  const detail = parseDetail(body, { caseID })
  const userMessage = detail.messages[expectedVersion]
  const assistantMessage = detail.messages[expectedVersion + 1]
  if (
    detail.conversation_version !== expectedVersion + 2 ||
    userMessage?.role !== "user" ||
    userMessage.content !== normalizedContent ||
    assistantMessage?.role !== "assistant"
  ) {
    throw malformedResponse()
  }
  return detail
}

function parseSummary(value: unknown): PRDevelopmentCaseSummary {
  if (!isRecord(value) || !onlyKeys(value, summaryKeys)) {
    throw malformedResponse()
  }
  return parseSummaryRecord(value)
}

function parseSummaryRecord(
  value: Record<string, unknown>,
): PRDevelopmentCaseSummary {
  if (
    !isPattern(value.id, CASE_ID_PATTERN) ||
    !isRepository(value.repository) ||
    !isPullNumber(value.pull_number) ||
    !isAbsoluteHTTPSURL(value.pull_url) ||
    !isLogin(value.pull_author) ||
    !isSetMember(value.pull_state, pullStates) ||
    typeof value.pull_draft !== "boolean" ||
    typeof value.pull_merged !== "boolean" ||
    !isRepository(value.head_repository) ||
    !isBoundedCanonicalText(value.head_ref, MAXIMUM_REF_BYTES) ||
    !isPattern(value.head_sha, GIT_OID_PATTERN) ||
    !isLogin(value.review_author) ||
    !isSetMember(value.submitted_review_state, submittedReviewStates) ||
    !isSetMember(value.current_review_state, currentReviewStates) ||
    (value.current_review_state !== value.submitted_review_state &&
      value.current_review_state !== "dismissed") ||
    !isTimestamp(value.review_submitted_at) ||
    !isAbsoluteHTTPSURL(value.review_url) ||
    !isTimestamp(value.captured_at) ||
    (value.pull_merged && (value.pull_state !== "closed" || value.pull_draft))
  ) {
    throw malformedResponse()
  }
  return {
    id: value.id,
    repository: value.repository,
    pull_number: value.pull_number,
    pull_url: value.pull_url,
    pull_author: value.pull_author,
    pull_state: value.pull_state,
    pull_draft: value.pull_draft,
    pull_merged: value.pull_merged,
    head_repository: value.head_repository,
    head_ref: value.head_ref,
    head_sha: value.head_sha,
    review_author: value.review_author,
    submitted_review_state: value.submitted_review_state,
    current_review_state: value.current_review_state,
    review_submitted_at: value.review_submitted_at,
    review_url: value.review_url,
    captured_at: value.captured_at,
  }
}

function parseCase(value: unknown): PRDevelopmentCase {
  if (!isRecord(value) || !onlyKeys(value, detailCaseKeys)) {
    throw malformedResponse()
  }
  const summary = parseSummaryRecord(value)
  if (
    !isRepository(value.base_repository) ||
    value.base_repository.toLowerCase() !== summary.repository.toLowerCase() ||
    !isBoundedCanonicalText(value.base_ref, MAXIMUM_REF_BYTES) ||
    !isPattern(value.base_sha, GIT_OID_PATTERN) ||
    !isPattern(value.review_commit_sha, GIT_OID_PATTERN) ||
    !isFeedback(value.feedback)
  ) {
    throw malformedResponse()
  }
  return {
    ...summary,
    base_repository: value.base_repository,
    base_ref: value.base_ref,
    base_sha: value.base_sha,
    review_commit_sha: value.review_commit_sha,
    feedback: value.feedback,
  }
}

function parseDetail(
  value: unknown,
  binding?: { caseID: string; minimumVersion?: number },
): PRDevelopmentCaseDetail {
  if (
    !isRecord(value) ||
    !onlyKeys(value, detailKeys) ||
    !Number.isSafeInteger(value.conversation_version) ||
    (value.conversation_version as number) < 0 ||
    (value.conversation_version as number) > MAXIMUM_MESSAGES
  ) {
    throw malformedResponse()
  }
  const conversationVersion = value.conversation_version as number
  const developmentCase = parseCase(value.case)
  if (
    binding !== undefined &&
    (developmentCase.id !== binding.caseID ||
      (binding.minimumVersion !== undefined &&
        conversationVersion < binding.minimumVersion))
  ) {
    throw malformedResponse()
  }
  return {
    case: developmentCase,
    conversation_version: conversationVersion,
    messages: parseMessages(value.messages, conversationVersion),
  }
}

function parseMessages(
  value: unknown,
  conversationVersion: unknown,
): PRDevelopmentMessage[] {
  if (
    !Array.isArray(value) ||
    value.length > MAXIMUM_MESSAGES ||
    conversationVersion !== value.length
  ) {
    throw malformedResponse()
  }
  const ids = new Set<string>()
  let transcriptBytes = 0
  return value.map((message, ordinal) => {
    if (
      !isRecord(message) ||
      !onlyKeys(message, messageKeys) ||
      !isPattern(message.id, MESSAGE_ID_PATTERN) ||
      ids.has(message.id) ||
      message.ordinal !== ordinal ||
      !isSetMember(message.role, messageRoles) ||
      !isBoundedCanonicalText(message.content, MAXIMUM_MESSAGE_BYTES) ||
      !isTimestamp(message.created_at)
    ) {
      throw malformedResponse()
    }
    ids.add(message.id)
    transcriptBytes += byteLength(message.content)
    if (transcriptBytes > MAXIMUM_TRANSCRIPT_BYTES) {
      throw malformedResponse()
    }
    return {
      id: message.id,
      ordinal,
      role: message.role,
      content: message.content,
      created_at: message.created_at,
    }
  })
}

async function responseBody(
  response: Response,
  errorBinding?: { caseID: string; minimumVersion?: number },
): Promise<unknown> {
  if (!response.ok) {
    throw await errorFromResponse(response, errorBinding)
  }
  try {
    return (await response.json()) as unknown
  } catch {
    throw malformedResponse()
  }
}

async function errorFromResponse(
  response: Response,
  binding?: { caseID: string; minimumVersion?: number },
): Promise<PRDevelopmentAPIError> {
  let body: unknown
  try {
    const source = await response.text()
    if (byteLength(source) > MAXIMUM_ERROR_BYTES) {
      return new PRDevelopmentAPIError(
        "PR development is unavailable.",
        response.status,
      )
    }
    body = JSON.parse(source) as unknown
  } catch {
    return new PRDevelopmentAPIError(
      "PR development is unavailable.",
      response.status,
    )
  }
  if (
    isRecord(body) &&
    onlyKeys(body, new Set(["error", "detail"])) &&
    isBoundedCanonicalText(body.error, 1024)
  ) {
    if (body.detail === undefined) {
      return new PRDevelopmentAPIError(body.error, response.status)
    }
    if (binding === undefined) {
      return new PRDevelopmentAPIError(
        "PR development is unavailable.",
        response.status,
      )
    }
    try {
      return new PRDevelopmentAPIError(
        body.error,
        response.status,
        parseDetail(body.detail, binding),
      )
    } catch {
      return new PRDevelopmentAPIError(
        "PR development is unavailable.",
        response.status,
      )
    }
  }
  return new PRDevelopmentAPIError(
    "PR development is unavailable.",
    response.status,
  )
}

function setOptionalParam(
  params: URLSearchParams,
  name: string,
  value: string | number | undefined,
): void {
  if (value !== undefined) {
    params.set(name, String(value))
  }
}

function malformedResponse(): PRDevelopmentAPIError {
  return new PRDevelopmentAPIError(MALFORMED_RESPONSE_MESSAGE, 502)
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value)
}

function onlyKeys(
  value: Record<string, unknown>,
  allowed: ReadonlySet<string>,
): boolean {
  return Object.keys(value).every((key) => allowed.has(key))
}

function isPattern(value: unknown, pattern: RegExp): value is string {
  return typeof value === "string" && pattern.test(value)
}

function isSetMember<T extends string>(
  value: unknown,
  values: ReadonlySet<T>,
): value is T {
  return typeof value === "string" && values.has(value as T)
}

function isPullNumber(value: unknown): value is number {
  return (
    Number.isSafeInteger(value) &&
    (value as number) > 0 &&
    (value as number) <= MAXIMUM_PULL_NUMBER
  )
}

function isRepository(value: unknown): value is string {
  if (!isBoundedCanonicalText(value, MAXIMUM_REPOSITORY_BYTES)) {
    return false
  }
  return REPOSITORY_PATTERN.test(value)
}

function isLogin(value: unknown): value is string {
  return isBoundedCanonicalText(value, MAXIMUM_LOGIN_BYTES)
}

function isBoundedCanonicalText(
  value: unknown,
  maximumBytes: number,
): value is string {
  return (
    typeof value === "string" &&
    value !== "" &&
    value === normalizePRDevelopmentChatContent(value) &&
    !value.includes("\0") &&
    isWellFormedUnicode(value) &&
    byteLength(value) <= maximumBytes
  )
}

function isOptionalBoundedText(
  value: unknown,
  maximumBytes: number,
): value is string | undefined {
  return (
    value === undefined ||
    (typeof value === "string" &&
      value !== "" &&
      value === normalizePRDevelopmentChatContent(value) &&
      !value.includes("\0") &&
      isWellFormedUnicode(value) &&
      byteLength(value) <= maximumBytes)
  )
}

function isFeedback(value: unknown): value is string {
  return (
    typeof value === "string" &&
    isWellFormedUnicode(value) &&
    byteLength(value) <= MAXIMUM_FEEDBACK_BYTES
  )
}

function isTimestamp(value: unknown): value is string {
  if (
    typeof value !== "string" ||
    value === "" ||
    value !== normalizePRDevelopmentChatContent(value) ||
    !isWellFormedUnicode(value)
  ) {
    return false
  }
  const match = RFC3339_PATTERN.exec(value)
  if (match == null) return false
  const year = Number(match[1])
  const month = Number(match[2])
  const day = Number(match[3])
  const hour = Number(match[4])
  const minute = Number(match[5])
  const second = Number(match[6])
  const offsetHour = match[8] === undefined ? 0 : Number(match[8])
  const offsetMinute = match[9] === undefined ? 0 : Number(match[9])
  const leapYear = year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0)
  const daysInMonth = [
    0,
    31,
    leapYear ? 29 : 28,
    31,
    30,
    31,
    30,
    31,
    31,
    30,
    31,
    30,
    31,
  ]
  return (
    year > 0 &&
    month >= 1 &&
    month <= 12 &&
    day >= 1 &&
    day <= daysInMonth[month] &&
    hour <= 23 &&
    minute <= 59 &&
    second <= 59 &&
    offsetHour <= 23 &&
    offsetMinute <= 59 &&
    Number.isFinite(Date.parse(value))
  )
}

function isAbsoluteHTTPSURL(value: unknown): value is string {
  if (
    typeof value !== "string" ||
    value !== normalizePRDevelopmentChatContent(value) ||
    value.includes("\0") ||
    !isWellFormedUnicode(value) ||
    byteLength(value) > MAXIMUM_URL_BYTES
  ) {
    return false
  }
  try {
    const parsed = new URL(value)
    return (
      parsed.protocol === "https:" &&
      parsed.host !== "" &&
      parsed.username === "" &&
      parsed.password === ""
    )
  } catch {
    return false
  }
}

function isWellFormedUnicode(value: string): boolean {
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index)
    if (code >= 0xd800 && code <= 0xdbff) {
      if (index + 1 >= value.length) {
        return false
      }
      const next = value.charCodeAt(index + 1)
      if (next < 0xdc00 || next > 0xdfff) {
        return false
      }
      index += 1
    } else if (code >= 0xdc00 && code <= 0xdfff) {
      return false
    }
  }
  return true
}

function byteLength(value: string): number {
  return new TextEncoder().encode(value).byteLength
}
