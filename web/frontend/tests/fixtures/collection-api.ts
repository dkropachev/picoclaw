import { type Page, type Route } from "@playwright/test"

export type CollectionVisualState = "ready" | "empty" | "error" | "loading"

const fixedNow = "2026-08-25T14:30:00Z"

const querySchemas = {
  aliases: schema([
    field("name", "string"),
    field("model", "string"),
    field("overrides", "number"),
    field("disabled_accounts", "number"),
  ]),
  routers: schema([
    field("name", "string"),
    field("enabled", "boolean", ["true", "false"]),
    field("blocks", "number"),
    field("rules", "number"),
  ]),
  mcp: schema([
    field("name", "string"),
    field("enabled", "boolean", ["true", "false"]),
    field("deferred", "boolean", ["true", "false"]),
    field("type", "enum", ["stdio", "http", "sse"]),
    field("auth", "enum", ["none", "custom", "bearer", "oauth"]),
  ]),
  agents: schema([
    field("id", "string"),
    field("name", "string"),
    field("workspace", "string"),
    field("account", "string"),
    field("model", "string"),
    field("default", "boolean", ["true", "false"]),
    field("implicit", "boolean", ["true", "false"]),
    field("position", "number"),
  ]),
  evaluations: schema([
    field("id", "string"),
    field("status", "enum", [
      "draft",
      "queued",
      "running",
      "completed",
      "failed",
      "canceled",
    ]),
    field("repository", "string"),
    field("ref", "string"),
    field("models", "number"),
    field("progress", "number"),
    field("version", "number"),
    field("created", "timestamp"),
    field("updated", "timestamp"),
  ]),
  reviews: schema([
    field("id", "string"),
    field("name", "string"),
    field("repository", "string"),
    field("branch", "string"),
    field("status", "enum", [
      "idle",
      "running",
      "stopping",
      "paused",
      "completed",
      "failed",
    ]),
    field("progress", "number"),
    field("reviewed", "number"),
    field("findings", "number"),
    field("updated", "timestamp"),
  ]),
  reviewProfiles: schema([
    field("id", "string"),
    field("name", "string"),
    field("account", "string"),
    field("reviewer", "string"),
    field("issue_writer", "string"),
    field("parallel", "number"),
    field("updated", "timestamp"),
  ]),
}

const aliases = [
  {
    name: "code",
    model: "gpt-5.6-codex",
    override_count: 1,
    disabled_account_count: 0,
    account_overrides: { "openai-primary": "gpt-5.6-codex" },
    disabled_accounts: [],
  },
  {
    name: "fast",
    model: "gpt-5.6-mini",
    override_count: 0,
    disabled_account_count: 1,
    account_overrides: {},
    disabled_accounts: ["offline-lab"],
  },
  {
    name: "review",
    model: "claude-sonnet-4.6",
    override_count: 0,
    disabled_account_count: 0,
    account_overrides: {},
    disabled_accounts: [],
  },
]

const routers = [
  {
    name: "task-router",
    enabled: true,
    entry: "entry",
    block_count: 3,
    rule_count: 1,
    blocks: [
      {
        id: "entry",
        type: "rules",
        rules: [{ match: "has_code", target: "code" }],
        fallback: "fast",
      },
      { id: "code", type: "model", model: "code" },
      { id: "fast", type: "model", model: "fast" },
    ],
  },
  {
    name: "media-router",
    enabled: false,
    entry: "entry",
    block_count: 1,
    rule_count: 1,
    blocks: [
      {
        id: "entry",
        type: "rules",
        rules: [{ match: "has_media", target: "review" }],
        fallback: "fast",
      },
    ],
  },
]

const mcpServers = [
  {
    name: "github",
    enabled: true,
    deferred: false,
    type: "http",
    url: "https://mcp.example.test/github",
    command: "",
    args: [],
    env_file: "",
    env_keys: [],
    header_keys: ["X-Workspace"],
    auth: { type: "oauth", configured: true, expired: false },
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
    auth: { type: "none", configured: false, expired: false },
  },
]

const mcpServerSummaries = mcpServers.map((server) => ({
  name: server.name,
  enabled: server.enabled,
  deferred: server.deferred,
  type: server.type,
  address:
    server.type === "stdio"
      ? [server.command, ...server.args].filter(Boolean).join(" ")
      : server.url,
  environment_key_count: server.env_keys.length,
  header_key_count: server.header_keys.length,
  auth: server.auth,
}))

const agents = [
  {
    id: "main",
    name: "Main agent",
    workspace: "/workspace/main",
    account_ref: "openai-primary",
    model: { primary: "code", fallbacks: ["fast"] },
    skills: null,
    subagents: null,
    is_default: true,
    default_configured: true,
    implicit: false,
  },
  {
    id: "reviewer",
    name: "Repository reviewer",
    workspace: "/workspace/reviewer",
    account_ref: "anthropic-review",
    model: { primary: "review", fallbacks: [] },
    skills: ["repository-review"],
    subagents: { allow: ["main"] },
    is_default: false,
    default_configured: true,
    implicit: false,
  },
]

const progress = {
  stage: "draft",
  languages: {},
  total_files: 0,
  selected_files: 0,
  completed_files: 0,
  total_tasks: 0,
  completed_tasks: 0,
  percent: 0,
  updated_at: fixedNow,
}

const usage = {
  requests: 0,
  input_tokens: 0,
  cached_input_tokens: 0,
  output_tokens: 0,
  reasoning_tokens: 0,
  duration_millis: 0,
}

const evaluation = {
  schema_version: 1,
  id: "rme_0123456789abcdef0123456789abcdef",
  version: 3,
  status: "draft",
  repository: "octo/picoclaw",
  ref: "main",
  candidate_models: ["code", "fast", "review"],
  selector_model_alias: "code",
  judge_model_alias: "review",
  focus: {},
  default_files_per_language: 20,
  files_per_language: {},
  profile: {
    id: "balanced",
    version: 2,
    name: "Balanced review",
    reviewer_model: "review",
    account_ref: "anthropic-review",
    review_focus: "Correctness and safety",
    focus: {},
    max_files_per_batch: 20,
    max_content_bytes_per_batch: 100000,
    max_parallel_children: 2,
  },
  progress,
  usage,
  model_stats: {},
  comparisons: [],
  warnings: [],
  run_ids: [],
  created_at: "2026-08-25T13:00:00Z",
  updated_at: fixedNow,
}

export const repositoryReviewVisualIDs = {
  automation: "rra_visual",
  finding: "rfn_visual_1",
  secondFinding: "rfn_visual_2",
  repositoryFinding: "rrf_visual_1",
  issue: "rrid_visual_1",
  failedIssue: "rrid_visual_2",
  generation: "rig_visual",
} as const

const repositoryReviewCommit = "a".repeat(40)

const repositoryReviewAutomation = {
  id: repositoryReviewVisualIDs.automation,
  version: 9,
  profile_id: "rrpf_visual",
  profile_version: 4,
  branch: "main",
  name: "Correctness review",
  repository: "octo/picoclaw",
  ref: "main",
  target: "all",
  account_ref: "openai-primary",
  effective_account_ref: "openai-primary",
  review_focus: "Find concrete correctness and reliability defects.",
  scope_policy: {
    code_types: ["hotpath-code", "code", "test"],
    include_folders: ["pkg", "web"],
    exclude_folders: ["vendor"],
    free_text: "Prioritize persistent state transitions.",
  },
  reviewer_models: ["review"],
  issue_writer_model: "issue-writer",
  compare_models: false,
  force: false,
  max_files_per_run: 24,
  max_content_bytes: 524288,
  max_parallel_children: 4,
  auto_continue: true,
  model_prices: {},
  budget: { guard_expression: "tokens.total < 250000" },
  status: "completed",
  run_ids: ["rrun_visual_1", "rrun_visual_2"],
  usage: {
    prompt_tokens: 48240,
    completion_tokens: 7840,
    total_tokens: 56080,
    cached_tokens: 12120,
  },
  estimated_cost_usd: 1.27,
  progress: {
    stage: "complete",
    completed_batches: 5,
    total_batches: 5,
    reviewed_files: 40,
    remaining_files: 0,
    unsupported_files: 1,
    findings: 2,
  },
  model_stats: [],
  account_limits: [],
  scope_plan: {
    commit_sha: repositoryReviewCommit,
    policy_hash: "sha256:visual-policy",
    hash: "sha256:visual-scope",
    summary: "40 source and test files pinned at the selected commit.",
    warnings: ["One generated fixture file was unsupported."],
    counts: {
      total_files: 48,
      code_type_files: 46,
      include_files: 43,
      excluded_files: 3,
      selected_files: 40,
    },
  },
  resolved_commit_sha: repositoryReviewCommit,
  started_at: "2026-08-25T13:00:00Z",
  completed_at: "2026-08-25T14:20:00Z",
  created_at: "2026-08-24T12:00:00Z",
  updated_at: "2026-08-25T14:20:00Z",
}

const repositoryReviewProfile = {
  id: "rrpf_visual",
  version: 4,
  name: "Correctness review",
  account_ref: "openai-primary",
  reviewer_model: "review",
  issue_writer_model: "issue-writer",
  review_focus: "Find concrete correctness and reliability defects.",
  issue_prompt: "Present the confirmed diagnosis with evidence and provenance.",
  scope_policy: {
    code_types: ["hotpath-code", "code", "test"],
    include_folders: ["pkg", "web"],
    exclude_folders: ["vendor"],
    free_text: "Prioritize persistent state transitions.",
  },
  force: false,
  auto_continue: true,
  max_files_per_run: 24,
  max_content_bytes: 524288,
  max_parallel_children: 4,
  budget: { guard_expression: "" },
  created_at: "2026-08-20T12:00:00Z",
  updated_at: fixedNow,
}

const repositoryReviewSummary = {
  schema_version: 1,
  id: "rrs_visual",
  repository: "octo/picoclaw",
  version: 14,
  review_version: 5,
  last_commit_sha: repositoryReviewCommit,
  finding_count: 2,
  open_finding_count: 2,
  issue_draft_count: 2,
  unsupported_count: 1,
  reviewed_file_count: 40,
  excluded_file_count: 3,
  updated_at: "2026-08-25T14:20:00Z",
}

const repositoryReviewFindings = [
  {
    id: repositoryReviewVisualIDs.finding,
    fingerprint: "sha256:visual-lost-update",
    repository: "octo/picoclaw",
    commit_sha: repositoryReviewCommit,
    file: {
      path: "pkg/repoaudit/store.go",
      blob_sha: "b".repeat(40),
      size_bytes: 16842,
      category: "hotpath-code",
    },
    line: 418,
    severity: "high",
    title: "Concurrent checkpoint writes can lose a finding",
    symbol: "Store.SaveFinding",
    message: "The read-modify-write sequence has no version fence.",
    evidence:
      "Two callers load the same ledger version and each replaces the findings slice; the later atomic rename discards the earlier checkpoint.",
    impact:
      "A validated repository finding can disappear from the findings view.",
    validation: {
      status: "confirmed",
      summary: "Traced both writers through the atomic rename path.",
      checks: ["Compared caller snapshots", "Verified no CAS guard"],
    },
    context_ids: ["rrctx_visual_1"],
    models: ["review", "code"],
    observation_count: 2,
    observations: [
      {
        context_id: "rrctx_visual_1",
        model: "review",
        reviewer: "review-child-2",
        severity: "high",
        title: "Concurrent checkpoint writes can lose a finding",
        symbol: "Store.SaveFinding",
        line: 418,
        evidence: "Both writers persist snapshots derived from version 13.",
        impact: "One validated checkpoint is overwritten.",
        validation: {
          status: "confirmed",
          summary: "Interleaving reproduced from both call paths.",
        },
      },
    ],
    status: "open",
    issue_draft_id: repositoryReviewVisualIDs.issue,
    repository_finding_id: repositoryReviewVisualIDs.repositoryFinding,
    repository_match_state: "known",
    version: 3,
    created_at: "2026-08-25T14:05:00Z",
    updated_at: "2026-08-25T14:10:00Z",
  },
  {
    id: repositoryReviewVisualIDs.secondFinding,
    fingerprint: "sha256:visual-cancel-retry",
    repository: "octo/picoclaw",
    commit_sha: repositoryReviewCommit,
    file: {
      path: "pkg/repoaudit/control.go",
      blob_sha: "c".repeat(40),
      size_bytes: 9240,
      category: "code",
    },
    line: 206,
    severity: "medium",
    title: "Canceled batches are retried without backoff",
    symbol: "Controller.continueRun",
    message: "Cancellation is classified as a transient review error.",
    evidence: "context.Canceled reaches the immediate continuation branch.",
    impact: "Shutdown can produce a tight retry loop and duplicate work.",
    validation: {
      status: "confirmed",
      summary: "Followed cancellation through continuation scheduling.",
      checks: ["Verified the retry delay remains zero"],
    },
    context_ids: ["rrctx_visual_2"],
    models: ["review"],
    observation_count: 1,
    status: "open",
    issue_draft_id: repositoryReviewVisualIDs.failedIssue,
    repository_finding_id: "rrf_visual_2",
    repository_match_state: "known",
    version: 2,
    created_at: "2026-08-25T14:12:00Z",
    updated_at: "2026-08-25T14:15:00Z",
  },
]

const repositoryFindings = repositoryReviewFindings.map((finding, index) => ({
  id:
    index === 0
      ? repositoryReviewVisualIDs.repositoryFinding
      : `rrf_visual_${index + 1}`,
  repository: finding.repository,
  canonical_title: finding.title,
  canonical_severity: finding.severity,
  review_finding_ids: [finding.id],
  found_commits: [finding.commit_sha],
  path_symbol_history: [
    {
      review_finding_id: finding.id,
      commit_sha: finding.commit_sha,
      path: finding.file.path,
      symbol: finding.symbol,
      observed_at: finding.created_at,
    },
  ],
  match_state: "known",
  lifecycle: "open",
  issue: {
    state: "draft",
    origin: "ai_generated",
    title: finding.title,
    snapshot_at: finding.updated_at,
  },
  validation_state: "not_requested",
  version: 1,
  created_at: finding.created_at,
  updated_at: finding.updated_at,
}))

const repositoryReviewContexts = repositoryReviewFindings.map(
  (finding, index) => ({
    id: finding.context_ids[0],
    repository: finding.repository,
    commit_sha: finding.commit_sha,
    inventory_hash: "sha256:visual-inventory",
    profile_hash: "sha256:visual-profile",
    run_id: `rrun_visual_${index + 1}`,
    model: finding.models[0],
    reviewer: `review-child-${index + 1}`,
    files: [finding.file],
    raw_digest: `sha256:visual-context-${index + 1}`,
    created_at: finding.created_at,
  }),
)

const repositoryReviewIssues = [
  {
    id: repositoryReviewVisualIDs.issue,
    repository: "octo/picoclaw",
    finding_ids: [repositoryReviewVisualIDs.finding],
    origin: "ai_generated",
    generation_id: repositoryReviewVisualIDs.generation,
    resolved_instructions:
      "Write a concise grounded issue with evidence, impact, validation, location, and commit provenance.",
    instructions_mode: "default",
    generator_model: "issue-writer",
    generator_account: "openai-primary",
    generator_profile_id: "rrpf_visual",
    generator_profile_version: 4,
    canonical: true,
    publishable: true,
    deletable: true,
    regeneratable: true,
    title: "Concurrent checkpoint writes can lose a finding",
    body: [
      "## Evidence",
      "",
      "Two writers persist snapshots derived from the same ledger version.",
      "",
      "| Location | Commit |",
      "| --- | --- |",
      `| \`pkg/repoaudit/store.go:418\` | \`${repositoryReviewCommit}\` |`,
      "",
      "## Impact",
      "",
      "A validated finding can disappear from the findings view.",
      "",
      "## Validation",
      "",
      "- Compared both caller snapshots",
      "- Verified there is no version fence",
    ].join("\n"),
    labels: ["bug", "data-loss"],
    state: "editing",
    version: 3,
    created_at: "2026-08-25T14:10:00Z",
    updated_at: "2026-08-25T14:10:00Z",
  },
  {
    id: repositoryReviewVisualIDs.failedIssue,
    repository: "octo/picoclaw",
    finding_ids: [repositoryReviewVisualIDs.secondFinding],
    origin: "ai_generated",
    generation_id: repositoryReviewVisualIDs.generation,
    resolved_instructions:
      "Write a concise grounded issue with evidence, impact, validation, location, and commit provenance.",
    instructions_mode: "default",
    generator_model: "issue-writer",
    generator_account: "openai-primary",
    generator_profile_id: "rrpf_visual",
    generator_profile_version: 4,
    generation_error: "The issue writer returned an invalid structured body.",
    canonical: true,
    publishable: false,
    deletable: true,
    regeneratable: true,
    title: "",
    body: "",
    labels: [],
    state: "failed",
    version: 1,
    created_at: "2026-08-25T14:15:00Z",
    updated_at: "2026-08-25T14:15:00Z",
  },
]

const repositoryReviewCapabilities = {
  github: true,
  can_generate: true,
  can_publish: true,
  can_search_issues: true,
  can_link_issue: true,
  can_edit: true,
  can_delete: true,
  can_regenerate: true,
}

export async function installCollectionVisualMocks(
  page: Page,
  state: CollectionVisualState = "ready",
) {
  await page.route(
    (url) => url.pathname.startsWith("/api/"),
    async (route) => {
      const request = route.request()
      const url = new URL(request.url())
      const path = url.pathname
      const method = request.method()

      if (method === "GET" && isCollectionList(path)) {
        if (state === "loading") await delay(5_000)
        if (state === "error") {
          return json(
            route,
            {
              code: "invalid_query",
              message: "Expected a value after the comparison operator.",
              position: 7,
            },
            400,
          )
        }
      }

      if (method !== "GET") {
        if (path.endsWith("/bulk-delete")) {
          return json(route, { deleted_ids: [], failures: [] })
        }
        return json(route, { status: "ok" })
      }

      switch (path) {
        case "/api/auth/status":
          return json(route, { authenticated: true, initialized: true })
        case "/api/gateway/status":
          return json(route, {
            gateway_status: "running",
            gateway_start_allowed: true,
            gateway_restart_required: false,
            boot_default_model: "code",
            config_default_model: "code",
          })
        case "/api/gateway/logs":
          return json(route, { logs: [], log_total: 0, log_run_id: 1 })
        case "/api/channels/catalog":
          return json(route, { channels: [] })
        case "/api/config":
          return json(route, { channels: {}, channel_list: {} })
        case "/api/model-aliases":
          return json(route, {
            model_aliases: state === "empty" ? [] : aliases,
            total: state === "empty" ? 0 : aliases.length,
            next_cursor: "",
            canonical_query: url.searchParams.get("query") ?? "",
            query_schema: querySchemas.aliases,
            config_revision: "cfg-visual-1",
          })
        case "/api/model-routers":
          return json(route, {
            model_routers: state === "empty" ? [] : routers,
            total: state === "empty" ? 0 : routers.length,
            next_cursor: "",
            canonical_query: url.searchParams.get("query") ?? "",
            query_schema: querySchemas.routers,
            config_revision: "cfg-visual-1",
          })
        case "/api/mcp/servers":
          return json(route, {
            servers: state === "empty" ? [] : mcpServerSummaries,
            total: state === "empty" ? 0 : mcpServerSummaries.length,
            next_cursor: "",
            canonical_query: url.searchParams.get("query") ?? "",
            query_schema: querySchemas.mcp,
            config_revision: "cfg-visual-1",
          })
        case "/api/mcp/settings":
        case "/api/mcp":
          return json(route, {
            enabled: true,
            discovery: {
              enabled: false,
              ttl: 5,
              max_search_results: 5,
              use_bm25: true,
              use_regex: false,
            },
            servers: mcpServers,
          })
        case "/api/agents":
          return json(route, {
            agents: state === "empty" ? [] : agents,
            default_agent_id: "main",
            config_revision: "cfg-visual-1",
            effects: effects(),
            total: state === "empty" ? 0 : agents.length,
            next_cursor: "",
            canonical_query: url.searchParams.get("query") ?? "",
            query_schema: querySchemas.agents,
          })
        case "/api/model-evaluations":
          return json(route, {
            evaluations: state === "empty" ? [] : [evaluationSummary()],
            total: state === "empty" ? 0 : 1,
            next_cursor: "",
            canonical_query: url.searchParams.get("query") ?? "",
            query_schema: querySchemas.evaluations,
          })
        case "/api/model-evaluations/options":
          return json(route, evaluationOptions())
        case "/api/repository-reviews/automations":
          return json(route, {
            automations: state === "empty" ? [] : [repositoryReviewAutomation],
            total: state === "empty" ? 0 : 1,
            next_cursor: "",
            canonical_query:
              url.searchParams.get("query") ?? "ORDER BY repository ASC",
            query_schema: querySchemas.reviews,
          })
        case "/api/repository-reviews/profiles":
          return json(route, {
            profiles: state === "empty" ? [] : [repositoryReviewProfile],
            total: state === "empty" ? 0 : 1,
            next_cursor: "",
            canonical_query:
              url.searchParams.get("query") ?? "ALL ORDER BY name ASC",
            query_schema: querySchemas.reviewProfiles,
          })
        case "/api/accounts/models":
          return json(route, modelOptions())
      }

      const reviewRoot = `/api/repository-reviews/automations/${repositoryReviewVisualIDs.automation}`
      if (path === reviewRoot) {
        return json(route, repositoryReviewAutomation)
      }
      if (path === `${reviewRoot}/findings`) {
        const scope =
          url.searchParams.get("scope") === "all" ? "all" : "current"
        return json(route, {
          automation: repositoryReviewAutomation,
          repository: repositoryReviewSummary,
          findings: repositoryReviewFindings,
          repository_findings: repositoryFindings,
          repository_finding_total: repositoryFindings.length,
          contexts: repositoryReviewContexts,
          scope,
          offset: 0,
          total: repositoryReviewFindings.length,
          capabilities: repositoryReviewCapabilities,
        })
      }
      if (path === `${reviewRoot}/issues`) {
        const generationID = url.searchParams.get("generation_id")
        const issues = generationID
          ? repositoryReviewIssues.filter(
              (issue) => issue.generation_id === generationID,
            )
          : repositoryReviewIssues
        return json(route, {
          automation: repositoryReviewAutomation,
          repository: repositoryReviewSummary,
          issues,
          offset: 0,
          total: issues.length,
          ...(generationID ? { generation_id: generationID } : {}),
          capabilities: repositoryReviewCapabilities,
        })
      }
      const reviewFindingPrefix = `${reviewRoot}/findings/`
      if (path.startsWith(reviewFindingPrefix)) {
        const findingID = decodeURIComponent(
          path.slice(reviewFindingPrefix.length),
        )
        const finding = repositoryReviewFindings.find(
          (candidate) => candidate.id === findingID,
        )
        if (!finding) {
          return json(
            route,
            { code: "not_found", message: "Finding not found" },
            404,
          )
        }
        return json(route, {
          automation: repositoryReviewAutomation,
          repository: repositoryReviewSummary,
          finding,
          contexts: repositoryReviewContexts.filter((context) =>
            finding.context_ids.includes(context.id),
          ),
          issue: repositoryReviewIssues.find((issue) =>
            issue.finding_ids.includes(finding.id),
          ),
          capabilities: repositoryReviewCapabilities,
        })
      }
      const reviewIssuePrefix = `${reviewRoot}/issues/`
      if (path.startsWith(reviewIssuePrefix)) {
        const issueID = decodeURIComponent(path.slice(reviewIssuePrefix.length))
        const issue = repositoryReviewIssues.find(
          (candidate) => candidate.id === issueID,
        )
        if (!issue) {
          return json(
            route,
            { code: "not_found", message: "Issue preview not found" },
            404,
          )
        }
        return json(route, {
          automation: repositoryReviewAutomation,
          repository: repositoryReviewSummary,
          issue,
          finding: repositoryReviewFindings.find((finding) =>
            issue.finding_ids.includes(finding.id),
          ),
          capabilities: {
            ...repositoryReviewCapabilities,
            can_publish: issue.state === "editing",
            can_edit: issue.state === "editing",
          },
        })
      }

      const aliasName = decodedTail(path, "/api/model-aliases/")
      if (aliasName) {
        const alias = aliases.find((item) => item.name === aliasName)
        return alias
          ? json(route, { model_alias: alias, config_revision: "cfg-visual-1" })
          : json(route, { code: "not_found", message: "Alias not found" }, 404)
      }
      const routerName = decodedTail(path, "/api/model-routers/")
      if (routerName) {
        const router = routers.find((item) => item.name === routerName)
        return router
          ? json(route, {
              model_router: router,
              config_revision: "cfg-visual-1",
            })
          : json(route, { code: "not_found", message: "Router not found" }, 404)
      }
      const serverName = decodedTail(path, "/api/mcp/servers/")
      if (serverName) {
        const server = mcpServers.find((item) => item.name === serverName)
        return server
          ? json(route, { server, config_revision: "cfg-visual-1" })
          : json(route, { code: "not_found", message: "Server not found" }, 404)
      }
      const agentID = decodedTail(path, "/api/agents/")
      if (agentID && !agentID.includes("/")) {
        const agent = agents.find((item) => item.id === agentID)
        return agent
          ? json(route, {
              agent,
              default_agent_id: "main",
              config_revision: "cfg-visual-1",
              effects: effects(),
            })
          : json(
              route,
              { code: "agent_not_found", message: "Agent not found" },
              404,
            )
      }
      const evaluationID = decodedTail(path, "/api/model-evaluations/")
      if (evaluationID === evaluation.id) {
        return json(route, { evaluation })
      }
      return json(route, {})
    },
  )
}

function field(
  name: string,
  type: "string" | "enum" | "boolean" | "number" | "timestamp",
  suggestedValues: string[] = [],
) {
  const comparisons =
    type === "string" || type === "enum"
      ? ["=", "!=", "~", "!~", "IN", "NOT IN"]
      : ["=", "!=", "<", "<=", ">", ">="]
  return {
    name,
    type,
    operators: comparisons,
    sortable: true,
    ...(suggestedValues.length > 0
      ? { suggested_values: suggestedValues }
      : {}),
  }
}

function schema(fields: ReturnType<typeof field>[]) {
  return { fields }
}

function effects() {
  return {
    launcher_effect: "applied",
    catalog_effect: "applied",
    gateway_effect: "applied",
  }
}

function evaluationSummary() {
  return {
    id: evaluation.id,
    version: evaluation.version,
    status: evaluation.status,
    repository: evaluation.repository,
    ref: evaluation.ref,
    candidate_models: evaluation.candidate_models,
    progress,
    usage,
    warnings: [],
    created_at: evaluation.created_at,
    updated_at: evaluation.updated_at,
  }
}

function evaluationOptions() {
  return {
    models: [
      { alias: "code", resolved_model: "gpt-5.6-codex", available: true },
      { alias: "fast", resolved_model: "gpt-5.6-mini", available: true },
      {
        alias: "review",
        resolved_model: "claude-sonnet-4.6",
        available: true,
      },
    ],
    repositories: [
      {
        id: "octo/picoclaw",
        repository: "octo/picoclaw",
        label: "octo/picoclaw",
      },
    ],
    profiles: [
      { ...evaluation.profile, available_models: ["code", "fast", "review"] },
    ],
    profile_count: 1,
    code_types: ["hotpath-code", "code", "test", "bench-test"],
    max_files_per_language: 20,
    default_files_per_language: 20,
    max_candidate_models: 8,
  }
}

function modelOptions() {
  return {
    models: [
      {
        index: 0,
        model_name: "openai-primary",
        provider: "openai",
        model: "gpt-5.6-codex",
        api_key: "",
        enabled: true,
        available: true,
        status: "available",
        is_default: true,
        is_virtual: false,
      },
    ],
    model_aliases: aliases,
    model_alias_catalog: [
      { name: "code", description: "Implementation and debugging" },
      { name: "fast", description: "Low-latency routine work" },
      { name: "review", description: "Correctness and safety review" },
    ],
    total: 1,
    default_model: "code",
    default_account_ref: "openai-primary",
    revision: "cfg-visual-1",
    provider_options: [],
  }
}

function isCollectionList(path: string) {
  return [
    "/api/model-aliases",
    "/api/model-routers",
    "/api/mcp/servers",
    "/api/agents",
    "/api/model-evaluations",
    "/api/repository-reviews/automations",
    "/api/repository-reviews/profiles",
  ].includes(path)
}

function decodedTail(path: string, prefix: string) {
  if (!path.startsWith(prefix)) return ""
  return decodeURIComponent(path.slice(prefix.length))
}

function delay(milliseconds: number) {
  return new Promise((resolve) => setTimeout(resolve, milliseconds))
}

async function json(route: Route, body: unknown, status = 200) {
  await route.fulfill({
    status,
    contentType: "application/json",
    body: JSON.stringify(body),
  })
}
