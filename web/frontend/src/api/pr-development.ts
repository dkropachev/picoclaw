import { launcherFetch } from "@/api/http"

export type PRDevelopmentPullState = "open" | "closed"
export type PRDevelopmentReviewState =
  | "approved"
  | "changes_requested"
  | "commented"
  | "dismissed"
export type PRDevelopmentMessageRole = "user" | "assistant"
export type PRDevelopmentRepairStatus =
  | "queued"
  | "preparing"
  | "running"
  | "completed"
  | "failed"
  | "recovery_required"
export type PRDevelopmentRepairErrorCode =
  | "provider_changed"
  | "not_actionable"
  | "runtime_unavailable"
  | "workspace_unavailable"
  | "repair_failed"
  | "recovery_required"
  | "internal_error"
export type PRDevelopmentCIStatus =
  | "passed"
  | "failed"
  | "incomplete"
  | "plan_changed"
  | "timed_out"
  | "canceled"
  | "output_limit_exceeded"
  | "environment_unavailable"
  | "infrastructure_error"
export type PRDevelopmentLocalReviewStatus =
  | "not_started"
  | "pending"
  | "completed"
export type PRDevelopmentLocalReviewOutcome =
  | "passed"
  | "changes_required"
  | "attention_required"

export interface PRDevelopmentMessage {
  id: string
  ordinal: number
  role: PRDevelopmentMessageRole
  content: string
  created_at: string
}

interface PRDevelopmentCaseBaseSummary {
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

export interface PRDevelopmentCaseSummary extends PRDevelopmentCaseBaseSummary {
  attention_required: boolean
}

export interface PRDevelopmentCase extends PRDevelopmentCaseBaseSummary {
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

export interface PRDevelopmentRepairAttempt {
  id: string
  ordinal: number
  status: PRDevelopmentRepairStatus
  conversation_version: number
  instruction: string
  summary?: string
  error_code?: PRDevelopmentRepairErrorCode
  created_at: string
  updated_at: string
}

export interface PRDevelopmentRepairSession {
  id: string
  revision: number
  agent_id: string
  head_repository?: string
  head_ref?: string
  head_sha?: string
  attempts: PRDevelopmentRepairAttempt[]
}

export interface PRDevelopmentLocalDevelopment {
  attempt_id: string
  attempt_ordinal: number
  attempt_status: PRDevelopmentRepairStatus
  summary?: string
  commit_sha?: string
  no_changes: boolean
  ci_status?: PRDevelopmentCIStatus
  ci_plan_digest?: string
  ci_result_digest?: string
  review_status: PRDevelopmentLocalReviewStatus
  review_outcome?: PRDevelopmentLocalReviewOutcome
  review_summary?: string
  review_finding_count: number
  local_ready: boolean
  updated_at: string
}

export interface PRDevelopmentCaseDetail {
  case: PRDevelopmentCase
  conversation_version: number
  messages: PRDevelopmentMessage[]
  repair_available: boolean
  repair_unavailable_reason?: "runtime_unavailable"
  repair_revision: number
  repair_session?: PRDevelopmentRepairSession
  local_development?: PRDevelopmentLocalDevelopment
}

export interface PRDevelopmentListParams {
  repository?: string
  pull_number?: number
  limit?: number
  cursor?: string
}

export interface StartPRDevelopmentRepairInput {
  expectedConversationVersion: number
  expectedRepairRevision: number
  expectedAttemptOrdinal: number
  requestID: string
  instruction: string
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
const REPAIR_SESSION_ID_PATTERN = /^pds_[0-9a-f]{32}$/
const REPAIR_ATTEMPT_ID_PATTERN = /^pdr_[0-9a-f]{32}$/
const REPAIR_REQUEST_ID_PATTERN = /^prq_[0-9a-f]{32}$/
const AGENT_ID_PATTERN = /^[a-z0-9][a-z0-9_-]{0,63}$/
const GIT_OID_PATTERN = /^(?:[0-9a-f]{40}|[0-9a-f]{64})$/
const SHA256_DIGEST_PATTERN = /^[0-9a-f]{64}$/
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
export const MAXIMUM_PR_DEVELOPMENT_REPAIR_INSTRUCTION_BYTES = 4 << 10
const MAXIMUM_MESSAGE_BYTES = 64 << 10
const MAXIMUM_REPAIR_SUMMARY_BYTES = 4 << 10
const MAXIMUM_MESSAGES = 256
const MAXIMUM_REPAIR_REVISION = 1024
const MAXIMUM_REPAIR_ATTEMPTS = 64
const MAXIMUM_REVIEW_FINDINGS = 128
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
const repairStatuses = new Set<PRDevelopmentRepairStatus>([
  "queued",
  "preparing",
  "running",
  "completed",
  "failed",
  "recovery_required",
])
const repairErrorCodes = new Set<PRDevelopmentRepairErrorCode>([
  "provider_changed",
  "not_actionable",
  "runtime_unavailable",
  "workspace_unavailable",
  "repair_failed",
  "recovery_required",
  "internal_error",
])
const ciStatuses = new Set<PRDevelopmentCIStatus>([
  "passed",
  "failed",
  "incomplete",
  "plan_changed",
  "timed_out",
  "canceled",
  "output_limit_exceeded",
  "environment_unavailable",
  "infrastructure_error",
])
const localReviewStatuses = new Set<PRDevelopmentLocalReviewStatus>([
  "not_started",
  "pending",
  "completed",
])
const localReviewOutcomes = new Set<PRDevelopmentLocalReviewOutcome>([
  "passed",
  "changes_required",
  "attention_required",
])
const baseSummaryKeys = new Set([
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
const listSummaryKeys = new Set([...baseSummaryKeys, "attention_required"])
const detailCaseKeys = new Set([
  ...baseSummaryKeys,
  "base_repository",
  "base_ref",
  "base_sha",
  "review_commit_sha",
  "feedback",
])
const detailKeys = new Set([
  "case",
  "conversation_version",
  "messages",
  "repair_available",
  "repair_unavailable_reason",
  "repair_revision",
  "repair_session",
  "local_development",
])
const messageKeys = new Set(["id", "ordinal", "role", "content", "created_at"])
const repairSessionKeys = new Set([
  "id",
  "revision",
  "agent_id",
  "head_repository",
  "head_ref",
  "head_sha",
  "attempts",
])
const repairAttemptKeys = new Set([
  "id",
  "ordinal",
  "status",
  "conversation_version",
  "instruction",
  "summary",
  "error_code",
  "created_at",
  "updated_at",
])
const localDevelopmentKeys = new Set([
  "attempt_id",
  "attempt_ordinal",
  "attempt_status",
  "summary",
  "commit_sha",
  "no_changes",
  "ci_status",
  "ci_plan_digest",
  "ci_result_digest",
  "review_status",
  "review_outcome",
  "review_summary",
  "review_finding_count",
  "local_ready",
  "updated_at",
])

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
    { caseID, minimumConversationVersion: expectedVersion },
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

export function createPRDevelopmentRepairRequestID(): string {
  const webCrypto = globalThis.crypto
  if (webCrypto == null || typeof webCrypto.getRandomValues !== "function") {
    throw new PRDevelopmentAPIError(
      "Secure random generation is unavailable.",
      503,
    )
  }
  const bytes = webCrypto.getRandomValues(new Uint8Array(16))
  return `prq_${Array.from(bytes, (byte) =>
    byte.toString(16).padStart(2, "0"),
  ).join("")}`
}

export async function startPRDevelopmentRepair(
  caseID: string,
  input: StartPRDevelopmentRepairInput,
): Promise<PRDevelopmentCaseDetail> {
  const instruction = normalizePRDevelopmentChatContent(input.instruction)
  if (
    !CASE_ID_PATTERN.test(caseID) ||
    !isConversationVersion(input.expectedConversationVersion) ||
    !isRepairRevision(input.expectedRepairRevision) ||
    !isExpectedRepairAttemptOrdinal(input.expectedAttemptOrdinal) ||
    !REPAIR_REQUEST_ID_PATTERN.test(input.requestID) ||
    !isBoundedCanonicalText(
      instruction,
      MAXIMUM_PR_DEVELOPMENT_REPAIR_INSTRUCTION_BYTES,
    )
  ) {
    throw new PRDevelopmentAPIError("Invalid local repair request.", 400)
  }

  const response = await launcherFetch(
    `/api/pr-development/${encodeURIComponent(caseID)}/repair`,
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        expected_conversation_version: input.expectedConversationVersion,
        expected_repair_revision: input.expectedRepairRevision,
        request_id: input.requestID,
        instruction,
      }),
    },
  )
  const body = await responseBody(response, {
    caseID,
    minimumConversationVersion: input.expectedConversationVersion,
    minimumRepairRevision: input.expectedRepairRevision,
  })
  if (response.status !== 202) {
    throw malformedResponse()
  }
  const detail = parseDetail(body, { caseID })
  const matchingAttempt =
    detail.repair_session?.attempts[input.expectedAttemptOrdinal]
  if (
    detail.conversation_version < input.expectedConversationVersion ||
    detail.repair_revision <= input.expectedRepairRevision ||
    matchingAttempt?.conversation_version !==
      input.expectedConversationVersion ||
    matchingAttempt.instruction !== instruction
  ) {
    throw malformedResponse()
  }
  return detail
}

function parseSummary(value: unknown): PRDevelopmentCaseSummary {
  if (
    !isRecord(value) ||
    !onlyKeys(value, listSummaryKeys) ||
    typeof value.attention_required !== "boolean"
  ) {
    throw malformedResponse()
  }
  return {
    ...parseBaseSummaryRecord(value),
    attention_required: value.attention_required,
  }
}

function parseBaseSummaryRecord(
  value: Record<string, unknown>,
): PRDevelopmentCaseBaseSummary {
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
  const summary = parseBaseSummaryRecord(value)
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
  binding?: {
    caseID: string
    minimumConversationVersion?: number
    minimumRepairRevision?: number
  },
): PRDevelopmentCaseDetail {
  if (
    !isRecord(value) ||
    !onlyKeys(value, detailKeys) ||
    !isConversationVersion(value.conversation_version) ||
    typeof value.repair_available !== "boolean" ||
    !isRepairRevision(value.repair_revision) ||
    (value.repair_unavailable_reason !== undefined &&
      value.repair_unavailable_reason !== "runtime_unavailable") ||
    (value.repair_available && value.repair_unavailable_reason !== undefined) ||
    (!value.repair_available &&
      value.repair_unavailable_reason !== "runtime_unavailable")
  ) {
    throw malformedResponse()
  }
  const conversationVersion = value.conversation_version as number
  const repairRevision = value.repair_revision as number
  const developmentCase = parseCase(value.case)
  if (
    binding !== undefined &&
    (developmentCase.id !== binding.caseID ||
      (binding.minimumConversationVersion !== undefined &&
        conversationVersion < binding.minimumConversationVersion) ||
      (binding.minimumRepairRevision !== undefined &&
        repairRevision < binding.minimumRepairRevision))
  ) {
    throw malformedResponse()
  }
  const repairSession = parseRepairSession(
    value.repair_session,
    repairRevision,
    conversationVersion,
  )
  const localDevelopment = parseLocalDevelopment(
    value.local_development,
    repairSession,
  )
  return {
    case: developmentCase,
    conversation_version: conversationVersion,
    messages: parseMessages(value.messages, conversationVersion),
    repair_available: value.repair_available,
    ...(value.repair_unavailable_reason === undefined
      ? {}
      : { repair_unavailable_reason: value.repair_unavailable_reason }),
    repair_revision: repairRevision,
    ...(repairSession === undefined ? {} : { repair_session: repairSession }),
    ...(localDevelopment === undefined
      ? {}
      : { local_development: localDevelopment }),
  }
}

function parseRepairSession(
  value: unknown,
  repairRevision: number,
  conversationVersion: number,
): PRDevelopmentRepairSession | undefined {
  if (value === undefined) {
    if (repairRevision !== 0) throw malformedResponse()
    return undefined
  }
  if (
    !isRecord(value) ||
    !onlyKeys(value, repairSessionKeys) ||
    !isPattern(value.id, REPAIR_SESSION_ID_PATTERN) ||
    value.revision !== repairRevision ||
    repairRevision === 0 ||
    !isPattern(value.agent_id, AGENT_ID_PATTERN) ||
    !Array.isArray(value.attempts) ||
    value.attempts.length === 0 ||
    value.attempts.length > MAXIMUM_REPAIR_ATTEMPTS
  ) {
    throw malformedResponse()
  }
  const pinnedValues = [value.head_repository, value.head_ref, value.head_sha]
  const pinned = pinnedValues.every((item) => item !== undefined)
  if (
    (!pinned && pinnedValues.some((item) => item !== undefined)) ||
    (pinned &&
      (!isRepository(value.head_repository) ||
        !isBoundedCanonicalText(value.head_ref, MAXIMUM_REF_BYTES) ||
        !isPattern(value.head_sha, GIT_OID_PATTERN)))
  ) {
    throw malformedResponse()
  }

  const attempts = parseRepairAttempts(value.attempts, conversationVersion)
  if (
    !pinned &&
    attempts.some(
      (attempt) =>
        attempt.status === "running" ||
        attempt.status === "completed" ||
        attempt.status === "recovery_required",
    )
  ) {
    throw malformedResponse()
  }
  return {
    id: value.id,
    revision: repairRevision,
    agent_id: value.agent_id,
    ...(pinned
      ? {
          head_repository: value.head_repository as string,
          head_ref: value.head_ref as string,
          head_sha: value.head_sha as string,
        }
      : {}),
    attempts,
  }
}

function parseRepairAttempts(
  attempts: unknown[],
  detailConversationVersion: number,
): PRDevelopmentRepairAttempt[] {
  const ids = new Set<string>()
  let previousCreatedAt = ""
  let previousConversationVersion = 0
  return attempts.map((attempt, ordinal) => {
    if (
      !isRecord(attempt) ||
      !onlyKeys(attempt, repairAttemptKeys) ||
      !isPattern(attempt.id, REPAIR_ATTEMPT_ID_PATTERN) ||
      ids.has(attempt.id) ||
      attempt.ordinal !== ordinal ||
      !isSetMember(attempt.status, repairStatuses) ||
      !isConversationVersion(attempt.conversation_version) ||
      attempt.conversation_version > detailConversationVersion ||
      (ordinal > 0 &&
        attempt.conversation_version < previousConversationVersion) ||
      !isBoundedCanonicalText(
        attempt.instruction,
        MAXIMUM_PR_DEVELOPMENT_REPAIR_INSTRUCTION_BYTES,
      ) ||
      !isOptionalBoundedText(attempt.summary, MAXIMUM_REPAIR_SUMMARY_BYTES) ||
      (attempt.error_code !== undefined &&
        !isSetMember(attempt.error_code, repairErrorCodes)) ||
      !isTimestamp(attempt.created_at) ||
      !isTimestamp(attempt.updated_at) ||
      Date.parse(attempt.updated_at) < Date.parse(attempt.created_at) ||
      (previousCreatedAt !== "" &&
        Date.parse(attempt.created_at) < Date.parse(previousCreatedAt)) ||
      (ordinal < attempts.length - 1 &&
        !isTerminalRepairStatus(attempt.status)) ||
      !hasValidRepairOutcome(attempt)
    ) {
      throw malformedResponse()
    }
    ids.add(attempt.id)
    previousCreatedAt = attempt.created_at
    previousConversationVersion = attempt.conversation_version
    return {
      id: attempt.id,
      ordinal,
      status: attempt.status,
      conversation_version: attempt.conversation_version,
      instruction: attempt.instruction,
      ...(attempt.summary === undefined ? {} : { summary: attempt.summary }),
      ...(attempt.error_code === undefined
        ? {}
        : { error_code: attempt.error_code }),
      created_at: attempt.created_at,
      updated_at: attempt.updated_at,
    }
  })
}

function hasValidRepairOutcome(attempt: Record<string, unknown>): boolean {
  switch (attempt.status) {
    case "queued":
    case "preparing":
    case "running":
      return attempt.summary === undefined && attempt.error_code === undefined
    case "completed":
      return attempt.summary !== undefined && attempt.error_code === undefined
    case "failed":
      return (
        attempt.summary !== undefined &&
        attempt.error_code !== undefined &&
        attempt.error_code !== "recovery_required"
      )
    case "recovery_required":
      return (
        attempt.summary !== undefined &&
        attempt.error_code === "recovery_required"
      )
    default:
      return false
  }
}

function isTerminalRepairStatus(status: PRDevelopmentRepairStatus): boolean {
  return (
    status === "completed" ||
    status === "failed" ||
    status === "recovery_required"
  )
}

function parseLocalDevelopment(
  value: unknown,
  repairSession: PRDevelopmentRepairSession | undefined,
): PRDevelopmentLocalDevelopment | undefined {
  if (repairSession === undefined) {
    if (value !== undefined) throw malformedResponse()
    return undefined
  }
  const attempt = repairSession.attempts.at(-1)
  if (
    attempt === undefined ||
    !isRecord(value) ||
    !onlyKeys(value, localDevelopmentKeys) ||
    value.attempt_id !== attempt.id ||
    value.attempt_ordinal !== attempt.ordinal ||
    value.attempt_status !== attempt.status ||
    value.summary !== attempt.summary ||
    typeof value.no_changes !== "boolean" ||
    !isSetMember(value.review_status, localReviewStatuses) ||
    !Number.isSafeInteger(value.review_finding_count) ||
    (value.review_finding_count as number) < 0 ||
    (value.review_finding_count as number) > MAXIMUM_REVIEW_FINDINGS ||
    typeof value.local_ready !== "boolean" ||
    !isTimestamp(value.updated_at) ||
    Date.parse(value.updated_at) < Date.parse(attempt.updated_at) ||
    !isOptionalBoundedText(value.review_summary, MAXIMUM_REPAIR_SUMMARY_BYTES)
  ) {
    throw malformedResponse()
  }

  const ciValues = [
    value.commit_sha,
    value.ci_status,
    value.ci_plan_digest,
    value.ci_result_digest,
  ]
  const hasCI = ciValues.every((item) => item !== undefined)
  if (
    (!hasCI && ciValues.some((item) => item !== undefined)) ||
    (hasCI &&
      (!isPattern(value.commit_sha, GIT_OID_PATTERN) ||
        !isSetMember(value.ci_status, ciStatuses) ||
        !isPattern(value.ci_plan_digest, SHA256_DIGEST_PATTERN) ||
        !isPattern(value.ci_result_digest, SHA256_DIGEST_PATTERN))) ||
    (!hasCI && value.no_changes) ||
    (hasCI && attempt.status !== "completed") ||
    (value.review_status === "not_started" && hasCI) ||
    (value.review_status !== "not_started" && !hasCI)
  ) {
    throw malformedResponse()
  }

  const reviewCompleted = value.review_status === "completed"
  const derivedLocalReady =
    value.ci_status === "passed" &&
    reviewCompleted &&
    value.review_outcome === "passed"
  if (
    (reviewCompleted &&
      (!isSetMember(value.review_outcome, localReviewOutcomes) ||
        !isBoundedCanonicalText(
          value.review_summary,
          MAXIMUM_REPAIR_SUMMARY_BYTES,
        ))) ||
    (!reviewCompleted &&
      (value.review_outcome !== undefined ||
        value.review_summary !== undefined ||
        value.review_finding_count !== 0)) ||
    (value.review_outcome === "passed" && value.review_finding_count !== 0) ||
    (value.review_outcome === "passed" && value.ci_status !== "passed") ||
    (value.review_outcome === "changes_required" &&
      value.review_finding_count === 0) ||
    value.local_ready !== derivedLocalReady
  ) {
    throw malformedResponse()
  }

  return {
    attempt_id: attempt.id,
    attempt_ordinal: attempt.ordinal,
    attempt_status: attempt.status,
    ...(attempt.summary === undefined ? {} : { summary: attempt.summary }),
    ...(hasCI
      ? {
          commit_sha: value.commit_sha as string,
          ci_status: value.ci_status as PRDevelopmentCIStatus,
          ci_plan_digest: value.ci_plan_digest as string,
          ci_result_digest: value.ci_result_digest as string,
        }
      : {}),
    no_changes: value.no_changes,
    review_status: value.review_status,
    ...(reviewCompleted
      ? {
          review_outcome:
            value.review_outcome as PRDevelopmentLocalReviewOutcome,
          review_summary: value.review_summary as string,
        }
      : {}),
    review_finding_count: value.review_finding_count as number,
    local_ready: value.local_ready,
    updated_at: value.updated_at,
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
  errorBinding?: {
    caseID: string
    minimumConversationVersion?: number
    minimumRepairRevision?: number
  },
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
  binding?: {
    caseID: string
    minimumConversationVersion?: number
    minimumRepairRevision?: number
  },
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

function isConversationVersion(value: unknown): value is number {
  return (
    Number.isSafeInteger(value) &&
    (value as number) >= 0 &&
    (value as number) <= MAXIMUM_MESSAGES
  )
}

function isRepairRevision(value: unknown): value is number {
  return (
    Number.isSafeInteger(value) &&
    (value as number) >= 0 &&
    (value as number) <= MAXIMUM_REPAIR_REVISION
  )
}

function isExpectedRepairAttemptOrdinal(value: unknown): value is number {
  // This fences the pre-admission history suffix, so a full 64-attempt
  // history legitimately has next ordinal 64 even though no parsed response
  // may contain an attempt at that ordinal.
  return (
    Number.isSafeInteger(value) &&
    (value as number) >= 0 &&
    (value as number) <= MAXIMUM_REPAIR_ATTEMPTS
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
