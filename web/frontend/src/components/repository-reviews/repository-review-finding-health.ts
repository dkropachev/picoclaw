import { useQuery } from "@tanstack/react-query"

import {
  type RepositoryReviewAutomation,
  type RepositoryReviewFindingHealth,
  type RepositoryReviewHistoricalConsolidation,
  getRepositoryReviewFindingHealth,
} from "@/api/repository-reviews"

export const repositoryReviewFindingHealthQueryKey = (automationID: string) =>
  ["repository-review-finding-health", automationID] as const

interface RepositoryReviewActivity {
  status: RepositoryReviewAutomation["status"]
  auto_continue: boolean
  progress: { stage: string }
}

export function useRepositoryReviewFindingHealth(
  automationID: string,
  review?: RepositoryReviewAutomation,
) {
  return useQuery({
    queryKey: repositoryReviewFindingHealthQueryKey(automationID),
    queryFn: ({ signal }) =>
      getRepositoryReviewFindingHealth(automationID, signal),
    enabled: Boolean(automationID),
    retry: false,
    refetchInterval: (current) =>
      repositoryReviewFindingHealthNeedsPolling(review, current.state.data) ||
      (review &&
        repositoryReviewAutomationIsActive(review) &&
        current.state.status === "error")
        ? 2_000
        : false,
  })
}

export function repositoryReviewFindingHealthNeedsPolling(
  review?: RepositoryReviewActivity,
  health?: RepositoryReviewFindingHealth,
): boolean {
  if (review && repositoryReviewAutomationIsActive(review)) return true
  if (!health) return false
  return (
    health.run_findings.pending > 0 ||
    health.run_findings.processing > 0 ||
    health.findings_processing.pending > 0 ||
    health.findings_processing.processing > 0 ||
    repositoryReviewHistoricalConsolidationIsActive(
      health.historical_consolidation,
    )
  )
}

export function repositoryReviewHistoricalConsolidationIsActive(
  consolidation?:
    | Pick<RepositoryReviewHistoricalConsolidation, "required" | "status">
    | { required: boolean; status?: string },
): boolean {
  return Boolean(
    consolidation?.required &&
    new Set(["pending", "replaying", "merging"]).has(
      consolidation.status ?? "",
    ),
  )
}

export function repositoryReviewAutomationIsActive(
  review: RepositoryReviewActivity,
): boolean {
  return (
    review.status === "running" ||
    review.status === "stopping" ||
    (review.status === "idle" &&
      review.auto_continue &&
      review.progress.stage.trim().toLowerCase() === "next batch queued")
  )
}
