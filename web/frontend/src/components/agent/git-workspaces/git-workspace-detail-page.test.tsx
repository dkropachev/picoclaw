import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, waitFor, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import type { ReactNode } from "react"
import { beforeEach, describe, expect, it, vi } from "vitest"

import { copyText } from "@/lib/clipboard"

import { GitWorkspaceDetailPage } from "./git-workspace-detail-page"

const mocks = vi.hoisted(() => ({
  getGitWorkspace: vi.fn(),
  cleanupGitWorkspace: vi.fn(),
  dropGitWorkspace: vi.fn(),
}))
vi.mock("@/api/git-workspaces", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/git-workspaces")>()),
  ...mocks,
}))
vi.mock("@/lib/clipboard", () => ({ copyText: vi.fn() }))
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

const workspaceID = "gw-0123456789ab"
const workspace = {
  id: workspaceID,
  repository: "team/repo",
  repository_id: "gw-fedcba987654",
  remote_url: "https://github.com/team/repo.git",
  path: "/tmp/git-workspaces/checkouts/repo-gw-0123456789ab",
  branch: "main",
  status: "available",
  locked: false,
  dirty: false,
  size: 4096,
  ignored: 512,
  created: "2026-07-31T00:00:00Z",
  updated: "2026-08-01T00:00:00Z",
}

function renderPage(onDropped = vi.fn()) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <GitWorkspaceDetailPage
        workspaceID={workspaceID}
        onBack={vi.fn()}
        onDropped={onDropped}
      />
    </QueryClientProvider>,
  )
  return onDropped
}

describe("git workspace detail", () => {
  beforeEach(() => {
    for (const mock of Object.values(mocks)) mock.mockReset()
    vi.mocked(copyText).mockReset()
    vi.mocked(copyText).mockResolvedValue(true)
    mocks.getGitWorkspace.mockResolvedValue({
      workspace,
      root_dir: "/tmp/git-workspaces",
    })
    mocks.cleanupGitWorkspace.mockResolvedValue({
      workspace: { ...workspace, ignored: 0 },
      before_ignored_bytes: 512,
      after_ignored_bytes: 0,
    })
    mocks.dropGitWorkspace.mockResolvedValue({ workspace })
  })

  it("loads one exact item and copies safe path/remote values", async () => {
    const user = userEvent.setup()
    renderPage()
    expect(
      await screen.findByText("checkouts/repo-gw-0123456789ab"),
    ).toBeVisible()
    expect(mocks.getGitWorkspace).toHaveBeenCalledWith(
      workspaceID,
      expect.any(AbortSignal),
    )
    await user.click(screen.getByRole("button", { name: "Copy checkout path" }))
    expect(copyText).toHaveBeenCalledWith(workspace.path)
    await user.click(
      screen.getByRole("button", { name: "Copy repository remote" }),
    )
    expect(copyText).toHaveBeenCalledWith("git@github.com:team/repo.git")
    expect(document.body.textContent).not.toContain("session_key")
  })

  it("confirms cleanup and drop independently", async () => {
    const user = userEvent.setup()
    const onDropped = renderPage()
    await user.click(
      await screen.findByRole("button", { name: "Clean ignored files" }),
    )
    let dialog = screen.getByRole("alertdialog")
    await user.click(within(dialog).getByRole("button", { name: "Clean" }))
    await waitFor(() => expect(mocks.cleanupGitWorkspace).toHaveBeenCalled())

    await user.click(screen.getByRole("button", { name: "Drop workspace" }))
    dialog = screen.getByRole("alertdialog")
    await user.click(within(dialog).getByRole("button", { name: "Drop" }))
    await waitFor(() => expect(mocks.dropGitWorkspace).toHaveBeenCalled())
    expect(onDropped).toHaveBeenCalled()
  })

  it("disables maintenance for a locked workspace", async () => {
    mocks.getGitWorkspace.mockResolvedValue({
      workspace: {
        ...workspace,
        locked: true,
        status: "locked",
        locked_by: {
          agent_id: "main",
          locked_at: "2026-08-01T00:00:00Z",
          heartbeat_at: "2026-08-01T00:01:00Z",
        },
      },
    })
    renderPage()
    expect(
      await screen.findByRole("button", { name: "Clean ignored files" }),
    ).toBeDisabled()
    expect(
      screen.getByRole("button", { name: "Drop workspace" }),
    ).toBeDisabled()
    expect(screen.getAllByText("main").length).toBeGreaterThan(0)
  })

  it("renders direct not-found without list state", async () => {
    mocks.getGitWorkspace.mockRejectedValue(
      Object.assign(new Error("missing"), { status: 404 }),
    )
    renderPage()
    expect(await screen.findByText("Item not found")).toBeVisible()
  })
})
