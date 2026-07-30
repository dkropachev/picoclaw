import { describe, expect, it } from "vitest"

import {
  agentsSearchIsCanonical,
  normalizeAgentsSearch,
} from "./agent-route-search"

describe("agent route search", () => {
  it("keeps exact canonical agent IDs and explicit supported tabs", () => {
    expect(
      normalizeAgentsSearch({ agent: "reviewer_2", tab: "capabilities" }),
    ).toEqual({ agent: "reviewer_2", tab: "capabilities" })
  })

  it("does not trim or lowercase agent IDs", () => {
    expect(
      normalizeAgentsSearch({ agent: " reviewer ", tab: "activity" }),
    ).toEqual({})
    expect(
      normalizeAgentsSearch({ agent: "Reviewer", tab: "activity" }),
    ).toEqual({})
  })

  it("defaults a selected agent to overview and removes invalid or stray search", () => {
    expect(normalizeAgentsSearch({ agent: "reviewer", tab: "other" })).toEqual({
      agent: "reviewer",
      tab: "overview",
    })
    expect(normalizeAgentsSearch({ tab: "activity", q: "secret" })).toEqual({})
    expect(
      normalizeAgentsSearch({
        agent: ["reviewer", "writer"],
        tab: "overview",
      }),
    ).toEqual({})
    expect(
      normalizeAgentsSearch({
        agent: "reviewer",
        tab: ["overview", "activity"],
      }),
    ).toEqual({ agent: "reviewer", tab: "overview" })
  })

  it("requires the exact normalized key set", () => {
    const normalized = { agent: "reviewer", tab: "overview" } as const
    expect(agentsSearchIsCanonical(normalized, normalized)).toBe(true)
    expect(
      agentsSearchIsCanonical(
        { ...normalized, unexpected: "discard-me" },
        normalized,
      ),
    ).toBe(false)
  })
})
