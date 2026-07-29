import { describe, expect, it } from "vitest"

import { eventsSearchIsCanonical, normalizeEventsSearch } from "@/routes/events"

const eventID = "ev_0123456789abcdef0123456789abcdef"
const dispatchID = "dsp_fedcba9876543210fedcba9876543210"

describe("events route search", () => {
  it("normalizes both visible and hidden view state", () => {
    expect(
      normalizeEventsSearch({
        view: "dispatches",
        source: " github ",
        connector: " primary ",
        type: " issues.opened ",
        routing_status: "claimed",
        event: eventID,
        dispatch_event: eventID,
        workflow: " workflows/triage.yml ",
        dispatch_status: "running",
        dispatch: dispatchID,
        cursor: "must-not-enter-url-state",
        payload: "must-not-enter-url-state",
        error: "must-not-enter-url-state",
      }),
    ).toEqual({
      view: "dispatches",
      source: "github",
      connector: "primary",
      type: "issues.opened",
      routing_status: "claimed",
      event: eventID,
      dispatch_event: eventID,
      workflow: "workflows/triage.yml",
      dispatch_status: "running",
      dispatch: dispatchID,
    })
  })

  it("omits the default events view and rejects malformed state", () => {
    expect(
      normalizeEventsSearch({
        view: "events",
        routing_status: "running",
        event: "ev_0123456789ABCDEF0123456789ABCDEF",
        dispatch_event: "ev_short",
        workflow: " ",
        dispatch_status: "unknown",
        dispatch: "dsp_0123456789ABCDEF0123456789ABCDEF",
      }),
    ).toEqual({})
  })

  it("bounds workflow references by UTF-8 bytes", () => {
    const exact = "é".repeat(512)
    expect(normalizeEventsSearch({ workflow: exact })).toEqual({
      workflow: exact,
    })
    expect(normalizeEventsSearch({ workflow: `${exact}a` })).toEqual({})
  })

  it("bounds event text filters by UTF-8 bytes", () => {
    expect(
      normalizeEventsSearch({
        source: "é".repeat(64),
        connector: "é".repeat(128),
        type: "é".repeat(128),
      }),
    ).toEqual({
      source: "é".repeat(64),
      connector: "é".repeat(128),
      type: "é".repeat(128),
    })
    expect(
      normalizeEventsSearch({
        source: "é".repeat(65),
        connector: "é".repeat(129),
        type: "é".repeat(129),
      }),
    ).toEqual({})
  })

  it("does not coerce repeated query values", () => {
    expect(
      normalizeEventsSearch({
        view: ["events", "dispatches"],
        event: [eventID],
        dispatch: [dispatchID],
        workflow: ["workflows/a.yml"],
      }),
    ).toEqual({})
  })

  it("detects raw sensitive and cursor fields that need canonical replacement", () => {
    expect(
      eventsSearchIsCanonical(
        {
          source: " github ",
          cursor: "opaque",
          payload: "never-route-this",
        },
        { source: "github" },
      ),
    ).toBe(false)
    expect(
      eventsSearchIsCanonical(
        { view: "dispatches", source: "github" },
        { view: "dispatches", source: "github" },
      ),
    ).toBe(true)
  })
})
