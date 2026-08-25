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
            servers: state === "empty" ? [] : mcpServers,
            total: state === "empty" ? 0 : mcpServers.length,
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
        case "/api/accounts/models":
          return json(route, modelOptions())
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
