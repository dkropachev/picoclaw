import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { beforeEach, describe, expect, it, vi } from "vitest"

import type { RepositoryReviewAutomation } from "@/api/repository-reviews"
import {
  getRepositoryReviewAutomationOptions,
  listRepositoryReviewAutomations,
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
  listRepositoryReviewAutomations: vi.fn(),
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
    vi.mocked(listRepositoryReviewAutomations).mockResolvedValue({
      automations: [run],
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
    expect(screen.getByText("25%")).toBeVisible()
    expect(screen.getByText("1,000 tokens used")).toBeVisible()
    expect(screen.getByText("Estimated cost unknown")).toBeVisible()
    expect(screen.getByText("Account: Default (Primary API)")).toBeVisible()
    expect(screen.getByText(`Resolved commit ${"a".repeat(40)}`)).toBeVisible()
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

  it("resumes a guard-paused run without hidden budget-reset semantics", async () => {
    const user = userEvent.setup()
    const paused = {
      ...run,
      status: "paused" as const,
      pause_reason: "token_budget" as const,
      pause_detail: "Task admission guard evaluated to false.",
    }
    vi.mocked(listRepositoryReviewAutomations).mockResolvedValue({
      automations: [paused],
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
    await user.click(screen.getByRole("button", { name: "Resume" }))

    await waitFor(() =>
      expect(resumeRepositoryReviewAutomation).toHaveBeenCalledWith("auto_1", {
        expected_version: 3,
      }),
    )
  })
})

function renderPage() {
  return render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <RepositoryReviewRunsPage />
    </QueryClientProvider>,
  )
}
