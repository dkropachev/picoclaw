import type {
  RepositoryReviewDeduplicationState,
  RepositoryReviewHistoricalConsolidationStatus,
  RepositoryReviewRawFindingDisposition,
} from "@/api/repository-reviews"

const stateLabels: Record<RepositoryReviewDeduplicationState, string> = {
  pending: "Queued",
  running: "Processing",
  failed: "Failed",
  completed: "Completed",
}

const dispositionLabels: Record<RepositoryReviewRawFindingDisposition, string> =
  {
    undecided: "Undecided",
    new: "New finding",
    duplicate: "Matched finding",
  }

const historicalLabels: Record<
  RepositoryReviewHistoricalConsolidationStatus,
  string
> = {
  not_required: "Not required",
  pending: "Pending",
  replaying: "Replaying",
  merging: "Merging",
  failed: "Failed",
  completed: "Completed",
}

export function repositoryReviewProcessingStateLabel(
  state: RepositoryReviewDeduplicationState,
): string {
  return stateLabels[state]
}

export function repositoryReviewProcessingDispositionLabel(
  disposition: RepositoryReviewRawFindingDisposition,
): string {
  return dispositionLabels[disposition]
}

export function repositoryReviewHistoricalConsolidationLabel(
  state: RepositoryReviewHistoricalConsolidationStatus,
): string {
  return historicalLabels[state]
}
