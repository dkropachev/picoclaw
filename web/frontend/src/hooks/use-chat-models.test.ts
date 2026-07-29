import { act, renderHook, waitFor } from "@testing-library/react"
import { toast } from "sonner"
import { beforeEach, describe, expect, it, vi } from "vitest"

import {
  type ModelInfo,
  fetchUpstreamModels,
  getModels,
  setDefaultModel,
} from "@/api/models"
import { useChatModels } from "@/hooks/use-chat-models"

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}))

vi.mock("sonner", () => ({
  toast: { error: vi.fn(), warning: vi.fn() },
}))

vi.mock("@/api/models", () => ({
  fetchUpstreamModels: vi.fn(),
  getModels: vi.fn(),
  setDefaultModel: vi.fn(),
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
    model_name: "model",
    model: "model",
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
    vi.mocked(fetchUpstreamModels).mockReset()
    vi.mocked(setDefaultModel).mockReset()
    vi.mocked(toast.error).mockReset()
    vi.mocked(toast.warning).mockReset()
    vi.mocked(fetchUpstreamModels).mockResolvedValue({
      models: [{ id: "gpt-5.5" }],
      total: 1,
    })
  })

  it("keeps accounts and account routers selectable and excludes model routers", async () => {
    vi.mocked(fetchUpstreamModels).mockResolvedValue({
      models: [{ id: "gpt-5.5" }],
      total: 1,
      issues: [
        {
          account_ref: "credential:openai:backup",
          error: "account unavailable",
        },
      ],
    })
    vi.mocked(getModels).mockResolvedValue({
      models: [
        model({
          index: 0,
          model_name: "router-1",
          provider: "router",
          model: "",
          router: {
            name: "router-1",
            enabled: true,
            entry: "account",
            blocks: [
              {
                id: "account",
                type: "account",
                account: "credential:openai:work",
              },
            ],
          },
          is_virtual: true,
        }),
        model({
          index: 1,
          model_name: "internal-virtual",
          is_virtual: true,
        }),
        model({
          index: 2,
          model_name: "gpt-api",
          provider: "openai",
          available: false,
          status: "unconfigured",
        }),
        model({
          index: 3,
          model_name: "credential:openai:work",
          provider: "openai",
          auth_method: "oauth",
          credential_id: "openai:work",
          is_virtual: true,
        }),
        model({
          index: 4,
          model_name: "credential:github-copilot:gh-copilot",
          provider: "github-copilot",
          auth_method: "token",
          credential_id: "github-copilot:gh-copilot",
          is_virtual: true,
        }),
        model({
          index: 5,
          model_name: "task-router",
          provider: "model-router",
          model_router: {
            name: "task-router",
            enabled: true,
            entry: "entry",
            blocks: [
              {
                id: "entry",
                type: "model",
                model: "credential:openai:work",
              },
            ],
          },
          is_virtual: true,
        }),
      ],
      total: 3,
      default_model: "router-1",
      provider_options: [],
    })

    const { result } = renderHook(() => useChatModels({ isConnected: true }))

    await waitFor(() => expect(result.current.selectedModelID).toBe("gpt-5.5"))

    expect(result.current.accountModels.map((m) => m.accountName)).toEqual([
      "credential:github-copilot:gh-copilot",
      "credential:openai:work",
    ])
    expect(result.current.accountRouterModels.map((m) => m.model_name)).toEqual(
      ["router-1"],
    )
    expect(result.current.hasAvailableModels).toBe(true)
    expect(result.current.selectedAccountName).toBe("router-1")
    expect(result.current.selectedModelID).toBe("gpt-5.5")
    expect(fetchUpstreamModels).toHaveBeenCalledWith({
      account_ref: "router-1",
    })
    expect(toast.warning).toHaveBeenCalledWith("chat.modelDiscoveryWarning", {
      description: "openai:backup: account unavailable",
    })
  })

  it("surfaces router discovery failures and retries without using the router alias as a model", async () => {
    vi.mocked(fetchUpstreamModels).mockRejectedValueOnce(
      new Error("model discovery failed"),
    )
    vi.mocked(getModels).mockResolvedValue({
      models: [
        model({
          model_name: "router-1",
          provider: "router",
          model: "router-1",
          router: {
            name: "router-1",
            enabled: true,
            entry: "account",
            blocks: [
              {
                id: "account",
                type: "account",
                account: "credential:openai:work",
              },
            ],
          },
          is_virtual: true,
        }),
      ],
      total: 1,
      default_model: "router-1",
      provider_options: [],
    })

    const { result } = renderHook(() => useChatModels({ isConnected: true }))

    await waitFor(() => {
      expect(fetchUpstreamModels).toHaveBeenCalledWith({
        account_ref: "router-1",
      })
      expect(result.current.isLoadingModelOptions).toBe(false)
    })

    expect(result.current.selectedAccountName).toBe("router-1")
    expect(result.current.selectedModelID).toBe("")
    expect(result.current.modelOptions).toEqual([])
    expect(result.current.modelDiscoveryError).toBe("model discovery failed")
    expect(toast.error).toHaveBeenCalledWith("chat.modelDiscoveryError", {
      description: "model discovery failed",
    })

    act(() => {
      result.current.retryModelDiscovery()
    })

    await waitFor(() => {
      expect(fetchUpstreamModels).toHaveBeenCalledTimes(2)
      expect(result.current.selectedModelID).toBe("gpt-5.5")
    })
    expect(result.current.modelDiscoveryError).toBeNull()
  })

  it("treats an empty all-failed router response as retryable discovery error", async () => {
    vi.mocked(fetchUpstreamModels).mockResolvedValue({
      models: [],
      total: 0,
      issues: [
        {
          account_ref: "credential:openai:work",
          error: "account unavailable",
        },
      ],
    })
    vi.mocked(getModels).mockResolvedValue({
      models: [
        model({
          model_name: "router-1",
          provider: "router",
          model: "",
          router: {
            name: "router-1",
            enabled: true,
            entry: "account",
            blocks: [
              {
                id: "account",
                type: "account",
                account: "credential:openai:work",
              },
            ],
          },
          is_virtual: true,
        }),
      ],
      total: 1,
      default_model: "router-1",
      provider_options: [],
    })

    const { result } = renderHook(() => useChatModels({ isConnected: true }))

    await waitFor(() => {
      expect(result.current.isLoadingModelOptions).toBe(false)
      expect(result.current.modelDiscoveryError).toBe(
        "openai:work: account unavailable",
      )
    })

    expect(result.current.selectedModelID).toBe("")
    expect(result.current.modelOptions).toEqual([])
    expect(toast.error).toHaveBeenCalledWith("chat.modelDiscoveryError", {
      description: "openai:work: account unavailable",
    })
    expect(toast.warning).not.toHaveBeenCalled()
  })

  it("preserves the configured model through initial credential discovery", async () => {
    vi.mocked(fetchUpstreamModels).mockResolvedValue({
      models: [{ id: "gpt-first" }, { id: "gpt-configured" }],
      total: 2,
    })
    vi.mocked(getModels).mockResolvedValue({
      models: [
        model({
          index: 0,
          model_name: "credential:openai:work",
          model: "gpt-first",
          provider: "openai",
          auth_method: "oauth",
          credential_id: "openai:work",
          is_virtual: true,
        }),
        model({
          index: 1,
          model_name: "work-codex",
          model: "gpt-configured",
          provider: "openai",
          auth_method: "oauth",
          credential_id: "openai:work",
        }),
      ],
      total: 2,
      default_model: "work-codex",
      provider_options: [],
    })

    const { result } = renderHook(() => useChatModels({ isConnected: true }))

    await waitFor(() => {
      expect(fetchUpstreamModels).toHaveBeenCalledWith({
        account_ref: "credential:openai:work",
      })
      expect(result.current.isLoadingModelOptions).toBe(false)
    })

    expect(result.current.selectedAccountName).toBe("credential:openai:work")
    expect(result.current.selectedModelID).toBe("gpt-configured")
  })

  it("preserves a second configured credential default after hard discovery failure", async () => {
    vi.mocked(fetchUpstreamModels).mockRejectedValue(
      new Error("discovery unavailable"),
    )
    vi.mocked(getModels).mockResolvedValue({
      models: [
        model({
          index: 0,
          model_name: "credential:openai:work",
          model: "gpt-first",
          provider: "openai",
          auth_method: "oauth",
          credential_id: "openai:work",
          is_virtual: true,
        }),
        model({
          index: 1,
          model_name: "work-codex",
          model: "gpt-configured",
          provider: "openai",
          auth_method: "oauth",
          credential_id: "openai:work",
        }),
      ],
      total: 2,
      default_model: "work-codex",
      provider_options: [],
    })

    const { result } = renderHook(() => useChatModels({ isConnected: true }))

    await waitFor(() => {
      expect(result.current.isLoadingModelOptions).toBe(false)
      expect(result.current.modelDiscoveryError).toBe("discovery unavailable")
    })

    expect(result.current.selectedModelID).toBe("gpt-configured")
    expect(result.current.modelOptions).toEqual([{ id: "gpt-configured" }])
  })

  it.each([
    {
      provider: "anthropic",
      credentialID: "anthropic:work",
      modelID: "claude-sonnet-4.6",
    },
    {
      provider: "antigravity",
      credentialID: "antigravity:work",
      modelID: "gemini-3-flash",
    },
  ])(
    "keeps a configured $provider credential fallback usable after hard discovery failure",
    async ({ provider, credentialID, modelID }) => {
      vi.mocked(fetchUpstreamModels).mockRejectedValue(
        new Error("discovery unavailable"),
      )
      vi.mocked(getModels).mockResolvedValue({
        models: [
          model({
            model_name: `${provider}-work`,
            model: modelID,
            provider,
            auth_method: "oauth",
            credential_id: credentialID,
          }),
        ],
        total: 1,
        default_model: `${provider}-work`,
        provider_options: [],
      })

      const { result } = renderHook(() => useChatModels({ isConnected: true }))

      await waitFor(() => {
        expect(fetchUpstreamModels).toHaveBeenCalledWith({
          account_ref: `credential:${credentialID}`,
        })
        expect(result.current.isLoadingModelOptions).toBe(false)
      })

      expect(result.current.selectedModelID).toBe(modelID)
      expect(result.current.modelOptions).toEqual([{ id: modelID }])
      expect(result.current.modelDiscoveryError).toBe("discovery unavailable")
    },
  )

  it.each([
    {
      kind: "API-key",
      configuredModel: model({
        model_name: "gpt-api",
        model: "gpt-4.1",
        provider: "openai",
      }),
      expectedModelID: "gpt-4.1",
    },
    {
      kind: "local",
      configuredModel: model({
        model_name: "local-model",
        model: "llama3.3",
        auth_method: "local",
        api_base: "http://localhost:11434",
      }),
      expectedModelID: "llama3.3",
    },
    {
      kind: "model-router",
      configuredModel: model({
        model_name: "task-router",
        model: "task-router",
        provider: "model-router",
        is_virtual: true,
        model_router: {
          name: "task-router",
          enabled: true,
          entry: "entry",
          blocks: [{ id: "entry", type: "model", model: "gpt-api" }],
        },
      }),
      expectedModelID: "task-router",
    },
  ])(
    "retains a configured $kind default when no account is selected",
    async ({ configuredModel, expectedModelID }) => {
      vi.mocked(getModels).mockResolvedValue({
        models: [configuredModel],
        total: 1,
        default_model: configuredModel.model_name,
        provider_options: [],
      })

      const { result } = renderHook(() => useChatModels({ isConnected: true }))

      await waitFor(() => {
        expect(result.current.selectedModelID).toBe(expectedModelID)
      })

      expect(result.current.selectedAccountName).toBe("")
      expect(result.current.accountModels).toEqual([])
      expect(result.current.accountRouterModels).toEqual([])
      expect(result.current.hasAvailableModels).toBe(true)
      expect(fetchUpstreamModels).not.toHaveBeenCalled()
    },
  )

  it("does not expose API-key or local models in chat selector groups", async () => {
    vi.mocked(getModels).mockResolvedValue({
      models: [
        model({
          index: 0,
          model_name: "gpt-api",
          model: "gpt-4.1",
          provider: "openai",
        }),
        model({
          index: 1,
          model_name: "local-model",
          auth_method: "local",
          api_base: "http://localhost:11434",
        }),
      ],
      total: 2,
      default_model: "gpt-api",
      provider_options: [],
    })

    const { result } = renderHook(() => useChatModels({ isConnected: true }))

    await waitFor(() => {
      expect(result.current.defaultModelName).toBe("gpt-api")
    })

    expect(result.current.accountModels).toEqual([])
    expect(result.current.accountRouterModels).toEqual([])
    expect(result.current.hasAvailableModels).toBe(true)
    expect(result.current.selectedModelID).toBe("gpt-4.1")
  })
})
