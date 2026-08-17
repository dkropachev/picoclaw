import { QueryClient } from "@tanstack/react-query"
import {
  RouterProvider,
  createMemoryHistory,
  createRouter,
} from "@tanstack/react-router"
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react"
import type { ReactNode } from "react"
import { beforeEach, describe, expect, it, vi } from "vitest"

import { routeTree } from "@/routeTree.gen"

const workspaceID = `prw_${"a".repeat(32)}`
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
  PRWorkspacePortfolioPage: ({
    onOpenWorkspace,
    onOpenGateProfiles,
  }: {
    onOpenWorkspace: (workspaceID: string) => void
    onOpenGateProfiles: () => void
  }) => (
    <div>
      <output>Workspace portfolio</output>
      <button type="button" onClick={() => onOpenWorkspace(workspaceID)}>
        Open workspace
      </button>
      <button type="button" onClick={onOpenGateProfiles}>
        Open profiles from portfolio
      </button>
    </div>
  ),
}))

vi.mock("@/components/pr-workspaces/pr-workspace-page", () => ({
  PRWorkspacePage: ({
    workspaceID: selected,
    onBack,
    onOpenGateProfiles,
  }: {
    workspaceID: string
    onBack: () => void
    onOpenGateProfiles: () => void
  }) => (
    <div>
      <output data-testid="workspace-id">{selected}</output>
      <button type="button" onClick={onBack}>
        Back to portfolio
      </button>
      <button type="button" onClick={onOpenGateProfiles}>
        Open profiles from workspace
      </button>
    </div>
  ),
}))

interface GateProfilesPageProps {
  page?: "profiles" | "profile" | "settings"
  settingsTab?: "nudging" | "scope" | "deferred"
  initialProfileID?: string
  initialDecisionPoint?: string
  activeFlowID?: "review" | "implementation"
  discardOpen?: boolean
  onBack: () => void
  onProfileChange?: (profileID?: string) => void
  onDecisionPointChange?: (gate?: string) => void
  onFlowChange?: (flow: "review" | "implementation") => void
  onDiscardOpenChange?: (open: boolean) => void
  onSettingsTabChange?: (tab: "nudging" | "scope" | "deferred") => void
}

vi.mock("@/components/pr-workspaces/pr-lifecycle-gate-profiles-page", () => ({
  PRLifecycleGateProfilesPage: (props: GateProfilesPageProps) => {
    mockedGateProfilePage(props)
    return (
      <div>
        <output data-testid="configuration-page">{props.page}</output>
        <output data-testid="profile-id">{props.initialProfileID}</output>
        <output data-testid="flow-id">{props.activeFlowID}</output>
        <output data-testid="gate-id">{props.initialDecisionPoint}</output>
        <output data-testid="settings-tab">{props.settingsTab}</output>
        <output data-testid="discard-open">
          {props.discardOpen ? "open" : "closed"}
        </output>
        <button type="button" onClick={props.onBack}>
          Back
        </button>
        <button type="button" onClick={() => props.onProfileChange?.("strict")}>
          Edit strict profile
        </button>
        <button type="button" onClick={() => props.onProfileChange?.()}>
          Back to profiles
        </button>
        <button
          type="button"
          onClick={() =>
            props.onDecisionPointChange?.("pr.implementation.scope")
          }
        >
          Select scope gate
        </button>
        <button type="button" onClick={() => props.onDecisionPointChange?.()}>
          Close gate
        </button>
        <button
          type="button"
          onClick={() => props.onFlowChange?.("implementation")}
        >
          Implementation flow
        </button>
        <button type="button" onClick={() => props.onDiscardOpenChange?.(true)}>
          Open discard
        </button>
        <button
          type="button"
          onClick={() => props.onDiscardOpenChange?.(false)}
        >
          Close discard
        </button>
        <button
          type="button"
          onClick={() => props.onSettingsTabChange?.("deferred")}
        >
          Deferred tab
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

async function expectLocation(
  router: ReturnType<typeof routerAt>,
  pathname: string,
  search: Record<string, unknown> = {},
) {
  await waitFor(() => {
    expect(router.state.location.pathname).toBe(pathname)
    expect(router.state.location.search).toEqual(search)
    expect(router.state.location.hash).toBe("")
  })
}

describe("pull requests route navigation", () => {
  beforeEach(() => mockedGateProfilePage.mockClear())

  it("scrubs the retired query-based views from the portfolio URL", async () => {
    const router = routerAt(
      `/pull-requests?view=gate-profiles&profile=strict&workspace=${workspaceID}#private`,
    )
    render(<RouterProvider router={router} />)

    await expectLocation(router, "/pull-requests")
    expect(screen.getByText("Workspace portfolio")).toBeVisible()
  })

  it("opens a workspace at its own URL and reaches profiles from there", async () => {
    const router = routerAt("/pull-requests")
    render(<RouterProvider router={router} />)

    fireEvent.click(
      await screen.findByRole("button", { name: "Open workspace" }),
    )
    await expectLocation(router, `/pull-requests/${workspaceID}`)
    expect(screen.getByTestId("workspace-id")).toHaveTextContent(workspaceID)

    fireEvent.click(
      screen.getByRole("button", { name: "Open profiles from workspace" }),
    )
    await expectLocation(router, "/pull-requests/profiles", {
      from: workspaceID,
    })
    expect(screen.getByTestId("configuration-page")).toHaveTextContent(
      "profiles",
    )

    fireEvent.click(screen.getByRole("button", { name: "Edit strict profile" }))
    await expectLocation(router, "/pull-requests/profiles/strict", {
      flow: "review",
      from: workspaceID,
    })
    fireEvent.click(screen.getByRole("button", { name: "Implementation flow" }))
    await expectLocation(router, "/pull-requests/profiles/strict", {
      flow: "implementation",
      from: workspaceID,
    })
    fireEvent.click(screen.getByRole("button", { name: "Back to profiles" }))
    await expectLocation(router, "/pull-requests/profiles", {
      from: workspaceID,
    })

    fireEvent.click(screen.getByRole("button", { name: "Back" }))
    await expectLocation(router, `/pull-requests/${workspaceID}`)
  })

  it("opens profiles directly from the portfolio", async () => {
    const router = routerAt("/pull-requests")
    render(<RouterProvider router={router} />)

    fireEvent.click(
      await screen.findByRole("button", {
        name: "Open profiles from portfolio",
      }),
    )
    await expectLocation(router, "/pull-requests/profiles")
  })

  it("scrubs private workspace state and redirects an invalid workspace", async () => {
    const router = routerAt(
      `/pull-requests/${workspaceID}?prompt=private#private`,
    )
    const view = render(<RouterProvider router={router} />)

    await expectLocation(router, `/pull-requests/${workspaceID}`)
    expect(screen.getByTestId("workspace-id")).toHaveTextContent(workspaceID)

    view.unmount()
    const invalidRouter = routerAt("/pull-requests/not-a-workspace")
    render(<RouterProvider router={invalidRouter} />)
    await expectLocation(invalidRouter, "/pull-requests")
    expect(screen.getByText("Workspace portfolio")).toBeVisible()
  })

  it("gives the profile list and each profile editor distinct URLs", async () => {
    const router = routerAt(
      "/pull-requests/profiles?view=gate-profiles&profile=strict#private",
    )
    render(<RouterProvider router={router} />)

    await expectLocation(router, "/pull-requests/profiles")
    expect(screen.getByTestId("configuration-page")).toHaveTextContent(
      "profiles",
    )

    fireEvent.click(screen.getByRole("button", { name: "Edit strict profile" }))
    await expectLocation(router, "/pull-requests/profiles/strict", {
      flow: "review",
    })
    expect(screen.getByTestId("configuration-page")).toHaveTextContent(
      "profile",
    )
    expect(screen.getByTestId("profile-id")).toHaveTextContent("strict")

    fireEvent.click(screen.getByRole("button", { name: "Back to profiles" }))
    await expectLocation(router, "/pull-requests/profiles")
  })

  it("deep-links a profile flow and gate modal while scrubbing extra state", async () => {
    const router = routerAt(
      "/pull-requests/profiles/strict?flow=implementation&gate=pr.implementation.scope&view=gate-profiles#private",
    )
    render(<RouterProvider router={router} />)

    await expectLocation(router, "/pull-requests/profiles/strict", {
      flow: "implementation",
      gate: "pr.implementation.scope",
    })
    expect(mockedGateProfilePage).toHaveBeenLastCalledWith(
      expect.objectContaining({
        page: "profile",
        activeFlowID: "implementation",
        initialProfileID: "strict",
        initialDecisionPoint: "pr.implementation.scope",
      }),
    )
  })

  it("redirects an invalid profile path to the profile list", async () => {
    const router = routerAt(
      "/pull-requests/profiles/Invalid%20Profile?flow=implementation",
    )
    render(<RouterProvider router={router} />)

    await expectLocation(router, "/pull-requests/profiles")
    expect(screen.getByTestId("configuration-page")).toHaveTextContent(
      "profiles",
    )
  })

  it("writes flow and gate selection to history without reopening a closed gate", async () => {
    const router = routerAt("/pull-requests/profiles/strict?flow=review")
    render(<RouterProvider router={router} />)

    fireEvent.click(
      await screen.findByRole("button", { name: "Implementation flow" }),
    )
    await expectLocation(router, "/pull-requests/profiles/strict", {
      flow: "implementation",
    })

    fireEvent.click(screen.getByRole("button", { name: "Select scope gate" }))
    await expectLocation(router, "/pull-requests/profiles/strict", {
      flow: "implementation",
      gate: "pr.implementation.scope",
    })

    fireEvent.click(screen.getByRole("button", { name: "Close gate" }))
    await expectLocation(router, "/pull-requests/profiles/strict", {
      flow: "implementation",
    })

    act(() => router.history.back())
    await expectLocation(router, "/pull-requests/profiles/strict", {
      flow: "review",
    })
    expect(screen.getByTestId("gate-id")).toBeEmptyDOMElement()
  })

  it("preserves a gate while correcting its workflow context", async () => {
    const router = routerAt(
      "/pull-requests/profiles/strict?flow=review&gate=pr.implementation.scope",
    )
    render(<RouterProvider router={router} />)

    fireEvent.click(
      await screen.findByRole("button", { name: "Implementation flow" }),
    )
    await expectLocation(router, "/pull-requests/profiles/strict", {
      flow: "implementation",
      gate: "pr.implementation.scope",
    })
  })

  it("keeps the owning gate while opening and closing profile discard", async () => {
    const router = routerAt(
      "/pull-requests/profiles/strict?flow=review&gate=pr.review.complete",
    )
    render(<RouterProvider router={router} />)

    fireEvent.click(await screen.findByRole("button", { name: "Open discard" }))
    await expectLocation(router, "/pull-requests/profiles/strict", {
      flow: "review",
      gate: "pr.review.complete",
      dialog: "discard",
    })
    expect(screen.getByTestId("discard-open")).toHaveTextContent("open")

    fireEvent.click(screen.getByRole("button", { name: "Close discard" }))
    await expectLocation(router, "/pull-requests/profiles/strict", {
      flow: "review",
      gate: "pr.review.complete",
    })

    act(() => router.history.back())
    await expectLocation(router, "/pull-requests/profiles/strict", {
      flow: "review",
      gate: "pr.review.complete",
    })
    expect(screen.getByTestId("discard-open")).toHaveTextContent("closed")
  })

  it("keeps settings separate, preserves its workspace origin, and gives every tab a URL", async () => {
    const router = routerAt(
      `/pull-requests/settings?tab=nudging&from=${workspaceID}`,
    )
    render(<RouterProvider router={router} />)

    await expectLocation(router, "/pull-requests/settings", {
      tab: "nudging",
      from: workspaceID,
    })
    expect(screen.getByTestId("configuration-page")).toHaveTextContent(
      "settings",
    )
    expect(screen.getByTestId("settings-tab")).toHaveTextContent("nudging")

    fireEvent.click(screen.getByRole("button", { name: "Deferred tab" }))
    await expectLocation(router, "/pull-requests/settings", {
      tab: "deferred",
      from: workspaceID,
    })
    expect(screen.getByTestId("settings-tab")).toHaveTextContent("deferred")

    fireEvent.click(screen.getByRole("button", { name: "Back" }))
    await expectLocation(router, `/pull-requests/${workspaceID}`)
  })

  it("canonicalizes settings deep links and URL-backs the discard modal", async () => {
    const router = routerAt(
      "/pull-requests/settings?tab=scope&dialog=discard&view=gate-profiles#private",
    )
    render(<RouterProvider router={router} />)

    await expectLocation(router, "/pull-requests/settings", {
      tab: "scope",
      dialog: "discard",
    })
    expect(mockedGateProfilePage).toHaveBeenLastCalledWith(
      expect.objectContaining({
        page: "settings",
        settingsTab: "scope",
        discardOpen: true,
      }),
    )

    fireEvent.click(screen.getByRole("button", { name: "Close discard" }))
    await expectLocation(router, "/pull-requests/settings", { tab: "scope" })
    expect(screen.getByTestId("discard-open")).toHaveTextContent("closed")
  })
})
