import { beforeEach, describe, expect, it, vi } from "vitest"

import { launcherFetch } from "@/api/http"
import {
  WorkflowAPIError,
  type WorkflowTriggerSimulationRequest,
  aiReviseWorkflowDevelopment,
  cancelWorkflowRun,
  checkWorkflowDependencies,
  discardWorkflowDevelopment,
  executeWorkflowDevelopmentTrigger,
  getWorkflowDefinition,
  getWorkflowDevelopment,
  getWorkflowRun,
  getWorkflowRunEvents,
  getWorkflowRunGraph,
  getWorkflowSettings,
  inspectPublishedWorkflowDefinition,
  inspectWorkflowEventTrigger,
  inspectWorkflowTemplate,
  inspectWorkflowTriggers,
  installWorkflowTemplate,
  listWorkflowDefinitions,
  listWorkflowRuns,
  listWorkflowTemplates,
  listWorkflows,
  matchWorkflowEventTrigger,
  patchWorkflowSettings,
  publishWorkflowDevelopment,
  reloadWorkflows,
  renderWorkflowEventTrigger,
  renderWorkflowTrigger,
  retryWorkflowRun,
  reviseWorkflowDevelopment,
  runWorkflow,
  simulateWorkflowDevelopmentTrigger,
  testWorkflowDevelopment,
  validateWorkflowDevelopment,
  workflowTriggerSimulationRequestBody,
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

  it("loads paged workflow definitions by opaque ID", async () => {
    const id = "a".repeat(43)
    const workflow = {
      id,
      ref: "workflows/review.yml",
      name: "Review",
      status: "valid",
      trigger: "workflow_call",
      inputs: 1,
      secrets: 1,
      workflow_call: { inputs: {}, secrets: {} },
    }
    mockedLauncherFetch
      .mockResolvedValueOnce(
        jsonResponse({
          workflows: [workflow],
          total: 1,
          next_cursor: "next",
          canonical_query: 'status = "valid" ORDER BY ref ASC',
          query_schema: { fields: [] },
        }),
      )
      .mockResolvedValueOnce(jsonResponse({ workflow }))

    await expect(
      listWorkflowDefinitions({
        query: "status = valid ORDER BY ref ASC",
        cursor: "cursor",
        limit: 25,
      }),
    ).resolves.toMatchObject({ workflows: [workflow], total: 1 })
    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      1,
      "/api/workflows/definitions?query=status+%3D+valid+ORDER+BY+ref+ASC&cursor=cursor&limit=25",
      undefined,
    )
    await expect(getWorkflowDefinition(id)).resolves.toEqual(workflow)
    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      2,
      `/api/workflows/definitions/${id}`,
      undefined,
    )
  })

  it("rejects malformed and duplicate workflow collection identities", async () => {
    const workflow = {
      id: "a".repeat(43),
      ref: "workflows/review.yml",
      status: "valid",
      trigger: "manual",
      inputs: 0,
      secrets: 0,
    }
    mockedLauncherFetch.mockResolvedValueOnce(
      jsonResponse({
        workflows: [workflow, workflow],
        total: 2,
        canonical_query: "ORDER BY ref ASC",
        query_schema: { fields: [] },
      }),
    )
    await expect(listWorkflowDefinitions()).rejects.toMatchObject({
      status: 502,
    })
  })

  it("normalizes nullable workflow run payloads", async () => {
    mockedLauncherFetch.mockResolvedValueOnce(
      jsonResponse({
        runs: null,
        total: 0,
        canonical_query: "ORDER BY created DESC",
        query_schema: { fields: [] },
      }),
    )
    await expect(listWorkflowRuns()).resolves.toMatchObject({
      runs: [],
      total: 0,
      canonical_query: "ORDER BY created DESC",
    })

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

  it("drops campaign authority from workflow-run payloads", async () => {
    mockedLauncherFetch.mockResolvedValueOnce(
      jsonResponse({
        id: "wr_campaign_private",
        workflow_ref: "workflows/repository-bug-finder.yml",
        status: "succeeded",
        inputs: {
          campaign_id: "rrc_frontend_canary",
          campaign_recovery_pending: true,
        },
        steps: {
          record: {
            outputs: {
              run: { campaign_id: "rrc_frontend_canary" },
            },
          },
        },
        created_at: "2026-07-16T12:00:00Z",
        updated_at: "2026-07-16T12:00:01Z",
      }),
    )
    const run = await getWorkflowRun("wr_campaign_private")
    expect(JSON.stringify(run)).not.toContain("rrc_frontend_canary")
    expect(JSON.stringify(run)).not.toContain("campaign_id")
  })

  it("keeps workflow run collection rows concise", async () => {
    mockedLauncherFetch.mockResolvedValueOnce(
      jsonResponse({
        runs: [
          {
            id: "wr_summary",
            workflow_id: "a".repeat(43),
            workflow_ref: "workflows/summary.yml",
            status: "failed",
            session: "workflow:summary",
            origin: {
              kind: "external_event",
              event_id: "ev_summary",
              root_run_id: "wr_summary",
            },
            created_at: "2026-07-16T12:00:00Z",
            updated_at: "2026-07-16T12:00:01Z",
            completed_at: "2026-07-16T12:00:01Z",
            delivery: { channel: "private" },
            event: { private: true },
            inputs: { private: true },
            outputs: { private: true },
            jobs: { private: {} },
            steps: { private: {} },
            error: "private diagnostic",
            cancel_reason: "private reason",
          },
        ],
        total: 1,
        canonical_query: "ORDER BY created DESC",
        query_schema: { fields: [] },
      }),
    )

    const page = await listWorkflowRuns()
    expect(page.runs).toEqual([
      {
        id: "wr_summary",
        workflow_id: "a".repeat(43),
        workflow_ref: "workflows/summary.yml",
        status: "failed",
        session: "workflow:summary",
        origin: {
          kind: "external_event",
          event_id: "ev_summary",
          root_run_id: "wr_summary",
        },
        created_at: "2026-07-16T12:00:00Z",
        updated_at: "2026-07-16T12:00:01Z",
        completed_at: "2026-07-16T12:00:01Z",
      },
    ])
  })

  it("sends exact draft fences for save, validation, and AI revision", async () => {
    const fence = {
      session_id: "dev_exact",
      expected_session_revision: "sha256:session-exact",
      expected_draft_revision: "sha256:draft-exact",
    }
    mockedLauncherFetch
      .mockResolvedValueOnce(jsonResponse({ session: { id: "dev_exact" } }))
      .mockResolvedValueOnce(jsonResponse({ session: { id: "dev_exact" } }))
      .mockResolvedValueOnce(jsonResponse({ session: { id: "dev_exact" } }))

    await reviseWorkflowDevelopment({
      ...fence,
      prompt: "save exact",
      yaml: "name: Exact\n",
    })
    await validateWorkflowDevelopment(fence)
    await aiReviseWorkflowDevelopment({ ...fence, prompt: "AI exact" })

    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      1,
      "/api/workflows/development/revise",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({
          ...fence,
          prompt: "save exact",
          yaml: "name: Exact\n",
        }),
      }),
    )
    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      2,
      "/api/workflows/development/validate",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify(fence),
      }),
    )
    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      3,
      "/api/workflows/development/ai-revise",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({ ...fence, prompt: "AI exact" }),
      }),
    )
  })

  it("exposes only bounded stable structured workflow error codes", async () => {
    mockedLauncherFetch
      .mockResolvedValueOnce(
        jsonResponse(
          {
            code: "workflow_development_fence_mismatch",
            message: "Workflow development session changed",
          },
          409,
        ),
      )
      .mockResolvedValueOnce(
        jsonResponse(
          { code: "INVALID CODE WITH SPACES", message: "Invalid response" },
          409,
        ),
      )
    const fence = {
      session_id: "dev_stale",
      expected_session_revision: "stale-session",
      expected_draft_revision: "stale-draft",
    }

    await expect(validateWorkflowDevelopment(fence)).rejects.toMatchObject({
      status: 409,
      code: "workflow_development_fence_mismatch",
      message: "Workflow development session changed",
    })
    await expect(validateWorkflowDevelopment(fence)).rejects.toMatchObject({
      status: 409,
      code: undefined,
      message: "Invalid response",
    })
  })

  it("submits exact dependency revisions for run and retry", async () => {
    mockedLauncherFetch
      .mockResolvedValueOnce(
        jsonResponse({ run_id: "wr_started", status: "running" }, 202),
      )
      .mockResolvedValueOnce(
        jsonResponse({ run_id: "wr_retried", status: "running" }, 202),
      )

    await expect(
      runWorkflow({
        ref: "workflows/review.yml",
        expected_dependency_revision: "opaque:run/revision?exact",
        inputs: { issue: 42 },
        async: true,
      }),
    ).resolves.toMatchObject({
      result: { run_id: "wr_started", status: "running" },
    })
    await expect(
      retryWorkflowRun("wr_original", {
        expected_dependency_revision: "opaque:retry/revision?exact",
        secrets: { token: "secret" },
      }),
    ).resolves.toMatchObject({
      result: { run_id: "wr_retried", status: "running" },
    })

    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      1,
      "/api/workflows/run",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({
          ref: "workflows/review.yml",
          expected_dependency_revision: "opaque:run/revision?exact",
          inputs: { issue: 42 },
          async: true,
        }),
      }),
    )
    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      2,
      "/api/workflows/runs/wr_original/retry",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({
          expected_dependency_revision: "opaque:retry/revision?exact",
          secrets: { token: "secret" },
        }),
      }),
    )
  })

  it("preserves launch error status with bounded dependency guidance", async () => {
    mockedLauncherFetch
      .mockResolvedValueOnce(
        jsonResponse({ error: "dependency_revision_mismatch" }, 409),
      )
      .mockResolvedValueOnce(
        jsonResponse({ error: "dependency_check_unavailable" }, 503),
      )

    await expect(
      runWorkflow({
        ref: "workflows/review.yml",
        expected_dependency_revision: "stale",
      }),
    ).rejects.toEqual(
      expect.objectContaining<Partial<WorkflowAPIError>>({
        status: 409,
        message:
          "Workflow dependencies changed. Wait for a fresh readiness check and try again.",
      }),
    )
    await expect(
      retryWorkflowRun("wr_original", {
        expected_dependency_revision: "current",
      }),
    ).rejects.toEqual(
      expect.objectContaining<Partial<WorkflowAPIError>>({
        status: 503,
        message: "Workflow dependency readiness is temporarily unavailable.",
      }),
    )
  })

  it("trims and validates explicit cancel reasons before sending", async () => {
    mockedLauncherFetch.mockResolvedValueOnce(
      jsonResponse({
        id: "wr_running",
        workflow_ref: "workflows/review.yml",
        status: "canceled",
        cancel_reason: "operator intervention",
        cancel_requested_at: "2026-07-29T12:00:01Z",
        completed_at: "2026-07-29T12:00:01Z",
        created_at: "2026-07-29T12:00:00Z",
        updated_at: "2026-07-29T12:00:01Z",
      }),
    )

    await expect(
      cancelWorkflowRun("wr_running", "  operator intervention  "),
    ).resolves.toMatchObject({
      id: "wr_running",
      status: "canceled",
      cancel_reason: "operator intervention",
      child_run_ids: [],
    })
    expect(mockedLauncherFetch).toHaveBeenCalledWith(
      "/api/workflows/runs/wr_running/cancel",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({ reason: "operator intervention" }),
      }),
    )

    await expect(cancelWorkflowRun("wr_running", " \n ")).rejects.toMatchObject(
      { status: 400 },
    )
    await expect(
      cancelWorkflowRun("wr_running", ` ${"é".repeat(513)} `),
    ).rejects.toMatchObject({ status: 400 })
    expect(mockedLauncherFetch).toHaveBeenCalledTimes(1)
  })

  it("sends an exact discard fence and surfaces structured conflicts", async () => {
    mockedLauncherFetch.mockResolvedValueOnce(
      jsonResponse(
        {
          code: "workflow_development_conflict",
          message: "Workflow development changed elsewhere.",
        },
        409,
      ),
    )
    await expect(
      discardWorkflowDevelopment({
        session_id: "dev_1",
        expected_session_revision: "session-1",
      }),
    ).rejects.toMatchObject({
      status: 409,
      message: "Workflow development changed elsewhere.",
    })
    expect(mockedLauncherFetch).toHaveBeenCalledWith(
      "/api/workflows/development/discard",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({
          session_id: "dev_1",
          expected_session_revision: "session-1",
        }),
      }),
    )
  })

  it("keeps exact run lookup status and identity authoritative", async () => {
    mockedLauncherFetch
      .mockResolvedValueOnce(
        jsonResponse({ error: "workflow run not found" }, 404),
      )
      .mockResolvedValueOnce(
        jsonResponse({
          id: "wr_other",
          workflow_ref: "workflows/other.yml",
          status: "succeeded",
          created_at: "2026-07-16T12:00:00Z",
          updated_at: "2026-07-16T12:00:01Z",
        }),
      )

    await expect(getWorkflowRun("wr_exact")).rejects.toEqual(
      expect.objectContaining<Partial<WorkflowAPIError>>({
        name: "WorkflowAPIError",
        message: "workflow run not found",
        status: 404,
      }),
    )
    await expect(getWorkflowRun("wr_exact")).rejects.toEqual(
      expect.objectContaining<Partial<WorkflowAPIError>>({
        name: "WorkflowAPIError",
        message: "The workflow service returned a mismatched run.",
        status: 502,
      }),
    )
    await expect(getWorkflowRun("not-a-run")).rejects.toMatchObject({
      status: 400,
    })
    expect(mockedLauncherFetch).toHaveBeenCalledTimes(2)
  })

  it("accepts 1024-byte run IDs for load, cancel, and retry", async () => {
    const maximumRunID = `wr_${"r".repeat(1021)}`
    const oversizedRunID = `${maximumRunID}r`
    const run = {
      id: maximumRunID,
      workflow_ref: "workflows/long-run-id.yml",
      status: "canceled",
      created_at: "2026-07-29T12:00:00Z",
      updated_at: "2026-07-29T12:00:01Z",
    }

    mockedLauncherFetch
      .mockResolvedValueOnce(jsonResponse(run))
      .mockResolvedValueOnce(jsonResponse(run))
      .mockResolvedValueOnce(
        jsonResponse({ run_id: "wr_retried", status: "running" }, 202),
      )

    await expect(getWorkflowRun(maximumRunID)).resolves.toMatchObject({
      id: maximumRunID,
    })
    await expect(
      cancelWorkflowRun(maximumRunID, "operator intervention"),
    ).resolves.toMatchObject({ id: maximumRunID })
    await expect(
      retryWorkflowRun(maximumRunID, {
        expected_dependency_revision: "opaque:current",
      }),
    ).resolves.toMatchObject({
      result: { run_id: "wr_retried", status: "running" },
    })

    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      1,
      `/api/workflows/runs/${maximumRunID}`,
      undefined,
    )
    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      2,
      `/api/workflows/runs/${maximumRunID}/cancel`,
      expect.objectContaining({ method: "POST" }),
    )
    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      3,
      `/api/workflows/runs/${maximumRunID}/retry`,
      expect.objectContaining({ method: "POST" }),
    )

    await expect(getWorkflowRun(oversizedRunID)).rejects.toMatchObject({
      status: 400,
    })
    await expect(
      cancelWorkflowRun(oversizedRunID, "operator intervention"),
    ).rejects.toMatchObject({ status: 400 })
    await expect(
      retryWorkflowRun(oversizedRunID, {
        expected_dependency_revision: "opaque:current",
      }),
    ).rejects.toMatchObject({ status: 400 })
    expect(mockedLauncherFetch).toHaveBeenCalledTimes(3)
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

  it("lists and installs built-in workflow templates with encoded names", async () => {
    mockedLauncherFetch
      .mockResolvedValueOnce(jsonResponse({ templates: null }))
      .mockResolvedValueOnce(
        jsonResponse({
          result: {
            name: "review/template",
            ref: "workflows/review.yml",
            state: "installed",
            installed: true,
            revalidated: true,
          },
          templates: [
            {
              name: "review/template",
              ref: "workflows/review.yml",
              state: "installed",
            },
          ],
        }),
      )

    await expect(listWorkflowTemplates()).resolves.toEqual({ templates: [] })
    await expect(
      installWorkflowTemplate("review/template", false),
    ).resolves.toMatchObject({
      result: {
        name: "review/template",
        installed: true,
        revalidated: true,
      },
      templates: [{ name: "review/template", state: "installed" }],
    })
    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      1,
      "/api/workflows/templates",
      undefined,
    )
    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      2,
      "/api/workflows/templates/review%2Ftemplate/install",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({ overwrite: false }),
      }),
    )
  })

  it("strictly inspects published definitions and encoded templates", async () => {
    mockedLauncherFetch
      .mockResolvedValueOnce(
        jsonResponse(
          definitionInspection({
            source: { kind: "published", ref: "workflows/review.yml" },
          }),
        ),
      )
      .mockResolvedValueOnce(
        jsonResponse(
          definitionInspection({
            source: {
              kind: "template",
              template_name: "review/template",
            },
          }),
        ),
      )

    const controller = new AbortController()
    await expect(
      inspectPublishedWorkflowDefinition(
        "workflows/review.yml",
        controller.signal,
      ),
    ).resolves.toMatchObject({
      source: { kind: "published", ref: "workflows/review.yml" },
      complete: true,
      jobs: [{ id: "review", steps: [{ target: "agent/reviewer" }] }],
    })
    await expect(
      inspectWorkflowTemplate("review/template", controller.signal),
    ).resolves.toMatchObject({
      source: { kind: "template", template_name: "review/template" },
      effects: [{ kind: "external_state_change_possible", occurrences: 1 }],
    })

    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      1,
      "/api/workflows/definitions/inspect",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({ ref: "workflows/review.yml" }),
        signal: controller.signal,
      }),
    )
    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      2,
      "/api/workflows/templates/review%2Ftemplate/inspect",
      { signal: controller.signal },
    )
  })

  it("rejects mismatched and malformed workflow inspection DTOs", async () => {
    mockedLauncherFetch
      .mockResolvedValueOnce(
        jsonResponse(
          definitionInspection({
            source: { kind: "published", ref: "workflows/other.yml" },
          }),
        ),
      )
      .mockResolvedValueOnce(
        jsonResponse({
          ...definitionInspection({
            source: { kind: "template", template_name: "review" },
          }),
          triggers: {
            manual: { present: true, projected: true },
          },
        }),
      )
    await expect(
      inspectPublishedWorkflowDefinition("workflows/review.yml"),
    ).rejects.toThrow("invalid response")
    await expect(inspectWorkflowTemplate("review")).rejects.toThrow(
      "invalid response",
    )
  })

  it("fails closed on unknown inspection claims and projection invariants", async () => {
    const base = definitionInspection({
      source: { kind: "published", ref: "workflows/review.yml" },
    })
    mockedLauncherFetch
      .mockResolvedValueOnce(
        jsonResponse({
          ...base,
          effects: [{ kind: "looks_safe", occurrences: 1 }],
        }),
      )
      .mockResolvedValueOnce(
        jsonResponse({
          ...base,
          triggers: {
            ...base.triggers,
            manual: {
              present: false,
              projected: true,
              value: {},
            },
          },
        }),
      )
      .mockResolvedValueOnce(
        jsonResponse({
          ...base,
          dependencies: [{ kind: "agent", target: "reviewer", occurrences: 0 }],
        }),
      )
      .mockResolvedValueOnce(
        new Response("{}", {
          headers: { "Content-Length": String((32 << 20) + 1) },
        }),
      )

    for (let index = 0; index < 4; index += 1) {
      await expect(
        inspectPublishedWorkflowDefinition("workflows/review.yml"),
      ).rejects.toThrow("invalid response")
    }
  })

  it("accepts server-bounded incomplete trigger and unsafe-field projections", async () => {
    const ref = `workflows/${"r".repeat(1500)}.yml`
    const base = definitionInspection({
      source: { kind: "published", ref },
    })
    mockedLauncherFetch
      .mockResolvedValueOnce(
        jsonResponse({
          ...base,
          complete: false,
          triggers: {
            ...base.triggers,
            schedule: { present: true, projected: false },
          },
          limits: ["triggers_truncated"],
        }),
      )
      .mockResolvedValueOnce(
        jsonResponse({
          ...definitionInspection({
            source: { kind: "published", ref: "workflows/unsafe.yml" },
          }),
          complete: false,
          jobs: [
            {
              id: "review",
              kind: "steps",
              steps: [{ index: 0, kind: "mcp" }],
            },
          ],
          dependencies: [],
          effects: [
            {
              kind: "external_state_change_possible",
              occurrences: 1,
            },
          ],
          limits: ["unsafe_fields_omitted"],
        }),
      )

    await expect(
      inspectPublishedWorkflowDefinition(ref),
    ).resolves.toMatchObject({
      source: { kind: "published", ref },
      complete: false,
      triggers: { schedule: { present: true, projected: false } },
      limits: ["triggers_truncated"],
    })
    await expect(
      inspectPublishedWorkflowDefinition("workflows/unsafe.yml"),
    ).resolves.toMatchObject({
      complete: false,
      jobs: [{ steps: [{ kind: "mcp" }] }],
      effects: [{ kind: "external_state_change_possible" }],
      limits: ["unsafe_fields_omitted"],
    })
  })

  it("enforces the server trigger projection boundaries", async () => {
    const ref = "workflows/bounds.yml"
    const base = definitionInspection({
      source: { kind: "published", ref },
    })
    const exactSchedules = Array.from({ length: 256 }, () => ({
      cron: "",
    }))
    mockedLauncherFetch
      .mockResolvedValueOnce(
        jsonResponse({
          ...base,
          triggers: {
            ...base.triggers,
            schedule: {
              present: true,
              projected: true,
              value: exactSchedules,
            },
          },
        }),
      )
      .mockResolvedValueOnce(
        jsonResponse({
          ...base,
          triggers: {
            ...base.triggers,
            workflow_call: {
              present: true,
              projected: true,
              value: { outputs: ["o".repeat(256)] },
            },
          },
        }),
      )
      .mockResolvedValueOnce(
        jsonResponse({
          ...base,
          triggers: {
            ...base.triggers,
            workflow_call: {
              present: true,
              projected: true,
              value: { outputs: ["o".repeat(257)] },
            },
          },
        }),
      )
      .mockResolvedValueOnce(
        jsonResponse({
          ...base,
          triggers: {
            ...base.triggers,
            schedule: {
              present: true,
              projected: true,
              value: [...exactSchedules, { cron: "" }],
            },
          },
        }),
      )

    await expect(
      inspectPublishedWorkflowDefinition(ref),
    ).resolves.toMatchObject({
      triggers: { schedule: { value: exactSchedules } },
    })
    await expect(
      inspectPublishedWorkflowDefinition(ref),
    ).resolves.toMatchObject({
      triggers: {
        workflow_call: { value: { outputs: ["o".repeat(256)] } },
      },
    })
    await expect(inspectPublishedWorkflowDefinition(ref)).rejects.toThrow(
      "invalid response",
    )
    await expect(inspectPublishedWorkflowDefinition(ref)).rejects.toThrow(
      "invalid response",
    )
  })

  it("accepts only sanitized trigger declaration metadata", async () => {
    const base = definitionInspection({
      source: { kind: "published", ref: "workflows/review.yml" },
    })
    const safeTriggers = {
      ...base.triggers,
      channel_message: {
        present: true,
        projected: true,
        value: {
          channels: ["github"],
          session_configured: true,
          delivery_configured: false,
        },
      },
      runtime_event: {
        present: true,
        projected: true,
        value: {
          kinds: ["workflow.run.failed"],
          session_filter_present: true,
          session_filter_count: 2,
        },
      },
      workflow_call: {
        present: true,
        projected: true,
        value: {
          inputs: {
            ticket: {
              type: "string",
              required: true,
              has_default: false,
            },
          },
          secrets: { api_token: { required: true } },
          outputs: ["summary"],
        },
      },
    }
    mockedLauncherFetch
      .mockResolvedValueOnce(jsonResponse({ ...base, triggers: safeTriggers }))
      .mockResolvedValueOnce(
        jsonResponse({
          ...base,
          triggers: {
            ...safeTriggers,
            runtime_event: {
              ...safeTriggers.runtime_event,
              value: {
                ...safeTriggers.runtime_event.value,
                sessions: ["must-not-cross-boundary"],
              },
            },
          },
        }),
      )
      .mockResolvedValueOnce(
        jsonResponse({
          ...base,
          triggers: {
            ...safeTriggers,
            schedule: {
              present: true,
              projected: true,
              value: [{ cron: "\u202e" }],
            },
          },
        }),
      )

    await expect(
      inspectPublishedWorkflowDefinition("workflows/review.yml"),
    ).resolves.toMatchObject({
      triggers: {
        channel_message: {
          value: {
            session_configured: true,
            delivery_configured: false,
          },
        },
        runtime_event: {
          value: {
            session_filter_present: true,
            session_filter_count: 2,
          },
        },
        workflow_call: {
          value: {
            inputs: { ticket: { has_default: false } },
            secrets: { api_token: { required: true } },
          },
        },
      },
    })
    await expect(
      inspectPublishedWorkflowDefinition("workflows/review.yml"),
    ).rejects.toThrow("invalid response")
    await expect(
      inspectPublishedWorkflowDefinition("workflows/review.yml"),
    ).rejects.toThrow("invalid response")
  })

  it("keeps sanitized invalid topology inspectable", async () => {
    const base = definitionInspection({
      source: { kind: "published", ref: "workflows/invalid.yml" },
    })
    mockedLauncherFetch.mockResolvedValueOnce(
      jsonResponse({
        ...base,
        validation: {
          valid: false,
          issue_count: 1,
          issues: [{ code: "step_id_duplicate", scope: "jobs" }],
          truncated: false,
        },
        jobs: [
          {
            id: "review",
            kind: "steps",
            steps: [
              {
                index: 0,
                id: "duplicate",
                kind: "agent",
                target: "agent/reviewer",
              },
              {
                index: 1,
                id: "duplicate",
                kind: "tool",
                target: "tool/message",
              },
            ],
          },
        ],
      }),
    )

    await expect(
      inspectPublishedWorkflowDefinition("workflows/invalid.yml"),
    ).resolves.toMatchObject({
      validation: {
        valid: false,
        issues: [{ code: "step_id_duplicate", scope: "jobs" }],
      },
      jobs: [{ steps: [{ id: "duplicate" }, { id: "duplicate" }] }],
    })
  })

  it("maps bounded workflow inspection failures without exposing responses", async () => {
    mockedLauncherFetch
      .mockResolvedValueOnce(
        jsonResponse({ error: "workflow_definition_too_large" }, 413),
      )
      .mockResolvedValueOnce(
        jsonResponse({ error: "workflow_inspection_unavailable" }, 503),
      )
      .mockResolvedValueOnce(jsonResponse({ secret: "must not surface" }, 500))

    await expect(
      inspectPublishedWorkflowDefinition("workflows/large.yml"),
    ).rejects.toThrow("too large to inspect safely")
    await expect(inspectWorkflowTemplate("review")).rejects.toThrow(
      "temporarily unavailable",
    )
    await expect(inspectWorkflowTemplate("review")).rejects.toThrow(
      "inspection is unavailable",
    )
  })

  it("gets and revision-fences workflow settings updates", async () => {
    const settings = {
      configured: {
        enabled: true,
        tool_enabled: false,
        definitions_dir: "",
        max_concurrent_runs: 0,
        default_timeout_seconds: 0,
        max_call_depth: 0,
        retention_days: 0,
      },
      effective: {
        enabled: true,
        tool_enabled: false,
        definitions_dir: "workflows",
        max_concurrent_runs: 4,
        default_timeout_seconds: 300,
        max_call_depth: 8,
        retention_days: 30,
      },
      config_revision: "sha256:settings-1",
      effects: {
        launcher_effect: "applied",
        catalog_effect: "applied",
        gateway_effect: "applied",
      },
    }
    mockedLauncherFetch
      .mockResolvedValueOnce(jsonResponse(settings))
      .mockResolvedValueOnce(
        jsonResponse({
          ...settings,
          config_revision: "sha256:settings-2",
          effects: {
            ...settings.effects,
            catalog_effect: "reload_required",
          },
        }),
      )

    await expect(getWorkflowSettings()).resolves.toEqual(settings)
    await expect(
      patchWorkflowSettings({
        expected_config_revision: "sha256:settings-1",
        tool_enabled: true,
        definitions_dir: "automations",
        max_concurrent_runs: 6,
      }),
    ).resolves.toMatchObject({
      config_revision: "sha256:settings-2",
      effects: { catalog_effect: "reload_required" },
    })
    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      1,
      "/api/workflows/settings",
      undefined,
    )
    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      2,
      "/api/workflows/settings",
      expect.objectContaining({
        method: "PATCH",
        body: JSON.stringify({
          expected_config_revision: "sha256:settings-1",
          tool_enabled: true,
          definitions_dir: "automations",
          max_concurrent_runs: 6,
        }),
      }),
    )
  })

  it("maps workflow control errors without exposing raw backend text", async () => {
    mockedLauncherFetch
      .mockResolvedValueOnce(
        new Response('{"error":"config_revision_mismatch"}', {
          status: 409,
        }),
      )
      .mockResolvedValueOnce(
        new Response("read /private/config.yaml: permission denied", {
          status: 500,
        }),
      )
      .mockResolvedValueOnce(
        new Response('{"error":"workflow_development_active"}', {
          status: 409,
        }),
      )
      .mockResolvedValueOnce(
        new Response('{"error":"workflow_transaction_recovery_conflict"}', {
          status: 409,
        }),
      )
      .mockResolvedValueOnce(
        new Response('{"error":"template_recovery_failed"}', {
          status: 503,
        }),
      )

    await expect(
      patchWorkflowSettings({
        expected_config_revision: "stale",
        enabled: true,
      }),
    ).rejects.toThrow(
      "Workflow settings changed elsewhere. Reload them and try again.",
    )
    await expect(listWorkflowTemplates()).rejects.toThrow(
      "Built-in workflow templates are unavailable.",
    )
    await expect(
      patchWorkflowSettings({
        expected_config_revision: "current",
        definitions_dir: "automations",
      }),
    ).rejects.toThrow(
      "Finish or discard the active workflow draft before changing workflow definitions or templates.",
    )
    await expect(listWorkflowTemplates()).rejects.toThrow(
      "Workflow recovery found files changed outside the interrupted transaction. Operator reconciliation is required; no files were changed.",
    )
    await expect(listWorkflowTemplates()).rejects.toThrow(
      "Template recovery needs operator attention. No further changes were attempted.",
    )
  })

  it("checks the exact draft dependency candidate and keeps its revision opaque", async () => {
    const draft = {
      target_ref: "workflows/review.yml",
      yaml: "name: Review\njobs:\n  inspect:\n    steps: []\n",
    }
    mockedLauncherFetch.mockResolvedValueOnce(
      jsonResponse({
        root_ref: "workflows/review.yml",
        revision: "opaque:dependency/revision?keep=exact",
        ready: false,
        workflow_enabled: true,
        structural_ready: false,
        runtime_ready: false,
        dependencies: [
          {
            dependency: {
              kind: "mcp",
              name: "github/get_pull_request",
              workflow_ref: "workflows/review.yml",
              path: "jobs.inspect.steps[0].uses",
            },
            code: "not_connected",
            ready: false,
          },
        ],
        structural_issues: [
          {
            code: "missing_required_input",
            workflow_ref: "workflows/review.yml",
            path: "jobs.reusable.with.owner",
            dependency_kind: "reusable",
            dependency_name: "workflows/shared.yml",
          },
        ],
      }),
    )

    await expect(checkWorkflowDependencies({ draft })).resolves.toMatchObject({
      revision: "opaque:dependency/revision?keep=exact",
      dependencies: [
        {
          dependency: {
            kind: "mcp",
            name: "github/get_pull_request",
          },
          code: "not_connected",
          ready: false,
        },
      ],
      structural_issues: [{ code: "missing_required_input" }],
    })
    expect(mockedLauncherFetch).toHaveBeenCalledWith(
      "/api/workflows/dependencies/check",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({ draft }),
      }),
    )
  })

  it("normalizes nullable dependency report arrays", async () => {
    mockedLauncherFetch.mockResolvedValueOnce(
      jsonResponse({
        root_ref: "workflows/empty.yml",
        revision: "opaque-empty",
        ready: true,
        workflow_enabled: true,
        structural_ready: true,
        runtime_ready: true,
        dependencies: null,
        structural_issues: null,
      }),
    )

    await expect(
      checkWorkflowDependencies({
        draft: {
          target_ref: "workflows/empty.yml",
          yaml: "name: Empty\njobs: {}\n",
        },
      }),
    ).resolves.toMatchObject({
      revision: "opaque-empty",
      dependencies: [],
      structural_issues: [],
    })
  })

  it("revision-fences publish and masks dependency and publish errors", async () => {
    const publishRequest = {
      session_id: "dev-1",
      expected_session_revision: "opaque-session",
      expected_draft_revision: "opaque-draft",
      expected_base_target_revision: "opaque-base",
      expected_dependency_revision: "opaque-dependencies",
    }
    mockedLauncherFetch
      .mockResolvedValueOnce(
        jsonResponse({
          workflow_ref: "workflows/review.yml",
          session: {
            id: "dev-1",
            status: "published",
          },
        }),
      )
      .mockResolvedValueOnce(
        new Response('{"error":"dependency_revision_mismatch"}', {
          status: 409,
        }),
      )
      .mockResolvedValueOnce(
        new Response("read /private/workflows: permission denied", {
          status: 500,
        }),
      )

    await expect(
      publishWorkflowDevelopment(publishRequest),
    ).resolves.toMatchObject({
      workflow_ref: "workflows/review.yml",
    })
    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      1,
      "/api/workflows/development/publish",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify(publishRequest),
      }),
    )
    await expect(publishWorkflowDevelopment(publishRequest)).rejects.toThrow(
      "Workflow dependencies changed. Wait for a fresh readiness check and try again.",
    )
    await expect(
      checkWorkflowDependencies({
        draft: {
          target_ref: "workflows/review.yml",
          yaml: "name: Review\n",
        },
      }),
    ).rejects.toThrow("Workflow dependency readiness is unavailable.")
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

  it("inspects all typed triggers and renders an explicit family deletion", async () => {
    const yaml =
      "name: Multi trigger\non:\n  schedule:\n    - cron: '0 * * * *'\njobs: {}\n"
    const projections = {
      manual: { present: false, editable: true, value: null },
      schedule: {
        present: true,
        editable: true,
        value: [{ cron: "0 * * * *" }],
      },
      channel_message: { present: false, editable: true, value: null },
      command: { present: false, editable: true, value: null },
      runtime_event: { present: false, editable: true, value: null },
      event: { present: false, editable: true, value: null },
      workflow_call: { present: false, editable: true, value: null },
    }
    mockedLauncherFetch
      .mockResolvedValueOnce(
        jsonResponse({
          revision: "opaque:trigger/revision?exact",
          triggers: projections,
          validation: { valid: true, validated_at: "2026-07-29T12:00:00Z" },
        }),
      )
      .mockResolvedValueOnce(
        jsonResponse({
          yaml: "name: Multi trigger\njobs: {}\n",
          revision: "opaque:trigger/revision?next",
          triggers: {
            ...projections,
            schedule: { present: false, editable: true, value: null },
          },
          validation: { valid: true, validated_at: "2026-07-29T12:00:01Z" },
        }),
      )

    await expect(inspectWorkflowTriggers(yaml)).resolves.toMatchObject({
      revision: "opaque:trigger/revision?exact",
      triggers: {
        schedule: {
          present: true,
          value: [{ cron: "0 * * * *" }],
        },
      },
    })
    await expect(
      renderWorkflowTrigger({
        yaml,
        revision: "opaque:trigger/revision?exact",
        trigger_type: "schedule",
        trigger: null,
      }),
    ).resolves.toMatchObject({
      revision: "opaque:trigger/revision?next",
      triggers: { schedule: { present: false, value: null } },
    })

    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      1,
      "/api/workflows/development/triggers/inspect",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({ yaml }),
      }),
    )
    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      2,
      "/api/workflows/development/triggers/render",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({
          yaml,
          revision: "opaque:trigger/revision?exact",
          trigger_type: "schedule",
          trigger: null,
        }),
      }),
    )
  })

  it("retains bounded candidate validation details on trigger render errors", async () => {
    mockedLauncherFetch.mockResolvedValueOnce(
      jsonResponse(
        {
          error: "invalid_workflow_trigger",
          candidate_validation: {
            valid: false,
            errors: [
              {
                path: "on.schedule[0].cron",
                message: "invalid cron expression",
              },
            ],
            validated_at: "2026-07-29T12:00:00Z",
          },
        },
        422,
      ),
    )

    await expect(
      renderWorkflowTrigger({
        yaml: "on:\n  schedule: []\njobs: {}\n",
        revision: "trigger-revision",
        trigger_type: "schedule",
        trigger: [{ cron: "not-a-cron" }],
      }),
    ).rejects.toEqual(
      expect.objectContaining<Partial<WorkflowAPIError>>({
        status: 422,
        message: "invalid_workflow_trigger",
        candidateValidation: {
          valid: false,
          errors: [
            {
              path: "on.schedule[0].cron",
              message: "invalid cron expression",
            },
          ],
          validated_at: "2026-07-29T12:00:00Z",
        },
      }),
    )
  })

  it("accepts the backend candidate-validation limit of 128 issues", async () => {
    const errors = Array.from({ length: 128 }, (_, index) => ({
      path: `on.schedule[${index}].cron`,
      message: `invalid cron expression ${index}`,
    }))
    mockedLauncherFetch.mockResolvedValueOnce(
      jsonResponse(
        {
          error: "invalid_workflow_trigger",
          candidate_validation: {
            valid: false,
            errors,
            validated_at: "2026-07-29T12:00:00Z",
          },
        },
        422,
      ),
    )

    const error = await renderWorkflowTrigger({
      yaml: "on:\n  schedule: []\njobs: {}\n",
      revision: "trigger-revision",
      trigger_type: "schedule",
      trigger: [{ cron: "not-a-cron" }],
    }).catch((caught: unknown) => caught)

    expect(error).toBeInstanceOf(WorkflowAPIError)
    expect(
      (error as WorkflowAPIError).candidateValidation?.errors,
    ).toHaveLength(128)
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

  it("returns degraded reconciliation metadata from development polling and accepted tests", async () => {
    const reconciliation = {
      state: "degraded",
      reason: "draft_test_snapshot_not_recorded",
      run_id: "wr_durable_test",
      message:
        "The workflow run was created, but its development snapshot could not be recorded.",
    }
    const session = {
      id: "dev-reconciliation",
      session_revision: "session-revision",
      draft_revision: "draft-revision",
      base_target_revision: "base-revision",
      reason: "new",
      status: "editing",
      target_workflow_ref: "workflows/reconciliation.yml",
      yaml: "name: Reconciliation\njobs: {}\n",
      created_at: "2026-07-29T12:00:00Z",
      updated_at: "2026-07-29T12:00:00Z",
    }
    mockedLauncherFetch
      .mockResolvedValueOnce(jsonResponse({ session, reconciliation }))
      .mockResolvedValueOnce(
        jsonResponse(
          {
            session,
            result: {
              run_id: reconciliation.run_id,
              status: "running",
            },
            reconciliation,
          },
          202,
        ),
      )
      .mockResolvedValueOnce(
        jsonResponse(
          {
            session,
            reconciliation,
            error: "The accepted test state could not be refreshed.",
          },
          409,
        ),
      )

    await expect(getWorkflowDevelopment()).resolves.toEqual({
      session,
      reconciliation,
    })
    await expect(
      testWorkflowDevelopment({
        target_ref: session.target_workflow_ref,
        yaml: session.yaml,
        async: true,
      }),
    ).resolves.toEqual({
      session,
      result: {
        run_id: reconciliation.run_id,
        status: "running",
      },
      reconciliation,
    })
    await expect(
      testWorkflowDevelopment({
        target_ref: session.target_workflow_ref,
        yaml: session.yaml,
        async: true,
      }),
    ).resolves.toEqual({
      session,
      reconciliation,
      error: "The accepted test state could not be refreshed.",
    })
  })

  it.each([
    [
      "manual",
      {
        trigger: { type: "manual" },
        scenario: {
          inputs: { ticket: "PIC-42" },
          secrets: { token: "hidden" },
          session: "workflow:test",
          delivery: { channel: "slack" },
        },
      },
    ],
    [
      "workflow_call",
      {
        trigger: { type: "workflow_call" },
        scenario: { inputs: {}, secrets: {}, delivery: {} },
      },
    ],
    [
      "schedule",
      {
        trigger: { type: "schedule", schedule_index: 1 },
        scenario: { scheduled_at: "2026-07-30T12:00:00Z" },
      },
    ],
    [
      "channel_message",
      {
        trigger: { type: "channel_message" },
        scenario: { message: { channel: "slack", text: "hello" } },
      },
    ],
    [
      "command",
      {
        trigger: { type: "command" },
        scenario: { message: { channel: "slack", text: "/review 42" } },
      },
    ],
    [
      "runtime_event",
      {
        trigger: { type: "runtime_event" },
        scenario: {
          event: {
            id: "runtime-1",
            kind: "agent.turn.completed",
            time: "2026-07-30T12:00:00Z",
            source: { component: "agent" },
          },
        },
      },
    ],
    [
      "event",
      {
        trigger: { type: "event" },
        scenario: { event_id: "ev_0123456789abcdef0123456789abcdef" },
      },
    ],
  ] as const)(
    "serializes the exact %s tagged trigger request",
    (_kind, variant) => {
      const request = {
        ...simulationRequestBase(),
        ...variant,
      } as WorkflowTriggerSimulationRequest
      expect(JSON.parse(workflowTriggerSimulationRequestBody(request))).toEqual(
        {
          ...simulationRequestBase(),
          ...variant,
        },
      )
    },
  )

  it("strictly parses a safe server simulation without reflected values", async () => {
    mockedLauncherFetch.mockResolvedValueOnce(
      jsonResponse(triggerSimulationResponse()),
    )

    await expect(
      simulateWorkflowDevelopmentTrigger(manualSimulationRequest()),
    ).resolves.toEqual(triggerSimulationResponse())
    expect(mockedLauncherFetch).toHaveBeenCalledWith(
      "/api/workflows/development/triggers/simulate",
      expect.objectContaining({ method: "POST" }),
    )
    const init = mockedLauncherFetch.mock.calls[0][1] as RequestInit
    expect(String(init.body)).toContain('"secrets":{"token":"hidden"}')
    expect(String(init.body)).not.toContain('"async"')
  })

  it.each([
    ["unknown root field", { reflected_secret: "hidden" }],
    [
      "unsafe simulation reflection",
      {
        simulation: {
          ...triggerSimulationResponse().simulation,
          message_text: "protected-payload",
        },
      },
    ],
    [
      "unsafe context reflection",
      {
        simulation: {
          ...triggerSimulationResponse().simulation,
          context_summary: {
            ...triggerSimulationResponse().simulation.context_summary,
            session: "private-session",
          },
        },
      },
    ],
  ])("rejects a simulation with %s", async (_label, override) => {
    mockedLauncherFetch.mockResolvedValueOnce(
      jsonResponse({
        ...triggerSimulationResponse(),
        ...override,
      }),
    )
    await expect(
      simulateWorkflowDevelopmentTrigger(manualSimulationRequest()),
    ).rejects.toThrow("invalid response")
  })

  it("requires a review token exactly when simulation is executable", async () => {
    const response = triggerSimulationResponse()
    const withoutToken: Partial<typeof response> = { ...response }
    delete withoutToken.review_token
    mockedLauncherFetch.mockResolvedValueOnce(jsonResponse(withoutToken))

    await expect(
      simulateWorkflowDevelopmentTrigger(manualSimulationRequest()),
    ).rejects.toThrow("invalid response")
  })

  it("executes only an HTTP 202 response with a fenced run identity", async () => {
    const execution = triggerExecutionResponse()
    mockedLauncherFetch.mockResolvedValueOnce(jsonResponse(execution, 202))

    await expect(
      executeWorkflowDevelopmentTrigger(
        manualSimulationRequest(),
        "review-token",
      ),
    ).resolves.toEqual(execution)
    const init = mockedLauncherFetch.mock.calls[0][1] as RequestInit
    expect(JSON.parse(String(init.body))).toEqual({
      ...JSON.parse(
        workflowTriggerSimulationRequestBody(manualSimulationRequest()),
      ),
      review_token: "review-token",
    })

    mockedLauncherFetch.mockResolvedValueOnce(jsonResponse(execution, 200))
    await expect(
      executeWorkflowDevelopmentTrigger(
        manualSimulationRequest(),
        "review-token",
      ),
    ).rejects.toBeInstanceOf(WorkflowAPIError)
  })

  it("rejects oversized review tokens and non-running execution results", async () => {
    const oversized = triggerSimulationResponse()
    oversized.review_token = "x".repeat(4097)
    mockedLauncherFetch.mockResolvedValueOnce(jsonResponse(oversized))
    await expect(
      simulateWorkflowDevelopmentTrigger(manualSimulationRequest()),
    ).rejects.toThrow("invalid response")

    mockedLauncherFetch.mockResolvedValueOnce(
      jsonResponse(
        {
          ...triggerExecutionResponse(),
          result: {
            run_id: "wr_trigger_test",
            status: "succeeded",
            outputs: {},
          },
        },
        202,
      ),
    )
    await expect(
      executeWorkflowDevelopmentTrigger(
        manualSimulationRequest(),
        "review-token",
      ),
    ).rejects.toThrow("invalid response")
  })

  it("strictly validates returned execution session snapshots", async () => {
    const invalid = triggerExecutionResponse()
    invalid.session.last_test = {
      ...invalid.session.last_test,
      reflected_payload: "protected-payload",
    } as typeof invalid.session.last_test
    mockedLauncherFetch.mockResolvedValueOnce(jsonResponse(invalid, 202))

    await expect(
      executeWorkflowDevelopmentTrigger(
        manualSimulationRequest(),
        "review-token",
      ),
    ).rejects.toThrow("invalid response")
  })

  it("accepts a session-omitted 202 only for the bounded truncated response", async () => {
    const truncated = {
      result: {
        run_id: "wr_trigger_test",
        status: "running",
      },
      reconciliation: {
        state: "degraded",
        reason: "draft_test_response_truncated",
        run_id: "wr_trigger_test",
        message:
          "The workflow run was created; refresh to load its development state.",
      },
    }
    mockedLauncherFetch.mockResolvedValueOnce(jsonResponse(truncated, 202))
    await expect(
      executeWorkflowDevelopmentTrigger(
        manualSimulationRequest(),
        "review-token",
      ),
    ).resolves.toEqual(truncated)

    mockedLauncherFetch.mockResolvedValueOnce(
      jsonResponse(
        {
          ...truncated,
          reconciliation: {
            ...truncated.reconciliation,
            reason: "draft_test_snapshot_not_recorded",
          },
        },
        202,
      ),
    )
    await expect(
      executeWorkflowDevelopmentTrigger(
        manualSimulationRequest(),
        "review-token",
      ),
    ).rejects.toThrow("invalid response")

    mockedLauncherFetch.mockResolvedValueOnce(
      jsonResponse({ result: truncated.result }, 202),
    )
    await expect(
      executeWorkflowDevelopmentTrigger(
        manualSimulationRequest(),
        "review-token",
      ),
    ).rejects.toThrow("invalid response")
  })
})

function simulationRequestBase() {
  return {
    session_id: "dev-session",
    expected_session_revision: "session-revision",
    expected_draft_revision: "draft-revision",
    prompt: "Simulate the workflow",
    target_ref: "workflows/simulate.yml",
    yaml: "name: Simulate\non: manual\njobs: {}\n",
  }
}

function manualSimulationRequest(): WorkflowTriggerSimulationRequest {
  return {
    ...simulationRequestBase(),
    trigger: { type: "manual" },
    scenario: {
      inputs: { ticket: "PIC-42" },
      secrets: { token: "hidden" },
      session: "workflow:test",
      delivery: { channel: "slack" },
    },
  }
}

function triggerSimulationResponse() {
  return {
    simulation: {
      selected_kind: "manual" as const,
      effective_kind: "manual" as const,
      present: true,
      matched: true,
      executable: true,
      reason: "matched" as const,
      context_summary: {
        input_count: 1,
        secret_count: 1,
        has_event: false,
        has_session: true,
        has_delivery: true,
      },
    },
    review: {
      job_count: 1,
      step_count: 1,
      targets: ["agent/main"],
      effects: [
        {
          kind: "model_or_delegated_action_possible" as const,
          target: "agent/main",
          occurrences: 1,
        },
      ],
      complete: true,
      validation: {
        valid: true,
        issue_count: 0,
        issues: [],
        truncated: false,
      },
      limits: [],
    },
    review_token: "review-token",
  }
}

function triggerExecutionResponse() {
  return {
    session: {
      id: "dev-session",
      session_revision: "session-revision-2",
      draft_revision: "draft-revision-2",
      base_target_revision: "base-revision",
      reason: "new",
      status: "testing",
      prompt: "Simulate the workflow",
      target_workflow_ref: "workflows/simulate.yml",
      target_picoclaw_version: "test",
      target_git_commit: "abcdef",
      yaml: "name: Simulate\non: manual\njobs: {}\n",
      validation: {
        valid: true,
        validated_at: "2026-07-30T12:00:00Z",
      },
      last_test: {
        draft_key:
          "workflows/simulate.yml\u0000name: Simulate\non: manual\njobs: {}\n",
        draft_revision: "draft-revision-2",
        target_workflow_ref: "workflows/simulate.yml",
        run_id: "wr_trigger_test",
        status: "running",
        tested_at: "2026-07-30T12:00:01Z",
      },
      created_at: "2026-07-30T12:00:00Z",
      updated_at: "2026-07-30T12:00:01Z",
    },
    result: {
      run_id: "wr_trigger_test",
      status: "running",
    },
  }
}

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

function definitionInspection({
  source,
}: {
  source:
    | { kind: "published"; ref: string }
    | { kind: "template"; template_name: string }
}) {
  return {
    source,
    revision: "sha256:inspection",
    complete: true,
    validation: {
      valid: true,
      issue_count: 0,
      issues: [],
      truncated: false,
    },
    triggers: {
      manual: { present: false, projected: true },
      schedule: {
        present: true,
        projected: true,
        value: [{ cron: "0 9 * * 1" }],
      },
      channel_message: { present: false, projected: true },
      command: { present: false, projected: true },
      runtime_event: { present: false, projected: true },
      event: { present: false, projected: true },
      workflow_call: { present: false, projected: true },
    },
    jobs: [
      {
        id: "review",
        kind: "steps",
        steps: [
          {
            index: 0,
            id: "inspect",
            kind: "agent",
            target: "agent/reviewer",
          },
        ],
      },
    ],
    dependencies: [{ kind: "agent", target: "reviewer", occurrences: 1 }],
    effects: [{ kind: "external_state_change_possible", occurrences: 1 }],
    limits: [],
  }
}
