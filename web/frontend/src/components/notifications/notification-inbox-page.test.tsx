import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, waitFor, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import type { ReactNode } from "react"
import { beforeEach, describe, expect, it, vi } from "vitest"

import {
  type DevelopmentNotification,
  NotificationAPIError,
  getDevelopmentNotification,
  getDevelopmentNotificationNeighbors,
  getNotificationSavedViews,
  listDevelopmentNotifications,
  mutateDevelopmentNotifications,
  openNotificationEventStream,
  updateNotificationSavedViews,
} from "@/api/notifications"
import { NotificationInboxPage } from "@/components/notifications/notification-inbox-page"

vi.mock("@/api/notifications", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/notifications")>()
  return {
    ...actual,
    getDevelopmentNotification: vi.fn(),
    getDevelopmentNotificationNeighbors: vi.fn(),
    getNotificationSavedViews: vi.fn(),
    listDevelopmentNotifications: vi.fn(),
    mutateDevelopmentNotifications: vi.fn(),
    openNotificationEventStream: vi.fn(),
    updateNotificationSavedViews: vi.fn(),
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

vi.mock("@/components/notifications/push-notification-settings", () => ({
  PushNotificationSettings: ({ open }: { open: boolean }) =>
    open ? <div>Push settings</div> : null,
}))

vi.mock("@/lib/pwa-notifications", () => ({
  refreshPicoClawAppBadge: vi.fn(),
}))

const publicationNotification: DevelopmentNotification = {
  id: "ntf_11111111111111111111111111111111",
  source_key: "workspace:publication",
  generation: 1,
  workspace_id: "devw_11111111111111111111111111111111",
  repository: "owner/repo",
  intent: "implement_feature",
  source_kind: "issue",
  phase: "publication",
  reason: "publication_approval",
  priority: "high",
  status: "open",
  read: false,
  title: "Approve draft PR publication",
  summary: "Validation passed. Review the frozen candidate before publication.",
  target: { panel: "publication", entity_id: "gate-1" },
  version: 3,
  created_at: "2026-08-24T10:00:00Z",
  updated_at: "2026-08-24T10:05:00Z",
}

const scopeNotification: DevelopmentNotification = {
  ...publicationNotification,
  id: "ntf_22222222222222222222222222222222",
  source_key: "workspace:scope",
  title: "Decide CI scope exception",
  reason: "scope_exception",
  priority: "medium",
  repository: "owner/other",
  version: 1,
}

function renderPage(
  props: React.ComponentProps<typeof NotificationInboxPage> = {},
) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  return render(
    <QueryClientProvider client={queryClient}>
      <NotificationInboxPage {...props} />
    </QueryClientProvider>,
  )
}

describe("NotificationInboxPage", () => {
  beforeEach(() => {
    vi.mocked(listDevelopmentNotifications).mockReset()
    vi.mocked(getDevelopmentNotification).mockReset()
    vi.mocked(getDevelopmentNotificationNeighbors).mockReset()
    vi.mocked(getNotificationSavedViews).mockReset()
    vi.mocked(mutateDevelopmentNotifications).mockReset()
    vi.mocked(updateNotificationSavedViews).mockReset()
    vi.mocked(openNotificationEventStream).mockReset()
    vi.mocked(listDevelopmentNotifications).mockResolvedValue({
      notifications: [publicationNotification, scopeNotification],
      total: 2,
      counts: { open: 2, unread: 2, snoozed: 0 },
    })
    vi.mocked(getDevelopmentNotification).mockImplementation(async (id) =>
      id === scopeNotification.id ? scopeNotification : publicationNotification,
    )
    vi.mocked(getDevelopmentNotificationNeighbors).mockResolvedValue({
      next_id: scopeNotification.id,
    })
    vi.mocked(getNotificationSavedViews).mockResolvedValue({
      version: 2,
      views: [
        {
          id: "view-1",
          name: "Critical only",
          query: "priority = critical ORDER BY updated DESC",
          pinned: true,
          default: false,
          position: 0,
          version: 1,
          created_at: "2026-08-24T09:00:00Z",
          updated_at: "2026-08-24T09:00:00Z",
        },
      ],
    })
    vi.mocked(mutateDevelopmentNotifications).mockResolvedValue({
      notifications: [],
    })
    vi.mocked(updateNotificationSavedViews).mockResolvedValue({
      version: 3,
      views: [],
    })
    vi.mocked(openNotificationEventStream).mockReturnValue(undefined)
  })

  it("renders piled notifications and navigates list/detail/workspace", async () => {
    const user = userEvent.setup()
    const onNotificationChange = vi.fn()
    const onOpenWorkspace = vi.fn()
    renderPage({ onNotificationChange, onOpenWorkspace })

    expect(
      await screen.findByText("Approve draft PR publication"),
    ).toBeVisible()
    expect(screen.getByText("Decide CI scope exception")).toBeVisible()
    expect(screen.getByText("2 open")).toBeVisible()

    await user.click(
      screen.getByRole("button", { name: "Open Approve draft PR publication" }),
    )
    expect(onNotificationChange).toHaveBeenCalledWith(
      publicationNotification.id,
    )
    expect(
      await screen.findByText(publicationNotification.summary),
    ).toBeVisible()

    await user.click(
      screen.getByRole("button", { name: "Open required action" }),
    )
    expect(onOpenWorkspace).toHaveBeenCalledWith(
      publicationNotification.workspace_id,
      publicationNotification.target,
    )
  })

  it("performs fenced bulk actions", async () => {
    const user = userEvent.setup()
    renderPage()
    await screen.findByText("Approve draft PR publication")

    await user.click(
      screen.getByRole("checkbox", {
        name: "Select Approve draft PR publication",
      }),
    )
    const toolbar = screen.getByText("1 selected").parentElement!
    await user.click(within(toolbar).getByRole("button", { name: "Read" }))

    await waitFor(() =>
      expect(mutateDevelopmentNotifications).toHaveBeenCalled(),
    )
    expect(
      vi.mocked(mutateDevelopmentNotifications).mock.calls[0]?.[0],
    ).toMatchObject({
      action: "mark_read",
      items: [{ id: publicationNotification.id, expected_version: 3 }],
    })
  })

  it("applies advanced queries through route integration hook", async () => {
    const user = userEvent.setup()
    const onQueryChange = vi.fn()
    renderPage({ onQueryChange })
    await screen.findByText("Approve draft PR publication")

    await user.click(screen.getByRole("button", { name: "Advanced" }))
    const editor = screen.getByLabelText("Advanced query")
    await user.clear(editor)
    await user.type(editor, "status = resolved ORDER BY updated DESC")
    await user.click(screen.getByRole("button", { name: "Run query" }))

    expect(onQueryChange).toHaveBeenCalledWith(
      "status = resolved ORDER BY updated DESC",
    )
  })

  it("keeps server query errors inline with their exact position", async () => {
    const user = userEvent.setup()
    vi.mocked(listDevelopmentNotifications).mockImplementation(
      async (input) => {
        const query = input?.query
        if (query === "NOT") {
          throw new NotificationAPIError(
            400,
            "expected query field",
            "invalid_query",
            3,
          )
        }
        return {
          notifications: [publicationNotification],
          total: 1,
        }
      },
    )
    renderPage()
    await screen.findByText("Approve draft PR publication")

    await user.click(screen.getByRole("button", { name: "Advanced" }))
    const editor = screen.getByLabelText("Advanced query")
    await user.clear(editor)
    await user.type(editor, "NOT")
    await user.click(screen.getByRole("button", { name: "Run query" }))

    expect(
      await screen.findByText("Position 3: expected query field"),
    ).toBeVisible()
    expect(editor).toHaveAttribute("aria-invalid", "true")
  })

  it("edits, duplicates, and persists saved views as one revision", async () => {
    const user = userEvent.setup()
    renderPage()
    await screen.findByText("Critical only")

    await user.click(screen.getByRole("button", { name: "Manage saved views" }))
    await user.click(
      screen.getByRole("button", { name: "Duplicate Critical only" }),
    )
    await user.clear(screen.getByLabelText("View 2 name"))
    await user.type(screen.getByLabelText("View 2 name"), "Release approvals")
    await user.click(screen.getByRole("button", { name: "Save views" }))

    await waitFor(() => expect(updateNotificationSavedViews).toHaveBeenCalled())
    expect(
      vi.mocked(updateNotificationSavedViews).mock.calls[0]?.[0],
    ).toMatchObject({
      expected_version: 2,
      views: [
        expect.objectContaining({ id: "view-1", name: "Critical only" }),
        expect.objectContaining({ name: "Release approvals" }),
      ],
    })
  })
})
