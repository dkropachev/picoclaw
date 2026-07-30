import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { describe, expect, it, vi } from "vitest"

import { WorkflowBuilderTabs } from "./workflow-builder-tabs"

describe("WorkflowBuilderTabs", () => {
  it("supports pointer and keyboard selection", async () => {
    const user = userEvent.setup()
    const onSectionChange = vi.fn()
    const { rerender } = render(
      <WorkflowBuilderTabs
        section="triggers"
        onSectionChange={onSectionChange}
      />,
    )

    await user.click(screen.getByRole("tab", { name: "Jobs & actions" }))
    expect(onSectionChange).toHaveBeenCalledWith("jobs")

    rerender(
      <WorkflowBuilderTabs section="jobs" onSectionChange={onSectionChange} />,
    )
    const jobs = screen.getByRole("tab", { name: "Jobs & actions" })
    jobs.focus()
    await user.keyboard("{ArrowLeft}")
    expect(onSectionChange).toHaveBeenLastCalledWith("triggers")
  })

  it("retains the active section while an editor has pending changes", async () => {
    const user = userEvent.setup()
    const onSectionChange = vi.fn()
    render(
      <WorkflowBuilderTabs
        section="jobs"
        disabledReason="Apply or reset the job changes first."
        onSectionChange={onSectionChange}
      />,
    )

    const triggers = screen.getByRole("tab", { name: "Triggers" })
    await user.click(triggers)
    expect(onSectionChange).not.toHaveBeenCalled()
    expect(triggers).toHaveAttribute(
      "title",
      "Apply or reset the job changes first.",
    )
    expect(triggers).toHaveAttribute("aria-disabled", "true")

    triggers.focus()
    await user.keyboard("{Enter}{ArrowRight}{Home}")
    expect(onSectionChange).not.toHaveBeenCalled()
  })
})
