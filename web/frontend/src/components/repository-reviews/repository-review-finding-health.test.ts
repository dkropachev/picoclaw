import { describe, expect, it } from "vitest"

import type { RepositoryReviewFindingHealth } from "@/api/repository-reviews"

import {
  repositoryReviewAutomationIsActive,
  repositoryReviewFindingHealthNeedsPolling,
  repositoryReviewHistoricalConsolidationIsActive,
} from "./repository-review-finding-health"

const terminalHealth: RepositoryReviewFindingHealth = {
  run_findings: {
    total: 8,
    pending: 0,
    processing: 0,
    failed: 2,
    needs_review: 1,
    associated_new: 2,
    associated_existing: 3,
    unrepresented: 2,
  },
  repository_findings: {
    total: 4,
    provisional: 1,
    validation_failed: 1,
    issue_conflicts: 1,
  },
  findings_processing: {
    total: 9,
    pending: 0,
    processing: 0,
    failed: 2,
    completed: 7,
  },
  historical_consolidation: {
    required: true,
    status: "failed",
    retryable: true,
  },
  updated_at: "2026-08-31T12:00:00Z",
}

describe("repository review finding health polling", () => {
  it.each([
    ["running", false, "reviewing", true],
    ["stopping", false, "stopping", true],
    ["idle", true, "Next batch queued", true],
    ["idle", false, "Next batch queued", false],
    ["completed", true, "completed", false],
  ] as const)(
    "projects %s / %s / %s activity",
    (status, autoContinue, stage, expected) => {
      expect(
        repositoryReviewAutomationIsActive({
          status,
          auto_continue: autoContinue,
          progress: { stage },
        }),
      ).toBe(expected)
    },
  )

  it.each(["pending", "replaying", "merging"] as const)(
    "polls active historical consolidation state %s",
    (status) => {
      expect(
        repositoryReviewHistoricalConsolidationIsActive({
          required: true,
          status,
        }),
      ).toBe(true)
    },
  )

  it("does not poll terminal attention states", () => {
    expect(
      repositoryReviewFindingHealthNeedsPolling(undefined, terminalHealth),
    ).toBe(false)
  })

  it.each([
    ["run pending", { run_findings: { pending: 1 } }],
    ["run processing", { run_findings: { processing: 1 } }],
    ["source pending", { findings_processing: { pending: 1 } }],
    ["source processing", { findings_processing: { processing: 1 } }],
  ])("polls %s work", (_label, change) => {
    const health = structuredClone(terminalHealth)
    Object.assign(
      health.run_findings,
      (change as { run_findings?: object }).run_findings,
    )
    Object.assign(
      health.findings_processing,
      (change as { findings_processing?: object }).findings_processing,
    )
    expect(repositoryReviewFindingHealthNeedsPolling(undefined, health)).toBe(
      true,
    )
  })
})
