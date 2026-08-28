import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { fireEvent, render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { beforeEach, describe, expect, it, vi } from "vitest"

import {
  type RepositoryReviewAutomation,
  createRepositoryReviewAutomation,
  deleteRepositoryReviewAutomation,
  getRepositoryReviewAutomation,
  listRepositoryReviewAutomations,
  listRepositoryReviewAutomationsPage,
  listRepositoryReviewProfiles,
  updateRepositoryReviewAutomation,
} from "@/api/repository-reviews"
import {
  RepositoryReviewRepositoriesPage,
  RepositoryReviewRepositoryEditorPage,
} from "@/components/repository-reviews/repository-review-repositories-page"

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
  RepositoryReviewAPIError: class RepositoryReviewAPIError extends Error {
    status: number

    constructor(status: number, message: string) {
      super(message)
      this.status = status
    }
  },
  createRepositoryReviewAutomation: vi.fn(),
  deleteRepositoryReviewAutomation: vi.fn(),
  getRepositoryReviewAutomation: vi.fn(),
  listRepositoryReviewAutomations: vi.fn(),
  listRepositoryReviewAutomationsPage: vi.fn(),
  listRepositoryReviewProfiles: vi.fn(),
  updateRepositoryReviewAutomation: vi.fn(),
}))

const profile = {
  id: "profile_1",
  version: 2,
  name: "Core bugs",
  account_ref: "",
  reviewer_model: "review-model",
  review_focus: "Correctness bugs",
  issue_prompt: "Present the confirmed diagnosis with evidence.",
  scope_policy: {
    code_types: ["code" as const],
    include_folders: [],
    exclude_folders: [],
    free_text: "",
  },
  force: false,
  auto_continue: true,
  max_files_per_run: 24,
  max_content_bytes: 524288,
  max_parallel_children: 8,
  budget: {
    guard_expression: "spend.total.usd < 25",
  },
  created_at: "2026-08-23T00:00:00Z",
  updated_at: "2026-08-23T00:00:00Z",
}

const repository = {
  id: "auto_1",
  version: 1,
  profile_id: profile.id,
  profile_version: profile.version,
  branch: "",
  name: profile.name,
  repository: "owner/repo",
  ref: "",
  target: "all",
  account_ref: profile.account_ref,
  review_focus: profile.review_focus,
  scope_policy: profile.scope_policy,
  reviewer_models: [profile.reviewer_model],
  compare_models: false,
  force: false,
  max_files_per_run: 24,
  max_content_bytes: 524288,
  max_parallel_children: 8,
  auto_continue: true,
  model_prices: {
    [profile.reviewer_model]: {
      input_price_per_1m: 1,
      output_price_per_1m: 4,
    },
  },
  budget: profile.budget,
  status: "idle" as const,
  run_ids: [],
  usage: {
    prompt_tokens: 0,
    completion_tokens: 0,
    total_tokens: 0,
    cached_tokens: 0,
  },
  estimated_cost_usd: 0,
  progress: {
    stage: "waiting",
    completed_batches: 0,
    total_batches: 0,
    coverage_available: false,
    coverage_exact: false,
    selected_files: 0,
    inspected_files: 0,
    reviewed_files: 0,
    remaining_files: 0,
    unsupported_files: 0,
    findings: 0,
    finding_aggregates: 0,
    unaggregated_findings: 0,
  },
  model_stats: [],
  account_limits: [],
  created_at: "2026-08-23T00:00:00Z",
  updated_at: "2026-08-23T00:00:00Z",
}

describe("RepositoryReviewRepositoriesPage", () => {
  beforeEach(() => {
    vi.mocked(listRepositoryReviewAutomations).mockResolvedValue({
      automations: [],
    })
    vi.mocked(listRepositoryReviewProfiles).mockResolvedValue({
      profiles: [profile],
    })
    vi.mocked(listRepositoryReviewAutomationsPage).mockResolvedValue({
      automations: [],
      total: 0,
      next_cursor: "",
      canonical_query: "ORDER BY repository ASC",
      query_schema: { fields: [] },
    })
    vi.mocked(getRepositoryReviewAutomation).mockResolvedValue(repository)
    vi.mocked(createRepositoryReviewAutomation).mockReset()
    vi.mocked(updateRepositoryReviewAutomation).mockReset()
    vi.mocked(deleteRepositoryReviewAutomation).mockReset()
  })

  it("uses the standard collection and exposes repository findings on each item", async () => {
    const user = userEvent.setup()
    const onOpenFindings = vi.fn()
    vi.mocked(listRepositoryReviewAutomationsPage).mockResolvedValue({
      automations: [repository],
      total: 1,
      next_cursor: "",
      canonical_query: "ORDER BY repository ASC",
      query_schema: { fields: [] },
    })

    renderCollection({ onOpenFindings })

    await user.click(
      await screen.findByRole("button", {
        name: "Repository findings for owner/repo",
      }),
    )

    expect(listRepositoryReviewAutomationsPage).toHaveBeenCalledWith(
      {
        query: "ORDER BY repository ASC",
        cursor: undefined,
        limit: 50,
      },
      expect.any(AbortSignal),
    )
    expect(onOpenFindings).toHaveBeenCalledWith(repository)
    expect(
      screen.getByRole("region", { name: "Review repositories list" }),
    ).toBeVisible()
  })

  it("assigns one profile and defaults to the repository base branch", async () => {
    const user = userEvent.setup()
    vi.mocked(createRepositoryReviewAutomation).mockResolvedValue({
      id: "auto_1",
      version: 1,
      profile_id: profile.id,
      profile_version: profile.version,
      branch: "",
      name: profile.name,
      repository: "owner/repo",
      ref: "",
      target: "all",
      account_ref: profile.account_ref,
      review_focus: profile.review_focus,
      scope_policy: profile.scope_policy,
      reviewer_models: [profile.reviewer_model],
      compare_models: false,
      force: false,
      max_files_per_run: 24,
      max_content_bytes: 524288,
      max_parallel_children: 8,
      auto_continue: true,
      model_prices: {
        [profile.reviewer_model]: {
          input_price_per_1m: 1,
          output_price_per_1m: 4,
        },
      },
      budget: profile.budget,
      status: "idle",
      run_ids: [],
      usage: {
        prompt_tokens: 0,
        completion_tokens: 0,
        total_tokens: 0,
        cached_tokens: 0,
      },
      estimated_cost_usd: 0,
      progress: {
        stage: "waiting",
        completed_batches: 0,
        total_batches: 0,
        coverage_available: false,
        coverage_exact: false,
        selected_files: 0,
        inspected_files: 0,
        reviewed_files: 0,
        remaining_files: 0,
        unsupported_files: 0,
        findings: 0,
        finding_aggregates: 0,
        unaggregated_findings: 0,
      },
      model_stats: [],
      account_limits: [],
      created_at: "2026-08-23T00:00:00Z",
      updated_at: "2026-08-23T00:00:00Z",
    })
    renderEditor()

    await user.type(await screen.findByLabelText("Repository"), "owner/repo")
    expect(screen.queryByLabelText("Branch override")).not.toBeInTheDocument()
    await user.click(screen.getByText(/^Advanced/))
    expect(screen.getByLabelText("Branch override")).toHaveValue("")
    await user.click(screen.getByRole("button", { name: "Save repository" }))

    await waitFor(() =>
      expect(createRepositoryReviewAutomation).toHaveBeenCalledWith({
        repository: "owner/repo",
        profile_id: profile.id,
        branch: "",
      }),
    )
  })

  it("blocks a second configuration for the same repository", async () => {
    const user = userEvent.setup()
    vi.mocked(listRepositoryReviewAutomations).mockResolvedValue({
      automations: [{ id: "auto_1", repository: "Owner/Repo" } as never],
    })
    renderEditor()
    await user.type(await screen.findByLabelText("Repository"), "owner/repo")
    expect(
      screen.getByText(/already has a review configuration/i),
    ).toBeVisible()
    expect(
      screen.getByRole("button", { name: "Save repository" }),
    ).toBeDisabled()
  })

  it("accepts branches and rejects revision expressions like the backend", async () => {
    const user = userEvent.setup()
    renderEditor()
    await user.type(await screen.findByLabelText("Repository"), "owner/repo")
    await user.click(screen.getByRole("button", { name: /^Advanced/ }))
    const branch = screen.getByLabelText("Branch override")
    for (const invalid of [
      "HEAD",
      "deadbee",
      "refs/heads/main",
      "tags/v1",
      "feature#1",
      ".hidden/main",
      "feature/build.lock",
      "a".repeat(256),
    ]) {
      fireEvent.change(branch, { target: { value: invalid } })
      expect(
        screen.getByText(/Enter a branch name, or leave blank/i),
      ).toBeVisible()
      expect(
        screen.getByRole("button", { name: "Save repository" }),
      ).toBeDisabled()
    }
    fireEvent.change(branch, { target: { value: "feature/review-ui" } })
    expect(
      screen.queryByText(/Enter a branch name, or leave blank/i),
    ).not.toBeInTheDocument()
    expect(
      screen.getByRole("button", { name: "Save repository" }),
    ).toBeEnabled()
  })

  it("shows save errors inside the repository editor", async () => {
    const user = userEvent.setup()
    vi.mocked(createRepositoryReviewAutomation).mockRejectedValue(
      new Error("Repository is already assigned."),
    )
    renderEditor()
    await user.type(
      await screen.findByLabelText("Repository"),
      "owner/new-repo",
    )
    await user.click(screen.getByRole("button", { name: "Save repository" }))

    const alert = await screen.findByRole("alert")
    expect(alert).toHaveTextContent("already assigned")
  })

  it("retries a transient new-editor context error without loading an empty repository ID", async () => {
    vi.mocked(listRepositoryReviewProfiles)
      .mockRejectedValueOnce(new Error("Temporary profile load failure."))
      .mockResolvedValueOnce({ profiles: [profile] })
    const user = userEvent.setup()

    renderEditor()

    expect(
      await screen.findByText("Temporary profile load failure."),
    ).toBeVisible()
    await user.click(screen.getByRole("button", { name: "Retry" }))

    expect(await screen.findByLabelText("Repository")).toBeVisible()
    expect(getRepositoryReviewAutomation).not.toHaveBeenCalled()
  })
})

function renderEditor() {
  return render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <RepositoryReviewRepositoryEditorPage
        onBack={vi.fn()}
        onSaved={vi.fn()}
      />
    </QueryClientProvider>,
  )
}

function renderCollection({
  onOpenFindings = vi.fn(),
}: {
  onOpenFindings?: (repository: RepositoryReviewAutomation) => void
} = {}) {
  return render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <RepositoryReviewRepositoriesPage
        search={{ q: "ORDER BY repository ASC" }}
        onSearchChange={vi.fn()}
        onAdd={vi.fn()}
        onOpen={vi.fn()}
        onEdit={vi.fn()}
        onOpenFindings={onOpenFindings}
      />
    </QueryClientProvider>,
  )
}
