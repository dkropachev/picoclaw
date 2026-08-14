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
  it("renders every gate as an exact selectable decision point", () => {
    const { container } = render(
      <PRLifecycleGateMap
        selectedDecisionPoint="pr.review.start"
        workflows={workflows}
        onSelect={vi.fn()}
      />,
    )

    expect(screen.getAllByRole("button")).toHaveLength(14)
    expect(
      Array.from(container.querySelectorAll("[data-decision-point]")).map(
        (node) => node.getAttribute("data-decision-point"),
      ),
    ).toEqual(prLifecycleKnownDecisionPoints)

    const selected = screen.getByRole("button", { name: "Start review" })
    expect(selected).toHaveAttribute("aria-pressed", "true")
    expect(selected).toHaveAttribute("data-gate-id", "pr.review.start")
    expect(selected).toHaveAttribute(
      "data-edit-href",
      "/pull-requests?view=gate-profiles&gate=pr.review.start",
    )
    expect(selected).toHaveAttribute("data-workflow-configured", "true")
    expect(within(selected).getByText("0")).toBeInTheDocument()

    const fallback = screen.getByRole("button", {
      name: "Complete review",
    })
    expect(fallback).toHaveAttribute("data-workflow-configured", "false")
    expect(
      within(fallback).getByText("NOT CONFIGURED · FALLBACK H"),
    ).toBeInTheDocument()

    expect(
      within(
        screen.getByRole("button", { name: "Complete implementation" }),
      ).getByText("D → AI-I → H"),
    ).toBeInTheDocument()
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
