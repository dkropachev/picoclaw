import {
  CollectionAPIError,
  type CollectionBulkDeleteFailure,
  type CollectionQuerySchema,
  collectionListURL,
  collectionRequest,
  projectCollectionQuerySchema,
} from "@/api/collection"
import {
  DevelopmentWorkspaceAPIError as PRWorkspaceAPIError,
  requestDevelopmentJSON as requestPRWorkspaceJSON,
} from "@/api/development-workspaces"

export interface PRLifecycleWorkflowConfigurationSummary {
  name: string
  deferredIssues: { mode: "off" | "ask" | "automatic" }
}

export interface PRLifecycleRepositoryAssignmentSnapshot {
  repositories: Record<string, { name: string; defaultBranch: string }>
  workflowConfigurations: Record<
    string,
    PRLifecycleWorkflowConfigurationSummary
  >
  defaultWorkflowConfiguration: string
  repositoryAssignments: Record<string, string>
  configRevision: string
  effects: {
    gatewayEffect: "applied" | "restart-required"
    deferredPolicyEffect: "applied" | "restart-required"
  }
}

export interface PutPRLifecycleRepositoryAssignmentsInput {
  expectedConfigRevision: string
  requestID: string
  repositoryAssignments: Record<string, string>
  repositories?: Record<string, { name: string; defaultBranch: string }>
}

export interface PRLifecycleRepositoryAssignmentIssue {
  path: string
  message: string
}

export interface PRLifecycleRepositoryAssignmentSummary {
  id: string
  repository: string
  configuration: string
  default_branch: string
}

export interface PRLifecycleRepositoryAssignment extends PRLifecycleRepositoryAssignmentSummary {
  provider_origin: string
  repository_id: string
}

export interface PRLifecycleRepositoryAssignmentsCollectionResponse {
  repository_assignments: PRLifecycleRepositoryAssignmentSummary[]
  total: number
  next_cursor?: string
  canonical_query: string
  query_schema: CollectionQuerySchema
  config_revision: string
  effects: PRLifecycleCollectionEffects
}

export interface PRLifecycleRepositoryAssignmentDetailResponse {
  repository_assignment: PRLifecycleRepositoryAssignment
  workflow_configurations: Record<
    string,
    PRLifecycleWorkflowConfigurationSummary
  >
  config_revision: string
  effects: PRLifecycleCollectionEffects
}

export interface PRLifecycleCollectionEffects {
  gateway_effect: "applied" | "restart_required"
  deferred_policy_effect: "applied" | "restart_required"
}

export type PRLifecycleRepositoryAssignmentMutationResponse =
  PRLifecycleRepositoryAssignmentDetailResponse

export interface PRLifecycleRepositoryAssignmentBulkDeleteResponse {
  deleted_ids: string[]
  failures: CollectionBulkDeleteFailure[]
  config_revision: string
  effects: PRLifecycleCollectionEffects
}

export interface PRLifecycleRepositoryAssignmentInput {
  provider_origin: string
  repository_id: string
  repository: string
  configuration: string
  default_branch: string
}

const maxRepositoryIdentityBytes = 1024
const maxRepositoryAssignments = 8192

export function canonicalPRLifecycleRepositoryIdentity(
  identity: string,
): string | undefined {
  if (identity !== identity.trim()) return undefined
  const parts = identity.split("|")
  if (parts.length !== 2) return undefined
  const [providerOrigin, repositoryID] = parts
  if (
    !providerOrigin ||
    !repositoryID ||
    providerOrigin !== providerOrigin.trim() ||
    repositoryID !== repositoryID.trim() ||
    !providerOrigin.toLowerCase().startsWith("https://") ||
    /[|\0\r\n]/u.test(providerOrigin) ||
    /[|\0\r\n]/u.test(repositoryID) ||
    new TextEncoder().encode(identity).length > maxRepositoryIdentityBytes
  ) {
    return undefined
  }
  return `${providerOrigin.replace(/\/+$/u, "")}|${repositoryID}`.toLowerCase()
}

export function validatePRLifecycleRepositoryAssignments(
  snapshot: Pick<
    PRLifecycleRepositoryAssignmentSnapshot,
    "workflowConfigurations" | "repositoryAssignments"
  >,
): PRLifecycleRepositoryAssignmentIssue[] {
  const issues: PRLifecycleRepositoryAssignmentIssue[] = []
  if (
    Object.keys(snapshot.repositoryAssignments).length >
    maxRepositoryAssignments
  ) {
    issues.push({
      path: "repository-assignments",
      message: `Repository assignments cannot exceed ${maxRepositoryAssignments}.`,
    })
  }
  const canonicalIdentities = new Map<string, string>()
  for (const [repository, configurationID] of Object.entries(
    snapshot.repositoryAssignments,
  )) {
    const canonical = canonicalPRLifecycleRepositoryIdentity(repository)
    if (!canonical) {
      issues.push({
        path: `repository-assignments.${repository || "<empty>"}`,
        message:
          "Repository identity must be an exact https:// origin and repository ID separated by one |, with no surrounding whitespace and at most 1024 bytes.",
      })
    } else {
      const previous = canonicalIdentities.get(canonical)
      if (previous !== undefined) {
        issues.push({
          path: `repository-assignments.${repository}`,
          message: `Repository identity collides with ${previous} after case and trailing-origin-slash normalization.`,
        })
      } else {
        canonicalIdentities.set(canonical, repository)
      }
    }
    if (!snapshot.workflowConfigurations[configurationID]) {
      issues.push({
        path: `repository-assignments.${repository}`,
        message:
          "Repository assignment references a missing workflow configuration.",
      })
    }
  }
  return issues
}

export async function getPRLifecycleRepositoryAssignments(
  signal?: AbortSignal,
): Promise<PRLifecycleRepositoryAssignmentSnapshot> {
  return projectSnapshot(
    await requestPRWorkspaceJSON<unknown>(
      "/api/development/repositories",
      undefined,
      signal,
    ),
  )
}

const repositoryAssignmentsCollectionPath =
  "/api/development/repository-assignments"

export async function listPRLifecycleRepositoryAssignments(
  options: { query?: string; cursor?: string; limit?: number } = {},
  signal?: AbortSignal,
): Promise<PRLifecycleRepositoryAssignmentsCollectionResponse> {
  return projectCollectionResponse(
    await collectionRequest<unknown>(
      collectionListURL(repositoryAssignmentsCollectionPath, options),
      undefined,
      signal,
    ),
  )
}

export async function getPRLifecycleRepositoryAssignment(
  id: string,
  signal?: AbortSignal,
): Promise<PRLifecycleRepositoryAssignmentDetailResponse> {
  if (!isCollectionAssignmentID(id)) malformedCollection()
  const response = projectDetailResponse(
    await collectionRequest<unknown>(
      `${repositoryAssignmentsCollectionPath}/${encodeURIComponent(id)}`,
      undefined,
      signal,
    ),
  )
  if (response.repository_assignment.id !== id) malformedCollection()
  return response
}

export async function createPRLifecycleRepositoryAssignment(
  repositoryAssignment: PRLifecycleRepositoryAssignmentInput,
  expectedConfigRevision: string,
): Promise<PRLifecycleRepositoryAssignmentMutationResponse> {
  const response = projectDetailResponse(
    await collectionRequest<unknown>(repositoryAssignmentsCollectionPath, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        expected_config_revision: expectedConfigRevision,
        repository_assignment: repositoryAssignment,
      }),
    }),
  )
  if (!repositoryAssignmentMatchesInput(response, repositoryAssignment)) {
    malformedCollection()
  }
  return response
}

export async function updatePRLifecycleRepositoryAssignment(
  id: string,
  repositoryAssignment: PRLifecycleRepositoryAssignmentInput,
  expectedConfigRevision: string,
): Promise<PRLifecycleRepositoryAssignmentMutationResponse> {
  if (!isCollectionAssignmentID(id)) malformedCollection()
  const response = projectDetailResponse(
    await collectionRequest<unknown>(
      `${repositoryAssignmentsCollectionPath}/${encodeURIComponent(id)}`,
      {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          expected_config_revision: expectedConfigRevision,
          repository_assignment: repositoryAssignment,
        }),
      },
    ),
  )
  if (
    response.repository_assignment.id !== id ||
    !repositoryAssignmentMatchesInput(response, repositoryAssignment)
  ) {
    malformedCollection()
  }
  return response
}

export async function deletePRLifecycleRepositoryAssignment(
  id: string,
  expectedConfigRevision: string,
): Promise<PRLifecycleRepositoryAssignmentBulkDeleteResponse> {
  if (!isCollectionAssignmentID(id)) malformedCollection()
  const response = projectBulkDeleteResponse(
    await collectionRequest<unknown>(
      `${repositoryAssignmentsCollectionPath}/${encodeURIComponent(id)}`,
      {
        method: "DELETE",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          expected_config_revision: expectedConfigRevision,
        }),
      },
    ),
  )
  if (
    response.deleted_ids.length !== 1 ||
    response.deleted_ids[0] !== id ||
    response.failures.length !== 0
  ) {
    malformedCollection()
  }
  return response
}

export async function bulkDeletePRLifecycleRepositoryAssignments(
  ids: string[],
  expectedConfigRevision: string,
): Promise<PRLifecycleRepositoryAssignmentBulkDeleteResponse> {
  const requested = new Set(ids)
  if (
    ids.length === 0 ||
    ids.length > 200 ||
    requested.size !== ids.length ||
    ids.some((id) => !isCollectionAssignmentID(id))
  ) {
    malformedCollection()
  }
  const response = projectBulkDeleteResponse(
    await collectionRequest<unknown>(
      `${repositoryAssignmentsCollectionPath}/bulk-delete`,
      {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          expected_config_revision: expectedConfigRevision,
          ids,
        }),
      },
    ),
  )
  if (
    [
      ...response.deleted_ids,
      ...response.failures.map((failure) => failure.id),
    ].some((id) => !requested.has(id))
  ) {
    malformedCollection()
  }
  return response
}

function repositoryAssignmentMatchesInput(
  response: PRLifecycleRepositoryAssignmentDetailResponse,
  input: PRLifecycleRepositoryAssignmentInput,
): boolean {
  const assignment = response.repository_assignment
  return (
    assignment.provider_origin === input.provider_origin &&
    assignment.repository_id === input.repository_id &&
    assignment.repository === input.repository &&
    assignment.configuration === input.configuration &&
    assignment.default_branch === input.default_branch
  )
}

function projectCollectionResponse(
  value: unknown,
): PRLifecycleRepositoryAssignmentsCollectionResponse {
  const root = collectionRecord(value)
  if (!Array.isArray(root.repository_assignments)) malformedCollection()
  const repositoryAssignments = root.repository_assignments.map(
    projectRepositoryAssignmentSummary,
  )
  rejectCollectionDuplicateIDs(
    repositoryAssignments.map((assignment) => assignment.id),
  )
  return {
    repository_assignments: repositoryAssignments,
    ...projectCollectionMetadata(root),
    config_revision: collectionString(root.config_revision),
    effects: projectCollectionEffects(root.effects),
  }
}

function projectDetailResponse(
  value: unknown,
): PRLifecycleRepositoryAssignmentDetailResponse {
  const root = collectionRecord(value)
  return {
    repository_assignment: projectRepositoryAssignment(
      root.repository_assignment,
    ),
    workflow_configurations: projectMap(
      root.workflow_configurations,
      projectCollectionConfigurationSummary,
    ),
    config_revision: collectionString(root.config_revision),
    effects: projectCollectionEffects(root.effects),
  }
}

function projectBulkDeleteResponse(
  value: unknown,
): PRLifecycleRepositoryAssignmentBulkDeleteResponse {
  const root = collectionRecord(value)
  if (!Array.isArray(root.deleted_ids) || !Array.isArray(root.failures)) {
    malformedCollection()
  }
  const deletedIDs = root.deleted_ids.map((id) => collectionID(id))
  rejectCollectionDuplicateIDs(deletedIDs)
  const failures = root.failures.map((failure): CollectionBulkDeleteFailure => {
    const entry = collectionRecord(failure)
    const blockers = entry.blockers
    if (blockers !== undefined && !Array.isArray(blockers)) {
      malformedCollection()
    }
    return {
      id: collectionID(entry.id),
      code: collectionCode(entry.code),
      ...(Array.isArray(blockers)
        ? { blockers: blockers.map((blocker) => collectionString(blocker)) }
        : {}),
    }
  })
  rejectCollectionDuplicateIDs(failures.map((failure) => failure.id))
  if (failures.some((failure) => deletedIDs.includes(failure.id))) {
    malformedCollection()
  }
  return {
    deleted_ids: deletedIDs,
    failures,
    config_revision: collectionString(root.config_revision),
    effects: projectCollectionEffects(root.effects),
  }
}

function projectRepositoryAssignmentSummary(
  value: unknown,
): PRLifecycleRepositoryAssignmentSummary {
  const source = collectionRecord(value)
  return {
    id: collectionID(source.id),
    repository: collectionString(source.repository),
    configuration: collectionConfigurationID(source.configuration),
    default_branch: collectionString(source.default_branch, true),
  }
}

function projectRepositoryAssignment(
  value: unknown,
): PRLifecycleRepositoryAssignment {
  const source = collectionRecord(value)
  const summary = projectRepositoryAssignmentSummary(source)
  const providerOrigin = collectionString(source.provider_origin)
  const repositoryID = collectionString(source.repository_id)
  if (
    !canonicalPRLifecycleRepositoryIdentity(`${providerOrigin}|${repositoryID}`)
  ) {
    malformedCollection()
  }
  return {
    ...summary,
    provider_origin: providerOrigin,
    repository_id: repositoryID,
  }
}

function projectCollectionConfigurationSummary(
  value: unknown,
): PRLifecycleWorkflowConfigurationSummary {
  const source = collectionRecord(value)
  const deferredIssues = collectionRecord(source.deferred_issues)
  const mode = collectionString(deferredIssues.mode)
  if (mode !== "off" && mode !== "ask" && mode !== "automatic") {
    malformedCollection()
  }
  return {
    name: collectionString(source.name),
    deferredIssues: { mode },
  }
}

function projectCollectionEffects(
  value: unknown,
): PRLifecycleCollectionEffects {
  const source = collectionRecord(value)
  const gatewayEffect = collectionEffect(source.gateway_effect)
  const deferredPolicyEffect = collectionEffect(source.deferred_policy_effect)
  return {
    gateway_effect: gatewayEffect,
    deferred_policy_effect: deferredPolicyEffect,
  }
}

function projectCollectionMetadata(root: Record<string, unknown>) {
  const total = collectionInteger(root.total)
  const canonicalQuery = collectionCanonicalQuery(root.canonical_query)
  const schema = projectCollectionQuerySchema(root.query_schema, [
    { field: "repository", direction: "ASC" },
  ])
  return {
    total,
    canonical_query: canonicalQuery,
    query_schema: schema,
    ...(root.next_cursor === undefined || root.next_cursor === ""
      ? {}
      : { next_cursor: collectionString(root.next_cursor) }),
  }
}

function collectionRecord(value: unknown): Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    malformedCollection()
  }
  return value as Record<string, unknown>
}

function collectionString(value: unknown, allowEmpty = false): string {
  if (
    typeof value !== "string" ||
    (!allowEmpty && value.length === 0) ||
    hasUnpairedSurrogate(value) ||
    new TextEncoder().encode(value).byteLength > 4096
  ) {
    malformedCollection()
  }
  return value
}

function collectionID(value: unknown): string {
  const id = collectionString(value)
  if (!isCollectionAssignmentID(id)) malformedCollection()
  return id
}

function isCollectionAssignmentID(id: string): boolean {
  return /^[A-Za-z0-9_-]{43}$/u.test(id)
}

function collectionCanonicalQuery(value: unknown): string {
  const query = collectionString(value)
  if (/\p{Cc}/u.test(query)) malformedCollection()
  return query
}

function hasUnpairedSurrogate(value: string): boolean {
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index)
    if (code >= 0xd800 && code <= 0xdbff) {
      const next = value.charCodeAt(index + 1)
      if (next < 0xdc00 || next > 0xdfff) return true
      index += 1
    } else if (code >= 0xdc00 && code <= 0xdfff) {
      return true
    }
  }
  return false
}

function collectionConfigurationID(value: unknown): string {
  const id = collectionString(value)
  if (!/^(?=.{1,64}$)[a-z][a-z0-9]*(?:-[a-z0-9]+)*$/u.test(id)) {
    malformedCollection()
  }
  return id
}

function collectionCode(value: unknown): string {
  const code = collectionString(value)
  if (!/^[a-z0-9_.-]{1,64}$/u.test(code)) malformedCollection()
  return code
}

function collectionInteger(value: unknown): number {
  if (!Number.isSafeInteger(value) || (value as number) < 0) {
    malformedCollection()
  }
  return value as number
}

function collectionEffect(
  value: unknown,
): PRLifecycleCollectionEffects["gateway_effect"] {
  if (value !== "applied" && value !== "restart_required") {
    malformedCollection()
  }
  return value
}

function rejectCollectionDuplicateIDs(ids: string[]) {
  if (new Set(ids).size !== ids.length) malformedCollection()
}

function malformedCollection(): never {
  throw new CollectionAPIError(
    502,
    "The server returned an invalid response.",
    {
      code: "malformed_response",
    },
  )
}

export async function putPRLifecycleRepositoryAssignments(
  input: PutPRLifecycleRepositoryAssignmentsInput,
  signal?: AbortSignal,
): Promise<PRLifecycleRepositoryAssignmentSnapshot> {
  return projectSnapshot(
    await requestPRWorkspaceJSON<unknown>(
      "/api/development/repositories",
      {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          "expected-config-revision": input.expectedConfigRevision,
          "request-id": input.requestID,
          "repository-assignments": input.repositoryAssignments,
          repositories: Object.fromEntries(
            Object.entries(input.repositories ?? {}).map(
              ([identity, repository]) => [
                identity,
                {
                  name: repository.name,
                  "default-branch": repository.defaultBranch,
                },
              ],
            ),
          ),
        }),
      },
      signal,
    ),
  )
}

function projectSnapshot(
  value: unknown,
): PRLifecycleRepositoryAssignmentSnapshot {
  const root = asRecord(value)
  onlyKeys(root, [
    "workflow-configurations",
    "default-workflow-configuration",
    "repository-assignments",
    "repositories",
    "config-revision",
    "effects",
  ])
  const snapshot: PRLifecycleRepositoryAssignmentSnapshot = {
    repositories: projectMap(root.repositories ?? {}, (value) => {
      const source = asRecord(value)
      onlyKeys(source, ["name", "default-branch"])
      return {
        name: stringValue(source.name),
        defaultBranch: stringValue(source["default-branch"]),
      }
    }),
    workflowConfigurations: projectMap(
      root["workflow-configurations"],
      projectConfigurationSummary,
    ),
    defaultWorkflowConfiguration: stringValue(
      root["default-workflow-configuration"],
    ),
    repositoryAssignments: projectMap(
      root["repository-assignments"],
      stringValue,
    ),
    configRevision: stringValue(root["config-revision"]),
    effects: projectEffects(root.effects),
  }
  if (
    !snapshot.workflowConfigurations[snapshot.defaultWorkflowConfiguration] ||
    validatePRLifecycleRepositoryAssignments(snapshot).length > 0
  ) {
    malformed()
  }
  return snapshot
}

export async function resolveDevelopmentRepository(
  repositoryURL: string,
  signal?: AbortSignal,
): Promise<{
  identity: string
  name: string
  default_branch: string
  can_implement: boolean
}> {
  const value = asRecord(
    await requestPRWorkspaceJSON<unknown>(
      "/api/development-workspaces/repositories/resolve",
      {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ repository_url: repositoryURL }),
      },
      signal,
    ),
  )
  onlyKeys(value, ["identity", "name", "default_branch", "can_implement"])
  return {
    identity: stringValue(value.identity),
    name: stringValue(value.name),
    default_branch: stringValue(value.default_branch),
    can_implement: value.can_implement === true,
  }
}

function projectConfigurationSummary(
  value: unknown,
): PRLifecycleWorkflowConfigurationSummary {
  const source = asRecord(value)
  onlyKeys(source, ["name", "deferred-issues"])
  const deferredIssues = asRecord(source["deferred-issues"])
  onlyKeys(deferredIssues, ["mode"])
  const mode = stringValue(deferredIssues.mode)
  if (mode !== "off" && mode !== "ask" && mode !== "automatic") {
    malformed()
  }
  return { name: stringValue(source.name), deferredIssues: { mode } }
}

function projectEffects(
  value: unknown,
): PRLifecycleRepositoryAssignmentSnapshot["effects"] {
  const source = asRecord(value)
  onlyKeys(source, ["gateway-effect", "deferred-policy-effect"])
  const gatewayEffect = stringValue(source["gateway-effect"])
  const deferredPolicyEffect = stringValue(source["deferred-policy-effect"])
  if (gatewayEffect !== "applied" && gatewayEffect !== "restart-required") {
    malformed()
  }
  if (
    deferredPolicyEffect !== "applied" &&
    deferredPolicyEffect !== "restart-required"
  ) {
    malformed()
  }
  return { gatewayEffect, deferredPolicyEffect }
}

function projectMap<T>(
  value: unknown,
  project: (value: unknown) => T,
): Record<string, T> {
  return Object.fromEntries(
    Object.entries(asRecord(value)).map(([key, entry]) => [
      key,
      project(entry),
    ]),
  )
}

function asRecord(value: unknown): Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    malformed()
  }
  return value as Record<string, unknown>
}

function onlyKeys(source: Record<string, unknown>, keys: string[]) {
  const allowed = new Set(keys)
  if (Object.keys(source).some((key) => !allowed.has(key))) malformed()
}

function stringValue(value: unknown): string {
  if (typeof value !== "string" || value.length === 0) malformed()
  return value
}

function malformed(): never {
  throw new PRWorkspaceAPIError("malformed_response", 502)
}
