import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { fireEvent, render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { beforeEach, describe, expect, it, vi } from "vitest"

import {
  createRepositoryReviewAutomation,
  deleteRepositoryReviewAutomation,
  listRepositoryReviewAutomations,
  listRepositoryReviewProfiles,
  updateRepositoryReviewAutomation,
} from "@/api/repository-reviews"
import { RepositoryReviewRepositoriesPage } from "@/components/repository-reviews/repository-review-repositories-page"

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
  createRepositoryReviewAutomation: vi.fn(),
  deleteRepositoryReviewAutomation: vi.fn(),
  listRepositoryReviewAutomations: vi.fn(),
  listRepositoryReviewProfiles: vi.fn(),
  updateRepositoryReviewAutomation: vi.fn(),
}))

const profile = {
  id: "profile_1",
  version: 2,
  name: "Core bugs",
  reviewer_model: "review-model",
  review_focus: "Correctness bugs",
  scope_policy: {
    code_types: ["code" as const],
    include_folders: [],
    exclude_folders: [],
    free_text: "",
  },
  model_price: { input_price_per_1m: 1, output_price_per_1m: 4 },
  force: false,
  auto_continue: true,
  max_files_per_run: 24,
  max_content_bytes: 524288,
  max_parallel_children: 1,
  estimated_output_tokens: 4096,
  budget: {
    max_total_tokens: 100000,
    max_estimated_cost_usd: 10,
    account_ids: [],
    min_remaining_percent: 0,
    min_remaining_percent_by_window: {},
    auto_resume: true,
    pause_on_unknown: false,
    check_interval_seconds: 900,
  },
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
    vi.mocked(createRepositoryReviewAutomation).mockReset()
    vi.mocked(updateRepositoryReviewAutomation).mockReset()
    vi.mocked(deleteRepositoryReviewAutomation).mockReset()
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
      review_focus: profile.review_focus,
      scope_policy: profile.scope_policy,
      reviewer_models: [profile.reviewer_model],
      compare_models: false,
      force: false,
      max_files_per_run: 24,
      max_content_bytes: 524288,
      max_parallel_children: 1,
      estimated_output_tokens: 4096,
      auto_continue: true,
      model_prices: { [profile.reviewer_model]: profile.model_price },
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
        reviewed_files: 0,
        remaining_files: 0,
        unsupported_files: 0,
        findings: 0,
      },
      model_stats: [],
      account_limits: [],
      created_at: "2026-08-23T00:00:00Z",
      updated_at: "2026-08-23T00:00:00Z",
    })
    renderPage()

    await user.click(
      await screen.findByRole("button", { name: "Add repository" }),
    )
    await user.type(screen.getByLabelText("Repository"), "owner/repo")
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
    renderPage()
    await user.click(
      await screen.findByRole("button", { name: "Add repository" }),
    )
    await user.type(screen.getByLabelText("Repository"), "owner/repo")
    expect(
      screen.getByText(/already has a review configuration/i),
    ).toBeVisible()
    expect(
      screen.getByRole("button", { name: "Save repository" }),
    ).toBeDisabled()
  })

  it("accepts branches and rejects revision expressions like the backend", async () => {
    const user = userEvent.setup()
    renderPage()
    await user.click(
      await screen.findByRole("button", { name: "Add repository" }),
    )
    await user.type(screen.getByLabelText("Repository"), "owner/repo")
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
})

function renderPage() {
  return render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <RepositoryReviewRepositoriesPage />
    </QueryClientProvider>,
  )
}
