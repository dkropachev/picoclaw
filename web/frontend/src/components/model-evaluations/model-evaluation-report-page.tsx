import {
  IconAlertTriangle,
  IconArrowLeft,
  IconChartBar,
  IconChartDots,
  IconChecks,
  IconChevronDown,
  IconClock,
  IconRefresh,
  IconReportAnalytics,
  IconShieldCheck,
  IconTargetArrow,
  IconTrophy,
} from "@tabler/icons-react"
import { useCallback, useEffect, useMemo, useState } from "react"

import {
  type EvaluationComparison,
  type RepositoryModelEvaluation,
  getModelEvaluation,
} from "@/api/model-evaluations"
import { PageHeader } from "@/components/page-header"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { cn } from "@/lib/utils"

import {
  type ModelEvaluationReportAnalysis,
  buildModelEvaluationReportAnalysis,
  isFiniteModelEvaluationNumber,
  modelEvaluationComparisonScore,
  modelEvaluationSupportedClaimRate,
  modelEvaluationUnsupportedClaims,
  positionModelEvaluationDonutSegments,
} from "./model-evaluation-report-analysis"

const chartColors = [
  "#0284c7",
  "#7c3aed",
  "#d97706",
  "#059669",
  "#e11d48",
  "#0891b2",
  "#4f46e5",
  "#65a30d",
]

const scoreDimensions = [
  { key: "overall", label: "Overall" },
  { key: "correctness", label: "Correctness" },
  { key: "evidence", label: "Evidence" },
  { key: "coverage", label: "Coverage" },
  { key: "actionability", label: "Actionability" },
] as const

function formatNumber(value: number): string {
  return new Intl.NumberFormat(undefined, { maximumFractionDigits: 0 }).format(
    value,
  )
}

function formatBytes(value: number): string {
  if (value < 1024) return `${value} B`
  if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KiB`
  return `${(value / (1024 * 1024)).toFixed(1)} MiB`
}

function formatDuration(value: number): string {
  if (value < 1000) return `${Math.max(0, value)} ms`
  const totalSeconds = Math.round(value / 1000)
  if (totalSeconds < 60) return `${totalSeconds}s`
  const minutes = Math.floor(totalSeconds / 60)
  const seconds = totalSeconds % 60
  return `${minutes}m ${seconds}s`
}

function formatTimestamp(value?: string): string {
  if (!value) return "Not reported"
  const timestamp = new Date(value)
  if (Number.isNaN(timestamp.getTime())) return "Not reported"
  return timestamp.toLocaleString()
}

function wallDuration(
  evaluation: RepositoryModelEvaluation,
): number | undefined {
  const started = new Date(
    evaluation.started_at ?? evaluation.created_at,
  ).getTime()
  const finished = new Date(
    evaluation.finished_at ?? evaluation.updated_at,
  ).getTime()
  if (!Number.isFinite(started) || !Number.isFinite(finished)) return undefined
  return Math.max(0, finished - started)
}

function concreteModels(comparison: EvaluationComparison): string {
  const entries = Object.entries(comparison.concrete_models)
  return entries.length > 0
    ? entries.map(([model, count]) => `${model} × ${count}`).join(", ")
    : "Concrete model unavailable"
}

function totalTokens(comparison: EvaluationComparison): number {
  return comparison.usage.input_tokens + comparison.usage.output_tokens
}

function SectionHeading({
  icon: Icon,
  title,
  description,
  titleID,
}: {
  icon: typeof IconChartBar
  title: string
  description: string
  titleID?: string
}) {
  return (
    <div className="flex items-start gap-3">
      <span className="bg-muted mt-0.5 rounded-lg p-2" aria-hidden="true">
        <Icon className="size-5" />
      </span>
      <div>
        <h2 id={titleID} className="text-base font-semibold">
          {title}
        </h2>
        <p className="text-muted-foreground mt-0.5 text-sm">{description}</p>
      </div>
    </div>
  )
}

function MetricCard({
  label,
  value,
  detail,
}: {
  label: string
  value: string
  detail: string
}) {
  return (
    <div className="border-border bg-card rounded-xl border p-4">
      <p className="text-muted-foreground text-xs font-medium tracking-wide uppercase">
        {label}
      </p>
      <p className="mt-2 text-2xl font-semibold tabular-nums">{value}</p>
      <p className="text-muted-foreground mt-1 text-xs">{detail}</p>
    </div>
  )
}

function ScoreComparisonGraph({
  comparisons,
}: {
  comparisons: EvaluationComparison[]
}) {
  return (
    <figure aria-labelledby="quality-score-title" className="space-y-5">
      <figcaption id="quality-score-title" className="sr-only">
        AI-judged quality scores from zero to one hundred for each model
      </figcaption>
      <div className="flex flex-wrap gap-4 text-xs">
        {comparisons.map((comparison, index) => (
          <span
            key={comparison.model_alias}
            className="flex items-center gap-2"
          >
            {/* ui-rule-allow dynamic-style: model colors stay consistent across report charts. */}
            <span
              className="size-2.5 rounded-full"
              style={{
                backgroundColor: chartColors[index % chartColors.length],
              }}
              aria-hidden="true"
            />
            {comparison.model_alias}
          </span>
        ))}
      </div>
      <div className="space-y-5">
        {scoreDimensions.map((dimension) => (
          <div key={dimension.key}>
            <div className="mb-2 flex items-center justify-between text-sm">
              <h3 className="font-medium">{dimension.label}</h3>
              <span className="text-muted-foreground text-xs">0–100</span>
            </div>
            <div className="space-y-2">
              {comparisons.map((comparison, index) => {
                const value = modelEvaluationComparisonScore(
                  comparison,
                  dimension.key,
                )
                return (
                  <div
                    key={comparison.model_alias}
                    className="grid grid-cols-[minmax(6rem,9rem)_1fr_3rem] items-center gap-3"
                  >
                    <span
                      className="truncate text-xs"
                      title={comparison.model_alias}
                    >
                      {comparison.model_alias}
                    </span>
                    <div className="bg-muted h-2.5 overflow-hidden rounded-full">
                      {value == null ? (
                        <span className="text-muted-foreground block px-2 text-[10px] leading-2.5">
                          not scored
                        </span>
                      ) : (
                        /* ui-rule-allow dynamic-style: bar width and model color encode exact runtime scores. */
                        <span
                          className="block h-full rounded-full"
                          style={{
                            backgroundColor:
                              chartColors[index % chartColors.length],
                            width: `${value}%`,
                          }}
                          aria-hidden="true"
                        />
                      )}
                    </div>
                    <span className="text-right text-xs font-medium tabular-nums">
                      {value == null ? "—" : value.toFixed(1)}
                    </span>
                  </div>
                )
              })}
            </div>
          </div>
        ))}
      </div>
    </figure>
  )
}

function EfficiencyScatterPlot({
  comparisons,
}: {
  comparisons: EvaluationComparison[]
}) {
  const points = comparisons
    .map((comparison, colorIndex) => ({ comparison, colorIndex }))
    .filter(
      ({ comparison }) =>
        isFiniteModelEvaluationNumber(comparison.overall_score) &&
        comparison.usage.duration_millis > 0,
    )
  if (points.length === 0) {
    return (
      <p className="text-muted-foreground text-sm">No scored timing data.</p>
    )
  }
  const width = 680
  const height = 320
  const padding = { left: 58, right: 26, top: 24, bottom: 52 }
  const plotWidth = width - padding.left - padding.right
  const plotHeight = height - padding.top - padding.bottom
  const maxMinutes = Math.max(
    1,
    ...points.map(
      ({ comparison }) => comparison.usage.duration_millis / 60_000,
    ),
  )
  const minimumScore = Math.min(
    ...points.map(({ comparison }) => comparison.overall_score ?? 0),
  )
  const yMinimum = Math.max(0, Math.floor((minimumScore - 5) / 5) * 5)
  const yMaximum = 100
  const yRange = Math.max(1, yMaximum - yMinimum)
  const x = (minutes: number) =>
    padding.left + (minutes / (maxMinutes * 1.1)) * plotWidth
  const y = (score: number) =>
    padding.top + ((yMaximum - score) / yRange) * plotHeight

  return (
    <figure aria-labelledby="efficiency-title" className="space-y-3">
      <figcaption id="efficiency-title" className="sr-only">
        Overall quality compared with cumulative model time
      </figcaption>
      <svg
        viewBox={`0 0 ${width} ${height}`}
        className="hidden h-auto w-full min-w-0 sm:block"
        role="img"
        aria-label="Efficiency graph: cumulative model time in minutes on the horizontal axis and AI-judged overall score on the vertical axis"
      >
        {[0, 0.25, 0.5, 0.75, 1].map((fraction) => {
          const score = yMinimum + (yMaximum - yMinimum) * fraction
          const lineY = y(score)
          return (
            <g key={fraction}>
              <line
                x1={padding.left}
                x2={width - padding.right}
                y1={lineY}
                y2={lineY}
                stroke="var(--border)"
                strokeWidth="1"
              />
              <text
                x={padding.left - 10}
                y={lineY + 4}
                textAnchor="end"
                fontSize="11"
                fill="var(--muted-foreground)"
              >
                {score.toFixed(0)}
              </text>
            </g>
          )
        })}
        <line
          x1={padding.left}
          x2={padding.left}
          y1={padding.top}
          y2={height - padding.bottom}
          stroke="var(--foreground)"
          strokeWidth="1.5"
        />
        <line
          x1={padding.left}
          x2={width - padding.right}
          y1={height - padding.bottom}
          y2={height - padding.bottom}
          stroke="var(--foreground)"
          strokeWidth="1.5"
        />
        <text
          x={width / 2}
          y={height - 12}
          textAnchor="middle"
          fontSize="12"
          fill="var(--muted-foreground)"
        >
          Cumulative model time (minutes)
        </text>
        <text
          x="14"
          y={height / 2}
          textAnchor="middle"
          fontSize="12"
          fill="var(--muted-foreground)"
          transform={`rotate(-90 14 ${height / 2})`}
        >
          Overall score
        </text>
        {points.map(({ comparison, colorIndex }, pointIndex) => {
          const minutes = comparison.usage.duration_millis / 60_000
          const score = comparison.overall_score ?? 0
          const pointX = x(minutes)
          const pointY = y(score)
          const labelOnLeft = pointX > width - 170
          const labelX = pointX + (labelOnLeft ? -12 : 12)
          const labelOffset = (pointIndex % 3) * 13 - 13
          return (
            <g key={comparison.model_alias}>
              <circle
                cx={pointX}
                cy={pointY}
                r="8"
                fill={chartColors[colorIndex % chartColors.length]}
                stroke="var(--background)"
                strokeWidth="3"
              >
                <title>
                  {comparison.model_alias}: {score.toFixed(1)} overall,{" "}
                  {minutes.toFixed(1)} cumulative model minutes
                </title>
              </circle>
              <text
                x={labelX}
                y={pointY - 10 + labelOffset}
                textAnchor={labelOnLeft ? "end" : "start"}
                fontSize="11"
                fontWeight="600"
                fill="var(--foreground)"
              >
                {comparison.model_alias}
              </text>
              <text
                x={labelX}
                y={pointY + 5 + labelOffset}
                textAnchor={labelOnLeft ? "end" : "start"}
                fontSize="10"
                fill="var(--muted-foreground)"
              >
                {score.toFixed(1)} · {minutes.toFixed(1)}m
              </text>
            </g>
          )
        })}
      </svg>
      <div className="space-y-3 sm:hidden">
        {points.map(({ comparison, colorIndex }) => {
          const minutes = comparison.usage.duration_millis / 60_000
          const score = comparison.overall_score ?? 0
          return (
            <div
              key={comparison.model_alias}
              className="border-border rounded-xl border p-3"
            >
              <div className="flex items-center justify-between gap-3 text-sm">
                <strong className="font-mono">{comparison.model_alias}</strong>
                <span className="tabular-nums">
                  {score.toFixed(1)} · {minutes.toFixed(1)}m
                </span>
              </div>
              <div className="mt-3 grid grid-cols-[3rem_1fr] items-center gap-2 text-[11px]">
                <span>Quality</span>
                <div className="bg-muted h-2 overflow-hidden rounded-full">
                  {/* ui-rule-allow dynamic-style: mobile bar width and color encode the exact score. */}
                  <span
                    className="block h-full rounded-full"
                    style={{
                      backgroundColor:
                        chartColors[colorIndex % chartColors.length],
                      width: `${score}%`,
                    }}
                    aria-hidden="true"
                  />
                </div>
                <span>Time</span>
                <div className="bg-muted h-2 overflow-hidden rounded-full">
                  {/* ui-rule-allow dynamic-style: mobile bar width compares cumulative model time. */}
                  <span
                    className="block h-full rounded-full opacity-60"
                    style={{
                      backgroundColor:
                        chartColors[colorIndex % chartColors.length],
                      width: `${(minutes / maxMinutes) * 100}%`,
                    }}
                    aria-hidden="true"
                  />
                </div>
              </div>
            </div>
          )
        })}
      </div>
      <ul className="hidden gap-2 text-xs sm:grid sm:grid-cols-2 xl:grid-cols-3">
        {points.map(({ comparison }) => (
          <li
            key={comparison.model_alias}
            className="bg-muted/60 rounded-lg p-2"
          >
            <strong>{comparison.model_alias}</strong>:{" "}
            {comparison.overall_score?.toFixed(1)} overall in{" "}
            {formatDuration(comparison.usage.duration_millis)} cumulative model
            time
          </li>
        ))}
      </ul>
    </figure>
  )
}

interface DonutSegment {
  label: string
  value: number
  color: string
}

function DonutChart({
  title,
  center,
  segments,
}: {
  title: string
  center: string
  segments: DonutSegment[]
}) {
  const positioned = positionModelEvaluationDonutSegments(segments)
  return (
    <figure className="flex flex-col items-center gap-3">
      <svg
        viewBox="0 0 120 120"
        className="size-36"
        role="img"
        aria-label={`${title}: ${positioned.map((segment) => `${segment.label} ${segment.value}`).join(", ") || "no data"}`}
      >
        <circle
          cx="60"
          cy="60"
          r="43"
          fill="none"
          stroke="var(--muted)"
          strokeWidth="15"
        />
        {positioned.map((segment) => (
          <circle
            key={segment.label}
            cx="60"
            cy="60"
            r="43"
            fill="none"
            stroke={segment.color}
            strokeWidth="15"
            pathLength="100"
            strokeDasharray={`${segment.percent} ${100 - segment.percent}`}
            strokeDashoffset={-segment.offset}
            transform="rotate(-90 60 60)"
          >
            <title>
              {segment.label}: {segment.value} ({segment.percent.toFixed(1)}%)
            </title>
          </circle>
        ))}
        <text
          x="60"
          y="57"
          textAnchor="middle"
          fontSize="16"
          fontWeight="700"
          fill="var(--foreground)"
        >
          {center}
        </text>
        <text
          x="60"
          y="73"
          textAnchor="middle"
          fontSize="8"
          fill="var(--muted-foreground)"
        >
          {title}
        </text>
      </svg>
      <ul className="w-full space-y-1 text-xs">
        {positioned.map((segment) => (
          <li
            key={segment.label}
            className="flex items-center justify-between gap-3"
          >
            <span className="flex min-w-0 items-center gap-2">
              {/* ui-rule-allow dynamic-style: legend swatch matches its runtime donut segment. */}
              <span
                className="size-2.5 shrink-0 rounded-full"
                style={{ backgroundColor: segment.color }}
                aria-hidden="true"
              />
              <span className="truncate">{segment.label}</span>
            </span>
            <span className="font-medium tabular-nums">{segment.value}</span>
          </li>
        ))}
      </ul>
    </figure>
  )
}

function corpusSegments(evaluation: RepositoryModelEvaluation): DonutSegment[] {
  const languages = Object.entries(evaluation.progress.languages)
    .map(([language, progress]) => ({
      label: language,
      value: progress.selected_files,
    }))
    .filter((item) => item.value > 0)
    .sort((left, right) => right.value - left.value)
  const visible = languages.slice(0, 4)
  const other = languages.slice(4).reduce((sum, item) => sum + item.value, 0)
  return [
    ...visible.map((item, index) => ({
      ...item,
      color: chartColors[index % chartColors.length],
    })),
    ...(other > 0
      ? [{ label: "Other languages", value: other, color: chartColors[4] }]
      : []),
  ]
}

function Recommendation({
  analysis,
}: {
  analysis: ModelEvaluationReportAnalysis
}) {
  const { winner, runnerUp, fastest, highestSupportedClaimShare } = analysis
  if (!winner) {
    return (
      <section className="border-border bg-card rounded-2xl border p-5">
        <h2 className="font-semibold">No recommendation available</h2>
        <p className="text-muted-foreground mt-2 text-sm">
          The probe completed without a scored model comparison.
        </p>
      </section>
    )
  }
  const winnerUnsupported = modelEvaluationUnsupportedClaims(winner)
  return (
    <section
      aria-labelledby="executive-recommendation"
      className="via-background overflow-hidden rounded-2xl border border-sky-200 bg-gradient-to-br from-sky-50 to-violet-50 p-5 md:p-7 dark:border-sky-900 dark:from-sky-950/40 dark:to-violet-950/30"
    >
      <div className="flex flex-col gap-5 lg:flex-row lg:items-start lg:justify-between">
        <div className="max-w-3xl">
          <div className="flex items-center gap-2 text-sky-700 dark:text-sky-300">
            <IconTrophy className="size-5" aria-hidden="true" />
            <p className="text-xs font-semibold tracking-wide uppercase">
              Executive recommendation
            </p>
          </div>
          <h2
            id="executive-recommendation"
            className="mt-3 text-2xl font-semibold md:text-3xl"
          >
            Use {winner.model_alias} when review quality matters.
          </h2>
          <p className="text-muted-foreground mt-3 text-sm leading-6 md:text-base">
            It ranked first at {winner.overall_score?.toFixed(1)} overall
            {runnerUp && analysis.qualityGap != null
              ? `, ${analysis.qualityGap.toFixed(1)} points ahead of ${runnerUp.model_alias}`
              : ""}
            . Its AI-judged actionability score is{" "}
            {modelEvaluationComparisonScore(winner, "actionability")?.toFixed(
              1,
            ) ?? "not reported"}
            , with {winner.confirmed_findings} supported claims
            {winnerUnsupported == null
              ? ". The unsupported-claim count was not recorded for this legacy result."
              : ` and ${winnerUnsupported} unsupported.`}
          </p>
          {fastest && fastest.model_alias !== winner.model_alias && (
            <p className="mt-3 text-sm leading-6">
              <strong>Time-constrained alternative:</strong>{" "}
              {fastest.model_alias} used{" "}
              {analysis.fastestTimeSaving?.toFixed(0) ?? "less"}% less
              cumulative model time
              {analysis.fastestQualityGap != null
                ? `, but scored ${Math.abs(analysis.fastestQualityGap).toFixed(1)} points ${analysis.fastestQualityGap >= 0 ? "lower" : "higher"} overall`
                : ""}
              {analysis.coverageGap != null
                ? ` and ${Math.abs(analysis.coverageGap).toFixed(1)} points ${analysis.coverageGap >= 0 ? "lower" : "higher"} on coverage`
                : ""}
              .
            </p>
          )}
        </div>
        <div className="border-border/70 bg-background/80 min-w-60 rounded-xl border p-4 backdrop-blur-sm">
          <p className="text-muted-foreground text-xs">Quality leader</p>
          <p className="mt-1 font-mono text-lg font-semibold">
            {winner.model_alias}
          </p>
          <p className="text-muted-foreground mt-1 text-xs">
            {concreteModels(winner)}
          </p>
          <dl className="mt-4 grid grid-cols-2 gap-3 text-sm">
            <div>
              <dt className="text-muted-foreground text-xs">Overall</dt>
              <dd className="font-semibold tabular-nums">
                {winner.overall_score?.toFixed(1) ?? "—"}
              </dd>
            </div>
            <div>
              <dt className="text-muted-foreground text-xs">
                Supported-claim share
              </dt>
              <dd className="font-semibold tabular-nums">
                {modelEvaluationSupportedClaimRate(winner)?.toFixed(1) ?? "—"}%
              </dd>
            </div>
          </dl>
        </div>
      </div>
      <div className="mt-5 grid gap-3 sm:grid-cols-2">
        <DecisionChip
          label="Fastest measured alternative"
          value={fastest?.model_alias ?? "Not available"}
          detail={
            fastest
              ? `${formatDuration(fastest.usage.duration_millis)} cumulative`
              : "Timing unavailable"
          }
        />
        <DecisionChip
          label="Highest supported-claim share"
          value={highestSupportedClaimShare?.model_alias ?? "Not available"}
          detail={
            highestSupportedClaimShare
              ? `${modelEvaluationSupportedClaimRate(highestSupportedClaimShare)?.toFixed(1)}% of assessed claims`
              : "Claim assessment unavailable"
          }
        />
      </div>
      <p className="text-muted-foreground mt-4 text-xs">
        Recommendation uses this probe’s immutable corpus and AI-judged scores.
        It is model-selection guidance, not a ground-truth defect benchmark.
      </p>
    </section>
  )
}

function DecisionChip({
  label,
  value,
  detail,
}: {
  label: string
  value: string
  detail: string
}) {
  return (
    <div className="border-border/70 bg-background/75 rounded-xl border p-3">
      <p className="text-muted-foreground text-xs">{label}</p>
      <p className="mt-1 truncate font-medium">{value}</p>
      <p className="text-muted-foreground text-xs">{detail}</p>
    </div>
  )
}

function ModelAnalysisCard({
  comparison,
  color,
  lowerOverallAndSlower,
}: {
  comparison: EvaluationComparison
  color: string
  lowerOverallAndSlower: boolean
}) {
  const cost = comparison.usage.estimated_cost_usd
  const unsupportedClaims = modelEvaluationUnsupportedClaims(comparison)
  const [expanded, setExpanded] = useState(comparison.rank === 1)
  const detailID = `model-analysis-${comparison.model_alias.replace(/[^a-zA-Z0-9_-]/g, "-")}`
  return (
    <article className="border-border bg-card overflow-hidden rounded-2xl border">
      <header className="border-border flex flex-wrap items-start justify-between gap-4 border-b p-5">
        <div className="flex items-start gap-3">
          {/* ui-rule-allow dynamic-style: model marker matches its chart series. */}
          <span
            className="mt-1 size-3 rounded-full"
            style={{ backgroundColor: color }}
            aria-hidden="true"
          />
          <div>
            <p className="text-muted-foreground text-xs font-medium tracking-wide uppercase">
              Rank {comparison.rank > 0 ? comparison.rank : "—"}
            </p>
            <h3 className="mt-1 font-mono text-xl font-semibold">
              {comparison.model_alias}
            </h3>
            <p className="text-muted-foreground mt-1 text-xs">
              {concreteModels(comparison)}
            </p>
          </div>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          {lowerOverallAndSlower && (
            <Badge variant="secondary">Lower overall and slower</Badge>
          )}
          <Badge
            variant={
              comparison.completion === "failed" ? "destructive" : "outline"
            }
          >
            {comparison.completion}
          </Badge>
          <span className="text-2xl font-semibold tabular-nums">
            {comparison.overall_score?.toFixed(1) ?? "—"}
          </span>
          <Button
            type="button"
            size="sm"
            variant="ghost"
            aria-expanded={expanded}
            aria-controls={detailID}
            onClick={() => setExpanded((value) => !value)}
          >
            {expanded ? "Hide analysis" : "View analysis"}
            <IconChevronDown
              className={cn("transition-transform", expanded && "rotate-180")}
              aria-hidden="true"
            />
          </Button>
        </div>
      </header>
      {expanded && (
        <div id={detailID} className="space-y-5 p-5">
          {comparison.failure && (
            <p
              role="note"
              className="border-destructive/30 bg-destructive/5 text-destructive rounded-lg border p-3 text-sm"
            >
              {comparison.failure}
            </p>
          )}
          <p className="text-sm leading-6">
            {comparison.verdict ||
              comparison.summary ||
              "No AI-judged verdict was returned."}
          </p>
          <div className="grid gap-4 lg:grid-cols-2">
            <div className="rounded-xl border border-emerald-200 bg-emerald-50/60 p-4 dark:border-emerald-900 dark:bg-emerald-950/20">
              <h4 className="flex items-center gap-2 text-sm font-semibold text-emerald-800 dark:text-emerald-300">
                <IconChecks className="size-4" aria-hidden="true" /> Strengths
              </h4>
              {(comparison.strengths ?? []).length > 0 ? (
                <ul className="mt-3 list-disc space-y-2 pl-5 text-sm leading-5">
                  {comparison.strengths?.map((strength) => (
                    <li key={strength}>{strength}</li>
                  ))}
                </ul>
              ) : (
                <p className="text-muted-foreground mt-3 text-sm">
                  No strengths reported.
                </p>
              )}
            </div>
            <div className="rounded-xl border border-amber-200 bg-amber-50/60 p-4 dark:border-amber-900 dark:bg-amber-950/20">
              <h4 className="flex items-center gap-2 text-sm font-semibold text-amber-800 dark:text-amber-300">
                <IconAlertTriangle className="size-4" aria-hidden="true" />{" "}
                Limitations
              </h4>
              {(comparison.limitations ?? []).length > 0 ? (
                <ul className="mt-3 list-disc space-y-2 pl-5 text-sm leading-5">
                  {comparison.limitations?.map((limitation) => (
                    <li key={limitation}>{limitation}</li>
                  ))}
                </ul>
              ) : (
                <p className="text-muted-foreground mt-3 text-sm">
                  No limitations reported.
                </p>
              )}
            </div>
          </div>
          <dl className="grid gap-3 text-sm sm:grid-cols-2 lg:grid-cols-4">
            <Definition
              label="Scope"
              value={`${comparison.files_analyzed} files · ${formatBytes(comparison.bytes_analyzed)}`}
            />
            <Definition
              label="AI-judge claims"
              value={
                unsupportedClaims == null
                  ? `${comparison.confirmed_findings} supported · unsupported not recorded`
                  : `${comparison.confirmed_findings} supported · ${unsupportedClaims} unsupported`
              }
            />
            <Definition
              label="Cumulative model time"
              value={formatDuration(comparison.usage.duration_millis)}
            />
            <Definition
              label="Requests and tokens"
              value={`${comparison.usage.requests} requests · ${formatNumber(totalTokens(comparison))} input + output`}
            />
            <Definition
              label="Failed candidate calls"
              value={formatNumber(comparison.failures)}
            />
            <Definition
              label="Cached input"
              value={formatNumber(comparison.usage.cached_input_tokens)}
            />
            <Definition
              label="Reasoning tokens"
              value={formatNumber(comparison.usage.reasoning_tokens)}
            />
            <Definition
              label="Regions / languages"
              value={`${comparison.regions.length} / ${comparison.languages.length}`}
            />
            <Definition
              label="Estimated cost"
              value={
                cost == null ? "Pricing unavailable" : `$${cost.toFixed(4)}`
              }
            />
          </dl>
        </div>
      )}
    </article>
  )
}

function Definition({ label, value }: { label: string; value: string }) {
  return (
    <div className="bg-muted/50 rounded-lg p-3">
      <dt className="text-muted-foreground text-xs">{label}</dt>
      <dd className="mt-1 font-medium tabular-nums">{value}</dd>
    </div>
  )
}

function ReportBody({ evaluation }: { evaluation: RepositoryModelEvaluation }) {
  const analysis = useMemo(
    () => buildModelEvaluationReportAnalysis(evaluation.comparisons),
    [evaluation.comparisons],
  )
  const corpus = corpusSegments(evaluation)
  const selectedFiles = evaluation.progress.selected_files
  const eligibleFiles = evaluation.progress.total_files
  const bytes = Object.values(evaluation.progress.languages).reduce(
    (sum, language) => sum + language.selected_bytes,
    0,
  )
  const totalRegions = new Set(
    evaluation.comparisons.flatMap((comparison) => comparison.regions),
  ).size
  const completeModels = evaluation.comparisons.filter(
    (comparison) => comparison.completion === "completed",
  ).length
  const hasKnownCost = evaluation.comparisons.some(
    (comparison) => comparison.usage.estimated_cost_usd != null,
  )
  const judgeOverlap = evaluation.candidate_models.includes(
    evaluation.judge_model_alias,
  )
  const commit = evaluation.corpus?.commit_sha
  const elapsed = wallDuration(evaluation)

  return (
    <div className="space-y-6">
      <Recommendation analysis={analysis} />

      <section
        aria-label="Report summary"
        className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4"
      >
        <MetricCard
          label="Model completion"
          value={`${completeModels}/${evaluation.comparisons.length}`}
          detail={`${evaluation.progress.completed_files}/${selectedFiles} files completed`}
        />
        <MetricCard
          label="Corpus sample"
          value={`${selectedFiles}/${eligibleFiles}`}
          detail={`${formatBytes(bytes)} across ${Object.keys(evaluation.progress.languages).length} languages`}
        />
        <MetricCard
          label="Wall-clock run"
          value={elapsed == null ? "Not reported" : formatDuration(elapsed)}
          detail={`Completed ${formatTimestamp(evaluation.finished_at ?? evaluation.updated_at)}`}
        />
        <MetricCard
          label="Provider usage"
          value={`${evaluation.usage.requests} requests`}
          detail={`${formatNumber(evaluation.usage.input_tokens + evaluation.usage.output_tokens)} input + output tokens`}
        />
      </section>

      <aside
        role="note"
        className="border-border bg-muted/40 grid gap-3 rounded-xl border p-4 text-sm md:grid-cols-2 xl:grid-cols-3"
      >
        <p>
          <strong>AI judged:</strong> scores are comparative judgments, not
          ground truth.
        </p>
        <p>
          <strong>Sampling:</strong> {selectedFiles} of {eligibleFiles} eligible
          files, {totalRegions} regions.
        </p>
        <p>
          <strong>Timing:</strong> per-model duration is cumulative provider
          time; calls ran in parallel.
        </p>
        <p>
          <strong>Cost:</strong>{" "}
          {hasKnownCost
            ? "known model prices are included."
            : "pricing metadata was unavailable; zero was not assumed."}
        </p>
        <p className={judgeOverlap ? "text-amber-700 dark:text-amber-300" : ""}>
          <strong>Judge:</strong> {evaluation.judge_model_alias}
          {judgeOverlap
            ? " is also a candidate alias; interpret the ranking with self-judge bias in mind."
            : " is not one of the candidate aliases."}
        </p>
      </aside>

      <div className="grid gap-6 xl:grid-cols-2">
        <section className="border-border bg-card rounded-2xl border p-5 md:p-6">
          <SectionHeading
            icon={IconChartBar}
            title="Quality score comparison"
            description="Exact AI-judged scores on a fixed 0–100 scale."
          />
          <div className="mt-6">
            <ScoreComparisonGraph comparisons={analysis.ranked} />
          </div>
        </section>
        <section className="border-border bg-card rounded-2xl border p-5 md:p-6">
          <SectionHeading
            icon={IconChartDots}
            title="Quality versus model time"
            description="Higher is better; farther left used less cumulative model time."
          />
          <div className="mt-4">
            <EfficiencyScatterPlot comparisons={analysis.ranked} />
          </div>
        </section>
      </div>

      <section className="border-border bg-card rounded-2xl border p-5 md:p-6">
        <SectionHeading
          icon={IconShieldCheck}
          title="Corpus and claim assessment"
          description="Corpus composition and AI-judge supported versus unsupported claim counts."
        />
        <div className="mt-6 grid gap-8 sm:grid-cols-2 lg:grid-cols-4">
          <DonutChart
            title="selected files"
            center={String(selectedFiles)}
            segments={corpus}
          />
          {analysis.ranked.map((comparison, index) => {
            const unsupported = modelEvaluationUnsupportedClaims(comparison)
            if (unsupported == null) {
              return (
                <div
                  key={comparison.model_alias}
                  className="bg-muted/40 flex min-h-52 flex-col items-center justify-center rounded-xl border p-4 text-center"
                >
                  <p className="font-mono font-medium">
                    {comparison.model_alias}
                  </p>
                  <p className="mt-3 text-2xl font-semibold tabular-nums">
                    {comparison.confirmed_findings}
                  </p>
                  <p className="text-muted-foreground mt-1 text-xs">
                    AI-judge supported claims
                  </p>
                  <p className="text-muted-foreground mt-3 text-xs">
                    Unsupported-claim count was not recorded for this legacy
                    result.
                  </p>
                </div>
              )
            }
            const supportedRate = modelEvaluationSupportedClaimRate(comparison)
            if (supportedRate == null) {
              return (
                <div
                  key={comparison.model_alias}
                  className="bg-muted/40 flex min-h-52 flex-col items-center justify-center rounded-xl border p-4 text-center"
                >
                  <p className="font-mono font-medium">
                    {comparison.model_alias}
                  </p>
                  <p className="mt-3 text-2xl font-semibold">—</p>
                  <p className="text-muted-foreground mt-1 text-xs">
                    No assessed claims; no ratio calculated.
                  </p>
                </div>
              )
            }
            return (
              <DonutChart
                key={comparison.model_alias}
                title={comparison.model_alias}
                center={`${supportedRate.toFixed(0)}%`}
                segments={[
                  {
                    label: "AI-judge supported claims",
                    value: Math.max(0, comparison.confirmed_findings),
                    color: chartColors[index % chartColors.length],
                  },
                  {
                    label: "Unsupported claims",
                    value: unsupported,
                    color: "var(--destructive)",
                  },
                ]}
              />
            )
          })}
        </div>
        <p className="text-muted-foreground mt-5 text-xs">
          “Supported” and “unsupported” are judge classifications of claims, not
          measured defect accuracy. Legacy results without an exact unsupported
          claim count do not receive a ratio.
        </p>
      </section>

      <section aria-labelledby="model-analysis-title" className="space-y-4">
        <SectionHeading
          icon={IconReportAnalytics}
          title="Model-by-model analysis"
          description="Readable verdicts, strengths, limitations, scope, and efficiency evidence."
          titleID="model-analysis-title"
        />
        {analysis.ranked.map((comparison, index) => (
          <ModelAnalysisCard
            key={comparison.model_alias}
            comparison={comparison}
            color={chartColors[index % chartColors.length]}
            lowerOverallAndSlower={analysis.lowerOverallAndSlowerAliases.has(
              comparison.model_alias,
            )}
          />
        ))}
      </section>

      <section className="border-border bg-card rounded-2xl border p-5 md:p-6">
        <SectionHeading
          icon={IconTargetArrow}
          title="Methodology and reproducibility"
          description="The immutable inputs and interpretation boundaries behind this report."
        />
        <dl className="mt-5 grid gap-3 text-sm sm:grid-cols-2 xl:grid-cols-4">
          <Definition
            label="Repository / ref"
            value={`${evaluation.repository} · ${evaluation.ref || "HEAD"}`}
          />
          <Definition label="Commit" value={commit || "Not exposed"} />
          <Definition
            label="Selector model"
            value={evaluation.selector_model_alias}
          />
          <Definition
            label="Judge / analyzer"
            value={evaluation.judge_model_alias}
          />
          <Definition label="Probe ID" value={evaluation.id} />
          <Definition
            label="Created"
            value={formatTimestamp(evaluation.created_at)}
          />
          <Definition
            label="Completed"
            value={formatTimestamp(
              evaluation.finished_at ?? evaluation.updated_at,
            )}
          />
          <Definition
            label="Run history"
            value={`${evaluation.run_ids.length} durable workflow runs`}
          />
        </dl>
        {evaluation.corpus?.selection_rationale && (
          <div className="bg-muted/50 mt-5 rounded-xl p-4">
            <h3 className="text-sm font-semibold">
              Corpus selection rationale
            </h3>
            <p className="text-muted-foreground mt-2 text-sm leading-6">
              {evaluation.corpus.selection_rationale}
            </p>
          </div>
        )}
        {evaluation.warnings.length > 0 && (
          <details className="border-border mt-5 rounded-xl border p-4">
            <summary className="cursor-pointer text-sm font-semibold">
              Interpretation notes and warnings ({evaluation.warnings.length})
            </summary>
            <ul className="text-muted-foreground mt-3 list-disc space-y-2 pl-5 text-sm leading-5">
              {evaluation.warnings.map((warning) => (
                <li key={warning}>{warning}</li>
              ))}
            </ul>
          </details>
        )}
      </section>
    </div>
  )
}

export function ModelEvaluationReportPage({
  evaluationID,
  onBack,
}: {
  evaluationID: string
  onBack: () => void
}) {
  const [evaluation, setEvaluation] =
    useState<RepositoryModelEvaluation | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState("")
  const [reload, setReload] = useState(0)

  const load = useCallback(() => setReload((value) => value + 1), [])

  useEffect(() => {
    const controller = new AbortController()
    setLoading(true)
    setError("")
    void getModelEvaluation(evaluationID, controller.signal)
      .then((value) => {
        setEvaluation(value)
        setLoading(false)
      })
      .catch((reason: unknown) => {
        if (reason instanceof DOMException && reason.name === "AbortError")
          return
        setError(
          reason instanceof Error
            ? reason.message
            : "Failed to load model probe report.",
        )
        setLoading(false)
      })
    return () => controller.abort()
  }, [evaluationID, reload])

  return (
    <div className="flex h-full min-h-0 flex-col">
      <PageHeader title="Probe report">
        <Button type="button" size="sm" variant="outline" onClick={onBack}>
          <IconArrowLeft aria-hidden="true" />
          <span className="hidden sm:inline">Back to </span>probes
        </Button>
      </PageHeader>
      <section
        aria-label="Model probe report"
        className="min-h-0 flex-1 overflow-y-auto"
      >
        <div className="mx-auto w-full max-w-[96rem] p-4 md:p-6">
          {loading ? (
            <div
              className="border-border bg-card flex min-h-64 items-center justify-center rounded-2xl border"
              role="status"
            >
              <IconRefresh
                className="mr-2 size-5 animate-spin"
                aria-hidden="true"
              />{" "}
              Loading report…
            </div>
          ) : error ? (
            <div
              className="border-destructive/30 bg-destructive/5 rounded-2xl border p-6"
              role="alert"
            >
              <h2 className="font-semibold">Report could not be loaded</h2>
              <p className="mt-2 text-sm">{error}</p>
              <Button
                type="button"
                className="mt-4"
                variant="outline"
                onClick={load}
              >
                Try again
              </Button>
            </div>
          ) : evaluation &&
            evaluation.status === "completed" &&
            evaluation.comparisons.length > 0 ? (
            <>
              <header className="mb-6 flex flex-col gap-3 md:flex-row md:items-end md:justify-between">
                <div className="min-w-0">
                  <div className="flex flex-wrap items-center gap-2">
                    <Badge variant="outline">completed</Badge>
                    <span className="text-muted-foreground font-mono text-xs break-all">
                      {evaluation.id}
                    </span>
                  </div>
                  <h2 className="mt-3 text-2xl font-semibold break-words">
                    {evaluation.repository}
                  </h2>
                  <p className="text-muted-foreground mt-1 text-sm">
                    Revision {evaluation.ref || "HEAD"} · immutable AI-judged
                    model comparison
                  </p>
                </div>
                <div className="text-muted-foreground flex items-center gap-2 text-xs">
                  <IconClock className="size-4" aria-hidden="true" /> Completed{" "}
                  {formatTimestamp(
                    evaluation.finished_at ?? evaluation.updated_at,
                  )}
                </div>
              </header>
              <ReportBody evaluation={evaluation} />
            </>
          ) : evaluation ? (
            <div className="border-border bg-card rounded-2xl border p-6">
              <div className="flex items-center gap-2">
                <IconTargetArrow className="size-5" aria-hidden="true" />
                <h2 className="font-semibold">Report is not ready</h2>
              </div>
              <p className="text-muted-foreground mt-2 text-sm">
                Probe {evaluation.id} is {evaluation.status}. The full report
                appears only after completed comparison results are durable.
              </p>
              <div
                className="bg-muted mt-4 h-2 overflow-hidden rounded-full"
                role="progressbar"
                aria-label={`Probe progress ${evaluation.progress.percent.toFixed(0)} percent`}
                aria-valuemin={0}
                aria-valuemax={100}
                aria-valuenow={evaluation.progress.percent}
              >
                {/* ui-rule-allow dynamic-style: width reflects durable probe progress. */}
                <span
                  className="bg-primary block h-full"
                  style={{ width: `${evaluation.progress.percent}%` }}
                  aria-hidden="true"
                />
              </div>
              <Button
                type="button"
                className="mt-4"
                variant="outline"
                onClick={load}
              >
                <IconRefresh aria-hidden="true" /> Check again
              </Button>
            </div>
          ) : null}
        </div>
      </section>
    </div>
  )
}
