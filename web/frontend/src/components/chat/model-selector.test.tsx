import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { describe, expect, it, vi } from "vitest"

import { ModelSelector } from "@/components/chat/model-selector"

describe("ModelSelector", () => {
  it("offers an explicit retry control after model discovery fails", async () => {
    const user = userEvent.setup()
    const onRetryModelDiscovery = vi.fn()

    render(
      <ModelSelector
        selectedAccountName="router-1"
        selectedModelID=""
        accountModels={[]}
        accountRouterModels={[
          {
            index: 0,
            model_name: "router-1",
            model: "",
            api_key: "",
            provider: "router",
            enabled: true,
            available: true,
            status: "available",
            is_default: true,
            is_virtual: true,
          },
        ]}
        modelOptions={[]}
        isLoadingModelOptions={false}
        modelDiscoveryError="network unavailable"
        onAccountChange={vi.fn()}
        onModelChange={vi.fn()}
        onRetryModelDiscovery={onRetryModelDiscovery}
      />,
    )

    const retryButton = screen.getByRole("button", {
      name: "Retry model discovery",
    })
    expect(retryButton).toHaveAttribute(
      "title",
      "Retry model discovery: network unavailable",
    )

    await user.click(retryButton)

    expect(onRetryModelDiscovery).toHaveBeenCalledOnce()
  })
})
