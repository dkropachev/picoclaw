import { render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { beforeEach, describe, expect, it, vi } from "vitest"

import {
  type ModelInfo,
  addModel,
  getCatalogs,
  setDefaultAccount,
  setDefaultModelAlias,
  updateModel,
} from "@/api/models"
import { refreshGatewayState } from "@/store/gateway"

import { AddModelSheet } from "./add-model-sheet"
import { ModelRouterSheet } from "./model-router-sheet"

vi.mock("@/api/models", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/models")>()
  return {
    ...actual,
    addModel: vi.fn(),
    getCatalogs: vi.fn(),
    setDefaultAccount: vi.fn(),
    setDefaultModelAlias: vi.fn(),
    updateModel: vi.fn(),
  }
})

vi.mock("@/store/gateway", () => ({
  refreshGatewayState: vi.fn(),
}))

vi.mock("@/lib/restart-required", () => ({
  showSaveSuccessOrRestartToast: vi.fn(),
}))

vi.mock("./provider-combobox", () => ({
  ProviderCombobox: ({
    onChange,
  }: {
    onChange: (provider: string) => void
  }) => (
    <button type="button" onClick={() => onChange("openai")}>
      Choose OpenAI
    </button>
  ),
}))

const mockedAddModel = vi.mocked(addModel)
const mockedGetCatalogs = vi.mocked(getCatalogs)
const mockedRefreshGatewayState = vi.mocked(refreshGatewayState)
const mockedSetDefaultAccount = vi.mocked(setDefaultAccount)
const mockedSetDefaultModelAlias = vi.mocked(setDefaultModelAlias)
const mockedUpdateModel = vi.mocked(updateModel)

beforeEach(() => {
  vi.clearAllMocks()
  mockedAddModel.mockResolvedValue({ status: "ok" })
  mockedUpdateModel.mockResolvedValue({ status: "ok" })
  mockedGetCatalogs.mockResolvedValue({ entries: [], total: 0 })
  mockedRefreshGatewayState.mockResolvedValue({
    status: "running",
    canStart: true,
    restartRequired: false,
  })
})

describe("atomic model default mutations", () => {
  it("adds an account and selects it with one model mutation", async () => {
    const user = userEvent.setup()
    const onSaved = vi.fn()
    const onClose = vi.fn()

    render(
      <AddModelSheet
        open
        existingModelNames={[]}
        providerOptions={[
          {
            id: "openai",
            display_name: "OpenAI",
            default_api_base: "https://api.openai.com/v1",
            empty_api_key_allowed: true,
            create_allowed: true,
          },
        ]}
        onSaved={onSaved}
        onClose={onClose}
      />,
    )

    await user.click(screen.getByRole("button", { name: "Choose OpenAI" }))
    await user.type(
      screen.getByPlaceholderText("e.g. openai-work"),
      "openai-second",
    )
    await user.click(screen.getByRole("switch", { name: "Default Account" }))
    await user.click(screen.getByRole("button", { name: "Add Account" }))

    await waitFor(() => expect(mockedAddModel).toHaveBeenCalledTimes(1))
    expect(mockedAddModel).toHaveBeenCalledWith(
      expect.objectContaining({
        model_name: "openai-second",
        provider: "openai",
        set_as_default: true,
      }),
    )
    expect(mockedSetDefaultAccount).not.toHaveBeenCalled()
    expect(onSaved).toHaveBeenCalledTimes(1)
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it("updates a default model router with one model mutation", async () => {
    const user = userEvent.setup()
    const onSaved = vi.fn()
    const onClose = vi.fn()
    const routerModel: ModelInfo = {
      index: 2,
      model_name: "task-router",
      provider: "model-router",
      model: "task-router",
      api_key: "",
      enabled: true,
      available: true,
      status: "available",
      is_default: true,
      is_virtual: true,
      model_router: {
        name: "task-router",
        enabled: true,
        entry: "entry",
        blocks: [
          {
            id: "entry",
            type: "rules",
            rules: [],
            fallback: "default-coding",
          },
          {
            id: "default-coding",
            type: "model",
            model: "coding",
          },
        ],
      },
    }

    render(
      <ModelRouterSheet
        open
        model={routerModel}
        revision="revision-7"
        models={[routerModel]}
        modelAliases={[{ name: "coding", model: "openai/gpt-5.4" }]}
        defaultModelName="task-router"
        onSaved={onSaved}
        onClose={onClose}
      />,
    )

    await user.click(screen.getByRole("button", { name: "Save" }))

    await waitFor(() => expect(mockedUpdateModel).toHaveBeenCalledTimes(1))
    expect(mockedUpdateModel).toHaveBeenCalledWith(
      2,
      "revision-7",
      expect.objectContaining({
        model_name: "task-router",
        provider: "model-router",
        set_as_default: true,
      }),
    )
    expect(mockedSetDefaultModelAlias).not.toHaveBeenCalled()
    expect(onSaved).toHaveBeenCalledTimes(1)
    expect(onClose).toHaveBeenCalledTimes(1)
  })
})
