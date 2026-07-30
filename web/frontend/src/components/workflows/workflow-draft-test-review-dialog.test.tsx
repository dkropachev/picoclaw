import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { beforeAll, describe, expect, it, vi } from "vitest"

import {
  type WorkflowDraftTestReviewContext,
  workflowDraftTestReviewIdentity,
} from "./workflow-draft-test-review"
import { WorkflowDraftTestReviewDialog } from "./workflow-draft-test-review-dialog"

describe("WorkflowDraftTestReviewDialog", () => {
  beforeAll(() => {
    Object.defineProperty(HTMLElement.prototype, "scrollIntoView", {
      configurable: true,
      value: vi.fn(),
    })
  })

  it("requires a second explicit effects confirmation", async () => {
    const user = userEvent.setup()
    const onConfirm = vi.fn()
    render(
      <WorkflowDraftTestReviewDialog
        open
        pending={false}
        identity="draft:first"
        scenario="Durable event ev_123"
        review={{
          jobCount: 1,
          stepCount: 2,
          targets: ["agent/main", "mcp/github/create_issue"],
          rawOnlyCount: 0,
        }}
        onOpenChange={vi.fn()}
        onConfirm={onConfirm}
      />,
    )

    expect(screen.getByText("agent/main")).toBeInTheDocument()
    expect(screen.getByText("mcp/github/create_issue")).toBeInTheDocument()
    expect(screen.getByText(/external system state/i)).toBeInTheDocument()
    const run = screen.getByRole("button", {
      name: "Confirm and run test",
    })
    expect(run).toBeDisabled()
    await user.click(
      screen.getByRole("switch", {
        name: "I reviewed this scenario and its possible effects",
      }),
    )
    await user.click(run)
    expect(onConfirm).toHaveBeenCalledWith("draft:first")
  })

  it("warns when raw-only actions cannot be summarized", () => {
    render(
      <WorkflowDraftTestReviewDialog
        open
        pending={false}
        identity="draft:raw"
        scenario="Manual inputs"
        review={{
          jobCount: 1,
          stepCount: 0,
          targets: [],
          rawOnlyCount: 1,
        }}
        onOpenChange={vi.fn()}
        onConfirm={vi.fn()}
      />,
    )

    expect(screen.getByRole("alert")).toHaveTextContent(
      "1 preserved item is raw-only",
    )
    expect(screen.getByText(/transitive effects/i)).toBeInTheDocument()
  })

  it("shows exact nonsecret manual context and secret names without secret values", () => {
    render(
      <WorkflowDraftTestReviewDialog
        open
        pending={false}
        identity="draft:manual-context"
        scenario="Manual or workflow-call test."
        scenarioDetails={[
          { label: "Mode", value: "Manual / workflow call" },
          { label: "Inputs JSON", value: '{"ticket":"PIC-42"}' },
          { label: "Session", value: "workflow:test" },
          {
            label: "Delivery JSON",
            value: '{"channel":"engineering"}',
          },
          { label: "Secrets", value: "2 configured: api_key, token" },
        ]}
        review={{
          jobCount: 1,
          stepCount: 1,
          targets: ["tool/message"],
          rawOnlyCount: 0,
        }}
        onOpenChange={vi.fn()}
        onConfirm={vi.fn()}
      />,
    )

    expect(screen.getByText('{"ticket":"PIC-42"}')).toBeInTheDocument()
    expect(screen.getByText("workflow:test")).toBeInTheDocument()
    expect(screen.getByText('{"channel":"engineering"}')).toBeInTheDocument()
    expect(screen.getByText("2 configured: api_key, token")).toBeInTheDocument()
    expect(screen.queryByText(/secret-value/i)).not.toBeInTheDocument()
  })

  it("resets acknowledgement when the exact reviewed draft changes", async () => {
    const user = userEvent.setup()
    const onConfirm = vi.fn()
    const review = {
      jobCount: 1,
      stepCount: 1,
      targets: ["tool/message"],
      rawOnlyCount: 0,
    }
    const { rerender } = render(
      <WorkflowDraftTestReviewDialog
        open
        pending={false}
        identity="draft:first"
        scenario="Manual inputs"
        review={review}
        onOpenChange={vi.fn()}
        onConfirm={onConfirm}
      />,
    )
    const acknowledgement = screen.getByRole("switch", {
      name: "I reviewed this scenario and its possible effects",
    })
    await user.click(acknowledgement)
    expect(acknowledgement).toBeChecked()

    rerender(
      <WorkflowDraftTestReviewDialog
        open
        pending={false}
        identity="draft:second"
        scenario="Durable event ev_456"
        review={{
          ...review,
          targets: ["tool/message", "workflows/child.yml"],
        }}
        onOpenChange={vi.fn()}
        onConfirm={onConfirm}
      />,
    )

    expect(acknowledgement).not.toBeChecked()
    expect(
      screen.getByRole("button", { name: "Confirm and run test" }),
    ).toBeDisabled()
    expect(screen.getByText(/transitive effects/i)).toBeInTheDocument()
  })

  it("clears acknowledgement synchronously on close before the same identity reopens", async () => {
    const user = userEvent.setup()
    const onOpenChange = vi.fn()
    const props = {
      open: true,
      pending: false,
      identity: "draft:same",
      scenario: "Manual inputs",
      review: {
        jobCount: 1,
        stepCount: 1,
        targets: ["tool/message"],
        rawOnlyCount: 0,
      },
      onOpenChange,
      onConfirm: vi.fn(),
    }
    const { rerender } = render(<WorkflowDraftTestReviewDialog {...props} />)
    const acknowledgement = screen.getByRole("switch", {
      name: "I reviewed this scenario and its possible effects",
    })
    await user.click(acknowledgement)
    expect(acknowledgement).toBeChecked()

    await user.click(screen.getByRole("button", { name: "Keep editing" }))
    expect(onOpenChange).toHaveBeenCalledWith(false)
    rerender(<WorkflowDraftTestReviewDialog {...props} />)

    expect(acknowledgement).not.toBeChecked()
    expect(
      screen.getByRole("button", { name: "Confirm and run test" }),
    ).toBeDisabled()
  })

  it("adds a conservative warning for mixed known and advanced targets", () => {
    render(
      <WorkflowDraftTestReviewDialog
        open
        pending={false}
        identity="draft:mixed"
        scenario="Manual inputs"
        review={{
          jobCount: 1,
          stepCount: 2,
          targets: ["agent/main", "advanced/custom"],
          rawOnlyCount: 0,
        }}
        onOpenChange={vi.fn()}
        onConfirm={vi.fn()}
      />,
    )

    expect(screen.getByText(/delegated tools/i)).toBeInTheDocument()
    expect(screen.getByText(/transitive effects/i)).toBeInTheDocument()
  })

  it.each([
    ["targetRef", "workflows/changed.yml"],
    ["yaml", "name: Changed\njobs: {}\n"],
    ["prompt", "Changed prompt"],
    ["inputsJSON", '{"ticket":"changed"}'],
    ["secretsJSON", '{"token":"changed"}'],
    ["session", "workflow:changed"],
    ["deliveryJSON", '{"channel":"changed"}'],
  ] as const)(
    "binds acknowledgement to the exact manual %s value",
    (field, value) => {
      const context = manualReviewContext()
      expect(
        workflowDraftTestReviewIdentity({ ...context, [field]: value }),
      ).not.toBe(workflowDraftTestReviewIdentity(context))
    },
  )

  it("binds event acknowledgement to mode and exact event ID without exposing unrelated manual fields", () => {
    const manual = manualReviewContext()
    const event: WorkflowDraftTestReviewContext = {
      ...manual,
      mode: "event",
      eventID: "ev_first",
    }
    const identity = workflowDraftTestReviewIdentity(event)

    expect(identity).not.toBe(workflowDraftTestReviewIdentity(manual))
    expect(
      workflowDraftTestReviewIdentity({ ...event, eventID: "ev_second" }),
    ).not.toBe(identity)
    expect(
      workflowDraftTestReviewIdentity({
        ...event,
        secretsJSON: '{"hidden":"changed"}',
      }),
    ).toBe(identity)
  })
})

function manualReviewContext(): WorkflowDraftTestReviewContext {
  return {
    targetRef: "workflows/review.yml",
    yaml: "name: Review\njobs: {}\n",
    prompt: "Review changes",
    mode: "manual",
    inputsJSON: '{"ticket":"123"}',
    secretsJSON: '{"token":"secret-ref"}',
    session: "workflow:test",
    deliveryJSON: '{"channel":"same"}',
    review: {
      jobCount: 1,
      stepCount: 1,
      targets: ["agent/main"],
      rawOnlyCount: 0,
    },
  }
}
