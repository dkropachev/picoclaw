import { createFileRoute, useLocation } from "@tanstack/react-router"
import { useCallback, useEffect, useMemo } from "react"

import { PRDevelopmentPage } from "@/components/reviews/pr-development-page"
import { ReviewAttentionPoliciesPage } from "@/components/reviews/review-attention-policies-page"
import {
  ReviewPortfolioPage,
  type ReviewPortfolioSearch,
} from "@/components/reviews/review-portfolio-page"
import {
  ReviewsPage,
  type ReviewsRouteSearch,
} from "@/components/reviews/reviews-page"

const reviewCaseIDPattern = /^prc_[0-9a-f]{32}$/
const developmentCaseIDPattern = /^pdc_[0-9a-f]{32}$/
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
      if (selectedCase && raw.focus === "chat") {
        return { view: "development", case: selectedCase, focus: "chat" }
      }
      const repository = optionalRepository(raw.repository)
      const pullNumber = optionalPullNumber(raw.pull_number)
      return {
        view: "development",
        ...(selectedCase ? { case: selectedCase } : {}),
        ...(repository ? { repository } : {}),
        ...(pullNumber ? { pull_number: pullNumber } : {}),
      }
    }
    if (raw.view === "review") {
      const selectedCase =
        typeof raw.case === "string" && reviewCaseIDPattern.test(raw.case)
          ? raw.case
          : undefined
      const repository = optionalRepository(raw.repo)
      const pullNumber = optionalPullNumber(raw.pr)
      const filter = repository ? optionalByteText(raw.filter, 2048) : undefined
      const requestedRole = repository
        ? optionalPortfolioRole(raw.role)
        : undefined
      if (!selectedCase) {
        return {
          ...(repository ? { repo: repository } : {}),
          ...(repository && pullNumber ? { pr: pullNumber } : {}),
          ...(filter ? { filter } : {}),
          ...(repository && pullNumber && requestedRole
            ? { role: requestedRole }
            : {}),
        }
      }
      if (raw.focus === "chat") {
        return {
          view: "review",
          case: selectedCase,
          focus: "chat",
          ...(repository ? { repo: repository } : {}),
          ...(repository && pullNumber ? { pr: pullNumber } : {}),
          ...(filter ? { filter } : {}),
          ...(repository && pullNumber ? { role: "review" as const } : {}),
        }
      }
      return {
        view: "review",
        ...(selectedCase ? { case: selectedCase } : {}),
        ...(repository ? { repo: repository } : {}),
        ...(repository && pullNumber ? { pr: pullNumber } : {}),
        ...(filter ? { filter } : {}),
        ...(repository && pullNumber ? { role: "review" as const } : {}),
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
  const legacyStatus = optionalLegacyReviewStatus(raw.status)
  const legacyRepository = optionalByteText(raw.repository, 512)
  if (selectedCase || legacyStatus || legacyRepository) {
    return {
      ...(selectedCase ? { case: selectedCase } : {}),
      ...(legacyStatus ? { status: legacyStatus } : {}),
      ...(legacyRepository ? { repository: legacyRepository } : {}),
    }
  }
  const repository = optionalRepository(raw.repo)
  const pullNumber = optionalPullNumber(raw.pr)
  const filter = optionalByteText(raw.filter, 2048)
  const role =
    repository && pullNumber ? optionalPortfolioRole(raw.role) : undefined
  const selectedPortfolioReviewCase =
    repository &&
    pullNumber &&
    typeof raw.review_case === "string" &&
    reviewCaseIDPattern.test(raw.review_case)
      ? raw.review_case
      : undefined
  return {
    ...(repository ? { repo: repository } : {}),
    ...(repository && pullNumber ? { pr: pullNumber } : {}),
    ...(repository && filter ? { filter } : {}),
    ...(role ? { role } : {}),
    ...(selectedPortfolioReviewCase
      ? { review_case: selectedPortfolioReviewCase }
      : {}),
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
        standalone
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
  if (
    search.view === "review" ||
    search.case != null ||
    search.status != null ||
    search.repository != null
  ) {
    return <ReviewsPage search={search} onSearchChange={changeSearch} />
  }
  return (
    <ReviewPortfolioPage
      search={search as ReviewPortfolioSearch}
      onSearchChange={changeSearch}
      onOpenReview={(caseID, repository, pullNumber) =>
        changeSearch({
          view: "review",
          case: caseID,
          repo: repository,
          pr: pullNumber,
          ...(search.filter ? { filter: search.filter } : {}),
          role: "review",
        })
      }
    />
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

function optionalLegacyReviewStatus(
  value: unknown,
): ReviewsRouteSearch["status"] {
  return value === "open" ||
    value === "all_dropped" ||
    value === "submitting" ||
    value === "submission_unknown" ||
    value === "submitted" ||
    value === "stale"
    ? value
    : undefined
}

function optionalPortfolioRole(value: unknown): ReviewsRouteSearch["role"] {
  return value === "review" || value === "develop" ? value : undefined
}
