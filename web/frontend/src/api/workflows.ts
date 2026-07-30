import { launcherFetch } from "@/api/http"

export interface WorkflowDefinition {
  ref: string
  name?: string
  path?: string
  error?: string
  workflow_call?: WorkflowCallDefinition
  event_trigger?: WorkflowEventTrigger | null
}

export interface WorkflowCallDefinition {
  inputs?: Record<string, WorkflowInputDefinition>
  secrets?: Record<string, WorkflowSecretDefinition>
  outputs?: Record<string, WorkflowOutputDefinition>
}

export interface WorkflowInputDefinition {
  type?: string
  required?: boolean
  default?: unknown
}

export interface WorkflowSecretDefinition {
  required?: boolean
}

export interface WorkflowOutputDefinition {
  value?: string
}

export interface WorkflowValidationIssue {
  path?: string
  message: string
}

export interface WorkflowValidationStamp {
  workflow_ref: string
  workflow_hash?: string
  validated_against_picoclaw_version: string
  validated_against_git_commit?: string
  workflow_engine_version: string
  workflow_schema_version: string
  validator_fingerprint: string
  status: string
  errors?: WorkflowValidationIssue[]
  warnings?: WorkflowValidationIssue[]
  validated_at: string
}

export interface WorkflowRuntimeCompatibility {
  picoclaw_version: string
  git_commit?: string
  workflow_engine_version: string
  workflow_schema_version: string
  validator_fingerprint: string
}

export interface WorkflowCompatibilitySummary {
  current: WorkflowRuntimeCompatibility
  manifest_runtime?: WorkflowRuntimeCompatibility
  workflows: WorkflowValidationStamp[]
  counts: Record<string, number>
  version_changed: boolean
  manifest_missing: boolean
  has_blocking: boolean
}

export interface WorkflowDevelopmentValidation {
  valid: boolean
  errors?: WorkflowValidationIssue[]
  warnings?: WorkflowValidationIssue[]
  validated_at: string
}

export interface WorkflowDevelopmentTestSnapshot {
  draft_key: string
  draft_revision?: string
  target_workflow_ref: string
  run_id?: string
  event_id?: string
  status: string
  error?: string
  tested_at: string
}

export interface WorkflowDevelopmentSession {
  id: string
  session_revision: string
  draft_revision: string
  base_target_revision: string
  reason: "new" | "edit" | "version_revalidation" | string
  status: string
  prompt?: string
  source_workflow_ref?: string
  target_workflow_ref: string
  target_picoclaw_version?: string
  target_git_commit?: string
  yaml: string
  validation?: WorkflowDevelopmentValidation
  last_test?: WorkflowDevelopmentTestSnapshot
  created_at: string
  updated_at: string
}

export interface WorkflowDevelopmentTestReconciliation {
  state: "degraded"
  reason:
    | "draft_test_snapshot_not_recorded"
    | "draft_test_run_unavailable"
    | "draft_test_terminal_snapshot_not_recorded"
  run_id: string
  message: string
}

export interface WorkflowDevelopmentResult {
  session: WorkflowDevelopmentSession | null
  reconciliation?: WorkflowDevelopmentTestReconciliation
}

export interface WorkflowEventEntityTrigger {
  ids?: string[]
  types?: string[]
  attributes?: Record<string, string[]>
}

export interface WorkflowEventTrigger {
  sources?: string[]
  connectors?: string[]
  types?: string[]
  actor?: WorkflowEventEntityTrigger
  subject?: WorkflowEventEntityTrigger
  attributes?: Record<string, string[]>
}

export interface WorkflowEventTriggerInspection {
  revision: string
  editable: boolean
  reason?: string
  event_trigger?: WorkflowEventTrigger | null
  validation?: WorkflowDevelopmentValidation
}

export interface WorkflowEventTriggerRenderResult extends WorkflowEventTriggerInspection {
  yaml: string
}

export const workflowTriggerKinds = [
  "manual",
  "schedule",
  "channel_message",
  "command",
  "runtime_event",
  "event",
  "workflow_call",
] as const

export type WorkflowTriggerKind = (typeof workflowTriggerKinds)[number]

export type WorkflowManualTrigger = Record<string, never>

export interface WorkflowScheduleTrigger {
  cron?: string
}

export interface WorkflowConversationSpec {
  session?: "discussion" | "sender" | "global" | string
  delivery?: "same_discussion" | "none" | string
}

export interface WorkflowChannelMessageTrigger {
  channels?: string[]
  chats?: string[]
  senders?: string[]
  mentioned?: boolean
  command?: string
  text_matches?: string
  passthrough?: boolean
  conversation?: WorkflowConversationSpec
}

export interface WorkflowCommandTrigger {
  name?: string
  channels?: string[]
  chats?: string[]
  senders?: string[]
  args?: Record<string, WorkflowInputDefinition>
  passthrough?: boolean
  conversation?: WorkflowConversationSpec
}

export interface WorkflowRuntimeEventTrigger {
  kinds?: string[]
  sources?: string[]
  agents?: string[]
  sessions?: string[]
  channels?: string[]
  chats?: string[]
}

export type WorkflowCallTrigger = WorkflowCallDefinition

export interface WorkflowTriggerValueMap {
  manual: WorkflowManualTrigger
  schedule: WorkflowScheduleTrigger[]
  channel_message: WorkflowChannelMessageTrigger
  command: WorkflowCommandTrigger
  runtime_event: WorkflowRuntimeEventTrigger
  event: WorkflowEventTrigger
  workflow_call: WorkflowCallTrigger
}

export type WorkflowTriggerProjectionMap = {
  [Kind in WorkflowTriggerKind]: {
    present: boolean
    editable: boolean
    reason?: string
    value: WorkflowTriggerValueMap[Kind] | null
  }
}

export interface WorkflowTriggersInspection {
  revision: string
  validation?: WorkflowDevelopmentValidation
  triggers: WorkflowTriggerProjectionMap
}

export interface WorkflowTriggerRenderResult extends WorkflowTriggersInspection {
  yaml: string
}

export interface WorkflowEventTriggerMatchCheck {
  path: string
  present: boolean
  value?: unknown
  matched: boolean
}

export interface WorkflowEventTriggerMatchResult {
  event_id: string
  matched: boolean
  checks: WorkflowEventTriggerMatchCheck[]
  validation?: WorkflowDevelopmentValidation
}

export interface WorkflowRun {
  id: string
  workflow_ref: string
  status: string
  origin?: WorkflowRunOrigin
  parent_run_id?: string
  child_run_ids?: string[]
  caller_job_id?: string
  retry_of_run_id?: string
  session?: string
  delivery?: Record<string, unknown>
  event?: Record<string, unknown>
  inputs?: Record<string, unknown>
  outputs?: Record<string, unknown>
  jobs?: Record<string, WorkflowJobExecution>
  steps?: Record<string, WorkflowStepExecution>
  error?: string
  cancel_reason?: string
  created_at: string
  updated_at: string
  completed_at?: string
  cancel_requested_at?: string
}

export interface WorkflowRunOrigin {
  kind: "external_event" | "external_event_draft_test"
  event_id: string
  dispatch_id?: string
  root_run_id: string
}

export interface WorkflowJobExecution {
  id: string
  status: string
  outputs?: Record<string, unknown>
  error?: string
}

export interface WorkflowStepExecution {
  id: string
  status: string
  outputs?: Record<string, unknown>
  error?: string
}

export interface WorkflowRunEvent {
  time: string
  kind: string
  run_id: string
  job_id?: string
  step_id?: string
  message?: string
  payload?: Record<string, unknown>
}

export interface WorkflowRunGraph {
  run_id: string
  nodes: Array<{
    id: string
    workflow_ref: string
    status: string
    parent_run_id?: string
    caller_job_id?: string
    retry_of_run_id?: string
  }>
  edges: Array<{
    from: string
    to: string
    job_id?: string
    kind: string
  }>
}

export interface WorkflowReloadResult {
  reloaded_at: string
  workflows: WorkflowDefinition[]
  errors: Array<{ ref: string; error: string }>
}

export interface WorkflowRunResult {
  run_id: string
  status: string
  outputs?: Record<string, unknown>
  error?: string
}

export interface WorkflowDevelopmentTestResult {
  session: WorkflowDevelopmentSession
  result?: WorkflowRunResult
  reconciliation?: WorkflowDevelopmentTestReconciliation
  error?: string
}

export interface WorkflowRunLaunchResult {
  result: WorkflowRunResult
  error?: string
}

export type WorkflowTemplateState =
  | "available"
  | "installed"
  | "modified"
  | "blocked"

export interface WorkflowTemplateCatalogEntry {
  name: string
  ref: string
  state: WorkflowTemplateState
  blocked_reason?:
    | "configuration_invalid"
    | "target_not_regular"
    | "target_unavailable"
    | string
}

export interface WorkflowTemplateInstallResult {
  name: string
  ref: string
  state: WorkflowTemplateState
  installed: boolean
  overwritten?: boolean
  revalidated: boolean
}

export interface WorkflowTemplateCatalog {
  templates: WorkflowTemplateCatalogEntry[]
}

export interface WorkflowSettingsValues {
  enabled: boolean
  tool_enabled: boolean
  definitions_dir: string
  max_concurrent_runs: number
  default_timeout_seconds: number
  max_call_depth: number
  retention_days: number
}

export interface WorkflowSettingsEffects {
  launcher_effect: string
  catalog_effect: string
  gateway_effect: string
}

export interface WorkflowSettingsResponse {
  configured: WorkflowSettingsValues
  effective: WorkflowSettingsValues
  config_revision: string
  effects: WorkflowSettingsEffects
}

export interface WorkflowSettingsPatch extends Partial<WorkflowSettingsValues> {
  expected_config_revision: string
}

export type WorkflowDependencyKind =
  | "agent"
  | "tool"
  | "mcp"
  | "function"
  | "reusable"

export interface WorkflowDependencyOccurrence {
  kind: WorkflowDependencyKind
  name: string
  workflow_ref: string
  path: string
}

export type WorkflowDependencyIssueCode =
  | "invalid_reusable_ref"
  | "reusable_unavailable"
  | "reusable_invalid"
  | "reusable_cycle"
  | "call_depth_exceeded"
  | "missing_required_input"
  | "input_type_mismatch"
  | "invalid_secrets"
  | "missing_required_secret"
  | "analysis_limit_exceeded"

export interface WorkflowDependencyIssue {
  code: WorkflowDependencyIssueCode
  workflow_ref: string
  path: string
  dependency_kind?: WorkflowDependencyKind
  dependency_name?: string
}

export type WorkflowDependencyReadinessCode =
  | "ready"
  | "unchecked"
  | "not_configured"
  | "disabled"
  | "not_allowed"
  | "not_connected"
  | "not_found"
  | "invalid_configuration"
  | "name_collision"
  | "unavailable"

export interface WorkflowDependencyReadiness {
  dependency: WorkflowDependencyOccurrence
  code: WorkflowDependencyReadinessCode
  ready: boolean
}

export interface WorkflowDependencyCheckResponse {
  root_ref: string
  revision: string
  ready: boolean
  workflow_enabled: boolean
  structural_ready: boolean
  runtime_ready: boolean
  dependencies: WorkflowDependencyReadiness[]
  structural_issues: WorkflowDependencyIssue[]
}

export type WorkflowDependencyCheckRequest =
  | {
      draft: {
        target_ref: string
        yaml: string
      }
      ref?: never
    }
  | {
      ref: string
      draft?: never
    }

export interface WorkflowDevelopmentPublishRequest {
  session_id: string
  expected_session_revision: string
  expected_draft_revision: string
  expected_base_target_revision: string
  expected_dependency_revision: string
}

export type WorkflowDeliveryPayload = Record<string, unknown>

export class WorkflowAPIError extends Error {
  readonly status: number
  readonly candidateValidation?: WorkflowDevelopmentValidation

  constructor(
    message: string,
    status: number,
    candidateValidation?: WorkflowDevelopmentValidation,
  ) {
    super(message)
    this.name = "WorkflowAPIError"
    this.status = status
    this.candidateValidation = candidateValidation
  }
}

export const WORKFLOW_CANCEL_REASON_MAX_BYTES = 1024

const workflowRunIDPattern = /^wr_[A-Za-z0-9_-]+$/
const maximumWorkflowRunIDBytes = 1024

function validWorkflowRunID(value: string): boolean {
  return (
    new TextEncoder().encode(value).byteLength <= maximumWorkflowRunIDBytes &&
    workflowRunIDPattern.test(value)
  )
}

async function request<T>(path: string, options?: RequestInit): Promise<T> {
  const res = await launcherFetch(path, options)
  if (!res.ok) {
    const text = await res.text()
    const details = apiErrorDetails(text, res.status, res.statusText)
    throw new WorkflowAPIError(
      details.message,
      res.status,
      details.candidateValidation,
    )
  }
  return res.json() as Promise<T>
}

function apiErrorMessage(text: string, status: number, statusText: string) {
  return apiErrorDetails(text, status, statusText).message
}

function apiErrorDetails(text: string, status: number, statusText: string) {
  let message = text.trim()
  let candidateValidation: WorkflowDevelopmentValidation | undefined
  try {
    const body = JSON.parse(text) as {
      error?: string
      errors?: string[]
      candidate_validation?: unknown
    }
    if (typeof body.error === "string" && body.error.trim() !== "") {
      message = body.error
    } else if (Array.isArray(body.errors) && body.errors.length > 0) {
      message = body.errors.join("; ")
    }
    candidateValidation = workflowCandidateValidation(body.candidate_validation)
  } catch {
    // Keep the plain-text response when the backend did not return JSON.
  }
  return {
    message: message || `API error: ${status} ${statusText}`,
    candidateValidation,
  }
}

function workflowCandidateValidation(
  value: unknown,
): WorkflowDevelopmentValidation | undefined {
  if (value == null || typeof value !== "object" || Array.isArray(value)) {
    return undefined
  }
  const candidate = value as Record<string, unknown>
  if (candidate.valid !== false || typeof candidate.validated_at !== "string") {
    return undefined
  }
  const errors = workflowCandidateValidationIssues(candidate.errors)
  const warnings = workflowCandidateValidationIssues(candidate.warnings)
  if (errors == null || warnings == null) {
    return undefined
  }
  return {
    valid: false,
    validated_at: candidate.validated_at.slice(0, 128),
    ...(errors.length > 0 ? { errors } : {}),
    ...(warnings.length > 0 ? { warnings } : {}),
  }
}

function workflowCandidateValidationIssues(
  value: unknown,
): WorkflowValidationIssue[] | null {
  if (value == null) {
    return []
  }
  if (!Array.isArray(value) || value.length > 128) {
    return null
  }
  const issues: WorkflowValidationIssue[] = []
  for (const item of value) {
    if (item == null || typeof item !== "object" || Array.isArray(item)) {
      return null
    }
    const issue = item as Record<string, unknown>
    if (
      typeof issue.message !== "string" ||
      issue.message === "" ||
      issue.message.length > 4096 ||
      (issue.path != null &&
        (typeof issue.path !== "string" || issue.path.length > 1024))
    ) {
      return null
    }
    issues.push({
      message: issue.message,
      ...(typeof issue.path === "string" ? { path: issue.path } : {}),
    })
  }
  return issues
}

export async function listWorkflows(): Promise<{
  workflows: WorkflowDefinition[]
  compatibility?: WorkflowCompatibilitySummary
}> {
  const payload = await request<{
    workflows?: WorkflowDefinition[] | null
    compatibility?: WorkflowCompatibilitySummary | null
  }>("/api/workflows")
  return {
    workflows: arrayOrEmpty(payload.workflows),
    compatibility:
      payload.compatibility == null
        ? undefined
        : normalizeWorkflowCompatibilitySummary(payload.compatibility),
  }
}

export async function getWorkflowCompatibility(): Promise<WorkflowCompatibilitySummary> {
  return normalizeWorkflowCompatibilitySummary(
    await request<WorkflowCompatibilitySummary>("/api/workflows/compatibility"),
  )
}

export async function revalidateWorkflows(): Promise<WorkflowCompatibilitySummary> {
  return normalizeWorkflowCompatibilitySummary(
    await request<WorkflowCompatibilitySummary>("/api/workflows/revalidate", {
      method: "POST",
    }),
  )
}

export async function listWorkflowTemplates(): Promise<WorkflowTemplateCatalog> {
  const payload = await requestWorkflowControl<{
    templates?: WorkflowTemplateCatalogEntry[] | null
  }>(
    "/api/workflows/templates",
    undefined,
    "Built-in workflow templates are unavailable.",
  )
  return { templates: arrayOrEmpty(payload.templates) }
}

export async function installWorkflowTemplate(
  name: string,
  overwrite: boolean,
): Promise<{
  result: WorkflowTemplateInstallResult
  templates: WorkflowTemplateCatalogEntry[]
}> {
  const payload = await requestWorkflowControl<{
    result: WorkflowTemplateInstallResult
    templates?: WorkflowTemplateCatalogEntry[] | null
  }>(
    `/api/workflows/templates/${encodeURIComponent(name)}/install`,
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ overwrite }),
    },
    "The workflow template could not be installed.",
  )
  return { ...payload, templates: arrayOrEmpty(payload.templates) }
}

export async function getWorkflowSettings(): Promise<WorkflowSettingsResponse> {
  return requestWorkflowControl(
    "/api/workflows/settings",
    undefined,
    "Workflow settings are unavailable.",
  )
}

export async function patchWorkflowSettings(
  payload: WorkflowSettingsPatch,
): Promise<WorkflowSettingsResponse> {
  return requestWorkflowControl(
    "/api/workflows/settings",
    {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload),
    },
    "Workflow settings could not be saved.",
  )
}

export async function checkWorkflowDependencies(
  payload: WorkflowDependencyCheckRequest,
  signal?: AbortSignal,
): Promise<WorkflowDependencyCheckResponse> {
  const result = await requestWorkflowControl<WorkflowDependencyCheckResponse>(
    "/api/workflows/dependencies/check",
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload),
      signal,
    },
    "Workflow dependency readiness is unavailable.",
  )
  return {
    ...result,
    dependencies: arrayOrEmpty(result.dependencies),
    structural_issues: arrayOrEmpty(result.structural_issues),
  }
}

export async function getWorkflowDevelopment(): Promise<WorkflowDevelopmentResult> {
  return request("/api/workflows/development")
}

export async function startWorkflowDevelopment(payload: {
  reason?: "new" | "edit" | "version_revalidation" | string
  prompt?: string
  ref?: string
  target_ref?: string
}): Promise<{ session: WorkflowDevelopmentSession; conflict?: boolean }> {
  const res = await launcherFetch("/api/workflows/development/start", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload),
  })
  const text = await res.text()
  if (res.ok) {
    return JSON.parse(text) as {
      session: WorkflowDevelopmentSession
    }
  }
  if (res.status === 409) {
    try {
      const body = JSON.parse(text) as {
        session?: WorkflowDevelopmentSession
      }
      if (body.session != null) {
        return { session: body.session, conflict: true }
      }
    } catch {
      // Fall through to the normal error message path.
    }
  }
  throw new Error(apiErrorMessage(text, res.status, res.statusText))
}

export async function reviseWorkflowDevelopment(payload: {
  prompt?: string
  target_ref?: string
  yaml?: string
  regenerate?: boolean
}): Promise<{ session: WorkflowDevelopmentSession }> {
  return request("/api/workflows/development/revise", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload),
  })
}

export async function inspectWorkflowEventTrigger(
  yaml: string,
  signal?: AbortSignal,
): Promise<WorkflowEventTriggerInspection> {
  return request("/api/workflows/development/event-trigger/inspect", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ yaml }),
    signal,
  })
}

export async function renderWorkflowEventTrigger(payload: {
  yaml: string
  revision: string
  event_trigger: WorkflowEventTrigger | null
}): Promise<WorkflowEventTriggerRenderResult> {
  return request("/api/workflows/development/event-trigger/render", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload),
  })
}

export async function inspectWorkflowTriggers(
  yaml: string,
  signal?: AbortSignal,
): Promise<WorkflowTriggersInspection> {
  return request("/api/workflows/development/triggers/inspect", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ yaml }),
    signal,
  })
}

export async function renderWorkflowTrigger<Kind extends WorkflowTriggerKind>(
  payload: {
    yaml: string
    revision: string
    trigger_type: Kind
    trigger: WorkflowTriggerValueMap[Kind] | null
  },
  signal?: AbortSignal,
): Promise<WorkflowTriggerRenderResult> {
  return request("/api/workflows/development/triggers/render", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload),
    signal,
  })
}

export async function matchWorkflowEventTrigger(
  payload: {
    yaml: string
    event_id: string
  },
  signal?: AbortSignal,
): Promise<WorkflowEventTriggerMatchResult> {
  return request("/api/workflows/development/event-trigger/match", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload),
    signal,
  })
}

export async function aiReviseWorkflowDevelopment(payload: {
  prompt?: string
  target_ref?: string
  yaml?: string
}): Promise<{ session: WorkflowDevelopmentSession }> {
  return request("/api/workflows/development/ai-revise", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload),
  })
}

export async function validateWorkflowDevelopment(): Promise<{
  session: WorkflowDevelopmentSession
}> {
  return request("/api/workflows/development/validate", { method: "POST" })
}

export async function testWorkflowDevelopment(payload: {
  prompt?: string
  target_ref?: string
  yaml?: string
  inputs?: Record<string, unknown>
  secrets?: Record<string, string>
  session?: string
  delivery?: WorkflowDeliveryPayload
  event_id?: string
  async?: boolean
}): Promise<WorkflowDevelopmentTestResult> {
  const res = await launcherFetch("/api/workflows/development/test", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload),
  })
  const text = await res.text()
  if (res.ok) {
    return JSON.parse(text) as WorkflowDevelopmentTestResult
  }
  try {
    const body = JSON.parse(text) as Partial<WorkflowDevelopmentTestResult>
    if (body.session != null) {
      return {
        session: body.session,
        result: body.result,
        reconciliation: body.reconciliation,
        error:
          typeof body.error === "string" && body.error.trim() !== ""
            ? body.error
            : apiErrorMessage(text, res.status, res.statusText),
      }
    }
  } catch {
    // Fall through to the normal error message path.
  }
  throw new Error(apiErrorMessage(text, res.status, res.statusText))
}

export async function publishWorkflowDevelopment(
  payload: WorkflowDevelopmentPublishRequest,
): Promise<{
  workflow_ref: string
  session: WorkflowDevelopmentSession
}> {
  return requestWorkflowControl(
    "/api/workflows/development/publish",
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload),
    },
    "The workflow could not be published.",
  )
}

export async function discardWorkflowDevelopment(): Promise<{
  session: WorkflowDevelopmentSession
}> {
  return request("/api/workflows/development/discard", { method: "POST" })
}

export async function reloadWorkflows(): Promise<WorkflowReloadResult> {
  return normalizeWorkflowReloadResult(
    await request<WorkflowReloadResult>("/api/workflows/reload", {
      method: "POST",
    }),
  )
}

export async function runWorkflow(payload: {
  ref: string
  expected_dependency_revision: string
  inputs?: Record<string, unknown>
  secrets?: Record<string, string>
  session?: string
  delivery?: WorkflowDeliveryPayload
  async?: boolean
}): Promise<WorkflowRunLaunchResult> {
  const res = await launcherFetch("/api/workflows/run", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload),
  })
  return workflowRunLaunchResultFromResponse(res)
}

async function workflowRunLaunchResultFromResponse(
  res: Response,
): Promise<WorkflowRunLaunchResult> {
  const text = await res.text()
  if (res.ok) {
    return { result: JSON.parse(text) as WorkflowRunResult }
  }
  try {
    const body = JSON.parse(text) as {
      result?: WorkflowRunResult
      error?: string
    }
    if (body.result != null) {
      return {
        result: body.result,
        error:
          typeof body.error === "string" && body.error.trim() !== ""
            ? body.error
            : apiErrorMessage(text, res.status, res.statusText),
      }
    }
  } catch {
    // Fall through to the normal error message path.
  }
  throw new WorkflowAPIError(
    workflowLaunchErrorMessage(text, res.status, res.statusText),
    res.status,
  )
}

export async function listWorkflowRuns(): Promise<{ runs: WorkflowRun[] }> {
  const payload = await request<{ runs?: WorkflowRun[] | null }>(
    "/api/workflows/runs",
  )
  return { runs: arrayOrEmpty(payload.runs).map(normalizeWorkflowRun) }
}

export async function getWorkflowRun(runID: string): Promise<WorkflowRun> {
  if (!validWorkflowRunID(runID)) {
    throw new WorkflowAPIError("Invalid workflow run identifier.", 400)
  }
  const run = normalizeWorkflowRun(
    await request<WorkflowRun>(
      `/api/workflows/runs/${encodeURIComponent(runID)}`,
    ),
  )
  if (run.id !== runID) {
    throw new WorkflowAPIError(
      "The workflow service returned a mismatched run.",
      502,
    )
  }
  return run
}

export async function getWorkflowRunEvents(
  runID: string,
): Promise<{ run_id: string; events: WorkflowRunEvent[] }> {
  const payload = await request<{
    run_id: string
    events?: WorkflowRunEvent[] | null
  }>(`/api/workflows/runs/${encodeURIComponent(runID)}/events`)
  return { ...payload, events: arrayOrEmpty(payload.events) }
}

export function workflowRunEventsStreamURL(runID: string): string {
  return `/api/workflows/runs/${encodeURIComponent(runID)}/events/stream`
}

export async function getWorkflowRunGraph(
  runID: string,
): Promise<WorkflowRunGraph> {
  return normalizeWorkflowRunGraph(
    await request<WorkflowRunGraph>(
      `/api/workflows/runs/${encodeURIComponent(runID)}/graph`,
    ),
  )
}

export async function cancelWorkflowRun(
  runID: string,
  reason: string,
): Promise<WorkflowRun> {
  if (!validWorkflowRunID(runID)) {
    throw new WorkflowAPIError("Invalid workflow run identifier.", 400)
  }
  const normalizedReason = reason.trim()
  if (
    normalizedReason === "" ||
    new TextEncoder().encode(normalizedReason).byteLength >
      WORKFLOW_CANCEL_REASON_MAX_BYTES
  ) {
    throw new WorkflowAPIError(
      "Cancel reason must be between 1 and 1024 UTF-8 bytes.",
      400,
    )
  }
  const run = normalizeWorkflowRun(
    await request<WorkflowRun>(
      `/api/workflows/runs/${encodeURIComponent(runID)}/cancel`,
      {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ reason: normalizedReason }),
      },
    ),
  )
  if (run.id !== runID) {
    throw new WorkflowAPIError(
      "The workflow service returned a mismatched run.",
      502,
    )
  }
  return run
}

export async function retryWorkflowRun(
  runID: string,
  payload: {
    expected_dependency_revision: string
    secrets?: Record<string, string>
  },
): Promise<WorkflowRunLaunchResult> {
  if (!validWorkflowRunID(runID)) {
    throw new WorkflowAPIError("Invalid workflow run identifier.", 400)
  }
  const res = await launcherFetch(
    `/api/workflows/runs/${encodeURIComponent(runID)}/retry`,
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload),
    },
  )
  return workflowRunLaunchResultFromResponse(res)
}

function workflowLaunchErrorMessage(
  text: string,
  status: number,
  statusText: string,
) {
  const code = workflowErrorCode(text)
  switch (code) {
    case "dependency_revision_mismatch":
      return "Workflow dependencies changed. Wait for a fresh readiness check and try again."
    case "workflow_dependencies_not_ready":
      return "Resolve the workflow dependency blockers and try again."
    case "dependency_check_unavailable":
      return "Workflow dependency readiness is temporarily unavailable."
    default:
      return apiErrorMessage(text, status, statusText)
  }
}

async function requestWorkflowControl<T>(
  path: string,
  options: RequestInit | undefined,
  fallbackMessage: string,
): Promise<T> {
  const res = await launcherFetch(path, options)
  const text = await res.text()
  if (!res.ok) {
    throw new Error(workflowControlErrorMessage(text, fallbackMessage))
  }
  try {
    return JSON.parse(text) as T
  } catch {
    throw new Error(fallbackMessage)
  }
}

function workflowControlErrorMessage(text: string, fallbackMessage: string) {
  const code = workflowErrorCode(text)
  switch (code) {
    case "template_not_found":
      return "That built-in workflow template is no longer available."
    case "template_overwrite_required":
      return "This workflow has local changes. Confirm Restore built-in to replace them."
    case "template_target_blocked":
      return "The template target is blocked and must be resolved manually."
    case "template_catalog_unavailable":
      return "Built-in workflow templates are unavailable."
    case "template_revalidation_failed":
      return "The template was not installed because workflow revalidation failed."
    case "template_rollback_failed":
      return "Template recovery needs operator attention. No further changes were attempted."
    case "template_recovery_failed":
      return "Template recovery needs operator attention. No further changes were attempted."
    case "workflow_development_active":
      return "Finish or discard the active workflow draft before changing workflow definitions or templates."
    case "config_revision_mismatch":
      return "Workflow settings changed elsewhere. Reload them and try again."
    case "invalid_workflow_settings":
      return "Workflow settings are invalid. Check the directory and numeric values."
    case "dependency_request_too_large":
      return "This workflow draft is too large to check for dependencies."
    case "invalid_dependency_request":
      return "Set a valid local workflow target before checking dependencies."
    case "workflow_not_found":
      return "The workflow dependency root is no longer available."
    case "workflow_invalid":
      return "Fix workflow validation errors before checking dependencies."
    case "dependency_check_unavailable":
      return "Workflow dependency readiness is temporarily unavailable."
    case "publish_request_too_large":
    case "invalid_publish_request":
      return "The publish request is invalid. Reload the draft and try again."
    case "workflow_development_not_found":
      return "The active workflow draft is no longer available."
    case "workflow_development_busy":
      return "Another workflow change is in progress. Wait and try again."
    case "session_revision_mismatch":
    case "draft_revision_mismatch":
    case "target_revision_mismatch":
      return "The workflow draft changed elsewhere. Reload it, test it, and check dependencies again."
    case "dependency_revision_mismatch":
      return "Workflow dependencies changed. Wait for a fresh readiness check and try again."
    case "workflow_dependencies_not_ready":
    case "workflow_publish_not_ready":
      return "Resolve workflow publish blockers and run a fresh test before publishing."
    case "workflow_publish_unavailable":
      return "Workflow publishing is temporarily unavailable."
    case "workflow_publish_recovery_failed":
    case "workflow_publish_rollback_failed":
      return "Workflow publish recovery needs operator attention. No further changes were attempted."
    case "workflow_transaction_recovery_conflict":
      return "Workflow recovery found files changed outside the interrupted transaction. Operator reconciliation is required; no files were changed."
    case "workflow_publish_gate_required":
      return "Workflow publishing is unavailable until dependency enforcement is restored."
    case "workflow_publish_failed":
      return "The workflow could not be published. Reload the draft and try again."
    default:
      return fallbackMessage
  }
}

function workflowErrorCode(text: string) {
  try {
    const body = JSON.parse(text) as { error?: unknown }
    return typeof body.error === "string" ? body.error : ""
  } catch {
    return ""
  }
}

function arrayOrEmpty<T>(value: T[] | null | undefined): T[] {
  return Array.isArray(value) ? value : []
}

function recordOrEmpty<T>(
  value: Record<string, T> | null | undefined,
): Record<string, T> {
  return value == null ? {} : value
}

function normalizeWorkflowCompatibilitySummary(
  summary: WorkflowCompatibilitySummary,
): WorkflowCompatibilitySummary {
  return {
    ...summary,
    workflows: arrayOrEmpty(summary.workflows),
    counts: recordOrEmpty(summary.counts),
  }
}

function normalizeWorkflowReloadResult(
  result: WorkflowReloadResult,
): WorkflowReloadResult {
  return {
    ...result,
    workflows: arrayOrEmpty(result.workflows),
    errors: arrayOrEmpty(result.errors),
  }
}

function normalizeWorkflowRun(run: WorkflowRun): WorkflowRun {
  return {
    ...run,
    child_run_ids: arrayOrEmpty(run.child_run_ids),
    jobs: recordOrEmpty(run.jobs),
    steps: recordOrEmpty(run.steps),
  }
}

function normalizeWorkflowRunGraph(graph: WorkflowRunGraph): WorkflowRunGraph {
  return {
    ...graph,
    nodes: arrayOrEmpty(graph.nodes),
    edges: arrayOrEmpty(graph.edges),
  }
}
