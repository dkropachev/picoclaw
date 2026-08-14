import { beforeEach, describe, expect, it, vi } from "vitest"

import {
  createPRLifecycleGateStage,
  getPRLifecycleGateProfiles,
  prLifecycleKnownDecisionPoints,
  validatePRLifecycleGateProfiles,
} from "@/api/pr-lifecycle-gate-profiles"
import { requestPRWorkspaceJSON } from "@/api/pr-workspaces"

vi.mock("@/api/pr-workspaces", async (importOriginal) => {
  const original = await importOriginal<typeof import("@/api/pr-workspaces")>()
  return { ...original, requestPRWorkspaceJSON: vi.fn() }
})

const mockedRequest = vi.mocked(requestPRWorkspaceJSON)
const validSnapshot = {
  gate_profiles: {
    default: {
      name: "Default",
      workflows: {
        "pr.review.complete": {
          id: "review_complete",
          name: "Review complete",
          purpose: "authorization",
          decision_point: "pr.review.complete",
          stages: [{ id: "automatic", kind: "zero" }],
        },
      },
    },
  },
  default_gate_profile_id: "default",
  repository_assignments: {},
  nudge: {
    review_minimum_additional: 2,
    review_maximum_additional: 5,
    completion_minimum_additional: 2,
    completion_maximum_additional: 5,
  },
  scope: {
    xs: { files: 1, semantic_lines: 20, modules: 1 },
    s: { files: 3, semantic_lines: 100, modules: 1 },
    m: { files: 10, semantic_lines: 500, modules: 3 },
  },
  deferred_issues: { mode: "ask" },
  catalog_revision: "sha256:catalog",
  config_revision: "sha256:config",
  effects: { gateway_effect: "applied" },
} as const

describe("PR lifecycle gate profiles", () => {
  beforeEach(() => mockedRequest.mockReset())

  it("creates exact core gate shapes", () => {
    expect(prLifecycleKnownDecisionPoints).toContain("pr.implementation.scope")
    expect(prLifecycleKnownDecisionPoints).toContain("pr.finding.classify")
    expect(prLifecycleKnownDecisionPoints).not.toContain(
      "pr.implementation.exhausted" as never,
    )
    expect(prLifecycleKnownDecisionPoints).not.toContain(
      "pr.validation.accept" as never,
    )
    expect(createPRLifecycleGateStage("deterministic", "head_check")).toEqual({
      id: "head_check",
      kind: "deterministic",
      title: "",
      when: "true",
    })
    expect(createPRLifecycleGateStage("zero", "automatic")).toEqual({
      id: "automatic",
      kind: "zero",
    })
    expect(createPRLifecycleGateStage("human", "approval")).toMatchObject({
      id: "approval",
      kind: "human",
      questions: ["Approve this step?"],
    })
  })

  it("validates staged all-of requirements and assignments", () => {
    const issues = validatePRLifecycleGateProfiles({
      gate_profiles: {
        default: {
          name: "Default",
          workflows: {
            "pr.review.complete": {
              id: "review_complete",
              name: "Review complete",
              purpose: "authorization",
              decision_point: "pr.review.complete",
              stages: [
                { id: "bad.stage", kind: "deterministic", title: "", when: "" },
                {
                  id: "bad.stage",
                  kind: "human",
                  title: "Approve",
                  questions: [],
                },
              ],
            },
          },
        },
      },
      default_gate_profile_id: "missing",
      repository_assignments: { "not a repo": "missing" },
      nudge: {
        review_minimum_additional: 2,
        review_maximum_additional: 5,
        completion_minimum_additional: 2,
        completion_maximum_additional: 5,
      },
      scope: {
        xs: { files: 1, semantic_lines: 20, modules: 1 },
        s: { files: 3, semantic_lines: 100, modules: 1 },
        m: { files: 10, semantic_lines: 500, modules: 3 },
      },
      deferred_issues: { mode: "ask" },
    })

    expect(issues.map((issue) => issue.message)).toEqual(
      expect.arrayContaining([
        "Choose an existing default profile.",
        "Stage ID must be unique.",
        "Stage title is required.",
        "Deterministic condition is required.",
        "Human stages require a question.",
        "Use https://provider-origin|repository-id.",
        "Assignment references a missing profile.",
      ]),
    )
  })

  it("projects the exact global config shape and rejects the legacy wrapper", async () => {
    mockedRequest.mockResolvedValueOnce(validSnapshot)
    await expect(getPRLifecycleGateProfiles()).resolves.toEqual(validSnapshot)
    expect(mockedRequest).toHaveBeenCalledWith(
      "/api/pr-lifecycle/gate-profiles",
      undefined,
      undefined,
    )

    mockedRequest.mockResolvedValueOnce({
      profiles: validSnapshot.gate_profiles,
    })
    await expect(getPRLifecycleGateProfiles()).rejects.toThrow(
      "malformed_pr_lifecycle_gate_profiles",
    )
  })

  it("rejects unknown deferred issue automation modes", async () => {
    mockedRequest.mockResolvedValueOnce({
      ...validSnapshot,
      deferred_issues: { mode: "sometimes" },
    })
    await expect(getPRLifecycleGateProfiles()).rejects.toMatchObject({
      code: "malformed_pr_lifecycle_gate_profiles",
    })
  })
})
