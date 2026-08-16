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
  roots: PRLifecycleFlowNode[]
  dominatorChildren: Map<string, PRLifecycleFlowNode[]>
  forwardDescendants: Map<string, Set<string>>
  immediateDominator: Map<string, string>
  nodeByID: Map<string, PRLifecycleFlowNode>
  incoming: Map<string, PRLifecycleFlowEdge[]>
  outgoing: Map<string, PRLifecycleFlowEdge[]>
  topologicalOrder: Map<string, number>
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
        {layout.roots.map((node) => (
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
    </section>
  )
}

function FlowNodeCell({
  branchEdge,
  flowNode,
  instanceID,
  layout,
  onSelect,
  profileID,
  selectedNodeID,
  workflows,
}: {
  branchEdge?: PRLifecycleFlowEdge
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
  const forward = outgoing.filter((edge) => !edge.loop)
  const singletonLoop =
    outgoing.length === 1 && outgoing[0].loop ? outgoing[0] : undefined
  const ownedBranchTargets = new Set(
    outgoing.flatMap((edge) =>
      !edge.loop &&
      layout.immediateDominator.get(edge.to) === flowNode.id &&
      (layout.incoming.get(edge.to) ?? []).length === 1
        ? [edge.to]
        : [],
    ),
  )
  const directChild =
    outgoing.length === 1 &&
    !outgoing[0].loop &&
    layout.immediateDominator.get(outgoing[0].to) === flowNode.id
      ? layout.nodeByID.get(outgoing[0].to)
      : undefined
  const renderedChildren = new Set(
    outgoing.length > 1
      ? ownedBranchTargets
      : directChild
        ? [directChild.id]
        : [],
  )
  const sharedContinuations = (
    layout.dominatorChildren.get(flowNode.id) ?? []
  ).filter((child) => !renderedChildren.has(child.id))
  const sharedContinuationLevels = groupSharedContinuations(
    sharedContinuations,
    layout,
  )
  return (
    <div
      className="flex min-w-0 flex-col"
      data-flow-cell={flowNode.id}
      data-flow-incoming-count={incoming.length}
    >
      {incoming.length > 0 ? (
        <TargetConnector
          branchEdge={branchEdge}
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
          instanceID={instanceID}
          layout={layout}
          nodeByID={layout.nodeByID}
          onSelect={onSelect}
          profileID={profileID}
          selectedNodeID={selectedNodeID}
          source={flowNode}
          workflows={workflows}
        />
      ) : null}
      {singletonLoop ? (
        <LoopConnector
          edge={singletonLoop}
          source={flowNode}
          target={layout.nodeByID.get(singletonLoop.to)!}
        />
      ) : null}
      {outgoing.length === 1 && forward.length === 1 ? (
        directChild ? (
          <FlowNodeCell
            flowNode={directChild}
            instanceID={instanceID}
            layout={layout}
            onSelect={onSelect}
            profileID={profileID}
            selectedNodeID={selectedNodeID}
            workflows={workflows}
          />
        ) : (
          <FlowReference
            edge={forward[0]}
            source={flowNode}
            target={layout.nodeByID.get(forward[0].to)!}
          />
        )
      ) : null}
      {sharedContinuations.length > 0 ? (
        <div
          className="min-w-0 space-y-1"
          data-flow-shared-continuations={flowNode.id}
        >
          {sharedContinuationLevels.map((children, level) => (
            <div
              className="grid min-w-0 [grid-template-columns:repeat(auto-fit,minmax(min(100%,10rem),1fr))] items-start gap-3"
              data-flow-shared-level={level}
              key={level}
            >
              {children.map((child) => (
                <div
                  className="min-w-0 self-start"
                  data-flow-shared-continuation={child.id}
                  key={child.id}
                >
                  <FlowNodeCell
                    flowNode={child}
                    instanceID={instanceID}
                    layout={layout}
                    onSelect={onSelect}
                    profileID={profileID}
                    selectedNodeID={selectedNodeID}
                    workflows={workflows}
                  />
                </div>
              ))}
            </div>
          ))}
        </div>
      ) : null}
    </div>
  )
}

function groupSharedContinuations(
  children: PRLifecycleFlowNode[],
  layout: FlowLayout,
): PRLifecycleFlowNode[][] {
  // Immediate-dominator siblings are not necessarily sequential. Keep
  // incomparable continuations on one level; only move a child down when a
  // forward path from another shared child actually reaches it.
  const ordered = [...children].sort(
    (left, right) =>
      (layout.topologicalOrder.get(left.id) ?? 0) -
      (layout.topologicalOrder.get(right.id) ?? 0),
  )
  const levelByID = new Map<string, number>()
  for (const child of ordered) {
    let level = 0
    for (const candidate of ordered) {
      if (candidate.id === child.id) continue
      if (!layout.forwardDescendants.get(candidate.id)?.has(child.id)) continue
      level = Math.max(level, (levelByID.get(candidate.id) ?? 0) + 1)
    }
    levelByID.set(child.id, level)
  }
  const maxLevel = Math.max(-1, ...levelByID.values())
  return Array.from({ length: maxLevel + 1 }, (_, level) =>
    ordered.filter((child) => levelByID.get(child.id) === level),
  )
}

function BranchLaunches({
  edges,
  instanceID,
  layout,
  nodeByID,
  onSelect,
  profileID,
  selectedNodeID,
  source,
  workflows,
}: {
  edges: PRLifecycleFlowEdge[]
  instanceID: string
  layout: FlowLayout
  nodeByID: Map<string, PRLifecycleFlowNode>
  onSelect: (node: PRLifecycleFlowNode) => void
  profileID?: string
  selectedNodeID?: string
  source: PRLifecycleFlowNode
  workflows?: PRLifecycleGateProfile["workflows"]
}) {
  const modes = [
    ...new Set(edges.map((edge) => edge.mode)),
  ] as PRLifecycleFlowEdge["mode"][]
  const routeComposition = modes.join("+")
  return (
    <div
      aria-label={`${source.title} branches`}
      className="border-primary/20 bg-muted/10 mt-2 min-w-0 space-y-3 border-y py-2"
      data-flow-launches={source.id}
      data-flow-route-composition={routeComposition}
      role="group"
    >
      <div className="flex min-w-0 flex-wrap gap-1 px-1">
        {modes.map((mode) => (
          <span
            className="border-primary/20 bg-background/60 text-muted-foreground rounded border px-1.5 py-0.5 text-[10px] font-bold tracking-wider uppercase"
            data-flow-route-group={source.id}
            data-flow-route-mode={mode}
            data-flow-source={source.id}
            key={mode}
          >
            <span data-flow-route-heading>{routeModeVisibleLabel(mode)}</span>
          </span>
        ))}
      </div>
      <div
        className="grid min-w-0 [grid-template-columns:repeat(auto-fit,minmax(min(100%,10rem),1fr))] items-start gap-3"
        data-flow-branch-group={source.id}
      >
        {edges.map((edge) => {
          const target = nodeByID.get(edge.to)!
          const owned =
            !edge.loop &&
            layout.immediateDominator.get(edge.to) === source.id &&
            (layout.incoming.get(edge.to) ?? []).length === 1
          return (
            <div
              aria-label={`${edge.label ?? "Primary"} branch from ${source.title}`}
              className="border-primary/20 bg-background/40 min-w-0 self-start rounded-lg border py-2"
              data-flow-branch-lane
              data-flow-branch-source={source.id}
              data-flow-branch-target={edge.to}
              data-flow-edge-key={flowEdgeKey(edge)}
              data-flow-route-mode={edge.mode}
              key={flowEdgeKey(edge)}
              role="group"
            >
              {owned ? (
                <FlowNodeCell
                  branchEdge={edge}
                  flowNode={target}
                  instanceID={instanceID}
                  layout={layout}
                  onSelect={onSelect}
                  profileID={profileID}
                  selectedNodeID={selectedNodeID}
                  workflows={workflows}
                />
              ) : (
                <FlowReference
                  branched
                  edge={edge}
                  source={source}
                  target={target}
                />
              )}
            </div>
          )
        })}
      </div>
    </div>
  )
}

function FlowReference({
  branched = false,
  edge,
  source,
  target,
}: {
  branched?: boolean
  edge: PRLifecycleFlowEdge
  source: PRLifecycleFlowNode
  target: PRLifecycleFlowNode
}) {
  const label = edge.label ?? "Primary"
  const loop = edge.loop
  const targetText = loop ? `Return to ${target.title}` : target.title
  return (
    <div
      aria-label={`${branched ? `${label} route from ` : ""}${source.title} ${loop ? "returns" : "continues"} to ${target.title}`}
      className={cn(
        "text-primary my-1 flex min-h-10 min-w-0 flex-col items-center justify-end text-center text-[11px] leading-snug",
        loop &&
          "border-primary/30 bg-primary/5 my-2 min-h-0 flex-row justify-center gap-2 rounded-lg border border-dashed px-2 py-2 text-left",
        edge.mode === "optional" && "text-muted-foreground",
      )}
      data-flow-branch={branched ? source.id : undefined}
      data-flow-branch-edge={branched ? label : undefined}
      data-flow-branch-path={branched ? label : undefined}
      data-flow-branch-target={branched ? edge.to : undefined}
      data-flow-edge-key={flowEdgeKey(edge)}
      data-flow-launch={branched ? true : undefined}
      data-flow-launch-target={branched ? edge.to : undefined}
      data-flow-loop-connector={loop ? edge.to : undefined}
      data-flow-optional={edge.mode === "optional" ? "true" : undefined}
      data-flow-parallel={edge.mode === "parallel" ? "true" : undefined}
      data-flow-reference={edge.to}
      data-flow-route-mode={edge.mode}
      data-flow-source={edge.from}
      data-flow-visible-edge-key={flowEdgeKey(edge)}
      role="group"
    >
      {loop ? <SemanticEdge edge={edge} /> : null}
      {branched ? (
        <span
          className="bg-background text-foreground shrink-0 rounded border px-1.5 py-0.5 font-semibold"
          data-flow-branch-label
          data-flow-launch-label
        >
          {label}
        </span>
      ) : null}
      {loop ? (
        <span className="shrink-0 text-base leading-none" aria-hidden="true">
          ↺
        </span>
      ) : (
        <span
          className="flex flex-col items-center justify-end text-lg leading-none"
          aria-hidden="true"
        >
          <span
            className={cn(
              "border-primary/40 h-3 border-l",
              edge.mode === "optional" &&
                "border-muted-foreground border-dashed",
            )}
            data-flow-connector-stem
          />
          <span>↓</span>
        </span>
      )}
      <span
        className={cn(
          "text-foreground min-w-0 [overflow-wrap:anywhere]",
          !loop && "sr-only",
        )}
        data-flow-launch-target-title={branched ? edge.to : undefined}
        data-flow-loop-target-title={loop ? edge.to : undefined}
      >
        {targetText}
      </span>
    </div>
  )
}

function TargetConnector({
  branchEdge,
  edges,
  nodeByID,
  target,
}: {
  branchEdge?: PRLifecycleFlowEdge
  edges: PRLifecycleFlowEdge[]
  nodeByID: Map<string, PRLifecycleFlowNode>
  target: PRLifecycleFlowNode
}) {
  const source = nodeByID.get(edges[0].from)!
  const singletonMode = edges.length === 1 ? edges[0].mode : undefined
  const optional = singletonMode === "optional"
  const merge = edges.length > 1
  const branchLabel = branchEdge?.label ?? (branchEdge ? "Primary" : undefined)
  const accessibleName = branchEdge
    ? `${branchLabel} route from ${source.title} ${optional ? "optionally leads" : "leads"} to ${target.title}`
    : merge
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
      data-flow-branch={branchEdge?.from}
      data-flow-branch-edge={branchLabel}
      data-flow-branch-path={branchLabel}
      data-flow-branch-target={branchEdge?.to}
      data-flow-edge-key={branchEdge ? flowEdgeKey(branchEdge) : undefined}
      data-flow-launch={branchEdge ? true : undefined}
      data-flow-launch-target={branchEdge?.to}
      data-flow-merge={merge ? "true" : undefined}
      data-flow-merge-contributors={
        merge ? edges.map((edge) => edge.from).join(" ") : undefined
      }
      data-flow-optional={optional ? "true" : undefined}
      data-flow-parallel={singletonMode === "parallel" ? "true" : undefined}
      data-flow-route-mode={singletonMode}
      data-flow-source={branchEdge?.from}
      data-flow-target-connector={target.id}
      data-flow-visible-edge-key={!merge ? flowEdgeKey(edges[0]) : undefined}
      role={branchEdge ? "group" : "img"}
    >
      {edges.map((edge) => (
        <SemanticEdge edge={edge} key={flowEdgeKey(edge)} />
      ))}
      {merge ? (
        <span
          className="flex flex-col items-center justify-end text-lg leading-none"
          aria-hidden="true"
        >
          <span
            className="bg-background text-foreground mb-1 rounded border px-1.5 py-0.5 text-[10px] leading-snug font-semibold"
            data-flow-merge-label
          >
            {edges.length} paths merge
          </span>
          <span className="border-primary/40 h-3 border-l" />
          <span>◆</span>
        </span>
      ) : (
        <span
          className="flex flex-col items-center justify-end text-lg leading-none"
          aria-hidden="true"
        >
          {branchLabel ? (
            <span
              className="bg-background text-foreground mb-1 rounded border px-1.5 py-0.5 text-[11px] leading-snug font-semibold"
              data-flow-branch-label
              data-flow-launch-label
            >
              {branchLabel}
            </span>
          ) : null}
          <span
            className={cn(
              "border-primary/40 h-3 border-l",
              optional && "border-muted-foreground border-dashed",
            )}
            data-flow-connector-stem
          />
          <span>↓</span>
        </span>
      )}
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
      data-flow-visible-edge-key={flowEdgeKey(edge)}
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
  onSelect,
  profileID,
  selected,
  workflow,
}: {
  instanceID: string
  node: PRLifecycleFlowNode
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
      data-gate-name={node.title}
      data-gate-selected={selected ? "true" : undefined}
      data-workflow-configured={workflow ? "true" : "false"}
      onClick={activate}
      type="button"
    >
      <strong className="text-xs leading-snug">{node.title}</strong>
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

/**
 * Assigns every forward node to its closest dominating branch. Unlike global
 * topological ranks, this keeps a descendant inside the branch that owns it.
 * Multi-parent nodes stay with their common dominator and render once after
 * the contributing branch references. Loop edges remain source-side links.
 */
function createFlowLayout(flow: PRLifecycleFlow): FlowLayout {
  const nodeByID = new Map(flow.nodes.map((node) => [node.id, node]))
  const incoming = new Map<string, PRLifecycleFlowEdge[]>()
  const outgoing = new Map<string, PRLifecycleFlowEdge[]>()
  for (const edge of flow.edges) {
    outgoing.set(edge.from, [...(outgoing.get(edge.from) ?? []), edge])
    if (edge.loop) continue
    incoming.set(edge.to, [...(incoming.get(edge.to) ?? []), edge])
  }

  const nodeOrder = new Map(flow.nodes.map((node, index) => [node.id, index]))
  const forwardAdjacency = new Map<string, string[]>()
  const pendingIndegree = new Map(
    flow.nodes.map((node) => [node.id, (incoming.get(node.id) ?? []).length]),
  )
  for (const node of flow.nodes) {
    forwardAdjacency.set(
      node.id,
      (outgoing.get(node.id) ?? [])
        .filter((edge) => !edge.loop)
        .map((edge) => edge.to),
    )
  }
  const pending = flow.nodes
    .filter((node) => (pendingIndegree.get(node.id) ?? 0) === 0)
    .sort(
      (left, right) =>
        (nodeOrder.get(left.id) ?? 0) - (nodeOrder.get(right.id) ?? 0),
    )
  const topologicalIDs: string[] = []
  while (pending.length > 0) {
    const current = pending.shift()!
    topologicalIDs.push(current.id)
    for (const targetID of forwardAdjacency.get(current.id) ?? []) {
      const remaining = (pendingIndegree.get(targetID) ?? 0) - 1
      pendingIndegree.set(targetID, remaining)
      if (remaining !== 0) continue
      pending.push(nodeByID.get(targetID)!)
      pending.sort(
        (left, right) =>
          (nodeOrder.get(left.id) ?? 0) - (nodeOrder.get(right.id) ?? 0),
      )
    }
  }
  for (const node of flow.nodes) {
    if (!topologicalIDs.includes(node.id)) topologicalIDs.push(node.id)
  }
  const topologicalOrder = new Map(
    topologicalIDs.map((nodeID, index) => [nodeID, index]),
  )
  const forwardDescendants = new Map<string, Set<string>>()
  for (const nodeID of [...topologicalIDs].reverse()) {
    const descendants = new Set<string>()
    for (const targetID of forwardAdjacency.get(nodeID) ?? []) {
      descendants.add(targetID)
      for (const descendant of forwardDescendants.get(targetID) ?? []) {
        descendants.add(descendant)
      }
    }
    forwardDescendants.set(nodeID, descendants)
  }

  const roots = flow.nodes.filter(
    (node) =>
      node.id === flow.entry || (incoming.get(node.id) ?? []).length === 0,
  )
  roots.sort((left, right) => {
    if (left.id === flow.entry) return -1
    if (right.id === flow.entry) return 1
    return (nodeOrder.get(left.id) ?? 0) - (nodeOrder.get(right.id) ?? 0)
  })

  const allNodeIDs = new Set(flow.nodes.map((node) => node.id))
  const rootIDs = new Set(roots.map((node) => node.id))
  const dominators = new Map<string, Set<string>>()
  for (const node of flow.nodes) {
    dominators.set(
      node.id,
      rootIDs.has(node.id) ? new Set([node.id]) : new Set(allNodeIDs),
    )
  }

  let changed = true
  while (changed) {
    changed = false
    for (const node of flow.nodes) {
      if (rootIDs.has(node.id)) continue
      const predecessors = incoming.get(node.id) ?? []
      let next =
        predecessors.length > 0
          ? new Set(dominators.get(predecessors[0].from) ?? [])
          : new Set<string>()
      for (const edge of predecessors.slice(1)) {
        const predecessorDominators = dominators.get(edge.from) ?? new Set()
        next = new Set(
          [...next].filter((candidate) => predecessorDominators.has(candidate)),
        )
      }
      next.add(node.id)
      const current = dominators.get(node.id)!
      if (
        next.size !== current.size ||
        [...next].some((candidate) => !current.has(candidate))
      ) {
        dominators.set(node.id, next)
        changed = true
      }
    }
  }

  const immediateDominator = new Map<string, string>()
  for (const node of flow.nodes) {
    const strictDominators = [...(dominators.get(node.id) ?? [])]
      .filter((candidate) => candidate !== node.id)
      .sort((left, right) => {
        const depthDifference =
          (dominators.get(right)?.size ?? 0) - (dominators.get(left)?.size ?? 0)
        if (depthDifference !== 0) return depthDifference
        return (nodeOrder.get(left) ?? 0) - (nodeOrder.get(right) ?? 0)
      })
    if (strictDominators[0]) {
      immediateDominator.set(node.id, strictDominators[0])
    }
  }

  const dominatorChildren = new Map<string, PRLifecycleFlowNode[]>()
  for (const node of flow.nodes) {
    const parent = immediateDominator.get(node.id)
    if (!parent) continue
    dominatorChildren.set(parent, [
      ...(dominatorChildren.get(parent) ?? []),
      node,
    ])
  }
  for (const children of dominatorChildren.values()) {
    children.sort(
      (left, right) =>
        (topologicalOrder.get(left.id) ?? 0) -
        (topologicalOrder.get(right.id) ?? 0),
    )
  }

  const dominated = new Set(immediateDominator.keys())
  for (const node of flow.nodes) {
    if (!dominated.has(node.id) && !rootIDs.has(node.id)) roots.push(node)
  }

  return {
    dominatorChildren,
    forwardDescendants,
    immediateDominator,
    incoming,
    nodeByID,
    outgoing,
    roots,
    topologicalOrder,
  }
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
