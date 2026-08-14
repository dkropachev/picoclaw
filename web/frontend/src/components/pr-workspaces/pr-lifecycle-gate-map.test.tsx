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
  "pr.implementation.complete": {
    id: "implementation-complete",
    name: "Complete implementation",
    purpose: "authorization",
    decision_point: "pr.implementation.complete",
    stages: [
      { id: "scope", kind: "deterministic", when: "true" },
      { id: "audit", kind: "ai_isolated_context", agent_id: "controller" },
      { id: "approve", kind: "human", questions: ["Accept?"] },
    ],
  },
}

describe("PR lifecycle gate map", () => {
  it("shows the user-visible review, implementation, and publication flow", () => {
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
    expect(screen.getByText("GitHub review request")).toBeInTheDocument()
    expect(screen.getByText("Track PR in PicoClaw")).toBeInTheDocument()
    expect(
      screen.getByText(
        "User action or explicitly configured automation; no built-in automatic workspace bridge",
      ),
    ).toBeInTheDocument()
    expect(screen.getByText("Confirm purpose and scope")).toBeInTheDocument()
    expect(screen.getByText("AI reviews the pull request")).toBeInTheDocument()
    expect(
      screen.getByText("Choose what to do with findings"),
    ).toBeInTheDocument()
    expect(screen.getByText("Implement selected findings")).toBeInTheDocument()
    expect(screen.getByText("Continue to implementation")).toBeInTheDocument()
    expect(screen.getByText("Check candidate scope")).toBeInTheDocument()
    expect(screen.getByText("Validate changes")).toBeInTheDocument()
    expect(screen.getByText("Deferred findings")).toBeInTheDocument()
    expect(screen.getByText("GitHub issues")).toBeInTheDocument()
    expect(
      screen.getByRole("heading", { name: "Advanced / exception gates" }),
    ).toBeInTheDocument()
    expect(
      container.querySelectorAll('[data-flow-kind="data"]'),
    ).not.toHaveLength(0)
    expect(
      container.querySelector('[data-flow-edge="accept completion"]'),
    ).toBeInTheDocument()
    expect(
      screen.getByText(
        "Remove extra code · revise purpose/scope · defer follow-up · stop",
      ),
    ).toBeInTheDocument()
    expect(
      screen.getByText(
        "↺ Repair candidate, then recheck scope before validation",
      ),
    ).toBeInTheDocument()

    const classify = screen.getByRole("button", { name: "Classify finding" })
    expect(
      classify.closest('[data-flow-branch="findings-decision"]'),
    ).toBeInTheDocument()
    const scope = screen.getByRole("button", {
      name: "Classify implementation",
    })
    expect(
      scope.closest('[data-flow-branch="candidate-scope"]'),
    ).toBeInTheDocument()

    const nonOwned = screen.getByRole("button", {
      name: "Authorize non-owned PR",
    })
    const startImplementation = screen.getByRole("button", {
      name: "Start implementation",
    })
    expect(
      nonOwned.compareDocumentPosition(startImplementation) &
        Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBe(Node.DOCUMENT_POSITION_FOLLOWING)

    const advancedHeading = screen.getByRole("heading", {
      name: "Advanced / exception gates",
    })
    const advancedRail = advancedHeading.closest("section")
    expect(advancedRail).not.toBeNull()
    expect(within(advancedRail!).getAllByRole("button")).toHaveLength(3)
    expect(
      within(advancedRail!).getByRole("button", {
        name: "Confirm revised charter",
      }),
    ).toBeInTheDocument()
    expect(
      within(advancedRail!).getByRole("button", {
        name: "Promote correction",
      }),
    ).toBeInTheDocument()
    expect(
      within(advancedRail!).getByRole("button", {
        name: "Resolve unknown result",
      }),
    ).toBeInTheDocument()

    expect(screen.queryByText(/nudge/i)).not.toBeInTheDocument()
    expect(screen.queryByText(/prompt digest/i)).not.toBeInTheDocument()
    expect(screen.queryByText("pr.review.start")).not.toBeInTheDocument()
    expect(screen.queryByText("0")).not.toBeInTheDocument()
    expect(screen.queryByText("AI-W")).not.toBeInTheDocument()
    expect(screen.queryByText("AI-I")).not.toBeInTheDocument()
    expect(screen.queryByText("D → AI-I → H")).not.toBeInTheDocument()
    expect(screen.queryByText(/fallback/i)).not.toBeInTheDocument()
  })

  it("renders every gate as an exact selectable decision point", () => {
    const { container } = render(
      <PRLifecycleGateMap
        selectedDecisionPoint="pr.review.start"
        workflows={workflows}
        onSelect={vi.fn()}
      />,
    )

    expect(screen.getAllByRole("button")).toHaveLength(14)
    const decisionPoints = Array.from(
      container.querySelectorAll("[data-decision-point]"),
      (node) => node.getAttribute("data-decision-point"),
    )
    expect(decisionPoints).toHaveLength(prLifecycleKnownDecisionPoints.length)
    expect(new Set(decisionPoints)).toEqual(
      new Set(prLifecycleKnownDecisionPoints),
    )

    const selected = screen.getByRole("button", { name: "Start review" })
    expect(selected).toHaveAttribute("aria-pressed", "true")
    expect(selected).toHaveAttribute("data-gate-id", "pr.review.start")
    expect(selected).toHaveAttribute(
      "data-edit-href",
      "/pull-requests?view=gate-profiles&gate=pr.review.start",
    )
    expect(selected).toHaveAttribute("data-workflow-configured", "true")
    expect(selected).not.toHaveTextContent("pr.review.start")

    const fallback = screen.getByRole("button", {
      name: "Complete review",
    })
    expect(fallback).toHaveAttribute("data-workflow-configured", "false")
    expect(fallback).not.toHaveTextContent("FALLBACK")
    expect(
      screen.getByRole("button", { name: "Complete implementation" }),
    ).not.toHaveTextContent("D → AI-I → H")
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

    fireEvent.click(screen.getByRole("button", { name: "Complete review" }))
    fireEvent.keyDown(
      screen.getByRole("button", { name: "Classify finding" }),
      { key: "Enter" },
    )
    fireEvent.keyDown(
      screen.getByRole("button", { name: "Authorize non-owned PR" }),
      { key: " " },
    )

    expect(onSelect.mock.calls).toEqual([
      ["pr.review.complete"],
      ["pr.finding.classify"],
      ["pr.implementation.eligibility"],
    ])
  })
})
