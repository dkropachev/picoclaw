import {
  CollectionAPIError,
  type CollectionListRequest,
  type CollectionMutationEffects,
  type CollectionPageMetadata,
  type CollectionQueryField,
  type CollectionQueryFieldType,
  type CollectionQuerySchema,
  collectionListURL,
  collectionRequest,
} from "@/api/collection"

export interface GitWorkspaceSummary {
  id: string
  repository: string
  branch: string
  status: string
  locked: boolean
  dirty: boolean
  size: number
  ignored: number
  updated: string
}

export interface GitWorkspacesPage extends CollectionPageMetadata {
  workspaces: GitWorkspaceSummary[]
  max_total_size_bytes: number
  total_size_bytes: number
  ignored_bytes: number
  repository_count: number
  workspace_count: number
  locked_workspace_count: number
  ignored_cleanup_delay_seconds: number
  drop_delay_seconds: number
}

export interface GitWorkspaceLock {
  agent_id?: string
  locked_at: string
  heartbeat_at: string
}

export interface GitWorkspaceDetail extends GitWorkspaceSummary {
  repository_id: string
  remote_url?: string
  upstream_url?: string
  path?: string
  ref?: string
  preserved_branch?: string
  created: string
  last_work?: string
  last_cleaned?: string
  locked_by?: GitWorkspaceLock
  dropped?: string
}

export interface GitWorkspaceDetailResponse {
  workspace: GitWorkspaceDetail
}

export interface GitWorkspaceHistoryEntry {
  id: string
  action: string
  workspace?: string
  repository?: string
  agent?: string
  time: string
}

export interface GitWorkspaceHistoryPage extends CollectionPageMetadata {
  history: GitWorkspaceHistoryEntry[]
}

export interface GitWorkspaceSettingsValues {
  max_total_size_bytes: number
  ignored_cleanup_delay_seconds: number
  drop_delay_seconds: number
}

export interface GitWorkspaceSettingsResponse {
  configured: GitWorkspaceSettingsValues
  effective: GitWorkspaceSettingsValues
  config_revision: string
  effects?: CollectionMutationEffects
}

export interface GitWorkspaceCleanupResult {
  workspace: GitWorkspaceDetail
  before_ignored_bytes: number
  after_ignored_bytes: number
}

export interface GitWorkspaceReconcileResult {
  cleaned: GitWorkspaceSummary[]
  dropped: GitWorkspaceSummary[]
  stats: GitWorkspaceAggregateStats
}

export interface GitWorkspaceAggregateStats {
  max_total_size_bytes: number
  ignored_cleanup_delay_seconds: number
  drop_delay_seconds: number
  total_size_bytes: number
  ignored_bytes: number
  repository_count: number
  workspace_count: number
  locked_workspace_count: number
}

export async function listGitWorkspaces(
  input: CollectionListRequest = {},
  signal?: AbortSignal,
): Promise<GitWorkspacesPage> {
  const value = await collectionRequest<unknown>(
    collectionListURL("/api/git-workspaces", input),
    undefined,
    signal,
  )
  return projectGitWorkspacesPage(value)
}

export async function getGitWorkspace(
  workspaceID: string,
  signal?: AbortSignal,
): Promise<GitWorkspaceDetailResponse> {
  const value = await collectionRequest<unknown>(
    `/api/git-workspaces/${encodeURIComponent(workspaceID)}`,
    undefined,
    signal,
  )
  if (!isRecord(value) || !isRecord(value.workspace)) malformed()
  const response: GitWorkspaceDetailResponse = {
    workspace: projectGitWorkspaceDetail(value.workspace),
  }
  if (response.workspace?.id !== workspaceID) {
    malformed()
  }
  return response
}

export async function listGitWorkspaceHistory(
  input: CollectionListRequest = {},
  signal?: AbortSignal,
): Promise<GitWorkspaceHistoryPage> {
  const value = await collectionRequest<unknown>(
    collectionListURL("/api/git-workspaces/history", input),
    undefined,
    signal,
  )
  return projectGitWorkspaceHistoryPage(value)
}

export async function getGitWorkspaceSettings(
  signal?: AbortSignal,
): Promise<GitWorkspaceSettingsResponse> {
  const value = await collectionRequest<unknown>(
    "/api/git-workspaces/settings",
    undefined,
    signal,
  )
  return projectGitWorkspaceSettings(value)
}

export async function updateGitWorkspaceSettings(
  settings: GitWorkspaceSettingsValues,
  expectedConfigRevision: string,
): Promise<GitWorkspaceSettingsResponse> {
  const value = await collectionRequest<unknown>(
    "/api/git-workspaces/settings",
    {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        expected_config_revision: expectedConfigRevision,
        settings,
      }),
    },
  )
  return projectGitWorkspaceSettings(value)
}

export async function reconcileGitWorkspaces(): Promise<GitWorkspaceReconcileResult> {
  const value = await collectionRequest<unknown>(
    "/api/git-workspaces/reconcile",
    { method: "POST" },
  )
  if (
    !isRecord(value) ||
    !Array.isArray(value.cleaned) ||
    !Array.isArray(value.dropped)
  ) {
    malformed()
  }
  const cleaned = value.cleaned.map(projectGitWorkspaceSummary)
  const dropped = value.dropped.map(projectGitWorkspaceSummary)
  const mutationIDs = [
    ...cleaned.map((workspace) => workspace.id),
    ...dropped.map((workspace) => workspace.id),
  ]
  rejectDuplicateIDs(mutationIDs)
  return {
    cleaned,
    dropped,
    stats: projectAggregateStats(value.stats),
  }
}

export async function cleanupGitWorkspace(
  workspaceID: string,
): Promise<GitWorkspaceCleanupResult> {
  const value = await collectionRequest<unknown>(
    "/api/git-workspaces/cleanup",
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ workspace_id: workspaceID }),
    },
  )
  if (!isRecord(value) || !isRecord(value.workspace)) malformed()
  const workspace = projectGitWorkspaceDetail(value.workspace)
  if (workspace.id !== workspaceID) malformed()
  return {
    workspace,
    before_ignored_bytes: nonnegativeInteger(value.before_ignored_bytes),
    after_ignored_bytes: nonnegativeInteger(value.after_ignored_bytes),
  }
}

export async function dropGitWorkspace(
  workspaceID: string,
): Promise<{ workspace: GitWorkspaceDetail }> {
  const value = await collectionRequest<unknown>(
    `/api/git-workspaces/${encodeURIComponent(workspaceID)}`,
    { method: "DELETE" },
  )
  if (!isRecord(value) || !isRecord(value.workspace)) malformed()
  const workspace = projectGitWorkspaceDetail(value.workspace)
  if (workspace.id !== workspaceID) malformed()
  return { workspace }
}

const gitWorkspaceIDPattern = /^gw-[0-9a-f]{12}(?:-(?:[2-9]|[1-9][0-9]+))?$/
const gitRepositoryIDPattern = /^gw-[0-9a-f]{12}$/
const historyIDPattern = /^[0-9a-f]{12}$/
const gitWorkspaceStatuses = new Set(["available", "locked", "dropped"])

function projectGitWorkspacesPage(value: unknown): GitWorkspacesPage {
  if (!isRecord(value) || !Array.isArray(value.workspaces)) malformed()
  const workspaces = value.workspaces.map(projectGitWorkspaceSummary)
  rejectDuplicateIDs(workspaces.map((workspace) => workspace.id))
  return {
    ...pageMetadata(value, [{ field: "updated", direction: "DESC" }]),
    workspaces,
    max_total_size_bytes: nonnegativeInteger(value.max_total_size_bytes),
    total_size_bytes: nonnegativeInteger(value.total_size_bytes),
    ignored_bytes: nonnegativeInteger(value.ignored_bytes),
    repository_count: nonnegativeInteger(value.repository_count),
    workspace_count: nonnegativeInteger(value.workspace_count),
    locked_workspace_count: nonnegativeInteger(value.locked_workspace_count),
    ignored_cleanup_delay_seconds: nonnegativeInteger(
      value.ignored_cleanup_delay_seconds,
    ),
    drop_delay_seconds: nonnegativeInteger(value.drop_delay_seconds),
  }
}

function projectGitWorkspaceSummary(value: unknown): GitWorkspaceSummary {
  if (!isRecord(value)) malformed()
  const id = boundedString(value.id)
  if (!gitWorkspaceIDPattern.test(id)) malformed()
  const status = boundedString(value.status)
  if (!gitWorkspaceStatuses.has(status)) malformed()
  return {
    id,
    repository: boundedString(value.repository),
    branch: boundedString(value.branch, true),
    status,
    locked: booleanValue(value.locked),
    dirty: booleanValue(value.dirty),
    size: nonnegativeInteger(value.size),
    ignored: nonnegativeInteger(value.ignored),
    updated: boundedString(value.updated),
  }
}

function projectAggregateStats(value: unknown): GitWorkspaceAggregateStats {
  if (!isRecord(value)) malformed()
  return {
    max_total_size_bytes: nonnegativeInteger(value.max_total_size_bytes),
    ignored_cleanup_delay_seconds: nonnegativeInteger(
      value.ignored_cleanup_delay_seconds,
    ),
    drop_delay_seconds: nonnegativeInteger(value.drop_delay_seconds),
    total_size_bytes: nonnegativeInteger(value.total_size_bytes),
    ignored_bytes: nonnegativeInteger(value.ignored_bytes),
    repository_count: nonnegativeInteger(value.repository_count),
    workspace_count: nonnegativeInteger(value.workspace_count),
    locked_workspace_count: nonnegativeInteger(value.locked_workspace_count),
  }
}

function projectGitWorkspaceDetail(
  value: Record<string, unknown>,
): GitWorkspaceDetail {
  const summary = projectGitWorkspaceSummary(value)
  const repositoryID = boundedString(value.repository_id)
  if (!gitRepositoryIDPattern.test(repositoryID)) malformed()
  const lockedBy = isRecord(value.locked_by)
    ? {
        ...(typeof value.locked_by.agent_id === "string"
          ? { agent_id: boundedString(value.locked_by.agent_id, true) }
          : {}),
        locked_at: boundedString(value.locked_by.locked_at),
        heartbeat_at: boundedString(value.locked_by.heartbeat_at),
      }
    : undefined
  return {
    ...summary,
    repository_id: repositoryID,
    ...(typeof value.remote_url === "string"
      ? { remote_url: boundedString(value.remote_url) }
      : {}),
    ...(typeof value.upstream_url === "string"
      ? { upstream_url: boundedString(value.upstream_url, true) }
      : {}),
    ...(typeof value.path === "string"
      ? { path: boundedString(value.path) }
      : {}),
    ...(typeof value.ref === "string"
      ? { ref: boundedString(value.ref, true) }
      : {}),
    ...(typeof value.preserved_branch === "string"
      ? { preserved_branch: boundedString(value.preserved_branch, true) }
      : {}),
    created: boundedString(value.created),
    ...(typeof value.last_work === "string"
      ? { last_work: boundedString(value.last_work) }
      : {}),
    ...(typeof value.last_cleaned === "string"
      ? { last_cleaned: boundedString(value.last_cleaned) }
      : {}),
    ...(lockedBy ? { locked_by: lockedBy } : {}),
    ...(typeof value.dropped === "string"
      ? { dropped: boundedString(value.dropped) }
      : {}),
  }
}

function projectGitWorkspaceHistoryPage(
  value: unknown,
): GitWorkspaceHistoryPage {
  if (!isRecord(value) || !Array.isArray(value.history)) malformed()
  const history = value.history.map((entry): GitWorkspaceHistoryEntry => {
    if (!isRecord(entry)) malformed()
    const id = boundedString(entry.id)
    if (!historyIDPattern.test(id)) malformed()
    return {
      id,
      action: boundedString(entry.action),
      ...(typeof entry.workspace === "string"
        ? { workspace: gitWorkspaceIdentity(entry.workspace) }
        : {}),
      ...(typeof entry.repository === "string"
        ? { repository: boundedString(entry.repository, true) }
        : {}),
      ...(typeof entry.agent === "string"
        ? { agent: boundedString(entry.agent, true) }
        : {}),
      time: boundedString(entry.time),
    }
  })
  rejectDuplicateIDs(history.map((entry) => entry.id))
  return {
    ...pageMetadata(value, [{ field: "time", direction: "DESC" }]),
    history,
  }
}

function projectGitWorkspaceSettings(
  value: unknown,
): GitWorkspaceSettingsResponse {
  if (
    !isRecord(value) ||
    !isRecord(value.configured) ||
    !isRecord(value.effective)
  ) {
    malformed()
  }
  return {
    configured: projectSettingsValues(value.configured),
    effective: projectSettingsValues(value.effective),
    config_revision: boundedString(value.config_revision),
    ...(isRecord(value.effects)
      ? {
          effects: {
            launcher_effect: effectValue(value.effects.launcher_effect),
            catalog_effect: effectValue(value.effects.catalog_effect),
            gateway_effect: gatewayEffectValue(value.effects.gateway_effect),
          },
        }
      : {}),
  }
}

function projectSettingsValues(
  value: Record<string, unknown>,
): GitWorkspaceSettingsValues {
  return {
    max_total_size_bytes: nonnegativeInteger(value.max_total_size_bytes),
    ignored_cleanup_delay_seconds: nonnegativeInteger(
      value.ignored_cleanup_delay_seconds,
    ),
    drop_delay_seconds: nonnegativeInteger(value.drop_delay_seconds),
  }
}

type CollectionDefaultOrder = {
  field: string
  direction: "ASC" | "DESC"
}

function pageMetadata(
  value: Record<string, unknown>,
  expectedDefaultOrder: readonly CollectionDefaultOrder[],
): CollectionPageMetadata {
  return {
    total: nonnegativeInteger(value.total),
    canonical_query: boundedString(value.canonical_query),
    query_schema: projectQuerySchema(value.query_schema, expectedDefaultOrder),
    ...(typeof value.next_cursor === "string"
      ? { next_cursor: boundedString(value.next_cursor, true) }
      : {}),
  }
}

const maximumSchemaFields = 128
const maximumFieldNameBytes = 64
const maximumSuggestedValues = 100
const maximumSuggestedValueBytes = 256
const maximumDefaultOrderFields = 3
const collectionFieldTypes = new Set<CollectionQueryFieldType>([
  "string",
  "enum",
  "boolean",
  "number",
  "timestamp",
])
const operatorsByType: Record<CollectionQueryFieldType, ReadonlySet<string>> = {
  string: new Set(["=", "!=", "~", "!~", "IN", "NOT IN"]),
  enum: new Set(["=", "!=", "IN", "NOT IN"]),
  boolean: new Set(["=", "!=", "IN", "NOT IN"]),
  number: new Set(["=", "!=", ">", ">=", "<", "<=", "IN", "NOT IN"]),
  timestamp: new Set(["=", "!=", ">", ">=", "<", "<=", "IN", "NOT IN"]),
}

function projectQuerySchema(
  value: unknown,
  expectedDefaultOrder: readonly CollectionDefaultOrder[],
): CollectionQuerySchema {
  if (
    !isRecord(value) ||
    !Array.isArray(value.fields) ||
    value.fields.length === 0 ||
    value.fields.length > maximumSchemaFields ||
    !Array.isArray(value.default_order) ||
    value.default_order.length === 0 ||
    value.default_order.length > maximumDefaultOrderFields
  ) {
    malformed()
  }
  const fields = value.fields.map(projectQueryField)
  rejectDuplicateIDs(fields.map((field) => field.name))
  const fieldsByName = new Map(fields.map((field) => [field.name, field]))
  const defaultOrder = value.default_order.map((entry) => {
    if (
      !isRecord(entry) ||
      typeof entry.field !== "string" ||
      (entry.direction !== "ASC" && entry.direction !== "DESC")
    ) {
      malformed()
    }
    const field = fieldsByName.get(entry.field)
    if (!field?.sortable) malformed()
    return { field: entry.field, direction: entry.direction }
  })
  rejectDuplicateIDs(defaultOrder.map((entry) => entry.field))
  if (
    defaultOrder.length !== expectedDefaultOrder.length ||
    defaultOrder.some(
      (entry, index) =>
        entry.field !== expectedDefaultOrder[index]?.field ||
        entry.direction !== expectedDefaultOrder[index]?.direction,
    )
  ) {
    malformed()
  }
  return { fields }
}

function projectQueryField(value: unknown): CollectionQueryField {
  if (
    !isRecord(value) ||
    typeof value.name !== "string" ||
    new TextEncoder().encode(value.name).byteLength > maximumFieldNameBytes ||
    !/^[a-z][a-z0-9_.-]*$/.test(value.name) ||
    value.name === "all" ||
    value.name === "not" ||
    typeof value.type !== "string" ||
    !collectionFieldTypes.has(value.type as CollectionQueryFieldType) ||
    !Array.isArray(value.operators) ||
    value.operators.length === 0 ||
    value.operators.length > 10 ||
    typeof value.sortable !== "boolean"
  ) {
    malformed()
  }
  const type = value.type as CollectionQueryFieldType
  const meaningfulOperators = operatorsByType[type]
  const operators = value.operators.map((operator) => {
    if (typeof operator !== "string" || !meaningfulOperators.has(operator)) {
      malformed()
    }
    return operator
  })
  rejectDuplicateIDs(operators)
  const rawSuggestions =
    value.suggested_values === undefined ? [] : value.suggested_values
  if (
    !Array.isArray(rawSuggestions) ||
    rawSuggestions.length > maximumSuggestedValues
  ) {
    malformed()
  }
  const suggestedValues = rawSuggestions.map((suggestion) => {
    if (
      typeof suggestion !== "string" ||
      suggestion === "" ||
      suggestion.trim() !== suggestion ||
      new TextEncoder().encode(suggestion).byteLength >
        maximumSuggestedValueBytes ||
      hasUnpairedSurrogate(suggestion) ||
      hasControlCharacter(suggestion) ||
      (type === "enum" && suggestion.toLowerCase() !== suggestion)
    ) {
      malformed()
    }
    return suggestion
  })
  if (
    new Set(suggestedValues.map((suggestion) => suggestion.toLowerCase()))
      .size !== suggestedValues.length ||
    (type === "enum" && suggestedValues.length === 0)
  ) {
    malformed()
  }
  return {
    name: value.name,
    type,
    operators,
    sortable: value.sortable,
    ...(suggestedValues.length > 0
      ? { suggested_values: suggestedValues }
      : {}),
  }
}

function hasUnpairedSurrogate(value: string): boolean {
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index)
    if (code >= 0xd800 && code <= 0xdbff) {
      const next = value.charCodeAt(index + 1)
      if (next < 0xdc00 || next > 0xdfff) return true
      index += 1
    } else if (code >= 0xdc00 && code <= 0xdfff) return true
  }
  return false
}

function hasControlCharacter(value: string): boolean {
  return Array.from(value).some((character) => {
    const codePoint = character.codePointAt(0) ?? 0
    return codePoint <= 0x1f || (codePoint >= 0x7f && codePoint <= 0x9f)
  })
}

function boundedString(value: unknown, allowEmpty = false): string {
  if (
    typeof value !== "string" ||
    new TextEncoder().encode(value).byteLength > 4096 ||
    (!allowEmpty && value.trim() === "")
  ) {
    malformed()
  }
  return value
}

function gitWorkspaceIdentity(value: unknown): string {
  const id = boundedString(value)
  if (!gitWorkspaceIDPattern.test(id)) malformed()
  return id
}

function nonnegativeInteger(value: unknown): number {
  if (!Number.isSafeInteger(value) || Number(value) < 0) malformed()
  return Number(value)
}

function booleanValue(value: unknown): boolean {
  if (typeof value !== "boolean") malformed()
  return value
}

function effectValue(value: unknown): "applied" {
  if (value !== "applied") malformed()
  return value
}

function gatewayEffectValue(value: unknown): "applied" | "restart_required" {
  if (value !== "applied" && value !== "restart_required") malformed()
  return value
}

function rejectDuplicateIDs(ids: string[]): void {
  if (new Set(ids).size !== ids.length) malformed()
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value)
}

function malformed(): never {
  throw new CollectionAPIError(
    502,
    "The git workspace service returned a malformed response.",
    { code: "malformed_response" },
  )
}
