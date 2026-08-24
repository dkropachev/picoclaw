import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import type { ReactNode } from "react"
import { beforeEach, describe, expect, it, vi } from "vitest"

import {
  getDevelopmentWorkspace,
  listDevelopmentWorkspaces,
} from "@/api/development-workspaces"
import { DevelopmentPortfolioPage } from "@/components/development-workspaces/development-portfolio-page"
import { DevelopmentWorkspacePage } from "@/components/development-workspaces/development-workspace-page"

const mockedUseIsMobile = vi.hoisted(() => vi.fn(() => false))

vi.mock("@/api/development-workspaces", async (importOriginal) => {
  const original =
    await importOriginal<typeof import("@/api/development-workspaces")>()
  return {
    ...original,
    getDevelopmentWorkspace: vi.fn(),
    listDevelopmentWorkspaces: vi.fn(),
  }
})
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
vi.mock("@/components/development-workspaces/development-chat", () => ({
  DevelopmentChat: ({ workspaceID }: { workspaceID: string }) => (
    <aside>Chat for {workspaceID}</aside>
  ),
}))
vi.mock("@/components/development-workspaces/development-code-browser", () => ({
  DevelopmentCodeBrowser: ({ selectedPath }: { selectedPath?: string }) => (
    <section>Code browser {selectedPath}</section>
  ),
}))
vi.mock("@/hooks/use-mobile", () => ({
  useIsMobile: mockedUseIsMobile,
}))

const workspaceID = `devw_${"1".repeat(32)}`
const workspace = {
  id: workspaceID,
  intent: "implement_feature" as const,
  source_kind: "issue" as const,
  repository: "octo/repo",
  title: "Improve retry feedback",
  phase: "validation" as const,
  execution_state: "running" as const,
  version: 3,
  created_at: "2026-08-24T10:00:00Z",
  updated_at: "2026-08-24T10:05:00Z",
  source: {
    kind: "issue" as const,
    url: "https://github.com/octo/repo/issues/7",
    number: 7,
    title: "Improve retry feedback",
  },
  base_revision: "base:1",
  candidate_revision: "candidate:2",
  changed_files: ["src/retry.ts"],
  activity: [
    {
      ordinal: 1,
      kind: "implementation_completed",
      summary: "Implementation candidate created.",
      created_at: "2026-08-24T10:04:00Z",
    },
  ],
  validation_checks: [
    { id: "test", name: "Tests", status: "passed", summary: "42 passed" },
  ],
  gates: [],
  publications: [],
  summary: "Candidate ready for validation.",
}

function renderWithClient(node: ReactNode) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(<QueryClientProvider client={queryClient}>{node}</QueryClientProvider>)
}

describe("development workspace pages", () => {
  beforeEach(() => {
    mockedUseIsMobile.mockReturnValue(false)
    vi.mocked(listDevelopmentWorkspaces).mockReset()
    vi.mocked(getDevelopmentWorkspace).mockReset()
    vi.mocked(listDevelopmentWorkspaces).mockResolvedValue({
      workspaces: [workspace],
    })
    vi.mocked(getDevelopmentWorkspace).mockResolvedValue(workspace)
  })

  it("shows and opens development work from the portfolio", async () => {
    const user = userEvent.setup()
    const onCreate = vi.fn()
    const onOpenWorkspace = vi.fn()
    renderWithClient(
      <DevelopmentPortfolioPage
        onCreate={onCreate}
        onOpenWorkspace={onOpenWorkspace}
      />,
    )

    await user.click(
      await screen.findByRole("button", { name: /Improve retry feedback/ }),
    )
    expect(onOpenWorkspace).toHaveBeenCalledWith(workspaceID)
    await user.click(screen.getByRole("button", { name: "New work" }))
    expect(onCreate).toHaveBeenCalled()
  })

  it("loads cursor-paginated portfolio work on demand", async () => {
    const user = userEvent.setup()
    vi.mocked(listDevelopmentWorkspaces)
      .mockReset()
      .mockResolvedValueOnce({
        workspaces: [workspace],
        next_cursor: "next+/=",
      })
      .mockResolvedValueOnce({
        workspaces: [
          {
            ...workspace,
            id: `devw_${"2".repeat(32)}`,
            title: "Second page feature",
          },
        ],
      })
    renderWithClient(
      <DevelopmentPortfolioPage onCreate={vi.fn()} onOpenWorkspace={vi.fn()} />,
    )

    await user.click(
      await screen.findByRole("button", { name: "Load more workspaces" }),
    )
    expect(await screen.findByText("Second page feature")).toBeVisible()
    expect(listDevelopmentWorkspaces).toHaveBeenLastCalledWith(
      { limit: 100, cursor: "next+/=" },
      expect.any(AbortSignal),
    )
  })

  it("renders lifecycle evidence and stable workspace tabs", async () => {
    const user = userEvent.setup()
    const onTabChange = vi.fn()
    renderWithClient(
      <DevelopmentWorkspacePage
        workspaceID={workspaceID}
        tab="overview"
        onBack={vi.fn()}
        onTabChange={onTabChange}
        onPathChange={vi.fn()}
      />,
    )

    expect(
      await screen.findByText("Candidate ready for validation."),
    ).toBeVisible()
    expect(screen.getByText("42 passed")).toBeVisible()
    expect(screen.getByText(`Chat for ${workspaceID}`)).toBeVisible()
    await user.click(screen.getByRole("button", { name: "Changes" }))
    expect(onTabChange).toHaveBeenCalledWith("changes")
  })

  it("keeps mobile chat behind an explicit sheet trigger", async () => {
    mockedUseIsMobile.mockReturnValue(true)
    const user = userEvent.setup()
    renderWithClient(
      <DevelopmentWorkspacePage
        workspaceID={workspaceID}
        tab="overview"
        onBack={vi.fn()}
        onTabChange={vi.fn()}
        onPathChange={vi.fn()}
      />,
    )

    const trigger = await screen.findByRole("button", {
      name: "Development chat",
    })
    expect(
      screen.queryByText(`Chat for ${workspaceID}`),
    ).not.toBeInTheDocument()
    await user.click(trigger)
    expect(await screen.findByText(`Chat for ${workspaceID}`)).toBeVisible()
  })
})
