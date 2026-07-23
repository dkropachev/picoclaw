import { launcherFetch } from "@/api/http"

export interface WorkflowDefinition {
  ref: string
  name?: string
  path?: string
  error?: string
  workflow_call?: WorkflowCallDefinition
}

export interface WorkflowCallDefinition {
  inputs?: Record<string, WorkflowInputDefinition>
  secrets?: Record<string, WorkflowSecretDefinition>
}

export interface WorkflowInputDefinition {
  type?: string
  required?: boolean
  default?: unknown
}

export interface WorkflowSecretDefinition {
  required?: boolean
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
  target_workflow_ref: string
  run_id?: string
  status: string
  error?: string
  tested_at: string
}

export interface WorkflowDevelopmentSession {
  id: string
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

export interface WorkflowRun {
  id: string
  workflow_ref: string
  status: string
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
  error?: string
}

export interface WorkflowRunLaunchResult {
  result: WorkflowRunResult
  error?: string
}

export type WorkflowDeliveryPayload = Record<string, unknown>

async function request<T>(path: string, options?: RequestInit): Promise<T> {
  const res = await launcherFetch(path, options)
  if (!res.ok) {
    const text = await res.text()
    throw new Error(apiErrorMessage(text, res.status, res.statusText))
  }
  return res.json() as Promise<T>
}

function apiErrorMessage(text: string, status: number, statusText: string) {
  let message = text.trim()
  try {
    const body = JSON.parse(text) as {
      error?: string
      errors?: string[]
    }
    if (typeof body.error === "string" && body.error.trim() !== "") {
      message = body.error
    } else if (Array.isArray(body.errors) && body.errors.length > 0) {
      message = body.errors.join("; ")
    }
  } catch {
    // Keep the plain-text response when the backend did not return JSON.
  }
  return message || `API error: ${status} ${statusText}`
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

export async function getWorkflowDevelopment(): Promise<{
  session: WorkflowDevelopmentSession | null
}> {
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

export async function publishWorkflowDevelopment(): Promise<{
  workflow_ref: string
  session: WorkflowDevelopmentSession
}> {
  return request("/api/workflows/development/publish", { method: "POST" })
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
  throw new Error(apiErrorMessage(text, res.status, res.statusText))
}

export async function listWorkflowRuns(): Promise<{ runs: WorkflowRun[] }> {
  const payload = await request<{ runs?: WorkflowRun[] | null }>(
    "/api/workflows/runs",
  )
  return { runs: arrayOrEmpty(payload.runs).map(normalizeWorkflowRun) }
}

export async function getWorkflowRun(runID: string): Promise<WorkflowRun> {
  return normalizeWorkflowRun(
    await request<WorkflowRun>(
      `/api/workflows/runs/${encodeURIComponent(runID)}`,
    ),
  )
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
  reason = "canceled from dashboard",
): Promise<WorkflowRun> {
  return request(`/api/workflows/runs/${encodeURIComponent(runID)}/cancel`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ reason }),
  })
}

export async function retryWorkflowRun(
  runID: string,
  secrets?: Record<string, string>,
): Promise<WorkflowRunLaunchResult> {
  const res = await launcherFetch(
    `/api/workflows/runs/${encodeURIComponent(runID)}/retry`,
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ secrets }),
    },
  )
  return workflowRunLaunchResultFromResponse(res)
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
