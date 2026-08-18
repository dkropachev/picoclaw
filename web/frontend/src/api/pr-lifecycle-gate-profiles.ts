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

export type PRLifecycleFlowNodeKind = "action" | "gate"

export interface PRLifecycleFlowNode {
  id: string
  kind: PRLifecycleFlowNodeKind
  title: string
  description: string
  operation?: string
  decision_point?: PRLifecycleDecisionPoint
  safeguard?: string
  ordinal?: number
  editable: boolean
}

export type PRLifecycleFlowEdgeMode =
  | "linear"
  | "choice"
  | "parallel"
  | "optional"

export interface PRLifecycleFlowEdge {
  from: string
  to: string
  mode: PRLifecycleFlowEdgeMode
  outcome?: string
  label?: string
  loop: boolean
}

export interface PRLifecycleFlow {
  id: string
  title: string
  entry: string
  nodes: PRLifecycleFlowNode[]
  edges: PRLifecycleFlowEdge[]
}

export interface PRLifecycleFlowCatalog {
  schema: string
  flows: PRLifecycleFlow[]
}

export interface PRLifecycleGateProfileSnapshot {
  gate_profiles: Record<string, PRLifecycleGateProfile>
  default_gate_profile_id: string
  repository_assignments: Record<string, string>
  nudge: PRLifecycleNudgeConfig
  scope: PRLifecycleScopeConfig
  deferred_issues: PRLifecycleDeferredIssueConfig
  flow: PRLifecycleFlowCatalog
  flow_revision: string
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

export type PRLifecycleDecisionPoint = string

const prLifecycleDecisionPointPattern = /^pr(?:\.[a-z][a-z0-9_-]*){2,7}$/

const prLifecycleDecisionPointOrdinals: Readonly<Record<string, number>> = {
  "pr.charter.confirm": 1,
  "pr.charter.reconfirm": 2,
  "pr.review.start": 3,
  "pr.review.complete": 4,
  "pr.finding.classify": 5,
  "pr.implementation.eligibility": 6,
  "pr.implementation.start": 7,
  "pr.implementation.scope": 8,
  "pr.implementation.complete": 9,
  "pr.review.publish": 10,
  "pr.implementation.publish": 11,
  "pr.deferred.publish": 12,
  "pr.correction.promote": 13,
  "pr.publication.reconcile": 14,
}

const prLifecycleDecisionPointPurposes: Readonly<
  Record<string, PRLifecycleGatePurpose>
> = {
  "pr.charter.confirm": "authorization",
  "pr.charter.reconfirm": "authorization",
  "pr.review.start": "authorization",
  "pr.review.complete": "authorization",
  "pr.finding.classify": "classification",
  "pr.implementation.eligibility": "authorization",
  "pr.implementation.start": "authorization",
  "pr.implementation.scope": "authorization",
  "pr.implementation.complete": "authorization",
  "pr.review.publish": "authorization",
  "pr.implementation.publish": "authorization",
  "pr.deferred.publish": "authorization",
  "pr.correction.promote": "authorization",
  "pr.publication.reconcile": "authorization",
}

export function getPRLifecycleDecisionPointPurpose(
  decisionPoint: string,
): PRLifecycleGatePurpose | undefined {
  return prLifecycleDecisionPointPurposes[decisionPoint]
}

export function isPRLifecycleDecisionPoint(
  value: unknown,
): value is PRLifecycleDecisionPoint {
  return (
    typeof value === "string" &&
    value.length <= 128 &&
    prLifecycleDecisionPointPattern.test(value)
  )
}

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

export const prLifecycleGateProfileIDPattern = /^[a-z][a-z0-9_-]{0,63}$/

export function isPRLifecycleGateProfileID(value: string): boolean {
  return prLifecycleGateProfileIDPattern.test(value)
}

const prLifecycleGateIDPattern = /^[a-z][a-z0-9_-]{0,63}$/

export function validatePRLifecycleGateWorkflow(
  workflow: PRLifecycleGateWorkflow,
  decisionPoint: string,
  workflowPath = "workflow",
): PRLifecycleGateProfileIssue[] {
  const issues: PRLifecycleGateProfileIssue[] = []
  if (!isPRLifecycleDecisionPoint(decisionPoint)) {
    issues.push({
      path: `${workflowPath}.decision_point`,
      message: "Decision point is invalid.",
    })
  }
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
  const expectedPurpose = getPRLifecycleDecisionPointPurpose(decisionPoint)
  if (expectedPurpose && workflow.purpose !== expectedPurpose) {
    issues.push({
      path: `${workflowPath}.purpose`,
      message: `Purpose must be ${expectedPurpose} for this decision point.`,
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
    | "flow"
  >,
): PRLifecycleGateProfileIssue[] {
  const issues: PRLifecycleGateProfileIssue[] = []
  const declaredDecisionPoints = new Set<PRLifecycleDecisionPoint>()
  const ordinalByDecisionPoint = new Map<PRLifecycleDecisionPoint, number>()
  const decisionPointByOrdinal = new Map<number, PRLifecycleDecisionPoint>()
  snapshot.flow.flows.forEach((flow, flowIndex) => {
    flow.nodes.forEach((node, nodeIndex) => {
      if (node.kind !== "gate" || !node.editable) return
      if (!isPRLifecycleDecisionPoint(node.decision_point)) {
        issues.push({
          path: `flow.flows.${flowIndex}.nodes.${nodeIndex}.decision_point`,
          message: "Editable gates require a canonical decision point.",
        })
        return
      }
      declaredDecisionPoints.add(node.decision_point)
      const expectedOrdinal =
        prLifecycleDecisionPointOrdinals[node.decision_point]
      if (
        !Number.isSafeInteger(node.ordinal) ||
        node.ordinal === undefined ||
        expectedOrdinal === undefined ||
        node.ordinal !== expectedOrdinal ||
        (ordinalByDecisionPoint.has(node.decision_point) &&
          ordinalByDecisionPoint.get(node.decision_point) !== node.ordinal) ||
        (decisionPointByOrdinal.has(node.ordinal) &&
          decisionPointByOrdinal.get(node.ordinal) !== node.decision_point)
      ) {
        issues.push({
          path: `flow.flows.${flowIndex}.nodes.${nodeIndex}.ordinal`,
          message:
            "Editable gate ordinal must preserve the 1–14 catalog order.",
        })
        return
      }
      ordinalByDecisionPoint.set(node.decision_point, node.ordinal)
      decisionPointByOrdinal.set(node.ordinal, node.decision_point)
    })
  })
  if (
    decisionPointByOrdinal.size !== 14 ||
    Array.from({ length: 14 }, (_, index) => index + 1).some(
      (ordinal) => !decisionPointByOrdinal.has(ordinal),
    )
  ) {
    issues.push({
      path: "flow.flows",
      message: "Editable gate ordinals must cover the complete 1–14 catalog.",
    })
  }
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
  const names = new Set<string>()
  for (const [profileID, profile] of Object.entries(snapshot.gate_profiles)) {
    if (!isPRLifecycleGateProfileID(profileID)) {
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
      if (!declaredDecisionPoints.has(decisionPoint)) {
        issues.push({
          path: workflowPath,
          message:
            "Workflow decision point is not declared by the lifecycle flow.",
        })
      }
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
    "flow",
    "flow_revision",
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
    flow: projectFlowCatalog(root.flow),
    flow_revision: flowRevisionValue(root.flow_revision),
    catalog_revision: stringValue(root.catalog_revision),
    config_revision: stringValue(root.config_revision),
    effects: projectEffects(root.effects),
  }
  if (validatePRLifecycleGateProfiles(snapshot).length > 0) malformed()
  return snapshot
}

function projectFlowCatalog(value: unknown): PRLifecycleFlowCatalog {
  const catalog = record(value)
  onlyKeys(catalog, ["schema", "flows"])
  if (!Array.isArray(catalog.flows) || catalog.flows.length !== 2) malformed()
  const result = {
    schema: nonBlankStringValue(catalog.schema),
    flows: catalog.flows.map(projectFlow),
  }
  if (
    result.schema !== "pr-lifecycle-flow/v1" ||
    result.flows[0].id !== "review" ||
    result.flows[1].id !== "implementation"
  ) {
    malformed()
  }
  const flowIDs = new Set<string>()
  const nodeIDs = new Set<string>()
  const ordinalByDecisionPoint = new Map<PRLifecycleDecisionPoint, number>()
  const decisionPointByOrdinal = new Map<number, PRLifecycleDecisionPoint>()
  for (const flow of result.flows) {
    if (flowIDs.has(flow.id)) malformed()
    flowIDs.add(flow.id)
    for (const node of flow.nodes) {
      if (nodeIDs.has(node.id)) malformed()
      nodeIDs.add(node.id)
      if (!node.editable) continue
      const decisionPoint = node.decision_point!
      const ordinal = node.ordinal!
      if (
        (ordinalByDecisionPoint.has(decisionPoint) &&
          ordinalByDecisionPoint.get(decisionPoint) !== ordinal) ||
        (decisionPointByOrdinal.has(ordinal) &&
          decisionPointByOrdinal.get(ordinal) !== decisionPoint)
      ) {
        malformed()
      }
      ordinalByDecisionPoint.set(decisionPoint, ordinal)
      decisionPointByOrdinal.set(ordinal, decisionPoint)
    }
  }
  if (
    decisionPointByOrdinal.size !== 14 ||
    Array.from({ length: 14 }, (_, index) => index + 1).some(
      (ordinal) => !decisionPointByOrdinal.has(ordinal),
    )
  ) {
    malformed()
  }
  return result
}

function projectFlow(value: unknown): PRLifecycleFlow {
  const source = record(value)
  onlyKeys(source, ["id", "title", "entry", "nodes", "edges"])
  if (
    !Array.isArray(source.nodes) ||
    source.nodes.length === 0 ||
    source.nodes.length > 256 ||
    !Array.isArray(source.edges) ||
    source.edges.length > 1024
  ) {
    malformed()
  }
  const flow: PRLifecycleFlow = {
    id: graphIDValue(source.id),
    title: textValue(source.title, 256),
    entry: graphIDValue(source.entry),
    nodes: source.nodes.map(projectFlowNode),
    edges: source.edges.map(projectFlowEdge),
  }
  validateProjectedFlow(flow)
  return flow
}

function projectFlowNode(value: unknown): PRLifecycleFlowNode {
  const source = record(value)
  onlyKeys(source, [
    "id",
    "kind",
    "title",
    "description",
    "operation",
    "decision_point",
    "safeguard",
    "ordinal",
    "editable",
  ])
  const kind = stringValue(source.kind)
  if (kind !== "action" && kind !== "gate") malformed()
  const operation = optionalGraphID(source, "operation")
  const decisionPoint = optionalNonBlankString(source, "decision_point")
  const safeguard = optionalGraphID(source, "safeguard")
  const ordinal = optionalInteger(source, "ordinal")
  const editable = booleanValue(source.editable)
  if (
    decisionPoint !== undefined &&
    !isPRLifecycleDecisionPoint(decisionPoint)
  ) {
    malformed()
  }
  const expectedOrdinal =
    decisionPoint === undefined
      ? undefined
      : prLifecycleDecisionPointOrdinals[decisionPoint]
  if (
    (kind === "action" &&
      (editable ||
        operation === undefined ||
        decisionPoint !== undefined ||
        safeguard !== undefined ||
        ordinal !== undefined)) ||
    (kind === "gate" &&
      (operation !== undefined ||
        (editable &&
          (decisionPoint === undefined ||
            safeguard !== undefined ||
            ordinal === undefined ||
            expectedOrdinal === undefined ||
            ordinal !== expectedOrdinal)) ||
        (!editable &&
          (safeguard === undefined ||
            decisionPoint !== undefined ||
            ordinal !== undefined))))
  ) {
    malformed()
  }
  return {
    id: graphIDValue(source.id),
    kind,
    title: textValue(source.title, 256),
    description: textValue(source.description, 1024),
    ...(operation === undefined ? {} : { operation }),
    ...(decisionPoint === undefined ? {} : { decision_point: decisionPoint }),
    ...(safeguard === undefined ? {} : { safeguard }),
    ...(ordinal === undefined ? {} : { ordinal }),
    editable,
  }
}

function projectFlowEdge(value: unknown): PRLifecycleFlowEdge {
  const source = record(value)
  onlyKeys(source, ["from", "to", "mode", "outcome", "label", "loop"])
  const mode = stringValue(source.mode)
  if (
    mode !== "linear" &&
    mode !== "choice" &&
    mode !== "parallel" &&
    mode !== "optional"
  ) {
    malformed()
  }
  return {
    from: graphIDValue(source.from),
    to: graphIDValue(source.to),
    mode,
    ...optionalGraphIDProperty(source, "outcome"),
    ...optionalTextProperty(source, "label", 256),
    loop: booleanValue(source.loop),
  }
}

function validateProjectedFlow(flow: PRLifecycleFlow) {
  const nodes = new Map<string, PRLifecycleFlowNode>()
  for (const node of flow.nodes) {
    if (nodes.has(node.id)) malformed()
    nodes.set(node.id, node)
  }
  if (!nodes.has(flow.entry)) malformed()

  const outgoing = new Map<string, PRLifecycleFlowEdge[]>()
  const adjacency = new Map<string, string[]>()
  const indegree = new Map(flow.nodes.map((node) => [node.id, 0]))
  const edgeKeys = new Set<string>()
  for (const edge of flow.edges) {
    if (!nodes.has(edge.from) || !nodes.has(edge.to) || edge.from === edge.to) {
      malformed()
    }
    const key = `${edge.from}\u0000${edge.to}`
    if (edgeKeys.has(key)) malformed()
    edgeKeys.add(key)
    const sourceEdges = outgoing.get(edge.from) ?? []
    sourceEdges.push(edge)
    outgoing.set(edge.from, sourceEdges)
    if (!edge.loop) {
      adjacency.set(edge.from, [...(adjacency.get(edge.from) ?? []), edge.to])
      indegree.set(edge.to, (indegree.get(edge.to) ?? 0) + 1)
    }
  }

  for (const edges of outgoing.values()) {
    if (edges.length === 1) {
      if (
        (edges[0].mode !== "linear" && edges[0].mode !== "optional") ||
        edges[0].label !== undefined ||
        edges[0].outcome !== undefined
      ) {
        malformed()
      }
      continue
    }
    const optional = edges.filter((edge) => edge.mode === "optional")
    const primary = edges.filter((edge) => edge.mode !== "optional")
    const primaryMode = primary[0]?.mode
    if (
      (primaryMode === "linear" && primary.length !== 1) ||
      (primaryMode === "choice" && primary.length < 2) ||
      primary.some((edge) => edge.mode !== primaryMode)
    ) {
      malformed()
    }
    const outcomes = new Set<string>()
    const labels = new Set<string>()
    for (const edge of primary) {
      if (edge.mode === "linear") {
        if (
          edge.label === undefined ||
          !oneOrTwoWords(edge.label) ||
          edge.outcome !== undefined ||
          labels.has(edge.label.toLocaleLowerCase())
        ) {
          malformed()
        }
        labels.add(edge.label.toLocaleLowerCase())
        continue
      }
      if (
        edge.label === undefined ||
        !oneOrTwoWords(edge.label) ||
        (edge.mode === "choice" && edge.outcome === undefined) ||
        (edge.mode !== "choice" && edge.outcome !== undefined) ||
        (edge.outcome !== undefined && outcomes.has(edge.outcome)) ||
        labels.has(edge.label.toLocaleLowerCase())
      ) {
        malformed()
      }
      if (edge.outcome !== undefined) outcomes.add(edge.outcome)
      labels.add(edge.label.toLocaleLowerCase())
    }
    for (const edge of optional) {
      if (
        edge.outcome !== undefined ||
        edge.label === undefined ||
        !oneOrTwoWords(edge.label) ||
        labels.has(edge.label.toLocaleLowerCase())
      ) {
        malformed()
      }
      labels.add(edge.label.toLocaleLowerCase())
    }
  }

  for (const edge of flow.edges) {
    if (!edge.loop || reachesNode(edge.to, edge.from, adjacency)) continue
    malformed()
  }

  const reached = new Set<string>([flow.entry])
  const pending = [flow.entry]
  while (pending.length > 0) {
    const current = pending.shift()!
    for (const edge of outgoing.get(current) ?? []) {
      if (reached.has(edge.to)) continue
      reached.add(edge.to)
      pending.push(edge.to)
    }
  }
  if (reached.size !== flow.nodes.length) malformed()

  const roots = flow.nodes
    .filter((node) => indegree.get(node.id) === 0)
    .map((node) => node.id)
  const topological = [...roots]
  let visited = 0
  while (topological.length > 0) {
    const current = topological.shift()!
    visited++
    for (const target of adjacency.get(current) ?? []) {
      const next = (indegree.get(target) ?? 0) - 1
      indegree.set(target, next)
      if (next === 0) topological.push(target)
    }
  }
  if (visited !== flow.nodes.length) malformed()
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
  const decisionPoint = stringValue(workflow.decision_point)
  if (!isPRLifecycleDecisionPoint(decisionPoint)) malformed()
  return {
    id: stringValue(workflow.id),
    name: stringValue(workflow.name),
    purpose,
    decision_point: decisionPoint,
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

const graphIDPattern = /^[a-z][a-z0-9_.-]{0,127}$/
const disallowedTextCharacterPattern = /[\p{Cc}\p{Cf}]/u

function graphIDValue(value: unknown): string {
  const result = stringValue(value)
  if (!graphIDPattern.test(result)) malformed()
  return result
}

function nonBlankStringValue(value: unknown): string {
  const result = stringValue(value)
  if (!result.trim() || result !== result.trim()) malformed()
  return result
}

function textValue(value: unknown, maximumBytes: number): string {
  const result = nonBlankStringValue(value)
  if (
    new TextEncoder().encode(result).length > maximumBytes ||
    !hasValidUTF16(result) ||
    disallowedTextCharacterPattern.test(result)
  ) {
    malformed()
  }
  return result
}

function hasValidUTF16(value: string): boolean {
  for (let index = 0; index < value.length; index++) {
    const code = value.charCodeAt(index)
    if (code >= 0xd800 && code <= 0xdbff) {
      const next = value.charCodeAt(index + 1)
      if (next < 0xdc00 || next > 0xdfff) return false
      index++
    } else if (code >= 0xdc00 && code <= 0xdfff) {
      return false
    }
  }
  return true
}

function flowRevisionValue(value: unknown): string {
  const result = stringValue(value)
  if (!/^sha256:[a-f0-9]{64}$/.test(result)) malformed()
  return result
}

function booleanValue(value: unknown): boolean {
  if (typeof value !== "boolean") malformed()
  return value
}

function optionalNonBlankString(
  value: Record<string, unknown>,
  key: string,
): string | undefined {
  if (value[key] === undefined) return undefined
  return nonBlankStringValue(value[key])
}

function optionalGraphID(
  value: Record<string, unknown>,
  key: string,
): string | undefined {
  if (value[key] === undefined) return undefined
  return graphIDValue(value[key])
}

function optionalGraphIDProperty(
  value: Record<string, unknown>,
  key: string,
): Record<string, string> {
  const result = optionalGraphID(value, key)
  return result === undefined ? {} : { [key]: result }
}

function optionalTextProperty(
  value: Record<string, unknown>,
  key: string,
  maximumBytes: number,
): Record<string, string> {
  const result =
    value[key] === undefined ? undefined : textValue(value[key], maximumBytes)
  return result === undefined ? {} : { [key]: result }
}

function optionalInteger(
  value: Record<string, unknown>,
  key: string,
): number | undefined {
  if (value[key] === undefined) return undefined
  return integerValue(value[key])
}

function oneOrTwoWords(value: string): boolean {
  const words = value.trim().split(/\s+/)
  return words.length >= 1 && words.length <= 2
}

function reachesNode(
  from: string,
  target: string,
  adjacency: Map<string, string[]>,
): boolean {
  const seen = new Set<string>([from])
  const pending = [from]
  while (pending.length > 0) {
    const current = pending.shift()!
    if (current === target) return true
    for (const next of adjacency.get(current) ?? []) {
      if (seen.has(next)) continue
      seen.add(next)
      pending.push(next)
    }
  }
  return false
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
