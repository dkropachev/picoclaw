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

vi.mock("@/components/events/event-source-collections", () => ({
  EventSourcesCollectionPage: ({
    search,
    onAdd,
    onOpen,
    onEdit,
    onSettings,
  }: {
    search: object
    onAdd: () => void
    onOpen: (source: { id: string }) => void
    onEdit: (source: { id: string }) => void
    onSettings: () => void
  }) => (
    <div>
      <output data-testid="event-sources-search">
        {JSON.stringify(search)}
      </output>
      <button type="button" onClick={onAdd}>
        Add source
      </button>
      <button type="button" onClick={() => onOpen({ id: "opaque-source" })}>
        Open source
      </button>
      <button type="button" onClick={() => onEdit({ id: "opaque-source" })}>
        Edit source
      </button>
      <button type="button" onClick={onSettings}>
        Source settings
      </button>
    </div>
  ),
  EventSourceDetailPage: ({
    id,
    onEdit,
  }: {
    id: string
    onEdit: () => void
  }) => (
    <div>
      <output data-testid="event-source-detail">{id}</output>
      <button type="button" onClick={onEdit}>
        Edit detail
      </button>
    </div>
  ),
}))
vi.mock("@/components/events/event-source-editor-page", () => ({
  EventSourceEditorPage: ({
    mode,
    id,
    onSaved,
  }: {
    mode: string
    id?: string
    onSaved: (id: string) => void
  }) => (
    <div>
      <output data-testid="event-source-editor">{`${mode}:${id ?? "new"}`}</output>
      <button type="button" onClick={() => onSaved(id ?? "created-source")}>
        Complete save
      </button>
    </div>
  ),
}))
vi.mock("@/components/events/event-source-settings-page", () => ({
  EventSourceSettingsPage: () => (
    <output data-testid="event-source-settings">settings</output>
  ),
}))

describe("event source collection routes", () => {
  it("canonicalizes q/view and removes unrelated aggregate state", async () => {
    const router = renderRoute(
      "/event-sources?tab=storage&q=kind%20%3D%20webhook&view=grid",
    )
    expect(await screen.findByTestId("event-sources-search")).toHaveTextContent(
      JSON.stringify({ q: "kind = webhook", view: "grid" }),
    )
    await waitFor(() =>
      expect(router.state.location.search).toEqual({
        q: "kind = webhook",
        view: "grid",
      }),
    )
  })

  it("preserves collection state through direct detail and edit", async () => {
    const user = userEvent.setup()
    const router = renderRoute(
      "/event-sources?q=status%20%3D%20available&view=table",
    )
    await user.click(await screen.findByRole("button", { name: "Open source" }))
    await waitFor(() =>
      expect(router.state.location.pathname).toBe(
        "/event-sources/opaque-source",
      ),
    )
    expect(router.state.location.search).toEqual({
      q: "status = available",
      view: "table",
    })
    expect(screen.getByTestId("event-source-detail")).toHaveTextContent(
      "opaque-source",
    )
    await user.click(screen.getByRole("button", { name: "Edit detail" }))
    await waitFor(() =>
      expect(router.state.location.pathname).toBe(
        "/event-sources/opaque-source/edit",
      ),
    )
    expect(screen.getByTestId("event-source-editor")).toHaveTextContent(
      "edit:opaque-source",
    )
  })

  it("uses separate new and global settings routes", async () => {
    const user = userEvent.setup()
    const router = renderRoute("/event-sources?view=list")
    await user.click(await screen.findByRole("button", { name: "Add source" }))
    await waitFor(() =>
      expect(router.state.location.pathname).toBe("/event-sources/new"),
    )
    expect(screen.getByTestId("event-source-editor")).toHaveTextContent(
      "create:new",
    )
    await user.click(screen.getByRole("button", { name: "Complete save" }))
    await waitFor(() =>
      expect(router.state.location.pathname).toBe(
        "/event-sources/created-source",
      ),
    )

    await router.navigate({
      to: "/event-sources",
      search: { q: "ORDER BY name ASC", view: "list" },
    })
    await user.click(
      await screen.findByRole("button", { name: "Source settings" }),
    )
    await waitFor(() =>
      expect(router.state.location.pathname).toBe("/event-sources/settings"),
    )
    expect(screen.getByTestId("event-source-settings")).toBeVisible()
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
