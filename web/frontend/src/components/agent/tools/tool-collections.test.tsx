import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { fireEvent, render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import type { ReactNode } from "react"
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest"

import {
  type ToolSupportItem,
  getTool,
  listTools,
  setToolEnabled,
} from "@/api/tools"
import {
  ToolDetailPage,
  ToolsCollectionPage,
} from "@/components/agent/tools/tool-collections"
import { resetCollectionRouteStateMemoryForTests } from "@/hooks/use-collection-route-state"
import { refreshGatewayState } from "@/store/gateway"

vi.mock("@/api/tools", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/tools")>()
  return {
    ...actual,
    getTool: vi.fn(),
    listTools: vi.fn(),
    setToolEnabled: vi.fn(),
  }
})
vi.mock("@/components/page-header", () => ({
  PageHeader: ({
    title,
    titleExtra,
    children,
  }: {
    title: string
    titleExtra?: ReactNode
    children?: ReactNode
  }) => (
    <header>
      <h1>{title}</h1>
      {titleExtra}
      {children}
    </header>
  ),
}))
vi.mock("@/store/gateway", () => ({ refreshGatewayState: vi.fn() }))
vi.mock("@/lib/restart-required", () => ({
  showSaveSuccessOrRestartToast: vi.fn(),
}))

const workflowTool: ToolSupportItem = {
  id: "workflow-tool",
  name: "workflow",
  description: "Run and inspect workflows.",
  category: "automation",
  config_key: "tools.workflow",
  status: "blocked",
  reason: "requires_workflows",
  reason_code: "requires_workflows",
}

function toolsPage() {
  return {
    tools: [workflowTool],
    total: 1,
    canonical_query: "ORDER BY category ASC, name ASC",
    query_schema: { fields: [] },
  }
}

describe("tools collection controller", () => {
  beforeAll(() => {
    Object.defineProperties(HTMLElement.prototype, {
      hasPointerCapture: { configurable: true, value: vi.fn(() => false) },
      setPointerCapture: { configurable: true, value: vi.fn() },
      releasePointerCapture: { configurable: true, value: vi.fn() },
      scrollIntoView: { configurable: true, value: vi.fn() },
    })
  })

  beforeEach(() => {
    vi.clearAllMocks()
    resetCollectionRouteStateMemoryForTests()
    globalThis.localStorage.clear()
    vi.mocked(listTools).mockResolvedValue(toolsPage())
    vi.mocked(setToolEnabled).mockResolvedValue({ status: "ok" })
    vi.mocked(refreshGatewayState).mockResolvedValue({
      status: "running",
      canStart: true,
      restartRequired: false,
    })
  })

  it("renders shared Table fields and toggles by backend-issued ID", async () => {
    const user = userEvent.setup()
    renderTools("table")
    expect(
      await screen.findByRole("columnheader", { name: "Status" }),
    ).toBeVisible()
    expect(screen.getByRole("cell", { name: "Blocked" })).toBeVisible()

    fireEvent.contextMenu(toolItem())
    await user.click(
      await screen.findByRole("menuitem", { name: "Disable tool" }),
    )
    await waitFor(() =>
      expect(setToolEnabled).toHaveBeenCalledWith("workflow-tool", false),
    )
  })

  it("opens items directly while exposing no bulk selection controls", async () => {
    const onOpen = vi.fn()
    renderTools("list", onOpen)
    await screen.findByText("workflow")
    fireEvent.doubleClick(toolItem())
    expect(onOpen).toHaveBeenCalledWith(workflowTool)
    expect(screen.queryByText(/selected/)).toBeNull()
  })

  it("loads direct tool detail and exposes routed configuration", async () => {
    vi.mocked(getTool).mockResolvedValue({ tool: workflowTool })
    const onEdit = vi.fn()
    const user = userEvent.setup()
    renderWithClient(
      <ToolDetailPage
        toolID="workflow-tool"
        onBack={vi.fn()}
        onEdit={onEdit}
      />,
    )
    expect(
      await screen.findByRole("heading", { name: "workflow" }),
    ).toBeVisible()
    expect(getTool).toHaveBeenCalledWith(
      "workflow-tool",
      expect.any(AbortSignal),
    )
    await user.click(screen.getByRole("button", { name: "Configure" }))
    expect(onEdit).toHaveBeenCalledOnce()
  })
})

function renderTools(view: "list" | "table" | "grid", onOpen = vi.fn()) {
  return renderWithClient(
    <ToolsCollectionPage
      search={{ q: "ORDER BY category ASC, name ASC", view }}
      onSearchChange={vi.fn()}
      onOpen={onOpen}
      onEdit={vi.fn()}
      onAdaptation={vi.fn()}
    />,
  )
}

function renderWithClient(element: ReactNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(
    <QueryClientProvider client={client}>{element}</QueryClientProvider>,
  )
}

function toolItem(): HTMLElement {
  const item = document.querySelector<HTMLElement>(
    '[data-item-id="workflow-tool"]',
  )
  if (!item) throw new Error("Missing workflow tool item")
  return item
}
