import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import {
  RouterProvider,
  createMemoryHistory,
  createRouter,
} from "@tanstack/react-router"
import { render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import type { ReactNode } from "react"
import { describe, expect, it, vi } from "vitest"

import type { GitWorkspaceSummary } from "@/api/git-workspaces"
import type { CollectionRouteSearch } from "@/hooks/use-collection-route-state"
import { routeTree } from "@/routeTree.gen"

const workspaceID = "gw-0123456789ab"
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
vi.mock("@/components/agent/git-workspaces/git-workspace-collections", () => ({
  GitWorkspacesCollectionPage: ({
    search,
    onSearchChange,
    onOpen,
    onHistory,
    onSettings,
  }: {
    search: CollectionRouteSearch
    onSearchChange: (search: CollectionRouteSearch, replace?: boolean) => void
    onOpen: (workspace: GitWorkspaceSummary) => void
    onHistory: () => void
    onSettings: () => void
  }) => (
    <div>
      <output data-testid="git-workspace-search">
        {JSON.stringify(search)}
      </output>
      <button
        type="button"
        onClick={() =>
          onSearchChange({ ...search, q: "status = locked" }, true)
        }
      >
        Filter workspaces
      </button>
      <button
        type="button"
        onClick={() =>
          onOpen({
            id: workspaceID,
            repository: "team/repo",
            branch: "main",
            status: "available",
            locked: false,
            dirty: false,
            size: 1,
            ignored: 0,
            updated: "2026-08-01T00:00:00Z",
          })
        }
      >
        Open workspace
      </button>
      <button type="button" onClick={onHistory}>
        Open history
      </button>
      <button type="button" onClick={onSettings}>
        Open settings
      </button>
    </div>
  ),
  GitWorkspaceHistoryCollectionPage: () => <div>History route</div>,
}))
vi.mock("@/components/agent/git-workspaces/git-workspace-detail-page", () => ({
  GitWorkspaceDetailPage: ({ workspaceID }: { workspaceID: string }) => (
    <div>Detail {workspaceID}</div>
  ),
}))
vi.mock(
  "@/components/agent/git-workspaces/git-workspace-settings-page",
  () => ({
    GitWorkspaceSettingsPage: () => <div>Settings route</div>,
  }),
)
vi.mock("@/features/chat/controller", () => ({ initializeChatStore: vi.fn() }))

function renderRoute(path: string) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const router = createRouter({
    routeTree,
    history: createMemoryHistory({ initialEntries: [path] }),
    context: { queryClient },
  })
  render(
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
  return router
}

describe("git workspace routes", () => {
  it("hard-cuts unknown legacy search into canonical q/view", async () => {
    const router = renderRoute(
      "/agent/git-workspaces?filter=locked&workspace=old&q=status%20%3D%20locked&view=grid",
    )
    expect(await screen.findByTestId("git-workspace-search")).toHaveTextContent(
      JSON.stringify({ q: "status = locked", view: "grid" }),
    )
    await waitFor(() =>
      expect(router.state.location.search).toEqual({
        q: "status = locked",
        view: "grid",
      }),
    )
  })

  it("preserves collection state through direct detail", async () => {
    const user = userEvent.setup()
    const router = renderRoute(
      "/agent/git-workspaces?q=status%20%3D%20available&view=table",
    )
    await user.click(
      await screen.findByRole("button", { name: "Open workspace" }),
    )
    await waitFor(() =>
      expect(router.state.location.pathname).toBe(
        `/agent/git-workspaces/${workspaceID}`,
      ),
    )
    expect(router.state.location.search).toEqual({
      q: "status = available",
      view: "table",
    })
  })

  it("uses dedicated history and settings routes", async () => {
    const user = userEvent.setup()
    const router = renderRoute("/agent/git-workspaces")
    await waitFor(() =>
      expect(router.state.location.search).toEqual({
        q: "ORDER BY updated DESC",
      }),
    )
    await user.click(screen.getByRole("button", { name: "Open history" }))
    await waitFor(() =>
      expect(router.state.location.pathname).toBe(
        "/agent/git-workspaces/history",
      ),
    )
    router.history.back()
    await waitFor(() =>
      expect(router.state.location.pathname).toBe("/agent/git-workspaces"),
    )
    await user.click(screen.getByRole("button", { name: "Open settings" }))
    await waitFor(() =>
      expect(router.state.location.pathname).toBe(
        "/agent/git-workspaces/settings",
      ),
    )
  })
})
