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

function expectTargetCellContract(
  container: HTMLElement,
  expectedFlow: PRLifecycleFlow,
) {
  const graph = container.querySelector<HTMLElement>(
    `[data-flow-graph="${expectedFlow.id}"]`,
  )!
  const semanticEdges = Array.from(
    graph.querySelectorAll<HTMLElement>("[data-flow-edge]"),
  )
  expect(semanticEdges).toHaveLength(expectedFlow.edges.length)
  const outgoing = new Map<string, PRLifecycleFlow["edges"]>()
  for (const edge of expectedFlow.edges) {
    outgoing.set(edge.from, [...(outgoing.get(edge.from) ?? []), edge])
  }

  for (const expectedEdge of expectedFlow.edges) {
    const edgeKey = `${expectedEdge.from}:${expectedEdge.to}`
    const matches = semanticEdges.filter(
      (edge) => edge.dataset.flowEdgeKey === edgeKey,
    )
    expect(matches, edgeKey).toHaveLength(1)
    const edge = matches[0]
    const expectedCellID = expectedEdge.loop
      ? expectedEdge.from
      : expectedEdge.to
    const cell = edge.closest<HTMLElement>("[data-flow-cell]")
    expect(cell, edgeKey).not.toBeNull()
    expect(cell, edgeKey).toHaveAttribute("data-flow-cell", expectedCellID)
    const node = cell!.querySelector<HTMLElement>(
      `[data-flow-node-id="${expectedCellID}"]`,
    )
    expect(node, edgeKey).not.toBeNull()
    const relation = edge.compareDocumentPosition(node!)
    if (expectedEdge.loop) {
      expect(
        relation & Node.DOCUMENT_POSITION_PRECEDING,
        `${edgeKey} loop must follow its source node`,
      ).toBeTruthy()
      const targetTitle = edge.querySelector<HTMLElement>(
        "[data-flow-loop-target-title]",
      )
      if ((outgoing.get(expectedEdge.from) ?? []).length > 1) {
        expect(targetTitle, `${edgeKey} loop target title`).toHaveAttribute(
          "data-flow-loop-target-title",
          expectedEdge.to,
        )
        expect(targetTitle, edgeKey).toHaveTextContent(
          `Returns to ${
            expectedFlow.nodes.find((node) => node.id === expectedEdge.to)!
              .title
          }`,
        )
      } else {
        expect(targetTitle, `${edgeKey} singleton loop target title`).toBeNull()
      }
    } else {
      expect(
        relation & Node.DOCUMENT_POSITION_FOLLOWING,
        `${edgeKey} connector must precede its target node`,
      ).toBeTruthy()
    }
  }
  for (const [sourceID, edges] of outgoing) {
    if (edges.length < 2) continue
    for (const edge of edges.filter((candidate) => !candidate.loop)) {
      const edgeKey = `${edge.from}:${edge.to}`
      const launch = graph.querySelector<HTMLElement>(
        `[data-flow-launch][data-flow-edge-key="${edgeKey}"]`,
      )
      expect(launch, `${edgeKey} source launch`).not.toBeNull()
      expect(launch!.closest("[data-flow-cell]"), edgeKey).toHaveAttribute(
        "data-flow-cell",
        sourceID,
      )
      expect(launch, edgeKey).toHaveAttribute(
        "data-flow-launch-target",
        edge.to,
      )
      expect(
        launch!.querySelector("[data-flow-launch-target-title]"),
        edgeKey,
      ).toHaveTextContent(
        expectedFlow.nodes.find((node) => node.id === edge.to)!.title,
      )
      const label = launch!
        .querySelector("[data-flow-launch-label]")!
        .textContent!.trim()
      expect(label, edgeKey).not.toContain("undefined")
      expect(label.split(/\s+/), edgeKey).toHaveLength(1)
    }
  }
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
    ).toHaveAttribute("aria-pressed", "true")

    fireEvent.keyDown(implementationTab, { key: "ArrowLeft" })
    expect(reviewTab).toHaveAttribute("aria-selected", "true")
    fireEvent.keyDown(reviewTab, { key: "End" })
    expect(implementationTab).toHaveAttribute("aria-selected", "true")
  })

  it("renders actions, editable gates, locked safeguards, and manifest ordinals", () => {
    const onSelect = vi.fn()
    const { container } = renderMap("pr.review.start", onSelect)

    for (const action of container.querySelectorAll(
      '[data-flow-kind="action"]',
    )) {
      expect(action).toHaveAttribute("data-flow-operation")
      expect(action.querySelector("[data-gate-id]")).toBeNull()
      expect(action.querySelector("[data-flow-description]")).not.toBeNull()
    }
    expect(
      screen.getByRole("button", { name: "Allow review" }),
    ).toHaveAttribute("data-gate-number", "3")
    expect(
      screen.getByRole("button", { name: "Accept review coverage" }),
    ).toHaveAttribute("data-gate-number", "4")
    const notificationGate = screen.getByRole("button", {
      name: "Approve notification",
    })
    expect(notificationGate).toHaveAttribute("data-gate-number", "10")
    expect(notificationGate).toHaveAttribute(
      "data-edit-href",
      "/pull-requests?view=gate-profiles&profile=strict%20profile&gate=pr.review.publish",
    )
    const locked = screen.getByRole("group", { name: "Protect audit archive" })
    expect(locked).toHaveAttribute("data-required-gate", "audit_archive")
    expect(within(locked).queryByRole("button")).toBeNull()

    fireEvent.click(notificationGate)
    fireEvent.keyDown(screen.getByRole("button", { name: "Allow review" }), {
      key: "Enter",
    })
    expect(onSelect).toHaveBeenNthCalledWith(1, "pr.review.publish")
    expect(onSelect).toHaveBeenNthCalledWith(2, "pr.review.start")
  })

  it("renders every manifest edge exactly once with explicit fork semantics", () => {
    const { container } = renderMap()
    const graph = container.querySelector<HTMLElement>(
      '[data-flow-graph="review"]',
    )!
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

    for (const edge of graph.querySelectorAll('[data-flow-edge="linear"]')) {
      expect(edge.querySelector("[data-flow-branch-label]")).toBeNull()
      expect(edge.textContent?.trim()).toBe("↓")
    }

    const choices = Array.from(
      graph.querySelectorAll<HTMLElement>(
        '[data-flow-branch="r_choice"][data-flow-route-mode="choice"]',
      ),
    )
    expect(choices).toHaveLength(2)
    for (const choice of choices) {
      expect(choice).toHaveAccessibleName(/choice leads to/i)
      expect(within(choice).getByText("Choice")).toBeInTheDocument()
    }
    expect(
      choices.map(
        (choice) =>
          choice.querySelector("[data-flow-branch-label]")?.textContent,
      ),
    ).toEqual(["Focused", "Thorough"])

    const parallels = Array.from(
      graph.querySelectorAll<HTMLElement>(
        '[data-flow-branch="r_parallel"][data-flow-route-mode="parallel"]',
      ),
    )
    expect(parallels).toHaveLength(2)
    for (const parallel of parallels) {
      expect(parallel).toHaveAttribute("data-flow-parallel", "true")
      expect(parallel).toHaveAccessibleName(/also follows/i)
      expect(within(parallel).getByText("All required")).toBeInTheDocument()
    }

    const optional = graph.querySelector<HTMLElement>(
      '[data-flow-branch="r_choice"][data-flow-route-mode="optional"]',
    )!
    expect(optional).toHaveAttribute("data-flow-optional", "true")
    expect(optional).toHaveAccessibleName(/optionally follows/i)
    expect(within(optional).getByText("Optional paths")).toBeInTheDocument()

    for (const label of graph.querySelectorAll("[data-flow-branch-label]")) {
      expect(label.textContent!.trim().split(/\s+/)).toHaveLength(1)
    }

    const singletonOptional = graph.querySelector<HTMLElement>(
      '[data-flow-edge="optional"][data-flow-source="r_store"]',
    )!
    expect(singletonOptional).toHaveAccessibleName(
      /optionally continues to approve deferred output/i,
    )
    expect(singletonOptional).not.toHaveAccessibleName(/undefined/i)
    expect(singletonOptional.textContent?.trim()).toBe("↓")
    expect(
      singletonOptional.querySelector("[data-flow-branch-label]"),
    ).toBeNull()
  })

  it("places every connector in its declared endpoint cell", () => {
    const { container } = renderMap()
    expectTargetCellContract(container, reviewFlow)

    fireEvent.click(
      screen.getByRole("tab", { name: /^Implementation workflow/ }),
    )
    expectTargetCellContract(container, implementationFlow)
  })

  it("keeps ownership branches attached to the correct gates", () => {
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

    expectTargetCellContract(container, ownershipFlow)
    expect(
      Array.from(
        graph.querySelectorAll<HTMLElement>(
          '[data-flow-launches="ownership_check"] [data-flow-launch-label]',
        ),
      ).map((label) => label.textContent?.trim()),
    ).toEqual(["Owned", "Non-owned", "Read-only"])
    expect(
      graph
        .querySelector(
          '[data-flow-edge-key="ownership_check:ownership_start"][data-flow-edge]',
        )
        ?.closest("[data-flow-cell]"),
    ).toHaveAttribute("data-flow-cell", "ownership_start")
    expect(
      graph
        .querySelector(
          '[data-flow-edge-key="ownership_check:ownership_eligibility"][data-flow-edge]',
        )
        ?.closest("[data-flow-cell]"),
    ).toHaveAttribute("data-flow-cell", "ownership_eligibility")
    expect(
      graph
        .querySelector(
          '[data-flow-edge-key="ownership_check:ownership_stop"][data-flow-edge]',
        )
        ?.closest("[data-flow-cell]"),
    ).toHaveAttribute("data-flow-cell", "ownership_stop")
    expect(
      graph.querySelectorAll(
        '[data-flow-continuation-for="ownership_check:ownership_start"]',
      ),
    ).toHaveLength(1)
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
      '[data-flow-launch][data-flow-edge-key="i_audit:i_decide"]',
    )!
    expect(launch).toHaveTextContent(/Primary.*Route audit result/s)
    expect(launch).toHaveAccessibleName(
      /Primary route from Audit implementation to Route audit result/i,
    )
    expect(container).not.toHaveTextContent("undefined")
  })

  it("groups optional sidecars separately from choice and required primary paths", () => {
    const { container } = renderMap()
    const choiceWithOptional = Array.from(
      container.querySelectorAll<HTMLElement>('[data-flow-branch="r_choice"]'),
    )!
    expect(choiceWithOptional).toHaveLength(3)
    for (const route of choiceWithOptional) {
      expect(route).toHaveAttribute(
        "data-flow-route-composition",
        "choice+optional",
      )
    }
    expect(
      choiceWithOptional.filter(
        (route) => route.dataset.flowRouteMode === "choice",
      ),
    ).toHaveLength(2)
    expect(
      choiceWithOptional.filter(
        (route) => route.dataset.flowRouteMode === "optional",
      ),
    ).toHaveLength(1)
    expect(
      choiceWithOptional.filter((route) => route.dataset.flowEdge === "choice"),
    ).toHaveLength(2)
    expect(
      choiceWithOptional.filter(
        (route) => route.dataset.flowEdge === "optional",
      ),
    ).toHaveLength(1)

    const parallelWithOptional = Array.from(
      container.querySelectorAll<HTMLElement>(
        '[data-flow-branch="r_parallel"]',
      ),
    )!
    expect(parallelWithOptional).toHaveLength(3)
    for (const route of parallelWithOptional) {
      expect(route).toHaveAttribute(
        "data-flow-route-composition",
        "parallel+optional",
      )
    }
    expect(
      parallelWithOptional.filter(
        (route) => route.dataset.flowRouteMode === "parallel",
      ),
    ).toHaveLength(2)
    expect(
      parallelWithOptional.filter(
        (route) => route.dataset.flowRouteMode === "optional",
      ),
    ).toHaveLength(1)

    fireEvent.click(
      screen.getByRole("tab", { name: /^Implementation workflow/ }),
    )
    const singleRequiredWithOptional = Array.from(
      container.querySelectorAll<HTMLElement>('[data-flow-branch="i_audit"]'),
    )!
    expect(singleRequiredWithOptional).toHaveLength(2)
    for (const route of singleRequiredWithOptional) {
      expect(route).toHaveAttribute(
        "data-flow-route-composition",
        "linear+optional",
      )
    }
    expect(
      singleRequiredWithOptional.find(
        (route) => route.dataset.flowRouteMode === "linear",
      ),
    ).toHaveTextContent(/Primary path.*Candidate/s)
    expect(
      singleRequiredWithOptional.find(
        (route) => route.dataset.flowRouteMode === "optional",
      ),
    ).toHaveTextContent(/Optional paths.*Metrics/s)
  })

  it("uses continuation rails for an unequal branch merge without duplicating its edge", () => {
    const { container } = renderMap()
    const graph = container.querySelector<HTMLElement>(
      '[data-flow-graph="review"]',
    )!
    const longEdge = graph.querySelectorAll(
      '[data-flow-edge][data-flow-source="r_short"][data-flow-target="r_merge"]',
    )
    expect(longEdge).toHaveLength(1)
    const rails = graph.querySelectorAll(
      '[data-flow-continuation-for="r_short:r_merge"]',
    )
    expect(rails).toHaveLength(1)
    expect(rails[0]).toHaveAttribute("data-flow-edge-key", "r_short:r_merge")
    expect(rails[0].closest("[data-flow-rank]")).toContainElement(
      graph.querySelector('[data-flow-node-id="r_long_gate"]'),
    )
    expect(
      graph.querySelectorAll('[data-flow-node-id="r_merge"]'),
    ).toHaveLength(1)
    expect(graph.querySelectorAll("[data-flow-edge]")).toHaveLength(
      reviewFlow.edges.length,
    )
  })

  it("shows loops without adding a branch label or duplicate edge", () => {
    const { container } = renderMap("pr.implementation.start")
    const graph = container.querySelector('[data-flow-graph="implementation"]')!
    expect(graph.querySelectorAll("[data-flow-edge]")).toHaveLength(
      implementationFlow.edges.length,
    )
    const loop = graph.querySelector(
      '[data-flow-loop-target="i_work"]',
    ) as HTMLElement
    expect(loop).toHaveAttribute("data-flow-edge", "linear")
    expect(loop).toHaveAttribute("title", "Returns to Implement fixes")
    expect(loop.textContent?.trim()).toBe("↺")
    expect(loop.querySelector("[data-flow-branch-label]")).toBeNull()
    expect(loop.querySelector("[data-flow-loop-target-title]")).toBeNull()
  })

  it("names the exact target of a loop in a multi-way route", () => {
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

    expectTargetCellContract(container, multiLoopFlow)
    const loop = graph.querySelector<HTMLElement>(
      '[data-flow-edge-key="i_retry:i_work"][data-flow-edge]',
    )!
    expect(loop.querySelector("[data-flow-branch-label]")).toHaveTextContent(
      "Repair",
    )
    expect(loop.querySelector("[data-flow-loop-target-title]")).toHaveAttribute(
      "data-flow-loop-target-title",
      "i_work",
    )
    expect(loop).toHaveTextContent(/Repair.*Returns to Implement fixes.*↺/s)
    expect(
      graph.querySelectorAll('[data-flow-edge-key="i_retry:i_work"]'),
    ).toHaveLength(1)
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
