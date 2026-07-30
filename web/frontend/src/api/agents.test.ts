import { beforeEach, describe, expect, it, vi } from "vitest"

import {
  AgentsAPIError,
  createAgent,
  deleteAgent,
  getAgent,
  getAgentActivity,
  getAgentCapabilities,
  getAgents,
  patchAgentCapabilities,
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

  it("fetches and patches capability policies with only caller-provided fields", async () => {
    const capabilities = capabilityResponse()
    const responseWithCanaries = {
      ...capabilities,
      capabilities: {
        ...capabilities.capabilities,
        tools: {
          ...capabilities.capabilities.tools,
          raw_policy: "CANARY_RAW_POLICY",
        },
        raw_frontmatter: "CANARY_RAW_FRONTMATTER",
      },
      catalogs: {
        ...capabilities.catalogs,
        tools: capabilities.catalogs.tools.map((tool) => ({
          ...tool,
          config_key: "CANARY_CONFIG_KEY",
        })),
        raw_config: "CANARY_RAW_CONFIG",
      },
      effects: {
        ...capabilities.effects,
        command: "CANARY_COMMAND",
      },
      raw_document: "CANARY_RAW_DOCUMENT",
    }
    mockedLauncherFetch
      .mockResolvedValueOnce(jsonResponse(responseWithCanaries))
      .mockResolvedValueOnce(jsonResponse(responseWithCanaries))

    const fetched = await getAgentCapabilities("reviewer")
    expect(fetched).toEqual(capabilities)
    expect(JSON.stringify(fetched)).not.toContain("CANARY_")
    const patched = await patchAgentCapabilities("reviewer", {
      expected_revision: "capability-revision-1",
      tools: { mode: "none", values: [] },
    })
    expect(patched).toEqual(capabilities)
    expect(JSON.stringify(patched)).not.toContain("CANARY_")

    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      1,
      "/api/agents/reviewer/capabilities",
      { signal: undefined },
    )
    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      2,
      "/api/agents/reviewer/capabilities",
      {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          expected_revision: "capability-revision-1",
          tools: { mode: "none", values: [] },
        }),
        signal: undefined,
      },
    )
  })

  it("rejects malformed and cross-agent capability responses", async () => {
    const capabilities = capabilityResponse()
    mockedLauncherFetch
      .mockResolvedValueOnce(
        jsonResponse({
          ...capabilities,
          capabilities: {
            ...capabilities.capabilities,
            tools: { mode: "selected", values: [] },
          },
        }),
      )
      .mockResolvedValueOnce(
        jsonResponse({ ...capabilities, agent_id: "writer" }),
      )
      .mockResolvedValueOnce(
        jsonResponse({ ...capabilities, agent_id: "writer" }),
      )

    await expect(getAgentCapabilities("reviewer")).rejects.toThrow(
      "invalid_agent_capabilities_response",
    )
    await expect(getAgentCapabilities("reviewer")).rejects.toThrow(
      "invalid_agent_capabilities_response",
    )
    await expect(
      patchAgentCapabilities("reviewer", {
        expected_revision: "capability-revision-1",
        tools: { mode: "none", values: [] },
      }),
    ).rejects.toThrow("invalid_agent_capabilities_response")
  })

  it("strictly projects activity and drops unapproved sensitive canary fields", async () => {
    mockedLauncherFetch.mockResolvedValueOnce(
      jsonResponse({
        agent_id: "reviewer",
        events: [
          {
            sequence: "18446744073709551615",
            agent_id: "reviewer",
            timestamp: "2026-07-30T12:00:00.000000001Z",
            kind: "agent.tool.exec_end",
            severity: "info",
            details: {
              tool_name: "web_search",
              duration_ms: "25",
              is_error: false,
              async: true,
              arguments: "CANARY_ARGUMENT_SECRET",
              result: "CANARY_RESULT_SECRET",
            },
            prompt: "CANARY_PROMPT_SECRET",
            error: "CANARY_ERROR_SECRET",
          },
        ],
        next_cursor: "opaque-cursor",
        reset: true,
        truncated: true,
        dropped: {
          subscription: "1",
          retention: "2",
          projection: "3",
          secret_counter: "CANARY_COUNTER_SECRET",
        },
        raw_payload: "CANARY_RAW_SECRET",
      }),
    )

    const projected = await getAgentActivity("reviewer", {
      cursor: "cursor/one",
      limit: 100,
    })

    expect(mockedLauncherFetch).toHaveBeenCalledWith(
      "/api/agents/reviewer/activity?limit=100&cursor=cursor%2Fone",
      { signal: undefined },
    )
    expect(projected).toEqual({
      agent_id: "reviewer",
      events: [
        {
          sequence: "18446744073709551615",
          agent_id: "reviewer",
          timestamp: "2026-07-30T12:00:00.000000001Z",
          kind: "agent.tool.exec_end",
          severity: "info",
          details: {
            tool_name: "web_search",
            duration_ms: "25",
            is_error: false,
            async: true,
          },
        },
      ],
      next_cursor: "opaque-cursor",
      reset: true,
      truncated: true,
      dropped: {
        subscription: "1",
        retention: "2",
        projection: "3",
      },
    })
    expect(JSON.stringify(projected)).not.toContain("CANARY_")
  })

  it("rejects unrecognized activity kinds instead of exposing generic details", async () => {
    mockedLauncherFetch.mockResolvedValueOnce(
      jsonResponse({
        agent_id: "reviewer",
        events: [
          {
            sequence: "1",
            agent_id: "reviewer",
            timestamp: "2026-07-30T12:00:00Z",
            kind: "agent.unknown",
            severity: "info",
            details: { text: "CANARY_SECRET" },
          },
        ],
        next_cursor: "",
        reset: false,
        truncated: false,
        dropped: {
          subscription: "0",
          retention: "0",
          projection: "0",
        },
      }),
    )

    await expect(getAgentActivity("reviewer")).rejects.toThrow(
      "invalid_agent_activity_response",
    )
  })
})

function capabilityResponse() {
  return {
    agent_id: "reviewer",
    source: "agent" as const,
    editable: true,
    issue_code: "",
    legacy_upgrade_required: false,
    capabilities: {
      tools: { mode: "all" as const, values: [] },
      skills: {
        mode: "inherit" as const,
        values: [],
        inherited_values: ["review"],
      },
      mcp_servers: { mode: "none" as const, values: [] },
    },
    catalogs: {
      tools: [
        {
          name: "web_search",
          description: "Search",
          category: "web",
          status: "enabled",
          reason_code: "",
        },
      ],
      skills: [{ name: "review", source: "workspace" }],
      mcp_servers: [{ name: "github", enabled: true }],
    },
    catalog_truncated: {
      tools: false,
      skills: false,
      mcp_servers: false,
    },
    revision: "capability-revision-1",
    config_revision: "config-revision-1",
    effects: response.effects,
  }
}

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}
