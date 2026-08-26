import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, waitFor, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { beforeEach, describe, expect, it, vi } from "vitest"

import type { RepositoryReviewAutomation } from "@/api/repository-reviews"
import {
  getRepositoryReviewAutomationOptions,
  getRepositoryReviewCommitOptions,
  listRepositoryReviewAutomationsPage,
  pauseRepositoryReviewAutomation,
  restartRepositoryReviewAutomation,
  resumeRepositoryReviewAutomation,
  startRepositoryReviewAutomation,
} from "@/api/repository-reviews"
import { RepositoryReviewRunsPage } from "@/components/repository-reviews/repository-review-runs-page"

vi.mock("@/components/page-header", () => ({
  PageHeader: ({
    title,
    children,
  }: {
    title: string
    children?: React.ReactNode
  }) => (
    <header>
      <h1>{title}</h1>
      {children}
    </header>
  ),
}))

vi.mock("@/api/repository-reviews", () => ({
  getRepositoryReviewAutomationOptions: vi.fn(),
  getRepositoryReviewCommitOptions: vi.fn(),
  listRepositoryReviewAutomationsPage: vi.fn(),
  pauseRepositoryReviewAutomation: vi.fn(),
  restartRepositoryReviewAutomation: vi.fn(),
  resumeRepositoryReviewAutomation: vi.fn(),
  startRepositoryReviewAutomation: vi.fn(),
}))

const run: RepositoryReviewAutomation = {
  id: "auto_1",
  version: 3,
  profile_id: "profile_1",
  profile_version: 2,
  branch: "",
  name: "Core bugs",
  repository: "owner/repo",
  ref: "",
  target: "all",
  account_ref: "",
  review_focus: "Correctness bugs",
  scope_policy: {
    code_types: ["code"],
    include_folders: [],
    exclude_folders: [],
    free_text: "",
  },
  reviewer_models: ["review-model"],
  compare_models: false,
  force: false,
  max_files_per_run: 24,
  max_content_bytes: 524288,
  max_parallel_children: 8,
  auto_continue: true,
  model_prices: {},
  budget: {
    guard_expression: "spend.total.usd < 25",
  },
  status: "idle",
  run_ids: ["workflow_run_1"],
  usage: {
    prompt_tokens: 800,
    completion_tokens: 200,
    total_tokens: 1000,
    cached_tokens: 0,
  },
  estimated_cost_usd: 0.2,
  progress: {
    stage: "waiting",
    completed_batches: 1,
    total_batches: 4,
    reviewed_files: 12,
    remaining_files: 36,
    unsupported_files: 1,
    findings: 3,
  },
  model_stats: [],
  account_limits: [],
  resolved_commit_sha: "a".repeat(40),
  scope_plan: {
    commit_sha: "a".repeat(40),
    policy_hash: "policy",
    hash: "scope",
    summary: "Production files selected",
    warnings: [],
    counts: {
      total_files: 48,
      code_type_files: 48,
      include_files: 48,
      excluded_files: 0,
      selected_files: 48,
    },
  },
  created_at: "2026-08-23T00:00:00Z",
  updated_at: "2026-08-23T00:00:00Z",
}

describe("RepositoryReviewRunsPage", () => {
  beforeEach(() => {
    vi.mocked(getRepositoryReviewAutomationOptions).mockResolvedValue({
      models: [],
      accounts: [
        {
          id: "api",
          label: "Primary API",
          status: "available",
          default: true,
          entries: [],
        },
      ],
    })
    vi.mocked(listRepositoryReviewAutomationsPage).mockResolvedValue({
      automations: [run],
      total: 1,
      next_cursor: "",
      canonical_query: "ORDER BY repository ASC",
      query_schema: { fields: [] },
    })
    vi.mocked(getRepositoryReviewCommitOptions).mockReset()
    vi.mocked(getRepositoryReviewCommitOptions).mockResolvedValue({
      expected_version: 3,
      remembered: {
        sha: "a".repeat(40),
        short_sha: "aaaaaaaa",
      },
      latest: {
        sha: "a".repeat(40),
        short_sha: "aaaaaaaa",
      },
      newer_commit_available: false,
    })
    vi.mocked(startRepositoryReviewAutomation).mockResolvedValue({
      ...run,
      version: 4,
      status: "running",
    })
    vi.mocked(pauseRepositoryReviewAutomation).mockReset()
    vi.mocked(resumeRepositoryReviewAutomation).mockReset()
    vi.mocked(restartRepositoryReviewAutomation).mockReset()
  })

  it("keeps actual run lifecycle separate from configuration and model probes", async () => {
    const user = userEvent.setup()
    renderPage()

    expect(await screen.findByText("owner/repo")).toBeVisible()
    expect(
      screen.getByText(/Core bugs.*Default repository branch.*review-model/),
    ).toBeVisible()
    expect(screen.getByText(/25%/)).toBeVisible()
    expect(screen.getByText("1,000 tokens used")).toBeVisible()
    expect(screen.getByText("Estimated cost unknown")).toBeVisible()
    expect(screen.getByText("Account: Default (Primary API)")).toBeVisible()
    const commitLink = screen.getByRole("link", { name: "aaaaaaaa" })
    expect(commitLink).toHaveAttribute(
      "href",
      `https://github.com/owner/repo/commit/${"a".repeat(40)}`,
    )
    expect(commitLink).toHaveAttribute("title", "a".repeat(40))
    expect(screen.queryByLabelText("Repository")).not.toBeInTheDocument()
    expect(screen.queryByLabelText("Reviewer model")).not.toBeInTheDocument()
    expect(screen.queryByText("Model comparison")).not.toBeInTheDocument()
    await user.click(screen.getByText("Run history (1)"))
    expect(screen.getByText("workflow_run_1")).toBeVisible()

    await user.click(screen.getByRole("button", { name: "Start review" }))
    await waitFor(() =>
      expect(startRepositoryReviewAutomation).toHaveBeenCalledWith("auto_1", {
        expected_version: 3,
      }),
    )
  })

  it("uses the canonical collection query and shared view controls", async () => {
    const user = userEvent.setup()
    const onSearchChange = vi.fn()
    renderPage(onSearchChange)

    expect(await screen.findByText("owner/repo")).toBeVisible()
    expect(listRepositoryReviewAutomationsPage).toHaveBeenCalledWith(
      {
        query: "ORDER BY repository ASC",
        cursor: undefined,
        limit: 50,
      },
      expect.anything(),
    )

    await user.click(screen.getByRole("button", { name: "Grid view" }))
    expect(onSearchChange).toHaveBeenCalledWith(
      { q: "ORDER BY repository ASC", view: "grid" },
      true,
    )
  })

  it("shows the commit-bound scope SHA for legacy runs without a resolved commit field", async () => {
    vi.mocked(listRepositoryReviewAutomationsPage).mockResolvedValue({
      automations: [{ ...run, resolved_commit_sha: undefined }],
      total: 1,
      next_cursor: "",
      canonical_query: "ORDER BY repository ASC",
      query_schema: { fields: [] },
    })
    renderPage()

    const commitLink = await screen.findByRole("link", { name: "aaaaaaaa" })
    expect(commitLink).toHaveAttribute("title", "a".repeat(40))
  })

  it("resumes a guard-paused run without hidden budget-reset semantics", async () => {
    const user = userEvent.setup()
    const paused = {
      ...run,
      status: "paused" as const,
      pause_reason: "token_budget" as const,
      pause_detail: "Task admission guard evaluated to false.",
    }
    vi.mocked(listRepositoryReviewAutomationsPage).mockResolvedValue({
      automations: [paused],
      total: 1,
      next_cursor: "",
      canonical_query: "ORDER BY repository ASC",
      query_schema: { fields: [] },
    })
    vi.mocked(resumeRepositoryReviewAutomation).mockResolvedValue({
      ...paused,
      version: 4,
      status: "running",
    })
    renderPage()

    expect(
      await screen.findByText(/Task admission guard evaluated/),
    ).toBeVisible()
    expect(
      screen.queryByRole("button", { name: /reset budget/i }),
    ).not.toBeInTheDocument()
    await user.click(screen.getByRole("button", { name: "Continue review" }))

    await waitFor(() =>
      expect(getRepositoryReviewCommitOptions).toHaveBeenCalledWith("auto_1"),
    )
    await waitFor(() =>
      expect(resumeRepositoryReviewAutomation).toHaveBeenCalledWith("auto_1", {
        expected_version: 3,
      }),
    )
  })

  it("offers remembered, latest, and custom commits when the branch moved", async () => {
    const user = userEvent.setup()
    const rememberedSHA = "a".repeat(40)
    const latestSHA = "b".repeat(40)
    const paused = {
      ...run,
      repository: "https://github.com/owner/repo.git",
      status: "paused" as const,
    }
    vi.mocked(listRepositoryReviewAutomationsPage).mockResolvedValue({
      automations: [paused],
      total: 1,
      next_cursor: "",
      canonical_query: "ORDER BY repository ASC",
      query_schema: { fields: [] },
    })
    vi.mocked(getRepositoryReviewCommitOptions).mockResolvedValue({
      expected_version: 9,
      remembered: { sha: rememberedSHA, short_sha: "aaaaaaaa" },
      latest: { sha: latestSHA, short_sha: "bbbbbbbb" },
      newer_commit_available: true,
    })
    vi.mocked(resumeRepositoryReviewAutomation).mockResolvedValue({
      ...paused,
      version: 10,
      status: "running",
      resolved_commit_sha: latestSHA,
    })
    renderPage()

    await user.click(
      await screen.findByRole("button", { name: "Continue review" }),
    )
    let dialog = await screen.findByRole("dialog", {
      name: "Choose commit to continue",
    })
    expect(
      within(dialog).getByRole("radio", {
        name: "Continue on remembered commit",
      }),
    ).toBeChecked()
    expect(
      within(dialog).getByRole("link", { name: "aaaaaaaa" }),
    ).toHaveAttribute(
      "href",
      `https://github.com/owner/repo/commit/${rememberedSHA}`,
    )
    expect(
      within(dialog).getByRole("link", { name: "bbbbbbbb" }),
    ).toHaveAttribute(
      "href",
      `https://github.com/owner/repo/commit/${latestSHA}`,
    )
    expect(
      within(dialog).getByRole("radio", { name: "Choose another commit" }),
    ).toBeVisible()

    await user.click(within(dialog).getByRole("button", { name: "Cancel" }))
    expect(resumeRepositoryReviewAutomation).not.toHaveBeenCalled()

    await user.click(screen.getByRole("button", { name: "Continue review" }))
    dialog = await screen.findByRole("dialog", {
      name: "Choose commit to continue",
    })
    await user.click(
      within(dialog).getByRole("radio", {
        name: "Continue on latest commit",
      }),
    )
    await user.click(
      within(dialog).getByRole("button", { name: "Continue review" }),
    )

    await waitFor(() =>
      expect(resumeRepositoryReviewAutomation).toHaveBeenCalledWith("auto_1", {
        expected_version: 9,
        commit_sha: latestSHA,
      }),
    )
  })

  it("accepts a full custom SHA and sends it exactly", async () => {
    const user = userEvent.setup()
    const customSHA = "c".repeat(64)
    const paused = { ...run, status: "paused" as const }
    vi.mocked(listRepositoryReviewAutomationsPage).mockResolvedValue({
      automations: [paused],
      total: 1,
      next_cursor: "",
      canonical_query: "ORDER BY repository ASC",
      query_schema: { fields: [] },
    })
    vi.mocked(getRepositoryReviewCommitOptions).mockResolvedValue({
      expected_version: 8,
      remembered: { sha: "a".repeat(40), short_sha: "aaaaaaaa" },
      latest: { sha: "b".repeat(40), short_sha: "bbbbbbbb" },
      newer_commit_available: true,
    })
    vi.mocked(resumeRepositoryReviewAutomation).mockResolvedValue({
      ...paused,
      version: 9,
      status: "running",
      resolved_commit_sha: customSHA,
    })
    renderPage()

    await user.click(
      await screen.findByRole("button", { name: "Continue review" }),
    )
    const dialog = await screen.findByRole("dialog")
    await user.type(
      within(dialog).getByLabelText("Custom commit SHA"),
      customSHA,
    )
    expect(
      within(dialog).getByRole("link", { name: "cccccccc" }),
    ).toHaveAttribute(
      "href",
      `https://github.com/owner/repo/commit/${customSHA}`,
    )
    await user.click(
      within(dialog).getByRole("button", { name: "Continue review" }),
    )

    await waitFor(() =>
      expect(resumeRepositoryReviewAutomation).toHaveBeenCalledWith("auto_1", {
        expected_version: 8,
        commit_sha: customSHA,
      }),
    )
  })

  it.each([
    ["running", "reviewing"],
    ["idle", "next batch queued"],
  ] as const)("stops safely from %s state", async (status, stage) => {
    const user = userEvent.setup()
    const active = {
      ...run,
      status,
      progress: { ...run.progress, stage },
    }
    vi.mocked(listRepositoryReviewAutomationsPage).mockResolvedValue({
      automations: [active],
      total: 1,
      next_cursor: "",
      canonical_query: "ORDER BY repository ASC",
      query_schema: { fields: [] },
    })
    vi.mocked(pauseRepositoryReviewAutomation).mockResolvedValue({
      ...active,
      version: 4,
      status: "stopping",
    })
    renderPage()

    await user.click(await screen.findByRole("button", { name: "Stop safely" }))
    await waitFor(() =>
      expect(pauseRepositoryReviewAutomation).toHaveBeenCalledWith("auto_1", {
        expected_version: 3,
        run_id: "workflow_run_1",
      }),
    )
  })
})

function renderPage(onSearchChange = vi.fn()) {
  return render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <RepositoryReviewRunsPage
        search={{ q: "ORDER BY repository ASC" }}
        onSearchChange={onSearchChange}
      />
    </QueryClientProvider>,
  )
}
