import { describe, expect, it } from "vitest"

import { normalizeEventsSearch } from "@/routes/events"

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
})
