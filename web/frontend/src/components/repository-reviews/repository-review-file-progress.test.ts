import { describe, expect, it } from "vitest"

import {
  type RepositoryReviewFileProgressSource,
  repositoryReviewFileProgress,
  repositoryReviewFileProgressLabel,
  repositoryReviewInspectedFilesLabel,
} from "./repository-review-file-progress"

const review = {
  status: "running",
  run_ids: ["legacy-run"],
  started_at: "2026-08-28T00:00:00Z",
  progress: {
    stage: "waiting",
    completed_batches: 0,
    total_batches: 1,
    coverage_available: false,
    coverage_exact: false,
    selected_files: 10,
    inspected_files: 0,
    reviewed_files: 0,
    remaining_files: 0,
    unsupported_files: 0,
    findings: 0,
    finding_aggregates: 0,
    unaggregated_findings: 0,
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
  // Campaign inspection did not exist in the legacy counter model, so a
  // missing coverage envelope must not be rendered as a measured zero.
  it("labels exact, lower-bound, and unavailable inspection coverage", () => {
    expect(repositoryReviewInspectedFilesLabel(review)).toBe("Unknown")
    expect(
      repositoryReviewInspectedFilesLabel({
        ...review,
        status: "idle",
        run_ids: [],
        started_at: undefined,
      }),
    ).toBe(0)
    expect(
      repositoryReviewInspectedFilesLabel({
        ...review,
        run_ids: [],
        started_at: undefined,
      }),
    ).toBe("Unknown")
    expect(
      repositoryReviewInspectedFilesLabel({
        ...review,
        progress: {
          ...review.progress,
          coverage_available: true,
          coverage_exact: false,
          inspected_files: 3,
        },
      }),
    ).toBe("At least 3")
    expect(
      repositoryReviewInspectedFilesLabel({
        ...review,
        progress: {
          ...review.progress,
          coverage_available: true,
          coverage_exact: true,
          inspected_files: 4,
        },
      }),
    ).toBe(4)
  })

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
