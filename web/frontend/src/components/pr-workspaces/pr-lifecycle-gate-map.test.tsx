import { fireEvent, render, screen, within } from "@testing-library/react"
import { describe, expect, it, vi } from "vitest"

import {
  type PRLifecycleGateProfile,
  prLifecycleKnownDecisionPoints,
} from "@/api/pr-lifecycle-gate-profiles"
import { PRLifecycleGateMap } from "@/components/pr-workspaces/pr-lifecycle-gate-map"

const workflows: PRLifecycleGateProfile["workflows"] = {
  "pr.review.start": {
    id: "review-start",
    name: "Start review",
    purpose: "authorization",
    decision_point: "pr.review.start",
    stages: [{ id: "domain", kind: "zero" }],
  },
  "pr.review.complete": {
    id: "review-complete",
    name: "Complete review",
    purpose: "authorization",
    decision_point: "pr.review.complete",
    stages: [
      {
        id: "policy",
        kind: "deterministic",
        title: "Check review policy",
        when: "true",
      },
    ],
  },
  "pr.finding.classify": {
    id: "finding-classify",
    name: "Classify finding",
    purpose: "classification",
    decision_point: "pr.finding.classify",
    stages: [
      {
        id: "classify",
        kind: "ai_working_context",
        title: "Classify finding",
        agent_id: "reviewer",
        criteria: "Classify the finding scope.",
      },
      {
        id: "verify",
        kind: "ai_isolated_context",
        title: "Verify classification",
        agent_id: "reviewer",
        criteria: "Verify the classification independently.",
      },
    ],
  },
  "pr.implementation.start": {
    id: "implementation-start",
    name: "Start implementation",
    purpose: "authorization",
    decision_point: "pr.implementation.start",
    stages: [
      {
        id: "approve",
        kind: "human",
        title: "Approve implementation",
        questions: ["Proceed?"],
      },
    ],
  },
  "pr.implementation.complete": {
    id: "implementation-complete",
    name: "Complete implementation",
    purpose: "authorization",
    decision_point: "pr.implementation.complete",
    stages: [
      {
        id: "scope",
        kind: "deterministic",
        title: "Check scope",
        when: "true",
      },
      {
        id: "audit",
        kind: "ai_isolated_context",
        title: "Audit implementation",
        agent_id: "controller",
        criteria: "Confirm the implementation is complete.",
      },
      {
        id: "approve",
        kind: "human",
        title: "Approve implementation",
        questions: ["Accept?"],
      },
    ],
  },
  "pr.review.publish": {
    id: "review-publish",
    name: "Publish review",
    purpose: "authorization",
    decision_point: "pr.review.publish",
    stages: [
      {
        id: "approval",
        kind: "human",
        title: "",
        questions: ["Publish?"],
      },
    ],
  },
  "pr.correction.promote": {
    id: "correction-promote",
    name: "Promote correction",
    purpose: "authorization",
    decision_point: "pr.correction.promote",
    stages: [],
  },
}

function expectCurrentFlowContract(container: HTMLElement) {
  const actions = Array.from(
    container.querySelectorAll<HTMLElement>('[data-flow-kind="action"]'),
  )
  expect(actions.length).toBeGreaterThan(0)
  for (const action of actions) {
    expect(action.querySelector("[data-gate-id]")).toBeNull()
    const description = action.querySelector(
      "[data-flow-description]",
    )?.textContent
    expect(description?.trim()).toBeTruthy()
    expect(description!.trim().split(/\s+/).length).toBeLessThanOrEqual(12)
  }

  const gates = Array.from(
    container.querySelectorAll<HTMLButtonElement>("button[data-gate-id]"),
  )
  expect(gates.length).toBeGreaterThan(0)
  for (const gate of gates) {
    expect(gate).toHaveAttribute("data-flow-kind", "gate")
    expect(gate.closest('[data-flow-kind="action"]')).toBeNull()
    expect(gate.querySelector('[data-flow-kind="action"]')).toBeNull()
    expect(gate.querySelector("[data-gate-description]")).not.toBeNull()
  }
  const gateNodes = Array.from(
    container.querySelectorAll<HTMLElement>('[data-flow-kind="gate"]'),
  )
  expect(gateNodes.length).toBeGreaterThanOrEqual(gates.length)
  for (const gateNode of gateNodes) {
    expect(gateNode.querySelector("[data-gate-description]")).not.toBeNull()
    expect(
      gateNode.matches("button[data-gate-id]") ||
        gateNode.hasAttribute("data-required-gate"),
    ).toBe(true)
  }

  const linearEdges = Array.from(
    container.querySelectorAll<HTMLElement>('[data-flow-edge="linear"]'),
  )
  expect(linearEdges.length).toBeGreaterThan(0)
  for (const edge of linearEdges) {
    expect(edge).toHaveAttribute("aria-hidden", "true")
    expect(edge.textContent?.trim()).toBe("↓")
    expect(edge.querySelector("[data-flow-branch-label]")).toBeNull()
  }

  const splits = Array.from(
    container.querySelectorAll<HTMLElement>("[data-flow-branch]"),
  )
  expect(splits.length).toBeGreaterThan(0)
  for (const split of splits) {
    expect(
      split.querySelector(":scope > [data-flow-split-label]"),
    ).not.toBeNull()
    const paths = Array.from(
      split.querySelectorAll<HTMLElement>(
        ":scope > div > [data-flow-branch-path]",
      ),
    )
    expect(paths.length).toBeGreaterThanOrEqual(2)
    const labels = paths.map((path) => {
      expect(path).toHaveAttribute("data-flow-branch-rail")
      const label = path.querySelector<HTMLElement>("[data-flow-branch-label]")
      const edge = path.querySelector<HTMLElement>("[data-flow-branch-edge]")
      const target = path.querySelector<HTMLElement>(
        "[data-flow-branch-target]",
      )
      expect(label).not.toBeNull()
      expect(edge).not.toBeNull()
      expect(target).not.toBeNull()
      expect(edge?.dataset.flowTarget).toBe(target?.dataset.flowBranchTarget)
      const text = label!.textContent!.trim()
      expect(text.split(/\s+/).length).toBeLessThanOrEqual(2)
      return text
    })
    expect(new Set(labels).size).toBe(labels.length)
  }
}

describe("PR lifecycle gate map", () => {
  it("keeps review and implementation in separate workflow views", () => {
    const { container } = render(
      <PRLifecycleGateMap
        selectedDecisionPoint="pr.charter.confirm"
        workflows={workflows}
        onSelect={vi.fn()}
      />,
    )

    expect(
      screen.getByRole("heading", {
        name: "PR lifecycle gate flow",
      }),
    ).toBeInTheDocument()
    const reviewTab = screen.getByRole("tab", { name: /^Review workflow/ })
    const implementationTab = screen.getByRole("tab", {
      name: /^Implementation workflow/,
    })
    expect(reviewTab).toHaveAttribute("aria-selected", "true")
    expect(implementationTab).toHaveAttribute("aria-selected", "false")
    expect(screen.getByRole("tabpanel")).toHaveAttribute(
      "data-flow-view",
      "review",
    )
    expect(screen.getByText("Request PR review")).toBeInTheDocument()
    expect(screen.getByText("Track pull request")).toBeInTheDocument()
    expect(screen.getByText("Review pull request")).toBeInTheDocument()
    expect(screen.getByText("Assess finding scope")).toBeInTheDocument()
    expect(
      screen.queryByText("Implement selected fixes"),
    ).not.toBeInTheDocument()
    expect(
      screen.getByRole("heading", { name: "Review follow-up gates" }),
    ).toBeInTheDocument()
    expect(
      container.querySelector("[data-gate-map-viewport]"),
    ).toBeInTheDocument()
    expect(
      container.querySelector("[data-gate-map-content]"),
    ).toBeInTheDocument()

    fireEvent.click(implementationTab)

    expect(reviewTab).toHaveAttribute("aria-selected", "false")
    expect(implementationTab).toHaveAttribute("aria-selected", "true")
    expect(screen.getByRole("tabpanel")).toHaveAttribute(
      "data-flow-view",
      "implementation",
    )
    expect(screen.getByText("Load selected findings")).toBeInTheDocument()
    expect(screen.getByText("Implement selected fixes")).toBeInTheDocument()
    expect(screen.getByText("Audit candidate scope")).toBeInTheDocument()
    expect(screen.getByText("Validate changes")).toBeInTheDocument()
    expect(screen.getByText("Audit completion")).toBeInTheDocument()
    expect(screen.getByText("Push accepted changes")).toBeInTheDocument()
    expect(screen.queryByText("Request PR review")).not.toBeInTheDocument()
    expect(
      screen.getByRole("heading", { name: "Implementation follow-up gates" }),
    ).toBeInTheDocument()

    expect(screen.queryByText(/nudge/i)).not.toBeInTheDocument()
    expect(screen.queryByText(/prompt digest/i)).not.toBeInTheDocument()
    expect(screen.queryByText("pr.review.start")).not.toBeInTheDocument()
    expect(screen.queryByText("0")).not.toBeInTheDocument()
    expect(screen.queryByText("AI-W")).not.toBeInTheDocument()
    expect(screen.queryByText("AI-I")).not.toBeInTheDocument()
    expect(screen.queryByText("D → AI-I → H")).not.toBeInTheDocument()
  })

  it("uses short action descriptions, standalone gates, and labels only real branches", () => {
    const { container } = render(
      <PRLifecycleGateMap
        selectedDecisionPoint="pr.charter.confirm"
        workflows={workflows}
        onSelect={vi.fn()}
      />,
    )

    expectCurrentFlowContract(container)
    expect(
      Array.from(
        container.querySelectorAll(
          ':scope [data-flow-branch="charter-approval"] > div > [data-flow-branch-path] > [data-flow-branch-edge] [data-flow-branch-label]',
        ),
        (node) => node.textContent,
      ),
    ).toEqual(["First scope", "Revised"])
    expect(
      Array.from(
        container.querySelectorAll(
          ':scope [data-flow-branch="finding-classification"] > div > [data-flow-branch-path] > [data-flow-branch-edge] [data-flow-branch-label]',
        ),
        (node) => node.textContent,
      ),
    ).toEqual(["Clear", "Ambiguous"])
    expect(
      Array.from(
        container.querySelectorAll(
          ':scope [data-flow-branch="finding-disposition"] > div > [data-flow-branch-path] > [data-flow-branch-edge] [data-flow-branch-label]',
        ),
        (node) => node.textContent,
      ),
    ).toEqual(["In scope", "Defer", "Dismiss"])
    expect(
      screen.getByRole("region", { name: "Review publication flow" }),
    ).toContainElement(
      screen.getByRole("button", { name: "Allow review publication" }),
    )
    expect(
      screen.getByRole("region", { name: "Implementation handoff flow" }),
    ).not.toContainElement(
      screen.getByRole("button", { name: "Allow review publication" }),
    )

    fireEvent.click(
      screen.getByRole("tab", { name: /^Implementation workflow/ }),
    )

    expectCurrentFlowContract(container)
    expect(
      Array.from(
        container.querySelectorAll(
          ':scope [data-flow-branch="candidate-scope-result"] > div > [data-flow-branch-path] > [data-flow-branch-edge] [data-flow-branch-label]',
        ),
        (node) => node.textContent,
      ),
    ).toEqual(["Safe path", "Hard stop"])
    expect(
      Array.from(
        container.querySelectorAll(
          ':scope [data-flow-branch="validation-result"] > div > [data-flow-branch-path] > [data-flow-branch-edge] [data-flow-branch-label]',
        ),
        (node) => node.textContent,
      ),
    ).toEqual(["Passed", "Failed"])
    expect(
      Array.from(
        container.querySelectorAll(
          ':scope [data-flow-branch="completion-audit-result"] > div > [data-flow-branch-path] > [data-flow-branch-edge] [data-flow-branch-label]',
        ),
        (node) => node.textContent,
      ),
    ).toEqual(["No gaps", "More work"])
    expect(
      Array.from(
        container.querySelectorAll(
          ':scope [data-flow-branch="final-scope-result"] > div > [data-flow-branch-path] > [data-flow-branch-edge] [data-flow-branch-label]',
        ),
        (node) => node.textContent,
      ),
    ).toEqual(["Allowed", "Hard stop"])
    expect(
      Array.from(
        container.querySelectorAll(
          ':scope [data-flow-branch="hard-scope-resolution"] > div > [data-flow-branch-path] > [data-flow-branch-edge] [data-flow-branch-label]',
        ),
        (node) => node.textContent,
      ),
    ).toEqual(["Remove code", "Revise scope", "Stop"])
    expect(
      container.querySelectorAll(
        '[data-flow-kind="external"], [data-flow-kind="agent"], [data-flow-kind="data"]',
      ),
    ).toHaveLength(0)
  })

  it("stops hard-scope candidates before validation and loops incomplete work", () => {
    const { container } = render(
      <PRLifecycleGateMap
        selectedDecisionPoint="pr.implementation.scope"
        workflows={workflows}
        onSelect={vi.fn()}
      />,
    )

    const safePath = container.querySelector<HTMLElement>(
      '[data-flow-branch-target="scope-safe"]',
    )
    const hardStop = container.querySelector<HTMLElement>(
      '[data-flow-branch-target="scope-hard-stop"]',
    )
    expect(safePath).not.toBeNull()
    expect(hardStop).not.toBeNull()
    expect(within(safePath!).getByText("Validate changes")).toBeInTheDocument()
    expect(within(safePath!).getByText("Audit completion")).toBeInTheDocument()
    expect(
      within(safePath!).getByRole("button", {
        name: "Allow large or adjacent work",
      }),
    ).toHaveTextContent(
      "WHEN · Candidate work is large exact or necessary adjacent",
    )
    expect(
      within(safePath!).getByRole("button", { name: "Accept implementation" }),
    ).toBeInTheDocument()

    expect(
      hardStop!.querySelector('[data-required-gate="hard-scope"]'),
    ).not.toBeNull()
    expect(within(hardStop!).queryByText("Validate changes")).toBeNull()
    expect(
      within(hardStop!).queryByRole("button", {
        name: "Accept implementation",
      }),
    ).toBeNull()
    expect(within(hardStop!).getByText("Remove and defer")).toBeInTheDocument()
    expect(within(hardStop!).getByText("Revise PR charter")).toBeInTheDocument()
    expect(
      within(hardStop!).getByRole("button", {
        name: "Approve revised purpose and scope",
      }),
    ).toBeInTheDocument()
    expect(
      within(hardStop!).getByText("Stop implementation"),
    ).toBeInTheDocument()

    const moreWork = container.querySelector<HTMLElement>(
      '[data-flow-branch-target="completion-more"]',
    )
    const failedValidation = container.querySelector<HTMLElement>(
      '[data-flow-branch-target="validation-failed"]',
    )
    const finalHardStop = container.querySelector<HTMLElement>(
      '[data-flow-branch-target="final-scope-hard"]',
    )
    expect(
      within(moreWork!).getByText("Resume implementation"),
    ).toHaveTextContent("Resume implementation")
    expect(
      within(failedValidation!).getByText("Repair validation failures"),
    ).toBeInTheDocument()
    expect(
      finalHardStop!.querySelector('[data-flow-loop-target="scope-hard-stop"]'),
    ).toHaveTextContent("Return to hard-scope gate")
  })

  it("opens the owning workflow view for a deep-linked gate and supports tab keys", () => {
    const { rerender } = render(
      <PRLifecycleGateMap
        selectedDecisionPoint="pr.implementation.scope"
        workflows={workflows}
        onSelect={vi.fn()}
      />,
    )

    const reviewTab = screen.getByRole("tab", { name: /^Review workflow/ })
    const implementationTab = screen.getByRole("tab", {
      name: /^Implementation workflow/,
    })
    expect(implementationTab).toHaveAttribute("aria-selected", "true")
    expect(
      screen.getByRole("button", { name: "Allow large or adjacent work" }),
    ).toHaveAttribute("aria-pressed", "true")

    fireEvent.keyDown(implementationTab, { key: "ArrowLeft" })
    expect(reviewTab).toHaveAttribute("aria-selected", "true")
    expect(screen.getByRole("tabpanel")).toHaveAttribute(
      "data-flow-view",
      "review",
    )

    fireEvent.keyDown(reviewTab, { key: "End" })
    expect(implementationTab).toHaveAttribute("aria-selected", "true")
    expect(screen.getByRole("tabpanel")).toHaveAttribute(
      "data-flow-view",
      "implementation",
    )

    rerender(
      <PRLifecycleGateMap
        selectedDecisionPoint="pr.publication.reconcile"
        workflows={workflows}
        onSelect={vi.fn()}
      />,
    )
    expect(implementationTab).toHaveAttribute("aria-selected", "true")
    expect(
      screen.getByRole("button", { name: "Allow result reconciliation" }),
    ).toHaveAttribute("aria-pressed", "true")
  })

  it("renders every gate as an exact selectable decision point", () => {
    const { container } = render(
      <PRLifecycleGateMap
        profileID="strict"
        selectedDecisionPoint="pr.review.start"
        workflows={workflows}
        onSelect={vi.fn()}
      />,
    )

    const reviewDecisionPoints = Array.from(
      container.querySelectorAll("[data-decision-point]"),
      (node) => node.getAttribute("data-decision-point"),
    )
    expect(reviewDecisionPoints).toHaveLength(9)

    const selected = screen.getByRole("button", { name: "Allow AI review" })
    expect(selected).toHaveAttribute("aria-pressed", "true")
    expect(selected).toHaveAttribute("aria-haspopup", "dialog")
    expect(selected).toHaveAttribute("aria-expanded", "true")
    expect(selected).toHaveAttribute("data-gate-id", "pr.review.start")
    expect(selected).toHaveAttribute("data-editor-title", "Start review")
    expect(selected).toHaveAttribute(
      "data-edit-href",
      "/pull-requests?view=gate-profiles&profile=strict&gate=pr.review.start",
    )
    expect(selected).toHaveAttribute("data-workflow-configured", "true")
    expect(selected).toHaveAttribute("data-gate-format", "automatic")
    expect(selected).not.toHaveTextContent("pr.review.start")
    expect(within(selected).getByText("Allow AI review")).toBeInTheDocument()
    expect(selected).toHaveAccessibleDescription(
      /Decides whether AI may review the approved pull request scope.*Gate format: Automatic/,
    )
    expect(
      within(
        screen.getByRole("button", { name: "Approve purpose and scope" }),
      ).getByText("Approve purpose and scope"),
    ).toBeInTheDocument()

    const fallback = screen.getByRole("button", {
      name: "Approve purpose and scope",
    })
    expect(fallback).toHaveAttribute("aria-haspopup", "dialog")
    expect(fallback).toHaveAttribute("aria-expanded", "false")
    expect(fallback).toHaveAttribute("data-workflow-configured", "false")
    expect(fallback).toHaveAttribute("data-gate-format", "user")
    expect(within(fallback).getByText("default fallback")).toBeInTheDocument()

    fireEvent.click(
      screen.getByRole("tab", { name: /^Implementation workflow/ }),
    )
    const implementationDecisionPoints = Array.from(
      container.querySelectorAll("[data-decision-point]"),
      (node) => node.getAttribute("data-decision-point"),
    )
    expect(implementationDecisionPoints).toHaveLength(9)
    const decisionPoints = [
      ...reviewDecisionPoints,
      ...implementationDecisionPoints,
    ]
    expect(decisionPoints.length).toBeGreaterThan(
      prLifecycleKnownDecisionPoints.length,
    )
    expect(new Set(decisionPoints)).toEqual(
      new Set(prLifecycleKnownDecisionPoints),
    )
    expect(
      screen.getByRole("button", { name: "Accept implementation" }),
    ).not.toHaveTextContent("D → AI-I → H")
  })

  it("summarizes workflow stages as human-friendly gate formats", () => {
    render(
      <PRLifecycleGateMap
        selectedDecisionPoint="pr.review.start"
        workflows={workflows}
        onSelect={vi.fn()}
      />,
    )

    const automatic = screen.getByRole("button", { name: "Allow AI review" })
    expect(automatic).toHaveAttribute("data-gate-format", "automatic")
    expect(within(automatic).getByText("Automatic")).toBeInTheDocument()

    const rule = screen.getByRole("button", { name: "Accept review results" })
    expect(rule).toHaveAttribute("data-gate-format", "rule")
    expect(within(rule).getByText("Rule")).toBeInTheDocument()

    const ai = screen.getByRole("button", {
      name: "Decide ambiguous finding scope",
    })
    expect(ai).toHaveAttribute("data-gate-format", "ai")
    expect(within(ai).getByText("AI")).toBeInTheDocument()
    expect(ai).toHaveAccessibleDescription(/Gate format: AI\./)

    const fallback = screen.getByRole("button", {
      name: "Approve purpose and scope",
    })
    expect(fallback).toHaveAttribute("data-gate-format", "user")
    expect(fallback).toHaveTextContent("default fallback")

    const needsSetup = screen.getByRole("button", {
      name: "Allow repository lesson",
    })
    expect(needsSetup).toHaveAttribute("data-gate-format", "needs-setup")
    expect(within(needsSetup).getByText("Needs setup")).toBeInTheDocument()

    const invalidNonEmpty = screen.getByRole("button", {
      name: "Allow review publication",
    })
    expect(invalidNonEmpty).toHaveAttribute("data-gate-format", "needs-setup")
    expect(invalidNonEmpty).toHaveAttribute("data-workflow-configured", "true")
    expect(within(invalidNonEmpty).getByText("Needs setup")).toBeInTheDocument()

    fireEvent.click(
      screen.getByRole("tab", { name: /^Implementation workflow/ }),
    )

    const user = screen.getByRole("button", {
      name: "Allow AI implementation",
    })
    expect(user).toHaveAttribute("data-gate-format", "user")
    expect(within(user).getByText("User")).toBeInTheDocument()
    expect(user).not.toHaveTextContent("default fallback")

    const mixed = screen.getByRole("button", {
      name: "Accept implementation",
    })
    expect(mixed).toHaveAttribute("data-gate-format", "mixed")
    expect(within(mixed).getByText("Mixed")).toBeInTheDocument()
    expect(within(mixed).getByText("Rule → AI → User")).toBeInTheDocument()
    expect(mixed).toHaveAccessibleDescription(
      /Gate format: Mixed\. Ordered composition: Rule, then AI, then User\./,
    )
  })

  it("selects gates with pointer, Enter, and Space interaction", () => {
    const onSelect = vi.fn()
    render(
      <PRLifecycleGateMap
        selectedDecisionPoint="pr.charter.confirm"
        workflows={workflows}
        onSelect={onSelect}
      />,
    )

    fireEvent.click(
      screen.getByRole("button", { name: "Accept review results" }),
    )
    fireEvent.keyDown(
      screen.getByRole("button", { name: "Decide ambiguous finding scope" }),
      { key: "Enter" },
    )
    fireEvent.click(
      screen.getByRole("tab", { name: /^Implementation workflow/ }),
    )
    fireEvent.keyDown(
      screen.getByRole("button", {
        name: "Allow non-owned PR implementation",
      }),
      { key: " " },
    )

    expect(onSelect.mock.calls).toEqual([
      ["pr.review.complete"],
      ["pr.finding.classify"],
      ["pr.implementation.eligibility"],
    ])
  })
})
