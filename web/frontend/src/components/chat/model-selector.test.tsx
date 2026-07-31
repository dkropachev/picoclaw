import { render, screen } from "@testing-library/react"
import { describe, expect, it, vi } from "vitest"

import { ModelSelector } from "@/components/chat/model-selector"

describe("ModelSelector", () => {
  it("shows stable aliases without exposing raw upstream model IDs", () => {
    render(
      <ModelSelector
        selectedAccountName="router-1"
        selectedModelAlias="coding"
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
        aliasOptions={[
          { name: "coding", model: "gpt-5.4" },
          { name: "fast", model: "gpt-4.1-mini" },
        ]}
        isSavingSelection={false}
        onAccountChange={vi.fn()}
        onModelAliasChange={vi.fn()}
      />,
    )

    expect(screen.getByRole("combobox", { name: "Account" })).toHaveTextContent(
      "router-1",
    )
    const aliasSelect = screen.getByRole("combobox", { name: "Model alias" })
    expect(aliasSelect).toHaveTextContent("coding")
    expect(screen.queryByText("gpt-5.4")).not.toBeInTheDocument()
  })
})
