import { QueryClient } from "@tanstack/react-query"
import {
  RouterProvider,
  createMemoryHistory,
  createRouter,
} from "@tanstack/react-router"
import { fireEvent, render, screen, waitFor } from "@testing-library/react"
import type { ReactNode } from "react"
import { describe, expect, it, vi } from "vitest"

import { routeTree } from "@/routeTree.gen"

const workspaceID = `prw_${"a".repeat(32)}`
const gate = "pr.review.complete" as const
const mockedGateProfilePage = vi.hoisted(() => vi.fn())

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

vi.mock("@/components/pr-workspaces/pr-workspace-portfolio-page", () => ({
  PRWorkspacePortfolioPage: () => <output>Workspace portfolio</output>,
}))

vi.mock("@/components/pr-workspaces/pr-workspace-page", () => ({
  PRWorkspacePage: ({ workspaceID: selected }: { workspaceID: string }) => (
    <output data-testid="workspace-id">{selected}</output>
  ),
}))

vi.mock("@/components/pr-workspaces/pr-lifecycle-gate-profiles-page", () => ({
  PRLifecycleGateProfilesPage: (props: {
    initialDecisionPoint?: string
    onDecisionPointChange: (gate: string) => void
  }) => {
    mockedGateProfilePage(props)
    return (
      <div>
        <output>Gate profile editor</output>
        <button
          type="button"
          onClick={() => props.onDecisionPointChange("pr.implementation.scope")}
        >
          Select scope gate
        </button>
      </div>
    )
  },
}))

vi.mock("@/features/chat/controller", () => ({
  initializeChatStore: vi.fn(),
}))

function routerAt(entry: string) {
  return createRouter({
    routeTree,
    history: createMemoryHistory({ initialEntries: [entry] }),
    context: {
      queryClient: new QueryClient({
        defaultOptions: { queries: { retry: false } },
      }),
    },
  })
}

describe("pull requests route navigation", () => {
  it("renders one workspace and scrubs private state", async () => {
    const router = routerAt(
      `/pull-requests?workspace=${workspaceID}&cursor=opaque&prompt=private#secret`,
    )
    render(<RouterProvider router={router} />)

    await waitFor(() => {
      expect(router.state.location.search).toEqual({ workspace: workspaceID })
      expect(router.state.location.hash).toBe("")
    })
    expect(screen.getByTestId("workspace-id")).toHaveTextContent(workspaceID)
  })

  it("renders the exact gate-profile editor view", async () => {
    const router = routerAt(
      `/pull-requests?view=gate-profiles&workspace=${workspaceID}&revision=private`,
    )
    render(<RouterProvider router={router} />)

    await waitFor(() => {
      expect(router.state.location.search).toEqual({ view: "gate-profiles" })
    })
    expect(screen.getByText("Gate profile editor")).toBeVisible()
  })

  it("passes an allowlisted gate deep link to the profile editor", async () => {
    const router = routerAt(`/pull-requests?view=gate-profiles&gate=${gate}`)
    render(<RouterProvider router={router} />)

    await waitFor(() => {
      expect(router.state.location.search).toEqual({
        view: "gate-profiles",
        gate,
      })
    })
    expect(mockedGateProfilePage).toHaveBeenCalledWith(
      expect.objectContaining({ initialDecisionPoint: gate }),
    )
  })

  it("keeps an in-page gate selection in the canonical URL", async () => {
    const router = routerAt("/pull-requests?view=gate-profiles")
    render(<RouterProvider router={router} />)

    fireEvent.click(
      await screen.findByRole("button", { name: "Select scope gate" }),
    )
    await waitFor(() => {
      expect(router.state.location.search).toEqual({
        view: "gate-profiles",
        gate: "pr.implementation.scope",
      })
    })
  })

  it("drops removed review and development views", async () => {
    const router = routerAt(
      `/pull-requests?view=development&case=pdc_${"b".repeat(32)}`,
    )
    render(<RouterProvider router={router} />)

    await waitFor(() => expect(router.state.location.search).toEqual({}))
    expect(screen.getByText("Workspace portfolio")).toBeVisible()
  })
})
