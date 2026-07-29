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
    const run = "wr_1/2"

    expect(workflowOperateHref(workflow)).toBe(
      "/agent/workflows?mode=operate&workflow=workflows%2Fa+b.yml%3Fbranch%3Dmain",
    )
    const runURL = workflowRunHref(workflow, run)
    expect(runURL).toBe(
      "/agent/workflows?mode=operate&workflow=workflows%2Fa+b.yml%3Fbranch%3Dmain&run=wr_1%2F2",
    )
    expect(runURL).not.toMatch(/payload|error|cursor/)
  })
})
