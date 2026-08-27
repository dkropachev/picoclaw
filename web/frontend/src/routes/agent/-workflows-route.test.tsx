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

import type { WorkflowDefinitionSummary } from "@/api/workflows"
import type { CollectionRouteSearch } from "@/hooks/use-collection-route-state"
import { routeTree } from "@/routeTree.gen"

const workflowID = "a".repeat(43)

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

vi.mock("@/components/workflows/workflow-collections", () => ({
  WorkflowDefinitionsCollectionPage: ({
    search,
    onSearchChange,
    onOpen,
  }: {
    search: CollectionRouteSearch
    onSearchChange: (search: CollectionRouteSearch, replace?: boolean) => void
    onOpen: (workflow: WorkflowDefinitionSummary) => void
  }) => (
    <div>
      <output data-testid="workflow-search">{JSON.stringify(search)}</output>
      <button
        type="button"
        onClick={() => onSearchChange({ ...search, q: "status = valid" }, true)}
      >
        Filter workflows
      </button>
      <button
        type="button"
        onClick={() =>
          onOpen({
            id: workflowID,
            ref: "workflows/review.yml",
            status: "valid",
            trigger: "manual",
            inputs: 0,
            secrets: 0,
          })
        }
      >
        Open workflow
      </button>
    </div>
  ),
}))

vi.mock("@/features/chat/controller", () => ({ initializeChatStore: vi.fn() }))

function renderWorkflowRoute(pathname: string) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const router = createRouter({
    routeTree,
    history: createMemoryHistory({ initialEntries: [pathname] }),
    context: {
      queryClient,
    },
  })
  render(
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
  return router
}

describe("workflow collection routes", () => {
  it("hard-cuts legacy mode, workflow, and run search state", async () => {
    const router = renderWorkflowRoute(
      "/agent/workflows?mode=operate&workflow=workflows%2Freview.yml&run=wr_old&q=status%20%3D%20valid&view=grid",
    )

    expect(await screen.findByTestId("workflow-search")).toHaveTextContent(
      JSON.stringify({ q: "status = valid", view: "grid" }),
    )
    await waitFor(() =>
      expect(router.state.location.search).toEqual({
        q: "status = valid",
        view: "grid",
      }),
    )
  })

  it("injects the canonical default query and drops invalid views", async () => {
    const router = renderWorkflowRoute("/agent/workflows?view=other")
    await waitFor(() =>
      expect(router.state.location.search).toEqual({ q: "ORDER BY ref ASC" }),
    )
  })

  it("replaces live query edits", async () => {
    const router = renderWorkflowRoute("/agent/workflows")
    const user = userEvent.setup()
    await waitFor(() =>
      expect(router.state.location.search).toEqual({ q: "ORDER BY ref ASC" }),
    )
    await user.click(
      await screen.findByRole("button", { name: "Filter workflows" }),
    )
    await waitFor(() =>
      expect(router.state.location.search).toEqual({ q: "status = valid" }),
    )
  })

  it("preserves collection state through direct stable-ID detail", async () => {
    const router = renderWorkflowRoute(
      "/agent/workflows?q=status%20%3D%20valid&view=table",
    )
    const user = userEvent.setup()
    await user.click(
      await screen.findByRole("button", { name: "Open workflow" }),
    )
    await waitFor(() =>
      expect(router.state.location.pathname).toBe(
        `/agent/workflows/${workflowID}`,
      ),
    )
    expect(router.state.location.search).toEqual({
      q: "status = valid",
      view: "table",
    })
  })
})
