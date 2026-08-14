import {
  PRWorkspaceAPIError,
  requestPRWorkspaceJSON,
} from "@/api/pr-workspaces"

export type PRLifecycleGateKind =
  | "deterministic"
  | "ai_working_context"
  | "ai_isolated_context"
  | "human"
  | "zero"
export type PRLifecycleGatePurpose =
  | "attention"
  | "authorization"
  | "classification"

export interface PRLifecycleGateStage {
  id: string
  kind: PRLifecycleGateKind
  title?: string
  when?: string
  agent_id?: string
  criteria?: string
  questions?: unknown
}

export interface PRLifecycleGateWorkflow {
  id: string
  name: string
  purpose: PRLifecycleGatePurpose
  decision_point: string
  stages: PRLifecycleGateStage[]
}

export interface PRLifecycleGateProfile {
  name: string
  workflows: Record<string, PRLifecycleGateWorkflow>
}

export interface PRLifecycleNudgeConfig {
  review_minimum_additional: number
  review_maximum_additional: number
  completion_minimum_additional: number
  completion_maximum_additional: number
}

export interface PRLifecycleSizeThreshold {
  files: number
  semantic_lines: number
  modules: number
}

export interface PRLifecycleScopeConfig {
  xs: PRLifecycleSizeThreshold
  s: PRLifecycleSizeThreshold
  m: PRLifecycleSizeThreshold
}

export type PRLifecycleDeferredIssueMode = "off" | "ask" | "automatic"

export interface PRLifecycleDeferredIssueConfig {
  mode: PRLifecycleDeferredIssueMode
}

export interface PRLifecycleGateProfileSnapshot {
  gate_profiles: Record<string, PRLifecycleGateProfile>
  default_gate_profile_id: string
  repository_assignments: Record<string, string>
  nudge: PRLifecycleNudgeConfig
  scope: PRLifecycleScopeConfig
  deferred_issues: PRLifecycleDeferredIssueConfig
  catalog_revision: string
  config_revision: string
  effects: { gateway_effect: "applied" | "restart_required" }
}

export interface PutPRLifecycleGateProfilesInput {
  expected_config_revision: string
  request_id: string
  gate_profiles: Record<string, PRLifecycleGateProfile>
  default_gate_profile_id: string
  repository_assignments: Record<string, string>
  nudge: PRLifecycleNudgeConfig
  scope: PRLifecycleScopeConfig
  deferred_issues: PRLifecycleDeferredIssueConfig
}

export const prLifecycleKnownDecisionPoints = [
  "pr.charter.confirm",
  "pr.charter.reconfirm",
  "pr.review.start",
  "pr.review.complete",
  "pr.finding.classify",
  "pr.implementation.eligibility",
  "pr.implementation.start",
  "pr.implementation.scope",
  "pr.implementation.complete",
  "pr.review.publish",
  "pr.implementation.publish",
  "pr.deferred.publish",
  "pr.correction.promote",
  "pr.publication.reconcile",
] as const

export type PRLifecycleDecisionPoint =
  (typeof prLifecycleKnownDecisionPoints)[number]

export async function getPRLifecycleGateProfiles(
  signal?: AbortSignal,
): Promise<PRLifecycleGateProfileSnapshot> {
  return projectPRLifecycleGateProfileSnapshot(
    await requestPRWorkspaceJSON<unknown>(
      "/api/pr-lifecycle/gate-profiles",
      undefined,
      signal,
    ),
  )
}

export async function putPRLifecycleGateProfiles(
  input: PutPRLifecycleGateProfilesInput,
  signal?: AbortSignal,
): Promise<PRLifecycleGateProfileSnapshot> {
  return projectPRLifecycleGateProfileSnapshot(
    await requestPRWorkspaceJSON<unknown>(
      "/api/pr-lifecycle/gate-profiles",
      {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(input),
      },
      signal,
    ),
  )
}

export function createPRLifecycleGateStage(
  kind: PRLifecycleGateKind,
  id: string,
): PRLifecycleGateStage {
  return {
    id,
    kind,
    ...(kind === "zero" ? {} : { title: "" }),
    ...(kind === "deterministic" ? { when: "true" } : {}),
    ...(kind === "human" ? { questions: ["Approve this step?"] } : {}),
  }
}

export interface PRLifecycleGateProfileIssue {
  path: string
  message: string
}

const prLifecycleGateIDPattern = /^[a-z][a-z0-9_-]{0,63}$/

export function validatePRLifecycleGateWorkflow(
  workflow: PRLifecycleGateWorkflow,
  decisionPoint: string,
  workflowPath = "workflow",
): PRLifecycleGateProfileIssue[] {
  const issues: PRLifecycleGateProfileIssue[] = []
  if (!prLifecycleGateIDPattern.test(workflow.id)) {
    issues.push({
      path: `${workflowPath}.id`,
      message: "Workflow ID is invalid.",
    })
  }
  if (!workflow.name.trim()) {
    issues.push({
      path: `${workflowPath}.name`,
      message: "Workflow name is required.",
    })
  }
  if (workflow.decision_point !== decisionPoint) {
    issues.push({
      path: `${workflowPath}.decision_point`,
      message: "Workflow decision point does not match its catalog key.",
    })
  }
  if (workflow.stages.length === 0) {
    issues.push({
      path: `${workflowPath}.stages`,
      message: "Add at least one gate stage.",
    })
  }
  const seen = new Set<string>()
  workflow.stages.forEach((stage, index) => {
    const path = `${workflowPath}.stages.${index}`
    if (!prLifecycleGateIDPattern.test(stage.id) || seen.has(stage.id)) {
      issues.push({
        path: `${path}.id`,
        message: "Stage ID must be unique.",
      })
    }
    seen.add(stage.id)
    if (stage.kind !== "zero" && !stage.title?.trim()) {
      issues.push({
        path: `${path}.title`,
        message: "Stage title is required.",
      })
    }
    if (stage.kind === "deterministic" && !stage.when?.trim()) {
      issues.push({
        path: `${path}.when`,
        message: "Deterministic condition is required.",
      })
    }
    if (
      stage.kind === "deterministic" &&
      (stage.agent_id != null ||
        stage.criteria != null ||
        stage.questions != null)
    ) {
      issues.push({
        path,
        message: "Deterministic stages can configure only title and condition.",
      })
    }
    if (
      (stage.kind === "ai_working_context" ||
        stage.kind === "ai_isolated_context") &&
      (!stage.agent_id?.trim() || !stage.criteria?.trim())
    ) {
      issues.push({
        path,
        message: "AI stages require an agent and criteria.",
      })
    }
    if (stage.kind === "human" && !hasQuestions(stage.questions)) {
      issues.push({
        path: `${path}.questions`,
        message: "Human stages require a question.",
      })
    }
    if (
      stage.kind === "human" &&
      (stage.agent_id != null || stage.criteria != null)
    ) {
      issues.push({
        path,
        message: "Human stages cannot configure an agent or AI criteria.",
      })
    }
    if (
      stage.kind === "zero" &&
      (stage.title != null ||
        stage.when != null ||
        stage.agent_id != null ||
        stage.criteria != null ||
        stage.questions != null)
    ) {
      issues.push({
        path,
        message: "Zero stages can contain only ID and kind.",
      })
    }
  })
  return issues
}

export function validatePRLifecycleGateProfiles(
  snapshot: Pick<
    PRLifecycleGateProfileSnapshot,
    | "gate_profiles"
    | "default_gate_profile_id"
    | "repository_assignments"
    | "nudge"
    | "scope"
    | "deferred_issues"
  >,
): PRLifecycleGateProfileIssue[] {
  const issues: PRLifecycleGateProfileIssue[] = []
  const profileIDs = Object.keys(snapshot.gate_profiles)
  if (profileIDs.length === 0) {
    issues.push({
      path: "gate_profiles",
      message: "Add at least one gate profile.",
    })
  }
  if (!snapshot.gate_profiles[snapshot.default_gate_profile_id]) {
    issues.push({
      path: "default_gate_profile_id",
      message: "Choose an existing default profile.",
    })
  }
  if (snapshot.gate_profiles.default?.name !== "Default") {
    issues.push({
      path: "gate_profiles.default",
      message:
        "The built-in Default profile is required and cannot be renamed.",
    })
  }
  const profileIDPattern = /^[a-z][a-z0-9_-]{0,63}$/
  const names = new Set<string>()
  for (const [profileID, profile] of Object.entries(snapshot.gate_profiles)) {
    if (!profileIDPattern.test(profileID)) {
      issues.push({
        path: `gate_profiles.${profileID}`,
        message: "Invalid profile ID.",
      })
    }
    const name = profile.name.trim()
    if (!name || name !== profile.name || name.length > 128) {
      issues.push({
        path: `gate_profiles.${profileID}.name`,
        message: "Profile name is required and cannot have surrounding space.",
      })
    }
    const foldedName = name.toLocaleLowerCase()
    if (names.has(foldedName)) {
      issues.push({
        path: `gate_profiles.${profileID}.name`,
        message: "Profile names must be unique.",
      })
    }
    names.add(foldedName)
    for (const [decisionPoint, workflow] of Object.entries(profile.workflows)) {
      const workflowPath = `gate_profiles.${profileID}.workflows.${decisionPoint}`
      issues.push(
        ...validatePRLifecycleGateWorkflow(
          workflow,
          decisionPoint,
          workflowPath,
        ),
      )
    }
  }
  for (const [repository, profileID] of Object.entries(
    snapshot.repository_assignments,
  )) {
    if (!validRepositoryIdentity(repository)) {
      issues.push({
        path: `repository_assignments.${repository}`,
        message: "Use https://provider-origin|repository-id.",
      })
    }
    if (!snapshot.gate_profiles[profileID]) {
      issues.push({
        path: `repository_assignments.${repository}`,
        message: "Assignment references a missing profile.",
      })
    }
  }
  if (
    !validNudgeBounds(
      snapshot.nudge.review_minimum_additional,
      snapshot.nudge.review_maximum_additional,
    ) ||
    !validNudgeBounds(
      snapshot.nudge.completion_minimum_additional,
      snapshot.nudge.completion_maximum_additional,
    )
  ) {
    issues.push({
      path: "nudge",
      message:
        "Each nudge minimum and maximum must be ordered between 0 and 10.",
    })
  }
  if (!validScopeConfig(snapshot.scope)) {
    issues.push({
      path: "scope",
      message:
        "Scope thresholds must be positive and monotonic from XS through M.",
    })
  }
  if (
    snapshot.deferred_issues.mode !== "off" &&
    snapshot.deferred_issues.mode !== "ask" &&
    snapshot.deferred_issues.mode !== "automatic"
  ) {
    issues.push({
      path: "deferred_issues.mode",
      message: "Choose off, ask, or automatic deferred issue handling.",
    })
  }
  return issues
}

function projectPRLifecycleGateProfileSnapshot(
  value: unknown,
): PRLifecycleGateProfileSnapshot {
  const root = record(value)
  onlyKeys(root, [
    "gate_profiles",
    "default_gate_profile_id",
    "repository_assignments",
    "nudge",
    "scope",
    "deferred_issues",
    "catalog_revision",
    "config_revision",
    "effects",
  ])
  const snapshot: PRLifecycleGateProfileSnapshot = {
    gate_profiles: projectMap(root.gate_profiles, projectProfile),
    default_gate_profile_id: stringValue(root.default_gate_profile_id),
    repository_assignments: projectMap(
      root.repository_assignments,
      stringValue,
    ),
    nudge: projectNudge(root.nudge),
    scope: projectScope(root.scope),
    deferred_issues: projectDeferredIssues(root.deferred_issues),
    catalog_revision: stringValue(root.catalog_revision),
    config_revision: stringValue(root.config_revision),
    effects: projectEffects(root.effects),
  }
  if (validatePRLifecycleGateProfiles(snapshot).length > 0) malformed()
  return snapshot
}

function projectDeferredIssues(value: unknown): PRLifecycleDeferredIssueConfig {
  const deferred = record(value)
  onlyKeys(deferred, ["mode"])
  const mode = stringValue(deferred.mode)
  if (mode !== "off" && mode !== "ask" && mode !== "automatic") malformed()
  return { mode }
}

function projectProfile(value: unknown): PRLifecycleGateProfile {
  const profile = record(value)
  onlyKeys(profile, ["name", "workflows"])
  return {
    name: stringValue(profile.name),
    workflows: projectMap(profile.workflows, projectWorkflow),
  }
}

function projectWorkflow(value: unknown): PRLifecycleGateWorkflow {
  const workflow = record(value)
  onlyKeys(workflow, ["id", "name", "purpose", "decision_point", "stages"])
  const purpose = stringValue(workflow.purpose)
  if (
    purpose !== "attention" &&
    purpose !== "authorization" &&
    purpose !== "classification"
  ) {
    malformed()
  }
  if (!Array.isArray(workflow.stages)) malformed()
  return {
    id: stringValue(workflow.id),
    name: stringValue(workflow.name),
    purpose,
    decision_point: stringValue(workflow.decision_point),
    stages: workflow.stages.map(projectStage),
  }
}

function projectStage(value: unknown): PRLifecycleGateStage {
  const stage = record(value)
  onlyKeys(stage, [
    "id",
    "title",
    "kind",
    "when",
    "criteria",
    "agent_id",
    "questions",
  ])
  const kind = stringValue(stage.kind)
  if (
    kind !== "deterministic" &&
    kind !== "ai_working_context" &&
    kind !== "ai_isolated_context" &&
    kind !== "human" &&
    kind !== "zero"
  ) {
    malformed()
  }
  return {
    id: stringValue(stage.id),
    kind,
    ...optionalStringProperty(stage, "title"),
    ...optionalStringProperty(stage, "when"),
    ...optionalStringProperty(stage, "criteria"),
    ...optionalStringProperty(stage, "agent_id"),
    ...(stage.questions === undefined
      ? {}
      : stage.questions === null
        ? malformed()
        : { questions: stage.questions }),
  }
}

function projectNudge(value: unknown): PRLifecycleNudgeConfig {
  const nudge = record(value)
  onlyKeys(nudge, [
    "review_minimum_additional",
    "review_maximum_additional",
    "completion_minimum_additional",
    "completion_maximum_additional",
  ])
  return {
    review_minimum_additional: integerValue(nudge.review_minimum_additional),
    review_maximum_additional: integerValue(nudge.review_maximum_additional),
    completion_minimum_additional: integerValue(
      nudge.completion_minimum_additional,
    ),
    completion_maximum_additional: integerValue(
      nudge.completion_maximum_additional,
    ),
  }
}

function projectScope(value: unknown): PRLifecycleScopeConfig {
  const scope = record(value)
  onlyKeys(scope, ["xs", "s", "m"])
  return {
    xs: projectThreshold(scope.xs),
    s: projectThreshold(scope.s),
    m: projectThreshold(scope.m),
  }
}

function projectThreshold(value: unknown): PRLifecycleSizeThreshold {
  const threshold = record(value)
  onlyKeys(threshold, ["files", "semantic_lines", "modules"])
  return {
    files: integerValue(threshold.files),
    semantic_lines: integerValue(threshold.semantic_lines),
    modules: integerValue(threshold.modules),
  }
}

function projectEffects(
  value: unknown,
): PRLifecycleGateProfileSnapshot["effects"] {
  const effects = record(value)
  onlyKeys(effects, ["gateway_effect"])
  const gatewayEffect = stringValue(effects.gateway_effect)
  if (gatewayEffect !== "applied" && gatewayEffect !== "restart_required") {
    malformed()
  }
  return { gateway_effect: gatewayEffect }
}

function projectMap<T>(
  value: unknown,
  project: (entry: unknown) => T,
): Record<string, T> {
  const source = record(value)
  const result: Record<string, T> = Object.create(null)
  for (const [key, entry] of Object.entries(source))
    result[key] = project(entry)
  return result
}

function record(value: unknown): Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value))
    malformed()
  return value as Record<string, unknown>
}

function onlyKeys(value: Record<string, unknown>, allowed: string[]) {
  const keys = new Set(allowed)
  if (Object.keys(value).some((key) => !keys.has(key))) malformed()
}

function stringValue(value: unknown): string {
  if (typeof value !== "string" || value.length === 0) malformed()
  return value
}

function integerValue(value: unknown): number {
  if (!Number.isSafeInteger(value)) malformed()
  return value as number
}

function optionalStringProperty(
  value: Record<string, unknown>,
  key: string,
): Record<string, string> {
  if (value[key] === undefined) return {}
  if (typeof value[key] !== "string") malformed()
  return { [key]: value[key] }
}

function hasQuestions(value: unknown): boolean {
  return Array.isArray(value) && value.length > 0
}

function validNudgeBounds(minimum: number, maximum: number): boolean {
  return (
    Number.isSafeInteger(minimum) &&
    Number.isSafeInteger(maximum) &&
    minimum >= 0 &&
    maximum >= minimum &&
    maximum <= 10
  )
}

function validScopeConfig(scope: PRLifecycleScopeConfig): boolean {
  const thresholds = [scope.xs, scope.s, scope.m]
  return (
    thresholds.every(
      (threshold) =>
        Number.isSafeInteger(threshold.files) &&
        Number.isSafeInteger(threshold.semantic_lines) &&
        Number.isSafeInteger(threshold.modules) &&
        threshold.files > 0 &&
        threshold.semantic_lines > 0 &&
        threshold.modules > 0,
    ) &&
    scope.xs.files <= scope.s.files &&
    scope.s.files <= scope.m.files &&
    scope.xs.semantic_lines <= scope.s.semantic_lines &&
    scope.s.semantic_lines <= scope.m.semantic_lines &&
    scope.xs.modules <= scope.s.modules &&
    scope.s.modules <= scope.m.modules
  )
}

function validRepositoryIdentity(value: string): boolean {
  const parts = value.split("|")
  return (
    parts.length === 2 &&
    parts[0].startsWith("https://") &&
    parts[1].length > 0 &&
    value === value.trim() &&
    value.length <= 1024
  )
}

function malformed(): never {
  throw new PRWorkspaceAPIError("malformed_pr_lifecycle_gate_profiles", 502)
}
