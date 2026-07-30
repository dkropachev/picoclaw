import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { describe, expect, it, vi } from "vitest"

import type { ToolSupportItem } from "@/api/tools"

import { ToolLibraryTab } from "./tool-library-tab"

describe("ToolLibraryTab", () => {
  it("keeps a dependency-blocked tool configured on and operable", async () => {
    const user = userEvent.setup()
    const onToggleTool = vi.fn()
    const tool = blockedTool("requires_workflows")

    renderLibrary(tool, onToggleTool)

    const toggle = screen.getByRole("switch", {
      name: "Toggle workflow",
    })
    expect(toggle).toBeChecked()
    expect(toggle).toBeEnabled()
    const descriptionID = toggle.getAttribute("aria-describedby")
    expect(descriptionID).toBeTruthy()
    expect(document.getElementById(descriptionID ?? "")).toHaveTextContent(
      "Enable workflows before the workflow tool can be used.",
    )

    await user.click(toggle)
    expect(onToggleTool).toHaveBeenCalledWith("workflow", false)
  })

  it("uses a safe message for an unknown dependency reason", () => {
    renderLibrary(blockedTool("future_dependency"), vi.fn())

    expect(
      screen.getByText("This tool is blocked by an unmet dependency."),
    ).toBeInTheDocument()
    expect(screen.queryByText(/future_dependency/)).not.toBeInTheDocument()
  })
})

function blockedTool(reasonCode: string): ToolSupportItem {
  return {
    name: "workflow",
    description: "Run and inspect workflows.",
    category: "automation",
    config_key: "tools.workflow",
    status: "blocked",
    reason_code: reasonCode,
  }
}

function renderLibrary(
  tool: ToolSupportItem,
  onToggleTool: (name: string, enabled: boolean) => void,
) {
  return render(
    <ToolLibraryTab
      allTools={[tool]}
      groupedTools={[["automation", [tool]]]}
      totalFilteredCount={1}
      searchQuery=""
      statusFilter="all"
      isLoading={false}
      hasError={false}
      pendingToolName={null}
      onSearchQueryChange={vi.fn()}
      onStatusFilterChange={vi.fn()}
      onOpenThreadPolicySettings={vi.fn()}
      onOpenWebSearchSettings={vi.fn()}
      onToggleTool={onToggleTool}
    />,
  )
}
