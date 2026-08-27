import type {
  RepositoryReviewFinding,
  RepositoryReviewRunFindingStatusState,
} from "@/api/repository-reviews"

export function runFindingStatusState(
  finding: RepositoryReviewFinding,
): RepositoryReviewRunFindingStatusState {
  if (finding.run_finding_status) return finding.run_finding_status
  if (finding.repository_match_state === "provisional") return "needs_review"
  if (finding.repository_finding_id) {
    return finding.repository_match_state === "new"
      ? "associated_new"
      : "associated_existing"
  }
  return "pending"
}

export function runFindingStatusLabel(
  finding: RepositoryReviewFinding,
): string {
  switch (runFindingStatusState(finding)) {
    case "processing":
      return "Processing"
    case "failed":
      return "Failed"
    case "associated_new":
      return "Created repository finding"
    case "associated_existing":
      return "Added to existing repository finding"
    case "needs_review":
      return "Needs review"
    default:
      return "Pending"
  }
}

export function runFindingStatusDescription(
  finding: RepositoryReviewFinding,
): string {
  switch (runFindingStatusState(finding)) {
    case "processing":
      return "Repository status is being determined."
    case "failed":
      return "Repository status could not be determined. Retry when ready."
    case "associated_new":
      return "A repository finding was created from this run finding."
    case "associated_existing":
      return "This run finding was added to an existing repository finding."
    case "needs_review":
      return "Repository status needs a decision before issue actions are available."
    default:
      return "Waiting to determine repository status."
  }
}

export function runFindingRepositoryFindingID(
  finding: RepositoryReviewFinding,
): string | undefined {
  return finding.repository_finding_id
}

export function runFindingStatusCanRetry(
  finding: RepositoryReviewFinding,
): boolean {
  return runFindingStatusState(finding) === "failed"
}

export function runFindingStatusIsInProgress(
  finding: RepositoryReviewFinding,
): boolean {
  const state = runFindingStatusState(finding)
  return state === "pending" || state === "processing"
}
