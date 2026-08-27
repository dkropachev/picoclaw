import { beforeEach, describe, expect, it, vi } from "vitest"

import { type SkillSupportItem, listSkills } from "@/api/skills"

import {
  findWorkspaceSkill,
  indexWorkspaceSkills,
  loadSkillInventory,
} from "./skill-inventory"

vi.mock("@/api/skills", () => ({ listSkills: vi.fn() }))

const mockedListSkills = vi.mocked(listSkills)

describe("Hub skill inventory", () => {
  beforeEach(() => mockedListSkills.mockReset())

  it("follows every collection cursor before indexing installed skills", async () => {
    const firstSkill = skill({ id: "first", name: "first" })
    const lateSkill = skill({ id: "late", name: "late" })
    mockedListSkills
      .mockResolvedValueOnce(page([firstSkill], "page-two", 2))
      .mockResolvedValueOnce(page([lateSkill], undefined, 2))
    const controller = new AbortController()

    const inventory = await loadSkillInventory(controller.signal)
    const installed = indexWorkspaceSkills(inventory)

    expect(mockedListSkills).toHaveBeenNthCalledWith(
      1,
      { cursor: undefined, limit: 200 },
      controller.signal,
    )
    expect(mockedListSkills).toHaveBeenNthCalledWith(
      2,
      { cursor: "page-two", limit: 200 },
      controller.signal,
    )
    expect(installed.get("late")).toEqual(lateSkill)
  })

  it("rejects repeated cursors instead of looping indefinitely", async () => {
    mockedListSkills
      .mockResolvedValueOnce(page([], "repeated", 1))
      .mockResolvedValueOnce(page([], "repeated", 1))

    await expect(loadSkillInventory()).rejects.toThrow("repeated cursor")
    expect(mockedListSkills).toHaveBeenCalledTimes(2)
  })

  it("honors cancellation before issuing another inventory request", async () => {
    const controller = new AbortController()
    controller.abort(new Error("inventory cancelled"))

    await expect(loadSkillInventory(controller.signal)).rejects.toThrow(
      "inventory cancelled",
    )
    expect(mockedListSkills).not.toHaveBeenCalled()
  })

  it("excludes non-workspace skills from the installed lookup", () => {
    const builtin = skill({
      id: "builtin",
      name: "builtin",
      source: "builtin",
      removable: false,
    })

    expect(indexWorkspaceSkills([builtin]).size).toBe(0)
  })

  it("matches backend skill identity case-insensitively", () => {
    const installed = skill({ name: "Review-Helper" })
    const index = indexWorkspaceSkills([installed])

    expect(findWorkspaceSkill(index, "  REVIEW-helper ")).toEqual(installed)
    expect([...index.keys()]).toEqual(["review-helper"])
  })
})

function page(
  skills: SkillSupportItem[],
  nextCursor: string | undefined,
  total: number,
) {
  return {
    skills,
    total,
    ...(nextCursor ? { next_cursor: nextCursor } : {}),
    canonical_query: "ORDER BY name ASC",
    query_schema: { fields: [] },
  }
}

function skill(overrides: Partial<SkillSupportItem> = {}): SkillSupportItem {
  return {
    id: "review-helper",
    name: "review-helper",
    path: "/workspace/skills/review-helper/SKILL.md",
    source: "workspace",
    description: "Review pull requests.",
    origin: "manual",
    origin_kind: "manual",
    removable: true,
    ...overrides,
  }
}
