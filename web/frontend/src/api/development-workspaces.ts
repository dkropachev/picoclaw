import { launcherFetch } from "@/api/http"

export type DevelopmentWorkspaceIntent = "implement_feature" | "pickup_pr"
export type DevelopmentWorkspaceSourceKind = "issue" | "brief" | "pull_request"
export type DevelopmentWorkspacePhase =
  | "intake"
  | "charter"
  | "planning"
  | "review"
  | "triage"
  | "implementation"
  | "validation"
  | "completion_audit"
  | "publication"
  | "complete"

export type DevelopmentWorkspaceExecutionState =
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

export type CreateDevelopmentWorkspaceRequest =
  | {
      intent: "implement_feature"
      source: { kind: "issue"; issue_url: string }
      request_id: string
    }
  | {
      intent: "implement_feature"
      source: {
        kind: "brief"
        repository_identity: string
        content: string
      }
      request_id: string
    }
  | {
      intent: "pickup_pr"
      pull_request_url: string
      request_id: string
    }

export interface DevelopmentWorkspaceSummary {
  id: string
  intent: DevelopmentWorkspaceIntent
  source_kind: DevelopmentWorkspaceSourceKind
  repository: string
  title: string
  phase: DevelopmentWorkspacePhase
  execution_state: DevelopmentWorkspaceExecutionState
  version: number
  created_at: string
  updated_at: string
}

export type DevelopmentWorkspaceSource =
  | {
      kind: "issue"
      url: string
      number?: number
      title?: string
      body?: string
    }
  | { kind: "brief"; content: string }
  | {
      kind: "pull_request"
      url: string
      number?: number
      title?: string
      body?: string
      head_ref?: string
      base_ref?: string
    }

export type DevelopmentCharterType =
  | "fix"
  | "feature"
  | "refactor"
  | "documentation"
  | "test"

export interface DevelopmentCharter {
  id: string
  revision: number
  type: DevelopmentCharterType
  goal: string
  acceptance_criteria: string[]
  included_areas: string[]
  excluded_areas: string[]
  non_goals: string[]
  clarification_needed: boolean
  clarification_question?: string
  confirmed: boolean
}

export interface DevelopmentWorkspaceActivity {
  id?: string
  ordinal?: number
  kind: string
  summary: string
  created_at: string
}

export interface DevelopmentValidationCheck {
  id: string
  name: string
  status: string
  summary?: string
}

export interface DevelopmentGateField {
  id: string
  type: "short-text" | "long-text" | "boolean" | "select"
  label: string
  required: boolean
  min_selections: number
  max_selections: number
  options: Array<{ id: string; label: string }>
}

export interface DevelopmentGateForm {
  gate_ref: string
  prompt: string
  fields: DevelopmentGateField[]
}

export interface DevelopmentGateTurn {
  stage_id: string
  kind: string
  title: string
  status: string
  gate_form?: DevelopmentGateForm
}

export interface DevelopmentGate {
  id: string
  decision_point: string
  target_id?: string
  state: DevelopmentWorkspaceExecutionState
  turns: DevelopmentGateTurn[]
  created_at: string
  finished_at?: string
}

export interface DevelopmentPublication {
  id: string
  kind: string
  state: DevelopmentWorkspaceExecutionState
  external_url?: string
  public_error_code?: string
  updated_at: string
}

export interface DevelopmentWorkspace extends DevelopmentWorkspaceSummary {
  source: DevelopmentWorkspaceSource
  charter?: DevelopmentCharter
  base_revision?: string
  candidate_revision?: string
  head_revision?: string
  changed_files: string[]
  activity: DevelopmentWorkspaceActivity[]
  validation_checks: DevelopmentValidationCheck[]
  gates: DevelopmentGate[]
  publications: DevelopmentPublication[]
  summary?: string
}

export interface DevelopmentWorkspacePage {
  workspaces: DevelopmentWorkspaceSummary[]
  next_cursor?: string
}

export interface ConfiguredDevelopmentRepository {
  identity: string
  name: string
  default_branch: string
  can_implement: boolean
}

export interface DevelopmentRepositoryPage {
  repositories: ConfiguredDevelopmentRepository[]
}

export type DevelopmentMessageMode = "ask" | "steer"
export type DevelopmentMessageStatus =
  | "queued"
  | "applied"
  | "answered"
  | "needs_clarification"
  | "canceled"

export interface DevelopmentConversationMessage {
  id: string
  role: "user" | "assistant" | "system"
  mode?: DevelopmentMessageMode
  status: DevelopmentMessageStatus
  content: string
  created_at: string
}

export interface DevelopmentConversationPage {
  revision: number
  messages: DevelopmentConversationMessage[]
  next_cursor?: string
}

export interface DevelopmentCodeTreeEntry {
  name: string
  path: string
  type: "file" | "directory"
  size?: number
}

export interface DevelopmentCodeTree {
  revision: string
  path: string
  entries: DevelopmentCodeTreeEntry[]
  next_cursor?: string
}

export interface DevelopmentCodeBlob {
  revision: string
  path: string
  content: string
  language?: string
  truncated: boolean
}

export interface DevelopmentCodeDiff {
  base_revision: string
  candidate_revision: string
  path: string
  original?: string
  modified?: string
  language?: string
  unified_diff?: string
}

export class DevelopmentWorkspaceAPIError extends Error {
  readonly status: number
  readonly code: string

  constructor(code: string, status: number, message = code) {
    super(message)
    this.name = "DevelopmentWorkspaceAPIError"
    this.status = status
    this.code = code
  }
}

const apiRoot = "/api/development-workspaces"
const maximumResponseBytes = 8 << 20

export function createDevelopmentRequestID(): string {
  const random = globalThis.crypto?.randomUUID?.().replaceAll("-", "")
  if (!random) throw new Error("secure_random_unavailable")
  return `devq_${random}`
}

export async function listDevelopmentWorkspaces(
  params: { limit?: number; cursor?: string } = {},
  signal?: AbortSignal,
): Promise<DevelopmentWorkspacePage> {
  const query = new URLSearchParams()
  if (params.limit != null) query.set("limit", String(params.limit))
  if (params.cursor) query.set("cursor", params.cursor)
  const value = await requestJSON<unknown>(
    `${apiRoot}${query.size > 0 ? `?${query.toString()}` : ""}`,
    undefined,
    signal,
  )
  if (!isRecord(value) || !Array.isArray(value.workspaces)) malformed()
  return {
    workspaces: value.workspaces.map((workspace) =>
      projectWorkspaceSummary(workspace),
    ),
    ...(typeof value.next_cursor === "string"
      ? { next_cursor: value.next_cursor }
      : {}),
  }
}

export async function listDevelopmentRepositories(
  signal?: AbortSignal,
): Promise<DevelopmentRepositoryPage> {
  const value = await requestJSON<unknown>(
    `${apiRoot}/repositories`,
    undefined,
    signal,
  )
  if (!isRecord(value) || !Array.isArray(value.repositories)) malformed()
  return { repositories: value.repositories.map(projectRepository) }
}

export async function createDevelopmentWorkspace(
  input: CreateDevelopmentWorkspaceRequest,
  signal?: AbortSignal,
): Promise<DevelopmentWorkspace> {
  // Rebuild the discriminated body. Extra properties supplied through an unsafe
  // cast cannot make issue, brief, and pull-request inputs coexist on the wire.
  const body: CreateDevelopmentWorkspaceRequest =
    input.intent === "pickup_pr"
      ? {
          intent: "pickup_pr",
          pull_request_url: input.pull_request_url,
          request_id: input.request_id,
        }
      : input.source.kind === "issue"
        ? {
            intent: "implement_feature",
            source: { kind: "issue", issue_url: input.source.issue_url },
            request_id: input.request_id,
          }
        : {
            intent: "implement_feature",
            source: {
              kind: "brief",
              repository_identity: input.source.repository_identity,
              content: input.source.content,
            },
            request_id: input.request_id,
          }
  return projectWorkspace(
    await requestJSON<unknown>(
      apiRoot,
      {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      },
      signal,
    ),
  )
}

export async function getDevelopmentWorkspace(
  workspaceID: string,
  signal?: AbortSignal,
): Promise<DevelopmentWorkspace> {
  return projectWorkspace(
    await requestJSON<unknown>(workspacePath(workspaceID), undefined, signal),
  )
}

export async function getDevelopmentConversation(
  workspaceID: string,
  params: { cursor?: string } = {},
  signal?: AbortSignal,
): Promise<DevelopmentConversationPage> {
  const query = new URLSearchParams()
  if (params.cursor) query.set("cursor", params.cursor)
  const value = await requestJSON<unknown>(
    `${workspacePath(workspaceID)}/conversation/messages${query.size > 0 ? `?${query.toString()}` : ""}`,
    undefined,
    signal,
  )
  return projectConversation(value)
}

export async function sendDevelopmentMessage(
  workspaceID: string,
  input: {
    mode: DevelopmentMessageMode
    content: string
    expected_revision: number
    request_id: string
    candidate_revision?: string
  },
  signal?: AbortSignal,
): Promise<DevelopmentConversationPage> {
  return projectConversation(
    await requestJSON<unknown>(
      `${workspacePath(workspaceID)}/conversation/messages`,
      {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(input),
      },
      signal,
    ),
  )
}

export async function respondDevelopmentGate(
  workspaceID: string,
  gateID: string,
  input: {
    expected_version: number
    request_id: string
    field_values: Record<string, unknown>
  },
  signal?: AbortSignal,
): Promise<DevelopmentWorkspace> {
  return projectWorkspace(
    await requestJSON<unknown>(
      `${workspacePath(workspaceID)}/gates/${encodeURIComponent(gateID)}/respond`,
      {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          expected_version: input.expected_version,
          request_id: input.request_id,
          "field-values": input.field_values,
        }),
      },
      signal,
    ),
  )
}

export async function saveDevelopmentCharter(
  workspaceID: string,
  input: {
    expected_version: number
    expected_head_revision: string
    request_id: string
    charter: Pick<
      DevelopmentCharter,
      | "type"
      | "goal"
      | "acceptance_criteria"
      | "included_areas"
      | "excluded_areas"
      | "non_goals"
    >
  },
  signal?: AbortSignal,
): Promise<DevelopmentWorkspace> {
  return projectWorkspace(
    await requestJSON<unknown>(
      `${workspacePath(workspaceID)}/charter`,
      {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          expected_version: input.expected_version,
          expected_head_revision: input.expected_head_revision,
          request_id: input.request_id,
          pr_type: input.charter.type,
          goal: input.charter.goal,
          acceptance_criteria: input.charter.acceptance_criteria,
          included_areas: input.charter.included_areas,
          exclusions: input.charter.excluded_areas,
          non_goals: input.charter.non_goals,
        }),
      },
      signal,
    ),
  )
}

export async function confirmDevelopmentCharter(
  workspaceID: string,
  input: {
    expected_version: number
    expected_charter_revision: number
    request_id: string
  },
  signal?: AbortSignal,
): Promise<DevelopmentWorkspace> {
  return projectWorkspace(
    await requestJSON<unknown>(
      `${workspacePath(workspaceID)}/charter/confirm`,
      {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(input),
      },
      signal,
    ),
  )
}

export async function reconcileDevelopmentPublication(
  workspaceID: string,
  publicationID: string,
  input: {
    expected_version: number
    expected_head_revision: string
    request_id: string
  },
  signal?: AbortSignal,
): Promise<DevelopmentWorkspace> {
  return projectWorkspace(
    await requestJSON<unknown>(
      `${workspacePath(workspaceID)}/publications/${encodeURIComponent(publicationID)}/reconcile`,
      {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(input),
      },
      signal,
    ),
  )
}

export async function getDevelopmentCodeTree(
  workspaceID: string,
  input: { revision: string; path?: string; cursor?: string },
  signal?: AbortSignal,
): Promise<DevelopmentCodeTree> {
  const query = codeQuery(input)
  const value = await requestJSON<unknown>(
    `${workspacePath(workspaceID)}/code/tree?${query.toString()}`,
    undefined,
    signal,
  )
  if (!isRecord(value) || !Array.isArray(value.entries)) malformed()
  return {
    revision: stringField(value, "revision"),
    path: optionalString(value.path) ?? input.path ?? "",
    entries: value.entries.map(projectTreeEntry),
    ...(typeof value.next_cursor === "string"
      ? { next_cursor: value.next_cursor }
      : {}),
  }
}

export async function getDevelopmentCodeBlob(
  workspaceID: string,
  input: { revision: string; candidate_revision?: string; path: string },
  signal?: AbortSignal,
): Promise<DevelopmentCodeBlob> {
  const value = await requestJSON<unknown>(
    `${workspacePath(workspaceID)}/code/blob?${codeQuery(input).toString()}`,
    undefined,
    signal,
  )
  if (!isRecord(value)) malformed()
  return {
    revision: optionalString(value.revision) ?? input.revision,
    path: optionalString(value.path) ?? input.path,
    content: stringField(value, "content"),
    ...(typeof value.language === "string" ? { language: value.language } : {}),
    truncated: value.truncated === true,
  }
}

export async function getDevelopmentCodeDiff(
  workspaceID: string,
  input: { revision: string; path: string },
  signal?: AbortSignal,
): Promise<DevelopmentCodeDiff> {
  const value = await requestJSON<unknown>(
    `${workspacePath(workspaceID)}/code/diff?${codeQuery(input).toString()}`,
    undefined,
    signal,
  )
  if (!isRecord(value)) malformed()
  const original =
    optionalString(value.original) ?? optionalString(value.base_content)
  const modified =
    optionalString(value.modified) ?? optionalString(value.candidate_content)
  const unifiedDiff =
    optionalString(value.unified_diff) ?? optionalString(value.diff)
  if ((original == null || modified == null) && unifiedDiff == null) malformed()
  return {
    base_revision:
      optionalString(value.base_revision) ??
      optionalString(value.revision) ??
      "",
    candidate_revision:
      optionalString(value.candidate_revision) ??
      optionalString(value.revision) ??
      input.revision,
    path: optionalString(value.path) ?? input.path,
    ...(original != null ? { original } : {}),
    ...(modified != null ? { modified } : {}),
    ...(typeof value.language === "string" ? { language: value.language } : {}),
    ...(unifiedDiff != null ? { unified_diff: unifiedDiff } : {}),
  }
}

function codeQuery(input: {
  revision: string
  candidate_revision?: string
  path?: string
  cursor?: string
}): URLSearchParams {
  const query = new URLSearchParams({ revision: input.revision })
  if (input.candidate_revision)
    query.set("candidate_revision", input.candidate_revision)
  if (input.path) query.set("path", input.path)
  if (input.cursor) query.set("cursor", input.cursor)
  return query
}

function workspacePath(workspaceID: string): string {
  return `${apiRoot}/${encodeURIComponent(workspaceID)}`
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
    throw new DevelopmentWorkspaceAPIError("malformed_response", 502)
  }
  if (!response.ok) {
    const error = isRecord(value) ? value : undefined
    throw new DevelopmentWorkspaceAPIError(
      optionalString(error?.code) ?? "request_failed",
      response.status,
      optionalString(error?.message) ?? "Request failed.",
    )
  }
  if (!jsonContentType(response.headers.get("Content-Type"))) malformed()
  return value as T
}

export async function requestDevelopmentJSON<T>(
  path: string,
  init?: RequestInit,
  signal?: AbortSignal,
): Promise<T> {
  return requestJSON<T>(path, init, signal)
}

async function boundedResponseText(response: Response): Promise<string> {
  const declared = Number(response.headers.get("Content-Length"))
  if (Number.isFinite(declared) && declared > maximumResponseBytes) {
    throw new DevelopmentWorkspaceAPIError("response_too_large", 502)
  }
  const text = await response.text()
  if (new TextEncoder().encode(text).byteLength > maximumResponseBytes) {
    throw new DevelopmentWorkspaceAPIError("response_too_large", 502)
  }
  return text
}

function projectWorkspace(value: unknown): DevelopmentWorkspace {
  if (!isRecord(value)) malformed()
  const record = isRecord(value.workspace) ? value.workspace : value
  const providerSnapshot = isRecord(value.provider_snapshot)
    ? value.provider_snapshot
    : undefined
  const summary = projectWorkspaceSummary(record, providerSnapshot)
  const rawSource = isRecord(value.source)
    ? value.source
    : isRecord(value.source_snapshot)
      ? value.source_snapshot
      : isRecord(record.source)
        ? record.source
        : providerSnapshot
  const source = projectSource(rawSource, summary.source_kind)
  const rawActivity = Array.isArray(value.activity) ? value.activity : []
  const latestValidation = lastRecord(value.validation_runs)
  const rawChecks = Array.isArray(value.validation_checks)
    ? value.validation_checks
    : Array.isArray(latestValidation?.checks)
      ? latestValidation.checks
      : []
  const latestRepair = lastRecord(value.repair_attempts)
  const latestCharter = lastRecord(value.charters)
  const changedFiles = Array.isArray(value.changed_files)
    ? value.changed_files.filter(
        (path): path is string => typeof path === "string",
      )
    : Array.isArray(latestRepair?.changed_files)
      ? latestRepair.changed_files.filter(
          (path): path is string => typeof path === "string",
        )
      : []
  const baseRevision =
    optionalString(value.base_revision) ??
    optionalString(record.base_revision) ??
    optionalString(providerSnapshot?.base_sha)
  const candidateRevision =
    optionalString(value.candidate_revision) ??
    optionalString(record.candidate_revision) ??
    optionalString(latestRepair?.candidate_sha) ??
    optionalString(latestValidation?.candidate_sha) ??
    optionalString(providerSnapshot?.head_sha)
  const headRevision =
    optionalString(providerSnapshot?.provider_revision) ??
    optionalString(providerSnapshot?.head_sha)
  const latestStage = lastRecord(value.stage_runs)
  const rawGates = Array.isArray(value.gates) ? value.gates : []
  const rawPublications = Array.isArray(value.publications)
    ? value.publications
    : []
  return {
    ...summary,
    source,
    ...(latestCharter ? { charter: projectCharter(latestCharter) } : {}),
    ...(baseRevision ? { base_revision: baseRevision } : {}),
    ...(candidateRevision ? { candidate_revision: candidateRevision } : {}),
    ...(headRevision ? { head_revision: headRevision } : {}),
    changed_files: changedFiles,
    activity: rawActivity.map(projectActivity),
    validation_checks: rawChecks.map(projectValidationCheck),
    gates: rawGates.map(projectGate),
    publications: rawPublications.map(projectPublication),
    ...(typeof value.summary === "string"
      ? { summary: value.summary }
      : typeof latestStage?.summary === "string"
        ? { summary: latestStage.summary }
        : {}),
  }
}

function projectCharter(value: Record<string, unknown>): DevelopmentCharter {
  const stringList = (field: string): string[] => {
    const raw = value[field]
    if (!Array.isArray(raw) || raw.some((item) => typeof item !== "string")) {
      malformed()
    }
    return raw as string[]
  }
  return {
    id: stringField(value, "id"),
    revision: numberField(value, "revision"),
    type: enumField(value, "type", charterTypes),
    goal: stringField(value, "goal"),
    acceptance_criteria: stringList("acceptance_criteria"),
    included_areas: stringList("included_areas"),
    excluded_areas: stringList("excluded_areas"),
    non_goals: stringList("non_goals"),
    clarification_needed: value.clarification_needed === true,
    ...(typeof value.clarification_question === "string"
      ? { clarification_question: value.clarification_question }
      : {}),
    confirmed: value.confirmed === true,
  }
}

function projectWorkspaceSummary(
  value: unknown,
  details?: Record<string, unknown>,
): DevelopmentWorkspaceSummary {
  if (!isRecord(value)) malformed()
  const sourceKind =
    typeof value.source_kind === "string"
      ? enumField(value, "source_kind", sourceKinds)
      : "pull_request"
  const intent =
    typeof value.intent === "string"
      ? enumField(value, "intent", intents)
      : sourceKind === "pull_request"
        ? "pickup_pr"
        : "implement_feature"
  if (
    (intent === "pickup_pr") !== (sourceKind === "pull_request") ||
    (intent === "implement_feature" && sourceKind === "pull_request")
  ) {
    malformed()
  }
  const repository = stringField(value, "repository")
  const sourceNumber =
    typeof value.source_number === "number"
      ? value.source_number
      : typeof value.pull_number === "number"
        ? value.pull_number
        : undefined
  const title =
    optionalString(value.title) ??
    optionalString(details?.title) ??
    (sourceKind === "brief"
      ? "Feature brief"
      : sourceKind === "issue" && sourceNumber
        ? `Issue #${sourceNumber}`
        : sourceKind === "pull_request" && sourceNumber
          ? `Pull request #${sourceNumber}`
          : repository)
  return {
    id: stringField(value, "id"),
    intent,
    source_kind: sourceKind,
    repository,
    title,
    phase: enumField(value, "phase", phases),
    execution_state: enumField(value, "execution_state", executionStates),
    version: numberField(value, "version"),
    created_at: stringField(value, "created_at"),
    updated_at: stringField(value, "updated_at"),
  }
}

function projectSource(
  value: Record<string, unknown> | undefined,
  kind: DevelopmentWorkspaceSourceKind,
): DevelopmentWorkspaceSource {
  if (!value) {
    return kind === "brief" ? { kind, content: "" } : { kind, url: "" }
  }
  const projectedKind =
    optionalString(value.kind) ?? optionalString(value.source_kind)
  if (projectedKind && projectedKind !== kind) malformed()
  if (kind === "brief") {
    return {
      kind,
      content:
        optionalString(value.content) ?? optionalString(value.body) ?? "",
    }
  }
  return {
    kind,
    url: optionalString(value.url) ?? optionalString(value.source_url) ?? "",
    ...(typeof value.number === "number"
      ? { number: value.number }
      : typeof value.source_number === "number"
        ? { number: value.source_number }
        : {}),
    ...(typeof value.title === "string" ? { title: value.title } : {}),
    ...(typeof value.body === "string" ? { body: value.body } : {}),
    ...(kind === "pull_request" && typeof value.head_ref === "string"
      ? { head_ref: value.head_ref }
      : {}),
    ...(kind === "pull_request" && typeof value.base_ref === "string"
      ? { base_ref: value.base_ref }
      : {}),
  }
}

function projectConversation(value: unknown): DevelopmentConversationPage {
  if (!isRecord(value) || !Array.isArray(value.messages)) malformed()
  return {
    revision: numberField(value, "revision"),
    messages: value.messages.map(projectMessage),
    ...(typeof value.next_cursor === "string"
      ? { next_cursor: value.next_cursor }
      : {}),
  }
}

function projectMessage(value: unknown): DevelopmentConversationMessage {
  if (!isRecord(value)) malformed()
  return {
    id: stringField(value, "id"),
    role: enumField(value, "role", messageRoles),
    ...(typeof value.mode === "string" &&
    messageModes.has(value.mode as DevelopmentMessageMode)
      ? { mode: value.mode as DevelopmentMessageMode }
      : {}),
    status: enumField(value, "status", messageStatuses),
    content: stringField(value, "content"),
    created_at: stringField(value, "created_at"),
  }
}

function projectRepository(value: unknown): ConfiguredDevelopmentRepository {
  if (!isRecord(value)) malformed()
  return {
    identity: stringField(value, "identity"),
    name: stringField(value, "name"),
    default_branch: optionalString(value.default_branch) ?? "",
    can_implement: value.can_implement === true,
  }
}

function projectTreeEntry(value: unknown): DevelopmentCodeTreeEntry {
  if (!isRecord(value)) malformed()
  const path = stringField(value, "path")
  return {
    name: optionalString(value.name) ?? path,
    path,
    type: enumField(value, "type", treeEntryTypes),
    ...(typeof value.size === "number" ? { size: value.size } : {}),
  }
}

function projectActivity(value: unknown): DevelopmentWorkspaceActivity {
  if (!isRecord(value)) malformed()
  return {
    ...(typeof value.id === "string" ? { id: value.id } : {}),
    ...(typeof value.ordinal === "number" ? { ordinal: value.ordinal } : {}),
    kind: stringField(value, "kind"),
    summary: stringField(value, "summary"),
    created_at: stringField(value, "created_at"),
  }
}

function projectValidationCheck(value: unknown): DevelopmentValidationCheck {
  if (!isRecord(value)) malformed()
  return {
    id: stringField(value, "id"),
    name: stringField(value, "name"),
    status: stringField(value, "status"),
    ...(typeof value.summary === "string" ? { summary: value.summary } : {}),
  }
}

function projectGate(value: unknown): DevelopmentGate {
  if (!isRecord(value) || !Array.isArray(value.turns)) malformed()
  return {
    id: stringField(value, "id"),
    decision_point: stringField(value, "decision_point"),
    ...(typeof value.target_id === "string"
      ? { target_id: value.target_id }
      : {}),
    state: enumField(value, "state", executionStates),
    turns: value.turns.map(projectGateTurn),
    created_at: stringField(value, "created_at"),
    ...(typeof value.finished_at === "string"
      ? { finished_at: value.finished_at }
      : {}),
  }
}

function projectGateTurn(value: unknown): DevelopmentGateTurn {
  if (!isRecord(value)) malformed()
  const rawForm = isRecord(value["gate-form"])
    ? value["gate-form"]
    : isRecord(value.gate_form)
      ? value.gate_form
      : undefined
  return {
    stage_id: stringField(value, "stage_id"),
    kind: stringField(value, "kind"),
    title: stringField(value, "title"),
    status: stringField(value, "status"),
    ...(rawForm ? { gate_form: projectGateForm(rawForm) } : {}),
  }
}

function projectGateForm(value: Record<string, unknown>): DevelopmentGateForm {
  const rawFields = Array.isArray(value.fields) ? value.fields : []
  return {
    gate_ref:
      optionalString(value["gate-ref"]) ?? optionalString(value.gate_ref) ?? "",
    prompt: stringField(value, "prompt"),
    fields: rawFields.map(projectGateField),
  }
}

function projectGateField(value: unknown): DevelopmentGateField {
  if (!isRecord(value)) malformed()
  const type = enumField(value, "type", gateFieldTypes)
  const rawOptions = Array.isArray(value.options) ? value.options : []
  return {
    id: stringField(value, "id"),
    type,
    label: stringField(value, "label"),
    required: value.required === true,
    min_selections:
      typeof value["min-selections"] === "number"
        ? value["min-selections"]
        : typeof value.min_selections === "number"
          ? value.min_selections
          : 0,
    max_selections:
      typeof value["max-selections"] === "number"
        ? value["max-selections"]
        : typeof value.max_selections === "number"
          ? value.max_selections
          : type === "select"
            ? 1
            : 0,
    options: rawOptions.map((option) => {
      if (!isRecord(option)) malformed()
      return {
        id: stringField(option, "id"),
        label: stringField(option, "label"),
      }
    }),
  }
}

function projectPublication(value: unknown): DevelopmentPublication {
  if (!isRecord(value)) malformed()
  return {
    id: stringField(value, "id"),
    kind: stringField(value, "kind"),
    state: enumField(value, "state", executionStates),
    ...(typeof value.external_url === "string"
      ? { external_url: value.external_url }
      : {}),
    ...(typeof value.public_error_code === "string"
      ? { public_error_code: value.public_error_code }
      : {}),
    updated_at: stringField(value, "updated_at"),
  }
}

function stringField(value: Record<string, unknown>, key: string): string {
  const field = value[key]
  if (typeof field !== "string") malformed()
  return field
}

function numberField(value: Record<string, unknown>, key: string): number {
  const field = value[key]
  if (typeof field !== "number" || !Number.isFinite(field)) malformed()
  return field
}

function enumField<T extends string>(
  value: Record<string, unknown>,
  key: string,
  allowed: ReadonlySet<T>,
): T {
  const field = value[key]
  if (typeof field !== "string" || !allowed.has(field as T)) malformed()
  return field as T
}

function optionalString(value: unknown): string | undefined {
  return typeof value === "string" ? value : undefined
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value)
}

function lastRecord(value: unknown): Record<string, unknown> | undefined {
  if (!Array.isArray(value)) return undefined
  for (let index = value.length - 1; index >= 0; index--) {
    if (isRecord(value[index])) return value[index]
  }
  return undefined
}

function jsonContentType(contentType: string | null): boolean {
  return (
    contentType?.split(";", 1)[0]?.trim().toLowerCase() === "application/json"
  )
}

function malformed(): never {
  throw new DevelopmentWorkspaceAPIError("malformed_response", 502)
}

const intents = new Set<DevelopmentWorkspaceIntent>([
  "implement_feature",
  "pickup_pr",
])
const sourceKinds = new Set<DevelopmentWorkspaceSourceKind>([
  "issue",
  "brief",
  "pull_request",
])
const charterTypes = new Set<DevelopmentCharterType>([
  "fix",
  "feature",
  "refactor",
  "documentation",
  "test",
])
const phases = new Set<DevelopmentWorkspacePhase>([
  "intake",
  "charter",
  "planning",
  "review",
  "triage",
  "implementation",
  "validation",
  "completion_audit",
  "publication",
  "complete",
])
const executionStates = new Set<DevelopmentWorkspaceExecutionState>([
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
const messageModes = new Set<DevelopmentMessageMode>(["ask", "steer"])
const messageRoles = new Set<DevelopmentConversationMessage["role"]>([
  "user",
  "assistant",
  "system",
])
const messageStatuses = new Set<DevelopmentMessageStatus>([
  "queued",
  "applied",
  "answered",
  "needs_clarification",
  "canceled",
])
const treeEntryTypes = new Set<DevelopmentCodeTreeEntry["type"]>([
  "file",
  "directory",
])
const gateFieldTypes = new Set<DevelopmentGateField["type"]>([
  "short-text",
  "long-text",
  "boolean",
  "select",
])
