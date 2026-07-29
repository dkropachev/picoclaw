import { beforeEach, describe, expect, it, vi } from "vitest"

import { launcherFetch } from "@/api/http"
import {
  getWorkflowRun,
  getWorkflowRunEvents,
  getWorkflowRunGraph,
  inspectWorkflowEventTrigger,
  listWorkflowRuns,
  listWorkflows,
  matchWorkflowEventTrigger,
  reloadWorkflows,
  renderWorkflowEventTrigger,
  testWorkflowDevelopment,
} from "@/api/workflows"

vi.mock("@/api/http", () => ({
  launcherFetch: vi.fn(),
}))

const mockedLauncherFetch = vi.mocked(launcherFetch)

describe("workflow API normalization", () => {
  beforeEach(() => {
    mockedLauncherFetch.mockReset()
  })

  it("normalizes nullable workflow list payloads", async () => {
    mockedLauncherFetch.mockResolvedValueOnce(
      jsonResponse({
        workflows: null,
        compatibility: {
          current: {
            picoclaw_version: "test",
            workflow_engine_version: "1",
            workflow_schema_version: "1",
            validator_fingerprint: "test",
          },
          workflows: null,
          counts: null,
          version_changed: false,
          manifest_missing: false,
          has_blocking: false,
        },
      }),
    )

    await expect(listWorkflows()).resolves.toMatchObject({
      workflows: [],
      compatibility: {
        workflows: [],
        counts: {},
      },
    })
  })

  it("keeps workflow call contracts in workflow list payloads", async () => {
    mockedLauncherFetch.mockResolvedValueOnce(
      jsonResponse({
        workflows: [
          {
            ref: "workflows/review.yml",
            event_trigger: {
              sources: ["github"],
              types: ["pull_request.*"],
            },
            workflow_call: {
              inputs: {
                action: {
                  type: "string",
                  required: true,
                  default: "plan",
                },
                dry_run: {
                  type: "boolean",
                  default: true,
                },
              },
              secrets: {
                token: {
                  required: true,
                },
              },
            },
          },
        ],
      }),
    )

    await expect(listWorkflows()).resolves.toMatchObject({
      workflows: [
        {
          ref: "workflows/review.yml",
          event_trigger: {
            sources: ["github"],
            types: ["pull_request.*"],
          },
          workflow_call: {
            inputs: {
              action: {
                type: "string",
                required: true,
                default: "plan",
              },
              dry_run: {
                type: "boolean",
                default: true,
              },
            },
            secrets: {
              token: {
                required: true,
              },
            },
          },
        },
      ],
    })
  })

  it("normalizes nullable workflow run payloads", async () => {
    mockedLauncherFetch.mockResolvedValueOnce(jsonResponse({ runs: null }))
    await expect(listWorkflowRuns()).resolves.toEqual({ runs: [] })

    mockedLauncherFetch.mockResolvedValueOnce(
      jsonResponse({
        id: "wr_nulls",
        workflow_ref: "workflows/nulls.yml",
        status: "succeeded",
        child_run_ids: null,
        jobs: null,
        steps: null,
        created_at: "2026-07-16T12:00:00Z",
        updated_at: "2026-07-16T12:00:01Z",
      }),
    )

    await expect(getWorkflowRun("wr_nulls")).resolves.toMatchObject({
      child_run_ids: [],
      jobs: {},
      steps: {},
    })
  })

  it("normalizes nullable workflow detail arrays", async () => {
    mockedLauncherFetch.mockResolvedValueOnce(
      jsonResponse({ run_id: "wr_nulls", events: null }),
    )
    await expect(getWorkflowRunEvents("wr_nulls")).resolves.toEqual({
      run_id: "wr_nulls",
      events: [],
    })

    mockedLauncherFetch.mockResolvedValueOnce(
      jsonResponse({ run_id: "wr_nulls", nodes: null, edges: null }),
    )
    await expect(getWorkflowRunGraph("wr_nulls")).resolves.toMatchObject({
      nodes: [],
      edges: [],
    })
  })

  it("normalizes nullable reload payloads", async () => {
    mockedLauncherFetch.mockResolvedValueOnce(
      jsonResponse({
        reloaded_at: "2026-07-16T12:00:00Z",
        workflows: null,
        errors: null,
      }),
    )

    await expect(reloadWorkflows()).resolves.toEqual({
      reloaded_at: "2026-07-16T12:00:00Z",
      workflows: [],
      errors: [],
    })
  })

  it("inspects and renders an event trigger without mutating the draft", async () => {
    const yaml = "name: Event workflow\non:\n  event:\n    types: issues.*\n"
    const eventTrigger = {
      sources: ["github"],
      types: ["issues.*"],
    }
    mockedLauncherFetch
      .mockResolvedValueOnce(
        jsonResponse({
          revision: "rev-1",
          editable: true,
          event_trigger: { types: ["issues.*"] },
          validation: { valid: true, validated_at: "2026-07-29T12:00:00Z" },
        }),
      )
      .mockResolvedValueOnce(
        jsonResponse({
          yaml: "name: Event workflow\non:\n  event:\n    sources: github\n    types: issues.*\n",
          revision: "rev-2",
          editable: true,
          event_trigger: eventTrigger,
          validation: { valid: true, validated_at: "2026-07-29T12:00:01Z" },
        }),
      )

    await expect(inspectWorkflowEventTrigger(yaml)).resolves.toMatchObject({
      revision: "rev-1",
      editable: true,
      event_trigger: { types: ["issues.*"] },
    })
    await expect(
      renderWorkflowEventTrigger({
        yaml,
        revision: "rev-1",
        event_trigger: eventTrigger,
      }),
    ).resolves.toMatchObject({
      revision: "rev-2",
      event_trigger: eventTrigger,
    })

    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      1,
      "/api/workflows/development/event-trigger/inspect",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({ yaml }),
      }),
    )
    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      2,
      "/api/workflows/development/event-trigger/render",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({
          yaml,
          revision: "rev-1",
          event_trigger: eventTrigger,
        }),
      }),
    )
  })

  it("matches a selected event using only YAML and event identity", async () => {
    const yaml = "on:\n  event:\n    types: issues.*\n"
    const eventID = "ev_11111111111111111111111111111111"
    mockedLauncherFetch.mockResolvedValueOnce(
      jsonResponse({
        event_id: eventID,
        matched: true,
        checks: [
          {
            path: "on.event.types",
            present: true,
            value: "issues.opened",
            matched: true,
          },
        ],
        validation: { valid: true, validated_at: "2026-07-29T12:00:00Z" },
      }),
    )

    await expect(
      matchWorkflowEventTrigger({ yaml, event_id: eventID }),
    ).resolves.toMatchObject({
      event_id: eventID,
      matched: true,
    })
    expect(mockedLauncherFetch).toHaveBeenCalledWith(
      "/api/workflows/development/event-trigger/match",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({ yaml, event_id: eventID }),
      }),
    )
    expect(mockedLauncherFetch.mock.calls[0]?.[1]?.body).not.toContain(
      "payload",
    )
  })

  it("sends event-parity draft tests by event id without manual context", async () => {
    const eventID = "ev_11111111111111111111111111111111"
    mockedLauncherFetch.mockResolvedValueOnce(
      jsonResponse({
        session: {
          id: "dev-event",
          reason: "new",
          status: "testing",
          target_workflow_ref: "workflows/event.yml",
          yaml: "on:\n  event:\n    types: issues.*\n",
          created_at: "2026-07-29T12:00:00Z",
          updated_at: "2026-07-29T12:00:00Z",
        },
        result: { run_id: "wr-event", status: "running" },
      }),
    )

    await testWorkflowDevelopment({
      yaml: "on:\n  event:\n    types: issues.*\n",
      event_id: eventID,
      async: true,
    })

    const body = JSON.parse(
      String(mockedLauncherFetch.mock.calls[0]?.[1]?.body),
    ) as Record<string, unknown>
    expect(body).toEqual({
      yaml: "on:\n  event:\n    types: issues.*\n",
      event_id: eventID,
      async: true,
    })
    expect(body).not.toHaveProperty("event")
    expect(body).not.toHaveProperty("inputs")
    expect(body).not.toHaveProperty("secrets")
    expect(body).not.toHaveProperty("session")
    expect(body).not.toHaveProperty("delivery")
  })
})

function jsonResponse(body: unknown) {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  })
}
