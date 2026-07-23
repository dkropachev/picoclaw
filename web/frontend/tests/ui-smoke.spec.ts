import AxeBuilder from "@axe-core/playwright"
import { type Page, type Route, expect, test } from "@playwright/test"

const smokeRoutes = [
  "/",
  "/models",
  "/accounts",
  "/logs",
  "/agent/git-workspaces",
  "/agent/tools",
  "/agent/workflows",
  "/agent/skills",
  "/agent/hub",
] as const

const modelResponse = {
  models: [
    {
      index: 0,
      model_name: "gpt-4o-mini",
      provider: "openai",
      model: "gpt-4o-mini",
      api_key: "",
      enabled: true,
      available: true,
      status: "available",
      is_default: true,
      is_virtual: false,
      default_model_allowed: true,
    },
    {
      index: 1,
      model_name: "gpt-4o",
      provider: "openai",
      model: "gpt-4o",
      api_key: "sk-****test",
      enabled: true,
      available: true,
      status: "available",
      is_default: false,
      is_virtual: false,
      default_model_allowed: true,
    },
  ],
  total: 2,
  default_model: "gpt-4o-mini",
  provider_options: [
    {
      id: "openai",
      display_name: "OpenAI",
      default_api_base: "https://api.openai.com/v1",
      empty_api_key_allowed: false,
      create_allowed: true,
      default_model_allowed: true,
      supports_fetch: true,
    },
  ],
}

const toolsResponse = {
  tools: [
    {
      name: "web_search",
      description: "Search the web",
      category: "web",
      config_key: "tools.web_search",
      status: "enabled",
    },
    {
      name: "find_skills",
      description: "Find skills",
      category: "skills",
      config_key: "tools.find_skills",
      status: "enabled",
    },
    {
      name: "install_skill",
      description: "Install skills",
      category: "skills",
      config_key: "tools.install_skill",
      status: "enabled",
    },
  ],
}

const gitWorkspaceResponse = {
  root_dir: "/tmp/picoclaw-git-workspaces",
  max_total_size_bytes: 21474836480,
  ignored_cleanup_delay_seconds: 86400,
  drop_delay_seconds: 2592000,
  total_size_bytes: 4096,
  ignored_bytes: 512,
  repository_count: 1,
  workspace_count: 1,
  locked_workspace_count: 0,
  repositories: [
    {
      id: "gw-repo",
      remote_url: "https://example.test/repo.git",
      first_seen_at: "2026-07-16T12:00:00Z",
      last_seen_at: "2026-07-16T12:00:00Z",
      workspace_count: 1,
      locked_count: 0,
      size_bytes: 4096,
      ignored_bytes: 512,
    },
  ],
  workspaces: [
    {
      id: "gw-workspace",
      repo_id: "gw-repo",
      remote_url: "https://example.test/repo.git",
      path: "/tmp/picoclaw-git-workspaces/checkouts/repo-gw-workspace",
      current_branch: "main",
      dirty: false,
      size_bytes: 4096,
      ignored_bytes: 512,
      created_at: "2026-07-16T12:00:00Z",
      updated_at: "2026-07-16T12:00:00Z",
      status: "available",
    },
  ],
  history: [
    {
      id: "hist-1",
      time: "2026-07-16T12:00:00Z",
      action: "allocated",
      repo_id: "gw-repo",
      workspace_id: "gw-workspace",
    },
  ],
}

const webSearchConfigResponse = {
  provider: "openai",
  current_service: "openai",
  prefer_native: true,
  providers: [
    {
      id: "openai",
      label: "OpenAI",
      configured: true,
      current: true,
      requires_auth: true,
    },
  ],
  settings: {
    openai: {
      enabled: true,
      max_results: 5,
      api_key_set: true,
    },
  },
}

const skillsResponse = {
  skills: [
    {
      name: "review-helper",
      path: "/workspace/skills/review-helper",
      source: "workspace",
      description: "Review code changes",
      origin_kind: "manual",
    },
  ],
}

const workflowRun = {
  id: "wr_test",
  workflow_ref: "workflows/summarize-text.yml",
  status: "succeeded",
  session: "workflow:demo",
  inputs: { text: "hello" },
  outputs: { summary: "hello" },
  jobs: {
    main: { id: "main", status: "succeeded" },
  },
  steps: {
    "main/summarize": { id: "summarize", status: "succeeded" },
  },
  child_run_ids: [],
  created_at: "2026-07-16T12:00:00Z",
  updated_at: "2026-07-16T12:00:01Z",
  completed_at: "2026-07-16T12:00:01Z",
}

const nullableWorkflowRun = {
  ...workflowRun,
  id: "wr_nulls",
  child_run_ids: null,
  jobs: null,
  steps: null,
}

const retryWorkflowRun = {
  ...workflowRun,
  id: "wr_retry",
  retry_of_run_id: "wr_test",
  outputs: { summary: "retry summary" },
  created_at: "2026-07-16T12:00:02Z",
  updated_at: "2026-07-16T12:00:03Z",
  completed_at: "2026-07-16T12:00:03Z",
}

const workflowDraftYAML = `name: Support Triage
on:
  workflow_call:
    inputs:
      ticket:
        type: string
        required: true
jobs:
  triage:
    runs-on: picoclaw
    steps:
      - id: summarize
        uses: agent/main
        with:
          prompt: Summarize support tickets
`

const supportTriageWorkflowDefinition = {
  ref: "workflows/support-triage.yml",
  name: "Support Triage",
  workflow_call: {
    inputs: {
      ticket: {
        type: "string",
        required: true,
      },
    },
  },
}

const workflowDraftSession = {
  id: "dev_test",
  reason: "new",
  status: "editing",
  prompt: "Triage support tickets",
  target_workflow_ref: "workflows/support-triage.yml",
  target_picoclaw_version: "test",
  target_git_commit: "test",
  yaml: workflowDraftYAML,
  validation: {
    valid: true,
    validated_at: "2026-07-16T12:00:00Z",
  },
  created_at: "2026-07-16T12:00:00Z",
  updated_at: "2026-07-16T12:00:00Z",
}

const workflowDraftLastTest = {
  draft_key: workflowDraftKey(
    workflowDraftSession.target_workflow_ref,
    workflowDraftYAML,
  ),
  target_workflow_ref: workflowDraftSession.target_workflow_ref,
  run_id: "wr_draft",
  status: "succeeded",
  tested_at: "2026-07-16T12:01:01Z",
}

type MockWorkflowDevelopmentSession = typeof workflowDraftSession & {
  source_workflow_ref?: string
  last_test?: typeof workflowDraftLastTest
}

const draftWorkflowRun = {
  id: "wr_draft",
  workflow_ref: "draft:workflows/support-triage.yml",
  status: "succeeded",
  session: "workflow:draft",
  delivery: {
    channel: "telegram",
    chat_id: "support",
    topic_id: "draft-topic",
  },
  event: {
    source: "draft_test",
    request_id: "req_draft",
  },
  inputs: { ticket: "Printer is offline" },
  outputs: { summary: "draft summary" },
  jobs: {
    triage: {
      id: "triage",
      status: "succeeded",
      outputs: { summary: "draft summary" },
    },
  },
  steps: {
    "triage/summarize": {
      id: "summarize",
      status: "succeeded",
      outputs: { text: "draft summary" },
    },
  },
  child_run_ids: [],
  created_at: "2026-07-16T12:01:00Z",
  updated_at: "2026-07-16T12:01:01Z",
  completed_at: "2026-07-16T12:01:01Z",
}

const manualWorkflowRun = {
  id: "wr_manual",
  workflow_ref: "workflows/support-triage.yml",
  status: "succeeded",
  session: "workflow:manual",
  delivery: {
    channel: "telegram",
    chat_id: "support",
    topic_id: "manual-topic",
  },
  event: {
    source: "manual",
    request_id: "req_manual",
  },
  inputs: { ticket: "Printer is offline" },
  outputs: { summary: "manual summary" },
  jobs: {
    triage: {
      id: "triage",
      status: "succeeded",
      outputs: { summary: "manual summary" },
    },
  },
  steps: {
    "triage/summarize": {
      id: "summarize",
      status: "succeeded",
      outputs: { text: "manual summary" },
    },
  },
  child_run_ids: [],
  created_at: "2026-07-16T12:02:00Z",
  updated_at: "2026-07-16T12:02:01Z",
  completed_at: "2026-07-16T12:02:01Z",
}

const runningDraftWorkflowRun = {
  ...draftWorkflowRun,
  status: "running",
  outputs: {},
  jobs: {
    triage: {
      ...draftWorkflowRun.jobs.triage,
      status: "running",
      outputs: {},
    },
  },
  steps: {
    "triage/summarize": {
      ...draftWorkflowRun.steps["triage/summarize"],
      status: "running",
      outputs: {},
    },
  },
  completed_at: undefined,
}

const failedDraftWorkflowRun = {
  ...draftWorkflowRun,
  id: "wr_draft_failed",
  status: "failed",
  error: "agent step failed",
  outputs: {},
  jobs: {
    triage: {
      ...draftWorkflowRun.jobs.triage,
      status: "failed",
      error: "agent step failed",
      outputs: {},
    },
  },
  steps: {
    "triage/summarize": {
      ...draftWorkflowRun.steps["triage/summarize"],
      status: "failed",
      error: "agent step failed",
      outputs: {},
    },
  },
  updated_at: "2026-07-16T12:01:03Z",
  completed_at: "2026-07-16T12:01:03Z",
}

const runningManualWorkflowRun = {
  ...manualWorkflowRun,
  status: "running",
  outputs: {},
  jobs: {
    triage: {
      ...manualWorkflowRun.jobs.triage,
      status: "running",
      outputs: {},
    },
  },
  steps: {
    "triage/summarize": {
      ...manualWorkflowRun.steps["triage/summarize"],
      status: "running",
      outputs: {},
    },
  },
  completed_at: undefined,
}

function workflowStamp(ref: string, status = "valid") {
  const stamp: {
    workflow_ref: string
    workflow_hash: string
    validated_against_picoclaw_version: string
    validated_against_git_commit: string
    workflow_engine_version: string
    workflow_schema_version: string
    validator_fingerprint: string
    status: string
    validated_at: string
    warnings?: Array<{ message: string }>
  } = {
    workflow_ref: ref,
    workflow_hash: `${ref}:hash`,
    validated_against_picoclaw_version: "test",
    validated_against_git_commit: "test",
    workflow_engine_version: "1",
    workflow_schema_version: "1",
    validator_fingerprint: "test",
    status,
    validated_at: "2026-07-16T12:00:00Z",
  }
  if (status === "pending_revalidation") {
    stamp.warnings = [
      {
        message:
          "workflow must be revalidated after the current Picoclaw version change",
      },
    ]
  }
  return stamp
}

function workflowDraftKey(ref: string, yaml: string) {
  return `${ref.trim()}\u0000${normalizeWorkflowDraftYAML(yaml)}`
}

function normalizeWorkflowDraftYAML(yaml: string) {
  const trimmed = yaml.trimEnd()
  return trimmed === "" ? "" : `${trimmed}\n`
}

const channelCatalogResponse = {
  channels: [
    {
      name: "telegram",
      display_name: "Telegram",
      config_key: "telegram",
    },
    {
      name: "discord",
      display_name: "Discord",
      config_key: "discord",
    },
  ],
}

interface MockLauncherApiOptions {
  completeDraftViaPolling?: boolean
  nullableWorkflowPayloads?: boolean
  oauthProviders?: unknown[]
}

async function mockLauncherApis(
  page: Page,
  options: MockLauncherApiOptions = {},
) {
  let activeDevelopmentSession: MockWorkflowDevelopmentSession | null = null
  let workflowDefinitions = [
    {
      ref: "workflows/summarize-text.yml",
      name: "Summarize text",
    },
  ]
  let runs = options.nullableWorkflowPayloads
    ? [nullableWorkflowRun]
    : [workflowRun]
  let workflowsRevalidated = false
  let completeDraftViaPolling = false

  function compatibilityResponse() {
    const stamps = workflowDefinitions.map((workflow) =>
      workflowStamp(
        workflow.ref,
        workflowsRevalidated ? "valid" : "pending_revalidation",
      ),
    )
    const pending = stamps.filter(
      (stamp) => stamp.status === "pending_revalidation",
    ).length
    return {
      current: {
        picoclaw_version: "test",
        git_commit: "test",
        workflow_engine_version: "1",
        workflow_schema_version: "1",
        validator_fingerprint: "test",
      },
      workflows: stamps,
      counts: workflowsRevalidated
        ? { valid: workflowDefinitions.length }
        : { pending_revalidation: pending },
      version_changed: !workflowsRevalidated,
      manifest_missing: false,
      has_blocking: !workflowsRevalidated,
    }
  }

  function runByID(id: string) {
    return runs.find((run) => run.id === id) ?? workflowRun
  }

  function currentDraftKey(session: MockWorkflowDevelopmentSession) {
    return workflowDraftKey(session.target_workflow_ref, session.yaml)
  }

  await page.route(
    (url) => url.pathname.startsWith("/api/"),
    async (route) => {
      const request = route.request()
      const url = new URL(request.url())
      const path = url.pathname
      const method = request.method()

      if (method === "POST") {
        switch (path) {
          case "/api/models/fetch":
            return json(route, {
              models: [
                { id: "gpt-4o", owned_by: "openai" },
                { id: "gpt-5.4", owned_by: "openai" },
              ],
              total: 2,
            })
          case "/api/workflows/development/start": {
            const body = request.postDataJSON() as {
              reason?: string
              prompt?: string
              ref?: string
              target_ref?: string
            }
            if (body.reason === "version_revalidation") {
              activeDevelopmentSession = {
                ...workflowDraftSession,
                reason: "version_revalidation",
                prompt: body.prompt ?? "",
                source_workflow_ref: body.ref,
                target_workflow_ref:
                  body.target_ref ??
                  body.ref ??
                  workflowDraftSession.target_workflow_ref,
                yaml: workflowDraftYAML,
              }
            } else {
              activeDevelopmentSession = {
                ...workflowDraftSession,
                prompt: body.prompt ?? workflowDraftSession.prompt,
                target_workflow_ref:
                  body.target_ref ?? workflowDraftSession.target_workflow_ref,
              }
            }
            return json(route, { session: activeDevelopmentSession })
          }
          case "/api/workflows/development/ai-revise": {
            const body = request.postDataJSON() as {
              prompt?: string
              target_ref?: string
              yaml?: string
            }
            if (body.prompt?.includes("Last draft test failed")) {
              expect(body.prompt).toContain("Run ID: wr_draft_failed")
              expect(body.prompt).toContain("Error: agent step failed")
              expect(body.prompt).toContain(
                '"workflow_ref": "draft:workflows/support-triage.yml"',
              )
              expect(body.prompt).toContain('"triage/summarize"')
              expect(body.prompt).toContain('"kind": "workflow.run.end"')
              expect(body.prompt).toContain("draft failure event")
            }
            const previous = activeDevelopmentSession ?? workflowDraftSession
            activeDevelopmentSession = {
              ...previous,
              prompt: body.prompt ?? previous.prompt,
              target_workflow_ref:
                body.target_ref ?? previous.target_workflow_ref,
              yaml:
                typeof body.yaml === "string"
                  ? normalizeWorkflowDraftYAML(body.yaml)
                  : previous.yaml,
              validation: {
                valid: true,
                validated_at: "2026-07-16T12:00:02Z",
              },
              updated_at: "2026-07-16T12:00:02Z",
            }
            return json(route, { session: activeDevelopmentSession })
          }
          case "/api/workflows/development/revise": {
            const body = request.postDataJSON() as {
              prompt?: string
              target_ref?: string
              yaml?: string
              regenerate?: boolean
            }
            const previous = activeDevelopmentSession ?? workflowDraftSession
            const nextYAML =
              typeof body.yaml === "string"
                ? normalizeWorkflowDraftYAML(body.yaml)
                : previous.yaml
            const nextTargetRef =
              typeof body.target_ref === "string" && body.target_ref !== ""
                ? body.target_ref
                : previous.target_workflow_ref
            const draftChanged =
              nextTargetRef !== previous.target_workflow_ref ||
              normalizeWorkflowDraftYAML(nextYAML) !==
                normalizeWorkflowDraftYAML(previous.yaml)
            activeDevelopmentSession = {
              ...previous,
              prompt: body.prompt ?? previous.prompt,
              target_workflow_ref: nextTargetRef,
              yaml: nextYAML,
              updated_at: "2026-07-16T12:01:02Z",
            }
            if (draftChanged) {
              activeDevelopmentSession = {
                ...activeDevelopmentSession,
                status: "editing",
              }
              delete activeDevelopmentSession.last_test
            }
            return json(route, { session: activeDevelopmentSession })
          }
          case "/api/workflows/development/discard": {
            const previous = activeDevelopmentSession
            activeDevelopmentSession = null
            return json(route, { session: previous })
          }
          case "/api/workflows/development/test": {
            const testBody = request.postDataJSON() as {
              async: boolean
              inputs?: { ticket?: string }
              session?: string
              delivery?: Record<string, unknown>
            }
            expect(testBody).toMatchObject({
              async: true,
              session: "workflow:draft",
              delivery: {
                channel: "telegram",
                chat_id: "support",
              },
            })
            if (testBody.inputs?.ticket === "Trigger failure") {
              activeDevelopmentSession = {
                ...workflowDraftSession,
                status: "editing",
                last_test: {
                  ...workflowDraftLastTest,
                  run_id: failedDraftWorkflowRun.id,
                  status: "failed",
                  error: "agent step failed",
                },
              }
              runs = [
                failedDraftWorkflowRun,
                ...runs.filter((run) => run.id !== "wr_draft_failed"),
              ]
              return json(route, {
                session: activeDevelopmentSession,
                result: {
                  run_id: failedDraftWorkflowRun.id,
                  status: "failed",
                  error: "agent step failed",
                },
                error: "agent step failed",
              })
            }
            expect(testBody).toMatchObject({
              inputs: { ticket: "Printer is offline" },
            })
            activeDevelopmentSession = {
              ...workflowDraftSession,
              status: "testing",
              last_test: {
                ...workflowDraftLastTest,
                status: "running",
              },
            }
            runs = [
              runningDraftWorkflowRun,
              ...runs.filter((run) => run.id !== "wr_draft"),
            ]
            completeDraftViaPolling = options.completeDraftViaPolling === true
            return json(route, {
              session: activeDevelopmentSession,
              result: {
                run_id: draftWorkflowRun.id,
                status: "running",
              },
            })
          }
          case "/api/workflows/development/publish":
            if (
              activeDevelopmentSession?.last_test?.status !== "succeeded" ||
              activeDevelopmentSession.last_test.draft_key !==
                currentDraftKey(activeDevelopmentSession)
            ) {
              return json(
                route,
                {
                  error:
                    "workflow draft must pass a current test run before publish",
                },
                409,
              )
            }
            activeDevelopmentSession = null
            if (
              !workflowDefinitions.some(
                (workflow) =>
                  workflow.ref === workflowDraftSession.target_workflow_ref,
              )
            ) {
              workflowDefinitions = [
                ...workflowDefinitions,
                supportTriageWorkflowDefinition,
              ]
            }
            return json(route, {
              workflow_ref: workflowDraftSession.target_workflow_ref,
              session: workflowDraftSession,
            })
          case "/api/workflows/run":
            expect(request.postDataJSON()).toMatchObject({
              async: true,
              ref: "workflows/support-triage.yml",
              inputs: { ticket: "Printer is offline" },
              session: "workflow:manual",
              delivery: {
                channel: "telegram",
                chat_id: "support",
              },
            })
            runs = [
              runningManualWorkflowRun,
              ...runs.filter((run) => run.id !== "wr_manual"),
            ]
            return json(route, {
              run_id: manualWorkflowRun.id,
              status: "running",
            })
          case "/api/workflows/runs/wr_test/retry":
            expect(request.postDataJSON()).toMatchObject({
              secrets: { token: "retry-token" },
            })
            runs = [
              retryWorkflowRun,
              ...runs.filter((run) => run.id !== "wr_retry"),
            ]
            return json(route, {
              run_id: retryWorkflowRun.id,
              status: retryWorkflowRun.status,
            })
          case "/api/workflows/revalidate":
            workflowsRevalidated = true
            return json(route, compatibilityResponse())
          case "/api/workflows/compatibility":
            return json(route, compatibilityResponse())
          case "/api/workflows/reload":
            return json(route, {
              reloaded_at: "2026-07-16T12:00:00Z",
              workflows: workflowDefinitions,
              errors: [],
            })
          default:
            return json(route, { status: "ok" })
        }
      }

      if (method !== "GET") {
        return json(route, { status: "ok" })
      }

      switch (path) {
        case "/api/auth/status":
          return json(route, { authenticated: true, initialized: true })
        case "/api/gateway/status":
          return json(route, {
            gateway_status: "stopped",
            gateway_start_allowed: true,
            gateway_restart_required: false,
            boot_default_model: "gpt-4o-mini",
            config_default_model: "gpt-4o-mini",
          })
        case "/api/gateway/logs":
          return json(route, { logs: [], log_total: 0, log_run_id: 1 })
        case "/api/channels/catalog":
          return json(route, channelCatalogResponse)
        case "/api/config":
          return json(route, {
            channels: {
              telegram: { enabled: true },
              discord: { enabled: false },
            },
          })
        case "/api/models":
          return json(route, modelResponse)
        case "/api/models/catalog":
          return json(route, { entries: [], total: 0 })
        case "/api/oauth/providers":
          return json(route, { providers: options.oauthProviders ?? [] })
        case "/api/sessions":
          return json(route, [])
        case "/api/tools":
          return json(route, toolsResponse)
        case "/api/git-workspaces":
          return json(route, gitWorkspaceResponse)
        case "/api/workflows":
          return json(route, {
            workflows: options.nullableWorkflowPayloads
              ? null
              : workflowDefinitions,
            compatibility: options.nullableWorkflowPayloads
              ? {
                  ...compatibilityResponse(),
                  workflows: null,
                  counts: null,
                }
              : compatibilityResponse(),
          })
        case "/api/workflows/compatibility":
          return json(
            route,
            options.nullableWorkflowPayloads
              ? {
                  ...compatibilityResponse(),
                  workflows: null,
                  counts: null,
                }
              : compatibilityResponse(),
          )
        case "/api/workflows/development":
          return json(route, { session: activeDevelopmentSession })
        case "/api/workflows/runs":
          if (completeDraftViaPolling) {
            activeDevelopmentSession = {
              ...workflowDraftSession,
              status: "ready_to_publish",
              last_test: workflowDraftLastTest,
            }
            runs = [
              draftWorkflowRun,
              ...runs.filter((run) => run.id !== "wr_draft"),
            ]
            completeDraftViaPolling = false
          }
          return json(route, { runs })
        case "/api/workflows/runs/wr_nulls":
          return json(route, nullableWorkflowRun)
        case "/api/workflows/runs/wr_test":
          return json(route, workflowRun)
        case "/api/workflows/runs/wr_retry":
          return json(route, retryWorkflowRun)
        case "/api/workflows/runs/wr_draft":
          return json(route, draftWorkflowRun)
        case "/api/workflows/runs/wr_draft_failed":
          return json(route, failedDraftWorkflowRun)
        case "/api/workflows/runs/wr_manual":
          return json(route, manualWorkflowRun)
        case "/api/workflows/runs/wr_test/events":
          return json(route, {
            run_id: "wr_test",
            events: [
              {
                time: "2026-07-16T12:00:00Z",
                kind: "workflow.run.start",
                run_id: "wr_test",
              },
              {
                time: "2026-07-16T12:00:01Z",
                kind: "workflow.run.end",
                run_id: "wr_test",
              },
            ],
          })
        case "/api/workflows/runs/wr_nulls/events":
          return json(route, {
            run_id: "wr_nulls",
            events: null,
          })
        case "/api/workflows/runs/wr_retry/events":
          return json(route, {
            run_id: "wr_retry",
            events: [
              {
                time: "2026-07-16T12:00:02Z",
                kind: "workflow.run.start",
                run_id: "wr_retry",
              },
              {
                time: "2026-07-16T12:00:03Z",
                kind: "workflow.run.end",
                run_id: "wr_retry",
                payload: {
                  result: "retry event",
                },
              },
            ],
          })
        case "/api/workflows/runs/wr_draft/events":
        case "/api/workflows/runs/wr_draft_failed/events":
        case "/api/workflows/runs/wr_manual/events": {
          const runID = path.split("/")[4]
          const eventResult =
            runID === "wr_manual"
              ? "manual event"
              : runID === "wr_draft_failed"
                ? "draft failure event"
                : "draft event"
          return json(route, {
            run_id: runID,
            events: [
              {
                time: "2026-07-16T12:00:00Z",
                kind: "workflow.run.start",
                run_id: runID,
                payload: {
                  source: "dashboard",
                },
              },
              {
                time: "2026-07-16T12:00:01Z",
                kind: "workflow.run.end",
                run_id: runID,
                job_id: "triage",
                step_id: "summarize",
                message: "Workflow completed",
                payload: {
                  result: eventResult,
                },
              },
            ],
          })
        }
        case "/api/workflows/runs/wr_test/events/stream":
          return sse(route, [
            {
              time: "2026-07-16T12:00:02Z",
              kind: "workflow.run.end",
              run_id: "wr_test",
              payload: {
                streamed: "test stream",
              },
            },
          ])
        case "/api/workflows/runs/wr_nulls/events/stream":
          return sse(route, [])
        case "/api/workflows/runs/wr_retry/events/stream":
          return sse(route, [
            {
              time: "2026-07-16T12:00:04Z",
              kind: "workflow.run.end",
              run_id: "wr_retry",
              payload: {
                streamed: "retry stream",
              },
            },
          ])
        case "/api/workflows/runs/wr_draft/events/stream":
        case "/api/workflows/runs/wr_draft_failed/events/stream":
        case "/api/workflows/runs/wr_manual/events/stream": {
          const runID = path.split("/")[4]
          const streamResult =
            runID === "wr_manual"
              ? "manual stream"
              : runID === "wr_draft_failed"
                ? "draft failure stream"
                : "draft stream"
          if (runID === "wr_draft") {
            activeDevelopmentSession = {
              ...workflowDraftSession,
              status: "ready_to_publish",
              last_test: workflowDraftLastTest,
            }
            runs = [
              draftWorkflowRun,
              ...runs.filter((run) => run.id !== "wr_draft"),
            ]
          } else if (runID === "wr_manual") {
            runs = [
              manualWorkflowRun,
              ...runs.filter((run) => run.id !== "wr_manual"),
            ]
          }
          return sse(route, [
            {
              time: "2026-07-16T12:00:02Z",
              kind: "workflow.step.end",
              run_id: runID,
              job_id: "triage",
              step_id: "summarize",
              payload: {
                streamed: streamResult,
              },
            },
            {
              time: "2026-07-16T12:00:03Z",
              kind: "workflow.run.end",
              run_id: runID,
              payload: {
                streamed: streamResult,
              },
            },
          ])
        }
        case "/api/workflows/runs/wr_test/graph":
          return json(route, {
            run_id: "wr_test",
            nodes: [
              {
                id: "wr_test",
                workflow_ref: "workflows/summarize-text.yml",
                status: "succeeded",
              },
            ],
            edges: [],
          })
        case "/api/workflows/runs/wr_nulls/graph":
          return json(route, {
            run_id: "wr_nulls",
            nodes: null,
            edges: null,
          })
        case "/api/workflows/runs/wr_retry/graph":
          return json(route, {
            run_id: "wr_retry",
            nodes: [
              {
                id: "wr_retry",
                workflow_ref: retryWorkflowRun.workflow_ref,
                status: retryWorkflowRun.status,
                retry_of_run_id: "wr_test",
              },
            ],
            edges: [
              {
                from: "wr_test",
                to: "wr_retry",
                kind: "retry",
              },
            ],
          })
        case "/api/workflows/runs/wr_draft/graph":
        case "/api/workflows/runs/wr_draft_failed/graph":
        case "/api/workflows/runs/wr_manual/graph": {
          const runID = path.split("/")[4]
          const run = runByID(runID)
          return json(route, {
            run_id: runID,
            nodes: [
              {
                id: runID,
                workflow_ref: run.workflow_ref,
                status: run.status,
              },
            ],
            edges: [],
          })
        }
        case "/api/tools/web-search-config":
          return json(route, webSearchConfigResponse)
        case "/api/skills":
          return json(route, skillsResponse)
        case "/api/skills/search":
          return json(route, {
            results: [],
            limit: Number(url.searchParams.get("limit") ?? 20),
            offset: Number(url.searchParams.get("offset") ?? 0),
            has_more: false,
          })
        case "/api/system/autostart":
          return json(route, {
            enabled: false,
            supported: true,
            platform: "linux",
          })
        case "/api/system/launcher-config":
          return json(route, {
            port: 18800,
            public: false,
            allowed_cidrs: [],
            allow_localhost_bypass: true,
            trusted_proxy_cidrs: [],
          })
        case "/api/system/version":
          return json(route, {
            version: "test",
            git_commit: "test",
            build_time: "test",
            go_version: "go1.25",
          })
        default:
          return json(route, {})
      }
    },
  )
}

async function json(route: Route, body: unknown, status = 200) {
  await route.fulfill({
    status,
    contentType: "application/json",
    body: JSON.stringify(body),
  })
}

async function sse(route: Route, events: Array<Record<string, unknown>>) {
  await route.fulfill({
    status: 200,
    contentType: "text/event-stream",
    body: events
      .map(
        (event) => `event: ${event.kind}\ndata: ${JSON.stringify(event)}\n\n`,
      )
      .join(""),
  })
}

function collectPageErrors(page: Page) {
  const errors: string[] = []
  page.on("console", (message) => {
    if (message.type() === "error") {
      errors.push(message.text())
    }
  })
  page.on("pageerror", (error) => {
    errors.push(error.message)
  })
  return errors
}

async function expectNoHorizontalOverflow(page: Page) {
  const hasHorizontalOverflow = await page.evaluate(() => {
    const doc = document.documentElement
    const body = document.body
    const scrollWidth = Math.max(doc.scrollWidth, body.scrollWidth)
    const clientWidth = Math.max(doc.clientWidth, window.innerWidth)
    return scrollWidth > clientWidth + 1
  })

  expect(hasHorizontalOverflow).toBe(false)
}

async function expectElementFitsViewport(
  page: Page,
  selector: string,
  label: string,
) {
  const fits = await page.locator(selector).evaluate((element) => {
    const rect = element.getBoundingClientRect()
    const tolerance = 1
    return (
      rect.left >= -tolerance &&
      rect.top >= -tolerance &&
      rect.right <= window.innerWidth + tolerance &&
      rect.bottom <= window.innerHeight + tolerance
    )
  })

  expect(fits, `${label} should fit in the viewport`).toBe(true)
}

async function expectNoSeriousA11yViolations(page: Page) {
  const results = await new AxeBuilder({ page })
    .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"])
    .analyze()
  const blocking = results.violations.filter(
    (violation) =>
      violation.impact === "serious" || violation.impact === "critical",
  )

  expect(
    blocking.map((violation) => ({
      id: violation.id,
      impact: violation.impact,
      targets: violation.nodes.map((node) => node.target.join(" ")),
    })),
  ).toEqual([])
}

async function gotoMockedRoute(
  page: Page,
  routePath: string,
  options?: MockLauncherApiOptions,
) {
  await page.addInitScript(() => {
    window.localStorage.setItem(
      "picoclaw-tour-state",
      JSON.stringify({ currentStep: "completed", isActive: false }),
    )
  })
  await mockLauncherApis(page, options)
  await page.goto(routePath)
  await expect(page.getByRole("banner")).toBeVisible()
  await expect(page.locator("main")).toBeVisible()
}

for (const routePath of smokeRoutes) {
  test(`${routePath} renders without console errors or horizontal overflow`, async ({
    page,
  }) => {
    const errors = collectPageErrors(page)

    await gotoMockedRoute(page, routePath)
    await expect(page.getByRole("button").first()).toBeVisible()
    await page.waitForTimeout(500)
    await expectNoHorizontalOverflow(page)
    await expectNoSeriousA11yViolations(page)
    expect(errors).toEqual([])
  })
}

test("accounts page lists registered accounts and opens onboarding", async ({
  page,
}) => {
  const errors = collectPageErrors(page)

  await gotoMockedRoute(page, "/accounts", {
    oauthProviders: [
      {
        provider: "openai",
        credential_id: "openai",
        display_name: "OpenAI",
        methods: ["browser", "device_code", "token"],
        logged_in: true,
        status: "connected",
        credentials: [
          {
            provider: "openai",
            credential_id: "openai:work",
            display_name: "OpenAI",
            methods: ["browser", "device_code", "token"],
            logged_in: true,
            status: "connected",
            auth_method: "oauth",
            account_id: "acc_123",
          },
        ],
      },
      {
        provider: "anthropic",
        credential_id: "anthropic",
        display_name: "Anthropic",
        methods: ["token"],
        logged_in: false,
        status: "not_logged_in",
        credentials: [],
      },
    ],
  })

  await expect(page.getByRole("heading", { name: "work" })).toBeVisible()
  await expect(page.getByText("openai:work")).toBeVisible()
  await expect(page.getByText("Anthropic")).not.toBeVisible()

  await page.getByRole("button", { name: "Add Account" }).first().click()
  await expect(
    page.getByRole("dialog", { name: "Onboard Account" }),
  ).toBeVisible()
  await expect(page.getByPlaceholder("work")).toBeVisible()
  expect(errors).toEqual([])
})

test("model catalog dialog fits the viewport", async ({ page }) => {
  const errors = collectPageErrors(page)

  await gotoMockedRoute(page, "/models")
  await page.getByRole("button", { name: "Saved Catalogs" }).click()

  await expect(
    page.getByRole("dialog", { name: "Saved Model Catalogs" }),
  ).toBeVisible()
  await expectElementFitsViewport(page, '[role="dialog"]', "model catalog")
  await expectNoHorizontalOverflow(page)
  await expectNoSeriousA11yViolations(page)
  expect(errors).toEqual([])
})

test("account router editor supports block fallback graph editing", async ({
  page,
}) => {
  const errors = collectPageErrors(page)

  await gotoMockedRoute(page, "/accounts", {
    oauthProviders: [
      {
        provider: "openai",
        credential_id: "openai",
        display_name: "OpenAI",
        methods: ["browser", "device_code", "token"],
        logged_in: true,
        status: "connected",
        auth_method: "oauth",
        account_id: "acct-primary",
        credentials: [
          {
            provider: "openai",
            credential_id: "openai",
            display_name: "OpenAI",
            methods: ["browser", "device_code", "token"],
            logged_in: true,
            status: "connected",
            auth_method: "oauth",
            account_id: "acct-primary",
          },
          {
            provider: "openai",
            credential_id: "openai:backup",
            display_name: "OpenAI",
            methods: ["browser", "device_code", "token"],
            logged_in: true,
            status: "connected",
            auth_method: "oauth",
            account_id: "acct-backup",
          },
        ],
      },
    ],
  })
  await page.getByRole("button", { name: "Account Router" }).click()

  await expect(page).toHaveURL(/\/accounts\/account-router\/new$/)
  await expect(
    page.getByRole("heading", { name: "Create Account Router" }),
  ).toBeVisible()
  await expect(
    page.getByText("Add an account or load balancer block to start."),
  ).toHaveCount(2)
  await expect(page.getByText("No accounts connected.")).toBeVisible()

  await page.getByRole("button", { name: "Add Account" }).click()
  await page.getByRole("combobox", { name: "Account" }).click()
  await page.getByRole("option", { name: "OpenAI: acct-primary" }).click()

  await page.getByRole("button", { name: "Add Load Balancer" }).click()
  await page.getByRole("button", { name: "OpenAI: acct-backup" }).click()

  await page.getByRole("button", { name: "account-1", exact: true }).click()
  await page.getByRole("combobox", { name: "Fallback Connection" }).click()
  await page.getByRole("option", { name: "load-balancer-1" }).click()

  await expect(page.getByText("Fallback -> load-balancer-1")).toBeVisible()
  await page.getByRole("button", { name: "Pile fallback chain" }).click()
  await page.getByRole("combobox", { name: "Scale" }).click()
  await page.getByRole("option", { name: "125%" }).click()
  await expect(page.getByRole("combobox", { name: "Scale" })).toContainText(
    "125%",
  )

  if ((page.viewportSize()?.width ?? 0) >= 700) {
    const canvas = page.locator('svg[aria-label="Router Diagram"]')
    const world = page.locator('svg[aria-label="Router Diagram"] > g')
    const canvasBox = await canvas.boundingBox()
    expect(canvasBox).not.toBeNull()

    const loadBalancerNode = page.getByRole("button", {
      name: "Edit block load-balancer-1",
    })
    const beforeDragTransform = await loadBalancerNode.evaluate((node) =>
      node.getAttribute("transform"),
    )
    const loadBalancerBox = await loadBalancerNode.boundingBox()
    expect(loadBalancerBox).not.toBeNull()
    await page.mouse.move(loadBalancerBox!.x + 24, loadBalancerBox!.y + 24)
    await page.mouse.down()
    await page.mouse.move(loadBalancerBox!.x + 96, loadBalancerBox!.y + 72)
    await page.mouse.up()
    await expect
      .poll(() =>
        loadBalancerNode.evaluate((node) => node.getAttribute("transform")),
      )
      .not.toBe(beforeDragTransform)

    await canvas.evaluate((element) => {
      element.dispatchEvent(
        new WheelEvent("wheel", {
          bubbles: true,
          cancelable: true,
          deltaY: -240,
          shiftKey: true,
        }),
      )
    })
    await expect(page.getByRole("combobox", { name: "Scale" })).toContainText(
      "150%",
    )

    const beforePanTransform = await world.evaluate((node) =>
      node.getAttribute("transform"),
    )
    await page.mouse.move(
      canvasBox!.x + canvasBox!.width - 36,
      canvasBox!.y + 36,
    )
    await page.mouse.down()
    await page.mouse.move(
      canvasBox!.x + canvasBox!.width - 116,
      canvasBox!.y + 92,
    )
    await page.mouse.up()
    await expect
      .poll(() => world.evaluate((node) => node.getAttribute("transform")))
      .not.toBe(beforePanTransform)
  }

  await expect(page.getByRole("button", { name: "Raw JSON" })).toBeVisible()
  await expectNoHorizontalOverflow(page)
  await expectNoSeriousA11yViolations(page)
  expect(errors).toEqual([])
})

test("skill import dialog fits the viewport", async ({ page }) => {
  const errors = collectPageErrors(page)

  await gotoMockedRoute(page, "/agent/skills")
  await page.getByRole("button", { name: "Import Skill" }).click()

  await expect(
    page.getByRole("dialog", { name: "Import Into Workspace" }),
  ).toBeVisible()
  await expectElementFitsViewport(page, '[role="dialog"]', "skill import")
  await expectNoHorizontalOverflow(page)
  await expectNoSeriousA11yViolations(page)
  expect(errors).toEqual([])
})

test("web-search provider settings expand without overflow", async ({
  page,
}) => {
  const errors = collectPageErrors(page)

  await gotoMockedRoute(page, "/agent/tools")
  await page.getByRole("button", { name: "Web Search" }).click()
  await expect(page.getByRole("heading", { name: "Web Search" })).toBeVisible()

  await page.getByRole("button", { name: /OpenAI/ }).click()
  await expect(page.getByText("Max Results")).toBeVisible()
  await expectNoHorizontalOverflow(page)
  await expectNoSeriousA11yViolations(page)
  expect(errors).toEqual([])
})

test("workflow dashboard tolerates null persisted collections", async ({
  page,
}) => {
  const errors = collectPageErrors(page)

  await page.addInitScript(() => {
    window.localStorage.setItem(
      "picoclaw-tour-state",
      JSON.stringify({ currentStep: "completed", isActive: false }),
    )
  })
  await mockLauncherApis(page, { nullableWorkflowPayloads: true })
  await page.goto("/agent/workflows")

  await expect(page.getByRole("banner")).toBeVisible()
  await expect(page.locator("main")).toBeVisible()
  await page.getByRole("button", { name: "Operate" }).click()
  await expect(page.getByText("wr_nulls").first()).toBeVisible()
  await expect(page.getByText("No events")).toBeVisible()
  await expect(page.getByText("No graph")).toBeVisible()
  await expectNoHorizontalOverflow(page)
  await expectNoSeriousA11yViolations(page)
  expect(errors).toEqual([])
})

test("workflow dashboard supports AI draft, publish, and manual run loop", async ({
  page,
}) => {
  const errors = collectPageErrors(page)

  await gotoMockedRoute(page, "/agent/workflows")
  await expect(
    page
      .locator(
        '[title="workflow must be revalidated after the current Picoclaw version change"]',
      )
      .first(),
  ).toBeAttached()
  await expect(page.getByRole("button", { name: "AI Review" })).toBeVisible()
  await page.getByRole("button", { name: "AI Review" }).click()
  await expect(page.getByText("Workflow YAML")).toBeVisible()
  await expect(page.getByRole("textbox", { name: "AI brief" })).toHaveValue(
    /Review this workflow against the current PicoClaw runtime/,
  )
  await expect(page.getByText("version revalidation")).toBeVisible()
  await page.getByRole("button", { name: "Discard" }).click()
  await expect(page.getByText("New workflow")).toBeVisible()

  await page.getByRole("button", { name: "Operate" }).click()
  await expect(page.getByText("Run workflow").first()).toBeVisible()
  await page.getByRole("button", { name: "Run workflow" }).first().click()
  await expect(
    page.getByText("Revalidate this workflow before running it."),
  ).toBeVisible()
  await expect(
    page.getByRole("button", { name: "Run workflow" }).last(),
  ).toBeDisabled()
  await page.keyboard.press("Escape")
  await expect(page.getByRole("button", { name: "Retry" })).toBeDisabled()
  await expect(page.getByRole("button", { name: "Retry" })).toHaveAttribute(
    "title",
    "Revalidate this workflow before retrying the run.",
  )
  await page.getByRole("button", { name: "Revalidate" }).click()
  await page.getByRole("button", { name: "Run workflow" }).first().click()
  await expect(page.getByText("Ready to run.")).toBeVisible()
  await expect(
    page.getByRole("button", { name: "Run workflow" }).last(),
  ).toBeEnabled()
  await page.keyboard.press("Escape")
  const retrySecrets = page.getByRole("textbox", {
    name: "Retry secrets JSON",
  })
  await expect(retrySecrets).toBeVisible()
  await retrySecrets.fill("{")
  await expect(page.getByText(/Retry secrets JSON is invalid/)).toBeVisible()
  await expect(page.getByRole("button", { name: "Retry" })).toBeDisabled()
  await retrySecrets.fill('{"token":"retry-token"}')
  await expect(page.getByText("Ready to retry.")).toBeVisible()
  await expect(page.getByRole("button", { name: "Retry" })).toBeEnabled()
  await page.getByRole("button", { name: "Retry" }).click()
  await expect(page.getByText("wr_retry").first()).toBeVisible()
  await expect(page.getByText("retry summary").first()).toBeVisible()
  await expect(page.getByText('"result": "retry event"')).toBeVisible()
  await expect(page.getByText('"streamed": "retry stream"')).toBeVisible()
  await page.getByRole("button", { name: "Develop" }).click()

  await expect(
    page.getByText("Describe the workflow outcome before starting."),
  ).toBeVisible()
  await page
    .getByPlaceholder("Describe the workflow outcome")
    .fill("Triage support tickets")
  await expect(
    page.getByText(
      "Ready to start. One workflow draft can be active at a time.",
    ),
  ).toBeVisible()
  await page.getByRole("button", { name: "Start with AI" }).click()

  await expect(page.getByText("Workflow YAML")).toBeVisible()
  await expect(page.getByText("Only active draft")).toBeVisible()
  await expect(page.getByText("Publish readiness")).toBeVisible()
  const yamlEditor = page.getByRole("textbox", { name: "Workflow YAML" })
  const localDraftYAML = `${workflowDraftYAML}# local edit\n`
  await yamlEditor.fill(localDraftYAML)
  const developmentRefresh = page.waitForResponse(
    (response) =>
      response.url().endsWith("/api/workflows/development") &&
      response.request().method() === "GET",
  )
  await page.getByRole("button", { name: "Refresh" }).click()
  await developmentRefresh
  await expect(yamlEditor).toHaveValue(localDraftYAML)
  await yamlEditor.fill(workflowDraftYAML)
  await expect(page.getByRole("button", { name: "Test Draft" })).toBeEnabled()
  await page.locator("#workflow-test-inputs").fill("{")
  await expect(page.getByText(/Inputs JSON is invalid/)).toBeVisible()
  await expect(page.getByRole("button", { name: "Test Draft" })).toBeDisabled()
  await expect(page.getByRole("button", { name: "Publish" })).toBeDisabled()
  await expect(
    page.getByText("Run a successful draft test before publishing."),
  ).toBeVisible()

  await page
    .locator("#workflow-test-inputs")
    .fill('{"ticket":"Trigger failure"}')
  await page.locator("#workflow-test-session").fill("workflow:draft")
  await page
    .locator("#workflow-test-delivery")
    .fill('{"channel":"telegram","chat_id":"support"}')
  await page.getByRole("button", { name: "Test Draft" }).click()
  await expect(page.getByText("wr_draft_failed")).toBeVisible()
  await expect(page.getByText("agent step failed").first()).toBeVisible()
  await expect(page.getByRole("button", { name: "Fix With AI" })).toBeVisible()
  await page.getByRole("button", { name: "Fix With AI" }).click()
  await expect(page.getByRole("textbox", { name: "AI brief" })).toHaveValue(
    /Last draft test failed/,
  )

  await page
    .locator("#workflow-test-inputs")
    .fill('{"ticket":"Printer is offline"}')
  await page.getByRole("button", { name: "Test Draft" }).click()
  await expect(page.getByText("wr_draft", { exact: true })).toBeVisible()
  await expect(page.getByRole("button", { name: "Publish" })).toBeEnabled()
  await expect(page.getByText("Ready to publish.")).toBeVisible()

  await page.reload()
  await expect(page.getByText("Workflow YAML")).toBeVisible()
  await expect(page.getByText("wr_draft", { exact: true })).toBeVisible()
  await expect(page.getByRole("button", { name: "Publish" })).toBeEnabled()
  await expect(page.getByText("Ready to publish.")).toBeVisible()

  await page.getByRole("button", { name: "Open Run" }).click()
  await expect(page.getByText("draft summary").first()).toBeVisible()
  await expect(page.getByText('"request_id": "req_draft"')).toBeVisible()
  await expect(page.getByText('"result": "draft event"')).toBeVisible()
  await expect(
    page.getByText('"streamed": "draft stream"').first(),
  ).toBeVisible()

  await page.getByRole("button", { name: "Develop" }).click()
  await page.getByRole("button", { name: "Publish" }).click()

  await expect(page.getByText("Run workflow").first()).toBeVisible()
  await expect(page.locator("#workflow-run-selected-ref")).toHaveText(
    "workflows/support-triage.yml",
  )
  await page.getByRole("button", { name: "Run workflow" }).first().click()
  await expect(page.locator("#workflow-run-input-ticket")).toBeVisible()
  await expect(page.getByText('Input "ticket" is required.')).toBeVisible()
  await expect(
    page.getByRole("button", { name: "Run workflow" }).last(),
  ).toBeDisabled()
  await page.locator("#workflow-run-input-ticket").fill("Printer is offline")
  await expect(
    page.getByRole("button", { name: "Run workflow" }).last(),
  ).toBeEnabled()
  await page.getByRole("button", { name: "Advanced options" }).click()
  await page.locator("#workflow-run-session").fill("workflow:manual")
  await page
    .locator("#workflow-run-delivery")
    .fill('{"channel":"telegram","chat_id":"support"}')
  await page.getByRole("button", { name: "Run workflow" }).last().focus()
  await page.keyboard.press("Enter")

  await expect(page.getByText("wr_manual").first()).toBeVisible()
  await expect(page.getByText("manual summary").first()).toBeVisible()
  await expect(page.getByText('"request_id": "req_manual"')).toBeVisible()
  await expect(page.getByText('"topic_id": "manual-topic"')).toBeVisible()
  await expect(page.getByText('"result": "manual event"')).toBeVisible()
  await expect(
    page.getByText('"streamed": "manual stream"').first(),
  ).toBeVisible()
  await page
    .locator("[data-sonner-toaster]")
    .evaluateAll((toasters) => toasters.forEach((toast) => toast.remove()))
  await expectNoHorizontalOverflow(page)
  await expectNoSeriousA11yViolations(page)
  expect(errors).toEqual([])
})

test("workflow dashboard refreshes async draft status from polling without SSE", async ({
  page,
}) => {
  const errors = collectPageErrors(page)

  await page.addInitScript(() => {
    window.localStorage.setItem(
      "picoclaw-tour-state",
      JSON.stringify({ currentStep: "completed", isActive: false }),
    )
    Object.defineProperty(window, "EventSource", {
      configurable: true,
      value: undefined,
    })
  })
  await mockLauncherApis(page, { completeDraftViaPolling: true })
  await page.goto("/agent/workflows")
  await expect(page.getByRole("banner")).toBeVisible()
  await expect(page.locator("main")).toBeVisible()

  await page
    .getByPlaceholder("Describe the workflow outcome")
    .fill("Triage support tickets")
  await page.getByRole("button", { name: "Start with AI" }).click()
  await expect(page.getByText("Workflow YAML")).toBeVisible()
  await page
    .locator("#workflow-test-inputs")
    .fill('{"ticket":"Printer is offline"}')
  await page.locator("#workflow-test-session").fill("workflow:draft")
  await page
    .locator("#workflow-test-delivery")
    .fill('{"channel":"telegram","chat_id":"support"}')
  await page.getByRole("button", { name: "Test Draft" }).click()

  await expect(page.getByText("wr_draft", { exact: true })).toBeVisible()
  await expect(page.getByRole("button", { name: "Publish" })).toBeEnabled()
  await expect(page.getByText("Ready to publish.")).toBeVisible()
  expect(errors).toEqual([])
})

test("mobile sidebar opens, fits the viewport, and navigates", async ({
  page,
}, testInfo) => {
  test.skip(testInfo.project.name !== "mobile", "mobile-only interaction")
  const errors = collectPageErrors(page)

  await gotoMockedRoute(page, "/")
  await page.getByRole("button", { name: "Toggle Sidebar" }).click()

  const sidebar = page.getByRole("dialog", { name: "Sidebar" })
  await expect(sidebar).toBeVisible()
  await page.waitForTimeout(300)
  await expectElementFitsViewport(
    page,
    '[data-sidebar="sidebar"][data-mobile="true"]',
    "mobile sidebar",
  )
  await sidebar.getByRole("button", { name: "Services" }).click()
  await sidebar.getByRole("link", { name: /Models/ }).click()
  await expect(page).toHaveURL(/\/models$/)
  await expect(sidebar).toBeHidden()
  await expectNoHorizontalOverflow(page)
  await expectNoSeriousA11yViolations(page)
  expect(errors).toEqual([])
})
