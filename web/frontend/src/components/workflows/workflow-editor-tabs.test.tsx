import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { useState } from "react"
import { describe, expect, it } from "vitest"

import {
  type WorkflowEditorMode,
  WorkflowEditorTabs,
} from "@/components/workflows/workflow-editor-tabs"

describe("WorkflowEditorTabs", () => {
  it("uses roving tab focus and selects tabs with horizontal and boundary keys", async () => {
    const user = userEvent.setup()
    render(<ControlledTabs />)
    const builder = screen.getByRole("tab", { name: "Builder" })
    const yaml = screen.getByRole("tab", { name: "YAML" })

    expect(builder).toHaveAttribute("tabindex", "0")
    expect(yaml).toHaveAttribute("tabindex", "-1")
    builder.focus()

    await user.keyboard("{ArrowRight}")
    expect(yaml).toHaveFocus()
    expect(yaml).toHaveAttribute("aria-selected", "true")
    expect(yaml).toHaveAttribute("tabindex", "0")
    expect(builder).toHaveAttribute("tabindex", "-1")

    await user.keyboard("{ArrowRight}")
    expect(builder).toHaveFocus()
    expect(builder).toHaveAttribute("aria-selected", "true")

    await user.keyboard("{ArrowLeft}")
    expect(yaml).toHaveFocus()
    expect(yaml).toHaveAttribute("aria-selected", "true")

    await user.keyboard("{Home}")
    expect(builder).toHaveFocus()
    expect(builder).toHaveAttribute("aria-selected", "true")

    await user.keyboard("{End}")
    expect(yaml).toHaveFocus()
    expect(yaml).toHaveAttribute("aria-selected", "true")
  })
})

function ControlledTabs() {
  const [mode, setMode] = useState<WorkflowEditorMode>("builder")
  return <WorkflowEditorTabs mode={mode} onModeChange={setMode} />
}
