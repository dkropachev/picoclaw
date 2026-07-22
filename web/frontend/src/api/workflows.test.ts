import { beforeEach, describe, expect, it, vi } from "vitest"

import { launcherFetch } from "@/api/http"
import {
  getWorkflowRun,
  getWorkflowRunEvents,
  getWorkflowRunGraph,
  listWorkflowRuns,
  listWorkflows,
  reloadWorkflows,
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
})

function jsonResponse(body: unknown) {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  })
}
