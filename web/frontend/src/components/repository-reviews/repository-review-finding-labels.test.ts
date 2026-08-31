import { describe, expect, it } from "vitest"

import {
  repositoryFindingAttentionLabel,
  repositoryFindingIssueLabel,
  repositoryFindingLifecycleLabel,
  repositoryFindingMatchLabel,
  repositoryFindingResolutionActionLabel,
  repositoryFindingResolutionLabel,
} from "./repository-review-finding-labels"

describe("repository finding labels", () => {
  it.each([
    ["new", "Unique so far"],
    ["known", "Matched existing finding"],
    ["provisional", "Needs duplicate review"],
  ] as const)("labels match state %s", (state, label) => {
    expect(repositoryFindingMatchLabel(state)).toBe(label)
  })

  it.each([
    ["open", "Open"],
    ["resolution_pending", "Resolution pending"],
    ["resolved", "Resolved"],
    ["regressed", "Regressed"],
    ["dismissed", "Dismissed"],
  ] as const)("labels lifecycle %s", (state, label) => {
    expect(repositoryFindingLifecycleLabel(state)).toBe(label)
  })

  it.each([
    ["none", "No issue or preview"],
    ["draft", "Saved preview · Not on GitHub"],
    ["open", "GitHub issue open"],
    ["closed", "GitHub issue closed"],
    ["unknown", "GitHub issue state unknown"],
  ] as const)("labels issue state %s", (state, label) => {
    expect(repositoryFindingIssueLabel(state)).toBe(label)
  })

  it.each([
    ["not_requested", "Not checked"],
    ["pending", "Queued"],
    ["running", "Checking"],
    ["confirmed", "Fix confirmed"],
    ["not_fixed", "Still present"],
    ["inconclusive", "Inconclusive"],
    ["failed", "Check failed"],
  ] as const)("labels resolution state %s", (state, label) => {
    expect(repositoryFindingResolutionLabel(state)).toBe(label)
  })

  it.each([
    ["not_requested", "Check for fix"],
    ["pending", "Fix check queued"],
    ["running", "Checking for fix…"],
    ["confirmed", "Check for fix"],
    ["not_fixed", "Check for fix"],
    ["inconclusive", "Check for fix"],
    ["failed", "Retry fix check"],
  ] as const)("labels resolution action %s", (state, label) => {
    expect(repositoryFindingResolutionActionLabel(state)).toBe(label)
  })

  it.each([
    ["duplicate_review", "Duplicate review"],
    ["issue_conflict", "Issue conflict"],
    ["fix_check_failed", "Fix check failed"],
  ] as const)("labels attention %s", (attention, label) => {
    expect(repositoryFindingAttentionLabel(attention)).toBe(label)
  })
})
