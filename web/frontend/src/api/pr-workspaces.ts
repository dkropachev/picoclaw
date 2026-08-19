import { launcherFetch } from "@/api/http"

export type PRWorkspacePhase =
  | "intake"
  | "charter"
  | "review"
  | "triage"
  | "implementation"
  | "validation"
  | "completion_audit"
  | "publication"
  | "complete"

export type PRWorkspaceExecutionState =
  | "queued"
  | "running"
  | "waiting_gate"
  | "waiting_user"
  | "succeeded"
  | "failed"
  | "blocked"
  | "canceled"
  | "stale"
  | "unknown"

export type PRWorkspaceType =
  | "fix"
  | "refactor"
  | "feature"
  | "documentation"
  | "test"

export type PRWorkspaceScopeDistance =
  | "S0_exact"
  | "S1_necessary_adjacent"
  | "S2_related_followup"
  | "S3_unrelated"

export type PRWorkspaceChangeSize = "XS" | "S" | "M" | "L"
export type PRWorkspaceWorkPresence = "candidate_present" | "follow_up"
export type PRWorkspaceFindingDisposition =
  | "open"
  | "in_scope"
  | "fixed"
  | "deferred"
  | "dismissed"
export type PRWorkspaceFindingSeverity = string
export type PRWorkspaceFindingOrigin =
  | "review"
  | "implementation"
  | "nudge"
  | "user"
export type PRWorkspaceCorrectionKind =
  | "factual"
  | "finding_quality"
  | "scope"
  | "pr_type"
  | "implementation"
  | "validation"
  | "repository_preference"
export type PRWorkspaceCorrectionApplicability =
  | "review"
  | "implementation"
  | "both"
export type PRWorkspacePublicationKind =
  | "github_review"
  | "branch_push"
  | "github_issue"
export type PRWorkspacePublicationPhase = "review" | "implementation"
export type PRWorkspaceNudgeStrategy =
  | "acceptance_criteria"
  | "adversarial"
  | "coverage_gaps"
  | "error_recovery"
  | "integration_boundaries"
  | "validation_adequacy"

export interface PRWorkspaceRecord {
  id: string
  provider: string
  provider_origin: string
  repository_id: string
  repository: string
  pull_request_id: string
  pull_number: number
  phase: PRWorkspacePhase
  execution_state: PRWorkspaceExecutionState
  active_charter_id?: string
  provider_head_sha: string
  version: number
  created_at: string
  updated_at: string
}

export interface PRWorkspaceProviderSnapshot {
  provider: string
  provider_origin: string
  repository_id: string
  repository: string
  pull_request_id: string
  pull_number: number
  title: string
  body?: string
  author_id: string
  author_login: string
  authenticated_user_id: string
  base_ref: string
  base_sha: string
  head_repository_id: string
  head_ref: string
  head_sha: string
  state: "open" | "closed" | "merged"
  owned: boolean
  head_writable: boolean
  can_review: boolean
  can_create_issue: boolean
  provider_revision?: string
  observed_at: string
}

export interface PRWorkspaceCharter {
  id: string
  revision: number
  type: PRWorkspaceType
  goal: string
  acceptance_criteria: string[]
  included_areas: string[]
  excluded_areas: string[]
  non_goals: string[]
  base_sha: string
  head_sha: string
  confirmed: boolean
  created_at: string
  confirmed_at?: string
}

export interface PRWorkspaceStage {
  id: string
  stage: string
  state: PRWorkspaceExecutionState
  charter_id: string
  head_sha: string
  attempt: number
  prompt_digest?: string
  summary?: string
  evidence?: PRWorkspaceStageEvidence
  public_error?: string
  started_at: string
  finished_at?: string
}

export interface PRWorkspaceStageEvidence {
  stage: string
  run_id: string
  summary: string
  coverage: PRWorkspaceStageCoverage
  finding_ids: string[]
  validation?: Record<string, unknown>
  prompt_digest: string
  created_at: string
}

export interface PRWorkspaceStageCoverage {
  reviewed_areas: string[]
  unreviewed_areas: string[]
  tests_considered: string[]
  residual_risks: string[]
}

export interface PRWorkspaceScopeAssessment {
  distance: PRWorkspaceScopeDistance
  size: PRWorkspaceChangeSize
  presence?: PRWorkspaceWorkPresence
  files: number
  semantic_lines: number
  modules: number
  estimated: boolean
  type_compatible: boolean
  confidence: number
  charter_clauses?: string[]
  explanation?: string
  change_evidence?: PRWorkspaceScopeChange[]
}

export interface PRWorkspaceScopeChange {
  path: string
  hunk: string
  module: string
  semantic_lines: number
  presence: PRWorkspaceWorkPresence
  scope_distance: PRWorkspaceScopeDistance
  change_size: PRWorkspaceChangeSize
  type_compatible: boolean
  confidence: number
  charter_clauses?: string[]
  explanation: string
}

export interface PRWorkspaceFinding {
  id: string
  fingerprint: string
  origin: PRWorkspaceFindingOrigin
  origin_run_id?: string
  sourceAvailable?: boolean
  severity: PRWorkspaceFindingSeverity
  title: string
  message: string
  evidence?: string
  impact?: string
  recommendation?: string
  validation?: string
  file?: string
  line?: number
  scope: PRWorkspaceScopeAssessment
  disposition: PRWorkspaceFindingDisposition
  version: number
  created_at: string
  updated_at: string
}

export interface PRWorkspaceCorrection {
  id: string
  kind: PRWorkspaceCorrectionKind
  applicability: PRWorkspaceCorrectionApplicability
  target_type: string
  target_id: string
  original_claim: string
  correction: string
  evidence?: string
  charter_id?: string
  head_sha: string
  supersedes_id?: string
  promoted: boolean
  created_at: string
}

export interface PRWorkspaceNudgeRound {
  id: string
  stage: "review" | "implementation_completion"
  stage_run_id: string
  round: number
  minimum_rounds: number
  hard_cap: number
  strategy: PRWorkspaceNudgeStrategy
  challenge: string
  variant_digest: string
  prompt_digest: string
  state: PRWorkspaceExecutionState
  public_error?: string
  novel_findings: number
  duplicate_count: number
  finding_ids?: string[]
  resolved_findings: number
  reward?: number
  reward_provenance?: string
  created_at: string
}

export interface PRWorkspaceMessage {
  id: string
  role: string
  stage?: string
  content: string
  charter_id?: string
  head_sha?: string
  created_at: string
}

export interface PRWorkspaceRepositoryLesson {
  id: string
  repository_id: string
  source_workspace_id: string
  correction_id: string
  kind: PRWorkspaceCorrectionKind
  applicability: PRWorkspaceCorrectionApplicability
  pr_type?: PRWorkspaceType
  text: string
  active: boolean
  created_at: string
  revoked_at?: string
}

export interface PRWorkspaceDeferredGroup {
  id: string
  title: string
  body: string
  finding_ids: string[]
  scope: PRWorkspaceScopeAssessment
  labels?: string[]
  existing_issue_url?: string
  publication_id?: string
  publication_suppressed?: boolean
  suppression_reason?: string
  version: number
  created_at: string
  updated_at: string
}

export interface PRWorkspaceGateTurn {
  stage_id: string
  kind: string
  title: string
  status: string
  gate_form?: PRWorkspaceGateForm
  field_values?: Record<string, unknown>
  actor_kind?: "human" | "ai" | "deterministic" | "workflow"
  execution_id?: string
  action_revision?: string
  input_hash?: string
}

export type PRWorkspaceGateField =
  | {
      id: string
      type: "short-text"
      label: string
      required: boolean
    }
  | {
      id: string
      type: "long-text"
      label: string
      required: boolean
    }
  | {
      id: string
      type: "boolean"
      label: string
      required: boolean
    }
  | {
      id: string
      type: "select"
      label: string
      required: boolean
      min_selections: number
      max_selections: number
      options: Array<{ id: string; label: string }>
    }

export interface PRWorkspaceGateForm {
  gate_ref: string
  prompt: string
  fields: PRWorkspaceGateField[]
}

export interface PRWorkspaceGateSummary {
  id: string
  decision_point: string
  target_id?: string
  state: PRWorkspaceExecutionState
  policy_revision: string
  subject_revision: string
  turns: PRWorkspaceGateTurn[]
  evidence?: PRWorkspaceGateEvidence
  created_at: string
  finished_at?: string
}

export interface PRWorkspaceGateEvidence {
  charter_type?: PRWorkspaceType
  charter_goal?: string
  candidate_sha?: string
  changed_files?: string[]
  scope?: PRWorkspaceScopeAssessment
  hard_scope?: boolean
  hard_scope_finding_ids?: string[]
  validation_state?: PRWorkspaceExecutionState
  validation_checks?: PRWorkspaceValidationEvidence["checks"]
  finding_ids?: string[]
  finding_count?: number
  publication_kind?: PRWorkspacePublicationKind
  payload_digest?: string
  expected_head_sha?: string
  provider_revision?: string
  repository?: string
  review_summary?: string
  publication_findings?: PRWorkspaceGatePublicationFinding[]
  issue_title?: string
  issue_body?: string
  issue_labels?: string[]
  repair_summary?: string
}

export interface PRWorkspaceGatePublicationFinding {
  id: string
  title: string
  file?: string
  line?: number
  message: string
}

export interface PRWorkspacePublication {
  id: string
  kind: PRWorkspacePublicationKind
  state: PRWorkspaceExecutionState
  target_id?: string
  expected_head_sha?: string
  payload_digest: string
  external_id?: string
  external_url?: string
  public_error_code?: string
  attempts: number
  created_at: string
  updated_at: string
  published_at?: string
}

export interface PRWorkspaceValidationEvidence {
  id: string
  stage_run_id: string
  state: PRWorkspaceExecutionState
  candidate_sha: string
  checks: {
    id: string
    name: string
    status: string
    summary?: string
    exit_code?: number
    duration_ms?: number
  }[]
  started_at: string
  finished_at?: string
}

export interface PRWorkspaceActivity {
  ordinal: number
  kind: string
  actor: string
  summary: string
  entity_id?: string
  metadata?: Record<string, unknown>
  created_at: string
}

export interface PRWorkspaceRepairAttempt {
  id: string
  stage_run_id: string
  number: number
  state: PRWorkspaceExecutionState
  instruction: string
  workspace_id?: string
  result_summary?: string
  changed_files?: string[]
  candidate_sha?: string
  scope: PRWorkspaceScopeAssessment
  prompt_digest: string
  started_at: string
  finished_at?: string
}

export interface PRWorkspace {
  workspace: PRWorkspaceRecord
  provider_snapshot: PRWorkspaceProviderSnapshot
  charters: PRWorkspaceCharter[]
  stage_runs: PRWorkspaceStage[]
  findings: PRWorkspaceFinding[]
  messages: PRWorkspaceMessage[]
  corrections: PRWorkspaceCorrection[]
  repository_lessons: PRWorkspaceRepositoryLesson[]
  nudge_rounds: PRWorkspaceNudgeRound[]
  deferred_groups: PRWorkspaceDeferredGroup[]
  repair_attempts: PRWorkspaceRepairAttempt[]
  validation_runs: PRWorkspaceValidationEvidence[]
  gates: PRWorkspaceGateSummary[]
  publications: PRWorkspacePublication[]
  activity: PRWorkspaceActivity[]
}

export interface PRWorkspacePage {
  workspaces: PRWorkspaceRecord[]
  next_cursor?: string
}

export interface PRWorkspaceListParams {
  repository?: string
  phase?: PRWorkspacePhase
  state?: PRWorkspaceExecutionState
  ownership?: "owned" | "external"
  needs_action?: boolean
  limit?: number
  cursor?: string
}

export interface PRWorkspaceMutationFence {
  expected_version: number
  request_id: string
}

export class PRWorkspaceAPIError extends Error {
  readonly status: number
  readonly code: string
  readonly current?: PRWorkspace

  constructor(
    code: string,
    status: number,
    message = code,
    current?: PRWorkspace,
  ) {
    super(message)
    this.name = "PRWorkspaceAPIError"
    this.status = status
    this.code = code
    this.current = current
  }
}

const apiRoot = "/api/pr-workspaces"
const maximumResponseBytes = 8 << 20

export function createPRWorkspaceRequestID(): string {
  const random = globalThis.crypto?.randomUUID?.().replaceAll("-", "")
  if (!random) throw new Error("secure_random_unavailable")
  return `prq_${random}`
}

export async function listPRWorkspaces(
  params: PRWorkspaceListParams = {},
  signal?: AbortSignal,
): Promise<PRWorkspacePage> {
  const query = new URLSearchParams()
  if (params.repository) query.set("repository", params.repository)
  if (params.phase) query.set("phase", params.phase)
  if (params.state) query.set("state", params.state)
  if (params.ownership) query.set("ownership", params.ownership)
  if (params.needs_action != null) {
    query.set("needs_action", String(params.needs_action))
  }
  if (params.limit != null) query.set("limit", String(params.limit))
  if (params.cursor) query.set("cursor", params.cursor)
  const suffix = query.size > 0 ? `?${query.toString()}` : ""
  const value = await requestJSON<unknown>(
    `${apiRoot}${suffix}`,
    undefined,
    signal,
  )
  if (!isRecord(value) || !Array.isArray(value.workspaces)) malformed()
  return {
    workspaces: value.workspaces.map((workspace) => {
      if (!isRecord(workspace)) malformed()
      return projectWorkspaceRecord(workspace)
    }),
    ...(typeof value.next_cursor === "string"
      ? { next_cursor: value.next_cursor }
      : {}),
  }
}

export async function createPRWorkspace(
  input:
    | { pull_request_url: string; request_id: string }
    | {
        provider_origin: string
        repository: string
        pull_number: number
        request_id: string
      },
  signal?: AbortSignal,
): Promise<PRWorkspace> {
  return mutateWorkspace(apiRoot, "POST", input, signal)
}

export async function getPRWorkspace(
  workspaceID: string,
  signal?: AbortSignal,
): Promise<PRWorkspace> {
  return requestPRWorkspaceAggregate(
    workspacePath(workspaceID),
    undefined,
    signal,
  )
}

export async function refreshPRWorkspace(
  workspaceID: string,
  input: PRWorkspaceMutationFence,
  signal?: AbortSignal,
): Promise<PRWorkspace> {
  return mutateWorkspace(
    `${workspacePath(workspaceID)}/refresh`,
    "POST",
    input,
    signal,
  )
}

export interface PRWorkspaceCharterDraftInput extends PRWorkspaceMutationFence {
  expected_head_revision: string
}

export async function draftPRWorkspaceCharter(
  workspaceID: string,
  input: PRWorkspaceCharterDraftInput,
  signal?: AbortSignal,
): Promise<PRWorkspace> {
  return mutateWorkspace(
    `${workspacePath(workspaceID)}/charter/draft`,
    "POST",
    input,
    signal,
  )
}

export interface PRWorkspaceCharterInput extends PRWorkspaceMutationFence {
  expected_head_revision: string
  pr_type: PRWorkspaceType
  goal: string
  acceptance_criteria: string[]
  included_areas: string[]
  exclusions: string[]
  non_goals: string[]
}

export async function savePRWorkspaceCharter(
  workspaceID: string,
  input: PRWorkspaceCharterInput,
  signal?: AbortSignal,
): Promise<PRWorkspace> {
  return mutateWorkspace(
    `${workspacePath(workspaceID)}/charter`,
    "PUT",
    input,
    signal,
  )
}

export async function revisePRWorkspaceCharter(
  workspaceID: string,
  input: PRWorkspaceCharterInput & { expected_charter_revision: number },
  signal?: AbortSignal,
): Promise<PRWorkspace> {
  return mutateWorkspace(
    `${workspacePath(workspaceID)}/charter/revise`,
    "POST",
    input,
    signal,
  )
}

export async function confirmPRWorkspaceCharter(
  workspaceID: string,
  input: PRWorkspaceMutationFence & { expected_charter_revision: number },
  signal?: AbortSignal,
): Promise<PRWorkspace> {
  return mutateWorkspace(
    `${workspacePath(workspaceID)}/charter/confirm`,
    "POST",
    input,
    signal,
  )
}

export type PRWorkspaceRunKind =
  | "review-runs"
  | "implementation-runs"
  | "completion-audits"
  | "nudge-runs"

export async function startPRWorkspaceRun(
  workspaceID: string,
  kind: PRWorkspaceRunKind,
  input: PRWorkspaceMutationFence & {
    expected_head_revision: string
    finding_ids?: string[]
    instruction?: string
    stage?: "review" | "implementation_completion"
  },
  signal?: AbortSignal,
): Promise<PRWorkspace> {
  return mutateWorkspace(
    `${workspacePath(workspaceID)}/${kind}`,
    "POST",
    input,
    signal,
  )
}

export async function cancelPRWorkspaceStageRun(
  workspaceID: string,
  runID: string,
  input: PRWorkspaceMutationFence,
  signal?: AbortSignal,
): Promise<PRWorkspace> {
  return mutateWorkspace(
    `${workspacePath(workspaceID)}/stage-runs/${encodeURIComponent(runID)}/cancel`,
    "POST",
    input,
    signal,
  )
}

export async function setPRWorkspaceFindingDisposition(
  workspaceID: string,
  findingID: string,
  input: PRWorkspaceMutationFence & {
    disposition: PRWorkspaceFindingDisposition
    reason?: string
  },
  signal?: AbortSignal,
): Promise<PRWorkspace> {
  return mutateWorkspace(
    `${workspacePath(workspaceID)}/findings/${encodeURIComponent(findingID)}/disposition`,
    "POST",
    input,
    signal,
  )
}

export async function updatePRWorkspaceFinding(
  workspaceID: string,
  findingID: string,
  input: PRWorkspaceMutationFence & {
    title: string
    message: string
    evidence: string
    severity: PRWorkspaceFindingSeverity
    scope_distance: PRWorkspaceScopeDistance
    size: PRWorkspaceChangeSize
    type_compatible: boolean
  },
  signal?: AbortSignal,
): Promise<PRWorkspace> {
  return mutateWorkspace(
    `${workspacePath(workspaceID)}/findings/${encodeURIComponent(findingID)}`,
    "PATCH",
    input,
    signal,
  )
}

export async function createPRWorkspaceCorrection(
  workspaceID: string,
  input: PRWorkspaceMutationFence & {
    kind: PRWorkspaceCorrectionKind
    applicability: PRWorkspaceCorrectionApplicability
    original_claim: string
    correction: string
    reason?: string
    target_id?: string
  },
  signal?: AbortSignal,
): Promise<PRWorkspace> {
  return mutateWorkspace(
    `${workspacePath(workspaceID)}/corrections`,
    "POST",
    input,
    signal,
  )
}

export async function promotePRWorkspaceCorrection(
  workspaceID: string,
  correctionID: string,
  input: PRWorkspaceMutationFence,
  signal?: AbortSignal,
): Promise<PRWorkspace> {
  return mutateWorkspace(
    `${workspacePath(workspaceID)}/corrections/${encodeURIComponent(correctionID)}/promote`,
    "POST",
    input,
    signal,
  )
}

export async function sendPRWorkspaceMessage(
  workspaceID: string,
  input: PRWorkspaceMutationFence & {
    content: string
    stage: string
    mark_as_correction?: boolean
    applicability?: PRWorkspaceCorrectionApplicability
  },
  signal?: AbortSignal,
): Promise<PRWorkspace> {
  return mutateWorkspace(
    `${workspacePath(workspaceID)}/messages`,
    "POST",
    input,
    signal,
  )
}

export type PRWorkspaceDeferredAction =
  | "split"
  | "merge"
  | "link"
  | "publish"
  | "reconcile"

export async function regroupPRWorkspaceDeferredFindings(
  workspaceID: string,
  input: PRWorkspaceMutationFence,
  signal?: AbortSignal,
): Promise<PRWorkspace> {
  return mutateWorkspace(
    `${workspacePath(workspaceID)}/deferred-groups/regroup`,
    "POST",
    input,
    signal,
  )
}

export async function syncPRWorkspaceAutomaticDeferredIssues(
  workspaceID: string,
  input: PRWorkspaceMutationFence,
  signal?: AbortSignal,
): Promise<PRWorkspace> {
  return mutateWorkspace(
    `${workspacePath(workspaceID)}/deferred-groups/automatic-sync`,
    "POST",
    input,
    signal,
  )
}

export async function mutatePRWorkspaceDeferredGroup(
  workspaceID: string,
  groupID: string,
  action: PRWorkspaceDeferredAction,
  input: PRWorkspaceMutationFence & Record<string, unknown>,
  signal?: AbortSignal,
): Promise<PRWorkspace> {
  return mutateWorkspace(
    `${workspacePath(workspaceID)}/deferred-groups/${encodeURIComponent(groupID)}/${action}`,
    "POST",
    input,
    signal,
  )
}

export async function updatePRWorkspaceDeferredGroup(
  workspaceID: string,
  groupID: string,
  input: PRWorkspaceMutationFence & {
    title: string
    body: string
    labels?: string[]
  },
  signal?: AbortSignal,
): Promise<PRWorkspace> {
  return mutateWorkspace(
    `${workspacePath(workspaceID)}/deferred-groups/${encodeURIComponent(groupID)}`,
    "PATCH",
    input,
    signal,
  )
}

export async function publishPRWorkspacePhase(
  workspaceID: string,
  phase: PRWorkspacePublicationPhase,
  input: PRWorkspaceMutationFence & {
    expected_head_revision: string
    finding_ids?: string[]
  },
  signal?: AbortSignal,
): Promise<PRWorkspace> {
  return mutateWorkspace(
    `${workspacePath(workspaceID)}/publications/${phase}`,
    "POST",
    input,
    signal,
  )
}

export async function reconcilePRWorkspacePublication(
  workspaceID: string,
  publicationID: string,
  input: PRWorkspaceMutationFence & { expected_head_revision: string },
  signal?: AbortSignal,
): Promise<PRWorkspace> {
  return mutateWorkspace(
    `${workspacePath(workspaceID)}/publications/${encodeURIComponent(publicationID)}/reconcile`,
    "POST",
    input,
    signal,
  )
}

function workspacePath(workspaceID: string): string {
  return `${apiRoot}/${encodeURIComponent(workspaceID)}`
}

async function mutateWorkspace(
  path: string,
  method: "POST" | "PUT" | "PATCH",
  body: unknown,
  signal?: AbortSignal,
): Promise<PRWorkspace> {
  return requestPRWorkspaceAggregate(
    path,
    {
      method,
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    },
    signal,
  )
}

export async function requestPRWorkspaceJSON<T>(
  path: string,
  init?: RequestInit,
  signal?: AbortSignal,
): Promise<T> {
  return requestJSON<T>(path, init, signal)
}

export async function requestPRWorkspaceAggregate(
  path: string,
  init?: RequestInit,
  signal?: AbortSignal,
): Promise<PRWorkspace> {
  return projectPRWorkspace(await requestJSON<unknown>(path, init, signal))
}

async function requestJSON<T>(
  path: string,
  init?: RequestInit,
  signal?: AbortSignal,
): Promise<T> {
  const response = await launcherFetch(path, { ...init, signal })
  const text = await boundedResponseText(response)
  let value: unknown
  try {
    value = text === "" ? null : JSON.parse(text)
  } catch {
    throw new PRWorkspaceAPIError("malformed_response", 502)
  }
  if (!response.ok) {
    const error = isRecord(value) ? value : undefined
    const code = typeof error?.code === "string" ? error.code : "request_failed"
    const message =
      typeof error?.message === "string" ? error.message : "Request failed."
    const current = isRecord(error?.current)
      ? projectPRWorkspace(error.current)
      : undefined
    throw new PRWorkspaceAPIError(code, response.status, message, current)
  }
  if (
    !jsonContentType(response.headers.get("Content-Type")) ||
    !isRecord(value)
  ) {
    throw new PRWorkspaceAPIError("malformed_response", 502)
  }
  return value as T
}

async function boundedResponseText(response: Response): Promise<string> {
  const declared = Number(response.headers.get("Content-Length"))
  if (Number.isFinite(declared) && declared > maximumResponseBytes) {
    throw new PRWorkspaceAPIError("response_too_large", 502)
  }
  const text = await response.text()
  if (new TextEncoder().encode(text).byteLength > maximumResponseBytes) {
    throw new PRWorkspaceAPIError("response_too_large", 502)
  }
  return text
}

function jsonContentType(contentType: string | null): boolean {
  return (
    contentType?.split(";", 1)[0]?.trim().toLowerCase() === "application/json"
  )
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value)
}

const phases = new Set<PRWorkspacePhase>([
  "intake",
  "charter",
  "review",
  "triage",
  "implementation",
  "validation",
  "completion_audit",
  "publication",
  "complete",
])
const executionStates = new Set<PRWorkspaceExecutionState>([
  "queued",
  "running",
  "waiting_gate",
  "waiting_user",
  "succeeded",
  "failed",
  "blocked",
  "canceled",
  "stale",
  "unknown",
])
const prTypes = new Set<PRWorkspaceType>([
  "fix",
  "refactor",
  "feature",
  "documentation",
  "test",
])
const scopeDistances = new Set<PRWorkspaceScopeDistance>([
  "S0_exact",
  "S1_necessary_adjacent",
  "S2_related_followup",
  "S3_unrelated",
])
const changeSizes = new Set<PRWorkspaceChangeSize>(["XS", "S", "M", "L"])
const workPresences = new Set<PRWorkspaceWorkPresence>([
  "candidate_present",
  "follow_up",
])
const findingDispositions = new Set<PRWorkspaceFindingDisposition>([
  "open",
  "in_scope",
  "fixed",
  "deferred",
  "dismissed",
])
const findingOrigins = new Set<PRWorkspaceFindingOrigin>([
  "review",
  "implementation",
  "nudge",
  "user",
])
const correctionKinds = new Set<PRWorkspaceCorrectionKind>([
  "factual",
  "finding_quality",
  "scope",
  "pr_type",
  "implementation",
  "validation",
  "repository_preference",
])
const correctionApplicabilities = new Set<PRWorkspaceCorrectionApplicability>([
  "review",
  "implementation",
  "both",
])
const nudgeStages = new Set<PRWorkspaceNudgeRound["stage"]>([
  "review",
  "implementation_completion",
])
const nudgeStrategies = new Set<PRWorkspaceNudgeStrategy>([
  "acceptance_criteria",
  "adversarial",
  "coverage_gaps",
  "error_recovery",
  "integration_boundaries",
  "validation_adequacy",
])
const publicationKinds = new Set<PRWorkspacePublicationKind>([
  "github_review",
  "branch_push",
  "github_issue",
])

function projectPRWorkspace(value: unknown): PRWorkspace {
  if (
    !isRecord(value) ||
    !isRecord(value.workspace) ||
    !isRecord(value.provider_snapshot)
  ) {
    malformed()
  }
  const workspace = projectWorkspaceRecord(value.workspace)
  const providerSnapshot = projectProviderSnapshot(value.provider_snapshot)
  if (
    workspace.provider !== providerSnapshot.provider ||
    workspace.provider_origin !== providerSnapshot.provider_origin ||
    workspace.repository_id !== providerSnapshot.repository_id ||
    workspace.repository !== providerSnapshot.repository ||
    workspace.pull_request_id !== providerSnapshot.pull_request_id ||
    workspace.pull_number !== providerSnapshot.pull_number ||
    workspace.provider_head_sha !== providerSnapshot.head_sha
  ) {
    malformed()
  }
  return {
    workspace,
    provider_snapshot: providerSnapshot,
    charters: optionalProjectedArray(value.charters, projectCharter),
    stage_runs: optionalProjectedArray(value.stage_runs, projectStageRun),
    findings: optionalProjectedArray(value.findings, projectFinding),
    messages: optionalProjectedArray(value.messages, projectMessage),
    corrections: optionalProjectedArray(value.corrections, projectCorrection),
    repository_lessons: optionalProjectedArray(
      value.repository_lessons,
      projectRepositoryLesson,
    ),
    nudge_rounds: optionalProjectedArray(value.nudge_rounds, projectNudgeRound),
    deferred_groups: optionalProjectedArray(
      value.deferred_groups,
      projectDeferredGroup,
    ),
    repair_attempts: optionalProjectedArray(
      value.repair_attempts,
      projectRepairAttempt,
    ),
    validation_runs: optionalProjectedArray(
      value.validation_runs,
      projectValidationRun,
    ),
    gates: optionalProjectedArray(value.gates, projectGateRecord),
    publications: optionalProjectedArray(
      value.publications,
      projectPublication,
    ),
    activity: optionalProjectedArray(value.activity, projectActivity),
  }
}

function projectWorkspaceRecord(
  value: Record<string, unknown>,
): PRWorkspaceRecord {
  const activeCharterID = optionalString(value.active_charter_id)
  return {
    id: requiredString(value.id),
    provider: requiredString(value.provider),
    provider_origin: webOrigin(value.provider_origin),
    repository_id: requiredString(value.repository_id),
    repository: requiredString(value.repository),
    pull_request_id: requiredString(value.pull_request_id),
    pull_number: positiveInteger(value.pull_number),
    phase: enumValue(value.phase, phases),
    execution_state: enumValue(value.execution_state, executionStates),
    ...(activeCharterID === undefined
      ? {}
      : { active_charter_id: activeCharterID }),
    provider_head_sha: requiredString(value.provider_head_sha),
    version: nonNegativeInteger(value.version),
    created_at: requiredString(value.created_at),
    updated_at: requiredString(value.updated_at),
  }
}

function projectProviderSnapshot(
  value: Record<string, unknown>,
): PRWorkspaceProviderSnapshot {
  const state = requiredString(value.state)
  if (state !== "open" && state !== "closed" && state !== "merged") malformed()
  const body = optionalString(value.body)
  const providerRevision = optionalString(value.provider_revision)
  return {
    provider: requiredString(value.provider),
    provider_origin: webOrigin(value.provider_origin),
    repository_id: requiredString(value.repository_id),
    repository: requiredString(value.repository),
    pull_request_id: requiredString(value.pull_request_id),
    pull_number: positiveInteger(value.pull_number),
    title: stringValue(value.title),
    ...(body === undefined ? {} : { body }),
    author_id: stringValue(value.author_id),
    author_login: stringValue(value.author_login),
    authenticated_user_id: stringValue(value.authenticated_user_id),
    base_ref: stringValue(value.base_ref),
    base_sha: requiredString(value.base_sha),
    head_repository_id: stringValue(value.head_repository_id),
    head_ref: stringValue(value.head_ref),
    head_sha: requiredString(value.head_sha),
    state,
    owned: booleanValue(value.owned),
    head_writable: booleanValue(value.head_writable),
    can_review: booleanValue(value.can_review),
    can_create_issue: booleanValue(value.can_create_issue),
    ...(providerRevision === undefined
      ? {}
      : { provider_revision: providerRevision }),
    observed_at: requiredString(value.observed_at),
  }
}

function projectCharter(value: Record<string, unknown>): PRWorkspaceCharter {
  const confirmedAt = optionalString(value.confirmed_at)
  return {
    id: requiredString(value.id),
    revision: positiveInteger(value.revision),
    type: enumValue(value.type, prTypes),
    goal: requiredString(value.goal),
    acceptance_criteria: stringArray(value.acceptance_criteria),
    included_areas: stringArray(value.included_areas),
    excluded_areas: stringArray(value.excluded_areas),
    non_goals: stringArray(value.non_goals),
    base_sha: requiredString(value.base_sha),
    head_sha: requiredString(value.head_sha),
    confirmed: booleanValue(value.confirmed),
    created_at: requiredString(value.created_at),
    ...(confirmedAt === undefined ? {} : { confirmed_at: confirmedAt }),
  }
}

function projectStageRun(value: Record<string, unknown>): PRWorkspaceStage {
  const promptDigest = optionalString(value.prompt_digest)
  const summary = optionalString(value.summary)
  const evidence =
    value.evidence == null ? undefined : projectStageEvidence(value.evidence)
  const publicError = optionalString(value.public_error)
  const finishedAt = optionalString(value.finished_at)
  return {
    id: requiredString(value.id),
    stage: requiredString(value.stage),
    state: enumValue(value.state, executionStates),
    charter_id: requiredString(value.charter_id),
    head_sha: requiredString(value.head_sha),
    attempt: positiveInteger(value.attempt),
    ...(promptDigest === undefined ? {} : { prompt_digest: promptDigest }),
    ...(summary === undefined ? {} : { summary }),
    ...(evidence === undefined ? {} : { evidence }),
    ...(publicError === undefined ? {} : { public_error: publicError }),
    started_at: requiredString(value.started_at),
    ...(finishedAt === undefined ? {} : { finished_at: finishedAt }),
  }
}

function projectStageEvidence(value: unknown): PRWorkspaceStageEvidence {
  if (!isRecord(value)) malformed()
  const validation = optionalRecord(value.validation)
  return {
    stage: requiredString(value.stage),
    run_id: requiredString(value.run_id),
    summary: requiredString(value.summary),
    coverage: projectStageCoverage(value.coverage),
    finding_ids: stringArray(value.finding_ids),
    ...(validation === undefined ? {} : { validation }),
    prompt_digest: requiredString(value.prompt_digest),
    created_at: requiredString(value.created_at),
  }
}

function projectStageCoverage(value: unknown): PRWorkspaceStageCoverage {
  if (!isRecord(value)) malformed()
  return {
    reviewed_areas: stringArray(value.reviewed_areas),
    unreviewed_areas: stringArray(value.unreviewed_areas),
    tests_considered: stringArray(value.tests_considered),
    residual_risks: stringArray(value.residual_risks),
  }
}

function projectScope(value: unknown): PRWorkspaceScopeAssessment {
  if (!isRecord(value)) malformed()
  const charterClauses = optionalStringArray(value.charter_clauses)
  const explanation = optionalString(value.explanation)
  const presence = optionalEnum(value.presence, workPresences)
  const changeEvidence =
    value.change_evidence == null
      ? undefined
      : optionalProjectedArray(value.change_evidence, projectScopeChange)
  const confidence = finiteNumber(value.confidence)
  if (confidence < 0 || confidence > 1) malformed()
  return {
    distance: enumValue(value.distance, scopeDistances),
    size: enumValue(value.size, changeSizes),
    ...(presence === undefined ? {} : { presence }),
    files: nonNegativeInteger(value.files),
    semantic_lines: nonNegativeInteger(value.semantic_lines),
    modules: nonNegativeInteger(value.modules),
    estimated: booleanValue(value.estimated),
    type_compatible: booleanValue(value.type_compatible),
    confidence,
    ...(charterClauses === undefined
      ? {}
      : { charter_clauses: charterClauses }),
    ...(explanation === undefined ? {} : { explanation }),
    ...(changeEvidence === undefined
      ? {}
      : { change_evidence: changeEvidence }),
  }
}

function projectScopeChange(
  value: Record<string, unknown>,
): PRWorkspaceScopeChange {
  const confidence = finiteNumber(value.confidence)
  if (confidence < 0 || confidence > 1) malformed()
  const charterClauses = optionalStringArray(value.charter_clauses)
  return {
    path: requiredString(value.path),
    hunk: stringValue(value.hunk),
    module: requiredString(value.module),
    semantic_lines: nonNegativeInteger(value.semantic_lines),
    presence: enumValue(value.presence, workPresences),
    scope_distance: enumValue(value.scope_distance, scopeDistances),
    change_size: enumValue(value.change_size, changeSizes),
    type_compatible: booleanValue(value.type_compatible),
    confidence,
    ...(charterClauses === undefined
      ? {}
      : { charter_clauses: charterClauses }),
    explanation: requiredString(value.explanation),
  }
}

function projectFinding(value: Record<string, unknown>): PRWorkspaceFinding {
  const originRunID = optionalString(value.origin_run_id)
  const sourceAvailable =
    value.source_available == null
      ? undefined
      : booleanValue(value.source_available)
  const file = optionalString(value.file)
  const line = optionalNonNegativeInteger(value.line)
  const evidence = optionalString(value.evidence)
  const impact = optionalString(value.impact)
  const recommendation = optionalString(value.recommendation)
  const validation = optionalString(value.validation)
  return {
    id: requiredString(value.id),
    fingerprint: requiredString(value.fingerprint),
    origin: enumValue(value.origin, findingOrigins),
    ...(originRunID === undefined ? {} : { origin_run_id: originRunID }),
    ...(sourceAvailable === undefined ? {} : { sourceAvailable }),
    severity: requiredString(value.severity),
    title: requiredString(value.title),
    ...(file === undefined ? {} : { file }),
    ...(line === undefined ? {} : { line }),
    message: requiredString(value.message),
    ...(evidence === undefined ? {} : { evidence }),
    ...(impact === undefined ? {} : { impact }),
    ...(recommendation === undefined ? {} : { recommendation }),
    ...(validation === undefined ? {} : { validation }),
    scope: projectScope(value.scope),
    disposition: enumValue(value.disposition, findingDispositions),
    version: nonNegativeInteger(value.version),
    created_at: requiredString(value.created_at),
    updated_at: requiredString(value.updated_at),
  }
}

function projectMessage(value: Record<string, unknown>): PRWorkspaceMessage {
  const stage = optionalString(value.stage)
  const charterID = optionalString(value.charter_id)
  const headSHA = optionalString(value.head_sha)
  return {
    id: requiredString(value.id),
    role: requiredString(value.role),
    ...(stage === undefined ? {} : { stage }),
    content: requiredString(value.content),
    ...(charterID === undefined ? {} : { charter_id: charterID }),
    ...(headSHA === undefined ? {} : { head_sha: headSHA }),
    created_at: requiredString(value.created_at),
  }
}

function projectCorrection(
  value: Record<string, unknown>,
): PRWorkspaceCorrection {
  const evidence = optionalString(value.evidence)
  const charterID = optionalString(value.charter_id)
  const supersedesID = optionalString(value.supersedes_id)
  return {
    id: requiredString(value.id),
    kind: enumValue(value.kind, correctionKinds),
    applicability: enumValue(value.applicability, correctionApplicabilities),
    target_type: requiredString(value.target_type),
    target_id: requiredString(value.target_id),
    original_claim: requiredString(value.original_claim),
    correction: requiredString(value.correction),
    ...(evidence === undefined ? {} : { evidence }),
    ...(charterID ? { charter_id: charterID } : {}),
    head_sha: requiredString(value.head_sha),
    ...(supersedesID === undefined ? {} : { supersedes_id: supersedesID }),
    promoted: booleanValue(value.promoted),
    created_at: requiredString(value.created_at),
  }
}

function projectRepositoryLesson(
  value: Record<string, unknown>,
): PRWorkspaceRepositoryLesson {
  const rawType = optionalString(value.pr_type)
  const revokedAt = optionalString(value.revoked_at)
  const prType = rawType === undefined ? undefined : enumValue(rawType, prTypes)
  return {
    id: requiredString(value.id),
    repository_id: requiredString(value.repository_id),
    source_workspace_id: requiredString(value.source_workspace_id),
    correction_id: requiredString(value.correction_id),
    kind: enumValue(value.kind, correctionKinds),
    applicability: enumValue(value.applicability, correctionApplicabilities),
    ...(prType === undefined ? {} : { pr_type: prType }),
    text: requiredString(value.text),
    active: booleanValue(value.active),
    created_at: requiredString(value.created_at),
    ...(revokedAt === undefined ? {} : { revoked_at: revokedAt }),
  }
}

function projectNudgeRound(
  value: Record<string, unknown>,
): PRWorkspaceNudgeRound {
  const reward = optionalFiniteNumber(value.reward)
  const rewardProvenance = optionalString(value.reward_provenance)
  const publicError = optionalString(value.public_error)
  const findingIDs = optionalStringArray(value.finding_ids)
  return {
    id: requiredString(value.id),
    stage_run_id: requiredString(value.stage_run_id),
    stage: enumValue(value.stage, nudgeStages),
    round: positiveInteger(value.round),
    minimum_rounds: nonNegativeInteger(value.minimum_rounds),
    hard_cap: nonNegativeInteger(value.hard_cap),
    strategy: enumValue(value.strategy, nudgeStrategies),
    challenge: requiredString(value.challenge),
    variant_digest: requiredString(value.variant_digest),
    prompt_digest: requiredString(value.prompt_digest),
    state: enumValue(value.state, executionStates),
    ...(publicError === undefined ? {} : { public_error: publicError }),
    novel_findings: nonNegativeInteger(value.novel_findings),
    duplicate_count: nonNegativeInteger(value.duplicate_count),
    ...(findingIDs === undefined ? {} : { finding_ids: findingIDs }),
    resolved_findings: nonNegativeInteger(value.resolved_findings),
    ...(reward === undefined ? {} : { reward }),
    ...(rewardProvenance === undefined
      ? {}
      : { reward_provenance: rewardProvenance }),
    created_at: requiredString(value.created_at),
  }
}

function projectDeferredGroup(
  value: Record<string, unknown>,
): PRWorkspaceDeferredGroup {
  const labels = optionalStringArray(value.labels)
  const existingIssueURL = optionalHTTPSURL(value.existing_issue_url)
  const publicationID = optionalString(value.publication_id)
  const publicationSuppressed =
    value.publication_suppressed == null
      ? undefined
      : booleanValue(value.publication_suppressed)
  const suppressionReason = optionalString(value.suppression_reason)
  return {
    id: requiredString(value.id),
    title: requiredString(value.title),
    body: requiredString(value.body),
    finding_ids: stringArray(value.finding_ids),
    scope: projectScope(value.scope),
    ...(labels === undefined ? {} : { labels }),
    ...(existingIssueURL === undefined
      ? {}
      : { existing_issue_url: existingIssueURL }),
    ...(publicationID === undefined ? {} : { publication_id: publicationID }),
    ...(publicationSuppressed === undefined
      ? {}
      : { publication_suppressed: publicationSuppressed }),
    ...(suppressionReason === undefined
      ? {}
      : { suppression_reason: suppressionReason }),
    version: nonNegativeInteger(value.version),
    created_at: requiredString(value.created_at),
    updated_at: requiredString(value.updated_at),
  }
}

function projectRepairAttempt(
  value: Record<string, unknown>,
): PRWorkspaceRepairAttempt {
  const workspaceID = optionalString(value.workspace_id)
  const resultSummary = optionalString(value.result_summary)
  const changedFiles = optionalStringArray(value.changed_files)
  const candidateSHA = optionalString(value.candidate_sha)
  const finishedAt = optionalString(value.finished_at)
  return {
    id: requiredString(value.id),
    stage_run_id: requiredString(value.stage_run_id),
    number: positiveInteger(value.number),
    state: enumValue(value.state, executionStates),
    instruction: requiredString(value.instruction),
    ...(workspaceID === undefined ? {} : { workspace_id: workspaceID }),
    ...(resultSummary === undefined ? {} : { result_summary: resultSummary }),
    ...(changedFiles === undefined ? {} : { changed_files: changedFiles }),
    ...(candidateSHA === undefined ? {} : { candidate_sha: candidateSHA }),
    scope: projectScope(value.scope),
    prompt_digest: requiredString(value.prompt_digest),
    started_at: requiredString(value.started_at),
    ...(finishedAt === undefined ? {} : { finished_at: finishedAt }),
  }
}

function projectValidationRun(
  value: Record<string, unknown>,
): PRWorkspaceValidationEvidence {
  const finishedAt = optionalString(value.finished_at)
  return {
    id: requiredString(value.id),
    stage_run_id: requiredString(value.stage_run_id),
    state: enumValue(value.state, executionStates),
    candidate_sha: requiredString(value.candidate_sha),
    checks: optionalProjectedArray(value.checks, projectValidationCheck),
    started_at: requiredString(value.started_at),
    ...(finishedAt === undefined ? {} : { finished_at: finishedAt }),
  }
}

function projectValidationCheck(value: Record<string, unknown>) {
  const summary = optionalString(value.summary)
  const exitCode = optionalInteger(value.exit_code)
  const durationMS = optionalNonNegativeInteger(value.duration_ms)
  return {
    id: requiredString(value.id),
    name: requiredString(value.name),
    status: requiredString(value.status),
    ...(summary === undefined ? {} : { summary }),
    ...(exitCode === undefined ? {} : { exit_code: exitCode }),
    ...(durationMS === undefined ? {} : { duration_ms: durationMS }),
  }
}

export function projectPRWorkspaceGateSummary(
  value: unknown,
): PRWorkspaceGateSummary {
  if (!isRecord(value)) malformed()
  return projectGateRecord(value)
}

function projectGateRecord(
  value: Record<string, unknown>,
): PRWorkspaceGateSummary {
  const targetID = optionalString(value.target_id)
  const finishedAt = optionalString(value.finished_at)
  const evidence = projectGateEvidence(value.evidence)
  return {
    id: requiredString(value.id),
    decision_point: requiredString(value.decision_point),
    ...(targetID === undefined ? {} : { target_id: targetID }),
    state: enumValue(value.state, executionStates),
    policy_revision: requiredString(value.policy_revision),
    subject_revision: requiredString(value.subject_revision),
    turns: optionalProjectedArray(value.turns, projectGateTurn),
    ...(evidence === undefined ? {} : { evidence }),
    created_at: requiredString(value.created_at),
    ...(finishedAt === undefined ? {} : { finished_at: finishedAt }),
  }
}

function projectGateEvidence(
  value: unknown,
): PRWorkspaceGateEvidence | undefined {
  if (value == null) return undefined
  if (!isRecord(value)) malformed()
  const charterType = optionalEnum(value.charter_type, prTypes)
  const charterGoal = optionalString(value.charter_goal)
  const candidateSHA = optionalString(value.candidate_sha)
  const changedFiles = optionalStringArray(value.changed_files)
  const scope = value.scope == null ? undefined : projectScope(value.scope)
  const hardScope =
    value.hard_scope == null ? undefined : booleanValue(value.hard_scope)
  const hardScopeFindingIDs = optionalStringArray(value.hard_scope_finding_ids)
  const validationState = optionalEnum(value.validation_state, executionStates)
  const validationChecks =
    value.validation_checks == null
      ? undefined
      : optionalProjectedArray(value.validation_checks, projectValidationCheck)
  const findingIDs = optionalStringArray(value.finding_ids)
  const findingCount = optionalNonNegativeInteger(value.finding_count)
  const publicationKind = optionalEnum(value.publication_kind, publicationKinds)
  const payloadDigest = optionalString(value.payload_digest)
  const expectedHeadSHA = optionalString(value.expected_head_sha)
  const providerRevision = optionalString(value.provider_revision)
  const repository = optionalString(value.repository)
  const reviewSummary = optionalString(value.review_summary)
  const publicationFindings =
    value.publication_findings == null
      ? undefined
      : optionalProjectedArray(
          value.publication_findings,
          projectGatePublicationFinding,
        )
  const issueTitle = optionalString(value.issue_title)
  const issueBody = optionalString(value.issue_body)
  const issueLabels = optionalStringArray(value.issue_labels)
  const repairSummary = optionalString(value.repair_summary)
  return {
    ...(charterType === undefined ? {} : { charter_type: charterType }),
    ...(charterGoal === undefined ? {} : { charter_goal: charterGoal }),
    ...(candidateSHA === undefined ? {} : { candidate_sha: candidateSHA }),
    ...(changedFiles === undefined ? {} : { changed_files: changedFiles }),
    ...(scope === undefined ? {} : { scope }),
    ...(hardScope === undefined ? {} : { hard_scope: hardScope }),
    ...(hardScopeFindingIDs === undefined
      ? {}
      : { hard_scope_finding_ids: hardScopeFindingIDs }),
    ...(validationState === undefined
      ? {}
      : { validation_state: validationState }),
    ...(validationChecks === undefined
      ? {}
      : { validation_checks: validationChecks }),
    ...(findingIDs === undefined ? {} : { finding_ids: findingIDs }),
    ...(findingCount === undefined ? {} : { finding_count: findingCount }),
    ...(publicationKind === undefined
      ? {}
      : { publication_kind: publicationKind }),
    ...(payloadDigest === undefined ? {} : { payload_digest: payloadDigest }),
    ...(expectedHeadSHA === undefined
      ? {}
      : { expected_head_sha: expectedHeadSHA }),
    ...(providerRevision === undefined
      ? {}
      : { provider_revision: providerRevision }),
    ...(repository === undefined ? {} : { repository }),
    ...(reviewSummary === undefined ? {} : { review_summary: reviewSummary }),
    ...(publicationFindings === undefined
      ? {}
      : { publication_findings: publicationFindings }),
    ...(issueTitle === undefined ? {} : { issue_title: issueTitle }),
    ...(issueBody === undefined ? {} : { issue_body: issueBody }),
    ...(issueLabels === undefined ? {} : { issue_labels: issueLabels }),
    ...(repairSummary === undefined ? {} : { repair_summary: repairSummary }),
  }
}

function projectGatePublicationFinding(
  value: Record<string, unknown>,
): PRWorkspaceGatePublicationFinding {
  const file = optionalString(value.file)
  const line = optionalNonNegativeInteger(value.line)
  return {
    id: requiredString(value.id),
    title: requiredString(value.title),
    ...(file === undefined ? {} : { file }),
    ...(line === undefined ? {} : { line }),
    message: requiredString(value.message),
  }
}

function projectGateTurn(value: Record<string, unknown>): PRWorkspaceGateTurn {
  const gateForm = projectGateForm(value["gate-form"])
  const fieldValues = optionalRecord(value["field-values"])
  const actorKind = optionalEnum(
    value["actor-kind"],
    new Set(["human", "ai", "deterministic", "workflow"] as const),
  )
  const actionRevision = optionalString(value["action-revision"])
  const executionID = optionalString(value["execution-id"])
  const inputHash = optionalString(value["input-hash"])
  return {
    stage_id: requiredString(value.stage_id),
    kind: requiredString(value.kind),
    title: stringValue(value.title),
    status: requiredString(value.status),
    ...(gateForm === undefined ? {} : { gate_form: gateForm }),
    ...(fieldValues === undefined ? {} : { field_values: fieldValues }),
    ...(actorKind === undefined ? {} : { actor_kind: actorKind }),
    ...(executionID === undefined ? {} : { execution_id: executionID }),
    ...(actionRevision === undefined
      ? {}
      : { action_revision: actionRevision }),
    ...(inputHash === undefined ? {} : { input_hash: inputHash }),
  }
}

function projectGateForm(value: unknown): PRWorkspaceGateForm | undefined {
  if (value == null) return undefined
  if (
    !isRecord(value) ||
    !Array.isArray(value.fields) ||
    value.fields.length > 64
  )
    malformed()
  const fieldIDs = new Set<string>()
  return {
    gate_ref: requiredString(value["gate-ref"]),
    prompt: requiredString(value.prompt),
    fields: value.fields.map((entry) => {
      if (!isRecord(entry)) malformed()
      const id = requiredString(entry.id)
      if (fieldIDs.has(id)) malformed()
      fieldIDs.add(id)
      const label = requiredString(entry.label)
      const required =
        entry.required === undefined ? false : booleanValue(entry.required)
      const type = requiredString(entry.type)
      if (type === "short-text" || type === "long-text" || type === "boolean") {
        return { id, label, required, type }
      }
      if (type !== "select" || !Array.isArray(entry.options)) malformed()
      const minimum = optionalNonNegativeInteger(entry["min-selections"]) ?? 0
      const maximum = optionalNonNegativeInteger(entry["max-selections"]) ?? 1
      if (
        maximum < 1 ||
        minimum > maximum ||
        entry.options.length === 0 ||
        entry.options.length > 128 ||
        maximum > entry.options.length
      ) {
        malformed()
      }
      const optionIDs = new Set<string>()
      return {
        id,
        label,
        required,
        type,
        min_selections: minimum,
        max_selections: maximum,
        options: entry.options.map((option) => {
          if (!isRecord(option)) malformed()
          const optionID = requiredString(option.id)
          if (optionIDs.has(optionID)) malformed()
          optionIDs.add(optionID)
          return { id: optionID, label: requiredString(option.label) }
        }),
      }
    }),
  }
}

function projectPublication(
  value: Record<string, unknown>,
): PRWorkspacePublication {
  const targetID = optionalString(value.target_id)
  const expectedHeadSHA = optionalString(value.expected_head_sha)
  const externalID = optionalString(value.external_id)
  const externalURL = optionalHTTPSURL(value.external_url)
  const publicErrorCode = optionalString(value.public_error_code)
  const publishedAt = optionalString(value.published_at)
  return {
    id: requiredString(value.id),
    kind: enumValue(value.kind, publicationKinds),
    state: enumValue(value.state, executionStates),
    ...(targetID === undefined ? {} : { target_id: targetID }),
    ...(expectedHeadSHA === undefined
      ? {}
      : { expected_head_sha: expectedHeadSHA }),
    payload_digest: requiredString(value.payload_digest),
    ...(externalID === undefined ? {} : { external_id: externalID }),
    ...(externalURL === undefined ? {} : { external_url: externalURL }),
    ...(publicErrorCode === undefined
      ? {}
      : { public_error_code: publicErrorCode }),
    attempts: nonNegativeInteger(value.attempts),
    created_at: requiredString(value.created_at),
    updated_at: requiredString(value.updated_at),
    ...(publishedAt === undefined ? {} : { published_at: publishedAt }),
  }
}

function projectActivity(value: Record<string, unknown>): PRWorkspaceActivity {
  const entityID = optionalString(value.entity_id)
  const metadata = optionalRecord(value.metadata)
  return {
    ordinal: nonNegativeInteger(value.ordinal),
    kind: requiredString(value.kind),
    actor: requiredString(value.actor),
    summary: requiredString(value.summary),
    ...(entityID === undefined ? {} : { entity_id: entityID }),
    ...(metadata === undefined ? {} : { metadata }),
    created_at: requiredString(value.created_at),
  }
}

function optionalProjectedArray<T>(
  value: unknown,
  project: (record: Record<string, unknown>) => T,
): T[] {
  if (value == null) return []
  if (!Array.isArray(value) || !value.every(isRecord)) malformed()
  return value.map(project)
}

function stringArray(value: unknown): string[] {
  if (value == null) return []
  if (
    !Array.isArray(value) ||
    !value.every((item) => typeof item === "string")
  ) {
    malformed()
  }
  return value
}

function optionalStringArray(value: unknown): string[] | undefined {
  if (value == null) return undefined
  return stringArray(value)
}

function optionalRecord(value: unknown): Record<string, unknown> | undefined {
  if (value == null) return undefined
  if (!isRecord(value)) malformed()
  return value
}

function requiredString(value: unknown): string {
  if (typeof value !== "string" || value.length === 0) malformed()
  return value
}

function stringValue(value: unknown): string {
  if (typeof value !== "string") malformed()
  return value
}

function webOrigin(value: unknown): string {
  const raw = requiredString(value)
  let parsed: URL
  try {
    parsed = new URL(raw)
  } catch {
    malformed()
  }
  if (
    (parsed.protocol !== "https:" && parsed.protocol !== "http:") ||
    parsed.username !== "" ||
    parsed.password !== "" ||
    parsed.pathname !== "/" ||
    parsed.search !== "" ||
    parsed.hash !== "" ||
    parsed.origin !== raw.replace(/\/$/u, "")
  ) {
    malformed()
  }
  return parsed.origin
}

function optionalString(value: unknown): string | undefined {
  if (value == null) return undefined
  if (typeof value !== "string") malformed()
  return value
}

function optionalHTTPSURL(value: unknown): string | undefined {
  if (value == null) return undefined
  const raw = requiredString(value)
  let parsed: URL
  try {
    parsed = new URL(raw)
  } catch {
    malformed()
  }
  if (
    parsed.protocol !== "https:" ||
    parsed.username !== "" ||
    parsed.password !== ""
  ) {
    malformed()
  }
  return raw
}

function booleanValue(value: unknown): boolean {
  if (typeof value !== "boolean") malformed()
  return value
}

function nonNegativeInteger(value: unknown): number {
  if (!Number.isSafeInteger(value) || (value as number) < 0) malformed()
  return value as number
}

function optionalNonNegativeInteger(value: unknown): number | undefined {
  if (value == null) return undefined
  return nonNegativeInteger(value)
}

function optionalInteger(value: unknown): number | undefined {
  if (value == null) return undefined
  if (!Number.isSafeInteger(value)) malformed()
  return value as number
}

function finiteNumber(value: unknown): number {
  if (typeof value !== "number" || !Number.isFinite(value)) malformed()
  return value
}

function optionalFiniteNumber(value: unknown): number | undefined {
  if (value == null) return undefined
  return finiteNumber(value)
}

function positiveInteger(value: unknown): number {
  const number = nonNegativeInteger(value)
  if (number === 0) malformed()
  return number
}

function enumValue<T extends string>(
  value: unknown,
  values: ReadonlySet<T>,
): T {
  if (typeof value !== "string" || !values.has(value as T)) malformed()
  return value as T
}

function optionalEnum<T extends string>(
  value: unknown,
  values: ReadonlySet<T>,
): T | undefined {
  if (value == null) return undefined
  return enumValue(value, values)
}

function malformed(): never {
  throw new PRWorkspaceAPIError("malformed_response", 502)
}
