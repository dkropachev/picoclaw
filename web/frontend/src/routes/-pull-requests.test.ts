import { describe, expect, it } from "vitest"

import { normalizePRProfilesSearch } from "@/routes/pull-requests_.profiles"
import { normalizePRProfileEditorSearch } from "@/routes/pull-requests_.profiles.$profileID"
import { normalizePRLifecycleSettingsSearch } from "@/routes/pull-requests_.settings"

const gate = "pr.review.complete" as const
const workspaceID = `prw_${"a".repeat(32)}`

describe("pull request profiles search", () => {
  it("keeps only the discard modal identity on the profile list", () => {
    expect(
      normalizePRProfilesSearch({ from: workspaceID, dialog: "discard" }),
    ).toEqual({
      from: workspaceID,
      dialog: "discard",
    })
    expect(
      normalizePRProfilesSearch({
        dialog: "other",
        flow: "implementation",
        gate,
        profile: "strict",
        view: "gate-profiles",
      }),
    ).toEqual({})
  })

  it("rejects repeated and non-string modal values", () => {
    expect(
      normalizePRProfilesSearch({
        from: [workspaceID],
        dialog: ["discard"],
      }),
    ).toEqual({})
    expect(
      normalizePRProfilesSearch({ from: "prw_INVALID", dialog: true }),
    ).toEqual({})
  })
})

describe("pull request profile editor search", () => {
  it("defaults to review and keeps a discard prompt owned by its gate", () => {
    expect(normalizePRProfileEditorSearch({})).toEqual({ flow: "review" })
    expect(normalizePRProfileEditorSearch({ gate })).toEqual({
      flow: "review",
      gate,
    })
    expect(
      normalizePRProfileEditorSearch({
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
      normalizePRProfileEditorSearch({
        flow: "implementation",
        gate,
        dialog: "discard",
      }),
    ).toEqual({ flow: "implementation", gate, dialog: "discard" })
  })

  it("scrubs legacy, unknown, malformed, and repeated state", () => {
    expect(
      normalizePRProfileEditorSearch({
        view: "gate-profiles",
        profile: "strict",
        workspace: workspaceID,
      }),
    ).toEqual({ flow: "review" })
    expect(
      normalizePRProfileEditorSearch({
        flow: "development",
        gate: "pr.not-a-gate",
        dialog: "delete",
      }),
    ).toEqual({ flow: "review" })
    expect(
      normalizePRProfileEditorSearch({
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
        view: "gate-profiles",
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
