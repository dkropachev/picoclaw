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

interface FlowContinuation {
  kind: "continuation"
  id: string
  edge: PRLifecycleFlowEdge
  edgeIndex: number
}

type FlowLayoutItem = PRLifecycleFlowNode | FlowContinuation

interface FlowLayout {
  ranks: FlowLayoutItem[][]
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

  return (
    <section
      aria-labelledby={titleID}
      className={cn(
        "bg-card min-w-0 overflow-hidden rounded-xl border",
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
                    onSelect={onSelect}
                    profileID={profileID}
                    selectedDecisionPoint={selectedDecisionPoint}
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
        const gateCount = flow.nodes.length - actionCount
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
              {actionCount} actions · {gateCount} gates
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
  selectedDecisionPoint,
  workflows,
}: {
  flow: PRLifecycleFlow
  instanceID: string
  onSelect: (decisionPoint: PRLifecycleDecisionPoint) => void
  profileID?: string
  selectedDecisionPoint?: PRLifecycleDecisionPoint
  workflows?: PRLifecycleGateProfile["workflows"]
}) {
  const layout = useMemo(() => createFlowLayout(flow), [flow])

  return (
    <section
      aria-label={`${flow.title} graph`}
      className="bg-background/60 min-w-0 rounded-xl border p-3"
      data-flow-graph={flow.id}
    >
      <div className="mb-3 flex items-center justify-between gap-3">
        <div className="min-w-0">
          <p className="text-primary text-[10px] font-semibold tracking-wider uppercase">
            Workflow flow
          </p>
          <h3 className="text-sm font-semibold">{flow.title}</h3>
        </div>
        <span className="text-muted-foreground rounded-md border px-2 py-1 font-mono text-[10px]">
          {flow.nodes.length} nodes
        </span>
      </div>

      <div className="mx-auto w-full max-w-5xl min-w-0 space-y-1">
        {layout.ranks.map((nodes, rank) => (
          <div
            className={responsiveGridClass(nodes.length)}
            data-flow-rank={rank}
            key={rank}
          >
            {nodes.map((item) =>
              item.kind === "continuation" ? (
                <ContinuationRail item={item} key={item.id} />
              ) : (
                <FlowNodeCell
                  flowNode={item}
                  instanceID={instanceID}
                  key={item.id}
                  layout={layout}
                  onSelect={onSelect}
                  profileID={profileID}
                  selectedDecisionPoint={selectedDecisionPoint}
                  workflows={workflows}
                />
              ),
            )}
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
  selectedDecisionPoint,
  workflows,
}: {
  flowNode: PRLifecycleFlowNode
  instanceID: string
  layout: FlowLayout
  onSelect: (decisionPoint: PRLifecycleDecisionPoint) => void
  profileID?: string
  selectedDecisionPoint?: PRLifecycleDecisionPoint
  workflows?: PRLifecycleGateProfile["workflows"]
}) {
  const incoming = layout.incoming.get(flowNode.id) ?? []
  const outgoing = layout.outgoing.get(flowNode.id) ?? []
  const forward = outgoing.filter((edge) => !edge.loop)
  const loops = outgoing.filter((edge) => edge.loop)
  return (
    <div
      className="flex min-w-0 flex-col"
      data-flow-cell={flowNode.id}
      data-flow-incoming-count={incoming.length}
    >
      {incoming.map((edge) => {
        const source = layout.nodeByID.get(edge.from)!
        return (
          <EdgeRoute
            edge={edge}
            key={`${edge.from}:${edge.to}:${edge.mode}:${edge.outcome ?? ""}`}
            showLabel={(layout.outgoing.get(edge.from) ?? []).length > 1}
            source={source}
            sourceEdges={layout.outgoing.get(edge.from) ?? []}
            target={flowNode}
          />
        )
      })}
      <GraphNode
        flowNode={flowNode}
        instanceID={instanceID}
        onSelect={onSelect}
        profileID={profileID}
        selected={
          flowNode.decision_point === selectedDecisionPoint && flowNode.editable
        }
        workflow={
          flowNode.decision_point
            ? workflows?.[flowNode.decision_point]
            : undefined
        }
      />
      {outgoing.length > 1 && forward.length > 0 ? (
        <BranchLaunches
          edges={forward}
          nodeByID={layout.nodeByID}
          source={flowNode}
        />
      ) : null}
      {loops.map((edge) => (
        <EdgeRoute
          edge={edge}
          key={`${edge.from}:${edge.to}:${edge.mode}:${edge.outcome ?? ""}`}
          showLabel={outgoing.length > 1}
          source={flowNode}
          sourceEdges={outgoing}
          target={layout.nodeByID.get(edge.to)!}
        />
      ))}
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
  return (
    <div
      aria-label={`${source.title} route launches`}
      className="border-primary/20 bg-muted/10 mt-1 min-w-0 space-y-1 rounded-lg border p-1.5"
      data-flow-launches={source.id}
      role="group"
    >
      {edges.map((edge) => {
        const target = nodeByID.get(edge.to)!
        const label = edge.label ?? "Primary"
        return (
          <div
            aria-label={`${label} route from ${source.title} to ${target.title}`}
            className={cn(
              "flex min-w-0 items-center gap-1.5 text-[10px] leading-snug",
              edge.mode === "optional" && "text-muted-foreground",
            )}
            data-flow-edge-key={flowEdgeKey(edge)}
            data-flow-launch
            data-flow-launch-target={edge.to}
            data-flow-source={edge.from}
            key={flowEdgeKey(edge)}
            role="group"
          >
            <span
              className="bg-background text-foreground shrink-0 rounded border px-1.5 py-0.5 font-semibold"
              data-flow-launch-label
            >
              {label}
            </span>
            <span aria-hidden="true" className="text-primary shrink-0">
              →
            </span>
            <span
              className="min-w-0 [overflow-wrap:anywhere]"
              data-flow-launch-target-title
            >
              {target.title}
            </span>
          </div>
        )
      })}
    </div>
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
  onSelect: (decisionPoint: PRLifecycleDecisionPoint) => void
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

function ContinuationRail({ item }: { item: FlowContinuation }) {
  return (
    <div
      aria-hidden="true"
      className="text-primary/60 flex min-h-24 min-w-0 flex-col items-center justify-center text-lg leading-none"
      data-flow-continuation-for={`${item.edge.from}:${item.edge.to}`}
      data-flow-edge-key={flowEdgeKey(item.edge)}
      data-flow-continuation-target={item.edge.to}
    >
      <span className="border-primary/40 min-h-14 flex-1 border-l-2" />
      <span>↓</span>
    </div>
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
      <span className="flex w-full items-start justify-between gap-2">
        <span className="text-destructive text-[9px] font-bold tracking-wider">
          LOCKED SAFEGUARD
        </span>
        <span className="border-destructive/40 text-destructive rounded border px-1.5 py-0.5 text-[8px] font-bold tracking-wider uppercase">
          Locked
        </span>
      </span>
      <strong className="mt-1 text-xs leading-snug">{node.title}</strong>
      <span
        className="text-muted-foreground mt-1 text-[10px] leading-tight"
        data-gate-description
      >
        {node.description}
      </span>
      <span className="text-muted-foreground mt-auto pt-2 text-[9px] font-bold tracking-wider uppercase">
        Fixed · not editable
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
  onSelect: (decisionPoint: PRLifecycleDecisionPoint) => void
  profileID?: string
  selected: boolean
  workflow?: PRLifecycleGateWorkflow
}) {
  const decisionPoint = node.decision_point!
  const format = summarizeGateFormat(workflow, decisionPoint)
  const descriptionID = `${instanceID}-${node.id}-description`
  const formatID = `${instanceID}-${node.id}-format`
  const activate = () => onSelect(decisionPoint)
  const handleKeyDown = (event: KeyboardEvent<HTMLButtonElement>) => {
    if (event.key !== "Enter" && event.key !== " ") return
    event.preventDefault()
    activate()
  }

  return (
    <button
      aria-describedby={`${descriptionID} ${formatID}`}
      aria-expanded={selected}
      aria-haspopup="dialog"
      aria-label={node.title}
      aria-pressed={selected}
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
      data-workflow-configured={workflow ? "true" : "false"}
      onClick={activate}
      onKeyDown={handleKeyDown}
      type="button"
    >
      <span className="flex w-full items-start justify-between gap-2">
        <span className="text-primary text-[9px] font-bold tracking-wider">
          EDITABLE GATE
        </span>
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
        className="text-muted-foreground mt-1 text-[10px] leading-tight"
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
          <span className="text-muted-foreground text-[8px] font-semibold tracking-wider uppercase">
            default fallback
          </span>
        ) : null}
      </span>
      {format.composition ? (
        <span className="text-foreground mt-1 text-[9px] font-semibold">
          {format.composition}
        </span>
      ) : null}
      <span className="text-primary mt-1 text-[9px] font-bold tracking-wider">
        OPEN SETTINGS →
      </span>
      <span className="sr-only" id={formatID}>
        {format.accessible}
      </span>
    </button>
  )
}

function EdgeRoute({
  edge,
  showLabel,
  source,
  sourceEdges,
  target,
}: {
  edge: PRLifecycleFlowEdge
  showLabel: boolean
  source: PRLifecycleFlowNode
  sourceEdges: PRLifecycleFlowEdge[]
  target: PRLifecycleFlowNode
}) {
  const displayLabel = showLabel ? (edge.label ?? "Primary") : undefined
  const describedLabel = edge.label ?? "primary"
  const routeComposition = [
    ...new Set(sourceEdges.map((candidate) => candidate.mode)),
  ].join("+")
  const routeDescription =
    edge.mode === "choice"
      ? `${describedLabel} choice leads to`
      : edge.mode === "parallel"
        ? `also follows ${describedLabel} to`
        : edge.mode === "optional"
          ? edge.label
            ? `optionally follows ${edge.label} to`
            : "optionally continues to"
          : "continues to"
  return (
    <div
      aria-label={`${source.title} ${routeDescription} ${target.title}${edge.loop ? ", loop" : ""}`}
      className={cn(
        "text-primary flex min-h-14 w-full min-w-0 flex-col items-center justify-center text-center",
        showLabel && "border-primary/20 rounded-lg border-x",
        edge.mode === "optional" && "text-muted-foreground",
        edge.loop &&
          "border-primary/40 bg-primary/5 my-1 rounded-lg border border-dashed px-2 py-2",
      )}
      data-flow-branch={showLabel ? source.id : undefined}
      data-flow-branch-edge={displayLabel}
      data-flow-branch-path={displayLabel}
      data-flow-branch-target={edge.to}
      data-flow-edge={edge.mode}
      data-flow-edge-key={flowEdgeKey(edge)}
      data-flow-incoming-for={edge.loop ? undefined : edge.to}
      data-flow-loop-target={edge.loop ? edge.to : undefined}
      data-flow-optional={edge.mode === "optional" ? "true" : undefined}
      data-flow-parallel={edge.mode === "parallel" ? "true" : undefined}
      data-flow-placement={edge.loop ? "source" : "target"}
      data-flow-route-composition={showLabel ? routeComposition : undefined}
      data-flow-route-mode={showLabel ? edge.mode : undefined}
      data-flow-source={edge.from}
      data-flow-target={edge.to}
      role="group"
      title={edge.loop ? `Returns to ${target.title}` : undefined}
    >
      {showLabel ? (
        <span
          className={cn(
            "text-muted-foreground mb-1 text-[8px] font-bold tracking-wider uppercase",
            edge.mode === "parallel" && "text-primary",
          )}
          data-flow-route-heading
        >
          {routeModeVisibleLabel(edge.mode)}
        </span>
      ) : null}
      {displayLabel ? (
        <span
          className="bg-background text-foreground rounded border px-2 py-0.5 text-[10px] font-semibold [overflow-wrap:anywhere]"
          data-flow-branch-label
        >
          {displayLabel}
        </span>
      ) : null}
      {edge.loop && showLabel ? (
        <span
          className="text-muted-foreground mt-1 text-[10px] leading-snug [overflow-wrap:anywhere]"
          data-flow-loop-target-title={edge.to}
        >
          Returns to {target.title}
        </span>
      ) : null}
      <span className="mt-1 text-lg leading-none" aria-hidden="true">
        {edge.loop ? "↺" : "↓"}
      </span>
    </div>
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
  const maxRank = Math.max(0, ...rankByNode.values())
  const nodeOrder = new Map(flow.nodes.map((node, index) => [node.id, index]))
  const ranks = Array.from({ length: maxRank + 1 }, (_, rank) => {
    const items: FlowLayoutItem[] = flow.nodes.filter(
      (node) => rankByNode.get(node.id) === rank,
    )
    flow.edges.forEach((edge, edgeIndex) => {
      if (edge.loop) return
      const fromRank = rankByNode.get(edge.from) ?? 0
      const toRank = rankByNode.get(edge.to) ?? fromRank + 1
      if (fromRank < rank && rank < toRank) {
        items.push({
          kind: "continuation",
          id: `${edge.from}:${edge.to}:${edge.outcome ?? ""}:${rank}`,
          edge,
          edgeIndex,
        })
      }
    })
    return items.sort((left, right) => {
      const leftOrder = layoutItemOrder(left, nodeOrder, flow.edges.length)
      const rightOrder = layoutItemOrder(right, nodeOrder, flow.edges.length)
      return leftOrder - rightOrder
    })
  }).filter((nodes) => nodes.length > 0)
  return { ranks, nodeByID, incoming, outgoing }
}

function layoutItemOrder(
  item: FlowLayoutItem,
  nodeOrder: Map<string, number>,
  edgeCount: number,
): number {
  if (item.kind !== "continuation") return nodeOrder.get(item.id) ?? 0
  const source = nodeOrder.get(item.edge.from) ?? 0
  const target = nodeOrder.get(item.edge.to) ?? source
  return (source + target) / 2 + item.edgeIndex / Math.max(1, edgeCount * 100)
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
    count === 2 && "sm:grid-cols-2",
    count >= 3 && "sm:grid-cols-2 lg:grid-cols-3",
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
