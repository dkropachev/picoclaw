import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { describe, expect, it, vi } from "vitest"

import { CollectionSelectionBar } from "@/components/collection/collection-selection-bar"
import { CollectionToolbar } from "@/components/collection/collection-toolbar"

describe("collection toolbar", () => {
  it("applies recent queries, clears history, and switches supported views", async () => {
    const user = userEvent.setup()
    const onApplyQuery = vi.fn()
    const onClearHistory = vi.fn()
    const onViewChange = vi.fn()
    render(
      <CollectionToolbar
        activeQuery="status = ready"
        defaultQuery="ORDER BY updated DESC"
        onApplyQuery={onApplyQuery}
        view="list"
        supportedViews={["list", "table"]}
        recentQueries={["status = blocked"]}
        onClearHistory={onClearHistory}
        onViewChange={onViewChange}
      />,
    )
    expect(screen.getByRole("button", { name: "List view" })).toHaveAttribute(
      "aria-pressed",
      "true",
    )
    expect(screen.queryByRole("button", { name: "Grid view" })).toBeNull()
    await user.click(screen.getByRole("button", { name: "Table view" }))
    expect(onViewChange).toHaveBeenCalledWith("table")

    await user.click(screen.getByRole("button", { name: "Recent queries" }))
    await user.click(screen.getByRole("menuitem", { name: "status = blocked" }))
    expect(onApplyQuery).toHaveBeenCalledWith("status = blocked")
    await user.click(screen.getByRole("button", { name: "Recent queries" }))
    await user.click(
      screen.getByRole("menuitem", { name: "Clear query history" }),
    )
    expect(onClearHistory).toHaveBeenCalledOnce()
  })
})

describe("CollectionSelectionBar", () => {
  it("uses the standard selected-item action area", async () => {
    const user = userEvent.setup()
    const onDelete = vi.fn()
    const onClear = vi.fn()
    const { rerender } = render(
      <CollectionSelectionBar
        selectedCount={0}
        onDelete={onDelete}
        onClear={onClear}
      />,
    )
    expect(screen.queryByText(/selected/)).toBeNull()
    rerender(
      <CollectionSelectionBar
        selectedCount={2}
        onDelete={onDelete}
        onClear={onClear}
      />,
    )
    expect(screen.getByText("2 selected")).toBeVisible()
    await user.click(screen.getByRole("button", { name: "Delete" }))
    await user.click(screen.getByRole("button", { name: "Clear selection" }))
    expect(onDelete).toHaveBeenCalledOnce()
    expect(onClear).toHaveBeenCalledOnce()
  })

  it("supports feature actions without a delete action", async () => {
    const user = userEvent.setup()
    const onClear = vi.fn()
    const onRun = vi.fn()
    render(
      <CollectionSelectionBar selectedCount={1} onClear={onClear}>
        <button type="button" onClick={onRun}>
          Run action
        </button>
      </CollectionSelectionBar>,
    )

    expect(screen.queryByRole("button", { name: "Delete" })).toBeNull()
    await user.click(screen.getByRole("button", { name: "Run action" }))
    await user.click(screen.getByRole("button", { name: "Clear selection" }))
    expect(onRun).toHaveBeenCalledOnce()
    expect(onClear).toHaveBeenCalledOnce()
  })
})
