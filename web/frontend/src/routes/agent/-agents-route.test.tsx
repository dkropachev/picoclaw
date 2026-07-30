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

import type { AgentsRouteSearch } from "@/components/agent/agents/agent-route-search"
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

vi.mock("@/components/agent/agents/agents-page", () => ({
  AgentsPage: ({
    search,
    onSearchChange,
  }: {
    search: AgentsRouteSearch
    onSearchChange: (search: AgentsRouteSearch, replace?: boolean) => void
  }) => (
    <div>
      <output data-testid="agents-search">{JSON.stringify(search)}</output>
      <button
        type="button"
        onClick={() =>
          onSearchChange({ agent: "reviewer", tab: "capabilities" })
        }
      >
        Manage reviewer
      </button>
      <button type="button" onClick={() => onSearchChange({})}>
        All agents
      </button>
    </div>
  ),
}))

vi.mock("@/features/chat/controller", () => ({
  initializeChatStore: vi.fn(),
}))

function renderAgentsRoute(pathname: string) {
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

describe("agents route navigation", () => {
  it("restores an exact canonical agent detail deep link", async () => {
    const router = renderAgentsRoute(
      "/agent/agents?agent=reviewer&tab=activity",
    )

    expect(await screen.findByTestId("agents-search")).toHaveTextContent(
      JSON.stringify({ agent: "reviewer", tab: "activity" }),
    )
    expect(router.state.location.search).toEqual({
      agent: "reviewer",
      tab: "activity",
    })
  })

  it("strictly removes noncanonical IDs and stray search keys", async () => {
    const router = renderAgentsRoute(
      "/agent/agents?agent=%20Reviewer%20&tab=activity&secret=canary",
    )

    await waitFor(() => {
      expect(router.state.location.search).toEqual({})
    })
    expect(screen.getByTestId("agents-search")).toHaveTextContent("{}")
  })

  it("removes repeated agent search values instead of choosing one", async () => {
    const router = renderAgentsRoute(
      "/agent/agents?agent=reviewer&agent=writer&tab=activity",
    )

    await waitFor(() => {
      expect(router.state.location.search).toEqual({})
    })
    expect(screen.getByTestId("agents-search")).toHaveTextContent("{}")
  })

  it("canonicalizes a missing or invalid tab to overview", async () => {
    const router = renderAgentsRoute("/agent/agents?agent=reviewer&tab=invalid")

    await waitFor(() => {
      expect(router.state.location.search).toEqual({
        agent: "reviewer",
        tab: "overview",
      })
    })
  })

  it("pushes selection and preserves browser back to the root grid", async () => {
    const router = renderAgentsRoute("/agent/agents")
    const user = userEvent.setup()

    await user.click(
      await screen.findByRole("button", { name: "Manage reviewer" }),
    )
    await waitFor(() => {
      expect(router.state.location.search).toEqual({
        agent: "reviewer",
        tab: "capabilities",
      })
    })
    expect(router.history.canGoBack()).toBe(true)

    router.history.back()
    await waitFor(() => expect(router.state.location.search).toEqual({}))
  })
})
