import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import type { ReactNode } from "react"
import { beforeEach, describe, expect, it, vi } from "vitest"

import type {
  WorkflowDevelopmentSession,
  WorkflowDevelopmentTestReconciliation,
} from "@/api/workflows"
import type { WorkflowsRouteSearch } from "@/components/workflows/workflow-route-search"
import { WorkflowsPage } from "@/components/workflows/workflows-page"

const workflowMocks = vi.hoisted(() => ({
  checkWorkflowDependencies: vi.fn(),
  getWorkflowDevelopment: vi.fn(),
  inspectWorkflowEventTrigger: vi.fn(),
  listWorkflowRuns: vi.fn(),
  listWorkflowTemplates: vi.fn(),
  listWorkflows: vi.fn(),
  testWorkflowDevelopment: vi.fn(),
}))

const toastMocks = vi.hoisted(() => ({
  error: vi.fn(),
  info: vi.fn(),
  success: vi.fn(),
  warning: vi.fn(),
}))

vi.mock("@/api/workflows", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/workflows")>()),
  ...workflowMocks,
}))

vi.mock("sonner", () => ({
  toast: toastMocks,
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

const runID = "wr_durable_reconciliation"
const reconciliation: WorkflowDevelopmentTestReconciliation = {
  state: "degraded",
  reason: "draft_test_snapshot_not_recorded",
  run_id: runID,
  message:
    "The workflow run was created, but its development snapshot could not be recorded.",
}

function developmentSession(): WorkflowDevelopmentSession {
  return {
    id: "dev-reconciliation",
    session_revision: "session-revision",
    draft_revision: "draft-revision",
    base_target_revision: "base-revision",
    reason: "new",
    status: "editing",
    prompt: "Check reconciliation behavior",
    target_workflow_ref: "workflows/reconciliation.yml",
    yaml: "name: Reconciliation\non:\n  workflow_call:\njobs: {}\n",
    created_at: "2026-07-29T12:00:00Z",
    updated_at: "2026-07-29T12:00:00Z",
  }
}

describe("WorkflowsPage draft-test reconciliation", () => {
  beforeEach(() => {
    for (const mock of [
      ...Object.values(workflowMocks),
      ...Object.values(toastMocks),
    ]) {
      mock.mockReset()
    }
    workflowMocks.listWorkflows.mockResolvedValue({ workflows: [] })
    workflowMocks.listWorkflowRuns.mockResolvedValue({ runs: [] })
    workflowMocks.listWorkflowTemplates.mockResolvedValue({ templates: [] })
    workflowMocks.inspectWorkflowEventTrigger.mockResolvedValue({
      revision: "trigger-revision",
      editable: true,
      event_trigger: null,
      validation: {
        valid: true,
        validated_at: "2026-07-29T12:00:00Z",
      },
    })
    workflowMocks.checkWorkflowDependencies.mockResolvedValue({
      root_ref: "workflows/reconciliation.yml",
      revision: "dependency-revision",
      ready: true,
      workflow_enabled: true,
      structural_ready: true,
      runtime_ready: true,
      dependencies: [],
      structural_issues: [],
    })
  })

  it("renders polled reconciliation as an accessible Develop warning", async () => {
    const session = developmentSession()
    workflowMocks.getWorkflowDevelopment.mockResolvedValue({
      session,
      reconciliation,
    })
    const onSearchChange = vi.fn()
    const user = userEvent.setup()

    renderWorkflowsPage(onSearchChange)

    const warning = await screen.findByRole("alert")
    expect(warning).toHaveTextContent("Draft test reconciliation degraded")
    expect(warning).toHaveTextContent(reconciliation.message)
    expect(warning).toHaveTextContent(runID)

    await user.click(
      screen.getByRole("button", {
        name: `Open reconciled run ${runID}`,
      }),
    )
    expect(onSearchChange).toHaveBeenCalledWith(
      { mode: "operate", run: runID },
      false,
    )
  })

  it("warns for an accepted degraded launch and still selects its durable run", async () => {
    const session = developmentSession()
    workflowMocks.getWorkflowDevelopment.mockResolvedValue({ session })
    workflowMocks.testWorkflowDevelopment.mockResolvedValue({
      session,
      result: { run_id: runID, status: "running" },
      reconciliation,
    })
    const onSearchChange = vi.fn()
    const user = userEvent.setup()

    renderWorkflowsPage(onSearchChange)

    const testButton = await screen.findByRole("button", {
      name: "Test Draft",
    })
    await waitFor(() => expect(testButton).toBeEnabled())
    await user.click(testButton)

    expect(workflowMocks.testWorkflowDevelopment).toHaveBeenCalledWith(
      expect.objectContaining({
        target_ref: session.target_workflow_ref,
        yaml: session.yaml,
        async: true,
      }),
    )
    await waitFor(() => {
      expect(toastMocks.warning).toHaveBeenCalledWith(reconciliation.message)
    })
    expect(screen.getByRole("alert")).toHaveTextContent(reconciliation.message)
    expect(toastMocks.error).not.toHaveBeenCalled()
    expect(onSearchChange).toHaveBeenCalledWith({ run: runID }, false)
  })
})

function renderWorkflowsPage(
  onSearchChange: (search: WorkflowsRouteSearch, replace?: boolean) => void,
) {
  const client = new QueryClient({
    defaultOptions: {
      queries: { retry: false },
      mutations: { retry: false },
    },
  })
  return render(
    <QueryClientProvider client={client}>
      <WorkflowsPage search={{}} onSearchChange={onSearchChange} />
    </QueryClientProvider>,
  )
}
