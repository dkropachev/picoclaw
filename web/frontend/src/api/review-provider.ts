import { launcherFetch } from "@/api/http"
import {
  type ExactJSONObject,
  type ExactJSONValue,
  isExactJSONNumber,
  isExactJSONObject,
  parseExactJSON,
} from "@/api/review-attention-json"

export type ReviewProviderAvailability =
  | "available"
  | "partial"
  | "unavailable"
  | "incompatible"

export type ReviewProviderPullState = "open" | "closed"
export type ReviewProviderThreadAction = "resolve" | "unresolve"

export interface ReviewProviderPullRequest {
  number: number
  title: string
  state: ReviewProviderPullState
  url: string
  author?: string
  draft: boolean
  merged: boolean
  updated_at?: string
}

export interface ReviewProviderCapabilities {
  thread_resolution: boolean
}

export interface ReviewProviderReview {
  id: string
  state: string
  body?: string
  url?: string
  author?: string
  commit_id?: string
  submitted_at?: string
}

export interface ReviewProviderThreadComment {
  body?: string
  path?: string
  line?: number
  author?: string
  created_at?: string
  updated_at?: string
  url?: string
}

export interface ReviewProviderThread {
  token?: string
  is_resolved: boolean
  is_outdated: boolean
  is_collapsed: boolean
  can_resolve: boolean
  total_count: number
  comments: ReviewProviderThreadComment[]
}

export interface ReviewProviderSnapshot {
  availability: ReviewProviderAvailability
  connector: string
  repository: string
  pull_number: number
  pull_request?: ReviewProviderPullRequest
  capabilities: ReviewProviderCapabilities
  reviews: ReviewProviderReview[]
  review_history_complete: boolean
  threads_complete: boolean
  limitations: string[]
  threads: ReviewProviderThread[]
}

export interface ReviewProviderStatus {
  availability: ReviewProviderAvailability
  connector: string
  repository: string
  pull_number: number
  pull_request?: ReviewProviderPullRequest
  capabilities: ReviewProviderCapabilities
  limitations: string[]
}

export class ReviewProviderAPIError extends Error {
  readonly status: number
  readonly code: string

  constructor(code: string, status: number) {
    super(code)
    this.name = "ReviewProviderAPIError"
    this.status = status
    this.code = code
  }
}

const snapshotKeys = new Set([
  "availability",
  "connector",
  "repository",
  "pull_number",
  "pull_request",
  "capabilities",
  "reviews",
  "review_history_complete",
  "threads_complete",
  "limitations",
  "threads",
])
const pullRequestKeys = new Set([
  "number",
  "title",
  "state",
  "url",
  "author",
  "draft",
  "merged",
  "updated_at",
])
const capabilitiesKeys = new Set(["thread_resolution"])
const reviewKeys = new Set([
  "id",
  "state",
  "body",
  "url",
  "author",
  "commit_id",
  "submitted_at",
])
const threadKeys = new Set([
  "token",
  "is_resolved",
  "is_outdated",
  "is_collapsed",
  "can_resolve",
  "total_count",
  "comments",
])
const commentKeys = new Set([
  "body",
  "path",
  "line",
  "author",
  "created_at",
  "updated_at",
  "url",
])
const availabilityValues = new Set<ReviewProviderAvailability>([
  "available",
  "partial",
  "unavailable",
  "incompatible",
])
const pullStateValues = new Set<ReviewProviderPullState>(["open", "closed"])
const threadTokenPattern = /^rtt_[A-Za-z0-9_-]{43}$/
const maximumEnvelopeBytes = 8 << 20
const maximumErrorBytes = 64 << 10
const maximumReviews = 500
const maximumThreads = 500
const maximumCommentsPerThread = 500
const maximumAggregateComments = 5_000
const maximumTextBytes = 64 << 10
const maximumURLBytes = 4 << 10
const maximumPathBytes = 4 << 10
const maximumIdentityBytes = 1 << 10
const maximumLimitations = 64
const maximumLimitationBytes = 4 << 10

export async function getReviewProviderSnapshot(
  caseID: string,
  signal?: AbortSignal,
): Promise<ReviewProviderSnapshot> {
  return requestSnapshot(
    `/api/reviews/${encodeURIComponent(caseID)}/provider`,
    undefined,
    signal,
  )
}

export async function getReviewProviderStatus(
  caseID: string,
  signal?: AbortSignal,
): Promise<ReviewProviderStatus> {
  return requestStatus(
    `/api/reviews/${encodeURIComponent(caseID)}/provider?view=status`,
    signal,
  )
}

export async function mutateReviewProviderThread(
  caseID: string,
  token: string,
  action: ReviewProviderThreadAction,
  signal?: AbortSignal,
): Promise<ReviewProviderSnapshot> {
  if (!threadTokenPattern.test(token) || !isThreadAction(action)) {
    throw new ReviewProviderAPIError("invalid_provider_thread_action", 400)
  }
  return requestSnapshot(
    `/api/reviews/${encodeURIComponent(caseID)}/provider/thread`,
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ token, action }),
    },
    signal,
  )
}

async function requestSnapshot(
  path: string,
  init?: RequestInit,
  signal?: AbortSignal,
): Promise<ReviewProviderSnapshot> {
  const response = await launcherFetch(path, {
    ...init,
    ...(signal ? { signal } : {}),
  })
  const source = await response.text()
  if (
    byteLength(source) >
    (response.ok ? maximumEnvelopeBytes : maximumErrorBytes)
  ) {
    throw response.ok
      ? malformedResponse()
      : new ReviewProviderAPIError(
          "provider_snapshot_unavailable",
          response.status,
        )
  }
  if (!response.ok) {
    throw parseError(source, response.status)
  }
  try {
    return parseSnapshot(source)
  } catch {
    throw malformedResponse()
  }
}

async function requestStatus(
  path: string,
  signal?: AbortSignal,
): Promise<ReviewProviderStatus> {
  const response = await launcherFetch(path, signal ? { signal } : undefined)
  const source = await response.text()
  if (
    byteLength(source) >
    (response.ok ? maximumEnvelopeBytes : maximumErrorBytes)
  ) {
    throw response.ok
      ? malformedResponse()
      : new ReviewProviderAPIError(
          "provider_snapshot_unavailable",
          response.status,
        )
  }
  if (!response.ok) throw parseError(source, response.status)
  try {
    return parseStatus(source)
  } catch {
    throw malformedResponse()
  }
}

function parseStatus(source: string): ReviewProviderStatus {
  const value = parseExactJSON(source, {
    maximumBytes: maximumEnvelopeBytes,
    maximumDepth: 6,
    maximumNodes: 128,
  })
  const statusKeys = new Set([
    "availability",
    "connector",
    "repository",
    "pull_number",
    "pull_request",
    "capabilities",
    "limitations",
  ])
  if (
    !isExactJSONObject(value) ||
    !onlyKeys(value, statusKeys) ||
    !isSetMember(value.availability, availabilityValues) ||
    !isBoundedIdentity(value.connector) ||
    !isBoundedIdentity(value.repository) ||
    !isExactJSONObject(value.capabilities) ||
    !onlyKeys(value.capabilities, capabilitiesKeys) ||
    value.capabilities.thread_resolution !== false ||
    !isStringList(
      value.limitations,
      maximumLimitations,
      maximumLimitationBytes,
    ) ||
    value.limitations.filter((limitation) => limitation === "status_view")
      .length !== 1
  ) {
    throw new TypeError("invalid provider status")
  }
  const pullNumber = exactPositiveSafeInteger(value.pull_number)
  if (pullNumber === undefined) throw new TypeError("invalid provider status")
  const pullRequest =
    value.pull_request === undefined
      ? undefined
      : parsePullRequest(value.pull_request, pullNumber)
  return {
    availability: value.availability,
    connector: value.connector,
    repository: value.repository,
    pull_number: pullNumber,
    ...(pullRequest === undefined ? {} : { pull_request: pullRequest }),
    capabilities: { thread_resolution: false },
    limitations: [...value.limitations],
  }
}

function parseSnapshot(source: string): ReviewProviderSnapshot {
  const value = parseExactJSON(source, {
    maximumBytes: maximumEnvelopeBytes,
    maximumDepth: 12,
    maximumNodes:
      maximumReviews * 12 +
      maximumThreads * maximumCommentsPerThread * 10 +
      1_000,
  })
  if (
    !isExactJSONObject(value) ||
    !onlyKeys(value, snapshotKeys) ||
    !isSetMember(value.availability, availabilityValues) ||
    !isBoundedIdentity(value.connector) ||
    !isBoundedIdentity(value.repository) ||
    !isBoolean(value.review_history_complete) ||
    !isBoolean(value.threads_complete) ||
    !Array.isArray(value.reviews) ||
    value.reviews.length > maximumReviews ||
    !Array.isArray(value.threads) ||
    value.threads.length > maximumThreads ||
    !isStringList(
      value.limitations,
      maximumLimitations,
      maximumLimitationBytes,
    ) ||
    !isExactJSONObject(value.capabilities) ||
    !onlyKeys(value.capabilities, capabilitiesKeys) ||
    !isBoolean(value.capabilities.thread_resolution)
  ) {
    throw new TypeError("invalid provider snapshot")
  }
  const pullNumber = exactPositiveSafeInteger(value.pull_number)
  if (pullNumber === undefined) {
    throw new TypeError("invalid provider pull number")
  }
  const pullRequest =
    value.pull_request === undefined
      ? undefined
      : parsePullRequest(value.pull_request, pullNumber)
  const reviews = value.reviews.map(parseReview)
  const reviewIDs = new Set<string>()
  for (const review of reviews) {
    if (reviewIDs.has(review.id)) {
      throw new TypeError("duplicate provider review")
    }
    reviewIDs.add(review.id)
  }
  const threads = value.threads.map(parseThread)
  const threadTokens = new Set<string>()
  let aggregateComments = 0
  for (const thread of threads) {
    aggregateComments += thread.comments.length
    if (
      aggregateComments > maximumAggregateComments ||
      (thread.can_resolve &&
        (!value.capabilities.thread_resolution ||
          thread.token === undefined)) ||
      (thread.token !== undefined && threadTokens.has(thread.token))
    ) {
      throw new TypeError("invalid provider thread authority")
    }
    if (thread.token !== undefined) threadTokens.add(thread.token)
  }
  if (
    value.availability === "available" &&
    (!value.review_history_complete || !value.threads_complete)
  ) {
    throw new TypeError("available provider snapshot is incomplete")
  }
  return {
    availability: value.availability,
    connector: value.connector,
    repository: value.repository,
    pull_number: pullNumber,
    ...(pullRequest === undefined ? {} : { pull_request: pullRequest }),
    capabilities: {
      thread_resolution: value.capabilities.thread_resolution,
    },
    reviews,
    review_history_complete: value.review_history_complete,
    threads_complete: value.threads_complete,
    limitations: [...value.limitations],
    threads,
  }
}

function parsePullRequest(
  value: ExactJSONValue,
  expectedNumber: number,
): ReviewProviderPullRequest {
  if (
    !isExactJSONObject(value) ||
    !onlyKeys(value, pullRequestKeys) ||
    !isBoundedText(value.title, maximumTextBytes) ||
    !isSetMember(value.state, pullStateValues) ||
    !isAbsoluteHTTPURL(value.url) ||
    !isOptionalBoundedIdentity(value.author) ||
    !isBoolean(value.draft) ||
    !isBoolean(value.merged) ||
    !isOptionalTimestamp(value.updated_at)
  ) {
    throw new TypeError("invalid provider pull request")
  }
  const number = exactPositiveSafeInteger(value.number)
  if (number !== expectedNumber || (value.merged && value.state !== "closed")) {
    throw new TypeError("inconsistent provider pull request")
  }
  return {
    number,
    title: value.title,
    state: value.state,
    url: value.url,
    ...(value.author === undefined ? {} : { author: value.author }),
    draft: value.draft,
    merged: value.merged,
    ...(value.updated_at === undefined ? {} : { updated_at: value.updated_at }),
  }
}

function parseReview(value: ExactJSONValue): ReviewProviderReview {
  if (
    !isExactJSONObject(value) ||
    !onlyKeys(value, reviewKeys) ||
    !isBoundedIdentity(value.id) ||
    !isBoundedIdentity(value.state) ||
    !isOptionalBoundedText(value.body, maximumTextBytes) ||
    !isOptionalAbsoluteHTTPURL(value.url) ||
    !isOptionalBoundedIdentity(value.author) ||
    !isOptionalBoundedIdentity(value.commit_id) ||
    !isOptionalTimestamp(value.submitted_at)
  ) {
    throw new TypeError("invalid provider review")
  }
  return {
    id: value.id,
    state: value.state,
    ...(value.body === undefined ? {} : { body: value.body }),
    ...(value.url === undefined ? {} : { url: value.url }),
    ...(value.author === undefined ? {} : { author: value.author }),
    ...(value.commit_id === undefined ? {} : { commit_id: value.commit_id }),
    ...(value.submitted_at === undefined
      ? {}
      : { submitted_at: value.submitted_at }),
  }
}

function parseThread(value: ExactJSONValue): ReviewProviderThread {
  if (
    !isExactJSONObject(value) ||
    !onlyKeys(value, threadKeys) ||
    !isOptionalThreadToken(value.token) ||
    !isBoolean(value.is_resolved) ||
    !isBoolean(value.is_outdated) ||
    !isBoolean(value.is_collapsed) ||
    !isBoolean(value.can_resolve) ||
    !Array.isArray(value.comments) ||
    value.comments.length > maximumCommentsPerThread
  ) {
    throw new TypeError("invalid provider thread")
  }
  const totalCount = exactNonNegativeSafeInteger(value.total_count)
  if (totalCount === undefined || totalCount < value.comments.length) {
    throw new TypeError("invalid provider thread count")
  }
  return {
    ...(value.token === undefined ? {} : { token: value.token }),
    is_resolved: value.is_resolved,
    is_outdated: value.is_outdated,
    is_collapsed: value.is_collapsed,
    can_resolve: value.can_resolve,
    total_count: totalCount,
    comments: value.comments.map(parseComment),
  }
}

function parseComment(value: ExactJSONValue): ReviewProviderThreadComment {
  if (
    !isExactJSONObject(value) ||
    !onlyKeys(value, commentKeys) ||
    !isOptionalBoundedText(value.body, maximumTextBytes) ||
    !isOptionalBoundedText(value.path, maximumPathBytes) ||
    !isOptionalBoundedIdentity(value.author) ||
    !isOptionalTimestamp(value.created_at) ||
    !isOptionalTimestamp(value.updated_at) ||
    !isOptionalAbsoluteHTTPURL(value.url)
  ) {
    throw new TypeError("invalid provider thread comment")
  }
  const line = exactOptionalPositiveSafeInteger(value.line)
  if (value.line !== undefined && line === undefined) {
    throw new TypeError("invalid provider comment line")
  }
  return {
    ...(value.body === undefined ? {} : { body: value.body }),
    ...(value.path === undefined ? {} : { path: value.path }),
    ...(line === undefined ? {} : { line }),
    ...(value.author === undefined ? {} : { author: value.author }),
    ...(value.created_at === undefined ? {} : { created_at: value.created_at }),
    ...(value.updated_at === undefined ? {} : { updated_at: value.updated_at }),
    ...(value.url === undefined ? {} : { url: value.url }),
  }
}

function parseError(source: string, status: number): ReviewProviderAPIError {
  try {
    const value = parseExactJSON(source, {
      maximumBytes: maximumErrorBytes,
      maximumDepth: 3,
      maximumNodes: 8,
    })
    if (
      isExactJSONObject(value) &&
      onlyKeys(value, new Set(["error"])) &&
      isBoundedIdentity(value.error)
    ) {
      return new ReviewProviderAPIError(value.error, status)
    }
  } catch {
    // Use one fixed, non-provider-controlled fallback below.
  }
  return new ReviewProviderAPIError("provider_snapshot_unavailable", status)
}

function malformedResponse(): ReviewProviderAPIError {
  return new ReviewProviderAPIError("invalid_provider_snapshot", 502)
}

function exactPositiveSafeInteger(value: ExactJSONValue | undefined) {
  const number = exactNonNegativeSafeInteger(value)
  return number !== undefined && number > 0 ? number : undefined
}

function exactOptionalPositiveSafeInteger(value: ExactJSONValue | undefined) {
  return value === undefined ? undefined : exactPositiveSafeInteger(value)
}

function exactNonNegativeSafeInteger(value: ExactJSONValue | undefined) {
  if (!isExactJSONNumber(value) || !/^(?:0|[1-9]\d*)$/.test(value.source)) {
    return undefined
  }
  const parsed = Number(value.source)
  return Number.isSafeInteger(parsed) ? parsed : undefined
}

function isThreadAction(value: string): value is ReviewProviderThreadAction {
  return value === "resolve" || value === "unresolve"
}

function isOptionalThreadToken(
  value: ExactJSONValue | undefined,
): value is string | undefined {
  return (
    value === undefined ||
    (typeof value === "string" && threadTokenPattern.test(value))
  )
}

function isSetMember<T extends string>(
  value: ExactJSONValue | undefined,
  values: ReadonlySet<T>,
): value is T {
  return typeof value === "string" && values.has(value as T)
}

function isBoolean(value: ExactJSONValue | undefined): value is boolean {
  return typeof value === "boolean"
}

function isStringList(
  value: ExactJSONValue | undefined,
  maximumItems: number,
  maximumItemBytes: number,
): value is string[] {
  return (
    Array.isArray(value) &&
    value.length <= maximumItems &&
    value.every((item) => isBoundedIdentity(item, maximumItemBytes))
  )
}

function isOptionalBoundedIdentity(
  value: ExactJSONValue | undefined,
): value is string | undefined {
  return value === undefined || isBoundedIdentity(value)
}

function isBoundedIdentity(
  value: ExactJSONValue | undefined,
  maximumBytes = maximumIdentityBytes,
): value is string {
  return (
    typeof value === "string" &&
    value !== "" &&
    value === value.trim() &&
    !value.includes("\0") &&
    byteLength(value) <= maximumBytes
  )
}

function isOptionalBoundedText(
  value: ExactJSONValue | undefined,
  maximumBytes: number,
): value is string | undefined {
  return value === undefined || isBoundedText(value, maximumBytes)
}

function isBoundedText(
  value: ExactJSONValue | undefined,
  maximumBytes: number,
): value is string {
  return (
    typeof value === "string" &&
    !value.includes("\0") &&
    byteLength(value) <= maximumBytes
  )
}

function isOptionalTimestamp(
  value: ExactJSONValue | undefined,
): value is string | undefined {
  return value === undefined || isTimestamp(value)
}

function isTimestamp(value: ExactJSONValue | undefined): value is string {
  return (
    typeof value === "string" &&
    value.length <= 64 &&
    /^\d{4}-\d{2}-\d{2}T/.test(value) &&
    !Number.isNaN(Date.parse(value))
  )
}

function isOptionalAbsoluteHTTPURL(
  value: ExactJSONValue | undefined,
): value is string | undefined {
  return value === undefined || isAbsoluteHTTPURL(value)
}

function isAbsoluteHTTPURL(value: ExactJSONValue | undefined): value is string {
  if (typeof value !== "string" || byteLength(value) > maximumURLBytes) {
    return false
  }
  try {
    const url = new URL(value)
    return url.protocol === "http:" || url.protocol === "https:"
  } catch {
    return false
  }
}

function onlyKeys(
  value: ExactJSONObject,
  allowed: ReadonlySet<string>,
): boolean {
  return Object.keys(value).every((key) => allowed.has(key))
}

function byteLength(value: string): number {
  return new TextEncoder().encode(value).byteLength
}
