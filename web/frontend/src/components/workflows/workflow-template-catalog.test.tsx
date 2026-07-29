import { render, screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { describe, expect, it, vi } from "vitest"

import { WorkflowTemplateCatalog } from "./workflow-template-catalog"

describe("WorkflowTemplateCatalog", () => {
  it("offers install, installed, modified, and blocked states safely", async () => {
    const onInstall = vi.fn()
    const user = userEvent.setup()
    render(
      <WorkflowTemplateCatalog
        templates={[
          {
            name: "available-template",
            ref: "workflows/available.yml",
            state: "available",
          },
          {
            name: "installed-template",
            ref: "workflows/installed.yml",
            state: "installed",
          },
          {
            name: "modified-template",
            ref: "workflows/modified.yml",
            state: "modified",
          },
          {
            name: "blocked-template",
            ref: "workflows/blocked.yml",
            state: "blocked",
            blocked_reason: "target_not_regular",
          },
        ]}
        loading={false}
        unavailable={false}
        onInstall={onInstall}
      />,
    )

    const available = screen.getByRole("article", {
      name: "Available Template template",
    })
    await user.click(within(available).getByRole("button", { name: "Install" }))
    expect(onInstall).toHaveBeenCalledWith("available-template", false)

    expect(
      screen.getByText("Installed and byte-for-byte current"),
    ).toBeInTheDocument()
    expect(
      screen.getByText(
        "The target is not a regular file. Resolve it manually.",
      ),
    ).toBeInTheDocument()
    expect(
      within(
        screen.getByRole("article", { name: "Blocked Template template" }),
      ).queryByRole("button"),
    ).not.toBeInTheDocument()
  })

  it("requires explicit confirmation before restoring a modified template", async () => {
    const onInstall = vi.fn()
    const user = userEvent.setup()
    render(
      <WorkflowTemplateCatalog
        templates={[
          {
            name: "code-review",
            ref: "workflows/code-review.yml",
            state: "modified",
          },
        ]}
        loading={false}
        unavailable={false}
        onInstall={onInstall}
      />,
    )

    await user.click(screen.getByRole("button", { name: "Restore built-in" }))
    const dialog = screen.getByRole("alertdialog")
    expect(
      within(dialog).getByText(/replaces local changes/i),
    ).toBeInTheDocument()
    expect(onInstall).not.toHaveBeenCalled()

    await user.click(
      within(dialog).getByRole("button", { name: "Restore built-in" }),
    )
    expect(onInstall).toHaveBeenCalledWith("code-review", true)
  })

  it("keeps catalog states visible but disables mutations during active development", () => {
    render(
      <WorkflowTemplateCatalog
        templates={[
          {
            name: "available-template",
            ref: "workflows/available.yml",
            state: "available",
          },
          {
            name: "modified-template",
            ref: "workflows/modified.yml",
            state: "modified",
          },
        ]}
        loading={false}
        unavailable={false}
        disabled
        disabledReason="Finish or discard the active workflow draft before installing or restoring templates."
        onInstall={vi.fn()}
      />,
    )

    expect(screen.getByRole("status")).toHaveTextContent(
      "Finish or discard the active workflow draft",
    )
    expect(screen.getByRole("button", { name: "Install" })).toBeDisabled()
    expect(
      screen.getByRole("button", { name: "Restore built-in" }),
    ).toBeDisabled()
  })

  it("uses a bounded unavailable message", () => {
    render(
      <WorkflowTemplateCatalog
        templates={[]}
        loading={false}
        unavailable
        onInstall={vi.fn()}
      />,
    )

    expect(screen.getByRole("alert")).toHaveTextContent(
      "Built-in workflow templates are unavailable. Refresh to try again.",
    )
  })

  it("shows operator guidance instead of retry advice for recovery conflicts", () => {
    render(
      <WorkflowTemplateCatalog
        templates={[]}
        loading={false}
        unavailable
        unavailableMessage="Workflow recovery found files changed outside the interrupted transaction. Operator reconciliation is required; no files were changed."
        onInstall={vi.fn()}
      />,
    )

    expect(screen.getByRole("alert")).toHaveTextContent(
      "Operator reconciliation is required",
    )
    expect(screen.getByRole("alert")).not.toHaveTextContent("Refresh")
  })
})
