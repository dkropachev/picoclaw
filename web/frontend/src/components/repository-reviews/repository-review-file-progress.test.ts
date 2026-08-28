import { describe, expect, it } from "vitest"

import {
  type RepositoryReviewFileProgressSource,
  repositoryReviewFileProgress,
  repositoryReviewFileProgressLabel,
} from "./repository-review-file-progress"

const review = {
  status: "running",
  progress: {
    stage: "waiting",
    completed_batches: 0,
    total_batches: 1,
    reviewed_files: 0,
    remaining_files: 0,
    unsupported_files: 0,
    findings: 0,
    scope_frozen: true,
  },
  scope_plan: {
    commit_sha: "a".repeat(40),
    policy_hash: "b".repeat(64),
    hash: "c".repeat(64),
    summary: "Frozen scope",
    rationale: "",
    warnings: [],
    counts: {
      total_files: 12,
      code_type_files: 10,
      include_files: 10,
      excluded_files: 0,
      selected_files: 10,
    },
  },
} satisfies RepositoryReviewFileProgressSource

describe("repositoryReviewFileProgress", () => {
  it("does not report a false 100 percent before the first checkpoint", () => {
    expect(repositoryReviewFileProgress(review)).toEqual({
      resolved: 0,
      total: 10,
      percent: 0,
    })
    expect(repositoryReviewFileProgressLabel(review)).toBe("0 of 10 files (0%)")
  })

  it("does not treat batches or finding-only checkpoints as file progress", () => {
    expect(
      repositoryReviewFileProgress({
        ...review,
        progress: {
          ...review.progress,
          completed_batches: 16,
          total_batches: 32,
          findings: 74,
        },
      }),
    ).toEqual({ resolved: 0, total: 10, percent: 0 })
  })

  it("uses frozen selected scope and remaining files", () => {
    expect(
      repositoryReviewFileProgress({
        ...review,
        progress: {
          ...review.progress,
          completed_batches: 1,
          remaining_files: 6,
        },
      }),
    ).toEqual({ resolved: 4, total: 10, percent: 40 })
  })

  it("rounds a frozen one-third percentage to the integer UI contract", () => {
    expect(
      repositoryReviewFileProgress({
        ...review,
        scope_plan: {
          ...review.scope_plan,
          counts: { ...review.scope_plan.counts, selected_files: 3 },
        },
        progress: { ...review.progress, remaining_files: 2 },
      }),
    ).toEqual({ resolved: 1, total: 3, percent: 33 })
  })

  it("ignores a stale legacy scope plan without a frozen marker", () => {
    expect(
      repositoryReviewFileProgress({
        ...review,
        progress: {
          ...review.progress,
          scope_frozen: false,
          reviewed_files: 3,
          unsupported_files: 1,
          remaining_files: 6,
        },
        scope_plan: {
          ...review.scope_plan,
          counts: { ...review.scope_plan.counts, selected_files: 100 },
        },
      }),
    ).toEqual({ resolved: 4, total: 10, percent: 40 })
  })

  it("uses legacy fully reviewed and unsupported counters", () => {
    expect(
      repositoryReviewFileProgress({
        ...review,
        scope_plan: undefined,
        progress: {
          ...review.progress,
          reviewed_files: 3,
          unsupported_files: 1,
          remaining_files: 6,
        },
      }),
    ).toEqual({ resolved: 4, total: 10, percent: 40 })
  })

  it("reports completed all-prechecked campaigns as complete", () => {
    expect(
      repositoryReviewFileProgress({
        ...review,
        status: "completed",
        scope_plan: undefined,
        progress: { ...review.progress, total_batches: 0 },
      }),
    ).toEqual({ resolved: 0, total: 0, percent: 100 })
  })
})
