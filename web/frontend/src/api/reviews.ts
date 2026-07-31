import { launcherFetch } from "@/api/http"

export type ReviewCaseStatus =
  | "open"
  | "all_dropped"
  | "submitting"
  | "submission_unknown"
  | "submitted"
  | "stale"

export type ReviewFindingState = "active" | "dropped"
export type ReviewSeverity = "critical" | "high" | "medium" | "low"
export type ReviewMessageKind = "chat" | "rephrase"
export type ReviewMessageRole = "user" | "assistant"
export type ReviewReconciliationResolution = "submitted" | "absent"
export type ReviewSubmissionStatus =
  | "pending"
  | "claimed"
  | "submitted"
  | "unknown"
  | "failed"

export interface ReviewCase {
  id: string
  event_id: string
  dispatch_id: string
  run_id: string
  workflow_ref: string
  workflow_revision?: string
  connector: string
  repository: string
  pull_number: number
  pull_url: string
  base_sha: string
  head_sha: string
  summary: string
  tests: string[]
  residual_risks: string[]
  status: ReviewCaseStatus
  version: number
  active_findings: number
  total_findings: number
  public_error_code?: string
  created_at: string
  updated_at: string
  resolved_at?: string
  submitted_at?: string
}

export interface ReviewFindingDraft {
  severity: ReviewSeverity
  title: string
  file?: string
  line?: number
  message: string
  evidence?: string
  impact?: string
  recommendation?: string
  validation?: string
}

export interface ReviewFinding extends ReviewFindingDraft {
  id: string
  case_id: string
  ordinal: number
  state: ReviewFindingState
  dropped_reason?: string
  revision: number
  created_at: string
  updated_at: string
  dropped_at?: string
}

export interface ReviewMessage {
  id: string
  case_id: string
  ordinal: number
  finding_id?: string
  kind: ReviewMessageKind
  role: ReviewMessageRole
  content: string
  created_at: string
}

/**
 * Intentionally omits the internal marker, lease, request, and error fields.
 * Any such field in a response makes the whole projection invalid.
 */
export interface ReviewSubmission {
  id: string
  case_id: string
  draft_version: number
  status: ReviewSubmissionStatus
  attempts: number
  public_error_code?: string
  external_review_id?: string
  external_url?: string
  created_at: string
  updated_at: string
  submitted_at?: string
}

export interface ReviewCaseDetail {
  case: ReviewCase
  findings: ReviewFinding[]
  messages: ReviewMessage[]
  submission?: ReviewSubmission
}

export interface ReviewCasePage {
  cases: ReviewCase[]
  next_cursor?: string
}

export interface ReviewListParams {
  status?: ReviewCaseStatus
  repository?: string
  limit?: number
  cursor?: string
}

export interface ReviewRephraseResult {
  detail: ReviewCaseDetail
  suggestion: {
    title: string
    message: string
  }
}

export class ReviewAPIError extends Error {
  readonly status: number
  readonly detail?: ReviewCaseDetail

  constructor(message: string, status: number, detail?: ReviewCaseDetail) {
    super(message)
    this.name = "ReviewAPIError"
    this.status = status
    this.detail = detail
  }
}

const MALFORMED_RESPONSE_MESSAGE =
  "The review service returned a malformed response."
const REVIEW_ID_PATTERNS = {
  case: /^prc_[0-9a-f]{32}$/,
  finding: /^prf_[0-9a-f]{32}$/,
  message: /^prm_[0-9a-f]{32}$/,
  submission: /^prs_[0-9a-f]{32}$/,
} as const
const EVENT_ID_PATTERN = /^ev_[0-9a-f]{32}$/
const DISPATCH_ID_PATTERN = /^dsp_[0-9a-f]{32}$/
const GIT_OID_PATTERN = /^(?:[0-9a-f]{40}|[0-9a-f]{64})$/
const MAX_FINDINGS = 200
const MAX_MESSAGES = 256
const MAX_TRANSCRIPT_BYTES = 4 << 20
const MAX_TEXT_ITEMS = 256
const MAX_REPOSITORY_BYTES = 512
const MAX_URL_BYTES = 4096
const MAX_FILE_BYTES = 4096
const MAX_TITLE_BYTES = 8192
const MAX_TEXT_BYTES = 64 << 10
const MAX_CURSOR_BYTES = 4096

const caseStatuses = new Set<ReviewCaseStatus>([
  "open",
  "all_dropped",
  "submitting",
  "submission_unknown",
  "submitted",
  "stale",
])
const findingStates = new Set<ReviewFindingState>(["active", "dropped"])
const severities = new Set<ReviewSeverity>([
  "critical",
  "high",
  "medium",
  "low",
])
const messageKinds = new Set<ReviewMessageKind>(["chat", "rephrase"])
const messageRoles = new Set<ReviewMessageRole>(["user", "assistant"])
const submissionStatuses = new Set<ReviewSubmissionStatus>([
  "pending",
  "claimed",
  "submitted",
  "unknown",
  "failed",
])

const caseKeys = new Set([
  "id",
  "event_id",
  "dispatch_id",
  "run_id",
  "workflow_ref",
  "workflow_revision",
  "connector",
  "repository",
  "pull_number",
  "pull_url",
  "base_sha",
  "head_sha",
  "summary",
  "tests",
  "residual_risks",
  "status",
  "version",
  "active_findings",
  "total_findings",
  "public_error_code",
  "created_at",
  "updated_at",
  "resolved_at",
  "submitted_at",
])
const findingKeys = new Set([
  "id",
  "case_id",
  "ordinal",
  "state",
  "severity",
  "title",
  "file",
  "line",
  "message",
  "evidence",
  "impact",
  "recommendation",
  "validation",
  "dropped_reason",
  "revision",
  "created_at",
  "updated_at",
  "dropped_at",
])
const messageKeys = new Set([
  "id",
  "case_id",
  "ordinal",
  "finding_id",
  "kind",
  "role",
  "content",
  "created_at",
])
const submissionKeys = new Set([
  "id",
  "case_id",
  "draft_version",
  "status",
  "attempts",
  "public_error_code",
  "external_review_id",
  "external_url",
  "created_at",
  "updated_at",
  "submitted_at",
])

export async function listReviews(
  input: ReviewListParams = {},
): Promise<ReviewCasePage> {
  const params = new URLSearchParams()
  setOptionalParam(params, "status", input.status)
  setOptionalParam(params, "repository", input.repository)
  setOptionalParam(params, "limit", input.limit)
  setOptionalParam(params, "cursor", input.cursor)
  const query = params.toString()
  const response = await launcherFetch(
    query === "" ? "/api/reviews" : `/api/reviews?${query}`,
    undefined,
  )
  const body = await responseBody(response)
  if (
    !isRecord(body) ||
    !onlyKeys(body, new Set(["cases", "next_cursor"])) ||
    !Array.isArray(body.cases) ||
    body.cases.length > 100 ||
    !isOptionalBoundedString(body.next_cursor, MAX_CURSOR_BYTES, true)
  ) {
    throw malformedResponse()
  }
  return {
    cases: body.cases.map(parseReviewCase),
    ...(body.next_cursor === undefined
      ? {}
      : { next_cursor: body.next_cursor }),
  }
}

export async function getReview(caseID: string): Promise<ReviewCaseDetail> {
  return readDetail(
    await launcherFetch(
      `/api/reviews/${encodeURIComponent(caseID)}`,
      undefined,
    ),
  )
}

export async function updateReviewFinding(
  caseID: string,
  findingID: string,
  expectedVersion: number,
  finding: ReviewFindingDraft,
): Promise<ReviewCaseDetail> {
  assertLocalFindingDraft(finding)
  return readDetail(
    await jsonRequest(
      `/api/reviews/${encodeURIComponent(caseID)}/findings/${encodeURIComponent(findingID)}`,
      "PATCH",
      {
        expected_version: expectedVersion,
        finding,
      },
    ),
  )
}

export async function dropReviewFinding(
  caseID: string,
  findingID: string,
  expectedVersion: number,
  reason?: string,
): Promise<ReviewCaseDetail> {
  return readDetail(
    await jsonRequest(
      `/api/reviews/${encodeURIComponent(caseID)}/findings/${encodeURIComponent(findingID)}/drop`,
      "POST",
      {
        expected_version: expectedVersion,
        ...(reason?.trim() ? { reason: reason.trim() } : {}),
      },
    ),
  )
}

export async function restoreReviewFinding(
  caseID: string,
  findingID: string,
  expectedVersion: number,
  reason?: string,
): Promise<ReviewCaseDetail> {
  return readDetail(
    await jsonRequest(
      `/api/reviews/${encodeURIComponent(caseID)}/findings/${encodeURIComponent(findingID)}/restore`,
      "POST",
      {
        expected_version: expectedVersion,
        ...(reason?.trim() ? { reason: reason.trim() } : {}),
      },
    ),
  )
}

export async function chatAboutReview(
  caseID: string,
  expectedVersion: number,
  content: string,
  findingID?: string,
): Promise<ReviewCaseDetail> {
  return readDetail(
    await jsonRequest(
      `/api/reviews/${encodeURIComponent(caseID)}/chat`,
      "POST",
      {
        expected_version: expectedVersion,
        ...(findingID ? { finding_id: findingID } : {}),
        content,
      },
    ),
  )
}

export async function rephraseReviewFinding(
  caseID: string,
  findingID: string,
  expectedVersion: number,
  instruction: string,
): Promise<ReviewRephraseResult> {
  const response = await jsonRequest(
    `/api/reviews/${encodeURIComponent(caseID)}/findings/${encodeURIComponent(findingID)}/rephrase`,
    "POST",
    { expected_version: expectedVersion, instruction },
  )
  const body = await responseBody(response)
  if (
    !isRecord(body) ||
    !onlyKeys(body, new Set(["detail", "suggestion"])) ||
    !isRecord(body.suggestion) ||
    !onlyKeys(body.suggestion, new Set(["title", "message"])) ||
    !isBoundedTrimmedString(body.suggestion.title, MAX_TITLE_BYTES) ||
    !isBoundedTrimmedString(body.suggestion.message, MAX_TEXT_BYTES)
  ) {
    throw malformedResponse()
  }
  return {
    detail: parseReviewDetail(body.detail),
    suggestion: {
      title: body.suggestion.title,
      message: body.suggestion.message,
    },
  }
}

export async function submitReview(
  caseID: string,
  expectedVersion: number,
): Promise<ReviewCaseDetail> {
  return readDetail(
    await jsonRequest(
      `/api/reviews/${encodeURIComponent(caseID)}/submit`,
      "POST",
      { expected_version: expectedVersion },
    ),
  )
}

export async function reconcileReview(
  caseID: string,
  expectedVersion: number,
  resolution: ReviewReconciliationResolution,
): Promise<ReviewCaseDetail> {
  if (resolution !== "submitted" && resolution !== "absent") {
    throw new ReviewAPIError("The reconciliation resolution is invalid.", 400)
  }
  return readDetail(
    await jsonRequest(
      `/api/reviews/${encodeURIComponent(caseID)}/reconcile`,
      "POST",
      { expected_version: expectedVersion, resolution },
    ),
  )
}

async function readDetail(response: Response): Promise<ReviewCaseDetail> {
  return parseReviewDetail(await responseBody(response))
}

async function jsonRequest(
  path: string,
  method: "PATCH" | "POST",
  body: unknown,
): Promise<Response> {
  return launcherFetch(path, {
    method,
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  })
}

async function responseBody(response: Response): Promise<unknown> {
  if (!response.ok) {
    throw await errorFromResponse(response)
  }
  try {
    return (await response.json()) as unknown
  } catch {
    throw malformedResponse()
  }
}

async function errorFromResponse(response: Response): Promise<ReviewAPIError> {
  const fallback =
    `Review request failed: ${response.status} ${response.statusText}`.trim()
  try {
    const body = (await response.json()) as unknown
    if (
      isRecord(body) &&
      onlyKeys(body, new Set(["error", "detail"])) &&
      typeof body.error === "string" &&
      body.error !== ""
    ) {
      let detail: ReviewCaseDetail | undefined
      if (body.detail !== undefined) {
        try {
          detail = parseReviewDetail(body.detail)
        } catch {
          detail = undefined
        }
      }
      return new ReviewAPIError(body.error, response.status, detail)
    }
  } catch {
    // Fall through to the status-based error.
  }
  return new ReviewAPIError(fallback, response.status)
}

function malformedResponse(): ReviewAPIError {
  return new ReviewAPIError(MALFORMED_RESPONSE_MESSAGE, 502)
}

function parseReviewDetail(value: unknown): ReviewCaseDetail {
  if (
    !isRecord(value) ||
    !onlyKeys(value, new Set(["case", "findings", "messages", "submission"])) ||
    !(Array.isArray(value.findings) || value.findings === null) ||
    !(Array.isArray(value.messages) || value.messages === null)
  ) {
    throw malformedResponse()
  }

  const rawFindings = value.findings ?? []
  const rawMessages = value.messages ?? []
  if (rawFindings.length > MAX_FINDINGS || rawMessages.length > MAX_MESSAGES) {
    throw malformedResponse()
  }
  const reviewCase = parseReviewCase(value.case)
  const findings = rawFindings.map(parseReviewFinding)
  const messages = rawMessages.map(parseReviewMessage)
  if (
    messages.reduce(
      (total, message) => total + byteLength(message.content),
      0,
    ) > MAX_TRANSCRIPT_BYTES
  ) {
    throw malformedResponse()
  }
  const submission =
    value.submission === undefined
      ? undefined
      : parseReviewSubmission(value.submission)
  const findingIDs = new Set<string>()
  const findingOrdinals = new Set<number>()
  let activeFindings = 0

  for (const finding of findings) {
    if (
      finding.case_id !== reviewCase.id ||
      findingIDs.has(finding.id) ||
      findingOrdinals.has(finding.ordinal)
    ) {
      throw malformedResponse()
    }
    findingIDs.add(finding.id)
    findingOrdinals.add(finding.ordinal)
    if (finding.state === "active") {
      activeFindings += 1
    }
  }

  const messageIDs = new Set<string>()
  const messageOrdinals = new Set<number>()
  for (const message of messages) {
    if (
      message.case_id !== reviewCase.id ||
      messageIDs.has(message.id) ||
      messageOrdinals.has(message.ordinal) ||
      (message.finding_id !== undefined && !findingIDs.has(message.finding_id))
    ) {
      throw malformedResponse()
    }
    messageIDs.add(message.id)
    messageOrdinals.add(message.ordinal)
  }

  if (
    reviewCase.total_findings !== findings.length ||
    reviewCase.active_findings !== activeFindings ||
    (reviewCase.status === "open" && activeFindings === 0) ||
    (reviewCase.status === "all_dropped" && activeFindings !== 0) ||
    (submission !== undefined && submission.case_id !== reviewCase.id)
  ) {
    throw malformedResponse()
  }

  return {
    case: reviewCase,
    findings,
    messages,
    ...(submission === undefined ? {} : { submission }),
  }
}

function parseReviewCase(value: unknown): ReviewCase {
  if (
    !isRecord(value) ||
    !onlyKeys(value, caseKeys) ||
    !isPattern(value.id, REVIEW_ID_PATTERNS.case) ||
    !isPattern(value.event_id, EVENT_ID_PATTERN) ||
    !isPattern(value.dispatch_id, DISPATCH_ID_PATTERN) ||
    !isBoundedTrimmedString(value.run_id, 1024) ||
    !isBoundedTrimmedString(value.workflow_ref, 1024) ||
    !isOptionalBoundedString(value.workflow_revision, 256) ||
    !isBoundedTrimmedString(value.connector, 128) ||
    !isBoundedTrimmedString(value.repository, MAX_REPOSITORY_BYTES) ||
    !isPositiveInteger(value.pull_number) ||
    !isAbsoluteHTTPURL(value.pull_url) ||
    !isPattern(value.base_sha, GIT_OID_PATTERN) ||
    !isPattern(value.head_sha, GIT_OID_PATTERN) ||
    !isBoundedTrimmedString(value.summary, MAX_TEXT_BYTES) ||
    !isOptionalStringList(value.tests) ||
    !isOptionalStringList(value.residual_risks) ||
    !isSetMember(value.status, caseStatuses) ||
    !isPositiveInteger(value.version) ||
    !isNonNegativeInteger(value.active_findings) ||
    !isNonNegativeInteger(value.total_findings) ||
    value.active_findings > value.total_findings ||
    value.total_findings > MAX_FINDINGS ||
    !isOptionalBoundedString(value.public_error_code, 256) ||
    !isTimestamp(value.created_at) ||
    !isTimestamp(value.updated_at) ||
    !isOptionalTimestamp(value.resolved_at) ||
    !isOptionalTimestamp(value.submitted_at)
  ) {
    throw malformedResponse()
  }

  return {
    id: value.id,
    event_id: value.event_id,
    dispatch_id: value.dispatch_id,
    run_id: value.run_id,
    workflow_ref: value.workflow_ref,
    ...(value.workflow_revision === undefined
      ? {}
      : { workflow_revision: value.workflow_revision }),
    connector: value.connector,
    repository: value.repository,
    pull_number: value.pull_number,
    pull_url: value.pull_url,
    base_sha: value.base_sha,
    head_sha: value.head_sha,
    summary: value.summary,
    tests: value.tests === undefined ? [] : [...value.tests],
    residual_risks:
      value.residual_risks === undefined ? [] : [...value.residual_risks],
    status: value.status,
    version: value.version,
    active_findings: value.active_findings,
    total_findings: value.total_findings,
    ...(value.public_error_code === undefined
      ? {}
      : { public_error_code: value.public_error_code }),
    created_at: value.created_at,
    updated_at: value.updated_at,
    ...(value.resolved_at === undefined
      ? {}
      : { resolved_at: value.resolved_at }),
    ...(value.submitted_at === undefined
      ? {}
      : { submitted_at: value.submitted_at }),
  }
}

function parseReviewFinding(value: unknown): ReviewFinding {
  if (
    !isRecord(value) ||
    !onlyKeys(value, findingKeys) ||
    !isPattern(value.id, REVIEW_ID_PATTERNS.finding) ||
    !isPattern(value.case_id, REVIEW_ID_PATTERNS.case) ||
    !isNonNegativeInteger(value.ordinal) ||
    !isSetMember(value.state, findingStates) ||
    !isSetMember(value.severity, severities) ||
    !isBoundedTrimmedString(value.title, MAX_TITLE_BYTES) ||
    !isOptionalBoundedString(value.file, MAX_FILE_BYTES) ||
    !isOptionalPositiveInteger(value.line) ||
    (value.line !== undefined && value.file === undefined) ||
    !isBoundedTrimmedString(value.message, MAX_TEXT_BYTES) ||
    !isOptionalBoundedString(value.evidence, MAX_TEXT_BYTES) ||
    !isOptionalBoundedString(value.impact, MAX_TEXT_BYTES) ||
    !isOptionalBoundedString(value.recommendation, MAX_TEXT_BYTES) ||
    !isOptionalBoundedString(value.validation, MAX_TEXT_BYTES) ||
    !isOptionalBoundedString(value.dropped_reason, MAX_TEXT_BYTES) ||
    !isPositiveInteger(value.revision) ||
    !isTimestamp(value.created_at) ||
    !isTimestamp(value.updated_at) ||
    !isOptionalTimestamp(value.dropped_at) ||
    (value.state === "active" &&
      (value.dropped_at !== undefined || value.dropped_reason !== undefined)) ||
    (value.state === "dropped" && value.dropped_at === undefined)
  ) {
    throw malformedResponse()
  }

  return {
    id: value.id,
    case_id: value.case_id,
    ordinal: value.ordinal,
    state: value.state,
    severity: value.severity,
    title: value.title,
    ...(value.file === undefined ? {} : { file: value.file }),
    ...(value.line === undefined ? {} : { line: value.line }),
    message: value.message,
    ...(value.evidence === undefined ? {} : { evidence: value.evidence }),
    ...(value.impact === undefined ? {} : { impact: value.impact }),
    ...(value.recommendation === undefined
      ? {}
      : { recommendation: value.recommendation }),
    ...(value.validation === undefined ? {} : { validation: value.validation }),
    ...(value.dropped_reason === undefined
      ? {}
      : { dropped_reason: value.dropped_reason }),
    revision: value.revision,
    created_at: value.created_at,
    updated_at: value.updated_at,
    ...(value.dropped_at === undefined ? {} : { dropped_at: value.dropped_at }),
  }
}

function parseReviewMessage(value: unknown): ReviewMessage {
  if (
    !isRecord(value) ||
    !onlyKeys(value, messageKeys) ||
    !isPattern(value.id, REVIEW_ID_PATTERNS.message) ||
    !isPattern(value.case_id, REVIEW_ID_PATTERNS.case) ||
    !isNonNegativeInteger(value.ordinal) ||
    !isOptionalPattern(value.finding_id, REVIEW_ID_PATTERNS.finding) ||
    !isSetMember(value.kind, messageKinds) ||
    !isSetMember(value.role, messageRoles) ||
    !isBoundedTrimmedString(value.content, MAX_TEXT_BYTES) ||
    !isTimestamp(value.created_at) ||
    (value.kind === "rephrase" && value.finding_id === undefined)
  ) {
    throw malformedResponse()
  }
  return {
    id: value.id,
    case_id: value.case_id,
    ordinal: value.ordinal,
    ...(value.finding_id === undefined ? {} : { finding_id: value.finding_id }),
    kind: value.kind,
    role: value.role,
    content: value.content,
    created_at: value.created_at,
  }
}

function parseReviewSubmission(value: unknown): ReviewSubmission {
  if (
    !isRecord(value) ||
    !onlyKeys(value, submissionKeys) ||
    !isPattern(value.id, REVIEW_ID_PATTERNS.submission) ||
    !isPattern(value.case_id, REVIEW_ID_PATTERNS.case) ||
    !isPositiveInteger(value.draft_version) ||
    !isSetMember(value.status, submissionStatuses) ||
    !isNonNegativeInteger(value.attempts) ||
    !isOptionalBoundedString(value.public_error_code, 256) ||
    !isOptionalBoundedString(value.external_review_id, 1024) ||
    !isOptionalAbsoluteHTTPURL(value.external_url) ||
    !isTimestamp(value.created_at) ||
    !isTimestamp(value.updated_at) ||
    !isOptionalTimestamp(value.submitted_at)
  ) {
    throw malformedResponse()
  }
  return {
    id: value.id,
    case_id: value.case_id,
    draft_version: value.draft_version,
    status: value.status,
    attempts: value.attempts,
    ...(value.public_error_code === undefined
      ? {}
      : { public_error_code: value.public_error_code }),
    ...(value.external_review_id === undefined
      ? {}
      : { external_review_id: value.external_review_id }),
    ...(value.external_url === undefined
      ? {}
      : { external_url: value.external_url }),
    created_at: value.created_at,
    updated_at: value.updated_at,
    ...(value.submitted_at === undefined
      ? {}
      : { submitted_at: value.submitted_at }),
  }
}

function assertLocalFindingDraft(finding: ReviewFindingDraft): void {
  if (
    !severities.has(finding.severity) ||
    !isBoundedTrimmedString(finding.title, MAX_TITLE_BYTES) ||
    !isOptionalBoundedString(finding.file, MAX_FILE_BYTES) ||
    !isOptionalPositiveInteger(finding.line) ||
    (finding.line !== undefined && !finding.file) ||
    !isBoundedTrimmedString(finding.message, MAX_TEXT_BYTES) ||
    !isOptionalBoundedString(finding.evidence, MAX_TEXT_BYTES) ||
    !isOptionalBoundedString(finding.impact, MAX_TEXT_BYTES) ||
    !isOptionalBoundedString(finding.recommendation, MAX_TEXT_BYTES) ||
    !isOptionalBoundedString(finding.validation, MAX_TEXT_BYTES)
  ) {
    throw new ReviewAPIError("The review finding is invalid.", 400)
  }
}

function setOptionalParam(
  params: URLSearchParams,
  name: string,
  value: string | number | undefined,
): void {
  if (value !== undefined && value !== "") {
    params.set(name, String(value))
  }
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

function isOptionalPattern(
  value: unknown,
  pattern: RegExp,
): value is string | undefined {
  return value === undefined || isPattern(value, pattern)
}

function isSetMember<T extends string>(
  value: unknown,
  values: ReadonlySet<T>,
): value is T {
  return typeof value === "string" && values.has(value as T)
}

function isNonNegativeInteger(value: unknown): value is number {
  return Number.isSafeInteger(value) && (value as number) >= 0
}

function isPositiveInteger(value: unknown): value is number {
  return Number.isSafeInteger(value) && (value as number) > 0
}

function isOptionalPositiveInteger(
  value: unknown,
): value is number | undefined {
  return value === undefined || isPositiveInteger(value)
}

function byteLength(value: string): number {
  return new TextEncoder().encode(value).byteLength
}

function isBoundedTrimmedString(
  value: unknown,
  maximumBytes: number,
): value is string {
  return (
    typeof value === "string" &&
    value !== "" &&
    value === value.trim() &&
    !value.includes("\0") &&
    byteLength(value) <= maximumBytes
  )
}

function isOptionalBoundedString(
  value: unknown,
  maximumBytes: number,
  allowEmpty = false,
): value is string | undefined {
  return (
    value === undefined ||
    (typeof value === "string" &&
      !value.includes("\0") &&
      byteLength(value) <= maximumBytes &&
      value === value.trim() &&
      (allowEmpty || value !== ""))
  )
}

function isOptionalStringList(value: unknown): value is string[] | undefined {
  return (
    value === undefined ||
    (Array.isArray(value) &&
      value.length <= MAX_TEXT_ITEMS &&
      value.every((item) => isBoundedTrimmedString(item, MAX_TEXT_BYTES)))
  )
}

function isTimestamp(value: unknown): value is string {
  return (
    typeof value === "string" &&
    value !== "" &&
    Number.isFinite(Date.parse(value))
  )
}

function isOptionalTimestamp(value: unknown): value is string | undefined {
  return value === undefined || isTimestamp(value)
}

function isAbsoluteHTTPURL(value: unknown): value is string {
  if (
    typeof value !== "string" ||
    value !== value.trim() ||
    byteLength(value) > MAX_URL_BYTES
  ) {
    return false
  }
  try {
    const parsed = new URL(value)
    return (
      (parsed.protocol === "https:" || parsed.protocol === "http:") &&
      parsed.host !== ""
    )
  } catch {
    return false
  }
}

function isOptionalAbsoluteHTTPURL(
  value: unknown,
): value is string | undefined {
  return value === undefined || isAbsoluteHTTPURL(value)
}
