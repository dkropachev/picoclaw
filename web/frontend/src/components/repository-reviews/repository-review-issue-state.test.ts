import { describe, expect, it } from "vitest"

import { repositoryReviewIssueStateLabel } from "./repository-review-issue-state"

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
