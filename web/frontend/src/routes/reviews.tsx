import { createFileRoute, useLocation } from "@tanstack/react-router"
import { useCallback, useEffect, useMemo } from "react"

import type { ReviewCaseStatus } from "@/api/reviews"
import { ReviewAttentionPoliciesPage } from "@/components/reviews/review-attention-policies-page"
import {
  ReviewsPage,
  type ReviewsRouteSearch,
} from "@/components/reviews/reviews-page"

const reviewCaseIDPattern = /^prc_[0-9a-f]{32}$/
const reviewStatuses = new Set<ReviewCaseStatus>([
  "open",
  "all_dropped",
  "submitting",
  "submission_unknown",
  "submitted",
  "stale",
])

export function normalizeReviewsSearch(
  raw: Record<string, unknown>,
): ReviewsRouteSearch {
  if (Object.hasOwn(raw, "view")) {
    return raw.view === "policies" ? { view: "policies" } : {}
  }
  const selectedCase =
    typeof raw.case === "string" && reviewCaseIDPattern.test(raw.case)
      ? raw.case
      : undefined
  const status =
    typeof raw.status === "string" &&
    reviewStatuses.has(raw.status as ReviewCaseStatus)
      ? (raw.status as ReviewCaseStatus)
      : undefined
  const repository = optionalByteText(raw.repository, 512)
  return {
    ...(selectedCase ? { case: selectedCase } : {}),
    ...(status ? { status } : {}),
    ...(repository ? { repository } : {}),
  }
}

export function reviewsSearchIsCanonical(
  raw: Record<string, unknown>,
  normalized: ReviewsRouteSearch,
): boolean {
  const rawKeys = Object.keys(raw)
  const normalizedKeys = Object.keys(normalized) as Array<
    keyof ReviewsRouteSearch
  >
  return (
    rawKeys.length === normalizedKeys.length &&
    normalizedKeys.every((key) => raw[key] === normalized[key])
  )
}

function ReviewsRoutePage() {
  const locationSearch = useLocation({
    select: (location) => location.search,
  })
  const navigate = Route.useNavigate()
  const search = useMemo(
    () => normalizeReviewsSearch({ ...locationSearch }),
    [locationSearch],
  )

  useEffect(() => {
    if (!reviewsSearchIsCanonical({ ...locationSearch }, search)) {
      void navigate({ search, replace: true })
    }
  }, [locationSearch, navigate, search])

  const changeSearch = useCallback(
    (next: ReviewsRouteSearch, replace = false) => {
      void navigate({ search: next, replace })
    },
    [navigate],
  )

  return search.view === "policies" ? (
    <ReviewAttentionPoliciesPage onShowInbox={() => changeSearch({})} />
  ) : (
    <ReviewsPage search={search} onSearchChange={changeSearch} />
  )
}

export const Route = createFileRoute("/reviews")({
  validateSearch: normalizeReviewsSearch,
  component: ReviewsRoutePage,
})

function optionalByteText(
  value: unknown,
  maximumBytes: number,
): string | undefined {
  if (typeof value !== "string") {
    return undefined
  }
  const normalized = value.trim()
  return normalized !== "" &&
    new TextEncoder().encode(normalized).byteLength <= maximumBytes
    ? normalized
    : undefined
}
