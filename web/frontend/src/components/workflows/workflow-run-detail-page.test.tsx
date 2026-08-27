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

import { WorkflowRunDetailPage } from "./workflow-run-detail-page"

const mocks = vi.hoisted(() => ({
  getWorkflowRun: vi.fn(),
  getWorkflowRunEvents: vi.fn(),
  getWorkflowRunGraph: vi.fn(),
  checkWorkflowDependencies: vi.fn(),
  retryWorkflowRun: vi.fn(),
  cancelWorkflowRun: vi.fn(),
}))
vi.mock("@/api/workflows", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/workflows")>()),
  ...mocks,
}))
vi.mock("./workflow-authoring-page", () => ({
  RunSummary: () => <div>Run summary</div>,
  RunGraphPanel: () => <div>Run graph</div>,
  ExecutionPanel: () => <div>Execution</div>,
  ManagedExecutionPanel: () => <div>Managed execution</div>,
  EventsPanel: () => <div>Events</div>,
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

const failedRun = {
  id: "wr_failed",
  workflow_id: "a".repeat(43),
  workflow_ref: "workflows/review.yml",
  status: "failed",
  created_at: "2026-08-01T00:00:00Z",
  updated_at: "2026-08-01T00:01:00Z",
}

function renderPage(runID = failedRun.id, onRetryCreated = vi.fn()) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <WorkflowRunDetailPage
        runID={runID}
        onBack={vi.fn()}
        onRetryCreated={onRetryCreated}
      />
    </QueryClientProvider>,
  )
  return onRetryCreated
}

describe("routed workflow run detail", () => {
  beforeEach(() => {
    for (const mock of Object.values(mocks)) mock.mockReset()
    vi.stubGlobal("EventSource", undefined)
    mocks.getWorkflowRun.mockResolvedValue(failedRun)
    mocks.getWorkflowRunEvents.mockResolvedValue({
      run_id: failedRun.id,
      events: [],
    })
    mocks.getWorkflowRunGraph.mockResolvedValue({
      run_id: failedRun.id,
      nodes: [],
      edges: [],
    })
    mocks.checkWorkflowDependencies.mockResolvedValue({
      root_ref: failedRun.workflow_ref,
      revision: "dependency-1",
      ready: true,
      workflow_enabled: true,
      structural_ready: true,
      runtime_ready: true,
      issues: [],
    })
  })

  it("fails closed on malformed retry secrets and navigates after a valid retry", async () => {
    const user = userEvent.setup()
    const onRetryCreated = renderPage()
    const secrets = await screen.findByLabelText("Secrets JSON")
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Retry" })).toBeEnabled(),
    )

    fireEvent.change(secrets, { target: { value: '{"token":1}' } })
    await user.click(screen.getByRole("button", { name: "Retry" }))
    expect(mocks.retryWorkflowRun).not.toHaveBeenCalled()

    mocks.retryWorkflowRun.mockResolvedValue({
      result: { run_id: "wr_retried", status: "running" },
    })
    fireEvent.change(secrets, { target: { value: '{"token":"secret"}' } })
    await user.click(screen.getByRole("button", { name: "Retry" }))
    await waitFor(() =>
      expect(mocks.retryWorkflowRun).toHaveBeenCalledWith("wr_failed", {
        expected_dependency_revision: "dependency-1",
        secrets: { token: "secret" },
      }),
    )
    expect(onRetryCreated).toHaveBeenCalledWith("wr_retried")
  })

  it("requires an explicit cancel reason for an active run", async () => {
    const user = userEvent.setup()
    mocks.getWorkflowRun.mockResolvedValue({
      ...failedRun,
      id: "wr_running",
      status: "running",
    })
    mocks.cancelWorkflowRun.mockResolvedValue({
      ...failedRun,
      id: "wr_running",
      status: "canceled",
    })
    renderPage("wr_running")
    await user.click(await screen.findByRole("button", { name: "Cancel" }))
    const dialog = screen.getByRole("alertdialog")
    await user.type(
      within(dialog).getByRole("textbox", { name: "Cancel reason" }),
      "operator stop",
    )
    await user.click(within(dialog).getByRole("button", { name: "Cancel run" }))
    await waitFor(() =>
      expect(mocks.cancelWorkflowRun).toHaveBeenCalledWith(
        "wr_running",
        "operator stop",
      ),
    )
  })

  it("renders an exact not-found state without substituting another run", async () => {
    mocks.getWorkflowRun.mockRejectedValue(
      Object.assign(new Error("missing"), { status: 404 }),
    )
    renderPage("wr_missing")
    expect(await screen.findByText("Item not found")).toBeVisible()
    expect(mocks.getWorkflowRun).toHaveBeenCalledWith("wr_missing")
  })
})
