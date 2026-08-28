import { describe, expect, it } from "vitest"

import {
  exactDispatchHref,
  exactEventHref,
  workflowOperateHref,
  workflowRunHref,
} from "./event-links"

describe("event relationship links", () => {
  it("puts exact identities in dedicated URL fields", () => {
    expect(exactEventHref("ev_exact")).toBe("/events?event=ev_exact")
    expect(exactDispatchHref("dsp_exact")).toBe(
      "/events?view=dispatches&dispatch=dsp_exact",
    )
  })

  it("encodes workflow and run identities without carrying operational data", () => {
    const workflow = "workflows/a b.yml?branch=main"
    const workflowID = "a".repeat(43)
    const run = "wr_1/2"

    expect(workflowOperateHref(workflow)).toBeUndefined()
    expect(workflowOperateHref(workflow, workflowID)).toBe(
      `/agent/workflows/${workflowID}`,
    )
    const runURL = workflowRunHref(workflow, run)
    expect(runURL).toBe("/agent/workflows/runs/wr_1%2F2")
    expect(runURL).not.toMatch(/payload|error|cursor/)
  })
})
