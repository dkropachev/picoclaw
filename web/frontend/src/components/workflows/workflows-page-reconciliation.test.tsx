import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react"
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
  inspectWorkflowJobs: vi.fn(),
  inspectWorkflowTriggers: vi.fn(),
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
    workflowMocks.inspectWorkflowTriggers.mockResolvedValue({
      revision: "trigger-revision",
      triggers: emptyTriggerProjections(),
      validation: {
        valid: true,
        validated_at: "2026-07-29T12:00:00Z",
      },
    })
    workflowMocks.inspectWorkflowJobs.mockResolvedValue({
      revision: "jobs-revision",
      editable: true,
      complete: true,
      limits: [],
      jobs: [],
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

    expect(workflowMocks.testWorkflowDevelopment).not.toHaveBeenCalled()
    const reviewDialog = await screen.findByRole("dialog", {
      name: "Review draft test",
    })
    await user.click(
      within(reviewDialog).getByRole("switch", {
        name: "I reviewed this scenario and its possible effects",
      }),
    )
    await user.click(
      within(reviewDialog).getByRole("button", {
        name: "Confirm and run test",
      }),
    )

    await waitFor(() =>
      expect(workflowMocks.testWorkflowDevelopment).toHaveBeenCalled(),
    )
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

  it("blocks draft actions and mode changes while builder edits are pending", async () => {
    const session = developmentSession()
    workflowMocks.getWorkflowDevelopment.mockResolvedValue({ session })
    const onSearchChange = vi.fn()
    const user = userEvent.setup()

    renderWorkflowsPage(onSearchChange)

    await user.click(await screen.findByRole("tab", { name: "Builder" }))
    await user.click(
      await screen.findByRole("switch", {
        name: "Enable manual trigger",
      }),
    )

    expect(
      await screen.findByText("Structured builder changes are pending."),
    ).toBeInTheDocument()
    for (const name of [
      "Save Draft",
      "Ask AI",
      "Scaffold",
      "Validate",
      "Test Draft",
      "Publish",
      "Discard",
    ]) {
      expect(screen.getByRole("button", { name })).toBeDisabled()
    }
    expect(screen.getByRole("button", { name: "Refresh" })).toBeDisabled()

    await user.click(screen.getByRole("tab", { name: "YAML" }))
    expect(screen.getByRole("tab", { name: "Builder" })).toHaveAttribute(
      "aria-selected",
      "true",
    )
    expect(toastMocks.warning).toHaveBeenCalledWith(
      "Apply or reset the trigger builder changes before leaving or running another draft action.",
    )

    onSearchChange.mockClear()
    await user.click(screen.getByRole("button", { name: "Operate" }))
    expect(onSearchChange).not.toHaveBeenCalled()
    expect(toastMocks.warning).toHaveBeenLastCalledWith(
      "Apply or reset the trigger builder changes before leaving or running another draft action.",
    )
  })

  it("retains builder edits when browser navigation requests Operate mode", async () => {
    const session = developmentSession()
    workflowMocks.getWorkflowDevelopment.mockResolvedValue({ session })
    const onSearchChange = vi.fn()
    const user = userEvent.setup()
    const view = renderWorkflowsPage(onSearchChange)

    await user.click(await screen.findByRole("tab", { name: "Builder" }))
    const manualSwitch = await screen.findByRole("switch", {
      name: "Enable manual trigger",
    })
    await user.click(manualSwitch)
    await screen.findByText("Structured builder changes are pending.")
    onSearchChange.mockClear()

    view.rerenderPage({ mode: "operate" })

    await waitFor(() => expect(onSearchChange).toHaveBeenCalledWith({}, true))
    expect(screen.getByRole("tab", { name: "Builder" })).toHaveAttribute(
      "aria-selected",
      "true",
    )
    expect(
      screen.getByRole("switch", { name: "Enable manual trigger" }),
    ).toBeChecked()
    expect(
      screen.getByText("Structured builder changes are pending."),
    ).toBeInTheDocument()
    expect(toastMocks.warning).toHaveBeenCalledWith(
      "Apply or reset the trigger builder changes before leaving or running another draft action.",
    )
  })

  it("retains dirty builder edits until an external session update is explicitly loaded", async () => {
    const session = developmentSession()
    workflowMocks.getWorkflowDevelopment.mockResolvedValue({ session })
    const user = userEvent.setup()
    const view = renderWorkflowsPage(vi.fn())

    await user.click(await screen.findByRole("tab", { name: "Builder" }))
    await user.click(
      await screen.findByRole("switch", {
        name: "Enable manual trigger",
      }),
    )
    await screen.findByText("Structured builder changes are pending.")

    const updatedSession: WorkflowDevelopmentSession = {
      ...session,
      session_revision: "session-revision-external",
      draft_revision: "draft-revision-external",
      prompt: "Changed by another operator",
      yaml: "name: Externally changed\non: {}\njobs: {}\n",
      updated_at: "2026-07-29T12:05:00Z",
    }
    workflowMocks.getWorkflowDevelopment.mockResolvedValue({
      session: updatedSession,
    })
    await act(async () => {
      await view.client.refetchQueries({
        queryKey: ["workflows", "development"],
      })
    })

    expect(
      await screen.findByText("Workflow development changed elsewhere."),
    ).toBeInTheDocument()
    expect(
      screen.getByText(/authoritative workflow draft changed elsewhere/i),
    ).toBeInTheDocument()
    expect(
      screen.getByRole("switch", { name: "Enable manual trigger" }),
    ).toBeChecked()
    expect(screen.getByRole("button", { name: "Save Draft" })).toBeDisabled()
    expect(screen.getByRole("button", { name: "Apply to YAML" })).toBeDisabled()

    await user.click(
      screen.getByRole("button", {
        name: "Discard local edits and load latest state",
      }),
    )

    await waitFor(() =>
      expect(workflowMocks.inspectWorkflowTriggers).toHaveBeenLastCalledWith(
        updatedSession.yaml,
        expect.any(AbortSignal),
      ),
    )
    expect(
      screen.queryByText("Workflow development changed elsewhere."),
    ).not.toBeInTheDocument()
    expect(
      screen.getByRole("switch", { name: "Enable manual trigger" }),
    ).not.toBeChecked()
  })

  it("turns an external update into a conflict while local draft YAML is unsaved", async () => {
    const session = developmentSession()
    workflowMocks.getWorkflowDevelopment.mockResolvedValue({ session })
    const user = userEvent.setup()
    const view = renderWorkflowsPage(vi.fn())

    const yamlEditor = await screen.findByRole("textbox", {
      name: "Workflow YAML",
    })
    fireEvent.change(yamlEditor, {
      target: {
        value: "name: Locally applied builder draft\non: {}\njobs: {}\n",
      },
    })

    const updatedSession: WorkflowDevelopmentSession = {
      ...session,
      session_revision: "session-revision-after-local-apply",
      draft_revision: "draft-revision-after-local-apply",
      yaml: "name: Externally changed\non: {}\njobs: {}\n",
      updated_at: "2026-07-29T12:06:00Z",
    }
    workflowMocks.getWorkflowDevelopment.mockResolvedValue({
      session: updatedSession,
    })
    await act(async () => {
      await view.client.refetchQueries({
        queryKey: ["workflows", "development"],
      })
    })

    expect(
      await screen.findByText("Workflow development changed elsewhere."),
    ).toBeInTheDocument()
    expect(yamlEditor).toHaveValue(
      "name: Locally applied builder draft\non: {}\njobs: {}\n",
    )
    expect(screen.getByRole("button", { name: "Save Draft" })).toBeDisabled()

    await user.click(
      screen.getByRole("button", {
        name: "Discard local edits and load latest state",
      }),
    )
    expect(yamlEditor).toHaveValue(updatedSession.yaml)
  })

  it("retains dirty builder edits when the active session is removed elsewhere", async () => {
    const session = developmentSession()
    workflowMocks.getWorkflowDevelopment.mockResolvedValue({ session })
    const user = userEvent.setup()
    const view = renderWorkflowsPage(vi.fn())

    await user.click(await screen.findByRole("tab", { name: "Builder" }))
    await user.click(
      await screen.findByRole("switch", {
        name: "Enable manual trigger",
      }),
    )
    await screen.findByText("Structured builder changes are pending.")

    workflowMocks.getWorkflowDevelopment.mockResolvedValue({ session: null })
    await act(async () => {
      await view.client.refetchQueries({
        queryKey: ["workflows", "development"],
      })
    })

    expect(
      await screen.findByText("Workflow development changed elsewhere."),
    ).toBeInTheDocument()
    expect(
      screen.getByText(/development session was removed elsewhere/i),
    ).toBeInTheDocument()
    expect(
      screen.getByRole("switch", { name: "Enable manual trigger" }),
    ).toBeChecked()
    expect(screen.queryByText("New workflow")).not.toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Save Draft" })).toBeDisabled()

    await user.click(
      screen.getByRole("button", {
        name: "Discard local edits and load latest state",
      }),
    )

    expect(await screen.findByText("New workflow")).toBeInTheDocument()
    expect(
      screen.queryByRole("switch", { name: "Enable manual trigger" }),
    ).not.toBeInTheDocument()
  })
})

function emptyTriggerProjections() {
  return {
    manual: { present: false, editable: true, value: null },
    schedule: { present: false, editable: true, value: null },
    channel_message: { present: false, editable: true, value: null },
    command: { present: false, editable: true, value: null },
    runtime_event: { present: false, editable: true, value: null },
    event: { present: false, editable: true, value: null },
    workflow_call: { present: false, editable: true, value: null },
  }
}

function renderWorkflowsPage(
  onSearchChange: (search: WorkflowsRouteSearch, replace?: boolean) => void,
  search: WorkflowsRouteSearch = {},
) {
  const client = new QueryClient({
    defaultOptions: {
      queries: { retry: false },
      mutations: { retry: false },
    },
  })
  const page = (nextSearch: WorkflowsRouteSearch) => (
    <QueryClientProvider client={client}>
      <WorkflowsPage search={nextSearch} onSearchChange={onSearchChange} />
    </QueryClientProvider>
  )
  const view = render(page(search))
  return {
    ...view,
    client,
    rerenderPage(nextSearch: WorkflowsRouteSearch) {
      view.rerender(page(nextSearch))
    },
  }
}
