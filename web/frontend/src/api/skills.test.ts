import { beforeEach, describe, expect, it, vi } from "vitest"

import { launcherFetch } from "@/api/http"
import {
  bulkDeleteSkills,
  deleteSkill,
  getSkill,
  listSkills,
} from "@/api/skills"

vi.mock("@/api/http", () => ({ launcherFetch: vi.fn() }))

const mockedLauncherFetch = vi.mocked(launcherFetch)

describe("skills collection API", () => {
  beforeEach(() => mockedLauncherFetch.mockReset())

  it("sends bounded collection paging state and its abort signal", async () => {
    mockedLauncherFetch.mockResolvedValueOnce(
      jsonResponse({
        skills: [],
        total: 0,
        canonical_query: "source = workspace ORDER BY name ASC",
        query_schema: { fields: [] },
      }),
    )
    const controller = new AbortController()

    await listSkills(
      {
        query: "source = workspace ORDER BY name ASC",
        cursor: "opaque+/cursor",
        limit: 50,
      },
      controller.signal,
    )

    expect(mockedLauncherFetch).toHaveBeenCalledWith(
      "/api/skills?query=source+%3D+workspace+ORDER+BY+name+ASC&cursor=opaque%2B%2Fcursor&limit=50",
      { signal: controller.signal },
    )
  })

  it("treats detail and bulk identities as backend-issued opaque values", async () => {
    mockedLauncherFetch
      .mockResolvedValueOnce(
        jsonResponse({
          id: "skill_YWJjL2RlZg",
          name: "review helper",
          content: "# Review",
        }),
      )
      .mockResolvedValueOnce(
        jsonResponse({
          deleted_ids: ["skill_YWJjL2RlZg"],
          failures: [],
        }),
      )

    await getSkill("skill_YWJjL2RlZg")
    await bulkDeleteSkills(["skill_YWJjL2RlZg"])

    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      1,
      "/api/skills/skill_YWJjL2RlZg",
      undefined,
    )
    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      2,
      "/api/skills/bulk-delete",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({ ids: ["skill_YWJjL2RlZg"] }),
      }),
    )
  })

  it("surfaces structured collection errors from item deletion", async () => {
    mockedLauncherFetch.mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          code: "read_only_origin",
          message: "Only workspace skills can be deleted",
        }),
        { status: 409, headers: { "Content-Type": "application/json" } },
      ),
    )

    await expect(deleteSkill("opaque-skill-id")).rejects.toMatchObject({
      name: "CollectionAPIError",
      status: 409,
      code: "read_only_origin",
      message: "Only workspace skills can be deleted",
    })
  })
})

function jsonResponse(body: unknown) {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  })
}
