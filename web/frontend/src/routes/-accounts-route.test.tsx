import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import {
  RouterProvider,
  createMemoryHistory,
  createRouter,
} from "@tanstack/react-router"
import { render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { type ReactNode, useEffect, useRef } from "react"
import { describe, expect, it, vi } from "vitest"

import { routeTree } from "@/routeTree.gen"

vi.mock("@/api/launcher-auth", () => ({
  getLauncherAuthStatus: vi
    .fn()
    .mockResolvedValue({ authenticated: true, initialized: true }),
}))
vi.mock("@/components/app-layout", () => ({
  AppLayout: ({ children }: { children: ReactNode }) => <div>{children}</div>,
}))
vi.mock("@/features/chat/controller", () => ({ initializeChatStore: vi.fn() }))

vi.mock("@/components/credentials/account-oauth-callback-recovery", () => ({
  AccountOAuthCallbackRecovery: ({
    flowID,
    onConsumed,
  }: {
    flowID: string
    onConsumed: () => void
  }) => {
    const onConsumedRef = useRef(onConsumed)
    useEffect(() => onConsumedRef.current(), [])
    return <output data-testid="oauth-callback-recovery">{flowID}</output>
  },
}))

vi.mock("@/components/credentials/account-collections", () => ({
  AccountsCollectionPage: ({
    search,
    onAdd,
    onOpen,
    onEdit,
    onRouters,
  }: {
    search: object
    onAdd: () => void
    onOpen: (account: { id: string }) => void
    onEdit: (account: { id: string }) => void
    onRouters: () => void
  }) => (
    <div>
      <output data-testid="accounts-search">{JSON.stringify(search)}</output>
      <button type="button" onClick={onAdd}>
        Add account
      </button>
      <button type="button" onClick={() => onOpen({ id: "account-hash" })}>
        Open account
      </button>
      <button type="button" onClick={() => onEdit({ id: "account-hash" })}>
        Renew account
      </button>
      <button type="button" onClick={onRouters}>
        Account routers
      </button>
    </div>
  ),
  AccountDetailPage: ({
    id,
    onBack,
    onEdit,
  }: {
    id: string
    onBack: () => void
    onEdit: () => void
  }) => (
    <div>
      <output data-testid="account-detail">{id}</output>
      <button type="button" onClick={onBack}>
        All accounts
      </button>
      <button type="button" onClick={onEdit}>
        Renew
      </button>
    </div>
  ),
  AccountRoutersCollectionPage: ({
    search,
    onAdd,
    onOpen,
  }: {
    search: object
    onAdd: () => void
    onOpen: (router: { id: string }) => void
  }) => (
    <div>
      <output data-testid="routers-search">{JSON.stringify(search)}</output>
      <button type="button" onClick={onAdd}>
        Add router
      </button>
      <button type="button" onClick={() => onOpen({ id: "team-router" })}>
        Open router
      </button>
    </div>
  ),
  AccountRouterDetailPage: ({
    id,
    onBack,
    onEdit,
  }: {
    id: string
    onBack: () => void
    onEdit: () => void
  }) => (
    <div>
      <output data-testid="router-detail">{id}</output>
      <button type="button" onClick={onBack}>
        All account routers
      </button>
      <button type="button" onClick={onEdit}>
        Edit router
      </button>
    </div>
  ),
}))

vi.mock("@/components/credentials/account-auth-editor-page", () => ({
  AccountAuthEditorPage: ({
    mode,
    id,
    onSaved,
  }: {
    mode: string
    id?: string
    onSaved: (id?: string) => void
  }) => (
    <div>
      <output data-testid="account-editor">{`${mode}:${id ?? "new"}`}</output>
      <button type="button" onClick={() => onSaved(id)}>
        Save account
      </button>
    </div>
  ),
}))

vi.mock("@/components/credentials/account-router-editor-page", () => ({
  AccountRouterEditorPage: ({
    mode,
    routerID,
    onSaved,
  }: {
    mode: string
    routerID?: string
    onSaved: (id: string) => void
  }) => (
    <div>
      <output data-testid="router-editor">{`${mode}:${routerID ?? "new"}`}</output>
      <button type="button" onClick={() => onSaved(routerID ?? "team-router")}>
        Save router
      </button>
    </div>
  ),
}))

function renderRoute(pathname: string) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const router = createRouter({
    routeTree,
    history: createMemoryHistory({ initialEntries: [pathname] }),
    context: { queryClient },
  })
  render(
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
  return router
}

describe("accounts collection routes", () => {
  it("recovers a hard-navigation OAuth callback and preserves canonical collection state", async () => {
    const router = renderRoute(
      "/accounts?oauth_flow_id=flow-hard-navigation&q=provider%20%3D%20google-antigravity&view=grid",
    )

    expect(
      await screen.findByTestId("oauth-callback-recovery"),
    ).toHaveTextContent("flow-hard-navigation")
    await waitFor(() =>
      expect(router.state.location.search).toEqual({
        q: "provider = google-antigravity",
        view: "grid",
      }),
    )
    expect(screen.getByTestId("accounts-search")).toHaveTextContent(
      JSON.stringify({ q: "provider = google-antigravity", view: "grid" }),
    )
  })

  it("normalizes legacy search state without compatibility-rendering routers", async () => {
    const router = renderRoute(
      "/accounts?tab=routers&q=status%20%3D%20connected&view=grid",
    )
    expect(await screen.findByTestId("accounts-search")).toHaveTextContent(
      JSON.stringify({ q: "status = connected", view: "grid" }),
    )
    await waitFor(() =>
      expect(router.state.location.search).toEqual({
        q: "status = connected",
        view: "grid",
      }),
    )
    expect(screen.queryByTestId("routers-search")).toBeNull()
  })

  it("preserves collection state through direct account detail and renewal routes", async () => {
    const user = userEvent.setup()
    const router = renderRoute("/accounts?q=provider%20%3D%20openai&view=table")

    await user.click(
      await screen.findByRole("button", { name: "Open account" }),
    )
    await waitFor(() =>
      expect(router.state.location.pathname).toBe("/accounts/account-hash"),
    )
    expect(router.state.location.search).toEqual({
      q: "provider = openai",
      view: "table",
    })
    expect(screen.getByTestId("account-detail")).toHaveTextContent(
      "account-hash",
    )

    await user.click(screen.getByRole("button", { name: "Renew" }))
    await waitFor(() =>
      expect(router.state.location.pathname).toBe(
        "/accounts/account-hash/edit",
      ),
    )
    expect(screen.getByTestId("account-editor")).toHaveTextContent(
      "edit:account-hash",
    )
  })

  it("uses a separate canonical router collection and routed new/detail/edit pages", async () => {
    const user = userEvent.setup()
    const router = renderRoute(
      "/accounts/routers?q=status%20%3D%20available&view=list",
    )
    expect(await screen.findByTestId("routers-search")).toHaveTextContent(
      JSON.stringify({ q: "status = available", view: "list" }),
    )

    await user.click(screen.getByRole("button", { name: "Open router" }))
    await waitFor(() =>
      expect(router.state.location.pathname).toBe(
        "/accounts/routers/team-router",
      ),
    )
    await user.click(screen.getByRole("button", { name: "Edit router" }))
    await waitFor(() =>
      expect(router.state.location.pathname).toBe(
        "/accounts/routers/team-router/edit",
      ),
    )
    expect(screen.getByTestId("router-editor")).toHaveTextContent(
      "edit:team-router",
    )
  })

  it.each(["/accounts/account-router/7", "/accounts/account-router/new"])(
    "does not route the legacy account-router URL %s",
    async (legacyURL) => {
      const router = renderRoute(legacyURL)
      expect(
        await screen.findByRole("heading", { name: "Page not found" }),
      ).toBeVisible()
      expect(router.state.location.pathname).toBe(legacyURL)
      expect(screen.queryByTestId("router-editor")).toBeNull()
    },
  )
})
