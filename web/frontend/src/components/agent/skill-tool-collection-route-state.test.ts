import { describe, expect, it } from "vitest"

import {
  normalizeSkillsCollectionSearch,
  normalizeToolsCollectionSearch,
  skillToolCollectionSearchIsCanonical,
  skillsDefaultQuery,
  toolsDefaultQuery,
} from "./skill-tool-collection-route-state"

describe("skills and tools collection route state", () => {
  it("defaults both collections to compact List-compatible queries", () => {
    expect(normalizeSkillsCollectionSearch({})).toEqual({
      q: skillsDefaultQuery,
    })
    expect(normalizeToolsCollectionSearch({})).toEqual({
      q: toolsDefaultQuery,
    })
  })

  it("keeps only canonical q/view state and hard-cuts legacy tool tabs", () => {
    const normalized = normalizeToolsCollectionSearch({
      q: " status = enabled ",
      view: "grid",
      tab: "web-search",
    })
    expect(normalized).toEqual({ q: "status = enabled", view: "grid" })
    expect(
      skillToolCollectionSearchIsCanonical(
        { ...normalized, tab: "web-search" },
        normalized,
      ),
    ).toBe(false)
    expect(skillToolCollectionSearchIsCanonical(normalized, normalized)).toBe(
      true,
    )
  })

  it("drops unsupported views instead of preserving invalid URL state", () => {
    expect(
      normalizeSkillsCollectionSearch({ q: "origin = builtin", view: "cards" }),
    ).toEqual({ q: "origin = builtin" })
  })
})
