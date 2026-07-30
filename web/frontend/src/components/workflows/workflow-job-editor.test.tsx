import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest"

import {
  type WorkflowEditorField,
  type WorkflowEditorJSONValue,
  type WorkflowJobEditorOperation,
  type WorkflowJobsInspection,
  inspectWorkflowJobs,
  renderWorkflowJobs,
} from "@/api/workflows"

import {
  WorkflowJobEditor,
  type WorkflowStructuredActionsState,
} from "./workflow-job-editor"

vi.mock("@/api/workflows", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/workflows")>()),
  inspectWorkflowJobs: vi.fn(),
  renderWorkflowJobs: vi.fn(),
}))

const yaml = "name: Review\njobs:\n  review:\n    steps: []\n"

describe("WorkflowJobEditor", () => {
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
    vi.mocked(inspectWorkflowJobs).mockReset()
    vi.mocked(renderWorkflowJobs).mockReset()
  })

  it("renders stable source order, graph dependencies, and every known field without changing YAML", async () => {
    const onYAMLChange = vi.fn()
    const onStructuredActionsChange = vi.fn()
    vi.mocked(inspectWorkflowJobs).mockResolvedValue(inspection())

    renderEditor({ onYAMLChange, onStructuredActionsChange })

    expect(await screen.findByText("Needs: prepare")).toBeInTheDocument()
    const actions = screen.getByRole("list", {
      name: "Actions in job review",
    })
    expect(within(actions).getAllByRole("listitem")).toHaveLength(2)
    expect(within(actions).getByText("#summarize")).toBeInTheDocument()
    expect(screen.getByText("Source: (empty string)")).toBeInTheDocument()
    expect(screen.getAllByText("Source: false")).not.toHaveLength(0)
    expect(screen.getByLabelText("Job ID mutation")).toHaveTextContent(
      "Keep source",
    )
    expect(onYAMLChange).not.toHaveBeenCalled()
    expect(onStructuredActionsChange).toHaveBeenLastCalledWith({
      yaml,
      status: "ready",
      review: {
        jobCount: 1,
        stepCount: 2,
        targets: ["agent/main", "tool/message"],
        rawOnlyCount: 0,
      },
    })
  })

  it("sends explicit set(false), set(empty), and remove envelopes on one revision", async () => {
    const user = userEvent.setup()
    const onYAMLChange = vi.fn()
    const base = inspection()
    const rendered = structuredClone(base)
    rendered.revision = "opaque:jobs:next"
    const renderedYAML = `${yaml}# rendered\n`
    vi.mocked(inspectWorkflowJobs).mockResolvedValue(base)
    vi.mocked(renderWorkflowJobs).mockResolvedValue({
      ...rendered,
      yaml: renderedYAML,
      validation: {
        valid: false,
        errors: [
          {
            path: "jobs.review.steps[0].if",
            message: "condition was removed",
          },
        ],
        validated_at: "2026-07-30T00:00:01Z",
      },
    })

    renderEditor({ onYAMLChange })
    const actionSection = await selectedActionSection()
    await chooseMutation(
      user,
      actionSection,
      "Display name mutation",
      "Set value",
    )
    await chooseMutation(
      user,
      actionSection,
      "Continue on error mutation",
      "Set value",
    )
    expect(
      within(actionSection).getByRole("switch", { name: "Explicit value" }),
    ).not.toBeChecked()
    await chooseMutation(
      user,
      actionSection,
      "Step condition mutation",
      "Remove",
    )
    await user.click(
      screen.getByRole("button", { name: "Apply fields to YAML" }),
    )

    await waitFor(() => expect(renderWorkflowJobs).toHaveBeenCalledOnce())
    expect(renderWorkflowJobs).toHaveBeenCalledWith(
      {
        yaml,
        revision: "opaque:jobs:base",
        operation: {
          type: "step.patch",
          job_id: "review",
          step_index: 0,
          fields: {
            name: { mode: "set", value: "" },
            if: { mode: "remove" },
            continue_on_error: { mode: "set", value: false },
          },
        },
      },
      expect.any(AbortSignal),
    )
    expect(onYAMLChange).toHaveBeenCalledWith(renderedYAML)
    expect(screen.getByText("condition was removed")).toBeInTheDocument()
  })

  it("renames only the job mapping key and leaves dependency repair to validation", async () => {
    const user = userEvent.setup()
    const base = inspection()
    const renamed = structuredClone(base)
    renamed.jobs[0].id = "review_v2"
    vi.mocked(inspectWorkflowJobs).mockResolvedValue(base)
    vi.mocked(renderWorkflowJobs).mockResolvedValue({
      ...renamed,
      revision: "opaque:jobs:renamed",
      yaml: `${yaml}# renamed\n`,
      validation: {
        valid: false,
        errors: [
          {
            path: "jobs.publish.needs",
            message: "unknown dependency review",
          },
        ],
        validated_at: "2026-07-30T00:00:01Z",
      },
    })

    renderEditor()
    await screen.findByText("Job graph")
    await chooseMutation(
      user,
      screen.getByRole("main"),
      "Job ID mutation",
      "Rename",
    )
    const newID = screen.getByLabelText("New job ID")
    await user.clear(newID)
    await user.type(newID, "review_v2")
    await user.click(
      screen.getByRole("button", { name: "Apply fields to YAML" }),
    )

    await waitFor(() => expect(renderWorkflowJobs).toHaveBeenCalledOnce())
    expect(renderWorkflowJobs).toHaveBeenCalledWith(
      expect.objectContaining({
        operation: {
          type: "job.patch",
          job_id: "review",
          fields: {},
          new_job_id: { mode: "set", value: "review_v2" },
        },
      }),
      expect.any(AbortSignal),
    )
    expect(await screen.findByText("unknown dependency review")).toBeVisible()
  })

  it("keeps raw-only state granular and permits a safe sibling job", async () => {
    const user = userEvent.setup()
    const mixed = inspection()
    mixed.complete = false
    mixed.limits = ["validation_truncated"]
    mixed.jobs[0].editable = false
    mixed.jobs[0].reason = "Alias-backed steps must stay in YAML."
    mixed.jobs.push({
      ...structuredClone(mixed.jobs[0]),
      id: "safe",
      index: 1,
      editable: true,
      reason: undefined,
      steps: [],
      fields: {
        ...structuredClone(mixed.jobs[0].fields),
        needs: absent(),
      },
    })
    vi.mocked(inspectWorkflowJobs).mockResolvedValue(mixed)

    renderEditor()

    expect(
      await screen.findByText("Alias-backed steps must stay in YAML."),
    ).toBeInTheDocument()
    expect(screen.getByText(/validation truncated/i)).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Add action" })).toBeEnabled()
    await user.click(screen.getByRole("button", { name: /safe/i }))
    expect(screen.getByLabelText("Job ID mutation")).toBeEnabled()
    expect(screen.getByRole("button", { name: "Add action" })).toBeEnabled()
  })

  it("discloses advanced fields and incomplete projection as conservative unknown effects", async () => {
    const projected = inspection()
    projected.complete = false
    projected.limits = ["steps_truncated"]
    projected.jobs[0].advanced_fields_present = true
    projected.jobs[0].steps[0].advanced_fields_present = true
    const onStructuredActionsChange = vi.fn()
    vi.mocked(inspectWorkflowJobs).mockResolvedValue(projected)

    renderEditor({ onStructuredActionsChange })

    await waitFor(() =>
      expect(onStructuredActionsChange).toHaveBeenLastCalledWith({
        yaml,
        status: "ready",
        review: {
          jobCount: 1,
          stepCount: 2,
          targets: ["agent/main", "tool/message"],
          rawOnlyCount: 3,
        },
      }),
    )
  })

  it("rejects duplicate dynamic JSON members before rendering YAML", async () => {
    const user = userEvent.setup()
    vi.mocked(inspectWorkflowJobs).mockResolvedValue(inspection())
    renderEditor()
    const actionSection = await selectedActionSection()
    await chooseMutation(
      user,
      actionSection,
      "Action inputs mutation",
      "Set value",
    )
    fireEvent.change(
      within(actionSection).getByLabelText("Action inputs value"),
      {
        target: { value: '{"query":"first","query":"second"}' },
      },
    )

    expect(
      await screen.findByText(/Duplicate JSON object keys are not allowed/),
    ).toBeInTheDocument()
    expect(
      screen.getByRole("button", { name: "Apply fields to YAML" }),
    ).toBeDisabled()
    expect(renderWorkflowJobs).not.toHaveBeenCalled()
  })

  it("rejects inexact numeric spellings and accepts exact equivalents", async () => {
    const user = userEvent.setup()
    const base = inspection()
    vi.mocked(inspectWorkflowJobs).mockResolvedValue(base)
    vi.mocked(renderWorkflowJobs).mockResolvedValue({
      ...base,
      revision: "opaque:jobs:numbers",
      yaml: `${yaml}# numbers\n`,
    })
    renderEditor()
    const actionSection = await selectedActionSection()
    await chooseMutation(
      user,
      actionSection,
      "Action inputs mutation",
      "Set value",
    )
    const inputs = within(actionSection).getByLabelText("Action inputs value")

    for (const source of [
      '{"value":0.100000000000000005}',
      '{"value":1e-400}',
      '{"value":0e401}',
    ]) {
      fireEvent.change(inputs, { target: { value: source } })
      expect(
        screen.getByRole("button", { name: "Apply fields to YAML" }),
      ).toBeDisabled()
    }

    fireEvent.change(inputs, {
      target: { value: '{"decimal":1.0,"exponent":1e0,"zero":0e400}' },
    })
    const apply = screen.getByRole("button", {
      name: "Apply fields to YAML",
    })
    expect(apply).toBeEnabled()
    await user.click(apply)

    await waitFor(() => expect(renderWorkflowJobs).toHaveBeenCalledOnce())
    expect(renderWorkflowJobs).toHaveBeenCalledWith(
      expect.objectContaining({
        operation: expect.objectContaining({
          type: "step.patch",
          fields: {
            with: {
              mode: "set",
              value: { decimal: 1, exponent: 1, zero: 0 },
            },
          },
        }),
      }),
      expect.any(AbortSignal),
    )
  })

  it("matches the service JSON depth boundary", async () => {
    const user = userEvent.setup()
    vi.mocked(inspectWorkflowJobs).mockResolvedValue(inspection())
    renderEditor()
    const actionSection = await selectedActionSection()
    await chooseMutation(
      user,
      actionSection,
      "Action inputs mutation",
      "Set value",
    )
    const inputs = within(actionSection).getByLabelText("Action inputs value")
    const nestedObject = (arrayCount: number) =>
      `{"value":${"[".repeat(arrayCount)}0${"]".repeat(arrayCount)}}`

    fireEvent.change(inputs, { target: { value: nestedObject(15) } })
    expect(
      screen.getByRole("button", { name: "Apply fields to YAML" }),
    ).toBeEnabled()

    fireEvent.change(inputs, { target: { value: nestedObject(16) } })
    expect(
      screen.getByRole("button", { name: "Apply fields to YAML" }),
    ).toBeDisabled()
    expect(renderWorkflowJobs).not.toHaveBeenCalled()
  })

  it("rejects lone JSON surrogates but allows valid pairs and replacement text", async () => {
    const user = userEvent.setup()
    vi.mocked(inspectWorkflowJobs).mockResolvedValue(inspection())
    renderEditor()
    const actionSection = await selectedActionSection()
    await chooseMutation(
      user,
      actionSection,
      "Action inputs mutation",
      "Set value",
    )
    const inputs = within(actionSection).getByLabelText("Action inputs value")

    for (const source of [
      '{"value":"\\uD800"}',
      '{"value":"\\uDC00"}',
      '{"\\uD800":"value"}',
    ]) {
      fireEvent.change(inputs, { target: { value: source } })
      expect(
        screen.getByRole("button", { name: "Apply fields to YAML" }),
      ).toBeDisabled()
    }

    fireEvent.change(inputs, {
      target: {
        value: '{"emoji":"\\uD83D\\uDE00","replacement":"\uFFFD"}',
      },
    })
    expect(
      screen.getByRole("button", { name: "Apply fields to YAML" }),
    ).toBeEnabled()
    expect(renderWorkflowJobs).not.toHaveBeenCalled()
  })

  it("rejects server-unsafe field text and dynamic JSON controls before render", async () => {
    const user = userEvent.setup()
    vi.mocked(inspectWorkflowJobs).mockResolvedValue(inspection())
    renderEditor()
    const actionSection = await selectedActionSection()
    await chooseMutation(
      user,
      actionSection,
      "Display name mutation",
      "Set value",
    )
    const name = within(actionSection).getByLabelText("Display name value")
    const apply = screen.getByRole("button", {
      name: "Apply fields to YAML",
    })
    for (const value of [
      "x".repeat((16 << 10) + 1),
      "unsafe\u0001text",
      "unsafe\u200etext",
    ]) {
      fireEvent.change(name, { target: { value } })
      expect(apply).toBeDisabled()
    }

    await user.click(screen.getByRole("button", { name: "Reset pending" }))
    await chooseMutation(
      user,
      screen.getByRole("main"),
      "Action inputs mutation",
      "Set value",
    )
    const inputs = screen.getByLabelText("Action inputs value")
    for (const value of [
      '{"value":"unsafe\\u0001text"}',
      '{"value":"unsafe\\u200etext"}',
      '{"unsafe\\u0001key":"value"}',
      '{"unsafe\\u200ekey":"value"}',
    ]) {
      fireEvent.change(inputs, { target: { value } })
      expect(apply).toBeDisabled()
    }
    fireEvent.change(inputs, {
      target: { value: '{"value":"line one\\n\\tline two\\r"}' },
    })
    expect(apply).toBeEnabled()
    expect(renderWorkflowJobs).not.toHaveBeenCalled()
  })

  it("rejects oversized or multiline action IDs and dependency references", async () => {
    const user = userEvent.setup()
    vi.mocked(inspectWorkflowJobs).mockResolvedValue(inspection())
    renderEditor()
    const actionSection = await selectedActionSection()
    await chooseMutation(user, actionSection, "Step ID mutation", "Set value")
    fireEvent.change(within(actionSection).getByLabelText("Step ID value"), {
      target: { value: "x".repeat(257) },
    })
    expect(
      screen.getByText(/Step ID must be a single-line value/i),
    ).toBeInTheDocument()
    expect(
      screen.getByRole("button", { name: "Apply fields to YAML" }),
    ).toBeDisabled()

    await user.click(screen.getByRole("button", { name: "Reset pending" }))
    await chooseMutation(
      user,
      screen.getByRole("main"),
      "Job ID mutation",
      "Rename",
    )
    fireEvent.change(screen.getByLabelText("New job ID"), {
      target: { value: " review " },
    })
    expect(
      screen.getByText(/Job ID must not have leading or trailing whitespace/i),
    ).toBeInTheDocument()
    expect(
      screen.getByRole("button", { name: "Apply fields to YAML" }),
    ).toBeDisabled()

    await user.click(screen.getByRole("button", { name: "Reset pending" }))
    await chooseMutation(
      user,
      screen.getByRole("main"),
      "Dependencies mutation",
      "Set value",
    )
    fireEvent.change(screen.getByLabelText("Dependencies value"), {
      target: { value: '["prepare\\npublish"]' },
    })
    expect(
      screen.getByText(
        /non-empty, single-line job IDs no larger than 256 UTF-8 bytes/i,
      ),
    ).toBeInTheDocument()
    expect(
      screen.getByRole("button", { name: "Apply fields to YAML" }),
    ).toBeDisabled()
    fireEvent.change(screen.getByLabelText("Dependencies value"), {
      target: { value: '["   "]' },
    })
    expect(
      screen.getByRole("button", { name: "Apply fields to YAML" }),
    ).toBeDisabled()
    fireEvent.change(screen.getByLabelText("Dependencies value"), {
      target: { value: '[" prepare "]' },
    })
    expect(
      screen.getByRole("button", { name: "Apply fields to YAML" }),
    ).toBeDisabled()
    expect(renderWorkflowJobs).not.toHaveBeenCalled()
  })

  it("does not move a safe action across a raw-only neighbor", async () => {
    const projected = inspection()
    projected.jobs[0].steps[1].editable = false
    projected.jobs[0].steps[1].reason = "Alias-backed action."
    vi.mocked(inspectWorkflowJobs).mockResolvedValue(projected)
    renderEditor()

    const moveDown = await screen.findByRole("button", {
      name: "Move action 1 down",
    })
    expect(moveDown).toBeDisabled()
    expect(moveDown).toHaveAttribute(
      "title",
      "A raw-only action cannot be reordered indirectly.",
    )
  })

  it("defaults new jobs to append and submits an arbitrary source-order index", async () => {
    const user = userEvent.setup()
    const projected = inspection()
    projected.jobs.unshift({
      ...structuredClone(projected.jobs[0]),
      id: "prepare",
      index: 0,
    })
    projected.jobs[1].index = 1
    vi.mocked(inspectWorkflowJobs).mockResolvedValue(projected)
    vi.mocked(renderWorkflowJobs).mockResolvedValue({
      ...projected,
      revision: "opaque:jobs:inserted",
      yaml: `${yaml}# job.insert\n`,
    })
    renderEditor()

    await user.click(await screen.findByRole("button", { name: "Add job" }))
    const position = screen.getByRole("combobox", {
      name: "New job insertion position",
    })
    expect(position).toHaveTextContent("After job 2 (append)")
    await user.click(position)
    expect(await screen.findAllByRole("option")).toHaveLength(3)
    await user.click(
      screen.getByRole("option", {
        name: "After job 1; before job 2",
      }),
    )
    await user.type(screen.getByLabelText("New job ID"), "middle")
    await user.click(screen.getByRole("button", { name: "Add job to YAML" }))

    await waitFor(() =>
      expect(renderWorkflowJobs).toHaveBeenCalledWith(
        expect.objectContaining({
          operation: {
            type: "job.insert",
            job_id: "middle",
            index: 1,
            fields: {
              runs_on: { mode: "set", value: "picoclaw" },
            },
          },
        }),
        expect.any(AbortSignal),
      ),
    )
  })

  it.each([
    {
      relation: "before",
      index: 1,
      option: "After action 1; before action 2 (raw-only)",
    },
    {
      relation: "after",
      index: 2,
      option: "After action 2 (raw-only); before action 3",
    },
  ])(
    "inserts a new action $relation a raw-only action without moving it",
    async ({ index, option }) => {
      const user = userEvent.setup()
      const projected = inspection()
      projected.jobs[0].steps[1].editable = false
      projected.jobs[0].steps[1].reason = "Alias-backed action."
      projected.jobs[0].steps.push(
        step(2, "archive", "Archive", "function/workflow.state"),
      )
      vi.mocked(inspectWorkflowJobs).mockResolvedValue(projected)
      vi.mocked(renderWorkflowJobs).mockImplementation(async ({ operation }) =>
        operationResult(projected, operation),
      )
      renderEditor()

      await user.click(
        await screen.findByRole("button", { name: "Add action" }),
      )
      const position = screen.getByRole("combobox", {
        name: "New action insertion position",
      })
      expect(position).toHaveTextContent("After action 3 (append)")
      await user.click(position)
      expect(await screen.findAllByRole("option")).toHaveLength(4)
      await user.click(screen.getByRole("option", { name: option }))
      await user.type(
        screen.getByRole("textbox", { name: "Action target" }),
        "function/workflow.state",
      )
      await user.click(
        screen.getByRole("button", { name: "Add action to YAML" }),
      )

      await waitFor(() =>
        expect(renderWorkflowJobs).toHaveBeenCalledWith(
          expect.objectContaining({
            operation: {
              type: "step.insert",
              job_id: "review",
              index,
              fields: {
                uses: {
                  mode: "set",
                  value: "function/workflow.state",
                },
              },
            },
          }),
          expect.any(AbortSignal),
        ),
      )
    },
  )

  it("ignores stale render success after authoritative YAML changes", async () => {
    const user = userEvent.setup()
    const onYAMLChange = vi.fn()
    const base = inspection()
    const newerYAML = `${yaml}# authoritative\n`
    let resolveRender!: (
      value: WorkflowJobsInspection & { yaml: string },
    ) => void
    vi.mocked(inspectWorkflowJobs)
      .mockResolvedValueOnce(base)
      .mockResolvedValueOnce({
        ...base,
        revision: "opaque:jobs:authoritative",
      })
    vi.mocked(renderWorkflowJobs).mockImplementation(
      () =>
        new Promise((resolve) => {
          resolveRender = resolve
        }),
    )
    const rendered = renderEditor({ onYAMLChange })
    const actionSection = await selectedActionSection()
    await chooseMutation(
      user,
      actionSection,
      "Display name mutation",
      "Set value",
    )
    await user.click(
      screen.getByRole("button", { name: "Apply fields to YAML" }),
    )

    rendered.rerender(editorElement({ yaml: newerYAML, onYAMLChange }))
    resolveRender({
      ...base,
      revision: "opaque:jobs:stale-result",
      yaml: `${yaml}# stale\n`,
    })

    await waitFor(() => expect(inspectWorkflowJobs).toHaveBeenCalledTimes(2))
    expect(onYAMLChange).not.toHaveBeenCalled()
  })

  it("ignores stale render failures after authoritative YAML changes", async () => {
    const user = userEvent.setup()
    const base = inspection()
    const newerYAML = `${yaml}# authoritative\n`
    let rejectRender!: (reason: Error) => void
    vi.mocked(inspectWorkflowJobs)
      .mockResolvedValueOnce(base)
      .mockResolvedValueOnce({
        ...base,
        revision: "opaque:jobs:authoritative",
      })
    vi.mocked(renderWorkflowJobs).mockImplementation(
      () =>
        new Promise((_, reject) => {
          rejectRender = reject
        }),
    )
    const rendered = renderEditor()
    const actionSection = await selectedActionSection()
    await chooseMutation(
      user,
      actionSection,
      "Display name mutation",
      "Set value",
    )
    await user.click(
      screen.getByRole("button", { name: "Apply fields to YAML" }),
    )

    rendered.rerender(editorElement({ yaml: newerYAML }))
    rejectRender(new Error("stale failure must not render"))

    await waitFor(() => expect(inspectWorkflowJobs).toHaveBeenCalledTimes(2))
    expect(screen.queryByText("stale failure must not render")).toBeNull()
  })

  it("wires insert, delete, and move controls to surgical operations", async () => {
    const user = userEvent.setup()
    const base = inspection()
    vi.mocked(inspectWorkflowJobs).mockResolvedValue(base)
    vi.mocked(renderWorkflowJobs).mockImplementation(async ({ operation }) =>
      operationResult(base, operation),
    )

    const moveEditor = renderEditor()
    await screen.findByText("Job graph")
    await user.click(screen.getByRole("button", { name: "Move action 1 down" }))
    await waitFor(() =>
      expect(renderWorkflowJobs).toHaveBeenCalledWith(
        expect.objectContaining({
          operation: {
            type: "step.move",
            job_id: "review",
            step_index: 0,
            to_index: 1,
          },
        }),
        expect.any(AbortSignal),
      ),
    )

    moveEditor.unmount()
    vi.mocked(renderWorkflowJobs).mockClear()
    const insertEditor = renderEditor()
    await screen.findByText("Job graph")
    await user.click(screen.getByRole("button", { name: "Add action" }))
    await user.type(
      screen.getByRole("textbox", { name: "Action target" }),
      "function/workflow.state",
    )
    await user.click(screen.getByRole("button", { name: "Add action to YAML" }))
    await waitFor(() =>
      expect(renderWorkflowJobs).toHaveBeenLastCalledWith(
        expect.objectContaining({
          operation: expect.objectContaining({
            type: "step.insert",
            job_id: "review",
            fields: {
              uses: { mode: "set", value: "function/workflow.state" },
            },
          }),
        }),
        expect.any(AbortSignal),
      ),
    )

    insertEditor.unmount()
    vi.mocked(renderWorkflowJobs).mockClear()
    renderEditor()
    await screen.findByText("Job graph")
    await user.click(screen.getByRole("button", { name: "Delete action 1" }))
    await user.click(screen.getByRole("button", { name: "Confirm delete" }))
    await waitFor(() =>
      expect(renderWorkflowJobs).toHaveBeenLastCalledWith(
        expect.objectContaining({
          operation: {
            type: "step.delete",
            job_id: "review",
            step_index: 0,
          },
        }),
        expect.any(AbortSignal),
      ),
    )
  })
})

async function selectedActionSection() {
  const heading = await screen.findByRole("heading", { name: "Action 1" })
  const section = heading.closest("section")
  if (section == null) {
    throw new Error("selected action section not found")
  }
  return section
}

async function chooseMutation(
  user: ReturnType<typeof userEvent.setup>,
  container: HTMLElement,
  label: string,
  option: string,
) {
  await user.click(within(container).getByRole("combobox", { name: label }))
  await user.click(await screen.findByRole("option", { name: option }))
}

function renderEditor(overrides: EditorOverrides = {}) {
  return render(editorElement(overrides))
}

interface EditorOverrides {
  yaml?: string
  onYAMLChange?: (yaml: string) => void
  onStructuredActionsChange?: (state: WorkflowStructuredActionsState) => void
}

function editorElement(overrides: EditorOverrides = {}) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return (
    <QueryClientProvider client={client}>
      <WorkflowJobEditor
        yaml={overrides.yaml ?? yaml}
        disabled={false}
        onYAMLChange={overrides.onYAMLChange ?? vi.fn()}
        onActivityChange={vi.fn()}
        onStructuredActionsChange={
          overrides.onStructuredActionsChange ??
          vi.fn<(state: WorkflowStructuredActionsState) => void>()
        }
        onOpenYAML={vi.fn()}
      />
    </QueryClientProvider>
  )
}

function inspection(): WorkflowJobsInspection {
  return {
    revision: "opaque:jobs:base",
    editable: true,
    complete: true,
    limits: [],
    jobs: [
      {
        id: "review",
        index: 0,
        editable: true,
        advanced_fields_present: false,
        steps_present: true,
        fields: {
          name: field("Review"),
          runs_on: field("picoclaw"),
          needs: field(["prepare"]),
          uses: absent(),
          if: absent(),
          continue_on_error: field(false),
          with: absent(),
          secrets: absent(),
          outputs: absent(),
          context: absent(),
        },
        steps: [
          step(0, "summarize", "", "agent/main"),
          step(1, "notify", "Notify", "tool/message"),
        ],
      },
    ],
    validation: {
      valid: true,
      validated_at: "2026-07-30T00:00:00Z",
    },
  }
}

function step(
  index: number,
  id: string,
  name: string,
  uses: string,
): WorkflowJobsInspection["jobs"][number]["steps"][number] {
  return {
    index,
    editable: true,
    advanced_fields_present: false,
    fields: {
      id: field(id),
      name: field(name),
      uses: field(uses),
      if: field("${{ true }}"),
      continue_on_error: field(false),
      with: field({
        prompt: "",
        enabled: false,
      }),
      context: absent(),
    },
  }
}

function field<Value>(value: Value): WorkflowEditorField<Value> {
  return { present: true, value }
}

function absent<Value>(): WorkflowEditorField<Value> {
  return { present: false, value: null }
}

function operationResult(
  current: WorkflowJobsInspection,
  operation: WorkflowJobEditorOperation,
): WorkflowJobsInspection & { yaml: string } {
  const next = structuredClone(current)
  const job = next.jobs.find((candidate) => candidate.id === "review")
  if (job != null && operation.type === "step.move") {
    const [moved] = job.steps.splice(operation.step_index, 1)
    job.steps.splice(operation.to_index, 0, moved)
    job.steps.forEach((item, index) => {
      item.index = index
    })
  }
  if (job != null && operation.type === "step.insert") {
    job.steps.splice(
      operation.index,
      0,
      step(operation.index, "", "", String(operation.fields.uses?.value ?? "")),
    )
    job.steps.forEach((item, index) => {
      item.index = index
    })
  }
  if (job != null && operation.type === "step.delete") {
    job.steps.splice(operation.step_index, 1)
    job.steps.forEach((item, index) => {
      item.index = index
    })
  }
  return {
    ...next,
    revision: `opaque:jobs:${operation.type}`,
    yaml: `${yaml}# ${operation.type}\n`,
  }
}

const _jsonTypeCheck: WorkflowEditorJSONValue = {
  empty: "",
  enabled: false,
}
void _jsonTypeCheck
