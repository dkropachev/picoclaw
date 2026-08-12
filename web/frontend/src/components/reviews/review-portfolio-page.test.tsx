import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, waitFor, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { useState } from "react"
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest"

import { type EventView, listEvents } from "@/api/events"
import {
  type PRDevelopmentCaseSummary,
  listPRDevelopmentCases,
} from "@/api/pr-development"
import {
  type ReviewProviderSnapshot,
  type ReviewProviderStatus,
  getReviewProviderSnapshot,
  getReviewProviderStatus,
  mutateReviewProviderThread,
} from "@/api/review-provider"
import { type ReviewCase, listReviews } from "@/api/reviews"
import {
  ReviewPortfolioPage,
  type ReviewPortfolioSearch,
} from "@/components/reviews/review-portfolio-page"
import { SidebarProvider } from "@/components/ui/sidebar"

vi.mock("@/api/events", async (importOriginal) => {
  const original = await importOriginal<typeof import("@/api/events")>()
  return { ...original, listEvents: vi.fn() }
})

vi.mock("@/api/pr-development", async (importOriginal) => {
  const original = await importOriginal<typeof import("@/api/pr-development")>()
  return { ...original, listPRDevelopmentCases: vi.fn() }
})

vi.mock("@/api/review-provider", async (importOriginal) => {
  const original =
    await importOriginal<typeof import("@/api/review-provider")>()
  return {
    ...original,
    getReviewProviderSnapshot: vi.fn(),
    getReviewProviderStatus: vi.fn(),
    mutateReviewProviderThread: vi.fn(),
  }
})

vi.mock("@/api/reviews", async (importOriginal) => {
  const original = await importOriginal<typeof import("@/api/reviews")>()
  return { ...original, listReviews: vi.fn() }
})

function reviewCase(overrides: Partial<ReviewCase> = {}): ReviewCase {
  return {
    id: "review-default",
    event_id: "event-default",
    dispatch_id: "dispatch-default",
    run_id: "run-default",
    workflow_ref: "builtin://github-pr-review",
    connector: "primary",
    repository: "octo/api",
    pull_number: 41,
    pull_url: "https://github.com/octo/api/pull/41",
    base_sha: "a".repeat(40),
    head_sha: "b".repeat(40),
    summary: "Review-only correctness issue",
    tests: ["pnpm test"],
    residual_risks: [],
    status: "open",
    version: 1,
    active_findings: 1,
    total_findings: 2,
    created_at: "2026-08-01T10:00:00Z",
    updated_at: "2026-08-01T10:01:00Z",
    ...overrides,
  }
}

function developmentCase(
  overrides: Partial<PRDevelopmentCaseSummary> = {},
): PRDevelopmentCaseSummary {
  return {
    id: "development-default",
    repository: "octo/api",
    pull_number: 42,
    pull_url: "https://github.com/octo/api/pull/42",
    pull_author: "builder",
    pull_state: "open",
    pull_draft: false,
    pull_merged: false,
    head_repository: "builder/api",
    head_ref: "fix/review-feedback",
    head_sha: "c".repeat(40),
    review_author: "maintainer",
    submitted_review_state: "changes_requested",
    current_review_state: "changes_requested",
    review_submitted_at: "2026-08-01T10:02:00Z",
    review_url: "https://github.com/octo/api/pull/42#pullrequestreview-2",
    captured_at: "2026-08-01T10:03:00Z",
    attention_required: true,
    ...overrides,
  }
}

function reviewEvent(overrides: Partial<EventView> = {}): EventView {
  return {
    id: "event-provider-review",
    source: "github",
    connector: "primary",
    type: "pull_request_review.submitted",
    received_at: "2026-08-01T11:00:00Z",
    payload_bytes: 128,
    routing: {
      status: "succeeded",
      available_at: "2026-08-01T11:00:00Z",
      attempts: 1,
      updated_at: "2026-08-01T11:00:00Z",
    },
    attributes: {
      repository_full_name: "octo/api",
      pull_request_number: "41",
      review_author: "external-reviewer",
      review_state: "approved",
      review_submitted_at: "2026-08-01T11:00:00Z",
      review_url: "https://github.com/octo/api/pull/41#pullrequestreview-100",
    },
    ...overrides,
  }
}

function providerSnapshot(
  overrides: Partial<ReviewProviderSnapshot> = {},
): ReviewProviderSnapshot {
  return {
    availability: "available",
    connector: "primary",
    repository: "octo/api",
    pull_number: 41,
    pull_request: {
      number: 41,
      title: "Review-only correctness issue",
      state: "open",
      url: "https://github.com/octo/api/pull/41",
      author: "pull-author",
      draft: false,
      merged: false,
      updated_at: "2026-08-12T10:00:00Z",
    },
    capabilities: { thread_resolution: true },
    reviews: [
      {
        id: "live-review-1",
        author: "live-reviewer",
        state: "changes_requested",
        body: "Please preserve optimistic concurrency.",
        submitted_at: "2026-08-12T09:00:00Z",
        url: "https://github.com/octo/api/pull/41#pullrequestreview-1",
      },
    ],
    review_history_complete: true,
    threads_complete: true,
    limitations: [],
    threads: [
      {
        token: "rtt_thread_one",
        is_resolved: false,
        is_outdated: false,
        is_collapsed: false,
        can_resolve: true,
        total_count: 1,
        comments: [
          {
            author: "live-reviewer",
            body: "The write can replace newer state.",
            path: "pkg/store.go",
            line: 72,
            created_at: "2026-08-12T09:01:00Z",
            url: "https://github.com/octo/api/pull/41#discussion_r1",
          },
        ],
      },
    ],
    ...overrides,
  }
}

function unavailableProviderSnapshot(pullNumber = 41): ReviewProviderSnapshot {
  return providerSnapshot({
    availability: "unavailable",
    pull_number: pullNumber,
    pull_request: undefined,
    capabilities: { thread_resolution: false },
    reviews: [],
    review_history_complete: false,
    threads_complete: false,
    limitations: ["provider_read_unavailable"],
    threads: [],
  })
}

function providerStatusFor(
  review: ReviewCase,
  state: "open" | "closed" = "open",
): ReviewProviderStatus {
  return {
    availability: "available",
    connector: review.connector,
    repository: review.repository,
    pull_number: review.pull_number,
    pull_request: {
      number: review.pull_number,
      title: review.summary,
      state,
      url: review.pull_url,
      draft: false,
      merged: false,
      updated_at: "2026-08-12T12:00:00Z",
    },
    capabilities: { thread_resolution: false },
    limitations: ["status_view"],
  }
}

const reviewOnly = reviewCase()
const dualRoleReview = reviewCase({
  id: "review-dual",
  pull_number: 43,
  pull_url: "https://github.com/octo/api/pull/43",
  summary: "Dual-role pull request",
  status: "submitted",
  active_findings: 0,
  updated_at: "2026-08-01T10:05:00Z",
})
const developmentOnly = developmentCase()
const dualRoleDevelopment = developmentCase({
  id: "development-dual",
  pull_number: 43,
  pull_url: "https://github.com/octo/api/pull/43",
  pull_state: "closed",
  attention_required: false,
  review_author: "dual-reviewer",
  captured_at: "2026-08-01T10:06:00Z",
})
const otherRepository = developmentCase({
  id: "development-web",
  repository: "octo/web",
  pull_number: 7,
  pull_url: "https://github.com/octo/web/pull/7",
  pull_state: "closed",
  attention_required: false,
  captured_at: "2026-08-01T10:07:00Z",
})

describe("ReviewPortfolioPage", () => {
  beforeAll(() => {
    Object.defineProperty(window, "matchMedia", {
      configurable: true,
      value: vi.fn().mockImplementation((query: string) => ({
        matches: false,
        media: query,
        onchange: null,
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
        addListener: vi.fn(),
        removeListener: vi.fn(),
        dispatchEvent: vi.fn(),
      })),
    })
  })

  beforeEach(() => {
    vi.mocked(listReviews).mockReset()
    vi.mocked(listPRDevelopmentCases).mockReset()
    vi.mocked(listEvents).mockReset()
    vi.mocked(getReviewProviderSnapshot).mockReset()
    vi.mocked(getReviewProviderStatus).mockReset()
    vi.mocked(mutateReviewProviderThread).mockReset()
    vi.mocked(listReviews).mockResolvedValue({
      cases: [reviewOnly, dualRoleReview],
    })
    vi.mocked(listPRDevelopmentCases).mockResolvedValue({
      cases: [developmentOnly, dualRoleDevelopment, otherRepository],
    })
    vi.mocked(listEvents).mockResolvedValue({ events: [] })
    vi.mocked(getReviewProviderSnapshot).mockImplementation(async (caseID) => {
      const match = [reviewOnly, dualRoleReview].find(
        (reviewCase) => reviewCase.id === caseID,
      )
      return unavailableProviderSnapshot(match?.pull_number ?? 41)
    })
    vi.mocked(getReviewProviderStatus).mockImplementation(async (caseID) => {
      const match = [reviewOnly, dualRoleReview].find(
        (reviewCase) => reviewCase.id === caseID,
      )
      return unavailableProviderSnapshot(match?.pull_number ?? 41)
    })
  })

  it("stops automatic pagination after a later page fails and offers an explicit retry", async () => {
    vi.mocked(listReviews).mockImplementation(({ cursor } = {}) =>
      cursor
        ? Promise.reject(new Error("older page unavailable"))
        : Promise.resolve({
            cases: [reviewOnly],
            next_cursor: "older-reviews",
          }),
    )
    vi.mocked(listPRDevelopmentCases).mockResolvedValue({ cases: [] })

    renderPortfolio()

    expect(
      await screen.findByText("Some older tracked work could not be loaded."),
    ).toBeVisible()
    await waitFor(() => expect(listReviews).toHaveBeenCalledTimes(2))
    await new Promise((resolve) => window.setTimeout(resolve, 50))
    expect(listReviews).toHaveBeenCalledTimes(2)
    expect(screen.getByRole("button", { name: "Retry" })).toBeVisible()
  })

  it("stops and reports partial data when the server repeats a cursor", async () => {
    vi.mocked(listReviews).mockImplementation(({ cursor } = {}) =>
      Promise.resolve({
        cases: cursor ? [] : [reviewOnly],
        next_cursor: "older-reviews",
      }),
    )
    vi.mocked(listPRDevelopmentCases).mockResolvedValue({ cases: [] })

    renderPortfolio()

    expect(
      await screen.findByText("Some older tracked work could not be loaded."),
    ).toBeVisible()
    await waitFor(() => expect(listReviews).toHaveBeenCalledTimes(2))
    await new Promise((resolve) => window.setTimeout(resolve, 50))
    expect(listReviews).toHaveBeenCalledTimes(2)
  })

  it("keeps a repository deep link in a loading state until later pages arrive", async () => {
    let resolveOlderPage!: (page: { cases: ReviewCase[] }) => void
    vi.mocked(listReviews).mockImplementation(({ cursor } = {}) =>
      cursor
        ? new Promise((resolve) => {
            resolveOlderPage = resolve
          })
        : Promise.resolve({ cases: [], next_cursor: "older-reviews" }),
    )
    vi.mocked(listPRDevelopmentCases).mockResolvedValue({ cases: [] })

    renderPortfolio({ initialSearch: { repo: "octo/api" } })

    expect(await screen.findByText("Loading repository work…")).toBeVisible()
    expect(screen.queryByText("Repository not found")).not.toBeInTheDocument()
    await waitFor(() => expect(listReviews).toHaveBeenCalledTimes(2))
    resolveOlderPage({ cases: [reviewOnly] })
    expect(
      await screen.findByRole("heading", { name: "octo/api" }),
    ).toBeVisible()
  })

  it("does not misreport an errored deep link as a missing repository", async () => {
    vi.mocked(listReviews).mockRejectedValue(new Error("reviews unavailable"))
    vi.mocked(listPRDevelopmentCases).mockResolvedValue({ cases: [] })

    renderPortfolio({ initialSearch: { repo: "octo/api" } })

    expect(
      await screen.findByRole("heading", { name: "Repository unavailable" }),
    ).toBeVisible()
    expect(screen.queryByText("Repository not found")).not.toBeInTheDocument()
  })

  it("waits for later pages before rejecting a direct pull-request link", async () => {
    let resolveOlderPage!: (page: { cases: ReviewCase[] }) => void
    vi.mocked(listReviews).mockImplementation(({ cursor } = {}) =>
      cursor
        ? new Promise((resolve) => {
            resolveOlderPage = resolve
          })
        : Promise.resolve({
            cases: [reviewCase({ id: "review-40", pull_number: 40 })],
            next_cursor: "older-reviews",
          }),
    )
    vi.mocked(listPRDevelopmentCases).mockResolvedValue({ cases: [] })

    renderPortfolio({ initialSearch: { repo: "octo/api", pr: 41 } })

    expect(await screen.findByText("Loading pull request work…")).toBeVisible()
    expect(screen.queryByText("Pull request not found")).not.toBeInTheDocument()
    await waitFor(() => expect(listReviews).toHaveBeenCalledTimes(2))
    resolveOlderPage({ cases: [reviewOnly] })
    expect(
      await screen.findByRole("heading", {
        name: "#41 Review-only correctness issue",
      }),
    ).toBeVisible()
  })

  it("starts with honest work-state, captured-closure, and role counts", async () => {
    renderPortfolio()

    expect(
      await screen.findByText("Only work tracked by PicoClaw"),
    ).toBeVisible()
    const apiRepository = (
      await screen.findByRole("heading", {
        name: "octo/api",
      })
    ).closest("button")
    expect(apiRepository).not.toBeNull()
    expect(
      within(apiRepository!).getByText("3 tracked pull requests"),
    ).toBeVisible()
    expect(
      within(apiRepository!).getByText("Pending").parentElement,
    ).toHaveTextContent("2Pending")
    expect(
      within(apiRepository!).getByText("Needs action").parentElement,
    ).toHaveTextContent("2Needs action")
    expect(
      within(apiRepository!).getByText("Finished").parentElement,
    ).toHaveTextContent("0Finished")
    expect(
      within(apiRepository!).getByText("Closed").parentElement,
    ).toHaveTextContent("1Closed")
    expect(apiRepository).toHaveTextContent("0 live closed · 1 captured closed")
    expect(apiRepository).toHaveTextContent("Reviewing2")
    expect(apiRepository).toHaveTextContent("Developing2")

    const webRepository = screen
      .getByRole("heading", { name: "octo/web" })
      .closest("button")
    expect(webRepository).not.toBeNull()
    expect(
      within(webRepository!).getByText("1 tracked pull request"),
    ).toBeVisible()
    expect(webRepository).toHaveTextContent("0Pending")
    expect(webRepository).toHaveTextContent("0Needs action")
    expect(webRepository).toHaveTextContent("0Finished")
    expect(webRepository).toHaveTextContent("1Closed")
    expect(webRepository).toHaveTextContent("0 live closed · 1 captured closed")
    expect(webRepository).toHaveTextContent("Reviewing0")
    expect(webRepository).toHaveTextContent("Developing1")

    expect(
      screen.queryByRole("button", { name: "Configuration" }),
    ).not.toBeInTheDocument()
  })

  it("bounds live status checks, reports progress and partial failure, and labels live closure", async () => {
    const cases = Array.from({ length: 6 }, (_, index) =>
      reviewCase({
        id: `review-status-${index + 1}`,
        repository: "octo/status",
        pull_number: index + 1,
        pull_url: `https://github.com/octo/status/pull/${index + 1}`,
        summary: `Status pull ${index + 1}`,
      }),
    )
    vi.mocked(listReviews).mockResolvedValue({ cases })
    vi.mocked(listPRDevelopmentCases).mockResolvedValue({ cases: [] })
    let active = 0
    let maximumActive = 0
    const releases: Array<() => void> = []
    vi.mocked(getReviewProviderStatus).mockImplementation(
      (caseID) =>
        new Promise((resolve, reject) => {
          const current = cases.find((review) => review.id === caseID)!
          active += 1
          maximumActive = Math.max(maximumActive, active)
          releases.push(() => {
            active -= 1
            if (caseID === "review-status-2") {
              reject(new Error("provider status unavailable"))
              return
            }
            resolve(
              providerStatusFor(
                current,
                caseID === "review-status-1" ? "closed" : "open",
              ),
            )
          })
        }),
    )

    const user = userEvent.setup()
    renderPortfolio()

    expect(
      await screen.findByText("Checking live provider state: 0 of 6."),
    ).toBeVisible()
    await waitFor(() =>
      expect(getReviewProviderStatus).toHaveBeenCalledTimes(4),
    )
    expect(maximumActive).toBe(4)

    releases.shift()?.()
    expect(
      await screen.findByText("Checking live provider state: 1 of 6."),
    ).toBeVisible()
    await waitFor(() =>
      expect(getReviewProviderStatus).toHaveBeenCalledTimes(5),
    )

    while (releases.length > 0) {
      releases.shift()?.()
      await new Promise((resolve) => window.setTimeout(resolve, 0))
    }
    await waitFor(() =>
      expect(getReviewProviderStatus).toHaveBeenCalledTimes(6),
    )
    while (releases.length > 0) {
      releases.shift()?.()
      await new Promise((resolve) => window.setTimeout(resolve, 0))
    }

    expect(
      await screen.findByText(
        /could not be verified for 1 of 6 tracked pull requests/,
      ),
    ).toBeVisible()
    expect(maximumActive).toBe(4)
    const repository = screen
      .getByRole("heading", { name: "octo/status" })
      .closest("button")
    expect(repository).not.toBeNull()
    expect(repository).toHaveTextContent("1 live closed · 0 captured closed")

    await user.click(repository!)
    const closedPull = await screen.findByRole("button", {
      name: /#1.*Status pull 1.*Closed/i,
    })
    expect(closedPull).toHaveTextContent("Closed")
    expect(closedPull).not.toHaveTextContent("Captured closed")
  })

  it("drills into a repository and applies an autocomplete-built role filter", async () => {
    const user = userEvent.setup()
    const onSearchChange = vi.fn()
    renderPortfolio({ onSearchChange })

    await user.click(
      await screen.findByRole("button", { name: /octo\/api 3 tracked/i }),
    )
    expect(onSearchChange).toHaveBeenCalledWith({ repo: "octo/api" }, undefined)
    expect(
      await screen.findByRole("heading", { name: "octo/api" }),
    ).toBeVisible()
    expect(
      screen.getByText(
        "2 pending · 2 need action · 0 finished · 1 closed (0 live, 1 captured)",
      ),
    ).toBeVisible()
    expect(
      screen.getByRole("button", {
        name: /#41.*Review-only correctness issue.*Reviewing.*Pending/i,
      }),
    ).toBeVisible()
    expect(
      screen.getByRole("button", {
        name: /#42.*Feedback from @maintainer.*Developing.*Pending/i,
      }),
    ).toBeVisible()

    await user.click(screen.getByRole("button", { name: "All repositories" }))
    expect(onSearchChange).toHaveBeenLastCalledWith({}, true)
    await user.click(
      await screen.findByRole("button", { name: /octo\/api 3 tracked/i }),
    )

    await user.click(screen.getByLabelText("Filter pull requests"))
    await user.click(screen.getByRole("option", { name: "role" }))
    await user.click(screen.getByRole("option", { name: "=" }))
    await user.click(screen.getByRole("option", { name: "develop" }))
    expect(screen.getByLabelText("Filter pull requests")).toHaveValue(
      "role = develop",
    )
    await user.click(screen.getByRole("button", { name: "Search" }))

    await waitFor(() => {
      expect(onSearchChange).toHaveBeenLastCalledWith(
        { repo: "octo/api", filter: "role = develop" },
        true,
      )
    })
    expect(
      screen.queryByText("Review-only correctness issue"),
    ).not.toBeInTheDocument()
    expect(screen.getByText("Feedback from @maintainer")).toBeVisible()
    expect(screen.getByText("Dual-role pull request")).toBeVisible()
  })

  it("shows provider review history and hands the current PicoClaw case to the editor", async () => {
    vi.mocked(listEvents).mockResolvedValue({
      events: [
        reviewEvent(),
        reviewEvent({
          id: "event-provider-review-older",
          actor: { display_name: "second-reviewer" },
          occurred_at: "2026-08-01T10:30:00Z",
          attributes: {
            repository_full_name: "octo/api",
            pull_request_number: "41",
            review_state: "changes_requested",
          },
        }),
      ],
    })
    const user = userEvent.setup()
    const onOpenReview = vi.fn()
    renderPortfolio({
      initialSearch: { repo: "octo/api", pr: 41 },
      onOpenReview,
    })

    expect(
      await screen.findByRole("heading", {
        name: "#41 Review-only correctness issue",
      }),
    ).toBeVisible()
    expect(
      screen.getByRole("heading", { name: "PicoClaw review" }),
    ).toBeVisible()
    expect(screen.getByText("1/2 active")).toBeVisible()
    expect(
      await screen.findByText(
        /Fallback: retained provider observations plus PicoClaw case lifecycle/,
      ),
    ).toBeVisible()
    expect(await screen.findByText("@external-reviewer")).toBeVisible()
    expect(screen.getByText("approved")).toBeVisible()
    expect(screen.getByText("@second-reviewer")).toBeVisible()
    expect(screen.getByText("changes requested")).toBeVisible()

    await user.click(screen.getByRole("button", { name: /Open review editor/ }))
    expect(onOpenReview).toHaveBeenCalledWith(reviewOnly.id, "octo/api", 41)
    expect(
      screen.getByText(/write or rephrase prepared comments/i),
    ).toBeVisible()
    expect(screen.getByText("Connector unavailable")).toBeVisible()
  })

  it("renders authoritative live review details and performs only token-authorized thread actions", async () => {
    const initial = providerSnapshot()
    const resolved = {
      ...initial,
      threads: initial.threads.map((thread) => ({
        ...thread,
        is_resolved: true,
      })),
    }
    vi.mocked(getReviewProviderSnapshot).mockResolvedValue(initial)
    vi.mocked(mutateReviewProviderThread).mockResolvedValue(resolved)
    const user = userEvent.setup()

    renderPortfolio({ initialSearch: { repo: "octo/api", pr: 41 } })

    expect(
      await screen.findByRole("heading", { name: "Live provider review" }),
    ).toBeVisible()
    expect(
      (await screen.findByText("Current pull request state")).parentElement,
    ).toHaveTextContent("#41 Review-only correctness issue · @pull-author")
    expect(screen.getAllByText("@live-reviewer")).toHaveLength(2)
    expect(
      screen.getByText("Please preserve optimistic concurrency."),
    ).toBeVisible()
    expect(screen.getByText("pkg/store.go:72")).toBeVisible()
    expect(screen.getByText("The write can replace newer state.")).toBeVisible()
    const liveThread = screen.getByText("pkg/store.go:72").closest("li")
    expect(liveThread).not.toBeNull()
    expect(within(liveThread!).getByText("Open", { exact: true })).toBeVisible()
    expect(
      screen.getByText(
        "Local PicoClaw case lifecycle. Live provider reviews and threads are shown separately above.",
      ),
    ).toBeVisible()
    expect(listEvents).not.toHaveBeenCalled()

    await user.click(screen.getByRole("button", { name: "Resolve thread" }))
    await waitFor(() =>
      expect(mutateReviewProviderThread).toHaveBeenCalledWith(
        reviewOnly.id,
        "rtt_thread_one",
        "resolve",
      ),
    )
  })

  it("reconciles a full live snapshot into the PR header and repository counts", async () => {
    const statusReleases: Array<() => void> = []
    vi.mocked(getReviewProviderStatus).mockImplementation(
      (caseID) =>
        new Promise((resolve) => {
          const match = [reviewOnly, dualRoleReview].find(
            (review) => review.id === caseID,
          )!
          statusReleases.push(() => resolve(providerStatusFor(match, "open")))
        }),
    )
    vi.mocked(getReviewProviderSnapshot).mockResolvedValue(
      providerSnapshot({
        pull_request: {
          ...providerSnapshot().pull_request!,
          title: "Provider-renamed pull request",
          state: "closed",
          updated_at: "2026-08-12T13:00:00Z",
        },
      }),
    )
    const user = userEvent.setup()

    renderPortfolio({ initialSearch: { repo: "octo/api", pr: 41 } })

    expect(
      await screen.findByRole("heading", {
        name: "#41 Provider-renamed pull request",
      }),
    ).toBeVisible()
    expect(
      screen.getAllByText("Closed", { exact: true }).length,
    ).toBeGreaterThanOrEqual(2)
    await waitFor(() =>
      expect(getReviewProviderStatus).toHaveBeenCalledTimes(2),
    )
    while (statusReleases.length > 0) statusReleases.shift()?.()
    await waitFor(() =>
      expect(
        screen.queryByText(/Checking live provider state:/),
      ).not.toBeInTheDocument(),
    )
    await waitFor(() =>
      expect(
        screen.getByRole("heading", {
          name: "#41 Provider-renamed pull request",
        }),
      ).toBeVisible(),
    )

    await user.click(screen.getByRole("button", { name: "octo/api" }))
    const row = await screen.findByRole("button", {
      name: /#41.*Provider-renamed pull request.*Closed/i,
    })
    expect(row).toHaveTextContent("Closed")
  })

  it("does not let an older full snapshot regress a newer status observation", async () => {
    let resolveFullSnapshot!: (snapshot: ReviewProviderSnapshot) => void
    vi.mocked(getReviewProviderSnapshot).mockImplementation(
      () =>
        new Promise((resolve) => {
          resolveFullSnapshot = resolve
        }),
    )
    const newerStatus = providerStatusFor(reviewOnly, "closed")
    newerStatus.pull_request = {
      ...newerStatus.pull_request!,
      title: "Newer provider title",
      updated_at: "2026-08-12T14:00:00Z",
    }
    vi.mocked(getReviewProviderStatus).mockImplementation(async (caseID) =>
      caseID === reviewOnly.id ? newerStatus : unavailableProviderSnapshot(42),
    )

    renderPortfolio({ initialSearch: { repo: "octo/api", pr: 41 } })

    expect(
      await screen.findByRole("heading", {
        name: "#41 Newer provider title",
      }),
    ).toBeVisible()
    resolveFullSnapshot(
      providerSnapshot({
        pull_request: {
          ...providerSnapshot().pull_request!,
          title: "Older full-snapshot title",
          state: "open",
          updated_at: "2026-08-12T13:00:00Z",
        },
      }),
    )
    await waitFor(() =>
      expect(getReviewProviderSnapshot).toHaveBeenCalledTimes(1),
    )
    expect(
      screen.getByRole("heading", { name: "#41 Newer provider title" }),
    ).toBeVisible()
    expect(
      screen.queryByRole("heading", { name: /Older full-snapshot title/ }),
    ).not.toBeInTheDocument()
  })

  it("keeps newer status state when a global refresh cannot replace the cached full snapshot", async () => {
    vi.mocked(listReviews).mockResolvedValue({ cases: [reviewOnly] })
    vi.mocked(listPRDevelopmentCases).mockResolvedValue({ cases: [] })
    vi.mocked(getReviewProviderSnapshot)
      .mockResolvedValueOnce(providerSnapshot())
      .mockRejectedValueOnce(new Error("full provider refresh failed"))
    vi.mocked(getReviewProviderStatus)
      .mockResolvedValueOnce(providerStatusFor(reviewOnly, "open"))
      .mockResolvedValueOnce(providerStatusFor(reviewOnly, "closed"))
    const user = userEvent.setup()

    renderPortfolio({ initialSearch: { repo: "octo/api", pr: 41 } })

    expect(
      await screen.findByRole("heading", {
        name: "#41 Review-only correctness issue",
      }),
    ).toBeVisible()
    await waitFor(() =>
      expect(getReviewProviderStatus).toHaveBeenCalledTimes(1),
    )
    await user.click(
      screen.getByRole("button", { name: "Refresh pull request work" }),
    )

    await waitFor(() =>
      expect(getReviewProviderSnapshot).toHaveBeenCalledTimes(2),
    )
    await waitFor(() =>
      expect(getReviewProviderStatus).toHaveBeenCalledTimes(2),
    )
    await waitFor(() =>
      expect(
        screen.getAllByText("Closed", { exact: true }).length,
      ).toBeGreaterThan(0),
    )
    expect(screen.getByText("Live provider review unavailable")).toBeVisible()
  })

  it("keeps an in-flight thread mutation bound to its original review case", async () => {
    const caseA = reviewCase({
      id: "review-case-a",
      summary: "Case A",
      updated_at: "2026-08-02T10:00:00Z",
    })
    const caseB = reviewCase({
      id: "review-case-b",
      summary: "Case B",
      updated_at: "2026-08-01T10:00:00Z",
    })
    vi.mocked(listReviews).mockResolvedValue({ cases: [caseA, caseB] })
    vi.mocked(listPRDevelopmentCases).mockResolvedValue({ cases: [] })
    const snapshotFor = (label: string, resolved = false) =>
      providerSnapshot({
        reviews: [],
        threads: [
          {
            ...providerSnapshot().threads[0],
            token: `token-${label.toLowerCase()}`,
            is_resolved: resolved,
            comments: [
              {
                ...providerSnapshot().threads[0].comments[0],
                body: `${label} provider thread`,
              },
            ],
          },
        ],
      })
    let caseAResolved = false
    vi.mocked(getReviewProviderSnapshot).mockImplementation(async (caseID) =>
      caseID === caseA.id
        ? snapshotFor("Case A", caseAResolved)
        : snapshotFor("Case B"),
    )
    let releaseMutation!: () => void
    const mutationGate = new Promise<void>((resolve) => {
      releaseMutation = resolve
    })
    vi.mocked(mutateReviewProviderThread).mockImplementation(async () => {
      await mutationGate
      caseAResolved = true
      return snapshotFor("Case A", true)
    })
    const user = userEvent.setup()

    renderPortfolio({
      initialSearch: {
        repo: "octo/api",
        pr: 41,
        review_case: caseA.id,
      },
    })

    expect(await screen.findByText("Case A provider thread")).toBeVisible()
    await user.click(screen.getByRole("button", { name: "Resolve thread" }))
    await user.click(screen.getByRole("radio", { name: /Case B.*Updated/i }))
    expect(await screen.findByText("Case B provider thread")).toBeVisible()

    releaseMutation()
    await waitFor(() =>
      expect(mutateReviewProviderThread).toHaveBeenCalledWith(
        caseA.id,
        "token-case a",
        "resolve",
      ),
    )
    expect(screen.getByText("Case B provider thread")).toBeVisible()
    expect(screen.getByRole("button", { name: "Resolve thread" })).toBeVisible()

    await user.click(screen.getByRole("radio", { name: /Case A.*Updated/i }))
    expect(await screen.findByText("Case A provider thread")).toBeVisible()
    expect(
      await screen.findByRole("button", { name: "Reopen thread" }),
    ).toBeVisible()
  })

  it("shows current partial data and upgrade limitations without unsafe thread controls or retained scans", async () => {
    const partial = providerSnapshot({
      availability: "partial",
      capabilities: { thread_resolution: false },
      review_history_complete: false,
      threads_complete: false,
      limitations: [
        "review_history_pagination_stalled",
        "thread_identity_unavailable",
      ],
      threads: [
        {
          ...providerSnapshot().threads[0],
          token: undefined,
          can_resolve: false,
          is_outdated: true,
        },
      ],
    })
    vi.mocked(getReviewProviderSnapshot).mockResolvedValue(partial)

    renderPortfolio({ initialSearch: { repo: "octo/api", pr: 41 } })

    expect(await screen.findByText("Live · incomplete")).toBeVisible()
    expect(screen.getByText("Live provider data is incomplete")).toBeVisible()
    expect(
      screen.getByText(/GitHub connector to v1\.0\.5 or newer/),
    ).toBeVisible()
    expect(
      screen.getByText(/GitHub connector to v1\.1\.0 or newer/),
    ).toBeVisible()
    expect(screen.getByText("Outdated")).toBeVisible()
    expect(
      screen.queryByRole("button", { name: "Resolve thread" }),
    ).not.toBeInTheDocument()
    expect(listEvents).not.toHaveBeenCalled()
  })

  it("labels retained activity as fallback after a live request error and retries explicitly", async () => {
    vi.mocked(getReviewProviderSnapshot)
      .mockRejectedValueOnce(new Error("connector offline"))
      .mockResolvedValueOnce(providerSnapshot())
    vi.mocked(listEvents).mockResolvedValue({ events: [reviewEvent()] })
    const user = userEvent.setup()

    renderPortfolio({ initialSearch: { repo: "octo/api", pr: 41 } })

    expect(
      await screen.findByText("Live provider review unavailable"),
    ).toBeVisible()
    expect(
      await screen.findByText(
        /Fallback: retained provider observations plus PicoClaw case lifecycle/,
      ),
    ).toBeVisible()
    expect(await screen.findByText("@external-reviewer")).toBeVisible()

    await user.click(
      screen.getByRole("button", { name: "Retry live provider" }),
    )
    expect(await screen.findByText("Current pull request state")).toBeVisible()
    await waitFor(() =>
      expect(getReviewProviderSnapshot).toHaveBeenCalledTimes(2),
    )
  })

  it("defaults to an older editable case and exposes every captured case as a URL-backed radio choice", async () => {
    const cases = [
      reviewCase({
        id: "review-stale",
        summary: "Newer stale case",
        status: "stale",
        updated_at: "2026-08-05T10:00:00Z",
      }),
      reviewCase({
        id: "review-submitting",
        summary: "Newer submitting case",
        status: "submitting",
        updated_at: "2026-08-04T10:00:00Z",
      }),
      reviewCase({
        id: "review-unknown",
        summary: "Newer unknown case",
        status: "submission_unknown",
        updated_at: "2026-08-03T10:00:00Z",
      }),
      reviewCase({
        id: "review-editable",
        summary: "Older editable case",
        status: "open",
        updated_at: "2026-08-02T10:00:00Z",
      }),
    ]
    vi.mocked(listReviews).mockResolvedValue({ cases })
    vi.mocked(listPRDevelopmentCases).mockResolvedValue({ cases: [] })
    const user = userEvent.setup()
    const onSearchChange = vi.fn()
    const onOpenReview = vi.fn()
    renderPortfolio({
      initialSearch: {
        repo: "octo/api",
        pr: 41,
        filter: "role = review",
        role: "review",
      },
      onSearchChange,
      onOpenReview,
    })

    const caseGroup = await screen.findByRole("group", {
      name: "Review cases",
    })
    const editableChoice = within(caseGroup).getByRole("radio", {
      name: /Older editable case.*Open/i,
    })
    expect(editableChoice).toBeChecked()
    expect(
      within(caseGroup).getByRole("radio", {
        name: /Newer stale case.*Stale/i,
      }),
    ).not.toBeChecked()
    expect(
      within(caseGroup).getByRole("radio", {
        name: /Newer submitting case.*Submitting/i,
      }),
    ).toBeVisible()
    expect(
      within(caseGroup).getByRole("radio", {
        name: /Newer unknown case.*Outcome unknown/i,
      }),
    ).toBeVisible()

    await user.click(screen.getByRole("button", { name: "Open review editor" }))
    expect(onOpenReview).toHaveBeenLastCalledWith(
      "review-editable",
      "octo/api",
      41,
    )

    await user.click(
      within(caseGroup).getByRole("radio", {
        name: /Newer stale case.*Stale/i,
      }),
    )
    expect(onSearchChange).toHaveBeenLastCalledWith(
      {
        repo: "octo/api",
        pr: 41,
        filter: "role = review",
        role: "review",
        review_case: "review-stale",
      },
      true,
    )
    expect(
      within(caseGroup).getByRole("radio", {
        name: /Newer stale case.*Stale/i,
      }),
    ).toBeChecked()
    await user.click(screen.getByRole("button", { name: "View review case" }))
    expect(onOpenReview).toHaveBeenLastCalledWith(
      "review-stale",
      "octo/api",
      41,
    )
  })

  it("keeps review and development views distinct and labels development as unfinished", async () => {
    const user = userEvent.setup()
    renderPortfolio({ initialSearch: { repo: "octo/api", pr: 43 } })

    const reviewing = await screen.findByRole("button", { name: "Reviewing" })
    const developing = screen.getByRole("button", { name: "Developing" })
    expect(reviewing).toHaveAttribute("aria-current", "page")
    expect(
      screen.getByRole("heading", { name: "PicoClaw review" }),
    ).toBeVisible()
    expect(await screen.findByText("@dual-reviewer")).toBeVisible()

    await user.click(developing)
    expect(developing).toHaveAttribute("aria-current", "page")
    expect(
      screen.getByRole("heading", { name: "Development workspace" }),
    ).toBeVisible()
    expect(screen.getByText("Coming soon")).toBeVisible()
    expect(screen.getByText(/Latest feedback from/)).toHaveTextContent(
      "Latest feedback from @dual-reviewer",
    )
  })

  it("restores the selected role from route state", async () => {
    renderPortfolio({
      initialSearch: { repo: "octo/api", pr: 43, role: "develop" },
    })

    expect(
      await screen.findByRole("heading", { name: "Development workspace" }),
    ).toBeVisible()
    expect(screen.getByRole("button", { name: "Developing" })).toHaveAttribute(
      "aria-current",
      "page",
    )
  })

  it("resolves repository casing and resets the role on direct PR-to-PR navigation", async () => {
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })

    function DirectNavigationHarness() {
      const [search, setSearch] = useState<ReviewPortfolioSearch>({
        repo: "OCTO/API",
        pr: 43,
      })
      return (
        <>
          <button
            type="button"
            onClick={() => setSearch({ repo: "OCTO/API", pr: 41 })}
          >
            Open review-only PR
          </button>
          <button
            type="button"
            onClick={() => setSearch({ repo: "OCTO/API", pr: 42 })}
          >
            Open development-only PR
          </button>
          <ReviewPortfolioPage
            search={search}
            onSearchChange={setSearch}
            onOpenReview={vi.fn()}
          />
        </>
      )
    }

    const user = userEvent.setup()
    render(
      <QueryClientProvider client={queryClient}>
        <SidebarProvider>
          <DirectNavigationHarness />
        </SidebarProvider>
      </QueryClientProvider>,
    )

    expect(
      await screen.findByRole("heading", {
        name: "#43 Dual-role pull request",
      }),
    ).toBeVisible()
    await user.click(screen.getByRole("button", { name: "Developing" }))
    expect(
      screen.getByRole("heading", { name: "Development workspace" }),
    ).toBeVisible()

    await user.click(
      screen.getByRole("button", { name: "Open review-only PR" }),
    )
    expect(
      await screen.findByRole("heading", { name: "PicoClaw review" }),
    ).toBeVisible()
    expect(
      screen.queryByRole("heading", { name: "Development workspace" }),
    ).not.toBeInTheDocument()

    await user.click(
      screen.getByRole("button", { name: "Open development-only PR" }),
    )
    expect(
      await screen.findByRole("heading", { name: "Development workspace" }),
    ).toBeVisible()
    expect(
      screen.queryByRole("heading", { name: "PicoClaw review" }),
    ).not.toBeInTheDocument()
  })
})

type SearchChangeHandler = (
  search: ReviewPortfolioSearch,
  replace?: boolean,
) => void
type OpenReviewHandler = (
  caseID: string,
  repository: string,
  pullNumber: number,
) => void

function renderPortfolio({
  initialSearch = {},
  onSearchChange = vi.fn<SearchChangeHandler>(),
  onOpenReview = vi.fn<OpenReviewHandler>(),
}: {
  initialSearch?: ReviewPortfolioSearch
  onSearchChange?: SearchChangeHandler
  onOpenReview?: OpenReviewHandler
} = {}) {
  const queryClient = new QueryClient({
    defaultOptions: {
      queries: { retry: false },
      mutations: { retry: false },
    },
  })

  function Harness() {
    const [search, setSearch] = useState(initialSearch)
    return (
      <ReviewPortfolioPage
        search={search}
        onSearchChange={(next, replace) => {
          onSearchChange(next, replace)
          setSearch(next)
        }}
        onOpenReview={onOpenReview}
      />
    )
  }

  return render(
    <QueryClientProvider client={queryClient}>
      <SidebarProvider>
        <Harness />
      </SidebarProvider>
    </QueryClientProvider>,
  )
}
