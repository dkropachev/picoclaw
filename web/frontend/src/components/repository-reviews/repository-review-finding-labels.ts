import type {
  RepositoryFindingIssueState,
  RepositoryFindingLifecycle,
  RepositoryFindingValidationState,
  RepositoryReviewMatchState,
} from "@/api/repository-reviews"

export type RepositoryFindingAttention =
  | "duplicate_review"
  | "issue_conflict"
  | "fix_check_failed"

export function repositoryFindingMatchLabel(
  state: RepositoryReviewMatchState,
): string {
  switch (state) {
    case "new":
      return "Unique so far"
    case "known":
      return "Matched existing finding"
    case "provisional":
      return "Needs duplicate review"
  }
}

export function repositoryFindingLifecycleLabel(
  state: RepositoryFindingLifecycle,
): string {
  switch (state) {
    case "open":
      return "Open"
    case "resolution_pending":
      return "Resolution pending"
    case "resolved":
      return "Resolved"
    case "regressed":
      return "Regressed"
    case "dismissed":
      return "Dismissed"
  }
}

export function repositoryFindingIssueLabel(
  state: RepositoryFindingIssueState,
): string {
  switch (state) {
    case "none":
      return "No issue or preview"
    case "draft":
      return "Saved preview · Not on GitHub"
    case "open":
      return "GitHub issue open"
    case "closed":
      return "GitHub issue closed"
    case "unknown":
      return "GitHub issue state unknown"
  }
}

export function repositoryFindingResolutionLabel(
  state: RepositoryFindingValidationState,
): string {
  switch (state) {
    case "not_requested":
      return "Not checked"
    case "pending":
      return "Queued"
    case "running":
      return "Checking"
    case "confirmed":
      return "Fix confirmed"
    case "not_fixed":
      return "Still present"
    case "inconclusive":
      return "Inconclusive"
    case "failed":
      return "Check failed"
  }
}

export function repositoryFindingResolutionActionLabel(
  state: RepositoryFindingValidationState,
): string {
  switch (state) {
    case "pending":
      return "Fix check queued"
    case "running":
      return "Checking for fix…"
    case "failed":
      return "Retry fix check"
    default:
      return "Check for fix"
  }
}

export function repositoryFindingAttentionLabel(
  attention: RepositoryFindingAttention,
): string {
  switch (attention) {
    case "duplicate_review":
      return "Duplicate review"
    case "issue_conflict":
      return "Issue conflict"
    case "fix_check_failed":
      return "Fix check failed"
  }
}
