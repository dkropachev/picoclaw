import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, waitFor, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import type { ReactNode } from "react"
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest"

import type { GitWorkspaceInfo, GitWorkspaceStats } from "@/api/git-workspaces"
import {
  cleanupGitWorkspace,
  dropGitWorkspace,
  getGitWorkspaces,
  reconcileGitWorkspaces,
} from "@/api/git-workspaces"
import { GitWorkspacesPage } from "@/components/agent/git-workspaces/git-workspaces-page"
import { SidebarProvider } from "@/components/ui/sidebar"

vi.mock("@/api/git-workspaces", () => ({
  getGitWorkspaces: vi.fn(),
  reconcileGitWorkspaces: vi.fn(),
  cleanupGitWorkspace: vi.fn(),
  dropGitWorkspace: vi.fn(),
}))

vi.mock("sonner", () => ({
  toast: {
    success: vi.fn(),
    error: vi.fn(),
  },
}))

const workspace: GitWorkspaceInfo = {
  id: "gw-workspace",
  repo_id: "gw-repo",
  remote_url: "https://example.test/repo.git",
  path: "/tmp/git-workspaces/checkouts/repo-gw-workspace",
  current_branch: "main",
  dirty: false,
  size_bytes: 4096,
  ignored_bytes: 1024,
  created_at: "2026-07-16T12:00:00Z",
  updated_at: "2026-07-16T12:00:00Z",
  status: "available",
}

const stats: GitWorkspaceStats = {
  root_dir: "/tmp/git-workspaces",
  max_total_size_bytes: 21474836480,
  ignored_cleanup_delay_seconds: 86400,
  drop_delay_seconds: 2592000,
  total_size_bytes: 4096,
  ignored_bytes: 1024,
  repository_count: 1,
  workspace_count: 1,
  locked_workspace_count: 0,
  repositories: [
    {
      id: "gw-repo",
      remote_url: "https://example.test/repo.git",
      first_seen_at: "2026-07-16T12:00:00Z",
      last_seen_at: "2026-07-16T12:00:00Z",
      workspace_count: 1,
      locked_count: 0,
      size_bytes: 4096,
      ignored_bytes: 1024,
    },
  ],
  workspaces: [workspace],
  history: [
    {
      id: "hist-1",
      time: "2026-07-16T12:00:00Z",
      action: "allocated",
      repo_id: "gw-repo",
      workspace_id: "gw-workspace",
    },
  ],
}

describe("GitWorkspacesPage", () => {
  beforeAll(() => {
    Object.defineProperty(window, "scrollTo", {
      writable: true,
      value: vi.fn(),
    })
    Object.defineProperty(window, "matchMedia", {
      writable: true,
      value: vi.fn().mockImplementation((query: string) => ({
        matches: false,
        media: query,
        onchange: null,
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
        addListener: vi.fn(),
        removeListener: vi.fn(),
        dispatchEvent: vi.fn(),
      })),
    })
  })

  beforeEach(() => {
    vi.mocked(getGitWorkspaces).mockReset()
    vi.mocked(reconcileGitWorkspaces).mockReset()
    vi.mocked(cleanupGitWorkspace).mockReset()
    vi.mocked(dropGitWorkspace).mockReset()
    vi.mocked(getGitWorkspaces).mockResolvedValue(stats)
    vi.mocked(reconcileGitWorkspaces).mockResolvedValue({
      cleaned: [],
      dropped: [],
      stats,
    })
    vi.mocked(cleanupGitWorkspace).mockResolvedValue({
      before_ignored_bytes: 1024,
      after_ignored_bytes: 0,
      workspace: { ...workspace, ignored_bytes: 0 },
    })
    vi.mocked(dropGitWorkspace).mockResolvedValue({ workspace })
  })

  it("renders inventory stats and history", async () => {
    renderWithClient(<GitWorkspacesPage />)

    expect(
      await screen.findByText("https://example.test/repo.git"),
    ).toBeInTheDocument()
    expect(screen.getByText("/tmp/git-workspaces")).toBeInTheDocument()
    expect(screen.getByText("allocated")).toBeInTheDocument()
    expect(
      screen.getByRole("button", { name: /maintain/i }),
    ).toBeInTheDocument()
  })

  it("runs cleanup and drop actions", async () => {
    const user = userEvent.setup()
    renderWithClient(<GitWorkspacesPage />)

    await user.click(await screen.findByRole("button", { name: /clean/i }))
    await waitFor(() => {
      expect(cleanupGitWorkspace).toHaveBeenCalled()
    })
    expect(vi.mocked(cleanupGitWorkspace).mock.calls[0]?.[0]).toBe(
      "gw-workspace",
    )

    await user.click(screen.getByRole("button", { name: /drop/i }))
    const dialog = await screen.findByRole("alertdialog")
    await user.click(within(dialog).getByRole("button", { name: /^drop$/i }))

    await waitFor(() => {
      expect(dropGitWorkspace).toHaveBeenCalled()
    })
    expect(vi.mocked(dropGitWorkspace).mock.calls[0]?.[0]).toBe("gw-workspace")
  })
})

function renderWithClient(children: ReactNode) {
  const client = new QueryClient({
    defaultOptions: {
      queries: { retry: false },
      mutations: { retry: false },
    },
  })
  return render(
    <QueryClientProvider client={client}>
      <SidebarProvider>{children}</SidebarProvider>
    </QueryClientProvider>,
  )
}
