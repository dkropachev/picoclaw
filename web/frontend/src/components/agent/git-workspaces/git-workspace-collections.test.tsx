import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, waitFor, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import type { ReactNode } from "react"
import { beforeEach, describe, expect, it, vi } from "vitest"

import {
  GitWorkspaceHistoryCollectionPage,
  GitWorkspacesCollectionPage,
} from "./git-workspace-collections"

const mocks = vi.hoisted(() => ({
  listGitWorkspaces: vi.fn(),
  listGitWorkspaceHistory: vi.fn(),
  reconcileGitWorkspaces: vi.fn(),
  cleanupGitWorkspace: vi.fn(),
  dropGitWorkspace: vi.fn(),
}))
vi.mock("@/api/git-workspaces", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/git-workspaces")>()),
  ...mocks,
}))
vi.mock("@/components/page-header", () => ({
  PageHeader: ({
    title,
    children,
  }: {
    title: string
    children?: ReactNode
  }) => (
    <header>
      <h1>{title}</h1>
      {children}
    </header>
  ),
}))

const schema = { fields: [] }
const workspace = {
  id: "gw-0123456789ab",
  repository: "git@example.test:team/repo.git",
  branch: "main",
  status: "available",
  locked: false,
  dirty: false,
  size: 4096,
  ignored: 512,
  updated: "2026-08-01T00:00:00Z",
}

function renderWithClient(node: ReactNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  render(<QueryClientProvider client={client}>{node}</QueryClientProvider>)
}

describe("git workspace collections", () => {
  beforeEach(() => {
    for (const mock of Object.values(mocks)) mock.mockReset()
    mocks.listGitWorkspaces.mockResolvedValue({
      workspaces: [workspace],
      total: 1,
      canonical_query: "ORDER BY updated DESC",
      query_schema: schema,
      max_total_size_bytes: 1024,
      total_size_bytes: 512,
      ignored_bytes: 64,
      repository_count: 1,
      workspace_count: 1,
      locked_workspace_count: 0,
      ignored_cleanup_delay_seconds: 3600,
      drop_delay_seconds: 86400,
    })
    mocks.listGitWorkspaceHistory.mockResolvedValue({
      history: [
        {
          id: "abcdef012345",
          action: "allocated",
          workspace: workspace.id,
          repository: workspace.repository,
          agent: "main",
          time: "2026-08-01T00:00:00Z",
        },
      ],
      total: 1,
      canonical_query: "ORDER BY time DESC",
      query_schema: schema,
    })
    mocks.cleanupGitWorkspace.mockResolvedValue({
      workspace: {
        ...workspace,
        repository_id: "gw-fedcba987654",
        created: "2026-07-31T00:00:00Z",
      },
      before_ignored_bytes: 512,
      after_ignored_bytes: 0,
    })
    mocks.reconcileGitWorkspaces.mockResolvedValue({
      cleaned: [],
      dropped: [],
      stats: {},
    })
  })

  it("renders all administrative views without selection", async () => {
    renderWithClient(
      <GitWorkspacesCollectionPage
        search={{ q: "ORDER BY updated DESC" }}
        onSearchChange={vi.fn()}
        onOpen={vi.fn()}
        onHistory={vi.fn()}
        onSettings={vi.fn()}
      />,
    )
    expect(await screen.findByText(workspace.repository)).toBeVisible()
    expect(screen.getByRole("button", { name: "List view" })).toBeVisible()
    expect(screen.getByRole("button", { name: "Table view" })).toBeVisible()
    expect(screen.getByRole("button", { name: "Grid view" })).toBeVisible()
    expect(screen.queryByText(/selected$/)).toBeNull()
  })

  it("confirms exact cleanup from the item context menu", async () => {
    const user = userEvent.setup()
    renderWithClient(
      <GitWorkspacesCollectionPage
        search={{ q: "ORDER BY updated DESC" }}
        onSearchChange={vi.fn()}
        onOpen={vi.fn()}
        onHistory={vi.fn()}
        onSettings={vi.fn()}
      />,
    )
    const item = (await screen.findByText(workspace.repository)).closest("li")!
    await user.pointer({ keys: "[MouseRight]", target: item })
    await user.click(
      await screen.findByRole("menuitem", { name: "Clean ignored files" }),
    )
    const dialog = screen.getByRole("alertdialog")
    await user.click(within(dialog).getByRole("button", { name: "Clean" }))
    await waitFor(() =>
      expect(mocks.cleanupGitWorkspace).toHaveBeenCalledWith(workspace.id),
    )
  })

  it("runs explicit global maintenance", async () => {
    const user = userEvent.setup()
    renderWithClient(
      <GitWorkspacesCollectionPage
        search={{ q: "ORDER BY updated DESC" }}
        onSearchChange={vi.fn()}
        onOpen={vi.fn()}
        onHistory={vi.fn()}
        onSettings={vi.fn()}
      />,
    )
    await user.click(
      await screen.findByRole("button", { name: "Maintain git workspaces" }),
    )
    await waitFor(() => expect(mocks.reconcileGitWorkspaces).toHaveBeenCalled())
  })

  it("renders operational history with List and Table only", async () => {
    renderWithClient(
      <GitWorkspaceHistoryCollectionPage
        search={{ q: "ORDER BY time DESC" }}
        onSearchChange={vi.fn()}
        onWorkspaces={vi.fn()}
      />,
    )
    expect((await screen.findAllByText("Allocated")).length).toBeGreaterThan(0)
    expect(screen.getByRole("button", { name: "Table view" })).toBeVisible()
    expect(screen.queryByRole("button", { name: "Grid view" })).toBeNull()
  })
})
