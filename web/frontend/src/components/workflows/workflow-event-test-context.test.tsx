import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import type { ReactNode } from "react"
import { beforeEach, describe, expect, it, vi } from "vitest"

import type { EventView } from "@/api/events"
import { getEventPayload, listEvents } from "@/api/events"
import { matchWorkflowEventTrigger } from "@/api/workflows"
import { WorkflowEventTestContext } from "@/components/workflows/workflow-event-test-context"

vi.mock("@/api/events", () => ({
  getEventPayload: vi.fn(),
  listEvents: vi.fn(),
}))

vi.mock("@/api/workflows", () => ({
  matchWorkflowEventTrigger: vi.fn(),
}))

const event: EventView = {
  id: "ev_11111111111111111111111111111111",
  source: "github",
  connector: "primary",
  type: "issues.opened",
  received_at: "2026-07-29T12:00:00Z",
  payload_bytes: 48,
  routing: {
    status: "succeeded",
    available_at: "2026-07-29T12:00:00Z",
    attempts: 1,
    updated_at: "2026-07-29T12:00:01Z",
  },
}

describe("WorkflowEventTestContext", () => {
  beforeEach(() => {
    vi.mocked(listEvents).mockReset()
    vi.mocked(getEventPayload).mockReset()
    vi.mocked(matchWorkflowEventTrigger).mockReset()
    vi.mocked(listEvents).mockResolvedValue({ events: [event] })
    vi.mocked(getEventPayload).mockResolvedValue(
      '{"issue":{"title":"redacted payload"}}',
    )
  })

  it("loads metadata, gates on the matched contract, and keeps payload opt-in", async () => {
    const user = userEvent.setup()
    const onMatchStateChange = vi.fn()
    vi.mocked(matchWorkflowEventTrigger).mockResolvedValue({
      event_id: event.id,
      matched: true,
      checks: [
        {
          path: "on.event.types",
          present: true,
          value: "issues.opened",
          matched: true,
        },
        {
          path: "on.event.attributes.repository",
          present: false,
          matched: false,
        },
      ],
    })

    renderContext({ onMatchStateChange })
    await screen.findByRole("option", {
      name: "github · primary · issues.opened · 11111111",
    })
    await user.selectOptions(
      screen.getByLabelText("Draft test event"),
      event.id,
    )

    await waitFor(() =>
      expect(matchWorkflowEventTrigger).toHaveBeenCalledWith(
        { yaml: "name: Event workflow", event_id: event.id },
        expect.any(AbortSignal),
      ),
    )
    await waitFor(() =>
      expect(onMatchStateChange).toHaveBeenLastCalledWith({
        eventID: event.id,
        status: "matched",
        message: expect.stringContaining("matches"),
      }),
    )
    expect(screen.getByText("missing from selected event")).toBeInTheDocument()
    expect(getEventPayload).not.toHaveBeenCalled()

    await user.click(screen.getByRole("button", { name: /payload/i }))
    expect(
      await screen.findByText('{"issue":{"title":"redacted payload"}}'),
    ).toBeInTheDocument()
    expect(getEventPayload).toHaveBeenCalledWith(
      event.id,
      expect.any(AbortSignal),
    )
  })

  it("restores and rechecks a prior event even when it is outside the recent page", async () => {
    const storedEventID = "ev_22222222222222222222222222222222"
    vi.mocked(listEvents).mockResolvedValue({ events: [] })
    vi.mocked(matchWorkflowEventTrigger).mockResolvedValue({
      event_id: storedEventID,
      matched: false,
      checks: [],
    })

    renderContext({ initialEventID: storedEventID })

    expect(
      await screen.findByRole("option", {
        name: `Stored event · ${storedEventID}`,
      }),
    ).toBeInTheDocument()
    await waitFor(() =>
      expect(matchWorkflowEventTrigger).toHaveBeenCalledWith(
        { yaml: "name: Event workflow", event_id: storedEventID },
        expect.any(AbortSignal),
      ),
    )
    expect(
      await screen.findByText(
        "The selected event does not match every populated trigger filter.",
      ),
    ).toBeInTheDocument()
    expect(getEventPayload).not.toHaveBeenCalled()
  })
})

function renderContext({
  initialEventID,
  onMatchStateChange = vi.fn(),
}: {
  initialEventID?: string
  onMatchStateChange?: Parameters<
    typeof WorkflowEventTestContext
  >[0]["onMatchStateChange"]
} = {}) {
  const client = new QueryClient({
    defaultOptions: {
      queries: { retry: false },
      mutations: { retry: false },
    },
  })
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  )
  return render(
    <WorkflowEventTestContext
      yaml="name: Event workflow"
      disabled={false}
      initialEventID={initialEventID}
      onMatchStateChange={onMatchStateChange}
    />,
    { wrapper },
  )
}
