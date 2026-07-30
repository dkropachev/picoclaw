import {
  QueryClient,
  type QueryClientConfig,
  QueryClientProvider,
} from "@tanstack/react-query"
import { render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import type { ReactElement } from "react"
import { beforeEach, describe, expect, it, vi } from "vitest"

import {
  type WorkflowDefinitionInspection,
  inspectPublishedWorkflowDefinition,
  inspectWorkflowTemplate,
} from "@/api/workflows"

import { WorkflowDefinitionInspector } from "./workflow-definition-inspector"

vi.mock("@/api/workflows", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/workflows")>()
  return {
    ...actual,
    inspectPublishedWorkflowDefinition: vi.fn(),
    inspectWorkflowTemplate: vi.fn(),
  }
})

describe("WorkflowDefinitionInspector", () => {
  beforeEach(() => {
    vi.mocked(inspectPublishedWorkflowDefinition).mockReset()
    vi.mocked(inspectWorkflowTemplate).mockReset()
  })

  it("shows safe trigger, topology, dependency, and effect projections", async () => {
    vi.mocked(inspectPublishedWorkflowDefinition).mockResolvedValue(
      inspection(),
    )
    renderWithClient(
      <WorkflowDefinitionInspector
        target={{ kind: "published", ref: "workflows/review.yml" }}
      />,
    )

    expect(screen.getByRole("status")).toHaveTextContent(
      "Inspecting workflow structure",
    )
    expect(await screen.findByText("schedule")).toBeInTheDocument()
    expect(screen.getByText("channel message")).toBeInTheDocument()
    expect(screen.getByText("runtime event")).toBeInTheDocument()
    expect(screen.getByText("workflow call")).toBeInTheDocument()
    expect(screen.getByText(/"session_configured": true/)).toBeInTheDocument()
    expect(screen.getByText(/"session_filter_count": 2/)).toBeInTheDocument()
    expect(screen.getByText(/"has_default": false/)).toBeInTheDocument()
    expect(screen.getByText(/"api_token"/)).toBeInTheDocument()
    expect(
      screen.getByRole("region", {
        name: "Published definition: workflows/review.yml inspection details",
      }),
    ).toHaveAttribute("tabindex", "0")
    expect(screen.getByLabelText("schedule trigger details")).toHaveAttribute(
      "tabindex",
      "0",
    )
    expect(screen.getByText("review")).toBeInTheDocument()
    expect(screen.getByTitle("Step ID: inspect")).toHaveTextContent("#inspect")
    expect(screen.getByText("agent/reviewer")).toBeInTheDocument()
    expect(screen.getByText("Possible effects")).toBeInTheDocument()
    expect(
      screen.getByText("external state change possible"),
    ).toBeInTheDocument()
    expect(screen.getByText(/Conservative preview/)).toBeInTheDocument()
    expect(inspectPublishedWorkflowDefinition).toHaveBeenCalledWith(
      "workflows/review.yml",
      expect.any(AbortSignal),
    )
  })

  it("shows invalid, incomplete, and empty states without inventing details", async () => {
    vi.mocked(inspectWorkflowTemplate).mockResolvedValue(
      inspection({
        source: { kind: "template", template_name: "empty" },
        complete: false,
        validation: {
          valid: false,
          issue_count: 3,
          issues: [
            { code: "schedule_cron_invalid", scope: "trigger.schedule" },
          ],
          truncated: true,
        },
        triggers: emptyTriggers(),
        jobs: [],
        dependencies: [],
        effects: [],
        limits: [
          "jobs_truncated",
          "unsafe_fields_omitted",
          "validation_issues_truncated",
        ],
      }),
    )
    renderWithClient(
      <WorkflowDefinitionInspector
        target={{ kind: "template", name: "empty" }}
      />,
    )

    expect(
      await screen.findByText("Invalid workflow definition (3 issues)"),
    ).toBeInTheDocument()
    expect(screen.getByText("Inspection is incomplete")).toBeInTheDocument()
    expect(screen.getByText("jobs truncated")).toBeInTheDocument()
    expect(screen.getByText("unsafe fields omitted")).toBeInTheDocument()
    expect(
      screen.getByText(
        "No inspectable triggers, jobs, dependencies, or possible effects.",
      ),
    ).toBeInTheDocument()
  })

  it("offers an explicit retry after a bounded inspection error", async () => {
    const user = userEvent.setup()
    vi.mocked(inspectWorkflowTemplate)
      .mockRejectedValueOnce(new Error("Inspection temporarily unavailable."))
      .mockResolvedValueOnce(
        inspection({
          source: { kind: "template", template_name: "review" },
        }),
      )
    renderWithClient(
      <WorkflowDefinitionInspector
        target={{ kind: "template", name: "review" }}
      />,
    )

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Inspection temporarily unavailable.",
    )
    await user.click(screen.getByRole("button", { name: "Retry inspection" }))
    expect(await screen.findByText("Inspected")).toBeInTheDocument()
    expect(inspectWorkflowTemplate).toHaveBeenCalledTimes(2)
  })

  it("defers a collapsed template request until review is opened", async () => {
    const user = userEvent.setup()
    vi.mocked(inspectWorkflowTemplate).mockResolvedValue(
      inspection({
        source: { kind: "template", template_name: "review" },
      }),
    )
    renderWithClient(
      <WorkflowDefinitionInspector
        target={{ kind: "template", name: "review" }}
        defaultOpen={false}
      />,
    )

    expect(screen.getByText("Review")).toBeInTheDocument()
    expect(inspectWorkflowTemplate).not.toHaveBeenCalled()
    await user.click(
      screen.getByRole("button", {
        name: "Built-in definition: review",
      }),
    )
    expect(await screen.findByText("Inspected")).toBeInTheDocument()
    expect(inspectWorkflowTemplate).toHaveBeenCalledWith(
      "review",
      expect.any(AbortSignal),
    )
  })

  it("aborts an active inspection when its panel is closed", async () => {
    const user = userEvent.setup()
    let requestSignal: AbortSignal | undefined
    vi.mocked(inspectWorkflowTemplate).mockImplementation((_name, signal) => {
      requestSignal = signal
      return new Promise<WorkflowDefinitionInspection>(() => undefined)
    })
    renderWithClient(
      <WorkflowDefinitionInspector
        target={{ kind: "template", name: "review" }}
      />,
    )

    await waitFor(() => {
      expect(requestSignal).toBeDefined()
    })
    await user.click(
      screen.getByRole("button", {
        name: "Built-in definition: review",
      }),
    )
    await waitFor(() => {
      expect(requestSignal?.aborted).toBe(true)
    })
    expect(screen.getByText("Review")).toBeInTheDocument()
  })

  it("renders reusable steps and labels an intentionally omitted target", async () => {
    vi.mocked(inspectPublishedWorkflowDefinition).mockResolvedValue(
      inspection({
        complete: false,
        validation: {
          valid: false,
          issue_count: 1,
          issues: [{ code: "reusable_steps_unsupported", scope: "jobs" }],
          truncated: false,
        },
        jobs: [
          {
            id: "reuse",
            kind: "reusable",
            reusable_target: "workflows/child.yml",
            steps: [{ index: 0, kind: "mcp" }],
          },
        ],
        dependencies: [],
        effects: [
          {
            kind: "external_state_change_possible",
            occurrences: 1,
          },
        ],
        limits: ["unsafe_fields_omitted"],
      }),
    )
    renderWithClient(
      <WorkflowDefinitionInspector
        target={{ kind: "published", ref: "workflows/review.yml" }}
      />,
    )

    expect(await screen.findByText("workflows/child.yml")).toBeInTheDocument()
    expect(screen.getByText("target omitted")).toBeInTheDocument()
    expect(screen.getByText("unsafe fields omitted")).toBeInTheDocument()
    expect(
      screen.getByText("external state change possible"),
    ).toBeInTheDocument()
  })

  it("keeps a late response from a previous identity out of the panel", async () => {
    const first = deferred<WorkflowDefinitionInspection>()
    const second = deferred<WorkflowDefinitionInspection>()
    vi.mocked(inspectPublishedWorkflowDefinition).mockImplementation((ref) =>
      ref === "workflows/first.yml" ? first.promise : second.promise,
    )
    const client = queryClient()
    const view = render(
      <QueryClientProvider client={client}>
        <WorkflowDefinitionInspector
          target={{ kind: "published", ref: "workflows/first.yml" }}
        />
      </QueryClientProvider>,
    )

    view.rerender(
      <QueryClientProvider client={client}>
        <WorkflowDefinitionInspector
          target={{ kind: "published", ref: "workflows/second.yml" }}
        />
      </QueryClientProvider>,
    )
    second.resolve(
      inspection({
        source: { kind: "published", ref: "workflows/second.yml" },
        revision: "second-revision",
      }),
    )
    expect(await screen.findByTitle("workflows/second.yml")).toBeInTheDocument()

    first.resolve(
      inspection({
        source: { kind: "published", ref: "workflows/first.yml" },
        revision: "first-revision",
      }),
    )
    await waitFor(() => {
      expect(screen.queryByTitle("workflows/first.yml")).not.toBeInTheDocument()
      expect(screen.getByTitle("second-revision")).toBeInTheDocument()
    })
  })

  it("hides cached inspection data while an invalidation refetch is pending", async () => {
    const next = deferred<WorkflowDefinitionInspection>()
    vi.mocked(inspectPublishedWorkflowDefinition)
      .mockResolvedValueOnce(inspection())
      .mockReturnValueOnce(next.promise)
    const client = queryClient()
    render(
      <QueryClientProvider client={client}>
        <WorkflowDefinitionInspector
          target={{ kind: "published", ref: "workflows/review.yml" }}
        />
      </QueryClientProvider>,
    )

    expect(await screen.findByText("Inspected")).toBeInTheDocument()
    void client.invalidateQueries({
      queryKey: [
        "workflows",
        "definition-inspections",
        "published",
        "workflows/review.yml",
      ],
      exact: true,
    })
    await waitFor(() => {
      expect(screen.getByRole("status")).toHaveTextContent(
        "Inspecting workflow structure",
      )
      expect(screen.queryByText("Inspected")).not.toBeInTheDocument()
      expect(screen.queryByTitle("sha256:inspection")).not.toBeInTheDocument()
    })

    next.resolve(
      inspection({
        revision: "sha256:refetched",
      }),
    )
    expect(await screen.findByTitle("sha256:refetched")).toBeInTheDocument()
  })
})

function renderWithClient(element: ReactElement, config?: QueryClientConfig) {
  return render(
    <QueryClientProvider client={queryClient(config)}>
      {element}
    </QueryClientProvider>,
  )
}

function queryClient(config?: QueryClientConfig) {
  return new QueryClient({
    defaultOptions: {
      queries: {
        retry: false,
        gcTime: Number.POSITIVE_INFINITY,
      },
    },
    ...config,
  })
}

function inspection(
  overrides: Partial<WorkflowDefinitionInspection> = {},
): WorkflowDefinitionInspection {
  return {
    source: { kind: "published", ref: "workflows/review.yml" },
    revision: "sha256:inspection",
    complete: true,
    validation: {
      valid: true,
      issue_count: 0,
      issues: [],
      truncated: false,
    },
    triggers: {
      ...emptyTriggers(),
      schedule: {
        present: true,
        projected: true,
        value: [{ cron: "0 9 * * 1" }],
      },
      channel_message: {
        present: true,
        projected: true,
        value: {
          channels: ["github"],
          session_configured: true,
          delivery_configured: false,
        },
      },
      runtime_event: {
        present: true,
        projected: true,
        value: {
          kinds: ["workflow.run.failed"],
          session_filter_present: true,
          session_filter_count: 2,
        },
      },
      workflow_call: {
        present: true,
        projected: true,
        value: {
          inputs: {
            ticket: {
              type: "string",
              required: true,
              has_default: false,
            },
          },
          secrets: { api_token: { required: true } },
          outputs: ["summary"],
        },
      },
    },
    jobs: [
      {
        id: "review",
        kind: "steps",
        steps: [
          {
            index: 0,
            id: "inspect",
            kind: "agent",
            target: "agent/reviewer",
          },
        ],
      },
    ],
    dependencies: [{ kind: "agent", target: "reviewer", occurrences: 1 }],
    effects: [{ kind: "external_state_change_possible", occurrences: 1 }],
    limits: [],
    ...overrides,
  }
}

function emptyTriggers(): WorkflowDefinitionInspection["triggers"] {
  return {
    manual: { present: false, projected: true },
    schedule: { present: false, projected: true },
    channel_message: { present: false, projected: true },
    command: { present: false, projected: true },
    runtime_event: { present: false, projected: true },
    event: { present: false, projected: true },
    workflow_call: { present: false, projected: true },
  }
}

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((resolvePromise) => {
    resolve = resolvePromise
  })
  return { promise, resolve }
}
