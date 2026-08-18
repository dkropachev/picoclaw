import { describe, expect, it } from "vitest"

import { normalizePRGateConfigsSearch } from "@/routes/pull-requests_.gate-configs"
import { normalizePRGateConfigEditorSearch } from "@/routes/pull-requests_.gate-configs.$configID"
import { normalizePRLifecycleSettingsSearch } from "@/routes/pull-requests_.settings"

const gate = "pr.review.complete" as const
const workspaceID = `prw_${"a".repeat(32)}`

describe("pull request Gate configurations search", () => {
  it("keeps only the discard modal identity on the configuration list", () => {
    expect(
      normalizePRGateConfigsSearch({ from: workspaceID, dialog: "discard" }),
    ).toEqual({
      from: workspaceID,
      dialog: "discard",
    })
    expect(
      normalizePRGateConfigsSearch({
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
      normalizePRGateConfigsSearch({
        from: [workspaceID],
        dialog: ["discard"],
      }),
    ).toEqual({})
    expect(
      normalizePRGateConfigsSearch({ from: "prw_INVALID", dialog: true }),
    ).toEqual({})
  })
})

describe("pull request Gate configuration editor search", () => {
  it("defaults to review and keeps a discard prompt owned by its gate", () => {
    expect(normalizePRGateConfigEditorSearch({})).toEqual({ flow: "review" })
    expect(normalizePRGateConfigEditorSearch({ gate })).toEqual({
      flow: "review",
      gate,
    })
    expect(
      normalizePRGateConfigEditorSearch({
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
      normalizePRGateConfigEditorSearch({
        flow: "implementation",
        gate,
        dialog: "discard",
      }),
    ).toEqual({ flow: "implementation", gate, dialog: "discard" })
  })

  it("scrubs legacy, unknown, malformed, and repeated state", () => {
    expect(
      normalizePRGateConfigEditorSearch({
        view: "retired",
        profile: "strict",
        workspace: workspaceID,
      }),
    ).toEqual({ flow: "review" })
    expect(
      normalizePRGateConfigEditorSearch({
        flow: "development",
        gate: "pr.not-a-gate",
        dialog: "delete",
      }),
    ).toEqual({ flow: "review" })
    expect(
      normalizePRGateConfigEditorSearch({
        flow: ["implementation"],
        from: [workspaceID],
        gate: [gate],
        dialog: ["discard"],
      }),
    ).toEqual({ flow: "review" })
  })
})

describe("pull request lifecycle settings search", () => {
  it("uses nudging by default and accepts each settings tab", () => {
    expect(normalizePRLifecycleSettingsSearch({})).toEqual({ tab: "nudging" })
    expect(normalizePRLifecycleSettingsSearch({ tab: "nudging" })).toEqual({
      tab: "nudging",
    })
    expect(normalizePRLifecycleSettingsSearch({ tab: "scope" })).toEqual({
      tab: "scope",
    })
    expect(normalizePRLifecycleSettingsSearch({ tab: "deferred" })).toEqual({
      tab: "deferred",
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
