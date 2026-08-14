import AxeBuilder from "@axe-core/playwright"
import { type Page, type Route, expect, test } from "@playwright/test"

import type {
  AgentCapabilitiesResponse,
  AgentInfo,
  AgentMutationInput,
  AgentsResponse,
} from "../src/api/agents"
import type {
  MCPConfigResponse,
  MCPServer,
  MCPServerInput,
} from "../src/api/mcp"

const smokeRoutes = [
  "/",
  "/models",
  "/accounts",
  "/events",
  "/event-sources",
  "/pull-requests",
  "/logs",
  "/agent/agents",
  "/agent/git-workspaces",
  "/agent/mcp",
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
    },
    {
      index: 2,
      model_name: "task-router",
      provider: "model-router",
      model: "task-router",
      api_key: "",
      enabled: true,
      available: true,
      status: "available",
      is_default: false,
      is_virtual: true,
      model_router: {
        name: "task-router",
        enabled: true,
        entry: "entry",
        blocks: [
          {
            id: "entry",
            type: "rules",
            fallback: "default-gpt-4o-mini",
            rules: [{ match: "has_code", target: "code-gpt-4o" }],
          },
          {
            id: "code-gpt-4o",
            type: "model",
            model: "code",
          },
          {
            id: "default-gpt-4o-mini",
            type: "model",
            model: "fast",
          },
        ],
      },
    },
  ],
  model_aliases: [
    {
      name: "code",
      model: "gpt-4o-mini",
      account_overrides: {
        "gpt-4o": "gpt-4o",
      },
    },
    {
      name: "fast",
      model: "gpt-4o-mini",
    },
  ],
  model_alias_catalog: [
    {
      name: "chat",
      description: "General discussion, planning, and technical writing.",
    },
    {
      name: "code",
      description: "Implementation, refactoring, debugging, and tests.",
    },
    {
      name: "investigate",
      description: "Deep research, root-cause analysis, and unfamiliar code.",
    },
    {
      name: "review",
      description: "Correctness, maintainability, and security review.",
    },
    {
      name: "fast",
      description:
        "Low-latency summaries, classification, and routine automation.",
    },
  ],
  total: 3,
  default_account_ref: "gpt-4o-mini",
  default_model: "code",
  provider_options: [
    {
      id: "openai",
      display_name: "OpenAI",
      default_api_base: "https://api.openai.com/v1",
      empty_api_key_allowed: false,
      create_allowed: true,
      supports_fetch: true,
    },
    {
      id: "gemini",
      display_name: "Google Gemini",
      default_api_base: "https://generativelanguage.googleapis.com/v1beta",
      empty_api_key_allowed: false,
      create_allowed: true,
    },
    {
      id: "deepseek",
      display_name: "DeepSeek",
      default_api_base: "https://api.deepseek.com/v1",
      empty_api_key_allowed: false,
      create_allowed: true,
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

const mcpResponse: MCPConfigResponse = {
  enabled: true,
  discovery: {
    enabled: false,
    ttl: 5,
    max_search_results: 5,
    use_bm25: true,
    use_regex: false,
  },
  servers: [
    {
      name: "github",
      enabled: true,
      deferred: null,
      type: "http",
      url: "https://mcp.example.test/github",
      command: "",
      args: [],
      env_file: "",
      env_keys: [],
      header_keys: [],
      auth: {
        type: "oauth",
        configured: true,
        expired: false,
      },
    },
    {
      name: "local-files",
      enabled: false,
      deferred: true,
      type: "stdio",
      url: "",
      command: "npx",
      args: ["-y", "@modelcontextprotocol/server-filesystem", "/workspace"],
      env_file: "",
      env_keys: ["FILESYSTEM_TOKEN"],
      header_keys: [],
      auth: {
        type: "none",
        configured: false,
        expired: false,
      },
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

const eventResponse = {
  id: "ev_0123456789abcdef0123456789abcdef",
  source: "github",
  connector: "triage",
  type: "issues.opened",
  actor: {
    id: "octocat",
    type: "user",
    display_name: "The Octocat",
  },
  subject: {
    id: "42",
    type: "issue",
    name: "Printer is offline",
    url: "https://example.test/issues/42",
  },
  occurred_at: "2026-07-16T11:59:59Z",
  received_at: "2026-07-16T12:00:00Z",
  attributes: {
    body_authenticated: "true",
    signature_algorithm: "hmac-sha256",
  },
  payload_bytes: 84,
  routing: {
    status: "succeeded",
    available_at: "2026-07-16T12:00:00Z",
    attempts: 1,
    updated_at: "2026-07-16T12:00:01Z",
  },
}

const eventDispatchResponse = {
  id: "dsp_0123456789abcdef0123456789abcdef",
  event_id: eventResponse.id,
  workflow_ref: "workflows/github-issue-triage.yml",
  workflow_revision: "sha256:0123456789abcdef",
  run_id: "wr_smoke",
  status: "succeeded",
  available_at: "2026-07-16T12:00:00Z",
  attempts: 1,
  created_at: "2026-07-16T12:00:00Z",
  updated_at: "2026-07-16T12:00:02Z",
  linked_at: "2026-07-16T12:00:01Z",
  finished_at: "2026-07-16T12:00:02Z",
}

const eventPayloadText =
  '{"issue":42,"estimate":9007199254740993,"title":"Printer is offline"}'

const replayEventID = "ev_fedcba9876543210fedcba9876543210"

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

const toolAdaptationResponse = {
  enabled: true,
  visible_tool_surface: "auto",
  learn_from_tool_calls: true,
  run_model_probes: true,
  allow_runtime_downgrade: "auto",
  allow_runtime_promotion: "auto",
  apply_visible_changes: "next_session",
  cache_sensitive_apis: "auto",
  cache_breaking_downgrade: false,
  profile_overrides: [
    {
      provider: "openai",
      model:
        "very-long-model-name-with-reasoning-context-and-tool-capabilities",
      visible_tool_surface: "simple",
      cache_sensitive_apis: "never",
    },
  ],
  profiles: [
    {
      id: "openai/gpt-4o-mini",
      label: "gpt-4o-mini",
      source: "model alias",
      is_default: true,
      is_override: false,
      probe_available: true,
      resolved: {
        provider: "openai",
        model: "gpt-4o-mini",
        state_path: "/tmp/tool-adaptation-state.json",
        visible_tool_surface: "codex",
        pinned_tool_surface: "codex",
        surface_evidence: "heuristic",
        runtime_downgrade: false,
        runtime_promotion: false,
        apply_visible_changes: "next_session",
        cache_sensitive: true,
        cache_evidence: "heuristic",
      },
    },
    {
      id: "openai/very-long-model-name-with-reasoning-context-and-tool-capabilities",
      label:
        "very-long-model-name-with-reasoning-context-and-tool-capabilities",
      source:
        "manual override for a configured provider profile with a long label",
      is_default: false,
      is_override: true,
      probe_available: false,
      resolved: {
        provider: "openai",
        model:
          "very-long-model-name-with-reasoning-context-and-tool-capabilities",
        state_path: "/tmp/tool-adaptation-state.json",
        visible_tool_surface: "simple",
        pinned_tool_surface: "simple",
        surface_evidence: "config",
        runtime_downgrade: true,
        runtime_promotion: true,
        apply_visible_changes: "next_session",
        cache_sensitive: false,
        cache_evidence: "config",
      },
    },
  ],
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

const lifecycleEventID = "ev_0123456789abcdef0123456789abcdef"
const lifecycleDispatchID = "dsp_0123456789abcdef0123456789abcdef"
const lifecycleDecoyEventID = "ev_fedcba9876543210fedcba9876543210"
const lifecycleDecoyDispatchID = "dsp_fedcba9876543210fedcba9876543210"

const lifecycleWorkflowRun = {
  ...workflowRun,
  id: "wr_lifecycle",
  workflow_ref: "workflows/github-issue-triage.yml",
  origin: {
    kind: "external_event",
    event_id: lifecycleEventID,
    dispatch_id: lifecycleDispatchID,
    root_run_id: "wr_lifecycle_root",
  },
  event: {
    id: lifecycleDecoyEventID,
    source: "github",
    connector: "primary",
    type: "issues.opened",
  },
  inputs: {
    event_id: lifecycleDecoyEventID,
    dispatch_id: lifecycleDecoyDispatchID,
  },
  session: `event:${lifecycleDecoyEventID}:dispatch:${lifecycleDecoyDispatchID}`,
}

const cancelableWorkflowRun = {
  ...workflowRun,
  id: "wr_cancel",
  status: "running",
  outputs: {},
  jobs: {},
  steps: {},
  updated_at: "2026-07-16T12:04:00Z",
  completed_at: undefined,
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

const workflowEventDraftYAML = `name: Support Triage
on:
  workflow_call:
    inputs:
      ticket:
        type: string
        required: true
  event:
    sources:
      - github
    types:
      - issues.opened
jobs:
  triage:
    runs-on: picoclaw
    steps:
      - id: summarize
        uses: agent/main
        with:
          prompt: Summarize support tickets
`

const workflowInspectionSecretCanary =
  "ui-smoke-workflow-secret-must-not-render"
const workflowInspectionRawYAMLCanary =
  "name: ui-smoke-raw-workflow-yaml-must-not-render"

type MockWorkflowInspectionSource =
  | { kind: "published"; ref: string }
  | { kind: "template"; template_name: string }

function workflowDefinitionInspection(source: MockWorkflowInspectionSource) {
  const inspection = {
    source,
    revision:
      "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
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
      event: {
        present: true,
        projected: true,
        value: {
          sources: ["github"],
          types: ["issues.opened"],
        },
      },
      workflow_call: { present: false, projected: true },
    },
    jobs: [
      {
        id: "review",
        kind: "steps",
        steps: [
          {
            index: 0,
            id: "analyze",
            kind: "agent",
            target: "agent/main",
          },
        ],
      },
    ],
    dependencies: [{ kind: "agent", target: "main", occurrences: 1 }],
    effects: [
      {
        kind: "model_or_delegated_action_possible",
        target: "main",
        occurrences: 1,
      },
    ],
    limits: [],
  }

  const serialized = JSON.stringify(inspection)
  expect(serialized).not.toContain(workflowInspectionSecretCanary)
  expect(serialized).not.toContain(workflowInspectionRawYAMLCanary)
  return inspection
}

const workflowCapabilityLongToolName = `z${"x".repeat(200)}`

function workflowAuthoringCapabilities() {
  return {
    complete: true,
    mcp_status: "ready",
    agents: [
      {
        id: "main",
        target: "agent/main",
        is_default: true,
        readiness: "ready",
      },
      {
        id: "reviewer",
        target: "agent/reviewer",
        is_default: false,
        readiness: "not_configured",
      },
    ],
    tools: [
      {
        name: "message",
        target: "tool/message",
        readiness: "ready",
        parameter_shape_projected: true,
        parameter_shape: {
          type: "object",
          properties: [
            {
              name: "channel",
              required: false,
              shape: { type: "string" },
            },
            {
              name: "text",
              required: true,
              shape: {
                type: "string",
                enum: ["brief", "full"],
              },
            },
          ],
          additional_properties: { allowed: false },
        },
      },
      {
        name: workflowCapabilityLongToolName,
        target: `tool/${workflowCapabilityLongToolName}`,
        readiness: "ready",
        parameter_shape_projected: true,
        parameter_shape: {},
      },
    ],
    mcp_tools: [
      {
        server: "github",
        tool: "create_issue",
        target: "mcp/github/create_issue",
        readiness: "ready",
        parameter_shape_projected: true,
        parameter_shape: {
          type: "object",
          additional_properties: {
            shape: { type: "string" },
          },
        },
      },
    ],
    functions: [
      {
        name: "git.diff",
        target: "function/git.diff",
        readiness: "ready",
      },
      {
        name: "git.filter",
        target: "function/git.filter",
        readiness: "ready",
      },
      {
        name: "git.inventory",
        target: "function/git.inventory",
        readiness: "ready",
      },
      {
        name: "workflow.artifact",
        target: "function/workflow.artifact",
        readiness: "ready",
      },
      {
        name: "workflow.state",
        target: "function/workflow.state",
        readiness: "ready",
      },
    ],
    limits: [],
  }
}

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
  session_revision: "opaque-session-revision",
  draft_revision: "opaque-draft-revision",
  base_target_revision: "opaque-base-target-revision",
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
  draft_revision: workflowDraftSession.draft_revision,
  target_workflow_ref: workflowDraftSession.target_workflow_ref,
  run_id: "wr_draft",
  status: "succeeded",
  tested_at: "2026-07-16T12:01:01Z",
}

type MockWorkflowDraftLastTest = typeof workflowDraftLastTest & {
  event_id?: string
}

type MockWorkflowDevelopmentSession = Omit<
  typeof workflowDraftSession,
  "last_test"
> & {
  source_workflow_ref?: string
  last_test?: MockWorkflowDraftLastTest
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

const prWorkspaceID = `prw_${"1".repeat(32)}`
const prWorkspaceCharterID = `pcr_${"2".repeat(32)}`
const prWorkspaceAggregate = {
  workspace: {
    id: prWorkspaceID,
    provider: "github",
    provider_origin: "https://github.com",
    repository_id: "100",
    repository: "octo/repo",
    pull_request_id: "200",
    pull_number: 42,
    phase: "completion_audit",
    execution_state: "waiting_user",
    active_charter_id: prWorkspaceCharterID,
    provider_head_sha: "b".repeat(40),
    version: 4,
    created_at: "2026-08-13T10:00:00Z",
    updated_at: "2026-08-13T10:05:00Z",
  },
  provider_snapshot: {
    provider: "github",
    provider_origin: "https://github.com",
    repository_id: "100",
    repository: "octo/repo",
    pull_request_id: "200",
    pull_number: 42,
    title: "Fix lost updates",
    body: "Keep optimistic concurrency intact.",
    author_id: "300",
    author_login: "octocat",
    authenticated_user_id: "300",
    base_ref: "main",
    base_sha: "a".repeat(40),
    head_repository_id: "100",
    head_ref: "fix/store",
    head_sha: "b".repeat(40),
    state: "open",
    owned: true,
    head_writable: true,
    can_review: true,
    can_create_issue: true,
    provider_revision: "github-etag-4",
    observed_at: "2026-08-13T10:00:00Z",
  },
  charters: [
    {
      id: prWorkspaceCharterID,
      revision: 1,
      type: "fix",
      goal: "Prevent lost updates.",
      acceptance_criteria: ["Concurrent writes conflict."],
      included_areas: ["pkg/store"],
      excluded_areas: ["Broad refactor"],
      non_goals: ["New storage engine"],
      base_sha: "a".repeat(40),
      head_sha: "b".repeat(40),
      confirmed: true,
      created_at: "2026-08-13T10:01:00Z",
      confirmed_at: "2026-08-13T10:02:00Z",
    },
  ],
  stage_runs: [
    {
      id: `psr_${"3".repeat(32)}`,
      stage: "review",
      state: "succeeded",
      charter_id: prWorkspaceCharterID,
      head_sha: "b".repeat(40),
      attempt: 1,
      summary: "Review completed after distinct coverage challenges.",
      started_at: "2026-08-13T10:03:00Z",
      finished_at: "2026-08-13T10:03:01Z",
    },
    {
      id: `psr_${"6".repeat(32)}`,
      stage: "implementation",
      state: "succeeded",
      charter_id: prWorkspaceCharterID,
      head_sha: "b".repeat(40),
      attempt: 1,
      summary: "Implemented the confirmed charter.",
      started_at: "2026-08-13T10:03:30Z",
      finished_at: "2026-08-13T10:03:59Z",
    },
    {
      id: `psr_${"4".repeat(32)}`,
      stage: "completion_audit",
      state: "succeeded",
      charter_id: prWorkspaceCharterID,
      head_sha: "b".repeat(40),
      attempt: 1,
      summary: "Implementation is complete within the charter.",
      started_at: "2026-08-13T10:04:00Z",
      finished_at: "2026-08-13T10:04:01Z",
    },
  ],
  findings: [],
  messages: [],
  corrections: [],
  repository_lessons: [],
  nudge_rounds: [
    {
      id: `pnr_${"5".repeat(32)}`,
      stage_run_id: `psr_${"3".repeat(32)}`,
      stage: "review",
      round: 1,
      minimum_rounds: 2,
      hard_cap: 5,
      strategy: "coverage_gaps",
      challenge: "Inspect unchecked callers.",
      variant_digest: "sha256:variant",
      prompt_digest: "sha256:prompt",
      state: "succeeded",
      novel_findings: 0,
      duplicate_count: 0,
      resolved_findings: 0,
      reward: 0.25,
      reward_provenance: "retained_open",
      created_at: "2026-08-13T10:04:00Z",
    },
  ],
  deferred_groups: [],
  repair_attempts: [
    {
      id: `pra_${"6".repeat(32)}`,
      stage_run_id: `psr_${"6".repeat(32)}`,
      number: 1,
      state: "succeeded",
      instruction: "Implement the confirmed charter.",
      candidate_sha: "c".repeat(40),
      scope: {
        distance: "S0_exact",
        size: "XS",
        presence: "candidate_present",
        files: 1,
        semantic_lines: 5,
        modules: 1,
        estimated: false,
        type_compatible: true,
        confidence: 1,
      },
      prompt_digest: "sha256:repair",
      started_at: "2026-08-13T10:03:30Z",
      finished_at: "2026-08-13T10:03:45Z",
    },
  ],
  validation_runs: [
    {
      id: `pvr_${"6".repeat(32)}`,
      stage_run_id: `psr_${"6".repeat(32)}`,
      state: "succeeded",
      candidate_sha: "c".repeat(40),
      checks: [{ id: "tests", name: "Tests", status: "passed" }],
      started_at: "2026-08-13T10:03:45Z",
      finished_at: "2026-08-13T10:03:50Z",
    },
  ],
  gates: [],
  publications: [],
  activity: [],
}

const prLifecycleGateProfiles = {
  gate_profiles: {
    default: {
      name: "Default",
      workflows: {
        "pr.review.complete": {
          id: "review_complete",
          name: "Review complete",
          purpose: "authorization",
          decision_point: "pr.review.complete",
          stages: [{ id: "automatic", kind: "zero" }],
        },
      },
    },
  },
  default_gate_profile_id: "default",
  repository_assignments: {},
  nudge: {
    review_minimum_additional: 2,
    review_maximum_additional: 5,
    completion_minimum_additional: 2,
    completion_maximum_additional: 5,
  },
  scope: {
    xs: { files: 1, semantic_lines: 20, modules: 1 },
    s: { files: 3, semantic_lines: 100, modules: 1 },
    m: { files: 10, semantic_lines: 500, modules: 3 },
  },
  deferred_issues: { mode: "ask" },
  catalog_revision: "sha256:catalog",
  config_revision: "sha256:config",
  effects: { gateway_effect: "applied" },
}

interface MockLauncherApiOptions {
  agentActivityRequests?: Array<{ method: string; path: string }>
  agentCapabilityRequests?: Array<{
    method: string
    path: string
    body: unknown
  }>
  agentRequests?: Array<{
    method: string
    path: string
    body: unknown
  }>
  completeDraftViaPolling?: boolean
  codexAccountLimits?: unknown
  fetchModelEmptyCredentials?: string[]
  fetchModelFailures?: Record<string, string>
  modelResponse?: unknown
  nullableWorkflowPayloads?: boolean
  oauthProviders?: unknown[]
  statefulAgents?: boolean
  statefulMCP?: boolean
  gatewayRunning?: boolean
  mcpRequests?: Array<{
    method: string
    path: string
    body: unknown
  }>
  workflowInspectionRequests?: Array<{
    method: string
    path: string
    body: unknown
  }>
  workflowCapabilityRequests?: Array<{
    method: string
    path: string
  }>
  workflowJobRequests?: Array<{
    method: string
    path: string
    body: unknown
  }>
  workflowDevelopmentYAML?: string
  workflowTriggerSimulationRequests?: Array<{
    method: string
    path: string
    body: Record<string, unknown>
  }>
  workflowTriggerExecutionRequests?: Array<{
    method: string
    path: string
    body: Record<string, unknown>
  }>
  workflowEventPayloadRequests?: string[]
  workflowCancelReasons?: string[]
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
  let reviseRequestCount = 0
  let currentMCPResponse = structuredClone(mcpResponse)
  let currentCancelableWorkflowRun = structuredClone(cancelableWorkflowRun)
  let currentAgentRevision = 1
  let currentCapabilityRevision = 1
  let currentDefaultAgentID = "main"
  let currentAgents: AgentInfo[] = [
    {
      id: "main",
      name: "Main",
      workspace: "",
      account_ref: "",
      model: null,
      skills: null,
      subagents: null,
      is_default: true,
      default_configured: options.statefulAgents === true,
      implicit: options.statefulAgents !== true,
    },
    {
      id: "reviewer",
      name: "Reviewer",
      workspace: "/workspace/reviewer",
      account_ref: "gpt-4o",
      model: {
        primary: "code",
        fallbacks: [],
      },
      skills: ["review-helper"],
      subagents: { allow_agents: ["main"] },
      is_default: false,
      default_configured: false,
      implicit: false,
    },
  ]

  const agentEffects = {
    launcher_effect: "applied",
    catalog_effect: "applied",
    gateway_effect: "applied",
  } as const

  let currentAgentCapabilities: AgentCapabilitiesResponse = {
    agent_id: "reviewer",
    source: "agent",
    editable: true,
    issue_code: "",
    legacy_upgrade_required: false,
    capabilities: {
      tools: {
        mode: "selected",
        values: ["web_search", "legacy_unknown"],
      },
      skills: {
        mode: "inherit",
        values: [],
        inherited_values: ["review-helper"],
      },
      mcp_servers: {
        mode: "all",
        values: [],
      },
    },
    catalogs: {
      tools: [
        {
          name: "web_search",
          description: "Search the web",
          category: "web",
          status: "enabled",
          reason_code: "",
        },
        {
          name: "filesystem",
          description: "Read approved workspace files",
          category: "workspace",
          status: "enabled",
          reason_code: "",
        },
      ],
      skills: [{ name: "review-helper", source: "workspace" }],
      mcp_servers: [{ name: "github", enabled: true }],
    },
    catalog_truncated: {
      tools: false,
      skills: false,
      mcp_servers: false,
    },
    revision: "capability-revision-1",
    config_revision: "agent-revision-1",
    effects: agentEffects,
  }

  function currentAgentsResponse(): AgentsResponse {
    return {
      agents: structuredClone(currentAgents),
      default_agent_id: currentDefaultAgentID,
      config_revision: `agent-revision-${currentAgentRevision}`,
      effects: agentEffects,
    }
  }

  function advanceAgentsRevision() {
    currentAgentRevision += 1
    return currentAgentsResponse()
  }

  function mcpServerFromInput(
    input: MCPServerInput,
    existing?: MCPServer,
  ): MCPServer {
    const envKeys = input.env_keys ?? Object.keys(input.env ?? {})
    const headerKeys = input.header_keys ?? Object.keys(input.headers ?? {})
    const authMode = input.auth_mode ?? existing?.auth.type ?? "none"
    let auth = existing?.auth ?? { type: "none", configured: false }
    if (authMode === "none") {
      auth = { type: "none", configured: false }
    } else if (authMode === "custom") {
      auth = { type: "custom", configured: headerKeys.length > 0 }
    } else if (authMode !== auth.type) {
      auth = { type: authMode, configured: false }
    }
    return {
      name: input.name,
      enabled: input.enabled,
      deferred: input.deferred,
      type: input.type,
      url: input.url ?? "",
      command: input.command ?? "",
      args: input.args ?? [],
      env_file: input.env_file ?? "",
      env_keys: envKeys,
      header_keys: headerKeys,
      auth,
    }
  }

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

  function eventTriggerRevision(yaml: string) {
    return `mock-revision:${normalizeWorkflowDraftYAML(yaml).length}`
  }

  function workflowTriggerInspection(yaml: string) {
    const eventTrigger = yaml.includes("\n  event:")
      ? {
          sources: ["github"],
          types: ["issues.opened"],
        }
      : null
    const workflowCall = yaml.includes("\n  workflow_call:")
      ? {
          inputs: {
            ticket: {
              type: "string",
              required: true,
            },
          },
        }
      : null
    const absent = { present: false, editable: true, value: null }
    return {
      revision: eventTriggerRevision(yaml),
      triggers: {
        manual: absent,
        schedule: absent,
        channel_message: absent,
        command: absent,
        runtime_event: absent,
        event:
          eventTrigger == null
            ? absent
            : { present: true, editable: true, value: eventTrigger },
        workflow_call:
          workflowCall == null
            ? absent
            : { present: true, editable: true, value: workflowCall },
      },
      validation: {
        valid: true,
        validated_at: "2026-07-16T12:00:00Z",
      },
    }
  }

  function workflowJobsRevision(yaml: string) {
    return `mock-jobs-revision:${normalizeWorkflowDraftYAML(yaml).length}`
  }

  function workflowJobsInspection(yaml: string) {
    const projectedName = yaml.match(/^# mock-step-name: (.*)$/m)?.[1]
    const absent = { present: false, value: null }
    return {
      revision: workflowJobsRevision(yaml),
      editable: true,
      complete: true,
      limits: [],
      jobs: [
        {
          id: "triage",
          index: 0,
          editable: true,
          advanced_fields_present: false,
          steps_present: true,
          fields: {
            name: absent,
            runs_on: { present: true, value: "picoclaw" },
            needs: absent,
            uses: absent,
            if: absent,
            continue_on_error: absent,
            with: absent,
            secrets: absent,
            outputs: absent,
            context: absent,
          },
          steps: [
            {
              index: 0,
              editable: true,
              advanced_fields_present: false,
              fields: {
                id: { present: true, value: "summarize" },
                name:
                  projectedName == null
                    ? absent
                    : { present: true, value: projectedName },
                uses: { present: true, value: "agent/main" },
                if: absent,
                continue_on_error: absent,
                with: {
                  present: true,
                  value: { prompt: "Summarize support tickets" },
                },
                context: absent,
              },
            },
          ],
        },
      ],
      validation: {
        valid: true,
        validated_at: "2026-07-16T12:00:00Z",
      },
    }
  }

  await page.route(
    (url) => url.pathname.startsWith("/api/"),
    async (route) => {
      const request = route.request()
      const url = new URL(request.url())
      const path = url.pathname
      const method = request.method()

      const capabilitiesMatch = path.match(
        /^\/api\/agents\/([^/]+)\/capabilities$/,
      )
      if (capabilitiesMatch) {
        const body =
          method === "PATCH"
            ? (request.postDataJSON() as Record<string, unknown>)
            : undefined
        options.agentCapabilityRequests?.push({ method, path, body })
        if (decodeURIComponent(capabilitiesMatch[1]) !== "reviewer") {
          return json(route, { error: "agent_not_found" }, 404)
        }
        if (method === "GET") {
          return json(route, structuredClone(currentAgentCapabilities))
        }
        if (
          method === "PATCH" &&
          body?.expected_revision === currentAgentCapabilities.revision
        ) {
          currentCapabilityRevision += 1
          currentAgentCapabilities = {
            ...currentAgentCapabilities,
            capabilities: {
              tools:
                (body.tools as AgentCapabilitiesResponse["capabilities"]["tools"]) ??
                currentAgentCapabilities.capabilities.tools,
              skills: body.skills
                ? {
                    ...(body.skills as {
                      mode: "inherit" | "none" | "selected"
                      values: string[]
                    }),
                    inherited_values:
                      currentAgentCapabilities.capabilities.skills
                        .inherited_values,
                  }
                : currentAgentCapabilities.capabilities.skills,
              mcp_servers:
                (body.mcp_servers as AgentCapabilitiesResponse["capabilities"]["mcp_servers"]) ??
                currentAgentCapabilities.capabilities.mcp_servers,
            },
            revision: `capability-revision-${currentCapabilityRevision}`,
          }
          return json(route, structuredClone(currentAgentCapabilities))
        }
        return json(route, { error: "capabilities_revision_mismatch" }, 409)
      }

      const activityMatch = path.match(/^\/api\/agents\/([^/]+)\/activity$/)
      if (activityMatch) {
        options.agentActivityRequests?.push({ method, path })
        if (
          method !== "GET" ||
          decodeURIComponent(activityMatch[1]) !== "reviewer"
        ) {
          return json(route, { error: "agent_not_found" }, 404)
        }
        return json(route, {
          agent_id: "reviewer",
          events: [
            {
              sequence: "1",
              agent_id: "reviewer",
              timestamp: "2026-07-30T12:00:00.000000001Z",
              kind: "agent.tool.exec_end",
              severity: "info",
              details: {
                tool_name: "web_search",
                duration_ms: "25",
                is_error: false,
                async: false,
                arguments: "CANARY_ARGUMENT_SECRET",
                result: "CANARY_RESULT_SECRET",
              },
              prompt: "CANARY_PROMPT_SECRET",
              error: "CANARY_ERROR_SECRET",
            },
          ],
          next_cursor: "opaque-cursor-1",
          reset: true,
          truncated: true,
          dropped: {
            subscription: "1",
            retention: "2",
            projection: "3",
          },
          raw_payload: "CANARY_RAW_SECRET",
        })
      }

      if (options.statefulAgents && path.startsWith("/api/agents")) {
        const rawBody = request.postData()
        const body = rawBody
          ? (request.postDataJSON() as {
              expected_config_revision?: string
              agent?: AgentMutationInput
            })
          : undefined
        if (method !== "GET") {
          options.agentRequests?.push({ method, path, body })
        }

        if (method === "GET" && path === "/api/agents") {
          return json(route, currentAgentsResponse())
        }

        const defaultMatch = path.match(/^\/api\/agents\/([^/]+)\/default$/)
        const itemMatch = path.match(/^\/api\/agents\/([^/]+)$/)

        if (method === "GET" && itemMatch) {
          const id = decodeURIComponent(itemMatch[1])
          const agent = currentAgents.find((candidate) => candidate.id === id)
          if (agent == null) {
            return json(route, { error: "agent_not_found" }, 404)
          }
          const collection = currentAgentsResponse()
          return json(route, {
            agent: structuredClone(agent),
            default_agent_id: collection.default_agent_id,
            config_revision: collection.config_revision,
            effects: collection.effects,
          })
        }

        if (
          body?.expected_config_revision !==
          currentAgentsResponse().config_revision
        ) {
          return json(route, { error: "config_revision_mismatch" }, 409)
        }

        if (method === "POST" && path === "/api/agents" && body.agent) {
          currentAgents.push({
            ...structuredClone(body.agent),
            is_default: false,
            default_configured: false,
            implicit: false,
          })
          return json(route, advanceAgentsRevision(), 201)
        }

        if (method === "POST" && defaultMatch) {
          const id = decodeURIComponent(defaultMatch[1])
          currentDefaultAgentID = id
          currentAgents = currentAgents.map((agent) => ({
            ...agent,
            is_default: agent.id === id,
            default_configured: agent.id === id,
          }))
          return json(route, advanceAgentsRevision())
        }

        if (itemMatch) {
          const id = decodeURIComponent(itemMatch[1])
          const index = currentAgents.findIndex((agent) => agent.id === id)
          if (index < 0) {
            return json(route, { error: "agent_not_found" }, 404)
          }
          if (method === "PUT" && body.agent) {
            const existing = currentAgents[index]
            currentAgents[index] = {
              ...structuredClone(body.agent),
              is_default: existing.is_default,
              default_configured: existing.default_configured,
              implicit: false,
            }
            return json(route, advanceAgentsRevision())
          }
          if (method === "DELETE") {
            currentAgents.splice(index, 1)
            if (currentDefaultAgentID === id) {
              currentDefaultAgentID = currentAgents[0]?.id ?? "main"
              currentAgents = currentAgents.map((agent, agentIndex) => ({
                ...agent,
                is_default: agentIndex === 0,
                default_configured: agentIndex === 0,
              }))
            }
            return json(route, advanceAgentsRevision())
          }
        }

        return json(route, { error: "unsupported_agent_request" }, 405)
      }

      if (options.statefulMCP && path.startsWith("/api/mcp")) {
        const rawBody = request.postData()
        const body = rawBody ? (request.postDataJSON() as unknown) : undefined
        if (method !== "GET") {
          options.mcpRequests?.push({ method, path, body })
        }

        if (method === "PATCH" && path === "/api/mcp/settings") {
          const settings = body as Pick<
            MCPConfigResponse,
            "enabled" | "discovery"
          >
          currentMCPResponse = {
            ...currentMCPResponse,
            enabled: settings.enabled,
            discovery: settings.discovery,
          }
          return json(route, currentMCPResponse)
        }
        if (method === "POST" && path === "/api/mcp/servers") {
          const input = body as MCPServerInput
          currentMCPResponse.servers.push(mcpServerFromInput(input))
          return json(route, currentMCPResponse)
        }
        if (method === "POST" && path === "/api/mcp/servers/test") {
          return json(route, {
            ok: true,
            tool_count: 2,
            tools: ["issues_list", "issue_create"],
          })
        }

        const credentialMatch = path.match(
          /^\/api\/mcp\/servers\/([^/]+)\/credential$/,
        )
        if (credentialMatch) {
          const name = decodeURIComponent(credentialMatch[1])
          const server = currentMCPResponse.servers.find(
            (candidate) => candidate.name === name,
          )
          if (server && method === "PUT") {
            server.auth = { type: "bearer", configured: true }
          } else if (
            server &&
            method === "DELETE" &&
            server.auth.type !== "custom"
          ) {
            server.auth = { type: "none", configured: false }
          }
          return json(route, { status: "ok" })
        }

        const serverMatch = path.match(/^\/api\/mcp\/servers\/([^/]+)$/)
        if (serverMatch) {
          const currentName = decodeURIComponent(serverMatch[1])
          const index = currentMCPResponse.servers.findIndex(
            (candidate) => candidate.name === currentName,
          )
          if (method === "PUT" && index >= 0) {
            currentMCPResponse.servers[index] = mcpServerFromInput(
              body as MCPServerInput,
              currentMCPResponse.servers[index],
            )
            return json(route, currentMCPResponse)
          }
          if (method === "DELETE" && index >= 0) {
            currentMCPResponse.servers.splice(index, 1)
            return json(route, { status: "ok" })
          }
        }
      }

      if (method === "POST") {
        switch (path) {
          case "/api/accounts/models/fetch": {
            const body = request.postDataJSON() as {
              credential_id?: string
              account_ref?: string
            }
            const failure = body.credential_id
              ? options.fetchModelFailures?.[body.credential_id]
              : undefined
            if (failure) {
              return route.fulfill({
                status: 502,
                contentType: "text/plain",
                body: failure,
              })
            }
            if (
              body.credential_id &&
              options.fetchModelEmptyCredentials?.includes(body.credential_id)
            ) {
              return json(route, {
                models: [],
                total: 0,
              })
            }
            const accountModels =
              body.account_ref === "gpt-4o-mini"
                ? ["gpt-4o-mini", "gpt-5.4"]
                : body.account_ref === "gpt-4o"
                  ? ["gpt-4o", "gpt-5.4"]
                  : ["gpt-4o", "gpt-5.4"]
            return json(route, {
              models: accountModels.map((id) => ({
                id,
                owned_by: "openai",
              })),
              total: 2,
            })
          }
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
                yaml:
                  options.workflowDevelopmentYAML ?? workflowDraftSession.yaml,
              }
            }
            return json(route, { session: activeDevelopmentSession })
          }
          case "/api/workflows/definitions/inspect": {
            const body = request.postDataJSON() as { ref?: unknown }
            expect(request.headers()["content-type"]).toBe("application/json")
            expect(typeof body.ref).toBe("string")
            const ref = body.ref as string
            expect(body).toEqual({ ref })
            expect(ref).toMatch(/^workflows\/[^/]+\.ya?ml$/)
            options.workflowInspectionRequests?.push({
              method,
              path,
              body,
            })
            return json(
              route,
              workflowDefinitionInspection({ kind: "published", ref }),
            )
          }
          case "/api/workflows/development/event-trigger/inspect": {
            const body = request.postDataJSON() as { yaml: string }
            const eventTrigger = body.yaml.includes("\n  event:")
              ? {
                  sources: ["github"],
                  types: ["issues.opened"],
                }
              : null
            return json(route, {
              revision: eventTriggerRevision(body.yaml),
              editable: true,
              event_trigger: eventTrigger,
              validation: {
                valid: true,
                validated_at: "2026-07-16T12:00:00Z",
              },
            })
          }
          case "/api/workflows/development/triggers/inspect": {
            const body = request.postDataJSON() as { yaml: string }
            return json(route, workflowTriggerInspection(body.yaml))
          }
          case "/api/workflows/development/triggers/render": {
            const body = request.postDataJSON() as {
              yaml: string
              revision: string
              trigger_type: string
              trigger: unknown
            }
            expect(body.revision).toBe(eventTriggerRevision(body.yaml))
            expect(body.trigger_type).toBe("event")
            const renderedYAML =
              body.trigger == null ? workflowDraftYAML : workflowEventDraftYAML
            return json(route, {
              yaml: renderedYAML,
              ...workflowTriggerInspection(renderedYAML),
            })
          }
          case "/api/workflows/development/jobs/inspect": {
            const body = request.postDataJSON() as { yaml: string }
            options.workflowJobRequests?.push({ method, path, body })
            return json(route, workflowJobsInspection(body.yaml))
          }
          case "/api/workflows/development/jobs/render": {
            const body = request.postDataJSON() as {
              yaml: string
              revision: string
              operation: {
                type: string
                fields?: {
                  name?: { mode: string; value?: string }
                }
              }
            }
            expect(body.revision).toBe(workflowJobsRevision(body.yaml))
            options.workflowJobRequests?.push({ method, path, body })
            const nameMutation = body.operation.fields?.name
            const renderedYAML =
              body.operation.type === "step.patch" &&
              nameMutation?.mode === "set"
                ? `${normalizeWorkflowDraftYAML(body.yaml)}# mock-step-name: ${nameMutation.value ?? ""}\n`
                : normalizeWorkflowDraftYAML(body.yaml)
            return json(route, {
              yaml: renderedYAML,
              ...workflowJobsInspection(renderedYAML),
            })
          }
          case "/api/workflows/development/event-trigger/render": {
            const body = request.postDataJSON() as {
              yaml: string
              revision: string
              event_trigger: {
                sources?: string[]
                connectors?: string[]
                types?: string[]
              } | null
            }
            expect(body.revision).toBe(eventTriggerRevision(body.yaml))
            const renderedYAML =
              body.event_trigger == null
                ? workflowDraftYAML
                : workflowEventDraftYAML
            return json(route, {
              yaml: renderedYAML,
              revision: eventTriggerRevision(renderedYAML),
              editable: true,
              event_trigger: body.event_trigger,
              validation: {
                valid: true,
                validated_at: "2026-07-16T12:00:00Z",
              },
            })
          }
          case "/api/workflows/development/event-trigger/match": {
            const body = request.postDataJSON() as {
              yaml: string
              event_id: string
            }
            expect(body).toEqual({
              yaml: workflowEventDraftYAML,
              event_id: eventResponse.id,
            })
            return json(route, {
              event_id: eventResponse.id,
              matched: true,
              checks: [
                {
                  path: "on.event.sources",
                  present: true,
                  value: "github",
                  matched: true,
                },
                {
                  path: "on.event.types",
                  present: true,
                  value: "issues.opened",
                  matched: true,
                },
              ],
              validation: {
                valid: true,
                validated_at: "2026-07-16T12:00:00Z",
              },
            })
          }
          case "/api/workflows/development/triggers/simulate": {
            const body = request.postDataJSON() as Record<string, unknown>
            options.workflowTriggerSimulationRequests?.push({
              method,
              path,
              body,
            })
            const trigger = body.trigger as {
              type: string
              schedule_index?: number
            }
            const scenario = body.scenario as {
              inputs?: Record<string, unknown>
              secrets?: Record<string, string>
              session?: string
              delivery?: Record<string, unknown>
            }
            return json(route, {
              simulation: {
                selected_kind: trigger.type,
                effective_kind: trigger.type,
                ...(trigger.type === "schedule"
                  ? { schedule_index: trigger.schedule_index }
                  : {}),
                present: true,
                matched: true,
                executable: true,
                reason: "matched",
                context_summary: {
                  input_count: Object.keys(scenario.inputs ?? {}).length,
                  secret_count: Object.keys(scenario.secrets ?? {}).length,
                  has_event:
                    trigger.type === "event" ||
                    trigger.type === "runtime_event",
                  has_session:
                    typeof scenario.session === "string" &&
                    scenario.session !== "",
                  has_delivery:
                    scenario.delivery != null &&
                    Object.keys(scenario.delivery).length > 0,
                },
              },
              review: {
                job_count: 1,
                step_count: 1,
                targets: ["agent/main"],
                effects: [
                  {
                    kind: "model_or_delegated_action_possible",
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
              review_token: `review-token:${trigger.type}`,
            })
          }
          case "/api/workflows/development/test/execute": {
            const body = request.postDataJSON() as Record<string, unknown>
            options.workflowTriggerExecutionRequests?.push({
              method,
              path,
              body,
            })
            const current =
              activeDevelopmentSession ??
              ({
                ...workflowDraftSession,
                yaml:
                  options.workflowDevelopmentYAML ?? workflowDraftSession.yaml,
              } satisfies MockWorkflowDevelopmentSession)
            activeDevelopmentSession = {
              ...current,
              prompt:
                typeof body.prompt === "string" ? body.prompt : current.prompt,
              target_workflow_ref:
                typeof body.target_ref === "string"
                  ? body.target_ref
                  : current.target_workflow_ref,
              yaml: typeof body.yaml === "string" ? body.yaml : current.yaml,
              session_revision: "opaque-session-reviewed-running",
              status: "testing",
              last_test: {
                ...workflowDraftLastTest,
                draft_key: workflowDraftKey(
                  typeof body.target_ref === "string"
                    ? body.target_ref
                    : current.target_workflow_ref,
                  typeof body.yaml === "string" ? body.yaml : current.yaml,
                ),
                draft_revision: current.draft_revision,
                run_id: "wr_draft",
                status: "running",
                tested_at: "2026-07-30T12:01:01Z",
              },
              updated_at: "2026-07-30T12:01:01Z",
            }
            completeDraftViaPolling = options.completeDraftViaPolling === true
            return json(
              route,
              {
                session: activeDevelopmentSession,
                result: {
                  run_id: "wr_draft",
                  status: "running",
                },
              },
              202,
            )
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
              expect(body.prompt).not.toContain("draft failure event")
              expect(body.prompt).not.toContain('"payload"')
              expect(body.prompt).not.toContain('"message"')
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
            reviseRequestCount += 1
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
          case "/api/workflows/dependencies/check": {
            const body = request.postDataJSON() as {
              ref?: string
              draft?: {
                target_ref: string
                yaml: string
              }
            }
            const workflowRef = body.ref ?? body.draft?.target_ref
            expect(workflowRef).toBeTruthy()
            if (body.draft != null) {
              expect(body.draft.target_ref).toBe(body.draft.target_ref.trim())
              expect(body.draft.yaml.trim()).not.toBe("")
            } else {
              expect(body).toEqual({ ref: workflowRef })
            }
            return json(route, {
              root_ref: workflowRef,
              revision: "opaque-dependency-revision",
              ready: true,
              workflow_enabled: true,
              structural_ready: true,
              runtime_ready: true,
              dependencies: [
                {
                  dependency: {
                    kind: "agent",
                    name: "main",
                    workflow_ref: workflowRef,
                    path: "jobs.triage.steps[0].uses",
                  },
                  code: "ready",
                  ready: true,
                },
              ],
              structural_issues: [],
            })
          }
          case "/api/workflows/development/discard": {
            const previous = activeDevelopmentSession
            activeDevelopmentSession = null
            return json(route, { session: previous })
          }
          case "/api/workflows/development/test": {
            const testBody = request.postDataJSON() as {
              async: boolean
              prompt?: string
              target_ref?: string
              yaml?: string
              inputs?: { ticket?: string }
              secrets?: Record<string, string>
              session?: string
              delivery?: Record<string, unknown>
              event_id?: string
            }
            if (testBody.event_id) {
              expect(testBody).toEqual({
                async: true,
                prompt: workflowDraftSession.prompt,
                target_ref: workflowDraftSession.target_workflow_ref,
                yaml: workflowEventDraftYAML,
                event_id: eventResponse.id,
              })
              activeDevelopmentSession = {
                ...workflowDraftSession,
                status: "testing",
                yaml: workflowEventDraftYAML,
                last_test: {
                  ...workflowDraftLastTest,
                  draft_key: workflowDraftKey(
                    workflowDraftSession.target_workflow_ref,
                    workflowEventDraftYAML,
                  ),
                  event_id: eventResponse.id,
                  status: "running",
                },
              }
              runs = [
                runningDraftWorkflowRun,
                ...runs.filter((run) => run.id !== "wr_draft"),
              ]
              return json(route, {
                session: activeDevelopmentSession,
                result: {
                  run_id: draftWorkflowRun.id,
                  status: "running",
                },
              })
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
              session_revision: "opaque-session-testing",
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
          case "/api/workflows/development/publish": {
            const publishSession = activeDevelopmentSession
            expect(reviseRequestCount).toBe(0)
            expect(request.postDataJSON()).toEqual({
              session_id: publishSession?.id,
              expected_session_revision: publishSession?.session_revision,
              expected_draft_revision: publishSession?.draft_revision,
              expected_base_target_revision:
                publishSession?.base_target_revision,
              expected_dependency_revision: "opaque-dependency-revision",
            })
            if (
              publishSession?.last_test?.status !== "succeeded" ||
              publishSession.last_test.draft_key !==
                currentDraftKey(publishSession) ||
              publishSession.last_test.draft_revision !==
                publishSession.draft_revision
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
              session: publishSession,
            })
          }
          case "/api/workflows/run":
            expect(request.postDataJSON()).toMatchObject({
              async: true,
              ref: "workflows/support-triage.yml",
              expected_dependency_revision: "opaque-dependency-revision",
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
              expected_dependency_revision: "opaque-dependency-revision",
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
          case "/api/workflows/runs/wr_cancel/cancel": {
            const body = request.postDataJSON() as { reason: string }
            expect(body).toEqual({ reason: "operator intervention" })
            options.workflowCancelReasons?.push(body.reason)
            currentCancelableWorkflowRun = {
              ...currentCancelableWorkflowRun,
              status: "canceled",
              cancel_reason: body.reason,
              cancel_requested_at: "2026-07-16T12:05:00Z",
              completed_at: "2026-07-16T12:05:00Z",
              updated_at: "2026-07-16T12:05:00Z",
            }
            return json(route, currentCancelableWorkflowRun)
          }
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
          case `/api/events/${eventResponse.id}/replay`:
            expect(request.postData()).toBe("{}")
            return json(
              route,
              {
                event: {
                  ...eventResponse,
                  id: replayEventID,
                  replay_of: eventResponse.id,
                  routing: {
                    ...eventResponse.routing,
                    status: "pending",
                    attempts: 0,
                  },
                },
              },
              201,
            )
          default:
            return json(route, { status: "ok" })
        }
      }

      if (method !== "GET") {
        return json(route, { status: "ok" })
      }

      const templateInspectionMatch = path.match(
        /^\/api\/workflows\/templates\/([^/]+)\/inspect$/,
      )
      if (templateInspectionMatch) {
        const templateName = decodeURIComponent(templateInspectionMatch[1])
        expect(templateName.trim()).not.toBe("")
        expect(request.postData()).toBeNull()
        options.workflowInspectionRequests?.push({
          method,
          path,
          body: null,
        })
        return json(
          route,
          workflowDefinitionInspection({
            kind: "template",
            template_name: templateName,
          }),
        )
      }

      switch (path) {
        case "/api/auth/status":
          return json(route, { authenticated: true, initialized: true })
        case "/api/gateway/status":
          return json(route, {
            gateway_status: options.gatewayRunning ? "running" : "stopped",
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
            channel_list: {
              telegram: { enabled: true, type: "telegram", settings: {} },
              discord: { enabled: false, type: "discord", settings: {} },
              deltachat: {
                enabled: true,
                type: "deltachat",
                settings: { email: "events@example.test" },
              },
            },
            gateway: { host: "127.0.0.1", port: 18789 },
            events: {
              ingress: {
                enabled: true,
                retention_days: 30,
                max_payload_bytes: 1048576,
                redact_fields: ["authorization"],
                webhooks: {
                  github: {
                    enabled: true,
                    format: "github",
                    secret: "[NOT_HERE]",
                  },
                },
                channels: {
                  deltachat: {
                    enabled: true,
                    source: "email",
                    mode: "mirror",
                  },
                },
              },
            },
          })
        case "/api/accounts/models":
          return json(route, options.modelResponse ?? modelResponse)
        case "/api/accounts/models/catalog":
          return json(route, { entries: [], total: 0 })
        case "/api/oauth/providers":
          return json(route, { providers: options.oauthProviders ?? [] })
        case "/api/oauth/codex-account-limits":
          return json(
            route,
            options.codexAccountLimits ?? {
              accounts: [
                {
                  id: "openai",
                  default: true,
                  email: "primary@example.test",
                  account_id: "acct-primary",
                  plan: "pro",
                  limits_status: "available",
                  entries: [
                    {
                      name: "codex",
                      status: "available",
                      window: "weekly",
                      used_percent: 64,
                      refreshes_at: "2026-07-28 13:05:32 -04:00",
                    },
                  ],
                },
              ],
            },
          )
        case "/api/sessions":
          return json(route, [])
        case "/api/tools":
          return json(route, toolsResponse)
        case "/api/mcp":
          return json(
            route,
            options.statefulMCP ? currentMCPResponse : mcpResponse,
          )
        case "/api/git-workspaces":
          return json(route, gitWorkspaceResponse)
        case "/api/agents":
          return json(route, currentAgentsResponse())
        case "/api/pr-workspaces":
          return json(route, {
            workspaces: [prWorkspaceAggregate.workspace],
          })
        case `/api/pr-workspaces/${prWorkspaceID}`:
          return json(route, prWorkspaceAggregate)
        case "/api/pr-lifecycle/gate-profiles":
          return json(route, prLifecycleGateProfiles)
        case "/api/events":
          return json(route, { events: [eventResponse] })
        case "/api/events/dispatches":
          return json(route, { dispatches: [eventDispatchResponse] })
        case `/api/events/dispatches/${eventDispatchResponse.id}`:
          return json(route, eventDispatchResponse)
        case `/api/events/${eventResponse.id}`:
          return json(route, eventResponse)
        case `/api/events/${eventResponse.id}/payload`:
          options.workflowEventPayloadRequests?.push(path)
          return route.fulfill({
            status: 200,
            contentType: "application/json",
            body: eventPayloadText,
          })
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
        case "/api/workflows/authoring/capabilities":
          options.workflowCapabilityRequests?.push({ method, path })
          return json(route, workflowAuthoringCapabilities())
        case "/api/workflows/templates":
          return json(route, {
            templates: [
              {
                name: "code-review",
                ref: "workflows/code-review.yml",
                state: "available",
              },
              {
                name: "github-issue-triage",
                ref: "workflows/github-issue-triage.yml",
                state: "modified",
              },
            ],
          })
        case "/api/workflows/runs":
          if (completeDraftViaPolling) {
            activeDevelopmentSession = {
              ...workflowDraftSession,
              session_revision: "opaque-session-tested",
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
        case "/api/workflows/runs/wr_lifecycle":
          return json(route, lifecycleWorkflowRun)
        case "/api/workflows/runs/wr_cancel":
          return json(route, currentCancelableWorkflowRun)
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
        case "/api/workflows/runs/wr_lifecycle/events":
          return json(route, {
            run_id: "wr_lifecycle",
            events: [],
          })
        case "/api/workflows/runs/wr_cancel/events":
          return json(route, {
            run_id: "wr_cancel",
            events: [],
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
        case "/api/workflows/runs/wr_lifecycle/events/stream":
        case "/api/workflows/runs/wr_cancel/events/stream":
          return sse(route, [])
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
            activeDevelopmentSession =
              activeDevelopmentSession?.last_test?.event_id != null
                ? {
                    ...activeDevelopmentSession,
                    session_revision: "opaque-session-event-tested",
                    status: "ready_to_publish",
                    last_test: {
                      ...activeDevelopmentSession.last_test,
                      status: "succeeded",
                    },
                  }
                : {
                    ...workflowDraftSession,
                    session_revision: "opaque-session-tested",
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
        case "/api/workflows/runs/wr_lifecycle/graph":
          return json(route, {
            run_id: "wr_lifecycle",
            nodes: [],
            edges: [],
          })
        case "/api/workflows/runs/wr_cancel/graph":
          return json(route, {
            run_id: "wr_cancel",
            nodes: [],
            edges: [],
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
        case "/api/tools/adaptation":
          return json(route, toolAdaptationResponse)
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

async function expectNoPersistentLoadingOrLoadError(page: Page) {
  const unresolvedState = page
    .locator("main")
    .getByText(
      /^(?:Loading\b|(?:Failed|Unable) to load\b|.*\b(?:is|are) unavailable\.?$)/i,
    )
  await expect(unresolvedState).toHaveCount(0)
}

async function expectElementFitsViewport(
  page: Page,
  selector: string,
  label: string,
) {
  await expect
    .poll(
      () =>
        page.locator(selector).evaluate((element) => {
          const rect = element.getBoundingClientRect()
          const tolerance = 1
          return (
            rect.left >= -tolerance &&
            rect.top >= -tolerance &&
            rect.right <= window.innerWidth + tolerance &&
            rect.bottom <= window.innerHeight + tolerance
          )
        }),
      { message: `${label} should fit in the viewport` },
    )
    .toBe(true)
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
      nodes: violation.nodes.map((node) => ({
        target: node.target.join(" "),
        html: node.html,
      })),
    })),
  ).toEqual([])
}

async function confirmTriggerExecutionReview(page: Page) {
  const dialog = page.getByRole("dialog", {
    name: "Review trigger execution",
  })
  await expect(dialog).toBeVisible()
  const confirm = dialog.getByRole("button", {
    name: "Confirm and execute",
  })
  await expect(confirm).toBeDisabled()
  await dialog
    .getByRole("switch", {
      name: "I reviewed this server simulation and its possible effects",
    })
    .click()
  await expect(confirm).toBeEnabled()
  await confirm.click()
  await expect(dialog).toBeHidden()
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
    await expectNoPersistentLoadingOrLoadError(page)
    await expectNoHorizontalOverflow(page)
    await expectNoSeriousA11yViolations(page)
    expect(errors).toEqual([])
  })
}

test("unified pull request workspace combines review, implementation, nudges, and gate profiles", async ({
  page,
}) => {
  await page.setViewportSize({ width: 1280, height: 900 })
  const errors = collectPageErrors(page)
  await gotoMockedRoute(
    page,
    `/pull-requests?workspace=${prWorkspaceID}&cursor=private&prompt=private#secret`,
  )

  await expect(page).toHaveURL(
    new RegExp(`/pull-requests\\?workspace=${prWorkspaceID}$`),
  )
  await expect(page.getByText("PR charter", { exact: true })).toBeVisible()
  await expect(
    page.getByText("Review search and nudges", { exact: true }),
  ).toBeVisible()
  await expect(
    page.getByText("Implementation and validation", { exact: true }),
  ).toBeVisible()
  // Both nudge controls stay visible in the unified workspace, but only the
  // control authorized for the aggregate's current lifecycle phase is active.
  await expect(page.getByRole("button", { name: "Find more" })).toBeDisabled()
  await expect(page.getByRole("button", { name: "Check again" })).toBeEnabled()
  await expect(
    page.getByRole("button", { name: "Publish review" }),
  ).toBeEnabled()
  await expect(
    page.getByRole("button", { name: "Publish implementation" }),
  ).toBeDisabled()
  await expect(page.getByText("reward 0.25")).toBeVisible()
  await expectNoSeriousA11yViolations(page)

  const pullRequests = page.getByRole("button", { name: "Pull requests" })
  if ((await pullRequests.getAttribute("aria-expanded")) !== "true") {
    await pullRequests.click()
  }
  await page.getByRole("link", { name: "Gate profiles" }).click()
  await expect(page).toHaveURL(/\/pull-requests\?view=gate-profiles$/)
  await expect(
    page.getByRole("heading", { name: "PR lifecycle gate profiles" }),
  ).toBeVisible()
  const gateMap = page
    .getByRole("heading", { name: "PR lifecycle gate flow" })
    .locator("xpath=ancestor::section[1]")
  await expect(gateMap.locator("[data-decision-point]")).toHaveCount(14)
  await gateMap.getByRole("button", { name: "Accept review results" }).click()
  await expect(page).toHaveURL(
    /\/pull-requests\?view=gate-profiles&gate=pr\.review\.complete$/,
  )
  await expect(page.locator("#pr-gate-workflow-editor")).toHaveAttribute(
    "data-decision-point",
    "pr.review.complete",
  )
  await expect(page.getByText("Review minimum")).toBeVisible()
  await expect(page.getByText("XS modules")).toBeVisible()
  await expectNoHorizontalOverflow(page)
  await expectNoSeriousA11yViolations(page)
  expect(errors).toEqual([])
})

test("agent management keeps configured policy editing safe on mobile", async ({
  page,
}) => {
  await page.setViewportSize({ width: 320, height: 720 })
  const errors = collectPageErrors(page)
  await gotoMockedRoute(page, "/agent/agents")

  await expect(page.getByText("Reviewer", { exact: true })).toBeVisible()
  await expect(page.getByRole("button", { name: "Delete main" })).toBeDisabled()
  await page.getByRole("button", { name: "Edit reviewer" }).click()

  const sheet = page.getByRole("dialog", { name: "Edit agent" })
  await expect(sheet).toBeVisible()
  await expect(sheet).toContainText("Configured policy")
  await expectElementFitsViewport(
    page,
    '[data-slot="sheet-content"]',
    "agent editor sheet",
  )
  await sheet
    .getByRole("textbox", { name: "Configured name" })
    .fill("Review team")
  await sheet.getByRole("button", { name: "Close" }).click()

  const discard = page.getByRole("alertdialog", {
    name: "Discard unsaved changes?",
  })
  await expect(discard).toBeVisible()
  await discard.getByRole("button", { name: "Keep editing" }).click()
  await expect(sheet).toBeVisible()
  await expectNoHorizontalOverflow(page)
  await expectNoSeriousA11yViolations(page)
  expect(errors).toEqual([])
})

test("agent management completes a stateful policy lifecycle with exact revisions", async ({
  page,
}) => {
  await page.setViewportSize({ width: 1280, height: 900 })
  const errors = collectPageErrors(page)
  const agentRequests: NonNullable<MockLauncherApiOptions["agentRequests"]> = []
  await gotoMockedRoute(page, "/agent/agents", {
    statefulAgents: true,
    agentRequests,
  })

  // Establish an in-app history entry, then verify browser Back uses the same
  // discard confirmation as closing a dirty editor.
  await page.getByRole("link", { name: "Models", exact: true }).click()
  await expect(page).toHaveURL(/\/models$/)
  await page.getByRole("link", { name: "Agents", exact: true }).click()
  await expect(page).toHaveURL(/\/agent\/agents$/)

  await page.getByRole("button", { name: "Edit reviewer" }).click()
  const reviewerSheet = page.getByRole("dialog", { name: "Edit agent" })
  await reviewerSheet
    .getByRole("textbox", { name: "Configured name" })
    .fill("Review team")
  await page.evaluate(() => window.history.back())

  const navigationDiscard = page.getByRole("alertdialog", {
    name: "Discard unsaved changes?",
  })
  await expect(navigationDiscard).toBeVisible()
  await navigationDiscard.getByRole("button", { name: "Keep editing" }).click()
  await expect(page).toHaveURL(/\/agent\/agents$/)
  await reviewerSheet.getByRole("button", { name: "Cancel" }).click()
  await page
    .getByRole("alertdialog", { name: "Discard unsaved changes?" })
    .getByRole("button", { name: "Discard changes" })
    .click()
  await expect(reviewerSheet).toBeHidden()

  await page.getByRole("button", { name: "Create agent" }).click()
  const createSheet = page.getByRole("dialog", { name: "Create agent" })
  await createSheet.getByRole("textbox", { name: "Agent ID" }).fill("triager")
  await createSheet
    .getByRole("textbox", { name: "Configured name" })
    .fill("Triager")
  await createSheet.getByRole("combobox", { name: "Provider account" }).click()
  await page.getByRole("option", { name: "gpt-4o", exact: true }).click()
  await createSheet
    .getByRole("combobox", { name: "Primary alias policy" })
    .click()
  await page.getByRole("option", { name: "Custom", exact: true }).click()
  await createSheet
    .getByRole("combobox", { name: "Primary model alias" })
    .click()
  await page.getByRole("option", { name: "code", exact: true }).click()
  await createSheet
    .getByRole("combobox", { name: "Fallback alias policy" })
    .click()
  await page.getByRole("option", { name: "Custom", exact: true }).click()
  await createSheet.getByRole("combobox", { name: "Fallback order" }).click()
  await page.getByRole("option", { name: "fast", exact: true }).click()
  await createSheet
    .getByRole("button", { name: "Add fallback order entry" })
    .click()
  await createSheet.getByRole("button", { name: "Save" }).click()

  const triagerCard = page.locator('[data-agent-id="triager"]')
  await expect(triagerCard).toBeVisible()
  await triagerCard.getByRole("button", { name: "Edit triager" }).click()

  const editSheet = page.getByRole("dialog", { name: "Edit agent" })
  await editSheet
    .getByRole("combobox", { name: "Fallback alias policy" })
    .click()
  await page.getByRole("option", { name: "None", exact: true }).click()
  await editSheet.getByRole("button", { name: "Save" }).click()
  await expect(triagerCard).toContainText("None")

  await triagerCard.getByRole("button", { name: "Set default" }).click()
  await expect(triagerCard.getByText("Default", { exact: true })).toBeVisible()

  await triagerCard.getByRole("button", { name: "Delete triager" }).click()
  const deleteDialog = page.getByRole("alertdialog", {
    name: "Delete agent?",
  })
  await deleteDialog
    .getByRole("button", { name: "Delete agent", exact: true })
    .click()
  await expect(triagerCard).toHaveCount(0)

  expect(agentRequests).toEqual([
    {
      method: "POST",
      path: "/api/agents",
      body: {
        expected_config_revision: "agent-revision-1",
        agent: {
          id: "triager",
          name: "Triager",
          workspace: "",
          account_ref: "gpt-4o",
          model: { primary: "code", fallbacks: ["fast"] },
          skills: null,
          subagents: null,
        },
      },
    },
    {
      method: "PUT",
      path: "/api/agents/triager",
      body: {
        expected_config_revision: "agent-revision-2",
        agent: {
          id: "triager",
          name: "Triager",
          workspace: "",
          account_ref: "gpt-4o",
          model: { primary: "code", fallbacks: [] },
          skills: null,
          subagents: null,
        },
      },
    },
    {
      method: "POST",
      path: "/api/agents/triager/default",
      body: { expected_config_revision: "agent-revision-3" },
    },
    {
      method: "DELETE",
      path: "/api/agents/triager",
      body: { expected_config_revision: "agent-revision-4" },
    },
  ])
  expect(errors).toEqual([])
})

test("agent details manage capabilities and privacy-safe activity at 320px", async ({
  page,
}) => {
  await page.setViewportSize({ width: 320, height: 720 })
  const errors = collectPageErrors(page)
  const capabilityRequests: NonNullable<
    MockLauncherApiOptions["agentCapabilityRequests"]
  > = []
  const activityRequests: NonNullable<
    MockLauncherApiOptions["agentActivityRequests"]
  > = []
  await page.routeWebSocket(/\/pico\/ws/, () => undefined)
  await gotoMockedRoute(page, "/agent/agents", {
    agentCapabilityRequests: capabilityRequests,
    agentActivityRequests: activityRequests,
    gatewayRunning: true,
  })

  await page
    .locator('[data-agent-id="reviewer"]')
    .getByRole("button", { name: "Manage reviewer" })
    .click()
  await expect(page).toHaveURL(/\/agent\/agents\?agent=reviewer&tab=overview$/)
  const tabs = page.getByRole("tablist", { name: "Agent management" })
  await tabs.getByRole("tab", { name: "Capabilities" }).click()
  await expect(page).toHaveURL(/tab=capabilities$/)
  await expect(tabs.getByRole("tab", { name: "Capabilities" })).toHaveAttribute(
    "aria-selected",
    "true",
  )
  await expect(page.getByText("Existing unknown selections")).toBeVisible()
  await page
    .getByRole("button", {
      name: "Remove unknown selection legacy_unknown",
    })
    .click()
  await page.getByRole("button", { name: "Save capabilities" }).click()
  await expect
    .poll(() => capabilityRequests.filter((entry) => entry.method === "PATCH"))
    .toEqual([
      {
        method: "PATCH",
        path: "/api/agents/reviewer/capabilities",
        body: {
          expected_revision: "capability-revision-1",
          tools: { mode: "selected", values: ["web_search"] },
        },
      },
    ])

  const tools = page.getByRole("group", { name: "Tools" })
  await tools.getByRole("radio", { name: "No tools" }).click()
  await tabs.getByRole("tab", { name: "Activity" }).click()
  const discard = page.getByRole("alertdialog", {
    name: "Discard capability changes?",
  })
  await expect(discard).toBeVisible()
  await discard.getByRole("button", { name: "Keep editing" }).click()
  await expect(page).toHaveURL(/tab=capabilities$/)

  await tabs.getByRole("tab", { name: "Activity" }).click()
  await page
    .getByRole("alertdialog", { name: "Discard capability changes?" })
    .getByRole("button", { name: "Discard changes" })
    .click()
  await expect(page).toHaveURL(/tab=activity$/)
  await expect(page.getByText("Tool execution ended")).toBeVisible()
  await expect(page.getByText(/web_search; 25 ms; completed/)).toBeVisible()
  await expect(page.getByText(/cursor was reset/)).toBeVisible()
  expect(activityRequests.length).toBeGreaterThan(0)
  await expect(page.locator("body")).not.toContainText("CANARY_")

  await tabs.getByRole("tab", { name: "Activity" }).focus()
  await page.keyboard.press("Home")
  await expect(page).toHaveURL(/tab=overview$/)
  await expect(tabs.getByRole("tab", { name: "Overview" })).toBeFocused()

  await expectNoHorizontalOverflow(page)
  await expectNoSeriousA11yViolations(page)
  expect(errors).toEqual([])
})

test("events payload stays opt-in and replay remains deliberate", async ({
  page,
}) => {
  const errors = collectPageErrors(page)
  let payloadRequests = 0
  let replayRequests = 0

  page.on("request", (request) => {
    const path = new URL(request.url()).pathname
    if (path === `/api/events/${eventResponse.id}/payload`) {
      payloadRequests += 1
    }
    if (path === `/api/events/${eventResponse.id}/replay`) {
      replayRequests += 1
    }
  })

  await gotoMockedRoute(page, "/events")
  await expect(
    page.getByRole("button", { name: /issues\.opened.*github\/triage/ }),
  ).toBeVisible()
  await expect(page.getByRole("button", { name: "Show payload" })).toBeVisible()
  expect(payloadRequests).toBe(0)

  await page.getByRole("button", { name: "Show payload" }).click()
  await expect(page.locator("pre")).toHaveText(eventPayloadText)
  expect(payloadRequests).toBe(1)

  await page.getByRole("button", { name: "Replay", exact: true }).click()
  const dialog = page.getByRole("alertdialog")
  await expect(dialog).toContainText(
    "may repeat workflows and external effects",
  )
  await dialog.getByRole("button", { name: "Cancel" }).click()
  await expect(dialog).toBeHidden()
  expect(replayRequests).toBe(0)

  await page.getByRole("button", { name: "Replay", exact: true }).click()
  await dialog
    .getByRole("button", { name: "Replay event", exact: true })
    .click()
  await expect.poll(() => replayRequests).toBe(1)
  await expect(page).toHaveURL(new RegExp(`event=${replayEventID}`))
  await expect(page.locator("pre")).toHaveCount(0)

  await page.waitForTimeout(100)
  expect(replayRequests).toBe(1)
  await expectNoHorizontalOverflow(page)
  await expectNoSeriousA11yViolations(page)
  expect(errors).toEqual([])
})

test("dispatch and workflow deep links survive reload with exact relationships", async ({
  page,
}) => {
  const errors = collectPageErrors(page)
  await gotoMockedRoute(
    page,
    `/events?view=dispatches&dispatch=${eventDispatchResponse.id}`,
  )

  await expect(
    page.getByText(eventDispatchResponse.workflow_revision),
  ).toBeVisible()
  await expect(page.getByRole("link", { name: "Open event" })).toHaveAttribute(
    "href",
    `/events?event=${eventResponse.id}`,
  )
  await expect(
    page.getByRole("link", { name: "Open workflow" }),
  ).toHaveAttribute(
    "href",
    "/agent/workflows?mode=operate&workflow=workflows%2Fgithub-issue-triage.yml",
  )
  await expect(page.getByRole("link", { name: "Open run" })).toHaveAttribute(
    "href",
    "/agent/workflows?mode=operate&workflow=workflows%2Fgithub-issue-triage.yml&run=wr_smoke",
  )
  await page.reload()
  await expect(
    page.getByText(eventDispatchResponse.workflow_revision),
  ).toBeVisible()
  await expectNoHorizontalOverflow(page)

  await page.goto(
    "/agent/workflows?mode=operate&workflow=workflows%2Fsummarize-text.yml&run=wr_test&q=succeeded",
  )
  await expect(page.getByText("wr_test", { exact: true }).first()).toBeVisible()
  await expect(
    page.getByRole("link", { name: "workflows/summarize-text.yml" }),
  ).toHaveAttribute(
    "href",
    expect.stringContaining(
      "mode=operate&workflow=workflows%2Fsummarize-text.yml&run=wr_test",
    ),
  )
  await page.reload()
  await expect(page.getByText("wr_test", { exact: true }).first()).toBeVisible()
  await expect(page).toHaveURL(/mode=operate/)
  await expect(page).toHaveURL(/run=wr_test/)
  await expectNoHorizontalOverflow(page)
  await expectNoSeriousA11yViolations(page)
  expect(errors).toEqual([])
})

test("workflow lifecycle links use only validated server origin", async ({
  page,
}) => {
  const errors = collectPageErrors(page)
  await gotoMockedRoute(
    page,
    "/agent/workflows?mode=operate&workflow=workflows%2Fgithub-issue-triage.yml&run=wr_lifecycle",
  )

  await expect(
    page.getByRole("link", { name: lifecycleEventID }),
  ).toHaveAttribute("href", `/events?event=${lifecycleEventID}`)
  await expect(
    page.getByRole("link", { name: lifecycleDispatchID }),
  ).toHaveAttribute(
    "href",
    `/events?view=dispatches&dispatch=${lifecycleDispatchID}`,
  )
  await expect(
    page.getByRole("link", { name: "wr_lifecycle_root" }),
  ).toHaveAttribute(
    "href",
    "/agent/workflows?mode=operate&run=wr_lifecycle_root",
  )
  await expect(page.locator(`a[href*="${lifecycleDecoyEventID}"]`)).toHaveCount(
    0,
  )
  await expect(
    page.locator(`a[href*="${lifecycleDecoyDispatchID}"]`),
  ).toHaveCount(0)
  await expectNoHorizontalOverflow(page)
  await expectNoSeriousA11yViolations(page)
  expect(errors).toEqual([])
})

test("workflow cancellation requires and persists an accessible explicit reason", async ({
  page,
}) => {
  const errors = collectPageErrors(page)
  const workflowCancelReasons: string[] = []
  await gotoMockedRoute(
    page,
    "/agent/workflows?mode=operate&workflow=workflows%2Fsummarize-text.yml&run=wr_cancel",
    { workflowCancelReasons },
  )

  const cancelButton = page.getByRole("button", { name: "Cancel" })
  await expect(cancelButton).toBeEnabled()
  await cancelButton.click()
  const dialog = page.getByRole("alertdialog")
  await expect(dialog).toContainText("wr_cancel")
  await expectElementFitsViewport(
    page,
    '[data-slot="alert-dialog-content"]',
    "workflow cancel dialog",
  )
  await expect(
    dialog.getByRole("button", { name: "Cancel run" }),
  ).toBeDisabled()
  await dialog.getByRole("textbox", { name: "Cancel reason" }).fill("   ")
  await expect(dialog.getByText("A cancel reason is required.")).toBeVisible()
  await expect(
    dialog.getByRole("button", { name: "Cancel run" }),
  ).toBeDisabled()
  await dialog
    .getByRole("textbox", { name: "Cancel reason" })
    .fill("operator intervention")
  await dialog.getByRole("button", { name: "Cancel run" }).click()

  await expect
    .poll(() => workflowCancelReasons)
    .toEqual(["operator intervention"])
  await expect(dialog).toBeHidden()
  await expect(page.getByText("Cancel requested")).toBeVisible()
  await expect(page.getByText("Completed")).toBeVisible()
  await expect(
    page.getByText("operator intervention", { exact: true }).first(),
  ).toBeVisible()
  await expectNoHorizontalOverflow(page)
  await expectNoSeriousA11yViolations(page)
  expect(errors).toEqual([])
})

test("MCP page exposes accessible server, OAuth, and settings flows", async ({
  page,
}) => {
  await page.setViewportSize({ width: 320, height: 720 })
  const errors = collectPageErrors(page)
  await gotoMockedRoute(page, "/agent/mcp")

  await expect(page.getByText("github", { exact: true })).toBeVisible()
  await expect(page.getByRole("button", { name: "Reconnect" })).toBeVisible()
  await expect(page.getByText("local-files", { exact: true })).toBeVisible()

  await page.getByRole("button", { name: "Add server" }).first().click()
  const sheet = page.getByRole("dialog", { name: "Add MCP server" })
  await expect(sheet).toBeVisible()
  await expectElementFitsViewport(
    page,
    '[data-slot="sheet-content"]',
    "MCP add sheet",
  )
  await sheet.getByRole("combobox", { name: "Authentication" }).first().click()
  await page.getByRole("option", { name: "OAuth login" }).click()
  await expect(
    sheet.getByRole("button", { name: "Save & log in" }),
  ).toBeVisible()
  await expectNoHorizontalOverflow(page)
  await expectNoSeriousA11yViolations(page)

  await sheet.getByRole("button", { name: "Close" }).click()
  await page.getByRole("button", { name: "Settings" }).first().click()
  const settings = page.getByRole("dialog", { name: "MCP settings" })
  await expect(settings).toBeVisible()
  await settings.getByRole("switch", { name: "Deferred discovery" }).click()
  await expect(
    settings.getByRole("spinbutton", { name: "Tool-use TTL" }),
  ).toBeVisible()
  await expectElementFitsViewport(
    page,
    '[data-slot="sheet-content"]',
    "MCP settings sheet",
  )
  await expectNoHorizontalOverflow(page)
  await expectNoSeriousA11yViolations(page)
  expect(errors).toEqual([])
})

test("MCP server add, bearer, update, custom-header, test, and delete flow", async ({
  page,
}) => {
  const requests: NonNullable<MockLauncherApiOptions["mcpRequests"]> = []
  await gotoMockedRoute(page, "/agent/mcp", {
    statefulMCP: true,
    mcpRequests: requests,
  })

  await page.getByRole("button", { name: "Add server" }).first().click()
  let sheet = page.getByRole("dialog", { name: "Add MCP server" })
  await sheet.getByRole("textbox", { name: "Name" }).fill("linear")
  await sheet
    .getByRole("textbox", { name: "Server URL" })
    .fill("https://mcp.linear.example/api")
  await sheet.getByRole("combobox", { name: "Authentication" }).click()
  await page.getByRole("option", { name: "Bearer token" }).click()
  await sheet.getByLabel("Set token").fill("linear-secret")
  await sheet.getByRole("button", { name: "Save & test" }).click()

  await expect(page.getByText("linear", { exact: true })).toBeVisible()
  expect(
    requests.some(
      (request) =>
        request.method === "POST" &&
        request.path === "/api/mcp/servers" &&
        (request.body as MCPServerInput).auth_mode === "bearer",
    ),
  ).toBe(true)
  expect(
    requests.some(
      (request) =>
        request.method === "PUT" &&
        request.path === "/api/mcp/servers/linear/credential" &&
        (request.body as { token?: string }).token === "linear-secret",
    ),
  ).toBe(true)
  expect(
    requests.some(
      (request) =>
        request.method === "POST" && request.path === "/api/mcp/servers/test",
    ),
  ).toBe(true)

  await page.getByRole("button", { name: "Edit linear" }).click()
  sheet = page.getByRole("dialog", { name: "Edit MCP server" })
  await sheet.getByRole("textbox", { name: "Name" }).fill("linear-team")
  await sheet.getByRole("combobox", { name: "Authentication" }).click()
  await page.getByRole("option", { name: "Custom headers" }).click()
  await sheet.getByRole("button", { name: "Add entry" }).click()
  await sheet.getByRole("textbox", { name: "Key" }).fill("X-Linear-Key")
  await sheet
    .getByRole("textbox", { name: "Value", exact: true })
    .fill("header-secret")
  await sheet.getByRole("button", { name: "Save", exact: true }).click()

  await expect(page.getByText("linear-team", { exact: true })).toBeVisible()
  expect(
    requests.some((request) => {
      if (
        request.method !== "PUT" ||
        request.path !== "/api/mcp/servers/linear"
      ) {
        return false
      }
      const body = request.body as MCPServerInput
      return (
        body.name === "linear-team" &&
        body.auth_mode === "custom" &&
        body.headers?.["X-Linear-Key"] === "header-secret"
      )
    }),
  ).toBe(true)

  await page.getByRole("button", { name: "Delete linear-team" }).click()
  const confirm = page.getByRole("alertdialog", {
    name: "Delete MCP server?",
  })
  await confirm.getByRole("button", { name: "Delete server" }).click()
  await expect(page.getByText("linear-team", { exact: true })).toHaveCount(0)
  expect(
    requests.some(
      (request) =>
        request.method === "DELETE" &&
        request.path === "/api/mcp/servers/linear-team",
    ),
  ).toBe(true)
})

test("accounts page lists registered accounts and opens onboarding", async ({
  page,
}) => {
  const errors = collectPageErrors(page)
  const accountModel = {
    index: 2,
    model_name: "gpt-4o-work",
    provider: "openai",
    model: "gpt-4o",
    api_key: "",
    enabled: true,
    available: true,
    status: "available",
    is_default: false,
    is_virtual: false,
    auth_method: "oauth",
    credential_id: "openai:work",
  }
  await gotoMockedRoute(page, "/accounts", {
    modelResponse: {
      ...modelResponse,
      models: [...modelResponse.models, accountModel],
      total: modelResponse.total + 1,
    },
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
          {
            provider: "openai",
            credential_id: "openai:zero",
            display_name: "OpenAI",
            methods: ["browser", "device_code", "token"],
            logged_in: true,
            status: "connected",
            auth_method: "oauth",
            account_id: "acc_zero",
          },
          {
            provider: "openai",
            credential_id: "openai:unsupported",
            display_name: "OpenAI",
            methods: ["browser", "device_code", "token"],
            logged_in: true,
            status: "connected",
            auth_method: "oauth",
            account_id: "acc_unsupported",
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
    codexAccountLimits: {
      accounts: [
        {
          id: "openai:work",
          email: "work@example.test",
          account_id: "acc_123",
          plan: "pro",
          limits_status: "available",
          rate_limit_reset_credits: {
            available_count: 2,
            auto_reset: true,
          },
          entries: [
            {
              name: "codex",
              status: "available",
              window: "5h",
              used_percent: 8,
            },
            {
              name: "codex",
              status: "available",
              window: "weekly",
              used_percent: 64,
            },
            {
              name: "GPT-5.3-Codex-Spark",
              status: "available",
              window: "weekly",
              used_percent: 0,
            },
          ],
        },
        {
          id: "openai:zero",
          email: "zero@example.test",
          account_id: "acc_zero",
          plan: "plus",
          limits_status: "available",
          rate_limit_reset_credits: {
            available_count: 0,
            auto_reset: true,
          },
          entries: [
            {
              name: "codex",
              status: "available",
              window: "weekly",
              used_percent: 12,
            },
          ],
        },
        {
          id: "openai:unsupported",
          email: "unsupported@example.test",
          account_id: "acc_unsupported",
          plan: "plus",
          limits_status: "available",
          entries: [
            {
              name: "codex",
              status: "available",
              window: "weekly",
              used_percent: 18,
            },
          ],
        },
        {
          id: "personal",
          email: "personal@example.test",
          plan: "plus",
          limits_status: "available",
          entries: [
            {
              name: "codex",
              status: "available",
              window: "weekly",
              used_percent: 12,
            },
          ],
        },
      ],
    },
  })

  await expect(page.getByRole("heading", { name: "work" })).toBeVisible()
  await expect(
    page.getByText(
      "Manage registered provider accounts and add named accounts for supported login methods.",
    ),
  ).toHaveCount(0)
  await expect(page.getByText("OpenAI oauth (pro)")).toBeVisible()
  await expect(page.getByText("openai:work")).toBeVisible()
  await expect(page.getByText("Codex Account Limits")).not.toBeVisible()
  await expect(page.getByText("personal@example.test")).not.toBeVisible()
  await expect(page.getByText("Anthropic")).not.toBeVisible()
  await expect(page.getByText("gpt-4o-mini")).toHaveCount(0)

  const accountCard = page.locator("article").filter({
    has: page.getByRole("heading", { name: "work" }),
  })
  await expect(accountCard.getByText("codex 5h")).toBeVisible()
  await expect(accountCard.getByText("codex Weekly")).toBeVisible()
  await expect(
    accountCard.getByText("GPT-5.3-Codex-Spark Weekly"),
  ).toBeVisible()
  await expect(accountCard.getByText("64%")).toBeVisible()
  await expect(accountCard.getByText("Usage limit resets: 2")).toBeVisible()
  await expect(accountCard.getByText("Auto-use when available")).toBeVisible()
  await expect(
    accountCard.getByRole("button", {
      name: "When Codex reaches an eligible usage limit and a reset is available, PicoClaw uses one automatically and retries once.",
    }),
  ).toBeVisible()
  await expect(accountCard.getByRole("combobox")).toHaveCount(0)

  const zeroCard = page.locator("article").filter({
    has: page.getByRole("heading", { name: "zero" }),
  })
  await expect(zeroCard.getByText("Usage limit resets: 0")).toBeVisible()
  await expect(zeroCard.getByText("Auto-use when available")).toBeVisible()

  const unsupportedCard = page.locator("article").filter({
    has: page.getByRole("heading", { name: "unsupported" }),
  })
  await expect(unsupportedCard.getByText(/Usage limit resets:/)).toHaveCount(0)

  await page.getByRole("button", { name: "Add Account" }).first().click()
  await expect(
    page.getByRole("dialog", { name: "Onboard Account" }),
  ).toBeVisible()
  await page.getByRole("combobox").first().click()
  await expect(page.getByRole("option", { name: "DeepSeek" })).toBeVisible()
  await expect(
    page.getByRole("option", { name: "Google Gemini" }),
  ).toBeVisible()
  await page.keyboard.press("Escape")
  await expect(page.getByPlaceholder("work")).toBeVisible()
  await expect(page.getByText("OAuth logins can infer this")).toBeVisible()
  expect(errors).toEqual([])
})

test("models page exposes editable model aliases without global runtime selection", async ({
  page,
}) => {
  const errors = collectPageErrors(page)
  await gotoMockedRoute(page, "/models")

  await expect(
    page.getByRole("heading", { name: "Runtime selection" }),
  ).toHaveCount(0)
  await expect(
    page.getByRole("heading", { name: "Provider accounts" }),
  ).toHaveCount(0)

  const aliasSection = page.locator("section").filter({
    has: page.getByRole("heading", { name: "Developer aliases" }),
  })
  const codingAlias = aliasSection.locator("article").filter({
    has: page.getByRole("heading", { name: "code" }),
  })
  await expect(codingAlias.getByRole("heading", { name: "code" })).toBeVisible()
  await expect(codingAlias.getByText("gpt-4o-mini")).toBeVisible()
  const investigateAlias = aliasSection.locator("article").filter({
    has: page.getByRole("heading", { name: "investigate" }),
  })
  await expect(investigateAlias.getByText("Not configured")).toBeVisible()
  await expect(
    investigateAlias.getByText(
      "Deep research, root-cause analysis, and unfamiliar code.",
    ),
  ).toBeVisible()
  await codingAlias.getByRole("button", { name: "Edit model alias" }).click()

  const editor = page.getByRole("dialog", { name: "Edit model alias" })
  await expect(editor).toBeVisible()
  await expect(editor.getByRole("textbox").first()).toHaveValue("code")
  await expect(editor.getByRole("textbox").first()).toBeDisabled()
  await expect(
    editor.getByText(
      "Choose another model or disable this alias for a concrete account.",
    ),
  ).toBeVisible()
  await editor.getByRole("combobox", { name: "Default model" }).click()
  const sharedModel = page.getByRole("option").filter({ hasText: "gpt-5.4" })
  await expect(sharedModel.getByText("All accounts (2)")).toBeVisible()
  const accountSpecificModel = page.getByRole("option", {
    name: /^gpt-4o-mini/,
  })
  await expect(accountSpecificModel.getByText(/Missing: gpt-4o/)).toBeVisible()
  const defaultModelSearch = page.getByPlaceholder("Search models...")
  await expect(defaultModelSearch).toBeFocused()
  await page.keyboard.type("gpt-5.4")
  await expect(defaultModelSearch).toHaveValue("gpt-5.4")
  await expect(sharedModel).toBeVisible()
  await expect(accountSpecificModel).toHaveCount(0)
  await defaultModelSearch.fill("")
  await page.keyboard.press("Escape")
  await expect(
    editor.getByRole("button", { name: "Add override" }),
  ).toBeEnabled()
  await editor.getByRole("button", { name: "Add override" }).click()
  await editor.getByRole("combobox", { name: "Override model" }).last().click()
  await expect(
    page.getByRole("option", { name: "Disabled for this account" }),
  ).toBeVisible()
  await expect(
    page.getByRole("option", { name: /^gpt-4o-mini All accounts/ }),
  ).toBeVisible()
  await expect(
    page.getByRole("option", { name: "gpt-5.4 All accounts (1)" }),
  ).toBeVisible()
  await expect(
    page.getByRole("option", { name: /^gpt-4o All accounts/ }),
  ).toHaveCount(0)
  const overrideModelSearch = page.getByPlaceholder("Search models...").last()
  await expect(overrideModelSearch).toBeFocused()
  await page.keyboard.type("gpt-5.4")
  await expect(overrideModelSearch).toHaveValue("gpt-5.4")
  await expect(
    page.getByRole("option", { name: "gpt-5.4 All accounts (1)" }),
  ).toBeVisible()
  await expect(
    page.getByRole("option", { name: /^gpt-4o-mini All accounts/ }),
  ).toHaveCount(0)
  await page.keyboard.press("Escape")
  expect(errors).toEqual([])
})

test("model alias editor explains when no enabled accounts are available", async ({
  page,
}) => {
  const errors = collectPageErrors(page)
  await gotoMockedRoute(page, "/models", {
    modelResponse: {
      ...modelResponse,
      models: modelResponse.models
        .filter((model) => model.provider !== "model-router")
        .map((model) => ({ ...model, enabled: false })),
      total: 2,
    },
  })

  const aliasSection = page.locator("section").filter({
    has: page.getByRole("heading", { name: "Developer aliases" }),
  })
  const investigateAlias = aliasSection.locator("article").filter({
    has: page.getByRole("heading", { name: "investigate" }),
  })
  await investigateAlias
    .getByRole("button", { name: "Configure model alias" })
    .click()

  const editor = page.getByRole("dialog", { name: "Configure model alias" })
  await expect(
    editor.getByText(
      "No enabled accounts are available. Add or restore one on the Accounts page before choosing models or overrides.",
    ),
  ).toBeVisible()
  await expect(
    editor.getByRole("combobox", { name: "Default model" }),
  ).toBeDisabled()
  await expect(
    editor.getByRole("button", { name: "Add override" }),
  ).toBeDisabled()
  expect(errors).toEqual([])
})

test("accounts page shows account routers beside registered accounts", async ({
  page,
}) => {
  const errors = collectPageErrors(page)

  await gotoMockedRoute(page, "/accounts", {
    modelResponse: {
      ...modelResponse,
      models: [
        ...modelResponse.models,
        {
          index: 2,
          model_name: "router-main",
          provider: "router",
          model: "gpt-4o",
          api_key: "",
          enabled: true,
          available: true,
          status: "available",
          is_default: false,
          is_virtual: false,
          router: {
            enabled: true,
            entry: "pool",
            blocks: [
              {
                id: "pool",
                type: "load_balance",
                accounts: [
                  "credential:openai:work",
                  "credential:openai:backup",
                ],
                strategy: "blind",
              },
            ],
          },
        },
      ],
      total: 3,
    },
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
          },
          {
            provider: "openai",
            credential_id: "openai:backup",
            display_name: "OpenAI",
            methods: ["browser", "device_code", "token"],
            logged_in: true,
            status: "needs_refresh",
            auth_method: "oauth",
          },
        ],
      },
    ],
  })

  const mainRouterCard = page.locator("article").filter({
    has: page.getByRole("heading", { name: "router-main" }),
  })

  await expect(page.getByRole("heading", { name: "router-main" })).toBeVisible()
  await expect(mainRouterCard.getByText("Account Router")).toBeVisible()
  await expect(mainRouterCard.getByText("Needs attention")).toBeVisible()
  await expect(page.getByText("work: Connected")).toBeVisible()
  await expect(page.getByText("backup: Needs refresh")).toBeVisible()
  await expect(
    page.getByRole("heading", { name: "Account Routers" }),
  ).toBeVisible()
  await expect(
    page.getByText(
      "Joint accounts that route requests through connected accounts.",
    ),
  ).toBeVisible()
  await expect(
    mainRouterCard.getByRole("button", { name: "Edit account router" }),
  ).toBeVisible()
  await expect(mainRouterCard.getByText("Decision Graph")).toBeVisible()
  await expect(page.getByText("No account routers configured.")).toHaveCount(0)
  await expectNoHorizontalOverflow(page)
  await expectNoSeriousA11yViolations(page)
  expect(errors).toEqual([])
})

test("accounts page creates accounts through account actions only", async ({
  page,
}) => {
  const errors = collectPageErrors(page)

  await gotoMockedRoute(page, "/accounts")

  await expect(
    page.getByRole("button", { name: "Add Account" }).first(),
  ).toBeVisible()
  await expect(
    page
      .locator("section")
      .filter({
        has: page.getByRole("heading", { name: "Account Routers" }),
      })
      .getByRole("button", { name: "Account Router" })
      .first(),
  ).toBeVisible()
  await expect(page.getByRole("button", { name: "Add Model" })).toHaveCount(0)
  await expect(
    page.getByRole("button", { name: "Saved Catalogs" }),
  ).toHaveCount(0)
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
          {
            provider: "openai",
            credential_id: "openai:empty",
            display_name: "OpenAI",
            methods: ["browser", "device_code", "token"],
            logged_in: true,
            status: "connected",
            auth_method: "oauth",
            account_id: "acct-empty",
          },
        ],
      },
    ],
  })
  await page
    .locator("section")
    .filter({
      has: page.getByRole("heading", { name: "Account Routers" }),
    })
    .getByRole("button", { name: "Account Router" })
    .first()
    .click()

  await expect(page).toHaveURL(/\/accounts\/account-router\/new$/)
  await expect(
    page.getByRole("heading", { name: "Create Account Router" }),
  ).toBeVisible()
  await expect(
    page.getByText("Add an account or load balancer block to start."),
  ).toHaveCount(1)
  await expect(page.getByText("No accounts connected.")).toBeVisible()
  await expect(page.getByText("Entry Block")).toHaveCount(0)

  await page.getByRole("button", { name: "Add Account" }).click()
  const accountDialog = page.getByRole("dialog", { name: "account-1" })
  await expect(accountDialog).toBeVisible()
  if ((page.viewportSize()?.width ?? 0) >= 700) {
    const dialogBox = await accountDialog.boundingBox()
    const viewport = page.viewportSize()
    expect(dialogBox).not.toBeNull()
    expect(viewport).not.toBeNull()
    expect(dialogBox!.height).toBeLessThan(viewport!.height * 0.85)
    expect(dialogBox!.width).toBeLessThan(viewport!.width * 0.8)
  }
  await page.getByRole("combobox", { name: "Account" }).click()
  await page.getByRole("option", { name: "OpenAI: acct-primary" }).click()
  await accountDialog.getByRole("button", { name: "Close" }).last().click()
  await expect(accountDialog).toBeHidden()

  await page.getByRole("button", { name: "Add Load Balancer" }).click()
  const loadBalancerDialog = page.getByRole("dialog", {
    name: "load-balancer-1",
  })
  await expect(loadBalancerDialog).toBeVisible()
  await page.getByRole("button", { name: "OpenAI: acct-backup" }).click()
  await page.getByRole("button", { name: "OpenAI: acct-empty" }).click()
  await loadBalancerDialog.getByRole("button", { name: "Close" }).last().click()
  await expect(loadBalancerDialog).toBeHidden()
  await page.getByRole("button", { name: "Add Branch" }).click()
  const branchDialog = page.getByRole("dialog", { name: "branch-1" })
  await expect(branchDialog).toBeVisible()
  await expect(page.getByText("Branch Condition")).toBeVisible()
  await expect(
    page.getByText("Type one condition. Use accounts:provider:name.metric"),
  ).toBeVisible()
  await expect(
    page.getByText("Start typing to autocomplete account metrics"),
  ).toBeVisible()
  await expect(
    page.getByText("Math functions: add, subtract, multiply"),
  ).toBeVisible()
  const branchCondition = page.getByRole("combobox", {
    name: "Branch Condition",
  })
  await expect(branchCondition).toHaveValue(
    "accounts:openai:acct-primary.rpm > 0",
  )
  await branchCondition.fill("accounts:openai:acct-primary.")
  await expect(
    page.getByText("Use syntax like accounts:openai:work.limit_pressure >= 80"),
  ).toBeVisible()
  await expect(
    page.getByRole("listbox", { name: "Branch Condition Suggestions" }),
  ).toBeVisible()
  const limitPressureMetric = "accounts:openai:acct-primary.limit_pressure"
  await expect(
    page.getByRole("option", {
      name: /accounts:openai:acct-primary\.rpm metric/,
    }),
  ).toBeVisible()
  await expect(
    page.getByRole("option", { name: />= 0\.8 example/ }),
  ).toHaveCount(0)
  await page
    .getByRole("option", {
      name: new RegExp(`${limitPressureMetric.replaceAll(".", "\\.")}.*metric`),
    })
    .click()
  await expect(branchCondition).toHaveValue(limitPressureMetric)
  await branchCondition.press("End")
  await branchCondition.press("Space")
  await expect(page.getByRole("option", { name: /> comparison/ })).toBeVisible()
  await expect(
    page.getByRole("option", { name: /limit_pressure metric/ }),
  ).toHaveCount(0)
  await page.getByRole("option", { name: /> comparison/ }).click()
  await expect(branchCondition).toHaveValue(`${limitPressureMetric} > `)
  const textCondition = `multiply(${limitPressureMetric}, 100) >= 75`
  await branchCondition.press("Control+A")
  await branchCondition.press("Backspace")
  await branchCondition.fill(textCondition)
  await expect(branchCondition).toHaveValue(textCondition)
  await expect(
    page.getByText("Use syntax like accounts:openai:work.limit_pressure >= 80"),
  ).toHaveCount(0)
  await expect(page.getByText("Left Value")).toHaveCount(0)
  await expect(page.getByText("Right Value")).toHaveCount(0)
  await expect(page.getByText("Operand", { exact: true })).toHaveCount(0)
  await expect(page.getByText("Threshold", { exact: true })).toHaveCount(0)
  await expect(page.getByText("When True")).toBeVisible()
  await expect(page.getByText("When False")).toBeVisible()
  await branchDialog.getByRole("button", { name: "Close" }).last().click()
  await expect(branchDialog).toBeHidden()
  await page.getByRole("button", { name: "Raw JSON" }).click()
  const rawRouterConfig = JSON.parse(
    await page.getByRole("textbox", { name: "Raw JSON" }).inputValue(),
  )
  const rawBranch = rawRouterConfig.blocks.find(
    (block: { id: string }) => block.id === "branch-1",
  )
  expect(rawBranch.condition).toMatchObject({
    operator: "gte",
    left: {
      op: "multiply",
      left: {
        account: "credential:openai",
        metric: "limit_pressure",
      },
      right: {
        value: 100,
      },
    },
    right: {
      value: 75,
    },
  })
  await page.getByRole("button", { name: "UI Editor" }).click()

  await page.getByRole("button", { name: "Edit block account-1" }).click()
  const reopenedAccountDialog = page.getByRole("dialog", { name: "account-1" })
  await expect(reopenedAccountDialog).toBeVisible()
  await page.getByRole("combobox", { name: "Fallback Connection" }).click()
  await page.getByRole("option", { name: "load-balancer-1" }).click()
  await reopenedAccountDialog
    .getByRole("button", { name: "Close" })
    .last()
    .click()
  await expect(reopenedAccountDialog).toBeHidden()

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

test("tool adaptation profile override dialog fits the viewport", async ({
  page,
}) => {
  const errors = collectPageErrors(page)

  await gotoMockedRoute(page, "/agent/tools?tab=adaptation")
  await expect(page.getByRole("heading", { name: "Adaptation" })).toBeVisible()
  await page
    .getByRole("button", { name: "Add override for openai / gpt-4o-mini" })
    .click()

  await expect(
    page.getByRole("dialog", { name: "Add profile override" }),
  ).toBeVisible()
  await expectElementFitsViewport(page, '[role="dialog"]', "profile override")
  await expectNoHorizontalOverflow(page)
  await expectNoSeriousA11yViolations(page)
  expect(errors).toEqual([])
})

test("tool adaptation worst-state row fits a narrow mobile viewport", async ({
  page,
}, testInfo) => {
  test.skip(testInfo.project.name !== "mobile", "mobile-only layout regression")
  await page.setViewportSize({ width: 320, height: 720 })
  const errors = collectPageErrors(page)

  await gotoMockedRoute(page, "/agent/tools?tab=adaptation")
  const unavailableProbe = page.getByRole("button", {
    name: /^Probe unavailable for /,
  })
  await expect(unavailableProbe).toBeVisible()
  await expect(unavailableProbe).toHaveAttribute("aria-describedby", /.+/)
  await expect(
    page.getByText(
      "No configured credentials or endpoint are available for this profile.",
    ),
  ).toBeVisible()

  const mobileMetrics = page.getByTestId(
    "adaptation-profile-mobile-metrics-openai/very-long-model-name-with-reasoning-context-and-tool-capabilities",
  )
  await expect(mobileMetrics).toBeVisible()
  await expect(mobileMetrics.getByText("Surface")).toBeVisible()
  await expect(mobileMetrics.getByText("simple")).toBeVisible()
  await expect(mobileMetrics.getByText("Cache")).toBeVisible()
  await expect(mobileMetrics.getByText("Flexible")).toBeVisible()

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

test("workflow Operate shows only the sanitized published definition inspection", async ({
  page,
}, testInfo) => {
  test.skip(
    testInfo.project.name !== "desktop",
    "desktop workflow inspection coverage",
  )
  const errors = collectPageErrors(page)
  const inspectionRequests: NonNullable<
    MockLauncherApiOptions["workflowInspectionRequests"]
  > = []

  await gotoMockedRoute(page, "/agent/workflows", {
    workflowInspectionRequests: inspectionRequests,
  })
  await page.getByRole("button", { name: "Operate" }).click()

  const inspectionTrigger = page.getByRole("button", {
    name: "Published definition: workflows/summarize-text.yml",
  })
  await expect(inspectionTrigger).toBeVisible()
  const inspection = inspectionTrigger.locator("..").locator("..")
  await expect(inspection).toContainText("Inspected")
  await expect(inspection).toContainText("workflows/summarize-text.yml")
  await expect(inspection).toContainText("issues.opened")
  await expect(inspection).toContainText("review")
  await expect(inspection).toContainText("main")
  await expect(inspection).toContainText("Possible effects")
  await expect(inspection).toContainText("model or delegated action possible")

  expect(inspectionRequests.length).toBeGreaterThan(0)
  for (const request of inspectionRequests) {
    expect(request).toEqual({
      method: "POST",
      path: "/api/workflows/definitions/inspect",
      body: { ref: "workflows/summarize-text.yml" },
    })
  }
  await expect(inspection).not.toContainText(workflowInspectionSecretCanary)
  await expect(inspection).not.toContainText(workflowInspectionRawYAMLCanary)
  await expectNoHorizontalOverflow(page)
  await expectNoSeriousA11yViolations(page)
  expect(errors).toEqual([])
})

test("workflow capability catalog is lazy, searchable, and available across workflow modes", async ({
  page,
}, testInfo) => {
  test.skip(
    testInfo.project.name !== "desktop",
    "desktop workflow capability coverage",
  )
  const errors = collectPageErrors(page)
  const capabilityRequests: NonNullable<
    MockLauncherApiOptions["workflowCapabilityRequests"]
  > = []

  await gotoMockedRoute(page, "/agent/workflows", {
    workflowCapabilityRequests: capabilityRequests,
  })
  const capabilitiesButton = page.getByRole("button", {
    name: "Workflow capabilities",
  })
  await expect(capabilitiesButton).toBeVisible()
  expect(capabilityRequests).toEqual([])

  await capabilitiesButton.click()
  const dialog = page.getByRole("dialog", {
    name: "Workflow capabilities",
  })
  await expect(dialog).toBeVisible()
  await expect(
    dialog.getByRole("region", { name: "Workflow capability results" }),
  ).toHaveAttribute("tabindex", "0")
  await expect(dialog.getByText("agent/main")).toBeVisible()
  await expect(dialog.getByText("tool/message")).toBeVisible()
  await expect(dialog.getByText("mcp/github/create_issue")).toBeVisible()
  await expect(dialog.getByText("Additional property values")).toBeVisible()
  await expect(
    dialog.getByRole("button", { name: "Copy agent/reviewer" }),
  ).toBeDisabled()
  expect(capabilityRequests).toEqual([
    {
      method: "GET",
      path: "/api/workflows/authoring/capabilities",
    },
  ])

  await dialog
    .getByRole("searchbox", { name: "Search capabilities" })
    .fill("create_issue")
  await expect(dialog.getByText("mcp/github/create_issue")).toBeVisible()
  await expect(dialog.getByText("agent/main")).toHaveCount(0)
  await dialog.getByRole("button", { name: "MCP tools" }).click()
  await expect(
    dialog.getByText(
      "No capabilities match the current search and category filters.",
    ),
  ).toBeVisible()
  await expectNoHorizontalOverflow(page)
  await expectNoSeriousA11yViolations(page)
  await dialog.getByRole("button", { name: "Close" }).click()

  await page.getByRole("button", { name: "Operate" }).click()
  await expect(capabilitiesButton).toBeVisible()
  await page.getByRole("button", { name: "Develop" }).click()
  await page
    .getByPlaceholder("Describe the workflow outcome")
    .fill("Triage support tickets")
  await page.getByRole("button", { name: "Start with AI" }).click()
  await expect(
    page.getByRole("heading", { name: "Workflow YAML", exact: true }),
  ).toBeVisible()
  await expect(capabilitiesButton).toBeVisible()
  await expect(capabilitiesButton).toBeEnabled()

  await expectNoHorizontalOverflow(page)
  expect(errors).toEqual([])
})

test("workflow capability catalog fits and wraps exact targets at 320px", async ({
  page,
}, testInfo) => {
  test.skip(
    testInfo.project.name !== "mobile",
    "mobile workflow capability coverage",
  )
  await page.setViewportSize({ width: 320, height: 720 })
  const errors = collectPageErrors(page)

  await gotoMockedRoute(page, "/agent/workflows")
  await page.getByRole("button", { name: "Workflow capabilities" }).click()
  const dialog = page.getByRole("dialog", {
    name: "Workflow capabilities",
  })
  await expect(dialog).toBeVisible()
  await dialog
    .getByRole("searchbox", { name: "Search capabilities" })
    .fill(workflowCapabilityLongToolName)
  await expect(
    dialog.getByText(`tool/${workflowCapabilityLongToolName}`),
  ).toBeVisible()
  await expectElementFitsViewport(
    page,
    '[role="dialog"]',
    "workflow capability catalog",
  )
  await expectNoHorizontalOverflow(page)
  await expectNoSeriousA11yViolations(page)
  expect(errors).toEqual([])
})

test("narrow mobile lazily opens a sanitized built-in workflow inspection", async ({
  page,
}, testInfo) => {
  test.skip(
    testInfo.project.name !== "mobile",
    "mobile workflow inspection coverage",
  )
  await page.setViewportSize({ width: 320, height: 720 })
  const errors = collectPageErrors(page)
  const inspectionRequests: NonNullable<
    MockLauncherApiOptions["workflowInspectionRequests"]
  > = []

  await gotoMockedRoute(page, "/agent/workflows", {
    workflowInspectionRequests: inspectionRequests,
  })
  const template = page.getByRole("article", {
    name: "Code review template",
  })
  await expect(template).toBeVisible()
  expect(inspectionRequests).toEqual([])

  await template
    .getByRole("button", { name: "Built-in definition: code-review" })
    .click()
  await expect(template.getByText("Inspected")).toBeVisible()
  await expect(template).toContainText("code-review")
  await expect(template).toContainText("issues.opened")
  await expect(template).toContainText("main")

  expect(inspectionRequests.length).toBeGreaterThan(0)
  for (const request of inspectionRequests) {
    expect(request).toEqual({
      method: "GET",
      path: "/api/workflows/templates/code-review/inspect",
      body: null,
    })
  }
  await expect(template).not.toContainText(workflowInspectionSecretCanary)
  await expect(template).not.toContainText(workflowInspectionRawYAMLCanary)
  await expectNoHorizontalOverflow(page)
  await expectNoSeriousA11yViolations(page)
  expect(errors).toEqual([])
})

test("mobile workflow trigger simulator reviews one explicit redacted scenario", async ({
  page,
}, testInfo) => {
  test.skip(
    testInfo.project.name !== "mobile",
    "mobile workflow trigger simulator coverage",
  )
  const errors = collectPageErrors(page)
  const simulationRequests: NonNullable<
    MockLauncherApiOptions["workflowTriggerSimulationRequests"]
  > = []
  const executionRequests: NonNullable<
    MockLauncherApiOptions["workflowTriggerExecutionRequests"]
  > = []
  const payloadRequests: string[] = []
  const secretCanary = "mobile-trigger-secret-value-must-not-render"

  await gotoMockedRoute(page, "/agent/workflows?mode=develop", {
    workflowDevelopmentYAML: workflowEventDraftYAML,
    workflowTriggerSimulationRequests: simulationRequests,
    workflowTriggerExecutionRequests: executionRequests,
    workflowEventPayloadRequests: payloadRequests,
  })
  await page.waitForURL((url) => {
    return (
      !url.searchParams.has("mode") &&
      url.searchParams.get("workflow") === "workflows/summarize-text.yml" &&
      url.searchParams.get("run") === "wr_test"
    )
  })
  await page
    .getByPlaceholder("Describe the workflow outcome")
    .fill("Triage support tickets from calls and durable events")
  const startWithAI = page.getByRole("button", { name: "Start with AI" })
  await expect(startWithAI).toBeEnabled()
  await startWithAI.click()

  await expect(
    page.getByText("Trigger simulator", { exact: true }),
  ).toBeVisible()
  const triggerScenario = page.getByRole("combobox", {
    name: "Trigger scenario",
  })
  await expect(triggerScenario).toHaveValue("")
  await expect(page.getByText(/dashboard will not guess/i)).toBeVisible()
  expect(simulationRequests).toEqual([])
  await expectNoHorizontalOverflow(page)

  await triggerScenario.selectOption("event")
  const durableEvent = page.getByRole("combobox", { name: "Durable event" })
  await durableEvent.selectOption(eventResponse.id)
  await expect
    .poll(() =>
      simulationRequests.some(
        (entry) =>
          (entry.body.trigger as { type?: string }).type === "event" &&
          (entry.body.scenario as { event_id?: string }).event_id ===
            eventResponse.id,
      ),
    )
    .toBe(true)
  await expect(
    page.getByText(/protected payload content stays server-side/i),
  ).toBeVisible()
  expect(payloadRequests).toEqual([])
  await expect(page.locator("body")).not.toContainText(eventPayloadText)

  await triggerScenario.selectOption("workflow_call")
  await page.getByLabel("Inputs JSON").fill('{"ticket":"PIC-mobile-review"}')
  await page
    .getByLabel("Secrets JSON")
    .fill(JSON.stringify({ github_token: secretCanary }))
  await expect
    .poll(() => {
      const latest = simulationRequests.at(-1)?.body
      return (
        (latest?.trigger as { type?: string } | undefined)?.type ===
          "workflow_call" &&
        (latest?.scenario as { secrets?: Record<string, string> } | undefined)
          ?.secrets?.github_token === secretCanary
      )
    })
    .toBe(true)

  const reviewButton = page.getByRole("button", {
    name: "Review & execute",
  })
  await expect(reviewButton).toBeEnabled()
  await reviewButton.click()
  const dialog = page.getByRole("dialog", {
    name: "Review trigger execution",
  })
  await expect(dialog).toBeVisible()
  await expect(dialog).toContainText("Provided secrets")
  await expect(dialog).not.toContainText(secretCanary)
  await expect(dialog).not.toContainText(eventPayloadText)
  await expectElementFitsViewport(
    page,
    '[role="dialog"]',
    "workflow trigger execution review",
  )
  await expectNoHorizontalOverflow(page)

  await dialog
    .getByRole("switch", {
      name: "I reviewed this server simulation and its possible effects",
    })
    .click()
  const confirm = dialog.getByRole("button", {
    name: "Confirm and execute",
  })
  await expect(confirm).toBeEnabled()
  await confirm.evaluate((button: HTMLButtonElement) => {
    button.click()
    button.click()
  })
  await expect.poll(() => executionRequests.length).toBe(1)
  expect(executionRequests).toEqual([
    {
      method: "POST",
      path: "/api/workflows/development/test/execute",
      body: expect.objectContaining({
        trigger: { type: "workflow_call" },
        scenario: expect.objectContaining({
          inputs: { ticket: "PIC-mobile-review" },
          secrets: { github_token: secretCanary },
        }),
        review_token: "review-token:workflow_call",
      }),
    },
  ])
  await expect(dialog).toBeHidden()
  expect(payloadRequests).toEqual([])
  await expect(page.locator("body")).not.toContainText(eventPayloadText)
  await expectNoHorizontalOverflow(page)
  expect(errors).toEqual([])
})

test("workflow event builder routes one exact durable event through server review", async ({
  page,
}) => {
  const errors = collectPageErrors(page)

  await gotoMockedRoute(page, "/agent/workflows")
  await page
    .getByPlaceholder("Describe the workflow outcome")
    .fill("Triage support tickets")
  await page.getByRole("button", { name: "Start with AI" }).click()
  await expect(
    page.getByRole("heading", { name: "Workflow YAML", exact: true }),
  ).toBeVisible()

  await page.getByRole("tab", { name: "Builder" }).click()
  await page.getByRole("combobox", { name: "Trigger type" }).click()
  await page
    .getByRole("option", { name: "Durable event · Not configured" })
    .click()
  await page
    .getByRole("switch", { name: "Enable durable event trigger" })
    .click()
  await expect(page.getByText("Deterministic event routing")).toBeVisible()
  await page
    .getByRole("textbox", { name: "Sources", exact: true })
    .fill("github")
  await page
    .getByRole("textbox", { name: "Event types", exact: true })
    .fill("issues.opened")
  await expect(
    page.getByRole("button", { name: "Apply to YAML" }),
  ).toBeEnabled()
  await page.getByRole("button", { name: "Apply to YAML" }).click()

  const triggerScenario = page.getByRole("combobox", {
    name: "Trigger scenario",
  })
  await expect(triggerScenario).toHaveValue("")
  await triggerScenario.selectOption("event")
  const eventPicker = page.getByRole("combobox", { name: "Durable event" })
  await expect(eventPicker).toBeVisible()
  await expect(page.getByLabel("Secrets JSON")).toHaveCount(0)
  const reviewButton = page.getByRole("button", {
    name: "Review & execute",
  })
  await expect(reviewButton).toBeDisabled()
  await eventPicker.selectOption(eventResponse.id)
  await expect(
    page.getByText(/server-reviewed execution token is ready/i),
  ).toBeVisible()
  await expect(reviewButton).toBeEnabled()
  await expect(page.locator("body")).not.toContainText(eventPayloadText)

  await reviewButton.click()
  const eventReview = page.getByRole("dialog", {
    name: "Review trigger execution",
  })
  await expect(eventReview).toContainText("Event context")
  await expect(eventReview).toContainText("Yes")
  await expect(eventReview).not.toContainText(eventPayloadText)
  await eventReview
    .getByRole("switch", {
      name: "I reviewed this server simulation and its possible effects",
    })
    .click()
  await eventReview.getByRole("button", { name: "Confirm and execute" }).click()
  await expect(page.getByText("wr_draft", { exact: true })).toBeVisible()

  await expect(page.locator("body")).not.toContainText(eventPayloadText)
  await expectNoHorizontalOverflow(page)
  expect(errors).toEqual([])
})

test("workflow jobs builder applies a surgical action field edit", async ({
  page,
}, testInfo) => {
  if (testInfo.project.name === "mobile") {
    await page.setViewportSize({ width: 320, height: 720 })
  }
  const errors = collectPageErrors(page)
  const jobRequests: NonNullable<
    MockLauncherApiOptions["workflowJobRequests"]
  > = []

  await gotoMockedRoute(page, "/agent/workflows", {
    workflowJobRequests: jobRequests,
  })
  await page
    .getByPlaceholder("Describe the workflow outcome")
    .fill("Triage support tickets")
  await page.getByRole("button", { name: "Start with AI" }).click()
  await page.getByRole("tab", { name: "Builder" }).click()
  await page.getByRole("tab", { name: "Jobs & actions" }).click()

  await expect(page.getByText("Job graph")).toBeVisible()
  await expect(page.getByText("agent/main").first()).toBeVisible()
  const actionSection = page
    .getByRole("heading", { name: "Action 1" })
    .locator("xpath=ancestor::section[1]")
  await actionSection
    .getByRole("combobox", { name: "Display name mutation" })
    .click()
  await page.getByRole("option", { name: "Set value" }).click()
  await actionSection
    .getByRole("textbox", { name: "Display name value" })
    .fill("Summarize ticket")
  await page.getByRole("button", { name: "Apply fields to YAML" }).click()

  await expect
    .poll(
      () =>
        jobRequests.filter(
          (request) =>
            request.path === "/api/workflows/development/jobs/render",
        ).length,
    )
    .toBe(1)
  const renderRequest = jobRequests.find(
    (request) => request.path === "/api/workflows/development/jobs/render",
  )
  expect(renderRequest?.body).toMatchObject({
    operation: {
      type: "step.patch",
      job_id: "triage",
      step_index: 0,
      fields: {
        name: { mode: "set", value: "Summarize ticket" },
      },
    },
  })
  await expect(
    actionSection.getByText("Source: Summarize ticket"),
  ).toBeVisible()
  await expectNoHorizontalOverflow(page)
  await expectNoSeriousA11yViolations(page)
  expect(errors).toEqual([])
})

test("workflow dashboard supports AI draft, publish, and manual run loop", async ({
  page,
}) => {
  test.slow()
  const errors = collectPageErrors(page)

  await gotoMockedRoute(page, "/agent/workflows", {
    completeDraftViaPolling: true,
  })
  await expect(
    page
      .locator(
        '[title="workflow must be revalidated after the current Picoclaw version change"]',
      )
      .first(),
  ).toBeAttached()
  await expect(
    page.getByRole("heading", { name: "Built-in templates" }),
  ).toBeVisible()
  await expect(
    page.getByRole("article", { name: "Code review template" }),
  ).toBeVisible()
  await expect(page.getByRole("button", { name: "AI Review" })).toBeVisible()
  await page.getByRole("button", { name: "AI Review" }).click()
  await expect(
    page.getByRole("heading", { name: "Workflow YAML", exact: true }),
  ).toBeVisible()
  await expect(page.getByRole("textbox", { name: "AI brief" })).toHaveValue(
    /Review this workflow against the current PicoClaw runtime/,
  )
  await expect(
    page.getByText(
      "Finish or discard the active workflow draft before installing or restoring templates.",
    ),
  ).toBeVisible()
  await expect(page.getByRole("button", { name: "Install" })).toBeDisabled()
  await expect(
    page.getByRole("button", { name: "Restore built-in" }),
  ).toBeDisabled()
  await expect(page.getByText("version revalidation")).toBeVisible()
  await page.getByRole("button", { name: "Discard" }).click()
  await expect(page.getByText("New workflow")).toBeVisible()

  await page.getByRole("button", { name: "Operate" }).click()
  await page
    .getByRole("button", { name: "Inspect workflow dependencies" })
    .click()
  const publishedReadiness = page.getByRole("region", {
    name: "Published workflow dependency readiness",
  })
  await expect(publishedReadiness).toContainText("workflows/summarize-text.yml")
  await expect(publishedReadiness).toContainText("Runtime dependencies (1)")
  await expect(publishedReadiness).toContainText("agent/main")
  await page.keyboard.press("Escape")
  await expect(page.getByText("Run workflow").first()).toBeVisible()
  await page.getByRole("button", { name: "Run workflow" }).first().click()
  await expect(
    page.getByText("Revalidate this workflow before running it."),
  ).toBeVisible()
  await expect(
    page.getByRole("button", { name: "Run workflow" }).last(),
  ).toBeDisabled()
  await page.keyboard.press("Escape")
  await expect(
    page.getByRole("button", { name: "Retry", exact: true }),
  ).toBeDisabled()
  await expect(
    page.getByRole("button", { name: "Retry", exact: true }),
  ).toHaveAttribute(
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
  await expect(
    page.getByRole("button", { name: "Retry", exact: true }),
  ).toBeDisabled()
  await retrySecrets.fill('{"token":"retry-token"}')
  await expect(page.getByText("Ready to retry.")).toBeVisible()
  await expect(
    page.getByRole("button", { name: "Retry", exact: true }),
  ).toBeEnabled()
  await page.getByRole("button", { name: "Retry", exact: true }).click()
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

  await expect(
    page.getByRole("heading", { name: "Workflow YAML", exact: true }),
  ).toBeVisible()
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
  const reviewDraft = page.getByRole("button", {
    name: "Review & execute",
  })
  await expect(reviewDraft).toBeEnabled()
  await page.getByLabel("Inputs JSON").fill("{")
  await expect(page.getByText(/Inputs must be valid JSON/)).toBeVisible()
  await expect(reviewDraft).toBeDisabled()
  await expect(page.getByRole("button", { name: "Publish" })).toBeDisabled()
  await expect(
    page.getByText("Run a successful draft test before publishing."),
  ).toBeVisible()

  await page.getByLabel("Inputs JSON").fill('{"ticket":"Printer is offline"}')
  await page.getByLabel("Session").fill("workflow:draft")
  await page
    .getByLabel("Delivery JSON")
    .fill('{"channel":"telegram","chat_id":"support"}')
  await expect(reviewDraft).toBeEnabled()
  await reviewDraft.click()
  await expect(page.getByText("wr_draft", { exact: true })).toHaveCount(0)
  await confirmTriggerExecutionReview(page)
  await expect(page.getByText("wr_draft", { exact: true })).toBeVisible()
  await expect(page.getByRole("button", { name: "Publish" })).toBeEnabled()
  await expect(page.getByText("Ready to publish.")).toBeVisible()

  const whitespaceOnlyDraft = `${workflowDraftYAML}\n`
  const whitespaceDependencyRequest = page.waitForRequest((request) => {
    if (
      !request.url().endsWith("/api/workflows/dependencies/check") ||
      request.method() !== "POST"
    ) {
      return false
    }
    const body = request.postDataJSON() as {
      draft?: { yaml?: string }
    }
    return body.draft?.yaml === whitespaceOnlyDraft
  })
  await yamlEditor.fill(whitespaceOnlyDraft)
  await whitespaceDependencyRequest
  await expect(page.getByRole("button", { name: "Publish" })).toBeDisabled()
  await expect(
    page.getByText("Validate the draft again after the latest edits."),
  ).toBeVisible()
  await yamlEditor.fill(workflowDraftYAML)
  await expect(page.getByRole("button", { name: "Publish" })).toBeEnabled()

  await yamlEditor.fill(`${workflowDraftYAML}# readiness is stale\n`)
  await expect(page.getByRole("button", { name: "Publish" })).toBeDisabled()
  await expect(
    page.getByText("Validate the draft again after the latest edits."),
  ).toBeVisible()
  await yamlEditor.fill(workflowDraftYAML)
  await expect(page.getByRole("button", { name: "Publish" })).toBeEnabled()

  await page.reload()
  await expect(
    page.getByRole("heading", { name: "Workflow YAML", exact: true }),
  ).toBeVisible()
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
  await expect(
    page.getByRole("heading", { name: "Workflow YAML", exact: true }),
  ).toBeVisible()
  await page.getByLabel("Inputs JSON").fill('{"ticket":"Printer is offline"}')
  await page.getByLabel("Session").fill("workflow:draft")
  await page
    .getByLabel("Delivery JSON")
    .fill('{"channel":"telegram","chat_id":"support"}')
  const reviewDraft = page.getByRole("button", {
    name: "Review & execute",
  })
  await expect(reviewDraft).toBeEnabled()
  await reviewDraft.click()
  await confirmTriggerExecutionReview(page)

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
  await sidebar.getByRole("link", { name: /Accounts/ }).click()
  await expect(page).toHaveURL(/\/accounts$/)
  await expect(sidebar).toBeHidden()
  await expectNoHorizontalOverflow(page)
  await expectNoSeriousA11yViolations(page)
  expect(errors).toEqual([])
})
