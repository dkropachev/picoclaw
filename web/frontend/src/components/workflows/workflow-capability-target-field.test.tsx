import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { useState } from "react"
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest"

import {
  type WorkflowAuthoringCapabilities,
  getWorkflowAuthoringCapabilities,
} from "@/api/workflows"

import { WorkflowCapabilityTargetField } from "./workflow-capability-target-field"

vi.mock("@/api/workflows", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/workflows")>()),
  getWorkflowAuthoringCapabilities: vi.fn(),
}))

describe("WorkflowCapabilityTargetField", () => {
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
    vi.mocked(getWorkflowAuthoringCapabilities).mockReset()
  })

  it("loads the shared capability query lazily and fills only ready targets", async () => {
    const user = userEvent.setup()
    const onChange = vi.fn()
    vi.mocked(getWorkflowAuthoringCapabilities).mockResolvedValue(catalog())

    renderField(onChange)
    expect(getWorkflowAuthoringCapabilities).not.toHaveBeenCalled()

    await user.click(
      screen.getByRole("button", { name: "Choose action capability" }),
    )
    expect(await screen.findByText("tool/message")).toBeInTheDocument()
    expect(
      screen.getByRole("button", { name: /reviewer.*Agent/i }),
    ).toBeDisabled()
    await user.click(screen.getByRole("button", { name: /message.*Tool/i }))
    expect(onChange).toHaveBeenCalledWith("tool/message")
  })

  it("keeps manual entry available when the catalog fails", async () => {
    const user = userEvent.setup()
    const onChange = vi.fn()
    vi.mocked(getWorkflowAuthoringCapabilities).mockRejectedValue(
      new Error("unavailable"),
    )

    renderField(onChange)
    await user.type(
      screen.getByLabelText("Action target"),
      "mcp/private/advanced",
    )
    expect(onChange).toHaveBeenLastCalledWith("mcp/private/advanced")
    await user.click(
      screen.getByRole("button", { name: "Choose action capability" }),
    )
    expect(
      await screen.findByText(/enter an exact target manually/i),
    ).toBeInTheDocument()
  })

  it("discloses an incomplete catalog without blocking exact manual entry", async () => {
    const user = userEvent.setup()
    const partial = catalog()
    partial.complete = false
    partial.limits = ["tools_truncated", "parameter_shapes_omitted"]
    vi.mocked(getWorkflowAuthoringCapabilities).mockResolvedValue(partial)

    renderField(vi.fn())
    await user.click(
      screen.getByRole("button", { name: "Choose action capability" }),
    )

    expect(
      await screen.findByText(/capability catalog is partial/i),
    ).toBeInTheDocument()
    expect(screen.getByText(/tools omitted/i)).toBeInTheDocument()
    expect(screen.getByText(/enter its exact target manually/i)).toBeVisible()
    await user.keyboard("{Escape}")
    await user.type(
      screen.getByLabelText("Action target"),
      "tool/ready-but-omitted",
    )
    expect(screen.getByLabelText("Action target")).toHaveValue(
      "tool/ready-but-omitted",
    )
  })

  it("ignores a catalog response resolved after the picker closes", async () => {
    const user = userEvent.setup()
    let resolveCatalog!: (catalog: WorkflowAuthoringCapabilities) => void
    vi.mocked(getWorkflowAuthoringCapabilities).mockImplementation(
      () =>
        new Promise((resolve) => {
          resolveCatalog = resolve
        }),
    )

    renderField(vi.fn())
    await user.click(
      screen.getByRole("button", { name: "Choose action capability" }),
    )
    await user.keyboard("{Escape}")
    resolveCatalog(catalog())

    await waitFor(() =>
      expect(
        screen.queryByRole("region", { name: "Action capability choices" }),
      ).not.toBeInTheDocument(),
    )
  })
})

function renderField(onChange: (value: string) => void) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(
    <QueryClientProvider client={client}>
      <TargetFieldHarness onChange={onChange} />
    </QueryClientProvider>,
  )
}

function TargetFieldHarness({
  onChange,
}: {
  onChange: (value: string) => void
}) {
  const [value, setValue] = useState("")
  return (
    <WorkflowCapabilityTargetField
      id="target"
      value={value}
      onChange={(nextValue) => {
        setValue(nextValue)
        onChange(nextValue)
      }}
    />
  )
}

function catalog(): WorkflowAuthoringCapabilities {
  return {
    complete: true,
    mcp_status: "ready",
    agents: [
      {
        id: "main",
        target: "agent/main",
        is_default: true,
        readiness: "ready",
      },
      {
        id: "reviewer",
        target: "agent/reviewer",
        is_default: false,
        readiness: "not_configured",
      },
    ],
    tools: [
      {
        name: "message",
        target: "tool/message",
        readiness: "ready",
        parameter_shape_projected: true,
        parameter_shape: {},
      },
    ],
    mcp_tools: [],
    functions: [
      {
        name: "git.diff",
        target: "function/git.diff",
        readiness: "ready",
      },
      {
        name: "git.filter",
        target: "function/git.filter",
        readiness: "ready",
      },
      {
        name: "git.inventory",
        target: "function/git.inventory",
        readiness: "ready",
      },
      {
        name: "workflow.artifact",
        target: "function/workflow.artifact",
        readiness: "ready",
      },
      {
        name: "workflow.state",
        target: "function/workflow.state",
        readiness: "ready",
      },
    ],
    limits: [],
  }
}
