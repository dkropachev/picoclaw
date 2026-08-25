import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import type { ReactNode } from "react"
import { beforeEach, describe, expect, it, vi } from "vitest"

import type {
  EvaluationProfileOption,
  RepositoryModelEvaluation,
} from "@/api/model-evaluations"
import {
  ModelEvaluationAPIError,
  getModelEvaluation,
  getModelEvaluationCorpus,
  getModelEvaluationOptions,
  listModelEvaluations,
  runModelEvaluation,
  runModelEvaluationAction,
  updateModelEvaluation,
} from "@/api/model-evaluations"

import { ModelEvaluationsPage } from "./model-evaluations-page"

vi.mock("@/api/model-evaluations", () => ({
  ModelEvaluationAPIError: class ModelEvaluationAPIError extends Error {
    readonly status: number
    readonly code?: string

    constructor(status: number, message: string, code?: string) {
      super(message)
      this.status = status
      this.code = code
    }
  },
  getModelEvaluation: vi.fn(),
  getModelEvaluationCorpus: vi.fn(),
  getModelEvaluationOptions: vi.fn(),
  listModelEvaluations: vi.fn(),
  runModelEvaluation: vi.fn(),
  runModelEvaluationAction: vi.fn(),
  updateModelEvaluation: vi.fn(),
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

const profile: EvaluationProfileOption = {
  id: "rrpf_main",
  version: 4,
  name: "Production bugs",
  reviewer_model: "code",
  account_ref: "primary",
  review_focus: "Find concrete correctness and reliability defects.",
  focus: {
    code_types: ["hotpath-code", "code"],
    include_folders: ["pkg"],
    exclude_folders: ["vendor"],
    free_text: "Prioritize persistence boundaries.",
  },
  max_files_per_batch: 8,
  max_content_bytes_per_batch: 131_072,
  max_parallel_children: 3,
  available_models: ["code", "fast"],
}

const alternativeProfile: EvaluationProfileOption = {
  ...profile,
  id: "rrpf_alternative",
  version: 2,
  name: "Alternative account",
  reviewer_model: "fast",
  available_models: ["fast", "review"],
}

const evaluation: RepositoryModelEvaluation = {
  schema_version: 1,
  id: "rme_11111111111111111111111111111111",
  version: 3,
  status: "ready",
  repository: "owner/repo",
  ref: "main",
  candidate_models: ["code", "fast"],
  selector_model_alias: "code",
  judge_model_alias: "code",
  focus: profile.focus,
  default_files_per_language: 20,
  files_per_language: {},
  profile,
  work_sizing_plan: [
    {
      id: "configured",
      axis: "configured",
      files_per_batch: 8,
      content_bytes_per_batch: 131_072,
    },
  ],
  work_sizing_results: [],
  progress: {
    stage: "validating",
    languages: {
      go: {
        available_files: 80,
        selected_files: 20,
        completed_files: 0,
        selected_bytes: 240_000,
        regions: ["pkg", "cmd"],
        limited: false,
      },
      typescript: {
        available_files: 8,
        selected_files: 8,
        completed_files: 0,
        selected_bytes: 40_000,
        regions: ["web/frontend"],
        limited: true,
      },
    },
    total_files: 88,
    selected_files: 28,
    completed_files: 0,
    total_tasks: 56,
    completed_tasks: 0,
    message: "Corpus ready",
    percent: 20,
    updated_at: "2026-08-21T12:01:00Z",
  },
  usage: {
    requests: 1,
    input_tokens: 100,
    cached_input_tokens: 20,
    output_tokens: 30,
    reasoning_tokens: 10,
    duration_millis: 500,
  },
  model_stats: {},
  comparisons: [],
  warnings: [
    "Several catalog languages contain fewer than the default target number of substantive candidates; selected available representative files.",
  ],
  run_ids: ["wr_selector"],
  created_at: "2026-08-21T12:00:00Z",
  updated_at: "2026-08-21T12:01:00Z",
}

const completed: RepositoryModelEvaluation = {
  ...evaluation,
  status: "completed",
  progress: {
    ...evaluation.progress,
    stage: "completed",
    completed_files: 28,
    percent: 100,
  },
  comparisons: [
    {
      model_alias: "code",
      concrete_models: { "gpt-code": 2 },
      completion: "completed",
      failures: 0,
      rank: 1,
      overall_score: 92.5,
      scores: {
        correctness: 95,
        evidence: 93,
        coverage: 88,
        actionability: 94,
      },
      languages: ["go", "typescript"],
      regions: ["pkg", "web/frontend"],
      files_analyzed: 28,
      bytes_analyzed: 280_000,
      confirmed_findings: 5,
      unsupported_claims: 1,
      unsupported_files: 1,
      usage: {
        requests: 4,
        input_tokens: 4_000,
        cached_input_tokens: 2_000,
        output_tokens: 1_000,
        reasoning_tokens: 500,
        duration_millis: 20_000,
      },
      verdict: "Best evidence-grounded analysis.",
      strengths: ["Strong evidence"],
      limitations: ["Higher latency"],
    },
    {
      model_alias: "fast",
      concrete_models: { "gpt-fast": 2 },
      completion: "completed",
      failures: 0,
      rank: 2,
      overall_score: 84,
      scores: {
        correctness: 86,
        evidence: 84,
        coverage: 80,
        actionability: 83,
      },
      languages: ["go", "typescript"],
      regions: ["pkg", "web/frontend"],
      files_analyzed: 28,
      bytes_analyzed: 280_000,
      confirmed_findings: 4,
      unsupported_claims: 2,
      unsupported_files: 2,
      usage: {
        requests: 4,
        input_tokens: 3_000,
        cached_input_tokens: 1_000,
        output_tokens: 700,
        reasoning_tokens: 200,
        duration_millis: 10_000,
      },
      verdict: "Faster but narrower.",
    },
  ],
  finished_at: "2026-08-21T12:03:00Z",
}

function renderPage(onOpenReport?: (evaluationID: string) => void) {
  return render(<ModelEvaluationsPage onOpenReport={onOpenReport} />)
}

describe("ModelEvaluationsPage", () => {
  beforeEach(() => {
    for (const mock of [
      listModelEvaluations,
      getModelEvaluation,
      getModelEvaluationCorpus,
      getModelEvaluationOptions,
      updateModelEvaluation,
      runModelEvaluation,
      runModelEvaluationAction,
    ]) {
      vi.mocked(mock).mockReset()
    }
    vi.mocked(listModelEvaluations).mockResolvedValue([])
    vi.mocked(getModelEvaluationCorpus).mockResolvedValue({
      files: [],
      total: 0,
      offset: 0,
      language_counts: {},
    })
    vi.mocked(getModelEvaluationOptions).mockResolvedValue({
      models: [
        { alias: "code", resolved_model: "gpt-code", available: true },
        { alias: "fast", resolved_model: "gpt-fast", available: true },
        { alias: "review", resolved_model: "gpt-review", available: true },
      ],
      repositories: [
        {
          id: "gw-seastar",
          repository: "https://github.com/scylladb/seastar.git",
          label: "seastar",
        },
      ],
      profiles: [profile, alternativeProfile],
      code_types: ["hotpath-code", "code", "test", "bench-test"],
      max_files_per_language: 20,
      default_files_per_language: 20,
      max_candidate_models: 8,
    })
  })

  it("uses a review profile, auto-selects compatible candidates, and sends only the new payload", async () => {
    const user = userEvent.setup()
    vi.mocked(runModelEvaluation).mockResolvedValue({
      ...evaluation,
      status: "preflighting",
    })
    renderPage()

    const repository = await screen.findByLabelText("Repository")
    expect(repository).toHaveAttribute(
      "list",
      "model-probe-workspace-repositories",
    )
    expect(screen.getByLabelText("Review profile")).toHaveValue(profile.id)
    expect(
      screen.getByRole("checkbox", { name: "Select candidate model code" }),
    ).toBeChecked()
    expect(
      screen.getByRole("checkbox", { name: "Select candidate model code" }),
    ).toBeDisabled()
    expect(
      screen.getByRole("checkbox", { name: "Select candidate model fast" }),
    ).toBeChecked()
    expect(
      screen.getByRole("checkbox", { name: "Select candidate model fast" }),
    ).toBeEnabled()
    expect(
      screen.getByRole("checkbox", { name: "Select candidate model review" }),
    ).toBeDisabled()
    expect(screen.queryByRole("button", { name: /^Advanced/ })).toBeNull()
    expect(screen.getByLabelText("Frozen review profile")).toHaveTextContent(
      "8",
    )
    expect(screen.getByLabelText("Frozen review profile")).toHaveTextContent(
      "128.0 KiB",
    )

    await user.type(repository, "owner/repo")
    await user.type(screen.getByLabelText("Revision"), "release")
    await user.click(screen.getByRole("button", { name: "Run probe" }))

    await waitFor(() => expect(runModelEvaluation).toHaveBeenCalledTimes(1))
    expect(runModelEvaluation).toHaveBeenCalledWith({
      repository: "owner/repo",
      profile_id: profile.id,
      candidate_models: ["code", "fast"],
      ref: "release",
    })
  })

  it("reconciles candidates when the selected profile account changes", async () => {
    const user = userEvent.setup()
    renderPage()
    await screen.findByLabelText("Review profile")

    await user.selectOptions(
      screen.getByLabelText("Review profile"),
      alternativeProfile.id,
    )

    expect(
      screen.getByRole("checkbox", { name: "Select candidate model code" }),
    ).toBeDisabled()
    expect(
      screen.getByRole("checkbox", { name: "Select candidate model fast" }),
    ).toBeChecked()
    expect(
      screen.getByRole("checkbox", { name: "Select candidate model fast" }),
    ).toBeDisabled()
    expect(
      screen.getByRole("checkbox", { name: "Select candidate model review" }),
    ).toBeChecked()
  })

  it("separates status, language summary, and paged corpus preview into accessible tabs", async () => {
    const user = userEvent.setup()
    const corpusFile = (path: string, candidateID: string) => ({
      candidate_id: candidateID,
      path,
      blob_sha: "a".repeat(40),
      size_bytes: 6_000,
      language: "go",
      code_type: "code" as const,
      module: "pkg",
      region: "pkg",
      chunks: [],
    })
    vi.mocked(listModelEvaluations).mockResolvedValue([evaluation])
    vi.mocked(getModelEvaluation).mockResolvedValue(evaluation)
    vi.mocked(getModelEvaluationCorpus)
      .mockResolvedValueOnce({
        files: [corpusFile("pkg/first.go", "cand_first")],
        total: 21,
        offset: 0,
        next_offset: 20,
        commit_sha: "a".repeat(40),
        inventory_hash: "sha256:inventory",
        language_counts: { go: 21 },
      })
      .mockResolvedValueOnce({
        files: [corpusFile("pkg/last.go", "cand_last")],
        total: 21,
        offset: 20,
        commit_sha: "a".repeat(40),
        inventory_hash: "sha256:inventory",
        language_counts: { go: 21 },
      })
    renderPage()

    const tabs = await screen.findByRole("tablist", {
      name: "Probe run details",
    })
    expect(within(tabs).getByRole("tab", { name: "Status" })).toHaveAttribute(
      "aria-selected",
      "true",
    )
    expect(
      screen.getByText(/Several catalog languages contain fewer/),
    ).toBeVisible()
    expect(
      screen.queryByRole("heading", { name: "Corpus by language" }),
    ).toBeNull()

    const languageTab = within(tabs).getByRole("tab", {
      name: "Corpus by language",
    })
    expect(languageTab).not.toHaveAttribute("aria-controls")
    languageTab.focus()
    fireEvent.keyDown(languageTab, { key: "ArrowRight" })
    expect(
      within(tabs).getByRole("tab", { name: "Corpus preview" }),
    ).toHaveFocus()
    expect(await screen.findByText("pkg/first.go")).toBeVisible()

    await user.click(
      within(tabs).getByRole("tab", { name: "Corpus by language" }),
    )
    expect(languageTab).toHaveAttribute("aria-controls")
    expect(
      screen.getByRole("heading", { name: "Corpus by language" }),
    ).toBeVisible()
    expect(screen.getByText("typescript").closest("tr")).toHaveTextContent(
      "limited",
    )

    await user.click(within(tabs).getByRole("tab", { name: "Corpus preview" }))
    await user.click(screen.getByRole("button", { name: "Next corpus page" }))
    expect(await screen.findByText("pkg/last.go")).toBeVisible()
    expect(getModelEvaluationCorpus).toHaveBeenLastCalledWith(
      evaluation.id,
      20,
      20,
      expect.any(AbortSignal),
    )
  })

  it("embeds the complete report in the final tab and retains its deep link", async () => {
    const user = userEvent.setup()
    const onOpenReport = vi.fn()
    vi.mocked(listModelEvaluations).mockResolvedValue([completed])
    vi.mocked(getModelEvaluation).mockResolvedValue(completed)
    renderPage(onOpenReport)

    const reportTab = await screen.findByRole("tab", { name: "Final report" })
    expect(reportTab).toBeEnabled()
    expect(
      screen.queryByRole("heading", {
        name: "Use code when review quality matters.",
      }),
    ).toBeNull()
    await user.click(reportTab)
    expect(
      screen.getByRole("heading", {
        name: "Use code when review quality matters.",
      }),
    ).toBeVisible()
    expect(
      screen.getByRole("heading", { name: "Work-sizing quality ceilings" }),
    ).toBeVisible()
    await user.click(
      screen.getByRole("button", { name: "Open dedicated report" }),
    )
    expect(onOpenReport).toHaveBeenCalledWith(completed.id)
  })

  it("cancels an active probe with its exact version", async () => {
    const user = userEvent.setup()
    const running = { ...evaluation, status: "running" as const }
    vi.mocked(listModelEvaluations).mockResolvedValue([running])
    vi.mocked(getModelEvaluation).mockResolvedValue(running)
    vi.mocked(runModelEvaluationAction).mockResolvedValue({
      ...running,
      status: "canceled",
      version: 4,
    })
    renderPage()

    await user.click(await screen.findByRole("button", { name: "Cancel" }))
    expect(runModelEvaluationAction).toHaveBeenCalledWith(
      evaluation.id,
      "cancel",
      evaluation.version,
    )
  })

  it("offers a reload when a profile-based draft patch loses its version fence", async () => {
    const user = userEvent.setup()
    const draft = { ...evaluation, status: "draft" as const }
    vi.mocked(listModelEvaluations).mockResolvedValue([draft])
    vi.mocked(getModelEvaluation).mockResolvedValue(draft)
    vi.mocked(updateModelEvaluation).mockRejectedValue(
      new ModelEvaluationAPIError(
        409,
        "repository model evaluation changed",
        "stale_repository_model_evaluation",
      ),
    )
    renderPage()

    await screen.findByDisplayValue("owner/repo")
    const repository = screen.getByLabelText("Repository")
    await user.clear(repository)
    await user.type(repository, "other/repo")
    await user.clear(screen.getByLabelText("Revision"))
    await user.click(screen.getByRole("button", { name: "Run probe" }))

    expect(
      await screen.findByText("repository model evaluation changed"),
    ).toBeVisible()
    expect(updateModelEvaluation).toHaveBeenCalledWith(draft.id, {
      repository: "other/repo",
      profile_id: profile.id,
      candidate_models: ["code", "fast"],
      ref: "",
      expected_version: draft.version,
    })
    await user.click(screen.getByRole("button", { name: "Reload latest" }))
    expect(getModelEvaluation).toHaveBeenCalledWith(draft.id, undefined)
  })

  it("requires an existing review profile", async () => {
    vi.mocked(getModelEvaluationOptions).mockResolvedValue({
      models: [],
      repositories: [],
      profiles: [],
      code_types: [],
      max_files_per_language: 20,
      default_files_per_language: 20,
      max_candidate_models: 8,
    })
    renderPage()

    expect(
      await screen.findByText(
        "Create a repository review profile before running a probe.",
      ),
    ).toBeVisible()
    expect(screen.getByRole("button", { name: "Run probe" })).toBeDisabled()
  })

  it("distinguishes incompatible profiles from an empty profile catalog", async () => {
    vi.mocked(getModelEvaluationOptions).mockResolvedValue({
      models: [],
      repositories: [],
      profiles: [],
      profile_count: 2,
      code_types: [],
      max_files_per_language: 20,
      default_files_per_language: 20,
      max_candidate_models: 8,
    })
    renderPage()

    expect(
      await screen.findByText(/No review profile has a runnable reviewer/),
    ).toBeVisible()
  })
})
