import { describe, expect, it } from "vitest"

import {
  normalizePullRequestsSearch,
  pullRequestsSearchIsCanonical,
} from "@/routes/pull-requests"

const workspaceID = `prw_${"a".repeat(32)}`
const gate = "pr.review.complete" as const

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

  it("keeps one allowlisted gate in the gate-profile view", () => {
    expect(
      normalizePullRequestsSearch({
        view: "gate-profiles",
        gate,
      }),
    ).toEqual({ view: "gate-profiles", gate })
  })

  it("scrubs invalid, repeated, and out-of-context gates", () => {
    expect(
      normalizePullRequestsSearch({
        view: "gate-profiles",
        gate: "pr.not-a-gate",
      }),
    ).toEqual({ view: "gate-profiles" })
    expect(
      normalizePullRequestsSearch({
        view: "gate-profiles",
        gate: [gate],
      }),
    ).toEqual({ view: "gate-profiles" })
    expect(normalizePullRequestsSearch({ gate })).toEqual({})
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
    expect(
      pullRequestsSearchIsCanonical(
        { view: "gate-profiles", gate },
        { view: "gate-profiles", gate },
      ),
    ).toBe(true)
  })
})
