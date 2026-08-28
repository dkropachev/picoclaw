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

import { routeTree } from "@/routeTree.gen"

const workspaceID = `devw_${"1".repeat(32)}`

vi.mock("@/api/launcher-auth", () => ({
  getLauncherAuthStatus: vi
    .fn()
    .mockResolvedValue({ authenticated: true, initialized: true }),
}))
vi.mock("@/components/app-layout", () => ({
  AppLayout: ({ children }: { children: ReactNode }) => <div>{children}</div>,
}))
vi.mock("@/features/chat/controller", () => ({ initializeChatStore: vi.fn() }))

vi.mock(
  "@/components/development-workspaces/development-portfolio-page",
  () => ({
    DevelopmentPortfolioPage: ({
      search,
      onCreate,
      onOpenWorkspace,
    }: {
      search: object
      onCreate: () => void
      onOpenWorkspace: (id: string) => void
    }) => (
      <div>
        <output data-testid="development-search">
          {JSON.stringify(search)}
        </output>
        <button type="button" onClick={onCreate}>
          New work
        </button>
        <button type="button" onClick={() => onOpenWorkspace(workspaceID)}>
          Open workspace
        </button>
      </div>
    ),
  }),
)

vi.mock(
  "@/components/development-workspaces/development-workspace-page",
  () => ({
    DevelopmentWorkspacePage: ({
      workspaceID: id,
      tab,
      selectedPath,
      onBack,
      onTabChange,
      onPathChange,
    }: {
      workspaceID: string
      tab: string
      selectedPath?: string
      onBack: () => void
      onTabChange: (tab: "files") => void
      onPathChange: (path: string, revision: string) => void
    }) => (
      <div>
        <output data-testid="workspace-route-state">
          {id}:{tab}:{selectedPath ?? ""}
        </output>
        <button type="button" onClick={onBack}>
          All development workspaces
        </button>
        <button type="button" onClick={() => onTabChange("files")}>
          Files
        </button>
        <button
          type="button"
          onClick={() => onPathChange("src/retry.ts", "candidate:2")}
        >
          Open code path
        </button>
      </div>
    ),
  }),
)

vi.mock("@/components/development-workspaces/development-intake-page", () => ({
  DevelopmentIntakePage: ({
    initialIssueURL,
    onBack,
    onCreated,
  }: {
    initialIssueURL?: string
    onBack: () => void
    onCreated: (id: string) => void
  }) => (
    <div>
      <output data-testid="intake-issue">{initialIssueURL ?? ""}</output>
      <button type="button" onClick={onBack}>
        Back from intake
      </button>
      <button type="button" onClick={() => onCreated(workspaceID)}>
        Create workspace
      </button>
    </div>
  ),
}))

describe("development workspace collection routes", () => {
  it("canonicalizes list q/view and removes legacy filter state", async () => {
    const router = renderRoute(
      "/development?filter=retry&q=phase%20%3D%20implementation&view=grid",
    )
    expect(await screen.findByTestId("development-search")).toHaveTextContent(
      JSON.stringify({ q: "phase = implementation", view: "grid" }),
    )
    await waitFor(() =>
      expect(router.state.location.search).toEqual({
        q: "phase = implementation",
        view: "grid",
      }),
    )
  })

  it("preserves collection state through detail tabs, paths, and Back", async () => {
    const user = userEvent.setup()
    const router = renderRoute(
      "/development?q=phase%20%3D%20implementation&view=table",
    )
    await user.click(
      await screen.findByRole("button", { name: "Open workspace" }),
    )
    await waitFor(() =>
      expect(router.state.location.pathname).toBe(
        `/development/${workspaceID}`,
      ),
    )
    expect(router.state.location.search).toEqual({
      q: "phase = implementation",
      view: "table",
      tab: "overview",
    })
    await user.click(screen.getByRole("button", { name: "Files" }))
    await user.click(screen.getByRole("button", { name: "Open code path" }))
    await waitFor(() =>
      expect(router.state.location.search).toEqual({
        q: "phase = implementation",
        view: "table",
        tab: "files",
        path: "src/retry.ts",
        revision: "candidate:2",
      }),
    )
    await user.click(
      screen.getByRole("button", { name: "All development workspaces" }),
    )
    await waitFor(() =>
      expect(router.state.location.pathname).toBe("/development"),
    )
    expect(router.state.location.search).toEqual({
      q: "phase = implementation",
      view: "table",
    })
  })

  it("preserves q/view beside New issue state and created detail", async () => {
    const user = userEvent.setup()
    const router = renderRoute(
      "/development/new?q=source%20%3D%20issue&view=grid&issue=https%3A%2F%2Fgithub.com%2Focto%2Frepo%2Fissues%2F42",
    )
    expect(await screen.findByTestId("intake-issue")).toHaveTextContent(
      "https://github.com/octo/repo/issues/42",
    )
    await user.click(screen.getByRole("button", { name: "Create workspace" }))
    await waitFor(() =>
      expect(router.state.location.pathname).toBe(
        `/development/${workspaceID}`,
      ),
    )
    expect(router.state.location.search).toEqual({
      q: "source = issue",
      view: "grid",
      tab: "overview",
    })
  })
})

function renderRoute(initialEntry: string) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const router = createRouter({
    routeTree,
    history: createMemoryHistory({ initialEntries: [initialEntry] }),
    context: { queryClient },
  })
  render(
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
  return router
}
