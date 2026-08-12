import type { EventView } from "@/api/events"
import type { PRDevelopmentCaseSummary } from "@/api/pr-development"
import type { ReviewCase, ReviewCaseStatus } from "@/api/reviews"

export type ReviewWorkRole = "review" | "develop"
// `complete` is a finished PicoClaw review workflow. `closed` represents a
// provider-verified PR state; review-role PRs can be enriched with live state,
// while development-only PRs retain their latest captured snapshot.
export type ReviewWorkStatus = "pending" | "complete" | "closed"

export interface LiveReviewPullRequestState {
  state: "open" | "closed"
  merged: boolean
  title: string
  url: string
  author?: string
  updatedAt?: string
}

export interface ReviewProviderStatusTarget {
  key: string
  caseID: string
  repository: string
  pullNumber: number
}

export interface ReviewWorkItem {
  key: string
  repository: string
  pullNumber: number
  pullURL: string
  title: string
  roles: ReviewWorkRole[]
  status: ReviewWorkStatus
  closureSource?: "live" | "captured"
  // Actionable workflow state; the current APIs do not expose viewed/attended state.
  needsAction: boolean
  updatedAt: string
  reviewCases: ReviewCase[]
  developmentCases: PRDevelopmentCaseSummary[]
  authors: string[]
  reviewers: string[]
}

export interface ReviewRepositorySummary {
  repository: string
  items: ReviewWorkItem[]
  pending: number
  needsAction: number
  closed: number
  liveClosed: number
  capturedClosed: number
  complete: number
  reviewing: number
  developing: number
  updatedAt: string
}

export interface ExternalPullReview {
  id: string
  author: string
  state: string
  submittedAt: string
  url?: string
  connector: string
}

const closedReviewStatuses = new Set<ReviewCaseStatus>([
  "all_dropped",
  "submitted",
])

export function buildReviewPortfolio(
  reviewCases: ReviewCase[],
  developmentCases: PRDevelopmentCaseSummary[],
  livePullRequestStates: ReadonlyMap<
    string,
    LiveReviewPullRequestState
  > = new Map(),
): ReviewRepositorySummary[] {
  const groups = new Map<
    string,
    {
      repository: string
      pullNumber: number
      reviewCases: ReviewCase[]
      developmentCases: PRDevelopmentCaseSummary[]
    }
  >()

  for (const reviewCase of deduplicate(reviewCases)) {
    const group = ensureGroup(
      groups,
      reviewCase.repository,
      reviewCase.pull_number,
    )
    group.reviewCases.push(reviewCase)
  }
  for (const developmentCase of deduplicate(developmentCases)) {
    const group = ensureGroup(
      groups,
      developmentCase.repository,
      developmentCase.pull_number,
    )
    group.developmentCases.push(developmentCase)
  }

  const repositories = new Map<
    string,
    { repository: string; items: ReviewWorkItem[] }
  >()
  for (const group of groups.values()) {
    group.reviewCases.sort((left, right) =>
      compareDateDescending(left.updated_at, right.updated_at),
    )
    group.developmentCases.sort((left, right) =>
      compareDateDescending(left.captured_at, right.captured_at),
    )
    const latestReview = group.reviewCases[0]
    const actionableReview = group.reviewCases.find(reviewNeedsAttention)
    const displayedReview = actionableReview ?? latestReview
    const latestDevelopment = group.developmentCases[0]
    const roles: ReviewWorkRole[] = [
      ...(latestReview ? (["review"] as const) : []),
      ...(latestDevelopment ? (["develop"] as const) : []),
    ]
    const reviewWorkPending = group.reviewCases.some(
      (reviewCase) => !closedReviewStatuses.has(reviewCase.status),
    )
    const livePullRequestState = latestReview
      ? livePullRequestStates.get(
          portfolioItemKey(group.repository, group.pullNumber),
        )
      : undefined
    const liveClosed =
      livePullRequestState?.state === "closed" ||
      livePullRequestState?.merged === true
    const capturedClosed = latestDevelopment?.pull_state === "closed"
    const status: ReviewWorkStatus = liveClosed
      ? "closed"
      : livePullRequestState?.state === "open"
        ? reviewWorkPending || latestDevelopment
          ? "pending"
          : "complete"
        : reviewWorkPending
          ? "pending"
          : latestDevelopment
            ? capturedClosed
              ? "closed"
              : "pending"
            : "complete"
    const item: ReviewWorkItem = {
      key: portfolioItemKey(group.repository, group.pullNumber),
      repository: group.repository,
      pullNumber: group.pullNumber,
      pullURL:
        livePullRequestState?.url ??
        displayedReview?.pull_url ??
        latestDevelopment?.pull_url ??
        "",
      title:
        livePullRequestState?.title ||
        firstLine(displayedReview?.summary) ||
        (latestDevelopment
          ? `Feedback from @${latestDevelopment.review_author}`
          : `Pull request #${group.pullNumber}`),
      roles,
      status,
      ...(status === "closed"
        ? {
            closureSource: liveClosed
              ? ("live" as const)
              : ("captured" as const),
          }
        : {}),
      needsAction:
        status !== "closed" &&
        (group.reviewCases.some(reviewNeedsAttention) ||
          group.developmentCases.some((item) => item.attention_required)),
      updatedAt: newestDate(
        livePullRequestState?.updatedAt,
        latestReview?.updated_at,
        latestDevelopment?.captured_at,
      ),
      reviewCases: group.reviewCases,
      developmentCases: group.developmentCases,
      authors: uniqueStrings(
        [
          livePullRequestState?.author,
          ...group.developmentCases.map((item) => item.pull_author),
        ].filter((author): author is string => Boolean(author)),
      ),
      reviewers: uniqueStrings(
        group.developmentCases.map((item) => item.review_author),
      ),
    }
    const repositoryKey = group.repository.toLowerCase()
    const repositoryGroup = repositories.get(repositoryKey) ?? {
      repository: group.repository,
      items: [],
    }
    repositoryGroup.items.push(item)
    repositories.set(repositoryKey, repositoryGroup)
  }

  return [...repositories.values()]
    .map(({ repository, items }): ReviewRepositorySummary => {
      items.sort((left, right) =>
        compareDateDescending(left.updatedAt, right.updatedAt),
      )
      return {
        repository,
        items,
        pending: items.filter((item) => item.status === "pending").length,
        needsAction: items.filter((item) => item.needsAction).length,
        closed: items.filter((item) => item.status === "closed").length,
        liveClosed: items.filter((item) => item.closureSource === "live")
          .length,
        capturedClosed: items.filter(
          (item) => item.closureSource === "captured",
        ).length,
        complete: items.filter((item) => item.status === "complete").length,
        reviewing: items.filter((item) => item.roles.includes("review")).length,
        developing: items.filter((item) => item.roles.includes("develop"))
          .length,
        updatedAt: items[0]?.updatedAt ?? "",
      }
    })
    .sort((left, right) => {
      const attentionDifference = right.needsAction - left.needsAction
      if (attentionDifference !== 0) return attentionDifference
      const pendingDifference = right.pending - left.pending
      if (pendingDifference !== 0) return pendingDifference
      return left.repository.localeCompare(right.repository)
    })
}

/** Pick one current review case per tracked PR for the cheap provider status view. */
export function reviewProviderStatusTargets(
  reviewCases: ReviewCase[],
): ReviewProviderStatusTarget[] {
  const targets = new Map<
    string,
    ReviewProviderStatusTarget & { updatedAt: string }
  >()
  for (const reviewCase of deduplicate(reviewCases)) {
    const key = portfolioItemKey(reviewCase.repository, reviewCase.pull_number)
    const current = targets.get(key)
    if (
      current &&
      compareDateDescending(current.updatedAt, reviewCase.updated_at) <= 0
    ) {
      continue
    }
    targets.set(key, {
      key,
      caseID: reviewCase.id,
      repository: reviewCase.repository,
      pullNumber: reviewCase.pull_number,
      updatedAt: reviewCase.updated_at,
    })
  }
  return [...targets.values()]
    .sort((left, right) => left.key.localeCompare(right.key))
    .map(
      (target): ReviewProviderStatusTarget => ({
        key: target.key,
        caseID: target.caseID,
        repository: target.repository,
        pullNumber: target.pullNumber,
      }),
    )
}

export function externalPullReviews(
  events: EventView[],
  repository: string,
  pullNumber: number,
  developmentCases: PRDevelopmentCaseSummary[] = [],
): ExternalPullReview[] {
  const seen = new Set<string>()
  const capturedReviews = developmentCases
    .filter(
      (developmentCase) =>
        developmentCase.repository.toLowerCase() === repository.toLowerCase() &&
        developmentCase.pull_number === pullNumber,
    )
    .map(
      (developmentCase): ExternalPullReview => ({
        id: `development:${developmentCase.id}`,
        author: developmentCase.review_author,
        state: developmentCase.current_review_state,
        submittedAt: developmentCase.review_submitted_at,
        url: developmentCase.review_url,
        connector: "verified development capture",
      }),
    )
  const retainedEventReviews = events
    .filter((event) => {
      const attributes = event.attributes
      return (
        event.type === "pull_request_review.submitted" &&
        attributes?.repository_full_name?.toLowerCase() ===
          repository.toLowerCase() &&
        attributes.pull_request_number === String(pullNumber)
      )
    })
    .map((event): ExternalPullReview => {
      const attributes = event.attributes ?? {}
      const reviewURL = safeExternalURL(attributes.review_url)
      return {
        id: event.id,
        author:
          attributes.review_author ||
          event.actor?.display_name ||
          event.actor?.id ||
          "Unknown reviewer",
        state: attributes.review_state || "commented",
        submittedAt:
          attributes.review_submitted_at ||
          event.occurred_at ||
          event.received_at,
        ...(reviewURL ? { url: reviewURL } : {}),
        connector: event.connector,
      }
    })
    .sort((left, right) =>
      compareDateDescending(left.submittedAt, right.submittedAt),
    )
  return [...capturedReviews, ...retainedEventReviews]
    .filter((review) => {
      const identity = review.url || review.id
      if (seen.has(identity)) return false
      seen.add(identity)
      return true
    })
    .sort((left, right) =>
      compareDateDescending(left.submittedAt, right.submittedAt),
    )
}

export function portfolioItemKey(
  repository: string,
  pullNumber: number,
): string {
  return `${repository.toLowerCase()}#${pullNumber}`
}

function ensureGroup(
  groups: Map<
    string,
    {
      repository: string
      pullNumber: number
      reviewCases: ReviewCase[]
      developmentCases: PRDevelopmentCaseSummary[]
    }
  >,
  repository: string,
  pullNumber: number,
) {
  const key = portfolioItemKey(repository, pullNumber)
  let group = groups.get(key)
  if (!group) {
    group = {
      repository,
      pullNumber,
      reviewCases: [],
      developmentCases: [],
    }
    groups.set(key, group)
  }
  return group
}

function deduplicate<T extends { id: string }>(items: T[]): T[] {
  return [...new Map(items.map((item) => [item.id, item])).values()]
}

function reviewNeedsAttention(reviewCase: ReviewCase | undefined): boolean {
  return reviewCase != null && !closedReviewStatuses.has(reviewCase.status)
}

function newestDate(...values: Array<string | undefined>): string {
  return (
    values
      .filter((value): value is string => Boolean(value))
      .sort(compareDateDescending)[0] ?? ""
  )
}

function compareDateDescending(left: string, right: string): number {
  const leftTime = Date.parse(left)
  const rightTime = Date.parse(right)
  if (!Number.isNaN(leftTime) && !Number.isNaN(rightTime)) {
    return rightTime - leftTime
  }
  return right.localeCompare(left)
}

function uniqueStrings(values: string[]): string[] {
  return [...new Set(values.filter(Boolean))]
}

function firstLine(value: string | undefined): string {
  return value?.split(/\r?\n/, 1)[0]?.trim() ?? ""
}

function safeExternalURL(value: string | undefined): string | undefined {
  if (!value) return undefined
  try {
    const url = new URL(value)
    return url.protocol === "https:" || url.protocol === "http:"
      ? url.toString()
      : undefined
  } catch {
    return undefined
  }
}
