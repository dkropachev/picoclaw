import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { fireEvent, render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import type { ReactNode } from "react"
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest"

import {
  bulkDeleteAccountRouters,
  getAccount,
  getAccountRouter,
  listAccountRouters,
  setDefaultAccountRouter,
  updateAccountRouter,
} from "@/api/accounts"
import { logoutOAuth } from "@/api/oauth"
import {
  AccountDetailPage,
  AccountRoutersCollectionPage,
} from "@/components/credentials/account-collections"
import { resetCollectionRouteStateMemoryForTests } from "@/hooks/use-collection-route-state"
import { showSaveSuccessOrRestartToast } from "@/lib/restart-required"
import { refreshGatewayState } from "@/store/gateway"

const { toastError } = vi.hoisted(() => ({ toastError: vi.fn() }))

vi.mock("sonner", () => ({
  toast: { error: toastError, success: vi.fn() },
}))
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
vi.mock("@/api/accounts", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/accounts")>()
  return {
    ...actual,
    bulkDeleteAccountRouters: vi.fn(),
    getAccount: vi.fn(),
    getAccountRouter: vi.fn(),
    listAccountRouters: vi.fn(),
    setDefaultAccountRouter: vi.fn(),
    updateAccountRouter: vi.fn(),
  }
})
vi.mock("@/api/oauth", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/oauth")>()
  return {
    ...actual,
    logoutOAuth: vi.fn(),
  }
})
vi.mock("@/lib/restart-required", () => ({
  showSaveSuccessOrRestartToast: vi.fn(),
}))
vi.mock("@/store/gateway", () => ({
  refreshGatewayState: vi.fn(),
}))

const routerSummary = {
  id: "team-router",
  name: "team-router",
  enabled: true,
  is_default: false,
  status: "available",
  entry: "primary",
  accounts: 1,
  blocks: 1,
}

const routerDetail = {
  ...routerSummary,
  accounts: ["credential:openai:work"],
  blocks: [
    {
      id: "primary",
      type: "account" as const,
      account: "credential:openai:work",
    },
  ],
  refresh_interval_seconds: 60,
}

function routerPage() {
  return {
    account_routers: [routerSummary],
    total: 1,
    canonical_query: "ORDER BY name ASC",
    query_schema: { fields: [] },
    config_revision: "revision-1",
  }
}

function mutationResponse(overrides: object = {}) {
  return {
    account_router: routerDetail,
    config_revision: "revision-2",
    effects: {
      launcher_effect: "applied" as const,
      catalog_effect: "applied" as const,
      gateway_effect: "applied" as const,
    },
    ...overrides,
  }
}

function renderWithClient(element: ReactNode) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(
    <QueryClientProvider client={queryClient}>{element}</QueryClientProvider>,
  )
}

function renderRouters(view: "list" | "table" = "list") {
  return renderWithClient(
    <AccountRoutersCollectionPage
      search={{ q: "ORDER BY name ASC", view }}
      onSearchChange={vi.fn()}
      onAdd={vi.fn()}
      onOpen={vi.fn()}
      onEdit={vi.fn()}
    />,
  )
}

function routerItem(): HTMLElement {
  const item = document.querySelector<HTMLElement>(
    '[data-item-id="team-router"]',
  )
  if (!item) throw new Error("Missing account router collection item")
  return item
}

describe("account collection controllers", () => {
  beforeAll(() => {
    Object.defineProperties(HTMLElement.prototype, {
      hasPointerCapture: {
        configurable: true,
        value: vi.fn(() => false),
      },
      setPointerCapture: { configurable: true, value: vi.fn() },
      releasePointerCapture: { configurable: true, value: vi.fn() },
      scrollIntoView: { configurable: true, value: vi.fn() },
    })
  })

  beforeEach(() => {
    vi.clearAllMocks()
    resetCollectionRouteStateMemoryForTests()
    globalThis.localStorage.clear()
    vi.mocked(listAccountRouters).mockResolvedValue(routerPage())
    vi.mocked(getAccountRouter).mockResolvedValue({
      account_router: routerDetail,
      config_revision: "revision-1",
    })
    vi.mocked(setDefaultAccountRouter).mockResolvedValue(mutationResponse())
    vi.mocked(updateAccountRouter).mockResolvedValue(
      mutationResponse({
        account_router: { ...routerDetail, enabled: false, status: "disabled" },
      }),
    )
    vi.mocked(refreshGatewayState).mockResolvedValue({
      status: "running",
      canStart: true,
      restartRequired: false,
    })
  })

  it("renders a bounded status badge in List and a Status column in Table", async () => {
    const list = renderRouters()
    expect(await screen.findByText("team-router")).toBeVisible()
    expect(screen.getByText("Available")).toBeVisible()
    list.unmount()

    renderRouters("table")
    expect(
      await screen.findByRole("columnheader", { name: "Status" }),
    ).toBeVisible()
    expect(screen.getByRole("cell", { name: "Available" })).toBeVisible()
  })

  it("uses revision-fenced default and enable actions from the context menu", async () => {
    const user = userEvent.setup()
    renderRouters()
    await screen.findByText("team-router")

    fireEvent.contextMenu(routerItem())
    await user.click(
      await screen.findByRole("menuitem", { name: "Make default" }),
    )
    await waitFor(() =>
      expect(setDefaultAccountRouter).toHaveBeenCalledWith(
        "team-router",
        "revision-1",
      ),
    )

    fireEvent.contextMenu(routerItem())
    await user.click(
      await screen.findByRole("menuitem", { name: "Disable router" }),
    )
    await waitFor(() =>
      expect(getAccountRouter).toHaveBeenCalledWith("team-router"),
    )
    expect(updateAccountRouter).toHaveBeenCalledWith(
      "team-router",
      expect.objectContaining({ enabled: false, blocks: routerDetail.blocks }),
      "revision-1",
    )
    expect(showSaveSuccessOrRestartToast).toHaveBeenCalled()
  })

  it("retains selection and safe blockers after a partial bulk failure", async () => {
    vi.mocked(bulkDeleteAccountRouters).mockResolvedValue({
      deleted_ids: [],
      failures: [
        { id: "team-router", code: "referenced", blockers: ["Agent reviewer"] },
      ],
      config_revision: "revision-1",
      effects: {
        launcher_effect: "applied",
        catalog_effect: "applied",
        gateway_effect: "applied",
      },
    })
    const user = userEvent.setup()
    renderRouters()
    await screen.findByText("team-router")

    await user.click(routerItem())
    await user.click(screen.getByRole("button", { name: "Delete" }))
    await user.click(screen.getByRole("button", { name: "Delete selected" }))

    await waitFor(() =>
      expect(bulkDeleteAccountRouters).toHaveBeenCalledWith(
        ["team-router"],
        "revision-1",
      ),
    )
    expect(await screen.findByText("Agent reviewer")).toBeVisible()
    expect(screen.getByText("1 selected")).toBeVisible()
  })

  it("keeps logout confirmation open and blocks duplicate confirms after failure", async () => {
    let rejectLogout!: (error: Error) => void
    vi.mocked(logoutOAuth).mockReturnValue(
      new Promise((_, reject) => {
        rejectLogout = reject
      }),
    )
    vi.mocked(getAccount).mockResolvedValue({
      account: {
        id: "opaque-account-id",
        provider: "anthropic",
        account: "anthropic:work",
        status: "connected",
        auth_method: "token",
        expires_at: "",
      },
    })
    const onRemoved = vi.fn()
    const user = userEvent.setup()
    renderWithClient(
      <AccountDetailPage
        id="opaque-account-id"
        onBack={vi.fn()}
        onEdit={vi.fn()}
        onRemoved={onRemoved}
      />,
    )

    await user.click(await screen.findByRole("button", { name: "Remove" }))
    const confirm = screen.getByRole("button", { name: "Logout" })
    await user.click(confirm)
    expect(confirm).toBeDisabled()
    await user.click(confirm)
    expect(logoutOAuth).toHaveBeenCalledOnce()

    rejectLogout(new Error("Logout service unavailable"))
    await waitFor(() =>
      expect(toastError).toHaveBeenCalledWith("Logout service unavailable"),
    )
    expect(
      screen.getByRole("heading", { name: "Logout provider?" }),
    ).toBeVisible()
    expect(screen.getByRole("button", { name: "Logout" })).toBeEnabled()
    expect(onRemoved).not.toHaveBeenCalled()
  })
})
