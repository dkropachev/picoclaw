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

vi.mock("@/features/chat/controller", () => ({
  initializeChatStore: vi.fn(),
}))

describe("reviews route navigation", () => {
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
})
