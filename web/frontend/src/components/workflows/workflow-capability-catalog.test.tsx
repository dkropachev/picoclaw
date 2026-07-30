import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { act, render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import type { ReactElement } from "react"
import { beforeEach, describe, expect, it, vi } from "vitest"

import {
  type WorkflowAuthoringCapabilities,
  getWorkflowAuthoringCapabilities,
  workflowAuthoringCapabilitiesQueryKey,
} from "@/api/workflows"
import { copyText } from "@/lib/clipboard"

import { WorkflowCapabilityCatalog } from "./workflow-capability-catalog"

vi.mock("@/api/workflows", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/workflows")>()
  return {
    ...actual,
    getWorkflowAuthoringCapabilities: vi.fn(),
  }
})

vi.mock("@/lib/clipboard", () => ({
  copyText: vi.fn(),
}))

describe("WorkflowCapabilityCatalog", () => {
  beforeEach(() => {
    vi.mocked(getWorkflowAuthoringCapabilities).mockReset()
    vi.mocked(copyText).mockReset()
  })

  it("loads lazily and renders searchable structured safe targets", async () => {
    const user = userEvent.setup()
    vi.mocked(getWorkflowAuthoringCapabilities).mockResolvedValue(
      capabilities(),
    )
    renderWithClient(<WorkflowCapabilityCatalog />)

    expect(getWorkflowAuthoringCapabilities).not.toHaveBeenCalled()
    await user.click(
      screen.getByRole("button", { name: "Workflow capabilities" }),
    )

    expect(await screen.findByRole("heading", { name: "Agents" })).toBeVisible()
    expect(
      screen.getByRole("region", { name: "Workflow capability results" }),
    ).toHaveAttribute("tabindex", "0")
    expect(screen.getByText("agent/main")).toBeVisible()
    expect(screen.getByText("mcp/github/create_issue")).toBeVisible()
    expect(screen.getByText("Additional property values")).toBeVisible()
    expect(screen.getByText("required")).toBeVisible()
    expect(getWorkflowAuthoringCapabilities).toHaveBeenCalledWith(
      expect.any(AbortSignal),
    )

    const search = screen.getByRole("searchbox", {
      name: "Search capabilities",
    })
    expect(search).toHaveAttribute("type", "search")
    await user.type(search, "create_issue")
    expect(screen.queryByText("agent/main")).not.toBeInTheDocument()
    expect(screen.getByText("mcp/github/create_issue")).toBeVisible()

    await user.click(screen.getByRole("button", { name: "MCP tools" }))
    expect(screen.getByRole("button", { name: "MCP tools" })).toHaveAttribute(
      "aria-pressed",
      "false",
    )
    expect(
      screen.getByText(
        "No capabilities match the current search and category filters.",
      ),
    ).toBeVisible()

    await user.clear(search)
    await user.type(search, "é".repeat(200))
    expect(
      new TextEncoder().encode((search as HTMLInputElement).value),
    ).toHaveLength(256)
  })

  it("copies only ready exact targets and reports clipboard success or failure", async () => {
    const user = userEvent.setup()
    vi.mocked(getWorkflowAuthoringCapabilities).mockResolvedValue(
      capabilities(),
    )
    vi.mocked(copyText)
      .mockResolvedValueOnce(true)
      .mockResolvedValueOnce(false)
      .mockRejectedValueOnce(new Error("private clipboard failure"))
    renderWithClient(<WorkflowCapabilityCatalog />)
    await user.click(
      screen.getByRole("button", { name: "Workflow capabilities" }),
    )
    await screen.findByText("tool/message")

    expect(
      screen.getByRole("button", { name: "Copy agent/reviewer" }),
    ).toBeDisabled()
    await user.click(screen.getByRole("button", { name: "Copy tool/message" }))
    expect(copyText).toHaveBeenCalledWith("tool/message")
    expect(await screen.findByText("Target copied.")).toBeVisible()
    expect(
      screen.getByRole("button", { name: "Copied tool/message" }),
    ).toBeVisible()

    await user.click(
      screen.getByRole("button", {
        name: "Copy function/workflow.state",
      }),
    )
    expect(
      await screen.findByText(
        "Could not copy the target. Copy it from the text above.",
      ),
    ).toBeVisible()

    await user.click(
      screen.getByRole("button", {
        name: "Copy mcp/github/create_issue",
      }),
    )
    await waitFor(() =>
      expect(copyText).toHaveBeenNthCalledWith(3, "mcp/github/create_issue"),
    )
    await waitFor(() =>
      expect(
        screen.getAllByText(
          "Could not copy the target. Copy it from the text above.",
        ),
      ).toHaveLength(2),
    )
  })

  it("shows partial, empty, error, and retry states without inventing rows", async () => {
    const user = userEvent.setup()
    const partial = capabilities()
    partial.complete = false
    partial.mcp_status = "unavailable"
    partial.mcp_tools = []
    partial.tools[0].parameter_shape_projected = false
    delete partial.tools[0].parameter_shape
    partial.limits = ["parameter_shapes_omitted"]
    vi.mocked(getWorkflowAuthoringCapabilities).mockRejectedValue(
      new Error("Temporary catalog failure."),
    )
    renderWithClient(<WorkflowCapabilityCatalog />)

    await user.click(
      screen.getByRole("button", { name: "Workflow capabilities" }),
    )
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Temporary catalog failure.",
    )
    const retry = deferred<WorkflowAuthoringCapabilities>()
    vi.mocked(getWorkflowAuthoringCapabilities).mockReturnValue(retry.promise)
    await user.click(screen.getByRole("button", { name: "Retry capabilities" }))
    expect(
      screen.getByRole("button", { name: "Retrying capabilities" }),
    ).toBeDisabled()
    retry.resolve(partial)
    expect(await screen.findByText("Partial capability catalog")).toBeVisible()
    expect(screen.getByText(/MCP unavailable/)).toBeVisible()
    expect(
      screen.getByText("Parameter shape unavailable in the safe projection."),
    ).toBeVisible()

    const search = screen.getByRole("searchbox", {
      name: "Search capabilities",
    })
    await user.type(search, "does-not-exist")
    expect(
      screen.getByText(
        "No capabilities match the current search and category filters.",
      ),
    ).toBeVisible()
  })

  it("cancels the exact active query on close and fences late responses", async () => {
    const user = userEvent.setup()
    const first = deferred<WorkflowAuthoringCapabilities>()
    const second = deferred<WorkflowAuthoringCapabilities>()
    let firstSignal: AbortSignal | undefined
    vi.mocked(getWorkflowAuthoringCapabilities)
      .mockImplementationOnce((signal) => {
        firstSignal = signal
        return first.promise
      })
      .mockImplementationOnce(() => second.promise)
    renderWithClient(<WorkflowCapabilityCatalog />)

    await user.click(
      screen.getByRole("button", { name: "Workflow capabilities" }),
    )
    expect(await screen.findByRole("status")).toHaveTextContent("Loading")
    await user.click(screen.getByRole("button", { name: "Close" }))
    await waitFor(() => expect(firstSignal?.aborted).toBe(true))

    await user.click(
      screen.getByRole("button", { name: "Workflow capabilities" }),
    )
    const lateCatalog = capabilities()
    lateCatalog.tools[0].name = "late-response"
    lateCatalog.tools[0].target = "tool/late-response"
    first.resolve(lateCatalog)
    await act(async () => {
      await Promise.resolve()
    })
    expect(screen.getByRole("status")).toHaveTextContent("Loading")
    expect(screen.queryByText("tool/late-response")).not.toBeInTheDocument()
    expect(screen.queryByText("tool/message")).not.toBeInTheDocument()
    second.resolve(capabilities())
    expect(await screen.findByText("tool/message")).toBeVisible()
  })

  it("hides cached rows while an invalidation refetches the exact catalog", async () => {
    const user = userEvent.setup()
    const refreshed = deferred<WorkflowAuthoringCapabilities>()
    vi.mocked(getWorkflowAuthoringCapabilities)
      .mockResolvedValueOnce(capabilities())
      .mockReturnValueOnce(refreshed.promise)
    const { client } = renderWithClient(<WorkflowCapabilityCatalog />)

    await user.click(
      screen.getByRole("button", { name: "Workflow capabilities" }),
    )
    expect(await screen.findByText("tool/message")).toBeVisible()
    expect(client.getQueryData(workflowAuthoringCapabilitiesQueryKey)).toEqual(
      capabilities(),
    )
    act(() => {
      void client.invalidateQueries({
        queryKey: workflowAuthoringCapabilitiesQueryKey,
        exact: true,
      })
    })
    await waitFor(() => {
      expect(screen.getByRole("status")).toHaveTextContent("Loading")
      expect(screen.queryByText("tool/message")).not.toBeInTheDocument()
    })

    refreshed.resolve(capabilities())
    expect(await screen.findByText("tool/message")).toBeVisible()
  })
})

function renderWithClient(element: ReactElement) {
  const client = new QueryClient({
    defaultOptions: {
      queries: { retry: false, gcTime: Infinity },
    },
  })
  return {
    ...render(
      <QueryClientProvider client={client}>{element}</QueryClientProvider>,
    ),
    client,
  }
}

function capabilities(): WorkflowAuthoringCapabilities {
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
        parameter_shape: {
          type: "object",
          properties: [
            {
              name: "channel",
              required: false,
              shape: { type: "string" },
            },
            {
              name: "text",
              required: true,
              shape: { type: "string", enum: ["brief", "full"] },
            },
          ],
          additional_properties: { allowed: false },
        },
      },
    ],
    mcp_tools: [
      {
        server: "github",
        tool: "create_issue",
        target: "mcp/github/create_issue",
        readiness: "ready",
        parameter_shape_projected: true,
        parameter_shape: {
          type: "object",
          additional_properties: {
            shape: { type: "string" },
          },
        },
      },
    ],
    functions: [
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

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((resolvePromise) => {
    resolve = resolvePromise
  })
  return { promise, resolve }
}
