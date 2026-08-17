import {
  type KeyboardEvent,
  type RefObject,
  useEffect,
  useId,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react"

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
  bands: PRLifecycleFlowNode[][]
  nodeByID: Map<string, PRLifecycleFlowNode>
  incoming: Map<string, PRLifecycleFlowEdge[]>
  outgoing: Map<string, PRLifecycleFlowEdge[]>
  rankByNode: Map<string, number>
}

interface MeasuredFlowEdge {
  edge: PRLifecycleFlowEdge
  endX: number
  endY: number
  labelX: number
  labelY: number
  path: string
  startX: number
  startY: number
}

interface FlowGeometry {
  edges: MeasuredFlowEdge[]
  height: number
  width: number
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

type FlowRouteTone = "linear" | "choice" | "parallel" | "optional" | "return"

const flowRouteTones: FlowRouteTone[] = [
  "linear",
  "choice",
  "parallel",
  "optional",
  "return",
]

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
            Plain cards are actions. Select a highlighted gate to change its
            policy.
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
          <FlowRouteLegend />
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
  const canvasRef = useRef<HTMLDivElement>(null)
  const geometry = useFlowGeometry(canvasRef, flow, layout)

  return (
    <section
      aria-label={`${flow.title} graph`}
      className="bg-background/60 @container/flow min-w-0 rounded-xl border p-3"
      data-flow-band-count={layout.bands.length}
      data-flow-graph={flow.id}
    >
      <div
        className="relative mx-auto w-full max-w-7xl min-w-0 px-12 py-8"
        data-flow-canvas
        ref={canvasRef}
      >
        <FlowEdgeOverlay
          flow={flow}
          geometry={geometry}
          instanceID={instanceID}
          layout={layout}
        />
        <div
          className="relative z-10 flex min-w-0 flex-col gap-16"
          data-flow-bands
        >
          {layout.bands.map((nodes, band) => (
            <div
              className={flowBandGridClass(nodes.length)}
              data-flow-band={band}
              data-flow-band-count={nodes.length}
              key={band}
            >
              {nodes.map((node) => (
                <div
                  className={cn(
                    "min-w-0",
                    nodes.length === 1 && "mx-auto w-full max-w-2xl",
                  )}
                  data-flow-node-cell={node.id}
                  data-flow-node-rank={band}
                  key={node.id}
                >
                  <FlowNodeCell
                    flowNode={node}
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
        <div hidden>
          {flow.edges.map((edge) => (
            <SemanticEdge edge={edge} key={flowEdgeKey(edge)} />
          ))}
        </div>
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
  return (
    <div
      className="flex min-w-0 flex-col"
      data-flow-cell={flowNode.id}
      data-flow-incoming-count={incoming.length}
    >
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
    </div>
  )
}

function useFlowGeometry(
  canvasRef: RefObject<HTMLDivElement | null>,
  flow: PRLifecycleFlow,
  layout: FlowLayout,
): FlowGeometry {
  const [geometry, setGeometry] = useState<FlowGeometry>({
    edges: [],
    height: 0,
    width: 0,
  })
  const signatureRef = useRef("")

  useLayoutEffect(() => {
    const canvas = canvasRef.current
    if (!canvas) return

    let animationFrame: number | undefined
    const measure = () => {
      const canvasRect = canvas.getBoundingClientRect()
      const cells = Array.from(
        canvas.querySelectorAll<HTMLElement>("[data-flow-node-cell]"),
      )
      const cellByID = new Map(
        cells.flatMap((cell) => {
          const nodeID = cell.dataset.flowNodeCell
          return nodeID ? [[nodeID, cell] as const] : []
        }),
      )
      const cellRect = new Map(
        cells.map((cell) => [cell, cell.getBoundingClientRect()]),
      )
      const nodeByID = new Map(
        Array.from(
          canvas.querySelectorAll<HTMLElement>("[data-flow-node-id]"),
        ).flatMap((node) => {
          const nodeID = node.dataset.flowNodeId
          return nodeID ? [[nodeID, node] as const] : []
        }),
      )
      const measurementByID = new Map<
        string,
        {
          hasRowsAbove: boolean
          hasRowsBelow: boolean
          nodeRect: DOMRect
          rowBottom: number
          rowTop: number
        }
      >()

      for (const [nodeID, node] of nodeByID) {
        const cell = cellByID.get(nodeID)
        if (!cell) continue
        const measuredCell = cellRect.get(cell)!
        const bandCells = Array.from(
          cell.parentElement?.querySelectorAll<HTMLElement>(
            ":scope > [data-flow-node-cell]",
          ) ?? [],
        )
        const rowCells = bandCells.filter(
          (candidate) =>
            Math.abs(cellRect.get(candidate)!.top - measuredCell.top) <= 1,
        )
        const rowNodeRects = rowCells.map((candidate) => {
          const candidateID = candidate.dataset.flowNodeCell
          return candidateID
            ? (nodeByID.get(candidateID)?.getBoundingClientRect() ??
                cellRect.get(candidate)!)
            : cellRect.get(candidate)!
        })
        measurementByID.set(nodeID, {
          hasRowsAbove: bandCells.some(
            (candidate) => cellRect.get(candidate)!.top < measuredCell.top - 1,
          ),
          hasRowsBelow: bandCells.some(
            (candidate) => cellRect.get(candidate)!.top > measuredCell.top + 1,
          ),
          nodeRect: node.getBoundingClientRect(),
          rowBottom:
            Math.max(...rowNodeRects.map((candidate) => candidate.bottom)) -
            canvasRect.top,
          rowTop:
            Math.min(...rowNodeRects.map((candidate) => candidate.top)) -
            canvasRect.top,
        })
      }

      const measuredEdges: MeasuredFlowEdge[] = []
      const edgeOrder = new Map(
        flow.edges.map((edge, index) => [flowEdgeKey(edge), index]),
      )
      const availableTrack = (
        tracks: Array<Array<[number, number]>>,
        interval: [number, number],
      ) => {
        const existing = tracks.findIndex((track) =>
          track.every(
            ([start, end]) => end < interval[0] - 2 || start > interval[1] + 2,
          ),
        )
        return existing === -1 ? tracks.length : existing
      }
      const centerXForNode = (nodeID: string) => {
        const measured = measurementByID.get(nodeID)
        return measured
          ? measured.nodeRect.left -
              canvasRect.left +
              measured.nodeRect.width / 2
          : canvasRect.width / 2
      }
      const laneOffset = (available: number, index: number, count: number) => {
        const usable = Math.max(8, available)
        if (count <= 1) return Math.max(4, Math.min(12, usable / 2))
        const first = Math.max(4, Math.min(10, usable / 3))
        const last = Math.max(
          first,
          Math.min(first + (count - 1) * 10, usable - 10),
        )
        return first + ((last - first) * index) / (count - 1)
      }
      const nextNodeTopAfter = (y: number) => {
        const candidates = [...measurementByID.values()]
          .map((measurement) => measurement.nodeRect.top - canvasRect.top)
          .filter((top) => top > y + 1)
        return candidates.length > 0
          ? Math.min(...candidates)
          : canvasRect.height
      }
      const previousNodeBottomBefore = (y: number) => {
        const candidates = [...measurementByID.values()]
          .map((measurement) => measurement.nodeRect.bottom - canvasRect.top)
          .filter((bottom) => bottom < y - 1)
        return candidates.length > 0 ? Math.max(...candidates) : 0
      }
      const fieldLeft = Math.min(
        ...[...measurementByID.values()].map(
          (measurement) => measurement.nodeRect.left - canvasRect.left,
        ),
      )
      const fieldRight = Math.max(
        ...[...measurementByID.values()].map(
          (measurement) => measurement.nodeRect.right - canvasRect.left,
        ),
      )
      const preferredSideForEdge = (edge: PRLifecycleFlowEdge) => {
        const outgoing = layout.outgoing.get(edge.from) ?? []
        const edgeIndex = Math.max(
          0,
          outgoing.findIndex(
            (candidate) => flowEdgeKey(candidate) === flowEdgeKey(edge),
          ),
        )
        return flowPreferredGutterSide(
          centerXForNode(edge.from),
          centerXForNode(edge.to),
          edgeIndex,
          canvasRect.width,
        )
      }
      const loopTrackPlans = {
        left: [] as Array<Array<[number, number]>>,
        right: [] as Array<Array<[number, number]>>,
      }
      const loopPlans = new Map<
        string,
        { interval: [number, number]; side: "left" | "right"; track: number }
      >()
      // Return rails claim the outer tracks first. Their broad intervals keep
      // later forward detours from being painted on top of a back edge.
      const loopCandidates = flow.edges
        .filter((edge) => edge.loop)
        .flatMap((edge) => {
          const source = measurementByID.get(edge.from)
          const target = measurementByID.get(edge.to)
          if (!source || !target) return []
          const interval: [number, number] = [
            previousNodeBottomBefore(target.rowTop),
            nextNodeTopAfter(source.rowBottom),
          ]
          return [{ edge, interval }]
        })
        .sort((left, right) => {
          const spanDifference =
            right.interval[1] -
            right.interval[0] -
            (left.interval[1] - left.interval[0])
          if (Math.abs(spanDifference) > 1) return spanDifference
          return (
            (edgeOrder.get(flowEdgeKey(left.edge)) ?? 0) -
            (edgeOrder.get(flowEdgeKey(right.edge)) ?? 0)
          )
        })

      for (const { edge, interval } of loopCandidates) {
        const sourceX = centerXForNode(edge.from)
        const targetX = centerXForNode(edge.to)
        const preferredSide = preferredSideForEdge(edge)
        const choices = (["left", "right"] as const).map((side) => {
          const track = availableTrack(loopTrackPlans[side], interval)
          const railX =
            side === "left" ? 5 + track * 8 : canvasRect.width - 5 - track * 8
          const endpointTravel =
            Math.abs(sourceX - railX) + Math.abs(targetX - railX)
          const clearance =
            side === "left" ? fieldLeft - railX : railX - fieldRight
          const intrusionPenalty =
            clearance < 4 ? 100_000 + (4 - clearance) * 1_000 : 0
          const trackPenalty =
            track * Math.max(56, Math.min(104, canvasRect.width * 0.08))
          const preferencePenalty = side === preferredSide ? 0 : 0.25
          return {
            score:
              intrusionPenalty +
              endpointTravel +
              trackPenalty +
              preferencePenalty,
            side,
            track,
          }
        })
        choices.sort((left, right) => {
          if (Math.abs(left.score - right.score) > 0.1) {
            return left.score - right.score
          }
          const preferLeft = (edgeOrder.get(flowEdgeKey(edge)) ?? 0) % 2 === 0
          return left.side === (preferLeft ? "left" : "right") ? -1 : 1
        })
        const selected = choices[0]
        loopTrackPlans[selected.side][selected.track] ??= []
        loopTrackPlans[selected.side][selected.track].push(interval)
        loopPlans.set(flowEdgeKey(edge), {
          interval,
          side: selected.side,
          track: selected.track,
        })
      }

      const gutterPlans = new Map(loopPlans)
      // Plan every remaining gutter route before assigning source ports. A
      // route must keep the same side and track in every per-edge calculation.
      const plannedTracks = {
        left: loopTrackPlans.left.map((track) => [...track]),
        right: loopTrackPlans.right.map((track) => [...track]),
      }
      const plannedSourceSides = {
        left: new Map<string, number>(),
        right: new Map<string, number>(),
      }
      for (const edge of flow.edges.filter((candidate) => candidate.loop)) {
        const side = loopPlans.get(flowEdgeKey(edge))?.side
        if (!side) continue
        plannedSourceSides[side].set(
          edge.from,
          (plannedSourceSides[side].get(edge.from) ?? 0) + 1,
        )
      }
      const needsMeasuredGutter = (edge: PRLifecycleFlowEdge) => {
        if (edge.loop) return true
        const source = measurementByID.get(edge.from)
        const target = measurementByID.get(edge.to)
        if (!source || !target) return false
        const sourceRank = layout.rankByNode.get(edge.from) ?? 0
        const targetRank = layout.rankByNode.get(edge.to) ?? sourceRank + 1
        return (
          targetRank - sourceRank > 1 ||
          source.hasRowsBelow ||
          target.hasRowsAbove
        )
      }
      const forwardGutterCandidates = flow.edges
        .filter((edge) => !edge.loop && needsMeasuredGutter(edge))
        .flatMap((edge) => {
          const source = measurementByID.get(edge.from)
          const target = measurementByID.get(edge.to)
          if (!source || !target) return []
          const interval: [number, number] = [
            Math.min(source.rowBottom, target.rowTop),
            Math.max(source.rowBottom, target.rowTop),
          ]
          return [{ edge, interval }]
        })
        .sort((left, right) => {
          const spanDifference =
            right.interval[1] -
            right.interval[0] -
            (left.interval[1] - left.interval[0])
          if (Math.abs(spanDifference) > 1) return spanDifference
          return (
            (edgeOrder.get(flowEdgeKey(left.edge)) ?? 0) -
            (edgeOrder.get(flowEdgeKey(right.edge)) ?? 0)
          )
        })

      for (const { edge, interval } of forwardGutterCandidates) {
        const sourceX = centerXForNode(edge.from)
        const targetX = centerXForNode(edge.to)
        const preferredSide = preferredSideForEdge(edge)
        const choices = (["left", "right"] as const).map((side) => {
          const track = availableTrack(plannedTracks[side], interval)
          const railX =
            side === "left" ? 5 + track * 8 : canvasRect.width - 5 - track * 8
          const clearance =
            side === "left" ? fieldLeft - railX : railX - fieldRight
          const intrusionPenalty =
            clearance < 4 ? 100_000 + (4 - clearance) * 1_000 : 0
          const endpointTravel =
            Math.abs(sourceX - railX) + Math.abs(targetX - railX)
          const trackPenalty = track * 64
          const sourceFanoutPenalty =
            (plannedSourceSides[side].get(edge.from) ?? 0) *
            Math.max(320, canvasRect.width)
          const preferencePenalty = side === preferredSide ? 0 : 0.25
          return {
            score:
              intrusionPenalty +
              endpointTravel * 0.25 +
              trackPenalty +
              sourceFanoutPenalty +
              preferencePenalty,
            side,
            track,
          }
        })
        choices.sort((left, right) => {
          if (Math.abs(left.score - right.score) > 0.1) {
            return left.score - right.score
          }
          const preferLeft = (edgeOrder.get(flowEdgeKey(edge)) ?? 0) % 2 === 0
          return left.side === (preferLeft ? "left" : "right") ? -1 : 1
        })
        const selected = choices[0]
        plannedTracks[selected.side][selected.track] ??= []
        plannedTracks[selected.side][selected.track].push(interval)
        plannedSourceSides[selected.side].set(
          edge.from,
          (plannedSourceSides[selected.side].get(edge.from) ?? 0) + 1,
        )
        gutterPlans.set(flowEdgeKey(edge), {
          interval,
          side: selected.side,
          track: selected.track,
        })
      }

      const orderedEdges = [...flow.edges].sort((left, right) => {
        if (Boolean(left.loop) !== Boolean(right.loop)) {
          return left.loop ? -1 : 1
        }
        if (left.loop && right.loop) {
          const leftPlan = loopPlans.get(flowEdgeKey(left))
          const rightPlan = loopPlans.get(flowEdgeKey(right))
          const leftSpan = leftPlan
            ? leftPlan.interval[1] - leftPlan.interval[0]
            : 0
          const rightSpan = rightPlan
            ? rightPlan.interval[1] - rightPlan.interval[0]
            : 0
          if (Math.abs(leftSpan - rightSpan) > 1) return rightSpan - leftSpan
        }
        return (
          (edgeOrder.get(flowEdgeKey(left)) ?? 0) -
          (edgeOrder.get(flowEdgeKey(right)) ?? 0)
        )
      })

      for (const edge of orderedEdges) {
        const sourceMeasurement = measurementByID.get(edge.from)
        const targetMeasurement = measurementByID.get(edge.to)
        if (!sourceMeasurement || !targetMeasurement) continue

        const sourceRect = sourceMeasurement.nodeRect
        const targetRect = targetMeasurement.nodeRect
        const outgoing = layout.outgoing.get(edge.from) ?? []
        const sourceCenterX =
          sourceRect.left - canvasRect.left + sourceRect.width / 2
        let endX = targetRect.left - canvasRect.left + targetRect.width / 2
        const endY = targetRect.top - canvasRect.top
        const sourceRank = layout.rankByNode.get(edge.from) ?? 0
        const sourceRowBottom = sourceMeasurement.rowBottom
        const targetRowTop = targetMeasurement.rowTop
        const targetXFor = (candidate: PRLifecycleFlowEdge) => {
          return centerXForNode(candidate.to)
        }
        const needsGutterFor = (candidate: PRLifecycleFlowEdge) => {
          if (candidate.loop) return true
          const candidateRank =
            layout.rankByNode.get(candidate.to) ?? sourceRank + 1
          if (
            candidateRank - sourceRank > 1 ||
            sourceMeasurement.hasRowsBelow
          ) {
            return true
          }
          return measurementByID.get(candidate.to)?.hasRowsAbove ?? false
        }
        const gutterSideFor = (candidate: PRLifecycleFlowEdge) => {
          return (
            gutterPlans.get(flowEdgeKey(candidate))?.side ??
            preferredSideForEdge(candidate)
          )
        }
        const portGroupFor = (candidate: PRLifecycleFlowEdge) => {
          if (!needsGutterFor(candidate)) return 1
          return gutterSideFor(candidate) === "left" ? 0 : 2
        }
        const portOrderedOutgoing = [...outgoing].sort((left, right) => {
          const leftGroup = portGroupFor(left)
          const rightGroup = portGroupFor(right)
          const groupDifference = leftGroup - rightGroup
          if (groupDifference !== 0) return groupDifference
          if (leftGroup !== 1) {
            const leftPlan = gutterPlans.get(flowEdgeKey(left))
            const rightPlan = gutterPlans.get(flowEdgeKey(right))
            const trackDifference =
              (leftPlan?.track ?? 0) - (rightPlan?.track ?? 0)
            if (trackDifference !== 0) {
              return leftGroup === 0 ? trackDifference : -trackDifference
            }
          }
          const targetDifference = targetXFor(left) - targetXFor(right)
          if (Math.abs(targetDifference) > 1) return targetDifference
          return (
            outgoing.findIndex(
              (candidate) => flowEdgeKey(candidate) === flowEdgeKey(left),
            ) -
            outgoing.findIndex(
              (candidate) => flowEdgeKey(candidate) === flowEdgeKey(right),
            )
          )
        })
        const portIndex = Math.max(
          0,
          portOrderedOutgoing.findIndex(
            (candidate) => flowEdgeKey(candidate) === flowEdgeKey(edge),
          ),
        )
        const split = outgoing.length > 1
        const sourcePortSpan = Math.max(24, Math.min(96, sourceRect.width - 32))
        const sourcePortOffset =
          outgoing.length > 1
            ? (portIndex - (outgoing.length - 1) / 2) *
              Math.min(36, sourcePortSpan / (outgoing.length - 1))
            : 0
        const startX = sourceCenterX + sourcePortOffset
        const startY = sourceRect.bottom - canvasRect.top
        const needsGutter = needsGutterFor(edge)
        const gutterSide = needsGutter ? gutterSideFor(edge) : undefined
        const gutterPeers = portOrderedOutgoing.filter(needsGutterFor)
        const gutterPeerIndex = Math.max(
          0,
          gutterPeers.findIndex(
            (candidate) => flowEdgeKey(candidate) === flowEdgeKey(edge),
          ),
        )

        if (edge.loop) {
          const loopPlan = loopPlans.get(flowEdgeKey(edge))
          if (!loopPlan) continue
          const targetSidePeers = flow.edges.filter(
            (candidate) =>
              candidate.loop &&
              candidate.to === edge.to &&
              loopPlans.get(flowEdgeKey(candidate))?.side === loopPlan.side,
          )
          const targetPeerIndex = Math.max(
            0,
            targetSidePeers.findIndex(
              (candidate) => flowEdgeKey(candidate) === flowEdgeKey(edge),
            ),
          )
          const targetInset = Math.min(
            16 + targetPeerIndex * 12,
            Math.max(16, targetRect.width / 2 - 16),
          )
          endX =
            loopPlan.side === "left"
              ? targetRect.left - canvasRect.left + targetInset
              : targetRect.right - canvasRect.left - targetInset
          const sourceGap = nextNodeTopAfter(sourceRowBottom) - sourceRowBottom
          const targetGap =
            targetRowTop - previousNodeBottomBefore(targetRowTop)
          const sourceShelfOffset = laneOffset(
            sourceGap,
            gutterPeerIndex,
            Math.max(1, gutterPeers.length),
          )
          const sourceShelfY = sourceRowBottom + sourceShelfOffset
          const loopSourcePeers = portOrderedOutgoing.filter(
            (candidate) => candidate.loop,
          )
          const loopSourcePeerIndex = Math.max(
            0,
            loopSourcePeers.findIndex(
              (candidate) => flowEdgeKey(candidate) === flowEdgeKey(edge),
            ),
          )
          const labelOffset = split
            ? Math.max(
                10,
                sourceGap -
                  10 -
                  (loopSourcePeers.length - loopSourcePeerIndex - 1) * 20,
              )
            : sourceShelfOffset
          const targetShelfY =
            targetRowTop -
            laneOffset(
              targetGap,
              targetPeerIndex,
              Math.max(1, targetSidePeers.length),
            )
          const trackX =
            loopPlan.side === "left"
              ? 5 + loopPlan.track * 8
              : canvasRect.width - 5 - loopPlan.track * 8
          measuredEdges.push({
            edge,
            endX,
            endY,
            labelX: trackX,
            labelY: sourceRowBottom + labelOffset,
            path: flowPath([
              [startX, startY],
              [startX, sourceShelfY],
              [trackX, sourceShelfY],
              [trackX, targetShelfY],
              [endX, targetShelfY],
              [endX, endY],
            ]),
            startX,
            startY,
          })
          continue
        }

        const merged = (layout.incoming.get(edge.to) ?? []).length > 1
        const pathEndY = merged ? endY - 10 : endY
        const sourceGap = nextNodeTopAfter(sourceRowBottom) - sourceRowBottom
        const upperY = Math.min(
          sourceRowBottom +
            (needsGutter
              ? laneOffset(
                  sourceGap,
                  gutterPeerIndex,
                  Math.max(1, gutterPeers.length),
                )
              : 4),
          pathEndY - 12,
        )
        const lowerY = Math.max(upperY + 8, targetRowTop - (merged ? 14 : 4))
        let labelX: number
        let labelY: number
        let path: string

        if (needsGutter) {
          const side = gutterSide!
          const track = gutterPlans.get(flowEdgeKey(edge))?.track ?? 0
          const trackX =
            side === "left" ? 5 + track * 8 : canvasRect.width - 5 - track * 8
          path = flowPath([
            [startX, startY],
            [startX, upperY],
            [trackX, upperY],
            [trackX, lowerY],
            [endX, lowerY],
            [endX, pathEndY],
          ])
          if (targetMeasurement.hasRowsAbove) {
            labelX = endX
            labelY = targetRowTop - 12
          } else {
            labelX = (startX + trackX) / 2
            labelY = upperY - 7
          }
        } else {
          const middleY = upperY + (lowerY - upperY) / 2
          path = flowCurvePath(
            startX,
            startY,
            upperY,
            middleY,
            endX,
            lowerY,
            pathEndY,
          )
          labelX = (startX + endX) / 2
          labelY = middleY - 7
        }

        if (split) {
          const incoming = layout.incoming.get(edge.to) ?? []
          const labeledIncoming = incoming.filter(
            (candidate) =>
              (layout.outgoing.get(candidate.from) ?? []).length > 1,
          )
          const placement = flowIncomingLabelPlacement(
            labeledIncoming,
            edge,
            canvasRect.width,
            endX,
          )
          labelX = placement.x
          labelY = targetRowTop - (merged ? 30 + placement.row * 22 : 12)
        }

        measuredEdges.push({
          edge,
          endX,
          endY,
          labelX,
          labelY,
          path,
          startX,
          startY,
        })
      }

      const next: FlowGeometry = {
        edges: measuredEdges,
        height: canvasRect.height,
        width: canvasRect.width,
      }
      const signature = JSON.stringify([
        roundFlowCoordinate(next.width),
        roundFlowCoordinate(next.height),
        ...next.edges.flatMap((edge) => [
          flowEdgeKey(edge.edge),
          edge.path,
          roundFlowCoordinate(edge.labelX),
          roundFlowCoordinate(edge.labelY),
        ]),
      ])
      if (signature === signatureRef.current) return
      signatureRef.current = signature
      setGeometry(next)
    }
    const scheduleMeasure = () => {
      if (animationFrame !== undefined) {
        window.cancelAnimationFrame(animationFrame)
      }
      animationFrame = window.requestAnimationFrame(measure)
    }

    measure()
    const observer =
      typeof ResizeObserver === "undefined"
        ? undefined
        : new ResizeObserver(scheduleMeasure)
    observer?.observe(canvas)
    for (const cell of canvas.querySelectorAll<HTMLElement>(
      "[data-flow-node-cell]",
    )) {
      observer?.observe(cell)
    }
    window.addEventListener("resize", scheduleMeasure)
    return () => {
      observer?.disconnect()
      window.removeEventListener("resize", scheduleMeasure)
      if (animationFrame !== undefined) {
        window.cancelAnimationFrame(animationFrame)
      }
    }
  }, [canvasRef, flow, layout])

  return geometry
}

function flowRouteTone(edge: PRLifecycleFlowEdge): FlowRouteTone {
  if (edge.loop) return "return"
  return edge.mode
}

function FlowEdgeOverlay({
  flow,
  geometry,
  instanceID,
  layout,
}: {
  flow: PRLifecycleFlow
  geometry: FlowGeometry
  instanceID: string
  layout: FlowLayout
}) {
  const markerIDByTone = new Map(
    flowRouteTones.map((tone) => [
      tone,
      `${instanceID}-${flow.id}-${tone}-edge-arrow`,
    ]),
  )
  const measuredByKey = new Map(
    geometry.edges.map((edge) => [flowEdgeKey(edge.edge), edge]),
  )
  const mergeTargets = [...layout.incoming.entries()].filter(
    ([, edges]) => edges.length > 1,
  )
  const forwardEdges = flow.edges.filter((edge) => !edge.loop)
  const loopEdges = flow.edges.filter((edge) => edge.loop)
  const visibleEdges = [...loopEdges, ...forwardEdges]

  return (
    <>
      <svg
        aria-hidden="true"
        className="pointer-events-none absolute inset-0 z-0 size-full overflow-hidden"
        data-flow-edge-overlay
        preserveAspectRatio="none"
        viewBox={`0 0 ${Math.max(1, geometry.width)} ${Math.max(1, geometry.height)}`}
      >
        <defs>
          {flowRouteTones.map((tone) => (
            <marker
              data-flow-arrow-marker={tone}
              data-flow-tone={tone}
              id={markerIDByTone.get(tone)}
              key={tone}
              markerHeight="8"
              markerUnits="userSpaceOnUse"
              markerWidth="8"
              orient="auto"
              refX="7"
              refY="4"
              viewBox="0 0 8 8"
            >
              <path
                className="flow-route-fill"
                d="M 0 0 L 8 4 L 0 8 z"
                data-flow-arrowhead={tone}
                data-flow-tone={tone}
              />
            </marker>
          ))}
        </defs>
        {visibleEdges.map((edge) => {
          const key = flowEdgeKey(edge)
          const tone = flowRouteTone(edge)
          const measured = measuredByKey.get(key)
          const outgoing = layout.outgoing.get(edge.from) ?? []
          const branched = outgoing.length > 1
          const merged =
            !edge.loop && (layout.incoming.get(edge.to) ?? []).length > 1
          const label = branched ? (edge.label ?? "Primary") : undefined
          return (
            <g
              data-flow-edge-layer={key}
              data-flow-tone={tone}
              key={`path-${key}`}
            >
              {measured && !measured.path.includes(" C ") ? (
                <path
                  className="stroke-background fill-none"
                  d={measured.path}
                  data-flow-edge-halo={key}
                  strokeLinecap="round"
                  strokeLinejoin="round"
                  strokeWidth={edge.mode === "parallel" ? "7" : "6"}
                  vectorEffect="non-scaling-stroke"
                />
              ) : null}
              <path
                className={cn(
                  "flow-route-color fill-none stroke-current",
                  edge.mode === "optional" && "[stroke-dasharray:5_4]",
                  !measured && "opacity-0",
                )}
                d={measured?.path ?? "M 0 0"}
                data-flow-branch={branched ? edge.from : undefined}
                data-flow-branch-edge={label}
                data-flow-branch-target={branched ? edge.to : undefined}
                data-flow-edge-key={key}
                data-flow-launch={branched ? true : undefined}
                data-flow-launch-target={branched ? edge.to : undefined}
                data-flow-loop={edge.loop ? "true" : undefined}
                data-flow-loop-target={edge.loop ? edge.to : undefined}
                data-flow-optional={
                  edge.mode === "optional" ? "true" : undefined
                }
                data-flow-parallel={
                  edge.mode === "parallel" ? "true" : undefined
                }
                data-flow-route-mode={edge.mode}
                data-flow-shape={
                  edge.loop
                    ? "back-edge"
                    : measured?.path.includes(" C ")
                      ? "curve"
                      : "orthogonal"
                }
                data-flow-source={edge.from}
                data-flow-target={edge.to}
                data-flow-tone={tone}
                data-flow-visible-edge-key={key}
                markerEnd={
                  merged ? undefined : `url(#${markerIDByTone.get(tone)})`
                }
                strokeLinecap="round"
                strokeLinejoin="round"
                strokeWidth={edge.mode === "parallel" ? "2.5" : "1.5"}
                vectorEffect="non-scaling-stroke"
              />
            </g>
          )
        })}
        {mergeTargets.map(([targetID, edges]) => {
          const measured = measuredByKey.get(flowEdgeKey(edges[0]))
          if (!measured) return null
          const size = 7
          const diamondY = measured.endY - 10
          return (
            <g data-flow-tone="merge" key={targetID}>
              <line
                className="flow-route-color stroke-current"
                data-flow-merge-stem={targetID}
                data-flow-tone="merge"
                strokeWidth="1.5"
                vectorEffect="non-scaling-stroke"
                x1={measured.endX}
                x2={measured.endX}
                y1={diamondY + 5}
                y2={measured.endY}
              />
              <rect
                className="flow-route-label-surface"
                data-flow-merge-diamond={targetID}
                data-flow-tone="merge"
                height={size}
                strokeWidth="1.5"
                transform={`rotate(45 ${measured.endX} ${diamondY})`}
                vectorEffect="non-scaling-stroke"
                width={size}
                x={measured.endX - size / 2}
                y={diamondY - size / 2}
              />
            </g>
          )
        })}
        {visibleEdges.map((edge) => {
          const measured = measuredByKey.get(flowEdgeKey(edge))
          const branched = (layout.outgoing.get(edge.from) ?? []).length > 1
          const label = branched ? (edge.label ?? "Primary") : undefined
          return label && measured ? (
            <FlowEdgeLabel
              edge={edge}
              key={`label-${flowEdgeKey(edge)}`}
              label={label}
              width={geometry.width}
              x={measured.labelX}
              y={measured.labelY}
            />
          ) : null
        })}
      </svg>
      <div
        aria-label={`${flow.title} connections`}
        className="sr-only"
        role="list"
      >
        {visibleEdges.map((edge) => {
          const source = layout.nodeByID.get(edge.from)!
          const target = layout.nodeByID.get(edge.to)!
          const merged =
            !edge.loop && (layout.incoming.get(edge.to) ?? []).length > 1
          return (
            <span key={flowEdgeKey(edge)} role="listitem">
              {edge.label ? `${edge.label}: ` : ""}
              {edge.loop ? (
                <>
                  {source.title} returns to {target.title}
                </>
              ) : (
                <>
                  {source.title} {flowRouteAccessibleRelation(edge.mode)}{" "}
                  {target.title}
                </>
              )}
              {merged ? ", where it merges with other routes" : ""}
            </span>
          )
        })}
      </div>
    </>
  )
}

function FlowEdgeLabel({
  edge,
  label,
  width,
  x,
  y,
}: {
  edge: PRLifecycleFlowEdge
  label: string
  width: number
  x: number
  y: number
}) {
  const labelWidth = flowEdgeLabelWidth(label)
  const tone = flowRouteTone(edge)
  const centerX = Math.max(
    labelWidth / 2 + 2,
    Math.min(width - labelWidth / 2 - 2, x),
  )
  return (
    <g
      data-flow-edge-key={flowEdgeKey(edge)}
      data-flow-launch-label
      data-flow-route-mode={edge.mode}
      data-flow-source={edge.from}
      data-flow-target={edge.to}
      data-flow-tone={tone}
      transform={`translate(${centerX} ${y})`}
    >
      <rect
        className="flow-route-label-surface"
        data-flow-label-surface={tone}
        data-flow-tone={tone}
        height="18"
        rx="4"
        width={labelWidth}
        x={-labelWidth / 2}
        y="-9"
      />
      <text
        className="flow-route-label-text text-[10px] font-semibold"
        data-flow-label-text={tone}
        data-flow-tone={tone}
        dominantBaseline="central"
        textAnchor="middle"
      >
        {label}
      </text>
    </g>
  )
}

function flowEdgeLabelWidth(label: string): number {
  return Math.max(36, Math.min(120, label.length * 6.2 + 14))
}

function flowPreferredGutterSide(
  sourceX: number,
  targetX: number,
  edgeIndex: number,
  canvasWidth: number,
): "left" | "right" {
  if (Math.abs(sourceX - targetX) < 32) {
    return edgeIndex % 2 === 0 ? "left" : "right"
  }
  return sourceX < canvasWidth / 2 ? "left" : "right"
}

function flowIncomingLabelPlacement(
  incoming: PRLifecycleFlowEdge[],
  selected: PRLifecycleFlowEdge,
  canvasWidth: number,
  anchorX: number,
): { row: number; x: number } {
  const gap = 8
  const availableWidth = Math.max(36, canvasWidth - 8)
  const rows: Array<{
    items: Array<{ key: string; width: number }>
    width: number
  }> = []

  for (const edge of incoming) {
    const item = {
      key: flowEdgeKey(edge),
      width: flowEdgeLabelWidth(edge.label ?? "Primary"),
    }
    let row = rows.at(-1)
    if (
      !row ||
      (row.items.length > 0 && row.width + gap + item.width > availableWidth)
    ) {
      row = { items: [], width: 0 }
      rows.push(row)
    }
    if (row.items.length > 0) row.width += gap
    row.items.push(item)
    row.width += item.width
  }

  const selectedKey = flowEdgeKey(selected)
  const rowIndex = Math.max(
    0,
    rows.findIndex((row) => row.items.some((item) => item.key === selectedKey)),
  )
  const row = rows[rowIndex]
  if (!row) return { row: 0, x: anchorX }

  const left = Math.max(
    4,
    Math.min(canvasWidth - row.width - 4, anchorX - row.width / 2),
  )
  let cursor = left
  for (const item of row.items) {
    if (item.key === selectedKey) {
      return { row: rowIndex, x: cursor + item.width / 2 }
    }
    cursor += item.width + gap
  }
  return { row: rowIndex, x: anchorX }
}

function flowPath(points: Array<[number, number]>): string {
  return points
    .map(
      ([x, y], index) =>
        `${index === 0 ? "M" : "L"} ${roundFlowCoordinate(x)} ${roundFlowCoordinate(y)}`,
    )
    .join(" ")
}

function flowCurvePath(
  startX: number,
  startY: number,
  upperY: number,
  middleY: number,
  endX: number,
  lowerY: number,
  endY: number,
): string {
  return [
    `M ${roundFlowCoordinate(startX)} ${roundFlowCoordinate(startY)}`,
    `L ${roundFlowCoordinate(startX)} ${roundFlowCoordinate(upperY)}`,
    `C ${roundFlowCoordinate(startX)} ${roundFlowCoordinate(middleY)} ${roundFlowCoordinate(endX)} ${roundFlowCoordinate(middleY)} ${roundFlowCoordinate(endX)} ${roundFlowCoordinate(lowerY)}`,
    `L ${roundFlowCoordinate(endX)} ${roundFlowCoordinate(endY)}`,
  ].join(" ")
}

function roundFlowCoordinate(value: number): number {
  return Math.round(value * 10) / 10
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
      data-flow-semantic-edge
      data-flow-source={edge.from}
      data-flow-target={edge.to}
      hidden
    />
  )
}

function flowRouteAccessibleRelation(
  mode: PRLifecycleFlowEdge["mode"],
): string {
  switch (mode) {
    case "choice":
      return "can choose the route to"
    case "parallel":
      return "must also continue to"
    case "optional":
      return "can optionally continue to"
    default:
      return "continues to"
  }
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
      className="flow-node-surface flex min-h-16 w-full min-w-0 flex-col rounded-lg border px-2.5 py-2 [overflow-wrap:anywhere] shadow-sm"
      data-flow-element="action"
      data-flow-kind="action"
      data-flow-node-id={node.id}
      data-flow-operation={node.operation}
      data-flow-tone="action"
    >
      <strong className="text-xs leading-snug">{node.title}</strong>
      <span
        className="text-muted-foreground mt-1 text-[11px] leading-snug"
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
      className="flow-node-surface flex min-h-20 w-full min-w-0 flex-col rounded-lg border-2 px-2.5 py-2 [overflow-wrap:anywhere] shadow-sm"
      data-flow-element="locked-safeguard"
      data-flow-kind="gate"
      data-flow-node-id={node.id}
      data-flow-tone="safeguard"
      data-required-gate={node.safeguard}
      role="group"
    >
      <span
        className="flow-tone-badge w-fit rounded border px-1.5 py-0.5 text-[9px] font-bold tracking-wider uppercase"
        data-flow-tone="safeguard"
      >
        Safeguard · locked
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
        "flow-node-surface focus-visible:ring-ring focus-visible:ring-offset-background relative flex min-h-20 w-full min-w-0 cursor-pointer flex-col rounded-lg border-2 px-2.5 py-2 text-left [overflow-wrap:anywhere] shadow-sm transition-[background-color,border-color,box-shadow] outline-none hover:shadow-md focus-visible:ring-2 focus-visible:ring-offset-2",
        format.format === "needs-setup" && "[border-style:dashed]",
      )}
      data-decision-point={decisionPoint}
      data-decision-title={node.title}
      data-edit-href={gateEditorHref(profileID, decisionPoint)}
      data-editor-title={node.title}
      data-flow-element="editable-gate"
      data-flow-kind="gate"
      data-flow-node-id={node.id}
      data-flow-tone={`gate-${format.format}`}
      data-gate-format={format.format}
      data-gate-id={decisionPoint}
      data-gate-name={node.title}
      data-gate-selected={selected ? "true" : undefined}
      data-workflow-configured={workflow ? "true" : "false"}
      onClick={activate}
      type="button"
    >
      <span className="flex flex-wrap items-start justify-between gap-1.5">
        <strong className="min-w-24 flex-1 text-xs leading-snug">
          {node.title}
        </strong>
        <span
          className={cn(
            "flow-tone-badge shrink-0 rounded-md border px-1.5 py-0.5 text-[9px] font-bold tracking-wider uppercase",
          )}
          data-flow-tone={`gate-${format.format}`}
        >
          {format.label} gate
        </span>
      </span>
      <span
        className="text-muted-foreground mt-1 text-[11px] leading-snug"
        data-gate-description
        id={descriptionID}
      >
        {node.description}
      </span>
      {format.fallback ? (
        <span className="mt-auto flex flex-wrap items-center gap-1.5 pt-1.5">
          <span className="text-muted-foreground text-[9px] font-semibold tracking-wider uppercase">
            default fallback
          </span>
        </span>
      ) : null}
      {format.composition ? (
        <span className="text-foreground mt-1 text-[10px] font-semibold">
          {format.composition}
        </span>
      ) : null}
      <span className="sr-only" id={formatID}>
        {format.accessible}
      </span>
    </button>
  )
}

/**
 * Assigns forward nodes to compact topological bands. A band contains only
 * nodes that actually run at that depth, so a route that has ended cannot keep
 * an empty column alive below it. Measured edges preserve exact lineage while
 * later bands independently reuse the available width.
 */
function createFlowLayout(flow: PRLifecycleFlow): FlowLayout {
  const nodeByID = new Map(flow.nodes.map((node) => [node.id, node]))
  const nodeOrder = new Map(flow.nodes.map((node, index) => [node.id, index]))
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
    .filter((node) => (indegree.get(node.id) ?? 0) === 0)
    .sort((left, right) => {
      if (left.id === flow.entry) return -1
      if (right.id === flow.entry) return 1
      return (nodeOrder.get(left.id) ?? 0) - (nodeOrder.get(right.id) ?? 0)
    })
  for (const node of pending) rankByNode.set(node.id, 0)

  const visited = new Set<string>()
  while (pending.length > 0) {
    const current = pending.shift()!
    visited.add(current.id)
    const currentRank = rankByNode.get(current.id) ?? 0
    for (const targetID of adjacency.get(current.id) ?? []) {
      rankByNode.set(
        targetID,
        Math.max(rankByNode.get(targetID) ?? 0, currentRank + 1),
      )
      const remaining = (indegree.get(targetID) ?? 0) - 1
      indegree.set(targetID, remaining)
      if (remaining !== 0) continue
      pending.push(nodeByID.get(targetID)!)
      pending.sort(
        (left, right) =>
          (nodeOrder.get(left.id) ?? 0) - (nodeOrder.get(right.id) ?? 0),
      )
    }
  }

  let fallbackRank = Math.max(0, ...rankByNode.values()) + 1
  for (const node of flow.nodes) {
    if (visited.has(node.id)) continue
    rankByNode.set(node.id, fallbackRank)
    fallbackRank += 1
  }

  const rawRanks = [...new Set(rankByNode.values())].sort(
    (left, right) => left - right,
  )
  const compactRank = new Map(rawRanks.map((rank, index) => [rank, index]))
  for (const [nodeID, rank] of rankByNode) {
    rankByNode.set(nodeID, compactRank.get(rank)!)
  }

  const bands = Array.from({ length: rawRanks.length }, (_, rank) =>
    flow.nodes
      .filter((node) => rankByNode.get(node.id) === rank)
      .sort(
        (left, right) =>
          (nodeOrder.get(left.id) ?? 0) - (nodeOrder.get(right.id) ?? 0),
      ),
  ).filter((nodes) => nodes.length > 0)

  const horizontalPosition = new Map<string, number>()
  for (const nodes of bands) {
    const predecessorPosition = (node: PRLifecycleFlowNode) => {
      const positions = (incoming.get(node.id) ?? []).flatMap((edge) => {
        const position = horizontalPosition.get(edge.from)
        return position === undefined ? [] : [position]
      })
      return positions.length > 0
        ? positions.reduce((sum, position) => sum + position, 0) /
            positions.length
        : Number.POSITIVE_INFINITY
    }
    nodes.sort((left, right) => {
      const leftPosition = predecessorPosition(left)
      const rightPosition = predecessorPosition(right)
      if (leftPosition !== rightPosition) return leftPosition - rightPosition
      return (nodeOrder.get(left.id) ?? 0) - (nodeOrder.get(right.id) ?? 0)
    })
    for (const [index, node] of nodes.entries()) {
      horizontalPosition.set(
        node.id,
        nodes.length === 1 ? 0.5 : index / (nodes.length - 1),
      )
    }
  }

  return { bands, incoming, nodeByID, outgoing, rankByNode }
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

function flowBandGridClass(count: number): string {
  return cn(
    "grid min-w-0 grid-cols-1 items-start gap-x-3 gap-y-10",
    count === 2 && "@xl/flow:grid-cols-2",
    count === 3 && "@md/flow:grid-cols-3",
    count === 4 && "@3xl/flow:grid-cols-4",
    count === 5 && "@5xl/flow:grid-cols-5",
    count === 6 && "@7xl/flow:grid-cols-6",
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

function Legend({
  label,
  variant,
}: {
  label: string
  variant: "action" | "gate" | "required"
}) {
  const tone =
    variant === "action"
      ? "action"
      : variant === "gate"
        ? "editable-gate"
        : "safeguard"
  return (
    <span
      className="flow-tone-badge rounded-md border px-2 py-1"
      data-flow-legend="element"
      data-flow-tone={tone}
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
    { format: "needs-setup", label: "Needs setup" },
  ]

  return (
    <div
      aria-label="Gate format legend"
      className="text-muted-foreground flex flex-wrap items-center gap-1 text-[10px]"
    >
      <span className="mr-1 font-semibold">Gate format</span>
      {formats.map(({ format, label }) => (
        <span
          className="flow-tone-badge rounded-md border px-1.5 py-0.5 font-bold tracking-wider uppercase"
          data-flow-legend="gate-format"
          data-flow-tone={`gate-${format}`}
          key={format}
        >
          {label}
        </span>
      ))}
    </div>
  )
}

function FlowRouteLegend() {
  const routes: Array<{ label: string; tone: FlowRouteTone }> = [
    { label: "Next", tone: "linear" },
    { label: "Choice", tone: "choice" },
    { label: "All required", tone: "parallel" },
    { label: "Optional", tone: "optional" },
    { label: "Return", tone: "return" },
  ]
  return (
    <div
      aria-label="Route connector legend"
      className="text-muted-foreground flex flex-wrap items-center gap-x-2 gap-y-1 text-[10px]"
    >
      <span className="font-semibold">Routes</span>
      {routes.map((route) => (
        <span
          className="flex items-center gap-1"
          data-flow-legend="route"
          data-flow-tone={route.tone}
          key={route.tone}
        >
          <FlowRouteLegendSample tone={route.tone} />
          {route.label}
        </span>
      ))}
    </div>
  )
}

function FlowRouteLegendSample({ tone }: { tone: FlowRouteTone }) {
  if (tone === "return") {
    return (
      <svg
        aria-hidden="true"
        className="flow-route-color h-3 w-6 overflow-visible"
        data-flow-tone={tone}
        viewBox="0 0 24 12"
      >
        <path
          className="fill-none stroke-current"
          d="M 22 10 H 9 Q 4 10 4 5 V 3"
          strokeLinecap="round"
          strokeLinejoin="round"
          strokeWidth="1.5"
        />
        <path className="fill-current" d="M 1 4 L 4 0 L 7 4 z" />
      </svg>
    )
  }
  return (
    <svg
      aria-hidden="true"
      className="flow-route-color h-3 w-6 overflow-visible"
      data-flow-tone={tone}
      viewBox="0 0 24 12"
    >
      <path
        className={cn(
          "fill-none stroke-current",
          tone === "optional" && "[stroke-dasharray:4_3]",
        )}
        d="M 1 6 H 18"
        strokeLinecap="round"
        strokeWidth={tone === "parallel" ? "3" : "1.5"}
      />
      <path className="fill-current" d="M 17 2 L 23 6 L 17 10 z" />
    </svg>
  )
}
