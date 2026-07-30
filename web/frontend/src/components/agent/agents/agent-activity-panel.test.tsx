import { render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import {
  type AgentActivityEvent,
  type AgentActivityResponse,
  getAgentActivity,
} from "@/api/agents"
import { updateGatewayStore } from "@/store/gateway"

import { mergeActivityEvents } from "./agent-activity"
import { AgentActivityPanel } from "./agent-activity-panel"

vi.mock("@/api/agents", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/agents")>()
  return {
    ...actual,
    getAgentActivity: vi.fn(),
  }
})

describe("AgentActivityPanel", () => {
  beforeEach(() => {
    vi.mocked(getAgentActivity).mockReset()
    setNavigatorOnline(true)
    setDocumentVisibility("visible")
    updateGatewayStore({
      status: "running",
      canStart: true,
      restartRequired: false,
    })
  })

  afterEach(() => {
    setNavigatorOnline(true)
    setDocumentVisibility("visible")
    updateGatewayStore({
      status: "unknown",
      canStart: true,
      restartRequired: false,
    })
  })

  it("renders only fixed privacy-safe labels and independent loss warnings", async () => {
    const rawEvent = {
      ...activityEvent("5"),
      details: {
        tool_name: "web_search",
        duration_ms: "12",
        is_error: false,
        async: false,
        arguments: "CANARY_ARGUMENT_SECRET",
        result: "CANARY_RESULT_SECRET",
      },
      prompt: "CANARY_PROMPT_SECRET",
      error: "CANARY_ERROR_SECRET",
    } as unknown as AgentActivityEvent
    vi.mocked(getAgentActivity).mockResolvedValue({
      agent_id: "reviewer",
      events: [rawEvent],
      next_cursor: "cursor-5",
      reset: true,
      truncated: true,
      dropped: {
        subscription: "1",
        retention: "2",
        projection: "3",
      },
    })

    const view = render(<AgentActivityPanel agentID="reviewer" />)

    expect(await screen.findByText("Tool execution ended")).toBeVisible()
    expect(screen.getByText(/web_search; 12 ms; completed/)).toBeVisible()
    expect(
      screen.getByText("The runtime restarted; the activity cursor was reset."),
    ).toBeVisible()
    expect(
      screen.getByText(
        "Some activity was omitted by the bounded activity window.",
      ),
    ).toBeVisible()
    expect(
      screen.getByText("1 records were dropped before delivery."),
    ).toBeVisible()
    expect(view.container.textContent).not.toContain("CANARY_")
  })

  it("pauses while the gateway is stopped and starts after it runs", async () => {
    updateGatewayStore({
      status: "stopped",
      canStart: true,
      restartRequired: false,
    })
    vi.mocked(getAgentActivity).mockResolvedValue(activityResponse([]))

    render(<AgentActivityPanel agentID="reviewer" />)
    expect(screen.getByText("Gateway is not running")).toBeVisible()
    expect(getAgentActivity).not.toHaveBeenCalled()

    updateGatewayStore({ status: "running" })
    await waitFor(() => expect(getAgentActivity).toHaveBeenCalledTimes(1))
  })

  it("does not request while the browser is offline and resumes on online", async () => {
    setNavigatorOnline(false)
    vi.mocked(getAgentActivity).mockResolvedValue(activityResponse([]))

    render(<AgentActivityPanel agentID="reviewer" />)
    expect(screen.getByText("Browser is offline")).toBeVisible()
    expect(getAgentActivity).not.toHaveBeenCalled()

    setNavigatorOnline(true)
    window.dispatchEvent(new Event("online"))
    await waitFor(() => expect(getAgentActivity).toHaveBeenCalledTimes(1))
  })

  it("polls only while visible and aborts an in-flight request on unmount", async () => {
    setDocumentVisibility("hidden")
    let requestSignal: AbortSignal | undefined
    vi.mocked(getAgentActivity).mockImplementation((_agentID, options) => {
      requestSignal = options?.signal
      return new Promise<AgentActivityResponse>(() => undefined)
    })

    const view = render(<AgentActivityPanel agentID="reviewer" />)
    expect(getAgentActivity).not.toHaveBeenCalled()

    setDocumentVisibility("visible")
    document.dispatchEvent(new Event("visibilitychange"))
    await waitFor(() => expect(getAgentActivity).toHaveBeenCalledTimes(1))
    expect(requestSignal?.aborted).toBe(false)

    view.unmount()
    expect(requestSignal?.aborted).toBe(true)
  })

  it("deduplicates by agent and decimal sequence, sorts, and bounds rows", () => {
    const current = [activityEvent("2"), activityEvent("10")]
    const incoming = [
      { ...activityEvent("2"), severity: "warn" as const },
      activityEvent("11"),
    ]

    expect(
      mergeActivityEvents(current, incoming, 2).map((event) => [
        event.sequence,
        event.severity,
      ]),
    ).toEqual([
      ["10", "info"],
      ["11", "info"],
    ])
  })
})

function activityEvent(sequence: string): AgentActivityEvent {
  return {
    sequence,
    agent_id: "reviewer",
    timestamp: "2026-07-30T12:00:00Z",
    kind: "agent.tool.exec_end",
    severity: "info",
    details: {
      tool_name: "web_search",
      duration_ms: "12",
      is_error: false,
      async: false,
    },
  }
}

function activityResponse(events: AgentActivityEvent[]): AgentActivityResponse {
  return {
    agent_id: "reviewer",
    events,
    next_cursor: "",
    reset: false,
    truncated: false,
    dropped: {
      subscription: "0",
      retention: "0",
      projection: "0",
    },
  }
}

function setNavigatorOnline(online: boolean) {
  Object.defineProperty(window.navigator, "onLine", {
    configurable: true,
    value: online,
  })
}

function setDocumentVisibility(state: DocumentVisibilityState) {
  Object.defineProperty(document, "visibilityState", {
    configurable: true,
    value: state,
  })
}
