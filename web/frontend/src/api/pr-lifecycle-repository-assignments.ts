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
