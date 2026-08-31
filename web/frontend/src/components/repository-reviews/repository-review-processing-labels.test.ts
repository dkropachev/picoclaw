import { describe, expect, it } from "vitest"

import {
  repositoryReviewHistoricalConsolidationLabel,
  repositoryReviewProcessingDispositionLabel,
  repositoryReviewProcessingStateLabel,
} from "./repository-review-processing-labels"

describe("repository review processing labels", () => {
  it.each([
    ["pending", "Queued"],
    ["running", "Processing"],
    ["failed", "Failed"],
    ["completed", "Completed"],
  ] as const)("labels state %s", (state, label) => {
    expect(repositoryReviewProcessingStateLabel(state)).toBe(label)
  })

  it.each([
    ["undecided", "Undecided"],
    ["new", "New finding"],
    ["duplicate", "Matched finding"],
  ] as const)("labels disposition %s", (state, label) => {
    expect(repositoryReviewProcessingDispositionLabel(state)).toBe(label)
  })

  it.each([
    ["not_required", "Not required"],
    ["pending", "Pending"],
    ["replaying", "Replaying"],
    ["merging", "Merging"],
    ["failed", "Failed"],
    ["completed", "Completed"],
  ] as const)("labels historical consolidation %s", (state, label) => {
    expect(repositoryReviewHistoricalConsolidationLabel(state)).toBe(label)
  })
})
