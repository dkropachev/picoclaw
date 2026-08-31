import { describe, expect, it } from "vitest"

import {
  repositoryReviewIssueStateLabel,
  safeRepositoryReviewGitHubIssueURL,
} from "./repository-review-issue-state"

describe("repositoryReviewIssueStateLabel", () => {
  it.each([
    ["generating", "Generating preview"],
    ["failed", "Preview generation failed"],
    ["editing", "Unpublished preview · Not on GitHub"],
    ["publishing", "Posting to GitHub"],
    ["unknown", "Publication needs reconciliation"],
    ["posted", "Posted to GitHub"],
  ] as const)("labels %s without exposing the API enum", (state, label) => {
    expect(repositoryReviewIssueStateLabel(state)).toBe(label)
  })
})

describe("safeRepositoryReviewGitHubIssueURL", () => {
  it("accepts only a same-repository positive GitHub issue URL", () => {
    expect(
      safeRepositoryReviewGitHubIssueURL(
        "https://github.com/owner/repo/issues/42",
        "owner/repo",
      ),
    ).toBe("https://github.com/owner/repo/issues/42")
  })

  it.each([
    "https://github.com/other/repo/issues/42",
    "https://issues.example.com/owner/repo/42",
    "http://github.com/owner/repo/issues/42",
    "https://user@github.com/owner/repo/issues/42",
    "https://github.com/owner/repo/issues/0",
    "https://github.com/owner/repo/pull/42",
    "https://github.com/owner/repo/issues/42?tab=activity",
    "https://github.com/owner/repo/issues/42#comment",
  ])("rejects unsafe or unrelated URL %s", (url) => {
    expect(
      safeRepositoryReviewGitHubIssueURL(url, "owner/repo"),
    ).toBeUndefined()
  })
})
