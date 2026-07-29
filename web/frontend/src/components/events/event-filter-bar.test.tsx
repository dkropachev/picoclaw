import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { describe, expect, it, vi } from "vitest"

import { EventFilterBar } from "./event-filter-bar"

describe("EventFilterBar", () => {
  it("rejects multibyte filters beyond the gateway byte contract", async () => {
    const onApply = vi.fn()
    const user = userEvent.setup()
    render(
      <EventFilterBar
        filters={{
          source: "",
          connector: "",
          type: "",
          routingStatus: "",
        }}
        onApply={onApply}
        onReset={vi.fn()}
      />,
    )

    await user.type(screen.getByLabelText("Source"), "é".repeat(65))
    await user.click(screen.getByRole("button", { name: "Apply filters" }))

    expect(screen.getByRole("alert")).toHaveTextContent(
      "Source must not exceed 128 UTF-8 bytes.",
    )
    expect(onApply).not.toHaveBeenCalled()

    await user.clear(screen.getByLabelText("Source"))
    await user.type(screen.getByLabelText("Source"), "github")
    await user.click(screen.getByRole("button", { name: "Apply filters" }))
    expect(onApply).toHaveBeenCalledWith({
      source: "github",
      connector: "",
      type: "",
      routingStatus: "",
    })
  })
})
