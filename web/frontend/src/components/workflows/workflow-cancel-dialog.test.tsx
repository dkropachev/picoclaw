import { fireEvent, render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { describe, expect, it, vi } from "vitest"

import { WorkflowCancelDialog } from "./workflow-cancel-dialog"
import { workflowCancelReason } from "./workflow-cancel-reason"

const target = {
  id: "wr_running",
  workflowRef: "workflows/review.yml",
}

describe("WorkflowCancelDialog", () => {
  it("requires a trimmed nonblank reason within the UTF-8 byte limit", () => {
    expect(workflowCancelReason(" \n ")).toEqual({
      reason: "",
      bytes: 0,
      error: "A cancel reason is required.",
    })
    expect(workflowCancelReason(` ${"é".repeat(512)} `)).toMatchObject({
      reason: "é".repeat(512),
      bytes: 1024,
      error: null,
    })
    expect(workflowCancelReason("é".repeat(513))).toMatchObject({
      bytes: 1026,
      error: "Cancel reason must not exceed 1024 UTF-8 bytes.",
    })
  })

  it("focuses the reason, submits the exact target, and keeps state on failure", async () => {
    const user = userEvent.setup()
    const onConfirm = vi.fn()
    const onDismiss = vi.fn()
    const view = render(
      <WorkflowCancelDialog
        target={target}
        pending={false}
        onDismiss={onDismiss}
        onConfirm={onConfirm}
      />,
    )

    const reason = await screen.findByRole("textbox", {
      name: "Cancel reason",
    })
    expect(reason).toHaveFocus()
    expect(screen.getByRole("button", { name: "Cancel run" })).toBeDisabled()

    await user.type(reason, "  operator intervention  ")
    await user.click(screen.getByRole("button", { name: "Cancel run" }))
    expect(onConfirm).toHaveBeenCalledWith("operator intervention")
    expect(screen.getByRole("alertdialog")).toBeVisible()

    view.rerender(
      <WorkflowCancelDialog
        target={target}
        pending
        requestError="The workflow run could not be canceled."
        onDismiss={onDismiss}
        onConfirm={onConfirm}
      />,
    )
    expect(reason).toHaveValue("  operator intervention  ")
    expect(screen.getByRole("alert")).toHaveTextContent(
      "The workflow run could not be canceled.",
    )
    fireEvent.keyDown(document, { key: "Escape" })
    expect(onDismiss).not.toHaveBeenCalled()
  })

  it("reports multibyte overflow without relying on character maxLength", async () => {
    const user = userEvent.setup()
    render(
      <WorkflowCancelDialog
        target={target}
        pending={false}
        onDismiss={vi.fn()}
        onConfirm={vi.fn()}
      />,
    )

    const reason = await screen.findByRole("textbox", {
      name: "Cancel reason",
    })
    fireEvent.change(reason, { target: { value: "é".repeat(513) } })
    expect(
      screen.getByText("Cancel reason must not exceed 1024 UTF-8 bytes."),
    ).toBeVisible()
    expect(screen.getByText("1026 / 1024 UTF-8 bytes")).toBeVisible()
    expect(screen.getByRole("button", { name: "Cancel run" })).toBeDisabled()

    await user.clear(reason)
    await user.type(reason, "operator")
    expect(screen.getByRole("button", { name: "Cancel run" })).toBeEnabled()
  })
})
