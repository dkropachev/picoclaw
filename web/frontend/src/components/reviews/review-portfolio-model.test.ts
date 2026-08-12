import { describe, expect, it } from "vitest"

import type { EventView } from "@/api/events"
import type { PRDevelopmentCaseSummary } from "@/api/pr-development"
import type { ReviewCase } from "@/api/reviews"
import {
  buildReviewPortfolio,
  externalPullReviews,
  portfolioItemKey,
  reviewProviderStatusTargets,
} from "@/components/reviews/review-portfolio-model"

function reviewCase(overrides: Partial<ReviewCase> = {}): ReviewCase {
  return {
    id: "review-default",
    event_id: "event-default",
    dispatch_id: "dispatch-default",
    run_id: "run-default",
    workflow_ref: "builtin://github-pr-review",
    connector: "primary",
    repository: "octo/api",
    pull_number: 11,
    pull_url: "https://github.com/octo/api/pull/11",
    base_sha: "a".repeat(40),
    head_sha: "b".repeat(40),
    summary: "Review summary",
    tests: [],
    residual_risks: [],
    status: "open",
    version: 1,
    active_findings: 1,
    total_findings: 1,
    created_at: "2026-08-01T10:00:00Z",
    updated_at: "2026-08-01T10:00:00Z",
    ...overrides,
  }
}

function developmentCase(
  overrides: Partial<PRDevelopmentCaseSummary> = {},
): PRDevelopmentCaseSummary {
  return {
    id: "development-default",
    repository: "octo/api",
    pull_number: 11,
    pull_url: "https://github.com/octo/api/pull/11",
    pull_author: "octocat",
    pull_state: "open",
    pull_draft: false,
    pull_merged: false,
    head_repository: "octocat/api",
    head_ref: "fix/review-feedback",
    head_sha: "c".repeat(40),
    review_author: "maintainer",
    submitted_review_state: "changes_requested",
    current_review_state: "changes_requested",
    review_submitted_at: "2026-08-01T10:00:00Z",
    review_url: "https://github.com/octo/api/pull/11#pullrequestreview-1",
    captured_at: "2026-08-01T10:00:01Z",
    attention_required: false,
    ...overrides,
  }
}

function event(overrides: Partial<EventView> = {}): EventView {
  return {
    id: "event-default",
    source: "github",
    connector: "primary",
    type: "pull_request_review.submitted",
    received_at: "2026-08-01T10:00:00Z",
    payload_bytes: 128,
    routing: {
      status: "succeeded",
      available_at: "2026-08-01T10:00:00Z",
      attempts: 1,
      updated_at: "2026-08-01T10:00:00Z",
    },
    ...overrides,
  }
}

describe("buildReviewPortfolio", () => {
  it("groups tracked work per repository and PR while preserving both work roles", () => {
    const oldReview = reviewCase({
      id: "review-11-old",
      summary: "Old review summary",
      updated_at: "2026-08-01T10:00:00Z",
    })
    const latestReview = reviewCase({
      id: "review-11-latest",
      summary: "Merged review summary\nAdditional detail",
      status: "submitted",
      active_findings: 0,
      updated_at: "2026-08-01T10:04:00Z",
    })
    const reviewOnly = reviewCase({
      id: "review-12",
      pull_number: 12,
      pull_url: "https://github.com/octo/api/pull/12",
      summary: "Review-only work",
      updated_at: "2026-08-01T10:05:00Z",
    })
    const closedDevelopment = developmentCase({
      id: "development-11",
      repository: "OCTO/API",
      pull_state: "closed",
      captured_at: "2026-08-01T10:03:00Z",
    })
    const developmentOnly = developmentCase({
      id: "development-13",
      pull_number: 13,
      pull_url: "https://github.com/octo/api/pull/13",
      pull_author: "builder",
      review_author: "reviewer-two",
      attention_required: true,
      captured_at: "2026-08-01T10:06:00Z",
    })
    const otherRepository = developmentCase({
      id: "development-7",
      repository: "octo/web",
      pull_number: 7,
      pull_url: "https://github.com/octo/web/pull/7",
      pull_state: "closed",
      captured_at: "2026-08-01T10:07:00Z",
    })

    const repositories = buildReviewPortfolio(
      [oldReview, latestReview, latestReview, reviewOnly],
      [closedDevelopment, developmentOnly, otherRepository],
    )

    expect(repositories.map((repository) => repository.repository)).toEqual([
      "octo/api",
      "octo/web",
    ])
    expect(repositories[0]).toMatchObject({
      repository: "octo/api",
      pending: 3,
      needsAction: 3,
      closed: 0,
      complete: 0,
      reviewing: 2,
      developing: 2,
      updatedAt: "2026-08-01T10:06:00Z",
    })
    expect(repositories[0].items.map((item) => item.pullNumber)).toEqual([
      13, 12, 11,
    ])

    const combined = repositories[0].items.find(
      (item) => item.pullNumber === 11,
    )
    expect(combined).toMatchObject({
      key: "octo/api#11",
      roles: ["review", "develop"],
      status: "pending",
      needsAction: true,
      title: "Old review summary",
      authors: ["octocat"],
      reviewers: ["maintainer"],
      updatedAt: "2026-08-01T10:04:00Z",
    })
    expect(combined?.reviewCases.map((item) => item.id)).toEqual([
      "review-11-latest",
      "review-11-old",
    ])
    expect(repositories[1]).toMatchObject({
      pending: 0,
      needsAction: 0,
      closed: 1,
      complete: 0,
      reviewing: 0,
      developing: 1,
    })
  })

  it("uses a stable case-insensitive PR identity", () => {
    expect(portfolioItemKey("Octo/Repo", 42)).toBe("octo/repo#42")
  })

  it("keeps finished review work distinct from provider-verified closed PRs", () => {
    const repositories = buildReviewPortfolio(
      [reviewCase({ status: "submitted", active_findings: 0 })],
      [],
    )

    expect(repositories[0]).toMatchObject({
      pending: 0,
      complete: 1,
      closed: 0,
      needsAction: 0,
    })
    expect(repositories[0].items[0]).toMatchObject({
      status: "complete",
      needsAction: false,
    })
  })

  it("keeps actionable review work pending when a development capture says the PR is closed", () => {
    const repositories = buildReviewPortfolio(
      [reviewCase({ status: "open", active_findings: 1 })],
      [developmentCase({ pull_state: "closed" })],
    )

    expect(repositories[0]).toMatchObject({
      pending: 1,
      needsAction: 1,
      closed: 0,
      complete: 0,
    })
    expect(repositories[0].items[0]).toMatchObject({
      roles: ["review", "develop"],
      status: "pending",
      needsAction: true,
    })
  })

  it("lets current provider closure override stale actionable local review work", () => {
    const repositories = buildReviewPortfolio(
      [reviewCase({ status: "open", active_findings: 1 })],
      [developmentCase({ pull_state: "open", attention_required: true })],
      new Map([
        [
          "octo/api#11",
          {
            state: "closed" as const,
            merged: true,
            title: "Current provider title",
            url: "https://github.com/octo/api/pull/11",
            author: "current-author",
            updatedAt: "2026-08-02T10:00:00Z",
          },
        ],
      ]),
    )

    expect(repositories[0]).toMatchObject({
      pending: 0,
      needsAction: 0,
      closed: 1,
      liveClosed: 1,
      capturedClosed: 0,
    })
    expect(repositories[0].items[0]).toMatchObject({
      status: "closed",
      closureSource: "live",
      needsAction: false,
      title: "Current provider title",
      authors: ["current-author", "octocat"],
      updatedAt: "2026-08-02T10:00:00Z",
    })
  })

  it("lets current provider open state override a stale captured closure", () => {
    const repositories = buildReviewPortfolio(
      [reviewCase({ status: "submitted", active_findings: 0 })],
      [developmentCase({ pull_state: "closed" })],
      new Map([
        [
          "octo/api#11",
          {
            state: "open" as const,
            merged: false,
            title: "Reopened pull request",
            url: "https://github.com/octo/api/pull/11",
          },
        ],
      ]),
    )

    expect(repositories[0]).toMatchObject({
      pending: 1,
      closed: 0,
      liveClosed: 0,
      capturedClosed: 0,
    })
    expect(repositories[0].items[0]).toMatchObject({
      status: "pending",
      title: "Reopened pull request",
    })
  })

  it("does not let a newer finished case hide older outstanding review work", () => {
    const repositories = buildReviewPortfolio(
      [
        reviewCase({ id: "older-open" }),
        reviewCase({
          id: "newer-submitted",
          status: "submitted",
          active_findings: 0,
          updated_at: "2026-08-01T11:00:00Z",
        }),
      ],
      [],
    )

    expect(repositories[0].items[0]).toMatchObject({
      status: "pending",
      needsAction: true,
    })
  })

  it("merges repository cards without regard to provider name casing", () => {
    const repositories = buildReviewPortfolio(
      [reviewCase({ repository: "Octo/API", pull_number: 10 })],
      [developmentCase({ repository: "octo/api", pull_number: 11 })],
    )

    expect(repositories).toHaveLength(1)
    expect(repositories[0].items.map((item) => item.pullNumber).sort()).toEqual(
      [10, 11],
    )
  })

  it("selects one newest provider status target per case-insensitive PR", () => {
    expect(
      reviewProviderStatusTargets([
        reviewCase({ id: "old", repository: "Octo/API" }),
        reviewCase({
          id: "new",
          repository: "octo/api",
          updated_at: "2026-08-02T10:00:00Z",
        }),
        reviewCase({ id: "other", pull_number: 12 }),
      ]),
    ).toEqual([
      {
        key: "octo/api#11",
        caseID: "new",
        repository: "octo/api",
        pullNumber: 11,
      },
      {
        key: "octo/api#12",
        caseID: "other",
        repository: "octo/api",
        pullNumber: 12,
      },
    ])
  })
})

describe("externalPullReviews", () => {
  it("includes provider-verified reviews from development captures", () => {
    expect(
      externalPullReviews([], "octo/api", 11, [developmentCase()]),
    ).toEqual([
      {
        id: "development:development-default",
        author: "maintainer",
        state: "changes_requested",
        submittedAt: "2026-08-01T10:00:00Z",
        url: "https://github.com/octo/api/pull/11#pullrequestreview-1",
        connector: "verified development capture",
      },
    ])
  })

  it("does not expose unsafe retained-event links", () => {
    expect(
      externalPullReviews(
        [
          event({
            attributes: {
              repository_full_name: "octo/api",
              pull_request_number: "11",
              review_url: "javascript:alert(1)",
            },
          }),
        ],
        "octo/api",
        11,
      )[0],
    ).not.toHaveProperty("url")
  })

  it("keeps only matching provider reviews, removes replay duplicates, and orders newest first", () => {
    const reviews = externalPullReviews(
      [
        event({
          id: "event-alice",
          attributes: {
            repository_full_name: "OCTO/API",
            pull_request_number: "11",
            review_author: "alice",
            review_state: "approved",
            review_submitted_at: "2026-08-01T10:00:00Z",
            review_url:
              "https://github.com/octo/api/pull/11#pullrequestreview-1",
          },
        }),
        event({
          id: "event-bob",
          actor: { display_name: "bob" },
          connector: "secondary",
          occurred_at: "2026-08-01T11:00:00Z",
          attributes: {
            repository_full_name: "octo/api",
            pull_request_number: "11",
            review_state: "changes_requested",
          },
        }),
        event({
          id: "event-replay",
          attributes: {
            repository_full_name: "octo/api",
            pull_request_number: "11",
            review_author: "alice replay",
            review_state: "approved",
            review_submitted_at: "2026-08-01T10:30:00Z",
            review_url:
              "https://github.com/octo/api/pull/11#pullrequestreview-1",
          },
        }),
        event({
          id: "event-other-pr",
          attributes: {
            repository_full_name: "octo/api",
            pull_request_number: "12",
            review_author: "ignored",
          },
        }),
        event({
          id: "event-other-type",
          type: "pull_request.opened",
          attributes: {
            repository_full_name: "octo/api",
            pull_request_number: "11",
            review_author: "ignored",
          },
        }),
      ],
      "octo/api",
      11,
    )

    expect(reviews).toEqual([
      {
        id: "event-bob",
        author: "bob",
        state: "changes_requested",
        submittedAt: "2026-08-01T11:00:00Z",
        connector: "secondary",
      },
      {
        id: "event-replay",
        author: "alice replay",
        state: "approved",
        submittedAt: "2026-08-01T10:30:00Z",
        url: "https://github.com/octo/api/pull/11#pullrequestreview-1",
        connector: "primary",
      },
    ])
  })
})
