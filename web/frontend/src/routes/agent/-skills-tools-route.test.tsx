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

vi.mock("@/components/agent/skills/skill-collections", () => ({
  SkillsCollectionPage: ({
    search,
    onAdd,
    onOpen,
  }: {
    search: object
    onAdd: () => void
    onOpen: (skill: { id: string }) => void
  }) => (
    <div>
      <output data-testid="skills-search">{JSON.stringify(search)}</output>
      <button type="button" onClick={onAdd}>
        Import skill
      </button>
      <button type="button" onClick={() => onOpen({ id: "review-helper" })}>
        Open review-helper
      </button>
    </div>
  ),
  SkillDetailPage: ({
    skillID,
    onBack,
  }: {
    skillID: string
    onBack: () => void
  }) => (
    <div>
      <output data-testid="skill-detail">{skillID}</output>
      <button type="button" onClick={onBack}>
        Back to skills
      </button>
    </div>
  ),
}))
vi.mock("@/components/agent/skills/skill-import-page", () => ({
  SkillImportPage: ({ onImported }: { onImported: (id: string) => void }) => (
    <div>
      <output data-testid="skill-import">new</output>
      <button type="button" onClick={() => onImported("imported-skill-id")}>
        Complete import
      </button>
    </div>
  ),
}))
vi.mock("@/components/agent/tools/tool-collections", () => ({
  ToolsCollectionPage: ({
    search,
    onOpen,
    onEdit,
    onAdaptation,
  }: {
    search: object
    onOpen: (tool: { id: string }) => void
    onEdit: (tool: { id: string }) => void
    onAdaptation: () => void
  }) => (
    <div>
      <output data-testid="tools-search">{JSON.stringify(search)}</output>
      <button type="button" onClick={() => onOpen({ id: "web-search-id" })}>
        Open tool
      </button>
      <button type="button" onClick={() => onEdit({ id: "web-search-id" })}>
        Configure tool
      </button>
      <button type="button" onClick={onAdaptation}>
        Adaptation settings
      </button>
    </div>
  ),
  ToolDetailPage: ({
    toolID,
    onEdit,
  }: {
    toolID: string
    onEdit: () => void
  }) => (
    <div>
      <output data-testid="tool-detail">{toolID}</output>
      <button type="button" onClick={onEdit}>
        Configure
      </button>
    </div>
  ),
  ToolEditorPage: ({ toolID }: { toolID: string }) => (
    <output data-testid="tool-editor">{toolID}</output>
  ),
  ToolAdaptationSettingsPage: () => (
    <output data-testid="tool-adaptation">settings</output>
  ),
}))

describe("skills and tools collection routes", () => {
  it("hard-cuts legacy tool tab search into canonical q/view state", async () => {
    const router = renderRoute(
      "/agent/tools?tab=web-search&q=status%20%3D%20enabled&view=grid",
    )
    expect(await screen.findByTestId("tools-search")).toHaveTextContent(
      JSON.stringify({ q: "status = enabled", view: "grid" }),
    )
    await waitFor(() =>
      expect(router.state.location.search).toEqual({
        q: "status = enabled",
        view: "grid",
      }),
    )
  })

  it("preserves skills collection state through new and direct detail routes", async () => {
    const user = userEvent.setup()
    const router = renderRoute(
      "/agent/skills?q=source%20%3D%20workspace&view=table",
    )
    await user.click(
      await screen.findByRole("button", { name: "Import skill" }),
    )
    await waitFor(() =>
      expect(router.state.location.pathname).toBe("/agent/skills/new"),
    )
    expect(screen.getByTestId("skill-import")).toBeVisible()
    await user.click(screen.getByRole("button", { name: "Complete import" }))
    await waitFor(() =>
      expect(router.state.location.pathname).toBe(
        "/agent/skills/imported-skill-id",
      ),
    )
    expect(router.state.location.search).toEqual({
      q: "source = workspace",
      view: "table",
    })
  })

  it("uses distinct tool detail, editor, and adaptation settings routes", async () => {
    const user = userEvent.setup()
    const router = renderRoute(
      "/agent/tools?q=category%20%3D%20search&view=list",
    )
    await user.click(await screen.findByRole("button", { name: "Open tool" }))
    await waitFor(() =>
      expect(router.state.location.pathname).toBe("/agent/tools/web-search-id"),
    )
    expect(screen.getByTestId("tool-detail")).toHaveTextContent("web-search-id")
    await user.click(screen.getByRole("button", { name: "Configure" }))
    await waitFor(() =>
      expect(router.state.location.pathname).toBe(
        "/agent/tools/web-search-id/edit",
      ),
    )
    expect(screen.getByTestId("tool-editor")).toHaveTextContent("web-search-id")

    await router.navigate({
      to: "/agent/tools",
      search: { q: "category = search", view: "list" },
    })
    await user.click(
      await screen.findByRole("button", { name: "Adaptation settings" }),
    )
    await waitFor(() =>
      expect(router.state.location.pathname).toBe(
        "/agent/tools/settings/adaptation",
      ),
    )
    expect(screen.getByTestId("tool-adaptation")).toBeVisible()
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
