import { beforeEach, describe, expect, it, vi } from "vitest"

import { launcherFetch } from "@/api/http"
import {
  getTool,
  getToolAdaptation,
  listTools,
  runToolAdaptationProbe,
} from "@/api/tools"

vi.mock("@/api/http", () => ({
  launcherFetch: vi.fn(),
}))

const mockedLauncherFetch = vi.mocked(launcherFetch)

describe("tools API", () => {
  beforeEach(() => {
    mockedLauncherFetch.mockReset()
  })

  it("sends a targeted adaptation probe", async () => {
    mockedLauncherFetch.mockResolvedValueOnce(
      jsonResponse({
        profile: { provider: "openai", model: "gpt-5.4" },
        visible_tool_surface: "codex",
        tool_name: "exec_command",
        success: true,
        duration_ms: 12,
        ran_at: "2026-07-28T12:00:00Z",
      }),
    )

    await runToolAdaptationProbe({
      account_ref: "openai-work",
      model_alias: "coding",
    })

    expect(mockedLauncherFetch).toHaveBeenCalledWith(
      "/api/tools/adaptation/probe",
      {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          account_ref: "openai-work",
          model_alias: "coding",
        }),
      },
    )
  })

  it("uses collection paging and backend-issued tool identities", async () => {
    mockedLauncherFetch
      .mockResolvedValueOnce(
        jsonResponse({
          tools: [],
          total: 0,
          canonical_query: "category = search ORDER BY name ASC",
          query_schema: { fields: [] },
        }),
      )
      .mockResolvedValueOnce(
        jsonResponse({
          tool: { id: "tool_d2ViL3NlYXJjaA", name: "web/search" },
        }),
      )
    const controller = new AbortController()

    await listTools(
      {
        query: "category = search ORDER BY name ASC",
        cursor: "next+/page",
        limit: 25,
      },
      controller.signal,
    )
    await getTool("tool_d2ViL3NlYXJjaA")

    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      1,
      "/api/tools?query=category+%3D+search+ORDER+BY+name+ASC&cursor=next%2B%2Fpage&limit=25",
      { signal: controller.signal },
    )
    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      2,
      "/api/tools/tool_d2ViL3NlYXJjaA",
      undefined,
    )
  })

  it("preserves plain-text API errors", async () => {
    mockedLauncherFetch.mockResolvedValueOnce(
      new Response(
        'failed to create probe provider: provider "router" is a router config\n',
        {
          status: 400,
          statusText: "Bad Request",
          headers: { "Content-Type": "text/plain" },
        },
      ),
    )

    await expect(getToolAdaptation()).rejects.toThrow(
      'failed to create probe provider: provider "router" is a router config',
    )
  })
})

function jsonResponse(body: unknown) {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  })
}
