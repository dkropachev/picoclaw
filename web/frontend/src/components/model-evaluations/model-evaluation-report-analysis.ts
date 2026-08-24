import type { EvaluationComparison } from "@/api/model-evaluations"

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

export function isFiniteModelEvaluationNumber(value: unknown): value is number {
  return typeof value === "number" && Number.isFinite(value)
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
