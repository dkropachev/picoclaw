import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import type { ReactNode } from "react"
import { beforeEach, describe, expect, it, vi } from "vitest"

import type { WorkflowDevelopmentSession } from "@/api/workflows"

import { WorkflowAuthoringRoutePage } from "./workflow-authoring-route-page"

const mocks = vi.hoisted(() => ({
  getWorkflowDefinition: vi.fn(),
  getWorkflowDevelopment: vi.fn(),
  startWorkflowDevelopment: vi.fn(),
  discardWorkflowDevelopment: vi.fn(),
}))

vi.mock("@/api/workflows", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/workflows")>()),
  ...mocks,
}))
vi.mock("./workflow-capability-catalog", () => ({
  WorkflowCapabilityCatalog: () => <button type="button">Capabilities</button>,
}))
vi.mock("./workflow-authoring-page", () => ({
  WorkflowAuthoringPage: ({
    onDevelopmentConflict,
  }: {
    onDevelopmentConflict?: (session: WorkflowDevelopmentSession) => void
  }) => (
    <div data-testid="authoring-editor">
      Editor
      <button type="button" onClick={() => onDevelopmentConflict?.(session())}>
        Simulate stale mutation
      </button>
    </div>
  ),
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

const requestedID = "b".repeat(43)
const activeID = "a".repeat(43)

function session(
  overrides: Partial<WorkflowDevelopmentSession> = {},
): WorkflowDevelopmentSession {
  return {
    id: "dev_1",
    session_revision: "session-1",
    draft_revision: "draft-1",
    base_target_revision: "base-1",
    reason: "edit",
    status: "active",
    source_workflow_ref: "workflows/active.yml",
    source_workflow_id: activeID,
    target_workflow_ref: "workflows/active.yml",
    yaml: "name: Active\n",
    created_at: "2026-08-01T00:00:00Z",
    updated_at: "2026-08-01T00:00:00Z",
    ...overrides,
  }
}

function renderPage() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <WorkflowAuthoringRoutePage
        intent={{ kind: "edit", workflowID: requestedID }}
        onBack={vi.fn()}
        onOpenActiveNew={vi.fn()}
        onOpenActiveEdit={vi.fn()}
        onOpenRun={vi.fn()}
        onPublished={vi.fn()}
      />
    </QueryClientProvider>,
  )
}

describe("workflow routed authoring", () => {
  beforeEach(() => {
    for (const mock of Object.values(mocks)) mock.mockReset()
    mocks.getWorkflowDefinition.mockResolvedValue({
      id: requestedID,
      ref: "workflows/requested.yml",
      status: "valid",
      trigger: "manual",
      inputs: 0,
      secrets: 0,
    })
    mocks.discardWorkflowDevelopment.mockResolvedValue({ session: session() })
  })

  it("gates a mismatched active singleton without starting or applying it", async () => {
    mocks.getWorkflowDevelopment.mockResolvedValue({ session: session() })
    renderPage()

    expect(
      await screen.findByText("Active workflow draft conflict"),
    ).toBeVisible()
    expect(screen.queryByTestId("authoring-editor")).not.toBeInTheDocument()
    expect(mocks.startWorkflowDevelopment).not.toHaveBeenCalled()
  })

  it("resumes a matching session returned by a start race", async () => {
    mocks.getWorkflowDevelopment.mockResolvedValue({ session: null })
    mocks.startWorkflowDevelopment.mockResolvedValue({
      session: session({
        source_workflow_id: requestedID,
        source_workflow_ref: "workflows/requested.yml",
        target_workflow_ref: "workflows/requested.yml",
      }),
      conflict: true,
    })
    renderPage()

    expect(await screen.findByTestId("authoring-editor")).toBeVisible()
    expect(
      screen.queryByText("Active workflow draft conflict"),
    ).not.toBeInTheDocument()
  })

  it("keeps a mismatching start race behind the conflict", async () => {
    mocks.getWorkflowDevelopment.mockResolvedValue({ session: null })
    mocks.startWorkflowDevelopment.mockResolvedValue({
      session: session(),
      conflict: true,
    })
    renderPage()

    expect(
      await screen.findByText("Active workflow draft conflict"),
    ).toBeVisible()
    expect(screen.queryByTestId("authoring-editor")).not.toBeInTheDocument()
  })

  it("resumes an issued-ID matching edit draft even if definition lookup is now missing", async () => {
    mocks.getWorkflowDefinition.mockRejectedValue(
      Object.assign(new Error("missing"), { status: 404 }),
    )
    mocks.getWorkflowDevelopment.mockResolvedValue({
      session: session({
        source_workflow_id: requestedID,
        source_workflow_ref: "workflows/requested.yml",
        target_workflow_ref: "workflows/requested.yml",
      }),
    })
    renderPage()

    expect(await screen.findByTestId("authoring-editor")).toBeVisible()
    expect(screen.queryByText("Item not found")).not.toBeInTheDocument()
  })

  it("moves a replacement session from a stale mutation behind the route conflict", async () => {
    const user = userEvent.setup()
    mocks.getWorkflowDevelopment.mockResolvedValue({
      session: session({
        source_workflow_id: requestedID,
        source_workflow_ref: "workflows/requested.yml",
        target_workflow_ref: "workflows/requested.yml",
      }),
    })
    renderPage()
    await user.click(
      await screen.findByRole("button", { name: "Simulate stale mutation" }),
    )
    expect(
      await screen.findByText("Active workflow draft conflict"),
    ).toBeVisible()
    expect(screen.queryByTestId("authoring-editor")).not.toBeInTheDocument()
  })

  it("discards only the exact displayed session fence", async () => {
    const user = userEvent.setup()
    mocks.getWorkflowDevelopment.mockResolvedValue({ session: session() })
    renderPage()
    await user.click(
      await screen.findByRole("button", { name: "Discard draft" }),
    )
    await user.click(
      screen.getAllByRole("button", { name: "Discard draft" }).at(-1)!,
    )
    await waitFor(() =>
      expect(mocks.discardWorkflowDevelopment).toHaveBeenCalledWith({
        session_id: "dev_1",
        expected_session_revision: "session-1",
      }),
    )
  })
})
