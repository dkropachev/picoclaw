import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import type { ReactNode } from "react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import type { RepositoryModelEvaluation } from "@/api/model-evaluations"
import {
  ModelEvaluationAPIError,
  createModelEvaluation,
  deleteModelEvaluation,
  getModelEvaluation,
  getModelEvaluationCorpus,
  getModelEvaluationOptions,
  listModelEvaluations,
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
  createModelEvaluation: vi.fn(),
  deleteModelEvaluation: vi.fn(),
  getModelEvaluation: vi.fn(),
  getModelEvaluationCorpus: vi.fn(),
  getModelEvaluationOptions: vi.fn(),
  listModelEvaluations: vi.fn(),
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

const evaluation: RepositoryModelEvaluation = {
  schema_version: 1,
  id: "rme_11111111111111111111111111111111",
  version: 3,
  status: "ready",
  repository: "owner/repo",
  ref: "main",
  candidate_models: ["code", "fast"],
  selector_model_alias: "review",
  judge_model_alias: "review",
  focus: {
    code_types: ["hotpath-code", "code", "test", "bench-test"],
    include_folders: ["pkg", "web"],
    exclude_folders: ["web/fixtures"],
    free_text: "Choose request and storage boundaries.",
  },
  default_files_per_language: 20,
  files_per_language: {},
  progress: {
    stage: "validating",
    languages: {
      go: {
        available_files: 80,
        selected_files: 20,
        completed_files: 0,
        selected_bytes: 240_000,
        regions: ["pkg", "cmd", "web/backend"],
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
  },
  usage: {
    requests: 1,
    input_tokens: 100,
    cached_input_tokens: 0,
    output_tokens: 30,
    reasoning_tokens: 0,
    duration_millis: 500,
  },
  model_stats: {},
  comparisons: [],
  warnings: [],
  run_ids: ["wr_selector"],
  created_at: "2026-08-21T12:00:00Z",
  updated_at: "2026-08-21T12:01:00Z",
}

function renderPage() {
  return render(<ModelEvaluationsPage />)
}

describe("ModelEvaluationsPage", () => {
  beforeEach(() => {
    for (const mock of [
      listModelEvaluations,
      getModelEvaluation,
      getModelEvaluationCorpus,
      getModelEvaluationOptions,
      createModelEvaluation,
      updateModelEvaluation,
      deleteModelEvaluation,
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
      repositories: [],
      code_types: ["hotpath-code", "code", "test", "bench-test"],
      max_files_per_language: 20,
      default_files_per_language: 20,
      max_candidate_models: 8,
    })
  })

  afterEach(() => vi.useRealTimers())

  it("creates a separate repository evaluation with structured scope", async () => {
    const user = userEvent.setup()
    vi.mocked(createModelEvaluation).mockResolvedValue(evaluation)
    renderPage()

    await user.type(await screen.findByLabelText("Repository"), "owner/repo")
    await user.click(
      screen.getByRole("checkbox", { name: "Select candidate model code" }),
    )
    await user.click(
      screen.getByRole("checkbox", { name: "Select candidate model fast" }),
    )
    await user.selectOptions(
      screen.getByLabelText("File selector model"),
      "review",
    )
    await user.selectOptions(
      screen.getByLabelText("Judge and analyzer model"),
      "review",
    )
    await user.type(screen.getByLabelText("Include folders"), "pkg\nweb")
    await user.type(screen.getByLabelText("Ignore folders"), "web/fixtures")
    await user.click(screen.getByRole("button", { name: "Create evaluation" }))

    await waitFor(() => expect(createModelEvaluation).toHaveBeenCalled())
    expect(vi.mocked(createModelEvaluation).mock.calls[0]?.[0]).toMatchObject({
      repository: "owner/repo",
      candidate_models: ["code", "fast"],
      selector_model_alias: "review",
      default_files_per_language: 20,
      focus: {
        include_folders: ["pkg", "web"],
        exclude_folders: ["web/fixtures"],
      },
    })
  })

  it("keeps a newly created evaluation selected after refreshing an existing list", async () => {
    const user = userEvent.setup()
    const created = {
      ...evaluation,
      id: "rme_22222222222222222222222222222222",
      version: 1,
      status: "draft" as const,
      repository: "other/repo",
      progress: {
        ...evaluation.progress,
        stage: "idle",
        languages: {},
        selected_files: 0,
        percent: 0,
      },
    }
    vi.mocked(listModelEvaluations)
      .mockResolvedValueOnce([evaluation])
      .mockResolvedValue([created, evaluation])
    vi.mocked(getModelEvaluation).mockImplementation(async (id) =>
      id === created.id ? created : evaluation,
    )
    vi.mocked(createModelEvaluation).mockResolvedValue(created)
    renderPage()

    expect(await screen.findByDisplayValue("owner/repo")).toBeVisible()
    await user.click(screen.getByRole("button", { name: "New evaluation" }))
    await user.type(screen.getByLabelText("Repository"), "other/repo")
    await user.click(
      screen.getByRole("checkbox", { name: "Select candidate model code" }),
    )
    await user.click(
      screen.getByRole("checkbox", { name: "Select candidate model fast" }),
    )
    await user.selectOptions(
      screen.getByLabelText("File selector model"),
      "review",
    )
    await user.selectOptions(
      screen.getByLabelText("Judge and analyzer model"),
      "review",
    )
    await user.click(screen.getByRole("button", { name: "Create evaluation" }))
    await waitFor(() =>
      expect(screen.getByLabelText("Repository")).toHaveValue("other/repo"),
    )
    expect(
      screen.getAllByText("draft", { exact: true }).length,
    ).toBeGreaterThan(0)
  })

  it("shows per-language corpus controls and starts an untouched ready evaluation", async () => {
    const user = userEvent.setup()
    vi.mocked(listModelEvaluations).mockResolvedValue([evaluation])
    vi.mocked(getModelEvaluation).mockResolvedValue(evaluation)
    vi.mocked(getModelEvaluationCorpus).mockResolvedValue({
      files: [],
      total: 28,
      offset: 0,
      commit_sha: "a".repeat(40),
      inventory_hash: "sha256:inventory",
      language_counts: { go: 20, typescript: 8 },
    })
    vi.mocked(runModelEvaluationAction).mockResolvedValue({
      ...evaluation,
      status: "running",
    })
    renderPage()

    expect(await screen.findByText("Corpus by language")).toBeVisible()
    expect(
      await screen.findByText(
        /Commit a{40} · inventory sha256:inventory · 28 files/,
      ),
    ).toBeVisible()
    const typescriptRow = screen.getByText("typescript").closest("tr")
    expect(typescriptRow).not.toBeNull()
    expect(within(typescriptRow!).getByText("limited")).toBeVisible()
    await user.click(screen.getByRole("button", { name: "Start evaluation" }))
    await waitFor(() =>
      expect(runModelEvaluationAction).toHaveBeenCalledWith(
        evaluation.id,
        "start",
        3,
      ),
    )
  })

  it("preflights a saved draft and explains stale corpus reset after edits", async () => {
    const user = userEvent.setup()
    const draftEvaluation: RepositoryModelEvaluation = {
      ...evaluation,
      status: "draft",
      corpus: undefined,
      progress: {
        ...evaluation.progress,
        stage: "idle",
        languages: {},
        selected_files: 0,
        percent: 0,
      },
    }
    vi.mocked(listModelEvaluations).mockResolvedValue([evaluation])
    vi.mocked(getModelEvaluation).mockResolvedValue(evaluation)
    vi.mocked(updateModelEvaluation).mockResolvedValue({
      ...draftEvaluation,
      version: 4,
      ref: "release",
    })
    vi.mocked(runModelEvaluationAction).mockResolvedValue({
      ...draftEvaluation,
      version: 5,
      ref: "release",
      status: "preflighting",
      progress: {
        ...draftEvaluation.progress,
        stage: "resolving",
        message: "Resolving exact commit.",
        percent: 1,
      },
    })
    renderPage()

    await screen.findByDisplayValue("owner/repo")
    const ref = await screen.findByLabelText("Ref")
    fireEvent.change(ref, { target: { value: "release" } })
    expect(
      screen.getByText(/Save configuration changes to clear the stale corpus/i),
    ).toBeVisible()
    expect(
      screen.getByRole("button", { name: "Start evaluation" }),
    ).toBeDisabled()
    await user.click(screen.getByRole("button", { name: "Save configuration" }))
    expect(await screen.findByText(/stale corpus was cleared/i)).toBeVisible()
    expect(
      screen.queryByRole("button", { name: "Start evaluation" }),
    ).not.toBeInTheDocument()
    await user.click(screen.getByRole("button", { name: "Analyze repository" }))
    await waitFor(() =>
      expect(runModelEvaluationAction).toHaveBeenCalledWith(
        evaluation.id,
        "preflight",
        4,
      ),
    )
  })

  it("polls active progress serially to completion", async () => {
    const running: RepositoryModelEvaluation = {
      ...evaluation,
      status: "running",
      version: 4,
      progress: {
        ...evaluation.progress,
        stage: "candidate_execution",
        message: "Running candidate models.",
        current_model: "code",
        current_path: "pkg/service.go",
        completed_tasks: 2,
        percent: 40,
      },
    }
    const completed: RepositoryModelEvaluation = {
      ...running,
      status: "completed",
      version: 5,
      progress: {
        ...running.progress,
        stage: "completed",
        message: "Evaluation complete.",
        completed_tasks: running.progress.total_tasks,
        percent: 100,
      },
    }
    vi.mocked(listModelEvaluations).mockResolvedValue([running])
    vi.mocked(getModelEvaluation)
      .mockResolvedValueOnce(running)
      .mockResolvedValueOnce(completed)
    renderPage()

    expect(
      await screen.findByRole("progressbar", { name: "Evaluation progress" }),
    ).toHaveAttribute("aria-valuenow", "40")
    expect(screen.getByText(/Model code · File pkg\/service.go/)).toBeVisible()
    expect(
      await screen.findByText("Evaluation complete.", {}, { timeout: 3_000 }),
    ).toBeVisible()
    expect(getModelEvaluation).toHaveBeenCalledTimes(2)
    expect(
      screen.queryByRole("button", { name: "Cancel" }),
    ).not.toBeInTheDocument()
  })

  it("cancels an active evaluation with its exact version", async () => {
    const user = userEvent.setup()
    const running: RepositoryModelEvaluation = {
      ...evaluation,
      status: "running",
      version: 4,
      progress: {
        ...evaluation.progress,
        stage: "candidate_execution",
        message: "Running candidate models.",
        percent: 40,
      },
    }
    vi.mocked(listModelEvaluations).mockResolvedValue([running])
    vi.mocked(getModelEvaluation).mockResolvedValue(running)
    vi.mocked(runModelEvaluationAction).mockResolvedValue({
      ...running,
      status: "canceling",
      version: 5,
    })
    renderPage()

    await user.click(await screen.findByRole("button", { name: "Cancel" }))
    await waitFor(() =>
      expect(runModelEvaluationAction).toHaveBeenCalledWith(
        evaluation.id,
        "cancel",
        4,
      ),
    )
  })

  it("locks navigation and configuration while a mutation is pending", async () => {
    const user = userEvent.setup()
    const draftEvaluation: RepositoryModelEvaluation = {
      ...evaluation,
      status: "draft",
      progress: {
        ...evaluation.progress,
        stage: "idle",
        languages: {},
        selected_files: 0,
        percent: 0,
      },
    }
    let resolveAction!: (value: RepositoryModelEvaluation) => void
    vi.mocked(listModelEvaluations).mockResolvedValue([draftEvaluation])
    vi.mocked(getModelEvaluation).mockResolvedValue(draftEvaluation)
    vi.mocked(runModelEvaluationAction).mockImplementation(
      () =>
        new Promise((resolve) => {
          resolveAction = resolve
        }),
    )
    renderPage()

    await user.click(
      await screen.findByRole("button", { name: "Analyze repository" }),
    )
    expect(
      screen.getByRole("button", { name: "New evaluation" }),
    ).toBeDisabled()
    expect(screen.getByRole("button", { name: /owner\/repo/i })).toBeDisabled()
    expect(screen.getByLabelText("Repository")).toBeDisabled()
    expect(screen.getByLabelText("Production code")).toBeDisabled()
    await act(async () =>
      resolveAction({
        ...draftEvaluation,
        status: "preflighting",
        version: 4,
      }),
    )
    expect(screen.getByRole("button", { name: "New evaluation" })).toBeEnabled()
  })

  it("offers explicit reload after a stale version conflict", async () => {
    const user = userEvent.setup()
    const latest = { ...evaluation, version: 4, ref: "latest-main" }
    vi.mocked(listModelEvaluations).mockResolvedValue([evaluation])
    vi.mocked(getModelEvaluation)
      .mockResolvedValueOnce(evaluation)
      .mockResolvedValueOnce(latest)
    vi.mocked(updateModelEvaluation).mockRejectedValue(
      new ModelEvaluationAPIError(
        409,
        "repository model evaluation changed",
        "stale_repository_model_evaluation",
      ),
    )
    renderPage()

    await screen.findByDisplayValue("owner/repo")
    const ref = await screen.findByLabelText("Ref")
    fireEvent.change(ref, { target: { value: "release" } })
    await user.click(screen.getByRole("button", { name: "Save configuration" }))
    expect(
      await screen.findByRole("button", { name: "Reload latest" }),
    ).toBeVisible()
    expect(ref).toHaveValue("release")
    await user.click(screen.getByRole("button", { name: "Reload latest" }))
    await waitFor(() => expect(ref).toHaveValue("latest-main"))
    expect(
      screen.queryByRole("button", { name: "Reload latest" }),
    ).not.toBeInTheDocument()
  })

  it("bounds per-language overrides and restores the configured default", async () => {
    const user = userEvent.setup()
    vi.mocked(listModelEvaluations).mockResolvedValue([evaluation])
    vi.mocked(getModelEvaluation).mockResolvedValue(evaluation)
    renderPage()

    const goLimit = await screen.findByRole("spinbutton", { name: "go files" })
    fireEvent.change(goLimit, { target: { value: "99" } })
    expect(goLimit).toHaveValue(20)
    fireEvent.change(goLimit, { target: { value: "0" } })
    expect(goLimit).toHaveValue(1)
    expect(
      screen.getByRole("button", { name: "Use default quota for go" }),
    ).toBeVisible()
    await user.click(
      screen.getByRole("button", { name: "Use default quota for go" }),
    )
    expect(goLimit).toHaveValue(20)
    expect(
      screen.getAllByText("default", { exact: true }).length,
    ).toBeGreaterThan(0)
  })

  it("pages safe corpus references and exposes bounded run history", async () => {
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

    expect(await screen.findByText("Corpus preview")).toBeVisible()
    expect(screen.getByText("pkg/first.go")).toBeVisible()
    expect(screen.queryByText(/source content/i)).toBeVisible()
    await user.click(screen.getByRole("button", { name: "Next corpus page" }))
    expect(await screen.findByText("pkg/last.go")).toBeVisible()
    expect(getModelEvaluationCorpus).toHaveBeenLastCalledWith(
      evaluation.id,
      20,
      20,
      expect.any(AbortSignal),
    )
    await user.click(screen.getByText("Run history (1)"))
    expect(screen.getByRole("link", { name: "wr_selector" })).toHaveAttribute(
      "href",
      "/agent/workflows?mode=operate&run=wr_selector",
    )
  })

  it("lets operators repair model aliases removed from the catalog", async () => {
    const user = userEvent.setup()
    const stale: RepositoryModelEvaluation = {
      ...evaluation,
      candidate_models: ["retired", "fast"],
      selector_model_alias: "retired",
      judge_model_alias: "retired",
    }
    vi.mocked(listModelEvaluations).mockResolvedValue([stale])
    vi.mocked(getModelEvaluation).mockResolvedValue(stale)
    vi.mocked(updateModelEvaluation).mockResolvedValue({
      ...evaluation,
      status: "draft",
      version: 4,
      corpus: undefined,
    })
    renderPage()

    expect(
      await screen.findByText(/Replace unavailable selector\/judge models/i),
    ).toBeVisible()
    expect(
      screen.getByRole("button", { name: "Start evaluation" }),
    ).toBeDisabled()
    await user.click(
      screen.getByRole("checkbox", {
        name: "Select candidate model retired",
      }),
    )
    await user.click(
      screen.getByRole("checkbox", { name: "Select candidate model code" }),
    )
    await user.selectOptions(
      screen.getByLabelText("File selector model"),
      "review",
    )
    await user.selectOptions(
      screen.getByLabelText("Judge and analyzer model"),
      "review",
    )
    await user.click(screen.getByRole("button", { name: "Save configuration" }))
    await waitFor(() =>
      expect(updateModelEvaluation).toHaveBeenCalledWith(
        stale.id,
        expect.objectContaining({
          candidate_models: ["fast", "code"],
          selector_model_alias: "review",
          judge_model_alias: "review",
          expected_version: stale.version,
        }),
      ),
    )
  })

  it("renders honest AI-judged comparison rows and deletes terminal work", async () => {
    const user = userEvent.setup()
    const completed: RepositoryModelEvaluation = {
      ...evaluation,
      status: "completed",
      progress: { ...evaluation.progress, stage: "completed", percent: 100 },
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
          unsupported_files: 0,
          usage: { ...evaluation.usage, estimated_cost_usd: 0.42 },
          verdict: "Best evidence-grounded analysis.",
          strengths: ["Strong evidence"],
          limitations: ["Higher latency"],
        },
      ],
    }
    vi.mocked(listModelEvaluations).mockResolvedValue([completed])
    vi.mocked(getModelEvaluation).mockResolvedValue(completed)
    vi.mocked(deleteModelEvaluation).mockResolvedValue()
    renderPage()

    expect(await screen.findByText("AI-judged comparison")).toBeVisible()
    expect(screen.getByRole("button", { name: "Run again" })).toBeVisible()
    expect(screen.getByText("92.5")).toBeVisible()
    expect(screen.getByText("Best evidence-grounded analysis.")).toBeVisible()
    expect(screen.getByText(/Strengths: Strong evidence/)).toBeVisible()
    await user.click(screen.getByRole("button", { name: "Delete" }))
    await waitFor(() =>
      expect(deleteModelEvaluation).toHaveBeenCalledWith(completed.id, 3),
    )
  })

  it("keeps partial and failed comparison evidence honest with unknown cost", async () => {
    const completed: RepositoryModelEvaluation = {
      ...evaluation,
      status: "completed",
      progress: { ...evaluation.progress, stage: "completed", percent: 100 },
      comparisons: [
        {
          model_alias: "code",
          concrete_models: { "gpt-code": 2 },
          completion: "partial",
          failure: "One corpus task timed out.",
          failures: 1,
          rank: 1,
          overall_score: 81,
          scores: { correctness: 82, evidence: 80 },
          languages: ["go", "typescript"],
          regions: ["pkg", "web"],
          files_analyzed: 24,
          bytes_analyzed: 240_000,
          confirmed_findings: 4,
          unsupported_files: 2,
          usage: { ...evaluation.usage, requests: 25 },
          verdict: "Useful but incomplete.",
        },
        {
          model_alias: "fast",
          concrete_models: {},
          completion: "failed",
          failure: "No valid candidate output.",
          failures: 3,
          rank: 0,
          scores: {},
          languages: [],
          regions: [],
          files_analyzed: 0,
          bytes_analyzed: 0,
          confirmed_findings: 0,
          unsupported_files: 0,
          usage: {
            ...evaluation.usage,
            requests: 3,
            estimated_cost_usd: 0,
          },
        },
      ],
    }
    vi.mocked(listModelEvaluations).mockResolvedValue([completed])
    vi.mocked(getModelEvaluation).mockResolvedValue(completed)
    renderPage()

    expect(await screen.findByText("AI-judged comparison")).toBeVisible()
    expect(screen.getByText("partial")).toBeVisible()
    expect(screen.getByText("1 failed task")).toBeVisible()
    expect(
      screen.getByText("Failure: One corpus task timed out."),
    ).toBeVisible()
    expect(
      screen.getByText("Failure: No valid candidate output."),
    ).toBeVisible()
    const partialRow = screen.getByRole("row", { name: /code gpt-code/i })
    expect(within(partialRow).getByText(/unknown/)).toBeVisible()
    const failedRow = screen.getByRole("row", { name: /fast unknown failed/i })
    expect(within(failedRow).getByText(/\$0\.0000/)).toBeVisible()
    expect(
      screen.getByText(/comparative AI judgments, not ground-truth/i),
    ).toBeVisible()
  })

  it("resumes recoverable evaluations with the exact version", async () => {
    const user = userEvent.setup()
    const failed: RepositoryModelEvaluation = {
      ...evaluation,
      status: "failed",
      failure: "Candidate execution failed.",
    }
    vi.mocked(listModelEvaluations).mockResolvedValue([failed])
    vi.mocked(getModelEvaluation).mockResolvedValue(failed)
    vi.mocked(runModelEvaluationAction).mockResolvedValue({
      ...failed,
      id: failed.id,
      status: "preflighting",
      version: 1,
    })
    renderPage()

    expect(await screen.findByRole("button", { name: "Resume" })).toBeVisible()
    expect(
      screen.queryByRole("button", { name: "Run again" }),
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole("button", { name: "Save configuration" }),
    ).not.toBeInTheDocument()
    await user.click(screen.getByRole("button", { name: "Resume" }))
    await waitFor(() =>
      expect(runModelEvaluationAction).toHaveBeenCalledWith(
        failed.id,
        "resume",
        failed.version,
      ),
    )
  })
})
