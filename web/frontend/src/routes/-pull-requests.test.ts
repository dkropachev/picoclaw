import { describe, expect, it } from "vitest"

import {
  normalizePullRequestsSearch,
  pullRequestsSearchIsCanonical,
} from "@/routes/pull-requests"

const workspaceID = `prw_${"a".repeat(32)}`

describe("pull requests route search", () => {
  it("keeps only one valid public workspace selection", () => {
    expect(
      normalizePullRequestsSearch({
        workspace: workspaceID,
        cursor: "server-owned",
        prompt: "private",
      }),
    ).toEqual({ workspace: workspaceID })
  })

  it("uses one canonical gate-profile view", () => {
    expect(
      normalizePullRequestsSearch({
        view: "gate-profiles",
        workspace: workspaceID,
        config_revision: "private",
      }),
    ).toEqual({ view: "gate-profiles" })
  })

  it("rejects legacy, malformed, repeated, and unknown route state", () => {
    for (const raw of [
      { view: "review", case: `prc_${"b".repeat(32)}` },
      { view: "development", case: `pdc_${"c".repeat(32)}` },
      { view: ["gate-profiles"] },
      { workspace: `prw_${"A".repeat(32)}` },
      { workspace: [workspaceID] },
    ]) {
      expect(normalizePullRequestsSearch(raw)).toEqual({})
    }
  })

  it("detects unknown or sensitive noncanonical state", () => {
    expect(
      pullRequestsSearchIsCanonical(
        { workspace: workspaceID, cursor: "opaque" },
        { workspace: workspaceID },
      ),
    ).toBe(false)
    expect(
      pullRequestsSearchIsCanonical(
        { view: "gate-profiles" },
        { view: "gate-profiles" },
      ),
    ).toBe(true)
  })
})
