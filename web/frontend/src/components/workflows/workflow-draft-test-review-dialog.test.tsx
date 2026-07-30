import { act, render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { beforeAll, describe, expect, it, vi } from "vitest"

import type {
  WorkflowTriggerSimulation,
  WorkflowTriggerSimulationReview,
} from "@/api/workflows"

import { WorkflowDraftTestReviewDialog } from "./workflow-draft-test-review-dialog"

describe("WorkflowDraftTestReviewDialog", () => {
  beforeAll(() => {
    Object.defineProperty(HTMLElement.prototype, "scrollIntoView", {
      configurable: true,
      value: vi.fn(),
    })
  })

  it("requires explicit consent before submitting the server review", async () => {
    const user = userEvent.setup()
    const onConfirm = vi.fn()
    render(
      <WorkflowDraftTestReviewDialog
        open
        pending={false}
        identity="review:first"
        simulation={simulation()}
        review={review()}
        onOpenChange={vi.fn()}
        onConfirm={onConfirm}
      />,
    )

    expect(screen.getByText("agent/main")).toBeInTheDocument()
    expect(screen.getByText("mcp/github/create_issue")).toBeInTheDocument()
    expect(screen.getByText(/external system state/i)).toBeInTheDocument()
    const execute = screen.getByRole("button", {
      name: "Confirm and execute",
    })
    expect(execute).toBeDisabled()
    await user.click(
      screen.getByRole("switch", {
        name: "I reviewed this server simulation and its possible effects",
      }),
    )
    await user.click(execute)
    expect(onConfirm).toHaveBeenCalledWith("review:first")
  })

  it("consumes one identity synchronously across rapid confirmation clicks", async () => {
    const user = userEvent.setup()
    const onConfirm = vi.fn()
    render(
      <WorkflowDraftTestReviewDialog
        open
        pending={false}
        identity="review:rapid"
        simulation={simulation()}
        review={review()}
        onOpenChange={vi.fn()}
        onConfirm={onConfirm}
      />,
    )
    await user.click(
      screen.getByRole("switch", {
        name: "I reviewed this server simulation and its possible effects",
      }),
    )
    const execute = screen.getByRole("button", {
      name: "Confirm and execute",
    })

    act(() => {
      execute.click()
      execute.click()
    })

    expect(onConfirm).toHaveBeenCalledTimes(1)
    expect(onConfirm).toHaveBeenCalledWith("review:rapid")
  })

  it("renders only safe server context counts, never scenario values", () => {
    render(
      <WorkflowDraftTestReviewDialog
        open
        pending={false}
        identity="review:redacted"
        simulation={{
          ...simulation(),
          context_summary: {
            input_count: 2,
            secret_count: 3,
            has_event: true,
            has_session: true,
            has_delivery: true,
          },
        }}
        review={review()}
        onOpenChange={vi.fn()}
        onConfirm={vi.fn()}
      />,
    )

    expect(screen.getByText("3")).toBeInTheDocument()
    expect(screen.getByText("session + delivery")).toBeInTheDocument()
    expect(screen.queryByText("top-secret-value")).not.toBeInTheDocument()
    expect(screen.queryByText("protected-payload")).not.toBeInTheDocument()
  })

  it("blocks consent for an incomplete server review", () => {
    render(
      <WorkflowDraftTestReviewDialog
        open
        pending={false}
        identity="review:incomplete"
        simulation={{ ...simulation(), executable: false }}
        review={{
          ...review(),
          complete: false,
          limits: ["effects_truncated"],
        }}
        onOpenChange={vi.fn()}
        onConfirm={vi.fn()}
      />,
    )

    expect(screen.getByRole("alert")).toHaveTextContent(
      "server review is incomplete",
    )
    expect(
      screen.getByRole("switch", {
        name: "I reviewed this server simulation and its possible effects",
      }),
    ).toBeDisabled()
  })

  it("invalidates consent when the exact server identity changes", async () => {
    const user = userEvent.setup()
    const props = {
      open: true,
      pending: false,
      simulation: simulation(),
      review: review(),
      onOpenChange: vi.fn(),
      onConfirm: vi.fn(),
    }
    const { rerender } = render(
      <WorkflowDraftTestReviewDialog identity="review:first" {...props} />,
    )
    const consent = screen.getByRole("switch", {
      name: "I reviewed this server simulation and its possible effects",
    })
    await user.click(consent)
    expect(consent).toBeChecked()

    rerender(
      <WorkflowDraftTestReviewDialog identity="review:second" {...props} />,
    )

    expect(consent).not.toBeChecked()
    expect(
      screen.getByRole("button", { name: "Confirm and execute" }),
    ).toBeDisabled()
  })

  it("clears consent synchronously when closed", async () => {
    const user = userEvent.setup()
    const onOpenChange = vi.fn()
    const props = {
      open: true,
      pending: false,
      identity: "review:same",
      simulation: simulation(),
      review: review(),
      onOpenChange,
      onConfirm: vi.fn(),
    }
    const { rerender } = render(<WorkflowDraftTestReviewDialog {...props} />)
    const consent = screen.getByRole("switch", {
      name: "I reviewed this server simulation and its possible effects",
    })
    await user.click(consent)
    await user.click(screen.getByRole("button", { name: "Keep editing" }))
    expect(onOpenChange).toHaveBeenCalledWith(false)
    rerender(<WorkflowDraftTestReviewDialog {...props} />)
    expect(consent).not.toBeChecked()
  })
})

function simulation(): WorkflowTriggerSimulation {
  return {
    selected_kind: "manual",
    effective_kind: "manual",
    present: true,
    matched: true,
    executable: true,
    reason: "matched",
    context_summary: {
      input_count: 1,
      secret_count: 1,
      has_event: false,
      has_session: false,
      has_delivery: false,
    },
  }
}

function review(): WorkflowTriggerSimulationReview {
  return {
    job_count: 1,
    step_count: 2,
    targets: ["agent/main", "mcp/github/create_issue"],
    effects: [
      {
        kind: "external_state_change_possible",
        target: "mcp/github/create_issue",
        occurrences: 1,
      },
    ],
    complete: true,
    validation: {
      valid: true,
      issue_count: 0,
      issues: [],
      truncated: false,
    },
    limits: [],
  }
}
