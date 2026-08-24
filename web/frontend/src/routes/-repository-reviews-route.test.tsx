import { QueryClient } from "@tanstack/react-query"
import {
  RouterProvider,
  createMemoryHistory,
  createRouter,
} from "@tanstack/react-router"
import { render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import type { ReactNode } from "react"
import { describe, expect, it, vi } from "vitest"

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

vi.mock("@/components/repository-reviews/repository-reviews-page", () => ({
  RepositoryReviewsPage: ({
    onOpenThread,
  }: {
    onOpenThread: (threadID: string) => void
  }) => (
    <div>
      <output>Repository review workspace</output>
      <button type="button" onClick={() => onOpenThread("session-review")}>
        Open finding discussion
      </button>
    </div>
  ),
}))

vi.mock("@/components/repository-reviews/repository-review-runs-page", () => ({
  RepositoryReviewRunsPage: () => <output>Review runs workspace</output>,
}))

vi.mock(
  "@/components/repository-reviews/repository-review-profiles-page",
  () => ({
    RepositoryReviewProfilesPage: () => <output>Profiles workspace</output>,
  }),
)

vi.mock(
  "@/components/repository-reviews/repository-review-repositories-page",
  () => ({
    RepositoryReviewRepositoriesPage: () => (
      <output>Repositories workspace</output>
    ),
  }),
)

vi.mock("@/components/threads/thread-open-page", () => ({
  ThreadOpenPage: ({ threadId }: { threadId?: string }) => (
    <output data-testid="opened-thread">{threadId}</output>
  ),
}))

vi.mock("@/features/chat/controller", () => ({
  initializeChatStore: vi.fn(),
}))

describe("repository reviews route", () => {
  it("renders the dedicated review runs route", async () => {
    const router = createRouter({
      routeTree,
      history: createMemoryHistory({ initialEntries: ["/repository-reviews"] }),
      context: {
        queryClient: new QueryClient({
          defaultOptions: { queries: { retry: false } },
        }),
      },
    })
    render(<RouterProvider router={router} />)

    expect(await screen.findByText("Review runs workspace")).toBeVisible()
  })

  it("keeps finding discussions in the dedicated results route", async () => {
    const router = createRouter({
      routeTree,
      history: createMemoryHistory({
        initialEntries: ["/repository-reviews/results"],
      }),
      context: {
        queryClient: new QueryClient({
          defaultOptions: { queries: { retry: false } },
        }),
      },
    })
    const user = userEvent.setup()

    render(<RouterProvider router={router} />)

    expect(await screen.findByText("Repository review workspace")).toBeVisible()
    await user.click(
      screen.getByRole("button", { name: "Open finding discussion" }),
    )
    await waitFor(() => {
      expect(router.state.location.pathname).toBe(
        "/threads/open/session-review",
      )
    })
    expect(await screen.findByTestId("opened-thread")).toHaveTextContent(
      "session-review",
    )
  })

  it.each([
    ["/repository-reviews/repositories", "Repositories workspace"],
    ["/repository-reviews/profiles", "Profiles workspace"],
  ])("renders the flat %s route", async (path, text) => {
    const router = createRouter({
      routeTree,
      history: createMemoryHistory({ initialEntries: [path] }),
      context: {
        queryClient: new QueryClient({
          defaultOptions: { queries: { retry: false } },
        }),
      },
    })

    render(<RouterProvider router={router} />)

    expect(await screen.findByText(text)).toBeVisible()
    expect(router.state.location.pathname).toBe(path)
  })
})
