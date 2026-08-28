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

vi.mock("@/api/launcher-auth", () => ({
  getLauncherAuthStatus: vi
    .fn()
    .mockResolvedValue({ authenticated: true, initialized: true }),
}))
vi.mock("@/components/app-layout", () => ({
  AppLayout: ({ children }: { children: ReactNode }) => <div>{children}</div>,
}))
vi.mock("@/features/chat/controller", () => ({ initializeChatStore: vi.fn() }))

vi.mock("@/components/pr-workspaces/pr-lifecycle-admin-collections", () => ({
  PRLifecycleRepositoryAssignmentsCollectionPage: ({
    search,
    onOpen,
  }: {
    search: object
    onOpen: (item: { id: string }) => void
  }) => (
    <div>
      <output data-testid="repository-assignments-search">
        {JSON.stringify(search)}
      </output>
      <button type="button" onClick={() => onOpen({ id: "cmVwb3NpdG9yeQ" })}>
        Open repository assignment
      </button>
    </div>
  ),
  PRLifecycleRepositoryAssignmentDetailPage: ({
    assignmentID,
    onBack,
  }: {
    assignmentID: string
    onBack: () => void
  }) => (
    <div>
      <output data-testid="repository-assignment-detail">{assignmentID}</output>
      <button type="button" onClick={onBack}>
        All repository assignments
      </button>
    </div>
  ),
  PRLifecycleRepositoryAssignmentEditorPage: () => (
    <output>Repository assignment editor</output>
  ),
  PRLifecycleWorkflowConfigurationsCollectionPage: ({
    search,
    onEdit,
  }: {
    search: object
    onEdit: (item: { id: string }) => void
  }) => (
    <div>
      <output data-testid="workflow-configurations-search">
        {JSON.stringify(search)}
      </output>
      <button type="button" onClick={() => onEdit({ id: "automated" })}>
        Edit workflow configuration
      </button>
    </div>
  ),
  PRLifecycleWorkflowConfigurationDetailPage: ({
    configurationID,
    onBack,
  }: {
    configurationID: string
    onBack: () => void
  }) => (
    <div>
      <output data-testid="workflow-configuration-detail">
        {configurationID}
      </output>
      <button type="button" onClick={onBack}>
        All workflow configurations
      </button>
    </div>
  ),
  PRLifecycleWorkflowConfigurationCreatePage: () => (
    <output>New workflow configuration</output>
  ),
  PRLifecycleWorkflowConfigurationEditorPage: ({
    configurationID,
    flowID,
    onBack,
  }: {
    configurationID: string
    flowID: string
    onBack: () => void
  }) => (
    <div>
      <output data-testid="workflow-configuration-editor">
        {configurationID}:{flowID}
      </output>
      <button type="button" onClick={onBack}>
        Workflow configuration
      </button>
    </div>
  ),
}))

describe("development administrative collection routes", () => {
  it("hard-cuts legacy workflow configuration search state", async () => {
    const router = renderRoute(
      "/development/workflow-configurations?config=automated&flow=implementation&view=grid",
    )

    expect(
      await screen.findByTestId("workflow-configurations-search"),
    ).toHaveTextContent(
      JSON.stringify({ q: "ORDER BY name ASC", view: "grid" }),
    )
    await waitFor(() =>
      expect(router.state.location.search).toEqual({
        q: "ORDER BY name ASC",
        view: "grid",
      }),
    )
  })

  it("restores repository collection q/view from routed detail", async () => {
    const user = userEvent.setup()
    const router = renderRoute(
      "/development/repositories?q=configuration%20%3D%20strict&view=table",
    )

    await user.click(
      await screen.findByRole("button", {
        name: "Open repository assignment",
      }),
    )
    await waitFor(() =>
      expect(router.state.location.pathname).toBe(
        "/development/repositories/cmVwb3NpdG9yeQ",
      ),
    )
    expect(router.state.location.search).toEqual({
      q: "configuration = strict",
      view: "table",
    })
    await user.click(
      screen.getByRole("button", { name: "All repository assignments" }),
    )
    await waitFor(() =>
      expect(router.state.location.pathname).toBe("/development/repositories"),
    )
    expect(router.state.location.search).toEqual({
      q: "configuration = strict",
      view: "table",
    })
  })

  it("keeps collection state separate from routed Gate editor state", async () => {
    const user = userEvent.setup()
    const router = renderRoute(
      "/development/workflow-configurations?q=name%20~%20auto&view=grid",
    )

    await user.click(
      await screen.findByRole("button", {
        name: "Edit workflow configuration",
      }),
    )
    await waitFor(() =>
      expect(router.state.location.pathname).toBe(
        "/development/workflow-configurations/automated/edit",
      ),
    )
    expect(router.state.location.search).toEqual({
      q: "name ~ auto",
      view: "grid",
      flow: "review",
    })
    expect(
      screen.getByTestId("workflow-configuration-editor"),
    ).toHaveTextContent("automated:review")
    await user.click(
      screen.getByRole("button", { name: "Workflow configuration" }),
    )
    expect(
      await screen.findByTestId("workflow-configuration-detail"),
    ).toHaveTextContent("automated")
    expect(router.state.location.search).toEqual({
      q: "name ~ auto",
      view: "grid",
    })
  })
})

function renderRoute(pathname: string) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const router = createRouter({
    routeTree,
    history: createMemoryHistory({ initialEntries: [pathname] }),
    context: { queryClient },
  })
  render(
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
  return router
}
