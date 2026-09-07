import type {
  RepositoryReviewDeduplicationState,
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
