import { beforeEach, describe, expect, it, vi } from "vitest"

import {
  EventAPIError,
  getEvent,
  getEventDispatch,
  getEventPayload,
  listEventDispatches,
  listEvents,
  replayEvent,
} from "@/api/events"
import { launcherFetch } from "@/api/http"

vi.mock("@/api/http", () => ({
  launcherFetch: vi.fn(),
}))

const mockedLauncherFetch = vi.mocked(launcherFetch)

const event = {
  id: "ev_0123456789abcdef0123456789abcdef",
  source: "github",
  connector: "primary",
  type: "issues.opened",
  received_at: "2026-07-29T13:00:01Z",
  payload_bytes: 42,
  routing: {
    status: "pending",
    available_at: "2026-07-29T13:00:01Z",
    attempts: 0,
    updated_at: "2026-07-29T13:00:01Z",
  },
} as const

const replayedEvent = {
  ...event,
  id: "ev_fedcba9876543210fedcba9876543210",
  replay_of: event.id,
} as const

const dispatch = {
  id: "dsp_0123456789abcdef0123456789abcdef",
  event_id: event.id,
  workflow_ref: "workflows/triage.yml",
  workflow_revision:
    "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  run_id: "wr_dispatch",
  status: "succeeded",
  available_at: "2026-07-29T13:00:01Z",
  attempts: 1,
  created_at: "2026-07-29T13:00:02Z",
  updated_at: "2026-07-29T13:00:03Z",
  linked_at: "2026-07-29T13:00:02Z",
  finished_at: "2026-07-29T13:00:03Z",
} as const

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

describe("events API", () => {
  beforeEach(() => {
    mockedLauncherFetch.mockReset()
  })

  it("lists events with URL-encoded filters and cursor", async () => {
    const page = { events: [], next_cursor: "next" }
    mockedLauncherFetch.mockResolvedValue(jsonResponse(page))

    await expect(
      listEvents({
        source: "git hub",
        connector: "primary/slash",
        type: "issues.opened",
        routingStatus: "claimed",
        limit: 42,
        cursor: "v1+/=",
      }),
    ).resolves.toEqual(page)

    expect(mockedLauncherFetch).toHaveBeenCalledOnce()
    expect(mockedLauncherFetch).toHaveBeenCalledWith(
      "/api/events?source=git+hub&connector=primary%2Fslash&type=issues.opened&routing_status=claimed&limit=42&cursor=v1%2B%2F%3D",
      undefined,
    )
  })

  it("does not append an empty query string", async () => {
    mockedLauncherFetch.mockResolvedValue(jsonResponse({ events: [] }))

    await listEvents()

    expect(mockedLauncherFetch).toHaveBeenCalledWith("/api/events", undefined)
  })

  it("gets an event using an encoded identifier", async () => {
    mockedLauncherFetch.mockResolvedValue(jsonResponse(event))

    await expect(getEvent("ev_/unsafe")).resolves.toEqual(event)

    expect(mockedLauncherFetch).toHaveBeenCalledWith(
      "/api/events/ev_%2Funsafe",
      undefined,
    )
  })

  it("lists dispatches with URL-encoded filters and cursor", async () => {
    const page = { dispatches: [], next_cursor: "next" }
    mockedLauncherFetch.mockResolvedValue(jsonResponse(page))

    await expect(
      listEventDispatches({
        eventID: "ev_abc/123",
        workflowRef: "workflows/triage?branch=main",
        status: "running",
        limit: 25,
        cursor: "cursor+/=",
      }),
    ).resolves.toEqual(page)

    expect(mockedLauncherFetch).toHaveBeenCalledWith(
      "/api/events/dispatches?event_id=ev_abc%2F123&workflow_ref=workflows%2Ftriage%3Fbranch%3Dmain&status=running&limit=25&cursor=cursor%2B%2F%3D",
      undefined,
    )
  })

  it("gets an exact dispatch by identifier", async () => {
    mockedLauncherFetch.mockResolvedValue(jsonResponse(dispatch))

    await expect(getEventDispatch(dispatch.id)).resolves.toEqual(dispatch)

    expect(mockedLauncherFetch).toHaveBeenCalledWith(
      `/api/events/dispatches/${dispatch.id}`,
      undefined,
    )
  })

  it("returns payload text without parsing or re-encoding it", async () => {
    const payload =
      ' \n{"large":9007199254740993,"tiny":1e-1000,"order":["a","b"]}\t '
    mockedLauncherFetch.mockResolvedValue(
      new Response(payload, {
        headers: { "Content-Type": "application/json" },
      }),
    )

    await expect(getEventPayload("ev_payload")).resolves.toBe(payload)

    expect(mockedLauncherFetch).toHaveBeenCalledWith(
      "/api/events/ev_payload/payload",
      { signal: undefined },
    )
  })

  it("passes an abort signal to payload requests", async () => {
    const controller = new AbortController()
    mockedLauncherFetch.mockResolvedValue(new Response("{}"))

    await getEventPayload("ev_payload", controller.signal)

    expect(mockedLauncherFetch).toHaveBeenCalledWith(
      "/api/events/ev_payload/payload",
      { signal: controller.signal },
    )
  })

  it("replays once with exactly an empty JSON object body", async () => {
    const result = { event: replayedEvent }
    mockedLauncherFetch.mockResolvedValue(jsonResponse(result, 201))

    await expect(replayEvent(event.id)).resolves.toEqual(result)

    expect(mockedLauncherFetch).toHaveBeenCalledOnce()
    expect(mockedLauncherFetch).toHaveBeenCalledWith(
      `/api/events/${event.id}/replay`,
      {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: "{}",
      },
    )
  })

  it("does not retry a failed replay request", async () => {
    mockedLauncherFetch.mockRejectedValue(new TypeError("network unavailable"))

    await expect(replayEvent(event.id)).rejects.toThrow(
      "replay outcome unknown; inspect events before retrying",
    )

    expect(mockedLauncherFetch).toHaveBeenCalledOnce()
  })

  it("reports malformed read responses without crashing consumers", async () => {
    mockedLauncherFetch
      .mockResolvedValueOnce(jsonResponse({}))
      .mockResolvedValueOnce(jsonResponse({ ...event, routing: {} }))
      .mockResolvedValueOnce(jsonResponse({ ...event, id: "not-an-event-id" }))
      .mockResolvedValueOnce(jsonResponse({ dispatches: [{}] }))
      .mockResolvedValueOnce(
        jsonResponse({ ...dispatch, id: "not-a-dispatch-id" }),
      )
      .mockResolvedValueOnce(
        jsonResponse({
          ...dispatch,
          event_id: "ev_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
        }),
      )
      .mockResolvedValueOnce(
        jsonResponse({ ...dispatch, workflow_ref: " workflows/triage.yml" }),
      )
      .mockResolvedValueOnce(
        jsonResponse({ ...dispatch, run_id: "run/not-navigable" }),
      )
      .mockResolvedValueOnce(
        jsonResponse({
          ...dispatch,
          id: "dsp_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
        }),
      )

    await expect(listEvents()).rejects.toMatchObject({
      message: "The event service returned a malformed response.",
      status: 502,
    })
    await expect(getEvent(event.id)).rejects.toMatchObject({
      message: "The event service returned a malformed response.",
      status: 502,
    })
    await expect(getEvent(event.id)).rejects.toMatchObject({
      message: "The event service returned a malformed response.",
      status: 502,
    })
    await expect(
      listEventDispatches({ eventID: event.id }),
    ).rejects.toMatchObject({
      message: "The event service returned a malformed response.",
      status: 502,
    })
    await expect(getEventDispatch(dispatch.id)).rejects.toMatchObject({
      message: "The event service returned a malformed response.",
      status: 502,
    })
    await expect(getEventDispatch(dispatch.id)).rejects.toMatchObject({
      message: "The event service returned a malformed response.",
      status: 502,
    })
    await expect(getEventDispatch(dispatch.id)).rejects.toMatchObject({
      message: "The event service returned a malformed response.",
      status: 502,
    })
    await expect(getEventDispatch(dispatch.id)).rejects.toMatchObject({
      message: "The event service returned a malformed response.",
      status: 502,
    })
    await expect(getEventDispatch(dispatch.id)).rejects.toMatchObject({
      message: "The event service returned a malformed response.",
      status: 502,
    })
  })

  it("rejects oversized dispatch workflow fields", async () => {
    mockedLauncherFetch
      .mockResolvedValueOnce(
        jsonResponse({
          ...dispatch,
          workflow_ref: `workflows/${"é".repeat(508)}.yml`,
        }),
      )
      .mockResolvedValueOnce(
        jsonResponse({
          ...dispatch,
          workflow_revision: "r".repeat(257),
        }),
      )
      .mockResolvedValueOnce(
        jsonResponse({
          ...dispatch,
          run_id: `wr_${"r".repeat(254)}`,
        }),
      )

    await expect(getEventDispatch(dispatch.id)).rejects.toMatchObject({
      status: 502,
    })
    await expect(getEventDispatch(dispatch.id)).rejects.toMatchObject({
      status: 502,
    })
    await expect(getEventDispatch(dispatch.id)).rejects.toMatchObject({
      status: 502,
    })
  })

  it("treats malformed replay success as an unknown outcome", async () => {
    mockedLauncherFetch
      .mockResolvedValueOnce(jsonResponse({}, 201))
      .mockResolvedValueOnce(
        jsonResponse(
          {
            event: {
              ...replayedEvent,
              replay_of: "ev_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
            },
          },
          201,
        ),
      )

    await expect(replayEvent(event.id)).rejects.toMatchObject({
      message: "replay outcome unknown; inspect events before retrying",
      status: 0,
    })
    await expect(replayEvent(event.id)).rejects.toMatchObject({
      message: "replay outcome unknown; inspect events before retrying",
      status: 0,
    })
  })

  it("preserves definite replay validation errors", async () => {
    mockedLauncherFetch.mockResolvedValue(
      jsonResponse({ error: "event not found" }, 404),
    )

    await expect(replayEvent(event.id)).rejects.toMatchObject({
      message: "event not found",
      status: 404,
    })
  })

  it("treats unexpected replay HTTP failures as an unknown outcome", async () => {
    mockedLauncherFetch.mockResolvedValue(
      jsonResponse({ error: "upstream response failed" }, 502),
    )

    await expect(replayEvent(event.id)).rejects.toMatchObject({
      message: "replay outcome unknown; inspect events before retrying",
      status: 0,
    })
  })

  it("preserves a pre-dispatch unavailable replay response", async () => {
    mockedLauncherFetch.mockResolvedValue(
      jsonResponse({ error: "event gateway unavailable" }, 503),
    )

    await expect(replayEvent(event.id)).rejects.toMatchObject({
      message: "event gateway unavailable",
      status: 503,
    })
  })

  it("surfaces the launcher error and status", async () => {
    mockedLauncherFetch.mockResolvedValue(
      jsonResponse({ error: "event gateway unavailable" }, 503),
    )

    const error = await getEvent("ev_missing").catch((cause: unknown) => cause)

    expect(error).toBeInstanceOf(EventAPIError)
    expect(error).toMatchObject({
      message: "event gateway unavailable",
      status: 503,
    })
  })
})
