import type { RepositoryReviewIssueDraftState } from "@/api/repository-reviews"
import { githubRepositoryPath } from "@/components/repository-reviews/repository-review-actions"

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

export function safeRepositoryReviewGitHubIssueURL(
  value: string | undefined,
  repository: string | undefined,
): string | undefined {
  const safeURL = safeHTTPSURL(value)
  const repositoryPath = repository
    ? githubRepositoryPath(repository)?.toLowerCase()
    : undefined
  if (!safeURL || !repositoryPath) return undefined
  const url = new URL(safeURL)
  const segments = url.pathname.split("/").filter(Boolean)
  if (
    url.hostname.toLowerCase() !== "github.com" ||
    url.port ||
    url.search ||
    url.hash ||
    segments.length !== 4 ||
    `${segments[0]}/${segments[1]}`.toLowerCase() !== repositoryPath ||
    segments[2]?.toLowerCase() !== "issues" ||
    !/^[1-9][0-9]*$/u.test(segments[3] ?? "")
  ) {
    return undefined
  }
  return safeURL
}

function safeHTTPSURL(value: string | undefined): string | undefined {
  if (!value) return undefined
  try {
    const url = new URL(value)
    return url.protocol === "https:" && !url.username && !url.password
      ? url.toString()
      : undefined
  } catch {
    return undefined
  }
}
