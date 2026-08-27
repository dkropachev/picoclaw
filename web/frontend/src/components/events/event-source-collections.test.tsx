import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, waitFor, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest"

import {
  bulkDeleteEventSources,
  deleteEventSource,
  getEventSource,
  listEventSources,
  updateEventSource,
} from "@/api/event-sources"
import { SidebarProvider } from "@/components/ui/sidebar"

import {
  EventSourceDetailPage,
  EventSourcesCollectionPage,
} from "./event-source-collections"

vi.mock("@/api/event-sources", () => ({
  listEventSources: vi.fn(),
  getEventSource: vi.fn(),
  updateEventSource: vi.fn(),
  deleteEventSource: vi.fn(),
  bulkDeleteEventSources: vi.fn(),
}))
vi.mock("@/store/gateway", () => ({
  refreshGatewayState: vi.fn().mockResolvedValue({ restartRequired: false }),
}))
vi.mock("@/lib/restart-required", () => ({
  showSaveSuccessOrRestartToast: vi.fn(),
}))

const githubSummary = {
  id: "evt_github",
  name: "github",
  kind: "webhook" as const,
  enabled: true,
  format: "github" as const,
  status: "available" as const,
  repositories: 2,
  poll_notifications: true,
}
const mailSummary = {
  id: "evt_mail",
  name: "mail",
  kind: "channel" as const,
  enabled: false,
  format: "deltachat" as const,
  status: "disabled" as const,
  repositories: 0,
  poll_notifications: false,
}
const githubDetail = {
  ...githubSummary,
  repositories: ["octo/picoclaw", "octo/docs"],
  target_user: "octocat",
  secret_configured: true,
  endpoint: "/webhooks/events/github",
}

describe("event source collection", () => {
  beforeAll(installBrowserPolyfills)
  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(listEventSources).mockResolvedValue({
      event_sources: [githubSummary, mailSummary],
      total: 2,
      next_cursor: "",
      canonical_query: "ORDER BY name ASC",
      query_schema: { fields: [] },
      config_revision: "revision-1",
    })
    vi.mocked(getEventSource).mockResolvedValue({
      event_source: githubDetail,
      config_revision: "revision-1",
    })
  })

  it("renders configured summaries through all shared collection views", async () => {
    renderCollection()

    expect(await screen.findByText("github", { exact: true })).toBeVisible()
    expect(screen.getByText("mail", { exact: true })).toBeVisible()
    expect(screen.getByText("POST /webhooks/events/github")).toBeVisible()
    expect(screen.getByRole("button", { name: "List view" })).toHaveAttribute(
      "aria-pressed",
      "true",
    )
    expect(screen.getByRole("button", { name: "Table view" })).toBeVisible()
    expect(screen.getByRole("button", { name: "Grid view" })).toBeVisible()
  })

  it("retains failed explicit bulk selections", async () => {
    const user = userEvent.setup()
    vi.mocked(bulkDeleteEventSources).mockResolvedValue({
      deleted_ids: [mailSummary.id],
      failures: [{ id: githubSummary.id, code: "not_found" }],
      config_revision: "revision-2",
      effects: {
        launcher_effect: "applied",
        catalog_effect: "applied",
        gateway_effect: "applied",
      },
    })
    renderCollection()
    const github = await screen.findByLabelText("github. Not selected.")
    const mail = screen.getByLabelText("mail. Not selected.")
    await user.click(github)
    await user.keyboard("{Control>}")
    await user.click(mail)
    await user.keyboard("{/Control}")
    expect(screen.getByText("2 selected", { exact: true })).toBeVisible()
    await user.click(screen.getByRole("button", { name: "Delete" }))
    const dialog = screen.getByRole("alertdialog")
    await within(dialog)
      .getByRole("button", { name: "Delete selected" })
      .click()

    await waitFor(() =>
      expect(bulkDeleteEventSources).toHaveBeenCalledWith(
        [githubSummary.id, mailSummary.id],
        "revision-1",
      ),
    )
    expect(screen.getByText("1 selected", { exact: true })).toBeVisible()
  })

  it("toggles from safe detail projection and preserves credentials", async () => {
    const user = userEvent.setup()
    vi.mocked(updateEventSource).mockResolvedValue({
      event_source: { ...githubDetail, enabled: false, status: "disabled" },
      config_revision: "revision-2",
      effects: {
        launcher_effect: "applied",
        catalog_effect: "applied",
        gateway_effect: "applied",
      },
    })
    renderCollection()
    await screen.findByText("github", { exact: true })
    const github = document.querySelector<HTMLElement>(
      `[data-item-id="${githubSummary.id}"]`,
    )
    expect(github).not.toBeNull()
    await user.pointer({ target: github!, keys: "[MouseRight]" })
    await screen.getByRole("menuitem", { name: "Disable source" }).click()

    await waitFor(() =>
      expect(updateEventSource).toHaveBeenCalledWith(
        githubSummary.id,
        expect.objectContaining({
          enabled: false,
          secret_update: "preserve",
        }),
        "revision-1",
      ),
    )
  })
})

describe("event source detail", () => {
  beforeAll(installBrowserPolyfills)
  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(getEventSource).mockResolvedValue({
      event_source: githubDetail,
      config_revision: "revision-1",
    })
  })

  it("loads direct detail without collection state and confirms deletion", async () => {
    const user = userEvent.setup()
    const onRemoved = vi.fn()
    vi.mocked(deleteEventSource).mockResolvedValue({
      deleted_ids: [githubSummary.id],
      failures: [],
      config_revision: "revision-2",
      effects: {
        launcher_effect: "applied",
        catalog_effect: "applied",
        gateway_effect: "applied",
      },
    })
    renderWithClient(
      <EventSourceDetailPage
        id={githubSummary.id}
        onBack={vi.fn()}
        onEdit={vi.fn()}
        onRemoved={onRemoved}
      />,
    )

    expect(
      await screen.findByText("POST /webhooks/events/github"),
    ).toBeVisible()
    expect(screen.getByText("Configured")).toBeVisible()
    expect(document.body).not.toHaveTextContent("x".repeat(32))
    await user.click(screen.getByRole("button", { name: "Delete source" }))
    await within(screen.getByRole("alertdialog"))
      .getByRole("button", { name: "Delete source" })
      .click()
    await waitFor(() => expect(onRemoved).toHaveBeenCalledOnce())
    expect(deleteEventSource).toHaveBeenCalledWith(
      githubSummary.id,
      "revision-1",
    )
  })
})

function renderCollection() {
  renderWithClient(
    <EventSourcesCollectionPage
      search={{ q: "ORDER BY name ASC", view: "list" }}
      onSearchChange={vi.fn()}
      onAdd={vi.fn()}
      onOpen={vi.fn()}
      onEdit={vi.fn()}
      onSettings={vi.fn()}
    />,
  )
}

function renderWithClient(children: React.ReactNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  return render(
    <QueryClientProvider client={client}>
      <SidebarProvider>{children}</SidebarProvider>
    </QueryClientProvider>,
  )
}

function installBrowserPolyfills() {
  Object.defineProperty(window, "matchMedia", {
    configurable: true,
    value: vi.fn().mockImplementation((query: string) => ({
      matches: false,
      media: query,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    })),
  })
}
