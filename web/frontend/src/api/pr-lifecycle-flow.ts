import { PRWorkspaceAPIError } from "@/api/pr-workspaces"

export type PRLifecycleDecisionPoint = string

const decisionPointPattern = /^pr(?:\.[a-z][a-z0-9_-]*){2,7}$/

export function isPRLifecycleDecisionPoint(
  value: unknown,
): value is PRLifecycleDecisionPoint {
  return (
    typeof value === "string" &&
    value.length <= 128 &&
    decisionPointPattern.test(value)
  )
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

const graphIDPattern = /^[a-z][a-z0-9_.-]{0,127}$/
const disallowedTextCharacterPattern = /[\p{Cc}\p{Cf}]/u

export function projectPRLifecycleFlowCatalog(
  value: unknown,
): PRLifecycleFlowCatalog {
  const source = record(value)
  onlyKeys(source, ["schema", "flows"])
  if (!Array.isArray(source.flows) || source.flows.length !== 2) malformed()
  const result = {
    schema: text(source.schema, 128),
    flows: source.flows.map(projectFlow),
  }
  if (
    result.schema !== "pr-lifecycle-flow/v1" ||
    result.flows[0]?.id !== "review" ||
    result.flows[1]?.id !== "implementation"
  ) {
    malformed()
  }
  const flowIDs = new Set<string>()
  const nodeIDs = new Set<string>()
  for (const flow of result.flows) {
    if (flowIDs.has(flow.id)) malformed()
    flowIDs.add(flow.id)
    for (const node of flow.nodes) {
      if (nodeIDs.has(node.id)) malformed()
      nodeIDs.add(node.id)
    }
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
    id: graphID(source.id),
    title: text(source.title, 256),
    entry: graphID(source.entry),
    nodes: source.nodes.map(projectNode),
    edges: source.edges.map(projectEdge),
  }
  validateFlow(flow)
  return flow
}

function projectNode(value: unknown): PRLifecycleFlowNode {
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
  const kind = string(source.kind)
  if (kind !== "action" && kind !== "gate") malformed()
  const editable = boolean(source.editable)
  const operation = optionalGraphID(source.operation)
  const decisionPoint = optionalString(source.decision_point)
  const safeguard = optionalGraphID(source.safeguard)
  const ordinal = optionalInteger(source.ordinal)
  if (
    decisionPoint !== undefined &&
    !isPRLifecycleDecisionPoint(decisionPoint)
  ) {
    malformed()
  }
  if (
    (kind === "action" &&
      (editable ||
        !operation ||
        decisionPoint !== undefined ||
        safeguard !== undefined ||
        ordinal !== undefined)) ||
    (kind === "gate" && operation !== undefined) ||
    (kind === "gate" &&
      editable &&
      (!decisionPoint || ordinal === undefined)) ||
    (kind === "gate" && !editable && (!safeguard || decisionPoint || ordinal))
  ) {
    malformed()
  }
  return {
    id: graphID(source.id),
    kind,
    title: text(source.title, 256),
    description: text(source.description, 1024),
    ...(operation ? { operation } : {}),
    ...(decisionPoint ? { decision_point: decisionPoint } : {}),
    ...(safeguard ? { safeguard } : {}),
    ...(ordinal === undefined ? {} : { ordinal }),
    editable,
  }
}

function projectEdge(value: unknown): PRLifecycleFlowEdge {
  const source = record(value)
  onlyKeys(source, ["from", "to", "mode", "outcome", "label", "loop"])
  const mode = string(source.mode)
  if (
    mode !== "linear" &&
    mode !== "choice" &&
    mode !== "parallel" &&
    mode !== "optional"
  ) {
    malformed()
  }
  const outcome = optionalGraphID(source.outcome)
  const label = optionalText(source.label, 128)
  if ((mode === "choice") !== (outcome !== undefined)) malformed()
  return {
    from: graphID(source.from),
    to: graphID(source.to),
    mode,
    ...(outcome ? { outcome } : {}),
    ...(label ? { label } : {}),
    loop: boolean(source.loop),
  }
}

function validateFlow(flow: PRLifecycleFlow) {
  const nodes = new Set(flow.nodes.map((node) => node.id))
  if (!nodes.has(flow.entry) || nodes.size !== flow.nodes.length) malformed()
  const incoming = new Map(flow.nodes.map((node) => [node.id, 0]))
  const adjacency = new Map(flow.nodes.map((node) => [node.id, [] as string[]]))
  const edgeKeys = new Set<string>()
  for (const edge of flow.edges) {
    if (!nodes.has(edge.from) || !nodes.has(edge.to) || edge.from === edge.to)
      malformed()
    const key = `${edge.from}\u0000${edge.to}`
    if (edgeKeys.has(key)) malformed()
    edgeKeys.add(key)
    if (edge.loop) continue
    incoming.set(edge.to, (incoming.get(edge.to) ?? 0) + 1)
    adjacency.get(edge.from)!.push(edge.to)
  }
  const queue = flow.nodes
    .filter((node) => (incoming.get(node.id) ?? 0) === 0)
    .map((node) => node.id)
  let visited = 0
  while (queue.length > 0) {
    const current = queue.shift()!
    visited += 1
    for (const target of adjacency.get(current) ?? []) {
      const next = (incoming.get(target) ?? 0) - 1
      incoming.set(target, next)
      if (next === 0) queue.push(target)
    }
  }
  if (visited !== flow.nodes.length) malformed()
}

function record(value: unknown): Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value))
    malformed()
  return value as Record<string, unknown>
}

function onlyKeys(source: Record<string, unknown>, keys: string[]) {
  const allowed = new Set(keys)
  if (Object.keys(source).some((key) => !allowed.has(key))) malformed()
}

function string(value: unknown): string {
  if (typeof value !== "string" || !value) malformed()
  return value
}

function optionalString(value: unknown): string | undefined {
  return value === undefined ? undefined : string(value)
}

function graphID(value: unknown): string {
  const result = string(value)
  if (!graphIDPattern.test(result)) malformed()
  return result
}

function optionalGraphID(value: unknown): string | undefined {
  return value === undefined ? undefined : graphID(value)
}

function text(value: unknown, maximumBytes: number): string {
  const result = string(value)
  if (
    result !== result.trim() ||
    new TextEncoder().encode(result).length > maximumBytes ||
    disallowedTextCharacterPattern.test(result)
  ) {
    malformed()
  }
  return result
}

function optionalText(
  value: unknown,
  maximumBytes: number,
): string | undefined {
  return value === undefined ? undefined : text(value, maximumBytes)
}

function boolean(value: unknown): boolean {
  if (typeof value !== "boolean") malformed()
  return value
}

function optionalInteger(value: unknown): number | undefined {
  if (value === undefined) return undefined
  if (!Number.isSafeInteger(value) || (value as number) <= 0) malformed()
  return value as number
}

function malformed(): never {
  throw new PRWorkspaceAPIError("malformed_response", 502)
}
