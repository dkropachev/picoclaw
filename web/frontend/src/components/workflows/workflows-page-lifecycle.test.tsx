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
import { beforeEach, describe, expect, it, vi } from "vitest"

import {
  WorkflowAPIError,
  type WorkflowDefinitionInspection,
  type WorkflowDependencyCheckResponse,
  type WorkflowRun,
} from "@/api/workflows"
import { WorkflowsPage } from "@/components/workflows/workflows-page"

const workflowMocks = vi.hoisted(() => ({
  cancelWorkflowRun: vi.fn(),
  checkWorkflowDependencies: vi.fn(),
  getWorkflowDevelopment: vi.fn(),
  getWorkflowRun: vi.fn(),
  getWorkflowRunEvents: vi.fn(),
  getWorkflowRunGraph: vi.fn(),
  inspectPublishedWorkflowDefinition: vi.fn(),
  listWorkflowRuns: vi.fn(),
  listWorkflowTemplates: vi.fn(),
  listWorkflows: vi.fn(),
  retryWorkflowRun: vi.fn(),
  reloadWorkflows: vi.fn(),
  runWorkflow: vi.fn(),
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

const runWorkflowRef = "workflows/manual.yml"
const retryWorkflowRef = "workflows/retry.yml"
const selectedRunID = "wr_selected"

function dependencyReport(
  ref: string,
  revision: string,
): WorkflowDependencyCheckResponse {
  return {
    root_ref: ref,
    revision,
    ready: true,
    workflow_enabled: true,
    structural_ready: true,
    runtime_ready: true,
    dependencies: [],
    structural_issues: [],
  }
}

function workflowRun(overrides: Partial<WorkflowRun> = {}): WorkflowRun {
  return {
    id: selectedRunID,
    workflow_ref: retryWorkflowRef,
    status: "succeeded",
    created_at: "2026-07-29T12:00:00Z",
    updated_at: "2026-07-29T12:00:01Z",
    completed_at: "2026-07-29T12:00:01Z",
    ...overrides,
  }
}

describe("WorkflowsPage lifecycle controls", () => {
  beforeEach(() => {
    for (const mock of Object.values(workflowMocks)) {
      mock.mockReset()
    }
    workflowMocks.listWorkflows.mockResolvedValue({
      workflows: [
        {
          ref: runWorkflowRef,
          name: "Manual",
          workflow_call: {
            inputs: {
              ticket: { type: "string", required: true },
            },
          },
        },
        { ref: retryWorkflowRef, name: "Retry" },
      ],
    })
    workflowMocks.getWorkflowDevelopment.mockResolvedValue({ session: null })
    workflowMocks.listWorkflowTemplates.mockResolvedValue({ templates: [] })
    workflowMocks.listWorkflowRuns.mockResolvedValue({ runs: [workflowRun()] })
    workflowMocks.getWorkflowRun.mockResolvedValue(workflowRun())
    workflowMocks.getWorkflowRunEvents.mockResolvedValue({
      run_id: selectedRunID,
      events: [],
    })
    workflowMocks.getWorkflowRunGraph.mockResolvedValue({
      run_id: selectedRunID,
      nodes: [],
      edges: [],
    })
    workflowMocks.inspectPublishedWorkflowDefinition.mockImplementation(
      (ref: string) => Promise.resolve(definitionInspection(ref)),
    )
    workflowMocks.reloadWorkflows.mockResolvedValue({
      reloaded_at: "2026-07-29T12:00:00Z",
      workflows: [],
      errors: [],
    })
    workflowMocks.checkWorkflowDependencies.mockImplementation(
      ({ ref }: { ref: string }) =>
        Promise.resolve(
          dependencyReport(
            ref,
            ref === runWorkflowRef
              ? "opaque-run-revision"
              : "opaque-retry-revision",
          ),
        ),
    )
    workflowMocks.runWorkflow.mockResolvedValue({
      result: { run_id: "wr_started", status: "running" },
    })
    workflowMocks.retryWorkflowRun.mockResolvedValue({
      result: { run_id: "wr_retried", status: "running" },
    })
  })

  it("uses separate exact-ref fresh stamps for manual run and retry", async () => {
    const user = userEvent.setup()
    renderWorkflowsPage()

    await openRunPopover(user)
    const input = screen.getByLabelText("ticket")
    await user.type(input, "Printer offline")
    const runButton = screen.getAllByRole("button", {
      name: "Run workflow",
    })[1]
    await waitFor(() => expect(runButton).toBeEnabled())
    await user.click(runButton)

    expect(workflowMocks.runWorkflow).toHaveBeenCalledWith(
      expect.objectContaining({
        ref: runWorkflowRef,
        expected_dependency_revision: "opaque-run-revision",
        inputs: { ticket: "Printer offline" },
      }),
    )

    const retrySecrets = screen.getByRole("textbox", {
      name: "Retry secrets JSON",
    })
    fireEvent.change(retrySecrets, {
      target: { value: '{"token":"retry-token"}' },
    })
    const retryButton = screen.getByRole("button", { name: "Retry" })
    await waitFor(() => expect(retryButton).toBeEnabled())
    await user.click(retryButton)

    expect(workflowMocks.retryWorkflowRun).toHaveBeenCalledWith(selectedRunID, {
      expected_dependency_revision: "opaque-retry-revision",
      secrets: { token: "retry-token" },
    })
    expect(workflowMocks.checkWorkflowDependencies).toHaveBeenCalledWith(
      { ref: runWorkflowRef },
      expect.any(AbortSignal),
    )
    expect(workflowMocks.checkWorkflowDependencies).toHaveBeenCalledWith(
      { ref: retryWorkflowRef },
      expect.any(AbortSignal),
    )
  })

  it("inspects the selected definition and refetches it after reload", async () => {
    const user = userEvent.setup()
    renderWorkflowsPage()

    expect(
      await screen.findByTitle("inspection:workflows/manual.yml"),
    ).toBeInTheDocument()
    expect(
      workflowMocks.inspectPublishedWorkflowDefinition,
    ).toHaveBeenCalledWith(runWorkflowRef, expect.any(AbortSignal))

    await user.click(screen.getByRole("button", { name: "Reload" }))
    await waitFor(() => {
      expect(workflowMocks.reloadWorkflows).toHaveBeenCalledTimes(1)
      expect(
        workflowMocks.inspectPublishedWorkflowDefinition.mock.calls.length,
      ).toBeGreaterThan(1)
    })
  })

  it("keeps run and retry forms while exact dependency refetches fail closed", async () => {
    const user = userEvent.setup()
    const runRefetch = deferred<WorkflowDependencyCheckResponse>()
    const retryRefetch = deferred<WorkflowDependencyCheckResponse>()
    const calls = new Map<string, number>()
    workflowMocks.checkWorkflowDependencies.mockImplementation(
      ({ ref }: { ref: string }) => {
        const call = (calls.get(ref) ?? 0) + 1
        calls.set(ref, call)
        if (call === 1) {
          return Promise.resolve(
            dependencyReport(
              ref,
              ref === runWorkflowRef
                ? "opaque-run-revision"
                : "opaque-retry-revision",
            ),
          )
        }
        return ref === runWorkflowRef
          ? runRefetch.promise
          : retryRefetch.promise
      },
    )
    workflowMocks.runWorkflow.mockRejectedValue(
      new WorkflowAPIError("Workflow dependencies changed.", 409),
    )
    workflowMocks.retryWorkflowRun.mockRejectedValue(
      new WorkflowAPIError(
        "Workflow dependency readiness is temporarily unavailable.",
        503,
      ),
    )
    renderWorkflowsPage()

    await openRunPopover(user)
    const input = screen.getByLabelText("ticket")
    await user.type(input, "Keep this input")
    const runButton = screen.getAllByRole("button", {
      name: "Run workflow",
    })[1]
    await waitFor(() => expect(runButton).toBeEnabled())
    await user.click(runButton)

    await waitFor(() => expect(calls.get(runWorkflowRef)).toBe(2))
    expect(input).toHaveValue("Keep this input")
    expect(runButton).toBeDisabled()
    expect(
      screen.getByText("Checking dependencies before running…"),
    ).toBeVisible()
    expect(workflowMocks.runWorkflow).toHaveBeenCalledTimes(1)

    const retrySecrets = screen.getByRole("textbox", {
      name: "Retry secrets JSON",
    })
    fireEvent.change(retrySecrets, {
      target: { value: '{"token":"keep-me"}' },
    })
    const retryButton = screen.getByRole("button", { name: "Retry" })
    await waitFor(() => expect(retryButton).toBeEnabled())
    await user.click(retryButton)

    await waitFor(() => expect(calls.get(retryWorkflowRef)).toBe(2))
    expect(retrySecrets).toHaveValue('{"token":"keep-me"}')
    expect(retryButton).toBeDisabled()
    expect(workflowMocks.retryWorkflowRun).toHaveBeenCalledTimes(1)
  })

  it("cancels the dialog's exact run and renders returned audit metadata", async () => {
    const user = userEvent.setup()
    const running = workflowRun({
      status: "running",
      completed_at: undefined,
    })
    const canceled = workflowRun({
      status: "canceled",
      cancel_reason: "operator intervention",
      cancel_requested_at: "2026-07-29T12:03:00Z",
      completed_at: "2026-07-29T12:03:00Z",
      updated_at: "2026-07-29T12:03:00Z",
    })
    workflowMocks.listWorkflowRuns.mockResolvedValue({ runs: [running] })
    workflowMocks.getWorkflowRun.mockResolvedValue(running)
    workflowMocks.cancelWorkflowRun.mockResolvedValue(canceled)
    renderWorkflowsPage()

    await user.click(await screen.findByRole("button", { name: "Cancel" }))
    workflowMocks.getWorkflowRun.mockResolvedValue(canceled)
    const dialog = screen.getByRole("alertdialog")
    expect(within(dialog).getByText(selectedRunID)).toBeVisible()
    await user.type(
      within(dialog).getByRole("textbox", { name: "Cancel reason" }),
      "  operator intervention  ",
    )
    await user.click(within(dialog).getByRole("button", { name: "Cancel run" }))

    expect(workflowMocks.cancelWorkflowRun).toHaveBeenCalledWith(
      selectedRunID,
      "operator intervention",
    )
    await waitFor(() =>
      expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument(),
    )
    expect(screen.getByText("Cancel requested")).toBeVisible()
    expect(screen.getByText("Completed")).toBeVisible()
    expect(screen.getByText("operator intervention")).toBeVisible()
  })
})

async function openRunPopover(user: ReturnType<typeof userEvent.setup>) {
  const triggers = await screen.findAllByRole("button", {
    name: "Run workflow",
  })
  await user.click(triggers[0])
  await screen.findByText('Input "ticket" is required.')
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
      <WorkflowsPage
        search={{
          mode: "operate",
          workflow: runWorkflowRef,
          run: selectedRunID,
        }}
        onSearchChange={vi.fn()}
      />
    </QueryClientProvider>,
  )
}

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise
    reject = rejectPromise
  })
  return { promise, resolve, reject }
}

function definitionInspection(ref: string): WorkflowDefinitionInspection {
  return {
    source: { kind: "published", ref },
    revision: `inspection:${ref}`,
    complete: true,
    validation: {
      valid: true,
      issue_count: 0,
      issues: [],
      truncated: false,
    },
    triggers: {
      manual: { present: true, projected: true, value: {} },
      schedule: { present: false, projected: true },
      channel_message: { present: false, projected: true },
      command: { present: false, projected: true },
      runtime_event: { present: false, projected: true },
      event: { present: false, projected: true },
      workflow_call: { present: false, projected: true },
    },
    jobs: [],
    dependencies: [],
    effects: [],
    limits: [],
  }
}
