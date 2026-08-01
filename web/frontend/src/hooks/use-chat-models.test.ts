import { act, renderHook, waitFor } from "@testing-library/react"
import { beforeEach, describe, expect, it, vi } from "vitest"

import { type ModelInfo, getModels, setDefaultSelection } from "@/api/models"
import { useChatModels } from "@/hooks/use-chat-models"

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}))

vi.mock("sonner", () => ({
  toast: { error: vi.fn() },
}))

vi.mock("@/api/models", () => ({
  getModels: vi.fn(),
  setDefaultSelection: vi.fn(),
}))

vi.mock("@/lib/restart-required", () => ({
  showSaveSuccessOrRestartToast: vi.fn(),
}))

vi.mock("@/store/gateway", () => ({
  refreshGatewayState: vi.fn(),
}))

function model(overrides: Partial<ModelInfo>): ModelInfo {
  return {
    index: 0,
    model_name: "openai-work",
    model: "",
    api_key: "",
    enabled: true,
    available: true,
    status: "available",
    is_default: false,
    is_virtual: false,
    ...overrides,
  }
}

describe("useChatModels", () => {
  beforeEach(() => {
    vi.mocked(getModels).mockReset()
    vi.mocked(setDefaultSelection).mockReset()
    vi.mocked(setDefaultSelection).mockResolvedValue({ status: "ok" })
  })

  it("selects a configured account and alias without discovering raw models", async () => {
    vi.mocked(getModels).mockResolvedValue({
      models: [
        model({ model_name: "openai-work", provider: "openai" }),
        model({
          index: 1,
          model_name: "credential:github-copilot:personal",
          provider: "github-copilot",
          credential_id: "github-copilot:personal",
          is_virtual: true,
        }),
        model({
          index: 2,
          model_name: "router-1",
          provider: "router",
          router: { name: "router-1", enabled: true },
          is_virtual: true,
        }),
        model({
          index: 3,
          model_name: "task-router",
          provider: "model-router",
          model_router: { name: "task-router", enabled: true },
          is_virtual: true,
        }),
      ],
      model_aliases: [
        {
          name: "coding",
          model: "gpt-5.4",
          account_overrides: {
            "credential:github-copilot:personal": "claude-sonnet-4.5",
          },
        },
      ],
      total: 4,
      default_account_ref: "router-1",
      default_model: "coding",
      revision: "models-revision-1",
      provider_options: [],
    })

    const { result } = renderHook(() => useChatModels({ isConnected: true }))

    await waitFor(() =>
      expect(result.current.selectedModelAlias).toBe("coding"),
    )

    expect(result.current.selectedAccountName).toBe("router-1")
    expect(
      result.current.accountModels.map((item) => item.accountName),
    ).toEqual(
      ["github-copilot:personal", "openai-work"].map((name, index) =>
        index === 0 ? `credential:${name}` : name,
      ),
    )
    expect(
      result.current.accountRouterModels.map((item) => item.model_name),
    ).toEqual(["router-1"])
    expect(result.current.aliasOptions.map((item) => item.name)).toEqual([
      "coding",
      "task-router",
    ])
  })

  it("updates account and alias atomically", async () => {
    vi.mocked(getModels).mockResolvedValue({
      models: [
        model({ model_name: "openai-work" }),
        model({ index: 1, model_name: "openai-backup" }),
      ],
      model_aliases: [
        { name: "coding", model: "gpt-5.4" },
        { name: "fast", model: "gpt-4.1-mini" },
      ],
      total: 2,
      default_account_ref: "openai-work",
      default_model: "coding",
      revision: "models-revision-1",
      provider_options: [],
    })

    const { result } = renderHook(() => useChatModels({ isConnected: true }))
    await waitFor(() =>
      expect(result.current.selectedAccountName).toBe("openai-work"),
    )

    act(() => result.current.handleSetModelAlias("fast"))
    await waitFor(() =>
      expect(setDefaultSelection).toHaveBeenCalledWith("openai-work", "fast"),
    )

    act(() => result.current.handleSetAccount("openai-backup"))
    await waitFor(() =>
      expect(setDefaultSelection).toHaveBeenCalledWith("openai-backup", "fast"),
    )
  })

  it("rolls back both valid optimistic selectors when the pair is rejected", async () => {
    vi.mocked(getModels).mockResolvedValue({
      models: [
        model({ model_name: "openai-work", provider: "openai" }),
        model({
          index: 1,
          model_name: "anthropic-work",
          provider: "anthropic",
        }),
      ],
      model_aliases: [
        { name: "coding", model: "openai/gpt-5.4" },
        { name: "fast", model: "openai/gpt-5.4-mini" },
      ],
      total: 2,
      default_account_ref: "openai-work",
      default_model: "coding",
      revision: "models-revision-1",
      provider_options: [],
    })

    const pending: Array<(error: Error) => void> = []
    vi.mocked(setDefaultSelection).mockImplementation(
      () =>
        new Promise((_, reject) => {
          pending.push(reject)
        }),
    )

    const { result } = renderHook(() => useChatModels({ isConnected: true }))
    await waitFor(() =>
      expect(result.current.selectedAccountName).toBe("openai-work"),
    )

    act(() => result.current.handleSetAccount("anthropic-work"))
    await waitFor(() =>
      expect(setDefaultSelection).toHaveBeenCalledWith(
        "anthropic-work",
        "coding",
      ),
    )

    act(() => result.current.handleSetModelAlias("fast"))
    await waitFor(() =>
      expect(setDefaultSelection).toHaveBeenLastCalledWith(
        "anthropic-work",
        "fast",
      ),
    )
    expect(result.current.selectedAccountName).toBe("anthropic-work")
    expect(result.current.selectedModelAlias).toBe("fast")

    await act(async () => {
      pending[1](new Error("model alias is incompatible with account"))
      await Promise.resolve()
    })

    await waitFor(() => {
      expect(result.current.selectedAccountName).toBe("openai-work")
      expect(result.current.selectedModelAlias).toBe("coding")
    })

    await act(async () => {
      pending[0](new Error("stale request"))
      await Promise.resolve()
    })
    expect(result.current.selectedAccountName).toBe("openai-work")
    expect(result.current.selectedModelAlias).toBe("coding")
  })

  it("does not invent a selection when no defaults are configured", async () => {
    vi.mocked(getModels).mockResolvedValue({
      models: [model({ model_name: "openai-work" })],
      model_aliases: [{ name: "coding", model: "gpt-5.4" }],
      total: 1,
      default_account_ref: "",
      default_model: "",
      revision: "models-revision-1",
      provider_options: [],
    })

    const { result } = renderHook(() => useChatModels({ isConnected: true }))
    await waitFor(() => expect(result.current.hasAvailableModels).toBe(true))

    expect(result.current.selectedAccountName).toBe("")
    expect(result.current.selectedModelAlias).toBe("")
    expect(setDefaultSelection).not.toHaveBeenCalled()
  })

  it("uses the configured chat alias locally when the persisted alias default is empty", async () => {
    vi.mocked(getModels).mockResolvedValue({
      models: [
        model({
          model_name: "router-1",
          provider: "router",
          router: { name: "router-1", enabled: true },
          is_virtual: true,
        }),
      ],
      model_aliases: [
        { name: "chat", model: "gpt-5.6-sol" },
        { name: "code", model: "gpt-5.6-sol" },
      ],
      total: 1,
      default_account_ref: "router-1",
      default_model: "",
      revision: "models-revision-1",
      provider_options: [],
    })

    const { result } = renderHook(() => useChatModels({ isConnected: true }))

    await waitFor(() => expect(result.current.selectedModelAlias).toBe("chat"))
    expect(result.current.selectedAccountName).toBe("router-1")
    expect(result.current.defaultModelName).toBe("")
    expect(setDefaultSelection).not.toHaveBeenCalled()
  })
})
