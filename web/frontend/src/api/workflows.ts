import { launcherFetch } from "@/api/http"

export interface WorkflowDefinition {
  ref: string
  name?: string
  path?: string
  error?: string
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

async function request<T>(path: string, options?: RequestInit): Promise<T> {
  const res = await launcherFetch(path, options)
  if (!res.ok) {
    const text = await res.text()
    throw new Error(text.trim() || `API error: ${res.status} ${res.statusText}`)
  }
  return res.json() as Promise<T>
}

export async function listWorkflows(): Promise<{
  workflows: WorkflowDefinition[]
}> {
  return request("/api/workflows")
}

export async function reloadWorkflows(): Promise<WorkflowReloadResult> {
  return request("/api/workflows/reload", { method: "POST" })
}

export async function listWorkflowRuns(): Promise<{ runs: WorkflowRun[] }> {
  return request("/api/workflows/runs")
}

export async function getWorkflowRun(runID: string): Promise<WorkflowRun> {
  return request(`/api/workflows/runs/${encodeURIComponent(runID)}`)
}

export async function getWorkflowRunEvents(
  runID: string,
): Promise<{ run_id: string; events: WorkflowRunEvent[] }> {
  return request(`/api/workflows/runs/${encodeURIComponent(runID)}/events`)
}

export async function getWorkflowRunGraph(
  runID: string,
): Promise<WorkflowRunGraph> {
  return request(`/api/workflows/runs/${encodeURIComponent(runID)}/graph`)
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

export async function retryWorkflowRun(runID: string): Promise<{
  run_id: string
  status: string
  outputs?: Record<string, unknown>
}> {
  return request(`/api/workflows/runs/${encodeURIComponent(runID)}/retry`, {
    method: "POST",
  })
}
