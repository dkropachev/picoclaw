import { describe, expect, it } from "vitest"

import { normalizePRRepositoryAssignmentsSearch } from "@/routes/pull-requests_.repository-assignments"
import { normalizePRLifecycleSettingsSearch } from "@/routes/pull-requests_.settings"
import { normalizePRWorkflowConfigurationsSearch } from "@/routes/pull-requests_.workflow-configurations"
import { normalizePRWorkflowConfigurationEditorSearch } from "@/routes/pull-requests_.workflow-configurations.$configurationID"

const gate = "pr.review.complete" as const
const workspaceID = `prw_${"a".repeat(32)}`

describe("pull request Workflow configurations search", () => {
  it("keeps only the discard modal identity on the configuration list", () => {
    expect(
      normalizePRWorkflowConfigurationsSearch({
        from: workspaceID,
        dialog: "discard",
      }),
    ).toEqual({
      from: workspaceID,
      dialog: "discard",
    })
    expect(
      normalizePRWorkflowConfigurationsSearch({
        dialog: "other",
        flow: "implementation",
        gate,
        profile: "strict",
        view: "retired",
      }),
    ).toEqual({})
  })

  it("rejects repeated and non-string modal values", () => {
    expect(
      normalizePRWorkflowConfigurationsSearch({
        from: [workspaceID],
        dialog: ["discard"],
      }),
    ).toEqual({})
    expect(
      normalizePRWorkflowConfigurationsSearch({
        from: "prw_INVALID",
        dialog: true,
      }),
    ).toEqual({})
  })
})

describe("pull request Workflow configuration editor search", () => {
  it("defaults to review and keeps a discard prompt owned by its gate", () => {
    expect(normalizePRWorkflowConfigurationEditorSearch({})).toEqual({
      flow: "review",
    })
    expect(normalizePRWorkflowConfigurationEditorSearch({ gate })).toEqual({
      flow: "review",
      gate,
    })
    expect(
      normalizePRWorkflowConfigurationEditorSearch({
        flow: "implementation",
        from: workspaceID,
        gate: "pr.implementation.scope",
      }),
    ).toEqual({
      flow: "implementation",
      from: workspaceID,
      gate: "pr.implementation.scope",
    })
    expect(
      normalizePRWorkflowConfigurationEditorSearch({
        flow: "implementation",
        gate,
        dialog: "discard",
      }),
    ).toEqual({ flow: "implementation", gate, dialog: "discard" })
  })

  it("scrubs legacy, unknown, malformed, and repeated state", () => {
    expect(
      normalizePRWorkflowConfigurationEditorSearch({
        view: "retired",
        profile: "strict",
        workspace: workspaceID,
      }),
    ).toEqual({ flow: "review" })
    expect(
      normalizePRWorkflowConfigurationEditorSearch({
        flow: "development",
        gate: "pr.not-a-gate",
        dialog: "delete",
      }),
    ).toEqual({ flow: "review" })
    expect(
      normalizePRWorkflowConfigurationEditorSearch({
        flow: ["implementation"],
        from: [workspaceID],
        gate: [gate],
        dialog: ["discard"],
      }),
    ).toEqual({ flow: "review" })
  })
})

describe("pull request repository assignments search", () => {
  it("keeps only its workspace origin and discard dialog", () => {
    expect(
      normalizePRRepositoryAssignmentsSearch({
        from: workspaceID,
        dialog: "discard",
        flow: "review",
        gate,
      }),
    ).toEqual({ from: workspaceID, dialog: "discard" })
    expect(
      normalizePRRepositoryAssignmentsSearch({
        from: [workspaceID],
        dialog: ["discard"],
      }),
    ).toEqual({})
  })
})

describe("pull request lifecycle settings search", () => {
  it("uses nudging by default and accepts the remaining settings tabs", () => {
    expect(normalizePRLifecycleSettingsSearch({})).toEqual({ tab: "nudging" })
    expect(normalizePRLifecycleSettingsSearch({ tab: "nudging" })).toEqual({
      tab: "nudging",
    })
    expect(normalizePRLifecycleSettingsSearch({ tab: "scope" })).toEqual({
      tab: "scope",
    })
    expect(normalizePRLifecycleSettingsSearch({ tab: "deferred" })).toEqual({
      tab: "nudging",
    })
  })

  it("keeps discard on its owning tab and scrubs all other route state", () => {
    expect(
      normalizePRLifecycleSettingsSearch({
        tab: "scope",
        from: workspaceID,
        dialog: "discard",
        view: "retired",
        gate,
      }),
    ).toEqual({ tab: "scope", from: workspaceID, dialog: "discard" })
    expect(
      normalizePRLifecycleSettingsSearch({
        tab: ["deferred"],
        from: [workspaceID],
        dialog: ["discard"],
        workspace: workspaceID,
      }),
    ).toEqual({ tab: "nudging" })
  })
})
