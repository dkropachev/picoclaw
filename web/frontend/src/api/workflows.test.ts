import { beforeEach, describe, expect, it, vi } from "vitest"

import { launcherFetch } from "@/api/http"
import {
  checkWorkflowDependencies,
  getWorkflowRun,
  getWorkflowRunEvents,
  getWorkflowRunGraph,
  getWorkflowSettings,
  inspectWorkflowEventTrigger,
  installWorkflowTemplate,
  listWorkflowRuns,
  listWorkflowTemplates,
  listWorkflows,
  matchWorkflowEventTrigger,
  patchWorkflowSettings,
  publishWorkflowDevelopment,
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

  it("gets and revision-fences workflow settings updates", async () => {
    const settings = {
      configured: {
        enabled: true,
        definitions_dir: "",
        max_concurrent_runs: 0,
        default_timeout_seconds: 0,
        max_call_depth: 0,
        retention_days: 0,
      },
      effective: {
        enabled: true,
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
