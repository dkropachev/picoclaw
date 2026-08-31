import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { beforeEach, describe, expect, it, vi } from "vitest"

import type { RepositoryReviewAutomation } from "@/api/repository-reviews"
import { listRepositoryReviewAutomationsPage } from "@/api/repository-reviews"
import { RepositoryReviewRunsPage } from "@/components/repository-reviews/repository-review-runs-page"
import type { CollectionRouteSearch } from "@/hooks/use-collection-route-state"

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
  listRepositoryReviewAutomationsPage: vi.fn(),
}))

const review: RepositoryReviewAutomation = {
  id: "auto_1",
  version: 3,
  profile_id: "profile_1",
  profile_version: 2,
  branch: "main",
  name: "Core bugs",
  repository: "owner/repo",
  ref: "main",
  target: "all",
  account_ref: "",
  effective_account_ref: "acct",
  review_focus: "Correctness bugs",
  scope_policy: {
    code_types: ["code"],
    include_folders: [],
    exclude_folders: [],
    free_text: "",
  },
  reviewer_models: ["review-model"],
  issue_writer_model: "writer-model",
  compare_models: false,
  force: false,
  max_files_per_run: 24,
  max_content_bytes: 524288,
  max_parallel_children: 8,
  assignment_timeout_seconds: 3_600,
  auto_continue: true,
  model_prices: {},
  budget: { guard_expression: "" },
  status: "running",
  run_ids: ["workflow_run_1"],
  usage: {
    prompt_tokens: 800,
    completion_tokens: 200,
    total_tokens: 1000,
    cached_tokens: 0,
  },
  estimated_cost_usd: 0.2,
  progress: {
    stage: "reviewing",
    completed_batches: 1,
    total_batches: 4,
    coverage_available: false,
    coverage_exact: false,
    selected_files: 49,
    inspected_files: 0,
    reviewed_files: 12,
    remaining_files: 36,
    unsupported_files: 1,
    findings: 3,
    raw_findings: 7,
    deduplicated_findings: 3,
    finding_aggregates: 3,
    unaggregated_findings: 0,
    assignment_progress: {
      total: 0,
      completed: 0,
      pending: 0,
      active: 0,
      by_focus: {
        correctness_state: { total: 0, completed: 0, pending: 0, active: 0 },
        security_trust: { total: 0, completed: 0, pending: 0, active: 0 },
        concurrency_recovery: {
          total: 0,
          completed: 0,
          pending: 0,
          active: 0,
        },
        integration_validation: {
          total: 0,
          completed: 0,
          pending: 0,
          active: 0,
        },
      },
    },
  },
  model_stats: [],
  account_limits: [],
  created_at: "2026-08-23T00:00:00Z",
  updated_at: "2026-08-23T00:00:00Z",
}

describe("RepositoryReviewRunsPage", () => {
  beforeEach(() => {
    vi.mocked(listRepositoryReviewAutomationsPage).mockResolvedValue({
      automations: [review],
      total: 1,
      next_cursor: "",
      canonical_query: "ORDER BY repository ASC",
      query_schema: { fields: [] },
    })
  })

  it("renders compact summaries and opens the dedicated detail", async () => {
    const user = userEvent.setup()
    const onOpen = vi.fn()
    const onOpenRawFindings = vi.fn()
    renderPage({ onOpen, onOpenRawFindings })

    const item = await screen.findByText("owner/repo")
    expect(screen.getByText("Branch main")).toBeVisible()
    expect(screen.getByText("running")).toBeVisible()
    expect(screen.queryByText("1,000 tokens used")).not.toBeInTheDocument()
    expect(
      screen.queryByRole("button", { name: "Stop safely" }),
    ).not.toBeInTheDocument()
    expect(screen.queryByText("Run history (1)")).not.toBeInTheDocument()

    await user.click(screen.getByRole("button", { name: "Raw findings: 7" }))
    expect(onOpenRawFindings).toHaveBeenCalledWith(review)
    expect(onOpen).not.toHaveBeenCalled()

    await user.dblClick(item.closest("[data-item-id]")!)
    expect(onOpen).toHaveBeenCalledWith(review)
  })

  it("uses canonical server query and shared view controls", async () => {
    const user = userEvent.setup()
    const onSearchChange = vi.fn()
    renderPage({ onSearchChange })

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
    expect(await screen.findByText("Findings")).toBeVisible()
    expect(screen.getByText("Raw findings")).toBeVisible()
    expect(screen.getByText("13 of 49 files (27%)")).toBeVisible()
  })

  it("renders a first-class empty collection", async () => {
    vi.mocked(listRepositoryReviewAutomationsPage).mockResolvedValue({
      automations: [],
      total: 0,
      next_cursor: "",
      canonical_query: "ORDER BY repository ASC",
      query_schema: { fields: [] },
    })
    renderPage()
    expect(await screen.findByText("No repository configured")).toBeVisible()
  })
})

function renderPage({
  onSearchChange = vi.fn(),
  onOpen = vi.fn(),
  onOpenRawFindings = vi.fn(),
}: {
  onSearchChange?: (search: CollectionRouteSearch, replace?: boolean) => void
  onOpen?: (review: RepositoryReviewAutomation) => void
  onOpenRawFindings?: (review: RepositoryReviewAutomation) => void
} = {}) {
  return render(
    <QueryClientProvider
      client={
        new QueryClient({
          defaultOptions: { queries: { retry: false } },
        })
      }
    >
      <RepositoryReviewRunsPage
        search={{ q: "ORDER BY repository ASC" }}
        onSearchChange={onSearchChange}
        onOpen={onOpen}
        onOpenRawFindings={onOpenRawFindings}
      />
    </QueryClientProvider>,
  )
}
