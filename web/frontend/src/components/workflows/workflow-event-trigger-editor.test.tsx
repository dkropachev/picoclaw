import { render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { beforeEach, describe, expect, it, vi } from "vitest"

import {
  inspectWorkflowEventTrigger,
  renderWorkflowEventTrigger,
} from "@/api/workflows"
import { WorkflowEventTriggerEditor } from "@/components/workflows/workflow-event-trigger-editor"

vi.mock("@/api/workflows", () => ({
  inspectWorkflowEventTrigger: vi.fn(),
  renderWorkflowEventTrigger: vi.fn(),
}))

const yaml = "name: Event workflow\non:\n  workflow_call:\n"

describe("WorkflowEventTriggerEditor", () => {
  beforeEach(() => {
    vi.mocked(inspectWorkflowEventTrigger).mockReset()
    vi.mocked(renderWorkflowEventTrigger).mockReset()
  })

  it("inspects, edits, and renders an event trigger without losing special attribute keys", async () => {
    const user = userEvent.setup()
    const onYAMLChange = vi.fn()
    const onInspectionChange = vi.fn()
    vi.mocked(inspectWorkflowEventTrigger).mockResolvedValue({
      revision: "rev-1",
      editable: true,
      event_trigger: {
        sources: ["github"],
        types: ["issues.opened"],
        actor: { types: ["user"] },
      },
      validation: validValidation(),
    })
    vi.mocked(renderWorkflowEventTrigger).mockImplementation(
      async ({ event_trigger }) => ({
        yaml: "name: Rendered event workflow",
        revision: "rev-2",
        editable: true,
        event_trigger,
        validation: validValidation(),
      }),
    )

    render(
      <WorkflowEventTriggerEditor
        yaml={yaml}
        disabled={false}
        onYAMLChange={onYAMLChange}
        onInspectionChange={onInspectionChange}
        onOpenYAML={vi.fn()}
      />,
    )

    expect(await screen.findByLabelText("Sources")).toHaveValue("github")
    expect(screen.getByLabelText("Event types")).toHaveValue("issues.opened")
    expect(screen.getByLabelText("Enable actor filters")).toBeChecked()
    expect(screen.getByLabelText("Types")).toHaveValue("user")

    await user.click(
      screen.getAllByRole("button", { name: "Add attribute" })[0],
    )
    await user.type(screen.getByLabelText("Attribute name"), "__proto__")
    await user.type(screen.getByLabelText("Value patterns"), "trusted")
    await user.click(screen.getByRole("button", { name: "Apply to YAML" }))

    await waitFor(() => expect(renderWorkflowEventTrigger).toHaveBeenCalled())
    const request = vi.mocked(renderWorkflowEventTrigger).mock.calls[0]?.[0]
    expect(request).toMatchObject({
      yaml,
      revision: "rev-1",
      event_trigger: {
        sources: ["github"],
        types: ["issues.opened"],
        actor: { types: ["user"] },
      },
    })
    const attributes = request?.event_trigger?.attributes
    expect(attributes && Object.hasOwn(attributes, "__proto__")).toBe(true)
    expect(attributes?.["__proto__"]).toEqual(["trusted"])
    expect(onYAMLChange).toHaveBeenCalledWith("name: Rendered event workflow")
  })

  it("preserves exact whitespace in existing attribute keys", async () => {
    const user = userEvent.setup()
    vi.mocked(inspectWorkflowEventTrigger).mockResolvedValue({
      revision: "rev-1",
      editable: true,
      event_trigger: {
        sources: ["github"],
        attributes: { " repository ": ["acme/*"] },
      },
      validation: validValidation(),
    })
    vi.mocked(renderWorkflowEventTrigger).mockImplementation(
      async ({ event_trigger }) => ({
        yaml: "name: Rendered event workflow",
        revision: "rev-2",
        editable: true,
        event_trigger,
        validation: validValidation(),
      }),
    )

    renderEditor()
    await user.type(await screen.findByLabelText("Sources"), "\nchat")
    await user.click(screen.getByRole("button", { name: "Apply to YAML" }))

    await waitFor(() => expect(renderWorkflowEventTrigger).toHaveBeenCalled())
    const attributes = vi.mocked(renderWorkflowEventTrigger).mock.calls[0]?.[0]
      .event_trigger?.attributes
    expect(Object.keys(attributes ?? {})).toContain(" repository ")
    expect(attributes?.[" repository "]).toEqual(["acme/*"])
    expect(Object.hasOwn(attributes ?? {}, "repository")).toBe(false)
  })

  it("requires an explicit filter before applying a newly enabled trigger", async () => {
    const user = userEvent.setup()
    vi.mocked(inspectWorkflowEventTrigger).mockResolvedValue({
      revision: "rev-1",
      editable: true,
      event_trigger: null,
      validation: validValidation(),
    })

    renderEditor()

    await user.click(
      await screen.findByRole("switch", {
        name: "Enable durable event trigger",
      }),
    )

    expect(screen.getByText(/Add at least one filter/)).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Apply to YAML" })).toBeDisabled()
    expect(renderWorkflowEventTrigger).not.toHaveBeenCalled()
  })

  it("falls back to the authoritative YAML editor for unsupported shapes", async () => {
    const user = userEvent.setup()
    const onOpenYAML = vi.fn()
    vi.mocked(inspectWorkflowEventTrigger).mockResolvedValue({
      revision: "rev-1",
      editable: false,
      reason: "YAML aliases are not safe to project.",
      event_trigger: null,
      validation: validValidation(),
    })

    renderEditor({ onOpenYAML })

    expect(
      await screen.findByText("YAML aliases are not safe to project."),
    ).toBeInTheDocument()
    await user.click(screen.getByRole("button", { name: "Open YAML" }))
    expect(onOpenYAML).toHaveBeenCalledOnce()
  })

  it("ignores a rendered result when the authoritative YAML changed in flight", async () => {
    const user = userEvent.setup()
    const onYAMLChange = vi.fn()
    let resolveRender:
      | ((
          value: Awaited<ReturnType<typeof renderWorkflowEventTrigger>>,
        ) => void)
      | undefined
    vi.mocked(inspectWorkflowEventTrigger).mockResolvedValue({
      revision: "rev-1",
      editable: true,
      event_trigger: { sources: ["github"] },
      validation: validValidation(),
    })
    vi.mocked(renderWorkflowEventTrigger).mockImplementation(
      () =>
        new Promise((resolve) => {
          resolveRender = resolve
        }),
    )

    const view = renderEditor({ onYAMLChange })
    const sources = await screen.findByLabelText("Sources")
    await user.type(sources, "\nchat")
    await user.click(screen.getByRole("button", { name: "Apply to YAML" }))
    await waitFor(() => expect(renderWorkflowEventTrigger).toHaveBeenCalled())

    view.rerender(
      editor({ yaml: "name: New authoritative YAML", onYAMLChange }),
    )
    resolveRender?.({
      yaml: "stale rendered yaml",
      revision: "rev-2",
      editable: true,
      event_trigger: { sources: ["github", "chat"] },
      validation: validValidation(),
    })

    expect(
      await screen.findByText(/YAML changed while the trigger was rendering/),
    ).toBeInTheDocument()
    expect(onYAMLChange).not.toHaveBeenCalled()
  })
})

function validValidation() {
  return {
    valid: true,
    validated_at: "2026-07-29T12:00:00Z",
  }
}

function renderEditor(props: Partial<Parameters<typeof editor>[0]> = {}) {
  return render(editor(props))
}

function editor({
  yaml: value = yaml,
  onYAMLChange = vi.fn(),
  onInspectionChange = vi.fn(),
  onOpenYAML = vi.fn(),
}: {
  yaml?: string
  onYAMLChange?: (yaml: string) => void
  onInspectionChange?: Parameters<
    typeof WorkflowEventTriggerEditor
  >[0]["onInspectionChange"]
  onOpenYAML?: () => void
} = {}) {
  return (
    <WorkflowEventTriggerEditor
      yaml={value}
      disabled={false}
      onYAMLChange={onYAMLChange}
      onInspectionChange={onInspectionChange}
      onOpenYAML={onOpenYAML}
    />
  )
}
