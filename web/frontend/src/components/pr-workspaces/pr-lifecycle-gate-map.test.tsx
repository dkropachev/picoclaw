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
    expect(screen.getByText("PR purpose and scope")).toBeInTheDocument()
    expect(
      screen.queryByText("Confirm purpose and scope"),
    ).not.toBeInTheDocument()
    expect(screen.getByText("AI review")).toBeInTheDocument()
    expect(
      screen.getByText("Choose what to do with findings"),
    ).toBeInTheDocument()
    expect(screen.getByText("Selected findings to fix")).toBeInTheDocument()
    expect(
      screen
        .getByText("Selected findings to fix")
        .closest('[data-flow-kind="data"]'),
    ).toBeInTheDocument()
    expect(screen.getByText("Continue to implementation")).toBeInTheDocument()
    expect(screen.getByText("AI implementation")).toBeInTheDocument()
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

    const classify = screen.getByRole("button", {
      name: "Decide ambiguous finding scope",
    })
    expect(
      classify.closest('[data-flow-branch="findings-decision"]'),
    ).toBeInTheDocument()
    const scope = screen.getByRole("button", {
      name: "Allow large or adjacent work",
    })
    expect(
      scope.closest('[data-flow-branch="candidate-scope"]'),
    ).toBeInTheDocument()

    const nonOwned = screen.getByRole("button", {
      name: "Allow non-owned PR implementation",
    })
    const startImplementation = screen.getByRole("button", {
      name: "Allow AI implementation",
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
        name: "Approve revised purpose and scope",
      }),
    ).toBeInTheDocument()
    expect(
      within(advancedRail!).getByRole("button", {
        name: "Allow repository lesson",
      }),
    ).toBeInTheDocument()
    expect(
      within(advancedRail!).getByRole("button", {
        name: "Allow result reconciliation",
      }),
    ).toBeInTheDocument()

    expect(screen.queryByText(/nudge/i)).not.toBeInTheDocument()
    expect(screen.queryByText(/prompt digest/i)).not.toBeInTheDocument()
    expect(screen.queryByText("pr.review.start")).not.toBeInTheDocument()
    expect(screen.queryByText("0")).not.toBeInTheDocument()
    expect(screen.queryByText("AI-W")).not.toBeInTheDocument()
    expect(screen.queryByText("AI-I")).not.toBeInTheDocument()
    expect(screen.queryByText("D → AI-I → H")).not.toBeInTheDocument()

    const aiActions = Array.from(
      container.querySelectorAll('[data-flow-kind="agent"]'),
      (node) => node.querySelector("strong")?.textContent,
    )
    expect(aiActions).toEqual(["AI review", "AI implementation"])
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

    const selected = screen.getByRole("button", { name: "Allow AI review" })
    expect(selected).toHaveAttribute("aria-pressed", "true")
    expect(selected).toHaveAttribute("data-gate-id", "pr.review.start")
    expect(selected).toHaveAttribute("data-editor-title", "Start review")
    expect(selected).toHaveAttribute(
      "data-edit-href",
      "/pull-requests?view=gate-profiles&gate=pr.review.start",
    )
    expect(selected).toHaveAttribute("data-workflow-configured", "true")
    expect(selected).toHaveAttribute("data-gate-format", "automatic")
    expect(selected).not.toHaveTextContent("pr.review.start")
    expect(within(selected).getByText("Allow AI review")).toBeInTheDocument()
    expect(
      within(
        screen.getByRole("button", { name: "Approve purpose and scope" }),
      ).getByText("Approve purpose and scope"),
    ).toBeInTheDocument()

    const fallback = screen.getByRole("button", {
      name: "Approve purpose and scope",
    })
    expect(fallback).toHaveAttribute("data-workflow-configured", "false")
    expect(fallback).toHaveAttribute("data-gate-format", "user")
    expect(within(fallback).getByText("default fallback")).toBeInTheDocument()
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

    const user = screen.getByRole("button", {
      name: "Allow AI implementation",
    })
    expect(user).toHaveAttribute("data-gate-format", "user")
    expect(within(user).getByText("User")).toBeInTheDocument()
    expect(user).not.toHaveTextContent("default fallback")

    const fallback = screen.getByRole("button", {
      name: "Approve purpose and scope",
    })
    expect(fallback).toHaveAttribute("data-gate-format", "user")
    expect(fallback).toHaveTextContent("default fallback")

    const mixed = screen.getByRole("button", {
      name: "Accept implementation",
    })
    expect(mixed).toHaveAttribute("data-gate-format", "mixed")
    expect(within(mixed).getByText("Mixed")).toBeInTheDocument()
    expect(within(mixed).getByText("Rule → AI → User")).toBeInTheDocument()
    expect(mixed).toHaveAccessibleDescription(
      /Gate format: Mixed\. Ordered composition: Rule, then AI, then User\./,
    )

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
