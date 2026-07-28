import { beforeEach, describe, expect, it, vi } from "vitest"

import { launcherFetch } from "@/api/http"
import { getToolAdaptation, runToolAdaptationProbe } from "@/api/tools"

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
        tool_name: "update_plan",
        success: true,
        duration_ms: 12,
        ran_at: "2026-07-28T12:00:00Z",
      }),
    )

    await runToolAdaptationProbe({
      provider: "openai",
      model: "gpt-5.4",
    })

    expect(mockedLauncherFetch).toHaveBeenCalledWith(
      "/api/tools/adaptation/probe",
      {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          provider: "openai",
          model: "gpt-5.4",
        }),
      },
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
