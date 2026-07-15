import { QueryClient } from "@tanstack/react-query"
import {
  RouterProvider,
  createMemoryHistory,
  createRouter,
} from "@tanstack/react-router"
import { render, screen } from "@testing-library/react"
import type { ReactNode } from "react"
import { beforeEach, describe, expect, it, vi } from "vitest"

import { routeTree } from "@/routeTree.gen"

const threadOpenPageCalls = vi.hoisted(() => [] as Array<string | undefined>)

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

vi.mock("@/components/threads/thread-open-page", () => ({
  ThreadOpenPage: ({ threadId = "" }: { threadId?: string }) => {
    threadOpenPageCalls.push(threadId)
    return (
      <div data-testid="thread-open-page">
        {threadId ? `thread:${threadId}` : "empty"}
      </div>
    )
  },
}))

vi.mock("@/features/chat/controller", () => ({
  initializeChatStore: vi.fn(),
}))

function renderThreadRoute(pathname: string) {
  const router = createRouter({
    routeTree,
    history: createMemoryHistory({ initialEntries: [pathname] }),
    context: {
      queryClient: new QueryClient(),
    },
  })

  render(<RouterProvider router={router} />)

  return router
}

describe("thread open routes", () => {
  beforeEach(() => {
    threadOpenPageCalls.length = 0
  })

  it("renders the empty Thread workspace only for the exact open route", async () => {
    const router = renderThreadRoute("/threads/open")

    const routeIds = router
      .matchRoutes("/threads/open", {})
      .map((match) => match.routeId)

    expect(routeIds).toContain("/threads/open")
    expect(routeIds).not.toContain("/threads/open/$threadId")
    expect(await screen.findByTestId("thread-open-page")).toHaveTextContent(
      "empty",
    )
  })

  it("renders concrete thread URLs through the open-thread child route", async () => {
    const threadId = "session-1784121116746-a08fa481"
    const router = renderThreadRoute(`/threads/open/${threadId}`)

    const routeIds = router
      .matchRoutes(`/threads/open/${threadId}`, {})
      .map((match) => match.routeId)

    expect(routeIds).toContain("/threads/open")
    expect(routeIds.at(-1)).toBe("/threads/open/$threadId")
    expect(await screen.findByTestId("thread-open-page")).toHaveTextContent(
      `thread:${threadId}`,
    )
    expect(screen.queryByText("empty")).not.toBeInTheDocument()
  })
})
