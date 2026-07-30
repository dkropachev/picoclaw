import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { fireEvent, render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import type { ReactNode } from "react"
import { beforeEach, describe, expect, it, vi } from "vitest"

import { listEvents } from "@/api/events"
import {
  type WorkflowDevelopmentSession,
  type WorkflowTriggerKind,
  type WorkflowTriggerSimulationResponse,
  type WorkflowTriggersInspection,
  simulateWorkflowDevelopmentTrigger,
} from "@/api/workflows"

import type { WorkflowTriggerInspectionState } from "./workflow-trigger-editor"
import {
  WorkflowTriggerSimulator,
  type WorkflowTriggerSimulatorState,
} from "./workflow-trigger-simulator"

vi.mock("@/api/events", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/events")>()),
  listEvents: vi.fn(),
}))

vi.mock("@/api/workflows", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/workflows")>()),
  simulateWorkflowDevelopmentTrigger: vi.fn(),
}))

const mockedListEvents = vi.mocked(listEvents)
const mockedSimulate = vi.mocked(simulateWorkflowDevelopmentTrigger)

describe("WorkflowTriggerSimulator", () => {
  beforeEach(() => {
    mockedListEvents.mockReset()
    mockedSimulate.mockReset()
    mockedListEvents.mockResolvedValue({
      events: [
        {
          id: "ev_0123456789abcdef0123456789abcdef",
          source: "github",
          connector: "primary",
          type: "pull_request.opened",
          received_at: "2026-07-30T12:00:00Z",
          payload_bytes: 4096,
          routing: {
            status: "succeeded",
            available_at: "2026-07-30T12:00:00Z",
            attempts: 1,
            updated_at: "2026-07-30T12:00:00Z",
          },
        },
      ],
    })
    mockedSimulate.mockImplementation(async (request) =>
      simulationResponse(request.trigger.type),
    )
  })

  it("auto-selects the only present trigger and produces a fenced review", async () => {
    const onSimulationChange = vi.fn()
    renderSimulator(["manual"], onSimulationChange)

    await waitFor(() => expect(mockedSimulate).toHaveBeenCalledTimes(1))
    expect(mockedSimulate.mock.calls[0][0]).toMatchObject({
      session_id: "dev-session",
      expected_session_revision: "session-revision",
      expected_draft_revision: "draft-revision",
      trigger: { type: "manual" },
      scenario: { inputs: {}, secrets: {}, delivery: {} },
    })
    await waitFor(() =>
      expect(
        latestReadySimulation(onSimulationChange)?.response.review_token,
      ).toBe("review-token"),
    )
  })

  it("requires an explicit family selection when several triggers coexist", async () => {
    const user = userEvent.setup()
    renderSimulator(["manual", "command"], vi.fn())

    expect(
      screen.getByRole("combobox", { name: "Trigger scenario" }),
    ).toHaveValue("")
    await new Promise((resolve) => window.setTimeout(resolve, 300))
    expect(mockedSimulate).not.toHaveBeenCalled()

    await user.selectOptions(
      screen.getByRole("combobox", { name: "Trigger scenario" }),
      "command",
    )
    await waitFor(() => expect(mockedSimulate).toHaveBeenCalledTimes(1))
    expect(mockedSimulate.mock.calls[0][0]).toMatchObject({
      trigger: { type: "command" },
      scenario: {
        message: {
          mentioned: false,
          text: "",
        },
      },
    })
  })

  it("clears an automatic cached choice when inspection reveals multiple triggers", async () => {
    const onSimulationChange = vi.fn()
    const view = renderSimulator(["workflow_call"], onSimulationChange)
    await waitFor(() =>
      expect(
        screen.getByRole("combobox", { name: "Trigger scenario" }),
      ).toHaveValue("workflow_call"),
    )

    view.rerender(
      <WorkflowTriggerSimulator
        session={developmentSession()}
        prompt="Simulate this workflow"
        targetRef="workflows/simulate.yml"
        yaml={"name: Simulate\non: manual\njobs: {}\n"}
        inspectionState={inspectionState(["event", "workflow_call"])}
        disabled={false}
        onSimulationChange={onSimulationChange}
      />,
    )

    await waitFor(() =>
      expect(
        screen.getByRole("combobox", { name: "Trigger scenario" }),
      ).toHaveValue(""),
    )
  })

  it.each([
    ["manual", "inputs"],
    ["workflow_call", "inputs"],
    ["schedule", "scheduled_at"],
    ["channel_message", "message"],
    ["command", "message"],
    ["runtime_event", "event"],
  ] as const)(
    "simulates the %s scenario union",
    async (kind, scenarioField) => {
      renderSimulator([kind], vi.fn())

      await waitFor(() => expect(mockedSimulate).toHaveBeenCalledTimes(1))
      const request = mockedSimulate.mock.calls[0][0]
      expect(request.trigger.type).toBe(kind)
      expect(request.scenario).toHaveProperty(scenarioField)
      expect(request).not.toHaveProperty("async")
    },
  )

  it("requires one exact index when several schedules are declared", async () => {
    const user = userEvent.setup()
    const state = inspectionState(["schedule"])
    if (state.inspection != null) {
      state.inspection.triggers.schedule.value = [
        { cron: "0 9 * * 1" },
        { cron: "0 17 * * 5" },
      ]
    }
    renderSimulator(["schedule"], vi.fn(), state)
    await new Promise((resolve) => window.setTimeout(resolve, 300))
    expect(mockedSimulate).not.toHaveBeenCalled()

    await user.selectOptions(
      screen.getByRole("combobox", { name: "Declared schedule" }),
      "1",
    )
    await waitFor(() => expect(mockedSimulate).toHaveBeenCalledTimes(1))
    expect(mockedSimulate.mock.calls[0][0]).toMatchObject({
      trigger: { type: "schedule", schedule_index: 1 },
    })
  })

  it.each([
    [
      "channel_message",
      "shadowed_by_command",
      /shadowed by the command trigger/i,
    ],
    [
      "runtime_event",
      "runtime_feedback_suppressed",
      /suppressed to prevent workflow feedback/i,
    ],
  ] as const)(
    "renders the server's fixed %s non-executable outcome",
    async (kind, reason, expected) => {
      const response = simulationResponse(kind)
      response.simulation.matched = false
      response.simulation.executable = false
      response.simulation.reason = reason
      delete response.review_token
      mockedSimulate.mockResolvedValue(response)

      renderSimulator([kind], vi.fn())

      expect(await screen.findByText(expected)).toBeInTheDocument()
    },
  )

  it("uses metadata-only durable event selection and never renders payload access", async () => {
    const user = userEvent.setup()
    renderSimulator(["event"], vi.fn())

    const selector = await screen.findByRole("combobox", {
      name: "Durable event",
    })
    await screen.findByRole("option", { name: /pull_request\.opened/i })
    await user.selectOptions(selector, "ev_0123456789abcdef0123456789abcdef")
    await waitFor(() => expect(mockedSimulate).toHaveBeenCalledTimes(1))
    expect(mockedSimulate.mock.calls[0][0]).toMatchObject({
      trigger: { type: "event" },
      scenario: {
        event_id: "ev_0123456789abcdef0123456789abcdef",
      },
    })
    expect(screen.queryByText(/reveal payload/i)).not.toBeInTheDocument()
    expect(screen.queryByText("protected-payload")).not.toBeInTheDocument()
  })

  it.each([
    ['{"nested":{"value":1,"\\u0076alue":2}}'],
    ['{"unsafe":9007199254740993}'],
    ['{"unsafe":0.10000000000000001}'],
  ])("rejects lossy or duplicate JSON before simulation: %s", async (value) => {
    renderSimulator(["manual"], vi.fn())
    await waitFor(() => expect(mockedSimulate).toHaveBeenCalledTimes(1))
    mockedSimulate.mockClear()

    fireEvent.change(screen.getByLabelText("Inputs JSON"), {
      target: { value },
    })
    await new Promise((resolve) => window.setTimeout(resolve, 300))

    expect(mockedSimulate).not.toHaveBeenCalled()
    expect(screen.getByText(/inputs must be valid json/i)).toBeInTheDocument()
  })

  it("rejects arbitrary additional message envelope fields client-side", async () => {
    renderSimulator(["channel_message"], vi.fn())
    await waitFor(() => expect(mockedSimulate).toHaveBeenCalledTimes(1))
    mockedSimulate.mockClear()

    fireEvent.change(
      screen.getByLabelText("Additional message envelope JSON"),
      {
        target: { value: '{"payload":"must-not-pass"}' },
      },
    )
    await new Promise((resolve) => window.setTimeout(resolve, 300))

    expect(mockedSimulate).not.toHaveBeenCalled()
    expect(
      screen.getByText(/may contain only media, reply_handles, and raw/i),
    ).toBeInTheDocument()
  })

  it("ignores stale success and failure after scenario identity changes", async () => {
    const first = deferred<WorkflowTriggerSimulationResponse>()
    const second = deferred<WorkflowTriggerSimulationResponse>()
    mockedSimulate
      .mockReturnValueOnce(first.promise)
      .mockReturnValueOnce(second.promise)
    const onSimulationChange = vi.fn()
    renderSimulator(["manual"], onSimulationChange)
    await waitFor(() => expect(mockedSimulate).toHaveBeenCalledTimes(1))

    fireEvent.change(screen.getByLabelText("Inputs JSON"), {
      target: { value: '{"ticket":"new"}' },
    })
    await waitFor(() => expect(mockedSimulate).toHaveBeenCalledTimes(2))
    second.resolve(simulationResponse("manual"))
    await waitFor(() =>
      expect(latestReadySimulation(onSimulationChange)).toBeDefined(),
    )
    const callsAfterCurrent = onSimulationChange.mock.calls.length

    first.reject(new Error("stale secret-bearing failure"))
    await new Promise((resolve) => window.setTimeout(resolve, 0))
    expect(onSimulationChange).toHaveBeenCalledTimes(callsAfterCurrent)
    expect(
      screen.queryByText(/stale secret-bearing failure/i),
    ).not.toBeInTheDocument()
  })
})

function renderSimulator(
  kinds: WorkflowTriggerKind[],
  onSimulationChange: (state: WorkflowTriggerSimulatorState) => void,
  state = inspectionState(kinds),
) {
  return render(
    <WorkflowTriggerSimulator
      session={developmentSession()}
      prompt="Simulate this workflow"
      targetRef="workflows/simulate.yml"
      yaml={"name: Simulate\non: manual\njobs: {}\n"}
      inspectionState={state}
      disabled={false}
      onSimulationChange={onSimulationChange}
    />,
    { wrapper: queryWrapper() },
  )
}

function queryWrapper() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return function QueryWrapper({ children }: { children: ReactNode }) {
    return <QueryClientProvider client={client}>{children}</QueryClientProvider>
  }
}

function inspectionState(
  present: WorkflowTriggerKind[],
): WorkflowTriggerInspectionState {
  return {
    yaml: "name: Simulate\non: manual\njobs: {}\n",
    status: "ready",
    eventTriggerPresent: present.includes("event"),
    inspection: inspection(present),
  }
}

function inspection(
  present: WorkflowTriggerKind[],
): WorkflowTriggersInspection {
  const value = {
    manual: {},
    schedule: [{ cron: "0 9 * * 1" }],
    channel_message: {},
    command: {},
    runtime_event: {},
    event: {},
    workflow_call: {},
  }
  return {
    revision: "trigger-revision",
    triggers: Object.fromEntries(
      (
        [
          "manual",
          "schedule",
          "channel_message",
          "command",
          "runtime_event",
          "event",
          "workflow_call",
        ] as WorkflowTriggerKind[]
      ).map((kind) => [
        kind,
        {
          present: present.includes(kind),
          editable: true,
          value: present.includes(kind) ? value[kind] : null,
        },
      ]),
    ) as WorkflowTriggersInspection["triggers"],
  }
}

function developmentSession(): WorkflowDevelopmentSession {
  return {
    id: "dev-session",
    session_revision: "session-revision",
    draft_revision: "draft-revision",
    base_target_revision: "base-revision",
    reason: "new",
    status: "editing",
    target_workflow_ref: "workflows/simulate.yml",
    yaml: "name: Simulate\non: manual\njobs: {}\n",
    created_at: "2026-07-30T12:00:00Z",
    updated_at: "2026-07-30T12:00:00Z",
  }
}

function simulationResponse(
  kind: WorkflowTriggerKind,
): WorkflowTriggerSimulationResponse {
  return {
    simulation: {
      selected_kind: kind,
      effective_kind: kind,
      ...(kind === "schedule" ? { schedule_index: 0 } : {}),
      present: true,
      matched: true,
      executable: true,
      reason: "matched",
      context_summary: {
        input_count: 0,
        secret_count: 0,
        has_event: kind === "event" || kind === "runtime_event",
        has_session: false,
        has_delivery: false,
      },
    },
    review: {
      job_count: 1,
      step_count: 1,
      targets: ["agent/main"],
      effects: [
        {
          kind: "model_or_delegated_action_possible",
          target: "agent/main",
          occurrences: 1,
        },
      ],
      complete: true,
      validation: {
        valid: true,
        issue_count: 0,
        issues: [],
        truncated: false,
      },
      limits: [],
    },
    review_token: "review-token",
  }
}

function latestReadySimulation(callback: ReturnType<typeof vi.fn>) {
  const states = callback.mock.calls.map(
    ([state]) => state as WorkflowTriggerSimulatorState,
  )
  for (let index = states.length - 1; index >= 0; index -= 1) {
    const state = states[index]
    if (state.status === "ready") {
      return state
    }
  }
  return undefined
}

function deferred<Value>() {
  let resolve!: (value: Value) => void
  let reject!: (error: unknown) => void
  const promise = new Promise<Value>((promiseResolve, promiseReject) => {
    resolve = promiseResolve
    reject = promiseReject
  })
  return { promise, resolve, reject }
}
