import { describe, expect, it } from "vitest"

import {
  normalizeRepositoryAssignmentsSearch,
  normalizeWorkflowConfigurationEditorSearch,
  normalizeWorkflowConfigurationsSearch,
} from "@/components/pr-workspaces/pr-lifecycle-collection-route-state"

describe("PR lifecycle administrative collection route state", () => {
  it("defaults both administrative collections to compact List queries", () => {
    expect(normalizeRepositoryAssignmentsSearch({})).toEqual({
      q: "ORDER BY repository ASC",
    })
    expect(normalizeWorkflowConfigurationsSearch({})).toEqual({
      q: "ORDER BY name ASC",
    })
  })

  it("keeps only canonical q/view collection state", () => {
    expect(
      normalizeRepositoryAssignmentsSearch({
        q: "configuration = strict ORDER BY repository ASC",
        view: "grid",
        config: "legacy",
      }),
    ).toEqual({
      q: "configuration = strict ORDER BY repository ASC",
      view: "grid",
    })
    expect(
      normalizeWorkflowConfigurationsSearch({
        q: "is_default = true ORDER BY name ASC",
        view: "table",
        config: "legacy",
        gate: "pr.review.complete",
      }),
    ).toEqual({
      q: "is_default = true ORDER BY name ASC",
      view: "table",
    })
  })

  it("adds bounded flow and Gate state only to the routed editor", () => {
    expect(
      normalizeWorkflowConfigurationEditorSearch({
        q: "name ~ auto ORDER BY name ASC",
        view: "grid",
        flow: "implementation",
        gate: "pr.implementation.publish",
        config: "legacy",
      }),
    ).toEqual({
      q: "name ~ auto ORDER BY name ASC",
      view: "grid",
      flow: "implementation",
      gate: "pr.implementation.publish",
    })
    expect(
      normalizeWorkflowConfigurationEditorSearch({
        flow: ["implementation"],
        gate: "../../secret",
      }),
    ).toEqual({ q: "ORDER BY name ASC", flow: "review" })
  })
})
