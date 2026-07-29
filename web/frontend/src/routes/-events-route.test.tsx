import { QueryClient } from "@tanstack/react-query"
import {
  RouterProvider,
  createMemoryHistory,
  createRouter,
} from "@tanstack/react-router"
import { render, screen, waitFor } from "@testing-library/react"
import type { ReactNode } from "react"
import { describe, expect, it, vi } from "vitest"

import type { EventsRouteSearch } from "@/components/events/events-page"
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

vi.mock("@/components/events/events-page", () => ({
  EventsPage: ({ search }: { search: EventsRouteSearch }) => (
    <output data-testid="events-search">{JSON.stringify(search)}</output>
  ),
}))

vi.mock("@/features/chat/controller", () => ({
  initializeChatStore: vi.fn(),
}))

function renderEventsRoute(pathname: string) {
  const router = createRouter({
    routeTree,
    history: createMemoryHistory({ initialEntries: [pathname] }),
    context: {
      queryClient: new QueryClient({
        defaultOptions: { queries: { retry: false } },
      }),
    },
  })

  render(<RouterProvider router={router} />)
  return router
}

describe("events route navigation", () => {
  it("replaces raw invalid, cursor, and sensitive search with canonical state", async () => {
    const router = renderEventsRoute(
      "/events?source=%20github%20&view=bad&cursor=opaque&payload=secret&error=private",
    )

    await waitFor(() => {
      expect(router.state.location.search).toEqual({ source: "github" })
    })
    expect(screen.getByTestId("events-search")).toHaveTextContent(
      JSON.stringify({ source: "github" }),
    )
  })
})
