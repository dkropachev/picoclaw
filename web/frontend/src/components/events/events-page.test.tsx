import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, waitFor, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import type { AnchorHTMLAttributes, ReactNode } from "react"
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest"

import type { DispatchView, EventView } from "@/api/events"
import {
  getEvent,
  getEventDispatch,
  getEventPayload,
  listEventDispatches,
  listEvents,
  replayEvent,
} from "@/api/events"
import {
  EventsPage,
  type EventsRouteSearch,
} from "@/components/events/events-page"
import { SidebarProvider } from "@/components/ui/sidebar"

vi.mock("@/api/events", () => ({
  getEvent: vi.fn(),
  getEventDispatch: vi.fn(),
  getEventPayload: vi.fn(),
  listEventDispatches: vi.fn(),
  listEvents: vi.fn(),
  replayEvent: vi.fn(),
}))

vi.mock("@tanstack/react-router", () => ({
  Link: ({
    children,
    to,
    ...props
  }: {
    children: ReactNode
    to: string
  } & AnchorHTMLAttributes<HTMLAnchorElement>) => (
    <a {...props} href={to}>
      {children}
    </a>
  ),
}))

const eventA: EventView = {
  id: "ev_11111111111111111111111111111111",
  source: "github",
  connector: "primary",
  type: "issues.opened",
  actor: {
    id: "actor-1",
    type: "user",
    display_name: "Ada",
  },
  subject: {
    id: "issue-42",
    type: "issue",
    name: "Keep JSON numbers exact",
  },
  occurred_at: "2026-07-29T13:00:00Z",
  received_at: "2026-07-29T13:00:01Z",
  attributes: { repository: "octo/repo" },
  payload_bytes: 74,
  routing: {
    status: "succeeded",
    available_at: "2026-07-29T13:00:01Z",
    attempts: 1,
    updated_at: "2026-07-29T13:00:03Z",
  },
}

const eventB: EventView = {
  ...eventA,
  id: "ev_22222222222222222222222222222222",
  type: "pull_request.closed",
  received_at: "2026-07-29T13:01:01Z",
  routing: {
    ...eventA.routing,
    status: "dead",
  },
}

const replayedEvent: EventView = {
  ...eventA,
  id: "ev_33333333333333333333333333333333",
  replay_of: eventA.id,
  routing: {
    ...eventA.routing,
    status: "pending",
    attempts: 0,
  },
}

const dispatchA: DispatchView = {
  id: "dsp_11111111111111111111111111111111",
  event_id: eventA.id,
  workflow_ref: "workflows/github-issue-triage.yml",
  workflow_revision:
    "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  run_id: "wr_event_1",
  status: "succeeded",
  available_at: "2026-07-29T13:00:01Z",
  attempts: 1,
  created_at: "2026-07-29T13:00:02Z",
  updated_at: "2026-07-29T13:00:03Z",
  linked_at: "2026-07-29T13:00:02Z",
  finished_at: "2026-07-29T13:00:03Z",
}

const dispatchB: DispatchView = {
  ...dispatchA,
  id: "dsp_22222222222222222222222222222222",
  event_id: eventB.id,
  workflow_ref: "workflows/pull-request-close.yml",
  run_id: "",
  status: "pending",
  workflow_revision:
    "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
  updated_at: "2026-07-29T13:01:03Z",
  linked_at: undefined,
  finished_at: undefined,
}

describe("EventsPage", () => {
  beforeAll(() => {
    Object.defineProperty(window, "matchMedia", {
      writable: true,
      value: vi.fn().mockImplementation((query: string) => ({
        matches: false,
        media: query,
        onchange: null,
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
        addListener: vi.fn(),
        removeListener: vi.fn(),
        dispatchEvent: vi.fn(),
      })),
    })
  })

  beforeEach(() => {
    vi.mocked(getEvent).mockReset()
    vi.mocked(getEventDispatch).mockReset()
    vi.mocked(getEventPayload).mockReset()
    vi.mocked(listEventDispatches).mockReset()
    vi.mocked(listEvents).mockReset()
    vi.mocked(replayEvent).mockReset()

    vi.mocked(listEvents).mockResolvedValue({
      events: [eventA, eventB],
    })
    vi.mocked(getEvent).mockImplementation(async (eventID) => {
      if (eventID === eventB.id) {
        return eventB
      }
      if (eventID === replayedEvent.id) {
        return replayedEvent
      }
      return eventA
    })
    vi.mocked(listEventDispatches).mockResolvedValue({
      dispatches: [dispatchA],
    })
    vi.mocked(getEventDispatch).mockImplementation(async (dispatchID) =>
      dispatchID === dispatchB.id ? dispatchB : dispatchA,
    )
    vi.mocked(getEventPayload).mockResolvedValue('{"issue":9007199254740993}')
    vi.mocked(replayEvent).mockResolvedValue({ event: replayedEvent })
  })

  it("loads a cursor-paginated event list and selected detail", async () => {
    vi.mocked(listEvents)
      .mockResolvedValueOnce({
        events: [eventA],
        next_cursor: "cursor-page-2",
      })
      .mockResolvedValueOnce({ events: [eventB] })

    const user = userEvent.setup()
    renderEventsPage({ event: eventA.id })

    await waitFor(() => {
      expect(getEvent).toHaveBeenCalledWith(eventA.id)
      expect(listEventDispatches).toHaveBeenCalledWith(
        expect.objectContaining({ eventID: eventA.id, limit: 40 }),
      )
    })
    expect(getEventPayload).not.toHaveBeenCalled()
    expect(
      (await screen.findAllByText("workflows/github-issue-triage.yml")).length,
    ).toBeGreaterThan(0)

    await user.click(screen.getByRole("button", { name: "Load more" }))

    await waitFor(() => {
      expect(listEvents).toHaveBeenNthCalledWith(
        2,
        expect.objectContaining({ cursor: "cursor-page-2", limit: 40 }),
      )
    })
    expect(screen.getByText("pull_request.closed")).toBeInTheDocument()
  })

  it("loads exact payload text only after explicit activation and clears it on selection", async () => {
    const rawPayload = '{"issue":9007199254740993,"rate":1e-400}'
    vi.mocked(getEventPayload).mockResolvedValue(rawPayload)
    const onSearchChange = vi.fn()
    const view = renderEventsPage({ event: eventA.id }, onSearchChange)
    const user = userEvent.setup()

    const loadPayload = await screen.findByRole("button", {
      name: "Show payload",
    })
    expect(getEventPayload).not.toHaveBeenCalled()

    await user.click(loadPayload)

    const payloadRegion = await screen.findByRole("region", {
      name: "Event payload",
    })
    expect(payloadRegion.textContent).toBe(rawPayload)
    expect(getEventPayload).toHaveBeenCalledTimes(1)
    expect(getEventPayload).toHaveBeenCalledWith(
      eventA.id,
      expect.any(AbortSignal),
    )

    view.rerenderPage({ event: eventB.id })

    await waitFor(() => {
      expect(getEvent).toHaveBeenCalledWith(eventB.id)
    })
    expect(getEventPayload).toHaveBeenCalledTimes(1)
    expect(screen.queryByText(rawPayload)).not.toBeInTheDocument()
    expect(
      await screen.findByRole("button", { name: "Show payload" }),
    ).toBeInTheDocument()
  })

  it("aborts and discards an in-flight payload when selection changes", async () => {
    let payloadSignal: AbortSignal | undefined
    vi.mocked(getEventPayload).mockImplementation(
      (_eventID, signal) =>
        new Promise((_resolve, reject) => {
          payloadSignal = signal
          signal?.addEventListener("abort", () => {
            reject(new DOMException("Aborted", "AbortError"))
          })
        }),
    )
    const view = renderEventsPage({ event: eventA.id })
    const user = userEvent.setup()

    await user.click(
      await screen.findByRole("button", { name: "Show payload" }),
    )
    await waitFor(() => {
      expect(payloadSignal).toBeDefined()
    })

    view.rerenderPage({ event: eventB.id })

    await waitFor(() => {
      expect(payloadSignal?.aborted).toBe(true)
    })
    expect(
      await screen.findByRole("button", { name: "Show payload" }),
    ).toBeInTheDocument()
  })

  it("retries a failed list refresh from the first page", async () => {
    vi.mocked(listEvents)
      .mockResolvedValueOnce({
        events: [eventA],
        next_cursor: "cursor-page-2",
      })
      .mockRejectedValueOnce(new Error("refresh failed"))
      .mockResolvedValueOnce({
        events: [eventA],
        next_cursor: "cursor-page-2",
      })
    const user = userEvent.setup()
    renderEventsPage({ event: eventA.id })

    expect((await screen.findAllByText(eventA.type)).length).toBeGreaterThan(0)
    await user.click(screen.getByRole("button", { name: "Refresh" }))
    await user.click(await screen.findByRole("button", { name: "Try again" }))

    await waitFor(() => {
      expect(listEvents).toHaveBeenCalledTimes(3)
    })
    expect(listEvents).toHaveBeenLastCalledWith(
      expect.not.objectContaining({ cursor: expect.anything() }),
    )
  })

  it("retries a failed next event page with its cursor", async () => {
    vi.mocked(listEvents)
      .mockResolvedValueOnce({
        events: [eventA],
        next_cursor: "cursor-page-2",
      })
      .mockRejectedValueOnce(new Error("next page failed"))
      .mockResolvedValueOnce({ events: [eventB] })
    const user = userEvent.setup()
    renderEventsPage({ event: eventA.id })

    await user.click(await screen.findByRole("button", { name: "Load more" }))
    await user.click(await screen.findByRole("button", { name: "Try again" }))

    await waitFor(() => {
      expect(listEvents).toHaveBeenCalledTimes(3)
    })
    expect(listEvents).toHaveBeenLastCalledWith(
      expect.objectContaining({ cursor: "cursor-page-2" }),
    )
  })

  it("applies URL-backed filters without retaining the old selection", async () => {
    const onSearchChange = vi.fn()
    const user = userEvent.setup()
    renderEventsPage({ event: eventA.id }, onSearchChange)

    const sourceInput = screen.getByLabelText("Source")
    await user.type(sourceInput, "github")
    await user.type(screen.getByLabelText("Connector"), "primary")
    await user.type(screen.getByLabelText("Event type"), "issues.opened")
    await user.click(screen.getByRole("button", { name: "Apply filters" }))

    expect(onSearchChange).toHaveBeenCalledWith(
      {
        source: "github",
        connector: "primary",
        type: "issues.opened",
      },
      true,
    )
  })

  it("keeps the replay warning open after a failed non-retried mutation", async () => {
    vi.mocked(replayEvent).mockRejectedValueOnce(
      new Error("Replay response was lost"),
    )
    const user = userEvent.setup()
    renderEventsPage({ event: eventA.id })

    await user.click(await screen.findByRole("button", { name: "Replay" }))
    const dialog = await screen.findByRole("alertdialog")
    expect(dialog).toHaveTextContent(eventA.id)
    expect(dialog).toHaveTextContent(/repeat workflows and external effects/i)

    await user.click(
      within(dialog).getByRole("button", { name: "Replay event" }),
    )

    expect(
      await within(dialog).findByText("Replay response was lost"),
    ).toBeInTheDocument()
    expect(screen.getByRole("alertdialog")).toBeInTheDocument()
    expect(replayEvent).toHaveBeenCalledTimes(1)
    expect(vi.mocked(replayEvent).mock.calls[0]?.[0]).toBe(eventA.id)
  })

  it("selects the newly created event after a successful replay", async () => {
    const onSearchChange = vi.fn()
    const user = userEvent.setup()
    renderEventsPage({ source: "github", event: eventA.id }, onSearchChange)

    await user.click(await screen.findByRole("button", { name: "Replay" }))
    await user.click(
      within(await screen.findByRole("alertdialog")).getByRole("button", {
        name: "Replay event",
      }),
    )

    await waitFor(() => {
      expect(onSearchChange).toHaveBeenCalledWith({
        source: "github",
        event: replayedEvent.id,
      })
    })
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument()
  })

  it("loads globally filtered dispatch pages and exact selected detail", async () => {
    vi.mocked(listEventDispatches)
      .mockResolvedValueOnce({
        dispatches: [dispatchA],
        next_cursor: "dispatch-page-2",
      })
      .mockResolvedValueOnce({ dispatches: [dispatchB] })
    const user = userEvent.setup()

    renderEventsPage({
      view: "dispatches",
      dispatch_event: eventA.id,
      workflow: dispatchA.workflow_ref,
      dispatch_status: "succeeded",
      dispatch: dispatchA.id,
    })

    await waitFor(() => {
      expect(listEventDispatches).toHaveBeenCalledWith({
        eventID: eventA.id,
        workflowRef: dispatchA.workflow_ref,
        status: "succeeded",
        limit: 40,
        cursor: undefined,
      })
      expect(getEventDispatch).toHaveBeenCalledWith(dispatchA.id)
    })
    expect(
      await screen.findByText(dispatchA.workflow_revision!),
    ).toBeInTheDocument()

    expect(screen.getByRole("link", { name: "Open event" })).toHaveAttribute(
      "href",
      `/events?event=${eventA.id}`,
    )
    expect(screen.getByRole("link", { name: "Open workflow" })).toHaveAttribute(
      "href",
      "/agent/workflows?mode=operate&workflow=workflows%2Fgithub-issue-triage.yml",
    )
    expect(screen.getByRole("link", { name: "Open run" })).toHaveAttribute(
      "href",
      "/agent/workflows?mode=operate&workflow=workflows%2Fgithub-issue-triage.yml&run=wr_event_1",
    )
    expect(
      screen.getByRole("link", { name: "Dispatch permalink" }),
    ).toHaveAttribute(
      "href",
      `/events?view=dispatches&dispatch=${dispatchA.id}`,
    )

    await user.click(screen.getByRole("button", { name: "Load more" }))
    await waitFor(() => {
      expect(listEventDispatches).toHaveBeenNthCalledWith(
        2,
        expect.objectContaining({ cursor: "dispatch-page-2" }),
      )
    })
    expect(
      screen.getByText("workflows/pull-request-close.yml"),
    ).toBeInTheDocument()
  })

  it("keeps exact dispatch detail selected outside the list filters", async () => {
    vi.mocked(listEventDispatches).mockResolvedValue({ dispatches: [] })
    const onSearchChange = vi.fn()

    renderEventsPage(
      {
        view: "dispatches",
        workflow: "workflows/no-match.yml",
        dispatch: dispatchA.id,
      },
      onSearchChange,
    )

    expect(
      await screen.findByText(dispatchA.workflow_revision!),
    ).toBeInTheDocument()
    expect(getEventDispatch).toHaveBeenCalledWith(dispatchA.id)
    expect(onSearchChange).not.toHaveBeenCalled()
  })

  it("preserves hidden URL state across view toggles and filter changes", async () => {
    const onSearchChange = vi.fn()
    const user = userEvent.setup()
    const search: EventsRouteSearch = {
      source: "github",
      connector: "primary",
      type: "issues.opened",
      routing_status: "succeeded",
      event: eventA.id,
      dispatch_event: eventB.id,
      workflow: dispatchB.workflow_ref,
      dispatch_status: "pending",
      dispatch: dispatchB.id,
    }
    renderEventsPage(search, onSearchChange)

    await user.click(screen.getByRole("tab", { name: "Dispatches" }))
    expect(onSearchChange).toHaveBeenCalledWith({
      ...search,
      view: "dispatches",
    })

    onSearchChange.mockClear()
    await user.clear(screen.getByLabelText("Source"))
    await user.type(screen.getByLabelText("Source"), "email")
    await user.click(screen.getByRole("button", { name: "Apply filters" }))
    expect(onSearchChange).toHaveBeenCalledWith(
      {
        source: "email",
        connector: "primary",
        type: "issues.opened",
        routing_status: "succeeded",
        dispatch_event: eventB.id,
        workflow: dispatchB.workflow_ref,
        dispatch_status: "pending",
        dispatch: dispatchB.id,
      },
      true,
    )
  })

  it("rehydrates each view from URL state during back-forward rerenders", async () => {
    const view = renderEventsPage({
      view: "dispatches",
      dispatch_event: eventB.id,
      workflow: dispatchB.workflow_ref,
      dispatch_status: "pending",
      dispatch: dispatchB.id,
    })

    expect(await screen.findByLabelText("Event ID")).toHaveValue(eventB.id)
    expect(screen.getByLabelText("Workflow")).toHaveValue(
      dispatchB.workflow_ref,
    )

    view.rerenderPage({
      source: "github",
      connector: "primary",
      type: "issues.opened",
      routing_status: "succeeded",
      event: eventA.id,
      dispatch_event: eventB.id,
      workflow: dispatchB.workflow_ref,
      dispatch_status: "pending",
      dispatch: dispatchB.id,
    })

    expect(await screen.findByLabelText("Source")).toHaveValue("github")
    expect(screen.getByLabelText("Connector")).toHaveValue("primary")
    expect(screen.getByLabelText("Event type")).toHaveValue("issues.opened")

    view.rerenderPage({
      view: "dispatches",
      dispatch_event: eventB.id,
      workflow: dispatchB.workflow_ref,
      dispatch_status: "pending",
      dispatch: dispatchB.id,
    })

    expect(await screen.findByLabelText("Event ID")).toHaveValue(eventB.id)
    expect(screen.getByLabelText("Workflow")).toHaveValue(
      dispatchB.workflow_ref,
    )
  })

  it("links event dispatch rows to exact dispatch URLs", async () => {
    renderEventsPage({ event: eventA.id })

    expect(
      await screen.findByRole("link", { name: "Open dispatch" }),
    ).toHaveAttribute(
      "href",
      `/events?view=dispatches&dispatch=${dispatchA.id}`,
    )
  })
})

function renderEventsPage(
  initialSearch: EventsRouteSearch,
  onSearchChange = vi.fn(),
) {
  const client = new QueryClient({
    defaultOptions: {
      queries: { retry: false },
      mutations: { retry: false },
    },
  })

  const page = (search: EventsRouteSearch): ReactNode => (
    <QueryClientProvider client={client}>
      <SidebarProvider>
        <EventsPage search={search} onSearchChange={onSearchChange} />
      </SidebarProvider>
    </QueryClientProvider>
  )

  const view = render(page(initialSearch))
  return {
    ...view,
    rerenderPage(search: EventsRouteSearch) {
      view.rerender(page(search))
    },
  }
}
