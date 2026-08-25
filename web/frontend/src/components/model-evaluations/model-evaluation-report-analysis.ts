import type {
  EvaluationComparison,
  EvaluationUsage,
  EvaluationWorkSizingAxis,
  EvaluationWorkSizingResult,
} from "@/api/model-evaluations"

export type ModelEvaluationScoreDimension =
  | "overall"
  | "correctness"
  | "evidence"
  | "coverage"
  | "actionability"

export interface ModelEvaluationReportAnalysis {
  ranked: EvaluationComparison[]
  winner?: EvaluationComparison
  runnerUp?: EvaluationComparison
  fastest?: EvaluationComparison
  highestSupportedClaimShare?: EvaluationComparison
  lowerOverallAndSlowerAliases: Set<string>
  qualityGap?: number
  coverageGap?: number
  fastestTimeSaving?: number
  fastestQualityGap?: number
}

export interface ModelEvaluationDegradationCeiling {
  kind: "degraded" | "at_least"
  value: number
  baselineValue: number
  baselineScore: number
  degradedAt?: number
}

export function isFiniteModelEvaluationNumber(value: unknown): value is number {
  return typeof value === "number" && Number.isFinite(value)
}

export function modelEvaluationEffectiveTokens(usage: EvaluationUsage): number {
  const input = Math.max(0, usage.input_tokens)
  const cached = Math.min(input, Math.max(0, usage.cached_input_tokens))
  const output = Math.max(0, usage.output_tokens)
  return input - cached + cached * 0.1 + output
}

export function modelEvaluationEffectiveTokensPerKiB(
  usage: EvaluationUsage,
  bytesAnalyzed: number,
): number | undefined {
  if (!isFiniteModelEvaluationNumber(bytesAnalyzed) || bytesAnalyzed <= 0) {
    return undefined
  }
  return (modelEvaluationEffectiveTokens(usage) * 1024) / bytesAnalyzed
}

export function modelEvaluationSizingPointAttained(
  result: EvaluationWorkSizingResult,
  axis = result.axis,
): boolean {
  if (result.completion !== "completed" || result.batch_samples < 1) {
    return false
  }
  if (axis === "configured") {
    return (
      result.observed_max_files_per_batch > 0 &&
      result.observed_max_files_per_batch <= result.files_per_batch &&
      result.observed_max_content_bytes_per_batch > 0 &&
      result.observed_max_content_bytes_per_batch <=
        result.content_bytes_per_batch
    )
  }
  return axis === "files_per_batch"
    ? result.observed_max_files_per_batch > 0 &&
        result.observed_max_files_per_batch <= result.files_per_batch
    : result.observed_max_content_bytes_per_batch > 0 &&
        result.observed_max_content_bytes_per_batch <=
          result.content_bytes_per_batch
}

export function modelEvaluationDegradationCeiling(
  results: EvaluationWorkSizingResult[],
  modelAlias: string,
  axis: EvaluationWorkSizingAxis,
  score = "overall",
  drop = 5,
): ModelEvaluationDegradationCeiling | undefined {
  const value = (result: EvaluationWorkSizingResult) =>
    axis === "files_per_batch"
      ? result.observed_max_files_per_batch
      : result.observed_max_content_bytes_per_batch
  const attained = results
    .filter(
      (result) =>
        result.model_alias === modelAlias &&
        (result.axis === axis || result.axis === "configured") &&
        modelEvaluationSizingPointAttained(result, axis) &&
        (result.scores[score]?.samples ?? 0) > 0 &&
        isFiniteModelEvaluationNumber(result.scores[score]?.weighted_mean),
    )
    .sort((left, right) => value(left) - value(right))
  const observed: Array<{
    value: number
    score: number
    weight: number
  }> = []
  for (const result of attained) {
    const observedValue = value(result)
    const observedScore = result.scores[score]?.weighted_mean
    if (!isFiniteModelEvaluationNumber(observedScore)) continue
    const weight = Math.max(1, result.files_analyzed)
    const prior = observed.at(-1)
    if (prior?.value === observedValue) {
      const combinedWeight = prior.weight + weight
      prior.score =
        (prior.score * prior.weight + observedScore * weight) / combinedWeight
      prior.weight = combinedWeight
      continue
    }
    observed.push({ value: observedValue, score: observedScore, weight })
  }
  const baseline = observed[0]
  if (!baseline) {
    return undefined
  }
  for (let index = 1; index < observed.length; index += 1) {
    const current = observed[index]
    if (current && baseline.score - current.score >= drop) {
      return {
        kind: "degraded",
        value: observed[index - 1]?.value ?? baseline.value,
        baselineValue: baseline.value,
        baselineScore: baseline.score,
        degradedAt: current.value,
      }
    }
  }
  const last = observed.at(-1)
  return last
    ? {
        kind: "at_least",
        value: last.value,
        baselineValue: baseline.value,
        baselineScore: baseline.score,
      }
    : undefined
}

export function modelEvaluationComparisonScore(
  comparison: EvaluationComparison,
  dimension: ModelEvaluationScoreDimension,
): number | undefined {
  const value =
    dimension === "overall"
      ? comparison.overall_score
      : comparison.scores[dimension]
  return isFiniteModelEvaluationNumber(value)
    ? Math.min(100, Math.max(0, value))
    : undefined
}

export function modelEvaluationSupportedClaimRate(
  comparison: EvaluationComparison,
): number | undefined {
  const supported = Math.max(0, comparison.confirmed_findings)
  const unsupported = modelEvaluationUnsupportedClaims(comparison)
  if (unsupported == null) return undefined
  const total = supported + unsupported
  return total > 0 ? (supported / total) * 100 : undefined
}

export function modelEvaluationUnsupportedClaims(
  comparison: EvaluationComparison,
): number | undefined {
  return isFiniteModelEvaluationNumber(comparison.unsupported_claims)
    ? Math.max(0, comparison.unsupported_claims)
    : undefined
}

export function positionModelEvaluationDonutSegments<
  Segment extends { value: number },
>(segments: Segment[]): Array<Segment & { percent: number; offset: number }> {
  const positive = segments.filter((segment) => segment.value > 0)
  const total = positive.reduce((sum, segment) => sum + segment.value, 0)
  return positive.map((segment, index) => ({
    ...segment,
    percent: total > 0 ? (segment.value / total) * 100 : 0,
    offset:
      total > 0
        ? (positive
            .slice(0, index)
            .reduce((sum, previous) => sum + previous.value, 0) /
            total) *
          100
        : 0,
  }))
}

export function buildModelEvaluationReportAnalysis(
  comparisons: EvaluationComparison[],
): ModelEvaluationReportAnalysis {
  const ranked = [...comparisons].sort((left, right) => {
    const leftRank = left.rank > 0 ? left.rank : Number.MAX_SAFE_INTEGER
    const rightRank = right.rank > 0 ? right.rank : Number.MAX_SAFE_INTEGER
    if (leftRank !== rightRank) return leftRank - rightRank
    return (right.overall_score ?? -1) - (left.overall_score ?? -1)
  })
  const scored = ranked.filter(
    (comparison) =>
      comparison.completion === "completed" &&
      isFiniteModelEvaluationNumber(comparison.overall_score),
  )
  const winner = scored[0]
  const runnerUp = scored[1]
  const timed = scored.filter(
    (comparison) => comparison.usage.duration_millis > 0,
  )
  const fastest = [...timed].sort(
    (left, right) => left.usage.duration_millis - right.usage.duration_millis,
  )[0]
  const assessed = scored.filter(
    (comparison) => modelEvaluationSupportedClaimRate(comparison) != null,
  )
  const highestSupportedClaimShare = [...assessed].sort(
    (left, right) =>
      (modelEvaluationSupportedClaimRate(right) ?? 0) -
      (modelEvaluationSupportedClaimRate(left) ?? 0),
  )[0]
  const lowerOverallAndSlowerAliases = new Set<string>()
  for (const candidate of timed) {
    for (const other of timed) {
      if (candidate.model_alias === other.model_alias) continue
      const candidateScore = candidate.overall_score ?? -1
      const otherScore = other.overall_score ?? -1
      const noWorse =
        otherScore >= candidateScore &&
        other.usage.duration_millis <= candidate.usage.duration_millis
      const strictlyBetter =
        otherScore > candidateScore ||
        other.usage.duration_millis < candidate.usage.duration_millis
      if (noWorse && strictlyBetter) {
        lowerOverallAndSlowerAliases.add(candidate.model_alias)
        break
      }
    }
  }
  const qualityGap =
    winner?.overall_score != null && runnerUp?.overall_score != null
      ? winner.overall_score - runnerUp.overall_score
      : undefined
  const winnerCoverage = winner
    ? modelEvaluationComparisonScore(winner, "coverage")
    : undefined
  const fastestCoverage = fastest
    ? modelEvaluationComparisonScore(fastest, "coverage")
    : undefined
  const coverageGap =
    winnerCoverage != null && fastestCoverage != null
      ? winnerCoverage - fastestCoverage
      : undefined
  const fastestTimeSaving =
    winner &&
    fastest &&
    winner.usage.duration_millis > 0 &&
    winner.model_alias !== fastest.model_alias
      ? 100 * (1 - fastest.usage.duration_millis / winner.usage.duration_millis)
      : undefined
  const fastestQualityGap =
    winner?.overall_score != null && fastest?.overall_score != null
      ? winner.overall_score - fastest.overall_score
      : undefined
  return {
    ranked,
    winner,
    runnerUp,
    fastest,
    highestSupportedClaimShare,
    lowerOverallAndSlowerAliases,
    qualityGap,
    coverageGap,
    fastestTimeSaving,
    fastestQualityGap,
  }
}
