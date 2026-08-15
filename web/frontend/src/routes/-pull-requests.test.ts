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

  it("uses the gate-profile view without a profile as the list", () => {
    expect(
      normalizePullRequestsSearch({
        view: "gate-profiles",
        workspace: workspaceID,
        config_revision: "private",
      }),
    ).toEqual({ view: "gate-profiles" })
  })

  it("keeps one valid profile as the editor", () => {
    expect(
      normalizePullRequestsSearch({
        view: "gate-profiles",
        profile: "release_candidate-1",
      }),
    ).toEqual({ view: "gate-profiles", profile: "release_candidate-1" })
  })

  it("keeps an allowlisted gate with its profile as the modal", () => {
    expect(
      normalizePullRequestsSearch({
        view: "gate-profiles",
        profile: "strict",
        gate,
      }),
    ).toEqual({ view: "gate-profiles", profile: "strict", gate })
  })

  it("attaches old gate-only deep links to the default profile", () => {
    expect(
      normalizePullRequestsSearch({
        view: "gate-profiles",
        gate,
      }),
    ).toEqual({ view: "gate-profiles", profile: "default", gate })
  })

  it("scrubs invalid, repeated, and out-of-context profile state", () => {
    expect(
      normalizePullRequestsSearch({
        view: "gate-profiles",
        profile: "Invalid Profile",
        gate: "pr.not-a-gate",
      }),
    ).toEqual({ view: "gate-profiles" })
    expect(
      normalizePullRequestsSearch({
        view: "gate-profiles",
        profile: ["default"],
        gate: [gate],
      }),
    ).toEqual({ view: "gate-profiles" })
    expect(
      normalizePullRequestsSearch({
        view: "gate-profiles",
        profile: `a${"b".repeat(64)}`,
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
        { view: "gate-profiles", profile: "default", gate },
        { view: "gate-profiles", profile: "default", gate },
      ),
    ).toBe(true)
    expect(
      pullRequestsSearchIsCanonical(
        { view: "gate-profiles", gate },
        { view: "gate-profiles", profile: "default", gate },
      ),
    ).toBe(false)
  })
})
