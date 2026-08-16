import { type KeyboardEvent, useEffect, useId, useMemo, useState } from "react"

import {
  type PRLifecycleDecisionPoint,
  type PRLifecycleFlow,
  type PRLifecycleFlowCatalog,
  type PRLifecycleFlowEdge,
  type PRLifecycleFlowNode,
  type PRLifecycleGateKind,
  type PRLifecycleGateProfile,
  type PRLifecycleGateWorkflow,
  validatePRLifecycleGateWorkflow,
} from "@/api/pr-lifecycle-gate-profiles"
import { cn } from "@/lib/utils"

interface PRLifecycleGateMapProps {
  flow: PRLifecycleFlowCatalog
  flowRevision: string
  selectedDecisionPoint?: PRLifecycleDecisionPoint
  workflows?: PRLifecycleGateProfile["workflows"]
  profileID?: string
  profileName?: string
  onSelect: (decisionPoint: PRLifecycleDecisionPoint) => void
  className?: string
}

type GateFormat =
  | "automatic"
  | "deterministic"
  | "ai"
  | "user"
  | "mixed"
  | "needs-setup"
type GateStageCategory = Exclude<GateFormat, "mixed" | "needs-setup">

interface GateFormatSummary {
  format: GateFormat
  label: string
  composition?: string
  fallback: boolean
  accessible: string
}

interface FlowLayout {
  ranks: PRLifecycleFlowNode[][]
  nodeByID: Map<string, PRLifecycleFlowNode>
  incoming: Map<string, PRLifecycleFlowEdge[]>
  outgoing: Map<string, PRLifecycleFlowEdge[]>
}

const stageCategory: Record<PRLifecycleGateKind, GateStageCategory> = {
  zero: "automatic",
  deterministic: "deterministic",
  ai_working_context: "ai",
  ai_isolated_context: "ai",
  human: "user",
}

const gateFormatLabels: Record<GateStageCategory, string> = {
  automatic: "Automatic",
  deterministic: "Deterministic",
  ai: "AI",
  user: "User",
}

export function PRLifecycleGateMap({
  flow,
  flowRevision,
  selectedDecisionPoint,
  workflows,
  profileID,
  profileName,
  onSelect,
  className,
}: PRLifecycleGateMapProps) {
  const instanceID = useId().replaceAll(":", "")
  const titleID = `${instanceID}-title`
  const descriptionID = `${instanceID}-description`
  const initialFlow =
    findDecisionPointFlow(flow, selectedDecisionPoint)?.id ?? flow.flows[0].id
  const [activeFlowID, setActiveFlowID] = useState(initialFlow)
  const [selectedNodeID, setSelectedNodeID] = useState<string | undefined>(
    () =>
      flow.flows
        .find((candidate) => candidate.id === initialFlow)
        ?.nodes.find(
          (node) =>
            node.editable && node.decision_point === selectedDecisionPoint,
        )?.id,
  )

  useEffect(() => {
    setActiveFlowID((current) => {
      const currentFlow = flow.flows.find(
        (candidate) => candidate.id === current,
      )
      if (
        selectedDecisionPoint &&
        currentFlow &&
        flowContainsDecisionPoint(currentFlow, selectedDecisionPoint)
      ) {
        return current
      }
      return (
        findDecisionPointFlow(flow, selectedDecisionPoint)?.id ??
        currentFlow?.id ??
        flow.flows[0].id
      )
    })
  }, [flow, selectedDecisionPoint])

  const activeFlow =
    flow.flows.find((candidate) => candidate.id === activeFlowID) ??
    flow.flows[0]

  useEffect(() => {
    if (!selectedDecisionPoint) {
      setSelectedNodeID(undefined)
      return
    }
    setSelectedNodeID((current) => {
      const currentNode = activeFlow.nodes.find((node) => node.id === current)
      if (
        currentNode?.editable &&
        currentNode.decision_point === selectedDecisionPoint
      ) {
        return current
      }
      return activeFlow.nodes.find(
        (node) =>
          node.editable && node.decision_point === selectedDecisionPoint,
      )?.id
    })
  }, [activeFlow, selectedDecisionPoint])

  return (
    <section
      aria-labelledby={titleID}
      className={cn(
        "bg-card @container/flow min-w-0 overflow-hidden rounded-xl border",
        className,
      )}
      data-flow-revision={flowRevision}
      data-flow-schema={flow.schema}
    >
      <div className="flex flex-col gap-3 px-4 py-4 lg:flex-row lg:items-start lg:justify-between">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <h2 id={titleID} className="text-sm font-semibold">
              PR lifecycle gate flow
            </h2>
            {profileName ? (
              <span className="bg-muted/50 text-muted-foreground min-w-0 rounded-md border px-2 py-0.5 text-xs [overflow-wrap:anywhere]">
                Profile · {profileName}
              </span>
            ) : null}
          </div>
          <p
            id={descriptionID}
            className="text-muted-foreground mt-1 max-w-3xl text-xs"
          >
            Choose a workflow, follow its actions and branches, and select an
            editable gate to change its policy.
          </p>
        </div>
        <div className="max-w-3xl min-w-0 space-y-1.5">
          <div
            aria-label="Diagram legend"
            className="text-muted-foreground flex flex-wrap gap-1.5 text-xs"
          >
            <Legend label="Action" variant="action" />
            <Legend label="Editable gate" variant="gate" />
            <Legend label="Locked safeguard" variant="required" />
          </div>
          <GateFormatLegend />
        </div>
      </div>

      <div
        className="bg-muted/10 w-full overflow-hidden border-t"
        data-gate-map-viewport
      >
        <div
          aria-describedby={descriptionID}
          className="w-full min-w-0 space-y-4 p-4"
          data-gate-map-content
          role="group"
        >
          <FlowTabs
            activeFlowID={activeFlow.id}
            flows={flow.flows}
            instanceID={instanceID}
            onChange={setActiveFlowID}
          />

          {flow.flows.map((candidate, index) => {
            const active = candidate.id === activeFlow.id
            return (
              <div
                aria-labelledby={`${instanceID}-flow-${index}-tab`}
                data-flow-view={candidate.id}
                hidden={!active}
                id={`${instanceID}-flow-${index}-panel`}
                key={candidate.id}
                role="tabpanel"
                tabIndex={active ? 0 : undefined}
              >
                {active ? (
                  <FlowGraph
                    flow={candidate}
                    instanceID={instanceID}
                    onSelect={(node) => {
                      setSelectedNodeID(node.id)
                      onSelect(node.decision_point!)
                    }}
                    profileID={profileID}
                    selectedNodeID={selectedNodeID}
                    workflows={workflows}
                  />
                ) : null}
              </div>
            )
          })}
        </div>
      </div>
    </section>
  )
}

function FlowTabs({
  activeFlowID,
  flows,
  instanceID,
  onChange,
}: {
  activeFlowID: string
  flows: PRLifecycleFlow[]
  instanceID: string
  onChange: (flowID: string) => void
}) {
  const selectAndFocus = (index: number) => {
    const flow = flows[index]
    onChange(flow.id)
    window.requestAnimationFrame(() => {
      document
        .getElementById(`${instanceID}-flow-${index}-tab`)
        ?.focus({ preventScroll: true })
    })
  }
  const handleKeyDown = (
    event: KeyboardEvent<HTMLButtonElement>,
    index: number,
  ) => {
    let nextIndex: number | undefined
    if (event.key === "ArrowLeft" || event.key === "ArrowUp") {
      nextIndex = (index - 1 + flows.length) % flows.length
    } else if (event.key === "ArrowRight" || event.key === "ArrowDown") {
      nextIndex = (index + 1) % flows.length
    } else if (event.key === "Home") {
      nextIndex = 0
    } else if (event.key === "End") {
      nextIndex = flows.length - 1
    }
    if (nextIndex === undefined) return
    event.preventDefault()
    selectAndFocus(nextIndex)
  }

  return (
    <div
      aria-label="PR workflow view"
      className={responsiveGridClass(flows.length)}
      role="tablist"
    >
      {flows.map((flow, index) => {
        const selected = flow.id === activeFlowID
        const actionCount = flow.nodes.filter(
          (node) => node.kind === "action",
        ).length
        const editableGateCount = flow.nodes.filter(
          (node) => node.kind === "gate" && node.editable,
        ).length
        const safeguardCount = flow.nodes.filter(
          (node) => node.kind === "gate" && !node.editable,
        ).length
        return (
          <button
            aria-controls={`${instanceID}-flow-${index}-panel`}
            aria-selected={selected}
            className={cn(
              "focus-visible:ring-ring flex min-w-0 flex-col rounded-lg border px-3 py-2 text-left transition-colors outline-none focus-visible:ring-2",
              selected
                ? "border-primary bg-primary/10"
                : "bg-background hover:bg-muted/50",
            )}
            data-flow-view-tab={flow.id}
            id={`${instanceID}-flow-${index}-tab`}
            key={flow.id}
            onClick={() => onChange(flow.id)}
            onKeyDown={(event) => handleKeyDown(event, index)}
            role="tab"
            tabIndex={selected ? 0 : -1}
            type="button"
          >
            <strong className="text-xs">{flow.title}</strong>
            <span className="text-muted-foreground mt-1 text-[11px] leading-snug">
              {actionCount} actions · {editableGateCount} editable gate
              {editableGateCount === 1 ? " placement" : " placements"}
              {safeguardCount > 0 ? ` · ${safeguardCount} safeguards` : ""}
            </span>
          </button>
        )
      })}
    </div>
  )
}

function FlowGraph({
  flow,
  instanceID,
  onSelect,
  profileID,
  selectedNodeID,
  workflows,
}: {
  flow: PRLifecycleFlow
  instanceID: string
  onSelect: (node: PRLifecycleFlowNode) => void
  profileID?: string
  selectedNodeID?: string
  workflows?: PRLifecycleGateProfile["workflows"]
}) {
  const layout = useMemo(() => createFlowLayout(flow), [flow])

  return (
    <section
      aria-label={`${flow.title} graph`}
      className="bg-background/60 @container/flow min-w-0 rounded-xl border p-3"
      data-flow-graph={flow.id}
    >
      <div className="mx-auto w-full max-w-7xl min-w-0 space-y-1">
        {layout.ranks.map((nodes, rank) => (
          <div
            className={responsiveGridClass(nodes.length)}
            data-flow-rank={rank}
            key={rank}
          >
            {nodes.map((node) => (
              <FlowNodeCell
                flowNode={node}
                instanceID={instanceID}
                key={node.id}
                layout={layout}
                onSelect={onSelect}
                profileID={profileID}
                selectedNodeID={selectedNodeID}
                workflows={workflows}
              />
            ))}
          </div>
        ))}
      </div>
    </section>
  )
}

function FlowNodeCell({
  flowNode,
  instanceID,
  layout,
  onSelect,
  profileID,
  selectedNodeID,
  workflows,
}: {
  flowNode: PRLifecycleFlowNode
  instanceID: string
  layout: FlowLayout
  onSelect: (node: PRLifecycleFlowNode) => void
  profileID?: string
  selectedNodeID?: string
  workflows?: PRLifecycleGateProfile["workflows"]
}) {
  const incoming = layout.incoming.get(flowNode.id) ?? []
  const outgoing = layout.outgoing.get(flowNode.id) ?? []
  const singletonLoop =
    outgoing.length === 1 && outgoing[0].loop ? outgoing[0] : undefined
  return (
    <div
      className="flex min-w-0 flex-col"
      data-flow-cell={flowNode.id}
      data-flow-incoming-count={incoming.length}
    >
      {incoming.length > 0 ? (
        <TargetConnector
          edges={incoming}
          nodeByID={layout.nodeByID}
          target={flowNode}
        />
      ) : null}
      <GraphNode
        flowNode={flowNode}
        instanceID={instanceID}
        onSelect={() => onSelect(flowNode)}
        profileID={profileID}
        selected={flowNode.id === selectedNodeID && flowNode.editable}
        workflow={
          flowNode.decision_point
            ? workflows?.[flowNode.decision_point]
            : undefined
        }
      />
      {outgoing.length > 1 ? (
        <BranchLaunches
          edges={outgoing}
          nodeByID={layout.nodeByID}
          source={flowNode}
        />
      ) : null}
      {singletonLoop ? (
        <LoopConnector
          edge={singletonLoop}
          source={flowNode}
          target={layout.nodeByID.get(singletonLoop.to)!}
        />
      ) : null}
    </div>
  )
}

function BranchLaunches({
  edges,
  nodeByID,
  source,
}: {
  edges: PRLifecycleFlowEdge[]
  nodeByID: Map<string, PRLifecycleFlowNode>
  source: PRLifecycleFlowNode
}) {
  const modes = [
    ...new Set(edges.map((edge) => edge.mode)),
  ] as PRLifecycleFlowEdge["mode"][]
  const routeComposition = modes.join("+")
  return (
    <div
      aria-label={`${source.title} branches`}
      className="border-primary/20 bg-muted/10 mt-2 min-w-0 space-y-2 rounded-lg border p-2"
      data-flow-launches={source.id}
      role="group"
    >
      {modes.map((mode) => (
        <div
          className="min-w-0 space-y-1"
          data-flow-route-composition={routeComposition}
          data-flow-route-group={source.id}
          data-flow-route-mode={mode}
          key={mode}
        >
          <span
            className="text-muted-foreground block text-[10px] font-bold tracking-wider uppercase"
            data-flow-route-heading
          >
            {routeModeVisibleLabel(mode)}
          </span>
          {edges
            .filter((edge) => edge.mode === mode)
            .map((edge) => {
              const target = nodeByID.get(edge.to)!
              const label = edge.label ?? "Primary"
              return (
                <div
                  aria-label={`${label} route from ${source.title} ${edge.loop ? "returns" : "leads"} to ${target.title}`}
                  className={cn(
                    "flex min-w-0 items-center gap-1.5 text-[11px] leading-snug",
                    edge.mode === "optional" && "text-muted-foreground",
                  )}
                  data-flow-branch={source.id}
                  data-flow-branch-edge={label}
                  data-flow-branch-path={label}
                  data-flow-branch-target={edge.to}
                  data-flow-edge-key={flowEdgeKey(edge)}
                  data-flow-launch
                  data-flow-launch-target={edge.to}
                  data-flow-loop-connector={edge.loop ? edge.to : undefined}
                  data-flow-optional={
                    edge.mode === "optional" ? "true" : undefined
                  }
                  data-flow-parallel={
                    edge.mode === "parallel" ? "true" : undefined
                  }
                  data-flow-route-mode={edge.mode}
                  data-flow-source={edge.from}
                  key={flowEdgeKey(edge)}
                  role="group"
                >
                  {edge.loop ? <SemanticEdge edge={edge} /> : null}
                  <span
                    className="bg-background text-foreground shrink-0 rounded border px-1.5 py-0.5 font-semibold"
                    data-flow-branch-label
                    data-flow-launch-label
                  >
                    {label}
                  </span>
                  <span aria-hidden="true" className="text-primary shrink-0">
                    {edge.loop ? "↺" : "→"}
                  </span>
                  <span
                    className="min-w-0 [overflow-wrap:anywhere]"
                    data-flow-launch-target-title
                    data-flow-loop-target-title={
                      edge.loop ? edge.to : undefined
                    }
                  >
                    {edge.loop ? `Return to ${target.title}` : target.title}
                  </span>
                </div>
              )
            })}
        </div>
      ))}
    </div>
  )
}

function TargetConnector({
  edges,
  nodeByID,
  target,
}: {
  edges: PRLifecycleFlowEdge[]
  nodeByID: Map<string, PRLifecycleFlowNode>
  target: PRLifecycleFlowNode
}) {
  const source = nodeByID.get(edges[0].from)!
  const singletonMode = edges.length === 1 ? edges[0].mode : undefined
  const optional = singletonMode === "optional"
  const accessibleName =
    edges.length > 1
      ? `${edges.length} paths merge into ${target.title}`
      : optional
        ? `${source.title} optionally continues to ${target.title}`
        : `${source.title} continues to ${target.title}`
  return (
    <div
      aria-label={accessibleName}
      className={cn(
        "text-primary flex min-h-10 w-full min-w-0 items-end justify-center text-center",
        optional && "text-muted-foreground",
      )}
      data-flow-incoming-count={edges.length}
      data-flow-merge={edges.length > 1 ? "true" : undefined}
      data-flow-optional={optional ? "true" : undefined}
      data-flow-route-mode={singletonMode}
      data-flow-target-connector={target.id}
      role="img"
    >
      {edges.map((edge) => (
        <SemanticEdge edge={edge} key={flowEdgeKey(edge)} />
      ))}
      <span
        className="flex flex-col items-center justify-end text-lg leading-none"
        aria-hidden="true"
      >
        <span
          className={cn(
            "border-primary/40 h-3 border-l",
            optional && "border-muted-foreground border-dashed",
          )}
          data-flow-connector-stem
        />
        <span>↓</span>
      </span>
    </div>
  )
}

function LoopConnector({
  edge,
  source,
  target,
}: {
  edge: PRLifecycleFlowEdge
  source: PRLifecycleFlowNode
  target: PRLifecycleFlowNode
}) {
  return (
    <div
      aria-label={`${source.title} returns to ${target.title}`}
      className="border-primary/40 bg-primary/5 text-primary my-2 flex min-w-0 items-center justify-center gap-2 rounded-lg border border-dashed px-2 py-2 text-[11px] leading-snug"
      data-flow-edge-key={flowEdgeKey(edge)}
      data-flow-loop-connector={edge.to}
      data-flow-source={edge.from}
      role="group"
    >
      <SemanticEdge edge={edge} />
      <span className="text-lg leading-none" aria-hidden="true">
        ↺
      </span>
      <span
        className="text-foreground min-w-0 [overflow-wrap:anywhere]"
        data-flow-loop-target-title={edge.to}
      >
        Return to {target.title}
      </span>
    </div>
  )
}

function SemanticEdge({ edge }: { edge: PRLifecycleFlowEdge }) {
  return (
    <span
      aria-hidden="true"
      data-flow-edge={edge.mode}
      data-flow-edge-key={flowEdgeKey(edge)}
      data-flow-loop-target={edge.loop ? edge.to : undefined}
      data-flow-optional={edge.mode === "optional" ? "true" : undefined}
      data-flow-parallel={edge.mode === "parallel" ? "true" : undefined}
      data-flow-placement={edge.loop ? "source" : "target"}
      data-flow-semantic-edge
      data-flow-source={edge.from}
      data-flow-target={edge.to}
      hidden
    />
  )
}

function GraphNode({
  flowNode,
  instanceID,
  onSelect,
  profileID,
  selected,
  workflow,
}: {
  flowNode: PRLifecycleFlowNode
  instanceID: string
  onSelect: () => void
  profileID?: string
  selected: boolean
  workflow?: PRLifecycleGateWorkflow
}) {
  if (flowNode.kind === "action") return <ActionNode node={flowNode} />
  if (!flowNode.editable) return <LockedGateNode node={flowNode} />
  return (
    <EditableGateNode
      instanceID={instanceID}
      node={flowNode}
      number={flowNode.ordinal}
      onSelect={onSelect}
      profileID={profileID}
      selected={selected}
      workflow={workflow}
    />
  )
}

function ActionNode({ node }: { node: PRLifecycleFlowNode }) {
  return (
    <div
      className="bg-secondary flex min-h-20 w-full min-w-0 flex-col rounded-xl border p-3 [overflow-wrap:anywhere]"
      data-flow-kind="action"
      data-flow-node-id={node.id}
      data-flow-operation={node.operation}
    >
      <span className="text-muted-foreground text-[9px] font-bold tracking-wider">
        ACTION
      </span>
      <strong className="mt-1 text-xs leading-snug">{node.title}</strong>
      <span
        className="text-muted-foreground mt-2 text-[11px] leading-snug"
        data-flow-description
      >
        {node.description}
      </span>
    </div>
  )
}

function LockedGateNode({ node }: { node: PRLifecycleFlowNode }) {
  return (
    <div
      aria-label={node.title}
      className="border-destructive/70 bg-destructive/5 flex min-h-28 w-full min-w-0 flex-col rounded-xl border-2 p-3 [overflow-wrap:anywhere] shadow-sm"
      data-flow-kind="gate"
      data-flow-node-id={node.id}
      data-required-gate={node.safeguard}
      role="group"
    >
      <span className="border-destructive/40 text-destructive w-fit rounded border px-1.5 py-0.5 text-[9px] font-bold tracking-wider uppercase">
        Locked safeguard
      </span>
      <strong className="mt-1 text-xs leading-snug">{node.title}</strong>
      <span
        className="text-muted-foreground mt-1 text-[11px] leading-snug"
        data-gate-description
      >
        {node.description}
      </span>
    </div>
  )
}

function EditableGateNode({
  instanceID,
  node,
  number,
  onSelect,
  profileID,
  selected,
  workflow,
}: {
  instanceID: string
  node: PRLifecycleFlowNode
  number?: number
  onSelect: () => void
  profileID?: string
  selected: boolean
  workflow?: PRLifecycleGateWorkflow
}) {
  const decisionPoint = node.decision_point!
  const format = summarizeGateFormat(workflow, decisionPoint)
  const descriptionID = `${instanceID}-${node.id}-description`
  const formatID = `${instanceID}-${node.id}-format`
  const activate = onSelect

  return (
    <button
      aria-describedby={`${descriptionID} ${formatID}`}
      aria-haspopup="dialog"
      aria-label={node.title}
      className={cn(
        "bg-primary/5 border-primary/60 hover:bg-primary/10 hover:border-primary focus-visible:ring-ring relative flex min-h-28 w-full min-w-0 flex-col rounded-xl border-2 p-3 text-left [overflow-wrap:anywhere] shadow-sm transition-colors outline-none focus-visible:ring-2",
        selected && "bg-primary/10 border-primary ring-primary/30 ring-2",
      )}
      data-decision-point={decisionPoint}
      data-decision-title={node.title}
      data-edit-href={gateEditorHref(profileID, decisionPoint)}
      data-editor-title={node.title}
      data-flow-kind="gate"
      data-flow-node-id={node.id}
      data-gate-format={format.format}
      data-gate-id={decisionPoint}
      data-gate-number={number}
      data-gate-selected={selected ? "true" : undefined}
      data-workflow-configured={workflow ? "true" : "false"}
      onClick={activate}
      type="button"
    >
      <span className="flex w-full justify-end">
        {number ? (
          <span
            className={cn(
              "bg-primary text-primary-foreground flex size-6 shrink-0 items-center justify-center rounded-full font-mono text-[10px] font-bold",
              selected && "ring-primary/30 ring-2",
            )}
          >
            {number}
          </span>
        ) : null}
      </span>
      <strong className="mt-1 text-xs leading-snug">{node.title}</strong>
      <span
        className="text-muted-foreground mt-1 text-[11px] leading-snug"
        data-gate-description
        id={descriptionID}
      >
        {node.description}
      </span>
      <span className="mt-auto flex flex-wrap items-center gap-1.5 pt-2">
        <span
          className={cn(
            "rounded-md border px-1.5 py-0.5 text-[9px] font-bold tracking-wider uppercase",
            gateFormatClassName(format.format),
          )}
        >
          {format.label}
        </span>
        {format.fallback ? (
          <span className="text-muted-foreground text-[9px] font-semibold tracking-wider uppercase">
            default fallback
          </span>
        ) : null}
      </span>
      {format.composition ? (
        <span className="text-foreground mt-1 text-[10px] font-semibold">
          {format.composition}
        </span>
      ) : null}
      <span className="text-primary mt-1 text-[10px] font-bold tracking-wider">
        Edit gate →
      </span>
      <span className="sr-only" id={formatID}>
        {format.accessible}
      </span>
    </button>
  )
}

function createFlowLayout(flow: PRLifecycleFlow): FlowLayout {
  const nodeByID = new Map(flow.nodes.map((node) => [node.id, node]))
  const incoming = new Map<string, PRLifecycleFlowEdge[]>()
  const outgoing = new Map<string, PRLifecycleFlowEdge[]>()
  const adjacency = new Map<string, string[]>()
  const indegree = new Map(flow.nodes.map((node) => [node.id, 0]))
  for (const edge of flow.edges) {
    outgoing.set(edge.from, [...(outgoing.get(edge.from) ?? []), edge])
    if (edge.loop) continue
    incoming.set(edge.to, [...(incoming.get(edge.to) ?? []), edge])
    adjacency.set(edge.from, [...(adjacency.get(edge.from) ?? []), edge.to])
    indegree.set(edge.to, (indegree.get(edge.to) ?? 0) + 1)
  }

  const rankByNode = new Map<string, number>()
  const pending = flow.nodes
    .filter((node) => indegree.get(node.id) === 0)
    .map((node) => node.id)
  for (const id of pending) rankByNode.set(id, id === flow.entry ? 0 : 0)
  const visited = new Set<string>()
  while (pending.length > 0) {
    const current = pending.shift()!
    visited.add(current)
    const currentRank = rankByNode.get(current) ?? 0
    for (const target of adjacency.get(current) ?? []) {
      rankByNode.set(
        target,
        Math.max(rankByNode.get(target) ?? 0, currentRank + 1),
      )
      const remaining = (indegree.get(target) ?? 0) - 1
      indegree.set(target, remaining)
      if (remaining === 0) pending.push(target)
    }
  }
  let fallbackRank = Math.max(0, ...rankByNode.values()) + 1
  for (const node of flow.nodes) {
    if (visited.has(node.id)) continue
    rankByNode.set(node.id, fallbackRank++)
  }

  // Pull short paths down toward their next node instead of drawing detached
  // continuation rails through unrelated ranks. A singleton forward path is
  // therefore always shown in the rank immediately before its destination;
  // longer fork paths remain identifiable by their labelled source routes.
  for (const nodeID of [...visited].reverse()) {
    const targets = adjacency.get(nodeID) ?? []
    if (targets.length !== 1 || (outgoing.get(nodeID) ?? []).length !== 1) {
      continue
    }
    const latestRank = (rankByNode.get(targets[0]) ?? 1) - 1
    if (latestRank > (rankByNode.get(nodeID) ?? 0)) {
      rankByNode.set(nodeID, latestRank)
    }
  }

  const maxRank = Math.max(0, ...rankByNode.values())
  const nodeOrder = new Map(flow.nodes.map((node, index) => [node.id, index]))
  const ranks = Array.from({ length: maxRank + 1 }, (_, rank) =>
    flow.nodes
      .filter((node) => rankByNode.get(node.id) === rank)
      .sort(
        (left, right) =>
          (nodeOrder.get(left.id) ?? 0) - (nodeOrder.get(right.id) ?? 0),
      ),
  ).filter((nodes) => nodes.length > 0)
  return { ranks, nodeByID, incoming, outgoing }
}

function flowEdgeKey(edge: PRLifecycleFlowEdge): string {
  return `${edge.from}:${edge.to}`
}

function findDecisionPointFlow(
  catalog: PRLifecycleFlowCatalog,
  decisionPoint: PRLifecycleDecisionPoint | undefined,
): PRLifecycleFlow | undefined {
  if (!decisionPoint) return undefined
  return catalog.flows.find((flow) =>
    flowContainsDecisionPoint(flow, decisionPoint),
  )
}

function flowContainsDecisionPoint(
  flow: PRLifecycleFlow,
  decisionPoint: PRLifecycleDecisionPoint,
): boolean {
  return flow.nodes.some(
    (node) => node.editable && node.decision_point === decisionPoint,
  )
}

function routeModeVisibleLabel(mode: PRLifecycleFlowEdge["mode"]): string {
  if (mode === "linear") return "Primary path"
  if (mode === "choice") return "Choice"
  if (mode === "parallel") return "All required"
  return "Optional paths"
}

function responsiveGridClass(count: number): string {
  return cn(
    "grid min-w-0 grid-cols-1 gap-3",
    count === 2 && "@2xs/flow:grid-cols-2",
    count === 3 && "@2xs/flow:grid-cols-2 @md/flow:grid-cols-3",
    count === 4 &&
      "@2xs/flow:grid-cols-2 @md/flow:grid-cols-3 @xl/flow:grid-cols-4",
    count >= 5 &&
      "@2xs/flow:grid-cols-2 @md/flow:grid-cols-3 @xl/flow:grid-cols-4 @3xl/flow:grid-cols-5",
  )
}

function gateEditorHref(
  profileID: string | undefined,
  decisionPoint: PRLifecycleDecisionPoint,
): string {
  const profile = profileID ? `&profile=${encodeURIComponent(profileID)}` : ""
  return `/pull-requests?view=gate-profiles${profile}&gate=${encodeURIComponent(decisionPoint)}`
}

function summarizeGateFormat(
  workflow: PRLifecycleGateWorkflow | undefined,
  decisionPoint: PRLifecycleDecisionPoint,
): GateFormatSummary {
  if (!workflow) {
    return {
      format: "user",
      label: "User",
      fallback: true,
      accessible:
        "Gate format: User. No workflow is configured, so the runtime uses the default human fallback.",
    }
  }
  if (validatePRLifecycleGateWorkflow(workflow, decisionPoint).length > 0) {
    return {
      format: "needs-setup",
      label: "Needs setup",
      fallback: false,
      accessible:
        "Gate format: Needs setup. The configured workflow is incomplete or invalid.",
    }
  }

  const categories = workflow.stages.map((stage) => stageCategory[stage.kind])
  const uniqueCategories = new Set(categories)
  if (uniqueCategories.size === 1) {
    const category = categories[0]
    const label = gateFormatLabels[category]
    return {
      format: category,
      label,
      fallback: false,
      accessible: `Gate format: ${label}.`,
    }
  }

  const orderedCategories = categories.filter(
    (category, index) => index === 0 || category !== categories[index - 1],
  )
  const composition = orderedCategories
    .map((category) => gateFormatLabels[category])
    .join(" → ")
  return {
    format: "mixed",
    label: "Mixed",
    composition,
    fallback: false,
    accessible: `Gate format: Mixed. Ordered composition: ${orderedCategories
      .map((category) => gateFormatLabels[category])
      .join(", then ")}.`,
  }
}

function gateFormatClassName(format: GateFormat) {
  return cn(
    format === "automatic" && "bg-muted text-muted-foreground",
    format === "deterministic" && "bg-secondary text-secondary-foreground",
    format === "ai" && "bg-primary/10 border-primary/40 text-primary",
    format === "user" && "bg-accent text-accent-foreground",
    format === "mixed" && "bg-primary text-primary-foreground",
    format === "needs-setup" &&
      "bg-destructive/10 border-destructive/50 text-destructive",
  )
}

function Legend({
  label,
  variant,
}: {
  label: string
  variant: "action" | "gate" | "required"
}) {
  return (
    <span
      className={cn(
        "rounded-md border px-2 py-1",
        variant === "action" && "bg-secondary",
        variant === "gate" && "bg-primary/10 border-primary",
        variant === "required" &&
          "bg-destructive/5 border-destructive/60 text-destructive",
      )}
    >
      {label}
    </span>
  )
}

function GateFormatLegend() {
  const formats: { format: GateFormat; label: string }[] = [
    { format: "automatic", label: "Automatic" },
    { format: "deterministic", label: "Deterministic" },
    { format: "ai", label: "AI" },
    { format: "user", label: "User" },
    { format: "mixed", label: "Mixed" },
  ]

  return (
    <div
      aria-label="Gate format legend"
      className="text-muted-foreground flex flex-wrap items-center gap-1 text-[10px]"
    >
      <span className="mr-1 font-semibold">Gate format</span>
      {formats.map(({ format, label }) => (
        <span
          className={cn(
            "rounded-md border px-1.5 py-0.5 font-bold tracking-wider uppercase",
            gateFormatClassName(format),
          )}
          key={format}
        >
          {label}
        </span>
      ))}
    </div>
  )
}
