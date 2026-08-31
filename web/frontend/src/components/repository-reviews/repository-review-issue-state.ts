import type { RepositoryReviewIssueDraftState } from "@/api/repository-reviews"

export function repositoryReviewIssueStateLabel(
  state: RepositoryReviewIssueDraftState,
): string {
  switch (state) {
    case "generating":
      return "Generating preview"
    case "failed":
      return "Preview generation failed"
    case "editing":
      return "Unpublished preview · Not on GitHub"
    case "publishing":
      return "Posting to GitHub"
    case "unknown":
      return "Publication needs reconciliation"
    case "posted":
      return "Posted to GitHub"
  }
}
