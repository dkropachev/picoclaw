import { fireEvent, render, screen, within } from "@testing-library/react"
import { describe, expect, it, vi } from "vitest"

import {
  type PRLifecycleFlow,
  type PRLifecycleFlowCatalog,
  type PRLifecycleGateProfile,
} from "@/api/pr-lifecycle-gate-profiles"
import { PRLifecycleGateMap } from "@/components/pr-workspaces/pr-lifecycle-gate-map"

const reviewFlow: PRLifecycleFlow = {
  id: "review",
  title: "Review workflow",
  entry: "r_trigger",
  nodes: [
    {
      id: "r_trigger",
      kind: "action",
      title: "Receive review assignment",
      description: "GitHub asks PicoClaw to review the pull request.",
      operation: "github.review.requested",
      editable: false,
    },
    {
      id: "r_gate",
      kind: "gate",
      title: "Allow review",
      description: "Choose whether automated review may start.",
      decision_point: "pr.review.start",
      ordinal: 3,
      editable: true,
    },
    {
      id: "r_choice",
      kind: "action",
      title: "Choose review depth",
      description: "Select one review path from the approved policy.",
      operation: "pr.review.depth.route",
      editable: false,
    },
    {
      id: "r_short",
      kind: "action",
      title: "Run focused review",
      description: "Inspect the focused changed area.",
      operation: "pr.review.focused.run",
      editable: false,
    },
    {
      id: "r_long_a",
      kind: "action",
      title: "Run broad review",
      description: "Inspect all affected changed areas.",
      operation: "pr.review.broad.run",
      editable: false,
    },
    {
      id: "r_long_gate",
      kind: "gate",
      title: "Accept review coverage",
      description: "Check whether broad review covered its scope.",
      decision_point: "pr.review.complete",
      ordinal: 4,
      editable: true,
    },
    {
      id: "r_merge",
      kind: "action",
      title: "Collect findings",
      description: "Merge findings from the selected review path.",
      operation: "pr.review.findings.collect",
      editable: false,
    },
    {
      id: "r_parallel",
      kind: "action",
      title: "Process accepted findings",
      description: "Start every required finding output.",
      operation: "pr.review.findings.process",
      editable: false,
    },
    {
      id: "r_optional",
      kind: "action",
      title: "Select follow-up",
      description: "Run any applicable independent follow-up.",
      operation: "pr.review.followup.select",
      editable: false,
    },
    {
      id: "r_store",
      kind: "action",
      title: "Store findings",
      description: "Persist findings for later consumers.",
      operation: "pr.review.findings.store",
      editable: false,
    },
    {
      id: "r_notify",
      kind: "gate",
      title: "Approve notification",
      description: "Decide whether to publish review feedback.",
      decision_point: "pr.review.publish",
      ordinal: 10,
      editable: true,
    },
    {
      id: "r_archive",
      kind: "gate",
      title: "Protect audit archive",
      description: "Always preserve the immutable audit record.",
      safeguard: "audit_archive",
      editable: false,
    },
    {
      id: "r_fallback_gate",
      kind: "gate",
      title: "Approve deferred output",
      description: "Choose whether deferred output may be created.",
      decision_point: "pr.deferred.publish",
      ordinal: 12,
      editable: true,
    },
    {
      id: "r_choice_sidecar",
      kind: "action",
      title: "Record review telemetry",
      description: "Record independent review routing telemetry.",
      operation: "pr.review.telemetry.record",
      editable: false,
    },
    {
      id: "r_parallel_sidecar",
      kind: "action",
      title: "Index finding metrics",
      description: "Index optional metrics for accepted findings.",
      operation: "pr.review.metrics.index",
      editable: false,
    },
  ],
  edges: [
    {
      from: "r_trigger",
      to: "r_gate",
      mode: "linear",
      loop: false,
    },
    { from: "r_gate", to: "r_choice", mode: "linear", loop: false },
    {
      from: "r_choice",
      to: "r_short",
      mode: "choice",
      outcome: "short",
      label: "Focused",
      loop: false,
    },
    {
      from: "r_choice",
      to: "r_long_a",
      mode: "choice",
      outcome: "thorough",
      label: "Thorough",
      loop: false,
    },
    {
      from: "r_choice",
      to: "r_choice_sidecar",
      mode: "optional",
      label: "Telemetry",
      loop: false,
    },
    { from: "r_short", to: "r_merge", mode: "linear", loop: false },
    {
      from: "r_long_a",
      to: "r_long_gate",
      mode: "linear",
      loop: false,
    },
    {
      from: "r_long_gate",
      to: "r_merge",
      mode: "linear",
      loop: false,
    },
    {
      from: "r_merge",
      to: "r_parallel",
      mode: "linear",
      loop: false,
    },
    {
      from: "r_parallel",
      to: "r_optional",
      mode: "parallel",
      label: "Follow-up",
      loop: false,
    },
    {
      from: "r_parallel",
      to: "r_store",
      mode: "parallel",
      label: "Storage",
      loop: false,
    },
    {
      from: "r_parallel",
      to: "r_parallel_sidecar",
      mode: "optional",
      label: "Metrics",
      loop: false,
    },
    {
      from: "r_optional",
      to: "r_notify",
      mode: "optional",
      label: "Notify",
      loop: false,
    },
    {
      from: "r_optional",
      to: "r_archive",
      mode: "optional",
      label: "Archive",
      loop: false,
    },
    {
      from: "r_store",
      to: "r_fallback_gate",
      mode: "optional",
      loop: false,
    },
  ],
}

const implementationFlow: PRLifecycleFlow = {
  id: "implementation",
  title: "Implementation workflow",
  entry: "i_load",
  nodes: [
    {
      id: "i_load",
      kind: "action",
      title: "Load implementation batch",
      description: "Load findings selected for implementation.",
      operation: "pr.implementation.load",
      editable: false,
    },
    {
      id: "i_gate",
      kind: "gate",
      title: "Allow implementation",
      description: "Choose whether automated implementation may start.",
      decision_point: "pr.implementation.start",
      ordinal: 7,
      editable: true,
    },
    {
      id: "i_work",
      kind: "action",
      title: "Implement fixes",
      description: "Apply only the selected fixes.",
      operation: "pr.implementation.run",
      editable: false,
    },
    {
      id: "i_audit",
      kind: "action",
      title: "Audit implementation",
      description: "Check completion and candidate scope.",
      operation: "pr.implementation.audit",
      editable: false,
    },
    {
      id: "i_decide",
      kind: "action",
      title: "Route audit result",
      description: "Choose the next implementation result.",
      operation: "pr.implementation.result.route",
      editable: false,
    },
    {
      id: "i_done",
      kind: "action",
      title: "Prepare completed changes",
      description: "Prepare validated changes for approval.",
      operation: "pr.implementation.prepare",
      editable: false,
    },
    {
      id: "i_retry",
      kind: "action",
      title: "Resume implementation",
      description: "Repair incomplete work before another audit.",
      operation: "pr.implementation.resume",
      editable: false,
    },
    {
      id: "i_locked",
      kind: "gate",
      title: "Stop unsafe changes",
      description: "Prevent publication of unsafe out-of-scope code.",
      safeguard: "hard_scope",
      editable: false,
    },
    {
      id: "i_complete",
      kind: "gate",
      title: "Accept implementation",
      description: "Approve completed and in-scope changes.",
      decision_point: "pr.implementation.complete",
      ordinal: 9,
      editable: true,
    },
    {
      id: "i_metrics",
      kind: "action",
      title: "Record audit metrics",
      description: "Record optional completion audit metrics.",
      operation: "pr.implementation.metrics.record",
      editable: false,
    },
  ],
  edges: [
    { from: "i_load", to: "i_gate", mode: "linear", loop: false },
    { from: "i_gate", to: "i_work", mode: "linear", loop: false },
    { from: "i_work", to: "i_audit", mode: "linear", loop: false },
    {
      from: "i_audit",
      to: "i_decide",
      mode: "linear",
      label: "Candidate",
      loop: false,
    },
    {
      from: "i_audit",
      to: "i_metrics",
      mode: "optional",
      label: "Metrics",
      loop: false,
    },
    {
      from: "i_decide",
      to: "i_done",
      mode: "choice",
      outcome: "done",
      label: "Done",
      loop: false,
    },
    {
      from: "i_decide",
      to: "i_retry",
      mode: "choice",
      outcome: "retry",
      label: "Retry",
      loop: false,
    },
    {
      from: "i_decide",
      to: "i_locked",
      mode: "choice",
      outcome: "blocked",
      label: "Blocked",
      loop: false,
    },
    { from: "i_done", to: "i_complete", mode: "linear", loop: false },
    { from: "i_retry", to: "i_work", mode: "linear", loop: true },
  ],
}

const ownershipFlow: PRLifecycleFlow = {
  id: "implementation",
  title: "Implementation workflow",
  entry: "ownership_check",
  nodes: [
    {
      id: "ownership_check",
      kind: "action",
      title: "Check pull request ownership",
      description: "Determine whether PicoClaw may change this pull request.",
      operation: "pr.implementation.ownership.check",
      editable: false,
    },
    {
      id: "ownership_eligibility",
      kind: "gate",
      title: "Confirm implementation eligibility",
      description: "Decide whether a non-owned pull request may be changed.",
      decision_point: "pr.implementation.eligibility",
      ordinal: 6,
      editable: true,
    },
    {
      id: "ownership_start",
      kind: "gate",
      title: "Approve implementation start",
      description: "Decide whether implementation may begin.",
      decision_point: "pr.implementation.start",
      ordinal: 7,
      editable: true,
    },
    {
      id: "ownership_stop",
      kind: "action",
      title: "Stop without changes",
      description: "Leave a read-only pull request unchanged.",
      operation: "pr.implementation.stop",
      editable: false,
    },
  ],
  edges: [
    {
      from: "ownership_check",
      to: "ownership_start",
      mode: "choice",
      outcome: "owned",
      label: "Owned",
      loop: false,
    },
    {
      from: "ownership_check",
      to: "ownership_eligibility",
      mode: "choice",
      outcome: "non_owned",
      label: "Non-owned",
      loop: false,
    },
    {
      from: "ownership_check",
      to: "ownership_stop",
      mode: "choice",
      outcome: "read_only",
      label: "Read-only",
      loop: false,
    },
    {
      from: "ownership_eligibility",
      to: "ownership_start",
      mode: "linear",
      loop: false,
    },
  ],
}

const flow: PRLifecycleFlowCatalog = {
  schema: "pr-lifecycle-flow/v1",
  flows: [reviewFlow, implementationFlow],
}

const workflows: PRLifecycleGateProfile["workflows"] = {
  "pr.review.start": {
    id: "review_start",
    name: "Start review",
    purpose: "authorization",
    decision_point: "pr.review.start",
    stages: [{ id: "automatic", kind: "zero" }],
  },
  "pr.review.complete": {
    id: "review_complete",
    name: "Complete review",
    purpose: "authorization",
    decision_point: "pr.review.complete",
    stages: [
      {
        id: "policy",
        kind: "deterministic",
        title: "Check coverage",
        when: "coverage.complete",
      },
    ],
  },
  "pr.review.publish": {
    id: "review_publish",
    name: "Publish review",
    purpose: "authorization",
    decision_point: "pr.review.publish",
    stages: [
      {
        id: "reviewer",
        kind: "ai_isolated_context",
        title: "Check publication",
        agent_id: "reviewer",
        criteria: "Confirm the findings are ready to publish.",
      },
    ],
  },
  "pr.implementation.start": {
    id: "implementation_start",
    name: "Start implementation",
    purpose: "authorization",
    decision_point: "pr.implementation.start",
    stages: [
      {
        id: "approval",
        kind: "human",
        title: "Approve implementation",
        questions: ["Proceed?"],
      },
    ],
  },
  "pr.implementation.complete": {
    id: "implementation_complete",
    name: "Complete implementation",
    purpose: "authorization",
    decision_point: "pr.implementation.complete",
    stages: [
      {
        id: "scope",
        kind: "deterministic",
        title: "Check scope",
        when: "scope.accepted",
      },
      {
        id: "audit",
        kind: "ai_isolated_context",
        title: "Audit completion",
        agent_id: "controller",
        criteria: "Confirm implementation is complete.",
      },
      {
        id: "approval",
        kind: "human",
        title: "Approve changes",
        questions: ["Accept?"],
      },
    ],
  },
}

function renderMap(
  selectedDecisionPoint = "pr.review.start",
  onSelect = vi.fn(),
) {
  return render(
    <PRLifecycleGateMap
      flow={flow}
      flowRevision={`sha256:${"b".repeat(64)}`}
      onSelect={onSelect}
      profileID="strict profile"
      profileName="Strict profile"
      selectedDecisionPoint={selectedDecisionPoint}
      workflows={workflows}
    />,
  )
}

function flowTestRect(
  x: number,
  y: number,
  width: number,
  height: number,
): DOMRect {
  return {
    bottom: y + height,
    height,
    left: x,
    right: x + width,
    top: y,
    width,
    x,
    y,
    toJSON: () => ({}),
  } as DOMRect
}

function expectFlowRenderingContract(
  container: HTMLElement,
  expectedFlow: PRLifecycleFlow,
) {
  const graph = container.querySelector<HTMLElement>(
    `[data-flow-graph="${expectedFlow.id}"]`,
  )!
  const semanticEdges = Array.from(
    graph.querySelectorAll<HTMLElement>("[data-flow-edge]"),
  )
  const visibleEdges = Array.from(
    graph.querySelectorAll<HTMLElement>("[data-flow-visible-edge-key]"),
  )
  const bands = Array.from(
    graph.querySelectorAll<HTMLElement>("[data-flow-band]"),
  )
  const nodeCells = Array.from(
    graph.querySelectorAll<HTMLElement>("[data-flow-node-cell]"),
  )

  expect(bands.length).toBeGreaterThan(0)
  for (const band of bands) {
    const bandCells = band.querySelectorAll(":scope > [data-flow-node-cell]")
    expect(bandCells.length, `band ${band.dataset.flowBand}`).toBeGreaterThan(0)
    expect(band).toHaveAttribute(
      "data-flow-band-count",
      String(bandCells.length),
    )
  }
  expect(nodeCells).toHaveLength(expectedFlow.nodes.length)
  for (const expectedNode of expectedFlow.nodes) {
    const cells = nodeCells.filter(
      (cell) => cell.dataset.flowNodeCell === expectedNode.id,
    )
    expect(cells, `${expectedNode.id} node cell`).toHaveLength(1)
    expect(
      cells[0].querySelectorAll(`[data-flow-node-id="${expectedNode.id}"]`),
      `${expectedNode.id} rendered node`,
    ).toHaveLength(1)
    expect(
      cells[0].querySelectorAll("[data-flow-node-cell]"),
      `${expectedNode.id} must not recursively own later bands`,
    ).toHaveLength(0)
  }
  expect(semanticEdges).toHaveLength(expectedFlow.edges.length)
  expect(visibleEdges).toHaveLength(expectedFlow.edges.length)

  for (const expectedEdge of expectedFlow.edges) {
    const edgeKey = `${expectedEdge.from}:${expectedEdge.to}`
    const matches = semanticEdges.filter(
      (edge) => edge.dataset.flowEdgeKey === edgeKey,
    )
    expect(matches, edgeKey).toHaveLength(1)
    expect(matches[0], `${edgeKey} semantic marker`).not.toBeVisible()
    expect(matches[0], edgeKey).toHaveAttribute(
      "data-flow-source",
      expectedEdge.from,
    )
    expect(matches[0], edgeKey).toHaveAttribute(
      "data-flow-target",
      expectedEdge.to,
    )
    expect(matches[0], edgeKey).toHaveAttribute(
      "data-flow-edge",
      expectedEdge.mode,
    )
    const visibleMatches = visibleEdges.filter(
      (edge) => edge.dataset.flowVisibleEdgeKey === edgeKey,
    )
    expect(visibleMatches, `${edgeKey} visible marker`).toHaveLength(1)
    expect(visibleMatches[0], `${edgeKey} source`).toHaveAttribute(
      "data-flow-source",
      expectedEdge.from,
    )
    expect(visibleMatches[0], `${edgeKey} target`).toHaveAttribute(
      "data-flow-target",
      expectedEdge.to,
    )
    expect(visibleMatches[0], `${edgeKey} route mode`).toHaveAttribute(
      "data-flow-route-mode",
      expectedEdge.mode,
    )
    expect(visibleMatches[0].tagName.toLowerCase(), `${edgeKey} path`).toBe(
      "path",
    )
    expect(
      visibleMatches[0].getAttribute("d"),
      `${edgeKey} measured path`,
    ).toBeTruthy()
    if (expectedEdge.loop) {
      expect(visibleMatches[0], `${edgeKey} loop marker`).toHaveAttribute(
        "data-flow-loop",
        "true",
      )
      expect(visibleMatches[0], `${edgeKey} loop target`).toHaveAttribute(
        "data-flow-loop-target",
        expectedEdge.to,
      )
      expect(visibleMatches[0], `${edgeKey} back-edge shape`).toHaveAttribute(
        "data-flow-shape",
        "back-edge",
      )
      expect(visibleMatches[0], `${edgeKey} return arrow`).toHaveAttribute(
        "marker-end",
      )
    } else {
      expect(visibleMatches[0], `${edgeKey} is not a loop`).not.toHaveAttribute(
        "data-flow-loop",
      )
    }
  }

  expect(graph.querySelector("[data-flow-loop-connector]")).toBeNull()

  const connectionItems = Array.from(
    graph.querySelectorAll<HTMLElement>(
      `[aria-label="${expectedFlow.title} connections"] [role="listitem"]`,
    ),
  )
  expect(connectionItems).toHaveLength(expectedFlow.edges.length)
  for (const expectedEdge of expectedFlow.edges.filter((edge) => edge.loop)) {
    const source = expectedFlow.nodes.find(
      (node) => node.id === expectedEdge.from,
    )!
    const target = expectedFlow.nodes.find(
      (node) => node.id === expectedEdge.to,
    )!
    const expectedText = `${expectedEdge.label ? `${expectedEdge.label}: ` : ""}${source.title} returns to ${target.title}`
    expect(
      connectionItems.filter(
        (item) =>
          item.textContent?.replace(/\s+/g, " ").trim() === expectedText,
      ),
      `${expectedEdge.from}:${expectedEdge.to} accessible return`,
    ).toHaveLength(1)
  }

  const terminalNodeIDs = expectedFlow.nodes
    .filter(
      (node) =>
        !expectedFlow.edges.some((edge) => edge.from === node.id && !edge.loop),
    )
    .map((node) => node.id)
  for (const terminalNodeID of terminalNodeIDs) {
    const terminalCell = graph.querySelector<HTMLElement>(
      `[data-flow-node-cell="${terminalNodeID}"]`,
    )!
    expect(
      terminalCell.querySelector("[data-flow-band]"),
      `${terminalNodeID} must not reserve later bands`,
    ).toBeNull()
  }
}

const routeTones = [
  "linear",
  "choice",
  "parallel",
  "optional",
  "return",
] as const

function expectFlowToneContract(
  container: HTMLElement,
  expectedFlow: PRLifecycleFlow,
) {
  const graph = container.querySelector<HTMLElement>(
    `[data-flow-graph="${expectedFlow.id}"]`,
  )!

  for (const node of expectedFlow.nodes) {
    const renderedNode = graph.querySelector<HTMLElement>(
      `[data-flow-node-id="${node.id}"]`,
    )!
    if (node.kind === "action") {
      expect(renderedNode, `${node.id} action tone`).toHaveAttribute(
        "data-flow-tone",
        "action",
      )
      continue
    }
    if (node.safeguard) {
      expect(renderedNode, `${node.id} safeguard tone`).toHaveAttribute(
        "data-flow-tone",
        "safeguard",
      )
      continue
    }

    const format = renderedNode.dataset.gateFormat
    expect(format, `${node.id} gate format`).toBeTruthy()
    expect(renderedNode, `${node.id} gate tone`).toHaveAttribute(
      "data-flow-tone",
      `gate-${format}`,
    )
    expect(
      renderedNode.querySelector(`[data-flow-tone="gate-${format}"]`),
      `${node.id} gate format badge tone`,
    ).not.toBeNull()
  }

  for (const edge of expectedFlow.edges) {
    const edgeKey = `${edge.from}:${edge.to}`
    const tone = edge.loop ? "return" : edge.mode
    const path = graph.querySelector<SVGPathElement>(
      `path[data-flow-visible-edge-key="${edgeKey}"]`,
    )!
    expect(path, `${edgeKey} path tone`).toHaveAttribute("data-flow-tone", tone)
    expect(
      path.closest(`[data-flow-edge-layer="${edgeKey}"]`),
      `${edgeKey} layer tone`,
    ).toHaveAttribute("data-flow-tone", tone)

    const merged =
      !edge.loop &&
      expectedFlow.edges.filter(
        (candidate) => candidate.to === edge.to && !candidate.loop,
      ).length > 1
    if (merged) {
      expect(path, `${edgeKey} terminates at merge`).not.toHaveAttribute(
        "marker-end",
      )
    } else {
      const marker = graph.querySelector<SVGMarkerElement>(
        `marker[data-flow-arrow-marker="${tone}"]`,
      )!
      expect(marker, `${tone} arrow marker`).toHaveAttribute(
        "data-flow-tone",
        tone,
      )
      expect(path, `${edgeKey} arrow marker linkage`).toHaveAttribute(
        "marker-end",
        `url(#${marker.id})`,
      )
    }
  }

  for (const tone of routeTones) {
    const marker = graph.querySelector<SVGMarkerElement>(
      `marker[data-flow-arrow-marker="${tone}"]`,
    )!
    expect(marker, `${tone} arrow marker`).toHaveAttribute(
      "data-flow-tone",
      tone,
    )
    expect(
      marker.querySelector(`[data-flow-arrowhead="${tone}"]`),
      `${tone} arrowhead`,
    ).toHaveAttribute("data-flow-tone", tone)
  }

  for (const label of graph.querySelectorAll<SVGElement>(
    "[data-flow-launch-label]",
  )) {
    const edgeKey = label.dataset.flowEdgeKey!
    const path = graph.querySelector<SVGPathElement>(
      `path[data-flow-visible-edge-key="${edgeKey}"]`,
    )!
    const tone = path.dataset.flowTone!
    expect(label, `${edgeKey} label tone`).toHaveAttribute(
      "data-flow-tone",
      tone,
    )
    expect(
      label.querySelector(`[data-flow-label-surface="${tone}"]`),
      `${edgeKey} label surface tone`,
    ).toHaveAttribute("data-flow-tone", tone)
    expect(
      label.querySelector(`[data-flow-label-text="${tone}"]`),
      `${edgeKey} label text tone`,
    ).toHaveAttribute("data-flow-tone", tone)
  }

  const mergeTargetIDs = expectedFlow.nodes
    .map((node) => node.id)
    .filter(
      (targetID) =>
        expectedFlow.edges.filter((edge) => edge.to === targetID && !edge.loop)
          .length > 1,
    )
  for (const targetID of mergeTargetIDs) {
    const stem = graph.querySelector<SVGLineElement>(
      `[data-flow-merge-stem="${targetID}"]`,
    )!
    const diamond = graph.querySelector<SVGRectElement>(
      `[data-flow-merge-diamond="${targetID}"]`,
    )!
    expect(stem, `${targetID} merge stem tone`).toHaveAttribute(
      "data-flow-tone",
      "merge",
    )
    expect(diamond, `${targetID} merge diamond tone`).toHaveAttribute(
      "data-flow-tone",
      "merge",
    )
    expect(stem.parentElement, `${targetID} merge layer tone`).toHaveAttribute(
      "data-flow-tone",
      "merge",
    )
    expect(diamond.parentElement).toBe(stem.parentElement)
  }

  expect(
    Array.from(
      container.querySelectorAll<HTMLElement>('[data-flow-legend="element"]'),
    ).map((legend) => legend.dataset.flowTone),
  ).toEqual(["action", "editable-gate", "safeguard"])
  expect(
    Array.from(
      container.querySelectorAll<HTMLElement>(
        '[data-flow-legend="gate-format"]',
      ),
    ).map((legend) => legend.dataset.flowTone),
  ).toEqual([
    "gate-automatic",
    "gate-deterministic",
    "gate-ai",
    "gate-user",
    "gate-mixed",
    "gate-needs-setup",
  ])
  expect(
    Array.from(
      container.querySelectorAll<HTMLElement>('[data-flow-legend="route"]'),
    ).map((legend) => legend.dataset.flowTone),
  ).toEqual(routeTones)
}

describe("PR lifecycle gate map", () => {
  it("keeps review and implementation in separate manifest-backed views", () => {
    const { container } = renderMap()
    const root = container.querySelector("[data-flow-revision]")
    expect(root).toHaveAttribute("data-flow-schema", "pr-lifecycle-flow/v1")
    expect(root).toHaveAttribute(
      "data-flow-revision",
      `sha256:${"b".repeat(64)}`,
    )

    const reviewTab = screen.getByRole("tab", { name: /^Review workflow/ })
    const implementationTab = screen.getByRole("tab", {
      name: /^Implementation workflow/,
    })
    expect(reviewTab).toHaveAttribute("aria-selected", "true")
    expect(screen.getByText("Receive review assignment")).toBeInTheDocument()
    expect(screen.queryByText("Load implementation batch")).toBeNull()

    fireEvent.click(implementationTab)

    expect(implementationTab).toHaveAttribute("aria-selected", "true")
    expect(screen.getByText("Load implementation batch")).toBeInTheDocument()
    expect(screen.queryByText("Receive review assignment")).toBeNull()
    expect(screen.getByRole("tabpanel")).toHaveAttribute(
      "data-flow-view",
      "implementation",
    )
  })

  it("opens the workflow containing a deep-linked gate and supports tab keys", () => {
    renderMap("pr.implementation.complete")
    const reviewTab = screen.getByRole("tab", { name: /^Review workflow/ })
    const implementationTab = screen.getByRole("tab", {
      name: /^Implementation workflow/,
    })
    expect(implementationTab).toHaveAttribute("aria-selected", "true")
    expect(
      screen.getByRole("button", { name: "Accept implementation" }),
    ).toHaveAttribute("data-gate-selected", "true")

    fireEvent.keyDown(implementationTab, { key: "ArrowLeft" })
    expect(reviewTab).toHaveAttribute("aria-selected", "true")
    fireEvent.keyDown(reviewTab, { key: "End" })
    expect(implementationTab).toHaveAttribute("aria-selected", "true")
  })

  it("renders actions, named editable gates, and locked safeguards", () => {
    const onSelect = vi.fn()
    const { container } = renderMap("pr.review.start", onSelect)

    for (const action of container.querySelectorAll(
      '[data-flow-kind="action"]',
    )) {
      expect(action).toHaveAttribute("data-flow-element", "action")
      expect(action).toHaveAttribute("data-flow-operation")
      expect(action.matches("button, a, [role=button]")).toBe(false)
      expect(action.querySelector("[data-gate-id]")).toBeNull()
      expect(action.querySelector("[data-flow-description]")).not.toBeNull()
      expect(
        within(action as HTMLElement).queryByText("ACTION", { exact: true }),
      ).toBeNull()
    }
    const reviewGate = screen.getByRole("button", { name: "Allow review" })
    expect(reviewGate).toHaveAttribute("data-gate-name", "Allow review")
    expect(reviewGate).toHaveAttribute("data-flow-element", "editable-gate")
    expect(
      screen.getByRole("button", { name: "Accept review coverage" }),
    ).toHaveAttribute("data-gate-name", "Accept review coverage")
    const notificationGate = screen.getByRole("button", {
      name: "Approve notification",
    })
    expect(notificationGate).toHaveAttribute(
      "data-gate-name",
      "Approve notification",
    )
    expect(container.querySelectorAll("[data-gate-number]")).toHaveLength(0)
    expect(notificationGate).toHaveAttribute(
      "data-edit-href",
      "/pull-requests?view=gate-profiles&profile=strict%20profile&gate=pr.review.publish",
    )
    const locked = screen.getByRole("group", { name: "Protect audit archive" })
    expect(locked).toHaveAttribute("data-flow-element", "locked-safeguard")
    expect(locked).toHaveAttribute("data-required-gate", "audit_archive")
    expect(within(locked).queryByRole("button")).toBeNull()
    expect(screen.queryByText(/^Edit gate(?:\s*→)?$/)).toBeNull()

    fireEvent.click(notificationGate)
    fireEvent.click(screen.getByRole("button", { name: "Allow review" }))
    expect(onSelect).toHaveBeenNthCalledWith(1, "pr.review.publish")
    expect(onSelect).toHaveBeenNthCalledWith(2, "pr.review.start")
  })

  it("links node, route, arrow, label, merge, and legend tones", () => {
    const { container } = renderMap()

    expectFlowToneContract(container, reviewFlow)

    fireEvent.click(
      screen.getByRole("tab", { name: /^Implementation workflow/ }),
    )
    expectFlowToneContract(container, implementationFlow)
  })

  it("renders every manifest edge once with explicit route metadata", () => {
    const { container } = renderMap()
    const graph = container.querySelector<HTMLElement>(
      '[data-flow-graph="review"]',
    )!
    expectFlowRenderingContract(container, reviewFlow)
    const renderedEdges = Array.from(
      graph.querySelectorAll<HTMLElement>("[data-flow-edge]"),
    )
    expect(renderedEdges).toHaveLength(reviewFlow.edges.length)
    const renderedContract = renderedEdges
      .map((edge) => ({
        from: edge.dataset.flowSource,
        to: edge.dataset.flowTarget,
        mode: edge.dataset.flowEdge,
      }))
      .sort((left, right) =>
        `${left.from}:${left.to}`.localeCompare(`${right.from}:${right.to}`),
      )
    const expectedContract = reviewFlow.edges
      .map((edge) => ({
        from: edge.from,
        to: edge.to,
        mode: edge.mode,
      }))
      .sort((left, right) =>
        `${left.from}:${left.to}`.localeCompare(`${right.from}:${right.to}`),
      )
    expect(renderedContract).toEqual(expectedContract)

    const choices = Array.from(
      graph.querySelectorAll<HTMLElement>(
        '[data-flow-visible-edge-key][data-flow-source="r_choice"][data-flow-route-mode="choice"]',
      ),
    )
    expect(choices).toHaveLength(2)
    expect(choices.map((choice) => choice.dataset.flowTarget)).toEqual([
      "r_short",
      "r_long_a",
    ])

    const parallels = Array.from(
      graph.querySelectorAll<HTMLElement>(
        '[data-flow-visible-edge-key][data-flow-source="r_parallel"][data-flow-route-mode="parallel"]',
      ),
    )
    expect(parallels).toHaveLength(2)

    const optional = graph.querySelector<HTMLElement>(
      '[data-flow-visible-edge-key][data-flow-source="r_choice"][data-flow-route-mode="optional"]',
    )!
    expect(optional).toHaveAttribute("data-flow-target", "r_choice_sidecar")
  })

  it("uses non-empty bands and releases terminal branches from later bands", () => {
    const { container } = renderMap()
    const graph = container.querySelector<HTMLElement>(
      '[data-flow-graph="review"]',
    )!
    const bands = Array.from(
      graph.querySelectorAll<HTMLElement>("[data-flow-band]"),
    )
    expect(bands.length).toBeGreaterThan(1)
    for (const band of bands) {
      expect(
        band.querySelectorAll(":scope > [data-flow-node-cell]").length,
      ).toBeGreaterThan(0)
    }
    const telemetryBand = graph
      .querySelector('[data-flow-node-cell="r_choice_sidecar"]')!
      .closest<HTMLElement>("[data-flow-band]")!
    const telemetryBandIndex = bands.indexOf(telemetryBand)
    expect(telemetryBandIndex).toBeGreaterThanOrEqual(0)
    for (const laterBand of bands.slice(telemetryBandIndex + 1)) {
      expect(
        laterBand.querySelector('[data-flow-node-cell="r_choice_sidecar"]'),
      ).toBeNull()
    }
    expect(
      graph.querySelectorAll('[data-flow-node-id="r_merge"]'),
    ).toHaveLength(1)

    for (const node of reviewFlow.nodes) {
      expect(
        graph.querySelectorAll(`[data-flow-node-id="${node.id}"]`),
        node.id,
      ).toHaveLength(1)
    }
  })

  it("keeps semantic and visible edges exact in both workflow views", () => {
    const { container } = renderMap()
    expectFlowRenderingContract(container, reviewFlow)

    fireEvent.click(
      screen.getByRole("tab", { name: /^Implementation workflow/ }),
    )
    expectFlowRenderingContract(container, implementationFlow)
  })

  it("keeps ownership routes attached to the correct gates", () => {
    const ownershipCatalog: PRLifecycleFlowCatalog = {
      schema: "pr-lifecycle-flow/v1",
      flows: [reviewFlow, ownershipFlow],
    }
    const { container } = render(
      <PRLifecycleGateMap
        flow={ownershipCatalog}
        flowRevision={`sha256:${"d".repeat(64)}`}
        onSelect={vi.fn()}
        selectedDecisionPoint="pr.implementation.start"
        workflows={workflows}
      />,
    )
    const graph = container.querySelector<HTMLElement>(
      '[data-flow-graph="implementation"]',
    )!

    expectFlowRenderingContract(container, ownershipFlow)
    expect(
      Array.from(
        graph.querySelectorAll<HTMLElement>(
          '[data-flow-visible-edge-key][data-flow-source="ownership_check"]',
        ),
      ).map((edge) => edge.dataset.flowTarget),
    ).toEqual(["ownership_start", "ownership_eligibility", "ownership_stop"])
    expect(
      graph.querySelectorAll('[data-flow-node-id="ownership_start"]'),
    ).toHaveLength(1)
    expect(
      graph.querySelectorAll(
        '[data-flow-visible-edge-key][data-flow-target="ownership_start"]',
      ),
    ).toHaveLength(2)
    expect(
      graph.querySelector('[data-flow-node-cell="ownership_stop"]'),
    ).not.toContainElement(
      graph.querySelector('[data-flow-node-cell="ownership_start"]'),
    )
  })

  it("uses a visible primary fallback for an unlabeled mixed branch", () => {
    const mixedFlow = structuredClone(implementationFlow)
    delete mixedFlow.edges.find(
      (edge) => edge.from === "i_audit" && edge.to === "i_decide",
    )!.label
    const mixedCatalog: PRLifecycleFlowCatalog = {
      schema: "pr-lifecycle-flow/v1",
      flows: [reviewFlow, mixedFlow],
    }
    const { container } = render(
      <PRLifecycleGateMap
        flow={mixedCatalog}
        flowRevision={`sha256:${"e".repeat(64)}`}
        onSelect={vi.fn()}
        selectedDecisionPoint="pr.implementation.start"
        workflows={workflows}
      />,
    )
    const launch = container.querySelector<HTMLElement>(
      '[data-flow-launch-label][data-flow-edge-key="i_audit:i_decide"]',
    )!
    expect(launch).toHaveTextContent("Primary")
    expect(container).not.toHaveTextContent("undefined")
  })

  it("keeps mixed route modes on the generated edge paths", () => {
    const { container } = renderMap()
    const choiceEdges = Array.from(
      container.querySelectorAll<HTMLElement>(
        '[data-flow-visible-edge-key][data-flow-source="r_choice"]',
      ),
    )
    expect(choiceEdges).toHaveLength(3)
    expect(choiceEdges.map((edge) => edge.dataset.flowRouteMode)).toEqual([
      "choice",
      "choice",
      "optional",
    ])
    expect(choiceEdges.map((edge) => edge.dataset.flowTarget)).toEqual([
      "r_short",
      "r_long_a",
      "r_choice_sidecar",
    ])

    const parallelEdges = Array.from(
      container.querySelectorAll<HTMLElement>(
        '[data-flow-visible-edge-key][data-flow-source="r_parallel"]',
      ),
    )
    expect(parallelEdges).toHaveLength(3)
    expect(parallelEdges.map((edge) => edge.dataset.flowRouteMode)).toEqual([
      "parallel",
      "parallel",
      "optional",
    ])

    fireEvent.click(
      screen.getByRole("tab", { name: /^Implementation workflow/ }),
    )
    const mixedEdges = Array.from(
      container.querySelectorAll<HTMLElement>(
        '[data-flow-visible-edge-key][data-flow-source="i_audit"]',
      ),
    )
    expect(mixedEdges).toHaveLength(2)
    expect(
      mixedEdges.find((edge) => edge.dataset.flowRouteMode === "linear"),
    ).toHaveAttribute("data-flow-target", "i_decide")
    expect(
      mixedEdges.find((edge) => edge.dataset.flowRouteMode === "optional"),
    ).toHaveAttribute("data-flow-target", "i_metrics")
    expect(
      container.querySelector(
        '[data-flow-launch-label][data-flow-edge-key="i_audit:i_decide"]',
      ),
    ).toHaveTextContent("Candidate")
    expect(
      container.querySelector(
        '[data-flow-launch-label][data-flow-edge-key="i_audit:i_metrics"]',
      ),
    ).toHaveTextContent("Metrics")
  })

  it("renders a two-parent merge once with both incoming edge paths", () => {
    const { container } = renderMap()
    const graph = container.querySelector<HTMLElement>(
      '[data-flow-graph="review"]',
    )!
    const longEdge = graph.querySelectorAll(
      '[data-flow-edge][data-flow-source="r_short"][data-flow-target="r_merge"]',
    )
    expect(longEdge).toHaveLength(1)
    expect(
      graph.querySelectorAll('[data-flow-node-id="r_merge"]'),
    ).toHaveLength(1)
    expect(
      graph.querySelectorAll(
        '[data-flow-node-cell="r_merge"] [data-flow-node-id="r_merge"]',
      ),
    ).toHaveLength(1)
    const semanticMergeEdges = graph.querySelectorAll(
      '[data-flow-edge][data-flow-target="r_merge"]',
    )
    expect(semanticMergeEdges).toHaveLength(2)
    expect(
      graph.querySelectorAll(
        '[data-flow-visible-edge-key][data-flow-target="r_merge"]',
      ),
    ).toHaveLength(2)
    expect(
      graph.querySelectorAll('[data-flow-merge-diamond="r_merge"]'),
    ).toHaveLength(1)
    expect(graph.querySelectorAll("[data-flow-edge]")).toHaveLength(
      reviewFlow.edges.length,
    )
  })

  it("renders a singleton loop as an unlabeled SVG back-edge", () => {
    const { container } = renderMap("pr.implementation.start")
    const graph = container.querySelector('[data-flow-graph="implementation"]')!
    expect(graph.querySelectorAll("[data-flow-edge]")).toHaveLength(
      implementationFlow.edges.length,
    )
    const loop = graph.querySelector<SVGPathElement>(
      'path[data-flow-visible-edge-key="i_retry:i_work"]',
    )!
    expect(loop).toHaveAttribute("data-flow-loop", "true")
    expect(loop).toHaveAttribute("data-flow-loop-target", "i_work")
    expect(loop).toHaveAttribute("data-flow-shape", "back-edge")
    expect(loop).toHaveAttribute("data-flow-source", "i_retry")
    expect(loop).toHaveAttribute("data-flow-target", "i_work")
    expect(loop).toHaveAttribute("data-flow-route-mode", "linear")
    expect(loop).toHaveAttribute("marker-end")
    expect(loop.getAttribute("d")).toBeTruthy()
    expect(
      graph.querySelector(
        '[data-flow-launch-label][data-flow-edge-key="i_retry:i_work"]',
      ),
    ).toBeNull()
    expect(graph.querySelector("[data-flow-loop-connector]")).toBeNull()
    expect(
      graph.querySelectorAll(
        '[data-flow-edge-key="i_retry:i_work"][data-flow-edge]',
      ),
    ).toHaveLength(1)
    expect(
      Array.from(graph.querySelectorAll('[role="listitem"]')).filter(
        (item) =>
          item.textContent?.replace(/\s+/g, " ").trim() ===
          "Resume implementation returns to Implement fixes",
      ),
    ).toHaveLength(1)
  })

  it("keeps a branched loop label on its SVG back-edge", () => {
    const multiLoopFlow = structuredClone(implementationFlow)
    multiLoopFlow.nodes.push({
      id: "i_followup",
      kind: "action",
      title: "Record deferred follow-up",
      description: "Record work that will continue outside this attempt.",
      operation: "pr.implementation.followup.record",
      editable: false,
    })
    multiLoopFlow.edges.find(
      (edge) => edge.from === "i_retry" && edge.loop,
    )!.label = "Repair"
    multiLoopFlow.edges.push({
      from: "i_retry",
      to: "i_followup",
      mode: "parallel",
      label: "Follow-up",
      loop: false,
    })
    const multiLoopCatalog: PRLifecycleFlowCatalog = {
      schema: "pr-lifecycle-flow/v1",
      flows: [reviewFlow, multiLoopFlow],
    }
    const { container } = render(
      <PRLifecycleGateMap
        flow={multiLoopCatalog}
        flowRevision={`sha256:${"f".repeat(64)}`}
        onSelect={vi.fn()}
        selectedDecisionPoint="pr.implementation.start"
        workflows={workflows}
      />,
    )
    const graph = container.querySelector<HTMLElement>(
      '[data-flow-graph="implementation"]',
    )!

    expectFlowRenderingContract(container, multiLoopFlow)
    const loop = graph.querySelector<SVGPathElement>(
      'path[data-flow-visible-edge-key="i_retry:i_work"]',
    )!
    expect(loop).toHaveAttribute("data-flow-loop", "true")
    expect(loop).toHaveAttribute("data-flow-loop-target", "i_work")
    expect(loop).toHaveAttribute("data-flow-shape", "back-edge")
    const label = graph.querySelector<SVGElement>(
      '[data-flow-launch-label][data-flow-edge-key="i_retry:i_work"]',
    )!
    expect(label).toHaveTextContent("Repair")
    expect(label).toHaveAttribute("data-flow-source", "i_retry")
    expect(label).toHaveAttribute("data-flow-target", "i_work")
    expect(
      graph.querySelectorAll(
        '[data-flow-edge-key="i_retry:i_work"][data-flow-edge]',
      ),
    ).toHaveLength(1)
    expect(
      graph.querySelectorAll(
        'path[data-flow-visible-edge-key="i_retry:i_work"][data-flow-loop="true"]',
      ),
    ).toHaveLength(1)
    expect(graph.querySelector("[data-flow-loop-connector]")).toBeNull()
    expect(
      Array.from(graph.querySelectorAll('[role="listitem"]')).filter(
        (item) =>
          item.textContent?.replace(/\s+/g, " ").trim() ===
          "Repair: Resume implementation returns to Implement fixes",
      ),
    ).toHaveLength(1)
  })

  it("gives multiple returns from one source distinct ports and launch shelves", () => {
    const twoLoopFlow = structuredClone(implementationFlow)
    twoLoopFlow.nodes.push({
      id: "i_work_alternate",
      kind: "action",
      title: "Alternate implementation anchor",
      description: "Provide a second earlier return target for route geometry.",
      operation: "pr.implementation.alternate",
      editable: false,
    })
    twoLoopFlow.edges.find(
      (edge) => edge.from === "i_retry" && edge.loop,
    )!.label = "Repair"
    twoLoopFlow.edges.push({
      from: "i_retry",
      to: "i_work_alternate",
      mode: "choice",
      label: "Rework",
      loop: true,
    })

    const rectSpy = vi
      .spyOn(HTMLElement.prototype, "getBoundingClientRect")
      .mockImplementation(function (this: HTMLElement) {
        if (this.hasAttribute("data-flow-canvas")) {
          return flowTestRect(0, 0, 1_200, 4_000)
        }
        const cell = this.matches("[data-flow-node-cell]")
          ? this
          : this.closest<HTMLElement>("[data-flow-node-cell]")
        if (cell) {
          const rank = Number(cell.dataset.flowNodeRank ?? 0)
          return flowTestRect(400, 40 + rank * 140, 400, 64)
        }
        return flowTestRect(0, 0, 0, 0)
      })

    try {
      const { container } = render(
        <PRLifecycleGateMap
          flow={{
            schema: "pr-lifecycle-flow/v1",
            flows: [reviewFlow, twoLoopFlow],
          }}
          flowRevision={`sha256:${"a".repeat(64)}`}
          onSelect={vi.fn()}
          selectedDecisionPoint="pr.implementation.start"
          workflows={workflows}
        />,
      )
      const loops = Array.from(
        container.querySelectorAll<SVGPathElement>(
          'path[data-flow-source="i_retry"][data-flow-loop="true"]',
        ),
      )
      expect(loops).toHaveLength(2)
      const launches = loops.map((loop) => {
        const match = loop
          .getAttribute("d")
          ?.match(/^M (-?[\d.]+) (-?[\d.]+) L (-?[\d.]+) (-?[\d.]+)/)
        expect(match, loop.dataset.flowVisibleEdgeKey).not.toBeNull()
        return { sourceX: Number(match![1]), shelfY: Number(match![4]) }
      })
      expect(new Set(launches.map((launch) => launch.sourceX)).size).toBe(2)
      expect(new Set(launches.map((launch) => launch.shelfY)).size).toBe(2)
    } finally {
      rectSpy.mockRestore()
    }
  })

  it("derives gate-format labels from stages", () => {
    const { container } = renderMap()
    expect(
      container.querySelector('[data-gate-id="pr.review.start"]'),
    ).toHaveAttribute("data-gate-format", "automatic")
    expect(
      container.querySelector('[data-gate-id="pr.review.complete"]'),
    ).toHaveAttribute("data-gate-format", "deterministic")
    expect(
      container.querySelector('[data-gate-id="pr.review.publish"]'),
    ).toHaveAttribute("data-gate-format", "ai")
    expect(
      container.querySelector('[data-gate-id="pr.deferred.publish"]'),
    ).toHaveAttribute("data-gate-format", "user")
    expect(
      container.querySelector('[data-gate-id="pr.deferred.publish"]'),
    ).toHaveTextContent("default fallback")

    fireEvent.click(
      screen.getByRole("tab", { name: /^Implementation workflow/ }),
    )
    expect(
      container.querySelector('[data-gate-id="pr.implementation.start"]'),
    ).toHaveAttribute("data-gate-format", "user")
    expect(
      container.querySelector('[data-gate-id="pr.implementation.complete"]'),
    ).toHaveAttribute("data-gate-format", "mixed")
    expect(
      container.querySelector('[data-gate-id="pr.implementation.complete"]'),
    ).toHaveTextContent("Deterministic → AI → User")
  })

  it("uses the needs-setup tone for an invalid gate workflow", () => {
    const invalidWorkflows = structuredClone(workflows)
    invalidWorkflows["pr.review.start"].stages = []
    const { container } = render(
      <PRLifecycleGateMap
        flow={flow}
        flowRevision={`sha256:${"b".repeat(64)}`}
        onSelect={vi.fn()}
        selectedDecisionPoint="pr.review.start"
        workflows={invalidWorkflows}
      />,
    )
    const gate = container.querySelector<HTMLElement>(
      '[data-gate-id="pr.review.start"]',
    )!

    expect(gate).toHaveAttribute("data-gate-format", "needs-setup")
    expect(gate).toHaveAttribute("data-flow-tone", "gate-needs-setup")
    expect(
      gate.querySelector('[data-flow-tone="gate-needs-setup"]'),
    ).toHaveTextContent("Needs setup gate")
  })

  it("renders a changed topology directly from the supplied catalog", () => {
    const { container, rerender } = renderMap()
    const changedFlow = structuredClone(flow)
    changedFlow.flows[0].nodes.push({
      id: "r_new_output",
      kind: "action",
      title: "Render new manifest output",
      description: "Prove that topology is supplied by the API.",
      operation: "pr.review.new.output",
      editable: false,
    })
    changedFlow.flows[0].edges.push({
      from: "r_fallback_gate",
      to: "r_new_output",
      mode: "linear",
      loop: false,
    })

    rerender(
      <PRLifecycleGateMap
        flow={changedFlow}
        flowRevision={`sha256:${"c".repeat(64)}`}
        onSelect={vi.fn()}
        selectedDecisionPoint="pr.review.start"
        workflows={workflows}
      />,
    )

    expect(screen.getByText("Render new manifest output")).toBeInTheDocument()
    expect(container.querySelector("[data-flow-revision]")).toHaveAttribute(
      "data-flow-revision",
      `sha256:${"c".repeat(64)}`,
    )
    expect(
      container.querySelectorAll('[data-flow-graph="review"] [data-flow-edge]'),
    ).toHaveLength(reviewFlow.edges.length + 1)
  })
})
