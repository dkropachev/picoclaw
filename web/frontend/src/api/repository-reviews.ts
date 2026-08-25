import { launcherFetch } from "@/api/http"

export type RepositoryReviewFindingStatus = "open" | "dismissed" | "posted"
export type RepositoryReviewIssueDraftState =
  | "editing"
  | "publishing"
  | "posted"
  | "unknown"

export interface RepositoryReviewFileRef {
  path: string
  blob_sha: string
  size_bytes: number
  category?: string
  mode?: string
}

export interface RepositoryReviewedFile extends RepositoryReviewFileRef {
  commit_sha: string
  profile_hash: string
  run_id: string
  reviewed_at: string
}

export interface RepositoryUnsupportedFile extends RepositoryReviewFileRef {
  commit_sha: string
  profile_hash: string
  reason: string
  updated_at: string
}

export interface RepositoryReviewValidation {
  status: string
  summary: string
  checks?: string[]
}

export interface RepositoryReviewFindingContext {
  id: string
  repository: string
  commit_sha: string
  inventory_hash: string
  profile_hash?: string
  run_id: string
  model: string
  reviewer?: string
  files: RepositoryReviewFileRef[]
  raw_digest?: string
  created_at: string
}

export interface RepositoryReviewFinding {
  id: string
  fingerprint: string
  repository: string
  commit_sha: string
  file: RepositoryReviewFileRef
  line?: number
  severity: string
  title: string
  symbol?: string
  message?: string
  evidence: string
  impact: string
  validation: RepositoryReviewValidation
  context_ids: string[]
  models: string[]
  observation_count: number
  observations?: RepositoryReviewFindingObservation[]
  status: RepositoryReviewFindingStatus
  version: number
  created_at: string
  updated_at: string
}

export interface RepositoryReviewFindingObservation {
  context_id: string
  model: string
  reviewer?: string
  severity: string
  title: string
  symbol?: string
  line?: number
  message?: string
  evidence: string
  impact: string
  validation: RepositoryReviewValidation
}

export interface RepositoryReviewRun {
  id: string
  plan_id: string
  commit_sha: string
  inventory_hash: string
  reviewed_files: number
  unreviewed_files: number
  unsupported_files: number
  remaining_files: number
  unreviewed_paths?: string[]
  unsupported_paths?: string[]
  skipped_files: number
  excluded_files?: number
  accepted_findings: number
  rejected_findings: number
  models: string[]
  completed_at: string
}

export interface RepositoryReviewIssueDraft {
  id: string
  repository: string
  finding_ids: string[]
  title: string
  body: string
  labels?: string[]
  state: RepositoryReviewIssueDraftState
  external_id?: string
  external_url?: string
  version: number
  created_at: string
  updated_at: string
}

export interface RepositoryReviewSummary {
  schema_version: number
  id: string
  repository: string
  version: number
  review_version: number
  last_commit_sha?: string
  finding_count?: number
  open_finding_count?: number
  issue_draft_count?: number
  unsupported_count?: number
  reviewed_file_count?: number
  excluded_file_count?: number
  updated_at: string
}

export interface RepositoryReviewState extends RepositoryReviewSummary {
  files: Record<string, RepositoryReviewedFile>
  unsupported: Record<string, RepositoryUnsupportedFile>
  findings: RepositoryReviewFinding[]
  contexts: RepositoryReviewFindingContext[]
  runs: RepositoryReviewRun[]
  issue_drafts: RepositoryReviewIssueDraft[]
  finding_offset?: number
  finding_total?: number
  next_finding_offset?: number
  draft_offset?: number
  draft_total?: number
  next_draft_offset?: number
}

export interface RepositoryReviewPage {
  repositories: RepositoryReviewSummary[]
}

export interface RepositoryReviewIssueDraftResult {
  repository: RepositoryReviewSummary
  draft: RepositoryReviewIssueDraft
  outcome?: "unknown"
}

export interface RepositoryReviewFindingResult {
  repository: RepositoryReviewSummary
  finding: RepositoryReviewFinding
}

export type RepositoryReviewAutomationStatus =
  | "idle"
  | "running"
  | "stopping"
  | "paused"
  | "completed"
  | "failed"

export type RepositoryReviewPauseReason =
  | "manual"
  | "token_budget"
  | "cost_budget"
  | "account_limit"
  | "guard_expression"
  | "run_failed"
  | "service_restart"

export interface ReviewModelPrice {
  input_price_per_1m: number
  output_price_per_1m: number
}

export interface ReviewModelOption extends ReviewModelPrice {
  alias: string
  resolved_model: string
  provider: string
  available: boolean
  price_known: boolean
  blocked_reason?: string
  subscription?: boolean
  equivalent_model?: string
}

export interface ReviewAccountLimitEntry {
  window?: string
  label?: string
  name?: string
  remaining_percent?: number
  used_percent?: number
  reset_at?: string
  refreshes_at?: string
  refreshed_at?: string
  status?: string
}

export interface ReviewAccountOption {
  id: string
  provider?: string
  label: string
  status: string
  available?: boolean
  default?: boolean
  models?: string[]
  entries: ReviewAccountLimitEntry[]
}

export interface RepositoryReviewAutomationBudget {
  guard_expression: string
}

export interface RepositoryReviewTokenUsage {
  prompt_tokens: number
  completion_tokens: number
  total_tokens: number
  cached_tokens: number
}

export interface RepositoryReviewAutomationProgress {
  stage: string
  completed_batches: number
  total_batches: number
  reviewed_files: number
  remaining_files: number
  unsupported_files: number
  findings: number
}

export type RepositoryReviewCodeType =
  | "hotpath-code"
  | "code"
  | "test"
  | "bench-test"

export interface RepositoryReviewScopePolicy {
  code_types: RepositoryReviewCodeType[]
  include_folders: string[]
  exclude_folders: string[]
  free_text: string
}

export interface RepositoryReviewScopePlanCounts {
  total_files: number
  code_type_files: number
  include_files: number
  excluded_files: number
  selected_files: number
}

export interface RepositoryReviewScopePlan {
  commit_sha: string
  policy_hash: string
  hash: string
  summary: string
  rationale?: string
  warnings: string[]
  counts: RepositoryReviewScopePlanCounts
}

export interface RepositoryReviewModelStats extends RepositoryReviewTokenUsage {
  model: string
  estimated_cost_usd: number
  requests: number
  failures: number
  reviewed_files: number
  findings: number
  latency_ms: number
}

export interface RepositoryReviewAccountSnapshot extends ReviewAccountOption {
  refreshed_at?: string
  error?: string
}

export interface RepositoryReviewAutomationConfig {
  name: string
  repository: string
  ref: string
  target: string
  account_ref: string
  effective_account_ref?: string
  review_focus: string
  scope_policy: RepositoryReviewScopePolicy
  reviewer_models: string[]
  compare_models: boolean
  force: boolean
  max_files_per_run: number
  max_content_bytes: number
  max_parallel_children: number
  auto_continue: boolean
  model_prices: Record<string, ReviewModelPrice>
  budget: RepositoryReviewAutomationBudget
}

export interface RepositoryReviewProfileConfig {
  name: string
  account_ref: string
  review_focus: string
  scope_policy: RepositoryReviewScopePolicy
  reviewer_model: string
  force: boolean
  auto_continue: boolean
  max_files_per_run: number
  max_content_bytes: number
  max_parallel_children: number
  budget: RepositoryReviewAutomationBudget
}

export interface RepositoryReviewProfile extends RepositoryReviewProfileConfig {
  id: string
  version: number
  created_at: string
  updated_at: string
}

export interface RepositoryReviewRepositoryConfigInput {
  repository: string
  branch: string
  profile_id: string
}

export interface RepositoryReviewAutomation extends RepositoryReviewAutomationConfig {
  id: string
  version: number
  profile_id: string
  profile_version: number
  branch: string
  status: RepositoryReviewAutomationStatus
  pause_reason?: RepositoryReviewPauseReason
  pause_detail?: string
  active_run_id?: string
  run_ids: string[]
  usage: RepositoryReviewTokenUsage
  estimated_cost_usd: number
  progress: RepositoryReviewAutomationProgress
  model_stats: RepositoryReviewModelStats[]
  account_limits: RepositoryReviewAccountSnapshot[]
  scope_plan?: RepositoryReviewScopePlan
  started_at?: string
  completed_at?: string
  created_at: string
  updated_at: string
}

export interface RepositoryReviewAutomationOptions {
  models: ReviewModelOption[]
  accounts: ReviewAccountOption[]
  limits_error?: string
}

export class RepositoryReviewAPIError extends Error {
  readonly status: number
  readonly code?: string

  constructor(status: number, message: string, code?: string) {
    super(message)
    this.name = "RepositoryReviewAPIError"
    this.status = status
    this.code = code
  }
}

const apiRoot = "/api/repository-reviews"

export async function listRepositoryReviews(
  signal?: AbortSignal,
): Promise<RepositoryReviewPage> {
  const page = await requestJSON<RepositoryReviewPage>(
    apiRoot,
    undefined,
    signal,
  )
  return {
    repositories: (page.repositories ?? []).map(
      normalizeRepositoryReviewSummary,
    ),
  }
}

export async function listRepositoryReviewAutomations(
  signal?: AbortSignal,
): Promise<{ automations: RepositoryReviewAutomation[] }> {
  const page = await requestJSON<{
    automations?: RepositoryReviewAutomation[]
  }>(`${apiRoot}/automations`, undefined, signal)
  return {
    automations: (page.automations ?? []).map(normalizeAutomation),
  }
}

export async function listRepositoryReviewProfiles(
  signal?: AbortSignal,
): Promise<{ profiles: RepositoryReviewProfile[] }> {
  const page = await requestJSON<{ profiles?: RepositoryReviewProfile[] }>(
    `${apiRoot}/profiles`,
    undefined,
    signal,
  )
  return { profiles: (page.profiles ?? []).map(normalizeProfile) }
}

export async function getRepositoryReviewProfile(
  profileID: string,
  signal?: AbortSignal,
): Promise<RepositoryReviewProfile> {
  return profileFromMutation(
    await requestJSON<RepositoryReviewProfile | ProfileMutationResult>(
      profilePath(profileID),
      undefined,
      signal,
    ),
  )
}

export async function createRepositoryReviewProfile(
  input: RepositoryReviewProfileConfig,
  signal?: AbortSignal,
): Promise<RepositoryReviewProfile> {
  return profileFromMutation(
    await requestJSON<RepositoryReviewProfile | ProfileMutationResult>(
      `${apiRoot}/profiles`,
      jsonMutation("POST", repositoryReviewProfileConfigPayload(input)),
      signal,
    ),
  )
}

export async function updateRepositoryReviewProfile(
  profileID: string,
  input: RepositoryReviewProfileConfig & { expected_version: number },
  signal?: AbortSignal,
): Promise<RepositoryReviewProfile> {
  return profileFromMutation(
    await requestJSON<RepositoryReviewProfile | ProfileMutationResult>(
      profilePath(profileID),
      jsonMutation("PATCH", {
        ...repositoryReviewProfileConfigPayload(input),
        expected_version: input.expected_version,
      }),
      signal,
    ),
  )
}

export async function deleteRepositoryReviewProfile(
  profileID: string,
  input: { expected_version: number },
  signal?: AbortSignal,
): Promise<void> {
  await requestVoid(
    profilePath(profileID),
    jsonMutation("DELETE", input),
    signal,
  )
}

export async function getRepositoryReviewAutomationOptions(
  signal?: AbortSignal,
): Promise<RepositoryReviewAutomationOptions> {
  const options = await requestJSON<RepositoryReviewAutomationOptions>(
    `${apiRoot}/automation-options`,
    undefined,
    signal,
  )
  return {
    models: (options.models ?? []).map((model) => ({
      ...model,
      available: model.available ?? false,
      price_known: model.price_known ?? false,
      input_price_per_1m: model.input_price_per_1m ?? 0,
      output_price_per_1m: model.output_price_per_1m ?? 0,
    })),
    accounts: (options.accounts ?? []).map(normalizeAccount),
    ...(options.limits_error ? { limits_error: options.limits_error } : {}),
  }
}

export async function createRepositoryReviewAutomation(
  input: RepositoryReviewRepositoryConfigInput,
  signal?: AbortSignal,
): Promise<RepositoryReviewAutomation> {
  return automationFromMutation(
    await requestJSON<RepositoryReviewAutomation | AutomationMutationResult>(
      `${apiRoot}/automations`,
      jsonMutation("POST", input),
      signal,
    ),
  )
}

export async function updateRepositoryReviewAutomation(
  automationID: string,
  input: RepositoryReviewRepositoryConfigInput & { expected_version: number },
  signal?: AbortSignal,
): Promise<RepositoryReviewAutomation> {
  return automationFromMutation(
    await requestJSON<RepositoryReviewAutomation | AutomationMutationResult>(
      automationPath(automationID),
      jsonMutation("PATCH", input),
      signal,
    ),
  )
}

export async function deleteRepositoryReviewAutomation(
  automationID: string,
  input: { expected_version: number },
  signal?: AbortSignal,
): Promise<void> {
  await requestVoid(
    automationPath(automationID),
    jsonMutation("DELETE", input),
    signal,
  )
}

export async function startRepositoryReviewAutomation(
  automationID: string,
  input: { expected_version: number },
  signal?: AbortSignal,
): Promise<RepositoryReviewAutomation> {
  return mutateAutomationAction(automationID, "start", input, signal)
}

export async function pauseRepositoryReviewAutomation(
  automationID: string,
  input: { expected_version: number },
  signal?: AbortSignal,
): Promise<RepositoryReviewAutomation> {
  return mutateAutomationAction(automationID, "pause", input, signal)
}

export async function resumeRepositoryReviewAutomation(
  automationID: string,
  input: { expected_version: number },
  signal?: AbortSignal,
): Promise<RepositoryReviewAutomation> {
  return mutateAutomationAction(automationID, "resume", input, signal)
}

export async function restartRepositoryReviewAutomation(
  automationID: string,
  input: { expected_version: number },
  signal?: AbortSignal,
): Promise<RepositoryReviewAutomation> {
  return mutateAutomationAction(automationID, "restart", input, signal)
}

export async function getRepositoryReview(
  repositoryID: string,
  signal?: AbortSignal,
  options?: {
    offset?: number
    limit?: number
    draftOffset?: number
    draftLimit?: number
  },
): Promise<RepositoryReviewState> {
  const params = new URLSearchParams()
  if (options?.offset) params.set("offset", String(options.offset))
  if (options?.limit) params.set("limit", String(options.limit))
  if (options?.draftOffset)
    params.set("draft_offset", String(options.draftOffset))
  if (options?.draftLimit) params.set("draft_limit", String(options.draftLimit))
  const query = params.size > 0 ? `?${params.toString()}` : ""
  return normalizeRepositoryReviewState(
    await requestJSON<RepositoryReviewState>(
      repositoryPath(repositoryID) + query,
      undefined,
      signal,
    ),
  )
}

export async function updateRepositoryReviewFinding(
  repositoryID: string,
  findingID: string,
  input: {
    status: RepositoryReviewFindingStatus
    expected_version: number
  },
  signal?: AbortSignal,
): Promise<RepositoryReviewFindingResult> {
  const result = await requestJSON<RepositoryReviewFindingResult>(
    `${repositoryPath(repositoryID)}/findings/${encodeURIComponent(findingID)}`,
    jsonMutation("PATCH", input),
    signal,
  )
  return {
    repository: normalizeRepositoryReviewSummary(result.repository),
    finding: normalizeFinding(result.finding),
  }
}

export async function createRepositoryReviewIssueDraft(
  repositoryID: string,
  input: {
    finding_ids: string[]
    title?: string
    body?: string
    labels?: string[]
    expected_version: number
  },
  signal?: AbortSignal,
): Promise<RepositoryReviewIssueDraftResult> {
  return normalizeIssueDraftResult(
    await requestJSON<RepositoryReviewIssueDraftResult>(
      `${repositoryPath(repositoryID)}/issue-drafts`,
      jsonMutation("POST", input),
      signal,
    ),
  )
}

export async function updateRepositoryReviewIssueDraft(
  repositoryID: string,
  draftID: string,
  input: {
    title: string
    body: string
    labels: string[]
    expected_version: number
  },
  signal?: AbortSignal,
): Promise<RepositoryReviewIssueDraftResult> {
  return normalizeIssueDraftResult(
    await requestJSON<RepositoryReviewIssueDraftResult>(
      `${repositoryPath(repositoryID)}/issue-drafts/${encodeURIComponent(draftID)}`,
      jsonMutation("PATCH", input),
      signal,
    ),
  )
}

export async function publishRepositoryReviewIssueDraft(
  repositoryID: string,
  draftID: string,
  input: { expected_version: number },
  signal?: AbortSignal,
): Promise<RepositoryReviewIssueDraftResult> {
  return normalizeIssueDraftResult(
    await requestJSON<RepositoryReviewIssueDraftResult>(
      `${repositoryPath(repositoryID)}/issue-drafts/${encodeURIComponent(draftID)}/publish`,
      jsonMutation("POST", input),
      signal,
    ),
  )
}

function normalizeIssueDraftResult(
  value: RepositoryReviewIssueDraftResult,
): RepositoryReviewIssueDraftResult {
  return {
    repository: normalizeRepositoryReviewSummary(value.repository),
    draft: normalizeIssueDraft(value.draft),
    ...(value.outcome ? { outcome: value.outcome } : {}),
  }
}

function normalizeRepositoryReviewState(
  value: RepositoryReviewState,
): RepositoryReviewState {
  return {
    ...normalizeRepositoryReviewSummary(value),
    files: value.files ?? {},
    unsupported: value.unsupported ?? {},
    findings: (value.findings ?? []).map(normalizeFinding),
    contexts: (value.contexts ?? []).map((context) => ({
      ...context,
      files: context.files ?? [],
    })),
    runs: (value.runs ?? []).map((run) => ({
      ...run,
      remaining_files: run.remaining_files ?? run.unreviewed_files ?? 0,
      unsupported_files: run.unsupported_files ?? 0,
      unreviewed_paths: run.unreviewed_paths ?? [],
      unsupported_paths: run.unsupported_paths ?? [],
      models: run.models ?? [],
    })),
    issue_drafts: (value.issue_drafts ?? []).map(normalizeIssueDraft),
  }
}

function normalizeRepositoryReviewSummary(
  value: RepositoryReviewSummary,
): RepositoryReviewSummary {
  return { ...value, review_version: value.review_version ?? 0 }
}

function normalizeFinding(
  finding: RepositoryReviewFinding,
): RepositoryReviewFinding {
  return {
    ...finding,
    context_ids: finding.context_ids ?? [],
    models: finding.models ?? [],
    observations: finding.observations ?? [],
    validation: {
      ...finding.validation,
      checks: finding.validation?.checks ?? [],
    },
  }
}

function normalizeIssueDraft(
  draft: RepositoryReviewIssueDraft,
): RepositoryReviewIssueDraft {
  return {
    ...draft,
    finding_ids: draft.finding_ids ?? [],
    labels: draft.labels ?? [],
  }
}

interface AutomationMutationResult {
  automation: RepositoryReviewAutomation
}

interface ProfileMutationResult {
  profile: RepositoryReviewProfile
}

async function mutateAutomationAction(
  automationID: string,
  action: "start" | "pause" | "resume" | "restart",
  input: { expected_version: number },
  signal?: AbortSignal,
): Promise<RepositoryReviewAutomation> {
  return automationFromMutation(
    await requestJSON<RepositoryReviewAutomation | AutomationMutationResult>(
      `${automationPath(automationID)}/${action}`,
      jsonMutation("POST", input),
      signal,
    ),
  )
}

function automationFromMutation(
  value: RepositoryReviewAutomation | AutomationMutationResult,
): RepositoryReviewAutomation {
  return normalizeAutomation("automation" in value ? value.automation : value)
}

function profileFromMutation(
  value: RepositoryReviewProfile | ProfileMutationResult,
): RepositoryReviewProfile {
  return normalizeProfile("profile" in value ? value.profile : value)
}

function normalizeProfile(
  profile: RepositoryReviewProfile,
): RepositoryReviewProfile {
  return {
    ...profile,
    name: profile.name ?? "Review profile",
    account_ref: profile.account_ref ?? "",
    review_focus: profile.review_focus ?? "",
    reviewer_model: profile.reviewer_model ?? "",
    force: profile.force ?? false,
    auto_continue: profile.auto_continue ?? true,
    max_files_per_run: profile.max_files_per_run ?? 24,
    max_content_bytes: profile.max_content_bytes ?? 524_288,
    max_parallel_children: profile.max_parallel_children ?? 8,
    scope_policy: {
      code_types:
        profile.scope_policy?.code_types?.length > 0
          ? profile.scope_policy.code_types
          : ["hotpath-code", "code"],
      include_folders: profile.scope_policy?.include_folders ?? [],
      exclude_folders: profile.scope_policy?.exclude_folders ?? [],
      free_text: profile.scope_policy?.free_text ?? "",
    },
    budget: normalizeBudget(profile.budget),
  }
}

function normalizeAutomation(
  automation: RepositoryReviewAutomation,
): RepositoryReviewAutomation {
  return {
    ...automation,
    name: automation.name ?? automation.repository ?? "Repository review",
    repository: automation.repository ?? "",
    profile_id: automation.profile_id ?? "",
    profile_version: automation.profile_version ?? 0,
    branch: automation.branch ?? automation.ref ?? "",
    ref: automation.branch ?? automation.ref ?? "",
    target: automation.target ?? "all",
    account_ref: automation.account_ref ?? "",
    effective_account_ref: automation.effective_account_ref ?? "",
    review_focus: automation.review_focus ?? "",
    scope_policy: {
      code_types:
        automation.scope_policy?.code_types?.length > 0
          ? automation.scope_policy.code_types
          : ["hotpath-code", "code"],
      include_folders: automation.scope_policy?.include_folders ?? [],
      exclude_folders: automation.scope_policy?.exclude_folders ?? [],
      free_text: automation.scope_policy?.free_text ?? "",
    },
    reviewer_models: automation.reviewer_models ?? [],
    compare_models: automation.compare_models ?? false,
    force: automation.force ?? false,
    max_files_per_run: automation.max_files_per_run ?? 24,
    max_content_bytes: automation.max_content_bytes ?? 524_288,
    max_parallel_children: automation.max_parallel_children ?? 8,
    auto_continue: automation.auto_continue ?? true,
    run_ids: automation.run_ids ?? [],
    model_prices: automation.model_prices ?? {},
    budget: normalizeBudget(automation.budget),
    usage: normalizeUsage(automation.usage),
    estimated_cost_usd: automation.estimated_cost_usd ?? 0,
    progress: {
      stage: automation.progress?.stage ?? "waiting",
      completed_batches: automation.progress?.completed_batches ?? 0,
      total_batches: automation.progress?.total_batches ?? 0,
      reviewed_files: automation.progress?.reviewed_files ?? 0,
      remaining_files: automation.progress?.remaining_files ?? 0,
      unsupported_files: automation.progress?.unsupported_files ?? 0,
      findings: automation.progress?.findings ?? 0,
    },
    model_stats: normalizeModelStats(automation.model_stats),
    account_limits: normalizeAccountSnapshots(automation.account_limits),
    scope_plan: automation.scope_plan
      ? {
          ...automation.scope_plan,
          warnings: automation.scope_plan.warnings ?? [],
          counts: {
            total_files: automation.scope_plan.counts?.total_files ?? 0,
            code_type_files: automation.scope_plan.counts?.code_type_files ?? 0,
            include_files: automation.scope_plan.counts?.include_files ?? 0,
            excluded_files: automation.scope_plan.counts?.excluded_files ?? 0,
            selected_files: automation.scope_plan.counts?.selected_files ?? 0,
          },
        }
      : undefined,
    started_at: normalizeOptionalTimestamp(automation.started_at),
    completed_at: normalizeOptionalTimestamp(automation.completed_at),
  }
}

function normalizeBudget(
  budget?: Partial<RepositoryReviewAutomationBudget>,
): RepositoryReviewAutomationBudget {
  return {
    guard_expression: budget?.guard_expression ?? "",
  }
}

function normalizeUsage(
  usage?: Partial<RepositoryReviewTokenUsage>,
): RepositoryReviewTokenUsage {
  return {
    prompt_tokens: usage?.prompt_tokens ?? 0,
    completion_tokens: usage?.completion_tokens ?? 0,
    total_tokens: usage?.total_tokens ?? 0,
    cached_tokens: usage?.cached_tokens ?? 0,
  }
}

function normalizeModelStats(value: unknown): RepositoryReviewModelStats[] {
  const rows: Array<[string, Record<string, unknown>]> = Array.isArray(value)
    ? value.flatMap((candidate) => {
        const record = isRecord(candidate) ? candidate : undefined
        return record && typeof record.model === "string"
          ? [[record.model, record]]
          : []
      })
    : isRecord(value)
      ? Object.entries(value).flatMap(([model, candidate]) =>
          isRecord(candidate) ? [[model, candidate]] : [],
        )
      : []
  return rows.map(([model, stats]) => {
    const nestedTokens = isRecord(stats.tokens) ? stats.tokens : undefined
    const usage = normalizeUsage(
      (nestedTokens ?? stats) as Partial<RepositoryReviewTokenUsage>,
    )
    return {
      ...usage,
      model,
      estimated_cost_usd: numberValue(stats.estimated_cost_usd),
      requests: numberValue(stats.requests),
      failures: numberValue(stats.failures),
      reviewed_files: numberValue(stats.reviewed_files),
      findings: numberValue(stats.findings),
      latency_ms: numberValue(stats.latency_ms ?? stats.latency_millis),
    }
  })
}

function normalizeAccount<T extends ReviewAccountOption>(account: T): T {
  return {
    ...account,
    available: account.available ?? false,
    models: Array.isArray(account.models) ? account.models : [],
    entries: (account.entries ?? []).map((entry) => ({
      ...entry,
      label: entry.label ?? entry.name,
      reset_at: normalizeOptionalTimestamp(
        entry.reset_at ?? entry.refreshes_at,
      ),
      refreshed_at: normalizeOptionalTimestamp(entry.refreshed_at),
    })),
  }
}

function normalizeAccountSnapshots(
  value: unknown,
): RepositoryReviewAccountSnapshot[] {
  if (!Array.isArray(value)) return []
  const grouped = new Map<string, RepositoryReviewAccountSnapshot>()
  for (const candidate of value) {
    if (!isRecord(candidate)) continue
    if (typeof candidate.account_id === "string") {
      const id = candidate.account_id
      const existing = grouped.get(id) ?? {
        id,
        provider: "",
        label: id,
        status: "available",
        entries: [],
      }
      const remaining =
        typeof candidate.remaining_percent === "number"
          ? candidate.remaining_percent
          : undefined
      existing.entries.push({
        window:
          typeof candidate.window === "string" ? candidate.window : "default",
        label:
          typeof candidate.name === "string" && candidate.name
            ? candidate.name
            : undefined,
        remaining_percent: remaining,
        status:
          typeof candidate.detail === "string" && candidate.detail
            ? candidate.detail
            : remaining === undefined
              ? "unknown"
              : "available",
        reset_at:
          typeof candidate.resets_at === "string"
            ? normalizeOptionalTimestamp(candidate.resets_at)
            : undefined,
        refreshed_at:
          typeof candidate.checked_at === "string"
            ? normalizeOptionalTimestamp(candidate.checked_at)
            : undefined,
      })
      existing.refreshed_at =
        typeof candidate.checked_at === "string"
          ? normalizeOptionalTimestamp(candidate.checked_at)
          : existing.refreshed_at
      if (remaining === undefined) existing.status = "unknown"
      grouped.set(id, existing)
      continue
    }
    if (typeof candidate.id !== "string") continue
    grouped.set(
      candidate.id,
      normalizeAccount(candidate as unknown as RepositoryReviewAccountSnapshot),
    )
  }
  return [...grouped.values()]
}

function numberValue(value: unknown): number {
  return typeof value === "number" && Number.isFinite(value) ? value : 0
}

function normalizeOptionalTimestamp(
  value: string | undefined,
): string | undefined {
  if (!value) return undefined
  const parsed = new Date(value)
  return Number.isNaN(parsed.getTime()) || parsed.getUTCFullYear() <= 1
    ? undefined
    : value
}

function repositoryPath(repositoryID: string): string {
  return `${apiRoot}/${encodeURIComponent(repositoryID)}`
}

function automationPath(automationID: string): string {
  return `${apiRoot}/automations/${encodeURIComponent(automationID)}`
}

function profilePath(profileID: string): string {
  return `${apiRoot}/profiles/${encodeURIComponent(profileID)}`
}

function repositoryReviewProfileConfigPayload(
  input: RepositoryReviewProfileConfig,
): RepositoryReviewProfileConfig {
  return {
    name: input.name,
    account_ref: input.account_ref,
    review_focus: input.review_focus,
    scope_policy: input.scope_policy,
    reviewer_model: input.reviewer_model,
    force: input.force,
    auto_continue: input.auto_continue,
    max_files_per_run: input.max_files_per_run,
    max_content_bytes: input.max_content_bytes,
    max_parallel_children: input.max_parallel_children,
    budget: input.budget,
  }
}

function jsonMutation(
  method: "POST" | "PATCH" | "DELETE",
  body: unknown,
): RequestInit {
  return {
    method,
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  }
}

async function requestVoid(
  path: string,
  init?: RequestInit,
  signal?: AbortSignal,
): Promise<void> {
  const response = await launcherFetch(path, { ...init, signal })
  if (response.ok) return
  const contentType = response.headers.get("Content-Type")
  const isJSON =
    contentType?.split(";", 1)[0]?.trim().toLowerCase() === "application/json"
  let payload: unknown
  if (isJSON) {
    try {
      payload = await response.json()
    } catch {
      payload = undefined
    }
  }
  const record = isRecord(payload) ? payload : undefined
  throw new RepositoryReviewAPIError(
    response.status,
    typeof record?.message === "string"
      ? record.message
      : "Repository review request failed.",
    typeof record?.code === "string" ? record.code : undefined,
  )
}

async function requestJSON<T>(
  path: string,
  init?: RequestInit,
  signal?: AbortSignal,
): Promise<T> {
  const response = await launcherFetch(path, { ...init, signal })
  const contentType = response.headers.get("Content-Type")
  const isJSON =
    contentType?.split(";", 1)[0]?.trim().toLowerCase() === "application/json"
  let payload: unknown
  if (isJSON) {
    try {
      payload = await response.json()
    } catch {
      throw new RepositoryReviewAPIError(
        response.ok ? 502 : response.status,
        "Repository review returned malformed JSON.",
        "malformed_response",
      )
    }
  }
  if (!response.ok) {
    const record = isRecord(payload) ? payload : undefined
    throw new RepositoryReviewAPIError(
      response.status,
      typeof record?.message === "string"
        ? record.message
        : "Repository review request failed.",
      typeof record?.code === "string" ? record.code : undefined,
    )
  }
  if (!isJSON || !isRecord(payload)) {
    throw new RepositoryReviewAPIError(
      502,
      "Repository review returned a malformed response.",
      "malformed_response",
    )
  }
  return payload as T
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value)
}
