import { QueryClient } from "@tanstack/react-query"
import {
  RouterProvider,
  createMemoryHistory,
  createRouter,
} from "@tanstack/react-router"
import { render, screen, waitFor } from "@testing-library/react"
import type { ReactNode } from "react"
import { describe, expect, it, vi } from "vitest"

import type { ReviewsRouteSearch } from "@/components/reviews/reviews-page"
import { routeTree } from "@/routeTree.gen"

const caseID = `prc_${"a".repeat(32)}`
const developmentCaseID = `pdc_${"b".repeat(32)}`

vi.mock("@/api/launcher-auth", () => ({
  getLauncherAuthStatus: vi.fn().mockResolvedValue({
    authenticated: true,
    initialized: true,
  }),
}))

vi.mock("@/components/app-layout", () => ({
  AppLayout: ({ children }: { children: ReactNode }) => (
    <div data-testid="app-layout">{children}</div>
  ),
}))

vi.mock("@/components/reviews/reviews-page", () => ({
  ReviewsPage: ({ search }: { search: ReviewsRouteSearch }) => (
    <output data-testid="reviews-search">{JSON.stringify(search)}</output>
  ),
}))

vi.mock("@/components/reviews/review-portfolio-page", () => ({
  ReviewPortfolioPage: ({ search }: { search: ReviewsRouteSearch }) => (
    <output data-testid="portfolio-search">{JSON.stringify(search)}</output>
  ),
}))

vi.mock("@/components/reviews/review-attention-policies-page", () => ({
  ReviewAttentionPoliciesPage: ({
    onShowInbox,
  }: {
    onShowInbox: () => void
    onShowDevelopment: () => void
  }) => (
    <button type="button" onClick={onShowInbox}>
      Policy editor
    </button>
  ),
}))

vi.mock("@/components/reviews/pr-development-page", () => ({
  PRDevelopmentPage: ({ search }: { search: ReviewsRouteSearch }) => (
    <output data-testid="development-search">{JSON.stringify(search)}</output>
  ),
}))

vi.mock("@/features/chat/controller", () => ({
  initializeChatStore: vi.fn(),
}))

describe("reviews route navigation", () => {
  it("renders the repository portfolio with canonical filter and role state", async () => {
    const router = createRouter({
      routeTree,
      history: createMemoryHistory({
        initialEntries: [
          `/reviews?repo=%20octo%2Frepo%20&pr=84&filter=%20role%20%3D%20develop%20&role=develop&review_case=${caseID}&cursor=opaque`,
        ],
      }),
      context: {
        queryClient: new QueryClient({
          defaultOptions: { queries: { retry: false } },
        }),
      },
    })

    render(<RouterProvider router={router} />)

    await waitFor(() => {
      expect(router.state.location.search).toEqual({
        repo: "octo/repo",
        pr: 84,
        filter: "role = develop",
        role: "develop",
        review_case: caseID,
      })
    })
    expect(screen.getByTestId("portfolio-search")).toHaveTextContent(
      JSON.stringify({
        repo: "octo/repo",
        pr: 84,
        filter: "role = develop",
        role: "develop",
        review_case: caseID,
      }),
    )
  })

  it("replaces cursor, prompt, and malformed state with the canonical URL", async () => {
    const router = createRouter({
      routeTree,
      history: createMemoryHistory({
        initialEntries: [
          "/reviews?repository=%20octo%2Frepo%20&status=bad&cursor=opaque&instruction=private",
        ],
      }),
      context: {
        queryClient: new QueryClient({
          defaultOptions: { queries: { retry: false } },
        }),
      },
    })

    render(<RouterProvider router={router} />)

    await waitFor(() => {
      expect(router.state.location.search).toEqual({
        repository: "octo/repo",
      })
    })
    expect(screen.getByTestId("reviews-search")).toHaveTextContent(
      JSON.stringify({ repository: "octo/repo" }),
    )
  })

  it("renders the policy editor and removes every non-view query value", async () => {
    const router = createRouter({
      routeTree,
      history: createMemoryHistory({
        initialEntries: [
          "/reviews?view=policies&case=private&questions=secret&revision=opaque",
        ],
      }),
      context: {
        queryClient: new QueryClient({
          defaultOptions: { queries: { retry: false } },
        }),
      },
    })

    render(<RouterProvider router={router} />)

    await waitFor(() => {
      expect(router.state.location.search).toEqual({ view: "policies" })
    })
    expect(screen.getByRole("button", { name: "Policy editor" })).toBeVisible()
    expect(screen.queryByTestId("reviews-search")).not.toBeInTheDocument()
  })

  it("renders development feedback with only its safe canonical route state", async () => {
    const router = createRouter({
      routeTree,
      history: createMemoryHistory({
        initialEntries: [
          `/reviews?view=development&case=${developmentCaseID}&repository=%20octo%2Frepo%20&pull_number=84&focus=chat&questions=private&cursor=opaque`,
        ],
      }),
      context: {
        queryClient: new QueryClient({
          defaultOptions: { queries: { retry: false } },
        }),
      },
    })

    render(<RouterProvider router={router} />)

    await waitFor(() => {
      expect(router.state.location.search).toEqual({
        view: "development",
        case: developmentCaseID,
        focus: "chat",
      })
    })
    expect(screen.getByTestId("development-search")).toHaveTextContent(
      JSON.stringify({
        view: "development",
        case: developmentCaseID,
        focus: "chat",
      }),
    )
    expect(screen.queryByTestId("reviews-search")).not.toBeInTheDocument()
  })

  it("keeps the fixed case-owned chat handoff and scrubs private attention state", async () => {
    const router = createRouter({
      routeTree,
      history: createMemoryHistory({
        initialEntries: [
          `/reviews?case=${caseID}&focus=chat&status=submitted&repository=octo%2Frepo&cursor=opaque&run=private&task=private&policy_revision=opaque&questions=secret#run=private-fragment`,
        ],
      }),
      context: {
        queryClient: new QueryClient({
          defaultOptions: { queries: { retry: false } },
        }),
      },
    })

    render(<RouterProvider router={router} />)

    await waitFor(() => {
      expect(router.state.location.search).toEqual({
        case: caseID,
        focus: "chat",
      })
      expect(router.state.location.hash).toBe("")
    })
    expect(screen.getByTestId("reviews-search")).toHaveTextContent(
      JSON.stringify({ case: caseID, focus: "chat" }),
    )
  })
})
