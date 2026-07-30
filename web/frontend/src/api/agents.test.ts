import { beforeEach, describe, expect, it, vi } from "vitest"

import {
  AgentsAPIError,
  createAgent,
  deleteAgent,
  getAgent,
  getAgents,
  setDefaultAgent,
  updateAgent,
} from "@/api/agents"
import { launcherFetch } from "@/api/http"

vi.mock("@/api/http", () => ({
  launcherFetch: vi.fn(),
}))

const mockedLauncherFetch = vi.mocked(launcherFetch)

const response = {
  agents: [
    {
      id: "main",
      name: "",
      workspace: "",
      model: null,
      skills: null,
      subagents: null,
      is_default: true,
      default_configured: false,
      implicit: true,
    },
  ],
  default_agent_id: "main",
  config_revision: "revision-1",
  effects: {
    launcher_effect: "applied" as const,
    catalog_effect: "applied" as const,
    gateway_effect: "applied" as const,
  },
}

const agent = {
  id: "reviewer",
  name: "Reviewer",
  workspace: "",
  model: {
    primary: "",
    fallbacks: [] as string[],
  },
  skills: ["review"],
  subagents: {
    allow_agents: ["main"],
  },
}

describe("agents API", () => {
  beforeEach(() => {
    mockedLauncherFetch.mockReset()
  })

  it("lists agents and fetches one using encoded IDs", async () => {
    mockedLauncherFetch
      .mockResolvedValueOnce(jsonResponse(response))
      .mockResolvedValueOnce(
        jsonResponse({
          agent: response.agents[0],
          default_agent_id: response.default_agent_id,
          config_revision: response.config_revision,
          effects: response.effects,
        }),
      )

    await expect(getAgents()).resolves.toEqual(response)
    await expect(getAgent("reviewer/one")).resolves.toEqual({
      agent: response.agents[0],
      default_agent_id: response.default_agent_id,
      config_revision: response.config_revision,
      effects: response.effects,
    })

    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      1,
      "/api/agents",
      undefined,
    )
    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      2,
      "/api/agents/reviewer%2Fone",
      undefined,
    )
  })

  it("preserves model null versus explicit empty fallbacks in create and update bodies", async () => {
    mockedLauncherFetch
      .mockResolvedValueOnce(jsonResponse(response, 201))
      .mockResolvedValueOnce(jsonResponse(response))

    await createAgent("revision-1", agent)
    await updateAgent("reviewer", "revision-2", {
      ...agent,
      model: null,
      skills: null,
      subagents: null,
    })

    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(1, "/api/agents", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        expected_config_revision: "revision-1",
        agent,
      }),
    })
    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      2,
      "/api/agents/reviewer",
      {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          expected_config_revision: "revision-2",
          agent: {
            ...agent,
            model: null,
            skills: null,
            subagents: null,
          },
        }),
      },
    )

    const createBody = JSON.parse(
      (mockedLauncherFetch.mock.calls[0][1]?.body as string) ?? "{}",
    )
    expect(createBody.agent.model.fallbacks).toEqual([])
    const updateBody = JSON.parse(
      (mockedLauncherFetch.mock.calls[1][1]?.body as string) ?? "{}",
    )
    expect(updateBody.agent.model).toBeNull()
  })

  it("sends CAS revisions for default and delete mutations", async () => {
    mockedLauncherFetch
      .mockResolvedValueOnce(jsonResponse(response))
      .mockResolvedValueOnce(jsonResponse(response))

    await setDefaultAgent("reviewer", "revision-2")
    await deleteAgent("reviewer", "revision-3")

    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      1,
      "/api/agents/reviewer/default",
      {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: '{"expected_config_revision":"revision-2"}',
      },
    )
    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      2,
      "/api/agents/reviewer",
      {
        method: "DELETE",
        headers: { "Content-Type": "application/json" },
        body: '{"expected_config_revision":"revision-3"}',
      },
    )
  })

  it("exposes conflicts and structured deletion blockers without retrying", async () => {
    mockedLauncherFetch.mockResolvedValueOnce(
      jsonResponse(
        {
          error: "agent_referenced",
          blockers: [
            { kind: "dispatch_rule", name: "review" },
            { kind: "delegation", agent_id: "main", ignored: "not projected" },
          ],
        },
        409,
      ),
    )

    const promise = deleteAgent("reviewer", "stale-revision")
    await expect(promise).rejects.toEqual(
      expect.objectContaining({
        name: "AgentsAPIError",
        status: 409,
        code: "agent_referenced",
        blockers: [
          { kind: "dispatch_rule", name: "review" },
          { kind: "delegation", agent_id: "main" },
        ],
      }),
    )
    await expect(promise).rejects.toBeInstanceOf(AgentsAPIError)
    expect(mockedLauncherFetch).toHaveBeenCalledTimes(1)
  })

  it("uses a safe error token as the code", async () => {
    mockedLauncherFetch.mockResolvedValueOnce(
      jsonResponse({ error: "config_revision_mismatch" }, 409),
    )

    await expect(setDefaultAgent("main", "old")).rejects.toMatchObject({
      status: 409,
      code: "config_revision_mismatch",
      message: "config_revision_mismatch",
    })
  })
})

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}
