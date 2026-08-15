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
    initialProfileID?: string
    initialDecisionPoint?: string
    onProfileChange: (profileID?: string) => void
    onDecisionPointChange: (gate?: string) => void
  }) => {
    mockedGateProfilePage(props)
    return (
      <div>
        <output>
          {props.initialProfileID ? "Gate profile editor" : "Gate profile list"}
        </output>
        <button type="button" onClick={() => props.onProfileChange("strict")}>
          Edit strict profile
        </button>
        <button type="button" onClick={() => props.onProfileChange()}>
          Back to profiles
        </button>
        <button
          type="button"
          onClick={() => props.onDecisionPointChange("pr.implementation.scope")}
        >
          Select scope gate
        </button>
        <button type="button" onClick={() => props.onDecisionPointChange()}>
          Close gate
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

  it("renders the gate-profile list and scrubs unrelated state", async () => {
    const router = routerAt(
      `/pull-requests?view=gate-profiles&workspace=${workspaceID}&revision=private`,
    )
    render(<RouterProvider router={router} />)

    await waitFor(() => {
      expect(router.state.location.search).toEqual({ view: "gate-profiles" })
    })
    expect(screen.getByText("Gate profile list")).toBeVisible()
  })

  it("canonicalizes a gate-only deep link to the default profile editor", async () => {
    const router = routerAt(`/pull-requests?view=gate-profiles&gate=${gate}`)
    render(<RouterProvider router={router} />)

    await waitFor(() => {
      expect(router.state.location.search).toEqual({
        view: "gate-profiles",
        profile: "default",
        gate,
      })
    })
    expect(mockedGateProfilePage).toHaveBeenCalledWith(
      expect.objectContaining({
        initialProfileID: "default",
        initialDecisionPoint: gate,
      }),
    )
  })

  it("drops a malformed gate identity before opening the profile editor", async () => {
    const router = routerAt(
      "/pull-requests?view=gate-profiles&profile=strict&gate=pr.review",
    )
    render(<RouterProvider router={router} />)

    await waitFor(() => {
      expect(router.state.location.search).toEqual({
        view: "gate-profiles",
        profile: "strict",
      })
    })
    expect(mockedGateProfilePage).toHaveBeenLastCalledWith(
      expect.objectContaining({
        initialProfileID: "strict",
        initialDecisionPoint: undefined,
      }),
    )
  })

  it("moves from the profile list to a distinct editor URL", async () => {
    const router = routerAt("/pull-requests?view=gate-profiles")
    render(<RouterProvider router={router} />)

    expect(await screen.findByText("Gate profile list")).toBeVisible()
    fireEvent.click(screen.getByRole("button", { name: "Edit strict profile" }))
    await waitFor(() => {
      expect(router.state.location.search).toEqual({
        view: "gate-profiles",
        profile: "strict",
      })
    })
    expect(screen.getByText("Gate profile editor")).toBeVisible()

    fireEvent.click(screen.getByRole("button", { name: "Back to profiles" }))
    await waitFor(() => {
      expect(router.state.location.search).toEqual({
        view: "gate-profiles",
      })
    })
    expect(screen.getByText("Gate profile list")).toBeVisible()
  })

  it("opens and closes a gate modal while preserving the profile", async () => {
    const router = routerAt("/pull-requests?view=gate-profiles&profile=strict")
    render(<RouterProvider router={router} />)

    fireEvent.click(
      await screen.findByRole("button", { name: "Select scope gate" }),
    )
    await waitFor(() => {
      expect(router.state.location.search).toEqual({
        view: "gate-profiles",
        profile: "strict",
        gate: "pr.implementation.scope",
      })
    })

    fireEvent.click(screen.getByRole("button", { name: "Close gate" }))
    await waitFor(() => {
      expect(router.state.location.search).toEqual({
        view: "gate-profiles",
        profile: "strict",
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
