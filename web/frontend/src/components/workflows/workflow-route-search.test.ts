import { describe, expect, it } from "vitest"

import {
  isWorkflowRunID,
  navigableWorkflowRef,
  normalizeWorkflowsSearch,
  trustedWorkflowEventID,
  workflowsSearchIsCanonical,
} from "./workflow-route-search"

describe("normalizeWorkflowsSearch", () => {
  it("accepts the exact event-dashboard workflow deep-link contract", () => {
    expect(
      normalizeWorkflowsSearch({
        mode: "operate",
        workflow: " workflows/github-issue-triage.yml ",
        run: "wr_01J7ABC_xyz-9",
        q: " failed ",
      }),
    ).toEqual({
      mode: "operate",
      workflow: "workflows/github-issue-triage.yml",
      run: "wr_01J7ABC_xyz-9",
      q: "failed",
    })
  })

  it("defaults to Develop by omitting invalid and blank values", () => {
    expect(
      normalizeWorkflowsSearch({
        mode: "other",
        workflow: " ",
        run: "wr_bad.id",
        q: "\n\t",
        inputs: '{"secret":"never-route-this"}',
        error: "never-route-this",
      }),
    ).toEqual({})
    expect(normalizeWorkflowsSearch({ mode: "develop" })).toEqual({})
    expect(
      workflowsSearchIsCanonical(
        { mode: "invalid", payload: "never-route-this" },
        {},
      ),
    ).toBe(false)
    expect(
      workflowsSearchIsCanonical(
        { mode: "operate", run: "wr_valid" },
        { mode: "operate", run: "wr_valid" },
      ),
    ).toBe(true)
  })

  it("enforces byte and character bounds", () => {
    const maximumRun = `wr_${"r".repeat(253)}`
    expect(
      normalizeWorkflowsSearch({
        workflow: "é".repeat(512),
        run: maximumRun,
        q: "🙂".repeat(256),
      }),
    ).toEqual({
      workflow: "é".repeat(512),
      run: maximumRun,
      q: "🙂".repeat(256),
    })

    expect(
      normalizeWorkflowsSearch({
        workflow: "é".repeat(513),
        run: `${maximumRun}r`,
        q: "🙂".repeat(257),
      }),
    ).toEqual({})
  })
})

describe("workflow relationship validation", () => {
  const eventID = "ev_0123456789abcdef0123456789abcdef"

  it("links only bounded run IDs and published workflow refs", () => {
    expect(isWorkflowRunID("wr_run_01-example")).toBe(true)
    expect(isWorkflowRunID("wr_run.01")).toBe(false)
    expect(navigableWorkflowRef(" workflows/triage.yml ")).toBe(
      "workflows/triage.yml",
    )
    expect(navigableWorkflowRef("draft:workflows/triage.yml")).toBeUndefined()
  })

  it("accepts only a complete validated server event context", () => {
    expect(
      trustedWorkflowEventID({
        id: eventID,
        source: "github",
        connector: "primary",
        type: "issues.opened",
      }),
    ).toBe(eventID)

    expect(
      trustedWorkflowEventID({
        id: eventID,
        source: "github",
        type: "issues.opened",
      }),
    ).toBeUndefined()
    expect(
      trustedWorkflowEventID({
        event_id: eventID,
        dispatch_id: "dsp_0123456789abcdef0123456789abcdef",
      }),
    ).toBeUndefined()
  })
})
