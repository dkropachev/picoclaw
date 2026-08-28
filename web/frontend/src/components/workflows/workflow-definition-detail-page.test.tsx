import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import type { ReactNode } from "react"
import { beforeEach, describe, expect, it, vi } from "vitest"

import { WorkflowDefinitionDetailPage } from "./workflow-definition-detail-page"

const mocks = vi.hoisted(() => ({
  getWorkflowDefinition: vi.fn(),
  checkWorkflowDependencies: vi.fn(),
}))
vi.mock("@/api/workflows", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/workflows")>()),
  ...mocks,
}))
vi.mock("./workflow-authoring-page", () => ({
  WorkflowRunPanel: () => <div>Typed run controls</div>,
  optionalString: (value: string) => value || undefined,
  parseDeliveryJSONObject: () => undefined,
  workflowRunInitialInputValues: () => ({}),
  workflowRunInitialSecretValues: () => ({}),
  workflowRunInputsPayload: () => undefined,
  workflowRunPayloadValidationMessage: () => null,
  workflowRunSecretsPayload: () => undefined,
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

const workflowID = "a".repeat(43)

function renderPage(onEdit = vi.fn(), onRuns = vi.fn()) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <WorkflowDefinitionDetailPage
        workflowID={workflowID}
        onBack={vi.fn()}
        onEdit={onEdit}
        onRuns={onRuns}
        onRunCreated={vi.fn()}
      />
    </QueryClientProvider>,
  )
  return { onEdit, onRuns }
}

describe("routed workflow definition detail", () => {
  beforeEach(() => {
    for (const mock of Object.values(mocks)) mock.mockReset()
    mocks.getWorkflowDefinition.mockResolvedValue({
      id: workflowID,
      ref: "workflows/review.yml",
      name: "Review",
      status: "valid",
      trigger: "manual",
      inputs: 0,
      secrets: 0,
    })
    mocks.checkWorkflowDependencies.mockResolvedValue({
      root_ref: "workflows/review.yml",
      revision: "dependency-1",
      ready: true,
      workflow_enabled: true,
      structural_ready: true,
      runtime_ready: true,
      issues: [],
    })
  })

  it("loads the exact issued ID and exposes routed actions", async () => {
    const user = userEvent.setup()
    const actions = renderPage()
    expect(await screen.findByText("Typed run controls")).toBeVisible()
    expect(mocks.getWorkflowDefinition).toHaveBeenCalledWith(
      workflowID,
      expect.any(AbortSignal),
    )
    await user.click(screen.getByRole("button", { name: "Edit" }))
    await user.click(screen.getByRole("button", { name: "Runs" }))
    expect(actions.onEdit).toHaveBeenCalled()
    expect(actions.onRuns).toHaveBeenCalled()
  })

  it("renders direct not-found without prior list state", async () => {
    mocks.getWorkflowDefinition.mockRejectedValue(
      Object.assign(new Error("missing"), { status: 404 }),
    )
    renderPage()
    expect(await screen.findByText("Item not found")).toBeVisible()
  })
})
