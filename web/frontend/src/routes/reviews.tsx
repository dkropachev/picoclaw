import { createFileRoute, useLocation } from "@tanstack/react-router"
import { useCallback, useEffect, useMemo } from "react"

import type { ReviewCaseStatus } from "@/api/reviews"
import { PRDevelopmentPage } from "@/components/reviews/pr-development-page"
import { ReviewAttentionPoliciesPage } from "@/components/reviews/review-attention-policies-page"
import {
  ReviewsPage,
  type ReviewsRouteSearch,
} from "@/components/reviews/reviews-page"

const reviewCaseIDPattern = /^prc_[0-9a-f]{32}$/
const developmentCaseIDPattern = /^pdc_[0-9a-f]{32}$/
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
    if (raw.view === "policies") return { view: "policies" }
    if (raw.view === "development") {
      const selectedCase =
        typeof raw.case === "string" && developmentCaseIDPattern.test(raw.case)
          ? raw.case
          : undefined
      const repository = optionalRepository(raw.repository)
      const pullNumber = optionalPullNumber(raw.pull_number)
      return {
        view: "development",
        ...(selectedCase ? { case: selectedCase } : {}),
        ...(repository ? { repository } : {}),
        ...(pullNumber ? { pull_number: pullNumber } : {}),
      }
    }
    return {}
  }
  const selectedCase =
    typeof raw.case === "string" && reviewCaseIDPattern.test(raw.case)
      ? raw.case
      : undefined
  if (selectedCase && raw.focus === "chat") {
    return { case: selectedCase, focus: "chat" }
  }
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
  const locationHash = useLocation({
    select: (location) => location.hash,
  })
  const navigate = Route.useNavigate()
  const search = useMemo(
    () => normalizeReviewsSearch({ ...locationSearch }),
    [locationSearch],
  )

  useEffect(() => {
    if (
      locationHash !== "" ||
      !reviewsSearchIsCanonical({ ...locationSearch }, search)
    ) {
      void navigate({ search, hash: "", replace: true })
    }
  }, [locationHash, locationSearch, navigate, search])

  const changeSearch = useCallback(
    (next: ReviewsRouteSearch, replace = false) => {
      void navigate({ search: next, hash: "", replace })
    },
    [navigate],
  )

  if (search.view === "policies") {
    return (
      <ReviewAttentionPoliciesPage
        onShowInbox={() => changeSearch({})}
        onShowDevelopment={() => changeSearch({ view: "development" })}
      />
    )
  }
  if (search.view === "development") {
    return (
      <PRDevelopmentPage
        search={{ ...search, view: "development" }}
        onSearchChange={changeSearch}
      />
    )
  }
  return <ReviewsPage search={search} onSearchChange={changeSearch} />
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

function optionalRepository(value: unknown): string | undefined {
  const repository = optionalByteText(value, 256)
  return repository != null &&
    /^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/.test(repository)
    ? repository
    : undefined
}

function optionalPullNumber(value: unknown): number | undefined {
  if (typeof value === "number") {
    return Number.isInteger(value) && value >= 1 && value <= 2_147_483_647
      ? value
      : undefined
  }
  if (typeof value !== "string" || !/^[1-9][0-9]*$/.test(value)) {
    return undefined
  }
  const pullNumber = Number(value)
  return Number.isInteger(pullNumber) && pullNumber <= 2_147_483_647
    ? pullNumber
    : undefined
}
