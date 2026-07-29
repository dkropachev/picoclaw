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

import type { WorkflowsRouteSearch } from "../../components/workflows/workflow-route-search"

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

vi.mock("@/components/workflows/workflows-page", () => ({
  WorkflowsPage: ({
    search,
    onSearchChange,
  }: {
    search: WorkflowsRouteSearch
    onSearchChange: (search: WorkflowsRouteSearch, replace?: boolean) => void
  }) => (
    <div>
      <output data-testid="workflow-search">{JSON.stringify(search)}</output>
      <button
        type="button"
        onClick={() =>
          onSearchChange({
            mode: "operate",
            workflow: "workflows/github-issue-triage.yml",
            run: "wr_linked-run_01",
          })
        }
      >
        Select linked run
      </button>
      <button
        type="button"
        onClick={() => onSearchChange({ ...search, q: "failed" }, true)}
      >
        Filter runs
      </button>
    </div>
  ),
}))

vi.mock("@/features/chat/controller", () => ({
  initializeChatStore: vi.fn(),
}))

function renderWorkflowRoute(pathname: string) {
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

describe("workflow route navigation", () => {
  it("restores the exact event-dashboard deep link", async () => {
    const router = renderWorkflowRoute(
      "/agent/workflows?mode=operate&workflow=workflows%2Fgithub-issue-triage.yml&run=wr_linked-run_01",
    )

    expect(await screen.findByTestId("workflow-search")).toHaveTextContent(
      JSON.stringify({
        mode: "operate",
        workflow: "workflows/github-issue-triage.yml",
        run: "wr_linked-run_01",
      }),
    )
    expect(router.state.location.search).toEqual({
      mode: "operate",
      workflow: "workflows/github-issue-triage.yml",
      run: "wr_linked-run_01",
    })
  })

  it("normalizes invalid search values away", async () => {
    const router = renderWorkflowRoute(
      "/agent/workflows?mode=invalid&workflow=%20%20&run=other&q=%20",
    )

    await waitFor(() => {
      expect(screen.getByTestId("workflow-search")).toHaveTextContent("{}")
    })
    expect(router.state.matches.at(-1)?.search).toEqual({})
    expect(router.state.location.search).toEqual({})
  })

  it("pushes explicit selection and replaces live filter edits", async () => {
    const router = renderWorkflowRoute("/agent/workflows")
    const user = userEvent.setup()

    await user.click(
      await screen.findByRole("button", { name: "Select linked run" }),
    )
    await waitFor(() => {
      expect(router.state.location.search).toEqual({
        mode: "operate",
        workflow: "workflows/github-issue-triage.yml",
        run: "wr_linked-run_01",
      })
    })
    expect(router.history.canGoBack()).toBe(true)

    await user.click(screen.getByRole("button", { name: "Filter runs" }))
    await waitFor(() => {
      expect(router.state.location.search).toEqual({
        mode: "operate",
        workflow: "workflows/github-issue-triage.yml",
        run: "wr_linked-run_01",
        q: "failed",
      })
    })

    router.history.back()
    await waitFor(() => {
      expect(router.state.location.search).toEqual({})
    })
  })
})
