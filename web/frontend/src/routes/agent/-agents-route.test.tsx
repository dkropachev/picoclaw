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

vi.mock("@/components/collections/pilots/agent-collection", () => {
  const defaultQuery = "ORDER BY position ASC"
  const normalize = (raw: object) => {
    const value = raw as Record<string, unknown>
    const q =
      typeof value.q === "string" && value.q.trim()
        ? value.q.trim()
        : defaultQuery
    const view = ["list", "table", "grid"].includes(String(value.view))
      ? String(value.view)
      : undefined
    return { q, ...(view ? { view } : {}) }
  }
  return {
    normalizeAgentCollectionSearch: normalize,
    agentCollectionSearchIsCanonical: (
      raw: object,
      normalized: Record<string, unknown>,
    ) => {
      const value = raw as Record<string, unknown>
      const keys = Object.keys(value).filter((key) => value[key] !== undefined)
      return (
        keys.length === Object.keys(normalized).length &&
        value.q === normalized.q &&
        value.view === normalized.view
      )
    },
    AgentCollectionPage: ({
      search,
      onAdd,
      onOpen,
      onEdit,
      onCapabilities,
      onActivity,
    }: {
      search: object
      onAdd: () => void
      onOpen: (agent: { id: string }) => void
      onEdit: (agent: { id: string }) => void
      onCapabilities: (agent: { id: string }) => void
      onActivity: (agent: { id: string }) => void
    }) => (
      <div>
        <output data-testid="agents-search">{JSON.stringify(search)}</output>
        <button type="button" onClick={onAdd}>
          Add agent
        </button>
        <button type="button" onClick={() => onOpen({ id: "reviewer" })}>
          Open reviewer
        </button>
        <button type="button" onClick={() => onEdit({ id: "reviewer" })}>
          Edit reviewer
        </button>
        <button
          type="button"
          onClick={() => onCapabilities({ id: "reviewer" })}
        >
          Reviewer capabilities
        </button>
        <button type="button" onClick={() => onActivity({ id: "reviewer" })}>
          Reviewer activity
        </button>
      </div>
    ),
    AgentCollectionDetailPage: ({
      agentID,
      onBack,
      onEdit,
      onCapabilities,
      onActivity,
    }: {
      agentID: string
      onBack: () => void
      onEdit: () => void
      onCapabilities: () => void
      onActivity: () => void
    }) => (
      <div>
        <output data-testid="agent-detail">{agentID}</output>
        <button type="button" onClick={onBack}>
          Back to agents
        </button>
        <button type="button" onClick={onEdit}>
          Edit agent
        </button>
        <button type="button" onClick={onCapabilities}>
          Capabilities
        </button>
        <button type="button" onClick={onActivity}>
          Activity
        </button>
      </div>
    ),
    AgentCollectionEditorPage: ({
      mode,
      agentID,
      onBack,
      onSaved,
    }: {
      mode: "create" | "edit"
      agentID?: string
      onBack: () => void
      onSaved: (id: string) => void
    }) => (
      <div>
        <output data-testid="agent-editor">{`${mode}:${agentID ?? "new"}`}</output>
        <button type="button" onClick={onBack}>
          Cancel editor
        </button>
        <button type="button" onClick={() => onSaved(agentID ?? "writer")}>
          Save editor
        </button>
      </div>
    ),
    AgentCollectionCapabilitiesPage: ({
      agentID,
      onBack,
      onEdit,
    }: {
      agentID: string
      onBack: () => void
      onEdit: () => void
    }) => (
      <div>
        <output data-testid="agent-capabilities">{agentID}</output>
        <button type="button" onClick={onBack}>
          Back to agent
        </button>
        <button type="button" onClick={onEdit}>
          Edit agent
        </button>
      </div>
    ),
    AgentCollectionActivityPage: ({
      agentID,
      onBack,
      onEdit,
    }: {
      agentID: string
      onBack: () => void
      onEdit: () => void
    }) => (
      <div>
        <output data-testid="agent-activity">{agentID}</output>
        <button type="button" onClick={onBack}>
          Back to agent
        </button>
        <button type="button" onClick={onEdit}>
          Edit agent
        </button>
      </div>
    ),
  }
})

vi.mock("@/features/chat/controller", () => ({
  initializeChatStore: vi.fn(),
}))

function renderAgentsRoute(pathname: string) {
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

describe("agents collection route navigation", () => {
  it("keeps only canonical q/view state and does not compatibility-render legacy detail search", async () => {
    const router = renderAgentsRoute(
      "/agent/agents?agent=reviewer&tab=activity&q=name%20~%20review&view=grid",
    )

    expect(await screen.findByTestId("agents-search")).toHaveTextContent(
      JSON.stringify({ q: "name ~ review", view: "grid" }),
    )
    await waitFor(() => {
      expect(router.state.location.search).toEqual({
        q: "name ~ review",
        view: "grid",
      })
    })
    expect(screen.queryByTestId("agent-detail")).toBeNull()
  })

  it("opens a dedicated detail route and preserves collection state on return", async () => {
    const router = renderAgentsRoute(
      "/agent/agents?q=default%20%3D%20false&view=table",
    )
    const user = userEvent.setup()

    await user.click(
      await screen.findByRole("button", { name: "Open reviewer" }),
    )
    await waitFor(() => {
      expect(router.state.location.pathname).toBe("/agent/agents/reviewer")
    })
    expect(router.state.location.search).toEqual({
      q: "default = false",
      view: "table",
    })
    expect(screen.getByTestId("agent-detail")).toHaveTextContent("reviewer")

    await user.click(screen.getByRole("button", { name: "Back to agents" }))
    await waitFor(() => {
      expect(router.state.location.pathname).toBe("/agent/agents")
    })
    expect(router.state.location.search).toEqual({
      q: "default = false",
      view: "table",
    })
  })

  it("uses dedicated new and edit routes and saves to direct detail", async () => {
    const router = renderAgentsRoute(
      "/agent/agents?q=ORDER%20BY%20position%20ASC",
    )
    const user = userEvent.setup()

    await user.click(await screen.findByRole("button", { name: "Add agent" }))
    await waitFor(() =>
      expect(router.state.location.pathname).toBe("/agent/agents/new"),
    )
    expect(screen.getByTestId("agent-editor")).toHaveTextContent("create:new")
    await user.click(screen.getByRole("button", { name: "Save editor" }))
    await waitFor(() =>
      expect(router.state.location.pathname).toBe("/agent/agents/writer"),
    )

    await user.click(screen.getByRole("button", { name: "Edit agent" }))
    await waitFor(() =>
      expect(router.state.location.pathname).toBe("/agent/agents/writer/edit"),
    )
    expect(screen.getByTestId("agent-editor")).toHaveTextContent("edit:writer")
  })

  it("addresses capabilities and activity as sibling routes without a detail Outlet", async () => {
    const router = renderAgentsRoute(
      "/agent/agents/reviewer/capabilities?q=id%20%3D%20reviewer&view=list",
    )
    const user = userEvent.setup()

    expect(await screen.findByTestId("agent-capabilities")).toHaveTextContent(
      "reviewer",
    )
    expect(screen.queryByTestId("agent-detail")).toBeNull()
    await user.click(screen.getByRole("button", { name: "Back to agent" }))
    await waitFor(() =>
      expect(router.state.location.pathname).toBe("/agent/agents/reviewer"),
    )
    await user.click(screen.getByRole("button", { name: "Activity" }))
    await waitFor(() =>
      expect(router.state.location.pathname).toBe(
        "/agent/agents/reviewer/activity",
      ),
    )
    expect(screen.getByTestId("agent-activity")).toHaveTextContent("reviewer")
    expect(router.state.location.search).toEqual({
      q: "id = reviewer",
      view: "list",
    })
  })

  it("browser Back returns from detail to the same list location", async () => {
    const router = renderAgentsRoute(
      "/agent/agents?q=implicit%20%3D%20false&view=grid",
    )
    const user = userEvent.setup()
    await user.click(
      await screen.findByRole("button", { name: "Open reviewer" }),
    )
    await waitFor(() =>
      expect(router.state.location.pathname).toBe("/agent/agents/reviewer"),
    )
    router.history.back()
    await waitFor(() =>
      expect(router.state.location.pathname).toBe("/agent/agents"),
    )
    expect(router.state.location.search).toEqual({
      q: "implicit = false",
      view: "grid",
    })
  })
})
