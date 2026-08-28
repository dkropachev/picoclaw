import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import type { ReactNode } from "react"
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest"

import type {
  WorkflowDevelopmentSession,
  WorkflowEditorField,
  WorkflowJobsInspection,
} from "@/api/workflows"
import { WorkflowAuthoringPage } from "@/components/workflows/workflow-authoring-page"

const workflowMocks = vi.hoisted(() => ({
  checkWorkflowDependencies: vi.fn(),
  getWorkflowDevelopment: vi.fn(),
  executeWorkflowDevelopmentTrigger: vi.fn(),
  inspectWorkflowJobs: vi.fn(),
  inspectWorkflowTriggers: vi.fn(),
  listWorkflowRuns: vi.fn(),
  listWorkflowTemplates: vi.fn(),
  listWorkflows: vi.fn(),
  simulateWorkflowDevelopmentTrigger: vi.fn(),
  testWorkflowDevelopment: vi.fn(),
}))

vi.mock("@/api/workflows", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/workflows")>()),
  ...workflowMocks,
}))

vi.mock("@/components/page-header", () => ({
  PageHeader: ({
    title,
    children,
  }: {
    title: string
    children?: ReactNode
  }) => (
    <header>
      <h1>{title}</h1>
      {children}
    </header>
  ),
}))

vi.mock("@tanstack/react-router", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-router")>()),
  Link: ({ children }: { children: ReactNode }) => <a href="/">{children}</a>,
}))

describe("WorkflowAuthoringPage jobs and actions integration", () => {
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
    for (const mock of Object.values(workflowMocks)) {
      mock.mockReset()
    }
    const session = developmentSession()
    workflowMocks.getWorkflowDevelopment.mockResolvedValue({ session })
    workflowMocks.listWorkflows.mockResolvedValue({ workflows: [] })
    workflowMocks.listWorkflowRuns.mockResolvedValue({ runs: [] })
    workflowMocks.listWorkflowTemplates.mockResolvedValue({ templates: [] })
    workflowMocks.inspectWorkflowTriggers.mockResolvedValue({
      revision: "opaque:triggers",
      triggers: emptyTriggerProjections(),
      validation: {
        valid: true,
        validated_at: "2026-07-30T00:00:00Z",
      },
    })
    workflowMocks.inspectWorkflowJobs.mockResolvedValue(jobsInspection())
    workflowMocks.checkWorkflowDependencies.mockResolvedValue({
      root_ref: session.target_workflow_ref,
      revision: "opaque:dependencies",
      ready: true,
      workflow_enabled: true,
      structural_ready: true,
      runtime_ready: true,
      dependencies: [],
      structural_issues: [],
    })
    workflowMocks.simulateWorkflowDevelopmentTrigger.mockImplementation(
      async (request) => {
        const scenario = request.scenario as {
          secrets?: Record<string, string>
        }
        return triggerSimulationResponse(
          Object.keys(scenario.secrets ?? {}).length,
        )
      },
    )
    workflowMocks.executeWorkflowDevelopmentTrigger.mockResolvedValue({
      session,
      result: { run_id: "wr_reviewed", status: "running" },
    })
  })

  it("exposes Jobs & actions inside Builder and blocks draft actions while its fields are dirty", async () => {
    const user = userEvent.setup()
    renderWorkflowsPage()

    await user.click(await screen.findByRole("tab", { name: "Builder" }))
    await user.click(screen.getByRole("tab", { name: "Jobs & actions" }))

    expect(await screen.findByText("Job graph")).toBeInTheDocument()
    expect(screen.getByText("Needs: prepare")).toBeInTheDocument()
    const action = await selectedActionSection()
    await user.click(
      within(action).getByRole("combobox", {
        name: "Display name mutation",
      }),
    )
    await user.click(await screen.findByRole("option", { name: "Set value" }))

    expect(
      await screen.findByText("Structured builder changes are pending."),
    ).toBeInTheDocument()
    for (const name of [
      "Save Draft",
      "Ask AI",
      "Scaffold",
      "Validate",
      "Review & execute",
      "Publish",
      "Discard",
    ]) {
      expect(screen.getByRole("button", { name })).toBeDisabled()
    }
    await user.click(screen.getByRole("tab", { name: "YAML" }))
    expect(screen.getByRole("tab", { name: "Builder" })).toHaveAttribute(
      "aria-selected",
      "true",
    )
  })

  it("executes only after an exact server review and hides scenario values", async () => {
    const user = userEvent.setup()
    renderWorkflowsPage()
    const inputs = await screen.findByLabelText("Inputs JSON")
    fireEvent.change(inputs, {
      target: { value: '{"ticket":"PIC-42"}' },
    })
    fireEvent.change(screen.getByLabelText("Session"), {
      target: { value: "workflow:review" },
    })
    fireEvent.change(screen.getByLabelText("Delivery JSON"), {
      target: { value: '{"channel":"engineering"}' },
    })
    fireEvent.change(screen.getByLabelText("Secrets JSON"), {
      target: {
        value:
          '{"github_token":"super-secret-value","api_key":"another-secret"}',
      },
    })

    const execute = screen.getByRole("button", { name: "Review & execute" })
    await waitFor(() => expect(execute).toBeEnabled())
    await user.click(execute)

    expect(workflowMocks.testWorkflowDevelopment).not.toHaveBeenCalled()
    expect(
      workflowMocks.executeWorkflowDevelopmentTrigger,
    ).not.toHaveBeenCalled()
    const dialog = screen.getByRole("dialog", {
      name: "Review trigger execution",
    })
    expect(dialog).not.toHaveTextContent('{"ticket":"PIC-42"}')
    expect(dialog).not.toHaveTextContent("workflow:review")
    expect(dialog).not.toHaveTextContent('{"channel":"engineering"}')
    expect(dialog).not.toHaveTextContent("github_token")
    expect(dialog).not.toHaveTextContent("api_key")
    expect(dialog).toHaveTextContent("Provided secrets")
    expect(dialog).toHaveTextContent("2")
    expect(dialog).not.toHaveTextContent("super-secret-value")
    expect(dialog).not.toHaveTextContent("another-secret")

    await user.click(
      within(dialog).getByRole("switch", {
        name: "I reviewed this server simulation and its possible effects",
      }),
    )
    fireEvent.change(inputs, {
      target: { value: '{"ticket":"PIC-43"}' },
    })
    expect(
      screen.queryByRole("dialog", { name: "Review trigger execution" }),
    ).not.toBeInTheDocument()
    expect(
      workflowMocks.executeWorkflowDevelopmentTrigger,
    ).not.toHaveBeenCalled()

    await waitFor(() => expect(execute).toBeEnabled())
    await user.click(execute)
    const currentDialog = screen.getByRole("dialog", {
      name: "Review trigger execution",
    })
    await user.click(
      within(currentDialog).getByRole("switch", {
        name: "I reviewed this server simulation and its possible effects",
      }),
    )
    await user.click(
      within(currentDialog).getByRole("button", {
        name: "Confirm and execute",
      }),
    )

    await waitFor(() =>
      expect(
        workflowMocks.executeWorkflowDevelopmentTrigger,
      ).toHaveBeenCalledWith(
        expect.objectContaining({
          trigger: { type: "workflow_call" },
          scenario: expect.objectContaining({
            inputs: { ticket: "PIC-43" },
            secrets: {
              github_token: "super-secret-value",
              api_key: "another-secret",
            },
            session: "workflow:review",
            delivery: { channel: "engineering" },
          }),
        }),
        "review-token",
      ),
    )
    expect(
      workflowMocks.executeWorkflowDevelopmentTrigger,
    ).toHaveBeenCalledTimes(1)
    expect(workflowMocks.testWorkflowDevelopment).not.toHaveBeenCalled()
  })

  it("requires the same explicit review for an empty structured projection", async () => {
    const user = userEvent.setup()
    const empty = jobsInspection()
    empty.jobs = []
    workflowMocks.inspectWorkflowJobs.mockResolvedValue(empty)
    workflowMocks.simulateWorkflowDevelopmentTrigger.mockResolvedValue(
      triggerSimulationResponse(0, []),
    )
    renderWorkflowsPage()

    const execute = await screen.findByRole("button", {
      name: "Review & execute",
    })
    await waitFor(() => expect(execute).toBeEnabled())
    await user.click(execute)

    const dialog = screen.getByRole("dialog", {
      name: "Review trigger execution",
    })
    expect(dialog).toHaveTextContent(
      "No action target was included in the bounded server review",
    )
    expect(workflowMocks.testWorkflowDevelopment).not.toHaveBeenCalled()
    const confirm = within(dialog).getByRole("button", {
      name: "Confirm and execute",
    })
    expect(confirm).toBeDisabled()
    await user.click(
      within(dialog).getByRole("switch", {
        name: "I reviewed this server simulation and its possible effects",
      }),
    )
    await user.click(confirm)
    await waitFor(() =>
      expect(
        workflowMocks.executeWorkflowDevelopmentTrigger,
      ).toHaveBeenCalledOnce(),
    )
    expect(workflowMocks.testWorkflowDevelopment).not.toHaveBeenCalled()
  })

  it("renders only a server secret count without names or values", async () => {
    const user = userEvent.setup()
    renderWorkflowsPage()
    const secrets = Object.fromEntries(
      Array.from({ length: 20 }, (_, index) => [
        `secret_${index}_${"x".repeat(300)}`,
        `never-render-value-${index}`,
      ]),
    )
    fireEvent.change(await screen.findByLabelText("Secrets JSON"), {
      target: { value: JSON.stringify(secrets) },
    })
    const execute = screen.getByRole("button", { name: "Review & execute" })
    await waitFor(() => expect(execute).toBeEnabled())
    await user.click(execute)

    const dialog = screen.getByRole("dialog", {
      name: "Review trigger execution",
    })
    const secretsLabel = within(dialog).getByText("Provided secrets")
    expect(secretsLabel.nextElementSibling).toHaveTextContent("20")
    expect(dialog).not.toHaveTextContent("secret_0_")
    expect(dialog).not.toHaveTextContent("never-render-value")
    expect(workflowMocks.testWorkflowDevelopment).not.toHaveBeenCalled()
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

function renderWorkflowsPage() {
  const client = new QueryClient({
    defaultOptions: {
      queries: { retry: false },
      mutations: { retry: false },
    },
  })
  return render(
    <QueryClientProvider client={client}>
      <WorkflowAuthoringPage />
    </QueryClientProvider>,
  )
}

function developmentSession(): WorkflowDevelopmentSession {
  return {
    id: "dev-jobs-builder",
    session_revision: "opaque:session",
    draft_revision: "opaque:draft",
    base_target_revision: "opaque:base",
    reason: "new",
    status: "editing",
    prompt: "Review incoming tickets",
    target_workflow_ref: "workflows/review.yml",
    yaml: "name: Review\non:\n  workflow_call:\njobs:\n  review:\n    needs: prepare\n    runs-on: picoclaw\n    steps:\n      - id: summarize\n        uses: agent/main\n",
    validation: {
      valid: true,
      validated_at: "2026-07-30T00:00:00Z",
    },
    created_at: "2026-07-30T00:00:00Z",
    updated_at: "2026-07-30T00:00:00Z",
  }
}

function emptyTriggerProjections() {
  return {
    manual: { present: false, editable: true, value: null },
    schedule: { present: false, editable: true, value: null },
    channel_message: { present: false, editable: true, value: null },
    command: { present: false, editable: true, value: null },
    runtime_event: { present: false, editable: true, value: null },
    event: { present: false, editable: true, value: null },
    workflow_call: {
      present: true,
      editable: true,
      value: { inputs: {}, secrets: {}, outputs: {} },
    },
  }
}

function triggerSimulationResponse(
  secretCount: number,
  targets = ["agent/main"],
) {
  return {
    simulation: {
      selected_kind: "workflow_call",
      effective_kind: "workflow_call",
      present: true,
      matched: true,
      executable: true,
      reason: "matched",
      context_summary: {
        input_count: 1,
        secret_count: secretCount,
        has_event: false,
        has_session: true,
        has_delivery: true,
      },
    },
    review: {
      job_count: targets.length === 0 ? 0 : 1,
      step_count: targets.length,
      targets,
      effects:
        targets.length === 0
          ? []
          : [
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

function jobsInspection(): WorkflowJobsInspection {
  return {
    revision: "opaque:jobs",
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
          continue_on_error: absent(),
          with: absent(),
          secrets: absent(),
          outputs: absent(),
          context: absent(),
        },
        steps: [
          {
            index: 0,
            editable: true,
            advanced_fields_present: false,
            fields: {
              id: field("summarize"),
              name: absent(),
              uses: field("agent/main"),
              if: absent(),
              continue_on_error: absent(),
              with: absent(),
              context: absent(),
            },
          },
        ],
      },
    ],
    validation: {
      valid: true,
      validated_at: "2026-07-30T00:00:00Z",
    },
  }
}

function field<Value>(value: Value): WorkflowEditorField<Value> {
  return { present: true, value }
}

function absent<Value>(): WorkflowEditorField<Value> {
  return { present: false, value: null }
}
