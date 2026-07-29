import { describe, expect, it } from "vitest"

import { trustedWorkflowRunOrigin } from "./workflow-run-origin"

const eventID = "ev_0123456789abcdef0123456789abcdef"
const dispatchID = "dsp_fedcba9876543210fedcba9876543210"

describe("trustedWorkflowRunOrigin", () => {
  it("projects only validated server-owned lifecycle identifiers", () => {
    expect(
      trustedWorkflowRunOrigin({
        kind: "external_event",
        event_id: eventID,
        dispatch_id: dispatchID,
        root_run_id: "wr_root-01",
        payload: { secret: "ignored" },
      }),
    ).toEqual({
      kind: "external_event",
      event_id: eventID,
      dispatch_id: dispatchID,
      root_run_id: "wr_root-01",
    })
    expect(
      trustedWorkflowRunOrigin({
        kind: "external_event_draft_test",
        event_id: eventID,
        root_run_id: "wr_draft",
      }),
    ).toEqual({
      kind: "external_event_draft_test",
      event_id: eventID,
      root_run_id: "wr_draft",
    })
  })

  it("rejects the entire origin when any supplied relationship is malformed", () => {
    expect(
      trustedWorkflowRunOrigin({
        kind: "external_event",
        event_id: eventID,
        root_run_id: "wr_root",
      }),
    ).toBeUndefined()
    expect(
      trustedWorkflowRunOrigin({
        kind: "external_event_draft_test",
        event_id: eventID,
        dispatch_id: dispatchID,
        root_run_id: "wr_root",
      }),
    ).toBeUndefined()
    expect(
      trustedWorkflowRunOrigin({
        kind: "external_event",
        event_id: eventID,
        dispatch_id: "dsp_invalid",
        root_run_id: "wr_root",
      }),
    ).toBeUndefined()
    expect(
      trustedWorkflowRunOrigin({
        kind: "external_event",
        event_id: "ev_invalid",
        root_run_id: "wr_root",
      }),
    ).toBeUndefined()
    expect(
      trustedWorkflowRunOrigin({
        kind: "external_event",
        event_id: eventID,
        root_run_id: "wr_invalid.dot",
      }),
    ).toBeUndefined()
    expect(
      trustedWorkflowRunOrigin({
        kind: "manual",
        event_id: eventID,
        root_run_id: "wr_root",
      }),
    ).toBeUndefined()
  })
})
