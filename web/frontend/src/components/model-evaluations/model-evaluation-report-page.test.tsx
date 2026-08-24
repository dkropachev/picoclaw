import { render, screen, waitFor, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import type { ReactNode } from "react"
import { beforeEach, describe, expect, it, vi } from "vitest"

import type {
  EvaluationComparison,
  RepositoryModelEvaluation,
} from "@/api/model-evaluations"
import { getModelEvaluation } from "@/api/model-evaluations"

import {
  buildModelEvaluationReportAnalysis,
  positionModelEvaluationDonutSegments,
} from "./model-evaluation-report-analysis"
import { ModelEvaluationReportPage } from "./model-evaluation-report-page"

vi.mock("@/api/model-evaluations", () => ({
  getModelEvaluation: vi.fn(),
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

const usage = {
  requests: 25,
  input_tokens: 492_551,
  cached_input_tokens: 0,
  output_tokens: 78_421,
  reasoning_tokens: 47_527,
  duration_millis: 1_586_977,
}

function comparison(
  input: Partial<EvaluationComparison> &
    Pick<EvaluationComparison, "model_alias" | "rank" | "overall_score">,
): EvaluationComparison {
  return {
    ...input,
    model_alias: input.model_alias,
    concrete_models: input.concrete_models ?? {
      [`gpt-${input.model_alias}`]: 25,
    },
    completion: input.completion ?? "completed",
    failures: input.failures ?? 0,
    rank: input.rank,
    overall_score: input.overall_score,
    scores: {
      correctness: input.overall_score ?? 0,
      evidence: input.overall_score ?? 0,
      coverage: input.overall_score ?? 0,
      actionability: input.overall_score ?? 0,
      ...input.scores,
    },
    languages: input.languages ?? ["go", "typescript"],
    regions: input.regions ?? ["pkg", "web"],
    files_analyzed: input.files_analyzed ?? 75,
    bytes_analyzed: input.bytes_analyzed ?? 1_558_744,
    confirmed_findings: input.confirmed_findings ?? 10,
    unsupported_claims:
      input.unsupported_claims ?? input.unsupported_files ?? 1,
    unsupported_files: input.unsupported_files ?? 1,
    usage: { ...usage, ...input.usage },
    verdict:
      input.verdict ??
      `${input.model_alias} verdict with concrete decision guidance.`,
    strengths: input.strengths ?? [`${input.model_alias} strength`],
    limitations: input.limitations ?? [`${input.model_alias} limitation`],
  }
}

const completed: RepositoryModelEvaluation = {
  schema_version: 1,
  id: "rme_012d820e0d5cf890740e990be0bc3651",
  version: 315,
  status: "completed",
  repository: "https://github.com/scylladb/seastar.git",
  ref: "HEAD",
  candidate_models: ["review", "review-cheap-2", "review-cheap"],
  selector_model_alias: "review-cheap-2",
  judge_model_alias: "chat",
  focus: { code_types: ["code", "test"] },
  default_files_per_language: 20,
  files_per_language: {},
  progress: {
    stage: "completed",
    languages: {
      cpp: {
        available_files: 532,
        selected_files: 20,
        completed_files: 20,
        selected_bytes: 1_162_634,
        regions: ["src", "tests"],
        limited: false,
      },
      python: {
        available_files: 21,
        selected_files: 20,
        completed_files: 20,
        selected_bytes: 311_389,
        regions: ["scripts", "tests"],
        limited: false,
      },
      yaml: {
        available_files: 35,
        selected_files: 35,
        completed_files: 35,
        selected_bytes: 84_721,
        regions: [".github"],
        limited: true,
      },
    },
    total_files: 588,
    selected_files: 75,
    completed_files: 75,
    total_tasks: 83,
    completed_tasks: 83,
    current_batch: 7,
    total_batches: 7,
    completed_calls: 3,
    total_calls: 3,
    failed_calls: 0,
    active_children: [],
    message: "Repository model evaluation completed.",
    percent: 100,
  },
  usage: {
    requests: 84,
    input_tokens: 2_262_134,
    cached_input_tokens: 119_552,
    output_tokens: 220_209,
    reasoning_tokens: 137_196,
    duration_millis: 4_321_650,
  },
  model_stats: {},
  comparisons: [
    comparison({
      model_alias: "review",
      rank: 1,
      overall_score: 95.52,
      scores: {
        correctness: 95.72,
        evidence: 96.72,
        coverage: 95.52,
        actionability: 92.92,
      },
      confirmed_findings: 150,
      unsupported_files: 1,
      strengths: ["Broad source-grounded review", "Practical remediation"],
      limitations: ["Some impacts need runtime validation"],
    }),
    comparison({
      model_alias: "review-cheap-2",
      rank: 2,
      overall_score: 85.24,
      scores: {
        correctness: 88.4,
        evidence: 91.68,
        coverage: 77.6,
        actionability: 84.72,
      },
      confirmed_findings: 83,
      unsupported_files: 5,
      usage: { ...usage, duration_millis: 968_391, output_tokens: 51_738 },
    }),
    comparison({
      model_alias: "review-cheap",
      rank: 3,
      overall_score: 84.84,
      scores: {
        correctness: 83.36,
        evidence: 88.28,
        coverage: 84.12,
        actionability: 85.76,
      },
      confirmed_findings: 100,
      unsupported_files: 13,
      usage: { ...usage, duration_millis: 1_165_817, output_tokens: 60_938 },
    }),
  ],
  warnings: ["Scores are comparative AI judgments."],
  run_ids: ["wr_selector", "wr_batch", "wr_analysis"],
  created_at: "2026-08-24T12:00:06Z",
  updated_at: "2026-08-24T12:37:15Z",
  started_at: "2026-08-24T12:01:16Z",
  finished_at: "2026-08-24T12:37:15Z",
}

describe("model evaluation report analysis", () => {
  it("positions donut segments cumulatively without overlaying them", () => {
    expect(
      positionModelEvaluationDonutSegments([
        { label: "supported", value: 3 },
        { label: "unsupported", value: 1 },
      ]),
    ).toEqual([
      { label: "supported", value: 3, percent: 75, offset: 0 },
      { label: "unsupported", value: 1, percent: 25, offset: 75 },
    ])
  })

  it("identifies the quality leader, fastest alternative, and dominated tradeoff", () => {
    const analysis = buildModelEvaluationReportAnalysis(completed.comparisons)
    expect(analysis.winner?.model_alias).toBe("review")
    expect(analysis.fastest?.model_alias).toBe("review-cheap-2")
    expect(analysis.qualityGap).toBeCloseTo(10.28)
    expect(analysis.coverageGap).toBeCloseTo(17.92)
    expect(analysis.fastestTimeSaving).toBeCloseTo(38.98, 1)
    expect(analysis.lowerOverallAndSlowerAliases).toEqual(
      new Set(["review-cheap"]),
    )
    expect(analysis.highestSupportedClaimShare?.model_alias).toBe("review")
  })

  it("does not manufacture rankings or percentages from missing data", () => {
    const failed = comparison({
      model_alias: "failed",
      rank: 0,
      overall_score: undefined,
      completion: "failed",
      confirmed_findings: 0,
      unsupported_files: 0,
      usage: { ...usage, duration_millis: 0 },
    })
    const analysis = buildModelEvaluationReportAnalysis([failed])
    expect(analysis.winner).toBeUndefined()
    expect(analysis.fastest).toBeUndefined()
    expect(analysis.fastestTimeSaving).toBeUndefined()

    const partial = comparison({
      model_alias: "partial",
      rank: 1,
      overall_score: 99,
      completion: "partial",
    })
    expect(buildModelEvaluationReportAnalysis([partial]).winner).toBeUndefined()
  })
})

describe("ModelEvaluationReportPage", () => {
  beforeEach(() => {
    vi.mocked(getModelEvaluation).mockReset()
  })

  it("renders a concise visual report with recommendation, graphs, pies, and narrative analysis", async () => {
    vi.mocked(getModelEvaluation).mockResolvedValue(completed)
    render(
      <ModelEvaluationReportPage
        evaluationID={completed.id}
        onBack={vi.fn()}
      />,
    )

    expect(
      await screen.findByRole("heading", {
        name: "Use review when review quality matters.",
      }),
    ).toBeVisible()
    expect(screen.getByText(/10\.3 points ahead/)).toBeVisible()
    expect(screen.getByText(/39% less cumulative model time/)).toBeVisible()
    expect(
      screen.getByRole("heading", { name: "Quality score comparison" }),
    ).toBeVisible()
    expect(screen.getByRole("img", { name: /Efficiency graph/i })).toBeVisible()
    expect(
      screen.getByRole("img", { name: /selected files:.*cpp 20/i }),
    ).toBeVisible()
    expect(
      screen.getByRole("img", {
        name: /review: AI-judge supported claims 150, Unsupported claims 1/i,
      }),
    ).toBeVisible()
    const winnerCard = screen
      .getByRole("heading", { name: /^review$/ })
      .closest("article")
    expect(winnerCard).not.toBeNull()
    expect(
      within(winnerCard as HTMLElement).getByText(
        "Broad source-grounded review",
      ),
    ).toBeVisible()
    expect(
      within(winnerCard as HTMLElement).getByText(
        "Some impacts need runtime validation",
      ),
    ).toBeVisible()
    expect(screen.getByText("Lower overall and slower")).toBeVisible()
    expect(screen.getAllByText("Pricing unavailable").length).toBeGreaterThan(0)
    expect(screen.getByText(/do not receive a ratio/i)).toBeVisible()
  })

  it("keeps unfinished probes honest instead of rendering empty charts", async () => {
    vi.mocked(getModelEvaluation).mockResolvedValue({
      ...completed,
      status: "running",
      comparisons: [],
      progress: { ...completed.progress, percent: 48 },
    })
    render(
      <ModelEvaluationReportPage
        evaluationID={completed.id}
        onBack={vi.fn()}
      />,
    )
    expect(
      await screen.findByRole("heading", { name: "Report is not ready" }),
    ).toBeVisible()
    expect(
      screen.getByRole("progressbar", { name: "Probe progress 48 percent" }),
    ).toHaveAttribute("aria-valuenow", "48")
    expect(
      screen.queryByText("Quality score comparison"),
    ).not.toBeInTheDocument()
  })

  it("surfaces judge overlap and does not turn missing wall time into zero", async () => {
    vi.mocked(getModelEvaluation).mockResolvedValue({
      ...completed,
      judge_model_alias: "review",
      started_at: "invalid",
      finished_at: undefined,
      created_at: "invalid",
      updated_at: "invalid",
    })
    render(
      <ModelEvaluationReportPage
        evaluationID={completed.id}
        onBack={vi.fn()}
      />,
    )

    expect(
      await screen.findByText(/also a candidate alias.*self-judge bias/i),
    ).toBeVisible()
    const wallMetric = screen.getByText("Wall-clock run").parentElement
    expect(wallMetric).not.toBeNull()
    expect(
      within(wallMetric as HTMLElement).getByText("Not reported"),
    ).toBeVisible()
  })

  it("keeps partial and failed model evidence explicit without inventing scores or cost", async () => {
    const user = userEvent.setup()
    const partial = comparison({
      model_alias: "partial-model",
      rank: 1,
      overall_score: 81,
      completion: "partial",
      failure: "One corpus task timed out.",
      failures: 1,
      confirmed_findings: 4,
      unsupported_files: 2,
      usage: { ...usage, estimated_cost_usd: undefined },
    })
    const failed = comparison({
      model_alias: "failed-model",
      rank: 0,
      overall_score: undefined,
      completion: "failed",
      failure: "No valid candidate output.",
      failures: 3,
      scores: {},
      concrete_models: {},
      files_analyzed: 0,
      bytes_analyzed: 0,
      confirmed_findings: 0,
      unsupported_files: 0,
      usage: { ...usage, duration_millis: 0, estimated_cost_usd: undefined },
    })
    vi.mocked(getModelEvaluation).mockResolvedValue({
      ...completed,
      comparisons: [partial, failed],
    })
    render(
      <ModelEvaluationReportPage
        evaluationID={completed.id}
        onBack={vi.fn()}
      />,
    )

    expect(await screen.findByText("One corpus task timed out.")).toBeVisible()
    await user.click(screen.getByRole("button", { name: "View analysis" }))
    expect(screen.getByText("No valid candidate output.")).toBeVisible()
    expect(screen.getByText("partial", { selector: "span" })).toBeVisible()
    expect(screen.getByText("failed", { selector: "span" })).toBeVisible()
    expect(screen.getAllByText("Pricing unavailable").length).toBeGreaterThan(0)
    expect(screen.getByText("Concrete model unavailable")).toBeVisible()
  })

  it("shows a recoverable loading error", async () => {
    const user = userEvent.setup()
    vi.mocked(getModelEvaluation)
      .mockRejectedValueOnce(new Error("Report unavailable"))
      .mockResolvedValueOnce(completed)
    render(
      <ModelEvaluationReportPage
        evaluationID={completed.id}
        onBack={vi.fn()}
      />,
    )
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Report unavailable",
    )
    await user.click(screen.getByRole("button", { name: "Try again" }))
    await waitFor(() =>
      expect(
        screen.getByRole("heading", {
          name: "Use review when review quality matters.",
        }),
      ).toBeVisible(),
    )
  })
})
