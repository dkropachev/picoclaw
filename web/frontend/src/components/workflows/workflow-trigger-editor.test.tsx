import { act, fireEvent, render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest"

import {
  WorkflowAPIError,
  type WorkflowTriggerProjectionMap,
  type WorkflowTriggersInspection,
  inspectWorkflowTriggers,
  renderWorkflowTrigger,
} from "@/api/workflows"

import { WorkflowTriggerEditor } from "./workflow-trigger-editor"

vi.mock("@/api/workflows", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/workflows")>()),
  inspectWorkflowTriggers: vi.fn(),
  renderWorkflowTrigger: vi.fn(),
}))

const yaml = "name: Typed triggers\non:\n  manual:\njobs: {}\n"

describe("WorkflowTriggerEditor", () => {
  beforeAll(() => {
    Object.defineProperties(HTMLElement.prototype, {
      hasPointerCapture: {
        configurable: true,
        value: vi.fn(() => false),
      },
      setPointerCapture: {
        configurable: true,
        value: vi.fn(),
      },
      releasePointerCapture: {
        configurable: true,
        value: vi.fn(),
      },
      scrollIntoView: {
        configurable: true,
        value: vi.fn(),
      },
    })
  })

  beforeEach(() => {
    vi.mocked(inspectWorkflowTriggers).mockReset()
    vi.mocked(renderWorkflowTrigger).mockReset()
  })

  it("inspects once and exposes all seven typed trigger families", async () => {
    const user = userEvent.setup()
    const onInspectionChange = vi.fn()
    vi.mocked(inspectWorkflowTriggers).mockResolvedValue(
      inspection({
        manual: present({}),
        schedule: present([{ cron: "0 8 * * *" }]),
        channel_message: present({ command: "ask" }),
        command: present({ name: "summarize" }),
        runtime_event: present({ kinds: ["agent.turn.end"] }),
        event: present({ sources: ["github"] }),
        workflow_call: present({
          inputs: { ticket: { type: "string", required: true } },
          secrets: { token: { required: true } },
          outputs: { result: { value: "${{ jobs.run.outputs.result }}" } },
        }),
      }),
    )

    renderEditor({ onInspectionChange })

    expect(
      await screen.findByText("Manual triggers have no additional settings."),
    ).toBeInTheDocument()
    await chooseTrigger(user, "Schedule")
    expect(screen.getByLabelText("Cron expression 1")).toHaveValue("0 8 * * *")
    await chooseTrigger(user, "Channel message")
    expect(screen.getByLabelText("Command word")).toHaveValue("ask")
    await chooseTrigger(user, "Command")
    expect(screen.getByLabelText("Command name")).toHaveValue("summarize")
    await chooseTrigger(user, "Runtime event")
    expect(screen.getByLabelText("Event kinds")).toHaveValue("agent.turn.end")
    await chooseTrigger(user, "Durable event")
    expect(screen.getByLabelText("Sources")).toHaveValue("github")
    await chooseTrigger(user, "Workflow call")
    expect(screen.getAllByText("Inputs")).not.toHaveLength(0)
    expect(screen.getByLabelText("Input name")).toHaveValue("ticket")
    expect(screen.getByLabelText("Secret name")).toHaveValue("token")
    expect(screen.getByLabelText("Output name")).toHaveValue("result")

    expect(inspectWorkflowTriggers).toHaveBeenCalledTimes(1)
    expect(onInspectionChange).toHaveBeenLastCalledWith(
      expect.objectContaining({
        yaml,
        status: "ready",
        eventTriggerPresent: true,
      }),
    )
  })

  it("protects dirty family changes and renders with the exact base revision", async () => {
    const user = userEvent.setup()
    const onYAMLChange = vi.fn()
    const initial = inspection({
      schedule: present([{ cron: "0 8 * * *" }]),
    })
    vi.mocked(inspectWorkflowTriggers).mockResolvedValue(initial)
    vi.mocked(renderWorkflowTrigger).mockImplementation(async (payload) => ({
      ...initial,
      yaml: "name: Rendered schedule\n",
      revision: "opaque:trigger/revision?next",
      triggers: {
        ...initial.triggers,
        schedule: present(
          payload.trigger as WorkflowTriggerProjectionMap["schedule"]["value"],
        ),
      },
    }))

    renderEditor({ onYAMLChange })

    const cron = await screen.findByLabelText("Cron expression 1")
    await user.clear(cron)
    await user.type(cron, "30 9 * * 1")
    await chooseTrigger(user, "Command")

    expect(screen.getByRole("alert")).toHaveTextContent(
      "Apply or reset the schedule changes before switching",
    )
    expect(screen.getByLabelText("Cron expression 1")).toHaveValue("30 9 * * 1")

    await user.click(screen.getByRole("button", { name: "Apply to YAML" }))
    await waitFor(() => expect(renderWorkflowTrigger).toHaveBeenCalledOnce())
    expect(renderWorkflowTrigger).toHaveBeenCalledWith(
      {
        yaml,
        revision: "opaque:trigger/revision?base",
        trigger_type: "schedule",
        trigger: [{ cron: "30 9 * * 1" }],
      },
      expect.any(AbortSignal),
    )
    expect(onYAMLChange).toHaveBeenCalledWith("name: Rendered schedule\n")
  })

  it("preserves an existing cron expression byte-for-byte on an unrelated schedule edit", async () => {
    const user = userEvent.setup()
    const initial = inspection({
      schedule: present([{ cron: "  0 8 * * *  " }, { cron: "30 9 * * 1" }]),
    })
    vi.mocked(inspectWorkflowTriggers).mockResolvedValue(initial)
    vi.mocked(renderWorkflowTrigger).mockImplementation(async (payload) => ({
      ...initial,
      yaml: "name: Updated schedule\n",
      triggers: {
        ...initial.triggers,
        schedule: present(
          payload.trigger as WorkflowTriggerProjectionMap["schedule"]["value"],
        ),
      },
    }))

    renderEditor()

    expect(await screen.findByLabelText("Cron expression 1")).toHaveValue(
      "  0 8 * * *  ",
    )
    await user.click(
      screen.getByRole("button", { name: "Remove cron schedule 2" }),
    )
    await user.click(screen.getByRole("button", { name: "Apply to YAML" }))

    await waitFor(() => expect(renderWorkflowTrigger).toHaveBeenCalledOnce())
    expect(renderWorkflowTrigger).toHaveBeenCalledWith(
      expect.objectContaining({
        trigger_type: "schedule",
        trigger: [{ cron: "  0 8 * * *  " }],
      }),
      expect.any(AbortSignal),
    )
  })

  it("preserves an existing command name byte-for-byte on an unrelated command edit", async () => {
    const user = userEvent.setup()
    const initial = inspection({
      command: present({ name: "  summarize  " }),
    })
    vi.mocked(inspectWorkflowTriggers).mockResolvedValue(initial)
    vi.mocked(renderWorkflowTrigger).mockImplementation(async (payload) => ({
      ...initial,
      yaml: "name: Updated command\n",
      triggers: {
        ...initial.triggers,
        command: present(
          payload.trigger as WorkflowTriggerProjectionMap["command"]["value"],
        ),
      },
    }))

    renderEditor()

    expect(await screen.findByLabelText("Command name")).toHaveValue(
      "  summarize  ",
    )
    await user.click(
      screen.getByRole("combobox", { name: "Normal agent handling" }),
    )
    await user.click(
      await screen.findByRole("option", {
        name: "Continue normal handling",
      }),
    )
    await user.click(screen.getByRole("button", { name: "Apply to YAML" }))

    await waitFor(() => expect(renderWorkflowTrigger).toHaveBeenCalledOnce())
    expect(renderWorkflowTrigger).toHaveBeenCalledWith(
      expect.objectContaining({
        trigger_type: "command",
        trigger: {
          name: "  summarize  ",
          passthrough: true,
        },
      }),
      expect.any(AbortSignal),
    )
  })

  it("preserves an omitted workflow-call input type on an unrelated field edit", async () => {
    const user = userEvent.setup()
    const initial = inspection({
      workflow_call: present({
        inputs: { ticket: {} },
      }),
    })
    vi.mocked(inspectWorkflowTriggers).mockResolvedValue(initial)
    vi.mocked(renderWorkflowTrigger).mockImplementation(async (payload) => ({
      ...initial,
      yaml: "name: Updated workflow call\n",
      triggers: {
        ...initial.triggers,
        workflow_call: present(
          payload.trigger as WorkflowTriggerProjectionMap["workflow_call"]["value"],
        ),
      },
    }))

    renderEditor()

    expect(
      await screen.findByRole("combobox", { name: "Type" }),
    ).toHaveTextContent("String")
    await user.click(screen.getByRole("switch", { name: "Required" }))
    await user.click(screen.getByRole("button", { name: "Apply to YAML" }))

    await waitFor(() => expect(renderWorkflowTrigger).toHaveBeenCalledOnce())
    const payload = vi.mocked(renderWorkflowTrigger).mock.calls[0]?.[0]
    expect(payload).toEqual(
      expect.objectContaining({
        trigger_type: "workflow_call",
        trigger: {
          inputs: {
            ticket: { required: true },
          },
        },
      }),
    )
    expect(
      (
        payload?.trigger as WorkflowTriggerProjectionMap["workflow_call"]["value"]
      )?.inputs?.ticket,
    ).not.toHaveProperty("type")
  })

  it("refreshes a stale revision and preserves the pending form for retry", async () => {
    const user = userEvent.setup()
    const initial = inspection({
      schedule: present([{ cron: "0 8 * * *" }]),
    })
    const refreshed = inspection(
      { schedule: present([{ cron: "0 8 * * *" }]) },
      "opaque:trigger/revision?refreshed",
    )
    vi.mocked(inspectWorkflowTriggers)
      .mockResolvedValueOnce(initial)
      .mockResolvedValueOnce(refreshed)
    vi.mocked(renderWorkflowTrigger)
      .mockRejectedValueOnce(
        new WorkflowAPIError("The trigger revision is stale.", 409),
      )
      .mockImplementationOnce(async (payload) => ({
        ...refreshed,
        yaml: "name: Retried schedule\n",
        revision: "opaque:trigger/revision?rendered",
        triggers: {
          ...refreshed.triggers,
          schedule: present(
            payload.trigger as WorkflowTriggerProjectionMap["schedule"]["value"],
          ),
        },
      }))

    renderEditor()
    const cron = await screen.findByLabelText("Cron expression 1")
    await user.clear(cron)
    await user.type(cron, "15 10 * * *")
    await user.click(screen.getByRole("button", { name: "Apply to YAML" }))

    await waitFor(() =>
      expect(inspectWorkflowTriggers).toHaveBeenCalledTimes(2),
    )
    expect(screen.getByLabelText("Cron expression 1")).toHaveValue(
      "15 10 * * *",
    )
    await user.click(screen.getByRole("button", { name: "Apply to YAML" }))

    await waitFor(() => expect(renderWorkflowTrigger).toHaveBeenCalledTimes(2))
    expect(renderWorkflowTrigger).toHaveBeenLastCalledWith(
      expect.objectContaining({
        revision: "opaque:trigger/revision?refreshed",
        trigger: [{ cron: "15 10 * * *" }],
      }),
      expect.any(AbortSignal),
    )
  })

  it("rejects unsafe or non-canonical numeric defaults before rendering", async () => {
    const user = userEvent.setup()
    vi.mocked(inspectWorkflowTriggers).mockResolvedValue(
      inspection({
        workflow_call: present({
          inputs: { count: { type: "number", default: 1 } },
        }),
      }),
    )

    renderEditor()

    const defaultValue = await screen.findByLabelText("Default value")
    await user.clear(defaultValue)
    await user.type(defaultValue, "9007199254740993")
    expect(screen.getByRole("alert")).toHaveTextContent(
      "number default must be a safe integer",
    )
    expect(screen.getByRole("button", { name: "Apply to YAML" })).toBeDisabled()

    await user.clear(defaultValue)
    await user.type(defaultValue, "1.0")
    expect(screen.getByRole("alert")).toHaveTextContent(
      "number default must use canonical notation, such as 1",
    )
    expect(renderWorkflowTrigger).not.toHaveBeenCalled()
  })

  it("rejects unsafe integers nested inside JSON defaults", async () => {
    vi.mocked(inspectWorkflowTriggers).mockResolvedValue(
      inspection({
        workflow_call: present({
          inputs: {
            payload: { type: "object", default: { count: 1 } },
          },
        }),
      }),
    )

    renderEditor()

    const defaultJSON = await screen.findByLabelText("Default JSON")
    fireEvent.change(defaultJSON, {
      target: { value: '{"count":9007199254740993}' },
    })

    expect(screen.getByRole("alert")).toHaveTextContent(
      'numeric token "9007199254740993" that cannot be represented exactly in the browser or as a safe integer',
    )
    expect(screen.getByRole("button", { name: "Apply to YAML" })).toBeDisabled()
    expect(renderWorkflowTrigger).not.toHaveBeenCalled()
  })

  it("rejects decimal and exponent tokens that would silently normalize", async () => {
    vi.mocked(inspectWorkflowTriggers).mockResolvedValue(
      inspection({
        workflow_call: present({
          inputs: {
            payload: { type: "object", default: { count: 0.1 } },
          },
        }),
      }),
    )

    renderEditor()

    const defaultJSON = await screen.findByLabelText("Default JSON")
    for (const token of [
      "9007199254740991.1",
      "0.10000000000000001",
      "9007199254740993e0",
      "1e-400",
      "0e401",
      "0e10000",
    ]) {
      fireEvent.change(defaultJSON, {
        target: { value: `{"count":${token}}` },
      })
      expect(screen.getByRole("alert")).toHaveTextContent(
        `numeric token "${token}" that cannot be represented exactly in the browser or as a safe integer`,
      )
      expect(
        screen.getByRole("button", { name: "Apply to YAML" }),
      ).toBeDisabled()
    }

    fireEvent.change(defaultJSON, {
      target: {
        value:
          '{"fraction":0.1,"limit":9007199254740991e0,"decimal":1.0,"exponent":1e0,"zero":0e400}',
      },
    })
    expect(
      screen.queryByText(/cannot be represented exactly in the browser/),
    ).not.toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Apply to YAML" })).toBeEnabled()
    expect(renderWorkflowTrigger).not.toHaveBeenCalled()
  })

  it("rejects duplicate keys nested inside JSON defaults, including escaped keys", async () => {
    vi.mocked(inspectWorkflowTriggers).mockResolvedValue(
      inspection({
        workflow_call: present({
          inputs: {
            payload: { type: "object", default: { outer: { id: 1 } } },
          },
        }),
      }),
    )

    renderEditor()

    const defaultJSON = await screen.findByLabelText("Default JSON")
    fireEvent.change(defaultJSON, {
      target: { value: '{"outer":{"id":1,"\\u0069d":2}}' },
    })

    expect(screen.getByRole("alert")).toHaveTextContent(
      'default contains duplicate object key "id"',
    )
    expect(screen.getByRole("button", { name: "Apply to YAML" })).toBeDisabled()
    expect(renderWorkflowTrigger).not.toHaveBeenCalled()
  })

  it("preserves unsupported input and conversation values when applying another edit", async () => {
    const user = userEvent.setup()
    const initial = inspection({
      command: present({
        name: "summarize",
        args: {
          count: { type: "integer", default: 7 },
        },
        conversation: {
          session: "thread",
          delivery: "later",
        },
      }),
    })
    vi.mocked(inspectWorkflowTriggers).mockResolvedValue(initial)
    vi.mocked(renderWorkflowTrigger).mockImplementation(async (payload) => ({
      ...initial,
      yaml: "name: Updated command\n",
      revision: "opaque:trigger/revision?next",
      triggers: {
        ...initial.triggers,
        command: present(
          payload.trigger as WorkflowTriggerProjectionMap["command"]["value"],
        ),
      },
    }))

    renderEditor()

    expect(
      await screen.findByRole("combobox", { name: "Type" }),
    ).toHaveTextContent('Unsupported YAML value: "integer"')
    expect(
      screen.getByRole("combobox", { name: "Session scope" }),
    ).toHaveTextContent('Unsupported YAML value: "thread"')
    expect(
      screen.getByRole("combobox", { name: "Delivery" }),
    ).toHaveTextContent('Unsupported YAML value: "later"')

    const commandName = screen.getByLabelText("Command name")
    await user.clear(commandName)
    await user.type(commandName, "summarize-now")
    await user.click(screen.getByRole("button", { name: "Apply to YAML" }))

    await waitFor(() => expect(renderWorkflowTrigger).toHaveBeenCalledOnce())
    expect(renderWorkflowTrigger).toHaveBeenCalledWith(
      expect.objectContaining({
        trigger_type: "command",
        trigger: {
          name: "summarize-now",
          args: {
            count: { type: "integer", default: 7 },
          },
          conversation: {
            session: "thread",
            delivery: "later",
          },
        },
      }),
      expect.any(AbortSignal),
    )
  })

  it("preserves dirty form state when authoritative YAML changes until explicit discard", async () => {
    const user = userEvent.setup()
    const onYAMLChange = vi.fn()
    const onInspectionChange = vi.fn()
    const onActivityChange = vi.fn()
    const initialYAML =
      "name: Initial\non:\n  schedule:\n    - cron: '0 8 * * *'\njobs: {}\n"
    const latestYAML =
      "name: Latest\non:\n  schedule:\n    - cron: '45 11 * * *'\njobs: {}\n"
    const initial = inspection({
      schedule: present([{ cron: "0 8 * * *" }]),
    })
    const latest = inspection(
      { schedule: present([{ cron: "45 11 * * *" }]) },
      "opaque:trigger/revision?latest",
    )
    vi.mocked(inspectWorkflowTriggers)
      .mockResolvedValueOnce(initial)
      .mockResolvedValueOnce(latest)

    const view = renderEditor({
      value: initialYAML,
      onYAMLChange,
      onInspectionChange,
      onActivityChange,
    })
    const cron = await screen.findByLabelText("Cron expression 1")
    await user.clear(cron)
    await user.type(cron, "30 9 * * 1")

    view.rerender(
      <WorkflowTriggerEditor
        yaml={latestYAML}
        disabled={false}
        onYAMLChange={onYAMLChange}
        onInspectionChange={onInspectionChange}
        onActivityChange={onActivityChange}
        onOpenYAML={vi.fn()}
      />,
    )

    expect(
      await screen.findByText(
        "The authoritative YAML changed outside this builder.",
      ),
    ).toBeInTheDocument()
    await waitFor(() =>
      expect(inspectWorkflowTriggers).toHaveBeenCalledTimes(2),
    )
    expect(await screen.findByLabelText("Cron expression 1")).toHaveValue(
      "30 9 * * 1",
    )
    expect(screen.getByRole("button", { name: "Apply to YAML" })).toBeDisabled()
    expect(onActivityChange).toHaveBeenLastCalledWith({
      dirty: true,
      applying: false,
      conflict: true,
    })

    await user.click(
      screen.getByRole("button", {
        name: "Discard edits and load latest YAML",
      }),
    )

    expect(screen.getByLabelText("Cron expression 1")).toHaveValue(
      "45 11 * * *",
    )
    await waitFor(() =>
      expect(onActivityChange).toHaveBeenLastCalledWith({
        dirty: false,
        applying: false,
        conflict: false,
      }),
    )
    expect(onYAMLChange).not.toHaveBeenCalled()
  })

  it("aborts an in-flight render on unmount and ignores its late result", async () => {
    const user = userEvent.setup()
    const onYAMLChange = vi.fn()
    const onInspectionChange = vi.fn()
    const initial = inspection({
      schedule: present([{ cron: "0 8 * * *" }]),
    })
    const pending =
      deferred<Awaited<ReturnType<typeof renderWorkflowTrigger>>>()
    vi.mocked(inspectWorkflowTriggers).mockResolvedValue(initial)
    vi.mocked(renderWorkflowTrigger).mockReturnValue(pending.promise)

    const view = renderEditor({ onYAMLChange, onInspectionChange })
    const cron = await screen.findByLabelText("Cron expression 1")
    await user.clear(cron)
    await user.type(cron, "30 9 * * 1")
    await user.click(screen.getByRole("button", { name: "Apply to YAML" }))

    await waitFor(() => expect(renderWorkflowTrigger).toHaveBeenCalledOnce())
    const signal = vi.mocked(renderWorkflowTrigger).mock.calls[0]?.[1]
    expect(signal).toBeInstanceOf(AbortSignal)
    onInspectionChange.mockClear()
    view.unmount()
    expect(signal?.aborted).toBe(true)

    await act(async () => {
      pending.resolve({
        ...initial,
        yaml: "name: Late render\n",
        revision: "opaque:trigger/revision?late",
      })
      await pending.promise
    })

    expect(onYAMLChange).not.toHaveBeenCalled()
    expect(onInspectionChange).not.toHaveBeenCalled()
  })

  it("renders actionable candidate validation returned by the server", async () => {
    const user = userEvent.setup()
    vi.mocked(inspectWorkflowTriggers).mockResolvedValue(
      inspection({
        schedule: present([{ cron: "0 8 * * *" }]),
      }),
    )
    vi.mocked(renderWorkflowTrigger).mockRejectedValue(
      new WorkflowAPIError("invalid_workflow_trigger", 422, {
        valid: false,
        errors: [
          {
            path: "on.schedule[0].cron",
            message: "invalid cron expression",
          },
        ],
        validated_at: "2026-07-29T12:00:00Z",
      }),
    )

    renderEditor()
    const cron = await screen.findByLabelText("Cron expression 1")
    await user.clear(cron)
    await user.type(cron, "not-a-cron")
    await user.click(screen.getByRole("button", { name: "Apply to YAML" }))

    expect(
      await screen.findByText("Candidate trigger validation"),
    ).toBeInTheDocument()
    expect(screen.getByText(/on\.schedule\[0\]\.cron:/)).toBeInTheDocument()
    expect(screen.getByText(/invalid cron expression/)).toBeInTheDocument()
    expect(
      screen.getByText(/Review its fields and validation errors/),
    ).toBeInTheDocument()
  })

  it("falls back to raw YAML when a trigger projection is not editable", async () => {
    const user = userEvent.setup()
    const onOpenYAML = vi.fn()
    vi.mocked(inspectWorkflowTriggers).mockResolvedValue(
      inspection({
        manual: {
          present: true,
          editable: false,
          reason: "YAML aliases are not safe to project.",
          value: {},
        },
      }),
    )

    renderEditor({ onOpenYAML })

    expect(
      await screen.findByText("YAML aliases are not safe to project."),
    ).toBeInTheDocument()
    await user.click(screen.getByRole("button", { name: "Open YAML" }))
    expect(onOpenYAML).toHaveBeenCalledOnce()
  })
})

async function chooseTrigger(
  user: ReturnType<typeof userEvent.setup>,
  label: string,
) {
  await user.click(screen.getByRole("combobox", { name: "Trigger type" }))
  await user.click(
    await screen.findByRole("option", {
      name: new RegExp(`^${label}`),
    }),
  )
}

function present<Value>(value: Value) {
  return { present: true, editable: true, value }
}

function inspection(
  overrides: Partial<WorkflowTriggerProjectionMap> = {},
  revision = "opaque:trigger/revision?base",
): WorkflowTriggersInspection {
  const triggers: WorkflowTriggerProjectionMap = {
    manual: { present: false, editable: true, value: null },
    schedule: { present: false, editable: true, value: null },
    channel_message: { present: false, editable: true, value: null },
    command: { present: false, editable: true, value: null },
    runtime_event: { present: false, editable: true, value: null },
    event: { present: false, editable: true, value: null },
    workflow_call: { present: false, editable: true, value: null },
    ...overrides,
  }
  return {
    revision,
    triggers,
    validation: validValidation(),
  }
}

function validValidation() {
  return {
    valid: true,
    validated_at: "2026-07-29T12:00:00Z",
  }
}

function renderEditor({
  value = yaml,
  onYAMLChange = vi.fn(),
  onInspectionChange = vi.fn(),
  onActivityChange = vi.fn(),
  onOpenYAML = vi.fn(),
}: {
  value?: string
  onYAMLChange?: (yaml: string) => void
  onInspectionChange?: Parameters<
    typeof WorkflowTriggerEditor
  >[0]["onInspectionChange"]
  onActivityChange?: Parameters<
    typeof WorkflowTriggerEditor
  >[0]["onActivityChange"]
  onOpenYAML?: () => void
} = {}) {
  return render(
    <WorkflowTriggerEditor
      yaml={value}
      disabled={false}
      onYAMLChange={onYAMLChange}
      onInspectionChange={onInspectionChange}
      onActivityChange={onActivityChange}
      onOpenYAML={onOpenYAML}
    />,
  )
}

function deferred<Value>() {
  let resolve!: (value: Value) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<Value>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise
    reject = rejectPromise
  })
  return { promise, resolve, reject }
}
