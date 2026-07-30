import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, waitFor, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { beforeEach, describe, expect, it, vi } from "vitest"

import {
  type AgentCapabilitiesResponse,
  AgentsAPIError,
  getAgentCapabilities,
  patchAgentCapabilities,
} from "@/api/agents"
import { showSaveSuccessOrRestartToast } from "@/lib/restart-required"
import { refreshGatewayState } from "@/store/gateway"

import { AgentCapabilitiesPanel } from "./agent-capabilities-panel"

vi.mock("@/api/agents", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/agents")>()
  return {
    ...actual,
    getAgentCapabilities: vi.fn(),
    patchAgentCapabilities: vi.fn(),
  }
})

vi.mock("@tanstack/react-router", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@tanstack/react-router")>()
  return {
    ...actual,
    useBlocker: vi.fn(() => ({
      status: "idle",
      proceed: vi.fn(),
      reset: vi.fn(),
    })),
  }
})

vi.mock("@/store/gateway", () => ({
  refreshGatewayState: vi.fn(),
}))

vi.mock("@/lib/restart-required", () => ({
  showSaveSuccessOrRestartToast: vi.fn(),
}))

describe("AgentCapabilitiesPanel", () => {
  beforeEach(() => {
    vi.mocked(getAgentCapabilities).mockReset()
    vi.mocked(patchAgentCapabilities).mockReset()
    vi.mocked(refreshGatewayState).mockReset()
    vi.mocked(showSaveSuccessOrRestartToast).mockReset()
    vi.mocked(refreshGatewayState).mockResolvedValue({
      status: "running",
      canStart: true,
      restartRequired: false,
    })
  })

  it("retains unknown existing values and sends only changed capability fields", async () => {
    const initial = capabilityResponse({
      capabilities: {
        tools: {
          mode: "selected",
          values: ["web_search", "legacy_unknown"],
        },
        skills: {
          mode: "inherit",
          values: [],
          inherited_values: ["review"],
        },
        mcp_servers: { mode: "all", values: [] },
      },
    })
    const saved = capabilityResponse({
      revision: "revision-2",
      capabilities: {
        ...initial.capabilities,
        tools: { mode: "selected", values: ["web_search"] },
      },
    })
    vi.mocked(getAgentCapabilities).mockResolvedValue(initial)
    vi.mocked(patchAgentCapabilities).mockResolvedValue(saved)
    const user = userEvent.setup()

    renderPanel()

    expect(await screen.findByText("Existing unknown selections")).toBeVisible()
    expect(screen.getByText("legacy_unknown")).toBeVisible()
    await user.click(
      screen.getByRole("button", {
        name: "Remove unknown selection legacy_unknown",
      }),
    )
    await user.click(screen.getByRole("button", { name: "Save capabilities" }))

    await waitFor(() =>
      expect(patchAgentCapabilities).toHaveBeenCalledWith("reviewer", {
        expected_revision: "revision-1",
        tools: { mode: "selected", values: ["web_search"] },
      }),
    )
    expect(showSaveSuccessOrRestartToast).toHaveBeenCalled()
  })

  it("case-folds existing skill selections against the catalog without duplicates", async () => {
    const base = capabilityResponse()
    const initial = capabilityResponse({
      capabilities: {
        ...base.capabilities,
        skills: {
          mode: "selected",
          values: ["Weather"],
          inherited_values: [],
        },
      },
      catalogs: {
        ...base.catalogs,
        skills: [{ name: "weather", source: "workspace" }],
      },
    })
    const saved = capabilityResponse({
      revision: "revision-2",
      capabilities: {
        ...initial.capabilities,
        skills: {
          mode: "selected",
          values: ["weather"],
          inherited_values: [],
        },
      },
      catalogs: initial.catalogs,
    })
    vi.mocked(getAgentCapabilities).mockResolvedValue(initial)
    vi.mocked(patchAgentCapabilities).mockResolvedValue(saved)
    const user = userEvent.setup()

    renderPanel()

    const skills = await screen.findByRole("group", { name: "Skills" })
    const weather = within(skills).getByRole("checkbox", { name: /weather/i })
    expect(weather).toBeChecked()
    expect(
      within(skills).queryByText("Existing unknown selections"),
    ).not.toBeInTheDocument()

    await user.click(weather)
    await user.click(weather)
    await user.click(screen.getByRole("button", { name: "Save capabilities" }))

    await waitFor(() =>
      expect(patchAgentCapabilities).toHaveBeenCalledWith("reviewer", {
        expected_revision: "revision-1",
        skills: { mode: "selected", values: ["weather"] },
      }),
    )
  })

  it("resends a safe retained unknown value when its policy changes", async () => {
    const unknown = `future_${"x".repeat(256)}`
    const initial = capabilityResponse({
      capabilities: {
        ...capabilityResponse().capabilities,
        tools: {
          mode: "selected",
          values: ["web_search", unknown],
        },
      },
    })
    vi.mocked(getAgentCapabilities).mockResolvedValue(initial)
    vi.mocked(patchAgentCapabilities).mockResolvedValue(
      capabilityResponse({
        revision: "revision-2",
        capabilities: {
          ...initial.capabilities,
          tools: {
            mode: "selected",
            values: ["web_search", unknown, "filesystem"],
          },
        },
      }),
    )
    const user = userEvent.setup()

    renderPanel()
    const tools = await screen.findByRole("group", { name: "Tools" })
    await user.click(
      within(tools).getByRole("checkbox", { name: /filesystem/i }),
    )
    await user.click(screen.getByRole("button", { name: "Save capabilities" }))

    await waitFor(() =>
      expect(patchAgentCapabilities).toHaveBeenCalledWith("reviewer", {
        expected_revision: "revision-1",
        tools: {
          mode: "selected",
          values: ["web_search", unknown, "filesystem"],
        },
      }),
    )
  })

  it("preserves a CAS-conflicted draft until explicit reload", async () => {
    const initial = capabilityResponse()
    const latest = capabilityResponse({
      revision: "revision-2",
      capabilities: {
        ...capabilityResponse().capabilities,
        tools: { mode: "selected", values: ["filesystem"] },
      },
    })
    vi.mocked(getAgentCapabilities)
      .mockResolvedValueOnce(initial)
      .mockResolvedValueOnce(latest)
    vi.mocked(patchAgentCapabilities).mockRejectedValue(
      new AgentsAPIError("capabilities_revision_mismatch", 409, {
        code: "capabilities_revision_mismatch",
      }),
    )
    const user = userEvent.setup()

    renderPanel()
    const tools = await screen.findByRole("group", { name: "Tools" })
    await user.click(within(tools).getByRole("radio", { name: "No tools" }))
    await user.click(screen.getByRole("button", { name: "Save capabilities" }))

    expect(within(tools).getByRole("radio", { name: "No tools" })).toBeChecked()
    const save = screen.getByRole("button", { name: "Save capabilities" })
    await waitFor(() => expect(save).toBeDisabled())
    await user.click(save)
    expect(patchAgentCapabilities).toHaveBeenCalledTimes(1)
    await user.click(
      await screen.findByRole("button", {
        name: "Reload latest capabilities",
      }),
    )

    await waitFor(() =>
      expect(
        within(tools).getByRole("radio", { name: "Selected tools" }),
      ).toBeChecked(),
    )
    expect(
      within(tools).getByRole("checkbox", { name: /filesystem/i }),
    ).toBeChecked()
  })

  it("keeps non-revision 409 save failures retryable", async () => {
    vi.mocked(getAgentCapabilities).mockResolvedValue(capabilityResponse())
    vi.mocked(patchAgentCapabilities).mockRejectedValue(
      new AgentsAPIError("capabilities_not_editable", 409, {
        code: "capabilities_not_editable",
      }),
    )
    const user = userEvent.setup()

    renderPanel()
    const tools = await screen.findByRole("group", { name: "Tools" })
    await user.click(within(tools).getByRole("radio", { name: "No tools" }))
    const save = screen.getByRole("button", { name: "Save capabilities" })
    await user.click(save)

    expect(
      await screen.findByText("Failed to save capabilities."),
    ).toBeVisible()
    expect(
      screen.queryByRole("button", { name: "Reload latest capabilities" }),
    ).not.toBeInTheDocument()
    expect(save).toBeEnabled()
    await user.click(save)
    expect(patchAgentCapabilities).toHaveBeenCalledTimes(2)
  })

  it("classifies legacy upgrade conflicts by exact revision code", async () => {
    const legacy = capabilityResponse({
      source: "legacy",
      editable: false,
      legacy_upgrade_required: true,
    })
    const latest = capabilityResponse({
      ...legacy,
      revision: "revision-2",
    })
    vi.mocked(getAgentCapabilities)
      .mockResolvedValueOnce(legacy)
      .mockResolvedValueOnce(latest)
    vi.mocked(patchAgentCapabilities).mockRejectedValue(
      new AgentsAPIError("capabilities_revision_mismatch", 409, {
        code: "capabilities_revision_mismatch",
      }),
    )
    const user = userEvent.setup()

    renderPanel()
    const upgrade = await screen.findByRole("button", {
      name: "Upgrade legacy file",
    })
    await user.click(upgrade)

    await waitFor(() => expect(upgrade).toBeDisabled())
    await user.click(upgrade)
    expect(patchAgentCapabilities).toHaveBeenCalledTimes(1)
    await user.click(
      screen.getByRole("button", { name: "Reload latest capabilities" }),
    )
    await waitFor(() => expect(upgrade).toBeEnabled())
  })

  it("keeps non-revision 409 legacy upgrade failures retryable", async () => {
    vi.mocked(getAgentCapabilities).mockResolvedValue(
      capabilityResponse({
        source: "legacy",
        editable: false,
        legacy_upgrade_required: true,
      }),
    )
    vi.mocked(patchAgentCapabilities).mockRejectedValue(
      new AgentsAPIError("capabilities_not_editable", 409, {
        code: "capabilities_not_editable",
      }),
    )
    const user = userEvent.setup()

    renderPanel()
    const upgrade = await screen.findByRole("button", {
      name: "Upgrade legacy file",
    })
    await user.click(upgrade)

    expect(
      await screen.findByText(
        "Failed to upgrade the legacy capabilities file.",
      ),
    ).toBeVisible()
    expect(
      screen.queryByRole("button", { name: "Reload latest capabilities" }),
    ).not.toBeInTheDocument()
    expect(upgrade).toBeEnabled()
    await user.click(upgrade)
    expect(patchAgentCapabilities).toHaveBeenCalledTimes(2)
  })

  it("requires an explicit legacy upgrade and keeps malformed files read-only", async () => {
    const legacy = capabilityResponse({
      source: "legacy",
      editable: false,
      legacy_upgrade_required: true,
    })
    const upgraded = capabilityResponse({ revision: "revision-2" })
    vi.mocked(getAgentCapabilities).mockResolvedValue(legacy)
    vi.mocked(patchAgentCapabilities).mockResolvedValue(upgraded)
    const user = userEvent.setup()

    const view = renderPanel()
    await user.click(
      await screen.findByRole("button", { name: "Upgrade legacy file" }),
    )
    expect(patchAgentCapabilities).toHaveBeenCalledWith("reviewer", {
      expected_revision: "revision-1",
      upgrade_legacy: true,
    })

    view.unmount()
    vi.mocked(getAgentCapabilities).mockResolvedValue(
      capabilityResponse({
        editable: false,
        issue_code: "agent_definition_invalid",
      }),
    )
    renderPanel()
    expect(await screen.findByText("Capabilities are read-only")).toBeVisible()
    expect(
      screen.getByRole("button", { name: "Save capabilities" }),
    ).toBeDisabled()
  })

  it("initializes a directly selected agent from an already cached query", async () => {
    const client = new QueryClient({
      defaultOptions: {
        queries: { retry: false, staleTime: Number.POSITIVE_INFINITY },
      },
    })
    client.setQueryData(
      ["agents", "reviewer", "capabilities"],
      capabilityResponse(),
    )
    client.setQueryData(
      ["agents", "writer", "capabilities"],
      capabilityResponse({
        agent_id: "writer",
        revision: "writer-revision-1",
        capabilities: {
          ...capabilityResponse().capabilities,
          tools: { mode: "none", values: [] },
        },
      }),
    )
    const view = render(
      <QueryClientProvider client={client}>
        <AgentCapabilitiesPanel agentID="reviewer" />
      </QueryClientProvider>,
    )
    expect(
      await screen.findByRole("radio", { name: "All tools" }),
    ).toBeChecked()

    view.rerender(
      <QueryClientProvider client={client}>
        <AgentCapabilitiesPanel agentID="writer" />
      </QueryClientProvider>,
    )

    expect(await screen.findByRole("radio", { name: "No tools" })).toBeChecked()
    expect(
      screen.queryByLabelText("Loading capabilities"),
    ).not.toBeInTheDocument()
    expect(getAgentCapabilities).not.toHaveBeenCalled()
  })

  it("shows a retryable error instead of an endless loader after initial failure", async () => {
    vi.mocked(getAgentCapabilities).mockRejectedValue(
      new Error("capabilities unavailable"),
    )

    renderPanel()

    expect(
      await screen.findByText("Failed to load capabilities."),
    ).toBeVisible()
    expect(screen.getByRole("button", { name: "Retry" })).toBeVisible()
    expect(
      screen.queryByLabelText("Loading capabilities"),
    ).not.toBeInTheDocument()
  })
})

function renderPanel() {
  return render(
    <QueryClientProvider
      client={
        new QueryClient({
          defaultOptions: { queries: { retry: false } },
        })
      }
    >
      <AgentCapabilitiesPanel agentID="reviewer" />
    </QueryClientProvider>,
  )
}

function capabilityResponse(
  overrides: Partial<AgentCapabilitiesResponse> = {},
): AgentCapabilitiesResponse {
  const base: AgentCapabilitiesResponse = {
    agent_id: "reviewer",
    source: "agent",
    editable: true,
    issue_code: "",
    legacy_upgrade_required: false,
    capabilities: {
      tools: { mode: "all", values: [] },
      skills: {
        mode: "inherit",
        values: [],
        inherited_values: ["review"],
      },
      mcp_servers: { mode: "all", values: [] },
    },
    catalogs: {
      tools: [
        {
          name: "web_search",
          description: "Search the web",
          category: "web",
          status: "enabled",
          reason_code: "",
        },
        {
          name: "filesystem",
          description: "Read approved files",
          category: "workspace",
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
    revision: "revision-1",
    config_revision: "config-revision-1",
    effects: {
      launcher_effect: "applied",
      catalog_effect: "applied",
      gateway_effect: "applied",
    },
  }
  return { ...base, ...overrides }
}
