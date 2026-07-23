import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { describe, expect, it, vi } from "vitest"

import type { ModelInfo } from "@/api/models"
import { TooltipProvider } from "@/components/ui/tooltip"

import { ModelCard } from "./model-card"

const routerModel: ModelInfo = {
  index: 2,
  model_name: "router-main",
  provider: "router",
  model: "router-main",
  api_key: "",
  router: {
    enabled: true,
    entry: "primary",
    blocks: [{ id: "primary", type: "account", account: "account" }],
  },
  enabled: true,
  available: true,
  status: "available",
  is_default: false,
  is_virtual: false,
  default_model_allowed: false,
}

function renderRouterCard(
  model: ModelInfo,
  onDelete: (model: ModelInfo) => void = vi.fn(),
) {
  render(
    <TooltipProvider>
      <ModelCard
        model={model}
        onEdit={vi.fn()}
        onSetDefault={vi.fn()}
        onDelete={onDelete}
        settingDefault={false}
      />
    </TooltipProvider>,
  )
}

function getDeleteButton() {
  const buttons = screen.getAllByRole("button", {
    name: "Delete account router",
  })
  const button = buttons.find((item) => item.tagName.toLowerCase() === "button")
  if (!button) throw new Error("delete button not found")
  return button
}

describe("ModelCard router actions", () => {
  it("allows deleting a non-default router entry", async () => {
    const user = userEvent.setup()
    const onDelete = vi.fn()
    renderRouterCard(routerModel, onDelete)

    await user.click(getDeleteButton())

    expect(onDelete).toHaveBeenCalledWith(routerModel)
  })

  it("disables deleting the default router entry", () => {
    const onDelete = vi.fn()
    renderRouterCard({ ...routerModel, is_default: true }, onDelete)

    expect(getDeleteButton()).toBeDisabled()
    expect(onDelete).not.toHaveBeenCalled()
  })
})
