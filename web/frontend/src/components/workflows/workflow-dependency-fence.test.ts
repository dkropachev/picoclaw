import { describe, expect, it } from "vitest"

import type { WorkflowDependencyCheckResponse } from "@/api/workflows"

import {
  workflowDependencyFence,
  workflowDependencyFenceMessage,
} from "./workflow-dependency-fence"

const workflowRef = "workflows/review.yml"

function report(
  overrides: Partial<WorkflowDependencyCheckResponse> = {},
): WorkflowDependencyCheckResponse {
  return {
    root_ref: workflowRef,
    revision: "opaque:dependency/revision?exact",
    ready: true,
    workflow_enabled: true,
    structural_ready: true,
    runtime_ready: true,
    dependencies: [],
    structural_issues: [],
    ...overrides,
  }
}

describe("workflowDependencyFence", () => {
  it("returns the exact opaque revision only for a current ready exact-ref report", () => {
    expect(workflowDependencyFence(workflowRef, "current", report())).toEqual({
      status: "ready",
      revision: "opaque:dependency/revision?exact",
    })
    expect(
      workflowDependencyFence(
        workflowRef,
        "current",
        report({ revision: " opaque-kept-exact " }),
      ),
    ).toEqual({
      status: "ready",
      revision: " opaque-kept-exact ",
    })
  })

  it("fails closed for pending, mismatched, blank, unavailable, and blocked reports", () => {
    expect(workflowDependencyFence(workflowRef, "loading", report())).toEqual({
      status: "loading",
    })
    expect(
      workflowDependencyFence(
        workflowRef,
        "current",
        report({ root_ref: "workflows/other.yml" }),
      ),
    ).toEqual({ status: "stale" })
    expect(
      workflowDependencyFence(
        workflowRef,
        "current",
        report({ revision: " \n " }),
      ),
    ).toEqual({ status: "unavailable" })
    expect(workflowDependencyFence(workflowRef, "error")).toEqual({
      status: "unavailable",
    })
    expect(
      workflowDependencyFence(
        workflowRef,
        "current",
        report({ ready: false, runtime_ready: false }),
      ),
    ).toEqual({ status: "runtime-blocked" })
  })

  it("provides action-specific bounded readiness messages", () => {
    expect(workflowDependencyFenceMessage("run", { status: "loading" })).toBe(
      "Checking dependencies before running…",
    )
    expect(
      workflowDependencyFenceMessage("retry", {
        status: "structural-blocked",
      }),
    ).toBe("Resolve the structural dependency blockers before retrying.")
  })
})
