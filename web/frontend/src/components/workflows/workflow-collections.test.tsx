import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, waitFor, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import type { ReactNode } from "react"
import { beforeEach, describe, expect, it, vi } from "vitest"

import {
  WorkflowDefinitionsCollectionPage,
  WorkflowRunsCollectionPage,
} from "./workflow-collections"

const mocks = vi.hoisted(() => ({
  listWorkflowDefinitions: vi.fn(),
  listWorkflowRuns: vi.fn(),
  cancelWorkflowRun: vi.fn(),
}))
vi.mock("@/api/workflows", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/workflows")>()),
  ...mocks,
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

const definitionID = "a".repeat(43)
const schema = { fields: [] }

function renderWithClient(node: ReactNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  render(<QueryClientProvider client={client}>{node}</QueryClientProvider>)
}

describe("workflow collections", () => {
  beforeEach(() => {
    for (const mock of Object.values(mocks)) mock.mockReset()
    mocks.listWorkflowDefinitions.mockResolvedValue({
      workflows: [
        {
          id: definitionID,
          ref: "workflows/review.yml",
          name: "Review",
          status: "valid",
          trigger: "workflow_call",
          inputs: 2,
          secrets: 1,
        },
      ],
      total: 1,
      canonical_query: "ORDER BY ref ASC",
      query_schema: schema,
    })
    mocks.listWorkflowRuns.mockResolvedValue({
      runs: [
        {
          id: "wr_running",
          workflow_id: definitionID,
          workflow_ref: "workflows/review.yml",
          status: "running",
          created_at: "2026-08-01T00:00:00Z",
          updated_at: "2026-08-01T00:01:00Z",
        },
      ],
      total: 1,
      canonical_query: "ORDER BY created DESC",
      query_schema: schema,
    })
  })

  it("renders definitions through all administrative views without selection", async () => {
    const user = userEvent.setup()
    const onRun = vi.fn()
    renderWithClient(
      <WorkflowDefinitionsCollectionPage
        search={{ q: "ORDER BY ref ASC" }}
        onSearchChange={vi.fn()}
        onOpen={vi.fn()}
        onEdit={vi.fn()}
        onRun={onRun}
        onNew={vi.fn()}
        onRuns={vi.fn()}
        onSettings={vi.fn()}
      />,
    )

    const row = await screen.findByText("Review")
    expect(screen.getByRole("button", { name: "List view" })).toBeVisible()
    expect(screen.getByRole("button", { name: "Table view" })).toBeVisible()
    expect(screen.getByRole("button", { name: "Grid view" })).toBeVisible()
    expect(screen.queryByText(/selected$/)).not.toBeInTheDocument()
    await user.pointer({ keys: "[MouseRight]", target: row.closest("li")! })
    await user.click(
      await screen.findByRole("menuitem", { name: "Run workflow" }),
    )
    expect(onRun).toHaveBeenCalledWith(
      expect.objectContaining({ id: definitionID }),
    )
  })

  it("limits operational runs to List/Table and confirms cancellation", async () => {
    const user = userEvent.setup()
    mocks.cancelWorkflowRun.mockResolvedValue({
      id: "wr_running",
      workflow_ref: "workflows/review.yml",
      status: "canceled",
      created_at: "2026-08-01T00:00:00Z",
      updated_at: "2026-08-01T00:02:00Z",
    })
    renderWithClient(
      <WorkflowRunsCollectionPage
        search={{ q: "ORDER BY created DESC" }}
        onSearchChange={vi.fn()}
        onOpen={vi.fn()}
        onDefinitions={vi.fn()}
      />,
    )
    const row = (await screen.findByText("wr_running")).closest("li")!
    expect(screen.getByRole("button", { name: "Table view" })).toBeVisible()
    expect(screen.queryByRole("button", { name: "Grid view" })).toBeNull()
    await user.pointer({ keys: "[MouseRight]", target: row })
    await user.click(
      await screen.findByRole("menuitem", { name: "Cancel run" }),
    )
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
})
