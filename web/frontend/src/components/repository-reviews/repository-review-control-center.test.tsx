import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { act, render, screen, waitFor, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import type {
  RepositoryReviewAutomation,
  RepositoryReviewAutomationOptions,
} from "@/api/repository-reviews"
import {
  createRepositoryReviewAutomation,
  deleteRepositoryReviewAutomation,
  getRepositoryReviewAutomationOptions,
  listRepositoryReviewAutomations,
  pauseRepositoryReviewAutomation,
  restartRepositoryReviewAutomation,
  resumeRepositoryReviewAutomation,
  startRepositoryReviewAutomation,
  updateRepositoryReviewAutomation,
} from "@/api/repository-reviews"
import { RepositoryReviewControlCenter } from "@/components/repository-reviews/repository-review-control-center"

vi.mock("@/api/repository-reviews", () => ({
  createRepositoryReviewAutomation: vi.fn(),
  deleteRepositoryReviewAutomation: vi.fn(),
  getRepositoryReviewAutomationOptions: vi.fn(),
  listRepositoryReviewAutomations: vi.fn(),
  pauseRepositoryReviewAutomation: vi.fn(),
  restartRepositoryReviewAutomation: vi.fn(),
  resumeRepositoryReviewAutomation: vi.fn(),
  startRepositoryReviewAutomation: vi.fn(),
  updateRepositoryReviewAutomation: vi.fn(),
}))

const options: RepositoryReviewAutomationOptions = {
  models: [
    {
      alias: "fast-review",
      resolved_model: "acme/fast-v2",
      provider: "acme",
      available: true,
      price_known: true,
      input_price_per_1m: 0.2,
      output_price_per_1m: 0.8,
      equivalent_model: "premium-review",
    },
    {
      alias: "premium-review",
      resolved_model: "acme/premium-v4",
      provider: "acme",
      available: true,
      price_known: true,
      input_price_per_1m: 2,
      output_price_per_1m: 8,
    },
  ],
  accounts: [
    {
      id: "acct-primary",
      provider: "acme",
      label: "Primary account",
      status: "ready",
      entries: [
        { name: "Chat", status: "available" },
        { name: "Premium", status: "available", window: "5h" },
      ],
    },
  ],
}

const automation: RepositoryReviewAutomation = {
  id: "auto_1",
  version: 3,
  name: "Core pre-review",
  repository: "owner/repo",
  ref: "HEAD",
  target: "all",
  review_focus: "Correctness and security bugs",
  scope_policy: {
    code_types: ["hotpath-code", "code"],
    include_folders: ["cmd", "internal/runtime"],
    exclude_folders: ["internal/runtime/generated"],
    free_text: "Prioritize authorization boundaries.",
  },
  reviewer_models: ["fast-review", "premium-review"],
  compare_models: true,
  force: false,
  max_files_per_run: 24,
  max_content_bytes: 524_288,
  max_parallel_children: 2,
  estimated_output_tokens: 4_096,
  auto_continue: true,
  model_prices: {
    "fast-review": {
      input_price_per_1m: 0.2,
      output_price_per_1m: 0.8,
    },
    "premium-review": {
      input_price_per_1m: 2,
      output_price_per_1m: 8,
    },
  },
  budget: {
    max_total_tokens: 250_000,
    max_estimated_cost_usd: 25,
    account_ids: ["acct-primary"],
    min_remaining_percent: 10,
    min_remaining_percent_by_window: { daily: 15, weekly: 10 },
    auto_resume: true,
    pause_on_unknown: true,
    check_interval_seconds: 900,
  },
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
  scope_plan: {
    commit_sha: "a".repeat(40),
    policy_hash: "b".repeat(64),
    hash: "c".repeat(64),
    summary: "18 production files selected",
    rationale: "Matched production code under the requested folders.",
    warnings: ["Generated files were excluded."],
    counts: {
      total_files: 100,
      code_type_files: 60,
      include_files: 24,
      excluded_files: 6,
      selected_files: 18,
    },
  },
  created_at: "2026-08-20T12:00:00Z",
  updated_at: "2026-08-20T12:00:00Z",
}

describe("RepositoryReviewControlCenter", () => {
  beforeEach(() => {
    vi.mocked(listRepositoryReviewAutomations).mockReset()
    vi.mocked(getRepositoryReviewAutomationOptions).mockReset()
    vi.mocked(createRepositoryReviewAutomation).mockReset()
    vi.mocked(updateRepositoryReviewAutomation).mockReset()
    vi.mocked(deleteRepositoryReviewAutomation).mockReset()
    vi.mocked(startRepositoryReviewAutomation).mockReset()
    vi.mocked(pauseRepositoryReviewAutomation).mockReset()
    vi.mocked(resumeRepositoryReviewAutomation).mockReset()
    vi.mocked(restartRepositoryReviewAutomation).mockReset()
    vi.mocked(listRepositoryReviewAutomations).mockResolvedValue({
      automations: [],
    })
    vi.mocked(getRepositoryReviewAutomationOptions).mockResolvedValue(options)
  })

  afterEach(() => vi.useRealTimers())

  it("sets up an empty-state profile and sends the complete create payload", async () => {
    const user = userEvent.setup()
    vi.mocked(createRepositoryReviewAutomation).mockResolvedValue(automation)
    renderControlCenter()

    expect(
      await screen.findByText("Set up your first pre-review"),
    ).toBeVisible()
    await user.click(
      screen.getByRole("button", { name: "Configure pre-review" }),
    )
    await user.type(screen.getByLabelText("Profile name"), "Core review")
    await user.type(screen.getByLabelText("Repository"), "owner/repo")
    expect(screen.getByText("Review scope")).toBeVisible()
    await user.click(screen.getByRole("checkbox", { name: /Tests/u }))
    await user.type(
      screen.getByLabelText("Include folder prefixes"),
      "cmd{enter}internal/runtime",
    )
    await user.type(
      screen.getByLabelText("Exclude folder prefixes"),
      "internal/runtime/generated",
    )
    await user.type(
      screen.getByLabelText("Additional scope guidance"),
      "Prioritize authorization boundaries.",
    )
    await user.click(screen.getByLabelText(/fast-review/u))
    await user.click(screen.getByLabelText(/Primary account/u))
    await user.type(screen.getByLabelText("Another limit window"), "monthly")
    await user.click(screen.getByRole("button", { name: "Add window" }))
    await user.clear(screen.getByLabelText("monthly remaining (%)"))
    await user.type(screen.getByLabelText("monthly remaining (%)"), "30")
    await user.clear(screen.getByLabelText("Maximum total tokens"))
    await user.type(screen.getByLabelText("Maximum total tokens"), "100000")
    await user.clear(screen.getByLabelText("Weekly remaining (%)"))
    await user.type(screen.getByLabelText("Weekly remaining (%)"), "20")
    await user.click(
      screen.getByRole("button", { name: "Save review profile" }),
    )

    await waitFor(() =>
      expect(createRepositoryReviewAutomation).toHaveBeenCalledWith(
        expect.objectContaining({
          name: "Core review",
          repository: "owner/repo",
          ref: "HEAD",
          target: "all",
          scope_policy: {
            code_types: ["hotpath-code", "code", "test"],
            include_folders: ["cmd", "internal/runtime"],
            exclude_folders: ["internal/runtime/generated"],
            free_text: "Prioritize authorization boundaries.",
          },
          reviewer_models: ["fast-review"],
          compare_models: false,
          max_files_per_run: 24,
          max_content_bytes: 524_288,
          max_parallel_children: 1,
          estimated_output_tokens: 4_096,
          auto_continue: true,
          model_prices: {
            "fast-review": expect.objectContaining({
              input_price_per_1m: 0.2,
              output_price_per_1m: 0.8,
              equivalent_model: "premium-review",
            }),
          },
          budget: expect.objectContaining({
            max_total_tokens: 100_000,
            account_ids: ["acct-primary"],
            min_remaining_percent_by_window: {
              daily: 15,
              weekly: 20,
              monthly: 30,
            },
            auto_resume: true,
            pause_on_unknown: true,
          }),
        }),
      ),
    )
  })

  it("requires a code type and safe repository-relative folder prefixes", async () => {
    const user = userEvent.setup()
    renderControlCenter()

    await user.click(
      await screen.findByRole("button", { name: "Configure pre-review" }),
    )
    await user.click(
      screen.getByRole("checkbox", { name: /Hot-path production code/u }),
    )
    await user.click(
      screen.getByRole("checkbox", { name: /^Production code/u }),
    )
    expect(screen.getByText("Select at least one code type.")).toBeVisible()
    expect(
      screen.getByRole("button", { name: "Save review profile" }),
    ).toBeDisabled()

    await user.click(
      screen.getByRole("checkbox", { name: /^Production code/u }),
    )
    await user.type(
      screen.getByLabelText("Include folder prefixes"),
      "../outside",
    )
    expect(
      screen.getByText(
        "Include folders must be canonical repository-relative prefixes.",
      ),
    ).toBeVisible()
    expect(
      screen.getByRole("button", { name: "Save review profile" }),
    ).toBeDisabled()
  })

  it("does not represent an unknown model price as free", async () => {
    const user = userEvent.setup()
    vi.mocked(getRepositoryReviewAutomationOptions).mockResolvedValue({
      models: [
        {
          alias: "unpriced-review",
          resolved_model: "acme/new-model",
          provider: "acme",
          available: true,
          price_known: false,
          input_price_per_1m: 0,
          output_price_per_1m: 0,
        },
      ],
      accounts: [],
    })
    vi.mocked(createRepositoryReviewAutomation).mockResolvedValue(automation)
    renderControlCenter()

    await user.click(
      await screen.findByRole("button", { name: "Configure pre-review" }),
    )
    expect(screen.getByText("price unknown")).toBeVisible()
    await user.type(screen.getByLabelText("Profile name"), "Unknown price")
    await user.type(screen.getByLabelText("Repository"), "owner/repo")
    await user.click(screen.getByLabelText(/unpriced-review/u))
    await user.click(
      screen.getByRole("button", { name: "Save review profile" }),
    )

    await waitFor(() =>
      expect(createRepositoryReviewAutomation).toHaveBeenCalledWith(
        expect.objectContaining({
          reviewer_models: ["unpriced-review"],
          model_prices: {},
          budget: expect.objectContaining({
            max_estimated_cost_usd: 0,
            account_ids: [],
            min_remaining_percent: 0,
            min_remaining_percent_by_window: {},
            pause_on_unknown: false,
          }),
        }),
      ),
    )
  })

  it("treats cleared catalog prices as unknown instead of free", async () => {
    const user = userEvent.setup()
    vi.mocked(createRepositoryReviewAutomation).mockResolvedValue(automation)
    renderControlCenter()

    await user.click(
      await screen.findByRole("button", { name: "Configure pre-review" }),
    )
    await user.type(screen.getByLabelText("Profile name"), "Cleared price")
    await user.type(screen.getByLabelText("Repository"), "owner/repo")
    await user.click(screen.getByLabelText(/fast-review/u))
    await user.clear(screen.getByLabelText("Input / 1M ($)"))
    await user.clear(screen.getByLabelText("Output / 1M ($)"))
    expect(screen.getByLabelText("Maximum estimated cost ($)")).toBeDisabled()
    await user.click(
      screen.getByRole("button", { name: "Save review profile" }),
    )

    await waitFor(() =>
      expect(createRepositoryReviewAutomation).toHaveBeenCalledWith(
        expect.objectContaining({
          model_prices: {},
          budget: expect.objectContaining({
            max_estimated_cost_usd: 0,
          }),
        }),
      ),
    )
  })

  it("shows rejected saves inside the editor with conflict recovery guidance", async () => {
    const user = userEvent.setup()
    vi.mocked(createRepositoryReviewAutomation).mockRejectedValue(
      Object.assign(new Error("Review profile changed."), { status: 409 }),
    )
    renderControlCenter()

    await user.click(
      await screen.findByRole("button", { name: "Configure pre-review" }),
    )
    await user.type(screen.getByLabelText("Profile name"), "Conflicted")
    await user.type(screen.getByLabelText("Repository"), "owner/repo")
    await user.click(screen.getByLabelText(/fast-review/u))
    await user.click(
      screen.getByRole("button", { name: "Save review profile" }),
    )

    expect(
      await screen.findByText(
        /close and reopen the editor to load the latest/i,
      ),
    ).toBeVisible()
    expect(screen.getByRole("dialog")).toBeVisible()
  })

  it("surfaces automation option failures and retries them", async () => {
    const user = userEvent.setup()
    vi.mocked(getRepositoryReviewAutomationOptions).mockRejectedValueOnce(
      new Error("configuration unavailable"),
    )
    renderControlCenter()

    await user.click(
      await screen.findByRole("button", { name: "Configure pre-review" }),
    )
    expect(
      await screen.findByText(
        /options could not be loaded: configuration unavailable/i,
      ),
    ).toBeVisible()
    vi.mocked(getRepositoryReviewAutomationOptions).mockResolvedValue(options)
    await user.click(screen.getByRole("button", { name: "Retry options" }))
    expect(await screen.findByLabelText(/fast-review/u)).toBeVisible()
  })

  it("starts an idle review profile", async () => {
    const user = userEvent.setup()
    vi.mocked(listRepositoryReviewAutomations).mockResolvedValue({
      automations: [automation],
    })
    vi.mocked(startRepositoryReviewAutomation).mockResolvedValue({
      ...automation,
      version: 4,
      status: "running",
      progress: { ...automation.progress, stage: "inventory" },
    })
    renderControlCenter()

    expect(await screen.findByText("Scope preflight")).toBeVisible()
    expect(screen.getByText("18 production files selected")).toBeVisible()
    expect(screen.getByText("Generated files were excluded.")).toBeVisible()
    await user.click(screen.getByRole("button", { name: "Start review" }))
    await waitFor(() =>
      expect(startRepositoryReviewAutomation).toHaveBeenCalledWith("auto_1", {
        expected_version: 3,
      }),
    )
    expect(screen.getByText("inventory")).toBeVisible()
  })

  it("lets an operator repair a saved model that left the catalog", async () => {
    const user = userEvent.setup()
    const stale = {
      ...automation,
      reviewer_models: ["retired-review"],
      compare_models: false,
      model_prices: {
        "retired-review": {
          input_price_per_1m: 1,
          output_price_per_1m: 2,
        },
      },
    }
    vi.mocked(listRepositoryReviewAutomations).mockResolvedValue({
      automations: [stale],
    })
    vi.mocked(updateRepositoryReviewAutomation).mockResolvedValue(automation)
    renderControlCenter()

    await user.click(
      await screen.findByRole("button", { name: "Edit Core pre-review" }),
    )
    const retired = screen.getByLabelText(/retired-review/u)
    expect(retired).toBeEnabled()
    expect(
      screen.getByText(/Remove unavailable selected models before saving/u),
    ).toBeVisible()
    await user.click(retired)
    await user.click(screen.getByLabelText(/fast-review/u))
    await user.click(
      screen.getByRole("button", { name: "Save review profile" }),
    )

    await waitFor(() =>
      expect(updateRepositoryReviewAutomation).toHaveBeenCalledWith(
        "auto_1",
        expect.objectContaining({
          expected_version: 3,
          reviewer_models: ["fast-review"],
          compare_models: false,
        }),
      ),
    )
  })

  it("polls a running review into its completed state", async () => {
    vi.useFakeTimers()
    vi.mocked(listRepositoryReviewAutomations)
      .mockResolvedValueOnce({
        automations: [{ ...automation, status: "running" }],
      })
      .mockResolvedValueOnce({
        automations: [
          {
            ...automation,
            status: "idle",
            auto_continue: true,
            progress: {
              ...automation.progress,
              stage: "next batch queued",
              remaining_files: 12,
            },
          },
        ],
      })
      .mockResolvedValue({
        automations: [
          {
            ...automation,
            status: "completed",
            progress: { ...automation.progress, stage: "complete" },
          },
        ],
      })
    renderControlCenter()

    await act(async () => vi.advanceTimersByTimeAsync(1))
    expect(screen.getByText("running")).toBeVisible()
    await act(async () => vi.advanceTimersByTimeAsync(2_000))
    expect(screen.getByText("continuing")).toBeVisible()
    expect(
      screen.queryByRole("button", { name: "Start review" }),
    ).not.toBeInTheDocument()
    await act(async () => vi.advanceTimersByTimeAsync(2_000))
    expect(listRepositoryReviewAutomations).toHaveBeenCalledTimes(3)
    expect(screen.getByText("completed")).toBeVisible()
    expect(screen.getByText("complete")).toBeVisible()
  })

  it("asks for a safe checkpoint before pausing a running review", async () => {
    const user = userEvent.setup()
    const running = {
      ...automation,
      status: "running" as const,
      active_run_id: "run/2",
      run_ids: ["run/1", "run/2"],
      started_at: "2026-08-20T12:05:00Z",
    }
    vi.mocked(listRepositoryReviewAutomations).mockResolvedValue({
      automations: [running],
    })
    vi.mocked(pauseRepositoryReviewAutomation).mockResolvedValue({
      ...running,
      version: 4,
      status: "stopping",
    })
    renderControlCenter()

    expect(
      await screen.findByRole("link", { name: "Active run run/2" }),
    ).toHaveAttribute("href", "/agent/workflows?mode=operate&run=run%2F2")
    expect(screen.getByText("Run history (2)")).toBeVisible()
    await user.click(screen.getByRole("button", { name: "Pause safely" }))
    expect(
      screen.getByText(/in-flight work is allowed to reach a safe checkpoint/i),
    ).toBeVisible()
    await user.click(screen.getByRole("button", { name: "Pause review" }))
    await waitFor(() =>
      expect(pauseRepositoryReviewAutomation).toHaveBeenCalledWith("auto_1", {
        expected_version: 3,
      }),
    )
  })

  it("explains an account-limit pause and its automatic recovery check", async () => {
    vi.mocked(listRepositoryReviewAutomations).mockResolvedValue({
      automations: [
        {
          ...automation,
          status: "paused",
          pause_reason: "account_limit",
          pause_detail: "Weekly account balance is below 10%.",
          next_check_at: "2026-08-20T12:15:00Z",
        },
      ],
    })
    renderControlCenter()

    expect(
      await screen.findByText("Paused: account limit guardrail"),
    ).toBeVisible()
    expect(
      screen.getByText("Weekly account balance is below 10%."),
    ).toBeVisible()
    expect(screen.getByText(/Auto-resume is enabled/u)).toBeVisible()
    expect(screen.getByText(/next account check/u)).toBeVisible()
  })

  it("requires confirmation to resume and reset an exhausted budget", async () => {
    const user = userEvent.setup()
    const paused = {
      ...automation,
      status: "paused" as const,
      pause_reason: "token_budget" as const,
      usage: { ...automation.usage, total_tokens: 250_000 },
    }
    vi.mocked(listRepositoryReviewAutomations).mockResolvedValue({
      automations: [paused],
    })
    vi.mocked(resumeRepositoryReviewAutomation).mockResolvedValue({
      ...automation,
      status: "running",
    })
    renderControlCenter()

    expect(
      await screen.findByText("Paused: token budget reached"),
    ).toBeVisible()
    expect(
      screen.queryByText(/Auto-resume is enabled/u),
    ).not.toBeInTheDocument()
    await user.click(
      screen.getByRole("button", { name: "Resume and reset budget" }),
    )
    expect(screen.getByText("Reset accumulated budget?")).toBeVisible()
    await user.click(screen.getByRole("button", { name: "Reset and resume" }))
    await waitFor(() =>
      expect(resumeRepositoryReviewAutomation).toHaveBeenCalledWith("auto_1", {
        expected_version: 3,
        reset_budget: true,
      }),
    )
  })

  it("shows failed run detail and confirms the destructive restart reset", async () => {
    const user = userEvent.setup()
    const failed = {
      ...automation,
      status: "failed" as const,
      pause_reason: "run_failed" as const,
      pause_detail: "Reviewer response was invalid after three attempts.",
      progress: { ...automation.progress, stage: "failed" },
    }
    vi.mocked(listRepositoryReviewAutomations).mockResolvedValue({
      automations: [failed],
    })
    vi.mocked(restartRepositoryReviewAutomation).mockResolvedValue({
      ...automation,
      status: "running",
    })
    renderControlCenter()

    expect(await screen.findByText("Failed: review run failed")).toBeVisible()
    expect(
      screen.getByText("Reviewer response was invalid after three attempts."),
    ).toBeVisible()
    await user.click(screen.getByRole("button", { name: "Restart" }))
    expect(restartRepositoryReviewAutomation).not.toHaveBeenCalled()
    expect(
      screen.getByText(/resets accumulated token, cost, progress/i),
    ).toBeVisible()
    await user.click(screen.getByRole("button", { name: "Reset and restart" }))
    await waitFor(() =>
      expect(restartRepositoryReviewAutomation).toHaveBeenCalledWith("auto_1", {
        expected_version: 3,
      }),
    )
  })

  it("highlights the cheapest known model comparison", async () => {
    vi.mocked(listRepositoryReviewAutomations).mockResolvedValue({
      automations: [
        {
          ...automation,
          status: "completed",
          model_prices: {
            ...automation.model_prices,
            "failed-cheap": {
              input_price_per_1m: 0.01,
              output_price_per_1m: 0.01,
            },
            "unpriced-review": {
              input_price_per_1m: 0,
              output_price_per_1m: 0,
              subscription: true,
            },
          },
          model_stats: [
            {
              model: "fast-review",
              prompt_tokens: 5_000,
              completion_tokens: 1_000,
              total_tokens: 6_000,
              cached_tokens: 500,
              estimated_cost_usd: 0.01,
              requests: 4,
              failures: 0,
              reviewed_files: 20,
              findings: 5,
              latency_ms: 2_400,
            },
            {
              model: "premium-review",
              prompt_tokens: 5_000,
              completion_tokens: 1_000,
              total_tokens: 6_000,
              cached_tokens: 0,
              estimated_cost_usd: 0.1,
              requests: 4,
              failures: 1,
              reviewed_files: 20,
              findings: 6,
              latency_ms: 3_000,
            },
            {
              model: "failed-cheap",
              prompt_tokens: 100,
              completion_tokens: 10,
              total_tokens: 110,
              cached_tokens: 0,
              estimated_cost_usd: 0.0001,
              requests: 2,
              failures: 2,
              reviewed_files: 0,
              findings: 0,
              latency_ms: 500,
            },
            {
              model: "unpriced-review",
              prompt_tokens: 500,
              completion_tokens: 100,
              total_tokens: 600,
              cached_tokens: 0,
              estimated_cost_usd: 0,
              requests: 1,
              failures: 0,
              reviewed_files: 2,
              findings: 1,
              latency_ms: 600,
            },
          ],
        },
      ],
    })
    renderControlCenter()

    const table = await screen.findByRole("table", { name: "Model comparison" })
    const cheapRow = within(table).getByText("fast-review").closest("tr")
    expect(cheapRow).not.toBeNull()
    expect(within(cheapRow!).getByText("cheapest")).toBeVisible()
    expect(within(cheapRow!).getByText("$0.0020")).toBeVisible()
    const premiumRow = within(table).getByText("premium-review").closest("tr")
    expect(within(premiumRow!).queryByText("cheapest")).not.toBeInTheDocument()
    const failedRow = within(table).getByText("failed-cheap").closest("tr")
    expect(within(failedRow!).queryByText("cheapest")).not.toBeInTheDocument()
    const unpricedRow = within(table).getByText("unpriced-review").closest("tr")
    expect(within(unpricedRow!).getByText("unknown")).toBeVisible()
    expect(screen.getByText("Estimated cost (partial)")).toBeVisible()
  })
})

function renderControlCenter() {
  const queryClient = new QueryClient({
    defaultOptions: {
      queries: { retry: false },
      mutations: { retry: false },
    },
  })
  return render(
    <QueryClientProvider client={queryClient}>
      <RepositoryReviewControlCenter />
    </QueryClientProvider>,
  )
}
