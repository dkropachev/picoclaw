import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { fireEvent, render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import type { ReactNode } from "react"
import { beforeEach, describe, expect, it, vi } from "vitest"

import type { WorkflowDefinition, WorkflowRun } from "@/api/workflows"
import type { WorkflowsRouteSearch } from "@/components/workflows/workflow-route-search"
import { WorkflowsPage } from "@/components/workflows/workflows-page"

const workflowMocks = vi.hoisted(() => ({
  checkWorkflowDependencies: vi.fn(),
  getWorkflowDevelopment: vi.fn(),
  getWorkflowRun: vi.fn(),
  getWorkflowRunEvents: vi.fn(),
  getWorkflowRunGraph: vi.fn(),
  listWorkflowRuns: vi.fn(),
  listWorkflowTemplates: vi.fn(),
  listWorkflows: vi.fn(),
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
  Link: ({
    to,
    search,
    children,
    className,
  }: {
    to: string
    search?: Record<string, string | undefined>
    children: ReactNode
    className?: string
  }) => {
    const params = new URLSearchParams()
    for (const [key, value] of Object.entries(search ?? {})) {
      if (value) {
        params.set(key, value)
      }
    }
    const query = params.toString()
    return (
      <a href={`${to}${query ? `?${query}` : ""}`} className={className}>
        {children}
      </a>
    )
  },
}))

const workflowRef = "workflows/github-issue-triage.yml"
const secondWorkflowRef = "workflows/second.yml"
const requestedRunID = "wr_requested-run_01"
const otherRunID = "wr_other-run_02"
const eventID = "ev_0123456789abcdef0123456789abcdef"

const workflows: WorkflowDefinition[] = [
  { ref: workflowRef, name: "Issue triage" },
  { ref: secondWorkflowRef, name: "Second" },
]

function workflowRun(
  id: string,
  overrides: Partial<WorkflowRun> = {},
): WorkflowRun {
  return {
    id,
    workflow_ref: workflowRef,
    status: "succeeded",
    created_at: "2026-07-29T12:00:00Z",
    updated_at: "2026-07-29T12:00:01Z",
    completed_at: "2026-07-29T12:00:01Z",
    ...overrides,
  }
}

describe("WorkflowsPage navigation", () => {
  beforeEach(() => {
    for (const mock of Object.values(workflowMocks)) {
      mock.mockReset()
    }
    workflowMocks.listWorkflows.mockResolvedValue({ workflows: [] })
    workflowMocks.getWorkflowDevelopment.mockResolvedValue({ session: null })
    workflowMocks.listWorkflowRuns.mockResolvedValue({ runs: [] })
    workflowMocks.listWorkflowTemplates.mockResolvedValue({ templates: [] })
    workflowMocks.getWorkflowRunEvents.mockResolvedValue({
      run_id: requestedRunID,
      events: [],
    })
    workflowMocks.getWorkflowRunGraph.mockResolvedValue({
      run_id: requestedRunID,
      nodes: [],
      edges: [],
    })
    workflowMocks.checkWorkflowDependencies.mockResolvedValue({
      root_ref: workflowRef,
      revision: "dependency-revision",
      ready: true,
      workflow_enabled: true,
      structural_ready: true,
      runtime_ready: true,
      dependencies: [],
      structural_issues: [],
    })
  })

  it("replaces route state when automatically selecting the first workflow and run", async () => {
    workflowMocks.listWorkflows.mockResolvedValue({
      workflows: [workflows[0]],
    })
    workflowMocks.listWorkflowRuns.mockResolvedValue({
      runs: [workflowRun(otherRunID)],
    })
    const onSearchChange = vi.fn()

    renderWorkflowsPage({}, onSearchChange)

    await waitFor(() => {
      expect(onSearchChange).toHaveBeenCalledWith(
        {
          workflow: workflowRef,
          run: otherRunID,
        },
        true,
      )
    })
  })

  it("retains an explicit run omitted from the list and shows not-found retry", async () => {
    workflowMocks.listWorkflows.mockResolvedValue({
      workflows: [workflows[0]],
    })
    workflowMocks.listWorkflowRuns.mockResolvedValue({
      runs: [workflowRun(otherRunID)],
    })
    workflowMocks.getWorkflowRun.mockRejectedValue(
      new Error("workflow run not found"),
    )
    const onSearchChange = vi.fn()
    const user = userEvent.setup()

    renderWorkflowsPage(
      {
        mode: "operate",
        workflow: workflowRef,
        run: requestedRunID,
      },
      onSearchChange,
    )

    expect(
      await screen.findByText("Workflow run not found"),
    ).toBeInTheDocument()
    expect(screen.getAllByText(requestedRunID).length).toBeGreaterThan(0)
    expect(screen.getByText(otherRunID)).toBeInTheDocument()
    expect(onSearchChange).not.toHaveBeenCalled()

    await user.click(screen.getByRole("button", { name: "Retry run detail" }))
    await waitFor(() => {
      expect(workflowMocks.getWorkflowRun).toHaveBeenCalledTimes(2)
    })
    expect(onSearchChange).not.toHaveBeenCalled()
  })

  it("shows loading and unavailable states without substituting a listed run", async () => {
    workflowMocks.listWorkflowRuns.mockResolvedValue({
      runs: [workflowRun(otherRunID)],
    })
    workflowMocks.getWorkflowRun.mockImplementation(
      () => new Promise<WorkflowRun>(() => undefined),
    )

    const loadingView = renderWorkflowsPage({
      mode: "operate",
      run: requestedRunID,
    })

    expect(await screen.findByText("Loading workflow run")).toBeInTheDocument()
    expect(screen.getAllByText(requestedRunID).length).toBeGreaterThan(0)

    loadingView.unmount()
    workflowMocks.getWorkflowRun.mockRejectedValue(
      new Error("workflow service unavailable"),
    )
    renderWorkflowsPage({ mode: "operate", run: requestedRunID })

    expect(
      await screen.findByText("Workflow run unavailable"),
    ).toBeInTheDocument()
    expect(screen.getAllByText(requestedRunID).length).toBeGreaterThan(0)
  })

  it("pushes mode, workflow, and run choices but replaces live query edits", async () => {
    const currentRun = workflowRun(requestedRunID)
    workflowMocks.listWorkflows.mockResolvedValue({ workflows })
    workflowMocks.listWorkflowRuns.mockResolvedValue({
      runs: [currentRun, workflowRun(otherRunID)],
    })
    workflowMocks.getWorkflowRun.mockResolvedValue(currentRun)
    const onSearchChange = vi.fn()
    const user = userEvent.setup()

    renderWorkflowsPage(
      {
        mode: "operate",
        workflow: workflowRef,
        run: requestedRunID,
      },
      onSearchChange,
    )

    await user.click(await screen.findByRole("button", { name: "Develop" }))
    await user.click(
      await screen.findByRole("button", {
        name: new RegExp(secondWorkflowRef.replace(".", "\\.")),
      }),
    )
    await user.click(
      await screen.findByRole("button", {
        name: new RegExp(otherRunID),
      }),
    )
    fireEvent.change(screen.getByPlaceholderText("Filter runs"), {
      target: { value: " failed " },
    })

    expect(onSearchChange).toHaveBeenNthCalledWith(
      1,
      {
        workflow: workflowRef,
        run: requestedRunID,
      },
      false,
    )
    expect(onSearchChange).toHaveBeenNthCalledWith(
      2,
      {
        mode: "operate",
        workflow: secondWorkflowRef,
        run: requestedRunID,
      },
      false,
    )
    expect(onSearchChange).toHaveBeenNthCalledWith(
      3,
      {
        mode: "operate",
        workflow: workflowRef,
        run: otherRunID,
      },
      false,
    )
    expect(onSearchChange).toHaveBeenNthCalledWith(
      4,
      {
        mode: "operate",
        workflow: workflowRef,
        run: requestedRunID,
        q: "failed",
      },
      true,
    )
  })

  it("links only a validated server event context and never input dispatch hints", async () => {
    const trustedRun = workflowRun(requestedRunID, {
      event: {
        id: eventID,
        source: "github",
        connector: "primary",
        type: "issues.opened",
      },
      inputs: {
        event_id: eventID,
        dispatch_id: "dsp_0123456789abcdef0123456789abcdef",
      },
    })
    workflowMocks.listWorkflows.mockResolvedValue({
      workflows: [workflows[0]],
    })
    workflowMocks.listWorkflowRuns.mockResolvedValue({ runs: [trustedRun] })
    workflowMocks.getWorkflowRun.mockResolvedValue(trustedRun)

    const trustedView = renderWorkflowsPage({
      mode: "operate",
      workflow: workflowRef,
      run: requestedRunID,
    })

    expect(await screen.findByRole("link", { name: eventID })).toHaveAttribute(
      "href",
      `/events?event=${eventID}`,
    )
    expect(screen.getByRole("link", { name: workflowRef })).toHaveAttribute(
      "href",
      expect.stringContaining(
        "mode=operate&workflow=workflows%2Fgithub-issue-triage.yml&run=wr_requested-run_01",
      ),
    )
    expect(
      screen.queryByRole("link", {
        name: "dsp_0123456789abcdef0123456789abcdef",
      }),
    ).not.toBeInTheDocument()

    trustedView.unmount()
    const untrustedRun = workflowRun(requestedRunID, {
      event: {
        id: eventID,
        source: "github",
        type: "issues.opened",
      },
      inputs: {
        event_id: eventID,
        dispatch_id: "dsp_0123456789abcdef0123456789abcdef",
      },
    })
    workflowMocks.getWorkflowRun.mockResolvedValue(untrustedRun)
    renderWorkflowsPage({
      mode: "operate",
      workflow: workflowRef,
      run: requestedRunID,
    })

    await screen.findByText("Summary")
    expect(
      screen.queryByRole("link", { name: eventID }),
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole("link", {
        name: "dsp_0123456789abcdef0123456789abcdef",
      }),
    ).not.toBeInTheDocument()
  })
})

function renderWorkflowsPage(
  search: WorkflowsRouteSearch,
  onSearchChange = vi.fn(),
) {
  const client = new QueryClient({
    defaultOptions: {
      queries: { retry: false },
      mutations: { retry: false },
    },
  })
  return render(
    <QueryClientProvider client={client}>
      <WorkflowsPage search={search} onSearchChange={onSearchChange} />
    </QueryClientProvider>,
  )
}
